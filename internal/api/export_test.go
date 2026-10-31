package api

import (
	"net/http"
	"time"
)

// SetUpdateAPIBase points the release checker at a fake GitHub API in tests.
func (s *Server) SetUpdateAPIBase(base string) { s.updates.client.APIBase = base }

// HoldFinalize makes upload finishes wait for release and shortens the
// asynchronous wait; the returned function restores both.
func HoldFinalize(wait time.Duration, hold <-chan struct{}) func() {
	prevWait, prevHook := finalizeWait, beforeFinalize
	finalizeWait, beforeFinalize = wait, func() { <-hold }
	return func() { finalizeWait, beforeFinalize = prevWait, prevHook }
}

// StreamHashHits returns how many finishes used the hash computed while the
// upload arrived.
func StreamHashHits() int64 { return streamHashHits.Load() }

// SetRemoteHTTP sets the client used to read from remote storage nodes
// (tests trust their own TLS certificates).
func (s *Server) SetRemoteHTTP(c *http.Client) { s.remote().http = c }

// ClearRemoteCache drops the relay's decrypted chunks.
func (s *Server) ClearRemoteCache() { s.remote().cache = newChunkCache(relayCacheBytes) }
