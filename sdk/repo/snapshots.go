package repo

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// ErrNoDeviceKey means no device key file carries the signing key of a
// snapshot's device; §8.1 treats this as a warning, not a failure.
var ErrNoDeviceKey = errors.New("repo: no device key file for the snapshot signer")

// ReadBlob reads one blob through the index with a ranged read of its pack.
// Full holders verify the blob id; metadata-only holders can read tree and
// meta blobs, which are authenticated by the AEAD (the blob id is in the
// AD), but cannot read data blobs.
func (r *Repo) ReadBlob(ctx context.Context, id pack.BlobID) ([]byte, error) {
	loc, ok := r.index.Lookup(id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrBlobNotFound, id)
	}
	if loc.Class == pack.ClassData && r.metaOnly {
		return nil, ErrMetadataOnly
	}
	sealed, err := r.be.GetRange(ctx, KindData, loc.Pack, int64(loc.Entry.Offset), int64(loc.Entry.Length))
	if err != nil {
		return nil, fmt.Errorf("repo: read blob %s from %s: %w", id, loc.Pack, err)
	}
	if r.metaOnly {
		return openMetaBlob(r.id, r.metaKeys(), loc.Entry, sealed)
	}
	return pack.OpenBlob(r.Keyring(), loc.Entry, sealed)
}

// openMetaBlob opens a tree/meta blob with K_meta only. Without DK the
// blob id cannot be recomputed; the AEAD binds the id through the AD.
func openMetaBlob(repo vdcrypt.RepoID, metaKeys map[uint32]vdcrypt.Key, e pack.Entry, sealed []byte) ([]byte, error) {
	if c, err := e.Type.Class(); err != nil || c != pack.ClassMeta {
		return nil, ErrMetadataOnly
	}
	if int64(e.RawLength) > int64(e.Type.MaxRaw()) || len(sealed) != int(e.Length) {
		return nil, pack.ErrBlobBounds
	}
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobAuth, err)
	}
	key, ok := metaKeys[epoch]
	if !ok {
		return nil, fmt.Errorf("%w %d", ErrNoEpoch, epoch)
	}
	payload, err := vdcrypt.Open(key, epoch, sealed, vdcrypt.BlobAD(repo, e.ID[:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobAuth, err)
	}
	raw, err := compress.Decode(payload, int(e.RawLength))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobPayload, err)
	}
	return raw, nil
}

// ReadTree reads and decodes a tree blob by hex id.
func (r *Repo) ReadTree(ctx context.Context, idHex string) (*snapshot.Tree, error) {
	raw, err := hex.DecodeString(idHex)
	if err != nil || len(raw) != pack.IDSize {
		return nil, fmt.Errorf("%w: tree id %q", ErrInvalidObject, idHex)
	}
	var id pack.BlobID
	copy(id[:], raw)
	b, err := r.ReadBlob(ctx, id)
	if err != nil {
		return nil, err
	}
	return snapshot.DecodeTree(b)
}

// SaveSnapshot writes the pending index first (§11), then sets the root's
// repo and epoch, signs it with the device key, seals it under K_meta of
// the current epoch and stores it at snapshots/<oid>.
func (r *Repo) SaveSnapshot(ctx context.Context, root *snapshot.Root, device ed25519.PrivateKey) (string, error) {
	ek, err := r.writable()
	if err != nil {
		return "", err
	}
	if _, err := r.FlushIndex(ctx); err != nil {
		return "", err
	}
	root.Repo = r.id.String()
	root.Epoch = ek.Epoch
	if err := root.Sign(device); err != nil {
		return "", err
	}
	sealed, err := snapshot.SealRoot(r.opts.Rand, r.id, ek, root)
	if err != nil {
		return "", err
	}
	return r.putObject(ctx, KindSnapshots, sealed)
}

// ListSnapshots returns the snapshot object oids, sorted.
func (r *Repo) ListSnapshots(ctx context.Context) ([]string, error) {
	return listOIDs(ctx, r.be, KindSnapshots)
}

// LoadSnapshot opens a snapshot object and returns the root and its stored
// JSON. The signature is not checked; use VerifySnapshot.
func (r *Repo) LoadSnapshot(ctx context.Context, oid string) (*snapshot.Root, []byte, error) {
	b, err := getObject(ctx, r.be, KindSnapshots, oid)
	if err != nil {
		return nil, nil, err
	}
	return snapshot.OpenRoot(r.id, r.metaKeys(), b)
}

// DeviceSignKey returns the signing key of a device from its key files,
// matching the snapshot's device key_id first, then the device id.
func (r *Repo) DeviceSignKey(d snapshot.Device) (ed25519.PublicKey, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var byID ed25519.PublicKey
	for _, oid := range sortedKeys(r.keyFiles) {
		kf := r.keyFiles[oid]
		if kf.Kind != vdcrypt.KindDevice || kf.Device == nil {
			continue
		}
		if d.KeyID != "" && kf.KeyID == d.KeyID {
			return ed25519.PublicKey(kf.Device.SignPub), nil
		}
		if kf.Device.ID == d.ID && byID == nil {
			byID = ed25519.PublicKey(kf.Device.SignPub)
		}
	}
	if byID == nil {
		return nil, ErrNoDeviceKey
	}
	return byID, nil
}

// VerifySnapshot checks the device signature of a loaded root. A bad
// signature is a hard error; ErrNoDeviceKey is a warning (§8.1).
func (r *Repo) VerifySnapshot(root *snapshot.Root, stored []byte) error {
	pub, err := r.DeviceSignKey(root.Device)
	if err != nil {
		return err
	}
	return snapshot.VerifyRoot(stored, pub)
}

// BlobSize returns the plaintext length of a blob from the index, without
// reading it (restore uses it to seek inside files).
func (r *Repo) BlobSize(id pack.BlobID) (int64, bool) {
	loc, ok := r.index.Lookup(id)
	if !ok {
		return 0, false
	}
	return int64(loc.Entry.RawLength), true
}
