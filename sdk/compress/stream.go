package compress

import (
	"io"

	"github.com/klauspost/compress/zstd"
)

// Plain zstd frames (no comp byte) for transport and at-rest file
// compression outside the repository format: sync uploads send chunks as
// "Content-Encoding: zstd" (api-v1 upload.zstd), and the server may keep
// stored files as zstd frames.

// Frame returns raw as one zstd frame (level 3, the content size recorded).
func Frame(raw []byte) []byte { return zstdAppend(nil, raw) }

// Worth reports whether a compressed form of n raw bytes is worth sending
// instead of the raw bytes (the same 0.97 rule as blob payloads).
func Worth(compressed, raw int) bool { return !keepRaw(compressed, raw) }

// NewFrameReader decodes a zstd stream from r. Callers bound the output
// themselves (io.LimitReader) and must Close the reader.
func NewFrameReader(r io.Reader) (io.ReadCloser, error) {
	d, err := zstd.NewReader(r, zstd.WithDecoderConcurrency(1), zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxWindow(64<<20))
	if err != nil {
		return nil, err
	}
	return d.IOReadCloser(), nil
}

// NewFrameWriter encodes a zstd stream (level 3) to w; Close flushes the
// frame but does not close w.
func NewFrameWriter(w io.Writer) (io.WriteCloser, error) {
	return zstd.NewWriter(w, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderConcurrency(1))
}
