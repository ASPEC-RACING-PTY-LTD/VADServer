package store_test

import (
	"context"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// An edited file synced again keeps its identity: same node, dates, tags and
// folder, whatever the folder's conflict setting, and no numbered copy.
func TestCommitUpdatesInPlace(t *testing.T) {
	st, l := openStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.NewUser{Username: "clips", PasswordHash: "x", Role: "user", LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	dir := []string{"Videos", "Rocket League"}
	const born = int64(1_700_000_000_000)
	commit := func(r store.CommitRequest) *store.CommitResult {
		t.Helper()
		r.User, r.LocationID = u, l.ID
		if r.DirSegs == nil {
			r.DirSegs = dir
		}
		if r.Name == "" {
			r.Name = "clip.mp4"
		}
		res, err := st.CommitFile(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	first := commit(store.CommitRequest{Size: 100, SHA256: sha('a'), ModifiedAt: born + 5000, OnConflict: "rename", Tags: []string{"clip"}, FileCreatedAt: born})
	if first.Result != "created" || first.Node.FileCreatedAt == nil || *first.Node.FileCreatedAt != born {
		t.Fatalf("create: %+v", first)
	}
	id, added := first.Node.ID, first.Node.CreatedAt
	// The user tags it on the server.
	if err := st.SetTags(ctx, u.ID, id, []string{"clip", "best"}); err != nil {
		t.Fatal(err)
	}

	// Edited three times: each upload names the content it replaces.
	base := sha('a')
	for i, c := range []byte{'b', 'c', 'd'} {
		res := commit(store.CommitRequest{Size: int64(110 + i), SHA256: sha(c), ModifiedAt: born + int64(10000*(i+1)), OnConflict: "rename", BaseSHA256: base, FileCreatedAt: born + 99999})
		n := res.Node
		if res.Result != "replaced" || n.ID != id || n.Name != "clip.mp4" || n.CreatedAt != added || *n.FileCreatedAt != born || n.ModifiedAt != born+int64(10000*(i+1)) {
			t.Fatalf("edit %d: result %s node %+v", i, res.Result, n)
		}
		if len(n.Tags) != 2 {
			t.Fatalf("edit %d lost tags: %v", i, n.Tags)
		}
		if len(res.Freed) != 1 || res.Freed[0].SHA256 != base {
			t.Fatalf("edit %d freed %v, want the old content", i, res.Freed)
		}
		base = sha(c)
	}
	// The same content again (a restart or Sync now): nothing changes.
	if res := commit(store.CommitRequest{Size: 112, SHA256: sha('d'), ModifiedAt: born + 30000, OnConflict: "rename", BaseSHA256: sha('d')}); res.Result != "unchanged" || res.Node.ID != id {
		t.Fatalf("again: %+v", res)
	}
	kids := listNames(t, st, u, dir)
	if len(kids) != 1 || kids[0] != "clip.mp4" {
		t.Fatalf("folder holds %v, want just clip.mp4", kids)
	}
	if stored, actual, err := st.UsageDrift(ctx, u.ID); err != nil || stored != 112 || actual != 112 {
		t.Fatalf("usage %d/%d %v", stored, actual, err)
	}

	// A different file at the path (not this device's version) is still a
	// conflict: "rename" keeps both.
	if res := commit(store.CommitRequest{Size: 5, SHA256: sha('x'), ModifiedAt: born, OnConflict: "rename", BaseSHA256: sha('a')}); res.Result != "renamed" || res.Node.ID == id {
		t.Fatalf("conflict: %+v", res)
	}
	// Without a base (older clients), "replace" also updates in place.
	if res := commit(store.CommitRequest{Size: 7, SHA256: sha('e'), ModifiedAt: born, OnConflict: "replace"}); res.Result != "replaced" || res.Node.ID != id || res.Node.CreatedAt != added {
		t.Fatalf("replace: %+v", res)
	}
	// KeepReplaced keeps the old content as a trashed copy.
	res := commit(store.CommitRequest{Size: 8, SHA256: sha('f'), ModifiedAt: born, OnConflict: "replace", BaseSHA256: sha('e'), KeepReplaced: true})
	if res.Node.ID != id || len(res.Freed) != 0 {
		t.Fatalf("keep: %+v", res)
	}
	trash, err := st.ListTrash(ctx, u.ID)
	if err != nil || len(trash) != 1 || trash[0].SHA256 != sha('e') || trash[0].Name != "clip.mp4" {
		t.Fatalf("trash %+v %v", trash, err)
	}
	if stored, actual, err := st.UsageDrift(ctx, u.ID); err != nil || stored != actual {
		t.Fatalf("usage drift %d/%d %v", stored, actual, err)
	}
}

// A file renamed or moved on the device moves its server copy (same node)
// instead of being stored again; an edit made with the rename applies too.
func TestCommitFollowsRenames(t *testing.T) {
	st, l := openStore(t)
	ctx := context.Background()
	u, err := st.CreateUser(ctx, store.NewUser{Username: "mover", PasswordHash: "x", Role: "user", LocationID: l.ID})
	if err != nil {
		t.Fatal(err)
	}
	commit := func(r store.CommitRequest) *store.CommitResult {
		t.Helper()
		r.User, r.LocationID, r.OnConflict = u, l.ID, "rename"
		res, err := st.CommitFile(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	orig := commit(store.CommitRequest{DirSegs: []string{"Clips"}, Name: "a.mp4", Size: 10, SHA256: sha('a'), ModifiedAt: 1000, FileCreatedAt: 500})
	id := orig.Node.ID

	// Renamed, same content: moved.
	res := commit(store.CommitRequest{DirSegs: []string{"Clips"}, Name: "goal.mp4", Size: 10, SHA256: sha('a'), ModifiedAt: 1000, BaseSHA256: sha('a'), PrevSegs: []string{"Clips", "a.mp4"}})
	if res.Result != "moved" || res.Node.ID != id || res.Node.Name != "goal.mp4" {
		t.Fatalf("rename: %+v", res)
	}
	// Moved into a nested folder and edited on the way.
	res = commit(store.CommitRequest{DirSegs: []string{"Clips", "2026", "Best"}, Name: "goal.mp4", Size: 12, SHA256: sha('b'), ModifiedAt: 2000, BaseSHA256: sha('a'), PrevSegs: []string{"Clips", "goal.mp4"}})
	if res.Result != "replaced" || res.Node.ID != id || res.Node.CreatedAt != orig.Node.CreatedAt || *res.Node.FileCreatedAt != 500 {
		t.Fatalf("move+edit: %+v", res)
	}
	if got := listNames(t, st, u, []string{"Clips"}); len(got) != 1 || got[0] != "2026" {
		t.Fatalf("old folder holds %v", got)
	}
	// Retried after a restart: the old path is gone and the file is there.
	res = commit(store.CommitRequest{DirSegs: []string{"Clips", "2026", "Best"}, Name: "goal.mp4", Size: 12, SHA256: sha('b'), ModifiedAt: 2000, BaseSHA256: sha('a'), PrevSegs: []string{"Clips", "goal.mp4"}})
	if res.Result != "unchanged" || res.Node.ID != id {
		t.Fatalf("retry: %+v", res)
	}
	// The old path holds other content now: it is not taken; a new file.
	other := commit(store.CommitRequest{DirSegs: []string{"Clips"}, Name: "c.mp4", Size: 3, SHA256: sha('c'), ModifiedAt: 1})
	res = commit(store.CommitRequest{DirSegs: []string{"Clips"}, Name: "d.mp4", Size: 4, SHA256: sha('d'), ModifiedAt: 1, BaseSHA256: sha('z'), PrevSegs: []string{"Clips", "c.mp4"}})
	if res.Result != "created" || res.Node.ID == other.Node.ID {
		t.Fatalf("foreign previous: %+v", res)
	}
	// The new path already exists: the rename does not overwrite it.
	res = commit(store.CommitRequest{DirSegs: []string{"Clips"}, Name: "d.mp4", Size: 3, SHA256: sha('c'), ModifiedAt: 1, BaseSHA256: sha('c'), PrevSegs: []string{"Clips", "c.mp4"}})
	if res.Result != "renamed" {
		t.Fatalf("occupied target: %+v", res)
	}
	if stored, actual, err := st.UsageDrift(ctx, u.ID); err != nil || stored != actual {
		t.Fatalf("usage drift %d/%d %v", stored, actual, err)
	}
}

func listNames(t *testing.T, st *store.Store, u *store.User, segs []string) []string {
	t.Helper()
	ctx := context.Background()
	dir, err := st.Resolve(ctx, u, segs)
	if err != nil {
		t.Fatal(err)
	}
	kids, _, err := st.ListChildren(ctx, u.ID, dir.ID, store.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, k := range kids {
		out = append(out, k.Name)
	}
	return out
}
