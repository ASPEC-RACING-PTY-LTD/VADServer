// Hand-written mirror of the install-mode/module fields of GET /devices and
// GET /devices/{id}/modules (api-v1 §6A, P1-19 deviceJSON).

export type InstallMode = 'simple' | 'advanced'

/** Fixed v1 module ids (api-v1 §6A). */
export type ModuleId = 'core' | 'sync' | 'automation' | 'profiles' | 'vss' | 'discovery' | 'reinstall'

export type ModuleState = 'enabled' | 'disabled' | 'installing' | 'failed'

/** `modules_summary` on each GET /devices row. `updated_at` 0 = the device never reported. */
export interface ModulesSummary {
  enabled: string[]
  failed: string[]
  updated_at: number
}

/** The §6A fields a GET /devices row carries (absent on servers without modules.v1). */
export interface DeviceModeFields {
  install_mode?: InstallMode | (string & {})
  modules_summary?: ModulesSummary
}

export interface ModuleStatus {
  id: ModuleId | (string & {})
  version: string
  state: ModuleState | (string & {})
  error: string
}

/** GET /devices/{id}/modules. */
export interface DeviceModuleState {
  install_mode: InstallMode | (string & {})
  core_version: string
  modules: ModuleStatus[]
  updated_at: number
}

export const moduleLabels: Record<ModuleId, string> = {
  core: 'Core',
  sync: 'Sync',
  automation: 'Automation',
  profiles: 'Profiles',
  vss: 'VSS',
  discovery: 'Discovery',
  reinstall: 'Reinstall',
}

export function moduleLabel(id: string): string {
  return moduleLabels[id as ModuleId] ?? id
}

export function modeLabel(mode: string | undefined): string {
  if (mode === 'advanced') return 'Advanced'
  if (mode === 'simple') return 'Simple'
  return mode || 'Unknown'
}
