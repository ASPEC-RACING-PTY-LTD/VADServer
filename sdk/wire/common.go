// Package wire holds the JSON wire types of the VaultDock HTTP API v1
// (sdk/contracts/api-v1.md). The server and the Windows client both use these
// types; neither defines its own copies.
//
// Conventions (api-v1 §1): snake_case JSON, timestamps are int64 Unix
// milliseconds, row ids are int64, object ids are 64 lowercase hex characters.
package wire

import "encoding/json"

// ErrorResponse is the body of every error reply: {"error":{...}}.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// ErrorBody carries a stable machine code, a human message and optional
// details. Validation errors put per-field messages in Details["fields"].
type ErrorBody struct {
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Details map[string]any `json:"details,omitempty"`
}

// Error codes added by api-v1 §1.1.
const (
	CodeInvalidOID                = "invalid_oid"
	CodeRepoLocked                = "repo_locked"
	CodeObjectHashMismatch        = "object_hash_mismatch"
	CodeLockNotFound              = "lock_not_found"
	CodePackMissing               = "pack_missing"
	CodeApprovalRequired          = "approval_required"
	CodeProtectedSnapshot         = "protected_snapshot"
	CodeBackupQuotaExceeded       = "backup_quota_exceeded"
	CodePlanStale                 = "plan_stale"
	CodeConflict                  = "conflict"
	CodeCatalogPending            = "catalog_pending" // rev 1.1c
	CodeDeviceCannotDelete        = "device_cannot_delete"
	CodeTaskNotClaimable          = "task_not_claimable"
	CodeRepoNotFound              = "repo_not_found"
	CodeSnapshotNotFound          = "snapshot_not_found"
	CodeObjectNotFound            = "object_not_found"
	CodeProfileNotFound           = "profile_not_found"
	CodeModuleUnavailable         = "module_unavailable"
	CodeProfileNotAuthorised      = "profile_not_authorised"
	CodeProfileBlacklisted        = "profile_blacklisted"
	CodeNotSuperuser              = "not_superuser"
	CodeEscrowUnavailable         = "escrow_unavailable"
	CodeSIKMismatch               = "sik_mismatch"
	CodeMassDeleteGuard           = "mass_delete_guard"
	CodeEndpointNotAllowed        = "endpoint_not_allowed"
	CodeEnvChangeRequiresApproval = "env_change_requires_approval"
)

// Feature flags advertised in /info and /device/me (api-v1 §1.3). A flag is
// advertised only when the feature is implemented.
const (
	FeatureBackup       = "backup.v1"
	FeatureEscrow       = "escrow.v1"
	FeatureDeviceTasks  = "device_tasks.v1"
	FeatureApprovals    = "approvals.v1"
	FeatureVersions     = "versions.v1"
	FeatureSyncTwoWay   = "sync_two_way.v1"
	FeatureProfiles     = "profiles.v1"
	FeatureModules      = "modules.v1"
	FeatureDestinations = "destinations.v1"
	FeatureAI           = "ai.v1"
	FeatureDeployEnv    = "deploy_env.v1"
	FeatureRestore      = "restore.v1" // rev 1.1c
	// FeatureUploadZstd: upload chunks may be sent with
	// "Content-Encoding: zstd"; X-Chunk-SHA256 stays the hash of the raw chunk.
	FeatureUploadZstd = "upload.zstd"
	// FeatureUploadCompressedFlag: an upload request may carry
	// "compressed": true (the client compresses chunks where that saves
	// bytes); the server then stores the client's frames as they are.
	FeatureUploadCompressedFlag = "upload.compressed_flag"
	// FeatureDirectUpload: a device may verify a direct (LAN) address of the
	// server with POST /devices/direct-probe and send its uploads there.
	FeatureDirectUpload = "upload.direct"
	// FeatureDeferredFinish: POST /uploads/{id}/complete with
	// "X-VD-Finalize: defer" queues the finish and answers 202 without
	// waiting; POST /uploads/results reports the outcomes later.
	FeatureDeferredFinish = "upload.deferred_finish"
)

// List limits for ?cursor=&limit= (api-v1 §1).
const (
	ListDefaultLimit = 100
	ListMaxLimit     = 500
)

// List is a page of items: {"items":[...],"next_cursor":""}. An empty
// NextCursor means there are no more pages.
type List[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor"`
}

// BulkResult reports a bulk action.
type BulkResult struct {
	OKCount int          `json:"ok_count"`
	Failed  []BulkFailed `json:"failed"`
}

// BulkFailed is one failed item of a bulk action.
type BulkFailed struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// OperationAccepted is the 202 reply of a long operation; poll
// GET /operations/{id}.
type OperationAccepted struct {
	OperationID int64 `json:"operation_id"`
}

// ApprovalAccepted is the 202 reply of a destructive action gated behind an
// approval. OperationID is nil until the approval is granted.
type ApprovalAccepted struct {
	ApprovalID  int64  `json:"approval_id"`
	OperationID *int64 `json:"operation_id"`
}

// PlanResponse is the reply of a …/plan route; execute it with
// PlanExecuteRequest before ExpiresAt. A stale token returns 409 plan_stale.
type PlanResponse struct {
	Plan       any    `json:"plan"`
	PlanToken  string `json:"plan_token"`
	PlanDigest string `json:"plan_digest"`
	ExpiresAt  int64  `json:"expires_at"`
}

// PlanExecuteRequest executes a previously returned plan.
type PlanExecuteRequest struct {
	PlanToken string `json:"plan_token"`
}

// SecretState describes a stored secret without revealing it. Secrets are
// never returned; requests set one with a string, clear it with null and keep
// it by omitting the field (see SecretUpdate).
type SecretState struct {
	HasSecret       bool  `json:"has_secret"`
	SecretUpdatedAt int64 `json:"secret_updated_at"`
}

// SecretUpdate is the request form of a secret field. Use it as a value field
// with `json:"secret,omitzero"`: omitted keeps the stored secret (Present is
// false), null clears it (Present true, Value nil) and a string sets it.
type SecretUpdate struct {
	Present bool
	Value   *string
}

// UnmarshalJSON records that the field was present and whether it was null.
func (s *SecretUpdate) UnmarshalJSON(b []byte) error {
	s.Present = true
	if string(b) == "null" {
		s.Value = nil
		return nil
	}
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	s.Value = &v
	return nil
}

// MarshalJSON writes null to clear or the string to set.
func (s SecretUpdate) MarshalJSON() ([]byte, error) {
	if s.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*s.Value)
}

// IsZero reports an omitted field, so omitzero drops it when marshalling.
func (s SecretUpdate) IsZero() bool { return !s.Present }
