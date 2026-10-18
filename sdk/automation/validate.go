package automation

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Issue is a single validation problem, addressed by a JSON path.
type Issue struct {
	Path    string `json:"path"`
	NodeID  string `json:"node_id,omitempty"`
	Message string `json:"message"`
}

// ValidationResult collects errors (which block enabling) and warnings.
type ValidationResult struct {
	Errors   []Issue `json:"errors"`
	Warnings []Issue `json:"warnings"`
}

// Valid reports whether there are no errors.
func (r *ValidationResult) Valid() bool { return len(r.Errors) == 0 }

func (r *ValidationResult) errf(path, node, format string, a ...any) {
	r.Errors = append(r.Errors, Issue{Path: path, NodeID: node, Message: fmt.Sprintf(format, a...)})
}

func (r *ValidationResult) warnf(path, node, format string, a ...any) {
	r.Warnings = append(r.Warnings, Issue{Path: path, NodeID: node, Message: fmt.Sprintf(format, a...)})
}

// Error summarises the errors in one string.
func (r *ValidationResult) Error() string {
	var parts []string
	for _, e := range r.Errors {
		parts = append(parts, e.Path+": "+e.Message)
	}
	return strings.Join(parts, "; ")
}

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// Validate checks a document and normalises its params in place
// (extension lists lower-cased, byte strings converted, defaults filled).
func Validate(a *Automation) *ValidationResult {
	r := &ValidationResult{Errors: []Issue{}, Warnings: []Issue{}}
	if a.Schema != SchemaID {
		r.errf("schema", "", "must be %q", SchemaID)
	}
	a.Name = strings.TrimSpace(a.Name)
	if a.Name == "" {
		r.errf("name", "", "is required")
	} else if len(a.Name) > MaxNameLen {
		r.errf("name", "", "must be at most %d characters", MaxNameLen)
	}
	if len(a.Description) > MaxDescLen {
		r.errf("description", "", "must be at most %d characters", MaxDescLen)
	}
	if a.Priority < -1000 || a.Priority > 1000 {
		r.errf("priority", "", "must be between -1000 and 1000")
	}
	if len(a.Nodes) == 0 {
		r.errf("nodes", "", "must contain at least one trigger and one action")
		return r
	}
	if len(a.Nodes) > MaxNodes {
		r.errf("nodes", "", "must contain at most %d nodes", MaxNodes)
		return r
	}
	if len(a.Edges) > MaxEdges {
		r.errf("edges", "", "must contain at most %d edges", MaxEdges)
		return r
	}

	ids := map[string]*Node{}
	nTrig, nAct := 0, 0
	for i := range a.Nodes {
		n := &a.Nodes[i]
		p := fmt.Sprintf("nodes[%d]", i)
		if !nodeIDPattern.MatchString(n.ID) {
			r.errf(p+".id", n.ID, "must be 1-64 characters of letters, digits, '-' or '_'")
		} else if ids[n.ID] != nil {
			r.errf(p+".id", n.ID, "duplicate node id %q", n.ID)
		} else {
			ids[n.ID] = n
		}
		if len(n.Label) > MaxNameLen {
			r.errf(p+".label", n.ID, "must be at most %d characters", MaxNameLen)
		}
		switch n.Type {
		case TypeTrigger:
			nTrig++
		case TypeAction:
			nAct++
		case TypeCondition:
		default:
			r.errf(p+".type", n.ID, "must be one of trigger, condition, action (got %q)", n.Type)
			continue
		}
		spec := LookupKind(n.Type, n.Kind)
		if spec == nil {
			r.errf(p+".kind", n.ID, "unknown %s kind %q%s", n.Type, n.Kind, suggestKind(n.Type, n.Kind))
			continue
		}
		if n.Params == nil {
			n.Params = map[string]any{}
		}
		validateParams(r, p+".params", n.ID, spec, n.Params, 0)
	}
	if nTrig == 0 {
		r.errf("nodes", "", "at least one trigger node is required")
	}
	if nAct == 0 {
		r.errf("nodes", "", "at least one action node is required")
	}

	// Edges
	out := map[string][]Edge{}
	seen := map[string]bool{}
	for i := range a.Edges {
		e := &a.Edges[i]
		p := fmt.Sprintf("edges[%d]", i)
		from, to := ids[e.From], ids[e.To]
		if from == nil {
			r.errf(p+".from", e.From, "references unknown node %q", e.From)
			continue
		}
		if to == nil {
			r.errf(p+".to", e.To, "references unknown node %q", e.To)
			continue
		}
		if e.From == e.To {
			r.errf(p, e.From, "a node cannot connect to itself")
			continue
		}
		if to.Type == TypeTrigger {
			r.errf(p+".to", e.To, "triggers cannot have incoming connections")
			continue
		}
		if e.Port == "" {
			if from.Type == TypeCondition {
				r.errf(p+".port", e.From, "connections from a condition must set port to \"true\" or \"false\"")
				continue
			}
			e.Port = PortNext
		}
		switch from.Type {
		case TypeCondition:
			if e.Port != PortTrue && e.Port != PortFalse {
				r.errf(p+".port", e.From, "condition ports are \"true\" or \"false\" (got %q)", e.Port)
				continue
			}
		default:
			if e.Port != PortNext {
				r.errf(p+".port", e.From, "%s nodes only have the \"next\" port (got %q)", from.Type, e.Port)
				continue
			}
		}
		k := e.From + "|" + e.Port + "|" + e.To
		if seen[k] {
			r.warnf(p, e.From, "duplicate connection ignored")
			continue
		}
		seen[k] = true
		out[e.From] = append(out[e.From], *e)
	}
	if !r.Valid() {
		return r
	}

	if cyc := findCycle(a, out); cyc != nil {
		r.errf("edges", cyc[0], "connections form a loop: %s. Automations must not contain cycles", strings.Join(cyc, " -> "))
		return r
	}

	// Reachability and context checks per trigger.
	reached := map[string]bool{}
	for i := range a.Nodes {
		t := &a.Nodes[i]
		if t.Type != TypeTrigger {
			continue
		}
		spec := LookupKind(t.Type, t.Kind)
		reached[t.ID] = true
		if len(out[t.ID]) == 0 {
			r.warnf(fmt.Sprintf("nodes[%d]", i), t.ID, "trigger %q is not connected to anything", t.ID)
		}
		walk(t.ID, out, func(id string) {
			if id == t.ID {
				return
			}
			reached[id] = true
			n := ids[id]
			checkContext(r, a, n, spec)
		})
		checkPathConflicts(r, t, out, ids)
	}
	for i := range a.Nodes {
		n := &a.Nodes[i]
		if !reached[n.ID] {
			r.warnf(fmt.Sprintf("nodes[%d]", i), n.ID, "%s %q is not reachable from any trigger and will never run", n.Type, n.ID)
		}
		if n.Type == TypeCondition && len(out[n.ID]) == 0 {
			r.warnf(fmt.Sprintf("nodes[%d]", i), n.ID, "condition %q has no connected outputs", n.ID)
		}
	}
	dedupeIssues(r)
	return r
}

