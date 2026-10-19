package api

// Storage nodes (vad-node agents): join requests, admin approval, the
// one-time secret, heartbeats and updates served by this server only.
//
//	public:  POST /node/join, GET /node/join/{id} (X-Poll-Token),
//	         GET /node/agent (manifest), GET /node/agent/{os}/{arch} (binary)
//	node:    POST /node/heartbeat (Authorization: Node <id>.<secret>)
//	admin:   GET /admin/nodes, POST /admin/nodes/{id}/approve|reject|revoke|update,
//	         PATCH /admin/nodes/{id}, DELETE /admin/nodes/{id}

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	nodePollInterval      = 5  // seconds between join polls
	nodeHeartbeatInterval = 30 // seconds between heartbeats
)

func (s *Server) mountNodePublic(r chi.Router) {
	r.Post("/node/join", s.handleNodeJoin)
	r.Get("/node/join/{id}", s.handleNodeJoinStatus)
	r.Get("/node/agent", s.handleNodeManifest)
	r.Get("/node/agent/{os}/{arch}", s.handleNodeBinary)
	r.Post("/node/heartbeat", s.handleNodeHeartbeat)
}

func (s *Server) mountNodesAdmin(rt routes) {
	rt.Admin.Get("/admin/nodes", s.handleAdminNodes)
	rt.Admin.Post("/admin/nodes/{id}/approve", s.handleAdminNodeDecision("approved"))
	rt.Admin.Post("/admin/nodes/{id}/reject", s.handleAdminNodeDecision("rejected"))
	rt.Admin.Post("/admin/nodes/{id}/revoke", s.handleAdminNodeRevoke)
	rt.Admin.Post("/admin/nodes/{id}/update", s.handleAdminNodeUpdate)
	rt.Admin.Patch("/admin/nodes/{id}", s.handleAdminNodePatch)
	rt.Admin.Delete("/admin/nodes/{id}", s.handleAdminNodeDelete)
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		s = s[:n]
	}
	return s
}

