package api

// Copy rules ("RAID style" copies): files matching a rule get a copy on a
// target, a local storage location (local:<id>) or a folder on a storage
// node (node:<id>, encrypted). A background service
//
//   - plans: works out which blobs each rule wants copied (and which copies
//     are no longer wanted: those are deleted only after the rule's delay, so
//     a mistake can be undone with "Recover");
//   - copies: local copies directly, node copies through node jobs
//     (encrypted with the node's own copy key, node_keys.go);
//   - checks: every copy and the original are re-hashed on a schedule; a
//     damaged copy is made again and a damaged or missing original is
//     repaired from a good copy;
//   - serves reads: when an original is missing, Open falls back to a copy.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/replica"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	copyVerifyEvery    = 7 * 24 * time.Hour  // re-check copies and originals
	copyPlanEvery      = 10 * time.Minute    // re-plan without a rule change
	copyNodeInFlight   = 2                   // put jobs per node at a time
	copyJobClaimedMax  = 6 * time.Hour       // a claimed job older than this failed
	copyJobQueuedMax   = 24 * time.Hour      // a queued job older than this failed
	copyMaxBackoff     = 6 * time.Hour       // between retries of a failed copy
	copyReadFromNode   = 10 * time.Minute    // a read waits this long for a node
	copyRecoverTimeout = 2 * time.Hour       // recovering a deleted file from a node
)

type copyService struct {
	mu        sync.Mutex
	kick      chan struct{}
	replan    bool
	running   bool
	lastRun   int64
	lastPlan  int64
	lastError string
	planned   int64
}

var copyServices sync.Map // *Server -> *copyService

func (s *Server) copies() *copyService {
	v, _ := copyServices.LoadOrStore(s, &copyService{kick: make(chan struct{}, 1), replan: true})
	return v.(*copyService)
}

func (cs *copyService) wake(replan bool) {
	cs.mu.Lock()
	cs.replan = cs.replan || replan
	cs.mu.Unlock()
	select {
	case cs.kick <- struct{}{}:
	default:
	}
}

// RunCopies runs the copy service until ctx ends. It also installs the read
// fallback on the storage manager.
func (s *Server) RunCopies(ctx context.Context) {
	s.blobs.SetFallback(func(locID, userID int64, sha string) (*os.File, error) {
		return s.readFromCopy(context.Background(), locID, userID, sha)
	})
	cs := s.copies()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.copyCycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-cs.kick:
		}
	}
}

func (s *Server) copyCycle(ctx context.Context) {
	cs := s.copies()
	cs.mu.Lock()
	if cs.running {
		cs.mu.Unlock()
		return
	}
	cs.running = true
	replan := cs.replan || time.Since(time.UnixMilli(cs.lastPlan)) > copyPlanEvery
	cs.replan = false
	cs.mu.Unlock()
	var errs []string
	defer func() {
		cs.mu.Lock()
		cs.running, cs.lastRun = false, time.Now().UnixMilli()
		cs.lastError = strings.Join(errs, "; ")
		cs.mu.Unlock()
	}()
	s.failStaleJobs(ctx)
	if replan {
		n, err := s.planCopies(ctx)
		cs.mu.Lock()
		cs.lastPlan, cs.planned = time.Now().UnixMilli(), n
		cs.mu.Unlock()
		if err != nil {
			errs = append(errs, "plan: "+err.Error())
			slog.Warn("copy planning failed", "err", err)
		}
	}
	if err := s.workCopies(ctx); err != nil {
		errs = append(errs, err.Error())
	}
	s.checkPrimaries(ctx)
	s.st.PruneNodeJobs(ctx, time.Now().Add(-7*24*time.Hour).UnixMilli())
}

// --- targets --------------------------------------------------------------------

type copyTarget struct {
	Kind string // local, node
	ID   int64
}

func parseTarget(t string) (copyTarget, error) {
	kind, id, ok := strings.Cut(t, ":")
	n, err := strconv.ParseInt(id, 10, 64)
	if !ok || err != nil || n <= 0 || (kind != "local" && kind != "node") {
		return copyTarget{}, fmt.Errorf("invalid copy target %q", t)
	}
	return copyTarget{Kind: kind, ID: n}, nil
}

