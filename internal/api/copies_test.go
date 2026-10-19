package api_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/nodeagent"
)

// startNode runs a real vad-node agent against the test server, approves it
// and returns its config and node id.
func startNode(t *testing.T, e *env, admin *web, roots string, terminal bool) (nodeagent.Config, int64) {
	t.Helper()
	dir := t.TempDir()
	term := "off"
	if terminal {
		term = "on"
	}
	envFile := fmt.Sprintf("VAD_NODE_SERVER_URL=%s\nVAD_NODE_NAME=homelab\nVAD_NODE_STORAGE_ROOTS=%s\nVAD_NODE_TERMINAL=%s\nVAD_NODE_KEY_FILE=%s\n",
		e.ts.URL, roots, term, filepath.Join(t.TempDir(), "node.key"))
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envFile), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := nodeagent.LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = nodeagent.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	var id int64
	waitFor(t, 30*time.Second, "join request", func() bool {
		for _, n := range admin.must("GET", "/api/v1/admin/nodes", nil, 200)["items"].([]any) {
			if m := n.(map[string]any); m["state"] == "pending" {
				id = int64(m["id"].(float64))
				return true
			}
		}
		return false
	})
	admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/approve", id), nil, 200)
	waitFor(t, 30*time.Second, "heartbeat", func() bool {
		st, err := nodeagent.ReadStatus(cfg)
		return err == nil && st.State == "approved" && st.LastHeartbeat > 0
	})
	return cfg, id
}

func waitFor(t *testing.T, d time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func copies(admin *web, state string) []map[string]any {
	var out []map[string]any
	for _, c := range admin.must("GET", "/api/v1/admin/copies?state="+state, nil, 200)["items"].([]any) {
		out = append(out, c.(map[string]any))
	}
	return out
}

func download(t *testing.T, w *web, base string, id int64) []byte {
	t.Helper()
	resp, err := w.c.Get(fmt.Sprintf("%s/api/v1/files/%d/content", base, id))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		t.Fatalf("download: %d %s", resp.StatusCode, b)
	}
	return b
}

