package catalog

import (
	"cmp"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/repo"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Bounds on what ingestion reads from a repository.
const (
	maxSmallObject = 64 << 20 // keys, chain, index and snapshot objects
	maxKeyFiles    = 10000
	maxListObjects = 1 << 20
)

// metaReader reads the metadata of one repository with K_meta of every
// epoch (backup-format-v1 §3.2, §4.4): snapshot roots, indexes and tree
// blobs. It never holds K_data, so it works the same under full and
// metadata escrow.
type metaReader struct {
	st    *store.Store
	bs    *backup.Store
	rp    *store.Repository
	id    vdcrypt.RepoID
	meta  map[uint32]vdcrypt.Key
	trees map[pack.BlobID]repo.Location
}

func (r *metaReader) wipe() {
	for e, k := range r.meta {
		k.Wipe()
		delete(r.meta, e)
	}
}

func (r *metaReader) get(ctx context.Context, kind, oid string) ([]byte, error) {
	rc, size, err := r.bs.Get(ctx, r.rp.RepoUUID, kind, oid)
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", kind, oid, err)
	}
	defer rc.Close()
	if size > maxSmallObject {
		return nil, fmt.Errorf("%s/%s is too large (%d bytes)", kind, oid, size)
	}
	return io.ReadAll(io.LimitReader(rc, maxSmallObject))
}

func (r *metaReader) objects(ctx context.Context, kind string, max int) ([]store.RepoObject, error) {
	var out []store.RepoObject
	var after int64
	for {
		page, err := r.st.ListRepoObjects(ctx, r.rp.ID, kind, after, wire.ListMaxLimit)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(out) > max {
			return nil, fmt.Errorf("too many %s objects", kind)
		}
		if len(page) < wire.ListMaxLimit {
			return out, nil
		}
		after = page[len(page)-1].ID
	}
}

// openMetaReader unwraps the newest escrow key file of the repository
// (audited, purpose catalog), walks the meta-chain to K_meta of every epoch
// and loads the tree blob locations of every index. errNoEscrow means the
// repository has no escrow key file the server can open.
func openMetaReader(ctx context.Context, st *store.Store, bs *backup.Store, m *keys.Manager, rp *store.Repository, snapshotID int64) (*metaReader, error) {
	id, err := vdcrypt.ParseRepoID(rp.RepoUUID)
	if err != nil {
		return nil, err
	}
	r := &metaReader{st: st, bs: bs, rp: rp, id: id}

	keyObjs, err := r.objects(ctx, wire.ObjectKindKeys, maxKeyFiles)
	if err != nil {
		return nil, err
	}
	var escrow []*vdcrypt.KeyFile
	for _, o := range keyObjs {
		b, err := r.get(ctx, wire.ObjectKindKeys, o.OID)
		if err != nil {
			return nil, err
		}
		kf, err := vdcrypt.DecodeKeyFile(b)
		if err != nil || kf.Kind != vdcrypt.KindEscrow || kf.Repo != id.String() {
			continue
		}
		escrow = append(escrow, kf)
	}
	if len(escrow) == 0 {
		return nil, errNoEscrow
	}
	slices.SortFunc(escrow, func(a, b *vdcrypt.KeyFile) int { return cmp.Compare(b.Epoch, a.Epoch) })

	var b vdcrypt.Bundle
	opened := false
	for _, kf := range escrow {
		uid, rid, sid := rp.UserID, rp.ID, snapshotID
		b, err = m.OpenKeyFile(ctx, kf, keys.Audit{Purpose: keys.PurposeCatalog, UserID: &uid, RepoID: &rid,
			Details: fmt.Sprintf("catalog ingestion of snapshot %d", sid)})
		if err == nil {
			opened = true
			break
		}
		if errors.Is(err, keys.ErrUnavailable) {
			return nil, err
		}
	}
	if !opened {
		return nil, fmt.Errorf("%w: %w", errNoEscrow, err)
	}
	defer b.Wipe()
	kMeta := b.KMeta
	if !b.Meta {
		ek, err := vdcrypt.DeriveEpochKeys(id, b.Epoch, b.RMK)
		if err != nil {
			return nil, err
		}
		kMeta = ek.Meta
		ek.Wipe()
	}

	chainObjs, err := r.objects(ctx, wire.ObjectKindChain, maxListObjects)
	if err != nil {
		return nil, err
	}
	var chains [][]byte
	for _, o := range chainObjs {
		c, err := r.get(ctx, wire.ObjectKindChain, o.OID)
		if err != nil {
			return nil, err
		}
		chains = append(chains, c)
	}
	if r.meta, err = vdcrypt.WalkMeta(id, b.Epoch, kMeta, chains); err != nil {
		return nil, fmt.Errorf("meta-chain: %w", err)
	}
	if err := r.loadIndexes(ctx); err != nil {
		r.wipe()
		return nil, err
	}
	return r, nil
}

