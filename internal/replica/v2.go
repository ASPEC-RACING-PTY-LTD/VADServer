package replica

// VDR2: files held only by a remote node. The content is encrypted with a
// random per-file key; that key is wrapped for the node's public key
// (ECDH P-256, HKDF-SHA256, AES-256-GCM), so whoever encrypts (this server
// or an uploader) needs no secret, and only the holder of the node's
// private key (this server) can decrypt. The node itself cannot.
//
//	"VDR2" | key id length (1) | key id | ephemeral public key (65,
//	uncompressed P-256) | wrap nonce (12) | wrapped key (48) |
//	chunk nonce prefix (8) | plaintext size (8, big endian) | chunks
//	chunk: 4-byte big-endian ciphertext length | AES-256-GCM ciphertext
//
// Chunks hold exactly ChunkSize plaintext bytes except the last (an empty
// file has one empty chunk), so chunk i starts at a fixed offset and any
// byte range can be read without the rest of the file. Chunk nonces and
// additional data are as in VDR1 (bound to the file and the chunk index;
// the last chunk is marked), so chunks cannot be reordered, swapped between
// files or dropped unnoticed.

import (
	"bytes"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic2 = "VDR2"
	// ChunkSize is the plaintext size of a VDR2 chunk.
	ChunkSize = chunkSize
	// chunkRecord is the stored size of a full chunk.
	chunkRecord = 4 + ChunkSize + 16
	// MaxHeaderV2 bounds the header (with the longest key id).
	MaxHeaderV2 = 4 + 1 + 255 + 65 + 12 + 48 + 8 + 8
	wrapInfo    = "vaultdock vdr2 file key"
)

// NewOriginKey makes a node's key pair.
func NewOriginKey() (*ecdh.PrivateKey, error) { return ecdh.P256().GenerateKey(rand.Reader) }

// MarshalPublic encodes a public key as base64 PKIX (what browsers import
// with WebCrypto "spki").
func MarshalPublic(k *ecdh.PublicKey) (string, error) {
	der, err := x509.MarshalPKIXPublicKey(k)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(der), nil
}

// ParsePublic decodes MarshalPublic's output.
func ParsePublic(s string) (*ecdh.PublicKey, error) {
	der, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, err
	}
	k, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, err
	}
	switch v := k.(type) {
	case *ecdh.PublicKey:
		if v.Curve() != ecdh.P256() {
			return nil, errors.New("replica: not a P-256 key")
		}
		return v, nil
	case interface {
		ECDH() (*ecdh.PublicKey, error)
	}:
		p, err := v.ECDH()
		if err != nil || p.Curve() != ecdh.P256() {
			return nil, errors.New("replica: not a P-256 key")
		}
		return p, nil
	}
	return nil, errors.New("replica: not an ECDH public key")
}

// KeyID names a public key (first 16 hex digits of its SHA-256).
func KeyID(k *ecdh.PublicKey) string {
	s := sha256.Sum256(k.Bytes())
	return fmt.Sprintf("%x", s[:8])
}

func wrapKey(shared, ephPub, nodePub []byte) ([]byte, error) {
	salt := append(append([]byte{}, ephPub...), nodePub...)
	return hkdf.Key(sha256.New, shared, salt, wrapInfo, 32)
}

func wrapAAD(keyID string) []byte { return append([]byte(magic2), keyID...) }

// V2Header is a parsed VDR2 header.
type V2Header struct {
	KeyID  string
	Size   int64 // plaintext size
	Len    int64 // header length in bytes
	ephPub []byte
	wnonce []byte
	wkey   []byte
	prefix []byte
}

// Chunks is the number of chunks.
func (h *V2Header) Chunks() int64 {
	if h.Size == 0 {
		return 1
	}
	return (h.Size + ChunkSize - 1) / ChunkSize
}

// ChunkSpan returns where chunk i is stored (offset, length including its
// length prefix) and how much plaintext it holds.
func (h *V2Header) ChunkSpan(i int64) (off, n, plain int64) {
	plain = ChunkSize
	if i == h.Chunks()-1 {
		plain = h.Size - i*ChunkSize
	}
	return h.Len + i*chunkRecord, 4 + plain + 16, plain
}

// CipherSize is the size of the whole encrypted file.
func (h *V2Header) CipherSize() int64 {
	off, n, _ := h.ChunkSpan(h.Chunks() - 1)
	return off + n
}

// ParseV2Header reads a header from the start of an encrypted file (at
// least its first MaxHeaderV2 bytes, or all of it if shorter).
func ParseV2Header(b []byte) (*V2Header, error) {
	if len(b) < 6 || string(b[:4]) != magic2 {
		return nil, ErrCorrupt
	}
	kl := int(b[4])
	need := 5 + kl + 65 + 12 + 48 + 8 + 8
	if len(b) < need {
		return nil, ErrCorrupt
	}
	h := &V2Header{KeyID: string(b[5 : 5+kl]), Len: int64(need)}
	p := 5 + kl
	h.ephPub, p = b[p:p+65], p+65
	h.wnonce, p = b[p:p+12], p+12
	h.wkey, p = b[p:p+48], p+48
	h.prefix, p = b[p:p+8], p+8
	size := binary.BigEndian.Uint64(b[p : p+8])
	if size > 1<<50 {
		return nil, ErrCorrupt
	}
	h.Size = int64(size)
	return h, nil
}

