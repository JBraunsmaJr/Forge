// Package artifacts provides artifact storage for Forge pipeline runs.
//
// Artifacts are files produced by steps (binaries, test results, reports)
// that need to be shared between jobs or retained after a run completes.
//
// # Storage backends
//
// Two backends are supported, selectable via FORGE_ARTIFACT_STORE:
//
//	local (default) — files stored on the scheduler's filesystem and served
//	via HTTP. No external dependencies. Suitable for single-machine setups
//	and local development. Works with `forge run` out of the box.
//
//	s3 — objects stored in any S3-compatible store (AWS S3, MinIO, GCS,
//	Cloudflare R2). The docker-compose stack ships MinIO for local S3 testing.
//
// # Pre-signed URL pattern
//
// All uploads and downloads go through pre-signed URLs. This design keeps
// agents simple — they never know which backend is in use, they just PUT
// or GET a URL provided by the scheduler. For large files this also means
// traffic bypasses the scheduler when using S3, which avoids making the
// scheduler a bandwidth bottleneck.
//
//	Agent upload flow:
//	  1. POST /api/v1/artifacts/presign → get {upload_url, artifact_id}
//	  2. PUT  <upload_url>             (directly to scheduler or S3)
//	  3. POST /api/v1/artifacts/{id}/confirm
//
//	Agent download flow:
//	  1. GET /api/v1/artifacts?run_id=X&name=Y → get {download_url, ...}
//	  2. GET <download_url>                    (directly from scheduler or S3)
package artifacts

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"
)

// ArtifactStorer is the artifact store interface used by the scheduler.
type ArtifactStorer interface {
	PresignUpload(ctx context.Context, req PresignRequest) (*PresignResponse, error)
	ConfirmUpload(ctx context.Context, artifactID string, sizeBytes int64) error
	GetArtifact(ctx context.Context, runID, name string) (*ArtifactMeta, error)
	ListArtifacts(ctx context.Context, runID string) ([]ArtifactMeta, error)
	DeleteArtifact(ctx context.Context, artifactID string) error
	ServeUpload(ctx context.Context, artifactID, uploadToken string, r io.Reader) error
	ServeDownload(ctx context.Context, artifactID string) (io.ReadCloser, int64, error)
}

// ArtifactMeta describes a stored artifact.
type ArtifactMeta struct {
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`     // logical name (e.g. "myapp-binary")
	Filename    string    `json:"filename"` // original filename (e.g. "myapp")
	SizeBytes   int64     `json:"size_bytes"`
	ContentType string    `json:"content_type"`
	DownloadURL string    `json:"download_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// PresignRequest is sent by the agent to request an upload URL.
type PresignRequest struct {
	RunID       string `json:"run_id"`
	JobID       string `json:"job_id"`
	Name        string `json:"name"`         // logical artifact name
	Filename    string `json:"filename"`     // original file name
	ContentType string `json:"content_type"` // MIME type
}

// PresignResponse is returned to the agent.
type PresignResponse struct {
	ArtifactID string `json:"artifact_id"`
	UploadURL  string `json:"upload_url"` // PUT to this URL
	Method     string `json:"method"`     // always "PUT"
}

// Config holds artifact store configuration read from environment variables.
type Config struct {
	Backend     string // "local" | "s3"
	LocalDir    string // path for local storage (FORGE_ARTIFACT_DIR)
	LocalBase   string // base URL the scheduler is reachable at (for local URLs)
	S3Endpoint  string
	S3Bucket    string
	S3Region    string
	S3AccessKey string
	S3SecretKey string
}

// ConfigFromEnv reads artifact store config from environment variables.
func ConfigFromEnv(schedulerBaseURL string) Config {
	c := Config{
		Backend:     getenv("FORGE_ARTIFACT_STORE", "local"),
		LocalDir:    getenv("FORGE_ARTIFACT_DIR", "/data/artifacts"),
		LocalBase:   schedulerBaseURL,
		S3Endpoint:  getenv("FORGE_S3_ENDPOINT", ""),
		S3Bucket:    getenv("FORGE_S3_BUCKET", "forge-artifacts"),
		S3Region:    getenv("FORGE_S3_REGION", "us-east-1"),
		S3AccessKey: getenv("FORGE_S3_ACCESS_KEY", ""),
		S3SecretKey: getenv("FORGE_S3_SECRET_KEY", ""),
	}
	return c
}

// ErrNotFound is returned when an artifact doesn't exist.
var ErrNotFound = fmt.Errorf("artifact not found")

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
