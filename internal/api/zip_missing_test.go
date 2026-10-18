package api_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestZipReportsMissingBlobs checks that a zip download never silently drops
// a file whose blob has vanished from storage (defect B2).
func TestZipReportsMissingBlobs(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	tmp := t.TempDir()

	keepPath, keepData := makeFile(t, tmp, "keep.txt", 4000)
	gonePath, goneData := makeFile(t, tmp, "gone.txt", 5000)
	res, err := dc.UploadFile(ctx, keepPath, "/docs/keep.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	keepID := nodeID(t, res.Node)
	res, err = dc.UploadFile(ctx, gonePath, "/docs/gone.txt", nil)
	if err != nil {
		t.Fatal(err)
	}
	goneID := nodeID(t, res.Node)

	// delete the second file's blob directly on disk
	sum := sha256.Sum256(goneData)
	sha := hex.EncodeToString(sum[:])
	removed := false
	filepath.WalkDir(e.storage, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == sha {
			if err := os.Remove(p); err != nil {
				t.Fatal(err)
			}
			removed = true
		}
		return nil
	})
	if !removed {
		t.Fatal("blob file not found on disk")
	}

	for _, tc := range []struct {
		name        string
		ids         string
		wantMissing string
		wantFiles   map[string][]byte
		wantPath    string
	}{
		{"one missing", fmt.Sprintf("%d,%d", keepID, goneID), "1", map[string][]byte{"keep.txt": keepData}, "gone.txt"},
		{"none missing", fmt.Sprintf("%d", keepID), "0", map[string][]byte{"keep.txt": keepData}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := alice.c.Get(e.ts.URL + "/api/v1/files/zip?ids=" + tc.ids)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				t.Fatalf("status %d: %s", resp.StatusCode, raw)
			}
			if got := resp.Header.Get("X-VD-Missing-Count"); got != tc.wantMissing {
				t.Fatalf("X-VD-Missing-Count = %q want %q", got, tc.wantMissing)
			}
			zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
			if err != nil {
				t.Fatal(err)
			}
			files := map[string][]byte{}
			for _, f := range zr.File {
				rc, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				b, _ := io.ReadAll(rc)
				rc.Close()
				files[f.Name] = b
			}
			manifest, hasManifest := files["MISSING_FILES.txt"]
			delete(files, "MISSING_FILES.txt")
			if len(files) != len(tc.wantFiles) {
				t.Fatalf("entries %v", zr.File)
			}
			for name, want := range tc.wantFiles {
				if !bytes.Equal(files[name], want) {
					t.Fatalf("%s content differs", name)
				}
			}
			if tc.wantPath == "" {
				if hasManifest {
					t.Fatalf("unexpected manifest: %s", manifest)
				}
				return
			}
			if !hasManifest {
				t.Fatal("MISSING_FILES.txt not in archive")
			}
			if !strings.Contains(string(manifest), tc.wantPath+"\tblob file not found on storage\n") {
				t.Fatalf("manifest does not list %s: %q", tc.wantPath, manifest)
			}
			if strings.Contains(string(manifest), "keep.txt") {
				t.Fatalf("manifest lists a present file: %q", manifest)
			}
		})
	}
}
