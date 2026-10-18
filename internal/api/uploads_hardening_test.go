package api_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// putChunk uploads chunk idx through the web session; hash is sent as
// X-Chunk-SHA256 when non-empty.
func (w *web) putChunk(id string, idx int, body, hash string) int {
	w.t.Helper()
	req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/uploads/%s/chunks/%d", w.base, id, idx), strings.NewReader(body))
	req.Header.Set("X-CSRF-Token", w.csrf)
	if hash != "" {
		req.Header.Set("X-Chunk-SHA256", hash)
	}
	resp, err := w.c.Do(req)
	if err != nil {
		w.t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode
}

func TestUploadCorruptRetryKeepsGoodChunk(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	w := e.createUser(admin, "alice", 0)
	for _, tc := range []struct {
		name  string
		retry string
		hash  bool
		code  int
	}{
		{"corrupt retry rejected", "Xorrupt", true, 422},
		{"wrong length retry rejected", "short", true, 400},
		{"identical retry accepted", "", true, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := strings.ReplaceAll(tc.name, " ", "-") + ".txt"
			// Distinct content per case so nothing is deduplicated.
			good := "content of " + name
			retry := good
			if tc.retry == "short" {
				retry = "short"
			} else if tc.retry != "" {
				retry = "X" + good[1:]
			}
			hash := ""
			if tc.hash {
				hash = sha(good)
			}
			up := w.must("POST", "/api/v1/uploads", map[string]any{"name": name, "size": len(good), "sha256": sha(good)}, 201)["upload"].(map[string]any)
			id := up["id"].(string)
			if code := w.putChunk(id, 0, good, sha(good)); code != 200 {
				t.Fatalf("first chunk: %d", code)
			}
			if code := w.putChunk(id, 0, retry, hash); code != tc.code {
				t.Fatalf("retry: %d want %d", code, tc.code)
			}
			out := w.must("POST", "/api/v1/uploads/"+id+"/complete", nil, 200)
			nid := int64(out["node"].(map[string]any)["id"].(float64))
			resp, err := w.c.Get(fmt.Sprintf("%s/api/v1/files/%d/content", e.ts.URL, nid))
			if err != nil {
				t.Fatal(err)
			}
			got, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if string(got) != good {
				t.Fatalf("content %q want %q", got, good)
			}
		})
	}
}

func TestUploadClosedRejectsChunksAndCancel(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	w := e.createUser(admin, "alice", 0)
	ctx := context.Background()
	const body = "0123456789"

	up := w.must("POST", "/api/v1/uploads", map[string]any{"name": "f.txt", "size": len(body)}, 201)["upload"].(map[string]any)
	id := up["id"].(string)
	if code := w.putChunk(id, 0, body, ""); code != 200 {
		t.Fatalf("chunk: %d", code)
	}
	// Simulate a finalisation in progress.
	if ok, err := e.st.SetUploadStatus(ctx, id, "open", "finalizing"); !ok || err != nil {
		t.Fatalf("set finalizing: %v %v", ok, err)
	}
	if code := w.putChunk(id, 0, body, ""); code != 409 {
		t.Fatalf("chunk during finalize: %d want 409", code)
	}
	code, out := w.req("DELETE", "/api/v1/uploads/"+id, nil)
	if code != 409 || errCode(out) != "upload_busy" {
		t.Fatalf("cancel during finalize: %d %v", code, out)
	}
	// Maintenance must not reap a finalising upload even when expired.
	if _, err := e.st.DB.ExecContext(ctx, "UPDATE uploads SET expires_at = 1 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	e.srv.RunMaintenance(ctx, false)
	if _, err := e.st.UploadAny(ctx, id); err != nil {
		t.Fatalf("finalising upload reaped by maintenance: %v", err)
	}
	if ok, _ := e.st.SetUploadStatus(ctx, id, "finalizing", "open"); !ok {
		t.Fatal("reopen")
	}
	if _, err := e.st.DB.ExecContext(ctx, "UPDATE uploads SET expires_at = 9999999999999 WHERE id = ?", id); err != nil {
		t.Fatal(err)
	}
	w.must("POST", "/api/v1/uploads/"+id+"/complete", nil, 200)
	if code := w.putChunk(id, 0, body, ""); code != 409 {
		t.Fatalf("chunk after complete: %d want 409", code)
	}

	// Out-of-range chunk index and cancel of an open upload.
	up = w.must("POST", "/api/v1/uploads", map[string]any{"name": "g.txt", "size": len(body)}, 201)["upload"].(map[string]any)
	id = up["id"].(string)
	if code := w.putChunk(id, 1, body, ""); code != 400 {
		t.Fatalf("chunk index out of range: %d want 400", code)
	}
	w.must("DELETE", "/api/v1/uploads/"+id, nil, 200)
	if _, err := e.st.UploadAny(ctx, id); err == nil {
		t.Fatal("cancelled upload still present")
	}
}
