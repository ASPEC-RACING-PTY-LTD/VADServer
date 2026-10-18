package localfs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/objstore"
)

var _ objstore.Replacer = (*Store)(nil)

// Replace implements objstore.Replacer. The new content is written and
// verified in a temp file next to the object, then renamed over it; the
// rename replaces the old file atomically on every supported platform.
func (s *Store) Replace(ctx context.Context, key string, r io.Reader, size int64, sha256hex string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := objstore.ValidateKey(key); err != nil {
		return err
	}
	if size < 0 {
		return fmt.Errorf("%w: negative size", objstore.ErrSizeMismatch)
	}
	if !objstore.ValidSHA256(sha256hex) {
		return fmt.Errorf("%w: expected hash is not lower-case hex SHA-256", objstore.ErrHashMismatch)
	}
	dst, err := s.path(key, true)
	if err != nil {
		return err
	}
	tmp, err := s.writeTemp(ctx, filepath.Dir(dst), r, size, sha256hex)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // no-op once renamed

	unlock := s.lock(key)
	defer unlock()
	fi, err := os.Lstat(dst)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() {
			return fmt.Errorf("%w: %s", objstore.ErrUnsafePath, key)
		}
	case !errors.Is(err, fs.ErrNotExist):
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		return err
	}
	syncDir(filepath.Dir(dst))
	return nil
}
