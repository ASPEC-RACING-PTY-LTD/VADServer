package wire

// EnrolRequest is the body of POST /device/enroll (api-v1 §5A). BoxPub,
// InstallMode and CoreVersion are new in rev 1.1 and required for new
// clients; the other fields are the existing enrolment fields.
// InstallMode is "simple" or "advanced" (api-v1 §6A).
type EnrolRequest struct {
	Login         string `json:"login"`
	Password      string `json:"password"`
	DeviceName    string `json:"device_name"`
	OS            string `json:"os"`
	Identifier    string `json:"identifier"`
	PublicKey     string `json:"public_key"`
	ClientVersion string `json:"client_version"`
	BoxPub        string `json:"box_pub"`
	InstallMode   string `json:"install_mode"`
	CoreVersion   string `json:"core_version"`
}

// EnrolResponse is the 201 reply of POST /device/enroll (existing route,
// unchanged by api-v1 §5A).
type EnrolResponse struct {
	RequestID           int64  `json:"request_id"`
	PollToken           string `json:"poll_token"`
	UserCode            string `json:"user_code"`
	ExpiresAt           int64  `json:"expires_at"`
	PollIntervalSeconds int    `json:"poll_interval_seconds"`
	KeyFingerprint      string `json:"key_fingerprint"`
	Username            string `json:"username"`
}

// Enrolment poll states (existing GET /device/enroll/{id}).
const (
	EnrolStatusPending  = "pending"
	EnrolStatusApproved = "approved"
	EnrolStatusRejected = "rejected"
	EnrolStatusExpired  = "expired"
)

// EnrolStatus is the reply of GET /device/enroll/{id} (api-v1 §5A).
// DeviceID and ServerIdentity are present only once approved; the device
// pins ServerIdentity.SIKFingerprint.
type EnrolStatus struct {
	Status         string          `json:"status"`
	DeviceID       int64           `json:"device_id,omitempty"`
	ExpiresAt      int64           `json:"expires_at"`
	ServerIdentity *ServerIdentity `json:"server_identity,omitempty"`
}

// ServerIdentity is the server's public identity (api-v1 §5A): the SIK
// (Ed25519 identity key) and the current SEK (X25519 escrow key) signed by
// the SIK over sek_pub || u32(sek_version) (backup-format §4.3). Keys and
// signatures are base64.
type ServerIdentity struct {
	SIKPub         string `json:"sik_pub"`
	SIKFingerprint string `json:"sik_fingerprint"`
	SEKPub         string `json:"sek_pub"`
	SEKVersion     uint32 `json:"sek_version"`
	SEKSig         string `json:"sek_sig"`
}

// ServerIdentityResponse is the reply of GET /keys/server (dev, sess;
// api-v1 §5A), shaped like the enrolment poll's server_identity member.
type ServerIdentityResponse struct {
	ServerIdentity ServerIdentity `json:"server_identity"`
}

// The key_share device task payloads of api-v1 §5A are TaskKeySharePayload
// (tasks.go, with the other §6 device task payloads).

// EscrowChangeRequest is the body of PATCH /repos/{id} (sess, owner;
// api-v1 §5A). The reply is 202 ApprovalAccepted with approval kind
// escrow_change; the change rotates the epoch.
type EscrowChangeRequest struct {
	EscrowMode string `json:"escrow_mode"`
}

// POST /repos/{id}/keys/rotate (sess, owner; api-v1 §5A) has no body and
// replies 202 OperationAccepted (operation kind rotate_keys).

// Key recipient kinds (backup-format §4.1), used in KeyRecipient.Kind.
const (
	KeyRecipientDevice     = "device"
	KeyRecipientEscrow     = "escrow"
	KeyRecipientRecovery   = "recovery"
	KeyRecipientPassphrase = "passphrase"
)

// KeyStatus is the reply of GET /repos/{id}/keys/status (sess, dev;
// api-v1 §5A).
type KeyStatus struct {
	CurrentEpoch           int64          `json:"current_epoch"`
	Recipients             []KeyRecipient `json:"recipients"`
	RecoveryWrappedCurrent bool           `json:"recovery_wrapped_current"`
	EscrowMode             string         `json:"escrow_mode"`
}

// KeyRecipient is one key file recipient in KeyStatus (api-v1 §5A).
// DeviceID is set only for kind device.
type KeyRecipient struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Epoch    int64  `json:"epoch"`
	DeviceID int64  `json:"device_id,omitempty"`
}
