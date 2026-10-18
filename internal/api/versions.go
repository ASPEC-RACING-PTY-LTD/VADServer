// Owner: SRV-5 (Phase 5). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-5 at creation. Scope: api-v1 §10.4 file versions (feature versions.v1;
// devices read-only).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountVersions registers the routes of this feature area.
func (s *Server) mountVersions(rt routes) {
	rt.User.Get("/files/{id}/versions", notImplemented)
	rt.User.Get("/files/{id}/versions/{vid}/content", notImplemented)
	rt.Sess.Post("/files/{id}/versions/{vid}/restore", notImplemented)
	rt.Sess.Put("/files/{id}/versions/{vid}/pin", notImplemented)
}
