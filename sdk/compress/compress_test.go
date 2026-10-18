package compress

import (
	"bytes"
	"crypto/rand"
	"errors"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func randBytes(t testing.TB, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoundTrip(t *testing.T) {
	tests := []struct {
		name          string
		raw           []byte
		precompressed bool
		wantComp      byte
	}{
		{"empty", []byte{}, false, CompRaw},
		{"tiny", []byte("a"), false, CompRaw},
		{"text", bytes.Repeat([]byte("vaultdock backup "), 4096), false, CompZstd},
		{"random", randBytes(t, 64<<10), false, CompRaw},
		{"precompressed", bytes.Repeat([]byte{'x'}, 4096), true, CompRaw},
		{"max data", bytes.Repeat([]byte{0, 1, 2, 3}, MaxDataRaw/4), false, CompZstd},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := Encode(tc.raw, tc.precompressed)
			if p[0] != tc.wantComp {
				t.Fatalf("comp = %d, want %d", p[0], tc.wantComp)
			}
			got, err := Decode(p, len(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tc.raw) {
				t.Fatal("round trip mismatch")
			}
		})
	}
}

func TestThreshold(t *testing.T) {
	if !keepRaw(97, 100) || keepRaw(96, 100) || !keepRaw(0, 0) {
		t.Fatal("threshold must be c >= 0.97*r")
	}
	// Half random, half zeros: compresses to roughly 50 %, so zstd is kept.
	raw := append(randBytes(t, 32<<10), make([]byte, 32<<10)...)
	p := Encode(raw, false)
	if p[0] != CompZstd || len(p)-1 >= len(raw)*97/100 {
		t.Fatalf("comp=%d len=%d raw=%d", p[0], len(p), len(raw))
	}
}

func TestAppendEncode(t *testing.T) {
	raw := randBytes(t, 1024) // incompressible: exercises the rollback path
	prefix := []byte("hdr")
	p := AppendEncode(append([]byte(nil), prefix...), raw, false)
	if !bytes.HasPrefix(p, prefix) || p[3] != CompRaw || !bytes.Equal(p[4:], raw) {
		t.Fatal("AppendEncode did not preserve prefix or roll back")
	}
}

func TestEncodeZstdDecodeLimit(t *testing.T) {
	raw := []byte(`{"v":1,"supersedes":[],"packs":[]}`)
	p := EncodeZstd(raw)
	if p[0] != CompZstd {
		t.Fatal("EncodeZstd must set comp=1")
	}
	got, err := DecodeLimit(p, 1<<20)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, err := DecodeLimit(p, len(raw)-1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestLimitEnforced(t *testing.T) {
	big := make([]byte, 1<<20) // compresses to a few bytes
	p := Encode(big, false)

	if _, err := Decode(p, 1000); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("FCS check: err = %v, want ErrTooLarge", err)
	}
	if _, err := Decode(p, MaxMetaRaw+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("rawLen over max: err = %v", err)
	}
	if _, err := Decode(p, len(big)+1); !errors.Is(err, ErrSizeMismatch) {
		t.Fatalf("short output: err = %v, want ErrSizeMismatch", err)
	}
	if _, err := Decode(append([]byte{CompRaw}, big...), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("raw over limit: err = %v", err)
	}

	// A streamed frame without a content size must still be cut off at the limit.
	var buf bytes.Buffer
	w, err := zstd.NewWriter(&buf)
	if err != nil {
		t.Fatal(err)
	}
	w.Write(big)
	w.Close()
	var h zstd.Header
	if err := h.Decode(buf.Bytes()); err != nil || h.HasFCS {
		t.Fatalf("expected frame without FCS: %v %v", h.HasFCS, err)
	}
	bomb := append([]byte{CompZstd}, buf.Bytes()...)
	if _, err := Decode(bomb, 4096); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("no-FCS bomb: err = %v, want ErrTooLarge", err)
	}
	if got, err := Decode(bomb, len(big)); err != nil || !bytes.Equal(got, big) {
		t.Fatalf("no-FCS exact: %v", err)
	}
}

func TestCorrupt(t *testing.T) {
	good := Encode(bytes.Repeat([]byte("abcdefgh"), 1000), false)
	flipped := bytes.Clone(good)
	flipped[len(flipped)/2] ^= 0xff
	truncated := good[:len(good)-5]

	tests := []struct {
		name    string
		payload []byte
		want    error
	}{
		{"empty", nil, ErrEmpty},
		{"unknown flag", []byte{2, 1, 2, 3}, ErrUnknownComp},
		{"zstd empty body", []byte{CompZstd}, ErrCorrupt},
		{"zstd garbage", []byte{CompZstd, 1, 2, 3, 4, 5, 6, 7, 8}, ErrCorrupt},
		{"flipped", flipped, nil},
		{"truncated", truncated, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Decode(tc.payload, 8000)
			if err == nil {
				t.Fatal("expected error")
			}
			if tc.want != nil && !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestConcurrent(t *testing.T) {
	raw := bytes.Repeat([]byte("concurrent "), 10000)
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 20 {
				got, err := Decode(Encode(raw, false), len(raw))
				if err != nil || !bytes.Equal(got, raw) {
					t.Error("concurrent round trip failed", err)
					return
				}
			}
		})
	}
	wg.Wait()
}

func BenchmarkEncode1MiB(b *testing.B) {
	raw := append(randBytes(b, 512<<10), bytes.Repeat([]byte("text "), 512<<10/5)...)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		Encode(raw, false)
	}
}

func BenchmarkDecode1MiB(b *testing.B) {
	raw := append(randBytes(b, 512<<10), bytes.Repeat([]byte("text "), 512<<10/5)...)
	p := Encode(raw, false)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for b.Loop() {
		if _, err := Decode(p, len(raw)); err != nil {
			b.Fatal(err)
		}
	}
}
