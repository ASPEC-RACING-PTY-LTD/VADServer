import { ApiError, api, csrfHeader, del, get, listAll, post, withQuery, type BulkResult, type Page } from '../../lib/api'
import { createKeys, queryKeys, useApiInfiniteList, useApiMutation, useApiQuery } from '../../lib/query'
import type { DeviceLite, DeviceProfileLite, Issue, IssueSkipRequest, Job, JobCreateRequest, JobUpdateRequest, Run } from './types'

export const backupKeys = createKeys('backups')

const RUNNING_REFRESH_MS = 3000

/** `GET /devices` (the caller's devices). */
export function useDevices() {
  return useApiQuery<DeviceLite[]>(backupKeys.sub('devices'), async () => (await get<{ devices: DeviceLite[] }>('/devices')).devices ?? [])
}

/** `GET /devices/{id}/profiles`, first page (enough for a picker). */
export function useDeviceProfiles(deviceId: number | null | undefined) {
  return useApiQuery<DeviceProfileLite[]>(
    backupKeys.sub('profiles', deviceId ?? 0),
    async () => (await get<Page<DeviceProfileLite>>(withQuery(`/devices/${deviceId}/profiles`, { limit: 500 }))).items ?? [],
    { enabled: !!deviceId, retry: false },
  )
}

/** Subset of a repository row (api-v1 §5). */
export type RepoLite = { id: number; name: string }

/** `GET /repos` (all pages) for the job editor's repository picker. */
export function useRepos() {
  return useApiQuery<RepoLite[]>(backupKeys.sub('repos'), () => listAll<RepoLite>('/repos', { limit: 500 }), { retry: false })
}

/** `GET /backup/jobs?device=` as a cursor list. */
export function useJobs(deviceId: number) {
  return useApiInfiniteList<Job>(backupKeys.list({ device: deviceId }), '/backup/jobs', { device: deviceId, limit: 100 })
}

/** Every job of the caller (dashboard). */
export function useAllJobs() {
  return useApiQuery<Job[]>(backupKeys.list({ all: true }), () => listAll<Job>('/backup/jobs', { limit: 500 }), { refetchInterval: 60_000 })
}

/**
 * One job. api-v1 §6 has no `GET /backup/jobs/{id}`, so this pages through the caller's jobs and picks it out.
 * Rejects with a 404 ApiError when it is not found.
 */
export function fetchJob(id: number): Promise<Job> {
  return listAll<Job>('/backup/jobs', { limit: 500 }).then((all) => {
    const j = all.find((x) => x.id === id)
    if (!j) throw new ApiError(404, 'not_found', 'This backup job no longer exists.')
    return j
  })
}

export function useJob(id: number) {
  return useApiQuery<Job>(backupKeys.detail(id), () => fetchJob(id), { enabled: id > 0 })
}

export type RunFilter = { job?: number; device?: number; status?: string; limit?: number }

/** `GET /backup/runs?job=&device=&status=` (newest first); polls while a run is in progress. */
export function useRuns(filter: RunFilter) {
  return useApiQuery<Page<Run>>(backupKeys.sub('runs', filter), () => get<Page<Run>>(withQuery('/backup/runs', { limit: 50, ...filter })), {
    refetchInterval: (q) => (q.state.data?.items.some((r) => ['queued', 'scanning', 'uploading', 'committing'].includes(r.status)) ? RUNNING_REFRESH_MS : false),
  })
}

export function useRun(id: number) {
  return useApiQuery<Run>(backupKeys.sub('run', id), `/backup/runs/${id}`, {
    enabled: id > 0,
    refetchInterval: (q) => (q.state.data && ['queued', 'scanning', 'uploading', 'committing'].includes(q.state.data.status) ? RUNNING_REFRESH_MS : false),
  })
}

export type IssueFilter = { class?: string; resolution?: string }

