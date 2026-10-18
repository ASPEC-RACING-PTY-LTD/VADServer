package store

import (
	"context"
	"database/sql"
	"errors"
)

// Device enrolment keys and the repository key projection (api-v1 §5A;
// backup-format-v1 §4; owner SRV-K, P1-13).
//
//   - device_keys holds the X25519 box_pub a device sent at enrolment and
//     the SIK fingerprint returned to it with the approved enrolment poll
//     (its pin).
//   - repo_keys is the server-side projection of the plaintext key files
//     (repo_objects kind 'keys'), filled lazily from the stored objects.

// DeviceKey is one row of device_keys.
type DeviceKey struct {
	DeviceID             int64
	BoxPub               string // std base64 X25519 public key
	SIKPinnedFingerprint string // "" until the approved enrolment poll
	CreatedAt            int64
	UpdatedAt            int64
}

// SetDeviceBoxPub stores (or replaces) a device's box_pub.
func (s *Store) SetDeviceBoxPub(ctx context.Context, deviceID int64, boxPub string) error {
	t := now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO device_keys(device_id, box_pub, created_at, updated_at) VALUES(?,?,?,?)
		ON CONFLICT(device_id) DO UPDATE SET box_pub = excluded.box_pub, updated_at = excluded.updated_at`,
		deviceID, boxPub, t, t)
	return err
}

// DeviceKey returns a device's enrolment keys, or ErrNotFound when the
// device sent no box_pub.
func (s *Store) DeviceKey(ctx context.Context, deviceID int64) (*DeviceKey, error) {
	var k DeviceKey
	err := s.DB.QueryRowContext(ctx, `SELECT device_id, box_pub, sik_pinned_fingerprint, created_at, updated_at
		FROM device_keys WHERE device_id = ?`, deviceID).Scan(&k.DeviceID, &k.BoxPub, &k.SIKPinnedFingerprint, &k.CreatedAt, &k.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

// PinDeviceSIK records the SIK fingerprint handed to the device, unless a
// pin is already recorded (the device pins on first use; re-pinning is an
// explicit, separate action). A device without a device_keys row is left
// alone.
func (s *Store) PinDeviceSIK(ctx context.Context, deviceID int64, fingerprint string) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE device_keys SET sik_pinned_fingerprint = ?, updated_at = ?
		WHERE device_id = ? AND sik_pinned_fingerprint = ''`, fingerprint, now(), deviceID)
	return err
}

// ActiveDeviceKeys returns the approved devices of a user that have a
// box_pub, keyed by device id.
func (s *Store) ActiveDeviceKeys(ctx context.Context, userID int64) (map[int64]DeviceKey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT k.device_id, k.box_pub, k.sik_pinned_fingerprint, k.created_at, k.updated_at
		FROM device_keys k JOIN devices d ON d.id = k.device_id WHERE d.user_id = ? AND d.status = 'approved'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]DeviceKey{}
	for rows.Next() {
		var k DeviceKey
		if err := rows.Scan(&k.DeviceID, &k.BoxPub, &k.SIKPinnedFingerprint, &k.CreatedAt, &k.UpdatedAt); err != nil {
			return nil, err
		}
		out[k.DeviceID] = k
	}
	return out, rows.Err()
}

// RepoKey is one row of repo_keys: the projection of a key file.
type RepoKey struct {
	RepoID       int64
	KeyFileOID   string
	KeyID        string
	Kind         string // device|escrow|recovery|passphrase
	Epoch        int64
	Label        string
	DeviceID     int64 // 0 when not a device key file or the device is gone
	RecipientPub string
	CreatedAt    int64
}

// AddRepoKey inserts a key file projection; an existing (repo, oid) row is
// kept.
func (s *Store) AddRepoKey(ctx context.Context, k *RepoKey) error {
	var dev any
	if k.DeviceID > 0 {
		dev = k.DeviceID
	}
	if k.CreatedAt == 0 {
		k.CreatedAt = now()
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO repo_keys(repo_id, key_file_oid, key_id, kind, epoch, label, device_id, recipient_pub, created_at)
		VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(repo_id, key_file_oid) DO NOTHING`,
		k.RepoID, k.KeyFileOID, k.KeyID, k.Kind, k.Epoch, k.Label, dev, k.RecipientPub, k.CreatedAt)
	return err
}

// ListRepoKeys returns a repository's key file projections, newest epoch
// first.
func (s *Store) ListRepoKeys(ctx context.Context, repoID int64) ([]RepoKey, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT repo_id, key_file_oid, key_id, kind, epoch, label, device_id, recipient_pub, created_at
		FROM repo_keys WHERE repo_id = ? ORDER BY epoch DESC, kind, created_at, key_file_oid`, repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoKey{}
	for rows.Next() {
		var k RepoKey
		var dev sql.NullInt64
		if err := rows.Scan(&k.RepoID, &k.KeyFileOID, &k.KeyID, &k.Kind, &k.Epoch, &k.Label, &dev, &k.RecipientPub, &k.CreatedAt); err != nil {
			return nil, err
		}
		k.DeviceID = dev.Int64
		out = append(out, k)
	}
	return out, rows.Err()
}

// UnprojectedKeyObjects returns up to limit live key-file objects of a
// repository that have no repo_keys row yet.
func (s *Store) UnprojectedKeyObjects(ctx context.Context, repoID int64, limit int) ([]string, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT o.oid FROM repo_objects o
		WHERE o.repo_id = ? AND o.kind = 'keys' AND o.delete_pending_at IS NULL
		AND NOT EXISTS (SELECT 1 FROM repo_keys k WHERE k.repo_id = o.repo_id AND k.key_file_oid = o.oid)
		ORDER BY o.id LIMIT ?`, repoID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var oid string
		if err := rows.Scan(&oid); err != nil {
			return nil, err
		}
		out = append(out, oid)
	}
	return out, rows.Err()
}

// RaiseRepoEpoch sets repositories.current_epoch to epoch when it is
// higher than the stored one.
func (s *Store) RaiseRepoEpoch(ctx context.Context, repoID, epoch int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE repositories SET current_epoch = ?, updated_at = ? WHERE id = ? AND current_epoch < ?`,
		epoch, now(), repoID, epoch)
	return err
}

// SetRepoEscrowMode changes a repository's escrow mode (full|metadata).
func (s *Store) SetRepoEscrowMode(ctx context.Context, repoID int64, mode string) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE repositories SET escrow_mode = ?, updated_at = ? WHERE id = ?`, mode, now(), repoID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OpenTask is an unfinished device task (see OpenTasks).
type OpenTask struct {
	ID       int64
	DeviceID int64
	Payload  string
}

// OpenTasks returns a user's device tasks of kind that are not finished
// (pending, awaiting approval or claimed).
func (s *Store) OpenTasks(ctx context.Context, userID int64, kind string) ([]OpenTask, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id, device_id, payload FROM device_tasks
		WHERE user_id = ? AND kind = ? AND status IN ('pending','awaiting_approval','claimed') ORDER BY id`, userID, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OpenTask
	for rows.Next() {
		var t OpenTask
		if err := rows.Scan(&t.ID, &t.DeviceID, &t.Payload); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AllRepositoryIDs returns the ids of every repository (all users).
func (s *Store) AllRepositoryIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM repositories ORDER BY id`)
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
