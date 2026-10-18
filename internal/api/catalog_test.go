package api_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/repo"
	serverbe "github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/repo/backend/server"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/catalog"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// catalogBackup initialises an SDK repository over the server's objects
// (escrow "" = no escrow wrap), writes one snapshot of {DOCUMENTS} with a
// nested directory, and commits it. It returns the repo and snapshot ids.
func catalogBackup(t *testing.T, m *keys.Manager, d *boxDevice, uuid, escrow, sid string) (int64, int64) {
	t.Helper()
	ctx := context.Background()
	rp, err := d.c.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: uuid, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	rid, _ := vdcrypt.ParseRepoID(uuid)
	rcpts := []repo.Recipient{repo.DeviceRecipient(d.boxPub, d.c.DeviceID, d.signPub, "PC")}
	mode := escrow
	if escrow != "" {
		pub, srv, err := m.CurrentSEK()
		if err != nil {
			t.Fatal(err)
		}
		rcpts = append(rcpts, repo.EscrowRecipient(pub, srv.SIKPub, srv.SEKSig, srv.SEKVersion, "server"))
	} else {
		mode = repo.EscrowFull
	}
	r, _, err := repo.Init(ctx, serverbe.New(d.c, rp.ID, serverbe.Options{}), repo.InitOptions{RepoID: rid, EscrowMode: mode, Recipients: rcpts})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	rk, _ := r.Keys()
	h := snapshot.HexPtr(strings.Repeat("ab", 32))
	file := func(n string, size int64) snapshot.Entry {
		return snapshot.Entry{N: n, T: snapshot.TypeFile, S: size, M: 1760745600123456789, A: 32, H: h, Ch: []string{}}
	}
	sub, err := snapshot.EncodeTrees(rk.ID, []snapshot.Entry{file("vaultdock-design.md", 300)})
	if err != nil {
		t.Fatal(err)
	}
	top, err := snapshot.EncodeTrees(rk.ID, []snapshot.Entry{file("Quarterly Report 2024.pdf", 5000), file("notes.txt", 10),
		{N: "Projects", T: snapshot.TypeDir, M: 1760745600000000000, A: 16, Sub: snapshot.HexPtr(sub[0].IDHex())}})
	if err != nil {
		t.Fatal(err)
	}
	mw, err := r.NewPackWriter(pack.ClassMeta)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range append(top, sub...) {
		if _, err := mw.Add(pack.TypeTree, tb.Raw, false); err != nil {
			t.Fatal(err)
		}
	}
	mp, err := mw.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SavePack(ctx, mp); err != nil {
		t.Fatal(err)
	}
	idx, err := r.FlushIndex(ctx)
	if err != nil {
		t.Fatal(err)
	}
	root := &snapshot.Root{V: snapshot.Version, Time: 1760745600000,
		Job:    snapshot.Job{Name: "Documents", Kind: "folder", Mode: "full"},
		Device: snapshot.Device{ID: d.c.DeviceID, Name: "PC"},
		Host:   snapshot.Host{Hostname: "PC", OS: "Windows 11", User: "u", SID: sid, MachineGUIDHash: strings.Repeat("ab", 32)},
		Tokens: snapshot.Tokens{snapshot.TokenDocuments: `C:\Users\u\Documents`},
		Roots:  []snapshot.RootRef{{TokenPath: snapshot.TokenDocuments, Tree: top[0].IDHex()}},
		Stats:  snapshot.Stats{Files: 3, Dirs: 2, Bytes: 5310}, Profile: snapshot.Profile{SID: sid, Username: "u", ConsentID: "c1"},
		InstallMode: snapshot.InstallModeSimple}
	snapOID, err := r.SaveSnapshot(ctx, root, ed25519.PrivateKey(d.c.Key))
	if err != nil {
		t.Fatal(err)
	}

	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sid, Username: "u", ProfilePath: `C:\Users\u`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sid, ConsentAt: 1000, ProofDigest: strings.Repeat("a", 64)}},
	}
	if err := d.c.Call(ctx, "PUT", "/api/v1/device/profiles", rep, nil); err != nil {
		t.Fatal(err)
	}
	resp, created, err := d.c.CommitSnapshot(ctx, rp.ID, wire.SnapshotCommitRequest{SnapshotOID: snapOID, Kind: "folder", Mode: "full",
		Hostname: "PC", OSUser: "u", UserSID: sid, ClientTime: 1, IndexOIDs: idx, PackOIDs: []string{mp.OID}, Epoch: 1, ProfileConsentID: "c1"})
	if err != nil || !created {
		t.Fatalf("commit: %v %v", created, err)
	}
	return rp.ID, resp.SnapshotID
}

