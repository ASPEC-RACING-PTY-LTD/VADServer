package keys_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/config"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestFingerprint(t *testing.T) {
	pub := make([]byte, 32)
	fp := keys.Fingerprint(pub)
	if !regexp.MustCompile(`^[0-9A-F]{4}(-[0-9A-F]{4}){7}$`).MatchString(fp) {
		t.Fatalf("format: %q", fp)
	}
	sum := sha256.Sum256(pub)
	if got := strings.ReplaceAll(fp, "-", ""); got != strings.ToUpper(hex.EncodeToString(sum[:16])) {
		t.Fatalf("value: %q", fp)
	}
}

func TestLoadStartupRules(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	kekPath := filepath.Join(t.TempDir(), "keys", "kek")

	// No KEK, no rows, test binary: generated.
	m, err := keys.Load(ctx, st, &config.Config{KEKFile: kekPath})
	if err != nil || m == nil {
		t.Fatalf("first load: %v %v", m, err)
	}
	if b, err := os.ReadFile(kekPath); err != nil || len(b) != keys.KEKSize {
		t.Fatalf("kek file: %d %v", len(b), err)
	}
	id, _ := m.Identity()

	// Reload with the same KEK: same identity.
	m2, err := keys.Load(ctx, st, &config.Config{KEKFile: kekPath})
	if err != nil {
		t.Fatal(err)
	}
	if id2, _ := m2.Identity(); id2 != id {
		t.Fatalf("identity changed: %v %v", id, id2)
	}

	// Wrong KEK: refuse.
	other := filepath.Join(t.TempDir(), "kek")
	os.WriteFile(other, make([]byte, 32), 0o600)
	if _, err := keys.Load(ctx, st, &config.Config{KEKFile: other}); !errors.Is(err, keys.ErrKEKMismatch) {
		t.Fatalf("wrong kek: %v", err)
	}
	// Wrong size: refuse.
	os.WriteFile(other, make([]byte, 31), 0o600)
	if _, err := keys.Load(ctx, st, &config.Config{KEKFile: other}); err == nil {
		t.Fatal("short kek accepted")
	}
	// KEK missing but keys exist: refuse (no generation over existing keys).
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := keys.Load(ctx, st, &config.Config{KEKFile: missing}); err == nil {
		t.Fatal("missing kek with keys accepted")
	}
	if _, err := os.Stat(missing); err == nil {
		t.Fatal("kek generated over existing keys")
	}

	// No KEK path, empty database: degraded (nil manager).
	m3, err := keys.Load(ctx, openStore(t), &config.Config{})
	if err != nil || m3 != nil {
		t.Fatalf("degraded: %v %v", m3, err)
	}
	if _, err := m3.Identity(); !errors.Is(err, keys.ErrUnavailable) {
		t.Fatalf("nil manager: %v", err)
	}
}

