package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type fixture struct {
	st  *store.Store
	m   *Manager
	loc store.Location
	usr *store.User
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	dir := t.TempDir()
	if err := PrepareLocation(dir); err != nil {
		t.Fatal(err)
	}
	l := &store.Location{Name: "Default", Path: dir, Enabled: true, IsDefault: true}
	if err := st.CreateLocation(ctx, l); err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, store.NewUser{Username: "alice", PasswordHash: "x", Role: "user", LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	m := New(st)
	m.GlobalMinFree = 0
	if err := m.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	return &fixture{st: st, m: m, loc: *l, usr: u}
}

// symlinkOrSkip creates a directory link: a symlink, or on Windows without
// the symlink privilege a junction (the common reparse point there). The
// test is skipped when neither can be created.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err != nil && runtime.GOOS == "windows" {
		if out, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); jerr == nil {
			return
		} else {
			err = fmt.Errorf("%v; junction: %v %s", err, jerr, out)
		}
	}
	if err != nil {
		t.Skipf("cannot create directory link: %v", err)
	}
}

func old(t *testing.T, p string) {
	t.Helper()
	ts := time.Now().Add(-3 * time.Hour)
	if err := os.Chtimes(p, ts, ts); err != nil {
		t.Fatal(err)
	}
}

func TestWithin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	for _, tc := range []struct {
		p    string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "a", "b"), true},
		{filepath.Join(root, "a", "..", "b"), true},
		{filepath.Join(root, ".."), false},
		{filepath.Join(root, "..", "rootx"), false},
		{root + "x", false},
		{filepath.Join(root, "..dots"), true},
	} {
		if got := Within(root, tc.p); got != tc.want {
			t.Errorf("Within(%q, %q) = %v want %v", root, tc.p, got, tc.want)
		}
	}
}

func TestResolveLocationPath(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "root")
	outside := filepath.Join(base, "outside")
	for _, d := range []string{filepath.Join(root, "hdd"), outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := ResolveLocationPath(filepath.Join(root, "hdd"), []string{root}); err != nil {
		t.Fatalf("inside root: %v", err)
	}
	if _, err := ResolveLocationPath(outside, []string{root}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("outside root: %v", err)
	}
	if _, err := ResolveLocationPath(filepath.Join(root, "missing"), []string{root}); err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("missing path: %v", err)
	}
	if _, err := ResolveLocationPath("relative", nil); err == nil {
		t.Fatal("relative path accepted")
	}
	if _, err := ResolveLocationPath(outside, nil); err != nil {
		t.Fatalf("no roots configured: %v", err)
	}
	symlinkOrSkip(t, outside, filepath.Join(root, "escape"))
	if _, err := ResolveLocationPath(filepath.Join(root, "escape"), []string{root}); !errors.Is(err, ErrOutsideRoots) {
		t.Fatalf("symlink escaping the root accepted: %v", err)
	}
	if !Overlaps(filepath.Join(root, "escape"), outside) {
		t.Fatal("symlink and its target must overlap")
	}
}

func TestPrepareLocation(t *testing.T) {
	dir := t.TempDir()
	if err := PrepareLocation(dir); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"blobs", "tmp", markerFile} {
		if _, err := os.Lstat(filepath.Join(dir, p)); err != nil {
			t.Fatalf("%s missing: %v", p, err)
		}
	}
	if err := LocationHealthy(dir); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := PrepareLocation(dir); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp-") {
			t.Fatalf("temp marker file left behind: %s", e.Name())
		}
	}
	file := filepath.Join(dir, "file")
	os.WriteFile(file, []byte("x"), 0o600)
	for name, p := range map[string]string{"relative": "rel/path", "missing": filepath.Join(dir, "nope"), "file": file} {
		if err := PrepareLocation(p); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := LocationHealthy(t.TempDir()); !errors.Is(err, ErrLocationUnavailable) {
		t.Fatalf("unmarked dir healthy: %v", err)
	}

	evil := t.TempDir()
	target := t.TempDir()
	symlinkOrSkip(t, target, filepath.Join(evil, "blobs"))
	if err := PrepareLocation(evil); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked blobs dir accepted: %v", err)
	}
}

func TestAtomicWrites(t *testing.T) {
	dir := t.TempDir()
	dst := filepath.Join(dir, "out")
	if err := writeFileAtomic(dst, []byte("hello"), 0o640); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "hello" {
		t.Fatalf("content %q", b)
	}
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("payload"), 0o600)
	blob := filepath.Join(dir, "blob")
	// a stale partial copy from a crash must not break the next copy
	os.WriteFile(blob+copySuffix, []byte("junk"), 0o600)
	if err := copyFile(src, blob); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(blob); string(b) != "payload" {
		t.Fatalf("content %q", b)
	}
	if _, err := os.Stat(blob + copySuffix); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial copy left: %v", err)
	}
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "x")); err == nil {
		t.Fatal("copy of missing file succeeded")
	}
	// fill failure leaves neither temp nor destination
	if err := writeAtomic(filepath.Join(dir, "fail"), filepath.Join(dir, "fail.tmp"), 0o640, func(io.Writer) error {
		return errors.New("boom")
	}); err == nil {
		t.Fatal("fill error swallowed")
	}
	for _, p := range []string{"fail", "fail.tmp"} {
		if _, err := os.Stat(filepath.Join(dir, p)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left after failed write: %v", p, err)
		}
	}
}

