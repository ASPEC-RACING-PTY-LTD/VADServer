package api_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Test-only approval kinds, registered once for the package.
const (
	testKindResume = "test_resume"
	testKindStale  = "test_stale"
)

var resumed atomic.Int64

func init() {
	api.RegisterApprovalHandler(testKindResume, api.ApprovalHandler{
		OpKind: wire.OperationKindGC,
		Resume: func(ctx context.Context, s *api.Server, a *store.Approval, p *api.OpProgress) (any, error) {
			resumed.Store(a.ID)
			p.Set(1, 1, 0)
			return map[string]any{"approval_id": a.ID, "data": string(a.Data)}, nil
		},
	})
	api.RegisterApprovalHandler(testKindStale, api.ApprovalHandler{
		Digest: func(context.Context, *api.Server, *store.Approval) (string, error) { return "changed", nil },
		Resume: func(context.Context, *api.Server, *store.Approval, *api.OpProgress) (any, error) {
			panic("stale approval resumed")
		},
	})
}

func TestApprovalGatedDeviceTask(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	// A destructive kind is gated: 202 {task_id, approval_id}.
	out := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindRestore, "payload": map[string]any{"restore_id": 3}}, 202)
	taskID, apprID := int64(out["task_id"].(float64)), int64(out["approval_id"].(float64))
	if items, err := dc.PollTasks(ctx, 0); err != nil || len(items) != 0 {
		t.Fatalf("gated task delivered: %v %v", items, err)
	}
	list := alice.must("GET", "/api/v1/device/tasks/list?status=pending", nil, 200)["items"].([]any)
	if it := list[0].(map[string]any); len(list) != 1 || it["approval_id"] != float64(apprID) || it["status"] != "pending" {
		t.Fatalf("pending list: %v", list)
	}

	// The inbox: alice sees it, bob does not; the summary counts it.
	path := fmt.Sprintf("/api/v1/approvals/%d", apprID)
	a := alice.must("GET", path, nil, 200)
	if a["kind"] != wire.ApprovalKindDeviceTask || a["subject_type"] != wire.ApprovalSubjectDeviceTask || a["subject_id"] != float64(taskID) ||
		a["state"] != "pending" || a["risk"] != "high" || a["plan_digest"] == "" || a["decided_by"] != nil {
		t.Fatalf("approval: %v", a)
	}
	if l := alice.must("GET", "/api/v1/approvals?state=pending", nil, 200)["items"].([]any); len(l) != 1 {
		t.Fatalf("inbox: %v", l)
	}
	if out := bob.must("GET", path, nil, 404); errCode(out) != "not_found" {
		t.Fatalf("bob get: %v", out)
	}
	if l := bob.must("GET", "/api/v1/approvals", nil, 200)["items"].([]any); len(l) != 0 {
		t.Fatalf("bob inbox: %v", l)
	}
	if s := alice.must("GET", "/api/v1/summary", nil, 200); s["pending_approvals"] != 1.0 {
		t.Fatalf("summary: %v", s)
	}
	// Devices never decide.
	if st, code := apiCode(t, dc.Call(ctx, "POST", path+"/approve", map[string]any{}, nil)); st != 403 || code != "session_required" {
		t.Fatalf("device approve: %d %s", st, code)
	}
	// A non-global approval is the owner's call, even for an admin.
	if out := admin.must("POST", path+"/approve", map[string]any{"confirm_text": "TEST-PC"}, 404); errCode(out) != "not_found" {
		t.Fatalf("admin approve: %v", out)
	}
	// High risk needs the subject name.
	if out := alice.must("POST", path+"/approve", map[string]any{"confirm_text": "wrong"}, 400); errCode(out) != "validation" {
		t.Fatalf("confirm: %v", out)
	}

	// Waking: a long-poll in flight gets the task once approved.
	ch := make(chan []wire.DeviceTask, 1)
	go func() {
		items, _ := dc.PollTasks(ctx, 20*time.Second)
		ch <- items
	}()
	time.Sleep(200 * time.Millisecond)
	dec := alice.must("POST", path+"/approve", map[string]any{"confirm_text": "TEST-PC"}, 200)
	if dec["state"] != wire.ApprovalStateConsumed || dec["operation_id"] != nil || dec["decided_by"] == nil {
		t.Fatalf("approved: %v", dec)
	}
	select {
	case items := <-ch:
		if len(items) != 1 || items[0].ID != taskID || items[0].ApprovalID == nil || *items[0].ApprovalID != apprID {
			t.Fatalf("delivered: %+v", items)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("approved task never delivered")
	}
	if out := alice.must("POST", path+"/approve", map[string]any{"confirm_text": "TEST-PC"}, 409); errCode(out) != "approval_not_pending" {
		t.Fatalf("second approve: %v", out)
	}

	// Rejecting cancels the waiting task.
	out = alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindPruneCompact, "payload": map[string]any{"repo_id": 1, "prune_plan_id": 2}}, 202)
	t2, a2 := int64(out["task_id"].(float64)), int64(out["approval_id"].(float64))
	rej := alice.must("POST", fmt.Sprintf("/api/v1/approvals/%d/reject", a2), map[string]any{"reason": "not today"}, 200)
	if rej["state"] != wire.ApprovalStateRejected || rej["reason"] != "not today" {
		t.Fatalf("rejected: %v", rej)
	}
	if tk, _ := e.st.Task(ctx, t2); tk.Status != wire.TaskStatusCancelled {
		t.Fatalf("task after reject: %+v", tk)
	}
	if out := alice.must("GET", "/api/v1/approvals?state=bogus", nil, 400); errCode(out) != "validation" {
		t.Fatalf("bad state filter: %v", out)
	}
}

