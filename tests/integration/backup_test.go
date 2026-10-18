package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"
)

const backupRepoUUID = "5c4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"

func sortedKeys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func checkFiles(t *testing.T, got, want map[string][]byte) {
	t.Helper()
	if strings.Join(sortedKeys(got), "|") != strings.Join(sortedKeys(want), "|") {
		t.Fatalf("restored %v, want %v", sortedKeys(got), sortedKeys(want))
	}
	for p, b := range want {
		if !bytes.Equal(got[p], b) {
			t.Errorf("%s: restored content differs", p)
		}
	}
}

// TestBackupIncrementalSecondDeviceCatalog: first-device keys → backup over
// the server backend → catalog commit (idempotent) → ingest, browse and
// search → incremental backup that uploads only new content and links its
// parent → a second device gets the keys by key_share and restores the
// latest snapshot byte for byte.
func TestBackupIncrementalSecondDeviceCatalog(t *testing.T) {
	srv := testserver.Start(t)
	ctx := context.Background()

	a := enrolKeyDevice(t, srv, "PC-A")
	ci := checkIdentity(t, a.ident)
	repoID, r, _ := initRepo(t, a, ci, backupRepoUUID, "PC backups", repo.EscrowFull)
	reportConsent(t, a)
	w := &snapWriter{t: t, r: r, dev: a}

	// Full backup.
	report := randomBytes(t, 300<<10)
	files := map[string][]byte{
		"Quarterly Report 2024.pdf":    report,
		"notes.txt":                    []byte("version 1"),
		"Projects/vaultdock-design.md": []byte("# design"),
		"Projects/empty.txt":           {},
	}
	s1 := w.write(files, nil)
	if s1.newBlobs != 3 || len(s1.newPacks) != 2 {
		t.Fatalf("first backup: %d new blobs, %d packs", s1.newBlobs, len(s1.newPacks))
	}
	id1, created := w.commit(repoID, s1, nil)
	if !created || id1 == 0 {
		t.Fatalf("commit 1: %d %v", id1, created)
	}
	if again, created := w.commit(repoID, s1, nil); created || again != id1 {
		t.Fatalf("commit replay: %d %v", again, created)
	}

	// Catalog: ingest, browse, search (session routes).
	waitIngest(t, srv.Store, id1)
	if v := srv.Do("GET", fmt.Sprintf("/api/v1/snapshots/%d", id1), nil, 200); v["browsable"] != true || v["device_id"] != float64(a.c.DeviceID) {
		t.Fatalf("snapshot 1: %v", v)
	}
	tree := func(id int64, p string) []string {
		t.Helper()
		return itemNames(srv.Do("GET", fmt.Sprintf("/api/v1/snapshots/%d/tree?path=%s", id, url.QueryEscape(p)), nil, 200))
	}
	search := func(id int64, q string) []string {
		t.Helper()
		return itemNames(srv.Do("GET", fmt.Sprintf("/api/v1/snapshots/%d/search?q=%s", id, url.QueryEscape(q)), nil, 200))
	}
	if got := tree(id1, ""); len(got) != 1 {
		t.Fatalf("roots: %v", got)
	}
	if got := strings.Join(tree(id1, "{DOCUMENTS}"), "|"); got != "Projects|Quarterly Report 2024.pdf|notes.txt" {
		t.Fatalf("documents: %s", got)
	}
	if got := strings.Join(tree(id1, "{DOCUMENTS}/Projects"), "|"); got != "empty.txt|vaultdock-design.md" {
		t.Fatalf("projects: %s", got)
	}
	if got := strings.Join(search(id1, "quarterly 2024"), "|"); got != "Quarterly Report 2024.pdf" {
		t.Fatalf("search: %s", got)
	}

	// Incremental: one changed and one new file; unchanged content is not
	// uploaded again.
	files["notes.txt"] = []byte("version 2")
	files["Projects/budget.xlsx"] = randomBytes(t, 40<<10)
	s2 := w.write(files, &s1.oid)
	if s2.newBlobs != 2 || s2.newBytes != int64(len("version 2")+40<<10) {
		t.Fatalf("incremental: %d new blobs, %d new bytes", s2.newBlobs, s2.newBytes)
	}
	reused := 0
	for _, p := range s2.packs {
		for _, q := range s1.newPacks {
			if p == q {
				reused++
			}
		}
	}
	if reused == 0 {
		t.Fatalf("incremental references none of the first packs: %v / %v", s2.packs, s1.newPacks)
	}
	id2, created := w.commit(repoID, s2, &id1)
	if !created || id2 == id1 {
		t.Fatalf("commit 2: %d %v", id2, created)
	}
	if v := srv.Do("GET", fmt.Sprintf("/api/v1/snapshots/%d", id2), nil, 200); v["parent_id"] != float64(id1) {
		t.Fatalf("parent link: %v", v)
	}
	if l := srv.Do("GET", fmt.Sprintf("/api/v1/snapshots?repo=%d", repoID), nil, 200); len(l["items"].([]any)) != 2 ||
		l["items"].([]any)[0].(map[string]any)["id"] != float64(id2) {
		t.Fatalf("snapshot list: %v", l)
	}
	waitIngest(t, srv.Store, id2)
	if got := strings.Join(search(id2, "budget"), "|"); got != "budget.xlsx" {
		t.Fatalf("search snapshot 2: %s", got)
	}
	if got := search(id1, "budget"); len(got) != 0 {
		t.Fatalf("snapshot 1 sees a later file: %v", got)
	}

	// Second device: key_share by the server (full escrow), then a restore
	// of both snapshots from B.
	b := enrolKeyDevice(t, srv, "PC-B")
	task := b.keyTask(t, wire.TaskKindKeyShare, nil)
	var share wire.TaskKeySharePayload
	if err := json.Unmarshal(task.Payload, &share); err != nil || share.RepoID != repoID || share.KeyFileOID == "" {
		t.Fatalf("key_share: %s %v", task.Payload, err)
	}
	if err := b.c.FinishKeyShare(ctx, task.ID, client.KeyTaskResult{RepoID: repoID, Epoch: 1, KeyFileOID: share.KeyFileOID}); err != nil {
		t.Fatal(err)
	}
	br := b.openRepo(t, repoID)
	checkFiles(t, restoreAll(t, br, s2.oid), files)
	first := restoreAll(t, br, s1.oid)
	if string(first["notes.txt"]) != "version 1" || first["Projects/budget.xlsx"] != nil {
		t.Fatalf("snapshot 1 from B: %v", sortedKeys(first))
	}
	if ks, err := b.c.KeyStatus(ctx, repoID); err != nil || !hasDeviceWrap(ks, b.c.DeviceID) {
		t.Fatalf("B key status: %+v %v", ks, err)
	}
}
