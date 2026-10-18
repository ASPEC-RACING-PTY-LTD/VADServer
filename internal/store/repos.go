package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Backup repositories and their objects (api-v1 §5; owner SRV-2, P1-14).
//
// Storage notes:
//   - The built-in server destination (api-v1 §14 rev 1.1b) is the oldest
//     destinations row with kind 'server' and scope 'global'. It is created
//     on demand; its config is {"root":"<abs path>"}, the localfs root fixed
//     when the row was created.
//   - repo_objects rows are written only after the object is stored and
//     verified. A row with delete_pending_at set is queued for GC and counts
//     as missing; a re-PUT clears the mark.
//   - Backup quota is users.backup_quota_bytes (0 = unlimited) against the
//     sum of repo_objects.size over all of the user's repositories.

// BuiltinDestinationName is the name of the built-in server destination.
const BuiltinDestinationName = "VaultDock server"

var (
	// ErrRepoExists is returned by CreateRepository when the user already
	// has a repository on the destination (one repo per user and
	// destination).
	ErrRepoExists = errors.New("the user already has a repository on this destination")
	// ErrBackupQuota is returned when an object would exceed the user's
	// backup quota (413 backup_quota_exceeded).
	ErrBackupQuota = errors.New("backup quota exceeded")
)

// Repository is one row of repositories.
type Repository struct {
	ID                int64
	UserID            int64
	DestinationID     int64
	RepoUUID          string
	Name              string
	FormatVersion     int
	EscrowMode        string
	CurrentEpoch      int64
	CreatedByDeviceID int64 // 0 when unknown or the device was deleted
	LastCatalogSyncAt int64 // 0 when never
	CreatedAt         int64
	UpdatedAt         int64
}

// RepositoryUsage is the usage block of a repository (api-v1 §5).
type RepositoryUsage struct {
	Objects      int64
	StoredBytes  int64
	LogicalBytes int64
	Snapshots    int64
}

// RepoObject is one live row of repo_objects.
type RepoObject struct {
	ID        int64
	RepoID    int64
	Kind      string
	OID       string
	Size      int64
	CreatedAt int64
}

// Destination kinds of the destinations table.
const destKindServer = "server"

// ServerDestination returns the id and localfs root of the built-in server
// destination, creating it with defaultRoot when it does not exist yet.
func (s *Store) ServerDestination(ctx context.Context, defaultRoot string) (id int64, root string, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var cfg string
		err := tx.QueryRowContext(ctx, "SELECT id, config FROM destinations WHERE kind = ? AND scope = 'global' ORDER BY id LIMIT 1", destKindServer).Scan(&id, &cfg)
		if err == nil {
			root, err = destinationRoot(cfg)
			return err
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if defaultRoot == "" {
			return errors.New("server destination: no default root")
		}
		b, err := json.Marshal(map[string]string{"root": defaultRoot})
		if err != nil {
			return err
		}
		t := now()
		res, err := tx.ExecContext(ctx, `INSERT INTO destinations(scope, user_id, name, kind, config, health, created_at, updated_at)
			VALUES('global', NULL, ?, ?, ?, 'ok', ?, ?)`, BuiltinDestinationName, destKindServer, string(b), t, t)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		root = defaultRoot
		return err
	})
	return id, root, err
}

// DestinationTarget returns the kind and (for server destinations) the
// localfs root of a destination that userID may use: a global one or the
// user's own. Others are ErrNotFound.
func (s *Store) DestinationTarget(ctx context.Context, id, userID int64) (kind, root string, err error) {
	var cfg string
	var owner sql.NullInt64
	var enabled bool
	err = s.DB.QueryRowContext(ctx, "SELECT kind, config, user_id, enabled FROM destinations WHERE id = ?", id).Scan(&kind, &cfg, &owner, &enabled)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && owner.Valid && owner.Int64 != userID) {
		return "", "", ErrNotFound
	}
	if err != nil {
		return "", "", err
	}
	if kind == destKindServer {
		root, err = destinationRoot(cfg)
	}
	return kind, root, err
}

func destinationRoot(cfg string) (string, error) {
	var c struct {
		Root string `json:"root"`
	}
	if err := json.Unmarshal([]byte(jsonOr(cfg, "{}")), &c); err != nil {
		return "", fmt.Errorf("server destination config: %w", err)
	}
	if c.Root == "" {
		return "", errors.New("server destination has no root")
	}
	return c.Root, nil
}

const repoCols = "id, user_id, destination_id, repo_uuid, name, format_version, escrow_mode, current_epoch, created_by_device_id, last_catalog_sync_at, created_at, updated_at"

