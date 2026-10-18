import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { ArrowDown, ArrowUp, ChevronsUpDown, Inbox } from 'lucide-react'
import { Button, EmptyState, ErrorState, Spinner, cx } from '../ui'
import { errorMessage } from '../../lib/api'
import { nextSort, sortRows, type SortState, type SortValue } from './sort'
import { useRowSelection, type RowKey } from './useRowSelection'

export type Column<T> = {
  id: string
  header: ReactNode
  cell: (row: T) => ReactNode
  /** Client-side sort key. Its presence makes the column sortable. */
  sortValue?: (row: T) => SortValue
  /** Makes the column sortable without a sortValue (server-side sorting via `manualSort`). */
  sortable?: boolean
  align?: 'left' | 'center' | 'right'
  /** Tailwind width class, for example `w-32`. */
  width?: string
  className?: string
  headerClassName?: string
  /** Text for aria-label on the sort button when `header` is not plain text. */
  label?: string
}

/** Data and load-state props; `infiniteTableProps()` produces these from `useApiInfiniteList`. */
export type DataTableLoadProps<T> = {
  rows: readonly T[]
  loading?: boolean
  error?: unknown
  onRetry?: () => void
  /** Shows a "Load more" button for cursor lists. */
  hasMore?: boolean
  loadingMore?: boolean
  onLoadMore?: () => void
  /** Error while fetching a later page; rendered next to "Load more". */
  loadMoreError?: unknown
}

export type DataTableProps<T> = DataTableLoadProps<T> & {
  columns: Column<T>[]
  rowKey: (row: T) => RowKey
  /** Accessible name of the table. */
  label: string
  /** Empty state; defaults to a generic "Nothing here yet". */
  empty?: ReactNode
  /** Sorting. Uncontrolled unless `sort` is passed. */
  sort?: SortState
  defaultSort?: SortState
  onSortChange?: (sort: SortState) => void
  /** Rows are already sorted by the server; only the header state changes. */
  manualSort?: boolean
  /** Row selection with checkboxes. Uncontrolled unless `selected` is passed. */
  selectable?: boolean
  selected?: ReadonlySet<RowKey>
  onSelectedChange?: (next: Set<RowKey>) => void
  isRowSelectable?: (row: T) => boolean
  /** Rendered in a bar above the table while rows are selected. */
  bulkActions?: (selectedRows: T[], clear: () => void) => ReactNode
  /** Row activation (click, Enter or Space on a focused row). */
  onRowClick?: (row: T) => void
  rowClassName?: (row: T) => string | undefined
  stickyHeader?: boolean
  dense?: boolean
  className?: string
}

const alignCls = { left: 'text-left', center: 'text-center', right: 'text-right' }

