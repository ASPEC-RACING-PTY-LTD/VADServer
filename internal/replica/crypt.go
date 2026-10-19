// Package replica holds the pieces of file copies ("copy rules") that do not
// need the server: the encrypted format of copies stored on nodes and rule
// matching.
package replica

// Copies on storage nodes are encrypted by the server before they leave it,
// so the machine's owner cannot read them. Format (VDR1):
//
//	"VDR1" | 8-byte random nonce prefix | chunks
//	chunk: 4-byte big-endian ciphertext length | AES-256-GCM ciphertext
//
// Each chunk holds up to 1 MiB of plaintext. The nonce is prefix | 4-byte
// chunk counter; the additional data binds the blob (user, sha256), the
// chunk index and whether it is the last chunk, so chunks cannot be
// reordered, swapped between files or cut off at the end unnoticed.

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	magic     = "VDR1"
	chunkSize = 1 << 20
)

var ErrCorrupt = errors.New("replica: encrypted copy is damaged or was made with another key")

func newAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("replica: key must be 32 bytes")
	}
	b, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

func chunkAAD(id string, idx uint32, last bool) []byte {
	l := byte(0)
	if last {
		l = 1
	}
	b := make([]byte, 0, len(id)+6)
	b = append(b, id...)
	b = binary.BigEndian.AppendUint32(b, idx)
	return append(b, 0, l)
}

// BlobID is the identity an encrypted copy is bound to.
func BlobID(userID int64, sha string) string { return fmt.Sprintf("vdr:%d:%s", userID, sha) }

// Encrypt writes the encrypted form of r to w.
func Encrypt(w io.Writer, r io.Reader, key []byte, id string) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	prefix := make([]byte, 8)
	if _, err := io.ReadFull(rand.Reader, prefix); err != nil {
		return err
	}
	if _, err := w.Write(append([]byte(magic), prefix...)); err != nil {
		return err
	}
	cur := make([]byte, chunkSize)
	next := make([]byte, chunkSize)
	n, err := io.ReadFull(r, cur)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		return err
	}
	nonce := make([]byte, 12)
	copy(nonce, prefix)
	var out []byte
	for idx := uint32(0); ; idx++ {
		var m int
		var rerr error
		last := n < chunkSize
		if !last {
			m, rerr = io.ReadFull(r, next)
			if rerr != nil && !errors.Is(rerr, io.ErrUnexpectedEOF) && !errors.Is(rerr, io.EOF) {
				return rerr
			}
			last = m == 0
		}
		binary.BigEndian.PutUint32(nonce[8:], idx)
		out = aead.Seal(out[:0], nonce, cur[:n], chunkAAD(id, idx, last))
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(out)))
		if _, err := w.Write(hdr[:]); err != nil {
			return err
		}
		if _, err := w.Write(out); err != nil {
			return err
		}
		if last {
			return nil
		}
		cur, next, n = next, cur, m
	}
}

// Decrypt writes the plaintext of the encrypted stream r to w.
func Decrypt(w io.Writer, r io.Reader, key []byte, id string) error {
	aead, err := newAEAD(key)
	if err != nil {
		return err
	}
	br := bufio.NewReaderSize(r, 64<<10)
	head := make([]byte, 12)
	if _, err := io.ReadFull(br, head); err != nil || string(head[:4]) != magic {
		return ErrCorrupt
	}
	nonce := make([]byte, 12)
	copy(nonce, head[4:])
	readChunk := func() ([]byte, error) {
		var hdr [4]byte
		if _, err := io.ReadFull(br, hdr[:]); err != nil {
			return nil, err
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n < uint32(aead.Overhead()) || n > chunkSize+uint32(aead.Overhead()) {
			return nil, ErrCorrupt
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(br, b); err != nil {
			return nil, ErrCorrupt
		}
		return b, nil
	}
	cur, err := readChunk()
	if err != nil {
		return ErrCorrupt
	}
	for idx := uint32(0); ; idx++ {
		next, nerr := readChunk()
		last := errors.Is(nerr, io.EOF)
		if nerr != nil && !last {
			return ErrCorrupt
		}
		binary.BigEndian.PutUint32(nonce[8:], idx)
		plain, err := aead.Open(nil, nonce, cur, chunkAAD(id, idx, last))
		if err != nil {
			return ErrCorrupt
		}
		if _, err := w.Write(plain); err != nil {
			return err
		}
		if last {
			return nil
		}
		cur = next
	}
}
