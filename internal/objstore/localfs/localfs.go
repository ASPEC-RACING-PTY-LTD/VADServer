// Package localfs is the objstore backend for a local or host-mounted
// (SMB/NFS) directory. Each key maps to root/<key> with '/' replaced by the
// OS separator; parent directories are created on demand.
//
// Writes go to a dot-prefixed temp file in the target directory, are hashed
// while copied, fsynced, then renamed into place, and the directory is
// fsynced where the platform supports it. Every path component below the
// root is checked with Lstat, so a symlink or junction planted inside the
// root is refused rather than followed. The root itself is resolved once in
// New. A root must be used by a single server process: Put's no-overwrite
// check is serialised in-process only.
package localfs

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
)

const (
	tempPrefix = ".tmp-"
	dirPerm    = 0o700
	filePerm   = 0o600
)

// Store is a localfs objstore.Store.
type Store struct {
	root  string
	locks [64]sync.Mutex
}

var _ objstore.Store = (*Store)(nil)

// New opens a store rooted at the existing absolute directory root. Symlinks
// and junctions in root itself are resolved here.
func New(root string) (*Store, error) {
	resolved, err := storage.ResolveLocationPath(root, nil)
	if err != nil {
		return nil, fmt.Errorf("objstore root: %w", err)
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("objstore root: %w", err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("objstore root %s is not a directory", root)
	}
	return &Store{root: resolved}, nil
}

// Root returns the resolved root directory.
func (s *Store) Root() string { return s.root }

// plain reports whether fi (from Lstat) is not a symlink, junction or other
// reparse point.
func plain(fi fs.FileInfo) bool {
	return fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
}

// path maps a validated key to its file path, checking every existing
// parent directory below the root with Lstat. With create, missing parents
// are created.
func (s *Store) path(key string, create bool) (string, error) {
	if err := objstore.ValidateKey(key); err != nil {
		return "", err
	}
	segs := strings.Split(key, "/")
	dir := s.root
	for _, seg := range segs[:len(segs)-1] {
		dir = filepath.Join(dir, seg)
		fi, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			if !create {
				return "", fmt.Errorf("%w: %s", objstore.ErrNotFound, key)
			}
			if err := os.Mkdir(dir, dirPerm); err != nil && !errors.Is(err, fs.ErrExist) {
				return "", err
			}
			syncDir(filepath.Dir(dir))
			fi, err = os.Lstat(dir)
		}
		if err != nil {
			return "", err
		}
		if !plain(fi) || (!fi.IsDir() && !fi.Mode().IsRegular()) {
			return "", fmt.Errorf("%w: %s", objstore.ErrUnsafePath, key)
		}
		if !fi.IsDir() {
			// A parent is itself an object: nothing can live below it.
			if create {
				return "", fmt.Errorf("%w: a parent of %s is an object", objstore.ErrExists, key)
			}
			return "", fmt.Errorf("%w: %s", objstore.ErrNotFound, key)
		}
	}
	return filepath.Join(dir, segs[len(segs)-1]), nil
}

// statFile Lstats the object file p, mapping a missing file to ErrNotFound
// and anything but a regular file to ErrUnsafePath.
func statFile(key, p string) (fs.FileInfo, error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%w: %s", objstore.ErrNotFound, key)
	}
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", objstore.ErrUnsafePath, key)
	}
	return fi, nil
}

func (s *Store) lock(key string) func() {
	h := fnv.New32a()
	h.Write([]byte(key))
	m := &s.locks[h.Sum32()%uint32(len(s.locks))]
	m.Lock()
	return m.Unlock
}

// Put implements objstore.Store.
func (s *Store) Put(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := objstore.ValidateKey(key); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: negative size", objstore.ErrSizeMismatch)
	}
	if !objstore.ValidSHA256(sha256hex) {
		return fmt.Errorf("%w: expected hash is not lower-case hex SHA-256", objstore.ErrHashMismatch)
	}
	dst, err := s.path(key, true)
	if err != nil {
		return err
	}
	tmp, err := s.writeTemp(ctx, filepath.Dir(dst), r, size, sha256hex)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // no-op once renamed

	unlock := s.lock(key)
	defer unlock()
	fi, err := os.Lstat(dst)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", objstore.ErrUnsafePath, key)
		}
		if fi.Size() == size {
			if got, herr := hashFile(dst); herr == nil && got == sha256hex {
				return nil
			}
		}
		return fmt.Errorf("%w: %s", objstore.ErrExists, key)
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

