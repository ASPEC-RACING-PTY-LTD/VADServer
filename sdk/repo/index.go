package repo

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

// IndexVersion is the index JSON "v" and also the payload type byte
// (0x01 = zstd(JSON)) of §7.4.
const IndexVersion = 1

// MaxIndexBlobs bounds the blob entries FlushIndex puts in one index
// object, keeping its JSON far below the 32 MiB metadata limit.
const MaxIndexBlobs = 100000

var (
	ErrIndex        = errors.New("repo: invalid index")
	ErrBlobNotFound = errors.New("repo: blob not in index")
)

// IndexBlob is one blob of an index pack (§7.4: id, t, o, l, r).
type IndexBlob struct {
	ID string        `json:"id"`
	T  pack.BlobType `json:"t"`
	O  uint32        `json:"o"`
	L  uint32        `json:"l"`
	R  uint32        `json:"r"`
}

// IndexPack lists the blobs of one pack.
type IndexPack struct {
	Pack  string      `json:"pack"`
	Class pack.Class  `json:"class"`
	Blobs []IndexBlob `json:"blobs"`
}

// IndexFile is the JSON of one index object.
type IndexFile struct {
	V          int         `json:"v"`
	Supersedes []string    `json:"supersedes"`
	Packs      []IndexPack `json:"packs"`
}

// IndexPackOf converts a finished pack to its index record.
func IndexPackOf(p *pack.Pack) IndexPack {
	ip := IndexPack{Pack: p.OID, Class: p.Class, Blobs: make([]IndexBlob, 0, len(p.Entries))}
	for _, e := range p.Entries {
		ip.Blobs = append(ip.Blobs, IndexBlob{ID: e.ID.String(), T: e.Type, O: e.Offset, L: e.Length, R: e.RawLength})
	}
	return ip
}

// Entry converts an index blob back to a pack entry.
func (b IndexBlob) Entry() (pack.Entry, error) {
	raw, err := hex.DecodeString(b.ID)
	if err != nil || len(raw) != pack.IDSize || hex.EncodeToString(raw) != b.ID {
		return pack.Entry{}, fmt.Errorf("%w: blob id %q", ErrIndex, b.ID)
	}
	e := pack.Entry{Type: b.T, Offset: b.O, Length: b.L, RawLength: b.R}
	copy(e.ID[:], raw)
	return e, nil
}

// validate checks an index file: version, oids, classes, and blob types
// belonging to their pack class.
func (f *IndexFile) validate() error {
	if f.V != IndexVersion {
		return fmt.Errorf("%w: v %d", ErrIndex, f.V)
	}
	for _, s := range f.Supersedes {
		if !IsOID(s) {
			return fmt.Errorf("%w: supersedes %q", ErrIndex, s)
		}
	}
	for _, p := range f.Packs {
		if !IsOID(p.Pack) || (p.Class != pack.ClassData && p.Class != pack.ClassMeta) || len(p.Blobs) == 0 {
			return fmt.Errorf("%w: pack %q", ErrIndex, p.Pack)
		}
		for _, b := range p.Blobs {
			e, err := b.Entry()
			if err != nil {
				return err
			}
			c, err := e.Type.Class()
			if err != nil || c != p.Class || int64(e.RawLength) > int64(e.Type.MaxRaw()) || e.Length <= vdcrypt.EnvelopeOverhead {
				return fmt.Errorf("%w: blob %s", ErrIndex, b.ID)
			}
		}
	}
	return nil
}

