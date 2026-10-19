package nodeagent

// The node secret is stored encrypted (AES-256-GCM) in data/secret.enc. The
// key is a random 32-byte file outside the installation folder (root only,
// /etc/vad-node/node.key by default), mixed with /etc/machine-id. This keeps
// the secret out of copies and backups of the installation folder; it does
// not protect against root on this machine, which can read the key too.

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type storedSecret struct {
	NodeID int64  `json:"node_id"`
	Nonce  []byte `json:"nonce"`
	Data   []byte `json:"data"`
}

func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		if b, err := os.ReadFile(p); err == nil {
			return strings.TrimSpace(string(b))
		}
	}
	return ""
}

// loadKey reads the key file, creating it (0600, directory 0700) if missing.
func loadKey(path string, create bool) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) && create {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		b = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
		return deriveKey(b), nil
	}
	if err != nil {
		return nil, err
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("%s is not a vad-node key", path)
	}
	return deriveKey(b), nil
}

func deriveKey(raw []byte) []byte {
	h := sha256.New()
	h.Write([]byte("vad-node secret v1\x00"))
	h.Write(raw)
	h.Write([]byte(machineID()))
	return h.Sum(nil)
}

func aad(nodeID int64) []byte { return []byte(fmt.Sprintf("vad-node:%d", nodeID)) }

func secretPath(c Config) string { return filepath.Join(c.DataDir(), "secret.enc") }

// SaveSecret encrypts and stores the node's id and secret.
func SaveSecret(c Config, nodeID int64, secret string) error {
	key, err := loadKey(c.KeyFile, true)
	if err != nil {
		return fmt.Errorf("encryption key %s: %w", c.KeyFile, err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	b, _ := json.Marshal(storedSecret{NodeID: nodeID, Nonce: nonce, Data: gcm.Seal(nil, nonce, []byte(secret), aad(nodeID))})
	if err := os.MkdirAll(c.DataDir(), 0o700); err != nil {
		return err
	}
	tmp := secretPath(c) + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, secretPath(c))
}

// LoadSecret returns the stored node id and secret; ok is false when none
// was stored yet.
func LoadSecret(c Config) (nodeID int64, secret string, ok bool, err error) {
	b, err := os.ReadFile(secretPath(c))
	if errors.Is(err, os.ErrNotExist) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, err
	}
	var s storedSecret
	if err := json.Unmarshal(b, &s); err != nil {
		return 0, "", false, fmt.Errorf("%s is damaged: %w", secretPath(c), err)
	}
	key, err := loadKey(c.KeyFile, false)
	if err != nil {
		return 0, "", false, fmt.Errorf("encryption key %s: %w", c.KeyFile, err)
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(s.Nonce) != gcm.NonceSize() {
		return 0, "", false, fmt.Errorf("%s is damaged", secretPath(c))
	}
	plain, err := gcm.Open(nil, s.Nonce, s.Data, aad(s.NodeID))
	if err != nil {
		return 0, "", false, fmt.Errorf("cannot decrypt the node secret (was the folder moved to another machine, or %s replaced?)", c.KeyFile)
	}
	return s.NodeID, string(plain), true, nil
}

// ForgetSecret removes the stored secret (after the server revoked the node).
func ForgetSecret(c Config) error {
	err := os.Remove(secretPath(c))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
