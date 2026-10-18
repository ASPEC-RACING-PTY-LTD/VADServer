package wire

// AI scopes (api-v1 §11).
const (
	AIScopeDiagnostics     = "diagnostics"
	AIScopeRecommendations = "recommendations"
)

// AISettings is the reply of GET /account/ai. The API key is never returned;
// SecretState reports whether one is stored.
type AISettings struct {
	Enabled bool     `json:"enabled"`
	BaseURL string   `json:"base_url"`
	Model   string   `json:"model"`
	Scopes  []string `json:"scopes"`
	SecretState
}

// AISettingsUpdate is the body of PUT /account/ai. Secret is the write-only
// API key (omitted keeps, null clears). BaseURL must be HTTPS and pass the
// SSRF policy (422 endpoint_not_allowed).
type AISettingsUpdate struct {
	Enabled bool         `json:"enabled"`
	BaseURL string       `json:"base_url"`
	Model   string       `json:"model"`
	Scopes  []string     `json:"scopes"`
	Secret  SecretUpdate `json:"secret,omitzero"`
}

// AIDiagnoseRequest is the body of POST /ai/diagnose (rev 1.0 §11): a run
// or a device, plus an optional free-text question.
type AIDiagnoseRequest struct {
	RunID    *int64 `json:"run_id,omitempty"`
	DeviceID *int64 `json:"device_id,omitempty"`
	Question string `json:"question,omitempty"`
}

// AIProposal is one proposed change (rev 1.0 §11). Nothing executes
// directly: a proposal that would change anything is created as an approval
// with requested_by "ai" (ApprovalID).
type AIProposal struct {
	Kind       string `json:"kind"`
	Summary    string `json:"summary"`
	ApprovalID *int64 `json:"approval_id,omitempty"`
}

// AIDiagnoseResponse is the reply of POST /ai/diagnose (rev 1.0 §11).
type AIDiagnoseResponse struct {
	ReportMarkdown string       `json:"report_markdown"`
	Proposals      []AIProposal `json:"proposals"`
}
