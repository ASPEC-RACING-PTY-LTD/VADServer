package storage

// At-rest compression of stored blobs. A compressed blob is kept as
// "<sha256>.zst" (one zstd frame) in place of the raw file. Readers never see
// the difference: Open extracts it once into <location>/cache/extract and
// serves that copy; SweepExtracted deletes copies nobody has opened for a
// while, so a file that is being worked with is not decompressed again and
// again. Blobs that do not shrink by at least 3 % get a "<sha256>.nozst"
// marker so later runs skip them.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	zstSuffix   = ".zst"
	noZstSuffix = ".nozst"
	// minCompressSize: smaller blobs are not worth a frame and a marker.
	minCompressSize = 4 << 10
)

// ExtractIdle is how long an extracted copy of a compressed blob is kept
// after it was last opened.
const ExtractIdle = 10 * time.Minute

// CompressStats summarises one compression run.
type CompressStats struct {
	Scanned     int64 `json:"scanned"`
	Compressed  int64 `json:"compressed"`
	Skipped     int64 `json:"skipped"`
	Failed      int64 `json:"failed"`
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
	StartedAt   int64 `json:"started_at"`
	FinishedAt  int64 `json:"finished_at"`
}

// regular reports whether p is a regular file (not a link or reparse point).
func regular(p string) bool {
	fi, err := os.Lstat(p)
	return err == nil && fi.Mode().IsRegular()
}

// storedPath returns the file holding blob p: the raw file, else its
// compressed form (zipped). Missing blobs return the raw path's error.
func storedPath(p string) (path string, zipped bool, err error) {
	fi, err := os.Lstat(p)
	if err == nil {
		if !fi.Mode().IsRegular() {
			return "", false, fmt.Errorf("%w: %s", ErrUnsafePath, p)
		}
		return p, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", false, err
	}
	if regular(p + zstSuffix) {
		return p + zstSuffix, true, nil
	}
	return "", false, err
}

// BlobStored reports whether the blob exists on disk, raw or compressed.
func (m *Manager) BlobStored(locID, userID int64, sha string) (bool, error) {
	p, err := m.BlobPath(locID, userID, sha)
	if err != nil {
		return false, err
	}
	_, _, err = storedPath(p)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func extractDir(l store.Location) string { return filepath.Join(l.Path, "cache", "extract") }

func extractName(userID int64, sha string) string { return strconv.FormatInt(userID, 10) + "-" + sha }

// openExtracted serves a compressed blob from its extracted copy, creating
// it (verified against sha) when missing, and refreshes its last-use time.
func (m *Manager) openExtracted(locID, userID int64, sha, zp string) (*os.File, error) {
	l, err := m.loc(locID)
	if err != nil {
		return nil, err
	}
	unlock := m.lock(userID, sha)
	defer unlock()
	dir := extractDir(l)
	cp := filepath.Join(dir, extractName(userID, sha))
	if regular(cp) {
		now := time.Now()
		_ = os.Chtimes(cp, now, now)
		return os.Open(cp)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	tmp := cp + ".tmp-" + hex.EncodeToString(rnd[:])
	err = func() error {
		in, err := os.Open(zp)
		if err != nil {
			return err
		}
		defer in.Close()
		zr, err := compress.NewFrameReader(in)
		if err != nil {
			return err
		}
		defer zr.Close()
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
		if err != nil {
			return err
		}
		h := sha256.New()
		if _, err := io.CopyBuffer(io.MultiWriter(out, h), zr, make([]byte, 1<<20)); err != nil {
			out.Close()
			return fmt.Errorf("extract %s: %w", zp, err)
		}
		if err := out.Close(); err != nil {
			return err
		}
		if hex.EncodeToString(h.Sum(nil)) != sha {
			return fmt.Errorf("%w: compressed blob %s", ErrHashMismatch, zp)
		}
		return os.Rename(tmp, cp)
	}()
	if err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return os.Open(cp)
}

// SweepExtracted deletes extracted copies not opened for idle (and stale
// temp files). A copy still open elsewhere may fail to delete on Windows; it
// is retried on the next sweep.
func (m *Manager) SweepExtracted(idle time.Duration) int {
	m.mu.RLock()
	locs := make([]store.Location, 0, len(m.locs))
	for _, l := range m.locs {
		locs = append(locs, l)
	}
	m.mu.RUnlock()
	cutoff := time.Now().Add(-idle)
	removed := 0
	for _, l := range locs {
		entries, err := os.ReadDir(extractDir(l))
		if err != nil {
			continue
		}
		for _, e := range entries {
			info, err := e.Info()
			if err != nil || !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
				continue
			}
			if os.Remove(filepath.Join(extractDir(l), e.Name())) == nil {
				removed++
			}
		}
	}
	return removed
}

// RunExtractSweeper sweeps extracted copies every interval until ctx ends.
func (m *Manager) RunExtractSweeper(ctx context.Context, interval, idle time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := m.SweepExtracted(idle); n > 0 {
				slog.Debug("removed idle extracted blobs", "count", n)
			}
		}
	}
}

