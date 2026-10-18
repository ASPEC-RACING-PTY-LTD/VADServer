// Package restore reads files back out of a repository snapshot
// (backup-format-v1 §8, api-v1 §7): it selects token paths, walks snapshot
// trees, streams and verifies file contents, maps token paths to target
// folders and decides what to do with files that already exist. It does no
// file-system writes; the Windows client's restore engine and the
// standalone tool do those through system-file protection.
package restore

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// ErrSelection reports a malformed selection path.
var ErrSelection = errors.New("restore: invalid selection")

// MaxSelection bounds the number of selection rules.
const MaxSelection = 10000

type rule struct {
	comps   []string // lowercased token + components
	include bool
}

// Selector decides which snapshot token paths a restore covers. The rule
// whose path is the longest prefix of an item decides (include or
// exclude); with no matching rule an item is selected only when the
// selection has no include rules (an empty selection is the whole
// snapshot).
type Selector struct {
	rules    []rule
	includes bool
}

// splitPath splits a token path into lowercased token and components.
func splitPath(p string) ([]string, error) {
	tok, comps, err := snapshot.SplitTokenPath(strings.TrimSuffix(p, "/"))
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(comps)+1)
	out = append(out, strings.ToLower(tok))
	for _, c := range comps {
		out = append(out, strings.ToLower(c))
	}
	return out, nil
}

// NewSelector validates sel.
func NewSelector(sel []wire.RestoreSelection) (*Selector, error) {
	if len(sel) > MaxSelection {
		return nil, fmt.Errorf("%w: more than %d paths", ErrSelection, MaxSelection)
	}
	s := &Selector{}
	for _, r := range sel {
		comps, err := splitPath(r.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: %q: %w", ErrSelection, r.Path, err)
		}
		s.rules = append(s.rules, rule{comps: comps, include: r.Include})
		if r.Include {
			s.includes = true
		}
	}
	return s, nil
}

func prefixOf(prefix, comps []string) bool {
	if len(prefix) > len(comps) {
		return false
	}
	for i := range prefix {
		if prefix[i] != comps[i] {
			return false
		}
	}
	return true
}

// Selected reports whether the item at token path p is restored.
func (s *Selector) Selected(p string) bool {
	comps, err := splitPath(p)
	if err != nil {
		return false
	}
	return s.selected(comps)
}

func (s *Selector) selected(comps []string) bool {
	best := -1
	include := !s.includes
	for _, r := range s.rules {
		if len(r.comps) > best && prefixOf(r.comps, comps) {
			best, include = len(r.comps), r.include
		}
	}
	return include
}

// Descend reports whether a walk must enter the directory at token path p:
// it is selected itself, or an include rule lies below it.
func (s *Selector) Descend(p string) bool {
	comps, err := splitPath(p)
	if err != nil {
		return false
	}
	if s.selected(comps) {
		return true
	}
	for _, r := range s.rules {
		if r.include && len(r.comps) > len(comps) && prefixOf(comps, r.comps) {
			return true
		}
	}
	return false
}
