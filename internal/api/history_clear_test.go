package api_test

import "testing"

func TestAdminClearHistory(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	alice.must("POST", "/api/v1/admin/history/clear", nil, 403)
	out := admin.must("POST", "/api/v1/admin/history/clear", nil, 200)
	for _, k := range []string{"transfers", "activity", "notifications"} {
		if _, ok := out[k]; !ok {
			t.Fatalf("missing %q in %v", k, out)
		}
	}
	// The clear itself stays in the audit log.
	found := false
	for _, a := range admin.must("GET", "/api/v1/admin/audit", nil, 200)["items"].([]any) {
		found = found || a.(map[string]any)["action"] == "history_cleared"
	}
	if !found {
		t.Fatal("history_cleared not audited")
	}
}
