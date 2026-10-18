package pack

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// countingReader counts ReadAt calls to check ranged-read behaviour.
type countingReader struct {
	r     io.ReaderAt
	calls int
}

func (c *countingReader) ReadAt(p []byte, off int64) (int, error) {
	c.calls++
	return c.r.ReadAt(p, off)
}

func dataPack(t *testing.T, f fixture) *Pack {
	t.Helper()
	return buildPack(t, f, ClassData, "test-data", dataPackInputs())
}

func TestRoundTrip(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		class  Class
		inputs []blobInput
	}{
		{ClassData, dataPackInputs()},
		{ClassMeta, metaPackInputs()},
	} {
		t.Run(tc.class.String(), func(t *testing.T) {
			p := buildPack(t, f, tc.class, "rt", tc.inputs)
			if p.OID != vdcrypt.OID(p.Bytes) || len(p.Entries) != len(tc.inputs) {
				t.Fatal("bad pack result")
			}
			cr := &countingReader{r: bytes.NewReader(p.Bytes)}
			pr, err := NewReader(cr, int64(len(p.Bytes)), f.kr, tc.class)
			if err != nil {
				t.Fatal(err)
			}
			if cr.calls != 2 {
				t.Fatalf("NewReader made %d reads, want 2", cr.calls)
			}
			for i, e := range p.Entries {
				if pr.Entries[i] != e {
					t.Fatalf("entry %d differs", i)
				}
			}
			for _, in := range tc.inputs {
				e, ok := pr.Find(ComputeID(f.keys.ID, in.raw))
				if !ok {
					t.Fatalf("%s not found", in.name)
				}
				cr.calls = 0
				got, err := pr.Blob(e)
				if err != nil || !bytes.Equal(got, in.raw) || cr.calls != 1 {
					t.Fatalf("%s: %v (reads %d)", in.name, err, cr.calls)
				}
				// Index-only path: no header read.
				got, err = ReadBlob(bytes.NewReader(p.Bytes), f.kr, e)
				if err != nil || !bytes.Equal(got, in.raw) {
					t.Fatalf("%s ReadBlob: %v", in.name, err)
				}
			}
		})
	}
}

func TestShuffleIsPermutation(t *testing.T) {
	f := newFixture(t)
	w, err := NewWriter(f.repo, f.keys.ID, f.k1, ClassData, WriterOptions{Rand: vdcrypttest.NewRNG("shuffle")})
	if err != nil {
		t.Fatal(err)
	}
	var want []BlobID
	for i := range 50 {
		e, err := w.Add(TypeData, []byte{byte(i), 1, 2, 3}, false)
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, e.ID)
	}
	p, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	same := true
	seen := map[BlobID]bool{}
	for i, e := range p.Entries {
		seen[e.ID] = true
		same = same && e.ID == want[i]
	}
	if len(seen) != 50 || same {
		t.Fatalf("entries not a shuffled permutation (unique %d, unchanged %v)", len(seen), same)
	}
}

