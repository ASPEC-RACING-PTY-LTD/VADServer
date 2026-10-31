import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { UIProvider } from '../../../components/ui'
import PublicSharePage from '../PublicSharePage'
import { ShareDialog } from '../ShareDialog'
import type { Node } from '../../../components/files/types'

type Call = { method: string; url: string; body: unknown }
let calls: Call[]
let reply: (c: Call) => { status: number; body: unknown }

beforeEach(() => {
  calls = []
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const c = { method: init?.method ?? 'GET', url, body: init?.body ? JSON.parse(String(init.body)) : undefined }
      calls.push(c)
      const r = reply(c)
      return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => vi.unstubAllGlobals())

const wrap = (ui: React.ReactNode) =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <UIProvider>{ui}</UIProvider>
    </QueryClientProvider>,
  )

describe('public share page', () => {
  it('shows the video with its title, sharer and download when allowed', async () => {
    reply = () => ({ status: 200, body: { title: 'Overtime winner', description: 'What a goal', name: 'g.mp4', size: 1048576, mime: 'video/mp4', recorded_at: Date.now(), shared_at: Date.now(), allow_download: true, shared_by: 'Bob', site: 'VaultDock', duration: 42 } })
    const { container } = wrap(<PublicSharePage token="tok123tok123tok123" />)
    expect(await screen.findByRole('heading', { name: 'Overtime winner' })).toBeInTheDocument()
    expect(screen.getByText('Bob')).toBeInTheDocument()
    expect(screen.getByText('What a goal')).toBeInTheDocument()
    expect(container.querySelector('video')?.getAttribute('src')).toBe('/api/v1/public/shares/tok123tok123tok123/video')
    expect(screen.getByRole('link', { name: /Download/ })).toHaveAttribute('href', '/api/v1/public/shares/tok123tok123tok123/video?download=1')
  })

  it('says when a link has expired', async () => {
    reply = () => ({ status: 410, body: { error: { code: 'expired', message: 'This link has expired' } } })
    wrap(<PublicSharePage token="tok123tok123tok123" />)
    expect(await screen.findByText('This link has expired')).toBeInTheDocument()
  })
})

describe('share dialog', () => {
  it('creates a link with the chosen expiry and download setting', async () => {
    reply = (c) => (c.method === 'POST' ? { status: 201, body: { id: 1, url: '/s/abc', token: 'abc', state: 'active' } } : { status: 200, body: { shares: [] } })
    const node = { id: 9, name: 'Insane goal.mp4', is_dir: false, mime: 'video/mp4' } as Node
    wrap(<ShareDialog node={node} onClose={() => {}} />)
    expect(await screen.findByDisplayValue('Insane goal')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('Link works for'), { target: { value: '86400' } })
    fireEvent.click(screen.getByRole('switch', { name: /Allow downloading/ }))
    fireEvent.click(screen.getByRole('button', { name: 'Create link' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ node_id: 9, title: 'Insane goal', description: '', expires_in: 86400, allow_download: true }))
  })
})
