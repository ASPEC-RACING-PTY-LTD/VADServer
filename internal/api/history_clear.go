package api

// Admin "Clear history": removes finished transfers, non-audit activity and
// notifications so counters such as "uploaded" start from zero (for example
// after the server was moved and its stored files were not). Audit entries
// stay, and the clear itself is audited.

import (
	"fmt"
	"net/http"
)

func (s *Server) handleAdminClearHistory(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.ClearHistory(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, principal(r).User.ID, "history_cleared", "server",
		fmt.Sprintf("cleared history: %d transfers, %d activity entries, %d notifications", n.Transfers, n.Activity, n.Notifications), true)
	writeJSON(w, http.StatusOK, n)
}
