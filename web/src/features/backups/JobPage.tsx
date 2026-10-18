import { useState, type ReactNode } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { HardDriveDownload } from 'lucide-react'
import { Button, EmptyState, ErrorState, Modal, PageHeader, PageLoader, useUI } from '../../components/ui'
import { DataTable, type Column } from '../../components/data/DataTable'
import { describeSchedule } from '../../components/ScheduleEditor'
import { formatBytes, formatDate, formatDurationMs, timeAgo } from '../../lib/format'
import { backupKeys, jobErrorView, useDeleteJob, useJob, useRuns, type JobErrorView } from './api'
import { isRunning, kindLabel, modeLabels, type Job, type JobMode, type Run } from './types'
import { JobActions, JobErrorBanner, JobStateBadge, RunProgress, RunStatusBadge, suspensionReason, useJobActions } from './shared'
import { JobEditor } from './JobEditor'
import { IssuesPanel } from './IssuesPanel'

/** `/backups/jobs/:id`: job detail, or the create wizard for `new`. */
export default function JobPage() {
  const { id = '' } = useParams()
  if (id === 'new') return <NewJob />
  return /^\d+$/.test(id) ? <JobDetail id={Number(id)} /> : <ErrorState error={new Error('Unknown backup job.')} />
}

function NewJob() {
  const navigate = useNavigate()
  const [params] = useSearchParams()
  const device = Number(params.get('device')) || undefined
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="New backup job" icon={<HardDriveDownload className="size-5" />} />
      <div className="card p-6">
        <JobEditor deviceId={device} onSaved={(j) => navigate(`/backups/jobs/${j.id}`, { replace: true })} onCancel={() => navigate('/backups')} />
      </div>
    </div>
  )
}

const runColumns: Column<Run>[] = [
  { id: 'id', header: 'Run', cell: (r) => <Link to={`/backups/runs/${r.id}`} className="font-medium text-brand-600 hover:underline dark:text-brand-400" onClick={(e) => e.stopPropagation()}>#{r.id}</Link>, sortValue: (r) => r.id },
  { id: 'status', header: 'Status', cell: (r) => (isRunning(r) ? <RunProgress run={r} /> : <RunStatusBadge status={r.status} />), sortValue: (r) => r.status },
  { id: 'trigger', header: 'Trigger', cell: (r) => r.trigger },
  { id: 'started', header: 'Started', cell: (r) => <span title={formatDate(r.started_at)}>{timeAgo(r.started_at)}</span>, sortValue: (r) => r.started_at },
  { id: 'duration', header: 'Duration', cell: (r) => (r.finished_at ? formatDurationMs(r.finished_at - r.started_at) : '-') },
  { id: 'new', header: 'New data', cell: (r) => formatBytes(r.bytes_new), sortValue: (r) => r.bytes_new, align: 'right' },
  { id: 'issues', header: 'Warnings / errors', cell: (r) => `${r.warnings} / ${r.errors}`, sortValue: (r) => r.errors, align: 'right' },
]

function JobDetail({ id }: { id: number }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { confirm, toast } = useUI()
  const q = useJob(id)
  const runs = useRuns({ job: id })
  const [editing, setEditing] = useState(false)
  const [err, setErr] = useState<JobErrorView | null>(null)
  const actions = useJobActions(setErr)
  const del = useDeleteJob()

  if (q.error) return <ErrorState error={q.error} onRetry={() => void q.refetch()} />
  if (q.isPending) return <PageLoader />
  const job: Job = q.data
  const why = suspensionReason(job)
  const items = runs.data?.items ?? []
  const issueRun = items.find((r) => !isRunning(r) && (r.warnings > 0 || r.errors > 0)) ?? items.find((r) => !isRunning(r))
  const last = items.find((r) => !isRunning(r))
  const lastOk = items.find((r) => r.status === 'completed')

  const reload = () => void qc.invalidateQueries({ queryKey: backupKeys.detail(id) })

  async function remove() {
    if (!(await confirm({ title: 'Delete backup job?', message: `"${job.name}" stops running. Existing snapshots are kept until retention removes them.`, confirmLabel: 'Delete' }))) return
    try {
      await del.mutateAsync(job.id)
      toast('success', 'Job deleted')
      navigate('/backups')
    } catch (e) {
      setErr(jobErrorView(e))
    }
  }

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title={job.name}
        description={
          <>
            <Link to="/backups" className="hover:underline">
              Backups
            </Link>{' '}
            · {kindLabel(job.kind)} · {modeLabels[job.mode as JobMode] ?? job.mode}
          </>
        }
        icon={<HardDriveDownload className="size-5" />}
        actions={
          <>
            <JobActions job={job} actions={actions} />
            <Button size="sm" onClick={() => setEditing(true)}>
              Edit
            </Button>
            <Button size="sm" variant="danger" loading={del.isPending} onClick={() => void remove()}>
              Delete
            </Button>
          </>
        }
      />
      {err && <JobErrorBanner view={err} onReload={reload} />}
      <div className="card grid grid-cols-1 gap-x-6 gap-y-3 p-5 text-sm sm:grid-cols-2 lg:grid-cols-4">
        <Item label="State">
          <JobStateBadge job={job} />
          {why && <p className="mt-1 text-xs text-slate-500 dark:text-slate-400">{why}</p>}
        </Item>
        <Item label="Schedule">{describeSchedule(job.schedule)}</Item>
        <Item label="Last run">{last ? <RunStatusBadge status={last.status} /> : 'Never'}</Item>
        <Item label="Last successful run">{lastOk ? timeAgo(lastOk.finished_at || lastOk.started_at) : 'Never'}</Item>
        <Item label="Paths">{(job.spec.paths ?? []).join(', ') || '-'}</Item>
        <Item label="Profile">{job.profile_sid || 'Active user'}</Item>
        <Item label="Required modules">{(job.required_modules ?? []).join(', ') || 'core only'}</Item>
        <Item label="Revision">{job.revision}</Item>
      </div>
      <section className="flex flex-col gap-3" aria-label="Recent runs">
        <h2 className="font-semibold text-slate-900 dark:text-white">Recent runs</h2>
        <DataTable
          rows={items}
          loading={runs.isPending}
          error={runs.error}
          onRetry={() => void runs.refetch()}
          columns={runColumns}
          rowKey={(r) => r.id}
          label="Recent runs"
          onRowClick={(r) => navigate(`/backups/runs/${r.id}`)}
          empty={<EmptyState icon={<HardDriveDownload className="size-6" />} title="No runs yet" />}
        />
      </section>
      {issueRun && (
        <div className="flex flex-col gap-2">
          <p className="text-sm text-slate-500 dark:text-slate-400">
            Issues from run <Link to={`/backups/runs/${issueRun.id}`} className="text-brand-600 hover:underline dark:text-brand-400">#{issueRun.id}</Link>
          </p>
          <IssuesPanel runId={issueRun.id} />
        </div>
      )}
      {editing && (
        <Modal open onClose={() => setEditing(false)} title={`Edit ${job.name}`} size="lg">
          <JobEditor
            key={job.revision}
            job={job}
            onSaved={() => {
              toast('success', 'Job saved')
              setEditing(false)
            }}
            onCancel={() => setEditing(false)}
            onReload={reload}
          />
        </Modal>
      )}
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
