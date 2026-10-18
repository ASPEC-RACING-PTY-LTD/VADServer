package integration

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/restore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"
)

const restoreRepoUUID = "6d5e7f2a-4e3b-4a6c-9d0e-1f2a3b4c5d6e"

// TestRestoreFlowAndDownloads (api-v1 §7, §5.4, rev 1.1c): plan from the
// catalog → execute → owner approval → restore device task → the device
// fetches the restore, restores the selection with sdk/restore and reports
// progress and items; a cancel before approval; full-escrow file content
// (with Range) and the single-use zip download.
func TestRestoreFlowAndDownloads(t *testing.T) {
	srv := testserver.Start(t)
	ctx := context.Background()

	a := enrolKeyDevice(t, srv, "PC-R")
	ci := checkIdentity(t, a.ident)
	repoID, r, _ := initRepo(t, a, ci, restoreRepoUUID, "PC backups", repo.EscrowFull)
	reportConsent(t, a)
	w := &snapWriter{t: t, r: r, dev: a}
	files := map[string][]byte{
		"notes.txt":           []byte("version 1"),
		"Projects/design.md":  []byte("# design"),
		"Projects/big.bin":    randomBytes(t, 200<<10),
		"Projects/empty.txt":  {},
		"Taxes/2024/form.pdf": []byte("tax"),
	}
	s1 := w.write(files, nil)
	snapID, _ := w.commit(repoID, s1, nil)
	waitIngest(t, srv.Store, snapID)

	// --- plan ---
	planReq := map[string]any{
		"snapshot_id": snapID, "target_device_id": a.c.DeviceID,
		"selection":       []map[string]any{{"path": "{DOCUMENTS}/Projects", "include": true}, {"path": "{DOCUMENTS}/Projects/big.bin", "include": false}},
		"mapping":         map[string]any{"auto": true, "overrides": []map[string]any{}},
		"conflict_policy": "rename", "reinstall_apps": []string{}, "include_registry": false,
	}
	plan := srv.Do("POST", "/api/v1/restore/plan", planReq, 200)
	pb := plan["plan"].(map[string]any)
	if sum := pb["summary"].(map[string]any); sum["files"] != float64(2) || sum["bytes"] != float64(len("# design")) {
		t.Fatalf("plan summary: %v", sum)
	}
	if m := pb["mappings"].([]any); len(m) != 1 || m[0].(map[string]any)["token"] != "{DOCUMENTS}" {
		t.Fatalf("plan mappings: %v", m)
	}
	token, _ := plan["plan_token"].(string)
	if token == "" {
		t.Fatalf("no plan token: %v", plan)
	}

	// Validation: unknown device, unconsented profile, protected override.
	bad := func(mut func(m map[string]any), want int) {
		t.Helper()
		m := map[string]any{}
		for k, v := range planReq {
			m[k] = v
		}
		mut(m)
		srv.Do("POST", "/api/v1/restore/plan", m, want)
	}
	bad(func(m map[string]any) { m["target_device_id"] = 999999 }, 404)
	bad(func(m map[string]any) { m["target_profile_sid"] = "S-1-5-21-9-9-9-1001" }, 403)
	bad(func(m map[string]any) { m["conflict_policy"] = "clobber" }, 422)
	g := srv.Do("POST", "/api/v1/restore/plan", map[string]any{"snapshot_id": snapID, "target_device_id": a.c.DeviceID,
		"selection": []map[string]any{{"path": "{DOCUMENTS}/Taxes", "include": true}},
		"mapping":   map[string]any{"auto": true, "overrides": []map[string]any{{"from": "{DOCUMENTS}/Taxes", "to": `C:\Windows\Taxes`}}},
	}, 200)
	if gp := g["plan"].(map[string]any); len(gp["guard"].([]any)) != 1 || gp["summary"].(map[string]any)["files"] != float64(0) {
		t.Fatalf("guarded plan: %v", gp)
	}

	// --- execute, approve, task ---
	ex := srv.Do("POST", "/api/v1/restore/execute", map[string]any{"plan_token": token}, 202)
	restoreID, approvalID := int64(ex["restore_id"].(float64)), int64(ex["approval_id"].(float64))
	srv.Do("POST", "/api/v1/restore/execute", map[string]any{"plan_token": token}, 409) // single use
	if v := srv.Do("GET", fmt.Sprintf("/api/v1/restore/%d", restoreID), nil, 200); v["status"] != wire.RestoreStatusAwaitingApproval {
		t.Fatalf("restore before approval: %v", v)
	}
	srv.Do("POST", fmt.Sprintf("/api/v1/approvals/%d/approve", approvalID), map[string]any{}, 200)
	if v := srv.Do("GET", fmt.Sprintf("/api/v1/restore/%d", restoreID), nil, 200); v["status"] != wire.RestoreStatusQueued || v["task_id"] == nil {
		t.Fatalf("restore after approval: %v", v)
	}
	task := a.keyTask(t, wire.TaskKindRestore, nil)
	var tp wire.TaskRestorePayload
	if err := json.Unmarshal(task.Payload, &tp); err != nil || tp.RestoreID != restoreID {
		t.Fatalf("restore task payload: %s %v", task.Payload, err)
	}

	// --- the device runs it ---
	dr, err := a.c.DeviceRestore(ctx, restoreID)
	if err != nil {
		t.Fatal(err)
	}
	if dr.RepoID != repoID || dr.RepoUUID != restoreRepoUUID || dr.SnapshotOID != s1.oid || dr.ConflictPolicy != "rename" || len(dr.Selection) != 2 {
		t.Fatalf("device restore: %+v", dr)
	}
	sel, err := restore.NewSelector(dr.Selection)
	if err != nil {
		t.Fatal(err)
	}
	root, _, err := r.LoadSnapshot(ctx, dr.SnapshotOID)
	if err != nil {
		t.Fatal(err)
	}
	rk, _ := r.Keys()
	got := map[string][]byte{}
	var items []wire.RestoreItem
	err = restore.Walk(ctx, r, root.Roots, sel, func(it restore.Item) error {
		if it.IsDir() {
			return nil
		}
		var buf bytes.Buffer
		if _, err := restore.CopyFile(ctx, r, &rk.FileHash, it.Entry, &buf); err != nil {
			return err
		}
		got[it.Path] = buf.Bytes()
		items = append(items, wire.RestoreItem{Path: it.Path, TargetPath: `C:\Users\u\Documents\` + it.Entry.N, Action: wire.RestoreActionRestore, Status: wire.RestoreItemDone, Size: it.Entry.S})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	checkFiles(t, got, map[string][]byte{"{DOCUMENTS}/Projects/design.md": files["Projects/design.md"], "{DOCUMENTS}/Projects/empty.txt": {}})
	if rep, err := a.c.ReportRestore(ctx, restoreID, wire.RestoreProgress{Status: wire.RestoreStatusRunning, FilesTotal: 2, BytesTotal: 8}); err != nil || rep.Status != wire.RestoreStatusRunning || rep.CancelRequested {
		t.Fatalf("running report: %+v %v", rep, err)
	}
	if rep, err := a.c.ReportRestore(ctx, restoreID, wire.RestoreProgress{Status: wire.RestoreStatusCompleted, FilesTotal: 2, FilesDone: 2, BytesTotal: 8, BytesDone: 8, Items: items}); err != nil || rep.Status != wire.RestoreStatusCompleted {
		t.Fatalf("final report: %+v %v", rep, err)
	}
	if _, err := a.c.ReportRestore(ctx, restoreID, wire.RestoreProgress{Status: wire.RestoreStatusRunning}); !client.IsCode(err, wire.CodeConflict) {
		t.Fatalf("report after the end: %v", err)
	}
	v := srv.Do("GET", fmt.Sprintf("/api/v1/restore/%d", restoreID), nil, 200)
	if v["status"] != wire.RestoreStatusCompleted || v["files_done"] != float64(2) || v["finished_at"] == nil {
		t.Fatalf("restore after completion: %v", v)
	}
	if it := srv.Do("GET", fmt.Sprintf("/api/v1/restore/%d/items?status=done", restoreID), nil, 200); len(it["items"].([]any)) != 2 {
		t.Fatalf("items: %v", it)
	}
	if l := srv.Do("GET", "/api/v1/restore", nil, 200); len(l["items"].([]any)) != 1 {
		t.Fatalf("restore list: %v", l)
	}
	srv.Do("POST", fmt.Sprintf("/api/v1/restore/%d/cancel", restoreID), map[string]any{}, 409)

	// --- cancel while waiting for approval ---
	plan2 := srv.Do("POST", "/api/v1/restore/plan", planReq, 200)
	ex2 := srv.Do("POST", "/api/v1/restore/execute", map[string]any{"plan_token": plan2["plan_token"]}, 202)
	r2 := int64(ex2["restore_id"].(float64))
	if v := srv.Do("POST", fmt.Sprintf("/api/v1/restore/%d/cancel", r2), map[string]any{}, 200); v["status"] != wire.RestoreStatusCancelled {
		t.Fatalf("cancelled restore: %v", v)
	}
	if ap := srv.Do("GET", fmt.Sprintf("/api/v1/approvals/%v", ex2["approval_id"]), nil, 200); ap["state"] != "expired" {
		t.Fatalf("approval after cancel: %v", ap)
	}
	if _, err := a.c.DeviceRestore(ctx, 999999); !client.IsCode(err, "not_found") {
		t.Fatalf("foreign restore: %v", err)
	}

	// --- full-escrow content and zip download ---
	content := func(path, rng string) (int, []byte) {
		t.Helper()
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/snapshots/%d/content?path=%s", srv.URL, snapID, url.QueryEscape(path)), nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if st, b := content("{DOCUMENTS}/notes.txt", ""); st != 200 || string(b) != "version 1" {
		t.Fatalf("content: %d %q", st, b)
	}
	if st, b := content("{DOCUMENTS}/Projects/big.bin", "bytes=100000-100009"); st != 206 || !bytes.Equal(b, files["Projects/big.bin"][100000:100010]) {
		t.Fatalf("range: %d %d bytes", st, len(b))
	}
	if st, _ := content("{DOCUMENTS}/missing.txt", ""); st != 404 {
		t.Fatalf("missing content: %d", st)
	}

	acc := srv.Do("POST", fmt.Sprintf("/api/v1/snapshots/%d/download", snapID), map[string]any{"paths": []string{"{DOCUMENTS}/Projects", "{DOCUMENTS}/notes.txt"}}, 202)
	opID := int64(acc["operation_id"].(float64))
	var op map[string]any
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		op = srv.Do("GET", fmt.Sprintf("/api/v1/operations/%d", opID), nil, 200)
		if op["state"] == "succeeded" || op["state"] == "failed" {
			break
		}
	}
	res, _ := op["result"].(map[string]any)
	dl, _ := res["download_url"].(string)
	if op["state"] != "succeeded" || !strings.HasPrefix(dl, "/api/v1/downloads/") || res["files"] != float64(4) {
		t.Fatalf("download operation: %v", op)
	}
	resp, err := http.Get(srv.URL + dl) // no session: the token is the credential
	if err != nil {
		t.Fatal(err)
	}
	zb, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("zip: %d %s", resp.StatusCode, zb)
	}
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	inZip := map[string][]byte{}
	for _, f := range zr.File {
		if strings.HasSuffix(f.Name, "/") {
			continue
		}
		rc, _ := f.Open()
		inZip[f.Name], _ = io.ReadAll(rc)
		rc.Close()
	}
	checkFiles(t, inZip, map[string][]byte{
		"Documents/notes.txt": files["notes.txt"], "Documents/Projects/design.md": files["Projects/design.md"],
		"Documents/Projects/big.bin": files["Projects/big.bin"], "Documents/Projects/empty.txt": {},
	})
	if resp, err := http.Get(srv.URL + dl); err != nil || resp.StatusCode != 404 {
		t.Fatalf("second use of the download link: %v %v", resp, err)
	}
}
