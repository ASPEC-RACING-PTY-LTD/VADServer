package api_test

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
)

func TestRepoLocks(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	dc2 := e.enrollDevice(alice, "alice", "password-alice")
	bc := e.enrollDevice(bob, "bob", "password-bob")
	if dc2.DeviceID == dc.DeviceID {
		t.Fatal("second device not distinct")
	}
	ctx := context.Background()
	repo, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	locksPath := fmt.Sprintf("/api/v1/repos/%d/locks", repo.ID)

	// Validation, other users and sessions.
	for _, body := range []wire.LockRequest{{Kind: "both", TTLSeconds: 60}, {Kind: "shared", TTLSeconds: -1}, {Kind: "shared", TTLSeconds: 7200}} {
		if st, code := apiCode(t, dc.Call(ctx, "POST", locksPath, body, nil)); st != 422 || code != "validation" {
			t.Fatalf("bad lock %+v: %d %s", body, st, code)
		}
	}
	if _, err := bc.AcquireLock(ctx, repo.ID, "shared", 60); err == nil {
		t.Fatal("other user locked the repo")
	} else if st, code := apiCode(t, err); st != 404 || code != wire.CodeRepoNotFound {
		t.Fatalf("other user lock: %d %s", st, code)
	}
	if code, out := alice.req("POST", locksPath, map[string]any{"kind": "shared", "ttl_seconds": 60}); code != 403 || errCode(out) != "device_required" {
		t.Fatalf("session lock: %d %v", code, out)
	}

	// Shared locks coexist; exclusive conflicts with them.
	start := time.Now().UnixMilli()
	s1, err := dc.AcquireLock(ctx, repo.ID, "shared", 60)
	if err != nil || s1.LockID == "" || s1.ExpiresAt < start+59_000 {
		t.Fatalf("shared 1: %+v %v", s1, err)
	}
	s2, err := dc2.AcquireLock(ctx, repo.ID, "shared", 0) // default TTL
	if err != nil || s2.ExpiresAt < start+299_000 {
		t.Fatalf("shared 2: %+v %v", s2, err)
	}
	_, err = dc2.AcquireLock(ctx, repo.ID, "exclusive", 60)
	if d, ok := client.RepoLocked(err); !ok || !strings.HasPrefix(d.Holder, "device ") || d.ExpiresAt <= start {
		t.Fatalf("exclusive over shared: %v %+v", err, d)
	}

	// Refresh and release: foreign, unknown and expired locks are 404.
	if _, err := dc2.RefreshLock(ctx, repo.ID, s1.LockID, 60); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("refresh foreign lock: %v", err)
	}
	if err := dc2.ReleaseLock(ctx, repo.ID, s1.LockID); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("release foreign lock: %v", err)
	}
	if _, err := dc.RefreshLock(ctx, repo.ID, "nope", 60); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("refresh unknown: %v", err)
	}
	if err := dc.ReleaseLock(ctx, repo.ID, "nope"); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("release unknown: %v", err)
	}
	r1, err := dc.RefreshLock(ctx, repo.ID, s1.LockID, 600)
	if err != nil || r1 == nil || r1.LockID != s1.LockID || r1.ExpiresAt < start+599_000 {
		t.Fatalf("refresh: %+v %v", r1, err)
	}
	if r1, err = dc.RefreshLock(ctx, repo.ID, s1.LockID, 0); err != nil || r1 == nil || r1.ExpiresAt < start+599_000 {
		t.Fatalf("refresh keeps ttl: %+v %v", r1, err)
	}
	if _, err := e.st.DB.Exec("UPDATE repo_locks SET expires_at = 1 WHERE id = ?", s2.LockID); err != nil {
		t.Fatal(err)
	}
	if _, err := dc2.RefreshLock(ctx, repo.ID, s2.LockID, 60); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("refresh expired: %v", err)
	}
	if err := dc.ReleaseLock(ctx, repo.ID, s1.LockID); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := dc.ReleaseLock(ctx, repo.ID, s1.LockID); !client.IsCode(err, wire.CodeLockNotFound) {
		t.Fatalf("double release: %v", err)
	}

	// Exclusive lock: blocks shared and exclusive from any device, then
	// guards the config PUT end to end (P1-14).
	x, err := dc.AcquireLock(ctx, repo.ID, "exclusive", 60)
	if err != nil {
		t.Fatalf("exclusive: %v", err)
	}
	for _, c := range []*client.Client{dc, dc2} {
		_, err = c.AcquireLock(ctx, repo.ID, "shared", 60)
		if d, ok := client.RepoLocked(err); !ok || d.Holder != fmt.Sprintf("device %d", dc.DeviceID) || d.ExpiresAt != x.ExpiresAt {
			t.Fatalf("shared over exclusive: %v %+v", err, d)
		}
	}
	cfg := []byte(`{"format":"vaultdock.repo","v":1,"escrow_mode":"full"}`)
	put := func(c *client.Client) (bool, error) {
		return c.PutObject(ctx, repo.ID, "config", wire.ObjectConfigOID, bytes.NewReader(cfg), int64(len(cfg)), protocol.SHA256Hex(cfg))
	}
	if created, err := put(dc); err != nil || !created {
		t.Fatalf("config under lock: %v %v", created, err)
	}
	if _, err := put(dc2); err == nil {
		t.Fatal("config without the lock accepted")
	} else if d, ok := client.RepoLocked(err); !ok || d.Holder != fmt.Sprintf("device %d", dc.DeviceID) {
		t.Fatalf("config by other device: %v %+v", err, d)
	}
	if err := dc.ReleaseLock(ctx, repo.ID, x.LockID); err != nil {
		t.Fatal(err)
	}
	if _, err := put(dc); err == nil {
		t.Fatal("config after release accepted")
	}
	// After release the other device can lock exclusively.
	x2, err := dc2.AcquireLock(ctx, repo.ID, "exclusive", 60)
	if err != nil {
		t.Fatalf("exclusive after release: %v", err)
	}
	// The expiry sweep removes expired locks; an expired exclusive lock no
	// longer blocks.
	if _, err := e.st.DB.Exec("UPDATE repo_locks SET expires_at = 1 WHERE id = ?", x2.LockID); err != nil {
		t.Fatal(err)
	}
	if n, err := backup.NewLocks(e.st).Sweep(ctx); err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	if _, err := dc.AcquireLock(ctx, repo.ID, "shared", 60); err != nil {
		t.Fatalf("shared after expiry: %v", err)
	}
}

