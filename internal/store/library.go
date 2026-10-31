package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// Owner is the user a node belongs to.
func (n *Node) Owner() int64 { return n.userID }

// NodeAnyUser returns a live file node of any user (the administrators'
// full library).
func (s *Store) NodeAnyUser(ctx context.Context, id int64) (*Node, error) {
	return scanNode(s.DB.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE id = ? AND deleted_at IS NULL AND is_dir = 0", id))
}

// LibraryItem is a file of the full library with its owner.
type LibraryItem struct {
	Node
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	// Clip: rendered by smart processing.
	Clip bool `json:"clip"`
}

// LibraryQuery filters the full library.
type LibraryQuery struct {
	UserID int64
	Kind   string // video (default), image, audio, document, archive, other, all
	Q      string // name contains
	Clips  string // "", clips (made by smart processing), originals (everything else)
	Sort   string // modified (default), name, size, created
	Desc   bool
	Limit  int
	Offset int
}

// LibraryOwner is a user with the number and size of their library files.
type LibraryOwner struct {
	UserID      int64  `json:"user_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Files       int64  `json:"files"`
	Bytes       int64  `json:"bytes"`
}

// Library lists live files of every user (or one), newest first by default.
func (s *Store) Library(ctx context.Context, q LibraryQuery) ([]LibraryItem, int64, int64, error) {
	where := "n.deleted_at IS NULL AND n.is_dir = 0"
	var args []any
	if q.UserID > 0 {
		where += " AND n.user_id = ?"
		args = append(args, q.UserID)
	}
	if q.Kind == "" {
		q.Kind = "video"
	}
	if q.Kind != "all" {
		k := kindWhere(q.Kind)
		if k == "" {
			return nil, 0, 0, errors.New("unknown file type")
		}
		where += strings.NewReplacer("is_dir", "n.is_dir", "mime", "n.mime").Replace(k)
	}
	if q.Q != "" {
		where += " AND n.name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+likeEscape(q.Q)+"%")
	}
	const clip = "EXISTS (SELECT 1 FROM smart_outputs o WHERE o.node_id = n.id AND o.state = 'done')"
	switch q.Clips {
	case "clips":
		where += " AND " + clip
	case "originals":
		where += " AND NOT " + clip
	}
	var total, bytes int64
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*), COALESCE(SUM(n.size), 0) FROM nodes n WHERE "+where, args...).Scan(&total, &bytes); err != nil {
		return nil, 0, 0, err
	}
	dir := "ASC"
	if q.Desc {
		dir = "DESC"
	}
	order := map[string]string{"name": "n.name COLLATE NOCASE", "size": "n.size", "created": "COALESCE(n.file_created_at, n.created_at)"}[q.Sort]
	if order == "" {
		order = "n.modified_at"
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	cols := "n." + strings.ReplaceAll(nodeCols, ", ", ", n.")
	cols = strings.ReplaceAll(cols, "n.COALESCE(sha256", "COALESCE(n.sha256")
	rows, err := s.DB.QueryContext(ctx, "SELECT "+cols+", u.username, u.display_name, "+clip+" FROM nodes n JOIN users u ON u.id = n.user_id WHERE "+where+
		" ORDER BY "+order+" "+dir+", n.id "+dir+" LIMIT ? OFFSET ?", append(args, q.Limit, q.Offset)...)
	if err != nil {
		return nil, 0, 0, err
	}
	defer rows.Close()
	out := []LibraryItem{}
	for rows.Next() {
		var it LibraryItem
		var user, display string
		var isClip bool
		n, err := scanNode(scanExtras{rows, []any{&user, &display, &isClip}})
		if err != nil {
			return nil, 0, 0, err
		}
		it.Node, it.UserID, it.Username, it.DisplayName, it.Clip = *n, n.userID, user, display, isClip
		out = append(out, it)
	}
	return out, total, bytes, rows.Err()
}

// scanExtras scans a node followed by further columns.
type scanExtras struct {
	rows  *sql.Rows
	extra []any
}

func (s scanExtras) Scan(dest ...any) error { return s.rows.Scan(append(dest, s.extra...)...) }

// LibraryOwners lists users that have files of the kind.
func (s *Store) LibraryOwners(ctx context.Context, kind string) ([]LibraryOwner, error) {
	where := "n.deleted_at IS NULL AND n.is_dir = 0"
	if kind == "" {
		kind = "video"
	}
	if kind != "all" {
		where += strings.NewReplacer("is_dir", "n.is_dir", "mime", "n.mime").Replace(kindWhere(kind))
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT u.id, u.username, u.display_name, COUNT(n.id), COALESCE(SUM(n.size), 0)
		FROM users u JOIN nodes n ON n.user_id = u.id AND `+where+` GROUP BY u.id ORDER BY u.username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []LibraryOwner{}
	for rows.Next() {
		var o LibraryOwner
		if err := rows.Scan(&o.UserID, &o.Username, &o.DisplayName, &o.Files, &o.Bytes); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
