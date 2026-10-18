package automation

import (
	"reflect"
	"testing"
	"time"
)

func TestMergeParams(t *testing.T) {
	for name, tc := range map[string]struct {
		defaults, overrides, want map[string]any
	}{
		"both empty":    {nil, map[string]any{}, nil},
		"defaults only": {map[string]any{"a": 1.0}, nil, map[string]any{"a": 1.0}},
		"override wins": {map[string]any{"a": 1.0, "b": "x"}, map[string]any{"a": 2.0}, map[string]any{"a": 2.0, "b": "x"}},
		"nil deletes":   {map[string]any{"a": 1.0, "b": "x"}, map[string]any{"b": nil}, map[string]any{"a": 1.0}},
		"list replaced": {map[string]any{"l": []any{"a", "b"}}, map[string]any{"l": []any{"c"}}, map[string]any{"l": []any{"c"}}},
		"nested merge": {
			map[string]any{"keep": map[string]any{"daily": 7.0, "weekly": 4.0}},
			map[string]any{"keep": map[string]any{"weekly": 8.0, "monthly": 12.0}},
			map[string]any{"keep": map[string]any{"daily": 7.0, "weekly": 8.0, "monthly": 12.0}},
		},
		"map replaces scalar": {map[string]any{"k": "s"}, map[string]any{"k": map[string]any{"x": true}}, map[string]any{"k": map[string]any{"x": true}}},
	} {
		got := MergeParams(tc.defaults, tc.overrides)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v, want %v", name, got, tc.want)
		}
	}

	// The result must not alias the inputs.
	def := map[string]any{"m": map[string]any{"x": 1.0}, "l": []any{"a"}}
	got := MergeParams(def, nil)
	got["m"].(map[string]any)["x"] = 2.0
	got["l"].([]any)[0] = "b"
	if def["m"].(map[string]any)["x"] != 1.0 || def["l"].([]any)[0] != "a" {
		t.Error("MergeParams result aliases its input")
	}
}

func TestNodeParams(t *testing.T) {
	spec := LookupKind(TypeAction, "notify.send")
	got := NodeParams(spec, map[string]any{"title": "Hi", "level": "error"})
	if got["title"] != "Hi" || got["level"] != "error" {
		t.Errorf("overrides lost: %v", got)
	}
	for _, ps := range spec.Params {
		if ps.Default != nil && ps.Name != "level" && !reflect.DeepEqual(got[ps.Name], ps.Default) {
			t.Errorf("default %s = %v, want %v", ps.Name, got[ps.Name], ps.Default)
		}
	}
	if NodeParams(nil, nil) != nil {
		t.Error("NodeParams(nil, nil) should be nil")
	}
}

