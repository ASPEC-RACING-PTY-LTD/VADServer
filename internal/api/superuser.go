// Owner: SRV-M (P1-20). Scope: api-v1 §11B superuser. No routes of its own:
// the contract puts granting/revoking is_superuser on PATCH /admin/users/{id}
// (admin.go, admin group), which calls setUserSuperuser below; user delete,
// disable and demote call guardLastSuperuser. rotate-server lives in keys.go
// and blacklist clearing in profiles.go. The "super" auth group is
// requireSuperuser in server.go.

package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// mountSuperuser registers the routes of this feature area (none; see the
// file header).
func (s *Server) mountSuperuser(rt routes) {
}

// setUserSuperuser applies {"is_superuser"} from PATCH /admin/users/{id}.
// The caller must be a superuser (403 not_superuser); granting needs an
// active administrator (400 not_admin); revoking the last superuser fails
// with 409 last_superuser. Every change is audit-logged.
func (s *Server) setUserSuperuser(r *http.Request, target *store.User, on bool) error {
	p := principal(r)
	if p == nil || p.Session == nil || !p.User.IsAdmin() {
		return errf(403, wire.CodeNotSuperuser, "Superuser access required")
	}
	ok, err := s.st.IsSuperuser(r.Context(), p.User.ID)
	if err != nil {
		return err
	}
	if !ok {
		return errf(403, wire.CodeNotSuperuser, "Superuser access required")
	}
	changed, err := s.st.SetSuperuser(r.Context(), target.ID, on)
	switch {
	case errors.Is(err, store.ErrLastSuperuser):
		return errf(409, "last_superuser", "At least one superuser is required")
	case errors.Is(err, store.ErrNotAdmin):
		return errf(400, "not_admin", "Only active administrators can be superusers")
	case err != nil:
		return err
	}
	if changed {
		action := "admin.superuser_revoked"
		if on {
			action = "admin.superuser_granted"
		}
		s.activity(r, target.ID, action, target.Username, "", true)
	}
	return nil
}

// guardLastSuperuser returns 409 last_superuser when userID is the only
// active superuser, so deleting, disabling or demoting it must be refused.
func (s *Server) guardLastSuperuser(ctx context.Context, userID int64) error {
	last, err := s.st.IsLastSuperuser(ctx, userID)
	if err != nil {
		return err
	}
	if last {
		return errf(409, "last_superuser", "At least one superuser is required")
	}
	return nil
}
