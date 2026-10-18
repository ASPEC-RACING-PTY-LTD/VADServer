import type { MouseEvent as RMouseEvent } from 'react'
import { MoreHorizontal } from 'lucide-react'
import { formatBytes, formatDate, timeAgo } from '../../lib/format'
import { Badge, cx, IconButton } from '../ui'
import { FileIcon } from '../FileIcon'
import { NODES_MIME, type Node } from './types'

export type FileViewProps = {
  items: Node[]
  selected: Set<number>
  onRowClick: (e: RMouseEvent, n: Node, idx: number) => void
  onToggle: (id: number) => void
  onOpen: (n: Node) => void
  /** Right-click on an item at viewport position (x, y). */
  onContextMenu: (n: Node, x: number, y: number) => void
  /** The item's "Actions" button was clicked; (x, y) is where to anchor the menu. */
  onActions: (n: Node, x: number, y: number) => void
}

/** List view: selectable rows that can be dragged onto folders. */
export function FileTable({
  items,
  selected,
  onRowClick,
  onToggle,
  onOpen,
  onContextMenu,
  onActions,
  onSelectAll,
  onMoveTo,
  showPath,
  total,
  filtered,
}: FileViewProps & {
  onSelectAll: (all: boolean) => void
  onMoveTo: (ids: number[], target: number) => void
  /** Show each item's path under its name (search results). */
  showPath: boolean
  total: any
  filtered: boolean
}) {
  return (
    <div className="card overflow-hidden">
      <table className="w-full table-fixed text-sm">
        <thead className="border-b border-slate-200 bg-slate-50/80 text-left text-xs font-semibold tracking-wide text-slate-500 uppercase dark:border-slate-800 dark:bg-slate-900/80">
          <tr>
            <th className="w-10 px-3 py-2.5">
              <input type="checkbox" className="size-4 rounded accent-brand-600" checked={selected.size === items.length && items.length > 0} onChange={(e) => onSelectAll(e.target.checked)} aria-label="Select all" />
            </th>
            <th className="px-2 py-2.5">Name</th>
            <th className="hidden w-40 px-3 py-2.5 md:table-cell">Modified</th>
            <th className="hidden w-28 px-3 py-2.5 text-right sm:table-cell">Size</th>
            <th className="w-12" />
          </tr>
        </thead>
        <tbody>
          {items.map((n, idx) => (
            <tr
              key={n.id}
              draggable
              onDragStart={(e) => {
                const ids = selected.has(n.id) ? [...selected] : [n.id]
                e.dataTransfer.setData(NODES_MIME, JSON.stringify(ids))
                e.dataTransfer.effectAllowed = 'move'
              }}
              onDragOver={(e) => n.is_dir && e.dataTransfer.types.includes(NODES_MIME) && e.preventDefault()}
              onDrop={(e) => {
                const raw = e.dataTransfer.getData(NODES_MIME)
                if (raw && n.is_dir) {
                  e.stopPropagation()
                  const ids: number[] = JSON.parse(raw).filter((x: number) => x !== n.id)
                  if (ids.length) onMoveTo(ids, n.id)
                }
              }}
              onClick={(e) => onRowClick(e, n, idx)}
              onDoubleClick={() => onOpen(n)}
              onContextMenu={(e) => {
                e.preventDefault()
                onContextMenu(n, e.clientX, e.clientY)
              }}
              className={cx('group cursor-default border-b border-slate-100 last:border-0 dark:border-slate-800/70', selected.has(n.id) ? 'bg-brand-50 dark:bg-brand-500/10' : 'hover:bg-slate-50 dark:hover:bg-slate-800/40')}
            >
              <td className="px-3 py-2" onClick={(e) => e.stopPropagation()}>
                <input type="checkbox" className="size-4 rounded accent-brand-600" checked={selected.has(n.id)} onChange={() => onToggle(n.id)} aria-label={`Select ${n.name}`} />
              </td>
              <td className="max-w-0 px-2 py-2">
                <div className="flex items-center gap-3">
                  <FileIcon mime={n.mime} isDir={n.is_dir} size="sm" />
                  <div className="min-w-0">
                    <button className="block max-w-full truncate text-left font-medium text-slate-800 hover:text-brand-600 hover:underline dark:text-slate-100" onClick={(e) => (e.stopPropagation(), onOpen(n))}>
                      {n.name}
                    </button>
                    <div className="flex items-center gap-1.5 truncate text-xs text-slate-500">
                      {showPath && n.path ? <span className="truncate">{n.path}</span> : n.is_dir ? <span>{n.child_count ?? 0} items</span> : <span className="sm:hidden">{formatBytes(n.size)}</span>}
                      {n.tags.slice(0, 3).map((t) => (
                        <Badge key={t} tone="violet" className="px-1.5 py-0 text-[10px]">
                          {t}
                        </Badge>
                      ))}
                    </div>
                  </div>
                </div>
              </td>
              <td className="hidden px-3 py-2 whitespace-nowrap text-slate-500 md:table-cell" title={formatDate(n.modified_at)}>
                {timeAgo(n.modified_at)}
              </td>
              <td className="hidden px-3 py-2 text-right whitespace-nowrap text-slate-500 tabular-nums sm:table-cell">{n.is_dir ? '-' : formatBytes(n.size)}</td>
              <td className="px-2 py-2 text-right" onClick={(e) => e.stopPropagation()}>
                <IconButton
                  label="Actions"
                  className="opacity-60 group-hover:opacity-100"
                  onClick={(e) => {
                    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
                    onActions(n, r.right, r.bottom + 4)
                  }}
                >
                  <MoreHorizontal className="size-4" />
                </IconButton>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      {total > items.length && !filtered && <div className="border-t border-slate-200 px-4 py-2 text-xs text-slate-500 dark:border-slate-800">Showing {items.length} of {total} items.</div>}
    </div>
  )
}