func TestTruncation(t *testing.T) {
	f := newFixture(t)
	b := dataPack(t, f).Bytes
	for cut := 1; cut < len(b); cut++ {
		short := b[:len(b)-cut]
		if _, err := NewReader(bytes.NewReader(short), int64(len(short)), f.kr, ClassData); err == nil {
			t.Fatalf("tail cut %d accepted", cut)
		}
		short = b[cut:]
		if _, err := NewReader(bytes.NewReader(short), int64(len(short)), f.kr, ClassData); err == nil {
			t.Fatalf("head cut %d accepted", cut)
		}
	}
	// The source is shorter than the claimed size.
	_, err := NewReader(bytes.NewReader(b[:len(b)-5]), int64(len(b)), f.kr, ClassData)
	if !errors.Is(err, ErrTruncated) {
		t.Fatalf("short source: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(b[:20]), 20, f.kr, ClassData); !errors.Is(err, ErrTruncated) {
		t.Fatalf("tiny pack: %v", err)
	}
	// A blob read past the end of the source.
	pr, err := NewReader(bytes.NewReader(b), int64(len(b)), f.kr, ClassData)
	if err != nil {
		t.Fatal(err)
	}
	last := pr.Entries[len(pr.Entries)-1]
	if _, err := ReadBlob(bytes.NewReader(b[:last.Offset+10]), f.kr, last); !errors.Is(err, ErrTruncated) {
		t.Fatalf("short blob: %v", err)
	}
}

func TestTrailer(t *testing.T) {
	f := newFixture(t)
	b := dataPack(t, f).Bytes
	n := len(b)
	l := binary.LittleEndian.Uint32(b[n-12:])
	cases := []struct {
		name string
		mut  func([]byte)
		want error
	}{
		{"magic", func(p []byte) { p[n-8] ^= 1 }, ErrTrailer},
		{"magic nul", func(p []byte) { p[n-2] = 1 }, ErrTrailer},
		{"version", func(p []byte) { p[n-1] = 2 }, ErrVersion},
		{"length too small", func(p []byte) { binary.LittleEndian.PutUint32(p[n-12:], 10) }, ErrHeaderLength},
		{"length not entry aligned", func(p []byte) { binary.LittleEndian.PutUint32(p[n-12:], l+1) }, ErrHeaderLength},
		{"length too large", func(p []byte) { binary.LittleEndian.PutUint32(p[n-12:], l+EntrySize*1000) }, ErrTruncated},
		{"length shifted", func(p []byte) { binary.LittleEndian.PutUint32(p[n-12:], l+EntrySize) }, ErrHeaderAuth},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := bytes.Clone(b)
			tc.mut(p)
			_, err := NewReader(bytes.NewReader(p), int64(n), f.kr, ClassData)
			if !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestHeaderTamper(t *testing.T) {
	f := newFixture(t)
	p := dataPack(t, f)
	b := p.Bytes
	hdrStart := len(b) - TrailerSize - int(binary.LittleEndian.Uint32(b[len(b)-12:]))
	for _, pos := range []int{hdrStart, hdrStart + NonceSize - 1, hdrStart + NonceSize + 5, hdrStart + NonceSize + 30, len(b) - TrailerSize - 1} {
		q := bytes.Clone(b)
		q[pos] ^= 0x80
		if _, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); !errors.Is(err, ErrHeaderAuth) {
			t.Fatalf("flip at %d: %v", pos, err)
		}
	}
	// Envelope version and epoch bytes.
	q := bytes.Clone(b)
	q[hdrStart+NonceSize] = 2
	if _, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); !errors.Is(err, ErrHeaderAuth) {
		t.Fatalf("envelope version: %v", err)
	}
	q = bytes.Clone(b)
	q[hdrStart+NonceSize+1] = 2 // epoch 1 -> 2
	if _, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); !errors.Is(err, ErrHeaderAuth) {
		t.Fatalf("envelope epoch: %v", err)
	}
	// A header from another pack (different nonce) does not open.
	other := buildPack(t, f, ClassData, "other", dataPackInputs()).Bytes
	if len(other) == len(b) {
		q = bytes.Clone(b)
		copy(q[hdrStart+NonceSize:], other[hdrStart+NonceSize:])
		if _, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); !errors.Is(err, ErrHeaderAuth) {
			t.Fatalf("swapped header: %v", err)
		}
	}
}

