package store

// Smart processing records: detection presets, queues of recordings, the
// segments suggested for them and their review decisions, rendered clips
// and the error history. Everything is scoped to its user. Lifetime
// counters (clips generated, originals deleted, storage reclaimed,
// processing time) live on the queue so they survive later changes.

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

// SmartPreset is a user's saved detection filter.
type SmartPreset struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Filter      string `json:"-"`
	Source      string `json:"source,omitempty"`
	CreatedAt   int64  `json:"created_at"`
	UpdatedAt   int64  `json:"updated_at"`
}

func (s *Store) SmartPresets(ctx context.Context, userID int64) ([]SmartPreset, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, name, description, filter, source, created_at, updated_at FROM smart_presets WHERE user_id = ? ORDER BY name COLLATE NOCASE", userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartPreset{}
	for rows.Next() {
		var p SmartPreset
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.Filter, &p.Source, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) SmartPreset(ctx context.Context, userID, id int64) (*SmartPreset, error) {
	var p SmartPreset
	err := s.DB.QueryRowContext(ctx, "SELECT id, name, description, filter, source, created_at, updated_at FROM smart_presets WHERE id = ? AND user_id = ?", id, userID).
		Scan(&p.ID, &p.Name, &p.Description, &p.Filter, &p.Source, &p.CreatedAt, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &p, err
}

func (s *Store) SaveSmartPreset(ctx context.Context, userID int64, p SmartPreset) (*SmartPreset, error) {
	t := now()
	if p.ID == 0 {
		res, err := s.DB.ExecContext(ctx, "INSERT INTO smart_presets(user_id, name, description, filter, source, created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
			userID, p.Name, p.Description, p.Filter, p.Source, t, t)
		if err != nil {
			return nil, err
		}
		p.ID, _ = res.LastInsertId()
	} else {
		res, err := s.DB.ExecContext(ctx, "UPDATE smart_presets SET name = ?, description = ?, filter = ?, source = ?, updated_at = ? WHERE id = ? AND user_id = ?",
			p.Name, p.Description, p.Filter, p.Source, t, p.ID, userID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return nil, ErrNotFound
		}
	}
	return s.SmartPreset(ctx, userID, p.ID)
}

func (s *Store) DeleteSmartPreset(ctx context.Context, userID, id int64) error {
	res, err := s.DB.ExecContext(ctx, "DELETE FROM smart_presets WHERE id = ? AND user_id = ?", id, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SmartQueue is a named, persistent batch of recordings to process.
type SmartQueue struct {
	ID          int64  `json:"id"`
	UserID      int64  `json:"-"`
	Name        string `json:"name"`
	Description string `json:"description"`
	// State: active, paused, cancelled, completed, archived.
	State       string        `json:"state"`
	Filter      string        `json:"-"`
	Presets     string        `json:"-"`
	Output      string        `json:"-"`
	AutoApprove bool          `json:"auto_approve"`
	FilterRev   int64         `json:"filter_rev"`
	Counters    SmartCounters `json:"counters"`
	CreatedAt   int64         `json:"created_at"`
	UpdatedAt   int64         `json:"updated_at"`
	CompletedAt *int64        `json:"completed_at,omitempty"`
	ArchivedAt  *int64        `json:"archived_at,omitempty"`
}

// SmartCounters are a queue's lifetime totals.
type SmartCounters struct {
	GeneratedCount int64 `json:"generated_count"`
	GeneratedBytes int64 `json:"generated_bytes"`
	DeletedCount   int64 `json:"deleted_count"`
	DeletedBytes   int64 `json:"deleted_bytes"`
	// FreedBytes: storage actually released on disk (content no other
	// file shares).
	FreedBytes int64 `json:"freed_bytes"`
	AnalysisMS int64 `json:"analysis_ms"`
	RenderMS   int64 `json:"render_ms"`
	Failures   int64 `json:"failures"`
}

const smartQueueCols = `id, user_id, name, description, state, filter, presets, output, auto_approve, filter_rev,
	generated_count, generated_bytes, deleted_count, deleted_bytes, freed_bytes, analysis_ms, render_ms, failures,
	created_at, updated_at, completed_at, archived_at`

func scanSmartQueue(sc interface{ Scan(...any) error }) (*SmartQueue, error) {
	var q SmartQueue
	c := &q.Counters
	err := sc.Scan(&q.ID, &q.UserID, &q.Name, &q.Description, &q.State, &q.Filter, &q.Presets, &q.Output, &q.AutoApprove, &q.FilterRev,
		&c.GeneratedCount, &c.GeneratedBytes, &c.DeletedCount, &c.DeletedBytes, &c.FreedBytes, &c.AnalysisMS, &c.RenderMS, &c.Failures,
		&q.CreatedAt, &q.UpdatedAt, &q.CompletedAt, &q.ArchivedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &q, err
}

func (s *Store) CreateSmartQueue(ctx context.Context, q SmartQueue) (*SmartQueue, error) {
	t := now()
	state := "active"
	if q.State == "paused" {
		state = "paused"
	}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO smart_queues(user_id, name, description, state, filter, presets, output, auto_approve, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?)`, q.UserID, q.Name, q.Description, state, q.Filter, q.Presets, q.Output, q.AutoApprove, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.SmartQueue(ctx, q.UserID, id)
}

func (s *Store) SmartQueue(ctx context.Context, userID, id int64) (*SmartQueue, error) {
	return scanSmartQueue(s.DB.QueryRowContext(ctx, "SELECT "+smartQueueCols+" FROM smart_queues WHERE id = ? AND user_id = ?", id, userID))
}

// SmartQueueByID loads a queue for the background runner.
func (s *Store) SmartQueueByID(ctx context.Context, id int64) (*SmartQueue, error) {
	return scanSmartQueue(s.DB.QueryRowContext(ctx, "SELECT "+smartQueueCols+" FROM smart_queues WHERE id = ?", id))
}

// SmartQueues lists a user's queues (all states when states is empty).
func (s *Store) SmartQueues(ctx context.Context, userID int64, states []string) ([]SmartQueue, error) {
	q := "SELECT " + smartQueueCols + " FROM smart_queues WHERE user_id = ?"
	args := []any{userID}
	if len(states) > 0 {
		q += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + ")"
		for _, st := range states {
			args = append(args, st)
		}
	}
	rows, err := s.DB.QueryContext(ctx, q+" ORDER BY updated_at DESC LIMIT 1000", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartQueue{}
	for rows.Next() {
		q, err := scanSmartQueue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *q)
	}
	return out, rows.Err()
}

// UpdateSmartQueue saves name, description, output settings and
// auto-approval.
func (s *Store) UpdateSmartQueue(ctx context.Context, userID int64, q SmartQueue) error {
	res, err := s.DB.ExecContext(ctx, "UPDATE smart_queues SET name = ?, description = ?, output = ?, auto_approve = ?, updated_at = ? WHERE id = ? AND user_id = ?",
		q.Name, q.Description, q.Output, q.AutoApprove, now(), q.ID, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetSmartQueueFilter replaces the filter; analysed recordings are
// evaluated again (their measurements are reused).
func (s *Store) SetSmartQueueFilter(ctx context.Context, userID, id int64, filter, presets string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "UPDATE smart_queues SET filter = ?, presets = ?, filter_rev = filter_rev + 1, updated_at = ? WHERE id = ? AND user_id = ?", filter, presets, now(), id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		// Recordings waiting for review are evaluated again by the runner.
		_, err = tx.ExecContext(ctx, "UPDATE smart_items SET state = 'pending', stage = 're-evaluating', progress = 0, updated_at = ? WHERE queue_id = ? AND state IN ('ready', 'failed')", now(), id)
		return err
	})
}

// SetSmartQueueState changes a queue's state.
func (s *Store) SetSmartQueueState(ctx context.Context, userID, id int64, state string) error {
	t := now()
	q := "UPDATE smart_queues SET state = ?, updated_at = ?"
	args := []any{state, t}
	switch state {
	case "archived":
		q += ", archived_at = ?"
		args = append(args, t)
	case "completed":
		q += ", completed_at = ?"
		args = append(args, t)
	case "active":
		q += ", archived_at = NULL"
	}
	q += " WHERE id = ? AND user_id = ?"
	res, err := s.DB.ExecContext(ctx, q, append(args, id, userID)...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteSmartQueue removes a queue and its records (never files).
func (s *Store) DeleteSmartQueue(ctx context.Context, userID, id int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, "DELETE FROM smart_queues WHERE id = ? AND user_id = ?", id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, t := range []string{"smart_segments", "smart_outputs"} {
			if _, err := tx.ExecContext(ctx, "DELETE FROM "+t+" WHERE queue_id = ?", id); err != nil {
				return err
			}
		}
		return nil
	})
}

// RestartSmartQueue starts a queue over: every recording still kept goes
// back to analysis, suggestions and review decisions are removed, and clips
// not rendered yet are dropped. Rendered clips (files and their links) and
// lifetime totals stay. It returns the content hashes of the recordings, so
// their cached measurements can be discarded.
func (s *Store) RestartSmartQueue(ctx context.Context, userID, id int64) ([]string, error) {
	var shas []string
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		res, err := tx.ExecContext(ctx, "UPDATE smart_queues SET state = 'active', completed_at = NULL, archived_at = NULL, filter_rev = filter_rev + 1, updated_at = ? WHERE id = ? AND user_id = ?", t, id, userID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		for _, q := range []string{
			"DELETE FROM smart_segments WHERE queue_id = ?",
			"DELETE FROM smart_outputs WHERE queue_id = ? AND state != 'done'",
		} {
			if _, err := tx.ExecContext(ctx, q, id); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE smart_items SET state = 'pending', stage = '', progress = 0, error = '', warnings = '[]', detectors = '[]', events = 0,
			analysed_at = NULL, reviewed_at = NULL, updated_at = ? WHERE queue_id = ? AND original_state = 'kept' AND state != 'gone'`, t, id); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, "SELECT DISTINCT sha256 FROM smart_items WHERE queue_id = ? AND state = 'pending'", id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h string
			if rows.Scan(&h) == nil && h != "" {
				shas = append(shas, h)
			}
		}
		return rows.Err()
	})
	return shas, err
}

// AddSmartCounters adds to a queue's lifetime totals.
func (s *Store) AddSmartCounters(ctx context.Context, queueID int64, c SmartCounters) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE smart_queues SET generated_count = generated_count + ?, generated_bytes = generated_bytes + ?,
		deleted_count = deleted_count + ?, deleted_bytes = deleted_bytes + ?, freed_bytes = freed_bytes + ?, analysis_ms = analysis_ms + ?,
		render_ms = render_ms + ?, failures = failures + ?, updated_at = ? WHERE id = ?`,
		c.GeneratedCount, c.GeneratedBytes, c.DeletedCount, c.DeletedBytes, c.FreedBytes, c.AnalysisMS, c.RenderMS, c.Failures, now(), queueID)
	return err
}

// SmartItem is one recording of a queue.
type SmartItem struct {
	ID       int64   `json:"id"`
	QueueID  int64   `json:"queue_id"`
	UserID   int64   `json:"-"`
	NodeID   int64   `json:"node_id"`
	SHA256   string  `json:"sha256"`
	Name     string  `json:"name"`
	Path     string  `json:"path"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration"`
	// State: pending, analysing, ready (to review), reviewed (approved
	// segments wait for rendering), rendering, done, failed, skipped,
	// cancelled, gone (the source was deleted or replaced elsewhere).
	State        string  `json:"state"`
	Stage        string  `json:"stage"`
	Progress     float64 `json:"progress"`
	Error        string  `json:"error,omitempty"`
	Attempts     int     `json:"attempts"`
	Warnings     string  `json:"-"`
	Detectors    string  `json:"-"`
	Events       int     `json:"events"`
	EvalRev      int64   `json:"eval_rev"`
	ProcessingMS int64   `json:"processing_ms"`
	Position     int64   `json:"position"`
	// OriginalState: kept, deleted (permanently, by cleanup) or trashed.
	OriginalState string `json:"original_state"`
	DeletedAt     *int64 `json:"deleted_at,omitempty"`
	DeletedBytes  int64  `json:"deleted_bytes"`
	FreedBytes    int64  `json:"freed_bytes"`
	AddedAt       int64  `json:"added_at"`
	AnalysedAt    *int64 `json:"analysed_at,omitempty"`
	ReviewedAt    *int64 `json:"reviewed_at,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
}

const smartItemCols = `id, queue_id, user_id, node_id, sha256, name, path, size, duration, state, stage, progress, error, attempts, warnings, detectors,
	events, eval_rev, processing_ms, position, original_state, deleted_at, deleted_bytes, freed_bytes, added_at, analysed_at, reviewed_at, updated_at`

func scanSmartItem(sc interface{ Scan(...any) error }) (*SmartItem, error) {
	var i SmartItem
	err := sc.Scan(&i.ID, &i.QueueID, &i.UserID, &i.NodeID, &i.SHA256, &i.Name, &i.Path, &i.Size, &i.Duration, &i.State, &i.Stage, &i.Progress, &i.Error,
		&i.Attempts, &i.Warnings, &i.Detectors, &i.Events, &i.EvalRev, &i.ProcessingMS, &i.Position, &i.OriginalState, &i.DeletedAt, &i.DeletedBytes,
		&i.FreedBytes, &i.AddedAt, &i.AnalysedAt, &i.ReviewedAt, &i.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &i, err
}

func collectSmartItems(rows *sql.Rows, err error) ([]SmartItem, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartItem{}
	for rows.Next() {
		i, err := scanSmartItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *i)
	}
	return out, rows.Err()
}

// AddSmartItems adds recordings to a queue; ones already in it are
// skipped. It returns how many were added.
func (s *Store) AddSmartItems(ctx context.Context, userID, queueID int64, items []SmartItem) (int, error) {
	added := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var pos int64
		if err := tx.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), 0) FROM smart_items WHERE queue_id = ?", queueID).Scan(&pos); err != nil {
			return err
		}
		t := now()
		for _, it := range items {
			pos++
			res, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO smart_items(queue_id, user_id, node_id, sha256, name, path, size, position, added_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?)`, queueID, userID, it.NodeID, it.SHA256, it.Name, it.Path, it.Size, pos, t, t)
			if err != nil {
				return err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
		if added > 0 {
			_, err := tx.ExecContext(ctx, "UPDATE smart_queues SET updated_at = ?, completed_at = NULL, state = CASE WHEN state = 'completed' THEN 'active' ELSE state END WHERE id = ?", t, queueID)
			return err
		}
		return nil
	})
	return added, err
}

