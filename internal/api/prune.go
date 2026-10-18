// Owner: SRV-4 (Phase 4). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-4 at creation. Scope: api-v1 §9 retention, prune, GC, verify, backup
// settings; paths per rev 1.0 (sdk/contracts/history/api-v1-rev1.0.md).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountPrune registers the routes of this feature area.
func (s *Server) mountPrune(rt routes) {
	rt.Sess.Get("/retention-policies", notImplemented)
	rt.Sess.Post("/retention-policies", notImplemented)
	rt.Sess.Get("/retention-policies/{id}", notImplemented)
	rt.Sess.Patch("/retention-policies/{id}", notImplemented)
	rt.Sess.Delete("/retention-policies/{id}", notImplemented)
	rt.Sess.Post("/repos/{id}/prune/plan", notImplemented)
	rt.Sess.Post("/repos/{id}/prune/execute", notImplemented) // {plan_token}
	rt.Sess.Post("/repos/{id}/verify", notImplemented)
	rt.Admin.Post("/admin/gc/plan", notImplemented)
	rt.Admin.Post("/admin/gc/execute", notImplemented)
	rt.Admin.Get("/admin/backup-settings", notImplemented)
	rt.Admin.Put("/admin/backup-settings", notImplemented)
}
