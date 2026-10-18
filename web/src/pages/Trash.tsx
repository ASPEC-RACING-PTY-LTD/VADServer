import { useQuery, useQueryClient } from '@tanstack/react-query'
import { RotateCcw, Trash2 } from 'lucide-react'
import { del, errorMessage, get, post } from '../lib/api'
import { formatBytes, formatDate, timeAgo } from '../lib/format'
import { Button, EmptyState, ErrorState, IconButton, PageHeader, PageLoader, useUI } from '../components/ui'
import { FileIcon } from '../components/FileIcon'

export default function TrashPage() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['trash'], queryFn: () => get('/trash') })
  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['trash'] })
    qc.invalidateQueries({ queryKey: ['files'] })
    qc.invalidateQueries({ queryKey: ['stats'] })
  }
  const restore = async (it: any) => {
    try {
      const res = await post(`/trash/${it.id}/restore`)
      toast('success', `Restored ${it.name}`, res.node?.path)
      refresh()
    } catch (e) {
      toast('error', 'Restore failed', errorMessage(e))
    }
  }
  const purge = async (it: any) => {
    if (!(await confirm({ title: `Permanently delete ${it.name}?`, message: 'This cannot be undone. The file data is removed from the storage device.', confirmLabel: 'Delete forever' }))) return
    try {
      await del(`/trash/${it.id}`)
      refresh()
    } catch (e) {
      toast('error', 'Delete failed', errorMessage(e))
    }
  }
  const empty = async () => {
    if (!(await confirm({ title: 'Empty trash?', message: `All ${data.items.length} item(s) will be permanently deleted. This cannot be undone.`, confirmLabel: 'Empty trash', requireText: 'delete' }))) return
    try {
      const res = await del('/trash')
      toast('success', `Permanently deleted ${res.purged} item(s)`)
      refresh()
    } catch (e) {
      toast('error', 'Could not empty trash', errorMessage(e))
    }
  }
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const items: any[] = data.items
  const total = items.reduce((a, i) => a + (i.total_size ?? 0), 0)
  return (
    <div>
      <PageHeader
        title="Trash"
        description={data.retention_days > 0 ? `Items are permanently deleted ${data.retention_days} days after being moved here. Replaced versions of synced files also land here.` : 'Items stay here until you delete them.'}
        icon={<Trash2 className="size-5" />}
        actions={
          items.length > 0 && (
            <Button variant="danger" icon={<Trash2 className="size-4" />} onClick={empty}>
              Empty trash ({formatBytes(total)})
            </Button>
          )
        }
      />
      <div className="card overflow-hidden">
        {items.length === 0 ? (
          <EmptyState icon={<Trash2 className="size-6" />} title="Trash is empty" description="Deleted files and replaced versions appear here." />
        ) : (
          items.map((it) => (
            <div key={it.id} className="flex items-center gap-3 border-b border-slate-100 px-4 py-3 last:border-0 dark:border-slate-800">
              <FileIcon mime={it.mime} isDir={it.is_dir} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{it.name}</div>
                <div className="truncate text-xs text-slate-500">
                  {it.original_path} · {it.is_dir ? `${it.item_count} items, ` : ''}
                  {formatBytes(it.total_size)} · deleted <span title={formatDate(it.deleted_at)}>{timeAgo(it.deleted_at)}</span>
                </div>
              </div>
              <Button size="sm" icon={<RotateCcw className="size-3.5" />} onClick={() => restore(it)}>
                Restore
              </Button>
              <IconButton label="Delete forever" onClick={() => purge(it)}>
                <Trash2 className="size-4 text-rose-500" />
              </IconButton>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