// RemoveSmartItems takes recordings out of a queue (their files stay).
func (s *Store) RemoveSmartItems(ctx context.Context, userID, queueID int64, ids []int64) (int, error) {
	n := 0
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			res, err := tx.ExecContext(ctx, "DELETE FROM smart_items WHERE id = ? AND queue_id = ? AND user_id = ? AND state NOT IN ('analysing', 'rendering')", id, queueID, userID)
			if err != nil {
				return err
			}
			if k, _ := res.RowsAffected(); k > 0 {
				n++
				if _, err := tx.ExecContext(ctx, "DELETE FROM smart_segments WHERE item_id = ?", id); err != nil {
					return err
				}
			}
		}
		return nil
	})
	return n, err
}

func (s *Store) SmartItem(ctx context.Context, userID, id int64) (*SmartItem, error) {
	return scanSmartItem(s.DB.QueryRowContext(ctx, "SELECT "+smartItemCols+" FROM smart_items WHERE id = ? AND user_id = ?", id, userID))
}

// SmartItemsQuery selects a queue's items.
type SmartItemsQuery struct {
	States []string
	Limit  int
	Offset int
}

func (s *Store) SmartItems(ctx context.Context, userID, queueID int64, o SmartItemsQuery) ([]SmartItem, int, error) {
	where := " WHERE user_id = ? AND queue_id = ?"
	args := []any{userID, queueID}
	if len(o.States) > 0 {
		where += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(o.States)), ",") + ")"
		for _, st := range o.States {
			args = append(args, st)
		}
	}
	var total int
	if err := s.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM smart_items"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if o.Limit <= 0 || o.Limit > 5000 {
		o.Limit = 5000
	}
	items, err := collectSmartItems(s.DB.QueryContext(ctx, "SELECT "+smartItemCols+" FROM smart_items"+where+" ORDER BY position LIMIT ? OFFSET ?", append(args, o.Limit, o.Offset)...))
	return items, total, err
}

