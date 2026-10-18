package repo

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// DirBackend stores a repository in a local directory with the §2 layout
// (the directory is repo/<uuid>): config, keys/<oid>, chain/<oid>,
// index/<oid>, snapshots/<oid>, data/<oid[0:2]>/<oid>, locks/<oid>. It is
// for tests and the standalone restore tool; locks are process-local.
type DirBackend struct {
	LockTable
	root string
}

// NewDirBackend opens (and creates) root.
func NewDirBackend(root string) (*DirBackend, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &DirBackend{root: root}, nil
}

// Root returns the repository directory.
func (d *DirBackend) Root() string { return d.root }

func (d *DirBackend) path(kind Kind, oid string) (string, error) {
	if err := CheckKey(kind, oid); err != nil {
		return "", err
	}
	switch kind {
	case KindConfig:
		return filepath.Join(d.root, "config"), nil
	case KindData:
		return filepath.Join(d.root, "data", oid[:2], oid), nil
	default:
		return filepath.Join(d.root, string(kind), oid), nil
	}
}

func notFound(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrNotFound
	}
	return err
}

// Put implements Backend. Objects are written to a temporary file and
// renamed into place.
func (d *DirBackend) Put(_ context.Context, kind Kind, oid string, data []byte) error {
	if err := CheckObject(kind, oid, data); err != nil {
		return err
	}
	p, _ := d.path(kind, oid)
	dir := filepath.Dir(p)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Rename(tmp, p)
	}
	if werr != nil {
		os.Remove(tmp)
	}
	return werr
}

// Get implements Backend.
func (d *DirBackend) Get(_ context.Context, kind Kind, oid string) ([]byte, error) {
	p, err := d.path(kind, oid)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	return b, notFound(err)
}

// GetRange implements Backend.
func (d *DirBackend) GetRange(_ context.Context, kind Kind, oid string, offset, length int64) ([]byte, error) {
	p, err := d.path(kind, oid)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, notFound(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if offset < 0 || length < 0 || offset > st.Size() || length > st.Size()-offset {
		return nil, ErrRange
	}
	b := make([]byte, length)
	if _, err := f.ReadAt(b, offset); err != nil && !(errors.Is(err, io.EOF) && length == 0) {
		return nil, err
	}
	return b, nil
}

// Stat implements Backend.
func (d *DirBackend) Stat(_ context.Context, kind Kind, oid string) (int64, error) {
	p, err := d.path(kind, oid)
	if err != nil {
		return 0, err
	}
	st, err := os.Stat(p)
	if err != nil {
		return 0, notFound(err)
	}
	return st.Size(), nil
}

// List implements Backend. Temporary and foreign files are skipped.
func (d *DirBackend) List(_ context.Context, kind Kind) ([]ObjectInfo, error) {
	if !kind.Valid() {
		return nil, ErrInvalidObject
	}
	if kind == KindConfig {
		st, err := os.Stat(filepath.Join(d.root, "config"))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return []ObjectInfo{}, nil
			}
			return nil, err
		}
		return []ObjectInfo{{OID: ConfigOID, Size: st.Size()}}, nil
	}
	base := filepath.Join(d.root, string(kind))
	out := []ObjectInfo{}
	err := filepath.WalkDir(base, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && p == base {
				return filepath.SkipDir
			}
			return err
		}
		if e.IsDir() || !IsOID(e.Name()) {
			return nil
		}
		if want, _ := d.path(kind, e.Name()); filepath.Clean(want) != filepath.Clean(p) {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		out = append(out, ObjectInfo{OID: e.Name(), Size: info.Size()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// Delete implements Backend.
func (d *DirBackend) Delete(_ context.Context, kind Kind, oid string) error {
	p, err := d.path(kind, oid)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
