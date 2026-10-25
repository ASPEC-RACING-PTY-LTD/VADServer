package api

// Smart processing: administrator settings (processing intensity, the
// inference worker, model setup) and the status shown to users.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/smart"
)

// smartSettings are the server-wide smart processing settings.
type smartSettings struct {
	// Enabled: queued recordings are processed. Off keeps queues and
	// reviews available but nothing is analysed or rendered.
	Enabled bool `json:"enabled"`
	// WorkerURL overrides VAULTDOCK_SMART_WORKER_URL (the worker's token
	// only comes from the environment).
	WorkerURL           string  `json:"worker_url"`
	AnalysisConcurrency int     `json:"analysis_concurrency"`
	RenderConcurrency   int     `json:"render_concurrency"`
	FFmpegThreads       int     `json:"ffmpeg_threads"`
	LowPriority         bool    `json:"low_priority"`
	HWAccel             string  `json:"hwaccel"`
	FrameRate           float64 `json:"frame_rate"`
	FrameWidth          int     `json:"frame_width"`
	OCREvery            float64 `json:"ocr_every"`
	ChunkSeconds        int     `json:"chunk_seconds"`
	Language            string  `json:"language"`
	// MaxMinutes: longer recordings are not analysed (0: no limit).
	MaxMinutes int `json:"max_minutes"`
	// UseLanguageModel lets the worker's local language model interpret
	// natural-language filters (rules are used otherwise).
	UseLanguageModel bool `json:"use_language_model"`
}

func defaultSmartSettings() smartSettings {
	return smartSettings{Enabled: true, AnalysisConcurrency: 1, RenderConcurrency: 1, LowPriority: true, FrameRate: 1, FrameWidth: 640,
		OCREvery: 2, ChunkSeconds: 600, UseLanguageModel: true}
}

func (c *smartSettings) validate() error {
	switch {
	case c.AnalysisConcurrency < 1 || c.AnalysisConcurrency > 16:
		return errors.New("recordings analysed at once must be 1-16")
	case c.RenderConcurrency < 1 || c.RenderConcurrency > 8:
		return errors.New("clips rendered at once must be 1-8")
	case c.FFmpegThreads < 0 || c.FFmpegThreads > 128:
		return errors.New("FFmpeg threads must be 0-128 (0: automatic)")
	case c.FrameRate < 0.1 || c.FrameRate > 5:
		return errors.New("frames per second must be 0.1-5")
	case c.FrameWidth < 224 || c.FrameWidth > 1920:
		return errors.New("frame width must be 224-1920 pixels")
	case c.OCREvery < 0.5 || c.OCREvery > 60:
		return errors.New("on-screen text must be read every 0.5-60 seconds")
	case c.ChunkSeconds < 60 || c.ChunkSeconds > 3600:
		return errors.New("audio chunks must be 60-3600 seconds")
	case c.MaxMinutes < 0 || c.MaxMinutes > 24*60:
		return errors.New("the longest recording must be 0-1440 minutes")
	case len(c.Language) > 8:
		return errors.New("use a language code such as en")
	}
	switch c.HWAccel {
	case "", "auto", "cuda", "vaapi", "qsv", "videotoolbox":
	default:
		return errors.New("unknown hardware acceleration")
	}
	if c.WorkerURL != "" {
		u, err := url.Parse(c.WorkerURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return errors.New("the worker address must be an http(s) URL")
		}
	}
	return nil
}

const smartSettingsKey = "smart"

func (s *Server) smartSettings() smartSettings {
	r := s.smartRun()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.settings != nil {
		return *r.settings
	}
	v := defaultSmartSettings()
	if raw, ok, err := s.st.GetSetting(context.Background(), smartSettingsKey); err == nil && ok {
		_ = json.Unmarshal([]byte(raw), &v)
	}
	r.settings = &v
	return v
}

func (s *Server) saveSmartSettings(ctx context.Context, v smartSettings) error {
	b, _ := json.Marshal(v)
	if err := s.st.SetSetting(ctx, smartSettingsKey, string(b)); err != nil {
		return err
	}
	r := s.smartRun()
	r.mu.Lock()
	r.settings = &v
	r.workerAt = time.Time{} // ask the (possibly new) worker again
	r.mu.Unlock()
	r.poke()
	return nil
}

