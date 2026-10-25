package api

// Smart processing API: detection presets and filters, queues of
// recordings, the review of suggested segments and the file browser's
// view of processed recordings. Cleanup and statistics are in
// smart_cleanup.go, server settings in smart_settings.go and the
// background work in smart_runner.go.
//
// Everything is scoped to the signed-in user: queues only hold the user's
// own files and every lookup checks the owner.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/smart"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
	"github.com/go-chi/chi/v5"
)

// maxQueueItems bounds one queue (and one add).
const maxQueueItems = 5000

func (s *Server) mountSmart(rt routes) {
	rt.Sess.Get("/smart/status", s.handleSmartStatus)
	rt.Sess.Get("/smart/catalog", s.handleSmartCatalog)
	rt.Sess.Post("/smart/check", s.handleSmartCheck)
	rt.Sess.Post("/smart/interpret", s.handleSmartInterpret)

	rt.Sess.Get("/smart/presets", s.handleSmartPresets)
	rt.Sess.Post("/smart/presets", s.handleSmartPresetSave)
	rt.Sess.Put("/smart/presets/{id}", s.handleSmartPresetSave)
	rt.Sess.Delete("/smart/presets/{id}", s.handleSmartPresetDelete)

	rt.Sess.Get("/smart/queues", s.handleSmartQueues)
	rt.Sess.Post("/smart/queues", s.handleSmartQueueCreate)
	rt.Sess.Get("/smart/queues/{id}", s.handleSmartQueueGet)
	rt.Sess.Patch("/smart/queues/{id}", s.handleSmartQueuePatch)
	rt.Sess.Delete("/smart/queues/{id}", s.handleSmartQueueDelete)
	rt.Sess.Put("/smart/queues/{id}/filter", s.handleSmartQueueFilter)
	rt.Sess.Post("/smart/queues/{id}/actions/{action}", s.handleSmartQueueAction)
	rt.Sess.Get("/smart/queues/{id}/items", s.handleSmartQueueItems)
	rt.Sess.Post("/smart/queues/{id}/items", s.handleSmartQueueAdd)
	rt.Sess.Post("/smart/queues/{id}/items/remove", s.handleSmartQueueRemove)
	rt.Sess.Get("/smart/queues/{id}/errors", s.handleSmartQueueErrors)

	rt.Sess.Get("/smart/review/next", s.handleSmartReviewNext)
	rt.Sess.Get("/smart/items/{id}", s.handleSmartItem)
	rt.Sess.Get("/smart/items/{id}/transcript", s.handleSmartTranscript)
	rt.Sess.Post("/smart/items/{id}/actions/{action}", s.handleSmartItemAction)
	rt.Sess.Post("/smart/items/{id}/segments", s.handleSmartSegmentCreate)
	rt.Sess.Patch("/smart/segments/{id}", s.handleSmartSegmentPatch)
	rt.Sess.Delete("/smart/segments/{id}", s.handleSmartSegmentDelete)
	rt.Sess.Post("/smart/segments/{id}/split", s.handleSmartSegmentSplit)
	rt.Sess.Post("/smart/segments/merge", s.handleSmartSegmentMerge)
	rt.Sess.Post("/smart/segments/bulk", s.handleSmartSegmentBulk)

	rt.Sess.Get("/smart/preferences", s.handleSmartPrefs)
	rt.Sess.Put("/smart/preferences", s.handleSmartPrefsSave)

	rt.Sess.Get("/smart/search", s.handleSmartSearch)
	rt.Sess.Post("/smart/files", s.handleSmartFiles)
	rt.Sess.Get("/smart/files/folder", s.handleSmartFolderFiles)

	rt.Sess.Get("/smart/cleanup", s.handleSmartCleanupList)
	rt.Sess.Post("/smart/cleanup", s.handleSmartCleanup)
	rt.Sess.Get("/smart/stats", s.handleSmartStats)

	rt.Admin.Get("/admin/smart/settings", s.handleSmartAdminSettings)
	rt.Admin.Put("/admin/smart/settings", s.handleSmartAdminSave)
	rt.Admin.Post("/admin/smart/models", s.handleSmartModels)
}

// --- catalog, filters and presets ---

func (s *Server) handleSmartCatalog(w http.ResponseWriter, r *http.Request) {
	avail := s.smartAvailability(r.Context())
	sounds := make([]string, 0, len(smart.SoundAliases))
	for k := range smart.SoundAliases {
		sounds = append(sounds, k)
	}
	sort.Strings(sounds)
	writeJSON(w, 200, map[string]any{
		"kinds":     smart.Kinds(),
		"sounds":    sounds,
		"detectors": avail.Detectors(),
		"context":   smart.DefaultContext(),
		"regions":   map[string]smart.Rect{"score": smart.DefaultScoreRegion, "killfeed": smart.DefaultKillfeedRegion},
	})
}

// handleSmartCheck validates a filter and reports what each condition
// needs and whether it can run on this server.
func (s *Server) handleSmartCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filter smart.Filter `json:"filter"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := req.Filter.Validate(); err != nil {
		writeErr(w, r, errf(400, "invalid_filter", "%s", err.Error()))
		return
	}
	writeJSON(w, 200, map[string]any{"filter": req.Filter, "components": nonNilComponents(smart.Check(&req.Filter, s.smartAvailability(r.Context())))})
}

func nonNilComponents(c []smart.Component) []smart.Component {
	if c == nil {
		return []smart.Component{}
	}
	return c
}

// handleSmartInterpret turns a description into a filter: with the
// worker's local language model when the administrator enabled it, with
// the built-in rules otherwise (or when the model fails). Either way the
// result lists what each part needs and what cannot be found reliably.
func (s *Server) handleSmartInterpret(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Description string `json:"description"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	desc := strings.TrimSpace(req.Description)
	if desc == "" || len(desc) > 1000 {
		writeErr(w, r, errf(400, "invalid_description", "Describe what to find in 1-1000 characters"))
		return
	}
	avail := s.smartAvailability(r.Context())
	if s.smartSettings().UseLanguageModel {
		if wk := s.smartWorker(); wk != nil {
			f, model, notes, err := wk.Plan(r.Context(), desc, plannerGuide())
			if err == nil && f != nil {
				if p, ok := smart.FinishPlan(desc, model, *f, notes, avail); ok {
					writeJSON(w, 200, p)
					return
				}
			}
			p := smart.Interpret(desc, avail)
			if err != nil && !errors.Is(err, smart.ErrNoPlanner) {
				p.Notes = append(p.Notes, "The local language model could not interpret this; the built-in rules were used instead.")
			}
			writeJSON(w, 200, p)
			return
		}
	}
	writeJSON(w, 200, smart.Interpret(desc, avail))
}

// plannerGuide tells the language model which conditions exist.
func plannerGuide() string {
	var b strings.Builder
	b.WriteString("Conditions (leaf nodes, field kind):\n")
	for _, k := range smart.Kinds() {
		fmt.Fprintf(&b, "- %s (%s): %s", k.Kind, k.Category, k.Description)
		if len(k.Modes) > 0 {
			fmt.Fprintf(&b, " modes: %s.", strings.Join(k.Modes, ", "))
		}
		if len(k.LabelSet) > 0 {
			fmt.Fprintf(&b, " label: one of %s.", strings.Join(k.LabelSet, ", "))
		}
		b.WriteString("\n")
	}
	sounds := make([]string, 0, len(smart.SoundAliases))
	for k := range smart.SoundAliases {
		sounds = append(sounds, k)
	}
	sort.Strings(sounds)
	b.WriteString("audio_event labels: " + strings.Join(sounds, ", ") + "\n")
	b.WriteString("Combine with op and|or|then|without, children and window (seconds).")
	return b.String()
}

