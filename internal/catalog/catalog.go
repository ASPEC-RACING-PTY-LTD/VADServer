// Package catalog builds and serves the encrypted snapshot catalog
// (api-v1 §5.2 step 3, §5.4; MASTER_PLAN D9, C2, C3).
//
// Owner: SRV-C (P1-16).
//
//   - Ingestion runs after a snapshot commit, on one background worker. It
//     unwraps the repository's newest escrow key file (keys.OpenKeyFile,
//     audited with purpose catalog), walks the meta-chain to K_meta of every
//     epoch, opens the snapshot root and walks its tree blobs. Only K_meta
//     is used, so a metadata-escrow repository is browsable too
//     (backup-format-v1 §1.2); file contents are never read.
//   - Every catalog row stores its path and name encrypted with keys derived
//     from a per-user DEK (catalog_keys). The DEK is wrapped by a key derived
//     from the server escrow key (SEK), which is sealed by the KEK; kek_id
//     records the SEK version.
//   - Browsing uses an HMAC blind index of the full path and of the parent
//     path. Search uses HMAC blind tokens of lowercased name words and their
//     prefixes (at least 2 runes): whole-word and prefix search only.
//   - Ingestion is idempotent and resumable at snapshot granularity: a
//     snapshot's rows are cleared before it is (re)ingested, and pending
//     snapshots are picked up again at startup and on a periodic scan.
//   - Without key custody or without an escrow key file the snapshot is
//     skipped: ingest_state failed, ingest_error "escrow_unavailable: …".
package catalog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// Ingestion limits.
const (
	maxEntries  = 5_000_000 // per snapshot
	maxDepth    = 512
	batchSize   = 1000
	queueSize   = 1024
	rescanEvery = time.Minute
)

// SkipPrefix starts ingest_error when a snapshot was skipped because the
// server cannot read the repository metadata.
const SkipPrefix = wire.CodeEscrowUnavailable + ": "

var (
	// ErrNoCatalogKey means the user has no catalog DEK yet.
	ErrNoCatalogKey = errors.New("catalog: no catalog key for the user")
	// ErrPathNotFound means a tree path is not in the snapshot catalog.
	ErrPathNotFound = errors.New("catalog: path not found")
	// ErrNotDirectory means a tree path names a file.
	ErrNotDirectory = errors.New("catalog: path is not a directory")

	errNoEscrow = errors.New("no escrow key file the server can open")
)

// Config wires a Service. Keys is called on every use, so custody attached
// after construction is picked up; it may return nil (no KEK). Objects
// opens the object store behind a repository's destination.
type Config struct {
	Store   *store.Store
	Keys    func() *keys.Manager
	Objects func(ctx context.Context, rp *store.Repository) (*backup.Store, error)
	Logger  *slog.Logger
}

// Service ingests and serves the catalog. Methods are safe for concurrent
// use; ingestion is serialised.
type Service struct {
	st      *store.Store
	keys    func() *keys.Manager
	objects func(ctx context.Context, rp *store.Repository) (*backup.Store, error)
	log     *slog.Logger
	queue   chan int64

	ingestMu sync.Mutex

	wrapMu   sync.Mutex
	wrapKeys map[uint32]vdcrypt.Key
}

// New returns a Service. Nothing runs until Run.
func New(cfg Config) *Service {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Keys == nil {
		cfg.Keys = func() *keys.Manager { return nil }
	}
	return &Service{st: cfg.Store, keys: cfg.Keys, objects: cfg.Objects, log: cfg.Logger,
		queue: make(chan int64, queueSize), wrapKeys: map[uint32]vdcrypt.Key{}}
}

// Enqueue asks the worker to ingest a committed snapshot. It never blocks;
// when the queue is full the periodic scan picks the snapshot up.
func (s *Service) Enqueue(snapshotID int64) {
	select {
	case s.queue <- snapshotID:
	default:
	}
}

// Run is the ingestion worker: it ingests every pending snapshot (resume
// after a restart), then queued snapshots, rescanning periodically, until
// ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(rescanEvery)
	defer t.Stop()
	s.scan(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case id := <-s.queue:
			s.ingestLogged(ctx, id)
		case <-t.C:
			s.scan(ctx)
		}
	}
}

func (s *Service) scan(ctx context.Context) {
	for ctx.Err() == nil {
		ids, err := s.st.PendingIngestSnapshots(ctx, wire.ListMaxLimit)
		if err != nil {
			if ctx.Err() == nil {
				s.log.Warn("catalog: listing pending snapshots failed", "err", err)
			}
			return
		}
		progressed := false
		for _, id := range ids {
			if ctx.Err() != nil {
				return
			}
			if s.ingestLogged(ctx, id) == nil {
				progressed = true
			}
		}
		if len(ids) < wire.ListMaxLimit || !progressed {
			return
		}
	}
}

func (s *Service) ingestLogged(ctx context.Context, id int64) error {
	err := s.Ingest(ctx, id)
	if err != nil && ctx.Err() == nil {
		s.log.Warn("catalog: ingestion postponed", "snapshot", id, "err", err)
	}
	return err
}

