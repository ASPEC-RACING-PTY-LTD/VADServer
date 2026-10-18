package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type User struct {
	ID           int64  `json:"id"`
	Username     string `json:"username"`
	Email        string `json:"email"`
	DisplayName  string `json:"display_name"`
	Role         string `json:"role"`
	Disabled     bool   `json:"disabled"`
	QuotaBytes   int64  `json:"quota_bytes"`
	UsedBytes    int64  `json:"used_bytes"`
	LocationID   *int64 `json:"location_id"`
	RootID       int64  `json:"root_id"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
	LastLoginAt  *int64 `json:"last_login_at"`
	PasswordHash string `json:"-"`
}

// ErrInvalidQuota is returned when a negative quota is stored.
var ErrInvalidQuota = errors.New("quota must not be negative")

func (u *User) IsAdmin() bool { return u.Role == "admin" }

const userCols = `id, username, COALESCE(email,''), display_name, role, disabled, quota_bytes, used_bytes, location_id, COALESCE(root_id,0), created_at, updated_at, last_login_at, password_hash`

func scanUser(sc interface{ Scan(...any) error }) (*User, error) {
	var u User
	var loc, last sql.NullInt64
	err := sc.Scan(&u.ID, &u.Username, &u.Email, &u.DisplayName, &u.Role, &u.Disabled, &u.QuotaBytes, &u.UsedBytes, &loc, &u.RootID, &u.CreatedAt, &u.UpdatedAt, &last, &u.PasswordHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.LocationID = nullInt(loc)
	u.LastLoginAt = nullInt(last)
	return &u, nil
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	return scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE id = ?", id))
}

// UserByLogin finds a user by username or email (case-insensitive).
func (s *Store) UserByLogin(ctx context.Context, login string) (*User, error) {
	login = strings.TrimSpace(login)
	return scanUser(s.DB.QueryRowContext(ctx, "SELECT "+userCols+" FROM users WHERE username = ? OR (email IS NOT NULL AND email = ?) LIMIT 1", login, login))
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+userCols+" FROM users ORDER BY username")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

type NewUser struct {
	Username, Email, DisplayName, PasswordHash, Role string
	QuotaBytes                                       int64
	LocationID                                       int64
}

// CreateUser inserts a user and its root folder. It fails with ErrConflict
// when the username or email is taken.
func (s *Store) CreateUser(ctx context.Context, nu NewUser) (*User, error) {
	if nu.QuotaBytes < 0 {
		return nil, ErrInvalidQuota
	}
	var loc any // 0 means "no location": store NULL, never a dangling id
	if nu.LocationID > 0 {
		loc = nu.LocationID
	}
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		var email any
		if e := strings.TrimSpace(nu.Email); e != "" {
			email = e
		}
		res, err := tx.ExecContext(ctx, `INSERT INTO users(username, email, display_name, password_hash, role, quota_bytes, location_id, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?)`, strings.TrimSpace(nu.Username), email, nu.DisplayName, nu.PasswordHash, nu.Role, nu.QuotaBytes, loc, t, t)
		if isUniqueErr(err) {
			return ErrConflict
		}
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		res, err = tx.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, created_at, modified_at, updated_at) VALUES(?, NULL, '', 1, ?, ?, ?)`, id, t, t, t)
		if err != nil {
			return err
		}
		root, _ := res.LastInsertId()
		_, err = tx.ExecContext(ctx, "UPDATE users SET root_id = ? WHERE id = ?", root, id)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.UserByID(ctx, id)
}

type UserPatch struct {
	Email       *string
	DisplayName *string
	Role        *string
	Disabled    *bool
	QuotaBytes  *int64
	LocationID  *int64
}

func (s *Store) UpdateUser(ctx context.Context, id int64, p UserPatch) error {
	sets := []string{"updated_at = ?"}
	args := []any{now()}
	if p.Email != nil {
		e := strings.TrimSpace(*p.Email)
		if e == "" {
			sets = append(sets, "email = NULL")
		} else {
			sets = append(sets, "email = ?")
			args = append(args, e)
		}
	}
	if p.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *p.DisplayName)
	}
	if p.Role != nil {
		sets = append(sets, "role = ?")
		args = append(args, *p.Role)
	}
	if p.Disabled != nil {
		sets = append(sets, "disabled = ?")
		args = append(args, *p.Disabled)
	}
	if p.QuotaBytes != nil {
		if *p.QuotaBytes < 0 {
			return ErrInvalidQuota
		}
		sets = append(sets, "quota_bytes = ?")
		args = append(args, *p.QuotaBytes)
	}
	if p.LocationID != nil {
		sets = append(sets, "location_id = ?")
		args = append(args, *p.LocationID)
	}
	args = append(args, id)
	res, err := s.DB.ExecContext(ctx, "UPDATE users SET "+strings.Join(sets, ", ")+" WHERE id = ?", args...)
	if isUniqueErr(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetPassword(ctx context.Context, id int64, hash string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET password_hash = ?, updated_at = ? WHERE id = ?", hash, now(), id)
	return err
}

func (s *Store) TouchLogin(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE users SET last_login_at = ? WHERE id = ?", now(), id)
}

func (s *Store) CountAdmins(ctx context.Context) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = 'admin' AND disabled = 0").Scan(&n)
	return n, err
}

