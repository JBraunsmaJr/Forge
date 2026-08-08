package pipeline

import (
	"fmt"
	"time"

	"github.com/JBraunsmaJr/forge/internal/api"
)

// Pipeline is the compiled, canonical representation of a pipeline.
// It is what the compiler produces from a .forge/pipeline.yaml file.
// The scheduler and executor never see raw YAML — only this.
type Pipeline struct {
	ID    string  // generated at compile time, e.g. "sha256:abc123"
	Name  string  // from yaml: name field
	Steps []*Step // ordered by dependency resolution
}

// Step is a single unit of work in a pipeline.
type Step struct {
	// With holds parameters for the template.
	With map[string]string
	// Env holds environment variables for this step.
	Env map[string]string
	// Release holds configuration for SCM releases (GitHub/GitLab).
	// Only used if Type == "release".
	Release *ReleaseConfig
	// DockerPublish holds configuration for promoting an image's tags.
	// Only used if Type == "docker_publish".
	DockerPublish *DockerPublishConfig
	// PipelineRef is non-nil when Type == "pipeline".
	PipelineRef *PipelineRef
	ID          string // unique within the pipeline, e.g. "lint"
	Name        string // human label, e.g. "Run linter"
	Image       string // container image, e.g. "node:20"
	// Type is "task" (default) or "generator".
	// A generator step's stdout is parsed as a JSON array of new step
	// definitions which are added to the live DAG at runtime (R11).
	Type string
	// Uses is a reference to a remote template.
	Uses string
	// WorkDir is the working directory inside the container.
	WorkDir string
	// CacheKey is computed by the compiler from Inputs file hashes.
	CacheKey string
	// Condition is a CEL expression that must evaluate to true for the step to run.
	Condition string
	// RunID and JobID are used for Docker label scoping.
	RunID string
	JobID string
	// OIDCToken is a short-lived identity token issued by the scheduler.
	OIDCToken  string
	Entrypoint []string // optional entrypoint override
	Command    []string // command + args, e.g. ["npm", "run", "lint"]
	// DependsOn lists IDs of steps that must complete before this one.
	DependsOn []string
	// Inputs is a list of glob patterns that affect this step's cache key.
	Inputs []string
	// Secrets is a list of secret names to fetch from Vault before running.
	Secrets []string
	// RedactValues holds the actual secret values fetched at runtime.
	RedactValues []string
	// ArtifactUploads declares files this step produces that should be stored
	ArtifactUploads []ArtifactUploadSpec
	// ArtifactDownloads declares artifacts from prior steps needed before running
	ArtifactDownloads []ArtifactDownloadSpec
	// Timeout is how long this step may run before being killed.
	Timeout time.Duration
	// DockerSocket mounts the host Docker daemon socket (/var/run/docker.sock)
	// into the step container. Use for steps that need to run docker commands
	// themselves (Docker-outside-Docker pattern). Requires docker-cli in the image.
	DockerSocket bool
	// AlwaysRun ensures the step runs even if dependencies fail.
	AlwaysRun bool

	// Split is non-nil when this step should be fanned out into parallel shards.
	Split *api.SplitConfig
	// TestReport is the path to the test report file produced by this step.
	TestReport string
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

// ReleaseConfig holds parameters for creating an SCM release.
type ReleaseConfig struct {
	Name      string   // Release name/title
	Tag       string   // Git tag name
	Body      string   // Release description
	Artifacts []string // Names of artifacts to attach to the release
}

// DockerPublishConfig holds parameters for a docker_publish step:
// promoting an already-pushed image to one or more new tags without
// rebuilding it (issue #57).
type DockerPublishConfig struct {
	Registry     string   // e.g. "ghcr.io", "docker.io"
	Repository   string   // e.g. "myorg/myapp"
	Source       string   // the tag being promoted from
	Tags         []string // the tag(s) being promoted to
	DeleteSource bool     // remove the source tag after successful promotion
}

func (p *Pipeline) ToAPISteps(variables map[string]string) []api.StepDef {
	steps := make([]api.StepDef, len(p.Steps))
	for i, s := range p.Steps {
		steps[i] = s.ToAPIStep(variables)
	}
	return steps
}

func (s *Step) ToAPIStep(variables map[string]string) api.StepDef {
	var uploads []api.ArtifactUploadSpec
	for _, u := range s.ArtifactUploads {
		uploads = append(uploads, api.ArtifactUploadSpec{
			Path: u.Path,
			Name: u.Name,
		})
	}
	var downloads []api.ArtifactDownloadSpec
	for _, d := range s.ArtifactDownloads {
		downloads = append(downloads, api.ArtifactDownloadSpec{
			Name: d.Name,
			Dest: d.Dest,
		})
	}

	var pipelineRef *api.PipelineRef
	if s.PipelineRef != nil {
		pipelineRef = &api.PipelineRef{
			Path:             s.PipelineRef.Path,
			Wait:             s.PipelineRef.Wait,
			Variables:        s.PipelineRef.Variables,
			ArtifactsSend:    s.PipelineRef.ArtifactsSend,
			ArtifactsReceive: s.PipelineRef.ArtifactsReceive,
		}
	}

	var release *api.ReleaseConfig
	if s.Release != nil {
		release = &api.ReleaseConfig{
			Name:      s.Release.Name,
			Tag:       s.Release.Tag,
			Body:      s.Release.Body,
			Artifacts: s.Release.Artifacts,
		}
	}

	var dockerPublish *api.DockerPublishConfig
	if s.DockerPublish != nil {
		dockerPublish = &api.DockerPublishConfig{
			Registry:     s.DockerPublish.Registry,
			Repository:   s.DockerPublish.Repository,
			Source:       s.DockerPublish.Source,
			Tags:         s.DockerPublish.Tags,
			DeleteSource: s.DockerPublish.DeleteSource,
		}
	}

	env := make(map[string]string)
	for k, v := range s.Env {
		env[k] = v
	}
	for k, v := range variables {
		env[k] = v
	}

	return api.StepDef{
		ID:                s.ID,
		Image:             s.Image,
		Entrypoint:        s.Entrypoint,
		Command:           s.Command,
		WorkDir:           s.WorkDir,
		Env:               env,
		DependsOn:         s.DependsOn,
		Inputs:            s.Inputs,
		Timeout:           s.Timeout,
		SecretNames:       s.Secrets,
		DockerSocket:      s.DockerSocket,
		Condition:         s.Condition,
		AlwaysRun:         s.AlwaysRun,
		Type:              s.Type,
		ArtifactUploads:   uploads,
		ArtifactDownloads: downloads,
		PipelineRef:       pipelineRef,
		Release:           release,
		DockerPublish:     dockerPublish,
		Split:             s.Split,
		TestReport:        s.TestReport,
		With:              s.With,
	}
}

// StepStatus represents where a step is in its lifecycle.
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

// Validate checks the pipeline for internal consistency, including cycle detection.
func (p *Pipeline) Validate() error {
	if p.Name == "" {
		return fmt.Errorf("pipeline name is required")
	}
	if len(p.Steps) == 0 {
		return fmt.Errorf("pipeline must have at least one step")
	}
	return ValidateSteps(p.Steps)
}

// ValidateSteps checks a set of steps for duplicate IDs, missing dependencies, and cycles.
func ValidateSteps(steps []*Step) error {
	ids := make(map[string]struct{})
	for _, s := range steps {
		if s.ID == "" {
			continue
		}
		if _, ok := ids[s.ID]; ok {
			return fmt.Errorf("duplicate step ID: %s", s.ID)
		}
		ids[s.ID] = struct{}{}
	}

	adj := make(map[string][]string)
	for _, s := range steps {
		adj[s.ID] = s.DependsOn
	}

	visited := make(map[string]bool)
	onStack := make(map[string]bool)

	var check func(string) error
	check = func(u string) error {
		visited[u] = true
		onStack[u] = true
		for _, v := range adj[u] {
			if _, ok := adj[v]; !ok {
				return fmt.Errorf("step %s depends on non-existent step %s", u, v)
			}
			if onStack[v] {
				return fmt.Errorf("cycle detected: step %s is part of a dependency loop", v)
			}
			if !visited[v] {
				if err := check(v); err != nil {
					return err
				}
			}
		}
		onStack[u] = false
		return nil
	}

	for _, s := range steps {
		if !visited[s.ID] {
			if err := check(s.ID); err != nil {
				return err
			}
		}
	}
	return nil
}
