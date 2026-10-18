package automation

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestBuiltinTemplatesValidateAndRoundTrip(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		b := mustJSON(t, tpl.Automation)
		a, res := Parse(b)
		if !res.Valid() {
			t.Fatalf("%s: %v", tpl.ID, res.Error())
		}
		if len(res.Warnings) > 0 {
			t.Errorf("%s: unexpected warnings %+v", tpl.ID, res.Warnings)
		}
		// export -> import must be lossless
		b2 := mustJSON(t, a)
		a2, res2 := Parse(b2)
		if !res2.Valid() {
			t.Fatalf("%s round trip: %v", tpl.ID, res2.Error())
		}
		if string(mustJSON(t, a2)) != string(b2) {
			t.Errorf("%s: round trip changed the document", tpl.ID)
		}
	}
}

func medal(t *testing.T) *Automation {
	a, res := Parse(mustJSON(t, BuiltinTemplates()[0].Automation))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	return a
}

func fileEvent(rel string, size int64, created time.Time) *Event {
	return &Event{
		Type: EventFileCreated, Config: "Medal", Root: `C:\Medal\Clips`, Destination: "/Medal", Device: "PC",
		File: &FileInfo{Path: `C:\Medal\Clips\` + strings.ReplaceAll(rel, "/", `\`), RelPath: rel, Size: size, Created: created, Modified: created},
		Now:  created.Add(time.Hour),
	}
}

func TestMedalTemplateEvaluation(t *testing.T) {
	a := medal(t)
	created := time.Date(2026, 10, 16, 21, 4, 11, 0, time.UTC)
	cases := []struct {
		rel      string
		size     int64
		decision string
		dir      string
		tags     []string
	}{
		{"Valorant/clip.mp4", 50 << 20, "sync", "/Medal/Valorant/2026-10", []string{"clips", "valorant"}},
		{"clip.mp4", 50 << 20, "sync", "/Medal/Unsorted/2026-10", []string{"clips", "unsorted"}},
		{"Counter-Strike 2/clip.MKV", 5 << 20, "sync", "/Medal/Counter-Strike 2/2026-10", []string{"clips", "counter-strike-2"}},
		{"Valorant/clip.mp4.tmp1", 50 << 20, "skip", "", nil},
		{"Valorant/clip.mp4", 100, "skip", "", nil},
		{"Valorant/notes.txt", 50 << 20, "skip", "", nil},
	}
	for _, c := range cases {
		plan := EvaluateAll([]Named{{ID: "1", Automation: a}}, fileEvent(c.rel, c.size, created))
		if plan.Decision != c.decision {
			t.Errorf("%s: decision %q want %q (trace %+v)", c.rel, plan.Decision, c.decision, plan.Outcomes[0].Trace)
			continue
		}
		if c.decision == "sync" {
			if plan.RemoteDir != c.dir {
				t.Errorf("%s: dir %q want %q", c.rel, plan.RemoteDir, c.dir)
			}
			if strings.Join(plan.Tags, ",") != strings.Join(c.tags, ",") {
				t.Errorf("%s: tags %v want %v", c.rel, plan.Tags, c.tags)
			}
		}
	}
}

func TestDefaultPlanMirrorsFolders(t *testing.T) {
	ev := fileEvent("a/b/c.txt", 10, time.Now())
	plan := EvaluateAll(nil, ev)
	if plan.Decision != "sync" || plan.RemoteDir != "/Medal/a/b" || plan.RemoteName != "c.txt" {
		t.Fatalf("unexpected plan %+v", plan)
	}
}

func TestValidationErrors(t *testing.T) {
	doc := `{
	  "schema": "vaultdock.automation/v1",
	  "name": "bad",
	  "nodes": [
	    {"id": "t", "type": "trigger", "kind": "file.craeted"},
	    {"id": "c", "type": "condition", "kind": "size.compare", "params": {"operator": "bigger", "bytes": "lots"}},
	    {"id": "a", "type": "action", "kind": "dest.set_folder", "params": {"folder": "{{file.nope}}", "extra": 1}}
	  ],
	  "edges": [{"from": "t", "to": "c"}, {"from": "c", "to": "a"}]
	}`
	_, res := Parse([]byte(doc))
	if res.Valid() {
		t.Fatal("expected errors")
	}
	joined := res.Error()
	for _, want := range []string{
		`did you mean "file.created"`,
		"nodes[1].params.operator: must be one of",
		"nodes[1].params.bytes: invalid size",
		`nodes[2].params.folder: unknown variable "file.nope"`,
		"nodes[2].params.extra: unknown parameter",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing error %q in %s", want, joined)
		}
	}
}

func TestEdgePortsAndCycles(t *testing.T) {
	base := func(edges string) string {
		return `{"schema":"vaultdock.automation/v1","name":"x","nodes":[
		 {"id":"t","type":"trigger","kind":"file.created"},
		 {"id":"c","type":"condition","kind":"name.contains","params":{"text":"a"}},
		 {"id":"a","type":"action","kind":"sync.file"},
		 {"id":"b","type":"action","kind":"tag.apply","params":{"tags":["x"]}}],"edges":` + edges + `}`
	}
	_, res := Parse([]byte(base(`[{"from":"t","to":"c"},{"from":"c","to":"a"}]`)))
	if res.Valid() || !strings.Contains(res.Error(), `port to "true" or "false"`) {
		t.Errorf("condition without port should fail: %v", res.Error())
	}
	_, res = Parse([]byte(base(`[{"from":"t","to":"c"},{"from":"c","to":"a","port":"true"},{"from":"a","to":"b"},{"from":"b","to":"a"}]`)))
	if res.Valid() || !strings.Contains(res.Error(), "loop") {
		t.Errorf("cycle should fail: %v", res.Error())
	}
	_, res = Parse([]byte(base(`[{"from":"a","to":"t"}]`)))
	if res.Valid() || !strings.Contains(res.Error(), "incoming") {
		t.Errorf("edge into trigger should fail: %v", res.Error())
	}
	_, res = Parse([]byte(base(`[{"from":"t","to":"c"},{"from":"c","to":"a","port":"true"}]`)))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	if len(res.Warnings) == 0 {
		t.Error("expected unreachable warning for node b")
	}
}

func TestContextChecks(t *testing.T) {
	doc := `{"schema":"vaultdock.automation/v1","name":"ctx","nodes":[
	 {"id":"t","type":"trigger","kind":"sync.failed"},
	 {"id":"a","type":"action","kind":"dest.set_folder","params":{"folder":"x"}},
	 {"id":"n","type":"action","kind":"notify.send","params":{"title":"{{file.name}}"}}],
	 "edges":[{"from":"t","to":"a"},{"from":"t","to":"n"}]}`
	_, res := Parse([]byte(doc))
	e := res.Error()
	if !strings.Contains(e, `"dest.set_folder" cannot be used after the "sync.failed" trigger`) {
		t.Errorf("missing context error: %s", e)
	}
	if !strings.Contains(e, `variable "file.name" is not available for sync events`) {
		t.Errorf("missing variable context error: %s", e)
	}
}

func TestUnknownFieldsRejected(t *testing.T) {
	_, res := Parse([]byte(`{"schema":"vaultdock.automation/v1","name":"x","nodes":[],"edges":[],"bogus":1}`))
	if res.Valid() || !strings.Contains(res.Error(), "bogus") {
		t.Errorf("expected unknown field error, got %v", res.Error())
	}
}

func TestConflictWarnings(t *testing.T) {
	doc := `{"schema":"vaultdock.automation/v1","name":"c","nodes":[
	 {"id":"t","type":"trigger","kind":"file.created"},
	 {"id":"s","type":"action","kind":"sync.file"},
	 {"id":"k","type":"action","kind":"sync.skip"}],
	 "edges":[{"from":"t","to":"s"},{"from":"s","to":"k"}]}`
	_, res := Parse([]byte(doc))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	found := false
	for _, w := range res.Warnings {
		if strings.Contains(w.Message, "both synchronises and skips") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected conflict warning: %+v", res.Warnings)
	}
}

func TestPriorityMerging(t *testing.T) {
	mk := func(name string, prio int, action string) *Automation {
		a, res := Parse([]byte(`{"schema":"vaultdock.automation/v1","name":"` + name + `","priority":` + itoa(prio) + `,"nodes":[
		 {"id":"t","type":"trigger","kind":"file.created"},
		 {"id":"a","type":"action","kind":"` + action + `"}],"edges":[{"from":"t","to":"a"}]}`))
		if !res.Valid() {
			t.Fatal(res.Error())
		}
		return a
	}
	ev := fileEvent("x.mp4", 10, time.Now())
	p := EvaluateAll([]Named{{"1", mk("skipper", 0, "sync.skip")}, {"2", mk("syncer", 5, "sync.file")}}, ev)
	if p.Decision != "sync" || len(p.Conflicts) == 0 {
		t.Errorf("higher priority sync should win with a conflict note: %+v", p)
	}
	p = EvaluateAll([]Named{{"1", mk("skipper", 0, "sync.skip")}, {"2", mk("syncer", 0, "sync.file")}}, ev)
	if p.Decision != "skip" {
		t.Errorf("skip should win at equal priority: %+v", p)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestTemplates(t *testing.T) {
	tm := time.Date(2026, 3, 7, 9, 5, 2, 0, time.UTC)
	vars := Vars{"file.created": tm, "file.parent_name": "", "file.stem": "My Clip", "file.size_mb": 12.5}
	cases := map[string]string{
		`{{file.created|date:"YYYY-MM-DD HH:mm:ss"}}`: "2026-03-07 09:05:02",
		`{{file.created|date:"MMM YY dddd"}}`:         "Mar 26 Saturday",
		`{{file.parent_name|default:"Unsorted"}}`:     "Unsorted",
		`{{file.stem|slug}}`:                          "my-clip",
		`{{file.stem|replace:" ","_"|upper}}`:         "MY_CLIP",
		`{{file.stem|truncate:2}}`:                    "My",
		`size {{ file.size_mb }} MB`:                  "size 12.5 MB",
	}
	for tpl, want := range cases {
		p, err := ParseTemplate(tpl)
		if err != nil {
			t.Fatalf("%s: %v", tpl, err)
		}
		if got := p.Render(vars); got != want {
			t.Errorf("%s = %q want %q", tpl, got, want)
		}
	}
	for _, bad := range []string{"{{", "{{a|nope}}", "{{a|date}}", "}}", `{{a|truncate:"x"}}`} {
		if _, err := ParseTemplate(bad); err == nil {
			t.Errorf("%q should fail to parse", bad)
		}
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, s string
		want   bool
	}{
		{"*.mp4", "a.mp4", true},
		{"*.mp4", "dir/a.mp4", false},
		{"**/*.mp4", "a.mp4", true},
		{"**/*.mp4", "x/y/a.mp4", true},
		{"**/Temp/**", "a/Temp/b.txt", true},
		{"**/Temp/**", "a/Temporary/b.txt", false},
		{"clip_??.mkv", "clip_01.mkv", true},
		{"[!a]*", "abc", false},
		{"*.MP4", "x.mp4", true},
	}
	for _, c := range cases {
		if got := MatchGlob(c.pat, c.s, false); got != c.want {
			t.Errorf("%q vs %q = %v", c.pat, c.s, got)
		}
	}
}

func TestParseBytes(t *testing.T) {
	for in, want := range map[string]int64{"1024": 1024, "50 MB": 50 << 20, "1.5GiB": 3 << 29, "10kb": 10240, "7 bytes": 7} {
		got, err := ParseBytes(in)
		if err != nil || got != want {
			t.Errorf("%s = %d, %v", in, got, err)
		}
	}
}

func TestDestinationCannotEscape(t *testing.T) {
	a, res := Parse([]byte(`{"schema":"vaultdock.automation/v1","name":"x","nodes":[
	 {"id":"t","type":"trigger","kind":"file.created"},
	 {"id":"d","type":"action","kind":"dest.set_folder","params":{"folder":"../../{{file.stem}}/.."}},
	 {"id":"r","type":"action","kind":"dest.rename","params":{"name":"../evil:{{file.ext}}"}}],
	 "edges":[{"from":"t","to":"d"},{"from":"d","to":"r"}]}`))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	p := EvaluateAll([]Named{{"1", a}}, fileEvent("sub/a.mp4", 1, time.Now()))
	if p.RemoteDir != "/Medal/a" {
		t.Errorf("dir = %q", p.RemoteDir)
	}
	if strings.ContainsAny(p.RemoteName, `/\:`) {
		t.Errorf("name = %q", p.RemoteName)
	}
}

func TestJSONSchemaAndGuide(t *testing.T) {
	b, err := json.Marshal(JSONSchema())
	if err != nil || len(b) < 1000 {
		t.Fatalf("schema: %v", err)
	}
	g := AIGuide()
	for _, s := range []string{SchemaID, "file.parent_name", "dest.set_folder", "```json"} {
		if !strings.Contains(g, s) {
			t.Errorf("guide missing %q", s)
		}
	}
}

func TestBytesAcceptsStringsOnImport(t *testing.T) {
	a, res := Parse([]byte(`{"schema":"vaultdock.automation/v1","name":"x","nodes":[
	 {"id":"t","type":"trigger","kind":"file.size_threshold","params":{"bytes":"2 GB"}},
	 {"id":"a","type":"action","kind":"sync.skip"}],"edges":[{"from":"t","to":"a"}]}`))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	if a.Nodes[0].Params["bytes"].(float64) != float64(2<<30) {
		t.Errorf("bytes not normalised: %v", a.Nodes[0].Params["bytes"])
	}
}

func TestTemplatesStayV1(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		b := string(mustJSON(t, tpl.Automation))
		for _, f := range []string{`"schema_version"`, `"runtime"`, `"approval"`} {
			if strings.Contains(b, f) {
				t.Errorf("%s: v1 template marshals v2 field %s", tpl.ID, f)
			}
		}
	}
}

