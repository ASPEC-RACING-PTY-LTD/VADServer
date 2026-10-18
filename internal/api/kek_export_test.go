package api_test

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

func TestKEKExportOnce(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(context.Background(), e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(m)

	if code, _ := alice.req("POST", "/api/v1/admin/keys/kek/export", nil); code != 403 {
		t.Fatalf("non-superuser export: %d", code)
	}
	if st := admin.must("GET", "/api/v1/admin/keys/kek", nil, 200); st["exported"] != false || st["available"] != true {
		t.Fatalf("status before: %v", st)
	}
	download := func() (int, []byte) {
		req, _ := http.NewRequest("POST", admin.base+"/api/v1/admin/keys/kek/export", nil)
		req.Header.Set("X-CSRF-Token", admin.csrf)
		resp, err := admin.c.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	if code, b := download(); code != 200 || !bytes.Equal(b, kek[:]) {
		t.Fatalf("first export: %d %d bytes", code, len(b))
	}
	if code, _ := download(); code != 409 {
		t.Fatalf("second export: %d", code)
	}
	if st := admin.must("GET", "/api/v1/admin/keys/kek", nil, 200); st["exported"] != true {
		t.Fatalf("status after: %v", st)
	}
}
