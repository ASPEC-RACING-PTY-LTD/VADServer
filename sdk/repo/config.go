package repo

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/chunker"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// Config constants (§5).
const (
	FormatName     = "vaultdock.repo"
	FormatVersion  = 1
	EscrowFull     = "full"
	EscrowMetadata = "metadata"
	Compression    = "zstd"
	AEAD           = "xchacha20poly1305"
)

var (
	ErrConfig        = errors.New("repo: invalid config")
	ErrConfigFormat  = errors.New("repo: unknown repository format")
	ErrConfigVersion = errors.New("repo: repository format version is newer than this build")
	ErrConfigMAC     = errors.New("repo: config mac does not verify")
)

// ChunkerParams is the config "chunker" member.
type ChunkerParams struct {
	Alg string `json:"alg"`
	Min int64  `json:"min"`
	Avg int64  `json:"avg"`
	Max int64  `json:"max"`
}

// Config is the plaintext repository config (§5). Created is Unix
// milliseconds.
type Config struct {
	Format      string        `json:"format"`
	V           int           `json:"v"`
	Repo        string        `json:"repo"`
	Created     int64         `json:"created"`
	EscrowMode  string        `json:"escrow_mode"`
	Chunker     ChunkerParams `json:"chunker"`
	Compression string        `json:"compression"`
	AEAD        string        `json:"aead"`
	PackTarget  int64         `json:"pack_target"`
	PackMax     int64         `json:"pack_max"`
	RecoveryPub []byte        `json:"recovery_pub"`
	MAC         string        `json:"mac"`
}

// NewConfig returns a v1 config with the default chunker and pack sizes.
// The mac is empty until Encode.
func NewConfig(repo vdcrypt.RepoID, created int64, escrowMode string, recoveryPub vdcrypt.Key) *Config {
	return &Config{
		Format: FormatName, V: FormatVersion, Repo: repo.String(), Created: created, EscrowMode: escrowMode,
		Chunker:     ChunkerParams{Alg: chunker.Name, Min: chunker.MinSize, Avg: chunker.AvgSize, Max: chunker.MaxSize},
		Compression: Compression, AEAD: AEAD, PackTarget: pack.TargetSize, PackMax: pack.MaxSize,
		RecoveryPub: append([]byte(nil), recoveryPub[:]...),
	}
}

func validEscrowMode(m string) bool { return m == EscrowFull || m == EscrowMetadata }

// validate checks the documented fields of a v1 config (not the mac).
func (c *Config) validate() error {
	if c.Format != FormatName {
		return ErrConfigFormat
	}
	if c.V > FormatVersion {
		return ErrConfigVersion
	}
	if c.V != FormatVersion {
		return ErrConfig
	}
	if _, err := vdcrypt.ParseRepoID(c.Repo); err != nil {
		return fmt.Errorf("%w: repo: %w", ErrConfig, err)
	}
	ch := c.Chunker
	switch {
	case !validEscrowMode(c.EscrowMode),
		ch.Alg != chunker.Name || ch.Min != chunker.MinSize || ch.Avg != chunker.AvgSize || ch.Max != chunker.MaxSize,
		c.Compression != Compression, c.AEAD != AEAD,
		c.PackTarget <= 0 || c.PackMax < c.PackTarget || c.PackMax > pack.MaxSize,
		len(c.RecoveryPub) != vdcrypt.KeySize:
		return ErrConfig
	}
	return nil
}

// RepoID returns the parsed repository id.
func (c *Config) RepoID() vdcrypt.RepoID {
	id, _ := vdcrypt.ParseRepoID(c.Repo)
	return id
}

func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// ConfigMACInput returns JCS(config without mac) for stored config bytes.
// Every member except mac is covered, including unknown ones.
func ConfigMACInput(stored []byte) ([]byte, error) {
	if _, err := snapshot.Canonicalize(stored); err != nil { // rejects duplicate members
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(stored, &m); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	delete(m, "mac")
	b, err := json.Marshal(m)
	if err != nil {
		return nil, err
	}
	return snapshot.Canonicalize(b)
}

// ConfigMAC returns hex(HMAC-SHA256(K_mac, JCS(config without mac))).
func ConfigMAC(kMAC vdcrypt.Key, stored []byte) (string, error) {
	in, err := ConfigMACInput(stored)
	if err != nil {
		return "", err
	}
	m := hmac.New(sha256.New, kMAC[:])
	m.Write(in)
	return hex.EncodeToString(m.Sum(nil)), nil
}

// Encode validates c, sets c.MAC under kMAC and returns the stored bytes
// (compact JSON in field order, no trailing newline).
func (c *Config) Encode(kMAC vdcrypt.Key) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	c.MAC = ""
	unsigned, err := encodeJSON(c)
	if err != nil {
		return nil, err
	}
	if c.MAC, err = ConfigMAC(kMAC, unsigned); err != nil {
		return nil, err
	}
	return encodeJSON(c)
}

// ParseConfig parses stored config bytes and checks format, version and
// the documented fields. Unknown members are ignored here but covered by
// the mac. The mac itself is checked by VerifyConfig, which needs K_mac.
func ParseConfig(stored []byte) (*Config, error) {
	var probe struct {
		Format string `json:"format"`
		V      int    `json:"v"`
	}
	if err := json.Unmarshal(stored, &probe); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if probe.Format != FormatName {
		return nil, ErrConfigFormat
	}
	if probe.V > FormatVersion {
		return nil, ErrConfigVersion
	}
	var c Config
	if err := json.Unmarshal(stored, &c); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrConfig, err)
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if !IsOID(c.MAC) {
		return nil, fmt.Errorf("%w: mac", ErrConfig)
	}
	return &c, nil
}

// VerifyConfig checks the mac of stored config bytes in constant time.
func VerifyConfig(kMAC vdcrypt.Key, stored []byte) error {
	c, err := ParseConfig(stored)
	if err != nil {
		return err
	}
	want, err := ConfigMAC(kMAC, stored)
	if err != nil {
		return err
	}
	if !hmac.Equal([]byte(want), []byte(c.MAC)) {
		return ErrConfigMAC
	}
	return nil
}
