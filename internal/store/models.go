package store

import (
	"time"

	"gorm.io/datatypes"
)

// Org represents the orgs table.
type Org struct {
	ID        string    `gorm:"primaryKey"`
	Name      string    `gorm:"unique;not null"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// User represents the users table.
type User struct {
	ID        string    `gorm:"primaryKey"`
	Email     string    `gorm:"unique;not null"`
	Name      string    `gorm:"not null"`
	Role      string    `gorm:"not null;default:'viewer'"`
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// SSOIdentity represents the sso_identities table.
type SSOIdentity struct {
	ID         string    `gorm:"primaryKey"`
	UserID     string    `gorm:"not null"`
	User       User      `gorm:"foreignKey:UserID;constraint:OnDelete:CASCADE"`
	Provider   string    `gorm:"not null;uniqueIndex:idx_provider_external"`
	ExternalID string    `gorm:"not null;uniqueIndex:idx_provider_external"`
	LastLogin  time.Time `gorm:"not null;default:now()"`
}

// Project represents the projects table.
type Project struct {
	ID                    string         `gorm:"primaryKey"`
	OrgID                 *string        `gorm:"index"`
	Org                   *Org           `gorm:"foreignKey:OrgID;constraint:OnDelete:SET NULL"`
	Name                  string         `gorm:"not null"`
	RepoURL               string         `gorm:"unique;not null"`
	PipelinePath          string         `gorm:"not null;default:''"`
	WebhookSecret         string         `gorm:"not null"`
	SCMToken              string         `gorm:"not null;default:''"`
	BranchFilter          datatypes.JSON `gorm:"not null;default:'[]'"`
	Cron                  string         `gorm:"not null;default:''"`
	ScheduledPipelinePath string         `gorm:"not null;default:''"`
	LastScheduledAt       *time.Time
	CreatedAt             time.Time `gorm:"not null;default:now()"`
}

// ProjectHealthSnapshot represents the project_health_snapshots table.
type ProjectHealthSnapshot struct {
	ID           string         `gorm:"primaryKey"`
	ProjectID    string         `gorm:"not null;index:idx_health_snapshots_project_time,priority:1"`
	Project      Project        `gorm:"foreignKey:ProjectID;constraint:OnDelete:CASCADE"`
	ComputedAt   time.Time      `gorm:"not null;default:now();index:idx_health_snapshots_project_time,priority:2,sort:desc"`
	PipelineName string         `gorm:"not null;default:''"`
	Score        int            `gorm:"not null"`
	Findings     datatypes.JSON `gorm:"not null;default:'[]'"`
}

// APIToken represents the api_tokens table.
type APIToken struct {
	ID        string `gorm:"primaryKey"`
	TokenHash string `gorm:"unique;not null"`
	Name      string `gorm:"not null"`
	Role      string `gorm:"not null;default:'admin'"`
	OrgID     *string
	Org       *Org `gorm:"foreignKey:OrgID;constraint:OnDelete:SET NULL"`
	ProjectID *string
	Project   *Project `gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL"`
	UserID    *string
	User      *User `gorm:"foreignKey:UserID;constraint:OnDelete:SET NULL"`
	ExpiresAt *time.Time
	CreatedAt time.Time `gorm:"not null;default:now()"`
}

// AuditLog represents the audit_logs table.
type AuditLog struct {
	ID         string    `gorm:"primaryKey"`
	Timestamp  time.Time `gorm:"not null;default:now()"`
	ActorID    *string
	ActorName  *string
	Action     string `gorm:"not null"`
	TargetType *string
	TargetID   *string
	Details    datatypes.JSON
	IPAddress  *string
	OrgID      *string
}

// Run represents the runs table.
type Run struct {
	ID               string         `gorm:"primaryKey"`
	Name             string         `gorm:"not null"`
	WorkspaceDir     string         `gorm:"not null;default:''"`
	AppliedStepIDs   datatypes.JSON `gorm:"not null;default:'[]'"`
	OrgID            *string        `gorm:"index"`
	Org              *Org           `gorm:"foreignKey:OrgID;constraint:OnDelete:SET NULL"`
	ProjectID        *string        `gorm:"index"`
	Project          *Project       `gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL"`
	Ref              *string
	CommitSHA        *string
	SCMProvider      *string
	ParentRunID      *string
	ParentJobID      *string `gorm:"uniqueIndex:runs_parent_job_idx"`
	PreferredAgentID *string
	PipelineName     string    `gorm:"not null;default:''"`
	CreatedAt        time.Time `gorm:"not null;default:now()"`
}

