package version

import "testing"

func TestNewer(t *testing.T) {
	cases := []struct {
		latest, current string
		want            bool
	}{
		{"1.0.1", "1.0.0", true},
		{"v1.1.0", "1.0.9", true},
		{"2.0.0", "v1.99.99", true},
		{"1.0.0", "1.0.0", false},
		{"1.0.0", "1.0.1", false},
		{"1.0.0", "1.0.0-rc.1", true},
		{"1.0.0-rc.2", "1.0.0-rc.1", true},
		{"1.0.0-rc.1", "1.0.0", false},
		{"1.0.1", "v1.0.0-3-gabc1234", true},
		{"1.0.0", "v1.0.0-3-gabc1234", false},
		{"1.0.0", "dev", false},
		{"garbage", "1.0.0", false},
		{"1.2", "1.1.9", true},
	}
	for _, c := range cases {
		if got := Newer(c.latest, c.current); got != c.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", c.latest, c.current, got, c.want)
		}
	}
}
