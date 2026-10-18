package pack

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// Run `go test ./pack/ -run TestVectors -update` to regenerate. Any change
// to the vector files is a format change and must not happen in v1.
var update = flag.Bool("update", false, "rewrite sdk/repo/testdata/v1/pack_*.json")

const vectorDir = "../repo/testdata/v1"

func hx(b []byte) string { return hex.EncodeToString(b) }

type fixture struct {
	repo   vdcrypt.RepoID
	keys   vdcrypt.RepoKeys
	k1, k2 vdcrypt.EpochKeys
	kr     *Keyring
}

func newFixture(t testing.TB) fixture {
	t.Helper()
	var f fixture
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
	f.kr = &Keyring{Repo: f.repo, ID: f.keys.ID, Epochs: func(e uint32) (vdcrypt.EpochKeys, error) {
		switch e {
		case 1:
			return f.k1, nil
		case 2:
			return f.k2, nil
		}
		return vdcrypt.EpochKeys{}, fmt.Errorf("no epoch %d", e)
	}}
	return f
}

type blobInput struct {
	name          string
	t             BlobType
	raw           []byte
	precompressed bool
}

func rngBytes(label string, n int) []byte {
	b := make([]byte, n)
	vdcrypttest.NewRNG(label).Read(b)
	return b
}

