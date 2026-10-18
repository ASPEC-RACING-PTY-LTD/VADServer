package wire

// Backup job states (api-v1 §6). A suspended job is never deleted.
const (
	JobStateActive             = "active"
	JobStateSuspendedByMode    = "suspended_by_mode"
	JobStateSuspendedByProfile = "suspended_by_profile"
)

// Backup job kinds (rev 1.0 §6); also snapshot kinds (rev 1.0 §5.2).
const (
	JobKindFolder     = "folder"
	JobKindProfile    = "profile"
	JobKindApp        = "app"
	JobKindGame       = "game"
	JobKindDevProject = "devproject"
	JobKindRegistry   = "registry"
	JobKindInventory  = "inventory"
)

// Backup job modes (rev 1.0 §6); also snapshot modes (rev 1.0 §5.2).
const (
	JobModeFull               = "full"
	JobModeDataOnly           = "data_only"
	JobModeReinstallInventory = "reinstall_inventory"
)

// Volume Shadow Copy use of a job (JobSpec.UseVSS, rev 1.0 §6).
const (
	JobUseVSSAuto     = "auto"
	JobUseVSSRequired = "required"
	JobUseVSSOff      = "off"
)

// Job schedule types (JobSchedule.Type, rev 1.0 §6).
const (
	JobScheduleCron     = "cron"
	JobScheduleDaily    = "daily"
	JobScheduleWeekly   = "weekly"
	JobScheduleInterval = "interval"
	JobScheduleManual   = "manual"
)

// Missed-schedule behaviour (JobSchedule.CatchUp, rev 1.0 §6).
const (
	JobCatchUpRunOnce = "run_once"
	JobCatchUpSkip    = "skip"
)

// JobSpec is BackupJob.spec (rev 1.0 §6). Paths use the path tokens of
// backup-format §6. RulesetID is null when no ruleset applies.
type JobSpec struct {
	Paths     []string `json:"paths"`
	Exclude   []string `json:"exclude"`
	RulesetID *int64   `json:"ruleset_id"`
	SourceRef string   `json:"source_ref"`
	UseVSS    string   `json:"use_vss"`
}

// JobSchedule is BackupJob.schedule (rev 1.0 §6). Weekdays are 1..7.
type JobSchedule struct {
	Type          string `json:"type"`
	Expr          string `json:"expr"`
	At            string `json:"at"`
	Weekdays      []int  `json:"weekdays"`
	EveryMinutes  int    `json:"every_minutes"`
	Timezone      string `json:"timezone"`
	CatchUp       string `json:"catch_up"`
	JitterMinutes int    `json:"jitter_minutes"`
}

// Job is a backup job definition (api-v1 §6 BackupJob): the rev 1.0 fields
// plus ProfileSID, RequiredModules and State. RequiredModules and Revision
// are set by the server. Revision increments on every change; a PATCH sends
// it as If-Match (409 conflict on mismatch).
type Job struct {
	ID                int64       `json:"id"`
	DeviceID          int64       `json:"device_id"`
	RepoID            int64       `json:"repo_id"`
	Name              string      `json:"name"`
	Kind              string      `json:"kind"`
	Mode              string      `json:"mode"`
	Spec              JobSpec     `json:"spec"`
	Schedule          JobSchedule `json:"schedule"`
	RetentionPolicyID *int64      `json:"retention_policy_id"`
	Priority          int         `json:"priority"`
	Enabled           bool        `json:"enabled"`
	Revision          int64       `json:"revision"`
	ProfileSID        string      `json:"profile_sid"`
	RequiredModules   []string    `json:"required_modules"`
	State             string      `json:"state"`
	CreatedAt         int64       `json:"created_at"`
	UpdatedAt         int64       `json:"updated_at"`
}

// JobCreateRequest is the body of POST /backup/jobs: the writable Job
// fields. The server answers 409 module_unavailable when the device lacks a
// required module.
type JobCreateRequest struct {
	DeviceID          int64       `json:"device_id"`
	RepoID            int64       `json:"repo_id"`
	Name              string      `json:"name"`
	Kind              string      `json:"kind"`
	Mode              string      `json:"mode"`
	Spec              JobSpec     `json:"spec"`
	Schedule          JobSchedule `json:"schedule"`
	RetentionPolicyID *int64      `json:"retention_policy_id"`
	Priority          int         `json:"priority"`
	Enabled           bool        `json:"enabled"`
	ProfileSID        string      `json:"profile_sid"`
}

// JobUpdateRequest is the body of PATCH /backup/jobs/{id}, sent with
// If-Match: <revision>; omitted fields are unchanged. Spec and Schedule,
// when present, replace the whole object.
type JobUpdateRequest struct {
	Name              *string      `json:"name,omitempty"`
	Mode              *string      `json:"mode,omitempty"`
	Spec              *JobSpec     `json:"spec,omitempty"`
	Schedule          *JobSchedule `json:"schedule,omitempty"`
	RetentionPolicyID *int64       `json:"retention_policy_id,omitempty"`
	Priority          *int         `json:"priority,omitempty"`
	Enabled           *bool        `json:"enabled,omitempty"`
	ProfileSID        *string      `json:"profile_sid,omitempty"`
}