// writeTemp copies exactly size bytes of r into a new temp file in dir,
// verifies size and hash, and syncs it. It returns the temp path; on error
// nothing is left behind.
func (s *Store) writeTemp(ctx context.Context, dir string, r io.Reader, size int64, want string) (string, error) {
	var rnd [12]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return "", err
	}
	tmp := filepath.Join(dir, tempPrefix+hex.EncodeToString(rnd[:]))
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, filePerm)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	src := &ctxReader{ctx: ctx, r: r}
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(src, size))
	if err == nil && n != size {
		err = fmt.Errorf("%w: got %d bytes, want %d", objstore.ErrSizeMismatch, n, size)
	}
	if err == nil {
		var extra [1]byte
		if m, _ := io.ReadFull(src, extra[:]); m > 0 {
			err = fmt.Errorf("%w: more than %d bytes", objstore.ErrSizeMismatch, size)
		} else if cerr := ctx.Err(); cerr != nil {
			err = cerr
		}
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != want {
		err = objstore.ErrHashMismatch
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return tmp, nil
}

// Get implements objstore.Store.
func (s *Store) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	f, fi, err := s.open(ctx, key)
	if err != nil {
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// GetRange implements objstore.Store.
func (s *Store) GetRange(ctx context.Context, key string, off, length int64) (io.ReadCloser, error) {
	f, fi, err := s.open(ctx, key)
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if off < 0 || off > size {
		f.Close()
		return nil, fmt.Errorf("%w: offset %d of %d", objstore.ErrInvalidRange, off, size)
	}
	if length < 0 || length > size-off {
		length = size - off
	}
	return &sectionReadCloser{SectionReader: io.NewSectionReader(f, off, length), f: f}, nil
}

func (s *Store) open(ctx context.Context, key string) (*os.File, fs.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	p, err := s.path(key, false)
	if err != nil {
		return nil, nil, err
	}
	if _, err := statFile(key, p); err != nil {
		return nil, nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("%w: %s", objstore.ErrNotFound, key)
	}
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%w: %s", objstore.ErrUnsafePath, key)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// Stat implements objstore.Store.
func (s *Store) Stat(ctx context.Context, key string) (objstore.ObjectInfo, error) {
	if err := ctx.Err(); err != nil {
		return objstore.ObjectInfo{}, err
	}
	p, err := s.path(key, false)
	if err != nil {
		return objstore.ObjectInfo{}, err
	}
	fi, err := statFile(key, p)
	if err != nil {
		return objstore.ObjectInfo{}, err
	}
	return objstore.ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime()}, nil
}

// Delete implements objstore.Store.
func (s *Store) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p, err := s.path(key, false)
	if errors.Is(err, objstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	unlock := s.lock(key)
	defer unlock()
	if _, err := statFile(key, p); err != nil {
		if errors.Is(err, objstore.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	syncDir(filepath.Dir(p))
	return nil
}

// List implements objstore.Store. It walks the deepest directory implied by
// prefix without following links, skips temp files and invalid names, then
// sorts and pages the keys. Cost is proportional to the subtree, so callers
// should list by repository-level prefixes.
func (s *Store) List(ctx context.Context, prefix, cursor string, limit int) (objstore.ListPage, error) {
	if err := ctx.Err(); err != nil {
		return objstore.ListPage{}, err
	}
	if err := objstore.ValidatePrefix(prefix); err != nil {
		return objstore.ListPage{}, err
	}
	if limit <= 0 {
		limit = objstore.DefaultListLimit
	}
	limit = min(limit, objstore.MaxListLimit)

	base := s.root
	if i := strings.LastIndexByte(prefix, '/'); i >= 0 {
		for _, seg := range strings.Split(prefix[:i], "/") {
			base = filepath.Join(base, seg)
			fi, err := os.Lstat(base)
			if errors.Is(err, fs.ErrNotExist) {
				return objstore.ListPage{}, nil
			}
			if err != nil {
				return objstore.ListPage{}, err
			}
			if !plain(fi) || !fi.IsDir() {
				return objstore.ListPage{}, fmt.Errorf("%w: %s", objstore.ErrUnsafePath, prefix)
			}
		}
	}

	var objs []objstore.ObjectInfo
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == base && errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		if p == base {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") || d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(s.root, p)
		if err != nil {
			return err
		}
		key := filepath.ToSlash(rel)
		if d.IsDir() {
			// Prune directories that cannot hold matching keys.
			if !strings.HasPrefix(key+"/", prefix) && !strings.HasPrefix(prefix, key+"/") {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || !strings.HasPrefix(key, prefix) || key <= cursor || objstore.ValidateKey(key) != nil {
			return nil
		}
		fi, err := d.Info()
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		objs = append(objs, objstore.ObjectInfo{Key: key, Size: fi.Size(), ModTime: fi.ModTime()})
		return nil
	})
	if err != nil {
		return objstore.ListPage{}, err
	}
	sort.Slice(objs, func(i, j int) bool { return objs[i].Key < objs[j].Key })
	page := objstore.ListPage{Objects: objs}
	if len(objs) > limit {
		page.Objects = objs[:limit:limit]
		page.NextCursor = objs[limit-1].Key
	}
	return page, nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// syncDir fsyncs a directory so a rename or create is durable. Platforms
// that cannot sync directories (Windows) ignore the error.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c *ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}

type sectionReadCloser struct {
	*io.SectionReader
	f *os.File
}

func (s *sectionReadCloser) Close() error { return s.f.Close() }