func TestApprovalResumeThroughOperation(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	ctx := context.Background()
	defer e.srv.StopOperations(ctx)
	u, _ := e.st.UserByLogin(ctx, "alice")
	adm, _ := e.st.UserByLogin(ctx, "admin")

	// approve → resume through the operation runner → consumed.
	acc, err := e.srv.RequireApproval(ctx, api.ApprovalRequest{UserID: u.ID, Kind: testKindResume, SubjectType: "repo", SubjectID: 4,
		Summary: "Do the thing", Plan: map[string]any{"n": 1}, Data: map[string]any{"x": 1}})
	if err != nil || acc.ApprovalID == 0 || acc.OperationID != nil {
		t.Fatalf("require: %+v %v", acc, err)
	}
	path := fmt.Sprintf("/api/v1/approvals/%d", acc.ApprovalID)
	dec := alice.must("POST", path+"/approve", nil, 200)
	opID, ok := dec["operation_id"].(float64)
	if !ok {
		t.Fatalf("no operation: %v", dec)
	}
	op := waitOperation(t, alice, int64(opID), wire.OperationStateSucceeded)
	if res := op["result"].(map[string]any); res["approval_id"] != float64(acc.ApprovalID) || res["data"] != `{"x":1}` || resumed.Load() != acc.ApprovalID {
		t.Fatalf("operation: %v", op)
	}
	deadline := time.Now().Add(5 * time.Second)
	for alice.must("GET", path, nil, 200)["state"] != wire.ApprovalStateConsumed {
		if time.Now().After(deadline) {
			t.Fatal("approval never consumed")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A changed plan digest expires the approval: 409 plan_stale.
	acc, err = e.srv.RequireApproval(ctx, api.ApprovalRequest{UserID: u.ID, Kind: testKindStale, PlanDigest: "original", Summary: "stale"})
	if err != nil {
		t.Fatal(err)
	}
	path = fmt.Sprintf("/api/v1/approvals/%d", acc.ApprovalID)
	if out := alice.must("POST", path+"/approve", nil, 409); errCode(out) != wire.CodePlanStale {
		t.Fatalf("stale: %v", out)
	}
	if a := alice.must("GET", path, nil, 200); a["state"] != wire.ApprovalStateExpired {
		t.Fatalf("stale state: %v", a)
	}

	// Global approvals: any admin decides, users cannot see them.
	acc, err = e.srv.RequireApproval(ctx, api.ApprovalRequest{UserID: adm.ID, Kind: wire.ApprovalKindGC, Global: true, Summary: "GC"})
	if err != nil {
		t.Fatal(err)
	}
	path = fmt.Sprintf("/api/v1/approvals/%d", acc.ApprovalID)
	alice.must("GET", path, nil, 404)
	if d := admin.must("POST", path+"/approve", nil, 200); d["state"] != wire.ApprovalStateApproved {
		t.Fatalf("global approve (no handler stays approved): %v", d)
	}

	// Superuser approvals: the owner who is not a superuser cannot decide.
	acc, err = e.srv.RequireApproval(ctx, api.ApprovalRequest{UserID: u.ID, Kind: wire.ApprovalKindTwoWayEnable, Superuser: true, Summary: "escrow"})
	if err != nil {
		t.Fatal(err)
	}
	path = fmt.Sprintf("/api/v1/approvals/%d", acc.ApprovalID)
	if out := alice.must("POST", path+"/approve", nil, 403); errCode(out) != wire.CodeNotSuperuser {
		t.Fatalf("superuser gate: %v", out)
	}
	if out := alice.must("POST", path+"/reject", map[string]any{"reason": "x"}, 403); errCode(out) != wire.CodeNotSuperuser {
		t.Fatalf("superuser reject gate: %v", out)
	}
	if d := admin.must("POST", path+"/approve", nil, 200); d["state"] != wire.ApprovalStateApproved || d["decided_by"] != float64(adm.ID) {
		t.Fatalf("superuser approve: %v", d)
	}

	// Invalid requests are refused before anything is stored.
	if _, err := e.srv.RequireApproval(ctx, api.ApprovalRequest{UserID: u.ID, Kind: testKindResume, Risk: wire.ApprovalRiskHigh}); err == nil {
		t.Fatal("high risk without subject name accepted")
	}
}
