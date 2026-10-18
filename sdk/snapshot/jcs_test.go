package snapshot

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestCanonicalize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		// RFC 8785 §3.2.2 sample.
		{"rfc sample",
			`{"numbers":[333333333.33333329,1E30,4.50,2e-3,0.000000000000000000000000001],` +
				`"string":"\u20ac$\u000F\u000aA'\u0042\u0022\u005c\\\"\/","literals":[null,true,false]}`,
			`{"literals":[null,true,false],"numbers":[333333333.3333333,1e+30,4.5,0.002,1e-27],` +
				`"string":"€$\u000f\nA'B\"\\\\\"/"}`},
		// RFC 8785 §3.2.3: sorting by UTF-16 code units, not bytes.
		{"utf16 sort",
			`{"\u20ac":"Euro","\r":"CR","\ufb33":"Hebrew","1":"One","\ud83d\ude00":"Emoji","\u0080":"Ctl","\u00f6":"o"}`,
			"{\"\\r\":\"CR\",\"1\":\"One\",\"\u0080\":\"Ctl\",\"ö\":\"o\",\"€\":\"Euro\",\"😀\":\"Emoji\",\"\ufb33\":\"Hebrew\"}"},
		{"whitespace and nesting", " { \"b\" : [ 1 , { \"z\":0, \"a\" :[] } ], \"a\" : {} } ",
			`{"a":{},"b":[1,{"a":[],"z":0}]}`},
		{"escapes", `"\u0000\u001f\b\t\n\f\r\u007f\u2028<>&"`, "\"\\u0000\\u001f\\b\\t\\n\\f\\r\u007f\u2028<>&\""},
		{"negative zero", `[-0,-0.0,0e10]`, `[0,0,0]`},
		{"integers", `[9007199254740991,-9007199254740991,100,1e2]`, `[9007199254740991,-9007199254740991,100,100]`},
		{"exponent edges", `[1e21,1e20,0.000001,1e-7,-1e-7,1.5e300,123.456]`,
			`[1e+21,100000000000000000000,0.000001,1e-7,-1e-7,1.5e+300,123.456]`},
		{"scalar", `true`, `true`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Canonicalize([]byte(tc.in))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("\n got %s\nwant %s", got, tc.want)
			}
			again, err := Canonicalize(got)
			if err != nil || string(again) != string(got) {
				t.Fatalf("not idempotent: %s", again)
			}
		})
	}
}

func TestCanonicalizeRejects(t *testing.T) {
	tests := []struct {
		name, in string
		want     error
	}{
		{"duplicate key", `{"a":1,"a":2}`, ErrJCSDuplicateKey},
		{"nested duplicate", `{"x":[{"k":1,"k":1}]}`, ErrJCSDuplicateKey},
		{"unsafe integer", `9007199254740993`, ErrJCSNumber},
		{"negative unsafe integer", `-9007199254740993`, ErrJCSNumber},
		{"huge integer", `123456789012345678901234567890`, ErrJCSNumber},
		{"overflow", `1e400`, ErrJCSNumber},
		{"invalid utf8", "\"\xff\"", ErrJCSInvalid},
		{"trailing data", `{} {}`, ErrJCSInvalid},
		{"truncated", `{"a":`, ErrJCSInvalid},
		{"empty", ``, ErrJCSInvalid},
		{"deep", strings.Repeat("[", 100) + strings.Repeat("]", 100), ErrJCSInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Canonicalize([]byte(tc.in)); !errors.Is(err, tc.want) {
				t.Fatalf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestFormatJCSFloat(t *testing.T) {
	pointOne, pointTwo := 0.1, 0.2
	tests := []struct {
		f    float64
		want string
	}{
		{math.Float64frombits(1), "5e-324"},
		{-math.Float64frombits(1), "-5e-324"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		{math.Float64frombits(0x0010000000000000), "2.2250738585072014e-308"},
		{pointOne + pointTwo, "0.30000000000000004"},
		{1, "1"},
		{-1.25, "-1.25"},
		{1e-6, "0.000001"},
		{123456789e-15, "1.23456789e-7"},
		{1e21 + 1e6, "1.000000000000001e+21"},
		{999999999999999900000, "999999999999999900000"},
	}
	for _, tc := range tests {
		if got := FormatJCSFloat(tc.f); got != tc.want {
			t.Errorf("FormatJCSFloat(%v) = %s, want %s", tc.f, got, tc.want)
		}
	}
}
