// Owner: SRV-3 (P1-18). Scope: api-v1 §6 jobs, runs and issues; routes and
// shapes per rev 1.0 (sdk/contracts/history/api-v1-rev1.0.md §6) plus the
// rev 1.1 job fields profile_sid, required_modules and state.
//
// Gating: creating a job, enabling it (or changing what it needs), starting
// a run and Retry Skipped check the job's Windows profile
// (requireProfileAuthorised: 423 / 403) and its required modules
// (requireModules: 409 module_unavailable). A device sees and edits only its
// own device's jobs and runs.
//
// The backup.v1 flag is not advertised here: it needs repositories and
// snapshots too (P1-15 calls implementFeature).

package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/schedule"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// mountBackupJobs registers the routes of this feature area.
func (s *Server) mountBackupJobs(rt routes) {
	rt.User.Get("/backup/jobs", s.handleListJobs)           // ?device=&cursor=; dev: own device only
	rt.User.Post("/backup/jobs", s.handleCreateJob)         // dev: own device only; 409 module_unavailable
	rt.User.Patch("/backup/jobs/{id}", s.handlePatchJob)    // dev: own device only; If-Match revision
	rt.Sess.Delete("/backup/jobs/{id}", s.handleDeleteJob)  //
	rt.Dev.Post("/backup/jobs/{id}/runs", s.handleStartRun) // idempotent by client_run_uuid
	rt.Sess.Get("/backup/runs", s.handleListRuns)
	rt.Sess.Get("/backup/runs/{id}", s.handleGetRun)
	rt.Dev.Patch("/backup/runs/{id}", s.handleRunHeartbeat)
	rt.Dev.Post("/backup/runs/{id}/issues", s.handleReportIssues)
	rt.User.Get("/backup/runs/{id}/issues", s.handleListIssues)           // dev: own device's runs only
	rt.User.Post("/backup/runs/{id}/issues/skip", s.handleSkipIssues)     // dev: own device's runs only
	rt.User.Post("/backup/runs/{id}/retry-skipped", s.handleRetrySkipped) // dev: own device's runs only
}

// Validation limits for jobs and issues.
const (
	maxJobName       = 200
	maxJobPaths      = 1000
	maxJobPathLen    = 4096
	maxJobSourceRef  = 1024
	maxJobPriority   = 1000
	maxRunUUID       = 64
	maxRunPhase      = 64
	maxIssueDetail   = 2000
	maxIssueSkipIDs  = 1000
	maxProfileSIDLen = 184
)

