package pack

import (
	"bytes"
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// minHeaderLength is the smallest trailer length field: pack_nonce plus a
// sealed header with one entry.
const minHeaderLength = NonceSize + vdcrypt.EnvelopeOverhead + headerFixed + EntrySize

// ParseTrailer validates the 12-byte trailer and returns its length field
// L = 16 + len(sealed_header). size is the full pack size.
func ParseTrailer(trailer []byte, size int64) (uint32, error) {
	if len(trailer) != TrailerSize {
		return 0, fmt.Errorf("%w: trailer is %d bytes", ErrTruncated, len(trailer))
	}
	if !bytes.Equal(trailer[4:11], magic[:]) {
		return 0, ErrTrailer
	}
	if trailer[11] != Version {
		return 0, fmt.Errorf("%w: %d", ErrVersion, trailer[11])
	}
	l := binary.LittleEndian.Uint32(trailer[:4])
	if l < minHeaderLength || (l-minHeaderLength)%EntrySize != 0 {
		return 0, fmt.Errorf("%w: %d", ErrHeaderLength, l)
	}
	// At least one sealed blob must precede the header.
	if int64(l) > size-TrailerSize-minSealedBlob {
		return 0, fmt.Errorf("%w: header length %d in a %d-byte pack", ErrTruncated, l, size)
	}
	return l, nil
}

// Reader reads one pack through an io.ReaderAt. NewReader fetches only the
// trailer and the header (two ranged reads); Blob fetches one blob.
type Reader struct {
	r       io.ReaderAt
	kr      *Keyring
	Class   Class
	Epoch   uint32 // epoch of the header envelope
	Nonce   [NonceSize]byte
	Entries []Entry // in header order
	Size    int64
	DataEnd int64 // end of the blob area (= start of pack_nonce)
}

// NewReader parses the trailer, opens the header of a pack of class c and
// validates every entry: types match the class, the blobs tile the data
// area exactly, ids are unique and raw lengths are within §7.2 limits.
func NewReader(r io.ReaderAt, size int64, kr *Keyring, c Class) (*Reader, error) {
	if !c.valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidType, c)
	}
	if size > MaxSize {
		return nil, fmt.Errorf("%w: %d bytes", ErrPackTooLarge, size)
	}
	if size < TrailerSize+minHeaderLength+minSealedBlob {
		return nil, fmt.Errorf("%w: pack is %d bytes", ErrTruncated, size)
	}
	tr := make([]byte, TrailerSize)
	if err := readFull(r, tr, size-TrailerSize); err != nil {
		return nil, err
	}
	l, err := ParseTrailer(tr, size)
	if err != nil {
		return nil, err
	}
	hdrStart := size - TrailerSize - int64(l)
	region := make([]byte, l)
	if err := readFull(r, region, hdrStart); err != nil {
		return nil, err
	}
	pr := &Reader{r: r, kr: kr, Class: c, Size: size, DataEnd: hdrStart}
	copy(pr.Nonce[:], region[:NonceSize])
	sh := region[NonceSize:]
	if pr.Epoch, err = vdcrypt.EnvelopeEpoch(sh); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHeaderAuth, err)
	}
	key, err := kr.key(c, pr.Epoch)
	if err != nil {
		return nil, err
	}
	plain, err := vdcrypt.Open(key, pr.Epoch, sh, vdcrypt.PackHeaderAD(kr.Repo, pr.Nonce[:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrHeaderAuth, err)
	}
	if pr.Entries, err = ParseHeader(plain, c, hdrStart); err != nil {
		return nil, err
	}
	return pr, nil
}

