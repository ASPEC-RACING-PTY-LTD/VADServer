package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Restores (api-v1 §7, rev 1.1c). A restore is created by /restore/execute
// as awaiting_approval, becomes queued when its approval queues the device
// task, running on the device's first progress report, and ends completed,
// partial, failed or cancelled.

// ErrRestoreFinal is returned for a change to a restore in a terminal state
// (api-v1 409 conflict).
var ErrRestoreFinal = errors.New("restore already finished")

// RestoreOptions is the stored request part of a restore (restores.options).
type RestoreOptions struct {
	Selection []wire.RestoreSelection   `json:"selection"`
	Overrides []wire.RestorePathMapping `json:"overrides"`
	Mappings  []wire.RestorePlanMapping `json:"mappings"`
}

// Restore is one row of restores.
type Restore struct {
	ID               int64
	UserID           int64
	DeviceID         *int64
	SnapshotID       *int64
	TargetProfileSID string
	ConflictPolicy   string
	Options          RestoreOptions
	Plan             string
	Guard            string
	Status           string
	TaskID           *int64
	ApprovalID       *int64
	CancelRequested  bool
	FilesTotal       int64
	FilesDone        int64
	BytesTotal       int64
	BytesDone        int64
	Error            string
	CreatedBy        *int64
	CreatedAt        int64
	UpdatedAt        int64
	FinishedAt       *int64
}

// Wire returns the API view.
func (r *Restore) Wire() wire.Restore {
	out := wire.Restore{ID: r.ID, TargetProfileSID: r.TargetProfileSID, ApprovalID: r.ApprovalID, TaskID: r.TaskID,
		Status: r.Status, FilesTotal: r.FilesTotal, FilesDone: r.FilesDone, BytesTotal: r.BytesTotal, BytesDone: r.BytesDone,
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt, ConflictPolicy: r.ConflictPolicy, CancelRequested: r.CancelRequested,
		Error: r.Error, FinishedAt: r.FinishedAt}
	if r.SnapshotID != nil {
		out.SnapshotID = *r.SnapshotID
	}
	if r.DeviceID != nil {
		out.TargetDeviceID = *r.DeviceID
	}
	return out
}

// RestoreTerminal reports whether status is final.
func RestoreTerminal(status string) bool {
	switch status {
	case wire.RestoreStatusCompleted, wire.RestoreStatusPartial, wire.RestoreStatusFailed, wire.RestoreStatusCancelled:
		return true
	}
	return false
}

const restoreCols = `id, user_id, device_id, snapshot_id, target_profile_sid, conflict_policy, options, plan, guard, status,
	task_id, approval_id, cancel_requested, files_total, files_done, bytes_total, bytes_done, error, created_by,
	created_at, updated_at, finished_at`

func scanRestore(sc interface{ Scan(...any) error }) (*Restore, error) {
	var r Restore
	var dev, snap, task, appr, by, fin sql.NullInt64
	var opts string
	var cancel int
	err := sc.Scan(&r.ID, &r.UserID, &dev, &snap, &r.TargetProfileSID, &r.ConflictPolicy, &opts, &r.Plan, &r.Guard, &r.Status,
		&task, &appr, &cancel, &r.FilesTotal, &r.FilesDone, &r.BytesTotal, &r.BytesDone, &r.Error, &by,
		&r.CreatedAt, &r.UpdatedAt, &fin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(jsonOr(opts, "{}")), &r.Options); err != nil {
		return nil, fmt.Errorf("restore %d: options: %w", r.ID, err)
	}
	r.DeviceID, r.SnapshotID, r.TaskID, r.ApprovalID, r.CreatedBy, r.FinishedAt = nullInt(dev), nullInt(snap), nullInt(task), nullInt(appr), nullInt(by), nullInt(fin)
	r.CancelRequested = cancel != 0
	return &r, nil
}

