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
	ServeDownload(ctx context.Context, artifactID string) (io.ReadCloser, *ArtifactMeta, error)
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
	S3PublicURL string
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
		S3PublicURL: getenv("FORGE_S3_PUBLIC_URL", ""),
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
