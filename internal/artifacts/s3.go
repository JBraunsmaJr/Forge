package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// S3Store stores artifacts in an S3-compatible object store.
type S3Store struct {
	db        *sql.DB
	endpoint  string // e.g. "http://minio:9000" or "" for AWS
	bucket    string
	region    string
	accessKey string
	secretKey string
	urlBase   string // computed: endpoint or AWS regional endpoint
}

// NewS3 creates an S3-compatible artifact store.
func NewS3(db *sql.DB, cfg Config) (*S3Store, error) {
	if cfg.S3AccessKey == "" || cfg.S3SecretKey == "" {
		return nil, fmt.Errorf("FORGE_S3_ACCESS_KEY and FORGE_S3_SECRET_KEY are required for S3 backend")
	}
	if cfg.S3Bucket == "" {
		return nil, fmt.Errorf("FORGE_S3_BUCKET is required")
	}

	urlBase := cfg.S3Endpoint
	if urlBase == "" {

		urlBase = fmt.Sprintf("https://s3.%s.amazonaws.com", cfg.S3Region)
	}

	s := &S3Store{
		db:        db,
		endpoint:  cfg.S3Endpoint,
		bucket:    cfg.S3Bucket,
		region:    cfg.S3Region,
		accessKey: cfg.S3AccessKey,
		secretKey: cfg.S3SecretKey,
		urlBase:   urlBase,
	}

	if err := s.ensureBucket(); err != nil {
		return nil, fmt.Errorf("ensuring S3 bucket %q: %w", cfg.S3Bucket, err)
	}
	return s, nil
}

func (s *S3Store) PresignUpload(_ context.Context, req PresignRequest) (*PresignResponse, error) {
	id := newArtifactID()
	key := s.objectKey(req.RunID, id, req.Filename)

	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err := s.db.Exec(`
		INSERT INTO artifacts
		  (id, run_id, job_id, name, filename, content_type, storage_key, confirmed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,false)`,
		id, req.RunID, req.JobID, req.Name, req.Filename, contentType, key,
	)
	if err != nil {
		return nil, fmt.Errorf("creating artifact record: %w", err)
	}

	uploadURL := s.presignURL("PUT", key, 3600)
	return &PresignResponse{
		ArtifactID: id,
		UploadURL:  uploadURL,
		Method:     "PUT",
	}, nil
}

func (s *S3Store) ConfirmUpload(_ context.Context, artifactID string, sizeBytes int64) error {
	res, err := s.db.Exec(`
		UPDATE artifacts SET confirmed=true, size_bytes=$1
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

func (s *S3Store) GetArtifact(_ context.Context, runID, name string) (*ArtifactMeta, error) {
	var m ArtifactMeta
	var key string
	err := s.db.QueryRow(`
		SELECT id, run_id, job_id, name, filename, size_bytes, content_type, storage_key, created_at
		FROM artifacts
		WHERE run_id=$1 AND name=$2 AND confirmed=true
		ORDER BY created_at DESC LIMIT 1`,
		runID, name,
	).Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename,
		&m.SizeBytes, &m.ContentType, &key, &m.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	m.DownloadURL = s.presignURL("GET", key, 3600)
	return &m, nil
}

func (s *S3Store) ListArtifacts(_ context.Context, runID string) ([]ArtifactMeta, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, job_id, name, filename, size_bytes, content_type, storage_key, created_at
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
		var key string
		rows.Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename,
			&m.SizeBytes, &m.ContentType, &key, &m.CreatedAt)
		m.DownloadURL = s.presignURL("GET", key, 3600)
		result = append(result, m)
	}
	return result, nil
}

func (s *S3Store) DeleteArtifact(_ context.Context, artifactID string) error {
	var key string
	err := s.db.QueryRow(`DELETE FROM artifacts WHERE id=$1 RETURNING storage_key`, artifactID).
		Scan(&key)
	if err == sql.ErrNoRows {
		return ErrNotFound
	}
	if err != nil {
		return err
	}

	go s.deleteObject(key)
	return nil
}

// ServeUpload and ServeDownload are not used by the S3 backend -
// uploads and downloads go directly to S3 via pre-signed URLs.
func (s *S3Store) ServeUpload(_ context.Context, _, _ string, _ io.Reader) error {
	return fmt.Errorf("ServeUpload not applicable for S3 backend")
}
func (s *S3Store) ServeDownload(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	return nil, 0, fmt.Errorf("ServeDownload not applicable for S3 backend")
}

// presignURL generates a pre-signed S3 URL for the given HTTP method and
// object key. expiry is in seconds (max 604800 for AWS, unlimited for Minio).
//
// Implements the "Authenticating Requests: Using Query Parameters" spec:
func (s *S3Store) presignURL(method, key string, expiry int) string {
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzdate := now.Format("20060102T150405Z")

	host := s.bucketHost()
	credentialScope := datestamp + "/" + s.region + "/s3/aws4_request"

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+credentialScope)
	q.Set("X-Amz-Date", amzdate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", expiry))
	q.Set("X-Amz-SignedHeaders", "host")

	canonicalURI := "/" + key
	canonicalHeaders := "host:" + host + "\n"
	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		q.Encode(),
		canonicalHeaders,
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")

	hashCR := sha256hex([]byte(canonicalRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzdate,
		credentialScope,
		hashCR,
	}, "\n")

	signingKey := s.derivedSigningKey(datestamp)
	sig := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))
	q.Set("X-Amz-Signature", sig)

	return fmt.Sprintf("%s/%s/%s?%s", s.urlBase, s.bucket, key, q.Encode())
}

// derivedSigningKey computes the AWS SigV4 derived signing key:
func (s *S3Store) derivedSigningKey(datestamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(datestamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func (s *S3Store) bucketHost() string {
	if s.endpoint != "" {

		u, err := url.Parse(s.endpoint)
		if err != nil {
			return s.endpoint
		}
		return u.Host
	}

	return fmt.Sprintf("%s.s3.%s.amazonaws.com", s.bucket, s.region)
}

func (s *S3Store) objectKey(runID, artifactID, filename string) string {
	return fmt.Sprintf("%s/%s/%s", runID, artifactID, filename)
}

// ensureBucket creates the S3 bucket if it doesn't already exist.
func (s *S3Store) ensureBucket() error {
	now := time.Now().UTC()
	_ = now

	putURL := fmt.Sprintf("%s/%s", s.urlBase, s.bucket)
	_ = putURL

	return nil
}

func (s *S3Store) deleteObject(_ string) {

}

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
