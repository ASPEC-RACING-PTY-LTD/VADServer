// Thin fetch wrapper for the VaultDock API with CSRF handling.
// Conventions follow api-v1 §1: `{"error":{code,message,details}}` errors,
// cursor lists `{items,next_cursor}`, 202 `{operation_id}` / `{approval_id}`,
// and 503 + Retry-After when the server is saturated.

export class ApiError extends Error {
  status: number
  code: string
  details?: unknown
  body?: any
  /** Milliseconds to wait before retrying, from `Retry-After` (503/429 only). */
  retryAfterMs?: number
  constructor(status: number, code: string, message: string, details?: unknown, body?: any, retryAfterMs?: number) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
    this.details = details
    this.body = body
    this.retryAfterMs = retryAfterMs
  }

  /** Per-field validation messages from `details.fields`, if present. */
  get fieldErrors(): Record<string, string> {
    const f = (this.details as { fields?: unknown } | undefined)?.fields
    if (!f || typeof f !== 'object') return {}
    const out: Record<string, string> = {}
    for (const [k, v] of Object.entries(f as Record<string, unknown>)) out[k] = String(v)
    return out
  }

  /** True for `503` capacity responses (`X-VD-Capacity: saturated`). */
  get isCapacity(): boolean {
    return this.status === 503
  }
}

let csrfToken = ''
export function setCsrf(token: string) {
  csrfToken = token
}

type Listener = () => void
const unauthListeners = new Set<Listener>()
export function onUnauthenticated(fn: Listener) {
  unauthListeners.add(fn)
  return () => {
    unauthListeners.delete(fn)
  }
}

/** Parses a `Retry-After` header (delta-seconds or HTTP date) into milliseconds. */
export function parseRetryAfter(v: string | null): number | undefined {
  if (!v) return undefined
  const s = Number(v)
  if (Number.isFinite(s)) return Math.max(0, s * 1000)
  const t = Date.parse(v)
  return isNaN(t) ? undefined : Math.max(0, t - Date.now())
}

export type ApiResponse<T> = { status: number; data: T; headers: Headers }

/** Low-level request returning status and headers alongside the parsed body. Throws ApiError on non-2xx. */
export async function apiRaw<T = any>(method: string, path: string, body?: unknown, init?: RequestInit): Promise<ApiResponse<T>> {
  const headers: Record<string, string> = {}
  if (body !== undefined) headers['Content-Type'] = 'application/json'
  if (method !== 'GET' && method !== 'HEAD' && csrfToken) headers['X-CSRF-Token'] = csrfToken
  const res = await fetch('/api/v1' + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: 'same-origin',
    ...init,
  })
  const text = await res.text()
  let data: any = null
  if (text) {
    try {
      data = JSON.parse(text)
    } catch {
      data = text
    }
  }
  if (!res.ok) {
    const e = data?.error ?? {}
    const retryAfter = res.status === 503 || res.status === 429 ? parseRetryAfter(res.headers.get('Retry-After')) : undefined
    const err = new ApiError(res.status, e.code ?? 'http_' + res.status, e.message ?? `Request failed (${res.status})`, e.details, data, retryAfter)
    if (res.status === 401 && !path.startsWith('/auth/login') && !path.startsWith('/setup')) {
      unauthListeners.forEach((fn) => fn())
    }
    throw err
  }
  return { status: res.status, data: data as T, headers: res.headers }
}

export async function api<T = any>(method: string, path: string, body?: unknown, init?: RequestInit): Promise<T> {
  return (await apiRaw<T>(method, path, body, init)).data
}

export const get = <T = any>(p: string) => api<T>('GET', p)
export const post = <T = any>(p: string, b?: unknown) => api<T>('POST', p, b ?? {})
export const put = <T = any>(p: string, b?: unknown) => api<T>('PUT', p, b ?? {})
export const patch = <T = any>(p: string, b?: unknown) => api<T>('PATCH', p, b ?? {})
export const del = <T = any>(p: string, b?: unknown) => api<T>('DELETE', p, b)

export function csrfHeader(): Record<string, string> {
  return csrfToken ? { 'X-CSRF-Token': csrfToken } : {}
}

export function errorMessage(e: unknown): string {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}

// --- Query strings ---