// EncodeIndexJSON returns the index JSON (compact, field order, no HTML
// escaping; supersedes and packs are never null).
func EncodeIndexJSON(f *IndexFile) ([]byte, error) {
	c := *f
	if c.Supersedes == nil {
		c.Supersedes = []string{}
	}
	if c.Packs == nil {
		c.Packs = []IndexPack{}
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return encodeJSON(&c)
}

// DecodeIndexJSON parses and validates index JSON. Unknown members are
// rejected.
func DecodeIndexJSON(js []byte) (*IndexFile, error) {
	dec := json.NewDecoder(bytes.NewReader(js))
	dec.DisallowUnknownFields()
	var f IndexFile
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	if dec.More() {
		return nil, fmt.Errorf("%w: trailing data", ErrIndex)
	}
	if err := f.validate(); err != nil {
		return nil, err
	}
	return &f, nil
}

// SealIndex returns index = seal(K_meta_e, 0x01 || zstd(JSON)) with AD
// "vd1:index:<uuid>" (§7.4).
func SealIndex(rnd io.Reader, repo vdcrypt.RepoID, ek vdcrypt.EpochKeys, f *IndexFile) ([]byte, error) {
	js, err := EncodeIndexJSON(f)
	if err != nil {
		return nil, err
	}
	if len(js) > compress.MaxMetaRaw {
		return nil, fmt.Errorf("%w: %d bytes of JSON", ErrIndex, len(js))
	}
	if ek.Meta.IsZero() {
		return nil, fmt.Errorf("%w %d", ErrNoEpoch, ek.Epoch)
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeIndex, repo)
	if err != nil {
		return nil, err
	}
	return vdcrypt.Seal(rnd, ek.Meta, ek.Epoch, compress.EncodeZstd(js), ad)
}

// OpenIndex opens an index object with the K_meta of its envelope epoch and
// returns the parsed file and its JSON.
func OpenIndex(repo vdcrypt.RepoID, metaKeys map[uint32]vdcrypt.Key, sealed []byte) (*IndexFile, []byte, error) {
	epoch, err := vdcrypt.EnvelopeEpoch(sealed)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	key, ok := metaKeys[epoch]
	if !ok {
		return nil, nil, fmt.Errorf("%w %d", ErrNoEpoch, epoch)
	}
	ad, err := vdcrypt.ObjectAD(vdcrypt.PurposeIndex, repo)
	if err != nil {
		return nil, nil, err
	}
	payload, err := vdcrypt.Open(key, epoch, sealed, ad)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	if len(payload) == 0 || payload[0] != compress.CompZstd {
		return nil, nil, fmt.Errorf("%w: payload type", ErrIndex)
	}
	js, err := compress.DecodeLimit(payload, compress.MaxMetaRaw)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrIndex, err)
	}
	f, err := DecodeIndexJSON(js)
	if err != nil {
		return nil, nil, err
	}
	return f, js, nil
}

// Location is where a blob is stored.
type Location struct {
	Pack  string
	Class pack.Class
	Entry pack.Entry
}

// Index is the in-memory union of index objects: blob id → location. The
// first location added for an id wins (§7.4). Safe for concurrent use.
type Index struct {
	mu    sync.RWMutex
	blobs map[pack.BlobID]Location
	packs map[string]bool
	files map[string]bool
}

// NewIndex returns an empty index.
func NewIndex() *Index {
	return &Index{blobs: map[pack.BlobID]Location{}, packs: map[string]bool{}, files: map[string]bool{}}
}

// AddPack records the blobs of one index pack; ids already present keep
// their first location.
func (ix *Index) AddPack(p IndexPack) error {
	entries := make([]pack.Entry, 0, len(p.Blobs))
	for _, b := range p.Blobs {
		e, err := b.Entry()
		if err != nil {
			return err
		}
		entries = append(entries, e)
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.packs[p.Pack] = true
	for _, e := range entries {
		if _, ok := ix.blobs[e.ID]; !ok {
			ix.blobs[e.ID] = Location{Pack: p.Pack, Class: p.Class, Entry: e}
		}
	}
	return nil
}

// Lookup returns the location of a blob.
func (ix *Index) Lookup(id pack.BlobID) (Location, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	l, ok := ix.blobs[id]
	return l, ok
}

// Has reports whether a blob is stored (the dedup check).
func (ix *Index) Has(id pack.BlobID) bool {
	_, ok := ix.Lookup(id)
	return ok
}

// HasPack reports whether a pack is referenced by the index.
func (ix *Index) HasPack(oid string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.packs[oid]
}

// Len returns the number of distinct blobs.
func (ix *Index) Len() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return len(ix.blobs)
}

// Files returns the oids of the loaded index objects.
func (ix *Index) Files() []string {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return sortedKeys(ix.files)
}

func (ix *Index) loaded(oid string) bool {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.files[oid]
}

func (ix *Index) markLoaded(oid string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.files[oid] = true
}

// Index returns the in-memory index.
func (r *Repo) Index() *Index { return r.index }