/** `GET /backup/runs/{id}/issues?class=&resolution=`. */
export function useIssues(runId: number, filter: IssueFilter = {}) {
  return useApiInfiniteList<Issue>(backupKeys.sub('issues', runId, filter), `/backup/runs/${runId}/issues`, { ...filter, limit: 200 }, { enabled: runId > 0 })
}

const invalidate = [backupKeys.all, queryKeys.summary]

export function useCreateJob() {
  return useApiMutation<Job, JobCreateRequest>((body) => post<Job>('/backup/jobs', body), { invalidate })
}

/** PATCH with `If-Match: <revision>`; a stale revision gives `409 conflict`. */
export function patchJob(id: number, revision: number, body: JobUpdateRequest): Promise<Job> {
  return api<Job>('PATCH', `/backup/jobs/${id}`, body, {
    headers: { 'Content-Type': 'application/json', ...csrfHeader(), 'If-Match': String(revision) },
  })
}

export function useUpdateJob() {
  return useApiMutation<Job, { id: number; revision: number; body: JobUpdateRequest }>(({ id, revision, body }) => patchJob(id, revision, body), { invalidate })
}

export function useDeleteJob() {
  return useApiMutation<void, number>((id) => del(`/backup/jobs/${id}`), { invalidate })
}

/** Run now: a session cannot start a run itself (dev route), so it queues a `backup_now` device task. */
export function useRunNow() {
  return useApiMutation<{ task_id: number }, Pick<Job, 'id' | 'device_id'>>(
    (j) => post<{ task_id: number }>('/device/tasks', { device_id: j.device_id, kind: 'backup_now', payload: { job_id: j.id } }),
    { invalidate },
  )
}

export function useSkipIssues(runId: number) {
  return useApiMutation<BulkResult, IssueSkipRequest>((body) => post<BulkResult>(`/backup/runs/${runId}/issues/skip`, body), { invalidate: [backupKeys.sub('issues', runId)] })
}

export function useRetrySkipped(runId: number) {
  return useApiMutation<{ task_id: number }, void>(() => post<{ task_id: number }>(`/backup/runs/${runId}/retry-skipped`, {}), { invalidate: [backupKeys.sub('issues', runId)] })
}

/** How a gating or save error should be presented. */
export type JobErrorView = { message: string; devicesLink: boolean; conflict: boolean }

/** User-facing text for job write / run errors (api-v1 §1.1, §6, §6A, §6B; P1-18 codes). */
export function jobErrorView(e: unknown): JobErrorView {
  const v = (message: string, devicesLink = false, conflict = false): JobErrorView => ({ message, devicesLink, conflict })
  if (!(e instanceof ApiError)) return v(e instanceof Error ? e.message : String(e))
  const d = (e.details ?? {}) as { module?: string; current_revision?: number }
  switch (e.code) {
    case 'conflict':
      return v('Someone else changed this job since you opened it. Reload to get the latest version, then reapply your changes.', false, true)
    case 'module_unavailable':
      return v(`The device does not have the ${d.module ? `"${d.module}" module` : 'module this job needs'} enabled. Switch the device to Advanced mode or enable the module on the Devices page.`, true)
    case 'profile_blacklisted':
      return v('The Windows profile for this job is blacklisted (for example after a password change). A superuser must clear it and the Windows user must consent again. See the Devices page.', true)
    case 'profile_not_authorised':
      return v('The Windows profile for this job has not consented to backup. Request consent from the Devices page; the Windows user must approve it on the device.', true)
    case 'device_not_approved':
      return v('The device is not approved yet. Approve it on the Devices page first.', true)
    case 'nothing_to_retry':
      return v('There are no skipped, retryable issues left to retry for this run.')
    case 'validation': {
      const f = e.fieldErrors
      const parts = Object.entries(f).map(([k, m]) => `${k}: ${m}`)
      return v(parts.length ? parts.join('; ') : e.message)
    }
    default:
      return v(e.message)
  }
}
