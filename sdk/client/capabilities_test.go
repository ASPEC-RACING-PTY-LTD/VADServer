package client

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// strictDecode decodes a request body rejecting unknown fields, as the
// server does.
func strictDecode(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func TestCapabilitiesInfoMe(t *testing.T) {
	f, c := newFake(t)
	ctx := context.Background()
	f.handle("GET /api/v1/info", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, map[string]any{"name": "VaultDock", "features": []string{wire.FeatureBackup}})
	})
	f.handle("GET /api/v1/device/me", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, map[string]any{"features": []string{wire.FeatureModules}})
	})
	f.handle("GET /api/v1/backup/capabilities", func(w http.ResponseWriter, r *http.Request, _ []byte) {
		writeJSON(w, 200, wire.Capabilities{LongPollSeconds: 25, Formats: []string{wire.CapabilitiesFormatRepoV1}})
	})
	f.handle("PUT /api/v1/device/backup-state", func(w http.ResponseWriter, r *http.Request, body []byte) {
		var st wire.BackupState
		if err := strictDecode(body, &st); err != nil || !st.ServiceInstalled {
			writeErr(w, 400, "invalid", nil)
			return
		}
		w.WriteHeader(204)
	})
	info, err := c.Info(ctx)
	if err != nil || !info.HasFeature(wire.FeatureBackup) || info.HasFeature(wire.FeatureAI) {
		t.Fatalf("info: %+v %v", info, err)
	}
	me, err := c.Me(ctx)
	if err != nil || !me.HasFeature(wire.FeatureModules) {
		t.Fatalf("me: %+v %v", me, err)
	}
	caps, err := c.Capabilities(ctx)
	if err != nil || caps.LongPollSeconds != 25 {
		t.Fatalf("caps: %+v %v", caps, err)
	}
	if err := c.ReportBackupState(ctx, wire.BackupState{ServiceInstalled: true}); err != nil {
		t.Fatal(err)
	}
}
