package api

// Diagnostics API: read-only server, storage and smart processing details
// for administrators, reachable with an admin session or a diagnostics API
// key ("Authorization: Bearer vdk_..."). Keys are created under
// Administration > API keys; they only work for GET requests under
// /api/v1/diag/ and never return secrets (tokens, keys, password hashes,
// KEK material, secret settings).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/logring"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/smart"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	apiKeyPrefix = "vdk_"
	diagPath     = "/api/v1/diag/"
	// ScopeDiagnostics: read-only diagnostics.
	scopeDiagnostics = "diagnostics:read"
)

// authAPIKey resolves a diagnostics key. Keys are refused anywhere but the
// read-only diagnostics routes.
func (s *Server) authAPIKey(r *http.Request) (*Principal, error) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || !strings.HasPrefix(tok, apiKeyPrefix) {
		return nil, nil
	}
	k, err := s.st.APIKeyByHash(r.Context(), auth.HashToken(tok))
	if err != nil {
		return nil, errf(401, "invalid_api_key", "The API key is not valid, has expired or was revoked")
	}
	if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, diagPath) {
		return nil, errf(403, "api_key_scope", "API keys only allow read-only diagnostics (GET %s…)", diagPath)
	}
	u, err := s.st.UserByID(r.Context(), k.UserID)
	if err != nil || u.Disabled || u.Role != "admin" {
		return nil, errf(401, "invalid_api_key", "The API key's administrator account is no longer active")
	}
	if s.shouldTouch("k:" + strconv.FormatInt(k.ID, 10)) {
		s.st.TouchAPIKey(r.Context(), k.ID, s.clientIP(r))
	}
	return &Principal{User: u, APIKey: k}, nil
}

// requireDiag allows administrators (session) and diagnostics keys.
func (s *Server) requireDiag(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principal(r)
		switch {
		case p != nil && p.APIKey != nil:
		case p != nil && p.Session != nil && p.User.Role == "admin":
		case p == nil:
			writeErr(w, r, errf(401, "unauthenticated", "Sign in as an administrator or use a diagnostics API key"))
			return
		default:
			writeErr(w, r, errf(403, "forbidden", "Administrators only"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// mountDiag registers the diagnostics routes (outside the user groups, so
// API keys reach them and nothing else).
func (s *Server) mountDiag(r chi.Router) {
	r.Route("/diag", func(r chi.Router) {
		r.Use(s.requireDiag)
		r.Get("/", s.handleDiagIndex)
		r.Get("/system", s.handleDiagSystem)
		r.Get("/logs", s.handleDiagLogs)
		r.Get("/smart", s.handleDiagSmart)
		r.Get("/smart/queues/{id}", s.handleDiagSmartQueue)
		r.Get("/smart/items/{id}/benchmark", s.handleDiagSmartBenchmark)
		// Existing read-only administration views.
		r.Get("/admin/overview", s.handleAdminOverview)
		r.Get("/admin/health", s.handleAdminHealth)
		r.Get("/admin/storage", s.handleAdminStorage)
		r.Get("/admin/devices", s.handleAdminDevices)
		r.Get("/admin/transfers", s.handleAdminTransfers)
		r.Get("/admin/uploads", s.handleAdminUploads)
		r.Get("/admin/audit", s.handleAdminAudit)
		r.Get("/admin/update", s.handleAdminUpdate)
	})
}

func (s *Server) mountAPIKeys(rt routes) {
	rt.Admin.Get("/admin/api-keys", s.handleAPIKeys)
	rt.Admin.Post("/admin/api-keys", s.handleAPIKeyCreate)
	rt.Admin.Delete("/admin/api-keys/{id}", s.handleAPIKeyRevoke)
}

func (s *Server) handleDiagIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"version": Version, "endpoints": []string{
		"GET /api/v1/diag/system", "GET /api/v1/diag/logs?lines=500&level=INFO&q=", "GET /api/v1/diag/smart",
		"GET /api/v1/diag/smart/queues/{id}", "GET /api/v1/diag/smart/items/{id}/benchmark",
		"GET /api/v1/diag/admin/overview", "GET /api/v1/diag/admin/health?deep=1", "GET /api/v1/diag/admin/storage",
		"GET /api/v1/diag/admin/devices", "GET /api/v1/diag/admin/transfers", "GET /api/v1/diag/admin/uploads",
		"GET /api/v1/diag/admin/audit", "GET /api/v1/diag/admin/update",
	}})
}

// --- API keys ---

func (s *Server) handleAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.st.APIKeys(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"keys": keys})
}

