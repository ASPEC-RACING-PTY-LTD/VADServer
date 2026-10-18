package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// --- storage locations ---

type Location struct {
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Path         string `json:"path"`
	ReserveBytes int64  `json:"reserve_bytes"`
	Enabled      bool   `json:"enabled"`
	IsDefault    bool   `json:"is_default"`
	CreatedAt    int64  `json:"created_at"`
}

func (s *Store) ListLocations(ctx context.Context) ([]Location, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, path, reserve_bytes, enabled, is_default, created_at FROM storage_locations ORDER BY is_default DESC, name")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		var l Location
		if err := rows.Scan(&l.ID, &l.Name, &l.Path, &l.ReserveBytes, &l.Enabled, &l.IsDefault, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Location(ctx context.Context, id int64) (*Location, error) {
	var l Location
	err := s.DB.QueryRowContext(ctx, "SELECT id, name, path, reserve_bytes, enabled, is_default, created_at FROM storage_locations WHERE id = ?", id).
		Scan(&l.ID, &l.Name, &l.Path, &l.ReserveBytes, &l.Enabled, &l.IsDefault, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &l, err
}

func (s *Store) DefaultLocation(ctx context.Context) (*Location, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, "SELECT id FROM storage_locations WHERE is_default = 1 LIMIT 1").Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return s.Location(ctx, id)
}

func (s *Store) CreateLocation(ctx context.Context, l *Location) error {
	res, err := s.DB.ExecContext(ctx, "INSERT INTO storage_locations(name, path, reserve_bytes, enabled, is_default, created_at) VALUES(?,?,?,?,?,?)", l.Name, l.Path, l.ReserveBytes, l.Enabled, l.IsDefault, now())
	if isUniqueErr(err) {
		return ErrConflict
	}
	if err != nil {
		return err
	}
	l.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) UpdateLocation(ctx context.Context, l *Location) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		if l.IsDefault {
			if _, err := tx.ExecContext(ctx, "UPDATE storage_locations SET is_default = 0"); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx, "UPDATE storage_locations SET name = ?, reserve_bytes = ?, enabled = ?, is_default = ? WHERE id = ?", l.Name, l.ReserveBytes, l.Enabled, l.IsDefault, l.ID)
		if isUniqueErr(err) {
			return ErrConflict
		}
		return err
	})
}

// LocationUsage returns bytes stored and blob count on a location.
func (s *Store) LocationUsage(ctx context.Context, id int64) (int64, int64, int64, error) {
	var bytes, blobs, users int64
	err := s.DB.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0), COUNT(*) FROM blobs WHERE location_id = ?", id).Scan(&bytes, &blobs)
	if err != nil {
		return 0, 0, 0, err
	}
	err = s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE location_id = ?", id).Scan(&users)
	return bytes, blobs, users, err
}

func (s *Store) DeleteLocation(ctx context.Context, id int64) error {
	_, err := s.DB.ExecContext(ctx, "DELETE FROM storage_locations WHERE id = ? AND is_default = 0", id)
	return err
}

// --- automations ---

