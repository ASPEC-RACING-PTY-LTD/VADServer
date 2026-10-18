package deployenv

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

func TestEmbeddedSchemaParses(t *testing.T) {
	s, err := Embedded()
	if err != nil {
		t.Fatal(err)
	}
	if s.SchemaVersion < 1 || len(s.Variables) == 0 {
		t.Fatalf("schema: version %d, %d variables", s.SchemaVersion, len(s.Variables))
	}
	v, ok := s.Lookup("COMPOSE_PROFILES")
	if !ok || !v.Obsolete || v.Replacement != "VAULTDOCK_INTEGRATIONS" {
		t.Fatalf("COMPOSE_PROFILES = %+v, %v", v, ok)
	}
	secrets := 0
	for _, v := range s.Variables {
		if v.Secret {
			secrets++
		}
	}
	if secrets == 0 {
		t.Fatal("expected at least one secret variable")
	}
}

func TestParseRejects(t *testing.T) {
	for name, doc := range map[string]string{
		"bad json":    `{`,
		"no version":  `{"variables":[]}`,
		"bad name":    `{"schema_version":1,"variables":[{"name":"1X","since":"1.0.0"}]}`,
		"duplicate":   `{"schema_version":1,"variables":[{"name":"A","since":"1.0.0"},{"name":"A","since":"1.0.0"}]}`,
		"bad version": `{"schema_version":1,"variables":[{"name":"A","since":"one"}]}`,
	} {
		if _, err := Parse([]byte(doc)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}

const testSchema = `{"schema_version":3,"variables":[
 {"name":"OLD_A","default":"x","secret":false,"env_file":true,"scope":"server","since":"1.0.0"},
 {"name":"NEW_PLAIN","default":"90","env_file":true,"scope":"server","since":"1.1.0"},
 {"name":"NEW_SECRET","default":"s3cr3t-default","secret":true,"env_file":true,"scope":"server","since":"1.1.0"},
 {"name":"NEW_INSTALLER","default":"","env_file":false,"scope":"installer","since":"1.1.0"},
 {"name":"REQ_SERVER","required":true,"env_file":true,"scope":"server","since":"1.0.0"},
 {"name":"GONE","env_file":true,"scope":"compose","since":"1.0.0","obsolete":true,"deprecated_since":"1.1.0","replacement":"OLD_A","description":"note"},
 {"name":"GONE_UNSET","env_file":true,"scope":"compose","since":"1.0.0","obsolete":true,"deprecated_since":"1.1.0"}
]}`

func TestComputeMasksAndClassifies(t *testing.T) {
	s, err := Parse([]byte(testSchema))
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"OLD_A": "a", "NEW_SECRET": "live-secret-value", "GONE": "caddy"}
	lookup := func(k string) (string, bool) { v, ok := env[k]; return v, ok }
	pending := []wire.DeployEnvRemovalApproval{{Name: "ZED", ApprovedBy: 1}, {Name: "GONE", ApprovedBy: 2}}
	v := Compute(s, lookup, pending)

	if v.SchemaVersion != 3 {
		t.Fatalf("schema_version %d", v.SchemaVersion)
	}
	got := map[string]string{}
	for _, a := range v.AddedByUpgrade {
		got[a.Name] = a.Default
	}
	if len(got) != 2 || got["NEW_PLAIN"] != "90" || got["NEW_SECRET"] != "" {
		t.Fatalf("added_by_upgrade = %+v", v.AddedByUpgrade)
	}
	if len(v.ObsoletePresent) != 1 || v.ObsoletePresent[0] != (wire.DeployEnvObsolete{Name: "GONE", DeprecatedSince: "1.1.0", Replacement: "OLD_A", Note: "note"}) {
		t.Fatalf("obsolete_present = %+v", v.ObsoletePresent)
	}
	if len(v.PendingRemovals) != 2 || v.PendingRemovals[0].Name != "GONE" {
		t.Fatalf("pending_removals = %+v", v.PendingRemovals)
	}
	if len(v.Missing) != 1 || v.Missing[0] != "REQ_SERVER" {
		t.Fatalf("missing = %v", v.Missing)
	}
	b, _ := json.Marshal(v.DeployEnvStatus)
	if strings.Contains(string(b), "secret-value") || strings.Contains(string(b), "s3cr3t") {
		t.Fatalf("secret leaked: %s", b)
	}

	// Empty input still yields arrays, never null.
	e := Compute(&Schema{SchemaVersion: 1}, lookup, nil)
	b, _ = json.Marshal(e.DeployEnvStatus)
	if strings.Contains(string(b), "null") {
		t.Fatalf("null list: %s", b)
	}
}

func TestApprovalsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	a := NewApprovals(dir)
	if a.Path() != filepath.Join(dir, "deploy", "env-removal-approvals.json") {
		t.Fatalf("path %s", a.Path())
	}
	if l, err := a.List(); err != nil || len(l) != 0 {
		t.Fatalf("empty list: %v %v", l, err)
	}
	if _, err := a.Approve([]string{"GONE", "OTHER"}, 7, 1000); err != nil {
		t.Fatal(err)
	}
	// Re-approving keeps the original approval.
	l, err := a.Approve([]string{"GONE"}, 9, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(l) != 2 || l[0] != (wire.DeployEnvRemovalApproval{Name: "GONE", ApprovedBy: 7, ApprovedAt: 1000}) {
		t.Fatalf("after approve: %+v", l)
	}
	// The file is a JSON array of flat objects (what install.sh reads).
	raw, err := os.ReadFile(a.Path())
	if err != nil {
		t.Fatal(err)
	}
	var onDisk []map[string]any
	if err := json.Unmarshal(raw, &onDisk); err != nil || len(onDisk) != 2 || onDisk[1]["name"] != "OTHER" || onDisk[1]["approved_by"] != 7.0 {
		t.Fatalf("on disk: %s (%v)", raw, err)
	}
	if _, err := a.Approve([]string{"bad name"}, 1, 1); err == nil {
		t.Fatal("invalid name accepted")
	}

	if ok, err := a.Withdraw("NOPE"); ok || err != nil {
		t.Fatalf("withdraw unknown: %v %v", ok, err)
	}
	if ok, err := a.Withdraw("GONE"); !ok || err != nil {
		t.Fatalf("withdraw: %v %v", ok, err)
	}
	if l, _ := NewApprovals(dir).List(); len(l) != 1 || l[0].Name != "OTHER" {
		t.Fatalf("after withdraw: %+v", l)
	}
	if ok, err := a.Withdraw("OTHER"); !ok || err != nil {
		t.Fatalf("withdraw last: %v %v", ok, err)
	}
	if _, err := os.Stat(a.Path()); !os.IsNotExist(err) {
		t.Fatalf("file kept after last withdraw: %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir, "deploy", "*.tmp")); len(m) != 0 {
		t.Fatalf("temp files left: %v", m)
	}
}

func TestApprovalsReadsWrappedAndRejectsCorrupt(t *testing.T) {
	dir := t.TempDir()
	a := NewApprovals(dir)
	os.MkdirAll(filepath.Dir(a.Path()), 0o755)
	os.WriteFile(a.Path(), []byte(`{"approvals":[{"name":"X","approved_by":1,"approved_at":2},{"name":"bad name"},{"name":"X"}]}`), 0o600)
	l, err := a.List()
	if err != nil || len(l) != 1 || l[0].Name != "X" {
		t.Fatalf("wrapped: %+v %v", l, err)
	}
	os.WriteFile(a.Path(), []byte(`[{`), 0o600)
	if _, err := a.List(); err == nil {
		t.Fatal("corrupt file accepted")
	}
}
