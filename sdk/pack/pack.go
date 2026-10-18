// Package pack implements blobs and packs of the VaultDock repository
// format v1 (backup-format-v1.md §7.1–§7.3):
//
//	sealed_blob = seal(K_data_e | K_meta_e, comp_u8 || body, AD="vd1:blob:<uuid>:<hex(blob_id)>")
//	pack        = sealed_blob_0 || … || sealed_blob_n || pack_nonce[16] || sealed_header || trailer
//	trailer     = u32(16 + len(sealed_header)) || "VDPACK" || 0x00 || 0x01
//	header_plain = u8(class) || u32(count) || count × (type u8 || blob_id[32] || offset u32 || sealed_len u32 || raw_len u32)
//
// Data packs (class 0) hold data blobs (type 0) sealed under K_data_e; meta
// packs (class 1) hold tree and meta blobs (types 1, 2) sealed under K_meta_e.
// A pack is named by oid = hex(SHA-256(pack bytes)).
package pack

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// Class is the pack class (§7.3).
type Class uint8

const (
	ClassData Class = 0
	ClassMeta Class = 1
)

func (c Class) valid() bool { return c == ClassData || c == ClassMeta }

func (c Class) String() string {
	switch c {
	case ClassData:
		return "data"
	case ClassMeta:
		return "meta"
	}
	return fmt.Sprintf("class(%d)", uint8(c))
}

// BlobType is the blob type (§7.1).
type BlobType uint8

const (
	TypeData BlobType = 0
	TypeTree BlobType = 1
	TypeMeta BlobType = 2
)

// Class returns the pack class that holds blobs of type t.
func (t BlobType) Class() (Class, error) {
	switch t {
	case TypeData:
		return ClassData, nil
	case TypeTree, TypeMeta:
		return ClassMeta, nil
	}
	return 0, fmt.Errorf("%w: blob type %d", ErrInvalidType, uint8(t))
}

// MaxRaw returns the raw size limit of blobs of type t (§7.2).
func (t BlobType) MaxRaw() int {
	if t == TypeData {
		return compress.MaxDataRaw
	}
	return compress.MaxMetaRaw
}

// Format constants.
const (
	NonceSize   = 16 // pack_nonce
	TrailerSize = 12 // u32 length || "VDPACK" || 0x00 || version
	Version     = 0x01
	IDSize      = sha256.Size
	EntrySize   = 1 + IDSize + 4 + 4 + 4 // 45
	headerFixed = 1 + 4                  // class || count

	// TargetSize and MaxSize are config.pack_target and config.pack_max
	// (§5); MaxSize is also the API's max_object_bytes.
	TargetSize = 16 << 20
	MaxSize    = 64 << 20

	// minSealedBlob is the smallest sealed blob: a 1-byte payload.
	minSealedBlob = vdcrypt.EnvelopeOverhead + 1
)

var magic = [7]byte{'V', 'D', 'P', 'A', 'C', 'K', 0x00}

// Errors. Every failure wraps exactly one of these; use errors.Is.
var (
	ErrTruncated    = errors.New("pack: truncated pack or blob")
	ErrTrailer      = errors.New("pack: bad trailer magic")
	ErrVersion      = errors.New("pack: unsupported pack version")
	ErrHeaderLength = errors.New("pack: invalid header length")
	ErrHeaderAuth   = errors.New("pack: header authentication failed")
	ErrHeader       = errors.New("pack: malformed header")
	ErrClass        = errors.New("pack: blob type does not belong to the pack class")
	ErrInvalidType  = errors.New("pack: unknown blob type or pack class")
	ErrBlobAuth     = errors.New("pack: blob authentication failed")
	ErrBlobPayload  = errors.New("pack: invalid blob payload")
	ErrBlobID       = errors.New("pack: blob id does not match content")
	ErrBlobTooLarge = errors.New("pack: blob exceeds the raw size limit")
	ErrBlobBounds   = errors.New("pack: blob entry outside the pack data area")
	ErrPackFull     = errors.New("pack: blob would make the pack exceed pack_max")
	ErrPackTooLarge = errors.New("pack: pack exceeds the maximum size")
	ErrEmpty        = errors.New("pack: pack has no blobs")
	ErrDuplicate    = errors.New("pack: duplicate blob id in pack")
	ErrFinished     = errors.New("pack: writer already finished")
	ErrNoKey        = errors.New("pack: no key for class and epoch")
	ErrOptions      = errors.New("pack: invalid writer options")
)

// BlobID is HMAC-SHA256(K_id, raw plaintext) (§7.2).
type BlobID [IDSize]byte

