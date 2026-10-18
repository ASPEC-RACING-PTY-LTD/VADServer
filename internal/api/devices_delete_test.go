package api_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
)

func apiCode(t *testing.T, err error) (int, string) {
	t.Helper()
	var ae *client.APIError
	if !errors.As(err, &ae) {
		t.Fatalf("expected API error, got %v", err)
	}
	return ae.Status, ae.Code
}

func deviceByID(t *testing.T, w *web, id int64) map[string]any {
	t.Helper()
	for _, d := range w.must("GET", "/api/v1/devices", nil, 200)["devices"].([]any) {
		dm := d.(map[string]any)
		if int64(dm["id"].(float64)) == id {
			return dm
		}
	}
	t.Fatalf("device %d not listed", id)
	return nil
}

// TestDeviceDeletePermission covers defect B1: devices cannot purge trash,
// and devices.can_delete gates device deletes.
func TestDeviceDeletePermission(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	tmp := t.TempDir()

	upload := func(name string) int64 {
		p, _ := makeFile(t, tmp, name, 1000)
		res, err := dc.UploadFile(ctx, p, "/docs/"+name, nil)
		if err != nil {
			t.Fatal(err)
		}
		return nodeID(t, res.Node)
	}

	// Default: can_delete is true and the device may delete.
	if got := deviceByID(t, alice, dc.DeviceID)["can_delete"]; got != true {
		t.Fatalf("default can_delete = %v", got)
	}
	a := upload("a.txt")
	if err := dc.Call(ctx, "DELETE", fmt.Sprintf("/api/v1/files/%d", a), nil, nil); err != nil {
		t.Fatalf("device delete with can_delete: %v", err)
	}

	// Devices may list and restore trash but never purge it.
	if err := dc.Call(ctx, "GET", "/api/v1/trash", nil, nil); err != nil {
		t.Fatalf("device trash list: %v", err)
	}
	for _, path := range []string{fmt.Sprintf("/api/v1/trash/%d", a), "/api/v1/trash"} {
		st, code := apiCode(t, dc.Call(ctx, "DELETE", path, nil, nil))
		if st != 403 || code != "session_required" {
			t.Fatalf("device DELETE %s: %d %s", path, st, code)
		}
	}
	if err := dc.Call(ctx, "POST", fmt.Sprintf("/api/v1/trash/%d/restore", a), nil, nil); err != nil {
		t.Fatalf("device trash restore: %v", err)
	}

	// Owner turns can_delete off: device deletes are refused.
	alice.must("PATCH", fmt.Sprintf("/api/v1/devices/%d", dc.DeviceID), map[string]any{"can_delete": false}, 200)
	if got := deviceByID(t, alice, dc.DeviceID)["can_delete"]; got != false {
		t.Fatalf("can_delete after PATCH = %v", got)
	}
	b := upload("b.txt")
	cases := []struct {
		method, path string
		body         any
	}{
		{"DELETE", fmt.Sprintf("/api/v1/files/%d", b), nil},
		{"POST", "/api/v1/files/delete", map[string]any{"ids": []int64{b}}},
		{"POST", "/api/v1/sync/folders/1/ops", map[string]any{"ops": []any{}}},
	}
	for _, c := range cases {
		st, code := apiCode(t, dc.Call(ctx, c.method, c.path, c.body, nil))
		if st != 403 || code != "device_cannot_delete" {
			t.Fatalf("%s %s with can_delete=0: %d %s", c.method, c.path, st, code)
		}
	}
	var me struct {
		Device struct {
			CanDelete *bool `json:"can_delete"`
		} `json:"device"`
		Features []string `json:"features"`
	}
	if err := dc.Call(ctx, "GET", "/api/v1/device/me", nil, &me); err != nil {
		t.Fatal(err)
	}
	if me.Device.CanDelete == nil || *me.Device.CanDelete || me.Features == nil {
		t.Fatalf("device/me: can_delete=%v features=%v", me.Device.CanDelete, me.Features)
	}
	// Sessions are unaffected.
	alice.must("DELETE", fmt.Sprintf("/api/v1/files/%d", b), nil, 200)

	// Another user cannot see or change the device; an admin may toggle
	// can_delete but not rename it.
	path := fmt.Sprintf("/api/v1/devices/%d", dc.DeviceID)
	if code, _ := bob.req("PATCH", path, map[string]any{"can_delete": true}); code != 404 {
		t.Fatalf("other user PATCH: %d", code)
	}
	if code, out := admin.req("PATCH", path, map[string]any{"name": "x"}); code != 403 {
		t.Fatalf("admin rename of foreign device: %d %v", code, out)
	}
	admin.must("PATCH", path, map[string]any{"can_delete": true}, 200)
	if err := dc.Call(ctx, "DELETE", fmt.Sprintf("/api/v1/files/%d", upload("c.txt")), nil, nil); err != nil {
		t.Fatalf("device delete after admin re-enabled: %v", err)
	}
	// Rename keeps working for the owner, and an empty body is still invalid.
	alice.must("PATCH", path, map[string]any{"name": "Laptop"}, 200)
	if code, out := alice.req("PATCH", path, map[string]any{}); code != 400 || errCode(out) != "bad_name" {
		t.Fatalf("empty PATCH: %d %v", code, out)
	}
}

