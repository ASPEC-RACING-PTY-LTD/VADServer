package replica

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestEncryptRoundTripAndTamper(t *testing.T) {
	key := make([]byte, 32)
	rand.Read(key)
	for _, size := range []int{0, 1, chunkSize - 1, chunkSize, chunkSize + 1, 3*chunkSize + 77} {
		plain := make([]byte, size)
		rand.Read(plain)
		var enc bytes.Buffer
		if err := Encrypt(&enc, bytes.NewReader(plain), key, BlobID(1, "abc")); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := Decrypt(&out, bytes.NewReader(enc.Bytes()), key, BlobID(1, "abc")); err != nil {
			t.Fatalf("size %d: %v", size, err)
		}
		if !bytes.Equal(out.Bytes(), plain) {
			t.Fatalf("size %d: round trip differs", size)
		}
		if size == 0 {
			continue
		}
		// Bound to the blob, the key, every byte and the full length.
		if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc.Bytes()), key, BlobID(2, "abc")); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("size %d: other blob id accepted: %v", size, err)
		}
		bad := append([]byte(nil), enc.Bytes()...)
		bad[len(bad)/2] ^= 1
		if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(bad), key, BlobID(1, "abc")); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("size %d: flipped byte accepted: %v", size, err)
		}
		if size > chunkSize {
			cut := enc.Bytes()[:12+4+chunkSize+16] // first chunk only
			if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(cut), key, BlobID(1, "abc")); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("size %d: truncated stream accepted: %v", size, err)
			}
		}
		other := make([]byte, 32)
		rand.Read(other)
		if err := Decrypt(&bytes.Buffer{}, bytes.NewReader(enc.Bytes()), other, BlobID(1, "abc")); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("size %d: other key accepted", size)
		}
	}
}

func TestRuleMatching(t *testing.T) {
	cases := []struct {
		pattern, path string
		want          bool
	}{
		{"/Medal/", "/Medal/Clips/a.mp4", true},
		{"/Medal/", "/Other/a.mp4", false},
		{"*.mp4", "/x/y/a.MP4", true},
		{"*.mp4", "/x/y/a.mkv", false},
		{"/Medal/*/*.mp4", "/Medal/Clips/a.mp4", true},
		{"/Medal/*/*.mp4", "/Medal/Clips/Sub/a.mp4", false},
		{"/Medal/**/*.mp4", "/Medal/Clips/Sub/a.mp4", true},
		{"/a.txt", "/a.txt", true},
	}
	for _, c := range cases {
		if got := Glob(c.pattern, c.path); got != c.want {
			t.Errorf("Glob(%q, %q) = %v", c.pattern, c.path, got)
		}
	}
	uid, loc := int64(2), int64(1)
	r := Rule{UserID: &uid, SourceLocationID: &loc, PathPattern: "/Medal/", Extensions: "mp4, .MKV", MinSize: 10, MaxSize: 100}
	f := File{UserID: 2, LocationID: 1, Path: "/Medal/a.mkv", Size: 50}
	if !r.Matches(f) {
		t.Fatal("expected a match")
	}
	for name, g := range map[string]File{
		"user": {UserID: 3, LocationID: 1, Path: "/Medal/a.mkv", Size: 50},
		"loc":  {UserID: 2, LocationID: 9, Path: "/Medal/a.mkv", Size: 50},
		"ext":  {UserID: 2, LocationID: 1, Path: "/Medal/a.avi", Size: 50},
		"min":  {UserID: 2, LocationID: 1, Path: "/Medal/a.mkv", Size: 5},
		"max":  {UserID: 2, LocationID: 1, Path: "/Medal/a.mkv", Size: 500},
		"path": {UserID: 2, LocationID: 1, Path: "/Other/a.mkv", Size: 50},
	} {
		if r.Matches(g) {
			t.Errorf("%s: unexpected match", name)
		}
	}
	if !(Rule{}).Matches(f) {
		t.Fatal("an empty rule matches everything")
	}
}
