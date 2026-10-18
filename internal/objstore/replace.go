package objstore

import (
	"context"
	"io"
)

// Replacer is an optional capability: a store that can overwrite an object
// atomically. Callers discover it by type assertion on a Store.
//
// It exists for the one mutable object of a repository, its config
// (backup-format-v1 §5: changing escrow_mode rewrites config). Every other
// object stays immutable and goes through Store.Put.
type Replacer interface {
	// Replace stores exactly size bytes from r under key, replacing any
	// existing content. The content must hash to sha256hex, otherwise
	// ErrHashMismatch (or ErrSizeMismatch) is returned and the previous
	// content, if any, stays visible. Readers see either the old or the
	// new content in full, never a mix.
	Replace(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) error
}