// Copy rules end to end: a local copy and an encrypted node copy are made,
// a damaged original is repaired from the local copy, a missing original is
// served (and repaired) from the node, and deleting the file deletes the
// copies after the rule's delay.
func TestCopyRulesLocalAndNode(t *testing.T) {
	e := newEnv(t, "")
	var kek vdcrypt.Key
	rand.Read(kek[:])
	km, err := keys.New(context.Background(), e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(km)
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.RunCopies(ctx)

	// Targets: a second local location and a folder on a node.
	second := filepath.Join(e.storage, "second")
	os.MkdirAll(second, 0o755)
	loc := admin.must("POST", "/api/v1/admin/storage", map[string]any{"name": "Second", "path": second}, 201)
	secondID := int64(loc["location"].(map[string]any)["id"].(float64))
	nodeRoot := t.TempDir()
	_, nid := startNode(t, e, admin, nodeRoot, false)
	admin.must("POST", "/api/v1/admin/node-locations", map[string]any{"node_id": nid, "name": "tank", "path": filepath.Join(t.TempDir(), "outside")}, 400) // outside the node's roots
	nl := admin.must("POST", "/api/v1/admin/node-locations", map[string]any{"node_id": nid, "name": "tank", "path": nodeRoot}, 201)
	nlID := int64(nl["id"].(float64))
	waitFor(t, 30*time.Second, "node location prepared", func() bool {
		for _, l := range admin.must("GET", "/api/v1/admin/node-locations", nil, 200)["items"].([]any) {
			if m := l.(map[string]any); int64(m["id"].(float64)) == nlID && m["status"] == "ok" {
				return true
			}
		}
		return false
	})

	// A file to protect, and one the rules do not match.
	tmp := t.TempDir()
	p, data := makeFile(t, tmp, "clip.mp4", 3<<20+5)
	res, err := dc.UploadFile(context.Background(), p, "/Medal/clip.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	fileID := nodeID(t, res.Node)
	other, _ := makeFile(t, tmp, "notes.txt", 100)
	if _, err := dc.UploadFile(context.Background(), other, "/Docs/notes.txt", nil); err != nil {
		t.Fatal(err)
	}
	sumB := sha256.Sum256(data)
	sha := hex.EncodeToString(sumB[:])
	uid := int64(alice.must("GET", "/api/v1/auth/me", nil, 200)["user"].(map[string]any)["id"].(float64))

	admin.must("POST", "/api/v1/admin/copy-rules", map[string]any{"name": "to second", "enabled": true, "target": fmt.Sprintf("local:%d", secondID), "path_pattern": "/Medal/", "delete_delay_hours": 0}, 200)
	admin.must("POST", "/api/v1/admin/copy-rules", map[string]any{"name": "to node", "enabled": true, "target": fmt.Sprintf("node:%d", nlID), "extensions": "mp4", "delete_delay_hours": 0}, 200)
	admin.must("POST", "/api/v1/admin/copy-rules", map[string]any{"name": "bad", "target": "node:999"}, 400)

	waitFor(t, 60*time.Second, "two copies", func() bool {
		admin.must("POST", "/api/v1/admin/copies/run", nil, 202)
		return len(copies(admin, "ok")) == 2
	})
	for _, c := range copies(admin, "") {
		if c["sha256"] != sha {
			t.Fatalf("an unmatched file was copied: %v", c)
		}
	}
	localCopy := filepath.Join(second, "replicas", strconv.FormatInt(uid, 10), sha[:2], sha[2:4], sha)
	if b, err := os.ReadFile(localCopy); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("local copy: %v", err)
	}
	nodeCopy := filepath.Join(nodeRoot, "replicas", strconv.FormatInt(uid, 10), sha[:2], sha[2:4], sha+".vdr")
	enc, err := os.ReadFile(nodeCopy)
	if err != nil {
		t.Fatalf("node copy: %v", err)
	}
	if bytes.Contains(enc, data[:4096]) {
		t.Fatal("the node copy is not encrypted")
	}

	// A damaged original is found by the check and repaired from a copy.
	primary := filepath.Join(e.storage, "default", "blobs", strconv.FormatInt(uid, 10), sha[:2], sha[2:4], sha)
	garbage := make([]byte, len(data))
	rand.Read(garbage)
	if err := os.WriteFile(primary, garbage, 0o640); err != nil {
		t.Fatal(err)
	}
	admin.must("POST", "/api/v1/admin/copies/verify", nil, 202)
	waitFor(t, 60*time.Second, "original repaired", func() bool {
		b, err := os.ReadFile(primary)
		return err == nil && bytes.Equal(b, data)
	})

	// A missing original is served from the node copy (local copy removed too).
	os.Remove(primary)
	os.Remove(localCopy)
	if got := download(t, alice, e.ts.URL, fileID); !bytes.Equal(got, data) {
		t.Fatal("content served from the node copy differs")
	}
	if b, err := os.ReadFile(primary); err != nil || !bytes.Equal(b, data) {
		t.Fatal("the original was not restored from the node copy")
	}
	// The next check finds the missing local copy and makes it again.
	admin.must("POST", "/api/v1/admin/copies/verify", nil, 202)
	waitFor(t, 60*time.Second, "local copy made again", func() bool {
		admin.must("POST", "/api/v1/admin/copies/run", nil, 202)
		b, err := os.ReadFile(localCopy)
		return err == nil && bytes.Equal(b, data)
	})

	// Deleting the file deletes its copies (delay 0 here).
	alice.must("DELETE", fmt.Sprintf("/api/v1/files/%d", fileID), nil, 200)
	alice.must("DELETE", "/api/v1/trash", nil, 200)
	waitFor(t, 60*time.Second, "copies deleted", func() bool {
		admin.must("POST", "/api/v1/admin/copies/run", nil, 202)
		_, e1 := os.Stat(localCopy)
		_, e2 := os.Stat(nodeCopy)
		return os.IsNotExist(e1) && os.IsNotExist(e2) && len(copies(admin, "")) == 0
	})
	admin.must("DELETE", fmt.Sprintf("/api/v1/admin/node-locations/%d", nlID), nil, 409) // a rule still uses it
}

// A file deleted by mistake can be recovered from a copy while the rule's
// delay runs.
func TestCopyRecoverDeletedFile(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.srv.RunCopies(ctx)
	second := filepath.Join(e.storage, "second")
	os.MkdirAll(second, 0o755)
	loc := admin.must("POST", "/api/v1/admin/storage", map[string]any{"name": "Second", "path": second}, 201)
	admin.must("POST", "/api/v1/admin/copy-rules", map[string]any{"name": "all", "enabled": true, "target": fmt.Sprintf("local:%d", int64(loc["location"].(map[string]any)["id"].(float64))), "delete_delay_hours": 48}, 200)
	p, data := makeFile(t, t.TempDir(), "report.pdf", 70_000)
	res, err := dc.UploadFile(context.Background(), p, "/Work/report.pdf", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "copy", func() bool {
		admin.must("POST", "/api/v1/admin/copies/run", nil, 202)
		return len(copies(admin, "ok")) == 1
	})
	alice.must("DELETE", fmt.Sprintf("/api/v1/files/%d", nodeID(t, res.Node)), nil, 200)
	alice.must("DELETE", "/api/v1/trash", nil, 200)
	var c map[string]any
	waitFor(t, 30*time.Second, "copy waiting for deletion", func() bool {
		admin.must("POST", "/api/v1/admin/copies/run", nil, 202)
		d := copies(admin, "deleting")
		if len(d) == 1 {
			c = d[0]
		}
		return len(d) == 1
	})
	if c["last_path"] != "/Work/report.pdf" {
		t.Fatalf("last path = %v", c["last_path"])
	}
	admin.must("POST", "/api/v1/admin/copies/recover", map[string]any{"user_id": c["user_id"], "sha256": c["sha256"], "target": c["target"]}, 202)
	var recovered int64
	waitFor(t, 30*time.Second, "recovered file", func() bool {
		code, out := alice.req("GET", "/api/v1/files/resolve?path="+"/Recovered/Work/report.pdf", nil)
		if code != 200 {
			return false
		}
		if n, ok := out["node"].(map[string]any); ok {
			recovered = int64(n["id"].(float64))
		} else if id, ok := out["id"].(float64); ok {
			recovered = int64(id)
		}
		return recovered > 0
	})
	if got := download(t, alice, e.ts.URL, recovered); !bytes.Equal(got, data) {
		t.Fatal("recovered content differs")
	}
}

// Terminals: superusers only, the password again, and only where the node's
// owner allowed it.
func TestNodeTerminalGuards(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	_, off := startNode(t, e, admin, t.TempDir(), false)
	code, out := admin.req("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/terminal", off), map[string]any{"password": "wrong"})
	if code != 403 || errCode(out) != "bad_password" {
		t.Fatalf("wrong password = %d %v", code, out)
	}
	code, out = admin.req("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/terminal", off), map[string]any{"password": adminPass})
	if code != 409 || errCode(out) != "terminal_off" {
		t.Fatalf("terminal on a node that does not allow it = %d %v", code, out)
	}
	alice := e.createUser(admin, "alice", 0)
	if code, _ := alice.req("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/terminal", off), map[string]any{"password": "password-alice"}); code != 403 {
		t.Fatalf("non-admin terminal = %d", code)
	}
}

