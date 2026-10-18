import { get, getOperation, listAll, post, withQuery, type OperationAccepted, type Page } from '../../lib/api'

/** Subset of wire.Snapshot (api-v1 §5). */
export type Snapshot = {
  id: number
  repo_id: number
  device_id: number
  hostname: string
  os_user: string
  committed_at: number
  stats: { file_count?: number; logical_bytes?: number }
}

/** wire.CatalogEntry. */
export type CatalogEntry = { path: string; name: string; type: string; size: number; modified_at: number }

/** Subset of wire.Restore. */
export type Restore = {
  id: number
  snapshot_id: number
  target_device_id: number
  status: string
  files_total: number
  files_done: number
  bytes_total: number
  bytes_done: number
  error?: string
  created_at: number
}

export type RestorePlan = {
  summary: { files: number; bytes: number; conflicts: number }
  mappings: { token: string; from: string; to: string }[]
  warnings: { code: string; message: string }[]
  guard: { path: string; reason: string }[]
}

export const listSnapshots = () => listAll<Snapshot>('/snapshots', { limit: 500 })
export const listTree = (id: number, path: string) => get<Page<CatalogEntry>>(withQuery(`/snapshots/${id}/tree`, { path, limit: 500 }))
export const listRestores = () => get<Page<Restore>>(withQuery('/restore', { limit: 100 }))
export const cancelRestore = (id: number) => post(`/restore/${id}/cancel`)

export const planRestore = (snapshotId: number, deviceId: number, paths: string[], conflictPolicy: string) =>
  post<PlanResponse<RestorePlan>>('/restore/plan', {
    snapshot_id: snapshotId,
    selection: paths.map((p) => ({ path: p, include: true })),
    target_device_id: deviceId,
    mapping: { auto: true, overrides: [] },
    conflict_policy: conflictPolicy,
  })

export const executeRestore = (planToken: string) => post<{ restore_id: number; approval_id: number }>('/restore/execute', { plan_token: planToken })

/** Starts a zip download and waits for its single-use link (valid 1 h). */
export async function downloadZip(snapshotId: number, paths: string[]): Promise<string> {
  const acc = await post<OperationAccepted>(`/snapshots/${snapshotId}/download`, { paths })
  for (;;) {
    const op = await getOperation<{ download_url: string }>(acc.operation_id)
    if (op.state === 'succeeded' && op.result) return op.result.download_url
    if (op.state === 'failed' || op.state === 'cancelled') {
      const e = op.error
      throw new Error(typeof e === 'string' ? e : (e?.message ?? 'The download could not be prepared.'))
    }
    await new Promise((r) => setTimeout(r, 1500))
  }
}

export type PlanResponse<P> = { plan: P; plan_token: string; plan_digest: string; expires_at: number }
