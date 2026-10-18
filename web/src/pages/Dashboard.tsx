import { Component, Suspense, type ReactNode } from 'react'
import { Link } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, ArrowUpRight, CheckCircle2, CloudUpload, Files, FolderOpen, HardDrive, Monitor, Workflow } from 'lucide-react'
import { get } from '../lib/api'
import { useUser } from '../lib/auth'
import { formatBytes, pluralize, timeAgo } from '../lib/format'
import { enabledWidgets, featureWidgets, type DashboardWidget } from '../lib/dashboardWidgets'
import { useFeatureContext } from '../app/useFeatureContext'
import { Button, ErrorState, PageHeader, PageLoader, ProgressBar, Stat } from '../components/ui'
import { FileIcon } from '../components/FileIcon'

const kindColors: Record<string, string> = { video: 'bg-violet-500', image: 'bg-pink-500', document: 'bg-sky-500', audio: 'bg-teal-500', archive: 'bg-orange-500', other: 'bg-slate-400' }
const kindLabels: Record<string, string> = { video: 'Videos', image: 'Images', document: 'Documents', audio: 'Audio', archive: 'Archives', other: 'Other' }

const statsQuery = { queryKey: ['stats'], queryFn: () => get('/stats'), refetchInterval: 30_000 }
const syncQuery = { queryKey: ['sync-overview'], queryFn: () => get('/sync/overview'), refetchInterval: 30_000 }

/** `/stats` for the built-in widgets. DashboardPage renders widgets only after it has loaded. */
function useStats(): any {
  return useQuery(statsQuery).data
}

function StatsWidget() {
  const data = useStats()
  const { data: sync } = useQuery(syncQuery)
  const used = data.usage.used_bytes as number
  const quota = data.usage.quota_bytes as number
  const devicesOnline = (sync?.devices ?? []).filter((d: any) => d.online).length
  return (
    <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <Stat label="Storage used" value={formatBytes(used)} sub={quota > 0 ? `of ${formatBytes(quota)} quota` : data.device_available_bytes !== undefined ? `${formatBytes(data.device_available_bytes)} free on server` : 'No quota set'} icon={<HardDrive className="size-5" />} />
      <Stat label="Files" value={data.files.files.toLocaleString()} sub={pluralize(data.files.folders, 'folder')} icon={<Files className="size-5" />} tone="sky" />
      <Stat label="Uploaded (24h)" value={data.transfers.completed_24h.toLocaleString()} sub={`${formatBytes(data.transfers.bytes_24h)} transferred`} icon={<CloudUpload className="size-5" />} tone="green" />
      <Stat label="Devices" value={`${data.devices.approved}`} sub={`${devicesOnline} online${data.devices.pending ? ` · ${data.devices.pending} pending` : ''}`} icon={<Monitor className="size-5" />} tone={data.devices.pending ? 'amber' : 'brand'} />
    </div>
  )
}

