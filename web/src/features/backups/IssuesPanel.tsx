import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { Badge, Button, EmptyState, useUI } from '../../components/ui'
import { DataTable, type Column } from '../../components/data/DataTable'
import { infiniteTableProps } from '../../components/data/infinite'
import { formatCount, timeAgo } from '../../lib/format'
import { jobErrorView, useIssues, useRetrySkipped, useSkipIssues, type JobErrorView } from './api'
import { ISSUE_CLASSES, type Issue } from './types'
import { JobErrorBanner } from './shared'

const columns: Column<Issue>[] = [
  { id: 'path', header: 'Path', cell: (i) => <span className="font-mono text-xs break-all">{i.path}</span>, sortValue: (i) => i.path },
  { id: 'class', header: 'Class', cell: (i) => i.class, sortValue: (i) => i.class },
  { id: 'severity', header: 'Severity', cell: (i) => <Badge tone={i.severity === 'error' ? 'red' : i.severity === 'skipped' ? 'amber' : 'slate'}>{i.severity}</Badge>, sortValue: (i) => i.severity },
  { id: 'resolution', header: 'Resolution', cell: (i) => i.resolution, sortValue: (i) => i.resolution },
  { id: 'count', header: 'Seen', cell: (i) => `${formatCount(i.count)}×, ${timeAgo(i.last_seen)}`, sortValue: (i) => i.last_seen, align: 'right' },
]

/** Issues of one run: filter by class, skip selected or a whole class, and retry skipped items. */
export function IssuesPanel({ runId }: { runId: number }) {
  const { toast } = useUI()
  const [cls, setCls] = useState('')
  const [err, setErr] = useState<JobErrorView | null>(null)
  const q = useIssues(runId, cls ? { class: cls } : {})
  const skip = useSkipIssues(runId)
  const retry = useRetrySkipped(runId)

  async function doSkip(body: { ids: number[] } | { class: string }, clear?: () => void) {
    setErr(null)
    try {
      const r = await skip.mutateAsync(body)
      toast(r.failed?.length ? 'warning' : 'success', `Skipped ${formatCount(r.ok_count)} item${r.ok_count === 1 ? '' : 's'}`, r.failed?.length ? `${r.failed.length} could not be skipped.` : undefined)
      clear?.()
    } catch (e) {
      setErr(jobErrorView(e))
    }
  }

  async function doRetry() {
    setErr(null)
    try {
      await retry.mutateAsync()
      toast('success', 'Retry requested', 'The device retries the skipped items on its next check-in.')
    } catch (e) {
      setErr(jobErrorView(e))
    }
  }

  return (
    <section className="card flex flex-col gap-3 p-5" aria-label="Issues">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h2 className="font-semibold text-slate-900 dark:text-white">Issues</h2>
        <div className="flex flex-wrap items-center gap-2">
          <select className="input w-auto py-1.5" aria-label="Issue class" value={cls} onChange={(e) => setCls(e.target.value)}>
            <option value="">All classes</option>
            {ISSUE_CLASSES.map((c) => (
              <option key={c} value={c}>
                {c}
              </option>
            ))}
          </select>
          {cls && (
            <Button size="sm" disabled={skip.isPending} onClick={() => void doSkip({ class: cls })}>
              Skip all {cls}
            </Button>
          )}
          <Button size="sm" variant="primary" loading={retry.isPending} onClick={() => void doRetry()}>
            Retry skipped
          </Button>
        </div>
      </div>
      {err && <JobErrorBanner view={err} />}
      <DataTable
        {...infiniteTableProps(q)}
        columns={columns}
        rowKey={(i) => i.id}
        label="Issues"
        dense
        selectable
        isRowSelectable={(i) => i.resolution === 'open'}
        bulkActions={(rows, clear) => (
          <Button size="sm" disabled={skip.isPending} onClick={() => void doSkip({ ids: rows.map((r) => r.id) }, clear)}>
            Skip selected
          </Button>
        )}
        empty={<EmptyState icon={<AlertTriangle className="size-6" />} title="No issues" />}
      />
    </section>
  )
}