// DeleteUserRecords removes the user and all metadata. Blob files must be
// removed by the caller using the returned list.
func (s *Store) DeleteUserRecords(ctx context.Context, id int64) ([]BlobRef, error) {
	var blobs []BlobRef
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, "SELECT user_id, sha256, location_id FROM blobs WHERE user_id = ?", id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var b BlobRef
			if err := rows.Scan(&b.UserID, &b.SHA256, &b.LocationID); err != nil {
				rows.Close()
				return err
			}
			blobs = append(blobs, b)
		}
		rows.Close()
		for _, q := range []string{
			"DELETE FROM blobs WHERE user_id = ?",
			"DELETE FROM node_tags WHERE node_id IN (SELECT id FROM nodes WHERE user_id = ?)",
			"UPDATE nodes SET parent_id = NULL WHERE user_id = ?",
			"DELETE FROM nodes WHERE user_id = ?",
			"DELETE FROM uploads WHERE user_id = ?",
			"DELETE FROM users WHERE id = ?",
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		return nil
	})
	return blobs, err
}

// usageQuery sums what CommitFile adds to and purgeTree subtracts from
// used_bytes: the size of every file node with content, including trash.
const usageQuery = "SELECT COALESCE(SUM(size),0) FROM nodes WHERE user_id = ? AND is_dir = 0 AND sha256 IS NOT NULL"

// RecalculateUsage recomputes used_bytes from file nodes (including trash).
// It returns ErrNotFound for an unknown user.
func (s *Store) RecalculateUsage(ctx context.Context, userID int64) (int64, error) {
	var used int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if err := tx.QueryRowContext(ctx, usageQuery, userID).Scan(&used); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, "UPDATE users SET used_bytes = ? WHERE id = ?", used, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	})
	return used, err
}

// UsageDrift reports a user's stored used_bytes and the value recomputed
// from file nodes, without changing anything (for health checks and tests).
func (s *Store) UsageDrift(ctx context.Context, userID int64) (stored, actual int64, err error) {
	if err = s.DB.QueryRowContext(ctx, "SELECT used_bytes FROM users WHERE id = ?", userID).Scan(&stored); errors.Is(err, sql.ErrNoRows) {
		return 0, 0, ErrNotFound
	} else if err != nil {
		return 0, 0, err
	}
	err = s.DB.QueryRowContext(ctx, usageQuery, userID).Scan(&actual)
	return stored, actual, err
}

// --- sessions ---

type Session struct {
	ID         string `json:"-"`
	PublicID   string `json:"id"`
	UserID     int64  `json:"user_id"`
	CSRF       string `json:"-"`
	IP         string `json:"ip"`
	UserAgent  string `json:"user_agent"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	ExpiresAt  int64  `json:"expires_at"`
	Current    bool   `json:"current"`
}

func (s *Store) CreateSession(ctx context.Context, sess *Session) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO sessions(id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at) VALUES(?,?,?,?,?,?,?,?)`,
		sess.ID, sess.UserID, sess.CSRF, sess.IP, sess.UserAgent, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt)
	return err
}

func (s *Store) SessionByID(ctx context.Context, id string) (*Session, error) {
	var x Session
	err := s.DB.QueryRowContext(ctx, `SELECT id, user_id, csrf, ip, user_agent, created_at, last_seen_at, expires_at FROM sessions WHERE id = ?`, id).
		Scan(&x.ID, &x.UserID, &x.CSRF, &x.IP, &x.UserAgent, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &x, err
}

func (s *Store) TouchSession(ctx context.Context, id string, ip string, expires int64) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE sessions SET last_seen_at = ?, ip = ?, expires_at = ? WHERE id = ?", now(), ip, expires, id)
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	return err
}

func (s *Store) DeleteUserSessions(ctx context.Context, userID int64, except string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id != ?", userID, except)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) ListSessions(ctx context.Context, userID int64) ([]Session, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, user_id, ip, user_agent, created_at, last_seen_at, expires_at FROM sessions WHERE user_id = ? AND expires_at > ? ORDER BY last_seen_at DESC`, userID, now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var x Session
		if err := rows.Scan(&x.ID, &x.UserID, &x.IP, &x.UserAgent, &x.CreatedAt, &x.LastSeenAt, &x.ExpiresAt); err != nil {
			return nil, err
		}
		x.PublicID = x.ID[:16]
		out = append(out, x)
	}
	return out, rows.Err()
}

// DeleteSessionByPublicID removes a session of the user by its public prefix.
func (s *Store) DeleteSessionByPublicID(ctx context.Context, userID int64, pub string) (bool, error) {
	if len(pub) != 16 {
		return false, nil
	}
	res, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND substr(id, 1, 16) = ?", userID, pub)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) PurgeExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at < ?", now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
