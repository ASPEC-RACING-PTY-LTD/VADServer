package nodeagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
)

// ExitRestart is the exit code after a self-update; systemd restarts the
// service (Restart=always) with the new binary.
const ExitRestart = 3

// Status is written to data/status.json for "vad-node status" and the installer.
type Status struct {
	State         string `json:"state"` // starting, pending, approved, rejected, expired, revoked, error
	NodeID        int64  `json:"node_id,omitempty"`
	Message       string `json:"message,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	Server        string `json:"server"`
	Version       string `json:"version"`
	LastHeartbeat int64  `json:"last_heartbeat,omitempty"`
	UpdatedAt     int64  `json:"updated_at"`
}

type joinState struct {
	ID        int64  `json:"id"`
	PollToken string `json:"poll_token"`
	ExpiresAt int64  `json:"expires_at"`
}

// Agent runs one node.
type Agent struct {
	cfg       Config
	log       *slog.Logger
	http      *http.Client
	started   time.Time
	mu        sync.Mutex // guards status, lastErr and locations
	status    Status
	lastErr   string
	locations *locations
}

func (a *Agent) current() Status {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.status
}

func (a *Agent) setLastErr(msg string) {
	a.mu.Lock()
	a.lastErr = msg
	a.mu.Unlock()
}

func (a *Agent) lastError() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

func New(cfg Config, log *slog.Logger) *Agent {
	return &Agent{cfg: cfg, log: log, http: &http.Client{Timeout: 30 * time.Second}, started: time.Now()}
}

func (a *Agent) setStatus(st Status) {
	st.Server, st.Version, st.UpdatedAt = a.cfg.ServerURL, Version, time.Now().UnixMilli()
	a.mu.Lock()
	a.status = st
	a.mu.Unlock()
	b, _ := json.MarshalIndent(st, "", "  ")
	_ = os.MkdirAll(a.cfg.DataDir(), 0o700)
	tmp := filepath.Join(a.cfg.DataDir(), "status.json.tmp")
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, filepath.Join(a.cfg.DataDir(), "status.json"))
	}
}

// ReadStatus returns the last status the service wrote.
func ReadStatus(c Config) (Status, error) {
	var st Status
	b, err := os.ReadFile(filepath.Join(c.DataDir(), "status.json"))
	if err != nil {
		return st, err
	}
	return st, json.Unmarshal(b, &st)
}

// Run joins (if needed) and then reports heartbeats until ctx ends. It
// returns ExitRestart-worthy errUpdated after a self-update.
func (a *Agent) Run(ctx context.Context) error {
	a.setStatus(Status{State: "starting"})
	a.serveLocal(ctx)
	for ctx.Err() == nil {
		id, secret, ok, err := LoadSecret(a.cfg)
		if err != nil {
			a.setStatus(Status{State: "error", Message: err.Error()})
			a.log.Error("node secret unavailable", "err", err)
			if !sleep(ctx, time.Minute) {
				break
			}
			continue
		}
		if !ok {
			if err := a.join(ctx); err != nil {
				if ctx.Err() != nil {
					break
				}
				a.log.Warn("join", "err", err)
				if !sleep(ctx, 30*time.Second) {
					break
				}
			}
			continue
		}
		err = a.heartbeats(ctx, id, secret)
		if errors.Is(err, ErrUpdated) {
			return err
		}
		if errors.Is(err, errRevoked) {
			a.log.Warn("the server revoked this node; asking to join again")
			_ = ForgetSecret(a.cfg)
			a.setStatus(Status{State: "revoked", NodeID: id, Message: "The server revoked this node. A new join request is sent; approve it in the admin console."})
			if !sleep(ctx, time.Minute) {
				break
			}
		}
	}
	return ctx.Err()
}

// ErrUpdated means the binary was replaced; the process should exit with ExitRestart.
var ErrUpdated = errors.New("updated; restart required")

var errRevoked = errors.New("node revoked")

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func (a *Agent) do(ctx context.Context, method, path string, body any, hdr map[string]string, out any) (int, error) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.cfg.ServerURL+path, rd)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vad-node/"+Version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		_ = json.Unmarshal(b, &e)
		msg := e.Error.Message
		if msg == "" {
			msg = strings.TrimSpace(string(b))
		}
		return resp.StatusCode, fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, msg)
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return resp.StatusCode, fmt.Errorf("%s %s: unexpected response: %w", method, path, err)
		}
	}
	return resp.StatusCode, nil
}

func (a *Agent) joinFile() string { return filepath.Join(a.cfg.DataDir(), "join.json") }

// join sends a join request (or resumes the pending one) and polls until the
// admin decides. On approval the secret is stored encrypted.
func (a *Agent) join(ctx context.Context) error {
	var js joinState
	if b, err := os.ReadFile(a.joinFile()); err == nil {
		_ = json.Unmarshal(b, &js)
	}
	if js.ID == 0 || time.Now().UnixMilli() > js.ExpiresAt {
		host, _ := os.Hostname()
		var jr nodeapi.JoinResponse
		if _, err := a.do(ctx, "POST", "/api/v1/node/join", nodeapi.JoinRequest{
			Name: a.cfg.Name, Hostname: host, IP: a.joinIP(), OS: runtime.GOOS, Arch: runtime.GOARCH,
			Port: a.cfg.Port, Proxy: a.cfg.Proxy, PublicURL: a.cfg.PublicURL, Version: Version,
		}, nil, &jr); err != nil {
			a.setStatus(Status{State: "error", Message: "Could not reach the server: " + err.Error()})
			return err
		}
		js = joinState{ID: jr.ID, PollToken: jr.PollToken, ExpiresAt: jr.ExpiresAt}
		b, _ := json.Marshal(js)
		_ = os.MkdirAll(a.cfg.DataDir(), 0o700)
		if err := os.WriteFile(a.joinFile(), b, 0o600); err != nil {
			return err
		}
		a.log.Info("join request sent; approve it in the admin console (Admin > Nodes)", "request", js.ID, "server", a.cfg.ServerURL)
	}
	a.setStatus(Status{State: "pending", NodeID: js.ID, ExpiresAt: js.ExpiresAt, Message: "Waiting for an administrator to approve this node in Admin > Nodes."})
	for {
		if !sleep(ctx, 5*time.Second) {
			return ctx.Err()
		}
		var st nodeapi.JoinStatus
		code, err := a.do(ctx, "GET", fmt.Sprintf("/api/v1/node/join/%d", js.ID), nil, map[string]string{"X-Poll-Token": js.PollToken}, &st)
		if code == 404 {
			_ = os.Remove(a.joinFile())
			return fmt.Errorf("the join request is gone from the server; sending a new one")
		}
		if err != nil {
			continue // transient; keep polling
		}
		switch st.Status {
		case "pending":
			continue
		case "approved":
			if st.Secret == "" {
				return fmt.Errorf("approved without a secret")
			}
			if err := SaveSecret(a.cfg, js.ID, st.Secret); err != nil {
				a.setStatus(Status{State: "error", NodeID: js.ID, Message: "Approved, but the secret could not be stored: " + err.Error()})
				return err
			}
			_ = os.Remove(a.joinFile())
			a.log.Info("node approved", "node", js.ID)
			a.setStatus(Status{State: "approved", NodeID: js.ID, Message: "Approved and connected."})
			return nil
		case "rejected":
			_ = os.Remove(a.joinFile())
			a.setStatus(Status{State: "rejected", NodeID: js.ID, Message: "An administrator rejected this node. Restart the service (systemctl restart vad-node) to ask again."})
			a.log.Warn("join request rejected")
			<-ctx.Done()
			return ctx.Err()
		case "expired":
			_ = os.Remove(a.joinFile())
			a.setStatus(Status{State: "expired", NodeID: js.ID, Message: "The request expired before it was approved; sending a new one."})
			return fmt.Errorf("join request expired")
		}
	}
}

// joinIP is the address the installer recorded (VAD_NODE_IP), shown to the
// admin who approves the request; detected when the installer left it empty.
func (a *Agent) joinIP() string {
	if a.cfg.IP != "" {
		return a.cfg.IP
	}
	return a.localIP()
}

// localIP is the address of the interface used to reach the internet.
func (a *Agent) localIP() string {
	conn, err := net.Dial("udp", "1.1.1.1:53")
	if err != nil {
		return a.cfg.IP
	}
	defer conn.Close()
	if ua, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return ua.IP.String()
	}
	return a.cfg.IP
}

func (a *Agent) heartbeats(ctx context.Context, id int64, secret string) error {
	auth := map[string]string{"Authorization": fmt.Sprintf("%s %d.%s", nodeapi.AuthScheme, id, secret)}
	interval := 30 * time.Second
	jctx, stopJobs := context.WithCancel(ctx)
	defer stopJobs()
	go a.jobLoop(jctx, auth)
	for {
		host, _ := os.Hostname()
		total, free := diskSpace(a.cfg.Dir)
		var resp nodeapi.HeartbeatResponse
		code, err := a.do(ctx, "POST", "/api/v1/node/heartbeat", nodeapi.Heartbeat{
			Name: a.cfg.Name, Hostname: host, IP: a.localIP(), Version: Version, OS: runtime.GOOS, Arch: runtime.GOARCH,
			Port: a.cfg.Port, PublicURL: a.cfg.PublicURL, DiskTotal: total, DiskFree: free,
			UptimeSeconds: int64(time.Since(a.started).Seconds()), LastError: a.lastError(),
			TerminalEnabled: a.cfg.Terminal, StorageRoots: a.cfg.StorageRoots, Locations: a.locationStatus(),
		}, auth, &resp)
		switch {
		case code == 401:
			return errRevoked
		case err != nil:
			a.log.Warn("heartbeat failed", "err", err)
			a.setStatus(Status{State: "approved", NodeID: id, Message: "Server unreachable: " + err.Error(), LastHeartbeat: a.current().LastHeartbeat})
		default:
			a.setStatus(Status{State: "approved", NodeID: id, Message: "Connected.", LastHeartbeat: time.Now().UnixMilli()})
			if resp.PollInterval >= 5 {
				interval = time.Duration(resp.PollInterval) * time.Second
			}
			if resp.Update != nil {
				if err := a.update(ctx, auth, *resp.Update); err != nil {
					a.setLastErr("update to " + resp.Update.Version + " failed: " + err.Error())
					a.log.Error("self-update failed", "version", resp.Update.Version, "err", err)
				} else {
					return ErrUpdated
				}
			}
		}
		if !sleep(ctx, interval) {
			return ctx.Err()
		}
	}
}

// update downloads the agent from the server (only), checks its size and
// SHA-256 and replaces the running binary. An attempt for the same version
// is not repeated within an hour, so a broken build cannot loop.
func (a *Agent) update(ctx context.Context, auth map[string]string, u nodeapi.Update) error {
	marker := filepath.Join(a.cfg.DataDir(), "update-attempt")
	if b, err := os.ReadFile(marker); err == nil {
		var last struct {
			Version string `json:"version"`
			At      int64  `json:"at"`
		}
		if json.Unmarshal(b, &last) == nil && last.Version == u.Version && time.Since(time.UnixMilli(last.At)) < time.Hour {
			return nil
		}
	}
	b, _ := json.Marshal(map[string]any{"version": u.Version, "at": time.Now().UnixMilli()})
	_ = os.WriteFile(marker, b, 0o600)

	if !strings.HasPrefix(u.URL, "/api/v1/node/agent/") {
		return fmt.Errorf("refusing update URL %q (updates come from this server only)", u.URL)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, "GET", a.cfg.ServerURL+u.URL, nil)
	if err != nil {
		return err
	}
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	tmp := exe + ".new"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, u.Size+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n != u.Size {
		err = fmt.Errorf("size %d, expected %d", n, u.Size)
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != strings.ToLower(u.SHA256) {
		err = fmt.Errorf("checksum mismatch")
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, exe); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	a.log.Info("updated; restarting", "from", Version, "to", u.Version)
	return nil
}

// serveLocal answers /healthz and /status on the configured listen address
// (never the secret).
func (a *Agent) serveLocal(ctx context.Context) {
	if a.cfg.Listen == "" {
		return
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok\n")) })
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": a.cfg.Name, "state": a.current().State, "version": Version})
	})
	srv := &http.Server{Addr: a.cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			a.log.Error("status endpoint", "listen", a.cfg.Listen, "err", err)
			a.setLastErr("status endpoint: " + err.Error())
		}
	}()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
}
