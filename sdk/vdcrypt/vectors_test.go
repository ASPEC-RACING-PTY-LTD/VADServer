package vdcrypt

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// Run `go test ./vdcrypt/ -run TestVectors -update` to regenerate. Any
// change to the vector files is a format change and must not happen in v1.
var update = flag.Bool("update", false, "rewrite sdk/repo/testdata/v1/crypto_*.json")

const vectorDir = "../repo/testdata/v1"

const testPassphrase = "correct horse battery staple"

func hx(b []byte) string { return hex.EncodeToString(b) }

type fixture struct {
	repo                        RepoID
	keys                        RepoKeys
	e1, e2                      EpochSecret
	k1, k2                      EpochKeys
	recPriv, recPub             Key
	devPriv, devPub             Key
	sekPriv, sekPub             Key
	devSign, sik                ed25519.PrivateKey
	sekSig                      []byte
	dk, recoverySecret, devSeed Key
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	var f fixture
	var err error
	if f.repo, err = ParseRepoID(vdcrypttest.RepoUUID); err != nil {
		t.Fatal(err)
	}
	f.dk = vdcrypttest.DK
	f.keys = DeriveRepoKeys(f.repo, f.dk)
	f.e1 = EpochSecret{Epoch: 1, RMK: vdcrypttest.RMK1}
	f.e2 = EpochSecret{Epoch: 2, RMK: vdcrypttest.RMK2}
	if f.k1, err = f.e1.Keys(f.repo); err != nil {
		t.Fatal(err)
	}
	if f.k2, err = f.e2.Keys(f.repo); err != nil {
		t.Fatal(err)
	}
	f.recoverySecret = vdcrypttest.RecoverySecret
	if f.recPriv, f.recPub, err = RecoveryKeyPair(f.repo, f.recoverySecret); err != nil {
		t.Fatal(err)
	}
	if f.devPriv, f.devPub, err = X25519FromSeed(vdcrypttest.DeviceBoxSeed); err != nil {
		t.Fatal(err)
	}
	if f.sekPriv, f.sekPub, err = X25519FromSeed(vdcrypttest.SEKSeed); err != nil {
		t.Fatal(err)
	}
	f.devSign = ed25519.NewKeyFromSeed(vdcrypttest.DeviceSignSeed[:])
	f.sik = ed25519.NewKeyFromSeed(vdcrypttest.SIKSeed[:])
	f.sekSig = SignSEK(f.sik, f.sekPub, 1)
	return f
}

func pub(k ed25519.PrivateKey) []byte { return k.Public().(ed25519.PublicKey) }

// Vector 1: all subkeys.
func vectorSubkeys(f fixture) map[string]any {
	return map[string]any{
		"vector":    1,
		"repo_uuid": vdcrypttest.RepoUUID,
		"inputs": map[string]any{
			"dk": hx(f.dk[:]), "rmk_1": hx(f.e1.RMK[:]), "rmk_2": hx(f.e2.RMK[:]),
			"recovery_secret":  hx(f.recoverySecret[:]),
			"device_box_seed":  hx(vdcrypttest.DeviceBoxSeed[:]),
			"sek_seed":         hx(vdcrypttest.SEKSeed[:]),
			"device_sign_seed": hx(vdcrypttest.DeviceSignSeed[:]),
			"sik_seed":         hx(vdcrypttest.SIKSeed[:]),
		},
		"repo_keys": map[string]any{
			"k_id": hx(f.keys.ID[:]), "k_chunker": hx(f.keys.Chunker[:]),
			"k_fh": hx(f.keys.FileHash[:]), "k_mac": hx(f.keys.ConfigMAC[:]),
		},
		"epoch_keys": []any{
			map[string]any{"epoch": 1, "k_data": hx(f.k1.Data[:]), "k_meta": hx(f.k1.Meta[:]), "k_chain": hx(f.k1.Chain[:])},
			map[string]any{"epoch": 2, "k_data": hx(f.k2.Data[:]), "k_meta": hx(f.k2.Meta[:]), "k_chain": hx(f.k2.Chain[:])},
		},
		"recipients": map[string]any{
			"recovery_priv": hx(f.recPriv[:]), "recovery_pub": hx(f.recPub[:]),
			"device_box_priv": hx(f.devPriv[:]), "device_box_pub": hx(f.devPub[:]),
			"sek_priv": hx(f.sekPriv[:]), "sek_pub": hx(f.sekPub[:]),
			"device_sign_pub": hx(pub(f.devSign)), "sik_pub": hx(pub(f.sik)),
			"sek_version": 1, "sek_sig": hx(f.sekSig),
		},
	}
}

