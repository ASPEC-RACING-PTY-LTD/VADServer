package replica

import (
	"bytes"
	"crypto/rand"
	"errors"
	"testing"
)

func TestV2RoundTripAndRanges(t *testing.T) {
	priv, err := NewOriginKey()
	if err != nil {
		t.Fatal(err)
	}
	pubStr, err := MarshalPublic(priv.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := ParsePublic(pubStr)
	if err != nil {
		t.Fatal(err)
	}
	id := BlobID(7, "abc")
	for _, size := range []int{0, 1, ChunkSize - 1, ChunkSize, ChunkSize + 1, 3*ChunkSize + 12345} {
		plain := make([]byte, size)
		rand.Read(plain)
		var enc bytes.Buffer
		if err := EncryptV2(&enc, bytes.NewReader(plain), int64(size), pub, id); err != nil {
			t.Fatal(err)
		}
		h, err := ParseV2Header(enc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if h.Size != int64(size) || h.KeyID != KeyID(pub) || h.CipherSize() != int64(enc.Len()) {
			t.Fatalf("size %d: header %+v, cipher %d vs %d", size, h, h.CipherSize(), enc.Len())
		}
		var dec bytes.Buffer
		if err := DecryptV2(&dec, bytes.NewReader(enc.Bytes()), priv, id); err != nil || !bytes.Equal(dec.Bytes(), plain) {
			t.Fatalf("size %d: round trip %v", size, err)
		}
		// Any chunk can be read on its own.
		key, err := h.FileKey(priv)
		if err != nil {
			t.Fatal(err)
		}
		for i := int64(0); i < h.Chunks(); i++ {
			off, n, pl := h.ChunkSpan(i)
			got, err := h.OpenChunk(key, id, i, enc.Bytes()[off:off+n])
			if err != nil || int64(len(got)) != pl || !bytes.Equal(got, plain[i*ChunkSize:i*ChunkSize+pl]) {
				t.Fatalf("size %d chunk %d: %v", size, i, err)
			}
		}
	}
}

func TestV2DetectsTamperingAndWrongKeys(t *testing.T) {
	priv, _ := NewOriginKey()
	other, _ := NewOriginKey()
	plain := make([]byte, 2*ChunkSize+10)
	rand.Read(plain)
	var enc bytes.Buffer
	if err := EncryptV2(&enc, bytes.NewReader(plain), int64(len(plain)), priv.PublicKey(), BlobID(1, "a")); err != nil {
		t.Fatal(err)
	}
	b := enc.Bytes()
	h, _ := ParseV2Header(b)
	if _, err := h.FileKey(other); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("other key unwrapped: %v", err)
	}
	key, _ := h.FileKey(priv)
	off0, n0, _ := h.ChunkSpan(0)
	off1, n1, _ := h.ChunkSpan(1)
	// Swapped chunks, another file's id, a flipped byte: all refused.
	if _, err := h.OpenChunk(key, BlobID(1, "a"), 0, b[off1:off1+n1]); !errors.Is(err, ErrCorrupt) {
		t.Fatal("chunk 1 accepted as chunk 0")
	}
	if _, err := h.OpenChunk(key, BlobID(2, "a"), 0, b[off0:off0+n0]); !errors.Is(err, ErrCorrupt) {
		t.Fatal("chunk accepted for another file")
	}
	bad := append([]byte{}, b...)
	bad[off0+20] ^= 1
	if err := DecryptV2(&bytes.Buffer{}, bytes.NewReader(bad), priv, BlobID(1, "a")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("flipped byte: %v", err)
	}
	// Truncation (last chunk missing) and extra data are refused.
	off2, _, _ := h.ChunkSpan(2)
	if err := DecryptV2(&bytes.Buffer{}, bytes.NewReader(b[:off2]), priv, BlobID(1, "a")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: %v", err)
	}
	if err := DecryptV2(&bytes.Buffer{}, bytes.NewReader(append(append([]byte{}, b...), 0)), priv, BlobID(1, "a")); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("extra data: %v", err)
	}
	// A short source is an error, not a short file.
	if err := EncryptV2(&bytes.Buffer{}, bytes.NewReader(plain[:10]), int64(len(plain)), priv.PublicKey(), "x"); err == nil {
		t.Fatal("short source accepted")
	}
}