function StorageWidget() {
  const data = useStats()
  const used = data.usage.used_bytes as number
  const quota = data.usage.quota_bytes as number
  const byKind: Record<string, number> = data.files.bytes_by_kind ?? {}
  const kinds = Object.entries(byKind).sort((a, b) => b[1] - a[1])
  const totalKind = kinds.reduce((a, [, v]) => a + v, 0)
  return (
    <div className="card p-6">
      <div className="mb-5 flex items-center justify-between">
        <h2 className="text-base font-semibold">Storage overview</h2>
        {quota > 0 && <span className="text-sm text-slate-500">{Math.round((used / quota) * 100)}% of quota</span>}
      </div>
      {quota > 0 && <ProgressBar value={(used / quota) * 100} tone={used / quota > 0.9 ? 'red' : used / quota > 0.75 ? 'amber' : 'brand'} className="mb-6 h-3" />}
      {totalKind > 0 ? (
        <>
          <div className="flex h-3 overflow-hidden rounded-full bg-slate-100 dark:bg-slate-800">
            {kinds.map(([k, v]) => (
              <div key={k} className={kindColors[k]} style={{ width: `${(v / totalKind) * 100}%` }} title={`${kindLabels[k]}: ${formatBytes(v)}`} />
            ))}
          </div>
          <div className="mt-5 grid grid-cols-2 gap-4 sm:grid-cols-3">
            {kinds.map(([k, v]) => (
              <div key={k} className="flex items-center gap-3">
                <span className={`size-3 rounded-full ${kindColors[k]}`} />
                <div>
                  <div className="text-sm font-medium">{kindLabels[k]}</div>
                  <div className="text-xs text-slate-500">
                    {formatBytes(v)} · {pluralize(data.files.count_by_kind[k] ?? 0, 'file')}
                  </div>
                </div>
              </div>
            ))}
          </div>
        </>
      ) : (
        <p className="text-sm text-slate-500">No files yet. Upload files in the browser or connect the desktop app to start syncing.</p>
      )}
      {data.files.trash_bytes > 0 && (
        <p className="mt-5 text-xs text-slate-500">
          Trash holds {formatBytes(data.files.trash_bytes)} that still counts towards your usage. <Link to="/trash" className="text-brand-600 hover:underline">Review trash</Link>
        </p>
      )}
    </div>
  )
}

function SyncWidget() {
  const data = useStats()
  const { data: sync } = useQuery(syncQuery)
  return (
    <div className="card p-6">
      <div className="mb-4 flex items-center justify-between">
        <h2 className="text-base font-semibold">Synchronisation</h2>
        <Link to="/sync" className="flex items-center gap-1 text-xs font-medium text-brand-600 hover:underline">
          Details <ArrowUpRight className="size-3" />
        </Link>
      </div>
      <div className="space-y-3">
        <div className="flex items-center justify-between text-sm">
          <span className="text-slate-500">Active transfers</span>
          <span className="font-medium">{data.transfers.active}</span>
        </div>
        <div className="flex items-center justify-between text-sm">
          <span className="text-slate-500">Failed (24h)</span>
          <span className={data.transfers.failed_24h ? 'font-medium text-rose-600' : 'font-medium'}>{data.transfers.failed_24h}</span>
        </div>
        <div className="flex items-center justify-between text-sm">
          <span className="text-slate-500">Total completed</span>
          <span className="font-medium">{data.transfers.completed_total.toLocaleString()}</span>
        </div>
      </div>
      <div className="mt-5 space-y-2">
        {(sync?.devices ?? []).slice(0, 4).map((d: any) => (
          <div key={d.id} className="flex items-center gap-3 rounded-xl bg-slate-50 px-3 py-2.5 dark:bg-slate-800/50">
            <span className={`size-2 rounded-full ${d.online ? 'bg-emerald-500' : 'bg-slate-400'}`} />
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">{d.name}</div>
              <div className="text-xs text-slate-500">{d.online ? 'Online' : `Last seen ${timeAgo(d.last_seen_at)}`}</div>
            </div>
          </div>
        ))}
        {!(sync?.devices ?? []).length && (
          <div className="rounded-xl border border-dashed border-slate-300 p-4 text-center text-sm text-slate-500 dark:border-slate-700">
            No devices connected yet. Install the VaultDock desktop app and sign in with this server's address.
          </div>
        )}
      </div>
      {data.transfers.failed_24h > 0 ? (
        <div className="mt-4 flex items-center gap-2 text-xs text-rose-600">
          <AlertTriangle className="size-4" /> Some transfers failed recently.
        </div>
      ) : (
        <div className="mt-4 flex items-center gap-2 text-xs text-emerald-600">
          <CheckCircle2 className="size-4" /> No failed transfers in the last 24 hours.
        </div>
      )}
    </div>
  )
}