type AutomationRow struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"user_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Enabled     bool   `json:"enabled"`
	Priority    int    `json:"priority"`
	IsTemplate  bool   `json:"is_template"`
	Valid       bool   `json:"valid"`
	Definition  string `json:"-"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

const autoCols = "id, user_id, name, description, enabled, priority, is_template, valid, definition, created_at, updated_at"

func scanAuto(sc interface{ Scan(...any) error }) (*AutomationRow, error) {
	var a AutomationRow
	err := sc.Scan(&a.ID, &a.UserID, &a.Name, &a.Description, &a.Enabled, &a.Priority, &a.IsTemplate, &a.Valid, &a.Definition, &a.CreatedAt, &a.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &a, err
}

func (s *Store) ListAutomations(ctx context.Context, userID int64, templates *bool, enabledOnly bool) ([]AutomationRow, error) {
	q := "SELECT " + autoCols + " FROM automations WHERE user_id = ?"
	args := []any{userID}
	if templates != nil {
		q += " AND is_template = ?"
		args = append(args, *templates)
	}
	if enabledOnly {
		q += " AND enabled = 1 AND valid = 1"
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY priority DESC, name COLLATE NOCASE", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AutomationRow{}
	for rows.Next() {
		a, err := scanAuto(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *a)
	}
	return out, rows.Err()
}

func (s *Store) Automation(ctx context.Context, userID, id int64) (*AutomationRow, error) {
	return scanAuto(s.DB.QueryRowContext(ctx, "SELECT "+autoCols+" FROM automations WHERE id = ? AND user_id = ?", id, userID))
}

func (s *Store) SaveAutomation(ctx context.Context, a *AutomationRow) error {
	t := now()
	if a.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO automations(user_id, name, description, enabled, priority, is_template, valid, definition, created_at, updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",
			a.UserID, a.Name, a.Description, a.Enabled, a.Priority, a.IsTemplate, a.Valid, a.Definition, t, t)
		if err != nil {
			return err
		}
		a.ID, _ = res.LastInsertId()
		a.CreatedAt, a.UpdatedAt = t, t
		return nil
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE automations SET name = ?, description = ?, enabled = ?, priority = ?, is_template = ?, valid = ?, definition = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		a.Name, a.Description, a.Enabled, a.Priority, a.IsTemplate, a.Valid, a.Definition, t, a.ID, a.UserID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	a.UpdatedAt = t
	return nil
}

func (s *Store) DeleteAutomation(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM automations WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// --- activity / audit ---

type Activity struct {
	ID         int64  `json:"id"`
	UserID     *int64 `json:"user_id"`
	Username   string `json:"username,omitempty"`
	ActorID    *int64 `json:"actor_id"`
	ActorName  string `json:"actor_name,omitempty"`
	DeviceID   *int64 `json:"device_id"`
	DeviceName string `json:"device_name,omitempty"`
	Action     string `json:"action"`
	Target     string `json:"target"`
	Details    string `json:"details"`
	IP         string `json:"ip"`
	Audit      bool   `json:"audit"`
	CreatedAt  int64  `json:"created_at"`
}

func (s *Store) LogActivity(ctx context.Context, a Activity) {
	_, _ = s.DB.ExecContext(ctx, "INSERT INTO activity(user_id, actor_id, device_id, action, target, details, ip, audit, created_at) VALUES(?,?,?,?,?,?,?,?,?)",
		a.UserID, a.ActorID, a.DeviceID, a.Action, truncate(a.Target, 1024), truncate(a.Details, 2048), a.IP, a.Audit, now())
}

type ActivityFilter struct {
	UserID   int64
	AuditAll bool // admin audit log: every audit row
	Action   string
	Before   int64
	Limit    int
}

func (s *Store) ListActivity(ctx context.Context, f ActivityFilter) ([]Activity, error) {
	where := []string{"1=1"}
	var args []any
	if f.AuditAll {
		where = append(where, "a.audit = 1")
	} else {
		where = append(where, "a.user_id = ?")
		args = append(args, f.UserID)
	}
	if f.Action != "" {
		where = append(where, "a.action LIKE ? ESCAPE '\\'")
		args = append(args, likeEscape(f.Action)+"%")
	}
	if f.Before > 0 {
		where = append(where, "a.id < ?")
		args = append(args, f.Before)
	}
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT a.id, a.user_id, COALESCE(u.username,''), a.actor_id, COALESCE(ac.username,''), a.device_id, COALESCE(d.name,''), a.action, a.target, a.details, a.ip, a.audit, a.created_at
		FROM activity a LEFT JOIN users u ON u.id = a.user_id LEFT JOIN users ac ON ac.id = a.actor_id LEFT JOIN devices d ON d.id = a.device_id
		WHERE `+strings.Join(where, " AND ")+" ORDER BY a.id DESC LIMIT ?", append(args, limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Activity{}
	for rows.Next() {
		var a Activity
		var u, ac, d sql.NullInt64
		if err := rows.Scan(&a.ID, &u, &a.Username, &ac, &a.ActorName, &d, &a.DeviceName, &a.Action, &a.Target, &a.Details, &a.IP, &a.Audit, &a.CreatedAt); err != nil {
			return nil, err
		}
		a.UserID, a.ActorID, a.DeviceID = nullInt(u), nullInt(ac), nullInt(d)
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) PruneActivity(ctx context.Context, olderThan int64) {
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM activity WHERE created_at < ? AND audit = 0", olderThan)
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM transfers WHERE started_at < ? AND status != 'active'", olderThan)
	_, _ = s.DB.ExecContext(ctx, "DELETE FROM notifications WHERE created_at < ?", olderThan)
}

// --- notifications ---

type Notification struct {
	ID        int64  `json:"id"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Level     string `json:"level"`
	Source    string `json:"source"`
	ReadAt    *int64 `json:"read_at"`
	CreatedAt int64  `json:"created_at"`
}

func (s *Store) AddNotification(ctx context.Context, userID int64, n Notification) error {
	_, err := s.DB.ExecContext(ctx, "INSERT INTO notifications(user_id, title, message, level, source, created_at) VALUES(?,?,?,?,?,?)",
		userID, truncate(n.Title, 200), truncate(n.Message, 2000), n.Level, truncate(n.Source, 100), now())
	return err
}

func (s *Store) ListNotifications(ctx context.Context, userID int64, limit int) ([]Notification, int, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.DB.QueryContext(ctx, "SELECT id, title, message, level, source, read_at, created_at FROM notifications WHERE user_id = ? ORDER BY id DESC LIMIT ?", userID, limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		var n Notification
		var r sql.NullInt64
		if err := rows.Scan(&n.ID, &n.Title, &n.Message, &n.Level, &n.Source, &r, &n.CreatedAt); err != nil {
			return nil, 0, err
		}
		n.ReadAt = nullInt(r)
		out = append(out, n)
	}
	var unread int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM notifications WHERE user_id = ? AND read_at IS NULL", userID).Scan(&unread); err != nil {
		return nil, 0, err
	}
	return out, unread, rows.Err()
}

func (s *Store) MarkNotificationsRead(ctx context.Context, userID int64, id int64) error {
	if id > 0 {
		_, err := s.DB.ExecContext(ctx, "UPDATE notifications SET read_at = ? WHERE user_id = ? AND id = ? AND read_at IS NULL", now(), userID, id)
		return err
	}
	_, err := s.DB.ExecContext(ctx, "UPDATE notifications SET read_at = ? WHERE user_id = ? AND read_at IS NULL", now(), userID)
	return err
}

// --- stats ---

type UserStats struct {
	Files       int64            `json:"files"`
	Folders     int64            `json:"folders"`
	TrashItems  int64            `json:"trash_items"`
	TrashBytes  int64            `json:"trash_bytes"`
	ByKind      map[string]int64 `json:"bytes_by_kind"`
	CountByKind map[string]int64 `json:"count_by_kind"`
}

func (s *Store) UserStats(ctx context.Context, userID int64) (*UserStats, error) {
	st := &UserStats{ByKind: map[string]int64{}, CountByKind: map[string]int64{}}
	err := s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(is_dir = 0),0), COALESCE(SUM(is_dir = 1 AND parent_id IS NOT NULL),0) FROM nodes WHERE user_id = ? AND deleted_at IS NULL`, userID).Scan(&st.Files, &st.Folders)
	if err != nil {
		return nil, err
	}
	err = s.DB.QueryRowContext(ctx, `SELECT COALESCE(SUM(trash_root),0), COALESCE(SUM(CASE WHEN is_dir = 0 THEN size ELSE 0 END),0) FROM nodes WHERE user_id = ? AND deleted_at IS NOT NULL`, userID).Scan(&st.TrashItems, &st.TrashBytes)
	if err != nil {
		return nil, err
	}
	rows, err := s.DB.QueryContext(ctx, `SELECT CASE
		WHEN mime LIKE 'video/%' THEN 'video'
		WHEN mime LIKE 'image/%' THEN 'image'
		WHEN mime LIKE 'audio/%' THEN 'audio'
		WHEN `+mimeKinds["document"]+` THEN 'document'
		WHEN `+mimeKinds["archive"]+` THEN 'archive'
		ELSE 'other' END AS k, COALESCE(SUM(size),0), COUNT(*)
		FROM nodes WHERE user_id = ? AND is_dir = 0 AND deleted_at IS NULL GROUP BY k`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		var b, c int64
		if err := rows.Scan(&k, &b, &c); err != nil {
			return nil, err
		}
		st.ByKind[k] = b
		st.CountByKind[k] = c
	}
	return st, rows.Err()
}

