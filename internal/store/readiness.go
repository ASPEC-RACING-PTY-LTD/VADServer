package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Readiness inputs (api-v1 §11; rev 1.0 §11). The store gathers the facts
// of each device; the api package turns them into readiness checks.

// PutBackupState stores the readiness inputs a device reported with PUT
// /device/backup-state, replacing the previous report.
func (s *Store) PutBackupState(ctx context.Context, deviceID int64, st wire.BackupState) error {
	if deviceID <= 0 {
		return errors.New("device id required")
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO device_backup_state(device_id, state, updated_at) VALUES(?,?,?)
		ON CONFLICT(device_id) DO UPDATE SET state = excluded.state, updated_at = excluded.updated_at`,
		deviceID, string(raw), now())
	return err
}

// BackupState returns a device's last backup-state report and its time, or
// ErrNotFound when the device never reported one.
func (s *Store) BackupState(ctx context.Context, deviceID int64) (*wire.BackupState, int64, error) {
	var raw string
	var at int64
	err := s.DB.QueryRowContext(ctx, `SELECT state, updated_at FROM device_backup_state WHERE device_id = ?`, deviceID).Scan(&raw, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, ErrNotFound
	}
	if err != nil {
		return nil, 0, err
	}
	var st wire.BackupState
	if err := json.Unmarshal([]byte(jsonOr(raw, "{}")), &st); err != nil {
		return nil, 0, err
	}
	return &st, at, nil
}

// ReadinessJob holds the readiness facts of one backup job. Timestamps are
// Unix milliseconds, 0 when the event never happened.
type ReadinessJob struct {
	Job BackupJob
	// LastSuccessAt is the newest committed snapshot of the job, or the
	// newest completed/partial run, whichever is later.
	LastSuccessAt int64
	// LastRunStatus and LastRunAt describe the newest finished run.
	LastRunStatus string
	LastRunAt     int64
	// OpenIssues counts unresolved issues (resolution open) of the job's runs.
	OpenIssues int64
	// VerifiedAt is the newest verification of the job's repository: a done
	// verify task of the device or a server-side object verification.
	VerifiedAt int64
	// TestRestoredAt is the newest done test_restore task of the device for a
	// snapshot of this job.
	TestRestoredAt int64
}

// ReadinessProfile is the sid and state of one Windows profile of a device.
type ReadinessProfile struct {
	SID   string
	State string
}

// ReadinessFacts are the readiness inputs of one device.
type ReadinessFacts struct {
	Device *Device
	// State is the device's backup-state report (nil: never reported).
	State   *wire.BackupState
	StateAt int64
	// Modules is the device's module report (nil: never reported).
	Modules  *DeviceModules
	Profiles []ReadinessProfile
	Jobs     []ReadinessJob
}

// ReadinessDevices returns the approved devices readiness covers: those of
// userID, or of every user when userID is 0. Devices are in id order.
func (s *Store) ReadinessDevices(ctx context.Context, userID int64) ([]Device, error) {
	q := "SELECT " + deviceCols + deviceFrom + " WHERE d.status = 'approved'"
	var args []any
	if userID > 0 {
		q += " AND d.user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY d.id", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

// ReadinessFacts gathers the readiness inputs of device d.
func (s *Store) ReadinessFacts(ctx context.Context, d *Device) (*ReadinessFacts, error) {
	f := &ReadinessFacts{Device: d, Profiles: []ReadinessProfile{}, Jobs: []ReadinessJob{}}
	st, at, err := s.BackupState(ctx, d.ID)
	switch {
	case err == nil:
		f.State, f.StateAt = st, at
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	m, err := s.DeviceModules(ctx, d.ID)
	switch {
	case err == nil:
		f.Modules = m
	case !errors.Is(err, ErrNotFound):
		return nil, err
	}
	if f.Profiles, err = s.readinessProfiles(ctx, d.ID); err != nil {
		return nil, err
	}
	jobs, err := s.deviceJobs(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	for _, j := range jobs {
		rj := ReadinessJob{Job: j}
		if err := s.jobReadiness(ctx, &rj); err != nil {
			return nil, err
		}
		f.Jobs = append(f.Jobs, rj)
	}
	return f, nil
}

func (s *Store) readinessProfiles(ctx context.Context, deviceID int64) ([]ReadinessProfile, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT sid, state FROM device_profiles WHERE device_id = ? ORDER BY id`, deviceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ReadinessProfile{}
	for rows.Next() {
		var p ReadinessProfile
		if err := rows.Scan(&p.SID, &p.State); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) deviceJobs(ctx context.Context, deviceID int64) ([]BackupJob, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+jobCols+" FROM backup_jobs WHERE device_id = ? ORDER BY id", deviceID)
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

func (s *Store) jobReadiness(ctx context.Context, rj *ReadinessJob) error {
	j := &rj.Job
	var snapAt, runAt, verifiedTask, verifiedObj, restored sql.NullInt64
	if err := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT MAX(committed_at) FROM snapshots WHERE job_id = ? AND status = 'committed'),
		(SELECT MAX(COALESCE(finished_at, updated_at)) FROM job_runs WHERE job_id = ? AND status IN (?,?)),
		(SELECT MAX(finished_at) FROM device_tasks WHERE device_id = ? AND kind = ? AND status = 'done'
			AND ? > 0 AND json_extract(payload, '$.repo_id') = ?),
		(SELECT MAX(last_verified_at) FROM repo_objects WHERE repo_id = ? AND verify_status = 'ok'),
		(SELECT MAX(t.finished_at) FROM device_tasks t JOIN snapshots sn ON sn.id = json_extract(t.payload, '$.snapshot_id')
			WHERE t.device_id = ? AND t.kind = ? AND t.status = 'done' AND sn.job_id = ?)`,
		j.ID, j.ID, wire.RunStatusCompleted, wire.RunStatusPartial,
		j.DeviceID, wire.TaskKindVerify, j.RepoID, j.RepoID,
		j.RepoID,
		j.DeviceID, wire.TaskKindTestRestore, j.ID,
	).Scan(&snapAt, &runAt, &verifiedTask, &verifiedObj, &restored); err != nil {
		return err
	}
	rj.LastSuccessAt = max(snapAt.Int64, runAt.Int64)
	rj.VerifiedAt = max(verifiedTask.Int64, verifiedObj.Int64)
	rj.TestRestoredAt = restored.Int64

	var status sql.NullString
	var at sql.NullInt64
	err := s.DB.QueryRowContext(ctx, `SELECT status, COALESCE(finished_at, updated_at) FROM job_runs
		WHERE job_id = ? AND finished_at IS NOT NULL ORDER BY finished_at DESC, id DESC LIMIT 1`, j.ID).Scan(&status, &at)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rj.LastRunStatus, rj.LastRunAt = status.String, at.Int64

	return s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM run_issues i JOIN job_runs r ON r.id = i.run_id
		WHERE r.job_id = ? AND i.resolved_at IS NULL AND i.skip = ? AND i.retry_run_id IS NULL`, j.ID, issueSkipNone).Scan(&rj.OpenIssues)
}
