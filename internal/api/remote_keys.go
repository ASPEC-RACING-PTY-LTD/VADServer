package api

// Remote storage nodes: each node gets a P-256 key pair when it is
// approved. Files held only by the node are encrypted for its public key
// (internal/replica VDR2); the private key is sealed with the node key file
// (node_keys.go, never the server KEK) and never leaves this server, so the
// node and anyone between (a Cloudflare tunnel) see ciphertext only.

import (
	"context"
	"crypto/ecdh"
	"errors"
	"strconv"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/replica"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func originKeyAAD(nodeID int64) []byte {
	return []byte("vaultdock node origin key:" + strconv.FormatInt(nodeID, 10))
}

// originKeys caches opened node key pairs.
var originKeys sync.Map // node id -> *originKey

type originKey struct {
	id   string
	priv *ecdh.PrivateKey
	pub  string // base64 PKIX
}

// nodeOriginKey returns a node's key pair, creating it on first use.
func (s *Server) nodeOriginKey(ctx context.Context, nodeID int64) (*originKey, error) {
	if k, ok := originKeys.Load(nodeID); ok {
		return k.(*originKey), nil
	}
	rec, err := s.st.NodeOriginKey(ctx, nodeID)
	if err != nil {
		return nil, err
	}
	if rec.KeyID == "" {
		priv, err := replica.NewOriginKey()
		if err != nil {
			return nil, err
		}
		pub, err := replica.MarshalPublic(priv.PublicKey())
		if err != nil {
			return nil, err
		}
		sealed, err := s.sealNodeSecret(originKeyAAD(nodeID), priv.Bytes())
		if err != nil {
			return nil, err
		}
		if err := s.st.SetNodeOriginKey(ctx, nodeID, store.NodeOriginKey{KeyID: replica.KeyID(priv.PublicKey()), PublicKey: pub, PrivateSealed: sealed}); err != nil {
			return nil, err
		}
		// Re-read: a concurrent caller may have stored its key first.
		if rec, err = s.st.NodeOriginKey(ctx, nodeID); err != nil {
			return nil, err
		}
	}
	raw, err := s.openNodeSecret(originKeyAAD(nodeID), rec.PrivateSealed)
	if err != nil {
		return nil, errors.New("the node's private key cannot be opened (was the node key file replaced?)")
	}
	priv, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, err
	}
	if replica.KeyID(priv.PublicKey()) != rec.KeyID {
		return nil, errors.New("the node's private key does not match its key id")
	}
	k := &originKey{id: rec.KeyID, priv: priv, pub: rec.PublicKey}
	originKeys.Store(nodeID, k)
	return k, nil
}
