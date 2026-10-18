package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// Device task statuses stored in addition to the wire statuses (api-v1 §6).
// An awaiting_approval task is shown as pending with its approval_id set and
// is never delivered until the approval releases it.
const TaskStatusAwaitingApproval = "awaiting_approval"

// Task lease defaults: a claimed task returns to pending when its lease ends
// without a result, and fails after MaxTaskAttempts claims.
const (
	DefaultTaskLease = 10 * time.Minute
	MaxTaskAttempts  = 3
)

// Task error codes stored in device_tasks.error_code.
const (
	TaskErrorLeaseExpired = "lease_expired"
)

// ErrTaskNotClaimable is returned when a task is not in a state that allows
// the requested transition for the caller (api-v1 409 task_not_claimable).
var ErrTaskNotClaimable = errors.New("task not claimable")

// DeviceTask is one row of device_tasks (api-v1 §6). Payload and Result hold
// JSON text. CreatedBy is the creating user, nil for server-created tasks.
// ExpiresAt is the lease end of a claimed task.
type DeviceTask struct {
	ID              int64
	UserID          int64
	DeviceID        int64
	Kind            string
	Payload         string
	RequiredModules []string
	Status          string
	ApprovalID      *int64
	CreatedBy       *int64
	Attempts        int
	Result          string
	ErrorCode       string
	Error           string
	CreatedAt       int64
	ClaimedAt       *int64
	FinishedAt      *int64
	ExpiresAt       *int64
}

const taskCols = "id, user_id, device_id, kind, payload, required_modules, status, approval_id, created_by, attempts, result, error_code, error, created_at, claimed_at, finished_at, expires_at"

func scanTask(sc interface{ Scan(...any) error }) (*DeviceTask, error) {
	var t DeviceTask
	var mods string
	var appr, by, claimed, fin, exp sql.NullInt64
	err := sc.Scan(&t.ID, &t.UserID, &t.DeviceID, &t.Kind, &t.Payload, &mods, &t.Status, &appr, &by, &t.Attempts,
		&t.Result, &t.ErrorCode, &t.Error, &t.CreatedAt, &claimed, &fin, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(jsonOr(mods, "[]")), &t.RequiredModules); err != nil {
		return nil, fmt.Errorf("device task %d: required_modules: %w", t.ID, err)
	}
	if t.RequiredModules == nil {
		t.RequiredModules = []string{}
	}
	t.ApprovalID, t.CreatedBy = nullInt(appr), nullInt(by)
	t.ClaimedAt, t.FinishedAt, t.ExpiresAt = nullInt(claimed), nullInt(fin), nullInt(exp)
	return &t, nil
}

// CreateTask inserts a task. t.Status may be "" (pending) or
// TaskStatusAwaitingApproval (then t.ApprovalID should be set, possibly
// later via SetTaskApproval). It sets t.ID, t.Status and t.CreatedAt.
func (s *Store) CreateTask(ctx context.Context, t *DeviceTask) error {
	if t.Kind == "" || t.DeviceID <= 0 || t.UserID <= 0 {
		return errors.New("device task kind, device and user required")
	}
	switch t.Status {
	case "":
		t.Status = wire.TaskStatusPending
	case wire.TaskStatusPending, TaskStatusAwaitingApproval:
	default:
		return fmt.Errorf("device task: invalid initial status %q", t.Status)
	}
	if t.RequiredModules == nil {
		t.RequiredModules = []string{}
	}
	mods, err := json.Marshal(t.RequiredModules)
	if err != nil {
		return err
	}
	t.Payload, t.Result = jsonOr(t.Payload, "{}"), "{}"
	t.CreatedAt = now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO device_tasks(user_id, device_id, kind, payload, required_modules, status, approval_id, created_by, result, created_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, t.UserID, t.DeviceID, t.Kind, t.Payload, string(mods), t.Status, t.ApprovalID, t.CreatedBy, t.Result, t.CreatedAt)
	if err != nil {
		return err
	}
	t.ID, err = res.LastInsertId()
	return err
}

// Task returns a task by id (any owner); callers check ownership.
func (s *Store) Task(ctx context.Context, id int64) (*DeviceTask, error) {
	return scanTask(s.DB.QueryRowContext(ctx, "SELECT "+taskCols+" FROM device_tasks WHERE id = ?", id))
}

// SetTaskApproval binds an approval to a task awaiting approval.
func (s *Store) SetTaskApproval(ctx context.Context, taskID, approvalID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET approval_id = ? WHERE id = ? AND status = ?`, approvalID, taskID, TaskStatusAwaitingApproval)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

// ReleaseTask moves a task awaiting approval to pending, so devices can
// claim it. A task in any other state returns ErrTaskNotClaimable.
func (s *Store) ReleaseTask(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET status = 'pending' WHERE id = ? AND status = ?`, id, TaskStatusAwaitingApproval)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

// CancelTask cancels a task that has not been claimed yet.
func (s *Store) CancelTask(ctx context.Context, id int64, reason string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET status = 'cancelled', error = ?, finished_at = ?
		WHERE id = ? AND status IN ('pending','awaiting_approval')`, reason, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

// FailTask fails a task that has not finished yet, with a machine code.
func (s *Store) FailTask(ctx context.Context, id int64, code, msg string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET status = 'failed', error_code = ?, error = ?, finished_at = ?
		WHERE id = ? AND status IN ('pending','awaiting_approval','claimed')`, code, msg, now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTaskNotClaimable
	}
	return nil
}

