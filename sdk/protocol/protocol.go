// Package protocol defines the device request-signing scheme shared by the
// server and the desktop client.
//
// Devices hold an Ed25519 key pair generated locally; only the public key is
// sent to the server during enrolment. Every authenticated device request is
// signed over a canonical string that binds the method, path, query, body
// hash, a timestamp and a random nonce. The server rejects stale timestamps
// and reused nonces, which prevents replay even if a request is captured
// after TLS termination (for example in a reverse proxy log).
package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

const (
	HeaderDevice     = "X-VD-Device"
	HeaderTimestamp  = "X-VD-Timestamp"
	HeaderNonce      = "X-VD-Nonce"
	HeaderBodySHA256 = "X-VD-Content-SHA256"
	HeaderSignature  = "X-VD-Signature"
	HeaderServerTime = "X-VD-Server-Time"

	// SignatureVersion prefixes the canonical string so the scheme can evolve.
	SignatureVersion = "VD1"

	// MaxClockSkew is the accepted difference between client and server time.
	MaxClockSkew = 5 * time.Minute

	// EmptySHA256 is the hex SHA-256 of an empty body.
	EmptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
)

// CanonicalString builds the exact bytes that are signed.
func CanonicalString(method, pathAndQuery, deviceID string, timestampMillis int64, nonce, bodySHA256 string) string {
	return strings.Join([]string{
		SignatureVersion,
		strings.ToUpper(method),
		pathAndQuery,
		deviceID,
		strconv.FormatInt(timestampMillis, 10),
		nonce,
		strings.ToLower(bodySHA256),
	}, "\n")
}

// SignedHeaders holds the header values a client attaches to a request.
type SignedHeaders struct {
	DeviceID   string
	Timestamp  string
	Nonce      string
	BodySHA256 string
	Signature  string
}

// Sign produces headers for a request. now should already include any
// server clock offset the client has learned.
func Sign(priv ed25519.PrivateKey, deviceID, method, pathAndQuery, bodySHA256 string, now time.Time) (SignedHeaders, error) {
	nonce, err := NewNonce()
	if err != nil {
		return SignedHeaders{}, err
	}
	ts := now.UnixMilli()
	msg := CanonicalString(method, pathAndQuery, deviceID, ts, nonce, bodySHA256)
	sig := ed25519.Sign(priv, []byte(msg))
	return SignedHeaders{
		DeviceID:   deviceID,
		Timestamp:  strconv.FormatInt(ts, 10),
		Nonce:      nonce,
		BodySHA256: strings.ToLower(bodySHA256),
		Signature:  base64.StdEncoding.EncodeToString(sig),
	}, nil
}

var (
	ErrBadSignature = errors.New("invalid request signature")
	ErrStale        = errors.New("request timestamp outside the accepted window")
	ErrMalformed    = errors.New("malformed signature headers")
)

// Verify checks the signature and timestamp window. Nonce uniqueness must be
// enforced by the caller because it requires shared state.
func Verify(pub ed25519.PublicKey, method, pathAndQuery string, h SignedHeaders, now time.Time) (time.Time, error) {
	ts, err := strconv.ParseInt(h.Timestamp, 10, 64)
	if err != nil || h.Nonce == "" || len(h.Nonce) > 64 || len(h.BodySHA256) != 64 {
		return time.Time{}, ErrMalformed
	}
	if _, err := hex.DecodeString(h.BodySHA256); err != nil {
		return time.Time{}, ErrMalformed
	}
	t := time.UnixMilli(ts)
	if d := now.Sub(t); d > MaxClockSkew || d < -MaxClockSkew {
		return t, ErrStale
	}
	sig, err := base64.StdEncoding.DecodeString(h.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize || len(pub) != ed25519.PublicKeySize {
		return t, ErrMalformed
	}
	msg := CanonicalString(method, pathAndQuery, h.DeviceID, ts, h.Nonce, h.BodySHA256)
	if !ed25519.Verify(pub, []byte(msg), sig) {
		return t, ErrBadSignature
	}
	return t, nil
}

// NewNonce returns 16 random bytes encoded as base64url.
func NewNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SHA256Hex hashes a byte slice.
func SHA256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// EncodePublicKey encodes an Ed25519 public key for transport.
func EncodePublicKey(pub ed25519.PublicKey) string {
	return base64.StdEncoding.EncodeToString(pub)
}

// DecodePublicKey parses a transported Ed25519 public key.
func DecodePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid Ed25519 public key")
	}
	return ed25519.PublicKey(b), nil
}

// KeyFingerprint returns a short, human-comparable fingerprint of a public key.
func KeyFingerprint(pub ed25519.PublicKey) string {
	s := sha256.Sum256(pub)
	h := strings.ToUpper(hex.EncodeToString(s[:8]))
	return h[0:4] + "-" + h[4:8] + "-" + h[8:12] + "-" + h[12:16]
}
