package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func TestDeviceModulesUpsert(t *testing.T) {
	st, _ := openStore(t)
	ctx := context.Background()
	u := newUser(t, st, "alice")
	d := &store.Device{UserID: u.ID, Name: "PC", Identifier: "pc", UserCode: "AAAA-BBBB"}
	if err := st.CreateDevice(ctx, d); err != nil {
		t.Fatal(err)
	}

	if _, err := st.DeviceModules(ctx, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unreported device: %v", err)
	}
	var none *store.DeviceModules
	if none.Enabled(wire.ModuleCore) || len(none.EnabledIDs()) != 0 {
		t.Fatal("nil report has enabled modules")
	}

	m := &store.DeviceModules{DeviceID: d.ID, InstallMode: wire.ModuleInstallModeSimple, CoreVersion: "1.0.0",
		Modules: []wire.ModuleStatus{{ID: wire.ModuleCore, Version: "1.0.0", State: wire.ModuleStateEnabled}}}
	if err := st.PutDeviceModules(ctx, m); err != nil {
		t.Fatal(err)
	}
	if m.UpdatedAt == 0 {
		t.Fatal("updated_at not set")
	}
	got, err := st.DeviceModules(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.InstallMode != "simple" || got.CoreVersion != "1.0.0" || len(got.Modules) != 1 || !got.Enabled(wire.ModuleCore) || got.Enabled(wire.ModuleSync) {
		t.Fatalf("first report: %+v", got)
	}

	// A second report replaces the first one completely.
	m = &store.DeviceModules{DeviceID: d.ID, InstallMode: wire.ModuleInstallModeAdvanced, CoreVersion: "1.1.0",
		Modules: []wire.ModuleStatus{
			{ID: wire.ModuleCore, Version: "1.1.0", State: wire.ModuleStateEnabled},
			{ID: wire.ModuleSync, Version: "1.1.0", State: wire.ModuleStateEnabled},
			{ID: wire.ModuleVSS, State: wire.ModuleStateFailed, Error: "download failed"},
		}}
	if err := st.PutDeviceModules(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, _ = st.DeviceModules(ctx, d.ID)
	if got.InstallMode != "advanced" || got.CoreVersion != "1.1.0" || len(got.Modules) != 3 || got.Modules[2].Error != "download failed" {
		t.Fatalf("second report: %+v", got)
	}
	if ids := got.EnabledIDs(); len(ids) != 2 || ids[0] != "core" || ids[1] != "sync" || got.Enabled(wire.ModuleVSS) {
		t.Fatalf("enabled: %v", ids)
	}

	// Nil modules are stored as an empty list; bad input is rejected.
	if err := st.PutDeviceModules(ctx, &store.DeviceModules{DeviceID: d.ID, InstallMode: "simple"}); err != nil {
		t.Fatal(err)
	}
	if got, _ = st.DeviceModules(ctx, d.ID); got.Modules == nil || len(got.Modules) != 0 {
		t.Fatalf("empty report: %+v", got)
	}
	if err := st.PutDeviceModules(ctx, &store.DeviceModules{DeviceID: d.ID, InstallMode: "expert"}); err == nil {
		t.Fatal("bad install mode accepted by the CHECK constraint")
	}
	if err := st.PutDeviceModules(ctx, &store.DeviceModules{InstallMode: "simple"}); err == nil {
		t.Fatal("missing device id accepted")
	}

	// The row goes with the device.
	if err := st.DeleteDevice(ctx, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DeviceModules(ctx, d.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("after device delete: %v", err)
	}
}
