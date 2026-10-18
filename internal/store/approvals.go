package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// DefaultApprovalTTL is the default approval expiry (rev 1.0 §8 rule 5).
const DefaultApprovalTTL = 72 * time.Hour

// ErrApprovalNotPending is returned when deciding an approval that is no
// longer pending (already decided, expired or consumed).
var ErrApprovalNotPending = errors.New("approval is not pending")

// Approval is one row of approvals (api-v1 §8). State uses the wire values;
// the DB stores "consumed" as 'executed' (P0-02 schema). Title holds the
// subject name a high-risk approval must be confirmed with. PlanDigest,
// RequesterRef, Global, Superuser and Data live in approvals.payload.
//
// Global marks a subject without a single owner (e.g. server GC): any admin
// may decide. Superuser restricts the decision to superusers (api-v1 §11B).
// Data is the handler's own JSON for resuming the action.
type Approval struct {
	ID                  int64
	UserID              int64 // owner of the subject
	Kind                string
	Risk                string
	State               string
	Title               string
	Summary             string
	SubjectType         string
	SubjectID           int64
	PlanDigest          string
	RequestedBy         string
	RequesterRef        string
	Global              bool
	Superuser           bool
	Data                json.RawMessage
	RequestedByUserID   *int64
	RequestedByDeviceID *int64
	OperationID         *int64
	DecidedBy           *int64
	DecidedAt           *int64
	Reason              string
	CreatedAt           int64
	UpdatedAt           int64
	ExpiresAt           int64
}

type approvalPayload struct {
	PlanDigest   string          `json:"plan_digest"`
	RequesterRef string          `json:"requester_ref,omitempty"`
	Global       bool            `json:"global,omitempty"`
	Superuser    bool            `json:"superuser,omitempty"`
	Data         json.RawMessage `json:"data,omitempty"`
}

func approvalStateToDB(state string) string {
	if state == wire.ApprovalStateConsumed {
		return "executed"
	}
	return state
}

func approvalStateFromDB(status string) string {
	switch status {
	case "executed", "failed":
		return wire.ApprovalStateConsumed
	case "cancelled":
		return wire.ApprovalStateRejected
	}
	return status
}

const approvalCols = "id, user_id, kind, risk, status, title, summary, payload, target_type, target_id, source, requested_by_user_id, requested_by_device_id, operation_id, decided_by, decided_at, decision_note, created_at, updated_at, expires_at"