// CreateRestore inserts r (status defaults to awaiting_approval) and sets
// r.ID and the times.
func (s *Store) CreateRestore(ctx context.Context, r *Restore) error {
	if r.UserID <= 0 || r.DeviceID == nil || r.SnapshotID == nil {
		return errors.New("restore user, device and snapshot required")
	}
	if r.Status == "" {
		r.Status = wire.RestoreStatusAwaitingApproval
	}
	opts, err := json.Marshal(r.Options)
	if err != nil {
		return err
	}
	if r.Plan == "" {
		r.Plan = "{}"
	}
	if r.Guard == "" {
		r.Guard = "[]"
	}
	r.CreatedAt = now()
	r.UpdatedAt = r.CreatedAt
	res, err := s.DB.ExecContext(ctx, `INSERT INTO restores(user_id, device_id, snapshot_id, target_profile_sid, conflict_policy,
		options, plan, guard, status, files_total, bytes_total, created_by, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		r.UserID, r.DeviceID, r.SnapshotID, r.TargetProfileSID, r.ConflictPolicy, string(opts), r.Plan, r.Guard, r.Status,
		r.FilesTotal, r.BytesTotal, r.CreatedBy, r.CreatedAt, r.UpdatedAt)
	if err != nil {
		return err
	}
	r.ID, err = res.LastInsertId()
	return err
}

// RestoreByID returns one restore.
func (s *Store) RestoreByID(ctx context.Context, id int64) (*Restore, error) {
	return scanRestore(s.DB.QueryRowContext(ctx, "SELECT "+restoreCols+" FROM restores WHERE id = ?", id))
}

// ListRestores lists a user's restores, newest first, below beforeID (0 =
// from the newest).
func (s *Store) ListRestores(ctx context.Context, userID, beforeID int64, limit int) ([]Restore, error) {
	q := "SELECT " + restoreCols + " FROM restores WHERE user_id = ?"
	args := []any{userID}
	if beforeID > 0 {
		q += " AND id < ?"
		args = append(args, beforeID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Restore
	for rows.Next() {
		r, err := scanRestore(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// SetRestoreApproval records the restore's approval.
func (s *Store) SetRestoreApproval(ctx context.Context, id, approvalID int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE restores SET approval_id = ?, updated_at = ? WHERE id = ?", approvalID, now(), id)
	return err
}

// QueueRestore moves an awaiting_approval restore to queued with its task.
func (s *Store) QueueRestore(ctx context.Context, id, taskID int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE restores SET status = ?, task_id = ?, updated_at = ? WHERE id = ? AND status = ?",
		wire.RestoreStatusQueued, taskID, now(), id, wire.RestoreStatusAwaitingApproval)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrRestoreFinal
	}
	return nil
}

// CancelRestore cancels an awaiting_approval or queued restore, or marks a
// running one cancel_requested. It returns the restore as it was before
// (so the caller can cancel its approval or task) and after.
func (s *Store) CancelRestore(ctx context.Context, id int64, reason string) (before, after *Restore, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanRestore(tx.QueryRowContext(ctx, "SELECT "+restoreCols+" FROM restores WHERE id = ?", id))
		if err != nil {
			return err
		}
		before = r
		t := now()
		switch r.Status {
		case wire.RestoreStatusAwaitingApproval, wire.RestoreStatusQueued:
			_, err = tx.ExecContext(ctx, "UPDATE restores SET status = ?, error = ?, updated_at = ?, finished_at = ? WHERE id = ?",
				wire.RestoreStatusCancelled, reason, t, t, id)
		case wire.RestoreStatusRunning:
			_, err = tx.ExecContext(ctx, "UPDATE restores SET cancel_requested = 1, updated_at = ? WHERE id = ?", t, id)
		default:
			return ErrRestoreFinal
		}
		if err != nil {
			return err
		}
		after, err = scanRestore(tx.QueryRowContext(ctx, "SELECT "+restoreCols+" FROM restores WHERE id = ?", id))
		return err
	})
	return before, after, err
}

// ReportRestoreProgress applies a device progress report: status and
// counters, and appends items. Only queued or running restores accept
// reports (ErrRestoreFinal otherwise).
func (s *Store) ReportRestoreProgress(ctx context.Context, id int64, p wire.RestoreProgress) (*Restore, error) {
	var out *Restore
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		r, err := scanRestore(tx.QueryRowContext(ctx, "SELECT "+restoreCols+" FROM restores WHERE id = ?", id))
		if err != nil {
			return err
		}
		if RestoreTerminal(r.Status) || r.Status == wire.RestoreStatusAwaitingApproval {
			return ErrRestoreFinal
		}
		t := now()
		var fin any
		if RestoreTerminal(p.Status) {
			fin = t
		}
		if _, err := tx.ExecContext(ctx, `UPDATE restores SET status = ?, files_total = ?, files_done = ?, bytes_total = ?,
			bytes_done = ?, error = ?, updated_at = ?, finished_at = COALESCE(?, finished_at) WHERE id = ?`,
			p.Status, p.FilesTotal, p.FilesDone, p.BytesTotal, p.BytesDone, clip(p.Error, 2000), t, fin, id); err != nil {
			return err
		}
		for _, it := range p.Items {
			if _, err := tx.ExecContext(ctx, `INSERT INTO restore_items(restore_id, path, target_path, kind, action, status, error, size, bytes, updated_at)
				VALUES(?,?,?,?,?,?,?,?,?,?)`, id, clip(it.Path, 4096), clip(it.TargetPath, 4096), "file",
				it.Action, it.Status, clip(it.Message, 2000), it.Size, it.Size, t); err != nil {
				return err
			}
		}
		out, err = scanRestore(tx.QueryRowContext(ctx, "SELECT "+restoreCols+" FROM restores WHERE id = ?", id))
		return err
	})
	return out, err
}

// RestoreItemRow is a restore item with its row id (the list cursor).
type RestoreItemRow struct {
	ID int64
	wire.RestoreItem
}

// RestoreItems lists items of a restore in report order after afterID,
// optionally filtered by status.
func (s *Store) RestoreItems(ctx context.Context, restoreID int64, status string, afterID int64, limit int) ([]RestoreItemRow, error) {
	q := "SELECT id, path, target_path, action, status, error, size FROM restore_items WHERE restore_id = ? AND id > ?"
	args := []any{restoreID, afterID}
	if status != "" {
		q += " AND status = ?"
		args = append(args, status)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY id LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RestoreItemRow
	for rows.Next() {
		var it RestoreItemRow
		if err := rows.Scan(&it.ID, &it.Path, &it.TargetPath, &it.Action, &it.Status, &it.Message, &it.Size); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// clip truncates s to at most n bytes of valid UTF-8.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
