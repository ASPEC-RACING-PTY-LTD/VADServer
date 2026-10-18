import { useQuery } from '@tanstack/react-query'
import { ArrowUpFromLine } from 'lucide-react'
import { get } from '../lib/api'
import { transferSource } from '../lib/activity'
import { formatBytes, formatDate, timeAgo } from '../lib/format'
import { Badge, EmptyState, ErrorState, PageLoader, ProgressBar } from './ui'

const transferTone: Record<string, any> = { completed: 'green', deduplicated: 'violet', unchanged: 'slate', active: 'blue', failed: 'red', cancelled: 'slate', skipped: 'amber' }

/**
 * Transfer history table (latest 200, refreshed every 10 s). `status` filters ('' = all),
 * `device` limits to one device, `admin` reads `/admin/transfers` and shows the owning user.
 */
export function TransfersTable({ status, device, admin }: { status: string; device?: number; admin?: boolean }) {
  const q = useQuery({
    queryKey: ['transfers', status, device, admin],
    queryFn: () => get(`${admin ? '/admin/transfers' : '/transfers'}?limit=200${status ? `&status=${status}` : ''}${device ? `&device=${device}` : ''}`),
    refetchInterval: 10_000,
  })
  if (q.isLoading) return <PageLoader />
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  const items = q.data.items as any[]
  if (!items.length) return <EmptyState icon={<ArrowUpFromLine className="size-6" />} title="No transfers" description="Transfers appear here as files are uploaded from devices or the browser." />
  return (
    <div className="overflow-x-auto">
      <table className="w-full text-sm">
        <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
          <tr>
            <th className="px-4 py-2.5">File</th>
            <th className="px-4 py-2.5">Status</th>
            <th className="hidden px-4 py-2.5 text-right sm:table-cell">Size</th>
            <th className="hidden px-4 py-2.5 md:table-cell">Source</th>
            <th className="px-4 py-2.5">When</th>
          </tr>
        </thead>
        <tbody>
          {items.map((t) => (
            <tr key={t.id} className="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
              <td className="max-w-md px-4 py-2.5">
                <div className="truncate font-medium" title={t.path}>
                  {t.path}
                </div>
                {t.error && <div className="mt-0.5 text-xs break-words text-rose-600">{t.error}</div>}
                {t.status === 'active' && <ProgressBar value={t.size ? (t.bytes_done / t.size) * 100 : 0} className="mt-1.5 h-1.5" />}
              </td>
              <td className="px-4 py-2.5">
                <Badge tone={transferTone[t.status] ?? 'slate'}>{t.status}</Badge>
              </td>
              <td className="hidden px-4 py-2.5 text-right text-slate-500 tabular-nums sm:table-cell">{formatBytes(t.size)}</td>
              <td className="hidden px-4 py-2.5 text-xs text-slate-500 md:table-cell">{transferSource(t, admin)}</td>
              <td className="px-4 py-2.5 text-xs whitespace-nowrap text-slate-500" title={formatDate(t.finished_at ?? t.started_at)}>
                {timeAgo(t.finished_at ?? t.started_at)}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
