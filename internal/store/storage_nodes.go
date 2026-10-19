package store

// Storage nodes: vad-node agents on other machines. A node asks to join
// (status pending, expires after an hour), an admin approves or rejects it,
// and on its next poll an approved node receives its secret exactly once
// (only the hash is kept). Approved nodes report a heartbeat and receive
// updates from this server only.

import (
	"context"
	"database/sql"
	"errors"
)

// NodeJoinTTLMillis is how long a join request waits for an admin decision.
const NodeJoinTTLMillis = 60 * 60 * 1000

type StorageNode struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	Status          string `json:"status"` // pending, approved, rejected, revoked; "expired" is derived
	Hostname        string `json:"hostname"`
	ReportedIP      string `json:"reported_ip"`
	RequestIP       string `json:"request_ip"`
	RequestLocation string `json:"request_location"`
	OS              string `json:"os"`
	Arch            string `json:"arch"`
	Port            int64  `json:"port"`
	Proxy           string `json:"proxy"`
	PublicURL       string `json:"public_url"`
	Version         string `json:"version"`
	SecretDelivered bool   `json:"secret_delivered"`
	AutoUpdate      bool   `json:"auto_update"`
	UpdateRequested bool   `json:"update_requested"`
	DiskTotal       int64  `json:"disk_total"`
	DiskFree        int64  `json:"disk_free"`
	UptimeSeconds   int64  `json:"uptime_seconds"`
	LastIP          string `json:"last_ip"`
	LastSeenAt      *int64 `json:"last_seen_at"`
	LastError       string `json:"last_error"`
	TerminalEnabled bool   `json:"terminal_enabled"`
	StorageRoots    string `json:"storage_roots"` // newline separated
	DecidedBy       *int64 `json:"decided_by"`
	DecidedAt       *int64 `json:"decided_at"`
	ExpiresAt       int64  `json:"expires_at"`
	CreatedAt       int64  `json:"created_at"`
	UpdatedAt       int64  `json:"updated_at"`

	pollTokenHash string
	secretHash    string
}

// Expired reports whether a pending request ran out of time.
func (n *StorageNode) Expired() bool { return n.Status == "pending" && now() > n.ExpiresAt }

// PollTokenHash and SecretHash are the stored hashes (never serialised).
func (n *StorageNode) PollTokenHash() string { return n.pollTokenHash }
func (n *StorageNode) SecretHash() string    { return n.secretHash }

var ErrStorageNodeNotFound = errors.New("storage node not found")

const storageNodeCols = `id, name, status, hostname, reported_ip, request_ip, request_location, os, arch, port, proxy, public_url, version,
	poll_token_hash, secret_hash, secret_delivered, auto_update, update_requested, disk_total, disk_free, uptime_seconds,
	last_ip, last_seen_at, last_error, terminal_enabled, storage_roots, decided_by, decided_at, expires_at, created_at, updated_at`

func scanStorageNode(row interface{ Scan(...any) error }) (*StorageNode, error) {
	var n StorageNode
	var seen, by, at sql.NullInt64
	err := row.Scan(&n.ID, &n.Name, &n.Status, &n.Hostname, &n.ReportedIP, &n.RequestIP, &n.RequestLocation, &n.OS, &n.Arch,
		&n.Port, &n.Proxy, &n.PublicURL, &n.Version, &n.pollTokenHash, &n.secretHash, &n.SecretDelivered, &n.AutoUpdate,
		&n.UpdateRequested, &n.DiskTotal, &n.DiskFree, &n.UptimeSeconds, &n.LastIP, &seen, &n.LastError, &n.TerminalEnabled, &n.StorageRoots, &by, &at,
		&n.ExpiresAt, &n.CreatedAt, &n.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrStorageNodeNotFound
	}
	if err != nil {
		return nil, err
	}
	n.LastSeenAt, n.DecidedBy, n.DecidedAt = nullInt(seen), nullInt(by), nullInt(at)
	return &n, nil
}

// StorageNodeJoin is a new join request.
type StorageNodeJoin struct {
	Name, Hostname, ReportedIP, RequestIP, RequestLocation, OS, Arch, Proxy, PublicURL, Version string
	Port                                                                                        int64
	PollTokenHash                                                                               string
}

