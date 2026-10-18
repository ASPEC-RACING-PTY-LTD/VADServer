import { ApiError, csrfHeader, get } from '../../lib/api'
import { createKeys, useApiQuery } from '../../lib/query'
import type { ServerIdentityResponse, ServerKeyStatus } from './types'

export const keyKeys = createKeys('keys')

/** GET /keys/server; `409 escrow_unavailable` (no server KEK) resolves to the unavailable state. */
export async function fetchServerKeyStatus(): Promise<ServerKeyStatus> {
  try {
    const r = await get<ServerIdentityResponse>('/keys/server')
    return { state: 'available', identity: r.server_identity }
  } catch (e) {
    if (e instanceof ApiError && e.status === 409 && e.code === 'escrow_unavailable') return { state: 'unavailable', message: e.message }
    throw e
  }
}

export function useServerKeyStatus() {
  return useApiQuery<ServerKeyStatus>(keyKeys.sub('server'), fetchServerKeyStatus, { retry: false, staleTime: 60_000 })
}

/** GET /admin/keys/kek (superusers): whether the one-time KEK download was used. Null for non-superusers. */
export type KEKExportStatus = { available: boolean; exported: boolean; exported_at?: number }

export function useKEKExportStatus() {
  return useApiQuery<KEKExportStatus | null>(
    keyKeys.sub('kek'),
    async () => {
      try {
        return await get<KEKExportStatus>('/admin/keys/kek')
      } catch (e) {
        if (e instanceof ApiError && (e.status === 403 || e.status === 401)) return null
        throw e
      }
    },
    { retry: false },
  )
}

/** POST /admin/keys/kek/export: downloads the server key file once (raw 32 bytes). */
export async function downloadKEK(): Promise<void> {
  const r = await fetch('/api/v1/admin/keys/kek/export', { method: 'POST', credentials: 'same-origin', headers: csrfHeader() })
  if (!r.ok) {
    let msg = 'The server key could not be downloaded.'
    try {
      msg = ((await r.json()) as { message?: string }).message ?? msg
    } catch {
      /* not JSON */
    }
    throw new Error(msg)
  }
  const url = URL.createObjectURL(await r.blob())
  const a = document.createElement('a')
  a.href = url
  a.download = 'vaultdock-server.kek'
  a.click()
  URL.revokeObjectURL(url)
}
