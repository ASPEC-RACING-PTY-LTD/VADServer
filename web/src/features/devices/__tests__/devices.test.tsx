import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { UIProvider } from '../../../components/ui'
import { ModeBadges } from '../ModeBadges'

let role: 'user' | 'admin' = 'admin'
const user = () => ({ id: 1, username: 'alice', email: '', display_name: 'Alice', role, disabled: false, quota_bytes: 0, used_bytes: 0, root_id: 1, created_at: 0, last_login_at: null })

vi.mock('../../../lib/auth', () => ({ useUser: () => user() }))

const { default: DevicesPage } = await import('../../../pages/Devices')

type Call = { method: string; url: string; body: unknown }
type Reply = { status: number; body: unknown }
let calls: Call[]
let routes: Record<string, Reply>

const device = {
  id: 5,
  name: 'LAPTOP-1',
  os: 'Windows 11',
  client_version: '1.0',
  identifier: 'x',
  key_fingerprint: 'kf',
  status: 'approved',
  user_code: '',
  request_ip: '',
  request_location: '',
  created_at: 0,
  expires_at: 0,
  decided_at: 0,
  last_seen_at: 0,
  last_ip: '',
  sync_state: null,
  install_mode: 'advanced',
  modules_summary: { enabled: ['core', 'sync', 'profiles'], failed: ['vss'], updated_at: Date.now() - 60_000 },
}

const profile = (o: object) => ({
  id: 1,
  device_id: 5,
  sid: 'S-1-5-21-1',
  username: 'bob',
  profile_path: 'C:\\Users\\bob',
  vaultdock_user_id: null,
  state: 'requested',
  consent_id: null,
  consent_at: null,
  requested_by_user_id: null,
  request_scope: 'selected',
  blacklisted_at: null,
  blacklist_reason: null,
  cleared_by: null,
  cleared_at: null,
  updated_at: 0,
  ...o,
})

