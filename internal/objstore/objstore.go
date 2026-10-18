// Package objstore defines a small object-store interface for the server-side
// backup repositories (backup-format-v1 §2) and the key rules every backend
// shares. Backends: localfs (this phase); S3-compatible stores (AWS, R2, B2,
// MinIO, Garage) are added in Phase 4 behind the same interface.
//
// Keys are slash-separated relative paths such as
// "repo/<uuid>/data/ab/<oid>". They map one-to-one onto backend names, so the
// format's own fan-out (data/<aa>/<oid>) is the only fan-out.
//
// Optional capabilities (object lock, server-side copy, presigning) belong in
// separate small interfaces discovered by type assertion, so Store never
// grows.
package objstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNotFound     = errors.New("object not found")
	ErrExists       = errors.New("object already exists with different content")
	ErrHashMismatch = errors.New("content hash does not match")
	ErrSizeMismatch = errors.New("content size does not match")
	ErrInvalidKey   = errors.New("invalid object key")
	ErrInvalidRange = errors.New("invalid byte range")
	ErrUnsafePath   = errors.New("object path is not a plain file or directory")
)

const (
	// MaxKeyLen bounds a key's length in bytes.
	MaxKeyLen = 512
	// MaxSegmentLen bounds one path segment of a key.
	MaxSegmentLen = 128
	// DefaultListLimit is the page size used when List is given limit <= 0.
	DefaultListLimit = 1000
	// MaxListLimit caps the page size of List.
	MaxListLimit = 1000
)

// ObjectInfo describes a stored object.
type ObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ListPage is one page of a List call. NextCursor is empty on the last page.
type ListPage struct {
	Objects    []ObjectInfo
	NextCursor string
}

// Store is a flat namespace of immutable objects.
type Store interface {
	// Put stores exactly size bytes from r under key. The content must hash
	// to sha256hex (lower-case hex), otherwise ErrHashMismatch (or
	// ErrSizeMismatch) is returned and nothing becomes visible. The write is
	// atomic. Putting identical content to an existing key succeeds
	// (idempotent); different content returns ErrExists.
	Put(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) error
	// Get opens the whole object and returns its size.
	Get(ctx context.Context, key string) (io.ReadCloser, int64, error)
	// GetRange opens length bytes starting at off. length < 0 reads to the
	// end; a range running past the end is clamped. off must be within
	// [0, size], otherwise ErrInvalidRange.
	GetRange(ctx context.Context, key string, off, length int64) (io.ReadCloser, error)
	// Stat returns the object's metadata or ErrNotFound.
	Stat(ctx context.Context, key string) (ObjectInfo, error)
	// Delete removes key. Deleting a missing key is not an error.
	Delete(ctx context.Context, key string) error
	// List returns objects whose keys start with prefix, in byte-wise
	// lexicographic key order, starting strictly after cursor (empty for
	// the first page). limit <= 0 means DefaultListLimit; larger values are
	// capped at MaxListLimit.
	List(ctx context.Context, prefix, cursor string, limit int) (ListPage, error)
}

var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ValidSHA256 reports whether s is a lower-case hex SHA-256.
func ValidSHA256(s string) bool { return shaPattern.MatchString(s) }

// segChar is the key charset allow-list: lower-case only, so keys never
// collide on case-insensitive filesystems.
func segChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.'
}

// Windows device names, refused as a segment's base name on every platform.
var reservedNames = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"com0": true, "com1": true, "com2": true, "com3": true, "com4": true,
	"com5": true, "com6": true, "com7": true, "com8": true, "com9": true,
	"lpt0": true, "lpt1": true, "lpt2": true, "lpt3": true, "lpt4": true,
	"lpt5": true, "lpt6": true, "lpt7": true, "lpt8": true, "lpt9": true,
}

// validSegment checks one key segment. partial allows the trailing,
// incomplete segment of a List prefix.
func validSegment(s string, partial bool) bool {
	if len(s) > MaxSegmentLen {
		return false
	}
	if s == "" {
		return partial
	}
	// A leading dot is reserved for backend temp files and excludes "." and
	// "..". A trailing dot is unrepresentable on Windows.
	if s[0] == '.' || (!partial && s[len(s)-1] == '.') {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !segChar(s[i]) {
			return false
		}
	}
	if partial {
		return true
	}
	base, _, _ := strings.Cut(s, ".")
	return !reservedNames[base]
}

// ValidateKey checks key against the shared key rules: non-empty, at most
// MaxKeyLen bytes, '/'-separated non-empty segments of [a-z0-9._-], no
// segment starting or ending with '.', no Windows device names. It returns
// an error wrapping ErrInvalidKey.
func ValidateKey(key string) error {
	if key == "" || len(key) > MaxKeyLen {
		return invalidKey(key)
	}
	for _, seg := range strings.Split(key, "/") {
		if !validSegment(seg, false) {
			return invalidKey(key)
		}
	}
	return nil
}

// ValidatePrefix checks a List prefix: empty, or a key prefix whose complete
// segments are valid and whose last segment may be partial or empty (a
// trailing '/').
func ValidatePrefix(prefix string) error {
	if prefix == "" {
		return nil
	}
	if len(prefix) > MaxKeyLen {
		return invalidKey(prefix)
	}
	segs := strings.Split(prefix, "/")
	for i, seg := range segs {
		if !validSegment(seg, i == len(segs)-1) {
			return invalidKey(prefix)
		}
	}
	return nil
}

func invalidKey(key string) error {
	if len(key) > 80 {
		key = key[:80] + "..."
	}
	return fmt.Errorf("%w: %q", ErrInvalidKey, key)
}
