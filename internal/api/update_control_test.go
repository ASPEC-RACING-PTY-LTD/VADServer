package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The Updates buttons leave a request for the host helper only while it is
// running, one at a time, and show the helper's progress.
func TestUpdateControl(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	ctl := filepath.Join(e.dir, "state", "control")

	st := admin.must("GET", "/api/v1/admin/update", nil, 200)
	if st["helper"] != false {
		t.Fatalf("helper online without a heartbeat: %v", st)
	}
	admin.must("POST", "/api/v1/admin/update/install", nil, 409)

	beat := func(at time.Time, auto string) {
		b := []byte(`{"seen_at":` + strconv.FormatInt(at.Unix(), 10) + `,"auto_update":"` + auto + `","version":"0.1.2"}`)
		if err := os.WriteFile(filepath.Join(ctl, "helper.json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	beat(time.Now().Add(-10*time.Minute), "on")
	if st := admin.must("GET", "/api/v1/admin/update", nil, 200); st["helper"] != false || st["auto_update"] != "off" {
		t.Fatalf("stale heartbeat counted: %v", st)
	}
	beat(time.Now(), "on")
	if st := admin.must("GET", "/api/v1/admin/update", nil, 200); st["helper"] != true || st["auto_update"] != "on" {
		t.Fatalf("fresh heartbeat not used: %v", st)
	}

	out := admin.must("POST", "/api/v1/admin/update/install", nil, 202)
	job := out["job"].(map[string]any)
	if job["state"] != "queued" || job["action"] != "upgrade" {
		t.Fatalf("job %v", job)
	}
	var req map[string]string
	b, _ := os.ReadFile(filepath.Join(ctl, "request.json"))
	if json.Unmarshal(b, &req) != nil || req["action"] != "upgrade" || req["id"] != job["id"] {
		t.Fatalf("request.json = %s", b)
	}
	// One request at a time.
	admin.must("POST", "/api/v1/admin/update/auto", map[string]any{"enabled": false}, 409)

	// The helper picked it up and finished.
	os.Remove(filepath.Join(ctl, "request.json"))
	done := `{"id":"` + req["id"] + `","action":"upgrade","state":"done","started_at":1,"finished_at":2,"exit_code":0,"log":"Upgrade complete"}`
	os.WriteFile(filepath.Join(ctl, "status.json"), []byte(done), 0o644)
	st = admin.must("GET", "/api/v1/admin/update", nil, 200)
	if j := st["job"].(map[string]any); j["state"] != "done" || j["log"] != "Upgrade complete" {
		t.Fatalf("job after finish: %v", j)
	}
	out = admin.must("POST", "/api/v1/admin/update/auto", map[string]any{"enabled": false}, 202)
	if out["job"].(map[string]any)["action"] != "auto-update-off" {
		t.Fatalf("auto off job: %v", out["job"])
	}
}
