import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, ChevronLeft, ChevronRight, Eye, Keyboard, Merge, Pause, Play, Plus, Repeat, Scissors, SkipForward, Trash2, X } from 'lucide-react'
import { Badge, Button, cx, EmptyState, ErrorState, IconButton, Modal, PageLoader, Tabs, useUI } from '../../components/ui'
import { contentURL } from '../../components/files/types'
import { errorMessage } from '../../lib/api'
import { categoryLabel, categoryTone, fmtTime, itemStateLabel, itemStateTone, smartApi, smartKeys, type ItemDetail, type Segment } from './api'
import { categoryColor, ReviewTimeline } from './ReviewTimeline'

const SHORTCUTS: [string, string][] = [
  ['Space', 'Play / pause'],
  ['← / →', 'Back / forward 1 s (Shift: 5 s)'],
  ['J / K', 'Previous / next segment'],
  ['P', 'Play the selected segment'],
  ['A / R', 'Approve / reject the selected segment'],
  ['I / O', 'Set the selected segment start / end to the playhead'],
  ['S', 'Split the selected segment at the playhead'],
  ['M', 'Merge the selected segment with the next one'],
  ['N', 'New segment at the playhead'],
  ['Delete', 'Remove (reject) the selected segment'],
  ['Enter', 'Done: render approved clips and open the next recording'],
  ['L', 'Skip this recording for now'],
  ['?', 'Show these shortcuts'],
]

/** Review: the next recording waiting for review, or the one in the URL. */
export default function ReviewPage() {
  const { itemId } = useParams()
  const [params] = useSearchParams()
  const queueId = Number(params.get('queue')) || undefined
  const navigate = useNavigate()
  const next = useQuery({ queryKey: smartKeys.sub('review-next', queueId ?? 0), queryFn: () => smartApi.nextReview({ queue_id: queueId }), enabled: !itemId })
  useEffect(() => {
    if (!itemId && next.data?.item) navigate(`/smart/review/${next.data.item.id}${queueId ? `?queue=${queueId}` : ''}`, { replace: true })
  }, [itemId, next.data, navigate, queueId])
  if (!itemId) {
    if (next.isLoading) return <PageLoader />
    if (next.error) return <ErrorState error={next.error} onRetry={() => next.refetch()} />
    return <EmptyState icon={<Eye className="size-6" />} title="Nothing to review" description="Recordings appear here when their analysis is finished." action={<Link to="/smart/queues"><Button>Queues</Button></Link>} />
  }
  return <Reviewer key={itemId} id={Number(itemId)} queueId={queueId} />
}

