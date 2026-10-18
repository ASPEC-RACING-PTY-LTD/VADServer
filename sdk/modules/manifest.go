package modules

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/version"
)

// ManifestVersion is the component manifest format this package reads.
const ManifestVersion = 1

// Errors returned by manifest parsing and checks.
var (
	ErrInvalidManifest = errors.New("modules: invalid component manifest")
	ErrIncompatible    = errors.New("modules: component incompatible")
	ErrMissingDeps     = errors.New("modules: missing component dependencies")
)

// Manifest is the release component manifest (components-<v>.json).
type Manifest struct {
	ManifestVersion int         `json:"manifest_version"`
	Release         string      `json:"release"`
	Components      []Component `json:"components"`
	// Signature is an optional embedded Ed25519 signature (base64, D29) over
	// SigningPayload of this manifest. A detached components-<v>.json.sig
	// over the raw file bytes is the alternative; see Verifier.
	Signature string `json:"signature,omitempty"`
}

// Component is one downloadable component build.
type Component struct {
	ID      ID     `json:"id"`
	Version string `json:"version"`
	// MinCore is the minimum core version that can run this component.
	MinCore string `json:"min_core"`
	Modes   []Mode `json:"modes"`
	OS      string `json:"os"`   // GOOS, e.g. "windows"
	Arch    string `json:"arch"` // GOARCH, e.g. "amd64"
	URL     string `json:"url"`
	SHA256  string `json:"sha256"` // lowercase hex of the archive
	Size    int64  `json:"size"`
	Depends []ID   `json:"depends,omitempty"`
	// Entry is the executable inside the archive the agent starts.
	Entry string `json:"entry"`
	// Authenticode, when set, is the publisher the archive's executables must
	// be signed by. The agent also requires the installed core's publisher
	// when the core itself is signed (§7.14).
	Authenticode *Authenticode `json:"authenticode,omitempty"`
}

// Authenticode is an expected Authenticode signer.
type Authenticode struct {
	Subject    string `json:"subject"`              // certificate subject CN
	Thumbprint string `json:"thumbprint,omitempty"` // SHA-1 hex, optional pin
}

// ParseManifest decodes and validates a component manifest.
func ParseManifest(raw []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidManifest, err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks the manifest's structure.
func (m *Manifest) Validate() error {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidManifest, fmt.Sprintf(format, a...))
	}
	if m.ManifestVersion != ManifestVersion {
		return bad("unsupported manifest_version %d", m.ManifestVersion)
	}
	if _, ok := version.Parse(m.Release); !ok {
		return bad("release %q is not a version", m.Release)
	}
	if m.Signature != "" {
		if _, err := base64.StdEncoding.DecodeString(m.Signature); err != nil {
			return bad("signature is not base64")
		}
	}
	type key struct {
		id       ID
		os, arch string
	}
	seen := map[key]bool{}
	for i, c := range m.Components {
		if !IsComponent(c.ID) {
			return bad("component %d: %q is not a downloadable component", i, c.ID)
		}
		k := key{c.ID, c.OS, c.Arch}
		if seen[k] {
			return bad("component %d: duplicate %s %s/%s", i, c.ID, c.OS, c.Arch)
		}
		seen[k] = true
		if _, ok := version.Parse(c.Version); !ok {
			return bad("%s: version %q", c.ID, c.Version)
		}
		if _, ok := version.Parse(c.MinCore); !ok {
			return bad("%s: min_core %q", c.ID, c.MinCore)
		}
		if c.OS == "" || c.Arch == "" {
			return bad("%s: os and arch are required", c.ID)
		}
		if len(c.Modes) == 0 {
			return bad("%s: modes are required", c.ID)
		}
		for _, md := range c.Modes {
			if !md.Valid() {
				return bad("%s: unknown mode %q", c.ID, md)
			}
		}
		u, err := url.Parse(c.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return bad("%s: url must be absolute https", c.ID)
		}
		if b, err := hex.DecodeString(c.SHA256); err != nil || len(b) != 32 {
			return bad("%s: sha256 must be 64 hex chars", c.ID)
		}
		if c.Size <= 0 {
			return bad("%s: size must be positive", c.ID)
		}
		if c.Entry == "" {
			return bad("%s: entry is required", c.ID)
		}
		for _, d := range c.Depends {
			if !Known(d) || d == c.ID {
				return bad("%s: bad dependency %q", c.ID, d)
			}
		}
		if c.Authenticode != nil && c.Authenticode.Subject == "" {
			return bad("%s: authenticode subject is required", c.ID)
		}
	}
	return nil
}

// SigningPayload returns the bytes an embedded Signature covers: the
// manifest re-encoded with Signature cleared (encoding/json output is
// deterministic for these types).
func (m *Manifest) SigningPayload() ([]byte, error) {
	c := *m
	c.Signature = ""
	return json.Marshal(&c)
}

// Find returns the component build for id on goos/goarch.
func (m *Manifest) Find(id ID, goos, goarch string) (Component, bool) {
	for _, c := range m.Components {
		if c.ID == id && c.OS == goos && c.Arch == goarch {
			return c, true
		}
	}
	return Component{}, false
}

// CheckCompatible reports whether c can run with coreVersion on goos/goarch.
// A core version that does not parse (a development build) is incompatible.
func CheckCompatible(c Component, coreVersion, goos, goarch string) error {
	if c.OS != goos || c.Arch != goarch {
		return fmt.Errorf("%w: %s is for %s/%s, not %s/%s", ErrIncompatible, c.ID, c.OS, c.Arch, goos, goarch)
	}
	cur, ok := version.Parse(coreVersion)
	if !ok {
		return fmt.Errorf("%w: core version %q is not a release", ErrIncompatible, coreVersion)
	}
	minCore, ok := version.Parse(c.MinCore)
	if !ok {
		return fmt.Errorf("%w: %s: min_core %q", ErrIncompatible, c.ID, c.MinCore)
	}
	if version.Compare(cur, minCore) < 0 {
		return fmt.Errorf("%w: %s needs core %s, have %s", ErrIncompatible, c.ID, c.MinCore, coreVersion)
	}
	return nil
}

// MissingDependencies returns c's dependencies not in available (enabled or
// installed modules), in declaration order.
func MissingDependencies(c Component, available []ID) []ID {
	var out []ID
	for _, d := range c.Depends {
		if !slices.Contains(available, d) {
			out = append(out, d)
		}
	}
	return out
}

// CheckDependencies is MissingDependencies as an error.
func CheckDependencies(c Component, available []ID) error {
	if miss := MissingDependencies(c, available); len(miss) > 0 {
		return fmt.Errorf("%w: %s needs %v", ErrMissingDeps, c.ID, miss)
	}
	return nil
}
