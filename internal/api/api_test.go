package api_test

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
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/automation"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

const setupToken = "test-setup-token-123"
const adminPass = "correct horse battery"

type env struct {
	t       *testing.T
	dir     string
	ts      *httptest.Server
	st      *store.Store
	srv     *api.Server
	storage string
}

func newEnv(t *testing.T, dir string) *env {
	t.Helper()
	if dir == "" {
		dir = t.TempDir()
	}
	storageDir := filepath.Join(dir, "storage")
	os.Setenv("VAULTDOCK_STATE_DIR", filepath.Join(dir, "state"))
	os.Setenv("VAULTDOCK_DEFAULT_STORAGE", filepath.Join(storageDir, "default"))
	os.Setenv("VAULTDOCK_STORAGE_ROOTS", storageDir)
	os.Setenv("VAULTDOCK_SETUP_TOKEN", setupToken)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(cfg.StateDir, "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DefaultLocation(ctx); err != nil {
		os.MkdirAll(cfg.DefaultStorage, 0o755)
		if err := storage.PrepareLocation(cfg.DefaultStorage); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateLocation(ctx, &store.Location{Name: "Default", Path: cfg.DefaultStorage, Enabled: true, IsDefault: true}); err != nil {
			t.Fatal(err)
		}
	}
	settings, err := api.LoadSettings(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	geo, _ := api.OpenGeo("")
	blobs := storage.New(st)
	blobs.GlobalMinFree = 0
	if err := blobs.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	srv := api.New(cfg, st, blobs, settings, geo)
	srv.RecoverAfterRestart(ctx)
	ts := httptest.NewServer(srv.Handler(nil))
	e := &env{t: t, dir: dir, ts: ts, st: st, srv: srv, storage: storageDir}
	t.Cleanup(func() { e.close() })
	return e
}

func (e *env) close() {
	if e.ts != nil {
		e.ts.Close()
		e.st.Close()
		e.ts = nil
	}
}

// web is a browser-like session client.
type web struct {
	t    *testing.T
	base string
	c    *http.Client
	csrf string
}

func (e *env) web() *web {
	jar, _ := cookiejar.New(nil)
	return &web{t: e.t, base: e.ts.URL, c: &http.Client{Jar: jar}}
}

func (w *web) req(method, path string, body any) (int, map[string]any) {
	w.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, w.base+path, rdr)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
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
		if c, ok := u["csrf_token"].(string); ok && c != "" {
			w.csrf = c
		}
	}
	return resp.StatusCode, out
}

func (w *web) must(method, path string, body any, want int) map[string]any {
	w.t.Helper()
	code, out := w.req(method, path, body)
	if code != want {
		w.t.Fatalf("%s %s: status %d want %d: %v", method, path, code, want, out)
	}
	return out
}

