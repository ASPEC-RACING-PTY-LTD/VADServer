// Owner: SRV-7 (P1-21). Scope: api-v1 §11 readiness and device backup state
// (shapes per rev 1.0 §11 plus the rev 1.1 §11 checks).

package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/schedule"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// readinessCheckServerKeys is an extra check shown only to administrators:
// server key custody (the KEK). It is not a rev 1.0 check id; clients show
// unknown ids as-is.
const readinessCheckServerKeys = "server_key_custody"

// Readiness thresholds.
const (
	readinessNeverGrace     = 24 * time.Hour      // a new job may have no backup yet
	readinessCriticalAge    = 7 * 24 * time.Hour  // overdue and older than this is critical
	readinessManualStale    = 30 * 24 * time.Hour // manual jobs
	readinessVerifyStale    = 30 * 24 * time.Hour
	readinessTestRestoreAge = 90 * 24 * time.Hour
)

// mountReadiness registers the routes of this feature area.
func (s *Server) mountReadiness(rt routes) {
	rt.Sess.Get("/readiness", s.handleReadiness) // ?device= | ?scope=all (admin)
	rt.Dev.Put("/device/backup-state", s.handlePutBackupState)
}

// handlePutBackupState stores the readiness inputs only the device can
// supply (recovery key confirmed, service installed, VSS available).
func (s *Server) handlePutBackupState(w http.ResponseWriter, r *http.Request) {
	d := principal(r).Device
	var req wire.BackupState
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.PutBackupState(r.Context(), d.ID, req); err != nil {
		writeErr(w, r, err)
		return
	}
	slog.Debug("device backup state reported", "device", d.ID)
	w.WriteHeader(http.StatusNoContent)
}

// handleReadiness answers GET /readiness. Without parameters it covers the
// caller's approved devices. ?device=<id> covers one device (its owner or an
// admin; anyone else gets 404). ?scope=all covers every approved device and
// is admin-only. Administrators also see the server key custody check.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	q := r.URL.Query()
	scope := q.Get("scope")
	if scope != "" && scope != "mine" && scope != "all" {
		writeErr(w, r, validationErr(map[string]string{"scope": "must be mine or all"}))
		return
	}
	var devs []store.Device
	if v := q.Get("device"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id <= 0 {
			writeErr(w, r, validationErr(map[string]string{"device": "must be a device id"}))
			return
		}
		d, err := s.st.Device(r.Context(), id)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if d.UserID != p.User.ID && !p.User.IsAdmin() {
			writeErr(w, r, store.ErrNotFound)
			return
		}
		devs = []store.Device{*d}
	} else {
		userID := p.User.ID
		if scope == "all" {
			if !p.User.IsAdmin() {
				writeErr(w, r, errf(403, "forbidden", "Administrator access required"))
				return
			}
			userID = 0
		}
		var err error
		if devs, err = s.st.ReadinessDevices(r.Context(), userID); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	rep, err := s.readinessReport(r.Context(), devs, p.User.IsAdmin())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, rep)
}

// readinessStatus is the overall readiness of a user's devices, for
// GET /summary.
func (s *Server) readinessStatus(ctx context.Context, user *store.User) (string, error) {
	devs, err := s.st.ReadinessDevices(ctx, user.ID)
	if err != nil {
		return wire.ReadinessStatusUnknown, err
	}
	rep, err := s.readinessReport(ctx, devs, user.IsAdmin())
	if err != nil {
		return wire.ReadinessStatusUnknown, err
	}
	return rep.Status, nil
}

func (s *Server) readinessReport(ctx context.Context, devs []store.Device, admin bool) (*wire.ReadinessReport, error) {
	ev := readinessEval{now: time.Now(), keysOK: s.Keys().Available(), admin: admin}
	rep := &wire.ReadinessReport{Devices: []wire.ReadinessDevice{}}
	for i := range devs {
		f, err := s.st.ReadinessFacts(ctx, &devs[i])
		if err != nil {
			return nil, err
		}
		rep.Devices = append(rep.Devices, ev.device(f))
	}
	rep.Score, rep.Status = readinessScore(rep.Devices)
	return rep, nil
}

