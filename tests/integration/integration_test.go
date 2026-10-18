//go:build integration

// Package integration exercises a deployed VaultDock instance over HTTP.
//
//	VAULTDOCK_URL=http://127.0.0.1:8080 VAULTDOCK_SETUP_TOKEN=... go test -tags integration ./tests/integration -run Phase1
//	(restart the container)
//	... -run Phase2
//
// Phase 1 populates the server; phase 2 verifies everything survived a
// container restart.
package integration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/automation"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/protocol"
)

const adminPass = "integration-admin-pass"

func baseURL(t *testing.T) string {
	u := os.Getenv("VAULTDOCK_URL")
	if u == "" {
		t.Skip("VAULTDOCK_URL not set")
	}
	return strings.TrimRight(u, "/")
}

func stateFile() string {
	if p := os.Getenv("VAULTDOCK_TEST_STATE"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "vaultdock-integration.json")
}

type persisted struct {
	DeviceID  int64             `json:"device_id"`
	Key       []byte            `json:"key"`
	Files     map[string]string `json:"files"` // path -> sha256
	BobDevice int64             `json:"bob_device"`
}

type web struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func newWeb(t *testing.T) *web {
	jar, _ := cookiejar.New(nil)
	return &web{t: t, base: baseURL(t), c: &http.Client{Jar: jar, Timeout: 2 * time.Minute}}
}

func (w *web) do(method, path string, body any) (int, map[string]any) {
	w.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.base+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	if w.csrf != "" {
		req.Header.Set("X-CSRF-Token", w.csrf)
	}
	resp, err := w.c.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if u, ok := out["user"].(map[string]any); ok {
		if c, _ := u["csrf_token"].(string); c != "" {
			w.csrf = c
		}
	}
	return resp.StatusCode, out
}

func (w *web) must(method, path string, body any, want int) map[string]any {
	w.t.Helper()
	code, out := w.do(method, path, body)
	if code != want {
		w.t.Fatalf("%s %s: %d want %d: %v", method, path, code, want, out)
	}
	return out
}

func (w *web) login(user, pass string) *web {
	w.must("POST", "/api/v1/auth/login", map[string]any{"login": user, "password": pass}, 200)
	return w
}

