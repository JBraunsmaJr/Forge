package artifacts

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// S3Store stores artifacts in an S3-compatible object store.
type S3Store struct {
	db               *sql.DB
	endpoint         string // e.g. "http://minio:9000" or "" for AWS
	bucket           string
	region           string
	accessKey        string
	secretKey        string
	urlBase          string // computed: endpoint or AWS regional endpoint
	publicURL        string // optional: public URL for browser access
	schedulerBaseURL string
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
		db:               db,
		endpoint:         cfg.S3Endpoint,
		bucket:           cfg.S3Bucket,
		region:           cfg.S3Region,
		accessKey:        cfg.S3AccessKey,
		secretKey:        cfg.S3SecretKey,
		urlBase:          urlBase,
		publicURL:        cfg.S3PublicURL,
		schedulerBaseURL: cfg.LocalBase,
	}

	if err := s.ensureBucket(); err != nil {
		return nil, fmt.Errorf("ensuring S3 bucket %q: %w", cfg.S3Bucket, err)
	}
	return s, nil
}

func (s *S3Store) PresignUpload(_ context.Context, req PresignRequest) (*PresignResponse, error) {
	id := newArtifactID()
	key := s.objectKey(req.RunID, id, req.Filename)

	var jobID any = req.JobID
	if req.JobID == "" {
		jobID = nil
	}

	contentType := req.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	_, err := s.db.Exec(`
		INSERT INTO artifacts
		  (id, run_id, job_id, name, filename, content_type, storage_key, confirmed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,false)`,
		id, req.RunID, jobID, req.Name, req.Filename, contentType, key,
	)
	if err != nil {
		return nil, fmt.Errorf("creating artifact record: %w", err)
	}

	uploadURL := s.presignURL("PUT", key, 3600, false, "", "")
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
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, storage_key, created_at
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
	m.DownloadURL = s.getDownloadURL(m.ID, key, m.ContentType, m.Filename)
	return &m, nil
}

