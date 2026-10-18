// Owner: SRV-0 (P0-05). Scope: api-v1 §3 summary.

package api

import (
	"net/http"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// mountSummary registers the routes of this feature area.
func (s *Server) mountSummary(rt routes) {
	rt.Sess.Get("/summary", s.handleSummary)
}

// handleSummary returns the dashboard counters of the session user. Counters
// of features without data are zero; readiness stays "unknown" until the
// readiness feature (SRV-7) provides an overall status here.
func (s *Server) handleSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.st.Summary(r.Context(), principal(r).User.ID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	sum.Readiness = wire.ReadinessStatusUnknown
	writeJSON(w, 200, sum)
}