// Run statuses (rev 1.0 §6). Partial means committed with skipped items.
const (
	RunStatusQueued     = "queued"
	RunStatusScanning   = "scanning"
	RunStatusUploading  = "uploading"
	RunStatusCommitting = "committing"
	RunStatusCompleted  = "completed"
	RunStatusPartial    = "partial"
	RunStatusFailed     = "failed"
	RunStatusCancelled  = "cancelled"
)

// Run triggers (RunStartRequest.Trigger, rev 1.0 §6).
const (
	RunTriggerSchedule     = "schedule"
	RunTriggerManual       = "manual"
	RunTriggerAutomation   = "automation"
	RunTriggerRetrySkipped = "retry_skipped"
	RunTriggerTask         = "task"
)

// RunProgress is the heartbeat body of PATCH /backup/runs/{id} (dev, at most
// one per 2 s; rev 1.0 §6). Warnings and Errors are counts.
type RunProgress struct {
	Status        string `json:"status"`
	Phase         string `json:"phase"`
	FilesTotal    int64  `json:"files_total"`
	FilesDone     int64  `json:"files_done"`
	BytesTotal    int64  `json:"bytes_total"`
	BytesDone     int64  `json:"bytes_done"`
	BytesNew      int64  `json:"bytes_new"`
	BytesUploaded int64  `json:"bytes_uploaded"`
	QueuePacks    int64  `json:"queue_packs"`
	Warnings      int64  `json:"warnings"`
	Errors        int64  `json:"errors"`
	ETAMS         int64  `json:"eta_ms"`
}

// Run is one execution of a backup job (GET /backup/runs[/{id}], sess).
// Rev 1.0 does not spell the object out; it carries the start fields and
// the last heartbeat. SnapshotID is null until committed.
type Run struct {
	ID            int64  `json:"id"`
	JobID         int64  `json:"job_id"`
	DeviceID      int64  `json:"device_id"`
	ClientRunUUID string `json:"client_run_uuid"`
	Trigger       string `json:"trigger"`
	SnapshotID    *int64 `json:"snapshot_id"`
	StartedAt     int64  `json:"started_at"`
	FinishedAt    int64  `json:"finished_at"`
	RunProgress
}

// RunStartRequest is the body of POST /backup/jobs/{id}/runs (dev; rev 1.0
// §6). Idempotent by ClientRunUUID.
type RunStartRequest struct {
	ClientRunUUID string `json:"client_run_uuid"`
	Trigger       string `json:"trigger"`
}

// RunStartResponse returns the run id (also on replay).
type RunStartResponse struct {
	RunID int64 `json:"run_id"`
}

// Issue severities (rev 1.0 §6).
const (
	IssueSeverityWarning = "warning"
	IssueSeveritySkipped = "skipped"
	IssueSeverityError   = "error"
)

// Issue classes: rev 1.0 §6 plus the three added by rev 1.1 §6.
const (
	IssueClassLocked              = "locked"
	IssueClassAccessDenied        = "access_denied"
	IssueClassNotFound            = "not_found"
	IssueClassPathTooLong         = "path_too_long"
	IssueClassIO                  = "io"
	IssueClassNetwork             = "network"
	IssueClassServer              = "server"
	IssueClassCrypto              = "crypto"
	IssueClassCloudPlaceholder    = "cloud_placeholder"
	IssueClassVSS                 = "vss"
	IssueClassOther               = "other"
	IssueClassProfileUnauthorised = "profile_unauthorised"
	IssueClassModuleUnavailable   = "module_unavailable"
	IssueClassGuardBlocked        = "guard_blocked"
)

// Issue resolutions (rev 1.0 §6).
const (
	IssueResolutionOpen       = "open"
	IssueResolutionSkipped    = "skipped"
	IssueResolutionSkippedAll = "skipped_all"
	IssueResolutionRetried    = "retried"
	IssueResolutionResolved   = "resolved"
)

// IssueBatchMax is the maximum number of issues per POST
// /backup/runs/{id}/issues call.
const IssueBatchMax = 500

// Issue is one warning, skip or error recorded for a run (rev 1.0 §6).
// RetryRunID is null until a retry run picked it up.
type Issue struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	Path       string `json:"path"`
	Class      string `json:"class"`
	Severity   string `json:"severity"`
	Detail     string `json:"detail"`
	FirstSeen  int64  `json:"first_seen"`
	LastSeen   int64  `json:"last_seen"`
	Count      int64  `json:"count"`
	Retryable  bool   `json:"retryable"`
	Resolution string `json:"resolution"`
	RetryRunID *int64 `json:"retry_run_id"`
}

// IssueReport is one issue as sent by the device: the Issue fields the
// device knows (the server assigns id, run_id, resolution and
// retry_run_id).
type IssueReport struct {
	Path      string `json:"path"`
	Class     string `json:"class"`
	Severity  string `json:"severity"`
	Detail    string `json:"detail"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Count     int64  `json:"count"`
	Retryable bool   `json:"retryable"`
}

// IssueBatchRequest is the body of POST /backup/runs/{id}/issues (dev),
// at most IssueBatchMax items.
type IssueBatchRequest struct {
	Issues []IssueReport `json:"issues"`
}

// IssueSkipRequest is the body of POST /backup/runs/{id}/issues/skip: either
// explicit ids or every issue of a class. Replies with BulkResult.
type IssueSkipRequest struct {
	IDs   []int64 `json:"ids,omitempty"`
	Class string  `json:"class,omitempty"`
}
