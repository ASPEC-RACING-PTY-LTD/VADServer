package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path"
	"strings"
)

var (
	ErrNotDir     = errors.New("not a folder")
	ErrIsDir      = errors.New("a folder with that name already exists")
	ErrExists     = errors.New("an item with that name already exists")
	ErrCycle      = errors.New("cannot move a folder into itself")
	ErrRootNode   = errors.New("the root folder cannot be modified")
	ErrQuota      = errors.New("storage quota exceeded")
	ErrParentGone = errors.New("destination folder does not exist")
)

type Node struct {
	ID         int64    `json:"id"`
	ParentID   *int64   `json:"parent_id"`
	Name       string   `json:"name"`
	IsDir      bool     `json:"is_dir"`
	Size       int64    `json:"size"`
	SHA256     string   `json:"sha256,omitempty"`
	Mime       string   `json:"mime"`
	CreatedAt  int64    `json:"created_at"`
	ModifiedAt int64    `json:"modified_at"`
	UpdatedAt  int64    `json:"updated_at"`
	DeletedAt  *int64   `json:"deleted_at,omitempty"`
	OriginDev  *int64   `json:"origin_device_id,omitempty"`
	Tags       []string `json:"tags"`
	Path       string   `json:"path,omitempty"`
	ChildCount *int64   `json:"child_count,omitempty"`
	userID     int64
	origParent *int64
}

const nodeCols = `id, user_id, parent_id, name, is_dir, size, COALESCE(sha256,''), mime, created_at, modified_at, updated_at, deleted_at, origin_device_id, original_parent_id`

func scanNode(sc interface{ Scan(...any) error }) (*Node, error) {
	var n Node
	var parent, del, dev, orig sql.NullInt64
	err := sc.Scan(&n.ID, &n.userID, &parent, &n.Name, &n.IsDir, &n.Size, &n.SHA256, &n.Mime, &n.CreatedAt, &n.ModifiedAt, &n.UpdatedAt, &del, &dev, &orig)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	n.ParentID, n.DeletedAt, n.OriginDev, n.origParent = nullInt(parent), nullInt(del), nullInt(dev), nullInt(orig)
	n.Tags = []string{}
	return &n, nil
}

// Node returns a live (not trashed) node owned by the user.
func (s *Store) Node(ctx context.Context, userID, id int64) (*Node, error) {
	return nodeQ(ctx, s.DB, userID, id)
}