func (s *Server) handleAPIKeyCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		Name string `json:"name"`
		Days int    `json:"days"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 80 {
		writeErr(w, r, errf(400, "invalid_name", "Give the key a name (up to 80 characters)"))
		return
	}
	if req.Days < 1 || req.Days > 365 {
		writeErr(w, r, errf(400, "invalid_expiry", "Keys expire after 1-365 days"))
		return
	}
	tok := apiKeyPrefix + auth.RandomToken(32)
	k, err := s.st.CreateAPIKey(r.Context(), store.APIKey{UserID: u.ID, Name: req.Name, Prefix: tok[:12], Scope: scopeDiagnostics,
		ExpiresAt: time.Now().Add(time.Duration(req.Days) * 24 * time.Hour).UnixMilli()}, auth.HashToken(tok))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "admin.api_key_created", req.Name, fmt.Sprintf("diagnostics (read-only), expires in %d days", req.Days), true)
	// The key itself is shown once; only its hash is stored.
	writeJSON(w, 201, map[string]any{"key": k, "token": tok})
}

func (s *Server) handleAPIKeyRevoke(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.RevokeAPIKey(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "admin.api_key_revoked", strconv.FormatInt(id, 10), "", true)
	w.WriteHeader(204)
}

// --- system ---

// readTrim reads a small /proc or /sys file.
func readTrim(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// containerLimits reports the cgroup CPU and memory limits the server runs
// under (Docker, LXC), which /proc/cpuinfo and /proc/meminfo may not show.
func containerLimits() map[string]any {
	out := map[string]any{}
	// cgroup v2
	if v := readTrim("/sys/fs/cgroup/cpu.max"); v != "" {
		out["cpu_max"] = v
		if q, p, ok := strings.Cut(v, " "); ok && q != "max" {
			qn, _ := strconv.ParseFloat(q, 64)
			pn, _ := strconv.ParseFloat(p, 64)
			if pn > 0 {
				out["cpu_limit_cores"] = qn / pn
			}
		}
	}
	for k, f := range map[string]string{
		"memory_max": "/sys/fs/cgroup/memory.max", "memory_current": "/sys/fs/cgroup/memory.current", "memory_high": "/sys/fs/cgroup/memory.high",
		"memory_swap_max": "/sys/fs/cgroup/memory.swap.max", "memory_events": "/sys/fs/cgroup/memory.events", "cpu_stat": "/sys/fs/cgroup/cpu.stat",
		"cpu_pressure": "/sys/fs/cgroup/cpu.pressure", "memory_pressure": "/sys/fs/cgroup/memory.pressure", "io_pressure": "/sys/fs/cgroup/io.pressure",
		"cpuset": "/sys/fs/cgroup/cpuset.cpus.effective",
	} {
		if v := readTrim(f); v != "" {
			out[k] = v
		}
	}
	// cgroup v1
	if q := readTrim("/sys/fs/cgroup/cpu/cpu.cfs_quota_us"); q != "" {
		out["cpu_cfs_quota_us"], out["cpu_cfs_period_us"] = q, readTrim("/sys/fs/cgroup/cpu/cpu.cfs_period_us")
	}
	if m := readTrim("/sys/fs/cgroup/memory/memory.limit_in_bytes"); m != "" {
		out["memory_limit_v1"], out["memory_usage_v1"] = m, readTrim("/sys/fs/cgroup/memory/memory.usage_in_bytes")
	}
	return out
}

func (s *Server) handleDiagSystem(w http.ResponseWriter, r *http.Request) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	host := hostResources()
	host["gomaxprocs"] = runtime.GOMAXPROCS(0)
	host["loadavg"] = readTrim("/proc/loadavg")
	host["pressure_cpu"] = readTrim("/proc/pressure/cpu")
	host["pressure_io"] = readTrim("/proc/pressure/io")
	host["pressure_memory"] = readTrim("/proc/pressure/memory")
	host["kernel"] = readTrim("/proc/sys/kernel/osrelease")
	if b, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		var mounts []string
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) > 9 && (strings.HasPrefix(f[4], "/state") || strings.HasPrefix(f[4], "/storage") || f[4] == "/" || f[4] == "/tmp") {
				sep := 0
				for i, x := range f {
					if x == "-" {
						sep = i
					}
				}
				if sep > 0 && sep+2 < len(f) {
					mounts = append(mounts, f[4]+" "+f[sep+1]+" "+f[sep+2])
				}
			}
		}
		host["mounts"] = mounts
	}
	tools := s.media.tools
	writeJSON(w, 200, map[string]any{
		"version": Version, "go": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH,
		"uptime_seconds": int64(time.Since(s.started).Seconds()), "goroutines": runtime.NumGoroutine(),
		"memory":    map[string]any{"heap_alloc": ms.HeapAlloc, "heap_sys": ms.HeapSys, "sys": ms.Sys, "num_gc": ms.NumGC},
		"host":      host,
		"container": containerLimits(),
		"ffmpeg":    map[string]any{"available": tools.Available(), "ffmpeg": tools.FFmpeg, "ffprobe": tools.FFprobe},
		"state_dir": s.cfg.StateDir,
	})
}

func (s *Server) handleDiagLogs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	n, _ := strconv.Atoi(q.Get("lines"))
	if n <= 0 || n > 5000 {
		n = 500
	}
	var lvl slog.Level
	if v := q.Get("level"); v != "" {
		_ = lvl.UnmarshalText([]byte(v))
	} else {
		lvl = slog.LevelDebug
	}
	writeJSON(w, 200, map[string]any{"lines": logring.Lines(n, lvl, q.Get("q"))})
}

// --- smart processing ---

func (s *Server) handleDiagSmart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	ws, werr := s.workerStatus(ctx, true)
	worker := map[string]any{"configured": s.smartWorker() != nil, "url": s.smartSettings().WorkerURL, "env_url": s.cfg.SmartWorkerURL, "token_set": s.cfg.SmartToken != "", "status": ws}
	if werr != nil {
		worker["error"] = werr.Error()
	}
	run := s.smartRun()
	run.mu.Lock()
	running := []smartJob{}
	for _, j := range run.running {
		running = append(running, *j)
	}
	run.mu.Unlock()
	type queueRow struct {
		ID      int64                  `json:"id"`
		UserID  int64                  `json:"user_id"`
		Name    string                 `json:"name"`
		State   string                 `json:"state"`
		Rev     int64                  `json:"filter_rev"`
		Created int64                  `json:"created_at"`
		Updated int64                  `json:"updated_at"`
		Count   store.SmartCounters    `json:"counters"`
		Tally   *store.SmartQueueTally `json:"tally"`
		Filter  json.RawMessage        `json:"filter"`
	}
	var queues []queueRow
	rows, err := s.st.DB.QueryContext(ctx, "SELECT id, user_id, name, state, filter_rev, created_at, updated_at, filter, analysis_ms, render_ms, failures, generated_count FROM smart_queues ORDER BY id DESC LIMIT 200")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for rows.Next() {
		var q queueRow
		var f string
		if rows.Scan(&q.ID, &q.UserID, &q.Name, &q.State, &q.Rev, &q.Created, &q.Updated, &f, &q.Count.AnalysisMS, &q.Count.RenderMS, &q.Count.Failures, &q.Count.GeneratedCount) == nil {
			q.Filter = json.RawMessage(f)
			queues = append(queues, q)
		}
	}
	rows.Close()
	for i := range queues {
		queues[i].Tally, _ = s.st.SmartTally(ctx, queues[i].ID)
	}
	// Timing of finished analyses: what each recording took.
	type timing struct {
		ItemID    int64   `json:"item_id"`
		QueueID   int64   `json:"queue_id"`
		Name      string  `json:"name"`
		Size      int64   `json:"size"`
		Duration  float64 `json:"duration"`
		State     string  `json:"state"`
		Stage     string  `json:"stage"`
		MS        int64   `json:"processing_ms"`
		Detectors string  `json:"detectors"`
		Attempts  int     `json:"attempts"`
		Updated   int64   `json:"updated_at"`
	}
	var recent, active []timing
	collect := func(q string, dst *[]timing) {
		rows, err := s.st.DB.QueryContext(ctx, q)
		if err != nil {
			return
		}
		defer rows.Close()
		for rows.Next() {
			var t timing
			if rows.Scan(&t.ItemID, &t.QueueID, &t.Name, &t.Size, &t.Duration, &t.State, &t.Stage, &t.MS, &t.Detectors, &t.Attempts, &t.Updated) == nil {
				*dst = append(*dst, t)
			}
		}
	}
	cols := "SELECT id, queue_id, name, size, duration, state, stage, processing_ms, detectors, attempts, updated_at FROM smart_items"
	collect(cols+" WHERE analysed_at IS NOT NULL ORDER BY analysed_at DESC LIMIT 50", &recent)
	collect(cols+" WHERE state IN ('analysing', 'rendering') ORDER BY updated_at", &active)
	errs := []store.SmartError{}
	erows, err := s.st.DB.QueryContext(ctx, "SELECT id, item_id, at, stage, message FROM smart_errors ORDER BY at DESC LIMIT 200")
	if err == nil {
		for erows.Next() {
			var e store.SmartError
			if erows.Scan(&e.ID, &e.ItemID, &e.At, &e.Stage, &e.Message) == nil {
				errs = append(errs, e)
			}
		}
		erows.Close()
	}
	an := s.smartAnalyzer()
	writeJSON(w, 200, map[string]any{
		"settings": s.smartSettings(), "worker": worker, "availability": s.smartAvailability(ctx).Detectors(),
		"running": running, "active_items": active, "recent_analyses": recent, "queues": queues, "errors": errs,
		"cache":       map[string]any{"dir": an.Dir, "bytes": dirSize(an.Dir), "temp": an.Temp, "temp_bytes": dirSize(an.Temp)},
		"timings_log": logring.Lines(200, slog.LevelDebug, "smart"),
	})
}

func dirSize(p string) int64 {
	var n int64
	_ = filepath.WalkDir(p, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func (s *Server) handleDiagSmartQueue(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q, err := s.st.SmartQueueByID(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items, total, err := s.st.SmartItems(r.Context(), q.UserID, q.ID, store.SmartItemsQuery{})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	views := make([]itemView, 0, len(items))
	for i := range items {
		views = append(views, newItemView(&items[i]))
	}
	errs, _ := s.st.SmartErrors(r.Context(), q.ID, 500)
	v, _ := s.queueView(r.Context(), q, true)
	writeJSON(w, 200, map[string]any{"queue": v, "items": views, "total": total, "errors": errs})
}

var benchMu sync.Mutex

// handleDiagSmartBenchmark times the steps of analysing one recording
// without changing anything: opening the stored file, reading it, probing
// it and decoding its audio as analysis does.
func (s *Server) handleDiagSmartBenchmark(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !benchMu.TryLock() {
		writeErr(w, r, errf(409, "busy", "A benchmark is already running"))
		return
	}
	defer benchMu.Unlock()
	var it store.SmartItem
	err = s.st.DB.QueryRowContext(r.Context(), "SELECT user_id, sha256, name, size FROM smart_items WHERE id = ?", id).Scan(&it.UserID, &it.SHA256, &it.Name, &it.Size)
	if err != nil {
		writeErr(w, r, store.ErrNotFound)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	out := map[string]any{"name": it.Name, "size": it.Size}
	step := func(name string, f func() error) bool {
		t := time.Now()
		err := f()
		ms := time.Since(t).Milliseconds()
		res := map[string]any{"ms": ms}
		if err != nil {
			res["error"] = err.Error()
		}
		out[name] = res
		return err == nil
	}
	var f *os.File
	if !step("open", func() error { f, err = s.openBlob(ctx, it.UserID, it.SHA256); return err }) {
		writeJSON(w, 200, out)
		return
	}
	defer f.Close()
	if fi, err := f.Stat(); err == nil {
		out["file_size"] = fi.Size()
	}
	step("read_file", func() error {
		buf := make([]byte, 4<<20)
		t := time.Now()
		n, err := io.CopyBuffer(io.Discard, io.LimitReader(f, 1<<30), buf)
		if secs := time.Since(t).Seconds(); secs > 0 {
			out["read_mb_per_s"] = float64(n) / secs / (1 << 20)
		}
		out["read_bytes"] = n
		return err
	})
	step("probe", func() error {
		info, err := s.media.tools.Probe(ctx, f)
		if info != nil {
			out["media"] = info
		}
		return err
	})
	set := s.smartSettings().analysis()
	var samples int64
	step("decode_audio", func() error {
		cw := &countWriter{}
		err := smart.DecodeAudio(ctx, s.media.tools, f, media.RunOptions{Threads: set.Threads, HWAccel: set.HWAccel, Nice: set.Nice}, cw)
		samples = cw.n / 2
		return err
	})
	out["audio_seconds"] = float64(samples) / smart.SampleRate
	step("decode_audio_normal_priority", func() error {
		cw := &countWriter{}
		return smart.DecodeAudio(ctx, s.media.tools, f, media.RunOptions{Threads: set.Threads}, cw)
	})
	writeJSON(w, 200, out)
}

type countWriter struct{ n int64 }

func (c *countWriter) Write(p []byte) (int, error) { c.n += int64(len(p)); return len(p), nil }
