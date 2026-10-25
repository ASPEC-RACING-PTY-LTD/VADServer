import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUpCircle, CalendarClock, CheckCircle2, Copy, Download, ExternalLink, RefreshCw, XCircle } from 'lucide-react'
import { errorMessage, get, post } from '../../lib/api'
import { formatDate, timeAgo } from '../../lib/format'
import { Badge, Button, ErrorState, PageLoader, useUI } from '../../components/ui'

export type UpdateStatus = {
  enabled: boolean
  repo: string
  current: string
  latest?: string
  available: boolean
  url?: string
  notes?: string
  published_at?: string
  checked_at?: string
  error?: string
  auto_update: 'on' | 'off'
  upgrade_command: string
  /** The installer's control job runs on the host, so the buttons work. */
  helper: boolean
  helper_seen_at?: number
  job?: ControlJob
}

export type ControlJob = {
  id: string
  action: 'upgrade' | 'auto-update-on' | 'auto-update-off'
  state: 'queued' | 'running' | 'done' | 'failed'
  started_at?: number
  finished_at?: number
  exit_code: number
  log?: string
}

const jobActive = (j?: ControlJob) => j?.state === 'queued' || j?.state === 'running'

export function useUpdateStatus() {
  return useQuery<UpdateStatus>({
    queryKey: ['admin-update'],
    queryFn: () => get('/admin/update'),
    // Follow a running request closely; the server restarts during an upgrade.
    refetchInterval: (q) => (jobActive(q.state.data?.job) || q.state.error ? 3000 : 10 * 60_000),
  })
}

const actionLabel: Record<string, string> = { upgrade: 'Upgrade', 'auto-update-on': 'Turning automatic updates on', 'auto-update-off': 'Turning automatic updates off' }

function JobCard({ job, current }: { job: ControlJob; current: string }) {
  const active = jobActive(job)
  const label = actionLabel[job.action] ?? job.action
  let title: string
  if (job.state === 'queued') title = `${label}: waiting for the server's update helper (up to a minute)`
  else if (job.state === 'running') title = `${label}: in progress${job.action === 'upgrade' ? '. VaultDock restarts during the upgrade; this page reconnects by itself.' : ''}`
  else if (job.state === 'done') title = job.action === 'upgrade' ? `Upgrade finished: running ${current}` : job.action === 'auto-update-on' ? 'Automatic updates are on' : 'Automatic updates are off'
  else title = `${label} failed (exit code ${job.exit_code})${job.action === 'upgrade' ? '. The installer rolled back to the previous version.' : ''}`
  return (
    <div className="card p-6">
      <div className="flex flex-wrap items-center gap-3">
        {active ? <RefreshCw className="size-5 animate-spin text-brand-600" /> : job.state === 'done' ? <CheckCircle2 className="size-5 text-emerald-600" /> : <XCircle className="size-5 text-rose-600" />}
        <div className="min-w-0 flex-1 font-medium">{title}</div>
        {job.started_at ? <span className="text-xs text-slate-500">{timeAgo(job.started_at * 1000)}</span> : null}
        {job.state === 'done' && job.action === 'upgrade' && (
          <Button size="sm" onClick={() => window.location.reload()}>
            Reload page
          </Button>
        )}
      </div>
      {job.log && <pre className="mt-3 max-h-64 overflow-auto rounded-lg bg-slate-900 p-3 font-mono text-xs whitespace-pre-wrap text-slate-100">{job.log}</pre>}
    </div>
  )
}

/** Shown while the host's update helper is not running. */
function HelperMissing({ data }: { data: UpdateStatus }) {
  return (
    <div className="space-y-2">
      <p className="text-sm text-amber-700 dark:text-amber-300">
        {data.helper_seen_at
          ? `The server's update helper last checked in ${timeAgo(data.helper_seen_at * 1000)}. It runs from cron on the server; check that cron is running.`
          : "One-time setup: this server has no update helper yet, and a container cannot install one on its own host. Run this once on the server. It upgrades VaultDock and sets up the helper; from then on, Upgrade and Automatic updates work from this page."}
      </p>
      <CommandLine command={data.upgrade_command} />
    </div>
  )
}

function CommandLine({ command }: { command: string }) {
  const { toast } = useUI()
  return (
    <div className="flex items-center gap-2 rounded-lg bg-slate-900 py-1.5 pr-1.5 pl-3 font-mono text-xs text-slate-100">
      <span className="text-brand-400 select-none">$</span>
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-nowrap">{command}</code>
      <button
        type="button"
        className="rounded-md p-1.5 text-slate-400 hover:bg-slate-800 hover:text-white"
        aria-label="Copy command"
        onClick={async () => {
          try {
            await navigator.clipboard.writeText(command)
            toast('success', 'Copied')
          } catch {
            toast('error', 'Copy failed', 'Select the command and copy it manually.')
          }
        }}
      >
        <Copy className="size-3.5" />
      </button>
    </div>
  )
}

/** Shown on the admin overview when a newer release exists. */
export function UpdateBanner() {
  const { data } = useUpdateStatus()
  if (!data?.available) return null
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-2xl bg-brand-50 p-4 text-brand-900 dark:bg-brand-500/10 dark:text-brand-100">
      <ArrowUpCircle className="size-6 shrink-0" />
      <div className="min-w-0 flex-1">
        <div className="font-semibold">VaultDock {data.latest} is available</div>
        <div className="text-sm opacity-80">
          You are running {data.current}. {data.auto_update === 'on' ? 'It will be installed automatically at the next scheduled update, or install it now.' : 'Install it from the Updates page.'}
        </div>
      </div>
      <Link to="/admin/updates" className="text-sm font-medium underline-offset-4 hover:underline">
        View update
      </Link>
    </div>
  )
}

