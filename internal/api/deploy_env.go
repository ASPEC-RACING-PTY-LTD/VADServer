// Owner: SRV-0 (P0-37). Scope: api-v1 §11A deployment environment (feature
// deploy_env.v1).
//
// The server never edits the host .env. It reports the environment against
// deploy/env.schema.json (embedded) and records removal approvals of obsolete
// variables in ${STATE}/deploy/env-removal-approvals.json, which install.sh
// upgrade applies once and then deletes.

package api

import (
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/deployenv"
)

func init() { implementFeature(wire.FeatureDeployEnv) }

// mountDeployEnv registers the routes of this feature area.
func (s *Server) mountDeployEnv(rt routes) {
	rt.Super.Get("/admin/deploy/env", s.handleDeployEnv)
	rt.Super.Post("/admin/deploy/env/removals", s.handleDeployEnvRemovals)
	rt.Super.Delete("/admin/deploy/env/removals/{name}", s.handleDeployEnvWithdraw)
}

var deployEnvSchema = sync.OnceValues(deployenv.Embedded)

// deployEnvApprovals guards the approvals file per state directory (tests run
// several servers in one process).
var deployEnvApprovals sync.Map // state dir -> *deployenv.Approvals

func (s *Server) envApprovals() *deployenv.Approvals {
	a, _ := deployEnvApprovals.LoadOrStore(s.cfg.StateDir, deployenv.NewApprovals(s.cfg.StateDir))
	return a.(*deployenv.Approvals)
}

func (s *Server) deployEnvStatus(pending []wire.DeployEnvRemovalApproval) (wire.DeployEnvStatus, error) {
	schema, err := deployEnvSchema()
	if err != nil {
		return wire.DeployEnvStatus{}, err
	}
	return deployenv.Compute(schema, os.LookupEnv, pending).DeployEnvStatus, nil
}

func (s *Server) handleDeployEnv(w http.ResponseWriter, r *http.Request) {
	pending, err := s.envApprovals().List()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st, err := s.deployEnvStatus(pending)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}

func envValidation(field, msg string) *apiError {
	e := errf(400, "validation_failed", "%s", msg)
	e.Details = map[string]any{"fields": map[string]string{field: msg}}
	return e
}

func (s *Server) handleDeployEnvRemovals(w http.ResponseWriter, r *http.Request) {
	var req wire.DeployEnvRemovalRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	schema, err := deployEnvSchema()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if len(req.Names) == 0 || len(req.Names) > len(schema.Variables) {
		writeErr(w, r, envValidation("names", "List the variables to remove"))
		return
	}
	seen := map[string]bool{}
	var notObsolete []string
	for _, n := range req.Names {
		if !deployenv.ValidName(n) {
			writeErr(w, r, envValidation("names", "Invalid variable name"))
			return
		}
		if seen[n] {
			writeErr(w, r, envValidation("names", "Duplicate variable "+n))
			return
		}
		seen[n] = true
		v, ok := schema.Lookup(n)
		if !ok {
			writeErr(w, r, envValidation("names", "Unknown variable "+n))
			return
		}
		if !v.Obsolete {
			notObsolete = append(notObsolete, n)
		}
	}
	if req.ConfirmText != wire.DeployEnvRemovalConfirmText(len(req.Names)) {
		writeErr(w, r, envValidation("confirm_text", "Type "+wire.DeployEnvRemovalConfirmText(len(req.Names))+" to confirm"))
		return
	}
	// Only obsolete variables can be approved here; any other change to the
	// host .env is made with install.sh (env-review or upgrade).
	if len(notObsolete) > 0 {
		e := errf(409, wire.CodeEnvChangeRequiresApproval, "Only obsolete variables can be removed from the console. Change other settings with install.sh on the host.")
		e.Details = map[string]any{"names": notObsolete}
		writeErr(w, r, e)
		return
	}
	p := principal(r)
	pending, err := s.envApprovals().Approve(req.Names, p.User.ID, time.Now().UnixMilli())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, 0, "admin.env_removal_approved", strings.Join(req.Names, ","), "applied by the next install.sh upgrade", true)
	st, err := s.deployEnvStatus(pending)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}

func (s *Server) handleDeployEnvWithdraw(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !deployenv.ValidName(name) {
		writeErr(w, r, envValidation("name", "Invalid variable name"))
		return
	}
	ok, err := s.envApprovals().Withdraw(name)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if !ok {
		writeErr(w, r, errf(404, "not_found", "No pending removal for %s", name))
		return
	}
	s.activity(r, 0, "admin.env_removal_withdrawn", name, "", true)
	pending, err := s.envApprovals().List()
	if err != nil {
		writeErr(w, r, err)
		return
	}
	st, err := s.deployEnvStatus(pending)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, st)
}