// FileKey unwraps the file key with the node's private key.
func (h *V2Header) FileKey(priv *ecdh.PrivateKey) ([]byte, error) {
	eph, err := ecdh.P256().NewPublicKey(h.ephPub)
	if err != nil {
		return nil, ErrCorrupt
	}
	shared, err := priv.ECDH(eph)
	if err != nil {
		return nil, ErrCorrupt
	}
	wk, err := wrapKey(shared, h.ephPub, priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	aead, err := newAEAD(wk)
	if err != nil {
		return nil, err
	}
	key, err := aead.Open(nil, h.wnonce, h.wkey, wrapAAD(h.KeyID))
	if err != nil {
		return nil, ErrCorrupt
	}
	return key, nil
}

// OpenChunk verifies and decrypts chunk i from its stored record (length
// prefix included).
func (h *V2Header) OpenChunk(key []byte, id string, i int64, rec []byte) ([]byte, error) {
	_, n, plain := h.ChunkSpan(i)
	if int64(len(rec)) != n || int64(binary.BigEndian.Uint32(rec[:4])) != plain+16 {
		return nil, ErrCorrupt
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 12)
	copy(nonce, h.prefix)
	binary.BigEndian.PutUint32(nonce[8:], uint32(i))
	out, err := aead.Open(nil, nonce, rec[4:], chunkAAD(id, uint32(i), i == h.Chunks()-1))
	if err != nil {
		return nil, ErrCorrupt
	}
	return out, nil
}

// EncryptV2 writes the VDR2 form of r (exactly size bytes) to w for the
// node key pub.
func EncryptV2(w io.Writer, r io.Reader, size int64, pub *ecdh.PublicKey, id string) error {
	keyID := KeyID(pub)
	fileKey := make([]byte, 32)
	prefix := make([]byte, 8)
	wnonce := make([]byte, 12)
	for _, b := range [][]byte{fileKey, prefix, wnonce} {
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return err
		}
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	shared, err := eph.ECDH(pub)
	if err != nil {
		return err
	}
	wk, err := wrapKey(shared, eph.PublicKey().Bytes(), pub.Bytes())
	if err != nil {
		return err
	}
	wa, err := newAEAD(wk)
	if err != nil {
		return err
	}
	var hdr bytes.Buffer
	hdr.WriteString(magic2)
	hdr.WriteByte(byte(len(keyID)))
	hdr.WriteString(keyID)
	hdr.Write(eph.PublicKey().Bytes())
	hdr.Write(wnonce)
	hdr.Write(wa.Seal(nil, wnonce, fileKey, wrapAAD(keyID)))
	hdr.Write(prefix)
	_ = binary.Write(&hdr, binary.BigEndian, uint64(size))
	if _, err := w.Write(hdr.Bytes()); err != nil {
		return err
	}
	aead, err := newAEAD(fileKey)
	if err != nil {
		return err
	}
	h := &V2Header{Size: size}
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	buf := make([]byte, ChunkSize)
	var out []byte
	for i := int64(0); i < h.Chunks(); i++ {
		_, _, plain := h.ChunkSpan(i)
		if _, err := io.ReadFull(r, buf[:plain]); err != nil {
			return fmt.Errorf("replica: the content is shorter than its size: %w", err)
		}
		binary.BigEndian.PutUint32(nonce[8:], uint32(i))
		out = aead.Seal(out[:0], nonce, buf[:plain], chunkAAD(id, uint32(i), i == h.Chunks()-1))
		var lp [4]byte
		binary.BigEndian.PutUint32(lp[:], uint32(len(out)))
		if _, err := w.Write(lp[:]); err != nil {
			return err
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
	}
	return nil
}

// DecryptV2 writes the plaintext of a whole VDR2 stream to w.
func DecryptV2(w io.Writer, r io.Reader, priv *ecdh.PrivateKey, id string) error {
	head := make([]byte, 6)
	if _, err := io.ReadFull(r, head); err != nil {
		return ErrCorrupt
	}
	rest := make([]byte, int(head[4])+65+12+48+8+8-1)
	if _, err := io.ReadFull(r, rest); err != nil {
		return ErrCorrupt
	}
	h, err := ParseV2Header(append(head, rest...))
	if err != nil {
		return err
	}
	key, err := h.FileKey(priv)
	if err != nil {
		return err
	}
	for i := int64(0); i < h.Chunks(); i++ {
		_, n, _ := h.ChunkSpan(i)
		rec := make([]byte, n)
		if _, err := io.ReadFull(r, rec); err != nil {
			return ErrCorrupt
		}
		plain, err := h.OpenChunk(key, id, i, rec)
		if err != nil {
			return err
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
	}
	var extra [1]byte
	if n, _ := r.Read(extra[:]); n > 0 {
		return ErrCorrupt
	}
	return nil
}
