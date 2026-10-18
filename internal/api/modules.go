// Owner: SRV-M (P1-19). Scope: api-v1 §6A install modes and modules (feature
// modules.v1) and the module gating helper used by other feature areas
// (api-v1 §1 "Module gating", 409 module_unavailable).

package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/automation"
	sdkmod "github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/modules"
	"github.com/ASPEC-RACING-PTY-LTD/vad-client/sdk/wire"
	"github.com/ASPEC-RACING-PTY-LTD/vad-server/internal/store"
)

func init() { implementFeature(wire.FeatureModules) }

// mountModules registers the routes of this feature area.
func (s *Server) mountModules(rt routes) {
	rt.Dev.Put("/device/modules", s.handlePutDeviceModules)
	rt.User.Get("/modules/catalog", s.handleModuleCatalog)
	rt.Sess.Get("/devices/{id}/modules", s.handleDeviceModules)           // owner/admin
	rt.Sess.Post("/devices/{id}/mode-request", s.handleModeChangeRequest) // owner/admin
}

// --- gating ---

// moduleUnavailable is the api-v1 §1.1 error for a device that lacks a module.
func moduleUnavailable(deviceID int64, module string) *apiError {
	return &apiError{Status: 409, Code: "module_unavailable",
		Message: fmt.Sprintf("This device does not have the %q module enabled. Requires Advanced mode on this device.", module),
		Details: map[string]any{"module": module, "device_id": deviceID}}
}