type keyFileCase struct {
	name   string
	params KeyFileParams
	bundle Bundle
	priv   Key
}

func keyFileCases(t *testing.T, f fixture) []keyFileCase {
	t.Helper()
	server := &KeyFileServer{SIKPub: pub(f.sik), SEKSig: f.sekSig, SEKVersion: 1}
	full := FullBundle(f.e1, f.dk)
	kdfRNG := vdcrypttest.NewRNG("keyfile-passphrase-kdf")
	kdf, err := NewPassphraseKDF(kdfRNG)
	if err != nil {
		t.Fatal(err)
	}
	kdf.T, kdf.MKiB = 1, 8*1024 // §13 test parameters
	pPriv, pPub, err := PassphraseKeyPair([]byte(testPassphrase), kdf)
	if err != nil {
		t.Fatal(err)
	}
	return []keyFileCase{
		{"device", KeyFileParams{Repo: f.repo, Kind: KindDevice, Label: "DESKTOP-ABC", RecipientPub: f.devPub,
			Device: &KeyFileDevice{ID: 3, SignPub: pub(f.devSign)}}, full, f.devPriv},
		{"escrow-full", KeyFileParams{Repo: f.repo, Kind: KindEscrow, Label: "server", RecipientPub: f.sekPub, Server: server}, full, f.sekPriv},
		{"escrow-meta", KeyFileParams{Repo: f.repo, Kind: KindEscrow, Label: "server", RecipientPub: f.sekPub, Server: server}, MetaBundle(f.k1), f.sekPriv},
		{"recovery", KeyFileParams{Repo: f.repo, Kind: KindRecovery, Label: "recovery key", RecipientPub: f.recPub}, full, f.recPriv},
		{"passphrase", KeyFileParams{Repo: f.repo, Kind: KindPassphrase, Label: "passphrase", RecipientPub: pPub, KDF: kdf}, full, pPriv},
	}
}

// Vector 6: key files.
func vectorKeyFiles(t *testing.T, f fixture) map[string]any {
	var files []any
	for _, c := range keyFileCases(t, f) {
		kf, err := NewKeyFile(vdcrypttest.NewRNG("keyfile-"+c.name), c.params, c.bundle)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		enc, err := kf.Encode()
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		// Round trip through the stored bytes.
		dec, err := DecodeKeyFile(enc)
		if err != nil {
			t.Fatalf("%s: decode: %v", c.name, err)
		}
		b, err := dec.Open(c.priv)
		if err != nil {
			t.Fatalf("%s: open: %v", c.name, err)
		}
		if b != c.bundle {
			t.Fatalf("%s: bundle mismatch", c.name)
		}
		payload, _ := c.bundle.encode()
		files = append(files, map[string]any{
			"name": c.name, "rng_label": "keyfile-" + c.name,
			"recipient_priv": hx(c.priv[:]),
			"bundle_json":    string(payload),
			"key_file":       string(enc),
			"oid":            OID(enc),
		})
	}
	return map[string]any{
		"vector":     6,
		"repo_uuid":  vdcrypttest.RepoUUID,
		"passphrase": testPassphrase,
		"note":       "passphrase KDF uses the §13 test parameters t=1, m=8 MiB; salt from rng_label keyfile-passphrase-kdf",
		"key_files":  files,
	}
}

