package vdcrypt

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt/vdcrypttest"
)

func TestParseRepoID(t *testing.T) {
	tests := []struct {
		in string
		ok bool
	}{
		{vdcrypttest.RepoUUID, true},
		{"6f1c2a9e-1b3d-4c5e-9f00-0123456789ab", true},
		{"6F1C2A9E-1B3D-4C5E-9F00-0123456789AB", false}, // uppercase
		{"6f1c2a9e-1b3d-1c5e-9f00-0123456789ab", false}, // version 1
		{"6f1c2a9e-1b3d-4c5e-1f00-0123456789ab", false}, // bad variant
		{"6f1c2a9e1b3d4c5e9f000123456789ab", false},
		{"", false},
	}
	for _, tc := range tests {
		id, err := ParseRepoID(tc.in)
		if (err == nil) != tc.ok {
			t.Fatalf("%q: err=%v", tc.in, err)
		}
		if tc.ok && id.String() != tc.in {
			t.Fatalf("%q: round trip %q", tc.in, id.String())
		}
	}
}

func TestEnvelope(t *testing.T) {
	f := newFixture(t)
	blobID := bytes.Repeat([]byte{0xab}, 32)
	ad := BlobAD(f.repo, blobID)
	if want := "vd1:blob:" + vdcrypttest.RepoUUID + ":" + strings.Repeat("ab", 32); string(ad) != want {
		t.Fatalf("AD = %q", ad)
	}
	pt := []byte("hello vaultdock")
	sealed, err := Seal(nil, f.k1.Data, 1, pt, ad)
	if err != nil {
		t.Fatal(err)
	}
	if len(sealed) != len(pt)+EnvelopeOverhead || EnvelopeOverhead != 45 || sealed[0] != 0x01 {
		t.Fatalf("bad layout: len %d", len(sealed))
	}
	if e, _ := EnvelopeEpoch(sealed); e != 1 {
		t.Fatalf("epoch %d", e)
	}
	got, err := Open(f.k1.Data, 1, sealed, ad)
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("open: %v", err)
	}

	tamper := func(i int) []byte {
		c := bytes.Clone(sealed)
		c[i] ^= 1
		return c
	}
	otherAD, _ := ObjectAD(PurposeIndex, f.repo)
	cases := []struct {
		name   string
		key    Key
		epoch  uint32
		sealed []byte
		ad     []byte
		want   error
	}{
		{"tampered ciphertext", f.k1.Data, 1, tamper(len(sealed) - 1), ad, ErrDecrypt},
		{"tampered nonce", f.k1.Data, 1, tamper(10), ad, ErrDecrypt},
		{"tampered version", f.k1.Data, 1, tamper(0), ad, ErrVersion},
		{"tampered epoch field", f.k1.Data, 1, tamper(2), ad, ErrEpochMismatch},
		{"wrong epoch key", f.k2.Data, 2, sealed, ad, ErrEpochMismatch},
		{"wrong class key", f.k1.Meta, 1, sealed, ad, ErrDecrypt},
		{"wrong AD", f.k1.Data, 1, sealed, otherAD, ErrDecrypt},
		{"wrong blob id", f.k1.Data, 1, sealed, BlobAD(f.repo, blobID[:31]), ErrDecrypt},
		{"truncated", f.k1.Data, 1, sealed[:44], ad, ErrMalformed},
	}
	for _, tc := range cases {
		if _, err := Open(tc.key, tc.epoch, tc.sealed, tc.ad); !errors.Is(err, tc.want) {
			t.Errorf("%s: got %v want %v", tc.name, err, tc.want)
		}
	}
	if _, err := Seal(nil, f.k1.Data, 0, pt, ad); err != ErrInvalidEpoch {
		t.Errorf("epoch 0: %v", err)
	}
	if _, err := ObjectAD(PurposeBlob, f.repo); err != ErrPurpose {
		t.Errorf("ObjectAD(blob): %v", err)
	}
	if got := string(PackHeaderAD(f.repo, []byte{1, 2})); got != "vd1:packhdr:"+vdcrypttest.RepoUUID+":0102" {
		t.Errorf("pack AD %q", got)
	}
}

