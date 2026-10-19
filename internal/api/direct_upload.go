package api

// Direct (LAN) uploads (upload.direct). A desktop client on the server's
// network may send its uploads to a local address of the server instead of
// the public URL, bypassing the proxy's request and speed limits. Before it
// does, it proves that the local address reaches this server:
//
//  1. through the public URL (whose identity it already trusts) it asks
//     POST /devices/direct-probe for a one-time nonce and its answer;
//  2. through the local address it asks GET /devices/direct-probe/{nonce};
//     only this server knows the answer.
//
// The probe reply also carries the device's direct_reset_gen: an
// administrator who resets direct uploads for a device raises it, and a
// client that sees a higher value than the one it enabled under turns the
// setting off.

import (
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/auth"
)

const directProbeTTL = 2 * time.Minute

type directProbe struct {
	deviceID int64
	answer   string
	expires  time.Time
}

var directProbes = struct {
	sync.Mutex
	m map[string]directProbe
}{m: map[string]directProbe{}}

func (s *Server) handleDirectProbeStart(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	var gen int64
	if err := s.st.DB.QueryRowContext(r.Context(), "SELECT direct_reset_gen FROM devices WHERE id = ?", p.Device.ID).Scan(&gen); err != nil {
		writeErr(w, r, err)
		return
	}
	nonce, answer := auth.RandomToken(18), auth.RandomToken(24)
	now := time.Now()
	directProbes.Lock()
	for k, v := range directProbes.m {
		if now.After(v.expires) {
			delete(directProbes.m, k)
		}
	}
	directProbes.m[nonce] = directProbe{deviceID: p.Device.ID, answer: answer, expires: now.Add(directProbeTTL)}
	directProbes.Unlock()
	writeJSON(w, 200, wire.DirectProbe{Nonce: nonce, Answer: answer, ResetGen: gen, ExpiresIn: int64(directProbeTTL / time.Second)})
}

func (s *Server) handleDirectProbeAnswer(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	nonce := chi.URLParam(r, "nonce")
	directProbes.Lock()
	v, ok := directProbes.m[nonce]
	if ok {
		delete(directProbes.m, nonce) // one use
	}
	directProbes.Unlock()
	if !ok || time.Now().After(v.expires) || v.deviceID != p.Device.ID {
		writeErr(w, r, errf(404, "not_found", "Unknown or expired probe"))
		return
	}
	writeJSON(w, 200, wire.DirectProbeAnswer{Answer: v.answer})
}

// handleAdminDirectReset turns direct uploads off on a device: the client
// sees the raised generation on its next probe and goes back to the public
// URL.
func (s *Server) handleAdminDirectReset(w http.ResponseWriter, r *http.Request) {
	id, err := idParam(r, "id")
	if err != nil {
		writeErr(w, r, err)
		return
	}
	d, err := s.st.Device(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if _, err := s.st.DB.ExecContext(r.Context(), "UPDATE devices SET direct_reset_gen = direct_reset_gen + 1 WHERE id = ?", id); err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, d.UserID, "admin.device_direct_reset", d.Name, "direct LAN uploads turned off", true)
	writeJSON(w, 200, map[string]any{"ok": true})
}

func init() { implementFeature(wire.FeatureDirectUpload) }
