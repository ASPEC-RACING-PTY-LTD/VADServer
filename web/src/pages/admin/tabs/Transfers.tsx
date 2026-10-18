import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, Eraser, XCircle } from 'lucide-react'
import { del, errorMessage, get, post } from '../../../lib/api'
import { formatBytes, timeAgo } from '../../../lib/format'
import { Button, EmptyState, ErrorState, IconButton, PageLoader, ProgressBar, Tabs, useUI } from '../../../components/ui'
import { TransfersTable } from '../../../components/TransfersTable'

export default function TransfersTab() {
  const [tab, setTab] = useState<'active' | 'failed' | ''>('active')
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const [clearing, setClearing] = useState(false)
  const clearHistory = async () => {
    const ok = await confirm({
      title: 'Clear history?',
      message: 'Removes all finished and failed transfers, the activity feed and notifications, so counters like "uploaded" start from zero. Active uploads, stored files, devices and audit entries are kept.',
      confirmLabel: 'Clear history',
    })
    if (!ok) return
    setClearing(true)
    try {
      const n = await post<{ transfers: number; activity: number; notifications: number }>('/admin/history/clear')
      toast('success', `Cleared ${n.transfers} transfers, ${n.activity} activity entries and ${n.notifications} notifications`)
      await qc.invalidateQueries()
    } catch (e) {
      toast('error', errorMessage(e))
    } finally {
      setClearing(false)
    }
  }
  const uploads = useQuery({ queryKey: ['admin-uploads'], queryFn: () => get('/admin/uploads'), refetchInterval: 5000, enabled: tab === 'active' })
  return (
    <div className="card overflow-hidden">
      <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
        <Tabs
          value={tab}
          onChange={setTab}
          tabs={[
            { id: 'active', label: 'Active uploads' },
            { id: 'failed', label: 'Failed' },
            { id: '', label: 'All transfers' },
          ]}
        />
        <Button size="sm" variant="danger" icon={<Eraser className="size-4" />} loading={clearing} onClick={clearHistory}>
          Clear history
        </Button>
      </div>
      {tab === 'active' ? (
        uploads.isLoading ? (
          <PageLoader />
        ) : uploads.error && !uploads.data ? (
          <ErrorState error={uploads.error} onRetry={uploads.refetch} />
        ) : (uploads.data?.uploads ?? []).length === 0 ? (
          <EmptyState icon={<CheckCircle2 className="size-6" />} title="No active uploads" />
        ) : (
          (uploads.data?.uploads ?? []).map((u: any) => (
            <div key={u.id} className="flex items-center gap-4 border-b border-slate-100 px-4 py-3 last:border-0 dark:border-slate-800">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{u.path}</div>
                <div className="text-xs text-slate-500">
                  {u.username} · {u.device_name || 'browser'} · updated {timeAgo(u.updated_at)} · expires {timeAgo(u.expires_at)}
                </div>
                <ProgressBar value={u.size ? (u.bytes_received / u.size) * 100 : 0} className="mt-1.5 h-1.5" />
              </div>
              <span className="text-xs text-slate-500">
                {formatBytes(u.bytes_received)} / {formatBytes(u.size)}
              </span>
              <IconButton
                label="Cancel upload"
                onClick={async () => {
                  try {
                    await del(`/admin/uploads/${u.id}`)
                    qc.invalidateQueries({ queryKey: ['admin-uploads'] })
                  } catch (e) {
                    toast('error', 'Could not cancel', errorMessage(e))
                  }
                }}
              >
                <XCircle className="size-4" />
              </IconButton>
            </div>
          ))
        )
      ) : (
        <TransfersTable status={tab} admin />
      )}
    </div>
  )
}
