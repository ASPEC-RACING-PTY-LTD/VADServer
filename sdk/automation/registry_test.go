package automation

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/golden_v2.json from the registry")

// checkGolden compares v (indented JSON) with testdata/name, rewriting the
// file first when update is true.
func checkGolden(t *testing.T, name string, v any, update bool) {
	t.Helper()
	got, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	path := filepath.Join("testdata", name)
	if update {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("catalog differs from %s; run go test -run TestGoldenCatalog -update if the change is intended", path)
	}
}

// TestGoldenCatalogV1 guards compatibility: the catalogue with the fields
// added for schema v2 (runtimes, destructive, approval) removed must stay
// byte-identical to the frozen v1 golden file. It is never regenerated.
func TestGoldenCatalogV1(t *testing.T) {
	cat := Catalog()
	v1 := map[string][]KindSpec{}
	for typ, kinds := range cat {
		for _, k := range kinds {
			if k.MinSchemaVersion > SchemaVersionV1 {
				continue
			}
			k.Runtimes, k.Destructive, k.Approval = nil, false, ""
			v1[typ] = append(v1[typ], k)
		}
	}
	checkGolden(t, "golden_v1.json", v1, false)
}

// TestGoldenCatalogV2 pins the full catalogue as served by the reference
// endpoint. Regenerate with -update after an intended change.
func TestGoldenCatalogV2(t *testing.T) {
	checkGolden(t, "golden_v2.json", Catalog(), *updateGolden)
}

func TestBuiltinKindsRegistered(t *testing.T) {
	builtins := BuiltinKinds()
	if len(builtins) != 27 {
		t.Fatalf("got %d built-in kinds, want 27", len(builtins))
	}
	for _, b := range builtins {
		s := LookupKind(b.Type, b.Kind)
		if s == nil {
			t.Fatalf("%s %s not registered", b.Type, b.Kind)
		}
		if s != DefaultRegistry.Lookup(b.Type, b.Kind) {
			t.Errorf("%s: LookupKind and DefaultRegistry.Lookup disagree", b.Kind)
		}
		if s.MinSchemaVersion != SchemaVersionV1 {
			t.Errorf("%s: min_schema_version %d", b.Kind, s.MinSchemaVersion)
		}
		hasAutomation := false
		for _, m := range s.RequiredModules {
			hasAutomation = hasAutomation || m == ModuleAutomation
		}
		if !hasAutomation {
			t.Errorf("%s: required_modules %v lacks %q", b.Kind, s.RequiredModules, ModuleAutomation)
		}
		if len(s.Ports) == 0 {
			t.Errorf("%s: no ports", b.Kind)
		}
	}
	if DefaultRegistry.kinds[TypeAction]["notify.send"] != LookupKind(TypeAction, "notify.send") {
		t.Error("catalog map is not the default registry's map")
	}
}

func TestRegistryRegister(t *testing.T) {
	r := NewRegistry()
	ok := KindSpec{Type: TypeCondition, Kind: "x.test", Title: "X", RequiredModules: []string{ModuleVSS}}
	if err := r.Register(ok); err != nil {
		t.Fatal(err)
	}
	s := r.Lookup(TypeCondition, "x.test")
	if s == nil || len(s.Ports) != 2 || s.Ports[0] != PortTrue || s.MinSchemaVersion != SchemaVersionV1 {
		t.Fatalf("defaults not applied: %+v", s)
	}
	if got := r.Kinds(TypeCondition); len(got) != 1 || got[0] != "x.test" {
		t.Errorf("Kinds = %v", got)
	}
	for name, bad := range map[string]KindSpec{
		"duplicate":    ok,
		"unknown type": {Type: "step", Kind: "y"},
		"empty kind":   {Type: TypeAction},
		"bad module":   {Type: TypeAction, Kind: "z", RequiredModules: []string{"teleport"}},
	} {
		if err := r.Register(bad); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if r.Lookup(TypeTrigger, "x.test") != nil {
		t.Error("lookup crossed node types")
	}
}
