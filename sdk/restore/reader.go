package restore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// BlobReader reads blob plaintext by id (*repo.Repo implements it; data
// blobs need a full key holder).
type BlobReader interface {
	ReadBlob(ctx context.Context, id pack.BlobID) ([]byte, error)
}

// BlobSizer reports a blob's plaintext length without reading it
// (*repo.Repo implements it). With it a FileReader can seek.
type BlobSizer interface {
	BlobSize(id pack.BlobID) (int64, bool)
}

// Reader errors.
var (
	ErrNotFile      = errors.New("restore: not a regular file")
	ErrHashMismatch = errors.New("restore: file content does not match its recorded hash")
	ErrSizeMismatch = errors.New("restore: file content does not match its recorded size")
	ErrNoSeek       = errors.New("restore: this source cannot seek")
)

// FileReader streams one file's content chunk by chunk. When the whole file
// is read sequentially from the start and a hash key was given, reaching the
// end verifies the keyed whole-file hash (backup-format §8.2 "h"); a
// mismatch is returned instead of io.EOF. It is an io.ReadSeeker when the
// source implements BlobSizer.
type FileReader struct {
	ctx   context.Context
	br    BlobReader
	ids   []pack.BlobID
	offs  []int64 // chunk start offsets; nil when sizes are unknown
	size  int64   // recorded size (entry "s")
	pos   int64
	cur   int // index of the chunk in buf, -1 for none
	buf   []byte
	start int64 // offset of buf[0]

	h      hash.Hash
	want   []byte
	hashed int64 // bytes fed to h, always a prefix of the file
	failed error
}

// NewFileReader opens the content of file entry e. kfh is the repository's
// K_fh; nil skips hash verification (size is still checked).
func NewFileReader(ctx context.Context, br BlobReader, kfh *vdcrypt.Key, e snapshot.Entry) (*FileReader, error) {
	if e.T != snapshot.TypeFile {
		return nil, ErrNotFile
	}
	fr := &FileReader{ctx: ctx, br: br, size: e.S, cur: -1}
	for _, c := range e.Ch {
		raw, err := hex.DecodeString(c)
		if err != nil || len(raw) != pack.IDSize {
			return nil, fmt.Errorf("restore: bad chunk id %q", c)
		}
		var id pack.BlobID
		copy(id[:], raw)
		fr.ids = append(fr.ids, id)
	}
	if bs, ok := br.(BlobSizer); ok {
		offs := make([]int64, len(fr.ids)+1)
		known := true
		for i, id := range fr.ids {
			n, ok := bs.BlobSize(id)
			if !ok {
				known = false
				break
			}
			offs[i+1] = offs[i] + n
		}
		if known {
			if offs[len(fr.ids)] != e.S {
				return nil, ErrSizeMismatch
			}
			fr.offs = offs
		}
	}
	if kfh != nil && e.H != nil {
		want, err := hex.DecodeString(*e.H)
		if err != nil || len(want) != 32 {
			return nil, fmt.Errorf("restore: bad file hash %q", *e.H)
		}
		fr.h, fr.want = snapshot.NewFileHash(*kfh), want
	}
	return fr, nil
}

// Size returns the recorded file size.
func (fr *FileReader) Size() int64 { return fr.size }

// load makes chunk i current.
func (fr *FileReader) load(i int, start int64) error {
	if err := fr.ctx.Err(); err != nil {
		return err
	}
	b, err := fr.br.ReadBlob(fr.ctx, fr.ids[i])
	if err != nil {
		return err
	}
	if fr.offs != nil && int64(len(b)) != fr.offs[i+1]-fr.offs[i] {
		return ErrSizeMismatch
	}
	fr.cur, fr.buf, fr.start = i, b, start
	return nil
}

// Read implements io.Reader.
func (fr *FileReader) Read(p []byte) (int, error) {
	if fr.failed != nil {
		return 0, fr.failed
	}
	if fr.pos >= fr.size {
		return 0, fr.finish()
	}
	if fr.cur < 0 || fr.pos < fr.start || fr.pos >= fr.start+int64(len(fr.buf)) {
		i, start, err := fr.chunkAt(fr.pos)
		if err != nil {
			fr.failed = err
			return 0, err
		}
		if err := fr.load(i, start); err != nil {
			fr.failed = err
			return 0, err
		}
		if len(fr.buf) == 0 {
			fr.failed = ErrSizeMismatch
			return 0, fr.failed
		}
	}
	off := fr.pos - fr.start
	n := copy(p, fr.buf[off:])
	if fr.h != nil && fr.pos == fr.hashed {
		fr.h.Write(p[:n])
		fr.hashed += int64(n)
	}
	fr.pos += int64(n)
	return n, nil
}

// chunkAt returns the chunk holding offset pos and its start offset.
func (fr *FileReader) chunkAt(pos int64) (int, int64, error) {
	if fr.offs != nil {
		lo, hi := 0, len(fr.ids)-1
		for lo < hi { // last chunk whose start <= pos
			mid := (lo + hi + 1) / 2
			if fr.offs[mid] <= pos {
				lo = mid
			} else {
				hi = mid - 1
			}
		}
		return lo, fr.offs[lo], nil
	}
	// Sizes unknown: only sequential reads, chunk after chunk.
	next, start := 0, int64(0)
	if fr.cur >= 0 {
		next, start = fr.cur+1, fr.start+int64(len(fr.buf))
	}
	if pos != start || next >= len(fr.ids) {
		if next >= len(fr.ids) {
			return 0, 0, ErrSizeMismatch
		}
		return 0, 0, ErrNoSeek
	}
	return next, start, nil
}

// finish runs at EOF: verify the hash when the whole file was hashed.
func (fr *FileReader) finish() error {
	if fr.h != nil && fr.hashed == fr.size {
		sum := fr.h.Sum(nil)
		fr.h = nil
		if !equal(sum, fr.want) {
			fr.failed = ErrHashMismatch
			return fr.failed
		}
	}
	return io.EOF
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var v byte
	for i := range a {
		v |= a[i] ^ b[i]
	}
	return v == 0
}

// Seek implements io.Seeker (needs a BlobSizer source).
func (fr *FileReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = fr.pos + offset
	case io.SeekEnd:
		abs = fr.size + offset
	default:
		return 0, errors.New("restore: bad whence")
	}
	if abs < 0 {
		return 0, errors.New("restore: negative position")
	}
	// Without chunk sizes the position may only move before the first read
	// (http.ServeContent probes the size with SeekEnd, then rewinds).
	if fr.offs == nil && fr.cur >= 0 && abs != fr.pos {
		return 0, ErrNoSeek
	}
	fr.pos = abs
	return abs, nil
}

// Verified reports whether the whole file was read and its hash matched
// (always false without a hash key).
func (fr *FileReader) Verified() bool {
	return fr.want != nil && fr.h == nil && fr.failed == nil && fr.hashed == fr.size
}

// CopyFile writes the whole content of file entry e to w and verifies it.
func CopyFile(ctx context.Context, br BlobReader, kfh *vdcrypt.Key, e snapshot.Entry, w io.Writer) (int64, error) {
	fr, err := NewFileReader(ctx, br, kfh, e)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(w, fr)
	if err != nil {
		return n, err
	}
	if n != e.S {
		return n, ErrSizeMismatch
	}
	return n, nil
}
