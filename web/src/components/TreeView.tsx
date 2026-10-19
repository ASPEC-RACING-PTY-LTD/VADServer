import { useCallback, useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { ChevronRight, Loader2 } from 'lucide-react'
import { cx } from './ui'
import { errorMessage } from '../lib/api'

export type TreeNode<T = unknown> = {
  /** Unique across the whole tree (e.g. the full path). */
  id: string
  label: ReactNode
  /** Plain-text name for screen readers when `label` is not a string. */
  textLabel?: string
  /** True when the node can be expanded; children are fetched lazily via onLoadChildren. */
  hasChildren?: boolean
  icon?: ReactNode
  disabled?: boolean
  data?: T
}

export type CheckState = boolean | 'mixed'

export type TreeViewProps<T = unknown> = {
  /** Accessible name of the tree. */
  ariaLabel: string
  /** Top-level nodes. When omitted they are loaded with onLoadChildren(null) on mount. */
  roots?: TreeNode<T>[]
  /** Loads the children of a node (or the roots for null). Called once per node; errors can be retried. */
  onLoadChildren: (node: TreeNode<T> | null) => Promise<TreeNode<T>[]>
  /** Show tri-state checkboxes. */
  checkable?: boolean
  /**
   * Checked ids, normalised to the minimal set of subtree roots: an id means "this node and everything below it",
   * including children that were never loaded. Controlled when provided.
   */
  checked?: string[]
  onCheckedChange?: (ids: string[]) => void
  /** Single selection (highlight). */
  selectedId?: string | null
  onSelect?: (node: TreeNode<T>) => void
  /** Enter or double click. Defaults to expand/collapse. */
  onActivate?: (node: TreeNode<T>) => void
  /** Ids expanded initially (their children load on mount). */
  defaultExpanded?: string[]
  emptyMessage?: ReactNode
  className?: string
}

const ROOT = '\u0000root'

type Row<T> = { node: TreeNode<T>; level: number; setSize: number; posInSet: number }

/**
 * Lazy-loading ARIA tree (flat rendering with aria-level/setsize/posinset) with roving focus,
 * APG keyboard support and optional tri-state checkbox selection. Remount with a new `key` to reset its cache.
 */
export function TreeView<T = unknown>({ ariaLabel, roots, onLoadChildren, checkable, checked, onCheckedChange, selectedId, onSelect, onActivate, defaultExpanded, emptyMessage = 'Nothing here', className }: TreeViewProps<T>) {
  const nodes = useRef(new Map<string, TreeNode<T>>())
  const parent = useRef(new Map<string, string>())
  const children = useRef(new Map<string, string[]>())
  const [loading, setLoading] = useState<ReadonlySet<string>>(new Set())
  const [errors, setErrors] = useState<ReadonlyMap<string, string>>(new Map())
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(() => new Set(defaultExpanded))
  const [version, setVersion] = useState(0)
  const [focusId, setFocusIdState] = useState<string | null>(null)
  // The latest focused row, readable by effects queued before it changed.
  const focusIdRef = useRef<string | null>(null)
  const setFocusId = useCallback((id: string | null) => {
    focusIdRef.current = id
    setFocusIdState(id)
  }, [])
  const [innerChecked, setInnerChecked] = useState<string[]>([])
  const checkedIds = checked ?? innerChecked
  const rowRefs = useRef(new Map<string, HTMLDivElement>())
  const treeRef = useRef<HTMLDivElement>(null)
  const inflight = useRef(new Set<string>())
  const loaderRef = useRef(onLoadChildren)
  useEffect(() => {
    loaderRef.current = onLoadChildren
  })

  const store = useCallback((key: string, list: TreeNode<T>[]) => {
    for (const n of list) {
      nodes.current.set(n.id, n)
      parent.current.set(n.id, key)
    }
    children.current.set(
      key,
      list.map((n) => n.id),
    )
    setVersion((v) => v + 1)
  }, [])

  const load = useCallback(
    async (key: string) => {
      if (inflight.current.has(key) || children.current.has(key)) return
      inflight.current.add(key)
      setLoading((s) => new Set(s).add(key))
      setErrors((m) => {
        if (!m.has(key)) return m
        const n = new Map(m)
        n.delete(key)
        return n
      })
      try {
        const list = await loaderRef.current(key === ROOT ? null : (nodes.current.get(key) ?? null))
        store(key, list)
      } catch (e) {
        setErrors((m) => new Map(m).set(key, errorMessage(e)))
        if (key !== ROOT) setExpanded((s) => withOut(s, key))
      } finally {
        inflight.current.delete(key)
        setLoading((s) => withOut(s, key))
      }
    },
    [store],
  )

  // Roots: from props, or loaded lazily.
  useEffect(() => {
    if (roots) store(ROOT, roots)
    else void load(ROOT)
  }, [roots, store, load])

  // Load children of every expanded node that has not been loaded yet (covers defaultExpanded and expand()).
  useEffect(() => {
    for (const id of expanded) if (nodes.current.has(id) && !children.current.has(id)) void load(id)
  }, [expanded, version, load])

  const rows = useMemo(() => {
    const out: Row<T>[] = []
    const walk = (key: string, level: number) => {
      const ids = children.current.get(key) ?? []
      ids.forEach((id, i) => {
        const node = nodes.current.get(id)
        if (!node) return
        out.push({ node, level, setSize: ids.length, posInSet: i + 1 })
        if (node.hasChildren && expanded.has(id)) walk(id, level + 1)
      })
    }
    walk(ROOT, 1)
    return out
    // version tracks mutations of the ref maps.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded, version])

  const ancestors = useCallback((id: string) => {
    const out: string[] = []
    let p = parent.current.get(id)
    while (p !== undefined && p !== ROOT) {
      out.push(p)
      p = parent.current.get(p)
    }
    return out
  }, [])

  const checkedSet = useMemo(() => new Set(checkedIds), [checkedIds])
  const mixed = useMemo(() => {
    const s = new Set<string>()
    for (const id of checkedIds) for (const a of ancestors(id)) s.add(a)
    return s
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [checkedIds, ancestors, version])

  const checkState = (id: string): CheckState => {
    if (checkedSet.has(id) || ancestors(id).some((a) => checkedSet.has(a))) return true
    return mixed.has(id) ? 'mixed' : false
  }

  const setChecked = (ids: string[]) => {
    if (checked === undefined) setInnerChecked(ids)
    onCheckedChange?.(ids)
  }

  const toggleCheck = (id: string) => {
    const set = new Set(checkedIds)
    if (checkState(id) === true) {
      if (set.has(id)) set.delete(id)
      else {
        // Checked through an ancestor: split the ancestor into its siblings along the path.
        const path = [id, ...ancestors(id)]
        const top = path.findIndex((a) => set.has(a))
        set.delete(path[top])
        for (let i = top; i > 0; i--) for (const c of children.current.get(path[i]) ?? []) if (c !== path[i - 1]) set.add(c)
      }
    } else {
      for (const s of [...set]) if (ancestors(s).includes(id)) set.delete(s)
      set.add(id)
      // Collapse fully checked sibling groups into their parent.
      let p = parent.current.get(id)
      while (p !== undefined && p !== ROOT) {
        const sib = children.current.get(p) ?? []
        if (sib.length === 0 || !sib.every((c) => set.has(c))) break
        for (const c of sib) set.delete(c)
        set.add(p)
        p = parent.current.get(p)
      }
    }
    setChecked([...set])
  }

  const expand = (id: string) => setExpanded((s) => new Set(s).add(id))
  const collapse = (id: string) => setExpanded((s) => withOut(s, id))
  const toggleExpand = (n: TreeNode<T>) => (expanded.has(n.id) ? collapse(n.id) : expand(n.id))

  const current = rows.find((r) => r.node.id === focusId) ?? rows.find((r) => r.node.id === selectedId) ?? rows[0]
  const focusRow = (id: string | undefined) => {
    if (!id) return
    setFocusId(id)
    rowRefs.current.get(id)?.focus()
  }

  // Keep DOM focus on the focused row when the list re-renders under keyboard use.
  // It reads the ref, not the rendered focusId: an effect queued before a
  // keypress moved focus must not pull focus back to the previous row.
  useEffect(() => {
    const id = focusIdRef.current
    const el = id ? rowRefs.current.get(id) : undefined
    if (el && el !== document.activeElement && treeRef.current?.contains(document.activeElement)) el.focus()
  }, [focusId, rows])

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    // The event target is authoritative (focus state may not have flushed yet).
    const targetId = (e.target as HTMLElement).closest<HTMLElement>('[role="treeitem"]')?.dataset.id
    const row = rows.find((r) => r.node.id === targetId) ?? current
    if (!row) return
    const i = rows.indexOf(row)
    const n = row.node
    const isOpen = !!n.hasChildren && expanded.has(n.id)
    switch (e.key) {
      case 'ArrowDown':
        focusRow(rows[i + 1]?.node.id)
        break
      case 'ArrowUp':
        focusRow(rows[i - 1]?.node.id)
        break
      case 'Home':
        focusRow(rows[0]?.node.id)
        break
      case 'End':
        focusRow(rows[rows.length - 1]?.node.id)
        break
      case 'ArrowRight':
        if (n.hasChildren && !isOpen) expand(n.id)
        else if (isOpen) focusRow(children.current.get(n.id)?.[0])
        break
      case 'ArrowLeft': {
        if (isOpen) collapse(n.id)
        else {
          const p = parent.current.get(n.id)
          if (p && p !== ROOT) focusRow(p)
        }
        break
      }
      case 'Enter':
        if (onActivate) onActivate(n)
        else if (n.hasChildren) toggleExpand(n)
        break
      case ' ':
        if (n.disabled) break
        if (checkable) toggleCheck(n.id)
        else onSelect?.(n)
        break
      default:
        return
    }
    e.preventDefault()
  }

  const rootError = errors.get(ROOT)
  if (rows.length === 0) {
    return (
      <div className={cx('px-3 py-4 text-sm text-slate-500 dark:text-slate-400', className)} aria-busy={loading.has(ROOT) || undefined}>
        {loading.has(ROOT) ? (
          <span className="flex items-center gap-2">
            <Loader2 className="size-4 animate-spin" aria-hidden /> Loading…
          </span>
        ) : rootError ? (
          <span role="alert" className="text-rose-600">
            {rootError}{' '}
            <button type="button" className="underline" onClick={() => void load(ROOT)}>
              Retry
            </button>
          </span>
        ) : (
          emptyMessage
        )}
      </div>
    )
  }

  return (
    <div ref={treeRef} role="tree" aria-label={ariaLabel} aria-multiselectable={checkable || undefined} onKeyDown={onKeyDown} className={cx('text-sm', className)}>
      {rows.map(({ node, level, setSize, posInSet }) => {
        const isOpen = !!node.hasChildren && expanded.has(node.id)
        const busy = loading.has(node.id)
        const err = errors.get(node.id)
        const cs = checkable ? checkState(node.id) : undefined
        const selected = selectedId === node.id
        return (
          <div
            key={node.id}
            ref={(el) => {
              if (el) rowRefs.current.set(node.id, el)
              else rowRefs.current.delete(node.id)
            }}
            role="treeitem"
            data-id={node.id}
            aria-level={level}
            aria-setsize={setSize}
            aria-posinset={posInSet}
            aria-expanded={node.hasChildren ? isOpen : undefined}
            aria-checked={checkable ? cs : undefined}
            aria-selected={onSelect && !checkable ? selected : undefined}
            aria-disabled={node.disabled || undefined}
            aria-busy={busy || undefined}
            aria-label={node.textLabel}
            tabIndex={current?.node.id === node.id ? 0 : -1}
            onFocus={() => setFocusId(node.id)}
            onClick={() => {
              setFocusId(node.id)
              if (!node.disabled) onSelect?.(node)
            }}
            onDoubleClick={() => (onActivate ? onActivate(node) : node.hasChildren && toggleExpand(node))}
            className={cx('flex cursor-default items-center gap-1.5 rounded-md py-1 pr-2 select-none hover:bg-slate-100 dark:hover:bg-slate-800', selected && 'bg-brand-50 dark:bg-brand-900/30', node.disabled && 'opacity-50')}
            style={{ paddingLeft: `${(level - 1) * 1.25 + 0.25}rem` }}
          >
            <span
              aria-hidden
              className={cx('flex size-5 shrink-0 items-center justify-center text-slate-400', node.hasChildren && 'cursor-pointer')}
              onClick={(e) => {
                if (!node.hasChildren) return
                e.stopPropagation()
                setFocusId(node.id)
                toggleExpand(node)
              }}
            >
              {busy ? <Loader2 className="size-3.5 animate-spin" /> : node.hasChildren ? <ChevronRight className={cx('size-4 transition-transform', isOpen && 'rotate-90')} /> : null}
            </span>
            {checkable && (
              <input
                type="checkbox"
                tabIndex={-1}
                aria-hidden
                className="size-4 shrink-0 accent-brand-600"
                checked={cs === true}
                disabled={node.disabled}
                ref={(el) => {
                  if (el) el.indeterminate = cs === 'mixed'
                }}
                onClick={(e) => e.stopPropagation()}
                onChange={() => toggleCheck(node.id)}
              />
            )}
            {node.icon && <span className="shrink-0" aria-hidden>{node.icon}</span>}
            <span className="min-w-0 truncate">{node.label}</span>
            {err && (
              <span className="ml-2 shrink-0 text-xs text-rose-600">
                {err}{' '}
                <button
                  type="button"
                  tabIndex={-1}
                  className="underline"
                  onClick={(e) => {
                    e.stopPropagation()
                    expand(node.id)
                  }}
                >
                  Retry
                </button>
              </span>
            )}
          </div>
        )
      })}
    </div>
  )
}

function withOut<V>(s: ReadonlySet<V>, v: V): ReadonlySet<V> {
  if (!s.has(v)) return s
  const n = new Set(s)
  n.delete(v)
  return n
}
