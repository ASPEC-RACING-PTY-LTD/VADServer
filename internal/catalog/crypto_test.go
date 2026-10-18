package catalog

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/vdcrypt"
)

func TestBlindTokensAndFields(t *testing.T) {
	dek, _ := vdcrypt.RandomKey(nil)
	uk, err := newUserKeys(dek)
	if err != nil {
		t.Fatal(err)
	}
	if got := splitWords("Quarterly_Report-2024 (final).PDF"); !slices.Equal(got, []string{"quarterly", "report", "2024", "final", "pdf"}) {
		t.Fatalf("words: %v", got)
	}
	if got := splitWords(strings.Repeat("x", 40)); len(got) != 1 || len(got[0]) != maxTokenRunes {
		t.Fatalf("long word: %v", got)
	}
	has := func(name, q string) bool {
		toks := uk.nameTokens(name)
		for _, qt := range uk.queryTokens(q) {
			if !slices.ContainsFunc(toks, func(b []byte) bool { return bytes.Equal(b, qt) }) {
				return false
			}
		}
		return true
	}
	for _, c := range []struct {
		name, q string
		want    bool
	}{
		{"Quarterly Report.pdf", "rep", true},
		{"Quarterly Report.pdf", "REPORT quart", true},
		{"Quarterly Report.pdf", "port", false}, // no substrings
		{"Quarterly Report.pdf", "r", false},    // 1-rune prefixes are not indexed
		{"a.txt", "a", true},                    // a 1-rune word is indexed whole
		{"Résumé.docx", "rés", true},            // unicode lowercasing
		{"notes.txt", "notes missing", false},
	} {
		if got := has(c.name, c.q); got != c.want {
			t.Errorf("%q ~ %q = %v, want %v", c.name, c.q, got, c.want)
		}
	}

	ct, err := uk.sealField(7, "name", "secret.txt")
	if err != nil || bytes.Contains(ct, []byte("secret")) {
		t.Fatalf("seal: %v", err)
	}
	if v, err := uk.openField(7, "name", ct); err != nil || v != "secret.txt" {
		t.Fatalf("open: %q %v", v, err)
	}
	if _, err := uk.openField(8, "name", ct); err == nil {
		t.Fatal("ciphertext moved to another snapshot opened")
	}
	if _, err := uk.openField(7, "path", ct); err == nil {
		t.Fatal("ciphertext moved to another column opened")
	}
	other, _ := vdcrypt.RandomKey(nil)
	uk2, _ := newUserKeys(other)
	if bytes.Equal(uk.pathBI("{DOCUMENTS}"), uk2.pathBI("{DOCUMENTS}")) {
		t.Fatal("blind index not keyed per user")
	}
}
