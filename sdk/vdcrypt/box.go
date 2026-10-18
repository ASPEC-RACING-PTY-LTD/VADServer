package vdcrypt

import (
	"errors"
	"io"

	"golang.org/x/crypto/curve25519"
)

// Kind is a recipient kind (§4.1).
type Kind string

const (
	KindDevice     Kind = "device"
	KindEscrow     Kind = "escrow"
	KindRecovery   Kind = "recovery"
	KindPassphrase Kind = "passphrase"
)

var ErrKind = errors.New("vdcrypt: unknown recipient kind")

// Valid reports whether k is one of the four recipient kinds.
func (k Kind) Valid() bool {
	switch k {
	case KindDevice, KindEscrow, KindRecovery, KindPassphrase:
		return true
	}
	return false
}

func wrapKey(repo RepoID, kind Kind, shared, ePub, rPub Key) Key {
	info := labelPrefix + "wrap" + string(kind) + string(ePub[:]) + string(rPub[:])
	return hkdf32(shared[:], repo[:], info)
}

func x25519(priv, pub Key) (Key, error) {
	out, err := curve25519.X25519(priv[:], pub[:])
	if err != nil {
		// Low-order point: the shared secret would be all zero.
		return Key{}, ErrInvalidKey
	}
	k, err := KeyFromBytes(out)
	clear(out)
	return k, err
}

// Wrap seals payload to recipientPub as an X25519 sealed box (§4.2). It
// returns the ephemeral public key (epk) and nonce || ciphertext.
func Wrap(rnd io.Reader, repo RepoID, kind Kind, recipientPub Key, keyID string, payload []byte) (epk Key, wrapped []byte, err error) {
	if !kind.Valid() {
		return Key{}, nil, ErrKind
	}
	ePriv, ePub, err := GenerateX25519(rnd)
	if err != nil {
		return Key{}, nil, err
	}
	defer ePriv.Wipe()
	shared, err := x25519(ePriv, recipientPub)
	if err != nil {
		return Key{}, nil, err
	}
	defer shared.Wipe()
	wk := wrapKey(repo, kind, shared, ePub, recipientPub)
	defer wk.Wipe()
	wrapped, err = sealRaw(rnd, wk, payload, KeyWrapAD(repo, keyID))
	if err != nil {
		return Key{}, nil, err
	}
	return ePub, wrapped, nil
}

// Unwrap opens a sealed box with the recipient's private key.
func Unwrap(repo RepoID, kind Kind, recipientPriv, epk Key, keyID string, wrapped []byte) ([]byte, error) {
	if !kind.Valid() {
		return nil, ErrKind
	}
	rPub, err := X25519Public(recipientPriv)
	if err != nil {
		return nil, err
	}
	shared, err := x25519(recipientPriv, epk)
	if err != nil {
		return nil, err
	}
	defer shared.Wipe()
	wk := wrapKey(repo, kind, shared, epk, rPub)
	defer wk.Wipe()
	return openRaw(wk, wrapped, KeyWrapAD(repo, keyID))
}
