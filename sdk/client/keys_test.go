package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

func TestCheckSIKPin(t *testing.T) {
	id := &wire.ServerIdentity{SIKFingerprint: "ab:cd:ef:01"}
	for _, pin := range []string{"ABCDEF01", "ab:cd:ef:01", " AB-CD-EF-01 "} {
		if err := CheckSIKPin(pin, id); err != nil {
			t.Errorf("%q: %v", pin, err)
		}
	}
	err := CheckSIKPin("abcdef02", id)
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != wire.CodeSIKMismatch {
		t.Fatalf("mismatch: %v", err)
	}
	if !IsCode(CheckSIKPin("abcdef01", nil), wire.CodeSIKMismatch) {
		t.Fatal("nil identity accepted")
	}
	if !errors.Is(CheckSIKPin("", id), ErrNoSIKPin) {
		t.Fatal("empty pin accepted")
	}
}

func TestServerIdentityAndKeyTasks(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("GET /api/v1/keys/server", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, wire.ServerIdentityResponse{ServerIdentity: wire.ServerIdentity{SIKPub: "p", SIKFingerprint: "AA:BB", SEKVersion: 2}})
	})
	f.handle("GET /api/v1/repos/3/keys/status", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, wire.KeyStatus{CurrentEpoch: 4, EscrowMode: wire.EscrowModeFull})
	})
	var results []wire.DeviceTaskResultRequest
	f.handle("POST /api/v1/device/tasks/9/result", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var req wire.DeviceTaskResultRequest
		json.Unmarshal(body, &req)
		results = append(results, req)
		w.WriteHeader(204)
	})

	id, err := c.VerifyServerIdentity(ctx, "aabb")
	if err != nil || id.SEKVersion != 2 {
		t.Fatalf("verify: %+v %v", id, err)
	}
	if _, err := c.VerifyServerIdentity(ctx, "aacc"); !IsCode(err, wire.CodeSIKMismatch) {
		t.Fatalf("mismatch: %v", err)
	}
	if ks, err := c.KeyStatus(ctx, 3); err != nil || ks.CurrentEpoch != 4 {
		t.Fatalf("status: %+v %v", ks, err)
	}
	if err := c.FinishKeyShare(ctx, 9, KeyTaskResult{RepoID: 3, KeyFileOID: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := c.FinishRewrap(ctx, 9, KeyTaskResult{RepoID: 3, Epoch: 5, Error: "no key"}); err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[0].Status != wire.TaskStatusDone || results[1].Status != wire.TaskStatusFailed {
		t.Fatalf("results %+v", results)
	}
}
