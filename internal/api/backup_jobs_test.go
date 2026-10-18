package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func folderJob(name string) wire.JobCreateRequest {
	return wire.JobCreateRequest{Name: name, Kind: wire.JobKindFolder, Enabled: true,
		Spec:     wire.JobSpec{Paths: []string{"{DOCUMENTS}"}, Exclude: []string{"**/node_modules"}},
		Schedule: wire.JobSchedule{Type: wire.JobScheduleDaily, At: "02:00", Timezone: "Local", CatchUp: wire.JobCatchUpRunOnce, JitterMinutes: 10}}
}

func TestBackupJobsCRUDGatingOwnDevice(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	bc := e.enrollDevice(bob, "bob", "password-bob")
	ctx := context.Background()

	// A device creates a job for itself.
	job, err := dc.CreateJob(ctx, folderJob("Documents"))
	if err != nil {
		t.Fatal(err)
	}
	if job.DeviceID != dc.DeviceID || job.Revision != 1 || job.State != wire.JobStateActive || len(job.RequiredModules) != 0 || job.Mode != wire.JobModeFull || job.Spec.UseVSS != wire.JobUseVSSAuto {
		t.Fatalf("created: %+v", job)
	}

	// Own-device enforcement.
	other := folderJob("x")
	other.DeviceID = bc.DeviceID
	if st, code := apiCode(t, dc.Call(ctx, "POST", "/api/v1/backup/jobs", other, nil)); st != 403 || code != "forbidden" {
		t.Fatalf("device creates for another device: %d %s", st, code)
	}
	if st, code := apiCode(t, dc.Call(ctx, "GET", fmt.Sprintf("/api/v1/backup/jobs?device=%d", bc.DeviceID), nil, nil)); st != 403 || code != "forbidden" {
		t.Fatalf("device lists another device: %d %s", st, code)
	}
	if st, _ := apiCode(t, func() error { _, err := bc.UpdateJob(ctx, job.ID, 1, wire.JobUpdateRequest{}); return err }()); st != 404 {
		t.Fatalf("foreign device patch: %d", st)
	}
	if out := bob.must("POST", "/api/v1/backup/jobs", map[string]any{"device_id": dc.DeviceID, "name": "x", "kind": "folder"}, 422); errCode(out) != "validation" {
		t.Fatalf("foreign user create: %v", out)
	}
	if code, _ := bob.req("PATCH", fmt.Sprintf("/api/v1/backup/jobs/%d", job.ID), map[string]any{"name": "y"}); code != 404 {
		t.Fatalf("foreign user patch: %d", code)
	}
	if l, err := bc.Jobs(ctx, "", 0); err != nil || len(l.Items) != 0 {
		t.Fatalf("bob device list: %v %v", l, err)
	}
	if l, err := dc.Jobs(ctx, "", 0); err != nil || len(l.Items) != 1 || l.Items[0].ID != job.ID {
		t.Fatalf("alice device list: %v %v", l, err)
	}
	if l := alice.must("GET", "/api/v1/backup/jobs", nil, 200); len(l["items"].([]any)) != 1 {
		t.Fatalf("session list: %v", l)
	}

	// Validation, including the schedule.
	bad := folderJob("bad")
	bad.Schedule = wire.JobSchedule{Type: wire.JobScheduleInterval, EveryMinutes: 1}
	if out := alice.must("POST", "/api/v1/backup/jobs", map[string]any{"device_id": dc.DeviceID, "name": "bad", "kind": "folder", "schedule": bad.Schedule}, 422); !strings.Contains(fmt.Sprint(out), "schedule") {
		t.Fatalf("bad schedule: %v", out)
	}
	if out := alice.must("POST", "/api/v1/backup/jobs", map[string]any{"device_id": dc.DeviceID, "name": "bad", "kind": "nope", "repo_id": 77}, 422); errCode(out) != "validation" {
		t.Fatalf("bad kind: %v", out)
	}

	// Revision conflicts (If-Match).
	name := "Docs"
	upd, err := dc.UpdateJob(ctx, job.ID, 1, wire.JobUpdateRequest{Name: &name})
	if err != nil || upd.Revision != 2 || upd.Name != "Docs" {
		t.Fatalf("patch: %+v %v", upd, err)
	}
	if st, code := apiCode(t, func() error { _, err := dc.UpdateJob(ctx, job.ID, 1, wire.JobUpdateRequest{Name: &name}); return err }()); st != 409 || code != wire.CodeConflict {
		t.Fatalf("stale patch: %d %s", st, code)
	}
	// A session PATCH without If-Match works on the current revision.
	if out := alice.must("PATCH", fmt.Sprintf("/api/v1/backup/jobs/%d", job.ID), map[string]any{"priority": 5}, 200); out["revision"] != 3.0 || out["priority"] != 5.0 {
		t.Fatalf("session patch: %v", out)
	}

	// Module gating: an app job needs discovery; use_vss required needs vss.
	app := folderJob("Apps")
	app.Kind = wire.JobKindApp
	_, err = dc.CreateJob(ctx, app)
	if d, ok := client.ModuleUnavailable(err); !ok || d.Module != wire.ModuleDiscovery {
		t.Fatalf("app job without discovery: %v", err)
	}
	if err := dc.ReportModules(ctx, wire.ModuleReport{InstallMode: "advanced", CoreVersion: "1", Modules: []wire.ModuleStatus{
		{ID: "core", State: "enabled"}, {ID: "discovery", State: "enabled"}}}); err != nil {
		t.Fatal(err)
	}
	appJob, err := dc.CreateJob(ctx, app)
	if err != nil || fmt.Sprint(appJob.RequiredModules) != "[discovery]" {
		t.Fatalf("app job: %+v %v", appJob, err)
	}
	vss := wire.JobSpec{Paths: []string{"{DOCUMENTS}"}, UseVSS: wire.JobUseVSSRequired}
	if st, code := apiCode(t, func() error { _, err := dc.UpdateJob(ctx, job.ID, 3, wire.JobUpdateRequest{Spec: &vss}); return err }()); st != 409 || code != wire.CodeModuleUnavailable {
		t.Fatalf("enable vss: %d %s", st, code)
	}
	// Disabling never needs the gate; enabling again does.
	off := false
	if _, err := dc.UpdateJob(ctx, appJob.ID, 1, wire.JobUpdateRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if err := dc.ReportModules(ctx, wire.ModuleReport{InstallMode: "simple", CoreVersion: "1", Modules: []wire.ModuleStatus{{ID: "core", State: "enabled"}}}); err != nil {
		t.Fatal(err)
	}
	// The module report may move the job's state, which bumps its revision.
	rev := int64(0)
	if l, err := dc.Jobs(ctx, "", 0); err != nil {
		t.Fatal(err)
	} else {
		for _, j := range l.Items {
			if j.ID == appJob.ID {
				rev = j.Revision
			}
		}
	}
	on := true
	if st, code := apiCode(t, func() error {
		_, err := dc.UpdateJob(ctx, appJob.ID, rev, wire.JobUpdateRequest{Enabled: &on})
		return err
	}()); st != 409 || code != wire.CodeModuleUnavailable {
		t.Fatalf("re-enable without module: %d %s", st, code)
	}

	// Profile gating: unknown profile → 403, blacklisted → 423.
	sid := "S-1-5-21-1000-2000-3000-1001"
	prof := folderJob("Profile")
	prof.ProfileSID = sid
	if st, code := apiCode(t, func() error { _, err := dc.CreateJob(ctx, prof); return err }()); st != 403 || code != wire.CodeProfileNotAuthorised {
		t.Fatalf("unknown profile: %d %s", st, code)
	}
	if err := dc.ReportProfiles(ctx, wire.ProfileReport{
		Profiles: []wire.ProfileReportEntry{{SID: sid, Username: "alice", ProfilePath: `C:\Users\alice`, State: wire.ProfileStateActiveDefault}},
		Consents: []wire.ProfileConsentRecord{{ConsentID: "c1", SID: sid, ConsentAt: 1000, ProofDigest: strings.Repeat("a", 64)}},
	}); err != nil {
		t.Fatal(err)
	}
	profJob, err := dc.CreateJob(ctx, prof)
	if err != nil || len(profJob.RequiredModules) != 0 {
		t.Fatalf("profile job: %+v %v", profJob, err)
	}
	if err := dc.ReportSecurityEvent(ctx, sid, wire.ProfileSecurityEventRequest{Signal: "password_changed", ObservedAt: time.Now().UnixMilli(), Evidence: "local_password_age"}); err != nil {
		t.Fatal(err)
	}
	if j, err := dc.Job(ctx, profJob.ID); err != nil || j.State != wire.JobStateSuspendedByProfile {
		t.Fatalf("suspended: %+v %v", j, err)
	}
	prof.Name = "Profile 2"
	if st, code := apiCode(t, func() error { _, err := dc.CreateJob(ctx, prof); return err }()); st != 423 || code != wire.CodeProfileBlacklisted {
		t.Fatalf("blacklisted create: %d %s", st, code)
	}
	cur, _ := dc.Job(ctx, profJob.ID)
	if cur, err = dc.UpdateJob(ctx, profJob.ID, cur.Revision, wire.JobUpdateRequest{Enabled: &off}); err != nil {
		t.Fatal(err)
	}
	if st, code := apiCode(t, func() error {
		_, err := dc.UpdateJob(ctx, profJob.ID, cur.Revision, wire.JobUpdateRequest{Enabled: &on})
		return err
	}()); st != 423 || code != wire.CodeProfileBlacklisted {
		t.Fatalf("blacklisted enable: %d %s", st, code)
	}
	if st, code := apiCode(t, func() error {
		_, err := dc.StartRun(ctx, profJob.ID, wire.RunStartRequest{ClientRunUUID: "p1", Trigger: wire.RunTriggerManual})
		return err
	}()); st != 423 || code != wire.CodeProfileBlacklisted {
		t.Fatalf("blacklisted run: %d %s", st, code)
	}

	// DELETE is session only.
	if st, code := apiCode(t, dc.Call(ctx, "DELETE", fmt.Sprintf("/api/v1/backup/jobs/%d", appJob.ID), nil, nil)); st != 403 || code != "session_required" {
		t.Fatalf("device delete: %d %s", st, code)
	}
	if code, _ := bob.req("DELETE", fmt.Sprintf("/api/v1/backup/jobs/%d", appJob.ID), nil); code != 404 {
		t.Fatalf("foreign delete: %d", code)
	}
	if code, _ := alice.req("DELETE", fmt.Sprintf("/api/v1/backup/jobs/%d", appJob.ID), nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
}

func TestBackupRunsIssuesRetrySkipped(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	bc := e.enrollDevice(bob, "bob", "password-bob")
	ctx := context.Background()
	job, err := dc.CreateJob(ctx, folderJob("Documents"))
	if err != nil {
		t.Fatal(err)
	}

	// Idempotent run start.
	start := wire.RunStartRequest{ClientRunUUID: "6f1c-run-1", Trigger: wire.RunTriggerSchedule}
	runID, err := dc.StartRun(ctx, job.ID, start)
	if err != nil {
		t.Fatal(err)
	}
	if again, err := dc.StartRun(ctx, job.ID, start); err != nil || again != runID {
		t.Fatalf("replay: %d %v", again, err)
	}
	if st, code := apiCode(t, func() error {
		_, err := dc.StartRun(ctx, job.ID, wire.RunStartRequest{ClientRunUUID: "x", Trigger: "cron"})
		return err
	}()); st != 422 || code != "validation" {
		t.Fatalf("bad trigger: %d %s", st, code)
	}
	if st, _ := apiCode(t, func() error { _, err := bc.StartRun(ctx, job.ID, start); return err }()); st != 404 {
		t.Fatalf("foreign device run: %d", st)
	}

	// Heartbeat, issues, finish.
	runPath := fmt.Sprintf("/api/v1/backup/runs/%d", runID)
	if err := dc.RunHeartbeat(ctx, runID, wire.RunProgress{Status: wire.RunStatusUploading, Phase: "upload", FilesTotal: 3, BytesDone: 10}); err != nil {
		t.Fatal(err)
	}
	if st, _ := apiCode(t, bc.RunHeartbeat(ctx, runID, wire.RunProgress{Status: wire.RunStatusUploading})); st != 404 {
		t.Fatalf("foreign heartbeat: %d", st)
	}
	if st, code := apiCode(t, dc.RunHeartbeat(ctx, runID, wire.RunProgress{Status: "bogus"})); st != 422 || code != "validation" {
		t.Fatalf("bad status: %d %s", st, code)
	}
	issues := []wire.IssueReport{
		{Path: "{APPDATA}/x.db", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Detail: "in use", Count: 1, Retryable: true},
		{Path: "{APPDATA}/x.db", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Detail: "in use", Count: 1, Retryable: true},
		{Path: "{DOCUMENTS}/a", Class: wire.IssueClassAccessDenied, Severity: wire.IssueSeveritySkipped, Count: 1},
	}
	if err := dc.ReportIssues(ctx, runID, issues); err != nil {
		t.Fatal(err)
	}
	if st, code := apiCode(t, dc.ReportIssues(ctx, runID, []wire.IssueReport{{Class: "weird", Severity: "skipped"}})); st != 422 || code != "validation" {
		t.Fatalf("bad issue: %d %s", st, code)
	}
	if err := dc.FinishRun(ctx, runID, wire.RunProgress{Status: wire.RunStatusPartial, FilesTotal: 3, FilesDone: 1}); err != nil {
		t.Fatal(err)
	}
	if st, code := apiCode(t, dc.RunHeartbeat(ctx, runID, wire.RunProgress{Status: wire.RunStatusUploading})); st != 409 || code != "run_finished" {
		t.Fatalf("heartbeat after finish: %d %s", st, code)
	}

	// Session views.
	run := alice.must("GET", runPath, nil, 200)
	if run["status"] != "partial" || run["trigger"] != "schedule" || run["finished_at"] == 0.0 || run["client_run_uuid"] != "6f1c-run-1" {
		t.Fatalf("run: %v", run)
	}
	if code, _ := bob.req("GET", runPath, nil); code != 404 {
		t.Fatalf("foreign run: %d", code)
	}
	if l := alice.must("GET", fmt.Sprintf("/api/v1/backup/runs?job=%d&status=partial", job.ID), nil, 200); len(l["items"].([]any)) != 1 {
		t.Fatalf("runs: %v", l)
	}
	if l := bob.must("GET", "/api/v1/backup/runs", nil, 200); len(l["items"].([]any)) != 0 {
		t.Fatalf("bob runs: %v", l)
	}
	il := alice.must("GET", runPath+"/issues", nil, 200)["items"].([]any)
	if len(il) != 2 || il[0].(map[string]any)["count"] != 2.0 || il[0].(map[string]any)["resolution"] != "open" {
		t.Fatalf("issues: %v", il)
	}
	accessID := il[1].(map[string]any)["id"]
	// The run's own device may list issues (agent Issues screen); another
	// user's device gets 404.
	if l, err := dc.RunIssues(ctx, runID, "", "", 0); err != nil || len(l.Items) != 2 {
		t.Fatalf("device issue list: %+v %v", l, err)
	}
	if st, _ := apiCode(t, func() error { _, err := bc.RunIssues(ctx, runID, "", "", 0); return err }()); st != 404 {
		t.Fatalf("foreign device issue list: %d", st)
	}
	if st, _ := apiCode(t, func() error {
		_, err := bc.SkipIssues(ctx, runID, wire.IssueSkipRequest{Class: wire.IssueClassAccessDenied})
		return err
	}()); st != 404 {
		t.Fatalf("foreign device skip: %d", st)
	}

	// Skip by id; validation of the skip body.
	res := alice.must("POST", runPath+"/issues/skip", map[string]any{"ids": []any{accessID, 99999}}, 200)
	if res["ok_count"] != 1.0 || len(res["failed"].([]any)) != 1 {
		t.Fatalf("skip: %v", res)
	}
	alice.must("POST", runPath+"/issues/skip", map[string]any{}, 422)
	if l := alice.must("GET", runPath+"/issues?resolution=skipped", nil, 200); len(l["items"].([]any)) != 1 {
		t.Fatalf("skipped filter: %v", l)
	}

	// Retry Skipped queues a retry_skipped task for the device.
	created := alice.must("POST", runPath+"/retry-skipped", nil, 201)
	taskID := int64(created["task_id"].(float64))
	tasks, err := dc.PollTasks(ctx, 0)
	if err != nil || len(tasks) != 1 || tasks[0].ID != taskID || tasks[0].Kind != wire.TaskKindRetrySkipped {
		t.Fatalf("poll: %+v %v", tasks, err)
	}
	// The device answers with a retry_skipped run, which takes the issue over.
	retryID, err := dc.StartRun(ctx, job.ID, wire.RunStartRequest{ClientRunUUID: "6f1c-run-2", Trigger: wire.RunTriggerRetrySkipped})
	if err != nil {
		t.Fatal(err)
	}
	if l := alice.must("GET", runPath+"/issues?resolution=retried", nil, 200)["items"].([]any); len(l) != 1 || l[0].(map[string]any)["retry_run_id"] != float64(retryID) {
		t.Fatalf("retried: %v", l)
	}
	if out := alice.must("POST", runPath+"/retry-skipped", nil, 409); errCode(out) != "nothing_to_retry" {
		t.Fatalf("nothing to retry: %v", out)
	}
	if err := dc.FinishRun(ctx, retryID, wire.RunProgress{Status: wire.RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	if l := alice.must("GET", runPath+"/issues?resolution=resolved", nil, 200)["items"].([]any); len(l) != 1 {
		t.Fatalf("resolved: %v", l)
	}
	if code, _ := bob.req("POST", runPath+"/retry-skipped", nil); code != 404 {
		t.Fatalf("foreign retry: %d", code)
	}
}
