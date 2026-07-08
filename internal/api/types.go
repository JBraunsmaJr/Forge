package api

import "time"

// JobStatus represents where a job is in its lifecycle.
// These are the only valid states — there is no "unknown" or "stuck" state.
// (This directly addresses R13 from the design doc.)
type JobStatus string

const (
	JobStatusPending  JobStatus = "pending"  // waiting for dependencies
	JobStatusQueued   JobStatus = "queued"   // ready, waiting for an agent
	JobStatusRunning  JobStatus = "running"  // claimed by an agent
	JobStatusPassed   JobStatus = "passed"   // exit code 0
	JobStatusFailed   JobStatus = "failed"   // exit code != 0
	JobStatusSkipped  JobStatus = "skipped"  // condition evaluated to false or cache hit
	JobStatusCanceled JobStatus = "canceled" // canceled before it ran
)

// JobSpec is what the scheduler sends to an agent when it leases a job.
// The agent uses this to know what to run.
type JobSpec struct {
	JobID             string                 `json:"job_id"`
	RunID             string                 `json:"run_id"`
	LeaseID           string                 `json:"lease_id"`
	StepID            string                 `json:"step_id"`
	Image             string                 `json:"image"`
	Command           []string               `json:"command"`
	WorkDir           string                 `json:"workdir"`
	Env               map[string]string      `json:"env"`
	Inputs            []string               `json:"inputs"`
	Timeout           time.Duration          `json:"timeout_ns"`
	SecretNames       []string               `json:"secret_names"`
	Type              string                 `json:"type"` // "task" | "generator"
	ArtifactUploads   []ArtifactUploadSpec   `json:"artifact_uploads,omitempty"`
	ArtifactDownloads []ArtifactDownloadSpec `json:"artifact_downloads,omitempty"`
	// OrgID and ProjectID are used by the agent for scoped secret resolution.
	// Secret lookup order: project → org → global → legacy.
	OrgID     string `json:"org_id,omitempty"`
	ProjectID string `json:"project_id,omitempty"`
	// PipelineRef is populated when Type == "pipeline".
	PipelineRef *PipelineRef `json:"pipeline_ref,omitempty"`
}

// SubmitRunRequest is sent by the CLI to submit a pipeline for execution.
type SubmitRunRequest struct {
	PipelineName string    `json:"pipeline_name"`
	Steps        []StepDef `json:"steps"`
	WorkspaceDir string    `json:"workspace_dir"`
	OrgID        string    `json:"org_id,omitempty"`     // enables policy injection
	ProjectID    string    `json:"project_id,omitempty"` // scopes secrets to this project
}

// StepDef carries a step's definition inside a SubmitRunRequest.
type StepDef struct {
	ID           string            `json:"id"`
	Image        string            `json:"image"`
	Command      []string          `json:"command,omitempty"`
	Run          string            `json:"run,omitempty"`
	WorkDir      string            `json:"workdir"`
	Env          map[string]string `json:"env"`
	DependsOn    []string          `json:"depends_on"`
	Inputs       []string          `json:"inputs"`
	Timeout      time.Duration     `json:"timeout_ns"`
	SecretNames  []string          `json:"secret_names"`
	Condition    string            `json:"condition,omitempty"`
	AlwaysRun    bool              `json:"always_run,omitempty"`
	Type         string            `json:"type"`
	PolicySource string            `json:"policy_source,omitempty"`
	// Artifact specs — carried through to the agent via the jobs table.
	ArtifactUploads   []ArtifactUploadSpec   `json:"artifact_uploads,omitempty"`
	ArtifactDownloads []ArtifactDownloadSpec `json:"artifact_downloads,omitempty"`
	// PipelineRef is populated when Type == "pipeline".
	PipelineRef *PipelineRef `json:"pipeline_ref,omitempty"`
}

// SubmitRunResponse is returned after a successful pipeline submission.
type SubmitRunResponse struct {
	RunID   string `json:"run_id"`
	Message string `json:"message"`
}

// LeaseRequest is sent by an agent to claim the next available job.
type LeaseRequest struct {
	AgentID string `json:"agent_id"` // unique ID of the requesting agent
}

// LeaseResponse is returned when a job is successfully leased.
// StatusCode 204 (No Content) means the queue is empty — try again later.
type LeaseResponse struct {
	Job *JobSpec `json:"job"` // nil if queue is empty
}