export function UpdatesTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useUpdateStatus()
  const [checking, setChecking] = useState(false)
  const [sending, setSending] = useState<string | null>(null)
  if (isLoading) return <PageLoader />
  // Keep the last status while the server restarts during an upgrade.
  if (!data) return <ErrorState error={error} onRetry={refetch} />
  const busy = jobActive(data.job) || sending !== null

  const request = async (key: string, path: string, body?: unknown) => {
    setSending(key)
    try {
      const s = await post<UpdateStatus>(path, body)
      qc.setQueryData(['admin-update'], s)
    } catch (e) {
      toast('error', 'The request was not sent', errorMessage(e))
    } finally {
      setSending(null)
    }
  }
  const install = async () => {
    if (
      !(await confirm({
        title: `Install VaultDock ${data.latest}?`,
        message:
          'The server backs up the database, configuration and application, installs the new version and restarts (usually under a minute of downtime). If the new version does not become healthy it rolls back automatically.',
        confirmLabel: 'Install update',
      }))
    )
      return
    await request('install', '/admin/update/install')
  }

  const checkNow = async () => {
    setChecking(true)
    try {
      const s = await post<UpdateStatus>('/admin/update/check')
      qc.setQueryData(['admin-update'], s)
      if (s.error) toast('error', 'Update check failed', s.error)
      else toast('success', s.available ? `VaultDock ${s.latest} is available` : 'VaultDock is up to date')
    } catch (e) {
      toast('error', 'Update check failed', errorMessage(e))
    } finally {
      setChecking(false)
    }
  }

  return (
    <div className="space-y-6">
      <div className="card flex flex-wrap items-center gap-4 p-6">
        {data.available ? <ArrowUpCircle className="size-8 text-brand-600" /> : <CheckCircle2 className="size-8 text-emerald-600" />}
        <div className="min-w-0 flex-1">
          <div className="text-lg font-semibold">{data.available ? `VaultDock ${data.latest} is available` : 'VaultDock is up to date'}</div>
          <div className="text-sm text-slate-500">
            Running {data.current}
            {data.latest && !data.available && data.latest !== data.current && ` · latest release ${data.latest}`}
            {data.checked_at ? ` · checked ${timeAgo(Date.parse(data.checked_at))}` : data.enabled ? ' · first check runs shortly after start' : ''}
          </div>
          {data.error && <div className="mt-1 text-sm text-red-600">{data.error}</div>}
          {!data.enabled && <div className="mt-1 text-sm text-slate-500">Release checks are turned off on this server (VAULTDOCK_UPDATE_CHECK=false).</div>}
        </div>
        {data.enabled && (
          <Button loading={checking} onClick={checkNow} icon={<RefreshCw className="size-4" />}>
            Check now
          </Button>
        )}
      </div>

      {data.available && (
        <div className="card p-6">
          <div className="mb-3 flex flex-wrap items-center gap-2">
            <h3 className="font-semibold">What's new in {data.latest}</h3>
            {data.published_at && <Badge>{formatDate(Date.parse(data.published_at))}</Badge>}
            {data.url && (
              <a href={data.url} target="_blank" rel="noopener noreferrer" className="ml-auto inline-flex items-center gap-1 text-sm text-brand-600 hover:underline">
                Release page <ExternalLink className="size-3.5" />
              </a>
            )}
          </div>
          <pre className="max-h-80 overflow-auto rounded-lg bg-slate-50 p-4 font-sans text-sm whitespace-pre-wrap text-slate-700 dark:bg-slate-900 dark:text-slate-300">{data.notes || 'No release notes.'}</pre>
        </div>
      )}

      {data.job && <JobCard job={data.job} current={data.current} />}

      <div className="grid gap-6 xl:grid-cols-2">
        <div className="card min-w-0 p-6">
          <h3 className="flex items-center gap-2 font-semibold">
            <ArrowUpCircle className="size-4" /> Upgrade now
          </h3>
          <p className="mt-2 mb-4 text-sm text-slate-500">
            The server backs up the database, configuration and application first, and rolls back automatically if the new version does not become healthy.
          </p>
          {data.helper ? (
            <Button variant="primary" icon={<Download className="size-4" />} loading={sending === 'install'} disabled={!data.available || busy} onClick={install}>
              {data.available ? `Install VaultDock ${data.latest}` : 'VaultDock is up to date'}
            </Button>
          ) : (
            <HelperMissing data={data} />
          )}
        </div>
        <div className="card min-w-0 p-6">
          <h3 className="flex items-center gap-2 font-semibold">
            <CalendarClock className="size-4" /> Automatic updates <Badge tone={data.auto_update === 'on' ? 'green' : undefined}>{data.auto_update === 'on' ? 'on' : 'off'}</Badge>
          </h3>
          <p className="mt-2 mb-4 text-sm text-slate-500">
            {data.auto_update === 'on'
              ? 'The server checks every night at 04:00 and installs new releases with the same backup and rollback protection.'
              : 'Let the server install new releases on its own every night at 04:00, with the same backup and rollback protection.'}
          </p>
          {data.helper ? (
            <Button
              variant={data.auto_update === 'on' ? 'secondary' : 'primary'}
              icon={<CalendarClock className="size-4" />}
              loading={sending === 'auto'}
              disabled={busy}
              onClick={() => request('auto', '/admin/update/auto', { enabled: data.auto_update !== 'on' })}
            >
              {data.auto_update === 'on' ? 'Turn off automatic updates' : 'Turn on automatic updates'}
            </Button>
          ) : (
            <HelperMissing data={data} />
          )}
        </div>
      </div>
      <p className="text-xs text-slate-400">Desktop apps update themselves from the same releases ({data.repo}).</p>
    </div>
  )
}