type presetView struct {
	smart.Preset
	Components []smart.Component `json:"components"`
}

func (s *Server) handleSmartPresets(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	avail := s.smartAvailability(r.Context())
	out := []presetView{}
	for _, p := range smart.Builtins() {
		out = append(out, presetView{p, nonNilComponents(smart.Check(&p.Filter, avail))})
	}
	saved, err := s.st.SmartPresets(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for _, sp := range saved {
		p := userPreset(sp)
		out = append(out, presetView{p, nonNilComponents(smart.Check(&p.Filter, avail))})
	}
	writeJSON(w, 200, map[string]any{"presets": out})
}

func userPreset(sp store.SmartPreset) smart.Preset {
	return smart.Preset{ID: strconv.FormatInt(sp.ID, 10), Name: sp.Name, Description: sp.Description, Filter: parseFilter(sp.Filter),
		Source: sp.Source, CreatedAt: sp.CreatedAt, UpdatedAt: sp.UpdatedAt}
}

// smartPreset finds a built-in ("builtin:x") or saved (numeric) preset.
func (s *Server) smartPreset(ctx context.Context, userID int64, id string) (smart.Preset, error) {
	if p, ok := smart.Builtin(id); ok {
		return p, nil
	}
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil {
		return smart.Preset{}, errf(404, "preset_not_found", "Unknown preset %q", id)
	}
	sp, err := s.st.SmartPreset(ctx, userID, n)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return smart.Preset{}, errf(404, "preset_not_found", "Unknown preset %q", id)
		}
		return smart.Preset{}, err
	}
	return userPreset(*sp), nil
}

