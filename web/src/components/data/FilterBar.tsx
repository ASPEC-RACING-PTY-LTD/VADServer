import { useEffect, useId, useRef, useState, type ReactNode } from 'react'
import { Search, X } from 'lucide-react'
import { Button, cx } from '../ui'

export type FilterOption = { value: string; label: ReactNode; count?: number }
export type ChipFilter = {
  id: string
  label: string
  options: FilterOption[]
  /** Allow several values at once (default: single choice; clicking the active chip clears it). */
  multiple?: boolean
}
/** Active values per filter id. A missing or empty array means "no filter". */
export type FilterValues = Record<string, string[]>

export type FilterBarProps = {
  /** Committed (debounced) search text. Omit `onSearchChange` to hide the search box. */
  search?: string
  onSearchChange?: (q: string) => void
  searchPlaceholder?: string
  searchLabel?: string
  debounceMs?: number
  filters?: ChipFilter[]
  values?: FilterValues
  onValuesChange?: (next: FilterValues) => void
  /** Extra controls on the right (buttons, view toggles). */
  actions?: ReactNode
  className?: string
}

export function hasActiveFilters(values?: FilterValues, search?: string) {
  return !!search || Object.values(values ?? {}).some((v) => v.length > 0)
}

export function FilterBar({ search = '', onSearchChange, searchPlaceholder = 'Search…', searchLabel = 'Search', debounceMs = 300, filters = [], values = {}, onValuesChange, actions, className }: FilterBarProps) {
  const id = useId()
  const [text, setText] = useState(search)
  const lastEmitted = useRef(search)

  // Follow external changes (for example "Clear all" or URL navigation).
  useEffect(() => {
    if (search !== lastEmitted.current) {
      lastEmitted.current = search
      setText(search)
    }
  }, [search])

  useEffect(() => {
    if (!onSearchChange || text === lastEmitted.current) return
    const t = setTimeout(() => {
      lastEmitted.current = text
      onSearchChange(text)
    }, debounceMs)
    return () => clearTimeout(t)
  }, [text, debounceMs, onSearchChange])

  const commitNow = (q: string) => {
    setText(q)
    lastEmitted.current = q
    onSearchChange?.(q)
  }

  const toggle = (f: ChipFilter, v: string) => {
    const cur = values[f.id] ?? []
    const on = cur.includes(v)
    const next = f.multiple ? (on ? cur.filter((x) => x !== v) : [...cur, v]) : on ? [] : [v]
    onValuesChange?.({ ...values, [f.id]: next })
  }

  const active = hasActiveFilters(values, text)
  const clearAll = () => {
    if (text) commitNow('')
    onValuesChange?.(Object.fromEntries(filters.map((f) => [f.id, []])))
  }

  return (
    <div className={cx('flex flex-col gap-3', className)}>
      <div className="flex flex-wrap items-center gap-2">
        {onSearchChange && (
          <div className="relative min-w-48 flex-1">
            <Search aria-hidden="true" className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
            <input
              id={`${id}-q`}
              type="search"
              role="searchbox"
              aria-label={searchLabel}
              className="input pr-8 pl-9"
              placeholder={searchPlaceholder}
              value={text}
              onChange={(e) => setText(e.target.value)}
              onKeyDown={(e) => {
                if (e.key === 'Enter') commitNow(text)
                else if (e.key === 'Escape' && text) {
                  e.preventDefault()
                  commitNow('')
                }
              }}
            />
            {text && (
              <button type="button" aria-label="Clear search" onClick={() => commitNow('')} className="absolute top-1/2 right-2 -translate-y-1/2 rounded p-0.5 text-slate-400 hover:text-slate-700 dark:hover:text-slate-200">
                <X className="size-4" />
              </button>
            )}
          </div>
        )}
        {actions && <div className="flex flex-wrap items-center gap-2">{actions}</div>}
        {active && (filters.length > 0 || onSearchChange) && (
          <Button size="sm" variant="ghost" onClick={clearAll}>
            Clear filters
          </Button>
        )}
      </div>
      {filters.map((f) => (
        <div key={f.id} role="group" aria-labelledby={`${id}-${f.id}`} className="flex flex-wrap items-center gap-1.5">
          <span id={`${id}-${f.id}`} className="mr-1 text-xs font-semibold tracking-wide text-slate-500 uppercase dark:text-slate-400">
            {f.label}
          </span>
          {f.options.map((o) => {
            const on = (values[f.id] ?? []).includes(o.value)
            return (
              <button
                key={o.value}
                type="button"
                aria-pressed={on}
                onClick={() => toggle(f, o.value)}
                className={cx(
                  'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs font-medium transition focus-visible:ring-2 focus-visible:ring-brand-500/40 focus-visible:outline-none',
                  on ? 'border-brand-500 bg-brand-50 text-brand-800 dark:border-brand-400 dark:bg-brand-500/15 dark:text-brand-200' : 'border-slate-300 bg-white text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:bg-slate-900 dark:text-slate-300 dark:hover:bg-slate-800',
                )}
              >
                {o.label}
                {o.count !== undefined && <span className="text-[10px] text-slate-400">{o.count}</span>}
              </button>
            )
          })}
        </div>
      ))}
    </div>
  )
}
