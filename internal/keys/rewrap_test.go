package keys_test

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

const rewrapRepo = "0b4f6c1e-3d2a-4f5b-8c9d-0e1f2a3b4c5d"

// escrowFile wraps b to the manager's current SEK.
func escrowFile(t *testing.T, m *keys.Manager, b vdcrypt.Bundle) *vdcrypt.KeyFile {
	t.Helper()
	repo, _ := vdcrypt.ParseRepoID(rewrapRepo)
	pub, server, err := m.CurrentSEK()
	if err != nil {
		t.Fatal(err)
	}
	kf, err := vdcrypt.NewKeyFile(nil, vdcrypt.KeyFileParams{Repo: repo, Kind: vdcrypt.KindEscrow, Label: "escrow", RecipientPub: pub, Server: &server}, b)
	if err != nil {
		t.Fatal(err)
	}
	return kf
}

func TestRewrapToDeviceAndEscrow(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, st, kek)
	if err != nil {
		t.Fatal(err)
	}
	rmk, _ := vdcrypt.RandomKey(nil)
	dk, _ := vdcrypt.RandomKey(nil)
	full := vdcrypt.FullBundle(vdcrypt.EpochSecret{Epoch: 2, RMK: rmk}, dk)
	kf := escrowFile(t, m, full)

	boxPriv, boxPub, _ := vdcrypt.GenerateX25519(nil)
	signPub, _, _ := ed25519.GenerateKey(nil)
	a := keys.Audit{Purpose: keys.PurposeRewrap, Details: "test"}

	dkf, err := m.RewrapToDevice(ctx, kf, keys.DeviceTarget{ID: 7, BoxPub: boxPub, SignPub: signPub, Label: "PC"}, a)
	if err != nil {
		t.Fatal(err)
	}
	if dkf.Kind != vdcrypt.KindDevice || dkf.Epoch != 2 || dkf.Device.ID != 7 || dkf.Repo != rewrapRepo {
		t.Fatalf("device key file: %+v", dkf)
	}
	got, err := dkf.Open(boxPriv)
	if err != nil || !got.RMK.Equal(rmk) || !got.DK.Equal(dk) {
		t.Fatalf("open device wrap: %v", err)
	}

	// Bad targets and meta bundles are refused.
	if _, err := m.RewrapToDevice(ctx, kf, keys.DeviceTarget{ID: 7, BoxPub: boxPub}, a); !errors.Is(err, vdcrypt.ErrKeyFile) {
		t.Fatalf("missing sign pub: %v", err)
	}
	meta := escrowFile(t, m, vdcrypt.MetaBundle(vdcrypt.EpochKeys{Epoch: 2, Meta: rmk}))
	if _, err := m.RewrapToDevice(ctx, meta, keys.DeviceTarget{ID: 7, BoxPub: boxPub, SignPub: signPub}, a); !errors.Is(err, keys.ErrMetaBundle) {
		t.Fatalf("meta bundle: %v", err)
	}

	// Escrow re-wrap: a no-op at the current SEK, a new file after rotation.
	if nf, err := m.RewrapEscrow(ctx, kf, a); err != nil || nf != nil {
		t.Fatalf("current sek: %v %v", nf, err)
	}
	if _, err := m.RotateSEK(ctx, keys.Audit{Details: "test"}); err != nil {
		t.Fatal(err)
	}
	nf, err := m.RewrapEscrow(ctx, kf, keys.Audit{Purpose: keys.PurposeRotation})
	if err != nil || nf == nil || nf.Server.SEKVersion != 2 || nf.Epoch != 2 {
		t.Fatalf("rewrap escrow: %+v %v", nf, err)
	}
	b, err := m.OpenKeyFile(ctx, nf, keys.Audit{Purpose: keys.PurposeRotation})
	if err != nil || !b.RMK.Equal(rmk) {
		t.Fatalf("open rewrapped: %v", err)
	}
	nm, err := m.RewrapEscrow(ctx, meta, keys.Audit{Purpose: keys.PurposeRotation})
	if err != nil || nm == nil {
		t.Fatalf("rewrap meta escrow: %v", err)
	}

	// A nil manager is unavailable.
	var none *keys.Manager
	if _, err := none.RewrapToDevice(ctx, kf, keys.DeviceTarget{}, a); !errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("nil manager: %v", err)
	}
}
