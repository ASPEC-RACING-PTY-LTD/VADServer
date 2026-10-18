// Package snapshot implements snapshot roots, tree blobs and meta blobs of
// the repository format (backup-format-v1 §6, §8): their JSON types, the
// RFC 8785 canonical form that roots are signed over, the keyed whole-file
// hash, the Ed25519 root signature and the sealed root object.
package snapshot

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// Version is the snapshot root and tree format version ("v").
const Version = 1

// payloadZstdJSON is the 0x01 = zstd(JSON) payload prefix (§8.1, §12).
const payloadZstdJSON = 0x01

// Install modes recorded in the root (informational).
const (
	InstallModeSimple   = "simple"
	InstallModeAdvanced = "advanced"
)

// Root errors.
var (
	ErrRoot        = errors.New("snapshot: invalid snapshot root")
	ErrNoSignature = errors.New("snapshot: root is not signed")
	ErrSignature   = errors.New("snapshot: bad root signature")
	ErrPayload     = errors.New("snapshot: unknown payload prefix")
	ErrRootEpoch   = errors.New("snapshot: root epoch does not match its envelope")
	ErrNoEpochKey  = errors.New("snapshot: no meta key for the envelope epoch")
)

// Root is the snapshot root JSON (§8.1, fields of rev 1.0 plus epoch,
// profile and install_mode). Time is Unix milliseconds.
type Root struct {
	V             int           `json:"v"`
	Repo          string        `json:"repo"`
	Time          int64         `json:"time"`
	Parent        *string       `json:"parent"`
	Job           Job           `json:"job"`
	Device        Device        `json:"device"`
	Host          Host          `json:"host"`
	Tokens        Tokens        `json:"tokens"`
	Roots         []RootRef     `json:"roots"`
	Inventory     *string       `json:"inventory"`
	Stats         Stats         `json:"stats"`
	Tags          []string      `json:"tags"`
	IssuesSummary IssuesSummary `json:"issues_summary"`
	Epoch         uint32        `json:"epoch"`
	Profile       Profile       `json:"profile"`
	InstallMode   string        `json:"install_mode"`
	Sig           string        `json:"sig,omitempty"`
}

// Job identifies the backup job.
type Job struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	Mode string `json:"mode"`
}

// Device identifies the writing device; KeyID names its key file, whose
// sign_pub verifies Sig.
type Device struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	KeyID string `json:"key_id"`
}

// Host describes the source machine.
type Host struct {
	Hostname        string `json:"hostname"`
	OS              string `json:"os"`
	User            string `json:"user"`
	SID             string `json:"sid"`
	MachineGUIDHash string `json:"machine_guid_hash"`
}

// RootRef is one backed-up root: a token path and its tree blob id (hex).
type RootRef struct {
	TokenPath string `json:"token_path"`
	Tree      string `json:"tree"`
}

// Stats are totals over the snapshot.
type Stats struct {
	Files int64 `json:"files"`
	Dirs  int64 `json:"dirs"`
	Bytes int64 `json:"bytes"`
}

// IssuesSummary counts skipped items by class.
type IssuesSummary struct {
	Skipped int64            `json:"skipped"`
	ByClass map[string]int64 `json:"by_class"`
}

// Profile is the authorised Windows profile (owner decision 6).
type Profile struct {
	SID       string `json:"sid"`
	Username  string `json:"username"`
	ConsentID string `json:"consent_id"`
}

func isHexID(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && s == lowerASCII(s)
}

func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + 32
		}
	}
	return string(b)
}

// Validate checks the structural rules a writer and reader both enforce.
// It does not check the signature.
func (r *Root) Validate() error {
	bad := func(what string) error { return fmt.Errorf("%w: %s", ErrRoot, what) }
	if r.V != Version {
		return bad("v")
	}
	if _, err := vdcrypt.ParseRepoID(r.Repo); err != nil {
		return bad("repo")
	}
	if r.Epoch == 0 {
		return bad("epoch")
	}
	if r.Parent != nil && !isHexID(*r.Parent) {
		return bad("parent")
	}
	if r.Inventory != nil && !isHexID(*r.Inventory) {
		return bad("inventory")
	}
	for tok := range r.Tokens {
		if !ValidToken(tok) {
			return bad("tokens")
		}
	}
	for _, ref := range r.Roots {
		if _, _, err := SplitTokenPath(ref.TokenPath); err != nil {
			return bad("roots.token_path")
		}
		if !isHexID(ref.Tree) {
			return bad("roots.tree")
		}
	}
	switch r.InstallMode {
	case InstallModeSimple, InstallModeAdvanced:
	default:
		return bad("install_mode")
	}
	return nil
}

