package api

// Per-node copy keys. When a node is approved the server generates a random
// 256-bit key for the copies it stores there. The key is kept on this server
// only, sealed (AES-256-GCM) with the node key file
// <state>/keys/node-copies.key, which is created on first use and is
// separate from every other server key. Copies are encrypted with it before
// they leave the server and decrypted when they come back; the node never
// receives it, so the node's owner cannot read the copies.
//
// Back up node-copies.key together with the database: without it the copies
// on nodes cannot be decrypted.

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

type nodeKeyFile struct {
	mu  sync.Mutex
	key []byte
}

var nodeKeyFiles sync.Map // *Server -> *nodeKeyFile

func (s *Server) nodeKeyPath() string {
	return filepath.Join(s.cfg.StateDir, "keys", "node-copies.key")
}

// nodeFileKey loads (creating on first use) the key that seals copy keys.
func (s *Server) nodeFileKey() ([]byte, error) {
	v, _ := nodeKeyFiles.LoadOrStore(s, &nodeKeyFile{})
	f := v.(*nodeKeyFile)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.key != nil {
		return f.key, nil
	}
	p := s.nodeKeyPath()
	b, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			return nil, err
		}
		b = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, b); err != nil {
			return nil, err
		}
		fh, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) { // created meanwhile by another process
			b, err = os.ReadFile(p)
		} else if err == nil {
			_, err = fh.Write(b)
			if cerr := fh.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			return nil, fmt.Errorf("node key file %s: %w", p, err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("node key file %s: %w", p, err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("node key file %s is damaged (expected 32 bytes)", p)
	}
	f.key = b
	return b, nil
}

func nodeKeyAAD(nodeID int64) []byte {
	return []byte("vaultdock node copy key:" + strconv.FormatInt(nodeID, 10))
}

func (s *Server) sealNodeKey(nodeID int64, key []byte) (string, error) {
	return s.sealNodeSecret(nodeKeyAAD(nodeID), key)
}

func (s *Server) openNodeKey(nodeID int64, sealed string) ([]byte, error) {
	key, err := s.openNodeSecret(nodeKeyAAD(nodeID), sealed)
	if err != nil {
		return nil, fmt.Errorf("the node's copy key cannot be opened (was %s replaced?)", s.nodeKeyPath())
	}
	return key, nil
}

// sealNodeSecret seals node key material with the node key file; aad names
// what it is (a copy key, an origin private key) and which node it belongs to.
func (s *Server) sealNodeSecret(aad, secret []byte) (string, error) {
	fk, err := s.nodeFileKey()
	if err != nil {
		return "", err
	}
	block, _ := aes.NewCipher(fk)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, secret, aad)), nil
}

func (s *Server) openNodeSecret(aad []byte, sealed string) ([]byte, error) {
	fk, err := s.nodeFileKey()
	if err != nil {
		return nil, err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	block, _ := aes.NewCipher(fk)
	gcm, _ := cipher.NewGCM(block)
	if err != nil || len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("sealed node key material is damaged")
	}
	return gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], aad)
}

// nodeCopyKey returns the copy key of a node, creating it if the node has
// none yet (nodes approved before copy keys existed).
func (s *Server) nodeCopyKey(ctx context.Context, nodeID int64) ([]byte, error) {
	sealed, err := s.st.StorageNodeCopyKey(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if sealed == "" {
		key := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		if sealed, err = s.sealNodeKey(nodeID, key); err != nil {
			return nil, err
		}
		if err := s.st.SetStorageNodeCopyKey(ctx, nodeID, sealed); err != nil {
			return nil, err
		}
		// Re-read: a concurrent caller may have stored its key first.
		if sealed, err = s.st.StorageNodeCopyKey(ctx, nodeID); err != nil {
			return nil, err
		}
	}
	return s.openNodeKey(nodeID, sealed)
}
