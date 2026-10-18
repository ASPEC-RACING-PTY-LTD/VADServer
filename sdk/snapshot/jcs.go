package snapshot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// JCS errors.
var (
	ErrJCSInvalid      = errors.New("snapshot: jcs: invalid JSON")
	ErrJCSDuplicateKey = errors.New("snapshot: jcs: duplicate object key")
	ErrJCSNumber       = errors.New("snapshot: jcs: number is not exactly representable as an IEEE-754 double")
)

// maxJCSDepth bounds nesting so hostile input cannot exhaust the stack.
const maxJCSDepth = 64

// Canonicalize returns the RFC 8785 (JCS) form of the JSON text data:
// members sorted by their UTF-16 code units, no insignificant whitespace,
// numbers in ECMAScript Number.prototype.toString form, and strings with
// only the mandatory escapes.
//
// Input must be valid UTF-8 and free of duplicate keys (I-JSON). Integer
// literals that a double cannot hold exactly (for example 2^53+1) are
// rejected rather than silently rounded, so a signature never covers a
// value other than the one written.
func Canonicalize(data []byte) ([]byte, error) {
	if !utf8.Valid(data) {
		return nil, ErrJCSInvalid
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var out bytes.Buffer
	if err := jcsValue(dec, &out, 0); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, ErrJCSInvalid
	}
	return out.Bytes(), nil
}

// MarshalJCS encodes v with encoding/json and canonicalises the result.
func MarshalJCS(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return Canonicalize(raw)
}

func jcsValue(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	if depth > maxJCSDepth {
		return ErrJCSInvalid
	}
	tok, err := dec.Token()
	if err != nil {
		return ErrJCSInvalid
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			return jcsObject(dec, out, depth)
		case '[':
			out.WriteByte('[')
			for i := 0; dec.More(); i++ {
				if i > 0 {
					out.WriteByte(',')
				}
				if err := jcsValue(dec, out, depth+1); err != nil {
					return err
				}
			}
			if end, err := dec.Token(); err != nil || end != json.Delim(']') {
				return ErrJCSInvalid
			}
			out.WriteByte(']')
			return nil
		}
		return ErrJCSInvalid
	case string:
		writeJCSString(out, t)
	case json.Number:
		s, err := formatJCSNumber(string(t))
		if err != nil {
			return err
		}
		out.WriteString(s)
	case bool:
		if t {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case nil:
		out.WriteString("null")
	default:
		return ErrJCSInvalid
	}
	return nil
}

type jcsMember struct {
	key   string
	utf16 []uint16
	value []byte
}

func jcsObject(dec *json.Decoder, out *bytes.Buffer, depth int) error {
	var members []jcsMember
	seen := map[string]bool{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return ErrJCSInvalid
		}
		key, ok := tok.(string)
		if !ok {
			return ErrJCSInvalid
		}
		if seen[key] {
			return ErrJCSDuplicateKey
		}
		seen[key] = true
		var val bytes.Buffer
		if err := jcsValue(dec, &val, depth+1); err != nil {
			return err
		}
		members = append(members, jcsMember{key, utf16.Encode([]rune(key)), val.Bytes()})
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return ErrJCSInvalid
	}
	sort.Slice(members, func(i, j int) bool { return lessUTF16(members[i].utf16, members[j].utf16) })
	out.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			out.WriteByte(',')
		}
		writeJCSString(out, m.key)
		out.WriteByte(':')
		out.Write(m.value)
	}
	out.WriteByte('}')
	return nil
}

func lessUTF16(a, b []uint16) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// writeJCSString writes s as a JSON string with the RFC 8785 escapes:
// \" \\ \b \t \n \f \r, other controls as \u00xx (lowercase hex), and
// everything else as literal UTF-8.
func writeJCSString(out *bytes.Buffer, s string) {
	const hexdigits = "0123456789abcdef"
	out.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if c < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hexdigits[c>>4])
				out.WriteByte(hexdigits[c&0xf])
			} else {
				out.WriteByte(c)
			}
		}
	}
	out.WriteByte('"')
}

// formatJCSNumber converts a JSON number literal to its ECMAScript
// Number.prototype.toString form (RFC 8785 §3.2.2.3).
func formatJCSNumber(lit string) (string, error) {
	f, err := strconv.ParseFloat(lit, 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return "", ErrJCSNumber
	}
	if !strings.ContainsAny(lit, ".eE") {
		// An integer literal must be exactly representable as a double,
		// otherwise its canonical form would name a different value.
		n, ok := new(big.Int).SetString(lit, 10)
		if !ok {
			return "", ErrJCSNumber
		}
		exact, acc := big.NewFloat(f).Int(nil)
		if acc != big.Exact || exact.Cmp(n) != 0 {
			return "", ErrJCSNumber
		}
	}
	return FormatJCSFloat(f), nil
}

// FormatJCSFloat formats a finite double as ECMAScript does.
func FormatJCSFloat(f float64) string {
	if f == 0 {
		return "0" // also -0
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits: "d.ddde±XX".
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expStr, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expStr)
	k, n := len(digits), exp+1 // value = 0.digits × 10^n
	var s string
	switch {
	case k <= n && n <= 21:
		s = digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		s = digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		s = "0." + strings.Repeat("0", -n) + digits
	default:
		esign := "+"
		if n-1 < 0 {
			esign = "-"
		}
		ev := n - 1
		if ev < 0 {
			ev = -ev
		}
		if k == 1 {
			s = digits + "e" + esign + strconv.Itoa(ev)
		} else {
			s = digits[:1] + "." + digits[1:] + "e" + esign + strconv.Itoa(ev)
		}
	}
	return sign + s
}

// stripMember returns the JCS form of the JSON object data without its
// top-level member key. It reports whether the member was present.
func stripMember(data []byte, key string) ([]byte, json.RawMessage, error) {
	var obj map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&obj); err != nil || obj == nil {
		return nil, nil, fmt.Errorf("%w: not a JSON object", ErrJCSInvalid)
	}
	// Duplicate detection happens in Canonicalize on the full input.
	if _, err := Canonicalize(data); err != nil {
		return nil, nil, err
	}
	val := obj[key]
	delete(obj, key)
	raw, err := json.Marshal(obj)
	if err != nil {
		return nil, nil, err
	}
	c, err := Canonicalize(raw)
	return c, val, err
}
