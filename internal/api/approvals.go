// Owner: SRV-3 (P1-17). Scope: api-v1 §8 approvals (feature approvals.v1);
// paths and rules per rev 1.0 (sdk/contracts/history/api-v1-rev1.0.md §8).
//
// Other feature areas gate destructive actions with RequireApproval (or
// acceptApproval in a handler) and register an ApprovalHandler per approval
// kind; approving resumes the action through that handler.

package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func init() { implementFeature(wire.FeatureApprovals) }

// mountApprovals registers the routes of this feature area.
func (s *Server) mountApprovals(rt routes) {
	rt.Sess.Get("/approvals", s.handleListApprovals)
	rt.Sess.Get("/approvals/{id}", s.handleGetApproval)
	rt.Sess.Post("/approvals/{id}/approve", s.handleApprove)
	rt.Sess.Post("/approvals/{id}/reject", s.handleReject)
}

// --- handler registry ---

// ApprovalHandler resumes the action an approval gates. Register one per
// approval kind with RegisterApprovalHandler, from the feature's init().
//
//   - Digest, when set, recomputes the plan digest of the subject as it is
//     now; a mismatch with Approval.PlanDigest expires the approval and the
//     approve call fails with 409 plan_stale (rev 1.0 §8 rule 4).
//   - Resume runs the approved action. With OpKind set it runs as a long
//     operation of that kind (api-v1 §3) owned by the approval's owner, and
//     the approval becomes consumed when Resume returns (success or not; the
//     operation carries the outcome). With OpKind "" it runs inside the
//     approve request (p is nil) and must be quick; the approval becomes
//     consumed when it returns nil.
//   - Rejected, when set, runs after a reject or an expiry, to roll the
//     subject back (for example a restore to cancelled).
//
// A kind without a handler is only marked approved; its feature must check
// the state itself and call Store.ConsumeApproval.
type ApprovalHandler struct {
	OpKind   string
	Digest   func(ctx context.Context, s *Server, a *store.Approval) (string, error)
	Resume   func(ctx context.Context, s *Server, a *store.Approval, p *OpProgress) (any, error)
	Rejected func(ctx context.Context, s *Server, a *store.Approval) error
}

var (
	approvalHandlersMu sync.RWMutex
	approvalHandlers   = map[string]ApprovalHandler{}
)

// RegisterApprovalHandler sets the handler for an approval kind (a
// wire.ApprovalKind* value). It panics on an empty kind or a nil Resume.
func RegisterApprovalHandler(kind string, h ApprovalHandler) {
	if kind == "" || h.Resume == nil {
		panic("api: RegisterApprovalHandler needs a kind and a Resume func")
	}
	approvalHandlersMu.Lock()
	approvalHandlers[kind] = h
	approvalHandlersMu.Unlock()
}

func approvalHandler(kind string) (ApprovalHandler, bool) {
	approvalHandlersMu.RLock()
	defer approvalHandlersMu.RUnlock()
	h, ok := approvalHandlers[kind]
	return h, ok
}

// --- creating approvals ---

// ApprovalRequest describes a destructive action to gate behind an approval.
//
// UserID owns the subject and decides (Global: any admin decides instead;
// Superuser: only superusers decide). SubjectName is what a high-risk
// approval must be confirmed with (confirm_text) and should appear in
// Summary. Plan, when set, is digested with store.PlanDigest; otherwise
// PlanDigest is used as given. Data is kept for the handler (JSON). TTL 0
// means 72 h.
type ApprovalRequest struct {
	UserID              int64
	Kind                string
	SubjectType         string
	SubjectID           int64
	SubjectName         string
	Summary             string
	Risk                string
	Plan                any
	PlanDigest          string
	Data                any
	RequestedBy         string
	RequesterRef        string
	RequestedByUserID   *int64
	RequestedByDeviceID *int64
	Global              bool
	Superuser           bool
	TTL                 time.Duration
}

// RequireApproval creates a pending approval (rev 1.0 §8: approvals are only
// created by the server) and returns the 202 body for the gated action.
// OperationID is always null: the operation starts on approval.
func (s *Server) RequireApproval(ctx context.Context, req ApprovalRequest) (*wire.ApprovalAccepted, error) {
	a, err := s.createApproval(ctx, req)
	if err != nil {
		return nil, err
	}
	return &wire.ApprovalAccepted{ApprovalID: a.ID}, nil
}

