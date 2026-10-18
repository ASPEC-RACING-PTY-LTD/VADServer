package repo

import (
	"cmp"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

var (
	ErrExists       = errors.New("repo: repository already initialised")
	ErrNoKeyFile    = errors.New("repo: no key file opens with this credential")
	ErrMetadataOnly = errors.New("repo: metadata-only access cannot read or write data")
	ErrStaleEpoch   = errors.New("repo: credential does not hold the current epoch; writing is refused")
	ErrNoEpoch      = errors.New("repo: no key for epoch")
	ErrRecipient    = errors.New("repo: invalid recipient")
)

// LockTTL is the lifetime of the locks Init, Rotate and SetEscrowMode take.
const LockTTL = 5 * time.Minute

// Options are common Init/Open options.
type Options struct {
	Rand   io.Reader        // nil = crypto/rand
	Now    func() time.Time // nil = time.Now
	Logger *slog.Logger     // nil = slog.Default()
}

func (o *Options) fill() {
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
}

// Repo is an opened repository. Methods are safe for concurrent use.
type Repo struct {
	be   Backend
	opts Options
	id   vdcrypt.RepoID

	mu       sync.Mutex
	cfg      *Config
	cfgRaw   []byte
	metaOnly bool
	dk       vdcrypt.Key
	keys     vdcrypt.RepoKeys
	secrets  map[uint32]vdcrypt.EpochSecret // full access only
	epochs   map[uint32]vdcrypt.EpochKeys   // metadata-only: Meta set only
	current  uint32
	stale    bool
	keyFiles map[string]*vdcrypt.KeyFile // by oid

	index   *Index
	pending []IndexPack // packs saved but not yet in a written index
}

// ID returns the repository id.
func (r *Repo) ID() vdcrypt.RepoID { return r.id }

// Backend returns the backend.
func (r *Repo) Backend() Backend { return r.be }

// Config returns a copy of the config.
func (r *Repo) Config() Config {
	r.mu.Lock()
	defer r.mu.Unlock()
	c := *r.cfg
	c.RecoveryPub = append([]byte(nil), c.RecoveryPub...)
	return c
}

// MetadataOnly reports whether only K_meta keys are held (metadata escrow).
func (r *Repo) MetadataOnly() bool { return r.metaOnly }

// Epoch returns the epoch new objects are written under.
func (r *Repo) Epoch() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.current
}

// Stale reports whether a newer epoch exists than the credential holds
// (for example after the recipient was removed). A stale Repo reads but
// refuses to write.
func (r *Repo) Stale() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stale
}

// Keys returns the DK-derived keys (K_id, K_chunker, K_fh, K_mac).
func (r *Repo) Keys() (vdcrypt.RepoKeys, error) {
	if r.metaOnly {
		return vdcrypt.RepoKeys{}, ErrMetadataOnly
	}
	return r.keys, nil
}

// EpochKeys returns the keys of epoch e. Metadata-only holders get Meta
// only (Data and Chain are zero).
func (r *Repo) EpochKeys(e uint32) (vdcrypt.EpochKeys, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	ek, ok := r.epochs[e]
	if !ok {
		return vdcrypt.EpochKeys{}, fmt.Errorf("%w %d", ErrNoEpoch, e)
	}
	return ek, nil
}

// Keyring returns the pack keyring over every resolved epoch.
func (r *Repo) Keyring() *pack.Keyring {
	return &pack.Keyring{Repo: r.id, ID: r.keys.ID, Epochs: r.EpochKeys}
}

// metaKeys returns K_meta of every resolved epoch.
func (r *Repo) metaKeys() map[uint32]vdcrypt.Key {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uint32]vdcrypt.Key, len(r.epochs))
	for e, ek := range r.epochs {
		out[e] = ek.Meta
	}
	return out
}

// writable returns the current epoch keys when this Repo may write.
func (r *Repo) writable() (vdcrypt.EpochKeys, error) {
	if r.metaOnly {
		return vdcrypt.EpochKeys{}, ErrMetadataOnly
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stale {
		return vdcrypt.EpochKeys{}, ErrStaleEpoch
	}
	return r.epochs[r.current], nil
}

// Close wipes the key material held in memory (best effort).
func (r *Repo) Close() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dk.Wipe()
	r.keys.Wipe()
	for e, s := range r.secrets {
		s.Wipe()
		delete(r.secrets, e)
	}
	for e, k := range r.epochs {
		k.Wipe()
		delete(r.epochs, e)
	}
}

// putObject stores data under its oid.
func (r *Repo) putObject(ctx context.Context, kind Kind, data []byte) (string, error) {
	oid := vdcrypt.OID(data)
	if err := r.be.Put(ctx, kind, oid, data); err != nil {
		return "", fmt.Errorf("repo: put %s/%s: %w", kind, oid, err)
	}
	return oid, nil
}

