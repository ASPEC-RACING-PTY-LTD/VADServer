package api

import "time"

// SetUpdateAPIBase points the release checker at a fake GitHub API in tests.
func (s *Server) SetUpdateAPIBase(base string) { s.updates.client.APIBase = base }

// HoldFinalize makes upload finishes wait for release and shortens the
// asynchronous wait; the returned function restores both.
func HoldFinalize(wait time.Duration, hold <-chan struct{}) func() {
	prevWait, prevHook := finalizeWait, beforeFinalize
	finalizeWait, beforeFinalize = wait, func() { <-hold }
	return func() { finalizeWait, beforeFinalize = prevWait, prevHook }
}
