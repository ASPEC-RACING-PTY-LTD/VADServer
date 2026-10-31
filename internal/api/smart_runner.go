package api

// Smart processing runner: analyses queued recordings and renders
// approved segments in the background, independent of any browser.
//
// Work is claimed from the database (items pending → analysing, reviewed
// → rendering), so queues survive restarts and upgrades: a restart puts
// interrupted analyses back to pending (they resume from their cached
// checkpoints) and interrupted renders back to their queue (an output is
// linked to its stored file in the same transaction, so a retry never
// makes a second copy).

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/mimetypes"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/smart"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// smartOutputSettings: how and where approved segments are rendered.
type smartOutputSettings struct {
	// Quality: auto (stream copy when the cut lands on a keyframe,
	// otherwise a high-quality frame-accurate encode), copy (keyframe cuts),
	// high, balanced or small.
	Quality string `json:"quality"`
	// Destination: beside (the original's folder), subfolder (a folder
	// next to it) or folder (FolderID, mirroring the originals' folders).
	Destination string `json:"destination"`
	Subfolder   string `json:"subfolder,omitempty"`
	FolderID    int64  `json:"folder_id,omitempty"`
}

func defaultOutput() smartOutputSettings {
	return smartOutputSettings{Quality: "auto", Destination: "beside", Subfolder: "Highlights"}
}