func dedupeIssues(r *ValidationResult) {
	seen := map[string]bool{}
	var e, w []Issue
	for _, i := range r.Errors {
		k := i.Path + i.Message
		if !seen[k] {
			seen[k] = true
			e = append(e, i)
		}
	}
	for _, i := range r.Warnings {
		k := i.Path + i.Message
		if !seen[k] {
			seen[k] = true
			w = append(w, i)
		}
	}
	r.Errors, r.Warnings = e, w
	if r.Errors == nil {
		r.Errors = []Issue{}
	}
	if r.Warnings == nil {
		r.Warnings = []Issue{}
	}
}

func nodeIndex(a *Automation, id string) int {
	for i := range a.Nodes {
		if a.Nodes[i].ID == id {
			return i
		}
	}
	return -1
}

func checkContext(r *ValidationResult, a *Automation, n *Node, trig *KindSpec) {
	spec := LookupKind(n.Type, n.Kind)
	p := fmt.Sprintf("nodes[%d]", nodeIndex(a, n.ID))
	if !contains(spec.Contexts, trig.Context) {
		r.errf(p+".kind", n.ID, "%s %q cannot be used after the %q trigger (it needs a %s event)", n.Type, n.Kind, trig.Kind, strings.Join(spec.Contexts, " or "))
		return
	}
	if n.Kind == "group" {
		checkRuleContexts(r, p+".params.rules", n.ID, n.Params["rules"], trig)
	}
	checkTemplateContexts(r, p+".params", n, spec, trig.Context)
	if n.Kind == "variable.compare" {
		if v, _ := n.Params["variable"].(string); v != "" && !variableAvailable(v, trig.Context) {
			r.errf(p+".params.variable", n.ID, "variable %q is not available for the %q trigger", v, trig.Kind)
		}
	}
	if n.Kind == "sync.start_task" && trig.Context == CtxSync {
		r.warnf(p, n.ID, "starting a sync task from a sync event can loop; VaultDock limits chained runs, but check the target configuration")
	}
}

