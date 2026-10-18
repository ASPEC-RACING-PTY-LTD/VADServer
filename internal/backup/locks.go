// Owner: SRV-2 (P1-15). Repository locks (api-v1 §5, rev 1.1b; backup-format
// §10): server API locks are authoritative for repositories behind the
// VaultDock server.
//
// Semantics:
//   - shared locks coexist; an exclusive lock conflicts with every other
//     unexpired lock on the repository, whoever holds it (including the
//     caller's own device, so a crashed run's lock must expire or be
//     released first);
//   - a lock lives for its TTL and is extended by Refresh, which keeps the
//     TTL it was acquired with unless a new one is given;
//   - Refresh and Release of an unknown, expired or foreign lock return
//     ErrLockNotFound (404 lock_not_found);
//   - expired rows are swept on every Acquire, and Sweep can be called by a
//     periodic job.
//
// Rows live in repo_locks (migration 5). The config PUT (P1-14) reads them
// through store.HoldsExclusiveRepoLock; ConflictHolder formats the holder
// the same way.

package backup

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Lock TTL bounds in seconds. A zero TTL in a request means DefaultLockTTL.
const (
	DefaultLockTTL = 300
	MinLockTTL     = 1
	MaxLockTTL     = 3600
)

// ErrLockNotFound is returned by Refresh and Release for a lock that does
// not exist, has expired, or belongs to another repository or holder.
var ErrLockNotFound = errors.New("lock not found")

// ErrInvalidLock reports a bad lock kind or TTL.
var ErrInvalidLock = errors.New("invalid lock request")

// LockedError is returned by Acquire when an incompatible lock is held
// (409 repo_locked).
type LockedError struct {
	Holder    string
	ExpiresAt int64 // Unix ms
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("repository locked by %s until %d", e.Holder, e.ExpiresAt)
}

// Lock is an acquired or refreshed lock.
type Lock struct {
	ID        string
	RepoID    int64
	Kind      string
	DeviceID  int64 // 0 for a server-held lock
	ExpiresAt int64 // Unix ms
}

// Locks manages repo_locks rows.
type Locks struct {
	DB *sql.DB
	// Now returns the current Unix ms time; nil means store.Now.
	Now func() int64
}

// NewLocks returns a lock manager over the store's database.
func NewLocks(st *store.Store) *Locks { return &Locks{DB: st.DB} }

func (l *Locks) now() int64 {
	if l.Now != nil {
		return l.Now()
	}
	return store.Now()
}

// ValidLockKind reports whether kind is shared or exclusive.
func ValidLockKind(kind string) bool {
	return kind == wire.LockKindShared || kind == wire.LockKindExclusive
}

// NormalizeTTL applies the default to 0 and checks the bounds.
func NormalizeTTL(ttl int) (int, error) {
	if ttl == 0 {
		return DefaultLockTTL, nil
	}
	if ttl < MinLockTTL || ttl > MaxLockTTL {
		return 0, ErrInvalidLock
	}
	return ttl, nil
}

// ConflictHolder formats a lock holder for LockedDetails.holder: the stored
// holder text, else "device <id>" or "server" (as store.HoldsExclusiveRepoLock).
func ConflictHolder(holder string, deviceID sql.NullInt64) string {
	if holder != "" && holder != "{}" {
		return holder
	}
	if deviceID.Valid {
		return fmt.Sprintf("device %d", deviceID.Int64)
	}
	return "server"
}

