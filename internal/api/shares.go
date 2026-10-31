package api

// Public video links: a user shares one video with anyone who has the
// link, for a limited time or until revoked. The public page (/s/<token>)
// is served by the web app; this file has the owner API, the public API
// (metadata, video stream, poster) and the link preview tags that chat
// apps read from /s/<token>.
//
// A link plays the exact content that was shared: when the file is
// changed, trashed or deleted the link stops working. Links only expose
// the video, its title and description and the sharer's display name.

import (
	"bytes"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const maxShareDays = 3650

func (s *Server) mountShares(rt routes) {
	rt.Sess.Get("/shares", s.handleShares)
	rt.Sess.Post("/shares", s.handleShareCreate)
	rt.Sess.Patch("/shares/{id}", s.handleSharePatch)
	rt.Sess.Delete("/shares/{id}", s.handleShareRevoke)
}

// mountPublicShares registers the unauthenticated routes.
func (s *Server) mountPublicShares(r chi.Router) {
	r.Get("/public/shares/{token}", s.handlePublicShare)
	r.Get("/public/shares/{token}/video", s.handlePublicShareVideo)
	r.Head("/public/shares/{token}/video", s.handlePublicShareVideo)
	r.Get("/public/shares/{token}/poster", s.handlePublicSharePoster)
}

type shareView struct {
	store.Share
	URL      string `json:"url"`
	Name     string `json:"name"`
	State    string `json:"state"` // active, expired, revoked, unavailable
	FilePath string `json:"path,omitempty"`
}

func (s *Server) shareView(r *http.Request, h store.Share) shareView {
	v := shareView{Share: h, URL: "/s/" + h.Token, State: "active"}
	n, err := s.st.Node(r.Context(), h.UserID, h.NodeID)
	if err == nil {
		v.Name = n.Name
		v.FilePath, _ = s.st.NodePath(r.Context(), h.UserID, n.ID)
	}
	switch {
	case h.RevokedAt != nil:
		v.State = "revoked"
	case h.ExpiresAt != nil && *h.ExpiresAt <= time.Now().UnixMilli():
		v.State = "expired"
	case err != nil || n.SHA256 != h.SHA256:
		v.State = "unavailable"
	}
	return v
}

func (s *Server) handleShares(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	nodeID, _ := strconv.ParseInt(r.URL.Query().Get("node_id"), 10, 64)
	list, err := s.st.Shares(r.Context(), u.ID, nodeID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]shareView, 0, len(list))
	for _, h := range list {
		out = append(out, s.shareView(r, h))
	}
	writeJSON(w, 200, map[string]any{"shares": out})
}

// shareExpiry turns "expires_in" seconds into a time (0: never).
func shareExpiry(sec int64) (*int64, error) {
	if sec < 0 || sec > maxShareDays*86400 {
		return nil, errf(400, "invalid_expiry", "Links can last up to %d days", maxShareDays)
	}
	if sec == 0 {
		return nil, nil
	}
	t := time.Now().Add(time.Duration(sec) * time.Second).UnixMilli()
	return &t, nil
}

func cleanShareText(title, desc string) (string, string, error) {
	title, desc = strings.TrimSpace(title), strings.TrimSpace(desc)
	if title == "" || len([]rune(title)) > 120 {
		return "", "", errf(400, "invalid_title", "Give the video a title (up to 120 characters)")
	}
	if len([]rune(desc)) > 2000 {
		return "", "", errf(400, "invalid_description", "The description can be up to 2000 characters")
	}
	return title, desc, nil
}

