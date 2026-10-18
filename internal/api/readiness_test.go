package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

func readinessOf(t *testing.T, w *web, path string) wire.ReadinessReport {
	t.Helper()
	var rep wire.ReadinessReport
	raw, _ := json.Marshal(w.must("GET", path, nil, 200))
	if err := json.Unmarshal(raw, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func checkOf(t *testing.T, d wire.ReadinessDevice, id string) wire.ReadinessCheck {
	t.Helper()
	for _, c := range d.Checks {
		if c.ID == id {
			return c
		}
	}
	t.Fatalf("check %s missing: %+v", id, d.Checks)
	return wire.ReadinessCheck{}
}

func hasCheck(d wire.ReadinessDevice, id string) bool {
	for _, c := range d.Checks {
		if c.ID == id {
			return true
		}
	}
	return false
}

func TestReadinessAndBackupState(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	// No devices: unknown, score 0.
	if rep := readinessOf(t, bob, "/api/v1/readiness"); rep.Status != wire.ReadinessStatusUnknown || rep.Score != 0 || len(rep.Devices) != 0 {
		t.Fatalf("bob empty: %+v", rep)
	}

	// A device with nothing reported: no jobs is critical, device inputs unknown.
	rep := readinessOf(t, alice, "/api/v1/readiness")
	if len(rep.Devices) != 1 || rep.Devices[0].DeviceID != dc.DeviceID || rep.Status != wire.ReadinessStatusCritical {
		t.Fatalf("fresh: %+v", rep)
	}
	d := rep.Devices[0]
	if c := checkOf(t, d, wire.ReadinessCheckRecentBackup); c.Status != wire.ReadinessStatusCritical || c.FixLink == "" {
		t.Fatalf("no jobs: %+v", c)
	}
	for _, id := range []string{wire.ReadinessCheckVSSAvailable, wire.ReadinessCheckRecoveryKeyConfirmed, wire.ReadinessCheckServiceInstalled, wire.ReadinessCheckInstallMode, wire.ReadinessCheckModulesHealthy} {
		if c := checkOf(t, d, id); c.Status != wire.ReadinessStatusUnknown {
			t.Fatalf("%s before reports: %+v", id, c)
		}
	}
	if hasCheck(d, "server_key_custody") {
		t.Fatal("non-admin sees server key custody")
	}

	// Backup state: dev only, strict JSON, 204.
	if err := dc.ReportBackupState(ctx, wire.BackupState{RecoveryKeyConfirmed: false, ServiceInstalled: true, VSSAvailable: false}); err != nil {
		t.Fatal(err)
	}
	if s, code := apiCode(t, dc.Call(ctx, "PUT", "/api/v1/device/backup-state", map[string]any{"service_installed": true, "extra": 1}, nil)); s != 400 || code != "bad_json" {
		t.Fatalf("unknown field: %d %s", s, code)
	}
	if code, out := alice.req("PUT", "/api/v1/device/backup-state", wire.BackupState{}); code != 403 || errCode(out) != "device_required" {
		t.Fatalf("session on dev route: %d %v", code, out)
	}
	if s, code := apiCode(t, dc.Call(ctx, "GET", "/api/v1/readiness", nil, nil)); s != 403 || code != "session_required" {
		t.Fatalf("device on sess route: %d %s", s, code)
	}
	if err := dc.ReportModules(ctx, wire.ModuleReport{InstallMode: "advanced", CoreVersion: "1", Modules: []wire.ModuleStatus{
		{ID: "core", Version: "1", State: "enabled"}, {ID: "vss", State: "failed", Error: "download failed"}}}); err != nil {
		t.Fatal(err)
	}

	// A job with a fresh successful run and one open issue.
	job, err := dc.CreateJob(ctx, folderJob("Documents"))
	if err != nil {
		t.Fatal(err)
	}
	runID, err := dc.StartRun(ctx, job.ID, wire.RunStartRequest{ClientRunUUID: "run-1", Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	if err := dc.ReportIssues(ctx, runID, []wire.IssueReport{{Path: `C:\a.pst`, Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Count: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := dc.FinishRun(ctx, runID, wire.RunProgress{Status: wire.RunStatusPartial}); err != nil {
		t.Fatal(err)
	}
	d = readinessOf(t, alice, fmt.Sprintf("/api/v1/readiness?device=%d", dc.DeviceID)).Devices[0]
	want := map[string]string{
		wire.ReadinessCheckRecentBackup:         wire.ReadinessStatusOK,
		wire.ReadinessCheckOpenIssues:           wire.ReadinessStatusWarning,
		wire.ReadinessCheckVSSAvailable:         wire.ReadinessStatusWarning,
		wire.ReadinessCheckVerifiedRecently:     wire.ReadinessStatusUnknown,
		wire.ReadinessCheckTestRestoreRecent:    wire.ReadinessStatusUnknown,
		wire.ReadinessCheckRecoveryKeyConfirmed: wire.ReadinessStatusCritical, // no escrow without keys
		wire.ReadinessCheckServiceInstalled:     wire.ReadinessStatusOK,
		wire.ReadinessCheckProfileAuthorised:    wire.ReadinessStatusOK,
		wire.ReadinessCheckInstallMode:          wire.ReadinessStatusOK,
		wire.ReadinessCheckModulesHealthy:       wire.ReadinessStatusWarning, // vss failed
	}
	for id, st := range want {
		if c := checkOf(t, d, id); c.Status != st {
			t.Fatalf("%s: %+v (want %s)", id, c, st)
		}
	}
	if len(d.Jobs) != 1 || d.Jobs[0].JobID != job.ID || d.Jobs[0].LastSuccessAt == 0 || d.Jobs[0].OpenIssues != 1 || d.Jobs[0].VSSUsed {
		t.Fatalf("jobs: %+v", d.Jobs)
	}

	// With server keys the unconfirmed recovery key is only a warning.
	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(m)
	d = readinessOf(t, alice, "/api/v1/readiness").Devices[0]
	if c := checkOf(t, d, wire.ReadinessCheckRecoveryKeyConfirmed); c.Status != wire.ReadinessStatusWarning {
		t.Fatalf("recovery key with escrow: %+v", c)
	}

	// An old last success on a daily job is critical.
	old := time.Now().Add(-10 * 24 * time.Hour).UnixMilli()
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE job_runs SET finished_at = ?, updated_at = ? WHERE id = ?`, old, old, runID); err != nil {
		t.Fatal(err)
	}
	d = readinessOf(t, alice, "/api/v1/readiness").Devices[0]
	if c := checkOf(t, d, wire.ReadinessCheckRecentBackup); c.Status != wire.ReadinessStatusCritical || c.FixLink != fmt.Sprintf("/backups/jobs/%d", job.ID) {
		t.Fatalf("overdue: %+v", c)
	}

	// A blacklisted profile suspends jobs: profile_authorised critical.
	if err := dc.Call(ctx, "POST", "/api/v1/device/profiles/S-1-5-21-1-1001/security-event",
		map[string]any{"signal": "password_reset", "observed_at": time.Now().UnixMilli(), "evidence": "eventlog_4724"}, nil); err != nil {
		t.Fatal(err)
	}
	d = readinessOf(t, alice, "/api/v1/readiness").Devices[0]
	if c := checkOf(t, d, wire.ReadinessCheckProfileAuthorised); c.Status != wire.ReadinessStatusCritical {
		t.Fatalf("blacklisted: %+v", c)
	}

	// Access: other users get 404; bad params 422; scope=all is admin-only.
	if code, _ := bob.req("GET", fmt.Sprintf("/api/v1/readiness?device=%d", dc.DeviceID), nil); code != 404 {
		t.Fatalf("other user device: %d", code)
	}
	for _, q := range []string{"device=abc", "device=0", "scope=everyone"} {
		if code, out := alice.req("GET", "/api/v1/readiness?"+q, nil); code != 422 || errCode(out) != "validation" {
			t.Fatalf("%s: %d %v", q, code, out)
		}
	}
	if code, out := alice.req("GET", "/api/v1/readiness?scope=all", nil); code != 403 || errCode(out) != "forbidden" {
		t.Fatalf("non-admin scope=all: %d %v", code, out)
	}

	// Admin view: every device, plus server key custody.
	rep = readinessOf(t, admin, "/api/v1/readiness?scope=all")
	if len(rep.Devices) != 1 || rep.Devices[0].DeviceID != dc.DeviceID {
		t.Fatalf("admin all: %+v", rep)
	}
	if c := checkOf(t, rep.Devices[0], "server_key_custody"); c.Status != wire.ReadinessStatusOK {
		t.Fatalf("custody with keys: %+v", c)
	}
	e.srv.SetKeys(nil)
	d = readinessOf(t, admin, fmt.Sprintf("/api/v1/readiness?device=%d", dc.DeviceID)).Devices[0]
	if c := checkOf(t, d, "server_key_custody"); c.Status != wire.ReadinessStatusCritical || c.FixLink != "/admin" {
		t.Fatalf("custody degraded: %+v", c)
	}
	if rep := readinessOf(t, admin, "/api/v1/readiness"); len(rep.Devices) != 0 {
		t.Fatalf("admin own devices: %+v", rep)
	}
}
