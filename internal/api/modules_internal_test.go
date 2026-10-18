package api

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/automation"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestRequireModules(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "vaultdock.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	u, err := st.CreateUser(ctx, store.NewUser{Username: "alice", PasswordHash: "x", Role: "user"})
	if err != nil {
		t.Fatal(err)
	}
	d := &store.Device{UserID: u.ID, Name: "PC", Identifier: "pc", UserCode: "AAAA-BBBB"}
	if err := st.CreateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	s := &Server{st: st}

	check := func(name string, err error, module string) {
		t.Helper()
		if module == "" {
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			return
		}
		var ae *apiError
		if !errors.As(err, &ae) || ae.Status != 409 || ae.Code != "module_unavailable" {
			t.Fatalf("%s: %v", name, err)
		}
		det := ae.Details.(map[string]any)
		if det["module"] != module || det["device_id"] != d.ID {
			t.Fatalf("%s details: %v", name, det)
		}
	}

	// Not reported yet: only core.
	check("none", s.requireModules(ctx, d.ID), "")
	check("core unreported", s.requireModules(ctx, d.ID, wire.ModuleCore), "")
	check("sync unreported", s.requireModules(ctx, d.ID, wire.ModuleSync), wire.ModuleSync)

	if err := st.PutDeviceModules(ctx, &store.DeviceModules{DeviceID: d.ID, InstallMode: "advanced", Modules: []wire.ModuleStatus{
		{ID: wire.ModuleCore, State: wire.ModuleStateEnabled},
		{ID: wire.ModuleSync, State: wire.ModuleStateEnabled},
		{ID: wire.ModuleDiscovery, State: wire.ModuleStateInstalling},
		{ID: wire.ModuleReinstall, State: wire.ModuleStateDisabled},
	}}); err != nil {
		t.Fatal(err)
	}
	check("sync", s.requireModules(ctx, d.ID, wire.ModuleSync, wire.ModuleCore), "")
	check("installing", s.requireModules(ctx, d.ID, wire.ModuleSync, wire.ModuleDiscovery), wire.ModuleDiscovery)
	check("first missing", s.requireModules(ctx, d.ID, wire.ModuleReinstall, wire.ModuleDiscovery), wire.ModuleReinstall)
	check("not listed", s.requireModules(ctx, d.ID, wire.ModuleVSS), wire.ModuleVSS)

	// Automation CheckSupport mapping.
	check("unsupported module", moduleErrorFrom(d.ID, &automation.UnsupportedError{NodeID: "n", Module: wire.ModuleSync}), wire.ModuleSync)
	other := &automation.UnsupportedError{SchemaVersion: 3}
	if err := moduleErrorFrom(d.ID, other); err != error(other) {
		t.Fatalf("version error mapped: %v", err)
	}
	if moduleErrorFrom(d.ID, nil) != nil {
		t.Fatal("nil mapped")
	}
}
