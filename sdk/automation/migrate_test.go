package automation

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// downgradeJSON marshals a migrated document with the three fields Migrate
// adds removed, for comparison with Parse of the v1 input.
func downgradeJSON(t testing.TB, a *Automation) []byte {
	t.Helper()
	c := *a
	c.Schema, c.SchemaVersion, c.Runtime = SchemaID, 0, ""
	b, err := json.Marshal(&c)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMigrateTemplates(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		in := mustJSON(t, tpl.Automation)
		m, from, err := Migrate(in)
		if err != nil {
			t.Fatalf("%s: %v", tpl.ID, err)
		}
		if from != SchemaVersionV1 || m.Schema != SchemaIDV2 || m.SchemaVersion != SchemaVersionV2 || m.Runtime != RuntimeDevice {
			t.Fatalf("%s: from=%d schema=%q version=%d runtime=%q", tpl.ID, from, m.Schema, m.SchemaVersion, m.Runtime)
		}
		// Lossless: apart from the v2 fields the result equals Parse(in).
		p, res := Parse(in)
		if !res.Valid() {
			t.Fatal(res.Error())
		}
		if got, want := downgradeJSON(t, m), mustJSON(t, p); !bytes.Equal(got, want) {
			t.Errorf("%s: migration changed content\n got %s\nwant %s", tpl.ID, got, want)
		}
		// Idempotent and deterministic.
		out := mustJSON(t, m)
		m2, from2, err := Migrate(out)
		if err != nil || from2 != SchemaVersionV2 {
			t.Fatalf("%s: re-migrate from=%d err=%v", tpl.ID, from2, err)
		}
		if !bytes.Equal(mustJSON(t, m2), out) {
			t.Errorf("%s: Migrate is not idempotent", tpl.ID)
		}
		if m3, _, _ := Migrate(in); !bytes.Equal(mustJSON(t, m3), out) {
			t.Errorf("%s: Migrate is not deterministic", tpl.ID)
		}
		// The migrated document parses as v2.
		if _, res := Parse(out); !res.Valid() {
			t.Errorf("%s: migrated doc invalid: %s", tpl.ID, res.Error())
		}
	}
}

func TestMigrateErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		from int
		msg  string
	}{
		"bad json":       {`{"schema":`, 0, "invalid JSON"},
		"unknown field":  {`{"schema":"vaultdock.automation/v1","x":1}`, 0, "x"},
		"unknown schema": {`{"schema":"vaultdock.automation/v9","name":"x","nodes":[],"edges":[]}`, 0, "unsupported schema"},
		"invalid v1":     {`{"schema":"vaultdock.automation/v1","name":"","nodes":[],"edges":[]}`, 1, "name"},
		"v2 field in v1": {`{"schema":"vaultdock.automation/v1","name":"x","runtime":"device","nodes":[{"id":"t","type":"trigger","kind":"file.created"},{"id":"a","type":"action","kind":"sync.file"}],"edges":[{"from":"t","to":"a"}]}`, 1, "runtime"},
	} {
		m, from, err := Migrate([]byte(tc.doc))
		if err == nil || m != nil {
			t.Errorf("%s: expected error", name)
			continue
		}
		if from != tc.from || !strings.Contains(err.Error(), tc.msg) {
			t.Errorf("%s: from=%d err=%v", name, from, err)
		}
		var vr *ValidationResult
		if tc.from != 0 && !errors.As(err, &vr) {
			t.Errorf("%s: error is %T, want *ValidationResult", name, err)
		}
	}
}

func TestUpgradeCopies(t *testing.T) {
	if Upgrade(nil) != nil {
		t.Error("Upgrade(nil) should be nil")
	}
	a := BuiltinTemplates()[1].Automation
	before := string(mustJSON(t, a))
	u := Upgrade(a)
	if string(mustJSON(t, a)) != before {
		t.Error("Upgrade mutated its input")
	}
	if u.Schema != SchemaIDV2 || u.Runtime != RuntimeDevice || u.SchemaVersion != SchemaVersionV2 {
		t.Errorf("Upgrade = %+v", u)
	}
	s := &Automation{Schema: SchemaIDV2, Runtime: RuntimeServer}
	if Upgrade(s).Runtime != RuntimeServer {
		t.Error("Upgrade replaced an explicit runtime")
	}
}
