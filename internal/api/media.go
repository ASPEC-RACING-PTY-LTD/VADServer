package api

// Video thumbnails, timeline frames and stream information (FFmpeg on the
// server). Thumbnails are made in the background, once per stored content
// (see media.Cache), for new uploads as they finish and for older files
// the first time they are shown; the file browser asks again while a
// thumbnail is pending (202) and falls back to an icon when the file cannot
// be read.

import (
	"context"
	"log/slog"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type thumbJob struct {
	userID int64
	sha    string
}

type mediaService struct {
	tools media.Tools
	cache media.Cache
	queue chan thumbJob

	mu      sync.Mutex
	pending map[thumbJob]bool
	probing map[thumbJob]chan struct{}
	version string

	frames chan struct{} // timeline frames extracted at once
}

func newMediaService(stateDir string) *mediaService {
	return &mediaService{
		tools:   media.Detect(),
		cache:   media.Cache{Dir: filepath.Join(stateDir, "cache", "media")},
		queue:   make(chan thumbJob, 4096),
		pending: map[thumbJob]bool{},
		probing: map[thumbJob]chan struct{}{},
		frames:  make(chan struct{}, 3),
	}
}

func isVideo(n *store.Node) bool {
	return n != nil && !n.IsDir && n.SHA256 != "" && strings.HasPrefix(n.Mime, "video/")
}

// enqueueThumb asks the background workers for a thumbnail; it reports
// false when the queue is full (the next request asks again).
func (m *mediaService) enqueueThumb(userID int64, sha string) bool {
	if !m.tools.Available() {
		return false
	}
	j := thumbJob{userID, sha}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending[j] {
		return true
	}
	select {
	case m.queue <- j:
		m.pending[j] = true
		return true
	default:
		return false
	}
}

// mediaCommitted starts the thumbnail of a newly stored video.
func (s *Server) mediaCommitted(userID int64, n *store.Node) {
	if isVideo(n) {
		if _, err := os.Stat(s.media.cache.PosterPath(userID, n.SHA256)); err != nil {
			s.media.enqueueThumb(userID, n.SHA256)
		}
	}
}

// openBlob opens a user's stored content.
func (s *Server) openBlob(ctx context.Context, userID int64, sha string) (*os.File, error) {
	loc, err := s.st.BlobLocation(ctx, userID, sha)
	if err != nil {
		return nil, err
	}
	return s.blobs.Open(loc, userID, sha)
}

// mediaInfo returns the stream information of stored content, probing it
// (once, however many ask at the same time) when it is not cached.
func (s *Server) mediaInfo(ctx context.Context, userID int64, sha string, f *os.File) (*media.Info, error) {
	m := s.media
	if i, ok := m.cache.Info(userID, sha); ok {
		return i, nil
	}
	if reason, failed := m.cache.Failed(userID, sha); failed {
		return nil, errf(422, "video_unreadable", "This video cannot be read: %s", reason)
	}
	if !m.tools.Available() {
		return nil, errf(501, "media_unavailable", "%s", media.ErrUnavailable.Error())
	}
	j := thumbJob{userID, sha}
	m.mu.Lock()
	if ch, ok := m.probing[j]; ok {
		m.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return s.mediaInfo(ctx, userID, sha, f)
	}
	ch := make(chan struct{})
	m.probing[j] = ch
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.probing, j)
		m.mu.Unlock()
		close(ch)
	}()
	if f == nil {
		var err error
		if f, err = s.openBlob(ctx, userID, sha); err != nil {
			return nil, errf(503, "content_unavailable", "File content is unavailable on the storage device")
		}
		defer f.Close()
	}
	info, err := m.tools.Probe(ctx, f)
	if err != nil {
		if ctx.Err() == nil {
			m.cache.PutFailed(userID, sha, err.Error())
		}
		return nil, errf(422, "video_unreadable", "This video cannot be read: %s", err.Error())
	}
	_ = m.cache.PutInfo(userID, sha, info)
	return info, nil
}

