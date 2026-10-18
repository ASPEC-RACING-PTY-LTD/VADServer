package pack

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// WriterOptions tunes a Writer. Zero values take the contract defaults.
type WriterOptions struct {
	Rand   io.Reader // nonces and shuffle; nil = crypto/rand
	Target int       // config.pack_target; 0 = TargetSize
	Max    int       // config.pack_max; 0 = MaxSize; never above MaxSize
}

// Pack is a finished pack.
type Pack struct {
	OID     string // hex(SHA-256(Bytes))
	Bytes   []byte
	Class   Class
	Epoch   uint32
	Entries []Entry // in pack (offset) order
}

type pendingBlob struct {
	e      Entry
	sealed []byte
}

// Writer accumulates blobs of one class under one epoch and seals them into
// a pack. It is not safe for concurrent use.
type Writer struct {
	repo        vdcrypt.RepoID
	kID         vdcrypt.Key
	ek          vdcrypt.EpochKeys
	class       Class
	rnd         io.Reader
	target, max int
	blobs       []pendingBlob
	ids         map[BlobID]struct{}
	dataLen     int
	done        bool
}

// NewWriter returns a writer for a pack of class c sealed under ek (the
// current epoch). kID is K_id.
func NewWriter(repo vdcrypt.RepoID, kID vdcrypt.Key, ek vdcrypt.EpochKeys, c Class, opts WriterOptions) (*Writer, error) {
	if !c.valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidType, c)
	}
	if _, err := classKey(ek, c); err != nil {
		return nil, err
	}
	if ek.Epoch == 0 {
		return nil, vdcrypt.ErrInvalidEpoch
	}
	w := &Writer{repo: repo, kID: kID, ek: ek, class: c, rnd: randReader(opts.Rand),
		target: opts.Target, max: opts.Max, ids: map[BlobID]struct{}{}}
	if w.max == 0 {
		w.max = MaxSize
	}
	if w.target == 0 {
		w.target = min(TargetSize, w.max)
	}
	if w.max < 0 || w.max > MaxSize || w.target <= 0 || w.target > w.max {
		return nil, fmt.Errorf("%w: target %d, max %d", ErrOptions, opts.Target, opts.Max)
	}
	return w, nil
}

// packSize is the final pack size with n blobs totalling dataLen bytes.
func packSize(n, dataLen int) int {
	return dataLen + NonceSize + vdcrypt.EnvelopeOverhead + headerFixed + n*EntrySize + TrailerSize
}

// Add seals raw as a blob of type t and queues it. It returns ErrPackFull
// (the blob is not added) when the pack would exceed pack_max: finish this
// pack and add the blob to a new one. Duplicate ids are rejected with
// ErrDuplicate; dedup is the caller's job.
func (w *Writer) Add(t BlobType, raw []byte, precompressed bool) (Entry, error) {
	if w.done {
		return Entry{}, ErrFinished
	}
	c, err := t.Class()
	if err != nil {
		return Entry{}, err
	}
	if c != w.class {
		return Entry{}, fmt.Errorf("%w: type %d in %s pack", ErrClass, t, w.class)
	}
	if len(raw) > t.MaxRaw() {
		return Entry{}, fmt.Errorf("%w: %d bytes, type %d", ErrBlobTooLarge, len(raw), t)
	}
	id := ComputeID(w.kID, raw)
	if _, dup := w.ids[id]; dup {
		return Entry{}, fmt.Errorf("%w: %s", ErrDuplicate, id)
	}
	e, sealed, err := SealBlob(w.rnd, w.repo, w.kID, w.ek, t, raw, precompressed)
	if err != nil {
		return Entry{}, err
	}
	if packSize(len(w.blobs)+1, w.dataLen+len(sealed)) > w.max {
		if len(w.blobs) == 0 {
			return Entry{}, fmt.Errorf("%w: sealed blob of %d bytes cannot fit pack_max %d", ErrBlobTooLarge, len(sealed), w.max)
		}
		return Entry{}, ErrPackFull
	}
	w.blobs = append(w.blobs, pendingBlob{e: e, sealed: sealed})
	w.ids[id] = struct{}{}
	w.dataLen += len(sealed)
	return e, nil
}

