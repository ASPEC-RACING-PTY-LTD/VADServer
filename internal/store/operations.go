package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Operation is a long-running server operation (api-v1 §1, §3). Params and
// Result hold JSON text; Error is a message, "" unless the operation failed.
type Operation struct {
	ID         int64
	UserID     *int64 // nil for system operations (scheduler, admin GC)
	DeviceID   *int64 // the device that started it, if any
	Kind       string
	State      string
	Done       int64
	Total      int64
	Bytes      int64
	Params     string
	Result     string
	Error      string
	CreatedAt  int64
	UpdatedAt  int64
	FinishedAt *int64
}

// Finished reports whether the operation reached a final state.
func (o *Operation) Finished() bool { return operationFinal(o.State) }

func operationFinal(state string) bool {
	switch state {
	case wire.OperationStateSucceeded, wire.OperationStateFailed, wire.OperationStateCancelled:
		return true
	}
	return false
}

// ErrOperationFinished is returned when an operation that already reached a
// final state is updated.
var ErrOperationFinished = errors.New("operation already finished")

const opCols = "id, user_id, device_id, kind, state, progress_done, progress_total, progress_bytes, params, result, error, created_at, updated_at, finished_at"

func scanOperation(sc interface{ Scan(...any) error }) (*Operation, error) {
	var o Operation
	var uid, did, fin sql.NullInt64
	err := sc.Scan(&o.ID, &uid, &did, &o.Kind, &o.State, &o.Done, &o.Total, &o.Bytes, &o.Params, &o.Result, &o.Error, &o.CreatedAt, &o.UpdatedAt, &fin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o.UserID, o.DeviceID, o.FinishedAt = nullInt(uid), nullInt(did), nullInt(fin)
	return &o, nil
}

func jsonOr(s, def string) string {
	if strings.TrimSpace(s) == "" {
		return def
	}
	return s
}

// CreateOperation inserts a queued operation and sets o.ID, o.State and the
// timestamps.
func (s *Store) CreateOperation(ctx context.Context, o *Operation) error {
	if o.Kind == "" {
		return errors.New("operation kind required")
	}
	t := now()
	o.State, o.CreatedAt, o.UpdatedAt = wire.OperationStateQueued, t, t
	o.Params, o.Result = jsonOr(o.Params, "{}"), "{}"
	res, err := s.DB.ExecContext(ctx, `INSERT INTO operations(user_id, device_id, kind, state, params, result, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?)`, o.UserID, o.DeviceID, o.Kind, o.State, o.Params, o.Result, t, t)
	if err != nil {
		return err
	}
	o.ID, err = res.LastInsertId()
	return err
}

// Operation returns one operation by id.
func (s *Store) Operation(ctx context.Context, id int64) (*Operation, error) {
	return scanOperation(s.DB.QueryRowContext(ctx, "SELECT "+opCols+" FROM operations WHERE id = ?", id))
}

// StartOperation moves a queued operation to running.
func (s *Store) StartOperation(ctx context.Context, id int64) error {
	return s.updateOperation(ctx, id, "state = ?", wire.OperationStateRunning)
}

// UpdateOperationProgress records progress of an unfinished operation.
func (s *Store) UpdateOperationProgress(ctx context.Context, id, done, total, bytes int64) error {
	return s.updateOperation(ctx, id, "progress_done = ?, progress_total = ?, progress_bytes = ?", done, total, bytes)
}

// FinishOperation moves an unfinished operation to a final state. result is
// JSON text ("" stores {}); errMsg is stored as the operation error.
func (s *Store) FinishOperation(ctx context.Context, id int64, state, result, errMsg string) error {
	if !operationFinal(state) {
		return errors.New("not a final operation state: " + state)
	}
	return s.updateOperation(ctx, id, "state = ?, result = ?, error = ?, finished_at = ?",
		state, jsonOr(result, "{}"), truncate(errMsg, 2000), now())
}

func (s *Store) updateOperation(ctx context.Context, id int64, set string, args ...any) error {
	args = append(args, now(), id)
	res, err := s.DB.ExecContext(ctx, "UPDATE operations SET "+set+", updated_at = ? WHERE id = ? AND state IN ('queued','running')", args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Operation(ctx, id); err != nil {
			return err
		}
		return ErrOperationFinished
	}
	return nil
}

// OperationFilter selects operations for ListOperations. A nil UserID lists
// every user's operations (admin view); SystemOnly lists those without a
// user. BeforeID pages backwards by id (0 = newest).
type OperationFilter struct {
	UserID     *int64
	SystemOnly bool
	Kind       string
	State      string
	BeforeID   int64
	Limit      int
}

// ListOperations returns operations newest first.
func (s *Store) ListOperations(ctx context.Context, f OperationFilter) ([]Operation, error) {
	q := "SELECT " + opCols + " FROM operations WHERE 1=1"
	var args []any
	switch {
	case f.UserID != nil:
		q += " AND user_id = ?"
		args = append(args, *f.UserID)
	case f.SystemOnly:
		q += " AND user_id IS NULL"
	}
	if f.Kind != "" {
		q += " AND kind = ?"
		args = append(args, f.Kind)
	}
	if f.State != "" {
		q += " AND state = ?"
		args = append(args, f.State)
	}
	if f.BeforeID > 0 {
		q += " AND id < ?"
		args = append(args, f.BeforeID)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	q += " ORDER BY id DESC LIMIT ?"
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		o, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// FailInterruptedOperations marks operations left queued or running by a
// previous process as failed. Call it once at startup, before serving.
func (s *Store) FailInterruptedOperations(ctx context.Context) (int64, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `UPDATE operations SET state = ?, error = 'interrupted by a server restart', updated_at = ?, finished_at = ?
		WHERE state IN ('queued','running')`, wire.OperationStateFailed, t, t)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
