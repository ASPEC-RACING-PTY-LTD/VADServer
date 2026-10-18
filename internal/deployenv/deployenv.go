// Package deployenv reads the deployment environment schema
// (deploy/env.schema.json), computes the admin view of the running
// deployment (api-v1 §11A) and keeps the admin removal approvals that
// install.sh applies on its next upgrade.
//
// The server never edits the host .env; only install.sh does (DR4). Values
// of secret variables are never read into a reply.
package deployenv

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/deploy"
)

// Variable is one entry of env.schema.json. Fields the server does not use
// (type, generate, description, ...) are ignored.
type Variable struct {
	Name            string `json:"name"`
	Default         string `json:"default"`
	Required        bool   `json:"required"`
	Secret          bool   `json:"secret"`
	EnvFile         bool   `json:"env_file"`
	Managed         bool   `json:"managed"`
	Scope           string `json:"scope"`
	Since           string `json:"since"`
	Obsolete        bool   `json:"obsolete"`
	DeprecatedSince string `json:"deprecated_since"`
	Replacement     string `json:"replacement"`
	Description     string `json:"description"`
}

// Schema is the parsed env.schema.json.
type Schema struct {
	SchemaVersion int        `json:"schema_version"`
	Variables     []Variable `json:"variables"`
}

var nameRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidName reports whether s is a well-formed environment variable name.
func ValidName(s string) bool { return len(s) <= 128 && nameRE.MatchString(s) }

// Parse decodes and checks a schema document.
func Parse(b []byte) (*Schema, error) {
	var s Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("deployenv: parse schema: %w", err)
	}
	if s.SchemaVersion < 1 {
		return nil, errors.New("deployenv: schema_version missing")
	}
	seen := map[string]bool{}
	for _, v := range s.Variables {
		if !ValidName(v.Name) {
			return nil, fmt.Errorf("deployenv: invalid variable name %q", v.Name)
		}
		if seen[v.Name] {
			return nil, fmt.Errorf("deployenv: duplicate variable %s", v.Name)
		}
		seen[v.Name] = true
		if _, ok := parseVersion(v.Since); !ok {
			return nil, fmt.Errorf("deployenv: %s: invalid since %q", v.Name, v.Since)
		}
	}
	return &s, nil
}

// Embedded parses the schema compiled into the binary (deploy/embed.go).
func Embedded() (*Schema, error) { return Parse(deploy.EnvSchema) }

// Lookup returns the schema entry for name.
func (s *Schema) Lookup(name string) (Variable, bool) {
	for _, v := range s.Variables {
		if v.Name == name {
			return v, true
		}
	}
	return Variable{}, false
}

// baseline is the oldest "since" in the schema: variables present before the
// schema existed. Anything newer was added by an upgrade.
func (s *Schema) baseline() [3]int {
	var min [3]int
	for i, v := range s.Variables {
		ver, _ := parseVersion(v.Since)
		if i == 0 || compareVersion(ver, min) < 0 {
			min = ver
		}
	}
	return min
}

// LookupFunc reads one variable of the process environment (os.LookupEnv).
type LookupFunc func(string) (string, bool)

// View is the computed state of the deployment environment.
type View struct {
	wire.DeployEnvStatus
	// Missing lists required, non-obsolete variables of the running
	// container's environment that are unset (names only).
	Missing []string
}

// Compute builds the §11A view from the schema, the process environment and
// the pending approvals. Only variable presence is read from the
// environment, never a value, and defaults of secret variables are masked.
//
//   - added_by_upgrade: current variables kept in .env whose "since" is newer
//     than the schema baseline (install.sh appends them with their default).
//   - obsolete_present: obsolete variables set in the environment.
//   - pending_removals: the approvals file, sorted by name.
func Compute(s *Schema, lookup LookupFunc, pending []wire.DeployEnvRemovalApproval) View {
	v := View{DeployEnvStatus: wire.DeployEnvStatus{
		SchemaVersion:   s.SchemaVersion,
		AddedByUpgrade:  []wire.DeployEnvAdded{},
		ObsoletePresent: []wire.DeployEnvObsolete{},
		PendingRemovals: []wire.DeployEnvRemovalApproval{},
	}, Missing: []string{}}
	base := s.baseline()
	for _, e := range s.Variables {
		_, present := lookup(e.Name)
		if e.Obsolete {
			if present {
				v.ObsoletePresent = append(v.ObsoletePresent, wire.DeployEnvObsolete{
					Name: e.Name, DeprecatedSince: e.DeprecatedSince, Replacement: e.Replacement, Note: e.Description,
				})
			}
			continue
		}
		if e.Required && !present && e.Scope == "server" {
			v.Missing = append(v.Missing, e.Name)
		}
		since, _ := parseVersion(e.Since)
		if e.EnvFile && e.Scope != "installer" && compareVersion(since, base) > 0 {
			def := e.Default
			if e.Secret {
				def = ""
			}
			v.AddedByUpgrade = append(v.AddedByUpgrade, wire.DeployEnvAdded{Name: e.Name, Default: def, Since: e.Since})
		}
	}
	v.PendingRemovals = append(v.PendingRemovals, pending...)
	slices.SortFunc(v.PendingRemovals, func(a, b wire.DeployEnvRemovalApproval) int { return strings.Compare(a.Name, b.Name) })
	return v
}

// parseVersion reads "MAJOR.MINOR.PATCH" (a leading "v" is allowed).
func parseVersion(s string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(s, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

func compareVersion(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			if a[i] < b[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}