// enabledModules returns the modules the device reported as enabled. A device
// that has not reported yet has only core: core is the installer itself, and
// the server never infers any other module (api-v1 §6A).
func (s *Server) enabledModules(ctx context.Context, deviceID int64) ([]string, error) {
	m, err := s.st.DeviceModules(ctx, deviceID)
	if errors.Is(err, store.ErrNotFound) {
		return []string{wire.ModuleCore}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := m.EnabledIDs()
	if !m.Enabled(wire.ModuleCore) {
		ids = append([]string{wire.ModuleCore}, ids...)
	}
	return ids, nil
}

// requireModules is the module gate for other feature areas: it returns nil
// when every module in modules is enabled on the device according to its last
// PUT /device/modules report, and otherwise 409 module_unavailable with
// details {module, device_id} for the first missing one. Pass the error to
// writeErr. Typical use:
//
//	if err := s.requireModules(r.Context(), job.DeviceID, job.RequiredModules...); err != nil {
//		writeErr(w, r, err)
//		return
//	}
func (s *Server) requireModules(ctx context.Context, deviceID int64, modules ...string) error {
	if len(modules) == 0 {
		return nil
	}
	have, err := s.enabledModules(ctx, deviceID)
	if err != nil {
		return err
	}
	return missingModule(deviceID, have, modules)
}

func missingModule(deviceID int64, have, need []string) error {
	set := make(map[string]bool, len(have))
	for _, m := range have {
		set[m] = true
	}
	for _, m := range need {
		if !set[m] {
			return moduleUnavailable(deviceID, m)
		}
	}
	return nil
}

// checkAutomationSupport runs automation.CheckSupport against the device's
// enabled modules. A missing module becomes 409 module_unavailable; any other
// *automation.UnsupportedError (version, runtime) is returned unchanged for the
// caller to map.
func (s *Server) checkAutomationSupport(ctx context.Context, deviceID int64, a *automation.Automation, version int) error {
	have, err := s.enabledModules(ctx, deviceID)
	if err != nil {
		return err
	}
	return moduleErrorFrom(deviceID, automation.CheckSupport(a, version, have))
}

// moduleErrorFrom converts an *automation.UnsupportedError whose Module is set
// into 409 module_unavailable; other errors (and nil) pass through.
func moduleErrorFrom(deviceID int64, err error) error {
	var ue *automation.UnsupportedError
	if errors.As(err, &ue) && ue.Module != "" {
		return moduleUnavailable(deviceID, ue.Module)
	}
	return err
}

// --- device report ---

const (
	maxModuleVersion = 64
	maxModuleError   = 500
)

// validateModuleReport checks a PUT /device/modules body. Unknown module ids
// are dropped (a newer client may know modules this server does not); every
// other problem is a 422 validation error with details.fields.
func validateModuleReport(req *wire.ModuleReport) (*store.DeviceModules, error) {
	fields := map[string]string{}
	if !sdkmod.Mode(req.InstallMode).Valid() {
		fields["install_mode"] = "must be simple or advanced"
	}
	core := cleanText(req.CoreVersion, maxModuleVersion+1)
	if len(core) > maxModuleVersion {
		fields["core_version"] = "too long"
	}
	out := &store.DeviceModules{InstallMode: req.InstallMode, CoreVersion: core, Modules: []wire.ModuleStatus{}}
	seen := map[string]bool{}
	for i, m := range req.Modules {
		key := fmt.Sprintf("modules[%d]", i)
		if !sdkmod.Known(sdkmod.ID(m.ID)) {
			continue
		}
		if seen[m.ID] {
			fields[key+".id"] = "duplicate module"
			continue
		}
		seen[m.ID] = true
		if !sdkmod.State(m.State).Valid() {
			fields[key+".state"] = "must be enabled, disabled, installing or failed"
		}
		v := cleanText(m.Version, maxModuleVersion+1)
		if len(v) > maxModuleVersion {
			fields[key+".version"] = "too long"
		}
		out.Modules = append(out.Modules, wire.ModuleStatus{ID: m.ID, Version: v, State: m.State, Error: cleanText(m.Error, maxModuleError)})
	}
	if len(fields) > 0 {
		return nil, &apiError{Status: 422, Code: "validation", Message: "Invalid module report", Details: map[string]any{"fields": fields}}
	}
	return out, nil
}

func (s *Server) handlePutDeviceModules(w http.ResponseWriter, r *http.Request) {
	d := principal(r).Device
	var req wire.ModuleReport
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	m, err := validateModuleReport(&req)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	m.DeviceID = d.ID
	if err := s.st.PutDeviceModules(r.Context(), m); err != nil {
		writeErr(w, r, err)
		return
	}
	slog.Debug("device modules reported", "device", d.ID, "mode", m.InstallMode, "enabled", m.EnabledIDs())
	// Suspend or resume jobs whose required modules changed with this report.
	if err := s.applyJobModuleState(r.Context(), d.ID); err != nil {
		slog.Warn("job module state", "device", d.ID, "err", err)
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- session views ---

// ownerOrAdminDevice loads {id} for its owner or an admin; anyone else gets
// 404 so device ids are not disclosed.
func (s *Server) ownerOrAdminDevice(r *http.Request) (*store.Device, error) {
	id, err := idParam(r, "id")
	if err != nil {
		return nil, err
	}
	p := principal(r)
	d, err := s.st.Device(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if d.UserID != p.User.ID && !p.User.IsAdmin() {
		return nil, store.ErrNotFound
	}
	return d, nil
}

// handleDeviceModules answers GET /devices/{id}/modules. A device that has not
// reported yet gets install_mode "simple" (the column default), no modules
// and updated_at 0.
func (s *Server) handleDeviceModules(w http.ResponseWriter, r *http.Request) {
	d, err := s.ownerOrAdminDevice(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := wire.ModuleDeviceState{InstallMode: wire.ModuleInstallModeSimple, Modules: []wire.ModuleStatus{}}
	m, err := s.st.DeviceModules(r.Context(), d.ID)
	switch {
	case err == nil:
		out = wire.ModuleDeviceState{InstallMode: m.InstallMode, CoreVersion: m.CoreVersion, Modules: m.Modules, UpdatedAt: m.UpdatedAt}
	case !errors.Is(err, store.ErrNotFound):
		writeErr(w, r, err)
		return
	}
	writeJSON(w, 200, out)
}

// moduleCatalog is the static v1 catalog in contract table order.
var moduleCatalog = []wire.ModuleCatalogEntry{
	{ID: wire.ModuleCore, Name: "Core", Summary: "Background service, tray and app, folder backups, schedules, browsing and file restore.",
		Modes: []string{wire.ModuleInstallModeSimple, wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryBuiltin, RequiresFeatures: []string{}},
	{ID: wire.ModuleSync, Name: "Sync", Summary: "One-way and two-way folder sync.",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryBuiltin, RequiresFeatures: []string{wire.FeatureSyncTwoWay}},
	{ID: wire.ModuleAutomation, Name: "Automations", Summary: "Automation workflows (schedules are part of core).",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryBuiltin, RequiresFeatures: []string{}},
	{ID: wire.ModuleProfiles, Name: "Profiles", Summary: "Protects other Windows profiles on the device, with each user's consent.",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryBuiltin, RequiresFeatures: []string{wire.FeatureProfiles}},
	{ID: wire.ModuleVSS, Name: "Snapshots (VSS)", Summary: "Volume shadow copies so locked and open files can be backed up.",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryComponent, RequiresFeatures: []string{}},
	{ID: wire.ModuleDiscovery, Name: "Discovery", Summary: "Finds apps, games, development projects and registry settings to protect.",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryComponent, RequiresFeatures: []string{}},
	{ID: wire.ModuleReinstall, Name: "App reinstall", Summary: "Reinstalls apps through winget, Chocolatey or Scoop, with your confirmation.",
		Modes: []string{wire.ModuleInstallModeAdvanced}, Delivery: wire.ModuleDeliveryComponent, RequiresFeatures: []string{}},
}

func (s *Server) handleModuleCatalog(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, moduleCatalog)
}

// --- mode-change request ---

// deviceTaskSpec is what a feature area hands to the device-task queue.
type deviceTaskSpec struct {
	UserID          int64 // device owner
	DeviceID        int64
	CreatedBy       int64
	Kind            string
	Payload         any
	RequiredModules []string
}

// queueDeviceTask creates a pending device task and returns its id.
//
// INT: the device-task store is built in parallel by P1-17 (SRV-3). Until it
// is wired here, this default writes a minimal pending row into device_tasks
// directly (the P0-02 schema). Replace it with the P1-17 store call, e.g.
//
//	queueDeviceTask = func(s *Server, ctx context.Context, t deviceTaskSpec) (int64, error) {
//		return s.st.CreateDeviceTask(ctx, ...)
//	}
var queueDeviceTask = func(s *Server, ctx context.Context, t deviceTaskSpec) (int64, error) {
	payload, err := json.Marshal(t.Payload)
	if err != nil {
		return 0, err
	}
	req := t.RequiredModules
	if req == nil {
		req = []string{}
	}
	reqRaw, _ := json.Marshal(req)
	res, err := s.st.DB.ExecContext(ctx, `INSERT INTO device_tasks(user_id, device_id, kind, payload, required_modules, status, created_by, created_at)
		VALUES(?,?,?,?,?,'pending',?,?)`, t.UserID, t.DeviceID, t.Kind, string(payload), string(reqRaw), t.CreatedBy, time.Now().UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// handleModeChangeRequest answers POST /devices/{id}/mode-request. It only
// queues a mode_change_request task: a local Windows administrator must
// confirm it on the device, and the server never changes a mode itself.
func (s *Server) handleModeChangeRequest(w http.ResponseWriter, r *http.Request) {
	d, err := s.ownerOrAdminDevice(r)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	var req wire.ModuleModeChangeRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, r, err)
		return
	}
	if !sdkmod.Mode(req.TargetMode).Valid() {
		writeErr(w, r, &apiError{Status: 422, Code: "validation", Message: "Invalid target mode",
			Details: map[string]any{"fields": map[string]string{"target_mode": "must be simple or advanced"}}})
		return
	}
	if d.Status != "approved" {
		writeErr(w, r, errf(409, "device_not_approved", "The device is not approved"))
		return
	}
	p := principal(r)
	id, err := queueDeviceTask(s, r.Context(), deviceTaskSpec{
		UserID: d.UserID, DeviceID: d.ID, CreatedBy: p.User.ID, Kind: wire.TaskKindModeChangeRequest,
		Payload: wire.TaskModeChangeRequestPayload{TargetMode: req.TargetMode, RequestedBy: p.User.Username},
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	s.activity(r, d.UserID, "device.mode_request", d.Name, "target_mode="+req.TargetMode, true)
	writeJSON(w, 201, wire.DeviceTaskCreated{TaskID: id})
}