func TestCheckSpace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if err := f.m.CheckSpace(ctx, f.loc.ID, 1); err != nil {
		t.Fatalf("small upload refused: %v", err)
	}
	if err := f.m.CheckSpace(ctx, f.loc.ID, -1); err == nil {
		t.Fatal("negative size accepted")
	}
	if err := f.m.CheckSpace(ctx, f.loc.ID, 1<<62); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("huge upload: %v", err)
	}
	f.m.SetGlobalMinFree(1 << 61)
	if err := f.m.CheckSpace(ctx, f.loc.ID, 1); !errors.Is(err, ErrInsufficientStorage) {
		t.Fatalf("global floor ignored: %v", err)
	}
	f.m.SetGlobalMinFree(-5)
	if f.m.globalMinFree() != 0 {
		t.Fatal("negative floor not clamped")
	}
	if err := f.m.CheckSpace(ctx, 9999, 1); !errors.Is(err, ErrLocationUnavailable) {
		t.Fatalf("unknown location: %v", err)
	}
	os.Remove(filepath.Join(f.loc.Path, markerFile))
	if err := f.m.CheckSpace(ctx, f.loc.ID, 1); !errors.Is(err, ErrLocationUnavailable) {
		t.Fatalf("unmounted location: %v", err)
	}
}

// TestGlobalMinFreeConcurrent exercises R7; run with -race to verify.
func TestGlobalMinFreeConcurrent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := range 50 {
				f.m.SetGlobalMinFree(int64(i * j))
			}
		}()
		go func() {
			defer wg.Done()
			for range 50 {
				_ = f.m.CheckSpace(ctx, f.loc.ID, 1)
			}
		}()
	}
	wg.Wait()
}

func writeTemp(t *testing.T, f *fixture, id, content string) string {
	t.Helper()
	p, err := f.m.TempPath(f.loc.ID, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestPlaceAndOpen(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := "hello blob"
	tmp := writeTemp(t, f, "upload-0000000000001", content)
	sum, err := HashFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	commit := func(loc int64) error {
		_, err := f.st.CommitFile(ctx, store.CommitRequest{User: f.usr, Name: "a.txt", Size: int64(len(content)), SHA256: sum, LocationID: loc, OnConflict: "replace"})
		return err
	}

	// temp files outside the location's tmp directory are refused
	stray := filepath.Join(t.TempDir(), "upload-0000000000001.part")
	os.WriteFile(stray, []byte(content), 0o600)
	if err := f.m.Place(ctx, f.usr.ID, sum, stray, f.loc.ID, commit); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("stray temp accepted: %v", err)
	}

	// a failed commit removes the newly created, unreferenced blob
	if err := f.m.Place(ctx, f.usr.ID, sum, tmp, f.loc.ID, func(int64) error { return errors.New("boom") }); err == nil {
		t.Fatal("commit error swallowed")
	}
	blob, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, sum)
	if _, err := os.Stat(blob); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan blob left after failed commit: %v", err)
	}

	tmp = writeTemp(t, f, "upload-0000000000002", content)
	if err := f.m.Place(ctx, f.usr.ID, sum, tmp, f.loc.ID, commit); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tmp); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temp file not consumed")
	}
	fh, err := f.m.Open(f.loc.ID, f.usr.ID, sum)
	if err != nil {
		t.Fatal(err)
	}
	fh.Close()
	if got, _ := HashFile(blob); got != sum {
		t.Fatalf("blob hash %s want %s", got, sum)
	}
	if _, err := f.m.Open(f.loc.ID, f.usr.ID, "not-a-hash"); err == nil {
		t.Fatal("invalid hash opened")
	}

	// a link planted in place of a blob is never served
	other := strings.Repeat("b", 64)
	link, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, other)
	os.MkdirAll(filepath.Dir(link), 0o750)
	symlinkOrSkip(t, t.TempDir(), link)
	if _, err := f.m.Open(f.loc.ID, f.usr.ID, other); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlinked blob opened: %v", err)
	}
}

func TestCleanOrphans(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	content := "kept"
	tmp := writeTemp(t, f, "upload-0000000000003", content)
	sum, _ := HashFile(tmp)
	if err := f.m.Place(ctx, f.usr.ID, sum, tmp, f.loc.ID, func(loc int64) error {
		_, err := f.st.CommitFile(ctx, store.CommitRequest{User: f.usr, Name: "k.txt", Size: int64(len(content)), SHA256: sum, LocationID: loc})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	kept, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, sum)
	old(t, kept)

	mk := func(p string, age bool) string {
		os.MkdirAll(filepath.Dir(p), 0o750)
		os.WriteFile(p, []byte("x"), 0o640)
		if age {
			old(t, p)
		}
		return p
	}
	orphanSHA := strings.Repeat("c", 64)
	orphan, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, orphanSHA)
	mk(orphan, true)
	staleCopy := mk(kept+copySuffix, true) // R6: leaked partial copy of a live blob
	youngCopy := mk(orphan+copySuffix, false)
	staleTemp := mk(filepath.Join(f.loc.Path, "tmp", "upload-0000000000009.part"), true)
	unrelated := mk(filepath.Join(f.loc.Path, "blobs", "README"), true)

	n, err := f.m.CleanOrphans(ctx, 2*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("removed %d files, want 3", n)
	}
	for p, want := range map[string]bool{kept: true, orphan: false, staleCopy: false, youngCopy: true, staleTemp: false, unrelated: true} {
		_, err := os.Stat(p)
		if exists := err == nil; exists != want {
			t.Errorf("%s exists=%v want %v", p, exists, want)
		}
	}
}
