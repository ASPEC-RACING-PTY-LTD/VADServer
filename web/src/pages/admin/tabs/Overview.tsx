import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, Cpu, Database, HardDrive, Monitor, RefreshCw, Users } from 'lucide-react'
import { errorMessage, get, post } from '../../../lib/api'
import { formatBytes, formatDuration, timeAgo } from '../../../lib/format'
import { describe } from '../../../lib/activity'
import { Button, cx, ErrorState, PageLoader, ProgressBar, Stat, useUI } from '../../../components/ui'
import { useFeatureContext } from '../../../app/useFeatureContext'
import { ReadinessPanel } from '../../../features/readiness/ReadinessPage'
import { UpdateBanner } from '../Updates'
import { LocationBar } from '../LocationBar'

/**
 * Administration > Overview: the server's status in one place (what used to be the overview,
 * System health and Readiness): health and maintenance, key numbers, disks, backup readiness of
 * every device, and recent security events.
 */
export default function OverviewTab() {
  const qc = useQueryClient()
  const { toast } = useUI()
  const { flags } = useFeatureContext()
  const [deep, setDeep] = useState(false)
  const [cleaning, setCleaning] = useState(false)
  const overview = useQuery({ queryKey: ['admin-overview'], queryFn: () => get('/admin/overview'), refetchInterval: 15_000 })
  const health = useQuery({ queryKey: ['admin-health', deep], queryFn: () => get(`/admin/health${deep ? '?deep=1' : ''}`), refetchInterval: 15_000 })
  if (overview.isLoading || health.isLoading) return <PageLoader />
  if (overview.error) return <ErrorState error={overview.error} onRetry={overview.refetch} />
  if (health.error) return <ErrorState error={health.error} onRetry={health.refetch} />
  const data = overview.data
  const h = health.data
  const s = data.stats
  const healthy = h.status === 'healthy'

  const maintenance = async () => {
    setCleaning(true)
    try {
      const r = await post('/admin/maintenance/cleanup')
      toast('success', 'Maintenance complete', Object.entries(r).map(([k, v]) => `${k.replace(/_/g, ' ')}: ${v}`).join(', '))
      qc.invalidateQueries({ queryKey: ['admin-health'] })
    } catch (e) {
      toast('error', 'Maintenance failed', errorMessage(e))
    } finally {
      setCleaning(false)
    }
  }

  return (
    <div className="space-y-6">
      <UpdateBanner />
      <div className={cx('flex flex-wrap items-center gap-3 rounded-2xl p-4', healthy ? 'bg-emerald-50 text-emerald-900 dark:bg-emerald-500/10 dark:text-emerald-200' : 'bg-amber-50 text-amber-900 dark:bg-amber-500/10 dark:text-amber-200')}>
        {healthy ? <CheckCircle2 className="size-6 shrink-0" /> : <AlertTriangle className="size-6 shrink-0" />}
        <div className="min-w-0 flex-1">
          <div className="font-semibold">{healthy ? 'All systems healthy' : 'Attention needed'}</div>
          {h.problems?.length > 0 && <div className="text-sm">{h.problems.join(' · ')}</div>}
          <div className="text-xs opacity-80">
            VaultDock {h.version} · up {formatDuration(h.uptime_seconds)}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          <Button size="sm" loading={health.isFetching && deep} onClick={() => (setDeep(true), health.refetch())}>
            Run integrity check
          </Button>
          <Button size="sm" loading={cleaning} onClick={maintenance}>
            Run maintenance
          </Button>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <Stat compact label="Users" value={s.users} sub={`${s.active_users} active`} icon={<Users className="size-5" />} />
        <Stat compact label="Stored" value={formatBytes(s.logical_bytes)} sub={`${formatBytes(s.physical_bytes)} on disk`} icon={<Database className="size-5" />} tone="sky" />
        <Stat compact label="Devices" value={s.devices} sub={s.pending_devices ? `${s.pending_devices} pending approval` : 'none pending'} icon={<Monitor className="size-5" />} tone={s.pending_devices ? 'amber' : 'green'} />
        <Stat compact label="Open uploads" value={s.open_uploads} sub={`${data.transfers.failed_24h} failed in 24h`} icon={<RefreshCw className="size-5" />} tone={data.transfers.failed_24h ? 'red' : 'brand'} />
        <Stat compact label="Memory" value={formatBytes(h.memory.heap_alloc)} sub={`${formatBytes(h.memory.sys)} reserved`} icon={<Cpu className="size-5" />} tone="sky" />
        <Stat compact label="Database" value={formatBytes(h.database.size_bytes)} sub={`schema v${h.database.schema_version} · ${h.database.integrity}`} icon={<Database className="size-5" />} tone={h.database.ok ? 'green' : 'red'} />
        <Stat compact label="Upload slots" value={`${h.chunk_slots.in_use} / ${h.chunk_slots.capacity}`} sub="chunk writes in use" icon={<Activity className="size-5" />} />
        <Stat compact label="Goroutines" value={h.goroutines} sub={h.go} icon={<Cpu className="size-5" />} />
      </div>

      <div className="grid items-start gap-6 lg:grid-cols-2">
        <div className="card p-5">
          <h3 className="mb-4 flex items-center gap-2 font-semibold">
            <HardDrive className="size-4" /> Disks
          </h3>
          <div className="space-y-4">
            {h.locations.map((l: any) => (
              <LocationBar key={l.id} l={l} />
            ))}
            <div>
              <div className="mb-1.5 flex justify-between gap-2 text-sm">
                <span className="font-medium">Application state (database)</span>
                <span className="shrink-0 text-xs text-slate-500">
                  {formatBytes(h.state_volume.available)} free of {formatBytes(h.state_volume.total)}
                </span>
              </div>
              <ProgressBar value={h.state_volume.total ? ((h.state_volume.total - h.state_volume.available) / h.state_volume.total) * 100 : 0} />
              <div className="mt-1 truncate font-mono text-[11px] text-slate-400">{h.database.path}</div>
            </div>
          </div>
        </div>
        <div className="card p-5">
          <h3 className="mb-3 font-semibold">Recent security events</h3>
          {data.recent_audit.length === 0 ? (
            <p className="text-sm text-slate-500">Nothing recorded yet.</p>
          ) : (
            <div className="space-y-2">
              {data.recent_audit.map((a: any) => (
                <div key={a.id} className="flex justify-between gap-3 text-sm">
                  <span className="truncate">
                    <span className="font-medium">{describe(a)}</span> <span className="text-slate-500">{a.target}</span>
                  </span>
                  <span className="shrink-0 text-xs text-slate-400">{timeAgo(a.created_at)}</span>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>

      {flags.has('backup.v1') && <ReadinessPanel />}
    </div>
  )
}
