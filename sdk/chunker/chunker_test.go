package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
	"math/rand/v2"
	"testing"
	"testing/iotest"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt/vdcrypttest"
)

// counterStream returns size bytes of the §13 vector-2 stream: block i is
// SHA-256(seed || u64le(i)), i = 0, 1, ….
func counterStream(seed string, size int) []byte {
	out := make([]byte, 0, size+sha256.Size)
	msg := make([]byte, len(seed)+8)
	copy(msg, seed)
	for i := uint64(0); len(out) < size; i++ {
		binary.LittleEndian.PutUint64(msg[len(seed):], i)
		s := sha256.Sum256(msg)
		out = append(out, s[:]...)
	}
	return out[:size]
}

func testGear(t testing.TB) *Gear {
	t.Helper()
	repo, err := vdcrypt.ParseRepoID(vdcrypttest.RepoUUID)
	if err != nil {
		t.Fatal(err)
	}
	return NewGear(vdcrypt.DeriveRepoKeys(repo, vdcrypttest.DK).Chunker)
}

// collect drains a Chunker, copying each chunk's data.
func collect(t testing.TB, c *Chunker) ([]int, []byte) {
	t.Helper()
	var lens []int
	var all []byte
	var off uint64
	for {
		ch, err := c.Next()
		if err == io.EOF {
			return lens, all
		}
		if err != nil {
			t.Fatal(err)
		}
		if ch.Offset != off || ch.Length != len(ch.Data) {
			t.Fatalf("chunk at %d: offset %d, length %d, data %d", off, ch.Offset, ch.Length, len(ch.Data))
		}
		off += uint64(ch.Length)
		lens = append(lens, ch.Length)
		all = append(all, ch.Data...)
	}
}

// randomReader returns reads of random size (1 byte .. 3 MiB).
type randomReader struct {
	r   io.Reader
	rng *rand.Rand
}

func (r *randomReader) Read(p []byte) (int, error) {
	n := 1 + r.rng.IntN(3<<20)
	if n < len(p) {
		p = p[:n]
	}
	return r.r.Read(p)
}

func TestDeterministicAcrossReaders(t *testing.T) {
	g := testGear(t)
	data := counterStream("vaultdock", 20<<20)
	want := g.Boundaries(data)
	if len(want) < 5 {
		t.Fatalf("expected several chunks, got %v", want)
	}
	readers := map[string]func() io.Reader{
		"whole":    func() io.Reader { return bytes.NewReader(data) },
		"onebyte":  func() io.Reader { return iotest.OneByteReader(bytes.NewReader(data)) },
		"half":     func() io.Reader { return iotest.HalfReader(bytes.NewReader(data)) },
		"dataerr":  func() io.Reader { return iotest.DataErrReader(bytes.NewReader(data)) },
		"random":   func() io.Reader { return &randomReader{bytes.NewReader(data), rand.New(rand.NewPCG(1, 2))} },
		"random2":  func() io.Reader { return &randomReader{bytes.NewReader(data), rand.New(rand.NewPCG(3, 4))} },
		"onebyte2": func() io.Reader { return iotest.HalfReader(iotest.OneByteReader(bytes.NewReader(data))) },
	}
	c := New(nil, g)
	for name, mk := range readers {
		t.Run(name, func(t *testing.T) {
			c.Reset(mk()) // also exercises buffer reuse
			lens, all := collect(t, c)
			if !equalInts(lens, want) {
				t.Fatalf("lengths %v, want %v", lens, want)
			}
			if !bytes.Equal(all, data) {
				t.Fatal("reassembled data differs")
			}
		})
	}
}

