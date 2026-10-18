// Package backup holds the server side of backup repositories: the object
// layout of backup-format-v1 §2 on top of an objstore backend. The built-in
// "server" destination (api-v1 §14 rev 1.1b) is a localfs store rooted at
// <default storage location>/backup.
//
// Owner: SRV-2 (P1-14).
package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore/localfs"
)

// DirName is the directory below a storage location that holds the
// built-in server destination.
const DirName = "backup"

// ErrNotReplaceable is returned by PutConfig when the backend cannot replace
// objects atomically.
var ErrNotReplaceable = errors.New("object store cannot replace objects")

var (
	uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	kinds       = map[string]bool{
		wire.ObjectKindConfig: true, wire.ObjectKindKeys: true, wire.ObjectKindChain: true, wire.ObjectKindIndex: true,
		wire.ObjectKindSnapshots: true, wire.ObjectKindData: true, wire.ObjectKindLocks: true,
	}
)

// ValidRepoUUID reports whether s is a lowercase canonical UUIDv4
// (backup-format-v1 §2).
func ValidRepoUUID(s string) bool { return uuidPattern.MatchString(s) }

// ValidKind reports whether kind is a repository object kind (api-v1 §5).
func ValidKind(kind string) bool { return kinds[kind] }

// ValidOID reports whether oid is valid for kind: the fixed all-zero oid for
// config, otherwise 64 lowercase hex characters.
func ValidOID(kind, oid string) bool {
	if kind == wire.ObjectKindConfig {
		return oid == wire.ObjectConfigOID
	}
	return objstore.ValidSHA256(oid)
}

// ObjectKey maps a repository object to its store key (backup-format-v1 §2):
// repo/<uuid>/config, repo/<uuid>/data/<oid[0:2]>/<oid> and
// repo/<uuid>/<kind>/<oid> for the other kinds.
func ObjectKey(repoUUID, kind, oid string) (string, error) {
	if !ValidRepoUUID(repoUUID) {
		return "", fmt.Errorf("%w: bad repo uuid", objstore.ErrInvalidKey)
	}
	if !ValidKind(kind) || !ValidOID(kind, oid) {
		return "", fmt.Errorf("%w: bad kind or oid", objstore.ErrInvalidKey)
	}
	switch kind {
	case wire.ObjectKindConfig:
		return "repo/" + repoUUID + "/config", nil
	case wire.ObjectKindData:
		return "repo/" + repoUUID + "/data/" + oid[:2] + "/" + oid, nil
	}
	return "repo/" + repoUUID + "/" + kind + "/" + oid, nil
}

// Store is the object layout of backup repositories over one backend.
type Store struct {
	os objstore.Store
}

// New wraps an objstore backend.
func New(s objstore.Store) *Store { return &Store{os: s} }

// Backend returns the underlying object store.
func (s *Store) Backend() objstore.Store { return s.os }

var (
	localMu sync.Mutex
	locals  = map[string]*Store{}
)

// OpenLocal returns the localfs-backed Store rooted at root, creating the
// directory if needed. Stores are cached per root for the life of the
// process, because localfs serialises writers in-process only.
func OpenLocal(root string) (*Store, error) {
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("backup root %q is not absolute", root)
	}
	root = filepath.Clean(root)
	localMu.Lock()
	defer localMu.Unlock()
	if s, ok := locals[root]; ok {
		return s, nil
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("backup root: %w", err)
	}
	ls, err := localfs.New(root)
	if err != nil {
		return nil, err
	}
	s := New(ls)
	locals[root] = s
	return s, nil
}

// Put stores an immutable object whose content must hash to oid. Identical
// content already stored succeeds. It returns objstore.ErrHashMismatch,
// ErrSizeMismatch or ErrExists like objstore.Store.Put. config is refused;
// use PutConfig.
func (s *Store) Put(ctx context.Context, repoUUID, kind, oid string, r io.Reader, size int64) error {
	if kind == wire.ObjectKindConfig {
		return fmt.Errorf("%w: config is written with PutConfig", objstore.ErrInvalidKey)
	}
	key, err := ObjectKey(repoUUID, kind, oid)
	if err != nil {
		return err
	}
	return s.os.Put(ctx, key, r, size, oid)
}

// PutConfig writes the repository config, whose content hashes to
// sha256hex, replacing any previous config atomically.
func (s *Store) PutConfig(ctx context.Context, repoUUID string, r io.Reader, size int64, sha256hex string) error {
	key, err := ObjectKey(repoUUID, wire.ObjectKindConfig, wire.ObjectConfigOID)
	if err != nil {
		return err
	}
	rp, ok := s.os.(objstore.Replacer)
	if !ok {
		return ErrNotReplaceable
	}
	return rp.Replace(ctx, key, r, size, sha256hex)
}

// Get opens a whole object and returns its size.
func (s *Store) Get(ctx context.Context, repoUUID, kind, oid string) (io.ReadCloser, int64, error) {
	key, err := ObjectKey(repoUUID, kind, oid)
	if err != nil {
		return nil, 0, err
	}
	return s.os.Get(ctx, key)
}

// GetRange opens length bytes of an object starting at off (see
// objstore.Store.GetRange).
func (s *Store) GetRange(ctx context.Context, repoUUID, kind, oid string, off, length int64) (io.ReadCloser, error) {
	key, err := ObjectKey(repoUUID, kind, oid)
	if err != nil {
		return nil, err
	}
	return s.os.GetRange(ctx, key, off, length)
}

// Stat returns an object's metadata or objstore.ErrNotFound.
func (s *Store) Stat(ctx context.Context, repoUUID, kind, oid string) (objstore.ObjectInfo, error) {
	key, err := ObjectKey(repoUUID, kind, oid)
	if err != nil {
		return objstore.ObjectInfo{}, err
	}
	return s.os.Stat(ctx, key)
}

// ConfigSHA256 returns the hex SHA-256 of the stored config, or ok false
// when the repository has no config yet.
func (s *Store) ConfigSHA256(ctx context.Context, repoUUID string) (sum string, ok bool, err error) {
	rc, _, err := s.Get(ctx, repoUUID, wire.ObjectKindConfig, wire.ObjectConfigOID)
	if errors.Is(err, objstore.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	defer rc.Close()
	h := sha256.New()
	if _, err := io.Copy(h, rc); err != nil {
		return "", false, err
	}
	return hex.EncodeToString(h.Sum(nil)), true, nil
}
