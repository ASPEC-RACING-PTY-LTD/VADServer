package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Backup jobs, runs and issues (api-v1 §6; shapes per rev 1.0 §6).
//
// Storage notes (schema migration 4):
//   - job_runs.source holds the run trigger; phase, queue_packs and eta_ms
//     of the last heartbeat live in job_runs.summary (JSON).
//   - run_issues.message holds {"detail","first_seen","last_seen","count"}
//     as JSON, because the table has no columns for those fields.
//   - run_issues.skip is 0 (open), 1 (skipped) or 2 (skipped_all).
//     resolved_at set means resolved; retry_run_id set (and not skipped or
//     resolved) means retried.

var (
	// ErrRevisionConflict is returned when If-Match does not match the job
	// revision (409 conflict).
	ErrRevisionConflict = errors.New("revision conflict")
	// ErrRunFinished is returned for a heartbeat on a finished run.
	ErrRunFinished = errors.New("run finished")
)

// Issue skip values stored in run_issues.skip.
const (
	issueSkipNone = 0
	issueSkipOne  = 1
	issueSkipAll  = 2
)

// BackupJob is one row of backup_jobs. RepoID 0 means no repository.
type BackupJob struct {
	ID                int64
	UserID            int64
	DeviceID          int64
	RepoID            int64
	Name              string
	Kind              string
	Mode              string
	Spec              wire.JobSpec
	Schedule          wire.JobSchedule
	RetentionPolicyID *int64
	Priority          int
	Enabled           bool
	ProfileSID        string
	RequiredModules   []string
	State             string
	Revision          int64
	CreatedAt         int64
	UpdatedAt         int64
}

const jobCols = "id, user_id, device_id, repo_id, name, kind, mode, spec, schedule, retention_policy_id, priority, enabled, profile_sid, required_modules, state, revision, created_at, updated_at"