func TestMergeOps(t *testing.T) {
	a := []Op{
		{Kind: "backup.run", Target: "job1", NodeID: "n1", Params: map[string]any{"mode": "full", "tags": []any{"a"}}},
		{Kind: "notify", Target: "", Params: map[string]any{"x": 1.0}},
	}
	b := []Op{
		{Kind: "retention.apply", Target: "job1", NodeID: "n3", Approval: OpApprovalPending},
		{Kind: "backup.run", Target: "job1", NodeID: "n2", Params: map[string]any{"mode": "incremental"}},
		{Kind: "backup.run", Target: "job2", NodeID: "n4"},
	}
	got := MergeOps(a, b)
	want := []Op{
		{Kind: "backup.run", Target: "job1", NodeID: "n2", Params: map[string]any{"mode": "incremental", "tags": []any{"a"}}},
		{Kind: "notify", Params: map[string]any{"x": 1.0}},
		{Kind: "retention.apply", Target: "job1", NodeID: "n3", Approval: OpApprovalPending},
		{Kind: "backup.run", Target: "job2", NodeID: "n4"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeOps:\n got %+v\nwant %+v", got, want)
	}
	if a[0].Params["mode"] != "full" {
		t.Error("MergeOps modified its input")
	}
	// A pending approval is sticky regardless of merge order.
	p := MergeOps([]Op{{Kind: "k", Approval: OpApprovalPending}}, []Op{{Kind: "k"}})
	if len(p) != 1 || p[0].Approval != OpApprovalPending {
		t.Errorf("pending approval dropped: %+v", p)
	}
	if MergeOps(nil, nil) != nil {
		t.Error("MergeOps(nil, nil) should be nil")
	}
}

func TestBuiltinHandlers(t *testing.T) {
	for _, k := range BuiltinKinds() {
		h := lookupHandlers(k.Type, k.Kind)
		if h.Match == nil && h.Eval == nil && h.Apply == nil {
			t.Errorf("%s %s has no handler", k.Type, k.Kind)
		}
		if k.ApprovalPolicy().Required() {
			t.Errorf("v1 kind %s must not require approval", k.Kind)
		}
	}
	for name, err := range map[string]error{
		"unknown kind":  RegisterHandlers(TypeAction, "no.such", Handlers{Apply: actSyncFile}),
		"wrong handler": RegisterHandlers(TypeAction, "notify.send", Handlers{Eval: condGroup}),
		"duplicate":     RegisterHandlers(TypeAction, "notify.send", Handlers{Apply: actNotify}),
	} {
		if err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// withTestPolicy swaps the handler and approval policy of a built-in action
// for the duration of a test, without registering kinds that would change the
// golden catalogue.
func withTestPolicy(t *testing.T, kind string, apply ActionFunc, p *ApprovalPolicy) {
	t.Helper()
	key := TypeAction + "/" + kind
	handlersMu.Lock()
	oldH := handlers[key]
	handlers[key] = Handlers{Apply: apply}
	handlersMu.Unlock()
	approvalMu.Lock()
	oldP, hadP := approvals[key]
	approvalMu.Unlock()
	if p != nil {
		if err := SetApprovalPolicy(TypeAction, kind, *p); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		handlersMu.Lock()
		handlers[key] = oldH
		handlersMu.Unlock()
		approvalMu.Lock()
		if hadP {
			approvals[key] = oldP
		} else {
			delete(approvals, key)
		}
		approvalMu.Unlock()
	})
}

func TestApprovalPolicyAndOps(t *testing.T) {
	emit := func(o *Outcome, n *Node, _ Vars, _ *Event) string {
		o.EmitOp(Op{Kind: "tag.apply", Target: "t", Params: map[string]any{"tags": n.Params["tags"]}})
		return "emitted"
	}
	withTestPolicy(t, "tag.apply", emit, &ApprovalPolicy{Destructive: true})

	if err := SetApprovalPolicy(TypeAction, "tag.apply", ApprovalPolicy{Approval: ApprovalNone}); err != nil {
		t.Fatal(err)
	}
	if p := LookupKind(TypeAction, "tag.apply").ApprovalPolicy(); !p.Destructive || p.Approval != ApprovalRequired {
		t.Fatalf("policy was lowered or not normalised: %+v", p)
	}
	if SetApprovalPolicy(TypeAction, "tag.apply", ApprovalPolicy{Approval: "maybe"}) == nil {
		t.Error("unknown level accepted")
	}
	if SetApprovalPolicy(TypeAction, "no.such", ApprovalPolicy{}) == nil {
		t.Error("unknown kind accepted")
	}

	a := &Automation{Schema: SchemaID, Name: "gate", Enabled: true,
		Nodes: []Node{
			{ID: "t1", Type: TypeTrigger, Kind: "file.created"},
			{ID: "a1", Type: TypeAction, Kind: "tag.apply", Params: map[string]any{"tags": []any{"x"}}},
			{ID: "a2", Type: TypeAction, Kind: "sync.file"},
			{ID: "a3", Type: TypeAction, Kind: "tag.apply", Params: map[string]any{"tags": []any{"y"}}},
		},
		Edges: []Edge{{From: "t1", To: "a1"}, {From: "a1", To: "a2"}, {From: "a2", To: "a3"}},
	}
	if got := RequiresApproval(a); !reflect.DeepEqual(got, []string{"a1", "a3"}) {
		t.Errorf("RequiresApproval = %v", got)
	}
	if RequiresApproval(nil) != nil {
		t.Error("RequiresApproval(nil) should be nil")
	}

	ev := &Event{Type: EventFileCreated, Destination: "/d", File: &FileInfo{RelPath: "a.txt"}, Now: time.Unix(0, 0)}
	o := Evaluate(a, ev)
	if len(o.Ops) != 2 || o.Ops[0].NodeID != "a1" || o.Ops[1].NodeID != "a3" || o.Ops[0].Approval != OpApprovalPending {
		t.Fatalf("outcome ops = %+v", o.Ops)
	}
	plan := EvaluateAll([]Named{{ID: "1", Automation: a}}, ev)
	if len(plan.Ops) != 1 || plan.Ops[0].NodeID != "a3" || plan.Ops[0].Approval != OpApprovalPending ||
		!reflect.DeepEqual(plan.Ops[0].Params["tags"], []any{"y"}) {
		t.Fatalf("plan ops = %+v", plan.Ops)
	}
	if plan.Decision != "sync" {
		t.Errorf("decision = %q", plan.Decision)
	}
}

func TestV1DocsEmitNoOps(t *testing.T) {
	for _, tpl := range BuiltinTemplates() {
		if got := RequiresApproval(tpl.Automation); got != nil {
			t.Errorf("%s: v1 template requires approval: %v", tpl.Name, got)
		}
	}
}
