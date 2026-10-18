package wire

import "encoding/json"

// Summary is the reply of GET /summary (sess; api-v1 §3). Readiness is one
// of the ReadinessStatus* values (rev 1.0 §3).
type Summary struct {
	PendingDevices      int64  `json:"pending_devices"`
	PendingApprovals    int64  `json:"pending_approvals"`
	RunningJobs         int64  `json:"running_jobs"`
	FailedRuns24h       int64  `json:"failed_runs_24h"`
	UnreadNotifications int64  `json:"unread_notifications"`
	OpenConflicts       int64  `json:"open_conflicts"`
	BlacklistedProfiles int64  `json:"blacklisted_profiles"`
	Readiness           string `json:"readiness"`
}

// Operation kinds (api-v1 §3).
const (
	OperationKindPrune            = "prune"
	OperationKindGC               = "gc"
	OperationKindVerify           = "verify"
	OperationKindReplicate        = "replicate"
	OperationKindCatalogRebuild   = "catalog_rebuild"
	OperationKindRestore          = "restore"
	OperationKindRotateKeys       = "rotate_keys"
	OperationKindSnapshotDownload = "snapshot_download"
)

// Operation states (rev 1.0 §3).
const (
	OperationStateQueued    = "queued"
	OperationStateRunning   = "running"
	OperationStateSucceeded = "succeeded"
	OperationStateFailed    = "failed"
	OperationStateCancelled = "cancelled"
)

// Operation is the reply of GET /operations/{id} (sess, or dev for
// operations it created; api-v1 §3, rev 1.0 §3). Result is kind-specific
// (for example SnapshotDownloadResult; rev 1.0 shows {} when empty) and
// Error is a message, "" unless the operation failed.
type Operation struct {
	ID        int64             `json:"id"`
	Kind      string            `json:"kind"`
	State     string            `json:"state"`
	Progress  OperationProgress `json:"progress"`
	Result    json.RawMessage   `json:"result"`
	Error     string            `json:"error"`
	CreatedAt int64             `json:"created_at"`
	UpdatedAt int64             `json:"updated_at"`
}

// OperationProgress is Operation.progress (api-v1 §3).
type OperationProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
	Bytes int64 `json:"bytes"`
}
