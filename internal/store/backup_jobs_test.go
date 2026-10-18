package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func newTestJob(t *testing.T, st *store.Store, userID, deviceID int64, mods ...string) *store.BackupJob {
	t.Helper()
	j := &store.BackupJob{UserID: userID, DeviceID: deviceID, Name: "Documents", Kind: wire.JobKindFolder, Mode: wire.JobModeFull,
		Spec: wire.JobSpec{Paths: []string{"{DOCUMENTS}"}, UseVSS: wire.JobUseVSSAuto}, Schedule: wire.JobSchedule{Type: wire.JobScheduleManual},
		Enabled: true, RequiredModules: mods}
	if err := st.CreateJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}

func TestBackupJobCRUDRevision(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	j := newTestJob(t, st, u.ID, d.ID)
	if j.ID == 0 || j.Revision != 1 || j.State != wire.JobStateActive {
		t.Fatalf("created: %+v", j)
	}
	got, err := st.Job(ctx, j.ID)
	if err != nil || got.Name != "Documents" || len(got.Spec.Paths) != 1 || got.RepoID != 0 || got.RequiredModules == nil || !got.Enabled {
		t.Fatalf("get: %+v %v", got, err)
	}

	got.Name = "Docs"
	if err := st.UpdateJob(ctx, got, 1); err != nil || got.Revision != 2 {
		t.Fatalf("update: %v rev %d", err, got.Revision)
	}
	// A stale revision is a conflict and changes nothing.
	got.Name = "Lost"
	if err := st.UpdateJob(ctx, got, 1); !errors.Is(err, store.ErrRevisionConflict) {
		t.Fatalf("stale update: %v", err)
	}
	if cur, _ := st.Job(ctx, j.ID); cur.Name != "Docs" || cur.Revision != 2 {
		t.Fatalf("after conflict: %+v", cur)
	}
	if err := st.UpdateJob(ctx, &store.BackupJob{ID: 9999}, 1); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing job: %v", err)
	}

	newTestJob(t, st, u.ID, d.ID)
	other := newUser(t, st, "bob")
	newTestJob(t, st, other.ID, newProfileDevice(t, st, other.ID).ID)
	page, err := st.ListJobs(ctx, store.JobFilter{UserID: u.ID, Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != j.ID {
		t.Fatalf("page 1: %v %v", page, err)
	}
	page, _ = st.ListJobs(ctx, store.JobFilter{UserID: u.ID, AfterID: j.ID})
	if len(page) != 1 {
		t.Fatalf("page 2: %v", page)
	}
	if err := st.DeleteJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Job(ctx, j.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("deleted: %v", err)
	}
}

func TestBackupJobModuleSuspension(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	plain := newTestJob(t, st, u.ID, d.ID)
	vss := newTestJob(t, st, u.ID, d.ID, wire.ModuleVSS)

	sus, res, err := st.ApplyJobModules(ctx, d.ID, map[string]bool{wire.ModuleCore: true})
	if err != nil || len(sus) != 1 || sus[0] != vss.ID || len(res) != 0 {
		t.Fatalf("downgrade: %v %v %v", sus, res, err)
	}
	if j, _ := st.Job(ctx, vss.ID); j.State != wire.JobStateSuspendedByMode || j.Revision != 2 {
		t.Fatalf("suspended: %+v", j)
	}
	if j, _ := st.Job(ctx, plain.ID); j.State != wire.JobStateActive {
		t.Fatalf("plain job: %+v", j)
	}
	sus, res, err = st.ApplyJobModules(ctx, d.ID, map[string]bool{wire.ModuleCore: true, wire.ModuleVSS: true})
	if err != nil || len(sus) != 0 || len(res) != 1 {
		t.Fatalf("upgrade: %v %v %v", sus, res, err)
	}
	if j, _ := st.Job(ctx, vss.ID); j.State != wire.JobStateActive {
		t.Fatalf("resumed: %+v", j)
	}
}

func TestBackupRunsIdempotentHeartbeatIssues(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	j := newTestJob(t, st, u.ID, d.ID)

	run := &store.JobRun{JobID: j.ID, UserID: u.ID, DeviceID: d.ID, ClientRunUUID: "uuid-1", Trigger: wire.RunTriggerManual}
	created, err := st.StartRun(ctx, run)
	if err != nil || !created || run.ID == 0 || run.Progress.Status != wire.RunStatusQueued {
		t.Fatalf("start: %v %v %+v", created, err, run)
	}
	again := &store.JobRun{JobID: j.ID, UserID: u.ID, DeviceID: d.ID, ClientRunUUID: "uuid-1", Trigger: wire.RunTriggerManual}
	created, err = st.StartRun(ctx, again)
	if err != nil || created || again.ID != run.ID {
		t.Fatalf("replay: %v %v %d", created, err, again.ID)
	}

	p := wire.RunProgress{Status: wire.RunStatusUploading, Phase: "upload", FilesTotal: 10, FilesDone: 4, QueuePacks: 2, ETAMS: 5000}
	got, err := st.UpdateRun(ctx, run.ID, p)
	if err != nil || got.Progress != p || got.FinishedAt != nil {
		t.Fatalf("heartbeat: %+v %v", got, err)
	}

	issues := []wire.IssueReport{
		{Path: "{APPDATA}/x.db", Class: wire.IssueClassLocked, Severity: wire.IssueSeverityWarning, Detail: "in use", FirstSeen: 100, LastSeen: 100, Count: 1, Retryable: true},
		{Path: "{APPDATA}/x.db", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Detail: "still in use", FirstSeen: 200, LastSeen: 300, Count: 2, Retryable: true},
		{Path: "{DOCUMENTS}/y", Class: wire.IssueClassAccessDenied, Severity: wire.IssueSeveritySkipped, Count: 1},
		{Path: "{DOCUMENTS}/z", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Count: 1, Retryable: true},
	}
	if err := st.ReportIssues(ctx, run.ID, issues); err != nil {
		t.Fatal(err)
	}
	list, err := st.ListIssues(ctx, store.IssueFilter{RunID: run.ID})
	if err != nil || len(list) != 3 {
		t.Fatalf("issues: %+v %v", list, err)
	}
	x := list[0]
	if x.Count != 3 || x.FirstSeen != 100 || x.LastSeen != 300 || x.Severity != wire.IssueSeveritySkipped || x.Detail != "still in use" || x.Resolution != wire.IssueResolutionOpen {
		t.Fatalf("dedup: %+v", x)
	}

	// Skip by id, then by class.
	res, err := st.SkipIssues(ctx, run.ID, []int64{list[1].ID, 9999}, "")
	if err != nil || res.OKCount != 1 || len(res.Failed) != 1 || res.Failed[0].Code != "not_found" {
		t.Fatalf("skip ids: %+v %v", res, err)
	}
	if ids, _ := st.RetryableIssueIDs(ctx, run.ID, 10); len(ids) != 2 {
		t.Fatalf("retryable: %v", ids)
	}

	got, err = st.UpdateRun(ctx, run.ID, wire.RunProgress{Status: wire.RunStatusPartial})
	if err != nil || got.FinishedAt == nil || got.Skipped != 3 {
		t.Fatalf("finish: %+v %v", got, err)
	}
	if _, err := st.UpdateRun(ctx, run.ID, p); !errors.Is(err, store.ErrRunFinished) {
		t.Fatalf("heartbeat after finish: %v", err)
	}
	if _, err := st.UpdateRun(ctx, 9999, p); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing run: %v", err)
	}

	// A retry_skipped run takes over the open retryable skipped issues; when
	// it completes without reporting x.db again, x.db is resolved, while z
	// reported again stays retried.
	retry := &store.JobRun{JobID: j.ID, UserID: u.ID, DeviceID: d.ID, ClientRunUUID: "uuid-2", Trigger: wire.RunTriggerRetrySkipped}
	if _, err := st.StartRun(ctx, retry); err != nil {
		t.Fatal(err)
	}
	retried, _ := st.ListIssues(ctx, store.IssueFilter{RunID: run.ID, Resolution: wire.IssueResolutionRetried})
	if len(retried) != 2 || *retried[0].RetryRunID != retry.ID {
		t.Fatalf("retried: %+v", retried)
	}
	if err := st.ReportIssues(ctx, retry.ID, []wire.IssueReport{{Path: "{DOCUMENTS}/z", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Retryable: true}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(ctx, retry.ID, wire.RunProgress{Status: wire.RunStatusCompleted}); err != nil {
		t.Fatal(err)
	}
	resolved, _ := st.ListIssues(ctx, store.IssueFilter{RunID: run.ID, Resolution: wire.IssueResolutionResolved})
	if len(resolved) != 1 || resolved[0].Path != "{APPDATA}/x.db" {
		t.Fatalf("resolved: %+v", resolved)
	}
	if skipped, _ := st.ListIssues(ctx, store.IssueFilter{RunID: run.ID, Resolution: wire.IssueResolutionSkipped}); len(skipped) != 1 {
		t.Fatalf("skipped: %+v", skipped)
	}

	// Skip by class; a failed retry puts its issues back to open.
	res, _ = st.SkipIssues(ctx, retry.ID, nil, wire.IssueClassLocked)
	if res.OKCount != 1 {
		t.Fatalf("skip class: %+v", res)
	}
	if l, _ := st.ListIssues(ctx, store.IssueFilter{RunID: retry.ID, Resolution: wire.IssueResolutionSkippedAll}); len(l) != 1 {
		t.Fatalf("skipped_all: %+v", l)
	}

	runs, err := st.ListRuns(ctx, store.RunFilter{UserID: u.ID, JobID: j.ID})
	if err != nil || len(runs) != 2 || runs[0].ID != retry.ID || runs[0].Trigger != wire.RunTriggerRetrySkipped {
		t.Fatalf("runs: %+v %v", runs, err)
	}
	if runs, _ := st.ListRuns(ctx, store.RunFilter{UserID: u.ID, Status: wire.RunStatusPartial}); len(runs) != 1 {
		t.Fatalf("status filter: %+v", runs)
	}
}

func TestBackupRetryRunFailureReopens(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newProfileDevice(t, st, u.ID)
	j := newTestJob(t, st, u.ID, d.ID)
	run := &store.JobRun{JobID: j.ID, UserID: u.ID, DeviceID: d.ID, ClientRunUUID: "a", Trigger: wire.RunTriggerSchedule}
	if _, err := st.StartRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	_ = st.ReportIssues(ctx, run.ID, []wire.IssueReport{{Path: "p", Class: wire.IssueClassLocked, Severity: wire.IssueSeveritySkipped, Retryable: true}})
	retry := &store.JobRun{JobID: j.ID, UserID: u.ID, DeviceID: d.ID, ClientRunUUID: "b", Trigger: wire.RunTriggerRetrySkipped}
	if _, err := st.StartRun(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpdateRun(ctx, retry.ID, wire.RunProgress{Status: wire.RunStatusFailed}); err != nil {
		t.Fatal(err)
	}
	l, _ := st.ListIssues(ctx, store.IssueFilter{RunID: run.ID})
	if len(l) != 1 || l[0].Resolution != wire.IssueResolutionOpen || l[0].RetryRunID != nil {
		t.Fatalf("reopened: %+v", l)
	}
}