func checkRuleContexts(r *ValidationResult, path, nodeID string, rules any, trig *KindSpec) {
	list, _ := rules.([]any)
	for i, it := range list {
		m, _ := it.(map[string]any)
		kind, _ := m["kind"].(string)
		spec := LookupKind(TypeCondition, kind)
		if spec == nil {
			continue
		}
		p := fmt.Sprintf("%s[%d]", path, i)
		if !contains(spec.Contexts, trig.Context) {
			r.errf(p+".kind", nodeID, "condition %q cannot be used after the %q trigger", kind, trig.Kind)
		}
		params, _ := m["params"].(map[string]any)
		if kind == "group" {
			checkRuleContexts(r, p+".params.rules", nodeID, params["rules"], trig)
		}
		if kind == "variable.compare" {
			if v, _ := params["variable"].(string); v != "" && !variableAvailable(v, trig.Context) {
				r.errf(p+".params.variable", nodeID, "variable %q is not available for the %q trigger", v, trig.Kind)
			}
		}
	}
}

func checkTemplateContexts(r *ValidationResult, path string, n *Node, spec *KindSpec, ctx string) {
	for _, ps := range spec.Params {
		var strs []string
		switch {
		case ps.Type == PTTemplate:
			if s, ok := n.Params[ps.Name].(string); ok {
				strs = []string{s}
			}
		case ps.Type == PTStringList && ps.Templated:
			strs = toStrings(n.Params[ps.Name])
		}
		for _, s := range strs {
			t, err := ParseTemplate(s)
			if err != nil {
				continue // reported by param validation
			}
			for _, v := range t.Variables() {
				if VariableByName(v) != nil && !variableAvailable(v, ctx) {
					r.errf(path+"."+ps.Name, n.ID, "variable %q is not available for %s events", v, ctx)
				}
			}
		}
	}
}

// checkPathConflicts walks every route from a trigger and warns about
// contradictory actions on the same route.
func checkPathConflicts(r *ValidationResult, t *Node, out map[string][]Edge, ids map[string]*Node) {
	routes := 0
	var dfs func(id string, kinds map[string]int)
	dfs = func(id string, kinds map[string]int) {
		if routes > 2000 {
			return
		}
		n := ids[id]
		if n.Type == TypeAction {
			kinds = copyCounts(kinds)
			kinds[n.Kind]++
		}
		next := out[id]
		if len(next) == 0 {
			routes++
			if kinds["sync.file"] > 0 && kinds["sync.skip"] > 0 {
				r.warnf("edges", id, "a route from trigger %q both synchronises and skips the file; skip wins", t.ID)
			}
			if kinds["dest.set_folder"] > 1 {
				r.warnf("edges", id, "a route from trigger %q sets the destination folder more than once; the last one wins", t.ID)
			}
			if kinds["dest.rename"] > 1 {
				r.warnf("edges", id, "a route from trigger %q renames the file more than once; the last one wins", t.ID)
			}
			if kinds["sync.skip"] > 0 && (kinds["dest.set_folder"] > 0 || kinds["dest.rename"] > 0 || kinds["tag.apply"] > 0) {
				r.warnf("edges", id, "a route from trigger %q skips the file but also sets destination or tags, which will have no effect", t.ID)
			}
			return
		}
		for _, e := range next {
			dfs(e.To, kinds)
		}
	}
	dfs(t.ID, map[string]int{})
}

func copyCounts(m map[string]int) map[string]int {
	c := make(map[string]int, len(m)+1)
	for k, v := range m {
		c[k] = v
	}
	return c
}

func walk(start string, out map[string][]Edge, visit func(string)) {
	seen := map[string]bool{}
	stack := []string{start}
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[id] {
			continue
		}
		seen[id] = true
		visit(id)
		for _, e := range out[id] {
			stack = append(stack, e.To)
		}
	}
}