// --- planning ---------------------------------------------------------------------

// planCopies records the copies the rules want and marks the copies no rule
// wants any more for (delayed) deletion. It returns the number of wanted copies.
func (s *Server) planCopies(ctx context.Context) (int64, error) {
	rules, err := s.st.CopyRules(ctx)
	if err != nil {
		return 0, err
	}
	type active struct {
		r   replica.Rule
		tgt string
		loc *int64 // local target location (skip files already stored there)
		dly int64
	}
	var act []active
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		t, err := parseTarget(r.Target)
		if err != nil {
			continue
		}
		a := active{r: replica.Rule{UserID: r.UserID, SourceLocationID: r.SourceLocationID, PathPattern: r.PathPattern, Extensions: r.Extensions, MinSize: r.MinSize, MaxSize: r.MaxSize},
			tgt: r.Target, dly: r.DeleteDelayHours}
		if t.Kind == "local" {
			id := t.ID
			a.loc = &id
		}
		act = append(act, a)
	}
	existing := map[string]string{} // user|sha|target -> state
	if err := s.st.AllCopyKeys(ctx, func(u int64, sha, tgt, st string, _ int64) {
		existing[fmt.Sprintf("%d|%s|%s", u, sha, tgt)] = st
	}); err != nil {
		return 0, err
	}
	deleting, err := s.st.ListCopies(ctx, "deleting", 1_000_000)
	if err != nil {
		return 0, err
	}
	for _, c := range deleting {
		existing[fmt.Sprintf("%d|%s|%s", c.UserID, c.SHA256, c.Target)] = "deleting"
	}
	wanted := map[string]bool{}
	err = s.st.StoredFiles(ctx, func(f store.StoredFile) error {
		for _, a := range act {
			if a.loc != nil && *a.loc == f.LocationID {
				continue // already stored on the target
			}
			if !a.r.Matches(replica.File{UserID: f.UserID, LocationID: f.LocationID, Path: f.Path, Size: f.Size}) {
				continue
			}
			k := fmt.Sprintf("%d|%s|%s", f.UserID, f.SHA256, a.tgt)
			if wanted[k] {
				continue
			}
			wanted[k] = true
			if st, ok := existing[k]; !ok || st == "deleting" {
				if err := s.st.WantCopy(ctx, f, a.tgt, max(a.dly, 0)); err != nil {
					return err
				}
			}
		}
		return ctx.Err()
	})
	if err != nil {
		return 0, err
	}
	// Copies nobody wants: delete after the delay (pending/failed ones now).
	now := time.Now()
	for k, st := range existing {
		if wanted[k] || st == "deleting" {
			continue
		}
		parts := strings.SplitN(k, "|", 3)
		u, _ := strconv.ParseInt(parts[0], 10, 64)
		c, err := s.st.BlobCopy(ctx, u, parts[1], parts[2])
		if err != nil {
			continue
		}
		at := now.UnixMilli()
		if c.State == "ok" {
			at = now.Add(time.Duration(c.DeleteDelayHours) * time.Hour).UnixMilli()
		}
		c.State, c.DeleteAt, c.NextAt = "deleting", &at, 0
		_ = s.st.SetCopy(ctx, c)
	}
	return int64(len(wanted)), nil
}

// --- work -----------------------------------------------------------------------

func backoff(attempts int64) time.Duration {
	d := time.Minute << min(attempts, 10)
	return min(d, copyMaxBackoff)
}

func (s *Server) copyFailed(ctx context.Context, c *store.BlobCopy, msg string) {
	c.Attempts++
	c.State, c.LastError = "failed", msg
	c.NextAt = time.Now().Add(backoff(c.Attempts)).UnixMilli()
	_ = s.st.SetCopy(ctx, c)
	slog.Warn("file copy failed", "user", c.UserID, "sha", c.SHA256, "target", c.Target, "err", msg)
}

