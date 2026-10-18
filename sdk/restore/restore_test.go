package restore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

type fakeRepo struct {
	trees map[string]*snapshot.Tree
	blobs map[pack.BlobID][]byte
	sizes bool
	reads int
}

func (f *fakeRepo) ReadTree(_ context.Context, id string) (*snapshot.Tree, error) {
	t, ok := f.trees[id]
	if !ok {
		return nil, fmt.Errorf("no tree %s", id)
	}
	return t, nil
}

func (f *fakeRepo) ReadBlob(_ context.Context, id pack.BlobID) ([]byte, error) {
	f.reads++
	b, ok := f.blobs[id]
	if !ok {
		return nil, errors.New("no blob")
	}
	return b, nil
}

type sizedRepo struct{ *fakeRepo }

func (s sizedRepo) BlobSize(id pack.BlobID) (int64, bool) {
	b, ok := s.blobs[id]
	return int64(len(b)), ok
}

func sp(s string) *string { return &s }

var kfh = vdcrypt.Key{1, 2, 3}

// addFile stores content in chunks of size n and returns its entry.
func (f *fakeRepo) addFile(name string, content []byte, n int) snapshot.Entry {
	var ch []string
	for i := 0; i < len(content); i += n {
		end := min(i+n, len(content))
		var id pack.BlobID
		copy(id[:], fmt.Sprintf("%s-%d-%032d", name, i, len(f.blobs)))
		f.blobs[id] = append([]byte(nil), content[i:end]...)
		ch = append(ch, hex.EncodeToString(id[:]))
	}
	h := snapshot.FileHashBytes(kfh, content)
	return snapshot.Entry{N: name, T: snapshot.TypeFile, S: int64(len(content)), M: 100, H: sp(hex.EncodeToString(h[:])), Ch: ch}
}

func fixture() (*fakeRepo, []snapshot.RootRef) {
	f := &fakeRepo{trees: map[string]*snapshot.Tree{}, blobs: map[pack.BlobID][]byte{}}
	f.trees["tmp"] = &snapshot.Tree{Entries: []snapshot.Entry{f.addFile("junk.bin", []byte("junk"), 2), {N: "keep", T: snapshot.TypeDir, Sub: sp("keep")}}}
	f.trees["keep"] = &snapshot.Tree{Entries: []snapshot.Entry{f.addFile("k.txt", []byte("k"), 4)}}
	f.trees["docs"] = &snapshot.Tree{Entries: []snapshot.Entry{
		f.addFile("a.txt", []byte("hello world"), 4),
		{N: "tmp", T: snapshot.TypeDir, Sub: sp("tmp")},
	}, Cont: sp("docs2")}
	f.trees["docs2"] = &snapshot.Tree{Entries: []snapshot.Entry{f.addFile("b.txt", []byte("bee"), 4)}}
	f.trees["desk"] = &snapshot.Tree{Entries: []snapshot.Entry{f.addFile("d.txt", []byte("desk"), 4)}}
	roots := []snapshot.RootRef{{TokenPath: "{DOCUMENTS}", Tree: "docs"}, {TokenPath: "{DESKTOP}/Work", Tree: "desk"}}
	return f, roots
}

