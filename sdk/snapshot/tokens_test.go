package snapshot

import (
	"errors"
	"strings"
	"testing"
)

const testVol = "{VOL:0a1b2c3d-4e5f-6071-8293-a4b5c6d7e8f9}"

func testTokens() Tokens {
	return Tokens{
		TokenProfile:      `C:\Users\ali`,
		TokenDocuments:    `D:\OneDrive\Documents`,
		TokenAppData:      `C:\Users\ali\AppData\Roaming`,
		TokenLocalAppData: `C:\Users\ali\AppData\Local`,
		TokenLocalLow:     `C:\Users\ali\AppData\LocalLow`,
		testVol:           `E:\`,
		TokenProgramFiles: `C:\Program Files`,
	}
}

func TestTokenEncodeResolve(t *testing.T) {
	tk := testTokens()
	long := strings.Repeat(`very long directory name `+strings.Repeat("x", 200)+`\`, 40) + "file.bin"
	tests := []struct {
		name, in, token, back string
	}{
		{"longest match", `C:\Users\ali\AppData\Roaming\Vendor\app.ini`, "{APPDATA}/Vendor/app.ini", ""},
		{"locallow beats local", `C:\Users\ali\AppData\LocalLow\x`, "{LOCALLOW}/x", ""},
		{"boundary", `C:\Users\alice\x`, "", ""},
		{"profile", `C:\Users\ali\.ssh\config`, "{PROFILE}/.ssh/config", ""},
		{"case-insensitive prefix keeps case", `c:\USERS\ALI\Desktop\Ä.txt`, "{PROFILE}/Desktop/Ä.txt",
			`C:\Users\ali\Desktop\Ä.txt`},
		{"token root", `D:\OneDrive\Documents\`, "{DOCUMENTS}", `D:\OneDrive\Documents`},
		{"volume", `E:\Photos\2024\IMG 0001.jpg`, testVol + "/Photos/2024/IMG 0001.jpg", ""},
		{"volume root", `E:\`, testVol, `E:\`},
		{"unicode", `D:\OneDrive\Documents\日本語\Ελληνικά\😀 notes.md`, "{DOCUMENTS}/日本語/Ελληνικά/😀 notes.md", ""},
		{"windows names", `C:\Program Files\a\CON\name. \trailing dot.`, "{PROGRAMFILES}/a/CON/name. /trailing dot.", ""},
		{"extended prefix", `\\?\C:\Users\ali\x`, "{PROFILE}/x", `C:\Users\ali\x`},
		{"forward slashes and doubles", `C:/Users/ali//a\\b`, "{PROFILE}/a/b", `C:\Users\ali\a\b`},
		{"long path", `E:\` + long, testVol + "/" + strings.ReplaceAll(long, `\`, "/"), ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tk.Encode(tc.in)
			if tc.token == "" {
				if !errors.Is(err, ErrNoTokenMatch) {
					t.Fatalf("got %q, %v; want no match", got, err)
				}
				return
			}
			if err != nil || got != tc.token {
				t.Fatalf("Encode = %q, %v; want %q", got, err, tc.token)
			}
			back, err := tk.Resolve(got)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.back
			if want == "" {
				want = tc.in
			}
			if back != want {
				t.Fatalf("Resolve = %q, want %q", back, want)
			}
		})
	}
}

func TestTokenRemap(t *testing.T) {
	src, dst := testTokens(), Tokens{TokenDocuments: `C:\Users\bob\Documents`}
	tp, err := src.Encode(`D:\OneDrive\Documents\a\b.txt`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := dst.Resolve(tp)
	if err != nil || got != `C:\Users\bob\Documents\a\b.txt` {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := dst.Resolve("{APPDATA}/x"); !errors.Is(err, ErrTokenUnmapped) {
		t.Fatal(err)
	}
}

func TestSplitTokenPathRejects(t *testing.T) {
	bad := []string{
		"", "Documents/x", "{DOCUMENTS", "{NOPE}/x", "{DOCUMENTS}x", "{DOCUMENTS}/", "{DOCUMENTS}/a//b",
		"{DOCUMENTS}/../x", "{DOCUMENTS}/./x", `{DOCUMENTS}/a\b`, "{DOCUMENTS}/f:stream", "{DOCUMENTS}/a\x00",
		"{VOL:0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9}/x", "{VOL:not-a-guid}", "{DOCUMENTS}/\xff",
	}
	for _, tp := range bad {
		if _, _, err := SplitTokenPath(tp); err == nil {
			t.Errorf("SplitTokenPath(%q) accepted", tp)
		}
	}
	tok, comps, err := SplitTokenPath("{SAVEDGAMES}/My Game/slot 1.sav")
	if err != nil || tok != TokenSavedGames || len(comps) != 2 {
		t.Fatal(tok, comps, err)
	}
}

func TestVolumeToken(t *testing.T) {
	got, err := VolumeToken("{0A1B2C3D-4E5F-6071-8293-A4B5C6D7E8F9}")
	if err != nil || got != testVol {
		t.Fatal(got, err)
	}
	if _, err := VolumeToken("xyz"); err == nil {
		t.Fatal("bad guid accepted")
	}
}

func TestExtendedPath(t *testing.T) {
	for in, want := range map[string]string{
		`C:\a`:             `\\?\C:\a`,
		`\\server\share\x`: `\\?\UNC\server\share\x`,
		`\\?\C:\a`:         `\\?\C:\a`,
	} {
		if got := ExtendedPath(in); got != want {
			t.Errorf("ExtendedPath(%q) = %q, want %q", in, got, want)
		}
	}
}