// Job represents the jobs table.
type Job struct {
	ID                string         `gorm:"primaryKey"`
	RunID             string         `gorm:"not null;index:jobs_run_id_idx"`
	Run               Run            `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	StepID            string         `gorm:"not null"`
	StepType          string         `gorm:"not null;default:'task'"`
	Image             string         `gorm:"not null;default:''"`
	Command           datatypes.JSON `gorm:"not null;default:'[]'"`
	WorkDir           string         `gorm:"not null;default:'/workspace'"`
	Env               datatypes.JSON `gorm:"not null;default:'{}'"`
	Inputs            datatypes.JSON `gorm:"not null;default:'[]'"`
	TimeoutNS         int64          `gorm:"not null;default:1800000000000"`
	DependsOn         datatypes.JSON `gorm:"not null;default:'[]'"`
	SecretNames       datatypes.JSON `gorm:"not null;default:'[]'"`
	DockerSocket      bool           `gorm:"not null;default:false"`
	PolicySource      string         `gorm:"not null;default:''"`
	Condition         string         `gorm:"not null;default:''"`
	AlwaysRun         bool           `gorm:"not null;default:false"`
	Entrypoint        datatypes.JSON `gorm:"not null;default:'[]'"`
	PipelineRef       datatypes.JSON
	ReleaseConfig     datatypes.JSON
	ArtifactUploads   datatypes.JSON `gorm:"not null;default:'[]'"`
	ArtifactDownloads datatypes.JSON `gorm:"not null;default:'[]'"`
	EmittedStepIDs    datatypes.JSON `gorm:"not null;default:'[]'"`
	Status            string         `gorm:"not null;default:'pending';index:jobs_status_idx"`
	LeaseID           string         `gorm:"not null;default:''"`
	AgentID           string         `gorm:"not null;default:''"`
	LeasedAt          *time.Time
	HeartbeatAt       *time.Time `gorm:"index:jobs_hb_idx,where:status = 'running'"`
	ExitCode          int        `gorm:"not null;default:0"`
	DurationMS        int64      `gorm:"not null;default:0"`
	StartedAt         *time.Time
	FinishedAt        *time.Time
	TestReport        string `gorm:"not null;default:''"`
	Split             datatypes.JSON
	With              datatypes.JSON `gorm:"column:with;not null;default:'{}'"`
}

// TestFileDuration represents the test_file_durations table.
type TestFileDuration struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	RunID        string    `gorm:"not null;index:test_file_dur_run_idx"`
	Run          Run       `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	JobID        string    `gorm:"not null;index:test_file_dur_job_id_idx"`
	Job          Job       `gorm:"foreignKey:JobID;constraint:OnDelete:CASCADE"`
	ProjectID    *string   `gorm:"index:test_file_dur_step_idx,priority:1"`
	Project      *Project  `gorm:"foreignKey:ProjectID;constraint:OnDelete:SET NULL"`
	PipelineName string    `gorm:"not null;index:test_file_dur_step_idx,priority:2"`
	StepID       string    `gorm:"not null;index:test_file_dur_step_idx,priority:3"`
	FilePath     string    `gorm:"not null"`
	DurationMS   int64     `gorm:"not null"`
	TestCount    int       `gorm:"not null;default:0"`
	Passed       int       `gorm:"not null;default:0"`
	Failed       int       `gorm:"not null;default:0"`
	Skipped      int       `gorm:"not null;default:0"`
	CreatedAt    time.Time `gorm:"not null;default:now();index:test_file_dur_step_idx,priority:4,sort:desc"`
}

