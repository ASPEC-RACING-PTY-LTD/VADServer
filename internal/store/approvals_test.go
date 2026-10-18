package store_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestApprovalLifecycle(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	admin := newUser(t, st, "root")

	a := &store.Approval{UserID: u.ID, Kind: wire.ApprovalKindPrune, SubjectType: wire.ApprovalSubjectPrunePlan, SubjectID: 7,
		Title: "Repo A", Summary: "Delete 3 snapshots of Repo A", Risk: wire.ApprovalRiskHigh, PlanDigest: "abc",
		RequesterRef: "alice", Data: json.RawMessage(`{"plan_id":7}`)}
	if err := st.CreateApproval(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := st.Approval(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != wire.ApprovalStatePending || got.PlanDigest != "abc" || got.Title != "Repo A" || got.RequestedBy != wire.ApprovalRequestedByUser ||
		got.SubjectID != 7 || string(got.Data) != `{"plan_id":7}` || got.ExpiresAt-got.CreatedAt != store.DefaultApprovalTTL.Milliseconds() {
		t.Fatalf("created: %+v", got)
	}

	// Consume needs approved first.
	if err := st.ConsumeApproval(ctx, a.ID); !errors.Is(err, store.ErrApprovalNotPending) {
		t.Fatalf("consume pending: %v", err)
	}
	d, err := st.DecideApproval(ctx, a.ID, u.ID, true, "")
	if err != nil {
		t.Fatal(err)
	}
	if d.State != wire.ApprovalStateApproved || d.DecidedBy == nil || *d.DecidedBy != u.ID || d.DecidedAt == nil {
		t.Fatalf("approved: %+v", d)
	}
	if _, err := st.DecideApproval(ctx, a.ID, u.ID, false, "late"); !errors.Is(err, store.ErrApprovalNotPending) {
		t.Fatalf("second decision: %v", err)
	}
	if err := st.SetApprovalOperation(ctx, a.ID, 42); err != nil {
		t.Fatal(err)
	}
	if err := st.ConsumeApproval(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Approval(ctx, a.ID)
	if got.State != wire.ApprovalStateConsumed || got.OperationID == nil || *got.OperationID != 42 {
		t.Fatalf("consumed: %+v", got)
	}

	// Global approvals are listed for admins only when asked; state filter.
	g := &store.Approval{UserID: admin.ID, Kind: wire.ApprovalKindGC, Global: true, Summary: "GC"}
	if err := st.CreateApproval(ctx, g); err != nil {
		t.Fatal(err)
	}
	other := newUser(t, st, "carol")
	if l, _ := st.ListApprovals(ctx, store.ApprovalFilter{UserID: other.ID}); len(l) != 0 {
		t.Fatalf("carol sees %+v", l)
	}
	if l, _ := st.ListApprovals(ctx, store.ApprovalFilter{UserID: other.ID, IncludeGlobal: true}); len(l) != 1 || !l[0].Global {
		t.Fatalf("global list: %+v", l)
	}
	if l, _ := st.ListApprovals(ctx, store.ApprovalFilter{UserID: u.ID, State: wire.ApprovalStateConsumed}); len(l) != 1 || l[0].ID != a.ID {
		t.Fatalf("consumed filter: %+v", l)
	}
	if l, _ := st.ListApprovals(ctx, store.ApprovalFilter{UserID: u.ID, State: wire.ApprovalStatePending}); len(l) != 0 {
		t.Fatalf("pending filter: %+v", l)
	}
}

func TestApprovalRejectAndExpiryCancelTasks(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := newTaskDevice(t, st, u, "pc1")

	gate := func() (*store.Approval, *store.DeviceTask) {
		tk := &store.DeviceTask{UserID: u.ID, DeviceID: d.ID, Kind: wire.TaskKindRestore, Payload: `{"restore_id":1}`, Status: store.TaskStatusAwaitingApproval}
		if err := st.CreateTask(ctx, tk); err != nil {
			t.Fatal(err)
		}
		a := &store.Approval{UserID: u.ID, Kind: wire.ApprovalKindDeviceTask, SubjectType: wire.ApprovalSubjectDeviceTask, SubjectID: tk.ID}
		if err := st.CreateApproval(ctx, a); err != nil {
			t.Fatal(err)
		}
		if err := st.SetTaskApproval(ctx, tk.ID, a.ID); err != nil {
			t.Fatal(err)
		}
		return a, tk
	}

	// Reject cancels the waiting task and keeps the reason.
	a, tk := gate()
	r, err := st.DecideApproval(ctx, a.ID, u.ID, false, "not now")
	if err != nil {
		t.Fatal(err)
	}
	if r.State != wire.ApprovalStateRejected || r.Reason != "not now" {
		t.Fatalf("rejected: %+v", r)
	}
	if got, _ := st.Task(ctx, tk.ID); got.Status != wire.TaskStatusCancelled {
		t.Fatalf("task after reject: %+v", got)
	}

	// Expiry (bulk) cancels too, and an expired approval cannot be decided.
	a2, tk2 := gate()
	if _, err := st.DB.ExecContext(ctx, `UPDATE approvals SET expires_at = 1 WHERE id = ?`, a2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DecideApproval(ctx, a2.ID, u.ID, true, ""); !errors.Is(err, store.ErrApprovalNotPending) {
		t.Fatalf("decide expired: %v", err)
	}
	ids, err := st.ExpireApprovals(ctx)
	if err != nil || len(ids) != 1 || ids[0] != a2.ID {
		t.Fatalf("expire: %v %v", ids, err)
	}
	if got, _ := st.Approval(ctx, a2.ID); got.State != wire.ApprovalStateExpired {
		t.Fatalf("expired: %+v", got)
	}
	if got, _ := st.Task(ctx, tk2.ID); got.Status != wire.TaskStatusCancelled {
		t.Fatalf("task after expiry: %+v", got)
	}

	// Reading an overdue approval expires it lazily; ExpireApproval on a
	// non-pending one fails.
	a3, tk3 := gate()
	if _, err := st.DB.ExecContext(ctx, `UPDATE approvals SET expires_at = 1 WHERE id = ?`, a3.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := st.Approval(ctx, a3.ID); got.State != wire.ApprovalStateExpired {
		t.Fatalf("lazy expiry: %+v", got)
	}
	if got, _ := st.Task(ctx, tk3.ID); got.Status != wire.TaskStatusCancelled {
		t.Fatalf("task after lazy expiry: %+v", got)
	}
	if err := st.ExpireApproval(ctx, a3.ID); !errors.Is(err, store.ErrApprovalNotPending) {
		t.Fatalf("re-expire: %v", err)
	}
}