export function DataTable<T>(props: DataTableProps<T>) {
  const { rows, columns, rowKey, label, loading, error, onRetry, empty, hasMore, loadingMore, onLoadMore, loadMoreError, selectable, isRowSelectable, bulkActions, onRowClick, rowClassName, stickyHeader, dense, className, manualSort } = props
  const id = useId()
  const [innerSort, setInnerSort] = useState<SortState>(props.defaultSort ?? null)
  const sort = props.sort !== undefined ? props.sort : innerSort
  const setSort = (s: SortState) => {
    if (props.sort === undefined) setInnerSort(s)
    props.onSortChange?.(s)
  }
  const sel = useRowSelection(props.selected, props.onSelectedChange)
  const bodyRef = useRef<HTMLTableSectionElement>(null)

  const sortCol = sort ? columns.find((c) => c.id === sort.id) : undefined
  const shown = useMemo(() => (manualSort ? rows : sortRows(rows, sort, sortCol?.sortValue)), [rows, sort, sortCol, manualSort])

  const selectableRows = useMemo(() => (selectable ? shown.filter((r) => !isRowSelectable || isRowSelectable(r)) : []), [shown, selectable, isRowSelectable])
  const selectedRows = useMemo(() => (selectable ? rows.filter((r) => sel.selected.has(rowKey(r))) : []), [rows, selectable, sel.selected, rowKey])
  const allSelected = selectableRows.length > 0 && selectableRows.every((r) => sel.selected.has(rowKey(r)))
  const someSelected = !allSelected && selectableRows.some((r) => sel.selected.has(rowKey(r)))

  const headCheckbox = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (headCheckbox.current) headCheckbox.current.indeterminate = someSelected
  }, [someSelected])

  const toggleAll = () => {
    const next = new Set(sel.selected)
    for (const r of selectableRows) {
      if (allSelected) next.delete(rowKey(r))
      else next.add(rowKey(r))
    }
    sel.set(next)
  }

  // Arrow keys move focus between rows; Enter/Space activates; Home/End jump.
  const onRowKey = (e: KeyboardEvent<HTMLTableRowElement>, row: T) => {
    if (e.target !== e.currentTarget) return
    const list = Array.from(bodyRef.current?.querySelectorAll<HTMLTableRowElement>('tr[data-row]') ?? [])
    const i = list.indexOf(e.currentTarget)
    let to: HTMLTableRowElement | undefined
    if (e.key === 'ArrowDown') to = list[i + 1]
    else if (e.key === 'ArrowUp') to = list[i - 1]
    else if (e.key === 'Home') to = list[0]
    else if (e.key === 'End') to = list[list.length - 1]
    else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      if (onRowClick) onRowClick(row)
      else if (selectable && (!isRowSelectable || isRowSelectable(row))) sel.toggle(rowKey(row))
      return
    } else return
    e.preventDefault()
    to?.focus()
  }

  const colSpan = columns.length + (selectable ? 1 : 0)
  const rowsInteractive = !!onRowClick || !!selectable
  const cellPad = dense ? 'px-3 py-1.5' : 'px-4 py-2.5'

  let body: ReactNode
  if (loading && rows.length === 0) {
    body = (
      <tr>
        <td colSpan={colSpan}>
          <div role="status" aria-live="polite" className="flex items-center justify-center gap-2 py-12 text-sm text-slate-500">
            <Spinner /> Loading
          </div>
        </td>
      </tr>
    )
  } else if (error) {
    body = (
      <tr>
        <td colSpan={colSpan} className="p-4">
          <ErrorState error={new Error(errorMessage(error))} onRetry={onRetry} />
        </td>
      </tr>
    )
  } else if (rows.length === 0) {
    body = (
      <tr>
        <td colSpan={colSpan}>{empty ?? <EmptyState icon={<Inbox className="size-6" />} title="Nothing here yet" />}</td>
      </tr>
    )
  } else {
    body = shown.map((row) => {
      const key = rowKey(row)
      const isSel = selectable && sel.selected.has(key)
      const canSel = selectable && (!isRowSelectable || isRowSelectable(row))
      return (
        <tr
          key={key}
          data-row=""
          tabIndex={rowsInteractive ? 0 : undefined}
          aria-selected={selectable ? !!isSel : undefined}
          onKeyDown={rowsInteractive ? (e) => onRowKey(e, row) : undefined}
          onClick={onRowClick ? () => onRowClick(row) : undefined}
          className={cx(
            'border-t border-slate-100 transition dark:border-slate-800',
            rowsInteractive && 'focus-visible:bg-brand-50 focus-visible:outline-none dark:focus-visible:bg-brand-500/10',
            onRowClick && 'cursor-pointer',
            isSel ? 'bg-brand-50/70 dark:bg-brand-500/10' : 'hover:bg-slate-50 dark:hover:bg-slate-800/40',
            rowClassName?.(row),
          )}
        >
          {selectable && (
            <td className={cx('w-10', cellPad)} onClick={(e) => e.stopPropagation()}>
              <input type="checkbox" className="size-4 accent-brand-600" tabIndex={onRowClick ? undefined : -1} checked={!!isSel} disabled={!canSel} onChange={() => sel.toggle(key)} aria-label={`Select row ${key}`} />
            </td>
          )}
          {columns.map((c) => (
            <td key={c.id} className={cx(cellPad, 'text-slate-700 dark:text-slate-200', alignCls[c.align ?? 'left'], c.className)}>
              {c.cell(row)}
            </td>
          ))}
        </tr>
      )
    })
  }

  return (
    <div className={cx('card overflow-hidden', className)}>
      {selectable && bulkActions && selectedRows.length > 0 && (
        <div role="toolbar" aria-label="Bulk actions" className="flex flex-wrap items-center gap-2 border-b border-slate-200 bg-brand-50/60 px-4 py-2 dark:border-slate-800 dark:bg-brand-500/10">
          <span className="text-sm font-medium text-slate-700 dark:text-slate-200" aria-live="polite">
            {selectedRows.length} selected
          </span>
          <div className="flex flex-1 flex-wrap items-center gap-2">{bulkActions(selectedRows, sel.clear)}</div>
          <Button size="sm" variant="ghost" onClick={sel.clear}>
            Clear selection
          </Button>
        </div>
      )}
      <div className={cx('scroll-thin overflow-auto', stickyHeader && 'max-h-[70vh]')}>
        <table aria-label={label} aria-busy={loading || loadingMore || undefined} aria-multiselectable={selectable || undefined} className="w-full border-collapse text-sm">
          <thead className={cx('bg-slate-50 text-xs font-semibold tracking-wide text-slate-500 uppercase dark:bg-slate-900/60 dark:text-slate-400', stickyHeader && 'sticky top-0 z-10')}>
            <tr>
              {selectable && (
                <th scope="col" className={cx('w-10', cellPad)}>
                  <input ref={headCheckbox} type="checkbox" className="size-4 accent-brand-600" checked={allSelected} disabled={selectableRows.length === 0} onChange={toggleAll} aria-label="Select all rows" />
                </th>
              )}
              {columns.map((c) => {
                const canSort = !!(c.sortValue || c.sortable)
                const active = sort?.id === c.id ? sort.dir : undefined
                return (
                  <th
                    key={c.id}
                    scope="col"
                    aria-sort={active === 'asc' ? 'ascending' : active === 'desc' ? 'descending' : canSort ? 'none' : undefined}
                    className={cx(cellPad, 'whitespace-nowrap', alignCls[c.align ?? 'left'], c.width, c.headerClassName)}
                  >
                    {canSort ? (
                      <button
                        type="button"
                        id={`${id}-sort-${c.id}`}
                        onClick={() => setSort(nextSort(sort, c.id))}
                        aria-label={c.label ? `Sort by ${c.label}` : undefined}
                        className={cx('inline-flex items-center gap-1 rounded uppercase hover:text-slate-800 focus-visible:ring-2 focus-visible:ring-brand-500/40 focus-visible:outline-none dark:hover:text-slate-100', active && 'text-slate-800 dark:text-slate-100')}
                      >
                        {c.header}
                        {active === 'asc' ? <ArrowUp aria-hidden="true" className="size-3.5" /> : active === 'desc' ? <ArrowDown aria-hidden="true" className="size-3.5" /> : <ChevronsUpDown aria-hidden="true" className="size-3.5 opacity-40" />}
                      </button>
                    ) : (
                      c.header
                    )}
                  </th>
                )
              })}
            </tr>
          </thead>
          <tbody ref={bodyRef}>{body}</tbody>
        </table>
      </div>
      {rows.length > 0 && !error && (hasMore || loadMoreError) ? (
        <div className="flex flex-col items-center gap-2 border-t border-slate-100 px-4 py-3 dark:border-slate-800">
          {loadMoreError ? (
            <p role="alert" className="text-xs text-rose-600">
              {errorMessage(loadMoreError)}
            </p>
          ) : null}
          {onLoadMore && (
            <Button size="sm" onClick={onLoadMore} loading={loadingMore}>
              {loadMoreError ? 'Try again' : 'Load more'}
            </Button>
          )}
        </div>
      ) : null}
    </div>
  )
}