export type QueryValue = string | number | boolean | null | undefined | readonly (string | number)[]
export type QueryParams = Record<string, QueryValue>

/** Builds `a=1&b=x` from params, skipping null/undefined/'' values. Arrays repeat the key. */
export function qs(params: QueryParams | undefined): string {
  if (!params) return ''
  const u = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) {
    if (v === undefined || v === null || v === '') continue
    if (Array.isArray(v)) v.forEach((x) => u.append(k, String(x)))
    else u.append(k, String(v))
  }
  return u.toString()
}

/** Appends params to a path, respecting an existing `?`. */
export function withQuery(path: string, params?: QueryParams): string {
  const q = qs(params)
  if (!q) return path
  return path + (path.includes('?') ? '&' : '?') + q
}

// --- Cursor lists (api-v1 §1 Lists) ---

export type Page<T> = { items: T[]; next_cursor: string }
export type PageParams = QueryParams & { cursor?: string; limit?: number }

export const MAX_PAGE_LIMIT = 500

/** Fetches one page of a cursor list. */
export async function getPage<T>(path: string, params?: PageParams): Promise<Page<T>> {
  const p = await get<Partial<Page<T>> | null>(withQuery(path, params))
  return { items: p?.items ?? [], next_cursor: p?.next_cursor ?? '' }
}

/** Follows `next_cursor` until exhausted or `maxItems` collected. Use for small lists only. */
export async function listAll<T>(path: string, params?: QueryParams, maxItems = 10_000): Promise<T[]> {
  const out: T[] = []
  let cursor = ''
  do {
    const page = await getPage<T>(path, { ...params, cursor: cursor || undefined, limit: MAX_PAGE_LIMIT })
    out.push(...page.items)
    cursor = page.next_cursor
  } while (cursor && out.length < maxItems)
  return out.length > maxItems ? out.slice(0, maxItems) : out
}

// --- Bulk, operations and approvals (api-v1 §1, §3, §8) ---

export type BulkResult = { ok_count: number; failed: { id: string | number; code: string; message: string }[] }

export type OperationId = string | number
export type OperationAccepted = { operation_id: OperationId }
export type ApprovalAccepted = { approval_id: string | number; operation_id: OperationId | null }

export type OperationState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | (string & {})
export type Operation<R = unknown> = {
  id: OperationId
  kind: string
  state: OperationState
  progress?: { done?: number; total?: number; bytes?: number }
  result?: R
  error?: string | { code?: string; message?: string } | null
  created_at: number
  updated_at: number
}

/** Result of a write that may be accepted asynchronously or gated behind an approval. */
export type MutationResult<T> =
  | { kind: 'done'; status: number; data: T }
  | { kind: 'operation'; status: 202; operation_id: OperationId }
  | { kind: 'approval'; status: 202; approval_id: string | number; operation_id: OperationId | null }

export function isApprovalAccepted(x: unknown): x is ApprovalAccepted {
  return !!x && typeof x === 'object' && 'approval_id' in x && (x as ApprovalAccepted).approval_id != null
}

export function isOperationAccepted(x: unknown): x is OperationAccepted {
  return !!x && typeof x === 'object' && !isApprovalAccepted(x) && (x as OperationAccepted).operation_id != null
}

/** Sends a write and classifies a 202 as an operation or approval; any other 2xx is `done`. */
export async function mutate<T = unknown>(method: 'POST' | 'PUT' | 'PATCH' | 'DELETE', path: string, body?: unknown): Promise<MutationResult<T>> {
  const res = await apiRaw<any>(method, path, method === 'DELETE' ? body : (body ?? {}))
  if (res.status === 202) {
    if (isApprovalAccepted(res.data)) return { kind: 'approval', status: 202, approval_id: res.data.approval_id, operation_id: res.data.operation_id ?? null }
    if (isOperationAccepted(res.data)) return { kind: 'operation', status: 202, operation_id: res.data.operation_id }
  }
  return { kind: 'done', status: res.status, data: res.data as T }
}

export const getOperation = <R = unknown>(id: OperationId) => get<Operation<R>>(`/operations/${encodeURIComponent(String(id))}`)

export function isOperationFinished(op: Pick<Operation, 'state'> | undefined): boolean {
  return !!op && (op.state === 'succeeded' || op.state === 'failed' || op.state === 'cancelled')
}
