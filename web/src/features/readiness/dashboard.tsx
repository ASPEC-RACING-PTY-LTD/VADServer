import { Link } from 'react-router-dom'
import { Gauge } from 'lucide-react'
import { ErrorState } from '../../components/ui'
import type { DashboardWidget } from '../../lib/dashboardWidgets'
import { useReadiness } from './api'
import { StatusBadge, StatusIcon } from './shared'
import { countChecks, countDevices, statusLabel } from './types'

/** Dashboard card: overall readiness score and status, devices per status and failing checks. */
export function ReadinessWidget() {
  const q = useReadiness()
  const devices = countDevices(q.data)
  const checks = countChecks(q.data)
  const total = q.data?.devices?.length ?? 0
  return (
    <div className="card flex flex-col p-6">
      <div className="mb-4 flex items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
          <Gauge className="size-5 text-brand-500" aria-hidden />
          Readiness
          {q.data && <StatusBadge status={q.data.status} />}
        </h2>
        <Link to="/readiness" className="text-sm font-medium text-brand-600 hover:underline dark:text-brand-400">
          View all
        </Link>
      </div>
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isPending ? (
        <p className="text-sm text-slate-500">Loading…</p>
      ) : total === 0 ? (
        <p className="text-sm text-slate-500 dark:text-slate-400">No approved devices to check yet.</p>
      ) : (
        <div className="flex flex-col gap-3">
          <div className="flex items-center gap-3">
            <StatusIcon status={q.data.status} className="size-7" />
            <span className="text-2xl font-semibold text-slate-900 dark:text-white" aria-label="Readiness score">
              {q.data.status === 'unknown' ? '–' : `${q.data.score}%`}
            </span>
          </div>
          <dl className="grid grid-cols-3 gap-2 text-sm">
            {(['critical', 'warning', 'ok'] as const).map((s) => (
              <div key={s}>
                <dt className="text-slate-500 dark:text-slate-400">{statusLabel(s)}</dt>
                <dd className="font-semibold text-slate-900 dark:text-white" data-testid={`readiness-devices-${s}`}>
                  {devices[s]}
                </dd>
              </div>
            ))}
          </dl>
          <p className="text-xs text-slate-500 dark:text-slate-400">
            {total} {total === 1 ? 'device' : 'devices'} · {checks.critical} critical and {checks.warning} warning {checks.critical + checks.warning === 1 ? 'check' : 'checks'}
          </p>
        </div>
      )}
    </div>
  )
}

export default { id: 'readiness.overview', order: 31, span: 1, component: ReadinessWidget, requiredFeatureFlag: 'backup.v1' } satisfies DashboardWidget