// TestOldClientRejectsV2 mirrors an old desktop: the v1-only Validate must
// refuse a v2 document with a clear schema error (research 02 §9.2).
func TestOldClientRejectsV2(t *testing.T) {
	a, res := Parse([]byte(serverDocV2))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	if r := Validate(a.Clone()); r.Valid() || r.Errors[0].Path != "schema" {
		t.Errorf("v1 Validate accepted v2: %+v", r.Errors)
	}
}

func v2Doc(runtime, trigger, action, approval string) string {
	rt := ""
	if runtime != "" {
		rt = `"runtime":"` + runtime + `",`
	}
	ap := ""
	if approval != "" {
		ap = `,"approval":"` + approval + `"`
	}
	tparams := ""
	if trigger == "schedule.interval" {
		tparams = `,"params":{"every_minutes":60}`
	}
	params := `{"title":"t","message":"m"}`
	switch action {
	case "sync.skip":
		params = `{"reason":"r"}`
	case "tag.apply":
		params = `{"tags":["x"]}`
	case "dest.create_folder":
		params = `{"folder":"/x"}`
	}
	return `{"schema":"vaultdock.automation/v2",` + rt + `"name":"x","nodes":[
	 {"id":"t","type":"trigger","kind":"` + trigger + `"` + tparams + `},
	 {"id":"a","type":"action","kind":"` + action + `"` + ap + `,"params":` + params + `}],
	 "edges":[{"from":"t","to":"a"}]}`
}

