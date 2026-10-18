package storage

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestCompressStoredOpenSweepRemove(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	place := func(name, content string) string {
		tmp := writeTemp(t, f, "upload-000000000000"+name, content)
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
		return sum
	}
	text := strings.Repeat("compressible text ", 10_000)
	noise := make([]byte, 64<<10)
	_, _ = rand.Read(noise)
	textSum := place("1", text)
	noiseSum := place("2", string(noise))

	st, err := f.m.CompressStored(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Compressed != 1 || st.Skipped != 1 || st.BytesAfter >= st.BytesBefore {
		t.Fatalf("stats %+v", st)
	}
	raw, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, textSum)
	if regular(raw) || !regular(raw+zstSuffix) {
		t.Fatal("text blob not replaced by its compressed form")
	}
	noisy, _ := f.m.BlobPath(f.loc.ID, f.usr.ID, noiseSum)
	if !regular(noisy) || !regular(noisy+noZstSuffix) {
		t.Fatal("incompressible blob not kept raw and marked")
	}
	// A second pass has nothing to do.
	if st2, _ := f.m.CompressStored(ctx, nil); st2.Compressed != 0 {
		t.Fatalf("second pass %+v", st2)
	}

	// Reads extract once and return the original bytes.
	fh, err := f.m.Open(f.loc.ID, f.usr.ID, textSum)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(fh)
	fh.Close()
	if string(got) != text {
		t.Fatal("extracted content differs")
	}
	cp := filepath.Join(extractDir(f.loc), extractName(f.usr.ID, textSum))
	if !regular(cp) {
		t.Fatal("no extracted copy")
	}
	if ok, err := f.m.BlobStored(f.loc.ID, f.usr.ID, textSum); !ok || err != nil {
		t.Fatalf("BlobStored = %v %v", ok, err)
	}

	// A recently used copy survives the sweep; an idle one is removed.
	if n := f.m.SweepExtracted(ExtractIdle); n != 0 || !regular(cp) {
		t.Fatal("fresh extracted copy swept")
	}
	idle := time.Now().Add(-ExtractIdle - time.Minute)
	_ = os.Chtimes(cp, idle, idle)
	if n := f.m.SweepExtracted(ExtractIdle); n != 1 || regular(cp) {
		t.Fatal("idle extracted copy not swept")
	}

	// Deleting the blob removes every variant.
	if fh, err := f.m.Open(f.loc.ID, f.usr.ID, textSum); err == nil {
		fh.Close()
	}
	if _, err := f.st.DB.ExecContext(ctx, "DELETE FROM nodes"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.DB.ExecContext(ctx, "DELETE FROM blobs"); err != nil {
		t.Fatal(err)
	}
	f.m.Remove(ctx, []store.BlobRef{{UserID: f.usr.ID, SHA256: textSum, LocationID: f.loc.ID}})
	for _, p := range []string{raw + zstSuffix, cp} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s left after remove", p)
		}
	}
}
