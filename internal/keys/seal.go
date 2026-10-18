package keys

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// sealAD binds a sealed private key to its kind and version, so rows cannot
// be swapped between kinds or versions.
func sealAD(kind string, version uint32) []byte {
	return fmt.Appendf(nil, "vaultdock.server-key/1:%s:%d", kind, version)
}

// seal encrypts a private key under the KEK: nonce(24) || XChaCha20-Poly1305.
func seal(kek vdcrypt.Key, kind string, version uint32, plaintext []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(kek[:])
	if err != nil {
		return nil, err
	}
	out := make([]byte, chacha20poly1305.NonceSizeX, chacha20poly1305.NonceSizeX+len(plaintext)+aead.Overhead())
	if _, err := rand.Read(out); err != nil {
		return nil, err
	}
	return aead.Seal(out, out, plaintext, sealAD(kind, version)), nil
}

// open reverses seal.
func open(kek vdcrypt.Key, kind string, version uint32, sealed []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(kek[:])
	if err != nil {
		return nil, err
	}
	if len(sealed) < chacha20poly1305.NonceSizeX+aead.Overhead() {
		return nil, errors.New("sealed key too short")
	}
	n := chacha20poly1305.NonceSizeX
	return aead.Open(nil, sealed[:n], sealed[n:], sealAD(kind, version))
}