func TestSealedBoxNegative(t *testing.T) {
	f := newFixture(t)
	payload := []byte(`{"epoch":1}`)
	keyID := strings.Repeat("0", 32)
	epk, wrapped, err := Wrap(nil, f.repo, KindDevice, f.devPub, keyID, payload)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := Unwrap(f.repo, KindDevice, f.devPriv, epk, keyID, wrapped); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("unwrap: %v", err)
	}
	other, _ := ParseRepoID("6f1c2a9e-1b3d-4c5e-9f00-0123456789ab")
	bad := bytes.Clone(wrapped)
	bad[len(bad)-1] ^= 1
	cases := []struct {
		name    string
		repo    RepoID
		kind    Kind
		priv    Key
		keyID   string
		wrapped []byte
	}{
		{"wrong recipient", f.repo, KindDevice, f.sekPriv, keyID, wrapped},
		{"wrong kind", f.repo, KindEscrow, f.devPriv, keyID, wrapped},
		{"wrong repo", other, KindDevice, f.devPriv, keyID, wrapped},
		{"wrong key id", f.repo, KindDevice, f.devPriv, strings.Repeat("1", 32), wrapped},
		{"tampered", f.repo, KindDevice, f.devPriv, keyID, bad},
	}
	for _, tc := range cases {
		if _, err := Unwrap(tc.repo, tc.kind, tc.priv, epk, tc.keyID, tc.wrapped); err != ErrDecrypt {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
	if _, _, err := Wrap(nil, f.repo, KindDevice, Key{}, keyID, payload); err != ErrInvalidKey {
		t.Errorf("low-order recipient: %v", err)
	}
	if _, _, err := Wrap(nil, f.repo, Kind("other"), f.devPub, keyID, payload); err != ErrKind {
		t.Errorf("bad kind: %v", err)
	}
}

func TestKeyFileNegative(t *testing.T) {
	f := newFixture(t)
	cases := keyFileCases(t, f)
	dev := cases[0]
	kf, err := NewKeyFile(nil, dev.params, dev.bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kf.Open(f.recPriv); err != ErrWrongRecipient {
		t.Errorf("wrong recipient: %v", err)
	}
	enc, _ := kf.Encode()

	// Tampered fields break the wrap or the validation.
	for _, mut := range []struct{ name, from, to string }{
		{"key_id", kf.KeyID, strings.Repeat("f", 32)},
		{"kind", `"kind":"device"`, `"kind":"recovery"`},
		{"version", `"v":1`, `"v":2`},
	} {
		dec, err := DecodeKeyFile([]byte(strings.Replace(string(enc), mut.from, mut.to, 1)))
		if err != nil {
			continue
		}
		if _, err := dec.Open(dev.priv); err == nil {
			t.Errorf("%s tamper opened", mut.name)
		}
	}
	// Epoch field mismatch with the bundle.
	dec, _ := DecodeKeyFile(enc)
	dec.Epoch = 2
	if _, err := dec.Open(dev.priv); err != ErrBundle {
		t.Errorf("epoch mismatch: %v", err)
	}

	// Kind-specific members.
	p := dev.params
	p.Device = nil
	if _, err := NewKeyFile(nil, p, dev.bundle); err != ErrKeyFile {
		t.Errorf("device without member: %v", err)
	}
	p = cases[3].params // recovery
	if _, err := NewKeyFile(nil, p, MetaBundle(f.k1)); err != ErrBundle {
		t.Errorf("meta bundle to recovery: %v", err)
	}

	// Escrow signature pinning.
	esc, err := NewKeyFile(nil, cases[1].params, cases[1].bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := esc.VerifyEscrow(pub(f.sik)); err != nil {
		t.Errorf("escrow verify: %v", err)
	}
	if err := esc.VerifyEscrow(pub(f.devSign)); err != ErrEscrowSig {
		t.Errorf("wrong SIK: %v", err)
	}
	esc.Server.SEKVersion = 2
	if err := esc.VerifyEscrow(pub(f.sik)); err != ErrEscrowSig {
		t.Errorf("wrong SEK version: %v", err)
	}

	// Wrong passphrase.
	pp := cases[4]
	pkf, err := NewKeyFile(nil, pp.params, pp.bundle)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pkf.OpenPassphrase([]byte("wrong")); err != ErrWrongRecipient {
		t.Errorf("wrong passphrase: %v", err)
	}
	if b, err := pkf.OpenPassphrase([]byte(testPassphrase)); err != nil || b != pp.bundle {
		t.Errorf("passphrase open: %v", err)
	}
	if _, err := DecodeKeyFile([]byte(`{"v":1}`)); err == nil {
		t.Error("decoded an empty key file")
	}
}

func TestPassphraseKDFDefaults(t *testing.T) {
	k, err := NewPassphraseKDF(nil)
	if err != nil {
		t.Fatal(err)
	}
	if k.Alg != "argon2id" || k.T != 3 || k.MKiB != 65536 || k.P != 4 || len(k.Salt) != 16 {
		t.Fatalf("defaults %+v", k)
	}
	k.MKiB = 1024
	if _, _, err := PassphraseKeyPair([]byte("x"), k); err != ErrPassphraseKDF {
		t.Fatalf("tiny memory accepted: %v", err)
	}
}

func TestChainAndRotation(t *testing.T) {
	f := newFixture(t)
	s1, o1, err := InitEpoch(nil, f.repo)
	if err != nil {
		t.Fatal(err)
	}
	s2, o2, err := Rotate(nil, f.repo, s1)
	if err != nil {
		t.Fatal(err)
	}
	s3, o3, err := Rotate(nil, f.repo, s2)
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := s1.Keys(f.repo)
	k2, _ := s2.Keys(f.repo)
	k3, _ := s3.Keys(f.repo)
	all := [][]byte{o3.Chain, o3.MetaChain, o1.MetaChain, o1.Chain, o2.MetaChain, o2.Chain}

	// Full-key holder walks the full chain.
	got, err := ResolveEpochs(f.repo, s3, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[1] != s1 || got[2] != s2 || got[3] != s3 {
		t.Fatal("ResolveEpochs did not recover all epochs")
	}
	if _, err := ResolveEpochs(f.repo, s3, [][]byte{o3.Chain, o1.Chain, o2.MetaChain}); err != ErrChainMissing {
		t.Errorf("missing link: %v", err)
	}

	// A K_meta-only holder (metadata escrow) cannot open any full chain
	// object, so it never reaches an RMK or K_data ...
	for e, c := range map[uint32][]byte{1: o1.Chain, 2: o2.Chain, 3: o3.Chain} {
		km := map[uint32]Key{1: k1.Meta, 2: k2.Meta, 3: k3.Meta}[e]
		if _, err := OpenChain(f.repo, e, km, c); err != ErrDecrypt {
			t.Errorf("chain_%d opened with K_meta: %v", e, err)
		}
	}
	// ... but it can walk the meta-chain back to epoch 1.
	metas, err := WalkMeta(f.repo, 3, k3.Meta, all)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 3 || metas[1] != k1.Meta || metas[2] != k2.Meta || metas[3] != k3.Meta {
		t.Fatal("WalkMeta mismatch")
	}
	// WalkMeta never uses the full chain: without meta-chain objects it fails.
	if _, err := WalkMeta(f.repo, 3, k3.Meta, [][]byte{o3.Chain, o2.Chain}); err != ErrChainMissing {
		t.Errorf("WalkMeta via full chain: %v", err)
	}

	// Holding an old epoch never grants a newer one.
	if _, err := OpenChain(f.repo, 3, k2.Chain, o3.Chain); err != ErrDecrypt {
		t.Errorf("old key opened newer chain: %v", err)
	}
	// Wrong epoch, wrong AD (repo, and chain vs metachain purpose).
	if _, err := OpenChain(f.repo, 2, k3.Chain, o3.Chain); err != ErrEpochMismatch {
		t.Errorf("wrong epoch: %v", err)
	}
	other, _ := ParseRepoID("6f1c2a9e-1b3d-4c5e-9f00-0123456789ab")
	if _, err := OpenChain(other, 3, k3.Chain, o3.Chain); err != ErrDecrypt {
		t.Errorf("wrong repo AD: %v", err)
	}
	if _, err := OpenMetaChain(f.repo, 3, k3.Chain, o3.Chain); err != ErrDecrypt {
		t.Errorf("chain opened as metachain: %v", err)
	}
	if l, err := OpenMetaChain(f.repo, 1, k1.Meta, o1.MetaChain); err != nil || l.HasPrev {
		t.Errorf("metachain_1: %v", err)
	}
	if _, err := SealChain(nil, f.repo, s3, &s1); err != ErrInvalidEpoch {
		t.Errorf("non-adjacent prev: %v", err)
	}
	if _, err := SealChain(nil, f.repo, s2, nil); err != ErrInvalidEpoch {
		t.Errorf("missing prev: %v", err)
	}
	if _, err := SealMetaChain(nil, f.repo, k2, nil); err != ErrInvalidEpoch {
		t.Errorf("metachain missing prev: %v", err)
	}
}

func TestRecoveryKeyText(t *testing.T) {
	for i := 0; i < 50; i++ {
		s, err := NewRecoverySecret(nil)
		if err != nil {
			t.Fatal(err)
		}
		text := EncodeRecoveryKey(s)
		if !strings.HasPrefix(text, RecoveryPrefix) || len(text) != len(RecoveryPrefix)+55+10 {
			t.Fatalf("bad text %q", text)
		}
		got, err := DecodeRecoveryKey(text)
		if err != nil || got != s {
			t.Fatalf("round trip: %v", err)
		}
	}
	for _, bad := range []string{"", "VDRK1-", "VDRK2-" + strings.Repeat("0", 55), "VDRK1-" + strings.Repeat("0", 54), "VDRK1-" + strings.Repeat("U", 55)} {
		if _, err := DecodeRecoveryKey(bad); err != ErrRecoveryFormat {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestSEKSignature(t *testing.T) {
	f := newFixture(t)
	if !VerifySEK(pub(f.sik), f.sekPub, 1, f.sekSig) {
		t.Fatal("valid signature rejected")
	}
	if VerifySEK(pub(f.sik), f.devPub, 1, f.sekSig) || VerifySEK(ed25519.PublicKey{}, f.sekPub, 1, f.sekSig) {
		t.Fatal("invalid signature accepted")
	}
}
