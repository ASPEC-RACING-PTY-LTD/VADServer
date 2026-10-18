// Package compress implements the blob payload encoding of the VaultDock
// repository format v1 (backup-format-v1.md §7.2):
//
//	payload = comp_u8 || body
//
// comp = 0 stores the raw bytes; comp = 1 stores a zstd (level 3) frame.
// Writers store raw when the compressed body is ≥ 0.97 × raw, or when the
// caller marks the content as already compressed. Decoders never inflate
// beyond the caller-supplied raw length, which bounds memory against
// decompression bombs.
//
// Index and snapshot objects (§7.4, §8.1) use the same `0x01 || zstd(JSON)`
// framing; use EncodeZstd and DecodeLimit for them.
//
// All functions are safe for concurrent use. Encoders and decoders are pooled.
package compress

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/klauspost/compress/zstd"
)

// Compression flag values (the first payload byte).
const (
	CompRaw  byte = 0
	CompZstd byte = 1
)

// Level is the zstd level mandated by §7.2.
const Level = 3

// Raw size limits per blob type (§7.2).
const (
	MaxDataRaw = 8 << 20  // data blobs (type 0)
	MaxMetaRaw = 32 << 20 // tree and meta blobs (types 1 and 2)
)

// Errors returned by the decoder.
var (
	ErrEmpty        = errors.New("compress: empty payload")
	ErrUnknownComp  = errors.New("compress: unknown compression flag")
	ErrTooLarge     = errors.New("compress: raw size exceeds limit")
	ErrSizeMismatch = errors.New("compress: decoded size does not match raw_len")
	ErrCorrupt      = errors.New("compress: corrupt zstd body")
)

var encPool = sync.Pool{New: func() any {
	e, err := zstd.NewWriter(nil,
		zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(Level)),
		zstd.WithEncoderConcurrency(1),
	)
	if err != nil {
		panic("compress: zstd encoder: " + err.Error())
	}
	return e
}}

var decPool = sync.Pool{New: func() any {
	d, err := zstd.NewReader(nil,
		zstd.WithDecoderConcurrency(1),
		zstd.WithDecoderMaxMemory(MaxMetaRaw),
		zstd.WithDecoderMaxWindow(MaxMetaRaw),
	)
	if err != nil {
		panic("compress: zstd decoder: " + err.Error())
	}
	return d
}}

// keepRaw reports whether a compressed body of length c is not worth
// storing for raw length r (c ≥ 0.97 × r).
func keepRaw(c, r int) bool {
	return int64(c)*100 >= int64(r)*97
}

// Encode returns the payload for raw. When precompressed is true (a known
// compressed content type) the bytes are stored raw without trying zstd.
// Otherwise zstd is used only if it saves more than 3 %.
func Encode(raw []byte, precompressed bool) []byte {
	return AppendEncode(nil, raw, precompressed)
}

// AppendEncode is Encode that appends the payload to dst.
func AppendEncode(dst, raw []byte, precompressed bool) []byte {
	if !precompressed && len(raw) > 0 {
		start := len(dst)
		dst = append(dst, CompZstd)
		dst = zstdAppend(dst, raw)
		if !keepRaw(len(dst)-start-1, len(raw)) {
			return dst
		}
		dst = dst[:start]
	}
	dst = append(dst, CompRaw)
	return append(dst, raw...)
}

// EncodeZstd always returns CompZstd || zstd(raw), as required for index and
// snapshot objects (`0x01 || zstd(JSON)`).
func EncodeZstd(raw []byte) []byte {
	return zstdAppend([]byte{CompZstd}, raw)
}

func zstdAppend(dst, raw []byte) []byte {
	e := encPool.Get().(*zstd.Encoder)
	dst = e.EncodeAll(raw, dst)
	encPool.Put(e)
	return dst
}

// Decode decodes a blob payload whose raw length is known from the pack
// header / index (raw_len). It never produces more than rawLen bytes and
// fails unless the output is exactly rawLen bytes. rawLen must not exceed
// MaxMetaRaw; callers should additionally check the per-type limit
// (MaxDataRaw for data blobs).
func Decode(payload []byte, rawLen int) ([]byte, error) {
	if rawLen < 0 || rawLen > MaxMetaRaw {
		return nil, ErrTooLarge
	}
	out, err := decode(payload, rawLen)
	if err != nil {
		return nil, err
	}
	if len(out) != rawLen {
		return nil, ErrSizeMismatch
	}
	return out, nil
}

// DecodeLimit decodes a payload whose raw length is unknown (index and
// snapshot objects), failing with ErrTooLarge if it would exceed max bytes.
// max is clamped to MaxMetaRaw.
func DecodeLimit(payload []byte, max int) ([]byte, error) {
	if max < 0 || max > MaxMetaRaw {
		max = MaxMetaRaw
	}
	return decode(payload, max)
}

func decode(payload []byte, max int) ([]byte, error) {
	if len(payload) == 0 {
		return nil, ErrEmpty
	}
	body := payload[1:]
	switch payload[0] {
	case CompRaw:
		if len(body) > max {
			return nil, ErrTooLarge
		}
		return bytes.Clone(body), nil
	case CompZstd:
		return zstdDecode(body, max)
	default:
		return nil, fmt.Errorf("%w: %d", ErrUnknownComp, payload[0])
	}
}

// zstdDecode streams body through a pooled decoder, reading at most max+1
// bytes so that an oversized frame is detected without inflating it.
func zstdDecode(body []byte, max int) ([]byte, error) {
	if len(body) == 0 {
		return nil, ErrCorrupt
	}
	// Reject early when the frame header declares an oversized content size.
	var h zstd.Header
	if err := h.Decode(body); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if h.HasFCS && h.FrameContentSize > uint64(max) {
		return nil, ErrTooLarge
	}

	d := decPool.Get().(*zstd.Decoder)
	defer func() {
		_ = d.Reset(nil) // drop the reference to body before pooling
		decPool.Put(d)
	}()
	if err := d.Reset(bytes.NewReader(body)); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}

	capHint := max
	if h.HasFCS {
		capHint = int(h.FrameContentSize)
	}
	buf := bytes.NewBuffer(make([]byte, 0, min(capHint, max)+1))
	n, err := buf.ReadFrom(io.LimitReader(d, int64(max)+1))
	if err != nil {
		if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
			return nil, ErrTooLarge
		}
		return nil, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if n > int64(max) {
		return nil, ErrTooLarge
	}
	return buf.Bytes(), nil
}