var (
	jobKinds       = []string{wire.JobKindFolder, wire.JobKindProfile, wire.JobKindApp, wire.JobKindGame, wire.JobKindDevProject, wire.JobKindRegistry, wire.JobKindInventory}
	jobModes       = []string{wire.JobModeFull, wire.JobModeDataOnly, wire.JobModeReinstallInventory}
	jobVSS         = []string{wire.JobUseVSSAuto, wire.JobUseVSSRequired, wire.JobUseVSSOff}
	runTriggers    = []string{wire.RunTriggerSchedule, wire.RunTriggerManual, wire.RunTriggerAutomation, wire.RunTriggerRetrySkipped, wire.RunTriggerTask}
	runStatuses    = []string{wire.RunStatusQueued, wire.RunStatusScanning, wire.RunStatusUploading, wire.RunStatusCommitting, wire.RunStatusCompleted, wire.RunStatusPartial, wire.RunStatusFailed, wire.RunStatusCancelled}
	issueSeverity  = []string{wire.IssueSeverityWarning, wire.IssueSeveritySkipped, wire.IssueSeverityError}
	issueClasses   = []string{wire.IssueClassLocked, wire.IssueClassAccessDenied, wire.IssueClassNotFound, wire.IssueClassPathTooLong, wire.IssueClassIO, wire.IssueClassNetwork, wire.IssueClassServer, wire.IssueClassCrypto, wire.IssueClassCloudPlaceholder, wire.IssueClassVSS, wire.IssueClassOther, wire.IssueClassProfileUnauthorised, wire.IssueClassModuleUnavailable, wire.IssueClassGuardBlocked}
	discoveryKinds = []string{wire.JobKindApp, wire.JobKindGame, wire.JobKindDevProject, wire.JobKindRegistry, wire.JobKindInventory}
	runUUIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]+$`)
)

// jobValidation is 422 validation with details.fields.
func jobValidation(fields map[string]string) *apiError {
	return &apiError{Status: 422, Code: "validation", Message: "Invalid request", Details: map[string]any{"fields": fields}}
}

// --- views ---

func jobView(j *store.BackupJob) wire.Job {
	return wire.Job{ID: j.ID, DeviceID: j.DeviceID, RepoID: j.RepoID, Name: j.Name, Kind: j.Kind, Mode: j.Mode, Spec: j.Spec,
		Schedule: j.Schedule, RetentionPolicyID: j.RetentionPolicyID, Priority: j.Priority, Enabled: j.Enabled, Revision: j.Revision,
		ProfileSID: j.ProfileSID, RequiredModules: j.RequiredModules, State: j.State, CreatedAt: j.CreatedAt, UpdatedAt: j.UpdatedAt}
}

func runView(r *store.JobRun) wire.Run {
	v := wire.Run{ID: r.ID, JobID: r.JobID, DeviceID: r.DeviceID, ClientRunUUID: r.ClientRunUUID, Trigger: r.Trigger,
		SnapshotID: r.SnapshotID, StartedAt: r.StartedAt, RunProgress: r.Progress}
	if r.FinishedAt != nil {
		v.FinishedAt = *r.FinishedAt
	}
	return v
}

// --- validation and gating ---

// validateJob checks and normalises the writable fields of j (defaults:
// mode full, use_vss auto, schedule manual).
func validateJob(j *store.BackupJob) map[string]string {
	f := map[string]string{}
	j.Name = strings.TrimSpace(j.Name)
	if j.Name == "" || utf8.RuneCountInString(j.Name) > maxJobName || strings.ContainsFunc(j.Name, isCtl) {
		f["name"] = fmt.Sprintf("required, at most %d characters", maxJobName)
	}
	if !slices.Contains(jobKinds, j.Kind) {
		f["kind"] = "unknown kind"
	}
	if j.Mode == "" {
		j.Mode = wire.JobModeFull
	}
	if !slices.Contains(jobModes, j.Mode) {
		f["mode"] = "unknown mode"
	}
	if j.Spec.UseVSS == "" {
		j.Spec.UseVSS = wire.JobUseVSSAuto
	}
	if !slices.Contains(jobVSS, j.Spec.UseVSS) {
		f["spec.use_vss"] = "must be auto, required or off"
	}
	if msg := checkPaths(j.Spec.Paths); msg != "" {
		f["spec.paths"] = msg
	}
	if msg := checkPaths(j.Spec.Exclude); msg != "" {
		f["spec.exclude"] = msg
	}
	if len(j.Spec.SourceRef) > maxJobSourceRef || strings.ContainsFunc(j.Spec.SourceRef, isCtl) {
		f["spec.source_ref"] = "too long or invalid"
	}
	if j.Schedule.Type == "" {
		j.Schedule.Type = wire.JobScheduleManual
	}
	if err := schedule.Schedule(j.Schedule).Validate(); err != nil {
		f["schedule"] = err.Error()
	}
	if j.Priority < -maxJobPriority || j.Priority > maxJobPriority {
		f["priority"] = fmt.Sprintf("must be %d..%d", -maxJobPriority, maxJobPriority)
	}
	if j.ProfileSID != "" && (len(j.ProfileSID) > maxProfileSIDLen || !sidPattern.MatchString(j.ProfileSID)) {
		f["profile_sid"] = "must be a Windows SID"
	}
	if j.RepoID < 0 {
		f["repo_id"] = "invalid"
	}
	return f
}

func checkPaths(ps []string) string {
	if len(ps) > maxJobPaths {
		return fmt.Sprintf("at most %d entries", maxJobPaths)
	}
	for _, p := range ps {
		if p == "" || len(p) > maxJobPathLen || strings.ContainsFunc(p, isCtl) {
			return "entries must be non-empty paths without control characters"
		}
	}
	return ""
}

func isCtl(r rune) bool { return r < 0x20 || r == 0x7f }

// checkJobRefs verifies that the repository and retention policy belong to
// the job owner.
func (s *Server) checkJobRefs(ctx context.Context, j *store.BackupJob) error {
	f := map[string]string{}
	if j.RepoID > 0 {
		ok, err := s.st.OwnedRef(ctx, "repositories", j.RepoID, j.UserID)
		if err != nil {
			return err
		}
		if !ok {
			f["repo_id"] = "unknown repository"
		}
	}
	if j.RetentionPolicyID != nil {
		ok, err := s.st.OwnedRef(ctx, "retention_policies", *j.RetentionPolicyID, j.UserID)
		if err != nil {
			return err
		}
		if !ok {
			f["retention_policy_id"] = "unknown retention policy"
		}
	}
	if len(f) > 0 {
		return jobValidation(f)
	}
	return nil
}

// jobModules computes required_modules (api-v1 §6): discovery for app,
// game, devproject, registry and inventory jobs; vss for use_vss required;
// profiles when the job reads a Windows profile other than the active
// user's (any state but active_default). Core is never listed.
func (s *Server) jobModules(ctx context.Context, j *store.BackupJob) ([]string, error) {
	mods := []string{}
	if slices.Contains(discoveryKinds, j.Kind) {
		mods = append(mods, wire.ModuleDiscovery)
	}
	if j.Spec.UseVSS == wire.JobUseVSSRequired {
		mods = append(mods, wire.ModuleVSS)
	}
	if j.ProfileSID != "" {
		p, err := s.st.DeviceProfileBySID(ctx, j.DeviceID, j.ProfileSID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
		if p == nil || p.State != wire.ProfileStateActiveDefault {
			mods = append(mods, wire.ModuleProfiles)
		}
	}
	return mods, nil
}

// gateJob is the create/enable gate: the profile must be authorised (423
// profile_blacklisted / 403 profile_not_authorised) and the device must
// have every required module (409 module_unavailable).
func (s *Server) gateJob(ctx context.Context, j *store.BackupJob) error {
	if j.ProfileSID != "" {
		if err := s.requireProfileAuthorised(ctx, j.DeviceID, j.ProfileSID); err != nil {
			return err
		}
	}
	return s.requireModules(ctx, j.DeviceID, j.RequiredModules...)
}

// applyJobModuleState moves the device's jobs between active and
// suspended_by_mode from its current module report. modules.go calls it
// after PUT /device/modules (see the P1-18 report).
func (s *Server) applyJobModuleState(ctx context.Context, deviceID int64) error {
	have, err := s.enabledModules(ctx, deviceID)
	if err != nil {
		return err
	}
	set := make(map[string]bool, len(have))
	for _, m := range have {
		set[m] = true
	}
	_, _, err = s.st.ApplyJobModules(ctx, deviceID, set)
	return err
}

// --- jobs ---

// loadJob returns job {id} if the caller may touch it: a device only its
// own device's jobs, a session only the user's jobs. Others get 404.
func (s *Server) loadJob(r *http.Request) (*store.BackupJob, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	j, err := s.st.Job(r.Context(), id)
	if err != nil {
		return nil, err
	}
	p := principal(r)
	if (p.Device != nil && j.DeviceID != p.Device.ID) || j.UserID != p.User.ID {
		return nil, store.ErrNotFound
	}
	return j, nil
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	f := store.JobFilter{UserID: p.User.ID}
	if v := r.URL.Query().Get("device"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			writeErr(w, r, jobValidation(map[string]string{"device": "invalid device id"}))
			return
		}
		f.DeviceID = n
	}
	if p.Device != nil {
		if f.DeviceID != 0 && f.DeviceID != p.Device.ID {
			writeErr(w, r, errf(403, "forbidden", "A device can only list its own jobs"))
			return
		}
		f.DeviceID = p.Device.ID
	}
	var err error
	if f.AfterID, f.Limit, err = listParams(r); err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListJobs(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Job]{Items: make([]wire.Job, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, jobView(&list[i]))
	}
	if len(list) == f.Limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req wire.JobCreateRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	p := principal(r)
	if p.Device != nil {
		if req.DeviceID == 0 {
			req.DeviceID = p.Device.ID
		}
		if req.DeviceID != p.Device.ID {
			writeErr(w, r, errf(403, "forbidden", "A device can only create jobs for itself"))
			return
		}
	}
	ctx := r.Context()
	d, err := s.st.Device(ctx, req.DeviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && d.UserID != p.User.ID) {
		writeErr(w, r, jobValidation(map[string]string{"device_id": "unknown device"}))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if d.Status != "approved" {
		writeErr(w, r, errf(409, "device_not_approved", "The device is not approved"))
		return
	}
	j := &store.BackupJob{UserID: d.UserID, DeviceID: d.ID, RepoID: req.RepoID, Name: req.Name, Kind: req.Kind, Mode: req.Mode,
		Spec: req.Spec, Schedule: req.Schedule, RetentionPolicyID: req.RetentionPolicyID, Priority: req.Priority,
		Enabled: req.Enabled, ProfileSID: req.ProfileSID, State: wire.JobStateActive}
	if f := validateJob(j); len(f) > 0 {
		writeErr(w, r, jobValidation(f))
		return
	}
	if err := s.checkJobRefs(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	if j.RequiredModules, err = s.jobModules(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.gateJob(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.CreateJob(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, j.UserID, "backup.job_created", j.Name, fmt.Sprintf("job_id=%d device_id=%d kind=%s", j.ID, j.DeviceID, j.Kind), false)
	writeJSON(w, http.StatusCreated, jobView(j))
}

// ifMatchRevision parses If-Match (a revision, optionally quoted); 0 when
// absent.
func ifMatchRevision(r *http.Request) (int64, error) {
	v := strings.TrimSpace(r.Header.Get("If-Match"))
	if v == "" {
		return 0, nil
	}
	v = strings.Trim(strings.TrimPrefix(v, "W/"), `"`)
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, errf(400, "bad_if_match", "If-Match must be the job revision")
	}
	return n, nil
}

