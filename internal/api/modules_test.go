package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestModulesEndpoints(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	path := fmt.Sprintf("/api/v1/devices/%d/modules", dc.DeviceID)

	// Feature flag advertised.
	found := false
	for _, f := range admin.must("GET", "/api/v1/info", nil, 200)["features"].([]any) {
		found = found || f == wire.FeatureModules
	}
	if !found {
		t.Fatal("modules.v1 not advertised")
	}

	// Before any report: simple, no modules, updated_at 0.
	out := alice.must("GET", path, nil, 200)
	if out["install_mode"] != "simple" || len(out["modules"].([]any)) != 0 || out["updated_at"] != 0.0 {
		t.Fatalf("unreported: %v", out)
	}

	report := wire.ModuleReport{InstallMode: "advanced", CoreVersion: "1.2.0", Modules: []wire.ModuleStatus{
		{ID: "core", Version: "1.2.0", State: "enabled"},
		{ID: "sync", Version: "1.2.0", State: "enabled"},
		{ID: "vss", State: "failed", Error: "download failed"},
		{ID: "future_module", Version: "9", State: "enabled"}, // unknown ids are dropped
	}}
	if err := dc.Call(ctx, "PUT", "/api/v1/device/modules", report, nil); err != nil {
		t.Fatal(err)
	}
	var st wire.ModuleDeviceState
	raw, _ := json.Marshal(alice.must("GET", path, nil, 200))
	_ = json.Unmarshal(raw, &st)
	if st.InstallMode != "advanced" || st.CoreVersion != "1.2.0" || len(st.Modules) != 3 || st.Modules[2].Error != "download failed" || st.UpdatedAt == 0 {
		t.Fatalf("reported: %+v", st)
	}
	// Admins may view; other users get 404; devices are refused.
	admin.must("GET", path, nil, 200)
	if code, _ := bob.req("GET", path, nil); code != 404 {
		t.Fatalf("other user: %d", code)
	}
	if s, code := apiCode(t, dc.Call(ctx, "GET", path, nil, nil)); s != 403 || code != "session_required" {
		t.Fatalf("device on sess route: %d %s", s, code)
	}

	// Validation.
	for name, body := range map[string]any{
		"mode":      map[string]any{"install_mode": "expert", "core_version": "1", "modules": []any{}},
		"state":     map[string]any{"install_mode": "simple", "core_version": "1", "modules": []any{map[string]any{"id": "core", "version": "1", "state": "wanted"}}},
		"duplicate": map[string]any{"install_mode": "simple", "core_version": "1", "modules": []any{map[string]any{"id": "core", "state": "enabled"}, map[string]any{"id": "core", "state": "enabled"}}},
	} {
		if s, code := apiCode(t, dc.Call(ctx, "PUT", "/api/v1/device/modules", body, nil)); s != 422 || code != "validation" {
			t.Fatalf("%s: %d %s", name, s, code)
		}
	}
	if s, code := apiCode(t, dc.Call(ctx, "PUT", "/api/v1/device/modules", map[string]any{"install_mode": "simple", "extra": 1}, nil)); s != 400 || code != "bad_json" {
		t.Fatalf("unknown field: %d %s", s, code)
	}
	if code, out := alice.req("PUT", "/api/v1/device/modules", report); code != 403 || errCode(out) != "device_required" {
		t.Fatalf("session on dev route: %d %v", code, out)
	}

	// Catalog: dev and sess, bare array in contract order.
	var cat []wire.ModuleCatalogEntry
	if err := dc.Call(ctx, "GET", "/api/v1/modules/catalog", nil, &cat); err != nil {
		t.Fatal(err)
	}
	want := []string{"core", "sync", "automation", "profiles", "vss", "discovery", "reinstall"}
	if len(cat) != len(want) {
		t.Fatalf("catalog: %+v", cat)
	}
	for i, c := range cat {
		if c.ID != want[i] || c.Name == "" || len(c.Modes) == 0 || c.RequiresFeatures == nil {
			t.Fatalf("catalog[%d]: %+v", i, c)
		}
	}
	if cat[0].Delivery != "builtin" || cat[4].Delivery != "component" || len(cat[0].Modes) != 2 {
		t.Fatalf("catalog delivery/modes: %+v", cat)
	}
	if code, _ := alice.req("GET", "/api/v1/modules/catalog", nil); code != 200 {
		t.Fatalf("session catalog: %d", code)
	}

	// Mode-change request: owner and admin create a task; others cannot.
	mr := fmt.Sprintf("/api/v1/devices/%d/mode-request", dc.DeviceID)
	out = alice.must("POST", mr, map[string]any{"target_mode": "simple"}, 201)
	taskID := int64(out["task_id"].(float64))
	var kind, payload, status string
	if err := e.st.DB.QueryRowContext(ctx, `SELECT kind, payload, status FROM device_tasks WHERE id = ? AND device_id = ?`, taskID, dc.DeviceID).Scan(&kind, &payload, &status); err != nil {
		t.Fatal(err)
	}
	var p wire.TaskModeChangeRequestPayload
	_ = json.Unmarshal([]byte(payload), &p)
	if kind != wire.TaskKindModeChangeRequest || status != "pending" || p.TargetMode != "simple" || p.RequestedBy != "alice" {
		t.Fatalf("task: %s %s %s", kind, payload, status)
	}
	admin.must("POST", mr, map[string]any{"target_mode": "advanced"}, 201)
	if code, _ := bob.req("POST", mr, map[string]any{"target_mode": "advanced"}); code != 404 {
		t.Fatalf("other user mode request: %d", code)
	}
	if code, out := alice.req("POST", mr, map[string]any{"target_mode": "expert"}); code != 422 || errCode(out) != "validation" {
		t.Fatalf("bad target: %d %v", code, out)
	}
	if s, code := apiCode(t, dc.Call(ctx, "POST", mr, map[string]any{"target_mode": "simple"}, nil)); s != 403 || code != "session_required" {
		t.Fatalf("device mode request: %d %s", s, code)
	}
}
