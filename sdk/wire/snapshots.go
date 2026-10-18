package wire

// SnapshotCommitRequest is the body of POST /repos/{id}/snapshots (dev,
// streamed; api-v1 §5.2): the rev 1.0 fields plus Epoch and
// ProfileConsentID. The commit is idempotent by SnapshotOID.
// ParentSnapshotID is null for the first snapshot of a job.
type SnapshotCommitRequest struct {
	SnapshotOID      string        `json:"snapshot_oid"`
	JobID            int64         `json:"job_id"`
	RunID            int64         `json:"run_id"`
	ParentSnapshotID *int64        `json:"parent_snapshot_id"`
	Kind             string        `json:"kind"`
	Mode             string        `json:"mode"`
	Hostname         string        `json:"hostname"`
	OSUser           string        `json:"os_user"`
	UserSID          string        `json:"user_sid"`
	ProfilePath      string        `json:"profile_path"`
	ClientTime       int64         `json:"client_time"`
	Tags             []string      `json:"tags"`
	Stats            SnapshotStats `json:"stats"`
	IndexOIDs        []string      `json:"index_oids"`
	PackOIDs         []string      `json:"pack_oids"`
	Epoch            int64         `json:"epoch"`
	ProfileConsentID string        `json:"profile_consent_id"`
}

// SnapshotStats is the cleartext statistics block of a snapshot (rev 1.0
// §5.2 "stats").
type SnapshotStats struct {
	FileCount    int64 `json:"file_count"`
	DirCount     int64 `json:"dir_count"`
	LogicalBytes int64 `json:"logical_bytes"`
	NewBytes     int64 `json:"new_bytes"`
	NewObjects   int64 `json:"new_objects"`
	DurationMS   int64 `json:"duration_ms"`
}

// SnapshotCommitResponse is the reply of a commit: 201 on first commit, 200
// on replay (api-v1 §5.2).
type SnapshotCommitResponse struct {
	SnapshotID int64 `json:"snapshot_id"`
}

// Snapshot catalog ingestion states (api-v1 §5.4).
const (
	SnapshotIngestPending = "pending"
	SnapshotIngestDone    = "done"
	SnapshotIngestFailed  = "failed"
)

// Snapshot catalog statuses (rev 1.0 §5.4).
const (
	SnapshotStatusCommitted = "committed"
	SnapshotStatusForgotten = "forgotten"
	SnapshotStatusMissing   = "missing"
)

// Snapshot is a catalog entry (sess; api-v1 §5.4): the rev 1.0 fields plus
// Epoch, Profile and IngestState. It is returned by GET /snapshots (as
// List[Snapshot]), GET /snapshots/{id} and PATCH /snapshots/{id}.
// Browsable is true when IngestState is done. ParentID is null for the first
// snapshot of a job; ProtectedUntil is null when protection has no end.
type Snapshot struct {
	ID             int64           `json:"id"`
	RepoID         int64           `json:"repo_id"`
	SnapshotOID    string          `json:"snapshot_oid"`
	JobID          int64           `json:"job_id"`
	DeviceID       int64           `json:"device_id"`
	ParentID       *int64          `json:"parent_id"`
	Kind           string          `json:"kind"`
	Mode           string          `json:"mode"`
	Hostname       string          `json:"hostname"`
	OSUser         string          `json:"os_user"`
	ProfilePath    string          `json:"profile_path"`
	ClientTime     int64           `json:"client_time"`
	CommittedAt    int64           `json:"committed_at"`
	Stats          SnapshotStats   `json:"stats"`
	Tags           []string        `json:"tags"`
	Protected      bool            `json:"protected"`
	ProtectedUntil *int64          `json:"protected_until"`
	Status         string          `json:"status"`
	Browsable      bool            `json:"browsable"`
	Epoch          int64           `json:"epoch"`
	Profile        SnapshotProfile `json:"profile"`
	IngestState    string          `json:"ingest_state"`
}

// SnapshotProfile is the Windows profile a snapshot was taken from
// (api-v1 §5.4).
type SnapshotProfile struct {
	SID      string `json:"sid"`
	Username string `json:"username"`
}
