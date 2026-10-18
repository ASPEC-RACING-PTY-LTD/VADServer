// Owner: SRV-2 (Phase 2, P2-07). Scope: api-v1 §5.4 snapshot content and
// zip download (full escrow only) plus the rev 1.1c download token route.
//
// The server opens the repository with its escrow key (SEK) through a
// read-only backend. Every open is audited (purpose data_operation) before
// any key is released, and the keys live only for the request or the
// download operation.

package api

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/restore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Download limits (api-v1 rev 1.1c).
const (
	maxDownloadPaths = 100
	maxDownloadFiles = 100000
	maxDownloadBytes = 50 << 30
	downloadTTL      = time.Hour
)

// mountSnapshotDownload registers the routes of this feature area. The
// unauthenticated GET /downloads/{token} is registered in Handler.
func (s *Server) mountSnapshotDownload(rt routes) {
	rt.Sess.Get("/snapshots/{id}/content", s.handleSnapshotContent) // Range
	rt.Sess.Head("/snapshots/{id}/content", s.handleSnapshotContent)
	rt.Sess.Post("/snapshots/{id}/download", s.handleSnapshotDownload) // 202 operation; result download_url
}

// errNeedsFullEscrow is the reply for a repository whose escrow does not
// cover file contents (escrow_mode metadata).
func errNeedsFullEscrow() *apiError {
	return errf(409, wire.CodeEscrowUnavailable, "The server cannot read file contents of this repository (metadata-only escrow); restore from a device instead")
}

// snapshotRepo loads a snapshot of the session user and its repository and
// checks that the server may read file contents.
func (s *Server) snapshotRepo(r *http.Request) (*store.Snapshot, *store.Repository, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, nil, err
	}
	sn, err := s.restoreSnapshot(r.Context(), principal(r).User.ID, id)
	if err != nil {
		return nil, nil, err
	}
	rp, err := s.st.Repository(r.Context(), sn.RepoID)
	if err != nil {
		return nil, nil, err
	}
	if rp.EscrowMode != "full" {
		return nil, nil, errNeedsFullEscrow()
	}
	return sn, rp, nil
}

// openFullRepo opens rp with the server escrow key for a data operation.
// The caller must Close the repository.
func (s *Server) openFullRepo(ctx context.Context, rp *store.Repository, actor int64, opID *int64, details string) (*repo.Repo, error) {
	m := s.Keys()
	if m == nil {
		return nil, errEscrowUnavailable()
	}
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		return nil, err
	}
	be := backup.NewReadBackend(bs, rp.RepoUUID, func(ctx context.Context, kind string) ([]repo.ObjectInfo, error) {
		var out []repo.ObjectInfo
		var after int64
		for {
			page, err := s.st.ListRepoObjects(ctx, rp.ID, kind, after, wire.ListMaxLimit)
			if err != nil {
				return nil, err
			}
			for _, o := range page {
				out = append(out, repo.ObjectInfo{OID: o.OID, Size: o.Size})
			}
			if len(page) < wire.ListMaxLimit {
				return out, nil
			}
			after = page[len(page)-1].ID
		}
	})
	// The newest escrow key file names the SEK version to release.
	kinfos, err := be.List(ctx, repo.KindKeys)
	if err != nil {
		return nil, err
	}
	var best *vdcrypt.KeyFile
	for _, o := range kinfos {
		b, err := be.Get(ctx, repo.KindKeys, o.OID)
		if err != nil {
			continue
		}
		kf, err := vdcrypt.DecodeKeyFile(b)
		if err != nil || kf.Kind != vdcrypt.KindEscrow || kf.Repo != rp.RepoUUID || kf.Server == nil {
			continue
		}
		if best == nil || kf.Epoch > best.Epoch {
			best = kf
		}
	}
	if best == nil {
		return nil, errNeedsFullEscrow()
	}
	uid, rid := rp.UserID, rp.ID
	sek, err := m.OpenSEK(ctx, best.Server.SEKVersion, keys.Audit{Purpose: keys.PurposeDataOperation, ActorUserID: &actor,
		UserID: &uid, RepoID: &rid, OperationID: opID, Details: details})
	if errors.Is(err, keys.ErrUnavailable) || errors.Is(err, store.ErrNotFound) {
		return nil, errEscrowUnavailable()
	}
	if err != nil {
		return nil, err
	}
	defer sek.Wipe()
	rr, err := repo.Open(ctx, be, repo.EscrowCredential(sek), repo.Options{Logger: slog.Default()})
	if err != nil {
		if errors.Is(err, repo.ErrNoKeyFile) {
			return nil, errNeedsFullEscrow()
		}
		return nil, err
	}
	if rr.MetadataOnly() {
		rr.Close()
		return nil, errNeedsFullEscrow()
	}
	return rr, nil
}

