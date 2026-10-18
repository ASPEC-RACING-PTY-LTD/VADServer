import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'
import { enabledModules, isEnabled, navEntries } from '../registry'
import type { FeatureModule } from '../types'

const user = { id: 1, username: 'alice', email: '', display_name: 'Alice', role: 'user', disabled: false, quota_bytes: 0, used_bytes: 0, root_id: 1, created_at: 0, last_login_at: null }

vi.mock('../../lib/auth', () => ({
  useUser: () => user,
  useAuth: () => ({ state: { status: 'authenticated', user, siteName: 'VaultDock' }, logout: async () => {}, setUser: () => {}, refresh: async () => {} }),
}))
vi.mock('../../lib/uploads', () => ({ useUploads: () => ({ items: [] }) }))

// Imported after the mocks are registered (vi.mock is hoisted anyway).
const { Layout } = await import('../../components/Layout')

type Reply = { status: number; body: unknown }
let info: Reply
let summary: Reply
let infoResolved: Promise<void>
let releaseInfo: () => void

beforeEach(() => {
  info = { status: 200, body: { site_name: 'VaultDock', features: ['approvals.v1'] } }
  summary = { status: 200, body: { pending_approvals: 3 } }
  infoResolved = Promise.resolve()
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => {
      let r: Reply = { status: 200, body: {} }
      if (url.endsWith('/info')) {
        await infoResolved
        r = info
      } else if (url.endsWith('/summary')) r = summary
      return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function renderLayout() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <MemoryRouter initialEntries={['/']}>
        <Layout>
          <div>page content</div>
        </Layout>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const gated: FeatureModule = { id: 'gated', routes: [{ path: '/gated', component: () => null }], nav: { label: 'Gated', icon: ShieldCheck, order: 1 }, requiredFeatureFlag: 'x.v1' }

describe('feature flags', () => {
  it('registry: a module with requiredFeatureFlag is enabled only when the flag is present', () => {
    const off = { isAdmin: false, flags: new Set<string>() }
    const on = { isAdmin: false, flags: new Set(['x.v1']) }
    expect(isEnabled(gated, off)).toBe(false)
    expect(isEnabled(gated, on)).toBe(true)
    expect(enabledModules(off).some((m) => m.id === 'approvals')).toBe(false)
    expect(enabledModules({ ...off, flags: new Set(['approvals.v1']) }).some((m) => m.id === 'approvals')).toBe(true)
    expect(navEntries({ ...off, flags: new Set(['approvals.v1']) }, 'main').find((n) => n.id === 'approvals')?.useBadge).toBeTypeOf('function')
  })

  it('shows a flag-gated nav entry once /info advertises the flag, and holds content while loading', async () => {
    infoResolved = new Promise<void>((r) => (releaseInfo = r))
    renderLayout()
    const nav = screen.getAllByRole('navigation')[0]
    expect(within(nav).getByRole('link', { name: /Dashboard/ })).toBeInTheDocument()
    expect(within(nav).queryByRole('link', { name: /Approvals/ })).toBeNull()
    expect(screen.queryByText('page content')).toBeNull()
    releaseInfo()
    expect(await within(nav).findByRole('link', { name: /Approvals/ })).toBeInTheDocument()
    expect(screen.getByText('page content')).toBeInTheDocument()
    expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).endsWith('/api/v1/info'))).toBe(true)
  })

  it('keeps a flag-gated entry hidden when /info does not advertise the flag', async () => {
    info = { status: 200, body: { features: ['backup.v1'] } }
    renderLayout()
    await screen.findByText('page content')
    expect(screen.queryByRole('link', { name: /Approvals/ })).toBeNull()
  })

  it('falls back to no flags when /info fails, without blocking the page', async () => {
    info = { status: 500, body: { error: { code: 'internal', message: 'boom' } } }
    renderLayout()
    await screen.findByText('page content')
    expect(screen.queryByRole('link', { name: /Approvals/ })).toBeNull()
  })
})

describe('nav badges', () => {
  it('renders the approvals pending count from /summary with an accessible label', async () => {
    renderLayout()
    const badge = await screen.findByLabelText('3 pending')
    expect(badge).toHaveTextContent('3')
    expect(badge.closest('a')).toHaveAttribute('href', '/approvals')
  })

  it('hides the badge when the count is zero', async () => {
    summary = { status: 200, body: { pending_approvals: 0 } }
    renderLayout()
    await screen.findByRole('link', { name: /Approvals/ })
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).endsWith('/summary'))).toBe(true))
    expect(screen.queryByLabelText(/pending/)).toBeNull()
  })
})