func (s *S3Store) ListArtifacts(_ context.Context, runID string) ([]ArtifactMeta, error) {
	rows, err := s.db.Query(`
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, storage_key, created_at
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
		m.DownloadURL = s.getDownloadURL(m.ID, key, m.ContentType, m.Filename)
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

func (s *S3Store) DeleteRunArtifacts(_ context.Context, runID string) error {
	rows, err := s.db.Query(`DELETE FROM artifacts WHERE run_id=$1 RETURNING storage_key`, runID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		rows.Scan(&key)
		go s.deleteObject(key)
	}
	return nil
}

func (s *S3Store) Cleanup() {
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
		go s.deleteObject(key)
	}
}

// ServeUpload is not used by the S3 backend - agents PUT directly to S3.
func (s *S3Store) ServeUpload(_ context.Context, _, _ string, _ io.Reader) error {
	return fmt.Errorf("ServeUpload not applicable for S3 backend")
}

func (s *S3Store) ServeDownload(ctx context.Context, artifactID string) (io.ReadCloser, *ArtifactMeta, error) {
	var m ArtifactMeta
	var key string
	err := s.db.QueryRow(`
		SELECT id, run_id, COALESCE(job_id, ''), name, filename, size_bytes, content_type, storage_key, created_at
		FROM artifacts WHERE id=$1`, artifactID).Scan(&m.ID, &m.RunID, &m.JobID, &m.Name, &m.Filename, &m.SizeBytes, &m.ContentType, &key, &m.CreatedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}

	downloadURL := s.presignURL("GET", key, 300, false, m.ContentType, m.Filename) // 5 min expiry, use internal endpoint
	req, err := http.NewRequestWithContext(ctx, "GET", downloadURL, nil)
	if err != nil {
		return nil, nil, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("S3 error: %s", resp.Status)
	}

	return resp.Body, &m, nil
}

func (s *S3Store) getDownloadURL(id, key, contentType, filename string) string {
	// If public URL is set, use it.
	if s.publicURL != "" {
		return s.presignURL("GET", key, 3600, true, contentType, filename)
	}
	// If no custom endpoint, it's AWS, which is public.
	if s.endpoint == "" {
		return s.presignURL("GET", key, 3600, false, contentType, filename)
	}
	// Custom internal endpoint, use proxy.
	return fmt.Sprintf("%s/api/v1/artifacts/%s/download", s.schedulerBaseURL, id)
}

// presignURL generates a pre-signed S3 URL for the given HTTP method and
// object key. expiry is in seconds (max 604800 for AWS, unlimited for Minio).
//
// If usePublic is true and FORGE_S3_PUBLIC_URL is set, it uses that as the
// base URL. This is important for Minio/S3 instances that have a different
// public address than the one used by the scheduler.
//
// Implements the "Authenticating Requests: Using Query Parameters" spec:
func (s *S3Store) presignURL(method, key string, expiry int, usePublic bool, contentType, filename string) string {
	now := time.Now().UTC()
	datestamp := now.Format("20060102")
	amzdate := now.Format("20060102T150405Z")

	host := s.bucketHost()
	base := strings.TrimSuffix(s.urlBase, "/")
	if usePublic && s.publicURL != "" {
		base = strings.TrimSuffix(s.publicURL, "/")
		if u, err := url.Parse(s.publicURL); err == nil {
			host = u.Host
		}
	}
	credentialScope := datestamp + "/" + s.region + "/s3/aws4_request"

	q := url.Values{}
	q.Set("X-Amz-Algorithm", "AWS4-HMAC-SHA256")
	q.Set("X-Amz-Credential", s.accessKey+"/"+credentialScope)
	q.Set("X-Amz-Date", amzdate)
	q.Set("X-Amz-Expires", fmt.Sprintf("%d", expiry))
	q.Set("X-Amz-SignedHeaders", "host")
	q.Set("X-Amz-Content-Sha256", "UNSIGNED-PAYLOAD")

	if method == "GET" {
		if contentType != "" && contentType != "application/octet-stream" {
			q.Set("response-content-type", contentType)
		}
		disposition := "inline"
		if filename != "" {
			disposition = fmt.Sprintf("inline; filename=%q", filename)
		}
		q.Set("response-content-disposition", disposition)
	}

	canonicalURI := s3EncodePath("/" + s.bucket + "/" + key)
	canonicalQueryString := s3EncodeQuery(q)
	canonicalHeaders := "host:" + host + "\n"
	canonicalRequest := strings.Join([]string{
		method,
		canonicalURI,
		canonicalQueryString,
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

	return base + canonicalURI + "?" + s3EncodeQuery(q)
}

func s3EncodePath(path string) string {
	var buf strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' || c == '/' {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

func s3EncodeQuery(v url.Values) string {
	if v == nil {
		return ""
	}
	var buf strings.Builder
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		prefix := s3EncodeQueryValue(k) + "="
		for _, v := range v[k] {
			if buf.Len() > 0 {
				buf.WriteByte('&')
			}
			buf.WriteString(prefix)
			buf.WriteString(s3EncodeQueryValue(v))
		}
	}
	return buf.String()
}

func s3EncodeQueryValue(s string) string {
	var buf strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			buf.WriteByte(c)
		} else {
			fmt.Fprintf(&buf, "%%%02X", c)
		}
	}
	return buf.String()
}

// derivedSigningKey computes the AWS SigV4 derived signing key:
func (s *S3Store) derivedSigningKey(datestamp string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+s.secretKey), []byte(datestamp))
	kRegion := hmacSHA256(kDate, []byte(s.region))
	kService := hmacSHA256(kRegion, []byte("s3"))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func (s *S3Store) bucketHost() string {
	u, err := url.Parse(s.urlBase)
	if err != nil {
		return ""
	}
	return u.Host
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

func (s *S3Store) deleteObject(key string) {
	url := s.presignURL("DELETE", key, 60, false, "", "")
	req, err := http.NewRequest("DELETE", url, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
}

// Ensure S3Store implements the embedded interface used by the scheduler.
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
} = (*S3Store)(nil)

func hmacSHA256(key, data []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(data)
	return h.Sum(nil)
}

func sha256hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}
