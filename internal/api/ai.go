// Owner: SRV-7 (Phase 6). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-7 at creation. Scope: api-v1 §11 AI (feature ai.v1).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountAI registers the routes of this feature area.
func (s *Server) mountAI(rt routes) {
	rt.Sess.Get("/account/ai", notImplemented)
	rt.Sess.Put("/account/ai", notImplemented)
	rt.Sess.Post("/ai/diagnose", notImplemented)
}
