import type { ReactNode } from 'react'
import { ArrowDownAZ, Check, Download, FolderInput, Grid2X2, List, Pencil, Search, Trash2, X } from 'lucide-react'
import { Button, cx, IconButton, Menu } from '../ui'
import type { Sort } from './types'

export type FilesView = 'list' | 'grid'

const sortLabel: Record<string, string> = { name: 'Name', size: 'Size', modified: 'Modified', type: 'Type' }

/** Bulk-action bar (when something is selected) or filter box, plus kind, sort and view controls. */
export function FilesToolbar({
  selectedCount,
  searchQ,
  filter,
  onFilter,
  kind,
  onKind,
  sort,
  onSort,
  view,
  onView,
  onDownload,
  onMove,
  onRename,
  onDelete,
  onClear,
  extra,
}: {
  selectedCount: number
  searchQ: string
  filter: string
  onFilter: (v: string) => void
  kind: string
  onKind: (v: string) => void
  sort: Sort
  onSort: (s: Sort) => void
  view: FilesView
  onView: (v: FilesView) => void
  onDownload: () => void
  onMove: () => void
  onRename: () => void
  onDelete: () => void
  onClear: () => void
  /** Feature actions shown next to the type filter (e.g. smart processing). */
  extra?: ReactNode
}) {
  return (
    <div className="card mb-4 flex flex-wrap items-center gap-2 p-2">
      {selectedCount > 0 ? (
        <div className="flex flex-1 flex-wrap items-center gap-2 px-1">
          <span className="text-sm font-medium text-slate-700 dark:text-slate-200">{selectedCount} selected</span>
          <Button size="sm" icon={<Download className="size-3.5" />} onClick={onDownload}>
            Download
          </Button>
          <Button size="sm" icon={<FolderInput className="size-3.5" />} onClick={onMove}>
            Move
          </Button>
          {selectedCount === 1 && (
            <Button size="sm" icon={<Pencil className="size-3.5" />} onClick={onRename}>
              Rename
            </Button>
          )}
          <Button size="sm" variant="ghost" className="text-rose-600" icon={<Trash2 className="size-3.5" />} onClick={onDelete}>
            Delete
          </Button>
          <IconButton label="Clear selection" onClick={onClear}>
            <X className="size-4" />
          </IconButton>
        </div>
      ) : (
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400" />
          <input className="input h-8 border-transparent bg-slate-100 pl-8 shadow-none dark:bg-slate-800" placeholder={searchQ ? 'Filter results' : 'Filter this folder'} value={filter} onChange={(e) => onFilter(e.target.value)} />
        </div>
      )}
      {extra}
      <select className="input h-8 w-auto py-0" value={kind} onChange={(e) => onKind(e.target.value)} aria-label="File type">
        <option value="">All types</option>
        <option value="folder">Folders</option>
        <option value="video">Videos</option>
        <option value="image">Images</option>
        <option value="audio">Audio</option>
        <option value="document">Documents</option>
        <option value="archive">Archives</option>
        <option value="other">Other</option>
      </select>
      <Menu
        trigger={(o) => (
          <Button size="sm" variant="ghost" icon={<ArrowDownAZ className="size-4" />} onClick={o}>
            {sortLabel[sort.by]} {sort.desc ? '↓' : '↑'}
          </Button>
        )}
        items={[
          ...['name', 'modified', 'size', 'type'].map((b) => ({ label: sortLabel[b], icon: sort.by === b ? <Check className="size-4" /> : undefined, onClick: () => onSort({ by: b, desc: sort.by === b ? !sort.desc : b !== 'name' }) })),
        ]}
      />
      <div className="flex rounded-lg bg-slate-100 p-0.5 dark:bg-slate-800">
        <button className={cx('rounded-md p-1.5', view === 'list' ? 'bg-white shadow-sm dark:bg-slate-900' : 'text-slate-500')} onClick={() => onView('list')} aria-label="List view">
          <List className="size-4" />
        </button>
        <button className={cx('rounded-md p-1.5', view === 'grid' ? 'bg-white shadow-sm dark:bg-slate-900' : 'text-slate-500')} onClick={() => onView('grid')} aria-label="Grid view">
          <Grid2X2 className="size-4" />
        </button>
      </div>
    </div>
  )
}
