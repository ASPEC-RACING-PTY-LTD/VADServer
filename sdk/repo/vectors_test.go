package repo

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/pack"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// Run `go test ./repo/ -run TestVectors -update` to regenerate. Any change
// to a vector file is a format change and must not happen in v1.
var update = flag.Bool("update", false, "rewrite sdk/repo/testdata/v1/{config_mac,index}.json")

const (
	vectorDir       = "testdata/v1"
	configVector    = "config_mac.json"
	indexVector     = "index.json"
	vectorCreatedMS = 1760745600000
)

func hx(b []byte) string { return hex.EncodeToString(b) }

type vfix struct {
	repo   vdcrypt.RepoID
	keys   vdcrypt.RepoKeys
	k1, k2 vdcrypt.EpochKeys
}

func newVfix(t *testing.T) vfix {
	t.Helper()
	var f vfix
	var err error
	if f.repo, err = vdcrypt.ParseRepoID(vdcrypttest.RepoUUID); err != nil {
		t.Fatal(err)
	}
	f.keys = vdcrypt.DeriveRepoKeys(f.repo, vdcrypttest.DK)
	if f.k1, err = vdcrypt.DeriveEpochKeys(f.repo, 1, vdcrypttest.RMK1); err != nil {
		t.Fatal(err)
	}
	if f.k2, err = vdcrypt.DeriveEpochKeys(f.repo, 2, vdcrypttest.RMK2); err != nil {
		t.Fatal(err)
	}
	return f
}

// vector9 is §13 vector 9: the config mac for both escrow modes.
func vector9(t *testing.T, f vfix) map[string]any {
	t.Helper()
	_, recPub, err := vdcrypt.RecoveryKeyPair(f.repo, vdcrypttest.RecoverySecret)
	if err != nil {
		t.Fatal(err)
	}
	var cases []any
	for _, mode := range []string{EscrowFull, EscrowMetadata} {
		c := NewConfig(f.repo, vectorCreatedMS, mode, recPub)
		stored, err := c.Encode(f.keys.ConfigMAC)
		if err != nil {
			t.Fatal(err)
		}
		in, err := ConfigMACInput(stored)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, map[string]any{
			"escrow_mode": mode, "mac_input": string(in), "mac": c.MAC, "config": string(stored),
		})
	}
	return map[string]any{
		"vector":    9,
		"repo_uuid": vdcrypttest.RepoUUID,
		"note":      "mac = hex(HMAC-SHA256(K_mac, JCS(config without mac))); config is the stored bytes; created is Unix ms",
		"inputs": map[string]any{
			"dk": hx(vdcrypttest.DK[:]), "recovery_secret": hx(vdcrypttest.RecoverySecret[:]), "created": vectorCreatedMS,
		},
		"k_mac":        hx(f.keys.ConfigMAC[:]),
		"recovery_pub": hx(recPub[:]),
		"cases":        cases,
	}
}

// packVector is the part of vector 4 (pack_packs.json) the index uses.
type packVector struct {
	OID     string `json:"oid"`
	Class   uint8  `json:"class"`
	Epoch   uint32 `json:"epoch"`
	Pack    string `json:"pack"`
	Entries []struct {
		ID string `json:"id"`
		T  uint8  `json:"t"`
		O  uint32 `json:"o"`
		L  uint32 `json:"l"`
		R  uint32 `json:"r"`
	} `json:"entries"`
	Inputs []struct {
		Raw string `json:"raw"`
	} `json:"inputs"`
}

