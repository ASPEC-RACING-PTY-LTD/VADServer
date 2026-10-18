import { useState } from 'react'
import { Field, Toggle } from '../../components/ui'
import { Stepper, type StepDef } from '../../components/Stepper'
import { ListInput } from '../../components/ListInput'
import { ScheduleEditor, validateSchedule } from '../../components/ScheduleEditor'
import { jobErrorView, useCreateJob, useDeviceProfiles, useDevices, useRepos, useUpdateJob, type JobErrorView } from './api'
import { JOB_KINDS, JOB_MODES, emptySchedule, emptySpec, kindLabel, modeLabels, type Job, type JobCreateRequest, type JobMode, type UseVSS } from './types'
import { JobErrorBanner } from './shared'

type Draft = Omit<JobCreateRequest, 'device_id' | 'repo_id'> & { device_id: number | ''; repo_id: number | '' }

const USABLE_PROFILE_STATES = ['active_default', 'authorised']

function draftFrom(job?: Job, deviceId?: number): Draft {
  if (job) {
    const { device_id, repo_id, name, kind, mode, spec, schedule, retention_policy_id, priority, enabled, profile_sid } = job
    return { device_id, repo_id, name, kind, mode, spec: { ...spec, paths: spec.paths ?? [], exclude: spec.exclude ?? [] }, schedule, retention_policy_id, priority, enabled, profile_sid }
  }
  return { device_id: deviceId ?? '', repo_id: '', name: '', kind: 'folder', mode: 'full', spec: emptySpec(), schedule: emptySchedule(), retention_policy_id: null, priority: 0, enabled: true, profile_sid: '' }
}

export type JobEditorProps = {
  /** Job to edit; omit to create. */
  job?: Job
  /** Preselected device when creating. */
  deviceId?: number
  onSaved: (job: Job) => void
  onCancel: () => void
  /** Reload the job after a 409 conflict (edit only). */
  onReload?: () => void
}

