// Owner: SRV-2 (P1-14). Scope: api-v1 §2 capabilities, §5 repositories and
// objects (feature backup.v1), with the rev 1.1b resolutions (§14):
//
//   - POST /repos with destination_id omitted or 0 uses the built-in server
//     destination (localfs under <default storage>/backup), created on
//     demand.
//   - config (fixed oid 0×64) is replaced atomically when its content
//     differs (201) and left alone when identical (200). The caller must
//     hold an unexpired exclusive lock (repo_locks, read directly; the lock
//     routes belong to P1-15), else 409 repo_locked.
//   - Every other kind: a body whose SHA-256 differs from the oid is 422
//     object_hash_mismatch; a body that differs from the signed
//     X-VD-Content-SHA256 stays 400 body_mismatch.
//   - There is no object DELETE route.
//   - keys objects (INT-FIX-E) are buffered (at most maxKeyFileBytes, else
//     413) and must decode as a key file of this repository (repo =
//     repo_uuid), else 422 validation naming the field.
//
// Object rows are written only after the object is stored and verified. The
// backup.v1 flag is advertised by P1-15 once snapshots and locks exist.

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
	"github.com/go-chi/chi/v5"
)

// mountBackupObjects registers the routes of this feature area.
func (s *Server) mountBackupObjects(rt routes) {
	rt.User.Get("/backup/capabilities", s.handleBackupCapabilities)
	rt.Dev.Post("/repos", s.handleCreateRepo) // idempotent by repo_uuid
	rt.User.Get("/repos", s.handleListRepos)
	rt.User.Get("/repos/{id}", s.handleGetRepo)
	rt.Dev.Get("/repos/{id}/objects", s.handleListObjects) // ?kind=&cursor=
	rt.Dev.Post("/repos/{id}/objects/missing", s.handleMissingObjects)
	rt.Dev.Head("/repos/{id}/objects/{kind}/{oid}", s.handleHeadObject)
	rt.Dev.Put("/repos/{id}/objects/{kind}/{oid}", s.handlePutObject)  // streamed (api-v1 §1.2)
	rt.User.Get("/repos/{id}/objects/{kind}/{oid}", s.handleGetObject) // Range
}

// Capability values (api-v1 §2).
const (
	maxObjectBytes  = 64 << 20
	packTargetBytes = 16 << 20
	maxCommitBytes  = 64 << 20
	longPollSeconds = 25
	maxRepoName     = 200
)

func (s *Server) handleBackupCapabilities(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, wire.Capabilities{
		Formats:           []string{wire.CapabilitiesFormatRepoV1},
		MaxObjectBytes:    maxObjectBytes,
		PackTargetBytes:   packTargetBytes,
		MaxMissingBatch:   wire.ObjectMissingMaxOIDs,
		MaxCommitBytes:    maxCommitBytes,
		EscrowModes:       []string{wire.EscrowModeFull, wire.EscrowModeMetadata},
		DefaultEscrowMode: wire.EscrowModeFull,
		LongPollSeconds:   longPollSeconds,
		ModulesSupported: []string{wire.ModuleCore, wire.ModuleSync, wire.ModuleAutomation, wire.ModuleVSS,
			wire.ModuleDiscovery, wire.ModuleReinstall, wire.ModuleProfiles},
	})
}

// --- repositories ---

func repoValidation(fields map[string]string) *apiError {
	return &apiError{Status: 422, Code: "validation", Message: "Invalid request", Details: map[string]any{"fields": fields}}
}

func errRepoNotFound() *apiError {
	return errf(404, wire.CodeRepoNotFound, "Repository not found")
}

func (s *Server) repoView(ctx context.Context, rp *store.Repository) (wire.Repo, error) {
	u, err := s.st.RepositoryUsage(ctx, rp.ID)
	if err != nil {
		return wire.Repo{}, err
	}
	return wire.Repo{
		ID: rp.ID, RepoUUID: rp.RepoUUID, Name: rp.Name, DestinationID: rp.DestinationID,
		FormatVersion: rp.FormatVersion, EscrowMode: rp.EscrowMode, CurrentEpoch: rp.CurrentEpoch,
		CreatedByDeviceID: rp.CreatedByDeviceID, CreatedAt: rp.CreatedAt, LastCatalogSyncAt: rp.LastCatalogSyncAt,
		Usage: wire.RepoUsage{Objects: u.Objects, StoredBytes: u.StoredBytes, LogicalBytes: u.LogicalBytes, Snapshots: u.Snapshots},
	}, nil
}

