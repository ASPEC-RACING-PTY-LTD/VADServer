import { Link } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'
import { Badge, ErrorState } from '../../components/ui'
import type { DashboardWidget } from '../../lib/dashboardWidgets'
import { timeAgo } from '../../lib/format'
import { usePendingApprovals } from './api'
import { kindLabel } from './types'

const LIMIT = 5

/** Dashboard card: the newest pending approvals with a link to the inbox. */
export function PendingApprovalsWidget() {
  const q = usePendingApprovals(LIMIT)
  const items = q.data?.items ?? []
  const more = !!q.data?.next_cursor
  return (
    <div className="card flex flex-col p-6">
      <div className="mb-4 flex items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
          <ShieldCheck className="size-5 text-amber-500" aria-hidden />
          Pending approvals
          {q.data && <Badge tone={items.length ? 'amber' : 'slate'}>{more ? `${items.length}+` : items.length}</Badge>}
        </h2>
        <Link to="/approvals" className="text-sm font-medium text-brand-600 hover:underline dark:text-brand-400">
          View all
        </Link>
      </div>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isPending ? (
        <p className="text-sm text-slate-500">Loading…</p>
      ) : items.length === 0 ? (
        <p className="text-sm text-slate-500 dark:text-slate-400">Nothing is waiting for your approval.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-slate-100 dark:divide-slate-800">
          {items.map((a) => (
            <li key={a.id}>
              <Link to={`/approvals/${a.id}`} className="flex items-center justify-between gap-3 py-2 text-sm hover:text-brand-600">
                <span className="min-w-0 truncate">{a.summary || kindLabel(a.kind)}</span>
                <span className="flex shrink-0 items-center gap-2 text-xs text-slate-500">
                  {a.risk === 'high' && <Badge tone="red">high</Badge>}
                  expires {timeAgo(a.expires_at)}
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}

export default { id: 'approvals.pending', order: 35, span: 1, component: PendingApprovalsWidget, requiredFeatureFlag: 'approvals.v1' } satisfies DashboardWidget