func (s *Server) handleNodeJoin(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	if !s.enrollLimiter.Allow("node-join:" + ip) {
		writeErr(w, r, errf(429, "rate_limited", "Too many join requests, wait a minute"))
		return
	}
	var req nodeapi.JoinRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	name := clip(req.Name, 64)
	if name == "" {
		writeErr(w, r, errf(400, "invalid_name", "Give the node a name"))
		return
	}
	if req.Port < 0 || req.Port > 65535 {
		writeErr(w, r, errf(400, "invalid_port", "Invalid port"))
		return
	}
	token := auth.RandomToken(24)
	n, err := s.st.CreateStorageNodeRequest(r.Context(), store.StorageNodeJoin{
		Name: name, Hostname: clip(req.Hostname, 128), ReportedIP: clip(req.IP, 64), RequestIP: ip, RequestLocation: s.geo.Describe(ip),
		OS: clip(req.OS, 16), Arch: clip(req.Arch, 16), Port: req.Port, Proxy: nodeapi.ValidProxy(req.Proxy),
		PublicURL: clip(req.PublicURL, 256), Version: clip(req.Version, 64), PollTokenHash: auth.HashToken(token),
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, 0, "node.join_requested", name, fmt.Sprintf("storage node %q asked to join from %s (reported %s)", name, ip, n.ReportedIP), true)
	writeJSON(w, 201, nodeapi.JoinResponse{ID: n.ID, PollToken: token, ExpiresAt: n.ExpiresAt, PollInterval: nodePollInterval})
}

func (s *Server) handleNodeJoinStatus(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	n, err := s.st.StorageNode(r.Context(), id)
	token := r.Header.Get("X-Poll-Token")
	if err != nil || token == "" || n.PollTokenHash() == "" ||
		subtle.ConstantTimeCompare([]byte(auth.HashToken(token)), []byte(n.PollTokenHash())) != 1 {
		writeErr(w, r, errf(404, "not_found", "Join request not found"))
		return
	}
	switch {
	case n.Expired():
		writeJSON(w, 200, nodeapi.JoinStatus{Status: "expired"})
	case n.Status == "approved":
		secret := auth.RandomToken(32)
		ok, err := s.st.DeliverStorageNodeSecret(r.Context(), n.ID, auth.HashToken(secret))
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if !ok { // already handed out (the poll token is cleared then, so this is a race)
			writeErr(w, r, errf(404, "not_found", "Join request not found"))
			return
		}
		writeJSON(w, 200, nodeapi.JoinStatus{Status: "approved", Secret: secret})
	default:
		writeJSON(w, 200, nodeapi.JoinStatus{Status: n.Status})
	}
}

// nodeFromRequest authenticates "Authorization: Node <id>.<secret>".
func (s *Server) nodeFromRequest(r *http.Request) (*store.StorageNode, error) {
	h := r.Header.Get("Authorization")
	cred, ok := strings.CutPrefix(h, nodeapi.AuthScheme+" ")
	if !ok {
		return nil, errf(401, "unauthenticated", "Node credentials required")
	}
	idStr, secret, ok := strings.Cut(cred, ".")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if !ok || err != nil || secret == "" {
		return nil, errf(401, "unauthenticated", "Malformed node credentials")
	}
	n, err := s.st.StorageNodeBySecret(r.Context(), id, auth.HashToken(secret))
	if err != nil {
		return nil, errf(401, "node_revoked", "This node is not approved or was revoked")
	}
	return n, nil
}

func (s *Server) handleNodeHeartbeat(w http.ResponseWriter, r *http.Request) {
	n, err := s.nodeFromRequest(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var hb nodeapi.Heartbeat
	if err := decode(r, &hb); err != nil {
		writeErr(w, r, err)
		return
	}
	if err := s.st.RecordStorageNodeHeartbeat(r.Context(), n.ID, store.StorageNodeHeartbeat{
		Hostname: clip(hb.Hostname, 128), ReportedIP: clip(hb.IP, 64), LastIP: s.clientIP(r), Version: clip(hb.Version, 64),
		PublicURL: clip(hb.PublicURL, 256), LastError: clip(hb.LastError, 500), OS: clip(hb.OS, 16), Arch: clip(hb.Arch, 16), Port: hb.Port,
		DiskTotal: max(0, hb.DiskTotal), DiskFree: max(0, hb.DiskFree), UptimeSeconds: max(0, hb.UptimeSeconds),
		TerminalEnabled: hb.TerminalEnabled, StorageRoots: clip(strings.Join(hb.StorageRoots, "\n"), 2000),
	}, Version); err != nil {
		writeErr(w, r, err)
		return
	}
	s.syncNodeLocations(r.Context(), n.ID, hb.Locations)
	// Jobs claimed before the agent (re)started were lost with the old process
	// (restart, self-update): fail them now so the work is retried at once.
	if hb.UptimeSeconds >= 0 && hb.UptimeSeconds < 24*3600 {
		started := time.Now().Add(-time.Duration(hb.UptimeSeconds)*time.Second - 5*time.Second).UnixMilli()
		if lost, err := s.st.ClaimedNodeJobsBefore(r.Context(), n.ID, started); err == nil {
			for _, j := range lost {
				s.failJob(context.WithoutCancel(r.Context()), j, "the node restarted before finishing the job")
			}
		}
	}
	resp := nodeapi.HeartbeatResponse{PollInterval: nodeHeartbeatInterval}
	if (n.AutoUpdate || n.UpdateRequested) && updatableVersion(Version) && hb.Version != Version {
		if f, ok := s.agentFile(hb.OS, hb.Arch); ok {
			resp.Update = &nodeapi.Update{Version: Version, URL: f.URL, SHA256: f.SHA256, Size: f.Size}
		}
	}
	writeJSON(w, 200, resp)
}

// syncNodeLocations updates the node's locations from its heartbeat. A
// location the node does not know (reinstalled, data wiped) is prepared again.
func (s *Server) syncNodeLocations(ctx context.Context, nodeID int64, reported []nodeapi.LocationStatus) {
	locs, err := s.st.NodeLocations(ctx)
	if err != nil {
		return
	}
	byID := map[int64]nodeapi.LocationStatus{}
	for _, l := range reported {
		byID[l.ID] = l
	}
	for _, l := range locs {
		if l.NodeID != nodeID {
			continue
		}
		rep, ok := byID[l.ID]
		switch {
		case ok && rep.OK:
			_ = s.st.SetNodeLocationStatus(ctx, l.ID, "ok", "", rep.DiskTotal, rep.DiskFree)
		case ok:
			_ = s.st.SetNodeLocationStatus(ctx, l.ID, "error", rep.Error, rep.DiskTotal, rep.DiskFree)
		case l.Status == "ok": // the node lost a working location (reinstalled, data wiped)
			if open, _ := s.st.CountOpenNodeJobs(ctx, nodeID, nodeapi.JobPrepare); open == 0 {
				_ = s.st.SetNodeLocationStatus(ctx, l.ID, "preparing", "", 0, 0)
				_, _ = s.enqueueJob(ctx, nodeID, nodeapi.JobPrepare, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, Path: l.Path}})
			}
		}
	}
}