func findCycle(a *Automation, out map[string][]Edge) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := map[string]int{}
	var stack []string
	var found []string
	var visit func(id string) bool
	visit = func(id string) bool {
		color[id] = grey
		stack = append(stack, id)
		for _, e := range out[id] {
			switch color[e.To] {
			case grey:
				for i, s := range stack {
					if s == e.To {
						found = append(append([]string{}, stack[i:]...), e.To)
						return true
					}
				}
			case white:
				if visit(e.To) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, n := range a.Nodes {
		if color[n.ID] == white && visit(n.ID) {
			return found
		}
	}
	return nil
}

func suggestKind(nodeType, kind string) string {
	names := DefaultRegistry.Kinds(nodeType)
	best, bestD := "", 1<<30
	for _, k := range names {
		if d := levenshtein(strings.ToLower(kind), k); d < bestD {
			best, bestD = k, d
		}
	}
	if best != "" && bestD <= 4 {
		return fmt.Sprintf(" (did you mean %q?)", best)
	}
	return fmt.Sprintf(" (valid kinds: %s)", strings.Join(names, ", "))
}

func levenshtein(a, b string) int {
	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev = cur
	}
	return prev[len(b)]
}

func validateParams(r *ValidationResult, path, nodeID string, spec *KindSpec, params map[string]any, depth int) {
	known := map[string]*ParamSpec{}
	for i := range spec.Params {
		known[spec.Params[i].Name] = &spec.Params[i]
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if known[k] == nil {
			var valid []string
			for _, p := range spec.Params {
				valid = append(valid, p.Name)
			}
			hint := "this kind has no parameters"
			if len(valid) > 0 {
				hint = "valid parameters: " + strings.Join(valid, ", ")
			}
			r.errf(path+"."+k, nodeID, "unknown parameter for %q (%s)", spec.Kind, hint)
		}
	}
	for _, ps := range spec.Params {
		v, present := params[ps.Name]
		if !present || v == nil {
			if ps.Required {
				r.errf(path+"."+ps.Name, nodeID, "is required (%s)", ps.Description)
			} else if ps.Default != nil {
				params[ps.Name] = ps.Default
			}
			continue
		}
		nv, err := normaliseParam(&ps, v, path+"."+ps.Name, nodeID, r, depth)
		if err != "" {
			r.errf(path+"."+ps.Name, nodeID, "%s", err)
			continue
		}
		params[ps.Name] = nv
	}
}

func normaliseParam(ps *ParamSpec, v any, path, nodeID string, r *ValidationResult, depth int) (any, string) {
	switch ps.Type {
	case PTString, PTTemplate, PTGlob, PTVariable:
		s, ok := v.(string)
		if !ok {
			return nil, "must be a string"
		}
		if len(s) > MaxStringParam {
			return nil, fmt.Sprintf("must be at most %d characters", MaxStringParam)
		}
		if ps.Required && strings.TrimSpace(s) == "" && ps.Type != PTGlob {
			return nil, "must not be empty"
		}
		switch ps.Type {
		case PTTemplate:
			if _, err := ParseTemplate(s); err != nil {
				return nil, "invalid template: " + err.Error()
			}
			if err := checkTemplateVars(s); err != "" {
				return nil, err
			}
		case PTGlob:
			if _, err := CompileGlob(s, true); err != nil {
				return nil, err.Error()
			}
		case PTVariable:
			if VariableByName(s) == nil {
				return nil, fmt.Sprintf("unknown variable %q (see the variable reference)", s)
			}
		}
		return s, ""
	case PTStringList:
		list, ok := v.([]any)
		if !ok {
			if ss, ok2 := v.([]string); ok2 {
				for _, s := range ss {
					list = append(list, s)
				}
			} else {
				return nil, "must be an array of strings"
			}
		}
		if ps.Required && len(list) == 0 {
			return nil, "must contain at least one item"
		}
		if len(list) > MaxListItems {
			return nil, fmt.Sprintf("must contain at most %d items", MaxListItems)
		}
		out := make([]any, 0, len(list))
		for i, it := range list {
			s, ok := it.(string)
			if !ok {
				return nil, fmt.Sprintf("item %d must be a string", i)
			}
			s = strings.TrimSpace(s)
			if s == "" || len(s) > MaxStringParam {
				return nil, fmt.Sprintf("item %d must be a non-empty string of at most %d characters", i, MaxStringParam)
			}
			if ps.Name == "extensions" {
				s = strings.ToLower(strings.TrimPrefix(s, "."))
				s = strings.TrimPrefix(s, "*.")
			}
			if ps.Templated {
				if _, err := ParseTemplate(s); err != nil {
					return nil, fmt.Sprintf("item %d: invalid template: %v", i, err)
				}
				if err := checkTemplateVars(s); err != "" {
					return nil, fmt.Sprintf("item %d: %s", i, err)
				}
			}
			out = append(out, s)
		}
		return out, ""
	case PTInteger:
		n, ok := toNumber(v)
		if !ok || n != math.Trunc(n) {
			return nil, "must be a whole number"
		}
		if ps.Min != nil && n < *ps.Min {
			return nil, fmt.Sprintf("must be at least %v", *ps.Min)
		}
		if ps.Max != nil && n > *ps.Max {
			return nil, fmt.Sprintf("must be at most %v", *ps.Max)
		}
		return n, ""
	case PTBytes:
		var n float64
		switch x := v.(type) {
		case string:
			b, err := ParseBytes(x)
			if err != nil {
				return nil, err.Error()
			}
			n = float64(b)
		default:
			f, ok := toNumber(v)
			if !ok {
				return nil, "must be a number of bytes (or a string such as \"50 MB\")"
			}
			n = f
		}
		if n < 0 || n != math.Trunc(n) || n > 1<<53 {
			return nil, "must be a non-negative whole number of bytes"
		}
		return n, ""
	case PTEnum:
		s, ok := v.(string)
		if !ok || !contains(ps.Options, s) {
			return nil, fmt.Sprintf("must be one of: %s", strings.Join(ps.Options, ", "))
		}
		return s, ""
	case PTBoolean:
		b, ok := v.(bool)
		if !ok {
			return nil, "must be true or false"
		}
		return b, ""
	case PTDate:
		s, ok := v.(string)
		if !ok {
			return nil, "must be a date string (YYYY-MM-DD or RFC 3339)"
		}
		if _, err := ParseDate(s); err != nil {
			return nil, "must be a date in the form YYYY-MM-DD or an RFC 3339 timestamp"
		}
		return s, ""
	case PTRules:
		list, ok := v.([]any)
		if !ok {
			return nil, "must be an array of {\"kind\", \"params\"} objects"
		}
		if len(list) == 0 {
			return nil, "must contain at least one rule"
		}
		if len(list) > MaxRulesInGroup {
			return nil, fmt.Sprintf("must contain at most %d rules", MaxRulesInGroup)
		}
		if depth >= MaxRuleDepth {
			return nil, fmt.Sprintf("groups can be nested at most %d levels deep", MaxRuleDepth)
		}
		for i, it := range list {
			p := fmt.Sprintf("%s[%d]", path, i)
			m, ok := it.(map[string]any)
			if !ok {
				r.errf(p, nodeID, "must be an object with \"kind\" and \"params\"")
				continue
			}
			for k := range m {
				if k != "kind" && k != "params" {
					r.errf(p+"."+k, nodeID, "unknown field (rules only have \"kind\" and \"params\")")
				}
			}
			kind, _ := m["kind"].(string)
			spec := LookupKind(TypeCondition, kind)
			if spec == nil {
				r.errf(p+".kind", nodeID, "unknown condition kind %q%s", kind, suggestKind(TypeCondition, kind))
				continue
			}
			params, ok := m["params"].(map[string]any)
			if !ok {
				if m["params"] != nil {
					r.errf(p+".params", nodeID, "must be an object")
					continue
				}
				params = map[string]any{}
				m["params"] = params
			}
			validateParams(r, p+".params", nodeID, spec, params, depth+1)
		}
		return list, ""
	}
	return nil, "unsupported parameter type " + ps.Type
}

func checkTemplateVars(s string) string {
	t, _ := ParseTemplate(s)
	for _, name := range t.Variables() {
		if VariableByName(name) == nil {
			return fmt.Sprintf("unknown variable %q in template (see the variable reference)", name)
		}
	}
	return ""
}

func toNumber(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f, err == nil
	}
	return 0, false
}

func toStrings(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, it := range x {
			if s, ok := it.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

var bytesPattern = regexp.MustCompile(`(?i)^\s*([0-9]+(?:\.[0-9]+)?)\s*([kmgt]?i?b?|bytes?)\s*$`)

// ParseBytes parses "1048576", "50 MB", "1.5GiB". KB/MB/GB use 1024 multiples,
// matching how Windows Explorer reports sizes.
func ParseBytes(s string) (int64, error) {
	m := bytesPattern.FindStringSubmatch(s)
	if m == nil {
		return 0, fmt.Errorf("invalid size %q (use a number of bytes or e.g. \"50 MB\")", s)
	}
	f, _ := strconv.ParseFloat(m[1], 64)
	unit := strings.ToLower(m[2])
	mult := 1.0
	switch {
	case strings.HasPrefix(unit, "k"):
		mult = 1 << 10
	case strings.HasPrefix(unit, "m"):
		mult = 1 << 20
	case strings.HasPrefix(unit, "g"):
		mult = 1 << 30
	case strings.HasPrefix(unit, "t"):
		mult = 1 << 40
	}
	return int64(math.Round(f * mult)), nil
}

// ParseDate parses YYYY-MM-DD (local midnight) or RFC 3339.
func ParseDate(s string) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}
