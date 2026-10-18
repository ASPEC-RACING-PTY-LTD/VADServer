// Owner: SRV-6 (Phase 4). Stub created by P0-03 (SRV-0); ownership passed to
// SRV-6 at creation. Scope: api-v1 §9 replication policies; paths per rev 1.0
// (sdk/contracts/history/api-v1-rev1.0.md).
//
// Routes answer 501 not_implemented until the owner replaces notImplemented
// with a real handler. Keep each route in its auth group (rt.User = dev+sess,
// rt.Dev, rt.Sess, rt.Admin, rt.Super) and call implementFeature from init()
// once the feature is complete.

package api

// mountReplication registers the routes of this feature area.
func (s *Server) mountReplication(rt routes) {
	rt.Sess.Get("/replication", notImplemented)
	rt.Sess.Post("/replication", notImplemented)
	rt.Sess.Get("/replication/{id}", notImplemented)
	rt.Sess.Patch("/replication/{id}", notImplemented)
	rt.Sess.Delete("/replication/{id}", notImplemented)
}
