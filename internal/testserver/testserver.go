// Package testserver starts an in-process VaultDock server for integration
// tests of other components (the desktop sync engine).
package testserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/app"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

type Server struct {
	URL   string
	Store *store.Store
	t     testing.TB
	ts    *httptest.Server
	web   *http.Client
	csrf  string
}

const Password = "test-password-123"

// Start runs a server with an administrator "admin" signed in.
func Start(t testing.TB) *Server {
	dir := t.TempDir()
	t.Setenv("VAULTDOCK_STATE_DIR", filepath.Join(dir, "state"))
	t.Setenv("VAULTDOCK_DEFAULT_STORAGE", filepath.Join(dir, "storage", "default"))
	t.Setenv("VAULTDOCK_STORAGE_ROOTS", filepath.Join(dir, "storage"))
	t.Setenv("VAULTDOCK_SETUP_TOKEN", "")
	t.Setenv("VAULTDOCK_GEOIP_DB", "")
	t.Setenv("VAULTDOCK_UPDATE_CHECK", "false") // no network in tests
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	// Built exactly as `vaultdock serve` builds it (internal/app), including
	// the background maintenance loop.
	a, err := app.New(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	bgDone := make(chan struct{})
	go func() { a.Run(ctx); close(bgDone) }()
	ts := httptest.NewServer(a.Server.Handler(nil))
	jar, _ := cookiejar.New(nil)
	s := &Server{URL: ts.URL, Store: a.Store, t: t, ts: ts, web: &http.Client{Jar: jar}}
	t.Cleanup(func() {
		ts.Close()
		cancel()
		<-bgDone
		_ = a.Close()
	})
	out := s.Do("POST", "/api/v1/setup", map[string]any{"username": "admin", "password": Password}, 200)
	s.csrf = out["user"].(map[string]any)["csrf_token"].(string)
	return s
}

// Do performs a request with the admin's browser session.
func (s *Server) Do(method, path string, body any, want int) map[string]any {
	s.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.URL+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if s.csrf != "" {
		req.Header.Set("X-CSRF-Token", s.csrf)
	}
	resp, err := s.web.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if want > 0 && resp.StatusCode != want {
		s.t.Fatalf("%s %s: %d (want %d): %s", method, path, resp.StatusCode, want, raw)
	}
	return out
}

// Content downloads a file's content by path.
func (s *Server) Content(path string) ([]byte, map[string]any, error) {
	res := s.Do("GET", "/api/v1/files/resolve?path="+url.QueryEscape(path), nil, 0)
	n, ok := res["node"].(map[string]any)
	if !ok {
		return nil, nil, fmt.Errorf("not found: %s", path)
	}
	resp, err := s.web.Get(fmt.Sprintf("%s/api/v1/files/%d/content", s.URL, int64(n["id"].(float64))))
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return b, n, err
}

// Device enrolls and approves a device for the admin and returns a signed
// client.
func (s *Server) Device() (*client.Client, *client.Me) {
	s.t.Helper()
	base, _ := url.Parse(s.URL)
	c := client.New(base, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ctx := context.Background()
	res, err := c.Enroll(ctx, client.EnrollRequest{Login: "admin", Password: Password, DeviceName: "TEST", OS: "Windows", Identifier: "test", PublicKey: protocol.EncodePublicKey(pub)})
	if err != nil {
		s.t.Fatal(err)
	}
	s.Do("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, 200)
	c.DeviceID, c.Key = res.RequestID, priv
	me, err := c.Me(ctx)
	if err != nil {
		s.t.Fatal(err)
	}
	return c, me
}

// Client returns the HTTP client carrying the admin's session cookie (for
// requests Do cannot express, such as Range downloads).
func (s *Server) Client() *http.Client { return s.web }
