package snapshot

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt/vdcrypttest"
)

// Run `go test ./snapshot/ -run TestVector5 -update` to regenerate. Any
// change to the vector file is a format change and must not happen in v1.
var update = flag.Bool("update", false, "rewrite sdk/repo/testdata/v1/snapshot_root.json")

const (
	vectorDir  = "../repo/testdata/v1"
	vectorFile = "snapshot_root.json"
	vectorRNG  = "snapshot_root"
)

func hx(b []byte) string { return hex.EncodeToString(b) }

// vector5 builds the snapshot part of §13 vector 5: keyed file hashes, a
// tree with a subtree, and a signed root sealed under K_meta_2. (The index
// half of vector 5 belongs to sdk/repo, P1-07.)
func vector5(t *testing.T, f fixture) map[string]any {
	t.Helper()
	hello := []byte("hello")
	hHello := FileHashBytes(f.keys.FileHash, hello)
	hEmpty := FileHashBytes(f.keys.FileHash, nil)
	chunk := BlobID(f.keys.ID, hello) // the "hello" data blob id

	sub, err := EncodeTrees(f.keys.ID, []Entry{
		{N: "empty.txt", T: TypeFile, M: 1760745600000000000, C: 1760745600000000000, A: 32, H: HexPtr(hx(hEmpty[:]))},
	})
	if err != nil {
		t.Fatal(err)
	}
	top, err := EncodeTrees(f.keys.ID, []Entry{
		{N: "report.docx", T: TypeFile, S: 5, M: 1760745600123456789, C: 1760745600000000000, A: 32,
			H: HexPtr(hx(hHello[:])), Ch: []string{hx(chunk[:])},
			X: &Ext{ADS: []ADS{{Name: "Zone.Identifier", Ch: []string{hx(chunk[:])}, S: 5}}}},
		{N: "Ünïcödé 😀", T: TypeDir, M: 1760745600000000000, C: 1760745600000000000, A: 16, Sub: HexPtr(sub[0].IDHex())},
	})
	if err != nil {
		t.Fatal(err)
	}

	r := testRoot(f, 2, top[0].IDHex())
	if err := r.Sign(f.sign); err != nil {
		t.Fatal(err)
	}
	signing, err := r.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	stored, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := SealRoot(vdcrypttest.NewRNG(vectorRNG), f.repo, f.k2, r)
	if err != nil {
		t.Fatal(err)
	}
	ad, _ := vdcrypt.ObjectAD(vdcrypt.PurposeSnapshot, f.repo)
	return map[string]any{
		"vector":    5,
		"part":      "snapshot",
		"repo_uuid": vdcrypttest.RepoUUID,
		"note":      "zstd payload bytes depend on klauspost/compress level 3; readers must only require that sealed opens to root_json",
		"inputs": map[string]any{
			"dk": hx(vdcrypttest.DK[:]), "rmk_2": hx(vdcrypttest.RMK2[:]),
			"device_sign_seed": hx(vdcrypttest.DeviceSignSeed[:]), "rng_label": vectorRNG,
		},
		"k_fh":   hx(f.keys.FileHash[:]),
		"k_id":   hx(f.keys.ID[:]),
		"k_meta": hx(f.k2.Meta[:]),
		"file_hashes": []any{
			map[string]any{"content": "hello", "h": hx(hHello[:])},
			map[string]any{"content": "", "h": hx(hEmpty[:])},
		},
		"trees": []any{
			map[string]any{"id": top[0].IDHex(), "raw": string(top[0].Raw)},
			map[string]any{"id": sub[0].IDHex(), "raw": string(sub[0].Raw)},
		},
		"device_sign_pub": hx(f.signPub()),
		"signing_bytes":   string(signing),
		"sig":             r.Sig,
		"root_json":       string(stored),
		"epoch":           2,
		"ad":              string(ad),
		"sealed":          hx(sealed),
		"oid":             vdcrypt.OID(sealed),
	}
}

func TestVector5(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(vector5(t, f)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vectorDir, vectorFile)
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
		t.Fatalf("%s: recomputed vector differs from the frozen file; the v1 format must not change", vectorFile)
	}
}

// TestVector5FileOpens checks the frozen file independently of the
// generator: the sealed root opens with K_meta_2, verifies with the device
// key, and the stored trees and hashes recompute.
func TestVector5FileOpens(t *testing.T) {
	if *update {
		t.Skip("updating")
	}
	f := newFixture(t)
	raw, err := os.ReadFile(filepath.Join(vectorDir, vectorFile))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		FileHashes []struct{ Content, H string } `json:"file_hashes"`
		Trees      []struct{ ID, Raw string }    `json:"trees"`
		Signing    string                        `json:"signing_bytes"`
		RootJSON   string                        `json:"root_json"`
		Sealed     string                        `json:"sealed"`
		OID        string                        `json:"oid"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	for _, fh := range v.FileHashes {
		if !VerifyFileHash(FileHashBytes(f.keys.FileHash, []byte(fh.Content)), fh.H) {
			t.Fatalf("file hash of %q", fh.Content)
		}
	}
	for _, tr := range v.Trees {
		id := BlobID(f.keys.ID, []byte(tr.Raw))
		if hx(id[:]) != tr.ID {
			t.Fatal("tree id")
		}
		if _, err := DecodeTree([]byte(tr.Raw)); err != nil {
			t.Fatal(err)
		}
	}
	sealed, _ := hex.DecodeString(v.Sealed)
	if vdcrypt.OID(sealed) != v.OID {
		t.Fatal("oid")
	}
	root, js, err := OpenRoot(f.repo, map[uint32]vdcrypt.Key{2: f.k2.Meta}, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if string(js) != v.RootJSON || root.Roots[0].Tree != v.Trees[0].ID {
		t.Fatal("root json")
	}
	if err := VerifyRoot(js, f.signPub()); err != nil {
		t.Fatal(err)
	}
	if c, _, _ := stripMember(js, "sig"); string(c) != v.Signing {
		t.Fatal("signing bytes")
	}
	if _, _, err := OpenRoot(f.repo, map[uint32]vdcrypt.Key{2: f.k1.Meta}, sealed); err == nil {
		t.Fatal("opened with the wrong epoch key")
	}
}
