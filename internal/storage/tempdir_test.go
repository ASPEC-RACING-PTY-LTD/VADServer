package storage

import (
	"os"
	"path/filepath"
	"testing"
)

// A location whose tmp directory is missing (moved without it) still accepts
// uploads: TempPath recreates the directory.
func TestTempPathRecreatesTmpDir(t *testing.T) {
	f := newFixture(t)
	if err := os.RemoveAll(filepath.Join(f.loc.Path, "tmp")); err != nil {
		t.Fatal(err)
	}
	p, err := f.m.TempPath(f.loc.ID, "abcdefghijklmnopqrstuvwx")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o640); err != nil {
		t.Fatalf("temp file not creatable: %v", err)
	}
}