// normalize writes empty collections as [] / {} rather than null.
func (r *Root) normalize() {
	if r.Tokens == nil {
		r.Tokens = Tokens{}
	}
	if r.Roots == nil {
		r.Roots = []RootRef{}
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	if r.IssuesSummary.ByClass == nil {
		r.IssuesSummary.ByClass = map[string]int64{}
	}
}

// SigningBytes returns JCS(root without sig), the bytes the device signs.
func (r *Root) SigningBytes() ([]byte, error) {
	c := *r
	c.Sig = ""
	return MarshalJCS(&c)
}

// Sign validates r and sets Sig to the device's Ed25519 signature over
// SigningBytes.
func (r *Root) Sign(device ed25519.PrivateKey) error {
	if len(device) != ed25519.PrivateKeySize {
		return fmt.Errorf("%w: device key", ErrRoot)
	}
	r.normalize()
	if err := r.Validate(); err != nil {
		return err
	}
	msg, err := r.SigningBytes()
	if err != nil {
		return err
	}
	r.Sig = base64.StdEncoding.EncodeToString(ed25519.Sign(device, msg))
	return nil
}

// Encode returns the stored root JSON: JCS of the root including sig.
func (r *Root) Encode() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return MarshalJCS(r)
}

// DecodeRoot parses and validates stored root JSON. Unknown fields are
// ignored (§12); verify the signature with VerifyRoot on the same bytes,
// not on a re-encoding of the result, so that unknown fields stay signed.
func DecodeRoot(data []byte) (*Root, error) {
	var r Root
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRoot, err)
	}
	if err := r.Validate(); err != nil {
		return nil, err
	}
	return &r, nil
}

// VerifyRoot checks the sig member of the stored root JSON data against
// the device's Ed25519 public key (sign_pub of the device key file).
func VerifyRoot(data []byte, devicePub ed25519.PublicKey) error {
	if len(devicePub) != ed25519.PublicKeySize {
		return ErrSignature
	}
	msg, rawSig, err := stripMember(data, "sig")
	if err != nil {
		return err
	}
	if rawSig == nil {
		return ErrNoSignature
	}
	var sigB64 string
	if err := json.Unmarshal(rawSig, &sigB64); err != nil {
		return ErrSignature
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(sigB64)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return ErrSignature
	}
	if !ed25519.Verify(devicePub, msg, sig) {
		return ErrSignature
	}
	return nil
}

// SealRoot encodes a signed root and seals it as a snapshot object:
// seal(K_meta_e, 0x01 || zstd(JSON), AD="vd1:snapshot:<uuid>") (§8.1).
// The envelope epoch is r.Epoch, which must match meta. The object is
// stored at snapshots/<vdcrypt.OID(sealed)>.
func SealRoot(rnd io.Reader, repo vdcrypt.RepoID, meta vdcrypt.EpochKeys, r *Root) ([]byte, error) {
	if r.Sig == "" {
		return nil, ErrNoSignature
	}
	if r.Epoch != meta.Epoch || r.Repo != repo.String() {
		return nil, ErrRootEpoch
	}
	js, err := r.Encode()
	if err != nil {
		return nil, err
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeSnapshot, repo)
	if err != nil {
		return nil, err
	}
	return vdcrypt.Seal(rnd, meta.Meta, meta.Epoch, compress.EncodeZstd(js), ad)
}

// OpenRoot decrypts a snapshot object with the meta key of its envelope epoch
// (metaKeys as returned by vdcrypt.ResolveMetaKeys) and returns the parsed
// root and its stored JSON. The root's epoch and repo must match the
// envelope. The signature is not checked; call VerifyRoot(json, signPub).
func OpenRoot(repo vdcrypt.RepoID, metaKeys map[uint32]vdcrypt.Key, sealed []byte) (*Root, []byte, error) {
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, nil, err
	}
	key, ok := metaKeys[epoch]
	if !ok {
		return nil, nil, ErrNoEpochKey
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeSnapshot, repo)
	if err != nil {
		return nil, nil, err
	}
	payload, err := vdcrypt.Open(key, epoch, sealed, ad)
	if err != nil {
		return nil, nil, err
	}
	if len(payload) == 0 || payload[0] != payloadZstdJSON {
		return nil, nil, ErrPayload
	}
	js, err := compress.DecodeLimit(payload, compress.MaxMetaRaw)
	if err != nil {
		return nil, nil, err
	}
	r, err := DecodeRoot(js)
	if err != nil {
		return nil, nil, err
	}
	if r.Epoch != epoch || r.Repo != repo.String() {
		return nil, nil, ErrRootEpoch
	}
	return r, bytes.Clone(js), nil
}
