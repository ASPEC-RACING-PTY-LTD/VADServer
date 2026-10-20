import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { UIProvider } from '../../../components/ui'
import { BackupJobsPanel } from '../JobsPage'
import JobPage from '../JobPage'
import RunPage from '../RunPage'
import { BackupStatusWidget } from '../dashboard'
import { emptySchedule, type Issue, type Job, type Run } from '../types'

const job: Job = {
  id: 5,
  device_id: 1,
  repo_id: 2,
  name: 'Documents',
  kind: 'folder',
  mode: 'full',
  spec: { paths: ['C:\\Users\\me\\Documents'], exclude: [], ruleset_id: null, source_ref: '', use_vss: 'auto' },
  schedule: emptySchedule(),
  retention_policy_id: null,
  priority: 0,
  enabled: true,
  revision: 3,
  profile_sid: '',
  required_modules: [],
  state: 'active',
  created_at: 1,
  updated_at: 1,
}
const suspended: Job = { ...job, id: 6, name: 'Apps', kind: 'app', required_modules: ['discovery'], state: 'suspended_by_mode' }
const paused: Job = { ...job, id: 7, name: 'Pictures', enabled: false }

const run: Run = {
  id: 9,
  job_id: 5,
  device_id: 1,
  client_run_uuid: 'u',
  trigger: 'schedule',
  snapshot_id: 11,
  started_at: Date.now() - 60_000,
  finished_at: Date.now(),
  status: 'partial',
  phase: '',
  files_total: 10,
  files_done: 10,
  bytes_total: 100,
  bytes_done: 100,
  bytes_new: 10,
  bytes_uploaded: 10,
  queue_packs: 0,
  warnings: 1,
  errors: 0,
  eta_ms: 0,
}
const issue: Issue = { id: 31, run_id: 9, path: 'C:\\locked.pst', class: 'locked', severity: 'skipped', detail: '', first_seen: 1, last_seen: Date.now(), count: 1, retryable: true, resolution: 'open', retry_run_id: null }

type Call = { method: string; url: string; body: unknown; headers: Record<string, string> }
type Reply = { status: number; body: unknown }
let calls: Call[]
let handler: (c: Call) => Reply | undefined

const ok = (body: unknown): Reply => ({ status: 200, body })
const fail = (status: number, code: string, details: unknown = {}): Reply => ({ status, body: { error: { code, message: 'server says no', details } } })

/** Default GET routes; `handler` can override any call by returning a reply. */
function route(c: Call): Reply {
  const r = handler(c)
  if (r) return r
  const u = new URL(c.url, 'http://x')
  const p = u.pathname.replace('/api/v1', '')
  if (p === '/devices') return ok({ devices: [{ id: 1, name: 'LAPTOP-1', status: 'approved', install_mode: 'simple' }] })
  if (p === '/repos') return ok({ items: [{ id: 2, name: 'Main' }], next_cursor: '' })
  if (p === '/devices/1/profiles') return ok({ items: [], next_cursor: '' })
  if (p === '/backup/jobs') return ok({ items: [job, suspended, paused], next_cursor: '' })
  if (p === '/backup/runs') return ok({ items: [run], next_cursor: '' })
  if (p === '/backup/runs/9') return ok(run)
  if (p === '/backup/runs/9/issues') return ok({ items: [issue], next_cursor: '' })
  return fail(404, 'not_found')
}

beforeEach(() => {
  calls = []
  handler = () => undefined
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const c: Call = { method: init?.method ?? 'GET', url, body: init?.body ? JSON.parse(String(init.body)) : undefined, headers: (init?.headers ?? {}) as Record<string, string> }
      calls.push(c)
      const r = route(c)
      return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <UIProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/backups" element={<BackupJobsPanel />} />
            <Route path="/backups/jobs/:id" element={<JobPage />} />
            <Route path="/backups/runs/:id" element={<RunPage />} />
            <Route path="/" element={<BackupStatusWidget />} />
          </Routes>
        </MemoryRouter>
      </UIProvider>
    </QueryClientProvider>,
  )
}

