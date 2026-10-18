package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type testPlan struct {
	Delete []int64 `json:"delete"`
	Bytes  int64   `json:"bytes"`
}

func TestPlanDigestCanonical(t *testing.T) {
	c1, d1, err := store.PlanDigest(map[string]any{"b": 1, "a": []int{1, 2}})
	if err != nil {
		t.Fatal(err)
	}
	_, d2, _ := store.PlanDigest(map[string]any{"a": []int{1, 2}, "b": 1})
	_, d3, _ := store.PlanDigest([]byte(`{"b":1,"a":[1,2]}`)) // bytes marshal as a string: differs
	if string(c1) != `{"a":[1,2],"b":1}` || d1 != d2 || d1 == d3 || len(d1) != 64 {
		t.Fatalf("canonical %s d1=%s d2=%s d3=%s", c1, d1, d2, d3)
	}
	// Large integers survive canonicalisation exactly.
	c, _, _ := store.PlanDigest(testPlan{Bytes: 1<<62 + 1})
	if string(c) != `{"bytes":4611686018427387905,"delete":null}` {
		t.Fatalf("large int: %s", c)
	}
}

func TestPlanExecute(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	alice, bob := newUser(t, st, "alice"), newUser(t, st, "bob")
	repo := int64(7)
	plan := testPlan{Delete: []int64{1, 2}, Bytes: 100}
	create := func(ttl time.Duration) (string, *store.Plan) {
		t.Helper()
		tok, p, err := st.CreatePlan(ctx, store.NewPlan{UserID: alice.ID, Kind: "prune", TargetType: "repo", TargetID: &repo, Plan: plan, TTL: ttl})
		if err != nil {
			t.Fatal(err)
		}
		return tok, p
	}
	same := func(*store.Plan) (any, error) { return plan, nil }

	tok, p := create(0)
	if len(tok) < 40 || p.ExpiresAt-p.CreatedAt != store.DefaultPlanTTL.Milliseconds() || p.Status != "open" {
		t.Fatalf("created: tok=%q %+v", tok, p)
	}
	var stored int
	_ = st.DB.QueryRow("SELECT COUNT(*) FROM plans WHERE token_hash = ?", tok).Scan(&stored)
	if stored != 0 {
		t.Fatal("token stored in clear")
	}

	stale := []struct {
		name  string
		user  int64
		kind  string
		token string
	}{
		{"other user", bob.ID, "prune", tok},
		{"other kind", alice.ID, "gc", tok},
		{"unknown token", alice.ID, "prune", tok + "x"},
		{"empty token", alice.ID, "prune", ""},
	}
	for _, tc := range stale {
		if _, err := st.ExecutePlan(ctx, tc.user, tc.kind, tc.token, same); !errors.Is(err, store.ErrPlanStale) {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	got, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, func(sp *store.Plan) (any, error) {
		if sp.TargetID == nil || *sp.TargetID != repo {
			t.Errorf("target not passed: %+v", sp)
		}
		return plan, nil
	})
	if err != nil || got.Status != "executed" || got.ExecutedAt == nil {
		t.Fatalf("execute: %+v %v", got, err)
	}
	// Single use.
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, same); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("reuse: %v", err)
	}

	// Current state changed: stale, and stays stale.
	tok, _ = create(0)
	changed := func(*store.Plan) (any, error) { return testPlan{Delete: []int64{1}, Bytes: 50}, nil }
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, changed); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("changed: %v", err)
	}
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, same); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("after stale: %v", err)
	}

	// Errors from the recompute are returned as is and do not consume.
	tok, _ = create(0)
	boom := errors.New("boom")
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, func(*store.Plan) (any, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("recompute error: %v", err)
	}
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, nil); err != nil {
		t.Fatalf("nil current: %v", err)
	}

	// Invalidated by a target change.
	tok, _ = create(0)
	if n, err := st.InvalidatePlans(ctx, "prune", "repo", repo); err != nil || n != 1 {
		t.Fatalf("invalidate: %d %v", n, err)
	}
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, same); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("invalidated: %v", err)
	}

	// Expired.
	tok, p = create(time.Hour)
	if _, err := st.DB.Exec("UPDATE plans SET expires_at = ? WHERE id = ?", time.Now().UnixMilli()-1, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ExecutePlan(ctx, alice.ID, "prune", tok, same); !errors.Is(err, store.ErrPlanStale) {
		t.Fatalf("expired: %v", err)
	}

	// ExpirePlans marks open plans past expiry and prunes old ended ones.
	_, p = create(time.Hour)
	if _, err := st.DB.Exec("UPDATE plans SET expires_at = ? WHERE id = ?", time.Now().UnixMilli()-1, p.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := st.ExpirePlans(ctx, time.Hour); err != nil || n != 1 {
		t.Fatalf("expire: %d %v", n, err)
	}
	var ended int
	_ = st.DB.QueryRow("SELECT COUNT(*) FROM plans WHERE status IN ('executed','expired')").Scan(&ended)
	if n, _ := st.ExpirePlans(ctx, 0); n != 0 {
		t.Fatalf("second expire: %d", n)
	}
	var left int
	_ = st.DB.QueryRow("SELECT COUNT(*) FROM plans WHERE status IN ('executed','expired')").Scan(&left)
	if ended == 0 || left != 0 {
		t.Fatalf("prune: before=%d after=%d", ended, left)
	}

	if _, _, err := st.CreatePlan(ctx, store.NewPlan{Kind: "prune", Plan: plan}); err == nil {
		t.Fatal("plan without user accepted")
	}
}
