package store

// Video editor records: drafts (editing instructions that point at a source
// file, never a copy of it), clip export jobs, the trash expiry of
// originals erased after an export, and the device re-uploads they block.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// VideoDraft is a saved editing session. Data is the editor's own JSON.
type VideoDraft struct {
	ID           int64  `json:"id"`
	SourceNodeID int64  `json:"source_node_id"`
	SourceSHA256 string `json:"source_sha256"`
	SourceName   string `json:"source_name"`
	Name         string `json:"name"`
	Data         string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	UpdatedAt    int64  `json:"updated_at"`
}

const draftCols = "id, source_node_id, source_sha256, source_name, name, data, created_at, updated_at"

func scanDraft(sc interface{ Scan(...any) error }) (*VideoDraft, error) {
	var d VideoDraft
	err := sc.Scan(&d.ID, &d.SourceNodeID, &d.SourceSHA256, &d.SourceName, &d.Name, &d.Data, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &d, err
}

// VideoDrafts lists a user's drafts, newest first; sourceID > 0 limits them
// to one source file.
func (s *Store) VideoDrafts(ctx context.Context, userID, sourceID int64) ([]VideoDraft, error) {
	q := "SELECT " + draftCols + " FROM video_drafts WHERE user_id = ?"
	args := []any{userID}
	if sourceID > 0 {
		q += " AND source_node_id = ?"
		args = append(args, sourceID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY updated_at DESC LIMIT 500", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VideoDraft{}
	for rows.Next() {
		d, err := scanDraft(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) VideoDraft(ctx context.Context, userID, id int64) (*VideoDraft, error) {
	return scanDraft(s.DB.QueryRowContext(ctx, "SELECT "+draftCols+" FROM video_drafts WHERE id = ? AND user_id = ?", id, userID))
}

func (s *Store) CreateVideoDraft(ctx context.Context, userID int64, d VideoDraft) (*VideoDraft, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO video_drafts(user_id, source_node_id, source_sha256, source_name, name, data, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, userID, d.SourceNodeID, d.SourceSHA256, d.SourceName, d.Name, d.Data, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.VideoDraft(ctx, userID, id)
}

// UpdateVideoDraft replaces a draft's name and data.
func (s *Store) UpdateVideoDraft(ctx context.Context, userID, id int64, name, data string) (*VideoDraft, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE video_drafts SET name = ?, data = ?, updated_at = ? WHERE id = ? AND user_id = ?", name, data, now(), id, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.VideoDraft(ctx, userID, id)
}

func (s *Store) DeleteVideoDraft(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM video_drafts WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// VideoExport is a clip export job. Request and Progress are JSON owned by
// the API layer.
type VideoExport struct {
	ID           int64  `json:"id"`
	UserID       int64  `json:"-"`
	SourceNodeID int64  `json:"source_node_id"`
	State        string `json:"state"` // queued, running, done, failed, cancelled, interrupted
	Request      string `json:"-"`
	Progress     string `json:"-"`
	CreatedAt    int64  `json:"created_at"`
	FinishedAt   *int64 `json:"finished_at,omitempty"`
}

const exportCols = "id, user_id, source_node_id, state, request, progress, created_at, finished_at"

func scanExport(sc interface{ Scan(...any) error }) (*VideoExport, error) {
	var e VideoExport
	var fin sql.NullInt64
	err := sc.Scan(&e.ID, &e.UserID, &e.SourceNodeID, &e.State, &e.Request, &e.Progress, &e.CreatedAt, &fin)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	e.FinishedAt = nullInt(fin)
	return &e, err
}

// VideoExportActive reports whether the job is still to finish.
func VideoExportActive(state string) bool { return state == "queued" || state == "running" }

func (s *Store) CreateVideoExport(ctx context.Context, userID, sourceID int64, request, progress string) (*VideoExport, error) {
	res, err := s.DB.ExecContext(ctx, "INSERT INTO video_exports(user_id, source_node_id, state, request, progress, created_at) VALUES (?, ?, 'queued', ?, ?, ?)",
		userID, sourceID, request, progress, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.VideoExport(ctx, userID, id)
}

// VideoExport loads a job; userID 0 loads any user's.
func (s *Store) VideoExport(ctx context.Context, userID, id int64) (*VideoExport, error) {
	if userID == 0 {
		return scanExport(s.DB.QueryRowContext(ctx, "SELECT "+exportCols+" FROM video_exports WHERE id = ?", id))
	}
	return scanExport(s.DB.QueryRowContext(ctx, "SELECT "+exportCols+" FROM video_exports WHERE id = ? AND user_id = ?", id, userID))
}

// VideoExports lists a user's recent jobs, newest first; active limits them
// to queued and running ones.
func (s *Store) VideoExports(ctx context.Context, userID int64, active bool) ([]VideoExport, error) {
	q := "SELECT " + exportCols + " FROM video_exports WHERE user_id = ?"
	if active {
		q += " AND state IN ('queued','running')"
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY id DESC LIMIT 50", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VideoExport{}
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

// UpdateVideoExport stores a job's state and progress; a finished state
// stamps finished_at.
func (s *Store) UpdateVideoExport(ctx context.Context, id int64, state, progress string) error {
	var fin any
	if !VideoExportActive(state) {
		fin = now()
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE video_exports SET state = ?, progress = ?, finished_at = COALESCE(?, finished_at) WHERE id = ?", state, progress, fin, id)
	return err
}

// InterruptVideoExports marks jobs a stopped server left unfinished and
// returns them (their partial output files are the caller's to remove).
func (s *Store) InterruptVideoExports(ctx context.Context) ([]VideoExport, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+exportCols+" FROM video_exports WHERE state IN ('queued','running')")
	if err != nil {
		return nil, err
	}
	var out []VideoExport
	for rows.Next() {
		e, err := scanExport(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *e)
	}
	rows.Close()
	if len(out) > 0 {
		_, err = s.DB.ExecContext(ctx, "UPDATE video_exports SET state = 'interrupted', finished_at = ? WHERE state IN ('queued','running')", now())
	}
	return out, err
}

// PruneVideoExports drops finished jobs older than before.
func (s *Store) PruneVideoExports(ctx context.Context, before int64) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM video_exports WHERE state NOT IN ('queued','running') AND created_at < ?", before)
}

// TrashedNode loads a node of the user that is in the trash.
func (s *Store) TrashedNode(ctx context.Context, userID, id int64) (*Node, error) {
	return scanNode(s.DB.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE id = ? AND user_id = ? AND deleted_at IS NOT NULL", id, userID))
}

// SetTrashExpiry makes a trashed item expire at the given time (the
// server-wide retention still applies if it comes first).
func (s *Store) SetTrashExpiry(ctx context.Context, userID, id, at int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE nodes SET trash_expires_at = ? WHERE id = ? AND user_id = ? AND trash_root = 1 AND deleted_at IS NOT NULL", at, id, userID)
	return err
}

// PurgeExpiredTrash permanently deletes trashed items whose own expiry has
// passed (see SetTrashExpiry).
func (s *Store) PurgeExpiredTrash(ctx context.Context) (int, []BlobRef, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id FROM nodes WHERE trash_root = 1 AND deleted_at IS NOT NULL AND trash_expires_at IS NOT NULL AND trash_expires_at < ?", now())
	if err != nil {
		return 0, nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if len(ids) == 0 {
		return 0, nil, nil
	}
	return s.PurgeTrash(ctx, 0, ids, 0)
}

// BlockReupload stops devices from uploading this exact content to this
// path again until expiresAt: the user erased it on purpose (an original
// replaced by exported clips) and a synced PC that still has it must not
// bring it back.
func (s *Store) BlockReupload(ctx context.Context, userID int64, path, sha string, expiresAt int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO reupload_blocks(user_id, path, sha256, expires_at) VALUES (?, ?, ?, ?)
		ON CONFLICT(user_id, path, sha256) DO UPDATE SET expires_at = excluded.expires_at`, userID, strings.TrimSpace(path), sha, expiresAt)
	return err
}

// ReuploadBlocked reports whether a device upload of sha to path is blocked.
func (s *Store) ReuploadBlocked(ctx context.Context, userID int64, path, sha string) bool {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM reupload_blocks WHERE user_id = ? AND path = ? AND sha256 = ? AND expires_at > ?", userID, path, sha, now()).Scan(&n)
	return err == nil && n > 0
}

// PruneReuploadBlocks drops expired blocks.
func (s *Store) PruneReuploadBlocks(ctx context.Context) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM reupload_blocks WHERE expires_at <= ?", now())
}