// Ingest builds the catalog of one snapshot when it is pending. A snapshot
// already done or failed is left alone. Permanent problems are recorded on
// the snapshot (ingest_state failed) and return nil; an error return means
// the snapshot stays pending (for example storage unavailable) and is
// retried by the next scan.
func (s *Service) Ingest(ctx context.Context, snapshotID int64) error {
	s.ingestMu.Lock()
	defer s.ingestMu.Unlock()
	sn, err := s.st.GetSnapshot(ctx, snapshotID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if sn.IngestState != wire.SnapshotIngestPending {
		return nil
	}
	rp, err := s.st.Repository(ctx, sn.RepoID)
	if err != nil {
		return err
	}
	fail := func(msg string) error {
		s.log.Info("catalog: snapshot not ingested", "snapshot", sn.ID, "repo", rp.ID, "reason", msg)
		return s.st.SetSnapshotIngest(ctx, sn.ID, wire.SnapshotIngestFailed, msg)
	}
	m := s.keys()
	if m == nil {
		return fail(SkipPrefix + "server key custody is not configured")
	}
	if s.objects == nil {
		return errors.New("catalog: no object store")
	}
	bs, err := s.objects(ctx, rp)
	if err != nil {
		return err
	}
	r, err := openMetaReader(ctx, s.st, bs, m, rp, sn.ID)
	switch {
	case errors.Is(err, errNoEscrow):
		return fail(SkipPrefix + err.Error())
	case errors.Is(err, keys.ErrUnavailable):
		return fail(SkipPrefix + "server key custody is not configured")
	case err != nil:
		return fail("metadata: " + err.Error())
	}
	defer r.wipe()
	uk, err := s.userKeysFor(ctx, rp.UserID, true)
	if err != nil {
		return err
	}
	defer uk.wipe()
	if err := s.st.ClearCatalogFiles(ctx, sn.ID); err != nil {
		return err
	}
	n, err := s.walk(ctx, r, uk, sn)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		_ = s.st.ClearCatalogFiles(ctx, sn.ID)
		return fail("walk: " + err.Error())
	}
	if err := s.st.SetSnapshotIngest(ctx, sn.ID, wire.SnapshotIngestDone, ""); err != nil {
		return err
	}
	if err := s.st.TouchCatalogSync(ctx, rp.ID, store.Now()); err != nil {
		return err
	}
	s.log.Info("catalog: snapshot ingested", "snapshot", sn.ID, "repo", rp.ID, "entries", n)
	return nil
}

// catalogType maps a tree entry type to the catalog type.
func catalogType(t string) string {
	switch t {
	case snapshot.TypeDir:
		return wire.CatalogEntryDir
	case snapshot.TypeFile:
		return wire.CatalogEntryFile
	}
	return "symlink" // symlinks and other reparse points
}

// lastComponent is the display name of a root token path.
func lastComponent(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[i+1:]
	}
	return p
}

type pendingDir struct {
	path  string
	tree  string
	depth int
}

// walk stores every root and tree entry of the snapshot and returns the
// number of rows written.
func (s *Service) walk(ctx context.Context, r *metaReader, uk *userKeys, sn *store.Snapshot) (int, error) {
	root, err := r.root(ctx, sn.SnapshotOID)
	if err != nil {
		return 0, err
	}
	var batch []store.CatalogFile
	n := 0
	emit := func(path, parent, name, typ string, size, mtime int64, mode uint32) error {
		if n++; n > maxEntries {
			return fmt.Errorf("more than %d entries", maxEntries)
		}
		pct, err := uk.sealField(sn.ID, "path", path)
		if err != nil {
			return err
		}
		nct, err := uk.sealField(sn.ID, "name", name)
		if err != nil {
			return err
		}
		batch = append(batch, store.CatalogFile{PathBI: uk.pathBI(path), ParentBI: uk.pathBI(parent), PathCT: pct, NameCT: nct,
			Tokens: uk.nameTokens(name), Type: typ, Size: size, MTime: mtime, Mode: int64(mode)})
		if len(batch) >= batchSize {
			if err := s.st.InsertCatalogFiles(ctx, sn.ID, batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
		return nil
	}

	var stack []pendingDir
	for _, rr := range root.Roots {
		if _, _, err := snapshot.SplitTokenPath(rr.TokenPath); err != nil {
			return n, fmt.Errorf("bad root token path %q", rr.TokenPath)
		}
		if err := emit(rr.TokenPath, "", lastComponent(rr.TokenPath), wire.CatalogEntryDir, 0, 0, 0); err != nil {
			return n, err
		}
		stack = append(stack, pendingDir{path: rr.TokenPath, tree: rr.Tree})
	}
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return n, err
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if d.depth > maxDepth {
			return n, fmt.Errorf("tree deeper than %d levels", maxDepth)
		}
		seen := map[string]bool{}
		for id := d.tree; id != ""; {
			if seen[id] {
				return n, fmt.Errorf("continuation loop at tree %s", id)
			}
			seen[id] = true
			t, err := r.tree(ctx, id)
			if err != nil {
				return n, err
			}
			for _, e := range t.Entries {
				p := d.path + "/" + e.N
				typ := catalogType(e.T)
				size := e.S
				if typ == wire.CatalogEntryDir {
					size = 0
				}
				if err := emit(p, d.path, e.N, typ, size, e.M/int64(time.Millisecond), e.A); err != nil {
					return n, err
				}
				if e.T == snapshot.TypeDir && e.Sub != nil {
					stack = append(stack, pendingDir{path: p, tree: *e.Sub, depth: d.depth + 1})
				}
			}
			id = ""
			if t.Cont != nil {
				id = *t.Cont
			}
		}
	}
	if len(batch) > 0 {
		if err := s.st.InsertCatalogFiles(ctx, sn.ID, batch); err != nil {
			return n, err
		}
	}
	return n, nil
}