// getObject reads a content-addressed object and checks its hash.
func getObject(ctx context.Context, be Backend, kind Kind, oid string) ([]byte, error) {
	b, err := be.Get(ctx, kind, oid)
	if err != nil {
		return nil, fmt.Errorf("repo: get %s/%s: %w", kind, oid, err)
	}
	if err := CheckObject(kind, oid, b); err != nil {
		return nil, err
	}
	return b, nil
}

// listOIDs returns the sorted oids of kind.
func listOIDs(ctx context.Context, be Backend, kind Kind) ([]string, error) {
	infos, err := be.List(ctx, kind)
	if err != nil {
		return nil, fmt.Errorf("repo: list %s: %w", kind, err)
	}
	out := make([]string, 0, len(infos))
	for _, i := range infos {
		out = append(out, i.OID)
	}
	slices.Sort(out)
	return out, nil
}

// loadKeyFiles reads every key file of the repository. Invalid or foreign
// key files are skipped with a warning.
func loadKeyFiles(ctx context.Context, be Backend, repo vdcrypt.RepoID, log *slog.Logger) (map[string]*vdcrypt.KeyFile, error) {
	oids, err := listOIDs(ctx, be, KindKeys)
	if err != nil {
		return nil, err
	}
	out := make(map[string]*vdcrypt.KeyFile, len(oids))
	for _, oid := range oids {
		b, err := getObject(ctx, be, KindKeys, oid)
		if err != nil {
			return nil, err
		}
		kf, err := vdcrypt.DecodeKeyFile(b)
		if err != nil || kf.Repo != repo.String() {
			log.Warn("repo: skipping invalid key file", "oid", oid, "err", err)
			continue
		}
		out[oid] = kf
	}
	return out, nil
}

// loadChains reads every chain/ object and returns them with the highest
// envelope epoch.
func loadChains(ctx context.Context, be Backend) ([][]byte, uint32, error) {
	oids, err := listOIDs(ctx, be, KindChain)
	if err != nil {
		return nil, 0, err
	}
	var out [][]byte
	var top uint32
	for _, oid := range oids {
		b, err := getObject(ctx, be, KindChain, oid)
		if err != nil {
			return nil, 0, err
		}
		if e, err := vdcrypt.EnvelopeEpoch(b); err == nil {
			top = max(top, e)
		}
		out = append(out, b)
	}
	return out, top, nil
}

// InitOptions configure a new repository.
type InitOptions struct {
	Options
	// RepoID is the repository uuid; zero = a fresh random UUIDv4.
	RepoID vdcrypt.RepoID
	// EscrowMode is "full" (default) or "metadata".
	EscrowMode string
	// Recipients receive epoch 1 wraps: devices, the escrow SEK and an
	// optional passphrase. The recovery recipient is always added.
	Recipients []Recipient
}