func TestV2Validation(t *testing.T) {
	withTestPolicy(t, "tag.apply", actTagApply, &ApprovalPolicy{Approval: ApprovalOptional})
	for name, tc := range map[string]struct {
		doc  string
		path string // "" = valid
	}{
		"server doc":              {serverDocV2, ""},
		"device default":          {v2Doc("", "file.created", "sync.skip", ""), ""},
		"explicit device":         {v2Doc("device", "schedule.interval", "notify.send", "required"), ""},
		"bad runtime":             {v2Doc("cloud", "file.created", "sync.skip", ""), "runtime"},
		"server file trigger":     {v2Doc("server", "file.created", "notify.send", ""), "nodes[0].kind"},
		"server storage action":   {v2Doc("server", "schedule.interval", "dest.create_folder", ""), "nodes[1].kind"},
		"bad approval":            {v2Doc("", "file.created", "sync.skip", "maybe"), "nodes[1].approval"},
		"approval tightens":       {v2Doc("", "file.created", "tag.apply", "required"), ""},
		"approval cannot lower":   {v2Doc("", "file.created", "tag.apply", "none"), "nodes[1].approval"},
		"schema_version mismatch": {strings.Replace(serverDocV2, `"schema_version":2`, `"schema_version":1`, 1), "schema_version"},
		"approval on trigger": {`{"schema":"vaultdock.automation/v2","name":"x","nodes":[{"id":"t","type":"trigger","kind":"file.created","approval":"required"},
		 {"id":"a","type":"action","kind":"sync.file"}],"edges":[{"from":"t","to":"a"}]}`, "nodes[0].approval"},
		"v1 schema_version": {`{"schema":"vaultdock.automation/v1","schema_version":1,"name":"x","nodes":[{"id":"t","type":"trigger","kind":"file.created"},
		 {"id":"a","type":"action","kind":"sync.file"}],"edges":[{"from":"t","to":"a"}]}`, "schema_version"},
		"v1 node approval": {`{"schema":"vaultdock.automation/v1","name":"x","nodes":[{"id":"t","type":"trigger","kind":"file.created"},
		 {"id":"a","type":"action","kind":"sync.file","approval":"required"}],"edges":[{"from":"t","to":"a"}]}`, "nodes[1].approval"},
		"server nested rule": {strings.Replace(serverDocV2, `"kind":"variable.compare","params":{"variable":"device.name","operator":"equals","value":"PC"}`,
			`"kind":"group","params":{"mode":"any","rules":[{"kind":"variable.compare","params":{"variable":"device.name","operator":"equals","value":"PC"}},{"kind":"name.contains","params":{"text":"x"}}]}`, 1),
			"nodes[1].params.rules[0].params.rules[1]"},
		"unknown schema": {`{"schema":"vaultdock.automation/v3","name":"x","nodes":[],"edges":[]}`, "schema"},
	} {
		_, res := Parse([]byte(tc.doc))
		if tc.path == "" {
			if !res.Valid() {
				t.Errorf("%s: %s", name, res.Error())
			}
			continue
		}
		found := false
		for _, e := range res.Errors {
			found = found || e.Path == tc.path
		}
		if !found {
			t.Errorf("%s: want error at %s, got %+v", name, tc.path, res.Errors)
		}
	}
	_, res := Parse([]byte(`{"schema":"vaultdock.automation/v3","name":"x","nodes":[],"edges":[]}`))
	if !strings.Contains(res.Error(), SchemaIDV2) {
		t.Errorf("unknown schema message should list v2: %s", res.Error())
	}
}

