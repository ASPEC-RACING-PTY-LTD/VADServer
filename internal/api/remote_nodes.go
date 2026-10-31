package api

// Remote storage nodes: an administrator pairs a node at another site
// (single-use pairing secret, entered in the node installer) and chooses
// which users' files it holds. Files moved there are encrypted for the
// node's public key (remote_keys.go); reads are relayed by this server,
// which fetches only the chunks it needs and decrypts them in memory
// (remote_relay.go). The node's owner gets no account and no settings.

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	pairingPrefix   = "vdp_"
	installerScript = "https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh"
)

func (s *Server) mountRemoteNodes(rt routes) {
	rt.Admin.Get("/admin/node-pairings", s.handleNodePairings)
	rt.Admin.Post("/admin/node-pairings", s.handleNodePairingCreate)
	rt.Admin.Delete("/admin/node-pairings/{id}", s.handleNodePairingDelete)
	rt.Admin.Get("/admin/nodes/{id}/remote", s.handleNodeRemote)
	rt.Admin.Put("/admin/users/{id}/upload-origin", s.handleUploadOriginSet)
	rt.Admin.Post("/admin/users/{id}/move-to-node", s.handleMoveToNode)
}

// publicBase is this server's external URL (for install commands).
func (s *Server) publicBase(r *http.Request) string {
	if s.cfg.PublicURL != "" {
		return s.cfg.PublicURL
	}
	scheme := "http"
	if s.isHTTPS(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) handleNodePairings(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.NodePairings(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, map[string]any{"pairings": list})
}

func (s *Server) handleNodePairingCreate(w http.ResponseWriter, r *http.Request) {
	admin := principal(r).User
	var req struct {
		Name      string `json:"name"`
		PublicURL string `json:"public_url"`
		Hours     int    `json:"hours"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	req.Name = clip(req.Name, 64)
	if req.Name == "" {
		writeErr(w, r, errf(400, "invalid_name", "Give the node a name"))
		return
	}
	pub := strings.TrimRight(strings.TrimSpace(req.PublicURL), "/")
	if pub != "" {
		u, err := url.Parse(pub)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" {
			writeErr(w, r, errf(400, "invalid_url", "The node's address must be an https:// URL with no path, for example https://storage.example.com"))
			return
		}
	}
	if req.Hours == 0 {
		req.Hours = 24
	}
	if req.Hours < 1 || req.Hours > 168 {
		writeErr(w, r, errf(400, "invalid_expiry", "A pairing secret can last 1-168 hours"))
		return
	}
	secret := pairingPrefix + auth.RandomToken(24)
	p, err := s.st.CreateNodePairing(r.Context(), store.NodePairing{Name: req.Name, PublicURL: pub, CreatedBy: &admin.ID,
		ExpiresAt: time.Now().Add(time.Duration(req.Hours) * time.Hour).UnixMilli()}, auth.HashToken(secret))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, admin.ID, "node.pairing_created", req.Name, fmt.Sprintf("pairing secret for a remote node, valid %d hours", req.Hours), true)
	base := s.publicBase(r)
	// The secret is shown once and is not part of the command (it would end
	// up in shell history); the installer asks for it with hidden input.
	writeJSON(w, 201, map[string]any{
		"pairing": p, "secret": secret, "server_url": base,
		"command": fmt.Sprintf("curl -fsSL %s | sudo VAD_NODE_SERVER_URL=%s bash -s install-node", installerScript, base),
	})
}

func (s *Server) handleNodePairingDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.st.DeleteNodePairing(r.Context(), pathID(r)); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(204)
}

// pairNode approves a node that joined with a valid pairing secret and
// creates its keys. A secret that is no longer usable leaves the request
// pending for an administrator.
func (s *Server) pairNode(ctx context.Context, n *store.StorageNode, secret string) error {
	p, err := s.st.UseNodePairing(ctx, auth.HashToken(secret), n.ID)
	if err != nil {
		return err
	}
	var by int64
	if p.CreatedBy != nil {
		by = *p.CreatedBy
	}
	if _, err := s.st.DecideStorageNode(ctx, n.ID, "approved", by); err != nil {
		return err
	}
	if n.PublicURL == "" && p.PublicURL != "" {
		_, _ = s.st.DB.ExecContext(ctx, "UPDATE storage_nodes SET public_url = ? WHERE id = ?", p.PublicURL, n.ID)
	}
	if _, err := s.nodeCopyKey(ctx, n.ID); err != nil {
		return err
	}
	_, err = s.nodeOriginKey(ctx, n.ID)
	return err
}

func (s *Server) handleNodeRemote(w http.ResponseWriter, r *http.Request) {
	n := s.adminNode(w, r)
	if n == nil {
		return
	}
	stats, err := s.st.RemoteStatsOfNode(r.Context(), n.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	ids, err := s.st.UsersOfNode(r.Context(), n.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	users := []map[string]any{}
	for _, id := range ids {
		if u, err := s.st.UserByID(r.Context(), id); err == nil {
			users = append(users, map[string]any{"id": u.ID, "username": u.Username, "display_name": u.DisplayName})
		}
	}
	keyID := ""
	if k, err := s.st.NodeOriginKey(r.Context(), n.ID); err == nil {
		keyID = k.KeyID
	}
	ready, why := s.remoteReady(r.Context(), n)
	writeJSON(w, 200, map[string]any{"stats": stats, "users": users, "key_id": keyID, "ready": ready, "reason": why, "moving": s.remote().moving(n.ID)})
}

// remoteReady says whether a node can hold files: approved, recently seen,
// with an HTTPS address and a prepared storage location.
func (s *Server) remoteReady(ctx context.Context, n *store.StorageNode) (bool, string) {
	switch {
	case n.Status != "approved":
		return false, "the node is not approved"
	case n.LastSeenAt == nil || time.Since(time.UnixMilli(*n.LastSeenAt)) > 3*nodeHeartbeatInterval*time.Second:
		return false, "the node is offline"
	case !strings.HasPrefix(n.PublicURL, "https://"):
		return false, "the node has no https:// address (set it on the node or in its settings)"
	}
	if _, err := s.nodeStorageLocation(ctx, n.ID); err != nil {
		return false, err.Error()
	}
	return true, ""
}

// nodeStorageLocation is the node location remote files are stored in: the
// first prepared location of the node.
func (s *Server) nodeStorageLocation(ctx context.Context, nodeID int64) (*store.NodeLocation, error) {
	locs, err := s.st.NodeLocations(ctx)
	if err != nil {
		return nil, err
	}
	for i := range locs {
		if locs[i].NodeID == nodeID && locs[i].Status == "ok" {
			return &locs[i], nil
		}
	}
	return nil, errf(409, "no_location", "The node has no prepared storage folder yet")
}

func (s *Server) handleUploadOriginSet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NodeID *int64 `json:"node_id"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	u, err := s.st.UserByID(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	target := "this server"
	if req.NodeID != nil {
		n, err := s.st.StorageNode(r.Context(), *req.NodeID)
		if err != nil || n.Status != "approved" {
			writeErr(w, r, errf(404, "not_found", "Node not found or not approved"))
			return
		}
		target = "storage node " + n.Name
	}
	if err := s.st.SetUploadOrigin(r.Context(), u.ID, req.NodeID); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "user.upload_origin", u.Username, "files of this user are kept on "+target, true)
	writeJSON(w, 200, map[string]any{"user_id": u.ID, "node_id": req.NodeID})
}

func (s *Server) handleMoveToNode(w http.ResponseWriter, r *http.Request) {
	u, err := s.st.UserByID(r.Context(), pathID(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	origin, err := s.st.UploadOrigin(r.Context(), u.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if origin == nil {
		writeErr(w, r, errf(409, "no_origin", "Choose the storage node for this user first"))
		return
	}
	n, err := s.st.StorageNode(r.Context(), *origin)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if ok, why := s.remoteReady(r.Context(), n); !ok {
		writeErr(w, r, errf(409, "node_not_ready", "The node cannot take files: %s", why))
		return
	}
	started := s.remote().startMove(s, u.ID, n.ID)
	s.activity(r, principal(r).User.ID, "user.move_to_node", u.Username, "moving existing files to storage node "+n.Name, true)
	writeJSON(w, 202, map[string]any{"started": started})
}

// autoNodeLocation gives a paired node a storage location in the folder its
// installer was given (its first storage root), so pairing needs nothing
// more from the administrator.
func (s *Server) autoNodeLocation(ctx context.Context, n *store.StorageNode, roots []string) {
	if len(roots) == 0 || !s.st.StorageNodePaired(ctx, n.ID) {
		return
	}
	locs, err := s.st.NodeLocations(ctx)
	if err != nil {
		return
	}
	for _, l := range locs {
		if l.NodeID == n.ID {
			return
		}
	}
	l, err := s.st.CreateNodeLocation(ctx, n.ID, fmt.Sprintf("%s (node %d)", n.Name, n.ID), roots[0])
	if err != nil {
		return
	}
	_, _ = s.enqueueJob(ctx, n.ID, nodeapi.JobPrepare, jobMeta{JobPayload: nodeapi.JobPayload{LocationID: l.ID, Path: l.Path}})
}
