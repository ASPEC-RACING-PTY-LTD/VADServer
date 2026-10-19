package client

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// CompleteUpload keeps asking while the server is finishing (202) and after
// a proxy gave up (524), then returns the server's result.
func TestCompleteUploadWaitsForFinish(t *testing.T) {
	prev := finishPoll
	finishPoll = 10 * time.Millisecond
	defer func() { finishPoll = prev }()
	f, c := newFake(t)
	calls := 0
	f.handle("POST /api/v1/uploads/u1/complete", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls++
		if r.Header.Get("X-VD-Finalize") != "async" {
			t.Errorf("missing async header")
		}
		switch calls {
		case 1, 3:
			writeJSON(w, 202, map[string]any{"status": "finalizing"})
		case 2:
			writeErr(w, 524, "timeout", nil) // Cloudflare: origin took too long
		default:
			writeJSON(w, 200, map[string]any{"status": "complete", "result": "created"})
		}
	})
	res, err := c.CompleteUpload(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "complete" || res.Result != "created" || calls != 4 {
		t.Fatalf("got %+v after %d calls", res, calls)
	}
}

// An answer from VaultDock itself is returned at once, not retried.
func TestCompleteUploadReturnsServerErrors(t *testing.T) {
	f, c := newFake(t)
	calls := 0
	f.handle("POST /api/v1/uploads/u2/complete", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		calls++
		writeErr(w, 422, "hash_mismatch", nil)
	})
	if _, err := c.CompleteUpload(context.Background(), "u2"); !IsCode(err, "hash_mismatch") || calls != 1 {
		t.Fatalf("err %v after %d calls", err, calls)
	}
}