// HeartbeatRequest is sent by an agent every N seconds while a job runs.
// If the scheduler stops receiving heartbeats, it reclaims the job.
type HeartbeatRequest struct {
	LeaseID string `json:"lease_id"`
	AgentID string `json:"agent_id"`
}

// CompleteRequest is sent by an agent when a job finishes.
type CompleteRequest struct {
	LeaseID      string     `json:"lease_id"`
	AgentID      string     `json:"agent_id"`
	ExitCode     int        `json:"exit_code"`
	Duration     int64      `json:"duration_ms"`
	LogEvents    []LogEvent `json:"log_events"`
	EmittedSteps []StepDef  `json:"emitted_steps,omitempty"` // set by generator steps
}

// LogEvent is a single structured log line from a running job.
type LogEvent struct {
	Timestamp time.Time `json:"ts"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
}

// RunStatus is returned when the CLI polls for a run's progress.
type RunStatus struct {
	RunID  string      `json:"run_id"`
	Name   string      `json:"name"`
	Status JobStatus   `json:"status"` // overall: derived from job statuses
	Jobs   []JobStatus `json:"jobs"`
	JobIDs []string    `json:"job_ids"`
}

// ErrorResponse is returned by the server on any error.
type ErrorResponse struct {
	Error string `json:"error"`
}

// RunSummary is a lightweight run entry for the run list sidebar.
type RunSummary struct {
	RunID     string    `json:"run_id"`
	Name      string    `json:"name"`
	Status    JobStatus `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	JobCount  int       `json:"job_count"`
}

// RunDetail is the rich response used by the DAG view.
// It includes per-job dependency info so the browser can draw edges.
type RunDetail struct {
	RunID           string      `json:"run_id"`
	Name            string      `json:"name"`
	Status          JobStatus   `json:"status"`
	CreatedAt       time.Time   `json:"created_at"`
	Jobs            []JobDetail `json:"jobs"`
	AppliedPolicies []string    `json:"applied_policies,omitempty"`
	OrgID           string      `json:"org_id,omitempty"`
	ProjectID       string      `json:"project_id,omitempty"`
}

// JobDetail carries everything the DAG renderer needs for one node.
type JobDetail struct {
	JobID        string    `json:"job_id"`
	StepID       string    `json:"step_id"`
	Status       JobStatus `json:"status"`
	DependsOn    []string  `json:"depends_on"`
	DurationMs   int64     `json:"duration_ms"`
	ExitCode     int       `json:"exit_code"`
	PolicySource string    `json:"policy_source,omitempty"`
}

// CreateDebugRequest asks the scheduler to start a debug session for a job.
type CreateDebugRequest struct {
	JobID string `json:"job_id"`
}

// DebugSessionInfo is returned after creating a session and by the status endpoint.
type DebugSessionInfo struct {
	SessionID     string `json:"session_id"`
	Status        string `json:"status"`
	ContainerID   string `json:"container_id"`
	ExpiresInS    int    `json:"expires_in_s"`
	TerminalWsURL string `json:"terminal_ws_url,omitempty"` // direct WS URL to the agent
}

// DebugJobSpec is what the scheduler sends to an agent when it leases a debug session.
type DebugJobSpec struct {
	SessionID    string            `json:"session_id"`
	Image        string            `json:"image"`
	WorkDir      string            `json:"workdir"`
	Env          map[string]string `json:"env"`
	WorkspaceDir string            `json:"workspace_dir"`
}

// DebugCommand is a command queued by the browser to run in the debug container.
type DebugCommand struct {
	CommandID string `json:"command_id"`
	Input     string `json:"input"`
}

// DebugOutput is the result of a DebugCommand, sent from agent → scheduler → browser.
type DebugOutput struct {
	CommandID string `json:"command_id"`
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
}

// RegisterContainerRequest is sent by the agent once the debug container is running.
type RegisterContainerRequest struct {
	SessionID     string `json:"session_id"`
	ContainerID   string `json:"container_id"`
	AgentID       string `json:"agent_id"`
	TerminalWsURL string `json:"terminal_ws_url"` // ws://agent-addr/debug/{id}/ws
}

// PollCommandsResponse is returned to the agent when it polls for pending commands.
type PollCommandsResponse struct {
	Commands      []DebugCommand `json:"commands"`
	Closed        bool           `json:"closed"`         // true = session was closed, stop polling
	CancelCurrent bool           `json:"cancel_current"` // true = kill the running command
}