func TestNodeApproval(t *testing.T) {
	withTestPolicy(t, "tag.apply", actTagApply, &ApprovalPolicy{Approval: ApprovalOptional})
	for _, tc := range []struct {
		kind     string
		override ApprovalLevel
		want     ApprovalLevel
	}{
		{"sync.file", "", ApprovalNone},
		{"sync.file", ApprovalRequired, ApprovalRequired},
		{"tag.apply", "", ApprovalOptional},
		{"tag.apply", ApprovalNone, ApprovalOptional},
		{"tag.apply", ApprovalRequired, ApprovalRequired},
		{"tag.apply", "bogus", ApprovalOptional},
	} {
		got := NodeApproval(&Node{Type: TypeAction, Kind: tc.kind, Approval: tc.override})
		if got.Approval != tc.want {
			t.Errorf("%s/%q: got %q want %q", tc.kind, tc.override, got.Approval, tc.want)
		}
	}
	if !NodeApproval(&Node{Type: TypeAction, Kind: "sync.file", Approval: ApprovalRequired}).Required() {
		t.Error("override required should gate")
	}
}

func TestNodeApprovalOverrideGates(t *testing.T) {
	emit := func(o *Outcome, n *Node, _ Vars, _ *Event) string {
		o.EmitOp(Op{Kind: "tag.apply", Target: n.ID})
		return "emitted"
	}
	withTestPolicy(t, "tag.apply", emit, nil)
	a := &Automation{Schema: SchemaIDV2, SchemaVersion: SchemaVersionV2, Name: "override", Enabled: true,
		Nodes: []Node{
			{ID: "t1", Type: TypeTrigger, Kind: "file.created"},
			{ID: "a1", Type: TypeAction, Kind: "tag.apply"},
			{ID: "a2", Type: TypeAction, Kind: "tag.apply", Approval: ApprovalRequired},
		},
		Edges: []Edge{{From: "t1", To: "a1"}, {From: "a1", To: "a2"}},
	}
	if got := RequiresApproval(a); !reflect.DeepEqual(got, []string{"a2"}) {
		t.Errorf("RequiresApproval = %v, want [a2]", got)
	}
	ev := &Event{Type: EventFileCreated, Destination: "/d", File: &FileInfo{RelPath: "a.txt"}, Now: time.Unix(0, 0)}
	o := Evaluate(a, ev)
	if len(o.Ops) != 2 || o.Ops[0].NodeID != "a1" || o.Ops[0].Approval == OpApprovalPending ||
		o.Ops[1].NodeID != "a2" || o.Ops[1].Approval != OpApprovalPending {
		t.Fatalf("ops = %+v", o.Ops)
	}
}

