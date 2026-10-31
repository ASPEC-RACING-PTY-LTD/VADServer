package nodeagent

// Remote files: content this node alone holds for the server, encrypted
// for a key only the server has. The server reads byte ranges of them from
// the node's public address (a Cloudflare tunnel, a reverse proxy) with
// requests signed with the node secret; nothing else is served there.

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
)

// setReadKey keeps the key that signs the server's reads (the SHA-256 of the
// node secret, which the server stores in place of the secret).
func (a *Agent) setReadKey(secret string) {
	h := sha256.Sum256([]byte(secret))
	a.mu.Lock()
	a.readKey = []byte(hex.EncodeToString(h[:]))
	a.mu.Unlock()
}

func (a *Agent) readKeyBytes() []byte {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.readKey
}

// serveRemote answers GET /v1/remote/<location>/<user>/<sha256> (Range
// supported) for requests signed by the server.
func (a *Agent) serveRemote(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/remote/"), "/")
	if len(parts) != 3 {
		http.NotFound(w, r)
		return
	}
	loc, err1 := strconv.ParseInt(parts[0], 10, 64)
	user, err2 := strconv.ParseInt(parts[1], 10, 64)
	sha := parts[2]
	if err1 != nil || err2 != nil || user <= 0 || !nodeapi.ValidSHA256(sha) {
		http.NotFound(w, r)
		return
	}
	key := a.readKeyBytes()
	if key == nil {
		http.Error(w, "not connected to the server yet", http.StatusServiceUnavailable)
		return
	}
	ts := r.Header.Get(nodeapi.HeaderTime)
	t, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || abs(time.Now().Unix()-t) > nodeapi.ReadSkew {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	want := nodeapi.SignRead(key, r.Method, r.URL.Path, r.Header.Get("Range"), ts)
	if !hmac.Equal([]byte(want), []byte(r.Header.Get(nodeapi.HeaderSig))) {
		http.Error(w, "unauthorised", http.StatusUnauthorized)
		return
	}
	root, ok := a.locs().get(loc)
	if !ok {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(root, filepath.FromSlash(nodeapi.RemoteFileName(user, sha))))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, "", time.Time{}, f)
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// pairingFile holds a pairing secret written by the installer; it is sent
// once with the join request and removed when the node is approved.
func (c Config) pairingFile() string { return filepath.Join(c.DataDir(), "pairing") }

func (a *Agent) pairingSecret() string {
	b, err := os.ReadFile(a.cfg.pairingFile())
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}
