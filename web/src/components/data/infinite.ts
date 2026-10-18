import type { DataTableLoadProps } from './DataTable'

/** The subset of `useApiInfiniteList` (lib/query.ts) that DataTable needs. */
export type InfiniteListLike<T> = {
  items: T[]
  isPending: boolean
  error: unknown
  refetch: () => unknown
  hasNextPage: boolean
  isFetchingNextPage: boolean
  fetchNextPage: () => unknown
}

/**
 * Maps a `useApiInfiniteList` result onto DataTable props:
 * `<DataTable {...infiniteTableProps(q)} columns={cols} rowKey={(r) => r.id} />`.
 * An error while a later page loads keeps the rows visible; only a first-page error shows the error state.
 */
export function infiniteTableProps<T>(q: InfiniteListLike<T>): DataTableLoadProps<T> {
  return {
    rows: q.items,
    loading: q.isPending,
    error: q.items.length === 0 ? q.error : null,
    loadMoreError: q.items.length > 0 ? q.error : null,
    onRetry: () => void q.refetch(),
    hasMore: q.hasNextPage,
    loadingMore: q.isFetchingNextPage,
    onLoadMore: () => void q.fetchNextPage(),
  }
}
