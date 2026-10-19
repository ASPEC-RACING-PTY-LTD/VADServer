package storage

import (
	"context"
	"crypto/rand"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// An upload a client compressed is stored from the client's frames (chunks
// without a frame are compressed here); one it sent raw is compressed whole;
// one whose frames do not save space is kept raw and marked.
func TestCompressUploaded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const chunk = 16 << 10
	store1 := func(name string, content []byte, frames map[int64][]byte) (string, string) {
		tmp := writeTemp(t, f, "upload-0000000000"+name, string(content))
		for idx, fr := range frames {
			w, err := NewFrameWriter(tmp, idx)
			if err != nil {
				t.Fatal(err)
			}
			w.Write(fr)
			if err := w.Keep(); err != nil {
				t.Fatal(err)
			}
		}
		sum, err := HashFile(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.m.Place(ctx, f.usr.ID, sum, tmp, f.loc.ID, func(loc int64) error {
			_, err := f.st.CommitFile(ctx, store.CommitRequest{User: f.usr, Name: name + ".bin", Size: int64(len(content)), SHA256: sum, LocationID: loc, OnConflict: "replace"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return sum, FramesDir(tmp)
	}
	read := func(sum string) []byte {
		fh, err := f.m.Open(f.loc.ID, f.usr.ID, sum)
		if err != nil {
			t.Fatal(err)
		}
		defer fh.Close()
		b, _ := io.ReadAll(fh)
		return b
	}

	// Client-compressed: chunks 0 and 2 arrived as frames, chunk 1 raw.
	text := []byte(strings.Repeat("compressible text ", 3*chunk/18+1))[:3*chunk]
	frames := map[int64][]byte{0: compress.Frame(text[:chunk]), 2: compress.Frame(text[2*chunk:])}
	sum, dir := store1("1", text, frames)
	done, err := f.m.CompressUploaded(ctx, UploadedFile{UserID: f.usr.ID, SHA256: sum, Size: int64(len(text)), ChunkSize: chunk, Frames: dir, ClientCompressed: true})
	if err != nil || !done {
		t.Fatalf("client-compressed: done=%v err=%v", done, err)
	}
	p, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, sum)
	if regular(p) || !regular(p+zstSuffix) {
		t.Fatal("blob not stored compressed")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("frames directory left behind")
	}
	if string(read(sum)) != string(text) {
		t.Fatal("content differs after compression")
	}

	// Sent raw by a client that does not compress: the server compresses it.
	text2 := []byte(strings.Repeat("other compressible text ", 2000))
	sum2, dir2 := store1("2", text2, nil)
	if done, err := f.m.CompressUploaded(ctx, UploadedFile{UserID: f.usr.ID, SHA256: sum2, Size: int64(len(text2)), ChunkSize: chunk, Frames: dir2}); err != nil || !done {
		t.Fatalf("raw upload: done=%v err=%v", done, err)
	}
	if string(read(sum2)) != string(text2) {
		t.Fatal("content differs after server compression")
	}

	// A compressing client sent everything raw (nothing shrank): kept raw,
	// marked, and not compressed again.
	noise := make([]byte, 2*chunk)
	_, _ = rand.Read(noise)
	sum3, dir3 := store1("3", noise, nil)
	if done, err := f.m.CompressUploaded(ctx, UploadedFile{UserID: f.usr.ID, SHA256: sum3, Size: int64(len(noise)), ChunkSize: chunk, Frames: dir3, ClientCompressed: true}); err != nil || done {
		t.Fatalf("incompressible: done=%v err=%v", done, err)
	}
	p3, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, sum3)
	if !regular(p3) || !regular(p3+noZstSuffix) {
		t.Fatal("incompressible upload not kept raw and marked")
	}
}

// A frame that does not decode to the stored content never replaces it.
func TestCompressUploadedRejectsBadFrames(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	const chunk = 16 << 10
	text := []byte(strings.Repeat("abc", chunk))[:2*chunk]
	tmp := writeTemp(t, f, "upload-000000000099", string(text))
	w, _ := NewFrameWriter(tmp, 0)
	w.Write(compress.Frame([]byte(strings.Repeat("x", chunk)))) // wrong content
	_ = w.Keep()
	sum, _ := HashFile(tmp)
	if err := f.m.Place(ctx, f.usr.ID, sum, tmp, f.loc.ID, func(loc int64) error {
		_, err := f.st.CommitFile(ctx, store.CommitRequest{User: f.usr, Name: "bad.bin", Size: int64(len(text)), SHA256: sum, LocationID: loc, OnConflict: "replace"})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if done, err := f.m.CompressUploaded(ctx, UploadedFile{UserID: f.usr.ID, SHA256: sum, Size: int64(len(text)), ChunkSize: chunk, Frames: FramesDir(tmp), ClientCompressed: true}); err == nil || done {
		t.Fatalf("bad frame accepted: done=%v err=%v", done, err)
	}
	p, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, sum)
	if !regular(p) || regular(p+zstSuffix) || regular(p+zstSuffix+".tmp") {
		t.Fatal("raw blob not kept after a bad frame")
	}
}
