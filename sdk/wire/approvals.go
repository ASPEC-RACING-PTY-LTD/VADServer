package wire

// Approval kinds: rev 1.0 §8 plus escrow_change and two_way_enable from
// rev 1.1 (§5A, §8, §10.1).
const (
	ApprovalKindRestore           = "restore"
	ApprovalKindPrune             = "prune"
	ApprovalKindGC                = "gc"
	ApprovalKindUnprotectSnapshot = "unprotect_snapshot"
	ApprovalKindDeviceTask        = "device_task"
	ApprovalKindAutomationOp      = "automation_op"
	ApprovalKindAIProposal        = "ai_proposal"
	ApprovalKindEscrowChange      = "escrow_change"
	ApprovalKindTwoWayEnable      = "two_way_enable"
)

// Approval subject types (rev 1.0 §8).
const (
	ApprovalSubjectRestore    = "restore"
	ApprovalSubjectPrunePlan  = "prune_plan"
	ApprovalSubjectSnapshot   = "snapshot"
	ApprovalSubjectDeviceTask = "device_task"
	ApprovalSubjectAutomation = "automation"
)

// Approval states (rev 1.0 §8). Consumed means executed.
const (
	ApprovalStatePending  = "pending"
	ApprovalStateApproved = "approved"
	ApprovalStateRejected = "rejected"
	ApprovalStateExpired  = "expired"
	ApprovalStateConsumed = "consumed"
)

// Approval risk levels (rev 1.0 §8).
const (
	ApprovalRiskLow    = "low"
	ApprovalRiskMedium = "medium"
	ApprovalRiskHigh   = "high"
)

// Approval requesters (Approval.RequestedBy, rev 1.0 §8).
const (
	ApprovalRequestedByUser       = "user"
	ApprovalRequestedByAutomation = "automation"
	ApprovalRequestedByAI         = "ai"
	ApprovalRequestedByDevice     = "device"
)

// Approval is a gated destructive action awaiting a decision (rev 1.0 §8).
// PlanDigest is the SHA-256 hex of the canonical plan JSON. DecidedBy (a
// user id) and DecidedAt are null until decided.
type Approval struct {
	ID           int64  `json:"id"`
	Kind         string `json:"kind"`
	SubjectType  string `json:"subject_type"`
	SubjectID    int64  `json:"subject_id"`
	PlanDigest   string `json:"plan_digest"`
	Summary      string `json:"summary"`
	Risk         string `json:"risk"`
	RequestedBy  string `json:"requested_by"`
	RequesterRef string `json:"requester_ref"`
	State        string `json:"state"`
	ExpiresAt    int64  `json:"expires_at"`
	DecidedBy    *int64 `json:"decided_by"`
	DecidedAt    *int64 `json:"decided_at"`
	Reason       string `json:"reason"`
}

// ApprovalDecisionRequest is the body of POST /approvals/{id}/approve.
// ConfirmText is required for high-risk approvals and must equal the
// subject name shown in the summary.
type ApprovalDecisionRequest struct {
	ConfirmText string `json:"confirm_text,omitempty"`
}

// ApprovalRejectRequest is the body of POST /approvals/{id}/reject.
type ApprovalRejectRequest struct {
	Reason string `json:"reason"`
}
