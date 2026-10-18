package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestRotateServerKeyConflict(t *testing.T) {
	ctx := context.Background()
	st, _ := openStore(t)
	k := func(v uint32) *store.ServerKey {
		return &store.ServerKey{Kind: store.ServerKeySEK, Version: v, Public: []byte{1}, PrivateSealed: []byte{2}}
	}
	if err := st.RotateServerKey(ctx, k(1)); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rotate without current: %v", err)
	}
	if err := st.InsertServerKeys(ctx, k(1)); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertServerKeys(ctx, k(2)); err == nil {
		t.Fatal("second current key inserted")
	}
	if err := st.RotateServerKey(ctx, k(3)); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("skipped version: %v", err)
	}
	if err := st.RotateServerKey(ctx, k(2)); err != nil {
		t.Fatal(err)
	}
	if cur, _ := st.CurrentServerKey(ctx, store.ServerKeySEK); cur.Version != 2 {
		t.Fatalf("current: %d", cur.Version)
	}
	if old, err := st.ServerKeyVersion(ctx, store.ServerKeySEK, 1); err != nil || old.RetiredAt == nil {
		t.Fatalf("retired v1: %+v %v", old, err)
	}
}
