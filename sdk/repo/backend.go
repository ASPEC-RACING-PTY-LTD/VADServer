// Package repo implements a VaultDock repository (backup-format-v1 rev 1.2)
// on top of an object Backend: init and open with any recipient kind, the
// config mac, key files, the epoch chain and rotation, the blob index and
// snapshot roots.
package repo

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"
)

// Kind is an object kind; it is the directory under repo/<uuid>/ (§2) and
// the {kind} of the objects API (api-v1 §5).
type Kind string

const (
	KindConfig    Kind = "config"
	KindKeys      Kind = "keys"
	KindChain     Kind = "chain"
	KindIndex     Kind = "index"
	KindSnapshots Kind = "snapshots"
	KindData      Kind = "data"
	KindLocks     Kind = "locks"
)

// Kinds lists every object kind.
var Kinds = []Kind{KindConfig, KindKeys, KindChain, KindIndex, KindSnapshots, KindData, KindLocks}

// Valid reports whether k is a known kind.
func (k Kind) Valid() bool {
	for _, x := range Kinds {
		if k == x {
			return true
		}
	}
	return false
}

// ConfigOID is the fixed object id of config (api-v1 §5); every other
// object is named by hex(SHA-256(stored bytes)).
var ConfigOID = strings.Repeat("0", 64)

// ObjectInfo describes one stored object.
type ObjectInfo struct {
	OID  string
	Size int64
}

// LockKind is a repository lock mode (§10).
type LockKind string

const (
	LockShared    LockKind = "shared"
	LockExclusive LockKind = "exclusive"
)

// Lock is a held repository lock.
type Lock struct {
	ID      string
	Kind    LockKind
	Expires time.Time
}

var (
	ErrNotFound      = errors.New("repo: object not found")
	ErrInvalidObject = errors.New("repo: invalid object kind, id or content")
	ErrRange         = errors.New("repo: range outside the object")
	ErrLocked        = errors.New("repo: repository is locked")
	ErrNoLock        = errors.New("repo: lock not held")
)

// Backend is the object store a Repo runs on. Keys are the §2 layout:
// (kind, oid), with config at (KindConfig, ConfigOID). Implementations
// must be safe for concurrent use.
//
// Locks are authoritative in the backend: behind the VaultDock server they
// are the server API locks (§10); the in-memory and directory backends keep
// a process-local table.
type Backend interface {
	// Put stores an object. Content-addressed kinds must satisfy
	// oid == hex(SHA-256(data)); re-putting an existing object is not an error.
	Put(ctx context.Context, kind Kind, oid string, data []byte) error
	// Get returns a whole object or ErrNotFound.
	Get(ctx context.Context, kind Kind, oid string) ([]byte, error)
	// GetRange returns length bytes at offset, or ErrRange when the range
	// does not lie inside the object.
	GetRange(ctx context.Context, kind Kind, oid string, offset, length int64) ([]byte, error)
	// Stat returns the object size or ErrNotFound.
	Stat(ctx context.Context, kind Kind, oid string) (int64, error)
	// List returns every object of kind (any order).
	List(ctx context.Context, kind Kind) ([]ObjectInfo, error)
	// Delete removes an object; a missing object is not an error.
	Delete(ctx context.Context, kind Kind, oid string) error

	// Lock takes a lock or fails with ErrLocked when an incompatible lock
	// is held.
	Lock(ctx context.Context, kind LockKind, ttl time.Duration) (Lock, error)
	// RefreshLock extends a held lock; ErrNoLock when it expired or is unknown.
	RefreshLock(ctx context.Context, id string, ttl time.Duration) (Lock, error)
	// Unlock releases a lock; an unknown id is not an error.
	Unlock(ctx context.Context, id string) error
}

// IsOID reports whether s is 64 lowercase hex characters.
func IsOID(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// CheckKey validates a (kind, oid) pair.
func CheckKey(kind Kind, oid string) error {
	if !kind.Valid() || !IsOID(oid) || (kind == KindConfig) != (oid == ConfigOID) {
		return fmt.Errorf("%w: %s/%q", ErrInvalidObject, kind, oid)
	}
	return nil
}

// CheckObject validates a (kind, oid) pair and, for content-addressed kinds,
// that oid is the SHA-256 of data.
func CheckObject(kind Kind, oid string, data []byte) error {
	if err := CheckKey(kind, oid); err != nil {
		return err
	}
	if kind == KindConfig {
		return nil
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != oid {
		return fmt.Errorf("%w: %s/%s content hash mismatch", ErrInvalidObject, kind, oid)
	}
	return nil
}

// LockTable is a process-local lock manager used by the in-memory and
// directory backends.
type LockTable struct {
	mu    sync.Mutex
	locks map[string]Lock
	Now   func() time.Time // nil = time.Now
}

func (t *LockTable) now() time.Time {
	if t.Now != nil {
		return t.Now()
	}
	return time.Now()
}

func (t *LockTable) expire(now time.Time) {
	for id, l := range t.locks {
		if !now.Before(l.Expires) {
			delete(t.locks, id)
		}
	}
}

// Lock implements Backend.Lock.
func (t *LockTable) Lock(_ context.Context, kind LockKind, ttl time.Duration) (Lock, error) {
	if (kind != LockShared && kind != LockExclusive) || ttl <= 0 {
		return Lock{}, fmt.Errorf("repo: invalid lock request")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.expire(now)
	for _, l := range t.locks {
		if kind == LockExclusive || l.Kind == LockExclusive {
			return Lock{}, ErrLocked
		}
	}
	var b [16]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return Lock{}, err
	}
	l := Lock{ID: hex.EncodeToString(b[:]), Kind: kind, Expires: now.Add(ttl)}
	if t.locks == nil {
		t.locks = map[string]Lock{}
	}
	t.locks[l.ID] = l
	return l, nil
}

// RefreshLock implements Backend.RefreshLock.
func (t *LockTable) RefreshLock(_ context.Context, id string, ttl time.Duration) (Lock, error) {
	if ttl <= 0 {
		return Lock{}, fmt.Errorf("repo: invalid lock ttl")
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.expire(now)
	l, ok := t.locks[id]
	if !ok {
		return Lock{}, ErrNoLock
	}
	l.Expires = now.Add(ttl)
	t.locks[id] = l
	return l, nil
}

// Unlock implements Backend.Unlock.
func (t *LockTable) Unlock(_ context.Context, id string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.locks, id)
	return nil
}
