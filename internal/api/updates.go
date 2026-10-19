package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/release"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/version"
)

const (
	updateCheckInterval = 12 * time.Hour
	updateForceMinGap   = time.Minute
	maxReleaseNotes     = 8000
)

// UpdateStatus is what the admin console shows about new releases.
type UpdateStatus struct {
	Enabled     bool       `json:"enabled"`
	Repo        string     `json:"repo"`
	Current     string     `json:"current"`
	Latest      string     `json:"latest,omitempty"`
	Available   bool       `json:"available"`
	URL         string     `json:"url,omitempty"`
	Notes       string     `json:"notes,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	CheckedAt   *time.Time `json:"checked_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	// AutoUpdate reflects the installer's scheduled updates ("on" or "off").
	AutoUpdate string `json:"auto_update"`
	Command    string `json:"upgrade_command"`
	// Helper: the installer's control job on the host is running, so the
	// admin console can install updates and change automatic updates
	// itself (update_control.go).
	Helper       bool        `json:"helper"`
	HelperSeenAt int64       `json:"helper_seen_at,omitempty"`
	Job          *ControlJob `json:"job,omitempty"`
}

type updateChecker struct {
	client  *release.Client
	repo    string
	enabled bool

	mu     sync.Mutex
	status UpdateStatus
	last   time.Time
}

func newUpdateChecker(repo string, enabled bool, autoUpdate string) *updateChecker {
	if autoUpdate != "on" {
		autoUpdate = "off"
	}
	return &updateChecker{
		client:  &release.Client{UserAgent: "VaultDock-Server/" + Version},
		repo:    repo,
		enabled: enabled && repo != "",
		status: UpdateStatus{
			Enabled:    enabled && repo != "",
			Repo:       repo,
			Current:    Version,
			AutoUpdate: autoUpdate,
			// The released installer: it also sets up the host helper that
			// lets the console install later updates (an older local copy
			// does not).
			Command: upgradeCommand(repo),
		},
	}
}

func (u *updateChecker) snapshot() UpdateStatus {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.status
}

// check queries GitHub. Calls closer together than updateForceMinGap reuse
// the previous result, so the admin "check now" button cannot hammer the API.
func (u *updateChecker) check(ctx context.Context) UpdateStatus {
	if !u.enabled {
		return u.snapshot()
	}
	u.mu.Lock()
	if time.Since(u.last) < updateForceMinGap {
		s := u.status
		u.mu.Unlock()
		return s
	}
	u.last = time.Now()
	u.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := u.client.Latest(ctx, u.repo)
	now := time.Now().UTC()

	u.mu.Lock()
	defer u.mu.Unlock()
	u.status.CheckedAt = &now
	switch {
	case errors.Is(err, release.ErrNoRelease):
		u.status.Error = ""
		u.status.Latest, u.status.Available, u.status.URL, u.status.Notes, u.status.PublishedAt = "", false, "", "", nil
	case err != nil:
		u.status.Error = err.Error()
		slog.Warn("update check failed", "err", err)
	default:
		was := u.status.Latest
		u.status.Error = ""
		u.status.Latest = rel.Version()
		u.status.Available = version.Newer(rel.Version(), Version)
		u.status.URL = rel.HTMLURL
		notes := rel.Body
		if len(notes) > maxReleaseNotes {
			notes = notes[:maxReleaseNotes] + "\n..."
		}
		u.status.Notes = notes
		pub := rel.PublishedAt
		u.status.PublishedAt = &pub
		if u.status.Available && was != rel.Version() {
			slog.Info("a new VaultDock release is available", "current", Version, "latest", rel.Version(), "url", rel.HTMLURL)
		}
	}
	return u.status
}

// RunUpdateChecks checks for new releases shortly after start and then every
// 12 hours until ctx ends. It never installs anything: the server runs in a
// locked-down container, and upgrades are applied by the host installer.
func (s *Server) RunUpdateChecks(ctx context.Context) {
	if !s.updates.enabled {
		return
	}
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.updates.check(ctx)
			t.Reset(updateCheckInterval)
		}
	}
}

func (s *Server) handleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.withControl(s.updates.snapshot()))
}

func (s *Server) handleAdminUpdateCheck(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.withControl(s.updates.check(r.Context())))
}

func upgradeCommand(repo string) string {
	if repo == "" {
		return "~/VaultDock/app/install.sh upgrade"
	}
	return "curl -fsSL https://github.com/" + repo + "/releases/latest/download/install.sh | bash -s -- upgrade"
}
