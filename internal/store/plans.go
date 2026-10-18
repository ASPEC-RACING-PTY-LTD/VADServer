package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// Plan/execute (api-v1 §1): a …/plan route stores the plan and hands the
// caller a random token; …/execute presents the token, the plan is
// recomputed from current state and must still have the same digest. Only
// the SHA-256 of the token is stored, and a plan executes at most once.

// ErrPlanStale means a plan token is unknown, expired, already used, issued
// to someone else or for another kind, or the plan no longer matches current
// state. The API maps it to 409 plan_stale.
var ErrPlanStale = errors.New("plan stale")

// DefaultPlanTTL is the plan lifetime when NewPlan.TTL is zero.
const DefaultPlanTTL = 15 * time.Minute

// Plan is a stored plan. Plan holds the canonical plan JSON.
type Plan struct {
	ID         int64
	UserID     int64
	Kind       string
	TargetType string
	TargetID   *int64
	Plan       string
	Digest     string
	Status     string
	CreatedAt  int64
	ExpiresAt  int64
	ExecutedAt *int64
}

// NewPlan describes a plan to store with CreatePlan.
type NewPlan struct {
	UserID     int64
	Kind       string // e.g. "prune", "restore", "gc"
	TargetType string // optional, e.g. "repo"
	TargetID   *int64
	Plan       any // marshalled to canonical JSON
	TTL        time.Duration
}

// PlanDigest returns the canonical JSON of plan (compact, object keys
// sorted) and its SHA-256 hex digest.
func PlanDigest(plan any) (canonical []byte, digest string, err error) {
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, "", err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, "", err
	}
	if canonical, err = json.Marshal(v); err != nil {
		return nil, "", err
	}
	sum := sha256.Sum256(canonical)
	return canonical, hex.EncodeToString(sum[:]), nil
}

func planTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

const planCols = "id, user_id, kind, target_type, target_id, plan, plan_digest, status, created_at, expires_at, executed_at"

func scanPlan(sc interface{ Scan(...any) error }) (*Plan, string, error) {
	var p Plan
	var tid, ex sql.NullInt64
	var hash string
	err := sc.Scan(&p.ID, &p.UserID, &p.Kind, &p.TargetType, &tid, &p.Plan, &p.Digest, &p.Status, &p.CreatedAt, &p.ExpiresAt, &ex, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	p.TargetID, p.ExecutedAt = nullInt(tid), nullInt(ex)
	return &p, hash, nil
}

// CreatePlan stores a plan and returns its one-time token. The token is
// returned only here and never stored.
func (s *Store) CreatePlan(ctx context.Context, np NewPlan) (string, *Plan, error) {
	if np.UserID <= 0 || np.Kind == "" {
		return "", nil, errors.New("plan user and kind required")
	}
	canonical, digest, err := PlanDigest(np.Plan)
	if err != nil {
		return "", nil, err
	}
	if np.TTL <= 0 {
		np.TTL = DefaultPlanTTL
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	t := now()
	p := &Plan{UserID: np.UserID, Kind: np.Kind, TargetType: np.TargetType, TargetID: np.TargetID,
		Plan: string(canonical), Digest: digest, Status: "open", CreatedAt: t, ExpiresAt: t + np.TTL.Milliseconds()}
	res, err := s.DB.ExecContext(ctx, `INSERT INTO plans(user_id, kind, target_type, target_id, plan, plan_digest, token_hash, status, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, p.UserID, p.Kind, p.TargetType, p.TargetID, p.Plan, p.Digest, planTokenHash(token), p.Status, p.CreatedAt, p.ExpiresAt)
	if err != nil {
		return "", nil, err
	}
	p.ID, err = res.LastInsertId()
	return token, p, err
}

// ExecutePlan validates and consumes a plan token for userID and kind.
//
// current, if not nil, recomputes the plan from current state (it is called
// outside any transaction and may query the store); its digest must equal
// the stored digest, otherwise the plan is marked stale. On success the plan
// is marked executed, so a token works once. Every rejection returns
// ErrPlanStale, except an error from current, which is returned as is.
func (s *Store) ExecutePlan(ctx context.Context, userID int64, kind, token string, current func(*Plan) (any, error)) (*Plan, error) {
	if token == "" || len(token) > 256 {
		return nil, ErrPlanStale
	}
	hash := planTokenHash(token)
	p, stored, err := scanPlan(s.DB.QueryRowContext(ctx, "SELECT "+planCols+", token_hash FROM plans WHERE token_hash = ?", hash))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrPlanStale
	}
	if err != nil {
		return nil, err
	}
	if subtle.ConstantTimeCompare([]byte(stored), []byte(hash)) != 1 || p.UserID != userID || p.Kind != kind || p.Status != "open" {
		return nil, ErrPlanStale
	}
	if p.ExpiresAt <= now() {
		s.markPlan(ctx, p.ID, "expired")
		return nil, ErrPlanStale
	}
	var v any
	if err := json.Unmarshal([]byte(p.Plan), &v); err != nil {
		return nil, err
	}
	if _, d, err := PlanDigest(v); err != nil || d != p.Digest {
		s.markPlan(ctx, p.ID, "stale")
		return nil, ErrPlanStale
	}
	if current != nil {
		cur, err := current(p)
		if err != nil {
			return nil, err
		}
		_, d, err := PlanDigest(cur)
		if err != nil {
			return nil, err
		}
		if subtle.ConstantTimeCompare([]byte(d), []byte(p.Digest)) != 1 {
			s.markPlan(ctx, p.ID, "stale")
			return nil, ErrPlanStale
		}
	}
	t := now()
	res, err := s.DB.ExecContext(ctx, "UPDATE plans SET status = 'executed', executed_at = ? WHERE id = ? AND status = 'open' AND expires_at > ?", t, p.ID, t)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, ErrPlanStale
	}
	p.Status, p.ExecutedAt = "executed", &t
	return p, nil
}

func (s *Store) markPlan(ctx context.Context, id int64, status string) {
	_, _ = s.DB.ExecContext(ctx, "UPDATE plans SET status = ? WHERE id = ? AND status = 'open'", status, id)
}

// InvalidatePlans marks every open plan of kind for a target stale, for
// example after the target changed in a way the plan did not cover.
func (s *Store) InvalidatePlans(ctx context.Context, kind, targetType string, targetID int64) (int64, error) {
	res, err := s.DB.ExecContext(ctx, "UPDATE plans SET status = 'stale' WHERE status = 'open' AND kind = ? AND target_type = ? AND target_id = ?", kind, targetType, targetID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ExpirePlans marks open plans past their expiry as expired and deletes
// plans that ended more than retain ago. It returns the number expired.
func (s *Store) ExpirePlans(ctx context.Context, retain time.Duration) (int64, error) {
	t := now()
	res, err := s.DB.ExecContext(ctx, "UPDATE plans SET status = 'expired' WHERE status = 'open' AND expires_at <= ?", t)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	if _, err := s.DB.ExecContext(ctx, "DELETE FROM plans WHERE status <> 'open' AND COALESCE(executed_at, expires_at) <= ?", t-retain.Milliseconds()); err != nil {
		return n, err
	}
	return n, nil
}
