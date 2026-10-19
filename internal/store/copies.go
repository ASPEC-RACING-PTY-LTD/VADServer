package store

// File copies ("copy rules"): node locations, rules, the per-blob copy state,
// primary integrity checks and the job queue nodes pull from.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// --- node locations -----------------------------------------------------------

type NodeLocation struct {
	ID        int64  `json:"id"`
	NodeID    int64  `json:"node_id"`
	NodeName  string `json:"node_name"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	Status    string `json:"status"` // preparing, ok, error
	Error     string `json:"error"`
	DiskTotal int64  `json:"disk_total"`
	DiskFree  int64  `json:"disk_free"`
	CreatedAt int64  `json:"created_at"`
	UpdatedAt int64  `json:"updated_at"`
}

const nodeLocCols = `l.id, l.node_id, COALESCE(n.name, ''), l.name, l.path, l.status, l.error, l.disk_total, l.disk_free, l.created_at, l.updated_at`

func scanNodeLoc(row interface{ Scan(...any) error }) (*NodeLocation, error) {
	var l NodeLocation
	err := row.Scan(&l.ID, &l.NodeID, &l.NodeName, &l.Name, &l.Path, &l.Status, &l.Error, &l.DiskTotal, &l.DiskFree, &l.CreatedAt, &l.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

func (s *Store) CreateNodeLocation(ctx context.Context, nodeID int64, name, path string) (*NodeLocation, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO node_locations(node_id, name, path, created_at, updated_at) VALUES(?,?,?,?,?)", nodeID, name, path, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.NodeLocation(ctx, id)
}

func (s *Store) NodeLocation(ctx context.Context, id int64) (*NodeLocation, error) {
	return scanNodeLoc(s.DB.QueryRowContext(ctx, "SELECT "+nodeLocCols+" FROM node_locations l LEFT JOIN storage_nodes n ON n.id = l.node_id WHERE l.id = ?", id))
}

func (s *Store) NodeLocations(ctx context.Context) ([]NodeLocation, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeLocCols+" FROM node_locations l LEFT JOIN storage_nodes n ON n.id = l.node_id ORDER BY l.name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NodeLocation{}
	for rows.Next() {
		l, err := scanNodeLoc(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
}

func (s *Store) SetNodeLocationStatus(ctx context.Context, id int64, status, msg string, total, free int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE node_locations SET status = ?, error = ?, disk_total = CASE WHEN ? > 0 THEN ? ELSE disk_total END,
		disk_free = CASE WHEN ? > 0 THEN ? ELSE disk_free END, updated_at = ? WHERE id = ?`, status, msg, total, total, total, free, now(), id)
	return err
}

func (s *Store) DeleteNodeLocation(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM node_locations WHERE id = ?", id)
	return err
}

// --- copy rules ---------------------------------------------------------------

