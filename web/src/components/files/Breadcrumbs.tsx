import { Link } from 'react-router-dom'
import { ChevronRight, Home } from 'lucide-react'
import { cx } from '../ui'
import { NODES_MIME, type Crumb } from './types'

/** Folder breadcrumbs; each crumb is also a drop target for dragged nodes. */
export function Breadcrumbs({ crumbs, onMoveTo }: { crumbs: Crumb[]; onMoveTo: (ids: number[], target: number) => void }) {
  return (
    <nav className="flex flex-wrap items-center gap-1 text-sm text-slate-500">
      {crumbs.map((c, i) => (
        <span key={c.id} className="flex items-center gap-1">
          {i > 0 && <ChevronRight className="size-3.5 text-slate-400" />}
          <Link
            to={i === 0 ? '/files' : `/files/${c.id}`}
            className={cx('flex items-center gap-1 rounded px-1 py-0.5 hover:bg-slate-100 dark:hover:bg-slate-800', i === crumbs.length - 1 && 'font-medium text-slate-800 dark:text-slate-100')}
            onDragOver={(e) => e.dataTransfer.types.includes(NODES_MIME) && e.preventDefault()}
            onDrop={(e) => {
              const raw = e.dataTransfer.getData(NODES_MIME)
              if (raw) onMoveTo(JSON.parse(raw), c.id)
            }}
          >
            {i === 0 ? <Home className="size-3.5" /> : null}
            {i === 0 ? 'My files' : c.name}
          </Link>
        </span>
      ))}
    </nav>
  )
}
