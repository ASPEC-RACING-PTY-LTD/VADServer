// Package storage manages blob files on configured storage locations.
//
// Layout of a location directory:
//
//	<location>/.vaultdock-location   marker file
//	<location>/blobs/<user>/<aa>/<bb>/<sha256>   content-addressed blobs
//	<location>/tmp/<upload-id>.part              in-progress uploads
//
// Blob paths are derived only from numeric user ids and validated hex
// hashes, so client-supplied names never reach the filesystem.
package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

var (
	ErrInsufficientStorage = errors.New("not enough free space on the storage device")
	ErrLocationUnavailable = errors.New("storage location is unavailable")
	ErrHashMismatch        = errors.New("content hash does not match")
	ErrOutsideRoots        = errors.New("path is outside the allowed storage roots")
	ErrUnsafePath          = errors.New("path is not a plain file or directory")
)

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var uploadIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// ValidSHA256 reports whether s is a lower-case hex SHA-256.
func ValidSHA256(s string) bool { return shaPattern.MatchString(s) }

// ValidUploadID reports whether s is a well-formed upload id.
func ValidUploadID(s string) bool { return uploadIDPattern.MatchString(s) }

const markerFile = ".vaultdock-location"

// copySuffix marks a partially copied blob (see copyFile); CleanOrphans
// removes stale ones.
const copySuffix = ".copying"

// Manager handles blob placement, reads and deletion.
type Manager struct {
	st    *store.Store
	locks [256]sync.Mutex
	mu    sync.RWMutex
	locs  map[int64]store.Location
	// GlobalMinFree is a hard floor of free bytes kept on every device.
	// Assign it directly only before the Manager is shared; afterwards use
	// SetGlobalMinFree (CheckSpace reads it under mu).
	GlobalMinFree int64
	// fallback supplies blobs whose primary file is missing (replicas.go).
	fallback FallbackFunc
}

func New(st *store.Store) *Manager {
	return &Manager{st: st, locs: map[int64]store.Location{}, GlobalMinFree: 1 << 30}
}

// SetGlobalMinFree updates the global free-space floor. It is safe to call
// while uploads run. Negative values are treated as zero.
func (m *Manager) SetGlobalMinFree(v int64) {
	m.mu.Lock()
	m.GlobalMinFree = max(v, 0)
	m.mu.Unlock()
}

func (m *Manager) globalMinFree() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.GlobalMinFree
}

// Reload refreshes the location cache from the database.
func (m *Manager) Reload(ctx context.Context) error {
	list, err := m.st.ListLocations(ctx)
	if err != nil {
		return err
	}
	locs := map[int64]store.Location{}
	for _, l := range list {
		locs[l.ID] = l
	}
	m.mu.Lock()
	m.locs = locs
	m.mu.Unlock()
	return nil
}

// Location returns a cached location.
func (m *Manager) Location(id int64) (store.Location, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	l, ok := m.locs[id]
	return l, ok
}

// PrepareLocation creates the directory layout and verifies the path is a
// writable directory whose blobs and tmp subdirectories are real
// directories (not symlinks or junctions) and whose free space is readable.
func PrepareLocation(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("storage path must be absolute")
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("storage path %s does not exist; create or mount it first", path)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("storage path %s is not a directory", path)
	}
	for _, d := range []string{"blobs", "tmp"} {
		sub := filepath.Join(path, d)
		if err := os.MkdirAll(sub, 0o750); err != nil {
			return fmt.Errorf("cannot create %s in %s: %w", d, path, err)
		}
		// A symlinked or junctioned blobs/tmp directory would let blob
		// writes escape the location.
		fi, err := os.Lstat(sub)
		if err != nil {
			return err
		}
		if !plainDir(fi) {
			return fmt.Errorf("%w: %s", ErrUnsafePath, sub)
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm()&0o027 != 0 {
			if err := os.Chmod(sub, 0o750); err != nil {
				return fmt.Errorf("cannot restrict permissions of %s: %w", sub, err)
			}
		}
	}
	probe := filepath.Join(path, "tmp", ".write-test-"+strconv.FormatInt(time.Now().UnixNano(), 36))
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("storage path %s is not writable: %w", path, err)
	}
	_ = os.Remove(probe)
	if _, err := DiskSpace(path); err != nil {
		return fmt.Errorf("cannot read free space of %s: %w", path, err)
	}
	marker := filepath.Join(path, markerFile)
	fi, err := os.Lstat(marker)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := writeFileAtomic(marker, []byte("VaultDock storage location. Do not delete files in this directory manually.\n"), 0o640); err != nil {
			return fmt.Errorf("cannot write location marker in %s: %w", path, err)
		}
	case err != nil:
		return err
	case !fi.Mode().IsRegular():
		return fmt.Errorf("%w: %s", ErrUnsafePath, marker)
	}
	return nil
}

