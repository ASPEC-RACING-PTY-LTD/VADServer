package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// Snapshot catalog rows (api-v1 §5.2, §5.4; owner SRV-2, P1-15).
//
// Storage notes:
//   - A commit inserts the snapshots row, one snapshot_packs row per pack
//     (kind data) and links job_runs.snapshot_id, in one transaction. The
//     existence check of the root, index and pack objects runs inside the
//     same transaction, so a concurrent GC mark cannot slip in between.
//   - ingest_state starts at 'pending'; catalog ingestion (P1-16) picks up
//     pending rows.
//   - stats is stored as JSON; file_count, logical_bytes and new_bytes are
//     mirrored into columns for usage and sorting.
//   - Repository usage (snapshot count and logical bytes) is
//     RepositoryUsage in repos.go.

// ErrObjectsMissing is returned by CommitSnapshot when referenced objects
// are missing; Missing lists them.
type ErrObjectsMissing struct{ Missing []string }

func (e *ErrObjectsMissing) Error() string { return "snapshot references missing objects" }

// Snapshot is one row of snapshots.
type Snapshot struct {
	ID               int64
	RepoID           int64
	UserID           int64
	DeviceID         int64 // 0 when the device was deleted
	SnapshotOID      string
	JobID            int64 // 0 when none
	RunID            int64 // 0 when none
	ParentSnapshotID int64 // 0 when none
	Kind             string
	Mode             string
	Hostname         string
	OSUser           string
	ProfileSID       string
	ProfileUsername  string
	ProfilePath      string
	ProfileConsentID string
	Epoch            int64
	ClientTime       int64
	CommittedAt      int64
	Tags             []string
	Stats            wire.SnapshotStats
	IndexOIDs        []string
	Protected        bool
	ProtectedUntil   int64 // 0 when none
	Status           string
	IngestState      string
}

const snapshotCols = `id, repo_id, user_id, device_id, snapshot_oid, job_id, run_id, parent_snapshot_id, kind, mode,
	hostname, os_user, profile_sid, profile_username, profile_path, profile_consent_id, epoch, client_time,
	committed_at, tags, stats, index_oids, protected, protected_until, status, ingest_state`

func scanSnapshot(sc interface{ Scan(...any) error }) (*Snapshot, error) {
	var sn Snapshot
	var dev, job, run, parent, until sql.NullInt64
	var tags, stats, idx string
	err := sc.Scan(&sn.ID, &sn.RepoID, &sn.UserID, &dev, &sn.SnapshotOID, &job, &run, &parent, &sn.Kind, &sn.Mode,
		&sn.Hostname, &sn.OSUser, &sn.ProfileSID, &sn.ProfileUsername, &sn.ProfilePath, &sn.ProfileConsentID, &sn.Epoch, &sn.ClientTime,
		&sn.CommittedAt, &tags, &stats, &idx, &sn.Protected, &until, &sn.Status, &sn.IngestState)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	sn.DeviceID, sn.JobID, sn.RunID, sn.ParentSnapshotID, sn.ProtectedUntil = dev.Int64, job.Int64, run.Int64, parent.Int64, until.Int64
	sn.Tags, sn.IndexOIDs = []string{}, []string{}
	_ = json.Unmarshal([]byte(tags), &sn.Tags)
	_ = json.Unmarshal([]byte(stats), &sn.Stats)
	_ = json.Unmarshal([]byte(idx), &sn.IndexOIDs)
	return &sn, nil
}

// GetSnapshot returns a snapshot by id.
func (s *Store) GetSnapshot(ctx context.Context, id int64) (*Snapshot, error) {
	return scanSnapshot(s.DB.QueryRowContext(ctx, "SELECT "+snapshotCols+" FROM snapshots WHERE id = ?", id))
}

// SnapshotByOID returns the snapshot of repoID with the given root oid.
func (s *Store) SnapshotByOID(ctx context.Context, repoID int64, oid string) (*Snapshot, error) {
	return scanSnapshot(s.DB.QueryRowContext(ctx, "SELECT "+snapshotCols+" FROM snapshots WHERE repo_id = ? AND snapshot_oid = ?", repoID, oid))
}

