// Owner: SRV-K (P1-12). Append-only server-side decryption audit
// (MASTER_PLAN §7.2, table key_audit). Store-only in v1 (api-v1 §14
// Rev 1.1b): there is no list route.

package store

import (
	"context"
	"errors"
)

// KeyAudit is one key_audit row. Optional references are nil when unknown.
type KeyAudit struct {
	ID            int64
	Action        string
	Purpose       string
	ActorUserID   *int64
	ActorDeviceID *int64
	UserID        *int64
	RepoID        *int64
	Epoch         *int64
	KeyKind       string
	KeyVersion    *int64
	OperationID   *int64
	Details       string
	CreatedAt     int64
}

// AppendKeyAudit inserts a and sets its ID and CreatedAt. Rows are never
// updated or deleted.
func (s *Store) AppendKeyAudit(ctx context.Context, a *KeyAudit) error {
	if a.Action == "" {
		return errors.New("key audit action required")
	}
	a.CreatedAt = now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO key_audit(action, purpose, actor_user_id, actor_device_id, user_id, repo_id, epoch,
		key_kind, key_version, operation_id, details, created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		a.Action, a.Purpose, a.ActorUserID, a.ActorDeviceID, a.UserID, a.RepoID, a.Epoch,
		a.KeyKind, a.KeyVersion, a.OperationID, a.Details, a.CreatedAt)
	if err != nil {
		return err
	}
	a.ID, err = res.LastInsertId()
	return err
}

// ListKeyAudit returns audit rows with id > afterID, oldest first, at most
// limit (1..500, default 100). repoID > 0 restricts to one repository.
func (s *Store) ListKeyAudit(ctx context.Context, repoID, afterID int64, limit int) ([]*KeyAudit, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `SELECT id, action, purpose, actor_user_id, actor_device_id, user_id, repo_id, epoch, key_kind, key_version,
		operation_id, details, created_at FROM key_audit WHERE id > ?`
	args := []any{afterID}
	if repoID > 0 {
		q += ` AND repo_id = ?`
		args = append(args, repoID)
	}
	q += ` ORDER BY id LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*KeyAudit
	for rows.Next() {
		var a KeyAudit
		if err := rows.Scan(&a.ID, &a.Action, &a.Purpose, &a.ActorUserID, &a.ActorDeviceID, &a.UserID, &a.RepoID, &a.Epoch,
			&a.KeyKind, &a.KeyVersion, &a.OperationID, &a.Details, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, &a)
	}
	return out, rows.Err()
}
