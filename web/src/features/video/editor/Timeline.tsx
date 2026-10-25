import { useEffect, useLayoutEffect, useMemo, useRef, useState, type PointerEvent as ReactPointerEvent, type WheelEvent as ReactWheelEvent } from 'react'
import { Maximize2, Minus, Plus, Scissors, SquarePlus, Trash2 } from 'lucide-react'
import { cx } from '../../../components/ui'
import { frameURL } from '../api'
import { clamp, formatLength, formatTime, laneLayout, MIN_CLIP, type Clip, type Edit } from '../model'

const RULER_H = 26
const STRIP_H = 58
const LANE_H = 30
const LANE_GAP = 4
const MAX_PX_PER_SEC = 600

// Ruler spacing and frame-sampling steps (seconds). Frames are taken on a grid of these steps so
// zooming in reuses the frames of coarser levels (and the server's cache).
const NICE = [0.05, 0.1, 0.25, 0.5, 1, 2, 5, 10, 15, 30, 60, 120, 300, 600, 900, 1800, 3600]
const niceAtLeast = (v: number) => NICE.find((s) => s >= v) ?? NICE[NICE.length - 1]

type DragKind = 'start' | 'end' | 'move' | 'playhead'

export type TimelineProps = {
  duration: number
  time: number
  playing: boolean
  clips: Clip[]
  selected: string | null
  sourceId: number
  sha?: string
  aspect: number
  onSeek: (t: number) => void
  onSelect: (id: string | null) => void
  onEdit: (e: Edit, coalesce: boolean) => void
  onAddAtPlayhead: () => void
  onSplit: () => void
  canSplit: boolean
}

function TButton({ label, onClick, disabled, children, active }: { label: string; onClick: () => void; disabled?: boolean; children: React.ReactNode; active?: boolean }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
      className={cx('flex h-8 items-center gap-1.5 rounded-lg px-2.5 text-xs font-medium transition disabled:opacity-35', active ? 'bg-brand-500/20 text-brand-200' : 'text-slate-300 hover:bg-white/[.07] hover:text-white')}
    >
      {children}
    </button>
  )
}

/**
 * Bottom timeline: frame thumbnails of the recording, a time ruler, the playhead and the numbered
 * selections (clips). Overlapping selections are drawn in separate lanes. Selections are moved by
 * dragging their body and trimmed by dragging their edges (on their bar, or on the frame strip for
 * the selected one); edges snap to the playhead and to other selections.
 */