// SmartItemsForNodes lists the items of a user that hold these files.
func (s *Store) SmartItemsForNodes(ctx context.Context, userID int64, nodeIDs []int64) ([]SmartItem, error) {
	if len(nodeIDs) == 0 {
		return []SmartItem{}, nil
	}
	args := []any{userID}
	for _, id := range nodeIDs {
		args = append(args, id)
	}
	return collectSmartItems(s.DB.QueryContext(ctx, "SELECT "+smartItemCols+" FROM smart_items WHERE user_id = ? AND node_id IN ("+
		strings.TrimSuffix(strings.Repeat("?,", len(nodeIDs)), ",")+") ORDER BY id", args...))
}

// SmartItemsOfUser lists every item of a user in the given states.
func (s *Store) SmartItemsOfUser(ctx context.Context, userID int64, states []string) ([]SmartItem, error) {
	q := "SELECT " + smartItemCols + " FROM smart_items WHERE user_id = ?"
	args := []any{userID}
	if len(states) > 0 {
		q += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(states)), ",") + ")"
		for _, st := range states {
			args = append(args, st)
		}
	}
	return collectSmartItems(s.DB.QueryContext(ctx, q+" ORDER BY queue_id, position LIMIT 20000", args...))
}

// ClaimSmartItem takes the next recording to analyse from an active
// queue (oldest queue first) and marks it analysing.
func (s *Store) ClaimSmartItem(ctx context.Context) (*SmartItem, error) {
	var it *SmartItem
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		it, err = scanSmartItem(tx.QueryRowContext(ctx, `SELECT `+prefixCols("i.", smartItemCols)+` FROM smart_items i JOIN smart_queues q ON q.id = i.queue_id
			WHERE i.state = 'pending' AND q.state = 'active' ORDER BY q.created_at, i.position LIMIT 1`))
		if err != nil {
			return err
		}
		t := now()
		_, err = tx.ExecContext(ctx, "UPDATE smart_items SET state = 'analysing', stage = 'starting', progress = 0, attempts = attempts + 1, updated_at = ? WHERE id = ?", t, it.ID)
		it.State = "analysing"
		return err
	})
	if err != nil {
		return nil, err
	}
	return it, nil
}

