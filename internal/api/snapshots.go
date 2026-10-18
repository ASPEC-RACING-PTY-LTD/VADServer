// Owner: SRV-2 (P1-15). Scope: api-v1 §5 locks and snapshot commit, §5.4
// snapshot catalog list/get (feature backup.v1, together with P1-14's
// repositories and objects). Tree, search, diff, PATCH and catalog rebuild
// stay 501 until SRV-C (P1-16) and later tasks implement them.
//
//   - Locks: shared/exclusive with TTL (internal/backup/locks.go). A
//     conflict is 409 repo_locked {holder, expires_at}; refreshing or
//     releasing an unknown, expired or foreign lock is 404 lock_not_found
//     (rev 1.1b). Refresh takes an optional {ttl_seconds} and returns the
//     new {lock_id, expires_at}; release returns 204.
//   - Commit (streamed): the body is hashed and verified before any state is
//     written. It is idempotent by snapshot_oid (200 with the same id). A
//     new commit needs the profile consent to be active for (device,
//     user_sid) (403 profile_not_authorised / 423 profile_blacklisted) and
//     every root, index and pack object to exist (409 pack_missing with at
//     most 1000 oids). The run is linked through job_runs.snapshot_id and
//     the row starts with ingest_state pending for catalog ingestion.

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"unicode/utf8"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
	"github.com/go-chi/chi/v5"
)

func init() { implementFeature(wire.FeatureBackup) }

// mountSnapshots registers the routes of this feature area.
func (s *Server) mountSnapshots(rt routes) {
	rt.Dev.Post("/repos/{id}/locks", s.handleAcquireLock)
	rt.Dev.Put("/repos/{id}/locks/{lock_id}", s.handleRefreshLock)
	rt.Dev.Delete("/repos/{id}/locks/{lock_id}", s.handleReleaseLock)
	rt.Dev.Post("/repos/{id}/snapshots", s.handleCommitSnapshot) // streamed (api-v1 §1.2)
	rt.Sess.Get("/snapshots", s.handleListSnapshots)
	rt.Sess.Get("/snapshots/diff", notImplemented)
	rt.Sess.Get("/snapshots/{id}", s.handleGetSnapshot)
	rt.Sess.Patch("/snapshots/{id}", notImplemented) // unprotect -> approval
	rt.Sess.Get("/snapshots/{id}/tree", s.handleSnapshotTree)
	rt.Sess.Get("/snapshots/{id}/search", s.handleSnapshotSearch)
	rt.Sess.Post("/catalog/rebuild", notImplemented) // 202 operation
}

// --- locks ---

const maxLockIDLen = 64

func errLockNotFound() *apiError {
	return errf(404, wire.CodeLockNotFound, "Lock not found or expired")
}

func lockErr(err error) error {
	var le *backup.LockedError
	switch {
	case errors.As(err, &le):
		return &apiError{Status: 409, Code: wire.CodeRepoLocked, Message: "The repository is locked",
			Details: map[string]any{"holder": le.Holder, "expires_at": le.ExpiresAt}}
	case errors.Is(err, backup.ErrLockNotFound):
		return errLockNotFound()
	case errors.Is(err, backup.ErrInvalidLock):
		return repoValidation(map[string]string{"ttl_seconds": fmt.Sprintf("must be %d..%d", backup.MinLockTTL, backup.MaxLockTTL)})
	}
	return err
}

func lockIDParam(r *http.Request) (string, error) {
	id := chi.URLParam(r, "lock_id")
	if id == "" || len(id) > maxLockIDLen {
		return "", errLockNotFound()
	}
	return id, nil
}

func (s *Server) handleAcquireLock(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.LockRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	fields := map[string]string{}
	if !backup.ValidLockKind(req.Kind) {
		fields["kind"] = "must be shared or exclusive"
	}
	ttl, terr := backup.NormalizeTTL(req.TTLSeconds)
	if terr != nil {
		fields["ttl_seconds"] = fmt.Sprintf("must be %d..%d", backup.MinLockTTL, backup.MaxLockTTL)
	}
	if len(fields) > 0 {
		writeErr(w, r, repoValidation(fields))
		return
	}
	lk, err := backup.NewLocks(s.st).Acquire(r.Context(), rp.ID, p.Device.ID, req.Kind, "", ttl)
	if err != nil {
		writeErr(w, r, lockErr(err))
		return
	}
	writeJSON(w, 201, wire.Lock{LockID: lk.ID, ExpiresAt: lk.ExpiresAt})
}