func loadPackVectors(t *testing.T) (data, meta packVector) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(vectorDir, "pack_packs.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Data packVector `json:"data_pack"`
		Meta packVector `json:"meta_pack"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	return v.Data, v.Meta
}

func (p packVector) indexPack() IndexPack {
	ip := IndexPack{Pack: p.OID, Class: pack.Class(p.Class)}
	for _, e := range p.Entries {
		ip.Blobs = append(ip.Blobs, IndexBlob{ID: e.ID, T: pack.BlobType(e.T), O: e.O, L: e.L, R: e.R})
	}
	return ip
}

// vector5Index is the index half of §13 vector 5, built over the vector 4
// packs: index_1 (epoch 1, data pack) and index_2 (epoch 2, both packs,
// superseding index_1).
func vector5Index(t *testing.T, f vfix) map[string]any {
	t.Helper()
	dp, mp := loadPackVectors(t)
	ad, _ := vdcrypt.ObjectAD(vdcrypt.PurposeIndex, f.repo)
	one := func(label string, ek vdcrypt.EpochKeys, idx *IndexFile) (map[string]any, string) {
		js, err := EncodeIndexJSON(idx)
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := SealIndex(vdcrypttest.NewRNG(label), f.repo, ek, idx)
		if err != nil {
			t.Fatal(err)
		}
		oid := vdcrypt.OID(sealed)
		return map[string]any{"rng": label, "epoch": ek.Epoch, "json": string(js), "ad": string(ad),
			"sealed": hx(sealed), "oid": oid}, oid
	}
	i1, oid1 := one("repo-index-1", f.k1, &IndexFile{V: 1, Packs: []IndexPack{dp.indexPack()}})
	i2, _ := one("repo-index-2", f.k2, &IndexFile{V: 1, Supersedes: []string{oid1}, Packs: []IndexPack{dp.indexPack(), mp.indexPack()}})
	return map[string]any{
		"vector":    5,
		"part":      "index",
		"repo_uuid": vdcrypttest.RepoUUID,
		"note":      "index = seal(K_meta_e, 0x01 || zstd(JSON)); packs and entries are those of vector 4 (pack_packs.json); zstd bytes depend on klauspost/compress level 3, so readers must only require that sealed opens to json",
		"inputs":    map[string]any{"dk": hx(vdcrypttest.DK[:]), "rmk_1": hx(vdcrypttest.RMK1[:]), "rmk_2": hx(vdcrypttest.RMK2[:])},
		"k_meta_1":  hx(f.k1.Meta[:]),
		"k_meta_2":  hx(f.k2.Meta[:]),
		"indexes":   []any{i1, i2},
	}
}

func checkVector(t *testing.T, file string, v any) {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vectorDir, file)
	if *update {
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
		t.Fatalf("%s: recomputed vector differs from the frozen file; the v1 format must not change", file)
	}
}

func TestVectors(t *testing.T) {
	f := newVfix(t)
	checkVector(t, configVector, vector9(t, f))
	checkVector(t, indexVector, vector5Index(t, f))
}

// TestVectorFilesOpen checks the frozen files independently of the
// generators: the configs verify (and fail with another K_mac), and the
// sealed indexes open, match their JSON, and locate the vector 4 blobs.
func TestVectorFilesOpen(t *testing.T) {
	if *update {
		t.Skip("updating")
	}
	f := newVfix(t)

	raw, err := os.ReadFile(filepath.Join(vectorDir, configVector))
	if err != nil {
		t.Fatal(err)
	}
	var v9 struct {
		Cases []struct {
			Mode   string `json:"escrow_mode"`
			Input  string `json:"mac_input"`
			MAC    string `json:"mac"`
			Config string `json:"config"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &v9); err != nil || len(v9.Cases) != 2 {
		t.Fatalf("config vector: %v", err)
	}
	other := vdcrypt.DeriveRepoKeys(f.repo, vdcrypttest.RMK1).ConfigMAC
	for _, c := range v9.Cases {
		if err := VerifyConfig(f.keys.ConfigMAC, []byte(c.Config)); err != nil {
			t.Fatalf("%s: %v", c.Mode, err)
		}
		if err := VerifyConfig(other, []byte(c.Config)); err != ErrConfigMAC {
			t.Fatalf("%s: verified with the wrong K_mac", c.Mode)
		}
		cfg, _ := ParseConfig([]byte(c.Config))
		if in, _ := ConfigMACInput([]byte(c.Config)); string(in) != c.Input || cfg.MAC != c.MAC || cfg.EscrowMode != c.Mode {
			t.Fatalf("%s: mac input", c.Mode)
		}
	}

	raw, err = os.ReadFile(filepath.Join(vectorDir, indexVector))
	if err != nil {
		t.Fatal(err)
	}
	var v5 struct {
		Indexes []struct {
			Epoch  uint32 `json:"epoch"`
			JSON   string `json:"json"`
			Sealed string `json:"sealed"`
			OID    string `json:"oid"`
		} `json:"indexes"`
	}
	if err := json.Unmarshal(raw, &v5); err != nil || len(v5.Indexes) != 2 {
		t.Fatalf("index vector: %v", err)
	}
	mk := map[uint32]vdcrypt.Key{1: f.k1.Meta, 2: f.k2.Meta}
	ix := NewIndex()
	for _, i := range v5.Indexes {
		sealed, _ := hex.DecodeString(i.Sealed)
		if vdcrypt.OID(sealed) != i.OID {
			t.Fatal("index oid")
		}
		idx, js, err := OpenIndex(f.repo, mk, sealed)
		if err != nil {
			t.Fatal(err)
		}
		if string(js) != i.JSON {
			t.Fatal("index json")
		}
		if e, _ := vdcrypt.EnvelopeEpoch(sealed); e != i.Epoch {
			t.Fatal("index epoch")
		}
		wrong := map[uint32]vdcrypt.Key{i.Epoch: f.k1.Data}
		if _, _, err := OpenIndex(f.repo, wrong, sealed); err == nil {
			t.Fatal("index opened with the wrong key")
		}
		for _, p := range idx.Packs {
			if err := ix.AddPack(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if f2, _, _ := OpenIndex(f.repo, mk, mustHex(t, v5.Indexes[1].Sealed)); len(f2.Supersedes) != 1 || f2.Supersedes[0] != v5.Indexes[0].OID {
		t.Fatal("supersedes")
	}

	// Every vector 4 input blob is found through the index and reads back
	// from the frozen pack bytes with a ranged read.
	kr := &pack.Keyring{Repo: f.repo, ID: f.keys.ID, Epochs: func(e uint32) (vdcrypt.EpochKeys, error) {
		return map[uint32]vdcrypt.EpochKeys{1: f.k1, 2: f.k2}[e], nil
	}}
	dp, mp := loadPackVectors(t)
	for _, pv := range []packVector{dp, mp} {
		pb := mustHex(t, pv.Pack)
		for _, in := range pv.Inputs {
			want := mustHex(t, in.Raw)
			loc, ok := ix.Lookup(pack.ComputeID(f.keys.ID, want))
			if !ok || loc.Pack != pv.OID {
				t.Fatal("blob not indexed")
			}
			got, err := pack.ReadBlob(bytes.NewReader(pb), kr, loc.Entry)
			if err != nil || !bytes.Equal(got, want) {
				t.Fatalf("read blob: %v", err)
			}
		}
	}
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