func (c smartSettings) analysis() smart.Settings {
	nice := 0
	if c.LowPriority {
		nice = 10
	}
	return smart.Settings{Threads: c.FFmpegThreads, HWAccel: c.HWAccel, Nice: nice, FrameRate: c.FrameRate, FrameWidth: c.FrameWidth,
		OCREvery: c.OCREvery, ChunkSeconds: float64(c.ChunkSeconds), Language: c.Language}
}

// worker returns the configured worker (nil when none).
func (s *Server) smartWorker() *smart.Worker {
	u := s.smartSettings().WorkerURL
	if u == "" {
		u = s.cfg.SmartWorkerURL
	}
	r := s.smartRun()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.worker == nil || r.worker.URL != strings.TrimRight(u, "/") {
		r.worker = smart.NewWorker(u, s.cfg.SmartToken)
		r.texts = nil
		if r.worker != nil {
			r.texts = smart.NewTextCache(r.worker)
		}
	}
	return r.worker
}

// workerStatus asks the worker (cached for 20 s).
func (s *Server) workerStatus(ctx context.Context, fresh bool) (*smart.WorkerStatus, error) {
	w := s.smartWorker()
	if w == nil {
		return nil, smart.ErrNoWorker
	}
	r := s.smartRun()
	r.mu.Lock()
	if !fresh && time.Since(r.workerAt) < 20*time.Second {
		st, err := r.workerSt, r.workerErr
		r.mu.Unlock()
		return st, err
	}
	r.mu.Unlock()
	st, err := w.Status(ctx)
	r.mu.Lock()
	r.workerSt, r.workerErr, r.workerAt = st, err, time.Now()
	r.mu.Unlock()
	return st, err
}

// smartAvailability is what can be measured right now.
func (s *Server) smartAvailability(ctx context.Context) smart.Availability {
	a := smart.Availability{FFmpeg: s.media.tools.Available(), Models: map[string]bool{}}
	if st, err := s.workerStatus(ctx, false); err == nil {
		a.Models = st.Installed()
	}
	return a
}

// resources reports the host's load (Linux /proc; best effort).
func hostResources() map[string]any {
	out := map[string]any{"cpus": runtime.NumCPU()}
	if b, err := os.ReadFile("/proc/loadavg"); err == nil {
		if f := strings.Fields(string(b)); len(f) >= 3 {
			l1, _ := strconv.ParseFloat(f[0], 64)
			l5, _ := strconv.ParseFloat(f[1], 64)
			out["load1"], out["load5"] = l1, l5
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			k, v, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			n, _ := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(v), " kB"), 10, 64)
			switch k {
			case "MemTotal":
				out["mem_total"] = n * 1024
			case "MemAvailable":
				out["mem_available"] = n * 1024
			}
		}
	}
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	out["server_heap"] = m.HeapAlloc
	return out
}

func (s *Server) handleSmartStatus(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	set := s.smartSettings()
	ws, werr := s.workerStatus(r.Context(), r.URL.Query().Get("fresh") == "1")
	worker := map[string]any{"configured": s.smartWorker() != nil, "reachable": werr == nil}
	if werr != nil && !errors.Is(werr, smart.ErrNoWorker) {
		worker["error"] = werr.Error()
	}
	if ws != nil {
		worker["version"], worker["device"], worker["models"] = ws.Version, ws.Device, ws.Models
		worker["busy"], worker["rss"], worker["cpu_percent"] = ws.Busy, ws.RSS, ws.CPUPercent
		if u.Role == "admin" {
			worker["threads"], worker["gpu"] = ws.Threads, ws.GPU
		}
	}
	avail := s.smartAvailability(r.Context())
	out := map[string]any{
		"enabled":   set.Enabled,
		"ffmpeg":    s.media.tools.Available(),
		"worker":    worker,
		"detectors": avail.Detectors(),
		"running":   s.smartRun().runningFor(u.ID),
	}
	if u.Role == "admin" {
		out["resources"] = hostResources()
		out["settings"] = set
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleSmartAdminSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"settings": s.smartSettings(), "env_worker_url": s.cfg.SmartWorkerURL, "token_set": s.cfg.SmartToken != ""})
}