func (s *Server) workCopies(ctx context.Context) error {
	due, err := s.st.DueCopies(ctx, time.Now().Add(-copyVerifyEvery).UnixMilli(), 500)
	if err != nil {
		return err
	}
	for i := range due {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c := &due[i]
		t, err := parseTarget(c.Target)
		if err != nil {
			_ = s.st.DeleteCopyRow(ctx, c.UserID, c.SHA256, c.Target)
			continue
		}
		switch {
		case c.State == "deleting":
			s.deleteCopy(ctx, c, t)
		case c.State == "pending" || c.State == "failed":
			s.makeCopy(ctx, c, t)
		case c.State == "ok":
			s.verifyCopy(ctx, c, t)
		}
	}
	return nil
}

func (s *Server) nodeLocation(ctx context.Context, id int64) (*store.NodeLocation, *store.StorageNode, error) {
	l, err := s.st.NodeLocation(ctx, id)
	if err != nil {
		return nil, nil, errors.New("the node location no longer exists")
	}
	n, err := s.st.StorageNode(ctx, l.NodeID)
	if err != nil || n.Status != "approved" {
		return l, nil, errors.New("the node is not approved")
	}
	return l, n, nil
}

func (s *Server) makeCopy(ctx context.Context, c *store.BlobCopy, t copyTarget) {
	size, loc, err := s.st.BlobRow(ctx, c.UserID, c.SHA256)
	if errors.Is(err, store.ErrNotFound) {
		_ = s.st.DeleteCopyRow(ctx, c.UserID, c.SHA256, c.Target) // nothing to copy any more
		return
	}
	if err != nil {
		return
	}
	if t.Kind == "local" {
		f, err := s.blobs.Open(loc, c.UserID, c.SHA256)
		if err != nil {
			s.copyFailed(ctx, c, "cannot read the original: "+err.Error())
			return
		}
		_, err = s.blobs.WriteReplica(t.ID, c.UserID, c.SHA256, f)
		f.Close()
		if err != nil {
			msg := err.Error()
			if strings.Contains(msg, "hash mismatch") || strings.Contains(msg, "checksum") {
				msg = "the original file is damaged (checksum mismatch)"
				_ = s.st.RecordPrimaryCheck(ctx, c.UserID, c.SHA256, false, "checksum mismatch while copying")
				go s.repairPrimary(context.Background(), c.UserID, c.SHA256, loc)
			}
			s.copyFailed(ctx, c, msg)
			return
		}
		now := time.Now().UnixMilli()
		c.State, c.Size, c.Attempts, c.NextAt, c.LastError, c.VerifiedAt = "ok", size, 0, 0, "", &now
		_ = s.st.SetCopy(ctx, c)
		return
	}
	l, n, err := s.nodeLocation(ctx, t.ID)
	if err != nil {
		s.copyFailed(ctx, c, err.Error())
		return
	}
	if l.Status != "ok" {
		s.copyFailed(ctx, c, "the node location is not ready ("+firstNonEmpty(l.Error, l.Status)+")")
		return
	}
	if _, err := s.nodeCopyKey(ctx, n.ID); err != nil {
		s.copyFailed(ctx, c, "the node's copy key is unavailable: "+err.Error())
		return
	}
	if open, _ := s.st.CountOpenNodeJobs(ctx, n.ID, nodeapi.JobPut); open >= copyNodeInFlight {
		return // the node is busy; try again next cycle
	}
	if _, err := s.enqueueJob(ctx, n.ID, nodeapi.JobPut, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, UserID: c.UserID, SHA256: c.SHA256, Size: size}, Target: c.Target}); err != nil {
		return
	}
	// The job's result sets the state; until then do not hand out another.
	c.State, c.NextAt = "pending", time.Now().Add(copyJobClaimedMax+copyJobQueuedMax).UnixMilli()
	_ = s.st.SetCopy(ctx, c)
}