func TestBlobTamper(t *testing.T) {
	f := newFixture(t)
	b := dataPack(t, f).Bytes
	pr0, err := NewReader(bytes.NewReader(b), int64(len(b)), f.kr, ClassData)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range pr0.Entries {
		for _, off := range []uint32{0, 1, 5, 30, e.Length - 1} {
			q := bytes.Clone(b)
			q[e.Offset+off] ^= 1
			pr, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData)
			if err != nil {
				t.Fatal(err) // blob bytes are not covered by the header
			}
			if _, err := pr.Blob(e); !errors.Is(err, ErrBlobAuth) && !errors.Is(err, ErrNoKey) {
				t.Fatalf("flip blob %s +%d: %v", e.ID, off, err)
			}
		}
		// Entry fields that disagree with the stored blob.
		bad := e
		bad.ID[0] ^= 1
		if _, err := pr0.Blob(bad); !errors.Is(err, ErrBlobAuth) {
			t.Fatalf("wrong id: %v", err)
		}
		bad = e
		bad.RawLength++
		if _, err := pr0.Blob(bad); !errors.Is(err, ErrBlobPayload) {
			t.Fatalf("wrong raw_len: %v", err)
		}
		bad = e
		bad.Length--
		if _, err := pr0.Blob(bad); !errors.Is(err, ErrBlobAuth) {
			t.Fatalf("wrong sealed_len: %v", err)
		}
		bad = e
		bad.Offset = uint32(pr0.DataEnd)
		if _, err := pr0.Blob(bad); !errors.Is(err, ErrBlobBounds) {
			t.Fatalf("offset outside: %v", err)
		}
		bad = e
		bad.Type = TypeTree
		if _, err := pr0.Blob(bad); !errors.Is(err, ErrClass) {
			t.Fatalf("wrong type: %v", err)
		}
	}
}

