package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Device task kinds (api-v1 §6).
const (
	TaskKindBackupNow             = "backup_now"
	TaskKindRestore               = "restore"
	TaskKindRetrySkipped          = "retry_skipped"
	TaskKindVerify                = "verify"
	TaskKindPruneCompact          = "prune_compact"
	TaskKindTestRestore           = "test_restore"
	TaskKindAutomationOp          = "automation_op"
	TaskKindKeyShare              = "key_share"
	TaskKindRewrap                = "rewrap"
	TaskKindProfileConsentRequest = "profile_consent_request"
	TaskKindModeChangeRequest     = "mode_change_request"
)

// Device task statuses (rev 1.0 §6).
const (
	TaskStatusPending   = "pending"
	TaskStatusClaimed   = "claimed"
	TaskStatusDone      = "done"
	TaskStatusFailed    = "failed"
	TaskStatusCancelled = "cancelled"
)

// Device task creators (DeviceTask.CreatedBy, rev 1.0 §6).
const (
	TaskCreatedByUser       = "user"
	TaskCreatedByAutomation = "automation"
	TaskCreatedByAI         = "ai"
	TaskCreatedByServer     = "server"
)

// DeviceTask is a unit of work the server queues for a device (rev 1.0 §6).
// Payload is polymorphic by Kind; decode it with DecodeTaskPayload.
// ApprovalID is null when no approval gates the task. RequiredModules is the
// rev 1.1 §6 module gate. ClaimedAt and FinishedAt are 0 until set.
type DeviceTask struct {
	ID              int64           `json:"id"`
	DeviceID        int64           `json:"device_id"`
	Kind            string          `json:"kind"`
	Payload         json.RawMessage `json:"payload"`
	Status          string          `json:"status"`
	ApprovalID      *int64          `json:"approval_id"`
	RequiredModules []string        `json:"required_modules"`
	CreatedBy       string          `json:"created_by"`
	CreatedAt       int64           `json:"created_at"`
	ClaimedAt       int64           `json:"claimed_at"`
	FinishedAt      int64           `json:"finished_at"`
	Result          json.RawMessage `json:"result"`
}

// DeviceTaskPollResponse is the reply of GET /device/tasks?wait=0..25 (dev;
// rev 1.0 §6): the tasks claimed by this poll, empty when the wait expired.
type DeviceTaskPollResponse struct {
	Items []DeviceTask `json:"items"`
}

// DeviceTaskCreateRequest is the body of POST /device/tasks (sess).
type DeviceTaskCreateRequest struct {
	DeviceID int64           `json:"device_id"`
	Kind     string          `json:"kind"`
	Payload  json.RawMessage `json:"payload"`
}

// DeviceTaskCreated is the reply of a route that queues a device task
// (POST /device/tasks, …/retry-skipped, mode and profile requests).
// ApprovalID is set when the task is gated: destructive kinds (restore,
// prune_compact) reply 202 {"task_id","approval_id"} (rev 1.0 §6).
type DeviceTaskCreated struct {
	TaskID     int64  `json:"task_id"`
	ApprovalID *int64 `json:"approval_id,omitempty"`
}

// DeviceTaskResultRequest is the body of POST /device/tasks/{id}/result
// (dev; rev 1.0 §6). Status is TaskStatusDone or TaskStatusFailed.
type DeviceTaskResultRequest struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result"`
}

// TaskBackupNowPayload is the payload of backup_now.
type TaskBackupNowPayload struct {
	JobID int64 `json:"job_id"`
}

// TaskRestorePayload is the payload of restore: the device fetches the
// approved restore RestoreID.
type TaskRestorePayload struct {
	RestoreID int64 `json:"restore_id"`
}

// TaskRetrySkippedPayload is the payload of retry_skipped; no IssueIDs means
// every skipped issue of the run.
type TaskRetrySkippedPayload struct {
	RunID    int64   `json:"run_id"`
	IssueIDs []int64 `json:"issue_ids,omitempty"`
}

// Verify task depths (TaskVerifyPayload.Depth, rev 1.0 §6).
const (
	TaskVerifyDepthMetadata = "metadata"
	TaskVerifyDepthSample   = "sample"
	TaskVerifyDepthFull     = "full"
)

// TaskVerifyPayload is the payload of verify (client-side verify).
// SamplePct applies to depth sample.
type TaskVerifyPayload struct {
	RepoID     int64  `json:"repo_id"`
	SnapshotID *int64 `json:"snapshot_id,omitempty"`
	Depth      string `json:"depth"`
	SamplePct  int    `json:"sample_pct,omitempty"`
}