// plainDir reports whether fi (from Lstat) is a real directory rather than a
// symlink, junction or other reparse point.
func plainDir(fi os.FileInfo) bool {
	return fi.IsDir() && fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0
}

// Within reports whether p is root or lies inside it, comparing cleaned
// paths lexically (case-insensitively on Windows, via filepath.Rel).
func Within(root, p string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(p))
	if err != nil || filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// realPath resolves symlinks and junctions in p. When p cannot be resolved
// (for example an unmounted volume) the cleaned path is returned.
func realPath(p string) string {
	if r, err := resolvePath(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}

// resolvePath resolves symlinks and, unlike filepath.EvalSymlinks on
// Windows, junctions (mount-point reparse points) in the existing path p.
// A reparse point that cannot be read as a link is refused.
func resolvePath(p string) (string, error) {
	cur := filepath.Clean(p)
	for range 40 {
		r, err := filepath.EvalSymlinks(cur)
		if err != nil {
			return "", err
		}
		next, changed, err := expandLink(r)
		if err != nil {
			return "", err
		}
		if !changed {
			return r, nil
		}
		cur = next
	}
	return "", fmt.Errorf("%w: too many links in %s", ErrUnsafePath, p)
}

// expandLink replaces the first link component left in p by its target.
func expandLink(p string) (string, bool, error) {
	sep := string(filepath.Separator)
	vol := filepath.VolumeName(p)
	rest := strings.Trim(p[len(vol):], sep)
	if rest == "" {
		return p, false, nil
	}
	parts := strings.Split(rest, sep)
	prefix := vol + sep
	for i, part := range parts {
		prefix = filepath.Join(prefix, part)
		fi, err := os.Lstat(prefix)
		if err != nil {
			return "", false, err
		}
		if fi.Mode()&(os.ModeSymlink|os.ModeIrregular) == 0 {
			continue
		}
		tgt, err := os.Readlink(prefix)
		if err != nil {
			return "", false, fmt.Errorf("%w: %s", ErrUnsafePath, prefix)
		}
		if !filepath.IsAbs(tgt) {
			tgt = filepath.Join(filepath.Dir(prefix), tgt)
		}
		return filepath.Join(append([]string{tgt}, parts[i+1:]...)...), true, nil
	}
	return p, false, nil
}

// ResolveLocationPath resolves symlinks and junctions in the existing
// directory p and checks that the result still lies inside one of roots
// (themselves resolved). An empty roots list allows any absolute path. It
// returns the resolved path.
func ResolveLocationPath(p string, roots []string) (string, error) {
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("storage path must be absolute")
	}
	resolved, err := resolvePath(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("storage path %s does not exist; create or mount it first", p)
		}
		return "", err
	}
	if len(roots) == 0 {
		return resolved, nil
	}
	for _, r := range roots {
		if Within(realPath(r), resolved) {
			return resolved, nil
		}
	}
	return "", ErrOutsideRoots
}