func nodeQ(ctx context.Context, q Querier, userID, id int64) (*Node, error) {
	return scanNode(q.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE id = ? AND user_id = ? AND deleted_at IS NULL", id, userID))
}

func (s *Store) childByName(ctx context.Context, q Querier, userID, parentID int64, name string) (*Node, error) {
	return scanNode(q.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE user_id = ? AND parent_id = ? AND name = ? COLLATE NOCASE AND deleted_at IS NULL", userID, parentID, name))
}

// ListOptions controls folder listings.
type ListOptions struct {
	Sort   string // name, size, modified, type
	Desc   bool
	Limit  int
	Offset int
	Filter string // substring filter on name
	Kind   string // "", "folder", "image", "video", "audio", "document", "archive", "other"
}

var mimeKinds = map[string]string{
	"image":    "mime LIKE 'image/%'",
	"video":    "mime LIKE 'video/%'",
	"audio":    "mime LIKE 'audio/%'",
	"document": "(mime LIKE 'text/%' OR mime IN ('application/pdf','application/msword','application/vnd.openxmlformats-officedocument.wordprocessingml.document','application/vnd.openxmlformats-officedocument.spreadsheetml.sheet','application/vnd.openxmlformats-officedocument.presentationml.presentation','application/json','application/rtf'))",
	"archive":  "mime IN ('application/zip','application/x-7z-compressed','application/x-rar-compressed','application/gzip','application/x-tar','application/vnd.rar','application/x-zip-compressed')",
}

func orderClause(o ListOptions) string {
	dir := "ASC"
	if o.Desc {
		dir = "DESC"
	}
	switch o.Sort {
	case "size":
		return fmt.Sprintf("is_dir DESC, size %s, name COLLATE NOCASE ASC", dir)
	case "modified":
		return fmt.Sprintf("is_dir DESC, modified_at %s, name COLLATE NOCASE ASC", dir)
	case "type":
		return fmt.Sprintf("is_dir DESC, mime %s, name COLLATE NOCASE ASC", dir)
	default:
		return fmt.Sprintf("is_dir DESC, name COLLATE NOCASE %s", dir)
	}
}

// ListChildren lists a folder's live children.
func (s *Store) ListChildren(ctx context.Context, userID, parentID int64, o ListOptions) ([]Node, int, error) {
	where := "user_id = ? AND parent_id = ? AND deleted_at IS NULL"
	args := []any{userID, parentID}
	if o.Filter != "" {
		where += " AND name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+likeEscape(o.Filter)+"%")
	}
	where += kindWhere(o.Kind)
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM nodes WHERE "+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	limit := o.Limit
	if limit <= 0 || limit > 5000 {
		limit = 5000
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeCols+", (SELECT COUNT(*) FROM nodes c WHERE c.parent_id = nodes.id AND c.deleted_at IS NULL) FROM nodes WHERE "+where+" ORDER BY "+orderClause(o)+" LIMIT ? OFFSET ?", append(args, limit, o.Offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var cc int64
		n, err := scanNode(scanExtra{rows, &cc})
		if err != nil {
			return nil, 0, err
		}
		if n.IsDir {
			n.ChildCount = &cc
		}
		out = append(out, *n)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	if err := s.attachTags(ctx, out); err != nil {
		return nil, 0, err
	}
	if out == nil {
		out = []Node{}
	}
	return out, total, nil
}

type scanExtra struct {
	rows  *sql.Rows
	extra *int64
}

func (s scanExtra) Scan(dest ...any) error { return s.rows.Scan(append(dest, s.extra)...) }

func kindWhere(kind string) string {
	switch kind {
	case "":
		return ""
	case "folder":
		return " AND is_dir = 1"
	case "other":
		var parts []string
		for _, w := range mimeKinds {
			parts = append(parts, "NOT "+w)
		}
		return " AND is_dir = 0 AND " + strings.Join(parts, " AND ")
	}
	if w, ok := mimeKinds[kind]; ok {
		return " AND is_dir = 0 AND " + w
	}
	return ""
}

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

func (s *Store) attachTags(ctx context.Context, nodes []Node) error {
	if len(nodes) == 0 {
		return nil
	}
	idx := map[int64]int{}
	ph := make([]string, 0, len(nodes))
	args := make([]any, 0, len(nodes))
	for i, n := range nodes {
		idx[n.ID] = i
		ph = append(ph, "?")
		args = append(args, n.ID)
	}
	for start := 0; start < len(args); start += 500 {
		end := min(start+500, len(args))
		rows, err := s.DB.QueryContext(ctx, "SELECT node_id, tag FROM node_tags WHERE node_id IN ("+strings.Join(ph[start:end], ",")+") ORDER BY tag", args[start:end]...)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id int64
			var tag string
			if err := rows.Scan(&id, &tag); err != nil {
				rows.Close()
				return err
			}
			nodes[idx[id]].Tags = append(nodes[idx[id]].Tags, tag)
		}
		rows.Close()
	}
	return nil
}

// NodeWithTags loads a node and its tags.
func (s *Store) NodeWithTags(ctx context.Context, userID, id int64) (*Node, error) {
	n, err := s.Node(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	list := []Node{*n}
	if err := s.attachTags(ctx, list); err != nil {
		return nil, err
	}
	p, err := s.NodePath(ctx, userID, id)
	if err != nil {
		return nil, err
	}
	list[0].Path = p
	return &list[0], nil
}

// Breadcrumb is one ancestor of a node.
type Breadcrumb struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// Ancestors returns the chain from the root to the node (inclusive).
func (s *Store) Ancestors(ctx context.Context, userID, id int64) ([]Breadcrumb, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH RECURSIVE up(id, parent_id, name, depth) AS (
		SELECT id, parent_id, name, 0 FROM nodes WHERE id = ? AND user_id = ?
		UNION ALL
		SELECT n.id, n.parent_id, n.name, up.depth + 1 FROM nodes n JOIN up ON n.id = up.parent_id WHERE n.user_id = ? AND up.depth < 100
	) SELECT id, name FROM up ORDER BY depth DESC`, id, userID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Breadcrumb
	for rows.Next() {
		var b Breadcrumb
		if err := rows.Scan(&b.ID, &b.Name); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return out, rows.Err()
}

// NodePath returns the virtual path of a node ("/a/b").
func (s *Store) NodePath(ctx context.Context, userID, id int64) (string, error) {
	crumbs, err := s.Ancestors(ctx, userID, id)
	if err != nil {
		return "", err
	}
	var parts []string
	for _, c := range crumbs[1:] {
		parts = append(parts, c.Name)
	}
	return "/" + strings.Join(parts, "/"), nil
}

// Resolve walks a path of validated segments from the user's root.
func (s *Store) Resolve(ctx context.Context, user *User, segs []string) (*Node, error) {
	cur, err := s.Node(ctx, user.ID, user.RootID)
	if err != nil {
		return nil, err
	}
	for _, seg := range segs {
		if !cur.IsDir {
			return nil, ErrNotFound
		}
		cur, err = s.childByName(ctx, s.DB, user.ID, cur.ID, seg)
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// EnsureDirs creates missing folders along segs (mkdir -p) and returns the
// final folder id.
func (s *Store) EnsureDirs(ctx context.Context, q Querier, user *User, segs []string, deviceID *int64) (int64, error) {
	cur := user.RootID
	t := now()
	for _, seg := range segs {
		child, err := s.childByName(ctx, q, user.ID, cur, seg)
		switch {
		case err == nil:
			if !child.IsDir {
				return 0, fmt.Errorf("%w: %q is a file", ErrNotDir, seg)
			}
			cur = child.ID
		case errors.Is(err, ErrNotFound):
			res, err := q.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, created_at, modified_at, updated_at, origin_device_id) VALUES(?,?,?,1,?,?,?,?)`, user.ID, cur, seg, t, t, t, deviceID)
			if isUniqueErr(err) {
				// created concurrently; re-read
				child, err = s.childByName(ctx, q, user.ID, cur, seg)
				if err != nil {
					return 0, err
				}
				if !child.IsDir {
					return 0, fmt.Errorf("%w: %q is a file", ErrNotDir, seg)
				}
				cur = child.ID
				continue
			}
			if err != nil {
				return 0, err
			}
			cur, _ = res.LastInsertId()
		default:
			return 0, err
		}
	}
	return cur, nil
}

// CreateFolder creates a single folder.
func (s *Store) CreateFolder(ctx context.Context, userID, parentID int64, name string) (*Node, error) {
	var id int64
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		parent, err := nodeQ(ctx, tx, userID, parentID)
		if err != nil {
			return ErrParentGone
		}
		if !parent.IsDir {
			return ErrNotDir
		}
		t := now()
		res, err := tx.ExecContext(ctx, `INSERT INTO nodes(user_id, parent_id, name, is_dir, created_at, modified_at, updated_at) VALUES(?,?,?,1,?,?,?)`, userID, parentID, name, t, t, t)
		if isUniqueErr(err) {
			return ErrExists
		}
		if err != nil {
			return err
		}
		id, _ = res.LastInsertId()
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET updated_at = ? WHERE id = ?", t, parentID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.NodeWithTags(ctx, userID, id)
}

func isAncestor(ctx context.Context, q Querier, userID, ancestor, node int64) (bool, error) {
	var found int
	err := q.QueryRowContext(ctx, `WITH RECURSIVE up(id, parent_id, depth) AS (
		SELECT id, parent_id, 0 FROM nodes WHERE id = ? AND user_id = ?
		UNION ALL SELECT n.id, n.parent_id, up.depth+1 FROM nodes n JOIN up ON n.id = up.parent_id WHERE up.depth < 100
	) SELECT COUNT(*) FROM up WHERE id = ?`, node, userID, ancestor).Scan(&found)
	return found > 0, err
}

// MoveRename renames and/or moves a node.
func (s *Store) MoveRename(ctx context.Context, user *User, id int64, newParent *int64, newName *string) (*Node, error) {
	if id == user.RootID {
		return nil, ErrRootNode
	}
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		n, err := nodeQ(ctx, tx, user.ID, id)
		if err != nil {
			return err
		}
		parent := *n.ParentID
		name := n.Name
		if newParent != nil {
			p, err := nodeQ(ctx, tx, user.ID, *newParent)
			if err != nil {
				return ErrParentGone
			}
			if !p.IsDir {
				return ErrNotDir
			}
			if n.IsDir {
				anc, err := isAncestor(ctx, tx, user.ID, n.ID, p.ID)
				if err != nil {
					return err
				}
				if anc {
					return ErrCycle
				}
			}
			parent = p.ID
		}
		if newName != nil {
			name = *newName
		}
		t := now()
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET parent_id = ?, name = ?, updated_at = ? WHERE id = ? AND user_id = ?", parent, name, t, n.ID, user.ID)
		if isUniqueErr(err) {
			return ErrExists
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.NodeWithTags(ctx, user.ID, id)
}

// Trash soft-deletes a node and its descendants.
func (s *Store) Trash(ctx context.Context, user *User, id int64) error {
	if id == user.RootID {
		return ErrRootNode
	}
	return s.Tx(ctx, func(tx *sql.Tx) error {
		return trashTx(ctx, tx, user.ID, id)
	})
}

func trashTx(ctx context.Context, tx *sql.Tx, userID, id int64) error {
	n, err := nodeQ(ctx, tx, userID, id)
	if err != nil {
		return err
	}
	t := now()
	if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
			SELECT id FROM nodes WHERE parent_id = ? AND user_id = ? AND deleted_at IS NULL
			UNION ALL SELECT n.id FROM nodes n JOIN sub ON n.parent_id = sub.id WHERE n.deleted_at IS NULL
		) UPDATE nodes SET deleted_at = ? WHERE id IN (SELECT id FROM sub)`, n.ID, userID, t); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE nodes SET deleted_at = ?, trash_root = 1, original_parent_id = parent_id, updated_at = ? WHERE id = ?", t, t, n.ID)
	return err
}

// TrashItem is a trashed node with its original location.
type TrashItem struct {
	Node
	OriginalPath string `json:"original_path"`
	ItemCount    int64  `json:"item_count"`
	TotalSize    int64  `json:"total_size"`
}

func (s *Store) ListTrash(ctx context.Context, userID int64) ([]TrashItem, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE user_id = ? AND trash_root = 1 AND deleted_at IS NOT NULL ORDER BY deleted_at DESC", userID)
	if err != nil {
		return nil, err
	}
	var items []TrashItem
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, TrashItem{Node: *n})
	}
	rows.Close()
	for i := range items {
		it := &items[i]
		if it.origParent != nil {
			if p, err := s.NodePath(ctx, userID, *it.origParent); err == nil {
				it.OriginalPath = path.Join(p, it.Name)
			}
		}
		if it.OriginalPath == "" {
			it.OriginalPath = "/" + it.Name
		}
		if it.IsDir {
			_ = s.DB.QueryRowContext(ctx, `WITH RECURSIVE sub(id, is_dir, size) AS (
				SELECT id, is_dir, size FROM nodes WHERE parent_id = ?
				UNION ALL SELECT n.id, n.is_dir, n.size FROM nodes n JOIN sub ON n.parent_id = sub.id
			) SELECT COUNT(*), COALESCE(SUM(CASE WHEN is_dir = 0 THEN size ELSE 0 END),0) FROM sub`, it.ID).Scan(&it.ItemCount, &it.TotalSize)
		} else {
			it.TotalSize = it.Size
		}
	}
	if items == nil {
		items = []TrashItem{}
	}
	return items, nil
}

// Restore brings a trash root back. If its folder is gone it is restored to
// the root; if the name is taken a suffix is added.
func (s *Store) Restore(ctx context.Context, user *User, id int64) (*Node, error) {
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		n, err := scanNode(tx.QueryRowContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE id = ? AND user_id = ? AND trash_root = 1 AND deleted_at IS NOT NULL", id, user.ID))
		if err != nil {
			return err
		}
		parent := user.RootID
		if n.origParent != nil {
			if p, err := nodeQ(ctx, tx, user.ID, *n.origParent); err == nil && p.IsDir {
				parent = p.ID
			}
		}
		name := n.Name
		for i := 1; ; i++ {
			if _, err := s.childByName(ctx, tx, user.ID, parent, name); errors.Is(err, ErrNotFound) {
				break
			} else if err != nil {
				return err
			}
			name = numberedName(n.Name, n.IsDir, i, "restored")
			if i > 1000 {
				return ErrExists
			}
		}
		t := now()
		if _, err := tx.ExecContext(ctx, `WITH RECURSIVE sub(id) AS (
				SELECT id FROM nodes WHERE parent_id = ? AND deleted_at = ?
				UNION ALL SELECT n.id FROM nodes n JOIN sub ON n.parent_id = sub.id WHERE n.deleted_at = ?
			) UPDATE nodes SET deleted_at = NULL WHERE id IN (SELECT id FROM sub)`, n.ID, *n.DeletedAt, *n.DeletedAt); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE nodes SET deleted_at = NULL, trash_root = 0, original_parent_id = NULL, parent_id = ?, name = ?, updated_at = ? WHERE id = ?", parent, name, t, n.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return s.NodeWithTags(ctx, user.ID, id)
}

func numberedName(name string, isDir bool, i int, label string) string {
	ext := ""
	if !isDir {
		ext = path.Ext(name)
	}
	stem := strings.TrimSuffix(name, ext)
	if label != "" {
		return fmt.Sprintf("%s (%s %d)%s", stem, label, i, ext)
	}
	return fmt.Sprintf("%s (%d)%s", stem, i, ext)
}

// BlobRef identifies a stored blob file.
type BlobRef struct {
	UserID     int64
	SHA256     string
	LocationID int64
}

// PurgeTrash permanently removes trash roots (all when ids is nil) or those
// older than olderThan (when > 0). It returns blobs whose last reference
// was removed; the caller deletes the files.
func (s *Store) PurgeTrash(ctx context.Context, userID int64, ids []int64, olderThan int64) (int, []BlobRef, error) {
	var freed []BlobRef
	count := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		q := "SELECT id FROM nodes WHERE trash_root = 1 AND deleted_at IS NOT NULL"
		var args []any
		if userID > 0 {
			q += " AND user_id = ?"
			args = append(args, userID)
		}
		if olderThan > 0 {
			q += " AND deleted_at < ?"
			args = append(args, olderThan)
		}
		rows, err := tx.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		var roots []int64
		want := map[int64]bool{}
		for _, id := range ids {
			want[id] = true
		}
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			if ids == nil || want[id] {
				roots = append(roots, id)
			}
		}
		rows.Close()
		for _, root := range roots {
			f, err := purgeTree(ctx, tx, root)
			if err != nil {
				return err
			}
			freed = append(freed, f...)
			count++
		}
		return nil
	})
	return count, freed, err
}

// purgeTree deletes a node and all descendants, adjusting blob refcounts
// and usage.
func purgeTree(ctx context.Context, tx *sql.Tx, root int64) ([]BlobRef, error) {
	const sub = `WITH RECURSIVE sub(id) AS (SELECT ? UNION ALL SELECT n.id FROM nodes n JOIN sub ON n.parent_id = sub.id)`
	rows, err := tx.QueryContext(ctx, sub+` SELECT n.user_id, n.sha256, COUNT(*), SUM(n.size) FROM nodes n WHERE n.id IN (SELECT id FROM sub) AND n.is_dir = 0 AND n.sha256 IS NOT NULL GROUP BY n.user_id, n.sha256`, root)
	if err != nil {
		return nil, err
	}
	type agg struct {
		user  int64
		sha   string
		count int64
		size  int64
	}
	var aggs []agg
	for rows.Next() {
		var a agg
		if err := rows.Scan(&a.user, &a.sha, &a.count, &a.size); err != nil {
			rows.Close()
			return nil, err
		}
		aggs = append(aggs, a)
	}
	rows.Close()
	var freed []BlobRef
	for _, a := range aggs {
		if _, err := tx.ExecContext(ctx, "UPDATE blobs SET refcount = refcount - ? WHERE user_id = ? AND sha256 = ?", a.count, a.user, a.sha); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET used_bytes = MAX(0, used_bytes - ?) WHERE id = ?", a.size, a.user); err != nil {
			return nil, err
		}
		var ref int64
		var loc int64
		err := tx.QueryRowContext(ctx, "SELECT refcount, location_id FROM blobs WHERE user_id = ? AND sha256 = ?", a.user, a.sha).Scan(&ref, &loc)
		if err == nil && ref <= 0 {
			if _, err := tx.ExecContext(ctx, "DELETE FROM blobs WHERE user_id = ? AND sha256 = ?", a.user, a.sha); err != nil {
				return nil, err
			}
			freed = append(freed, BlobRef{UserID: a.user, SHA256: a.sha, LocationID: loc})
		}
	}
	if _, err := tx.ExecContext(ctx, sub+` DELETE FROM node_tags WHERE node_id IN (SELECT id FROM sub)`, root); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, sub+` DELETE FROM nodes WHERE id IN (SELECT id FROM sub)`, root); err != nil {
		return nil, err
	}
	return freed, nil
}

// SetTags replaces a node's tags.
func (s *Store) SetTags(ctx context.Context, userID, id int64, tags []string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if _, err := nodeQ(ctx, tx, userID, id); err != nil {
			return err
		}
		return setTagsTx(ctx, tx, id, tags, true)
	})
}

func setTagsTx(ctx context.Context, tx *sql.Tx, id int64, tags []string, replace bool) error {
	if replace {
		if _, err := tx.ExecContext(ctx, "DELETE FROM node_tags WHERE node_id = ?", id); err != nil {
			return err
		}
	}
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO node_tags(node_id, tag) VALUES(?, ?)", id, t); err != nil {
			return err
		}
	}
	return nil
}

// UserTags lists tags in use with counts.
func (s *Store) UserTags(ctx context.Context, userID int64) ([]map[string]any, error) {
	rows, err := s.DB.QueryContext(ctx, `SELECT t.tag, COUNT(*) FROM node_tags t JOIN nodes n ON n.id = t.node_id WHERE n.user_id = ? AND n.deleted_at IS NULL GROUP BY t.tag COLLATE NOCASE ORDER BY COUNT(*) DESC LIMIT 200`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var tag string
		var c int64
		if err := rows.Scan(&tag, &c); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"tag": tag, "count": c})
	}
	return out, rows.Err()
}

// SearchOptions filters a search.
type SearchOptions struct {
	Query          string
	Kind           string
	Tag            string
	MinSize        int64
	MaxSize        int64
	ModifiedAfter  int64
	ModifiedBefore int64
	Sort           string
	Desc           bool
	Limit          int
}

func (s *Store) Search(ctx context.Context, userID int64, o SearchOptions) ([]Node, error) {
	where := "n.user_id = ? AND n.deleted_at IS NULL AND n.parent_id IS NOT NULL"
	args := []any{userID}
	for _, term := range strings.Fields(o.Query) {
		where += " AND n.name LIKE ? ESCAPE '\\'"
		args = append(args, "%"+likeEscape(term)+"%")
	}
	if o.Kind != "" {
		where += strings.ReplaceAll(strings.ReplaceAll(kindWhere(o.Kind), "is_dir", "n.is_dir"), "mime", "n.mime")
	}
	if o.Tag != "" {
		where += " AND EXISTS (SELECT 1 FROM node_tags t WHERE t.node_id = n.id AND t.tag = ?)"
		args = append(args, o.Tag)
	}
	if o.MinSize > 0 {
		where += " AND n.is_dir = 0 AND n.size >= ?"
		args = append(args, o.MinSize)
	}
	if o.MaxSize > 0 {
		where += " AND n.is_dir = 0 AND n.size <= ?"
		args = append(args, o.MaxSize)
	}
	if o.ModifiedAfter > 0 {
		where += " AND n.modified_at >= ?"
		args = append(args, o.ModifiedAfter)
	}
	if o.ModifiedBefore > 0 {
		where += " AND n.modified_at <= ?"
		args = append(args, o.ModifiedBefore)
	}
	limit := o.Limit
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	order := strings.NewReplacer("is_dir", "n.is_dir", "size", "n.size", "modified_at", "n.modified_at", "mime", "n.mime", "name ", "n.name ").Replace(orderClause(ListOptions{Sort: o.Sort, Desc: o.Desc}))
	cols := strings.ReplaceAll("n."+strings.ReplaceAll(nodeCols, ", ", ", n."), "n.COALESCE(sha256", "COALESCE(n.sha256")
	rows, err := s.DB.QueryContext(ctx, "SELECT "+cols+" FROM nodes n WHERE "+where+" ORDER BY "+order+" LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *n)
	}
	rows.Close()
	if err := s.attachTags(ctx, out); err != nil {
		return nil, err
	}
	for i := range out {
		if p, err := s.NodePath(ctx, userID, out[i].ID); err == nil {
			out[i].Path = p
		}
	}
	if out == nil {
		out = []Node{}
	}
	return out, nil
}

// Descendants returns all live file descendants of a folder with their
// relative paths, for zip downloads.
func (s *Store) Descendants(ctx context.Context, userID, folderID int64, limit int) ([]Node, error) {
	rows, err := s.DB.QueryContext(ctx, `WITH RECURSIVE sub(id, rel) AS (
		SELECT id, name FROM nodes WHERE parent_id = ? AND user_id = ? AND deleted_at IS NULL
		UNION ALL SELECT n.id, sub.rel || '/' || n.name FROM nodes n JOIN sub ON n.parent_id = sub.id WHERE n.deleted_at IS NULL
	) SELECT `+strings.ReplaceAll("n."+strings.ReplaceAll(nodeCols, ", ", ", n."), "n.COALESCE(sha256", "COALESCE(n.sha256")+`, sub.rel FROM nodes n JOIN sub ON n.id = sub.id ORDER BY sub.rel LIMIT ?`, folderID, userID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Node
	for rows.Next() {
		var rel string
		n, err := scanNode(scanStr{rows, &rel})
		if err != nil {
			return nil, err
		}
		n.Path = rel
		out = append(out, *n)
	}
	return out, rows.Err()
}

type scanStr struct {
	rows  *sql.Rows
	extra *string
}

func (s scanStr) Scan(dest ...any) error { return s.rows.Scan(append(dest, s.extra)...) }

// BlobLocation returns where a user's blob is stored.
func (s *Store) BlobLocation(ctx context.Context, userID int64, sha string) (int64, error) {
	var loc int64
	err := s.DB.QueryRowContext(ctx, "SELECT location_id FROM blobs WHERE user_id = ? AND sha256 = ?", userID, sha).Scan(&loc)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return loc, err
}

// HasBlob reports whether the user already stores content with this hash.
func (s *Store) HasBlob(ctx context.Context, userID int64, sha string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM blobs WHERE user_id = ? AND sha256 = ? AND refcount > 0", userID, sha).Scan(&n)
	return n > 0, err
}
