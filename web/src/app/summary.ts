import { useApiQuery, queryKeys } from '../lib/query'

/** `GET /summary` counters (api-v1 §3). */
export type Summary = {
  pending_devices: number
  pending_approvals: number
  running_jobs: number
  failed_runs_24h: number
  unread_notifications: number
  open_conflicts: number
  blacklisted_profiles: number
  readiness: string
}

export type SummaryCounter = Exclude<keyof Summary, 'readiness'>

/** Shared `/summary` query (key `queryKeys.summary`, so feature mutations can invalidate it). */
export function useSummary() {
  return useApiQuery<Summary>(queryKeys.summary, '/summary', { refetchInterval: 30_000 })
}

/** Nav badge hook factory: `nav: { ..., useBadge: summaryBadge('pending_approvals') }`. */
export function summaryBadge(field: SummaryCounter): () => number | undefined {
  return function useSummaryBadge() {
    const v = useSummary().data?.[field]
    return typeof v === 'number' ? v : undefined
  }
}