func scanRepository(sc interface{ Scan(...any) error }) (*Repository, error) {
	var r Repository
	var dev, sync sql.NullInt64
	err := sc.Scan(&r.ID, &r.UserID, &r.DestinationID, &r.RepoUUID, &r.Name, &r.FormatVersion, &r.EscrowMode,
		&r.CurrentEpoch, &dev, &sync, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedByDeviceID, r.LastCatalogSyncAt = dev.Int64, sync.Int64
	return &r, nil
}

// CreateRepository inserts r, idempotent by RepoUUID: when the uuid already
// belongs to r.UserID the stored row is copied into r and created is false.
// A uuid of another user is ErrConflict; a second repository of the user on
// the same destination is ErrRepoExists (r.ID is then set to the existing
// repository's id).
func (s *Store) CreateRepository(ctx context.Context, r *Repository) (created bool, err error) {
	if r.UserID <= 0 || r.DestinationID <= 0 || r.RepoUUID == "" || r.Name == "" {
		return false, errors.New("repository user, destination, uuid and name required")
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		ex, err := scanRepository(tx.QueryRowContext(ctx, "SELECT "+repoCols+" FROM repositories WHERE repo_uuid = ?", r.RepoUUID))
		if err == nil {
			if ex.UserID != r.UserID {
				return ErrConflict
			}
			*r = *ex
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		var other int64
		err = tx.QueryRowContext(ctx, "SELECT id FROM repositories WHERE user_id = ? AND destination_id = ?", r.UserID, r.DestinationID).Scan(&other)
		if err == nil {
			r.ID = other
			return ErrRepoExists
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if r.FormatVersion == 0 {
			r.FormatVersion = 1
		}
		if r.EscrowMode == "" {
			r.EscrowMode = "full"
		}
		if r.CurrentEpoch == 0 {
			r.CurrentEpoch = 1
		}
		r.CreatedAt = now()
		r.UpdatedAt = r.CreatedAt
		res, err := tx.ExecContext(ctx, `INSERT INTO repositories(user_id, destination_id, repo_uuid, name, format_version, escrow_mode, current_epoch, created_by_device_id, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?)`, r.UserID, r.DestinationID, r.RepoUUID, r.Name, r.FormatVersion, r.EscrowMode,
			r.CurrentEpoch, nullID(r.CreatedByDeviceID), r.CreatedAt, r.UpdatedAt)
		if err != nil {
			return err
		}
		r.ID, err = res.LastInsertId()
		created = true
		return err
	})
	return created, err
}

// Repository returns a repository by id (any owner); callers check
// ownership.
func (s *Store) Repository(ctx context.Context, id int64) (*Repository, error) {
	return scanRepository(s.DB.QueryRowContext(ctx, "SELECT "+repoCols+" FROM repositories WHERE id = ?", id))
}

// ListRepositories returns the user's repositories in ascending id order,
// starting after afterID.
func (s *Store) ListRepositories(ctx context.Context, userID, afterID int64, limit int) ([]Repository, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+repoCols+" FROM repositories WHERE user_id = ? AND id > ? ORDER BY id LIMIT ?", userID, afterID, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Repository{}
	for rows.Next() {
		r, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// RepositoryUsage sums the live objects and committed snapshots of a
// repository.
func (s *Store) RepositoryUsage(ctx context.Context, repoID int64) (RepositoryUsage, error) {
	var u RepositoryUsage
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(size), 0) FROM repo_objects WHERE repo_id = ? AND delete_pending_at IS NULL", repoID).
		Scan(&u.Objects, &u.StoredBytes)
	if err != nil {
		return u, err
	}
	err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(logical_bytes), 0) FROM snapshots WHERE repo_id = ? AND status = 'committed'", repoID).
		Scan(&u.Snapshots, &u.LogicalBytes)
	return u, err
}

// --- objects ---

// RepoObject returns a live (not pending-delete) object row or ErrNotFound.
func (s *Store) RepoObject(ctx context.Context, repoID int64, kind, oid string) (*RepoObject, error) {
	var o RepoObject
	err := s.DB.QueryRowContext(ctx, "SELECT id, repo_id, kind, oid, size, created_at FROM repo_objects WHERE repo_id = ? AND kind = ? AND oid = ? AND delete_pending_at IS NULL",
		repoID, kind, oid).Scan(&o.ID, &o.RepoID, &o.Kind, &o.OID, &o.Size, &o.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &o, nil
}

// CheckBackupQuota returns ErrBackupQuota when adding add bytes to the
// user's stored backup objects would exceed users.backup_quota_bytes
// (0 = unlimited).
func (s *Store) CheckBackupQuota(ctx context.Context, userID, add int64) error {
	var quota int64
	if err := s.DB.QueryRowContext(ctx, "SELECT backup_quota_bytes FROM users WHERE id = ?", userID).Scan(&quota); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if quota <= 0 {
		return nil
	}
	var used int64
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(o.size), 0) FROM repo_objects o
		JOIN repositories r ON r.id = o.repo_id WHERE r.user_id = ?`, userID).Scan(&used)
	if err != nil {
		return err
	}
	if used+add > quota {
		return ErrBackupQuota
	}
	return nil
}

// PutRepoObject records a stored and verified object. An existing row gets
// the new size and loses any pending-delete mark. existed reports whether a
// live row was already present.
func (s *Store) PutRepoObject(ctx context.Context, repoID int64, kind, oid string, size int64) (existed bool, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		var pending sql.NullInt64
		err := tx.QueryRowContext(ctx, "SELECT delete_pending_at FROM repo_objects WHERE repo_id = ? AND kind = ? AND oid = ?", repoID, kind, oid).Scan(&pending)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			_, err = tx.ExecContext(ctx, "INSERT INTO repo_objects(repo_id, kind, oid, size, created_at) VALUES(?,?,?,?,?)", repoID, kind, oid, size, now())
			return err
		case err != nil:
			return err
		}
		existed = !pending.Valid
		_, err = tx.ExecContext(ctx, "UPDATE repo_objects SET size = ?, delete_pending_at = NULL WHERE repo_id = ? AND kind = ? AND oid = ?", size, repoID, kind, oid)
		return err
	})
	return existed, err
}

// ListRepoObjects returns live objects of a repository in ascending id
// order after afterID; kind "" lists every kind.
func (s *Store) ListRepoObjects(ctx context.Context, repoID int64, kind string, afterID int64, limit int) ([]RepoObject, error) {
	where := "repo_id = ? AND id > ? AND delete_pending_at IS NULL"
	args := []any{repoID, afterID}
	if kind != "" {
		where += " AND kind = ?"
		args = append(args, kind)
	}
	args = append(args, clampLimit(limit))
	rows, err := s.DB.QueryContext(ctx, "SELECT id, repo_id, kind, oid, size, created_at FROM repo_objects WHERE "+where+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RepoObject{}
	for rows.Next() {
		var o RepoObject
		if err := rows.Scan(&o.ID, &o.RepoID, &o.Kind, &o.OID, &o.Size, &o.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// missingBatch bounds the IN list of one MissingRepoObjects query.
const missingBatch = 500

// MissingRepoObjects returns the oids (in request order, without
// duplicates) that have no live row of kind in the repository.
// Pending-delete objects count as missing.
func (s *Store) MissingRepoObjects(ctx context.Context, repoID int64, kind string, oids []string) ([]string, error) {
	have := make(map[string]bool, len(oids))
	for i := 0; i < len(oids); i += missingBatch {
		batch := oids[i:min(i+missingBatch, len(oids))]
		args := make([]any, 0, len(batch)+2)
		args = append(args, repoID, kind)
		for _, o := range batch {
			args = append(args, o)
		}
		q := "SELECT oid FROM repo_objects WHERE repo_id = ? AND kind = ? AND delete_pending_at IS NULL AND oid IN (?" + strings.Repeat(",?", len(batch)-1) + ")"
		rows, err := s.DB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o string
			if err := rows.Scan(&o); err != nil {
				rows.Close()
				return nil, err
			}
			have[o] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	missing := []string{}
	for _, o := range oids {
		if !have[o] {
			have[o] = true // report each oid once
			missing = append(missing, o)
		}
	}
	return missing, nil
}

// --- locks (read-only; the lock routes belong to P1-15) ---

// RepoLockHolder describes the holder of an unexpired repository lock.
type RepoLockHolder struct {
	Holder    string
	ExpiresAt int64
}

// HoldsExclusiveRepoLock reports whether deviceID holds an unexpired
// exclusive lock on the repository. When it does not, other describes
// another unexpired lock (if any) for the 409 repo_locked details.
func (s *Store) HoldsExclusiveRepoLock(ctx context.Context, repoID, deviceID int64) (held bool, other *RepoLockHolder, err error) {
	t := now()
	var n int64
	err = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM repo_locks WHERE repo_id = ? AND kind = 'exclusive'
		AND holder_kind = 'device' AND holder_device_id = ? AND expires_at > ?`, repoID, deviceID, t).Scan(&n)
	if err != nil || n > 0 {
		return n > 0, nil, err
	}
	var h RepoLockHolder
	var dev sql.NullInt64
	err = s.DB.QueryRowContext(ctx, `SELECT holder, holder_device_id, expires_at FROM repo_locks WHERE repo_id = ? AND expires_at > ?
		ORDER BY (kind = 'exclusive') DESC, expires_at DESC LIMIT 1`, repoID, t).Scan(&h.Holder, &dev, &h.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if h.Holder == "" || h.Holder == "{}" {
		if dev.Valid {
			h.Holder = fmt.Sprintf("device %d", dev.Int64)
		} else {
			h.Holder = "server"
		}
	}
	return false, &h, nil
}
