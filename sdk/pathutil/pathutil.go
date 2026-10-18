// Package pathutil validates and normalises user-supplied file names and
// virtual paths. Virtual paths always use forward slashes, are rooted at "/"
// and never contain "." or ".." segments.
package pathutil

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxNameLength = 255
const MaxPathDepth = 64

var (
	ErrEmptyName    = errors.New("name must not be empty")
	ErrNameTooLong  = errors.New("name is longer than 255 bytes")
	ErrReservedName = errors.New("name is reserved")
	ErrInvalidChars = errors.New(`name contains a character that is not allowed (\ / : * ? " < > | or control characters)`)
	ErrTrailing     = errors.New("name must not end with a space or a dot")
	ErrInvalidUTF8  = errors.New("name is not valid UTF-8")
	ErrPathTooDeep  = errors.New("path is nested too deeply")
)

var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// ValidateName checks a single path segment. The rules are the intersection
// of what Linux and Windows accept so files can round-trip to any client.
func ValidateName(name string) error {
	if name == "" {
		return ErrEmptyName
	}
	if !utf8.ValidString(name) {
		return ErrInvalidUTF8
	}
	if len(name) > MaxNameLength {
		return ErrNameTooLong
	}
	if name == "." || name == ".." {
		return ErrReservedName
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\/:*?"<>|`, r) {
			return ErrInvalidChars
		}
	}
	if strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") {
		return ErrTrailing
	}
	base := strings.ToUpper(name)
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	if windowsReserved[base] {
		return ErrReservedName
	}
	return nil
}

// SanitizeName turns arbitrary text (for example a rendered automation
// template) into a valid name, replacing disallowed characters with "_".
// It returns fallback if nothing usable remains.
func SanitizeName(s, fallback string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || strings.ContainsRune(`\/:*?"<>|`, r) || r == utf8.RuneError {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.TrimRightFunc(strings.TrimSpace(b.String()), func(r rune) bool { return r == '.' || unicode.IsSpace(r) })
	for len(out) > MaxNameLength {
		_, size := utf8.DecodeLastRuneInString(out)
		out = out[:len(out)-size]
	}
	if out == "" || out == "." || out == ".." || ValidateName(out) != nil {
		if fallback == "" {
			return "_"
		}
		return fallback
	}
	return out
}

// Split parses a virtual path such as "/Clips/Game/a.mp4" (backslashes are
// accepted) into validated segments. The root path yields no segments.
func Split(p string) ([]string, error) {
	p = strings.ReplaceAll(p, `\`, "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s == "" {
			continue
		}
		if err := ValidateName(s); err != nil {
			return nil, &PathError{Segment: s, Err: err}
		}
		segs = append(segs, s)
	}
	if len(segs) > MaxPathDepth {
		return nil, ErrPathTooDeep
	}
	return segs, nil
}

// Clean returns the canonical form of a virtual path ("/a/b").
func Clean(p string) (string, error) {
	segs, err := Split(p)
	if err != nil {
		return "", err
	}
	return "/" + strings.Join(segs, "/"), nil
}

// Join concatenates virtual path elements and validates the result.
func Join(elem ...string) (string, error) {
	return Clean(strings.Join(elem, "/"))
}

// SanitizePath sanitises each segment of a rendered path. Empty segments,
// "." and ".." are dropped so a template can never escape its root.
func SanitizePath(p string) string {
	p = strings.ReplaceAll(p, `\`, "/")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		s = strings.TrimSpace(s)
		if s == "" || s == "." || s == ".." {
			continue
		}
		segs = append(segs, SanitizeName(s, "_"))
	}
	return "/" + strings.Join(segs, "/")
}

type PathError struct {
	Segment string
	Err     error
}

func (e *PathError) Error() string {
	return "invalid path segment " + quote(e.Segment) + ": " + e.Err.Error()
}
func (e *PathError) Unwrap() error { return e.Err }

func quote(s string) string {
	if len(s) > 40 {
		s = s[:40] + "..."
	}
	return `"` + s + `"`
}