func (s *Server) handleSmartPresetSave(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		Name        string       `json:"name"`
		Description string       `json:"description"`
		Source      string       `json:"source"`
		Filter      smart.Filter `json:"filter"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	var id int64
	if raw := chiParam(r, "id"); raw != "" {
		if strings.HasPrefix(raw, "builtin:") {
			writeErr(w, r, errf(409, "builtin_preset", "Built-in presets cannot be changed; save a copy under a new name"))
			return
		}
		v, err := idParam(r, "id")
		if err != nil {
			writeErr(w, r, err)
			return
		}
		id = v
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 || len(req.Description) > 1000 || len(req.Source) > 1000 {
		writeErr(w, r, errf(400, "invalid_preset", "Give the preset a name (up to 120 characters)"))
		return
	}
	if err := req.Filter.Validate(); err != nil {
		writeErr(w, r, errf(400, "invalid_filter", "%s", err.Error()))
		return
	}
	b, _ := json.Marshal(req.Filter)
	sp, err := s.st.SaveSmartPreset(r.Context(), u.ID, store.SmartPreset{ID: id, Name: req.Name, Description: strings.TrimSpace(req.Description), Filter: string(b), Source: req.Source})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p := userPreset(*sp)
	writeJSON(w, 200, presetView{p, nonNilComponents(smart.Check(&p.Filter, s.smartAvailability(r.Context())))})
}

func (s *Server) handleSmartPresetDelete(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	if strings.HasPrefix(chiParam(r, "id"), "builtin:") {
		writeErr(w, r, errf(409, "builtin_preset", "Built-in presets cannot be deleted"))
		return
	}
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.DeleteSmartPreset(r.Context(), u.ID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

// composeFilter joins presets and a custom filter into one. Rule ids are
// prefixed so rules of different presets never clash; the segment
// settings come from ctx, else the custom filter, else the first preset.
func composeFilter(presets []smart.Preset, custom *smart.Filter, ctx *smart.Context) (smart.Filter, error) {
	f := smart.Filter{Context: smart.DefaultContext()}
	ctxSet := false
	for i, p := range presets {
		for _, rule := range p.Filter.Rules {
			rule.ID = fmt.Sprintf("p%d.%s", i+1, rule.ID)
			f.Rules = append(f.Rules, rule)
		}
		if !ctxSet && (len(p.Filter.Rules) > 0 || len(presets) == 1) {
			f.Context, ctxSet = p.Filter.Context, true
		}
	}
	if custom != nil {
		for _, rule := range custom.Rules {
			if rule.ID != "" {
				rule.ID = "c." + rule.ID
			}
			f.Rules = append(f.Rules, rule)
		}
		if custom.Context != (smart.Context{}) {
			f.Context = custom.Context
		}
	}
	if ctx != nil {
		f.Context = *ctx
	}
	if err := f.Validate(); err != nil {
		return f, errf(400, "invalid_filter", "%s", err.Error())
	}
	return f, nil
}

// --- queues ---

type queueView struct {
	*store.SmartQueue
	Presets    []string               `json:"presets"`
	Output     smartOutputSettings    `json:"output"`
	Filter     smart.Filter           `json:"filter"`
	Tally      *store.SmartQueueTally `json:"tally,omitempty"`
	Components []smart.Component      `json:"components,omitempty"`
	Running    []smartJob             `json:"running,omitempty"`
}

func (s *Server) queueView(ctx context.Context, q *store.SmartQueue, detail bool) (queueView, error) {
	v := queueView{SmartQueue: q, Presets: []string{}, Output: parseOutput(q.Output), Filter: parseFilter(q.Filter)}
	_ = json.Unmarshal([]byte(q.Presets), &v.Presets)
	t, err := s.st.SmartTally(ctx, q.ID)
	if err != nil {
		return v, err
	}
	v.Tally = t
	if detail {
		v.Components = nonNilComponents(smart.Check(&v.Filter, s.smartAvailability(ctx)))
		v.Running = []smartJob{}
		for _, j := range s.smartRun().runningFor(q.UserID) {
			if j.QueueID == q.ID {
				v.Running = append(v.Running, j)
			}
		}
	}
	return v, nil
}

func (s *Server) handleSmartQueues(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var states []string
	if v := r.URL.Query().Get("state"); v != "" {
		states = strings.Split(v, ",")
	}
	qs, err := s.st.SmartQueues(r.Context(), u.ID, states)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]queueView, 0, len(qs))
	for i := range qs {
		v, err := s.queueView(r.Context(), &qs[i], false)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"queues": out})
}

// queueSources picks recordings for a queue.
type queueSources struct {
	FileIDs   []int64 `json:"file_ids"`
	FolderIDs []int64 `json:"folder_ids"`
	Recursive bool    `json:"recursive"`
}

// resolveVideos finds the user's videos among files and folders. Folders
// contribute their videos (and those of subfolders when recursive).
func (s *Server) resolveVideos(ctx context.Context, user *store.User, src queueSources) ([]store.SmartItem, error) {
	seen := map[int64]bool{}
	var out []store.SmartItem
	add := func(n *store.Node) error {
		if !isVideo(n) || seen[n.ID] {
			return nil
		}
		if len(out) >= maxQueueItems {
			return errf(400, "too_many_files", "A queue can hold at most %d recordings", maxQueueItems)
		}
		seen[n.ID] = true
		p, err := s.st.NodePath(ctx, user.ID, n.ID)
		if err != nil {
			return err
		}
		out = append(out, store.SmartItem{NodeID: n.ID, SHA256: n.SHA256, Name: n.Name, Path: p, Size: n.Size})
		return nil
	}
	for _, id := range src.FileIDs {
		n, err := s.st.Node(ctx, user.ID, id)
		if err != nil {
			return nil, err
		}
		if n.IsDir {
			src.FolderIDs = append(src.FolderIDs, n.ID)
			continue
		}
		if !isVideo(n) {
			return nil, errf(400, "not_video", "%s is not a video", n.Name)
		}
		if err := add(n); err != nil {
			return nil, err
		}
	}
	for _, id := range src.FolderIDs {
		dir, err := s.st.Node(ctx, user.ID, id)
		if err != nil {
			return nil, err
		}
		if !dir.IsDir {
			return nil, errf(400, "not_folder", "%s is not a folder", dir.Name)
		}
		if src.Recursive {
			nodes, err := s.st.Descendants(ctx, user.ID, dir.ID, 100000)
			if err != nil {
				return nil, err
			}
			for i := range nodes {
				if err := add(&nodes[i]); err != nil {
					return nil, err
				}
			}
			continue
		}
		for off := 0; ; off += 5000 {
			nodes, total, err := s.st.ListChildren(ctx, user.ID, dir.ID, store.ListOptions{Kind: "video", Limit: 5000, Offset: off})
			if err != nil {
				return nil, err
			}
			for i := range nodes {
				if err := add(&nodes[i]); err != nil {
					return nil, err
				}
			}
			if off+len(nodes) >= total || len(nodes) == 0 {
				break
			}
		}
	}
	return out, nil
}

func (s *Server) handleSmartQueueCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		Name        string               `json:"name"`
		Description string               `json:"description"`
		Presets     []string             `json:"preset_ids"`
		Filter      *smart.Filter        `json:"filter"`
		Context     *smart.Context       `json:"context"`
		Output      *smartOutputSettings `json:"output"`
		AutoApprove bool                 `json:"auto_approve"`
		Paused      bool                 `json:"paused"`
		queueSources
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 120 || len(req.Description) > 2000 {
		writeErr(w, r, errf(400, "invalid_name", "Give the queue a name (up to 120 characters)"))
		return
	}
	q, err := s.buildQueue(r.Context(), u, req.Presets, req.Filter, req.Context, req.Output)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := s.resolveVideos(r.Context(), u, req.queueSources)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q.Name, q.Description, q.AutoApprove, q.State = req.Name, strings.TrimSpace(req.Description), req.AutoApprove, "active"
	if req.Paused {
		q.State = "paused"
	}
	created, err := s.st.CreateSmartQueue(r.Context(), *q)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	added, err := s.st.AddSmartItems(r.Context(), u.ID, created.ID, items)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "smart.queue_created", created.Name, fmt.Sprintf("%d recording(s)", added), false)
	s.smartRun().poke()
	created, _ = s.st.SmartQueue(r.Context(), u.ID, created.ID)
	v, err := s.queueView(r.Context(), created, true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, v)
}

// buildQueue checks a queue's filter and output settings.
func (s *Server) buildQueue(ctx context.Context, u *store.User, presetIDs []string, custom *smart.Filter, sctx *smart.Context, out *smartOutputSettings) (*store.SmartQueue, error) {
	if len(presetIDs) == 0 && (custom == nil || len(custom.Rules) == 0) {
		return nil, errf(400, "no_filter", "Choose at least one preset or add a condition")
	}
	if len(presetIDs) > 12 {
		return nil, errf(400, "too_many_presets", "Use at most 12 presets")
	}
	var presets []smart.Preset
	for _, id := range presetIDs {
		p, err := s.smartPreset(ctx, u.ID, id)
		if err != nil {
			return nil, err
		}
		presets = append(presets, p)
	}
	f, err := composeFilter(presets, custom, sctx)
	if err != nil {
		return nil, err
	}
	if len(f.Rules) == 0 {
		return nil, errf(400, "no_filter", "The chosen presets have no conditions; add at least one")
	}
	o := defaultOutput()
	if out != nil {
		o = *out
	}
	if err := o.validate(); err != nil {
		return nil, err
	}
	if o.Destination == "folder" {
		if n, err := s.st.Node(ctx, u.ID, o.FolderID); err != nil || !n.IsDir {
			return nil, errf(404, "folder_missing", "The destination folder does not exist")
		}
	}
	fb, _ := json.Marshal(f)
	ob, _ := json.Marshal(o)
	pb, _ := json.Marshal(nonNilStrings(presetIDs))
	return &store.SmartQueue{UserID: u.ID, Filter: string(fb), Output: string(ob), Presets: string(pb)}, nil
}

func (s *Server) userQueue(r *http.Request) (*store.SmartQueue, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	return s.st.SmartQueue(r.Context(), principal(r).User.ID, id)
}

func (s *Server) handleSmartQueueGet(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.queueView(r.Context(), q, true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) handleSmartQueuePatch(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Name        *string              `json:"name"`
		Description *string              `json:"description"`
		Output      *smartOutputSettings `json:"output"`
		AutoApprove *bool                `json:"auto_approve"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 120 {
			writeErr(w, r, errf(400, "invalid_name", "Give the queue a name (up to 120 characters)"))
			return
		}
		q.Name = n
	}
	if req.Description != nil {
		if len(*req.Description) > 2000 {
			writeErr(w, r, errf(400, "invalid_description", "The description is too long"))
			return
		}
		q.Description = strings.TrimSpace(*req.Description)
	}
	if req.Output != nil {
		if err := req.Output.validate(); err != nil {
			writeErr(w, r, err)
			return
		}
		if req.Output.Destination == "folder" {
			if n, err := s.st.Node(r.Context(), q.UserID, req.Output.FolderID); err != nil || !n.IsDir {
				writeErr(w, r, errf(404, "folder_missing", "The destination folder does not exist"))
				return
			}
		}
		b, _ := json.Marshal(req.Output)
		q.Output = string(b)
	}
	if req.AutoApprove != nil {
		q.AutoApprove = *req.AutoApprove
	}
	if err := s.st.UpdateSmartQueue(r.Context(), q.UserID, *q); err != nil {
		writeErr(w, r, err)
		return
	}
	s.handleSmartQueueGet(w, r)
}