func newLockID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Acquire takes a lock on repoID for deviceID (0 = a server-held lock,
// recorded with holder text). ttl is in seconds and must already be
// normalised. An incompatible unexpired lock gives *LockedError.
func (l *Locks) Acquire(ctx context.Context, repoID, deviceID int64, kind, holder string, ttl int) (*Lock, error) {
	if !ValidLockKind(kind) || ttl < MinLockTTL || ttl > MaxLockTTL {
		return nil, ErrInvalidLock
	}
	id, err := newLockID()
	if err != nil {
		return nil, err
	}
	t := l.now()
	lk := &Lock{ID: id, RepoID: repoID, Kind: kind, DeviceID: deviceID, ExpiresAt: t + int64(ttl)*1000}
	// _txlock=immediate serialises the check and the insert.
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "DELETE FROM repo_locks WHERE expires_at <= ?", t); err != nil {
		return nil, err
	}
	q := "SELECT holder, holder_device_id, expires_at FROM repo_locks WHERE repo_id = ? AND expires_at > ?"
	if kind == wire.LockKindShared {
		q += " AND kind = 'exclusive'"
	}
	q += " ORDER BY (kind = 'exclusive') DESC, expires_at DESC LIMIT 1"
	var h string
	var dev sql.NullInt64
	var exp int64
	err = tx.QueryRowContext(ctx, q, repoID, t).Scan(&h, &dev, &exp)
	switch {
	case err == nil:
		return nil, &LockedError{Holder: ConflictHolder(h, dev), ExpiresAt: exp}
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	hk, hdev := "server", sql.NullInt64{}
	if deviceID > 0 {
		hk, hdev = "device", sql.NullInt64{Int64: deviceID, Valid: true}
	}
	if holder == "" {
		holder = "{}"
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO repo_locks(id, repo_id, kind, holder_kind, holder_device_id, holder, acquired_at, refreshed_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?)`, id, repoID, kind, hk, hdev, holder, t, t, lk.ExpiresAt); err != nil {
		return nil, err
	}
	return lk, tx.Commit()
}

// Refresh extends an unexpired lock held by deviceID on repoID. ttl 0 keeps
// the lock's current TTL; otherwise it must already be normalised. An
// expired lock is deleted and reported as ErrLockNotFound.
func (l *Locks) Refresh(ctx context.Context, repoID, deviceID int64, lockID string, ttl int) (*Lock, error) {
	if ttl != 0 && (ttl < MinLockTTL || ttl > MaxLockTTL) {
		return nil, ErrInvalidLock
	}
	t := l.now()
	tx, err := l.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var kind string
	var refreshed, exp int64
	err = tx.QueryRowContext(ctx, `SELECT kind, refreshed_at, expires_at FROM repo_locks
		WHERE id = ? AND repo_id = ? AND holder_kind = 'device' AND holder_device_id = ?`, lockID, repoID, deviceID).Scan(&kind, &refreshed, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrLockNotFound
	}
	if err != nil {
		return nil, err
	}
	if exp <= t {
		if _, err := tx.ExecContext(ctx, "DELETE FROM repo_locks WHERE id = ?", lockID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return nil, ErrLockNotFound
	}
	life := int64(ttl) * 1000
	if ttl == 0 {
		life = max(exp-refreshed, int64(MinLockTTL)*1000)
	}
	lk := &Lock{ID: lockID, RepoID: repoID, Kind: kind, DeviceID: deviceID, ExpiresAt: t + life}
	if _, err := tx.ExecContext(ctx, "UPDATE repo_locks SET refreshed_at = ?, expires_at = ? WHERE id = ?", t, lk.ExpiresAt, lockID); err != nil {
		return nil, err
	}
	return lk, tx.Commit()
}

// Release deletes an unexpired lock held by deviceID on repoID. An unknown
// or expired lock gives ErrLockNotFound (an expired row is removed anyway).
func (l *Locks) Release(ctx context.Context, repoID, deviceID int64, lockID string) error {
	var exp int64
	err := l.DB.QueryRowContext(ctx, `DELETE FROM repo_locks WHERE id = ? AND repo_id = ? AND holder_kind = 'device' AND holder_device_id = ?
		RETURNING expires_at`, lockID, repoID, deviceID).Scan(&exp)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrLockNotFound
	}
	if err != nil {
		return err
	}
	if exp <= l.now() {
		return ErrLockNotFound
	}
	return nil
}

// Sweep deletes every expired lock and returns how many were removed.
func (l *Locks) Sweep(ctx context.Context) (int64, error) {
	res, err := l.DB.ExecContext(ctx, "DELETE FROM repo_locks WHERE expires_at <= ?", l.now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunSweeper calls Sweep every interval until ctx ends.
func (l *Locks) RunSweeper(ctx context.Context, interval time.Duration) {
	tk := time.NewTicker(interval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			_, _ = l.Sweep(ctx)
		}
	}
}