func TestSizes(t *testing.T) {
	g := testGear(t)
	zeros := make([]byte, 20<<20)
	tests := []struct {
		name string
		data []byte
		want []int
	}{
		{"empty", nil, nil},
		{"one byte", []byte{1}, []int{1}},
		{"below min", counterStream("x", MinSize-1), []int{MinSize - 1}},
		{"exactly min", counterStream("x", MinSize), []int{MinSize}},
		// A constant input never matches either mask: hard cuts at max.
		{"zeros", zeros, []int{MaxSize, MaxSize, 4 << 20}},
		{"zeros max", zeros[:MaxSize], []int{MaxSize}},
		{"zeros max+1", zeros[:MaxSize+1], []int{MaxSize, 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := g.Boundaries(tt.data); !equalInts(got, tt.want) {
				t.Fatalf("Boundaries = %v, want %v", got, tt.want)
			}
			lens, _ := collect(t, New(bytes.NewReader(tt.data), g))
			if !equalInts(lens, tt.want) {
				t.Fatalf("Chunker = %v, want %v", lens, tt.want)
			}
		})
	}
}

func TestBounds(t *testing.T) {
	g := testGear(t)
	data := counterStream("bounds", 64<<20)
	lens := g.Boundaries(data)
	for i, n := range lens {
		if n > MaxSize || (n < MinSize && i != len(lens)-1) {
			t.Fatalf("chunk %d has length %d", i, n)
		}
	}
	avg := len(data) / len(lens)
	if avg < AvgSize/2 || avg > 3*AvgSize {
		t.Fatalf("average chunk %d far from %d", avg, AvgSize)
	}
	parts := g.Split(data)
	if len(parts) != len(lens) || !bytes.Equal(bytes.Join(parts, nil), data) {
		t.Fatal("Split does not match Boundaries")
	}
}

// A different key must give different cuts.
func TestKeyed(t *testing.T) {
	data := counterStream("vaultdock", 16<<20)
	a := testGear(t).Boundaries(data)
	b := NewGear(vdcrypt.Key(vdcrypttest.Seq(0x01))).Boundaries(data)
	if equalInts(a, b) {
		t.Fatal("different K_chunker produced identical boundaries")
	}
}

// Editing a byte must only change the cuts near it (content-defined).
func TestLocality(t *testing.T) {
	g := testGear(t)
	data := counterStream("vaultdock", 24<<20)
	before := g.Split(data)
	edited := bytes.Clone(data)
	edited[len(data)-3<<20] ^= 0xff
	after := g.Split(edited)
	same := 0
	for same < len(before) && same < len(after) && len(before[same]) == len(after[same]) && bytes.Equal(before[same], after[same]) {
		same++
	}
	if same == 0 || same == len(before) {
		t.Fatalf("unexpected shared prefix %d of %d chunks", same, len(before))
	}
}

var errBoom = errors.New("boom")

func TestReadErrors(t *testing.T) {
	g := testGear(t)
	c := New(iotest.ErrReader(errBoom), g)
	if _, err := c.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("got %v", err)
	}
	if _, err := c.Next(); !errors.Is(err, errBoom) {
		t.Fatalf("sticky error: got %v", err)
	}
	c = New(iotest.TimeoutReader(bytes.NewReader(counterStream("e", 12<<20))), g)
	for {
		_, err := c.Next()
		if err == nil {
			continue
		}
		if !errors.Is(err, iotest.ErrTimeout) {
			t.Fatalf("got %v", err)
		}
		break
	}
	c = New(zeroReader{}, g)
	if _, err := c.Next(); !errors.Is(err, ErrNoProgress) {
		t.Fatalf("got %v", err)
	}
}

type zeroReader struct{}

func (zeroReader) Read([]byte) (int, error) { return 0, nil }

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func BenchmarkCut(b *testing.B) {
	g := testGear(b)
	data := counterStream("bench", 64<<20)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		g.Boundaries(data)
	}
}

func BenchmarkChunker(b *testing.B) {
	g := testGear(b)
	data := counterStream("bench", 64<<20)
	c := New(nil, g)
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		c.Reset(bytes.NewReader(data))
		for {
			if _, err := c.Next(); err != nil {
				if err != io.EOF {
					b.Fatal(err)
				}
				break
			}
		}
	}
}

func newRand() *rand.Rand { return rand.New(rand.NewPCG(7, 8)) }
