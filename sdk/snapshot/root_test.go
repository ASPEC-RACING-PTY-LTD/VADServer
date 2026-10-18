package snapshot

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

type fixture struct {
	repo   vdcrypt.RepoID
	keys   vdcrypt.RepoKeys
	k1, k2 vdcrypt.EpochKeys
	sign   ed25519.PrivateKey
}

func newFixture(t *testing.T) fixture {
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
	f.sign = ed25519.NewKeyFromSeed(vdcrypttest.DeviceSignSeed[:])
	return f
}

func (f fixture) signPub() ed25519.PublicKey { return f.sign.Public().(ed25519.PublicKey) }

func testRoot(f fixture, epoch uint32, tree string) *Root {
	return &Root{
		V: Version, Repo: f.repo.String(), Time: 1760745600000,
		Job:    Job{ID: 12, Name: "Documents", Kind: "folder", Mode: "full"},
		Device: Device{ID: 3, Name: "DESKTOP-ABC", KeyID: "00112233445566778899aabbccddeeff"},
		Host: Host{Hostname: "DESKTOP-ABC", OS: "Windows 11 Pro 10.0.26300", User: "ali",
			SID: "S-1-5-21-1000", MachineGUIDHash: strings.Repeat("ab", 32)},
		Tokens:        Tokens{TokenProfile: `C:\Users\ali`, TokenDocuments: `D:\OneDrive\Documents`},
		Roots:         []RootRef{{TokenPath: TokenDocuments, Tree: tree}},
		Stats:         Stats{Files: 1, Dirs: 2, Bytes: 5},
		Tags:          []string{"daily"},
		IssuesSummary: IssuesSummary{Skipped: 1, ByClass: map[string]int64{"locked": 1}},
		Epoch:         epoch,
		Profile:       Profile{SID: "S-1-5-21-1000", Username: "ali", ConsentID: "consent-1"},
		InstallMode:   InstallModeSimple,
	}
}

func signedRootJSON(t *testing.T, f fixture) []byte {
	t.Helper()
	r := testRoot(f, 1, strings.Repeat("0", 64))
	if err := r.Sign(f.sign); err != nil {
		t.Fatal(err)
	}
	js, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return js
}

func TestRootSignVerify(t *testing.T) {
	f := newFixture(t)
	js := signedRootJSON(t, f)
	if err := VerifyRoot(js, f.signPub()); err != nil {
		t.Fatal(err)
	}
	// Signing bytes are the root without sig, canonical.
	r, err := DecodeRoot(js)
	if err != nil {
		t.Fatal(err)
	}
	msg, _ := r.SigningBytes()
	if bytes.Contains(msg, []byte(`"sig"`)) || !bytes.HasPrefix(msg, []byte(`{"device":`)) {
		t.Fatalf("unexpected signing bytes %s", msg)
	}
	// Re-encoding with whitespace or reordering does not matter.
	spaced := bytes.ReplaceAll(js, []byte(","), []byte(" ,\n "))
	if err := VerifyRoot(spaced, f.signPub()); err != nil {
		t.Fatal(err)
	}
}