func (id BlobID) String() string { return hex.EncodeToString(id[:]) }

// ComputeID returns the blob id of raw under K_id.
func ComputeID(kID vdcrypt.Key, raw []byte) BlobID {
	m := hmac.New(sha256.New, kID[:])
	m.Write(raw)
	var id BlobID
	m.Sum(id[:0])
	return id
}

// Entry is one header entry; it is also what the index records (§7.4:
// t, id, o, l, r).
type Entry struct {
	Type      BlobType
	ID        BlobID
	Offset    uint32 // from the pack start
	Length    uint32 // sealed_len
	RawLength uint32 // raw_len
}

// KeyFunc returns the epoch keys for epoch. A holder without data keys
// (metadata escrow) returns keys whose Data is zero.
type KeyFunc func(epoch uint32) (vdcrypt.EpochKeys, error)

// Keyring is what a reader needs: the repository, K_id and the epoch keys.
type Keyring struct {
	Repo   vdcrypt.RepoID
	ID     vdcrypt.Key
	Epochs KeyFunc
}

func (kr *Keyring) key(c Class, epoch uint32) (vdcrypt.Key, error) {
	if kr == nil || kr.Epochs == nil {
		return vdcrypt.Key{}, ErrNoKey
	}
	ek, err := kr.Epochs(epoch)
	if err != nil {
		return vdcrypt.Key{}, fmt.Errorf("%w: epoch %d: %w", ErrNoKey, epoch, err)
	}
	return classKey(ek, c)
}

func classKey(ek vdcrypt.EpochKeys, c Class) (vdcrypt.Key, error) {
	var k vdcrypt.Key
	switch c {
	case ClassData:
		k = ek.Data
	case ClassMeta:
		k = ek.Meta
	default:
		return k, fmt.Errorf("%w: %s", ErrInvalidType, c)
	}
	if k.IsZero() {
		return k, fmt.Errorf("%w: %s epoch %d", ErrNoKey, c, ek.Epoch)
	}
	return k, nil
}

// SealBlob encodes and seals one blob of type t under the class key of ek.
// The returned entry has Offset 0. rnd supplies the nonce (nil = crypto/rand).
func SealBlob(rnd io.Reader, repo vdcrypt.RepoID, kID vdcrypt.Key, ek vdcrypt.EpochKeys, t BlobType, raw []byte, precompressed bool) (Entry, []byte, error) {
	c, err := t.Class()
	if err != nil {
		return Entry{}, nil, err
	}
	if len(raw) > t.MaxRaw() {
		return Entry{}, nil, fmt.Errorf("%w: %d bytes, type %d", ErrBlobTooLarge, len(raw), t)
	}
	key, err := classKey(ek, c)
	if err != nil {
		return Entry{}, nil, err
	}
	id := ComputeID(kID, raw)
	sealed, err := vdcrypt.Seal(randReader(rnd), key, ek.Epoch, compress.Encode(raw, precompressed), vdcrypt.BlobAD(repo, id[:]))
	if err != nil {
		return Entry{}, nil, err
	}
	return Entry{Type: t, ID: id, Length: uint32(len(sealed)), RawLength: uint32(len(raw))}, sealed, nil
}

// OpenBlob opens a sealed blob described by e, decompresses it and checks
// that its content hashes to e.ID. The pack class follows from e.Type.
func OpenBlob(kr *Keyring, e Entry, sealed []byte) ([]byte, error) {
	c, err := e.Type.Class()
	if err != nil {
		return nil, err
	}
	if int64(e.RawLength) > int64(e.Type.MaxRaw()) {
		return nil, fmt.Errorf("%w: raw_len %d, type %d", ErrBlobTooLarge, e.RawLength, e.Type)
	}
	if len(sealed) != int(e.Length) || len(sealed) < minSealedBlob {
		return nil, fmt.Errorf("%w: sealed blob is %d bytes, entry says %d", ErrTruncated, len(sealed), e.Length)
	}
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlobAuth, err)
	}
	key, err := kr.key(c, epoch)
	if err != nil {
		return nil, err
	}
	payload, err := vdcrypt.Open(key, epoch, sealed, vdcrypt.BlobAD(kr.Repo, e.ID[:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlobAuth, err)
	}
	raw, err := compress.Decode(payload, int(e.RawLength))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBlobPayload, err)
	}
	got := ComputeID(kr.ID, raw)
	if !hmac.Equal(got[:], e.ID[:]) {
		return nil, ErrBlobID
	}
	return raw, nil
}