// serverBackupRoot is the localfs root for a new built-in server
// destination: <default storage location>/backup.
func (s *Server) serverBackupRoot(ctx context.Context) (string, error) {
	loc, err := s.st.DefaultLocation(ctx)
	if err != nil {
		return "", fmt.Errorf("default storage location: %w", err)
	}
	return filepath.Join(loc.Path, backup.DirName), nil
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var req wire.RepoCreateRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	fields := map[string]string{}
	if !backup.ValidRepoUUID(req.RepoUUID) {
		fields["repo_uuid"] = "must be a lowercase canonical UUIDv4"
	}
	if req.Name == "" || utf8.RuneCountInString(req.Name) > maxRepoName || !utf8.ValidString(req.Name) {
		fields["name"] = fmt.Sprintf("required, at most %d characters", maxRepoName)
	}
	if req.EscrowMode == "" {
		req.EscrowMode = wire.EscrowModeFull
	}
	if req.EscrowMode != wire.EscrowModeFull && req.EscrowMode != wire.EscrowModeMetadata {
		fields["escrow_mode"] = "must be full or metadata"
	}
	if req.FormatVersion != wire.RepoFormatVersion {
		fields["format_version"] = "must be 1"
	}
	if req.DestinationID < 0 {
		fields["destination_id"] = "invalid"
	}
	if len(fields) > 0 {
		writeErr(w, r, repoValidation(fields))
		return
	}

	ctx := r.Context()
	destID := req.DestinationID
	if destID == 0 {
		root, err := s.serverBackupRoot(ctx)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if destID, _, err = s.st.ServerDestination(ctx, root); err != nil {
			writeErr(w, r, err)
			return
		}
	} else {
		kind, _, err := s.st.DestinationTarget(ctx, destID, p.User.ID)
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, r, repoValidation(map[string]string{"destination_id": "unknown destination"}))
			return
		}
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if kind != "server" {
			writeErr(w, r, errf(501, "not_implemented", "Only the built-in server destination is supported in this version"))
			return
		}
	}

	rp := &store.Repository{UserID: p.User.ID, DestinationID: destID, RepoUUID: req.RepoUUID, Name: req.Name,
		FormatVersion: req.FormatVersion, EscrowMode: req.EscrowMode, CreatedByDeviceID: p.Device.ID}
	created, err := s.st.CreateRepository(ctx, rp)
	switch {
	case errors.Is(err, store.ErrConflict):
		writeErr(w, r, errf(409, "conflict", "That repo_uuid is already in use"))
		return
	case errors.Is(err, store.ErrRepoExists):
		writeErr(w, r, &apiError{Status: 409, Code: "repo_exists", Message: "You already have a repository on this destination",
			Details: map[string]any{"repo_id": rp.ID}})
		return
	case err != nil:
		writeErr(w, r, err)
		return
	}
	view, err := s.repoView(ctx, rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
		s.activity(r, p.User.ID, "repo.create", rp.Name, rp.RepoUUID, false)
	}
	writeJSON(w, status, view)
}

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	after, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListRepositories(r.Context(), p.User.ID, after, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Repo]{Items: make([]wire.Repo, 0, len(list))}
	for i := range list {
		v, err := s.repoView(r.Context(), &list[i])
		if err != nil {
			writeErr(w, r, err)
			return
		}
		out.Items = append(out.Items, v)
	}
	if len(list) == limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

// loadRepo returns the caller's repository {id}; other users' repositories
// are 404 repo_not_found.
func (s *Server) loadRepo(r *http.Request) (*store.Repository, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	rp, err := s.st.Repository(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && rp.UserID != principal(r).User.ID) {
		return nil, errRepoNotFound()
	}
	return rp, err
}

func (s *Server) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	v, err := s.repoView(r.Context(), rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, v)
}

// repoObjects opens the object store behind a repository's destination.
func (s *Server) repoObjects(ctx context.Context, rp *store.Repository) (*backup.Store, error) {
	kind, root, err := s.st.DestinationTarget(ctx, rp.DestinationID, rp.UserID)
	if err != nil {
		return nil, err
	}
	if kind != "server" {
		return nil, errf(501, "not_implemented", "This repository's destination is not supported in this version")
	}
	bs, err := backup.OpenLocal(root)
	if err != nil {
		slog.Error("backup store unavailable", "repo", rp.ID, "err", err)
		return nil, errf(503, "storage_unavailable", "Backup storage is unavailable")
	}
	return bs, nil
}

