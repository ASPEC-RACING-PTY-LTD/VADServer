package automation

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Template is a parsed {{variable|filter:"arg"}} string.
type Template struct {
	parts []tplPart
}

type tplPart struct {
	literal  string
	variable string
	filters  []tplFilter
}

type tplFilter struct {
	name string
	args []string
}

var knownFilters = map[string][2]int{ // min, max args
	"date": {1, 1}, "default": {1, 1}, "lower": {0, 0}, "upper": {0, 0},
	"slug": {0, 0}, "trim": {0, 0}, "truncate": {1, 1}, "replace": {2, 2},
}

// ParseTemplate parses a template string.
func ParseTemplate(s string) (*Template, error) {
	t := &Template{}
	for {
		i := strings.Index(s, "{{")
		if i < 0 {
			if strings.Contains(s, "}}") {
				return nil, fmt.Errorf("unmatched \"}}\"")
			}
			if s != "" {
				t.parts = append(t.parts, tplPart{literal: s})
			}
			return t, nil
		}
		if i > 0 {
			lit := s[:i]
			if strings.Contains(lit, "}}") {
				return nil, fmt.Errorf("unmatched \"}}\"")
			}
			t.parts = append(t.parts, tplPart{literal: lit})
		}
		rest := s[i+2:]
		j := indexOutsideQuotes(rest, "}}")
		if j < 0 {
			return nil, fmt.Errorf("unclosed \"{{\"")
		}
		p, err := parseExpr(rest[:j])
		if err != nil {
			return nil, err
		}
		t.parts = append(t.parts, p)
		s = rest[j+2:]
	}
}

func indexOutsideQuotes(s, sub string) int {
	inQ := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && inQ {
			i++
			continue
		}
		if s[i] == '"' {
			inQ = !inQ
			continue
		}
		if !inQ && strings.HasPrefix(s[i:], sub) {
			return i
		}
	}
	return -1
}

func splitOutsideQuotes(s string, sep byte) []string {
	var out []string
	inQ := false
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && inQ {
			i++
			continue
		}
		if s[i] == '"' {
			inQ = !inQ
		}
		if !inQ && s[i] == sep {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func parseExpr(expr string) (tplPart, error) {
	segs := splitOutsideQuotes(expr, '|')
	name := strings.TrimSpace(segs[0])
	if name == "" {
		return tplPart{}, fmt.Errorf("empty variable in \"{{%s}}\"", expr)
	}
	for _, r := range name {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '.' || r == '_') {
			return tplPart{}, fmt.Errorf("invalid variable name %q", name)
		}
	}
	p := tplPart{variable: name}
	for _, fs := range segs[1:] {
		fs = strings.TrimSpace(fs)
		fname, argStr, hasArgs := strings.Cut(fs, ":")
		fname = strings.TrimSpace(fname)
		lim, ok := knownFilters[fname]
		if !ok {
			return tplPart{}, fmt.Errorf("unknown filter %q", fname)
		}
		var args []string
		if hasArgs {
			for _, a := range splitOutsideQuotes(argStr, ',') {
				a = strings.TrimSpace(a)
				if len(a) >= 2 && a[0] == '"' && a[len(a)-1] == '"' {
					uq, err := strconv.Unquote(a)
					if err != nil {
						return tplPart{}, fmt.Errorf("invalid argument %s for filter %q", a, fname)
					}
					a = uq
				} else if _, err := strconv.Atoi(a); err != nil {
					return tplPart{}, fmt.Errorf("filter %q argument must be a quoted string or a number, got %s", fname, a)
				}
				args = append(args, a)
			}
		}
		if len(args) < lim[0] || len(args) > lim[1] {
			return tplPart{}, fmt.Errorf("filter %q expects %d argument(s)", fname, lim[0])
		}
		if fname == "truncate" {
			if n, err := strconv.Atoi(args[0]); err != nil || n < 1 {
				return tplPart{}, fmt.Errorf("truncate needs a positive number")
			}
		}
		p.filters = append(p.filters, tplFilter{name: fname, args: args})
	}
	return p, nil
}

// Variables returns the variable names referenced by the template.
func (t *Template) Variables() []string {
	var out []string
	for _, p := range t.parts {
		if p.variable != "" {
			out = append(out, p.variable)
		}
	}
	return out
}

// Render evaluates the template against a variable set.
func (t *Template) Render(vars Vars) string {
	var b strings.Builder
	for _, p := range t.parts {
		if p.variable == "" {
			b.WriteString(p.literal)
			continue
		}
		v := vars[p.variable]
		for _, fl := range p.filters {
			if tm, ok := v.(time.Time); ok && fl.name == "date" {
				if tm.IsZero() {
					v = ""
				} else {
					v = FormatDate(tm, fl.args[0])
				}
				continue
			}
			v = applyFilter(formatValue(v), fl)
		}
		b.WriteString(formatValue(v))
	}
	return b.String()
}

func applyFilter(s string, fl tplFilter) string {
	switch fl.name {
	case "default":
		if strings.TrimSpace(s) == "" {
			return fl.args[0]
		}
	case "lower":
		return strings.ToLower(s)
	case "upper":
		return strings.ToUpper(s)
	case "trim":
		return strings.TrimSpace(s)
	case "slug":
		return slugify(s)
	case "truncate":
		n, _ := strconv.Atoi(fl.args[0])
		r := []rune(s)
		if len(r) > n {
			return string(r[:n])
		}
	case "replace":
		if fl.args[0] != "" {
			return strings.ReplaceAll(s, fl.args[0], fl.args[1])
		}
	case "date":
		// value was not a datetime; leave unchanged
	}
	return s
}

func slugify(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.TrimSuffix(b.String(), "-")
}

func formatValue(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case time.Time:
		if x.IsZero() {
			return ""
		}
		return x.Format(time.RFC3339)
	case int64:
		return strconv.FormatInt(x, 10)
	case int:
		return strconv.Itoa(x)
	case float64:
		if x == math.Trunc(x) {
			return strconv.FormatFloat(x, 'f', 1, 64)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

var dateTokens = []struct {
	tok string
	fn  func(time.Time) string
}{
	{"YYYY", func(t time.Time) string { return fmt.Sprintf("%04d", t.Year()) }},
	{"YY", func(t time.Time) string { return fmt.Sprintf("%02d", t.Year()%100) }},
	{"MMMM", func(t time.Time) string { return t.Month().String() }},
	{"MMM", func(t time.Time) string { return t.Month().String()[:3] }},
	{"MM", func(t time.Time) string { return fmt.Sprintf("%02d", int(t.Month())) }},
	{"DD", func(t time.Time) string { return fmt.Sprintf("%02d", t.Day()) }},
	{"dddd", func(t time.Time) string { return t.Weekday().String() }},
	{"ddd", func(t time.Time) string { return t.Weekday().String()[:3] }},
	{"HH", func(t time.Time) string { return fmt.Sprintf("%02d", t.Hour()) }},
	{"mm", func(t time.Time) string { return fmt.Sprintf("%02d", t.Minute()) }},
	{"ss", func(t time.Time) string { return fmt.Sprintf("%02d", t.Second()) }},
}

// FormatDate formats a time using YYYY/MM/DD style tokens.
func FormatDate(t time.Time, layout string) string {
	var b strings.Builder
	for i := 0; i < len(layout); {
		matched := false
		for _, dt := range dateTokens {
			if strings.HasPrefix(layout[i:], dt.tok) {
				b.WriteString(dt.fn(t))
				i += len(dt.tok)
				matched = true
				break
			}
		}
		if !matched {
			b.WriteByte(layout[i])
			i++
		}
	}
	return b.String()
}