func TestKindSpecApprovalPolicyUsesSpec(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "x.wipe", Destructive: true, RequiredModules: []string{ModuleAutomation}})
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "x.ask", Approval: ApprovalOptional, RequiredModules: []string{ModuleAutomation}})
	if p := r.Lookup(TypeAction, "x.wipe").ApprovalPolicy(); !p.Destructive || p.Approval != ApprovalRequired || !p.Required() {
		t.Errorf("destructive spec policy = %+v", p)
	}
	if p := r.Lookup(TypeAction, "x.ask").ApprovalPolicy(); p.Destructive || p.Approval != ApprovalOptional {
		t.Errorf("optional spec policy = %+v", p)
	}
}

func TestKindsSupportedBy(t *testing.T) {
	all := []string{ModuleCore, ModuleAutomation, ModuleSync}
	if got := len(KindsSupportedBy(SchemaVersionV1, all)); got != 27 {
		t.Errorf("all modules: %d kinds, want 27", got)
	}
	auto := KindsSupportedBy(CurrentSchemaVersion, []string{ModuleAutomation})
	if len(auto) != 12 {
		t.Errorf("automation only: %d kinds, want 12", len(auto))
	}
	for _, k := range auto {
		if contains(k.RequiredModules, ModuleSync) {
			t.Errorf("%s needs sync", k.Kind)
		}
	}
	if len(KindsSupportedBy(SchemaVersionV1, nil)) != 0 || len(KindsSupportedBy(0, all)) != 0 {
		t.Error("no modules or version 0 should support nothing")
	}

	r := NewRegistry()
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "v1.act", RequiredModules: []string{ModuleAutomation}})
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "v2.act", MinSchemaVersion: 2, RequiredModules: []string{ModuleAutomation}})
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "srv.act", Runtimes: []string{RuntimeServer}})
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "vss.act", RequiredModules: []string{ModuleAutomation, ModuleVSS}})
	names := func(ks []KindSpec) string {
		var s []string
		for _, k := range ks {
			s = append(s, k.Kind)
		}
		return strings.Join(s, ",")
	}
	if got := names(r.KindsSupportedBy(1, []string{ModuleAutomation})); got != "v1.act" {
		t.Errorf("v1 client: %s", got)
	}
	if got := names(r.KindsSupportedBy(2, []string{ModuleAutomation, ModuleVSS})); got != "v1.act,v2.act,vss.act" {
		t.Errorf("v2 client: %s", got)
	}
}

