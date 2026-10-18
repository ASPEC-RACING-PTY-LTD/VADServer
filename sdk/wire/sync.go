package wire

import "encoding/json"

// Sync folder directions (api-v1 §10.1). Upload is the default.
const (
	SyncDirectionUpload = "upload"
	SyncDirectionTwoWay = "two_way"
)

// Sync conflict policies.
const (
	SyncConflictKeepBoth   = "keep_both"
	SyncConflictNewerWins  = "newer_wins"
	SyncConflictLocalWins  = "local_wins"
	SyncConflictRemoteWins = "remote_wins"
	SyncConflictAsk        = "ask"
)

// Sync delete policies.
const (
	SyncDeletePropagateToTrash = "propagate_to_trash"
	SyncDeleteIgnore           = "ignore"
)

// Sync folder states.
const (
	SyncStateActive             = "active"
	SyncStatePaused             = "paused"
	SyncStateSuspendedByMode    = "suspended_by_mode"
	SyncStateSuspendedByProfile = "suspended_by_profile"
)

// Default mass-delete threshold.
const (
	SyncMassDeleteDefaultFiles   = 50
	SyncMassDeleteDefaultPercent = 10
)

// SyncMassDeleteThreshold limits a two-way delete batch; exceeding it without
// confirmation returns 409 mass_delete_guard.
type SyncMassDeleteThreshold struct {
	Files   int `json:"files"`
	Percent int `json:"percent"`
}

// SyncFolder is the desired state of a sync folder, pulled by the device.
type SyncFolder struct {
	ID                  int64                   `json:"id"`
	DeviceID            int64                   `json:"device_id"`
	ProfileSID          string                  `json:"profile_sid"`
	LocalPath           string                  `json:"local_path"`
	RemotePath          string                  `json:"remote_path"`
	Direction           string                  `json:"direction"`
	ConflictPolicy      string                  `json:"conflict_policy"`
	DeletePolicy        string                  `json:"delete_policy"`
	MassDeleteThreshold SyncMassDeleteThreshold `json:"mass_delete_threshold"`
	Ruleset             json.RawMessage         `json:"ruleset"`
	Schedule            json.RawMessage         `json:"schedule"`
	Enabled             bool                    `json:"enabled"`
	Revision            int64                   `json:"revision"`
	AckedRevision       int64                   `json:"acked_revision"`
	State               string                  `json:"state"`
	CreatedAt           int64                   `json:"created_at"`
	UpdatedAt           int64                   `json:"updated_at"`
}

// SyncFolderCreateRequest is the body of POST /sync-folders (sess).
type SyncFolderCreateRequest struct {
	DeviceID            int64                    `json:"device_id"`
	ProfileSID          string                   `json:"profile_sid"`
	LocalPath           string                   `json:"local_path"`
	RemotePath          string                   `json:"remote_path"`
	Direction           string                   `json:"direction,omitempty"`
	ConflictPolicy      string                   `json:"conflict_policy,omitempty"`
	DeletePolicy        string                   `json:"delete_policy,omitempty"`
	MassDeleteThreshold *SyncMassDeleteThreshold `json:"mass_delete_threshold,omitempty"`
	Ruleset             json.RawMessage          `json:"ruleset,omitempty"`
	Schedule            json.RawMessage          `json:"schedule,omitempty"`
	Enabled             bool                     `json:"enabled"`
}

// SyncFolderUpdateRequest is the body of PATCH /sync-folders/{id}; omitted
// fields are unchanged. Changing Direction to two_way returns ApprovalAccepted
// (approval two_way_enable). A device may only send AckedRevision and State.
type SyncFolderUpdateRequest struct {
	LocalPath           *string                  `json:"local_path,omitempty"`
	RemotePath          *string                  `json:"remote_path,omitempty"`
	Direction           *string                  `json:"direction,omitempty"`
	ConflictPolicy      *string                  `json:"conflict_policy,omitempty"`
	DeletePolicy        *string                  `json:"delete_policy,omitempty"`
	MassDeleteThreshold *SyncMassDeleteThreshold `json:"mass_delete_threshold,omitempty"`
	Ruleset             json.RawMessage          `json:"ruleset,omitempty"`
	Schedule            json.RawMessage          `json:"schedule,omitempty"`
	Enabled             *bool                    `json:"enabled,omitempty"`
	AckedRevision       *int64                   `json:"acked_revision,omitempty"`
	State               *string                  `json:"state,omitempty"`
}

// Change feed ops (api-v1 §10.2).
const (
	ChangeOpCreate = "create"
	ChangeOpUpdate = "update"
	ChangeOpMove   = "move"
	ChangeOpDelete = "delete"
)