// readinessScore averages the scored checks (ok 100, warning 50, critical 0;
// unknown and the informational install_mode are left out). The status is
// the worst scored check, or unknown when nothing could be scored.
func readinessScore(devs []wire.ReadinessDevice) (int, string) {
	total, n := 0, 0
	status := wire.ReadinessStatusUnknown
	for _, d := range devs {
		for _, c := range d.Checks {
			if c.ID == wire.ReadinessCheckInstallMode {
				continue
			}
			switch c.Status {
			case wire.ReadinessStatusOK:
				total += 100
			case wire.ReadinessStatusWarning:
				total += 50
			case wire.ReadinessStatusCritical:
			default:
				continue
			}
			n++
			if statusRank(c.Status) > statusRank(status) {
				status = c.Status
			}
		}
	}
	if n == 0 {
		return 0, wire.ReadinessStatusUnknown
	}
	return (total + n/2) / n, status
}

func statusRank(s string) int {
	switch s {
	case wire.ReadinessStatusOK:
		return 1
	case wire.ReadinessStatusWarning:
		return 2
	case wire.ReadinessStatusCritical:
		return 3
	}
	return 0
}

// readinessEval turns the facts of one device into checks.
type readinessEval struct {
	now    time.Time
	keysOK bool
	admin  bool
}

func check(id, status, msg, link string) wire.ReadinessCheck {
	return wire.ReadinessCheck{ID: id, Status: status, Message: msg, FixLink: link}
}

// runnable reports whether a job is expected to run: enabled and active.
func runnable(j *store.BackupJob) bool {
	return j.Enabled && j.State == wire.JobStateActive
}

func (ev readinessEval) device(f *store.ReadinessFacts) wire.ReadinessDevice {
	out := wire.ReadinessDevice{DeviceID: f.Device.ID, Checks: []wire.ReadinessCheck{}, Jobs: []wire.ReadinessJob{}}
	vssUsable := f.Modules.Enabled(wire.ModuleVSS) && f.State != nil && f.State.VSSAvailable
	for _, j := range f.Jobs {
		out.Jobs = append(out.Jobs, wire.ReadinessJob{
			JobID:          j.Job.ID,
			LastSuccessAt:  j.LastSuccessAt,
			OpenIssues:     j.OpenIssues,
			VSSUsed:        vssUsable && j.Job.Spec.UseVSS != wire.JobUseVSSOff,
			VerifiedAt:     j.VerifiedAt,
			TestRestoredAt: j.TestRestoredAt,
		})
	}
	out.Checks = append(out.Checks,
		ev.recentBackup(f),
		ev.openIssues(f),
		ev.vssAvailable(f),
		ev.freshness(f, wire.ReadinessCheckVerifiedRecently, readinessVerifyStale, "verified",
			func(j *store.ReadinessJob) int64 { return j.VerifiedAt }),
		ev.freshness(f, wire.ReadinessCheckTestRestoreRecent, readinessTestRestoreAge, "test-restored",
			func(j *store.ReadinessJob) int64 { return j.TestRestoredAt }),
		ev.recoveryKey(f),
		ev.serviceInstalled(f),
		ev.profileAuthorised(f),
		ev.installMode(f),
		ev.modulesHealthy(f),
	)
	if ev.admin {
		if ev.keysOK {
			out.Checks = append(out.Checks, check(readinessCheckServerKeys, wire.ReadinessStatusOK, "Server key custody is available.", ""))
		} else {
			out.Checks = append(out.Checks, check(readinessCheckServerKeys, wire.ReadinessStatusCritical,
				"The server key (KEK) is missing, so escrow is unavailable. Create and back up the KEK with install.sh.", "/admin"))
		}
	}
	return out
}

