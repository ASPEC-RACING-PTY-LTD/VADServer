package api_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/client"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/protocol"
	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/api"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestSummary(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()

	out := alice.must("GET", "/api/v1/summary", nil, 200)
	// Approving the device notified alice; everything else starts at zero.
	unread, _ := out["unread_notifications"].(float64)
	for _, k := range []string{"pending_devices", "pending_approvals", "running_jobs", "failed_runs_24h", "open_conflicts", "blacklisted_profiles"} {
		if v, ok := out[k].(float64); !ok || v != 0 {
			t.Fatalf("%s = %v in %v", k, out[k], out)
		}
	}
	if out["readiness"] != wire.ReadinessStatusUnknown {
		t.Fatalf("readiness = %v", out["readiness"])
	}

	// A pending enrolment and an unread notification are counted for their
	// owner only.
	base, _ := url.Parse(e.ts.URL)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err := client.New(base, client.Options{}).Enroll(ctx, client.EnrollRequest{Login: "alice", Password: "password-alice", DeviceName: "PENDING", OS: "Windows 11", Identifier: "pending", PublicKey: protocol.EncodePublicKey(pub), ClientVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	unread, _ = alice.must("GET", "/api/v1/summary", nil, 200)["unread_notifications"].(float64)
	u, err := e.st.UserByLogin(ctx, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.AddNotification(ctx, u.ID, store.Notification{Title: "t", Message: "m", Level: "info", Source: "test"}); err != nil {
		t.Fatal(err)
	}
	out = alice.must("GET", "/api/v1/summary", nil, 200)
	if out["pending_devices"] != 1.0 || out["unread_notifications"] != unread+1 {
		t.Fatalf("alice summary: %v", out)
	}
	if out = admin.must("GET", "/api/v1/summary", nil, 200); out["pending_devices"] != 0.0 {
		t.Fatalf("admin summary: %v", out)
	}

	// Session only.
	if st, code := apiCode(t, dc.Call(ctx, "GET", "/api/v1/summary", nil, nil)); st != 403 || code != "session_required" {
		t.Fatalf("device summary: %d %s", st, code)
	}
}

func waitOperation(t *testing.T, w *web, id int64, state string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := w.must("GET", fmt.Sprintf("/api/v1/operations/%d", id), nil, 200)
		if out["state"] == state {
			return out
		}
		if time.Now().After(deadline) {
			t.Fatalf("operation %d never reached %s: %v", id, state, out)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestOperations(t *testing.T) {
	e := newEnv(t, "")
	admin := e.setupAdmin()
	alice := e.createUser(admin, "alice", 0)
	bob := e.createUser(admin, "bob", 0)
	dc := e.enrollDevice(alice, "alice", "password-alice")
	ctx := context.Background()
	u, _ := e.st.UserByLogin(ctx, "alice")
	defer e.srv.StopOperations(ctx)

	// A user operation reports progress, then its result.
	release := make(chan struct{})
	id, err := e.srv.StartOperation(api.OpStart{Kind: wire.OperationKindVerify, UserID: &u.ID, Params: map[string]any{"repo_id": 1}},
		func(ctx context.Context, p *api.OpProgress) (any, error) {
			p.Set(1, 2, 512)
			<-release
			return map[string]any{"checked": 2}, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/operations/%d", id)
	deadline := time.Now().Add(10 * time.Second)
	for {
		out := alice.must("GET", path, nil, 200)
		if pr, _ := out["progress"].(map[string]any); out["state"] == wire.OperationStateRunning && pr["done"] == 1.0 && pr["total"] == 2.0 && pr["bytes"] == 512.0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no progress: %v", out)
		}
		time.Sleep(20 * time.Millisecond)
	}
	// Other users, admins and devices that did not create it see 404.
	for name, w := range map[string]*web{"bob": bob, "admin": admin} {
		if out := w.must("GET", path, nil, 404); errCode(out) != "not_found" {
			t.Fatalf("%s: %v", name, out)
		}
	}
	if st, _ := apiCode(t, dc.Call(ctx, "GET", path, nil, nil)); st != 404 {
		t.Fatalf("device sees foreign operation: %d", st)
	}
	close(release)
	out := waitOperation(t, alice, id, wire.OperationStateSucceeded)
	if res, _ := out["result"].(map[string]any); res["checked"] != 2.0 || out["error"] != "" || out["kind"] != wire.OperationKindVerify || out["id"] != float64(id) {
		t.Fatalf("finished: %v", out)
	}

	// A device sees operations it created; failures carry the message only
	// for API errors, and panics are contained.
	devID := dc.DeviceID
	fail, err := e.srv.StartOperation(api.OpStart{Kind: wire.OperationKindRestore, UserID: &u.ID, DeviceID: &devID},
		func(context.Context, *api.OpProgress) (any, error) { return nil, errors.New("secret internal detail") })
	if err != nil {
		t.Fatal(err)
	}
	out = waitOperation(t, alice, fail, wire.OperationStateFailed)
	if out["error"] != "Internal server error" {
		t.Fatalf("internal error leaked: %v", out)
	}
	var op wire.Operation
	if err := dc.Call(ctx, "GET", fmt.Sprintf("/api/v1/operations/%d", fail), nil, &op); err != nil || op.ID != fail || op.State != wire.OperationStateFailed {
		t.Fatalf("device own operation: %+v %v", op, err)
	}
	panicky, _ := e.srv.StartOperation(api.OpStart{Kind: wire.OperationKindGC},
		func(context.Context, *api.OpProgress) (any, error) { panic("boom") })
	// System operations are visible to admins only.
	waitOperation(t, admin, panicky, wire.OperationStateFailed)
	alice.must("GET", fmt.Sprintf("/api/v1/operations/%d", panicky), nil, 404)

	for _, p := range []string{"/api/v1/operations/0", "/api/v1/operations/x"} {
		if out := alice.must("GET", p, nil, 400); errCode(out) != "bad_id" {
			t.Fatalf("%s: %v", p, out)
		}
	}
	alice.must("GET", "/api/v1/operations/99999", nil, 404)

	// Shutdown cancels running operations and refuses new ones.
	started := make(chan struct{})
	long, _ := e.srv.StartOperation(api.OpStart{Kind: wire.OperationKindPrune, UserID: &u.ID},
		func(ctx context.Context, _ *api.OpProgress) (any, error) {
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		})
	<-started
	e.srv.StopOperations(ctx)
	if o, err := e.st.Operation(ctx, long); err != nil || o.State != wire.OperationStateCancelled {
		t.Fatalf("after stop: %+v %v", o, err)
	}
	if _, err := e.srv.StartOperation(api.OpStart{Kind: wire.OperationKindPrune}, nil); err == nil {
		t.Fatal("operation started after shutdown")
	}
}
