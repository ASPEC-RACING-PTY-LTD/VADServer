package snapshot

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func fileEntry(name string) Entry {
	return Entry{N: name, T: TypeFile, S: 1, M: 1, C: 1, A: 32, H: HexPtr(strings.Repeat("1", 64)),
		Ch: []string{strings.Repeat("2", 64)}}
}

func TestEncodeTreesSortsAndRoundTrips(t *testing.T) {
	f := newFixture(t)
	in := []Entry{fileEntry("b"), fileEntry("B"), fileEntry("ä"), fileEntry("a"),
		{N: "dir", T: TypeDir, Sub: HexPtr(strings.Repeat("3", 64))},
		{N: "link", T: TypeSymlink, Ln: HexPtr(`..\target`)}}
	blobs, err := EncodeTrees(f.keys.ID, in)
	if err != nil || len(blobs) != 1 {
		t.Fatal(len(blobs), err)
	}
	if blobs[0].ID != BlobID(f.keys.ID, blobs[0].Raw) {
		t.Fatal("blob id")
	}
	tr, err := DecodeTree(blobs[0].Raw)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range tr.Entries {
		names = append(names, e.N)
	}
	if got := strings.Join(names, ","); got != "B,a,b,dir,link,ä" {
		t.Fatalf("order %s", got)
	}
	if in[0].N != "b" {
		t.Fatal("input modified")
	}
	// Deterministic bytes: same input, same blob id (dedup).
	again, _ := EncodeTrees(f.keys.ID, in)
	if again[0].ID != blobs[0].ID {
		t.Fatal("not deterministic")
	}
	// HTML characters are not escaped.
	odd, _ := EncodeTrees(f.keys.ID, []Entry{fileEntry("<a&b>")})
	if !bytes.Contains(odd[0].Raw, []byte(`"n":"<a&b>"`)) {
		t.Fatalf("%s", odd[0].Raw)
	}
}

func TestEncodeTreesSplit(t *testing.T) {
	f := newFixture(t)
	var in []Entry
	for i := 0; i < MaxTreeEntries*2+5; i++ {
		e := Entry{N: fmt.Sprintf("f%06d", i), T: TypeFile, H: HexPtr(strings.Repeat("1", 64))}
		in = append(in, e)
	}
	blobs, err := EncodeTrees(f.keys.ID, in)
	if err != nil || len(blobs) != 3 {
		t.Fatal(len(blobs), err)
	}
	total := 0
	for i, b := range blobs {
		tr, err := DecodeTree(b.Raw)
		if err != nil {
			t.Fatal(err)
		}
		total += len(tr.Entries)
		if i < 2 && (tr.Cont == nil || *tr.Cont != blobs[i+1].IDHex()) {
			t.Fatalf("blob %d cont", i)
		}
		if i == 2 && tr.Cont != nil {
			t.Fatal("last blob has cont")
		}
		if tr.Entries[0].Ch == nil {
			t.Fatal("empty file ch must be []")
		}
	}
	if total != len(in) {
		t.Fatal(total)
	}
	empty, err := EncodeTrees(f.keys.ID, nil)
	if err != nil || len(empty) != 1 || !bytes.Equal(empty[0].Raw, []byte(`{"v":1,"entries":[],"cont":null}`)) {
		t.Fatalf("%s %v", empty[0].Raw, err)
	}
}

func TestTreeRejects(t *testing.T) {
	f := newFixture(t)
	bad := map[string][]Entry{
		"duplicate":  {fileEntry("a"), fileEntry("a")},
		"dotdot":     {fileEntry("..")},
		"slash":      {fileEntry("a/b")},
		"backslash":  {fileEntry(`a\b`)},
		"ads colon":  {fileEntry("a:b")},
		"empty":      {fileEntry("")},
		"bad type":   {{N: "x", T: "z"}},
		"file no h":  {{N: "x", T: TypeFile}},
		"bad ch":     {{N: "x", T: TypeFile, H: HexPtr(strings.Repeat("1", 64)), Ch: []string{"zz"}}},
		"bad nb":     {{N: "x", T: TypeDir, NB: HexPtr("YQ")}},
		"negative s": {{N: "x", T: TypeDir, S: -1}},
	}
	for name, entries := range bad {
		if _, err := EncodeTrees(f.keys.ID, entries); !errors.Is(err, ErrTree) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, raw := range map[string]string{
		"unsorted": `{"v":1,"entries":[{"n":"b","t":"d"},{"n":"a","t":"d"}],"cont":null}`,
		"version":  `{"v":2,"entries":[],"cont":null}`,
		"null":     `{"v":1,"entries":null,"cont":null}`,
		"cont":     `{"v":1,"entries":[],"cont":"x"}`,
		"json":     `{"v":1,`,
	} {
		if _, err := DecodeTree([]byte(raw)); !errors.Is(err, ErrTree) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Names that differ only in their raw UTF-16 form are distinct.
	nb1, nb2 := b64([]byte{0x00, 0xd8}), b64([]byte{0x01, 0xd8})
	if _, err := EncodeTrees(f.keys.ID, []Entry{{N: "�", NB: &nb1, T: TypeDir}, {N: "�", NB: &nb2, T: TypeDir}}); err != nil {
		t.Fatal(err)
	}
}

func TestMetaBlobs(t *testing.T) {
	inv := &Inventory{Schema: SchemaInventory, Apps: []App{{Name: "7-Zip", Source: "winget", Tier: 1,
		DataRoots: []string{"{APPDATA}/7-Zip"}}}}
	raw, err := EncodeMeta(inv)
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := MetaSchema(raw); s != SchemaInventory {
		t.Fatal(s)
	}
	got, err := DecodeInventory(raw)
	if err != nil || got.Apps[0].DataRoots[0] != "{APPDATA}/7-Zip" {
		t.Fatal(err)
	}
	reg := &Registry{Schema: SchemaRegistry, Hive: "HKCU", Key: `Software\Vendor\App`,
		Values:  []RegistryValue{{N: "", Type: "REG_DWORD", D: []byte{1, 0, 0, 0}}},
		Subkeys: []Registry{{Key: "Sub"}}}
	raw, err = EncodeMeta(reg)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"d":"AQAAAA=="`)) {
		t.Fatalf("%s", raw)
	}
	if r, err := DecodeRegistry(raw); err != nil || r.Subkeys[0].Key != "Sub" {
		t.Fatal(err)
	}
	if _, err := DecodeInventory(raw); !errors.Is(err, ErrMeta) {
		t.Fatal("schema mismatch accepted")
	}
	if _, err := EncodeMeta(&Inventory{Schema: "inventory/9"}); !errors.Is(err, ErrMeta) {
		t.Fatal("unknown schema accepted")
	}
}