func newRepoID(rnd io.Reader) (vdcrypt.RepoID, error) {
	var id vdcrypt.RepoID
	if _, err := io.ReadFull(rnd, id[:]); err != nil {
		return id, err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return id, nil
}

func randOr(r io.Reader) io.Reader {
	if r == nil {
		return rand.Reader
	}
	return r
}

// Init creates a repository (§4.7 step 3): DK, RMK_1 and the recovery
// secret; chain_1 and metachain_1; key files for the recipients and the
// recovery recipient; then config with its mac. It returns the opened
// repository and the VDRK1- recovery key text, which must be shown to the
// user once.
func Init(ctx context.Context, be Backend, opts InitOptions) (*Repo, string, error) {
	opts.fill()
	opts.Rand = randOr(opts.Rand)
	if opts.EscrowMode == "" {
		opts.EscrowMode = EscrowFull
	}
	if !validEscrowMode(opts.EscrowMode) {
		return nil, "", fmt.Errorf("%w: escrow_mode %q", ErrConfig, opts.EscrowMode)
	}
	for _, rc := range opts.Recipients {
		if err := checkRecipient(rc); err != nil {
			return nil, "", err
		}
	}
	id := opts.RepoID
	if id == (vdcrypt.RepoID{}) {
		var err error
		if id, err = newRepoID(opts.Rand); err != nil {
			return nil, "", err
		}
	}

	lk, err := be.Lock(ctx, LockExclusive, LockTTL)
	if err != nil {
		return nil, "", err
	}
	defer be.Unlock(context.WithoutCancel(ctx), lk.ID)
	if _, err := be.Stat(ctx, KindConfig, ConfigOID); err == nil {
		return nil, "", ErrExists
	} else if !errors.Is(err, ErrNotFound) {
		return nil, "", err
	}

	dk, err := vdcrypt.RandomKey(opts.Rand)
	if err != nil {
		return nil, "", err
	}
	secret, err := vdcrypt.NewRecoverySecret(opts.Rand)
	if err != nil {
		return nil, "", err
	}
	defer secret.Wipe()
	recPriv, recPub, err := vdcrypt.RecoveryKeyPair(id, secret)
	if err != nil {
		return nil, "", err
	}
	recPriv.Wipe()
	s1, objs, err := vdcrypt.InitEpoch(opts.Rand, id)
	if err != nil {
		return nil, "", err
	}
	r := &Repo{be: be, opts: opts.Options, id: id, dk: dk, keys: vdcrypt.DeriveRepoKeys(id, dk),
		cfg:     NewConfig(id, opts.Now().UnixMilli(), opts.EscrowMode, recPub),
		secrets: map[uint32]vdcrypt.EpochSecret{}, epochs: map[uint32]vdcrypt.EpochKeys{},
		keyFiles: map[string]*vdcrypt.KeyFile{}, index: NewIndex()}
	if err := r.addEpoch(s1); err != nil {
		return nil, "", err
	}
	r.current = 1

	// Chain objects and wraps first, config last: a repository without
	// config is not initialised and can be retried.
	for _, b := range [][]byte{objs.Chain, objs.MetaChain} {
		if _, err := r.putObject(ctx, KindChain, b); err != nil {
			return nil, "", err
		}
	}
	recips := append([]Recipient{{Kind: vdcrypt.KindRecovery, Pub: recPub, Label: "recovery"}}, opts.Recipients...)
	for _, rc := range recips {
		if err := r.writeWrap(ctx, rc, s1); err != nil {
			return nil, "", err
		}
	}
	stored, err := r.cfg.Encode(r.keys.ConfigMAC)
	if err != nil {
		return nil, "", err
	}
	if err := be.Put(ctx, KindConfig, ConfigOID, stored); err != nil {
		return nil, "", fmt.Errorf("repo: put config: %w", err)
	}
	r.cfgRaw = stored
	return r, vdcrypt.EncodeRecoveryKey(secret), nil
}

func checkRecipient(rc Recipient) error {
	ok := rc.Kind.Valid() && !rc.Pub.IsZero() &&
		(rc.KDF != nil) == (rc.Kind == vdcrypt.KindPassphrase) &&
		(rc.Device != nil) == (rc.Kind == vdcrypt.KindDevice) &&
		(rc.Server != nil) == (rc.Kind == vdcrypt.KindEscrow)
	if !ok {
		return fmt.Errorf("%w: kind %q", ErrRecipient, rc.Kind)
	}
	return nil
}

// addEpoch records a full epoch secret.
func (r *Repo) addEpoch(s vdcrypt.EpochSecret) error {
	ek, err := s.Keys(r.id)
	if err != nil {
		return err
	}
	r.secrets[s.Epoch] = s
	r.epochs[s.Epoch] = ek
	return nil
}

// bundleFor returns the bundle a recipient receives for epoch s: escrow
// under escrow_mode=metadata gets K_meta only (§4.3).
func (r *Repo) bundleFor(kind vdcrypt.Kind, s vdcrypt.EpochSecret) (vdcrypt.Bundle, error) {
	r.mu.Lock()
	mode := r.cfg.EscrowMode
	r.mu.Unlock()
	if kind == vdcrypt.KindEscrow && mode == EscrowMetadata {
		ek, err := s.Keys(r.id)
		if err != nil {
			return vdcrypt.Bundle{}, err
		}
		b := vdcrypt.MetaBundle(ek)
		ek.Wipe()
		return b, nil
	}
	return vdcrypt.FullBundle(s, r.dk), nil
}

// writeWrap writes a key file wrapping epoch s to rc.
func (r *Repo) writeWrap(ctx context.Context, rc Recipient, s vdcrypt.EpochSecret) error {
	b, err := r.bundleFor(rc.Kind, s)
	if err != nil {
		return err
	}
	defer b.Wipe()
	kf, err := vdcrypt.NewKeyFile(r.opts.Rand, vdcrypt.KeyFileParams{
		Repo: r.id, Kind: rc.Kind, Created: r.opts.Now().UnixMilli(), Label: rc.Label,
		RecipientPub: rc.Pub, KDF: rc.KDF, Device: rc.Device, Server: rc.Server,
	}, b)
	if err != nil {
		return err
	}
	data, err := kf.Encode()
	if err != nil {
		return err
	}
	oid, err := r.putObject(ctx, KindKeys, data)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.keyFiles[oid] = kf
	r.mu.Unlock()
	return nil
}

// Open opens an existing repository with cred (§5 reader steps): it
// rejects an unknown format or newer version, unwraps the newest key file
// the credential opens, resolves every earlier epoch through the chain and
// verifies the config mac. An escrow meta bundle gives a metadata-only
// Repo that walks the meta-chain and cannot verify the mac (§5). The
// index is loaded before Open returns.
func Open(ctx context.Context, be Backend, cred Credential, opts Options) (*Repo, error) {
	opts.fill()
	opts.Rand = randOr(opts.Rand)
	cfgRaw, err := be.Get(ctx, KindConfig, ConfigOID)
	if err != nil {
		return nil, fmt.Errorf("repo: get config: %w", err)
	}
	cfg, err := ParseConfig(cfgRaw)
	if err != nil {
		return nil, err
	}
	id := cfg.RepoID()
	r := &Repo{be: be, opts: opts, id: id, cfg: cfg, cfgRaw: cfgRaw,
		secrets: map[uint32]vdcrypt.EpochSecret{}, epochs: map[uint32]vdcrypt.EpochKeys{}, index: NewIndex()}
	if r.keyFiles, err = loadKeyFiles(ctx, be, id, opts.Logger); err != nil {
		return nil, err
	}
	chains, top, err := loadChains(ctx, be)
	if err != nil {
		return nil, err
	}
	b, err := r.unwrap(cred)
	if err != nil {
		return nil, err
	}
	defer b.Wipe()

	if b.Meta {
		mk, err := vdcrypt.WalkMeta(id, b.Epoch, b.KMeta, chains)
		if err != nil {
			return nil, fmt.Errorf("repo: meta-chain: %w", err)
		}
		for e, k := range mk {
			r.epochs[e] = vdcrypt.EpochKeys{Epoch: e, Meta: k}
		}
		r.metaOnly = true
		opts.Logger.Debug("repo: metadata-only open; config mac not verifiable", "repo", id.String())
	} else {
		secrets, err := vdcrypt.ResolveEpochs(id, b.Secret(), chains)
		if err != nil {
			return nil, fmt.Errorf("repo: chain: %w", err)
		}
		for _, s := range secrets {
			if err := r.addEpoch(s); err != nil {
				return nil, err
			}
		}
		r.dk = b.DK
		r.keys = vdcrypt.DeriveRepoKeys(id, b.DK)
		if err := VerifyConfig(r.keys.ConfigMAC, cfgRaw); err != nil {
			r.Close()
			return nil, err
		}
	}
	r.current = b.Epoch
	r.stale = top > b.Epoch
	if err := r.LoadIndex(ctx); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// unwrap opens the newest key file the credential can open.
func (r *Repo) unwrap(cred Credential) (vdcrypt.Bundle, error) {
	priv, hasPriv := cred.private(r.id)
	if cred.kind == vdcrypt.KindRecovery {
		defer priv.Wipe()
	}
	var pub vdcrypt.Key
	if hasPriv {
		var err error
		if pub, err = vdcrypt.X25519Public(priv); err != nil {
			return vdcrypt.Bundle{}, err
		}
	}
	var cands []*vdcrypt.KeyFile
	for _, kf := range r.keyFiles {
		if kf.Kind != cred.kind {
			continue
		}
		if hasPriv {
			if kp, _ := vdcrypt.KeyFromBytes(kf.RecipientPub); !kp.Equal(pub) {
				continue
			}
		}
		cands = append(cands, kf)
	}
	slices.SortFunc(cands, func(a, b *vdcrypt.KeyFile) int {
		return cmp.Or(cmp.Compare(b.Epoch, a.Epoch), cmp.Compare(a.KeyID, b.KeyID))
	})
	var lastErr error = ErrNoKeyFile
	tried := map[string]bool{} // passphrase: a failed KDF result per recipient
	for _, kf := range cands {
		var b vdcrypt.Bundle
		var err error
		if hasPriv {
			b, err = kf.Open(priv)
		} else {
			pk := string(kf.RecipientPub)
			if tried[pk] {
				continue
			}
			b, err = kf.OpenPassphrase(cred.passphrase)
			if errors.Is(err, vdcrypt.ErrWrongRecipient) {
				tried[pk] = true
			}
		}
		if err == nil {
			return b, nil
		}
		lastErr = fmt.Errorf("%w: %w", ErrNoKeyFile, err)
	}
	return vdcrypt.Bundle{}, lastErr
}

// KeyFiles returns the loaded key files by oid.
func (r *Repo) KeyFiles() map[string]*vdcrypt.KeyFile {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]*vdcrypt.KeyFile, len(r.keyFiles))
	for k, v := range r.keyFiles {
		out[k] = v
	}
	return out
}

// RecoveryReady reports whether the current epoch has a recovery wrap
// (§4.3; readiness check recovery_key_confirmed needs this).
func (r *Repo) RecoveryReady() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, kf := range r.keyFiles {
		if kf.Kind == vdcrypt.KindRecovery && kf.Epoch == r.current {
			return true
		}
	}
	return false
}
