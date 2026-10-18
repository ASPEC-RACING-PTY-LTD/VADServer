import type { ReactNode } from 'react'
import { ArrowLeft } from 'lucide-react'
import { IconButton } from '../ui'
import { Breadcrumbs } from './Breadcrumbs'
import type { Crumb } from './types'

/** Page heading: search-result title, or breadcrumbs plus folder name; `actions` renders on the right. */
export function FilesHeader({
  searchQ,
  matchCount,
  crumbs,
  onBack,
  onMoveTo,
  actions,
}: {
  searchQ: string
  matchCount: number
  crumbs: Crumb[]
  onBack: () => void
  onMoveTo: (ids: number[], target: number) => void
  actions: ReactNode
}) {
  return (
    <div className="mb-5 flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
      <div className="min-w-0">
        {searchQ ? (
          <div className="flex items-center gap-3">
            <IconButton label="Back" onClick={onBack}>
              <ArrowLeft className="size-5" />
            </IconButton>
            <div>
              <h1 className="text-2xl font-semibold tracking-tight text-slate-900 dark:text-white">Search results</h1>
              <p className="text-sm text-slate-500">
                for “{searchQ}” · {matchCount} match{matchCount === 1 ? '' : 'es'}
              </p>
            </div>
          </div>
        ) : (
          <>
            <Breadcrumbs crumbs={crumbs} onMoveTo={onMoveTo} />
            <h1 className="mt-1 truncate text-2xl font-semibold tracking-tight text-slate-900 dark:text-white">{crumbs.length > 1 ? crumbs[crumbs.length - 1].name : 'My files'}</h1>
          </>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-2">{actions}</div>
    </div>
  )
}
