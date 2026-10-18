package integration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/testserver"
)

// TestUploadZstdChunks sends a signed device upload with every chunk
// zstd-compressed (upload.zstd); the stored file must match the raw bytes.
func TestUploadZstdChunks(t *testing.T) {
	srv := testserver.Start(t)
	ctx := context.Background()
	a := enrolKeyDevice(t, srv, "PC-Z")

	data := []byte(strings.Repeat("VaultDock compresses this text nicely. ", 200_000)) // ~7.8 MB
	sum := sha256.Sum256(data)
	hexSum := hex.EncodeToString(sum[:])
	res, err := a.c.CreateUpload(ctx, client.UploadRequest{Path: "/Zstd/big.txt", Size: int64(len(data)), SHA256: hexSum, ModifiedAt: 1})
	if err != nil {
		t.Fatal(err)
	}
	up := res.Upload
	r := bytes.NewReader(data)
	for _, idx := range up.Missing() {
		off, n := idx*up.ChunkSize, up.ChunkLen(idx)
		csum, err := client.HashSection(r, off, n)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := a.c.PutChunkCompressed(ctx, up.ID, idx, r, off, n, csum)
		if err != nil || !sent {
			t.Fatalf("chunk %d: sent=%v err=%v", idx, sent, err)
		}
	}
	done, err := a.c.CompleteUpload(ctx, up.ID)
	if err != nil || done.Status != "complete" {
		t.Fatalf("complete: %+v %v", done, err)
	}

	// A compressed chunk whose raw hash does not match is refused.
	data2 := append([]byte("other "), data...)
	sum2 := sha256.Sum256(data2)
	res, err = a.c.CreateUpload(ctx, client.UploadRequest{Path: "/Zstd/bad.txt", Size: int64(len(data2)), SHA256: hex.EncodeToString(sum2[:]), ModifiedAt: 1})
	if err != nil || res.Upload == nil {
		t.Fatalf("second upload: %+v %v", res, err)
	}
	bad := strings.Repeat("0", 64)
	r2 := bytes.NewReader(data2)
	if _, err := a.c.PutChunkCompressed(ctx, res.Upload.ID, 0, r2, 0, res.Upload.ChunkLen(0), bad); err == nil {
		t.Fatal("chunk with a wrong raw hash was accepted")
	}
}
