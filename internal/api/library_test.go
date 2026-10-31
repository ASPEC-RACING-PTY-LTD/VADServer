package api_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestAdminFullLibrary: administrators list and open every user's files;
// users cannot; every downloaded file is in the audit log.
func TestAdminFullLibrary(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	ctx := context.Background()
	alice := e.createUser(admin, "alice", 0)
	ac := e.enrollDevice(alice, "alice", "password-alice")
	tmp := t.TempDir()
	p, _ := makeFile(t, tmp, "clip.mp4", 4096)
	if _, err := ac.UploadFile(ctx, p, "/Medal/clip.mp4", nil); err != nil {
		t.Fatal(err)
	}
	pt, _ := makeFile(t, tmp, "notes.txt", 100)
	if _, err := ac.UploadFile(ctx, pt, "/notes.txt", nil); err != nil {
		t.Fatal(err)
	}

	lib := admin.must("GET", "/api/v1/admin/library", nil, 200)
	items := lib["items"].([]any)
	if len(items) != 1 || lib["total"].(float64) != 1 {
		t.Fatalf("videos: %v", lib)
	}
	it := items[0].(map[string]any)
	if it["username"] != "alice" || it["path"] != "/Medal/clip.mp4" || it["clip"] != false {
		t.Fatalf("item: %v", it)
	}
	if all := admin.must("GET", "/api/v1/admin/library?kind=all&q=notes", nil, 200)["items"].([]any); len(all) != 1 {
		t.Fatalf("search all kinds: %v", all)
	}
	if none := admin.must("GET", "/api/v1/admin/library?clips=clips", nil, 200)["items"].([]any); len(none) != 0 {
		t.Fatalf("clips filter: %v", none)
	}
	owners := admin.must("GET", "/api/v1/admin/library/owners", nil, 200)["owners"].([]any)
	if len(owners) != 1 || owners[0].(map[string]any)["files"].(float64) != 1 {
		t.Fatalf("owners: %v", owners)
	}

	id := int64(it["id"].(float64))
	req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/admin/library/%d/content?download=1", e.ts.URL, id), nil)
	resp, err := admin.c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(b) != 4096 || !strings.Contains(resp.Header.Get("Content-Disposition"), "attachment") {
		t.Fatalf("download: %d %d %s", resp.StatusCode, len(b), resp.Header.Get("Content-Disposition"))
	}
	if audit := admin.must("GET", "/api/v1/admin/audit?action=admin.library_downloaded", nil, 200); !strings.Contains(fmt.Sprint(audit), "/Medal/clip.mp4") {
		t.Fatalf("audit: %v", audit)
	}

	// Users have no access to the library.
	for _, p := range []string{"/api/v1/admin/library", fmt.Sprintf("/api/v1/admin/library/%d/content", id)} {
		if code, _ := alice.req("GET", p, nil); code != 403 {
			t.Fatalf("user GET %s: %d", p, code)
		}
	}
	if code, _ := admin.req("GET", "/api/v1/admin/library/999999/content", nil); code != 404 {
		t.Fatalf("missing file: %d", code)
	}
}
