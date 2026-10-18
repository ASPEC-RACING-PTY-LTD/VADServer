package wire

// Restore conflict policies (rev 1.0 §7).
const (
	RestoreConflictSkip      = "skip"
	RestoreConflictOverwrite = "overwrite"
	RestoreConflictRename    = "rename"
	RestoreConflictNewer     = "newer"
)

// Restore plan action values (RestorePlanAction.Action). Rev 1.0 does not
// list them; RestoreActionDataOnly marks apps whose reinstall was requested
// but whose target lacks the discovery/reinstall modules (rev 1.1 §7).
const (
	RestoreActionRestore   = "restore"
	RestoreActionSkip      = "skip"
	RestoreActionOverwrite = "overwrite"
	RestoreActionRename    = "rename"
	RestoreActionReinstall = "reinstall"
	RestoreActionDataOnly  = "data_only"
)

// Restore statuses (rev 1.0 §7).
const (
	RestoreStatusAwaitingApproval = "awaiting_approval"
	RestoreStatusQueued           = "queued"
	RestoreStatusRunning          = "running"
	RestoreStatusCompleted        = "completed"
	RestoreStatusPartial          = "partial"
	RestoreStatusFailed           = "failed"
	RestoreStatusCancelled        = "cancelled"
)

// RestoreSelection includes or excludes one snapshot path (rev 1.0 §7).
type RestoreSelection struct {
	Path    string `json:"path"`
	Include bool   `json:"include"`
}

// RestoreTargetUser is RestorePlanRequest.target_user (rev 1.0 §7); both
// fields are optional.
type RestoreTargetUser struct {
	SID         string `json:"sid,omitempty"`
	ProfilePath string `json:"profile_path,omitempty"`
}

// RestorePathMapping maps a source path prefix to a target path prefix.
type RestorePathMapping struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// RestoreMapping controls destination mapping: automatic profile-path
// detection plus explicit overrides (rev 1.0 §7).
type RestoreMapping struct {
	Auto      bool                 `json:"auto"`
	Overrides []RestorePathMapping `json:"overrides"`
}

// RestorePlanRequest is the body of POST /restore/plan (sess; rev 1.0 §7
// plus TargetProfileSID from rev 1.1 §7). Reply is PlanResponse with
// Plan = RestorePlan. TargetProfileSID must be authorised or active_default
// on the target device. ReinstallApps holds package ids; reinstalling needs
// the discovery and reinstall modules on the target.
type RestorePlanRequest struct {
	SnapshotID       int64              `json:"snapshot_id"`
	Selection        []RestoreSelection `json:"selection"`
	TargetDeviceID   int64              `json:"target_device_id"`
	TargetUser       RestoreTargetUser  `json:"target_user"`
	TargetProfileSID string             `json:"target_profile_sid"`
	Mapping          RestoreMapping     `json:"mapping"`
	ConflictPolicy   string             `json:"conflict_policy"`
	ReinstallApps    []string           `json:"reinstall_apps"`
	IncludeRegistry  bool               `json:"include_registry"`
}

// RestorePlanSummary is RestorePlan.summary (rev 1.0 §7).
type RestorePlanSummary struct {
	Files      int64 `json:"files"`
	Bytes      int64 `json:"bytes"`
	Conflicts  int64 `json:"conflicts"`
	Reinstalls int64 `json:"reinstalls"`
}

// RestorePlanMapping is one token mapping of a plan (rev 1.0 §7). Auto is
// true when the server derived it.
type RestorePlanMapping struct {
	Token string `json:"token"`
	From  string `json:"from"`
	To    string `json:"to"`
	Auto  bool   `json:"auto"`
}

// RestorePlanAction is one planned action (the first 1000 are listed).
type RestorePlanAction struct {
	Path       string `json:"path"`
	TargetPath string `json:"target_path"`
	Action     string `json:"action"`
	Size       int64  `json:"size"`
}

// RestorePlanActionsMax is the number of actions a plan lists at most
// (rev 1.0 §7).
const RestorePlanActionsMax = 1000

