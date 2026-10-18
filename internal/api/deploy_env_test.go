package api_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestDeployEnvAPI(t *testing.T) {
	e := newEnv(t, "")
	t.Setenv("COMPOSE_PROFILES", "caddy")
	super := e.setupAdmin() // the first admin is a superuser
	alice := e.createUser(super, "alice", 0)
	bob := e.createUser(super, "bob", 0)
	if _, err := e.st.DB.ExecContext(context.Background(), `UPDATE users SET role = 'admin' WHERE username = 'bob'`); err != nil {
		t.Fatal(err)
	}
	dc := e.enrollDevice(alice, "alice", "password-alice")
	approvals := filepath.Join(e.dir, "state", "deploy", "env-removal-approvals.json")

	// Auth: super only (an admin without is_superuser is refused).
	for _, w := range []*web{alice, bob} {
		code, out := w.req("GET", "/api/v1/admin/deploy/env", nil)
		if code != 403 || (w == bob && errCode(out) != wire.CodeNotSuperuser) {
			t.Fatalf("non-super GET: %d %v", code, out)
		}
		if code, _ := w.req("POST", "/api/v1/admin/deploy/env/removals", map[string]any{"names": []string{"COMPOSE_PROFILES"}, "confirm_text": "REMOVE 1 VARIABLES"}); code != 403 {
			t.Fatalf("non-super POST: %d", code)
		}
		if code, _ := w.req("DELETE", "/api/v1/admin/deploy/env/removals/COMPOSE_PROFILES", nil); code != 403 {
			t.Fatalf("non-super DELETE: %d", code)
		}
	}
	if st, _ := apiCode(t, dc.Call(context.Background(), "GET", "/api/v1/admin/deploy/env", nil, nil)); st != 403 {
		t.Fatalf("device GET: %d", st)
	}
	if _, err := os.Stat(approvals); !os.IsNotExist(err) {
		t.Fatalf("approvals written by a refused request: %v", err)
	}

	// GET: obsolete variable present, no secret values.
	code, raw := rawGet(t, super, "/api/v1/admin/deploy/env")
	if code != 200 || strings.Contains(raw, setupToken) {
		t.Fatalf("GET: %d %s", code, raw)
	}
	var st wire.DeployEnvStatus
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	if st.SchemaVersion < 1 || !slices.ContainsFunc(st.ObsoletePresent, func(o wire.DeployEnvObsolete) bool { return o.Name == "COMPOSE_PROFILES" }) || len(st.PendingRemovals) != 0 {
		t.Fatalf("status: %+v", st)
	}

	// Validation.
	for _, c := range []struct {
		body any
		want int
		code string
	}{
		{map[string]any{"names": []string{}, "confirm_text": "REMOVE 0 VARIABLES"}, 400, "validation_failed"},
		{map[string]any{"names": []string{"COMPOSE_PROFILES"}, "confirm_text": "remove 1 variables"}, 400, "validation_failed"},
		{map[string]any{"names": []string{"COMPOSE_PROFILES", "COMPOSE_PROFILES"}, "confirm_text": "REMOVE 2 VARIABLES"}, 400, "validation_failed"},
		{map[string]any{"names": []string{"NOT_IN_SCHEMA"}, "confirm_text": "REMOVE 1 VARIABLES"}, 400, "validation_failed"},
		{map[string]any{"names": []string{"bad name"}, "confirm_text": "REMOVE 1 VARIABLES"}, 400, "validation_failed"},
		{map[string]any{"names": []string{"COMPOSE_PROFILES"}, "confirm_text": "REMOVE 1 VARIABLES", "extra": 1}, 400, "bad_json"},
		{map[string]any{"names": []string{"VAULTDOCK_SETUP_TOKEN"}, "confirm_text": "REMOVE 1 VARIABLES"}, 409, wire.CodeEnvChangeRequiresApproval},
	} {
		code, out := super.req("POST", "/api/v1/admin/deploy/env/removals", c.body)
		if code != c.want || errCode(out) != c.code {
			t.Fatalf("%v: %d %v", c.body, code, out)
		}
	}
	if _, err := os.Stat(approvals); !os.IsNotExist(err) {
		t.Fatalf("approvals written by an invalid request: %v", err)
	}

	// Approve: written to the state volume as an explicit per-variable approval.
	out := super.must("POST", "/api/v1/admin/deploy/env/removals", wire.DeployEnvRemovalRequest{Names: []string{"COMPOSE_PROFILES"}, ConfirmText: wire.DeployEnvRemovalConfirmText(1)}, 200)
	pr, _ := out["pending_removals"].([]any)
	if len(pr) != 1 || pr[0].(map[string]any)["name"] != "COMPOSE_PROFILES" || pr[0].(map[string]any)["approved_by"].(float64) < 1 {
		t.Fatalf("approve: %v", out)
	}
	b, err := os.ReadFile(approvals)
	if err != nil || !strings.Contains(string(b), `"name": "COMPOSE_PROFILES"`) {
		t.Fatalf("approvals file: %s %v", b, err)
	}
	if out := super.must("GET", "/api/v1/admin/deploy/env", nil, 200); len(out["pending_removals"].([]any)) != 1 {
		t.Fatalf("GET after approve: %v", out)
	}

	// Withdraw.
	if code, out := super.req("DELETE", "/api/v1/admin/deploy/env/removals/OTHER", nil); code != 404 {
		t.Fatalf("withdraw unknown: %d %v", code, out)
	}
	out = super.must("DELETE", "/api/v1/admin/deploy/env/removals/COMPOSE_PROFILES", nil, 200)
	if len(out["pending_removals"].([]any)) != 0 {
		t.Fatalf("withdraw: %v", out)
	}
	if _, err := os.Stat(approvals); !os.IsNotExist(err) {
		t.Fatalf("approvals file kept: %v", err)
	}

	// The feature is advertised.
	info := e.web().must("GET", "/api/v1/info", nil, 200)
	if !slices.Contains(info["features"].([]any), any(wire.FeatureDeployEnv)) {
		t.Fatalf("features: %v", info["features"])
	}
}

// rawGet returns the status and the raw body of a session GET.
func rawGet(t *testing.T, w *web, path string) (int, string) {
	t.Helper()
	resp, err := w.c.Get(w.base + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}
