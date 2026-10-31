package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
)

// TestPublicVideoLinks: a user shares a video; anyone with the link can
// watch it (and download it only when allowed) until it expires, is
// revoked or the file changes. Link previews carry the title.
func TestPublicVideoLinks(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	ctx := context.Background()
	bob := e.createUser(admin, "bob", 0)
	bc := e.enrollDevice(bob, "bob", "password-bob")
	tmp := t.TempDir()
	p, data := makeFile(t, tmp, "goal.mp4", 8192)
	res, err := bc.UploadFile(ctx, p, "/Clips/goal.mp4", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := nodeID(t, res.Node)
	pt, _ := makeFile(t, tmp, "notes.txt", 10)
	tr, _ := bc.UploadFile(ctx, pt, "/notes.txt", nil)

	if code, _ := bob.req("POST", "/api/v1/shares", map[string]any{"node_id": nodeID(t, tr.Node), "title": "x"}); code != 400 {
		t.Fatalf("shared a text file: %d", code)
	}
	if code, _ := bob.req("POST", "/api/v1/shares", map[string]any{"node_id": id, "title": " "}); code != 400 {
		t.Fatalf("empty title: %d", code)
	}
	if code, _ := admin.req("POST", "/api/v1/shares", map[string]any{"node_id": id, "title": "x"}); code != 404 {
		t.Fatalf("shared another user's file: %d", code)
	}
	sh := bob.must("POST", "/api/v1/shares", map[string]any{"node_id": id, "title": "Insane <goal>", "description": "Overtime winner", "expires_in": 86400}, 201)
	tok := sh["token"].(string)
	sid := int64(sh["id"].(float64))
	if sh["state"] != "active" || sh["url"] != "/s/"+tok || len(tok) < 20 {
		t.Fatalf("share: %v", sh)
	}

	// Anyone (no session) can read it.
	anon := e.web()
	meta := anon.must("GET", "/api/v1/public/shares/"+tok, nil, 200)
	if meta["title"] != "Insane <goal>" || meta["shared_by"] != "bob" || meta["allow_download"] != false {
		t.Fatalf("public meta: %v", meta)
	}
	get := func(path string, hdr map[string]string) (*http.Response, []byte) {
		req, _ := http.NewRequest("GET", e.ts.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	resp, b := get("/api/v1/public/shares/"+tok+"/video", map[string]string{"Range": "bytes=0-99"})
	if resp.StatusCode != 206 || len(b) != 100 || string(b) != string(data[:100]) || !strings.Contains(resp.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("stream: %d %d %s", resp.StatusCode, len(b), resp.Header.Get("Content-Disposition"))
	}
	if resp, _ := get("/api/v1/public/shares/"+tok+"/video?download=1", nil); resp.StatusCode != 403 {
		t.Fatalf("download while not allowed: %d", resp.StatusCode)
	}
	bob.must("PATCH", fmt.Sprintf("/api/v1/shares/%d", sid), map[string]any{"allow_download": true}, 200)
	if resp, b := get("/api/v1/public/shares/"+tok+"/video?download=1", nil); resp.StatusCode != 200 || len(b) != 8192 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d", resp.StatusCode)
	}
	// A guessed token finds nothing; the owner's other files stay private.
	if code, _ := anon.req("GET", "/api/v1/public/shares/"+strings.Repeat("A", len(tok)), nil); code != 404 {
		t.Fatalf("guessed token: %d", code)
	}
	if code, _ := anon.req("GET", fmt.Sprintf("/api/v1/files/%d/content", id), nil); code != 401 {
		t.Fatalf("private content without a session: %d", code)
	}

	// The page carries link preview tags (escaped).
	h := e.srv.Handler(api.SPAHandler(fstest.MapFS{"index.html": {Data: []byte("<html><head><title>VaultDock</title></head><body></body></html>")}}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/s/"+tok, nil))
	page := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(page, `og:title" content="Insane &lt;goal&gt;"`) || !strings.Contains(page, "/api/v1/public/shares/"+tok+"/poster") {
		t.Fatalf("preview page: %d %s", rec.Code, page)
	}

	// Expiry, revocation and a changed file end the link.
	bob.must("PATCH", fmt.Sprintf("/api/v1/shares/%d", sid), map[string]any{"expires_in": 0}, 200)
	if got := bob.must("GET", fmt.Sprintf("/api/v1/shares?node_id=%d", id), nil, 200)["shares"].([]any)[0].(map[string]any); got["expires_at"] != nil || got["views"].(float64) < 1 {
		t.Fatalf("listed share: %v", got)
	}
	p2, _ := makeFile(t, tmp, "goal2.mp4", 9000)
	if _, err := bc.UploadFile(ctx, p2, "/Clips/goal.mp4", nil); err != nil {
		t.Fatal(err)
	}
	if code, out := anon.req("GET", "/api/v1/public/shares/"+tok, nil); code != 404 || errCode(out) != "unavailable" {
		t.Fatalf("changed file still shared: %d %v", code, out)
	}
	sh2 := bob.must("POST", "/api/v1/shares", map[string]any{"node_id": id, "title": "New", "expires_in": 3600}, 201)
	bob.must("DELETE", fmt.Sprintf("/api/v1/shares/%d", int64(sh2["id"].(float64))), nil, 204)
	if code, _ := anon.req("GET", "/api/v1/public/shares/"+sh2["token"].(string), nil); code != 404 {
		t.Fatalf("revoked link: %d", code)
	}
	sh3 := bob.must("POST", "/api/v1/shares", map[string]any{"node_id": id, "title": "Old", "expires_in": 3600}, 201)
	if _, err := e.st.DB.ExecContext(ctx, "UPDATE shares SET expires_at = 1 WHERE id = ?", int64(sh3["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	if code, out := anon.req("GET", "/api/v1/public/shares/"+sh3["token"].(string), nil); code != 410 || errCode(out) != "expired" {
		t.Fatalf("expired link: %d %v", code, out)
	}
	if code, _ := admin.req("DELETE", fmt.Sprintf("/api/v1/shares/%d", int64(sh3["id"].(float64))), nil); code != 404 {
		t.Fatalf("other user revoked: %d", code)
	}
}
