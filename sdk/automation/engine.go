package automation

import (
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/mimetypes"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/pathutil"
)

// Event types produced by the desktop client.
const (
	EventFileCreated   = "file.created"
	EventFileModified  = "file.modified"
	EventFileRenamed   = "file.renamed"
	EventFileDeleted   = "file.deleted"
	EventScheduleTick  = "schedule.tick"
	EventSyncCompleted = "sync.completed"
	EventSyncFailed    = "sync.failed"
)

// MaxSteps bounds the number of node visits per evaluation.
const MaxSteps = 1000

// FileInfo describes the file an event is about.
type FileInfo struct {
	Path       string    `json:"path"`
	RelPath    string    `json:"rel_path"`
	Size       int64     `json:"size"`
	Created    time.Time `json:"created"`
	Modified   time.Time `json:"modified"`
	IsDir      bool      `json:"is_dir,omitempty"`
	Hidden     bool      `json:"hidden,omitempty"`
	ReadOnly   bool      `json:"readonly,omitempty"`
	OldRelPath string    `json:"old_rel_path,omitempty"`
}

// SyncInfo summarises a finished sync run.
type SyncInfo struct {
	FilesUploaded int    `json:"files_uploaded"`
	FilesFailed   int    `json:"files_failed"`
	BytesUploaded int64  `json:"bytes_uploaded"`
	Error         string `json:"error,omitempty"`
}

// Event is the input to an evaluation.
type Event struct {
	Type          string    `json:"type"`
	Config        string    `json:"config,omitempty"`
	Root          string    `json:"root,omitempty"`
	Destination   string    `json:"destination,omitempty"`
	Device        string    `json:"device,omitempty"`
	File          *FileInfo `json:"file,omitempty"`
	Sync          *SyncInfo `json:"sync,omitempty"`
	TriggerNodeID string    `json:"trigger_node_id,omitempty"`
	Now           time.Time `json:"now"`
}

// Vars is the variable set used by templates and conditions.
type Vars map[string]any

