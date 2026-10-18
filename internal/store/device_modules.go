package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/ASPEC-RACING-PTY-LTD/VaultDock/sdk/wire"
)

// DeviceModules is the last install-mode and module report of a device
// (api-v1 §6A, table device_modules). Modules is exactly what the device
// reported; the server never derives modules from the mode.
type DeviceModules struct {
	DeviceID    int64
	InstallMode string
	CoreVersion string
	Modules     []wire.ModuleStatus
	UpdatedAt   int64
}

// Enabled reports whether module id is reported with state enabled.
func (m *DeviceModules) Enabled(id string) bool {
	if m == nil {
		return false
	}
	for _, x := range m.Modules {
		if x.ID == id {
			return x.State == wire.ModuleStateEnabled
		}
	}
	return false
}

// EnabledIDs returns the ids of the enabled modules in report order (never
// nil).
func (m *DeviceModules) EnabledIDs() []string {
	out := []string{}
	if m == nil {
		return out
	}
	for _, x := range m.Modules {
		if x.State == wire.ModuleStateEnabled {
			out = append(out, x.ID)
		}
	}
	return out
}

// PutDeviceModules stores a device's report, replacing the previous one, and
// sets m.UpdatedAt. Callers validate the report first.
func (s *Store) PutDeviceModules(ctx context.Context, m *DeviceModules) error {
	if m.DeviceID <= 0 {
		return errors.New("device id required")
	}
	if m.Modules == nil {
		m.Modules = []wire.ModuleStatus{}
	}
	raw, err := json.Marshal(m.Modules)
	if err != nil {
		return err
	}
	m.UpdatedAt = now()
	_, err = s.DB.ExecContext(ctx, `INSERT INTO device_modules(device_id, install_mode, core_version, modules, updated_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(device_id) DO UPDATE SET install_mode = excluded.install_mode, core_version = excluded.core_version,
			modules = excluded.modules, updated_at = excluded.updated_at`,
		m.DeviceID, m.InstallMode, m.CoreVersion, string(raw), m.UpdatedAt)
	return err
}

// DeviceModules returns a device's last report, or ErrNotFound when the
// device has never reported.
func (s *Store) DeviceModules(ctx context.Context, deviceID int64) (*DeviceModules, error) {
	m := DeviceModules{DeviceID: deviceID}
	var raw string
	err := s.DB.QueryRowContext(ctx, `SELECT install_mode, core_version, modules, updated_at FROM device_modules WHERE device_id = ?`, deviceID).
		Scan(&m.InstallMode, &m.CoreVersion, &raw, &m.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(jsonOr(raw, "[]")), &m.Modules); err != nil {
		return nil, err
	}
	if m.Modules == nil {
		m.Modules = []wire.ModuleStatus{}
	}
	return &m, nil
}