const writes = () => calls.filter((c) => c.method !== 'GET')

describe('BackupJobsPanel', () => {
  it('lists jobs per device with state, suspension reason and actions', async () => {
    renderAt('/backups')
    const table = await screen.findByRole('table', { name: 'Backup jobs on LAPTOP-1' })
    await within(table).findByText('Documents')
    const rows = within(table).getAllByRole('row')
    const row = (name: string) => rows.find((r) => r.textContent?.includes(name))!
    expect(row('Documents')).toHaveTextContent('Active')
    expect(within(row('Documents')).getByRole('button', { name: 'Run now' })).toBeInTheDocument()
    expect(row('Apps')).toHaveTextContent('Suspended (mode)')
    expect(row('Apps')).toHaveTextContent('discovery')
    expect(within(row('Apps')).queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument()
    expect(within(row('Apps')).getByRole('button', { name: 'Re-check' })).toBeInTheDocument()
    expect(within(row('Pictures')).getByRole('button', { name: 'Resume' })).toBeInTheDocument()
    expect(within(row('Pictures')).queryByRole('button', { name: 'Run now' })).not.toBeInTheDocument()
    expect(calls.some((c) => c.url === '/api/v1/backup/jobs?device=1&limit=100')).toBe(true)
  })

  it('pauses with If-Match and queues run-now as a backup_now task', async () => {
    handler = (c) => (c.method === 'PATCH' ? ok({ ...job, enabled: false, revision: 4 }) : c.method === 'POST' ? { status: 201, body: { task_id: 77 } } : undefined)
    renderAt('/backups')
    const table = await screen.findByRole('table', { name: 'Backup jobs on LAPTOP-1' })
    await within(table).findByText('Documents')
    const row = within(table).getAllByRole('row').find((r) => r.textContent?.includes('Documents'))!
    fireEvent.click(within(row).getByRole('button', { name: 'Pause' }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toMatchObject({ method: 'PATCH', url: '/api/v1/backup/jobs/5', body: { enabled: false } })
    expect(writes()[0].headers['If-Match']).toBe('3')
    fireEvent.click(within(row).getByRole('button', { name: 'Run now' }))
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes()[1]).toMatchObject({ method: 'POST', url: '/api/v1/device/tasks', body: { device_id: 1, kind: 'backup_now', payload: { job_id: 5 } } })
  })
})

describe('JobPage', () => {
  it('shows a conflict with Reload after a stale If-Match', async () => {
    handler = (c) => (c.method === 'PATCH' ? fail(409, 'conflict', { current_revision: 4 }) : undefined)
    renderAt('/backups/jobs/5')
    fireEvent.click(await screen.findByRole('button', { name: 'Edit' }))
    const dialog = await screen.findByRole('dialog')
    for (let i = 0; i < 3; i++) {
      fireEvent.click(within(dialog).getByRole('button', { name: 'Next' }))
      await within(dialog).findByText(['Sources', 'Schedule', 'Options'][i], { selector: 'h2 *, h2' })
    }
    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }))
    const alert = await within(dialog).findByRole('alert')
    expect(alert).toHaveTextContent(/Someone else changed this job/)
    const patch = writes()[0]
    expect(patch.url).toBe('/api/v1/backup/jobs/5')
    expect(patch.headers['If-Match']).toBe('3')
    const before = calls.filter((c) => c.url.startsWith('/api/v1/backup/jobs?')).length
    fireEvent.click(within(alert).getByRole('button', { name: 'Reload' }))
    await waitFor(() => expect(calls.filter((c) => c.url.startsWith('/api/v1/backup/jobs?')).length).toBeGreaterThan(before))
  })

  it.each([
    ['module_unavailable', { module: 'vss' }, /"vss" module/],
    ['profile_blacklisted', {}, /blacklisted/],
    ['profile_not_authorised', {}, /has not consented/],
    ['device_not_approved', {}, /not approved yet/],
  ])('explains %s with a link to Devices', async (code, details, msg) => {
    handler = (c) => (c.method === 'POST' ? fail(409, code, details) : undefined)
    renderAt('/backups/jobs/5')
    fireEvent.click(await screen.findByRole('button', { name: 'Run now' }))
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent(msg)
    expect(within(alert).getByRole('link', { name: 'Open Devices' })).toHaveAttribute('href', '/devices')
  })

  it('reports a gating error when creating a job', async () => {
    handler = (c) => (c.method === 'POST' ? fail(409, 'module_unavailable', { module: 'discovery' }) : undefined)
    renderAt('/backups/jobs/new?device=1')
    fireEvent.change(await screen.findByLabelText('Name'), { target: { value: 'Apps' } })
    await screen.findByRole('option', { name: 'Main' })
    fireEvent.change(screen.getByLabelText('Repository'), { target: { value: '2' } })
    fireEvent.change(screen.getByLabelText('Kind'), { target: { value: 'app' } })
    for (let i = 0; i < 3; i++) fireEvent.click(await screen.findByRole('button', { name: 'Next' }))
    fireEvent.click(await screen.findByRole('button', { name: 'Create job' }))
    expect(await screen.findByText(/"discovery" module/)).toBeInTheDocument()
    expect(writes()[0]).toMatchObject({ url: '/api/v1/backup/jobs', body: { device_id: 1, repo_id: 2, name: 'Apps', kind: 'app' } })
  })
})