func prefixCols(p, cols string) string {
	parts := strings.Split(cols, ",")
	for i := range parts {
		parts[i] = p + strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ", ")
}

// SetSmartItemProgress records the stage of a running item.
func (s *Store) SetSmartItemProgress(ctx context.Context, id int64, stage string, progress float64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE smart_items SET stage = ?, progress = ?, updated_at = ? WHERE id = ?", stage, progress, now(), id)
	return err
}

// SmartAnalysed is the outcome of analysing one recording.
type SmartAnalysed struct {
	State     string
	SHA256    string
	Duration  float64
	Detectors string
	Warnings  string
	Events    int
	EvalRev   int64
	ElapsedMS int64
	Error     string
	Segments  []SmartSegment // new suggestions (replace unreviewed ones)
	Approve   bool           // auto-approval: suggestions are approved and the item reviewed
}

// FinishSmartAnalysis stores the suggestions of an analysed recording.
// Suggestions not yet reviewed are replaced; reviewed, edited and manual
// segments are kept and new suggestions overlapping them are dropped.
func (s *Store) FinishSmartAnalysis(ctx context.Context, it *SmartItem, r SmartAnalysed) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		if r.Error != "" {
			if _, err := tx.ExecContext(ctx, "UPDATE smart_items SET state = 'failed', stage = '', progress = 0, error = ?, processing_ms = processing_ms + ?, updated_at = ? WHERE id = ?",
				r.Error, r.ElapsedMS, t, it.ID); err != nil {
				return err
			}
			if _, err := tx.ExecContext(ctx, "INSERT INTO smart_errors(queue_id, item_id, at, stage, message) VALUES (?,?,?,?,?)", it.QueueID, it.ID, t, "analysis", it.Name+": "+r.Error); err != nil {
				return err
			}
			_, err := tx.ExecContext(ctx, "UPDATE smart_queues SET failures = failures + 1, analysis_ms = analysis_ms + ?, updated_at = ? WHERE id = ?", r.ElapsedMS, t, it.QueueID)
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM smart_segments WHERE item_id = ? AND state = 'suggested' AND origin = 'auto'", it.ID); err != nil {
			return err
		}
		kept, err := segmentsTx(ctx, tx, it.ID)
		if err != nil {
			return err
		}
		for _, sg := range r.Segments {
			overlap := false
			for _, k := range kept {
				if overlapShare(sg.Start, sg.End, k.Start, k.End) > 0.5 {
					overlap = true
					break
				}
			}
			if overlap {
				continue
			}
			state := "suggested"
			if r.Approve {
				state = "approved"
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO smart_segments(item_id, queue_id, user_id, start_s, end_s, score, state, origin, category, label, name, reasons, eval_rev, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,'auto',?,?,?,?,?,?,?)`, it.ID, it.QueueID, it.UserID, sg.Start, sg.End, sg.Score, state, sg.Category, sg.Label, sg.Name, sg.Reasons, r.EvalRev, t, t); err != nil {
				return err
			}
		}
		state := r.State
		var reviewed any
		if r.Approve {
			state, reviewed = "reviewed", t
		}
		_, err = tx.ExecContext(ctx, `UPDATE smart_items SET state = ?, stage = '', progress = 1, error = '', sha256 = ?, duration = ?, detectors = ?, warnings = ?, events = ?,
			eval_rev = ?, processing_ms = processing_ms + ?, analysed_at = ?, reviewed_at = COALESCE(?, reviewed_at), updated_at = ? WHERE id = ?`,
			state, r.SHA256, r.Duration, r.Detectors, r.Warnings, r.Events, r.EvalRev, r.ElapsedMS, t, reviewed, t, it.ID)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE smart_queues SET analysis_ms = analysis_ms + ?, updated_at = ? WHERE id = ?", r.ElapsedMS, t, it.QueueID)
		return err
	})
}

func overlapShare(a0, a1, b0, b1 float64) float64 {
	ov := min(a1, b1) - max(a0, b0)
	short := min(a1-a0, b1-b0)
	if ov <= 0 || short <= 0 {
		return 0
	}
	return ov / short
}

// SetSmartItemState changes an item's state (review progress, skips,
// retries); from limits it to items currently in one of those states.
func (s *Store) SetSmartItemState(ctx context.Context, userID, id int64, state string, from ...string) error {
	t := now()
	q := "UPDATE smart_items SET state = ?, updated_at = ?"
	args := []any{state, t}
	switch state {
	case "reviewed":
		q += ", reviewed_at = ?"
		args = append(args, t)
	case "pending":
		q += ", error = '', stage = '', progress = 0"
	}
	q += " WHERE id = ? AND user_id = ?"
	args = append(args, id, userID)
	if len(from) > 0 {
		q += " AND state IN (" + strings.TrimSuffix(strings.Repeat("?,", len(from)), ",") + ")"
		for _, f := range from {
			args = append(args, f)
		}
	}
	res, err := s.DB.ExecContext(ctx, q, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	// Work again in a completed queue reopens it, so the runner picks it up.
	if state == "pending" || state == "ready" || state == "reviewed" {
		_, err = s.DB.ExecContext(ctx, "UPDATE smart_queues SET state = 'active', completed_at = NULL, updated_at = ? WHERE state = 'completed' AND id = (SELECT queue_id FROM smart_items WHERE id = ?)", t, id)
	}
	return err
}

// SetQueueItemsState moves a queue's items between states (pause,
// cancel, retry); it returns how many changed.
func (s *Store) SetQueueItemsState(ctx context.Context, queueID int64, from []string, to string) (int64, error) {
	args := []any{to, now(), queueID}
	for _, f := range from {
		args = append(args, f)
	}
	extra := ""
	if to == "pending" {
		extra = ", error = '', stage = '', progress = 0"
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE smart_items SET state = ?, updated_at = ?"+extra+" WHERE queue_id = ? AND state IN ("+strings.TrimSuffix(strings.Repeat("?,", len(from)), ",")+")", args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// MarkSmartOriginal records what cleanup did to an item's original.
func (s *Store) MarkSmartOriginal(ctx context.Context, it *SmartItem, state string, deleted, freed int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		if _, err := tx.ExecContext(ctx, "UPDATE smart_items SET original_state = ?, deleted_at = ?, deleted_bytes = ?, freed_bytes = ?, updated_at = ? WHERE id = ?",
			state, t, deleted, freed, t, it.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE smart_queues SET deleted_count = deleted_count + 1, deleted_bytes = deleted_bytes + ?, freed_bytes = freed_bytes + ?, updated_at = ? WHERE id = ?",
			deleted, freed, t, it.QueueID)
		return err
	})
}

// MarkSmartSourceGone flags the items of other queues whose source file
// was removed.
func (s *Store) MarkSmartSourceGone(ctx context.Context, userID, nodeID, exceptItem int64) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE smart_items SET state = CASE WHEN state IN ('done', 'reviewed') THEN state ELSE 'gone' END,
		original_state = CASE WHEN original_state = 'kept' THEN 'removed' ELSE original_state END, updated_at = ?
		WHERE user_id = ? AND node_id = ? AND id != ?`, now(), userID, nodeID, exceptItem)
	return err
}