type CopyRule struct {
	ID               int64  `json:"id"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	UserID           *int64 `json:"user_id"`
	SourceLocationID *int64 `json:"source_location_id"`
	Target           string `json:"target"`
	PathPattern      string `json:"path_pattern"`
	Extensions       string `json:"extensions"`
	MinSize          int64  `json:"min_size"`
	MaxSize          int64  `json:"max_size"`
	DeleteDelayHours int64  `json:"delete_delay_hours"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

const ruleCols = `id, name, enabled, user_id, source_location_id, target, path_pattern, extensions, min_size, max_size, delete_delay_hours, created_at, updated_at`

func scanRule(row interface{ Scan(...any) error }) (*CopyRule, error) {
	var r CopyRule
	var u, src sql.NullInt64
	err := row.Scan(&r.ID, &r.Name, &r.Enabled, &u, &src, &r.Target, &r.PathPattern, &r.Extensions, &r.MinSize, &r.MaxSize, &r.DeleteDelayHours, &r.CreatedAt, &r.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	r.UserID, r.SourceLocationID = nullInt(u), nullInt(src)
	return &r, err
}

func (s *Store) CopyRules(ctx context.Context) ([]CopyRule, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+ruleCols+" FROM copy_rules ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CopyRule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (s *Store) CopyRule(ctx context.Context, id int64) (*CopyRule, error) {
	return scanRule(s.DB.QueryRowContext(ctx, "SELECT "+ruleCols+" FROM copy_rules WHERE id = ?", id))
}

// SaveCopyRule inserts (ID 0) or updates a rule.
func (s *Store) SaveCopyRule(ctx context.Context, r *CopyRule) error {
	t := now()
	if r.ID == 0 {
		res, err := s.DB.ExecContext(ctx, `INSERT INTO copy_rules(name, enabled, user_id, source_location_id, target, path_pattern, extensions, min_size, max_size, delete_delay_hours, created_at, updated_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, r.Name, r.Enabled, r.UserID, r.SourceLocationID, r.Target, r.PathPattern, r.Extensions, r.MinSize, r.MaxSize, r.DeleteDelayHours, t, t)
		if err != nil {
			return err
		}
		r.ID, _ = res.LastInsertId()
		r.CreatedAt, r.UpdatedAt = t, t
		return nil
	}
	_, err := s.DB.ExecContext(ctx, `UPDATE copy_rules SET name = ?, enabled = ?, user_id = ?, source_location_id = ?, target = ?, path_pattern = ?, extensions = ?,
		min_size = ?, max_size = ?, delete_delay_hours = ?, updated_at = ? WHERE id = ?`,
		r.Name, r.Enabled, r.UserID, r.SourceLocationID, r.Target, r.PathPattern, r.Extensions, r.MinSize, r.MaxSize, r.DeleteDelayHours, t, r.ID)
	r.UpdatedAt = t
	return err
}

func (s *Store) DeleteCopyRule(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM copy_rules WHERE id = ?", id)
	return err
}

// --- stored files for planning -------------------------------------------------

// StoredFile is a live file (not trashed) with its blob, for rule matching.
type StoredFile struct {
	UserID     int64
	SHA256     string
	Size       int64
	LocationID int64
	Path       string
}

// StoredFiles returns every file whose blob is stored, with its full path.
// Trashed files are included: their content is still stored.
func (s *Store) StoredFiles(ctx context.Context, fn func(StoredFile) error) error {
	rows, err := s.DB.QueryContext(ctx, `
WITH RECURSIVE tree(id, path) AS (
	SELECT id, '' FROM nodes WHERE parent_id IS NULL
	UNION ALL
	SELECT n.id, tree.path || '/' || n.name FROM nodes n JOIN tree ON n.parent_id = tree.id WHERE n.is_dir = 1
)
SELECT n.user_id, n.sha256, b.size, b.location_id, COALESCE(tree.path, '') || '/' || n.name
FROM nodes n
JOIN blobs b ON b.user_id = n.user_id AND b.sha256 = n.sha256
LEFT JOIN tree ON tree.id = COALESCE(n.parent_id, n.original_parent_id)
WHERE n.is_dir = 0 AND n.sha256 IS NOT NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var f StoredFile
		if err := rows.Scan(&f.UserID, &f.SHA256, &f.Size, &f.LocationID, &f.Path); err != nil {
			return err
		}
		if err := fn(f); err != nil {
			return err
		}
	}
	return rows.Err()
}

// --- blob copies --------------------------------------------------------------

type BlobCopy struct {
	UserID           int64  `json:"user_id"`
	Username         string `json:"username"`
	SHA256           string `json:"sha256"`
	Target           string `json:"target"`
	State            string `json:"state"` // pending, ok, failed, deleting
	Size             int64  `json:"size"`
	StoredSHA256     string `json:"-"`
	LastPath         string `json:"last_path"`
	DeleteDelayHours int64  `json:"delete_delay_hours"`
	Attempts         int64  `json:"attempts"`
	NextAt           int64  `json:"next_at"`
	DeleteAt         *int64 `json:"delete_at"`
	VerifiedAt       *int64 `json:"verified_at"`
	LastError        string `json:"last_error"`
	CreatedAt        int64  `json:"created_at"`
	UpdatedAt        int64  `json:"updated_at"`
}

const copyCols = `c.user_id, COALESCE(u.username, ''), c.sha256, c.target, c.state, c.size, c.stored_sha256, c.last_path, c.delete_delay_hours,
	c.attempts, c.next_at, c.delete_at, c.verified_at, c.last_error, c.created_at, c.updated_at`

func scanCopy(row interface{ Scan(...any) error }) (*BlobCopy, error) {
	var c BlobCopy
	var del, ver sql.NullInt64
	err := row.Scan(&c.UserID, &c.Username, &c.SHA256, &c.Target, &c.State, &c.Size, &c.StoredSHA256, &c.LastPath, &c.DeleteDelayHours,
		&c.Attempts, &c.NextAt, &del, &ver, &c.LastError, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	c.DeleteAt, c.VerifiedAt = nullInt(del), nullInt(ver)
	return &c, err
}

func (s *Store) queryCopies(ctx context.Context, where string, args ...any) ([]BlobCopy, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+copyCols+" FROM blob_copies c LEFT JOIN users u ON u.id = c.user_id WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []BlobCopy{}
	for rows.Next() {
		c, err := scanCopy(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *c)
	}
	return out, rows.Err()
}

func (s *Store) BlobCopy(ctx context.Context, userID int64, sha, target string) (*BlobCopy, error) {
	return scanCopy(s.DB.QueryRowContext(ctx, "SELECT "+copyCols+" FROM blob_copies c LEFT JOIN users u ON u.id = c.user_id WHERE c.user_id = ? AND c.sha256 = ? AND c.target = ?", userID, sha, target))
}

// CopiesOf lists the copies of one blob.
func (s *Store) CopiesOf(ctx context.Context, userID int64, sha string) ([]BlobCopy, error) {
	return s.queryCopies(ctx, "c.user_id = ? AND c.sha256 = ? ORDER BY c.target", userID, sha)
}

// ListCopies lists copies in a state (all when empty), newest change first.
func (s *Store) ListCopies(ctx context.Context, state string, limit int) ([]BlobCopy, error) {
	if state == "" {
		return s.queryCopies(ctx, "1 = 1 ORDER BY c.updated_at DESC LIMIT ?", limit)
	}
	return s.queryCopies(ctx, "c.state = ? ORDER BY c.updated_at DESC LIMIT ?", state, limit)
}

// DueCopies returns copies to work on: pending and failed ones due now
// (failed ones after their backoff), ok ones whose
// check is older than verifyBefore, and deletions that are due.
func (s *Store) DueCopies(ctx context.Context, verifyBefore int64, limit int) ([]BlobCopy, error) {
	t := now()
	return s.queryCopies(ctx, `(c.state IN ('pending','failed') AND c.next_at <= ?)
		OR (c.state = 'ok' AND COALESCE(c.verified_at, 0) < ? AND c.next_at <= ?)
		OR (c.state = 'deleting' AND COALESCE(c.delete_at, 0) <= ? AND c.next_at <= ?)
		ORDER BY c.state = 'deleting' DESC, c.state = 'pending' DESC, c.next_at LIMIT ?`, t, verifyBefore, t, t, t, limit)
}

// WantCopy records that a blob should have a copy on target. A copy marked
// for deletion becomes wanted again (pending, re-checked).
func (s *Store) WantCopy(ctx context.Context, f StoredFile, target string, delayHours int64) error {
	t := now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO blob_copies(user_id, sha256, target, state, size, last_path, delete_delay_hours, created_at, updated_at)
		VALUES(?,?,?,'pending',?,?,?,?,?)
		ON CONFLICT(user_id, sha256, target) DO UPDATE SET
			last_path = excluded.last_path,
			delete_delay_hours = MAX(blob_copies.delete_delay_hours, excluded.delete_delay_hours),
			state = CASE WHEN blob_copies.state = 'deleting' THEN 'ok' ELSE blob_copies.state END,
			verified_at = CASE WHEN blob_copies.state = 'deleting' THEN NULL ELSE blob_copies.verified_at END,
			delete_at = NULL,
			updated_at = CASE WHEN blob_copies.state = 'deleting' THEN excluded.updated_at ELSE blob_copies.updated_at END`,
		f.UserID, f.SHA256, target, f.Size, f.Path, delayHours, t, t)
	return err
}

// SetCopy updates a copy's state after work on it.
func (s *Store) SetCopy(ctx context.Context, c *BlobCopy) error {
	c.UpdatedAt = now()
	_, err := s.DB.ExecContext(ctx, `UPDATE blob_copies SET state = ?, stored_sha256 = ?, attempts = ?, next_at = ?, delete_at = ?, verified_at = ?, last_error = ?, updated_at = ?
		WHERE user_id = ? AND sha256 = ? AND target = ?`,
		c.State, c.StoredSHA256, c.Attempts, c.NextAt, c.DeleteAt, c.VerifiedAt, c.LastError, c.UpdatedAt, c.UserID, c.SHA256, c.Target)
	return err
}

func (s *Store) DeleteCopyRow(ctx context.Context, userID int64, sha, target string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM blob_copies WHERE user_id = ? AND sha256 = ? AND target = ?", userID, sha, target)
	return err
}

// AllCopyKeys calls fn for every copy that is not being deleted.
func (s *Store) AllCopyKeys(ctx context.Context, fn func(userID int64, sha, target, state string, delayHours int64)) error {
	rows, err := s.DB.QueryContext(ctx, "SELECT user_id, sha256, target, state, delete_delay_hours FROM blob_copies WHERE state != 'deleting'")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var u, d int64
		var sha, tgt, st string
		if err := rows.Scan(&u, &sha, &tgt, &st, &d); err != nil {
			return err
		}
		fn(u, sha, tgt, st, d)
	}
	return rows.Err()
}

// RetryFailedCopies makes failed copies pending again.
func (s *Store) RetryFailedCopies(ctx context.Context) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE blob_copies SET state = 'pending', attempts = 0, next_at = 0, updated_at = ? WHERE state = 'failed'", now())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// VerifyAllCopiesNow makes every ok copy due for a check.
func (s *Store) VerifyAllCopiesNow(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE blob_copies SET verified_at = NULL, next_at = 0 WHERE state = 'ok'")
	if err == nil {
		_, err = s.DB.ExecContext(ctx, "DELETE FROM blob_checks")
	}
	return err
}

// CopyTargetStats counts copies per target and state.
type CopyTargetStats struct {
	Target   string `json:"target"`
	Pending  int64  `json:"pending"`
	OK       int64  `json:"ok"`
	Failed   int64  `json:"failed"`
	Deleting int64  `json:"deleting"`
	Bytes    int64  `json:"bytes"` // of ok copies
}

func (s *Store) CopyStats(ctx context.Context) ([]CopyTargetStats, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT target,
		SUM(state = 'pending'), SUM(state = 'ok'), SUM(state = 'failed'), SUM(state = 'deleting'), SUM(CASE WHEN state = 'ok' THEN size ELSE 0 END)
		FROM blob_copies GROUP BY target ORDER BY target`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []CopyTargetStats{}
	for rows.Next() {
		var c CopyTargetStats
		if err := rows.Scan(&c.Target, &c.Pending, &c.OK, &c.Failed, &c.Deleting, &c.Bytes); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CountCopiesOnTarget counts copies (any state) on target.
func (s *Store) CountCopiesOnTarget(ctx context.Context, target string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM blob_copies WHERE target = ?", target).Scan(&n)
	return n, err
}

// --- primary checks -------------------------------------------------------------

// DuePrimaryChecks returns blobs that have an ok copy and whose primary was
// not checked since before (oldest first).
func (s *Store) DuePrimaryChecks(ctx context.Context, before int64, limit int) ([]BlobRef, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT DISTINCT b.user_id, b.sha256, b.location_id FROM blob_copies c
		JOIN blobs b ON b.user_id = c.user_id AND b.sha256 = c.sha256
		LEFT JOIN blob_checks k ON k.user_id = b.user_id AND k.sha256 = b.sha256
		WHERE c.state = 'ok' AND COALESCE(k.verified_at, 0) < ?
		ORDER BY COALESCE(k.verified_at, 0) LIMIT ?`, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []BlobRef
	for rows.Next() {
		var r BlobRef
		if err := rows.Scan(&r.UserID, &r.SHA256, &r.LocationID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) RecordPrimaryCheck(ctx context.Context, userID int64, sha string, ok bool, msg string) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO blob_checks(user_id, sha256, verified_at, ok, error) VALUES(?,?,?,?,?)
		ON CONFLICT(user_id, sha256) DO UPDATE SET verified_at = excluded.verified_at, ok = excluded.ok, error = excluded.error`, userID, sha, now(), ok, msg)
	return err
}

// PrimaryCheckStats counts checked primaries and failures.
func (s *Store) PrimaryCheckStats(ctx context.Context) (checked, bad int64, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(ok = 0), 0) FROM blob_checks").Scan(&checked, &bad)
	return
}

// BlobRow returns a stored blob's size and location.
func (s *Store) BlobRow(ctx context.Context, userID int64, sha string) (size, locID int64, err error) {
	err = s.DB.QueryRowContext(ctx, "SELECT size, location_id FROM blobs WHERE user_id = ? AND sha256 = ?", userID, sha).Scan(&size, &locID)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return
}

// --- node jobs ----------------------------------------------------------------

type NodeJob struct {
	ID         int64  `json:"id"`
	NodeID     int64  `json:"node_id"`
	Kind       string `json:"kind"`
	Payload    string `json:"payload"`
	Status     string `json:"status"`
	Result     string `json:"result"`
	Error      string `json:"error"`
	CreatedAt  int64  `json:"created_at"`
	ClaimedAt  *int64 `json:"claimed_at"`
	FinishedAt *int64 `json:"finished_at"`
}

const nodeJobCols = `id, node_id, kind, payload, status, result, error, created_at, claimed_at, finished_at`

func scanNodeJob(row interface{ Scan(...any) error }) (*NodeJob, error) {
	var j NodeJob
	var c, f sql.NullInt64
	err := row.Scan(&j.ID, &j.NodeID, &j.Kind, &j.Payload, &j.Status, &j.Result, &j.Error, &j.CreatedAt, &c, &f)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	j.ClaimedAt, j.FinishedAt = nullInt(c), nullInt(f)
	return &j, err
}

func (s *Store) EnqueueNodeJob(ctx context.Context, nodeID int64, kind, payload string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "INSERT INTO node_jobs(node_id, kind, payload, created_at) VALUES(?,?,?,?)", nodeID, kind, payload, now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ClaimNodeJob hands the oldest queued job of a node to it.
func (s *Store) ClaimNodeJob(ctx context.Context, nodeID int64) (*NodeJob, error) {
	t := now()
	return scanNodeJob(s.DB.QueryRowContext(ctx, `UPDATE node_jobs SET status = 'claimed', claimed_at = ?
		WHERE id = (SELECT id FROM node_jobs WHERE node_id = ? AND status = 'queued' ORDER BY id LIMIT 1)
		RETURNING `+nodeJobCols, t, nodeID))
}

func (s *Store) NodeJob(ctx context.Context, id int64) (*NodeJob, error) {
	return scanNodeJob(s.DB.QueryRowContext(ctx, "SELECT "+nodeJobCols+" FROM node_jobs WHERE id = ?", id))
}

// FinishNodeJob records a claimed job's result once.
func (s *Store) FinishNodeJob(ctx context.Context, id int64, ok bool, result, msg string) (bool, error) {
	st := "done"
	if !ok {
		st = "failed"
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE node_jobs SET status = ?, result = ?, error = ?, finished_at = ? WHERE id = ? AND status IN ('queued','claimed')", st, result, msg, now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// CountOpenNodeJobs counts queued or claimed jobs of kinds on a node.
func (s *Store) CountOpenNodeJobs(ctx context.Context, nodeID int64, kinds ...string) (int64, error) {
	q := "SELECT COUNT(*) FROM node_jobs WHERE node_id = ? AND status IN ('queued','claimed')"
	args := []any{nodeID}
	if len(kinds) > 0 {
		q += " AND kind IN (?" + strings.Repeat(",?", len(kinds)-1) + ")"
		for _, k := range kinds {
			args = append(args, k)
		}
	}
	var n int64
	err := s.DB.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// StaleNodeJobs returns claimed jobs older than before and queued jobs
// older than queuedBefore, which are failed by the caller.
func (s *Store) StaleNodeJobs(ctx context.Context, claimedBefore, queuedBefore int64) ([]NodeJob, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeJobCols+" FROM node_jobs WHERE (status = 'claimed' AND claimed_at < ?) OR (status = 'queued' AND created_at < ?)", claimedBefore, queuedBefore)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeJob
	for rows.Next() {
		j, err := scanNodeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// ClaimedNodeJobsBefore returns a node's jobs claimed before t (lost when
// the node restarted after claiming them).
func (s *Store) ClaimedNodeJobsBefore(ctx context.Context, nodeID, t int64) ([]NodeJob, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeJobCols+" FROM node_jobs WHERE node_id = ? AND status = 'claimed' AND claimed_at < ?", nodeID, t)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NodeJob
	for rows.Next() {
		j, err := scanNodeJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *j)
	}
	return out, rows.Err()
}

// PruneNodeJobs removes finished jobs older than before.
func (s *Store) PruneNodeJobs(ctx context.Context, before int64) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM node_jobs WHERE status IN ('done','failed') AND finished_at < ?", before)
}