// waitIngested waits for the worker to leave the pending state.
func waitIngested(t *testing.T, st *store.Store, id int64) *store.Snapshot {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		sn, err := st.GetSnapshot(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if sn.IngestState != wire.SnapshotIngestPending {
			return sn
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot %d still pending", id)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func names(t *testing.T, out map[string]any) []string {
	t.Helper()
	var ns []string
	for _, it := range out["items"].([]any) {
		ns = append(ns, it.(map[string]any)["name"].(string))
	}
	return ns
}

func TestCatalogIngestBrowseSearch(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	carol := e.createUser(admin, "carol", 0)
	ctx := context.Background()
	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(m)
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go e.srv.RunCatalog(wctx)

	a := e.enrollBox(alice, "alice", "password-alice", "PC-A")
	repoID, snapID := catalogBackup(t, m, a, aliceRepoUUID, repo.EscrowFull, "S-1-5-21-1-2-3-1001")
	if sn := waitIngested(t, e.st, snapID); sn.IngestState != wire.SnapshotIngestDone {
		msg, _ := e.st.SnapshotIngestError(ctx, snapID)
		t.Fatalf("ingest: %s %s", sn.IngestState, msg)
	}
	if v := alice.must("GET", fmt.Sprintf("/api/v1/snapshots/%d", snapID), nil, 200); v["browsable"] != true {
		t.Fatalf("browsable: %v", v)
	}
	if v := alice.must("GET", fmt.Sprintf("/api/v1/repos/%d", repoID), nil, 200); v["last_catalog_sync_at"].(float64) <= 0 {
		t.Fatalf("last_catalog_sync_at: %v", v)
	}

	tree := func(w *web, path, extra string, status int) map[string]any {
		t.Helper()
		return w.must("GET", fmt.Sprintf("/api/v1/snapshots/%d/tree?path=%s%s", snapID, url.QueryEscape(path), extra), nil, status)
	}
	search := func(w *web, query string, status int) map[string]any {
		t.Helper()
		return w.must("GET", fmt.Sprintf("/api/v1/snapshots/%d/search?%s", snapID, query), nil, status)
	}

	// Browse.
	top := tree(alice, "", "", 200)["items"].([]any)
	if len(top) != 1 || top[0].(map[string]any)["path"] != "{DOCUMENTS}" || top[0].(map[string]any)["type"] != "dir" {
		t.Fatalf("top: %v", top)
	}
	docs := tree(alice, "{DOCUMENTS}", "", 200)
	if got := strings.Join(names(t, docs), "|"); got != "Projects|Quarterly Report 2024.pdf|notes.txt" {
		t.Fatalf("documents: %s", got)
	}
	for _, it := range docs["items"].([]any) {
		f := it.(map[string]any)
		if f["name"] == "Quarterly Report 2024.pdf" && (f["size"].(float64) != 5000 || f["modified_at"].(float64) != 1760745600123 ||
			f["path"] != "{DOCUMENTS}/Quarterly Report 2024.pdf" || f["type"] != "file") {
			t.Fatalf("file entry: %v", f)
		}
	}
	p1 := tree(alice, "{DOCUMENTS}", "&limit=2", 200)
	if len(names(t, p1)) != 2 || p1["next_cursor"] == "" {
		t.Fatalf("page 1: %v", p1)
	}
	if p2 := tree(alice, "{DOCUMENTS}", "&limit=2&cursor="+p1["next_cursor"].(string), 200); strings.Join(names(t, p2), "|") != "notes.txt" || p2["next_cursor"] != "" {
		t.Fatalf("page 2: %v", p2)
	}
	if sub := tree(alice, "{DOCUMENTS}/Projects", "", 200)["items"].([]any); len(sub) != 1 ||
		sub[0].(map[string]any)["path"] != "{DOCUMENTS}/Projects/vaultdock-design.md" {
		t.Fatalf("nested: %v", sub)
	}
	tree(alice, "{DOCUMENTS}/missing", "", 404)
	tree(alice, "{DOCUMENTS}/notes.txt", "", 422)

	// Search: whole words and prefixes, case-insensitive; no substrings.
	for q, want := range map[string]string{
		"q=rep":                                "Quarterly Report 2024.pdf",
		"q=QUART+2024":                         "Quarterly Report 2024.pdf",
		"q=port":                               "",
		"q=design":                             "vaultdock-design.md",
		"q=vaultdock+md":                       "vaultdock-design.md",
		"q=pro&type=dir":                       "Projects",
		"q=pro&type=file":                      "",
		"q=report&min_size=1000&max_size=6000": "Quarterly Report 2024.pdf",
		"q=report&min_size=6000":               "",
		"q=txt":                                "notes.txt",
	} {
		if got := strings.Join(names(t, search(alice, q, 200)), "|"); got != want {
			t.Errorf("search %s: %q, want %q", q, got, want)
		}
	}
	search(alice, "q=%20-", 422)
	search(alice, "q=a&type=bogus", 422)
	search(alice, "q=a&min_size=-1", 422)

	// Access: another user gets 404; an admin may browse.
	tree(bob, "", "", 404)
	search(bob, "q=rep", 404)
	if len(names(t, tree(admin, "{DOCUMENTS}", "", 200))) != 3 {
		t.Fatal("admin browse")
	}

	// At rest: no plaintext names, one DEK row, an audited escrow open.
	var n int
	if err := e.st.DB.QueryRow("SELECT COUNT(*) FROM snapshot_files WHERE snapshot_id = ?", snapID).Scan(&n); err != nil || n != 5 {
		t.Fatalf("rows: %d %v", n, err)
	}
	rows, _ := e.st.DB.Query("SELECT path_ct, name_ct FROM snapshot_files")
	for rows.Next() {
		var p, nm []byte
		rows.Scan(&p, &nm)
		if bytes.Contains(p, []byte("Quarterly")) || bytes.Contains(nm, []byte("notes")) {
			t.Fatal("plaintext name stored")
		}
	}
	rows.Close()
	if ck, err := e.st.CatalogKey(ctx, mustUserID(t, e, repoID)); err != nil || !strings.HasPrefix(ck.KEKID, "sek:") {
		t.Fatalf("catalog key: %+v %v", ck, err)
	}
	audits, _ := e.st.ListKeyAudit(ctx, repoID, 0, 100)
	found := false
	for _, au := range audits {
		found = found || (au.Purpose == keys.PurposeCatalog && au.Action == keys.ActionUnwrap)
	}
	if !found {
		t.Fatalf("no catalog unwrap audit: %+v", audits)
	}

	// Idempotent: a done snapshot is left alone; a forced re-run (pending
	// again, as after a crash) rebuilds the same rows.
	if err := e.srv.Catalog().Ingest(ctx, snapID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetSnapshotIngest(ctx, snapID, wire.SnapshotIngestPending, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.srv.Catalog().Ingest(ctx, snapID); err != nil {
		t.Fatal(err)
	}
	if err := e.st.DB.QueryRow("SELECT COUNT(*) FROM snapshot_files WHERE snapshot_id = ?", snapID).Scan(&n); err != nil || n != 5 {
		t.Fatalf("rows after re-ingest: %d %v", n, err)
	}
	if got := strings.Join(names(t, search(alice, "q=notes", 200)), "|"); got != "notes.txt" {
		t.Fatalf("search after re-ingest: %q", got)
	}

	// No escrow wrap: skipped, recorded, not browsable.
	b := e.enrollBox(bob, "bob", "password-bob", "PC-B")
	_, bobSnap := catalogBackup(t, m, b, "1b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d", "", "S-1-5-21-1-2-3-2001")
	if sn := waitIngested(t, e.st, bobSnap); sn.IngestState != wire.SnapshotIngestFailed {
		t.Fatalf("no escrow: %s", sn.IngestState)
	}
	if msg, _ := e.st.SnapshotIngestError(ctx, bobSnap); !strings.HasPrefix(msg, catalog.SkipPrefix) {
		t.Fatalf("skip reason: %q", msg)
	}
	bob.must("GET", fmt.Sprintf("/api/v1/snapshots/%d/tree?path=", bobSnap), nil, 409)

	// Metadata escrow: K_meta alone is enough to browse.
	c := e.enrollBox(carol, "carol", "password-carol", "PC-C")
	_, carolSnap := catalogBackup(t, m, c, "2b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d", repo.EscrowMetadata, "S-1-5-21-1-2-3-3001")
	if sn := waitIngested(t, e.st, carolSnap); sn.IngestState != wire.SnapshotIngestDone {
		msg, _ := e.st.SnapshotIngestError(ctx, carolSnap)
		t.Fatalf("metadata escrow: %s %s", sn.IngestState, msg)
	}
	if got := carol.must("GET", fmt.Sprintf("/api/v1/snapshots/%d/search?q=quarterly", carolSnap), nil, 200); len(names(t, got)) != 1 {
		t.Fatalf("metadata escrow search: %v", got)
	}
}

func mustUserID(t *testing.T, e *env, repoID int64) int64 {
	t.Helper()
	rp, err := e.st.Repository(context.Background(), repoID)
	if err != nil {
		t.Fatal(err)
	}
	return rp.UserID
}

// Without key custody a committed snapshot is skipped and browsing answers
// escrow_unavailable / not_browsable.
func TestCatalogWithoutKeyCustody(t *testing.T) {
	e := newEnv(t, "")
	ctx := context.Background()
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	rp, err := dc.CreateRepo(ctx, wire.RepoCreateRequest{RepoUUID: aliceRepoUUID, Name: "PC"})
	if err != nil {
		t.Fatal(err)
	}
	sid := "S-1-5-21-1-2-3-1001"
	rep := wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sid, Username: "alice", ProfilePath: `C:\Users\alice`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sid, ConsentAt: 1000, ProofDigest: strings.Repeat("a", 64)}},
	}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/profiles", rep, nil); err != nil {
		t.Fatal(err)
	}
	root := []byte("sealed root")
	oid := vdcrypt.OID(root)
	if _, err := dc.PutObject(ctx, rp.ID, "snapshots", oid, bytes.NewReader(root), int64(len(root)), oid); err != nil {
		t.Fatal(err)
	}
	resp, _, err := dc.CommitSnapshot(ctx, rp.ID, wire.SnapshotCommitRequest{SnapshotOID: oid, Kind: "folder", Mode: "full",
		UserSID: sid, Epoch: 1, ProfileConsentID: "c1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.srv.Catalog().Ingest(ctx, resp.SnapshotID); err != nil {
		t.Fatal(err)
	}
	sn, _ := e.st.GetSnapshot(ctx, resp.SnapshotID)
	msg, _ := e.st.SnapshotIngestError(ctx, resp.SnapshotID)
	if sn.IngestState != wire.SnapshotIngestFailed || !strings.HasPrefix(msg, catalog.SkipPrefix) {
		t.Fatalf("skip: %s %q", sn.IngestState, msg)
	}
	alice.must("GET", fmt.Sprintf("/api/v1/snapshots/%d/search?q=x", resp.SnapshotID), nil, 409)
}
