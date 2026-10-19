package api

// Work for storage nodes. Nodes pull it, so they need no inbound access
// (they sit behind NAT or a tunnel at someone's home):
//
//	GET  /node/jobs/next             long poll (up to 25 s) for the next job
//	GET  /node/jobs/{id}/data        put: the encrypted copy to store
//	PUT  /node/jobs/{id}/data        get: the node sends its stored copy back
//	POST /node/jobs/{id}/result      the outcome of any job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/replica"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// jobMeta is the stored payload of a job: what the node sees plus what the
// server needs to apply the result.
type jobMeta struct {
	nodeapi.JobPayload
	Target     string `json:"target,omitempty"`      // copy target ("node:<id>")
	Purpose    string `json:"purpose,omitempty"`     // get: repair, recover
	PrimaryLoc int64  `json:"primary_loc,omitempty"` // get: where the content goes
}

// jobState is what the server learns while serving a job's data.
type jobState struct {
	encSHA   string // SHA-256 of the encrypted stream sent (put)
	plainSHA string // SHA-256 of the plaintext read (put)
	received bool   // get: the node sent its copy
	restored error  // get: outcome of writing the returned content
	done     chan struct{}
}

type nodeJobHub struct {
	mu     sync.Mutex
	wake   map[int64]chan struct{} // node id -> signal
	states map[int64]*jobState     // job id -> state
}

var nodeHubs sync.Map // *Server -> *nodeJobHub

func (s *Server) jobHub() *nodeJobHub {
	v, _ := nodeHubs.LoadOrStore(s, &nodeJobHub{wake: map[int64]chan struct{}{}, states: map[int64]*jobState{}})
	return v.(*nodeJobHub)
}

func (h *nodeJobHub) signal(nodeID int64) chan struct{} {
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.wake[nodeID]
	if !ok {
		c = make(chan struct{}, 1)
		h.wake[nodeID] = c
	}
	return c
}

func (h *nodeJobHub) notify(nodeID int64) {
	select {
	case h.signal(nodeID) <- struct{}{}:
	default:
	}
}

func (h *nodeJobHub) state(jobID int64) *jobState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.states[jobID]
	if !ok {
		st = &jobState{done: make(chan struct{})}
		h.states[jobID] = st
	}
	return st
}

func (h *nodeJobHub) drop(jobID int64) {
	h.mu.Lock()
	delete(h.states, jobID)
	h.mu.Unlock()
}

// enqueueJob stores a job for a node and wakes its long poll.
func (s *Server) enqueueJob(ctx context.Context, nodeID int64, kind string, meta jobMeta) (int64, error) {
	b, _ := json.Marshal(meta)
	id, err := s.st.EnqueueNodeJob(ctx, nodeID, kind, string(b))
	if err == nil {
		s.jobHub().notify(nodeID)
	}
	return id, err
}

func (s *Server) mountNodeJobs(r chi.Router) {
	r.Get("/node/jobs/next", s.handleNodeJobNext)
	r.Get("/node/jobs/{id}/data", s.handleNodeJobDataGet)
	r.Put("/node/jobs/{id}/data", s.handleNodeJobDataPut)
	r.Post("/node/jobs/{id}/result", s.handleNodeJobResult)
	r.Get("/node/terminal/{sid}/input", s.handleNodeTerminalInput)
	r.Post("/node/terminal/{sid}/output", s.handleNodeTerminalOutput)
}