// SyncMaxWaitSeconds is the maximum wait of GET /sync/changes.
const SyncMaxWaitSeconds = 25

// SyncChange is one entry of the change feed.
type SyncChange struct {
	Seq        int64  `json:"seq"`
	NodeID     int64  `json:"node_id"`
	Op         string `json:"op"`
	Path       string `json:"path"`
	OldPath    string `json:"old_path"`
	Revision   int64  `json:"revision"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	ModifiedAt int64  `json:"modified_at"`
	DeviceID   int64  `json:"device_id"`
}

// SyncChangesResponse is the reply of GET /sync/changes. Reset means the
// tombstone window was exceeded and a full reconcile is needed.
type SyncChangesResponse struct {
	Items     []SyncChange `json:"items"`
	NextSince int64        `json:"next_since"`
	Reset     bool         `json:"reset"`
}

// SyncUploadOnConflictKeepBoth is the on_conflict value that stores a
// conflicting upload as a conflict copy instead of failing with 409.
const SyncUploadOnConflictKeepBoth = "keep_both"

// SyncUploadFields are the sync preconditions added to POST /uploads (rev
// 1.0 §10, rev 1.1 §10.2). Embed them in the upload request. BaseRevision
// nil means create. OnConflict "keep_both" stores a conflicting upload as a
// conflict copy (reply conflict:true) instead of failing with 409.
type SyncUploadFields struct {
	FolderID     int64  `json:"folder_id,omitempty"`
	BaseRevision *int64 `json:"base_revision,omitempty"`
	BaseSHA256   string `json:"base_sha256,omitempty"`
	OnConflict   string `json:"on_conflict,omitempty"`
}

// ConflictDetails is ErrorBody.Details of a 409 conflict.
type ConflictDetails struct {
	CurrentRevision   int64  `json:"current_revision"`
	CurrentSHA256     string `json:"current_sha256"`
	CurrentModifiedAt int64  `json:"current_modified_at"`
}

// SyncOp is one device-side delete or move.
type SyncOp struct {
	Op           string `json:"op"`
	Path         string `json:"path"`
	NewPath      string `json:"new_path,omitempty"`
	BaseRevision int64  `json:"base_revision"`
}

// SyncOpsRequest is the body of POST /sync/folders/{id}/ops (dev). Reply
// BulkResult. ConfirmMassDelete must come from the local user.
type SyncOpsRequest struct {
	Ops               []SyncOp `json:"ops"`
	ConfirmMassDelete bool     `json:"confirm_mass_delete"`
}

// Version is one stored version of a synced file (rev 1.0 §10), returned by
// GET /files/{id}/versions?cursor= as List[Version]. OriginDeviceID is null
// for versions not uploaded by a device.
type Version struct {
	ID             int64  `json:"id"`
	SHA256         string `json:"sha256"`
	Size           int64  `json:"size"`
	ModifiedAt     int64  `json:"modified_at"`
	OriginDeviceID *int64 `json:"origin_device_id"`
	CreatedAt      int64  `json:"created_at"`
	Pinned         bool   `json:"pinned"`
}

// VersionPinRequest is the body of PUT /files/{id}/versions/{vid}/pin.
type VersionPinRequest struct {
	Pinned bool `json:"pinned"`
}

// Conflict states.
const (
	ConflictStateOpen     = "open"
	ConflictStateResolved = "resolved"
)

// Conflict resolution choices.
const (
	ConflictChoiceKeepLocal  = "keep_local"
	ConflictChoiceKeepRemote = "keep_remote"
	ConflictChoiceKeepBoth   = "keep_both"
)

// Conflict is a sync conflict awaiting or after resolution.
type Conflict struct {
	ID               int64  `json:"id"`
	FolderID         int64  `json:"folder_id"`
	NodeID           int64  `json:"node_id"`
	DeviceID         int64  `json:"device_id"`
	Path             string `json:"path"`
	State            string `json:"state"`
	LocalSHA256      string `json:"local_sha256"`
	LocalModifiedAt  int64  `json:"local_modified_at"`
	RemoteSHA256     string `json:"remote_sha256"`
	RemoteModifiedAt int64  `json:"remote_modified_at"`
	CreatedAt        int64  `json:"created_at"`
	ResolvedAt       int64  `json:"resolved_at,omitempty"`
}

// ConflictResolveRequest is the body of POST /sync/conflicts/resolve. Reply
// BulkResult.
type ConflictResolveRequest struct {
	IDs    []int64 `json:"ids"`
	Choice string  `json:"choice"`
}