// makeThumb generates the browser thumbnail.
func (s *Server) makeThumb(ctx context.Context, j thumbJob) {
	m := s.media
	poster := m.cache.PosterPath(j.userID, j.sha)
	if _, err := os.Stat(poster); err == nil {
		return
	}
	if _, failed := m.cache.Failed(j.userID, j.sha); failed {
		return
	}
	f, err := s.openBlob(ctx, j.userID, j.sha)
	if err != nil {
		return // gone or its storage offline: asked again on the next view
	}
	defer f.Close()
	info, err := s.mediaInfo(ctx, j.userID, j.sha, f)
	if err != nil {
		return
	}
	img, err := m.tools.Frame(ctx, f, media.PosterTime(info.Duration), 360, 4)
	if err != nil && ctx.Err() == nil {
		img, err = m.tools.Frame(ctx, f, 0, 360, 4)
	}
	if err != nil {
		if ctx.Err() == nil {
			m.cache.PutFailed(j.userID, j.sha, err.Error())
			slog.Info("video thumbnail failed", "user", j.userID, "sha", j.sha, "err", err)
		}
		return
	}
	if err := m.cache.Put(poster, img); err != nil {
		slog.Warn("video thumbnail not cached", "err", err)
	}
}

// RunMedia runs the thumbnail workers and marks clip exports a previous
// process left unfinished.
func (s *Server) RunMedia(ctx context.Context) {
	if jobs, err := s.st.InterruptVideoExports(ctx); err == nil && len(jobs) > 0 {
		slog.Info("clip exports interrupted by a restart", "count", len(jobs))
	}
	if !s.media.tools.Available() {
		slog.Info("FFmpeg not found: video thumbnails and the video editor are off (set VAULTDOCK_FFMPEG and VAULTDOCK_FFPROBE or install ffmpeg)")
		return
	}
	s.media.version = s.media.tools.Version(ctx)
	slog.Info("video processing ready", "ffmpeg", s.media.tools.FFmpeg, "version", s.media.version)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-s.media.queue:
					s.makeThumb(ctx, j)
					s.media.mu.Lock()
					delete(s.media.pending, j)
					s.media.mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()
}

// sweepMedia drops cached thumbnails of content no longer stored and old
// timeline frames.
func (s *Server) sweepMedia(ctx context.Context) {
	n := s.media.cache.Sweep(ctx, func(userID int64, sha string) bool {
		ok, err := s.st.BlobExists(ctx, userID, sha)
		return ok || err != nil // keep on database errors
	})
	if n > 0 {
		slog.Info("video cache cleaned", "removed", n)
	}
}

func (s *Server) handleMediaStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"available": s.media.tools.Available(), "version": s.media.version})
}

// videoNode loads a video file of the caller.
func (s *Server) videoNode(r *http.Request) (*store.Node, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	n, err := s.st.Node(r.Context(), principal(r).User.ID, id)
	if err != nil {
		return nil, err
	}
	if !isVideo(n) {
		return nil, errf(400, "not_video", "Not a video file")
	}
	return n, nil
}

