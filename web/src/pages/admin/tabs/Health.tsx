import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Activity, AlertTriangle, CheckCircle2, Cpu, Database, RefreshCw } from 'lucide-react'
import { errorMessage, get, post } from '../../../lib/api'
import { formatBytes, formatDuration } from '../../../lib/format'
import { Button, cx, ErrorState, PageLoader, ProgressBar, Stat, useUI } from '../../../components/ui'
import { LocationBar } from '../LocationBar'

export default function HealthTab() {
  const [deep, setDeep] = useState(false)
  const qc = useQueryClient()
  const { toast } = useUI()
  const { data, isLoading, error, refetch, isFetching } = useQuery({ queryKey: ['admin-health', deep], queryFn: () => get(`/admin/health${deep ? '?deep=1' : ''}`), refetchInterval: 15_000 })
  const [cleaning, setCleaning] = useState(false)
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  return (
    <div className="space-y-6">
      <div className={cx('flex items-center gap-3 rounded-2xl p-4', data.status === 'healthy' ? 'bg-emerald-50 text-emerald-900 dark:bg-emerald-500/10 dark:text-emerald-200' : 'bg-amber-50 text-amber-900 dark:bg-amber-500/10 dark:text-amber-200')}>
        {data.status === 'healthy' ? <CheckCircle2 className="size-6" /> : <AlertTriangle className="size-6" />}
        <div className="flex-1">
          <div className="font-semibold">{data.status === 'healthy' ? 'All systems healthy' : 'Attention needed'}</div>
          {data.problems?.length > 0 && <div className="text-sm">{data.problems.join(' · ')}</div>}
        </div>
        <Button size="sm" loading={isFetching} onClick={() => (setDeep(true), refetch())}>
          Run integrity check
        </Button>
        <Button
          size="sm"
          loading={cleaning}
          onClick={async () => {
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
          }}
        >
          Run maintenance
        </Button>
      </div>
      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat label="Uptime" value={formatDuration(data.uptime_seconds)} sub={`VaultDock ${data.version} · ${data.go}`} icon={<Activity className="size-5" />} />
        <Stat label="Memory" value={formatBytes(data.memory.heap_alloc)} sub={`${formatBytes(data.memory.sys)} reserved · ${data.goroutines} goroutines`} icon={<Cpu className="size-5" />} tone="sky" />
        <Stat label="Database" value={formatBytes(data.database.size_bytes)} sub={`schema v${data.database.schema_version} · integrity ${data.database.integrity}`} icon={<Database className="size-5" />} tone={data.database.ok ? 'green' : 'red'} />
        <Stat label="Upload slots" value={`${data.chunk_slots.in_use} / ${data.chunk_slots.capacity}`} sub="concurrent chunk writes" icon={<RefreshCw className="size-5" />} />
      </div>
      <div className="card p-6">
        <h3 className="mb-4 font-semibold">Disks</h3>
        <div className="space-y-5">
          {data.locations.map((l: any) => (
            <LocationBar key={l.id} l={l} />
          ))}
          <div>
            <div className="mb-1.5 flex justify-between text-sm">
              <span className="font-medium">Application state (database)</span>
              <span className="text-xs text-slate-500">
                {formatBytes(data.state_volume.available)} free of {formatBytes(data.state_volume.total)}
              </span>
            </div>
            <ProgressBar value={data.state_volume.total ? ((data.state_volume.total - data.state_volume.available) / data.state_volume.total) * 100 : 0} />
            <div className="mt-1 font-mono text-[11px] text-slate-400">{data.database.path}</div>
          </div>
        </div>
      </div>
    </div>
  )
}