func walkPaths(t *testing.T, f *fakeRepo, roots []snapshot.RootRef, sel []wire.RestoreSelection) []string {
	t.Helper()
	s, err := NewSelector(sel)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	if err := Walk(context.Background(), f, roots, s, func(it Item) error { out = append(out, it.Path); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWalkSelection(t *testing.T) {
	f, roots := fixture()
	all := walkPaths(t, f, roots, nil)
	want := "{DOCUMENTS}/a.txt {DOCUMENTS}/tmp {DOCUMENTS}/tmp/junk.bin {DOCUMENTS}/tmp/keep {DOCUMENTS}/tmp/keep/k.txt {DOCUMENTS}/b.txt {DESKTOP}/Work/d.txt"
	if got := strings.Join(all, " "); got != want {
		t.Fatalf("all = %s", got)
	}
	got := walkPaths(t, f, roots, []wire.RestoreSelection{
		{Path: "{DOCUMENTS}", Include: true},
		{Path: "{DOCUMENTS}/tmp", Include: false},
		{Path: "{DOCUMENTS}/TMP/keep/", Include: true},
	})
	if s := strings.Join(got, " "); s != "{DOCUMENTS}/a.txt {DOCUMENTS}/tmp/keep {DOCUMENTS}/tmp/keep/k.txt {DOCUMENTS}/b.txt" {
		t.Fatalf("selected = %s", s)
	}
	got = walkPaths(t, f, roots, []wire.RestoreSelection{{Path: "{DESKTOP}/Work/d.txt", Include: true}})
	if len(got) != 1 || got[0] != "{DESKTOP}/Work/d.txt" {
		t.Fatalf("single file = %v", got)
	}
	if _, err := NewSelector([]wire.RestoreSelection{{Path: "C:\\x", Include: true}}); !errors.Is(err, ErrSelection) {
		t.Fatalf("bad path accepted: %v", err)
	}
}

func TestListDirAndLookup(t *testing.T) {
	f, roots := fixture()
	ctx := context.Background()
	top, err := ListDir(ctx, f, roots, "")
	if err != nil || len(top) != 2 || top[0].Path != "{DESKTOP}" || top[1].Path != "{DOCUMENTS}" {
		t.Fatalf("top = %+v, %v", top, err)
	}
	desk, err := ListDir(ctx, f, roots, "{DESKTOP}")
	if err != nil || len(desk) != 1 || desk[0].Path != "{DESKTOP}/Work" || !desk[0].IsDir() {
		t.Fatalf("desktop = %+v, %v", desk, err)
	}
	docs, err := ListDir(ctx, f, roots, "{DOCUMENTS}")
	if err != nil || len(docs) != 3 || docs[0].Entry.N != "tmp" || docs[2].Entry.N != "b.txt" {
		t.Fatalf("docs = %+v, %v", docs, err)
	}
	it, err := Lookup(ctx, f, roots, "{DOCUMENTS}/TMP/keep/k.txt")
	if err != nil || it.Entry.S != 1 {
		t.Fatalf("lookup = %+v, %v", it, err)
	}
	if _, err := ListDir(ctx, f, roots, "{DOCUMENTS}/nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing dir = %v", err)
	}
}

func TestFileReaderVerifiesAndSeeks(t *testing.T) {
	f, roots := fixture()
	ctx := context.Background()
	it, _ := Lookup(ctx, f, roots, "{DOCUMENTS}/a.txt")
	var buf bytes.Buffer
	if n, err := CopyFile(ctx, f, &kfh, it.Entry, &buf); err != nil || n != 11 || buf.String() != "hello world" {
		t.Fatalf("copy = %d %q %v", n, buf.String(), err)
	}
	// A wrong hash fails at EOF.
	bad := it.Entry
	bad.H = sp(strings.Repeat("00", 32))
	if _, err := CopyFile(ctx, f, &kfh, bad, io.Discard); !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("bad hash = %v", err)
	}
	// Seeking with known chunk sizes.
	fr, err := NewFileReader(ctx, sizedRepo{f}, &kfh, it.Entry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fr.Seek(6, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	rest, err := io.ReadAll(fr)
	if err != nil || string(rest) != "world" {
		t.Fatalf("after seek = %q, %v", rest, err)
	}
	if fr.Verified() {
		t.Fatal("partial read reported verified")
	}
	fr, _ = NewFileReader(ctx, sizedRepo{f}, &kfh, it.Entry)
	if all, err := io.ReadAll(fr); err != nil || string(all) != "hello world" || !fr.Verified() {
		t.Fatalf("full read = %q, %v, verified %v", all, err, fr.Verified())
	}
	// Without sizes only a sequential read works.
	fr, _ = NewFileReader(ctx, f, nil, it.Entry)
	if _, err := fr.Seek(0, io.SeekEnd); err != nil {
		t.Fatal(err)
	}
	if _, err := fr.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 2)
	fr.Read(b)
	if _, err := fr.Seek(7, io.SeekStart); !errors.Is(err, ErrNoSeek) {
		t.Fatalf("seek without sizes = %v", err)
	}
	// Recorded size disagreeing with the chunks.
	short := it.Entry
	short.S = 5
	if _, err := NewFileReader(ctx, sizedRepo{f}, nil, short); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("size mismatch = %v", err)
	}
	// Empty file.
	empty := f.addFile("e", nil, 4)
	if n, err := CopyFile(ctx, f, &kfh, empty, io.Discard); err != nil || n != 0 {
		t.Fatalf("empty = %d %v", n, err)
	}
}

func TestMapperAndDecide(t *testing.T) {
	m, err := NewMapper(snapshot.Tokens{"{DOCUMENTS}": `C:\Users\bob\Documents`}, []wire.RestorePathMapping{
		{From: "{DOCUMENTS}/Taxes", To: `D:\Restore\Taxes\`},
		{From: "{DESKTOP}", To: `\\nas\share\desk`},
	})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"{DOCUMENTS}/a.txt":            `C:\Users\bob\Documents\a.txt`,
		"{DOCUMENTS}/taxes/2024/x.pdf": `D:\Restore\Taxes\2024\x.pdf`,
		"{DOCUMENTS}/Taxes":            `D:\Restore\Taxes`,
		"{DESKTOP}/n.txt":              `\\nas\share\desk\n.txt`,
	}
	for in, want := range cases {
		if got, err := m.Target(in); err != nil || got != want {
			t.Errorf("Target(%s) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := m.Target("{MUSIC}/a.mp3"); !errors.Is(err, ErrUnmapped) {
		t.Fatalf("unmapped = %v", err)
	}
	if _, err := NewMapper(nil, []wire.RestorePathMapping{{From: "{DOCUMENTS}", To: `relative\x`}}); err == nil {
		t.Fatal("relative override accepted")
	}

	e := snapshot.Entry{T: snapshot.TypeFile, S: 10, M: 200}
	for _, tc := range []struct {
		policy string
		ex     Existing
		want   string
	}{
		{"skip", Existing{}, wire.RestoreActionRestore},
		{"overwrite", Existing{Exists: true, IsDir: true}, wire.RestoreActionSkip},
		{"rename", Existing{Exists: true, Size: 10, MTime: 200}, wire.RestoreActionSkip},
		{"overwrite", Existing{Exists: true, Size: 10, MTime: 200}, wire.RestoreActionOverwrite},
		{"skip", Existing{Exists: true, Size: 3}, wire.RestoreActionSkip},
		{"rename", Existing{Exists: true, Size: 3}, wire.RestoreActionRename},
		{"newer", Existing{Exists: true, Size: 3, MTime: 100}, wire.RestoreActionOverwrite},
		{"newer", Existing{Exists: true, Size: 3, MTime: 300}, wire.RestoreActionSkip},
	} {
		if got := Decide(tc.policy, e, tc.ex); got != tc.want {
			t.Errorf("Decide(%s, %+v) = %s; want %s", tc.policy, tc.ex, got, tc.want)
		}
	}

	taken := map[string]bool{`C:\d\r.txt`: true, `C:\d\r (restored).txt`: true}
	if p, err := RenamedPath(`C:\d\r.txt`, func(p string) bool { return taken[p] }); err != nil || p != `C:\d\r (restored 2).txt` {
		t.Fatalf("renamed = %q, %v", p, err)
	}
	at := time.Date(2026, 10, 18, 15, 30, 5, 0, time.UTC)
	if got := ReplacedName(`C:\d\.profile`, at); got != `C:\d\.profile (replaced 2026-10-18 153005)` {
		t.Fatalf("replaced = %q", got)
	}
}

func TestSummarize(t *testing.T) {
	f, roots := fixture()
	s, err := Summarize(context.Background(), f, roots, nil)
	if err != nil || s.Files != 5 || s.Dirs != 2 || s.Bytes != int64(len("hello world")+len("junk")+1+3+4) {
		t.Fatalf("summary = %+v, %v", s, err)
	}
}
