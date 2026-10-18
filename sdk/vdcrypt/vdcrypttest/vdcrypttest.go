// Package vdcrypttest holds the fixed inputs of the repository format test
// vectors (backup-format-v1 §13) and the deterministic test RNG that
// supplies their nonces and ephemerals. Never use it outside tests.
package vdcrypttest

import (
	"crypto/sha256"
	"encoding/binary"
)

// RepoUUID is the fixed repository id of every vector.
const RepoUUID = "00000000-0000-4000-8000-000000000000"

// Seq returns the 32 bytes start, start+1, …, start+31.
func Seq(start byte) [32]byte {
	var b [32]byte
	for i := range b {
		b[i] = start + byte(i)
	}
	return b
}

// Fixed inputs (§13).
var (
	DK             = Seq(0x00)
	RMK1           = Seq(0x20)
	RMK2           = Seq(0x40)
	RecoverySecret = Seq(0x60)
	DeviceBoxSeed  = Seq(0x80)
	SEKSeed        = Seq(0xa0)
	DeviceSignSeed = Seq(0xc0)
	SIKSeed        = Seq(0xe0)
)

// RNG is a deterministic byte stream: block i is
// SHA-256("vaultdock.testrng/1" || 0x00 || label || u64le(i)), i = 0, 1, ….
// Each vector uses its own label, so vectors are independent.
type RNG struct {
	label string
	ctr   uint64
	buf   []byte
}

// NewRNG returns a fresh stream for label.
func NewRNG(label string) *RNG { return &RNG{label: label} }

// Read fills p; it never fails.
func (r *RNG) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		if len(r.buf) == 0 {
			h := sha256.New()
			h.Write([]byte("vaultdock.testrng/1\x00" + r.label))
			h.Write(binary.LittleEndian.AppendUint64(nil, r.ctr))
			r.ctr++
			r.buf = h.Sum(nil)
		}
		c := copy(p[n:], r.buf)
		r.buf = r.buf[c:]
		n += c
	}
	return n, nil
}
