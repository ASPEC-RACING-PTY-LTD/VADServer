package protocol

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	body := SHA256Hex([]byte(`{"a":1}`))
	now := time.Now()
	h, err := Sign(priv, "7", "post", "/api/v1/x?y=1", body, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(pub, "POST", "/api/v1/x?y=1", h, now); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	for name, mutate := range map[string]func(SignedHeaders) (string, string, SignedHeaders){
		"path":   func(h SignedHeaders) (string, string, SignedHeaders) { return "POST", "/api/v1/x?y=2", h },
		"method": func(h SignedHeaders) (string, string, SignedHeaders) { return "PUT", "/api/v1/x?y=1", h },
		"body": func(h SignedHeaders) (string, string, SignedHeaders) {
			h.BodySHA256 = EmptySHA256
			return "POST", "/api/v1/x?y=1", h
		},
		"device": func(h SignedHeaders) (string, string, SignedHeaders) {
			h.DeviceID = "8"
			return "POST", "/api/v1/x?y=1", h
		},
		"nonce": func(h SignedHeaders) (string, string, SignedHeaders) {
			h.Nonce = "other"
			return "POST", "/api/v1/x?y=1", h
		},
	} {
		m, p, hh := mutate(h)
		if _, err := Verify(pub, m, p, hh, now); err == nil {
			t.Errorf("tampered %s accepted", name)
		}
	}
	if _, err := Verify(pub, "POST", "/api/v1/x?y=1", h, now.Add(10*time.Minute)); err != ErrStale {
		t.Errorf("stale request: %v", err)
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	h2, _ := Sign(other, "7", "POST", "/api/v1/x?y=1", body, now)
	if _, err := Verify(pub, "POST", "/api/v1/x?y=1", h2, now); err == nil {
		t.Error("signature by another key accepted")
	}
}
