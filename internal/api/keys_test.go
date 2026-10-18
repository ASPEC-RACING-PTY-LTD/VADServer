package api_test

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/vdcrypt"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/keys"
)

func TestServerKeysRoutes(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	ctx := context.Background()

	// Without a KEK the routes answer 409 escrow_unavailable.
	if code, out := alice.req("GET", "/api/v1/keys/server", nil); code != 409 || errCode(out) != wire.CodeEscrowUnavailable {
		t.Fatalf("no keys: %d %v", code, out)
	}
	if code, out := admin.req("POST", "/api/v1/admin/keys/rotate-server", nil); code != 409 || errCode(out) != wire.CodeEscrowUnavailable {
		t.Fatalf("rotate without keys: %d %v", code, out)
	}

	kek, _ := vdcrypt.RandomKey(nil)
	m, err := keys.New(ctx, e.st, kek)
	if err != nil {
		t.Fatal(err)
	}
	e.srv.SetKeys(m)

	id := alice.must("GET", "/api/v1/keys/server", nil, 200)["server_identity"].(map[string]any)
	sik, _ := base64.StdEncoding.DecodeString(id["sik_pub"].(string))
	if id["sik_fingerprint"] != keys.Fingerprint(sik) || id["sek_version"] != float64(1) {
		t.Fatalf("identity: %v", id)
	}

	// Signed device requests see the same identity and pass the pin check.
	dc := e.enrollDevice(alice, "alice", "password-alice")
	if got, err := dc.VerifyServerIdentity(ctx, id["sik_fingerprint"].(string)); err != nil || got.SEKVersion != 1 {
		t.Fatalf("device identity: %v %v", got, err)
	}

	// Rotation is superuser-only and rotates the SEK, keeping the SIK.
	if code, out := alice.req("POST", "/api/v1/admin/keys/rotate-server", nil); code != 403 {
		t.Fatalf("user rotate: %d %v", code, out)
	}
	out := admin.must("POST", "/api/v1/admin/keys/rotate-server", nil, 202)
	op := waitOperation(t, admin, int64(out["operation_id"].(float64)), wire.OperationStateSucceeded)
	if op["kind"] != wire.OperationKindRotateKeys || op["result"].(map[string]any)["sek_version"] != float64(2) {
		t.Fatalf("operation: %v", op)
	}
	id2 := alice.must("GET", "/api/v1/keys/server", nil, 200)["server_identity"].(map[string]any)
	if id2["sek_version"] != float64(2) || id2["sik_pub"] != id["sik_pub"] || id2["sek_pub"] == id["sek_pub"] {
		t.Fatalf("after rotation: %v", id2)
	}
	sekB, _ := base64.StdEncoding.DecodeString(id2["sek_pub"].(string))
	sig, _ := base64.StdEncoding.DecodeString(id2["sek_sig"].(string))
	sek, _ := vdcrypt.KeyFromBytes(sekB)
	if !vdcrypt.VerifySEK(sik, sek, 2, sig) {
		t.Fatal("rotated SEK signature")
	}
	if rows, err := e.st.ListKeyAudit(ctx, 0, 0, 0); err != nil || len(rows) != 1 || rows[0].Action != keys.ActionRotateSEK || rows[0].OperationID == nil {
		t.Fatalf("audit: %v %v", rows, err)
	}
}