// TestStubRoutesAndAuthGroups checks the 501 stubs, the super group, the
// streamed-route body cap and the feature list.
func TestStubRoutesAndAuthGroups(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	admin.must("POST", "/api/v1/admin/users", map[string]any{"username": "admin2", "password": "password-admin2", "role": "admin"}, 201)
	admin2 := e.web()
	admin2.must("POST", "/api/v1/auth/login", map[string]any{"login": "admin2", "password": "password-admin2"}, 200)
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	info := admin.must("GET", "/api/v1/info", nil, 200)
	if _, ok := info["features"].([]any); !ok {
		t.Fatalf("info features: %v", info["features"])
	}

	if code, out := alice.req("GET", "/api/v1/storage/analytics", nil); code != 501 || errCode(out) != "not_implemented" {
		t.Fatalf("sess stub: %d %v", code, out)
	}
	// Super: the setup admin is the superuser; a second admin is not.
	if code, out := admin.req("GET", "/api/v1/admin/deploy/env", nil); code != 200 {
		t.Fatalf("superuser route: %d %v", code, out)
	}
	if code, out := admin2.req("GET", "/api/v1/admin/deploy/env", nil); code != 403 || errCode(out) != "not_superuser" {
		t.Fatalf("non-superuser admin: %d %v", code, out)
	}
	if code, out := alice.req("GET", "/api/v1/admin/deploy/env", nil); code != 403 || errCode(out) != "not_superuser" {
		t.Fatalf("plain user on super route: %d %v", code, out)
	}
	// Admin group without the /admin prefix.
	if code, _ := alice.req("POST", "/api/v1/devices/1/profiles/requests", map[string]any{"all": true}); code != 403 {
		t.Fatalf("user on admin route: %d", code)
	}
	// dev-only stub refuses sessions.
	if code, out := alice.req("GET", "/api/v1/device/tasks", nil); code != 403 || errCode(out) != "device_required" {
		t.Fatalf("session on dev route: %d %v", code, out)
	}
	// Streamed routes skip the 2 MiB signed-body cap; others do not.
	big := strings.Repeat("a", 3<<20)
	oid := strings.Repeat("0", 64)
	for _, path := range []string{"/api/v1/repos/1/objects/data/" + oid, "/api/v1/device/inventory"} {
		if st, code := apiCode(t, dc.Call(ctx, "PUT", path, big, nil)); st == 413 || st < 400 {
			t.Fatalf("streamed PUT %s: %d %s", path, st, code)
		}
	}
	if st, code := apiCode(t, dc.Call(ctx, "POST", "/api/v1/repos/1/snapshots", big, nil)); st == 413 || st < 400 {
		t.Fatalf("streamed snapshot commit: %d %s", st, code)
	}
	if st, code := apiCode(t, dc.Call(ctx, "PUT", "/api/v1/device/modules", big, nil)); st != 413 || code != "too_large" {
		t.Fatalf("non-streamed big body: %d %s", st, code)
	}
}
