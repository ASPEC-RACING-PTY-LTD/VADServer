package release

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

const sum = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"

func TestLatestAndDigest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/acme/vault/releases/latest" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"tag_name":"v1.4.0","html_url":"https://github.com/acme/vault/releases/tag/v1.4.0","assets":[
			{"name":"VaultDock-1.4.0-amd64-setup.exe","size":12,"browser_download_url":"https://github.com/acme/vault/releases/download/v1.4.0/VaultDock-1.4.0-amd64-setup.exe","digest":"sha256:` + sum + `"}]}`))
	}))
	defer srv.Close()
	c := &Client{APIBase: srv.URL}
	rel, err := c.Latest(context.Background(), "acme/vault")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version() != "1.4.0" {
		t.Fatalf("version %q", rel.Version())
	}
	a := rel.Find(regexp.MustCompile(`-amd64-setup\.exe$`))
	if a == nil {
		t.Fatal("asset not found")
	}
	got, err := c.SHA256(context.Background(), rel, a)
	if err != nil || got != sum {
		t.Fatalf("sha %q %v", got, err)
	}
	if _, err := c.Latest(context.Background(), "acme/none"); !errors.Is(err, ErrNoRelease) {
		t.Fatalf("missing release: %v", err)
	}
	if _, err := c.Latest(context.Background(), "../etc"); err == nil {
		t.Fatal("invalid repo accepted")
	}
}

func TestNoChecksumIsRejected(t *testing.T) {
	rel := &Release{Tag: "v1.0.0", Assets: []Asset{{Name: "a.exe", DownloadURL: "https://github.com/o/r/releases/download/v1.0.0/a.exe"}}}
	if _, err := (&Client{}).SHA256(context.Background(), rel, &rel.Assets[0]); err == nil {
		t.Fatal("asset without checksum accepted")
	}
}

func TestParseChecksums(t *testing.T) {
	in := "0000000000000000000000000000000000000000000000000000000000000000  other.exe\n" + strings.ToUpper(sum) + " *VaultDock-1.0.0-amd64-setup.exe\n"
	got, err := ParseChecksums(strings.NewReader(in), "VaultDock-1.0.0-amd64-setup.exe")
	if err != nil || got != sum {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := ParseChecksums(strings.NewReader(in), "missing.exe"); err == nil {
		t.Fatal("missing entry accepted")
	}
}

func TestTrustedLocations(t *testing.T) {
	if err := TrustedDownloadURL("https://github.com/o/r/releases/download/v1/a.exe"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{
		"http://github.com/o/r/releases/download/v1/a.exe",
		"https://evil.example/o/r/releases/download/v1/a.exe",
		"https://github.com.evil.example/o/r/releases/download/v1/a.exe",
		"https://github.com/o/r/archive/main.zip",
	} {
		if TrustedDownloadURL(bad) == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	redirect := func(raw string) error {
		u, _ := url.Parse(raw)
		return TrustedRedirect(&http.Request{URL: u}, nil)
	}
	if err := redirect("https://release-assets.githubusercontent.com/x"); err != nil {
		t.Fatal(err)
	}
	if redirect("https://evil.example/x") == nil || redirect("http://objects.githubusercontent.com/x") == nil {
		t.Fatal("untrusted redirect accepted")
	}
}
