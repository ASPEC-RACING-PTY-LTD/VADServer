package pathutil

import "testing"

func TestValidateName(t *testing.T) {
	good := []string{"clip.mp4", "Counter-Strike 2", "résumé.pdf", ".hidden", "a b"}
	bad := []string{"", ".", "..", "a/b", `a\b`, "a:b", "con", "CON.txt", "nul.mp4", "trail.", "trail ", "x\x00y", "q?"}
	for _, n := range good {
		if err := ValidateName(n); err != nil {
			t.Errorf("%q rejected: %v", n, err)
		}
	}
	for _, n := range bad {
		if ValidateName(n) == nil {
			t.Errorf("%q accepted", n)
		}
	}
}

func TestSplitAndSanitize(t *testing.T) {
	if _, err := Split("/a/../b"); err == nil {
		t.Error("'..' segment accepted")
	}
	if p, err := Clean(`\Medal\\Valorant/`); err != nil || p != "/Medal/Valorant" {
		t.Errorf("clean = %q %v", p, err)
	}
	if got := SanitizePath("../../etc/./passwd"); got != "/etc/passwd" {
		t.Errorf("sanitize escaped root: %q", got)
	}
	if got := SanitizeName("a:b*c", "x"); got != "a_b_c" {
		t.Errorf("sanitize name = %q", got)
	}
	if got := SanitizeName("..", "fallback"); got != "fallback" {
		t.Errorf("sanitize '..' = %q", got)
	}
}