func TestCheckSupportAndModules(t *testing.T) {
	a := medal(t)
	if got := RequiredModules(a); strings.Join(got, ",") != "automation,sync" {
		t.Errorf("RequiredModules = %v", got)
	}
	if err := CheckSupport(a, 1, []string{ModuleAutomation, ModuleSync}); err != nil {
		t.Errorf("supported doc: %v", err)
	}
	var ue *UnsupportedError
	err := CheckSupport(a, 1, []string{ModuleAutomation})
	if !errors.As(err, &ue) || ue.Module != ModuleSync || ue.NodeID != "trigger" {
		t.Errorf("missing module: %v", err)
	}
	v2 := Upgrade(a)
	if err := CheckSupport(v2, 1, []string{ModuleAutomation, ModuleSync}); !errors.As(err, &ue) || ue.SchemaVersion != 2 {
		t.Errorf("old client: %v", err)
	}
	s, res := Parse([]byte(serverDocV2))
	if !res.Valid() {
		t.Fatal(res.Error())
	}
	if err := CheckSupport(s, 2, []string{ModuleAutomation}); !errors.As(err, &ue) || ue.Runtime != RuntimeServer {
		t.Errorf("server doc: %v", err)
	}
}

func TestRegisterV2Metadata(t *testing.T) {
	r := NewRegistry()
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "wipe", Destructive: true, Runtimes: []string{RuntimeServer}})
	r.MustRegister(KindSpec{Type: TypeAction, Kind: "plain", Approval: ApprovalNone})
	cat := r.Catalog()[TypeAction]
	if cat[1].Kind != "wipe" || !cat[1].Destructive || cat[1].Approval != ApprovalRequired {
		t.Errorf("wipe = %+v", cat[1])
	}
	if cat[0].Approval != "" || len(cat[0].Runtimes) != 1 || cat[0].Runtimes[0] != RuntimeDevice {
		t.Errorf("plain = %+v", cat[0])
	}
	if b := string(mustJSON(t, cat[0])); strings.Contains(b, "destructive") || strings.Contains(b, `"approval"`) {
		t.Errorf("empty policy marshalled: %s", b)
	}
	if approvalPolicy(TypeAction, "wipe").Destructive {
		t.Error("a private registry leaked into the shared policy table")
	}
	for name, bad := range map[string]KindSpec{
		"bad runtime":  {Type: TypeAction, Kind: "a", Runtimes: []string{"cloud"}},
		"bad approval": {Type: TypeAction, Kind: "b", Approval: "maybe"},
		"bad version":  {Type: TypeAction, Kind: "c", MinSchemaVersion: 9},
	} {
		if r.Register(bad) == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	for _, k := range [][2]string{{TypeTrigger, "schedule.interval"}, {TypeCondition, "variable.compare"}, {TypeCondition, "group"}, {TypeAction, "notify.send"}} {
		spec := LookupKind(k[0], k[1])
		if spec == nil || !spec.SupportsRuntime(RuntimeServer) || !spec.SupportsRuntime(RuntimeDevice) {
			t.Errorf("%s should run on device and server", k[1])
		}
	}
	if LookupKind(TypeTrigger, "file.created").SupportsRuntime(RuntimeServer) {
		t.Error("file.created must be device only")
	}
}

func TestJSONSchemaV2AndGuide(t *testing.T) {
	b := string(mustJSON(t, JSONSchema()))
	for _, s := range []string{SchemaIDV2, `"runtime"`, `"schema_version"`, `"approval"`, `"x-runtimes"`} {
		if !strings.Contains(b, s) {
			t.Errorf("schema missing %s", s)
		}
	}
	g := AIGuide()
	for _, s := range []string{SchemaIDV2, `"runtime"`, "[runs on server]", "can only raise"} {
		if !strings.Contains(g, s) {
			t.Errorf("guide missing %q", s)
		}
	}
	if strings.Contains(g, "Actions never delete") {
		t.Error("guide still claims actions never delete")
	}
}
