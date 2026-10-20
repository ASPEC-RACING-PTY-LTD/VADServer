import type { ReactNode } from 'react'
import { Link, useParams } from 'react-router-dom'
import { HardDriveDownload } from 'lucide-react'
import { ErrorState, PageHeader, PageLoader } from '../../components/ui'
import { formatBytes, formatCount, formatDate, formatDurationMs } from '../../lib/format'
import { useRun } from './api'
import { isRunning } from './types'
import { RunProgress, RunStatusBadge } from './shared'
import { IssuesPanel } from './IssuesPanel'

/** `/backups/runs/:id`: one run with live progress and its issues. */
export default function RunPage() {
  const { id = '' } = useParams()
  const runId = /^\d+$/.test(id) ? Number(id) : 0
  const q = useRun(runId)
  if (!runId) return <ErrorState error={new Error('Unknown backup run.')} />
  if (q.error) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />
  if (q.isPending) return <PageLoader />
  const r = q.data
  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={`Run #${r.id}`}
        description={
          <>
            <Link to="/sync/backups" className="hover:underline">
              Backups
            </Link>{' '}
            ·{' '}
            <Link to={`/backups/jobs/${r.job_id}`} className="hover:underline">
              Job #{r.job_id}
            </Link>
          </>
        }
        icon={<HardDriveDownload className="size-5" />}
        actions={<RunStatusBadge status={r.status} />}
      />
      {isRunning(r) && (
        <div className="card p-5">
          <RunProgress run={r} title="Progress" />
        </div>
      )}
      <div className="card grid grid-cols-1 gap-x-6 gap-y-3 p-5 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <Item label="Trigger">{r.trigger}</Item>
        <Item label="Started">{formatDate(r.started_at)}</Item>
        <Item label="Finished">{r.finished_at ? formatDate(r.finished_at) : '-'}</Item>
        <Item label="Duration">{r.finished_at ? formatDurationMs(r.finished_at - r.started_at) : '-'}</Item>
        <Item label="Files">
          {formatCount(r.files_done)} of {formatCount(r.files_total)}
        </Item>
        <Item label="Data">
          {formatBytes(r.bytes_done)} of {formatBytes(r.bytes_total)}
        </Item>
        <Item label="New / uploaded">
          {formatBytes(r.bytes_new)} / {formatBytes(r.bytes_uploaded)}
        </Item>
        <Item label="Warnings / errors">
          {r.warnings} / {r.errors}
        </Item>
        <Item label="Snapshot">{r.snapshot_id ?? '-'}</Item>
      </div>
      <IssuesPanel runId={r.id} />
    </div>
  )
}

function Item({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex flex-col">
      <span className="text-xs text-slate-500 dark:text-slate-400">{label}</span>
      <div className="text-slate-900 dark:text-slate-100">{children}</div>
    </div>
  )
}
