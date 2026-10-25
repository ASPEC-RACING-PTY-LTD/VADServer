import { Link } from 'react-router-dom'
import { AlertTriangle, CheckCircle2, Clapperboard, Cpu, Eye, HardDrive, ListVideo, Plus, Sparkles, Trash2 } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, PageHeader, PageLoader, ProgressBar, Stat } from '../../components/ui'
import { formatBytes, formatDuration } from '../../lib/format'
import { queueStateTone, useQueues, useSmartStatus, useStats } from './api'

export default function OverviewPage() {
  const status = useSmartStatus(5000)
  const stats = useStats()
  const queues = useQueues('active,paused', 5000)
  if (status.isLoading || stats.isLoading) return <PageLoader />
  if (status.error) return <ErrorState error={status.error} onRetry={() => status.refetch()} />
  const st = status.data!
  const t = stats.data?.total
  const review = (queues.data?.queues ?? []).reduce((n, q) => n + (q.tally?.item_states.ready ?? 0), 0)
  const avail = st.detectors.filter((d) => d.available).length
  return (
    <div className="space-y-6">
      <PageHeader
        title="Smart Processing"
        icon={<Sparkles className="size-5" />}
        description="Finds interesting moments in your recordings, lets you review them, renders the clips you keep and helps reclaim space from originals you no longer need. Everything runs on this server."
        actions={
          <Link to="/smart/new">
            <Button variant="primary" icon={<Plus className="size-4" />}>
              New queue
            </Button>
          </Link>
        }
      />

      {!st.enabled && (
        <div className="card flex items-center gap-3 border-amber-300 p-4 text-sm text-amber-800 dark:text-amber-200">
          <AlertTriangle className="size-5" /> Processing is switched off by the administrator. Queues and reviews stay available; nothing is analysed or rendered.
        </div>
      )}
      {!st.ffmpeg && (
        <div className="card flex items-center gap-3 border-rose-300 p-4 text-sm text-rose-700 dark:text-rose-300">
          <AlertTriangle className="size-5" /> FFmpeg is not available on the server, so recordings cannot be analysed.
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Waiting for review" value={review} icon={<Eye className="size-5" />} tone="amber" sub={review ? <Link className="text-brand-600" to="/smart/review">Start reviewing</Link> : 'Nothing to review'} />
        <Stat label="Clips made" value={t?.generated_count ?? 0} icon={<Clapperboard className="size-5" />} sub={formatBytes(t?.generated_bytes ?? 0)} />
        <Stat label="Space reclaimed" value={formatBytes(t?.freed_bytes ?? 0)} icon={<HardDrive className="size-5" />} tone="green" sub={`net ${formatBytes(Math.abs(t?.net_bytes ?? 0))} ${(t?.net_bytes ?? 0) < 0 ? 'used by clips' : 'saved'}`} />
        <Stat label="Could be reclaimed" value={formatBytes(t?.estimated_bytes ?? 0)} icon={<Trash2 className="size-5" />} tone="sky" sub={t?.estimated_count ? <Link className="text-brand-600" to="/smart/cleanup">{t.estimated_count} originals ready for cleanup</Link> : 'Estimated from verified clips'} />
      </div>

      <div className="grid gap-6 lg:grid-cols-3">
        <div className="card p-5 lg:col-span-2">
          <div className="mb-3 flex items-center justify-between">
            <h2 className="font-semibold text-slate-900 dark:text-white">Active queues</h2>
            <Link className="text-sm text-brand-600" to="/smart/queues">
              All queues
            </Link>
          </div>
          {(queues.data?.queues ?? []).length === 0 ? (
            <EmptyState icon={<ListVideo className="size-6" />} title="No active queues" description="Create a queue from folders or files, choose what to look for, and processing runs in the background." />
          ) : (
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {queues.data!.queues.map((q) => {
                const tl = q.tally
                const finished = tl ? (tl.item_states.done ?? 0) + (tl.item_states.failed ?? 0) + (tl.item_states.gone ?? 0) + (tl.item_states.cancelled ?? 0) : 0
                return (
                  <li key={q.id} className="py-3">
                    <Link to={`/smart/queues/${q.id}`} className="flex items-center gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2 font-medium text-slate-800 dark:text-slate-100">
                          {q.name} <Badge tone={queueStateTone(q.state)}>{q.state}</Badge>
                        </div>
                        <div className="text-xs text-slate-500">
                          {tl?.items ?? 0} recordings · {tl?.item_states.ready ?? 0} to review · {tl?.item_states.pending ?? 0} queued · {q.counters.generated_count} clips
                        </div>
                      </div>
                      <div className="w-40">
                        <ProgressBar value={tl?.items ? (finished / tl.items) * 100 : 0} />
                      </div>
                    </Link>
                  </li>
                )
              })}
            </ul>
          )}
        </div>

        <div className="card space-y-4 p-5">
          <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
            <Cpu className="size-4" /> Processing
          </h2>
          <div className="text-sm text-slate-600 dark:text-slate-300">
            {avail} of {st.detectors.length} detectors available.{' '}
            {st.worker.configured ? (
              st.worker.reachable ? (
                <span className="text-emerald-600">
                  <CheckCircle2 className="inline size-4" /> Inference worker online ({st.worker.device})
                </span>
              ) : (
                <span className="text-amber-600">Inference worker unreachable</span>
              )
            ) : (
              <span>No inference worker: loudness, motion and scene changes only.</span>
            )}
          </div>
          {st.running.length === 0 ? (
            <p className="text-sm text-slate-500">Nothing running right now.</p>
          ) : (
            <ul className="space-y-3">
              {st.running.map((j) => (
                <li key={j.item_id} className="text-sm">
                  <div className="flex justify-between gap-2">
                    <span className="truncate font-medium">{j.name}</span>
                    <span className="shrink-0 text-xs text-slate-500">{formatDuration((Date.now() - j.started_at) / 1000)}</span>
                  </div>
                  <div className="text-xs text-slate-500">
                    {j.kind === 'render' ? 'Rendering' : 'Analysing'}: {j.stage}
                  </div>
                  <ProgressBar value={j.progress * 100} />
                </li>
              ))}
            </ul>
          )}
          <p className="text-xs text-slate-500">Processing continues when this page is closed.</p>
        </div>
      </div>
    </div>
  )
}
