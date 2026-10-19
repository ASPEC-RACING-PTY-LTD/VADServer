package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type Upload struct {
	ID            string   `json:"id"`
	UserID        int64    `json:"user_id"`
	DeviceID      *int64   `json:"device_id"`
	TransferID    int64    `json:"transfer_id"`
	Path          string   `json:"path"`
	Size          int64    `json:"size"`
	SHA256        string   `json:"sha256,omitempty"`
	ChunkSize     int64    `json:"chunk_size"`
	ChunksTotal   int64    `json:"chunks_total"`
	Received      []byte   `json:"-"`
	BytesReceived int64    `json:"bytes_received"`
	LocationID    int64    `json:"location_id"`
	ModifiedAt    int64    `json:"modified_at"`
	Tags          []string `json:"tags"`
	OnConflict    string   `json:"on_conflict"`
	Status        string   `json:"status"`
	CreatedAt     int64    `json:"created_at"`
	UpdatedAt     int64    `json:"updated_at"`
	ExpiresAt     int64    `json:"expires_at"`
	Username      string   `json:"username,omitempty"`
	DeviceName    string   `json:"device_name,omitempty"`
}

// ReceivedChunks returns the indices of received chunks.
func (u *Upload) ReceivedChunks() []int64 {
	out := []int64{}
	for i := int64(0); i < u.ChunksTotal; i++ {
		if u.Received[i/8]&(1<<(i%8)) != 0 {
			out = append(out, i)
		}
	}
	return out
}

// Complete reports whether every chunk has arrived.
func (u *Upload) Complete() bool {
	for i := int64(0); i < u.ChunksTotal; i++ {
		if u.Received[i/8]&(1<<(i%8)) == 0 {
			return false
		}
	}
	return true
}

// ChunkLen returns the expected length of chunk i.
func (u *Upload) ChunkLen(i int64) int64 {
	if u.Size == 0 {
		return 0
	}
	start := i * u.ChunkSize
	return min(u.ChunkSize, u.Size-start)
}

const uploadCols = `u.id, u.user_id, u.device_id, COALESCE(u.transfer_id,0), u.path, u.size, u.sha256, u.chunk_size, u.chunks_total, u.received, u.bytes_received, u.location_id, u.modified_at, u.tags, u.on_conflict, u.status, u.created_at, u.updated_at, u.expires_at, COALESCE(us.username,''), COALESCE(d.name,'')`
const uploadFrom = ` FROM uploads u LEFT JOIN users us ON us.id = u.user_id LEFT JOIN devices d ON d.id = u.device_id`

func scanUpload(sc interface{ Scan(...any) error }) (*Upload, error) {
	var u Upload
	var dev sql.NullInt64
	var tags string
	err := sc.Scan(&u.ID, &u.UserID, &dev, &u.TransferID, &u.Path, &u.Size, &u.SHA256, &u.ChunkSize, &u.ChunksTotal, &u.Received, &u.BytesReceived, &u.LocationID, &u.ModifiedAt, &tags, &u.OnConflict, &u.Status, &u.CreatedAt, &u.UpdatedAt, &u.ExpiresAt, &u.Username, &u.DeviceName)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	u.DeviceID = nullInt(dev)
	_ = json.Unmarshal([]byte(tags), &u.Tags)
	if u.Tags == nil {
		u.Tags = []string{}
	}
	return &u, nil
}

func (s *Store) CreateUpload(ctx context.Context, u *Upload) error {
	tags, _ := json.Marshal(u.Tags)
	u.Received = make([]byte, (u.ChunksTotal+7)/8)
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `INSERT INTO transfers(user_id, device_id, upload_id, path, size, status, source, started_at) VALUES(?,?,?,?,?,'active',?,?)`,
			u.UserID, u.DeviceID, u.ID, u.Path, u.Size, sourceOf(u.DeviceID), u.CreatedAt)
		if err != nil {
			return err
		}
		u.TransferID, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, `INSERT INTO uploads(id, user_id, device_id, transfer_id, path, size, sha256, chunk_size, chunks_total, received, location_id, modified_at, tags, on_conflict, status, created_at, updated_at, expires_at)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,'open',?,?,?)`,
			u.ID, u.UserID, u.DeviceID, u.TransferID, u.Path, u.Size, u.SHA256, u.ChunkSize, u.ChunksTotal, u.Received, u.LocationID, u.ModifiedAt, string(tags), u.OnConflict, u.CreatedAt, u.UpdatedAt, u.ExpiresAt)
		return err
	})
}

