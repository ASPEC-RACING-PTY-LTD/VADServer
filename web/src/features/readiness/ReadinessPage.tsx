import { Link, useSearchParams } from 'react-router-dom'
import { Gauge, HardDriveDownload, Monitor } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, PageHeader, PageLoader, cx } from '../../components/ui'
import { useAuth } from '../../lib/auth'
import { formatDate, pluralize, timeAgo } from '../../lib/format'
import { useAllJobs, useDevices } from '../backups/api'
import { useReadiness } from './api'
import { StatusBadge, StatusIcon } from './shared'
import {
  INFORMATIONAL_CHECKS,
  checkLabel,
  countDevices,
  deviceStatus,
  safeFixLink,
  sortChecks,
  statusLabel,
  statusSeverity,
  type ReadinessCheck,
  type ReadinessDevice,
  type ReadinessJob,
} from './types'

const linkClass = 'font-medium text-brand-600 hover:underline dark:text-brand-400'

function useIsAdmin(): boolean {
  const { state } = useAuth()
  return state.status === 'authenticated' && state.user.role === 'admin'
}

/** `/readiness`: per-device checks (worst first) and per-job freshness, filterable by `?device=` and `?scope=all` (admins). */
export default function ReadinessPage() {
  const [params, setParams] = useSearchParams()
  const isAdmin = useIsAdmin()
  const device = Number(params.get('device')) || undefined
  const scope = isAdmin && params.get('scope') === 'all' ? 'all' : 'mine'
  const q = useReadiness({ device, scope })
  const devices = useDevices()
  const jobs = useAllJobs()
  const deviceName = new Map((devices.data ?? []).map((d) => [d.id, d.name]))
  const jobName = new Map((jobs.data ?? []).map((j) => [j.id, j.name]))

  const setParam = (k: string, v: string | null) => {
    const next = new URLSearchParams(params)
    if (v) next.set(k, v)
    else next.delete(k)
    setParams(next, { replace: true })
  }

  const list = [...(q.data?.devices ?? [])].sort((a, b) => statusSeverity(deviceStatus(b)) - statusSeverity(deviceStatus(a)) || a.device_id - b.device_id)
  const counts = countDevices(q.data)

  return (
    <div className="flex flex-col gap-6">
      <PageHeader
        title="Readiness"
        description="Can each device be restored right now? Problems are listed worst first, with a link to fix them."
        icon={<Gauge className="size-6" />}
        actions={
          <div className="flex flex-wrap items-center gap-3">
            {device ? (
              <Button size="sm" onClick={() => setParam('device', null)}>
                Show all devices
              </Button>
            ) : null}
            {isAdmin && !device && (
              <label className="flex items-center gap-2 text-sm text-slate-600 dark:text-slate-300">
                <input type="checkbox" checked={scope === 'all'} onChange={(e) => setParam('scope', e.target.checked ? 'all' : null)} />
                All users
              </label>
            )}
          </div>
        }
      />
      {q.error ? (
        <ErrorState error={q.error} onRetry={() => void q.refetch()} />
      ) : q.isPending ? (
        <PageLoader label="Loading readiness" />
      ) : (
        <>
          <section aria-label="Overall readiness" className="card flex flex-wrap items-center gap-6 p-6">
            <div className="flex items-center gap-3">
              <StatusIcon status={q.data.status} className="size-8" />
              <div>
                <div className="text-3xl font-semibold text-slate-900 dark:text-white" data-testid="readiness-score">
                  {q.data.status === 'unknown' ? '–' : `${q.data.score}%`}
                </div>
                <StatusBadge status={q.data.status} />
              </div>
            </div>
            <dl className="flex flex-wrap gap-6 text-sm">
              {(['critical', 'warning', 'ok', 'unknown'] as const).map((s) => (
                <div key={s}>
                  <dt className="text-slate-500 dark:text-slate-400">{statusLabel(s)}</dt>
                  <dd className="text-lg font-semibold text-slate-900 dark:text-white">{pluralize(counts[s], 'device')}</dd>
                </div>
              ))}
            </dl>
          </section>
          {list.length === 0 ? (
            <EmptyState
              icon={<Monitor className="size-6" />}
              title="No devices to check"
              description="Readiness is reported for approved devices. Approve a device and create a backup job to see it here."
              action={
                <Link to="/devices" className={linkClass}>
                  Open Devices
                </Link>
              }
            />
          ) : (
            list.map((d) => <DeviceCard key={d.device_id} device={d} name={deviceName.get(d.device_id)} jobName={jobName} />)
          )}
        </>
      )}
    </div>
  )
}