func (s *Server) verifyCopy(ctx context.Context, c *store.BlobCopy, t copyTarget) {
	if t.Kind == "local" {
		now := time.Now().UnixMilli()
		f, err := s.blobs.OpenReplica(t.ID, c.UserID, c.SHA256)
		if err == nil {
			sum, herr := sha256Of(f)
			f.Close()
			if herr == nil && sum == c.SHA256 {
				c.VerifiedAt, c.LastError = &now, ""
				_ = s.st.SetCopy(ctx, c)
				return
			}
		}
		c.State, c.NextAt, c.LastError = "pending", 0, "the copy was missing or damaged; copying again"
		_ = s.st.SetCopy(ctx, c)
		return
	}
	l, n, err := s.nodeLocation(ctx, t.ID)
	if err != nil {
		c.NextAt, c.LastError = time.Now().Add(time.Hour).UnixMilli(), "cannot check: "+err.Error()
		_ = s.st.SetCopy(ctx, c)
		return
	}
	if _, err := s.enqueueJob(ctx, n.ID, nodeapi.JobVerify, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, UserID: c.UserID, SHA256: c.SHA256}, Target: c.Target}); err == nil {
		c.NextAt = time.Now().Add(copyJobClaimedMax + copyJobQueuedMax).UnixMilli()
		_ = s.st.SetCopy(ctx, c)
	}
}

func (s *Server) deleteCopy(ctx context.Context, c *store.BlobCopy, t copyTarget) {
	if t.Kind == "local" {
		if _, ok := s.blobs.Location(t.ID); !ok {
			_ = s.blobs.Reload(ctx)
		}
		if _, ok := s.blobs.Location(t.ID); !ok { // the location was removed: nothing left to delete
			_ = s.st.DeleteCopyRow(ctx, c.UserID, c.SHA256, c.Target)
			return
		}
		if err := s.blobs.RemoveReplica(t.ID, c.UserID, c.SHA256); err != nil && !errors.Is(err, os.ErrNotExist) {
			c.NextAt, c.LastError = time.Now().Add(time.Hour).UnixMilli(), "delete failed: "+err.Error()
			_ = s.st.SetCopy(ctx, c)
			return
		}
		_ = s.st.DeleteCopyRow(ctx, c.UserID, c.SHA256, c.Target)
		return
	}
	l, n, err := s.nodeLocation(ctx, t.ID)
	if err != nil {
		if l == nil { // location gone: nothing left to delete
			_ = s.st.DeleteCopyRow(ctx, c.UserID, c.SHA256, c.Target)
			return
		}
		c.NextAt, c.LastError = time.Now().Add(time.Hour).UnixMilli(), "delete waits: "+err.Error()
		_ = s.st.SetCopy(ctx, c)
		return
	}
	if _, err := s.enqueueJob(ctx, n.ID, nodeapi.JobDelete, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, UserID: c.UserID, SHA256: c.SHA256}, Target: c.Target}); err == nil {
		c.NextAt = time.Now().Add(copyJobClaimedMax + copyJobQueuedMax).UnixMilli()
		_ = s.st.SetCopy(ctx, c)
	}
}

func (s *Server) failStaleJobs(ctx context.Context) {
	now := time.Now()
	jobs, err := s.st.StaleNodeJobs(ctx, now.Add(-copyJobClaimedMax).UnixMilli(), now.Add(-copyJobQueuedMax).UnixMilli())
	if err != nil {
		return
	}
	for _, j := range jobs {
		s.failJob(ctx, j, "the node did not finish the job in time")
	}
}