// RecoverSmart resets work a restart interrupted: analysis restarts from
// its checkpoints, renders from scratch.
func (s *Store) RecoverSmart(ctx context.Context) (items, outputs int64, err error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE smart_items SET state = 'pending', stage = 'resuming', updated_at = ? WHERE state = 'analysing'", now())
	if err != nil {
		return 0, 0, err
	}
	items, _ = res.RowsAffected()
	res, err = s.DB.ExecContext(ctx, "UPDATE smart_outputs SET state = 'pending' WHERE state = 'rendering'")
	if err != nil {
		return items, 0, err
	}
	outputs, _ = res.RowsAffected()
	_, err = s.DB.ExecContext(ctx, "UPDATE smart_items SET state = 'reviewed', updated_at = ? WHERE state = 'rendering'", now())
	return items, outputs, err
}

// SmartSegment is a suggested, edited or manual segment.
type SmartSegment struct {
	ID       int64   `json:"id"`
	ItemID   int64   `json:"item_id"`
	QueueID  int64   `json:"queue_id"`
	Start    float64 `json:"start"`
	End      float64 `json:"end"`
	Score    float64 `json:"score"`
	State    string  `json:"state"`  // suggested, approved, rejected
	Origin   string  `json:"origin"` // auto, edited, manual, split, merged
	Category string  `json:"category"`
	Label    string  `json:"label"`
	Name     string  `json:"name"`
	Reasons  string  `json:"-"`
	EvalRev  int64   `json:"eval_rev"`
	Created  int64   `json:"created_at"`
	Updated  int64   `json:"updated_at"`
}

