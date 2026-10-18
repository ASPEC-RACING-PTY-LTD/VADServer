package snapshot

import (
	"errors"
	"sort"
	"strings"
	"unicode/utf8"
)

// Path tokens (§6). A token path is a token followed by "/"-separated
// components, for example "{DOCUMENTS}/Reports/q3.xlsx".
const (
	TokenProfile         = "{PROFILE}"
	TokenDesktop         = "{DESKTOP}"
	TokenDocuments       = "{DOCUMENTS}"
	TokenDownloads       = "{DOWNLOADS}"
	TokenPictures        = "{PICTURES}"
	TokenMusic           = "{MUSIC}"
	TokenVideos          = "{VIDEOS}"
	TokenSavedGames      = "{SAVEDGAMES}"
	TokenAppData         = "{APPDATA}"
	TokenLocalAppData    = "{LOCALAPPDATA}"
	TokenLocalLow        = "{LOCALLOW}"
	TokenProgramData     = "{PROGRAMDATA}"
	TokenProgramFiles    = "{PROGRAMFILES}"
	TokenProgramFilesX86 = "{PROGRAMFILESX86}"
	volPrefix            = "{VOL:"
)

var namedTokens = map[string]bool{
	TokenProfile: true, TokenDesktop: true, TokenDocuments: true, TokenDownloads: true,
	TokenPictures: true, TokenMusic: true, TokenVideos: true, TokenSavedGames: true,
	TokenAppData: true, TokenLocalAppData: true, TokenLocalLow: true,
	TokenProgramData: true, TokenProgramFiles: true, TokenProgramFilesX86: true,
}

// Path token errors.
var (
	ErrToken         = errors.New("snapshot: unknown or malformed path token")
	ErrTokenPath     = errors.New("snapshot: malformed token path")
	ErrNoTokenMatch  = errors.New("snapshot: no path token covers this path")
	ErrTokenUnmapped = errors.New("snapshot: token has no concrete value")
)

// VolumeToken returns "{VOL:<guid>}" for a volume GUID given with or
// without braces; the GUID is written in lowercase.
func VolumeToken(guid string) (string, error) {
	g := strings.ToLower(strings.TrimSuffix(strings.TrimPrefix(guid, "{"), "}"))
	tok := volPrefix + g + "}"
	if !ValidToken(tok) {
		return "", ErrToken
	}
	return tok, nil
}

// ValidToken reports whether tok is a §6 token. Volume tokens must carry a
// lowercase canonical GUID (8-4-4-4-12 hex digits, no braces).
func ValidToken(tok string) bool {
	if namedTokens[tok] {
		return true
	}
	if !strings.HasPrefix(tok, volPrefix) || !strings.HasSuffix(tok, "}") {
		return false
	}
	g := tok[len(volPrefix) : len(tok)-1]
	if len(g) != 36 {
		return false
	}
	for i := 0; i < len(g); i++ {
		c := g[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return false
			}
		}
	}
	return true
}

// validComponent rejects components that could escape the token root or
// address an alternate data stream on restore.
func validComponent(c string) bool {
	return c != "" && c != "." && c != ".." && utf8.ValidString(c) &&
		!strings.ContainsAny(c, "/\\:\x00")
}

// SplitTokenPath splits a token path into its token and components.
func SplitTokenPath(tp string) (token string, components []string, err error) {
	if !strings.HasPrefix(tp, "{") {
		return "", nil, ErrTokenPath
	}
	end := strings.IndexByte(tp, '}')
	if end < 0 {
		return "", nil, ErrTokenPath
	}
	token, rest := tp[:end+1], tp[end+1:]
	if !ValidToken(token) {
		return "", nil, ErrToken
	}
	if rest == "" {
		return token, nil, nil
	}
	if rest[0] != '/' {
		return "", nil, ErrTokenPath
	}
	components = strings.Split(rest[1:], "/")
	for _, c := range components {
		if !validComponent(c) {
			return "", nil, ErrTokenPath
		}
	}
	return token, components, nil
}