func TestSnapshotCommitConsentAndCatalog(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	info := admin.must("GET", "/api/v1/info", nil, 200)
	hasBackup := false
	for _, f := range info["features"].([]any) {
		hasBackup = hasBackup || f == wire.FeatureBackup
	}
	if !hasBackup {
		t.Fatalf("backup.v1 not advertised: %v", info["features"])
	}

	repo, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	putObj := func(kind string, b []byte) string {
		t.Helper()
		oid := protocol.SHA256Hex(b)
		if _, err := dc.PutObject(ctx, repo.ID, kind, oid, bytes.NewReader(b), int64(len(b)), oid); err != nil {
			t.Fatalf("put %s: %v", kind, err)
		}
		return oid
	}
	root := putObj("snapshots", []byte("sealed snapshot root"))
	idx := putObj("index", []byte("sealed index"))
	pack := putObj("data", []byte("pack bytes"))
	absent := protocol.SHA256Hex([]byte("never uploaded"))

	// Profiles: one consented, one blacklisted.
	sid, badSID := "S-1-5-21-1-2-3-1001", "S-1-5-21-1-2-3-1002"
	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sid, Username: "alice", ProfilePath: `C:\Users\alice`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sid, ConsentAt: 1000, ProofDigest: strings.Repeat("a", 64)}},
	}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/profiles", rep, nil); err != nil {
		t.Fatal(err)
	}
	if err := dc.Call(ctx, "POST", "/api/v1/device/profiles/"+badSID+"/security-event",
		map[string]any{"signal": "password_reset", "observed_at": 1, "evidence": "eventlog_4724"}, nil); err != nil {
		t.Fatal(err)
	}

	job, err := dc.CreateJob(ctx, folderJob("Documents"))
	if err != nil {
		t.Fatal(err)
	}
	runID, err := dc.StartRun(ctx, job.ID, wire.RunStartRequest{ClientRunUUID: "run-1", Trigger: wire.RunTriggerSchedule})
	if err != nil {
		t.Fatal(err)
	}

	req := wire.SnapshotCommitRequest{SnapshotOID: root, JobID: job.ID, RunID: runID, Mode: "full",
		Hostname: "TEST-PC", OSUser: "alice", UserSID: sid, ProfilePath: `C:\Users\alice`, ClientTime: 5, Tags: []string{"daily"},
		Stats: wire.SnapshotStats{FileCount: 3, DirCount: 1, LogicalBytes: 300, NewBytes: 10, NewObjects: 1}, IndexOIDs: []string{idx},
		PackOIDs: []string{pack, absent}, Epoch: 1, ProfileConsentID: "c1"}
	commit := func(r wire.SnapshotCommitRequest) (int64, bool, error) {
		resp, created, err := dc.CommitSnapshot(ctx, repo.ID, r)
		if err != nil {
			return 0, false, err
		}
		return resp.SnapshotID, created, nil
	}
	codeOf := func(r wire.SnapshotCommitRequest) (int, string) {
		t.Helper()
		_, _, err := commit(r)
		return apiCode(t, err)
	}

	// Consent: unknown profile, wrong consent id, blacklisted profile.
	bad := req
	bad.UserSID = "S-1-5-21-1-2-3-9999"
	if st, code := codeOf(bad); st != 403 || code != wire.CodeProfileNotAuthorised {
		t.Fatalf("unknown profile: %d %s", st, code)
	}
	bad = req
	bad.ProfileConsentID = "old"
	if st, code := codeOf(bad); st != 403 || code != wire.CodeProfileNotAuthorised {
		t.Fatalf("stale consent: %d %s", st, code)
	}
	bad = req
	bad.UserSID = badSID
	if st, code := codeOf(bad); st != 423 || code != wire.CodeProfileBlacklisted {
		t.Fatalf("blacklisted profile: %d %s", st, code)
	}
	// Shape errors.
	bad = req
	bad.SnapshotOID = "xyz"
	if st, code := codeOf(bad); st != 400 || code != wire.CodeInvalidOID {
		t.Fatalf("bad oid: %d %s", st, code)
	}
	bad = req
	bad.Epoch = 0
	if st, code := codeOf(bad); st != 422 || code != "validation" {
		t.Fatalf("bad epoch: %d %s", st, code)
	}
	bad = req
	bad.RunID = runID + 100
	if st, code := codeOf(bad); st != 422 || code != "validation" {
		t.Fatalf("unknown run: %d %s", st, code)
	}
	// A missing pack: 409 pack_missing listing it.
	if _, _, err := commit(req); err == nil {
		t.Fatal("commit with a missing pack accepted")
	} else if ae, ok := err.(*client.APIError); !ok || ae.Status != 409 || ae.Code != wire.CodePackMissing || string(ae.Details) != `{"missing":["`+absent+`"]}` {
		t.Fatalf("missing pack: %v", err)
	}

	// Commit, then an idempotent replay.
	req.PackOIDs = []string{pack}
	id, created, err := commit(req)
	if err != nil || !created || id <= 0 {
		t.Fatalf("commit: %d %v %v", id, created, err)
	}
	if again, created, err := commit(req); err != nil || created || again != id {
		t.Fatalf("replay: %d %v %v", again, created, err)
	}
	if n, err := e.st.SnapshotPackCount(ctx, id); err != nil || n != 1 {
		t.Fatalf("packs: %d %v", n, err)
	}

	// The run is linked; the repo usage counts the snapshot.
	run := alice.must("GET", fmt.Sprintf("/api/v1/backup/runs/%d", runID), nil, 200)
	if sid, _ := run["snapshot_id"].(float64); int64(sid) != id {
		t.Fatalf("run snapshot_id: %v", run["snapshot_id"])
	}
	r2, err := dc.Repo(ctx, repo.ID)
	if err != nil || r2.Usage.Snapshots != 1 || r2.Usage.LogicalBytes != 300 {
		t.Fatalf("usage: %+v %v", r2, err)
	}

	// A child snapshot with a parent; a foreign parent is refused.
	root2 := putObj("snapshots", []byte("second root"))
	child := req
	child.SnapshotOID, child.RunID, child.ParentSnapshotID, child.Tags = root2, 0, &id, []string{"weekly"}
	childID, created, err := commit(child)
	if err != nil || !created {
		t.Fatalf("child commit: %v", err)
	}
	bad = child
	bad.SnapshotOID = putObj("snapshots", []byte("third root"))
	nope := int64(99999)
	bad.ParentSnapshotID = &nope
	if st, code := codeOf(bad); st != 422 || code != "validation" {
		t.Fatalf("unknown parent: %d %s", st, code)
	}

	// Catalog list/get (sess).
	out := alice.must("GET", "/api/v1/snapshots", nil, 200)
	items := out["items"].([]any)
	if len(items) != 2 || int64(items[0].(map[string]any)["id"].(float64)) != childID {
		t.Fatalf("list: %v", out)
	}
	first := items[1].(map[string]any)
	prof := first["profile"].(map[string]any)
	if first["kind"] != "folder" || first["ingest_state"] != "pending" || first["browsable"] != false || first["status"] != "committed" ||
		prof["sid"] != sid || prof["username"] != "alice" || first["snapshot_oid"] != root || first["parent_id"] != nil {
		t.Fatalf("snapshot view: %v", first)
	}
	if got := alice.must("GET", "/api/v1/snapshots?tag=weekly", nil, 200)["items"].([]any); len(got) != 1 {
		t.Fatalf("tag filter: %v", got)
	}
	if got := alice.must("GET", fmt.Sprintf("/api/v1/snapshots?repo=%d&job=%d&profile=%s&protected=false&limit=1", repo.ID, job.ID, sid), nil, 200); len(got["items"].([]any)) != 1 || got["next_cursor"] == "" {
		t.Fatalf("filters and paging: %v", got)
	} else {
		next := alice.must("GET", "/api/v1/snapshots?limit=1&cursor="+got["next_cursor"].(string), nil, 200)["items"].([]any)
		if len(next) != 1 || int64(next[0].(map[string]any)["id"].(float64)) != id {
			t.Fatalf("second page: %v", next)
		}
	}
	if code, out := alice.req("GET", "/api/v1/snapshots?repo=x", nil); code != 422 || errCode(out) != "validation" {
		t.Fatalf("bad filter: %d %v", code, out)
	}
	got := alice.must("GET", fmt.Sprintf("/api/v1/snapshots/%d", childID), nil, 200)
	if p, _ := got["parent_id"].(float64); int64(p) != id {
		t.Fatalf("get child: %v", got)
	}
	if code, out := bob.req("GET", fmt.Sprintf("/api/v1/snapshots/%d", id), nil); code != 404 || errCode(out) != wire.CodeSnapshotNotFound {
		t.Fatalf("other user get: %d %v", code, out)
	}
	if items := bob.must("GET", "/api/v1/snapshots", nil, 200)["items"].([]any); len(items) != 0 {
		t.Fatalf("other user list: %v", items)
	}

	// A replay still returns 200 after the profile is blacklisted; a new
	// commit for it is refused.
	if err := dc.Call(ctx, "POST", "/api/v1/device/profiles/"+sid+"/security-event",
		map[string]any{"signal": "password_changed", "observed_at": 2, "evidence": "local_password_age"}, nil); err != nil {
		t.Fatal(err)
	}
	if again, created, err := commit(req); err != nil || created || again != id {
		t.Fatalf("replay after blacklist: %d %v %v", again, created, err)
	}
	bad = req
	bad.SnapshotOID, bad.RunID, bad.ParentSnapshotID = putObj("snapshots", []byte("fourth root")), 0, nil
	if st, code := codeOf(bad); st != 423 || code != wire.CodeProfileBlacklisted {
		t.Fatalf("commit after blacklist: %d %s", st, code)
	}
}
