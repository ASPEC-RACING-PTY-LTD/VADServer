package api_test

import (
	"bytes"
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
)

// completeAsync posts complete with the asynchronous header and returns the
// status code (0 when the request was cancelled).
func (w *web) completeAsync(ctx context.Context, id string) int {
	w.t.Helper()
	req, _ := http.NewRequestWithContext(ctx, "POST", w.base+"/api/v1/uploads/"+id+"/complete", bytes.NewReader([]byte("{}")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-CSRF-Token", w.csrf)
	req.Header.Set("X-VD-Finalize", "async")
	resp, err := w.c.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.StatusCode
}

// A finish that outlives the request (a proxy timeout) is neither lost nor
// restarted: retries join it and get its result.
func TestUploadFinishSurvivesDisconnect(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	w := e.createUser(admin, "alice", 0)
	hold := make(chan struct{})
	restore := api.HoldFinalize(200*time.Millisecond, hold)
	defer restore()

	body := "a large file, finished slowly"
	req := map[string]any{"name": "big.mp4", "size": len(body), "sha256": sha(body)}
	id := w.must("POST", "/api/v1/uploads", req, 201)["upload"].(map[string]any)["id"].(string)
	if code := w.putChunk(id, 0, body, sha(body)); code != 200 {
		t.Fatalf("chunk: %d", code)
	}

	// The client gives up while the finish runs (like Cloudflare's 524).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	if code := w.completeAsync(ctx, id); code != 0 {
		t.Fatalf("cancelled complete returned %d", code)
	}
	cancel()
	// An async client is told to ask again.
	if code := w.completeAsync(context.Background(), id); code != 202 {
		t.Fatalf("async complete while finishing: %d want 202", code)
	}
	// Starting the same upload again hands back the session being finished.
	again := w.must("POST", "/api/v1/uploads", req, 200)
	if got := again["upload"].(map[string]any)["id"]; got != id {
		t.Fatalf("resume returned %v, want the finishing session %s", got, id)
	}

	close(hold)
	out := w.must("POST", "/api/v1/uploads/"+id+"/complete", nil, 200)
	if out["status"] != "complete" || out["result"] != "created" {
		t.Fatalf("complete after finish: %v", out)
	}
	// Asking again after it ended returns the kept result.
	if out := w.must("POST", "/api/v1/uploads/"+id+"/complete", nil, 200); out["status"] != "complete" {
		t.Fatalf("repeat complete: %v", out)
	}
	if n := failedTransfers(t, w); n != 0 {
		t.Fatalf("failed transfers = %d, want 0", n)
	}
}