func TestRootSignatureTamper(t *testing.T) {
	f := newFixture(t)
	js := signedRootJSON(t, f)
	other := ed25519.NewKeyFromSeed(make([]byte, 32)).Public().(ed25519.PublicKey)
	tests := []struct {
		name string
		data []byte
		pub  ed25519.PublicKey
		want error
	}{
		{"changed field", bytes.Replace(js, []byte(`"files":1`), []byte(`"files":2`), 1), f.signPub(), ErrSignature},
		{"changed token", bytes.Replace(js, []byte(`OneDrive`), []byte(`OneDrivX`), 1), f.signPub(), ErrSignature},
		{"added field", bytes.Replace(js, []byte(`{`), []byte(`{"evil":true,`), 1), f.signPub(), ErrSignature},
		{"wrong key", js, other, ErrSignature},
		{"short key", js, f.signPub()[:10], ErrSignature},
		{"missing sig", stripSig(t, js), f.signPub(), ErrNoSignature},
		{"duplicate member", bytes.Replace(js, []byte(`{`), []byte(`{"v":1,`), 1), f.signPub(), ErrJCSDuplicateKey},
		{"bad base64", bytes.Replace(js, []byte(`"sig":"`), []byte(`"sig":"!`), 1), f.signPub(), ErrSignature},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := VerifyRoot(tc.data, tc.pub); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
	// Flip every byte of the signature value in turn: always rejected.
	i := bytes.Index(js, []byte(`"sig":"`)) + len(`"sig":"`)
	for j := 0; j < 86; j++ {
		b := bytes.Clone(js)
		if b[i+j] == 'A' {
			b[i+j] = 'B'
		} else {
			b[i+j] = 'A'
		}
		if VerifyRoot(b, f.signPub()) == nil {
			t.Fatalf("flipped sig char %d accepted", j)
		}
	}
}

func stripSig(t *testing.T, js []byte) []byte {
	t.Helper()
	out, _, err := stripMember(js, "sig")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// A future minor revision may add fields; they stay covered by the
// signature because verification works on the stored bytes.
func TestRootUnknownFieldSigned(t *testing.T) {
	f := newFixture(t)
	r := testRoot(f, 1, strings.Repeat("0", 64))
	msg, err := r.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	ext := append([]byte(`{"zz_future":{"b":2,"a":1},`), msg[1:]...)
	canon, err := Canonicalize(ext)
	if err != nil {
		t.Fatal(err)
	}
	sig := ed25519.Sign(f.sign, canon)
	stored := append(bytes.TrimSuffix(ext, []byte("}")), []byte(`,"sig":"`+b64(sig)+`"}`)...)
	if err := VerifyRoot(stored, f.signPub()); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeRoot(stored); err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(stored, []byte(`"a":1`), []byte(`"a":3`), 1)
	if !errors.Is(VerifyRoot(tampered, f.signPub()), ErrSignature) {
		t.Fatal("tampered unknown field accepted")
	}
}

func TestRootValidate(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name string
		mod  func(r *Root)
	}{
		{"version", func(r *Root) { r.V = 2 }},
		{"repo", func(r *Root) { r.Repo = "x" }},
		{"epoch", func(r *Root) { r.Epoch = 0 }},
		{"parent", func(r *Root) { r.Parent = HexPtr("ABC") }},
		{"inventory", func(r *Root) { r.Inventory = HexPtr(strings.Repeat("G", 64)) }},
		{"token", func(r *Root) { r.Tokens["{BAD}"] = `C:\` }},
		{"root path", func(r *Root) { r.Roots[0].TokenPath = "{DOCUMENTS}/../x" }},
		{"tree id", func(r *Root) { r.Roots[0].Tree = "00" }},
		{"install mode", func(r *Root) { r.InstallMode = "expert" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := testRoot(f, 1, strings.Repeat("0", 64))
			tc.mod(r)
			if err := r.Sign(f.sign); !errors.Is(err, ErrRoot) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSealOpenRoot(t *testing.T) {
	f := newFixture(t)
	r := testRoot(f, 2, strings.Repeat("0", 64))
	if err := r.Sign(f.sign); err != nil {
		t.Fatal(err)
	}
	sealed, err := SealRoot(nil, f.repo, f.k2, r)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[uint32]vdcrypt.Key{1: f.k1.Meta, 2: f.k2.Meta}
	got, js, err := OpenRoot(f.repo, keys, sealed)
	if err != nil {
		t.Fatal(err)
	}
	if got.Epoch != 2 || got.Job.Name != "Documents" || VerifyRoot(js, f.signPub()) != nil {
		t.Fatalf("round trip: %+v", got)
	}

	// Wrong epoch key: K_meta_1 offered for the epoch-2 envelope.
	if _, _, err := OpenRoot(f.repo, map[uint32]vdcrypt.Key{2: f.k1.Meta}, sealed); !errors.Is(err, vdcrypt.ErrDecrypt) {
		t.Fatalf("wrong epoch key: %v", err)
	}
	// Data key of the right epoch is not the meta key.
	if _, _, err := OpenRoot(f.repo, map[uint32]vdcrypt.Key{2: f.k2.Data}, sealed); !errors.Is(err, vdcrypt.ErrDecrypt) {
		t.Fatalf("data key: %v", err)
	}
	if _, _, err := OpenRoot(f.repo, map[uint32]vdcrypt.Key{1: f.k1.Meta}, sealed); !errors.Is(err, ErrNoEpochKey) {
		t.Fatalf("missing epoch: %v", err)
	}
	otherRepo, _ := vdcrypt.ParseRepoID("11111111-1111-4111-8111-111111111111")
	if _, _, err := OpenRoot(otherRepo, keys, sealed); !errors.Is(err, vdcrypt.ErrDecrypt) {
		t.Fatalf("other repo AD: %v", err)
	}
	// Envelope epoch rewritten to 1: opened with K_meta_1 it fails.
	b := bytes.Clone(sealed)
	b[1] = 1
	if _, _, err := OpenRoot(f.repo, keys, b); !errors.Is(err, vdcrypt.ErrDecrypt) {
		t.Fatalf("epoch field tamper: %v", err)
	}
	// A root claiming epoch 1 cannot be sealed under epoch 2 keys.
	r1 := testRoot(f, 1, strings.Repeat("0", 64))
	_ = r1.Sign(f.sign)
	if _, err := SealRoot(nil, f.repo, f.k2, r1); !errors.Is(err, ErrRootEpoch) {
		t.Fatalf("epoch mismatch: %v", err)
	}
	if _, err := SealRoot(nil, f.repo, f.k2, testRoot(f, 2, strings.Repeat("0", 64))); !errors.Is(err, ErrNoSignature) {
		t.Fatalf("unsigned: %v", err)
	}
}

func TestFileHash(t *testing.T) {
	f := newFixture(t)
	sum, err := FileHash(f.keys.FileHash, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if sum != FileHashBytes(f.keys.FileHash, []byte("hello")) {
		t.Fatal("stream and bytes differ")
	}
	if !VerifyFileHash(sum, hex.EncodeToString(sum[:])) || VerifyFileHash(sum, strings.Repeat("0", 64)) || VerifyFileHash(sum, "zz") {
		t.Fatal("VerifyFileHash")
	}
	// Keyed: a plain SHA-256 or another repo's K_fh gives a different h.
	other := vdcrypt.DeriveRepoKeys(f.repo, vdcrypttest.RMK1)
	if FileHashBytes(other.FileHash, []byte("hello")) == sum {
		t.Fatal("hash not keyed")
	}
}