func (s *Server) createApproval(ctx context.Context, req ApprovalRequest) (*store.Approval, error) {
	if req.UserID <= 0 || req.Kind == "" {
		return nil, errors.New("approval: user and kind required")
	}
	switch req.Risk {
	case "":
		req.Risk = wire.ApprovalRiskMedium
	case wire.ApprovalRiskLow, wire.ApprovalRiskMedium, wire.ApprovalRiskHigh:
	default:
		return nil, fmt.Errorf("approval: invalid risk %q", req.Risk)
	}
	if req.Risk == wire.ApprovalRiskHigh && req.SubjectName == "" {
		return nil, errors.New("approval: a high-risk approval needs a subject name")
	}
	switch req.RequestedBy {
	case "", wire.ApprovalRequestedByUser, wire.ApprovalRequestedByAutomation, wire.ApprovalRequestedByAI, wire.ApprovalRequestedByDevice:
	default:
		return nil, fmt.Errorf("approval: invalid requested_by %q", req.RequestedBy)
	}
	digest := req.PlanDigest
	if req.Plan != nil {
		var err error
		if _, digest, err = store.PlanDigest(req.Plan); err != nil {
			return nil, err
		}
	}
	var data json.RawMessage
	if req.Data != nil {
		b, err := json.Marshal(req.Data)
		if err != nil {
			return nil, err
		}
		data = b
	}
	a := &store.Approval{
		UserID: req.UserID, Kind: req.Kind, Risk: req.Risk, Title: req.SubjectName, Summary: req.Summary,
		SubjectType: req.SubjectType, SubjectID: req.SubjectID, PlanDigest: digest,
		RequestedBy: req.RequestedBy, RequesterRef: req.RequesterRef, Global: req.Global, Superuser: req.Superuser, Data: data,
		RequestedByUserID: req.RequestedByUserID, RequestedByDeviceID: req.RequestedByDeviceID,
	}
	if req.TTL > 0 {
		a.ExpiresAt = time.Now().Add(req.TTL).UnixMilli()
	}
	if err := s.st.CreateApproval(ctx, a); err != nil {
		return nil, err
	}
	s.notifyApprovalPending(ctx, a)
	return a, nil
}

// acceptApproval creates an approval requested by the caller (user, plus
// device for signed requests) and replies 202 {"approval_id","operation_id"}.
func (s *Server) acceptApproval(w http.ResponseWriter, r *http.Request, req ApprovalRequest) {
	if p := principal(r); p != nil && p.User != nil {
		if req.RequestedByUserID == nil {
			uid := p.User.ID
			req.RequestedByUserID = &uid
		}
		if req.RequestedByDeviceID == nil {
			req.RequestedByDeviceID = p.deviceID()
		}
		if req.RequestedBy == "" && p.Device != nil {
			req.RequestedBy = wire.ApprovalRequestedByDevice
		}
	}
	acc, err := s.RequireApproval(r.Context(), req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, acc)
}

// notifyApprovalPending raises the in-app notification for a new approval.
func (s *Server) notifyApprovalPending(ctx context.Context, a *store.Approval) {
	msg := a.Summary
	if msg == "" {
		msg = "An action is waiting for your approval."
	}
	if err := s.st.AddNotification(ctx, a.UserID, store.Notification{Title: "Approval required", Message: msg, Level: "warning", Source: "approval"}); err != nil {
		slog.Warn("approval notification failed", "approval_id", a.ID, "err", err)
	}
}

// --- views and access ---

func approvalView(a *store.Approval) wire.Approval {
	return wire.Approval{
		ID: a.ID, Kind: a.Kind, SubjectType: a.SubjectType, SubjectID: a.SubjectID, PlanDigest: a.PlanDigest,
		Summary: a.Summary, Risk: a.Risk, RequestedBy: a.RequestedBy, RequesterRef: a.RequesterRef,
		State: a.State, ExpiresAt: a.ExpiresAt, DecidedBy: a.DecidedBy, DecidedAt: a.DecidedAt, Reason: a.Reason,
	}
}

// approvalDecided is the reply of approve/reject: the Approval plus the
// operation that resumes it (null when none was started).
type approvalDecided struct {
	wire.Approval
	OperationID *int64 `json:"operation_id"`
}

// canSeeApproval: the owner, or any admin for global and superuser
// approvals (deciding the latter still needs a superuser).
func canSeeApproval(p *Principal, a *store.Approval) bool {
	return a.UserID == p.User.ID || ((a.Global || a.Superuser) && p.User.IsAdmin())
}

