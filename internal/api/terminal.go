package api

// Browser terminals into storage nodes. Only superusers may open one, they
// re-enter their password, and only on nodes whose owner allowed it on the
// node itself (VAD_NODE_TERMINAL=on; the server cannot switch it on). Opening
// and closing are audited. Everything runs over short HTTP requests (no
// WebSocket), so it works through tunnels and proxies:
//
//	browser: POST /admin/nodes/{id}/terminal, GET /admin/terminal/{sid}/output?after=N (long poll),
//	         POST /admin/terminal/{sid}/input, DELETE /admin/terminal/{sid}
//	node:    a "terminal" job, then GET /node/terminal/{sid}/input (long poll) and
//	         POST /node/terminal/{sid}/output

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	termIdle       = 15 * time.Minute // no input from the browser
	termNodeStart  = 2 * time.Minute  // the node must pick the session up
	termMaxLife    = 8 * time.Hour
	termOutputKeep = 1 << 20 // bytes of output kept for the browser
	termPoll       = 20 * time.Second
)

type termChunk struct {
	seq  int64
	data []byte
}

type termSession struct {
	id       string
	nodeID   int64
	nodeName string
	userID   int64
	opened   time.Time

	mu         sync.Mutex
	wake       chan struct{} // output or close (browser side)
	inWake     chan struct{} // input (node side)
	out        []termChunk
	outBytes   int
	seq        int64
	in         []byte
	cols, rows int
	resized    bool
	closeReq   bool
	closed     bool
	reason     string
	lastInput  time.Time
	nodeSeen   bool
}

type termHub struct {
	mu       sync.Mutex
	sessions map[string]*termSession
}

var termHubs sync.Map // *Server -> *termHub

func (s *Server) terms() *termHub {
	v, loaded := termHubs.LoadOrStore(s, &termHub{sessions: map[string]*termSession{}})
	h := v.(*termHub)
	if !loaded {
		go s.reapTerminals(h)
	}
	return h
}

func (h *termHub) get(id string) *termSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func signal(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// close ends a session once and records why.
func (s *Server) closeTerm(t *termSession, reason string) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed, t.closeReq, t.reason = true, true, reason
	t.mu.Unlock()
	signal(t.wake)
	signal(t.inWake)
	uid := t.userID
	s.st.LogActivity(context.Background(), store.Activity{UserID: &uid, ActorID: &uid, Action: "node.terminal_closed", Target: t.nodeName,
		Details: fmt.Sprintf("terminal on %q closed after %s: %s", t.nodeName, time.Since(t.opened).Round(time.Second), reason), Audit: true})
	go func() { // keep it a moment so both sides see the close
		time.Sleep(time.Minute)
		h := s.terms()
		h.mu.Lock()
		delete(h.sessions, t.id)
		h.mu.Unlock()
	}()
}

func (s *Server) reapTerminals(h *termHub) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		h.mu.Lock()
		list := make([]*termSession, 0, len(h.sessions))
		for _, ts := range h.sessions {
			list = append(list, ts)
		}
		h.mu.Unlock()
		for _, ts := range list {
			ts.mu.Lock()
			idle, seen, closed := time.Since(ts.lastInput), ts.nodeSeen, ts.closed
			ts.mu.Unlock()
			switch {
			case closed:
			case !seen && time.Since(ts.opened) > termNodeStart:
				s.closeTerm(ts, "the node did not open the terminal (offline, or terminal access is off on the node)")
			case idle > termIdle:
				s.closeTerm(ts, "idle for 15 minutes")
			case time.Since(ts.opened) > termMaxLife:
				s.closeTerm(ts, "the 8-hour session limit was reached")
			}
		}
	}
}

func (s *Server) mountTerminal(rt routes) {
	rt.Super.Post("/admin/nodes/{id}/terminal", s.handleTerminalOpen)
	rt.Super.Get("/admin/terminal/{sid}/output", s.handleTerminalOutput)
	rt.Super.Post("/admin/terminal/{sid}/input", s.handleTerminalInput)
	rt.Super.Delete("/admin/terminal/{sid}", s.handleTerminalClose)
}