// loadIndexes records the location of every tree blob; the first index
// listing a blob wins (backup-format-v1 §7.4).
func (r *metaReader) loadIndexes(ctx context.Context) error {
	objs, err := r.objects(ctx, wire.ObjectKindIndex, maxListObjects)
	if err != nil {
		return err
	}
	r.trees = map[pack.BlobID]repo.Location{}
	for _, o := range objs {
		sealed, err := r.get(ctx, wire.ObjectKindIndex, o.OID)
		if err != nil {
			return err
		}
		f, _, err := repo.OpenIndex(r.id, r.meta, sealed)
		if err != nil {
			return fmt.Errorf("index %s: %w", o.OID, err)
		}
		for _, p := range f.Packs {
			if p.Class != pack.ClassMeta {
				continue
			}
			for _, bl := range p.Blobs {
				if bl.T != pack.TypeTree {
					continue
				}
				e, err := bl.Entry()
				if err != nil {
					return err
				}
				if _, ok := r.trees[e.ID]; !ok {
					r.trees[e.ID] = repo.Location{Pack: p.Pack, Class: p.Class, Entry: e}
				}
			}
		}
	}
	return nil
}

// root opens a snapshot root object.
func (r *metaReader) root(ctx context.Context, oid string) (*snapshot.Root, error) {
	sealed, err := r.get(ctx, wire.ObjectKindSnapshots, oid)
	if err != nil {
		return nil, err
	}
	root, _, err := snapshot.OpenRoot(r.id, r.meta, sealed)
	if err != nil {
		return nil, err
	}
	if root.Repo != r.id.String() {
		return nil, fmt.Errorf("snapshot root belongs to repository %q", root.Repo)
	}
	return root, nil
}

// tree reads and decodes a tree blob by hex id. The AEAD authenticates the
// blob and binds its id through the AD (backup-format-v1 §7.2).
func (r *metaReader) tree(ctx context.Context, idHex string) (*snapshot.Tree, error) {
	raw, err := hex.DecodeString(idHex)
	if err != nil || len(raw) != pack.IDSize {
		return nil, fmt.Errorf("bad tree id %q", idHex)
	}
	var id pack.BlobID
	copy(id[:], raw)
	loc, ok := r.trees[id]
	if !ok {
		return nil, fmt.Errorf("tree %s is not in any index", idHex)
	}
	e := loc.Entry
	if int64(e.RawLength) > int64(e.Type.MaxRaw()) {
		return nil, pack.ErrBlobBounds
	}
	rc, err := r.bs.GetRange(ctx, r.rp.RepoUUID, wire.ObjectKindData, loc.Pack, int64(e.Offset), int64(e.Length))
	if err != nil {
		return nil, fmt.Errorf("read tree %s: %w", idHex, err)
	}
	sealed, err := io.ReadAll(io.LimitReader(rc, int64(e.Length)+1))
	rc.Close()
	if err != nil {
		return nil, err
	}
	if len(sealed) != int(e.Length) {
		return nil, pack.ErrBlobBounds
	}
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobAuth, err)
	}
	key, ok := r.meta[epoch]
	if !ok {
		return nil, fmt.Errorf("no meta key for epoch %d", epoch)
	}
	payload, err := vdcrypt.Open(key, epoch, sealed, vdcrypt.BlobAD(r.id, e.ID[:]))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobAuth, err)
	}
	plain, err := compress.Decode(payload, int(e.RawLength))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", pack.ErrBlobPayload, err)
	}
	return snapshot.DecodeTree(plain)
}