func TestIdentityAndRotation(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, st, kek)
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.Identity()
	if err != nil {
		t.Fatal(err)
	}
	sik, _ := base64.StdEncoding.DecodeString(id.SIKPub)
	sekB, _ := base64.StdEncoding.DecodeString(id.SEKPub)
	sig, _ := base64.StdEncoding.DecodeString(id.SEKSig)
	sek, _ := vdcrypt.KeyFromBytes(sekB)
	if id.SEKVersion != 1 || id.SIKFingerprint != keys.Fingerprint(sik) || !vdcrypt.VerifySEK(sik, sek, 1, sig) {
		t.Fatalf("identity: %+v", id)
	}
	if !m.SIKPublic().Equal(ed25519.PublicKey(sik)) {
		t.Fatal("SIKPublic")
	}
	msg := []byte("hello")
	if s, err := m.Sign(msg); err != nil || !ed25519.Verify(sik, msg, s) {
		t.Fatal("Sign")
	}

	// Escrow wrap to SEK v1, then rotate; the v1 file still opens.
	repo, _ := vdcrypt.ParseRepoID("0f8b3c2a-4d5e-4f60-8a7b-9c0d1e2f3a4b")
	pub, server, err := m.CurrentSEK()
	if err != nil || server.SEKVersion != 1 {
		t.Fatal(err)
	}
	dk, _ := vdcrypt.RandomKey(nil)
	rmk, _ := vdcrypt.RandomKey(nil)
	bundle := vdcrypt.Bundle{Epoch: 1, RMK: rmk, DK: dk}
	kf, err := vdcrypt.NewKeyFile(nil, vdcrypt.KeyFileParams{Repo: repo, Kind: vdcrypt.KindEscrow, Label: "server", RecipientPub: pub, Server: &server}, bundle)
	if err != nil {
		t.Fatal(err)
	}

	uid := int64(7)
	v, err := m.RotateSEK(ctx, keys.Audit{ActorUserID: &uid})
	if err != nil || v != 2 {
		t.Fatalf("rotate: %d %v", v, err)
	}
	id2, _ := m.Identity()
	if id2.SEKVersion != 2 || id2.SEKPub == id.SEKPub || id2.SIKPub != id.SIKPub {
		t.Fatalf("after rotate: %+v", id2)
	}
	cur, _ := st.CurrentServerKey(ctx, store.ServerKeySEK)
	all, _ := st.ListServerKeys(ctx, store.ServerKeySEK)
	if cur.Version != 2 || len(all) != 2 || all[0].RetiredAt == nil {
		t.Fatalf("rows: %+v %+v", cur, all)
	}

	repoID := int64(3)
	got, err := m.OpenKeyFile(ctx, kf, keys.Audit{Purpose: keys.PurposeRewrap, RepoID: &repoID})
	if err != nil || !got.RMK.Equal(rmk) || !got.DK.Equal(dk) {
		t.Fatalf("open retired-version file: %v", err)
	}
	if _, err := m.OpenKeyFile(ctx, kf, keys.Audit{Purpose: "curiosity"}); !errors.Is(err, keys.ErrPurpose) {
		t.Fatalf("bad purpose: %v", err)
	}
	priv, err := m.OpenSEK(ctx, 1, keys.Audit{Purpose: keys.PurposeRotation})
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := vdcrypt.X25519Public(priv); !p.Equal(pub) {
		t.Fatal("OpenSEK v1")
	}

	// Audit: rotation, unwrap, open_sek (the rejected purpose wrote nothing).
	rows, err := st.ListKeyAudit(ctx, 0, 0, 0)
	if err != nil || len(rows) != 3 {
		t.Fatalf("audit rows: %d %v", len(rows), err)
	}
	if rows[0].Action != keys.ActionRotateSEK || *rows[0].KeyVersion != 2 || *rows[0].ActorUserID != 7 {
		t.Fatalf("rotate audit: %+v", rows[0])
	}
	if rows[1].Action != keys.ActionUnwrap || *rows[1].RepoID != 3 || *rows[1].Epoch != 1 || *rows[1].KeyVersion != 1 || rows[1].Purpose != keys.PurposeRewrap {
		t.Fatalf("unwrap audit: %+v", rows[1])
	}
	if r, _ := st.ListKeyAudit(ctx, 3, 0, 0); len(r) != 1 {
		t.Fatalf("repo filter: %d", len(r))
	}

	// A file wrapped to another server's SEK is refused before any audit.
	_, otherPub, _ := vdcrypt.GenerateX25519(nil)
	_, foreignSIK, _ := ed25519.GenerateKey(nil)
	fs := vdcrypt.KeyFileServer{SIKPub: foreignSIK.Public().(ed25519.PublicKey), SEKSig: vdcrypt.SignSEK(foreignSIK, otherPub, 1), SEKVersion: 1}
	kf2, _ := vdcrypt.NewKeyFile(nil, vdcrypt.KeyFileParams{Repo: repo, Kind: vdcrypt.KindEscrow, Label: "x", RecipientPub: otherPub, Server: &fs}, bundle)
	if _, err := m.OpenKeyFile(ctx, kf2, keys.Audit{Purpose: keys.PurposeRewrap}); !errors.Is(err, keys.ErrWrongServer) {
		t.Fatalf("foreign file: %v", err)
	}
	if r, _ := st.ListKeyAudit(ctx, 0, 0, 0); len(r) != 3 {
		t.Fatalf("foreign file audited: %d", len(r))
	}

	// Reload after rotation: current is v2 and still verifies.
	m2, err := keys.New(ctx, st, kek)
	if err != nil {
		t.Fatal(err)
	}
	if id3, _ := m2.Identity(); id3 != id2 {
		t.Fatalf("reload: %+v", id3)
	}
}