function RecentWidget() {
  const data = useStats()
  return (
    <div className="card p-6">
      <h2 className="mb-4 text-base font-semibold">Recently added</h2>
      {data.recent.length === 0 ? (
        <p className="text-sm text-slate-500">Nothing here yet.</p>
      ) : (
        <div className="divide-y divide-slate-100 dark:divide-slate-800">
          {data.recent.map((f: any) => (
            <Link key={f.id} to={`/files/${f.parent_id}?select=${f.id}`} className="-mx-2 flex items-center gap-3 rounded-lg px-2 py-2.5 hover:bg-slate-50 dark:hover:bg-slate-800/50">
              <FileIcon mime={f.mime} isDir={false} size="sm" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{f.name}</div>
                <div className="truncate text-xs text-slate-500">{f.path}</div>
              </div>
              <div className="text-right text-xs text-slate-500">
                <div>{formatBytes(f.size)}</div>
                <div>{timeAgo(f.created_at)}</div>
              </div>
            </Link>
          ))}
        </div>
      )}
    </div>
  )
}

function AutomationWidget() {
  return (
    <div className="card flex flex-col p-6">
      <div className="mb-3 flex size-10 items-center justify-center rounded-xl bg-brand-100 text-brand-700 dark:bg-brand-500/15 dark:text-brand-300">
        <Workflow className="size-5" />
      </div>
      <h2 className="text-base font-semibold">Automate your uploads</h2>
      <p className="mt-1 flex-1 text-sm text-slate-500 dark:text-slate-400">Build visual workflows that sort clips by game, skip temporary files, tag uploads and notify you when something goes wrong.</p>
      <Link to="/automations" className="mt-4">
        <Button className="w-full">Open automation builder</Button>
      </Link>
    </div>
  )
}

/** Built-in widgets: the original dashboard content. Feature widgets come from lib/dashboardWidgets. */
const builtinWidgets: DashboardWidget[] = [
  { id: 'core.stats', order: 10, span: 3, component: StatsWidget },
  { id: 'core.storage', order: 20, span: 2, component: StorageWidget },
  { id: 'core.sync', order: 30, component: SyncWidget },
  { id: 'core.recent', order: 40, span: 2, component: RecentWidget },
  { id: 'core.automation', order: 50, component: AutomationWidget },
]
const allWidgets: readonly DashboardWidget[] = [...builtinWidgets, ...featureWidgets]

const spanClass = { 1: '', 2: 'xl:col-span-2', 3: 'xl:col-span-3' } as const

/** Keeps one broken widget from blanking the whole dashboard. */
class WidgetBoundary extends Component<{ id: string; children: ReactNode }, { error: unknown }> {
  state: { error: unknown } = { error: null }
  static getDerivedStateFromError(error: unknown) {
    return { error }
  }
  componentDidCatch(error: unknown) {
    console.error(`dashboard widget "${this.props.id}" failed`, error)
  }
  render() {
    if (this.state.error) return <ErrorState error={this.state.error} onRetry={() => this.setState({ error: null })} />
    return this.props.children
  }
}

export function DashboardPage() {
  const user = useUser()
  const ctx = useFeatureContext()
  const { isLoading, error, refetch } = useQuery(statsQuery)
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const greeting = new Date().getHours() < 12 ? 'Good morning' : new Date().getHours() < 18 ? 'Good afternoon' : 'Good evening'

  return (
    <div>
      <PageHeader
        title={`${greeting}, ${user.display_name || user.username}`}
        description="Here's what's happening across your storage and devices."
        actions={
          <Link to="/files">
            <Button variant="primary" icon={<FolderOpen className="size-4" />}>
              Open files
            </Button>
          </Link>
        }
      />
      <div className="grid gap-6 xl:grid-cols-3">
        {enabledWidgets(allWidgets, ctx).map((w) => (
          // `grid` stretches the widget's root card to the row height, as when the cards were grid items.
          <div key={w.id} className={`grid ${spanClass[w.span ?? 1]}`}>
            <WidgetBoundary id={w.id}>
              <Suspense fallback={<PageLoader />}>
                <w.component />
              </Suspense>
            </WidgetBoundary>
          </div>
        ))}
      </div>
    </div>
  )
}
