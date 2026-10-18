package wire

import "encoding/json"

// RetentionRules are the rules of a retention policy (rev 1.0 §9). Zero
// counts are unused, but a policy whose rules are all zero is invalid (400).
// Protected snapshots are always kept.
type RetentionRules struct {
	KeepLast       int      `json:"keep_last"`
	KeepHourly     int      `json:"keep_hourly"`
	KeepDaily      int      `json:"keep_daily"`
	KeepWeekly     int      `json:"keep_weekly"`
	KeepMonthly    int      `json:"keep_monthly"`
	KeepYearly     int      `json:"keep_yearly"`
	KeepWithinDays int      `json:"keep_within_days"`
	KeepTags       []string `json:"keep_tags"`
}

// RetentionPolicy is a stored retention policy (/retention-policies).
type RetentionPolicy struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Rules     RetentionRules `json:"rules"`
	CreatedAt int64          `json:"created_at"`
	UpdatedAt int64          `json:"updated_at"`
}

// RetentionPolicyRequest is the body of POST/PATCH /retention-policies.
type RetentionPolicyRequest struct {
	Name  string         `json:"name"`
	Rules RetentionRules `json:"rules"`
}

// PrunePlanRequest is the body of POST /repos/{id}/prune/plan. Reply is
// PlanResponse with Plan = PrunePlan; POST /repos/{id}/prune/execute
// {plan_token} replies 202 ApprovalAccepted.
type PrunePlanRequest struct {
	PolicyID int64 `json:"policy_id"`
}

// PruneForget is a snapshot the plan forgets (rev 1.0 §9).
type PruneForget struct {
	SnapshotID int64  `json:"snapshot_id"`
	Reason     string `json:"reason"`
}

// PruneKeep is a snapshot the plan keeps, with the rules that keep it
// (rev 1.0 §9).
type PruneKeep struct {
	SnapshotID int64    `json:"snapshot_id"`
	Reasons    []string `json:"reasons"`
}

// PrunePlan is the plan body of a prune (rev 1.0 §9). Partially live packs
// remain until compaction.
type PrunePlan struct {
	Forget                          []PruneForget `json:"forget"`
	Keep                            []PruneKeep   `json:"keep"`
	PacksToDelete                   int64         `json:"packs_to_delete"`
	BytesReclaimed                  int64         `json:"bytes_reclaimed"`
	BytesReclaimableAfterCompaction int64         `json:"bytes_reclaimable_after_compaction"`
}

// GCLocation is the unreferenced data in one storage location.
type GCLocation struct {
	Location string `json:"location"`
	Objects  int64  `json:"objects"`
	Bytes    int64  `json:"bytes"`
}

// GCPlan is the plan body of POST /admin/gc/plan (orphan objects across
// repos; rev 1.0 §9 gives no shape).
type GCPlan struct {
	Locations  []GCLocation `json:"locations"`
	TotalBytes int64        `json:"total_bytes"`
}

// BackupSettings is the body and reply of GET/PUT /admin/backup-settings
// (admin; rev 1.0 §9). DefaultRetentionPolicyID is null when unset.
type BackupSettings struct {
	GraceHours               int    `json:"grace_hours"`
	MaxObjectMB              int    `json:"max_object_mb"`
	ScrubCron                string `json:"scrub_cron"`
	DBBackupCron             string `json:"db_backup_cron"`
	DefaultRetentionPolicyID *int64 `json:"default_retention_policy_id"`
}

// Repository verify depths (VerifyRequest.Depth, rev 1.0 §9): hash is a
// server-side scrub operation; sample and full are device tasks.
const (
	VerifyDepthHash   = "hash"
	VerifyDepthSample = "sample"
	VerifyDepthFull   = "full"
)

// VerifyRequest is the body of POST /repos/{id}/verify (rev 1.0 §9). Reply
// 202 OperationAccepted. SamplePct applies to depth sample.
type VerifyRequest struct {
	Depth     string `json:"depth"`
	SamplePct int    `json:"sample_pct"`
}

// Replication modes (rev 1.0 §9).
const (
	ReplicationModeMirror  = "mirror"
	ReplicationModeCopyNew = "copy_new"
)

// ReplicationStatus is the status block of a replication (rev 1.0 §9).
// LastRunAt is null before the first run.
type ReplicationStatus struct {
	LagObjects int64  `json:"lag_objects"`
	LagBytes   int64  `json:"lag_bytes"`
	LastRunAt  *int64 `json:"last_run_at"`
	State      string `json:"state"`
}

// Replication copies a repository to another destination server-side
// (GET /replication[/{id}], rev 1.0 §9).
type Replication struct {
	ID               int64             `json:"id"`
	SrcRepoID        int64             `json:"src_repo_id"`
	DstDestinationID int64             `json:"dst_destination_id"`
	Mode             string            `json:"mode"`
	Schedule         json.RawMessage   `json:"schedule"`
	Enabled          bool              `json:"enabled"`
	Status           ReplicationStatus `json:"status"`
	CreatedAt        int64             `json:"created_at"`
	UpdatedAt        int64             `json:"updated_at"`
}

// ReplicationRequest is the body of POST/PATCH /replication (rev 1.0 §9).
type ReplicationRequest struct {
	SrcRepoID        int64           `json:"src_repo_id"`
	DstDestinationID int64           `json:"dst_destination_id"`
	Mode             string          `json:"mode"`
	Schedule         json.RawMessage `json:"schedule"`
	Enabled          bool            `json:"enabled"`
}

// Storage analytics groupings (GET /storage/analytics?group=, rev 1.0 §9).
const (
	AnalyticsGroupJob         = "job"
	AnalyticsGroupApp         = "app"
	AnalyticsGroupDevice      = "device"
	AnalyticsGroupDestination = "destination"
)

// AnalyticsPoint is one sample of Analytics.Series (rev 1.0 §9).
type AnalyticsPoint struct {
	T       int64 `json:"t"`
	Logical int64 `json:"logical"`
	Stored  int64 `json:"stored"`
}

// AnalyticsTop is one item of Analytics.Top (rev 1.0 §9); Key is the group
// key.
type AnalyticsTop struct {
	Key         string `json:"key"`
	UniqueBytes int64  `json:"unique_bytes"`
}

// Analytics is the reply of GET /repos/{id}/analytics and
// GET /storage/analytics?group=&range= (rev 1.0 §9).
type Analytics struct {
	LogicalBytes     int64            `json:"logical_bytes"`
	StoredBytes      int64            `json:"stored_bytes"`
	DedupRatio       float64          `json:"dedup_ratio"`
	ReclaimableBytes int64            `json:"reclaimable_bytes"`
	Series           []AnalyticsPoint `json:"series"`
	Top              []AnalyticsTop   `json:"top"`
}
