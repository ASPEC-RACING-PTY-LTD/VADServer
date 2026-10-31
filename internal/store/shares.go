package store

import (
	"context"
	"database/sql"
	"errors"
)

// Share is a public link to one video. The token in the link is the only
// credential; the link plays the exact content that was shared (SHA256),
// so it stops working when the file is changed or removed.
type Share struct {
	ID            int64  `json:"id"`
	UserID        int64  `json:"user_id"`
	NodeID        int64  `json:"node_id"`
	SHA256        string `json:"-"`
	Token         string `json:"token"`
	Title         string `json:"title"`
	Description   string `json:"description"`
	AllowDownload bool   `json:"allow_download"`
	CreatedAt     int64  `json:"created_at"`
	ExpiresAt     *int64 `json:"expires_at,omitempty"`
	RevokedAt     *int64 `json:"revoked_at,omitempty"`
	Views         int64  `json:"views"`
	LastViewedAt  *int64 `json:"last_viewed_at,omitempty"`
}

const shareCols = "id, user_id, node_id, sha256, token, title, description, allow_download, created_at, expires_at, revoked_at, views, last_viewed_at"

func scanShare(sc interface{ Scan(...any) error }) (*Share, error) {
	var h Share
	err := sc.Scan(&h.ID, &h.UserID, &h.NodeID, &h.SHA256, &h.Token, &h.Title, &h.Description, &h.AllowDownload, &h.CreatedAt, &h.ExpiresAt, &h.RevokedAt, &h.Views, &h.LastViewedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &h, err
}

func (s *Store) CreateShare(ctx context.Context, h Share) (*Share, error) {
	h.CreatedAt = now()
	res, err := s.DB.ExecContext(ctx, "INSERT INTO shares(user_id, node_id, sha256, token, title, description, allow_download, created_at, expires_at) VALUES (?,?,?,?,?,?,?,?,?)",
		h.UserID, h.NodeID, h.SHA256, h.Token, h.Title, h.Description, h.AllowDownload, h.CreatedAt, h.ExpiresAt)
	if err != nil {
		return nil, err
	}
	h.ID, _ = res.LastInsertId()
	return &h, nil
}

// Shares lists a user's links (of one file when nodeID > 0), newest first.
func (s *Store) Shares(ctx context.Context, userID, nodeID int64) ([]Share, error) {
	q := "SELECT " + shareCols + " FROM shares WHERE user_id = ?"
	args := []any{userID}
	if nodeID > 0 {
		q += " AND node_id = ?"
		args = append(args, nodeID)
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY created_at DESC LIMIT 1000", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		h, err := scanShare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

func (s *Store) Share(ctx context.Context, userID, id int64) (*Share, error) {
	return scanShare(s.DB.QueryRowContext(ctx, "SELECT "+shareCols+" FROM shares WHERE id = ? AND user_id = ?", id, userID))
}

// ShareByToken finds a link by its token (revoked or not; the caller checks).
func (s *Store) ShareByToken(ctx context.Context, token string) (*Share, error) {
	return scanShare(s.DB.QueryRowContext(ctx, "SELECT "+shareCols+" FROM shares WHERE token = ?", token))
}

// UpdateShare saves title, description, expiry and download permission.
func (s *Store) UpdateShare(ctx context.Context, h Share) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE shares SET title = ?, description = ?, allow_download = ?, expires_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL",
		h.Title, h.Description, h.AllowDownload, h.ExpiresAt, h.ID, h.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) RevokeShare(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE shares SET revoked_at = ? WHERE id = ? AND user_id = ? AND revoked_at IS NULL", now(), id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// CountShareView records a view of a link.
func (s *Store) CountShareView(ctx context.Context, id int64) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE shares SET views = views + 1, last_viewed_at = ? WHERE id = ?", now(), id)
}