// Overlaps reports whether a and b (resolved where possible) are the same
// directory or one contains the other.
func Overlaps(a, b string) bool {
	ra, rb := realPath(a), realPath(b)
	return Within(ra, rb) || Within(rb, ra)
}

// LocationHealthy checks that the location directory is still mounted
// (marker present), so a missing mount is never mistaken for empty storage.
func LocationHealthy(path string) error {
	if _, err := os.Stat(filepath.Join(path, markerFile)); err != nil {
		return fmt.Errorf("%w: %s (marker missing; is the volume mounted?)", ErrLocationUnavailable, path)
	}
	return nil
}

func (m *Manager) lock(userID int64, sha string) func() {
	h := fnv.New32a()
	h.Write([]byte(sha))
	h.Write([]byte(strconv.FormatInt(userID, 10)))
	mu := &m.locks[h.Sum32()%256]
	mu.Lock()
	return mu.Unlock
}

func (m *Manager) loc(id int64) (store.Location, error) {
	l, ok := m.Location(id)
	if !ok {
		if err := m.Reload(context.Background()); err == nil {
			l, ok = m.Location(id)
		}
	}
	if !ok {
		return l, ErrLocationUnavailable
	}
	return l, nil
}

// BlobPath returns the on-disk path for a blob.
func (m *Manager) BlobPath(locID, userID int64, sha string) (string, error) {
	if !ValidSHA256(sha) {
		return "", errors.New("invalid hash")
	}
	l, err := m.loc(locID)
	if err != nil {
		return "", err
	}
	return filepath.Join(l.Path, "blobs", strconv.FormatInt(userID, 10), sha[0:2], sha[2:4], sha), nil
}

// TempPath returns the path for an upload's partial file.
func (m *Manager) TempPath(locID int64, uploadID string) (string, error) {
	if !ValidUploadID(uploadID) {
		return "", errors.New("invalid upload id")
	}
	l, err := m.loc(locID)
	if err != nil {
		return "", err
	}
	// The tmp directory is recreated if it went missing (for example a
	// location moved without it); only on a mounted location (marker present).
	dir := filepath.Join(l.Path, "tmp")
	if err := LocationHealthy(l.Path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create upload temp directory: %w", err)
	}
	return filepath.Join(dir, uploadID+".part"), nil
}

// Space describes a filesystem's capacity.
type Space struct {
	Total     int64 `json:"total"`
	Free      int64 `json:"free"`
	Available int64 `json:"available"`
}

// DiskSpace returns capacity for the filesystem holding path.
func DiskSpace(path string) (Space, error) { return diskSpace(path) }

// CheckSpace verifies that need more bytes fit on the location while
// keeping its reserve, the global floor and in-flight uploads free.
func (m *Manager) CheckSpace(ctx context.Context, locID int64, need int64) error {
	l, err := m.loc(locID)
	if err != nil {
		return err
	}
	if !l.Enabled {
		return fmt.Errorf("%w: location %q is disabled", ErrLocationUnavailable, l.Name)
	}
	if err := LocationHealthy(l.Path); err != nil {
		return err
	}
	sp, err := DiskSpace(l.Path)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrLocationUnavailable, err)
	}
	if need < 0 {
		return errors.New("invalid size")
	}
	pending, err := m.st.PendingBytes(ctx, locID)
	if err != nil {
		return err
	}
	reserve := max(l.ReserveBytes, m.globalMinFree())
	// Compared this way round so a huge need cannot overflow.
	if need > sp.Available-pending-reserve {
		return ErrInsufficientStorage
	}
	return nil
}

