// Package app builds a VaultDock server from configuration. `vaultdock serve`
// and the in-process test server both use it, so they cannot drift (B13).
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/backup"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/storage"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

// planRetention is how long finished or expired plans are kept.
const planRetention = 7 * 24 * time.Hour

// DBPath is the metadata database path inside the state directory.
func DBPath(cfg *config.Config) string { return filepath.Join(cfg.StateDir, "vaultdock.db") }

// App is a fully constructed server with its dependencies.
type App struct {
	Cfg      *config.Config
	Store    *store.Store
	Blobs    *storage.Manager
	Settings *api.SettingsCache
	Server   *api.Server
}

// New prepares the state directory, opens and migrates the database, makes
// sure a default storage location exists, loads settings and storage, and
// builds the API server after crash recovery. Close releases it.
func New(ctx context.Context, cfg *config.Config) (*App, error) {
	if err := os.MkdirAll(cfg.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	st, err := store.Open(ctx, DBPath(cfg))
	if err != nil {
		return nil, err
	}
	a, err := build(ctx, cfg, st)
	if err != nil {
		st.Close()
		return nil, err
	}
	return a, nil
}

func build(ctx context.Context, cfg *config.Config, st *store.Store) (*App, error) {
	if err := ensureDefaultLocation(ctx, cfg, st); err != nil {
		return nil, err
	}
	settings, err := api.LoadSettings(ctx, st)
	if err != nil {
		return nil, err
	}
	geo, err := api.OpenGeo(cfg.GeoIPDB)
	if err != nil {
		slog.Warn("GeoIP database could not be opened; continuing without it", "err", err)
		geo, _ = api.OpenGeo("")
	}
	blobs := storage.New(st)
	blobs.SetGlobalMinFree(settings.Get().MinFreeBytes)
	if err := blobs.Reload(ctx); err != nil {
		return nil, err
	}
	locs, err := st.ListLocations(ctx)
	if err != nil {
		return nil, err
	}
	for _, l := range locs {
		if err := storage.LocationHealthy(l.Path); err != nil {
			slog.Error("storage location unavailable; uploads to it will be refused until it is mounted", "name", l.Name, "path", l.Path)
		}
	}
	if n, err := st.FailInterruptedOperations(ctx); err != nil {
		return nil, err
	} else if n > 0 {
		slog.Warn("marked operations interrupted by a restart as failed", "count", n)
	}
	km, err := keys.Load(ctx, st, cfg)
	if err != nil {
		return nil, err
	}
	srv := api.New(cfg, st, blobs, settings, geo)
	srv.SetKeys(km)
	srv.RecoverAfterRestart(ctx)
	return &App{Cfg: cfg, Store: st, Blobs: blobs, Settings: settings, Server: srv}, nil
}

// Run performs the background work of a serving process (update checks and
// periodic maintenance) until ctx ends.
func (a *App) Run(ctx context.Context) {
	done := make(chan struct{})
	go func() { a.Server.RunUpdateChecks(ctx); close(done) }()
	go backup.NewLocks(a.Store).RunSweeper(ctx, time.Minute)
	go a.Server.RunCatalog(ctx) // catalog ingestion worker (P1-16)
	// Extracted copies of compressed stored files live 10 minutes past
	// their last use.
	go a.Blobs.RunExtractSweeper(ctx, time.Minute, storage.ExtractIdle)
	// File copies (copy rules, storage nodes) and the read fallback.
	go a.Server.RunCopies(ctx)
	// Video thumbnails (FFmpeg), and exports a restart interrupted.
	go a.Server.RunMedia(ctx)
	t := time.NewTicker(10 * time.Minute)
	defer t.Stop()
	n := 0
	a.maintain(ctx, false)
	for {
		select {
		case <-ctx.Done():
			<-done
			return
		case <-t.C:
			n++
			a.maintain(ctx, n%6 == 0)
		}
	}
}

func (a *App) maintain(ctx context.Context, orphans bool) {
	a.Server.RunMaintenance(ctx, orphans)
	a.Server.ExpireApprovals(ctx)
	a.Server.ExpireTaskLeases(ctx)
	go a.Server.MaybeCompressStorage(ctx) // incremental; skips if a pass is running
	if _, err := a.Store.ExpirePlans(ctx, planRetention); err != nil && ctx.Err() == nil {
		slog.Warn("plan expiry failed", "err", err)
	}
}

// Close stops running operations (waiting up to 20 s) and closes the
// database. Stop Run and the HTTP server first.
func (a *App) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	a.Server.StopOperations(ctx)
	return a.Store.Close()
}

func ensureDefaultLocation(ctx context.Context, cfg *config.Config, st *store.Store) error {
	if _, err := st.DefaultLocation(ctx); err == nil {
		return nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	p := filepath.Clean(cfg.DefaultStorage)
	if err := os.MkdirAll(p, 0o750); err != nil {
		return fmt.Errorf("default storage %s: %w", p, err)
	}
	if err := storage.PrepareLocation(p); err != nil {
		return err
	}
	l := &store.Location{Name: "Default", Path: p, Enabled: true, IsDefault: true}
	if err := st.CreateLocation(ctx, l); err != nil {
		return err
	}
	slog.Info("created default storage location", "path", p)
	return nil
}
