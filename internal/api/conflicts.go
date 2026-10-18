// Owner: SRV-5 (Phase 5). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-5 at creation. Scope: api-v1 §10.4 sync conflicts.
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountConflicts registers the routes of this feature area.
func (s *Server) mountConflicts(rt routes) {
	rt.Sess.Get("/sync/conflicts", notImplemented)
	rt.Sess.Post("/sync/conflicts/resolve", notImplemented)
}
