import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { UIProvider } from '../../../components/ui'
import { ReadinessPanel } from '../ReadinessPage'
import { readinessPath } from '../api'
import { checkLabel, countChecks, countDevices, deviceStatus, safeFixLink, sortChecks, type ReadinessReport } from '../types'

let role: 'user' | 'admin' = 'user'
vi.mock('../../../lib/auth', () => ({
  useAuth: () => ({ state: { status: 'authenticated', user: { id: 1, role }, siteName: 'x' } }),
}))

const report: ReadinessReport = {
  score: 67,
  status: 'critical',
  devices: [
    {
      device_id: 1,
      checks: [
        { id: 'install_mode', status: 'ok', message: 'Simple mode.', fix_link: '' },
        { id: 'recent_backup', status: 'ok', message: 'Backed up 2 hours ago.', fix_link: '/backups?device=1' },
        { id: 'vss_available', status: 'warning', message: 'Snapshot support is not available.', fix_link: '/devices?device=1' },
      ],
      jobs: [{ job_id: 3, last_success_at: Date.now() - 7200_000, open_issues: 2, vss_used: false, verified_at: 0, test_restored_at: 0 }],
    },
    {
      device_id: 2,
      checks: [
        { id: 'recent_backup', status: 'critical', message: 'No backup job.', fix_link: '/backups?device=2' },
        { id: 'server_key_custody', status: 'unknown', message: '', fix_link: 'https://evil.example/' },
      ],
      jobs: null,
    },
  ],
}

type Call = { method: string; url: string }
let calls: Call[]
let readinessReply: { status: number; body: unknown }

beforeEach(() => {
  role = 'user'
  calls = []
  readinessReply = { status: 200, body: report }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ method: init?.method ?? 'GET', url })
      const p = new URL(url, 'http://x').pathname.replace('/api/v1', '')
      let r: { status: number; body: unknown } = { status: 404, body: { error: { code: 'not_found', message: 'nf' } } }
      if (p === '/readiness') r = readinessReply
      if (p === '/devices') r = { status: 200, body: { devices: [{ id: 1, name: 'LAPTOP-1', status: 'approved' }] } }
      if (p === '/backup/jobs') r = { status: 200, body: { items: [{ id: 3, device_id: 1, name: 'Documents' }], next_cursor: '' } }
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
            <Route path="/admin" element={<ReadinessPanel />} />
          </Routes>
        </MemoryRouter>
      </UIProvider>
    </QueryClientProvider>,
  )
}

const readinessCalls = () => calls.filter((c) => c.url.includes('/readiness'))

describe('readiness helpers', () => {
  it('derives device status from scored checks only', () => {
    expect(deviceStatus(report.devices![0])).toBe('warning')
    expect(deviceStatus(report.devices![1])).toBe('critical')
    expect(deviceStatus({ device_id: 9, checks: [{ id: 'install_mode', status: 'ok', message: '', fix_link: '' }], jobs: [] })).toBe('unknown')
  })

  it('counts devices and non-informational checks', () => {
    expect(countDevices(report)).toEqual({ ok: 0, warning: 1, critical: 1, unknown: 0 })
    expect(countChecks(report)).toEqual({ ok: 1, warning: 1, critical: 1, unknown: 1 })
  })

  it('sorts checks worst first with informational last', () => {
    expect(sortChecks(report.devices![0].checks).map((c) => c.id)).toEqual(['vss_available', 'recent_backup', 'install_mode'])
  })

  it('labels unknown check ids generically', () => {
    expect(checkLabel('recent_backup')).toBe('Recent backup')
    expect(checkLabel('some_new_check')).toBe('Some new check')
  })

  it('only follows app-relative fix links', () => {
    expect(safeFixLink('/backups/jobs/3')).toBe('/backups/jobs/3')
    expect(safeFixLink('https://evil.example/')).toBeNull()
    expect(safeFixLink('//evil.example')).toBeNull()
    expect(safeFixLink('javascript:alert(1)')).toBeNull()
    expect(safeFixLink('')).toBeNull()
  })

  it('builds query paths', () => {
    expect(readinessPath()).toBe('/readiness')
    expect(readinessPath({ device: 4, scope: 'all' })).toBe('/readiness?device=4')
    expect(readinessPath({ scope: 'all' })).toBe('/readiness?scope=all')
  })
})

describe('ReadinessPanel', () => {
  it('shows the score, devices worst first, checks with fix links and jobs', async () => {
    renderAt('/admin')
    expect(await screen.findByTestId('readiness-score')).toHaveTextContent('67%')
    expect(readinessCalls()[0].url).toBe('/api/v1/readiness')
    const laptop = await screen.findByRole('region', { name: 'LAPTOP-1' })
    const cards = screen.getAllByRole('region').filter((s) => s.getAttribute('aria-label') !== 'Overall readiness')
    expect(cards.map((s) => s.getAttribute('aria-label'))).toEqual(['Device #2', 'LAPTOP-1'])

    expect(within(laptop).getByText('Snapshot support is not available.')).toBeInTheDocument()
    expect(within(laptop).getByRole('link', { name: 'Fix: Snapshot support (VSS)' })).toHaveAttribute('href', '/devices?device=1')
    // ok checks get no fix link
    expect(within(laptop).queryByRole('link', { name: 'Fix: Recent backup' })).not.toBeInTheDocument()
    expect(within(laptop).getByText('Info')).toBeInTheDocument()
    expect(within(laptop).getByRole('link', { name: 'Backups' })).toHaveAttribute('href', '/sync/backups?device=1')
    expect(within(laptop).getByRole('link', { name: 'Device' })).toHaveAttribute('href', '/admin/devices?device=1')
    expect(await within(laptop).findByRole('link', { name: 'Documents' })).toHaveAttribute('href', '/backups/jobs/3')

    const other = screen.getByRole('region', { name: 'Device #2' })
    expect(within(other).getByText('Server key custody')).toBeInTheDocument()
    expect(within(other).getByRole('link', { name: 'Fix: Recent backup' })).toHaveAttribute('href', '/backups?device=2')
    expect(within(other).getAllByRole('link').map((a) => a.getAttribute('href'))).not.toContain('https://evil.example/')
  })

  it('passes ?device= through', async () => {
    renderAt('/admin?device=1')
    await screen.findByTestId('readiness-score')
    expect(readinessCalls()[0].url).toBe('/api/v1/readiness?device=1')
    expect(screen.getByRole('button', { name: 'Show all devices' })).toBeInTheDocument()
  })

  it("shows every user's devices to admins", async () => {
    role = 'admin'
    renderAt('/admin')
    await screen.findByTestId('readiness-score')
    expect(readinessCalls()[0].url).toBe('/api/v1/readiness?scope=all')
  })

  it('shows an empty state without devices', async () => {
    readinessReply = { status: 200, body: { score: 0, status: 'unknown', devices: [] } }
    renderAt('/admin')
    expect(await screen.findByText('No devices to check')).toBeInTheDocument()
    expect(screen.getByTestId('readiness-score')).toHaveTextContent('–')
  })
})