// RestoreWarning is one plan warning (rev 1.0 §7), for example code
// module_unavailable (rev 1.1 §7).
type RestoreWarning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RestoreGuardItem is an item blocked by system-file protection (api-v1
// §10.3). It can never be overridden through the API.
type RestoreGuardItem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// RestorePlan is the plan body inside PlanResponse for a restore (rev 1.0
// §7 plus the rev 1.1 §7 guard section). NotPortable lists the snapshot
// items that cannot be restored to the target as-is.
type RestorePlan struct {
	Summary     RestorePlanSummary   `json:"summary"`
	Mappings    []RestorePlanMapping `json:"mappings"`
	Actions     []RestorePlanAction  `json:"actions"`
	Warnings    []RestoreWarning     `json:"warnings"`
	NotPortable []string             `json:"not_portable"`
	Guard       []RestoreGuardItem   `json:"guard"`
}

// RestoreExecuteResponse is the 202 reply of POST /restore/execute (rev 1.0
// §7). After approval the server creates a restore device task.
type RestoreExecuteResponse struct {
	RestoreID  int64 `json:"restore_id"`
	ApprovalID int64 `json:"approval_id"`
}

// Restore is the state of a restore (GET /restore/{id}).
type Restore struct {
	ID               int64  `json:"id"`
	SnapshotID       int64  `json:"snapshot_id"`
	TargetDeviceID   int64  `json:"target_device_id"`
	TargetProfileSID string `json:"target_profile_sid"`
	ApprovalID       *int64 `json:"approval_id"`
	TaskID           *int64 `json:"task_id"`
	Status           string `json:"status"`
	FilesTotal       int64  `json:"files_total"`
	FilesDone        int64  `json:"files_done"`
	BytesTotal       int64  `json:"bytes_total"`
	BytesDone        int64  `json:"bytes_done"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
	// Rev 1.1c additions.
	ConflictPolicy  string `json:"conflict_policy,omitempty"`
	CancelRequested bool   `json:"cancel_requested,omitempty"`
	Error           string `json:"error,omitempty"`
	FinishedAt      *int64 `json:"finished_at,omitempty"`
}

// RestoreItem is one item result (GET /restore/{id}/items?status=); for e2e
// style plans the device reports the final per-file actions here.
type RestoreItem struct {
	Path       string `json:"path"`
	TargetPath string `json:"target_path"`
	Action     string `json:"action"`
	Status     string `json:"status"`
	Message    string `json:"message,omitempty"`
	Size       int64  `json:"size,omitempty"`
}

// Restore item statuses (rev 1.1c §14). blocked marks an item refused by
// system-file protection (§10.3); it is never retried.
const (
	RestoreItemPending = "pending"
	RestoreItemDone    = "done"
	RestoreItemSkipped = "skipped"
	RestoreItemFailed  = "failed"
	RestoreItemBlocked = "blocked"
)

// RestoreProgressItemsMax bounds the items of one progress report.
const RestoreProgressItemsMax = 1000

// DeviceRestore is the reply of GET /device/restores/{id} (dev, rev 1.1c):
// what the target device needs to run an approved restore. Mappings are the
// plan's token mappings; an auto mapping with an empty To is resolved by the
// device for TargetProfileSID. Overrides map a source token path prefix to
// an absolute target folder and win over token mappings.
type DeviceRestore struct {
	Restore          Restore              `json:"restore"`
	RepoID           int64                `json:"repo_id"`
	RepoUUID         string               `json:"repo_uuid"`
	SnapshotOID      string               `json:"snapshot_oid"`
	Selection        []RestoreSelection   `json:"selection"`
	Mappings         []RestorePlanMapping `json:"mappings"`
	Overrides        []RestorePathMapping `json:"overrides"`
	TargetProfileSID string               `json:"target_profile_sid"`
	ConflictPolicy   string               `json:"conflict_policy"`
	CancelRequested  bool                 `json:"cancel_requested"`
}

// RestoreProgress is the body of POST /device/restores/{id}/progress (dev,
// rev 1.1c). Status is running or a terminal status (completed, partial,
// failed, cancelled); after a terminal report the restore is final. Items
// (at most RestoreProgressItemsMax per report) are appended.
type RestoreProgress struct {
	Status     string        `json:"status"`
	FilesTotal int64         `json:"files_total"`
	FilesDone  int64         `json:"files_done"`
	BytesTotal int64         `json:"bytes_total"`
	BytesDone  int64         `json:"bytes_done"`
	Error      string        `json:"error,omitempty"`
	Items      []RestoreItem `json:"items,omitempty"`
}

// RestoreProgressReply answers a progress report. CancelRequested tells the
// device to stop and report cancelled.
type RestoreProgressReply struct {
	Status          string `json:"status"`
	CancelRequested bool   `json:"cancel_requested"`
}
