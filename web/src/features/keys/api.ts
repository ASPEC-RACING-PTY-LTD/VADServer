import { ApiError, get } from '../../lib/api'
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
