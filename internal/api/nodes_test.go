package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeagent"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeapi"
)

// The real agent joins, an admin approves it, the secret is issued once and
// stored encrypted, heartbeats arrive, updates are offered from this server
// only, and a revoked node is refused.
func TestStorageNodeJoinApproveHeartbeat(t *testing.T) {
	bins := t.TempDir()
	agentBin := []byte("#!/bin/sh\necho fake vad-node\n")
	if err := os.WriteFile(filepath.Join(bins, nodeapi.BinaryName("linux", "amd64")), agentBin, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("VAULTDOCK_NODE_BINARIES", bins)
	e := newEnv(t, "")
	admin := e.setupAdmin()

	dir := t.TempDir()
	envFile := fmt.Sprintf("VAD_NODE_SERVER_URL=%s\nVAD_NODE_NAME=Friend's homelab\nVAD_NODE_PORT=0\nVAD_NODE_PROXY=cloudflared\nVAD_NODE_IP=192.168.1.50\nVAD_NODE_KEY_FILE=%s\n",
		e.ts.URL, filepath.Join(t.TempDir(), "keys", "node.key"))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = nodeagent.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)
	}()
	defer func() { cancel(); <-done }()

	waitState := func(want string) nodeagent.Status {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if st, err := nodeagent.ReadStatus(cfg); err == nil && st.State == want {
				return st
			}
			time.Sleep(100 * time.Millisecond)
		}
		st, _ := nodeagent.ReadStatus(cfg)
		t.Fatalf("agent state %q, want %q (%s)", st.State, want, st.Message)
		return st
	}

	waitState("pending")
	list := admin.must("GET", "/api/v1/admin/nodes", nil, 200)["items"].([]any)
	if len(list) != 1 {
		t.Fatalf("nodes = %d", len(list))
	}
	n := list[0].(map[string]any)
	if n["state"] != "pending" || n["name"] != "Friend's homelab" || n["reported_ip"] != "192.168.1.50" || n["proxy"] != "cloudflared" || n["request_ip"] == "" {
		t.Fatalf("pending node = %v", n)
	}
	if _, ok := n["secret_hash"]; ok {
		t.Fatal("secret hash leaked")
	}
	id := int64(n["id"].(float64))
	admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/approve", id), nil, 200)
	admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/approve", id), nil, 409) // decided already

	st := waitState("approved")
	deadline := time.Now().Add(10 * time.Second)
	for st.LastHeartbeat == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		st, _ = nodeagent.ReadStatus(cfg)
	}
	if st.LastHeartbeat == 0 {
		t.Fatal("no heartbeat after approval")
	}
	n = admin.must("GET", "/api/v1/admin/nodes", nil, 200)["items"].([]any)[0].(map[string]any)
	if n["online"] != true || n["secret_delivered"] != true {
		t.Fatalf("approved node = %v", n)
	}

	// The secret is stored encrypted and is not in the folder in clear text.
	nodeID, secret, ok, err := nodeagent.LoadSecret(cfg)
	if err != nil || !ok || nodeID != id || secret == "" {
		t.Fatalf("LoadSecret = %d %v %v", nodeID, ok, err)
	}
	if raw, _ := os.ReadFile(filepath.Join(cfg.DataDir(), "secret.enc")); bytes.Contains(raw, []byte(secret)) {
		t.Fatal("secret stored in clear text")
	}

	heartbeat := func(version string) (int, nodeapi.HeartbeatResponse) {
		b, _ := json.Marshal(nodeapi.Heartbeat{Name: "x", Version: version, OS: "linux", Arch: "amd64"})
		req, _ := http.NewRequest("POST", e.ts.URL+"/api/v1/node/heartbeat", bytes.NewReader(b))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", fmt.Sprintf("Node %d.%s", nodeID, secret))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out nodeapi.HeartbeatResponse
		_ = json.NewDecoder(resp.Body).Decode(&out)
		return resp.StatusCode, out
	}

	// Updates: offered for a release build, matching the served binary.
	old := api.Version
	api.Version = "1.2.3"
	defer func() { api.Version = old }()
	code, hb := heartbeat("1.2.2")
	sum := sha256.Sum256(agentBin)
	if code != 200 || hb.Update == nil || hb.Update.Version != "1.2.3" || hb.Update.SHA256 != hex.EncodeToString(sum[:]) || hb.Update.URL != "/api/v1/node/agent/linux/amd64" {
		t.Fatalf("heartbeat = %d %+v", code, hb.Update)
	}
	if code, hb = heartbeat("1.2.3"); hb.Update != nil {
		t.Fatalf("update offered to an up-to-date node: %+v", hb.Update)
	}
	admin.must("PATCH", fmt.Sprintf("/api/v1/admin/nodes/%d", id), map[string]any{"auto_update": false}, 200)
	if _, hb = heartbeat("1.2.2"); hb.Update != nil {
		t.Fatal("update offered with automatic updates off")
	}
	admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/update", id), nil, 202)
	if _, hb = heartbeat("1.2.2"); hb.Update == nil {
		t.Fatal("update now not offered")
	}
	resp, err := http.Get(e.ts.URL + "/api/v1/node/agent/linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !bytes.Equal(got, agentBin) {
		t.Fatal("served agent differs")
	}

	// Revoked: the secret stops working.
	admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/revoke", id), nil, 204)
	if code, _ = heartbeat("1.2.3"); code != 401 {
		t.Fatalf("heartbeat after revoke = %d", code)
	}
}

// A join request lasts at most an hour and its status needs the poll token.
func TestStorageNodeJoinPollTokenRequired(t *testing.T) {
	e := newEnv(t, "")
	b, _ := json.Marshal(nodeapi.JoinRequest{Name: "n1", OS: "linux", Arch: "amd64"})
	resp, err := http.Post(e.ts.URL+"/api/v1/node/join", "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	var jr nodeapi.JoinResponse
	_ = json.NewDecoder(resp.Body).Decode(&jr)
	resp.Body.Close()
	if resp.StatusCode != 201 || jr.PollToken == "" || jr.ExpiresAt-time.Now().UnixMilli() > 61*60*1000 {
		t.Fatalf("join = %d %+v", resp.StatusCode, jr)
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/node/join/%d", e.ts.URL, jr.ID), nil)
	req.Header.Set("X-Poll-Token", "wrong")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("poll with a wrong token = %d", resp.StatusCode)
	}
}
