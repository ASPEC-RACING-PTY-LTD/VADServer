// Package server implements repo.Backend over the VaultDock server API
// (api-v1 §5): objects are the streamed object routes of one repository and
// locks are the server-authoritative repository locks.
package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Options configure a Backend. Zero values take the defaults.
type Options struct {
	// MaxRetries bounds the retries of a request answered 503 or 429
	// (default 4; negative disables retries).
	MaxRetries int
	// BaseDelay is the first backoff delay (default 500ms); it doubles per
	// attempt. A server Retry-After longer than the backoff wins.
	BaseDelay time.Duration
	// MaxDelay caps one delay (default 30s).
	MaxDelay time.Duration
	// Sleep waits d or until ctx ends (nil = a timer).
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the clock for lock expiry fallbacks (nil = time.Now).
	Now func() time.Time
	// Logger receives retry notices at debug level (nil = slog.Default()).
	Logger *slog.Logger
}

func (o *Options) fill() {
	if o.MaxRetries == 0 {
		o.MaxRetries = 4
	} else if o.MaxRetries < 0 {
		o.MaxRetries = 0
	}
	if o.BaseDelay <= 0 {
		o.BaseDelay = 500 * time.Millisecond
	}
	if o.MaxDelay <= 0 {
		o.MaxDelay = 30 * time.Second
	}
	if o.Sleep == nil {
		o.Sleep = sleepCtx
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Backend is a repo.Backend for one server repository. It is safe for
// concurrent use.
type Backend struct {
	c      *client.Client
	repoID int64
	opts   Options

	mu    sync.Mutex
	locks map[string]repo.LockKind // kinds of locks taken through this Backend
}

var _ repo.Backend = (*Backend)(nil)

// New returns a Backend for repository repoID. c must carry the device
// credential (DeviceID and Key): streamed PUTs send their body hash only in
// signed requests.
func New(c *client.Client, repoID int64, opts Options) *Backend {
	opts.fill()
	return &Backend{c: c, repoID: repoID, opts: opts, locks: map[string]repo.LockKind{}}
}

// RepoID returns the server id of the repository.
func (b *Backend) RepoID() int64 { return b.repoID }

// LockedError is a 409 repo_locked reply. errors.Is(err, repo.ErrLocked)
// holds; Unwrap returns the *client.APIError.
type LockedError struct {
	Holder  string
	Expires time.Time // zero when the server did not say
	Err     error
}

func (e *LockedError) Error() string {
	if e.Holder != "" {
		return fmt.Sprintf("repo: repository is locked by %s", e.Holder)
	}
	return repo.ErrLocked.Error()
}

func (e *LockedError) Is(target error) bool { return target == repo.ErrLocked }
func (e *LockedError) Unwrap() error        { return e.Err }

// HashMismatchError reports that the server (422 object_hash_mismatch) or
// the client transport rejected an object body whose hash differs from its
// oid. errors.Is(err, repo.ErrInvalidObject) holds.
type HashMismatchError struct {
	Kind repo.Kind
	OID  string
	Err  error
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("repo: %s/%s: object body hash mismatch", e.Kind, e.OID)
}

func (e *HashMismatchError) Is(target error) bool { return target == repo.ErrInvalidObject }
func (e *HashMismatchError) Unwrap() error        { return e.Err }

// retryable reports whether the server asked to come back later.
func retryable(err error) (time.Duration, bool) {
	var ae *client.APIError
	if errors.As(err, &ae) && (ae.Status == http.StatusServiceUnavailable || ae.Status == http.StatusTooManyRequests) {
		return ae.RetryAfter, true
	}
	return 0, false
}

// retry runs fn until it succeeds, fails with a non-retryable error, or
// MaxRetries retries are used up.
func (b *Backend) retry(ctx context.Context, op string, fn func() error) error {
	for attempt := 0; ; attempt++ {
		err := fn()
		ra, ok := retryable(err)
		if err == nil || !ok || attempt >= b.opts.MaxRetries {
			return err
		}
		d := b.opts.BaseDelay * time.Duration(math.Pow(2, float64(attempt)))
		d = max(d, ra)
		if d > b.opts.MaxDelay || d <= 0 {
			d = b.opts.MaxDelay
		}
		b.opts.Logger.Debug("server backend: retrying", "op", op, "attempt", attempt+1, "delay", d, "err", err)
		if err := b.opts.Sleep(ctx, d); err != nil {
			return err
		}
	}
}

// mapErr turns an object-route error into the repo sentinels.
func mapErr(kind repo.Kind, oid string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, client.ErrBodyMismatch) {
		return &HashMismatchError{Kind: kind, OID: oid, Err: err}
	}
	var ae *client.APIError
	if !errors.As(err, &ae) {
		return err
	}
	switch {
	case ae.Code == wire.CodeRepoLocked:
		le := &LockedError{Err: ae}
		if d, ok := client.RepoLocked(ae); ok {
			le.Holder = d.Holder
			if d.ExpiresAt > 0 {
				le.Expires = time.UnixMilli(d.ExpiresAt)
			}
		}
		return le
	case ae.Code == wire.CodeObjectHashMismatch:
		return &HashMismatchError{Kind: kind, OID: oid, Err: ae}
	case ae.Code == wire.CodeObjectNotFound, ae.Status == http.StatusNotFound && ae.Code == "":
		return fmt.Errorf("%w: %s/%s: %w", repo.ErrNotFound, kind, oid, ae)
	case ae.Code == wire.CodeInvalidOID:
		return fmt.Errorf("%w: %s/%s: %w", repo.ErrInvalidObject, kind, oid, ae)
	case ae.Status == http.StatusRequestedRangeNotSatisfiable:
		return fmt.Errorf("%w: %s/%s: %w", repo.ErrRange, kind, oid, ae)
	}
	return err
}

// Put implements repo.Backend: an idempotent streamed PUT. The config body
// replaces the stored config and needs an exclusive lock held by this
// device.
func (b *Backend) Put(ctx context.Context, kind repo.Kind, oid string, data []byte) error {
	if err := repo.CheckObject(kind, oid, data); err != nil {
		return err
	}
	sha := ""
	if kind == repo.KindConfig {
		s := sha256.Sum256(data)
		sha = hex.EncodeToString(s[:])
	}
	err := b.retry(ctx, "put", func() error {
		_, err := b.c.PutObject(ctx, b.repoID, string(kind), oid, bytes.NewReader(data), int64(len(data)), sha)
		return err
	})
	return mapErr(kind, oid, err)
}

// Get implements repo.Backend.
func (b *Backend) Get(ctx context.Context, kind repo.Kind, oid string) ([]byte, error) {
	if err := repo.CheckKey(kind, oid); err != nil {
		return nil, err
	}
	var out []byte
	err := b.retry(ctx, "get", func() error {
		rc, err := b.c.GetObject(ctx, b.repoID, string(kind), oid, 0, 0)
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err = io.ReadAll(rc)
		return err
	})
	if err != nil {
		return nil, mapErr(kind, oid, err)
	}
	return out, nil
}

// GetRange implements repo.Backend with an HTTP Range request. A reply
// shorter than length, a 416, or a reply longer than length (the server
// ignored the Range) are ErrRange or an error.
func (b *Backend) GetRange(ctx context.Context, kind repo.Kind, oid string, offset, length int64) ([]byte, error) {
	if err := repo.CheckKey(kind, oid); err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 {
		return nil, repo.ErrRange
	}
	if length == 0 {
		size, err := b.Stat(ctx, kind, oid)
		if err != nil {
			return nil, err
		}
		if offset > size {
			return nil, repo.ErrRange
		}
		return []byte{}, nil
	}
	var out []byte
	err := b.retry(ctx, "get_range", func() error {
		rc, err := b.c.GetObject(ctx, b.repoID, string(kind), oid, offset, length)
		if err != nil {
			return err
		}
		defer rc.Close()
		out, err = io.ReadAll(io.LimitReader(rc, length+1))
		return err
	})
	if err != nil {
		return nil, mapErr(kind, oid, err)
	}
	switch {
	case int64(len(out)) < length:
		return nil, fmt.Errorf("%w: %s/%s: %d bytes at %d, got %d", repo.ErrRange, kind, oid, length, offset, len(out))
	case int64(len(out)) > length:
		return nil, fmt.Errorf("repo: %s/%s: server ignored the Range request", kind, oid)
	}
	return out, nil
}

// Stat implements repo.Backend with HEAD.
func (b *Backend) Stat(ctx context.Context, kind repo.Kind, oid string) (int64, error) {
	if err := repo.CheckKey(kind, oid); err != nil {
		return 0, err
	}
	var ok bool
	var size int64
	err := b.retry(ctx, "stat", func() error {
		var err error
		ok, size, err = b.c.HeadObject(ctx, b.repoID, string(kind), oid)
		return err
	})
	if err != nil {
		return 0, mapErr(kind, oid, err)
	}
	if !ok {
		return 0, fmt.Errorf("%w: %s/%s", repo.ErrNotFound, kind, oid)
	}
	return size, nil
}

// List implements repo.Backend, following next_cursor to the last page.
func (b *Backend) List(ctx context.Context, kind repo.Kind) ([]repo.ObjectInfo, error) {
	if !kind.Valid() {
		return nil, fmt.Errorf("%w: kind %q", repo.ErrInvalidObject, kind)
	}
	var out []repo.ObjectInfo
	seen := map[string]bool{}
	cursor := ""
	for {
		var page *wire.List[wire.ObjectInfo]
		err := b.retry(ctx, "list", func() error {
			var err error
			page, err = b.c.ListObjects(ctx, b.repoID, string(kind), cursor, wire.ListMaxLimit)
			return err
		})
		if err != nil {
			return nil, mapErr(kind, "", err)
		}
		for _, it := range page.Items {
			if err := repo.CheckKey(kind, it.OID); err != nil || it.Size < 0 {
				return nil, fmt.Errorf("repo: list %s: server returned an invalid object %q", kind, it.OID)
			}
			out = append(out, repo.ObjectInfo{OID: it.OID, Size: it.Size})
		}
		if page.NextCursor == "" {
			return out, nil
		}
		if seen[page.NextCursor] {
			return nil, fmt.Errorf("repo: list %s: server repeated cursor", kind)
		}
		seen[page.NextCursor] = true
		cursor = page.NextCursor
	}
}

// Delete implements repo.Backend. api-v1 has no object DELETE route
// (rev 1.1b; garbage collection is server-side, §9), so it always fails
// with an error wrapping errors.ErrUnsupported.
func (b *Backend) Delete(_ context.Context, kind repo.Kind, oid string) error {
	return fmt.Errorf("repo: delete %s/%s on the server backend: %w", kind, oid, errors.ErrUnsupported)
}

// ttlSeconds rounds ttl up to whole seconds (at least 1).
func ttlSeconds(ttl time.Duration) int {
	return max(1, int((ttl+time.Second-1)/time.Second))
}

func (b *Backend) lockOf(id string, kind repo.LockKind, ttl time.Duration, l *wire.Lock) repo.Lock {
	out := repo.Lock{ID: id, Kind: kind, Expires: b.opts.Now().Add(ttl)}
	if l != nil && l.ExpiresAt > 0 {
		out.Expires = time.UnixMilli(l.ExpiresAt)
	}
	return out
}

// Lock implements repo.Backend with POST /repos/{id}/locks. An
// incompatible lock is a *LockedError (errors.Is repo.ErrLocked).
func (b *Backend) Lock(ctx context.Context, kind repo.LockKind, ttl time.Duration) (repo.Lock, error) {
	if (kind != repo.LockShared && kind != repo.LockExclusive) || ttl <= 0 {
		return repo.Lock{}, fmt.Errorf("repo: invalid lock request")
	}
	var l *wire.Lock
	err := b.retry(ctx, "lock", func() error {
		var err error
		l, err = b.c.AcquireLock(ctx, b.repoID, string(kind), ttlSeconds(ttl))
		return err
	})
	if err != nil {
		return repo.Lock{}, mapErr(repo.KindLocks, "", err)
	}
	if l == nil || l.LockID == "" {
		return repo.Lock{}, errors.New("repo: server returned no lock id")
	}
	b.mu.Lock()
	b.locks[l.LockID] = kind
	b.mu.Unlock()
	return b.lockOf(l.LockID, kind, ttl, l), nil
}

// RefreshLock implements repo.Backend. 404 lock_not_found is repo.ErrNoLock.
func (b *Backend) RefreshLock(ctx context.Context, id string, ttl time.Duration) (repo.Lock, error) {
	if ttl <= 0 || id == "" {
		return repo.Lock{}, fmt.Errorf("repo: invalid lock refresh")
	}
	var l *wire.Lock
	err := b.retry(ctx, "refresh_lock", func() error {
		var err error
		l, err = b.c.RefreshLock(ctx, b.repoID, id, ttlSeconds(ttl))
		return err
	})
	if client.IsCode(err, wire.CodeLockNotFound) {
		b.mu.Lock()
		delete(b.locks, id)
		b.mu.Unlock()
		return repo.Lock{}, fmt.Errorf("%w: %s", repo.ErrNoLock, id)
	}
	if err != nil {
		return repo.Lock{}, mapErr(repo.KindLocks, "", err)
	}
	b.mu.Lock()
	kind := b.locks[id]
	b.mu.Unlock()
	return b.lockOf(id, kind, ttl, l), nil
}

// Unlock implements repo.Backend. An unknown or expired lock (404
// lock_not_found) is not an error.
func (b *Backend) Unlock(ctx context.Context, id string) error {
	err := b.retry(ctx, "unlock", func() error {
		return b.c.ReleaseLock(ctx, b.repoID, id)
	})
	if err == nil || client.IsCode(err, wire.CodeLockNotFound) {
		b.mu.Lock()
		delete(b.locks, id)
		b.mu.Unlock()
		return nil
	}
	return mapErr(repo.KindLocks, "", err)
}

// CommitSnapshot commits a snapshot to the server catalog (api-v1 §5.2)
// after repo.SaveSnapshot stored its root object. It is idempotent by
// snapshot_oid, so 503/429 replies are retried; created is false on a
// replay. Errors other than repo_locked / object_not_found / invalid_oid
// stay *client.APIError (e.g. 409 pack_missing, 403 profile_not_authorised).
func (b *Backend) CommitSnapshot(ctx context.Context, req wire.SnapshotCommitRequest) (resp *wire.SnapshotCommitResponse, created bool, err error) {
	if !repo.IsOID(req.SnapshotOID) {
		return nil, false, fmt.Errorf("%w: snapshot oid %q", repo.ErrInvalidObject, req.SnapshotOID)
	}
	err = b.retry(ctx, "commit_snapshot", func() error {
		var err error
		resp, created, err = b.c.CommitSnapshot(ctx, b.repoID, req)
		return err
	})
	if err != nil {
		return nil, false, mapErr(repo.KindSnapshots, req.SnapshotOID, err)
	}
	return resp, created, nil
}
