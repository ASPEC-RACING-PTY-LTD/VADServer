// Owner: SRV-5 (Phase 5). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-5 at creation. Scope: api-v1 §10.1 sync folders (sess CRUD; dev GET own
// + PATCH acked_revision/state).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountSyncFolders registers the routes of this feature area.
func (s *Server) mountSyncFolders(rt routes) {
	rt.User.Get("/sync-folders", notImplemented)
	rt.Sess.Post("/sync-folders", notImplemented)
	rt.User.Get("/sync-folders/{id}", notImplemented)
	rt.User.Patch("/sync-folders/{id}", notImplemented) // dev: acked_revision/state only
	rt.Sess.Delete("/sync-folders/{id}", notImplemented)
}