// ParseHeader decodes and validates header_plain for a pack of class c
// whose blob area is [0, dataEnd).
func ParseHeader(plain []byte, c Class, dataEnd int64) ([]Entry, error) {
	if len(plain) < headerFixed+EntrySize {
		return nil, fmt.Errorf("%w: %d bytes", ErrHeader, len(plain))
	}
	if Class(plain[0]) != c {
		return nil, fmt.Errorf("%w: header class %d, expected %s", ErrClass, plain[0], c)
	}
	count := binary.LittleEndian.Uint32(plain[1:5])
	if count == 0 {
		return nil, ErrEmpty
	}
	if uint64(len(plain)-headerFixed) != uint64(count)*EntrySize {
		return nil, fmt.Errorf("%w: count %d does not match %d bytes", ErrHeader, count, len(plain))
	}
	entries := make([]Entry, count)
	ids := make(map[BlobID]struct{}, count)
	p := plain[headerFixed:]
	for i := range entries {
		e := &entries[i]
		e.Type = BlobType(p[0])
		copy(e.ID[:], p[1:1+IDSize])
		q := p[1+IDSize:]
		e.Offset = binary.LittleEndian.Uint32(q[0:4])
		e.Length = binary.LittleEndian.Uint32(q[4:8])
		e.RawLength = binary.LittleEndian.Uint32(q[8:12])
		p = p[EntrySize:]
		tc, err := e.Type.Class()
		if err != nil {
			return nil, fmt.Errorf("%w: entry %d: %w", ErrHeader, i, err)
		}
		if tc != c {
			return nil, fmt.Errorf("%w: entry %d type %d in %s pack", ErrClass, i, e.Type, c)
		}
		if int64(e.RawLength) > int64(e.Type.MaxRaw()) {
			return nil, fmt.Errorf("%w: entry %d raw_len %d", ErrBlobTooLarge, i, e.RawLength)
		}
		// Writers store raw when compression does not pay, so a payload
		// never exceeds 1 + max raw size.
		if e.Length < minSealedBlob || int64(e.Length) > int64(e.Type.MaxRaw())+minSealedBlob {
			return nil, fmt.Errorf("%w: entry %d sealed_len %d", ErrHeader, i, e.Length)
		}
		if _, dup := ids[e.ID]; dup {
			return nil, fmt.Errorf("%w: %s", ErrDuplicate, e.ID)
		}
		ids[e.ID] = struct{}{}
	}
	// The blobs must tile [0, dataEnd) exactly (§7.3 layout); header
	// order is not required to be offset order.
	sorted := slices.Clone(entries)
	slices.SortFunc(sorted, func(a, b Entry) int { return cmp.Compare(a.Offset, b.Offset) })
	var pos int64
	for _, e := range sorted {
		if int64(e.Offset) != pos {
			return nil, fmt.Errorf("%w: blob at %d, expected %d", ErrBlobBounds, e.Offset, pos)
		}
		pos += int64(e.Length)
	}
	if pos != dataEnd {
		return nil, fmt.Errorf("%w: blobs end at %d, data area ends at %d", ErrBlobBounds, pos, dataEnd)
	}
	return entries, nil
}

// Find returns the header entry for id.
func (pr *Reader) Find(id BlobID) (Entry, bool) {
	for _, e := range pr.Entries {
		if e.ID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// Blob reads, opens and verifies the blob described by e, which must lie
// inside this pack's data area and belong to its class.
func (pr *Reader) Blob(e Entry) ([]byte, error) {
	if c, err := e.Type.Class(); err != nil {
		return nil, err
	} else if c != pr.Class {
		return nil, fmt.Errorf("%w: type %d in %s pack", ErrClass, e.Type, pr.Class)
	}
	if int64(e.Offset)+int64(e.Length) > pr.DataEnd {
		return nil, fmt.Errorf("%w: [%d,+%d) beyond %d", ErrBlobBounds, e.Offset, e.Length, pr.DataEnd)
	}
	return ReadBlob(pr.r, pr.kr, e)
}

// ReadBlob reads one blob straight from a pack using only its index entry
// (one ranged read of e.Length bytes at e.Offset), then opens and verifies
// it. Use it when the header is not needed.
func ReadBlob(r io.ReaderAt, kr *Keyring, e Entry) ([]byte, error) {
	if _, err := e.Type.Class(); err != nil {
		return nil, err
	}
	if int64(e.RawLength) > int64(e.Type.MaxRaw()) {
		return nil, fmt.Errorf("%w: raw_len %d, type %d", ErrBlobTooLarge, e.RawLength, e.Type)
	}
	if e.Length < minSealedBlob || int64(e.Length) > int64(e.Type.MaxRaw())+minSealedBlob {
		return nil, fmt.Errorf("%w: sealed_len %d", ErrBlobBounds, e.Length)
	}
	buf := make([]byte, e.Length)
	if err := readFull(r, buf, int64(e.Offset)); err != nil {
		return nil, err
	}
	return OpenBlob(kr, e, buf)
}

func readFull(r io.ReaderAt, p []byte, off int64) error {
	n, err := r.ReadAt(p, off)
	if n == len(p) {
		return nil
	}
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: read %d of %d bytes at %d", ErrTruncated, n, len(p), off)
	}
	return fmt.Errorf("pack: read at %d: %w", off, err)
}