// Vector 7 (rev 1.2): chain_2 under K_chain_2 and metachain_2 under
// K_meta_2, plus the epoch-1 objects.
func vectorChain(t *testing.T, f fixture) map[string]any {
	c1, err := SealChain(vdcrypttest.NewRNG("chain_1"), f.repo, f.e1, nil)
	if err != nil {
		t.Fatal(err)
	}
	c2, err := SealChain(vdcrypttest.NewRNG("chain_2"), f.repo, f.e2, &f.e1)
	if err != nil {
		t.Fatal(err)
	}
	m1, err := SealMetaChain(vdcrypttest.NewRNG("metachain_1"), f.repo, f.k1, nil)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := SealMetaChain(vdcrypttest.NewRNG("metachain_2"), f.repo, f.k2, &f.k1.Meta)
	if err != nil {
		t.Fatal(err)
	}
	obj := func(label string, sealed []byte, key Key, epoch uint32, ad []byte) map[string]any {
		pt, err := Open(key, epoch, sealed, ad)
		if err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		return map[string]any{"rng_label": label, "epoch": epoch, "plaintext": string(pt),
			"ad": string(ad), "sealed": hx(sealed), "oid": OID(sealed)}
	}
	return map[string]any{
		"vector":      7,
		"revision":    "1.2",
		"repo_uuid":   vdcrypttest.RepoUUID,
		"chain_1":     obj("chain_1", c1, f.k1.Chain, 1, chainAD(f.repo)),
		"chain_2":     obj("chain_2", c2, f.k2.Chain, 2, chainAD(f.repo)),
		"metachain_1": obj("metachain_1", m1, f.k1.Meta, 1, metaChainAD(f.repo)),
		"metachain_2": obj("metachain_2", m2, f.k2.Meta, 2, metaChainAD(f.repo)),
	}
}

// Vector 8: recovery key text round trip.
func vectorRecovery(t *testing.T, f fixture) map[string]any {
	text := EncodeRecoveryKey(f.recoverySecret)
	body := strings.TrimPrefix(text, RecoveryPrefix)
	variants := []string{
		text,
		strings.ToLower(text),
		"  " + strings.ReplaceAll(text, "-", " ") + "\n",
		RecoveryPrefix + strings.NewReplacer("0", "o", "1", "l").Replace(body),
		"VDRK1" + strings.ReplaceAll(body, "-", ""),
	}
	for _, v := range variants {
		got, err := DecodeRecoveryKey(v)
		if err != nil || got != f.recoverySecret {
			t.Fatalf("decode %q: %v", v, err)
		}
	}
	bad := corruptRecovery(text)
	if _, err := DecodeRecoveryKey(bad); err != ErrRecoveryChecksum {
		t.Fatalf("bad checksum: got %v", err)
	}
	return map[string]any{
		"vector":          8,
		"recovery_secret": hx(f.recoverySecret[:]),
		"text":            text,
		"accepted":        variants,
		"bad_checksum":    bad,
	}
}

// corruptRecovery changes the first character after the prefix, which
// keeps the text well-formed but breaks the checksum.
func corruptRecovery(text string) string {
	b := []byte(text)
	i := len(RecoveryPrefix)
	if b[i] == 'Z' {
		b[i] = 'Y'
	} else {
		b[i] = 'Z'
	}
	return string(b)
}

