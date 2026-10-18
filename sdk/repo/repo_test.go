package repo

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// testKDF is the §13 test passphrase KDF (t=1, m=8 MiB).
func testKDF() *vdcrypt.PassphraseKDF {
	return &vdcrypt.PassphraseKDF{Alg: vdcrypt.PassphraseAlg, T: 1, MKiB: 8 * 1024, P: 4, Salt: bytes.Repeat([]byte{7}, 16)}
}

const passphrase = "correct horse battery staple"

type keyset struct {
	boxPriv, boxPub vdcrypt.Key
	sekPriv, sekPub vdcrypt.Key
	sign            ed25519.PrivateKey
	sik             ed25519.PrivateKey
}

func newKeyset(t *testing.T) keyset {
	t.Helper()
	var k keyset
	var err error
	if k.boxPriv, k.boxPub, err = vdcrypt.X25519FromSeed(vdcrypttest.DeviceBoxSeed); err != nil {
		t.Fatal(err)
	}
	if k.sekPriv, k.sekPub, err = vdcrypt.X25519FromSeed(vdcrypttest.SEKSeed); err != nil {
		t.Fatal(err)
	}
	k.sign = ed25519.NewKeyFromSeed(vdcrypttest.DeviceSignSeed[:])
	k.sik = ed25519.NewKeyFromSeed(vdcrypttest.SIKSeed[:])
	return k
}

func (k keyset) recipients(t *testing.T) []Recipient {
	t.Helper()
	pp, err := PassphraseRecipient(nil, []byte(passphrase), testKDF(), "passphrase")
	if err != nil {
		t.Fatal(err)
	}
	return []Recipient{
		DeviceRecipient(k.boxPub, 3, k.sign.Public().(ed25519.PublicKey), "DESKTOP-ABC"),
		EscrowRecipient(k.sekPub, k.sik.Public().(ed25519.PublicKey), vdcrypt.SignSEK(k.sik, k.sekPub, 1), 1, "server"),
		pp,
	}
}