func treeJSON(n int) []byte {
	var sb strings.Builder
	sb.WriteString(`{"v":1,"entries":[`)
	for i := range n {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"n":"file-%03d.txt","t":"f","s":%d,"m":1760745600}`, i, 1000+i)
	}
	sb.WriteString(`]}`)
	return []byte(sb.String())
}

// Inputs shared by vectors 3 and 4.
var (
	dataText  = bytes.Repeat([]byte("VaultDock vector data blob: the quick brown fox.\n"), 80)
	dataRand  = rngBytes("pack-input-random", 1024)
	dataShort = []byte("hello, vaultdock")
	treeBlob  = treeJSON(20)
	metaBlob  = []byte(`{"v":1,"kind":"inventory/1","apps":[]}`)
)

// Vector 3: sealed blobs, data under epoch 1, tree/meta under epoch 2.
func vectorBlobs(t testing.TB, f fixture) map[string]any {
	inputs := []blobInput{
		{"data-zstd", TypeData, dataText, false},
		{"data-incompressible", TypeData, dataRand, false},
		{"data-precompressed", TypeData, dataText, true},
		{"tree", TypeTree, treeBlob, false},
		{"meta", TypeMeta, metaBlob, false},
	}
	var out []any
	for _, in := range inputs {
		ek, keyName := f.k1, "k_data"
		if in.t != TypeData {
			ek, keyName = f.k2, "k_meta"
		}
		e, sealed, err := SealBlob(vdcrypttest.NewRNG("pack-v3-"+in.name), f.repo, f.keys.ID, ek, in.t, in.raw, in.precompressed)
		if err != nil {
			t.Fatal(err)
		}
		key, _ := classKey(ek, mustClass(in.t))
		payload, err := vdcrypt.Open(key, ek.Epoch, sealed, vdcrypt.BlobAD(f.repo, e.ID[:]))
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, map[string]any{
			"name": in.name, "type": int(in.t), "epoch": ek.Epoch, "key": keyName,
			"precompressed": in.precompressed,
			"raw":           hx(in.raw), "raw_len": len(in.raw),
			"blob_id": e.ID.String(), "ad": string(vdcrypt.BlobAD(f.repo, e.ID[:])),
			"comp": int(payload[0]), "payload": hx(payload),
			"sealed": hx(sealed), "sealed_len": len(sealed),
		})
	}
	return map[string]any{
		"vector": 3, "repo_uuid": vdcrypttest.RepoUUID,
		"rng":   "vdcrypttest.NewRNG(\"pack-v3-\" + name); raw of data-incompressible = NewRNG(\"pack-input-random\") 1024 bytes",
		"blobs": out,
	}
}

func mustClass(t BlobType) Class { c, _ := t.Class(); return c }

func buildPack(t testing.TB, f fixture, c Class, label string, inputs []blobInput) *Pack {
	t.Helper()
	ek := f.k1
	if c == ClassMeta {
		ek = f.k2
	}
	w, err := NewWriter(f.repo, f.keys.ID, ek, c, WriterOptions{Rand: vdcrypttest.NewRNG(label)})
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range inputs {
		if _, err := w.Add(in.t, in.raw, in.precompressed); err != nil {
			t.Fatal(err)
		}
	}
	p, err := w.Finish()
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func dataPackInputs() []blobInput {
	return []blobInput{
		{"data-zstd", TypeData, dataText, false},
		{"data-incompressible", TypeData, dataRand, false},
		{"data-short", TypeData, dataShort, false},
	}
}

func metaPackInputs() []blobInput {
	return []blobInput{
		{"tree", TypeTree, treeBlob, false},
		{"meta", TypeMeta, metaBlob, false},
	}
}

func packVector(t testing.TB, f fixture, c Class, label string, inputs []blobInput) map[string]any {
	p := buildPack(t, f, c, label, inputs)
	b := p.Bytes
	tr := b[len(b)-TrailerSize:]
	l := int(binary.LittleEndian.Uint32(tr[:4]))
	region := b[len(b)-TrailerSize-l : len(b)-TrailerSize]
	hdr := []byte{byte(p.Class)}
	hdr = binary.LittleEndian.AppendUint32(hdr, uint32(len(p.Entries)))
	var entries, ins []any
	for _, e := range p.Entries {
		hdr = appendEntry(hdr, e)
		entries = append(entries, map[string]any{"t": int(e.Type), "id": e.ID.String(), "o": e.Offset, "l": e.Length, "r": e.RawLength})
	}
	for _, in := range inputs {
		ins = append(ins, map[string]any{"name": in.name, "type": int(in.t), "raw": hx(in.raw), "precompressed": in.precompressed})
	}
	return map[string]any{
		"class": int(p.Class), "epoch": p.Epoch, "rng": label,
		"inputs":        ins,
		"entries":       entries,
		"pack_nonce":    hx(region[:NonceSize]),
		"header_plain":  hx(hdr),
		"sealed_header": hx(region[NonceSize:]),
		"trailer":       hx(tr),
		"size":          len(b),
		"oid":           p.OID,
		"pack":          hx(b),
	}
}

// Vector 4: one data pack (epoch 1) and one meta pack (epoch 2).
func vectorPacks(t testing.TB, f fixture) map[string]any {
	return map[string]any{
		"vector": 4, "repo_uuid": vdcrypttest.RepoUUID,
		"note":      "inputs are listed in Add order; entries are in pack order after the shuffle",
		"data_pack": packVector(t, f, ClassData, "pack-v4-data", dataPackInputs()),
		"meta_pack": packVector(t, f, ClassMeta, "pack-v4-meta", metaPackInputs()),
	}
}

func TestVectors(t *testing.T) {
	f := newFixture(t)
	vectors := []struct {
		file string
		gen  func() map[string]any
	}{
		{"pack_blobs.json", func() map[string]any { return vectorBlobs(t, f) }},
		{"pack_packs.json", func() map[string]any { return vectorPacks(t, f) }},
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

func unhex(t testing.TB, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestVectorFilesOpen reads the frozen files independently of the
// generators: every sealed blob opens to its raw input and every pack opens
// through the Reader with the recorded oid and entries.
func TestVectorFilesOpen(t *testing.T) {
	if *update {
		t.Skip("updating")
	}
	f := newFixture(t)

	var v3 struct {
		Blobs []struct {
			Name   string `json:"name"`
			Type   int    `json:"type"`
			Raw    string `json:"raw"`
			ID     string `json:"blob_id"`
			Sealed string `json:"sealed"`
		} `json:"blobs"`
	}
	readJSON(t, "pack_blobs.json", &v3)
	if len(v3.Blobs) != 5 {
		t.Fatalf("vector 3 has %d blobs", len(v3.Blobs))
	}
	for _, b := range v3.Blobs {
		var e Entry
		e.Type = BlobType(b.Type)
		copy(e.ID[:], unhex(t, b.ID))
		raw := unhex(t, b.Raw)
		sealed := unhex(t, b.Sealed)
		e.Length, e.RawLength = uint32(len(sealed)), uint32(len(raw))
		got, err := OpenBlob(f.kr, e, sealed)
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatalf("%s: open: %v", b.Name, err)
		}
	}

	type packJSON struct {
		Class   int    `json:"class"`
		OID     string `json:"oid"`
		Pack    string `json:"pack"`
		Entries []struct {
			T  int    `json:"t"`
			ID string `json:"id"`
			O  uint32 `json:"o"`
			L  uint32 `json:"l"`
			R  uint32 `json:"r"`
		} `json:"entries"`
	}
	var v4 struct {
		Data packJSON `json:"data_pack"`
		Meta packJSON `json:"meta_pack"`
	}
	readJSON(t, "pack_packs.json", &v4)
	for _, pj := range []packJSON{v4.Data, v4.Meta} {
		b := unhex(t, pj.Pack)
		if vdcrypt.OID(b) != pj.OID {
			t.Fatalf("oid mismatch")
		}
		pr, err := NewReader(bytes.NewReader(b), int64(len(b)), f.kr, Class(pj.Class))
		if err != nil {
			t.Fatal(err)
		}
		if len(pr.Entries) != len(pj.Entries) {
			t.Fatalf("entries: %d vs %d", len(pr.Entries), len(pj.Entries))
		}
		for i, je := range pj.Entries {
			e := pr.Entries[i]
			if int(e.Type) != je.T || e.ID.String() != je.ID || e.Offset != je.O || e.Length != je.L || e.RawLength != je.R {
				t.Fatalf("entry %d mismatch: %+v vs %+v", i, e, je)
			}
			if _, err := pr.Blob(e); err != nil {
				t.Fatalf("entry %d: %v", i, err)
			}
		}
	}
}

func readJSON(t testing.TB, name string, v any) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(vectorDir, name))
	if err != nil {
		t.Fatalf("missing vector file (run with -update once): %v", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatal(err)
	}
}
