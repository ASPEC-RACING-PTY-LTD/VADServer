// Package objstoretest is the conformance suite every objstore backend must
// pass. A backend test calls Run with a constructor returning a fresh, empty
// store.
package objstoretest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
)

// Sum returns the lower-case hex SHA-256 of b.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// MustPut stores b under key and fails the test on error.
func MustPut(t testing.TB, s objstore.Store, key string, b []byte) {
	t.Helper()
	if err := s.Put(context.Background(), key, bytes.NewReader(b), int64(len(b)), Sum(b)); err != nil {
		t.Fatalf("Put(%q): %v", key, err)
	}
}

// ReadAll reads key fully and fails the test on error.
func ReadAll(t testing.TB, s objstore.Store, key string) []byte {
	t.Helper()
	rc, size, err := s.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("Get(%q): %v", key, err)
	}
	defer rc.Close()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read %q: %v", key, err)
	}
	if int64(len(b)) != size {
		t.Fatalf("Get(%q) size %d, read %d bytes", key, size, len(b))
	}
	return b
}

const (
	oid  = "ab3f0c9e1d2b4a5f6e7d8c9b0a1f2e3d4c5b6a7f8e9d0c1b2a3f4e5d6c7b8a9f"
	repo = "repo/0b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"
)

// Run runs the conformance suite against stores made by newStore.
func Run(t *testing.T, newStore func(t *testing.T) objstore.Store) {
	ctx := context.Background()

	t.Run("PutGetRoundTrip", func(t *testing.T) {
		s := newStore(t)
		keys := []string{
			repo + "/config",
			repo + "/keys/" + oid,
			repo + "/chain/" + oid,
			repo + "/index/" + oid,
			repo + "/snapshots/" + oid,
			repo + "/data/" + oid[:2] + "/" + oid,
			repo + "/locks/" + oid,
		}
		for i, k := range keys {
			b := []byte(fmt.Sprintf("object %d %s", i, strings.Repeat("x", i*1000)))
			MustPut(t, s, k, b)
			if got := ReadAll(t, s, k); !bytes.Equal(got, b) {
				t.Fatalf("Get(%q) content differs", k)
			}
		}
	})

	t.Run("EmptyObject", func(t *testing.T) {
		s := newStore(t)
		MustPut(t, s, "a/empty", nil)
		if got := ReadAll(t, s, "a/empty"); len(got) != 0 {
			t.Fatalf("got %d bytes", len(got))
		}
		info, err := s.Stat(ctx, "a/empty")
		if err != nil || info.Size != 0 {
			t.Fatalf("Stat = %+v, %v", info, err)
		}
	})

	t.Run("Stat", func(t *testing.T) {
		s := newStore(t)
		b := []byte("hello world")
		MustPut(t, s, "a/b/c", b)
		info, err := s.Stat(ctx, "a/b/c")
		if err != nil {
			t.Fatal(err)
		}
		if info.Key != "a/b/c" || info.Size != int64(len(b)) || info.ModTime.IsZero() {
			t.Fatalf("Stat = %+v", info)
		}
	})

	t.Run("NotFound", func(t *testing.T) {
		s := newStore(t)
		MustPut(t, s, "a/present", []byte("x"))
		for _, k := range []string{"a/missing", "missing/deep/key", "a/present/child"} {
			if _, err := s.Stat(ctx, k); !errors.Is(err, objstore.ErrNotFound) {
				t.Errorf("Stat(%q) = %v, want ErrNotFound", k, err)
			}
			if _, _, err := s.Get(ctx, k); !errors.Is(err, objstore.ErrNotFound) {
				t.Errorf("Get(%q) = %v, want ErrNotFound", k, err)
			}
			if _, err := s.GetRange(ctx, k, 0, 1); !errors.Is(err, objstore.ErrNotFound) {
				t.Errorf("GetRange(%q) = %v, want ErrNotFound", k, err)
			}
		}
	})

	t.Run("GetRange", func(t *testing.T) {
		s := newStore(t)
		b := []byte("0123456789")
		MustPut(t, s, "r/obj", b)
		cases := []struct {
			off, length int64
			want        string
			err         error
		}{
			{0, 10, "0123456789", nil},
			{0, -1, "0123456789", nil},
			{2, 3, "234", nil},
			{7, -1, "789", nil},
			{7, 100, "789", nil},
			{9, 1, "9", nil},
			{10, 5, "", nil},
			{0, 0, "", nil},
			{-1, 2, "", objstore.ErrInvalidRange},
			{11, 1, "", objstore.ErrInvalidRange},
		}
		for _, c := range cases {
			rc, err := s.GetRange(ctx, "r/obj", c.off, c.length)
			if c.err != nil {
				if !errors.Is(err, c.err) {
					t.Errorf("GetRange(%d,%d) err = %v, want %v", c.off, c.length, err, c.err)
				}
				if err == nil {
					rc.Close()
				}
				continue
			}
			if err != nil {
				t.Errorf("GetRange(%d,%d): %v", c.off, c.length, err)
				continue
			}
			got, err := io.ReadAll(rc)
			rc.Close()
			if err != nil || string(got) != c.want {
				t.Errorf("GetRange(%d,%d) = %q, %v; want %q", c.off, c.length, got, err, c.want)
			}
		}
	})

	t.Run("HashMismatch", func(t *testing.T) {
		s := newStore(t)
		b := []byte("payload")
		err := s.Put(ctx, "h/obj", bytes.NewReader(b), int64(len(b)), Sum([]byte("other")))
		if !errors.Is(err, objstore.ErrHashMismatch) {
			t.Fatalf("Put = %v, want ErrHashMismatch", err)
		}
		assertAbsent(t, s, "h/obj")
		for _, bad := range []string{"", "abc", strings.ToUpper(Sum(b)), Sum(b) + "0"} {
			if err := s.Put(ctx, "h/obj", bytes.NewReader(b), int64(len(b)), bad); err == nil {
				t.Errorf("Put with hash %q succeeded", bad)
			}
		}
		assertAbsent(t, s, "h/obj")
		assertEmpty(t, s)
	})

	t.Run("SizeMismatch", func(t *testing.T) {
		s := newStore(t)
		b := []byte("payload")
		for _, size := range []int64{int64(len(b)) - 1, int64(len(b)) + 1, -1} {
			err := s.Put(ctx, "z/obj", bytes.NewReader(b), size, Sum(b))
			if !errors.Is(err, objstore.ErrSizeMismatch) && !errors.Is(err, objstore.ErrHashMismatch) {
				t.Errorf("Put(size %d) = %v, want size or hash mismatch", size, err)
			}
		}
		assertAbsent(t, s, "z/obj")
		assertEmpty(t, s)
	})

	t.Run("IdempotentPut", func(t *testing.T) {
		s := newStore(t)
		b := []byte("same content")
		MustPut(t, s, "i/obj", b)
		MustPut(t, s, "i/obj", b)
		if got := ReadAll(t, s, "i/obj"); !bytes.Equal(got, b) {
			t.Fatal("content changed")
		}
	})

	t.Run("ExistsDifferentContent", func(t *testing.T) {
		s := newStore(t)
		b := []byte("first")
		MustPut(t, s, "e/obj", b)
		for _, other := range [][]byte{[]byte("other"), []byte("first and longer")} {
			err := s.Put(ctx, "e/obj", bytes.NewReader(other), int64(len(other)), Sum(other))
			if !errors.Is(err, objstore.ErrExists) {
				t.Fatalf("Put = %v, want ErrExists", err)
			}
		}
		if got := ReadAll(t, s, "e/obj"); !bytes.Equal(got, b) {
			t.Fatal("existing object was overwritten")
		}
	})

	t.Run("ConcurrentPutSameKey", func(t *testing.T) {
		s := newStore(t)
		b := bytes.Repeat([]byte("c"), 64<<10)
		var wg sync.WaitGroup
		errs := make([]error, 8)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				errs[i] = s.Put(ctx, "c/obj", bytes.NewReader(b), int64(len(b)), Sum(b))
			}()
		}
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Errorf("Put %d: %v", i, err)
			}
		}
		if got := ReadAll(t, s, "c/obj"); !bytes.Equal(got, b) {
			t.Fatal("content differs")
		}
	})

	t.Run("InvalidKeys", func(t *testing.T) {
		s := newStore(t)
		bad := []string{
			"", "/", "/abs", "a/", "a//b", ".", "..", "a/../b", "../a", "a/..",
			"./a", "a/./b", ".hidden", "a/.tmp-x", "a/b.", "A", "a/B",
			"a\\b", "c:/x", "a b", "a\x00b", "a:b", "a*b", "ü", "con", "a/nul.txt",
			"lpt1", strings.Repeat("a", objstore.MaxSegmentLen+1),
			strings.Repeat("a/", objstore.MaxKeyLen/2) + "a",
		}
		b := []byte("x")
		for _, k := range bad {
			if err := s.Put(ctx, k, bytes.NewReader(b), 1, Sum(b)); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("Put(%q) = %v, want ErrInvalidKey", k, err)
			}
			if _, _, err := s.Get(ctx, k); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("Get(%q) = %v, want ErrInvalidKey", k, err)
			}
			if _, err := s.GetRange(ctx, k, 0, 1); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("GetRange(%q) = %v, want ErrInvalidKey", k, err)
			}
			if _, err := s.Stat(ctx, k); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("Stat(%q) = %v, want ErrInvalidKey", k, err)
			}
			if err := s.Delete(ctx, k); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("Delete(%q) = %v, want ErrInvalidKey", k, err)
			}
		}
		for _, p := range []string{"/", "/a", "a//", "../", "a/../", ".x", "a/.t", "A/", "a\\"} {
			if _, err := s.List(ctx, p, "", 0); !errors.Is(err, objstore.ErrInvalidKey) {
				t.Errorf("List(%q) = %v, want ErrInvalidKey", p, err)
			}
		}
		assertEmpty(t, s)
	})

	t.Run("ValidKeys", func(t *testing.T) {
		s := newStore(t)
		for _, k := range []string{"a", "a-b_c.d", "v/b/c/d/e", "x.y/z", "cons", "com10", strings.Repeat("a", objstore.MaxSegmentLen)} {
			MustPut(t, s, k, []byte(k))
			if got := ReadAll(t, s, k); string(got) != k {
				t.Errorf("Get(%q) = %q", k, got)
			}
		}
	})

	t.Run("Delete", func(t *testing.T) {
		s := newStore(t)
		MustPut(t, s, "d/obj", []byte("x"))
		MustPut(t, s, "d/keep", []byte("y"))
		if err := s.Delete(ctx, "d/obj"); err != nil {
			t.Fatal(err)
		}
		assertAbsent(t, s, "d/obj")
		if err := s.Delete(ctx, "d/obj"); err != nil {
			t.Fatalf("second Delete = %v, want nil", err)
		}
		if err := s.Delete(ctx, "never/existed"); err != nil {
			t.Fatalf("Delete missing = %v, want nil", err)
		}
		if got := ReadAll(t, s, "d/keep"); string(got) != "y" {
			t.Fatal("sibling affected")
		}
		// A deleted key can be written again with new content.
		MustPut(t, s, "d/obj", []byte("new"))
		if got := ReadAll(t, s, "d/obj"); string(got) != "new" {
			t.Fatal("re-put content differs")
		}
	})

	t.Run("List", func(t *testing.T) {
		s := newStore(t)
		// A key cannot also be a directory prefix of another key on every
		// backend (localfs), so the set avoids that; byte order is
		// '-' < '.' < '/' < '0'.
		keys := []string{"a0", "a-c", "a.x", "a/b/c", "a/bz", "b/1", "b/2", "ba", "repo/x/data/ab/" + oid}
		for _, k := range keys {
			MustPut(t, s, k, []byte(k))
		}
		cases := []struct {
			prefix string
			want   []string
		}{
			{"", []string{"a-c", "a.x", "a/b/c", "a/bz", "a0", "b/1", "b/2", "ba", "repo/x/data/ab/" + oid}},
			{"a", []string{"a-c", "a.x", "a/b/c", "a/bz", "a0"}},
			{"a/", []string{"a/b/c", "a/bz"}},
			{"a/b", []string{"a/b/c", "a/bz"}},
			{"a/b/", []string{"a/b/c"}},
			{"b", []string{"b/1", "b/2", "ba"}},
			{"b/", []string{"b/1", "b/2"}},
			{"repo/x/data/", []string{"repo/x/data/ab/" + oid}},
			{"nothing", nil},
			{"nothing/deeper/", nil},
		}
		for _, c := range cases {
			got := listAll(t, s, c.prefix, 0)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Errorf("List(%q) = %v, want %v", c.prefix, got, c.want)
			}
		}
		page, err := s.List(ctx, "a/", "", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, o := range page.Objects {
			if o.Size != int64(len(o.Key)) {
				t.Errorf("List size of %q = %d", o.Key, o.Size)
			}
		}
	})

	t.Run("ListPaging", func(t *testing.T) {
		s := newStore(t)
		var want []string
		for i := range 25 {
			k := fmt.Sprintf("p/%02x/obj%02d", i%4, i)
			MustPut(t, s, k, []byte{byte(i)})
			want = append(want, k)
		}
		all := listAll(t, s, "p/", 0)
		slices.Sort(want)
		if strings.Join(all, ",") != strings.Join(want, ",") {
			t.Fatalf("List = %v, want %v", all, want)
		}
		for _, limit := range []int{1, 3, 7, 25, 26} {
			got := listAll(t, s, "p/", limit)
			if strings.Join(got, ",") != strings.Join(want, ",") {
				t.Errorf("paged(%d) = %v", limit, got)
			}
		}
		page, err := s.List(ctx, "p/", want[10], 5)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Objects) != 5 || page.Objects[0].Key != want[11] || page.NextCursor != want[15] {
			t.Errorf("List after cursor = %+v", page)
		}
		page, err = s.List(ctx, "p/", want[24], 5)
		if err != nil || len(page.Objects) != 0 || page.NextCursor != "" {
			t.Errorf("List after last = %+v, %v", page, err)
		}
	})

	t.Run("CancelledContext", func(t *testing.T) {
		s := newStore(t)
		MustPut(t, s, "x/obj", []byte("x"))
		cctx, cancel := context.WithCancel(ctx)
		cancel()
		b := []byte("y")
		if err := s.Put(cctx, "x/new", bytes.NewReader(b), 1, Sum(b)); !errors.Is(err, context.Canceled) {
			t.Errorf("Put = %v", err)
		}
		if _, _, err := s.Get(cctx, "x/obj"); !errors.Is(err, context.Canceled) {
			t.Errorf("Get = %v", err)
		}
		if _, err := s.Stat(cctx, "x/obj"); !errors.Is(err, context.Canceled) {
			t.Errorf("Stat = %v", err)
		}
		if _, err := s.List(cctx, "", "", 0); !errors.Is(err, context.Canceled) {
			t.Errorf("List = %v", err)
		}
		if err := s.Delete(cctx, "x/obj"); !errors.Is(err, context.Canceled) {
			t.Errorf("Delete = %v", err)
		}
		assertAbsent(t, s, "x/new")
		ReadAll(t, s, "x/obj")
	})

	t.Run("CancelDuringPut", func(t *testing.T) {
		s := newStore(t)
		cctx, cancel := context.WithCancel(ctx)
		b := bytes.Repeat([]byte("q"), 1<<20)
		r := &cancelAfter{r: bytes.NewReader(b), n: 4096, cancel: cancel}
		if err := s.Put(cctx, "q/obj", r, int64(len(b)), Sum(b)); !errors.Is(err, context.Canceled) {
			t.Errorf("Put = %v, want context.Canceled", err)
		}
		assertAbsent(t, s, "q/obj")
		assertEmpty(t, s)
	})

	t.Run("ReaderError", func(t *testing.T) {
		s := newStore(t)
		boom := errors.New("boom")
		r := io.MultiReader(strings.NewReader("part"), errReader{boom})
		if err := s.Put(ctx, "f/obj", r, 10, Sum([]byte("0123456789"))); !errors.Is(err, boom) {
			t.Errorf("Put = %v, want reader error", err)
		}
		assertAbsent(t, s, "f/obj")
		assertEmpty(t, s)
	})
}