// --- objects ---

func errInvalidOID() *apiError {
	return errf(400, wire.CodeInvalidOID, "Invalid object kind or oid")
}

func errObjectNotFound() *apiError {
	return errf(404, wire.CodeObjectNotFound, "Object not found")
}

// objectParams parses {kind} and {oid}.
func objectParams(r *http.Request) (kind, oid string, err error) {
	kind, oid = chi.URLParam(r, "kind"), chi.URLParam(r, "oid")
	if !backup.ValidKind(kind) || !backup.ValidOID(kind, oid) {
		return "", "", errInvalidOID()
	}
	return kind, oid, nil
}

func (s *Server) handleHeadObject(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	kind, oid, err := objectParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	o, err := s.st.RepoObject(r.Context(), rp.ID, kind, oid)
	if errors.Is(err, store.ErrNotFound) {
		err = errObjectNotFound()
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(o.Size, 10))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
}

func (s *Server) handleGetObject(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	kind, oid, err := objectParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	if _, err := s.st.RepoObject(ctx, rp.ID, kind, oid); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			err = errObjectNotFound()
		}
		writeErr(w, r, err)
		return
	}
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	info, err := bs.Stat(ctx, rp.RepoUUID, kind, oid)
	if errors.Is(err, objstore.ErrNotFound) {
		slog.Error("repository object row without stored object", "repo", rp.ID, "kind", kind, "oid", oid)
		err = errObjectNotFound()
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	size := info.Size
	h := w.Header()
	h.Set("Content-Type", "application/octet-stream")
	h.Set("Accept-Ranges", "bytes")
	h.Set("Cache-Control", "no-store")

	off, length, partial, ok := parseByteRange(r.Header.Get("Range"), size)
	if !ok {
		h.Set("Content-Range", "bytes */"+strconv.FormatInt(size, 10))
		writeErr(w, r, errf(416, "range_not_satisfiable", "Requested range not satisfiable"))
		return
	}
	rc, err := bs.GetRange(ctx, rp.RepoUUID, kind, oid, off, length)
	if errors.Is(err, objstore.ErrNotFound) {
		err = errObjectNotFound()
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	defer rc.Close()
	h.Set("Content-Length", strconv.FormatInt(length, 10))
	status := 200
	if partial {
		h.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", off, off+length-1, size))
		status = 206
	}
	w.WriteHeader(status)
	if _, err := io.Copy(w, rc); err != nil && ctx.Err() == nil {
		slog.Warn("object download interrupted", "repo", rp.ID, "kind", kind, "err", err)
	}
}

// parseByteRange interprets a single-range "bytes=" header against size. An
// absent, malformed or multi-range header selects the whole object
// (partial false; RFC 9110 lets servers ignore Range). ok is false when a
// well-formed range is unsatisfiable (416).
func parseByteRange(hdr string, size int64) (off, length int64, partial, ok bool) {
	spec, found := strings.CutPrefix(strings.TrimSpace(hdr), "bytes=")
	if !found || strings.Contains(spec, ",") {
		return 0, size, false, true
	}
	first, last, found := strings.Cut(strings.TrimSpace(spec), "-")
	if !found {
		return 0, size, false, true
	}
	if first == "" {
		n, err := strconv.ParseInt(last, 10, 64)
		if err != nil || n < 0 {
			return 0, size, false, true
		}
		if n == 0 || size == 0 {
			return 0, 0, false, false
		}
		n = min(n, size)
		return size - n, n, true, true
	}
	start, err := strconv.ParseInt(first, 10, 64)
	if err != nil || start < 0 {
		return 0, size, false, true
	}
	end := size - 1
	if last != "" {
		e, err := strconv.ParseInt(last, 10, 64)
		if err != nil || e < start {
			return 0, size, false, true
		}
		end = min(e, size-1)
	}
	if start >= size {
		return 0, 0, false, false
	}
	return start, end - start + 1, true, true
}

func (s *Server) handlePutObject(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	kind, oid, err := objectParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	size := r.ContentLength
	if size < 0 {
		writeErr(w, r, errf(411, "length_required", "Content-Length is required"))
		return
	}
	if size > maxObjectBytes {
		writeErr(w, r, errf(413, "too_large", "Objects are limited to %d bytes", maxObjectBytes))
		return
	}
	if kind == wire.ObjectKindKeys && size > maxKeyFileBytes {
		writeErr(w, r, errf(413, "too_large", "Key files are limited to %d bytes", maxKeyFileBytes))
		return
	}
	body, err := newStreamedBody(r, size)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()
	bs, err := s.repoObjects(ctx, rp)
	if err != nil {
		writeErr(w, r, err)
		return
	}

	// Server capacity: as for upload chunks, wait briefly for a write slot,
	// then tell the client to back off.
	select {
	case s.chunkSem <- struct{}{}:
		defer func() { <-s.chunkSem }()
	case <-time.After(3 * time.Second):
		w.Header().Set("Retry-After", "2")
		w.Header().Set("X-VD-Capacity", "saturated")
		writeErr(w, r, errf(503, "server_busy", "Server is busy, retry shortly"))
		return
	case <-ctx.Done():
		return
	}

	var created bool
	if kind == wire.ObjectKindConfig {
		created, err = s.putConfig(ctx, p, rp, bs, body, size)
	} else {
		if kind == wire.ObjectKindKeys {
			body, err = checkKeyFile(p, rp, oid, body, size)
		}
		if err == nil {
			created, err = s.putObject(ctx, p, rp, bs, kind, oid, body, size)
		}
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
	}
	writeJSON(w, status, wire.ObjectInfo{OID: oid, Size: size})
}

// verifyThen422 decides a hash failure against the oid: a body that does
// not match its signed hash is a transport error (400 body_mismatch);
// otherwise the body itself does not hash to the oid (422).
func verifyThen422(body *streamedBody) error {
	if err := body.Verify(); err != nil {
		return err
	}
	return errf(422, wire.CodeObjectHashMismatch, "Object content does not hash to its oid")
}

// checkKeyFile buffers a keys object (the caller has capped size at
// maxKeyFileBytes), verifies it against the signed hash and its oid, and
// decodes the plaintext key file header without opening the wrap. A body
// that is not a key file, or a key file of another repository, is 422
// validation. It returns a fresh body over the buffered bytes for putObject.
func checkKeyFile(p *Principal, rp *store.Repository, oid string, body *streamedBody, size int64) (*streamedBody, error) {
	b, err := io.ReadAll(body)
	if err != nil || int64(len(b)) != size {
		var ae *apiError
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, errf(400, "body_mismatch", "Request body does not match its Content-Length")
	}
	if err := body.Verify(); err != nil {
		return nil, err
	}
	if p.DeclaredBodySHA != oid {
		return nil, verifyThen422(body)
	}
	kf, err := vdcrypt.DecodeKeyFile(b)
	if err != nil {
		return nil, repoValidation(map[string]string{"body": "not a valid key file"})
	}
	got, gerr := vdcrypt.ParseRepoID(kf.Repo)
	want, werr := vdcrypt.ParseRepoID(rp.RepoUUID)
	if gerr != nil || werr != nil || got != want {
		return nil, repoValidation(map[string]string{"repo": "key file belongs to another repository"})
	}
	h := sha256.New()
	return &streamedBody{r: io.TeeReader(bytes.NewReader(b), h), h: h, want: body.want}, nil
}

func mapPutErr(err error, body *streamedBody) error {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
		return err
	case errors.Is(err, objstore.ErrHashMismatch):
		if verr := body.Verify(); verr != nil {
			return verr
		}
		return errf(400, "body_mismatch", "Request body does not match the signed hash")
	case errors.Is(err, objstore.ErrSizeMismatch):
		return errf(400, "body_mismatch", "Request body does not match its Content-Length")
	case errors.Is(err, objstore.ErrInvalidKey):
		return errInvalidOID()
	}
	return err
}