// TaskPruneCompactPayload is the payload of prune_compact (metadata escrow,
// api-v1 §9).
type TaskPruneCompactPayload struct {
	RepoID      int64 `json:"repo_id"`
	PrunePlanID int64 `json:"prune_plan_id"`
}

// TaskTestRestorePayload is the payload of test_restore.
type TaskTestRestorePayload struct {
	SnapshotID  int64 `json:"snapshot_id"`
	SampleFiles int   `json:"sample_files"`
}

// TaskAutomationOpPayload is the payload of automation_op: one
// device-runtime node of an automation (rev 1.0 §6). NodeID is the node id
// of the automation definition.
type TaskAutomationOpPayload struct {
	AutomationID int64          `json:"automation_id"`
	NodeID       string         `json:"node_id"`
	Op           TaskAutomation `json:"op"`
}

// TaskAutomation is TaskAutomationOpPayload.op.
type TaskAutomation struct {
	Kind   string          `json:"kind"`
	Params json.RawMessage `json:"params"`
}

// TaskKeySharePayload is the payload of key_share (api-v1 §5A). Under full
// escrow it carries RepoID and KeyFileOID; under metadata escrow it goes to
// an existing device with RepoID, TargetDeviceID, TargetBoxPub and
// TargetSignPub (the target's VD1 Ed25519 key, std base64; rev 1.1b).
type TaskKeySharePayload struct {
	RepoID         int64  `json:"repo_id"`
	KeyFileOID     string `json:"key_file_oid,omitempty"`
	TargetDeviceID int64  `json:"target_device_id,omitempty"`
	TargetBoxPub   string `json:"target_box_pub,omitempty"`
	TargetSignPub  string `json:"target_sign_pub,omitempty"`
}

// TaskRewrapPayload is the payload of rewrap.
type TaskRewrapPayload struct {
	RepoID int64 `json:"repo_id"`
	Epoch  int64 `json:"epoch"`
}

// TaskProfileConsentRequestPayload is the payload of profile_consent_request.
type TaskProfileConsentRequestPayload struct {
	RequestID   string   `json:"request_id"`
	SIDs        TaskSIDs `json:"sids"`
	RequestedBy string   `json:"requested_by"`
}

// TaskModeChangeRequestPayload is the payload of mode_change_request. A local
// Windows administrator must always confirm it on the device.
type TaskModeChangeRequestPayload struct {
	TargetMode  string `json:"target_mode"`
	RequestedBy string `json:"requested_by"`
}

// TaskSIDs is a list of Windows profile SIDs or the string "all".
type TaskSIDs struct {
	All  bool
	SIDs []string
}

// MarshalJSON writes "all" or the SID array.
func (s TaskSIDs) MarshalJSON() ([]byte, error) {
	if s.All {
		return []byte(`"all"`), nil
	}
	if s.SIDs == nil {
		return []byte("[]"), nil
	}
	return json.Marshal(s.SIDs)
}

// UnmarshalJSON accepts "all" or an array of SIDs.
func (s *TaskSIDs) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) > 0 && b[0] == '"' {
		var v string
		if err := json.Unmarshal(b, &v); err != nil {
			return err
		}
		if v != "all" {
			return fmt.Errorf("wire: sids must be an array or \"all\", got %q", v)
		}
		*s = TaskSIDs{All: true}
		return nil
	}
	var list []string
	if err := json.Unmarshal(b, &list); err != nil {
		return err
	}
	*s = TaskSIDs{SIDs: list}
	return nil
}

// ErrUnknownTaskKind is returned by DecodeTaskPayload for a kind not listed
// in api-v1 §6.
var ErrUnknownTaskKind = errors.New("wire: unknown device task kind")

// DecodeTaskPayload decodes a device task payload into the typed struct for
// kind (for example *TaskRewrapPayload). Unknown fields are rejected.
func DecodeTaskPayload(kind string, payload json.RawMessage) (any, error) {
	var v any
	switch kind {
	case TaskKindBackupNow:
		v = &TaskBackupNowPayload{}
	case TaskKindRestore:
		v = &TaskRestorePayload{}
	case TaskKindRetrySkipped:
		v = &TaskRetrySkippedPayload{}
	case TaskKindVerify:
		v = &TaskVerifyPayload{}
	case TaskKindPruneCompact:
		v = &TaskPruneCompactPayload{}
	case TaskKindTestRestore:
		v = &TaskTestRestorePayload{}
	case TaskKindAutomationOp:
		v = &TaskAutomationOpPayload{}
	case TaskKindKeyShare:
		v = &TaskKeySharePayload{}
	case TaskKindRewrap:
		v = &TaskRewrapPayload{}
	case TaskKindProfileConsentRequest:
		v = &TaskProfileConsentRequestPayload{}
	case TaskKindModeChangeRequest:
		v = &TaskModeChangeRequestPayload{}
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownTaskKind, kind)
	}
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return nil, fmt.Errorf("wire: decode %s payload: %w", kind, err)
	}
	return v, nil
}