// missingInTx returns the oids of kind with no live row in the repository.
func missingInTx(ctx context.Context, tx *sql.Tx, repoID int64, kind string, oids []string) ([]string, error) {
	have := make(map[string]bool, len(oids))
	for i := 0; i < len(oids); i += missingBatch {
		batch := oids[i:min(i+missingBatch, len(oids))]
		args := make([]any, 0, len(batch)+2)
		args = append(args, repoID, kind)
		for _, o := range batch {
			args = append(args, o)
		}
		q := "SELECT oid FROM repo_objects WHERE repo_id = ? AND kind = ? AND delete_pending_at IS NULL AND oid IN (?" + strings.Repeat(",?", len(batch)-1) + ")"
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var o string
			if err := rows.Scan(&o); err != nil {
				rows.Close()
				return nil, err
			}
			have[o] = true
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	var missing []string
	for _, o := range oids {
		if !have[o] {
			have[o] = true
			missing = append(missing, o)
		}
	}
	return missing, nil
}

// CommitSnapshot inserts a snapshot with its pack references and links the
// run. It is idempotent by (repo, snapshot_oid): a replay sets sn.ID to the
// existing row and returns created false without re-checking objects.
// Missing root (kind snapshots), index or pack (kind data) objects give
// *ErrObjectsMissing and nothing is written.
func (s *Store) CommitSnapshot(ctx context.Context, sn *Snapshot, packOIDs []string) (created bool, err error) {
	err = s.Tx(ctx, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx, "SELECT id FROM snapshots WHERE repo_id = ? AND snapshot_oid = ?", sn.RepoID, sn.SnapshotOID).Scan(&sn.ID)
		if err == nil {
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var missing []string
		for _, ref := range []struct {
			kind string
			oids []string
		}{{wire.ObjectKindSnapshots, []string{sn.SnapshotOID}}, {wire.ObjectKindIndex, sn.IndexOIDs}, {wire.ObjectKindData, packOIDs}} {
			m, err := missingInTx(ctx, tx, sn.RepoID, ref.kind, ref.oids)
			if err != nil {
				return err
			}
			missing = append(missing, m...)
		}
		if len(missing) > 0 {
			return &ErrObjectsMissing{Missing: missing}
		}
		tags, _ := json.Marshal(nonNil(sn.Tags))
		stats, _ := json.Marshal(sn.Stats)
		idx, _ := json.Marshal(nonNil(sn.IndexOIDs))
		sn.CommittedAt = now()
		if sn.Status == "" {
			sn.Status = wire.SnapshotStatusCommitted
		}
		sn.IngestState = wire.SnapshotIngestPending
		res, err := tx.ExecContext(ctx, `INSERT INTO snapshots(repo_id, user_id, device_id, snapshot_oid, job_id, run_id, parent_snapshot_id,
			kind, mode, hostname, os_user, profile_sid, profile_username, profile_path, profile_consent_id, epoch, client_time,
			committed_at, tags, stats, index_oids, file_count, logical_bytes, new_bytes, status, ingest_state)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			sn.RepoID, sn.UserID, nullID(sn.DeviceID), sn.SnapshotOID, nullID(sn.JobID), nullID(sn.RunID), nullID(sn.ParentSnapshotID),
			sn.Kind, sn.Mode, sn.Hostname, sn.OSUser, sn.ProfileSID, sn.ProfileUsername, sn.ProfilePath, sn.ProfileConsentID, sn.Epoch, sn.ClientTime,
			sn.CommittedAt, string(tags), string(stats), string(idx), sn.Stats.FileCount, sn.Stats.LogicalBytes, sn.Stats.NewBytes, sn.Status, sn.IngestState)
		if err != nil {
			return err
		}
		if sn.ID, err = res.LastInsertId(); err != nil {
			return err
		}
		for i := 0; i < len(packOIDs); i += missingBatch {
			batch := packOIDs[i:min(i+missingBatch, len(packOIDs))]
			args := make([]any, 0, len(batch)+2)
			args = append(args, sn.ID, sn.RepoID)
			for _, o := range batch {
				args = append(args, o)
			}
			if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO snapshot_packs(snapshot_id, object_id)
				SELECT ?, id FROM repo_objects WHERE repo_id = ? AND kind = 'data' AND oid IN (?`+strings.Repeat(",?", len(batch)-1)+")", args...); err != nil {
				return err
			}
		}
		if sn.RunID > 0 {
			if _, err := tx.ExecContext(ctx, "UPDATE job_runs SET snapshot_id = ?, updated_at = ? WHERE id = ?", sn.ID, now(), sn.RunID); err != nil {
				return err
			}
		}
		created = true
		return nil
	})
	return created, err
}

func nonNil(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

// SnapshotPackCount returns the number of packs referenced by a snapshot.
func (s *Store) SnapshotPackCount(ctx context.Context, snapshotID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM snapshot_packs WHERE snapshot_id = ?", snapshotID).Scan(&n)
	return n, err
}

// SnapshotFilter selects snapshots for ListSnapshots. Zero values do not
// filter. Results are newest first (descending id); BeforeID is the cursor.
type SnapshotFilter struct {
	UserID     int64
	RepoID     int64
	JobID      int64
	DeviceID   int64
	ProfileSID string
	Kind       string
	Tag        string
	From, To   int64 // committed_at bounds in Unix ms, inclusive
	Protected  *bool
	BeforeID   int64
	Limit      int
}

// ListSnapshots returns the snapshots matching f.
func (s *Store) ListSnapshots(ctx context.Context, f SnapshotFilter) ([]Snapshot, error) {
	where := []string{"user_id = ?"}
	args := []any{f.UserID}
	add := func(cond string, v any) {
		where = append(where, cond)
		args = append(args, v)
	}
	if f.RepoID > 0 {
		add("repo_id = ?", f.RepoID)
	}
	if f.JobID > 0 {
		add("job_id = ?", f.JobID)
	}
	if f.DeviceID > 0 {
		add("device_id = ?", f.DeviceID)
	}
	if f.ProfileSID != "" {
		add("profile_sid = ?", f.ProfileSID)
	}
	if f.Kind != "" {
		add("kind = ?", f.Kind)
	}
	if f.Tag != "" {
		add("EXISTS (SELECT 1 FROM json_each(snapshots.tags) WHERE json_each.value = ?)", f.Tag)
	}
	if f.From > 0 {
		add("committed_at >= ?", f.From)
	}
	if f.To > 0 {
		add("committed_at <= ?", f.To)
	}
	if f.Protected != nil {
		add("protected = ?", *f.Protected)
	}
	if f.BeforeID > 0 {
		add("id < ?", f.BeforeID)
	}
	args = append(args, clampLimit(f.Limit))
	rows, err := s.DB.QueryContext(ctx, "SELECT "+snapshotCols+" FROM snapshots WHERE "+strings.Join(where, " AND ")+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Snapshot{}
	for rows.Next() {
		sn, err := scanSnapshot(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *sn)
	}
	return out, rows.Err()
}
