package repo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// ErrRecoveryRequired is returned when a change would leave the current
// epoch without a recovery wrap (§4.3).
var ErrRecoveryRequired = errors.New("repo: the recovery recipient cannot be removed")

// RotateOptions select the recipients of the new epoch: the current
// recipients minus Exclude, plus Add.
type RotateOptions struct {
	Exclude func(*vdcrypt.KeyFile) bool
	Add     []Recipient
}

// Rotate starts epoch e+1 (§4.5): a fresh RMK, chain_{e+1} and
// metachain_{e+1}, wraps for the current recipients only, then writers
// switch. Existing data is not re-encrypted. It takes an exclusive lock.
func (r *Repo) Rotate(ctx context.Context, opts RotateOptions) (uint32, error) {
	if _, err := r.writable(); err != nil {
		return 0, err
	}
	lk, err := r.be.Lock(ctx, LockExclusive, LockTTL)
	if err != nil {
		return 0, err
	}
	defer r.be.Unlock(context.WithoutCancel(ctx), lk.ID)
	return r.rotateLocked(ctx, opts)
}

func (r *Repo) rotateLocked(ctx context.Context, opts RotateOptions) (uint32, error) {
	for _, rc := range opts.Add {
		if err := checkRecipient(rc); err != nil {
			return 0, err
		}
	}
	// Re-read key files and chains so wraps and epochs written elsewhere
	// are seen.
	kfs, err := loadKeyFiles(ctx, r.be, r.id, r.opts.Logger)
	if err != nil {
		return 0, err
	}
	_, top, err := loadChains(ctx, r.be)
	if err != nil {
		return 0, err
	}
	r.mu.Lock()
	r.keyFiles = kfs
	cur := r.secrets[r.current]
	if top > r.current {
		r.stale = true
	}
	stale := r.stale
	r.mu.Unlock()
	if stale {
		return 0, ErrStaleEpoch
	}

	recips := r.recipientsOf(cur.Epoch, opts.Exclude)
	recips = append(recips, opts.Add...)
	hasRecovery := false
	for _, rc := range recips {
		hasRecovery = hasRecovery || rc.Kind == vdcrypt.KindRecovery
	}
	if !hasRecovery {
		return 0, ErrRecoveryRequired
	}

	next, objs, err := vdcrypt.Rotate(r.opts.Rand, r.id, cur)
	if err != nil {
		return 0, err
	}
	for _, b := range [][]byte{objs.Chain, objs.MetaChain} {
		if _, err := r.putObject(ctx, KindChain, b); err != nil {
			return 0, err
		}
	}
	seen := map[string]bool{}
	for _, rc := range recips {
		k := string(rc.Kind) + string(rc.Pub[:])
		if seen[k] {
			continue
		}
		seen[k] = true
		if err := r.writeWrap(ctx, rc, next); err != nil {
			return 0, err
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.addEpoch(next); err != nil {
		return 0, err
	}
	r.current = next.Epoch
	r.opts.Logger.Info("repo: rotated epoch", "repo", r.id.String(), "epoch", next.Epoch, "recipients", len(seen))
	return next.Epoch, nil
}

// recipientsOf returns the distinct recipients wrapped for epoch e.
func (r *Repo) recipientsOf(e uint32, exclude func(*vdcrypt.KeyFile) bool) []Recipient {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Recipient
	for _, oid := range sortedKeys(r.keyFiles) {
		kf := r.keyFiles[oid]
		if kf.Epoch != e || (exclude != nil && exclude(kf)) {
			continue
		}
		out = append(out, recipientOf(kf))
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}

// Recipients returns the distinct recipients of the current epoch.
func (r *Repo) Recipients() []Recipient {
	return r.recipientsOf(r.Epoch(), nil)
}

// AddRecipient wraps the current epoch to rc (older epochs are reachable
// through the chain). Under escrow_mode=metadata an escrow recipient
// receives the meta bundle.
func (r *Repo) AddRecipient(ctx context.Context, rc Recipient) error {
	if err := checkRecipient(rc); err != nil {
		return err
	}
	if _, err := r.writable(); err != nil {
		return err
	}
	r.mu.Lock()
	cur := r.secrets[r.current]
	r.mu.Unlock()
	return r.writeWrap(ctx, rc, cur)
}

// RemoveRecipient rotates to a new epoch whose wraps exclude every key
// file match selects (device revocation, §4.5). The removed recipient can
// still read epochs it held; that is inherent.
func (r *Repo) RemoveRecipient(ctx context.Context, match func(*vdcrypt.KeyFile) bool) (uint32, error) {
	if match == nil {
		return 0, fmt.Errorf("%w: no match", ErrRecipient)
	}
	return r.Rotate(ctx, RotateOptions{Exclude: match})
}

// SetEscrowMode rewrites config with the new escrow_mode under an
// exclusive lock and starts a new epoch (§5), so the escrow recipient gets
// the bundle of the new mode.
func (r *Repo) SetEscrowMode(ctx context.Context, mode string) (uint32, error) {
	if !validEscrowMode(mode) {
		return 0, fmt.Errorf("%w: escrow_mode %q", ErrConfig, mode)
	}
	if _, err := r.writable(); err != nil {
		return 0, err
	}
	lk, err := r.be.Lock(ctx, LockExclusive, LockTTL)
	if err != nil {
		return 0, err
	}
	defer r.be.Unlock(context.WithoutCancel(ctx), lk.ID)

	r.mu.Lock()
	c := *r.cfg
	r.mu.Unlock()
	c.EscrowMode = mode
	stored, err := c.Encode(r.keys.ConfigMAC)
	if err != nil {
		return 0, err
	}
	if err := r.be.Put(ctx, KindConfig, ConfigOID, stored); err != nil {
		return 0, fmt.Errorf("repo: put config: %w", err)
	}
	r.mu.Lock()
	r.cfg, r.cfgRaw = &c, stored
	r.mu.Unlock()
	return r.rotateLocked(ctx, RotateOptions{})
}
