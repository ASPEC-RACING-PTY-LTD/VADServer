import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { UIProvider } from '../../../components/ui'
import ApprovalsPage from '../ApprovalsPage'
import { PendingApprovalsWidget } from '../dashboard'
import type { Approval } from '../types'

const base: Approval = {
  id: 7,
  kind: 'device_task',
  subject_type: 'device_task',
  subject_id: 42,
  plan_digest: 'ab'.repeat(32),
  summary: 'Restore 3 files to LAPTOP-1',
  risk: 'high',
  requested_by: 'user',
  requester_ref: '',
  state: 'pending',
  expires_at: Date.now() + 3600_000,
  decided_by: null,
  decided_at: null,
  reason: '',
}
const low: Approval = { ...base, id: 8, kind: 'prune', subject_type: 'prune_plan', risk: 'low', summary: 'Delete 14 snapshots' }

type Call = { method: string; url: string; body: unknown }
type Reply = { status: number; body: unknown }
let calls: Call[]
let handler: (c: Call) => Reply

beforeEach(() => {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const c: Call = { method: init?.method ?? 'GET', url, body: init?.body ? JSON.parse(String(init.body)) : undefined }
      calls.push(c)
      const r = handler(c)
      return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function listHandler(items: Approval[], onPost?: (c: Call) => Reply) {
  return (c: Call): Reply => {
    if (c.method === 'POST' && onPost) return onPost(c)
    const m = c.url.match(/\/approvals\/(\d+)$/)
    if (m) {
      const a = items.find((x) => x.id === Number(m[1]))
      return a ? { status: 200, body: a } : { status: 404, body: { error: { code: 'not_found', message: 'not found' } } }
    }
    const state = new URL(c.url, 'http://x').searchParams.get('state')
    return { status: 200, body: { items: items.filter((a) => !state || a.state === state), next_cursor: '' } }
  }
}

function renderAt(path: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <UIProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path="/approvals" element={<ApprovalsPage />} />
            <Route path="/approvals/:id" element={<ApprovalsPage />} />
            <Route path="/" element={<PendingApprovalsWidget />} />
          </Routes>
        </MemoryRouter>
      </UIProvider>
    </QueryClientProvider>,
  )
}

const posts = () => calls.filter((c) => c.method === 'POST')

describe('ApprovalsPage', () => {
  it('lists pending approvals and filters by kind', async () => {
    handler = listHandler([base, low])
    renderAt('/approvals')
    expect(await screen.findByText('Restore 3 files to LAPTOP-1')).toBeInTheDocument()
    expect(screen.getByText('Delete 14 snapshots')).toBeInTheDocument()
    expect(calls[0].url).toBe('/api/v1/approvals?state=pending&limit=100')
    fireEvent.click(within(screen.getByRole('group', { name: 'Kind' })).getByRole('button', { name: 'Prune' }))
    expect(screen.queryByText('Restore 3 files to LAPTOP-1')).not.toBeInTheDocument()
    expect(screen.getByText('Delete 14 snapshots')).toBeInTheDocument()
  })

  it('approves a high-risk approval with confirm text', async () => {
    handler = listHandler([base], () => ({ status: 200, body: { ...base, state: 'consumed', decided_at: Date.now(), decided_by: 1, operation_id: null } }))
    renderAt('/approvals')
    fireEvent.click(await screen.findByText('Restore 3 files to LAPTOP-1'))
    const dialog = await screen.findByRole('dialog')
    const approve = within(dialog).getByRole('button', { name: 'Approve' })
    expect(approve).toBeDisabled()
    fireEvent.change(within(dialog).getByLabelText('Confirmation text'), { target: { value: 'LAPTOP-1' } })
    expect(approve).toBeEnabled()
    fireEvent.click(approve)
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posts()[0].url).toBe('/api/v1/approvals/7/approve')
    expect(posts()[0].body).toEqual({ confirm_text: 'LAPTOP-1' })
    expect(await screen.findByText('Approved')).toBeInTheDocument()
  })

  it('approves a low-risk approval with an empty body', async () => {
    handler = listHandler([low], () => ({ status: 200, body: { ...low, state: 'approved', operation_id: null } }))
    renderAt('/approvals/8')
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Approve' }))
    await waitFor(() => expect(posts()).toHaveLength(1))
    expect(within(dialog).queryByLabelText('Confirmation text')).not.toBeInTheDocument()
    expect(posts()[0].body).toEqual({})
  })

  it('rejects with a reason', async () => {
    handler = listHandler([low], () => ({ status: 200, body: { ...low, state: 'rejected', reason: 'not now', operation_id: null } }))
    renderAt('/approvals/8')
    const dialog = await screen.findByRole('dialog')
    fireEvent.click(await within(dialog).findByRole('button', { name: 'Reject…' }))
    fireEvent.change(within(dialog).getByLabelText('Reason'), { target: { value: 'not now' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reject' }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
    expect(posts()[0].url).toBe('/api/v1/approvals/8/reject')
    expect(posts()[0].body).toEqual({ reason: 'not now' })
  })

  it.each([
    [409, 'plan_stale', {}, /plan changed/i],
    [409, 'approval_not_pending', { state: 'rejected' }, /already been decided \(it is now rejected\)/],
    [403, 'not_superuser', {}, /Only a superuser/],
    [400, 'validation', { fields: { confirm_text: 'must equal the subject name' } }, /confirmation text does not match/],
  ])('shows a clear message for %i %s', async (status, code, details, msg) => {
    handler = listHandler([base], () => ({ status, body: { error: { code, message: 'server says no', details } } }))
    renderAt('/approvals/7')
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(await within(dialog).findByLabelText('Confirmation text'), { target: { value: 'X' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Approve' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(msg)
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('refetches after approval_not_pending', async () => {
    handler = listHandler([low], () => ({ status: 409, body: { error: { code: 'approval_not_pending', message: 'x', details: { state: 'approved' } } } }))
    renderAt('/approvals/8')
    const dialog = await screen.findByRole('dialog')
    const approve = await within(dialog).findByRole('button', { name: 'Approve' })
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET')).toHaveLength(2))
    fireEvent.click(approve)
    await within(dialog).findByRole('alert')
    await waitFor(() => expect(calls.filter((c) => c.method === 'GET').length).toBeGreaterThan(2))
  })
})

describe('PendingApprovalsWidget', () => {
  it('shows the pending count and items', async () => {
    handler = () => ({ status: 200, body: { items: [base, low], next_cursor: 'c' } })
    renderAt('/')
    expect(await screen.findByText('Restore 3 files to LAPTOP-1')).toBeInTheDocument()
    expect(screen.getByText('2+')).toBeInTheDocument()
    expect(calls[0].url).toBe('/api/v1/approvals?state=pending&limit=5')
  })
})