describe('RunPage issues', () => {
  it('skips selected issues and a whole class, then retries skipped', async () => {
    handler = (c) => {
      if (c.url.endsWith('/issues/skip')) return ok({ ok_count: 1, failed: [] })
      if (c.url.endsWith('/retry-skipped')) return { status: 201, body: { task_id: 5 } }
      return undefined
    }
    renderAt('/backups/runs/9')
    fireEvent.click(await screen.findByLabelText('Select row 31'))
    fireEvent.click(screen.getByRole('button', { name: 'Skip selected' }))
    await waitFor(() => expect(writes()).toHaveLength(1))
    expect(writes()[0]).toMatchObject({ method: 'POST', url: '/api/v1/backup/runs/9/issues/skip', body: { ids: [31] } })

    fireEvent.change(screen.getByLabelText('Issue class'), { target: { value: 'locked' } })
    fireEvent.click(await screen.findByRole('button', { name: 'Skip all locked' }))
    await waitFor(() => expect(writes()).toHaveLength(2))
    expect(writes()[1].body).toEqual({ class: 'locked' })
    expect(calls.some((c) => c.url === '/api/v1/backup/runs/9/issues?class=locked&limit=200')).toBe(true)

    fireEvent.click(screen.getByRole('button', { name: 'Retry skipped' }))
    await waitFor(() => expect(writes()).toHaveLength(3))
    expect(writes()[2]).toMatchObject({ method: 'POST', url: '/api/v1/backup/runs/9/retry-skipped' })
  })

  it('explains nothing_to_retry', async () => {
    handler = (c) => (c.url.endsWith('/retry-skipped') ? fail(409, 'nothing_to_retry') : undefined)
    renderAt('/backups/runs/9')
    fireEvent.click(await screen.findByRole('button', { name: 'Retry skipped' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/no skipped, retryable issues/)
  })
})

describe('BackupStatusWidget', () => {
  it('shows the last run per job and the failing count', async () => {
    handler = (c) => (c.url.startsWith('/api/v1/backup/runs') ? ok({ items: [{ ...run, job_id: 6, status: 'failed' }, run], next_cursor: '' }) : undefined)
    renderAt('/')
    expect(await screen.findByText('1 failing')).toBeInTheDocument()
    expect(screen.getByText('Documents').closest('a')).toHaveAttribute('href', '/backups/jobs/5')
    expect(screen.getByText('Pictures').closest('a')).toHaveTextContent('never run')
  })
})
