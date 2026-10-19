package storage

// Local copies of blobs ("copy rules" with a local target) live in
// <location>/replicas/<user>/<aa>/<bb>/<sha256>, outside blobs/, so orphan
// cleanup, compression and quotas never touch them. When a blob's primary
// file is missing, Open asks the fallback (the copy service) for the content.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
)

// FallbackFunc returns the content of a blob whose primary file is missing,
// or an error (os.ErrNotExist when no copy can supply it).
type FallbackFunc func(locID, userID int64, sha string) (*os.File, error)

// SetFallback installs the fallback used by Open.
func (m *Manager) SetFallback(f FallbackFunc) {
	m.mu.Lock()
	m.fallback = f
	m.mu.Unlock()
}

func (m *Manager) getFallback() FallbackFunc {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.fallback
}

// LocalLocation returns a local storage location by id.
func (m *Manager) LocalLocation(id int64) (path string, err error) {
	l, err := m.loc(id)
	if err != nil {
		return "", err
	}
	if err := LocationHealthy(l.Path); err != nil {
		return "", err
	}
	return l.Path, nil
}

// ReplicaPath is the file of a local copy.
func (m *Manager) ReplicaPath(locID, userID int64, sha string) (string, error) {
	if !ValidSHA256(sha) {
		return "", errors.New("invalid hash")
	}
	root, err := m.LocalLocation(locID)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "replicas", strconv.FormatInt(userID, 10), sha[0:2], sha[2:4], sha), nil
}

// hashingWrite writes r atomically to dst and returns its SHA-256 and size.
func hashingWrite(dst string, r io.Reader) (string, int64, error) {
	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return "", 0, err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := dst + ".tmp-" + hex.EncodeToString(rnd[:])
	h := sha256.New()
	var n int64
	err := writeAtomic(dst, tmp, 0o640, func(w io.Writer) error {
		var err error
		n, err = io.CopyBuffer(io.MultiWriter(w, h), r, make([]byte, 1<<20))
		return err
	})
	if err != nil {
		_ = os.Remove(tmp)
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// WriteReplica stores r as the local copy of a blob on locID and checks it
// against sha; a mismatch removes the copy and returns ErrHashMismatch.
func (m *Manager) WriteReplica(locID, userID int64, sha string, r io.Reader) (int64, error) {
	p, err := m.ReplicaPath(locID, userID, sha)
	if err != nil {
		return 0, err
	}
	got, n, err := hashingWrite(p, r)
	if err != nil {
		return 0, err
	}
	if got != sha {
		_ = os.Remove(p)
		return 0, fmt.Errorf("%w: copy of %s", ErrHashMismatch, sha)
	}
	return n, nil
}

// OpenReplica opens a local copy.
func (m *Manager) OpenReplica(locID, userID int64, sha string) (*os.File, error) {
	p, err := m.ReplicaPath(locID, userID, sha)
	if err != nil {
		return nil, err
	}
	if !regular(p) {
		return nil, os.ErrNotExist
	}
	return os.Open(p)
}

// RemoveReplica deletes a local copy (missing is fine).
func (m *Manager) RemoveReplica(locID, userID int64, sha string) error {
	p, err := m.ReplicaPath(locID, userID, sha)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// HashPrimary hashes the stored primary of a blob (decompressing a
// compressed one). A missing file returns os.ErrNotExist.
func (m *Manager) HashPrimary(locID, userID int64, sha string) (string, error) {
	p, err := m.BlobPath(locID, userID, sha)
	if err != nil {
		return "", err
	}
	if _, _, err := storedPath(p); err != nil {
		return "", err
	}
	f, err := m.openPrimary(locID, userID, sha)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, f, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// RestorePrimary replaces a blob's primary file with the content of r after
// checking it against sha (repair from a copy). Compressed variants and
// extracted copies of the old file are removed.
func (m *Manager) RestorePrimary(locID, userID int64, sha string, r io.Reader) error {
	p, err := m.BlobPath(locID, userID, sha)
	if err != nil {
		return err
	}
	l, err := m.loc(locID)
	if err != nil {
		return err
	}
	if err := LocationHealthy(l.Path); err != nil {
		return err
	}
	unlock := m.lock(userID, sha)
	defer unlock()
	staging := filepath.Join(l.Path, "tmp", "restore-"+strconv.FormatInt(userID, 10)+"-"+sha)
	got, _, err := hashingWrite(staging, r)
	if err != nil {
		return err
	}
	if got != sha {
		_ = os.Remove(staging)
		return fmt.Errorf("%w: restored content of %s", ErrHashMismatch, sha)
	}
	m.removeVariants(locID, userID, sha, p)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		_ = os.Remove(staging)
		return err
	}
	if err := os.Rename(staging, p); err != nil {
		_ = os.Remove(staging)
		return err
	}
	syncDir(filepath.Dir(p))
	return nil
}
