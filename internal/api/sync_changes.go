// Owner: SRV-5 (Phase 5). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-5 at creation. Scope: api-v1 §10.2 change feed and device ops (feature
// sync_two_way.v1).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountSyncChanges registers the routes of this feature area.
func (s *Server) mountSyncChanges(rt routes) {
	rt.Dev.Get("/sync/changes", notImplemented)                                          // ?folder_id=&since=&wait=
	rt.Dev.With(s.requireDeviceCanDelete).Post("/sync/folders/{id}/ops", notImplemented) // also 403 device_cannot_delete outside an authorised two_way folder
}
