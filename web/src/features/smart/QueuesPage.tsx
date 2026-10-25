import { Link } from 'react-router-dom'
import { Archive, ListVideo, Plus } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, PageHeader, PageLoader, ProgressBar } from '../../components/ui'
import { formatBytes, formatDuration, timeAgo } from '../../lib/format'
import { queueStateTone, useQueues, type Queue } from './api'

function finished(q: Queue) {
  const s = q.tally?.item_states ?? {}
  return (s.done ?? 0) + (s.failed ?? 0) + (s.gone ?? 0) + (s.cancelled ?? 0)
}

/** Active (active, paused) or completed (completed, cancelled, archived) queues. */
export default function QueuesPage({ view }: { view: 'active' | 'completed' }) {
  const q = useQueues(view === 'active' ? 'active,paused' : 'completed,cancelled,archived', view === 'active' ? 5000 : undefined)
  if (q.isLoading) return <PageLoader />
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  const list = q.data?.queues ?? []
  return (
    <div className="space-y-4">
      <PageHeader
        title={view === 'active' ? 'Active queues' : 'Completed queues'}
        icon={view === 'active' ? <ListVideo className="size-5" /> : <Archive className="size-5" />}
        description={view === 'active' ? 'Queues being analysed, reviewed or rendered. They continue after restarts.' : 'Finished, cancelled and archived queues keep their recordings, decisions, clips and statistics.'}
        actions={
          view === 'active' && (
            <Link to="/smart/new">
              <Button variant="primary" icon={<Plus className="size-4" />}>
                New queue
              </Button>
            </Link>
          )
        }
      />
      {list.length === 0 ? (
        <EmptyState icon={<ListVideo className="size-6" />} title={view === 'active' ? 'No active queues' : 'No completed queues yet'} />
      ) : (
        <div className="card divide-y divide-slate-100 dark:divide-slate-800">
          {list.map((x) => {
            const t = x.tally
            return (
              <Link key={x.id} to={`/smart/queues/${x.id}`} className="flex flex-wrap items-center gap-4 p-4 hover:bg-slate-50 dark:hover:bg-slate-800/40">
                <div className="min-w-52 flex-1">
                  <div className="flex items-center gap-2 font-medium text-slate-900 dark:text-white">
                    {x.name} <Badge tone={queueStateTone(x.state)}>{x.state}</Badge>
                    {x.auto_approve && <Badge tone="violet">auto-approve</Badge>}
                  </div>
                  <div className="text-xs text-slate-500">
                    {t?.items ?? 0} recordings ({formatBytes(t?.original_bytes ?? 0)}) · created {timeAgo(x.created_at)}
                    {x.completed_at ? ` · finished ${timeAgo(x.completed_at)}` : ''}
                  </div>
                </div>
                <div className="grid grid-cols-4 gap-4 text-center text-xs text-slate-500">
                  <div>
                    <div className="text-base font-semibold text-slate-800 dark:text-slate-100">{t?.item_states.ready ?? 0}</div>to review
                  </div>
                  <div>
                    <div className="text-base font-semibold text-slate-800 dark:text-slate-100">{x.counters.generated_count}</div>clips
                  </div>
                  <div>
                    <div className="text-base font-semibold text-slate-800 dark:text-slate-100">{formatBytes(x.counters.freed_bytes)}</div>reclaimed
                  </div>
                  <div>
                    <div className="text-base font-semibold text-slate-800 dark:text-slate-100">{formatDuration((x.counters.analysis_ms + x.counters.render_ms) / 1000)}</div>processing
                  </div>
                </div>
                <div className="w-40">
                  <ProgressBar value={t?.items ? (finished(x) / t.items) * 100 : 0} tone={x.state === 'completed' ? 'green' : 'brand'} />
                </div>
              </Link>
            )
          })}
        </div>
      )}
    </div>
  )
}
