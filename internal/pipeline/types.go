// Package pipeline defines Forge's canonical pipeline types.
//
// This is the "DAG is the API" principle from the design doc —
// everything (CLI, scheduler, UI) operates on these types, not
// on raw YAML. The compiler translates YAML → these types.
//
// C# equivalent mental model:
//
//	Pipeline  ≈ a class with properties
//	Step      ≈ a record/struct
//	StepStatus ≈ an enum
package pipeline

import "time"

// Pipeline is the compiled, canonical representation of a pipeline.
// It is what the compiler produces from a .forge/pipeline.yaml file.
// The scheduler and executor never see raw YAML — only this.
type Pipeline struct {
	ID    string  // generated at compile time, e.g. "sha256:abc123"
	Name  string  // from yaml: name field
	Steps []*Step // ordered by dependency resolution
}

// Step is a single unit of work in a pipeline.
// In C# terms, think of this as a sealed record with init-only properties.
type Step struct {
	ID      string   // unique within the pipeline, e.g. "lint"
	Name    string   // human label, e.g. "Run linter"
	Image   string   // container image, e.g. "node:20"
	Command []string // command + args, e.g. ["npm", "run", "lint"]

	// Type is "task" (default) or "generator".
	// A generator step's stdout is parsed as a JSON array of new step
	// definitions which are added to the live DAG at runtime (R11).
	Type string

	// WorkDir is the working directory inside the container.
	WorkDir string

	// Env holds environment variables for this step.
	Env map[string]string

	// DependsOn lists IDs of steps that must complete before this one.
	DependsOn []string

	// Inputs is a list of glob patterns that affect this step's cache key.
	Inputs []string

	// CacheKey is computed by the compiler from Inputs file hashes.
	CacheKey string

	// Secrets is a list of secret names to fetch from Vault before running.
	Secrets []string

	// RedactValues holds the actual secret values fetched at runtime.
	RedactValues []string

	// DockerSocket mounts the host Docker daemon socket (/var/run/docker.sock)
	// into the step container. Use for steps that need to run docker commands
	// themselves (Docker-outside-Docker pattern). Requires docker-cli in the image.
	DockerSocket bool

	// ArtifactUploads declares files this step produces that should be stored
	ArtifactUploads []ArtifactUploadSpec

	// ArtifactDownloads declares artifacts from prior steps needed before running
	ArtifactDownloads []ArtifactDownloadSpec

	// PipelineRef is non-nil when Type == "pipeline".
	PipelineRef *PipelineRef

	// Timeout is how long this step may run before being killed.
	Timeout time.Duration
}

// PipelineRef holds chaining configuration for pipeline steps.
type PipelineRef struct {
	Path             string
	Wait             bool
	Variables        map[string]string
	ArtifactsSend    []string
	ArtifactsReceive []string
}

// ArtifactUploadSpec declares a file or glob pattern to upload after a step succeeds
type ArtifactUploadSpec struct {
	Path string // glob relative to /workspace
	Name string // logical name; defaults to filepath.Base(Path)
}

// ArtifactDownloadSpec declares an artifact to fetch before a step runs
type ArtifactDownloadSpec struct {
	Name string // logical name from a prior step's upload
	Dest string // destination path inside /workspace
}

// StepStatus represents where a step is in its lifecycle.
// In C# this would be: public enum StepStatus { ... }
type StepStatus int

const (
	StatusPending  StepStatus = iota // waiting for dependencies
	StatusQueued                     // dependencies met, waiting for a runner
	StatusRunning                    // actively executing
	StatusPassed                     // exited 0
	StatusFailed                     // exited non-zero
	StatusSkipped                    // cache hit — execution skipped
	StatusCanceled                   // canceled before it could run
)

// String makes StepStatus implement the fmt.Stringer interface.
// In C# this is equivalent to overriding ToString().
func (s StepStatus) String() string {
	switch s {
	case StatusPending:
		return "pending"
	case StatusQueued:
		return "queued"
	case StatusRunning:
		return "running"
	case StatusPassed:
		return "passed"
	case StatusFailed:
		return "failed"
	case StatusSkipped:
		return "skipped (cache hit)"
	case StatusCanceled:
		return "canceled"
	default:
		return "unknown"
	}
}

// RunResult is what the executor returns after a pipeline completes.
type RunResult struct {
	Pipeline *Pipeline
	Steps    []*StepResult
	Passed   bool
	Duration time.Duration
}

// StepResult holds the outcome of a single step execution.
type StepResult struct {
	Step     *Step
	Status   StepStatus
	ExitCode int
	Duration time.Duration
	LogFile  string // path to the structured log file for this step
	CacheHit bool   // true if this step was skipped due to a cache hit

	// GeneratedStepsJSON is set by generator steps — it's the raw JSON
	// array of new step definitions emitted to stdout.
	GeneratedStepsJSON []byte
}
