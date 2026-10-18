package chunker

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

// Run `go test ./chunker/ -run TestVectors -update` to regenerate. Any
// change to the vector file is a format change and must not happen in v1.
var update = flag.Bool("update", false, "rewrite sdk/repo/testdata/v1/chunker_*.json")

const (
	vectorDir    = "../repo/testdata/v1"
	vectorFile   = "chunker_boundaries.json"
	vectorSeed   = "vaultdock"
	vectorStream = 32 << 20
)

// Vector 2: gear table excerpt and the chunk boundaries of the 32 MiB
// SHA-256-counter stream.
func vector2(t *testing.T) map[string]any {
	repo, err := vdcrypt.ParseRepoID(vdcrypttest.RepoUUID)
	if err != nil {
		t.Fatal(err)
	}
	kc := vdcrypt.DeriveRepoKeys(repo, vdcrypttest.DK).Chunker
	g := NewGear(kc)

	excerpt := map[string]string{}
	for _, i := range []int{0, 1, 2, 3, 127, 128, 254, 255} {
		excerpt[fmt.Sprintf("%03d", i)] = fmt.Sprintf("%016x", g[i])
	}
	table := make([]byte, 0, 256*8)
	for _, v := range g {
		table = binary.LittleEndian.AppendUint64(table, v)
	}
	tableSum := sha256.Sum256(table)

	data := counterStream(vectorSeed, vectorStream)
	dataSum := sha256.Sum256(data)
	var chunks []map[string]any
	off := 0
	for _, part := range g.Split(data) {
		s := sha256.Sum256(part)
		chunks = append(chunks, map[string]any{
			"offset": off,
			"length": len(part),
			"sha256": hex.EncodeToString(s[:]),
		})
		off += len(part)
	}
	return map[string]any{
		"vector":  2,
		"chunker": Name,
		"params": map[string]any{
			"min":    MinSize,
			"avg":    AvgSize,
			"max":    MaxSize,
			"mask_s": fmt.Sprintf("%016x", MaskS),
			"mask_l": fmt.Sprintf("%016x", MaskL),
		},
		"inputs": map[string]any{
			"repo_uuid": vdcrypttest.RepoUUID,
			"dk":        hex.EncodeToString(vdcrypttest.DK[:]),
			"stream":    "concatenation of SHA-256(seed || u64le(i)) for i = 0, 1, ..., truncated to length",
			"seed":      vectorSeed,
			"length":    vectorStream,
		},
		"k_chunker":          hex.EncodeToString(kc[:]),
		"gear_excerpt":       excerpt,
		"gear_table_sha256":  hex.EncodeToString(tableSum[:]),
		"gear_table_note":    "gear[i] as hex u64; table hash over gear[0..255] as u64le",
		"stream_sha256":      hex.EncodeToString(dataSum[:]),
		"chunk_count":        len(chunks),
		"chunks":             chunks,
		"cut_point_semantic": "length = index after the byte whose hash matched the mask, counted from the chunk start",
	}
}

func TestVectors(t *testing.T) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(vector2(t)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(vectorDir, vectorFile)
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
		t.Fatalf("%s: recomputed vector differs from the frozen file; the v1 format must not change", vectorFile)
	}
}

// TestVectorFileStreaming checks the frozen boundaries independently of
// the generator, through the streaming Chunker, and that K_chunker agrees
// with vector 1.
func TestVectorFileStreaming(t *testing.T) {
	if *update {
		t.Skip("update run")
	}
	raw, err := os.ReadFile(filepath.Join(vectorDir, vectorFile))
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		KChunker string `json:"k_chunker"`
		Chunks   []struct {
			Offset uint64 `json:"offset"`
			Length int    `json:"length"`
			SHA256 string `json:"sha256"`
		} `json:"chunks"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	sub, err := os.ReadFile(filepath.Join(vectorDir, "crypto_subkeys.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(sub, []byte(`"`+v.KChunker+`"`)) {
		t.Fatal("k_chunker does not appear in vector 1")
	}
	data := counterStream(vectorSeed, vectorStream)
	c := New(&randomReader{bytes.NewReader(data), newRand()}, testGear(t))
	for i, want := range v.Chunks {
		ch, err := c.Next()
		if err != nil {
			t.Fatalf("chunk %d: %v", i, err)
		}
		s := sha256.Sum256(ch.Data)
		if ch.Offset != want.Offset || ch.Length != want.Length || hex.EncodeToString(s[:]) != want.SHA256 {
			t.Fatalf("chunk %d: got %d+%d, want %d+%d", i, ch.Offset, ch.Length, want.Offset, want.Length)
		}
	}
	if _, err := c.Next(); err == nil {
		t.Fatal("extra chunk after the vector's last one")
	}
}
