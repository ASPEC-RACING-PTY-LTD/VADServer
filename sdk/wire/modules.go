package wire

// Module ids, fixed for v1 (api-v1 §6A).
const (
	ModuleCore       = "core"
	ModuleSync       = "sync"
	ModuleAutomation = "automation"
	ModuleProfiles   = "profiles"
	ModuleVSS        = "vss"
	ModuleDiscovery  = "discovery"
	ModuleReinstall  = "reinstall"
)

// Install modes.
const (
	ModuleInstallModeSimple   = "simple"
	ModuleInstallModeAdvanced = "advanced"
)

// Module states.
const (
	ModuleStateEnabled    = "enabled"
	ModuleStateDisabled   = "disabled"
	ModuleStateInstalling = "installing"
	ModuleStateFailed     = "failed"
)

// Module delivery kinds in the catalog.
const (
	ModuleDeliveryBuiltin   = "builtin"
	ModuleDeliveryComponent = "component"
)

// ModuleStatus is one module as reported by a device.
type ModuleStatus struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	State   string `json:"state"`
	Error   string `json:"error,omitempty"`
}

// ModuleReport is the body of PUT /device/modules (dev), sent at enrolment,
// on change and daily. The server answers 204.
type ModuleReport struct {
	InstallMode string         `json:"install_mode"`
	CoreVersion string         `json:"core_version"`
	Modules     []ModuleStatus `json:"modules"`
}

// ModuleDeviceState is the reply of GET /devices/{id}/modules (sess).
type ModuleDeviceState struct {
	InstallMode string         `json:"install_mode"`
	CoreVersion string         `json:"core_version"`
	Modules     []ModuleStatus `json:"modules"`
	UpdatedAt   int64          `json:"updated_at"`
}

// ModuleCatalogEntry is one item of GET /modules/catalog, which returns a
// bare JSON array.
type ModuleCatalogEntry struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Summary          string   `json:"summary"`
	Modes            []string `json:"modes"`
	Delivery         string   `json:"delivery"`
	RequiresFeatures []string `json:"requires_features"`
}

// ModuleModeChangeRequest is the body of POST /devices/{id}/mode-request
// (sess). It creates a mode_change_request device task; reply
// DeviceTaskCreated.
type ModuleModeChangeRequest struct {
	TargetMode string `json:"target_mode"`
}