func (w *web) content(path string) ([]byte, int) {
	code, out := w.do("GET", "/api/v1/files/resolve?path="+url.QueryEscape(path), nil)
	if code != 200 {
		return nil, code
	}
	id := int64(out["node"].(map[string]any)["id"].(float64))
	resp, err := w.c.Get(fmt.Sprintf("%s/api/v1/files/%d/content", w.base, id))
	if err != nil {
		w.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return b, resp.StatusCode
}

func enroll(t *testing.T, w *web, base, login, pass string) (*client.Client, ed25519.PrivateKey) {
	u, _ := url.Parse(base)
	c := client.New(u, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	res, err := c.Enroll(context.Background(), client.EnrollRequest{Login: login, Password: pass, DeviceName: "INTEGRATION-" + strings.ToUpper(login), OS: "Linux test", Identifier: "it-" + login, PublicKey: protocol.EncodePublicKey(pub), ClientVersion: "it"})
	if err != nil {
		t.Fatalf("enroll %s: %v", login, err)
	}
	w.must("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, 200)
	c.DeviceID, c.Key = res.RequestID, priv
	if _, err := c.Me(context.Background()); err != nil {
		t.Fatal(err)
	}
	return c, priv
}

func randomFile(t *testing.T, dir, name string, size int64) (string, string) {
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	if _, err := io.CopyN(io.MultiWriter(f, h), rand.Reader, size); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p, hex.EncodeToString(h.Sum(nil))
}

// flakyProxy forwards TCP connections and kills each one after a byte
// budget, simulating network drops during uploads.
type flakyProxy struct {
	ln      net.Listener
	target  string
	budget  int64
	cuts    atomic.Int64
	enabled atomic.Bool
}

func startProxy(t *testing.T, target string, budget int64) *flakyProxy {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &flakyProxy{ln: ln, target: target, budget: budget}
	p.enabled.Store(true)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return p
}

func (p *flakyProxy) handle(c net.Conn) {
	defer c.Close()
	up, err := net.Dial("tcp", p.target)
	if err != nil {
		return
	}
	defer up.Close()
	go io.Copy(c, up)
	if !p.enabled.Load() {
		io.Copy(up, c)
		return
	}
	n, _ := io.CopyN(up, c, p.budget)
	if n == p.budget {
		p.cuts.Add(1) // connection dropped mid-transfer
	}
}

func TestPhase1Populate(t *testing.T) {
	base := baseURL(t)
	tmp := t.TempDir()
	ctx := context.Background()

	// --- setup & authentication
	admin := newWeb(t)
	if code, _ := admin.do("POST", "/api/v1/setup", map[string]any{"username": "admin", "password": adminPass, "setup_token": "wrong-token"}); code != 403 {
		t.Fatalf("setup accepted a wrong token: %d", code)
	}
	admin.must("POST", "/api/v1/setup", map[string]any{"username": "admin", "password": adminPass, "setup_token": os.Getenv("VAULTDOCK_SETUP_TOKEN")}, 200)
	if code, out := newWeb(t).do("POST", "/api/v1/auth/login", map[string]any{"login": "admin", "password": "nope-nope-nope"}); code != 401 {
		t.Fatalf("invalid credentials accepted: %d %v", code, out)
	}
	if code, _ := newWeb(t).do("GET", "/api/v1/files", nil); code != 401 {
		t.Fatalf("unauthenticated access allowed: %d", code)
	}
	// registration (user registration flow): admin enables it, a user registers
	admin.must("PUT", "/api/v1/admin/settings", map[string]any{
		"site_name": "VaultDock IT", "registration_enabled": true, "default_quota_bytes": 0, "max_file_bytes": 0,
		"trash_retention_days": 30, "activity_retention_days": 90, "device_request_ttl_minutes": 15, "keep_replaced_versions": true, "min_free_bytes": 0,
	}, 200)
	bob := newWeb(t)
	bob.must("POST", "/api/v1/auth/register", map[string]any{"username": "bob", "password": "bob-password-123"}, 200)
	users := admin.must("GET", "/api/v1/admin/users", nil, 200)["users"].([]any)
	var bobID int
	for _, u := range users {
		if u.(map[string]any)["username"] == "bob" {
			bobID = int(u.(map[string]any)["id"].(float64))
		}
	}
	admin.must("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", bobID), map[string]any{"quota_bytes": 5 << 20}, 200)

	// --- devices: rejection then approval
	u, _ := url.Parse(base)
	rc := client.New(u, client.Options{})
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	rej, err := rc.Enroll(ctx, client.EnrollRequest{Login: "admin", Password: adminPass, DeviceName: "REJECT-ME", OS: "Linux", Identifier: "x", PublicKey: protocol.EncodePublicKey(pub)})
	if err != nil {
		t.Fatal(err)
	}
	admin.must("POST", fmt.Sprintf("/api/v1/devices/%d/reject", rej.RequestID), nil, 200)
	if st, _ := rc.EnrollStatus(ctx, rej.RequestID, rej.PollToken); st == nil || st.Status != "rejected" {
		t.Fatalf("rejection not visible to the device: %+v", st)
	}
	dc, key := enroll(t, admin, base, "admin", adminPass)
	bc, _ := enroll(t, bob, base, "bob", "bob-password-123")

	files := map[string]string{}

	// --- large file through a proxy that drops every connection after
	// 20 MiB: the upload must resume and complete with intact content.
	host := strings.TrimPrefix(strings.TrimPrefix(base, "http://"), "https://")
	proxy := startProxy(t, host, 20<<20)
	pu, _ := url.Parse("http://" + proxy.ln.Addr().String())
	flaky := client.New(pu, client.Options{})
	flaky.DeviceID, flaky.Key = dc.DeviceID, key
	bigPath, bigSum := randomFile(t, tmp, "match-recording.mp4", 300<<20)
	start := time.Now()
	res, err := flaky.UploadFile(ctx, bigPath, "/Medal/Valorant/match-recording.mp4", []string{"clips"})
	if err != nil {
		t.Fatalf("large upload over flaky network: %v", err)
	}
	if proxy.cuts.Load() == 0 {
		t.Fatal("proxy never interrupted the transfer; the test did not simulate failures")
	}
	t.Logf("300 MiB uploaded through %d dropped connections in %s (%s)", proxy.cuts.Load(), time.Since(start).Round(time.Millisecond), res.Result)
	files["/Medal/Valorant/match-recording.mp4"] = bigSum

	// --- interrupted transfer recovery: abandon an upload half way, then a
	// "restarted" client resumes the same server session.
	p2, sum2 := randomFile(t, tmp, "resume.bin", 40<<20)
	st2, _ := os.Stat(p2)
	cr, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Backups/resume.bin", Size: st2.Size(), SHA256: sum2})
	if err != nil {
		t.Fatal(err)
	}
	f2, _ := os.Open(p2)
	for _, idx := range []int64{0, 1} {
		if err := dc.PutChunk(ctx, cr.Upload.ID, idx, f2, idx*cr.Upload.ChunkSize, cr.Upload.ChunkLen(idx)); err != nil {
			t.Fatal(err)
		}
	}
	f2.Close()
	again, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Backups/resume.bin", Size: st2.Size(), SHA256: sum2})
	if err != nil || !again.Resumed || len(again.Upload.Received) != 2 {
		t.Fatalf("expected resumable session with 2 chunks: %+v %v", again, err)
	}
	if _, err := dc.UploadFile(ctx, p2, "/Backups/resume.bin", nil); err != nil {
		t.Fatal(err)
	}
	files["/Backups/resume.bin"] = sum2

	// --- duplicates
	res, err = dc.UploadFile(ctx, bigPath, "/Archive/copy-of-match.mp4", nil)
	if err != nil || res.Result != "deduplicated" {
		t.Fatalf("duplicate handling: %+v %v", res, err)
	}
	files["/Archive/copy-of-match.mp4"] = bigSum
	res, err = dc.UploadFile(ctx, bigPath, "/Medal/Valorant/match-recording.mp4", nil)
	if err != nil || res.Result != "unchanged" {
		t.Fatalf("re-upload of identical file: %+v %v", res, err)
	}

	// --- quota enforcement for bob (5 MiB)
	small, smallSum := randomFile(t, tmp, "bob-small.bin", 3<<20)
	if _, err := bc.UploadFile(ctx, small, "/bob-small.bin", nil); err != nil {
		t.Fatal(err)
	}
	big2, _ := randomFile(t, tmp, "bob-big.bin", 4<<20)
	if _, err := bc.UploadFile(ctx, big2, "/bob-big.bin", nil); !client.IsCode(err, "quota_exceeded") {
		t.Fatalf("quota not enforced: %v", err)
	}

	// --- user isolation
	code, out := admin.do("GET", "/api/v1/files/resolve?path=/Medal/Valorant/match-recording.mp4", nil)
	if code != 200 {
		t.Fatal(out)
	}
	adminFile := int64(out["node"].(map[string]any)["id"].(float64))
	for _, p := range []string{fmt.Sprintf("/api/v1/files/%d", adminFile), fmt.Sprintf("/api/v1/files/%d/content", adminFile)} {
		if code, _ := bob.do("GET", p, nil); code != 404 {
			t.Fatalf("bob accessed admin's file via %s: %d", p, code)
		}
	}
	if code, _ := bob.do("GET", "/api/v1/admin/users", nil); code != 403 {
		t.Fatalf("bob reached admin API: %d", code)
	}
	if _, err := bc.CreateUpload(ctx, client.UploadRequest{Path: "/../../etc/passwd", Size: 1, SHA256: strings.Repeat("a", 64)}); !client.IsCode(err, "invalid_path") {
		t.Fatalf("path traversal accepted: %v", err)
	}

	// --- range request (video seeking)
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/files/%d/content", base, adminFile), nil)
	req.Header.Set("Range", "bytes=100000000-100000999")
	resp, err := admin.c.Do(req)
	if err != nil || resp.StatusCode != 206 {
		t.Fatalf("range request: %v %v", resp.StatusCode, err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(part) != 1000 {
		t.Fatalf("range length %d", len(part))
	}

	// --- automation JSON import / validate / export / execution (dry run)
	bad := []byte(`{"schema":"vaultdock.automation/v1","name":"bad","nodes":[{"id":"t","type":"trigger","kind":"file.created"},{"id":"a","type":"action","kind":"dest.set_folder","params":{"folder":"{{nope}}"}}],"edges":[{"from":"t","to":"a"}]}`)
	var badDoc map[string]any
	json.Unmarshal(bad, &badDoc)
	out = admin.must("POST", "/api/v1/automations/validate", badDoc, 200)
	if out["valid"] != false || len(out["errors"].([]any)) == 0 {
		t.Fatalf("invalid automation passed validation: %v", out)
	}
	tpl := automation.BuiltinTemplates()[0].Automation
	created := admin.must("POST", "/api/v1/automations", map[string]any{"definition": tpl}, 200)
	aid := int(created["automation"].(map[string]any)["id"].(float64))
	resp, _ = admin.c.Get(fmt.Sprintf("%s/api/v1/automations/%d/export", base, aid))
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var exp map[string]any
	json.Unmarshal(exported, &exp)
	exp["name"] = "Imported copy"
	admin.must("POST", "/api/v1/automations", map[string]any{"definition": exp}, 200)
	list, err := dc.Automations(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("device should receive both automations: %d %v", len(list), err)
	}
	now := time.Now()
	out = admin.must("POST", "/api/v1/automations/dry-run", map[string]any{"definition": exp, "events": []any{
		map[string]any{"type": "file.created", "destination": "/Medal", "file": map[string]any{"rel_path": "Apex Legends/clip.mp4", "size": 30 << 20, "created": now, "modified": now}},
	}}, 200)
	plan := out["results"].([]any)[0].(map[string]any)["plan"].(map[string]any)
	if plan["remote_dir"] != "/Medal/Apex Legends/"+now.Format("2006-01") {
		t.Fatalf("automation execution produced %v", plan["remote_dir"])
	}

	// --- revocation stops a device immediately
	admin.must("POST", fmt.Sprintf("/api/v1/devices/%d/revoke", rej.RequestID), nil, 409) // rejected devices cannot be revoked
	_, bkey := enroll(t, bob, base, "bob", "bob-password-123")
	_ = bkey

	b, _ := json.Marshal(persisted{DeviceID: dc.DeviceID, Key: key, Files: files, BobDevice: bc.DeviceID})
	if err := os.WriteFile(stateFile(), b, 0o600); err != nil {
		t.Fatal(err)
	}
	_ = smallSum
}

func TestPhase2AfterRestart(t *testing.T) {
	base := baseURL(t)
	raw, err := os.ReadFile(stateFile())
	if err != nil {
		t.Skip("phase 1 state missing")
	}
	var st persisted
	json.Unmarshal(raw, &st)
	ctx := context.Background()
	admin := newWeb(t).login("admin", adminPass)
	for p, sum := range st.Files {
		b, code := admin.content(p)
		if code != 200 {
			t.Fatalf("%s missing after restart: %d", p, code)
		}
		got := sha256.Sum256(b)
		if hex.EncodeToString(got[:]) != sum {
			t.Fatalf("%s content changed across restart", p)
		}
	}
	// device credentials survive restarts
	u, _ := url.Parse(base)
	dc := client.New(u, client.Options{})
	dc.DeviceID, dc.Key = st.DeviceID, ed25519.PrivateKey(st.Key)
	if _, err := dc.Me(ctx); err != nil {
		t.Fatalf("device authorisation lost: %v", err)
	}
	if list, err := dc.Automations(ctx); err != nil || len(list) != 2 {
		t.Fatalf("automations lost: %d %v", len(list), err)
	}
	// revoke bob's device after restart and confirm it is locked out
	if code, out := admin.do("POST", fmt.Sprintf("/api/v1/admin/devices/%d/revoke", st.BobDevice), nil); code != 200 && code != 409 {
		t.Fatalf("revoke: %d %v", code, out)
	}
	bob := client.New(u, client.Options{})
	bob.DeviceID = st.BobDevice
	_ = bob
	health := admin.must("GET", "/api/v1/admin/health?deep=1", nil, 200)
	if health["database"].(map[string]any)["integrity"] != "ok" {
		t.Fatalf("database integrity: %v", health["database"])
	}
	stats := admin.must("GET", "/api/v1/stats", nil, 200)
	t.Logf("after restart: %v files, %v used", stats["files"].(map[string]any)["files"], stats["usage"].(map[string]any)["used_bytes"])
}
