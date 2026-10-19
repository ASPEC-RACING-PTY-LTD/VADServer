package storage

// Compression of freshly uploaded files. A client that sends chunks
// zstd-compressed (upload.zstd) has already done the work, so the received
// frames are kept beside the upload's temp file (<id>.part.frames/<index>)
// and, once the file is stored, joined into its compressed form instead of
// compressing it again. Chunks a compressing client sent raw did not shrink,
// so they are not tried again either. Files a client sent raw are compressed
// by the server as a whole after they are stored.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
)

const framesSuffix = ".frames"

// FramesDir is where the compressed frames of the upload whose temp file is
// tempPath are kept.
func FramesDir(tempPath string) string { return tempPath + framesSuffix }

// RemoveTemp deletes an upload's temp file and its kept frames.
func RemoveTemp(tempPath string) {
	_ = os.Remove(tempPath)
	_ = os.RemoveAll(FramesDir(tempPath))
}

// FrameWriter stages one received compressed chunk; Keep makes it the
// chunk's frame, Discard drops it.
type FrameWriter struct {
	f         *os.File
	tmp, dest string
}

// NewFrameWriter starts staging the frame of chunk idx of the upload whose
// temp file is tempPath.
func NewFrameWriter(tempPath string, idx int64) (*FrameWriter, error) {
	dir := FramesDir(tempPath)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	var rnd [6]byte
	_, _ = rand.Read(rnd[:])
	dest := filepath.Join(dir, strconv.FormatInt(idx, 10))
	tmp := dest + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	return &FrameWriter{f: f, tmp: tmp, dest: dest}, nil
}

func (w *FrameWriter) Write(p []byte) (int, error) { return w.f.Write(p) }

// Keep stores the staged frame as the chunk's frame.
func (w *FrameWriter) Keep() error {
	if err := w.f.Close(); err != nil {
		_ = os.Remove(w.tmp)
		return err
	}
	if err := os.Rename(w.tmp, w.dest); err != nil {
		_ = os.Remove(w.tmp)
		return err
	}
	return nil
}

// Discard drops the staged frame.
func (w *FrameWriter) Discard() {
	_ = w.f.Close()
	_ = os.Remove(w.tmp)
}

// DropFrame forgets the kept frame of chunk idx (it was received raw).
func DropFrame(tempPath string, idx int64) {
	_ = os.Remove(filepath.Join(FramesDir(tempPath), strconv.FormatInt(idx, 10)))
}

// UploadedFile describes a stored upload to compress.
type UploadedFile struct {
	UserID    int64
	SHA256    string
	Size      int64
	ChunkSize int64
	// Frames is the upload's frames directory (FramesDir); it is removed.
	Frames string
	// ClientCompressed: chunks without a frame did not compress on the
	// client and are not tried again.
	ClientCompressed bool
}

// CompressUploaded stores the blob of a just-uploaded file compressed when
// that saves at least 3 %, reusing the client's frames. It always removes
// the frames directory. A blob that is already compressed, marked
// incompressible or gone is left alone.
func (m *Manager) CompressUploaded(ctx context.Context, f UploadedFile) (done bool, err error) {
	defer os.RemoveAll(f.Frames)
	loc, err := m.st.BlobLocation(ctx, f.UserID, f.SHA256)
	if err != nil {
		return false, nil // not stored (or already deleted)
	}
	p, err := m.BlobPath(loc, f.UserID, f.SHA256)
	if err != nil {
		return false, err
	}
	if !f.ClientCompressed {
		_, _, done, err = m.compressBlob(f.UserID, f.SHA256, p)
		return done, err
	}
	unlock := m.lock(f.UserID, f.SHA256)
	defer unlock()
	fi, err := os.Lstat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() != f.Size || f.ChunkSize <= 0 {
		return false, nil // compressed already, removed, or not the uploaded content
	}
	if fi.Size() < minCompressSize || regular(p+noZstSuffix) {
		return false, nil
	}
	chunks := (f.Size + f.ChunkSize - 1) / f.ChunkSize
	frame := func(i int64) string { return filepath.Join(f.Frames, strconv.FormatInt(i, 10)) }
	// Without a single useful frame the client found nothing to compress.
	var estimate int64
	for i := int64(0); i < chunks; i++ {
		if st, err := os.Lstat(frame(i)); err == nil && st.Mode().IsRegular() {
			estimate += st.Size()
		} else {
			estimate += min(f.ChunkSize, f.Size-i*f.ChunkSize)
		}
	}
	if !compress.Worth(int(min(estimate, 1<<62)), int(min(f.Size, 1<<62))) {
		_ = os.WriteFile(p+noZstSuffix, nil, 0o640)
		return false, nil
	}
	tmp := p + zstSuffix + ".tmp"
	n, err := assembleFrames(tmp, p, f, chunks, frame)
	if err == nil {
		// The frames were checked chunk by chunk on receipt; check the whole
		// result too before the raw file goes.
		err = verifyFrames(tmp, f.SHA256)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	if !compress.Worth(int(min(n, 1<<62)), int(min(f.Size, 1<<62))) {
		_ = os.Remove(tmp)
		_ = os.WriteFile(p+noZstSuffix, nil, 0o640)
		return false, nil
	}
	if err := os.Rename(tmp, p+zstSuffix); err != nil {
		_ = os.Remove(tmp)
		return false, err
	}
	syncDir(filepath.Dir(p))
	if err := os.Remove(p); err != nil {
		_ = os.Remove(p + zstSuffix) // the raw file wins on read
		return false, err
	}
	return true, nil
}

// assembleFrames writes the compressed form of raw blob p to tmp: the
// client's frame of each chunk, or the chunk compressed here where it has
// none. It returns the size written.
func assembleFrames(tmp, p string, f UploadedFile, chunks int64, frame func(int64) string) (int64, error) {
	raw, err := os.Open(p)
	if err != nil {
		return 0, err
	}
	defer raw.Close()
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return 0, err
	}
	cw := &countWriter{w: out}
	buf := make([]byte, 1<<20)
	for i := int64(0); i < chunks; i++ {
		if err := func() error {
			if fr, err := os.Open(frame(i)); err == nil {
				defer fr.Close()
				_, err = io.CopyBuffer(cw, fr, buf)
				return err
			}
			zw, err := compress.NewFrameWriter(cw)
			if err != nil {
				return err
			}
			length := min(f.ChunkSize, f.Size-i*f.ChunkSize)
			if _, err := io.CopyBuffer(zw, io.NewSectionReader(raw, i*f.ChunkSize, length), buf); err != nil {
				zw.Close()
				return err
			}
			return zw.Close()
		}(); err != nil {
			out.Close()
			return 0, err
		}
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return 0, err
	}
	return cw.n, out.Close()
}

// verifyFrames checks that the zstd stream in p decodes to content with
// the given sha256.
func verifyFrames(p, sha string) error {
	in, err := os.Open(p)
	if err != nil {
		return err
	}
	defer in.Close()
	zr, err := compress.NewFrameReader(in)
	if err != nil {
		return err
	}
	defer zr.Close()
	h := sha256.New()
	if _, err := io.CopyBuffer(h, zr, make([]byte, 1<<20)); err != nil {
		return fmt.Errorf("decode assembled frames: %w", err)
	}
	if hex.EncodeToString(h.Sum(nil)) != sha {
		return fmt.Errorf("%w: assembled frames of %s", ErrHashMismatch, sha)
	}
	return nil
}
