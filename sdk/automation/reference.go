package automation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// JSONSchema returns a JSON Schema (draft 2020-12) describing the document
// format, v1 and v2. It is generated from the catalogue so it can never drift
// from the validator. Rules the schema cannot express (runtime support of each
// kind, approval overrides that may only tighten) are enforced by
// ValidateDocument.
func JSONSchema() map[string]any {
	defs := map[string]any{}
	var refs []any
	for _, t := range []string{TypeTrigger, TypeCondition, TypeAction} {
		kinds := Catalog()[t]
		for _, k := range kinds {
			name := t + ":" + k.Kind
			props := map[string]any{
				"id":       map[string]any{"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$"},
				"type":     map[string]any{"const": t},
				"kind":     map[string]any{"const": k.Kind},
				"label":    map[string]any{"type": "string", "maxLength": MaxNameLen},
				"params":   paramsSchema(k.Params),
				"position": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": "number"}, "y": map[string]any{"type": "number"}}},
			}
			if t == TypeAction {
				props["approval"] = map[string]any{
					"enum":        []string{string(ApprovalNone), string(ApprovalOptional), string(ApprovalRequired)},
					"description": "v2 only: raise this action's approval level; it can never be lower than the kind's catalogue policy",
				}
			}
			defs[name] = map[string]any{
				"type":                 "object",
				"description":          k.Title + ". " + k.Description,
				"required":             []string{"id", "type", "kind"},
				"additionalProperties": false,
				"properties":           props,
				"x-runtimes":           k.Runtimes,
			}
			refs = append(refs, map[string]any{"$ref": "#/$defs/" + name})
		}
	}
	condRefs := []any{}
	for _, k := range Catalog()[TypeCondition] {
		condRefs = append(condRefs, map[string]any{
			"type": "object", "additionalProperties": false, "required": []string{"kind"},
			"properties": map[string]any{"kind": map[string]any{"const": k.Kind}, "params": paramsSchema(k.Params)},
		})
	}
	defs["rule"] = map[string]any{"oneOf": condRefs}
	return map[string]any{
		"$schema":              "https://json-schema.org/draft/2020-12/schema",
		"$id":                  "https://vaultdock.dev/schema/automation-v2.json",
		"title":                "VaultDock automation",
		"type":                 "object",
		"required":             []string{"schema", "name", "nodes", "edges"},
		"additionalProperties": false,
		"properties": map[string]any{
			"schema":         map[string]any{"enum": []string{SchemaID, SchemaIDV2}},
			"schema_version": map[string]any{"const": SchemaVersionV2, "description": "v2 only: optional numeric copy of the schema version"},
			"runtime":        map[string]any{"enum": []string{RuntimeDevice, RuntimeServer}, "default": RuntimeDevice, "description": "v2 only: where the automation runs"},
			"name":           map[string]any{"type": "string", "minLength": 1, "maxLength": MaxNameLen},
			"description":    map[string]any{"type": "string", "maxLength": MaxDescLen},
			"enabled":        map[string]any{"type": "boolean"},
			"priority":       map[string]any{"type": "integer", "minimum": -1000, "maximum": 1000},
			"nodes":          map[string]any{"type": "array", "minItems": 2, "maxItems": MaxNodes, "items": map[string]any{"oneOf": refs}},
			"edges": map[string]any{"type": "array", "maxItems": MaxEdges, "items": map[string]any{
				"type": "object", "required": []string{"from", "to"}, "additionalProperties": false,
				"properties": map[string]any{
					"from": map[string]any{"type": "string"},
					"to":   map[string]any{"type": "string"},
					"port": map[string]any{"enum": []string{PortNext, PortTrue, PortFalse}, "description": "\"true\"/\"false\" for conditions, \"next\" (default) otherwise"},
				},
			}},
		},
		// v1 documents cannot use the v2 top-level fields.
		"if":    map[string]any{"properties": map[string]any{"schema": map[string]any{"const": SchemaID}}},
		"then":  map[string]any{"properties": map[string]any{"schema_version": false, "runtime": false}},
		"$defs": defs,
	}
}

func paramsSchema(ps []ParamSpec) map[string]any {
	props := map[string]any{}
	var req []string
	for _, p := range ps {
		props[p.Name] = paramSchema(p)
		if p.Required {
			req = append(req, p.Name)
		}
	}
	s := map[string]any{"type": "object", "additionalProperties": false, "properties": props}
	if len(req) > 0 {
		s["required"] = req
	}
	return s
}

func paramSchema(p ParamSpec) map[string]any {
	var s map[string]any
	switch p.Type {
	case PTString, PTTemplate, PTGlob:
		s = map[string]any{"type": "string", "maxLength": MaxStringParam}
	case PTVariable:
		var names []string
		for _, v := range variables {
			names = append(names, v.Name)
		}
		s = map[string]any{"enum": names}
	case PTStringList:
		s = map[string]any{"type": "array", "maxItems": MaxListItems, "items": map[string]any{"type": "string", "minLength": 1}}
	case PTInteger:
		s = map[string]any{"type": "integer"}
		if p.Min != nil {
			s["minimum"] = *p.Min
		}
		if p.Max != nil {
			s["maximum"] = *p.Max
		}
	case PTBytes:
		s = map[string]any{"oneOf": []any{
			map[string]any{"type": "integer", "minimum": 0},
			map[string]any{"type": "string", "pattern": `^\s*[0-9]+(\.[0-9]+)?\s*([KkMmGgTt]i?[Bb]?|[Bb]ytes?)?\s*$`},
		}}
	case PTEnum:
		s = map[string]any{"enum": p.Options}
	case PTBoolean:
		s = map[string]any{"type": "boolean"}
	case PTDate:
		s = map[string]any{"type": "string", "description": "YYYY-MM-DD or RFC 3339"}
	case PTRules:
		s = map[string]any{"type": "array", "minItems": 1, "maxItems": MaxRulesInGroup, "items": map[string]any{"$ref": "#/$defs/rule"}}
	default:
		s = map[string]any{}
	}
	if p.Description != "" {
		s["description"] = p.Description
	}
	if p.Default != nil {
		s["default"] = p.Default
	}
	return s
}

// ExampleJSON returns the Medal clips template as an indented example.
func ExampleJSON() string {
	b, _ := json.MarshalIndent(BuiltinTemplates()[0].Automation, "", "  ")
	return string(b)
}

// AIGuide returns plain-text instructions that can be pasted into an
// external AI assistant so it can generate valid automations.
func AIGuide() string {
	var b strings.Builder
	b.WriteString("# VaultDock automation format (" + SchemaID + ", " + SchemaIDV2 + ")\n\n")
	b.WriteString(`You are generating a JSON automation for VaultDock, a self-hosted file sync platform.
The desktop client evaluates device automations when local files change; the server runs server automations itself. Output ONE JSON object only.

## Document structure
- "schema": "` + SchemaID + `" (default; works on every client) or "` + SchemaIDV2 + `" (needed only for the v2 fields below)
- "name": short name (required), "description": optional text
- "enabled": true or false, "priority": integer -1000..1000 (higher wins conflicts; default 0)
- "nodes": array of nodes. Each node: {"id": "unique-id", "type": "trigger"|"condition"|"action", "kind": "<kind>", "label": "optional", "params": {...}}
- "edges": array of {"from": "<node id>", "to": "<node id>", "port": "next"|"true"|"false"}
  - Triggers and actions use port "next". Conditions MUST use port "true" or "false".
  - Edges must not form loops. Triggers cannot have incoming edges.
- Optional "position": {"x": number, "y": number} on nodes for the visual editor (auto-layout is used if missing).

## Version 2 fields (only with "schema": "` + SchemaIDV2 + `")
- "schema_version": optional, must be ` + fmt.Sprint(SchemaVersionV2) + ` when present.
- "runtime": "` + RuntimeDevice + `" (default: delivered to desktops) or "` + RuntimeServer + `" (run by the server). A server automation may only use kinds marked [runs on server] below.
- "approval" on an action node: "none", "optional" or "required". It can only raise the kind's approval policy, never lower it.
- Prefer v1 unless one of these fields is needed: older desktops only understand v1, and a v1 document is upgraded to v2 losslessly.

## Evaluation rules
- A file is uploaded by default when it passes the sync configuration's filters. Use "sync.skip" to prevent uploads.
- When a trigger matches, VaultDock follows its edges. A condition follows its "true" or "false" edges. Actions run and then follow "next".
- Destination folders without a leading "/" are relative to the sync configuration's destination folder.
- No current action deletes, moves or renames the user's local files; they only affect the server copy and notifications.
- Actions marked [destructive] or [approval required] never run on their own: the engine marks their operations pending until a person approves them on the server. A document cannot approve itself.
- Byte sizes may be numbers or strings like "50 MB" (1 MB = 1048576 bytes).
- Glob patterns: * (any characters except /), ** (any folders), ? (one character), [abc].
- Templates use {{variable}} with optional filters, e.g. {{file.created|date:"YYYY-MM"}} or {{file.parent_name|default:"Unsorted"}}.

`)
	b.WriteString("## Triggers\n")
	writeKinds(&b, TypeTrigger)
	b.WriteString("\n## Conditions (every condition also accepts \"negate\": true)\n")
	writeKinds(&b, TypeCondition)
	b.WriteString("\n## Actions\n")
	writeKinds(&b, TypeAction)
	b.WriteString("\n## Variables\n| name | type | description | example |\n|---|---|---|---|\n")
	for _, v := range variables {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", v.Name, v.Type, v.Description, v.Example)
	}
	b.WriteString("\nVariables starting with file. are only available for file triggers; sync.files_* and sync.error only for sync triggers.\n")
	b.WriteString("\n## Template filters\n")
	for _, f := range TemplateFilters {
		fmt.Fprintf(&b, "- %s: %s e.g. %s\n", f.Name, f.Description, f.Usage)
	}
	b.WriteString("\n## Complete example\n```json\n" + ExampleJSON() + "\n```\n")
	return b.String()
}

func writeKinds(b *strings.Builder, t string) {
	kinds := Catalog()[t]
	sort.Slice(kinds, func(i, j int) bool { return kinds[i].Kind < kinds[j].Kind })
	for _, k := range kinds {
		fmt.Fprintf(b, "- %s (%s): %s", k.Kind, k.Title, k.Description)
		if t != TypeTrigger && len(k.Contexts) > 0 {
			fmt.Fprintf(b, " [usable with %s triggers]", strings.Join(k.Contexts, "/"))
		}
		for _, rt := range k.Runtimes {
			if rt == RuntimeServer {
				b.WriteString(" [runs on server]")
			}
		}
		if k.MinSchemaVersion > SchemaVersionV1 {
			fmt.Fprintf(b, " [needs %s]", SchemaIDFor(k.MinSchemaVersion))
		}
		if k.Destructive {
			b.WriteString(" [destructive]")
		}
		if k.Approval == ApprovalRequired {
			b.WriteString(" [approval required]")
		}
		b.WriteString("\n")
		for _, p := range k.Params {
			if p.Name == "negate" {
				continue
			}
			req := "optional"
			if p.Required {
				req = "required"
			}
			extra := ""
			if len(p.Options) > 0 {
				extra = " one of: " + strings.Join(p.Options, ", ") + "."
			}
			ex := ""
			if p.Example != nil {
				eb, _ := json.Marshal(p.Example)
				ex = " Example: " + string(eb)
			}
			fmt.Fprintf(b, "    - %s (%s, %s): %s%s%s\n", p.Name, p.Type, req, p.Description, extra, ex)
		}
	}
}

// Template is a reusable automation shipped with VaultDock.
type BuiltinTemplate struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Description string      `json:"description"`
	Automation  *Automation `json:"automation"`
}

func pos(x, y float64) *Position { return &Position{X: x, Y: y} }

// BuiltinTemplates returns the templates bundled with VaultDock.
func BuiltinTemplates() []BuiltinTemplate {
	return []BuiltinTemplate{
		{ID: "builtin-medal-clips", Name: "Medal clips by game and month",
			Description: "Uploads finished Medal recordings into <game>/<year-month> folders, skips temporary or tiny files and tags clips by game.",
			Automation: &Automation{
				Schema: SchemaID, Name: "Medal clips by game and month", Enabled: true,
				Description: "Medal stores each game's clips in its own folder. Point a sync configuration at the Medal Clips folder; files directly in the root go to Unsorted.",
				Nodes: []Node{
					{ID: "trigger", Type: TypeTrigger, Kind: "file.created", Label: "New recording", Params: map[string]any{}, Position: pos(0, 40)},
					{ID: "modified", Type: TypeTrigger, Kind: "file.modified", Label: "Recording changed", Params: map[string]any{}, Position: pos(0, 200)},
					{ID: "is-video", Type: TypeCondition, Kind: "extension.in", Label: "Is a video", Params: map[string]any{"extensions": []any{"mp4", "mkv", "mov", "webm"}}, Position: pos(280, 120)},
					{ID: "is-temp", Type: TypeCondition, Kind: "group", Label: "Temporary or incomplete?", Position: pos(560, 40), Params: map[string]any{
						"mode": "any",
						"rules": []any{
							map[string]any{"kind": "name.matches", "params": map[string]any{"pattern": "*.tmp*"}},
							map[string]any{"kind": "name.contains", "params": map[string]any{"text": ".part"}},
							map[string]any{"kind": "name.matches", "params": map[string]any{"pattern": "~*"}},
							map[string]any{"kind": "size.compare", "params": map[string]any{"operator": "lt", "bytes": float64(1 << 20)}},
						},
					}},
					{ID: "skip-other", Type: TypeAction, Kind: "sync.skip", Label: "Skip non-video", Params: map[string]any{"reason": "Not a video file"}, Position: pos(560, 260)},
					{ID: "skip-temp", Type: TypeAction, Kind: "sync.skip", Label: "Skip temporary", Params: map[string]any{"reason": "Temporary, incomplete or smaller than 1 MB"}, Position: pos(860, -40)},
					{ID: "folder", Type: TypeAction, Kind: "dest.set_folder", Label: "Game / month folder", Params: map[string]any{"folder": `{{file.parent_name|default:"Unsorted"}}/{{file.created|date:"YYYY-MM"}}`}, Position: pos(860, 100)},
					{ID: "tag", Type: TypeAction, Kind: "tag.apply", Label: "Tag clip", Params: map[string]any{"tags": []any{"clips", `{{file.parent_name|default:"unsorted"|slug}}`}}, Position: pos(1140, 100)},
					{ID: "upload", Type: TypeAction, Kind: "sync.file", Label: "Upload", Params: map[string]any{}, Position: pos(1420, 100)},
				},
				Edges: []Edge{
					{From: "trigger", To: "is-video", Port: PortNext},
					{From: "modified", To: "is-video", Port: PortNext},
					{From: "is-video", To: "is-temp", Port: PortTrue},
					{From: "is-video", To: "skip-other", Port: PortFalse},
					{From: "is-temp", To: "skip-temp", Port: PortTrue},
					{From: "is-temp", To: "folder", Port: PortFalse},
					{From: "folder", To: "tag", Port: PortNext},
					{From: "tag", To: "upload", Port: PortNext},
				},
			}},
		{ID: "builtin-screenshots-by-month", Name: "Screenshots by month",
			Description: "Files images into year/month folders and tags them.",
			Automation: &Automation{
				Schema: SchemaID, Name: "Screenshots by month", Enabled: true,
				Nodes: []Node{
					{ID: "trigger", Type: TypeTrigger, Kind: "file.extension_matched", Params: map[string]any{"extensions": []any{"png", "jpg", "jpeg", "webp"}}, Position: pos(0, 80)},
					{ID: "folder", Type: TypeAction, Kind: "dest.set_folder", Params: map[string]any{"folder": `{{file.modified|date:"YYYY"}}/{{file.modified|date:"MM MMMM"}}`}, Position: pos(300, 80)},
					{ID: "tag", Type: TypeAction, Kind: "tag.apply", Params: map[string]any{"tags": []any{"screenshots"}}, Position: pos(600, 80)},
				},
				Edges: []Edge{{From: "trigger", To: "folder", Port: PortNext}, {From: "folder", To: "tag", Port: PortNext}},
			}},
		{ID: "builtin-skip-temp", Name: "Skip temporary and system files",
			Description: "Never upload common temporary, lock and system files.",
			Automation: &Automation{
				Schema: SchemaID, Name: "Skip temporary and system files", Enabled: true, Priority: 100,
				Nodes: []Node{
					{ID: "created", Type: TypeTrigger, Kind: "file.created", Params: map[string]any{}, Position: pos(0, 0)},
					{ID: "modified", Type: TypeTrigger, Kind: "file.modified", Params: map[string]any{}, Position: pos(0, 160)},
					{ID: "junk", Type: TypeCondition, Kind: "group", Position: pos(300, 80), Params: map[string]any{"mode": "any", "rules": []any{
						map[string]any{"kind": "extension.in", "params": map[string]any{"extensions": []any{"tmp", "temp", "part", "crdownload", "lock", "lnk"}}},
						map[string]any{"kind": "name.matches", "params": map[string]any{"pattern": "~$*"}},
						map[string]any{"kind": "variable.compare", "params": map[string]any{"variable": "file.name", "operator": "equals", "value": "desktop.ini"}},
						map[string]any{"kind": "variable.compare", "params": map[string]any{"variable": "file.name", "operator": "equals", "value": "Thumbs.db"}},
					}}},
					{ID: "skip", Type: TypeAction, Kind: "sync.skip", Params: map[string]any{"reason": "Temporary or system file"}, Position: pos(600, 80)},
				},
				Edges: []Edge{{From: "created", To: "junk", Port: PortNext}, {From: "modified", To: "junk", Port: PortNext}, {From: "junk", To: "skip", Port: PortTrue}},
			}},
		{ID: "builtin-notify-failure", Name: "Notify when a sync fails",
			Description: "Sends a warning notification whenever a sync run has failed transfers.",
			Automation: &Automation{
				Schema: SchemaID, Name: "Notify when a sync fails", Enabled: true,
				Nodes: []Node{
					{ID: "failed", Type: TypeTrigger, Kind: "sync.failed", Params: map[string]any{}, Position: pos(0, 0)},
					{ID: "notify", Type: TypeAction, Kind: "notify.send", Params: map[string]any{"title": "Sync failed: {{sync.config}}", "message": "{{sync.files_failed}} file(s) failed on {{device.name}}. {{sync.error}}", "level": "error", "channel": "both"}, Position: pos(300, 0)},
				},
				Edges: []Edge{{From: "failed", To: "notify", Port: PortNext}},
			}},
		{ID: "builtin-large-files", Name: "Archive large files",
			Description: "Files of 2 GB or more go into a Large files folder and you get a notification.",
			Automation: &Automation{
				Schema: SchemaID, Name: "Archive large files", Enabled: true,
				Nodes: []Node{
					{ID: "big", Type: TypeTrigger, Kind: "file.size_threshold", Params: map[string]any{"bytes": float64(2 << 30)}, Position: pos(0, 0)},
					{ID: "folder", Type: TypeAction, Kind: "dest.set_folder", Params: map[string]any{"folder": "/Large files/{{sync.config}}"}, Position: pos(300, 0)},
					{ID: "notify", Type: TypeAction, Kind: "notify.send", Params: map[string]any{"title": "Large file queued", "message": "{{file.name}} ({{file.size_mb}} MB)", "level": "info", "channel": "web"}, Position: pos(600, 0)},
				},
				Edges: []Edge{{From: "big", To: "folder", Port: PortNext}, {From: "folder", To: "notify", Port: PortNext}},
			}},
	}
}