func scanApproval(sc interface{ Scan(...any) error }) (*Approval, error) {
	var a Approval
	var payload, status string
	var target, ru, rd, op, db, da, exp sql.NullInt64
	err := sc.Scan(&a.ID, &a.UserID, &a.Kind, &a.Risk, &status, &a.Title, &a.Summary, &payload, &a.SubjectType, &target,
		&a.RequestedBy, &ru, &rd, &op, &db, &da, &a.Reason, &a.CreatedAt, &a.UpdatedAt, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var p approvalPayload
	if err := json.Unmarshal([]byte(jsonOr(payload, "{}")), &p); err != nil {
		return nil, err
	}
	a.State = approvalStateFromDB(status)
	a.SubjectID = target.Int64
	a.PlanDigest, a.RequesterRef, a.Global, a.Superuser, a.Data = p.PlanDigest, p.RequesterRef, p.Global, p.Superuser, p.Data
	a.RequestedByUserID, a.RequestedByDeviceID, a.OperationID = nullInt(ru), nullInt(rd), nullInt(op)
	a.DecidedBy, a.DecidedAt = nullInt(db), nullInt(da)
	a.ExpiresAt = exp.Int64
	return &a, nil
}

// CreateApproval inserts a pending approval and sets a.ID, a.State and the
// timestamps. a.ExpiresAt 0 means now + DefaultApprovalTTL.
func (s *Store) CreateApproval(ctx context.Context, a *Approval) error {
	if a.UserID <= 0 || a.Kind == "" {
		return errors.New("approval user and kind required")
	}
	if a.Risk == "" {
		a.Risk = wire.ApprovalRiskMedium
	}
	if a.RequestedBy == "" {
		a.RequestedBy = wire.ApprovalRequestedByUser
	}
	payload, err := json.Marshal(approvalPayload{PlanDigest: a.PlanDigest, RequesterRef: a.RequesterRef, Global: a.Global, Superuser: a.Superuser, Data: a.Data})
	if err != nil {
		return err
	}
	t := now()
	if a.ExpiresAt == 0 {
		a.ExpiresAt = t + DefaultApprovalTTL.Milliseconds()
	}
	a.State, a.CreatedAt, a.UpdatedAt = wire.ApprovalStatePending, t, t
	var target any
	if a.SubjectID != 0 {
		target = a.SubjectID
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO approvals(user_id, kind, risk, status, title, summary, payload, target_type, target_id, source,
		requested_by_user_id, requested_by_device_id, created_at, updated_at, expires_at) VALUES(?,?,?,'pending',?,?,?,?,?,?,?,?,?,?,?)`,
		a.UserID, a.Kind, a.Risk, a.Title, a.Summary, string(payload), a.SubjectType, target, a.RequestedBy,
		a.RequestedByUserID, a.RequestedByDeviceID, t, t, a.ExpiresAt)
	if err != nil {
		return err
	}
	a.ID, err = res.LastInsertId()
	return err
}

// Approval returns an approval by id; callers check visibility. A pending
// approval past its expiry is reported (and stored) as expired.
func (s *Store) Approval(ctx context.Context, id int64) (*Approval, error) {
	a, err := scanApproval(s.DB.QueryRowContext(ctx, "SELECT "+approvalCols+" FROM approvals WHERE id = ?", id))
	if err != nil {
		return nil, err
	}
	if a.State == wire.ApprovalStatePending && a.ExpiresAt > 0 && a.ExpiresAt <= now() {
		if err := s.ExpireApproval(ctx, id); err != nil && !errors.Is(err, ErrApprovalNotPending) {
			return nil, err
		}
		return scanApproval(s.DB.QueryRowContext(ctx, "SELECT "+approvalCols+" FROM approvals WHERE id = ?", id))
	}
	return a, nil
}

// ApprovalFilter selects approvals for ListApprovals: those owned by UserID,
// plus global ones when IncludeGlobal. State is a wire state ("" = all).
type ApprovalFilter struct {
	UserID        int64
	IncludeGlobal bool
	State         string
	BeforeID      int64
	Limit         int
}

// ListApprovals returns approvals newest first.
func (s *Store) ListApprovals(ctx context.Context, f ApprovalFilter) ([]Approval, error) {
	owner := "user_id = ?"
	if f.IncludeGlobal {
		owner = "(user_id = ? OR json_extract(payload, '$.global') = 1)"
	}
	where := []string{owner}
	args := []any{f.UserID}
	switch f.State {
	case "":
	case wire.ApprovalStateConsumed:
		where = append(where, "status IN ('executed','failed')")
	case wire.ApprovalStateRejected:
		where = append(where, "status IN ('rejected','cancelled')")
	default:
		where = append(where, "status = ?")
		args = append(args, f.State)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+approvalCols+" FROM approvals WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Approval{}
	for rows.Next() {
		a, err := scanApproval(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

// DecideApproval approves or rejects a pending, unexpired approval for
// deciderID. Rejecting also cancels device tasks waiting on it. Anything
// else returns ErrApprovalNotPending.
func (s *Store) DecideApproval(ctx context.Context, id, deciderID int64, approve bool, reason string) (*Approval, error) {
	state := wire.ApprovalStateRejected
	if approve {
		state = wire.ApprovalStateApproved
	}
	t := now()
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = ?, decided_by = ?, decided_at = ?, decision_note = ?, updated_at = ?
			WHERE id = ? AND status = 'pending' AND (expires_at IS NULL OR expires_at > ?)`, state, deciderID, t, reason, t, id, t)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrApprovalNotPending
		}
		if !approve {
			return cancelWaitingTasks(ctx, tx, []int64{id}, "approval rejected", t)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.Approval(ctx, id)
}

// ExpireApproval marks one pending approval expired (for example when its
// plan digest no longer matches, rev 1.0 §8 rule 4) and cancels device
// tasks waiting on it.
func (s *Store) ExpireApproval(ctx context.Context, id int64) error {
	t := now()
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'expired', updated_at = ? WHERE id = ? AND status = 'pending'`, t, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrApprovalNotPending
		}
		return cancelWaitingTasks(ctx, tx, []int64{id}, "approval expired", t)
	})
}

// ExpireApprovals expires every pending approval past its expiry, cancels
// the device tasks waiting on them and returns their ids.
func (s *Store) ExpireApprovals(ctx context.Context) ([]int64, error) {
	t := now()
	var ids []int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals WHERE status = 'pending' AND expires_at IS NOT NULL AND expires_at <= ?`, t)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil || len(ids) == 0 {
			return err
		}
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, `UPDATE approvals SET status = 'expired', updated_at = ? WHERE id = ? AND status = 'pending'`, t, id); err != nil {
				return err
			}
		}
		return cancelWaitingTasks(ctx, tx, ids, "approval expired", t)
	})
	return ids, err
}

func cancelWaitingTasks(ctx context.Context, tx *sql.Tx, approvalIDs []int64, reason string, t int64) error {
	for _, id := range approvalIDs {
		if _, err := tx.ExecContext(ctx, `UPDATE device_tasks SET status = 'cancelled', error = ?, finished_at = ?
			WHERE approval_id = ? AND status = ?`, reason, t, id, TaskStatusAwaitingApproval); err != nil {
			return err
		}
	}
	return nil
}

// ConsumeApproval marks an approved approval consumed (executed). It
// returns ErrApprovalNotPending unless the approval is approved.
func (s *Store) ConsumeApproval(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE approvals SET status = 'executed', updated_at = ? WHERE id = ? AND status = 'approved'`, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrApprovalNotPending
	}
	return nil
}

// SetApprovalOperation records the operation that resumes an approval.
func (s *Store) SetApprovalOperation(ctx context.Context, id, operationID int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE approvals SET operation_id = ?, updated_at = ? WHERE id = ?`, operationID, now(), id)
	return err
}