// echoShell stands in for a PTY shell (Linux only): it echoes its input.
type echoShell struct {
	r    *io.PipeReader
	w    *io.PipeWriter
	done chan struct{}
}

func (s *echoShell) Read(b []byte) (int, error)  { return s.r.Read(b) }
func (s *echoShell) Write(b []byte) (int, error) { return s.w.Write(b) }
func (s *echoShell) Resize(int, int) error       { return nil }
func (s *echoShell) Kill() {
	s.w.Close()
	select {
	case <-s.done:
	default:
		close(s.done)
	}
}
func (s *echoShell) Wait() int { <-s.done; return 0 }

// A terminal session end to end: browser input reaches the node's shell and
// its output comes back; closing ends it and both are audited.
func TestNodeTerminalSession(t *testing.T) {
	old := nodeagent.NewShell
	nodeagent.NewShell = func(string, int, int) (nodeagent.PTYSession, error) {
		r, w := io.Pipe()
		return &echoShell{r: r, w: w, done: make(chan struct{})}, nil
	}
	defer func() { nodeagent.NewShell = old }()
	e := newEnv(t, "")
	admin := e.setupAdmin()
	_, id := startNode(t, e, admin, t.TempDir(), true)
	sid := admin.must("POST", fmt.Sprintf("/api/v1/admin/nodes/%d/terminal", id), map[string]any{"password": adminPass, "cols": 100, "rows": 30}, 201)["session"].(string)
	admin.must("POST", "/api/v1/admin/terminal/"+sid+"/input", map[string]any{"data": []byte("echo hi\n"), "cols": 120, "rows": 40}, 204)
	var got []byte
	seq := 0.0
	waitFor(t, 30*time.Second, "terminal output", func() bool {
		out := admin.must("GET", fmt.Sprintf("/api/v1/admin/terminal/%s/output?after=%d", sid, int64(seq)), nil, 200)
		seq = out["seq"].(float64)
		if s, ok := out["data"].(string); ok && s != "" {
			b, _ := base64.StdEncoding.DecodeString(s)
			got = append(got, b...)
		}
		return bytes.Contains(got, []byte("echo hi"))
	})
	admin.must("DELETE", "/api/v1/admin/terminal/"+sid, nil, 204)
	out := admin.must("GET", fmt.Sprintf("/api/v1/admin/terminal/%s/output?after=%d", sid, int64(seq)), nil, 200)
	if out["closed"] != true {
		t.Fatalf("after close: %v", out)
	}
	actions := map[string]bool{}
	for _, a := range admin.must("GET", "/api/v1/admin/audit", nil, 200)["items"].([]any) {
		actions[a.(map[string]any)["action"].(string)] = true
	}
	if !actions["node.terminal_opened"] || !actions["node.terminal_closed"] {
		t.Fatalf("terminal not audited: %v", actions)
	}
}
