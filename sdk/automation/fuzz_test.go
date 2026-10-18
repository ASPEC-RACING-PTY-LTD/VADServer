package automation

import (
	"bytes"
	"encoding/json"
	"testing"
)

const serverDocV2 = `{"schema":"vaultdock.automation/v2","schema_version":2,"runtime":"server","name":"Hourly ping","enabled":true,
 "nodes":[{"id":"t","type":"trigger","kind":"schedule.interval","params":{"every_minutes":60}},
  {"id":"c","type":"condition","kind":"group","params":{"mode":"all","rules":[{"kind":"variable.compare","params":{"variable":"device.name","operator":"equals","value":"PC"}}]}},
  {"id":"a","type":"action","kind":"notify.send","approval":"optional","params":{"title":"Hi","message":"{{device.name}}"}}],
 "edges":[{"from":"t","to":"c"},{"from":"c","to":"a","port":"true"}]}`

func fuzzSeeds(f *testing.F) {
	for _, tpl := range BuiltinTemplates() {
		b, _ := json.Marshal(tpl.Automation)
		f.Add(b)
		f.Add(mustJSONUpgrade(tpl.Automation))
	}
	f.Add([]byte(serverDocV2))
	f.Add([]byte(`{"schema":"vaultdock.automation/v2","name":"x","runtime":"cloud","nodes":[],"edges":[]}`))
	f.Add([]byte(`{"schema":"vaultdock.automation/v1","name":"x","nodes":[{"id":"t","type":"trigger","kind":"file.size_threshold","params":{"bytes":"2 GB"}},{"id":"a","type":"action","kind":"sync.skip","approval":"required"}],"edges":[{"from":"t","to":"a"}]}`))
	f.Add([]byte(`{"schema":1}`))
	f.Add([]byte(`null`))
	f.Add([]byte(``))
}

func mustJSONUpgrade(a *Automation) []byte {
	b, _ := json.Marshal(Upgrade(a))
	return b
}

// checkRoundTrip asserts that a valid document marshals to JSON that parses
// back, valid, to the same bytes.
func checkRoundTrip(t *testing.T, a *Automation) {
	t.Helper()
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatalf("marshal valid doc: %v", err)
	}
	a2, res := Parse(b)
	if !res.Valid() {
		t.Fatalf("round trip of %s invalid: %s", b, res.Error())
	}
	b2, _ := json.Marshal(a2)
	if !bytes.Equal(b, b2) {
		t.Fatalf("round trip changed the document\n got %s\nwant %s", b2, b)
	}
}

func FuzzParse(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		a, res := Parse(data)
		if res == nil {
			t.Fatal("nil result")
		}
		if res.Valid() {
			checkRoundTrip(t, a)
		}
	})
}

// FuzzValidate feeds leniently decoded documents (unknown fields ignored,
// so shapes Parse would reject still reach the validator).
func FuzzValidate(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var a Automation
		if json.Unmarshal(data, &a) != nil {
			return
		}
		res := ValidateDocument(&a)
		_ = RequiredModules(&a)
		_ = CheckSupport(&a, CurrentSchemaVersion, []string{ModuleAutomation, ModuleSync})
		for i := range a.Nodes {
			_ = NodeApproval(&a.Nodes[i])
		}
		if res.Valid() {
			checkRoundTrip(t, &a)
		}
	})
}

func FuzzMigrate(f *testing.F) {
	fuzzSeeds(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		m, from, err := Migrate(data)
		if err != nil {
			if m != nil {
				t.Fatal("document returned with an error")
			}
			return
		}
		if from != SchemaVersionV1 && from != SchemaVersionV2 {
			t.Fatalf("from = %d", from)
		}
		out, _ := json.Marshal(m)
		m2, from2, err := Migrate(out)
		if err != nil || from2 != SchemaVersionV2 {
			t.Fatalf("re-migrate: from=%d err=%v", from2, err)
		}
		if b, _ := json.Marshal(m2); !bytes.Equal(b, out) {
			t.Fatalf("not idempotent\n got %s\nwant %s", b, out)
		}
		checkRoundTrip(t, m)
		if from == SchemaVersionV1 {
			p, res := Parse(data)
			if !res.Valid() {
				t.Fatalf("Migrate accepted what Parse rejects: %s", res.Error())
			}
			want, _ := json.Marshal(p)
			if got := downgradeJSON(t, m); !bytes.Equal(got, want) {
				t.Fatalf("migration lost data\n got %s\nwant %s", got, want)
			}
		}
	})
}