func scanJob(sc interface{ Scan(...any) error }) (*BackupJob, error) {
	var j BackupJob
	var repo, ret sql.NullInt64
	var spec, sched, mods string
	err := sc.Scan(&j.ID, &j.UserID, &j.DeviceID, &repo, &j.Name, &j.Kind, &j.Mode, &spec, &sched, &ret, &j.Priority,
		&j.Enabled, &j.ProfileSID, &mods, &j.State, &j.Revision, &j.CreatedAt, &j.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.RepoID = repo.Int64
	j.RetentionPolicyID = nullInt(ret)
	if err := json.Unmarshal([]byte(jsonOr(spec, "{}")), &j.Spec); err != nil {
		return nil, fmt.Errorf("backup job %d: spec: %w", j.ID, err)
	}
	if err := json.Unmarshal([]byte(jsonOr(sched, "{}")), &j.Schedule); err != nil {
		return nil, fmt.Errorf("backup job %d: schedule: %w", j.ID, err)
	}
	if err := json.Unmarshal([]byte(jsonOr(mods, "[]")), &j.RequiredModules); err != nil {
		return nil, fmt.Errorf("backup job %d: required_modules: %w", j.ID, err)
	}
	normaliseJob(&j)
	return &j, nil
}

func normaliseJob(j *BackupJob) {
	if j.RequiredModules == nil {
		j.RequiredModules = []string{}
	}
	if j.Spec.Paths == nil {
		j.Spec.Paths = []string{}
	}
	if j.Spec.Exclude == nil {
		j.Spec.Exclude = []string{}
	}
	if j.Schedule.Weekdays == nil {
		j.Schedule.Weekdays = []int{}
	}
}

func jobJSON(j *BackupJob) (spec, sched, mods string, err error) {
	normaliseJob(j)
	b1, err := json.Marshal(j.Spec)
	if err != nil {
		return
	}
	b2, err := json.Marshal(j.Schedule)
	if err != nil {
		return
	}
	b3, err := json.Marshal(j.RequiredModules)
	return string(b1), string(b2), string(b3), err
}

func nullID(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// CreateJob inserts a job (revision 1, state active unless set). It sets
// j.ID, j.Revision and the timestamps.
func (s *Store) CreateJob(ctx context.Context, j *BackupJob) error {
	if j.UserID <= 0 || j.DeviceID <= 0 || j.Name == "" || j.Kind == "" {
		return errors.New("backup job user, device, name and kind required")
	}
	if j.State == "" {
		j.State = wire.JobStateActive
	}
	spec, sched, mods, err := jobJSON(j)
	if err != nil {
		return err
	}
	j.CreatedAt = now()
	j.UpdatedAt, j.Revision = j.CreatedAt, 1
	res, err := s.DB.ExecContext(ctx, `INSERT INTO backup_jobs(user_id, device_id, repo_id, name, kind, mode, spec, schedule, retention_policy_id, priority, enabled, profile_sid, required_modules, state, revision, created_at, updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, j.UserID, j.DeviceID, nullID(j.RepoID), j.Name, j.Kind, j.Mode, spec, sched,
		j.RetentionPolicyID, j.Priority, j.Enabled, j.ProfileSID, mods, j.State, j.Revision, j.CreatedAt, j.UpdatedAt)
	if err != nil {
		return err
	}
	j.ID, err = res.LastInsertId()
	return err
}

// Job returns a job by id (any owner); callers check ownership.
func (s *Store) Job(ctx context.Context, id int64) (*BackupJob, error) {
	return scanJob(s.DB.QueryRowContext(ctx, "SELECT "+jobCols+" FROM backup_jobs WHERE id = ?", id))
}

// JobFilter selects jobs of a user, optionally one device. Jobs are listed
// in id order; AfterID is the cursor (the last id of the previous page).
type JobFilter struct {
	UserID   int64
	DeviceID int64
	AfterID  int64
	Limit    int
}

// ListJobs returns jobs in ascending id order.
func (s *Store) ListJobs(ctx context.Context, f JobFilter) ([]BackupJob, error) {
	where := []string{"user_id = ?", "id > ?"}
	args := []any{f.UserID, f.AfterID}
	if f.DeviceID > 0 {
		where = append(where, "device_id = ?")
		args = append(args, f.DeviceID)
	}
	f.Limit = clampLimit(f.Limit)
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+jobCols+" FROM backup_jobs WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BackupJob{}
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

func clampLimit(n int) int {
	if n <= 0 || n > wire.ListMaxLimit {
		return wire.ListDefaultLimit
	}
	return n
}

// UpdateJob writes every writable field of j when the stored revision is
// still ifRevision, then increments the revision (j.Revision and
// j.UpdatedAt are set). A changed revision returns ErrRevisionConflict, a
// missing job ErrNotFound.
func (s *Store) UpdateJob(ctx context.Context, j *BackupJob, ifRevision int64) error {
	spec, sched, mods, err := jobJSON(j)
	if err != nil {
		return err
	}
	ts := now()
	res, err := s.DB.ExecContext(ctx, `UPDATE backup_jobs SET repo_id = ?, name = ?, mode = ?, spec = ?, schedule = ?, retention_policy_id = ?,
		priority = ?, enabled = ?, profile_sid = ?, required_modules = ?, state = ?, revision = revision + 1, updated_at = ?
		WHERE id = ? AND revision = ?`, nullID(j.RepoID), j.Name, j.Mode, spec, sched, j.RetentionPolicyID, j.Priority, j.Enabled,
		j.ProfileSID, mods, j.State, ts, j.ID, ifRevision)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := s.Job(ctx, j.ID); err != nil {
			return err
		}
		return ErrRevisionConflict
	}
	j.Revision, j.UpdatedAt = ifRevision+1, ts
	return nil
}

// DeleteJob removes a job with its runs and issues.
func (s *Store) DeleteJob(ctx context.Context, id int64) error {
	res, err := s.DB.ExecContext(ctx, `DELETE FROM backup_jobs WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ApplyJobModules moves the device's jobs between active and
// suspended_by_mode from the device's enabled modules (core is always
// enabled): an active job needing a module that is not enabled is
// suspended, a suspended_by_mode job whose modules are all enabled again is
// resumed. Jobs are never deleted (api-v1 §6). Each change bumps the
// revision.
func (s *Store) ApplyJobModules(ctx context.Context, deviceID int64, enabled map[string]bool) (suspended, resumed []int64, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT id, required_modules, state FROM backup_jobs WHERE device_id = ? AND state IN (?, ?)`,
			deviceID, wire.JobStateActive, wire.JobStateSuspendedByMode)
		if err != nil {
			return err
		}
		type change struct {
			id    int64
			state string
		}
		var changes []change
		for rows.Next() {
			var id int64
			var mods, state string
			if err := rows.Scan(&id, &mods, &state); err != nil {
				rows.Close()
				return err
			}
			var need []string
			if err := json.Unmarshal([]byte(jsonOr(mods, "[]")), &need); err != nil {
				rows.Close()
				return fmt.Errorf("backup job %d: required_modules: %w", id, err)
			}
			ok := true
			for _, m := range need {
				if m != wire.ModuleCore && !enabled[m] {
					ok = false
				}
			}
			switch {
			case !ok && state == wire.JobStateActive:
				changes = append(changes, change{id, wire.JobStateSuspendedByMode})
				suspended = append(suspended, id)
			case ok && state == wire.JobStateSuspendedByMode:
				changes = append(changes, change{id, wire.JobStateActive})
				resumed = append(resumed, id)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		ts := now()
		for _, c := range changes {
			if _, err := tx.ExecContext(ctx, `UPDATE backup_jobs SET state = ?, revision = revision + 1, updated_at = ? WHERE id = ?`, c.state, ts, c.id); err != nil {
				return err
			}
		}
		return nil
	})
	return suspended, resumed, err
}

// OwnedRef reports whether a row of table (repositories or
// retention_policies) with id belongs to userID.
func (s *Store) OwnedRef(ctx context.Context, table string, id, userID int64) (bool, error) {
	switch table {
	case "repositories", "retention_policies":
	default:
		return false, fmt.Errorf("owned ref: unsupported table %q", table)
	}
	var owner int64
	err := s.DB.QueryRowContext(ctx, "SELECT user_id FROM "+table+" WHERE id = ?", id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil && owner == userID, err
}

// --- runs ---

// JobRun is one row of job_runs. Progress holds the last heartbeat.
type JobRun struct {
	ID            int64
	JobID         int64
	UserID        int64
	DeviceID      int64
	ClientRunUUID string
	Trigger       string
	SnapshotID    *int64
	StartedAt     int64
	UpdatedAt     int64
	FinishedAt    *int64
	Skipped       int64
	Progress      wire.RunProgress
}

// runSummary is the JSON kept in job_runs.summary.
type runSummary struct {
	Phase      string `json:"phase"`
	QueuePacks int64  `json:"queue_packs"`
	ETAMS      int64  `json:"eta_ms"`
}

const runCols = "id, job_id, user_id, device_id, client_run_uuid, source, snapshot_id, status, started_at, updated_at, finished_at, files_total, files_done, bytes_total, bytes_done, bytes_new, bytes_uploaded, warnings, errors, skipped, summary"

func scanRun(sc interface{ Scan(...any) error }) (*JobRun, error) {
	var r JobRun
	var dev, snap, fin sql.NullInt64
	var sum string
	p := &r.Progress
	err := sc.Scan(&r.ID, &r.JobID, &r.UserID, &dev, &r.ClientRunUUID, &r.Trigger, &snap, &p.Status, &r.StartedAt, &r.UpdatedAt, &fin,
		&p.FilesTotal, &p.FilesDone, &p.BytesTotal, &p.BytesDone, &p.BytesNew, &p.BytesUploaded, &p.Warnings, &p.Errors, &r.Skipped, &sum)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.DeviceID = dev.Int64
	r.SnapshotID, r.FinishedAt = nullInt(snap), nullInt(fin)
	var rs runSummary
	if err := json.Unmarshal([]byte(jsonOr(sum, "{}")), &rs); err == nil {
		p.Phase, p.QueuePacks, p.ETAMS = rs.Phase, rs.QueuePacks, rs.ETAMS
	}
	return &r, nil
}

// RunFinished reports whether status is terminal.
func RunFinished(status string) bool {
	switch status {
	case wire.RunStatusCompleted, wire.RunStatusPartial, wire.RunStatusFailed, wire.RunStatusCancelled:
		return true
	}
	return false
}

// StartRun opens a queued run for a job, idempotent by (job, client run
// uuid): a replay returns the existing run with created false. A new run
// with trigger retry_skipped takes over the retryable skipped issues it
// retries (see linkRetryIssues).
func (s *Store) StartRun(ctx context.Context, r *JobRun) (created bool, err error) {
	if r.JobID <= 0 || r.UserID <= 0 || r.ClientRunUUID == "" {
		return false, errors.New("run job, user and client_run_uuid required")
	}
	if old, err := s.RunByUUID(ctx, r.JobID, r.ClientRunUUID); err == nil {
		*r = *old
		return false, nil
	} else if !errors.Is(err, ErrNotFound) {
		return false, err
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		ts := now()
		res, err := tx.ExecContext(ctx, `INSERT INTO job_runs(job_id, user_id, device_id, client_run_uuid, source, status, started_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?)`, r.JobID, r.UserID, nullID(r.DeviceID), r.ClientRunUUID, r.Trigger, wire.RunStatusQueued, ts, ts)
		if err != nil {
			return err
		}
		if r.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		r.StartedAt, r.UpdatedAt, r.Progress = ts, ts, wire.RunProgress{Status: wire.RunStatusQueued}
		if r.Trigger == wire.RunTriggerRetrySkipped {
			return linkRetryIssues(ctx, tx, r)
		}
		return nil
	})
	if isUniqueErr(err) {
		old, err := s.RunByUUID(ctx, r.JobID, r.ClientRunUUID)
		if err != nil {
			return false, err
		}
		*r = *old
		return false, nil
	}
	return err == nil, err
}

// RunByUUID returns the run of a job opened with a client run uuid.
func (s *Store) RunByUUID(ctx context.Context, jobID int64, uuid string) (*JobRun, error) {
	return scanRun(s.DB.QueryRowContext(ctx, "SELECT "+runCols+" FROM job_runs WHERE job_id = ? AND client_run_uuid = ?", jobID, uuid))
}

// retryEligible is the SQL condition for issues a Retry Skipped picks up:
// open (not skipped, resolved or already taken by a retry), retryable and
// of severity skipped.
const retryEligible = "severity = 'skipped' AND retryable = 1 AND skip = 0 AND resolved_at IS NULL AND retry_run_id IS NULL"

// linkRetryIssues marks the issues a retry_skipped run retries. The source
// is the newest retry_skipped device task of the device (claimed or done)
// whose run belongs to the same job; its issue_ids, when given, limit the
// set. Without such a task every eligible issue of the job's earlier runs
// is taken.
func linkRetryIssues(ctx context.Context, tx *sql.Tx, r *JobRun) error {
	var ids []int64
	runID := int64(0)
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM device_tasks WHERE device_id = ? AND kind = ? AND status IN ('claimed','done') ORDER BY id DESC LIMIT 20`,
		r.DeviceID, wire.TaskKindRetrySkipped)
	if err != nil {
		return err
	}
	var payloads []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			rows.Close()
			return err
		}
		payloads = append(payloads, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, raw := range payloads {
		var p wire.TaskRetrySkippedPayload
		if json.Unmarshal([]byte(raw), &p) != nil {
			continue
		}
		var job int64
		if err := tx.QueryRowContext(ctx, `SELECT job_id FROM job_runs WHERE id = ?`, p.RunID).Scan(&job); err == nil && job == r.JobID {
			runID, ids = p.RunID, p.IssueIDs
			break
		}
	}
	q := `UPDATE run_issues SET retry_run_id = ? WHERE ` + retryEligible + ` AND run_id IN (SELECT id FROM job_runs WHERE job_id = ? AND id <> ?)`
	args := []any{r.ID, r.JobID, r.ID}
	if runID > 0 {
		q += " AND run_id = ?"
		args = append(args, runID)
		if len(ids) > 0 {
			q += " AND id IN (" + placeholders(len(ids)) + ")"
			for _, id := range ids {
				args = append(args, id)
			}
		}
	}
	_, err = tx.ExecContext(ctx, q, args...)
	return err
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

// Run returns a run by id (any owner); callers check ownership.
func (s *Store) Run(ctx context.Context, id int64) (*JobRun, error) {
	return scanRun(s.DB.QueryRowContext(ctx, "SELECT "+runCols+" FROM job_runs WHERE id = ?", id))
}

// UpdateRun stores a heartbeat. A terminal status finishes the run: it
// sets finished_at and the skipped count, and settles the issues the run
// retried (resolved unless reported again by this run; back to open when
// the run failed or was cancelled). A finished run returns ErrRunFinished.
func (s *Store) UpdateRun(ctx context.Context, id int64, p wire.RunProgress) (*JobRun, error) {
	sum, err := json.Marshal(runSummary{Phase: p.Phase, QueuePacks: p.QueuePacks, ETAMS: p.ETAMS})
	if err != nil {
		return nil, err
	}
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		ts := now()
		var fin any
		if RunFinished(p.Status) {
			fin = ts
		}
		res, err := tx.ExecContext(ctx, `UPDATE job_runs SET status = ?, updated_at = ?, finished_at = ?, files_total = ?, files_done = ?, bytes_total = ?,
			bytes_done = ?, bytes_new = ?, bytes_uploaded = ?, warnings = ?, errors = ?, summary = ?
			WHERE id = ? AND finished_at IS NULL`, p.Status, ts, fin, p.FilesTotal, p.FilesDone, p.BytesTotal, p.BytesDone, p.BytesNew,
			p.BytesUploaded, p.Warnings, p.Errors, string(sum), id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			var x int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM job_runs WHERE id = ?`, id).Scan(&x); errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			} else if err != nil {
				return err
			}
			return ErrRunFinished
		}
		if fin == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `UPDATE job_runs SET skipped = (SELECT COUNT(*) FROM run_issues WHERE run_id = ? AND severity = 'skipped') WHERE id = ?`, id, id); err != nil {
			return err
		}
		if p.Status == wire.RunStatusCompleted || p.Status == wire.RunStatusPartial {
			_, err = tx.ExecContext(ctx, `UPDATE run_issues SET resolved_at = ? WHERE retry_run_id = ? AND resolved_at IS NULL AND NOT EXISTS
				(SELECT 1 FROM run_issues n WHERE n.run_id = ? AND n.path = run_issues.path AND n.class = run_issues.class)`, ts, id, id)
		} else {
			_, err = tx.ExecContext(ctx, `UPDATE run_issues SET retry_run_id = NULL WHERE retry_run_id = ? AND resolved_at IS NULL`, id)
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.Run(ctx, id)
}

// SetRunSnapshot links the snapshot a run committed (snapshot commit,
// api-v1 §5.2).
func (s *Store) SetRunSnapshot(ctx context.Context, runID, snapshotID int64) error {
	res, err := s.DB.ExecContext(ctx, `UPDATE job_runs SET snapshot_id = ?, updated_at = ? WHERE id = ?`, snapshotID, now(), runID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// RunFilter selects runs of a user, newest first. BeforeID is the cursor.
type RunFilter struct {
	UserID   int64
	JobID    int64
	DeviceID int64
	Status   string
	BeforeID int64
	Limit    int
}

// ListRuns returns runs newest first.
func (s *Store) ListRuns(ctx context.Context, f RunFilter) ([]JobRun, error) {
	where := []string{"user_id = ?"}
	args := []any{f.UserID}
	for _, c := range []struct {
		col string
		v   int64
	}{{"job_id", f.JobID}, {"device_id", f.DeviceID}, {"-id", f.BeforeID}} {
		if c.v <= 0 {
			continue
		}
		if c.col == "-id" {
			where = append(where, "id < ?")
		} else {
			where = append(where, c.col+" = ?")
		}
		args = append(args, c.v)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	f.Limit = clampLimit(f.Limit)
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+runCols+" FROM job_runs WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []JobRun{}
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// --- issues ---

// issueMeta is the JSON kept in run_issues.message.
type issueMeta struct {
	Detail    string `json:"detail"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	Count     int64  `json:"count"`
}

const issueCols = "id, run_id, path, severity, class, message, retryable, skip, resolved_at, retry_run_id, created_at"

func scanIssue(sc interface{ Scan(...any) error }) (*wire.Issue, error) {
	var i wire.Issue
	var msg string
	var skip int
	var resolved, retry sql.NullInt64
	var created int64
	err := sc.Scan(&i.ID, &i.RunID, &i.Path, &i.Severity, &i.Class, &msg, &i.Retryable, &skip, &resolved, &retry, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	var m issueMeta
	if json.Unmarshal([]byte(msg), &m) != nil {
		m = issueMeta{Detail: msg}
	}
	i.Detail, i.FirstSeen, i.LastSeen, i.Count = m.Detail, m.FirstSeen, m.LastSeen, max(m.Count, 1)
	if i.FirstSeen == 0 {
		i.FirstSeen = created
	}
	if i.LastSeen == 0 {
		i.LastSeen = i.FirstSeen
	}
	i.RetryRunID = nullInt(retry)
	i.Resolution = issueResolution(skip, resolved.Valid, retry.Valid)
	return &i, nil
}

func issueResolution(skip int, resolved, retried bool) string {
	switch {
	case resolved:
		return wire.IssueResolutionResolved
	case skip == issueSkipOne:
		return wire.IssueResolutionSkipped
	case skip == issueSkipAll:
		return wire.IssueResolutionSkippedAll
	case retried:
		return wire.IssueResolutionRetried
	}
	return wire.IssueResolutionOpen
}

var severityRank = map[string]int{wire.IssueSeverityWarning: 0, wire.IssueSeveritySkipped: 1, wire.IssueSeverityError: 2}

// ReportIssues stores a device's issue batch for a run, deduplicated by
// (run, path, class): a repeat adds its count, widens first/last seen,
// keeps the most severe severity and takes the latest detail and
// retryable flag. The caller validates the entries.
func (s *Store) ReportIssues(ctx context.Context, runID int64, issues []wire.IssueReport) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		ts := now()
		for _, in := range issues {
			cnt := max(in.Count, 1)
			first, last := in.FirstSeen, in.LastSeen
			if first <= 0 {
				first = ts
			}
			if last < first {
				last = first
			}
			var id int64
			var msg, sev string
			err := tx.QueryRowContext(ctx, `SELECT id, message, severity FROM run_issues WHERE run_id = ? AND path = ? AND class = ? ORDER BY id LIMIT 1`,
				runID, in.Path, in.Class).Scan(&id, &msg, &sev)
			if errors.Is(err, sql.ErrNoRows) {
				b, err := json.Marshal(issueMeta{Detail: in.Detail, FirstSeen: first, LastSeen: last, Count: cnt})
				if err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO run_issues(run_id, path, severity, class, message, retryable, created_at) VALUES(?,?,?,?,?,?,?)`,
					runID, in.Path, in.Severity, in.Class, string(b), in.Retryable, ts); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			var m issueMeta
			if json.Unmarshal([]byte(msg), &m) != nil {
				m = issueMeta{Detail: msg, Count: 1}
			}
			m.Count = max(m.Count, 1) + cnt
			if m.FirstSeen == 0 || first < m.FirstSeen {
				m.FirstSeen = first
			}
			m.LastSeen = max(m.LastSeen, last)
			if in.Detail != "" {
				m.Detail = in.Detail
			}
			if severityRank[in.Severity] > severityRank[sev] {
				sev = in.Severity
			}
			b, err := json.Marshal(m)
			if err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, `UPDATE run_issues SET message = ?, severity = ?, retryable = ? WHERE id = ?`, string(b), sev, in.Retryable, id); err != nil {
				return err
			}
		}
		return nil
	})
}

// IssueFilter selects issues of one run in id order. AfterID is the cursor.
type IssueFilter struct {
	RunID      int64
	Class      string
	Resolution string
	AfterID    int64
	Limit      int
}

var resolutionWhere = map[string]string{
	wire.IssueResolutionOpen:       "resolved_at IS NULL AND skip = 0 AND retry_run_id IS NULL",
	wire.IssueResolutionSkipped:    "resolved_at IS NULL AND skip = 1",
	wire.IssueResolutionSkippedAll: "resolved_at IS NULL AND skip = 2",
	wire.IssueResolutionRetried:    "resolved_at IS NULL AND skip = 0 AND retry_run_id IS NOT NULL",
	wire.IssueResolutionResolved:   "resolved_at IS NOT NULL",
}

// ValidIssueResolution reports whether r is a wire issue resolution.
func ValidIssueResolution(r string) bool { _, ok := resolutionWhere[r]; return ok }

// ListIssues returns a run's issues in id order.
func (s *Store) ListIssues(ctx context.Context, f IssueFilter) ([]wire.Issue, error) {
	where := []string{"run_id = ?", "id > ?"}
	args := []any{f.RunID, f.AfterID}
	if f.Class != "" {
		where = append(where, "class = ?")
		args = append(args, f.Class)
	}
	if f.Resolution != "" {
		w, ok := resolutionWhere[f.Resolution]
		if !ok {
			return nil, fmt.Errorf("unknown issue resolution %q", f.Resolution)
		}
		where = append(where, w)
	}
	f.Limit = clampLimit(f.Limit)
	args = append(args, f.Limit)
	rows, err := s.DB.QueryContext(ctx, "SELECT "+issueCols+" FROM run_issues WHERE "+strings.Join(where, " AND ")+" ORDER BY id LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []wire.Issue{}
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// SkipIssues marks issues of a run as skipped: explicit ids (resolution
// skipped; an id not in the run, or already resolved, fails with
// not_found / issue_resolved) or every open issue of a class (resolution
// skipped_all; ok counts the issues changed).
func (s *Store) SkipIssues(ctx context.Context, runID int64, ids []int64, class string) (wire.BulkResult, error) {
	out := wire.BulkResult{Failed: []wire.BulkFailed{}}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		if class != "" {
			res, err := tx.ExecContext(ctx, `UPDATE run_issues SET skip = ? WHERE run_id = ? AND class = ? AND skip = 0 AND resolved_at IS NULL`, issueSkipAll, runID, class)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			out.OKCount = int(n)
			return nil
		}
		seen := map[int64]bool{}
		for _, id := range ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			var resolved sql.NullInt64
			err := tx.QueryRowContext(ctx, `SELECT resolved_at FROM run_issues WHERE id = ? AND run_id = ?`, id, runID).Scan(&resolved)
			sid := fmt.Sprint(id)
			switch {
			case errors.Is(err, sql.ErrNoRows):
				out.Failed = append(out.Failed, wire.BulkFailed{ID: sid, Code: "not_found", Message: "Issue not found in this run"})
				continue
			case err != nil:
				return err
			case resolved.Valid:
				out.Failed = append(out.Failed, wire.BulkFailed{ID: sid, Code: "issue_resolved", Message: "Issue is already resolved"})
				continue
			}
			if _, err := tx.ExecContext(ctx, `UPDATE run_issues SET skip = ? WHERE id = ?`, issueSkipOne, id); err != nil {
				return err
			}
			out.OKCount++
		}
		return nil
	})
	return out, err
}

// RetryableIssueIDs returns the ids of the run's issues a Retry Skipped
// would pick up (open, retryable, severity skipped), at most limit.
func (s *Store) RetryableIssueIDs(ctx context.Context, runID int64, limit int) ([]int64, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT id FROM run_issues WHERE run_id = ? AND `+retryEligible+` ORDER BY id LIMIT ?`, runID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ResolveIssues marks issues of a run resolved (for example after a manual
// fix); it returns the number changed.
func (s *Store) ResolveIssues(ctx context.Context, runID int64, ids []int64) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	ids = slices.Compact(slices.Sorted(slices.Values(ids)))
	args := []any{now(), runID}
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := s.DB.ExecContext(ctx, `UPDATE run_issues SET resolved_at = ? WHERE run_id = ? AND resolved_at IS NULL AND id IN (`+placeholders(len(ids))+`)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
