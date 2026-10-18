package api_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// dirLinkOrSkip creates a directory symlink, or a junction on Windows
// without the symlink privilege; the test is skipped when neither works.
func dirLinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	err := os.Symlink(target, link)
	if err != nil && runtime.GOOS == "windows" {
		if _, jerr := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); jerr == nil {
			return
		}
	}
	if err != nil {
		t.Skipf("cannot create directory link: %v", err)
	}
}

func TestAdminStorageLocationValidation(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	outside := t.TempDir()
	for _, d := range []string{filepath.Join(e.storage, "hdd2"), filepath.Join(e.storage, "default", "inner")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, path string
		code       int
		errCode    string
	}{
		{"outside roots", outside, 400, "path_not_allowed"},
		{"missing", filepath.Join(e.storage, "missing"), 400, "bad_path"},
		{"inside existing", filepath.Join(e.storage, "default", "inner"), 400, "nested"},
		{"contains existing", e.storage, 400, "nested"},
		{"ok", filepath.Join(e.storage, "hdd2"), 201, ""},
		{"duplicate", filepath.Join(e.storage, "hdd2"), 400, "nested"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, out := admin.req("POST", "/api/v1/admin/storage", map[string]any{"name": tc.name, "path": tc.path})
			if code != tc.code || (tc.errCode != "" && errCode(out) != tc.errCode) {
				t.Fatalf("got %d %v, want %d %s", code, out, tc.code, tc.errCode)
			}
		})
	}

	// a link inside the allowed root that points outside it is refused
	dirLinkOrSkip(t, outside, filepath.Join(e.storage, "escape"))
	code, out := admin.req("POST", "/api/v1/admin/storage", map[string]any{"name": "escape", "path": filepath.Join(e.storage, "escape")})
	if code != 400 || errCode(out) != "path_not_allowed" {
		t.Fatalf("link escaping the storage root accepted: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(outside, "blobs")); err == nil {
		t.Fatal("layout was created outside the storage root")
	}
}

func TestAdminStorageEnableAndAssign(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	dir := filepath.Join(e.storage, "hdd2")
	os.MkdirAll(dir, 0o755)
	l := admin.must("POST", "/api/v1/admin/storage", map[string]any{"name": "HDD 2", "path": dir}, 201)["location"].(map[string]any)
	id := int64(l["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/storage/%d", id)
	admin.must("PATCH", path, map[string]any{"enabled": false}, 200)

	// users cannot be assigned to a disabled location
	code, out := admin.req("POST", "/api/v1/admin/users", map[string]any{"username": "bob", "password": "password-bob", "location_id": id})
	if code != 400 || errCode(out) != "bad_location" {
		t.Fatalf("assign to disabled location on create: %d %v", code, out)
	}
	u := admin.must("POST", "/api/v1/admin/users", map[string]any{"username": "carol", "password": "password-carol"}, 201)["user"].(map[string]any)
	userPath := fmt.Sprintf("/api/v1/admin/users/%d", int64(u["id"].(float64)))
	code, out = admin.req("PATCH", userPath, map[string]any{"location_id": id})
	if code != 400 || errCode(out) != "bad_location" {
		t.Fatalf("assign to disabled location on patch: %d %v", code, out)
	}

	// an unmounted location (marker missing) cannot be enabled or made default
	marker := filepath.Join(dir, ".vaultdock-location")
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	os.Remove(marker)
	for _, body := range []map[string]any{{"enabled": true}, {"is_default": true}} {
		code, out = admin.req("PATCH", path, body)
		if code != 400 || errCode(out) != "location_unavailable" {
			t.Fatalf("%v on unmounted location: %d %v", body, code, out)
		}
	}
	os.WriteFile(marker, raw, 0o640)
	admin.must("PATCH", path, map[string]any{"enabled": true}, 200)
	admin.must("PATCH", userPath, map[string]any{"location_id": id}, 200)
	admin.must("PATCH", userPath, map[string]any{"quota_bytes": 1000}, 200)
	if code, out = admin.req("PATCH", userPath, map[string]any{"quota_bytes": -1}); code != 400 {
		t.Fatalf("negative quota: %d %v", code, out)
	}
}

func TestAdminSettingsPartialUpdate(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	admin.must("PUT", "/api/v1/admin/settings", map[string]any{"site_name": "Vault A", "trash_retention_days": 7}, 200)
	s := admin.must("PUT", "/api/v1/admin/settings", map[string]any{"registration_enabled": true}, 200)["settings"].(map[string]any)
	if s["site_name"] != "Vault A" || s["trash_retention_days"] != float64(7) || s["registration_enabled"] != true {
		t.Fatalf("partial PUT lost fields: %v", s)
	}
	got := admin.must("GET", "/api/v1/admin/settings", nil, 200)["settings"].(map[string]any)
	if got["site_name"] != "Vault A" || got["trash_retention_days"] != float64(7) || got["registration_enabled"] != true || got["keep_replaced_versions"] != true {
		t.Fatalf("stored settings: %v", got)
	}
	for _, tc := range []struct {
		body any
		code string
	}{
		{map[string]any{"no_such_field": 1}, "bad_json"},
		{map[string]any{"site_name": 5}, "bad_json"},
		{[]any{1}, "bad_json"},
		{map[string]any{"trash_retention_days": -1}, "invalid_settings"},
	} {
		if code, out := admin.req("PUT", "/api/v1/admin/settings", tc.body); code != 400 || errCode(out) != tc.code {
			t.Errorf("%v: %d %v, want 400 %s", tc.body, code, out, tc.code)
		}
	}
	// min_free_bytes reaches the storage manager: a huge floor refuses uploads
	admin.must("PUT", "/api/v1/admin/settings", map[string]any{"min_free_bytes": int64(1) << 60}, 200)
	if code, out := admin.req("POST", "/api/v1/uploads", map[string]any{"name": "x.bin", "size": 10}); code != 507 {
		t.Fatalf("global floor not applied: %d %v", code, out)
	}
}

func TestMaintenanceKeepsFinalizingUploads(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	up := admin.must("POST", "/api/v1/uploads", map[string]any{"name": "big.bin", "size": 10}, 201)["upload"].(map[string]any)
	id := up["id"].(string)
	ctx := context.Background()
	exists := func() bool {
		var n int
		if err := e.st.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM uploads WHERE id = ?", id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	setState := func(status string, age time.Duration) {
		if _, err := e.st.DB.ExecContext(ctx, "UPDATE uploads SET status = ?, expires_at = ? WHERE id = ?", status, time.Now().Add(-age).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
	}
	// R5: an expired upload that is being finalised is left alone
	setState("finalizing", 24*time.Hour)
	e.srv.RunMaintenance(ctx, false)
	if !exists() {
		t.Fatal("finalising upload cancelled by maintenance")
	}
	// and an administrator cannot cancel it either
	if code, out := admin.req("DELETE", "/api/v1/admin/uploads/"+id, nil); code != 409 || errCode(out) != "upload_busy" {
		t.Fatalf("admin cancel of finalising upload: %d %v", code, out)
	}
	// once reopened (as after a restart) it expires normally
	setState("open", time.Minute)
	e.srv.RunMaintenance(ctx, false)
	if exists() {
		t.Fatal("expired open upload not cleaned")
	}
}
