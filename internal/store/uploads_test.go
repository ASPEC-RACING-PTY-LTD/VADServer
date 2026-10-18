package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func newUploadStore(t *testing.T) (*Store, *User) {
	t.Helper()
	ctx := context.Background()
	st, err := Open(ctx, filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.CreateUser(ctx, NewUser{Username: "alice", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return st, u
}

func addUpload(t *testing.T, st *Store, u *User, id string, chunks, expires int64) *Upload {
	t.Helper()
	t0 := now()
	up := &Upload{ID: id, UserID: u.ID, Path: "/" + id, Size: chunks * 4, ChunkSize: 4, ChunksTotal: chunks, LocationID: 1,
		ModifiedAt: t0, Tags: []string{}, OnConflict: "replace", CreatedAt: t0, UpdatedAt: t0, ExpiresAt: expires}
	if err := st.CreateUpload(context.Background(), up); err != nil {
		t.Fatal(err)
	}
	return up
}

func TestMarkChunkBoundsAndStatus(t *testing.T) {
	ctx := context.Background()
	st, u := newUploadStore(t)
	up := addUpload(t, st, u, "up-bounds", 3, now()+60_000)
	exp := now() + 60_000

	for _, tc := range []struct {
		name    string
		idx     int64
		added   bool
		wantErr error
	}{
		{"first", 1, true, nil},
		{"retry", 1, false, nil},
		{"negative", -1, false, ErrConflict},
		{"beyond bitmap", 8, false, ErrConflict},
	} {
		added, err := st.MarkChunk(ctx, u.ID, up.ID, tc.idx, 4, exp)
		if tc.wantErr != nil {
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("%s: err %v want %v", tc.name, err, tc.wantErr)
			}
			continue
		}
		if err != nil || added != tc.added {
			t.Fatalf("%s: added=%v err=%v want added=%v", tc.name, added, err, tc.added)
		}
	}
	got, _ := st.Upload(ctx, u.ID, up.ID)
	if got.BytesReceived != 4 {
		t.Fatalf("retried chunk counted twice: bytes_received=%d", got.BytesReceived)
	}

	if ok, err := st.SetUploadStatus(ctx, up.ID, "open", "finalizing"); !ok || err != nil {
		t.Fatalf("set finalizing: %v %v", ok, err)
	}
	if _, err := st.MarkChunk(ctx, u.ID, up.ID, 0, 4, exp); !errors.Is(err, ErrConflict) {
		t.Fatalf("chunk on finalizing upload: err %v want ErrConflict", err)
	}
}

func TestExpiredUploadsSkipsFinalizing(t *testing.T) {
	ctx := context.Background()
	st, u := newUploadStore(t)
	past := now() - 1000
	addUpload(t, st, u, "expired-open", 1, past)
	addUpload(t, st, u, "expired-final", 1, past)
	addUpload(t, st, u, "live-done", 1, now()+60_000)
	addUpload(t, st, u, "live-open", 1, now()+60_000)
	if ok, _ := st.SetUploadStatus(ctx, "expired-final", "open", "finalizing"); !ok {
		t.Fatal("set finalizing")
	}
	if ok, _ := st.SetUploadStatus(ctx, "live-done", "open", "done"); !ok {
		t.Fatal("set done")
	}
	list, err := st.ExpiredUploads(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, x := range list {
		got[x.ID] = true
	}
	want := map[string]bool{"expired-open": true, "live-done": true}
	if len(got) != len(want) || !got["expired-open"] || !got["live-done"] {
		t.Fatalf("expired uploads = %v, want %v", got, want)
	}
}

func TestDeleteUploadUnlessFinalizing(t *testing.T) {
	ctx := context.Background()
	st, u := newUploadStore(t)
	addUpload(t, st, u, "del-open", 1, now()+60_000)
	addUpload(t, st, u, "del-final", 1, now()+60_000)
	if ok, _ := st.SetUploadStatus(ctx, "del-final", "open", "finalizing"); !ok {
		t.Fatal("set finalizing")
	}
	for _, tc := range []struct {
		id      string
		deleted bool
	}{
		{"del-open", true},
		{"del-open", false}, // already gone
		{"del-final", false},
	} {
		ok, err := st.DeleteUploadUnlessFinalizing(ctx, tc.id)
		if err != nil || ok != tc.deleted {
			t.Fatalf("%s: deleted=%v err=%v want %v", tc.id, ok, err, tc.deleted)
		}
	}
	if _, err := st.UploadAny(ctx, "del-final"); err != nil {
		t.Fatalf("finalizing upload was deleted: %v", err)
	}
}