func (s *Server) handleRefreshLock(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	id, err := lockIDParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Optional body {ttl_seconds}; a kind, when sent, is ignored.
	var req wire.LockRequest
	if err := decodeOptional(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Kind != "" && !backup.ValidLockKind(req.Kind) {
		writeErr(w, r, repoValidation(map[string]string{"kind": "must be shared or exclusive"}))
		return
	}
	lk, err := backup.NewLocks(s.st).Refresh(r.Context(), rp.ID, p.Device.ID, id, req.TTLSeconds)
	if err != nil {
		writeErr(w, r, lockErr(err))
		return
	}
	writeJSON(w, 200, wire.Lock{LockID: lk.ID, ExpiresAt: lk.ExpiresAt})
}

func (s *Server) handleReleaseLock(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	id, err := lockIDParam(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := backup.NewLocks(s.st).Release(r.Context(), rp.ID, p.Device.ID, id); err != nil {
		writeErr(w, r, lockErr(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- snapshot commit ---

// Commit field limits.
const (
	maxSnapshotTags   = 50
	maxSnapshotTagLen = 100
	maxSnapshotText   = 1024
	maxMissingListed  = 1000
)

var (
	snapshotKinds = map[string]bool{wire.JobKindFolder: true, wire.JobKindProfile: true, wire.JobKindApp: true, wire.JobKindGame: true,
		wire.JobKindDevProject: true, wire.JobKindRegistry: true, wire.JobKindInventory: true}
	snapshotModes = map[string]bool{wire.JobModeFull: true, wire.JobModeDataOnly: true, wire.JobModeReinstallInventory: true}
)

func errSnapshotNotFound() *apiError {
	return errf(404, wire.CodeSnapshotNotFound, "Snapshot not found")
}

func validText(v string) bool {
	return len(v) <= maxSnapshotText && utf8.ValidString(v)
}

// validateCommit checks the request shape; oids are 400 invalid_oid, other
// fields 422 validation.
func validateCommit(req *wire.SnapshotCommitRequest) error {
	if !backup.ValidOID(wire.ObjectKindSnapshots, req.SnapshotOID) {
		return errInvalidOID()
	}
	for _, o := range req.IndexOIDs {
		if !backup.ValidOID(wire.ObjectKindIndex, o) {
			return errInvalidOID()
		}
	}
	for _, o := range req.PackOIDs {
		if !backup.ValidOID(wire.ObjectKindData, o) {
			return errInvalidOID()
		}
	}
	fields := map[string]string{}
	if req.Kind != "" && !snapshotKinds[req.Kind] {
		fields["kind"] = "unknown kind"
	}
	if req.Mode != "" && !snapshotModes[req.Mode] {
		fields["mode"] = "unknown mode"
	}
	if req.Epoch < 1 {
		fields["epoch"] = "must be at least 1"
	}
	if req.JobID < 0 {
		fields["job_id"] = "invalid"
	}
	if req.RunID < 0 || (req.RunID > 0 && req.JobID == 0) {
		fields["run_id"] = "invalid, or given without job_id"
	}
	if req.ParentSnapshotID != nil && *req.ParentSnapshotID <= 0 {
		fields["parent_snapshot_id"] = "invalid"
	}
	if req.UserSID == "" || !validText(req.UserSID) {
		fields["user_sid"] = "required"
	}
	if req.ProfileConsentID == "" || !validText(req.ProfileConsentID) {
		fields["profile_consent_id"] = "required"
	}
	for k, v := range map[string]string{"hostname": req.Hostname, "os_user": req.OSUser, "profile_path": req.ProfilePath} {
		if !validText(v) {
			fields[k] = fmt.Sprintf("at most %d bytes of UTF-8", maxSnapshotText)
		}
	}
	if len(req.Tags) > maxSnapshotTags {
		fields["tags"] = fmt.Sprintf("at most %d tags", maxSnapshotTags)
	}
	for _, t := range req.Tags {
		if t == "" || len(t) > maxSnapshotTagLen || !utf8.ValidString(t) {
			fields["tags"] = fmt.Sprintf("tags are 1..%d bytes of UTF-8", maxSnapshotTagLen)
		}
	}
	st := req.Stats
	if req.ClientTime < 0 || st.FileCount < 0 || st.DirCount < 0 || st.LogicalBytes < 0 || st.NewBytes < 0 || st.NewObjects < 0 || st.DurationMS < 0 {
		fields["stats"] = "values must not be negative"
	}
	if len(fields) > 0 {
		return repoValidation(fields)
	}
	return nil
}

// decodeCommit reads the streamed commit body and verifies its signed hash
// before returning; a body that does not match is 400 body_mismatch even
// when it is not valid JSON.
func decodeCommit(body *streamedBody, req *wire.SnapshotCommitRequest) error {
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	derr := dec.Decode(req)
	var ae *apiError
	if errors.As(derr, &ae) { // too large
		return derr
	}
	if err := body.Verify(); err != nil {
		return err
	}
	if derr != nil {
		return errf(400, "bad_json", "Invalid request body: %v", derr)
	}
	if dec.More() {
		return errf(400, "bad_json", "Invalid request body: trailing data")
	}
	return nil
}

// checkCommitRefs resolves the job, run and parent of a new commit and
// fills defaults for kind and mode from the job.
func (s *Server) checkCommitRefs(ctx context.Context, p *Principal, rp *store.Repository, req *wire.SnapshotCommitRequest) error {
	fields := map[string]string{}
	if req.JobID > 0 {
		j, err := s.st.Job(ctx, req.JobID)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && (j.UserID != rp.UserID || j.DeviceID != p.Device.ID || (j.RepoID != 0 && j.RepoID != rp.ID))):
			fields["job_id"] = "unknown job for this device and repository"
		case err != nil:
			return err
		default:
			if req.Kind == "" {
				req.Kind = j.Kind
			}
			if req.Mode == "" {
				req.Mode = j.Mode
			}
		}
	}
	if req.RunID > 0 && len(fields) == 0 {
		run, err := s.st.Run(ctx, req.RunID)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && (run.JobID != req.JobID || run.DeviceID != p.Device.ID)):
			fields["run_id"] = "unknown run for this job"
		case err != nil:
			return err
		}
	}
	if req.ParentSnapshotID != nil {
		parent, err := s.st.GetSnapshot(ctx, *req.ParentSnapshotID)
		switch {
		case errors.Is(err, store.ErrNotFound) || (err == nil && parent.RepoID != rp.ID):
			fields["parent_snapshot_id"] = "unknown snapshot in this repository"
		case err != nil:
			return err
		}
	}
	if req.Mode == "" {
		req.Mode = wire.JobModeFull
	}
	if req.Kind == "" {
		fields["kind"] = "required"
	}
	if len(fields) > 0 {
		return repoValidation(fields)
	}
	return nil
}

func (s *Server) handleCommitSnapshot(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	rp, err := s.loadRepo(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if r.ContentLength > maxCommitBytes {
		writeErr(w, r, errf(413, "too_large", "Snapshot commits are limited to %d bytes", maxCommitBytes))
		return
	}
	body, err := newStreamedBody(r, maxCommitBytes)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.SnapshotCommitRequest
	if err := decodeCommit(body, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := validateCommit(&req); err != nil {
		writeErr(w, r, err)
		return
	}
	ctx := r.Context()

	// Replay: same snapshot_oid in this repository.
	if prev, err := s.st.SnapshotByOID(ctx, rp.ID, req.SnapshotOID); err == nil {
		writeJSON(w, 200, wire.SnapshotCommitResponse{SnapshotID: prev.ID})
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		writeErr(w, r, err)
		return
	}

	if err := s.requireProfileConsent(ctx, p.Device.ID, req.UserSID, req.ProfileConsentID); err != nil {
		writeErr(w, r, err)
		return
	}
	prof, err := s.st.DeviceProfileBySID(ctx, p.Device.ID, req.UserSID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.checkCommitRefs(ctx, p, rp, &req); err != nil {
		writeErr(w, r, err)
		return
	}

	sn := &store.Snapshot{RepoID: rp.ID, UserID: rp.UserID, DeviceID: p.Device.ID, SnapshotOID: req.SnapshotOID,
		JobID: req.JobID, RunID: req.RunID, Kind: req.Kind, Mode: req.Mode, Hostname: req.Hostname, OSUser: req.OSUser,
		ProfileSID: req.UserSID, ProfileUsername: prof.Username, ProfilePath: req.ProfilePath, ProfileConsentID: req.ProfileConsentID,
		Epoch: req.Epoch, ClientTime: req.ClientTime, Tags: req.Tags, Stats: req.Stats, IndexOIDs: req.IndexOIDs}
	if req.ParentSnapshotID != nil {
		sn.ParentSnapshotID = *req.ParentSnapshotID
	}
	created, err := s.st.CommitSnapshot(ctx, sn, req.PackOIDs)
	var me *store.ErrObjectsMissing
	if errors.As(err, &me) {
		missing := me.Missing
		if len(missing) > maxMissingListed {
			missing = missing[:maxMissingListed]
		}
		writeErr(w, r, &apiError{Status: 409, Code: wire.CodePackMissing, Message: "The snapshot references objects that are not stored",
			Details: map[string]any{"missing": missing}})
		return
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := 200
	if created {
		status = 201
		s.activity(r, rp.UserID, "snapshot.commit", rp.Name, fmt.Sprintf("snapshot %d (%s)", sn.ID, sn.Kind), false)
		s.queueCatalogIngest(sn.ID) // catalog ingestion (P1-16, §5.2 step 3)
	}
	writeJSON(w, status, wire.SnapshotCommitResponse{SnapshotID: sn.ID})
}

// --- catalog list/get (sess) ---

func snapshotView(sn *store.Snapshot) wire.Snapshot {
	v := wire.Snapshot{ID: sn.ID, RepoID: sn.RepoID, SnapshotOID: sn.SnapshotOID, JobID: sn.JobID, DeviceID: sn.DeviceID,
		Kind: sn.Kind, Mode: sn.Mode, Hostname: sn.Hostname, OSUser: sn.OSUser, ProfilePath: sn.ProfilePath,
		ClientTime: sn.ClientTime, CommittedAt: sn.CommittedAt, Stats: sn.Stats, Tags: sn.Tags, Protected: sn.Protected,
		Status: sn.Status, Browsable: sn.IngestState == wire.SnapshotIngestDone, Epoch: sn.Epoch,
		Profile: wire.SnapshotProfile{SID: sn.ProfileSID, Username: sn.ProfileUsername}, IngestState: sn.IngestState}
	if sn.ParentSnapshotID > 0 {
		id := sn.ParentSnapshotID
		v.ParentID = &id
	}
	if sn.ProtectedUntil > 0 {
		u := sn.ProtectedUntil
		v.ProtectedUntil = &u
	}
	if v.Tags == nil {
		v.Tags = []string{}
	}
	return v
}

func (s *Server) handleListSnapshots(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	before, limit, err := listParams(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	q := r.URL.Query()
	f := store.SnapshotFilter{UserID: p.User.ID, ProfileSID: q.Get("profile"), Kind: q.Get("kind"), Tag: q.Get("tag"), BeforeID: before, Limit: limit}
	fields := map[string]string{}
	for name, dst := range map[string]*int64{"repo": &f.RepoID, "job": &f.JobID, "device": &f.DeviceID, "from": &f.From, "to": &f.To} {
		if v := q.Get(name); v != "" {
			n, perr := strconv.ParseInt(v, 10, 64)
			if perr != nil || n < 0 {
				fields[name] = "must be a non-negative integer"
				continue
			}
			*dst = n
		}
	}
	if v := q.Get("protected"); v != "" {
		b, perr := strconv.ParseBool(v)
		if perr != nil {
			fields["protected"] = "must be true or false"
		} else {
			f.Protected = &b
		}
	}
	if len(fields) > 0 {
		writeErr(w, r, repoValidation(fields))
		return
	}
	list, err := s.st.ListSnapshots(r.Context(), f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.List[wire.Snapshot]{Items: make([]wire.Snapshot, 0, len(list))}
	for i := range list {
		out.Items = append(out.Items, snapshotView(&list[i]))
	}
	if len(list) == limit {
		out.NextCursor = strconv.FormatInt(list[len(list)-1].ID, 10)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleGetSnapshot(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sn, err := s.st.GetSnapshot(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && sn.UserID != principal(r).User.ID) {
		err = errSnapshotNotFound()
	}
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, snapshotView(sn))
}