// SubmitOutputRequest is sent by the agent with the result of a command execution.
type SubmitOutputRequest struct {
	CommandID string `json:"command_id"`
	Output    string `json:"output"`
	ExitCode  int    `json:"exit_code"`
}

// ── Org and policy types ──────────────────────────────────────────────────────

// OrgInfo represents an organisation that owns projects and policies.
type OrgInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateOrgRequest creates a new org.
type CreateOrgRequest struct {
	Name string `json:"name"`
}

// PolicyInfo represents a set of mandatory steps enforced on every pipeline
// submitted under an org.
type PolicyInfo struct {
	ID             string             `json:"id"`
	OrgID          string             `json:"org_id"`
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Steps          []StepDef          `json:"steps,omitempty"`       // static injection
	Transformer    *PolicyTransformer `json:"transformer,omitempty"` // dynamic transformation
	ForbidOverride bool               `json:"forbid_override"`
	CreatedAt      time.Time          `json:"created_at"`
}

// CreatePolicyRequest creates a new policy for an org.
type CreatePolicyRequest struct {
	Name           string             `json:"name"`
	Description    string             `json:"description"`
	Steps          []StepDef          `json:"steps,omitempty"`
	Transformer    *PolicyTransformer `json:"transformer,omitempty"`
	ForbidOverride bool               `json:"forbid_override"`
}

// PolicyTransformer defines an executable that receives the full pipeline on
// stdin and outputs the modified pipeline on stdout.
//
// The transformer is a pure function: pipeline in, pipeline out.
// It can add steps, remove steps, or restructure dependencies freely.
//
// Protocol:
//
//	stdin:  TransformerInput as JSON
//	stdout: []StepDef as JSON (the complete modified step list)
//	stderr: logged for debugging; non-zero exit = transformer failed
type PolicyTransformer struct {
	// Image runs the transformer inside a Docker container.
	// The workspace is mounted read-only at /workspace so the transformer
	// can inspect files (Dockerfiles, requirements.txt, go.mod, etc.).
	Image string `json:"image,omitempty"`

	// Command overrides the container entrypoint.
	Command []string `json:"command,omitempty"`

	// Script runs an inline shell script directly on the scheduler host.
	// Useful for development; Image is preferred for production.
	Script string `json:"script,omitempty"`

	// Timeout is how long the transformer may run before being killed.
	// Defaults to 30s — transformers should be fast.
	Timeout string `json:"timeout,omitempty"`
}

// TransformerInput is what the policy engine writes to the transformer's stdin.
type TransformerInput struct {
	PipelineName string    `json:"pipeline_name"`
	Steps        []StepDef `json:"steps"`
	WorkspaceDir string    `json:"workspace_dir"`
	OrgID        string    `json:"org_id"`
}

