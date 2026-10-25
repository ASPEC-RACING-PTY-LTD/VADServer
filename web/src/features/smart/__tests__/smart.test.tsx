import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { UIProvider } from '../../../components/ui'
import CleanupPage from '../CleanupPage'
import ReviewPage from '../ReviewPage'
import { DescribeBox } from '../shared'
import { fmtTime, type ItemDetail, type Segment } from '../api'

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
      return new Response(r.status === 204 ? null : JSON.stringify(r.body), { status: r.status, headers: { 'Content-Type': 'application/json' } })
    }),
  )
})
afterEach(() => vi.unstubAllGlobals())

function wrap(ui: React.ReactNode, path: string, route: string) {
  const qc = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={qc}>
      <UIProvider>
        <MemoryRouter initialEntries={[path]}>
          <Routes>
            <Route path={route} element={ui} />
          </Routes>
        </MemoryRouter>
      </UIProvider>
    </QueryClientProvider>,
  )
}

const ok = (body: unknown): Reply => ({ status: 200, body })

describe('cleanup', () => {
  const items = [
    { item_id: 1, queue_id: 1, node_id: 11, name: 'a.mp4', path: '/rec/a.mp4', size: 2 * 1024 ** 3, status: 'eligible', clips: 2, clip_bytes: 100 * 1024 ** 2, approved: 2 },
    { item_id: 2, queue_id: 1, node_id: 12, name: 'b.mp4', path: '/rec/b.mp4', size: 1024 ** 3, status: 'eligible', clips: 1, clip_bytes: 50 * 1024 ** 2, approved: 1 },
    { item_id: 3, queue_id: 1, node_id: 13, name: 'c.mp4', path: '/rec/c.mp4', size: 512 * 1024 ** 2, status: 'no_segments', clips: 0, clip_bytes: 0, approved: 0 },
    { item_id: 4, queue_id: 1, node_id: 14, name: 'd.mp4', path: '/rec/d.mp4', size: 100, status: 'blocked', reason: 'waiting for review', clips: 0, clip_bytes: 0, approved: 0 },
  ]
  beforeEach(() => {
    handler = (c) => {
      if (c.url.startsWith('/api/v1/smart/queues')) return ok({ queues: [] })
      if (c.method === 'POST' && c.url === '/api/v1/smart/cleanup') return ok({ results: [{ item_id: 1, name: 'a.mp4', status: 'deleted', bytes: 2 * 1024 ** 3, freed: 2 * 1024 ** 3 }, { item_id: 2, name: 'b.mp4', status: 'skipped', reason: 'the file changed after it was processed', bytes: 0, freed: 0 }], removed: 1, bytes: 2 * 1024 ** 3, freed: 2 * 1024 ** 3 })
      if (c.url.startsWith('/api/v1/smart/cleanup')) return ok({ items, totals: { eligible: { count: 2, bytes: 3 * 1024 ** 3, clip_bytes: 150 * 1024 ** 2 }, no_segments: { count: 1, bytes: 512 * 1024 ** 2, clip_bytes: 0 }, blocked: { count: 1, bytes: 100, clip_bytes: 0 } } })
      return { status: 404, body: {} }
    }
  })

  it('confirms with count and size, without typing DELETE, and sends only the chosen originals', async () => {
    wrap(<CleanupPage />, '/smart/cleanup', '/smart/cleanup')
    expect(await screen.findByText('a.mp4')).toBeInTheDocument()
    // Originals not ready are hidden by default.
    expect(screen.queryByText('d.mp4')).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('button', { name: 'Select all ready' }))
    fireEvent.click(screen.getByRole('button', { name: /Remove 2 originals \(3\.0 GB\)/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/2 original recordings, 3\.0 GB in total/)).toBeInTheDocument()
    expect(within(dialog).queryByRole('textbox')).not.toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: /Delete \(3\.0 GB\)/ }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url === '/api/v1/smart/cleanup')).toBe(true))
    const post = calls.find((c) => c.method === 'POST' && c.url === '/api/v1/smart/cleanup')!
    expect(post.body).toEqual({ item_ids: [1, 2], mode: 'delete', include_no_segments: false })
    // Skipped originals are reported with their reason.
    expect(await screen.findByText(/the file changed after it was processed/)).toBeInTheDocument()
  })

  it('includes originals with nothing kept only when they are chosen', async () => {
    wrap(<CleanupPage />, '/smart/cleanup', '/smart/cleanup')
    await screen.findByText('c.mp4')
    fireEvent.click(screen.getByRole('button', { name: 'Select all with nothing kept' }))
    fireEvent.click(screen.getByRole('button', { name: /Remove 1 original \(512 MB\)/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText(/had no segments approved/)).toBeInTheDocument()
    fireEvent.click(within(dialog).getByLabelText(/Move to trash/))
    fireEvent.click(within(dialog).getByRole('button', { name: /Move to trash \(512 MB\)/ }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url === '/api/v1/smart/cleanup')?.body).toEqual({ item_ids: [3], mode: 'trash', include_no_segments: true }))
  })
})