func (s *Server) handleShareCreate(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		NodeID        int64  `json:"node_id"`
		Title         string `json:"title"`
		Description   string `json:"description"`
		ExpiresIn     int64  `json:"expires_in"`
		AllowDownload bool   `json:"allow_download"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.Node(r.Context(), u.ID, req.NodeID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !isVideo(n) {
		writeErr(w, r, errf(400, "not_video", "Only videos can be shared publicly"))
		return
	}
	title, desc, err := cleanShareText(req.Title, req.Description)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	exp, err := shareExpiry(req.ExpiresIn)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h, err := s.st.CreateShare(r.Context(), store.Share{UserID: u.ID, NodeID: n.ID, SHA256: n.SHA256, Token: auth.RandomToken(18), Title: title, Description: desc,
		AllowDownload: req.AllowDownload, ExpiresAt: exp})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, _ := s.st.NodePath(r.Context(), u.ID, n.ID)
	when := "never expires"
	if exp != nil {
		when = "expires " + time.UnixMilli(*exp).UTC().Format(time.RFC3339)
	}
	s.activity(r, u.ID, "file.shared", p, "public link; "+when, false)
	// Make sure the poster exists for link previews.
	s.media.enqueueThumb(u.ID, n.SHA256)
	writeJSON(w, 201, s.shareView(r, *h))
}

func (s *Server) handleSharePatch(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h, err := s.st.Share(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Title         *string `json:"title"`
		Description   *string `json:"description"`
		ExpiresIn     *int64  `json:"expires_in"`
		AllowDownload *bool   `json:"allow_download"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	title, desc := h.Title, h.Description
	if req.Title != nil {
		title = *req.Title
	}
	if req.Description != nil {
		desc = *req.Description
	}
	if h.Title, h.Description, err = cleanShareText(title, desc); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.ExpiresIn != nil {
		if h.ExpiresAt, err = shareExpiry(*req.ExpiresIn); err != nil {
			writeErr(w, r, err)
			return
		}
	}
	if req.AllowDownload != nil {
		h.AllowDownload = *req.AllowDownload
	}
	if err := s.st.UpdateShare(r.Context(), *h); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, s.shareView(r, *h))
}

