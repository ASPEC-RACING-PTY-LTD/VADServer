package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeagent"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/replica"
)

// TestRemoteStorageNode pairs a real node agent (behind an HTTPS proxy, as
// behind a Cloudflare tunnel), moves a user's file to it, reads it back
// through the relay (ranges, seeking), detects tampering and deletes it.
func TestRemoteStorageNode(t *testing.T) {
	e := newEnv(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.RunRemote(ctx)
	admin := e.setupAdmin()

	// Pairing secret from the admin panel.
	pr := admin.must("POST", "/api/v1/admin/node-pairings", map[string]any{"name": "hater"}, 201)
	secret := pr["secret"].(string)
	if !strings.HasPrefix(secret, "vdp_") || !strings.Contains(pr["command"].(string), "install-node") || strings.Contains(pr["command"].(string), secret) {
		t.Fatalf("pairing: %v", pr)
	}

	// The node: an agent listening locally, published over HTTPS.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(&url.URL{Scheme: "http", Host: addr}))
	defer proxy.Close()
	e.srv.SetRemoteHTTP(proxy.Client())
	hdd := filepath.Join(t.TempDir(), "hdd")
	if err := os.MkdirAll(hdd, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := nodeagent.Config{Dir: t.TempDir(), ServerURL: e.ts.URL, Name: "hater", Listen: addr, PublicURL: proxy.URL, Proxy: "cloudflared",
		KeyFile: filepath.Join(t.TempDir(), "node.key"), StorageRoots: []string{hdd}}
	if err := os.MkdirAll(cfg.DataDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.DataDir(), "pairing"), []byte(secret), 0o600); err != nil {
		t.Fatal(err)
	}
	go func() { _ = nodeagent.New(cfg, slog.Default()).Run(ctx) }()

	// Paired without an approval click; its storage folder is set up.
	var nid int64
	waitFor(t, 60*time.Second, "node ready", func() bool {
		ps := admin.must("GET", "/api/v1/admin/node-pairings", nil, 200)["pairings"].([]any)
		if id, ok := ps[0].(map[string]any)["node_id"].(float64); ok {
			nid = int64(id)
			rem := admin.must("GET", fmt.Sprintf("/api/v1/admin/nodes/%d/remote", nid), nil, 200)
			return rem["ready"] == true
		}
		return false
	})
	if _, err := os.Stat(filepath.Join(cfg.DataDir(), "pairing")); !os.IsNotExist(err) {
		t.Fatal("pairing secret kept on the node after use")
	}
	// The secret is single use.
	if code, out := admin.req("POST", "/api/v1/node/join", map[string]any{"name": "again", "pairing": secret}); code != 201 {
		t.Fatalf("join: %d %v", code, out)
	}
	nodes := admin.must("GET", "/api/v1/admin/nodes", nil, 200)["items"].([]any)
	for _, n := range nodes {
		if m := n.(map[string]any); m["name"] == "again" && m["status"] != "pending" {
			t.Fatalf("reused pairing secret approved a node: %v", m)
		}
	}

	// A user whose files live on the node.
	bob := e.createUser(admin, "bob", 0)
	bc := e.enrollDevice(bob, "bob", "password-bob")
	tmp := t.TempDir()
	data := make([]byte, 6*replica.ChunkSize+777)
	rand.Read(data)
	p := filepath.Join(tmp, "clip.bin")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := bc.UploadFile(ctx, p, "/Clips/clip.bin", nil)
	if err != nil {
		t.Fatal(err)
	}
	fileID := nodeID(t, res.Node)
	me := bob.must("GET", "/api/v1/auth/me", nil, 200)
	uid := int64(me["user"].(map[string]any)["id"].(float64))
	admin.must("PUT", fmt.Sprintf("/api/v1/admin/users/%d/upload-origin", uid), map[string]any{"node_id": nid}, 200)
	admin.must("POST", fmt.Sprintf("/api/v1/admin/users/%d/move-to-node", uid), map[string]any{}, 202)
	waitFor(t, 60*time.Second, "file moved", func() bool {
		st := admin.must("GET", fmt.Sprintf("/api/v1/admin/nodes/%d/remote", nid), nil, 200)["stats"].(map[string]any)
		ok, _ := st["files"].(map[string]any)["ok"].(float64)
		return ok == 1
	})

	// The local file is gone; only ciphertext is on the node.
	var local []string
	_ = filepath.Walk(filepath.Join(e.storage, "default", "blobs"), func(path string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			local = append(local, path)
		}
		return nil
	})
	if len(local) != 0 {
		t.Fatalf("local copies left: %v", local)
	}
	var remoteFiles []string
	_ = filepath.Walk(filepath.Join(hdd, "remote"), func(path string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			remoteFiles = append(remoteFiles, path)
		}
		return nil
	})
	if len(remoteFiles) != 1 {
		t.Fatalf("remote files: %v", remoteFiles)
	}
	stored, _ := os.ReadFile(remoteFiles[0])
	if !bytes.HasPrefix(stored, []byte("VDR2")) || bytes.Contains(stored, data[:4096]) {
		t.Fatal("the node holds readable content")
	}

	// Reads go through the relay: whole file, ranges, seeking.
	get := func(rng string) (int, []byte) {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/files/%d/content", e.ts.URL, fileID), nil)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		resp, err := bob.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, b := get(""); code != 200 || !bytes.Equal(b, data) {
		t.Fatalf("whole file: %d, %d bytes, equal %v", code, len(b), bytes.Equal(b, data))
	}
	off := 5*replica.ChunkSize + 100
	if code, b := get(fmt.Sprintf("bytes=%d-%d", off, off+999)); code != 206 || !bytes.Equal(b, data[off:off+1000]) {
		t.Fatalf("range: %d", code)
	}

	// Nobody can read straight from the node without the server's signature.
	resp, err := proxy.Client().Get(proxy.URL + strings.TrimPrefix(remoteFiles[0], hdd))
	if err == nil {
		resp.Body.Close()
		if resp.StatusCode == 200 {
			t.Fatal("node served a file without a signature")
		}
	}
	nodeURL := fmt.Sprintf("%s/v1/remote/1/%d/%s", proxy.URL, uid, filepath.Base(strings.TrimSuffix(remoteFiles[0], ".vdr2")))
	if resp, err := proxy.Client().Get(nodeURL); err == nil {
		resp.Body.Close()
		if resp.StatusCode != 401 {
			t.Fatalf("unsigned node read: %d", resp.StatusCode)
		}
	}

	// Tampering on the node is detected (not served).
	h, err := replica.ParseV2Header(stored)
	if err != nil {
		t.Fatal(err)
	}
	c6, _, _ := h.ChunkSpan(6)
	stored[c6+10] ^= 0xff
	if err := os.WriteFile(remoteFiles[0], stored, 0o640); err != nil {
		t.Fatal(err)
	}
	e.srv.ClearRemoteCache()
	if code, b := get(fmt.Sprintf("bytes=%d-%d", 6*replica.ChunkSize, 6*replica.ChunkSize+10)); code != 503 || bytes.Contains(b, data[6*replica.ChunkSize:6*replica.ChunkSize+11]) {
		t.Fatalf("tampered chunk: %d %s", code, b)
	}

	// Deleting the file removes it from the node.
	bob.must("DELETE", fmt.Sprintf("/api/v1/files/%d", fileID), nil, 200)
	bob.must("DELETE", fmt.Sprintf("/api/v1/trash/%d", fileID), nil, 200)
	waitFor(t, 60*time.Second, "remote file deleted", func() bool {
		_, err := os.Stat(remoteFiles[0])
		return os.IsNotExist(err)
	})
}