// RecentFiles returns recently added files.
func (s *Store) RecentFiles(ctx context.Context, userID int64, limit int) ([]Node, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT "+nodeCols+" FROM nodes WHERE user_id = ? AND is_dir = 0 AND deleted_at IS NULL ORDER BY created_at DESC LIMIT ?", userID, limit)
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

// GlobalStats for the admin dashboard.
type GlobalStats struct {
	Users         int64 `json:"users"`
	ActiveUsers   int64 `json:"active_users"`
	Files         int64 `json:"files"`
	LogicalBytes  int64 `json:"logical_bytes"`
	PhysicalBytes int64 `json:"physical_bytes"`
	Devices       int64 `json:"devices"`
	PendingDevs   int64 `json:"pending_devices"`
	OpenUploads   int64 `json:"open_uploads"`
}

func (s *Store) GlobalStats(ctx context.Context) (*GlobalStats, error) {
	g := &GlobalStats{}
	err := s.DB.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM users),
		(SELECT COUNT(*) FROM users WHERE disabled = 0),
		(SELECT COUNT(*) FROM nodes WHERE is_dir = 0),
		(SELECT COALESCE(SUM(used_bytes),0) FROM users),
		(SELECT COALESCE(SUM(size),0) FROM blobs),
		(SELECT COUNT(*) FROM devices WHERE status = 'approved'),
		(SELECT COUNT(*) FROM devices WHERE status = 'pending' AND expires_at > ?),
		(SELECT COUNT(*) FROM uploads WHERE status IN ('open','finalizing'))`, now()).
		Scan(&g.Users, &g.ActiveUsers, &g.Files, &g.LogicalBytes, &g.PhysicalBytes, &g.Devices, &g.PendingDevs, &g.OpenUploads)
	return g, err
}
