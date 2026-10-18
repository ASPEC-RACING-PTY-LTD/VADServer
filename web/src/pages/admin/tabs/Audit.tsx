import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get } from '../../../lib/api'
import { formatDate, timeAgo } from '../../../lib/format'
import { describe } from '../../../lib/activity'
import { cx, ErrorState, PageLoader } from '../../../components/ui'

export default function AuditTab() {
  const [filter, setFilter] = useState('')
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-audit', filter], queryFn: () => get(`/admin/audit${filter ? `?action=${filter}` : ''}`) })
  return (
    <div>
      <div className="mb-4 flex justify-end">
        <select className="input w-56" value={filter} onChange={(e) => setFilter(e.target.value)}>
          <option value="">All events</option>
          <option value="auth.">Sign-ins</option>
          <option value="device.">Devices</option>
          <option value="admin.">Administration</option>
          <option value="account.">Account changes</option>
        </select>
      </div>
      {isLoading ? (
        <PageLoader />
      ) : error ? (
        <ErrorState error={error} onRetry={refetch} />
      ) : (
        <div className="card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
              <tr>
                <th className="px-4 py-2.5">Event</th>
                <th className="px-4 py-2.5">User</th>
                <th className="hidden px-4 py-2.5 md:table-cell">Details</th>
                <th className="px-4 py-2.5">IP</th>
                <th className="px-4 py-2.5">Time</th>
              </tr>
            </thead>
            <tbody>
              {data.items.map((a: any) => (
                <tr key={a.id} className="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
                  <td className="px-4 py-2.5">
                    <div className={cx('font-medium', a.action === 'auth.failed' && 'text-rose-600')}>{describe(a)}</div>
                    <div className="text-xs break-all text-slate-500">{a.target}</div>
                  </td>
                  <td className="px-4 py-2.5 text-xs">
                    {a.actor_name || '-'}
                    {a.username && a.username !== a.actor_name && <span className="text-slate-500"> → {a.username}</span>}
                  </td>
                  <td className="hidden max-w-xs px-4 py-2.5 text-xs break-words text-slate-500 md:table-cell">{a.details}</td>
                  <td className="px-4 py-2.5 font-mono text-xs text-slate-500">{a.ip}</td>
                  <td className="px-4 py-2.5 text-xs whitespace-nowrap text-slate-500" title={formatDate(a.created_at)}>
                    {timeAgo(a.created_at)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}
