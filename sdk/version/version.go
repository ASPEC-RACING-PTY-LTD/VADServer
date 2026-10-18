// Package version compares VaultDock release versions ("1.2.3", "v1.2.3",
// "1.2.3-rc.1", or a git describe string such as "v1.2.3-4-gabcdef").
package version

import (
	"strconv"
	"strings"
)

// Parsed is a semantic version. Pre is the pre-release part, empty for a
// final release.
type Parsed struct {
	Major, Minor, Patch int
	Pre                 string
}

// Parse reads a version. ok is false for values that are not a version at all
// (for example "dev").
func Parse(v string) (p Parsed, ok bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexByte(v, '+'); i >= 0 {
		v = v[:i]
	}
	core, pre, _ := strings.Cut(v, "-")
	parts := strings.Split(core, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return p, false
	}
	nums := [3]int{}
	for i, s := range parts {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return p, false
		}
		nums[i] = n
	}
	// "1.2.3-4-gabcdef" (git describe) is a build after 1.2.3, not a pre-release.
	if fields := strings.Split(pre, "-"); len(fields) == 2 && strings.HasPrefix(fields[1], "g") {
		if _, err := strconv.Atoi(fields[0]); err == nil {
			pre = ""
		}
	}
	return Parsed{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre}, true
}

// Compare returns -1, 0 or 1. A final release sorts after its pre-releases.
func Compare(a, b Parsed) int {
	for _, d := range [3]int{a.Major - b.Major, a.Minor - b.Minor, a.Patch - b.Patch} {
		if d != 0 {
			if d < 0 {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	case a.Pre < b.Pre:
		return -1
	default:
		return 1
	}
}

// Newer reports whether latest is a newer release than current. When current
// is not a version (a development build) it returns false: such builds are
// never offered automatic updates.
func Newer(latest, current string) bool {
	l, ok1 := Parse(latest)
	c, ok2 := Parse(current)
	return ok1 && ok2 && Compare(l, c) > 0
}
