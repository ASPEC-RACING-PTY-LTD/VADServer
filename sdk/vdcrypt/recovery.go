package vdcrypt

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"io"
	"strings"
	"unicode"
)

// RecoveryPrefix starts every recovery key text (§4.8).
const RecoveryPrefix = "VDRK1-"

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

const (
	recoveryBytes = KeySize + 2               // secret || SHA-256(secret)[0:2]
	recoveryChars = (recoveryBytes*8 + 4) / 5 // 55
	recoveryGroup = 5
)

var (
	// ErrRecoveryFormat: the text is not a VDRK1 recovery key.
	ErrRecoveryFormat = errors.New("this is not a valid VaultDock recovery key")
	// ErrRecoveryChecksum: user-facing checksum error (§4.8).
	ErrRecoveryChecksum = errors.New("the recovery key checksum does not match; check for typing mistakes")
)

// NewRecoverySecret returns a fresh 32-byte recovery secret.
func NewRecoverySecret(rnd io.Reader) (Key, error) { return RandomKey(rnd) }

// EncodeRecoveryKey renders "VDRK1-" plus Crockford base32 (MSB first, the
// final character zero-padded) of secret || SHA-256(secret)[0:2], in groups
// of 5 joined by "-".
func EncodeRecoveryKey(secret Key) string {
	sum := sha256.Sum256(secret[:])
	data := make([]byte, 0, recoveryBytes)
	data = append(data, secret[:]...)
	data = append(data, sum[0], sum[1])
	defer clear(data)

	chars := make([]byte, 0, recoveryChars)
	var acc uint32
	bits := 0
	for _, b := range data {
		acc = acc<<8 | uint32(b)
		bits += 8
		for bits >= 5 {
			bits -= 5
			chars = append(chars, crockford[(acc>>bits)&31])
		}
	}
	if bits > 0 {
		chars = append(chars, crockford[(acc<<(5-bits))&31])
	}
	var sb strings.Builder
	sb.WriteString(RecoveryPrefix)
	for i := 0; i < len(chars); i += recoveryGroup {
		if i > 0 {
			sb.WriteByte('-')
		}
		sb.Write(chars[i:min(i+recoveryGroup, len(chars))])
	}
	return sb.String()
}

func crockfordValue(c byte) int {
	switch c {
	case 'O':
		c = '0'
	case 'I', 'L':
		c = '1'
	}
	return strings.IndexByte(crockford, c)
}

// DecodeRecoveryKey parses recovery key text: case-insensitive, O→0,
// I/L→1, whitespace and "-" ignored. A checksum mismatch returns
// ErrRecoveryChecksum.
func DecodeRecoveryKey(text string) (Key, error) {
	var sb strings.Builder
	for _, r := range text {
		if unicode.IsSpace(r) || r == '-' {
			continue
		}
		if r > unicode.MaxASCII {
			return Key{}, ErrRecoveryFormat
		}
		sb.WriteRune(unicode.ToUpper(r))
	}
	s := sb.String()
	const prefix = "VDRK1"
	if !strings.HasPrefix(s, prefix) {
		return Key{}, ErrRecoveryFormat
	}
	s = s[len(prefix):]
	if len(s) != recoveryChars {
		return Key{}, ErrRecoveryFormat
	}
	data := make([]byte, 0, recoveryBytes)
	defer clear(data)
	var acc uint32
	bits := 0
	for i := 0; i < len(s); i++ {
		v := crockfordValue(s[i])
		if v < 0 {
			return Key{}, ErrRecoveryFormat
		}
		acc = acc<<5 | uint32(v)
		bits += 5
		if bits >= 8 {
			bits -= 8
			data = append(data, byte(acc>>bits))
		}
	}
	if len(data) != recoveryBytes || acc&(1<<bits-1) != 0 {
		return Key{}, ErrRecoveryFormat
	}
	var secret Key
	copy(secret[:], data[:KeySize])
	sum := sha256.Sum256(secret[:])
	if subtle.ConstantTimeCompare(sum[:2], data[KeySize:]) != 1 {
		secret.Wipe()
		return Key{}, ErrRecoveryChecksum
	}
	return secret, nil
}
