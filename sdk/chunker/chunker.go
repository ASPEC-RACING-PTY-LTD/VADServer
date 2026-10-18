// Package chunker implements the content-defined chunker vd-fastcdc-1 of
// backup-format-v1 §9: normalised FastCDC with a gear table keyed by
// K_chunker, min 512 KiB, avg 1 MiB, max 8 MiB.
//
// Cut points depend only on the input bytes and the key, never on how the
// input is split into reads.
package chunker

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// Name is the config identifier of this chunker.
const Name = "vd-fastcdc-1"

// Chunk size parameters and normalisation masks (§9).
const (
	MinSize = 512 << 10
	AvgSize = 1 << 20
	MaxSize = 8 << 20

	// MaskS applies before AvgSize (harder to match), MaskL after it.
	MaskS uint64 = 0xFFFFFC0000000000
	MaskL uint64 = 0xFFFFC00000000000
)

// bufSize is the reusable window. Two max-size chunks keep compaction
// (moving the unconsumed tail to the front) to about one copy per byte.
const bufSize = 2 * MaxSize

// maxEmptyReads bounds consecutive (0, nil) reads before giving up.
const maxEmptyReads = 100

// Gear is the keyed gear table.
type Gear [256]uint64

// NewGear derives the table: gear[i] = u64_le(HMAC-SHA256(K_chunker,
// "gear" || u8(i))[0:8]).
func NewGear(kChunker vdcrypt.Key) *Gear {
	var g Gear
	mac := hmac.New(sha256.New, kChunker[:])
	msg := []byte("gear\x00")
	var sum [sha256.Size]byte
	for i := range g {
		msg[4] = byte(i)
		mac.Reset()
		mac.Write(msg)
		g[i] = binary.LittleEndian.Uint64(mac.Sum(sum[:0])[:8])
	}
	return &g
}

// Cut returns the length of the first chunk of data. data must hold either
// at least MaxSize bytes or everything that remains of the input; with a
// shorter prefix the result is not the §9 cut point.
func (g *Gear) Cut(data []byte) int {
	n := len(data)
	if n <= MinSize {
		return n
	}
	if n > MaxSize {
		n = MaxSize
	}
	normal := AvgSize
	if n < normal {
		normal = n
	}
	data = data[:n]
	var h uint64
	i := MinSize
	for ; i < normal; i++ {
		h = (h << 1) + g[data[i]]
		if h&MaskS == 0 {
			return i + 1
		}
	}
	for ; i < n; i++ {
		h = (h << 1) + g[data[i]]
		if h&MaskL == 0 {
			return i + 1
		}
	}
	return n
}

// Boundaries returns the chunk lengths of data, which is treated as one
// whole file. An empty input has no chunks.
func (g *Gear) Boundaries(data []byte) []int {
	var out []int
	for len(data) > 0 {
		n := g.Cut(data)
		out = append(out, n)
		data = data[n:]
	}
	return out
}

// Split returns the chunks of data as sub-slices of data (no copying).
func (g *Gear) Split(data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := g.Cut(data)
		out = append(out, data[:n:n])
		data = data[n:]
	}
	return out
}

// Chunk is one chunk returned by Chunker.Next.
type Chunk struct {
	Offset uint64 // position of the first byte in the stream
	Length int
	// Data aliases the chunker's buffer and is valid only until the next
	// call to Next or Reset. Copy it to keep it.
	Data []byte
}

// ErrNoProgress is returned when the reader repeatedly returns no data and
// no error.
var ErrNoProgress = errors.New("chunker: reader makes no progress")

// Chunker splits a stream (one file) into chunks. It is not safe for
// concurrent use. Reset lets one Chunker, and its buffer, serve many files.
type Chunker struct {
	gear       *Gear
	r          io.Reader
	buf        []byte
	start, end int // unconsumed window buf[start:end]
	offset     uint64
	eof        bool
	err        error
}

// New returns a Chunker reading from r with gear table g.
func New(r io.Reader, g *Gear) *Chunker {
	c := &Chunker{gear: g}
	c.Reset(r)
	return c
}

// Reset starts a new stream from r, keeping the buffer.
func (c *Chunker) Reset(r io.Reader) {
	c.r = r
	c.start, c.end = 0, 0
	c.offset = 0
	c.eof = false
	c.err = nil
}

// Next returns the next chunk, or io.EOF after the last one. A read error
// is returned as is; the chunker then stays in that error state.
func (c *Chunker) Next() (Chunk, error) {
	if c.err != nil {
		return Chunk{}, c.err
	}
	if err := c.fill(); err != nil {
		c.err = err
		return Chunk{}, err
	}
	if c.start == c.end {
		c.err = io.EOF
		return Chunk{}, io.EOF
	}
	window := c.buf[c.start:c.end]
	n := c.gear.Cut(window)
	ch := Chunk{Offset: c.offset, Length: n, Data: window[:n:n]}
	c.start += n
	c.offset += uint64(n)
	return ch, nil
}

// fill makes the window hold at least MaxSize bytes or the rest of the
// stream.
func (c *Chunker) fill() error {
	if c.eof || c.end-c.start >= MaxSize {
		return nil
	}
	if c.buf == nil {
		c.buf = make([]byte, bufSize)
	}
	if len(c.buf)-c.start < MaxSize {
		c.end = copy(c.buf, c.buf[c.start:c.end])
		c.start = 0
	}
	empty := 0
	for c.end-c.start < MaxSize {
		n, err := c.r.Read(c.buf[c.end:])
		c.end += n
		if err == io.EOF {
			c.eof = true
			return nil
		}
		if err != nil {
			return err
		}
		if n == 0 {
			if empty++; empty >= maxEmptyReads {
				return ErrNoProgress
			}
		} else {
			empty = 0
		}
	}
	return nil
}
