// Owner: SRV-M (P1-20). users.is_superuser management (api-v1 §11B). The
// first administrator becomes superuser through the users_first_superuser
// trigger (migration 2); this file keeps at least one superuser.

package store

import (
	"context"
	"database/sql"
	"errors"
)

var (
	// ErrLastSuperuser is returned when a change would leave no active
	// superuser.
	ErrLastSuperuser = errors.New("at least one superuser must remain")
	// ErrNotAdmin is returned when superuser is granted to a user who is not
	// an active administrator.
	ErrNotAdmin = errors.New("user is not an active administrator")
)

// activeSuperusers counts enabled administrators with is_superuser.
const activeSuperusers = `SELECT COUNT(*) FROM users WHERE is_superuser = 1 AND role = 'admin' AND disabled = 0`

// IsSuperuser reports users.is_superuser for id.
func (s *Store) IsSuperuser(ctx context.Context, id int64) (bool, error) {
	var v int
	err := s.DB.QueryRowContext(ctx, `SELECT is_superuser FROM users WHERE id = ?`, id).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	return v == 1, err
}

// SuperuserIDs lists the active superusers (for notifications).
func (s *Store) SuperuserIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM users WHERE is_superuser = 1 AND role = 'admin' AND disabled = 0 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// IsLastSuperuser reports whether id is the only active superuser, so that
// deleting, disabling or demoting it must be refused (ErrLastSuperuser).
func (s *Store) IsLastSuperuser(ctx context.Context, id int64) (bool, error) {
	var mine, n int
	err := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users WHERE id = ? AND is_superuser = 1 AND role = 'admin' AND disabled = 0),
		(`+activeSuperusers+`)`, id).Scan(&mine, &n)
	return mine == 1 && n <= 1, err
}

// SetSuperuser grants or revokes is_superuser. Granting requires an active
// administrator (ErrNotAdmin); revoking the last active superuser fails with
// ErrLastSuperuser. changed reports whether the flag actually changed.
func (s *Store) SetSuperuser(ctx context.Context, id int64, on bool) (changed bool, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var role string
		var disabled bool
		var cur int
		err := tx.QueryRowContext(ctx, `SELECT role, disabled, is_superuser FROM users WHERE id = ?`, id).Scan(&role, &disabled, &cur)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		if (cur == 1) == on {
			return nil
		}
		if on && (role != "admin" || disabled) {
			return ErrNotAdmin
		}
		if !on {
			var others int
			if err := tx.QueryRowContext(ctx, activeSuperusers+` AND id <> ?`, id).Scan(&others); err != nil {
				return err
			}
			if others == 0 {
				return ErrLastSuperuser
			}
		}
		v := 0
		if on {
			v = 1
		}
		if _, err := tx.ExecContext(ctx, `UPDATE users SET is_superuser = ?, updated_at = ? WHERE id = ?`, v, now(), id); err != nil {
			return err
		}
		changed = true
		return nil
	})
	return changed, err
}
