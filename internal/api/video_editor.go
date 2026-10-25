package api

// Video editor: drafts and clip exports.
//
// A draft stores the editor's instructions (selections, names, speed,
// audio, crop) and a reference to the source file, never a copy of it.
//
// An export turns every selection of one source recording into its own
// file, one after the other: FFmpeg writes to a temp file in the storage
// location, the result is probed (it must play and have the expected
// length), then stored like an upload (hash, blob store, file node, quota)
// in the chosen folder. Originals are only read. With "erase original" the
// source goes to the trash, kept 7 days, and only when every clip was
// stored; a synced PC that still has it is stopped from uploading it again.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/mimetypes"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	maxDraftBytes    = 512 << 10
	maxExportClips   = 50
	eraseTrashDays   = 7
	reuploadBlockFor = 180 * 24 * time.Hour
)

func (s *Server) mountVideoEditor(rt routes) {
	rt.User.Get("/media/status", s.handleMediaStatus)
	rt.User.Get("/files/media", s.handleMediaBatch)
	rt.User.Get("/files/{id}/thumbnail", s.handleThumbnail)
	rt.User.Get("/files/{id}/media", s.handleMediaInfo)
	rt.User.Get("/files/{id}/frame", s.handleFrame)

	rt.Sess.Get("/video/drafts", s.handleDraftList)
	rt.Sess.Post("/video/drafts", s.handleDraftCreate)
	rt.Sess.Get("/video/drafts/{id}", s.handleDraftGet)
	rt.Sess.Put("/video/drafts/{id}", s.handleDraftUpdate)
	rt.Sess.Delete("/video/drafts/{id}", s.handleDraftDelete)

	rt.Sess.Get("/video/exports", s.handleExportList)
	rt.Sess.Post("/video/exports", s.handleExportCreate)
	rt.Sess.Get("/video/exports/{id}", s.handleExportGet)
	rt.Sess.Post("/video/exports/{id}/cancel", s.handleExportCancel)
}

// --- drafts ---

type draftView struct {
	store.VideoDraft
	Data json.RawMessage `json:"data,omitempty"`
	// SourceState: ok, changed (the file was replaced by other content),
	// trashed or missing.
	SourceState string `json:"source_state"`
	SourcePath  string `json:"source_path,omitempty"`
}

func (s *Server) draftView(ctx context.Context, userID int64, d *store.VideoDraft, withData bool) draftView {
	v := draftView{VideoDraft: *d, SourceState: "missing"}
	if withData {
		v.Data = json.RawMessage(d.Data)
	}
	if n, err := s.st.Node(ctx, userID, d.SourceNodeID); err == nil {
		v.SourceState = "ok"
		if n.SHA256 != d.SourceSHA256 {
			v.SourceState = "changed"
		}
		v.SourcePath, _ = s.st.NodePath(ctx, userID, n.ID)
	} else if _, err := s.st.TrashedNode(ctx, userID, d.SourceNodeID); err == nil {
		v.SourceState = "trashed"
	}
	return v
}

func (s *Server) handleDraftList(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	src, _ := strconv.ParseInt(r.URL.Query().Get("source"), 10, 64)
	ds, err := s.st.VideoDrafts(r.Context(), u.ID, src)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]draftView, 0, len(ds))
	for i := range ds {
		out = append(out, s.draftView(r.Context(), u.ID, &ds[i], false))
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

type draftReq struct {
	SourceID int64           `json:"source_id"`
	Name     string          `json:"name"`
	Data     json.RawMessage `json:"data"`
}

func checkDraftData(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "{}", nil
	}
	if len(raw) > maxDraftBytes {
		return "", errf(413, "draft_too_large", "The draft is too large")
	}
	var v map[string]any
	if json.Unmarshal(raw, &v) != nil {
		return "", errf(400, "bad_draft", "Draft data must be a JSON object")
	}
	return string(raw), nil
}

