// Owner: SRV-7 (Phase 6). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-7 at creation. Scope: api-v1 §11 notification preferences.
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountNotifyPrefs registers the routes of this feature area.
func (s *Server) mountNotifyPrefs(rt routes) {
	rt.Sess.Get("/account/notification-prefs", notImplemented)
	rt.Sess.Put("/account/notification-prefs", notImplemented)
}
