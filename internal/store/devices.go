package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

type Device struct {
	ID              int64           `json:"id"`
	UserID          int64           `json:"user_id"`
	Username        string          `json:"username,omitempty"`
	Name            string          `json:"name"`
	OS              string          `json:"os"`
	ClientVersion   string          `json:"client_version"`
	Identifier      string          `json:"identifier"`
	PublicKey       string          `json:"-"`
	KeyFingerprint  string          `json:"key_fingerprint"`
	Status          string          `json:"status"`
	UserCode        string          `json:"user_code"`
	PollTokenHash   string          `json:"-"`
	RequestIP       string          `json:"request_ip"`
	RequestLocation string          `json:"request_location"`
	CreatedAt       int64           `json:"created_at"`
	ExpiresAt       int64           `json:"expires_at"`
	DecidedAt       *int64          `json:"decided_at"`
	DecidedBy       *int64          `json:"decided_by"`
	LastSeenAt      *int64          `json:"last_seen_at"`
	LastIP          string          `json:"last_ip"`
	SyncState       json.RawMessage `json:"sync_state"`
	SyncStateAt     *int64          `json:"sync_state_at"`
}

const deviceCols = `d.id, d.user_id, COALESCE(u.username,''), d.name, d.os, d.client_version, d.identifier, d.public_key, d.key_fingerprint, d.status, d.user_code, COALESCE(d.poll_token_hash,''), d.request_ip, d.request_location, d.created_at, d.expires_at, d.decided_at, d.decided_by, d.last_seen_at, d.last_ip, d.sync_state, d.sync_state_at`
const deviceFrom = ` FROM devices d LEFT JOIN users u ON u.id = d.user_id`

func scanDevice(sc interface{ Scan(...any) error }) (*Device, error) {
	var d Device
	var dec, decBy, seen, stAt sql.NullInt64
	var state string
	err := sc.Scan(&d.ID, &d.UserID, &d.Username, &d.Name, &d.OS, &d.ClientVersion, &d.Identifier, &d.PublicKey, &d.KeyFingerprint, &d.Status, &d.UserCode, &d.PollTokenHash, &d.RequestIP, &d.RequestLocation, &d.CreatedAt, &d.ExpiresAt, &dec, &decBy, &seen, &d.LastIP, &state, &stAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.DecidedAt, d.DecidedBy, d.LastSeenAt, d.SyncStateAt = nullInt(dec), nullInt(decBy), nullInt(seen), nullInt(stAt)
	if state == "" {
		state = "{}"
	}
	d.SyncState = json.RawMessage(state)
	return &d, nil
}

func (s *Store) CreateDevice(ctx context.Context, d *Device) error {
	res, err := s.DB.ExecContext(ctx, `INSERT INTO devices(user_id, name, os, client_version, identifier, public_key, key_fingerprint, status, user_code, poll_token_hash, request_ip, request_location, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,'pending',?,?,?,?,?,?)`, d.UserID, d.Name, d.OS, d.ClientVersion, d.Identifier, d.PublicKey, d.KeyFingerprint, d.UserCode, d.PollTokenHash, d.RequestIP, d.RequestLocation, d.CreatedAt, d.ExpiresAt)
	if err != nil {
		return err
	}
	d.ID, _ = res.LastInsertId()
	d.Status = "pending"
	return nil
}

// Device returns a device by id (any owner); callers must check ownership.
func (s *Store) Device(ctx context.Context, id int64) (*Device, error) {
	return scanDevice(s.DB.QueryRowContext(ctx, "SELECT "+deviceCols+deviceFrom+" WHERE d.id = ?", id))
}

// ListDevices lists devices for a user (userID > 0) or all devices.
func (s *Store) ListDevices(ctx context.Context, userID int64) ([]Device, error) {
	q := "SELECT " + deviceCols + deviceFrom
	var args []any
	if userID > 0 {
		q += " WHERE d.user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY CASE d.status WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END, d.created_at DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// DecideDevice moves a pending, unexpired device to approved or rejected.
func (s *Store) DecideDevice(ctx context.Context, id, userID int64, status string, by int64) (bool, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, "UPDATE devices SET status = ?, decided_at = ?, decided_by = ? WHERE id = ? AND user_id = ? AND status = 'pending' AND expires_at > ?", status, t, by, id, userID, t)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RevokeDevice revokes an approved or pending device.
func (s *Store) RevokeDevice(ctx context.Context, id int64, by int64) (bool, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE devices SET status = 'revoked', decided_at = ?, decided_by = ?, poll_token_hash = NULL WHERE id = ? AND status IN ('approved','pending')", now(), by, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) RenameDevice(ctx context.Context, id int64, name string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE devices SET name = ? WHERE id = ?", name, id)
	return err
}

func (s *Store) DeleteDevice(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM devices WHERE id = ? AND status != 'approved'", id)
	return err
}

// ExpirePendingDevices marks timed-out requests as expired.
func (s *Store) ExpirePendingDevices(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE devices SET status = 'expired', poll_token_hash = NULL WHERE status = 'pending' AND expires_at <= ?", now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ClearPollToken removes the poll token once the client has observed the
// final status.
func (s *Store) ClearPollToken(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE devices SET poll_token_hash = NULL WHERE id = ?", id)
}

func (s *Store) TouchDevice(ctx context.Context, id int64, ip string) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE devices SET last_seen_at = ?, last_ip = ? WHERE id = ?", now(), ip, id)
}

func (s *Store) SetDeviceSyncState(ctx context.Context, id int64, state []byte, version string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE devices SET sync_state = ?, sync_state_at = ?, client_version = CASE WHEN ? != '' THEN ? ELSE client_version END WHERE id = ?", string(state), now(), version, version, id)
	return err
}

func (s *Store) CountPendingDevices(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE user_id = ? AND status = 'pending' AND expires_at > ?", userID, now()).Scan(&n)
	return n, err
}

// CountRecentPendingFromIP limits enrolment spam.
func (s *Store) CountRecentPendingForUser(ctx context.Context, userID int64) (int, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM devices WHERE user_id = ? AND status = 'pending' AND expires_at > ?", userID, now()).Scan(&n)
	return n, err
}
