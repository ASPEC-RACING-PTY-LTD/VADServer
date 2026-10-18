package api

// SetUpdateAPIBase points the release checker at a fake GitHub API in tests.
func (s *Server) SetUpdateAPIBase(base string) { s.updates.client.APIBase = base }