func initRepo(t *testing.T, be Backend, k keyset, mode string) (*Repo, string) {
	t.Helper()
	r, text, err := Init(context.Background(), be, InitOptions{EscrowMode: mode, Recipients: k.recipients(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(text, vdcrypt.RecoveryPrefix) || !r.RecoveryReady() || r.Epoch() != 1 {
		t.Fatalf("init: text %q ready %v epoch %d", text, r.RecoveryReady(), r.Epoch())
	}
	return r, text
}

// written records what writeBackup stored, for read-back checks.
type written struct {
	snap   string
	files  map[string][]byte // name → content
	chunks map[string]pack.BlobID
}

// writeBackup writes one data pack, one meta pack with a tree and a signed
// snapshot whose files are name → content.
func writeBackup(t *testing.T, r *Repo, k keyset, files map[string][]byte) written {
	t.Helper()
	ctx := context.Background()
	keys, err := r.Keys()
	if err != nil {
		t.Fatal(err)
	}
	w := written{files: files, chunks: map[string]pack.BlobID{}}
	dw, err := r.NewPackWriter(pack.ClassData)
	if err != nil {
		t.Fatal(err)
	}
	var entries []snapshot.Entry
	for name, content := range files {
		id := pack.ComputeID(keys.ID, content)
		w.chunks[name] = id
		if !r.HasBlob(id) && !dw.Has(id) {
			if _, err := dw.Add(pack.TypeData, content, false); err != nil {
				t.Fatal(err)
			}
		}
		h := snapshot.FileHashBytes(keys.FileHash, content)
		entries = append(entries, snapshot.Entry{N: name, T: snapshot.TypeFile, S: int64(len(content)),
			M: 1760745600000000000, C: 1760745600000000000, A: 32,
			H: snapshot.HexPtr(id2hex(h)), Ch: []string{id.String()}})
	}
	if dw.Len() > 0 {
		p, err := dw.Finish()
		if err != nil {
			t.Fatal(err)
		}
		if err := r.SavePack(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	trees, err := snapshot.EncodeTrees(keys.ID, entries)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := r.NewPackWriter(pack.ClassMeta)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range trees {
		if _, err := mw.Add(pack.TypeTree, tb.Raw, false); err != nil {
			t.Fatal(err)
		}
	}
	mp, err := mw.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SavePack(ctx, mp); err != nil {
		t.Fatal(err)
	}
	root := &snapshot.Root{
		V: snapshot.Version, Time: 1760745600000,
		Job:    snapshot.Job{ID: 12, Name: "Documents", Kind: "folder", Mode: "full"},
		Device: snapshot.Device{ID: 3, Name: "DESKTOP-ABC"},
		Host: snapshot.Host{Hostname: "DESKTOP-ABC", OS: "Windows 11", User: "ali",
			SID: "S-1-5-21-1000", MachineGUIDHash: strings.Repeat("ab", 32)},
		Tokens:      snapshot.Tokens{snapshot.TokenDocuments: `C:\Users\ali\Documents`},
		Roots:       []snapshot.RootRef{{TokenPath: snapshot.TokenDocuments, Tree: trees[0].IDHex()}},
		Stats:       snapshot.Stats{Files: int64(len(files)), Dirs: 1},
		Profile:     snapshot.Profile{SID: "S-1-5-21-1000", Username: "ali", ConsentID: "c1"},
		InstallMode: snapshot.InstallModeSimple,
	}
	if w.snap, err = r.SaveSnapshot(ctx, root, k.sign); err != nil {
		t.Fatal(err)
	}
	return w
}

func id2hex(b [32]byte) string { return pack.BlobID(b).String() }

// readBack loads snapshot w.snap and checks every file through the tree.
// With data=false it expects ErrMetadataOnly for file content.
func readBack(t *testing.T, r *Repo, w written, data bool) {
	t.Helper()
	ctx := context.Background()
	snaps, err := r.ListSnapshots(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range snaps {
		found = found || s == w.snap
	}
	if !found {
		t.Fatalf("snapshot %s not listed in %v", w.snap, snaps)
	}
	root, js, err := r.LoadSnapshot(ctx, w.snap)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.VerifySnapshot(root, js); err != nil {
		t.Fatal(err)
	}
	tree, err := r.ReadTree(ctx, root.Roots[0].Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Entries) != len(w.files) {
		t.Fatalf("tree has %d entries, want %d", len(tree.Entries), len(w.files))
	}
	for _, e := range tree.Entries {
		if e.Ch[0] != w.chunks[e.N].String() {
			t.Fatalf("%s: chunk id", e.N)
		}
		got, err := r.ReadBlob(ctx, w.chunks[e.N])
		if !data {
			if !errors.Is(err, ErrMetadataOnly) {
				t.Fatalf("metadata-only read of data: %v", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, w.files[e.N]) {
			t.Fatalf("%s: content differs", e.N)
		}
	}
}

func credentials(t *testing.T, k keyset, recovery string) map[string]Credential {
	t.Helper()
	rc, err := RecoveryCredential(strings.ToLower(recovery))
	if err != nil {
		t.Fatal(err)
	}
	return map[string]Credential{
		"device":     DeviceCredential(k.boxPriv),
		"recovery":   rc,
		"passphrase": PassphraseCredential([]byte(passphrase)),
		"escrow":     EscrowCredential(k.sekPriv),
	}
}

func TestRoundTrip(t *testing.T) {
	ctx := context.Background()
	k := newKeyset(t)
	be := NewMemBackend()
	r, text := initRepo(t, be, k, EscrowFull)
	files := map[string][]byte{
		"a.txt": []byte(strings.Repeat("VaultDock round trip ", 200)),
		"b.bin": bytes.Repeat([]byte{1, 2, 3}, 1000),
		"c.txt": []byte("small"),
	}
	w := writeBackup(t, r, k, files)
	readBack(t, r, w, true)
	if !r.HasBlob(w.chunks["a.txt"]) || r.Index().Len() != 4 {
		t.Fatalf("dedup lookup / index len %d", r.Index().Len())
	}

	for name, cred := range credentials(t, k, text) {
		t.Run(name, func(t *testing.T) {
			o, err := Open(ctx, be, cred, Options{})
			if err != nil {
				t.Fatal(err)
			}
			defer o.Close()
			if o.MetadataOnly() || o.Stale() || o.Epoch() != 1 || len(o.Index().Files()) == 0 {
				t.Fatalf("open state: meta %v stale %v epoch %d files %v", o.MetadataOnly(), o.Stale(), o.Epoch(), o.Index().Files())
			}
			readBack(t, o, w, true)
			// Writing a second backup dedups all data blobs.
			w2 := writeBackup(t, o, k, files)
			readBack(t, o, w2, true)
		})
	}

	if _, _, err := Init(ctx, be, InitOptions{Recipients: k.recipients(t)}); !errors.Is(err, ErrExists) {
		t.Fatalf("second init: %v", err)
	}
	_, wrong, _ := vdcrypt.GenerateX25519(nil)
	if _, err := Open(ctx, be, DeviceCredential(wrong), Options{}); !errors.Is(err, ErrNoKeyFile) {
		t.Fatalf("wrong device key: %v", err)
	}
	if _, err := Open(ctx, be, PassphraseCredential([]byte("nope")), Options{}); !errors.Is(err, ErrNoKeyFile) {
		t.Fatalf("wrong passphrase: %v", err)
	}
}

func TestRotation(t *testing.T) {
	ctx := context.Background()
	k := newKeyset(t)
	be := NewMemBackend()
	r, text := initRepo(t, be, k, EscrowFull)
	w1 := writeBackup(t, r, k, map[string][]byte{"old.txt": []byte("epoch one data")})

	e, err := r.Rotate(ctx, RotateOptions{})
	if err != nil || e != 2 || r.Epoch() != 2 {
		t.Fatalf("rotate: %d %v", e, err)
	}
	w2 := writeBackup(t, r, k, map[string][]byte{"new.txt": []byte("epoch two data")})
	if ek, _ := r.EpochKeys(2); ek.Epoch != 2 {
		t.Fatal("epoch 2 keys")
	}

	// Every recipient reaches epoch 1 data through the chain.
	for name, cred := range credentials(t, k, text) {
		o, err := Open(ctx, be, cred, Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if o.Epoch() != 2 || o.Stale() {
			t.Fatalf("%s: epoch %d stale %v", name, o.Epoch(), o.Stale())
		}
		readBack(t, o, w1, true)
		readBack(t, o, w2, true)
	}

	// Revoke the device: epoch 3 without its wrap.
	e, err = r.RemoveRecipient(ctx, func(kf *vdcrypt.KeyFile) bool { return kf.Kind == vdcrypt.KindDevice })
	if err != nil || e != 3 {
		t.Fatalf("remove: %d %v", e, err)
	}
	for _, rc := range r.Recipients() {
		if rc.Kind == vdcrypt.KindDevice {
			t.Fatal("device still wrapped")
		}
	}
	dev, err := Open(ctx, be, DeviceCredential(k.boxPriv), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !dev.Stale() || dev.Epoch() != 2 {
		t.Fatalf("revoked device: stale %v epoch %d", dev.Stale(), dev.Epoch())
	}
	readBack(t, dev, w1, true) // inherent: epochs it held stay readable
	if _, err := dev.NewPackWriter(pack.ClassData); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale write: %v", err)
	}
	if _, err := dev.Rotate(ctx, RotateOptions{}); !errors.Is(err, ErrStaleEpoch) {
		t.Fatalf("stale rotate: %v", err)
	}

	// Re-add a (new) device to the current epoch.
	priv2, pub2, _ := vdcrypt.GenerateX25519(nil)
	if err := r.AddRecipient(ctx, DeviceRecipient(pub2, 4, k.sign.Public().(ed25519.PublicKey), "NEW-PC")); err != nil {
		t.Fatal(err)
	}
	d2, err := Open(ctx, be, DeviceCredential(priv2), Options{})
	if err != nil || d2.Epoch() != 3 || d2.Stale() {
		t.Fatalf("new device: %v", err)
	}
	readBack(t, d2, w1, true)

	if _, err := r.RemoveRecipient(ctx, func(kf *vdcrypt.KeyFile) bool { return kf.Kind == vdcrypt.KindRecovery }); !errors.Is(err, ErrRecoveryRequired) {
		t.Fatalf("remove recovery: %v", err)
	}
}

func TestMetadataOnly(t *testing.T) {
	ctx := context.Background()
	k := newKeyset(t)
	be := NewMemBackend()
	r, _ := initRepo(t, be, k, EscrowMetadata)
	w1 := writeBackup(t, r, k, map[string][]byte{"secret.txt": []byte("server must not read this")})
	if _, err := r.Rotate(ctx, RotateOptions{}); err != nil {
		t.Fatal(err)
	}
	w2 := writeBackup(t, r, k, map[string][]byte{"secret2.txt": []byte("nor this")})

	m, err := Open(ctx, be, EscrowCredential(k.sekPriv), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !m.MetadataOnly() || m.Epoch() != 2 {
		t.Fatalf("meta %v epoch %d", m.MetadataOnly(), m.Epoch())
	}
	readBack(t, m, w1, false) // epoch 1 trees via the meta-chain
	readBack(t, m, w2, false)
	if _, err := m.Keys(); !errors.Is(err, ErrMetadataOnly) {
		t.Fatal("meta keys")
	}
	if ek, _ := m.EpochKeys(1); !ek.Data.IsZero() || !ek.Chain.IsZero() || ek.Meta.IsZero() {
		t.Fatal("metadata-only holder has data or chain keys")
	}
	if _, err := m.NewPackWriter(pack.ClassMeta); !errors.Is(err, ErrMetadataOnly) {
		t.Fatal("meta write")
	}
	if _, err := m.Rotate(ctx, RotateOptions{}); !errors.Is(err, ErrMetadataOnly) {
		t.Fatal("meta rotate")
	}
	// The full chain does not open with K_meta.
	chains, _, _ := loadChains(ctx, be)
	if _, err := vdcrypt.ResolveEpochs(m.ID(), vdcrypt.EpochSecret{Epoch: 2, RMK: mustEK(t, m, 2).Meta}, chains); err == nil {
		t.Fatal("full chain walked with K_meta")
	}

	// Switch to full escrow: the server can now read data of the new epoch
	// and, through the chain, of the older ones.
	if e, err := r.SetEscrowMode(ctx, EscrowFull); err != nil || e != 3 {
		t.Fatalf("set escrow: %d %v", e, err)
	}
	f, err := Open(ctx, be, EscrowCredential(k.sekPriv), Options{})
	if err != nil || f.MetadataOnly() || f.Config().EscrowMode != EscrowFull {
		t.Fatalf("full escrow open: %v", err)
	}
	readBack(t, f, w1, true)
}

func mustEK(t *testing.T, r *Repo, e uint32) vdcrypt.EpochKeys {
	t.Helper()
	ek, err := r.EpochKeys(e)
	if err != nil {
		t.Fatal(err)
	}
	return ek
}

func TestConfigTamper(t *testing.T) {
	ctx := context.Background()
	k := newKeyset(t)
	be := NewMemBackend()
	initRepo(t, be, k, EscrowFull)
	orig, _ := be.Raw(KindConfig, ConfigOID)
	orig = append([]byte(nil), orig...)
	edit := func(f func(m map[string]any)) []byte {
		var m map[string]any
		if err := json.Unmarshal(orig, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		b, _ := json.Marshal(m)
		return b
	}
	cases := []struct {
		name string
		cfg  []byte
		want error
	}{
		{"pack_target", edit(func(m map[string]any) { m["pack_target"] = 1 << 20 }), ErrConfigMAC},
		{"escrow_mode", edit(func(m map[string]any) { m["escrow_mode"] = EscrowMetadata }), ErrConfigMAC},
		{"created", edit(func(m map[string]any) { m["created"] = 1 }), ErrConfigMAC},
		{"unknown member", edit(func(m map[string]any) { m["extra"] = true }), ErrConfigMAC},
		{"mac", edit(func(m map[string]any) { m["mac"] = strings.Repeat("0", 64) }), ErrConfigMAC},
		{"recovery_pub", edit(func(m map[string]any) { m["recovery_pub"] = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=" }), ErrConfigMAC},
		{"format", edit(func(m map[string]any) { m["format"] = "other" }), ErrConfigFormat},
		{"version", edit(func(m map[string]any) { m["v"] = 2 }), ErrConfigVersion},
		{"chunker", edit(func(m map[string]any) { m["chunker"] = map[string]any{"alg": "x"} }), ErrConfig},
		{"duplicate member", append(bytes.TrimSuffix(bytes.Clone(orig), []byte("}")), []byte(`,"v":1}`)...), ErrConfig},
		{"not json", []byte("{"), ErrConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			be.SetRaw(KindConfig, ConfigOID, tc.cfg)
			if _, err := Open(ctx, be, DeviceCredential(k.boxPriv), Options{}); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	// Re-encoded but semantically identical JSON still verifies (JCS).
	be.SetRaw(KindConfig, ConfigOID, edit(func(map[string]any) {}))
	if _, err := Open(ctx, be, DeviceCredential(k.boxPriv), Options{}); err != nil {
		t.Fatalf("reordered config: %v", err)
	}
}

func TestObjectTamper(t *testing.T) {
	ctx := context.Background()
	k := newKeyset(t)
	be := NewMemBackend()
	r, _ := initRepo(t, be, k, EscrowFull)
	w := writeBackup(t, r, k, map[string][]byte{"x": []byte("payload")})

	// A snapshot whose bytes no longer match its oid is rejected.
	b, _ := be.Raw(KindSnapshots, w.snap)
	bad := append([]byte(nil), b...)
	bad[len(bad)-1] ^= 1
	be.SetRaw(KindSnapshots, w.snap, bad)
	if _, _, err := r.LoadSnapshot(ctx, w.snap); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("tampered snapshot: %v", err)
	}
	be.SetRaw(KindSnapshots, w.snap, b)

	// A flipped byte inside a data pack fails the blob AEAD.
	loc, _ := r.Index().Lookup(w.chunks["x"])
	p, _ := be.Raw(KindData, loc.Pack)
	p[loc.Entry.Offset+50] ^= 1
	if _, err := r.ReadBlob(ctx, w.chunks["x"]); !errors.Is(err, pack.ErrBlobAuth) {
		t.Fatalf("tampered pack: %v", err)
	}
	p[loc.Entry.Offset+50] ^= 1

	// Another device's signature does not verify.
	root, js, err := r.LoadSnapshot(ctx, w.snap)
	if err != nil {
		t.Fatal(err)
	}
	root.Device.ID = 99
	if err := r.VerifySnapshot(root, js); !errors.Is(err, ErrNoDeviceKey) {
		t.Fatalf("unknown device: %v", err)
	}
}

func TestLockObject(t *testing.T) {
	k := newKeyset(t)
	r, _ := initRepo(t, NewMemBackend(), k, EscrowFull)
	l := &LockObject{V: 1, Holder: map[string]string{"host": "restore"}, Kind: LockExclusive, Created: 1, Expires: 2}
	sealed, err := r.SealLock(l)
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.OpenLock(sealed)
	if err != nil || got.Kind != LockExclusive || got.Holder["host"] != "restore" {
		t.Fatalf("lock: %+v %v", got, err)
	}
	sealed[len(sealed)-1] ^= 1
	if _, err := r.OpenLock(sealed); err == nil {
		t.Fatal("tampered lock opened")
	}
}
