package api

// Full library: administrators browse, play and download the files of
// every user. It is read-only (nothing can be moved, renamed or deleted
// here) and every file opened or downloaded is recorded in the audit log.

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/media"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func (s *Server) mountLibrary(rt routes) {
	rt.Admin.Get("/admin/library", s.handleLibrary)
	rt.Admin.Get("/admin/library/owners", s.handleLibraryOwners)
	rt.Admin.Get("/admin/library/{id}/content", s.handleLibraryContent)
	rt.Admin.Get("/admin/library/{id}/thumbnail", s.handleLibraryThumbnail)
}

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	lq := store.LibraryQuery{Kind: q.Get("kind"), Q: q.Get("q"), Clips: q.Get("clips"), Sort: q.Get("sort"), Desc: q.Get("desc") != "0"}
	lq.UserID, _ = strconv.ParseInt(q.Get("user_id"), 10, 64)
	lq.Limit, _ = strconv.Atoi(q.Get("limit"))
	lq.Offset, _ = strconv.Atoi(q.Get("offset"))
	if len(lq.Q) > 200 {
		writeErr(w, r, errf(400, "invalid_query", "The search is too long"))
		return
	}
	items, total, bytes, err := s.st.Library(r.Context(), lq)
	if err != nil {
		writeErr(w, r, errf(400, "invalid_query", "%s", err.Error()))
		return
	}
	for i := range items {
		items[i].Path, _ = s.st.NodePath(r.Context(), items[i].UserID, items[i].ID)
	}
	writeJSON(w, 200, map[string]any{"items": items, "total": total, "bytes": bytes})
}

func (s *Server) handleLibraryOwners(w http.ResponseWriter, r *http.Request) {
	owners, err := s.st.LibraryOwners(r.Context(), r.URL.Query().Get("kind"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"owners": owners})
}

func (s *Server) libraryNode(r *http.Request) (*store.Node, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	return s.st.NodeAnyUser(r.Context(), id)
}

// auditLibrary records an administrator opening another user's file (a
// view once an hour per file; every download).
func (s *Server) auditLibrary(r *http.Request, n *store.Node, download bool) {
	admin := principal(r).User
	action := "admin.library_viewed"
	if download {
		action = "admin.library_downloaded"
	} else if !s.shouldTouch(fmt.Sprintf("lib:%d:%d:%d", admin.ID, n.ID, time.Now().Unix()/3600)) {
		return
	}
	p, _ := s.st.NodePath(r.Context(), n.Owner(), n.ID)
	owner := n.Owner()
	s.st.LogActivity(r.Context(), store.Activity{UserID: &owner, ActorID: &admin.ID, Action: action, Target: p, Details: "by administrator " + admin.Username, IP: s.clientIP(r), Audit: true})
}

func (s *Server) handleLibraryContent(w http.ResponseWriter, r *http.Request) {
	n, err := s.libraryNode(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	uid := n.Owner()
	f, err := s.openBlob(r.Context(), uid, n.SHA256)
	if err != nil {
		slog.Error("blob unavailable", "node", n.ID, "err", err)
		writeErr(w, r, contentError(err))
		return
	}
	defer f.Close()
	download := r.URL.Query().Get("download") == "1"
	h := w.Header()
	ctype := n.Mime
	if ctype == "" || !inlineSafe(ctype) {
		ctype = "application/octet-stream"
		download = true
	}
	if download {
		h.Set("Content-Disposition", contentDisposition("attachment", n.Name))
	} else {
		h.Set("Content-Disposition", contentDisposition("inline", n.Name))
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	h.Set("ETag", `"`+n.SHA256+`"`)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	s.auditLibrary(r, n, download)
	if err := precheck(r, f); err != nil {
		writeErr(w, r, contentError(err))
		return
	}
	http.ServeContent(w, r, n.Name, time.UnixMilli(n.ModifiedAt), f)
}

func (s *Server) handleLibraryThumbnail(w http.ResponseWriter, r *http.Request) {
	n, err := s.libraryNode(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !isVideo(n) {
		writeErr(w, r, errf(400, "not_video", "Thumbnails are made for videos only"))
		return
	}
	uid := n.Owner()
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