func errCode(out map[string]any) string {
	e, _ := out["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func (e *env) setupAdmin() *web {
	w := e.web()
	code, out := w.req("POST", "/api/v1/setup", map[string]any{"username": "admin", "password": adminPass, "setup_token": "wrong"})
	if code != 403 {
		e.t.Fatalf("setup with wrong token: %d %v", code, out)
	}
	w.must("POST", "/api/v1/setup", map[string]any{"username": "admin", "email": "admin@example.test", "password": adminPass, "setup_token": setupToken}, 200)
	return w
}

func (e *env) createUser(admin *web, name string, quota int64) *web {
	admin.must("POST", "/api/v1/admin/users", map[string]any{"username": name, "password": "password-" + name, "quota_bytes": quota}, 201)
	w := e.web()
	w.must("POST", "/api/v1/auth/login", map[string]any{"login": name, "password": "password-" + name}, 200)
	return w
}

// enrollDevice runs the full device authorisation flow and returns a
// signed client.
func (e *env) enrollDevice(w *web, login, password string) *client.Client {
	e.t.Helper()
	base, _ := url.Parse(e.ts.URL)
	c := client.New(base, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	ctx := context.Background()
	res, err := c.Enroll(ctx, client.EnrollRequest{Login: login, Password: password, DeviceName: "TEST-PC", OS: "Windows 11", Identifier: "id-" + login, PublicKey: protocol.EncodePublicKey(pub), ClientVersion: "test"})
	if err != nil {
		e.t.Fatalf("enroll: %v", err)
	}
	st, err := c.EnrollStatus(ctx, res.RequestID, res.PollToken)
	if err != nil || st.Status != "pending" {
		e.t.Fatalf("expected pending: %v %v", st, err)
	}
	devs := w.must("GET", "/api/v1/devices", nil, 200)["devices"].([]any)
	found := false
	for _, d := range devs {
		dm := d.(map[string]any)
		if int64(dm["id"].(float64)) == res.RequestID {
			found = true
			if dm["user_code"] != res.UserCode || dm["os"] != "Windows 11" || dm["request_ip"] == "" {
				e.t.Fatalf("device request details missing: %v", dm)
			}
		}
	}
	if !found {
		e.t.Fatal("pending device not listed")
	}
	w.must("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, 200)
	st, err = c.EnrollStatus(ctx, res.RequestID, res.PollToken)
	if err != nil || st.Status != "approved" || st.DeviceID != res.RequestID {
		e.t.Fatalf("expected approved: %+v %v", st, err)
	}
	c.DeviceID = st.DeviceID
	c.Key = priv
	return c
}

func TestSetupAuthAndCSRF(t *testing.T) {
	e := newEnv(t, "")
	w := e.web()
	out := w.must("GET", "/api/v1/setup/status", nil, 200)
	if out["needs_setup"] != true {
		t.Fatal("expected needs_setup")
	}
	admin := e.setupAdmin()
	code, _ := e.web().req("POST", "/api/v1/setup", map[string]any{"username": "evil", "password": "aaaaaaaaaaaa", "setup_token": setupToken})
	if code != 409 {
		t.Fatalf("second setup should fail, got %d", code)
	}
	anon := e.web()
	code, out = anon.req("POST", "/api/v1/auth/login", map[string]any{"login": "admin", "password": "nope-nope-nope"})
	if code != 401 || errCode(out) != "invalid_credentials" {
		t.Fatalf("bad login: %d %v", code, out)
	}
	code, out = anon.req("POST", "/api/v1/auth/login", map[string]any{"login": "ghost", "password": "nope-nope-nope"})
	if code != 401 || errCode(out) != "invalid_credentials" {
		t.Fatalf("unknown user login must look identical: %d %v", code, out)
	}
	code, _ = anon.req("GET", "/api/v1/files", nil)
	if code != 401 {
		t.Fatalf("unauthenticated list: %d", code)
	}
	admin.must("GET", "/api/v1/auth/me", nil, 200)
	saved := admin.csrf
	admin.csrf = ""
	code, out = admin.req("POST", "/api/v1/files/folder", map[string]any{"name": "x"})
	if code != 403 || errCode(out) != "csrf" {
		t.Fatalf("missing csrf should be rejected: %d %v", code, out)
	}
	admin.csrf = saved
	admin.must("POST", "/api/v1/files/folder", map[string]any{"name": "x"}, 201)
	// registration is disabled by default
	code, out = anon.req("POST", "/api/v1/auth/register", map[string]any{"username": "bob", "password": "aaaaaaaaaaaa"})
	if code != 403 {
		t.Fatalf("registration should be closed: %d %v", code, out)
	}
	// non-admin cannot reach admin API
	u := e.createUser(admin, "alice", 0)
	code, _ = u.req("GET", "/api/v1/admin/users", nil)
	if code != 403 {
		t.Fatalf("non-admin admin access: %d", code)
	}
	// disabling a user ends their sessions
	users := admin.must("GET", "/api/v1/admin/users", nil, 200)["users"].([]any)
	var aliceID float64
	for _, x := range users {
		if x.(map[string]any)["username"] == "alice" {
			aliceID = x.(map[string]any)["id"].(float64)
		}
	}
	admin.must("PATCH", fmt.Sprintf("/api/v1/admin/users/%d", int(aliceID)), map[string]any{"disabled": true}, 200)
	code, _ = u.req("GET", "/api/v1/auth/me", nil)
	if code != 401 {
		t.Fatalf("disabled user session should be invalid: %d", code)
	}
	code, out = e.web().req("POST", "/api/v1/auth/login", map[string]any{"login": "alice", "password": "password-alice"})
	if code != 403 || errCode(out) != "account_disabled" {
		t.Fatalf("disabled login: %d %v", code, out)
	}
	w.must("POST", "/api/v1/auth/logout", nil, 401)
	admin.must("POST", "/api/v1/auth/logout", nil, 200)
	code, _ = admin.req("GET", "/api/v1/auth/me", nil)
	if code != 401 {
		t.Fatalf("after logout: %d", code)
	}
}

func TestDeviceApprovalRejectionReplayRevocation(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	base, _ := url.Parse(e.ts.URL)
	ctx := context.Background()

	// invalid credentials
	c := client.New(base, client.Options{})
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	_, err := c.Enroll(ctx, client.EnrollRequest{Login: "alice", Password: "wrong-password", DeviceName: "PC", OS: "Windows", Identifier: "x", PublicKey: protocol.EncodePublicKey(pub)})
	if !client.IsCode(err, "invalid_credentials") {
		t.Fatalf("expected invalid credentials, got %v", err)
	}

	res, err := c.Enroll(ctx, client.EnrollRequest{Login: "alice", Password: "password-alice", DeviceName: "PC", OS: "Windows", Identifier: "x", PublicKey: protocol.EncodePublicKey(pub)})
	if err != nil {
		t.Fatal(err)
	}
	c.DeviceID, c.Key = res.RequestID, priv
	if _, err := c.Me(ctx); !client.IsCode(err, "device_pending") {
		t.Fatalf("pending device must not access the API: %v", err)
	}
	// another user cannot approve it
	code, _ := bob.req("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil)
	if code != 404 {
		t.Fatalf("cross-user approval: %d", code)
	}
	// a device cannot approve itself (session only)
	if err := c.Call(ctx, "POST", fmt.Sprintf("/api/v1/devices/%d/approve", res.RequestID), nil, nil); err == nil {
		t.Fatal("device approved itself")
	}
	alice.must("POST", fmt.Sprintf("/api/v1/devices/%d/reject", res.RequestID), nil, 200)
	st, err := c.EnrollStatus(ctx, res.RequestID, res.PollToken)
	if err != nil || st.Status != "rejected" {
		t.Fatalf("expected rejected: %+v %v", st, err)
	}
	if _, err := c.Me(ctx); err == nil {
		t.Fatal("rejected device accessed API")
	}

	// expiry
	res2, err := c.Enroll(ctx, client.EnrollRequest{Login: "alice", Password: "password-alice", DeviceName: "PC2", OS: "Windows", Identifier: "y", PublicKey: protocol.EncodePublicKey(pub)})
	if err != nil {
		t.Fatal(err)
	}
	e.st.DB.Exec("UPDATE devices SET expires_at = ? WHERE id = ?", time.Now().Add(-time.Minute).UnixMilli(), res2.RequestID)
	code, out := alice.req("POST", fmt.Sprintf("/api/v1/devices/%d/approve", res2.RequestID), nil)
	if code != 409 || errCode(out) != "not_pending" {
		t.Fatalf("expired approval: %d %v", code, out)
	}
	st, err = c.EnrollStatus(ctx, res2.RequestID, res2.PollToken)
	if err != nil || st.Status != "expired" {
		t.Fatalf("expected expired: %+v %v", st, err)
	}

	// approved device
	dc := e.enrollDevice(alice, "alice", "password-alice")
	me, err := dc.Me(ctx)
	if err != nil || me.User.Username != "alice" {
		t.Fatalf("me: %+v %v", me, err)
	}

	// replay: send the exact same signed request twice
	body := []byte(`{"path":"/replay"}`)
	h, _ := protocol.Sign(dc.Key, strconv.FormatInt(dc.DeviceID, 10), "POST", "/api/v1/files/ensure-folder", protocol.SHA256Hex(body), time.Now())
	send := func(b []byte) (int, string) {
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/files/ensure-folder", bytes.NewReader(b))
		req.Header.Set(protocol.HeaderDevice, h.DeviceID)
		req.Header.Set(protocol.HeaderTimestamp, h.Timestamp)
		req.Header.Set(protocol.HeaderNonce, h.Nonce)
		req.Header.Set(protocol.HeaderBodySHA256, h.BodySHA256)
		req.Header.Set(protocol.HeaderSignature, h.Signature)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		var m map[string]any
		json.Unmarshal(raw, &m)
		return resp.StatusCode, errCode(m)
	}
	if code, _ := send(body); code != 200 {
		t.Fatalf("first signed request: %d", code)
	}
	if code, ec := send(body); code != 401 || ec != "replay" {
		t.Fatalf("replayed request must fail: %d %s", code, ec)
	}
	// tampered body with a valid signature for another body
	h, _ = protocol.Sign(dc.Key, strconv.FormatInt(dc.DeviceID, 10), "POST", "/api/v1/files/ensure-folder", protocol.SHA256Hex(body), time.Now())
	if code, ec := send([]byte(`{"path":"/tampered"}`)); code != 400 || ec != "body_mismatch" {
		t.Fatalf("tampered body: %d %s", code, ec)
	}
	// stale timestamp
	h, _ = protocol.Sign(dc.Key, strconv.FormatInt(dc.DeviceID, 10), "POST", "/api/v1/files/ensure-folder", protocol.SHA256Hex(body), time.Now().Add(-10*time.Minute))
	if code, ec := send(body); code != 401 || ec != "clock_skew" {
		t.Fatalf("stale request: %d %s", code, ec)
	}
	// wrong key
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	h, _ = protocol.Sign(other, strconv.FormatInt(dc.DeviceID, 10), "POST", "/api/v1/files/ensure-folder", protocol.SHA256Hex(body), time.Now())
	if code, ec := send(body); code != 401 || ec != "bad_signature" {
		t.Fatalf("forged signature: %d %s", code, ec)
	}

	// revocation takes effect immediately
	alice.must("POST", fmt.Sprintf("/api/v1/devices/%d/revoke", dc.DeviceID), nil, 200)
	if _, err := dc.Me(ctx); !client.IsCode(err, "device_revoked") {
		t.Fatalf("revoked device: %v", err)
	}
}

func makeFile(t *testing.T, dir, name string, size int) (string, []byte) {
	t.Helper()
	b := make([]byte, size)
	rand.Read(b)
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, b
}

func nodeID(t *testing.T, raw json.RawMessage) int64 {
	var n struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(raw, &n); err != nil || n.ID == 0 {
		t.Fatalf("bad node %s", raw)
	}
	return n.ID
}

func TestUploadsResumeDedupRangeAndIntegrity(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	tmp := t.TempDir()

	// 20 MiB + 123 bytes: three 8 MiB chunks, last one partial
	big, data := makeFile(t, tmp, "clip.mp4", 20<<20+123)
	res, err := dc.UploadFile(ctx, big, "/Medal/Valorant/clip.mp4", []string{"clips"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" || res.Result != "created" {
		t.Fatalf("upload result %+v", res)
	}
	id := nodeID(t, res.Node)

	// full download through the web session
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/files/%d/content", e.ts.URL, id), nil)
	resp, err := alice.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded content differs")
	}
	if resp.Header.Get("Content-Type") != "video/mp4" || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("headers: %v", resp.Header)
	}
	// range request (video seeking)
	req, _ = http.NewRequest("GET", fmt.Sprintf("%s/api/v1/files/%d/content", e.ts.URL, id), nil)
	req.Header.Set("Range", "bytes=10000000-10000099")
	resp, err = alice.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 206 || !bytes.Equal(part, data[10000000:10000100]) {
		t.Fatalf("range: %d len %d", resp.StatusCode, len(part))
	}

	// identical content at another path is deduplicated (no data sent)
	res, err = dc.UploadFile(ctx, big, "/Backup/clip-copy.mp4", nil)
	if err != nil || res.Result != "deduplicated" {
		t.Fatalf("dedup: %+v %v", res, err)
	}
	// identical content at the same path is unchanged
	res, err = dc.UploadFile(ctx, big, "/Medal/Valorant/clip.mp4", nil)
	if err != nil || res.Result != "unchanged" {
		t.Fatalf("unchanged: %+v %v", res, err)
	}
	var blobCount int
	e.st.DB.QueryRow("SELECT COUNT(*) FROM blobs").Scan(&blobCount)
	if blobCount != 1 {
		t.Fatalf("expected one physical blob, got %d", blobCount)
	}

	// interrupted upload: send one chunk, then resume from a "new process"
	p2, data2 := makeFile(t, tmp, "second.mkv", 17<<20)
	sum, _ := client.HashFile(p2)
	cr, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Medal/second.mkv", Size: int64(len(data2)), SHA256: sum, ModifiedAt: time.Now().UnixMilli()})
	if err != nil || cr.Status != "open" {
		t.Fatalf("create: %+v %v", cr, err)
	}
	f, _ := os.Open(p2)
	defer f.Close()
	if err := dc.PutChunk(ctx, cr.Upload.ID, 1, f, cr.Upload.ChunkSize, cr.Upload.ChunkLen(1)); err != nil {
		t.Fatal(err)
	}
	if _, err := dc.CompleteUpload(ctx, cr.Upload.ID); !client.IsCode(err, "incomplete") {
		t.Fatalf("complete before all chunks: %v", err)
	}
	cr2, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Medal/second.mkv", Size: int64(len(data2)), SHA256: sum, ModifiedAt: time.Now().UnixMilli()})
	if err != nil || !cr2.Resumed || cr2.Upload.ID != cr.Upload.ID {
		t.Fatalf("expected resumed session: %+v %v", cr2, err)
	}
	if m := cr2.Upload.Missing(); len(m) != 2 || m[0] != 0 || m[1] != 2 {
		t.Fatalf("missing chunks %v", m)
	}
	// a corrupted chunk (body does not match declared hash) is rejected
	bad := bytes.Repeat([]byte{1}, int(cr2.Upload.ChunkLen(0)))
	err = dc.PutChunkHashed(ctx, cr2.Upload.ID, 0, bytes.NewReader(bad), 0, int64(len(bad)), protocol.SHA256Hex(data2[:cr2.Upload.ChunkLen(0)]))
	if !client.IsCode(err, "chunk_hash_mismatch") {
		t.Fatalf("corrupt chunk: %v", err)
	}
	for _, idx := range cr2.Upload.Missing() {
		if err := dc.PutChunk(ctx, cr2.Upload.ID, idx, f, idx*cr2.Upload.ChunkSize, cr2.Upload.ChunkLen(idx)); err != nil {
			t.Fatal(err)
		}
	}
	done, err := dc.CompleteUpload(ctx, cr2.Upload.ID)
	if err != nil || done.Result != "created" {
		t.Fatalf("complete: %+v %v", done, err)
	}

	// file changed during upload: declared hash no longer matches
	p3, _ := makeFile(t, tmp, "changing.mp4", 3<<20)
	sum3, _ := client.HashFile(p3)
	cr3, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/changing.mp4", Size: 3 << 20, SHA256: sum3})
	if err != nil {
		t.Fatal(err)
	}
	makeFile(t, tmp, "changing.mp4", 3<<20) // rewritten with new content
	f3, _ := os.Open(p3)
	if err := dc.PutChunk(ctx, cr3.Upload.ID, 0, f3, 0, 3<<20); err != nil {
		t.Fatal(err)
	}
	f3.Close()
	if _, err := dc.CompleteUpload(ctx, cr3.Upload.ID); !client.IsCode(err, "hash_mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}

	// replacing a file keeps the previous version in trash
	p4 := filepath.Join(tmp, "clip.mp4")
	os.WriteFile(p4, []byte("new content"), 0o644)
	res, err = dc.UploadFile(ctx, p4, "/Medal/Valorant/clip.mp4", nil)
	if err != nil || res.Result != "replaced" {
		t.Fatalf("replace: %+v %v", res, err)
	}
	trash := alice.must("GET", "/api/v1/trash", nil, 200)["items"].([]any)
	if len(trash) != 1 {
		t.Fatalf("expected replaced version in trash, got %d", len(trash))
	}

	// empty file
	p5 := filepath.Join(tmp, "empty.txt")
	os.WriteFile(p5, nil, 0o644)
	if res, err := dc.UploadFile(ctx, p5, "/empty.txt", nil); err != nil || res.Status != "complete" {
		t.Fatalf("empty file: %+v %v", res, err)
	}

	// web upload without hash (browser path) in one chunk
	w := alice
	out := w.must("POST", "/api/v1/uploads", map[string]any{"name": "note.txt", "size": 5}, 201)
	up := out["upload"].(map[string]any)
	req, _ = http.NewRequest("PUT", e.ts.URL+"/api/v1/uploads/"+up["id"].(string)+"/chunks/0", strings.NewReader("hello"))
	req.Header.Set("X-CSRF-Token", w.csrf)
	resp, _ = w.c.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("web chunk: %d", resp.StatusCode)
	}
	out = w.must("POST", "/api/v1/uploads/"+up["id"].(string)+"/complete", nil, 200)
	if out["result"] != "created" {
		t.Fatalf("web upload: %v", out)
	}
	// HTML is never served inline
	out = w.must("POST", "/api/v1/uploads", map[string]any{"name": "evil.html", "size": 20}, 201)
	up = out["upload"].(map[string]any)
	req, _ = http.NewRequest("PUT", e.ts.URL+"/api/v1/uploads/"+up["id"].(string)+"/chunks/0", strings.NewReader("<script>x()</script>"))
	req.Header.Set("X-CSRF-Token", w.csrf)
	resp, _ = w.c.Do(req)
	resp.Body.Close()
	out = w.must("POST", "/api/v1/uploads/"+up["id"].(string)+"/complete", nil, 200)
	hid := int64(out["node"].(map[string]any)["id"].(float64))
	resp, _ = w.c.Get(fmt.Sprintf("%s/api/v1/files/%d/content", e.ts.URL, hid))
	resp.Body.Close()
	if resp.Header.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("html served unsafely: %v", resp.Header)
	}
}

func TestQuotaAndIsolation(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 2<<20) // 2 MiB quota
	ad := e.enrollDevice(alice, "alice", "password-alice")
	bd := e.enrollDevice(bob, "bob", "password-bob")
	ctx := context.Background()
	tmp := t.TempDir()

	small, _ := makeFile(t, tmp, "a.bin", 1<<20)
	if _, err := bd.UploadFile(ctx, small, "/a.bin", nil); err != nil {
		t.Fatal(err)
	}
	big, _ := makeFile(t, tmp, "b.bin", 3<<20/2)
	if _, err := bd.UploadFile(ctx, big, "/b.bin", nil); !client.IsCode(err, "quota_exceeded") {
		t.Fatalf("expected quota error, got %v", err)
	}

	secret, _ := makeFile(t, tmp, "secret.txt", 1000)
	res, err := ad.UploadFile(ctx, secret, "/private/secret.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeID(t, res.Node)
	for _, p := range []string{
		fmt.Sprintf("/api/v1/files/%d", id),
		fmt.Sprintf("/api/v1/files/%d/content", id),
		fmt.Sprintf("/api/v1/files?parent=%d", id),
		fmt.Sprintf("/api/v1/files/zip?ids=%d", id),
	} {
		if code, _ := bob.req("GET", p, nil); code != 404 {
			t.Errorf("bob accessed %s: %d", p, code)
		}
	}
	if code, _ := bob.req("PATCH", fmt.Sprintf("/api/v1/files/%d", id), map[string]any{"name": "pwned"}); code != 404 {
		t.Errorf("bob renamed alice's file: %d", code)
	}
	if code, _ := bob.req("DELETE", fmt.Sprintf("/api/v1/files/%d", id), nil); code != 404 {
		t.Errorf("bob deleted alice's file: %d", code)
	}
	// moving into another user's folder
	bobFolder := bob.must("POST", "/api/v1/files/folder", map[string]any{"name": "mine"}, 201)["node"].(map[string]any)
	if code, _ := alice.req("PATCH", fmt.Sprintf("/api/v1/files/%d", id), map[string]any{"parent_id": bobFolder["id"]}); code != 404 {
		t.Errorf("alice moved into bob's folder: %d", code)
	}
	// device of bob cannot read alice's upload sessions
	cr, err := ad.CreateUpload(ctx, client.UploadRequest{Path: "/x.bin", Size: 10, SHA256: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bd.GetUpload(ctx, cr.Upload.ID); !client.IsCode(err, "not_found") {
		t.Errorf("bob read alice's upload: %v", err)
	}
	// path traversal and invalid names
	for _, p := range []string{"/../../etc/passwd", "/a/../../b.txt", "/a/./b.txt", `/a\..\b.txt`, "/con.txt", "/bad:name.txt"} {
		if _, err := ad.CreateUpload(ctx, client.UploadRequest{Path: p, Size: 1, SHA256: strings.Repeat("b", 64)}); !client.IsCode(err, "invalid_path") {
			t.Errorf("path %q accepted: %v", p, err)
		}
	}
	if code, _ := alice.req("POST", "/api/v1/files/folder", map[string]any{"name": ".."}); code != 400 {
		t.Errorf("'..' folder accepted: %d", code)
	}
	// blobs live under numeric user directories only
	filepath.WalkDir(e.storage, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.Contains(p, "secret") {
			t.Errorf("client file name reached disk: %s", p)
		}
		return nil
	})
}

func TestAutomationsAPI(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	dc := e.enrollDevice(admin, "admin", adminPass)
	ctx := context.Background()

	ref := admin.must("GET", "/api/v1/automations/reference", nil, 200)
	if ref["schema_id"] != automation.SchemaID || ref["ai_guide"] == "" {
		t.Fatal("reference incomplete")
	}
	code, out := admin.req("POST", "/api/v1/automations/validate", map[string]any{"schema": "nope"})
	if code != 200 || out["valid"] != false {
		t.Fatalf("validate: %v", out)
	}
	tpl := automation.BuiltinTemplates()[0].Automation
	out = admin.must("POST", "/api/v1/automations", map[string]any{"definition": tpl}, 200)
	aid := int(out["automation"].(map[string]any)["id"].(float64))
	if out["automation"].(map[string]any)["enabled"] != true {
		t.Fatal("expected enabled")
	}
	// invalid definitions cannot be enabled but can be saved as drafts
	bad := map[string]any{"schema": automation.SchemaID, "name": "broken", "enabled": true, "nodes": []any{map[string]any{"id": "t", "type": "trigger", "kind": "file.created"}}, "edges": []any{}}
	code, out = admin.req("POST", "/api/v1/automations", map[string]any{"definition": bad})
	if code != 422 {
		t.Fatalf("invalid enabled automation saved: %d %v", code, out)
	}
	bad["enabled"] = false
	admin.must("POST", "/api/v1/automations", map[string]any{"definition": bad}, 200)

	// device receives only valid, enabled automations
	list, err := dc.Automations(ctx)
	if err != nil || len(list) != 1 || list[0].ID != int64(aid) {
		t.Fatalf("device automations: %+v %v", list, err)
	}
	// export round trip
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/automations/%d/export", e.ts.URL, aid), nil)
	resp, _ := admin.c.Do(req)
	exported, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if _, res := automation.Parse(exported); !res.Valid() {
		t.Fatalf("export does not re-import: %v", res.Error())
	}
	// dry run
	now := time.Now()
	out = admin.must("POST", "/api/v1/automations/dry-run", map[string]any{
		"definition": tpl,
		"events": []any{
			map[string]any{"type": "file.created", "destination": "/Medal", "file": map[string]any{"rel_path": "Valorant/clip.mp4", "size": 50 << 20, "created": now, "modified": now}},
			map[string]any{"type": "file.created", "destination": "/Medal", "file": map[string]any{"rel_path": "Valorant/clip.tmp", "size": 50 << 20, "created": now, "modified": now}},
		},
	}, 200)
	results := out["results"].([]any)
	p0 := results[0].(map[string]any)["plan"].(map[string]any)
	p1 := results[1].(map[string]any)["plan"].(map[string]any)
	if p0["decision"] != "sync" || !strings.HasPrefix(p0["remote_dir"].(string), "/Medal/Valorant/") || p1["decision"] != "skip" {
		t.Fatalf("dry run: %v / %v", p0, p1)
	}
}

func TestTrashRestorePurgeAndPersistence(t *testing.T) {
	dir := t.TempDir()
	e := newEnv(t, dir)
	admin := e.setupAdmin()
	dc := e.enrollDevice(admin, "admin", adminPass)
	ctx := context.Background()
	tmp := t.TempDir()
	p, data := makeFile(t, tmp, "keep.bin", 9<<20)
	res, err := dc.UploadFile(ctx, p, "/Docs/keep.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	keepID := nodeID(t, res.Node)
	p2, _ := makeFile(t, tmp, "gone.bin", 1<<20)
	res, err = dc.UploadFile(ctx, p2, "/Docs/Sub/gone.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	folder := admin.must("GET", "/api/v1/files/resolve?path=/Docs/Sub", nil, 200)["node"].(map[string]any)
	fid := int64(folder["id"].(float64))
	admin.must("DELETE", fmt.Sprintf("/api/v1/files/%d", fid), nil, 200)
	if code, _ := admin.req("GET", "/api/v1/files/resolve?path=/Docs/Sub/gone.bin", nil); code != 404 {
		t.Fatal("trashed file still visible")
	}
	admin.must("POST", fmt.Sprintf("/api/v1/trash/%d/restore", fid), nil, 200)
	admin.must("GET", "/api/v1/files/resolve?path=/Docs/Sub/gone.bin", nil, 200)
	admin.must("DELETE", fmt.Sprintf("/api/v1/files/%d", fid), nil, 200)
	countBlobs := func() int {
		n := 0
		filepath.WalkDir(filepath.Join(e.storage, "default", "blobs"), func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				n++
			}
			return nil
		})
		return n
	}
	before := countBlobs()
	admin.must("DELETE", "/api/v1/trash", nil, 200)
	if after := countBlobs(); after != before-1 {
		t.Fatalf("purge should remove exactly one blob file: %d -> %d", before, after)
	}
	stats := admin.must("GET", "/api/v1/stats", nil, 200)
	used := stats["usage"].(map[string]any)["used_bytes"].(float64)
	if int(used) != len(data) {
		t.Fatalf("usage after purge: %v", used)
	}

	// restart: close everything and reopen on the same directories
	e.close()
	e2 := newEnv(t, dir)
	w := e2.web()
	w.must("POST", "/api/v1/auth/login", map[string]any{"login": "admin", "password": adminPass}, 200)
	resp, err := w.c.Get(fmt.Sprintf("%s/api/v1/files/%d/content", e2.ts.URL, keepID))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("content changed across restart")
	}
	devs := w.must("GET", "/api/v1/devices", nil, 200)["devices"].([]any)
	if len(devs) != 1 || devs[0].(map[string]any)["status"] != "approved" {
		t.Fatalf("device authorisation lost across restart: %v", devs)
	}
	// device keeps working with a new client after restart (nonces from
	// before the restart are rejected by the start-time guard)
	base, _ := url.Parse(e2.ts.URL)
	dc2 := client.New(base, client.Options{})
	dc2.DeviceID, dc2.Key = dc.DeviceID, dc.Key
	if _, err := dc2.Me(ctx); err != nil {
		t.Fatalf("device after restart: %v", err)
	}
}

func TestLowDiskSpaceRefusesUploads(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	locs := admin.must("GET", "/api/v1/admin/storage", nil, 200)["locations"].([]any)
	l := locs[0].(map[string]any)
	// a reserve larger than any disk forces the low-space path
	admin.must("PATCH", fmt.Sprintf("/api/v1/admin/storage/%d", int(l["id"].(float64))), map[string]any{"reserve_bytes": int64(1) << 60}, 200)
	code, out := admin.req("POST", "/api/v1/uploads", map[string]any{"name": "x.bin", "size": 10})
	if code != 507 || errCode(out) != "insufficient_storage" {
		t.Fatalf("expected 507, got %d %v", code, out)
	}
	// storage locations outside the allowed roots are rejected
	code, _ = admin.req("POST", "/api/v1/admin/storage", map[string]any{"name": "etc", "path": "/etc"})
	if code != 400 {
		t.Fatalf("location outside roots accepted: %d", code)
	}
	os.MkdirAll(filepath.Join(e.storage, "hdd2"), 0o755)
	admin.must("POST", "/api/v1/admin/storage", map[string]any{"name": "HDD 2", "path": filepath.Join(e.storage, "hdd2")}, 201)
}

func TestAdminUpdateStatus(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/ASPEC-RACING-PTY-LTD/vad-server/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v9.1.0","html_url":"https://github.com/ASPEC-RACING-PTY-LTD/vad-server/releases/tag/v9.1.0","body":"Faster sync.","published_at":"2026-10-01T00:00:00Z","assets":[]}`))
	}))
	defer gh.Close()
	prev := api.Version
	api.Version = "9.0.2"
	defer func() { api.Version = prev }()

	e := newEnv(t, "")
	e.srv.SetUpdateAPIBase(gh.URL)
	admin := e.setupAdmin()

	out := admin.must("GET", "/api/v1/admin/update", nil, 200)
	if out["available"] != false || out["enabled"] != true || out["auto_update"] != "off" {
		t.Fatalf("before check: %v", out)
	}
	out = admin.must("POST", "/api/v1/admin/update/check", nil, 200)
	if out["latest"] != "9.1.0" || out["available"] != true || out["notes"] != "Faster sync." || out["checked_at"] == nil {
		t.Fatalf("after check: %v", out)
	}
	if got := admin.must("GET", "/api/v1/admin/update", nil, 200); got["latest"] != "9.1.0" {
		t.Fatalf("status not kept: %v", got)
	}
	user := e.createUser(admin, "bob", 0)
	if code, _ := user.req("GET", "/api/v1/admin/update", nil); code != 403 {
		t.Fatalf("non-admin got %d", code)
	}
}