func (o *smartOutputSettings) validate() error {
	if o.Quality == "" {
		o.Quality = "auto"
	}
	if o.Quality != "auto" && !media.ValidQuality(o.Quality) {
		return errf(400, "invalid_quality", "Unknown output quality")
	}
	switch o.Destination {
	case "", "beside":
		o.Destination = "beside"
	case "subfolder":
		o.Subfolder = strings.TrimSpace(o.Subfolder)
		if o.Subfolder == "" {
			o.Subfolder = "Highlights"
		}
		if _, err := pathutil.Split("/" + o.Subfolder); err != nil || strings.ContainsAny(o.Subfolder, `/\`) {
			return errf(400, "invalid_folder", "The subfolder name is not valid")
		}
	case "folder":
		if o.FolderID <= 0 {
			return errf(400, "invalid_folder", "Choose the destination folder")
		}
	default:
		return errf(400, "invalid_destination", "Unknown output destination")
	}
	return nil
}

func parseOutput(raw string) smartOutputSettings {
	o := defaultOutput()
	_ = json.Unmarshal([]byte(raw), &o)
	if o.Quality == "" {
		o.Quality = "auto"
	}
	return o
}

func parseFilter(raw string) smart.Filter {
	var f smart.Filter
	if json.Unmarshal([]byte(raw), &f) != nil {
		f = smart.Filter{Context: smart.DefaultContext()}
	}
	return f
}

func (s *Server) smartAnalyzer() *smart.Analyzer {
	r := s.smartRun()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.an == nil {
		r.an = &smart.Analyzer{
			Tools:  s.media.tools,
			Worker: s.smartWorker,
			Dir:    filepath.Join(s.cfg.StateDir, "cache", "smart"),
			Temp:   filepath.Join(s.cfg.StateDir, "tmp", "smart"),
			Opts:   func() smart.Settings { return s.smartSettings().analysis() },
		}
	}
	return r.an
}

func (s *Server) timelinePath(itemID int64) string {
	return filepath.Join(s.cfg.StateDir, "smart", "timeline", fmt.Sprintf("%d.json", itemID))
}

// RunSmart processes queues until ctx ends.
func (s *Server) RunSmart(ctx context.Context) {
	r := s.smartRun()
	if n, o, err := s.st.RecoverSmart(ctx); err != nil {
		slog.Warn("smart processing recovery failed", "err", err)
	} else if n+o > 0 {
		logSmart("resuming work interrupted by a restart", "recordings", n, "clips", o)
	}
	an := s.smartAnalyzer()
	an.Sweep(ctx, func(int64, string) bool { return true }, true)
	var analysing, rendering int
	done := make(chan string, 64)
	sweep := time.NewTicker(6 * time.Hour)
	defer sweep.Stop()
	for {
		set := s.smartSettings()
		if set.Enabled && s.media.tools.Available() {
			for analysing < set.AnalysisConcurrency {
				it, err := s.st.ClaimSmartItem(ctx)
				if err != nil {
					break
				}
				analysing++
				s.ops.wg.Add(1)
				go func() {
					defer s.ops.wg.Done()
					s.analyseItem(ctx, it)
					done <- "analysis"
				}()
			}
			for rendering < set.RenderConcurrency {
				it, err := s.st.ClaimSmartRender(ctx)
				if err != nil {
					break
				}
				rendering++
				s.ops.wg.Add(1)
				go func() {
					defer s.ops.wg.Done()
					s.renderItem(ctx, it)
					done <- "render"
				}()
			}
		}
		select {
		case <-ctx.Done():
			for analysing+rendering > 0 {
				if <-done == "analysis" {
					analysing--
				} else {
					rendering--
				}
			}
			return
		case k := <-done:
			if k == "analysis" {
				analysing--
			} else {
				rendering--
			}
		case <-r.wake:
		case <-sweep.C:
			go an.Sweep(ctx, func(uid int64, sha string) bool { ok, _ := s.st.HasBlob(ctx, uid, sha); return ok }, false)
		case <-time.After(15 * time.Second):
		}
		s.completeQueues(ctx)
	}
}

// completeQueues marks active queues with nothing left to do completed.
func (s *Server) completeQueues(ctx context.Context) {
	rows, err := s.st.DB.QueryContext(ctx, `SELECT q.id, q.user_id FROM smart_queues q WHERE q.state = 'active'
		AND EXISTS (SELECT 1 FROM smart_items i WHERE i.queue_id = q.id)
		AND NOT EXISTS (SELECT 1 FROM smart_items i WHERE i.queue_id = q.id AND i.state IN ('pending', 'analysing', 'ready', 'reviewed', 'rendering'))`)
	if err != nil {
		return
	}
	type qu struct{ id, user int64 }
	var qs []qu
	for rows.Next() {
		var q qu
		if rows.Scan(&q.id, &q.user) == nil {
			qs = append(qs, q)
		}
	}
	rows.Close()
	for _, q := range qs {
		_ = s.st.SetSmartQueueState(ctx, q.user, q.id, "completed")
	}
}

func (r *smartRunner) start(it *store.SmartItem, kind string, parent context.Context) (context.Context, *smartJob) {
	ctx, cancel := context.WithCancel(parent)
	j := &smartJob{ItemID: it.ID, QueueID: it.QueueID, UserID: it.UserID, Name: it.Name, Kind: kind, Started: time.Now().UnixMilli(), cancel: cancel}
	r.mu.Lock()
	r.running[it.ID] = j
	r.mu.Unlock()
	return ctx, j
}

func (r *smartRunner) finish(j *smartJob) {
	j.cancel()
	r.mu.Lock()
	delete(r.running, j.ItemID)
	r.mu.Unlock()
}

func (r *smartRunner) set(j *smartJob, stage string, p float64) {
	r.mu.Lock()
	j.Stage, j.Progress = stage, p
	r.mu.Unlock()
}

// smartTimeline is what the review editor draws besides the segments.
type smartTimeline struct {
	Events   []smart.Event    `json:"events"`
	Loudness []float32        `json:"loudness,omitempty"` // dBFS, 2 per second
	Motion   []float32        `json:"motion,omitempty"`   // 0..1, 2 per second
	Speech   []smart.Interval `json:"speech,omitempty"`
	Warnings []string         `json:"warnings"`
	Rate     float64          `json:"rate"`
}

func downsample(s *smart.Series, rate float64) []float32 {
	if s == nil || s.Rate <= 0 || len(s.Values) == 0 {
		return nil
	}
	step := s.Rate / rate
	n := int(float64(len(s.Values)) / step)
	out := make([]float32, n)
	for i := range out {
		lo, hi := int(float64(i)*step), int(float64(i+1)*step)
		hi = min(max(hi, lo+1), len(s.Values))
		m := s.Values[lo]
		for _, v := range s.Values[lo:hi] {
			m = max(m, v)
		}
		out[i] = float32(math.Round(float64(m)*100) / 100)
	}
	return out
}

// analyseItem measures one recording and stores its suggestions.
func (s *Server) analyseItem(parent context.Context, it *store.SmartItem) {
	r := s.smartRun()
	ctx, job := r.start(it, "analysis", parent)
	defer r.finish(job)
	started := time.Now()
	fail := func(msg string) {
		if ctx.Err() != nil {
			return // paused, cancelled or shutting down: the state was set elsewhere
		}
		if err := s.st.FinishSmartAnalysis(context.WithoutCancel(ctx), it, store.SmartAnalysed{Error: msg, ElapsedMS: time.Since(started).Milliseconds()}); err != nil {
			slog.Warn("smart item state not saved", "item", it.ID, "err", err)
		}
	}
	q, err := s.st.SmartQueueByID(ctx, it.QueueID)
	if err != nil {
		fail("The queue could not be loaded")
		return
	}
	user, err := s.st.UserByID(ctx, it.UserID)
	if err != nil {
		fail("The account could not be loaded")
		return
	}
	node, err := s.st.Node(ctx, user.ID, it.NodeID)
	if err != nil || !isVideo(node) {
		if ctx.Err() == nil {
			_ = s.st.SetSmartItemState(context.WithoutCancel(ctx), user.ID, it.ID, "gone")
			s.st.LogSmartError(ctx, it.QueueID, &it.ID, "analysis", it.Name+": the file is no longer in its folder")
		}
		return
	}
	if node.SHA256 != it.SHA256 {
		// The file was replaced (edited and synced again): its new content
		// is analysed; measurements of the old content are not reused.
		s.st.LogSmartError(ctx, it.QueueID, &it.ID, "analysis", it.Name+": the file changed since it was queued; analysing the new version")
		it.SHA256 = node.SHA256
	}
	tProbe := time.Now()
	info, err := s.mediaInfo(ctx, user.ID, node.SHA256, nil)
	if err != nil {
		fail(exportUserMessage(err))
		return
	}
	probeMS := time.Since(tProbe).Milliseconds()
	set := s.smartSettings()
	if set.MaxMinutes > 0 && info.Duration > float64(set.MaxMinutes)*60 {
		fail(fmt.Sprintf("The recording is longer than the %d minute limit set by the administrator", set.MaxMinutes))
		return
	}
	filter := parseFilter(q.Filter)
	avail := s.smartAvailability(ctx)
	dets := filter.Detectors(avail)
	have := map[string]bool{}
	for _, d := range dets {
		have[d] = true
	}
	// Loudness lets segments drop silent ends; voice activity places edges
	// between sentences.
	if !have[smart.DAudioDSP] && avail.Has(smart.DAudioDSP) {
		dets = append([]string{smart.DAudioDSP}, dets...)
	}
	var keep []string
	for _, d := range dets {
		if avail.Has(d) {
			keep = append(keep, d)
		}
	}
	src := smart.Source{UserID: user.ID, SHA256: node.SHA256, Info: info, Open: func() (media.Input, func(), error) {
		f, err := s.openBlob(ctx, user.ID, node.SHA256)
		if err != nil {
			return nil, nil, err
		}
		in, release := f.Media()
		return in, func() { release(); f.Close() }, nil
	}}
	an := s.smartAnalyzer()
	last := time.Time{}
	tMeasure := time.Now()
	failed, err := an.Ensure(ctx, src, keep, func(stage string, p float64) {
		r.set(job, stage, p)
		if time.Since(last) > 2*time.Second {
			last = time.Now()
			_ = s.st.SetSmartItemProgress(ctx, it.ID, stage, p)
		}
	})
	if err != nil {
		fail(exportUserMessage(err))
		return
	}
	measureMS := time.Since(tMeasure).Milliseconds()
	tEval := time.Now()
	r.set(job, "finding moments", 1)
	_ = s.st.SetSmartItemProgress(ctx, it.ID, "finding moments", 1)
	a, err := an.Load(src, keep)
	if err != nil {
		fail("The measurements could not be read: " + err.Error())
		return
	}
	env := &smart.Env{A: a, KeepLeaves: true}
	if tc := r.texter(); tc != nil {
		tc.Ctx = ctx
		env.Embed = tc
	}
	events := smart.Evaluate(&filter, env)
	sugg := smart.Suggest(a, events, filter.Context)
	var warnings []string
	for _, c := range smart.Check(&filter, avail) {
		if !c.Available && !ruleDisabled(&filter, c.Rule) {
			warnings = append(warnings, fmt.Sprintf("%s: %s cannot run here (needs %s)", c.Rule, c.Summary, strings.Join(c.Missing, ", ")))
		}
	}
	for d, e := range failed {
		warnings = append(warnings, fmt.Sprintf("%s was not measured: %s", d, e.Error()))
	}
	warnings = append(warnings, env.Warnings...)
	if ctx.Err() != nil {
		return
	}
	segs := make([]store.SmartSegment, 0, len(sugg))
	for _, sg := range sugg {
		reasons, _ := json.Marshal(sg.Reasons)
		segs = append(segs, store.SmartSegment{Start: sg.Start, End: sg.End, Score: math.Round(sg.Score*1000) / 1000, Category: sg.Category, Label: sg.Label, Reasons: string(reasons)})
	}
	tl := smartTimeline{Events: env.Leaves, Warnings: warnings, Rate: 2, Speech: a.Speech}
	if a.Audio != nil {
		tl.Loudness = downsample(&a.Audio.Loudness, 2)
	}
	if a.Video != nil {
		tl.Motion = downsample(&a.Video.Motion, 2)
	}
	if len(tl.Events) > 5000 {
		tl.Events = tl.Events[:5000]
	}
	if b, err := json.Marshal(tl); err == nil {
		_ = s.media.cache.Put(s.timelinePath(it.ID), b)
	}
	measured, _ := json.Marshal(an.Cached(src))
	warnJSON, _ := json.Marshal(nonNilStrings(warnings))
	res := store.SmartAnalysed{State: "ready", SHA256: node.SHA256, Duration: info.Duration, Detectors: string(measured), Warnings: string(warnJSON),
		Events: len(env.Leaves), EvalRev: q.FilterRev, ElapsedMS: time.Since(started).Milliseconds(), Segments: segs, Approve: q.AutoApprove}
	if err := s.st.FinishSmartAnalysis(context.WithoutCancel(ctx), it, res); err != nil {
		slog.Warn("smart analysis not saved", "item", it.ID, "err", err)
		return
	}
	logSmart("analysis finished", "item", it.ID, "queue", it.QueueID, "size", node.Size, "duration_s", math.Round(info.Duration),
		"detectors", strings.Join(keep, ","), "probe_ms", probeMS, "measure_ms", measureMS, "evaluate_ms", time.Since(tEval).Milliseconds(),
		"total_ms", time.Since(started).Milliseconds(), "suggestions", len(segs), "failed", len(failed))
	if q.AutoApprove {
		r.poke()
	}
}

func ruleDisabled(f *smart.Filter, name string) bool {
	for _, r := range f.Rules {
		if r.Name == name {
			return r.Disabled
		}
	}
	return false
}

func nonNilStrings(v []string) []string {
	if v == nil {
		return []string{}
	}
	return v
}

func renderKey(g *store.SmartSegment, quality string) string {
	return fmt.Sprintf("%.2f-%.2f-%s", g.Start, g.End, quality)
}

// renderItem renders the approved segments of a reviewed recording.
func (s *Server) renderItem(parent context.Context, it *store.SmartItem) {
	r := s.smartRun()
	ctx, job := r.start(it, "render", parent)
	defer r.finish(job)
	bg := context.WithoutCancel(ctx)
	finishItem := func(state, msg string) {
		if ctx.Err() != nil && parent.Err() != nil {
			return // shutting down: recovered at the next start
		}
		q := "UPDATE smart_items SET state = ?, stage = '', progress = ?, error = ?, updated_at = ? WHERE id = ?"
		p := 1.0
		if state != "done" {
			p = 0
		}
		if _, err := s.st.DB.ExecContext(bg, q, state, p, msg, time.Now().UnixMilli(), it.ID); err != nil {
			slog.Warn("smart item state not saved", "item", it.ID, "err", err)
		}
	}
	q, err := s.st.SmartQueueByID(ctx, it.QueueID)
	if err != nil {
		finishItem("failed", "The queue could not be loaded")
		return
	}
	out := parseOutput(q.Output)
	user, err := s.st.UserByID(ctx, it.UserID)
	if err != nil {
		finishItem("failed", "The account could not be loaded")
		return
	}
	segs, err := s.st.SmartSegments(ctx, it.ID)
	if err != nil {
		finishItem("failed", "The segments could not be loaded")
		return
	}
	keys := map[int64]string{}
	byID := map[int64]*store.SmartSegment{}
	for i := range segs {
		if segs[i].State == "approved" {
			keys[segs[i].ID] = renderKey(&segs[i], out.Quality)
			byID[segs[i].ID] = &segs[i]
		}
	}
	if len(keys) == 0 {
		finishItem("done", "")
		return
	}
	src, err := s.st.Node(ctx, user.ID, it.NodeID)
	if err != nil || src.SHA256 != it.SHA256 {
		finishItem("failed", "The original changed or was removed after it was reviewed; analyse it again")
		return
	}
	info, err := s.mediaInfo(ctx, user.ID, src.SHA256, nil)
	if err != nil {
		finishItem("failed", exportUserMessage(err))
		return
	}
	todo, err := s.st.PlanSmartRenders(ctx, it, keys)
	if err != nil {
		finishItem("failed", "The clips could not be planned")
		return
	}
	failed := 0
	for i := range todo {
		if ctx.Err() != nil {
			break
		}
		o := &todo[i]
		r.set(job, fmt.Sprintf("clip %d of %d", i+1, len(todo)), float64(i)/float64(len(todo)))
		_ = s.st.SetSmartItemProgress(ctx, it.ID, fmt.Sprintf("rendering clip %d of %d", i+1, len(todo)), float64(i)/float64(len(todo)))
		if err := s.renderOutput(ctx, user, src, info, byID[o.SegmentID], o, out); err != nil {
			if ctx.Err() != nil {
				_, _ = s.st.DB.ExecContext(bg, "UPDATE smart_outputs SET state = 'pending' WHERE id = ? AND state = 'rendering'", o.ID)
				break
			}
			failed++
			msg := exportUserMessage(err)
			slog.Warn("smart clip render failed", "item", it.ID, "output", o.ID, "err", err)
			_ = s.st.FailSmartOutput(bg, o, it.Name+": "+msg)
		}
	}
	if ctx.Err() != nil {
		if parent.Err() == nil {
			finishItem("reviewed", "") // paused: rendered again on resume
		}
		return
	}
	if failed > 0 {
		finishItem("failed", fmt.Sprintf("%d of %d clips could not be rendered", failed, len(todo)))
		return
	}
	finishItem("done", "")
}

func fmtClipTime(t float64) string {
	sec := int(t)
	if sec >= 3600 {
		return fmt.Sprintf("%02d-%02d-%02d", sec/3600, sec/60%60, sec%60)
	}
	return fmt.Sprintf("%02d-%02d", sec/60, sec%60)
}

// clipDir is the folder a clip goes to.
func (s *Server) clipDir(ctx context.Context, user *store.User, src *store.Node, out smartOutputSettings) ([]string, error) {
	srcDir := "/"
	if src.ParentID != nil {
		p, err := s.st.NodePath(ctx, user.ID, *src.ParentID)
		if err != nil {
			return nil, err
		}
		srcDir = p
	}
	dir := srcDir
	switch out.Destination {
	case "subfolder":
		dir = path.Join(srcDir, out.Subfolder)
	case "folder":
		root, err := s.st.Node(ctx, user.ID, out.FolderID)
		if err != nil || !root.IsDir {
			return nil, errf(404, "folder_missing", "The destination folder no longer exists")
		}
		rp, err := s.st.NodePath(ctx, user.ID, root.ID)
		if err != nil {
			return nil, err
		}
		dir = path.Join(rp, srcDir) // the originals' folders, mirrored
	}
	if dir == "/" {
		return nil, nil
	}
	return pathutil.Split(dir)
}

// renderOutput renders, verifies and stores one clip.
func (s *Server) renderOutput(ctx context.Context, user *store.User, src *store.Node, info *media.Info, g *store.SmartSegment, o *store.SmartOutput, out smartOutputSettings) error {
	if g == nil {
		return errors.New("the segment no longer exists")
	}
	started := time.Now()
	c := media.Clip{Start: g.Start, End: g.End, Speed: 1, Volume: 1}
	if err := c.Normalize(info); err != nil {
		return errf(400, "invalid_clip", "%s", err.Error())
	}
	in, err := s.openBlob(ctx, user.ID, src.SHA256)
	if err != nil {
		return errf(503, "content_unavailable", "The original's content is unavailable on the storage device")
	}
	defer in.Close()
	mi, release := in.Media()
	defer release()
	quality := out.Quality
	mode := quality
	if quality == "auto" {
		mode = media.QualityHigh
		// A stream copy is exact only when the cut starts on a keyframe.
		if ok, err := s.media.tools.KeyframeAt(ctx, mi, c.Start, 0.5/math.Max(info.FPS, 1)); err == nil && ok {
			mode = media.QualityCopy
		}
	}
	mode = media.Mode(&c, mode)
	format, ext := media.Container(mode, src.Name, info)
	label := strings.TrimSpace(g.Name)
	if label == "" {
		label = g.Label
	}
	if label == "" {
		label = "Clip"
	}
	if r := []rune(label); len(r) > 60 {
		label = string(r[:60])
	}
	base := strings.TrimSuffix(src.Name, path.Ext(src.Name))
	name := cleanClipName(fmt.Sprintf("%s - %s %s", base, label, fmtClipTime(g.Start))) + ext
	dirSegs, err := s.clipDir(ctx, user, src, out)
	if err != nil {
		return err
	}
	if err := s.st.StartSmartOutput(ctx, o.ID, name); err != nil {
		return err
	}
	loc, err := s.locationFor(ctx, user)
	if err != nil {
		return err
	}
	est := media.EstimateSize(&c, mode, info)
	if fresh, err := s.st.UserByID(ctx, user.ID); err == nil && fresh.QuotaBytes > 0 && fresh.UsedBytes+est > fresh.QuotaBytes {
		return store.ErrQuota
	}
	if err := s.blobs.CheckSpace(ctx, loc, est+est/4+64<<20); err != nil {
		return err
	}
	tmp, err := s.blobs.TempPath(loc, "sx"+auth.RandomToken(15))
	if err != nil {
		return err
	}
	defer storage.RemoveTemp(tmp)
	args := media.ExportArgs(&c, mode, info, format, tmp)
	if err := s.media.tools.Export(ctx, mi, args, nil); err != nil {
		return err
	}
	want := c.OutputDuration()
	if _, err := s.media.tools.Verify(ctx, tmp, want, info.HasAudio()); err != nil {
		return err
	}
	sum, err := storage.HashFile(tmp)
	if err != nil {
		return err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		return err
	}
	// The clip keeps the original's recording dates, not the render time.
	created := src.ModifiedAt
	if src.FileCreatedAt != nil && *src.FileCreatedAt > 0 {
		created = *src.FileCreatedAt
	}
	o.SHA256, o.Size, o.Name, o.Mode, o.RenderMS = sum, fi.Size(), name, mode, time.Since(started).Milliseconds()
	var res *store.CommitResult
	err = s.blobs.Place(ctx, user.ID, sum, tmp, loc, func(l int64) error {
		var cerr error
		res, cerr = s.st.CommitFile(ctx, store.CommitRequest{
			User: user, DirSegs: dirSegs, Name: name, Size: fi.Size(), SHA256: sum, Mime: mimetypes.ByName(name),
			ModifiedAt: src.ModifiedAt, FileCreatedAt: created, OnConflict: "rename", LocationID: l, KeepReplaced: s.settings.Get().KeepReplacedVersions,
			AfterTx: func(tx *sql.Tx, nodeID int64) error { return store.FinishSmartOutputTx(ctx, tx, o, nodeID) },
		})
		return cerr
	})
	if err != nil {
		return err
	}
	s.blobs.Remove(context.WithoutCancel(ctx), res.Freed)
	p, _ := s.st.NodePath(ctx, user.ID, res.Node.ID)
	_ = s.st.SetSmartOutputPath(context.WithoutCancel(ctx), o.ID, res.Node.Name, p)
	s.mediaCommitted(user.ID, res.Node)
	s.st.LogActivity(context.WithoutCancel(ctx), store.Activity{UserID: &user.ID, ActorID: &user.ID, Action: "smart.clip_rendered", Target: p, Details: "from " + src.Name})
	return nil
}
