export type SortDir = 'asc' | 'desc'
export type SortState = { id: string; dir: SortDir } | null
export type SortValue = string | number | boolean | null | undefined

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: 'base' })

/** Compares two sort values; null/undefined always sort last regardless of direction. */
export function compareValues(a: SortValue, b: SortValue, dir: SortDir): number {
  const an = a === null || a === undefined || a === ''
  const bn = b === null || b === undefined || b === ''
  if (an || bn) return an === bn ? 0 : an ? 1 : -1
  let r: number
  if (typeof a === 'number' && typeof b === 'number') r = a - b
  else if (typeof a === 'boolean' && typeof b === 'boolean') r = Number(a) - Number(b)
  else r = collator.compare(String(a), String(b))
  return dir === 'asc' ? r : -r
}

/** Stable client-side sort. Returns the input array unchanged when there is nothing to sort by. */
export function sortRows<T>(rows: readonly T[], sort: SortState, value: ((row: T) => SortValue) | undefined): readonly T[] {
  if (!sort || !value) return rows
  return rows
    .map((row, i) => ({ row, i, v: value(row) }))
    .sort((x, y) => compareValues(x.v, y.v, sort.dir) || x.i - y.i)
    .map((x) => x.row)
}

/** Cycles a column through asc -> desc -> unsorted. */
export function nextSort(current: SortState, id: string): SortState {
  if (!current || current.id !== id) return { id, dir: 'asc' }
  if (current.dir === 'asc') return { id, dir: 'desc' }
  return null
}
