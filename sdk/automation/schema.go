// Package automation implements the VaultDock automation schema: the JSON
// document format, its catalogue of triggers/conditions/actions, validation,
// template rendering and the evaluation engine.
//
// The same package is compiled into the server (validation, dry runs,
// storage) and the desktop client (live evaluation against local file
// events), so the visual editor, the JSON format and the runtime always agree.
package automation

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// SchemaID identifies version 1 of the automation document format. It stays
// the default write version so documents keep working on older desktops;
// documents that need a v2 feature use SchemaIDV2.
const SchemaID = "vaultdock.automation/v1"

// SchemaIDV2 identifies version 2 of the document format. v2 is a strict
// superset of v1: it adds schema_version, runtime and per-node approval.
const SchemaIDV2 = "vaultdock.automation/v2"

// SchemaVersionV2 is the numeric version of SchemaIDV2.
const SchemaVersionV2 = 2

// CurrentSchemaVersion is the newest document version this package parses.
const CurrentSchemaVersion = SchemaVersionV2

// SchemaVersionOf maps a schema id to its numeric version, or 0 if unknown.
func SchemaVersionOf(id string) int {
	switch id {
	case SchemaID:
		return SchemaVersionV1
	case SchemaIDV2:
		return SchemaVersionV2
	}
	return 0
}

// SchemaIDFor maps a numeric version to its schema id, or "" if unknown.
func SchemaIDFor(version int) string {
	switch version {
	case SchemaVersionV1:
		return SchemaID
	case SchemaVersionV2:
		return SchemaIDV2
	}
	return ""
}

// Limits keep documents small and evaluation bounded.
const (
	MaxNodes        = 200
	MaxEdges        = 400
	MaxNameLen      = 120
	MaxDescLen      = 2000
	MaxRuleDepth    = 5
	MaxRulesInGroup = 50
	MaxStringParam  = 1024
	MaxListItems    = 100
)

// Node types.
const (
	TypeTrigger   = "trigger"
	TypeCondition = "condition"
	TypeAction    = "action"
)

// Edge ports.
const (
	PortNext  = "next"
	PortTrue  = "true"
	PortFalse = "false"
)

// Automation is the portable automation document.
type Automation struct {
	Schema      string `json:"schema"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	// Priority orders automations that act on the same file. Higher runs
	// later and wins conflicting decisions.
	Priority int    `json:"priority,omitempty"`
	Nodes    []Node `json:"nodes"`
	Edges    []Edge `json:"edges"`

	// v2 fields. All are omitted when empty so v1 documents marshal
	// byte-identically.

	// SchemaVersion optionally repeats the numeric version of Schema. When
	// set it must match Schema; only v2 documents may carry it.
	SchemaVersion int `json:"schema_version,omitempty"`
	// Runtime is where the automation runs: RuntimeDevice (default) or
	// RuntimeServer. Only v2 documents may set it.
	Runtime string `json:"runtime,omitempty"`
}

// Definition is the automation document; an alias kept for the migration
// and runtime APIs.
type Definition = Automation

// Version returns the numeric schema version of the document, or 0 if the
// schema id is unknown.
func (a *Automation) Version() int { return SchemaVersionOf(a.Schema) }

// EffectiveRuntime returns the document runtime, defaulting to RuntimeDevice.
func (a *Automation) EffectiveRuntime() string {
	if a.Runtime == "" {
		return RuntimeDevice
	}
	return a.Runtime
}

// Node is a single trigger, condition or action.
type Node struct {
	ID       string         `json:"id"`
	Type     string         `json:"type"`
	Kind     string         `json:"kind"`
	Label    string         `json:"label,omitempty"`
	Params   map[string]any `json:"params,omitempty"`
	Position *Position      `json:"position,omitempty"`
	// Approval (v2, actions only) raises the approval level of this node
	// above its kind's catalogue policy. It can never lower it.
	Approval ApprovalLevel `json:"approval,omitempty"`
}

// Position is optional layout information for the visual editor.
type Position struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// Edge connects the output port of one node to another node.
// Triggers and actions use the "next" port (default); conditions use
// "true" or "false".
type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Port string `json:"port,omitempty"`
}

// Parse decodes a document strictly (unknown top-level and node fields are
// rejected) and validates it with ValidateDocument, so both v1 and v2
// documents are accepted. The returned automation has normalised params.
func Parse(data []byte) (*Automation, *ValidationResult) {
	a, res := decodeStrict(data)
	if res != nil {
		return nil, res
	}
	return a, ValidateDocument(a)
}

func decodeStrict(data []byte) (*Automation, *ValidationResult) {
	var a Automation
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, &ValidationResult{Errors: []Issue{{Path: "$", Message: jsonErrorMessage(err)}}}
	}
	if dec.More() {
		return nil, &ValidationResult{Errors: []Issue{{Path: "$", Message: "unexpected data after the JSON document"}}}
	}
	return &a, nil
}

func jsonErrorMessage(err error) string {
	switch e := err.(type) {
	case *json.SyntaxError:
		return fmt.Sprintf("invalid JSON at byte %d: %v", e.Offset, e)
	case *json.UnmarshalTypeError:
		field := e.Field
		if field == "" {
			field = "document"
		}
		return fmt.Sprintf("%s: expected %s but got %s", field, e.Type.String(), e.Value)
	default:
		return "invalid JSON: " + err.Error()
	}
}

// NodeByID returns the node with the given id.
func (a *Automation) NodeByID(id string) *Node {
	for i := range a.Nodes {
		if a.Nodes[i].ID == id {
			return &a.Nodes[i]
		}
	}
	return nil
}

// Clone returns a deep copy via JSON round-trip.
func (a *Automation) Clone() *Automation {
	b, _ := json.Marshal(a)
	var c Automation
	_ = json.Unmarshal(b, &c)
	return &c
}