func errRevisionConflict(current int64) *apiError {
	return &apiError{Status: 409, Code: wire.CodeConflict, Message: "The job was changed meanwhile; reload it and try again",
		Details: map[string]any{"current_revision": current}}
}

func (s *Server) handlePatchJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.loadJob(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	rev, err := ifMatchRevision(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if rev != 0 && rev != j.Revision {
		writeErr(w, r, errRevisionConflict(j.Revision))
		return
	}
	var req wire.JobUpdateRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	wasEnabled, oldMods, oldSID := j.Enabled, slices.Clone(j.RequiredModules), j.ProfileSID
	if req.Name != nil {
		j.Name = *req.Name
	}
	if req.Mode != nil {
		j.Mode = *req.Mode
	}
	if req.Spec != nil {
		j.Spec = *req.Spec
	}
	if req.Schedule != nil {
		j.Schedule = *req.Schedule
	}
	if req.RetentionPolicyID != nil {
		j.RetentionPolicyID = req.RetentionPolicyID
	}
	if req.Priority != nil {
		j.Priority = *req.Priority
	}
	if req.Enabled != nil {
		j.Enabled = *req.Enabled
	}
	if req.ProfileSID != nil {
		j.ProfileSID = *req.ProfileSID
	}
	if f := validateJob(j); len(f) > 0 {
		writeErr(w, r, jobValidation(f))
		return
	}
	if err := s.checkJobRefs(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	if j.RequiredModules, err = s.jobModules(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	// Enabling a job, or changing what an enabled job needs, passes the gate
	// again; a suspended job that passes it becomes active.
	if j.Enabled && (!wasEnabled || !slices.Equal(oldMods, j.RequiredModules) || oldSID != j.ProfileSID || j.State != wire.JobStateActive) {
		if err := s.gateJob(ctx, j); err != nil {
			writeErr(w, r, err)
			return
		}
		j.State = wire.JobStateActive
	}
	if err := s.st.UpdateJob(ctx, j, j.Revision); err != nil {
		if errors.Is(err, store.ErrRevisionConflict) {
			cur := int64(0)
			if now, e := s.st.Job(ctx, j.ID); e == nil {
				cur = now.Revision
			}
			err = errRevisionConflict(cur)
		}
		writeErr(w, r, err)
		return
	}
	s.activity(r, j.UserID, "backup.job_updated", j.Name, fmt.Sprintf("job_id=%d revision=%d", j.ID, j.Revision), false)
	writeJSON(w, 200, jobView(j))
}

func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	j, err := s.loadJob(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.DeleteJob(r.Context(), j.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, j.UserID, "backup.job_deleted", j.Name, fmt.Sprintf("job_id=%d", j.ID), true)
	w.WriteHeader(http.StatusNoContent)
}

// --- runs ---

func (s *Server) handleStartRun(w http.ResponseWriter, r *http.Request) {
	j, err := s.loadJob(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.RunStartRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	f := map[string]string{}
	if req.ClientRunUUID == "" || len(req.ClientRunUUID) > maxRunUUID || !runUUIDPattern.MatchString(req.ClientRunUUID) {
		f["client_run_uuid"] = "required, at most 64 characters [A-Za-z0-9._:-]"
	}
	if !slices.Contains(runTriggers, req.Trigger) {
		f["trigger"] = "unknown trigger"
	}
	if len(f) > 0 {
		writeErr(w, r, jobValidation(f))
		return
	}
	ctx := r.Context()
	// A replay returns the run already opened, without gating again.
	if old, err := s.st.RunByUUID(ctx, j.ID, req.ClientRunUUID); err == nil {
		writeJSON(w, 200, wire.RunStartResponse{RunID: old.ID})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, err)
		return
	}
	if err := s.gateJob(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	run := &store.JobRun{JobID: j.ID, UserID: j.UserID, DeviceID: j.DeviceID, ClientRunUUID: req.ClientRunUUID, Trigger: req.Trigger}
	created, err := s.st.StartRun(ctx, run)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, wire.RunStartResponse{RunID: run.ID})
}

// loadRun returns run {id} if the caller may touch it: a device only runs
// of its own device, a session only the user's runs. Others get 404.
func (s *Server) loadRun(r *http.Request) (*store.JobRun, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	run, err := s.st.Run(r.Context(), id)
	if err != nil {
		return nil, err
	}
	p := principal(r)
	if (p.Device != nil && run.DeviceID != p.Device.ID) || run.UserID != p.User.ID {
		return nil, store.ErrNotFound
	}
	return run, nil
}

func (s *Server) handleRunHeartbeat(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var p wire.RunProgress
	if err := decode(r, &p); err != nil {
		writeErr(w, r, err)
		return
	}
	f := map[string]string{}
	if !slices.Contains(runStatuses, p.Status) {
		f["status"] = "unknown status"
	}
	if len(p.Phase) > maxRunPhase || strings.ContainsFunc(p.Phase, isCtl) {
		f["phase"] = "too long or invalid"
	}
	for name, v := range map[string]int64{"files_total": p.FilesTotal, "files_done": p.FilesDone, "bytes_total": p.BytesTotal,
		"bytes_done": p.BytesDone, "bytes_new": p.BytesNew, "bytes_uploaded": p.BytesUploaded, "queue_packs": p.QueuePacks,
		"warnings": p.Warnings, "errors": p.Errors, "eta_ms": p.ETAMS} {
		if v < 0 {
			f[name] = "must not be negative"
		}
	}
	if len(f) > 0 {
		writeErr(w, r, jobValidation(f))
		return
	}
	if _, err := s.st.UpdateRun(r.Context(), run.ID, p); err != nil {
		if errors.Is(err, store.ErrRunFinished) {
			err = errf(409, "run_finished", "The run has already finished")
		}
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.RunFilter{UserID: principal(r).User.ID, Status: q.Get("status")}
	fields := map[string]string{}
	if f.Status != "" && !slices.Contains(runStatuses, f.Status) {
		fields["status"] = "unknown status"
	}
	for name, dst := range map[string]*int64{"job": &f.JobID, "device": &f.DeviceID} {
		if v := q.Get(name); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil || n <= 0 {
				fields[name] = "invalid id"
			}
			*dst = n
		}
	}
	if len(fields) > 0 {
		writeErr(w, r, jobValidation(fields))
		return
	}
	var err error
	if f.BeforeID, f.Limit, err = listParams(r); err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListRuns(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Run]{Items: make([]wire.Run, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, runView(&list[i]))
	}
	if len(list) == f.Limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, runView(run))
}

// --- issues ---

func (s *Server) handleReportIssues(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.IssueBatchRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Issues) > wire.IssueBatchMax {
		writeErr(w, r, jobValidation(map[string]string{"issues": fmt.Sprintf("at most %d per call", wire.IssueBatchMax)}))
		return
	}
	f := map[string]string{}
	for i := range req.Issues {
		in := &req.Issues[i]
		key := fmt.Sprintf("issues[%d]", i)
		switch {
		case len(in.Path) > maxJobPathLen || strings.ContainsFunc(in.Path, isCtl):
			f[key+".path"] = "too long or invalid"
		case !slices.Contains(issueClasses, in.Class):
			f[key+".class"] = "unknown class"
		case !slices.Contains(issueSeverity, in.Severity):
			f[key+".severity"] = "unknown severity"
		case in.Count < 0 || in.FirstSeen < 0 || in.LastSeen < 0:
			f[key] = "count and timestamps must not be negative"
		}
		in.Detail = truncateRunes(strings.Map(func(r rune) rune {
			if isCtl(r) && r != '\n' && r != '\t' {
				return -1
			}
			return r
		}, in.Detail), maxIssueDetail)
	}
	if len(f) > 0 {
		writeErr(w, r, jobValidation(f))
		return
	}
	if err := s.st.ReportIssues(r.Context(), run.ID, req.Issues); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func (s *Server) handleListIssues(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.IssueFilter{RunID: run.ID, Class: q.Get("class"), Resolution: q.Get("resolution")}
	fields := map[string]string{}
	if f.Class != "" && !slices.Contains(issueClasses, f.Class) {
		fields["class"] = "unknown class"
	}
	if f.Resolution != "" && !store.ValidIssueResolution(f.Resolution) {
		fields["resolution"] = "unknown resolution"
	}
	if len(fields) > 0 {
		writeErr(w, r, jobValidation(fields))
		return
	}
	if f.AfterID, f.Limit, err = listParams(r); err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListIssues(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Issue]{Items: list}
	if len(list) == f.Limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleSkipIssues(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.IssueSkipRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	switch {
	case (len(req.IDs) == 0) == (req.Class == ""):
		writeErr(w, r, jobValidation(map[string]string{"ids": "give either ids or class"}))
		return
	case len(req.IDs) > maxIssueSkipIDs:
		writeErr(w, r, jobValidation(map[string]string{"ids": fmt.Sprintf("at most %d ids", maxIssueSkipIDs)}))
		return
	case req.Class != "" && !slices.Contains(issueClasses, req.Class):
		writeErr(w, r, jobValidation(map[string]string{"class": "unknown class"}))
		return
	}
	res, err := s.st.SkipIssues(r.Context(), run.ID, req.IDs, req.Class)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, run.UserID, "backup.issues_skipped", "", fmt.Sprintf("run_id=%d ok=%d class=%s", run.ID, res.OKCount, req.Class), false)
	writeJSON(w, 200, res)
}

// handleRetrySkipped queues a retry_skipped device task for a run's open,
// retryable skipped issues; the device answers it with a run started with
// trigger retry_skipped, which takes the issues over.
func (s *Server) handleRetrySkipped(w http.ResponseWriter, r *http.Request) {
	run, err := s.loadRun(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	j, err := s.st.Job(ctx, run.JobID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ids, err := s.st.RetryableIssueIDs(ctx, run.ID, 1)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(ids) == 0 {
		writeErr(w, r, errf(409, "nothing_to_retry", "This run has no open retryable skipped items"))
		return
	}
	if err := s.gateJob(ctx, j); err != nil {
		writeErr(w, r, err)
		return
	}
	uid := principal(r).User.ID
	out, err := s.CreateTask(ctx, TaskRequest{UserID: j.UserID, DeviceID: j.DeviceID, Kind: wire.TaskKindRetrySkipped,
		Payload: wire.TaskRetrySkippedPayload{RunID: run.ID}, RequiredModules: slices.Clone(j.RequiredModules), CreatedBy: &uid})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, j.UserID, "backup.retry_skipped", j.Name, fmt.Sprintf("run_id=%d task_id=%d", run.ID, out.TaskID), false)
	writeJSON(w, http.StatusCreated, out)
}