func (s *Server) handleSnapshotContent(w http.ResponseWriter, r *http.Request) {
	sn, rp, err := s.snapshotRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	path := r.URL.Query().Get("path")
	if _, _, perr := snapshot.SplitTokenPath(path); perr != nil {
		writeErr(w, r, validation("path", "must be a snapshot token path"))
		return
	}
	ctx := r.Context()
	uid := principal(r).User.ID
	rr, err := s.openFullRepo(ctx, rp, uid, nil, fmt.Sprintf("content of snapshot %d", sn.ID))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defer rr.Close()
	root, _, err := rr.LoadSnapshot(ctx, sn.SnapshotOID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	it, err := restore.Lookup(ctx, rr, root.Roots, path)
	if errors.Is(err, restore.ErrNotFound) || (err == nil && it.Entry.T != snapshot.TypeFile) {
		writeErr(w, r, errf(404, "not_found", "No such file in the snapshot"))
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	k, err := rr.Keys()
	if err != nil {
		writeErr(w, r, errEscrowUnavailable())
		return
	}
	fr, err := restore.NewFileReader(ctx, rr, &k.FileHash, it.Entry)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Content-Disposition", contentDisposition("attachment", it.Entry.N))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; sandbox")
	h.Set("Cache-Control", "private, no-store")
	if it.Entry.H != nil {
		h.Set("ETag", `"`+*it.Entry.H+`"`)
	}
	if r.Method == http.MethodGet && r.Header.Get("Range") == "" {
		s.activity(r, uid, "snapshot.file_downloaded", "snapshot", fmt.Sprintf("snapshot_id=%d", sn.ID), false)
	}
	http.ServeContent(w, r, it.Entry.N, time.Unix(0, it.Entry.M), fr)
}

// --- zip downloads ---

type pendingDownload struct {
	file    string
	name    string
	userID  int64
	expires time.Time
}

// downloadHub holds finished zips by token hash. Tokens are single-use and
// live in memory only: a server restart invalidates them (the zip files are
// removed by the sweep).
type downloadHub struct {
	mu    sync.Mutex
	items map[string]*pendingDownload
	dir   string
}

var downloadHubs sync.Map // *Server -> *downloadHub

func (s *Server) downloads() *downloadHub {
	if v, ok := downloadHubs.Load(s); ok {
		return v.(*downloadHub)
	}
	dir := filepath.Join(s.cfg.StateDir, "tmp", "downloads")
	v, _ := downloadHubs.LoadOrStore(s, &downloadHub{items: map[string]*pendingDownload{}, dir: dir})
	return v.(*downloadHub)
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// add registers a finished zip and returns its token.
func (d *downloadHub) add(p *pendingDownload) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	d.mu.Lock()
	d.sweepLocked()
	d.items[tokenKey(token)] = p
	d.mu.Unlock()
	return token, nil
}

// take removes and returns the download of token (single use).
func (d *downloadHub) take(token string) *pendingDownload {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sweepLocked()
	k := tokenKey(token)
	p := d.items[k]
	delete(d.items, k)
	return p
}

// sweepLocked deletes expired downloads and stray zip files.
func (d *downloadHub) sweepLocked() {
	now := time.Now()
	live := map[string]bool{}
	for k, p := range d.items {
		if now.After(p.expires) {
			_ = os.Remove(p.file)
			delete(d.items, k)
			continue
		}
		live[p.file] = true
	}
	ents, _ := os.ReadDir(d.dir)
	for _, e := range ents {
		f := filepath.Join(d.dir, e.Name())
		if live[f] {
			continue
		}
		if info, err := e.Info(); err == nil && now.Sub(info.ModTime()) > 2*downloadTTL {
			_ = os.Remove(f)
		}
	}
}

// zipName turns a token path into a zip entry name: "{DOCUMENTS}/a/b.txt"
// becomes "Documents/a/b.txt".
func zipName(p string) string {
	tok, comps, err := snapshot.SplitTokenPath(p)
	if err != nil {
		return ""
	}
	t := strings.Trim(tok, "{}")
	if strings.HasPrefix(t, "VOL:") {
		t = "Volume-" + t[4:12]
	} else if len(t) > 1 {
		t = t[:1] + strings.ToLower(t[1:])
	}
	return strings.Join(append([]string{t}, comps...), "/")
}

func (s *Server) handleSnapshotDownload(w http.ResponseWriter, r *http.Request) {
	sn, rp, err := s.snapshotRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.SnapshotDownloadRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > maxDownloadPaths {
		writeErr(w, r, validation("paths", "choose 1 to %d paths", maxDownloadPaths))
		return
	}
	sel := make([]wire.RestoreSelection, 0, len(req.Paths))
	for _, p := range req.Paths {
		sel = append(sel, wire.RestoreSelection{Path: p, Include: true})
	}
	selector, err := restore.NewSelector(sel)
	if err != nil {
		writeErr(w, r, validation("paths", "%s", err.Error()))
		return
	}
	uid := principal(r).User.ID
	hub := s.downloads()
	var opID int64
	opID, err = s.StartOperation(OpStart{Kind: wire.OperationKindSnapshotDownload, UserID: &uid, Params: map[string]any{"snapshot_id": sn.ID, "paths": len(req.Paths)}},
		func(ctx context.Context, p *OpProgress) (any, error) {
			return s.buildZip(ctx, hub, rp, sn, selector, uid, &opID, p)
		})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, uid, "snapshot.download", "snapshot", fmt.Sprintf("snapshot_id=%d paths=%d", sn.ID, len(req.Paths)), false)
	writeJSON(w, http.StatusAccepted, wire.OperationAccepted{OperationID: opID})
}

