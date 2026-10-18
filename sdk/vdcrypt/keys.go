// Package vdcrypt implements the cryptographic layer of the VaultDock
// repository format v1 (sdk/contracts/backup-format-v1.md §3–§4): the key
// hierarchy, the sealed envelope, X25519 sealed-box wraps, key files, the
// backward epoch chain and the VDRK1 recovery key text.
//
// Functions that consume randomness take an io.Reader; nil means
// crypto/rand.Reader. A deterministic reader is only for test vectors.
package vdcrypt

import (
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/curve25519"
)

// KeySize is the size of every symmetric key, X25519 key and secret.
const KeySize = 32

// labelPrefix prefixes every HKDF info string (§3.2, §4.1, §4.2).
const labelPrefix = "vaultdock.repo/1 "

var (
	ErrInvalidKey    = errors.New("vdcrypt: invalid key")
	ErrInvalidRepoID = errors.New("vdcrypt: repository id is not a lowercase canonical UUIDv4")
	ErrInvalidEpoch  = errors.New("vdcrypt: invalid epoch")
)

// Key is a 32-byte secret or public key.
type Key [KeySize]byte

// Wipe zeroes the key (best effort; Go cannot guarantee it, §4.6).
func (k *Key) Wipe() { clear(k[:]) }

// Equal compares two keys in constant time.
func (k Key) Equal(o Key) bool { return subtle.ConstantTimeCompare(k[:], o[:]) == 1 }

// IsZero reports whether the key is all zero bytes.
func (k Key) IsZero() bool { return k.Equal(Key{}) }

// KeyFromBytes copies a 32-byte slice into a Key.
func KeyFromBytes(b []byte) (Key, error) {
	var k Key
	if len(b) != KeySize {
		return k, ErrInvalidKey
	}
	copy(k[:], b)
	return k, nil
}

// RandomKey returns 32 random bytes from rnd (nil = crypto/rand).
func RandomKey(rnd io.Reader) (Key, error) {
	var k Key
	if _, err := io.ReadFull(randReader(rnd), k[:]); err != nil {
		return Key{}, fmt.Errorf("vdcrypt: random: %w", err)
	}
	return k, nil
}

func randReader(rnd io.Reader) io.Reader {
	if rnd == nil {
		return rand.Reader
	}
	return rnd
}

// RepoID is the 16 raw bytes of a repository UUID (uuid_bytes, §2).
type RepoID [16]byte

// ParseRepoID parses a lowercase canonical UUIDv4 (RFC 4122 variant).
func ParseRepoID(s string) (RepoID, error) {
	var id RepoID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return id, ErrInvalidRepoID
	}
	h := s[0:8] + s[9:13] + s[14:18] + s[19:23] + s[24:36]
	for i := 0; i < len(h); i++ {
		c := h[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return id, ErrInvalidRepoID
		}
	}
	if _, err := hex.Decode(id[:], []byte(h)); err != nil {
		return id, ErrInvalidRepoID
	}
	if id[6]>>4 != 4 || id[8]>>6 != 2 {
		return RepoID{}, ErrInvalidRepoID
	}
	return id, nil
}