// handleSmartQueueFilter replaces a queue's filter. Recordings waiting
// for review are evaluated again from their stored measurements (only
// newly needed detectors are measured); reviewed ones keep their
// decisions.
func (s *Server) handleSmartQueueFilter(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Presets []string       `json:"preset_ids"`
		Filter  *smart.Filter  `json:"filter"`
		Context *smart.Context `json:"context"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	nq, err := s.buildQueue(r.Context(), u, req.Presets, req.Filter, req.Context, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetSmartQueueFilter(r.Context(), u.ID, q.ID, nq.Filter, nq.Presets); err != nil {
		writeErr(w, r, err)
		return
	}
	s.smartRun().poke()
	s.handleSmartQueueGet(w, r)
}

func (s *Server) handleSmartQueueDelete(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.smartRun().cancelItems(func(j *smartJob) bool { return j.QueueID == q.ID })
	if err := s.st.DeleteSmartQueue(r.Context(), q.UserID, q.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, q.UserID, "smart.queue_deleted", q.Name, "queue records removed; files were not changed", false)
	w.WriteHeader(204)
}

// handleSmartQueueAction: pause, resume, cancel, retry, archive, reopen,
// duplicate.
func (s *Server) handleSmartQueueAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	stop := func() { s.smartRun().cancelItems(func(j *smartJob) bool { return j.QueueID == q.ID }) }
	setState := func(st string) bool {
		if err := s.st.SetSmartQueueState(ctx, q.UserID, q.ID, st); err != nil {
			writeErr(w, r, err)
			return false
		}
		return true
	}
	action := chiParam(r, "action")
	switch action {
	case "pause":
		if q.State != "active" {
			writeErr(w, r, errf(409, "not_active", "Only an active queue can be paused"))
			return
		}
		if !setState("paused") {
			return
		}
		stop()
		// Interrupted work goes back to its queue: analyses resume from
		// their checkpoints, renders start again.
		_, _ = s.st.SetQueueItemsState(context.WithoutCancel(ctx), q.ID, []string{"analysing"}, "pending")
		_, _ = s.st.SetQueueItemsState(context.WithoutCancel(ctx), q.ID, []string{"rendering"}, "reviewed")
	case "resume":
		if q.State != "paused" && q.State != "completed" {
			writeErr(w, r, errf(409, "not_paused", "Only a paused queue can be resumed"))
			return
		}
		if !setState("active") {
			return
		}
	case "cancel":
		if q.State == "archived" || q.State == "cancelled" {
			writeErr(w, r, errf(409, "not_running", "The queue is not running"))
			return
		}
		if !setState("cancelled") {
			return
		}
		stop()
		bg := context.WithoutCancel(ctx)
		_, _ = s.st.SetQueueItemsState(bg, q.ID, []string{"pending", "analysing"}, "cancelled")
		// Approved work keeps its decisions; it renders when reopened.
		_, _ = s.st.SetQueueItemsState(bg, q.ID, []string{"rendering"}, "reviewed")
	case "retry":
		if _, err := s.st.SetQueueItemsState(ctx, q.ID, []string{"failed", "cancelled"}, "pending"); err != nil {
			writeErr(w, r, err)
			return
		}
		if err := s.st.ResetFailedSmartOutputs(ctx, q.ID); err != nil {
			writeErr(w, r, err)
			return
		}
		if q.State != "active" && q.State != "paused" && !setState("active") {
			return
		}
	case "archive":
		if q.State == "active" {
			writeErr(w, r, errf(409, "still_active", "Pause, cancel or finish the queue before archiving it"))
			return
		}
		if !setState("archived") {
			return
		}
	case "reopen":
		if q.State != "archived" && q.State != "completed" && q.State != "cancelled" {
			writeErr(w, r, errf(409, "not_closed", "The queue is already open"))
			return
		}
		if q.State == "cancelled" {
			_, _ = s.st.SetQueueItemsState(ctx, q.ID, []string{"cancelled"}, "pending")
		}
		if !setState("active") {
			return
		}
	case "duplicate":
		s.duplicateQueue(w, r, q)
		return
	case "restart":
		stop()
		shas, err := s.st.RestartSmartQueue(context.WithoutCancel(ctx), q.UserID, q.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		// From scratch: measurements are taken again, not reused.
		an := s.smartAnalyzer()
		for _, h := range shas {
			_ = an.Forget(q.UserID, h)
		}
	default:
		writeErr(w, r, errf(404, "unknown_action", "Unknown queue action"))
		return
	}
	s.activity(r, q.UserID, "smart.queue_"+action, q.Name, "", false)
	s.smartRun().poke()
	s.handleSmartQueueGet(w, r)
}

// duplicateQueue makes a new queue with the same recordings and settings
// (analysis is reused from the cache; review decisions are not copied).
func (s *Server) duplicateQueue(w http.ResponseWriter, r *http.Request, q *store.SmartQueue) {
	ctx := r.Context()
	items, _, err := s.st.SmartItems(ctx, q.UserID, q.ID, store.SmartItemsQuery{})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	nq := *q
	nq.ID, nq.State, nq.Counters = 0, "paused", store.SmartCounters{}
	nq.Name = strings.TrimSpace(q.Name + " (copy)")
	if len(nq.Name) > 120 {
		nq.Name = nq.Name[:120]
	}
	created, err := s.st.CreateSmartQueue(ctx, nq)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var keep []store.SmartItem
	for _, it := range items {
		if it.OriginalState == "kept" && it.State != "gone" {
			keep = append(keep, it)
		}
	}
	if _, err := s.st.AddSmartItems(ctx, q.UserID, created.ID, keep); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, q.UserID, "smart.queue_duplicated", created.Name, "from "+q.Name, false)
	created, _ = s.st.SmartQueue(ctx, q.UserID, created.ID)
	v, err := s.queueView(ctx, created, true)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, v)
}

type itemView struct {
	*store.SmartItem
	Warnings  []string `json:"warnings"`
	Detectors []string `json:"detectors"`
	Segments  *struct {
		Suggested int `json:"suggested"`
		Approved  int `json:"approved"`
		Rejected  int `json:"rejected"`
	} `json:"segments,omitempty"`
}

func newItemView(it *store.SmartItem) itemView {
	v := itemView{SmartItem: it, Warnings: []string{}, Detectors: []string{}}
	_ = json.Unmarshal([]byte(it.Warnings), &v.Warnings)
	_ = json.Unmarshal([]byte(it.Detectors), &v.Detectors)
	if v.Warnings == nil {
		v.Warnings = []string{}
	}
	if v.Detectors == nil {
		v.Detectors = []string{}
	}
	return v
}

func (s *Server) handleSmartQueueItems(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	qs := r.URL.Query()
	o := store.SmartItemsQuery{}
	if v := qs.Get("state"); v != "" {
		o.States = strings.Split(v, ",")
	}
	o.Limit, _ = strconv.Atoi(qs.Get("limit"))
	o.Offset, _ = strconv.Atoi(qs.Get("offset"))
	items, total, err := s.st.SmartItems(r.Context(), q.UserID, q.ID, o)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	counts, err := s.segmentCounts(r.Context(), q.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	running := map[int64]smartJob{}
	for _, j := range s.smartRun().runningFor(q.UserID) {
		running[j.ItemID] = j
	}
	out := make([]itemView, 0, len(items))
	for i := range items {
		it := &items[i]
		if j, ok := running[it.ID]; ok {
			it.Stage, it.Progress = j.Stage, j.Progress
		}
		v := newItemView(it)
		c := counts[it.ID]
		v.Segments = &c
		out = append(out, v)
	}
	writeJSON(w, 200, map[string]any{"items": out, "total": total})
}

func (s *Server) segmentCounts(ctx context.Context, queueID int64) (map[int64]struct {
	Suggested int `json:"suggested"`
	Approved  int `json:"approved"`
	Rejected  int `json:"rejected"`
}, error) {
	out := map[int64]struct {
		Suggested int `json:"suggested"`
		Approved  int `json:"approved"`
		Rejected  int `json:"rejected"`
	}{}
	rows, err := s.st.DB.QueryContext(ctx, "SELECT item_id, state, COUNT(*) FROM smart_segments WHERE queue_id = ? GROUP BY item_id, state", queueID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st string
		var n int
		if err := rows.Scan(&id, &st, &n); err != nil {
			return nil, err
		}
		c := out[id]
		switch st {
		case "suggested":
			c.Suggested = n
		case "approved":
			c.Approved = n
		case "rejected":
			c.Rejected = n
		}
		out[id] = c
	}
	return out, rows.Err()
}

func (s *Server) handleSmartQueueAdd(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if q.State == "archived" {
		writeErr(w, r, errf(409, "archived", "Reopen the queue before adding recordings"))
		return
	}
	var req queueSources
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	items, err := s.resolveVideos(r.Context(), u, req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	_, total, err := s.st.SmartItems(r.Context(), u.ID, q.ID, store.SmartItemsQuery{Limit: 1})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if total+len(items) > maxQueueItems {
		writeErr(w, r, errf(400, "too_many_files", "A queue can hold at most %d recordings", maxQueueItems))
		return
	}
	added, err := s.st.AddSmartItems(r.Context(), u.ID, q.ID, items)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.smartRun().poke()
	writeJSON(w, 200, map[string]any{"added": added, "skipped": len(items) - added, "found": len(items)})
}

func (s *Server) handleSmartQueueRemove(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		ItemIDs []int64 `json:"item_ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.ItemIDs) > maxQueueItems {
		writeErr(w, r, errf(400, "too_many", "Too many items"))
		return
	}
	want := map[int64]bool{}
	for _, id := range req.ItemIDs {
		want[id] = true
	}
	s.smartRun().cancelItems(func(j *smartJob) bool { return j.QueueID == q.ID && want[j.ItemID] })
	_, _ = s.st.DB.ExecContext(r.Context(), "UPDATE smart_items SET state = 'pending' WHERE queue_id = ? AND state = 'analysing' AND id IN ("+int64List(req.ItemIDs)+")", q.ID)
	n, err := s.st.RemoveSmartItems(r.Context(), q.UserID, q.ID, req.ItemIDs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"removed": n})
}