func (s *Server) putObject(ctx context.Context, p *Principal, rp *store.Repository, bs *backup.Store, kind, oid string, body *streamedBody, size int64) (bool, error) {
	if p.DeclaredBodySHA != oid {
		return false, verifyThen422(body)
	}
	row, err := s.st.RepoObject(ctx, rp.ID, kind, oid)
	switch {
	case err == nil:
		// Already stored: accept the retry without rewriting when the
		// stored object is intact.
		if info, serr := bs.Stat(ctx, rp.RepoUUID, kind, oid); serr == nil && info.Size == row.Size && row.Size == size {
			if err := body.Verify(); err != nil {
				return false, err
			}
			return false, nil
		}
	case errors.Is(err, store.ErrNotFound):
		if err := s.st.CheckBackupQuota(ctx, rp.UserID, size); err != nil {
			return false, mapQuotaErr(err)
		}
	default:
		return false, err
	}
	if err := bs.Put(ctx, rp.RepoUUID, kind, oid, body, size); err != nil {
		if errors.Is(err, objstore.ErrHashMismatch) {
			return false, verifyThen422(body)
		}
		if errors.Is(err, objstore.ErrExists) {
			slog.Error("stored repository object differs from its oid", "repo", rp.ID, "kind", kind, "oid", oid)
			return false, errf(409, "object_corrupt", "The stored copy of this object is damaged; ask an administrator to run a scrub")
		}
		return false, mapPutErr(err, body)
	}
	if err := body.Verify(); err != nil {
		return false, err
	}
	existed, err := s.st.PutRepoObject(ctx, rp.ID, kind, oid, size)
	return !existed, err
}