function Reviewer({ id, queueId }: { id: number; queueId?: number }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const q = useQuery({ queryKey: smartKeys.detail(`item-${id}`), queryFn: () => smartApi.item(id) })
  const video = useRef<HTMLVideoElement>(null)
  const [time, setTime] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [rate, setRate] = useState(1)
  const [selected, setSelected] = useState<number | null>(null)
  const [loopSeg, setLoopSeg] = useState<Segment | null>(null)
  const [cats, setCats] = useState<Set<string>>(new Set())
  const [panel, setPanel] = useState<'segments' | 'transcript'>('segments')
  const [help, setHelp] = useState(false)
  const [saving, setSaving] = useState(0)

  const data = q.data
  const segs = useMemo(() => (data?.segments ?? []).filter((g) => cats.size === 0 || cats.has(g.category) || g.origin === 'manual'), [data, cats])
  const sel = data?.segments.find((g) => g.id === selected) ?? null

  const reload = useCallback(() => qc.invalidateQueries({ queryKey: smartKeys.detail(`item-${id}`) }), [qc, id])
  const setData = (fn: (d: ItemDetail) => ItemDetail) => qc.setQueryData<ItemDetail>(smartKeys.detail(`item-${id}`), (d) => (d ? fn(d) : d))

  // Every change is saved at once (autosave); the list is refreshed from the server afterwards.
  const run = async (fn: () => Promise<unknown>, what: string) => {
    setSaving((n) => n + 1)
    try {
      await fn()
    } catch (e) {
      toast('error', `${what} failed`, errorMessage(e))
    } finally {
      setSaving((n) => n - 1)
      reload()
    }
  }
  const patchSeg = (g: Segment, p: Partial<Segment>) => {
    setData((d) => ({ ...d, segments: d.segments.map((x) => (x.id === g.id ? { ...x, ...p } : x)) }))
    return run(() => smartApi.updateSegment(g.id, p), 'Saving the segment')
  }

  const seek = (t: number) => {
    const v = video.current
    if (v) v.currentTime = Math.max(0, Math.min(t, data?.item.duration || v.duration || t))
    setTime(t)
  }
  const select = (g: Segment | null, jump = true) => {
    setSelected(g?.id ?? null)
    if (g && jump) seek(g.start)
  }
  const ordered = useMemo(() => [...segs].sort((a, b) => a.start - b.start), [segs])
  const step = (d: 1 | -1) => {
    if (!ordered.length) return
    const i = ordered.findIndex((g) => g.id === selected)
    const n = i < 0 ? (d > 0 ? 0 : ordered.length - 1) : Math.min(Math.max(i + d, 0), ordered.length - 1)
    select(ordered[n])
  }
  const togglePlay = () => {
    const v = video.current
    if (!v) return
    if (v.paused) v.play().catch(() => {})
    else v.pause()
  }
  const playSegment = (g: Segment) => {
    setLoopSeg(g)
    seek(g.start)
    video.current?.play().catch(() => {})
  }

  const goNext = async () => {
    const r = await smartApi.nextReview({ queue_id: queueId, after: id })
    qc.invalidateQueries({ queryKey: smartKeys.all })
    if (r.item && r.item.id !== id) navigate(`/smart/review/${r.item.id}${queueId ? `?queue=${queueId}` : ''}`)
    else navigate(`/smart/review${queueId ? `?queue=${queueId}` : ''}`)
  }
  const finish = async () => {
    if (!data) return
    const open = data.segments.filter((g) => g.state === 'suggested').length
    if (open) {
      const ok = await confirm({ title: `${open} suggestion(s) not decided`, message: 'Reject the undecided suggestions and render the approved segments?', confirmLabel: 'Reject the rest and finish', tone: 'primary' })
      if (!ok) return
    }
    try {
      await smartApi.itemAction(id, 'review', { reject_rest: true })
      const n = data.segments.filter((g) => g.state === 'approved').length
      toast('success', n ? `${n} clip(s) queued for rendering` : 'Reviewed: nothing kept', n ? 'They are rendered in the background.' : 'The original is listed in Cleanup as having no approved segments.')
      goNext()
    } catch (e) {
      toast('error', 'Not finished', errorMessage(e))
    }
  }
  const skip = async () => {
    await run(() => smartApi.itemAction(id, 'skip'), 'Skipping')
    goNext()
  }
  const newSegment = () => {
    if (!data) return
    const d = data.item.duration || 0
    const s = Math.max(0, time - 2)
    run(async () => {
      const g = await smartApi.createSegment(id, { start: s, end: Math.min(d || s + 10, s + 10) })
      setSelected(g.id)
    }, 'Adding a segment')
  }
  const split = (g: Segment) => run(() => smartApi.splitSegment(g.id, time), 'Splitting')
  const mergeNext = (g: Segment) => {
    const i = ordered.findIndex((x) => x.id === g.id)
    const nx = ordered[i + 1]
    if (!nx) return toast('info', 'No following segment to merge with')
    run(async () => {
      const m = await smartApi.mergeSegments([g.id, nx.id])
      setSelected(m.id)
    }, 'Merging')
  }
  const removeSeg = (g: Segment) => {
    if (g.origin === 'manual' || g.origin === 'split' || g.origin === 'merged') {
      setData((d) => ({ ...d, segments: d.segments.filter((x) => x.id !== g.id) }))
      run(() => smartApi.deleteSegment(g.id), 'Removing')
    } else patchSeg(g, { state: 'rejected' })
  }
  const bulk = (state: 'approved' | 'rejected', ids: number[]) => run(() => smartApi.bulkSegments({ ids, state }), 'Updating segments')

  // Keyboard shortcuts.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const t = e.target
      if ((t instanceof Element && t.closest('input, textarea, select, [contenteditable="true"]')) || e.ctrlKey || e.metaKey || e.altKey) return
      const k = e.key.length === 1 ? e.key.toLowerCase() : e.key
      const always: Record<string, () => void> = {
        ' ': togglePlay,
        ArrowLeft: () => seek(time - (e.shiftKey ? 5 : 1)),
        ArrowRight: () => seek(time + (e.shiftKey ? 5 : 1)),
        j: () => step(-1),
        k: () => step(1),
        n: newSegment,
        Enter: finish,
        l: skip,
        '?': () => setHelp(true),
      }
      const onSelected: Record<string, (g: Segment) => void> = {
        a: (g) => patchSeg(g, { state: 'approved' }),
        r: (g) => patchSeg(g, { state: 'rejected' }),
        p: playSegment,
        s: split,
        m: mergeNext,
        i: (g) => time < g.end - 0.3 && patchSeg(g, { start: Math.round(time * 10) / 10 }),
        o: (g) => time > g.start + 0.3 && patchSeg(g, { end: Math.round(time * 10) / 10 }),
        Delete: removeSeg,
        Backspace: removeSeg,
      }
      if (always[k]) {
        e.preventDefault()
        always[k]()
      } else if (sel && onSelected[k]) {
        e.preventDefault()
        onSelected[k](sel)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  })

  // Select the first segment once loaded.
  useEffect(() => {
    if (selected === null && ordered.length) setSelected(ordered[0].id)
  }, [ordered, selected])

  if (q.isLoading) return <PageLoader />
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  const d = data!
  const it = d.item
  const allCats = [...new Set(d.segments.map((g) => g.category))]
  const counts = { approved: d.segments.filter((g) => g.state === 'approved').length, suggested: d.segments.filter((g) => g.state === 'suggested').length, rejected: d.segments.filter((g) => g.state === 'rejected').length }
  const reviewable = ['ready', 'skipped', 'reviewed', 'done', 'failed'].includes(it.state)

  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <h1 className="truncate text-lg font-semibold text-slate-900 dark:text-white" title={it.path}>
              {it.name}
            </h1>
            <Badge tone={itemStateTone(it.state)}>{itemStateLabel[it.state]}</Badge>
            {saving > 0 && <span className="text-xs text-slate-400">saving…</span>}
          </div>
          <div className="truncate text-xs text-slate-500">
            {d.queue && (
              <Link className="text-brand-600" to={`/smart/queues/${d.queue.id}`}>
                {d.queue.name}
              </Link>
            )}{' '}
            · {it.path} · {counts.approved} approved, {counts.suggested} open, {counts.rejected} rejected
          </div>
        </div>
        <IconButton label="Keyboard shortcuts" onClick={() => setHelp(true)}>
          <Keyboard className="size-4" />
        </IconButton>
        <Button icon={<ChevronLeft className="size-4" />} onClick={async () => {
          const r = await smartApi.nextReview({ queue_id: queueId, after: id, dir: 'prev' })
          if (r.item) navigate(`/smart/review/${r.item.id}${queueId ? `?queue=${queueId}` : ''}`)
        }}>
          Previous
        </Button>
        <Button icon={<SkipForward className="size-4" />} onClick={skip} disabled={it.state !== 'ready'}>
          Skip for now
        </Button>
        {it.state === 'ready' || it.state === 'skipped' ? (
          <Button variant="primary" icon={<Check className="size-4" />} onClick={finish}>
            Done ({counts.approved} clip{counts.approved === 1 ? '' : 's'})
          </Button>
        ) : (
          reviewable && (
            <Button onClick={() => run(() => smartApi.itemAction(id, 'reopen'), 'Reopening')}>Reopen for review</Button>
          )
        )}
        <Button icon={<ChevronRight className="size-4" />} onClick={goNext}>
          Next
        </Button>
      </div>

      {!d.file_exists && (
        <div className="card flex items-center gap-2 border-rose-300 p-3 text-sm text-rose-700">
          <AlertTriangle className="size-4" /> The original is no longer in its folder; it cannot be played or rendered.
        </div>
      )}
      {(d.timeline?.warnings ?? it.warnings).length > 0 && (
        <details className="card border-amber-300 p-3 text-sm text-amber-800 dark:text-amber-200">
          <summary className="cursor-pointer">
            <AlertTriangle className="inline size-4" /> {(d.timeline?.warnings ?? it.warnings).length} detection note(s)
          </summary>
          <ul className="mt-2 list-disc pl-5 text-xs">
            {(d.timeline?.warnings ?? it.warnings).map((w, i) => (
              <li key={i}>{w}</li>
            ))}
          </ul>
        </details>
      )}

      <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_380px]">
        <div className="space-y-3">
          <div className="overflow-hidden rounded-xl bg-black">
            <video
              ref={video}
              src={d.file_exists ? contentURL(it.node_id) : undefined}
              className="aspect-video w-full"
              preload="metadata"
              onClick={togglePlay}
              onPlay={() => setPlaying(true)}
              onPause={() => setPlaying(false)}
              onTimeUpdate={(e) => {
                const t = e.currentTarget.currentTime
                setTime(t)
                if (loopSeg && t >= loopSeg.end) {
                  e.currentTarget.pause()
                  setLoopSeg(null)
                }
              }}
            />
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Button size="sm" icon={playing ? <Pause className="size-4" /> : <Play className="size-4" />} onClick={togglePlay}>
              {playing ? 'Pause' : 'Play'}
            </Button>
            {sel && (
              <Button size="sm" icon={<Repeat className="size-4" />} onClick={() => playSegment(sel)}>
                Play segment
              </Button>
            )}
            <select
              className="input h-8 w-auto py-0"
              aria-label="Playback speed"
              value={rate}
              onChange={(e) => {
                const r = Number(e.target.value)
                setRate(r)
                if (video.current) video.current.playbackRate = r
              }}
            >
              {[0.5, 1, 1.5, 2, 3].map((r) => (
                <option key={r} value={r}>
                  {r}×
                </option>
              ))}
            </select>
            <span className="font-mono text-sm text-slate-500">{fmtTime(time)}</span>
            <div className="flex-1" />
            <Button size="sm" icon={<Plus className="size-4" />} onClick={newSegment}>
              New segment here
            </Button>
          </div>
          <ReviewTimeline
            duration={it.duration}
            time={time}
            timeline={d.timeline}
            segments={segs}
            selected={selected}
            categories={cats}
            onSeek={seek}
            onSelect={(sid) => select(d.segments.find((g) => g.id === sid) ?? null, false)}
            onChange={(sid, start, end) => {
              const g = d.segments.find((x) => x.id === sid)
              if (g) patchSeg(g, { start, end })
            }}
          />
        </div>

        <div className="space-y-3">
          <Tabs
            value={panel}
            onChange={setPanel}
            tabs={[
              { id: 'segments', label: 'Segments', count: d.segments.length },
              { id: 'transcript', label: 'Transcript' },
            ]}
          />
          {panel === 'segments' ? (
            <>
              {allCats.length > 1 && (
                <div className="flex flex-wrap gap-1">
                  {allCats.map((c) => {
                    const on = cats.has(c)
                    return (
                      <button
                        key={c}
                        type="button"
                        aria-pressed={on}
                        onClick={() => {
                          const s = new Set(cats)
                          if (on) s.delete(c)
                          else s.add(c)
                          setCats(s)
                        }}
                        className={cx('flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs', on ? 'border-brand-500 bg-brand-50 dark:bg-brand-500/15' : 'border-slate-300 dark:border-slate-600')}
                      >
                        <span className={cx('size-2 rounded-full', categoryColor(c))} />
                        {categoryLabel[c] ?? c}
                      </button>
                    )
                  })}
                </div>
              )}
              <div className="flex flex-wrap gap-2">
                <Button size="sm" onClick={() => bulk('approved', segs.filter((g) => g.state === 'suggested').map((g) => g.id))} disabled={!segs.some((g) => g.state === 'suggested')}>
                  Approve all shown
                </Button>
                <Button size="sm" onClick={() => bulk('rejected', segs.filter((g) => g.state === 'suggested').map((g) => g.id))} disabled={!segs.some((g) => g.state === 'suggested')}>
                  Reject all shown
                </Button>
                <Button size="sm" variant="ghost" onClick={() => bulk('approved', segs.filter((g) => g.state === 'suggested' && g.score >= 0.75).map((g) => g.id))}>
                  Approve ≥ 75%
                </Button>
              </div>
              <div className="max-h-[60vh] space-y-2 overflow-y-auto pr-1">
                {ordered.length === 0 && <p className="text-sm text-slate-500">No segments. Add one with “New segment here” (N).</p>}
                {ordered.map((g) => (
                  <SegmentCard
                    key={g.id}
                    g={g}
                    selected={g.id === selected}
                    onSelect={() => select(g)}
                    onState={(s) => patchSeg(g, { state: s })}
                    onName={(name) => patchSeg(g, { name })}
                    onPlay={() => playSegment(g)}
                    onSplit={() => split(g)}
                    onMerge={() => mergeNext(g)}
                    onRemove={() => removeSeg(g)}
                  />
                ))}
              </div>
              {d.outputs.length > 0 && (
                <div className="card p-3 text-xs">
                  <div className="mb-1 font-medium">Clips</div>
                  {d.outputs.map((o) => (
                    <div key={o.id} className="flex justify-between gap-2">
                      <span className="truncate" title={o.path}>
                        {o.name || `segment ${o.segment_id}`}
                      </span>
                      <Badge tone={o.state === 'done' ? 'green' : o.state === 'failed' ? 'red' : 'blue'}>{o.state}</Badge>
                    </div>
                  ))}
                </div>
              )}
            </>
          ) : (
            <TranscriptPanel id={id} time={time} onSeek={seek} />
          )}
        </div>
      </div>

      <Modal open={help} onClose={() => setHelp(false)} title="Keyboard shortcuts" size="sm">
        <table className="w-full text-sm">
          <tbody>
            {SHORTCUTS.map(([k, v]) => (
              <tr key={k}>
                <td className="py-1 pr-3 font-mono text-xs whitespace-nowrap">{k}</td>
                <td className="py-1 text-slate-600 dark:text-slate-300">{v}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </Modal>
    </div>
  )
}

function SegmentCard({ g, selected, onSelect, onState, onName, onPlay, onSplit, onMerge, onRemove }: { g: Segment; selected: boolean; onSelect: () => void; onState: (s: Segment['state']) => void; onName: (n: string) => void; onPlay: () => void; onSplit: () => void; onMerge: () => void; onRemove: () => void }) {
  const [name, setName] = useState(g.name)
  useEffect(() => setName(g.name), [g.name])
  return (
    <div onClick={onSelect} className={cx('cursor-pointer rounded-xl border p-3 text-sm transition', selected ? 'border-brand-500 ring-2 ring-brand-500/20' : 'border-slate-200 dark:border-slate-700', g.state === 'rejected' && 'opacity-60')}>
      <div className="flex items-center gap-2">
        <span className="font-mono text-xs">
          {fmtTime(g.start)}–{fmtTime(g.end)}
        </span>
        <span className="text-xs text-slate-500">{(g.end - g.start).toFixed(1)}s</span>
        <Badge tone={categoryTone[g.category] ?? 'slate'}>{g.label || categoryLabel[g.category]}</Badge>
        <div className="flex-1" />
        <span className="text-xs text-slate-500" title="Confidence">
          {Math.round(g.score * 100)}%
        </span>
      </div>
      {g.reasons.length > 0 && (
        <ul className="mt-1.5 space-y-0.5 text-xs text-slate-600 dark:text-slate-300">
          {g.reasons.slice(0, 4).map((r, i) => (
            <li key={i}>
              • {r.text} <span className="text-slate-400">({Math.round(r.score * 100)}%, {r.detector})</span>
            </li>
          ))}
          {g.reasons.length > 4 && <li className="text-slate-400">+{g.reasons.length - 4} more</li>}
        </ul>
      )}
      {selected && (
        <input
          className="input mt-2 h-8 py-0 text-xs"
          placeholder="Clip name (optional)"
          value={name}
          maxLength={120}
          onClick={(e) => e.stopPropagation()}
          onChange={(e) => setName(e.target.value)}
          onBlur={() => name !== g.name && onName(name)}
        />
      )}
      <div className="mt-2 flex flex-wrap items-center gap-1" onClick={(e) => e.stopPropagation()}>
        <Button size="sm" variant={g.state === 'approved' ? 'primary' : 'secondary'} icon={<Check className="size-3.5" />} onClick={() => onState(g.state === 'approved' ? 'suggested' : 'approved')}>
          {g.state === 'approved' ? 'Approved' : 'Approve'}
        </Button>
        <Button size="sm" variant={g.state === 'rejected' ? 'danger' : 'ghost'} icon={<X className="size-3.5" />} onClick={() => onState(g.state === 'rejected' ? 'suggested' : 'rejected')}>
          {g.state === 'rejected' ? 'Rejected' : 'Reject'}
        </Button>
        <div className="flex-1" />
        <IconButton label="Play segment" onClick={onPlay}>
          <Play className="size-3.5" />
        </IconButton>
        <IconButton label="Split at playhead" onClick={onSplit}>
          <Scissors className="size-3.5" />
        </IconButton>
        <IconButton label="Merge with next" onClick={onMerge}>
          <Merge className="size-3.5" />
        </IconButton>
        <IconButton label="Remove" onClick={onRemove}>
          <Trash2 className="size-3.5" />
        </IconButton>
      </div>
      <div className="mt-1 text-[10px] text-slate-400">{g.origin}</div>
    </div>
  )
}

function TranscriptPanel({ id, time, onSeek }: { id: number; time: number; onSeek: (t: number) => void }) {
  const t = useQuery({ queryKey: smartKeys.sub('transcript', id), queryFn: () => smartApi.transcript(id) })
  const [q, setQ] = useState('')
  if (t.isLoading) return <PageLoader />
  if (!t.data?.available) return <p className="text-sm text-slate-500">No transcript: speech to text was not part of this queue's detection or its model is not installed.</p>
  const needle = q.trim().toLowerCase()
  const list = t.data.utterances.filter((u) => !needle || u.text.toLowerCase().includes(needle))
  return (
    <div className="space-y-2">
      <input className="input h-8 py-0" placeholder="Search the transcript" value={q} onChange={(e) => setQ(e.target.value)} />
      <p className="text-xs text-slate-500">
        {t.data.language} · {t.data.speakers ? `${t.data.speakers} voice(s), numbered anonymously` : 'speakers not separated'}
      </p>
      <div className="max-h-[60vh] space-y-1 overflow-y-auto text-sm">
        {list.map((u, i) => (
          <button key={i} type="button" onClick={() => onSeek(u.start)} className={cx('block w-full rounded-lg px-2 py-1 text-left hover:bg-slate-100 dark:hover:bg-slate-800', time >= u.start && time < u.end && 'bg-brand-50 dark:bg-brand-500/10')}>
            <span className="mr-2 font-mono text-[11px] text-slate-400">{fmtTime(u.start)}</span>
            {u.speaker > 0 && <span className="mr-1 text-xs font-medium text-slate-500">Speaker {u.speaker}:</span>}
            {u.text}
          </button>
        ))}
      </div>
    </div>
  )
}