func sourceOf(dev *int64) string {
	if dev != nil {
		return "device"
	}
	return "web"
}

// Upload returns an upload owned by the user.
func (s *Store) Upload(ctx context.Context, userID int64, id string) (*Upload, error) {
	return scanUpload(s.DB.QueryRowContext(ctx, "SELECT "+uploadCols+uploadFrom+" WHERE u.id = ? AND u.user_id = ?", id, userID))
}

// UploadAny returns an upload regardless of owner (admin/janitor use).
func (s *Store) UploadAny(ctx context.Context, id string) (*Upload, error) {
	return scanUpload(s.DB.QueryRowContext(ctx, "SELECT "+uploadCols+uploadFrom+" WHERE u.id = ?", id))
}

// FindResumableUpload finds an open upload for the same target and content.
func (s *Store) FindResumableUpload(ctx context.Context, userID int64, deviceID *int64, path string, size int64, sha string) (*Upload, error) {
	if sha == "" {
		return nil, ErrNotFound
	}
	return scanUpload(s.DB.QueryRowContext(ctx, "SELECT "+uploadCols+uploadFrom+` WHERE u.user_id = ? AND u.path = ? AND u.size = ? AND u.sha256 = ? AND u.status = 'open' AND u.expires_at > ? AND (u.device_id IS ? ) ORDER BY u.updated_at DESC LIMIT 1`,
		userID, path, size, sha, now(), deviceID))
}

// MarkChunk records a received chunk. It returns false if it was already
// recorded (a retried chunk).
func (s *Store) MarkChunk(ctx context.Context, userID int64, id string, idx, length, expires int64) (bool, error) {
	added := false
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var bm []byte
		var status string
		if err := tx.QueryRowContext(ctx, "SELECT received, status FROM uploads WHERE id = ? AND user_id = ?", id, userID).Scan(&bm, &status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if status != "open" {
			return fmt.Errorf("%w: upload is %s", ErrConflict, status)
		}
		if idx < 0 || idx/8 >= int64(len(bm)) {
			return fmt.Errorf("%w: chunk index %d out of range", ErrConflict, idx)
		}
		if bm[idx/8]&(1<<(idx%8)) != 0 {
			_, err := tx.ExecContext(ctx, "UPDATE uploads SET updated_at = ?, expires_at = ? WHERE id = ?", now(), expires, id)
			return err
		}
		bm[idx/8] |= 1 << (idx % 8)
		added = true
		t := now()
		if _, err := tx.ExecContext(ctx, "UPDATE uploads SET received = ?, bytes_received = bytes_received + ?, updated_at = ?, expires_at = ? WHERE id = ?", bm, length, t, expires, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE transfers SET bytes_done = (SELECT bytes_received FROM uploads WHERE id = ?) WHERE upload_id = ?", id, id)
		return err
	})
	return added, err
}

// SetUploadStatus changes an upload's status atomically from one state.
func (s *Store) SetUploadStatus(ctx context.Context, id, from, to string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE uploads SET status = ?, updated_at = ? WHERE id = ? AND status = ?", to, now(), id, from)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func (s *Store) DeleteUpload(ctx context.Context, id string) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM uploads WHERE id = ?", id)
	return err
}