// int64List formats ids for an IN clause (integers only, so no
// placeholders are needed).
func int64List(ids []int64) string {
	if len(ids) == 0 {
		return "NULL"
	}
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.FormatInt(id, 10)
	}
	return strings.Join(parts, ",")
}

func (s *Server) handleSmartQueueErrors(w http.ResponseWriter, r *http.Request) {
	q, err := s.userQueue(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	errs, err := s.st.SmartErrors(r.Context(), q.ID, 500)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"errors": errs})
}

// --- review ---

type segmentView struct {
	*store.SmartSegment
	Reasons json.RawMessage `json:"reasons"`
}

func newSegmentView(g *store.SmartSegment) segmentView {
	raw := json.RawMessage(g.Reasons)
	if !json.Valid(raw) {
		raw = json.RawMessage("[]")
	}
	return segmentView{g, raw}
}

func (s *Server) userItem(r *http.Request) (*store.SmartItem, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	return s.st.SmartItem(r.Context(), principal(r).User.ID, id)
}

func (s *Server) handleSmartItem(w http.ResponseWriter, r *http.Request) {
	it, err := s.userItem(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.writeItem(w, r, it)
}

func (s *Server) writeItem(w http.ResponseWriter, r *http.Request, it *store.SmartItem) {
	ctx := r.Context()
	segs, err := s.st.SmartSegments(ctx, it.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	outs, err := s.st.SmartOutputs(ctx, it.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sv := make([]segmentView, 0, len(segs))
	for i := range segs {
		sv = append(sv, newSegmentView(&segs[i]))
	}
	var tl json.RawMessage
	if b, err := os.ReadFile(s.timelinePath(it.ID)); err == nil && json.Valid(b) {
		tl = b
	}
	q, _ := s.st.SmartQueue(ctx, it.UserID, it.QueueID)
	var queue any
	if q != nil {
		queue = map[string]any{"id": q.ID, "name": q.Name, "state": q.State}
	}
	_, nodeErr := s.st.Node(ctx, it.UserID, it.NodeID)
	writeJSON(w, 200, map[string]any{"item": newItemView(it), "segments": sv, "outputs": outs, "timeline": tl, "queue": queue, "file_exists": nodeErr == nil})
}

// handleSmartTranscript returns a recording's timestamped transcript
// (speakers are anonymous numbers).
func (s *Server) handleSmartTranscript(w http.ResponseWriter, r *http.Request) {
	it, err := s.userItem(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	t, err := s.loadTranscript(it)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if t == nil {
		writeJSON(w, 200, map[string]any{"available": false, "utterances": []any{}})
		return
	}
	writeJSON(w, 200, map[string]any{"available": true, "language": t.Language, "model": t.Model, "speakers": t.Speakers, "utterances": t.Utterances})
}

func (s *Server) loadTranscript(it *store.SmartItem) (*smart.Transcript, error) {
	if it.SHA256 == "" {
		return nil, nil
	}
	src := smart.Source{UserID: it.UserID, SHA256: it.SHA256, Info: &media.Info{Duration: it.Duration}}
	a, err := s.smartAnalyzer().Load(src, []string{smart.DTranscript})
	if err != nil {
		return nil, err
	}
	return a.Transcript, nil
}

// handleSmartReviewNext finds the next recording waiting for review
// (after the given item; wraps around), optionally within one queue.
func (s *Server) handleSmartReviewNext(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	qs := r.URL.Query()
	queueID, _ := strconv.ParseInt(qs.Get("queue_id"), 10, 64)
	after, _ := strconv.ParseInt(qs.Get("after"), 10, 64)
	dir := qs.Get("dir")
	q := `SELECT i.id FROM smart_items i JOIN smart_queues q ON q.id = i.queue_id WHERE i.user_id = ? AND i.state = 'ready' AND q.state != 'archived'`
	args := []any{u.ID}
	if queueID > 0 {
		q += " AND i.queue_id = ?"
		args = append(args, queueID)
	}
	order := " ORDER BY i.queue_id, i.position"
	cmp := ">"
	if dir == "prev" {
		order, cmp = " ORDER BY i.queue_id DESC, i.position DESC", "<"
	}
	var id int64
	var err error
	if after > 0 {
		cur, cerr := s.st.SmartItem(r.Context(), u.ID, after)
		if cerr == nil {
			err = s.st.DB.QueryRowContext(r.Context(), q+" AND (i.queue_id, i.position) "+cmp+" (?, ?)"+order+" LIMIT 1", append(args, cur.QueueID, cur.Position)...).Scan(&id)
		}
	}
	if id == 0 {
		err = s.st.DB.QueryRowContext(r.Context(), q+order+" LIMIT 1", args...).Scan(&id)
	}
	if err != nil || id == 0 {
		writeJSON(w, 200, map[string]any{"item": nil})
		return
	}
	var left int
	_ = s.st.DB.QueryRowContext(r.Context(), strings.Replace(q, "SELECT i.id", "SELECT COUNT(*)", 1), args...).Scan(&left)
	it, err := s.st.SmartItem(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"item": newItemView(it), "remaining": left})
}

// handleSmartItemAction: review (decisions done: approved segments are
// rendered), skip (later), reopen (back to review), retry (analyse again).
func (s *Server) handleSmartItemAction(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	it, err := s.userItem(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	switch chiParam(r, "action") {
	case "review":
		var req struct {
			// RejectRest rejects suggestions left undecided.
			RejectRest bool `json:"reject_rest"`
		}
		_ = decodeOptional(r, &req)
		segs, err := s.st.SmartSegments(ctx, it.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		var open []int64
		for _, g := range segs {
			if g.State == "suggested" {
				open = append(open, g.ID)
			}
		}
		if len(open) > 0 && !req.RejectRest {
			writeErr(w, r, errf(409, "undecided", "%d suggestion(s) are not approved or rejected yet", len(open)))
			return
		}
		if _, err := s.st.SetSmartSegmentsState(ctx, it.UserID, open, "rejected"); err != nil {
			writeErr(w, r, err)
			return
		}
		err = s.st.SetSmartItemState(ctx, it.UserID, it.ID, "reviewed", "ready", "skipped", "done", "failed")
	case "skip":
		err = s.st.SetSmartItemState(ctx, it.UserID, it.ID, "skipped", "ready")
	case "reopen":
		err = s.st.SetSmartItemState(ctx, it.UserID, it.ID, "ready", "skipped", "reviewed", "done", "failed")
	case "retry":
		err = s.st.SetSmartItemState(ctx, it.UserID, it.ID, "pending", "failed", "cancelled", "gone", "ready", "skipped")
	default:
		writeErr(w, r, errf(404, "unknown_action", "Unknown action"))
		return
	}
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, errf(409, "wrong_state", "This recording is %s; that action does not apply", it.State))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.smartRun().poke()
	it, err = s.st.SmartItem(ctx, it.UserID, it.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.writeItem(w, r, it)
}

// segmentBounds checks segment times against the recording.
func segmentBounds(it *store.SmartItem, start, end float64) (float64, float64, error) {
	start, end = math.Round(start*1000)/1000, math.Round(end*1000)/1000
	if math.IsNaN(start) || math.IsNaN(end) || start < 0 || end-start < 0.2 {
		return 0, 0, errf(400, "invalid_segment", "A segment must be at least 0.2 seconds long")
	}
	if it.Duration > 0 && end > it.Duration+0.05 {
		return 0, 0, errf(400, "invalid_segment", "The segment ends after the recording")
	}
	if it.Duration > 0 {
		end = math.Min(end, it.Duration)
	}
	return start, end, nil
}

// editable reports whether an item's segments may change (not while its
// clips are rendering).
func editable(it *store.SmartItem) error {
	switch it.State {
	case "rendering":
		return errf(409, "rendering", "Its clips are being rendered; pause the queue to change segments")
	case "pending", "analysing":
		return errf(409, "analysing", "The recording has not been analysed yet")
	}
	return nil
}

func (s *Server) handleSmartSegmentCreate(w http.ResponseWriter, r *http.Request) {
	it, err := s.userItem(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := editable(it); err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Start float64 `json:"start"`
		End   float64 `json:"end"`
		Name  string  `json:"name"`
		State string  `json:"state"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	start, end, err := segmentBounds(it, req.Start, req.End)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if req.State == "" {
		req.State = "approved"
	}
	if !validSegState(req.State) || len(req.Name) > 120 {
		writeErr(w, r, errf(400, "invalid_segment", "Invalid segment"))
		return
	}
	g, err := s.st.CreateSmartSegment(r.Context(), it.UserID, store.SmartSegment{ItemID: it.ID, QueueID: it.QueueID, Start: start, End: end, Score: 1,
		State: req.State, Origin: "manual", Category: "custom", Label: "Manual", Name: strings.TrimSpace(req.Name)})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.segmentsChanged(r.Context(), it)
	writeJSON(w, 201, newSegmentView(g))
}

func validSegState(st string) bool { return st == "suggested" || st == "approved" || st == "rejected" }

// segmentsChanged: a finished recording whose decisions change goes back
// to review (its existing clips stay; new approvals render on review).
func (s *Server) segmentsChanged(ctx context.Context, it *store.SmartItem) {
	if it.State == "done" || it.State == "reviewed" || it.State == "failed" {
		_ = s.st.SetSmartItemState(ctx, it.UserID, it.ID, "ready", it.State)
	}
}

func (s *Server) userSegment(r *http.Request) (*store.SmartSegment, *store.SmartItem, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, nil, err
	}
	uid := principal(r).User.ID
	g, err := s.st.SmartSegment(r.Context(), uid, id)
	if err != nil {
		return nil, nil, err
	}
	it, err := s.st.SmartItem(r.Context(), uid, g.ItemID)
	if err != nil {
		return nil, nil, err
	}
	return g, it, editable(it)
}

func (s *Server) handleSmartSegmentPatch(w http.ResponseWriter, r *http.Request) {
	g, it, err := s.userSegment(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Start *float64 `json:"start"`
		End   *float64 `json:"end"`
		State *string  `json:"state"`
		Name  *string  `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	start, end := g.Start, g.End
	if req.Start != nil {
		start = *req.Start
	}
	if req.End != nil {
		end = *req.End
	}
	if g.Start, g.End, err = segmentBounds(it, start, end); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.State != nil {
		if !validSegState(*req.State) {
			writeErr(w, r, errf(400, "invalid_state", "Invalid state"))
			return
		}
		g.State = *req.State
	}
	if req.Name != nil {
		if len(*req.Name) > 120 {
			writeErr(w, r, errf(400, "invalid_name", "The name is too long"))
			return
		}
		g.Name = strings.TrimSpace(*req.Name)
	}
	g, err = s.st.UpdateSmartSegment(r.Context(), it.UserID, *g)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.segmentsChanged(r.Context(), it)
	writeJSON(w, 200, newSegmentView(g))
}

func (s *Server) handleSmartSegmentDelete(w http.ResponseWriter, r *http.Request) {
	g, it, err := s.userSegment(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if g.Origin == "auto" || g.Origin == "edited" {
		// Rejecting keeps the decision when the recording is evaluated again.
		_, err = s.st.SetSmartSegmentsState(r.Context(), it.UserID, []int64{g.ID}, "rejected")
	} else {
		err = s.st.DeleteSmartSegments(r.Context(), it.UserID, []int64{g.ID})
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.segmentsChanged(r.Context(), it)
	w.WriteHeader(204)
}

// handleSmartSegmentSplit cuts a segment in two at a time inside it.
func (s *Server) handleSmartSegmentSplit(w http.ResponseWriter, r *http.Request) {
	g, it, err := s.userSegment(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		At float64 `json:"at"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.At-g.Start < 0.2 || g.End-req.At < 0.2 {
		writeErr(w, r, errf(400, "invalid_split", "Split inside the segment, at least 0.2 seconds from its ends"))
		return
	}
	ctx := r.Context()
	second := *g
	second.Start, second.Origin = req.At, "split"
	if g.Name != "" {
		second.Name = g.Name + " (2)"
	}
	b, err := s.st.CreateSmartSegment(ctx, it.UserID, second)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	g.End = req.At
	if _, err := s.st.DB.ExecContext(ctx, "UPDATE smart_segments SET end_s = ?, origin = CASE WHEN origin = 'manual' THEN 'manual' ELSE 'split' END, updated_at = ? WHERE id = ? AND user_id = ?",
		g.End, time.Now().UnixMilli(), g.ID, it.UserID); err != nil {
		writeErr(w, r, err)
		return
	}
	a, err := s.st.SmartSegment(ctx, it.UserID, g.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.segmentsChanged(ctx, it)
	writeJSON(w, 200, map[string]any{"segments": []segmentView{newSegmentView(a), newSegmentView(b)}})
}

// handleSmartSegmentMerge joins segments of one recording into one
// spanning them all (keeping the strongest one's labels and reasons).
func (s *Server) handleSmartSegmentMerge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := principal(r).User.ID
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.IDs) < 2 || len(req.IDs) > 100 {
		writeErr(w, r, errf(400, "invalid_merge", "Choose 2-100 segments to merge"))
		return
	}
	var segs []*store.SmartSegment
	for _, id := range req.IDs {
		g, err := s.st.SmartSegment(ctx, uid, id)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if len(segs) > 0 && g.ItemID != segs[0].ItemID {
			writeErr(w, r, errf(400, "invalid_merge", "Only segments of the same recording can be merged"))
			return
		}
		segs = append(segs, g)
	}
	it, err := s.st.SmartItem(ctx, uid, segs[0].ItemID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := editable(it); err != nil {
		writeErr(w, r, err)
		return
	}
	best := segs[0]
	m := *best
	for _, g := range segs {
		m.Start, m.End = math.Min(m.Start, g.Start), math.Max(m.End, g.End)
		if g.Score > best.Score {
			best = g
		}
	}
	m.Score, m.Category, m.Label, m.Reasons, m.Name = best.Score, best.Category, best.Label, best.Reasons, best.Name
	m.State, m.Origin = "approved", "merged"
	merged, err := s.st.CreateSmartSegment(ctx, uid, m)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// The parts: suggestions are rejected (so re-evaluation does not bring
	// them back), the user's own segments are removed.
	var reject, del []int64
	for _, g := range segs {
		if g.Origin == "auto" || g.Origin == "edited" {
			reject = append(reject, g.ID)
		} else {
			del = append(del, g.ID)
		}
	}
	if _, err := s.st.SetSmartSegmentsState(ctx, uid, reject, "rejected"); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.DeleteSmartSegments(ctx, uid, del); err != nil {
		writeErr(w, r, err)
		return
	}
	s.segmentsChanged(ctx, it)
	writeJSON(w, 200, newSegmentView(merged))
}

// handleSmartSegmentBulk approves or rejects many segments: by ids, or
// every undecided suggestion of items/queues (optionally only of some
// categories or above a score).
func (s *Server) handleSmartSegmentBulk(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	uid := principal(r).User.ID
	var req struct {
		IDs        []int64  `json:"ids"`
		ItemIDs    []int64  `json:"item_ids"`
		QueueID    int64    `json:"queue_id"`
		Categories []string `json:"categories"`
		MinScore   float64  `json:"min_score"`
		State      string   `json:"state"`
		// Finish marks the touched recordings reviewed (their approved
		// segments are then rendered).
		Finish bool `json:"finish"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if !validSegState(req.State) {
		writeErr(w, r, errf(400, "invalid_state", "Choose approved, rejected or suggested"))
		return
	}
	if len(req.IDs) > 20000 || len(req.ItemIDs) > maxQueueItems || len(req.Categories) > 10 {
		writeErr(w, r, errf(400, "too_many", "Too many items"))
		return
	}
	ids := append([]int64{}, req.IDs...)
	if len(req.ItemIDs) > 0 || req.QueueID > 0 {
		q := `SELECT g.id FROM smart_segments g JOIN smart_items i ON i.id = g.item_id WHERE g.user_id = ? AND g.state = 'suggested' AND g.score >= ? AND i.state IN ('ready', 'skipped')`
		args := []any{uid, req.MinScore}
		if len(req.ItemIDs) > 0 {
			q += " AND g.item_id IN (" + int64List(req.ItemIDs) + ")"
		}
		if req.QueueID > 0 {
			q += " AND g.queue_id = ?"
			args = append(args, req.QueueID)
		}
		if len(req.Categories) > 0 {
			q += " AND g.category IN (" + strings.TrimSuffix(strings.Repeat("?,", len(req.Categories)), ",") + ")"
			for _, c := range req.Categories {
				args = append(args, c)
			}
		}
		rows, err := s.st.DB.QueryContext(ctx, q, args...)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	// Segments of recordings being rendered are left alone.
	if len(ids) > 0 {
		rows, err := s.st.DB.QueryContext(ctx, "SELECT g.id FROM smart_segments g JOIN smart_items i ON i.id = g.item_id WHERE g.user_id = ? AND i.state NOT IN ('rendering', 'pending', 'analysing') AND g.id IN ("+int64List(ids)+")", uid)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		ids = ids[:0]
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				ids = append(ids, id)
			}
		}
		rows.Close()
	}
	n, err := s.st.SetSmartSegmentsState(ctx, uid, ids, req.State)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Touched recordings: finished ones go back to review; with finish,
	// every touched recording is marked reviewed.
	items := map[int64]bool{}
	for _, id := range req.ItemIDs {
		items[id] = true
	}
	if len(ids) > 0 {
		rows, err := s.st.DB.QueryContext(ctx, "SELECT DISTINCT item_id FROM smart_segments WHERE user_id = ? AND id IN ("+int64List(ids)+")", uid)
		if err == nil {
			for rows.Next() {
				var id int64
				if rows.Scan(&id) == nil {
					items[id] = true
				}
			}
			rows.Close()
		}
	}
	reviewed := 0
	for id := range items {
		it, err := s.st.SmartItem(ctx, uid, id)
		if err != nil {
			continue
		}
		if req.Finish {
			var open int
			_ = s.st.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM smart_segments WHERE item_id = ? AND state = 'suggested'", id).Scan(&open)
			if open == 0 && s.st.SetSmartItemState(ctx, uid, id, "reviewed", "ready", "skipped", "done", "failed") == nil {
				reviewed++
			}
			continue
		}
		s.segmentsChanged(ctx, it)
	}
	if reviewed > 0 {
		s.smartRun().poke()
	}
	writeJSON(w, 200, map[string]any{"updated": n, "reviewed": reviewed})
}

// --- per-user preferences ---

// smartPrefs are a user's defaults for new queues.
type smartPrefs struct {
	Context     smart.Context       `json:"context"`
	Output      smartOutputSettings `json:"output"`
	AutoApprove bool                `json:"auto_approve"`
	Presets     []string            `json:"preset_ids"`
}

func smartPrefsKey(userID int64) string { return fmt.Sprintf("smart.prefs.%d", userID) }

func (s *Server) smartPrefs(ctx context.Context, userID int64) smartPrefs {
	p := smartPrefs{Context: smart.DefaultContext(), Output: defaultOutput(), Presets: []string{}}
	if raw, ok, err := s.st.GetSetting(ctx, smartPrefsKey(userID)); err == nil && ok {
		_ = json.Unmarshal([]byte(raw), &p)
	}
	if p.Presets == nil {
		p.Presets = []string{}
	}
	return p
}

func (s *Server) handleSmartPrefs(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.smartPrefs(r.Context(), principal(r).User.ID))
}

func (s *Server) handleSmartPrefsSave(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var p smartPrefs
	if err := decode(r, &p); err != nil {
		writeErr(w, r, err)
		return
	}
	f := smart.Filter{Context: p.Context}
	if err := f.Validate(); err != nil {
		writeErr(w, r, errf(400, "invalid_settings", "%s", err.Error()))
		return
	}
	p.Context = f.Context
	if err := p.Output.validate(); err != nil {
		writeErr(w, r, err)
		return
	}
	if p.Output.Destination == "folder" {
		if n, err := s.st.Node(r.Context(), u.ID, p.Output.FolderID); err != nil || !n.IsDir {
			writeErr(w, r, errf(404, "folder_missing", "The destination folder does not exist"))
			return
		}
	}
	if len(p.Presets) > 12 {
		writeErr(w, r, errf(400, "too_many_presets", "Use at most 12 presets"))
		return
	}
	for _, id := range p.Presets {
		if _, err := s.smartPreset(r.Context(), u.ID, id); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if p.Presets == nil {
		p.Presets = []string{}
	}
	b, _ := json.Marshal(p)
	if err := s.st.SetSetting(r.Context(), smartPrefsKey(u.ID), string(b)); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, p)
}

// --- transcript search and the file browser ---

// handleSmartSearch finds words in the transcripts of the user's analysed
// recordings.
func (s *Server) handleSmartSearch(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	qs := r.URL.Query()
	query := strings.TrimSpace(qs.Get("q"))
	if len(query) < 2 || len(query) > 200 {
		writeErr(w, r, errf(400, "invalid_query", "Search for 2-200 characters"))
		return
	}
	queueID, _ := strconv.ParseInt(qs.Get("queue_id"), 10, 64)
	items, err := s.st.SmartItemsOfUser(r.Context(), u.ID, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	needle := strings.ToLower(query)
	type hit struct {
		ItemID  int64   `json:"item_id"`
		QueueID int64   `json:"queue_id"`
		Name    string  `json:"name"`
		Path    string  `json:"path"`
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Speaker int     `json:"speaker"`
		Text    string  `json:"text"`
	}
	hits := []hit{}
	seen := map[string]bool{}
	for i := range items {
		it := &items[i]
		if queueID > 0 && it.QueueID != queueID {
			continue
		}
		if it.AnalysedAt == nil || seen[it.SHA256+strconv.FormatInt(it.QueueID, 10)] {
			continue
		}
		seen[it.SHA256+strconv.FormatInt(it.QueueID, 10)] = true
		if r.Context().Err() != nil {
			return
		}
		t, err := s.loadTranscript(it)
		if err != nil || t == nil {
			continue
		}
		for _, ut := range t.Utterances {
			if strings.Contains(strings.ToLower(ut.Text), needle) {
				hits = append(hits, hit{it.ID, it.QueueID, it.Name, it.Path, ut.Start, ut.End, ut.Speaker, ut.Text})
				if len(hits) >= 500 {
					writeJSON(w, 200, map[string]any{"hits": hits, "truncated": true})
					return
				}
			}
		}
	}
	writeJSON(w, 200, map[string]any{"hits": hits, "truncated": false})
}

// fileSmartState is how smart processing sees one file.
type fileSmartState struct {
	NodeID int64 `json:"node_id"`
	// State: queued, analysing, review, rendering, processed, failed.
	State string `json:"state"`
	// Cleanup: eligible (clips verified), no_segments (reviewed, nothing
	// kept) or "" (not ready).
	Cleanup string  `json:"cleanup,omitempty"`
	Clips   int     `json:"clips"`
	Queues  []int64 `json:"queues"`
	// Output: the file is a clip made by smart processing.
	Output bool `json:"output,omitempty"`
}

// smartFileStates summarises the items holding these files (originals)
// and marks files that are rendered clips.
func (s *Server) smartFileStates(ctx context.Context, userID int64, nodeIDs []int64) (map[int64]*fileSmartState, error) {
	items, err := s.st.SmartItemsForNodes(ctx, userID, nodeIDs)
	if err != nil {
		return nil, err
	}
	out := map[int64]*fileSmartState{}
	rank := map[string]int{"processed": 1, "queued": 2, "review": 3, "analysing": 4, "rendering": 5, "failed": 6}
	for i := range items {
		it := &items[i]
		if it.OriginalState != "kept" {
			continue
		}
		st := map[string]string{"pending": "queued", "analysing": "analysing", "ready": "review", "skipped": "review", "reviewed": "rendering",
			"rendering": "rendering", "done": "processed", "failed": "failed"}[it.State]
		if st == "" {
			continue
		}
		f := out[it.NodeID]
		if f == nil {
			f = &fileSmartState{NodeID: it.NodeID, Queues: []int64{}}
			out[it.NodeID] = f
		}
		f.Queues = append(f.Queues, it.QueueID)
		if rank[st] > rank[f.State] {
			f.State = st
		}
		if it.State == "done" {
			el, err := s.cleanupEligibility(ctx, it, false)
			if err != nil {
				return nil, err
			}
			f.Clips += el.Clips
			if el.Status == "eligible" || (el.Status == "no_segments" && f.Cleanup == "") {
				f.Cleanup = el.Status
			}
		}
	}
	if len(nodeIDs) > 0 {
		rows, err := s.st.DB.QueryContext(ctx, "SELECT DISTINCT node_id FROM smart_outputs WHERE user_id = ? AND state = 'done' AND node_id IN ("+int64List(nodeIDs)+")", userID)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		for rows.Next() {
			var id int64
			if rows.Scan(&id) == nil {
				if out[id] == nil {
					out[id] = &fileSmartState{NodeID: id, Queues: []int64{}}
				}
				out[id].Output = true
			}
		}
	}
	return out, nil
}

// handleSmartFiles returns the smart processing state of files shown in
// the file browser.
func (s *Server) handleSmartFiles(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		IDs []int64 `json:"ids"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.IDs) > 5000 {
		writeErr(w, r, errf(400, "too_many", "Too many files"))
		return
	}
	m, err := s.smartFileStates(r.Context(), u.ID, req.IDs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]*fileSmartState, 0, len(m))
	for _, f := range m {
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].NodeID < out[j].NodeID })
	writeJSON(w, 200, map[string]any{"files": out})
}

// handleSmartFolderFiles lists a folder's videos (optionally recursive)
// with their smart state, so the file browser can select all, processed
// or cleanup-eligible recordings at once.
func (s *Server) handleSmartFolderFiles(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	qs := r.URL.Query()
	folderID, _ := strconv.ParseInt(qs.Get("folder_id"), 10, 64)
	if folderID <= 0 {
		folderID = u.RootID
	}
	vids, err := s.resolveVideos(r.Context(), u, queueSources{FolderIDs: []int64{folderID}, Recursive: qs.Get("recursive") == "1"})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ids := make([]int64, len(vids))
	for i, v := range vids {
		ids[i] = v.NodeID
	}
	states, err := s.smartFileStates(r.Context(), u.ID, ids)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sel := qs.Get("select") // all, processed, eligible, unprocessed
	type row struct {
		ID    int64           `json:"id"`
		Name  string          `json:"name"`
		Path  string          `json:"path"`
		Size  int64           `json:"size"`
		Smart *fileSmartState `json:"smart,omitempty"`
	}
	out := []row{}
	var bytes int64
	for _, v := range vids {
		st := states[v.NodeID]
		switch sel {
		case "processed":
			if st == nil || st.State != "processed" {
				continue
			}
		case "eligible":
			if st == nil || st.Cleanup == "" {
				continue
			}
		case "unprocessed":
			// Neither queued nor a clip made by smart processing.
			if st != nil {
				continue
			}
		}
		out = append(out, row{v.NodeID, v.Name, v.Path, v.Size, st})
		bytes += v.Size
	}
	writeJSON(w, 200, map[string]any{"files": out, "count": len(out), "bytes": bytes})
}

func chiParam(r *http.Request, name string) string { return chi.URLParam(r, name) }