// canDecideApproval applies rev 1.0 §8 rules 2–3: only a session user who
// owns the subject decides, or an admin for global subjects; superuser
// approvals need a superuser. Devices never reach these session routes.
func (s *Server) canDecideApproval(ctx context.Context, p *Principal, a *store.Approval) (bool, error) {
	if p == nil || p.Session == nil || p.Device != nil {
		return false, nil
	}
	if a.Superuser {
		if !p.User.IsAdmin() {
			return false, nil
		}
		return s.isSuperuser(ctx, p.User.ID)
	}
	if a.Global {
		return p.User.IsAdmin(), nil
	}
	return a.UserID == p.User.ID, nil
}

// loadApproval returns {id} if the caller may see it, else 404.
func (s *Server) loadApproval(r *http.Request) (*store.Approval, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	s.expireApprovals(r.Context())
	a, err := s.st.Approval(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !canSeeApproval(principal(r), a) {
		return nil, store.ErrNotFound
	}
	return a, nil
}

// expireApprovals expires overdue approvals and runs their Rejected hooks.
func (s *Server) expireApprovals(ctx context.Context) {
	ids, err := s.st.ExpireApprovals(ctx)
	if err != nil {
		slog.Warn("approval expiry failed", "err", err)
		return
	}
	for _, id := range ids {
		if a, err := s.st.Approval(ctx, id); err == nil {
			s.runRejected(ctx, a)
		}
	}
}

func (s *Server) runRejected(ctx context.Context, a *store.Approval) {
	h, ok := approvalHandler(a.Kind)
	if !ok || h.Rejected == nil {
		return
	}
	if err := h.Rejected(ctx, s, a); err != nil {
		slog.Error("approval rejected hook failed", "approval_id", a.ID, "kind", a.Kind, "err", err)
	}
}

func errApprovalNotPending(a *store.Approval) *apiError {
	return &apiError{Status: 409, Code: "approval_not_pending", Message: "This approval was already decided or has expired",
		Details: map[string]any{"state": a.State}}
}

// --- routes ---

func (s *Server) handleListApprovals(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	q := r.URL.Query()
	f := store.ApprovalFilter{UserID: p.User.ID, IncludeGlobal: p.User.IsAdmin(), State: q.Get("state")}
	switch f.State {
	case "", wire.ApprovalStatePending, wire.ApprovalStateApproved, wire.ApprovalStateRejected, wire.ApprovalStateExpired, wire.ApprovalStateConsumed:
	default:
		writeErr(w, r, &apiError{Status: 400, Code: "validation", Message: "Invalid state filter",
			Details: map[string]any{"fields": map[string]string{"state": "unknown approval state"}}})
		return
	}
	var err error
	if f.BeforeID, f.Limit, err = listParams(r); err != nil {
		writeErr(w, r, err)
		return
	}
	s.expireApprovals(r.Context())
	list, err := s.st.ListApprovals(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Approval]{Items: make([]wire.Approval, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, approvalView(&list[i]))
	}
	if len(list) == f.Limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetApproval(w http.ResponseWriter, r *http.Request) {
	a, err := s.loadApproval(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, approvalView(a))
}

func (s *Server) handleApprove(w http.ResponseWriter, r *http.Request) {
	var req wire.ApprovalDecisionRequest
	if err := decodeOptional(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	a, err := s.loadApproval(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	p := principal(r)
	ok, err := s.canDecideApproval(ctx, p, a)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		if a.Superuser {
			writeErr(w, r, errf(403, wire.CodeNotSuperuser, "Only a superuser can decide this approval"))
			return
		}
		writeErr(w, r, errf(403, "forbidden", "You cannot decide this approval"))
		return
	}
	if a.State != wire.ApprovalStatePending {
		writeErr(w, r, errApprovalNotPending(a))
		return
	}
	if a.Risk == wire.ApprovalRiskHigh && subtle.ConstantTimeCompare([]byte(req.ConfirmText), []byte(a.Title)) != 1 {
		writeErr(w, r, &apiError{Status: 400, Code: "validation", Message: "Type the name shown in the summary to confirm",
			Details: map[string]any{"fields": map[string]string{"confirm_text": "must equal the subject name"}}})
		return
	}
	h, hasHandler := approvalHandler(a.Kind)
	if hasHandler && h.Digest != nil {
		cur, err := h.Digest(ctx, s, a)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			writeErr(w, r, err)
			return
		}
		if err != nil || cur != a.PlanDigest {
			if err := s.st.ExpireApproval(ctx, a.ID); err != nil && !errors.Is(err, store.ErrApprovalNotPending) {
				writeErr(w, r, err)
				return
			}
			s.runRejected(ctx, a)
			writeErr(w, r, errf(409, wire.CodePlanStale, "The plan changed since this approval was requested; plan again"))
			return
		}
	}
	a, err = s.st.DecideApproval(ctx, a.ID, p.User.ID, true, "")
	if errors.Is(err, store.ErrApprovalNotPending) {
		cur, _ := s.st.Approval(ctx, a.ID)
		if cur == nil {
			cur = a
		}
		writeErr(w, r, errApprovalNotPending(cur))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, a.UserID, "approval.approve", a.Kind, fmt.Sprintf("approval_id=%d", a.ID), true)
	var opID *int64
	if hasHandler {
		if opID, err = s.resumeApproval(ctx, h, a); err != nil {
			writeErr(w, r, err)
			return
		}
		if cur, err := s.st.Approval(ctx, a.ID); err == nil {
			a = cur
		}
	}
	writeJSON(w, 200, approvalDecided{Approval: approvalView(a), OperationID: opID})
}

// resumeApproval runs the registered handler of an approved approval.
func (s *Server) resumeApproval(ctx context.Context, h ApprovalHandler, a *store.Approval) (*int64, error) {
	if h.OpKind == "" {
		if _, err := h.Resume(ctx, s, a, nil); err != nil {
			return nil, err
		}
		if err := s.st.ConsumeApproval(ctx, a.ID); err != nil && !errors.Is(err, store.ErrApprovalNotPending) {
			return nil, err
		}
		return nil, nil
	}
	owner := a.UserID
	appr := *a
	id, err := s.StartOperation(OpStart{Kind: h.OpKind, UserID: &owner, Params: map[string]any{"approval_id": a.ID}},
		func(ctx context.Context, p *OpProgress) (any, error) {
			defer func() {
				if err := s.st.ConsumeApproval(context.WithoutCancel(ctx), appr.ID); err != nil && !errors.Is(err, store.ErrApprovalNotPending) {
					slog.Error("approval consume failed", "approval_id", appr.ID, "err", err)
				}
			}()
			return h.Resume(ctx, s, &appr, p)
		})
	if err != nil {
		return nil, err
	}
	if err := s.st.SetApprovalOperation(ctx, a.ID, id); err != nil {
		slog.Warn("approval operation link failed", "approval_id", a.ID, "operation_id", id, "err", err)
	}
	return &id, nil
}

func (s *Server) handleReject(w http.ResponseWriter, r *http.Request) {
	var req wire.ApprovalRejectRequest
	if err := decodeOptional(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Reason) > 1000 {
		writeErr(w, r, &apiError{Status: 400, Code: "validation", Message: "Reason too long",
			Details: map[string]any{"fields": map[string]string{"reason": "at most 1000 characters"}}})
		return
	}
	a, err := s.loadApproval(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p := principal(r)
	ok, err := s.canDecideApproval(r.Context(), p, a)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		if a.Superuser {
			writeErr(w, r, errf(403, wire.CodeNotSuperuser, "Only a superuser can decide this approval"))
			return
		}
		writeErr(w, r, errf(403, "forbidden", "You cannot decide this approval"))
		return
	}
	d, err := s.st.DecideApproval(r.Context(), a.ID, p.User.ID, false, req.Reason)
	if errors.Is(err, store.ErrApprovalNotPending) {
		cur, _ := s.st.Approval(r.Context(), a.ID)
		if cur == nil {
			cur = a
		}
		writeErr(w, r, errApprovalNotPending(cur))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.runRejected(r.Context(), d)
	s.activity(r, d.UserID, "approval.reject", d.Kind, fmt.Sprintf("approval_id=%d", d.ID), true)
	writeJSON(w, 200, approvalDecided{Approval: approvalView(d)})
}

// --- small helpers shared with device_tasks.go ---

// decodeOptional decodes a JSON body when one is present; an empty body
// leaves v unchanged.
func decodeOptional(r *http.Request, v any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decode(r, v)
}

// listParams parses ?cursor=&limit= (api-v1 §1 lists). The cursor is the id
// of the last item of the previous page.
func listParams(r *http.Request) (beforeID int64, limit int, err error) {
	q := r.URL.Query()
	limit = 100
	if v := q.Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 500 {
			return 0, 0, &apiError{Status: 400, Code: "validation", Message: "Invalid limit",
				Details: map[string]any{"fields": map[string]string{"limit": "must be 1..500"}}}
		}
		limit = n
	}
	if v := q.Get("cursor"); v != "" {
		n, perr := strconv.ParseInt(v, 10, 64)
		if perr != nil || n <= 0 {
			return 0, 0, &apiError{Status: 400, Code: "validation", Message: "Invalid cursor",
				Details: map[string]any{"fields": map[string]string{"cursor": "invalid"}}}
		}
		beforeID = n
	}
	return beforeID, limit, nil
}