// TestShardAssignment represents the test_shard_assignments table.
type TestShardAssignment struct {
	ID          uint64         `gorm:"primaryKey;autoIncrement"`
	RunID       string         `gorm:"not null;index:test_shard_run_idx,priority:1"`
	Run         Run            `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	StepID      string         `gorm:"not null;index:test_shard_run_idx,priority:2"`
	ShardIndex  int            `gorm:"not null;index:test_shard_run_idx,priority:3"`
	TotalShards int            `gorm:"not null"`
	FilePaths   datatypes.JSON `gorm:"not null"`
	EstimatedMS int64          `gorm:"not null"`
	CreatedAt   time.Time      `gorm:"not null;default:now()"`
}

// JobLog represents the job_logs table.
type JobLog struct {
	ID      uint64    `gorm:"primaryKey;autoIncrement"`
	JobID   string    `gorm:"not null;index:job_logs_job_id_idx"`
	Job     Job       `gorm:"foreignKey:JobID;constraint:OnDelete:CASCADE"`
	TS      time.Time `gorm:"not null"`
	Level   string    `gorm:"not null"`
	Message string    `gorm:"not null"`
}

// JobRootCause represents the job_root_causes table — an automatic
// classification of why a job's step failed, produced by pattern-matching
// its logs against a known-signature library. One row per
// job: a job is classified at most once, re-classification (if it were
// ever re-run under the same ID) replaces the existing row.
type JobRootCause struct {
	JobID        string    `gorm:"primaryKey"`
	Job          Job       `gorm:"foreignKey:JobID;constraint:OnDelete:CASCADE"`
	RunID        string    `gorm:"not null;index:job_root_causes_run_idx"`
	ProjectID    *string   `gorm:"index:job_root_causes_project_step_idx,priority:1"`
	StepID       string    `gorm:"not null;index:job_root_causes_project_step_idx,priority:2"`
	Category     string    `gorm:"not null;index:job_root_causes_category_idx"`
	PatternID    string    `gorm:"not null;default:''"`
	Description  string    `gorm:"not null;default:''"`
	MatchedLine  string    `gorm:"not null;default:''"`
	SuggestedFix string    `gorm:"not null;default:''"`
	CreatedAt    time.Time `gorm:"not null;default:now();index:job_root_causes_project_step_idx,priority:3,sort:desc"`
}

// Policy represents the policies table.
type Policy struct {
	ID             string         `gorm:"primaryKey"`
	OrgID          string         `gorm:"not null;index:policies_org_id_idx"`
	Org            Org            `gorm:"foreignKey:OrgID;constraint:OnDelete:CASCADE"`
	Name           string         `gorm:"not null"`
	Description    string         `gorm:"not null;default:''"`
	Steps          datatypes.JSON `gorm:"not null;default:'[]'"`
	Transformer    datatypes.JSON
	ForbidOverride bool      `gorm:"not null;default:false"`
	CreatedAt      time.Time `gorm:"not null;default:now()"`
}

// StepResult represents the step_results table.
type StepResult struct {
	ID           uint64    `gorm:"primaryKey;autoIncrement"`
	RunID        string    `gorm:"not null;index:step_results_run_id_idx"`
	Run          Run       `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	PipelineName string    `gorm:"not null;index:step_results_step_idx,priority:1"`
	StepID       string    `gorm:"not null;index:step_results_step_idx,priority:2"`
	Status       string    `gorm:"not null"`
	DurationMS   int64     `gorm:"not null;default:0"`
	CreatedAt    time.Time `gorm:"not null;default:now();index:step_results_time_idx,sort:desc"`
}

// Artifact represents the artifacts table.
type Artifact struct {
	ID          string  `gorm:"primaryKey"`
	RunID       string  `gorm:"not null;index:artifacts_run_id_idx;index:artifacts_run_name_idx,priority:1"`
	Run         Run     `gorm:"foreignKey:RunID;constraint:OnDelete:CASCADE"`
	JobID       *string `gorm:"index"`
	Job         *Job    `gorm:"foreignKey:JobID;constraint:OnDelete:CASCADE"`
	Name        string  `gorm:"not null;index:artifacts_run_name_idx,priority:2"`
	Filename    string  `gorm:"not null"`
	SizeBytes   int64   `gorm:"not null;default:0"`
	ContentType string  `gorm:"not null;default:'application/octet-stream'"`
	StorageKey  string  `gorm:"not null"`
	UploadToken *string
	Confirmed   bool      `gorm:"not null;default:false"`
	CreatedAt   time.Time `gorm:"not null;default:now()"`
}
