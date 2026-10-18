// Package modules defines the VaultDock install modes, module ids and the
// release component manifest (MASTER_PLAN §7.7, §7.14; api-v1 §6A).
//
// The core and the built-in modules ship in the installer; components are
// downloaded from the release URLs listed in a component manifest and are
// verified (SHA-256 always, Ed25519 manifest signature per D29) before use.
package modules

import "slices"

// ID is a module id as fixed by api-v1 §6A.
type ID string

// Module ids (api-v1 §6A, fixed for v1).
const (
	Core       ID = "core"
	Sync       ID = "sync"
	Automation ID = "automation"
	Profiles   ID = "profiles"
	VSS        ID = "vss"
	Discovery  ID = "discovery"
	Reinstall  ID = "reinstall"
)

// Kind says how a module is delivered.
type Kind string

const (
	KindCore      Kind = "core"      // the installer itself
	KindBuiltin   Kind = "builtin"   // built into core, enabled by mode
	KindComponent Kind = "component" // downloaded component
)

// Mode is an install mode.
type Mode string

const (
	ModeSimple   Mode = "simple"
	ModeAdvanced Mode = "advanced"
)

// Valid reports whether m is a known install mode.
func (m Mode) Valid() bool { return m == ModeSimple || m == ModeAdvanced }

// State is a module state as reported in PUT /device/modules.
type State string

const (
	StateEnabled    State = "enabled"
	StateDisabled   State = "disabled"
	StateInstalling State = "installing"
	StateFailed     State = "failed"
)

// Valid reports whether s is a known module state.
func (s State) Valid() bool {
	switch s {
	case StateEnabled, StateDisabled, StateInstalling, StateFailed:
		return true
	}
	return false
}

// All lists every module id in contract table order.
var All = []ID{Core, Sync, Automation, Profiles, VSS, Discovery, Reinstall}

var kinds = map[ID]Kind{
	Core:       KindCore,
	Sync:       KindBuiltin,
	Automation: KindBuiltin,
	Profiles:   KindBuiltin,
	VSS:        KindComponent,
	Discovery:  KindComponent,
	Reinstall:  KindComponent,
}

// Known reports whether id is a v1 module id.
func Known(id ID) bool { _, ok := kinds[id]; return ok }

// KindOf returns the delivery kind of id; ok is false for an unknown id.
func KindOf(id ID) (k Kind, ok bool) { k, ok = kinds[id]; return k, ok }

// IsBuiltin reports whether id is built into the core and toggled by mode.
func IsBuiltin(id ID) bool { return kinds[id] == KindBuiltin }

// IsComponent reports whether id is a downloaded component.
func IsComponent(id ID) bool { return kinds[id] == KindComponent }

// ModulesForMode returns the default module set for mode, in contract table
// order. An unknown mode yields nil.
func ModulesForMode(mode Mode) []ID {
	switch mode {
	case ModeSimple:
		return []ID{Core}
	case ModeAdvanced:
		return slices.Clone(All)
	}
	return nil
}

// ComponentsForMode returns the downloadable components mode needs.
func ComponentsForMode(mode Mode) []ID {
	var out []ID
	for _, id := range ModulesForMode(mode) {
		if IsComponent(id) {
			out = append(out, id)
		}
	}
	return out
}

// InMode reports whether id belongs to mode's default set.
func InMode(id ID, mode Mode) bool { return slices.Contains(ModulesForMode(mode), id) }