func sha256Of(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.CopyBuffer(h, r, make([]byte, 1<<20)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// --- originals: checks, repair, read fallback ------------------------------------

func (s *Server) checkPrimaries(ctx context.Context) {
	due, err := s.st.DuePrimaryChecks(ctx, time.Now().Add(-copyVerifyEvery).UnixMilli(), 20)
	if err != nil {
		return
	}
	for _, b := range due {
		if ctx.Err() != nil {
			return
		}
		sum, err := s.blobs.HashPrimary(b.LocationID, b.UserID, b.SHA256)
		if err == nil && sum == b.SHA256 {
			_ = s.st.RecordPrimaryCheck(ctx, b.UserID, b.SHA256, true, "")
			continue
		}
		msg := "checksum mismatch"
		if err != nil {
			msg = err.Error()
		}
		_ = s.st.RecordPrimaryCheck(ctx, b.UserID, b.SHA256, false, msg)
		slog.Warn("original file is missing or damaged; repairing from a copy", "user", b.UserID, "sha", b.SHA256, "err", msg)
		s.repairPrimary(ctx, b.UserID, b.SHA256, b.LocationID)
	}
}

// repairPrimary restores a blob's original from its first usable copy.
func (s *Server) repairPrimary(ctx context.Context, userID int64, sha string, loc int64) error {
	err := s.restoreFromCopies(ctx, userID, sha, loc, "repair", copyReadFromNode)
	if err == nil {
		_ = s.st.RecordPrimaryCheck(ctx, userID, sha, true, "")
		s.st.LogActivity(ctx, store.Activity{Action: "file.repaired", Target: sha, Details: "the original was missing or damaged and was restored from a copy", Audit: true})
		slog.Info("original file repaired from a copy", "user", userID, "sha", sha)
	}
	return err
}

// restoreFromCopies writes the content of a blob into location loc from the
// first copy that works: local copies first, then nodes.
func (s *Server) restoreFromCopies(ctx context.Context, userID int64, sha string, loc int64, purpose string, nodeWait time.Duration) error {
	copies, err := s.st.CopiesOf(ctx, userID, sha)
	if err != nil {
		return err
	}
	sort.SliceStable(copies, func(i, j int) bool { return strings.HasPrefix(copies[i].Target, "local:") && !strings.HasPrefix(copies[j].Target, "local:") })
	last := errors.New("no copy of this file exists")
	for _, c := range copies {
		if c.State != "ok" && c.State != "deleting" {
			continue
		}
		t, err := parseTarget(c.Target)
		if err != nil {
			continue
		}
		if t.Kind == "local" {
			f, err := s.blobs.OpenReplica(t.ID, userID, sha)
			if err != nil {
				last = err
				continue
			}
			err = s.blobs.RestorePrimary(loc, userID, sha, f)
			f.Close()
			if err == nil {
				return nil
			}
			last = err
			continue
		}
		l, n, err := s.nodeLocation(ctx, t.ID)
		if err != nil {
			last = err
			continue
		}
		id, err := s.enqueueJob(ctx, n.ID, nodeapi.JobGet, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, UserID: userID, SHA256: sha}, Target: c.Target, Purpose: purpose, PrimaryLoc: loc})
		if err != nil {
			last = err
			continue
		}
		if err = s.waitJob(ctx, id, nodeWait); err == nil {
			return nil
		}
		last = err
	}
	return last
}

// readFromCopy is the storage fallback for a missing original: it repairs
// the original from a copy and opens it.
func (s *Server) readFromCopy(ctx context.Context, locID, userID int64, sha string) (*os.File, error) {
	if err := s.repairPrimary(ctx, userID, sha, locID); err != nil {
		return nil, fmt.Errorf("%w: %v", os.ErrNotExist, err)
	}
	return s.blobs.Open(locID, userID, sha)
}

// --- admin ------------------------------------------------------------------------

func (s *Server) mountCopiesAdmin(rt routes) {
	rt.Admin.Get("/admin/node-locations", s.handleNodeLocations)
	rt.Admin.Post("/admin/node-locations", s.handleNodeLocationCreate)
	rt.Admin.Post("/admin/node-locations/{id}/prepare", s.handleNodeLocationPrepare)
	rt.Admin.Delete("/admin/node-locations/{id}", s.handleNodeLocationDelete)
	rt.Admin.Get("/admin/copy-targets", s.handleCopyTargets)
	rt.Admin.Get("/admin/copy-rules", s.handleCopyRules)
	rt.Admin.Post("/admin/copy-rules", s.handleCopyRuleSave)
	rt.Admin.Put("/admin/copy-rules/{id}", s.handleCopyRuleSave)
	rt.Admin.Delete("/admin/copy-rules/{id}", s.handleCopyRuleDelete)
	rt.Admin.Get("/admin/copies/summary", s.handleCopiesSummary)
	rt.Admin.Get("/admin/copies", s.handleCopiesList)
	rt.Admin.Post("/admin/copies/run", s.handleCopiesRun)
	rt.Admin.Post("/admin/copies/verify", s.handleCopiesVerify)
	rt.Admin.Post("/admin/copies/retry", s.handleCopiesRetry)
	rt.Admin.Post("/admin/copies/recover", s.handleCopiesRecover)
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	return id
}

