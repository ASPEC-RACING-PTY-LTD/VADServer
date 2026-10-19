import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Monitor } from 'lucide-react'
import { errorMessage, get, post } from '../../../lib/api'
import { timeAgo } from '../../../lib/format'
import { Badge, Button, EmptyState, ErrorState, PageLoader, useUI } from '../../../components/ui'

export default function DevicesTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-devices'], queryFn: () => get('/admin/devices'), refetchInterval: 15_000 })
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const tone = { pending: 'amber', approved: 'green', rejected: 'red', revoked: 'red', expired: 'slate' } as any
  return (
    <div className="card overflow-x-auto">
      {data.devices.length === 0 ? (
        <EmptyState icon={<Monitor className="size-6" />} title="No devices" />
      ) : (
        <table className="w-full text-sm">
          <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
            <tr>
              <th className="px-4 py-2.5">Device</th>
              <th className="px-4 py-2.5">Owner</th>
              <th className="px-4 py-2.5">Status</th>
              <th className="hidden px-4 py-2.5 md:table-cell">Last seen</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data.devices.map((d: any) => (
              <tr key={d.id} className="border-b border-slate-100 last:border-0 dark:border-slate-800">
                <td className="px-4 py-2.5">
                  <div className="font-medium">{d.name}</div>
                  <div className="text-xs text-slate-500">
                    {d.os} · {d.key_fingerprint}
                  </div>
                  {d.sync_state?.direct_upload?.enabled && (
                    <div className="text-xs text-sky-600 dark:text-sky-400">
                      Direct LAN uploads {d.sync_state.direct_upload.active ? 'in use' : 'on (not reachable)'}
                      {d.sync_state.direct_upload.url && ` · ${d.sync_state.direct_upload.url}`}
                    </div>
                  )}
                </td>
                <td className="px-4 py-2.5">{d.username}</td>
                <td className="px-4 py-2.5">
                  <Badge tone={tone[d.status]}>{d.status === 'pending' && d.expires_at < Date.now() ? 'expired' : d.status}</Badge>
                </td>
                <td className="hidden px-4 py-2.5 text-xs text-slate-500 md:table-cell">
                  {timeAgo(d.last_seen_at)} {d.last_ip && `· ${d.last_ip}`}
                </td>
                <td className="px-4 py-2.5 text-right whitespace-nowrap">
                  {d.status === 'approved' && d.sync_state?.direct_upload?.enabled && (
                    <Button
                      size="sm"
                      variant="ghost"
                      onClick={async () => {
                        if (!(await confirm({ title: `Turn off direct uploads on ${d.name}?`, message: 'The device goes back to uploading through the public address within a few minutes. Its owner can turn direct uploads on again in the app.', confirmLabel: 'Turn off' }))) return
                        try {
                          await post(`/admin/devices/${d.id}/direct-reset`)
                          toast('success', 'Direct uploads turned off', 'The device switches back at its next check.')
                          qc.invalidateQueries({ queryKey: ['admin-devices'] })
                        } catch (e) {
                          toast('error', 'Could not turn off direct uploads', errorMessage(e))
                        }
                      }}
                    >
                      Reset LAN uploads
                    </Button>
                  )}
                  {(d.status === 'approved' || d.status === 'pending') && (
                    <Button
                      size="sm"
                      variant="ghost"
                      className="text-rose-600"
                      onClick={async () => {
                        if (!(await confirm({ title: `Revoke ${d.name}?`, message: `The device owned by ${d.username} loses access immediately.`, confirmLabel: 'Revoke' }))) return
                        try {
                          await post(`/admin/devices/${d.id}/revoke`)
                          qc.invalidateQueries({ queryKey: ['admin-devices'] })
                        } catch (e) {
                          toast('error', 'Could not revoke', errorMessage(e))
                        }
                      }}
                    >
                      Revoke
                    </Button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  )
}