func (s *Server) handleTerminalOpen(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Password string `json:"password"`
		Cols     int    `json:"cols"`
		Rows     int    `json:"rows"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	u := principal(r).User
	full, err := s.st.UserByID(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if ok, _ := auth.VerifyPassword(r.Context(), req.Password, full.PasswordHash); !ok {
		s.activity(r, u.ID, "node.terminal_denied", "", "terminal refused: wrong password", true)
		writeErr(w, r, errf(403, "bad_password", "Password is incorrect"))
		return
	}
	n, err := s.st.StorageNode(r.Context(), pathID(r))
	if err != nil || n.Status != "approved" {
		writeErr(w, r, errf(404, "not_found", "Node not found"))
		return
	}
	if !n.TerminalEnabled {
		writeErr(w, r, errf(409, "terminal_off", "The node's owner has not allowed terminal access (VAD_NODE_TERMINAL=on on the node)"))
		return
	}
	ts := &termSession{id: auth.RandomToken(18), nodeID: n.ID, nodeName: n.Name, userID: u.ID, opened: time.Now(), lastInput: time.Now(),
		wake: make(chan struct{}, 1), inWake: make(chan struct{}, 1), cols: max(20, min(req.Cols, 500)), rows: max(5, min(req.Rows, 200))}
	h := s.terms()
	h.mu.Lock()
	h.sessions[ts.id] = ts
	h.mu.Unlock()
	if _, err := s.enqueueJob(r.Context(), n.ID, nodeapi.JobTerminal, jobMeta{JobPayload: nodeapi.JobPayload{Session: ts.id, Cols: ts.cols, Rows: ts.rows}}); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, u.ID, "node.terminal_opened", n.Name, fmt.Sprintf("opened a terminal on storage node %q", n.Name), true)
	writeJSON(w, 201, map[string]any{"session": ts.id})
}

// browserSession loads a session of the signed-in superuser.
func (s *Server) browserSession(w http.ResponseWriter, r *http.Request) *termSession {
	ts := s.terms().get(chi.URLParam(r, "sid"))
	if ts == nil || ts.userID != principal(r).User.ID {
		writeErr(w, r, errf(404, "not_found", "Terminal session not found"))
		return nil
	}
	return ts
}

func (s *Server) handleTerminalOutput(w http.ResponseWriter, r *http.Request) {
	ts := s.browserSession(w, r)
	if ts == nil {
		return
	}
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	deadline := time.NewTimer(termPoll)
	defer deadline.Stop()
	for {
		ts.mu.Lock()
		var data []byte
		for _, c := range ts.out {
			if c.seq > after {
				data = append(data, c.data...)
			}
		}
		seq, closed, reason := ts.seq, ts.closed, ts.reason
		ts.mu.Unlock()
		if len(data) > 0 || closed {
			writeJSON(w, 200, map[string]any{"seq": seq, "data": data, "closed": closed, "reason": reason})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			writeJSON(w, 200, map[string]any{"seq": seq, "data": []byte{}, "closed": false})
			return
		case <-ts.wake:
		}
	}
}

func (s *Server) handleTerminalInput(w http.ResponseWriter, r *http.Request) {
	ts := s.browserSession(w, r)
	if ts == nil {
		return
	}
	var req struct {
		Data []byte `json:"data"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	ts.mu.Lock()
	if ts.closed {
		ts.mu.Unlock()
		writeErr(w, r, errf(410, "closed", "The terminal is closed"))
		return
	}
	if len(ts.in)+len(req.Data) > 1<<20 {
		ts.mu.Unlock()
		writeErr(w, r, errf(429, "busy", "Too much input queued"))
		return
	}
	ts.in = append(ts.in, req.Data...)
	if req.Cols > 0 && req.Rows > 0 && (req.Cols != ts.cols || req.Rows != ts.rows) {
		ts.cols, ts.rows, ts.resized = max(20, min(req.Cols, 500)), max(5, min(req.Rows, 200)), true
	}
	ts.lastInput = time.Now()
	ts.mu.Unlock()
	signal(ts.inWake)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTerminalClose(w http.ResponseWriter, r *http.Request) {
	ts := s.browserSession(w, r)
	if ts == nil {
		return
	}
	s.closeTerm(ts, "closed by the user")
	w.WriteHeader(http.StatusNoContent)
}

// nodeSession loads a session of the authenticated node.
func (s *Server) nodeSession(w http.ResponseWriter, r *http.Request) *termSession {
	n, err := s.nodeFromRequest(r)
	if err != nil {
		writeErr(w, r, err)
		return nil
	}
	ts := s.terms().get(chi.URLParam(r, "sid"))
	if ts == nil || ts.nodeID != n.ID {
		writeErr(w, r, errf(404, "not_found", "Terminal session not found"))
		return nil
	}
	ts.mu.Lock()
	ts.nodeSeen = true
	ts.mu.Unlock()
	return ts
}

func (s *Server) handleNodeTerminalInput(w http.ResponseWriter, r *http.Request) {
	ts := s.nodeSession(w, r)
	if ts == nil {
		return
	}
	deadline := time.NewTimer(termPoll)
	defer deadline.Stop()
	for {
		ts.mu.Lock()
		in := nodeapi.TerminalInput{Data: ts.in, Close: ts.closeReq}
		if ts.resized {
			in.Cols, in.Rows = ts.cols, ts.rows
		}
		has := len(ts.in) > 0 || ts.resized || ts.closeReq
		if has {
			ts.in, ts.resized = nil, false
		}
		ts.mu.Unlock()
		if has {
			writeJSON(w, 200, in)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-ts.inWake:
		}
	}
}

func (s *Server) handleNodeTerminalOutput(w http.ResponseWriter, r *http.Request) {
	ts := s.nodeSession(w, r)
	if ts == nil {
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(data) > 0 {
		ts.mu.Lock()
		ts.seq++
		ts.out = append(ts.out, termChunk{seq: ts.seq, data: data})
		ts.outBytes += len(data)
		for ts.outBytes > termOutputKeep && len(ts.out) > 1 {
			ts.outBytes -= len(ts.out[0].data)
			ts.out = ts.out[1:]
		}
		ts.mu.Unlock()
		signal(ts.wake)
	}
	if exit := r.Header.Get("X-Terminal-Exit"); exit != "" {
		s.closeTerm(ts, exit)
	}
	w.WriteHeader(http.StatusNoContent)
}