// updatableVersion: development builds never push updates to nodes.
func updatableVersion(v string) bool { return v != "" && v != "dev" }

// --- agent binaries -------------------------------------------------------------

type agentCache struct {
	mu    sync.Mutex
	files map[string]nodeapi.AgentFile // key os/arch
	mtime map[string]time.Time
}

var agentCaches sync.Map // *Server -> *agentCache

func (s *Server) agentDir() string { return s.cfg.NodeBinaries }

// agentFile describes the agent binary for os/arch, hashing it once per
// modification time.
func (s *Server) agentFile(goos, arch string) (nodeapi.AgentFile, bool) {
	if !nodeapi.ValidPlatform(goos, arch) || s.agentDir() == "" {
		return nodeapi.AgentFile{}, false
	}
	v, _ := agentCaches.LoadOrStore(s, &agentCache{files: map[string]nodeapi.AgentFile{}, mtime: map[string]time.Time{}})
	c := v.(*agentCache)
	p := filepath.Join(s.agentDir(), nodeapi.BinaryName(goos, arch))
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() {
		return nodeapi.AgentFile{}, false
	}
	key := goos + "/" + arch
	c.mu.Lock()
	defer c.mu.Unlock()
	if f, ok := c.files[key]; ok && c.mtime[key].Equal(fi.ModTime()) {
		return f, true
	}
	fh, err := os.Open(p)
	if err != nil {
		return nodeapi.AgentFile{}, false
	}
	defer fh.Close()
	h := sha256.New()
	size, err := io.Copy(h, fh)
	if err != nil {
		return nodeapi.AgentFile{}, false
	}
	f := nodeapi.AgentFile{OS: goos, Arch: arch, SHA256: hex.EncodeToString(h.Sum(nil)), Size: size, URL: "/api/v1/node/agent/" + key}
	c.files[key], c.mtime[key] = f, fi.ModTime()
	return f, true
}

func (s *Server) handleNodeManifest(w http.ResponseWriter, r *http.Request) {
	m := nodeapi.Manifest{Version: Version, Files: []nodeapi.AgentFile{}}
	for _, arch := range []string{"amd64", "arm64"} {
		if f, ok := s.agentFile("linux", arch); ok {
			m.Files = append(m.Files, f)
		}
	}
	writeJSON(w, 200, m)
}