func (s *Server) handleSmartAdminSave(w http.ResponseWriter, r *http.Request) {
	var v smartSettings
	if err := decode(r, &v); err != nil {
		writeErr(w, r, err)
		return
	}
	v.WorkerURL = strings.TrimRight(strings.TrimSpace(v.WorkerURL), "/")
	v.Language = strings.ToLower(strings.TrimSpace(v.Language))
	if err := v.validate(); err != nil {
		writeErr(w, r, errf(400, "invalid_settings", "%s", err.Error()))
		return
	}
	if err := s.saveSmartSettings(r.Context(), v); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, 0, "admin.smart_settings", "smart processing", fmt.Sprintf("enabled=%v analysis=%d render=%d", v.Enabled, v.AnalysisConcurrency, v.RenderConcurrency), true)
	writeJSON(w, 200, map[string]any{"settings": v})
}

// handleSmartModels installs or removes worker models (explicit setup:
// the worker downloads them once; inference then runs offline).
func (s *Server) handleSmartModels(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Install []string `json:"install"`
		Remove  []string `json:"remove"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	wk := s.smartWorker()
	if wk == nil {
		writeErr(w, r, errf(409, "no_worker", "No smart processing worker is configured"))
		return
	}
	valid := func(names []string) error {
		for _, n := range names {
			if len(n) > 40 || strings.TrimFunc(n, func(c rune) bool { return c == '-' || c == '.' || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') }) != "" {
				return errf(400, "invalid_model", "Invalid model name")
			}
		}
		return nil
	}
	if err := valid(append(append([]string{}, req.Install...), req.Remove...)); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Install) > 0 {
		if err := wk.Install(r.Context(), req.Install); err != nil {
			writeErr(w, r, errf(502, "worker_error", "%s", err.Error()))
			return
		}
		s.activity(r, 0, "admin.smart_models", strings.Join(req.Install, ", "), "model download started", true)
	}
	if len(req.Remove) > 0 {
		if err := wk.Remove(r.Context(), req.Remove); err != nil {
			writeErr(w, r, errf(502, "worker_error", "%s", err.Error()))
			return
		}
		s.activity(r, 0, "admin.smart_models", strings.Join(req.Remove, ", "), "models removed", true)
	}
	st, _ := s.workerStatus(r.Context(), true)
	writeJSON(w, 200, map[string]any{"worker": st})
}

// smartRunner holds the smart processing runtime state.
type smartRunner struct {
	mu        sync.Mutex
	settings  *smartSettings
	worker    *smart.Worker
	texts     *smart.TextCache
	workerSt  *smart.WorkerStatus
	workerErr error
	workerAt  time.Time

	wake    chan struct{}
	running map[int64]*smartJob // by item id
	an      *smart.Analyzer
}

type smartJob struct {
	ItemID   int64   `json:"item_id"`
	QueueID  int64   `json:"queue_id"`
	UserID   int64   `json:"-"`
	Name     string  `json:"name"`
	Kind     string  `json:"kind"` // analysis or render
	Stage    string  `json:"stage"`
	Progress float64 `json:"progress"`
	Started  int64   `json:"started_at"`
	cancel   context.CancelFunc
}

func newSmartRunner() *smartRunner {
	return &smartRunner{wake: make(chan struct{}, 1), running: map[int64]*smartJob{}}
}

func (s *Server) smartRun() *smartRunner { return s.smart }

func (r *smartRunner) poke() {
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *smartRunner) runningFor(userID int64) []smartJob {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []smartJob{}
	for _, j := range r.running {
		if j.UserID == userID {
			out = append(out, *j)
		}
	}
	return out
}

// cancelItems stops running work on items (pause, cancel, removal).
func (r *smartRunner) cancelItems(match func(j *smartJob) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, j := range r.running {
		if match(j) {
			j.cancel()
		}
	}
}

func (r *smartRunner) texter() *smart.TextCache {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.texts
}

func logSmart(msg string, args ...any) { slog.Info("smart processing: "+msg, args...) }
