package snapshot

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// NewFileHash returns the streaming keyed whole-file hash of §8.2:
// h = HMAC-SHA256(K_fh, whole file). Pass RepoKeys.FileHash.
func NewFileHash(kfh vdcrypt.Key) hash.Hash { return hmac.New(sha256.New, kfh[:]) }

// FileHash hashes everything r yields under K_fh.
func FileHash(kfh vdcrypt.Key, r io.Reader) ([32]byte, error) {
	var out [32]byte
	h := NewFileHash(kfh)
	if _, err := io.Copy(h, r); err != nil {
		return out, err
	}
	h.Sum(out[:0])
	return out, nil
}

// FileHashBytes hashes data under K_fh.
func FileHashBytes(kfh vdcrypt.Key, data []byte) [32]byte {
	var out [32]byte
	h := NewFileHash(kfh)
	h.Write(data)
	h.Sum(out[:0])
	return out
}

// VerifyFileHash compares a computed hash with the hex `h` of a tree entry
// in constant time.
func VerifyFileHash(sum [32]byte, entryHex string) bool {
	want, err := hex.DecodeString(entryHex)
	if err != nil || len(want) != len(sum) {
		return false
	}
	return hmac.Equal(sum[:], want)
}

// BlobID is blob_id = HMAC-SHA256(K_id, raw_plaintext) (§7.2), used here
// to name tree and meta blobs. Pass RepoKeys.ID.
func BlobID(kid vdcrypt.Key, raw []byte) [32]byte {
	var out [32]byte
	h := hmac.New(sha256.New, kid[:])
	h.Write(raw)
	h.Sum(out[:0])
	return out
}
