package keys

// Server-side re-wraps (backup-format-v1 §4.6 (b) and (d), owner SRV-K,
// P1-13): a full-escrow bundle re-wrapped to a newly approved device, and an
// escrow wrap re-wrapped under the current SEK after a SEK rotation. Bundles
// exist only in memory and are wiped on return.

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// ErrMetaBundle is returned when a full bundle is needed but the escrow key
// file holds a meta bundle (metadata escrow: the server cannot share keys).
var ErrMetaBundle = errors.New("keys: escrow key file holds a meta bundle")

// DeviceTarget is the device a bundle is re-wrapped to.
type DeviceTarget struct {
	ID      int64
	BoxPub  vdcrypt.Key       // X25519 recipient
	SignPub ed25519.PublicKey // the device's VD1 key (key file "device" member)
	Label   string
}

// RewrapToDevice opens a full-escrow key file (audited, purpose rewrap is
// the caller's choice) and wraps the same epoch's full bundle to the device.
// It returns the new device key file.
func (m *Manager) RewrapToDevice(ctx context.Context, kf *vdcrypt.KeyFile, dev DeviceTarget, a Audit) (*vdcrypt.KeyFile, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	if dev.ID <= 0 || dev.BoxPub.IsZero() || len(dev.SignPub) != ed25519.PublicKeySize {
		return nil, vdcrypt.ErrKeyFile
	}
	repo, err := vdcrypt.ParseRepoID(kf.Repo)
	if err != nil {
		return nil, err
	}
	b, err := m.OpenKeyFile(ctx, kf, a)
	if err != nil {
		return nil, err
	}
	defer b.Wipe()
	if b.Meta {
		return nil, ErrMetaBundle
	}
	return vdcrypt.NewKeyFile(rand.Reader, vdcrypt.KeyFileParams{
		Repo: repo, Kind: vdcrypt.KindDevice, Created: time.Now().UnixMilli(), Label: dev.Label,
		RecipientPub: dev.BoxPub,
		Device:       &vdcrypt.KeyFileDevice{ID: dev.ID, SignPub: append([]byte(nil), dev.SignPub...)},
	}, b)
}

// RewrapEscrow re-wraps an escrow key file (full or meta bundle) under the
// current SEK. It returns nil when the key file already names the current
// SEK version.
func (m *Manager) RewrapEscrow(ctx context.Context, kf *vdcrypt.KeyFile, a Audit) (*vdcrypt.KeyFile, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	pub, server, err := m.CurrentSEK()
	if err != nil {
		return nil, err
	}
	if kf == nil || kf.Kind != vdcrypt.KindEscrow || kf.Server == nil {
		return nil, vdcrypt.ErrKeyFile
	}
	if kf.Server.SEKVersion == server.SEKVersion {
		return nil, nil
	}
	repo, err := vdcrypt.ParseRepoID(kf.Repo)
	if err != nil {
		return nil, err
	}
	b, err := m.OpenKeyFile(ctx, kf, a)
	if err != nil {
		return nil, err
	}
	defer b.Wipe()
	return vdcrypt.NewKeyFile(rand.Reader, vdcrypt.KeyFileParams{
		Repo: repo, Kind: vdcrypt.KindEscrow, Created: time.Now().UnixMilli(), Label: kf.Label,
		RecipientPub: pub, Server: &server,
	}, b)
}
