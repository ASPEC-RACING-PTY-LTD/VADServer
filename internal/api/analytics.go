// Owner: SRV-6 (Phase 4). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-6 at creation. Scope: api-v1 §9 analytics (repository and storage analytics).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountAnalytics registers the routes of this feature area.
func (s *Server) mountAnalytics(rt routes) {
	rt.Sess.Get("/repos/{id}/analytics", notImplemented)
	rt.Sess.Get("/storage/analytics", notImplemented) // ?group=job|app|device|destination&range=
}
