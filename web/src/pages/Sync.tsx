import { useState } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useFeatureContext } from '../app/useFeatureContext'
import { BackupJobsPanel } from '../features/backups/JobsPage'
import { RestorePanel } from '../features/restore/RestorePage'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, ArrowUpFromLine, CheckCircle2, Folder, Gauge, Laptop, PauseCircle, RefreshCw, Timer, X, XCircle } from 'lucide-react'
import { get, post } from '../lib/api'
import { TransfersTable } from '../components/TransfersTable'
import { formatBytes, formatDuration, formatSpeed, timeAgo } from '../lib/format'
import { Badge, EmptyState, ErrorState, PageHeader, PageLoader, ProgressBar, Stat, Tabs } from '../components/ui'

/** Re-exported for existing importers (admin console); new code imports from components/TransfersTable. */
export { TransfersTable }

function DeviceSync({ d }: { d: any }) {
  const s = d.sync_state ?? {}
  const stats = s.stats ?? {}
  const configs: any[] = s.configs ?? []
  const active: any[] = s.active ?? []
  const status: string = d.online ? (s.status ?? 'idle') : 'offline'
  const tone = { syncing: 'blue', idle: 'green', paused: 'amber', error: 'red', offline: 'slate', scanning: 'blue' }[status] ?? 'slate'
  return (
    <div className="card overflow-hidden">
      <div className="flex flex-wrap items-center gap-3 border-b border-slate-200 px-5 py-4 dark:border-slate-800">
        <Laptop className="size-5 text-slate-500" />
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{d.name}</div>
          <div className="text-xs text-slate-500">
            {d.os} · {d.sync_state_at ? `reported ${timeAgo(d.sync_state_at)}` : 'has not reported sync status yet'}
          </div>
        </div>
        <Badge tone={tone as any}>
          {status === 'paused' ? <PauseCircle className="size-3" /> : status === 'syncing' ? <RefreshCw className="size-3 animate-spin" /> : null}
          {status}
        </Badge>
      </div>
      {d.online && (
        <div className="grid grid-cols-2 gap-px bg-slate-200 sm:grid-cols-4 dark:bg-slate-800">
          {[
            { l: 'Upload speed', v: formatSpeed(stats.speed_bps ?? 0), i: <Gauge className="size-4" /> },
            { l: 'Parallel transfers', v: `${stats.concurrency ?? 0}${stats.concurrency_limit ? ` / ${stats.concurrency_limit}` : ''}`, i: <ArrowUpFromLine className="size-4" /> },
            { l: 'Queued', v: `${stats.queued ?? 0} files`, i: <Timer className="size-4" /> },
            { l: 'Time remaining', v: stats.eta_seconds ? formatDuration(stats.eta_seconds) : '-', i: <Timer className="size-4" /> },
          ].map((x) => (
            <div key={x.l} className="bg-white px-5 py-3 dark:bg-slate-900">
              <div className="flex items-center gap-1.5 text-xs text-slate-500">
                {x.i}
                {x.l}
              </div>
              <div className="mt-0.5 font-semibold">{x.v}</div>
            </div>
          ))}
        </div>
      )}
      <div className="p-5">
        {configs.length === 0 ? (
          <p className="text-sm text-slate-500">No synchronisation configurations on this device yet.</p>
        ) : (
          <div className="space-y-2">
            {configs.map((c) => (
              <div key={c.id} className="flex flex-col gap-2 rounded-xl bg-slate-50 px-4 py-3 sm:flex-row sm:items-center dark:bg-slate-800/50">
                <Folder className="size-5 shrink-0 text-amber-500" />
                <div className="min-w-0 flex-1">
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="font-medium">{c.name}</span>
                    {!c.enabled && <Badge>disabled</Badge>}
                    <Badge tone={c.mode === 'auto' ? 'violet' : 'slate'}>{c.mode === 'auto' ? 'automatic' : 'manual'}</Badge>
                  </div>
                  <div className="truncate text-xs text-slate-500">
                    {c.local_path} → {c.destination}
                  </div>
                  {c.last_error && <div className="mt-0.5 text-xs text-rose-600">{c.last_error}</div>}
                </div>
                <div className="text-xs text-slate-500 sm:text-right">
                  <div>{(c.files_synced ?? 0).toLocaleString()} synced · {(c.files_pending ?? 0).toLocaleString()} pending</div>
                  <div>last scan {timeAgo(c.last_scan_at)}</div>
                </div>
              </div>
            ))}
          </div>
        )}
        {active.length > 0 && (
          <div className="mt-4 space-y-2">
            <div className="text-xs font-semibold tracking-wide text-slate-500 uppercase">Transferring now</div>
            {active.map((a, i) => (
              <div key={i}>
                <div className="flex justify-between text-xs">
                  <span className="truncate">{a.path}</span>
                  <span className="ml-2 shrink-0 text-slate-500">
                    {formatBytes(a.sent)} / {formatBytes(a.size)}
                  </span>
                </div>
                <ProgressBar value={a.size ? (a.sent / a.size) * 100 : 0} className="mt-1 h-1.5" />
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  )
}

/**
 * Sync: everything devices keep on this server in one place. Synced folders and transfers, and
 * (when the server has them) backups and restores, which are another kind of sync.
 */
export default function SyncPage() {
  const { tab: section } = useParams()
  const navigate = useNavigate()
  const { flags } = useFeatureContext()
  const backups = flags.has('backup.v1') || flags.has('restore.v1')
  const onBackups = backups && section === 'backups'
  return (
    <div>
      <PageHeader title="Sync" description="Folders and files your devices sync to this server, their backups, and restores." icon={<RefreshCw className="size-5" />} />
      {backups && (
        <div className="mb-6">
          <Tabs
            value={onBackups ? 'backups' : ''}
            onChange={(v) => navigate(v ? `/sync/${v}` : '/sync')}
            tabs={[
              { id: '', label: 'Synced folders' },
              { id: 'backups', label: 'Backups & Restore' },
            ]}
          />
        </div>
      )}
      {onBackups ? (
        <div className="space-y-10">
          {flags.has('backup.v1') && <BackupJobsPanel />}
          {flags.has('restore.v1') && <RestorePanel />}
        </div>
      ) : (
        <SyncedFolders />
      )}
    </div>
  )
}

function SyncedFolders() {
  const [tab, setTab] = useState<'' | 'succeeded' | 'failed' | 'active'>('')
  const qc = useQueryClient()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['sync-overview'], queryFn: () => get('/sync/overview'), refetchInterval: 5000 })
  const dismiss = useMutation({
    mutationFn: () => post('/sync/failures/dismiss'),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['sync-overview'] }),
  })
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  return (
    <div>
      <div className="mb-6 grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Active transfers" value={data.stats.active} icon={<ArrowUpFromLine className="size-5" />} tone="sky" />
        <Stat label="Completed (24h)" value={data.stats.completed_24h} sub={formatBytes(data.stats.bytes_24h)} icon={<CheckCircle2 className="size-5" />} tone="green" />
        <Stat label="Failed (24h)" value={data.stats.failed_24h} icon={<XCircle className="size-5" />} tone={data.stats.failed_24h ? 'red' : 'brand'} />
        <Stat label="Completed total" value={data.stats.completed_total.toLocaleString()} icon={<RefreshCw className="size-5" />} />
      </div>
      {data.recent_failures.length > 0 && (
        <div className="mb-6 rounded-2xl border border-rose-200 bg-rose-50 p-4 dark:border-rose-500/30 dark:bg-rose-500/10">
          <div className="mb-2 flex items-center gap-2 text-sm font-semibold text-rose-800 dark:text-rose-200">
            <AlertTriangle className="size-4" /> Recent failures
            <button
              type="button"
              onClick={() => dismiss.mutate()}
              disabled={dismiss.isPending}
              title="Hide these failures. New failures will show again."
              className="ml-auto inline-flex items-center gap-1 rounded-lg px-2 py-1 text-xs font-medium hover:bg-rose-100 disabled:opacity-50 dark:hover:bg-rose-500/20"
            >
              <X className="size-3.5" /> Dismiss
            </button>
          </div>
          <ul className="space-y-1 text-sm text-rose-700 dark:text-rose-300">
            {data.recent_failures.slice(0, 5).map((f: any) => (
              <li key={f.id} className="truncate">
                <span className="font-medium">{f.path}</span>: {f.error || 'failed'} <span className="text-xs opacity-70">({timeAgo(f.finished_at)})</span>
              </li>
            ))}
          </ul>
          <p className="mt-2 text-xs text-rose-700/80 dark:text-rose-300/80">Desktop apps retry failed files automatically, or you can retry them from the app's Transfers view.</p>
        </div>
      )}
      <div className="mb-8 grid gap-4 xl:grid-cols-2">
        {data.devices.length === 0 ? (
          <div className="card xl:col-span-2">
            <EmptyState icon={<Laptop className="size-6" />} title="No devices connected" description="Install the VaultDock desktop app on Windows to synchronise folders automatically." />
          </div>
        ) : (
          data.devices.map((d: any) => <DeviceSync key={d.id} d={d} />)
        )}
      </div>
      <div className="card overflow-hidden">
        <div className="flex flex-wrap items-center justify-between gap-3 border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="font-semibold">Transfer history</h2>
          <Tabs
            value={tab}
            onChange={setTab}
            tabs={[
              { id: '', label: 'All' },
              { id: 'active', label: 'Active' },
              { id: 'succeeded', label: 'Completed' },
              { id: 'failed', label: 'Failed' },
            ]}
          />
        </div>
        <TransfersTable status={tab} />
      </div>
    </div>
  )
}
