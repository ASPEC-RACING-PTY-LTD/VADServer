package api

// Background work for remote storage nodes: users assigned to a node have
// their files moved there (new uploads included), remote files are
// re-checked on the node every week, and remote files of deleted content
// are removed from the node.

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const (
	remoteCycle       = 10 * time.Minute
	remoteVerifyEvery = 7 * 24 * time.Hour
)

// RunRemote runs the remote node service until ctx ends.
func (s *Server) RunRemote(ctx context.Context) {
	s.blobs.SetRemoveHook(func(ref store.BlobRef) { s.remoteRemoved(context.WithoutCancel(ctx), ref) })
	t := time.NewTicker(remoteCycle)
	defer t.Stop()
	for {
		s.remoteCycle(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) remoteCycle(ctx context.Context) {
	nodes, err := s.st.StorageNodes(ctx)
	if err != nil {
		return
	}
	for i := range nodes {
		n := &nodes[i]
		if ok, _ := s.remoteReady(ctx, n); !ok {
			continue
		}
		users, err := s.st.UsersOfNode(ctx, n.ID)
		if err != nil {
			continue
		}
		// New uploads of assigned users land here first; move them on.
		for _, u := range users {
			if refs, _, err := s.st.BlobsToMove(ctx, u, moveAttempts, 1); err == nil && len(refs) > 0 {
				s.remote().startMove(s, u, n.ID)
			}
		}
		s.remoteVerifyDue(ctx, n.ID)
		s.remoteRetryDeletes(ctx, n.ID)
	}
}

// remoteRemoved deletes a remote file whose content nothing uses any more.
func (s *Server) remoteRemoved(ctx context.Context, ref store.BlobRef) {
	rb, err := s.st.RemoteBlob(ctx, ref.UserID, ref.SHA256)
	if err != nil {
		return
	}
	_ = s.st.SetRemoteBlobState(ctx, rb.UserID, rb.SHA256, "deleting", "")
	s.remoteDelete(ctx, rb)
}

func (s *Server) remoteDelete(ctx context.Context, rb *store.RemoteBlob) {
	if _, err := s.enqueueJob(ctx, rb.NodeID, nodeapi.JobDelete, jobMeta{
		JobPayload: nodeapi.JobPayload{LocationID: rb.LocationID, UserID: rb.UserID, SHA256: rb.SHA256, Kind: nodeapi.KindRemote},
		Target:     fmt.Sprintf("remote:%d", rb.NodeID), Purpose: nodeapi.KindRemote,
	}); err != nil {
		slog.Warn("remote file delete not queued", "user", rb.UserID, "sha", rb.SHA256, "err", err)
	}
}

func (s *Server) remoteRetryDeletes(ctx context.Context, nodeID int64) {
	list, err := s.st.RemoteBlobsOfNode(ctx, nodeID, "deleting", 50)
	if err != nil {
		return
	}
	for i := range list {
		if time.Since(time.UnixMilli(list[i].UpdatedAt)) > time.Hour {
			s.remoteDelete(ctx, &list[i])
		}
	}
}

// remoteVerifyDue asks the node to re-hash remote files not checked for a
// week.
func (s *Server) remoteVerifyDue(ctx context.Context, nodeID int64) {
	list, err := s.st.RemoteBlobsOfNode(ctx, nodeID, "ok", 500)
	if err != nil {
		return
	}
	queued := 0
	for _, rb := range list {
		if rb.VerifiedAt != nil && time.Since(time.UnixMilli(*rb.VerifiedAt)) < remoteVerifyEvery {
			continue
		}
		if _, err := s.enqueueJob(ctx, nodeID, nodeapi.JobVerify, jobMeta{
			JobPayload: nodeapi.JobPayload{LocationID: rb.LocationID, UserID: rb.UserID, SHA256: rb.SHA256, Kind: nodeapi.KindRemote},
			Target:     fmt.Sprintf("remote:%d", nodeID), Purpose: nodeapi.KindRemote,
		}); err == nil {
			queued++
		}
		if queued >= 20 {
			return
		}
	}
}

// remoteVerified records a node's re-hash of a remote file.
func (s *Server) remoteVerified(ctx context.Context, meta jobMeta, res nodeapi.JobResult) {
	rb, err := s.st.RemoteBlob(ctx, meta.UserID, meta.SHA256)
	if err != nil || rb.State != "ok" {
		return
	}
	switch {
	case res.OK && res.SHA256 == rb.CipherSHA256:
		_, _ = s.st.DB.ExecContext(ctx, "UPDATE remote_blobs SET verified_at = ?, last_error = '' WHERE user_id = ? AND sha256 = ?", time.Now().UnixMilli(), rb.UserID, rb.SHA256)
	case res.OK:
		_ = s.st.SetRemoteBlobState(ctx, rb.UserID, rb.SHA256, "missing", "the copy on the node is damaged (checksum mismatch)")
		slog.Error("remote file damaged on its node", "user", rb.UserID, "sha", rb.SHA256, "node", rb.NodeID)
	default:
		_ = s.st.SetRemoteBlobState(ctx, rb.UserID, rb.SHA256, "missing", "the node could not check it: "+res.Error)
		slog.Error("remote file missing on its node", "user", rb.UserID, "sha", rb.SHA256, "node", rb.NodeID, "err", res.Error)
	}
}