func (s *Server) handleNodeJobNext(w http.ResponseWriter, r *http.Request) {
	n, err := s.nodeFromRequest(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	wake := s.jobHub().signal(n.ID)
	deadline := time.NewTimer(25 * time.Second)
	defer deadline.Stop()
	for {
		j, err := s.st.ClaimNodeJob(r.Context(), n.ID)
		if err == nil {
			var meta jobMeta
			_ = json.Unmarshal([]byte(j.Payload), &meta)
			writeJSON(w, 200, nodeapi.Job{ID: j.ID, Kind: j.Kind, Payload: meta.JobPayload})
			return
		}
		if !errors.Is(err, store.ErrNotFound) {
			writeErr(w, r, err)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-wake:
		}
	}
}

// nodeJob loads a job of the authenticated node.
func (s *Server) nodeJob(w http.ResponseWriter, r *http.Request) (*store.StorageNode, *store.NodeJob, jobMeta) {
	var meta jobMeta
	n, err := s.nodeFromRequest(r)
	if err != nil {
		writeErr(w, r, err)
		return nil, nil, meta
	}
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	j, err := s.st.NodeJob(r.Context(), id)
	if err != nil || j.NodeID != n.ID {
		writeErr(w, r, errf(404, "not_found", "Job not found"))
		return nil, nil, meta
	}
	_ = json.Unmarshal([]byte(j.Payload), &meta)
	return n, j, meta
}

func (s *Server) copyKey() ([]byte, error) {
	k, err := s.Keys().Derive("node-copies")
	if err != nil {
		return nil, errors.New("the server key (KEK) is not available, so copies cannot be encrypted for nodes")
	}
	return k, nil
}

// handleNodeJobDataGet streams the encrypted blob of a put job.
func (s *Server) handleNodeJobDataGet(w http.ResponseWriter, r *http.Request) {
	_, j, meta := s.nodeJob(w, r)
	if j == nil {
		return
	}
	if j.Kind != nodeapi.JobPut || j.Status != "claimed" {
		writeErr(w, r, errf(409, "not_put", "This job has no data to download"))
		return
	}
	key, err := s.copyKey()
	if err != nil {
		writeErr(w, r, errf(503, "no_key", "%s", err.Error()))
		return
	}
	_, loc, err := s.st.BlobRow(r.Context(), meta.UserID, meta.SHA256)
	if err != nil {
		writeErr(w, r, errf(404, "gone", "The file is no longer stored"))
		return
	}
	f, err := s.blobs.Open(loc, meta.UserID, meta.SHA256)
	if err != nil {
		writeErr(w, r, errf(404, "gone", "The stored file cannot be read: %v", err))
		return
	}
	defer f.Close()
	st := s.jobHub().state(j.ID)
	plain, enc := sha256.New(), sha256.New()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(200)
	if err := replica.Encrypt(io.MultiWriter(w, enc), io.TeeReader(f, plain), key, replica.BlobID(meta.UserID, meta.SHA256)); err != nil {
		return
	}
	s.jobHub().mu.Lock()
	st.encSHA, st.plainSHA = hex.EncodeToString(enc.Sum(nil)), hex.EncodeToString(plain.Sum(nil))
	s.jobHub().mu.Unlock()
}

// handleNodeJobDataPut receives a node's stored copy (get job), decrypts it
// and restores the primary file from it.
func (s *Server) handleNodeJobDataPut(w http.ResponseWriter, r *http.Request) {
	_, j, meta := s.nodeJob(w, r)
	if j == nil {
		return
	}
	if j.Kind != nodeapi.JobGet || j.Status != "claimed" {
		writeErr(w, r, errf(409, "not_get", "This job does not take data"))
		return
	}
	key, err := s.copyKey()
	if err != nil {
		writeErr(w, r, errf(503, "no_key", "%s", err.Error()))
		return
	}
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(replica.Decrypt(pw, r.Body, key, replica.BlobID(meta.UserID, meta.SHA256)))
	}()
	err = s.blobs.RestorePrimary(meta.PrimaryLoc, meta.UserID, meta.SHA256, pr)
	_ = pr.CloseWithError(err)
	st := s.jobHub().state(j.ID)
	s.jobHub().mu.Lock()
	st.received, st.restored = true, err
	s.jobHub().mu.Unlock()
	if err != nil {
		writeErr(w, r, errf(422, "restore_failed", "The copy could not be restored: %v", err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNodeJobResult(w http.ResponseWriter, r *http.Request) {
	_, j, meta := s.nodeJob(w, r)
	if j == nil {
		return
	}
	var res nodeapi.JobResult
	if err := decode(r, &res); err != nil {
		writeErr(w, r, err)
		return
	}
	b, _ := json.Marshal(res)
	if ok, err := s.st.FinishNodeJob(r.Context(), j.ID, res.OK, string(b), clip(res.Error, 500)); err != nil || !ok {
		w.WriteHeader(http.StatusNoContent) // already finished (stale or duplicate)
		return
	}
	s.applyJobResult(context.WithoutCancel(r.Context()), j, meta, res)
	w.WriteHeader(http.StatusNoContent)
}

// failJob ends a job the node never finished (stale or the node is gone).
func (s *Server) failJob(ctx context.Context, j store.NodeJob, msg string) {
	if ok, _ := s.st.FinishNodeJob(ctx, j.ID, false, "{}", msg); !ok {
		return
	}
	var meta jobMeta
	_ = json.Unmarshal([]byte(j.Payload), &meta)
	s.applyJobResult(ctx, &j, meta, nodeapi.JobResult{OK: false, Error: msg})
}

// applyJobResult updates copies and locations after a job.
func (s *Server) applyJobResult(ctx context.Context, j *store.NodeJob, meta jobMeta, res nodeapi.JobResult) {
	hub := s.jobHub()
	st := hub.state(j.ID)
	defer func() {
		select {
		case <-st.done:
		default:
			close(st.done)
		}
		if j.Kind != nodeapi.JobGet { // get waiters read the state, then drop it
			hub.drop(j.ID)
		}
	}()
	switch j.Kind {
	case nodeapi.JobPrepare:
		status, msg := "ok", ""
		if !res.OK {
			status, msg = "error", res.Error
		}
		_ = s.st.SetNodeLocationStatus(ctx, meta.LocationID, status, msg, res.DiskTotal, res.DiskFree)
	case nodeapi.JobPut:
		c, err := s.st.BlobCopy(ctx, meta.UserID, meta.SHA256, meta.Target)
		if err != nil {
			return
		}
		hub.mu.Lock()
		enc, plain := st.encSHA, st.plainSHA
		hub.mu.Unlock()
		switch {
		case !res.OK:
			s.copyFailed(ctx, c, res.Error)
		case plain != meta.SHA256:
			s.copyFailed(ctx, c, "the original file is damaged (its checksum does not match); it is repaired from another copy if one exists")
			_ = s.st.RecordPrimaryCheck(ctx, meta.UserID, meta.SHA256, false, "checksum mismatch while copying")
		case enc == "" || res.SHA256 != enc:
			s.copyFailed(ctx, c, "the node stored different data than was sent")
		default:
			t := time.Now().UnixMilli()
			c.State, c.StoredSHA256, c.Attempts, c.NextAt, c.LastError, c.VerifiedAt, c.DeleteAt = "ok", enc, 0, 0, "", &t, nil
			_ = s.st.SetCopy(ctx, c)
		}
	case nodeapi.JobVerify:
		c, err := s.st.BlobCopy(ctx, meta.UserID, meta.SHA256, meta.Target)
		if err != nil || c.State != "ok" {
			return
		}
		t := time.Now().UnixMilli()
		switch {
		case res.OK && res.SHA256 == c.StoredSHA256:
			c.VerifiedAt, c.NextAt, c.LastError = &t, 0, ""
		case res.OK || res.Error != "":
			// Missing or damaged on the node: copy it again.
			c.State, c.NextAt, c.LastError = "pending", 0, fmt.Sprintf("the copy on the node failed its check (%s); copying again", firstNonEmpty(res.Error, "checksum mismatch"))
		}
		_ = s.st.SetCopy(ctx, c)
	case nodeapi.JobDelete:
		if res.OK {
			_ = s.st.DeleteCopyRow(ctx, meta.UserID, meta.SHA256, meta.Target)
		} else if c, err := s.st.BlobCopy(ctx, meta.UserID, meta.SHA256, meta.Target); err == nil {
			c.NextAt, c.LastError = time.Now().Add(time.Hour).UnixMilli(), "delete failed: "+res.Error
			_ = s.st.SetCopy(ctx, c)
		}
	case nodeapi.JobTerminal:
		if ts := s.terms().get(meta.Session); ts != nil && !res.OK {
			s.closeTerm(ts, firstNonEmpty(res.Error, "the node could not open a terminal"))
		}
	case nodeapi.JobGet:
		hub.mu.Lock()
		if (!res.OK || !st.received) && st.restored == nil {
			st.restored = errors.New(firstNonEmpty(res.Error, "the node could not send the copy"))
		}
		hub.mu.Unlock()
	}
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if s != "" {
			return s
		}
	}
	return ""
}

// waitJob waits for a job to finish and returns the restore outcome of a
// get job (nil on success).
func (s *Server) waitJob(ctx context.Context, jobID int64, timeout time.Duration) error {
	hub := s.jobHub()
	st := hub.state(jobID)
	defer hub.drop(jobID)
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-st.done:
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return errors.New("the node did not answer in time")
	}
	hub.mu.Lock()
	defer hub.mu.Unlock()
	return st.restored
}
