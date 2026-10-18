package automation

import (
	"fmt"
	"sort"
)

// Document runtimes (schema v2). A device automation is delivered to and
// evaluated by desktops; a server automation is run by the server itself and
// may only use kinds whose KindSpec.Runtimes include RuntimeServer.
const (
	RuntimeDevice = "device"
	RuntimeServer = "server"
)

var knownRuntimes = map[string]bool{RuntimeDevice: true, RuntimeServer: true}

// ValidateDocument validates a v1 or v2 document and normalises its params
// in place. It runs the v1 rules of Validate (which only knows SchemaID) and
// then the version rules:
//   - v1 documents must not use v2 fields (schema_version, runtime, approval);
//   - schema_version, when set, must match schema;
//   - runtime must be "device" or "server", and every kind (including rule
//     kinds inside groups) must support it;
//   - a node approval is only allowed on actions and may only tighten the
//     kind's catalogue policy;
//   - every kind must be known to the document's schema version.
func ValidateDocument(a *Automation) *ValidationResult {
	v := a.Version()
	var r *ValidationResult
	switch v {
	case SchemaVersionV1:
		r = Validate(a)
	case SchemaVersionV2:
		// Validate only accepts SchemaID; the v1 rules apply unchanged to v2.
		r = validateAs(a, SchemaID)
	default:
		r = Validate(a)
		for i := range r.Errors {
			if r.Errors[i].Path == "schema" {
				r.Errors[i].Message = fmt.Sprintf("must be %q or %q", SchemaID, SchemaIDV2)
			}
		}
		return r
	}
	validateVersioned(r, a, v)
	return r
}

// validateAs runs Validate with the schema id temporarily replaced.
func validateAs(a *Automation, id string) *ValidationResult {
	saved := a.Schema
	a.Schema = id
	defer func() { a.Schema = saved }()
	return Validate(a)
}

func validateVersioned(r *ValidationResult, a *Automation, v int) {
	if a.SchemaVersion != 0 && a.SchemaVersion != v {
		r.errf("schema_version", "", "must be %d for schema %q", v, a.Schema)
	}
	if v < SchemaVersionV2 {
		if a.SchemaVersion != 0 {
			r.errf("schema_version", "", "requires schema %q", SchemaIDV2)
		}
		if a.Runtime != "" {
			r.errf("runtime", "", "requires schema %q", SchemaIDV2)
		}
	} else if a.Runtime != "" && !knownRuntimes[a.Runtime] {
		r.errf("runtime", "", "must be %q or %q (got %q)", RuntimeDevice, RuntimeServer, a.Runtime)
	}
	rt := a.EffectiveRuntime()
	for i := range a.Nodes {
		n := &a.Nodes[i]
		p := fmt.Sprintf("nodes[%d]", i)
		spec := LookupKind(n.Type, n.Kind)
		if n.Approval != "" {
			switch {
			case v < SchemaVersionV2:
				r.errf(p+".approval", n.ID, "requires schema %q", SchemaIDV2)
			case n.Type != TypeAction:
				r.errf(p+".approval", n.ID, "is only allowed on action nodes")
			case !validNodeApproval(n.Approval):
				r.errf(p+".approval", n.ID, "must be one of none, optional, required (got %q)", n.Approval)
			case spec != nil && approvalRank[n.Approval] < approvalRank[spec.policy().Approval]:
				r.errf(p+".approval", n.ID, "cannot lower the %q approval policy of %s", spec.policy().Approval, n.Kind)
			}
		}
		if spec == nil {
			continue // already reported by Validate
		}
		checkKindSupport(r, p+".kind", n.ID, spec, v, rt)
		if n.Type == TypeCondition {
			walkRuleKinds(n.Params, 0, func(path, kind string) {
				if rs := LookupKind(TypeCondition, kind); rs != nil {
					checkKindSupport(r, p+".params"+path, n.ID, rs, v, rt)
				}
			})
		}
	}
	dedupeIssues(r)
}

func checkKindSupport(r *ValidationResult, path, nodeID string, spec *KindSpec, v int, rt string) {
	if spec.MinSchemaVersion > v {
		r.errf(path, nodeID, "%s kind %q requires schema %q", spec.Type, spec.Kind, SchemaIDFor(spec.MinSchemaVersion))
	}
	if !spec.SupportsRuntime(rt) {
		r.errf(path, nodeID, "%s kind %q cannot run in the %s runtime", spec.Type, spec.Kind, rt)
	}
}

func validNodeApproval(l ApprovalLevel) bool {
	return l == ApprovalNone || l == ApprovalOptional || l == ApprovalRequired
}