// Place moves a verified temp file into the blob store under the blob lock
// and runs commit while still holding it. If commit fails and the blob was
// newly created and remains unreferenced, the blob file is removed.
func (m *Manager) Place(ctx context.Context, userID int64, sha, tempPath string, locID int64, commit func(locID int64) error) error {
	if err := m.checkTemp(locID, tempPath); err != nil {
		return err
	}
	unlock := m.lock(userID, sha)
	defer unlock()
	useLoc := locID
	if existing, err := m.st.BlobLocation(ctx, userID, sha); err == nil {
		useLoc = existing
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	dest, err := m.BlobPath(useLoc, userID, sha)
	if err != nil {
		return err
	}
	created := false
	_, err = os.Lstat(dest)
	if errors.Is(err, os.ErrNotExist) && regular(dest+zstSuffix) {
		err = nil // already stored compressed
	}
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(dest), 0o750); err != nil {
			return err
		}
		if useLoc == locID {
			if err := os.Rename(tempPath, dest); err != nil {
				return fmt.Errorf("finalise blob: %w", err)
			}
		} else {
			// Existing blob row on another location but the file is missing:
			// copy the verified content there.
			if err := copyFile(tempPath, dest); err != nil {
				return err
			}
			_ = os.Remove(tempPath)
		}
		syncDir(filepath.Dir(dest))
		created = true
	} else if err != nil {
		return err
	} else {
		_ = os.Remove(tempPath)
	}
	if err := commit(useLoc); err != nil {
		if created {
			if ok, _ := m.st.BlobExists(ctx, userID, sha); !ok {
				_ = os.Remove(dest)
			}
		}
		return err
	}
	return nil
}

// checkTemp verifies that tempPath is a regular file inside the temp
// directory of location locID, so Place never moves an arbitrary file or a
// symlink into the blob store.
func (m *Manager) checkTemp(locID int64, tempPath string) error {
	l, err := m.loc(locID)
	if err != nil {
		return err
	}
	if !Within(filepath.Join(l.Path, "tmp"), tempPath) {
		return fmt.Errorf("%w: %s", ErrUnsafePath, tempPath)
	}
	fi, err := os.Lstat(tempPath)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%w: %s", ErrUnsafePath, tempPath)
	}
	return nil
}

// LinkExisting runs commit for content the user already stores
// (deduplication) while holding the blob lock.
func (m *Manager) LinkExisting(ctx context.Context, userID int64, sha string, commit func(locID int64) error) (bool, error) {
	unlock := m.lock(userID, sha)
	defer unlock()
	loc, err := m.st.BlobLocation(ctx, userID, sha)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	p, err := m.BlobPath(loc, userID, sha)
	if err != nil {
		return false, err
	}
	if _, _, err := storedPath(p); err != nil {
		slog.Warn("blob row exists but file is missing; forcing re-upload", "user", userID, "sha", sha)
		return false, nil
	}
	return true, commit(loc)
}

// Remove deletes blob files whose rows are gone.
func (m *Manager) Remove(ctx context.Context, refs []store.BlobRef) {
	for _, r := range refs {
		func() {
			unlock := m.lock(r.UserID, r.SHA256)
			defer unlock()
			if ok, err := m.st.BlobExists(ctx, r.UserID, r.SHA256); err != nil || ok {
				return
			}
			p, err := m.BlobPath(r.LocationID, r.UserID, r.SHA256)
			if err != nil {
				return
			}
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				slog.Warn("failed to delete blob", "path", p, "err", err)
			}
			m.removeVariants(r.LocationID, r.UserID, r.SHA256, p)
		}()
	}
}

// Open opens a blob for reading. Anything other than a regular file (for
// example a planted symlink) is refused.
func (m *Manager) Open(locID, userID int64, sha string) (*os.File, error) {
	f, err := m.openPrimary(locID, userID, sha)
	if errors.Is(err, os.ErrNotExist) {
		if fb := m.getFallback(); fb != nil {
			if f2, ferr := fb(locID, userID, sha); ferr == nil {
				return f2, nil
			}
		}
	}
	return f, err
}

// openPrimary opens the primary file of a blob, without the fallback.
func (m *Manager) openPrimary(locID, userID int64, sha string) (*os.File, error) {
	p, err := m.BlobPath(locID, userID, sha)
	if err != nil {
		return nil, err
	}
	sp, zipped, err := storedPath(p)
	if err != nil {
		return nil, err
	}
	if zipped {
		// At-rest compressed (compress.go): read from the extracted copy.
		return m.openExtracted(locID, userID, sha, sp)
	}
	return os.Open(sp)
}