func jobsLink(f *store.ReadinessFacts) string { return fmt.Sprintf("/backups?device=%d", f.Device.ID) }

func (ev readinessEval) recentBackup(f *store.ReadinessFacts) wire.ReadinessCheck {
	worst := check(wire.ReadinessCheckRecentBackup, wire.ReadinessStatusOK, "All backup jobs are up to date.", "")
	suspended, active := 0, 0
	for i := range f.Jobs {
		j := &f.Jobs[i]
		if j.Job.Enabled && j.Job.State != wire.JobStateActive {
			suspended++
		}
		if !runnable(&j.Job) {
			continue
		}
		active++
		status, msg := ev.jobFreshness(j)
		if statusRank(status) > statusRank(worst.Status) {
			worst = check(wire.ReadinessCheckRecentBackup, status,
				fmt.Sprintf("%q: %s", j.Job.Name, msg), fmt.Sprintf("/backups/jobs/%d", j.Job.ID))
		}
	}
	switch {
	case active > 0:
		return worst
	case suspended > 0:
		return check(wire.ReadinessCheckRecentBackup, wire.ReadinessStatusCritical,
			fmt.Sprintf("All backup jobs are suspended (%d).", suspended), jobsLink(f))
	case len(f.Jobs) > 0:
		return check(wire.ReadinessCheckRecentBackup, wire.ReadinessStatusCritical, "All backup jobs are disabled.", jobsLink(f))
	}
	return check(wire.ReadinessCheckRecentBackup, wire.ReadinessStatusCritical, "No backup jobs are set up for this device.", jobsLink(f))
}

// jobFreshness compares a job's last successful backup with its schedule.
// Missing at most one scheduled run is ok; missing more is a warning, and a
// critical once the last success is older than readinessCriticalAge.
func (ev readinessEval) jobFreshness(j *store.ReadinessJob) (string, string) {
	if j.LastSuccessAt == 0 {
		if ev.now.Sub(time.UnixMilli(j.Job.CreatedAt)) < readinessNeverGrace {
			return wire.ReadinessStatusWarning, "no successful backup yet"
		}
		return wire.ReadinessStatusCritical, "has never completed a backup"
	}
	last := time.UnixMilli(j.LastSuccessAt)
	age := ev.now.Sub(last)
	ago := "last successful backup " + humanAge(age) + " ago"
	failed := j.LastRunStatus == wire.RunStatusFailed && j.LastRunAt > j.LastSuccessAt
	sched := schedule.Schedule(j.Job.Schedule)
	loc, err := schedule.ResolveLocation(sched.Timezone, time.UTC)
	if err != nil {
		loc = time.UTC
	}
	var overdue bool
	if n1 := sched.Next(last, loc); n1.IsZero() {
		overdue = age > readinessManualStale // manual or no occurrence
	} else {
		n2 := sched.Next(n1, loc)
		overdue = n2.IsZero() || !ev.now.Before(n2)
	}
	switch {
	case overdue && age > readinessCriticalAge:
		return wire.ReadinessStatusCritical, "overdue, " + ago
	case overdue:
		return wire.ReadinessStatusWarning, "missed scheduled backups, " + ago
	case failed:
		return wire.ReadinessStatusWarning, "the last run failed, " + ago
	}
	return wire.ReadinessStatusOK, ago
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return plural(int(d/time.Minute), "minute")
	case d < 48*time.Hour:
		return plural(int(d/time.Hour), "hour")
	}
	return plural(int(d/(24*time.Hour)), "day")
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func (ev readinessEval) openIssues(f *store.ReadinessFacts) wire.ReadinessCheck {
	var n int64
	for _, j := range f.Jobs {
		n += j.OpenIssues
	}
	if n == 0 {
		return check(wire.ReadinessCheckOpenIssues, wire.ReadinessStatusOK, "No open backup issues.", "")
	}
	return check(wire.ReadinessCheckOpenIssues, wire.ReadinessStatusWarning, fmt.Sprintf("%d open backup issues need attention.", n), jobsLink(f))
}

