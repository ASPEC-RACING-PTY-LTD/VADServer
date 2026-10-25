package api_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestDiagnosticsAPIKeys: admin-made keys read the diagnostics API and
// nothing else; they never see secrets and stop working when revoked.
func TestDiagnosticsAPIKeys(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	user := e.createUser(admin, "carol", 0)

	if code, _ := user.req("POST", "/api/v1/admin/api-keys", map[string]any{"name": "x", "days": 7}); code != 403 {
		t.Fatalf("user created a key: %d", code)
	}
	if code, _ := user.req("GET", "/api/v1/diag/system", nil); code != 403 {
		t.Fatalf("user read diagnostics: %d", code)
	}
	if code, _ := admin.req("POST", "/api/v1/admin/api-keys", map[string]any{"name": "x", "days": 999}); code != 400 {
		t.Fatalf("1000-day key: %d", code)
	}
	k := admin.must("POST", "/api/v1/admin/api-keys", map[string]any{"name": "Support", "days": 7}, 201)
	tok := k["token"].(string)
	id := int64(k["key"].(map[string]any)["id"].(float64))

	call := func(method, path, token string) (int, string) {
		req, _ := http.NewRequest(method, e.ts.URL+path, strings.NewReader("{}"))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	for _, p := range []string{"/api/v1/diag/", "/api/v1/diag/system", "/api/v1/diag/logs?lines=50", "/api/v1/diag/smart", "/api/v1/diag/admin/health", "/api/v1/diag/admin/overview", "/api/v1/diag/admin/storage"} {
		code, body := call("GET", p, tok)
		if code != 200 {
			t.Fatalf("GET %s with key: %d %s", p, code, body)
		}
		low := strings.ToLower(body)
		for _, bad := range []string{tok, "password_hash", "vdk_" + tok[4:12]} {
			if strings.Contains(low, strings.ToLower(bad)) {
				t.Fatalf("%s leaks %q", p, bad)
			}
		}
	}
	var sys map[string]any
	_, body := call("GET", "/api/v1/diag/system", tok)
	_ = json.Unmarshal([]byte(body), &sys)
	if sys["container"] == nil || sys["host"] == nil {
		t.Fatalf("system: %v", sys)
	}
	// Nothing outside the diagnostics routes, and no writes.
	for _, c := range [][2]string{{"GET", "/api/v1/files"}, {"GET", "/api/v1/admin/users"}, {"GET", "/api/v1/admin/api-keys"}, {"POST", "/api/v1/diag/system"}, {"DELETE", fmt.Sprintf("/api/v1/admin/api-keys/%d", id)}, {"PUT", "/api/v1/admin/smart/settings"}} {
		if code, _ := call(c[0], c[1], tok); code != 403 {
			t.Fatalf("%s %s with key: %d", c[0], c[1], code)
		}
	}
	if code, _ := call("GET", "/api/v1/diag/system", "vdk_notakey"); code != 401 {
		t.Fatalf("bad key: %d", code)
	}
	// Listed (prefix only), then revoked.
	keys := admin.must("GET", "/api/v1/admin/api-keys", nil, 200)["keys"].([]any)
	if len(keys) != 1 || keys[0].(map[string]any)["prefix"] != tok[:12] || keys[0].(map[string]any)["last_used_at"] == nil {
		t.Fatalf("keys: %v", keys)
	}
	admin.must("DELETE", fmt.Sprintf("/api/v1/admin/api-keys/%d", id), nil, 204)
	if code, _ := call("GET", "/api/v1/diag/system", tok); code != 401 {
		t.Fatalf("revoked key: %d", code)
	}
}
