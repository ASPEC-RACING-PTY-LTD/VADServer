package automation

import (
	"fmt"
	"sort"
	"sync"
)

// SchemaVersionV1 is the numeric version of SchemaID. KindSpec.MinSchemaVersion
// is compared against it.
const SchemaVersionV1 = 1

// Module ids a kind can require (api-v1 §6A). The server only delivers an
// automation to a device that reports every module its kinds require.
const (
	ModuleCore       = "core"
	ModuleSync       = "sync"
	ModuleAutomation = "automation"
	ModuleProfiles   = "profiles"
	ModuleVSS        = "vss"
	ModuleDiscovery  = "discovery"
	ModuleReinstall  = "reinstall"
)

var knownModules = map[string]bool{
	ModuleCore: true, ModuleSync: true, ModuleAutomation: true, ModuleProfiles: true,
	ModuleVSS: true, ModuleDiscovery: true, ModuleReinstall: true,
}

// KindSpec describes a trigger, condition or action kind.
type KindSpec struct {
	// Type is the node category: TypeTrigger, TypeCondition or TypeAction.
	Type string `json:"type"`
	Kind string `json:"kind"`
	// Title is the human label.
	Title       string `json:"title"`
	Description string `json:"description"`
	// Category groups kinds in the editor palette (e.g. "File events").
	Category string      `json:"category"`
	Params   []ParamSpec `json:"params"`
	// Context is the event context a trigger produces.
	Context string `json:"context,omitempty"`
	// Contexts lists the contexts a condition or action can be used in.
	Contexts []string `json:"contexts,omitempty"`
	// Ports lists the output ports of the node.
	Ports []string `json:"ports"`
	// MinSchemaVersion is the first document schema version that knows
	// this kind (1 = vaultdock.automation/v1).
	MinSchemaVersion int `json:"min_schema_version,omitempty"`
	// RequiredModules lists the device module ids (api-v1 §6A) the kind
	// needs at runtime.
	RequiredModules []string `json:"required_modules,omitempty"`
	// Runtimes lists where the kind can run: RuntimeDevice and/or
	// RuntimeServer. Register defaults it to device only.
	Runtimes []string `json:"runtimes,omitempty"`
	// Destructive and Approval are the kind's approval policy (api-v1 §8,
	// see approval.go). Catalog fills them from the policy table; both are
	// omitted for kinds without a policy.
	Destructive bool          `json:"destructive,omitempty"`
	Approval    ApprovalLevel `json:"approval,omitempty"`
}

// SupportsRuntime reports whether the kind can run in the given runtime.
func (k *KindSpec) SupportsRuntime(rt string) bool {
	for _, r := range k.Runtimes {
		if r == rt {
			return true
		}
	}
	return false
}

// policy returns the kind's effective approval policy: its own fields
// tightened by the DefaultRegistry policy table.
func (k *KindSpec) policy() ApprovalPolicy {
	p := approvalPolicy(k.Type, k.Kind)
	p.Destructive = p.Destructive || k.Destructive
	if approvalRank[k.Approval] > approvalRank[p.Approval] {
		p.Approval = k.Approval
	}
	if p.Destructive {
		p.Approval = ApprovalRequired
	}
	return p
}

// Registry holds kind specs by node type and kind. It is safe for
// concurrent use; registration normally happens during package init.
type Registry struct {
	mu    sync.RWMutex
	kinds map[string]map[string]*KindSpec
	// shared marks DefaultRegistry, whose kind policies go into the
	// package approval policy table used by the engine.
	shared bool
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{kinds: map[string]map[string]*KindSpec{TypeTrigger: {}, TypeCondition: {}, TypeAction: {}}}
}

// Register adds a kind. Ports default to "next" (triggers, actions) or
// "true"/"false" (conditions), MinSchemaVersion defaults to 1 and Runtimes
// to device only. A Destructive/Approval policy registered into
// DefaultRegistry is also recorded in the approval policy table.
func (r *Registry) Register(spec KindSpec) error {
	byKind, ok := r.kinds[spec.Type]
	if !ok {
		return fmt.Errorf("automation: register %q: unknown node type %q", spec.Kind, spec.Type)
	}
	if spec.Kind == "" {
		return fmt.Errorf("automation: register: empty kind for type %q", spec.Type)
	}
	for _, m := range spec.RequiredModules {
		if !knownModules[m] {
			return fmt.Errorf("automation: register %s %q: unknown module %q", spec.Type, spec.Kind, m)
		}
	}
	if spec.Ports == nil {
		if spec.Type == TypeCondition {
			spec.Ports = []string{PortTrue, PortFalse}
		} else {
			spec.Ports = []string{PortNext}
		}
	}
	if spec.MinSchemaVersion == 0 {
		spec.MinSchemaVersion = SchemaVersionV1
	}
	if spec.MinSchemaVersion < SchemaVersionV1 || spec.MinSchemaVersion > CurrentSchemaVersion {
		return fmt.Errorf("automation: register %s %q: unsupported min_schema_version %d", spec.Type, spec.Kind, spec.MinSchemaVersion)
	}
	if len(spec.Runtimes) == 0 {
		spec.Runtimes = []string{RuntimeDevice}
	}
	for _, rt := range spec.Runtimes {
		if !knownRuntimes[rt] {
			return fmt.Errorf("automation: register %s %q: unknown runtime %q", spec.Type, spec.Kind, rt)
		}
	}
	if _, ok := approvalRank[spec.Approval]; !ok {
		return fmt.Errorf("automation: register %s %q: unknown approval level %q", spec.Type, spec.Kind, spec.Approval)
	}
	if spec.Destructive {
		spec.Approval = ApprovalRequired
	}
	if spec.Approval == ApprovalNone {
		spec.Approval = ""
	}
	r.mu.Lock()
	if _, dup := byKind[spec.Kind]; dup {
		r.mu.Unlock()
		return fmt.Errorf("automation: register %s %q: already registered", spec.Type, spec.Kind)
	}
	byKind[spec.Kind] = &spec
	r.mu.Unlock()
	if r.shared && (spec.Destructive || spec.Approval != "") {
		recordPolicy(spec.Type+"/"+spec.Kind, ApprovalPolicy{Destructive: spec.Destructive, Approval: spec.Approval})
	}
	return nil
}