func TestBlobIDVerified(t *testing.T) {
	f := newFixture(t)
	// A correctly sealed blob whose content does not hash to its id (only a
	// key holder can build one): the AD binds the claimed id.
	claimed := ComputeID(f.keys.ID, []byte("claimed"))
	raw := []byte("actual content")
	sealed, err := vdcrypt.Seal(nil, f.k1.Data, 1, compress.Encode(raw, false), vdcrypt.BlobAD(f.repo, claimed[:]))
	if err != nil {
		t.Fatal(err)
	}
	e := Entry{Type: TypeData, ID: claimed, Length: uint32(len(sealed)), RawLength: uint32(len(raw))}
	if _, err := OpenBlob(f.kr, e, sealed); !errors.Is(err, ErrBlobID) {
		t.Fatalf("got %v", err)
	}
	// A blob sealed under another repo's K_id.
	other := *f.kr
	other.ID[0] ^= 1
	e2, sealed2, err := SealBlob(nil, f.repo, f.keys.ID, f.k1, TypeData, raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenBlob(&other, e2, sealed2); !errors.Is(err, ErrBlobID) {
		t.Fatalf("other K_id: %v", err)
	}
	// Unknown compression flag inside a valid envelope.
	id := ComputeID(f.keys.ID, raw)
	sealed3, _ := vdcrypt.Seal(nil, f.k1.Data, 1, append([]byte{7}, raw...), vdcrypt.BlobAD(f.repo, id[:]))
	e3 := Entry{Type: TypeData, ID: id, Length: uint32(len(sealed3)), RawLength: uint32(len(raw))}
	if _, err := OpenBlob(f.kr, e3, sealed3); !errors.Is(err, ErrBlobPayload) {
		t.Fatalf("bad comp: %v", err)
	}
}

func TestWrongClassKey(t *testing.T) {
	f := newFixture(t)
	b := dataPack(t, f).Bytes
	n := int64(len(b))
	// Opened as a meta pack: K_meta does not open a data header.
	if _, err := NewReader(bytes.NewReader(b), n, f.kr, ClassMeta); !errors.Is(err, ErrHeaderAuth) {
		t.Fatalf("as meta: %v", err)
	}
	// A keyring whose data key is the meta key.
	swapped := *f.kr
	swapped.Epochs = func(e uint32) (vdcrypt.EpochKeys, error) {
		k := f.k1
		k.Data = k.Meta
		return k, nil
	}
	if _, err := NewReader(bytes.NewReader(b), n, &swapped, ClassData); !errors.Is(err, ErrHeaderAuth) {
		t.Fatalf("swapped key: %v", err)
	}
	// Metadata escrow: no data key at all.
	metaOnly := *f.kr
	metaOnly.Epochs = func(e uint32) (vdcrypt.EpochKeys, error) {
		k := f.k1
		k.Data = vdcrypt.Key{}
		return k, nil
	}
	if _, err := NewReader(bytes.NewReader(b), n, &metaOnly, ClassData); !errors.Is(err, ErrNoKey) {
		t.Fatalf("meta only: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(b), n, nil, ClassData); !errors.Is(err, ErrNoKey) {
		t.Fatalf("nil keyring: %v", err)
	}
	// Wrong repository: same keys, different AD.
	wrongRepo := *f.kr
	wrongRepo.Repo[15] ^= 1
	if _, err := NewReader(bytes.NewReader(b), n, &wrongRepo, ClassData); !errors.Is(err, ErrHeaderAuth) {
		t.Fatalf("wrong repo: %v", err)
	}
	// A meta pack's tree blob read with a data-class entry type.
	mp := buildPack(t, f, ClassMeta, "m", metaPackInputs())
	pr, err := NewReader(bytes.NewReader(mp.Bytes), int64(len(mp.Bytes)), f.kr, ClassMeta)
	if err != nil {
		t.Fatal(err)
	}
	e := pr.Entries[0]
	e.Type = TypeData
	if _, err := ReadBlob(bytes.NewReader(mp.Bytes), f.kr, e); !errors.Is(err, ErrBlobAuth) {
		t.Fatalf("meta blob as data: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(mp.Bytes), int64(len(mp.Bytes)), f.kr, Class(2)); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("class 2: %v", err)
	}
}

func TestWriterErrors(t *testing.T) {
	f := newFixture(t)
	newW := func(c Class, o WriterOptions) *Writer {
		t.Helper()
		w, err := NewWriter(f.repo, f.keys.ID, f.k1, c, o)
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	w := newW(ClassData, WriterOptions{})
	if _, err := w.Add(TypeData, make([]byte, compress.MaxDataRaw+1), false); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("oversized data: %v", err)
	}
	if _, err := w.Add(TypeData, make([]byte, compress.MaxDataRaw), true); err != nil {
		t.Fatalf("max data: %v", err)
	}
	if _, err := w.Add(TypeTree, []byte("x"), false); !errors.Is(err, ErrClass) {
		t.Fatalf("tree in data pack: %v", err)
	}
	if _, err := w.Add(BlobType(3), []byte("x"), false); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("type 3: %v", err)
	}
	if _, err := w.Add(TypeData, make([]byte, compress.MaxDataRaw), true); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate: %v", err)
	}
	if _, _, err := SealBlob(nil, f.repo, f.keys.ID, f.k2, TypeMeta, make([]byte, compress.MaxMetaRaw+1), true); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("oversized meta: %v", err)
	}
	if _, err := w.Finish(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Finish(); !errors.Is(err, ErrFinished) {
		t.Fatalf("finish twice: %v", err)
	}
	if _, err := w.Add(TypeData, []byte("y"), false); !errors.Is(err, ErrFinished) {
		t.Fatalf("add after finish: %v", err)
	}
	if _, err := newW(ClassMeta, WriterOptions{}).Finish(); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty: %v", err)
	}

	// pack_target / pack_max.
	w = newW(ClassData, WriterOptions{Target: 600, Max: 1000})
	var full bool
	for i := 0; ; i++ {
		_, err := w.Add(TypeData, bytes.Repeat([]byte{byte(i)}, 100), true)
		if errors.Is(err, ErrPackFull) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		full = full || w.Full()
	}
	if !full || w.Size() > 1000 {
		t.Fatalf("full %v size %d", full, w.Size())
	}
	p, err := w.Finish()
	if err != nil || len(p.Bytes) > 1000 {
		t.Fatalf("finish: %v", err)
	}
	w = newW(ClassData, WriterOptions{Max: 200})
	if _, err := w.Add(TypeData, make([]byte, 300), true); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("blob larger than pack_max: %v", err)
	}

	for _, o := range []WriterOptions{{Max: MaxSize + 1}, {Target: 10, Max: 5}, {Target: -1}, {Max: -1}} {
		if _, err := NewWriter(f.repo, f.keys.ID, f.k1, ClassData, o); !errors.Is(err, ErrOptions) {
			t.Fatalf("options %+v: %v", o, err)
		}
	}
	if _, err := NewWriter(f.repo, f.keys.ID, f.k1, Class(9), WriterOptions{}); !errors.Is(err, ErrInvalidType) {
		t.Fatalf("class 9: %v", err)
	}
	noData := f.k1
	noData.Data = vdcrypt.Key{}
	if _, err := NewWriter(f.repo, f.keys.ID, noData, ClassData, WriterOptions{}); !errors.Is(err, ErrNoKey) {
		t.Fatalf("no data key: %v", err)
	}
}