// HasBlob is the dedup lookup: whether the blob is in a saved pack.
func (r *Repo) HasBlob(id pack.BlobID) bool { return r.index.Has(id) }

// LoadIndex reads every index object not loaded yet, in oid order, and
// adds it to the in-memory index.
func (r *Repo) LoadIndex(ctx context.Context) error {
	oids, err := listOIDs(ctx, r.be, KindIndex)
	if err != nil {
		return err
	}
	mk := r.metaKeys()
	for _, oid := range oids {
		if r.index.loaded(oid) {
			continue
		}
		b, err := getObject(ctx, r.be, KindIndex, oid)
		if err != nil {
			return err
		}
		f, _, err := OpenIndex(r.id, mk, b)
		if err != nil {
			return fmt.Errorf("repo: index %s: %w", oid, err)
		}
		for _, p := range f.Packs {
			if err := r.index.AddPack(p); err != nil {
				return err
			}
		}
		r.index.markLoaded(oid)
	}
	return nil
}

// NewPackWriter returns a pack writer for class under the current epoch,
// with the config pack sizes.
func (r *Repo) NewPackWriter(c pack.Class) (*pack.Writer, error) {
	ek, err := r.writable()
	if err != nil {
		return nil, err
	}
	cfg := r.Config()
	return pack.NewWriter(r.id, r.keys.ID, ek, c, pack.WriterOptions{Rand: r.opts.Rand, Target: int(cfg.PackTarget), Max: int(cfg.PackMax)})
}

// SavePack stores a finished pack at data/<oid> and adds it to the
// in-memory index and to the pending index records. FlushIndex (or
// SaveSnapshot) writes them (§11: data packs → index → snapshot).
func (r *Repo) SavePack(ctx context.Context, p *pack.Pack) error {
	if _, err := r.writable(); err != nil {
		return err
	}
	if vdcrypt.OID(p.Bytes) != p.OID {
		return fmt.Errorf("%w: pack oid", ErrInvalidObject)
	}
	if err := r.be.Put(ctx, KindData, p.OID, p.Bytes); err != nil {
		return fmt.Errorf("repo: put pack %s: %w", p.OID, err)
	}
	ip := IndexPackOf(p)
	if err := r.index.AddPack(ip); err != nil {
		return err
	}
	r.mu.Lock()
	r.pending = append(r.pending, ip)
	r.mu.Unlock()
	return nil
}

// FlushIndex writes the pending pack records as index objects under the
// current epoch (at most MaxIndexBlobs blobs each) and returns their oids.
func (r *Repo) FlushIndex(ctx context.Context) ([]string, error) {
	ek, err := r.writable()
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	pending := r.pending
	r.pending = nil
	r.mu.Unlock()
	var oids []string
	for len(pending) > 0 {
		n, blobs := 0, 0
		for n < len(pending) && (n == 0 || blobs+len(pending[n].Blobs) <= MaxIndexBlobs) {
			blobs += len(pending[n].Blobs)
			n++
		}
		oid, err := r.writeIndex(ctx, ek, &IndexFile{V: IndexVersion, Packs: pending[:n]})
		if err != nil {
			r.mu.Lock()
			r.pending = append(pending, r.pending...)
			r.mu.Unlock()
			return oids, err
		}
		oids = append(oids, oid)
		pending = pending[n:]
	}
	return oids, nil
}

// WriteIndex writes an index object under the current epoch, for example a
// consolidated index that supersedes older ones (compaction).
func (r *Repo) WriteIndex(ctx context.Context, f *IndexFile) (string, error) {
	ek, err := r.writable()
	if err != nil {
		return "", err
	}
	return r.writeIndex(ctx, ek, f)
}

func (r *Repo) writeIndex(ctx context.Context, ek vdcrypt.EpochKeys, f *IndexFile) (string, error) {
	sealed, err := SealIndex(r.opts.Rand, r.id, ek, f)
	if err != nil {
		return "", err
	}
	oid, err := r.putObject(ctx, KindIndex, sealed)
	if err != nil {
		return "", err
	}
	r.index.markLoaded(oid)
	for _, p := range f.Packs {
		if err := r.index.AddPack(p); err != nil {
			return "", err
		}
	}
	return oid, nil
}
