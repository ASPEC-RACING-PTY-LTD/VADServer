package repo

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// ErrLockObject is returned for an invalid sealed lock object.
var ErrLockObject = errors.New("repo: invalid lock object")

// LockObject is a sealed lock object locks/<oid> (§10). Server API locks
// are authoritative; these are written only by vaultdock-restore or
// maintenance tooling working offline on a repository copy. Times are Unix
// milliseconds.
type LockObject struct {
	V       int               `json:"v"`
	Holder  map[string]string `json:"holder"`
	Kind    LockKind          `json:"kind"`
	Created int64             `json:"created"`
	Expires int64             `json:"expires"`
}

const lockPayload = 0x01

// SealLock returns lock = seal(K_meta_e, 0x01 || JSON) with AD
// "vd1:lock:<uuid>".
func SealLock(rnd io.Reader, repo vdcrypt.RepoID, ek vdcrypt.EpochKeys, l *LockObject) ([]byte, error) {
	if l.V != 1 || (l.Kind != LockShared && l.Kind != LockExclusive) || l.Expires < l.Created {
		return nil, ErrLockObject
	}
	c := *l
	if c.Holder == nil {
		c.Holder = map[string]string{}
	}
	js, err := encodeJSON(&c)
	if err != nil {
		return nil, err
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeLock, repo)
	if err != nil {
		return nil, err
	}
	return vdcrypt.Seal(rnd, ek.Meta, ek.Epoch, append([]byte{lockPayload}, js...), ad)
}

// OpenLock opens a sealed lock object with the K_meta of its epoch.
func OpenLock(repo vdcrypt.RepoID, metaKeys map[uint32]vdcrypt.Key, sealed []byte) (*LockObject, error) {
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLockObject, err)
	}
	key, ok := metaKeys[epoch]
	if !ok {
		return nil, fmt.Errorf("%w %d", ErrNoEpoch, epoch)
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeLock, repo)
	if err != nil {
		return nil, err
	}
	pt, err := vdcrypt.Open(key, epoch, sealed, ad)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrLockObject, err)
	}
	if len(pt) == 0 || pt[0] != lockPayload {
		return nil, ErrLockObject
	}
	dec := json.NewDecoder(bytes.NewReader(pt[1:]))
	var l LockObject
	if err := dec.Decode(&l); err != nil || l.V != 1 || (l.Kind != LockShared && l.Kind != LockExclusive) {
		return nil, ErrLockObject
	}
	return &l, nil
}

// SealLock seals a lock object under the current epoch.
func (r *Repo) SealLock(l *LockObject) ([]byte, error) {
	r.mu.Lock()
	ek := r.epochs[r.current]
	r.mu.Unlock()
	return SealLock(r.opts.Rand, r.id, ek, l)
}

// OpenLock opens a lock object of any resolved epoch.
func (r *Repo) OpenLock(sealed []byte) (*LockObject, error) {
	return OpenLock(r.id, r.metaKeys(), sealed)
}