const smartSegCols = "id, item_id, queue_id, start_s, end_s, score, state, origin, category, label, name, reasons, eval_rev, created_at, updated_at"

func scanSmartSegment(sc interface{ Scan(...any) error }) (*SmartSegment, error) {
	var g SmartSegment
	err := sc.Scan(&g.ID, &g.ItemID, &g.QueueID, &g.Start, &g.End, &g.Score, &g.State, &g.Origin, &g.Category, &g.Label, &g.Name, &g.Reasons, &g.EvalRev, &g.Created, &g.Updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &g, err
}

func segmentsTx(ctx context.Context, q interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, itemID int64) ([]SmartSegment, error) {
	rows, err := q.QueryContext(ctx, "SELECT "+smartSegCols+" FROM smart_segments WHERE item_id = ? ORDER BY start_s, id", itemID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartSegment{}
	for rows.Next() {
		g, err := scanSmartSegment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// SmartSegments lists an item's segments by start.
func (s *Store) SmartSegments(ctx context.Context, itemID int64) ([]SmartSegment, error) {
	return segmentsTx(ctx, s.DB, itemID)
}

func (s *Store) SmartSegment(ctx context.Context, userID, id int64) (*SmartSegment, error) {
	return scanSmartSegment(s.DB.QueryRowContext(ctx, "SELECT "+smartSegCols+" FROM smart_segments WHERE id = ? AND user_id = ?", id, userID))
}

// CreateSmartSegment adds a segment (manual, split or merged).
func (s *Store) CreateSmartSegment(ctx context.Context, userID int64, g SmartSegment) (*SmartSegment, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, `INSERT INTO smart_segments(item_id, queue_id, user_id, start_s, end_s, score, state, origin, category, label, name, reasons, eval_rev, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, g.ItemID, g.QueueID, userID, g.Start, g.End, g.Score, g.State, g.Origin, g.Category, g.Label, g.Name, nonEmpty(g.Reasons, "[]"), g.EvalRev, t, t)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.SmartSegment(ctx, userID, id)
}

func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// UpdateSmartSegment saves boundaries, state and name. A suggestion whose
// boundaries changed counts as edited (it is kept on re-evaluation).
func (s *Store) UpdateSmartSegment(ctx context.Context, userID int64, g SmartSegment) (*SmartSegment, error) {
	res, err := s.DB.ExecContext(ctx, `UPDATE smart_segments SET start_s = ?, end_s = ?, state = ?, name = ?,
		origin = CASE WHEN origin = 'auto' AND (start_s != ? OR end_s != ?) THEN 'edited' ELSE origin END, updated_at = ? WHERE id = ? AND user_id = ?`,
		g.Start, g.End, g.State, g.Name, g.Start, g.End, now(), g.ID, userID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrNotFound
	}
	return s.SmartSegment(ctx, userID, g.ID)
}

// SetSmartSegmentsState approves or rejects segments in bulk.
func (s *Store) SetSmartSegmentsState(ctx context.Context, userID int64, ids []int64, state string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	args := []any{state, now(), userID}
	for _, id := range ids {
		args = append(args, id)
	}
	res, err := s.DB.ExecContext(ctx, "UPDATE smart_segments SET state = ?, updated_at = ? WHERE user_id = ? AND id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",")+")", args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) DeleteSmartSegments(ctx context.Context, userID int64, ids []int64) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		for _, id := range ids {
			if _, err := tx.ExecContext(ctx, "DELETE FROM smart_segments WHERE id = ? AND user_id = ?", id, userID); err != nil {
				return err
			}
		}
		return nil
	})
}

// SmartOutput is a clip rendered from a segment.
type SmartOutput struct {
	ID         int64  `json:"id"`
	SegmentID  int64  `json:"segment_id"`
	ItemID     int64  `json:"item_id"`
	QueueID    int64  `json:"queue_id"`
	UserID     int64  `json:"-"`
	RenderKey  string `json:"render_key"`
	State      string `json:"state"` // pending, rendering, done, failed
	NodeID     *int64 `json:"node_id,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	Mode       string `json:"mode"`
	Error      string `json:"error,omitempty"`
	Attempts   int    `json:"attempts"`
	RenderMS   int64  `json:"render_ms"`
	CreatedAt  int64  `json:"created_at"`
	StartedAt  *int64 `json:"started_at,omitempty"`
	FinishedAt *int64 `json:"finished_at,omitempty"`
	VerifiedAt *int64 `json:"verified_at,omitempty"`
}

const smartOutCols = "id, segment_id, item_id, queue_id, user_id, render_key, state, node_id, sha256, size, name, path, mode, error, attempts, render_ms, created_at, started_at, finished_at, verified_at"

func scanSmartOutput(sc interface{ Scan(...any) error }) (*SmartOutput, error) {
	var o SmartOutput
	err := sc.Scan(&o.ID, &o.SegmentID, &o.ItemID, &o.QueueID, &o.UserID, &o.RenderKey, &o.State, &o.NodeID, &o.SHA256, &o.Size, &o.Name, &o.Path, &o.Mode,
		&o.Error, &o.Attempts, &o.RenderMS, &o.CreatedAt, &o.StartedAt, &o.FinishedAt, &o.VerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &o, err
}

func collectSmartOutputs(rows *sql.Rows, err error) ([]SmartOutput, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartOutput{}
	for rows.Next() {
		o, err := scanSmartOutput(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
}

// SmartOutputs lists an item's clips.
func (s *Store) SmartOutputs(ctx context.Context, itemID int64) ([]SmartOutput, error) {
	return collectSmartOutputs(s.DB.QueryContext(ctx, "SELECT "+smartOutCols+" FROM smart_outputs WHERE item_id = ? ORDER BY id", itemID))
}

// SmartQueueOutputs lists a queue's clips.
func (s *Store) SmartQueueOutputs(ctx context.Context, queueID int64) ([]SmartOutput, error) {
	return collectSmartOutputs(s.DB.QueryContext(ctx, "SELECT "+smartOutCols+" FROM smart_outputs WHERE queue_id = ? ORDER BY id", queueID))
}

// PlanSmartRenders creates the missing outputs of an item's approved
// segments (one per segment and render key, so retries never duplicate
// clips) and returns those still to render.
func (s *Store) PlanSmartRenders(ctx context.Context, it *SmartItem, keys map[int64]string) ([]SmartOutput, error) {
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		for seg, key := range keys {
			if _, err := tx.ExecContext(ctx, "INSERT OR IGNORE INTO smart_outputs(segment_id, item_id, queue_id, user_id, render_key, created_at) VALUES (?,?,?,?,?,?)",
				seg, it.ID, it.QueueID, it.UserID, key, t); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	outs, err := s.SmartOutputs(ctx, it.ID)
	if err != nil {
		return nil, err
	}
	var todo []SmartOutput
	for _, o := range outs {
		if key, ok := keys[o.SegmentID]; ok && key == o.RenderKey && o.State != "done" {
			todo = append(todo, o)
		}
	}
	return todo, nil
}

// StartSmartOutput marks a clip as rendering.
func (s *Store) StartSmartOutput(ctx context.Context, id int64, name string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE smart_outputs SET state = 'rendering', name = ?, attempts = attempts + 1, started_at = ?, error = '' WHERE id = ?", name, now(), id)
	return err
}

// FinishSmartOutputTx links a stored clip to its output inside the
// commit's transaction (see CommitRequest.AfterTx).
func FinishSmartOutputTx(ctx context.Context, tx *sql.Tx, o *SmartOutput, nodeID int64) error {
	t := now()
	_, err := tx.ExecContext(ctx, "UPDATE smart_outputs SET state = 'done', node_id = ?, sha256 = ?, size = ?, name = ?, path = ?, mode = ?, render_ms = ?, finished_at = ?, verified_at = ? WHERE id = ?",
		nodeID, o.SHA256, o.Size, o.Name, o.Path, o.Mode, o.RenderMS, t, t, o.ID)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "UPDATE smart_queues SET generated_count = generated_count + 1, generated_bytes = generated_bytes + ?, render_ms = render_ms + ?, updated_at = ? WHERE id = ?",
		o.Size, o.RenderMS, t, o.QueueID)
	return err
}

// SetSmartOutputPath records where a clip was stored (its name may have
// changed on a name clash).
func (s *Store) SetSmartOutputPath(ctx context.Context, id int64, name, path string) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE smart_outputs SET name = ?, path = ? WHERE id = ?", name, path, id)
	return err
}

// FailSmartOutput records a failed render.
func (s *Store) FailSmartOutput(ctx context.Context, o *SmartOutput, msg string) error {
	return s.Tx(ctx, func(tx *sql.Tx) error {
		t := now()
		if _, err := tx.ExecContext(ctx, "UPDATE smart_outputs SET state = 'failed', error = ?, finished_at = ? WHERE id = ?", msg, t, o.ID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO smart_errors(queue_id, item_id, at, stage, message) VALUES (?,?,?,?,?)", o.QueueID, o.ItemID, t, "render", msg); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE smart_queues SET failures = failures + 1, updated_at = ? WHERE id = ?", t, o.QueueID)
		return err
	})
}

// ResetFailedSmartOutputs lets failed clips of a queue be rendered again.
func (s *Store) ResetFailedSmartOutputs(ctx context.Context, queueID int64) error {
	_, err := s.DB.ExecContext(ctx, "UPDATE smart_outputs SET state = 'pending', error = '' WHERE queue_id = ? AND state = 'failed'", queueID)
	return err
}

// ClaimSmartRender takes the next reviewed item with approved segments
// to render (active queues only) and marks it rendering.
func (s *Store) ClaimSmartRender(ctx context.Context) (*SmartItem, error) {
	var it *SmartItem
	err := s.Tx(ctx, func(tx *sql.Tx) error {
		var err error
		it, err = scanSmartItem(tx.QueryRowContext(ctx, `SELECT `+prefixCols("i.", smartItemCols)+` FROM smart_items i JOIN smart_queues q ON q.id = i.queue_id
			WHERE i.state = 'reviewed' AND q.state = 'active' ORDER BY i.reviewed_at, i.id LIMIT 1`))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, "UPDATE smart_items SET state = 'rendering', stage = 'rendering', progress = 0, updated_at = ? WHERE id = ?", now(), it.ID)
		it.State = "rendering"
		return err
	})
	return it, err
}

// SmartError is an entry of a queue's error history.
type SmartError struct {
	ID      int64  `json:"id"`
	ItemID  *int64 `json:"item_id,omitempty"`
	At      int64  `json:"at"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

func (s *Store) SmartErrors(ctx context.Context, queueID int64, limit int) ([]SmartError, error) {
	rows, err := s.DB.QueryContext(ctx, "SELECT id, item_id, at, stage, message FROM smart_errors WHERE queue_id = ? ORDER BY at DESC, id DESC LIMIT ?", queueID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SmartError{}
	for rows.Next() {
		var e SmartError
		if err := rows.Scan(&e.ID, &e.ItemID, &e.At, &e.Stage, &e.Message); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LogSmartError adds to a queue's error history.
func (s *Store) LogSmartError(ctx context.Context, queueID int64, itemID *int64, stage, msg string) {
	_, _ = s.DB.ExecContext(ctx, "INSERT INTO smart_errors(queue_id, item_id, at, stage, message) VALUES (?,?,?,?,?)", queueID, itemID, now(), stage, msg)
}

// SmartQueueTally counts a queue's items and segments.
type SmartQueueTally struct {
	Items         int64            `json:"items"`
	ItemStates    map[string]int64 `json:"item_states"`
	OriginalBytes int64            `json:"original_bytes"`
	Analysed      int64            `json:"analysed"`
	Events        int64            `json:"events"`
	Segments      map[string]int64 `json:"segments"`
	Outputs       map[string]int64 `json:"outputs"`
	OutputBytes   int64            `json:"output_bytes"`
	ProcessingMS  int64            `json:"processing_ms"`
}

func (s *Store) SmartTally(ctx context.Context, queueID int64) (*SmartQueueTally, error) {
	t := &SmartQueueTally{ItemStates: map[string]int64{}, Segments: map[string]int64{}, Outputs: map[string]int64{}}
	rows, err := s.DB.QueryContext(ctx, "SELECT state, COUNT(*), COALESCE(SUM(size),0), COALESCE(SUM(events),0), COALESCE(SUM(processing_ms),0), COALESCE(SUM(analysed_at IS NOT NULL),0) FROM smart_items WHERE queue_id = ? GROUP BY state", queueID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n, size, ev, ms, an int64
		if err := rows.Scan(&st, &n, &size, &ev, &ms, &an); err != nil {
			rows.Close()
			return nil, err
		}
		t.ItemStates[st] = n
		t.Items += n
		t.OriginalBytes += size
		t.Events += ev
		t.ProcessingMS += ms
		t.Analysed += an
	}
	rows.Close()
	rows, err = s.DB.QueryContext(ctx, "SELECT state, COUNT(*) FROM smart_segments WHERE queue_id = ? GROUP BY state", queueID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var st string
		var n int64
		if err := rows.Scan(&st, &n); err != nil {
			rows.Close()
			return nil, err
		}
		t.Segments[st] = n
	}
	rows.Close()
	rows, err = s.DB.QueryContext(ctx, "SELECT state, COUNT(*), COALESCE(SUM(size),0) FROM smart_outputs WHERE queue_id = ? GROUP BY state", queueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var st string
		var n, size int64
		if err := rows.Scan(&st, &n, &size); err != nil {
			return nil, err
		}
		t.Outputs[st] = n
		if st == "done" {
			t.OutputBytes += size
		}
	}
	return t, rows.Err()
}
