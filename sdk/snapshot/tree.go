package snapshot

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/compress"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
)

// MaxTreeEntries is the entry count at which a directory listing is split
// into continuation tree blobs (§8.2).
const MaxTreeEntries = 50000

// Entry types ("t").
const (
	TypeFile    = "f"
	TypeDir     = "d"
	TypeSymlink = "l"
	TypeReparse = "r"
)

// ErrTree reports a malformed tree blob.
var ErrTree = errors.New("snapshot: invalid tree blob")

// Tree is the raw JSON of a tree blob (blob type 1, §8.2).
type Tree struct {
	V       int     `json:"v"`
	Entries []Entry `json:"entries"`
	Cont    *string `json:"cont"`
}

// Entry is one directory entry. Times are Unix nanoseconds; A holds the
// Windows FILE_ATTRIBUTE_* bits. H is the keyed whole-file hash
// HMAC-SHA256(K_fh, file) in hex (files only). Security descriptors are
// never recorded.
type Entry struct {
	N   string   `json:"n"`
	NB  *string  `json:"nb"`
	T   string   `json:"t"`
	S   int64    `json:"s"`
	M   int64    `json:"m"`
	C   int64    `json:"c"`
	A   uint32   `json:"a"`
	H   *string  `json:"h"`
	Ch  []string `json:"ch"`
	Sub *string  `json:"sub"`
	Ln  *string  `json:"ln"`
	X   *Ext     `json:"x,omitempty"`
}

// Ext holds the optional extensions; readers ignore unknown keys.
type Ext struct {
	ADS         []ADS `json:"ads,omitempty"`
	Placeholder bool  `json:"placeholder,omitempty"`
}

// ADS is an alternate data stream of a file.
type ADS struct {
	Name string   `json:"name"`
	Ch   []string `json:"ch"`
	S    int64    `json:"s"`
}

// TreeBlob is one encoded tree blob: its raw plaintext and its blob id
// HMAC-SHA256(K_id, raw). Hand Raw to the meta pack writer as type 1.
type TreeBlob struct {
	ID  [32]byte
	Raw []byte
}

// IDHex returns the blob id as lowercase hex, as trees and roots store it.
func (b TreeBlob) IDHex() string { return hex.EncodeToString(b.ID[:]) }

// HexPtr returns a pointer to s, for the nullable hex fields.
func HexPtr(s string) *string { return &s }

// entryLess orders entries bytewise by n, then by nb for names that only
// differ in their raw UTF-16 form.
func entryLess(a, b *Entry) bool {
	if a.N != b.N {
		return a.N < b.N
	}
	return nbOf(a) < nbOf(b)
}

func nbOf(e *Entry) string {
	if e.NB == nil {
		return ""
	}
	return *e.NB
}

func validName(n string) bool {
	return n != "" && n != "." && n != ".." && utf8.ValidString(n) &&
		!strings.ContainsAny(n, "/\\:\x00")
}

func validHexPtr(p *string) bool { return p == nil || isHexID(*p) }

func (e *Entry) validate() error {
	bad := func(what string) error { return fmt.Errorf("%w: entry %q: %s", ErrTree, e.N, what) }
	if !validName(e.N) {
		return bad("n")
	}
	if e.NB != nil {
		raw, err := base64.StdEncoding.Strict().DecodeString(*e.NB)
		if err != nil || len(raw) == 0 || len(raw)%2 != 0 {
			return bad("nb")
		}
	}
	switch e.T {
	case TypeFile, TypeDir, TypeSymlink, TypeReparse:
	default:
		return bad("t")
	}
	if e.S < 0 {
		return bad("s")
	}
	if !validHexPtr(e.H) || !validHexPtr(e.Sub) {
		return bad("h/sub")
	}
	if e.T == TypeFile && e.H == nil {
		return bad("file without h")
	}
	for _, id := range e.Ch {
		if !isHexID(id) {
			return bad("ch")
		}
	}
	if e.X != nil {
		for _, a := range e.X.ADS {
			if a.Name == "" || a.S < 0 {
				return bad("x.ads")
			}
			for _, id := range a.Ch {
				if !isHexID(id) {
					return bad("x.ads.ch")
				}
			}
		}
	}
	return nil
}

func encodeTree(t *Tree) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(t); err != nil {
		return nil, err
	}
	raw := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	if len(raw) > compress.MaxMetaRaw {
		return nil, fmt.Errorf("%w: tree blob exceeds %d bytes", ErrTree, compress.MaxMetaRaw)
	}
	return raw, nil
}

// EncodeTrees encodes one directory listing. Entries are sorted bytewise by
// n and split into blobs of at most MaxTreeEntries chained through cont.
// The first returned blob is the head (the id that roots or a parent's sub
// reference). kid is RepoKeys.ID. The input slice is not modified.
func EncodeTrees(kid vdcrypt.Key, entries []Entry) ([]TreeBlob, error) {
	sorted := append([]Entry(nil), entries...)
	sort.SliceStable(sorted, func(i, j int) bool { return entryLess(&sorted[i], &sorted[j]) })
	for i := range sorted {
		if sorted[i].Ch == nil && sorted[i].T == TypeFile {
			sorted[i].Ch = []string{} // [] for an empty file
		}
		if err := sorted[i].validate(); err != nil {
			return nil, err
		}
		if i > 0 && !entryLess(&sorted[i-1], &sorted[i]) {
			return nil, fmt.Errorf("%w: duplicate name %q", ErrTree, sorted[i].N)
		}
	}
	n := (len(sorted) + MaxTreeEntries - 1) / MaxTreeEntries
	if n == 0 {
		n = 1
	}
	blobs := make([]TreeBlob, n)
	var cont *string
	for i := n - 1; i >= 0; i-- {
		lo, hi := i*MaxTreeEntries, min((i+1)*MaxTreeEntries, len(sorted))
		t := Tree{V: Version, Entries: sorted[lo:hi], Cont: cont}
		if t.Entries == nil {
			t.Entries = []Entry{}
		}
		raw, err := encodeTree(&t)
		if err != nil {
			return nil, err
		}
		blobs[i] = TreeBlob{ID: BlobID(kid, raw), Raw: raw}
		cont = HexPtr(blobs[i].IDHex())
	}
	return blobs, nil
}

// DecodeTree parses and validates one tree blob's raw plaintext. Follow
// Cont (if non-nil) to read the rest of the directory.
func DecodeTree(raw []byte) (*Tree, error) {
	if len(raw) > compress.MaxMetaRaw {
		return nil, ErrTree
	}
	var t Tree
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTree, err)
	}
	if t.V != Version || t.Entries == nil || len(t.Entries) > MaxTreeEntries || !validHexPtr(t.Cont) {
		return nil, ErrTree
	}
	for i := range t.Entries {
		if err := t.Entries[i].validate(); err != nil {
			return nil, err
		}
		if i > 0 && !entryLess(&t.Entries[i-1], &t.Entries[i]) {
			return nil, fmt.Errorf("%w: entries not sorted or duplicated", ErrTree)
		}
	}
	return &t, nil
}
