import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight } from 'lucide-react'
import { get } from '../../lib/api'
import { Button, Modal, PageLoader } from '../ui'
import { FileIcon } from '../FileIcon'
import type { Crumb, Node } from './types'

export function MoveModal({ ids, onClose, onMove, title = 'Move to folder' }: { ids: number[] | null; onClose: () => void; onMove: (target: number) => void; title?: string }) {
  const [cur, setCur] = useState<string>('root')
  useEffect(() => {
    if (ids) setCur('root')
  }, [ids])
  const { data, isLoading } = useQuery({ queryKey: ['folder-picker', cur], queryFn: () => get(`/files?parent=${cur}&kind=folder&sort=name`), enabled: !!ids })
  const crumbs: Crumb[] = data?.breadcrumbs ?? []
  const target = data?.folder?.id
  return (
    <Modal
      open={!!ids}
      onClose={onClose}
      title={title}
      description={ids ? `${ids.length} item(s) selected` : undefined}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="primary"
            disabled={!target}
            onClick={() => {
              onMove(target)
              onClose()
            }}
          >
            Move here
          </Button>
        </>
      }
    >
      <div className="mb-3 flex flex-wrap items-center gap-1 text-sm">
        {crumbs.map((c, i) => (
          <span key={c.id} className="flex items-center gap-1">
            {i > 0 && <ChevronRight className="size-3.5 text-slate-400" />}
            <button className="rounded px-1 hover:bg-slate-100 dark:hover:bg-slate-800" onClick={() => setCur(i === 0 ? 'root' : String(c.id))}>
              {i === 0 ? 'My files' : c.name}
            </button>
          </span>
        ))}
      </div>
      <div className="scroll-thin max-h-80 min-h-40 overflow-y-auto rounded-xl border border-slate-200 dark:border-slate-800">
        {isLoading && <PageLoader />}
        {!isLoading && (data?.items ?? []).length === 0 && <div className="p-6 text-center text-sm text-slate-500">No subfolders here.</div>}
        {(data?.items ?? []).map((f: Node) => (
          <button key={f.id} disabled={ids?.includes(f.id)} onClick={() => setCur(String(f.id))} className="flex w-full items-center gap-3 border-b border-slate-100 px-3 py-2 text-left text-sm last:border-0 hover:bg-slate-50 disabled:opacity-40 dark:border-slate-800 dark:hover:bg-slate-800/50">
            <FileIcon mime="" isDir size="sm" />
            <span className="flex-1 truncate">{f.name}</span>
            <ChevronRight className="size-4 text-slate-400" />
          </button>
        ))}
      </div>
    </Modal>
  )
}