function DeviceCard({ device, name, jobName }: { device: ReadinessDevice; name?: string; jobName: Map<number, string> }) {
  const status = deviceStatus(device)
  const title = name ?? `Device #${device.device_id}`
  const checks = sortChecks(device.checks)
  const jobs = device.jobs ?? []
  return (
    <section aria-label={title} className="card p-6">
      <div className="mb-4 flex flex-wrap items-center justify-between gap-2">
        <h2 className="flex items-center gap-2 font-semibold text-slate-900 dark:text-white">
          <StatusIcon status={status} />
          {title}
          <StatusBadge status={status} />
        </h2>
        <div className="flex gap-3 text-sm">
          <Link to={`/backups?device=${device.device_id}`} className={linkClass}>
            Backups
          </Link>
          <Link to={`/devices?device=${device.device_id}`} className={linkClass}>
            Device
          </Link>
        </div>
      </div>
      {checks.length === 0 ? (
        <p className="text-sm text-slate-500">No checks reported.</p>
      ) : (
        <ul className="flex flex-col divide-y divide-slate-100 dark:divide-slate-800">
          {checks.map((c, i) => (
            <CheckRow key={`${c.id}-${i}`} check={c} />
          ))}
        </ul>
      )}
      {jobs.length > 0 && <JobsTable jobs={jobs} jobName={jobName} />}
    </section>
  )
}

function CheckRow({ check }: { check: ReadinessCheck }) {
  const info = INFORMATIONAL_CHECKS.has(check.id)
  const fix = safeFixLink(check.fix_link)
  const label = checkLabel(check.id)
  return (
    <li className="flex items-start justify-between gap-3 py-2 text-sm">
      <div className="flex min-w-0 items-start gap-2">
        <StatusIcon status={info ? 'unknown' : check.status} className="mt-0.5 size-4 shrink-0" />
        <div className="min-w-0">
          <div className="font-medium text-slate-900 dark:text-white">{label}</div>
          {check.message && <div className="text-slate-600 dark:text-slate-400">{check.message}</div>}
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-2">
        {info ? <Badge tone="slate">Info</Badge> : <StatusBadge status={check.status} />}
        {fix && !info && check.status !== 'ok' && (
          <Link to={fix} className={linkClass} aria-label={`Fix: ${label}`}>
            Fix
          </Link>
        )}
      </div>
    </li>
  )
}

function when(ms: number): string {
  return ms > 0 ? timeAgo(ms) : 'never'
}

function JobsTable({ jobs, jobName }: { jobs: ReadinessJob[]; jobName: Map<number, string> }) {
  return (
    <div className="mt-4 overflow-x-auto">
      <table className="w-full text-left text-sm">
        <caption className="sr-only">Jobs</caption>
        <thead className="text-xs text-slate-500 uppercase">
          <tr>
            <th className="py-2 pr-4">Job</th>
            <th className="py-2 pr-4">Last success</th>
            <th className="py-2 pr-4">Open issues</th>
            <th className="py-2 pr-4">Verified</th>
            <th className="py-2 pr-4">Test restore</th>
            <th className="py-2">VSS</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
          {jobs.map((j) => (
            <tr key={j.job_id}>
              <td className="py-2 pr-4">
                <Link to={`/backups/jobs/${j.job_id}`} className={cx(linkClass, 'inline-flex items-center gap-1')}>
                  <HardDriveDownload className="size-4" aria-hidden />
                  {jobName.get(j.job_id) ?? `Job #${j.job_id}`}
                </Link>
              </td>
              <td className="py-2 pr-4" title={j.last_success_at ? formatDate(j.last_success_at) : undefined}>
                {when(j.last_success_at)}
              </td>
              <td className="py-2 pr-4">{j.open_issues > 0 ? <Badge tone="amber">{j.open_issues}</Badge> : '0'}</td>
              <td className="py-2 pr-4">{when(j.verified_at)}</td>
              <td className="py-2 pr-4">{when(j.test_restored_at)}</td>
              <td className="py-2">{j.vss_used ? 'Yes' : 'No'}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}
