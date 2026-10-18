// React Query conventions for the VaultDock web app.
//
// Query keys: every feature defines a key factory rooted at a single string,
// e.g. `const jobKeys = createKeys('jobs')` -> `jobKeys.all` = ['jobs'],
// `jobKeys.list({state})` = ['jobs','list',{state}], `jobKeys.detail(7)` = ['jobs','detail',7].
// Invalidate `xKeys.all` to refresh everything for a feature.

import {
  useInfiniteQuery,
  useMutation,
  useQuery,
  useQueryClient,
  type InfiniteData,
  type QueryKey,
  type UseInfiniteQueryOptions,
  type UseMutationOptions,
  type UseQueryOptions,
} from '@tanstack/react-query'
import { ApiError, get, getPage, getOperation, isOperationFinished, type Operation, type OperationId, type Page, type QueryParams } from './api'

// --- Key factory ---

export function createKeys<const R extends string>(root: R) {
  const all = [root] as const
  return {
    all,
    lists: () => [root, 'list'] as const,
    list: <F extends object | undefined>(filter?: F) => [root, 'list', filter ?? {}] as const,
    details: () => [root, 'detail'] as const,
    detail: (id: string | number) => [root, 'detail', id] as const,
    /** Arbitrary sub-key under the root, e.g. `keys.sub('tree', id, path)`. */
    sub: <T extends readonly unknown[]>(...parts: T) => [root, ...parts] as const,
  }
}

/** Shared keys for cross-feature resources. */
export const queryKeys = {
  summary: ['summary'] as const,
  operations: createKeys('operations'),
}

// --- Retry policy honouring 503 + Retry-After ---

/** Retry network/5xx errors up to `max` times; never retry 4xx. */
export function shouldRetry(failureCount: number, err: unknown, max = 2): boolean {
  if (err instanceof ApiError && err.status < 500) return false
  return failureCount < max
}

/** Delay honouring `Retry-After` on capacity errors, else exponential backoff capped at 30 s. */
export function retryDelay(attempt: number, err: unknown): number {
  if (err instanceof ApiError && err.retryAfterMs !== undefined) return Math.min(err.retryAfterMs, 120_000)
  return Math.min(1000 * 2 ** attempt, 30_000)
}

// --- Queries ---

type ApiQueryOptions<T, K extends QueryKey> = Omit<UseQueryOptions<T, ApiError, T, K>, 'queryKey' | 'queryFn'>

/** `useQuery` with an ApiError-typed error. Pass a path string (GET) or a custom fetcher. */
export function useApiQuery<T, K extends QueryKey = QueryKey>(key: K, pathOrFn: string | (() => Promise<T>), options?: ApiQueryOptions<T, K>) {
  return useQuery<T, ApiError, T, K>({
    queryKey: key,
    queryFn: typeof pathOrFn === 'string' ? () => get<T>(pathOrFn) : pathOrFn,
    retryDelay,
    ...options,
  })
}

// --- Mutations ---

export type ApiMutationOptions<TData, TVars> = Omit<UseMutationOptions<TData, ApiError, TVars>, 'mutationFn'> & {
  /** Keys invalidated after success (prefix match). */
  invalidate?: QueryKey[]
  /** Called with a display message on error, e.g. `(m) => toast('error', 'Save failed', m)`. */
  onErrorMessage?: (message: string, err: ApiError) => void
}

/**
 * `useMutation` that invalidates `invalidate` keys on success and reports errors.
 * Field errors are available as `mutation.error?.fieldErrors`.
 */
export function useApiMutation<TData = unknown, TVars = void>(fn: (vars: TVars) => Promise<TData>, options: ApiMutationOptions<TData, TVars> = {}) {
  const qc = useQueryClient()
  const { invalidate, onErrorMessage, onSuccess, onError, ...rest } = options
  return useMutation<TData, ApiError, TVars>({
    ...rest,
    mutationFn: fn,
    onSuccess: async (...args) => {
      if (invalidate?.length) await Promise.all(invalidate.map((queryKey) => qc.invalidateQueries({ queryKey })))
      return onSuccess?.(...args)
    },
    onError: (...args) => {
      const err = args[0]
      onErrorMessage?.(err?.message ?? String(err), err)
      return onError?.(...args)
    },
  })
}

// --- Cursor lists ---

type ApiInfiniteOptions<T, K extends QueryKey> = Omit<
  UseInfiniteQueryOptions<Page<T>, ApiError, InfiniteData<Page<T>, string>, K, string>,
  'queryKey' | 'queryFn' | 'initialPageParam' | 'getNextPageParam'
>

/**
 * Infinite query over a `{items,next_cursor}` list. `items` flattens loaded pages.
 * Example: `useApiInfiniteList(jobKeys.list(f), '/jobs', { ...f, limit: 100 })`.
 */
export function useApiInfiniteList<T, K extends QueryKey = QueryKey>(key: K, path: string, params?: QueryParams, options?: ApiInfiniteOptions<T, K>) {
  const q = useInfiniteQuery<Page<T>, ApiError, InfiniteData<Page<T>, string>, K, string>({
    queryKey: key,
    queryFn: ({ pageParam }) => getPage<T>(path, { ...params, cursor: pageParam || undefined }),
    initialPageParam: '',
    getNextPageParam: (last) => last.next_cursor || undefined,
    retryDelay,
    ...options,
  })
  const items: T[] = q.data ? q.data.pages.flatMap((p) => p.items) : []
  return { ...q, items }
}

// --- Long operations ---

/** Polls `GET /operations/{id}` every `intervalMs` until it reaches a terminal state. */
export function useOperation<R = unknown>(id: OperationId | null | undefined, intervalMs = 1500) {
  return useApiQuery<Operation<R>>(queryKeys.operations.detail(id ?? ''), () => getOperation<R>(id!), {
    enabled: id !== null && id !== undefined && id !== '',
    refetchInterval: (q) => (isOperationFinished(q.state.data) ? false : intervalMs),
  })
}
