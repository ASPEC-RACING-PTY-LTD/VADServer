import { Link } from 'react-router-dom'
import { HardDriveDownload } from 'lucide-react'
import { Badge, ErrorState } from '../../components/ui'
import type { DashboardWidget } from '../../lib/dashboardWidgets'
import { timeAgo } from '../../lib/format'
import { useAllJobs, useRuns } from './api'
import type { Run } from './types'
import { RunStatusBadge } from './shared'

const LIMIT = 6

/** Dashboard card: last run status per backup job and how many jobs are failing. */
export function BackupStatusWidget() {
  const jobs = useAllJobs()
  const runs = useRuns({ limit: 200 })
  const lastByJob = new Map<number, Run>()
  for (const r of runs.data?.items ?? []) if (!lastByJob.has(r.job_id)) lastByJob.set(r.job_id, r)
  const list = jobs.data ?? []
  const failing = list.filter((j) => lastByJob.get(j.id)?.status === 'failed').length
  const error = jobs.error ?? runs.error
  return (
    <div className="card flex flex-col p-6">
      <div className="mb-4 flex items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
          <HardDriveDownload className="size-5 text-brand-500" aria-hidden />
          Backups
          {jobs.data && runs.data && <Badge tone={failing ? 'red' : 'green'}>{failing ? `${failing} failing` : 'all OK'}</Badge>}
        </h2>
        <Link to="/backups" className="text-sm font-medium text-brand-600 hover:underline dark:text-brand-400">
          View all
        </Link>
      </div>
      {error ? (
        <ErrorState
          error={error}
          onRetry={() => {
            void jobs.refetch()
            void runs.refetch()
          }}
        />
      ) : jobs.isPending || runs.isPending ? (
        <p className="text-sm text-slate-500">Loading…</p>
      ) : list.length === 0 ? (
        <p className="text-sm text-slate-500 dark:text-slate-400">No backup jobs yet.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-slate-100 dark:divide-slate-800">
          {list.slice(0, LIMIT).map((j) => {
            const r = lastByJob.get(j.id)
            return (
              <li key={j.id}>
                <Link to={`/backups/jobs/${j.id}`} className="flex items-center justify-between gap-3 py-2 text-sm hover:text-brand-600">
                  <span className="min-w-0 truncate">{j.name}</span>
                  <span className="flex shrink-0 items-center gap-2 text-xs text-slate-500">
                    {r ? (
                      <>
                        <RunStatusBadge status={r.status} />
                        {timeAgo(r.started_at)}
                      </>
                    ) : (
                      'never run'
                    )}
                  </span>
                </Link>
              </li>
            )
          })}
        </ul>
      )}
    </div>
  )
}

export default { id: 'backups.status', order: 32, span: 1, component: BackupStatusWidget, requiredFeatureFlag: 'backup.v1' } satisfies DashboardWidget
