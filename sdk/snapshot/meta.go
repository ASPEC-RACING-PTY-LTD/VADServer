package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/compress"
)

// Meta blob schemas (blob type 2, §8.3).
const (
	SchemaInventory = "inventory/1"
	SchemaRegistry  = "registry/1"
)

// ErrMeta reports a malformed meta blob.
var ErrMeta = errors.New("snapshot: invalid meta blob")

// Inventory is the "inventory/1" meta blob.
type Inventory struct {
	Schema      string        `json:"schema"`
	Apps        []App         `json:"apps"`
	Games       []Game        `json:"games"`
	DevProjects []DevProject  `json:"devprojects"`
	NotPortable []NotPortable `json:"not_portable"`
}

// App is an installed application.
type App struct {
	Name            string   `json:"name"`
	Version         string   `json:"version"`
	Publisher       string   `json:"publisher"`
	Source          string   `json:"source"`
	PackageID       string   `json:"package_id"`
	MatchMethod     string   `json:"match_method"`
	Tier            int      `json:"tier"`
	InstallScope    string   `json:"install_scope"`
	Mode            string   `json:"mode"`
	InstallLocation string   `json:"install_location"`
	UninstallKey    string   `json:"uninstall_key"`
	Flags           []string `json:"flags"`
	DataRoots       []string `json:"data_roots"`
	Registry        []string `json:"registry"`
}

// Game is a game with save roots.
type Game struct {
	Name           string   `json:"name"`
	Store          string   `json:"store"`
	AppID          string   `json:"app_id"`
	SaveRoots      []string `json:"save_roots"`
	ManifestSource string   `json:"manifest_source"`
}

// DevProject is a source repository on the device.
type DevProject struct {
	Root   string `json:"root"`
	Remote string `json:"remote"`
	Branch string `json:"branch"`
	Head   string `json:"head"`
	Dirty  bool   `json:"dirty"`
	Pushed bool   `json:"pushed"`
	Mode   string `json:"mode"`
}

// NotPortable is an item that cannot be restored elsewhere, with a reason.
type NotPortable struct {
	Item   string `json:"item"`
	Reason string `json:"reason"`
}

// Registry is the "registry/1" meta blob: one key and its subtree. Nested
// subkeys use the same shape; Schema and Hive are only set at the top.
type Registry struct {
	Schema  string          `json:"schema,omitempty"`
	Hive    string          `json:"hive,omitempty"`
	Key     string          `json:"key"`
	Values  []RegistryValue `json:"values"`
	Subkeys []Registry      `json:"subkeys"`
}

// RegistryValue is one value; D is the raw data in base64.
type RegistryValue struct {
	N    string `json:"n"`
	Type string `json:"type"`
	D    []byte `json:"d"`
}

// EncodeMeta returns the raw JSON of a meta blob (Inventory or Registry,
// with Schema set). Name it with BlobID(K_id, raw) and store it as type 2.
func EncodeMeta(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(raw) > compress.MaxMetaRaw {
		return nil, ErrMeta
	}
	if _, err := MetaSchema(raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// MetaSchema returns the "schema" discriminator of a meta blob.
func MetaSchema(raw []byte) (string, error) {
	var head struct {
		Schema string `json:"schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return "", fmt.Errorf("%w: %v", ErrMeta, err)
	}
	switch head.Schema {
	case SchemaInventory, SchemaRegistry:
		return head.Schema, nil
	}
	return "", fmt.Errorf("%w: unknown schema %q", ErrMeta, head.Schema)
}

// DecodeInventory parses an "inventory/1" meta blob.
func DecodeInventory(raw []byte) (*Inventory, error) {
	var inv Inventory
	if err := json.Unmarshal(raw, &inv); err != nil || inv.Schema != SchemaInventory {
		return nil, ErrMeta
	}
	return &inv, nil
}

// DecodeRegistry parses a "registry/1" meta blob.
func DecodeRegistry(raw []byte) (*Registry, error) {
	var reg Registry
	if err := json.Unmarshal(raw, &reg); err != nil || reg.Schema != SchemaRegistry {
		return nil, ErrMeta
	}
	return &reg, nil
}