// ProjectInfo represents a source repo registered with Forge.
type ProjectInfo struct {
	ID            string    `json:"id"`
	OrgID         string    `json:"org_id"`
	Name          string    `json:"name"`
	RepoURL       string    `json:"repo_url"`
	PipelinePath  string    `json:"pipeline_path"`
	BranchFilter  []string  `json:"branch_filter,omitempty"`
	WebhookSecret string    `json:"webhook_secret,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ProjectBranchesResponse returns the list of branches for a project.
type ProjectBranchesResponse struct {
	Branches []string `json:"branches"`
	Default  string   `json:"default"`
}

// CreateProjectRequest registers a new project.
type CreateProjectRequest struct {
	Name         string   `json:"name"`
	RepoURL      string   `json:"repo_url"`
	PipelinePath string   `json:"pipeline_path,omitempty"`
	SCMToken     string   `json:"scm_token,omitempty"`
	BranchFilter []string `json:"branch_filter,omitempty"` // empty = all branches
}

// ManualTriggerRequest is used to manually start a pipeline run.
type ManualTriggerRequest struct {
	Branch string `json:"branch"`
	Commit string `json:"commit,omitempty"` // optional, defaults to HEAD
}

// WebhookRunMeta carries SCM context attached to webhook-triggered runs.
type WebhookRunMeta struct {
	Provider  string `json:"provider"` // "github" | "gitlab" | "generic"
	Event     string `json:"event"`    // "push" | "pull_request" | "merge_request"
	RepoURL   string `json:"repo_url"`
	RepoName  string `json:"repo_name"`
	Branch    string `json:"branch"`
	CommitSHA string `json:"commit_sha"`
	CommitMsg string `json:"commit_message"`
	Author    string `json:"author"`
	PRNumber  int    `json:"pr_number,omitempty"`
}

// AppendLogsRequest is sent by the agent to stream log events during execution.
// The lease_id ensures only the agent currently running the job can append.
type AppendLogsRequest struct {
	LeaseID string     `json:"lease_id"`
	Events  []LogEvent `json:"events"`
}

// TerminalResizeMsg is sent by the browser when the xterm.js window is resized.
// Sent as a JSON message on the same WebSocket as raw terminal bytes.
// The agent detects it by the leading '{' character.
type TerminalResizeMsg struct {
	Type string `json:"type"` // "resize"
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// TokenInfo describes an API token (the raw value is never returned after creation).
type TokenInfo struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Role      string     `json:"role"` // "admin" | "agent"
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
}

// CreateTokenRequest creates a new API token.
type CreateTokenRequest struct {
	Name      string     `json:"name"`
	Role      string     `json:"role,omitempty"`       // defaults to "admin"
	ExpiresAt *time.Time `json:"expires_at,omitempty"` // optional expiry date
	Preset    string     `json:"preset,omitempty"`     // use a specific token value (for reproducible dev environments)
}

// CreateTokenResponse is the only time the raw token value is returned.
// After this response the value cannot be recovered — only its hash is stored.
type CreateTokenResponse struct {
	Token string    `json:"token"` // raw fgt_... value — store this now
	Info  TokenInfo `json:"info"`
}

// ArtifactUploadSpec declares a file (or glob) a step will produce
type ArtifactUploadSpec struct {
	Path string `json:"path"`           // glob relative to workspace, e.g. "dist/myapp"
	Name string `json:"name,omitempty"` // logical name; defaults to basename of path
}

// ArtifactDownloadSpec declares an artifact a step needs before running
type ArtifactDownloadSpec struct {
	Name string `json:"name"`
	Dest string `json:"dest"`
}

// ArtifactMeta describes a stored artifact returned by the scheduler
type ArtifactMeta struct {
	ID          string    `json:"id"`
	RunID       string    `json:"run_id"`
	JobID       string    `json:"job_id"`
	Name        string    `json:"name"`
	Filename    string    `json:"filename"`
	SizeBytes   int64     `json:"size_bytes"`
	ContentType string    `json:"content_type"`
	DownloadURL string    `json:"download_url,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// PresignUploadRequest is sent by the agent to get an upload URL.
type PresignUploadRequest struct {
	RunID       string `json:"run_id"`
	JobID       string `json:"job_id"`
	Name        string `json:"name"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type,omitempty"`
}

// PresignUploadResponse gives the agent a URL to PUT the artifact file.
type PresignUploadResponse struct {
	ArtifactID string `json:"artifact_id"`
	UploadURL  string `json:"upload_url"`
	Method     string `json:"method"`
}

// ConfirmUploadRequest is sent after the PUT completes.
type ConfirmUploadRequest struct {
	SizeBytes int64 `json:"size_bytes"`
}

// PipelineRef is set on steps of type "pipeline". The agent compiles and submits
// the referenced pipeline as a child run, optionally waiting for it to complete.
type PipelineRef struct {
	// Path is a pipeline file path relative to the workspace (e.g. ".forge/deploy.yml").
	Path string `json:"path,omitempty"`

	// Wait blocks the parent step until the child run finishes.
	// Defaults to true — set false for fire-and-forget triggers.
	Wait bool `json:"wait"`

	// Variables are injected into every step of the child pipeline as env vars.
	// Use this to pass build metadata (IMAGE_TAG, GIT_SHA, TARGET_ENV, etc.).
	Variables map[string]string `json:"variables,omitempty"`

	// ArtifactsSend names artifacts from the PARENT run to make available
	// in the child run. The child can download them by the same name.
	ArtifactsSend []string `json:"artifacts_send,omitempty"`

	// ArtifactsReceive downloads artifacts from the CHILD run into the parent
	// run's artifact store once the child completes. Downstream parent steps
	// can then declare artifact downloads using the same name.
	ArtifactsReceive []string `json:"artifacts_receive,omitempty"`
}

// FlakyStep describes a step that has inconsistent pass/fail history.
type FlakyStep struct {
	PipelineName string  `json:"pipeline_name"`
	StepID       string  `json:"step_id"`
	TotalRuns    int     `json:"total_runs"`
	Failures     int     `json:"failures"`
	FlakeRate    float64 `json:"flake_rate"` // failures / total_runs
	LastSeen     string  `json:"last_seen"`
}
