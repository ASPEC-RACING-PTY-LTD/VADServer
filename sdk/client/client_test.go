package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestSelfSignedPinning(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"name":"VaultDock","version":"test"}`))
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL)
	ctx := context.Background()

	// Unknown certificate: refused, but its fingerprint is reported for the user to verify.
	_, err := Probe(ctx, base, "")
	var ue *UntrustedCertError
	if !errors.As(err, &ue) {
		t.Fatalf("expected UntrustedCertError, got %v", err)
	}
	want := CertFingerprint(srv.Certificate())
	if ue.Fingerprint != want {
		t.Fatalf("fingerprint %s want %s", ue.Fingerprint, want)
	}

	// After the user confirms, exactly that certificate is trusted.
	info, err := Probe(ctx, base, FormatFingerprint(ue.Fingerprint))
	if err != nil || info["name"] != "VaultDock" {
		t.Fatalf("pinned probe failed: %v %v", info, err)
	}

	// Any other certificate is rejected even when pinning is used.
	wrong := strings.Repeat("ab", 32)
	if _, err := Probe(ctx, base, wrong); err == nil {
		t.Fatal("wrong pin accepted")
	}
}

func TestNormalizeURL(t *testing.T) {
	for in, want := range map[string]string{
		"vault.example.com":          "https://vault.example.com",
		"https://vault.example.com/": "https://vault.example.com",
		"http://localhost:8080":      "http://localhost:8080",
		"http://127.0.0.1:8080":      "http://127.0.0.1:8080",
	} {
		u, err := NormalizeURL(in)
		if err != nil || u.String() != want {
			t.Errorf("%s -> %v %v", in, u, err)
		}
	}
	for _, bad := range []string{"http://vault.example.com", "http://192.168.1.20:8080", "ftp://x", ""} {
		if _, err := NormalizeURL(bad); err == nil {
			t.Errorf("%s accepted", bad)
		}
	}
}