func (s *Server) handleNodeBinary(w http.ResponseWriter, r *http.Request) {
	goos, arch := chi.URLParam(r, "os"), chi.URLParam(r, "arch")
	f, ok := s.agentFile(goos, arch)
	if !ok {
		writeErr(w, r, errf(404, "not_found", "No vad-node agent for %s/%s on this server", goos, arch))
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="vad-node"`)
	w.Header().Set("X-Content-SHA256", f.SHA256)
	w.Header().Set("X-Agent-Version", Version)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.agentDir(), nodeapi.BinaryName(goos, arch)))
}

// --- admin ------------------------------------------------------------------------

type nodeView struct {
	store.StorageNode
	State        string `json:"state"` // status, or "expired" for a lapsed request
	Online       bool   `json:"online"`
	ServerVer    string `json:"server_version"`
	UpdateNeeded bool   `json:"update_available"`
}

func (s *Server) viewNode(n store.StorageNode) nodeView {
	v := nodeView{StorageNode: n, State: n.Status, ServerVer: Version}
	if n.Expired() {
		v.State = "expired"
	}
	if n.LastSeenAt != nil {
		v.Online = time.Since(time.UnixMilli(*n.LastSeenAt)) < 3*nodeHeartbeatInterval*time.Second
	}
	v.UpdateNeeded = n.Status == "approved" && n.Version != "" && updatableVersion(Version) && n.Version != Version
	return v
}

func (s *Server) handleAdminNodes(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.StorageNodes(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]nodeView, 0, len(list))
	for _, n := range list {
		out = append(out, s.viewNode(n))
	}
	writeJSON(w, 200, map[string]any{"items": out, "server_version": Version})
}

func (s *Server) adminNode(w http.ResponseWriter, r *http.Request) *store.StorageNode {
	id, _ := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	n, err := s.st.StorageNode(r.Context(), id)
	if err != nil {
		writeErr(w, r, errf(404, "not_found", "Node not found"))
		return nil
	}
	return n
}

func (s *Server) handleAdminNodeDecision(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := s.adminNode(w, r)
		if n == nil {
			return
		}
		ok, err := s.st.DecideStorageNode(r.Context(), n.ID, status, principal(r).User.ID)
		if err != nil {
			writeErr(w, r, err)
			return
		}
		if !ok {
			writeErr(w, r, errf(409, "not_pending", "This request is no longer pending (it expired or was already decided). Run the node installer again for a new request."))
			return
		}
		verb := "approved"
		if status == "rejected" {
			verb = "rejected"
		}
		s.activity(r, principal(r).User.ID, "node."+verb, n.Name, fmt.Sprintf("%s storage node %q (%s)", verb, n.Name, n.RequestIP), true)
		if status == "approved" {
			// The node's copy key is created with the approval (node_keys.go).
			if _, err := s.nodeCopyKey(r.Context(), n.ID); err != nil {
				writeErr(w, r, err)
				return
			}
		}
		n, _ = s.st.StorageNode(r.Context(), n.ID)
		writeJSON(w, 200, s.viewNode(*n))
	}
}

func (s *Server) handleAdminNodeRevoke(w http.ResponseWriter, r *http.Request) {
	n := s.adminNode(w, r)
	if n == nil {
		return
	}
	if err := s.st.RevokeStorageNode(r.Context(), n.ID, principal(r).User.ID); err != nil {
		writeErr(w, r, errf(409, "not_approved", "Only approved nodes can be revoked"))
		return
	}
	s.activity(r, principal(r).User.ID, "node.revoked", n.Name, fmt.Sprintf("revoked storage node %q", n.Name), true)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAdminNodeUpdate(w http.ResponseWriter, r *http.Request) {
	n := s.adminNode(w, r)
	if n == nil {
		return
	}
	if !updatableVersion(Version) {
		writeErr(w, r, errf(409, "no_release", "This server is a development build and does not update nodes"))
		return
	}
	if _, ok := s.agentFile(n.OS, n.Arch); !ok {
		writeErr(w, r, errf(409, "no_agent", "This server has no vad-node agent for %s/%s", n.OS, n.Arch))
		return
	}
	if err := s.st.RequestStorageNodeUpdate(r.Context(), n.ID); err != nil {
		writeErr(w, r, errf(409, "not_approved", "Only approved nodes can be updated"))
		return
	}
	s.activity(r, principal(r).User.ID, "node.update_requested", n.Name, fmt.Sprintf("requested an update of storage node %q to %s", n.Name, Version), true)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Server) handleAdminNodePatch(w http.ResponseWriter, r *http.Request) {
	n := s.adminNode(w, r)
	if n == nil {
		return
	}
	var req struct {
		Name       *string `json:"name"`
		AutoUpdate *bool   `json:"auto_update"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if req.Name != nil {
		v := clip(*req.Name, 64)
		if v == "" {
			writeErr(w, r, errf(400, "invalid_name", "Name must not be empty"))
			return
		}
		req.Name = &v
	}
	if err := s.st.PatchStorageNode(r.Context(), n.ID, req.Name, req.AutoUpdate); err != nil {
		writeErr(w, r, err)
		return
	}
	n, _ = s.st.StorageNode(r.Context(), n.ID)
	writeJSON(w, 200, s.viewNode(*n))
}

func (s *Server) handleAdminNodeDelete(w http.ResponseWriter, r *http.Request) {
	n := s.adminNode(w, r)
	if n == nil {
		return
	}
	if err := s.st.DeleteStorageNode(r.Context(), n.ID); err != nil {
		if errors.Is(err, store.ErrStorageNodeNotFound) {
			writeErr(w, r, errf(409, "approved", "Revoke the node before deleting it"))
			return
		}
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "node.deleted", n.Name, fmt.Sprintf("deleted storage node %q", n.Name), true)
	w.WriteHeader(http.StatusNoContent)
}
