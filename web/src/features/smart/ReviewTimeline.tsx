import { useEffect, useMemo, useRef, useState, type PointerEvent as RPointerEvent } from 'react'
import { ZoomIn, ZoomOut } from 'lucide-react'
import { cx, IconButton } from '../../components/ui'
import { fmtTime, type Segment, type Timeline } from './api'

const catColor: Record<string, string> = {
  emotion: 'bg-violet-500',
  conversation: 'bg-sky-500',
  gaming: 'bg-emerald-500',
  visual: 'bg-amber-500',
  audio: 'bg-rose-500',
  speech: 'bg-blue-500',
  custom: 'bg-slate-500',
}

export function categoryColor(c: string) {
  return catColor[c] ?? 'bg-slate-500'
}

type Drag = { id: number; edge: 'start' | 'end' | 'move'; x0: number; s0: number; e0: number }

/**
 * The review timeline: loudness and motion curves, speech, detected moments and the segments.
 * Click to seek; drag a segment's edges (or body) to adjust it.
 */
export function ReviewTimeline({
  duration,
  time,
  timeline,
  segments,
  selected,
  categories,
  onSeek,
  onSelect,
  onChange,
}: {
  duration: number
  time: number
  timeline: Timeline | null
  segments: Segment[]
  selected: number | null
  categories: Set<string>
  onSeek: (t: number) => void
  onSelect: (id: number) => void
  onChange: (id: number, start: number, end: number) => void
}) {
  const [zoom, setZoom] = useState(1)
  const [drag, setDrag] = useState<Drag | null>(null)
  const [preview, setPreview] = useState<{ id: number; start: number; end: number } | null>(null)
  const scroller = useRef<HTMLDivElement>(null)
  const inner = useRef<HTMLDivElement>(null)
  const dur = Math.max(duration, 0.1)
  const pctOf = (t: number) => `${(Math.min(Math.max(t, 0), dur) / dur) * 100}%`

  // Keep the playhead in view when zoomed.
  useEffect(() => {
    const sc = scroller.current
    const el = inner.current
    if (!sc || !el || zoom === 1) return
    const x = (time / dur) * el.clientWidth
    if (x < sc.scrollLeft || x > sc.scrollLeft + sc.clientWidth - 20) sc.scrollLeft = Math.max(0, x - sc.clientWidth / 3)
  }, [time, dur, zoom])

  const timeAt = (clientX: number) => {
    const el = inner.current
    if (!el) return 0
    const r = el.getBoundingClientRect()
    return Math.min(Math.max(((clientX - r.left) / r.width) * dur, 0), dur)
  }

  const curve = (vals: number[] | undefined, lo: number, hi: number) => {
    if (!vals?.length) return ''
    const n = vals.length
    const pts = vals.map((v, i) => `${(i / Math.max(n - 1, 1)) * 1000},${30 - ((Math.min(Math.max(v, lo), hi) - lo) / (hi - lo)) * 30}`)
    return `M0,30 L${pts.join(' L')} L1000,30 Z`
  }
  const loud = useMemo(() => curve(timeline?.loudness, -60, 0), [timeline])
  const motion = useMemo(() => curve(timeline?.motion, 0, 0.3), [timeline])

  const onPointerMove = (e: RPointerEvent) => {
    if (!drag) return
    const el = inner.current
    if (!el) return
    const dt = ((e.clientX - drag.x0) / el.getBoundingClientRect().width) * dur
    let s = drag.s0
    let en = drag.e0
    if (drag.edge === 'start') s = Math.min(Math.max(drag.s0 + dt, 0), drag.e0 - 0.3)
    else if (drag.edge === 'end') en = Math.max(Math.min(drag.e0 + dt, dur), drag.s0 + 0.3)
    else {
      const len = drag.e0 - drag.s0
      s = Math.min(Math.max(drag.s0 + dt, 0), dur - len)
      en = s + len
    }
    setPreview({ id: drag.id, start: s, end: en })
  }
  const onPointerUp = () => {
    if (drag && preview) onChange(preview.id, Math.round(preview.start * 10) / 10, Math.round(preview.end * 10) / 10)
    setDrag(null)
    setPreview(null)
  }

  const ticks = useMemo(() => {
    const step = [1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800].find((s) => dur / s <= 12 * zoom) ?? 3600
    const out: number[] = []
    for (let t = 0; t <= dur; t += step) out.push(t)
    return out
  }, [dur, zoom])

  return (
    <div className="card select-none p-3">
      <div className="mb-2 flex items-center gap-2 text-xs text-slate-500">
        <span className="font-mono">
          {fmtTime(time)} / {fmtTime(dur)}
        </span>
        <div className="flex-1" />
        <IconButton label="Zoom out" onClick={() => setZoom(Math.max(1, zoom / 2))}>
          <ZoomOut className="size-4" />
        </IconButton>
        <span>{zoom}×</span>
        <IconButton label="Zoom in" onClick={() => setZoom(Math.min(64, zoom * 2))}>
          <ZoomIn className="size-4" />
        </IconButton>
      </div>
      <div ref={scroller} className="overflow-x-auto">
        <div ref={inner} className="relative" style={{ width: `${zoom * 100}%`, minWidth: '100%' }} onPointerMove={onPointerMove} onPointerUp={onPointerUp} onPointerLeave={onPointerUp}>
          {/* ruler */}
          <div className="relative h-5 cursor-pointer border-b border-slate-200 dark:border-slate-700" onClick={(e) => onSeek(timeAt(e.clientX))}>
            {ticks.map((t) => (
              <span key={t} className="absolute top-0 text-[10px] text-slate-400" style={{ left: pctOf(t) }}>
                {fmtTime(t).replace(/\.\d$/, '')}
              </span>
            ))}
          </div>
          {/* curves */}
          <div className="relative h-16 cursor-pointer" onClick={(e) => onSeek(timeAt(e.clientX))} title="Loudness (blue) and motion (orange)">
            <svg viewBox="0 0 1000 30" preserveAspectRatio="none" className="absolute inset-0 h-8 w-full">
              <path d={loud} className="fill-sky-400/40 stroke-sky-500" strokeWidth={0.5} vectorEffect="non-scaling-stroke" />
            </svg>
            <svg viewBox="0 0 1000 30" preserveAspectRatio="none" className="absolute inset-x-0 top-8 h-8 w-full">
              <path d={motion} className="fill-orange-400/40 stroke-orange-500" strokeWidth={0.5} vectorEffect="non-scaling-stroke" />
            </svg>
          </div>
          {/* speech */}
          <div className="relative h-2.5 bg-slate-100 dark:bg-slate-800" title="Speech">
            {(timeline?.speech ?? []).map((s, i) => (
              <div key={i} className="absolute inset-y-0 bg-blue-400/70" style={{ left: pctOf(s.start), width: `calc(${pctOf(s.end)} - ${pctOf(s.start)})` }} />
            ))}
          </div>
          {/* detected moments */}
          <div className="relative mt-1 h-4">
            {(timeline?.events ?? [])
              .filter((e) => categories.size === 0 || categories.has(e.category))
              .map((e, i) => (
                <div
                  key={i}
                  className={cx('absolute inset-y-0 rounded-sm opacity-70 hover:opacity-100', categoryColor(e.category))}
                  style={{ left: pctOf(e.start), width: `max(2px, calc(${pctOf(e.end)} - ${pctOf(e.start)}))` }}
                  title={`${e.rule_name}: ${e.reasons.map((r) => r.text).join('; ')} (${Math.round(e.score * 100)}%)`}
                  onClick={() => onSeek(e.start)}
                />
              ))}
          </div>
          {/* segments */}
          <div className="relative mt-1 h-9 rounded bg-slate-50 dark:bg-slate-800/50">
            {segments.map((g) => {
              const p = preview?.id === g.id ? preview : g
              const tone = g.state === 'approved' ? 'border-emerald-500 bg-emerald-500/25' : g.state === 'rejected' ? 'border-slate-400 bg-slate-400/15 opacity-50' : 'border-amber-500 bg-amber-400/25'
              return (
                <div
                  key={g.id}
                  className={cx('absolute inset-y-0 cursor-grab rounded border-2', tone, selected === g.id && 'ring-2 ring-brand-500')}
                  style={{ left: pctOf(p.start), width: `calc(${pctOf(p.end)} - ${pctOf(p.start)})` }}
                  title={`${g.label} ${fmtTime(p.start)}–${fmtTime(p.end)} (${Math.round(g.score * 100)}%)`}
                  onPointerDown={(e) => {
                    e.preventDefault()
                    onSelect(g.id)
                    ;(e.target as HTMLElement).setPointerCapture?.(e.pointerId)
                    setDrag({ id: g.id, edge: 'move', x0: e.clientX, s0: g.start, e0: g.end })
                  }}
                >
                  <span
                    className="absolute inset-y-0 left-0 w-2 cursor-ew-resize bg-black/10"
                    onPointerDown={(e) => {
                      e.stopPropagation()
                      e.preventDefault()
                      onSelect(g.id)
                      setDrag({ id: g.id, edge: 'start', x0: e.clientX, s0: g.start, e0: g.end })
                    }}
                  />
                  <span className="pointer-events-none block truncate px-2.5 text-[11px] leading-8 text-slate-700 dark:text-slate-200">{g.name || g.label}</span>
                  <span
                    className="absolute inset-y-0 right-0 w-2 cursor-ew-resize bg-black/10"
                    onPointerDown={(e) => {
                      e.stopPropagation()
                      e.preventDefault()
                      onSelect(g.id)
                      setDrag({ id: g.id, edge: 'end', x0: e.clientX, s0: g.start, e0: g.end })
                    }}
                  />
                </div>
              )
            })}
          </div>
          {/* playhead */}
          <div className="pointer-events-none absolute inset-y-0 w-px bg-rose-500" style={{ left: pctOf(time) }} />
        </div>
      </div>
    </div>
  )
}
