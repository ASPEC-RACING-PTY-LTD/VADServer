package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestDeviceTasksLongPollClaimResult(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	bc := e.enrollDevice(bob, "bob", "password-bob")
	ctx := context.Background()

	// The feature flag is advertised.
	info := admin.must("GET", "/api/v1/info", nil, 200)
	if fs := fmt.Sprint(info["features"]); !strings.Contains(fs, wire.FeatureDeviceTasks) || !strings.Contains(fs, wire.FeatureApprovals) {
		t.Fatalf("features: %v", info["features"])
	}

	// wait=0 returns at once with nothing.
	if items, err := dc.PollTasks(ctx, 0); err != nil || len(items) != 0 {
		t.Fatalf("empty poll: %v %v", items, err)
	}

	// A long-poll wakes up as soon as a task is created.
	type pollRes struct {
		items []wire.DeviceTask
		err   error
		took  time.Duration
	}
	ch := make(chan pollRes, 1)
	go func() {
		start := time.Now()
		items, err := dc.PollTasks(ctx, 20*time.Second)
		ch <- pollRes{items, err, time.Since(start)}
	}()
	time.Sleep(300 * time.Millisecond)
	created := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindBackupNow, "payload": map[string]any{"job_id": 5}}, 201)
	taskID := int64(created["task_id"].(float64))
	if _, ok := created["approval_id"]; ok {
		t.Fatalf("ungated task has approval: %v", created)
	}
	var res pollRes
	select {
	case res = <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal("long-poll did not wake up")
	}
	if res.err != nil || len(res.items) != 1 || res.took > 5*time.Second {
		t.Fatalf("poll: %+v", res)
	}
	got := res.items[0]
	var payload wire.TaskBackupNowPayload
	if got.ID != taskID || got.Status != wire.TaskStatusClaimed || got.CreatedBy != wire.TaskCreatedByUser || got.ClaimedAt == 0 || json.Unmarshal(got.Payload, &payload) != nil || payload.JobID != 5 {
		t.Fatalf("task: %+v", got)
	}

	// The claimed task is not returned again (no double claim).
	if items, err := dc.PollTasks(ctx, 0); err != nil || len(items) != 0 {
		t.Fatalf("double claim: %v %v", items, err)
	}

	// Only the claiming device can report; a result is accepted once.
	resultPath := fmt.Sprintf("/api/v1/device/tasks/%d/result", taskID)
	if st, code := apiCode(t, bc.TaskResult(ctx, taskID, wire.TaskStatusDone, nil)); st != 409 || code != wire.CodeTaskNotClaimable {
		t.Fatalf("foreign result: %d %s", st, code)
	}
	if st, _ := apiCode(t, dc.Call(ctx, "POST", resultPath, map[string]any{"status": "claimed", "result": map[string]any{}}, nil)); st != 400 {
		t.Fatalf("bad status: %d", st)
	}
	if st, _ := apiCode(t, dc.Call(ctx, "POST", resultPath, map[string]any{"status": "done", "result": []int{1}}, nil)); st != 400 {
		t.Fatalf("non-object result: %d", st)
	}
	if err := dc.TaskResult(ctx, taskID, wire.TaskStatusDone, json.RawMessage(`{"run_id":9}`)); err != nil {
		t.Fatal(err)
	}
	if st, code := apiCode(t, dc.TaskResult(ctx, taskID, wire.TaskStatusFailed, nil)); st != 409 || code != wire.CodeTaskNotClaimable {
		t.Fatalf("second result: %d %s", st, code)
	}

	// The session list shows the finished task; bob sees nothing.
	list := alice.must("GET", fmt.Sprintf("/api/v1/device/tasks/list?device=%d&status=done", dc.DeviceID), nil, 200)
	items := list["items"].([]any)
	if len(items) != 1 || list["next_cursor"] != "" {
		t.Fatalf("list: %v", list)
	}
	if it := items[0].(map[string]any); it["status"] != "done" || it["result"].(map[string]any)["run_id"] != 9.0 || it["finished_at"] == 0.0 {
		t.Fatalf("listed task: %v", it)
	}
	if l := bob.must("GET", "/api/v1/device/tasks/list", nil, 200); len(l["items"].([]any)) != 0 {
		t.Fatalf("bob list: %v", l)
	}
	if out := alice.must("GET", "/api/v1/device/tasks/list?status=nope", nil, 400); errCode(out) != "validation" {
		t.Fatalf("bad status filter: %v", out)
	}

	// Validation: unknown kind, bad payload, foreign device; devices cannot
	// use the session routes.
	for name, body := range map[string]map[string]any{
		"kind":    {"device_id": dc.DeviceID, "kind": "format_disk", "payload": map[string]any{}},
		"payload": {"device_id": dc.DeviceID, "kind": wire.TaskKindBackupNow, "payload": map[string]any{"job": 1}},
		"device":  {"device_id": bc.DeviceID, "kind": wire.TaskKindBackupNow, "payload": map[string]any{"job_id": 1}},
	} {
		if out := alice.must("POST", "/api/v1/device/tasks", body, 400); errCode(out) != "validation" {
			t.Fatalf("%s: %v", name, out)
		}
	}
	if st, code := apiCode(t, dc.Call(ctx, "GET", "/api/v1/device/tasks/list", nil, nil)); st != 403 || code != "session_required" {
		t.Fatalf("device list: %d %s", st, code)
	}
	// Module gate: automation_op needs the automation module.
	out := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindAutomationOp,
		"payload": map[string]any{"automation_id": 1, "node_id": "n1", "op": map[string]any{"kind": "x", "params": map[string]any{}}}}, 409)
	if errCode(out) != wire.CodeModuleUnavailable {
		t.Fatalf("module gate: %v", out)
	}
}

func TestDeviceTaskLeaseExpiryRequeues(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	created := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindVerify, "payload": map[string]any{"repo_id": 1, "depth": "full"}}, 201)
	taskID := int64(created["task_id"].(float64))
	items, err := dc.PollTasks(ctx, 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("first claim: %v %v", items, err)
	}
	// The device vanished without a result: end the lease.
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE device_tasks SET expires_at = 1 WHERE id = ?`, taskID); err != nil {
		t.Fatal(err)
	}
	items, err = dc.PollTasks(ctx, 0)
	if err != nil || len(items) != 1 || items[0].ID != taskID || items[0].Status != wire.TaskStatusClaimed {
		t.Fatalf("reclaim after lease expiry: %v %v", items, err)
	}
	tk, _ := e.st.Task(ctx, taskID)
	if tk.Attempts != 2 {
		t.Fatalf("attempts: %+v", tk)
	}
	if err := dc.TaskResult(ctx, taskID, wire.TaskStatusFailed, nil); err != nil {
		t.Fatal(err)
	}
}