beforeEach(() => {
  role = 'admin'
  calls = []
  routes = {
    'GET /devices': { status: 200, body: { devices: [device] } },
    'GET /info': { status: 200, body: { features: ['profiles.v1', 'modules.v1'] } },
    'GET /keys/server': { status: 200, body: { server_identity: { sik_pub: 'p', sik_fingerprint: 'ABCD-0123-4567-89AB-CDEF-0123-4567-89AB', sek_pub: 'q', sek_version: 3, sek_sig: 's' } } },
    'GET /admin/users': { status: 200, body: { users: [{ id: 1, is_superuser: true }] } },
    'GET /devices/5/profiles': {
      status: 200,
      body: {
        items: [
          profile({ id: 1, sid: 'S-1-5-21-1', username: 'bob', state: 'requested' }),
          profile({ id: 2, sid: 'S-1-5-21-2', username: 'eve', state: 'blacklisted', blacklist_reason: 'password_changed', blacklisted_at: 1 }),
          profile({ id: 3, sid: 'S-1-5-21-3', username: 'alice', state: 'active_default', consent_at: 1 }),
        ],
        next_cursor: '',
      },
    },
    'POST /devices/5/profiles/requests': { status: 201, body: { task_id: 9 } },
    'POST /admin/device-profiles/2/clear': { status: 200, body: profile({ id: 2, state: 'cleared_pending_consent' }) },
  }
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const method = init?.method ?? 'GET'
      const path = url.replace('/api/v1', '').split('?')[0]
      calls.push({ method, url: path, body: init?.body ? JSON.parse(String(init.body)) : undefined })
      const r = routes[`${method} ${path}`] ?? { status: 404, body: { error: { code: 'not_found', message: 'nf' } } }
      return new Response(JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => {
  vi.unstubAllGlobals()
})

function renderPage() {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <UIProvider>
        <DevicesPage />
      </UIProvider>
    </QueryClientProvider>,
  )
}

async function openProfiles() {
  renderPage()
  fireEvent.click(await screen.findByRole('button', { name: /Profiles/ }))
  return screen.findByRole('region', { name: 'Profiles on LAPTOP-1' })
}

describe('ModeBadges', () => {
  it('shows mode and reported modules, enabled and failed', () => {
    render(<ModeBadges device={{ install_mode: 'advanced', modules_summary: { enabled: ['core', 'sync'], failed: ['vss'], updated_at: Date.now() } }} />)
    expect(screen.getByText('Advanced mode')).toBeTruthy()
    expect(screen.getByText('Sync')).toBeTruthy()
    expect(screen.getByText('VSS failed')).toBeTruthy()
  })
  it('never infers modules before the first report', () => {
    render(<ModeBadges device={{ install_mode: 'simple', modules_summary: { enabled: [], failed: [], updated_at: 0 } }} />)
    expect(screen.getByText('Simple mode')).toBeTruthy()
    expect(screen.getByText('Modules not reported')).toBeTruthy()
  })
  it('renders nothing for servers without modules', () => {
    const { container } = render(<ModeBadges device={{}} />)
    expect(container.innerHTML).toBe('')
  })
})

describe('Devices page', () => {
  it('shows mode badges and the server key status', async () => {
    renderPage()
    expect(await screen.findByText('Advanced mode')).toBeTruthy()
    expect(screen.getByText('VSS failed')).toBeTruthy()
    expect((await screen.findByTestId('sik-fingerprint')).textContent).toBe('ABCD-0123-4567-89AB-CDEF-0123-4567-89AB')
    expect(screen.getByTestId('sek-version').textContent).toBe('v3')
  })

  it('shows the unavailable key state on 409 escrow_unavailable', async () => {
    routes['GET /keys/server'] = { status: 409, body: { error: { code: 'escrow_unavailable', message: 'server keys are not configured' } } }
    renderPage()
    expect(await screen.findByText('Unavailable')).toBeTruthy()
    expect(screen.getByText(/server keys are not configured/)).toBeTruthy()
  })

  it('hides the profiles panel without profiles.v1', async () => {
    routes['GET /info'] = { status: 200, body: { features: [] } }
    renderPage()
    await screen.findByText('LAPTOP-1')
    await waitFor(() => expect(calls.some((c) => c.url === '/info')).toBe(true))
    expect(screen.queryByRole('button', { name: /Profiles/ })).toBeNull()
  })

  it('lists profile states and requests consent for selected and all', async () => {
    const panel = await openProfiles()
    expect(await within(panel).findByText('Consent requested')).toBeTruthy()
    expect(within(panel).getByText('Blacklisted')).toBeTruthy()
    expect(within(panel).getByText('Active user')).toBeTruthy()
    expect(within(panel).getByText(/Password changed/)).toBeTruthy()
    // Only requestable profiles get a checkbox.
    expect(within(panel).queryByLabelText('Select eve')).toBeNull()
    expect(within(panel).queryByLabelText('Select alice')).toBeNull()
    fireEvent.click(within(panel).getByLabelText('Select bob'))
    fireEvent.click(within(panel).getByRole('button', { name: /Request consent \(1\)/ }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST')?.body).toEqual({ sids: ['S-1-5-21-1'] }))
    fireEvent.click(within(panel).getByRole('button', { name: /for all profiles/ }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'POST')[1]?.body).toEqual({ all: true }))
  })

  it('superuser clears a blacklist with typed confirmation', async () => {
    const panel = await openProfiles()
    fireEvent.click(await within(panel).findByRole('button', { name: /Clear blacklist/ }))
    const dialog = await screen.findByRole('dialog')
    const confirmBtn = within(dialog).getByRole('button', { name: 'Clear blacklist' }) as HTMLButtonElement
    expect(confirmBtn.disabled).toBe(true)
    fireEvent.change(within(dialog).getByLabelText(/to confirm/), { target: { value: 'eve' } })
    expect(confirmBtn.disabled).toBe(false)
    fireEvent.click(confirmBtn)
    await waitFor(() => expect(calls.find((c) => c.url === '/admin/device-profiles/2/clear')?.body).toEqual({ confirm_text: 'eve' }))
  })

  it('shows not_superuser verbatim', async () => {
    routes['POST /admin/device-profiles/2/clear'] = { status: 403, body: { error: { code: 'not_superuser', message: 'superuser required' } } }
    const panel = await openProfiles()
    fireEvent.click(await within(panel).findByRole('button', { name: /Clear blacklist/ }))
    const dialog = await screen.findByRole('dialog')
    fireEvent.change(within(dialog).getByLabelText(/to confirm/), { target: { value: 'eve' } })
    fireEvent.click(within(dialog).getByRole('button', { name: 'Clear blacklist' }))
    expect((await within(panel).findByRole('alert')).textContent).toBe('not_superuser: superuser required')
  })

  it('hides clear for a known non-superuser admin', async () => {
    routes['GET /admin/users'] = { status: 200, body: { users: [{ id: 1, is_superuser: false }] } }
    const panel = await openProfiles()
    expect(await within(panel).findByText(/Only a superuser can clear/)).toBeTruthy()
    expect(within(panel).queryByRole('button', { name: /Clear blacklist/ })).toBeNull()
  })

  it('hides admin actions from plain users and never asks /admin/users', async () => {
    role = 'user'
    const panel = await openProfiles()
    await within(panel).findByText('Blacklisted')
    expect(within(panel).queryByRole('button', { name: /Request consent/ })).toBeNull()
    expect(within(panel).queryByRole('button', { name: /Clear blacklist/ })).toBeNull()
    expect(within(panel).queryByRole('checkbox')).toBeNull()
    expect(calls.some((c) => c.url === '/admin/users')).toBe(false)
  })
})
