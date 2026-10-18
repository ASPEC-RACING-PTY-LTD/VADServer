package store_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const testRepoUUID = "0b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"

func newTestRepo(t *testing.T, st *store.Store, userID, deviceID int64, uuid string) *store.Repository {
	t.Helper()
	dest, _, err := st.ServerDestination(context.Background(), "/srv/backup")
	if err != nil {
		t.Fatal(err)
	}
	r := &store.Repository{UserID: userID, DestinationID: dest, RepoUUID: uuid, Name: "PC", CreatedByDeviceID: deviceID}
	if _, err := st.CreateRepository(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestServerDestinationOnDemand(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	id, root, err := st.ServerDestination(ctx, "/srv/a/backup")
	if err != nil || id == 0 || root != "/srv/a/backup" {
		t.Fatalf("first: %d %q %v", id, root, err)
	}
	// The root is fixed at creation; later defaults do not move it.
	id2, root2, err := st.ServerDestination(ctx, "/srv/b/backup")
	if err != nil || id2 != id || root2 != "/srv/a/backup" {
		t.Fatalf("second: %d %q %v", id2, root2, err)
	}
	u := newUser(t, st, "alice")
	kind, root3, err := st.DestinationTarget(ctx, id, u.ID)
	if err != nil || kind != "server" || root3 != root {
		t.Fatalf("target: %s %q %v", kind, root3, err)
	}
	if _, _, err := st.DestinationTarget(ctx, id+100, u.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown destination: %v", err)
	}
}

func TestRepositoryCreateIdempotentAndScoped(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	alice, bob := newUser(t, st, "alice"), newUser(t, st, "bob")
	d := newProfileDevice(t, st, alice.ID)
	r := newTestRepo(t, st, alice.ID, d.ID, testRepoUUID)
	if r.ID == 0 || r.EscrowMode != "full" || r.FormatVersion != 1 || r.CurrentEpoch != 1 {
		t.Fatalf("created: %+v", r)
	}

	// Same uuid, same user: the stored row comes back.
	again := &store.Repository{UserID: alice.ID, DestinationID: r.DestinationID, RepoUUID: testRepoUUID, Name: "other"}
	created, err := st.CreateRepository(ctx, again)
	if err != nil || created || again.ID != r.ID || again.Name != "PC" || again.CreatedByDeviceID != d.ID {
		t.Fatalf("replay: %v %v %+v", created, err, again)
	}
	// Same uuid, other user.
	foreign := &store.Repository{UserID: bob.ID, DestinationID: r.DestinationID, RepoUUID: testRepoUUID, Name: "x"}
	if _, err := st.CreateRepository(ctx, foreign); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("foreign uuid: %v", err)
	}
	// One repo per (user, destination).
	second := &store.Repository{UserID: alice.ID, DestinationID: r.DestinationID, RepoUUID: "1b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d", Name: "x"}
	if _, err := st.CreateRepository(ctx, second); !errors.Is(err, store.ErrRepoExists) || second.ID != r.ID {
		t.Fatalf("second repo: %v id %d", err, second.ID)
	}
	// Bob gets his own on the same destination.
	newTestRepo(t, st, bob.ID, 0, "2b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d")

	list, err := st.ListRepositories(ctx, alice.ID, 0, 0)
	if err != nil || len(list) != 1 || list[0].ID != r.ID {
		t.Fatalf("alice list: %+v %v", list, err)
	}
	if list, err := st.ListRepositories(ctx, alice.ID, r.ID, 0); err != nil || len(list) != 0 {
		t.Fatalf("after cursor: %+v %v", list, err)
	}
	if _, err := st.Repository(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing repo: %v", err)
	}
}

func TestRepoObjectsUpsertMissingUsageQuota(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	r := newTestRepo(t, st, u.ID, 0, testRepoUUID)

	existed, err := st.PutRepoObject(ctx, r.ID, "data", sha('a'), 100)
	if err != nil || existed {
		t.Fatalf("first put: %v %v", existed, err)
	}
	if existed, err = st.PutRepoObject(ctx, r.ID, "data", sha('a'), 100); err != nil || !existed {
		t.Fatalf("replay put: %v %v", existed, err)
	}
	if _, err := st.PutRepoObject(ctx, r.ID, "index", sha('b'), 50); err != nil {
		t.Fatal(err)
	}
	if o, err := st.RepoObject(ctx, r.ID, "data", sha('a')); err != nil || o.Size != 100 {
		t.Fatalf("object: %+v %v", o, err)
	}
	if _, err := st.RepoObject(ctx, r.ID, "index", sha('a')); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("other kind: %v", err)
	}

	// Pending-delete objects count as missing until re-PUT.
	if _, err := st.DB.Exec("UPDATE repo_objects SET delete_pending_at = 1 WHERE oid = ?", sha('a')); err != nil {
		t.Fatal(err)
	}
	missing, err := st.MissingRepoObjects(ctx, r.ID, "data", []string{sha('a'), sha('c'), sha('a'), sha('b')})
	if err != nil || fmt.Sprint(missing) != fmt.Sprint([]string{sha('a'), sha('c'), sha('b')}) {
		t.Fatalf("missing: %v %v", missing, err)
	}
	if _, err := st.RepoObject(ctx, r.ID, "data", sha('a')); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("pending object visible: %v", err)
	}
	if existed, err = st.PutRepoObject(ctx, r.ID, "data", sha('a'), 100); err != nil || existed {
		t.Fatalf("re-put pending: %v %v", existed, err)
	}
	if missing, err := st.MissingRepoObjects(ctx, r.ID, "data", []string{sha('a')}); err != nil || len(missing) != 0 {
		t.Fatalf("after re-put: %v %v", missing, err)
	}

	// Lists page by id and filter by kind.
	all, err := st.ListRepoObjects(ctx, r.ID, "", 0, 1)
	if err != nil || len(all) != 1 {
		t.Fatalf("page 1: %+v %v", all, err)
	}
	rest, err := st.ListRepoObjects(ctx, r.ID, "", all[0].ID, 0)
	if err != nil || len(rest) != 1 {
		t.Fatalf("page 2: %+v %v", rest, err)
	}
	if idx, err := st.ListRepoObjects(ctx, r.ID, "index", 0, 0); err != nil || len(idx) != 1 || idx[0].OID != sha('b') {
		t.Fatalf("index list: %+v %v", idx, err)
	}

	usage, err := st.RepositoryUsage(ctx, r.ID)
	if err != nil || usage.Objects != 2 || usage.StoredBytes != 150 || usage.Snapshots != 0 {
		t.Fatalf("usage: %+v %v", usage, err)
	}

	// Quota: 0 is unlimited; otherwise stored + new must fit.
	if err := st.CheckBackupQuota(ctx, u.ID, 1<<40); err != nil {
		t.Fatalf("unlimited: %v", err)
	}
	if _, err := st.DB.Exec("UPDATE users SET backup_quota_bytes = 200 WHERE id = ?", u.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.CheckBackupQuota(ctx, u.ID, 50); err != nil {
		t.Fatalf("fits: %v", err)
	}
	if err := st.CheckBackupQuota(ctx, u.ID, 51); !errors.Is(err, store.ErrBackupQuota) {
		t.Fatalf("over quota: %v", err)
	}
}