// DeleteUploadUnlessFinalizing deletes an upload unless it is being
// finalised. It reports whether a row was deleted; false with a nil error
// means the upload is gone or currently finalising.
func (s *Store) DeleteUploadUnlessFinalizing(ctx context.Context, id string) (bool, error) {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM uploads WHERE id = ? AND status != 'finalizing'", id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// ListUploads lists open uploads, for one user (userID > 0) or all.
func (s *Store) ListUploads(ctx context.Context, userID int64) ([]Upload, error) {
	q := "SELECT " + uploadCols + uploadFrom + " WHERE u.status IN ('open','finalizing')"
	var args []any
	if userID > 0 {
		q += " AND u.user_id = ?"
		args = append(args, userID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY u.updated_at DESC LIMIT 500", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Upload{}
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// ExpiredUploads returns uploads past their expiry and finished uploads.
// Uploads being finalised are never returned: deleting their temp file
// mid-finalise would fail the commit. RecoverAfterRestart reopens any
// finalisation interrupted by a crash, after which normal expiry applies.
func (s *Store) ExpiredUploads(ctx context.Context) ([]Upload, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+uploadCols+uploadFrom+" WHERE (u.expires_at < ? AND u.status != 'finalizing') OR u.status IN ('done','failed')", now())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Upload
	for rows.Next() {
		u, err := scanUpload(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// PendingBytes returns bytes still expected for open uploads on a location.
func (s *Store) PendingBytes(ctx context.Context, locationID int64) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(size - bytes_received),0) FROM uploads WHERE location_id = ? AND status IN ('open','finalizing') AND expires_at > ?", locationID, now()).Scan(&n)
	return n, err
}

// PendingUserBytes returns bytes still expected for the user's open uploads.
func (s *Store) PendingUserBytes(ctx context.Context, userID int64, exceptUpload string) (int64, error) {
	var n int64
	err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0) FROM uploads WHERE user_id = ? AND id != ? AND status IN ('open','finalizing') AND expires_at > ?", userID, exceptUpload, now()).Scan(&n)
	return n, err
}

// --- transfers ---

type Transfer struct {
	ID         int64  `json:"id"`
	UserID     int64  `json:"user_id"`
	Username   string `json:"username,omitempty"`
	DeviceID   *int64 `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"`
	UploadID   string `json:"upload_id,omitempty"`
	Path       string `json:"path"`
	Size       int64  `json:"size"`
	BytesDone  int64  `json:"bytes_done"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
	Source     string `json:"source"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt *int64 `json:"finished_at"`
}

func (s *Store) FinishTransfer(ctx context.Context, q Querier, id int64, status, errMsg string, bytes int64) error {
	if q == nil {
		q = s.DB
	}
	_, err := q.ExecContext(ctx, "UPDATE transfers SET status = ?, error = ?, bytes_done = CASE WHEN ? >= 0 THEN ? ELSE bytes_done END, finished_at = ? WHERE id = ?", status, truncate(errMsg, 500), bytes, bytes, now(), id)
	return err
}

// DeleteTransfer removes a transfer record (an upload session that was
// discarded and restarted, so it never shows as a failure).
func (s *Store) DeleteTransfer(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM transfers WHERE id = ?", id)
	return err
}

// FailuresDismissedID returns the newest failed transfer id the user has
// dismissed from the sync warnings.
func (s *Store) FailuresDismissedID(ctx context.Context, userID int64) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT failures_dismissed_id FROM users WHERE id = ?", userID).Scan(&id)
	return id, err
}

// DismissFailures hides the user's current failed transfers from the sync
// warnings; later failures show again.
func (s *Store) DismissFailures(ctx context.Context, userID int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE users SET failures_dismissed_id = (SELECT COALESCE(MAX(id),0) FROM transfers WHERE user_id = ? AND status = 'failed') WHERE id = ?", userID, userID)
	return err
}

// RecordTransfer inserts an already-finished transfer (dedup hits,
// unchanged files, device-side failures).
func (s *Store) RecordTransfer(ctx context.Context, t Transfer) error {
	if t.StartedAt == 0 {
		t.StartedAt = now()
	}
	fin := now()
	_, err := s.DB.ExecContext(ctx, `INSERT INTO transfers(user_id, device_id, path, size, bytes_done, status, error, source, started_at, finished_at) VALUES(?,?,?,?,?,?,?,?,?,?)`,
		t.UserID, t.DeviceID, truncate(t.Path, 4096), t.Size, t.BytesDone, t.Status, truncate(t.Error, 500), t.Source, t.StartedAt, fin)
	return err
}

type TransferFilter struct {
	UserID   int64
	DeviceID int64
	Status   string // "", active, completed, failed, ... ; "finished" = not active
	Limit    int
	Before   int64
	After    int64 // only ids above this
}

func (s *Store) ListTransfers(ctx context.Context, f TransferFilter) ([]Transfer, error) {
	where := []string{"1=1"}
	var args []any
	if f.UserID > 0 {
		where = append(where, "t.user_id = ?")
		args = append(args, f.UserID)
	}
	if f.DeviceID > 0 {
		where = append(where, "t.device_id = ?")
		args = append(args, f.DeviceID)
	}
	switch f.Status {
	case "":
	case "finished":
		where = append(where, "t.status != 'active'")
	case "succeeded":
		where = append(where, "t.status IN ('completed','deduplicated','unchanged')")
	default:
		where = append(where, "t.status = ?")
		args = append(args, f.Status)
	}
	if f.Before > 0 {
		where = append(where, "t.id < ?")
		args = append(args, f.Before)
	}
	if f.After > 0 {
		where = append(where, "t.id > ?")
		args = append(args, f.After)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT t.id, t.user_id, COALESCE(u.username,''), t.device_id, COALESCE(d.name,''), COALESCE(t.upload_id,''), t.path, t.size, t.bytes_done, t.status, t.error, t.source, t.started_at, t.finished_at
		FROM transfers t LEFT JOIN users u ON u.id = t.user_id LEFT JOIN devices d ON d.id = t.device_id WHERE `+strings.Join(where, " AND ")+" ORDER BY t.id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Transfer{}
	for rows.Next() {
		var t Transfer
		var dev, fin sql.NullInt64
		if err := rows.Scan(&t.ID, &t.UserID, &t.Username, &dev, &t.DeviceName, &t.UploadID, &t.Path, &t.Size, &t.BytesDone, &t.Status, &t.Error, &t.Source, &t.StartedAt, &fin); err != nil {
			return nil, err
		}
		t.DeviceID, t.FinishedAt = nullInt(dev), nullInt(fin)
		out = append(out, t)
	}
	return out, rows.Err()
}

// TransferStats summarises recent transfers.
type TransferStats struct {
	Active         int64 `json:"active"`
	Completed24h   int64 `json:"completed_24h"`
	Failed24h      int64 `json:"failed_24h"`
	Bytes24h       int64 `json:"bytes_24h"`
	CompletedTotal int64 `json:"completed_total"`
}

func (s *Store) TransferStats(ctx context.Context, userID int64) (TransferStats, error) {
	var st TransferStats
	since := now() - 24*3600*1000
	where := "1=1"
	args := []any{}
	if userID > 0 {
		where = "user_id = ?"
		args = append(args, userID)
	}
	err := s.DB.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(status = 'active'),0),
		COALESCE(SUM(status IN ('completed','deduplicated') AND finished_at >= ?),0),
		COALESCE(SUM(status = 'failed' AND finished_at >= ?),0),
		COALESCE(SUM(CASE WHEN status = 'completed' AND finished_at >= ? THEN bytes_done ELSE 0 END),0),
		COALESCE(SUM(status IN ('completed','deduplicated')),0)
		FROM transfers WHERE `+where, append([]any{since, since, since}, args...)...).Scan(&st.Active, &st.Completed24h, &st.Failed24h, &st.Bytes24h, &st.CompletedTotal)
	return st, err
}

// MarkStaleTransfersFailed fails active transfers whose upload no longer
// exists (after expiry or crash cleanup).
func (s *Store) MarkStaleTransfersFailed(ctx context.Context) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE transfers SET status = 'failed', error = 'upload session expired', finished_at = ? WHERE status = 'active' AND (upload_id IS NULL OR upload_id NOT IN (SELECT id FROM uploads WHERE status IN ('open','finalizing')))`, now())
	return err
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