// buildZip writes the selected files into a temporary zip and registers it.
func (s *Server) buildZip(ctx context.Context, hub *downloadHub, rp *store.Repository, sn *store.Snapshot, sel *restore.Selector, uid int64, opID *int64, p *OpProgress) (any, error) {
	rr, err := s.openFullRepo(ctx, rp, uid, opID, fmt.Sprintf("zip download of snapshot %d", sn.ID))
	if err != nil {
		return nil, err
	}
	defer rr.Close()
	root, _, err := rr.LoadSnapshot(ctx, sn.SnapshotOID)
	if err != nil {
		return nil, err
	}
	sum, err := restore.Summarize(ctx, rr, root.Roots, sel)
	if err != nil {
		return nil, err
	}
	if sum.Files == 0 {
		return nil, errf(422, "validation", "The selection contains no files")
	}
	if sum.Files > maxDownloadFiles || sum.Bytes > maxDownloadBytes {
		return nil, errf(413, "too_large", "At most %d files and %d GiB per download; choose fewer files", maxDownloadFiles, maxDownloadBytes>>30)
	}
	k, err := rr.Keys()
	if err != nil {
		return nil, errEscrowUnavailable()
	}
	if err := os.MkdirAll(hub.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.CreateTemp(hub.dir, "snapshot-*.zip")
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			f.Close()
			_ = os.Remove(f.Name())
		}
	}()
	zw := zip.NewWriter(f)
	var done, bytes int64
	err = restore.Walk(ctx, rr, root.Roots, sel, func(it restore.Item) error {
		name := zipName(it.Path)
		if name == "" {
			return nil
		}
		hdr := &zip.FileHeader{Name: name, Modified: time.Unix(0, it.Entry.M), Method: zip.Deflate}
		switch it.Entry.T {
		case snapshot.TypeDir:
			hdr.Name += "/"
			_, err := zw.CreateHeader(hdr)
			return err
		case snapshot.TypeFile:
		default:
			return nil // links and reparse points are not exported
		}
		hdr.UncompressedSize64 = uint64(it.Entry.S)
		zf, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		n, err := restore.CopyFile(ctx, rr, &k.FileHash, it.Entry, zf)
		if err != nil {
			return fmt.Errorf("%s: %w", it.Path, err)
		}
		done++
		bytes += n
		p.Set(done, sum.Files, bytes)
		return nil
	})
	if err == nil {
		err = zw.Close()
	}
	if err == nil {
		err = f.Close()
	}
	if err != nil {
		return nil, err
	}
	exp := time.Now().Add(downloadTTL)
	name := fmt.Sprintf("snapshot-%d.zip", sn.ID)
	token, err := hub.add(&pendingDownload{file: f.Name(), name: name, userID: uid, expires: exp})
	if err != nil {
		return nil, err
	}
	keep = true
	return wire.SnapshotDownloadResult{DownloadURL: "/api/v1/downloads/" + token, ExpiresAt: exp.UnixMilli(), Files: done, Bytes: bytes}, nil
}

// handleDownloadToken serves a finished zip once (no session: the token is
// the credential).
func (s *Server) handleDownloadToken(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(chi.URLParam(r, "token"))
	var d *pendingDownload
	if len(token) >= 40 && len(token) <= 64 {
		d = s.downloads().take(token)
	}
	if d == nil {
		writeErr(w, r, errf(404, "not_found", "This download link is invalid, already used or expired"))
		return
	}
	defer os.Remove(d.file)
	f, err := os.Open(d.file)
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "This download is no longer available"))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/zip")
	h.Set("Content-Disposition", contentDisposition("attachment", d.name))
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "private, no-store")
	h.Set("Content-Length", fmt.Sprint(info.Size()))
	_, _ = io.Copy(w, f)
}