func TestHoldsExclusiveRepoLock(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	r := newTestRepo(t, st, u.ID, d.ID, testRepoUUID)
	future := store.Now() + 60_000
	lock := func(id, kind string, dev any, holder string, exp int64) {
		t.Helper()
		if _, err := st.DB.Exec(`INSERT INTO repo_locks(id, repo_id, kind, holder_kind, holder_device_id, holder, acquired_at, refreshed_at, expires_at)
			VALUES(?,?,?,?,?,?,1,1,?)`, id, r.ID, kind, map[bool]string{true: "device", false: "server"}[dev != nil], dev, holder, exp); err != nil {
			t.Fatal(err)
		}
	}

	held, other, err := st.HoldsExclusiveRepoLock(ctx, r.ID, d.ID)
	if err != nil || held || other != nil {
		t.Fatalf("no locks: %v %+v %v", held, other, err)
	}
	lock("expired", "exclusive", d.ID, "{}", 1)
	lock("shared", "shared", d.ID, "{}", future)
	held, other, err = st.HoldsExclusiveRepoLock(ctx, r.ID, d.ID)
	if err != nil || held || other == nil || other.Holder != fmt.Sprintf("device %d", d.ID) || other.ExpiresAt != future {
		t.Fatalf("shared only: %v %+v %v", held, other, err)
	}
	lock("server", "exclusive", nil, "maintenance", future+1)
	if held, other, _ = st.HoldsExclusiveRepoLock(ctx, r.ID, d.ID); held || other.Holder != "maintenance" {
		t.Fatalf("server exclusive: %v %+v", held, other)
	}
	lock("mine", "exclusive", d.ID, "{}", future)
	if held, _, err = st.HoldsExclusiveRepoLock(ctx, r.ID, d.ID); err != nil || !held {
		t.Fatalf("held: %v %v", held, err)
	}
}