func (s *Store) CreateStorageNodeRequest(ctx context.Context, j StorageNodeJoin) (*StorageNode, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO storage_nodes(name, status, hostname, reported_ip, request_ip, request_location, os, arch, port, proxy, public_url, version, poll_token_hash, expires_at, created_at, updated_at)
		VALUES(?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.Name, j.Hostname, j.ReportedIP, j.RequestIP, j.RequestLocation, j.OS, j.Arch, j.Port, j.Proxy, j.PublicURL, j.Version, j.PollTokenHash,
		t+NodeJoinTTLMillis, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.StorageNode(ctx, id)
}

func (s *Store) StorageNode(ctx context.Context, id int64) (*StorageNode, error) {
	return scanStorageNode(s.DB.QueryRowContext(ctx, "SELECT "+storageNodeCols+" FROM storage_nodes WHERE id = ?", id))
}

// Nodes lists every node, newest first. Pending requests that expired more
// than a day ago are removed first.
func (s *Store) StorageNodes(ctx context.Context) ([]StorageNode, error) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM storage_nodes WHERE status = 'pending' AND expires_at < ?", now()-24*60*60*1000)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+storageNodeCols+" FROM storage_nodes ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageNode{}
	for rows.Next() {
		n, err := scanStorageNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// DecideStorageNode approves or rejects a pending, unexpired request. It returns
// false when the request is not pending any more (or expired).
func (s *Store) DecideStorageNode(ctx context.Context, id int64, status string, by int64) (bool, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `UPDATE storage_nodes SET status = ?, decided_by = ?, decided_at = ?, updated_at = ?
		WHERE id = ? AND status = 'pending' AND expires_at >= ?`, status, by, t, t, id, t)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeliverStorageNodeSecret stores the secret's hash for an approved node that has
// not received one yet. It returns false when the secret was already issued,
// so a secret is handed out exactly once.
func (s *Store) DeliverStorageNodeSecret(ctx context.Context, id int64, secretHash string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE storage_nodes SET secret_hash = ?, secret_delivered = 1, poll_token_hash = '', updated_at = ?
		WHERE id = ? AND status = 'approved' AND secret_delivered = 0`, secretHash, now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// StorageNodeBySecret returns the approved node with this secret hash.
func (s *Store) StorageNodeBySecret(ctx context.Context, id int64, secretHash string) (*StorageNode, error) {
	return scanStorageNode(s.DB.QueryRowContext(ctx, "SELECT "+storageNodeCols+" FROM storage_nodes WHERE id = ? AND status = 'approved' AND secret_hash = ? AND secret_hash != ''", id, secretHash))
}

// StorageNodeHeartbeat is what a node reports periodically.
type StorageNodeHeartbeat struct {
	Hostname, ReportedIP, LastIP, Version, PublicURL, LastError, OS, Arch string
	Port, DiskTotal, DiskFree, UptimeSeconds                              int64
	TerminalEnabled                                                       bool
	StorageRoots                                                          string
}

// RecordStorageNodeHeartbeat stores a heartbeat. A pending "update now" is cleared
// once the node reports the version it was asked to run.
func (s *Store) RecordStorageNodeHeartbeat(ctx context.Context, id int64, h StorageNodeHeartbeat, serverVersion string) error {
	t := now()
	_, err := s.DB.ExecContext(ctx, `UPDATE storage_nodes SET hostname = ?, reported_ip = ?, last_ip = ?, version = ?, public_url = ?, last_error = ?,
		os = COALESCE(NULLIF(?, ''), os), arch = COALESCE(NULLIF(?, ''), arch), terminal_enabled = ?, storage_roots = ?,
		port = ?, disk_total = ?, disk_free = ?, uptime_seconds = ?, last_seen_at = ?, updated_at = ?,
		update_requested = CASE WHEN ? = ? THEN 0 ELSE update_requested END
		WHERE id = ?`,
		h.Hostname, h.ReportedIP, h.LastIP, h.Version, h.PublicURL, h.LastError, h.OS, h.Arch, h.TerminalEnabled, h.StorageRoots, h.Port, h.DiskTotal, h.DiskFree, h.UptimeSeconds, t, t,
		h.Version, serverVersion, id)
	return err
}

// PatchStorageNode changes the name and/or the automatic update flag.
func (s *Store) PatchStorageNode(ctx context.Context, id int64, name *string, autoUpdate *bool) error {
	n, err := s.StorageNode(ctx, id)
	if err != nil {
		return err
	}
	if name != nil {
		n.Name = *name
	}
	if autoUpdate != nil {
		n.AutoUpdate = *autoUpdate
	}
	_, err = s.DB.ExecContext(ctx, "UPDATE storage_nodes SET name = ?, auto_update = ?, updated_at = ? WHERE id = ?", n.Name, n.AutoUpdate, now(), id)
	return err
}

func (s *Store) RequestStorageNodeUpdate(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE storage_nodes SET update_requested = 1, updated_at = ? WHERE id = ? AND status = 'approved'", now(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrStorageNodeNotFound
	}
	return nil
}

// RevokeStorageNode cuts an approved node off: its secret stops working.
func (s *Store) RevokeStorageNode(ctx context.Context, id int64, by int64) error {
	t := now()
	res, err := s.DB.ExecContext(ctx, "UPDATE storage_nodes SET status = 'revoked', secret_hash = '', decided_by = ?, decided_at = ?, updated_at = ? WHERE id = ? AND status = 'approved'", by, t, t, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrStorageNodeNotFound
	}
	return nil
}

func (s *Store) DeleteStorageNode(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM storage_nodes WHERE id = ? AND status != 'approved'", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrStorageNodeNotFound
	}
	return nil
}