// ClaimTask atomically claims one pending task for deviceID with a lease.
// A task that is not pending, belongs to another device or whose approval is
// not approved returns ErrTaskNotClaimable.
func (s *Store) ClaimTask(ctx context.Context, id, deviceID int64, lease time.Duration) (*DeviceTask, error) {
	if lease <= 0 {
		lease = DefaultTaskLease
	}
	t := now()
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET status = 'claimed', claimed_at = ?, expires_at = ?, attempts = attempts + 1
		WHERE id = ? AND device_id = ? AND status = 'pending'
		AND (approval_id IS NULL OR approval_id IN (SELECT id FROM approvals WHERE status IN ('approved','executed')))`,
		t, t+lease.Milliseconds(), id, deviceID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrTaskNotClaimable
	}
	return s.Task(ctx, id)
}

// ClaimTasks claims up to limit pending tasks of deviceID (oldest first)
// with a lease. Tasks needing a module that is not in enabled fail at once
// with error code module_unavailable instead (api-v1 §6); enabled nil skips
// that check. A task claimed concurrently by another poll is skipped.
func (s *Store) ClaimTasks(ctx context.Context, deviceID int64, limit int, lease time.Duration, enabled map[string]bool) ([]DeviceTask, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+taskCols+` FROM device_tasks WHERE device_id = ? AND status = 'pending'
		AND (approval_id IS NULL OR approval_id IN (SELECT id FROM approvals WHERE status IN ('approved','executed')))
		ORDER BY id LIMIT ?`, deviceID, limit)
	if err != nil {
		return nil, err
	}
	var cand []DeviceTask
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		cand = append(cand, *t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := []DeviceTask{}
	for _, c := range cand {
		if missing := missingTaskModule(c.RequiredModules, enabled); missing != "" {
			err := s.FailTask(ctx, c.ID, wire.CodeModuleUnavailable, fmt.Sprintf("module %q is not enabled on the device", missing))
			if err != nil && !errors.Is(err, ErrTaskNotClaimable) {
				return nil, err
			}
			continue
		}
		t, err := s.ClaimTask(ctx, c.ID, deviceID, lease)
		if errors.Is(err, ErrTaskNotClaimable) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

func missingTaskModule(need []string, enabled map[string]bool) string {
	if enabled == nil {
		return ""
	}
	for _, m := range need {
		if !enabled[m] {
			return m
		}
	}
	return ""
}

// CompleteTask records the result of a task claimed by deviceID. status is
// done or failed; result is JSON text ("" stores {}). A task that is not
// claimed by this device returns ErrTaskNotClaimable.
func (s *Store) CompleteTask(ctx context.Context, id, deviceID int64, status, result string) (*DeviceTask, error) {
	if status != wire.TaskStatusDone && status != wire.TaskStatusFailed {
		return nil, fmt.Errorf("device task: invalid result status %q", status)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE device_tasks SET status = ?, result = ?, finished_at = ?, expires_at = NULL
		WHERE id = ? AND device_id = ? AND status = 'claimed'`, status, jsonOr(result, "{}"), now(), id, deviceID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrTaskNotClaimable
	}
	return s.Task(ctx, id)
}

// ExpireTaskLeases returns claimed tasks whose lease ended to pending, or
// fails them (error code lease_expired) once they used MaxTaskAttempts
// claims. It returns the ids of requeued and failed tasks.
func (s *Store) ExpireTaskLeases(ctx context.Context) (requeued, failed []int64, err error) {
	t := now()
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, attempts FROM device_tasks WHERE status = 'claimed' AND expires_at IS NOT NULL AND expires_at <= ?`, t)
		if err != nil {
			return err
		}
		type exp struct {
			id       int64
			attempts int
		}
		var list []exp
		for rows.Next() {
			var e exp
			if err := rows.Scan(&e.id, &e.attempts); err != nil {
				rows.Close()
				return err
			}
			list = append(list, e)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, e := range list {
			if e.attempts >= MaxTaskAttempts {
				if _, err := tx.ExecContext(ctx, `UPDATE device_tasks SET status = 'failed', error_code = ?, error = 'lease expired without a result', finished_at = ?, expires_at = NULL
					WHERE id = ? AND status = 'claimed'`, TaskErrorLeaseExpired, t, e.id); err != nil {
					return err
				}
				failed = append(failed, e.id)
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE device_tasks SET status = 'pending', claimed_at = NULL, expires_at = NULL
				WHERE id = ? AND status = 'claimed'`, e.id); err != nil {
				return err
			}
			requeued = append(requeued, e.id)
		}
		return nil
	})
	return requeued, failed, err
}

// TaskFilter selects tasks for ListTasks. UserID is required (the owner);
// DeviceID and Status are optional. BeforeID pages by descending id.
type TaskFilter struct {
	UserID   int64
	DeviceID int64
	Status   string
	BeforeID int64
	Limit    int
}

// ListTasks returns tasks newest first. A wire status filter of "pending"
// also matches tasks awaiting approval.
func (s *Store) ListTasks(ctx context.Context, f TaskFilter) ([]DeviceTask, error) {
	where := []string{"user_id = ?"}
	args := []any{f.UserID}
	if f.DeviceID > 0 {
		where = append(where, "device_id = ?")
		args = append(args, f.DeviceID)
	}
	switch f.Status {
	case "":
	case wire.TaskStatusPending:
		where = append(where, "status IN ('pending','awaiting_approval')")
	default:
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.BeforeID > 0 {
		where = append(where, "id < ?")
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+taskCols+" FROM device_tasks WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DeviceTask{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}