func (s *Server) handleNodeLocations(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.NodeLocations(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleNodeLocationCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID int64  `json:"node_id"`
		Name   string `json:"name"`
		Path   string `json:"path"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name, req.Path = clip(req.Name, 64), strings.TrimRight(strings.TrimSpace(req.Path), `/\`)
	if req.Name == "" || !absNodePath(req.Path) || strings.Contains(req.Path, "..") {
		writeErr(w, r, errf(400, "invalid", "Give a name and an absolute folder path on the node (for example /srv/vad-node/storage)"))
		return
	}
	n, err := s.st.StorageNode(r.Context(), req.NodeID)
	if err != nil || n.Status != "approved" {
		writeErr(w, r, errf(400, "invalid_node", "Choose an approved node"))
		return
	}
	if roots := strings.Fields(n.StorageRoots); len(roots) > 0 && !underAny(req.Path, roots) {
		writeErr(w, r, errf(400, "outside_roots", "The node's owner allows copies only below: %s", strings.Join(roots, ", ")))
		return
	}
	l, err := s.st.CreateNodeLocation(r.Context(), n.ID, req.Name, req.Path)
	if err != nil {
		writeErr(w, r, errf(409, "exists", "A node location with this name or folder already exists"))
		return
	}
	if _, err := s.enqueueJob(r.Context(), n.ID, nodeapi.JobPrepare, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, Path: l.Path}}); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "node_location.created", l.Name, fmt.Sprintf("added node location %q on %s at %s", l.Name, n.Name, l.Path), true)
	writeJSON(w, 201, l)
}

// absNodePath accepts an absolute folder on the node: "/srv/x", or "C:\x"
// for a node on Windows (development). The node checks it again.
func absNodePath(p string) bool {
	return strings.HasPrefix(p, "/") || (len(p) > 2 && p[1] == ':' && (p[2] == '\\' || p[2] == '/'))
}

func underAny(p string, roots []string) bool {
	p = path.Clean(p)
	for _, r := range roots {
		r = path.Clean(r)
		if p == r || strings.HasPrefix(p, r+"/") {
			return true
		}
	}
	return false
}

func (s *Server) handleNodeLocationPrepare(w http.ResponseWriter, r *http.Request) {
	l, err := s.st.NodeLocation(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Node location not found"))
		return
	}
	_ = s.st.SetNodeLocationStatus(r.Context(), l.ID, "preparing", "", 0, 0)
	if _, err := s.enqueueJob(r.Context(), l.NodeID, nodeapi.JobPrepare, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, Path: l.Path}}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleNodeLocationDelete(w http.ResponseWriter, r *http.Request) {
	l, err := s.st.NodeLocation(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Node location not found"))
		return
	}
	target := fmt.Sprintf("node:%d", l.ID)
	if n, _ := s.st.CountCopiesOnTarget(r.Context(), target); n > 0 {
		writeErr(w, r, errf(409, "in_use", "%d copies are stored there. Remove the rules that use it and wait until the copies are deleted.", n))
		return
	}
	for _, rule := range mustRules(s, r.Context()) {
		if rule.Target == target {
			writeErr(w, r, errf(409, "in_use", "The rule %q uses this location", rule.Name))
			return
		}
	}
	if err := s.st.DeleteNodeLocation(r.Context(), l.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "node_location.deleted", l.Name, fmt.Sprintf("removed node location %q", l.Name), true)
	w.WriteHeader(http.StatusNoContent)
}

func mustRules(s *Server, ctx context.Context) []store.CopyRule {
	r, _ := s.st.CopyRules(ctx)
	return r
}

type targetView struct {
	Target string `json:"target"`
	Label  string `json:"label"`
	Kind   string `json:"kind"`
	Ready  bool   `json:"ready"`
	Free   int64  `json:"free"`
	Total  int64  `json:"total"`
}

func (s *Server) copyTargets(ctx context.Context) []targetView {
	var out []targetView
	if locs, err := s.st.ListLocations(ctx); err == nil {
		for _, l := range locs {
			tv := targetView{Target: fmt.Sprintf("local:%d", l.ID), Label: l.Name + " (this server)", Kind: "local", Ready: l.Enabled}
			if _, err := s.blobs.LocalLocation(l.ID); err != nil {
				tv.Ready = false
			}
			out = append(out, tv)
		}
	}
	if nl, err := s.st.NodeLocations(ctx); err == nil {
		for _, l := range nl {
			out = append(out, targetView{Target: fmt.Sprintf("node:%d", l.ID), Label: l.Name + " (node " + l.NodeName + ")", Kind: "node", Ready: l.Status == "ok", Free: l.DiskFree, Total: l.DiskTotal})
		}
	}
	return out
}

func (s *Server) handleCopyTargets(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"items": s.copyTargets(r.Context())})
}

func (s *Server) handleCopyRules(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.CopyRules(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleCopyRuleSave(w http.ResponseWriter, r *http.Request) {
	var req store.CopyRule
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.ID = pathID(r)
	if req.ID != 0 {
		if _, err := s.st.CopyRule(r.Context(), req.ID); err != nil {
			writeErr(w, r, errf(404, "not_found", "Rule not found"))
			return
		}
	}
	req.Name = clip(req.Name, 80)
	req.PathPattern, req.Extensions = clip(req.PathPattern, 300), clip(strings.Join(replica.SplitList(req.Extensions), ","), 300)
	if req.Name == "" {
		writeErr(w, r, errf(400, "invalid_name", "Give the rule a name"))
		return
	}
	t, err := parseTarget(req.Target)
	if err != nil {
		writeErr(w, r, errf(400, "invalid_target", "Choose where the copies go"))
		return
	}
	valid := false
	for _, tv := range s.copyTargets(r.Context()) {
		valid = valid || tv.Target == req.Target
	}
	if !valid {
		writeErr(w, r, errf(400, "invalid_target", "That copy target does not exist"))
		return
	}
	if t.Kind == "local" && req.SourceLocationID != nil && *req.SourceLocationID == t.ID {
		writeErr(w, r, errf(400, "same_location", "The copy must go to a different location than the source"))
		return
	}
	if req.MinSize < 0 || req.MaxSize < 0 || (req.MaxSize > 0 && req.MaxSize < req.MinSize) {
		writeErr(w, r, errf(400, "invalid_size", "Invalid size limits"))
		return
	}
	req.DeleteDelayHours = max(0, min(req.DeleteDelayHours, 24*365))
	if err := s.st.SaveCopyRule(r.Context(), &req); err != nil {
		writeErr(w, r, err)
		return
	}
	s.copies().wake(true)
	s.activity(r, principal(r).User.ID, "copy_rule.saved", req.Name, fmt.Sprintf("saved copy rule %q (%s)", req.Name, req.Target), true)
	writeJSON(w, 200, req)
}

func (s *Server) handleCopyRuleDelete(w http.ResponseWriter, r *http.Request) {
	rule, err := s.st.CopyRule(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Rule not found"))
		return
	}
	if err := s.st.DeleteCopyRule(r.Context(), rule.ID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.copies().wake(true)
	s.activity(r, principal(r).User.ID, "copy_rule.deleted", rule.Name, fmt.Sprintf("deleted copy rule %q; its copies are deleted after %d hours", rule.Name, rule.DeleteDelayHours), true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleCopiesSummary(w http.ResponseWriter, r *http.Request) {
	stats, err := s.st.CopyStats(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	labels := map[string]targetView{}
	for _, tv := range s.copyTargets(r.Context()) {
		labels[tv.Target] = tv
	}
	type row struct {
		store.CopyTargetStats
		Label string `json:"label"`
		Ready bool   `json:"ready"`
	}
	out := []row{}
	for _, st := range stats {
		tv := labels[st.Target]
		out = append(out, row{CopyTargetStats: st, Label: firstNonEmpty(tv.Label, st.Target+" (removed)"), Ready: tv.Ready})
	}
	checked, bad, _ := s.st.PrimaryCheckStats(r.Context())
	cs := s.copies()
	cs.mu.Lock()
	defer cs.mu.Unlock()
	writeJSON(w, 200, map[string]any{
		"targets": out, "primary_checked": checked, "primary_bad": bad,
		"running": cs.running, "last_run": cs.lastRun, "last_plan": cs.lastPlan, "wanted": cs.planned, "last_error": cs.lastError,
		"verify_days": int(copyVerifyEvery / (24 * time.Hour)),
	})
}

func (s *Server) handleCopiesList(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "", "pending", "ok", "failed", "deleting":
	default:
		writeErr(w, r, errf(400, "invalid_state", "Unknown state"))
		return
	}
	list, err := s.st.ListCopies(r.Context(), state, 200)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": list})
}

func (s *Server) handleCopiesRun(w http.ResponseWriter, r *http.Request) {
	s.copies().wake(true)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleCopiesVerify(w http.ResponseWriter, r *http.Request) {
	if err := s.st.VerifyAllCopiesNow(r.Context()); err != nil {
		writeErr(w, r, err)
		return
	}
	s.copies().wake(false)
	s.activity(r, principal(r).User.ID, "copies.verify", "copies", "started a check of every copy and original", true)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleCopiesRetry(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.RetryFailedCopies(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.copies().wake(false)
	writeJSON(w, 200, map[string]any{"retried": n})
}

// handleCopiesRecover brings back a file whose copy is waiting to be
// deleted (the original was deleted): the content is restored and the file
// is put in the owner's /Recovered folder.
func (s *Server) handleCopiesRecover(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserID int64  `json:"user_id"`
		SHA256 string `json:"sha256"`
		Target string `json:"target"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	c, err := s.st.BlobCopy(r.Context(), req.UserID, req.SHA256, req.Target)
	if err != nil || c.State != "deleting" {
		writeErr(w, r, errf(404, "not_found", "No copy waiting for deletion matches"))
		return
	}
	if _, _, err := s.st.BlobRow(r.Context(), c.UserID, c.SHA256); err == nil {
		writeErr(w, r, errf(409, "still_stored", "This file is still stored; it does not need recovering"))
		return
	}
	u, err := s.st.UserByID(r.Context(), c.UserID)
	if err != nil {
		writeErr(w, r, errf(404, "no_user", "The file's owner no longer exists"))
		return
	}
	loc, err := s.userLocation(r, u)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Keep the copy until the recovery is over.
	hold := time.Now().Add(copyRecoverTimeout + time.Hour).UnixMilli()
	if c.DeleteAt == nil || *c.DeleteAt < hold {
		c.DeleteAt = &hold
		_ = s.st.SetCopy(r.Context(), c)
	}
	admin := principal(r).User.ID
	s.activity(r, admin, "copies.recover", c.LastPath, fmt.Sprintf("recovering %s for %s from %s", c.LastPath, u.Username, c.Target), true)
	go func() {
		ctx := context.Background()
		err := s.restoreFromCopies(ctx, c.UserID, c.SHA256, loc, "recover", copyRecoverTimeout)
		if err == nil {
			dir, name := path.Split(strings.TrimPrefix(c.LastPath, "/"))
			segs := append([]string{"Recovered"}, strings.FieldsFunc(dir, func(r rune) bool { return r == '/' })...)
			if name == "" {
				name = c.SHA256
			}
			_, err = s.st.CommitFile(ctx, store.CommitRequest{User: u, DirSegs: segs, Name: name, Size: c.Size, SHA256: c.SHA256, ModifiedAt: time.Now().UnixMilli(), OnConflict: "rename", LocationID: loc})
		}
		details := fmt.Sprintf("recovered %s into /Recovered", c.LastPath)
		if err != nil {
			details = fmt.Sprintf("recovering %s failed: %v", c.LastPath, err)
		}
		s.st.LogActivity(ctx, store.Activity{UserID: &admin, Action: "copies.recovered", Target: c.LastPath, Details: details, Audit: true})
		_ = s.st.AddNotification(ctx, admin, store.Notification{Title: "File recovery", Message: details, Level: map[bool]string{true: "success", false: "error"}[err == nil], Source: "copies"})
		s.copies().wake(true)
	}()
	writeJSON(w, 202, map[string]any{"recovering": true})
}