// JoinTokenPath builds a token path from a token and components.
func JoinTokenPath(token string, components ...string) (string, error) {
	if !ValidToken(token) {
		return "", ErrToken
	}
	for _, c := range components {
		if !validComponent(c) {
			return "", ErrTokenPath
		}
	}
	if len(components) == 0 {
		return token, nil
	}
	return token + "/" + strings.Join(components, "/"), nil
}

// Tokens maps each token to its concrete Windows path on the source
// machine, for example "{DOCUMENTS}" → `D:\OneDrive\Documents`. A snapshot
// root records it (field "tokens"). Volume tokens map to the volume's
// mount point or its `\\?\Volume{…}\` name.
type Tokens map[string]string

// normWin converts a Windows path to a comparable form: backslashes, no
// `\\?\` prefix (`\\?\UNC\x` becomes `\\x`), no trailing separator.
func normWin(p string) string {
	p = strings.ReplaceAll(p, "/", `\`)
	switch {
	case strings.HasPrefix(p, `\\?\UNC\`):
		p = `\\` + p[len(`\\?\UNC\`):]
	case strings.HasPrefix(p, `\\?\`):
		p = p[len(`\\?\`):]
	}
	return strings.TrimRight(p, `\`)
}

// hasPrefixFold reports whether p starts with prefix, ignoring case (NTFS
// names are case-insensitive), at a component boundary.
func hasPrefixFold(p, prefix string) bool {
	if len(p) < len(prefix) || !strings.EqualFold(p[:len(prefix)], prefix) {
		return false
	}
	return len(p) == len(prefix) || p[len(prefix)] == '\\'
}

// Encode converts a concrete Windows path to a token path using the
// longest matching token (§6). Ties go to the lexically smaller token so
// the choice is deterministic. Long (`\\?\`) paths are accepted.
func (t Tokens) Encode(winPath string) (string, error) {
	p := normWin(winPath)
	if p == "" {
		return "", ErrTokenPath
	}
	toks := make([]string, 0, len(t))
	for tok := range t {
		toks = append(toks, tok)
	}
	sort.Strings(toks)
	best, bestLen := "", -1
	for _, tok := range toks {
		if !ValidToken(tok) {
			return "", ErrToken
		}
		v := normWin(t[tok])
		if v == "" || !hasPrefixFold(p, v) {
			continue
		}
		if len(v) > bestLen {
			best, bestLen = tok, len(v)
		}
	}
	if best == "" {
		return "", ErrNoTokenMatch
	}
	rest := strings.TrimLeft(p[bestLen:], `\`)
	var comps []string
	if rest != "" {
		for _, c := range strings.Split(rest, `\`) {
			if c != "" { // collapse doubled separators
				comps = append(comps, c)
			}
		}
	}
	return JoinTokenPath(best, comps...)
}

// Resolve converts a token path to a concrete Windows path with t's
// values (the source machine's, or remapped ones on restore).
func (t Tokens) Resolve(tokenPath string) (string, error) {
	tok, comps, err := SplitTokenPath(tokenPath)
	if err != nil {
		return "", err
	}
	v, ok := t[tok]
	if !ok || normWin(v) == "" {
		return "", ErrTokenUnmapped
	}
	base := strings.TrimRight(strings.ReplaceAll(v, "/", `\`), `\`)
	if len(comps) == 0 {
		if len(base) == 2 && base[1] == ':' {
			return base + `\`, nil
		}
		return base, nil
	}
	return base + `\` + strings.Join(comps, `\`), nil
}

// ExtendedPath returns the `\\?\` form of an absolute Windows path, which
// lifts the MAX_PATH limit and keeps trailing dots and spaces.
func ExtendedPath(p string) string {
	switch {
	case strings.HasPrefix(p, `\\?\`):
		return p
	case strings.HasPrefix(p, `\\`):
		return `\\?\UNC\` + p[2:]
	default:
		return `\\?\` + p
	}
}
