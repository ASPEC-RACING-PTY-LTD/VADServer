package automation

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
)

var globCache sync.Map // key: pattern + "\x00" + case flag

// CompileGlob converts a glob into an anchored regular expression.
// Supported syntax: * (any run of characters except /), ** (anything,
// including /), ? (one character except /), [abc] / [a-z] / [!abc].
func CompileGlob(pattern string, caseSensitive bool) (*regexp.Regexp, error) {
	key := pattern + "\x00" + fmt.Sprint(caseSensitive)
	if re, ok := globCache.Load(key); ok {
		return re.(*regexp.Regexp), nil
	}
	var b strings.Builder
	if !caseSensitive {
		b.WriteString("(?i)")
	}
	b.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				// "**/" also matches zero folders
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '[':
			j := strings.IndexByte(pattern[i+1:], ']')
			if j < 0 {
				return nil, fmt.Errorf("unclosed '[' in pattern %q", pattern)
			}
			class := pattern[i+1 : i+1+j]
			if class == "" {
				return nil, fmt.Errorf("empty character class in pattern %q", pattern)
			}
			if class[0] == '!' {
				class = "^" + class[1:]
			}
			b.WriteString("[" + strings.ReplaceAll(class, `\`, `\\`) + "]")
			i += j + 1
		case '\\':
			// Windows users often paste backslashes; treat them as separators.
			b.WriteString("/")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, fmt.Errorf("invalid pattern %q: %v", pattern, err)
	}
	globCache.Store(key, re)
	return re, nil
}

// MatchGlob reports whether s matches the glob. Invalid patterns never match.
func MatchGlob(pattern, s string, caseSensitive bool) bool {
	re, err := CompileGlob(pattern, caseSensitive)
	if err != nil {
		return false
	}
	return re.MatchString(strings.ReplaceAll(s, `\`, "/"))
}
