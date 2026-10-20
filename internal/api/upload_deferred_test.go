package api_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
)

// A deferred finish answers at once; the outcome is reported later, and a
// file the server does not have is reported missing.
func TestDeferredFinishAndResults(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	if me, err := dc.Me(ctx); err != nil || !me.HasFeature(wire.FeatureDeferredFinish) {
		t.Fatalf("feature not advertised: %v", err)
	}

	tmp := t.TempDir()
	p, data := makeFile(t, tmp, "clip.mp4", 19<<20) // three chunks
	sum, _ := client.HashFile(p)
	cr, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Clips/clip.mp4", Size: int64(len(data)), SHA256: sum})
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Open(p)
	defer f.Close()
	// Out of order, so the in-flight hash has to wait for chunk 0.
	for _, idx := range []int64{2, 1, 0} {
		if err := dc.PutChunk(ctx, cr.Upload.ID, idx, f, idx*cr.Upload.ChunkSize, cr.Upload.ChunkLen(idx)); err != nil {
			t.Fatal(err)
		}
	}
	hits := api.StreamHashHits()
	if _, err := dc.FinishLater(ctx, cr.Upload.ID); err != nil {
		t.Fatal(err)
	}
	checks := []wire.UploadCheck{
		{ID: cr.Upload.ID, Path: "/Clips/clip.mp4", Size: int64(len(data)), SHA256: sum},
		{ID: "nope", Path: "/Clips/never-sent.mp4", Size: 5, SHA256: sum},
	}
	var res []wire.UploadOutcome
	deadline := time.Now().Add(20 * time.Second)
	for {
		res, err = dc.UploadResults(ctx, checks)
		if err != nil {
			t.Fatal(err)
		}
		if res[0].Status != wire.UploadPending || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if res[0].Status != wire.UploadDone || res[1].Status != wire.UploadMissing {
		t.Fatalf("results %+v", res)
	}
	if api.StreamHashHits() != hits+1 {
		t.Fatal("the finish read the file again instead of using the hash computed on arrival")
	}
	node := alice.must("GET", "/api/v1/files/resolve?path=%2FClips%2Fclip.mp4", nil, 200)["node"].(map[string]any)
	if node["sha256"] != sum {
		t.Fatalf("stored file %v, want hash %s", node, sum)
	}

	// A session missing a chunk is reported incomplete.
	p2, data2 := makeFile(t, tmp, "half.mp4", 17<<20)
	sum2, _ := client.HashFile(p2)
	cr2, _ := dc.CreateUpload(ctx, client.UploadRequest{Path: "/Clips/half.mp4", Size: int64(len(data2)), SHA256: sum2})
	f2, _ := os.Open(p2)
	defer f2.Close()
	if err := dc.PutChunk(ctx, cr2.Upload.ID, 0, f2, 0, cr2.Upload.ChunkLen(0)); err != nil {
		t.Fatal(err)
	}
	res, _ = dc.UploadResults(ctx, []wire.UploadCheck{{ID: cr2.Upload.ID, Path: "/Clips/half.mp4", Size: int64(len(data2)), SHA256: sum2}})
	if res[0].Status != wire.UploadIncomplete {
		t.Fatalf("half upload: %+v", res)
	}
}