func TestVectors(t *testing.T) {
	f := newFixture(t)
	vectors := []struct {
		file string
		gen  func() map[string]any
	}{
		{"crypto_subkeys.json", func() map[string]any { return vectorSubkeys(f) }},
		{"crypto_keyfiles.json", func() map[string]any { return vectorKeyFiles(t, f) }},
		{"crypto_chain.json", func() map[string]any { return vectorChain(t, f) }},
		{"crypto_recovery.json", func() map[string]any { return vectorRecovery(t, f) }},
	}
	for _, v := range vectors {
		t.Run(v.file, func(t *testing.T) {
			var buf bytes.Buffer
			enc := json.NewEncoder(&buf)
			enc.SetEscapeHTML(false)
			enc.SetIndent("", "  ")
			if err := enc.Encode(v.gen()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(vectorDir, v.file)
			if *update {
				if err := os.MkdirAll(vectorDir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("missing vector file (run with -update once): %v", err)
			}
			want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
			if !bytes.Equal(want, buf.Bytes()) {
				t.Fatalf("%s: recomputed vector differs from the frozen file; the v1 format must not change", v.file)
			}
		})
	}
}

// TestVectorFilesOpen checks the frozen files independently of the
// generator: stored key files and chain_2 must open with the fixed keys.
func TestVectorFilesOpen(t *testing.T) {
	if *update {
		t.Skip("updating")
	}
	f := newFixture(t)
	raw, err := os.ReadFile(filepath.Join(vectorDir, "crypto_keyfiles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var kv struct {
		KeyFiles []struct {
			Name     string `json:"name"`
			Priv     string `json:"recipient_priv"`
			KeyFile  string `json:"key_file"`
			OID      string `json:"oid"`
			BundleJS string `json:"bundle_json"`
		} `json:"key_files"`
	}
	if err := json.Unmarshal(raw, &kv); err != nil {
		t.Fatal(err)
	}
	if len(kv.KeyFiles) != 5 {
		t.Fatalf("want 5 key files, got %d", len(kv.KeyFiles))
	}
	for _, c := range kv.KeyFiles {
		if OID([]byte(c.KeyFile)) != c.OID {
			t.Fatalf("%s: oid mismatch", c.Name)
		}
		kf, err := DecodeKeyFile([]byte(c.KeyFile))
		if err != nil {
			t.Fatalf("%s: %v", c.Name, err)
		}
		var b Bundle
		if c.Name == "passphrase" {
			b, err = kf.OpenPassphrase([]byte(testPassphrase))
		} else {
			pb, _ := hex.DecodeString(c.Priv)
			priv, _ := KeyFromBytes(pb)
			b, err = kf.Open(priv)
		}
		if err != nil {
			t.Fatalf("%s: open: %v", c.Name, err)
		}
		if enc, _ := b.encode(); string(enc) != c.BundleJS {
			t.Fatalf("%s: bundle mismatch", c.Name)
		}
		if kf.Kind == KindEscrow {
			if err := kf.VerifyEscrow(pub(f.sik)); err != nil {
				t.Fatalf("%s: %v", c.Name, err)
			}
		}
	}

	raw, err = os.ReadFile(filepath.Join(vectorDir, "crypto_chain.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cv struct {
		Chain2 struct {
			Sealed string `json:"sealed"`
		} `json:"chain_2"`
		Meta2 struct {
			Sealed string `json:"sealed"`
		} `json:"metachain_2"`
	}
	if err := json.Unmarshal(raw, &cv); err != nil {
		t.Fatal(err)
	}
	sealed, _ := hex.DecodeString(cv.Chain2.Sealed)
	l, err := OpenChain(f.repo, 2, f.k2.Chain, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !l.HasPrev || l.PrevRMK != f.e1.RMK {
		t.Fatal("chain_2 does not link to epoch 1")
	}
	if _, err := OpenChain(f.repo, 2, f.k2.Meta, sealed); err != ErrDecrypt {
		t.Fatalf("chain_2 opened with K_meta_2: %v", err)
	}
	msealed, _ := hex.DecodeString(cv.Meta2.Sealed)
	ml, err := OpenMetaChain(f.repo, 2, f.k2.Meta, msealed)
	if err != nil {
		t.Fatal(err)
	}
	if !ml.HasPrev || ml.PrevKMeta != f.k1.Meta {
		t.Fatal("metachain_2 does not link to epoch 1")
	}
}
