import { useQuery } from '@tanstack/react-query'
import { Database, Monitor, RefreshCw, Users } from 'lucide-react'
import { get } from '../../../lib/api'
import { formatBytes, formatDuration, timeAgo } from '../../../lib/format'
import { describe } from '../../../lib/activity'
import { ErrorState, PageLoader, Stat } from '../../../components/ui'
import { UpdateBanner } from '../Updates'
import { LocationBar } from '../LocationBar'

export default function OverviewTab() {
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-overview'], queryFn: () => get('/admin/overview'), refetchInterval: 15_000 })
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const s = data.stats
  return (
    <div className="space-y-6">
      <UpdateBanner />
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Users" value={s.users} sub={`${s.active_users} active`} icon={<Users className="size-5" />} />
        <Stat label="Stored" value={formatBytes(s.logical_bytes)} sub={`${formatBytes(s.physical_bytes)} on disk after deduplication`} icon={<Database className="size-5" />} tone="sky" />
        <Stat label="Devices" value={s.devices} sub={s.pending_devices ? `${s.pending_devices} pending approval` : 'none pending'} icon={<Monitor className="size-5" />} tone={s.pending_devices ? 'amber' : 'green'} />
        <Stat label="Open uploads" value={s.open_uploads} sub={`${data.transfers.failed_24h} failed in 24h`} icon={<RefreshCw className="size-5" />} tone={data.transfers.failed_24h ? 'red' : 'brand'} />
      </div>
      <div className="grid gap-6 xl:grid-cols-2">
        <div className="card p-6">
          <h3 className="mb-4 font-semibold">Storage devices</h3>
          <div className="space-y-5">
            {data.locations.map((l: any) => (
              <LocationBar key={l.id} l={l} />
            ))}
          </div>
        </div>
        <div className="card p-6">
          <h3 className="mb-4 font-semibold">Recent security events</h3>
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
          <p className="mt-4 text-xs text-slate-400">
            VaultDock {data.version} · up {formatDuration(data.uptime_seconds)}
          </p>
        </div>
      </div>
    </div>
  )
}
