package modules

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestModeSets(t *testing.T) {
	cases := []struct {
		mode Mode
		want []ID
		comp []ID
	}{
		{ModeSimple, []ID{Core}, nil},
		{ModeAdvanced, []ID{Core, Sync, Automation, Profiles, VSS, Discovery, Reinstall}, []ID{VSS, Discovery, Reinstall}},
		{"bogus", nil, nil},
	}
	for _, c := range cases {
		if got := ModulesForMode(c.mode); !slices.Equal(got, c.want) {
			t.Errorf("ModulesForMode(%q) = %v, want %v", c.mode, got, c.want)
		}
		if got := ComponentsForMode(c.mode); !slices.Equal(got, c.comp) {
			t.Errorf("ComponentsForMode(%q) = %v, want %v", c.mode, got, c.comp)
		}
	}
	// The returned slice must not alias All.
	ModulesForMode(ModeAdvanced)[0] = "x"
	if All[0] != Core {
		t.Fatal("ModulesForMode aliases All")
	}
	if !InMode(Sync, ModeAdvanced) || InMode(Sync, ModeSimple) || !InMode(Core, ModeSimple) {
		t.Error("InMode wrong")
	}
}

func TestKinds(t *testing.T) {
	cases := []struct {
		id        ID
		kind      Kind
		builtin   bool
		component bool
	}{
		{Core, KindCore, false, false},
		{Sync, KindBuiltin, true, false},
		{Automation, KindBuiltin, true, false},
		{Profiles, KindBuiltin, true, false},
		{VSS, KindComponent, false, true},
		{Discovery, KindComponent, false, true},
		{Reinstall, KindComponent, false, true},
	}
	for _, c := range cases {
		k, ok := KindOf(c.id)
		if !ok || k != c.kind || IsBuiltin(c.id) != c.builtin || IsComponent(c.id) != c.component {
			t.Errorf("%s: kind=%q ok=%v builtin=%v component=%v", c.id, k, ok, IsBuiltin(c.id), IsComponent(c.id))
		}
	}
	if Known("nope") || IsBuiltin("nope") {
		t.Error("unknown id accepted")
	}
	if !StateFailed.Valid() || State("x").Valid() || !ModeSimple.Valid() || Mode("x").Valid() {
		t.Error("Valid wrong")
	}
}

func manifestJSON(comp string) string {
	return `{"manifest_version":1,"release":"1.4.0","components":[` + comp + `]}`
}

const goodComp = `{"id":"vss","version":"1.4.0","min_core":"1.3.0","modes":["advanced"],"os":"windows","arch":"amd64",
"url":"https://github.com/x/y/releases/download/v1.4.0/vd-vss-1.4.0.zip",
"sha256":"` + "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" + `","size":10,"entry":"vd-vss.exe",
"authenticode":{"subject":"ASPEC Racing Pty Ltd"}}`

func TestParseManifest(t *testing.T) {
	m, err := ParseManifest([]byte(manifestJSON(goodComp)))
	if err != nil {
		t.Fatal(err)
	}
	c, ok := m.Find(VSS, "windows", "amd64")
	if !ok || c.Entry != "vd-vss.exe" || c.Authenticode.Subject != "ASPEC Racing Pty Ltd" {
		t.Fatalf("Find = %+v, %v", c, ok)
	}
	if _, ok := m.Find(VSS, "windows", "arm64"); ok {
		t.Error("found wrong arch")
	}

	bad := []struct{ name, raw string }{
		{"syntax", `{`},
		{"version", `{"manifest_version":2,"release":"1.0.0"}`},
		{"release", `{"manifest_version":1,"release":"dev"}`},
		{"builtin id", manifestJSON(strings.Replace(goodComp, `"vss"`, `"sync"`, 1))},
		{"http url", manifestJSON(strings.Replace(goodComp, "https://", "http://", 1))},
		{"sha", manifestJSON(strings.Replace(goodComp, "e3b0c442", "zz", 1))},
		{"size", manifestJSON(strings.Replace(goodComp, `"size":10`, `"size":0`, 1))},
		{"mode", manifestJSON(strings.Replace(goodComp, `["advanced"]`, `["pro"]`, 1))},
		{"min_core", manifestJSON(strings.Replace(goodComp, `"1.3.0"`, `"x"`, 1))},
		{"dep", manifestJSON(strings.Replace(goodComp, `"size":10`, `"size":10,"depends":["nope"]`, 1))},
		{"dup", manifestJSON(goodComp + "," + goodComp)},
		{"sig", `{"manifest_version":1,"release":"1.0.0","signature":"!!"}`},
	}
	for _, b := range bad {
		if _, err := ParseManifest([]byte(b.raw)); !errors.Is(err, ErrInvalidManifest) {
			t.Errorf("%s: err = %v", b.name, err)
		}
	}
}

