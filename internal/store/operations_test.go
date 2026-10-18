package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func newUser(t *testing.T, st *store.Store, name string) *store.User {
	t.Helper()
	u, err := st.CreateUser(context.Background(), store.NewUser{Username: name, PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestOperationLifecycle(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")

	op := &store.Operation{Kind: wire.OperationKindVerify, UserID: &u.ID, Params: `{"repo_id":1}`}
	if err := st.CreateOperation(ctx, op); err != nil {
		t.Fatal(err)
	}
	if op.ID == 0 || op.State != wire.OperationStateQueued {
		t.Fatalf("created: %+v", op)
	}
	if err := st.StartOperation(ctx, op.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateOperationProgress(ctx, op.ID, 3, 10, 4096); err != nil {
		t.Fatal(err)
	}
	got, err := st.Operation(ctx, op.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != wire.OperationStateRunning || got.Done != 3 || got.Total != 10 || got.Bytes != 4096 || got.Result != "{}" || got.Finished() {
		t.Fatalf("running: %+v", got)
	}
	if err := st.FinishOperation(ctx, op.ID, wire.OperationStateRunning, "", ""); err == nil {
		t.Fatal("non-final finish state accepted")
	}
	if err := st.FinishOperation(ctx, op.ID, wire.OperationStateSucceeded, `{"ok":true}`, ""); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Operation(ctx, op.ID)
	if got.State != wire.OperationStateSucceeded || got.Result != `{"ok":true}` || got.FinishedAt == nil || !got.Finished() {
		t.Fatalf("finished: %+v", got)
	}
	// A finished operation cannot change any more.
	for name, err := range map[string]error{
		"progress": st.UpdateOperationProgress(ctx, op.ID, 1, 1, 1),
		"finish":   st.FinishOperation(ctx, op.ID, wire.OperationStateFailed, "", "x"),
		"start":    st.StartOperation(ctx, op.ID),
	} {
		if !errors.Is(err, store.ErrOperationFinished) {
			t.Fatalf("%s after finish: %v", name, err)
		}
	}
	if err := st.StartOperation(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := st.Operation(ctx, 9999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown get: %v", err)
	}
	if err := st.CreateOperation(ctx, &store.Operation{}); err == nil {
		t.Fatal("operation without kind accepted")
	}
}

func TestListAndInterruptedOperations(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	a, b := newUser(t, st, "alice"), newUser(t, st, "bob")
	mk := func(uid *int64, kind string) int64 {
		op := &store.Operation{Kind: kind, UserID: uid}
		if err := st.CreateOperation(ctx, op); err != nil {
			t.Fatal(err)
		}
		return op.ID
	}
	a1 := mk(&a.ID, wire.OperationKindPrune)
	a2 := mk(&a.ID, wire.OperationKindVerify)
	b1 := mk(&b.ID, wire.OperationKindPrune)
	sys := mk(nil, wire.OperationKindGC)
	if err := st.StartOperation(ctx, a2); err != nil {
		t.Fatal(err)
	}
	if err := st.FinishOperation(ctx, b1, wire.OperationStateSucceeded, "", ""); err != nil {
		t.Fatal(err)
	}

	ids := func(f store.OperationFilter) []int64 {
		ops, err := st.ListOperations(ctx, f)
		if err != nil {
			t.Fatal(err)
		}
		out := []int64{}
		for _, o := range ops {
			out = append(out, o.ID)
		}
		return out
	}
	tests := []struct {
		name string
		f    store.OperationFilter
		want []int64
	}{
		{"all", store.OperationFilter{}, []int64{sys, b1, a2, a1}},
		{"user", store.OperationFilter{UserID: &a.ID}, []int64{a2, a1}},
		{"system", store.OperationFilter{SystemOnly: true}, []int64{sys}},
		{"kind", store.OperationFilter{Kind: wire.OperationKindPrune}, []int64{b1, a1}},
		{"state", store.OperationFilter{State: wire.OperationStateRunning}, []int64{a2}},
		{"page", store.OperationFilter{BeforeID: a2}, []int64{a1}},
		{"limit", store.OperationFilter{Limit: 1}, []int64{sys}},
	}
	for _, tc := range tests {
		if got := ids(tc.f); len(got) != len(tc.want) || (len(got) > 0 && (got[0] != tc.want[0] || got[len(got)-1] != tc.want[len(tc.want)-1])) {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}

	n, err := st.FailInterruptedOperations(ctx)
	if err != nil || n != 3 {
		t.Fatalf("interrupted: n=%d err=%v", n, err)
	}
	for _, id := range []int64{a1, a2, sys} {
		o, _ := st.Operation(ctx, id)
		if o.State != wire.OperationStateFailed || o.Error == "" || o.FinishedAt == nil {
			t.Fatalf("op %d after restart: %+v", id, o)
		}
	}
	if o, _ := st.Operation(ctx, b1); o.State != wire.OperationStateSucceeded {
		t.Fatalf("finished op changed: %+v", o)
	}
}
