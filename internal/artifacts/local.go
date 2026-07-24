package artifacts

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// LocalStore stores artifacts on the scheduler's local filesystem.
type LocalStore struct {
	db      *sql.DB
	dir     string // root storage directory
	baseURL string // scheduler's public base URL (for constructing download URLs)
}

// NewLocal creates a local filesystem artifact store.
// dir is the root directory for artifact files.
// baseURL is the scheduler's public URL (e.g. "http://localhost:8080")
func NewLocal(db *sql.DB, dir, baseURL string) (*LocalStore, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("creating artifact dir %s: %w", dir, err)
	}
	return &LocalStore{db: db, dir: dir, baseURL: baseURL}, nil
}

func (s *LocalStore) PresignUpload(_ context.Context, req PresignRequest) (*PresignResponse, error) {
	id := newArtifactID()

	b := make([]byte, 16)
	rand.Read(b)
	uploadToken := hex.EncodeToString(b)

	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err := s.db.Exec(`
		INSERT INTO artifacts
		  (id, run_id, job_id, name, filename, content_type, storage_key, upload_token, confirmed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,false)`,
		id, req.RunID, req.JobID, req.Name, req.Filename,
		contentType, s.storageKey(req.RunID, id, req.Filename), uploadToken,
	)
	if err != nil {
		return nil, fmt.Errorf("creating artifact record: %w", err)
	}

	uploadURL := fmt.Sprintf("%s/api/v1/artifacts/%s/upload?token=%s",
		s.baseURL, id, uploadToken)

	return &PresignResponse{
		ArtifactID: id,
		UploadURL:  uploadURL,
		Method:     "PUT",
	}, nil
}

func (s *LocalStore) ConfirmUpload(_ context.Context, artifactID string, sizeBytes int64) error {
	res, err := s.db.Exec(`
		UPDATE artifacts
		SET confirmed=true, size_bytes=$1, upload_token=NULL
		WHERE id=$2 AND confirmed=false`,
		sizeBytes, artifactID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return fmt.Errorf("artifact %s not found or already confirmed", artifactID)
	}
	return nil
}

func (s *LocalStore) GetArtifact(_ context.Context, runID, name string) (*ArtifactMeta, error) {
	var m ArtifactMeta
	var storageKey string
	err := s.db.QueryRow(`
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, storage_key, created_at
		FROM artifacts
		WHERE run_id=$1 AND name=$2 AND confirmed=true
		ORDER BY created_at DESC LIMIT 1`,
		runID, name,
	).Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename,
		&m.SizeBytes, &m.ContentType, &storageKey, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.DownloadURL = fmt.Sprintf("%s/api/v1/artifacts/%s/download", s.baseURL, m.ID)
	return &m, nil
}

func (s *LocalStore) ListArtifacts(_ context.Context, runID string) ([]ArtifactMeta, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, created_at
		FROM artifacts
		WHERE run_id=$1 AND confirmed=true
		ORDER BY created_at`,
		runID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ArtifactMeta
	for rows.Next() {
		var m ArtifactMeta
		rows.Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename,
			&m.SizeBytes, &m.ContentType, &m.CreatedAt)
		m.DownloadURL = fmt.Sprintf("%s/api/v1/artifacts/%s/download", s.baseURL, m.ID)
		result = append(result, m)
	}
	return result, nil
}

func (s *LocalStore) DeleteArtifact(_ context.Context, artifactID string) error {
	var storageKey string
	err := s.db.QueryRow(`DELETE FROM artifacts WHERE id=$1 RETURNING storage_key`, artifactID).
		Scan(&storageKey)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, filepath.FromSlash(storageKey))
	os.Remove(path)
	os.Remove(filepath.Dir(path))
	return nil
}

func (s *LocalStore) DeleteRunArtifacts(_ context.Context, runID string) error {
	rows, err := s.db.Query(`DELETE FROM artifacts WHERE run_id=$1 RETURNING storage_key`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		rows.Scan(&key)
		path := filepath.Join(s.dir, filepath.FromSlash(key))
		os.Remove(path)
		os.Remove(filepath.Dir(path))
	}
	// Also try to remove the run directory if empty
	os.Remove(filepath.Join(s.dir, runID))
	return nil
}

// ServeUpload handles the actual PUT request for a local upload.
// Called by the scheduler's /api/v1/artifacts/{id}/upload handler.
func (s *LocalStore) ServeUpload(_ context.Context, artifactID, uploadToken string, r io.Reader) error {
	// Verify the one-time upload token.
	var storedToken, storageKey, filename string
	err := s.db.QueryRow(`
		SELECT upload_token, storage_key, filename
		FROM artifacts WHERE id=$1 AND confirmed=false`,
		artifactID,
	).Scan(&storedToken, &storageKey, &filename)
	if err == sql.ErrNoRows {
		return fmt.Errorf("artifact not found or already confirmed")
	}
	if err != nil {
		return err
	}
	if storedToken != uploadToken {
		return fmt.Errorf("invalid upload token")
	}

	path := filepath.Join(s.dir, filepath.FromSlash(storageKey))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("creating artifact directory: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating artifact file: %w", err)
	}
	defer f.Close()
	_, err = io.Copy(f, r)
	return err
}

// ServeDownload returns a reader for a local artifact download.
// Called by the scheduler's /api/v1/artifacts/{id}/download handler.
func (s *LocalStore) ServeDownload(_ context.Context, artifactID string) (io.ReadCloser, *ArtifactMeta, error) {
	var m ArtifactMeta
	var storageKey string
	err := s.db.QueryRow(`
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, storage_key, created_at
		FROM artifacts WHERE id=$1 AND confirmed=true`,
		artifactID,
	).Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename, &m.SizeBytes, &m.ContentType, &storageKey, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil, ErrNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	path := filepath.Join(s.dir, filepath.FromSlash(storageKey))
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening artifact file: %w", err)
	}
	return f, &m, nil
}

func (s *LocalStore) storageKey(runID, artifactID, filename string) string {
	return fmt.Sprintf("%s/%s/%s", runID, artifactID, filename)
}

func newArtifactID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Ensure LocalStore implements the embedded interface used by the scheduler.
var _ interface {
	PresignUpload(context.Context, PresignRequest) (*PresignResponse, error)
	ConfirmUpload(context.Context, string, int64) error
	GetArtifact(context.Context, string, string) (*ArtifactMeta, error)
	ListArtifacts(context.Context, string) ([]ArtifactMeta, error)
	DeleteArtifact(context.Context, string) error
	DeleteRunArtifacts(context.Context, string) error
	Cleanup()
	ServeUpload(context.Context, string, string, io.Reader) error
	ServeDownload(context.Context, string) (io.ReadCloser, *ArtifactMeta, error)
} = (*LocalStore)(nil)

// Cleanup runs periodically to remove unconfirmed artifacts older than 1 hour.
func (s *LocalStore) Cleanup() {
	cutoff := time.Now().Add(-1 * time.Hour)
	rows, err := s.db.Query(`
		DELETE FROM artifacts WHERE confirmed=false AND created_at < $1
		RETURNING storage_key`, cutoff)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		rows.Scan(&key)
		os.Remove(filepath.Join(s.dir, filepath.FromSlash(key)))
	}
}
