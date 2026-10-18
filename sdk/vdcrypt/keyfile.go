package vdcrypt

import (
	"bytes"
	"crypto/ed25519"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

var (
	ErrKeyFile        = errors.New("vdcrypt: invalid key file")
	ErrBundle         = errors.New("vdcrypt: invalid key bundle")
	ErrWrongRecipient = errors.New("vdcrypt: private key does not match the key file recipient")
	ErrPassphraseKDF  = errors.New("vdcrypt: invalid passphrase KDF parameters")
	ErrEscrowSig      = errors.New("vdcrypt: escrow key file is not signed by the pinned server identity")
)

// Argon2id parameters for passphrase recipients (§4.1). Writers use the
// defaults; readers accept anything inside the reader bounds so the §13
// test parameters (t=1, m=8 MiB) also open.
const (
	PassphraseAlg      = "argon2id"
	DefaultArgonT      = 3
	DefaultArgonMKiB   = 64 * 1024
	DefaultArgonP      = 4
	PassphraseSaltSize = 16

	maxArgonT    = 64
	minArgonMKiB = 8 * 1024
	maxArgonMKiB = 1024 * 1024
	minSaltSize  = 16
	maxSaltSize  = 64
)

// PassphraseKDF is the "kdf" member of a passphrase key file.
type PassphraseKDF struct {
	Alg  string `json:"alg"`
	T    uint32 `json:"t"`
	MKiB uint32 `json:"m_kib"`
	P    uint32 `json:"p"`
	Salt []byte `json:"salt"`
}

// NewPassphraseKDF returns the default writer parameters (t=3, m=64 MiB,
// p=4) with a fresh 16-byte salt.
func NewPassphraseKDF(rnd io.Reader) (*PassphraseKDF, error) {
	salt := make([]byte, PassphraseSaltSize)
	if _, err := io.ReadFull(randReader(rnd), salt); err != nil {
		return nil, fmt.Errorf("vdcrypt: random: %w", err)
	}
	return &PassphraseKDF{Alg: PassphraseAlg, T: DefaultArgonT, MKiB: DefaultArgonMKiB, P: DefaultArgonP, Salt: salt}, nil
}

func (k *PassphraseKDF) validate() error {
	if k == nil || k.Alg != PassphraseAlg || k.T < 1 || k.T > maxArgonT ||
		k.MKiB < minArgonMKiB || k.MKiB > maxArgonMKiB || k.P < 1 || k.P > 255 ||
		len(k.Salt) < minSaltSize || len(k.Salt) > maxSaltSize {
		return ErrPassphraseKDF
	}
	return nil
}

// PassphraseKeyPair derives the passphrase recipient key pair
// priv = clamp(Argon2id(passphrase, salt, t, m, p, 32)). The passphrase must
// be valid UTF-8 and already NFC-normalised by the caller (this package has
// no Unicode normalisation tables; see the P1-01 report).
func PassphraseKeyPair(passphrase []byte, kdf *PassphraseKDF) (priv, pub Key, err error) {
	if err := kdf.validate(); err != nil {
		return Key{}, Key{}, err
	}
	if len(passphrase) == 0 || !utf8.Valid(passphrase) {
		return Key{}, Key{}, ErrInvalidKey
	}
	raw := argon2.IDKey(passphrase, kdf.Salt, kdf.T, kdf.MKiB, uint8(kdf.P), KeySize)
	var seed Key
	copy(seed[:], raw)
	clear(raw)
	defer seed.Wipe()
	return X25519FromSeed(seed)
}

// Bundle is the plaintext inside a wrap (§4.3): a full bundle (RMK_e and
// DK) or, for metadata escrow only, a meta bundle (K_meta_e).
type Bundle struct {
	Epoch uint32
	Meta  bool // true: only KMeta is set
	RMK   Key
	DK    Key
	KMeta Key
}

// FullBundle builds {"epoch":e,"rmk":…,"dk":…}.
func FullBundle(s EpochSecret, dk Key) Bundle { return Bundle{Epoch: s.Epoch, RMK: s.RMK, DK: dk} }

// MetaBundle builds {"epoch":e,"k_meta":…}.
func MetaBundle(k EpochKeys) Bundle { return Bundle{Epoch: k.Epoch, Meta: true, KMeta: k.Meta} }

// Secret returns the epoch secret of a full bundle.
func (b Bundle) Secret() EpochSecret { return EpochSecret{Epoch: b.Epoch, RMK: b.RMK} }

// Wipe zeroes the key material.
func (b *Bundle) Wipe() {
	b.RMK.Wipe()
	b.DK.Wipe()
	b.KMeta.Wipe()
}

type fullBundleWire struct {
	Epoch uint32 `json:"epoch"`
	RMK   []byte `json:"rmk"`
	DK    []byte `json:"dk"`
}

type metaBundleWire struct {
	Epoch uint32 `json:"epoch"`
	KMeta []byte `json:"k_meta"`
}

type anyBundleWire struct {
	Epoch *uint32 `json:"epoch"`
	RMK   []byte  `json:"rmk"`
	DK    []byte  `json:"dk"`
	KMeta []byte  `json:"k_meta"`
}

func (b Bundle) encode() ([]byte, error) {
	if b.Epoch == 0 {
		return nil, ErrInvalidEpoch
	}
	if b.Meta {
		return marshalJSON(metaBundleWire{Epoch: b.Epoch, KMeta: b.KMeta[:]})
	}
	return marshalJSON(fullBundleWire{Epoch: b.Epoch, RMK: b.RMK[:], DK: b.DK[:]})
}

func decodeBundle(data []byte) (Bundle, error) {
	var w anyBundleWire
	if err := json.Unmarshal(data, &w); err != nil || w.Epoch == nil || *w.Epoch == 0 {
		return Bundle{}, ErrBundle
	}
	defer func() { clear(w.RMK); clear(w.DK); clear(w.KMeta) }()
	b := Bundle{Epoch: *w.Epoch}
	switch {
	case w.KMeta != nil && w.RMK == nil && w.DK == nil:
		k, err := KeyFromBytes(w.KMeta)
		if err != nil {
			return Bundle{}, ErrBundle
		}
		b.Meta, b.KMeta = true, k
	case w.KMeta == nil && w.RMK != nil && w.DK != nil:
		r, err1 := KeyFromBytes(w.RMK)
		d, err2 := KeyFromBytes(w.DK)
		if err1 != nil || err2 != nil {
			return Bundle{}, ErrBundle
		}
		b.RMK, b.DK = r, d
	default:
		return Bundle{}, ErrBundle
	}
	return b, nil
}

// KeyFileDevice is the "device" member (device key files only).
type KeyFileDevice struct {
	ID      int64  `json:"id"`
	SignPub []byte `json:"sign_pub"`
}

// KeyFileServer is the "server" member (escrow key files only).
type KeyFileServer struct {
	SIKPub     []byte `json:"sik_pub"`
	SEKSig     []byte `json:"sek_sig"`
	SEKVersion uint32 `json:"sek_version"`
}

// KeyFile is a plaintext JSON key file keys/<oid> (§4.3).
type KeyFile struct {
	V            int            `json:"v"`
	Repo         string         `json:"repo"`
	KeyID        string         `json:"key_id"`
	Kind         Kind           `json:"kind"`
	Epoch        uint32         `json:"epoch"`
	Created      int64          `json:"created"`
	Label        string         `json:"label"`
	RecipientPub []byte         `json:"recipient_pub"`
	EPK          []byte         `json:"epk"`
	KDF          *PassphraseKDF `json:"kdf,omitempty"`
	Device       *KeyFileDevice `json:"device,omitempty"`
	Server       *KeyFileServer `json:"server,omitempty"`
	Wrapped      []byte         `json:"wrapped"`
}

// KeyFileParams describes a key file to create. KDF, Device and Server must
// be set exactly for the passphrase, device and escrow kinds respectively.
type KeyFileParams struct {
	Repo         RepoID
	Kind         Kind
	Created      int64
	Label        string
	RecipientPub Key
	KDF          *PassphraseKDF
	Device       *KeyFileDevice
	Server       *KeyFileServer
}

// NewKeyFile wraps bundle to the recipient. The key_id is 16 random bytes
// (32 hex). Meta bundles are allowed only for the escrow kind.
func NewKeyFile(rnd io.Reader, p KeyFileParams, b Bundle) (*KeyFile, error) {
	if b.Meta && p.Kind != KindEscrow {
		return nil, ErrBundle
	}
	var id [16]byte
	if _, err := io.ReadFull(randReader(rnd), id[:]); err != nil {
		return nil, fmt.Errorf("vdcrypt: random: %w", err)
	}
	kf := &KeyFile{
		V: 1, Repo: p.Repo.String(), KeyID: hex.EncodeToString(id[:]), Kind: p.Kind,
		Epoch: b.Epoch, Created: p.Created, Label: p.Label,
		RecipientPub: append([]byte(nil), p.RecipientPub[:]...),
		KDF:          p.KDF, Device: p.Device, Server: p.Server,
	}
	if err := kf.checkMembers(); err != nil {
		return nil, err
	}
	payload, err := b.encode()
	if err != nil {
		return nil, err
	}
	defer clear(payload)
	epk, wrapped, err := Wrap(rnd, p.Repo, p.Kind, p.RecipientPub, kf.KeyID, payload)
	if err != nil {
		return nil, err
	}
	kf.EPK, kf.Wrapped = epk[:], wrapped
	return kf, nil
}

func (kf *KeyFile) checkMembers() error {
	if !kf.Kind.Valid() {
		return ErrKind
	}
	if (kf.KDF != nil) != (kf.Kind == KindPassphrase) ||
		(kf.Device != nil) != (kf.Kind == KindDevice) ||
		(kf.Server != nil) != (kf.Kind == KindEscrow) {
		return ErrKeyFile
	}
	if kf.KDF != nil {
		if err := kf.KDF.validate(); err != nil {
			return err
		}
	}
	if kf.Device != nil && len(kf.Device.SignPub) != ed25519.PublicKeySize {
		return ErrKeyFile
	}
	if kf.Server != nil && (len(kf.Server.SIKPub) != ed25519.PublicKeySize || len(kf.Server.SEKSig) != ed25519.SignatureSize) {
		return ErrKeyFile
	}
	return nil
}

func (kf *KeyFile) validate() error {
	if kf.V != 1 || kf.Epoch == 0 || len(kf.RecipientPub) != KeySize || len(kf.EPK) != KeySize ||
		len(kf.Wrapped) < NonceSize+TagSize || !isLowerHex(kf.KeyID, 32) {
		return ErrKeyFile
	}
	if _, err := ParseRepoID(kf.Repo); err != nil {
		return err
	}
	return kf.checkMembers()
}

func isLowerHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < n; i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Encode returns the stored bytes (compact JSON, no HTML escaping, no
// trailing newline). The object name is OID(Encode()).
func (kf *KeyFile) Encode() ([]byte, error) {
	if err := kf.validate(); err != nil {
		return nil, err
	}
	return marshalJSON(kf)
}

// DecodeKeyFile parses and validates a stored key file.
func DecodeKeyFile(data []byte) (*KeyFile, error) {
	var kf KeyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, ErrKeyFile
	}
	if err := kf.validate(); err != nil {
		return nil, err
	}
	return &kf, nil
}

// Open unwraps the bundle with the recipient private key. It checks that
// the key matches recipient_pub, the bundle epoch matches the key file and
// only escrow key files carry meta bundles.
func (kf *KeyFile) Open(priv Key) (Bundle, error) {
	if err := kf.validate(); err != nil {
		return Bundle{}, err
	}
	repo, _ := ParseRepoID(kf.Repo)
	pub, err := X25519Public(priv)
	if err != nil {
		return Bundle{}, err
	}
	if subtle.ConstantTimeCompare(pub[:], kf.RecipientPub) != 1 {
		return Bundle{}, ErrWrongRecipient
	}
	epk, _ := KeyFromBytes(kf.EPK)
	payload, err := Unwrap(repo, kf.Kind, priv, epk, kf.KeyID, kf.Wrapped)
	if err != nil {
		return Bundle{}, err
	}
	defer clear(payload)
	b, err := decodeBundle(payload)
	if err != nil {
		return Bundle{}, err
	}
	if b.Epoch != kf.Epoch || (b.Meta && kf.Kind != KindEscrow) {
		b.Wipe()
		return Bundle{}, ErrBundle
	}
	return b, nil
}

// OpenPassphrase derives the passphrase recipient key from the key file's
// KDF parameters and opens it.
func (kf *KeyFile) OpenPassphrase(passphrase []byte) (Bundle, error) {
	if kf.Kind != KindPassphrase || kf.KDF == nil {
		return Bundle{}, ErrKind
	}
	priv, _, err := PassphraseKeyPair(passphrase, kf.KDF)
	if err != nil {
		return Bundle{}, err
	}
	defer priv.Wipe()
	return kf.Open(priv)
}

// VerifyEscrow checks that an escrow key file's sik_pub equals the pinned
// SIK and that sek_sig is the SIK signature over recipient_pub ||
// u32(sek_version) (§4.3, §4.6).
func (kf *KeyFile) VerifyEscrow(pinnedSIK ed25519.PublicKey) error {
	if kf.Kind != KindEscrow || kf.Server == nil || len(kf.RecipientPub) != KeySize {
		return ErrKeyFile
	}
	if subtle.ConstantTimeCompare(pinnedSIK, kf.Server.SIKPub) != 1 {
		return ErrEscrowSig
	}
	sek, _ := KeyFromBytes(kf.RecipientPub)
	if !VerifySEK(pinnedSIK, sek, kf.Server.SEKVersion, kf.Server.SEKSig) {
		return ErrEscrowSig
	}
	return nil
}

func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}
