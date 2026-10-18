// Hand-written mirror of the wire readiness types (sdk/wire/readiness.go, api-v1 §11 rev 1.0 + 1.1).

export type ReadinessStatus = 'ok' | 'warning' | 'critical' | 'unknown'

/** Known check ids. Servers may send others (e.g. the admin-only `server_key_custody`); render those generically. */
export type ReadinessCheckId =
  | 'recent_backup'
  | 'open_issues'
  | 'vss_available'
  | 'verified_recently'
  | 'test_restore_recent'
  | 'recovery_key_confirmed'
  | 'service_installed'
  | 'redundant_destination'
  | 'not_portable_items'
  | 'profile_authorised'
  | 'install_mode'
  | 'modules_healthy'
  | 'server_key_custody'

export interface ReadinessCheck {
  id: ReadinessCheckId | (string & {})
  status: ReadinessStatus | (string & {})
  message: string
  /** Server-relative web path (e.g. `/backups/jobs/3`), or empty. */
  fix_link: string
}

/** Timestamps are Unix ms, 0 when the event never happened. */
export interface ReadinessJob {
  job_id: number
  last_success_at: number
  open_issues: number
  vss_used: boolean
  verified_at: number
  test_restored_at: number
}

export interface ReadinessDevice {
  device_id: number
  checks: ReadinessCheck[] | null
  jobs: ReadinessJob[] | null
}

/** `GET /readiness`. Score is 0..100. */
export interface ReadinessReport {
  score: number
  status: ReadinessStatus | (string & {})
  devices: ReadinessDevice[] | null
}

/** Informational checks are shown but never affect status or counts (P1-21 scoring). */
export const INFORMATIONAL_CHECKS: ReadonlySet<string> = new Set(['install_mode'])

export const checkLabels: Record<ReadinessCheckId, string> = {
  recent_backup: 'Recent backup',
  open_issues: 'Open issues',
  vss_available: 'Snapshot support (VSS)',
  verified_recently: 'Verified recently',
  test_restore_recent: 'Test restore',
  recovery_key_confirmed: 'Recovery key confirmed',
  service_installed: 'Background service',
  redundant_destination: 'Second copy',
  not_portable_items: 'Non-portable items',
  profile_authorised: 'Profile authorised',
  install_mode: 'Install mode',
  modules_healthy: 'Modules healthy',
  server_key_custody: 'Server key custody',
}

/** Label for a check id; unknown ids are humanised (`some_check` becomes "Some check"). */
export function checkLabel(id: string): string {
  const l = checkLabels[id as ReadinessCheckId]
  if (l) return l
  const s = id.replace(/[_.-]+/g, ' ').trim()
  return s ? s[0].toUpperCase() + s.slice(1) : id
}

export const statusLabels: Record<ReadinessStatus, string> = { ok: 'Ready', warning: 'Warning', critical: 'Critical', unknown: 'Unknown' }

export function statusLabel(s: string): string {
  return statusLabels[s as ReadinessStatus] ?? s
}

export type BadgeTone = 'green' | 'amber' | 'red' | 'slate'

export function statusTone(s: string): BadgeTone {
  return s === 'ok' ? 'green' : s === 'warning' ? 'amber' : s === 'critical' ? 'red' : 'slate'
}

const severity: Record<string, number> = { critical: 3, warning: 2, ok: 1, unknown: 0 }

/** Higher is worse; unrecognised statuses rank with unknown. */
export function statusSeverity(s: string): number {
  return severity[s] ?? 0
}

/** Scored checks: informational and unknown ones are left out (as the server does). */
export function isScored(c: ReadinessCheck): boolean {
  return !INFORMATIONAL_CHECKS.has(c.id) && (c.status === 'ok' || c.status === 'warning' || c.status === 'critical')
}

/** A device's status: its worst scored check, or `unknown` when nothing could be scored. */
export function deviceStatus(d: ReadinessDevice): ReadinessStatus {
  let worst: ReadinessStatus = 'unknown'
  for (const c of d.checks ?? []) if (isScored(c) && statusSeverity(c.status) > statusSeverity(worst)) worst = c.status as ReadinessStatus
  return worst
}

/** Checks ordered worst first; informational ones last. Stable for equal severity. */
export function sortChecks(checks: readonly ReadinessCheck[] | null): ReadinessCheck[] {
  const rank = (c: ReadinessCheck) => (INFORMATIONAL_CHECKS.has(c.id) ? -1 : statusSeverity(c.status))
  return [...(checks ?? [])].sort((a, b) => rank(b) - rank(a))
}

export type StatusCounts = Record<ReadinessStatus, number>

/** Devices per status. */
export function countDevices(r: ReadinessReport | undefined): StatusCounts {
  const n: StatusCounts = { ok: 0, warning: 0, critical: 0, unknown: 0 }
  for (const d of r?.devices ?? []) n[deviceStatus(d)]++
  return n
}

/** Non-informational checks per status, over all devices. */
export function countChecks(r: ReadinessReport | undefined): StatusCounts {
  const n: StatusCounts = { ok: 0, warning: 0, critical: 0, unknown: 0 }
  for (const d of r?.devices ?? [])
    for (const c of d.checks ?? []) {
      if (INFORMATIONAL_CHECKS.has(c.id)) continue
      const s = c.status in n ? (c.status as ReadinessStatus) : 'unknown'
      n[s]++
    }
  return n
}

/** Only same-origin app paths are followed; anything else (absolute or protocol-relative URLs) is dropped. */
export function safeFixLink(link: string | undefined | null): string | null {
  if (!link || !link.startsWith('/') || link.startsWith('//') || link.startsWith('/\\')) return null
  return link
}
