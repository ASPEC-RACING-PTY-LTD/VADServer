import { useState } from 'react'
import { useInfiniteQuery } from '@tanstack/react-query'
import { Activity as ActivityIcon, Download, FolderPlus, LogIn, Monitor, Pencil, RotateCcw, Shield, Trash2, Upload, Workflow } from 'lucide-react'
import { get } from '../lib/api'
import { formatDate, timeAgo } from '../lib/format'
import { Button, EmptyState, ErrorState, PageHeader, PageLoader, Tabs } from '../components/ui'
import { TransfersTable } from '../components/TransfersTable'
import { describe } from '../lib/activity'

const icons: Record<string, React.ReactNode> = {
  'file.uploaded': <Upload className="size-4" />,
  'file.downloaded': <Download className="size-4" />,
  'file.trashed': <Trash2 className="size-4" />,
  'file.purged': <Trash2 className="size-4" />,
  'trash.emptied': <Trash2 className="size-4" />,
  'file.restored': <RotateCcw className="size-4" />,
  'file.renamed': <Pencil className="size-4" />,
  'file.moved': <Pencil className="size-4" />,
  'folder.created': <FolderPlus className="size-4" />,
  'auth.login': <LogIn className="size-4" />,
  'automation.saved': <Workflow className="size-4" />,
  'automation.deleted': <Workflow className="size-4" />,
}

/** Re-exported for existing importers (admin console); new code imports from lib/activity. */
export { describe }

export default function ActivityPage() {
  const [tab, setTab] = useState<'activity' | 'transfers'>('activity')
  const q = useInfiniteQuery({
    queryKey: ['activity'],
    queryFn: ({ pageParam }) => get(`/activity?limit=50${pageParam ? `&before=${pageParam}` : ''}`),
    initialPageParam: 0,
    getNextPageParam: (last: any) => (last.items.length === 50 ? last.items[last.items.length - 1].id : undefined),
    enabled: tab === 'activity',
  })
  const items = q.data?.pages.flatMap((p: any) => p.items) ?? []
  return (
    <div>
      <PageHeader title="Activity" description="Everything that happened in your account, and every file transfer." icon={<ActivityIcon className="size-5" />} actions={<Tabs value={tab} onChange={setTab} tabs={[{ id: 'activity', label: 'Activity log' }, { id: 'transfers', label: 'Transfer history' }]} />} />
      {tab === 'transfers' ? (
        <div className="card overflow-hidden">
          <TransfersTable status="" />
        </div>
      ) : q.isLoading ? (
        <PageLoader />
      ) : q.error ? (
        <ErrorState error={q.error} onRetry={() => q.refetch()} />
      ) : items.length === 0 ? (
        <div className="card">
          <EmptyState icon={<ActivityIcon className="size-6" />} title="No activity yet" />
        </div>
      ) : (
        <div className="card p-2">
          {items.map((a: any) => (
            <div key={a.id} className="flex gap-3 rounded-xl px-3 py-2.5 hover:bg-slate-50 dark:hover:bg-slate-800/40">
              <div className={`mt-0.5 flex size-8 shrink-0 items-center justify-center rounded-lg ${a.action.startsWith('auth.failed') ? 'bg-rose-100 text-rose-600 dark:bg-rose-500/15' : 'bg-slate-100 text-slate-500 dark:bg-slate-800'}`}>{icons[a.action] ?? (a.action.startsWith('device') ? <Monitor className="size-4" /> : <Shield className="size-4" />)}</div>
              <div className="min-w-0 flex-1">
                <div className="text-sm">
                  <span className="font-medium">{describe(a)}</span> {a.target && <span className="break-all text-slate-600 dark:text-slate-300">{a.target}</span>}
                </div>
                <div className="mt-0.5 text-xs text-slate-500">
                  {a.details && <span>{a.details} · </span>}
                  {a.device_name ? `via ${a.device_name} · ` : ''}
                  {a.ip && `${a.ip} · `}
                  <span title={formatDate(a.created_at)}>{timeAgo(a.created_at)}</span>
                </div>
              </div>
            </div>
          ))}
          {q.hasNextPage && (
            <div className="p-3 text-center">
              <Button loading={q.isFetchingNextPage} onClick={() => q.fetchNextPage()}>
                Load more
              </Button>
            </div>
          )}
        </div>
      )}
    </div>
  )
}
