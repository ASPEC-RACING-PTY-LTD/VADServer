package localfs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore/objstoretest"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestConformance(t *testing.T) {
	objstoretest.Run(t, func(t *testing.T) objstore.Store { return newStore(t) })
}

func TestNew(t *testing.T) {
	dir := t.TempDir()
	if _, err := New("relative/path"); err == nil {
		t.Error("relative root accepted")
	}
	if _, err := New(filepath.Join(dir, "missing")); err == nil {
		t.Error("missing root accepted")
	}
	f := filepath.Join(dir, "file")
	if err := os.WriteFile(f, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f); err == nil {
		t.Error("file root accepted")
	}
}

func TestLayoutOnDisk(t *testing.T) {
	s := newStore(t)
	oid := strings.Repeat("ab", 32)
	key := "repo/u/data/" + oid[:2] + "/" + oid
	objstoretest.MustPut(t, s, key, []byte("pack"))
	b, err := os.ReadFile(filepath.Join(s.Root(), "repo", "u", "data", "ab", oid))
	if err != nil || string(b) != "pack" {
		t.Fatalf("file on disk = %q, %v", b, err)
	}
	// No temp files are left next to the object.
	ents, err := os.ReadDir(filepath.Join(s.Root(), "repo", "u", "data", "ab"))
	if err != nil || len(ents) != 1 {
		t.Fatalf("dir entries = %v, %v", ents, err)
	}
}

func TestFailedPutLeavesNoTemp(t *testing.T) {
	s := newStore(t)
	b := []byte("data")
	err := s.Put(context.Background(), "t/obj", bytes.NewReader(b), 4, objstoretest.Sum([]byte("nope")))
	if !errors.Is(err, objstore.ErrHashMismatch) {
		t.Fatal(err)
	}
	ents, err := os.ReadDir(filepath.Join(s.Root(), "t"))
	if err != nil || len(ents) != 0 {
		t.Fatalf("leftovers = %v, %v", ents, err)
	}
}

func TestListSkipsTempAndForeignFiles(t *testing.T) {
	s := newStore(t)
	objstoretest.MustPut(t, s, "a/obj", []byte("x"))
	for _, name := range []string{".tmp-stale", "UPPER", "bad name"} {
		if err := os.WriteFile(filepath.Join(s.Root(), "a", name), []byte("y"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	page, err := s.List(context.Background(), "", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Objects) != 1 || page.Objects[0].Key != "a/obj" {
		t.Fatalf("List = %+v", page.Objects)
	}
}

// link creates a directory link at link pointing to target: a symlink, or a
// junction on Windows when symlinks need privileges. It skips the test when
// neither is possible.
func link(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		return
	}
	if runtime.GOOS == "windows" {
		if err := exec.Command("cmd", "/c", "mklink", "/J", link, target).Run(); err == nil {
			return
		}
	}
	t.Skip("cannot create symlinks or junctions here")
}

func TestRefusesLinkEscape(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	outside := t.TempDir()
	secret := []byte("secret")
	if err := os.MkdirAll(filepath.Join(outside, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "sub", "obj"), secret, 0o600); err != nil {
		t.Fatal(err)
	}
	link(t, outside, filepath.Join(s.Root(), "esc"))

	b := []byte("x")
	if err := s.Put(ctx, "esc/new", bytes.NewReader(b), 1, objstoretest.Sum(b)); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Put through link = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "new")); err == nil {
		t.Error("Put wrote outside the root")
	}
	if _, _, err := s.Get(ctx, "esc/sub/obj"); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Get through link = %v", err)
	}
	if _, err := s.GetRange(ctx, "esc/sub/obj", 0, 1); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("GetRange through link = %v", err)
	}
	if _, err := s.Stat(ctx, "esc/sub/obj"); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Stat through link = %v", err)
	}
	if err := s.Delete(ctx, "esc/sub/obj"); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Delete through link = %v", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "sub", "obj")); err != nil {
		t.Error("Delete removed a file outside the root")
	}
	if _, err := s.List(ctx, "esc/", "", 0); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("List through link = %v", err)
	}
	page, err := s.List(ctx, "", "", 0)
	if err != nil || len(page.Objects) != 0 {
		t.Errorf("List root = %+v, %v (link must not be followed)", page.Objects, err)
	}
}

func TestRefusesFileSymlink(t *testing.T) {
	ctx := context.Background()
	s := newStore(t)
	outside := filepath.Join(t.TempDir(), "target")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(s.Root(), "f"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root(), "f", "obj")); err != nil {
		t.Skip("cannot create file symlinks here")
	}
	if _, _, err := s.Get(ctx, "f/obj"); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Get = %v", err)
	}
	b := []byte("secret")
	if err := s.Put(ctx, "f/obj", bytes.NewReader(b), int64(len(b)), objstoretest.Sum(b)); !errors.Is(err, objstore.ErrUnsafePath) {
		t.Errorf("Put = %v", err)
	}
}

func TestRootIsLink(t *testing.T) {
	real := t.TempDir()
	l := filepath.Join(t.TempDir(), "root")
	link(t, real, l)
	s, err := New(l)
	if err != nil {
		t.Fatal(err)
	}
	objstoretest.MustPut(t, s, "k", []byte("v"))
	if _, err := os.Stat(filepath.Join(real, "k")); err != nil {
		t.Fatalf("object not under resolved root: %v", err)
	}
}