func TestCompatibilityAndDeps(t *testing.T) {
	c := Component{ID: Discovery, MinCore: "1.3.0", OS: "windows", Arch: "amd64", Depends: []ID{VSS, Profiles}}
	cases := []struct {
		core, os, arch string
		ok             bool
	}{
		{"1.3.0", "windows", "amd64", true},
		{"v1.5.2", "windows", "amd64", true},
		{"1.2.9", "windows", "amd64", false},
		{"1.3.0-rc.1", "windows", "amd64", false},
		{"dev", "windows", "amd64", false},
		{"1.3.0", "linux", "amd64", false},
	}
	for _, k := range cases {
		err := CheckCompatible(c, k.core, k.os, k.arch)
		if (err == nil) != k.ok || (err != nil && !errors.Is(err, ErrIncompatible)) {
			t.Errorf("CheckCompatible(%s %s/%s) = %v", k.core, k.os, k.arch, err)
		}
	}
	if got := MissingDependencies(c, []ID{Core, Profiles}); !slices.Equal(got, []ID{VSS}) {
		t.Errorf("missing = %v", got)
	}
	if err := CheckDependencies(c, []ID{VSS, Profiles}); err != nil {
		t.Error(err)
	}
	if err := CheckDependencies(c, nil); !errors.Is(err, ErrMissingDeps) {
		t.Error(err)
	}
}

func TestManifestSignature(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	v, err := NewEd25519Verifier(otherPub, pub)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(manifestJSON(goodComp))
	sig := SignManifest(priv, raw)

	if err := v.VerifyManifest(raw, []byte(sig)); err != nil {
		t.Errorf("base64 sig: %v", err)
	}
	if err := v.VerifyManifest(raw, ed25519.Sign(priv, raw)); err != nil {
		t.Errorf("raw sig: %v", err)
	}
	tampered := []byte(strings.Replace(string(raw), "1.3.0", "1.0.0", 1))
	if err := v.VerifyManifest(tampered, []byte(sig)); !errors.Is(err, ErrBadSignature) {
		t.Errorf("tampered: %v", err)
	}
	only, _ := NewEd25519Verifier(pub)
	if err := only.VerifyManifest(raw, []byte(SignManifest(otherPriv, raw))); !errors.Is(err, ErrBadSignature) {
		t.Errorf("untrusted key: %v", err)
	}
	if err := v.VerifyManifest(raw, []byte("garbage")); !errors.Is(err, ErrBadSignature) {
		t.Errorf("garbage: %v", err)
	}
	if _, err := NewEd25519Verifier(); !errors.Is(err, ErrNoTrustedKeys) {
		t.Error(err)
	}
	if _, err := NewEd25519Verifier(ed25519.PublicKey{1}); !errors.Is(err, ErrInvalidKeySize) {
		t.Error(err)
	}

	// Embedded signature.
	m, _ := ParseManifest(raw)
	payload, _ := m.SigningPayload()
	m.Signature = SignManifest(priv, payload)
	signed := fmt.Sprintf(`{"manifest_version":1,"release":"1.4.0","signature":%q,"components":[%s]}`, m.Signature, goodComp)
	if got, err := v.VerifyEmbedded([]byte(signed)); err != nil || got.Release != "1.4.0" {
		t.Errorf("embedded: %v", err)
	}
	if _, err := v.VerifyEmbedded([]byte(strings.Replace(signed, `"size":10`, `"size":11`, 1))); !errors.Is(err, ErrBadSignature) {
		t.Errorf("embedded tampered: %v", err)
	}
	if _, err := v.VerifyEmbedded(raw); !errors.Is(err, ErrNoSignature) {
		t.Errorf("unsigned: %v", err)
	}
}

func TestVerifyComponent(t *testing.T) {
	data := []byte("component!")
	sum := sha256.Sum256(data)
	path := filepath.Join(t.TempDir(), "c.zip")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	v, _ := NewEd25519Verifier(pub)
	good := Component{ID: VSS, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}

	if err := v.VerifyComponent(good, path); err != nil {
		t.Errorf("good: %v", err)
	}
	wrongSum := good
	wrongSum.SHA256 = strings.Repeat("00", 32)
	if err := v.VerifyComponent(wrongSum, path); !errors.Is(err, ErrChecksum) {
		t.Errorf("sha mismatch: %v", err)
	}
	wrongSize := good
	wrongSize.Size++
	if err := v.VerifyComponent(wrongSize, path); !errors.Is(err, ErrSizeMismatch) {
		t.Errorf("size mismatch: %v", err)
	}
	if err := v.VerifyComponent(good, path+".missing"); err == nil {
		t.Error("missing file accepted")
	}
}
