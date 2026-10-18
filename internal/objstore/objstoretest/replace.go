package objstoretest

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
)

// ReplaceableStore is a Store that also implements objstore.Replacer.
type ReplaceableStore interface {
	objstore.Store
	objstore.Replacer
}

// RunReplacer runs the conformance suite of the optional Replacer
// capability against stores made by newStore.
func RunReplacer(t *testing.T, newStore func(t *testing.T) ReplaceableStore) {
	ctx := context.Background()
	key := repo + "/config"
	replace := func(s ReplaceableStore, b []byte) error {
		return s.Replace(ctx, key, bytes.NewReader(b), int64(len(b)), Sum(b))
	}

	t.Run("CreateAndReplace", func(t *testing.T) {
		s := newStore(t)
		if err := replace(s, []byte("v1")); err != nil {
			t.Fatal(err)
		}
		if got := ReadAll(t, s, key); string(got) != "v1" {
			t.Fatalf("after create = %q", got)
		}
		if err := replace(s, []byte("version two")); err != nil {
			t.Fatal(err)
		}
		if got := ReadAll(t, s, key); string(got) != "version two" {
			t.Fatalf("after replace = %q", got)
		}
		// Identical content is fine too.
		if err := replace(s, []byte("version two")); err != nil {
			t.Fatal(err)
		}
		// Put of the replaced content stays idempotent; other content still
		// conflicts.
		MustPut(t, s, key, []byte("version two"))
		if err := s.Put(ctx, key, bytes.NewReader([]byte("v1")), 2, Sum([]byte("v1"))); !errors.Is(err, objstore.ErrExists) {
			t.Fatalf("Put after replace = %v, want ErrExists", err)
		}
	})

	t.Run("MismatchKeepsOld", func(t *testing.T) {
		s := newStore(t)
		MustPut(t, s, key, []byte("old"))
		b := []byte("new")
		if err := s.Replace(ctx, key, bytes.NewReader(b), 3, Sum([]byte("other"))); !errors.Is(err, objstore.ErrHashMismatch) {
			t.Fatalf("hash mismatch = %v", err)
		}
		if err := s.Replace(ctx, key, bytes.NewReader(b), 4, Sum(b)); !errors.Is(err, objstore.ErrSizeMismatch) {
			t.Fatalf("size mismatch = %v", err)
		}
		if got := ReadAll(t, s, key); string(got) != "old" {
			t.Fatalf("content after failed replace = %q", got)
		}
		page, err := s.List(ctx, repo+"/", "", 0)
		if err != nil || len(page.Objects) != 1 {
			t.Fatalf("List = %+v, %v (temp files must not be listed)", page, err)
		}
	})

	t.Run("InvalidKey", func(t *testing.T) {
		s := newStore(t)
		if err := s.Replace(ctx, "../x", bytes.NewReader(nil), 0, Sum(nil)); !errors.Is(err, objstore.ErrInvalidKey) {
			t.Fatalf("Replace(../x) = %v", err)
		}
	})
}
