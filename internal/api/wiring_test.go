package api_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// adminUserByName finds a user in GET /admin/users.
func adminUserByName(t *testing.T, admin *web, name string) map[string]any {
	t.Helper()
	for _, it := range admin.must("GET", "/api/v1/admin/users", nil, 200)["users"].([]any) {
		if u := it.(map[string]any); u["username"] == name {
			return u
		}
	}
	t.Fatalf("user %s not listed", name)
	return nil
}

func TestAdminSuperuserToggle(t *testing.T) {
	e := newEnv(t, "")
	root := e.setupAdmin() // first admin: superuser
	root.must("POST", "/api/v1/admin/users", map[string]any{"username": "admin2", "password": "password-admin2", "role": "admin"}, 201)
	admin2 := e.web()
	admin2.must("POST", "/api/v1/auth/login", map[string]any{"login": "admin2", "password": "password-admin2"}, 200)
	e.createUser(root, "alice", 0)

	if u := adminUserByName(t, root, "admin"); u["is_superuser"] != true {
		t.Fatalf("root listed: %v", u)
	}
	a2 := adminUserByName(t, root, "admin2")
	if a2["is_superuser"] != false {
		t.Fatalf("admin2 listed: %v", a2)
	}
	a2Path := fmt.Sprintf("/api/v1/admin/users/%d", int64(a2["id"].(float64)))
	rootPath := fmt.Sprintf("/api/v1/admin/users/%d", int64(adminUserByName(t, root, "admin")["id"].(float64)))
	alicePath := fmt.Sprintf("/api/v1/admin/users/%d", int64(adminUserByName(t, root, "alice")["id"].(float64)))

	// A plain admin cannot toggle, and nothing else in the request applies.
	if code, out := admin2.req("PATCH", a2Path, map[string]any{"is_superuser": true, "display_name": "x"}); code != 403 || errCode(out) != wire.CodeNotSuperuser {
		t.Fatalf("admin2 self-grant: %d %v", code, out)
	}
	if u := adminUserByName(t, root, "admin2"); u["display_name"] == "x" {
		t.Fatalf("partial update applied: %v", u)
	}
	// Superuser needs an active administrator.
	if code, out := root.req("PATCH", alicePath, map[string]any{"is_superuser": true}); code != 400 || errCode(out) != "not_admin" {
		t.Fatalf("grant to user: %d %v", code, out)
	}
	if u := root.must("PATCH", a2Path, map[string]any{"is_superuser": true}, 200)["user"].(map[string]any); u["is_superuser"] != true {
		t.Fatalf("grant reply: %v", u)
	}
	// admin2 (now superuser) revokes root; admin2 is then the last one.
	admin2.must("PATCH", rootPath, map[string]any{"is_superuser": false}, 200)
	if u := adminUserByName(t, root, "admin"); u["is_superuser"] != false {
		t.Fatalf("revoked root listed: %v", u)
	}
	for name, body := range map[string]map[string]any{
		"disable": {"disabled": true},
		"demote":  {"role": "user"},
	} {
		if code, out := root.req("PATCH", a2Path, body); code != 409 || errCode(out) != "last_superuser" {
			t.Fatalf("%s last superuser: %d %v", name, code, out)
		}
	}
	if code, out := root.req("DELETE", a2Path, map[string]any{"confirm_username": "admin2"}); code != 409 || errCode(out) != "last_superuser" {
		t.Fatalf("delete last superuser: %d %v", code, out)
	}
	if code, out := admin2.req("PATCH", a2Path, map[string]any{"is_superuser": false}); code != 409 || errCode(out) != "last_superuser" {
		t.Fatalf("revoke last superuser: %d %v", code, out)
	}
}

