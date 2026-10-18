package api

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pathutil"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func (s *Server) parentParam(r *http.Request, u *store.User) (int64, error) {
	v := r.URL.Query().Get("parent")
	if v == "" || v == "root" {
		return u.RootID, nil
	}
	id, err := strconv.ParseInt(v, 10, 64)
	if err != nil || id <= 0 {
		return 0, errf(400, "bad_id", "Invalid parent id")
	}
	return id, nil
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	parentID, err := s.parentParam(r, u)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	parent, err := s.st.Node(r.Context(), u.ID, parentID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !parent.IsDir {
		writeErr(w, r, errf(400, "not_folder", "Not a folder"))
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	items, total, err := s.st.ListChildren(r.Context(), u.ID, parentID, store.ListOptions{
		Sort: q.Get("sort"), Desc: q.Get("order") == "desc", Limit: limit, Offset: max(0, offset), Filter: q.Get("q"), Kind: q.Get("kind"),
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	crumbs, err := s.st.Ancestors(r.Context(), u.ID, parentID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"folder": parent, "breadcrumbs": crumbs, "items": items, "total": total})
}

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	segs, err := pathutil.Split(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, r, errf(400, "invalid_path", "%s", err.Error()))
		return
	}
	n, err := s.st.Resolve(r.Context(), u, segs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n2, err := s.st.NodeWithTags(r.Context(), u.ID, n.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"node": n2})
}

func (s *Server) handleGetNode(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.NodeWithTags(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	crumbs, _ := s.st.Ancestors(r.Context(), u.ID, id)
	writeJSON(w, 200, map[string]any{"node": n, "breadcrumbs": crumbs})
}

func (s *Server) handleCreateFolder(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req struct {
		ParentID int64  `json:"parent_id"`
		Name     string `json:"name"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if err := pathutil.ValidateName(req.Name); err != nil {
		writeErr(w, r, errf(400, "invalid_name", "%s", err.Error()))
		return
	}
	if req.ParentID == 0 {
		req.ParentID = u.RootID
	}
	n, err := s.st.CreateFolder(r.Context(), u.ID, req.ParentID, req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "folder.created", n.Path, "", false)
	writeJSON(w, 201, map[string]any{"node": n})
}

func (s *Server) handleEnsureFolder(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req struct {
		Path string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	segs, err := pathutil.Split(req.Path)
	if err != nil {
		writeErr(w, r, errf(400, "invalid_path", "%s", err.Error()))
		return
	}
	id, err := s.st.EnsureDirs(r.Context(), s.st.DB, p.User, segs, p.deviceID())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.NodeWithTags(r.Context(), p.User.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"node": n})
}

func (s *Server) handlePatchNode(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Name     *string `json:"name"`
		ParentID *int64  `json:"parent_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Name != nil {
		*req.Name = strings.TrimSpace(*req.Name)
		if err := pathutil.ValidateName(*req.Name); err != nil {
			writeErr(w, r, errf(400, "invalid_name", "%s", err.Error()))
			return
		}
	}
	before, _ := s.st.NodePath(r.Context(), u.ID, id)
	n, err := s.st.MoveRename(r.Context(), u, id, req.ParentID, req.Name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	action := "file.renamed"
	if req.ParentID != nil {
		action = "file.moved"
	}
	s.activity(r, u.ID, action, n.Path, "from "+before, false)
	writeJSON(w, 200, map[string]any{"node": n})
}

type idsReq struct {
	IDs      []int64 `json:"ids"`
	ParentID int64   `json:"parent_id"`
}

func (s *Server) handleBatchMove(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req idsReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		writeErr(w, r, errf(400, "bad_ids", "Select between 1 and 1000 items"))
		return
	}
	var failed []map[string]any
	moved := 0
	for _, id := range req.IDs {
		pid := req.ParentID
		if _, err := s.st.MoveRename(r.Context(), u, id, &pid, nil); err != nil {
			msg := err.Error()
			var ae *apiError
			if errors.As(err, &ae) {
				msg = ae.Message
			} else if errors.Is(err, store.ErrExists) {
				msg = "an item with that name already exists in the destination"
			}
			failed = append(failed, map[string]any{"id": id, "error": msg})
			continue
		}
		moved++
	}
	dest, _ := s.st.NodePath(r.Context(), u.ID, req.ParentID)
	s.activity(r, u.ID, "file.moved", dest, fmt.Sprintf("%d item(s)", moved), false)
	writeJSON(w, 200, map[string]any{"moved": moved, "failed": failed})
}

func (s *Server) handleBatchDelete(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var req idsReq
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.IDs) == 0 || len(req.IDs) > 1000 {
		writeErr(w, r, errf(400, "bad_ids", "Select between 1 and 1000 items"))
		return
	}
	deleted := 0
	var failed []map[string]any
	for _, id := range req.IDs {
		if err := s.st.Trash(r.Context(), u, id); err != nil {
			failed = append(failed, map[string]any{"id": id, "error": err.Error()})
			continue
		}
		deleted++
	}
	s.activity(r, u.ID, "file.trashed", "", fmt.Sprintf("%d item(s)", deleted), false)
	writeJSON(w, 200, map[string]any{"deleted": deleted, "failed": failed})
}