func assertAbsent(t *testing.T, s objstore.Store, key string) {
	t.Helper()
	if _, err := s.Stat(context.Background(), key); !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("Stat(%q) = %v, want ErrNotFound", key, err)
	}
}

func assertEmpty(t *testing.T, s objstore.Store) {
	t.Helper()
	if keys := listAll(t, s, "", 0); len(keys) != 0 {
		t.Fatalf("store not empty: %v", keys)
	}
}

func listAll(t *testing.T, s objstore.Store, prefix string, limit int) []string {
	t.Helper()
	var keys []string
	cursor := ""
	for range 10000 {
		page, err := s.List(context.Background(), prefix, cursor, limit)
		if err != nil {
			t.Fatalf("List(%q, %q): %v", prefix, cursor, err)
		}
		if limit > 0 && len(page.Objects) > limit {
			t.Fatalf("List returned %d > limit %d", len(page.Objects), limit)
		}
		for _, o := range page.Objects {
			if len(keys) > 0 && o.Key <= keys[len(keys)-1] {
				t.Fatalf("List out of order: %q after %q", o.Key, keys[len(keys)-1])
			}
			keys = append(keys, o.Key)
		}
		if page.NextCursor == "" {
			return keys
		}
		cursor = page.NextCursor
	}
	t.Fatal("List did not terminate")
	return nil
}

type cancelAfter struct {
	r      io.Reader
	n      int
	cancel func()
}

func (c *cancelAfter) Read(p []byte) (int, error) {
	if c.n <= 0 {
		c.cancel()
	}
	if len(p) > 1024 {
		p = p[:1024]
	}
	n, err := c.r.Read(p)
	c.n -= n
	return n, err
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }
