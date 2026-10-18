package automation

// Contexts describe what data an event carries.
const (
	CtxFile     = "file"     // a single local file or folder event
	CtxSync     = "sync"     // the outcome of a sync run
	CtxSchedule = "schedule" // a timer tick with no file attached
)

// Param types.
const (
	PTString     = "string"
	PTTemplate   = "template"    // string that may contain {{variables}}
	PTStringList = "string_list" // array of strings
	PTInteger    = "integer"
	PTBytes      = "bytes" // non-negative integer byte count ("50 MB" strings are accepted on import)
	PTEnum       = "enum"
	PTBoolean    = "boolean"
	PTDate       = "date"     // YYYY-MM-DD or RFC 3339
	PTGlob       = "glob"     // glob pattern, supports * ? ** and [abc]
	PTRules      = "rules"    // nested condition rules (group condition only)
	PTVariable   = "variable" // name of an automation variable
)

// ParamSpec describes one parameter of a node kind.
type ParamSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Default     any      `json:"default,omitempty"`
	Options     []string `json:"options,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
	Description string   `json:"description"`
	Example     any      `json:"example,omitempty"`
	// Templated marks string_list params whose items may contain {{variables}}.
	Templated bool `json:"templated,omitempty"`
}

func f(v float64) *float64 { return &v }

var configsParam = ParamSpec{
	Name: "configs", Type: PTStringList,
	Description: "Only fire for these sync configuration names. Empty means every sync configuration the automation is attached to.",
	Example:     []string{"Medal Clips"},
}

var negateParam = ParamSpec{Name: "negate", Type: PTBoolean, Default: false, Description: "Invert the result of the condition."}

// Module sets required by the built-in kinds.
var (
	modsAutomation = []string{ModuleAutomation}
	modsSync       = []string{ModuleAutomation, ModuleSync}
)

var compareOps = []string{"gt", "gte", "lt", "lte", "eq"}

var triggers = []KindSpec{
	{Kind: "file.created", Title: "File created", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "A new file appeared in a synchronised folder (fires once the file has stopped changing).",
		Params:      []ParamSpec{configsParam}},
	{Kind: "file.modified", Title: "File modified", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "An existing synchronised file changed size or modification time.",
		Params:      []ParamSpec{configsParam}},
	{Kind: "file.renamed", Title: "File renamed", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "A file was renamed or moved inside a synchronised folder. file.old_path holds the previous relative path.",
		Params:      []ParamSpec{configsParam}},
	{Kind: "file.deleted", Title: "File deleted", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "A file was removed locally. Server copies are never deleted by this trigger; use it for notifications or tagging.",
		Params:      []ParamSpec{configsParam}},
	{Kind: "folder.changed", Title: "Folder changed", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "Any file event inside a folder whose relative path matches the pattern.",
		Params: []ParamSpec{
			{Name: "folder", Type: PTGlob, Required: false, Default: "**", Description: "Glob matched against the folder's path relative to the sync root (\"\" is the root).", Example: "Valorant/**"},
			configsParam,
		}},
	{Kind: "schedule.interval", Title: "Scheduled interval", Category: "Time", Context: CtxSchedule, RequiredModules: modsAutomation,
		Description: "Fires repeatedly on the desktop client at a fixed interval.",
		Params: []ParamSpec{
			{Name: "every_minutes", Type: PTInteger, Required: true, Min: f(5), Max: f(10080), Description: "Interval in minutes (5 minutes to 7 days).", Example: 60},
		}},
	{Kind: "sync.completed", Title: "Synchronisation completed", Category: "Sync events", Context: CtxSync, RequiredModules: modsSync,
		Description: "A sync run finished with no failed transfers.",
		Params:      []ParamSpec{configsParam}},
	{Kind: "sync.failed", Title: "Synchronisation failed", Category: "Sync events", Context: CtxSync, RequiredModules: modsSync,
		Description: "A sync run finished with one or more failed transfers, or could not run.",
		Params:      []ParamSpec{configsParam}},
	{Kind: "file.size_threshold", Title: "File size threshold reached", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "A file was created or modified and is at least the given size.",
		Params: []ParamSpec{
			{Name: "bytes", Type: PTBytes, Required: true, Description: "Minimum size in bytes.", Example: 104857600},
			configsParam,
		}},
	{Kind: "file.extension_matched", Title: "File extension matched", Category: "File events", Context: CtxFile, RequiredModules: modsSync,
		Description: "A file with one of the listed extensions was created or modified.",
		Params: []ParamSpec{
			{Name: "extensions", Type: PTStringList, Required: true, Description: "Extensions without the dot, case-insensitive.", Example: []string{"mp4", "mkv"}},
			configsParam,
		}},
}

var fileCtx = []string{CtxFile}
var allCtx = []string{CtxFile, CtxSync, CtxSchedule}

var conditions = []KindSpec{
	{Kind: "name.contains", Title: "Filename contains", Category: "Name", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "The file name contains the given text.",
		Params: []ParamSpec{
			{Name: "text", Type: PTString, Required: true, Description: "Text to look for.", Example: "highlight"},
			{Name: "case_sensitive", Type: PTBoolean, Default: false, Description: "Match case exactly."},
			negateParam,
		}},
	{Kind: "name.matches", Title: "Filename matches pattern", Category: "Name", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "The file name matches a glob pattern such as \"*.tmp\" or \"Replay_*\".",
		Params: []ParamSpec{
			{Name: "pattern", Type: PTGlob, Required: true, Description: "Glob pattern.", Example: "*.part"},
			{Name: "case_sensitive", Type: PTBoolean, Default: false, Description: "Match case exactly."},
			negateParam,
		}},
	{Kind: "extension.in", Title: "File extension matches", Category: "Name", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "The file extension is one of the listed values.",
		Params: []ParamSpec{
			{Name: "extensions", Type: PTStringList, Required: true, Description: "Extensions without the dot, case-insensitive.", Example: []string{"mp4", "mov"}},
			negateParam,
		}},
	{Kind: "size.compare", Title: "File size", Category: "Size", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "Compares the file size with a value.",
		Params: []ParamSpec{
			{Name: "operator", Type: PTEnum, Required: true, Options: compareOps, Description: "gt, gte, lt, lte or eq.", Example: "gt"},
			{Name: "bytes", Type: PTBytes, Required: true, Description: "Size in bytes.", Example: 1048576},
			negateParam,
		}},
	{Kind: "path.matches", Title: "Folder path matches", Category: "Path", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "The file's path relative to the sync root matches a glob. Use ** to match any number of folders.",
		Params: []ParamSpec{
			{Name: "pattern", Type: PTGlob, Required: true, Description: "Glob pattern over the relative path.", Example: "**/Temp/**"},
			{Name: "case_sensitive", Type: PTBoolean, Default: false, Description: "Match case exactly."},
			negateParam,
		}},
	{Kind: "file.age", Title: "File age", Category: "Time", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "Compares the number of days since the file was last modified.",
		Params: []ParamSpec{
			{Name: "operator", Type: PTEnum, Required: true, Options: compareOps, Description: "gt, gte, lt, lte or eq.", Example: "lt"},
			{Name: "days", Type: PTInteger, Required: true, Min: f(0), Max: f(36500), Description: "Age in whole days.", Example: 30},
			negateParam,
		}},
	{Kind: "date.compare", Title: "Date created or modified", Category: "Time", Contexts: fileCtx, RequiredModules: modsAutomation,
		Description: "Compares the file's created or modified time with a fixed date.",
		Params: []ParamSpec{
			{Name: "field", Type: PTEnum, Required: true, Options: []string{"created", "modified"}, Description: "Which timestamp to compare.", Example: "created"},
			{Name: "operator", Type: PTEnum, Required: true, Options: []string{"before", "after"}, Description: "before or after.", Example: "after"},
			{Name: "date", Type: PTDate, Required: true, Description: "YYYY-MM-DD (local midnight) or an RFC 3339 timestamp.", Example: "2026-01-01"},
			negateParam,
		}},
	{Kind: "variable.compare", Title: "Metadata matches", Category: "Advanced", Contexts: allCtx, RequiredModules: modsAutomation,
		Description: "Compares any automation variable (see the variable reference) with a value.",
		Params: []ParamSpec{
			{Name: "variable", Type: PTVariable, Required: true, Description: "Variable name, e.g. file.parent_name or sync.files_failed.", Example: "file.parent_name"},
			{Name: "operator", Type: PTEnum, Required: true, Options: []string{"equals", "not_equals", "contains", "starts_with", "ends_with", "matches", "gt", "gte", "lt", "lte", "exists", "empty"}, Description: "Comparison. matches uses a glob; gt/lt compare numbers or dates.", Example: "equals"},
			{Name: "value", Type: PTString, Description: "Value to compare with (ignored for exists/empty).", Example: "Valorant"},
			{Name: "case_sensitive", Type: PTBoolean, Default: false, Description: "Match case exactly for text comparisons."},
			negateParam,
		}},
	{Kind: "group", Title: "AND / OR group", Category: "Logic", Contexts: allCtx, RequiredModules: modsAutomation,
		Description: "Combines several rules. mode \"all\" is AND, \"any\" is OR. Rules can nest groups.",
		Params: []ParamSpec{
			{Name: "mode", Type: PTEnum, Required: true, Options: []string{"all", "any"}, Description: "all = every rule must pass (AND); any = at least one (OR).", Example: "any"},
			{Name: "rules", Type: PTRules, Required: true, Description: "List of {\"kind\": <condition kind>, \"params\": {...}} objects.",
				Example: []any{map[string]any{"kind": "extension.in", "params": map[string]any{"extensions": []string{"mp4"}}}}},
			negateParam,
		}},
}

var actions = []KindSpec{
	{Kind: "sync.file", Title: "Synchronise file", Category: "Sync", Contexts: fileCtx, RequiredModules: modsSync,
		Description: "Upload the file. Files that pass the sync configuration filters are uploaded by default; use this to make intent explicit.",
		Params:      []ParamSpec{}},
	{Kind: "sync.skip", Title: "Skip file", Category: "Sync", Contexts: fileCtx, RequiredModules: modsSync,
		Description: "Do not upload the file. Skip always wins over Synchronise for the same automation.",
		Params: []ParamSpec{
			{Name: "reason", Type: PTString, Description: "Shown in transfer history.", Example: "Temporary file"},
		}},
	{Kind: "dest.set_folder", Title: "Move to destination folder", Category: "Destination", Contexts: fileCtx, RequiredModules: modsSync,
		Description: "Upload into this folder instead of mirroring the local folder. Relative paths are inside the sync configuration's destination; paths starting with / are from the root of your storage. Supports {{variables}}.",
		Params: []ParamSpec{
			{Name: "folder", Type: PTTemplate, Required: true, Description: "Destination folder template.", Example: "{{file.parent_name|default:\"Unsorted\"}}/{{file.created|date:\"YYYY-MM\"}}"},
		}},
	{Kind: "dest.rename", Title: "Rename destination file", Category: "Destination", Contexts: fileCtx, RequiredModules: modsSync,
		Description: "Store the file under a different name. The local file is never renamed.",
		Params: []ParamSpec{
			{Name: "name", Type: PTTemplate, Required: true, Description: "File name template.", Example: "{{file.created|date:\"YYYY-MM-DD_HH-mm\"}}_{{file.stem}}.{{file.ext}}"},
		}},
	{Kind: "dest.create_folder", Title: "Create destination folder", Category: "Destination", Contexts: allCtx, RequiredModules: modsAutomation,
		Description: "Ensure a folder exists in your storage (created if missing).",
		Params: []ParamSpec{
			{Name: "folder", Type: PTTemplate, Required: true, Description: "Folder path template; relative paths are inside the sync destination.", Example: "/Archive/{{now|date:\"YYYY\"}}"},
		}},
	{Kind: "tag.apply", Title: "Apply tag", Category: "Organise", Contexts: fileCtx, RequiredModules: modsSync,
		Description: "Attach storage tags/categories to the uploaded file.",
		Params: []ParamSpec{
			{Name: "tags", Type: PTStringList, Required: true, Templated: true, Description: "Tags (templates allowed).", Example: []string{"clips", "{{file.parent_name|lower}}"}},
		}},
	{Kind: "sync.start_task", Title: "Start another sync task", Category: "Sync", Contexts: allCtx, RequiredModules: modsSync,
		Description: "Queue a scan of another sync configuration on the same device. Guarded against loops.",
		Params: []ParamSpec{
			{Name: "config", Type: PTString, Required: true, Description: "Name of the sync configuration to run.", Example: "Screenshots"},
		}},
	{Kind: "notify.send", Title: "Send notification", Category: "Notify", Contexts: allCtx, RequiredModules: modsAutomation,
		Description: "Show a notification in the web interface and/or the desktop app.",
		Params: []ParamSpec{
			{Name: "title", Type: PTTemplate, Required: true, Description: "Notification title.", Example: "New clip: {{file.name}}"},
			{Name: "message", Type: PTTemplate, Description: "Body text.", Example: "Uploaded {{file.size_mb}} MB"},
			{Name: "level", Type: PTEnum, Options: []string{"info", "success", "warning", "error"}, Default: "info", Description: "Severity."},
			{Name: "channel", Type: PTEnum, Options: []string{"both", "web", "desktop"}, Default: "both", Description: "Where to show it."},
		}},
}

// FileActionKinds are actions that change how a file is uploaded.
var FileActionKinds = map[string]bool{"sync.file": true, "sync.skip": true, "dest.set_folder": true, "dest.rename": true, "tag.apply": true}

// BuiltinKinds returns the built-in v1 kinds with their node type set.
// They are registered in DefaultRegistry.
func BuiltinKinds() []KindSpec {
	var out []KindSpec
	for _, g := range []struct {
		t     string
		kinds []KindSpec
	}{{TypeTrigger, triggers}, {TypeCondition, conditions}, {TypeAction, actions}} {
		for _, k := range g.kinds {
			k.Type = g.t
			out = append(out, k)
		}
	}
	return out
}

// LookupKind returns the spec for a node type and kind.
func LookupKind(nodeType, kind string) *KindSpec {
	return DefaultRegistry.Lookup(nodeType, kind)
}

// Catalog returns every kind spec grouped by node type.
func Catalog() map[string][]KindSpec {
	return DefaultRegistry.Catalog()
}

// VariableSpec documents a template/condition variable.
type VariableSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Example     string   `json:"example"`
	Contexts    []string `json:"contexts"`
	AppliesTo   string   `json:"applies_to"`
}

const fileTriggersText = "File triggers (file.*, folder.changed); all conditions and actions"
const syncTriggersText = "sync.completed, sync.failed"

var variables = []VariableSpec{
	{"file.name", "string", "File name including extension.", "Valorant 2026.10.16 - 21.04.11.mp4", fileCtx, fileTriggersText},
	{"file.stem", "string", "File name without the extension.", "Valorant 2026.10.16 - 21.04.11", fileCtx, fileTriggersText},
	{"file.ext", "string", "Lower-case extension without the dot (empty if none).", "mp4", fileCtx, fileTriggersText},
	{"file.size", "integer", "Size in bytes.", "254803968", fileCtx, fileTriggersText},
	{"file.size_mb", "number", "Size in mebibytes, rounded to 2 decimals.", "243.0", fileCtx, fileTriggersText},
	{"file.path", "string", "Full local path on the device.", `C:\Users\me\Videos\Medal\Clips\Valorant\clip.mp4`, fileCtx, fileTriggersText},
	{"file.rel_path", "string", "Path relative to the sync root, forward slashes.", "Valorant/clip.mp4", fileCtx, fileTriggersText},
	{"file.rel_dir", "string", "Folder part of rel_path (empty at the root).", "Valorant", fileCtx, fileTriggersText},
	{"file.parent_name", "string", "Name of the immediate parent folder inside the sync root; empty if the file is directly in the root. Medal stores clips in one folder per game, so this is the game name for Medal.", "Valorant", fileCtx, fileTriggersText},
	{"file.top_folder", "string", "First folder below the sync root (empty at the root).", "Valorant", fileCtx, fileTriggersText},
	{"file.created", "datetime", "Creation time (local time of the device).", "2026-10-16T21:04:11+11:00", fileCtx, fileTriggersText},
	{"file.modified", "datetime", "Last modification time.", "2026-10-16T21:05:02+11:00", fileCtx, fileTriggersText},
	{"file.age_days", "integer", "Whole days since last modification.", "3", fileCtx, fileTriggersText},
	{"file.mime", "string", "MIME type guessed from the extension.", "video/mp4", fileCtx, fileTriggersText},
	{"file.is_dir", "boolean", "true when the event is for a folder.", "false", fileCtx, fileTriggersText},
	{"file.hidden", "boolean", "true when the file has the hidden attribute or starts with a dot.", "false", fileCtx, fileTriggersText},
	{"file.readonly", "boolean", "true when the file is read-only.", "false", fileCtx, fileTriggersText},
	{"file.old_path", "string", "Previous relative path (file.renamed only, otherwise empty).", "Valorant/old-name.mp4", fileCtx, "file.renamed"},
	{"sync.config", "string", "Name of the sync configuration.", "Medal Clips", []string{CtxFile, CtxSync}, "File and sync triggers"},
	{"sync.root", "string", "Local root folder of the sync configuration.", `C:\Users\me\Videos\Medal\Clips`, []string{CtxFile, CtxSync}, "File and sync triggers"},
	{"sync.destination", "string", "Destination folder in VaultDock storage.", "/Medal", []string{CtxFile, CtxSync}, "File and sync triggers"},
	{"sync.files_uploaded", "integer", "Files uploaded in the run.", "12", []string{CtxSync}, syncTriggersText},
	{"sync.files_failed", "integer", "Files that failed in the run.", "1", []string{CtxSync}, syncTriggersText},
	{"sync.bytes_uploaded", "integer", "Bytes uploaded in the run.", "734003200", []string{CtxSync}, syncTriggersText},
	{"sync.error", "string", "First error message of a failed run (empty on success).", "connection reset by peer", []string{CtxSync}, "sync.failed"},
	{"device.name", "string", "Name of the device running the automation.", "GAMING-PC", allCtx, "All triggers"},
	{"event.type", "string", "Trigger event type.", "file.created", allCtx, "All triggers"},
	{"now", "datetime", "Time the automation ran.", "2026-10-16T21:05:30+11:00", allCtx, "All triggers"},
}

// Variables returns the variable reference.
func Variables() []VariableSpec { return variables }

// VariableByName looks up a variable spec.
func VariableByName(name string) *VariableSpec {
	for i := range variables {
		if variables[i].Name == name {
			return &variables[i]
		}
	}
	return nil
}

func variableAvailable(name, ctx string) bool {
	v := VariableByName(name)
	if v == nil {
		return false
	}
	for _, c := range v.Contexts {
		if c == ctx {
			return true
		}
	}
	return false
}

// TemplateFilters documents the filters supported in {{ }} templates.
var TemplateFilters = []struct {
	Name        string `json:"name"`
	Usage       string `json:"usage"`
	Description string `json:"description"`
}{
	{"date", `{{file.created|date:"YYYY-MM-DD"}}`, "Formats a datetime. Tokens: YYYY YY MMMM MMM MM DD dddd ddd HH mm ss."},
	{"default", `{{file.parent_name|default:"Unsorted"}}`, "Uses the fallback when the value is empty."},
	{"lower", `{{file.ext|lower}}`, "Lower-case."},
	{"upper", `{{file.ext|upper}}`, "Upper-case."},
	{"slug", `{{file.parent_name|slug}}`, "Lower-case, spaces and symbols replaced by '-'."},
	{"trim", `{{file.stem|trim}}`, "Removes surrounding whitespace."},
	{"truncate", `{{file.stem|truncate:40}}`, "Keeps at most N characters."},
	{"replace", `{{file.stem|replace:" ","_"}}`, "Replaces all occurrences of the first argument with the second."},
}