func (ev readinessEval) vssAvailable(f *store.ReadinessFacts) wire.ReadinessCheck {
	if f.State == nil {
		return check(wire.ReadinessCheckVSSAvailable, wire.ReadinessStatusUnknown, "The device has not reported its backup state yet.", "")
	}
	if f.State.VSSAvailable {
		return check(wire.ReadinessCheckVSSAvailable, wire.ReadinessStatusOK, "Snapshot support (VSS) is available.", "")
	}
	for _, j := range f.Jobs {
		if runnable(&j.Job) && j.Job.Spec.UseVSS == wire.JobUseVSSRequired {
			return check(wire.ReadinessCheckVSSAvailable, wire.ReadinessStatusCritical,
				fmt.Sprintf("%q requires snapshot support (VSS), which is not available.", j.Job.Name), "/devices")
		}
	}
	return check(wire.ReadinessCheckVSSAvailable, wire.ReadinessStatusWarning,
		"Snapshot support (VSS) is not available, so locked files are skipped.", "/devices")
}

// freshness checks verify and test-restore recency over the jobs that have
// backed up at least once. Never done at all is unknown (not scored).
func (ev readinessEval) freshness(f *store.ReadinessFacts, id string, stale time.Duration, verb string, at func(*store.ReadinessJob) int64) wire.ReadinessCheck {
	seen, never, old := false, 0, 0
	for i := range f.Jobs {
		j := &f.Jobs[i]
		if !runnable(&j.Job) || j.LastSuccessAt == 0 {
			continue
		}
		switch t := at(j); {
		case t == 0:
			never++
		case ev.now.Sub(time.UnixMilli(t)) > stale:
			seen, old = true, old+1
		default:
			seen = true
		}
	}
	switch {
	case !seen:
		return check(id, wire.ReadinessStatusUnknown, "Backups have not been "+verb+" yet.", jobsLink(f))
	case never+old > 0:
		return check(id, wire.ReadinessStatusWarning,
			fmt.Sprintf("%d backup jobs have not been %s in the last %d days.", never+old, verb, int(stale/(24*time.Hour))), jobsLink(f))
	}
	return check(id, wire.ReadinessStatusOK, "Backups were "+verb+" recently.", "")
}

func (ev readinessEval) recoveryKey(f *store.ReadinessFacts) wire.ReadinessCheck {
	switch {
	case f.State == nil:
		return check(wire.ReadinessCheckRecoveryKeyConfirmed, wire.ReadinessStatusUnknown, "The device has not reported its backup state yet.", "")
	case f.State.RecoveryKeyConfirmed:
		return check(wire.ReadinessCheckRecoveryKeyConfirmed, wire.ReadinessStatusOK, "The recovery key has been confirmed.", "")
	case !ev.keysOK:
		// Without server escrow the recovery key is the only way back.
		return check(wire.ReadinessCheckRecoveryKeyConfirmed, wire.ReadinessStatusCritical,
			"The recovery key has not been confirmed and server escrow is unavailable.", "")
	}
	return check(wire.ReadinessCheckRecoveryKeyConfirmed, wire.ReadinessStatusWarning, "The recovery key has not been confirmed on the device.", "")
}

func (ev readinessEval) serviceInstalled(f *store.ReadinessFacts) wire.ReadinessCheck {
	switch {
	case f.State == nil:
		return check(wire.ReadinessCheckServiceInstalled, wire.ReadinessStatusUnknown, "The device has not reported its backup state yet.", "")
	case f.State.ServiceInstalled:
		return check(wire.ReadinessCheckServiceInstalled, wire.ReadinessStatusOK, "The background service is installed.", "")
	}
	return check(wire.ReadinessCheckServiceInstalled, wire.ReadinessStatusWarning,
		"The background service is not installed, so backups run only while the app is open.", "")
}