func (s *Server) handleDraftCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req draftReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.Node(r.Context(), u.ID, req.SourceID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !isVideo(n) {
		writeErr(w, r, errf(400, "not_video", "Not a video file"))
		return
	}
	data, err := checkDraftData(req.Data)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	name := clip(strings.TrimSpace(req.Name), 200)
	if name == "" {
		name = strings.TrimSuffix(n.Name, path.Ext(n.Name))
	}
	d, err := s.st.CreateVideoDraft(r.Context(), u.ID, store.VideoDraft{SourceNodeID: n.ID, SourceSHA256: n.SHA256, SourceName: n.Name, Name: name, Data: data})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 201, s.draftView(r.Context(), u.ID, d, true))
}

func (s *Server) handleDraftGet(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	d, err := s.st.VideoDraft(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, s.draftView(r.Context(), u.ID, d, true))
}

func (s *Server) handleDraftUpdate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req draftReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	data, err := checkDraftData(req.Data)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	name := clip(strings.TrimSpace(req.Name), 200)
	if name == "" {
		writeErr(w, r, errf(400, "invalid_name", "Give the project a name"))
		return
	}
	d, err := s.st.UpdateVideoDraft(r.Context(), u.ID, id, name, data)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, s.draftView(r.Context(), u.ID, d, true))
}

