package api

// Installing updates and switching automatic updates from the admin
// console. The server runs in a locked-down container and cannot run the
// host installer; install.sh adds a per-minute cron job ("install.sh
// control") that does. They talk through files in <state>/control:
//
//	helper.json   heartbeat of the host job {seen_at, auto_update, version}
//	request.json  one pending request {action, id} written here
//	status.json   progress of the last request, written by the host job
//
// The host job only maps the action to one of its fixed commands.

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
)

// helperFresh: a heartbeat older than this means the host job is not
// running (not installed yet, cron stopped, host down).
const helperFresh = 3 * time.Minute

// Control actions the host job understands.
const (
	controlUpgrade       = "upgrade"
	controlAutoUpdateOn  = "auto-update-on"
	controlAutoUpdateOff = "auto-update-off"
)

// ControlJob is the last request to the host job and its progress.
type ControlJob struct {
	ID     string `json:"id"`
	Action string `json:"action"`
	// State is queued (waiting for the host job), running, done or failed.
	State      string `json:"state"`
	StartedAt  int64  `json:"started_at,omitempty"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	ExitCode   int    `json:"exit_code"`
	Log        string `json:"log,omitempty"`
}

type helperBeat struct {
	SeenAt     int64  `json:"seen_at"`
	AutoUpdate string `json:"auto_update"`
	Version    string `json:"version"`
}

func (s *Server) controlDir() string { return filepath.Join(s.cfg.StateDir, "control") }

func readJSONFile(p string, v any) bool {
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 1<<20 {
		return false
	}
	return json.Unmarshal(b, v) == nil
}

func writeJSONFile(p string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp := p + ".tmp-" + auth.RandomToken(6)
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, p); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// withControl adds the host job's state to an update status.
func (s *Server) withControl(st UpdateStatus) UpdateStatus {
	dir := s.controlDir()
	_ = os.MkdirAll(dir, 0o755) // the host job writes here too
	var beat helperBeat
	if readJSONFile(filepath.Join(dir, "helper.json"), &beat) && beat.SeenAt > 0 {
		st.HelperSeenAt = beat.SeenAt
		st.Helper = time.Since(time.Unix(beat.SeenAt, 0)) < helperFresh
		if st.Helper && (beat.AutoUpdate == "on" || beat.AutoUpdate == "off") {
			st.AutoUpdate = beat.AutoUpdate // live from the host's crontab
		}
	}
	var job ControlJob
	if readJSONFile(filepath.Join(dir, "status.json"), &job) && job.ID != "" {
		st.Job = &job
	}
	return st
}

// handleAdminUpdateInstall asks the host job to upgrade to the latest
// release (with the installer's backup and rollback).
func (s *Server) handleAdminUpdateInstall(w http.ResponseWriter, r *http.Request) {
	s.requestControl(w, r, controlUpgrade, "requested an upgrade from the admin console")
}

// handleAdminUpdateAuto turns the host's scheduled updates on or off.
func (s *Server) handleAdminUpdateAuto(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Enabled {
		s.requestControl(w, r, controlAutoUpdateOn, "turned automatic updates on")
		return
	}
	s.requestControl(w, r, controlAutoUpdateOff, "turned automatic updates off")
}

func (s *Server) requestControl(w http.ResponseWriter, r *http.Request, action, what string) {
	st := s.withControl(s.updates.snapshot())
	if !st.Helper {
		writeErr(w, r, errf(409, "helper_offline", "The server's update helper is not running. Run the upgrade command on the server once to install it."))
		return
	}
	dir := s.controlDir()
	if _, err := os.Stat(filepath.Join(dir, "request.json")); err == nil {
		writeErr(w, r, errf(409, "busy", "A request is already waiting for the server's update helper"))
		return
	}
	if st.Job != nil && (st.Job.State == "queued" || st.Job.State == "running") {
		writeErr(w, r, errf(409, "busy", "The server's update helper is still working on the last request"))
		return
	}
	job := ControlJob{ID: auth.RandomToken(12), Action: action, State: "queued", StartedAt: time.Now().Unix()}
	// The status first, so the console shows the request as queued at once;
	// the host job replaces it when it picks the request up.
	if err := writeJSONFile(filepath.Join(dir, "status.json"), job); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := writeJSONFile(filepath.Join(dir, "request.json"), map[string]string{"action": action, "id": job.ID}); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "admin.update_control", action, what, true)
	st.Job = &job
	writeJSON(w, 202, st)
}