// Has reports whether a blob with id is already queued.
func (w *Writer) Has(id BlobID) bool { _, ok := w.ids[id]; return ok }

// Len returns the number of queued blobs.
func (w *Writer) Len() int { return len(w.blobs) }

// Size returns the size the pack would have if finished now.
func (w *Writer) Size() int { return packSize(len(w.blobs), w.dataLen) }

// Full reports whether the pack reached pack_target and should be sealed.
func (w *Writer) Full() bool { return w.Size() >= w.target }

// Finish shuffles the blobs, writes and seals the header and returns the
// pack. The writer cannot be used afterwards. Randomness is consumed in
// this order: one nonce per Add, then the shuffle, the pack_nonce and the
// header nonce.
func (w *Writer) Finish() (*Pack, error) {
	if w.done {
		return nil, ErrFinished
	}
	if len(w.blobs) == 0 {
		return nil, ErrEmpty
	}
	if err := shuffle(w.rnd, w.blobs); err != nil {
		return nil, err
	}
	size := w.Size()
	out := make([]byte, 0, size)
	hdr := make([]byte, headerFixed, headerFixed+len(w.blobs)*EntrySize)
	hdr[0] = byte(w.class)
	binary.LittleEndian.PutUint32(hdr[1:5], uint32(len(w.blobs)))
	entries := make([]Entry, len(w.blobs))
	for i := range w.blobs {
		b := &w.blobs[i]
		b.e.Offset = uint32(len(out))
		out = append(out, b.sealed...)
		entries[i] = b.e
		hdr = appendEntry(hdr, b.e)
	}
	var nonce [NonceSize]byte
	if _, err := io.ReadFull(w.rnd, nonce[:]); err != nil {
		return nil, fmt.Errorf("pack: nonce: %w", err)
	}
	key, err := classKey(w.ek, w.class)
	if err != nil {
		return nil, err
	}
	sh, err := vdcrypt.Seal(w.rnd, key, w.ek.Epoch, hdr, vdcrypt.PackHeaderAD(w.repo, nonce[:]))
	if err != nil {
		return nil, err
	}
	out = append(out, nonce[:]...)
	out = append(out, sh...)
	out = binary.LittleEndian.AppendUint32(out, uint32(NonceSize+len(sh)))
	out = append(out, magic[:]...)
	out = append(out, Version)
	if len(out) != size {
		return nil, fmt.Errorf("pack: internal size mismatch %d != %d", len(out), size)
	}
	w.done = true
	w.blobs, w.ids = nil, nil
	return &Pack{OID: vdcrypt.OID(out), Bytes: out, Class: w.class, Epoch: w.ek.Epoch, Entries: entries}, nil
}

func appendEntry(b []byte, e Entry) []byte {
	b = append(b, byte(e.Type))
	b = append(b, e.ID[:]...)
	b = binary.LittleEndian.AppendUint32(b, e.Offset)
	b = binary.LittleEndian.AppendUint32(b, e.Length)
	return binary.LittleEndian.AppendUint32(b, e.RawLength)
}

// shuffle is a Fisher–Yates shuffle driven by rnd (unbiased: rejection
// sampling over u64 draws).
func shuffle(rnd io.Reader, s []pendingBlob) error {
	var buf [8]byte
	for i := len(s) - 1; i > 0; i-- {
		n := uint64(i + 1)
		limit := ^uint64(0) - (^uint64(0) % n)
		var v uint64
		for {
			if _, err := io.ReadFull(rnd, buf[:]); err != nil {
				return fmt.Errorf("pack: shuffle: %w", err)
			}
			if v = binary.LittleEndian.Uint64(buf[:]); v < limit {
				break
			}
		}
		j := int(v % n)
		s[i], s[j] = s[j], s[i]
	}
	return nil
}

func randReader(r io.Reader) io.Reader {
	if r == nil {
		return rand.Reader
	}
	return r
}