func consented(state string) bool {
	return state == wire.ProfileStateActiveDefault || state == wire.ProfileStateAuthorised
}

func (ev readinessEval) profileAuthorised(f *store.ReadinessFacts) wire.ReadinessCheck {
	states := map[string]string{}
	blacklisted := 0
	for _, p := range f.Profiles {
		states[p.SID] = p.State
		if p.State == wire.ProfileStateBlacklisted {
			blacklisted++
		}
	}
	suspended, unauthorised := 0, 0
	for _, j := range f.Jobs {
		if !j.Job.Enabled {
			continue
		}
		if j.Job.State == wire.JobStateSuspendedByProfile {
			suspended++
		} else if j.Job.ProfileSID != "" && !consented(states[j.Job.ProfileSID]) {
			unauthorised++
		}
	}
	link := fmt.Sprintf("/devices?device=%d", f.Device.ID)
	switch {
	case blacklisted > 0 || suspended > 0:
		return check(wire.ReadinessCheckProfileAuthorised, wire.ReadinessStatusCritical,
			fmt.Sprintf("%d blacklisted profiles; %d backup jobs suspended until the profile owner consents again.", blacklisted, suspended), link)
	case unauthorised > 0:
		return check(wire.ReadinessCheckProfileAuthorised, wire.ReadinessStatusWarning,
			fmt.Sprintf("%d backup jobs read a Windows profile without consent.", unauthorised), link)
	}
	return check(wire.ReadinessCheckProfileAuthorised, wire.ReadinessStatusOK, "All backed-up profiles are authorised.", "")
}

func (ev readinessEval) installMode(f *store.ReadinessFacts) wire.ReadinessCheck {
	if f.Modules == nil {
		return check(wire.ReadinessCheckInstallMode, wire.ReadinessStatusUnknown, "The device has not reported its install mode yet.", "")
	}
	mode := "Simple"
	if f.Modules.InstallMode == wire.ModuleInstallModeAdvanced {
		mode = "Advanced"
	}
	return check(wire.ReadinessCheckInstallMode, wire.ReadinessStatusOK, mode+" mode.", "")
}

func (ev readinessEval) modulesHealthy(f *store.ReadinessFacts) wire.ReadinessCheck {
	if f.Modules == nil {
		return check(wire.ReadinessCheckModulesHealthy, wire.ReadinessStatusUnknown, "The device has not reported its modules yet.", "")
	}
	link := fmt.Sprintf("/devices?device=%d", f.Device.ID)
	var missing []string
	suspended := 0
	for _, j := range f.Jobs {
		if !j.Job.Enabled {
			continue
		}
		if j.Job.State == wire.JobStateSuspendedByMode {
			suspended++
			continue
		}
		if j.Job.State != wire.JobStateActive {
			continue
		}
		for _, m := range j.Job.RequiredModules {
			if m != wire.ModuleCore && !f.Modules.Enabled(m) && !slices.Contains(missing, m) {
				missing = append(missing, m)
			}
		}
	}
	if len(missing) > 0 {
		return check(wire.ReadinessCheckModulesHealthy, wire.ReadinessStatusCritical,
			"Backup jobs need modules that are not enabled: "+strings.Join(missing, ", ")+".", link)
	}
	var failed []string
	for _, m := range f.Modules.Modules {
		if m.State == wire.ModuleStateFailed {
			failed = append(failed, m.ID)
		}
	}
	switch {
	case len(failed) > 0:
		return check(wire.ReadinessCheckModulesHealthy, wire.ReadinessStatusWarning, "Modules failed: "+strings.Join(failed, ", ")+".", link)
	case suspended > 0:
		return check(wire.ReadinessCheckModulesHealthy, wire.ReadinessStatusWarning,
			fmt.Sprintf("%d backup jobs are suspended because the device's mode no longer includes their modules.", suspended), link)
	}
	return check(wire.ReadinessCheckModulesHealthy, wire.ReadinessStatusOK, "All modules are healthy.", "")
}