export function Timeline(p: TimelineProps) {
  const { duration, clips, selected } = p
  const scroller = useRef<HTMLDivElement>(null)
  const [view, setView] = useState({ left: 0, width: 800 })
  const [pps, setPps] = useState(0) // pixels per second; 0 until measured (fit)
  const fit = Math.max(view.width - 2, 100) / Math.max(duration, 0.1)
  const zoom = pps || fit
  const width = Math.max(duration * zoom, view.width - 2)
  const { lanes, count } = useMemo(() => laneLayout(clips), [clips])
  const sel = clips.find((c) => c.id === selected)

  useLayoutEffect(() => {
    const el = scroller.current
    if (!el) return
    const ro = new ResizeObserver(() => setView({ left: el.scrollLeft, width: el.clientWidth }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const x = (t: number) => t * zoom
  const timeAt = (clientX: number) => {
    const el = scroller.current!
    const r = el.getBoundingClientRect()
    return clamp((clientX - r.left + el.scrollLeft) / zoom, 0, duration)
  }

  // Zoom around an anchor time kept under the same screen position.
  const zoomTo = (next: number, anchor = p.time) => {
    const el = scroller.current
    const z = clamp(next, fit, MAX_PX_PER_SEC)
    const screenX = el ? x(anchor) - el.scrollLeft : 0
    setPps(z <= fit * 1.001 ? 0 : z)
    requestAnimationFrame(() => {
      if (el) el.scrollLeft = Math.max(0, anchor * z - screenX)
    })
  }
  const onWheel = (e: ReactWheelEvent) => {
    if (!e.ctrlKey && !e.metaKey) return
    e.preventDefault()
    zoomTo(zoom * (e.deltaY < 0 ? 1.25 : 0.8), timeAt(e.clientX))
  }
  // Keyboard zoom from the editor (+ / -).
  useEffect(() => {
    const h = (e: Event) => zoomTo(zoom * ((e as CustomEvent<number>).detail > 0 ? 1.5 : 1 / 1.5))
    window.addEventListener('vd-timeline-zoom', h)
    return () => window.removeEventListener('vd-timeline-zoom', h)
  })

  // Keep the playhead in view while playing.
  useEffect(() => {
    const el = scroller.current
    if (!el || !p.playing) return
    const px = p.time * zoom
    if (px < el.scrollLeft || px > el.scrollLeft + el.clientWidth - 24) el.scrollLeft = Math.max(0, px - el.clientWidth * 0.15)
  }, [p.time, p.playing, zoom])

  // --- dragging ---
  const drag = useRef<{ kind: DragKind; id?: string; x0: number; start0: number; end0: number; freezes0: Clip['freezes']; moved: boolean } | null>(null)
  const snap = (t: number, id?: string) => {
    const tol = 8 / zoom
    const marks = [p.time, 0, duration]
    for (const c of clips) if (c.id !== id) marks.push(c.start, c.end)
    let best = t
    let d = tol
    for (const m of marks) {
      if (Math.abs(m - t) < d) {
        d = Math.abs(m - t)
        best = m
      }
    }
    return best
  }
  const begin = (e: ReactPointerEvent, kind: DragKind, c?: Clip) => {
    if (e.button !== 0) return
    e.preventDefault()
    e.stopPropagation()
    if (c) p.onSelect(c.id)
    drag.current = { kind, id: c?.id, x0: e.clientX, start0: c?.start ?? 0, end0: c?.end ?? 0, freezes0: c?.freezes ?? [], moved: false }
    if (kind === 'playhead') p.onSeek(timeAt(e.clientX))
    const move = (ev: PointerEvent) => {
      const d = drag.current
      if (!d) return
      if (d.kind === 'playhead') {
        p.onSeek(timeAt(ev.clientX))
        return
      }
      const dt = (ev.clientX - d.x0) / zoom
      if (!d.moved && Math.abs(ev.clientX - d.x0) < 3) return
      const coalesce = d.moved
      d.moved = true
      if (d.kind === 'start') {
        p.onEdit({ type: 'update', id: d.id!, patch: { start: clamp(snap(d.start0 + dt, d.id), 0, d.end0 - MIN_CLIP) } }, coalesce)
      } else if (d.kind === 'end') {
        p.onEdit({ type: 'update', id: d.id!, patch: { end: clamp(snap(d.end0 + dt, d.id), d.start0 + MIN_CLIP, duration) } }, coalesce)
      } else {
        const len = d.end0 - d.start0
        let s = d.start0 + dt
        const ss = snap(s, d.id)
        const se = snap(s + len, d.id)
        if (ss !== s) s = ss
        else if (se !== s + len) s = se - len
        s = clamp(s, 0, duration - len)
        const shift = s - d.start0
        p.onEdit({ type: 'update', id: d.id!, patch: { start: s, end: s + len, freezes: d.freezes0.map((f) => ({ ...f, at: f.at + shift })) } }, coalesce)
      }
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
      window.removeEventListener('pointercancel', up)
      drag.current = null
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
    window.addEventListener('pointercancel', up)
  }

  // --- ruler ---
  const tick = niceAtLeast(80 / zoom)
  const minor = niceAtLeast(16 / zoom)
  const from = Math.max(0, Math.floor((view.left - 100) / zoom / tick) * tick)
  const to = Math.min(duration, (view.left + view.width + 100) / zoom)
  const ticks: number[] = []
  for (let t = from; t <= to + 1e-9 && ticks.length < 400; t += tick) ticks.push(t)
  const minors: number[] = []
  if (minor < tick) for (let t = Math.floor(from / minor) * minor; t <= to && minors.length < 2000; t += minor) minors.push(t)

  // --- frame strip ---
  const tileW = clamp(Math.round(STRIP_H * p.aspect), 40, 150)
  const step = niceAtLeast(tileW / zoom)
  const firstTile = Math.max(0, Math.floor((view.left - view.width / 2) / tileW))
  const lastTile = Math.min(Math.ceil(width / tileW), Math.ceil((view.left + view.width * 1.5) / tileW))
  const tiles: { i: number; t: number }[] = []
  for (let i = firstTile; i < lastTile; i++) {
    const center = ((i + 0.5) * tileW) / zoom
    tiles.push({ i, t: clamp(Math.round(center / step) * step, 0, Math.max(0, duration - 0.05)) })
  }

  // Parts of the recording no selection covers are dimmed.
  const gaps = useMemo(() => {
    if (!clips.length) return [] // nothing chosen yet: show the whole recording
    const iv = clips.map((c) => [c.start, c.end] as const).sort((a, b) => a[0] - b[0])
    const out: [number, number][] = []
    let cur = 0
    for (const [s, e] of iv) {
      if (s > cur) out.push([cur, s])
      cur = Math.max(cur, e)
    }
    if (cur < duration) out.push([cur, duration])
    return out
  }, [clips, duration])

  const lanesH = count * (LANE_H + LANE_GAP) + LANE_GAP
  const totalH = RULER_H + STRIP_H + lanesH

  return (
    <div className="flex min-h-0 flex-col border-t border-white/10 bg-[#0d1326]">
      <div className="flex flex-wrap items-center gap-1 border-b border-white/[.06] px-3 py-1.5">
        <TButton label="Add a selection at the playhead (A)" onClick={p.onAddAtPlayhead}>
          <SquarePlus className="size-4" /> Add selection
        </TButton>
        <TButton label="Split the selection at the playhead (S)" onClick={p.onSplit} disabled={!p.canSplit}>
          <Scissors className="size-4" /> Split
        </TButton>
        <TButton label="Remove the selected clip (Delete)" onClick={() => sel && p.onEdit({ type: 'remove', id: sel.id }, false)} disabled={!sel}>
          <Trash2 className="size-4" />
        </TButton>
        <div className="mx-2 hidden h-5 w-px bg-white/10 sm:block" />
        <div className="font-mono text-xs text-slate-400 tabular-nums">
          {sel ? (
            <>
              <span className="text-brand-200">#{sel.number}</span> {formatTime(sel.start)} → {formatTime(sel.end)} <span className="text-slate-500">·</span> {formatLength(sel.end - sel.start)}
            </>
          ) : (
            `${clips.length} selection${clips.length === 1 ? '' : 's'}`
          )}
        </div>
        <div className="ml-auto flex items-center gap-1">
          <TButton label="Zoom out (-)" onClick={() => zoomTo(zoom / 1.5)} disabled={zoom <= fit * 1.001}>
            <Minus className="size-4" />
          </TButton>
          <input
            type="range"
            aria-label="Timeline zoom"
            min={0}
            max={1000}
            value={Math.round((Math.log(zoom / fit) / Math.log(MAX_PX_PER_SEC / fit || 2)) * 1000) || 0}
            onChange={(e) => zoomTo(fit * Math.pow(MAX_PX_PER_SEC / fit, Number(e.target.value) / 1000))}
            className="w-28 accent-brand-500"
          />
          <TButton label="Zoom in (+)" onClick={() => zoomTo(zoom * 1.5)} disabled={zoom >= MAX_PX_PER_SEC}>
            <Plus className="size-4" />
          </TButton>
          <TButton label="Fit the whole recording" onClick={() => zoomTo(fit)} active={!pps}>
            <Maximize2 className="size-3.5" /> Fit
          </TButton>
        </div>
      </div>
      <div
        ref={scroller}
        onScroll={(e) => setView({ left: e.currentTarget.scrollLeft, width: e.currentTarget.clientWidth })}
        onWheel={onWheel}
        className="scroll-thin relative min-h-0 overflow-x-auto overflow-y-auto"
        data-testid="timeline"
      >
        <div className="relative select-none" style={{ width, height: totalH }}>
          {/* ruler */}
          <div className="absolute inset-x-0 top-0 cursor-pointer border-b border-white/10 bg-[#0b1020]" style={{ height: RULER_H }} onPointerDown={(e) => begin(e, 'playhead')}>
            {minors.map((t) => (
              <div key={`m${t.toFixed(3)}`} className="absolute bottom-0 h-1.5 w-px bg-white/15" style={{ left: x(t) }} />
            ))}
            {ticks.map((t) => (
              <div key={t.toFixed(3)} className="absolute bottom-0" style={{ left: x(t) }}>
                <div className="h-3 w-px bg-white/35" />
                <span className="absolute bottom-3 left-1 font-mono text-[10px] whitespace-nowrap text-slate-400 tabular-nums">{formatTime(t, tick < 1 ? 2 : 0)}</span>
              </div>
            ))}
          </div>
          {/* frames */}
          <div className="absolute inset-x-0 cursor-pointer overflow-hidden bg-black" style={{ top: RULER_H, height: STRIP_H }} onPointerDown={(e) => begin(e, 'playhead')}>
            {tiles.map(({ i, t }) => (
              <img
                key={`${i}:${t}`}
                src={frameURL(p.sourceId, t, 90, p.sha)}
                alt=""
                draggable={false}
                loading="lazy"
                decoding="async"
                className="absolute top-0 h-full object-cover opacity-90"
                style={{ left: i * tileW, width: tileW }}
                onError={(e) => (e.currentTarget.style.visibility = 'hidden')}
              />
            ))}
            {gaps.map(([s, e]) => (
              <div key={`g${s}`} className="pointer-events-none absolute inset-y-0 bg-[#060912]/70" style={{ left: x(s), width: x(e) - x(s) }} />
            ))}
            {clips.map((c) => (
              <div
                key={`o${c.id}`}
                className={cx('pointer-events-none absolute inset-y-0 border-y-2', c.id === selected ? 'border-brand-400 bg-brand-400/10' : 'border-white/25')}
                style={{ left: x(c.start), width: Math.max(2, x(c.end) - x(c.start)) }}
              />
            ))}
            {sel && (
              <>
                <div
                  title="Drag to trim the start"
                  onPointerDown={(e) => begin(e, 'start', sel)}
                  className="absolute inset-y-0 z-10 flex w-3 cursor-ew-resize items-center justify-center rounded-l-md bg-brand-400"
                  style={{ left: x(sel.start) - 12 }}
                >
                  <div className="h-5 w-0.5 rounded bg-[#0b1020]/70" />
                </div>
                <div
                  title="Drag to trim the end"
                  onPointerDown={(e) => begin(e, 'end', sel)}
                  className="absolute inset-y-0 z-10 flex w-3 cursor-ew-resize items-center justify-center rounded-r-md bg-brand-400"
                  style={{ left: x(sel.end) }}
                >
                  <div className="h-5 w-0.5 rounded bg-[#0b1020]/70" />
                </div>
              </>
            )}
          </div>
          {/* selections */}
          <div className="absolute inset-x-0" style={{ top: RULER_H + STRIP_H, height: lanesH }} onPointerDown={(e) => {
            if (e.button !== 0) return
            p.onSelect(null)
            begin(e, 'playhead')
          }}>
            {clips.map((c) => {
              const lane = lanes.get(c.id) ?? 0
              const w = Math.max(6, x(c.end) - x(c.start))
              const on = c.id === selected
              return (
                <div
                  key={c.id}
                  data-testid={`clip-${c.number}`}
                  onPointerDown={(e) => begin(e, 'move', c)}
                  onDoubleClick={() => p.onSeek(c.start)}
                  title={`#${c.number} ${c.name || ''} ${formatTime(c.start)} – ${formatTime(c.end)}`}
                  className={cx(
                    'group absolute flex cursor-grab items-center overflow-hidden rounded-md border text-[11px] font-medium shadow-sm active:cursor-grabbing',
                    on ? 'border-brand-300 bg-gradient-to-b from-brand-500 to-brand-600 text-white shadow-brand-900/40' : 'border-violet-400/30 bg-violet-500/25 text-violet-100 hover:bg-violet-500/35',
                  )}
                  style={{ left: x(c.start), width: w, top: LANE_GAP + lane * (LANE_H + LANE_GAP), height: LANE_H }}
                >
                  <div onPointerDown={(e) => begin(e, 'start', c)} className="absolute inset-y-0 left-0 w-2 cursor-ew-resize hover:bg-white/30" />
                  <span className="pointer-events-none truncate px-2.5">
                    <b>#{c.number}</b> {w > 90 && <span className="opacity-90">{c.name || ''}</span>} {w > 150 && <span className="opacity-60">· {formatLength(c.end - c.start)}</span>}
                  </span>
                  <div onPointerDown={(e) => begin(e, 'end', c)} className="absolute inset-y-0 right-0 w-2 cursor-ew-resize hover:bg-white/30" />
                </div>
              )
            })}
            {clips.length === 0 && (
              <div className="pointer-events-none sticky left-0 flex h-full items-center px-4 text-xs text-slate-500" style={{ width: view.width }}>
                Press A or “Add selection” to mark a moment, then drag its edges to trim.
              </div>
            )}
          </div>
          {/* playhead */}
          <div className="pointer-events-none absolute top-0 z-20" style={{ left: x(p.time), height: totalH }}>
            <div className="absolute top-0 left-1/2 h-full w-0.5 -translate-x-1/2 bg-rose-400 shadow-[0_0_6px_rgba(251,113,133,.6)]" />
            <div
              className="pointer-events-auto absolute top-0 left-1/2 size-3.5 -translate-x-1/2 cursor-ew-resize rounded-b-sm bg-rose-400"
              style={{ clipPath: 'polygon(0 0,100% 0,100% 55%,50% 100%,0 55%)' }}
              onPointerDown={(e) => begin(e, 'playhead')}
            />
          </div>
        </div>
      </div>
    </div>
  )
}
