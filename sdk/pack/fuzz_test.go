package pack

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// FuzzReader feeds arbitrary bytes to NewReader and Blob for both classes.
// It must never panic, and anything accepted must decode consistently.
func FuzzReader(f *testing.F) {
	fx := newFixture(f)
	dp := buildPack(f, fx, ClassData, "fuzz-data", dataPackInputs()).Bytes
	mp := buildPack(f, fx, ClassMeta, "fuzz-meta", metaPackInputs()).Bytes
	for _, b := range [][]byte{dp, mp, dp[:len(dp)-1], dp[1:], dp[len(dp)-TrailerSize:], {}} {
		f.Add(b)
	}
	tampered := bytes.Clone(dp)
	tampered[len(tampered)-TrailerSize-3] ^= 1
	f.Add(tampered)
	f.Fuzz(func(t *testing.T, b []byte) {
		for _, c := range []Class{ClassData, ClassMeta} {
			pr, err := NewReader(bytes.NewReader(b), int64(len(b)), fx.kr, c)
			if err != nil {
				continue
			}
			for _, e := range pr.Entries {
				raw, err := pr.Blob(e)
				if err == nil && (ComputeID(fx.keys.ID, raw) != e.ID || len(raw) != int(e.RawLength)) {
					t.Fatalf("accepted blob does not match entry %+v", e)
				}
			}
		}
		// The trailer parser alone.
		if len(b) >= TrailerSize {
			_, _ = ParseTrailer(b[len(b)-TrailerSize:], int64(len(b)))
		}
	})
}

// FuzzParseHeader exercises the post-decryption header checks, which the
// AEAD otherwise shields from FuzzReader.
func FuzzParseHeader(f *testing.F) {
	one := binary.LittleEndian.AppendUint32([]byte{0}, 1)
	one = appendEntry(one, Entry{Type: TypeData, Length: 100, RawLength: 10})
	f.Add(one, int64(100))
	f.Add(one, int64(99))
	f.Add([]byte{1, 0, 0, 0, 0}, int64(0))
	f.Fuzz(func(t *testing.T, plain []byte, dataEnd int64) {
		for _, c := range []Class{ClassData, ClassMeta} {
			es, err := ParseHeader(plain, c, dataEnd)
			if err != nil {
				continue
			}
			var sum int64
			for _, e := range es {
				if bc, _ := e.Type.Class(); bc != c {
					t.Fatalf("entry type %d accepted in %s pack", e.Type, c)
				}
				sum += int64(e.Length)
			}
			if sum != dataEnd || len(es) == 0 {
				t.Fatalf("accepted header does not tile: %d vs %d", sum, dataEnd)
			}
		}
	})
}