describe('review', () => {
  const seg = (id: number, start: number, end: number, extra: Partial<Segment> = {}): Segment => ({
    id,
    item_id: 5,
    queue_id: 1,
    start,
    end,
    score: 0.8,
    state: 'suggested',
    origin: 'auto',
    category: 'emotion',
    label: 'Laughter',
    name: '',
    reasons: [{ kind: 'audio_event', detector: 'audio.events', text: 'Laughter heard (80%)', score: 0.8, start, end }],
    eval_rev: 1,
    created_at: 0,
    updated_at: 0,
    ...extra,
  })
  let detail: ItemDetail
  beforeEach(() => {
    detail = {
      item: { id: 5, queue_id: 1, node_id: 50, sha256: 'x', name: 'stream.mp4', path: '/rec/stream.mp4', size: 1000, duration: 120, state: 'ready', stage: '', progress: 1, attempts: 1, events: 3, eval_rev: 1, processing_ms: 0, position: 1, original_state: 'kept', deleted_bytes: 0, freed_bytes: 0, added_at: 0, updated_at: 0, warnings: [], detectors: [] },
      segments: [seg(1, 10, 20), seg(2, 40, 55, { category: 'speech', label: 'Keyword' })],
      outputs: [],
      timeline: { events: [], warnings: ['Speech emotion was not measured (emotion model)'], rate: 2 },
      queue: { id: 1, name: 'OmeTV', state: 'active' },
      file_exists: true,
    }
    handler = (c) => {
      if (c.url === '/api/v1/smart/items/5' && c.method === 'GET') return ok(detail)
      if (c.method === 'PATCH' && c.url.startsWith('/api/v1/smart/segments/')) {
        const id = Number(c.url.split('/').pop())
        const g = detail.segments.find((s) => s.id === id)!
        Object.assign(g, c.body)
        return ok(g)
      }
      if (c.url === '/api/v1/smart/items/5/actions/review') return ok({ ...detail, item: { ...detail.item, state: 'reviewed' } })
      if (c.url.startsWith('/api/v1/smart/review/next')) return ok({ item: null })
      if (c.url.startsWith('/api/v1/smart/items/5/transcript')) return ok({ available: false, utterances: [] })
      return { status: 404, body: {} }
    }
  })

  it('shows reasons and detection notes, and approves with the keyboard', async () => {
    wrap(<ReviewPage />, '/smart/review/5', '/smart/review/:itemId')
    expect(await screen.findByText('stream.mp4')).toBeInTheDocument()
    expect(screen.getAllByText(/Laughter heard \(80%\)/).length).toBeGreaterThan(0)
    expect(screen.getByText(/1 detection note/)).toBeInTheDocument()
    // The first segment is selected; A approves it, K moves to the next and R rejects that one.
    await screen.findByPlaceholderText('Clip name (optional)')
    fireEvent.keyDown(window, { key: 'a' })
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/v1/smart/segments/1' && (c.body as { state: string }).state === 'approved')).toBe(true))
    fireEvent.keyDown(window, { key: 'k' })
    fireEvent.keyDown(window, { key: 'r' })
    await waitFor(() => expect(calls.some((c) => c.method === 'PATCH' && c.url === '/api/v1/smart/segments/2' && (c.body as { state: string }).state === 'rejected')).toBe(true))
  })

  it('asks before rejecting undecided suggestions when finishing', async () => {
    wrap(<ReviewPage />, '/smart/review/5', '/smart/review/:itemId')
    await screen.findByText('stream.mp4')
    fireEvent.click(screen.getByRole('button', { name: /Done \(0 clips\)/ }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByText('2 suggestion(s) not decided')).toBeInTheDocument()
    fireEvent.click(within(dialog).getByRole('button', { name: 'Reject the rest and finish' }))
    await waitFor(() => expect(calls.find((c) => c.url === '/api/v1/smart/items/5/actions/review')?.body).toEqual({ reject_rest: true }))
  })

  it('does not react to shortcuts while typing a clip name', async () => {
    wrap(<ReviewPage />, '/smart/review/5', '/smart/review/:itemId')
    await screen.findByText('stream.mp4')
    const input = await screen.findByPlaceholderText('Clip name (optional)')
    fireEvent.keyDown(input, { key: 'a' })
    await new Promise((r) => setTimeout(r, 50))
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
  })
})

describe('describe box', () => {
  it('shows what is unsupported and what needs models', async () => {
    handler = () =>
      ok({
        description: 'clutch plays where I say nice',
        interpreter: 'rules',
        filter: { rules: [], context: {} },
        components: [{ rule: 'Keyword', kind: 'keyword', summary: 'says "nice"', detectors: ['speech.transcript'], available: false, missing: ['whisper'], reliability: 'Depends on transcription' }],
        unsupported: ['"clutch" has no reliable detector'],
        notes: ['"I" cannot be told apart from other speakers'],
        status: 'partial',
      })
    const onPlan = vi.fn()
    wrap(<DescribeBox onPlan={onPlan} />, '/', '/')
    fireEvent.change(screen.getByRole('textbox'), { target: { value: 'clutch plays where I say nice' } })
    fireEvent.click(screen.getByRole('button', { name: 'Interpret' }))
    expect(await screen.findByText('"clutch" has no reliable detector')).toBeInTheDocument()
    expect(screen.getByText(/needs whisper/)).toBeInTheDocument()
    expect(screen.getByText('partial')).toBeInTheDocument()
    expect(onPlan).toHaveBeenCalledOnce()
    expect(calls[0].body).toEqual({ description: 'clutch plays where I say nice' })
  })
})

describe('fmtTime', () => {
  it('formats seconds', () => {
    expect(fmtTime(0)).toBe('0:00.0')
    expect(fmtTime(65.25)).toBe('1:05.3')
    expect(fmtTime(3725)).toBe('1:02:05.0')
  })
})
