import { Link } from 'react-router-dom'
import { Badge, Button, useUI } from '../../components/ui'
import { JobProgress } from '../../components/JobProgress'
import { jobErrorView, useRunNow, useUpdateJob, type JobErrorView } from './api'
import { progressState, runTone, type Job, type Run } from './types'

const stateLabels: Record<string, string> = { active: 'Active', suspended_by_mode: 'Suspended (mode)', suspended_by_profile: 'Suspended (profile)' }

export function JobStateBadge({ job }: { job: Pick<Job, 'state' | 'enabled'> }) {
  if (job.state === 'active') return job.enabled ? <Badge tone="green">Active</Badge> : <Badge tone="slate">Paused</Badge>
  return <Badge tone="amber">{stateLabels[job.state] ?? job.state}</Badge>
}

/** Why a job is suspended, or null when it is active. */
export function suspensionReason(job: Pick<Job, 'state' | 'required_modules' | 'profile_sid'>): string | null {
  if (job.state === 'suspended_by_mode') {
    const mods = job.required_modules ?? []
    return `The device lacks ${mods.length ? `the module${mods.length > 1 ? 's' : ''} ${mods.join(', ')}` : 'a module this job needs'}. Switch the device to Advanced mode or enable the module.`
  }
  if (job.state === 'suspended_by_profile') {
    return `The Windows profile ${job.profile_sid || '(active user)'} is not authorised for backup (blacklisted or consent missing).`
  }
  return null
}

export function RunStatusBadge({ status }: { status: string }) {
  return <Badge tone={runTone(status)}>{status}</Badge>
}

export function RunProgress({ run, title }: { run: Run; title?: string }) {
  return (
    <JobProgress
      title={title}
      state={progressState(run.status)}
      phase={run.phase || run.status}
      bytesDone={run.bytes_done}
      bytesTotal={run.bytes_total || undefined}
      filesDone={run.files_done}
      filesTotal={run.files_total || undefined}
      etaSec={run.eta_ms > 0 ? run.eta_ms / 1000 : undefined}
    />
  )
}

/** Error banner for job writes and gating errors, with a Devices link and a Reload button on conflict. */
export function JobErrorBanner({ view, onReload }: { view: JobErrorView; onReload?: () => void }) {
  return (
    <div role="alert" className="flex flex-col gap-2 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-950/40 dark:text-rose-300">
      <p>{view.message}</p>
      {(view.devicesLink || (view.conflict && onReload)) && (
        <div className="flex flex-wrap items-center gap-2">
          {view.devicesLink && (
            <Link to="/devices" className="font-medium underline">
              Open Devices
            </Link>
          )}
          {view.conflict && onReload && (
            <Button size="sm" onClick={onReload}>
              Reload
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

/** Pause/resume, re-check and run-now actions for one job. Errors go to `onError` (or a toast). */
export function useJobActions(onError?: (v: JobErrorView) => void) {
  const { toast } = useUI()
  const update = useUpdateJob()
  const runNow = useRunNow()
  const fail = (e: unknown) => {
    const v = jobErrorView(e)
    if (onError) onError(v)
    else toast('error', 'Action failed', v.message)
  }
  return {
    busy: update.isPending || runNow.isPending,
    setEnabled: async (job: Job, enabled: boolean) => {
      try {
        await update.mutateAsync({ id: job.id, revision: job.revision, body: { enabled } })
        toast('success', enabled ? 'Job enabled' : 'Job paused')
      } catch (e) {
        fail(e)
      }
    },
    /** Re-check a suspended job: PATCH enabled:true makes the server re-evaluate its gates. */
    recheck: async (job: Job) => {
      try {
        const j = await update.mutateAsync({ id: job.id, revision: job.revision, body: { enabled: true } })
        toast(j.state === 'active' ? 'success' : 'warning', j.state === 'active' ? 'Job is active again' : 'Job is still suspended')
      } catch (e) {
        fail(e)
      }
    },
    runNow: async (job: Job) => {
      try {
        await runNow.mutateAsync({ id: job.id, device_id: job.device_id })
        toast('success', 'Backup requested', 'The device starts the run when it next checks in.')
      } catch (e) {
        fail(e)
      }
    },
  }
}

export const canRunNow = (j: Pick<Job, 'enabled' | 'state'>) => j.enabled && j.state === 'active'

export function JobActions({ job, actions }: { job: Job; actions: ReturnType<typeof useJobActions> }) {
  const suspended = job.state !== 'active'
  return (
    <div className="flex flex-wrap justify-end gap-1.5" onClick={(e) => e.stopPropagation()}>
      {suspended ? (
        <Button size="sm" disabled={actions.busy} onClick={() => void actions.recheck(job)}>
          Re-check
        </Button>
      ) : (
        <Button size="sm" disabled={actions.busy} onClick={() => void actions.setEnabled(job, !job.enabled)}>
          {job.enabled ? 'Pause' : 'Resume'}
        </Button>
      )}
      {canRunNow(job) && (
        <Button size="sm" variant="primary" disabled={actions.busy} onClick={() => void actions.runNow(job)}>
          Run now
        </Button>
      )}
    </div>
  )
}