// HashFile computes the SHA-256 of a file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 1<<20)
	if _, err := io.CopyBuffer(h, f, buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// copyFile copies src to dst atomically: it writes dst+copySuffix, syncs it
// and renames it into place. Callers hold the blob lock, so the fixed temp
// name cannot collide.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	return writeAtomic(dst, dst+copySuffix, 0o640, func(w io.Writer) error {
		_, err := io.Copy(w, in)
		return err
	})
}

// writeFileAtomic writes data to path via a synced temp file and rename.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp := path + ".tmp-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	return writeAtomic(path, tmp, perm, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// writeAtomic creates tmp exclusively, fills and syncs it, renames it to dst
// and syncs the directory. tmp is removed on any failure.
func writeAtomic(dst, tmp string, perm os.FileMode, fill func(io.Writer) error) error {
	_ = os.Remove(tmp) // stale leftover of a crashed attempt
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	err = fill(out)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, dst)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}

func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// CleanOrphans removes temp files without an open upload, blob files
// without a database row and stale partial copies (<sha>.copying). Files
// younger than minAge are left alone.
func (m *Manager) CleanOrphans(ctx context.Context, minAge time.Duration) (int, error) {
	if err := m.Reload(ctx); err != nil {
		return 0, err
	}
	m.mu.RLock()
	locs := make([]store.Location, 0, len(m.locs))
	for _, l := range m.locs {
		locs = append(locs, l)
	}
	m.mu.RUnlock()
	removed := 0
	cutoff := time.Now().Add(-minAge)
	for _, l := range locs {
		if LocationHealthy(l.Path) != nil {
			continue
		}
		tmpDir := filepath.Join(l.Path, "tmp")
		entries, _ := os.ReadDir(tmpDir)
		for _, e := range entries {
			name := e.Name()
			info, err := e.Info()
			if err != nil || info.ModTime().After(cutoff) {
				continue
			}
			id := name
			if ext := filepath.Ext(name); ext == ".part" {
				id = name[:len(name)-len(ext)]
			}
			if u, err := m.st.UploadAny(ctx, id); err == nil && (u.Status == "open" || u.Status == "finalizing") {
				continue
			}
			if os.Remove(filepath.Join(tmpDir, name)) == nil {
				removed++
			}
		}
		keys, err := m.st.AllBlobKeys(ctx, l.ID)
		if err != nil {
			return removed, err
		}
		blobRoot := filepath.Join(l.Path, "blobs")
		_ = filepath.WalkDir(blobRoot, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			sha, partial := strings.CutSuffix(d.Name(), copySuffix)
			if !partial {
				// A compression run interrupted mid-write.
				sha, partial = strings.CutSuffix(sha, zstSuffix+".tmp")
			}
			sha = blobBaseName(sha)
			if !ValidSHA256(sha) {
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
			if !partial && keys[store.BlobKey(uid, sha)] {
				return nil
			}
			info, err := d.Info()
			if err != nil || info.ModTime().After(cutoff) {
				return nil
			}
			unlock := m.lock(uid, sha)
			defer unlock()
			// A stale partial copy is never valid content whatever the blob
			// row says; a copy in progress is young and skipped above.
			if !partial {
				if ok, _ := m.st.BlobExists(ctx, uid, sha); ok {
					return nil
				}
			}
			if os.Remove(p) == nil {
				removed++
			}
			return nil
		})
	}
	return removed, nil
}

func splitPath(p string) []string {
	var out []string
	for {
		dir, file := filepath.Split(p)
		if file != "" {
			out = append([]string{file}, out...)
		}
		dir = filepath.Clean(dir)
		if dir == "." || dir == string(filepath.Separator) || dir == p {
			break
		}
		p = dir
	}
	return out
}
