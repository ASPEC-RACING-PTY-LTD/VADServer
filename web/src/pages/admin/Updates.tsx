import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ArrowUpCircle, CalendarClock, CheckCircle2, Copy, ExternalLink, RefreshCw } from 'lucide-react'
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
}

export function useUpdateStatus() {
  return useQuery<UpdateStatus>({ queryKey: ['admin-update'], queryFn: () => get('/admin/update'), refetchInterval: 10 * 60_000 })
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
          You are running {data.current}. {data.auto_update === 'on' ? 'It will be installed automatically at the next scheduled update.' : 'Upgrade from the server with one command.'}
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
  const { toast } = useUI()
  const { data, isLoading, error, refetch } = useUpdateStatus()
  const [checking, setChecking] = useState(false)
  if (isLoading) return <PageLoader />
  if (error || !data) return <ErrorState error={error} onRetry={refetch} />

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

      <div className="grid gap-6 xl:grid-cols-2">
        <div className="card p-6">
          <h3 className="flex items-center gap-2 font-semibold">
            <ArrowUpCircle className="size-4" /> Upgrade now
          </h3>
          <p className="mt-2 mb-4 text-sm text-slate-500">
            Run this on the server. The installer backs up the database, configuration and application first, and rolls back automatically if the new version does not become healthy.
          </p>
          <CommandLine command={data.upgrade_command} />
        </div>
        <div className="card p-6">
          <h3 className="flex items-center gap-2 font-semibold">
            <CalendarClock className="size-4" /> Automatic updates <Badge tone={data.auto_update === 'on' ? 'green' : undefined}>{data.auto_update === 'on' ? 'on' : 'off'}</Badge>
          </h3>
          <p className="mt-2 mb-4 text-sm text-slate-500">
            {data.auto_update === 'on'
              ? 'The server checks every night at 04:00 and installs new releases with the same backup and rollback protection. Turn it off with:'
              : 'Let the server install new releases on its own every night at 04:00, with the same backup and rollback protection. Turn it on with:'}
          </p>
          <CommandLine command={data.auto_update === 'on' ? '~/VaultDock/app/install.sh auto-update off' : '~/VaultDock/app/install.sh auto-update on'} />
        </div>
      </div>
      <p className="text-xs text-slate-400">Desktop apps update themselves from the same releases ({data.repo}).</p>
    </div>
  )
}
