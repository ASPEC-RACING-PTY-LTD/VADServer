// Owner: SRV-K (P1-12). Scope: api-v1 §5A server identity (GET
// /keys/server) and §11B SEK rotation (POST /admin/keys/rotate-server).
// The repository key routes (/repos/{id}/keys/*, PATCH /repos/{id}) belong
// to P1-13, which also advertises escrow.v1.

package api

import (
	"context"
	"errors"
	"net/http"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

// mountKeys registers the routes of this feature area.
func (s *Server) mountKeys(rt routes) {
	rt.User.Get("/keys/server", s.handleServerKeys)
	s.mountRepoKeys(rt)                                                  // P1-13: /repos/{id}/keys/*, PATCH /repos/{id} (repo_keys.go)
	rt.Super.Post("/admin/keys/rotate-server", s.handleRotateServerKeys) // §11B; 202 operation
	s.mountKEKExport(rt)
}

// keyManagers holds the key custody of each Server (server.go is not owned
// by this feature area, so the manager is attached here; see SetKeys).
var keyManagers sync.Map // *Server -> *keys.Manager

// SetKeys attaches the server key custody. A nil manager (no KEK) leaves key
// routes answering 409 escrow_unavailable.
func (s *Server) SetKeys(m *keys.Manager) {
	if m == nil {
		keyManagers.Delete(s)
		return
	}
	keyManagers.Store(s, m)
}

// Keys returns the server key custody, or nil when it is unavailable. The
// nil manager's methods return keys.ErrUnavailable.
func (s *Server) Keys() *keys.Manager {
	if v, ok := keyManagers.Load(s); ok {
		return v.(*keys.Manager)
	}
	return nil
}

// errEscrowUnavailable is the reply when the server runs without a KEK.
func errEscrowUnavailable() *apiError {
	return errf(http.StatusConflict, wire.CodeEscrowUnavailable, "Server key custody is unavailable: the server key file (KEK) is missing")
}

// keysErr maps key custody errors to API errors.
func keysErr(err error) error {
	if errors.Is(err, keys.ErrUnavailable) {
		return errEscrowUnavailable()
	}
	return err
}

func (s *Server) handleServerKeys(w http.ResponseWriter, r *http.Request) {
	id, err := s.Keys().Identity()
	if err != nil {
		writeErr(w, r, keysErr(err))
		return
	}
	writeJSON(w, http.StatusOK, wire.ServerIdentityResponse{ServerIdentity: id})
}

// rotateServerResult is the operation result of a server key rotation.
type rotateServerResult struct {
	SEKVersion uint32 `json:"sek_version"`
}

func (s *Server) handleRotateServerKeys(w http.ResponseWriter, r *http.Request) {
	m := s.Keys()
	if m == nil {
		writeErr(w, r, errEscrowUnavailable())
		return
	}
	p := principal(r)
	uid := p.User.ID
	params := map[string]string{"scope": "server", "key": "sek"}
	fn := func(ctx context.Context, prog *OpProgress) (any, error) {
		opID := prog.ID
		v, err := m.RotateSEK(ctx, keys.Audit{ActorUserID: &uid, OperationID: &opID, Details: "admin rotate-server"})
		if err != nil {
			return nil, keysErr(err)
		}
		s.rewrapEscrowAfterSEKRotation(ctx, opID, uid) // P1-13: re-wrap the current epochs
		prog.Set(1, 1, 0)
		return rotateServerResult{SEKVersion: v}, nil
	}
	s.acceptOperation(w, r, wire.OperationKindRotateKeys, params, fn)
}
