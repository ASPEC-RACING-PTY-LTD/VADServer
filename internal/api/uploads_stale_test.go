package api_test

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// partFile finds an upload's temp file under the test storage.
func (e *env) partFile(id string) string {
	e.t.Helper()
	var found string
	_ = filepath.WalkDir(e.dir, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == id+".part" {
			found = p
		}
		return nil
	})
	if found == "" {
		e.t.Fatalf("temp file for %s not found", id)
	}
	return found
}

func failedTransfers(t *testing.T, w *web) int {
	t.Helper()
	out := w.must("GET", "/api/v1/transfers?status=failed", nil, 200)
	items, _ := out["items"].([]any)
	if items == nil {
		items, _ = out["transfers"].([]any)
	}
	return len(items)
}

func TestUploadLostDataStartsFresh(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	w := e.createUser(admin, "alice", 0)
	for _, tc := range []struct {
		name string
		brk  func(path string)
	}{
		{"missing", func(p string) { os.Remove(p) }},
		{"partial", func(p string) { os.Truncate(p, 3) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := "content for the " + tc.name + " case"
			req := map[string]any{"name": tc.name + ".txt", "size": len(body), "sha256": sha(body)}
			id := w.must("POST", "/api/v1/uploads", req, 201)["upload"].(map[string]any)["id"].(string)
			if code := w.putChunk(id, 0, body, sha(body)); code != 200 {
				t.Fatalf("chunk: %d", code)
			}
			tc.brk(e.partFile(id))

			// Resuming must not hand back the broken session.
			fresh := w.must("POST", "/api/v1/uploads", req, 201)["upload"].(map[string]any)["id"].(string)
			if fresh == id {
				t.Fatal("broken session was resumed")
			}
			if code, _ := w.req("GET", "/api/v1/uploads/"+id, nil); code != 404 {
				t.Fatalf("old session still there: %d", code)
			}
			if code := w.putChunk(fresh, 0, body, sha(body)); code != 200 {
				t.Fatalf("fresh chunk: %d", code)
			}
			w.must("POST", "/api/v1/uploads/"+fresh+"/complete", nil, 200)
			if n := failedTransfers(t, w); n != 0 {
				t.Fatalf("failed transfers = %d, want 0", n)
			}
		})
	}

	t.Run("missing at complete", func(t *testing.T) {
		body := "content lost before completion"
		req := map[string]any{"name": "late.txt", "size": len(body), "sha256": sha(body)}
		id := w.must("POST", "/api/v1/uploads", req, 201)["upload"].(map[string]any)["id"].(string)
		if code := w.putChunk(id, 0, body, sha(body)); code != 200 {
			t.Fatalf("chunk: %d", code)
		}
		os.Remove(e.partFile(id))
		if code, _ := w.req("POST", "/api/v1/uploads/"+id+"/complete", nil); code != 410 {
			t.Fatalf("complete: %d want 410", code)
		}
		if n := failedTransfers(t, w); n != 0 {
			t.Fatalf("failed transfers = %d, want 0", n)
		}
		w.must("POST", "/api/v1/uploads", req, 201)
	})
}

func TestSyncDismissFailures(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	w := e.createUser(admin, "bob", 0)
	u, err := e.st.UserByLogin(context.Background(), "bob")
	if err != nil {
		t.Fatal(err)
	}
	fail := func(path string) {
		if err := e.st.RecordTransfer(context.Background(), store.Transfer{UserID: u.ID, Path: path, Status: "failed", Error: "boom", Source: "web"}); err != nil {
			t.Fatal(err)
		}
	}
	shown := func() int {
		return len(w.must("GET", "/api/v1/sync/overview", nil, 200)["recent_failures"].([]any))
	}
	fail("/a.txt")
	if n := shown(); n != 1 {
		t.Fatalf("before dismiss: %d", n)
	}
	w.must("POST", "/api/v1/sync/failures/dismiss", nil, 200)
	if n := shown(); n != 0 {
		t.Fatalf("after dismiss: %d", n)
	}
	fail("/b.txt")
	if n := shown(); n != 1 {
		t.Fatalf("new failure: %d", n)
	}
}