// BuildVars computes the variable set for an event.
func BuildVars(ev *Event) Vars {
	now := ev.Now
	if now.IsZero() {
		now = time.Now()
	}
	v := Vars{
		"device.name":      ev.Device,
		"event.type":       ev.Type,
		"now":              now,
		"sync.config":      ev.Config,
		"sync.root":        ev.Root,
		"sync.destination": ev.Destination,
	}
	if f := ev.File; f != nil {
		rel := strings.Trim(strings.ReplaceAll(f.RelPath, `\`, "/"), "/")
		name := path.Base(rel)
		if rel == "" {
			name = path.Base(strings.ReplaceAll(f.Path, `\`, "/"))
		}
		ext := strings.TrimPrefix(path.Ext(name), ".")
		stem := strings.TrimSuffix(name, path.Ext(name))
		if f.IsDir {
			ext, stem = "", name
		}
		relDir := path.Dir(rel)
		if relDir == "." || relDir == "/" {
			relDir = ""
		}
		parent, top := "", ""
		if relDir != "" {
			parent = path.Base(relDir)
			top = strings.SplitN(relDir, "/", 2)[0]
		}
		age := int64(0)
		if !f.Modified.IsZero() && now.After(f.Modified) {
			age = int64(now.Sub(f.Modified) / (24 * time.Hour))
		}
		mt := ""
		if !f.IsDir {
			mt = mimetypes.ByName(name)
		}
		v["file.name"] = name
		v["file.stem"] = stem
		v["file.ext"] = strings.ToLower(ext)
		v["file.size"] = f.Size
		v["file.size_mb"] = math.Round(float64(f.Size)/(1<<20)*100) / 100
		v["file.path"] = f.Path
		v["file.rel_path"] = rel
		v["file.rel_dir"] = relDir
		v["file.parent_name"] = parent
		v["file.top_folder"] = top
		v["file.created"] = f.Created
		v["file.modified"] = f.Modified
		v["file.age_days"] = age
		v["file.mime"] = mt
		v["file.is_dir"] = f.IsDir
		v["file.hidden"] = f.Hidden || strings.HasPrefix(name, ".")
		v["file.readonly"] = f.ReadOnly
		v["file.old_path"] = strings.Trim(strings.ReplaceAll(f.OldRelPath, `\`, "/"), "/")
	}
	if s := ev.Sync; s != nil {
		v["sync.files_uploaded"] = int64(s.FilesUploaded)
		v["sync.files_failed"] = int64(s.FilesFailed)
		v["sync.bytes_uploaded"] = s.BytesUploaded
		v["sync.error"] = s.Error
	}
	return v
}

// Notification is produced by notify.send.
type Notification struct {
	Title   string `json:"title"`
	Message string `json:"message"`
	Level   string `json:"level"`
	Channel string `json:"channel"`
}

// TraceStep records one node visit, for dry runs and history.
type TraceStep struct {
	NodeID string `json:"node_id"`
	Type   string `json:"type"`
	Kind   string `json:"kind"`
	Result string `json:"result"`
	Detail string `json:"detail,omitempty"`
}

// Outcome is the result of evaluating one automation.
type Outcome struct {
	AutomationID   string         `json:"automation_id,omitempty"`
	AutomationName string         `json:"automation_name"`
	Priority       int            `json:"priority"`
	Triggered      bool           `json:"triggered"`
	Decision       string         `json:"decision,omitempty"` // "", "sync", "skip"
	SkipReason     string         `json:"skip_reason,omitempty"`
	DestFolder     string         `json:"dest_folder,omitempty"`
	DestAbsolute   bool           `json:"dest_absolute,omitempty"`
	DestName       string         `json:"dest_name,omitempty"`
	CreateFolders  []string       `json:"create_folders,omitempty"`
	Tags           []string       `json:"tags,omitempty"`
	StartTasks     []string       `json:"start_tasks,omitempty"`
	Notifications  []Notification `json:"notifications,omitempty"`
	// Ops are generic operations emitted by kinds without a dedicated
	// field above.
	Ops      []Op        `json:"ops,omitempty"`
	Trace    []TraceStep `json:"trace"`
	Warnings []string    `json:"warnings,omitempty"`
}

// Evaluate runs one automation against an event. Evaluation is pure: it
// never touches the filesystem or network; the caller applies the outcome.
func Evaluate(a *Automation, ev *Event) Outcome {
	o := Outcome{AutomationName: a.Name, Priority: a.Priority, Trace: []TraceStep{}}
	vars := BuildVars(ev)
	out := map[string][]Edge{}
	for _, e := range a.Edges {
		p := e.Port
		if p == "" {
			p = PortNext
		}
		e.Port = p
		out[e.From] = append(out[e.From], e)
	}
	visited := map[string]bool{}
	steps := 0
	var run func(id string)
	run = func(id string) {
		if visited[id] {
			return
		}
		steps++
		if steps > MaxSteps {
			o.Warnings = appendOnce(o.Warnings, "evaluation stopped after the step limit")
			return
		}
		visited[id] = true
		n := a.NodeByID(id)
		if n == nil {
			return
		}
		switch n.Type {
		case TypeCondition:
			ok, detail := evalCondition(n.Kind, n.Params, vars, 0)
			port := PortFalse
			if ok {
				port = PortTrue
			}
			o.Trace = append(o.Trace, TraceStep{NodeID: n.ID, Type: n.Type, Kind: n.Kind, Result: port, Detail: detail})
			for _, e := range out[id] {
				if e.Port == port {
					run(e.To)
				}
			}
		case TypeAction:
			detail := applyAction(&o, n, vars, ev)
			o.Trace = append(o.Trace, TraceStep{NodeID: n.ID, Type: n.Type, Kind: n.Kind, Result: "applied", Detail: detail})
			for _, e := range out[id] {
				run(e.To)
			}
		}
	}
	for i := range a.Nodes {
		n := &a.Nodes[i]
		if n.Type != TypeTrigger {
			continue
		}
		if ok, why := matchTrigger(n, ev, vars); !ok {
			if why != "" {
				o.Trace = append(o.Trace, TraceStep{NodeID: n.ID, Type: n.Type, Kind: n.Kind, Result: "not_matched", Detail: why})
			}
			continue
		}
		o.Triggered = true
		o.Trace = append(o.Trace, TraceStep{NodeID: n.ID, Type: n.Type, Kind: n.Kind, Result: "matched"})
		visited[n.ID] = true
		for _, e := range out[n.ID] {
			run(e.To)
		}
	}
	return o
}

func appendOnce(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func numParam(p map[string]any, k string) float64 {
	f, _ := toNumber(p[k])
	return f
}

func strParam(p map[string]any, k string) string {
	s, _ := p[k].(string)
	return s
}

func boolParam(p map[string]any, k string) bool {
	b, _ := p[k].(bool)
	return b
}

func isFileEvent(t string) bool {
	return t == EventFileCreated || t == EventFileModified || t == EventFileRenamed || t == EventFileDeleted
}

// TriggerFunc reports whether a trigger node matches an event, with an
// optional reason when it does not.
type TriggerFunc func(n *Node, ev *Event, vars Vars) (bool, string)

// ConditionFunc evaluates a condition (or a nested group rule) and returns
// the result with a human-readable detail. depth is the group nesting level.
type ConditionFunc func(p map[string]any, vars Vars, depth int) (bool, string)

// ActionFunc records an action's effect on the outcome and returns a detail
// for the trace. New kinds should emit generic ops (Outcome.EmitOp) rather
// than adding Outcome fields.
type ActionFunc func(o *Outcome, n *Node, vars Vars, ev *Event) string

// Handlers are the runtime functions for one kind; only the one matching the
// kind's node type is used.
type Handlers struct {
	Match TriggerFunc
	Eval  ConditionFunc
	Apply ActionFunc
}

var (
	handlersMu sync.RWMutex
	handlers   = map[string]Handlers{}
)

// RegisterHandlers attaches runtime handlers to a kind of DefaultRegistry.
// The kind must already be registered and the handler for its node type must
// be set. Registration normally happens in init.
func RegisterHandlers(nodeType, kind string, h Handlers) error {
	if DefaultRegistry.Lookup(nodeType, kind) == nil {
		return fmt.Errorf("automation: handlers for unknown %s %q", nodeType, kind)
	}
	if (nodeType == TypeTrigger && h.Match == nil) || (nodeType == TypeCondition && h.Eval == nil) || (nodeType == TypeAction && h.Apply == nil) {
		return fmt.Errorf("automation: handlers for %s %q: missing %s handler", nodeType, kind, nodeType)
	}
	handlersMu.Lock()
	defer handlersMu.Unlock()
	key := nodeType + "/" + kind
	if _, dup := handlers[key]; dup {
		return fmt.Errorf("automation: handlers for %s %q: already registered", nodeType, kind)
	}
	handlers[key] = h
	return nil
}

// MustRegisterHandlers is RegisterHandlers that panics on error.
func MustRegisterHandlers(nodeType, kind string, h Handlers) {
	if err := RegisterHandlers(nodeType, kind, h); err != nil {
		panic(err)
	}
}

func lookupHandlers(nodeType, kind string) Handlers {
	handlersMu.RLock()
	defer handlersMu.RUnlock()
	return handlers[nodeType+"/"+kind]
}

func init() {
	for _, k := range []string{"file.created", "file.modified", "file.renamed", "file.deleted"} {
		MustRegisterHandlers(TypeTrigger, k, Handlers{Match: matchFileEvent})
	}
	MustRegisterHandlers(TypeTrigger, "folder.changed", Handlers{Match: matchFolderChanged})
	MustRegisterHandlers(TypeTrigger, "schedule.interval", Handlers{Match: func(n *Node, ev *Event, _ Vars) (bool, string) {
		return ev.Type == EventScheduleTick && ev.TriggerNodeID == n.ID, ""
	}})
	for _, k := range []string{"sync.completed", "sync.failed"} {
		MustRegisterHandlers(TypeTrigger, k, Handlers{Match: func(n *Node, ev *Event, _ Vars) (bool, string) {
			return ev.Type == n.Kind, ""
		}})
	}
	MustRegisterHandlers(TypeTrigger, "file.size_threshold", Handlers{Match: matchSizeThreshold})
	MustRegisterHandlers(TypeTrigger, "file.extension_matched", Handlers{Match: matchExtension})

	for k, fn := range map[string]ConditionFunc{
		"name.contains":    condNameContains,
		"name.matches":     condNameMatches,
		"extension.in":     condExtensionIn,
		"size.compare":     condSizeCompare,
		"path.matches":     condPathMatches,
		"file.age":         condFileAge,
		"date.compare":     condDateCompare,
		"variable.compare": func(p map[string]any, vars Vars, _ int) (bool, string) { return compareVariable(p, vars) },
		"group":            condGroup,
	} {
		MustRegisterHandlers(TypeCondition, k, Handlers{Eval: fn})
	}

	for k, fn := range map[string]ActionFunc{
		"sync.file":          actSyncFile,
		"sync.skip":          actSyncSkip,
		"dest.set_folder":    actSetFolder,
		"dest.rename":        actRename,
		"dest.create_folder": actCreateFolder,
		"tag.apply":          actTagApply,
		"sync.start_task":    actStartTask,
		"notify.send":        actNotify,
	} {
		MustRegisterHandlers(TypeAction, k, Handlers{Apply: fn})
	}
}

func matchTrigger(n *Node, ev *Event, vars Vars) (bool, string) {
	if cfgs := toStrings(n.Params["configs"]); len(cfgs) > 0 {
		found := false
		for _, c := range cfgs {
			if strings.EqualFold(c, ev.Config) {
				found = true
			}
		}
		if !found {
			return false, fmt.Sprintf("sync configuration %q is not in the trigger's list", ev.Config)
		}
	}
	h := lookupHandlers(TypeTrigger, n.Kind)
	if h.Match == nil {
		return false, ""
	}
	return h.Match(n, ev, vars)
}

func matchFileEvent(n *Node, ev *Event, _ Vars) (bool, string) {
	return ev.Type == n.Kind && ev.File != nil && !ev.File.IsDir, ""
}

func matchFolderChanged(n *Node, ev *Event, vars Vars) (bool, string) {
	if !isFileEvent(ev.Type) || ev.File == nil {
		return false, ""
	}
	dir, _ := vars["file.rel_dir"].(string)
	if ev.File.IsDir {
		dir, _ = vars["file.rel_path"].(string)
	}
	pat := strParam(n.Params, "folder")
	if pat == "" {
		pat = "**"
	}
	if pat == "**" || MatchGlob(pat, dir, false) {
		return true, ""
	}
	return false, fmt.Sprintf("folder %q does not match %q", dir, pat)
}

func isFileWrite(ev *Event) bool {
	return (ev.Type == EventFileCreated || ev.Type == EventFileModified) && ev.File != nil && !ev.File.IsDir
}

func matchSizeThreshold(n *Node, ev *Event, _ Vars) (bool, string) {
	if !isFileWrite(ev) {
		return false, ""
	}
	if float64(ev.File.Size) >= numParam(n.Params, "bytes") {
		return true, ""
	}
	return false, fmt.Sprintf("size %d is below the threshold", ev.File.Size)
}

func matchExtension(n *Node, ev *Event, vars Vars) (bool, string) {
	if !isFileWrite(ev) {
		return false, ""
	}
	ext, _ := vars["file.ext"].(string)
	for _, e := range toStrings(n.Params["extensions"]) {
		if strings.EqualFold(e, ext) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("extension %q is not listed", ext)
}

func compareNum(op string, a, b float64) bool {
	switch op {
	case "gt":
		return a > b
	case "gte":
		return a >= b
	case "lt":
		return a < b
	case "lte":
		return a <= b
	case "eq":
		return a == b
	}
	return false
}

func evalCondition(kind string, p map[string]any, vars Vars, depth int) (bool, string) {
	ok, detail := evalConditionRaw(kind, p, vars, depth)
	if boolParam(p, "negate") {
		return !ok, "negated: " + detail
	}
	return ok, detail
}

func evalConditionRaw(kind string, p map[string]any, vars Vars, depth int) (bool, string) {
	h := lookupHandlers(TypeCondition, kind)
	if h.Eval == nil {
		return false, "unknown condition"
	}
	return h.Eval(p, vars, depth)
}

func condNameContains(p map[string]any, vars Vars, _ int) (bool, string) {
	name, _ := vars["file.name"].(string)
	text := strParam(p, "text")
	if boolParam(p, "case_sensitive") {
		return strings.Contains(name, text), fmt.Sprintf("%q contains %q", name, text)
	}
	return strings.Contains(strings.ToLower(name), strings.ToLower(text)), fmt.Sprintf("%q contains %q", name, text)
}

func condNameMatches(p map[string]any, vars Vars, _ int) (bool, string) {
	name, _ := vars["file.name"].(string)
	pat := strParam(p, "pattern")
	return MatchGlob(pat, name, boolParam(p, "case_sensitive")), fmt.Sprintf("%q matches %q", name, pat)
}

func condExtensionIn(p map[string]any, vars Vars, _ int) (bool, string) {
	ext, _ := vars["file.ext"].(string)
	for _, e := range toStrings(p["extensions"]) {
		if strings.EqualFold(e, ext) {
			return true, fmt.Sprintf("extension %q is listed", ext)
		}
	}
	return false, fmt.Sprintf("extension %q is not listed", ext)
}

func condSizeCompare(p map[string]any, vars Vars, _ int) (bool, string) {
	size, _ := vars["file.size"].(int64)
	op := strParam(p, "operator")
	b := numParam(p, "bytes")
	return compareNum(op, float64(size), b), fmt.Sprintf("size %d %s %.0f", size, op, b)
}

func condPathMatches(p map[string]any, vars Vars, _ int) (bool, string) {
	rel, _ := vars["file.rel_path"].(string)
	pat := strParam(p, "pattern")
	return MatchGlob(pat, rel, boolParam(p, "case_sensitive")), fmt.Sprintf("%q matches %q", rel, pat)
}

func condFileAge(p map[string]any, vars Vars, _ int) (bool, string) {
	age, _ := vars["file.age_days"].(int64)
	op := strParam(p, "operator")
	d := numParam(p, "days")
	return compareNum(op, float64(age), d), fmt.Sprintf("age %d days %s %.0f", age, op, d)
}

func condDateCompare(p map[string]any, vars Vars, _ int) (bool, string) {
	field := strParam(p, "field")
	t, _ := vars["file."+field].(time.Time)
	ref, err := ParseDate(strParam(p, "date"))
	if err != nil || t.IsZero() {
		return false, "date unavailable"
	}
	op := strParam(p, "operator")
	res := t.Before(ref)
	if op == "after" {
		res = t.After(ref)
	}
	return res, fmt.Sprintf("%s %s %s %s", field, t.Format(time.RFC3339), op, ref.Format(time.RFC3339))
}

func condGroup(p map[string]any, vars Vars, depth int) (bool, string) {
	mode := strParam(p, "mode")
	rules, _ := p["rules"].([]any)
	if depth > MaxRuleDepth {
		return false, "group nesting too deep"
	}
	var parts []string
	for _, it := range rules {
		m, _ := it.(map[string]any)
		k, _ := m["kind"].(string)
		rp, _ := m["params"].(map[string]any)
		if rp == nil {
			rp = map[string]any{}
		}
		ok, _ := evalCondition(k, rp, vars, depth+1)
		parts = append(parts, fmt.Sprintf("%s=%v", k, ok))
		if mode == "any" && ok {
			return true, "any: " + strings.Join(parts, ", ")
		}
		if mode != "any" && !ok {
			return false, "all: " + strings.Join(parts, ", ")
		}
	}
	return mode != "any", mode + ": " + strings.Join(parts, ", ")
}

func compareVariable(p map[string]any, vars Vars) (bool, string) {
	name := strParam(p, "variable")
	op := strParam(p, "operator")
	want := strParam(p, "value")
	raw, exists := vars[name]
	got := formatValue(raw)
	detail := fmt.Sprintf("%s (%q) %s %q", name, got, op, want)
	switch op {
	case "exists":
		return exists && raw != nil, detail
	case "empty":
		return strings.TrimSpace(got) == "", detail
	case "gt", "gte", "lt", "lte":
		if t, ok := raw.(time.Time); ok {
			ref, err := ParseDate(want)
			if err != nil {
				return false, detail + " (value is not a date)"
			}
			return compareNum(op, float64(t.UnixNano()), float64(ref.UnixNano())), detail
		}
		a, ok1 := toNumber(got)
		b, ok2 := toNumber(want)
		if !ok1 || !ok2 {
			return false, detail + " (not numeric)"
		}
		return compareNum(op, a, b), detail
	}
	cs := boolParam(p, "case_sensitive")
	g, w := got, want
	if !cs {
		g, w = strings.ToLower(g), strings.ToLower(w)
	}
	switch op {
	case "equals":
		return g == w, detail
	case "not_equals":
		return g != w, detail
	case "contains":
		return strings.Contains(g, w), detail
	case "starts_with":
		return strings.HasPrefix(g, w), detail
	case "ends_with":
		return strings.HasSuffix(g, w), detail
	case "matches":
		return MatchGlob(want, got, cs), detail
	}
	return false, detail
}

func render(tpl string, vars Vars) string {
	t, err := ParseTemplate(tpl)
	if err != nil {
		return ""
	}
	return t.Render(vars)
}

func applyAction(o *Outcome, n *Node, vars Vars, ev *Event) string {
	h := lookupHandlers(TypeAction, n.Kind)
	if h.Apply == nil {
		return ""
	}
	first := len(o.Ops)
	detail := h.Apply(o, n, vars, ev)
	markApproval(o.Ops[first:], n)
	return detail
}

func actSyncFile(o *Outcome, _ *Node, _ Vars, _ *Event) string {
	if o.Decision != "skip" {
		o.Decision = "sync"
	}
	return "file will be uploaded"
}

func actSyncSkip(o *Outcome, n *Node, _ Vars, _ *Event) string {
	o.Decision = "skip"
	o.SkipReason = strParam(n.Params, "reason")
	if o.SkipReason == "" {
		o.SkipReason = "skipped by automation " + strconv.Quote(o.AutomationName)
	}
	return o.SkipReason
}

func actSetFolder(o *Outcome, n *Node, vars Vars, _ *Event) string {
	raw := strings.TrimSpace(render(strParam(n.Params, "folder"), vars))
	o.DestAbsolute = strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`)
	o.DestFolder = pathutil.SanitizePath(raw)
	return "destination folder " + o.DestFolder
}

func actRename(o *Outcome, n *Node, vars Vars, _ *Event) string {
	fallback, _ := vars["file.name"].(string)
	o.DestName = pathutil.SanitizeName(render(strParam(n.Params, "name"), vars), fallback)
	return "destination name " + o.DestName
}

func actCreateFolder(o *Outcome, n *Node, vars Vars, ev *Event) string {
	raw := strings.TrimSpace(render(strParam(n.Params, "folder"), vars))
	folder := pathutil.SanitizePath(raw)
	if !(strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, `\`)) {
		folder = pathutil.SanitizePath(ev.Destination + "/" + folder)
	}
	o.CreateFolders = appendOnce(o.CreateFolders, folder)
	return "ensure folder " + folder
}

func actTagApply(o *Outcome, n *Node, vars Vars, _ *Event) string {
	var added []string
	for _, t := range toStrings(n.Params["tags"]) {
		tag := strings.TrimSpace(render(t, vars))
		if tag == "" {
			continue
		}
		if len(tag) > 64 {
			tag = tag[:64]
		}
		o.Tags = appendOnce(o.Tags, tag)
		added = append(added, tag)
	}
	return "tags " + strings.Join(added, ", ")
}

func actStartTask(o *Outcome, n *Node, _ Vars, _ *Event) string {
	cfg := strParam(n.Params, "config")
	o.StartTasks = appendOnce(o.StartTasks, cfg)
	return "queue sync configuration " + strconv.Quote(cfg)
}

func actNotify(o *Outcome, n *Node, vars Vars, _ *Event) string {
	p := n.Params
	nt := Notification{
		Title:   render(strParam(p, "title"), vars),
		Message: render(strParam(p, "message"), vars),
		Level:   strParam(p, "level"),
		Channel: strParam(p, "channel"),
	}
	if nt.Level == "" {
		nt.Level = "info"
	}
	if nt.Channel == "" {
		nt.Channel = "both"
	}
	o.Notifications = append(o.Notifications, nt)
	return "notify: " + nt.Title
}

// Named pairs an automation with its storage id.
type Named struct {
	ID         string
	Automation *Automation
}

// Plan is the merged result of every automation for one event.
type Plan struct {
	Decision      string         `json:"decision"` // "sync" or "skip"
	SkipReason    string         `json:"skip_reason,omitempty"`
	RemoteDir     string         `json:"remote_dir,omitempty"`
	RemoteName    string         `json:"remote_name,omitempty"`
	Tags          []string       `json:"tags,omitempty"`
	CreateFolders []string       `json:"create_folders,omitempty"`
	StartTasks    []string       `json:"start_tasks,omitempty"`
	Notifications []Notification `json:"notifications,omitempty"`
	Conflicts     []string       `json:"conflicts,omitempty"`
	// Ops merges the ops of every triggered outcome (see MergeOps).
	Ops      []Op      `json:"ops,omitempty"`
	Outcomes []Outcome `json:"outcomes"`
}

// EvaluateAll evaluates every enabled automation and merges the outcomes.
// Lower priority runs first; a later (higher priority) automation overrides
// decisions and destinations. At equal priority "skip" beats "sync".
func EvaluateAll(list []Named, ev *Event) Plan {
	plan := Plan{Decision: "sync", Outcomes: []Outcome{}}
	sorted := append([]Named(nil), list...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Automation.Priority < sorted[j].Automation.Priority })

	decisionPrio := math.MinInt
	decisionBy := ""
	explicit := ""
	var destDir, destBy, destName, nameBy string
	destAbs := false
	destPrio, namePrio := math.MinInt, math.MinInt

	for _, na := range sorted {
		o := Evaluate(na.Automation, ev)
		o.AutomationID = na.ID
		plan.Outcomes = append(plan.Outcomes, o)
		if !o.Triggered {
			continue
		}
		pr := na.Automation.Priority
		if o.Decision != "" {
			if explicit != "" && explicit != o.Decision {
				if pr == decisionPrio {
					plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%q and %q disagree about uploading the file at the same priority; skip wins", decisionBy, o.AutomationName))
				} else {
					plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%q overrides %q (higher priority)", o.AutomationName, decisionBy))
				}
			}
			if pr > decisionPrio || explicit == "" || (pr == decisionPrio && o.Decision == "skip") {
				explicit = o.Decision
				decisionPrio = pr
				decisionBy = o.AutomationName
				if o.Decision == "skip" {
					plan.SkipReason = o.SkipReason
				}
			}
		}
		if o.DestFolder != "" {
			if destBy != "" && destDir != o.DestFolder {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%q and %q set different destination folders; %q wins", destBy, o.AutomationName, winner(pr, destPrio, o.AutomationName, destBy)))
			}
			if pr >= destPrio {
				destDir, destAbs, destBy, destPrio = o.DestFolder, o.DestAbsolute, o.AutomationName, pr
			}
		}
		if o.DestName != "" {
			if nameBy != "" && destName != o.DestName {
				plan.Conflicts = append(plan.Conflicts, fmt.Sprintf("%q and %q rename the file differently; %q wins", nameBy, o.AutomationName, winner(pr, namePrio, o.AutomationName, nameBy)))
			}
			if pr >= namePrio {
				destName, nameBy, namePrio = o.DestName, o.AutomationName, pr
			}
		}
		for _, t := range o.Tags {
			plan.Tags = appendOnce(plan.Tags, t)
		}
		for _, f := range o.CreateFolders {
			plan.CreateFolders = appendOnce(plan.CreateFolders, f)
		}
		for _, s := range o.StartTasks {
			plan.StartTasks = appendOnce(plan.StartTasks, s)
		}
		plan.Notifications = append(plan.Notifications, o.Notifications...)
		plan.Ops = MergeOps(plan.Ops, o.Ops)
	}
	if explicit != "" {
		plan.Decision = explicit
	}
	if plan.Decision != "skip" {
		plan.SkipReason = ""
	}

	if ev.File != nil {
		vars := BuildVars(ev)
		relDir, _ := vars["file.rel_dir"].(string)
		name, _ := vars["file.name"].(string)
		base := ev.Destination
		if base == "" {
			base = "/"
		}
		switch {
		case destDir != "" && destAbs:
			plan.RemoteDir = destDir
		case destDir != "":
			plan.RemoteDir = pathutil.SanitizePath(base + "/" + destDir)
		default:
			plan.RemoteDir = pathutil.SanitizePath(base + "/" + relDir)
		}
		plan.RemoteName = name
		if destName != "" {
			plan.RemoteName = destName
		}
		plan.RemoteName = pathutil.SanitizeName(plan.RemoteName, "unnamed")
	}
	return plan
}

func winner(pr, prev int, cur, prevName string) string {
	if pr >= prev {
		return cur
	}
	return prevName
}
