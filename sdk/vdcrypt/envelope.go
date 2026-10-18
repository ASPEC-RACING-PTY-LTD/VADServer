package vdcrypt

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
)

// Envelope layout (§3.3): 0x01 || u32(epoch) || nonce[24] || ciphertext||tag[16].
const (
	EnvelopeVersion  = 0x01
	NonceSize        = chacha20poly1305.NonceSizeX
	TagSize          = chacha20poly1305.Overhead
	envelopeHeader   = 1 + 4 + NonceSize
	EnvelopeOverhead = envelopeHeader + TagSize // 45
)

// Envelope purposes (§3.3 AD table).
const (
	PurposeBlob      = "blob"
	PurposePackHdr   = "packhdr"
	PurposeIndex     = "index"
	PurposeSnapshot  = "snapshot"
	PurposeLock      = "lock"
	PurposeChain     = "chain"
	PurposeMetaChain = "metachain"
	purposeKeyWrap   = "keywrap"
)

var (
	ErrMalformed     = errors.New("vdcrypt: malformed sealed object")
	ErrVersion       = errors.New("vdcrypt: unsupported envelope version")
	ErrEpochMismatch = errors.New("vdcrypt: envelope epoch does not match key epoch")
	ErrDecrypt       = errors.New("vdcrypt: decryption failed (wrong key, wrong AD or tampered data)")
	ErrPurpose       = errors.New("vdcrypt: purpose has an AD suffix or is unknown")
)

func adBase(purpose string, repo RepoID) []byte {
	return []byte("vd1:" + purpose + ":" + repo.String())
}

// BlobAD returns "vd1:blob:<uuid>:<hex(blob_id)>" (data and tree/meta blobs).
func BlobAD(repo RepoID, blobID []byte) []byte {
	return append(adBase(PurposeBlob, repo), ":"+hex.EncodeToString(blobID)...)
}

// PackHeaderAD returns "vd1:packhdr:<uuid>:<hex(pack_nonce)>".
func PackHeaderAD(repo RepoID, packNonce []byte) []byte {
	return append(adBase(PurposePackHdr, repo), ":"+hex.EncodeToString(packNonce)...)
}

// ObjectAD returns "vd1:<purpose>:<uuid>" for the suffix-less purposes
// index, snapshot, lock, chain and metachain.
func ObjectAD(purpose string, repo RepoID) ([]byte, error) {
	switch purpose {
	case PurposeIndex, PurposeSnapshot, PurposeLock, PurposeChain, PurposeMetaChain:
		return adBase(purpose, repo), nil
	}
	return nil, ErrPurpose
}

// KeyWrapAD returns "vd1:keywrap:<uuid>:<key_id>" (§4.2).
func KeyWrapAD(repo RepoID, keyID string) []byte {
	return append(adBase(purposeKeyWrap, repo), ":"+keyID...)
}

// Seal encrypts plaintext into the v1 envelope under key for epoch with a
// random nonce from rnd (nil = crypto/rand).
func Seal(rnd io.Reader, key Key, epoch uint32, plaintext, ad []byte) ([]byte, error) {
	if epoch == 0 {
		return nil, ErrInvalidEpoch
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, envelopeHeader, envelopeHeader+len(plaintext)+TagSize)
	out[0] = EnvelopeVersion
	binary.LittleEndian.PutUint32(out[1:5], epoch)
	if _, err := io.ReadFull(randReader(rnd), out[5:envelopeHeader]); err != nil {
		return nil, fmt.Errorf("vdcrypt: nonce: %w", err)
	}
	return aead.Seal(out, out[5:envelopeHeader], plaintext, ad), nil
}

// EnvelopeEpoch returns the epoch recorded in a sealed envelope so the
// reader can pick the right key.
func EnvelopeEpoch(sealed []byte) (uint32, error) {
	if len(sealed) < EnvelopeOverhead {
		return 0, ErrMalformed
	}
	if sealed[0] != EnvelopeVersion {
		return 0, ErrVersion
	}
	e := binary.LittleEndian.Uint32(sealed[1:5])
	if e == 0 {
		return 0, ErrInvalidEpoch
	}
	return e, nil
}

// Open decrypts a v1 envelope with key, which must belong to epoch.
func Open(key Key, epoch uint32, sealed, ad []byte) ([]byte, error) {
	e, err := EnvelopeEpoch(sealed)
	if err != nil {
		return nil, err
	}
	if e != epoch {
		return nil, ErrEpochMismatch
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, sealed[5:envelopeHeader], sealed[envelopeHeader:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}

// sealRaw is XChaCha20-Poly1305 with the 24-byte nonce prepended (§4.2).
func sealRaw(rnd io.Reader, key Key, plaintext, ad []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, NonceSize, NonceSize+len(plaintext)+TagSize)
	if _, err := io.ReadFull(randReader(rnd), out); err != nil {
		return nil, fmt.Errorf("vdcrypt: nonce: %w", err)
	}
	return aead.Seal(out, out[:NonceSize], plaintext, ad), nil
}

func openRaw(key Key, sealed, ad []byte) ([]byte, error) {
	if len(sealed) < NonceSize+TagSize {
		return nil, ErrMalformed
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	pt, err := aead.Open(nil, sealed[:NonceSize], sealed[NonceSize:], ad)
	if err != nil {
		return nil, ErrDecrypt
	}
	return pt, nil
}