func (s *Server) handleShareRevoke(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h, err := s.st.Share(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.RevokeShare(r.Context(), u.ID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "file.unshared", h.Title, "public link revoked", false)
	w.WriteHeader(204)
}

// --- public ---

// publicShare resolves a link to its share and file; any problem is a
// plain "not available" (expired links say so).
func (s *Server) publicShare(r *http.Request) (*store.Share, *store.Node, error) {
	tok := chi.URLParam(r, "token")
	if len(tok) < 16 || len(tok) > 64 {
		return nil, nil, errf(404, "not_found", "This link does not exist")
	}
	h, err := s.st.ShareByToken(r.Context(), tok)
	if err != nil || h.RevokedAt != nil {
		return nil, nil, errf(404, "not_found", "This link does not exist or was removed")
	}
	if h.ExpiresAt != nil && *h.ExpiresAt <= time.Now().UnixMilli() {
		return nil, nil, errf(410, "expired", "This link has expired")
	}
	n, err := s.st.Node(r.Context(), h.UserID, h.NodeID)
	if err != nil || n.SHA256 != h.SHA256 {
		return nil, nil, errf(404, "unavailable", "This video is no longer available")
	}
	return h, n, nil
}

func (s *Server) handlePublicShare(w http.ResponseWriter, r *http.Request) {
	h, n, err := s.publicShare(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if s.shouldTouch(fmt.Sprintf("sv:%d:%s:%d", h.ID, s.clientIP(r), time.Now().Unix()/3600)) {
		s.st.CountShareView(r.Context(), h.ID)
	}
	by := ""
	if u, err := s.st.UserByID(r.Context(), h.UserID); err == nil {
		by = u.DisplayName
		if by == "" {
			by = u.Username
		}
	}
	date := n.ModifiedAt
	if n.FileCreatedAt != nil {
		date = *n.FileCreatedAt
	}
	out := map[string]any{"title": h.Title, "description": h.Description, "name": n.Name, "size": n.Size, "mime": n.Mime, "recorded_at": date,
		"shared_at": h.CreatedAt, "expires_at": h.ExpiresAt, "allow_download": h.AllowDownload, "shared_by": by, "site": s.settings.Get().SiteName}
	if info, err := s.mediaInfo(r.Context(), h.UserID, n.SHA256, nil); err == nil {
		out["duration"], out["width"], out["height"] = info.Duration, info.Width, info.Height
	}
	writeJSON(w, 200, out)
}

func (s *Server) handlePublicShareVideo(w http.ResponseWriter, r *http.Request) {
	h, n, err := s.publicShare(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	download := r.URL.Query().Get("download") == "1"
	if download && !h.AllowDownload {
		writeErr(w, r, errf(403, "download_disabled", "Downloading is not allowed for this video"))
		return
	}
	f, err := s.openBlob(r.Context(), h.UserID, n.SHA256)
	if err != nil {
		slog.Error("shared video unavailable", "share", h.ID, "err", err)
		writeErr(w, r, errf(503, "content_unavailable", "The video is temporarily unavailable"))
		return
	}
	defer f.Close()
	hd := w.Header()
	name := h.Title
	if ext := strings.LastIndex(n.Name, "."); ext > 0 {
		name += n.Name[ext:]
	}
	if download {
		hd.Set("Content-Disposition", contentDisposition("attachment", name))
	} else {
		hd.Set("Content-Disposition", contentDisposition("inline", name))
	}
	hd.Set("Content-Type", n.Mime)
	hd.Set("Content-Security-Policy", "default-src 'none'; media-src 'self'; sandbox")
	hd.Set("Cache-Control", "private, max-age=0, must-revalidate")
	hd.Set("ETag", `"`+n.SHA256+`"`)
	hd.Set("X-Robots-Tag", "noindex")
	http.ServeContent(w, r, name, time.UnixMilli(n.ModifiedAt), f)
}

func (s *Server) handlePublicSharePoster(w http.ResponseWriter, r *http.Request) {
	h, n, err := s.publicShare(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p := s.media.cache.PosterPath(h.UserID, n.SHA256)
	fi, err := os.Stat(p)
	if err != nil {
		s.media.enqueueThumb(h.UserID, n.SHA256)
		writeErr(w, r, errf(404, "poster_pending", "The preview image is being made"))
		return
	}
	serveCachedImage(w, r, p, n.SHA256, fi)
}

// shareMeta serves the web app for /s/<token> with link preview tags
// (Open Graph, Twitter) so chat apps show the title and poster.
func (s *Server) shareMeta(web http.Handler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rec := httptest.NewRecorder()
		idx := r.Clone(r.Context())
		idx.URL.Path = "/"
		web.ServeHTTP(rec, idx)
		body := rec.Body.Bytes()
		for k, v := range rec.Header() {
			w.Header()[k] = v
		}
		w.Header().Set("X-Robots-Tag", "noindex")
		if h, n, err := s.publicShare(r); err == nil && rec.Code == 200 {
			base := s.cfg.PublicURL
			if base == "" {
				scheme := "http"
				if s.isHTTPS(r) {
					scheme = "https"
				}
				base = scheme + "://" + r.Host
			}
			link := base + "/s/" + h.Token
			api := base + "/api/v1/public/shares/" + h.Token
			desc := h.Description
			if desc == "" {
				desc = "Video shared with VaultDock"
			}
			e := html.EscapeString
			tags := fmt.Sprintf(`<title>%s</title>
<meta name="robots" content="noindex">
<meta property="og:type" content="video.other">
<meta property="og:title" content="%s">
<meta property="og:description" content="%s">
<meta property="og:url" content="%s">
<meta property="og:image" content="%s/poster">
<meta property="og:video" content="%s/video">
<meta property="og:video:secure_url" content="%s/video">
<meta property="og:video:type" content="%s">
<meta name="twitter:card" content="summary_large_image">
<meta name="twitter:title" content="%s">
<meta name="twitter:image" content="%s/poster">
`, e(h.Title), e(h.Title), e(desc), e(link), e(api), e(api), e(api), e(n.Mime), e(h.Title), e(api))
			if info, err := s.mediaInfo(r.Context(), h.UserID, n.SHA256, nil); err == nil && info.Width > 0 {
				tags += fmt.Sprintf("<meta property=\"og:video:width\" content=\"%d\">\n<meta property=\"og:video:height\" content=\"%d\">\n", info.Width, info.Height)
			}
			body = bytes.Replace(body, []byte("<head>"), []byte("<head>\n"+tags), 1)
		}
		w.Header().Del("Content-Length")
		w.WriteHeader(rec.Code)
		_, _ = w.Write(body)
	}
}