func TestOversizedReads(t *testing.T) {
	f := newFixture(t)
	e := Entry{Type: TypeData, Length: 100, RawLength: compress.MaxDataRaw + 1}
	if _, err := ReadBlob(bytes.NewReader(make([]byte, 200)), f.kr, e); !errors.Is(err, ErrBlobTooLarge) {
		t.Fatalf("raw_len: %v", err)
	}
	e = Entry{Type: TypeData, Length: compress.MaxDataRaw + 100, RawLength: 1}
	if _, err := ReadBlob(bytes.NewReader(nil), f.kr, e); !errors.Is(err, ErrBlobBounds) {
		t.Fatalf("sealed_len: %v", err)
	}
	if _, err := NewReader(bytes.NewReader(nil), MaxSize+1, f.kr, ClassData); !errors.Is(err, ErrPackTooLarge) {
		t.Fatalf("pack size: %v", err)
	}
}

func TestParseHeader(t *testing.T) {
	entry := func(typ BlobType, id byte, off, l, r uint32) []byte {
		var e Entry
		e.Type, e.Offset, e.Length, e.RawLength = typ, off, l, r
		e.ID[0] = id
		return appendEntry(nil, e)
	}
	hdr := func(c byte, count uint32, entries ...[]byte) []byte {
		b := binary.LittleEndian.AppendUint32([]byte{c}, count)
		for _, e := range entries {
			b = append(b, e...)
		}
		return b
	}
	ok := hdr(0, 2, entry(0, 1, 100, 60, 10), entry(0, 2, 0, 100, 50))
	if es, err := ParseHeader(ok, ClassData, 160); err != nil || len(es) != 2 {
		t.Fatalf("valid header (out of offset order): %v", err)
	}
	cases := []struct {
		name    string
		plain   []byte
		class   Class
		dataEnd int64
		want    error
	}{
		{"short", []byte{0, 1, 0, 0, 0}, ClassData, 100, ErrHeader},
		{"class byte", hdr(1, 1, entry(0, 1, 0, 100, 1)), ClassData, 100, ErrClass},
		{"count zero", hdr(0, 0, entry(0, 1, 0, 100, 1)), ClassData, 100, ErrEmpty},
		{"count too big", hdr(0, 2, entry(0, 1, 0, 100, 1)), ClassData, 100, ErrHeader},
		{"count huge", hdr(0, 0xffffffff, entry(0, 1, 0, 100, 1)), ClassData, 100, ErrHeader},
		{"trailing byte", append(hdr(0, 1, entry(0, 1, 0, 100, 1)), 0), ClassData, 100, ErrHeader},
		{"unknown type", hdr(1, 1, entry(3, 1, 0, 100, 1)), ClassMeta, 100, ErrHeader},
		{"data in meta", hdr(1, 1, entry(0, 1, 0, 100, 1)), ClassMeta, 100, ErrClass},
		{"tree in data", hdr(0, 1, entry(1, 1, 0, 100, 1)), ClassData, 100, ErrClass},
		{"raw too large", hdr(0, 1, entry(0, 1, 0, 100, compress.MaxDataRaw+1)), ClassData, 100, ErrBlobTooLarge},
		{"meta raw too large", hdr(1, 1, entry(2, 1, 0, 100, compress.MaxMetaRaw+1)), ClassMeta, 100, ErrBlobTooLarge},
		{"sealed too small", hdr(0, 1, entry(0, 1, 0, 45, 0)), ClassData, 45, ErrHeader},
		{"sealed too large", hdr(0, 1, entry(0, 1, 0, compress.MaxDataRaw+47, 1)), ClassData, compress.MaxDataRaw + 47, ErrHeader},
		{"duplicate id", hdr(0, 2, entry(0, 1, 0, 100, 1), entry(0, 1, 100, 100, 1)), ClassData, 200, ErrDuplicate},
		{"gap", hdr(0, 2, entry(0, 1, 0, 100, 1), entry(0, 2, 101, 100, 1)), ClassData, 201, ErrBlobBounds},
		{"overlap", hdr(0, 2, entry(0, 1, 0, 100, 1), entry(0, 2, 99, 100, 1)), ClassData, 199, ErrBlobBounds},
		{"not at zero", hdr(0, 1, entry(0, 1, 1, 100, 1)), ClassData, 101, ErrBlobBounds},
		{"short of data end", hdr(0, 1, entry(0, 1, 0, 100, 1)), ClassData, 150, ErrBlobBounds},
		{"past data end", hdr(0, 1, entry(0, 1, 0, 100, 1)), ClassData, 50, ErrBlobBounds},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseHeader(tc.plain, tc.class, tc.dataEnd); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

// TestCraftedHeader seals structurally invalid headers with the right key
// so the checks after decryption are exercised end to end.
func TestCraftedHeader(t *testing.T) {
	f := newFixture(t)
	p := dataPack(t, f)
	b := p.Bytes
	l := int(binary.LittleEndian.Uint32(b[len(b)-12:]))
	hdrStart := len(b) - TrailerSize - l
	nonce := b[hdrStart : hdrStart+NonceSize]
	reseal := func(entries []Entry, class byte) []byte {
		plain := binary.LittleEndian.AppendUint32([]byte{class}, uint32(len(entries)))
		for _, e := range entries {
			plain = appendEntry(plain, e)
		}
		sh, err := vdcrypt.Seal(nil, f.k1.Data, 1, plain, vdcrypt.PackHeaderAD(f.repo, nonce))
		if err != nil {
			t.Fatal(err)
		}
		out := append(bytes.Clone(b[:hdrStart+NonceSize]), sh...)
		return append(out, b[len(b)-TrailerSize:]...)
	}
	es := p.Entries
	swapped := append([]Entry{}, es...)
	swapped[0].Offset, swapped[1].Offset = swapped[1].Offset, swapped[0].Offset
	dup := append([]Entry{}, es...)
	dup[1].ID = dup[0].ID
	for _, tc := range []struct {
		name    string
		entries []Entry
		class   byte
		want    error
	}{
		{"class byte", es, 1, ErrClass},
		{"duplicate", dup, 0, ErrDuplicate},
	} {
		q := reseal(tc.entries, tc.class)
		if _, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); !errors.Is(err, tc.want) {
			t.Fatalf("%s: got %v, want %v", tc.name, err, tc.want)
		}
	}
	// Swapped offsets still tile the area when lengths are equal; otherwise
	// the blob reads fail authentication instead of returning wrong data.
	q := reseal(swapped, 0)
	if pr, err := NewReader(bytes.NewReader(q), int64(len(q)), f.kr, ClassData); err == nil {
		for _, e := range pr.Entries[:2] {
			if _, err := pr.Blob(e); err == nil && e.Length != es[0].Length {
				t.Fatal("misplaced blob opened")
			}
		}
	} else if !errors.Is(err, ErrBlobBounds) {
		t.Fatalf("swapped offsets: %v", err)
	}
}