/** Create/edit wizard for a backup job: basics, sources, schedule, options. */
export function JobEditor({ job, deviceId, onSaved, onCancel, onReload }: JobEditorProps) {
  const editing = !!job
  const [d, setD] = useState<Draft>(() => draftFrom(job, deviceId))
  const [err, setErr] = useState<JobErrorView | null>(null)
  const devices = useDevices()
  const repos = useRepos()
  const profiles = useDeviceProfiles(d.device_id || null)
  const create = useCreateJob()
  const update = useUpdateJob()
  const set = (p: Partial<Draft>) => setD((x) => ({ ...x, ...p }))
  const setSpec = (p: Partial<Draft['spec']>) => setD((x) => ({ ...x, spec: { ...x.spec, ...p } }))

  async function save() {
    setErr(null)
    try {
      if (job) {
        const { name, mode, spec, schedule, retention_policy_id, priority, enabled, profile_sid } = d
        onSaved(await update.mutateAsync({ id: job.id, revision: job.revision, body: { name, mode, spec, schedule, retention_policy_id, priority, enabled, profile_sid } }))
      } else {
        onSaved(await create.mutateAsync({ ...d, device_id: Number(d.device_id), repo_id: Number(d.repo_id) }))
      }
    } catch (e) {
      setErr(jobErrorView(e))
    }
  }

  const steps: StepDef[] = [
    {
      id: 'basics',
      title: 'Basics',
      validate: () => (!d.name.trim() ? 'Enter a name.' : !d.device_id ? 'Choose a device.' : !d.repo_id ? 'Choose a repository.' : null),
      content: (
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name">
            <input className="input" value={d.name} maxLength={200} onChange={(e) => set({ name: e.target.value })} />
          </Field>
          <Field label="Device" hint={editing ? 'A job cannot move to another device.' : undefined}>
            <select className="input" value={d.device_id} disabled={editing} onChange={(e) => set({ device_id: e.target.value ? Number(e.target.value) : '', profile_sid: '' })}>
              <option value="">Choose…</option>
              {(devices.data ?? []).map((dev) => (
                <option key={dev.id} value={dev.id}>
                  {dev.name}
                </option>
              ))}
            </select>
          </Field>
          {repos.data && repos.data.length > 0 ? (
            <Field label="Repository">
              <select className="input" value={d.repo_id} disabled={editing} onChange={(e) => set({ repo_id: e.target.value ? Number(e.target.value) : '' })}>
                <option value="">Choose…</option>
                {repos.data.map((r) => (
                  <option key={r.id} value={r.id}>
                    {r.name}
                  </option>
                ))}
              </select>
            </Field>
          ) : (
            <Field label="Repository ID">
              <input className="input" type="number" min={1} value={d.repo_id} disabled={editing} onChange={(e) => set({ repo_id: e.target.value ? Number(e.target.value) : '' })} />
            </Field>
          )}
          <Field label="Kind" hint={editing ? 'The kind cannot be changed.' : undefined}>
            <select className="input" value={d.kind} disabled={editing} onChange={(e) => set({ kind: e.target.value })}>
              {JOB_KINDS.map((k) => (
                <option key={k} value={k}>
                  {kindLabel(k)}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Mode">
            <select className="input" value={d.mode} onChange={(e) => set({ mode: e.target.value })}>
              {JOB_MODES.map((m) => (
                <option key={m} value={m}>
                  {modeLabels[m as JobMode]}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Windows profile" hint="Only profiles that have consented to backup can be chosen.">
            <select className="input" value={d.profile_sid} onChange={(e) => set({ profile_sid: e.target.value })}>
              <option value="">Active user (default)</option>
              {(profiles.data ?? []).map((p) => (
                <option key={p.sid} value={p.sid} disabled={!USABLE_PROFILE_STATES.includes(p.state)}>
                  {p.username || p.sid} ({p.state})
                </option>
              ))}
              {d.profile_sid && !(profiles.data ?? []).some((p) => p.sid === d.profile_sid) && <option value={d.profile_sid}>{d.profile_sid}</option>}
            </select>
          </Field>
        </div>
      ),
    },
    {
      id: 'sources',
      title: 'Sources',
      validate: () => (d.kind === 'folder' && !(d.spec.paths ?? []).length ? 'Add at least one folder.' : null),
      content: (
        <div className="flex flex-col gap-4">
          <Field label="Paths">
            <ListInput label="Paths" value={d.spec.paths ?? []} onChange={(paths) => setSpec({ paths })} placeholder="C:\Users\me\Documents" emptyText="No paths yet" />
          </Field>
          <Field label="Exclude">
            <ListInput label="Exclude patterns" value={d.spec.exclude ?? []} onChange={(exclude) => setSpec({ exclude })} placeholder="**/node_modules" />
          </Field>
          <Field label="Volume Shadow Copy" hint="Required needs the vss module on the device.">
            <select className="input w-auto" value={d.spec.use_vss || 'auto'} onChange={(e) => setSpec({ use_vss: e.target.value as UseVSS })}>
              <option value="auto">Auto</option>
              <option value="required">Required</option>
              <option value="off">Off</option>
            </select>
          </Field>
        </div>
      ),
    },
    {
      id: 'schedule',
      title: 'Schedule',
      validate: () => validateSchedule(d.schedule),
      content: <ScheduleEditor value={d.schedule} onChange={(schedule) => set({ schedule })} />,
    },
    {
      id: 'options',
      title: 'Options',
      content: (
        <div className="flex flex-col gap-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Retention policy ID" hint="Leave empty for the repository default.">
              <input className="input" type="number" min={1} value={d.retention_policy_id ?? ''} onChange={(e) => set({ retention_policy_id: e.target.value ? Number(e.target.value) : null })} />
            </Field>
            <Field label="Priority">
              <input className="input" type="number" value={d.priority} onChange={(e) => set({ priority: Number(e.target.value) || 0 })} />
            </Field>
          </div>
          <Toggle checked={d.enabled} onChange={(enabled) => set({ enabled })} label="Enabled" description="Paused jobs keep their history but do not run." />
          {err && <JobErrorBanner view={err} onReload={onReload} />}
        </div>
      ),
    },
  ]

  return <Stepper steps={steps} onFinish={save} onCancel={onCancel} finishLabel={editing ? 'Save' : 'Create job'} />
}
