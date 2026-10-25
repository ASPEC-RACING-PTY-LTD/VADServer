package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// A clip edited and renamed on a PC keeps its server identity: the same
// file id, creation dates and folder, with no numbered copies, through
// chunked uploads (the session keeps the identity until it is finished)
// and deduplicated ones.
func TestUploadIdentityAcrossEdits(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	if me, err := dc.Me(ctx); err != nil || !me.HasFeature(wire.FeatureUploadIdentity) {
		t.Fatalf("feature not advertised: %v", err)
	}
	type node struct {
		ID            int64  `json:"id"`
		Name          string `json:"name"`
		Path          string `json:"path"`
		CreatedAt     int64  `json:"created_at"`
		ModifiedAt    int64  `json:"modified_at"`
		FileCreatedAt int64  `json:"file_created_at"`
	}
	send := func(req client.UploadRequest, data []byte) (string, node) {
		t.Helper()
		s := sha256.Sum256(data)
		req.Size, req.SHA256, req.OnConflict = int64(len(data)), hex.EncodeToString(s[:]), "rename"
		res, err := dc.CreateUpload(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if res.Status == "open" {
			for i := int64(0); i < res.Upload.ChunksTotal; i++ {
				if err := dc.PutChunk(ctx, res.Upload.ID, i, bytes.NewReader(data), i*res.Upload.ChunkSize, res.Upload.ChunkLen(i)); err != nil {
					t.Fatal(err)
				}
			}
			if res, err = dc.CompleteUpload(ctx, res.Upload.ID); err != nil {
				t.Fatal(err)
			}
		}
		var n node
		if err := json.Unmarshal(res.Node, &n); err != nil {
			t.Fatal(err)
		}
		return res.Result, n
	}
	const born = int64(1_750_000_000_000)
	v1 := bytes.Repeat([]byte("v1"), 5000)
	res, first := send(client.UploadRequest{Path: "/PC/Videos/Clips/clip.mp4", ModifiedAt: born + 1000, FileCreatedAt: born}, v1)
	if res != "created" || first.FileCreatedAt != born {
		t.Fatalf("create: %s %+v", res, first)
	}

	// Edited twice; each upload names the content it continues.
	base, prev := v1, first
	for i, data := range [][]byte{bytes.Repeat([]byte("v2"), 6000), bytes.Repeat([]byte("v3"), 7000)} {
		s := sha256.Sum256(base)
		res, n := send(client.UploadRequest{Path: "/PC/Videos/Clips/clip.mp4", ModifiedAt: born + int64(2000+i), BaseSHA256: hex.EncodeToString(s[:]), FileCreatedAt: born + 50}, data)
		if res != "replaced" || n.ID != first.ID || n.CreatedAt != first.CreatedAt || n.FileCreatedAt != born || n.ModifiedAt != born+int64(2000+i) {
			t.Fatalf("edit %d: %s %+v (was %+v)", i, res, n, prev)
		}
		base, prev = data, n
	}

	// Renamed into a nested folder without changes: moved, nothing sent.
	s := sha256.Sum256(base)
	cur := hex.EncodeToString(s[:])
	res, n := send(client.UploadRequest{Path: "/PC/Videos/Clips/2026/goal.mp4", ModifiedAt: born + 2001, BaseSHA256: cur, PreviousPath: "/PC/Videos/Clips/clip.mp4"}, base)
	if res != "moved" || n.ID != first.ID || n.Path != "/PC/Videos/Clips/2026/goal.mp4" {
		t.Fatalf("rename: %s %+v", res, n)
	}
	// Renamed and edited: a chunked upload that moves and updates the file.
	v4 := bytes.Repeat([]byte("v4"), 8000)
	res, n = send(client.UploadRequest{Path: "/PC/Videos/Clips/2026/best goal.mp4", ModifiedAt: born + 3000, BaseSHA256: cur, PreviousPath: "/PC/Videos/Clips/2026/goal.mp4"}, v4)
	if res != "replaced" || n.ID != first.ID || n.Name != "best goal.mp4" || n.FileCreatedAt != born {
		t.Fatalf("rename+edit: %s %+v", res, n)
	}
	// Synchronised again after a restart: unchanged.
	s = sha256.Sum256(v4)
	if res, n = send(client.UploadRequest{Path: "/PC/Videos/Clips/2026/best goal.mp4", ModifiedAt: born + 3000, BaseSHA256: hex.EncodeToString(s[:])}, v4); res != "unchanged" || n.ID != first.ID {
		t.Fatalf("again: %s %+v", res, n)
	}
	if _, err := dc.CreateUpload(ctx, client.UploadRequest{Path: "/PC/x.mp4", Size: 1, SHA256: cur, BaseSHA256: "nothex"}); !client.IsCode(err, "invalid_hash") {
		t.Fatalf("bad base: %v", err)
	}
}
