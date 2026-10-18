package modules

import (
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Verification errors.
var (
	ErrBadSignature   = errors.New("modules: manifest signature invalid")
	ErrNoSignature    = errors.New("modules: manifest is not signed")
	ErrChecksum       = errors.New("modules: component sha256 mismatch")
	ErrSizeMismatch   = errors.New("modules: component size mismatch")
	ErrNoTrustedKeys  = errors.New("modules: no trusted manifest keys")
	ErrInvalidKeySize = errors.New("modules: invalid ed25519 public key size")
)

// Verifier checks release integrity (§7.14).
type Verifier interface {
	// VerifyManifest checks sig (a detached signature) over the raw
	// manifest bytes.
	VerifyManifest(raw, sig []byte) error
	// VerifyComponent checks the downloaded archive at path against c.
	VerifyComponent(c Component, path string) error
}

// Ed25519Verifier verifies Ed25519 manifest signatures against embedded
// public keys (any one may match, which allows key rotation) and component
// archives by size and SHA-256. Authenticode checks are platform specific
// and left to the caller.
type Ed25519Verifier struct {
	keys []ed25519.PublicKey
}

var _ Verifier = (*Ed25519Verifier)(nil)

// NewEd25519Verifier returns a verifier trusting keys.
func NewEd25519Verifier(keys ...ed25519.PublicKey) (*Ed25519Verifier, error) {
	if len(keys) == 0 {
		return nil, ErrNoTrustedKeys
	}
	for _, k := range keys {
		if len(k) != ed25519.PublicKeySize {
			return nil, ErrInvalidKeySize
		}
	}
	return &Ed25519Verifier{keys: keys}, nil
}

// VerifyManifest implements Verifier. sig may be raw (64 bytes) or base64
// text, as written to components-<v>.json.sig.
func (v *Ed25519Verifier) VerifyManifest(raw, sig []byte) error {
	s, err := decodeSig(sig)
	if err != nil {
		return err
	}
	for _, k := range v.keys {
		if ed25519.Verify(k, raw, s) {
			return nil
		}
	}
	return ErrBadSignature
}

// VerifyEmbedded parses raw and verifies its embedded Signature over
// SigningPayload. It returns the verified manifest.
func (v *Ed25519Verifier) VerifyEmbedded(raw []byte) (*Manifest, error) {
	m, err := ParseManifest(raw)
	if err != nil {
		return nil, err
	}
	if m.Signature == "" {
		return nil, ErrNoSignature
	}
	payload, err := m.SigningPayload()
	if err != nil {
		return nil, err
	}
	if err := v.VerifyManifest(payload, []byte(m.Signature)); err != nil {
		return nil, err
	}
	return m, nil
}

// VerifyComponent implements Verifier.
func (v *Ed25519Verifier) VerifyComponent(c Component, path string) error {
	return VerifyFile(c, path)
}

// VerifyFile checks the file at path against c's size and SHA-256.
func VerifyFile(c Component, path string) error {
	want, err := hex.DecodeString(c.SHA256)
	if err != nil || len(want) != sha256.Size {
		return fmt.Errorf("%w: %s: bad expected sha256", ErrChecksum, c.ID)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != c.Size {
		return fmt.Errorf("%w: %s: got %d bytes, want %d", ErrSizeMismatch, c.ID, n, c.Size)
	}
	if subtle.ConstantTimeCompare(h.Sum(nil), want) != 1 {
		return fmt.Errorf("%w: %s", ErrChecksum, c.ID)
	}
	return nil
}

// SignManifest returns the base64 Ed25519 signature of payload. It is used
// by the release tooling and tests; the private key never ships.
func SignManifest(priv ed25519.PrivateKey, payload []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
}

func decodeSig(sig []byte) ([]byte, error) {
	if len(sig) == ed25519.SignatureSize {
		return sig, nil
	}
	s, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(s) != ed25519.SignatureSize {
		return nil, ErrBadSignature
	}
	return s, nil
}