// CompressStored compresses the raw blobs of every healthy location that
// shrink by at least 3 %. It is incremental: compressed blobs and those
// marked incompressible are skipped. progress (optional) is called after
// each blob.
func (m *Manager) CompressStored(ctx context.Context, progress func(CompressStats)) (CompressStats, error) {
	st := CompressStats{StartedAt: time.Now().UnixMilli()}
	if err := m.Reload(ctx); err != nil {
		return st, err
	}
	m.mu.RLock()
	locs := make([]store.Location, 0, len(m.locs))
	for _, l := range m.locs {
		locs = append(locs, l)
	}
	m.mu.RUnlock()
	for _, l := range locs {
		if LocationHealthy(l.Path) != nil {
			continue
		}
		blobRoot := filepath.Join(l.Path, "blobs")
		err := filepath.WalkDir(blobRoot, func(p string, d os.DirEntry, err error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil || d.IsDir() || !d.Type().IsRegular() || !ValidSHA256(d.Name()) {
				return nil
			}
			rel, _ := filepath.Rel(blobRoot, p)
			parts := splitPath(rel)
			if len(parts) != 4 {
				return nil
			}
			uid, err := strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return nil
			}
			st.Scanned++
			before, after, done, err := m.compressBlob(uid, d.Name(), p)
			switch {
			case err != nil:
				st.Failed++
				slog.Warn("blob compression failed", "path", p, "err", err)
			case done:
				st.Compressed++
				st.BytesBefore += before
				st.BytesAfter += after
			default:
				st.Skipped++
			}
			if progress != nil {
				progress(st)
			}
			return nil
		})
		if err != nil && ctx.Err() != nil {
			st.FinishedAt = time.Now().UnixMilli()
			return st, ctx.Err()
		}
	}
	st.FinishedAt = time.Now().UnixMilli()
	return st, nil
}

// compressBlob replaces raw blob p by p.zst when that saves at least 3 %.
func (m *Manager) compressBlob(userID int64, sha, p string) (before, after int64, done bool, err error) {
	unlock := m.lock(userID, sha)
	defer unlock()
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return 0, 0, false, nil // removed meanwhile
	}
	if fi.Size() < minCompressSize || regular(p+noZstSuffix) {
		return 0, 0, false, nil
	}
	tmp := p + zstSuffix + ".tmp"
	n, err := func() (int64, error) {
		in, err := os.Open(p)
		if err != nil {
			return 0, err
		}
		defer in.Close()
		out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
		if err != nil {
			return 0, err
		}
		cw := &countWriter{w: out}
		zw, err := compress.NewFrameWriter(cw)
		if err != nil {
			out.Close()
			return 0, err
		}
		if _, err := io.CopyBuffer(zw, in, make([]byte, 1<<20)); err != nil {
			zw.Close()
			out.Close()
			return 0, err
		}
		if err := zw.Close(); err != nil {
			out.Close()
			return 0, err
		}
		if err := out.Sync(); err != nil {
			out.Close()
			return 0, err
		}
		return cw.n, out.Close()
	}()
	if err != nil {
		_ = os.Remove(tmp)
		return 0, 0, false, err
	}
	if !compress.Worth(int(min(n, 1<<62)), int(min(fi.Size(), 1<<62))) {
		_ = os.Remove(tmp)
		_ = os.WriteFile(p+noZstSuffix, nil, 0o640)
		return 0, 0, false, nil
	}
	if err := os.Rename(tmp, p+zstSuffix); err != nil {
		_ = os.Remove(tmp)
		return 0, 0, false, err
	}
	syncDir(filepath.Dir(p))
	if err := os.Remove(p); err != nil {
		// Keep a consistent state: the raw file wins on read, so drop the frame.
		_ = os.Remove(p + zstSuffix)
		return 0, 0, false, err
	}
	return fi.Size(), n, true, nil
}

// removeVariants deletes the compressed form, the marker and any extracted
// copy of a blob (with its raw file already handled by the caller).
func (m *Manager) removeVariants(locID, userID int64, sha, p string) {
	for _, q := range []string{p + zstSuffix, p + noZstSuffix} {
		if err := os.Remove(q); err != nil && !os.IsNotExist(err) {
			slog.Warn("failed to delete blob file", "path", q, "err", err)
		}
	}
	if l, err := m.loc(locID); err == nil {
		_ = os.Remove(filepath.Join(extractDir(l), extractName(userID, sha)))
	}
}

// blobBaseName strips the on-disk variant suffixes of a blob file name.
func blobBaseName(name string) string {
	for _, s := range []string{zstSuffix, noZstSuffix} {
		if b, ok := strings.CutSuffix(name, s); ok {
			return b
		}
	}
	return name
}

type countWriter struct {
	w io.Writer
	n int64
}

func (c *countWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
