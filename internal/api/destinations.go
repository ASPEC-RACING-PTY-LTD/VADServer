// Owner: SRV-1 (Phase 4). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-1 at creation. Scope: api-v1 §4 destinations (feature destinations.v1).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountDestinations registers the routes of this feature area.
func (s *Server) mountDestinations(rt routes) {
	rt.User.Get("/destinations", notImplemented) // dev: names and ids only
	rt.Sess.Post("/destinations", notImplemented)
	rt.Sess.Post("/destinations/test", notImplemented)
	rt.Sess.Get("/destinations/providers", notImplemented)
	rt.Sess.Patch("/destinations/{id}", notImplemented)
	rt.Sess.Delete("/destinations/{id}", notImplemented) // 409 in_use
	rt.Sess.Post("/destinations/{id}/test", notImplemented)
	rt.Admin.Get("/admin/destinations", notImplemented)
	rt.Admin.Post("/admin/destinations", notImplemented)
	rt.Admin.Get("/admin/destinations/{id}", notImplemented)
	rt.Admin.Patch("/admin/destinations/{id}", notImplemented)
	rt.Admin.Delete("/admin/destinations/{id}", notImplemented)
}