func (s *Server) handleDraftDelete(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.DeleteVideoDraft(r.Context(), u.ID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// --- exports ---

type exportClipReq struct {
	Number int        `json:"number"`
	Name   string     `json:"name"`
	Clip   media.Clip `json:"clip"`
}

type exportReq struct {
	SourceID      int64           `json:"source_id"`
	SourceSHA256  string          `json:"source_sha256"`
	DraftID       int64           `json:"draft_id,omitempty"`
	FolderID      int64           `json:"folder_id"`
	Quality       string          `json:"quality"`
	EraseOriginal bool            `json:"erase_original"`
	Clips         []exportClipReq `json:"clips"`
}

type exportClipState struct {
	Number   int     `json:"number"`
	Name     string  `json:"name"`  // file name it is stored under
	State    string  `json:"state"` // pending, running, done, failed, cancelled
	Mode     string  `json:"mode"`  // copy or an encode preset
	Progress float64 `json:"progress"`
	Estimate int64   `json:"estimate"`
	Size     int64   `json:"size,omitempty"`
	NodeID   int64   `json:"node_id,omitempty"`
	Path     string  `json:"path,omitempty"`
	Error    string  `json:"error,omitempty"`
}

type exportOriginal struct {
	// State: kept, trashed or failed (erase was asked for and did not
	// happen; Error says why).
	State     string `json:"state"`
	NodeID    int64  `json:"node_id"`
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Error     string `json:"error,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
}

type exportProgress struct {
	Clips    []exportClipState `json:"clips"`
	Current  int               `json:"current"` // index of the clip being made, -1 when none
	Original exportOriginal    `json:"original"`
	Folder   string            `json:"folder"`
	Quality  string            `json:"quality"`
	Erase    bool              `json:"erase_original"`
	Error    string            `json:"error,omitempty"`
}

type exportView struct {
	store.VideoExport
	Progress exportProgress `json:"progress"`
}

func viewExport(e *store.VideoExport) exportView {
	v := exportView{VideoExport: *e}
	_ = json.Unmarshal([]byte(e.Progress), &v.Progress)
	return v
}

// exportRunner runs one export at a time (encodes use every core).
type exportRunner struct {
	slot    chan struct{}
	mu      sync.Mutex
	cancels map[int64]context.CancelFunc
}

func newExportRunner() *exportRunner {
	return &exportRunner{slot: make(chan struct{}, 1), cancels: map[int64]context.CancelFunc{}}
}

// cleanClipName turns a requested name into a file name without extension.
func cleanClipName(name string) string {
	name = strings.TrimSpace(name)
	// A typed extension is replaced by the one the output gets.
	if ext := path.Ext(name); ext != "" && len(ext) <= 6 && strings.HasPrefix(mimetypes.ByName(name), "video/") {
		name = strings.TrimSuffix(name, ext)
	}
	name = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) || r == unicode.ReplacementChar {
			return '-'
		}
		return r
	}, name)
	name = strings.Trim(strings.TrimSpace(name), ".")
	if len(name) > 180 {
		name = name[:180]
	}
	return name
}

func (s *Server) handleExportCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req exportReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if !s.media.tools.Available() {
		writeErr(w, r, errf(501, "media_unavailable", "%s", media.ErrUnavailable.Error()))
		return
	}
	if req.Quality == "" {
		req.Quality = media.QualityHigh
	}
	if !media.ValidQuality(req.Quality) {
		writeErr(w, r, errf(400, "invalid_quality", "Unknown quality"))
		return
	}
	if len(req.Clips) == 0 || len(req.Clips) > maxExportClips {
		writeErr(w, r, errf(400, "invalid_clips", "Export between 1 and %d clips", maxExportClips))
		return
	}
	src, err := s.st.Node(r.Context(), u.ID, req.SourceID)
	if err != nil {
		writeErr(w, r, errf(404, "source_missing", "The source video is no longer available"))
		return
	}
	if !isVideo(src) {
		writeErr(w, r, errf(400, "not_video", "Not a video file"))
		return
	}
	if req.SourceSHA256 != "" && req.SourceSHA256 != src.SHA256 {
		writeErr(w, r, errf(409, "source_changed", "The source video was replaced since the editor opened it; reopen it"))
		return
	}
	folder, err := s.st.Node(r.Context(), u.ID, req.FolderID)
	if err != nil || !folder.IsDir {
		writeErr(w, r, errf(400, "invalid_folder", "Choose a destination folder"))
		return
	}
	info, err := s.mediaInfo(r.Context(), u.ID, src.SHA256, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Names: cleaned, unique within the batch (the folder's existing files
	// are handled when each clip is stored: a taken name gets a number).
	used := map[string]bool{}
	prog := exportProgress{Current: -1, Quality: req.Quality, Erase: req.EraseOriginal,
		Original: exportOriginal{State: "kept", NodeID: src.ID, Name: src.Name, Size: src.Size}}
	prog.Folder, _ = s.st.NodePath(r.Context(), u.ID, folder.ID)
	var total int64
	for i := range req.Clips {
		c := &req.Clips[i]
		if err := c.Clip.Normalize(info); err != nil {
			writeErr(w, r, errf(400, "invalid_clip", "Clip #%d: %s", c.Number, err.Error()))
			return
		}
		mode := media.Mode(&c.Clip, req.Quality)
		_, ext := media.Container(mode, src.Name, info)
		base := cleanClipName(c.Name)
		if base == "" {
			base = fmt.Sprintf("%s #%d", strings.TrimSuffix(src.Name, path.Ext(src.Name)), c.Number)
		}
		name := base + ext
		for k := 2; used[strings.ToLower(name)]; k++ {
			name = fmt.Sprintf("%s (%d)%s", base, k, ext)
		}
		if _, err := pathutil.Split("/" + name); err != nil {
			writeErr(w, r, errf(400, "invalid_name", "Clip #%d: invalid file name", c.Number))
			return
		}
		used[strings.ToLower(name)] = true
		est := media.EstimateSize(&c.Clip, req.Quality, info)
		total += est
		prog.Clips = append(prog.Clips, exportClipState{Number: c.Number, Name: name, State: "pending", Mode: mode, Estimate: est})
	}
	if fresh, err := s.st.UserByID(r.Context(), u.ID); err == nil && fresh.QuotaBytes > 0 && fresh.UsedBytes+total > fresh.QuotaBytes {
		writeErr(w, r, &apiError{Status: 413, Code: "quota_exceeded", Message: "The clips would not fit in your storage quota",
			Details: map[string]any{"quota": fresh.QuotaBytes, "used": fresh.UsedBytes, "requested": total}})
		return
	}
	// One export of a source at a time.
	if active, err := s.st.VideoExports(r.Context(), u.ID, true); err == nil {
		for _, e := range active {
			if e.SourceNodeID == src.ID {
				writeErr(w, r, errf(409, "export_running", "This video is already being exported"))
				return
			}
		}
	}
	req.SourceSHA256 = src.SHA256 // the content the clips are cut from
	reqJSON, _ := json.Marshal(req)
	progJSON, _ := json.Marshal(prog)
	job, err := s.st.CreateVideoExport(r.Context(), u.ID, src.ID, string(reqJSON), string(progJSON))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "video.export_started", src.Name, fmt.Sprintf("%d clip(s) to %s", len(req.Clips), prog.Folder), false)
	ctx, cancel := context.WithCancel(s.ops.ctx)
	s.exports.mu.Lock()
	s.exports.cancels[job.ID] = cancel
	s.exports.mu.Unlock()
	s.ops.wg.Add(1)
	go func() {
		defer s.ops.wg.Done()
		defer cancel()
		s.runExport(ctx, job.ID, u.ID, req, prog, info)
		s.exports.mu.Lock()
		delete(s.exports.cancels, job.ID)
		s.exports.mu.Unlock()
	}()
	writeJSON(w, 202, viewExport(job))
}

func (s *Server) handleExportGet(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	e, err := s.st.VideoExport(r.Context(), principal(r).User.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, viewExport(e))
}

func (s *Server) handleExportList(w http.ResponseWriter, r *http.Request) {
	es, err := s.st.VideoExports(r.Context(), principal(r).User.ID, r.URL.Query().Get("active") == "1")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]exportView, 0, len(es))
	for i := range es {
		out = append(out, viewExport(&es[i]))
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) handleExportCancel(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	e, err := s.st.VideoExport(r.Context(), principal(r).User.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.exports.mu.Lock()
	cancel := s.exports.cancels[e.ID]
	s.exports.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// exportJob is one running export.
type exportJob struct {
	s     *Server
	id    int64
	mu    sync.Mutex
	prog  exportProgress
	state string
	saved time.Time
}

func (j *exportJob) save(force bool) {
	j.mu.Lock()
	if !force && time.Since(j.saved) < time.Second {
		j.mu.Unlock()
		return
	}
	j.saved = time.Now()
	b, _ := json.Marshal(j.prog)
	state := j.state
	j.mu.Unlock()
	if err := j.s.st.UpdateVideoExport(context.Background(), j.id, state, string(b)); err != nil {
		slog.Warn("export progress not saved", "export", j.id, "err", err)
	}
}

func (j *exportJob) clip(i int, f func(c *exportClipState)) {
	j.mu.Lock()
	f(&j.prog.Clips[i])
	j.mu.Unlock()
}

// exportUserMessage turns an error into the text shown for a clip.
func exportUserMessage(err error) string {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return ae.Message
	case errors.Is(err, store.ErrQuota):
		return "Storage quota exceeded"
	case errors.Is(err, storage.ErrInsufficientStorage):
		return "Not enough free space on the storage device"
	}
	return err.Error()
}

func (s *Server) runExport(ctx context.Context, id, userID int64, req exportReq, prog exportProgress, info *media.Info) {
	j := &exportJob{s: s, id: id, prog: prog, state: "queued"}
	select {
	case s.exports.slot <- struct{}{}:
		defer func() { <-s.exports.slot }()
	case <-ctx.Done():
		j.state = "cancelled"
		j.mu.Lock()
		for i := range j.prog.Clips {
			j.prog.Clips[i].State = "cancelled"
		}
		j.mu.Unlock()
		j.save(true)
		return
	}
	j.state = "running"
	j.save(true)
	fail := func(msg string) {
		j.mu.Lock()
		j.state, j.prog.Error, j.prog.Current = "failed", msg, -1
		for i := range j.prog.Clips {
			if j.prog.Clips[i].State == "pending" {
				j.prog.Clips[i].State = "cancelled"
			}
		}
		j.mu.Unlock()
		j.save(true)
	}
	user, err := s.st.UserByID(ctx, userID)
	if err != nil {
		fail("Your account could not be loaded")
		return
	}
	src, err := s.st.Node(ctx, userID, req.SourceID)
	if err != nil || src.SHA256 != req.SourceSHA256 {
		fail("The source video is no longer available")
		return
	}
	f, err := s.openBlob(ctx, userID, src.SHA256)
	if err != nil {
		fail("The source video's content is unavailable on the storage device")
		return
	}
	defer f.Close()
	folderPath, err := s.st.NodePath(ctx, userID, req.FolderID)
	if err != nil {
		fail("The destination folder no longer exists")
		return
	}
	dirSegs, err := pathutil.Split(folderPath)
	if err != nil && folderPath != "/" {
		fail("The destination folder is invalid")
		return
	}
	loc, err := s.locationFor(ctx, user)
	if err != nil {
		fail("No storage location is available")
		return
	}
	failed := 0
	for i := range req.Clips {
		if ctx.Err() != nil {
			break
		}
		c := req.Clips[i].Clip
		j.mu.Lock()
		j.prog.Current = i
		cs := j.prog.Clips[i]
		j.mu.Unlock()
		j.clip(i, func(c *exportClipState) { c.State = "running" })
		j.save(true)
		node, size, err := s.exportClip(ctx, j, i, user, f, info, src, &c, cs, dirSegs, loc)
		if err != nil {
			if ctx.Err() != nil {
				j.clip(i, func(c *exportClipState) { c.State, c.Error = "cancelled", "" })
				break
			}
			failed++
			msg := exportUserMessage(err)
			slog.Warn("clip export failed", "export", id, "clip", cs.Number, "err", err)
			j.clip(i, func(c *exportClipState) { c.State, c.Error = "failed", msg })
			j.save(true)
			continue
		}
		p, _ := s.st.NodePath(ctx, userID, node.ID)
		j.clip(i, func(c *exportClipState) {
			c.State, c.Progress, c.Size, c.NodeID, c.Path, c.Name = "done", 1, size, node.ID, p, node.Name
		})
		j.save(true)
		s.st.LogActivity(context.WithoutCancel(ctx), store.Activity{UserID: &userID, ActorID: &userID, Action: "video.clip_exported", Target: p, Details: "from " + src.Name})
	}
	cancelled := ctx.Err() != nil
	j.mu.Lock()
	j.prog.Current = -1
	for i := range j.prog.Clips {
		if j.prog.Clips[i].State == "pending" || j.prog.Clips[i].State == "running" {
			j.prog.Clips[i].State = "cancelled"
		}
	}
	j.mu.Unlock()
	// The original is only erased when every clip was stored.
	if req.EraseOriginal {
		bg := context.WithoutCancel(ctx)
		switch {
		case cancelled:
			j.prog.Original.State, j.prog.Original.Error = "failed", "The export was cancelled, so the original was kept"
		case failed > 0:
			j.prog.Original.State, j.prog.Original.Error = "failed", "Not every clip was exported, so the original was kept"
		default:
			if err := s.eraseOriginal(bg, user, src, &j.prog.Original); err != nil {
				j.prog.Original.State, j.prog.Original.Error = "failed", "The original could not be moved to the trash: "+exportUserMessage(err)
			}
		}
	}
	switch {
	case cancelled:
		j.state = "cancelled"
	case failed > 0:
		j.state = "failed"
		j.prog.Error = fmt.Sprintf("%d of %d clips failed", failed, len(req.Clips))
	default:
		j.state = "done"
	}
	j.save(true)
}

// locationFor is the storage location new files of the user go to.
func (s *Server) locationFor(ctx context.Context, u *store.User) (int64, error) {
	if u.LocationID != nil {
		if l, ok := s.blobs.Location(*u.LocationID); ok && l.Enabled {
			return l.ID, nil
		}
	}
	l, err := s.st.DefaultLocation(ctx)
	if err != nil {
		return 0, err
	}
	return l.ID, nil
}

// exportClip makes, checks and stores one clip.
func (s *Server) exportClip(ctx context.Context, j *exportJob, i int, user *store.User, in *os.File, info *media.Info, src *store.Node, c *media.Clip, cs exportClipState, dirSegs []string, loc int64) (*store.Node, int64, error) {
	if fresh, err := s.st.UserByID(ctx, user.ID); err == nil && fresh.QuotaBytes > 0 && fresh.UsedBytes+cs.Estimate > fresh.QuotaBytes {
		return nil, 0, store.ErrQuota
	}
	// Room for the clip (estimates can be low) plus a margin.
	if err := s.blobs.CheckSpace(ctx, loc, cs.Estimate+cs.Estimate/4+64<<20); err != nil {
		return nil, 0, err
	}
	tmp, err := s.blobs.TempPath(loc, "vx"+auth.RandomToken(15))
	if err != nil {
		return nil, 0, err
	}
	defer storage.RemoveTemp(tmp) // gone after Place moved it; removes it on failure
	format, _ := media.Container(cs.Mode, src.Name, info)
	want := c.OutputDuration()
	args := media.ExportArgs(c, cs.Mode, info, format, tmp)
	err = s.media.tools.Export(ctx, in, args, func(out float64) {
		if want > 0 {
			p := math.Min(out/want, 0.99)
			j.clip(i, func(c *exportClipState) { c.Progress = p })
			j.save(false)
		}
	})
	if err != nil {
		return nil, 0, err
	}
	if cs.Mode == media.QualityCopy {
		want = c.End - c.Start
	}
	outInfo, err := s.media.tools.Verify(ctx, tmp, want, info.HasAudio() && !c.Muted)
	if err != nil {
		return nil, 0, err
	}
	sum, err := storage.HashFile(tmp)
	if err != nil {
		return nil, 0, err
	}
	fi, err := os.Stat(tmp)
	if err != nil {
		return nil, 0, err
	}
	var res *store.CommitResult
	err = s.blobs.Place(ctx, user.ID, sum, tmp, loc, func(l int64) error {
		var cerr error
		res, cerr = s.st.CommitFile(ctx, store.CommitRequest{
			User: user, DirSegs: dirSegs, Name: cs.Name, Size: fi.Size(), SHA256: sum, Mime: mimetypes.ByName(cs.Name),
			ModifiedAt: time.Now().UnixMilli(), OnConflict: "rename", LocationID: l, KeepReplaced: s.settings.Get().KeepReplacedVersions,
		})
		return cerr
	})
	if err != nil {
		return nil, 0, err
	}
	s.blobs.Remove(context.WithoutCancel(ctx), res.Freed)
	_ = s.media.cache.PutInfo(user.ID, sum, outInfo)
	s.mediaCommitted(user.ID, res.Node)
	return res.Node, fi.Size(), nil
}

// eraseOriginal moves the source to the trash after a complete export.
func (s *Server) eraseOriginal(ctx context.Context, user *store.User, src *store.Node, o *exportOriginal) error {
	cur, err := s.st.Node(ctx, user.ID, src.ID)
	if err != nil {
		return errf(404, "source_missing", "it is no longer in its folder")
	}
	if cur.SHA256 != src.SHA256 {
		return errf(409, "source_changed", "it was replaced by other content during the export")
	}
	p, _ := s.st.NodePath(ctx, user.ID, cur.ID)
	if err := s.st.Trash(ctx, user, cur.ID); err != nil {
		return err
	}
	expires := time.Now().Add(eraseTrashDays * 24 * time.Hour).UnixMilli()
	if err := s.st.SetTrashExpiry(ctx, user.ID, cur.ID, expires); err != nil {
		slog.Warn("trash expiry not set", "node", cur.ID, "err", err)
	}
	if err := s.st.BlockReupload(ctx, user.ID, p, cur.SHA256, time.Now().Add(reuploadBlockFor).UnixMilli()); err != nil {
		slog.Warn("re-upload block not stored", "node", cur.ID, "err", err)
	}
	o.State, o.ExpiresAt = "trashed", expires
	s.st.LogActivity(ctx, store.Activity{UserID: &user.ID, ActorID: &user.ID, Action: "file.trashed", Target: p, Details: "original erased after exporting clips; permanently deleted after 7 days"})
	return nil
}
