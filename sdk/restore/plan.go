package restore

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/snapshot"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
)

// ErrUnmapped means a token path has no target (no override covers it and
// the target has no value for its token).
var ErrUnmapped = errors.New("restore: no target folder for this path")

// Mapper turns snapshot token paths into target Windows paths. Overrides
// (token path prefix → absolute folder) win, longest prefix first; other
// paths resolve through Tokens, the target profile's token values.
type Mapper struct {
	Tokens    snapshot.Tokens
	overrides []override
}

type override struct {
	comps []string
	to    string
}

// NewMapper validates the overrides. Each From must be a token path and
// each To an absolute Windows path (drive or UNC).
func NewMapper(tokens snapshot.Tokens, overrides []wire.RestorePathMapping) (*Mapper, error) {
	m := &Mapper{Tokens: tokens}
	for _, o := range overrides {
		comps, err := splitPath(o.From)
		if err != nil {
			return nil, fmt.Errorf("restore: override from %q: %w", o.From, err)
		}
		to := strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(o.To), "/", `\`), `\`)
		if !IsAbsWindows(to + `\`) {
			return nil, fmt.Errorf("restore: override target %q must be an absolute folder", o.To)
		}
		m.overrides = append(m.overrides, override{comps: comps, to: to})
	}
	sort.SliceStable(m.overrides, func(i, j int) bool { return len(m.overrides[i].comps) > len(m.overrides[j].comps) })
	return m, nil
}

// IsAbsWindows reports whether p is an absolute Windows path: `X:\…` or a
// UNC path `\\server\share\…`.
func IsAbsWindows(p string) bool {
	p = strings.ReplaceAll(p, "/", `\`)
	if len(p) >= 3 && p[1] == ':' && p[2] == '\\' {
		c := p[0] | 0x20
		return c >= 'a' && c <= 'z'
	}
	if strings.HasPrefix(p, `\\`) && !strings.HasPrefix(p, `\\?\`) && !strings.HasPrefix(p, `\\.\`) {
		parts := strings.SplitN(p[2:], `\`, 3)
		return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
	}
	return false
}

// Target returns the Windows path for token path p.
func (m *Mapper) Target(p string) (string, error) {
	comps, err := splitPath(p)
	if err != nil {
		return "", err
	}
	_, orig, _ := snapshot.SplitTokenPath(strings.TrimSuffix(p, "/"))
	for _, o := range m.overrides {
		if prefixOf(o.comps, comps) {
			rest := orig[len(o.comps)-1:]
			if len(rest) == 0 {
				return o.to, nil
			}
			return o.to + `\` + strings.Join(rest, `\`), nil
		}
	}
	t, err := m.Tokens.Resolve(strings.TrimSuffix(p, "/"))
	if errors.Is(err, snapshot.ErrTokenUnmapped) {
		return "", ErrUnmapped
	}
	return t, err
}

// Existing describes what is at a target path now.
type Existing struct {
	Exists bool
	IsDir  bool
	Size   int64
	MTime  int64 // Unix nanoseconds
}

// Decide returns the action for a file (wire.RestoreAction*) under policy:
//   - nothing there → restore;
//   - a directory there → skip (a file never replaces a folder);
//   - the same size and modification time → skip (already restored),
//     except under overwrite;
//   - skip → skip; overwrite → overwrite; rename → rename;
//   - newer → overwrite when the snapshot copy is newer, else skip.
func Decide(policy string, e snapshot.Entry, ex Existing) string {
	if !ex.Exists {
		return wire.RestoreActionRestore
	}
	if ex.IsDir {
		return wire.RestoreActionSkip
	}
	if policy != wire.RestoreConflictOverwrite && ex.Size == e.S && ex.MTime == e.M {
		return wire.RestoreActionSkip
	}
	switch policy {
	case wire.RestoreConflictOverwrite:
		return wire.RestoreActionOverwrite
	case wire.RestoreConflictRename:
		return wire.RestoreActionRename
	case wire.RestoreConflictNewer:
		if e.M > ex.MTime {
			return wire.RestoreActionOverwrite
		}
		return wire.RestoreActionSkip
	}
	return wire.RestoreActionSkip
}

// ValidPolicy reports whether p is a conflict policy ("" is not).
func ValidPolicy(p string) bool {
	switch p {
	case wire.RestoreConflictSkip, wire.RestoreConflictOverwrite, wire.RestoreConflictRename, wire.RestoreConflictNewer:
		return true
	}
	return false
}

// splitExt splits a Windows file name into base and extension (".txt");
// names starting with a dot have no extension.
func splitExt(name string) (string, string) {
	ext := path.Ext(name)
	if ext == name || ext == "." {
		return name, ""
	}
	return strings.TrimSuffix(name, ext), ext
}

// RenamedPath returns the first free "<name> (restored N)<ext>" beside
// target; exists reports whether a path is taken.
func RenamedPath(target string, exists func(string) bool) (string, error) {
	dir, name := splitWin(target)
	base, ext := splitExt(name)
	for n := 1; n <= 1000; n++ {
		label := " (restored)"
		if n > 1 {
			label = fmt.Sprintf(" (restored %d)", n)
		}
		p := dir + `\` + base + label + ext
		if !exists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("restore: no free name beside %s", target)
}

// ReplacedName is where a replaced file is kept when it cannot go to the
// Recycle Bin: "<name> (replaced yyyy-mm-dd hhmmss)<ext>" in the same
// folder (MASTER_PLAN Phase 2 decision P2-D2).
func ReplacedName(target string, at time.Time) string {
	dir, name := splitWin(target)
	base, ext := splitExt(name)
	return dir + `\` + base + " (replaced " + at.Format("2006-01-02 150405") + ")" + ext
}

// splitWin splits a Windows path into folder and last component.
func splitWin(p string) (string, string) {
	p = strings.TrimRight(strings.ReplaceAll(p, "/", `\`), `\`)
	i := strings.LastIndex(p, `\`)
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

// Summary counts what a selection covers.
type Summary struct {
	Files int64
	Dirs  int64
	Bytes int64
}

// Summarize walks the selection and counts it.
func Summarize(ctx context.Context, tr TreeReader, roots []snapshot.RootRef, sel *Selector) (Summary, error) {
	var s Summary
	err := Walk(ctx, tr, roots, sel, func(it Item) error {
		switch it.Entry.T {
		case snapshot.TypeDir:
			s.Dirs++
		case snapshot.TypeFile:
			s.Files++
			s.Bytes += it.Entry.S
		}
		return nil
	})
	return s, err
}