func serveCachedImage(w http.ResponseWriter, r *http.Request, p, etag string, fi os.FileInfo) {
	h := w.Header()
	h.Set("Content-Type", "image/jpeg")
	h.Set("ETag", `"`+etag+`"`)
	if r.URL.Query().Get("v") != "" {
		h.Set("Cache-Control", "private, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	}
	media.Touch(p, fi)
	http.ServeFile(w, r, p)
}

// GET /files/{id}/thumbnail: 200 image, 202 while it is made, 404 when the
// video cannot be read (or FFmpeg is missing).
func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request) {
	n, err := s.videoNode(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	uid := principal(r).User.ID
	p := s.media.cache.PosterPath(uid, n.SHA256)
	if fi, err := os.Stat(p); err == nil {
		serveCachedImage(w, r, p, n.SHA256, fi)
		return
	}
	if reason, failed := s.media.cache.Failed(uid, n.SHA256); failed {
		writeErr(w, r, errf(404, "thumbnail_failed", "No thumbnail: %s", reason))
		return
	}
	if !s.media.tools.Available() {
		writeErr(w, r, errf(404, "media_unavailable", "%s", media.ErrUnavailable.Error()))
		return
	}
	s.media.enqueueThumb(uid, n.SHA256)
	w.Header().Set("Retry-After", "2")
	writeJSON(w, 202, map[string]any{"state": "pending"})
}

type mediaItem struct {
	ID       int64   `json:"id"`
	State    string  `json:"state"` // ready, pending, failed, unavailable
	Duration float64 `json:"duration,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Thumb    bool    `json:"thumbnail"`
}

// GET /files/media?ids=1,2,3: thumbnail state and duration of several
// videos (the file browser's cards); missing thumbnails are queued.
func (s *Server) handleMediaBatch(w http.ResponseWriter, r *http.Request) {
	uid := principal(r).User.ID
	var items []mediaItem
	for i, part := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if i >= 300 {
			break
		}
		id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
		if err != nil || id <= 0 {
			continue
		}
		n, err := s.st.Node(r.Context(), uid, id)
		if err != nil || !isVideo(n) {
			continue
		}
		it := mediaItem{ID: id, State: "pending"}
		if info, ok := s.media.cache.Info(uid, n.SHA256); ok {
			it.Duration, it.Width, it.Height = info.Duration, info.Width, info.Height
		}
		if _, err := os.Stat(s.media.cache.PosterPath(uid, n.SHA256)); err == nil {
			it.State, it.Thumb = "ready", true
		} else if _, failed := s.media.cache.Failed(uid, n.SHA256); failed {
			it.State = "failed"
		} else if !s.media.tools.Available() {
			it.State = "unavailable"
		} else {
			s.media.enqueueThumb(uid, n.SHA256)
		}
		items = append(items, it)
	}
	if items == nil {
		items = []mediaItem{}
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

// GET /files/{id}/media: stream information of a video.
func (s *Server) handleMediaInfo(w http.ResponseWriter, r *http.Request) {
	n, err := s.videoNode(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	info, err := s.mediaInfo(r.Context(), principal(r).User.ID, n.SHA256, nil)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"node": n, "info": info})
}

// GET /files/{id}/frame?t=SECONDS&h=PIXELS: one frame for the editor's
// timeline. Times are rounded to 10 ms so neighbouring requests share
// cache entries; at most a few frames are extracted at once.
func (s *Server) handleFrame(w http.ResponseWriter, r *http.Request) {
	n, err := s.videoNode(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	t, err := strconv.ParseFloat(q.Get("t"), 64)
	if err != nil || t < 0 || math.IsNaN(t) || math.IsInf(t, 0) {
		writeErr(w, r, errf(400, "bad_time", "Invalid time"))
		return
	}
	h, _ := strconv.Atoi(q.Get("h"))
	h = min(max(h, 32), 360)
	h -= h % 2
	ms := int64(math.Round(t*100)) * 10
	uid := principal(r).User.ID
	p := s.media.cache.FramePath(uid, n.SHA256, h, ms)
	etag := n.SHA256[:16] + "-" + strconv.Itoa(h) + "-" + strconv.FormatInt(ms, 10)
	if fi, err := os.Stat(p); err == nil {
		serveCachedImage(w, r, p, etag, fi)
		return
	}
	if !s.media.tools.Available() {
		writeErr(w, r, errf(501, "media_unavailable", "%s", media.ErrUnavailable.Error()))
		return
	}
	select {
	case s.media.frames <- struct{}{}:
		defer func() { <-s.media.frames }()
	case <-r.Context().Done():
		return
	case <-time.After(30 * time.Second):
		writeErr(w, r, errf(503, "busy", "The server is busy making frames; try again"))
		return
	}
	if fi, err := os.Stat(p); err == nil { // made while this request waited
		serveCachedImage(w, r, p, etag, fi)
		return
	}
	f, err := s.openBlob(r.Context(), uid, n.SHA256)
	if err != nil {
		writeErr(w, r, errf(503, "content_unavailable", "File content is unavailable on the storage device"))
		return
	}
	defer f.Close()
	img, err := s.media.tools.Frame(r.Context(), f, float64(ms)/1000, h, 6)
	if err != nil {
		if r.Context().Err() == nil {
			writeErr(w, r, errf(422, "no_frame", "%s", err.Error()))
		}
		return
	}
	_ = s.media.cache.Put(p, img)
	if fi, err := os.Stat(p); err == nil {
		serveCachedImage(w, r, p, etag, fi)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	_, _ = w.Write(img)
}