func TestDeviceListModulesAndCanDelete(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	first := func(w *web, path string) map[string]any {
		t.Helper()
		list := w.must("GET", path, nil, 200)["devices"].([]any)
		if len(list) != 1 {
			t.Fatalf("%s: %v", path, list)
		}
		return list[0].(map[string]any)
	}
	if d := first(admin, "/api/v1/admin/devices"); d["can_delete"] != true {
		t.Fatalf("admin devices: %v", d)
	}
	d := first(alice, "/api/v1/devices")
	sum, _ := d["modules_summary"].(map[string]any)
	if d["install_mode"] != wire.ModuleInstallModeSimple || sum == nil || len(sum["enabled"].([]any)) != 0 || sum["updated_at"] != 0.0 {
		t.Fatalf("unreported: %v", d)
	}
	report := wire.ModuleReport{InstallMode: wire.ModuleInstallModeAdvanced, CoreVersion: "1.0.0", Modules: []wire.ModuleStatus{
		{ID: wire.ModuleCore, Version: "1.0.0", State: wire.ModuleStateEnabled},
		{ID: wire.ModuleVSS, State: wire.ModuleStateFailed, Error: "boom"},
	}}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/modules", report, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/devices", "/api/v1/admin/devices"} {
		w := alice
		if path == "/api/v1/admin/devices" {
			w = admin
		}
		d = first(w, path)
		sum = d["modules_summary"].(map[string]any)
		if d["install_mode"] != wire.ModuleInstallModeAdvanced || fmt.Sprint(sum["enabled"]) != "[core]" || fmt.Sprint(sum["failed"]) != "[vss]" || sum["updated_at"] == 0.0 {
			t.Fatalf("%s reported: %v", path, d)
		}
	}
}

func TestProfileConsentTaskWiring(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	reqPath := fmt.Sprintf("/api/v1/devices/%d/profiles/requests", dc.DeviceID)

	// Without the profiles module the request is refused before any state.
	if code, out := admin.req("POST", reqPath, map[string]any{"all": true}); code != 409 || errCode(out) != "module_unavailable" {
		t.Fatalf("no module: %d %v", code, out)
	}
	report := wire.ModuleReport{InstallMode: wire.ModuleInstallModeAdvanced, CoreVersion: "1.0.0", Modules: []wire.ModuleStatus{
		{ID: wire.ModuleCore, Version: "1.0.0", State: wire.ModuleStateEnabled},
		{ID: wire.ModuleProfiles, Version: "1.0.0", State: wire.ModuleStateEnabled},
	}}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/modules", report, nil); err != nil {
		t.Fatal(err)
	}
	out := admin.must("POST", reqPath, map[string]any{"all": true}, 201)
	taskID := int64(out["task_id"].(float64))
	tk, err := e.st.Task(ctx, taskID)
	if err != nil || tk.Kind != wire.TaskKindProfileConsentRequest || fmt.Sprint(tk.RequiredModules) != "[profiles]" || tk.UserID == 0 || tk.CreatedBy == nil {
		t.Fatalf("task: %+v %v", tk, err)
	}
	items, err := dc.PollTasks(ctx, 0)
	if err != nil || len(items) != 1 || items[0].ID != taskID {
		t.Fatalf("delivered: %v %v", items, err)
	}
}

func TestMaintenanceSweeps(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	// Lease expiry: a claimed task whose lease ended goes back to pending.
	created := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindVerify, "payload": map[string]any{"repo_id": 1, "depth": "full"}}, 201)
	taskID := int64(created["task_id"].(float64))
	if items, err := dc.PollTasks(ctx, 0); err != nil || len(items) != 1 {
		t.Fatalf("claim: %v %v", items, err)
	}
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE device_tasks SET expires_at = 1 WHERE id = ?`, taskID); err != nil {
		t.Fatal(err)
	}
	e.srv.ExpireTaskLeases(ctx)
	if tk, _ := e.st.Task(ctx, taskID); tk == nil || tk.Status != wire.TaskStatusPending {
		t.Fatalf("after lease sweep: %+v", tk)
	}

	// Approval expiry: an overdue approval expires and its task is cancelled.
	out := alice.must("POST", "/api/v1/device/tasks", map[string]any{"device_id": dc.DeviceID, "kind": wire.TaskKindRestore, "payload": map[string]any{"restore_id": 3}}, 202)
	apprID := int64(out["approval_id"].(float64))
	if _, err := e.st.DB.ExecContext(ctx, `UPDATE approvals SET expires_at = 1 WHERE id = ?`, apprID); err != nil {
		t.Fatal(err)
	}
	e.srv.ExpireApprovals(ctx)
	if a, err := e.st.Approval(ctx, apprID); err != nil || a.State != wire.ApprovalStateExpired {
		t.Fatalf("after approval sweep: %+v %v", a, err)
	}
}
