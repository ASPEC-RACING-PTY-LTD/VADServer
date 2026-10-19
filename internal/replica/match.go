package replica

import (
	"path"
	"strings"
)

// Rule is the matching part of a copy rule.
type Rule struct {
	UserID           *int64
	SourceLocationID *int64
	PathPattern      string // glob on the file path, "**" crosses folders; empty = all
	Extensions       string // comma or space separated, without dots; empty = all
	MinSize          int64
	MaxSize          int64 // 0 = no limit
}

// File is one stored file the rule is tested against.
type File struct {
	UserID     int64
	LocationID int64
	Path       string // "/Medal/Clips/a.mp4"
	Size       int64
}

// Matches reports whether the rule wants a copy of f.
func (r Rule) Matches(f File) bool {
	if r.UserID != nil && *r.UserID != f.UserID {
		return false
	}
	if r.SourceLocationID != nil && *r.SourceLocationID != f.LocationID {
		return false
	}
	if f.Size < r.MinSize || (r.MaxSize > 0 && f.Size > r.MaxSize) {
		return false
	}
	if exts := SplitList(r.Extensions); len(exts) > 0 {
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(f.Path), "."))
		ok := false
		for _, e := range exts {
			ok = ok || e == ext
		}
		if !ok {
			return false
		}
	}
	if p := strings.TrimSpace(r.PathPattern); p != "" {
		return Glob(p, f.Path)
	}
	return true
}

// SplitList splits "mp4, mkv .mov" into lower-case items without dots.
func SplitList(s string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' }) {
		if f = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(f), ".")); f != "" {
			out = append(out, f)
		}
	}
	return out
}

// Glob matches a slash path against a pattern where "*" and "?" stay within
// one path segment and "**" matches any number of segments. Matching is
// case-insensitive. A pattern without a leading "/" matches anywhere
// ("*.mp4" = "**/*.mp4"); a pattern ending in "/" matches everything below
// that folder.
func Glob(pattern, name string) bool {
	pattern, name = strings.ToLower(strings.TrimSpace(pattern)), strings.ToLower(name)
	if strings.HasSuffix(pattern, "/") {
		pattern += "**"
	}
	if !strings.HasPrefix(pattern, "/") {
		pattern = "/**/" + pattern
	}
	return globSegs(strings.Split(strings.Trim(pattern, "/"), "/"), strings.Split(strings.Trim(name, "/"), "/"))
}

func globSegs(p, n []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			for i := 0; i <= len(n); i++ {
				if globSegs(p[1:], n[i:]) {
					return true
				}
			}
			return false
		}
		if len(n) == 0 {
			return false
		}
		if ok, err := path.Match(p[0], n[0]); err != nil || !ok {
			return false
		}
		p, n = p[1:], n[1:]
	}
	return len(n) == 0
}
