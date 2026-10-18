// Hand-written mirror of the wire backup job types (sdk/wire/jobs.go, api-v1 §6 rev 1.0 + 1.1).
import type { Schedule } from '../../components/ScheduleEditor'

export type JobState = 'active' | 'suspended_by_mode' | 'suspended_by_profile'
export type JobKind = 'folder' | 'profile' | 'app' | 'game' | 'devproject' | 'registry' | 'inventory'
export type JobMode = 'full' | 'data_only' | 'reinstall_inventory'
export type UseVSS = 'auto' | 'required' | 'off'

export interface JobSpec {
  paths: string[] | null
  exclude: string[] | null
  ruleset_id: number | null
  source_ref: string
  use_vss: UseVSS | ''
}

/** Wire `JobSchedule`; identical to the ScheduleEditor value. */
export type JobSchedule = Schedule

export interface Job {
  id: number
  device_id: number
  repo_id: number
  name: string
  kind: JobKind | (string & {})
  mode: JobMode | (string & {})
  spec: JobSpec
  schedule: JobSchedule
  retention_policy_id: number | null
  priority: number
  enabled: boolean
  /** Increments on every change; sent as If-Match on PATCH. */
  revision: number
  /** Windows profile the job reads; '' = the device's active user. */
  profile_sid: string
  /** Computed by the server (never includes core). */
  required_modules: string[] | null
  state: JobState | (string & {})
  created_at: number
  updated_at: number
}

/** POST /backup/jobs. */
export interface JobCreateRequest {
  device_id: number
  repo_id: number
  name: string
  kind: string
  mode: string
  spec: JobSpec
  schedule: JobSchedule
  retention_policy_id: number | null
  priority: number
  enabled: boolean
  profile_sid: string
}

/** PATCH /backup/jobs/{id} (with If-Match: revision). Omitted fields are unchanged. */
export type JobUpdateRequest = Partial<Pick<JobCreateRequest, 'name' | 'mode' | 'spec' | 'schedule' | 'retention_policy_id' | 'priority' | 'enabled' | 'profile_sid'>>

export type RunStatus = 'queued' | 'scanning' | 'uploading' | 'committing' | 'completed' | 'partial' | 'failed' | 'cancelled'
export type RunTrigger = 'schedule' | 'manual' | 'automation' | 'retry_skipped' | 'task'

export interface Run {
  id: number
  job_id: number
  device_id: number
  client_run_uuid: string
  trigger: RunTrigger | (string & {})
  snapshot_id: number | null
  started_at: number
  finished_at: number
  status: RunStatus | (string & {})
  phase: string
  files_total: number
  files_done: number
  bytes_total: number
  bytes_done: number
  bytes_new: number
  bytes_uploaded: number
  queue_packs: number
  warnings: number
  errors: number
  eta_ms: number
}

export type IssueSeverity = 'warning' | 'skipped' | 'error'
export type IssueResolution = 'open' | 'skipped' | 'skipped_all' | 'retried' | 'resolved'

export interface Issue {
  id: number
  run_id: number
  path: string
  class: string
  severity: IssueSeverity | (string & {})
  detail: string
  first_seen: number
  last_seen: number
  count: number
  retryable: boolean
  resolution: IssueResolution | (string & {})
  retry_run_id: number | null
}

/** POST /backup/runs/{id}/issues/skip: ids or a class, never both. */
export type IssueSkipRequest = { ids: number[] } | { class: string }

/** Subset of a `GET /devices` row used here. */
export interface DeviceLite {
  id: number
  name: string
  status: string
  install_mode?: string
}

/** Subset of a `GET /devices/{id}/profiles` row (api-v1 §6B). */
export interface DeviceProfileLite {
  id: number
  sid: string
  username: string
  state: string
}

export const ISSUE_CLASSES = [
  'locked',
  'access_denied',
  'not_found',
  'path_too_long',
  'io',
  'network',
  'server',
  'crypto',
  'cloud_placeholder',
  'vss',
  'other',
  'profile_unauthorised',
  'module_unavailable',
  'guard_blocked',
] as const

export const JOB_KINDS: readonly JobKind[] = ['folder', 'profile', 'app', 'game', 'devproject', 'registry', 'inventory']
export const JOB_MODES: readonly JobMode[] = ['full', 'data_only', 'reinstall_inventory']

export const kindLabels: Record<JobKind, string> = {
  folder: 'Folders',
  profile: 'User profile',
  app: 'Applications',
  game: 'Games',
  devproject: 'Dev projects',
  registry: 'Registry',
  inventory: 'Inventory',
}
export const modeLabels: Record<JobMode, string> = { full: 'Full', data_only: 'Data only', reinstall_inventory: 'Reinstall inventory' }

export const RUNNING_STATUSES: readonly string[] = ['queued', 'scanning', 'uploading', 'committing']
export const isRunning = (r: Pick<Run, 'status'>) => RUNNING_STATUSES.includes(r.status)

export function kindLabel(k: string): string {
  return kindLabels[k as JobKind] ?? k
}

/** Badge tone for a run status. */
export function runTone(s: string): 'green' | 'amber' | 'red' | 'blue' | 'slate' {
  if (s === 'completed') return 'green'
  if (s === 'partial' || s === 'cancelled') return 'amber'
  if (s === 'failed') return 'red'
  if (RUNNING_STATUSES.includes(s)) return 'blue'
  return 'slate'
}

/** JobProgress state for a run status. */
export function progressState(s: string): string {
  if (s === 'completed' || s === 'partial') return 'succeeded'
  if (s === 'failed' || s === 'cancelled') return s
  if (s === 'queued') return 'queued'
  return 'running'
}

export const emptySpec = (): JobSpec => ({ paths: [], exclude: [], ruleset_id: null, source_ref: '', use_vss: 'auto' })
export const emptySchedule = (): JobSchedule => ({ type: 'daily', expr: '', at: '02:00', weekdays: null, every_minutes: 60, timezone: 'Local', catch_up: 'run_once', jitter_minutes: 10 })