// walkRuleKinds calls fn with the JSON path suffix and kind of every rule in
// a group condition's params, recursing into nested groups up to
// MaxRuleDepth. Malformed rules are skipped (Validate reports them).
func walkRuleKinds(params map[string]any, depth int, fn func(path, kind string)) {
	if depth >= MaxRuleDepth {
		return
	}
	rules, _ := params["rules"].([]any)
	for i, raw := range rules {
		rule, _ := raw.(map[string]any)
		kind, _ := rule["kind"].(string)
		if kind == "" {
			continue
		}
		path := fmt.Sprintf(".rules[%d]", i)
		fn(path, kind)
		if sub, ok := rule["params"].(map[string]any); ok {
			walkRuleKinds(sub, depth+1, func(p, k string) { fn(path+".params"+p, k) })
		}
	}
}

// NodeApproval returns the effective approval policy of a node: its kind's
// catalogue policy, tightened (never lowered) by the node's v2 approval
// override.
func NodeApproval(n *Node) ApprovalPolicy {
	var p ApprovalPolicy
	if spec := LookupKind(n.Type, n.Kind); spec != nil {
		p = spec.policy()
	} else {
		p = approvalPolicy(n.Type, n.Kind)
	}
	if validNodeApproval(n.Approval) && approvalRank[n.Approval] > approvalRank[p.Approval] {
		p.Approval = n.Approval
	}
	return p
}

// documentKinds calls fn for every node kind and every rule kind in a
// document, with the id of the node that holds it.
func documentKinds(a *Automation, fn func(nodeID, nodeType, kind string)) {
	for i := range a.Nodes {
		n := &a.Nodes[i]
		fn(n.ID, n.Type, n.Kind)
		if n.Type == TypeCondition {
			walkRuleKinds(n.Params, 0, func(_, k string) { fn(n.ID, TypeCondition, k) })
		}
	}
}

// RequiredModules returns the sorted union of the modules required by every
// kind used in the document (api-v1 §6A).
func RequiredModules(a *Automation) []string {
	set := map[string]bool{}
	documentKinds(a, func(_, t, k string) {
		if spec := LookupKind(t, k); spec != nil {
			for _, m := range spec.RequiredModules {
				set[m] = true
			}
		}
	})
	out := make([]string, 0, len(set))
	for m := range set {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// UnsupportedError explains why a device cannot run a document. Module is
// set when a required module is missing; the server answers that case with
// 409 module_unavailable and details.module (api-v1 §6A).
type UnsupportedError struct {
	NodeID string
	Kind   string
	// Module is the first missing module, if that is the reason.
	Module string
	// SchemaVersion is the document or kind version the device lacks, if
	// that is the reason.
	SchemaVersion int
	// Runtime is set when the document does not run on devices at all.
	Runtime string
}

func (e *UnsupportedError) Error() string {
	switch {
	case e.Runtime != "" && e.Kind != "":
		return fmt.Sprintf("automation: node %q (%s) only runs in the %s runtime", e.NodeID, e.Kind, e.Runtime)
	case e.Runtime != "":
		return fmt.Sprintf("automation: runs in the %s runtime, not on devices", e.Runtime)
	case e.Module != "":
		return fmt.Sprintf("automation: node %q (%s) needs module %q", e.NodeID, e.Kind, e.Module)
	case e.Kind != "" && e.SchemaVersion == 0:
		return fmt.Sprintf("automation: node %q uses unknown kind %q", e.NodeID, e.Kind)
	case e.Kind != "":
		return fmt.Sprintf("automation: node %q (%s) needs schema version %d", e.NodeID, e.Kind, e.SchemaVersion)
	default:
		return fmt.Sprintf("automation: document needs schema version %d", e.SchemaVersion)
	}
}

// CheckSupport reports whether a device that parses schema versions up to
// `version` and has `modules` enabled can run the document. It returns nil or
// an *UnsupportedError for the first reason found (document version, runtime,
// then nodes in document order).
func CheckSupport(a *Automation, version int, modules []string) error {
	if v := a.Version(); v == 0 || v > version {
		return &UnsupportedError{SchemaVersion: v}
	}
	if rt := a.EffectiveRuntime(); rt != RuntimeDevice {
		return &UnsupportedError{Runtime: rt}
	}
	have := make(map[string]bool, len(modules))
	for _, m := range modules {
		have[m] = true
	}
	var err *UnsupportedError
	documentKinds(a, func(id, t, k string) {
		if err != nil {
			return
		}
		spec := LookupKind(t, k)
		switch {
		case spec == nil:
			err = &UnsupportedError{NodeID: id, Kind: k}
		case spec.MinSchemaVersion > version:
			err = &UnsupportedError{NodeID: id, Kind: k, SchemaVersion: spec.MinSchemaVersion}
		case !spec.SupportsRuntime(RuntimeDevice):
			err = &UnsupportedError{NodeID: id, Kind: k, Runtime: RuntimeServer}
		default:
			if m := missingModule(spec.RequiredModules, have); m != "" {
				err = &UnsupportedError{NodeID: id, Kind: k, Module: m}
			}
		}
	})
	if err != nil {
		return err
	}
	return nil
}
