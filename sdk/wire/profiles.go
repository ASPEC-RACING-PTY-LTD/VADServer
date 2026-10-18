package wire

// Windows profile states (api-v1 §6B). The agent only touches profiles in
// ProfileStateActiveDefault or ProfileStateAuthorised.
const (
	ProfileStateActiveDefault         = "active_default"
	ProfileStateRequested             = "requested"
	ProfileStateAuthorised            = "authorised"
	ProfileStateBlacklisted           = "blacklisted"
	ProfileStateClearedPendingConsent = "cleared_pending_consent"
	ProfileStateRevoked               = "revoked"
)

// Consent request scopes.
const (
	ProfileRequestScopeSelected = "selected"
	ProfileRequestScopeAll      = "all"
)

// Security-event signals; the same values are used as blacklist_reason.
const (
	ProfileSignalPasswordChanged     = "password_changed"
	ProfileSignalPasswordReset       = "password_reset"
	ProfileSignalSIDChanged          = "sid_changed"
	ProfileSignalAccountDisabled     = "account_disabled"
	ProfileSignalAccountRemoved      = "account_removed"
	ProfileSignalProfileMoved        = "profile_moved"
	ProfileSignalConsentUnverifiable = "consent_unverifiable"
)

// Security-event evidence kinds.
const (
	ProfileEvidenceLocalPasswordAge     = "local_password_age"
	ProfileEvidenceDPAPIUnprotectFailed = "dpapi_unprotect_failed"
	ProfileEvidenceLDAPPwdLastSet       = "ldap_pwdlastset"
	ProfileEvidenceEventLog4723         = "eventlog_4723"
	ProfileEvidenceEventLog4724         = "eventlog_4724"
	ProfileEvidenceSIDLookup            = "sid_lookup"
)

// Profile is a Windows profile on a device (api-v1 §6B, DeviceProfile).
// Nullable fields are pointers and are always present in replies.
type Profile struct {
	ID                int64   `json:"id"`
	DeviceID          int64   `json:"device_id"`
	SID               string  `json:"sid"`
	Username          string  `json:"username"`
	ProfilePath       string  `json:"profile_path"`
	VaultDockUserID   *int64  `json:"vaultdock_user_id"`
	State             string  `json:"state"`
	ConsentID         *string `json:"consent_id"`
	ConsentAt         *int64  `json:"consent_at"`
	RequestedByUserID *int64  `json:"requested_by_user_id"`
	RequestScope      *string `json:"request_scope"`
	BlacklistedAt     *int64  `json:"blacklisted_at"`
	BlacklistReason   *string `json:"blacklist_reason"`
	ClearedBy         *int64  `json:"cleared_by"`
	ClearedAt         *int64  `json:"cleared_at"`
	UpdatedAt         int64   `json:"updated_at"`
}

// ProfileConsentRecord is a consent the agent recorded locally. ProofDigest is
// the SHA-256 (hex) of the DPAPI-bound consent blob, never the blob itself.
type ProfileConsentRecord struct {
	ConsentID   string `json:"consent_id"`
	SID         string `json:"sid"`
	ConsentAt   int64  `json:"consent_at"`
	ProofDigest string `json:"proof_digest"`
}

// ProfileReportEntry is one profile in the agent's state report.
type ProfileReportEntry struct {
	SID         string `json:"sid"`
	Username    string `json:"username"`
	ProfilePath string `json:"profile_path"`
	State       string `json:"state"`
}

// ProfileReport is the body of PUT /device/profiles (dev): the full state
// report, including consent records.
type ProfileReport struct {
	Profiles []ProfileReportEntry   `json:"profiles"`
	Consents []ProfileConsentRecord `json:"consents"`
}

// ProfileSecurityEventRequest is the body of
// POST /device/profiles/{sid}/security-event (dev).
type ProfileSecurityEventRequest struct {
	Signal     string `json:"signal"`
	ObservedAt int64  `json:"observed_at"`
	Evidence   string `json:"evidence"`
}

// ProfileConsentRequestCreate is the body of
// POST /devices/{id}/profiles/requests (admin): either SIDs or All. It creates
// a profile_consent_request device task; reply DeviceTaskCreated.
type ProfileConsentRequestCreate struct {
	SIDs []string `json:"sids,omitempty"`
	All  bool     `json:"all,omitempty"`
}

// ProfileClearRequest is the body of POST /admin/device-profiles/{id}/clear
// (super). The profile moves to cleared_pending_consent.
type ProfileClearRequest struct {
	ConfirmText string `json:"confirm_text"`
}