// recordPolicy tightens the policy table entry for key, with the same rules
// as SetApprovalPolicy (which cannot be called here: it looks the kind up in
// DefaultRegistry, which may still be initialising).
func recordPolicy(key string, p ApprovalPolicy) {
	approvalMu.Lock()
	defer approvalMu.Unlock()
	cur := approvals[key]
	cur.Destructive = cur.Destructive || p.Destructive
	if approvalRank[p.Approval] > approvalRank[cur.Approval] {
		cur.Approval = p.Approval
	}
	approvals[key] = cur
}

// MustRegister is Register that panics on error, for init-time use.
func (r *Registry) MustRegister(spec KindSpec) {
	if err := r.Register(spec); err != nil {
		panic(err)
	}
}

// Lookup returns the spec for a node type and kind, or nil.
func (r *Registry) Lookup(nodeType, kind string) *KindSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.kinds[nodeType][kind]
}

// Kinds returns the sorted kind ids registered for a node type.
func (r *Registry) Kinds(nodeType string) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]string, 0, len(r.kinds[nodeType]))
	for k := range r.kinds[nodeType] {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Catalog returns copies of every spec grouped by node type, sorted by kind.
// Destructive and Approval carry the effective approval policy.
func (r *Registry) Catalog() map[string][]KindSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := map[string][]KindSpec{}
	for t, kinds := range r.kinds {
		for _, k := range kinds {
			c := *k
			p := k.policy()
			c.Destructive = p.Destructive
			c.Approval = p.Approval
			if c.Approval == ApprovalNone {
				c.Approval = ""
			}
			out[t] = append(out[t], c)
		}
		sort.Slice(out[t], func(i, j int) bool { return out[t][i].Kind < out[t][j].Kind })
	}
	return out
}

// builtinServerKinds are the v1 built-in kinds the server runtime can also
// run: a schedule trigger, the context-free conditions and notifications.
// Every other built-in kind needs local files or the sync engine.
var builtinServerKinds = map[string]bool{
	TypeTrigger + "/schedule.interval":  true,
	TypeCondition + "/variable.compare": true,
	TypeCondition + "/group":            true,
	TypeAction + "/notify.send":         true,
}

// DefaultRegistry holds the built-in kinds (BuiltinKinds) plus any kinds
// registered by other files of this package.
var DefaultRegistry = func() *Registry {
	r := NewRegistry()
	r.shared = true
	for _, s := range BuiltinKinds() {
		if s.Runtimes == nil && builtinServerKinds[s.Type+"/"+s.Kind] {
			s.Runtimes = []string{RuntimeDevice, RuntimeServer}
		}
		r.MustRegister(s)
	}
	return r
}()

// KindsSupportedBy returns the kinds of DefaultRegistry a device can run when
// it parses documents up to schema version `version` and has the given
// modules enabled (api-v1 §6A): MinSchemaVersion <= version, every required
// module present, and the device runtime supported. Sorted by type, then kind.
func KindsSupportedBy(version int, modules []string) []KindSpec {
	return DefaultRegistry.KindsSupportedBy(version, modules)
}

// KindsSupportedBy is the Registry form of the package-level function.
func (r *Registry) KindsSupportedBy(version int, modules []string) []KindSpec {
	have := make(map[string]bool, len(modules))
	for _, m := range modules {
		have[m] = true
	}
	var out []KindSpec
	cat := r.Catalog()
	for _, t := range []string{TypeTrigger, TypeCondition, TypeAction} {
		for _, k := range cat[t] {
			if k.MinSchemaVersion > version || !k.SupportsRuntime(RuntimeDevice) || missingModule(k.RequiredModules, have) != "" {
				continue
			}
			out = append(out, k)
		}
	}
	return out
}

// missingModule returns the first required module not in have, or "".
func missingModule(required []string, have map[string]bool) string {
	for _, m := range required {
		if !have[m] {
			return m
		}
	}
	return ""
}