func (s *Server) putConfig(ctx context.Context, p *Principal, rp *store.Repository, bs *backup.Store, body *streamedBody, size int64) (bool, error) {
	held, other, err := s.st.HoldsExclusiveRepoLock(ctx, rp.ID, p.Device.ID)
	if err != nil {
		return false, err
	}
	if !held {
		d := wire.LockedDetails{}
		if other != nil {
			d = wire.LockedDetails{Holder: other.Holder, ExpiresAt: other.ExpiresAt}
		}
		return false, &apiError{Status: 409, Code: wire.CodeRepoLocked, Message: "Writing the config needs an exclusive repository lock",
			Details: map[string]any{"holder": d.Holder, "expires_at": d.ExpiresAt}}
	}
	cur, exists, err := bs.ConfigSHA256(ctx, rp.RepoUUID)
	if err != nil {
		return false, err
	}
	if exists && cur == p.DeclaredBodySHA {
		if err := body.Verify(); err != nil {
			return false, err
		}
		_, err := s.st.PutRepoObject(ctx, rp.ID, wire.ObjectKindConfig, wire.ObjectConfigOID, size)
		return false, err
	}
	add := size
	if row, err := s.st.RepoObject(ctx, rp.ID, wire.ObjectKindConfig, wire.ObjectConfigOID); err == nil {
		add -= row.Size
	}
	if add > 0 {
		if err := s.st.CheckBackupQuota(ctx, rp.UserID, add); err != nil {
			return false, mapQuotaErr(err)
		}
	}
	if err := bs.PutConfig(ctx, rp.RepoUUID, body, size, p.DeclaredBodySHA); err != nil {
		return false, mapPutErr(err, body)
	}
	if err := body.Verify(); err != nil {
		return false, err
	}
	if _, err := s.st.PutRepoObject(ctx, rp.ID, wire.ObjectKindConfig, wire.ObjectConfigOID, size); err != nil {
		return false, err
	}
	return true, nil
}

func mapQuotaErr(err error) error {
	if errors.Is(err, store.ErrBackupQuota) {
		return errf(413, wire.CodeBackupQuotaExceeded, "Backup quota exceeded")
	}
	return err
}

func (s *Server) handleListObjects(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	kind := r.URL.Query().Get("kind")
	if kind != "" && !backup.ValidKind(kind) {
		writeErr(w, r, errInvalidOID())
		return
	}
	after, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	list, err := s.st.ListRepoObjects(r.Context(), rp.ID, kind, after, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.ObjectInfo]{Items: make([]wire.ObjectInfo, 0, len(list))}
	for _, o := range list {
		out.Items = append(out.Items, wire.ObjectInfo{OID: o.OID, Size: o.Size})
	}
	if len(list) == limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleMissingObjects(w http.ResponseWriter, r *http.Request) {
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.ObjectMissingRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if !backup.ValidKind(req.Kind) {
		writeErr(w, r, errInvalidOID())
		return
	}
	if len(req.OIDs) > wire.ObjectMissingMaxOIDs {
		writeErr(w, r, repoValidation(map[string]string{"oids": fmt.Sprintf("at most %d per request", wire.ObjectMissingMaxOIDs)}))
		return
	}
	for _, o := range req.OIDs {
		if !backup.ValidOID(req.Kind, o) {
			writeErr(w, r, errInvalidOID())
			return
		}
	}
	missing, err := s.st.MissingRepoObjects(r.Context(), rp.ID, req.Kind, req.OIDs)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, wire.ObjectMissingResponse{Missing: missing})
}
