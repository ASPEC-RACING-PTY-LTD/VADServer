package wire

// Readiness statuses (rev 1.0 §3, §11): ReadinessReport.Status and
// Summary.Readiness. Readiness checks use the same values.
const (
	ReadinessStatusOK       = "ok"
	ReadinessStatusWarning  = "warning"
	ReadinessStatusCritical = "critical"
	ReadinessStatusUnknown  = "unknown"
)

// Readiness check ids: rev 1.0 §11 plus the three added by rev 1.1 §11
// (install_mode is informational).
const (
	ReadinessCheckRecentBackup         = "recent_backup"
	ReadinessCheckOpenIssues           = "open_issues"
	ReadinessCheckVSSAvailable         = "vss_available"
	ReadinessCheckVerifiedRecently     = "verified_recently"
	ReadinessCheckTestRestoreRecent    = "test_restore_recent"
	ReadinessCheckRecoveryKeyConfirmed = "recovery_key_confirmed"
	ReadinessCheckServiceInstalled     = "service_installed"
	ReadinessCheckRedundantDestination = "redundant_destination"
	ReadinessCheckNotPortableItems     = "not_portable_items"
	ReadinessCheckProfileAuthorised    = "profile_authorised"
	ReadinessCheckInstallMode          = "install_mode"
	ReadinessCheckModulesHealthy       = "modules_healthy"
)

// ReadinessCheck is one readiness check of a device (rev 1.0 §11).
type ReadinessCheck struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Message string `json:"message"`
	FixLink string `json:"fix_link"`
}

// ReadinessJob is the readiness view of one job (rev 1.0 §11). The
// timestamps are 0 when the event never happened.
type ReadinessJob struct {
	JobID          int64 `json:"job_id"`
	LastSuccessAt  int64 `json:"last_success_at"`
	OpenIssues     int64 `json:"open_issues"`
	VSSUsed        bool  `json:"vss_used"`
	VerifiedAt     int64 `json:"verified_at"`
	TestRestoredAt int64 `json:"test_restored_at"`
}

// ReadinessDevice is the readiness of one device (rev 1.0 §11).
type ReadinessDevice struct {
	DeviceID int64            `json:"device_id"`
	Checks   []ReadinessCheck `json:"checks"`
	Jobs     []ReadinessJob   `json:"jobs"`
}

// ReadinessReport is the reply of GET /readiness?device= (sess; rev 1.0
// §11). Score is 0..100.
type ReadinessReport struct {
	Score   int               `json:"score"`
	Status  string            `json:"status"`
	Devices []ReadinessDevice `json:"devices"`
}

// BackupState is the body of PUT /device/backup-state (dev; rev 1.0 §11):
// the readiness inputs only the device can supply.
type BackupState struct {
	RecoveryKeyConfirmed bool `json:"recovery_key_confirmed"`
	ServiceInstalled     bool `json:"service_installed"`
	VSSAvailable         bool `json:"vss_available"`
}

// Notification events: rev 1.0 §11 plus the two added by rev 1.1 §11.
const (
	NotificationEventBackupFailed       = "backup.failed"
	NotificationEventBackupPartial      = "backup.partial"
	NotificationEventReadinessDegraded  = "readiness.degraded"
	NotificationEventQuotaWarning       = "quota.warning"
	NotificationEventReplicationLagging = "replication.lagging"
	NotificationEventApprovalPending    = "approval.pending"
	NotificationEventVerifyFailed       = "verify.failed"
	NotificationEventProfileBlacklisted = "profile.blacklisted"
	NotificationEventModuleFailed       = "module.failed"
)

// Notification channels (rev 1.0 §11).
const (
	NotificationChannelInApp   = "in_app"
	NotificationChannelDesktop = "desktop"
	NotificationChannelEmail   = "email"
	NotificationChannelWebhook = "webhook"
)

// NotificationPref enables or disables one event on one channel. GET/PUT
// /account/notification-prefs carries a bare JSON array of them
// ([]NotificationPref, rev 1.0 §11).
type NotificationPref struct {
	Event   string `json:"event"`
	Channel string `json:"channel"`
	Enabled bool   `json:"enabled"`
}