// String returns the lowercase canonical UUID form.
func (r RepoID) String() string {
	h := hex.EncodeToString(r[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func hkdf32(secret, salt []byte, info string) Key {
	out, err := hkdf.Key(sha256.New, secret, salt, info, KeySize)
	if err != nil {
		// Only possible for absurd lengths; 32 bytes is always valid.
		panic("vdcrypt: hkdf: " + err.Error())
	}
	var k Key
	copy(k[:], out)
	clear(out)
	return k
}

// RepoKeys are the keys derived from the dedup key DK (§3.2).
type RepoKeys struct {
	ID        Key // K_id: blob ids
	Chunker   Key // K_chunker: gear table
	FileHash  Key // K_fh: keyed whole-file hashes
	ConfigMAC Key // K_mac: config integrity
}

// DeriveRepoKeys derives K_id, K_chunker, K_fh and K_mac from DK with
// salt = uuid_bytes.
func DeriveRepoKeys(repo RepoID, dk Key) RepoKeys {
	return RepoKeys{
		ID:        hkdf32(dk[:], repo[:], labelPrefix+"id"),
		Chunker:   hkdf32(dk[:], repo[:], labelPrefix+"chunker"),
		FileHash:  hkdf32(dk[:], repo[:], labelPrefix+"filehash"),
		ConfigMAC: hkdf32(dk[:], repo[:], labelPrefix+"config-mac"),
	}
}

// Wipe zeroes all keys.
func (k *RepoKeys) Wipe() {
	k.ID.Wipe()
	k.Chunker.Wipe()
	k.FileHash.Wipe()
	k.ConfigMAC.Wipe()
}

// EpochSecret is an epoch number and its RMK_e.
type EpochSecret struct {
	Epoch uint32
	RMK   Key
}

// EpochKeys are the keys derived from RMK_e (§3.2).
type EpochKeys struct {
	Epoch uint32
	Data  Key // K_data_e
	Meta  Key // K_meta_e
	Chain Key // K_chain_e (rev 1.2): full chain objects only
}

// Wipe zeroes all keys.
func (k *EpochKeys) Wipe() {
	k.Data.Wipe()
	k.Meta.Wipe()
	k.Chain.Wipe()
}

// Wipe zeroes RMK_e.
func (s *EpochSecret) Wipe() { s.RMK.Wipe() }

// Keys derives K_data_e, K_meta_e and K_chain_e with salt = uuid_bytes || u32(e).
func (s EpochSecret) Keys(repo RepoID) (EpochKeys, error) {
	return DeriveEpochKeys(repo, s.Epoch, s.RMK)
}

// DeriveEpochKeys derives K_data_e, K_meta_e and K_chain_e. Epochs start at 1.
func DeriveEpochKeys(repo RepoID, epoch uint32, rmk Key) (EpochKeys, error) {
	if epoch == 0 {
		return EpochKeys{}, ErrInvalidEpoch
	}
	salt := make([]byte, 0, 20)
	salt = append(salt, repo[:]...)
	salt = binary.LittleEndian.AppendUint32(salt, epoch)
	return EpochKeys{
		Epoch: epoch,
		Data:  hkdf32(rmk[:], salt, labelPrefix+"data"),
		Meta:  hkdf32(rmk[:], salt, labelPrefix+"meta"),
		Chain: hkdf32(rmk[:], salt, labelPrefix+"chain"),
	}, nil
}

// Clamp applies X25519 scalar clamping.
func Clamp(k Key) Key {
	k[0] &= 248
	k[31] &= 127
	k[31] |= 64
	return k
}

// X25519Public returns the public key of a private scalar.
func X25519Public(priv Key) (Key, error) {
	out, err := curve25519.X25519(priv[:], curve25519.Basepoint)
	if err != nil {
		return Key{}, ErrInvalidKey
	}
	return KeyFromBytes(out)
}

// X25519FromSeed clamps a 32-byte seed into a private key and returns the
// key pair. It is how fixed test seeds become keys (§13).
func X25519FromSeed(seed Key) (priv, pub Key, err error) {
	priv = Clamp(seed)
	pub, err = X25519Public(priv)
	return priv, pub, err
}

// GenerateX25519 returns a fresh clamped X25519 key pair.
func GenerateX25519(rnd io.Reader) (priv, pub Key, err error) {
	seed, err := RandomKey(rnd)
	if err != nil {
		return Key{}, Key{}, err
	}
	defer seed.Wipe()
	return X25519FromSeed(seed)
}

// RecoveryPrivate derives recovery_priv = clamp(HKDF(recovery_secret,
// salt=uuid_bytes, info="vaultdock.repo/1 recovery-x25519")) (§4.1).
func RecoveryPrivate(repo RepoID, secret Key) Key {
	k := hkdf32(secret[:], repo[:], labelPrefix+"recovery-x25519")
	return Clamp(k)
}

// RecoveryKeyPair returns the recovery recipient key pair for a secret.
func RecoveryKeyPair(repo RepoID, secret Key) (priv, pub Key, err error) {
	priv = RecoveryPrivate(repo, secret)
	pub, err = X25519Public(priv)
	return priv, pub, err
}

// SEKMessage is the message the SIK signs: sek_pub || u32(sek_version) (§4.3).
func SEKMessage(sekPub Key, version uint32) []byte {
	m := make([]byte, 0, KeySize+4)
	m = append(m, sekPub[:]...)
	return binary.LittleEndian.AppendUint32(m, version)
}

// SignSEK signs a SEK public key and version with the SIK.
func SignSEK(sik ed25519.PrivateKey, sekPub Key, version uint32) []byte {
	return ed25519.Sign(sik, SEKMessage(sekPub, version))
}

// VerifySEK checks a SIK signature over a SEK public key and version.
func VerifySEK(sikPub ed25519.PublicKey, sekPub Key, version uint32, sig []byte) bool {
	if len(sikPub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize {
		return false
	}
	return ed25519.Verify(sikPub, SEKMessage(sekPub, version), sig)
}

// OID returns hex(SHA-256(stored bytes)), the name of every stored object
// except config (§1.5).
func OID(stored []byte) string {
	s := sha256.Sum256(stored)
	return hex.EncodeToString(s[:])
}
