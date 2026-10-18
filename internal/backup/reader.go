package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
)

// ErrReadOnly is returned by the write methods of ReadBackend.
var ErrReadOnly = errors.New("backup: repository opened read-only")

// maxWholeObject bounds whole-object reads through ReadBackend (config,
// keys, chain, index and snapshot objects; packs are read by range).
const maxWholeObject = 256 << 20

// ObjectLister lists a repository's objects of one kind (the database is the
// listing source of truth, api-v1 §5).
type ObjectLister func(ctx context.Context, kind string) ([]repo.ObjectInfo, error)

// ReadBackend adapts one repository in the object store to repo.Backend
// for server-side reads (full-escrow browse and download, api-v1 §5.4).
// Every write method fails with ErrReadOnly.
type ReadBackend struct {
	bs   *Store
	uuid string
	list ObjectLister
}

// NewReadBackend returns a read-only backend for repository repoUUID.
func NewReadBackend(bs *Store, repoUUID string, list ObjectLister) *ReadBackend {
	return &ReadBackend{bs: bs, uuid: repoUUID, list: list}
}

var _ repo.Backend = (*ReadBackend)(nil)

func mapErr(err error) error {
	if errors.Is(err, objstore.ErrNotFound) {
		return fmt.Errorf("%w: %w", repo.ErrNotFound, err)
	}
	return err
}

// Get implements repo.Backend.
func (b *ReadBackend) Get(ctx context.Context, kind repo.Kind, oid string) ([]byte, error) {
	rc, size, err := b.bs.Get(ctx, b.uuid, string(kind), oid)
	if err != nil {
		return nil, mapErr(err)
	}
	defer rc.Close()
	if size > maxWholeObject {
		return nil, fmt.Errorf("backup: object %s/%s too large to read whole (%d bytes)", kind, oid, size)
	}
	return io.ReadAll(io.LimitReader(rc, maxWholeObject))
}

// GetRange implements repo.Backend.
func (b *ReadBackend) GetRange(ctx context.Context, kind repo.Kind, oid string, offset, length int64) ([]byte, error) {
	if offset < 0 || length < 0 || length > maxWholeObject {
		return nil, repo.ErrRange
	}
	rc, err := b.bs.GetRange(ctx, b.uuid, string(kind), oid, offset, length)
	if err != nil {
		if errors.Is(err, objstore.ErrInvalidRange) {
			return nil, repo.ErrRange
		}
		return nil, mapErr(err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, length+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != length {
		return nil, repo.ErrRange
	}
	return data, nil
}

// Stat implements repo.Backend.
func (b *ReadBackend) Stat(ctx context.Context, kind repo.Kind, oid string) (int64, error) {
	info, err := b.bs.Stat(ctx, b.uuid, string(kind), oid)
	if err != nil {
		return 0, mapErr(err)
	}
	return info.Size, nil
}

// List implements repo.Backend.
func (b *ReadBackend) List(ctx context.Context, kind repo.Kind) ([]repo.ObjectInfo, error) {
	return b.list(ctx, string(kind))
}

// Put implements repo.Backend (refused).
func (b *ReadBackend) Put(context.Context, repo.Kind, string, []byte) error { return ErrReadOnly }

// Delete implements repo.Backend (refused).
func (b *ReadBackend) Delete(context.Context, repo.Kind, string) error { return ErrReadOnly }

// Lock implements repo.Backend (refused).
func (b *ReadBackend) Lock(context.Context, repo.LockKind, time.Duration) (repo.Lock, error) {
	return repo.Lock{}, ErrReadOnly
}

// RefreshLock implements repo.Backend (refused).
func (b *ReadBackend) RefreshLock(context.Context, string, time.Duration) (repo.Lock, error) {
	return repo.Lock{}, ErrReadOnly
}

// Unlock implements repo.Backend (refused).
func (b *ReadBackend) Unlock(context.Context, string) error { return ErrReadOnly }
