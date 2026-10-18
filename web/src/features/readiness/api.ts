import { withQuery } from '../../lib/api'
import { createKeys, useApiQuery } from '../../lib/query'
import type { ReadinessReport } from './types'

export const readinessKeys = createKeys('readiness')

/** `device` takes precedence over `scope` server-side; `scope: 'all'` is admin only (403 otherwise). */
export type ReadinessFilter = { device?: number; scope?: 'mine' | 'all' }

export function readinessPath(filter: ReadinessFilter = {}): string {
  return withQuery('/readiness', { device: filter.device || undefined, scope: filter.scope === 'all' && !filter.device ? 'all' : undefined })
}

/** `GET /readiness?device=&scope=` (api-v1 §11). */
export function useReadiness(filter: ReadinessFilter = {}) {
  return useApiQuery<ReadinessReport>(readinessKeys.list(filter), readinessPath(filter), { refetchInterval: 60_000 })
}