func (s *Server) handleDeleteNode(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	p, _ := s.st.NodePath(r.Context(), u.ID, id)
	if err := s.st.Trash(r.Context(), u, id); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "file.trashed", p, "", false)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleSetTags(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req struct {
		Tags []string `json:"tags"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	tags, err := cleanTags(req.Tags)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.SetTags(r.Context(), u.ID, id, tags); err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.NodeWithTags(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"node": n})
}

func cleanTags(in []string) ([]string, error) {
	if len(in) > 50 {
		return nil, errf(400, "too_many_tags", "At most 50 tags per file")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, t := range in {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if len(t) > 64 {
			return nil, errf(400, "bad_tag", "Tags must be at most 64 characters")
		}
		k := strings.ToLower(t)
		if !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out, nil
}

func (s *Server) handleTagList(w http.ResponseWriter, r *http.Request) {
	tags, err := s.st.UserTags(r.Context(), principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"tags": tags})
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	q := r.URL.Query()
	num := func(k string) int64 { v, _ := strconv.ParseInt(q.Get(k), 10, 64); return v }
	res, err := s.st.Search(r.Context(), u.ID, store.SearchOptions{
		Query: q.Get("q"), Kind: q.Get("kind"), Tag: q.Get("tag"), MinSize: num("min_size"), MaxSize: num("max_size"),
		ModifiedAfter: num("modified_after"), ModifiedBefore: num("modified_before"), Sort: q.Get("sort"), Desc: q.Get("order") == "desc",
		Limit: int(num("limit")),
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": res})
}

// inlineSafe lists MIME types that may be rendered by the browser. Anything
// else (HTML, SVG, XML, scripts) is forced to download so stored files can
// never run script in the VaultDock origin.
func inlineSafe(m string) bool {
	switch {
	case strings.HasPrefix(m, "video/"), strings.HasPrefix(m, "audio/"):
		return true
	case strings.HasPrefix(m, "image/") && m != "image/svg+xml":
		return true
	case m == "application/pdf", m == "text/plain", m == "text/markdown", m == "text/csv", m == "application/json", m == "text/yaml", m == "text/vtt":
		return true
	}
	return false
}

func contentDisposition(kind, name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e || r == '"' || r == '\\' {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`%s; filename="%s"; filename*=UTF-8''%s`, kind, ascii, url.PathEscape(name))
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.Node(r.Context(), u.ID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n.IsDir {
		writeErr(w, r, errf(400, "is_folder", "Folders cannot be downloaded directly; use the zip download"))
		return
	}
	loc, err := s.st.BlobLocation(r.Context(), u.ID, n.SHA256)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	f, err := s.blobs.Open(loc, u.ID, n.SHA256)
	if err != nil {
		slog.Error("blob missing", "node", n.ID, "sha", n.SHA256, "err", err)
		writeErr(w, r, errf(503, "content_unavailable", "File content is unavailable on the storage device"))
		return
	}
	defer f.Close()
	h := w.Header()
	download := r.URL.Query().Get("download") == "1"
	ctype := n.Mime
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	if download || !inlineSafe(ctype) {
		h.Set("Content-Disposition", contentDisposition("attachment", n.Name))
		if !inlineSafe(ctype) {
			ctype = "application/octet-stream"
		}
	} else {
		h.Set("Content-Disposition", contentDisposition("inline", n.Name))
		if strings.HasPrefix(ctype, "text/") || ctype == "application/json" {
			ctype = "text/plain; charset=utf-8"
		}
	}
	h.Set("Content-Type", ctype)
	h.Set("Content-Security-Policy", "default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'; sandbox")
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
	h.Set("ETag", `"`+n.SHA256+`"`)
	h.Set("X-Frame-Options", "SAMEORIGIN")
	if r.URL.Query().Get("download") == "1" {
		s.activity(r, u.ID, "file.downloaded", n.Name, "", false)
	}
	// ServeContent handles Range, If-Range and conditional requests without
	// reading the whole file into memory.
	http.ServeContent(w, r, n.Name, time.UnixMilli(n.ModifiedAt), f)
}

const maxZipEntries = 100000

type zipEntry struct {
	rel  string
	node store.Node
}

func (s *Server) handleZip(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	var ids []int64
	for _, part := range strings.Split(r.URL.Query().Get("ids"), ",") {
		if id, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil && id > 0 {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 1000 {
		writeErr(w, r, errf(400, "bad_ids", "Select between 1 and 1000 items"))
		return
	}
	var entries []zipEntry
	archiveName := "vaultdock-download.zip"
	for _, id := range ids {
		n, err := s.st.Node(r.Context(), u.ID, id)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if len(ids) == 1 && n.IsDir && n.ParentID != nil {
			archiveName = n.Name + ".zip"
		}
		if !n.IsDir {
			entries = append(entries, zipEntry{rel: n.Name, node: *n})
			continue
		}
		desc, err := s.st.Descendants(r.Context(), u.ID, n.ID, maxZipEntries+1)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		prefix := n.Name + "/"
		if n.ParentID == nil {
			prefix = ""
		}
		for _, d := range desc {
			rel := prefix + d.Path
			if d.IsDir {
				rel += "/"
			}
			entries = append(entries, zipEntry{rel: rel, node: d})
		}
		if len(entries) > maxZipEntries {
			writeErr(w, r, errf(400, "too_many", "Too many files for one archive"))
			return
		}
	}
	// Check every blob before the first byte is written so the response can
	// still carry X-VD-Missing-Count. A blob that disappears between this
	// check and streaming is still reported in MISSING_FILES.txt, but the
	// header (already sent) cannot include it.
	var missing []zipMissing
	skip := map[int]bool{}
	locs := make([]int64, len(entries))
	for i, e := range entries {
		if e.node.IsDir {
			continue
		}
		loc, reason := s.zipBlobLocation(r, u.ID, e.node.SHA256)
		if reason != "" {
			missing = append(missing, zipMissing{path: e.rel, reason: reason})
			skip[i] = true
			continue
		}
		locs[i] = loc
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", contentDisposition("attachment", archiveName))
	w.Header().Set("X-VD-Missing-Count", strconv.Itoa(len(missing)))
	zw := zip.NewWriter(w)
	buf := make([]byte, 1<<20)
	for i, e := range entries {
		hdr := &zip.FileHeader{Name: e.rel, Method: zip.Store, Modified: time.UnixMilli(e.node.ModifiedAt)}
		if e.node.IsDir {
			if _, err := zw.CreateHeader(hdr); err != nil {
				return
			}
			continue
		}
		if skip[i] {
			continue
		}
		if !strings.HasPrefix(e.node.Mime, "video/") && !strings.HasPrefix(e.node.Mime, "image/") && !strings.HasPrefix(e.node.Mime, "audio/") && e.node.Mime != "application/zip" {
			hdr.Method = zip.Deflate
		}
		f, err := s.blobs.Open(locs[i], u.ID, e.node.SHA256)
		if err != nil {
			missing = append(missing, zipMissing{path: e.rel, reason: zipMissingReason(err)})
			continue
		}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			f.Close()
			return
		}
		_, err = io.CopyBuffer(fw, f, buf)
		f.Close()
		if err != nil {
			return
		}
	}
	if len(missing) > 0 {
		slog.Warn("zip: blobs missing from download", "user", u.ID, "missing", len(missing), "entries", len(entries))
		if fw, err := zw.CreateHeader(&zip.FileHeader{Name: zipManifestName(entries), Method: zip.Deflate, Modified: time.Now()}); err == nil {
			_, _ = io.WriteString(fw, zipManifest(missing))
		}
	}
	_ = zw.Close()
	s.activity(r, u.ID, "file.downloaded", archiveName, fmt.Sprintf("%d entries", len(entries)), false)
}

type zipMissing struct {
	path   string
	reason string
}

// zipBlobLocation resolves the storage location of a blob and checks that the
// file exists. It returns a human-readable reason when the blob cannot be
// included in an archive.
func (s *Server) zipBlobLocation(r *http.Request, userID int64, sha string) (int64, string) {
	loc, err := s.st.BlobLocation(r.Context(), userID, sha)
	if err != nil {
		return 0, "no blob record in the database"
	}
	p, err := s.blobs.BlobPath(loc, userID, sha)
	if err != nil {
		return 0, "storage location unavailable"
	}
	if _, err := os.Stat(p); err != nil {
		return 0, zipMissingReason(err)
	}
	return loc, ""
}

func zipMissingReason(err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return "blob file not found on storage"
	}
	return "blob file unreadable"
}

// zipManifestName picks a root-level name for the missing-files report that
// does not collide with a real archive entry.
func zipManifestName(entries []zipEntry) string {
	used := make(map[string]bool, len(entries))
	for _, e := range entries {
		used[e.rel] = true
	}
	name := "MISSING_FILES.txt"
	for i := 2; used[name]; i++ {
		name = fmt.Sprintf("MISSING_FILES-%d.txt", i)
	}
	return name
}

func zipManifest(missing []zipMissing) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VaultDock could not include %d file(s) in this archive because their stored data is missing.\n", len(missing))
	b.WriteString("Ask your administrator to check the storage location. Each line is: path<TAB>reason\n\n")
	for _, m := range missing {
		b.WriteString(m.path)
		b.WriteByte('\t')
		b.WriteString(m.reason)
		b.WriteByte('\n')
	}
	return b.String()
}

// --- trash ---

func (s *Server) handleTrashList(w http.ResponseWriter, r *http.Request) {
	items, err := s.st.ListTrash(r.Context(), principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items, "retention_days": s.settings.Get().TrashRetentionDays})
}

func (s *Server) handleTrashRestore(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, err := s.st.Restore(r.Context(), u, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "file.restored", n.Path, "", false)
	writeJSON(w, 200, map[string]any{"node": n})
}

func (s *Server) handleTrashPurge(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	n, freed, err := s.st.PurgeTrash(r.Context(), u.ID, []int64{id}, 0)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if n == 0 {
		writeErr(w, r, store.ErrNotFound)
		return
	}
	s.blobs.Remove(r.Context(), freed)
	s.activity(r, u.ID, "file.purged", "", "1 item permanently deleted", false)
	writeJSON(w, 200, map[string]any{"purged": n})
}

func (s *Server) handleTrashEmpty(w http.ResponseWriter, r *http.Request) {
	u := principal(r).User
	n, freed, err := s.st.PurgeTrash(r.Context(), u.ID, nil, 0)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.blobs.Remove(r.Context(), freed)
	s.activity(r, u.ID, "trash.emptied", "", fmt.Sprintf("%d item(s) permanently deleted", n), false)
	writeJSON(w, 200, map[string]any{"purged": n})
}
