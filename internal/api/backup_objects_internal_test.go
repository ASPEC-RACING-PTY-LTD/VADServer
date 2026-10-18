package api

import "testing"

func TestParseByteRange(t *testing.T) {
	cases := []struct {
		hdr                 string
		size, off, length   int64
		partial, satisfying bool
	}{
		{"", 10, 0, 10, false, true},
		{"bytes=0-4", 10, 0, 5, true, true},
		{"bytes=5-", 10, 5, 5, true, true},
		{"bytes=8-100", 10, 8, 2, true, true},
		{"bytes=-3", 10, 7, 3, true, true},
		{"bytes=-30", 10, 0, 10, true, true},
		{"bytes=10-", 10, 0, 0, false, false},
		{"bytes=-0", 10, 0, 0, false, false},
		{"bytes=0-", 0, 0, 0, false, false},
		{"bytes=0-1,4-5", 10, 0, 10, false, true}, // multi-range ignored
		{"bytes=5-2", 10, 0, 10, false, true},     // malformed ignored
		{"items=0-1", 10, 0, 10, false, true},
		{"bytes=x-1", 10, 0, 10, false, true},
	}
	for _, c := range cases {
		off, length, partial, ok := parseByteRange(c.hdr, c.size)
		if off != c.off || length != c.length || partial != c.partial || ok != c.satisfying {
			t.Errorf("parseByteRange(%q, %d) = %d %d %v %v", c.hdr, c.size, off, length, partial, ok)
		}
	}
}