// DecodePayload decodes t.Payload according to t.Kind.
func (t DeviceTask) DecodePayload() (any, error) { return DecodeTaskPayload(t.Kind, t.Payload) }

// Inventory app sources (backup-format rev 1.0 §8.3 inventory/1).
const (
	InventorySourceWinget  = "winget"
	InventorySourceMSStore = "msstore"
	InventorySourceChoco   = "choco"
	InventorySourceScoop   = "scoop"
	InventorySourceMSI     = "msi"
	InventorySourceExe     = "exe"
	InventorySourceSteam   = "steam"
	InventorySourceEpic    = "epic"
	InventorySourceGOG     = "gog"
)

// Inventory app match methods (backup-format rev 1.0 §8.3).
const (
	InventoryMatchExactProductCode = "exact_product_code"
	InventoryMatchWingetList       = "winget_list"
	InventoryMatchNamePublisher    = "name_publisher"
	InventoryMatchManual           = "manual"
	InventoryMatchNone             = "none"
)

// Inventory app install scopes and flags (backup-format rev 1.0 §8.3).
const (
	InventoryScopeUser                 = "user"
	InventoryScopeMachine              = "machine"
	InventoryFlagNotPortable           = "not_portable"
	InventoryFlagSettingsBeforeInstall = "settings_before_install"
)

// InventorySchema is Inventory.Schema.
const InventorySchema = "inventory/1"

// Inventory is the body of PUT /device/inventory (dev, streamed, opt-in,
// discovery module): the plaintext inventory/1 JSON of backup-format §8.3
// (rev 1.0 text).
type Inventory struct {
	Schema      string                 `json:"schema"`
	Apps        []InventoryApp         `json:"apps"`
	Games       []InventoryGame        `json:"games"`
	DevProjects []InventoryDevProject  `json:"devprojects"`
	NotPortable []InventoryNotPortable `json:"not_portable"`
}

// InventoryApp is one installed application of inventory/1. Tier is 1..6.
// DataRoots are token paths; Registry lists registry/1 meta blob ids.
type InventoryApp struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Publisher       string   `json:"publisher"`
	Source          string   `json:"source"`
	PackageID       string   `json:"package_id"`
	MatchMethod     string   `json:"match_method"`
	Tier            int      `json:"tier"`
	InstallScope    string   `json:"install_scope"`
	Mode            string   `json:"mode"`
	InstallLocation string   `json:"install_location"`
	UninstallKey    string   `json:"uninstall_key"`
	Flags           []string `json:"flags"`
	DataRoots       []string `json:"data_roots"`
	Registry        []string `json:"registry"`
}

// InventoryGame is one game of inventory/1. ManifestSource is
// "ludusavi@<rev>" or "builtin".
type InventoryGame struct {
	Name           string   `json:"name"`
	Store          string   `json:"store"`
	AppID          string   `json:"app_id"`
	SaveRoots      []string `json:"save_roots"`
	ManifestSource string   `json:"manifest_source"`
}

// InventoryDevProject is one development project of inventory/1.
type InventoryDevProject struct {
	Root   string `json:"root"`
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`
	Pushed bool   `json:"pushed"`
	Mode   string `json:"mode"`
}

// InventoryNotPortable is one not_portable entry of inventory/1.
type InventoryNotPortable struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

// DeviceApp is one item of GET /devices/{id}/apps?cursor= (sess; rev 1.0
// §6), returned as List[DeviceApp]. LastSnapshotID is null when no snapshot
// holds the app's data.
type DeviceApp struct {
	Name           string   `json:"name"`
	Version        string   `json:"version"`
	Publisher      string   `json:"publisher"`
	Source         string   `json:"source"`
	PackageID      string   `json:"package_id"`
	MatchMethod    string   `json:"match_method"`
	Tier           int      `json:"tier"`
	InstallScope   string   `json:"install_scope"`
	Mode           string   `json:"mode"`
	DataBytes      int64    `json:"data_bytes"`
	LastSnapshotID *int64   `json:"last_snapshot_id"`
	Flags          []string `json:"flags"`
}
