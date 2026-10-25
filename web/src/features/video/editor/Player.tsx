import { useLayoutEffect, useRef, useState, type PointerEvent as ReactPointerEvent, type RefObject } from 'react'
import { ChevronFirst, ChevronLast, Maximize, Pause, Play, Repeat, Volume2, VolumeX } from 'lucide-react'
import { cx } from '../../../components/ui'
import { ASPECTS, clamp, formatTime, type Clip, type MediaInfo, type Rect } from '../model'

export const PREVIEW_RATES = [0.25, 0.5, 1, 1.5, 2] as const

type Props = {
  src: string
  info: MediaInfo
  videoRef: RefObject<HTMLVideoElement | null>
  time: number
  playing: boolean
  rate: number
  volume: number
  muted: boolean
  clip?: Clip
  previewing: boolean
  onTogglePlay: () => void
  onSeek: (t: number) => void
  onStep: (frames: number) => void
  onRate: (r: number) => void
  onVolume: (v: number) => void
  onMute: () => void
  onPreview: () => void
  onCrop: (r: Rect, coalesce: boolean) => void
}

function CtlButton({ label, onClick, children, active, disabled }: { label: string; onClick: () => void; children: React.ReactNode; active?: boolean; disabled?: boolean }) {
  return (
    <button
      type="button"
      title={label}
      aria-label={label}
      aria-pressed={active}
      disabled={disabled}
      onClick={onClick}
      className={cx('flex size-9 items-center justify-center rounded-lg transition disabled:opacity-35', active ? 'bg-brand-500/25 text-brand-200' : 'text-slate-300 hover:bg-white/[.08] hover:text-white')}
    >
      {children}
    </button>
  )
}

/** Fits a w:h box into the area (letterboxing). */
function fitBox(area: { w: number; h: number }, aspect: number) {
  if (area.w <= 0 || area.h <= 0) return { w: 0, h: 0 }
  return area.w / area.h > aspect ? { w: area.h * aspect, h: area.h } : { w: area.w, h: area.w / aspect }
}

/**
 * The central preview: the recording letterboxed in black at its own aspect ratio. With a crop on
 * the selected clip, a frame shows the kept area (drag it to reposition, drag a corner to resize);
 * "Preview clip" shows the clip as it will be exported.
 */
export function Player(p: Props) {
  const wrap = useRef<HTMLDivElement>(null)
  const area = useRef<HTMLDivElement>(null)
  const [size, setSize] = useState({ w: 0, h: 0 })
  useLayoutEffect(() => {
    const el = area.current
    if (!el) return
    const ro = new ResizeObserver(() => setSize({ w: el.clientWidth, h: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const W = p.info.width
  const H = p.info.height
  const crop = p.clip?.crop ?? null
  const showCropped = p.previewing && crop !== null
  const box = fitBox(size, showCropped ? (crop!.w * W) / (crop!.h * H) : W / H)
  // In the cropped preview the video is larger than the box and shifted so only the crop shows.
  const vid = showCropped ? { w: box.w / crop!.w, h: box.h / crop!.h, x: (-crop!.x * box.w) / crop!.w, y: (-crop!.y * box.h) / crop!.h } : { w: box.w, h: box.h, x: 0, y: 0 }

  // --- crop frame dragging ---
  const ratio = p.clip ? ASPECTS.find((a) => a.id === p.clip!.aspect)?.ratio ?? null : null
  const beginCrop = (e: ReactPointerEvent, mode: 'move' | 'nw' | 'ne' | 'sw' | 'se') => {
    if (!crop || e.button !== 0) return
    e.preventDefault()
    e.stopPropagation()
    const x0 = e.clientX
    const y0 = e.clientY
    const r0 = crop
    let first = true
    // Fractions of the frame per screen pixel.
    const fx = 1 / box.w
    const fy = 1 / box.h
    // Aspect (as fractions) to keep while resizing: frame ratio adjusted to the source shape.
    const fracRatio = ratio ? ratio / (W / H) : null
    const move = (ev: PointerEvent) => {
      const dx = (ev.clientX - x0) * fx
      const dy = (ev.clientY - y0) * fy
      let r: Rect
      if (mode === 'move') {
        r = { ...r0, x: clamp(r0.x + dx, 0, 1 - r0.w), y: clamp(r0.y + dy, 0, 1 - r0.h) }
      } else {
        const left = mode === 'nw' || mode === 'sw'
        const top = mode === 'nw' || mode === 'ne'
        // The opposite corner stays put.
        const ax = left ? r0.x + r0.w : r0.x
        const ay = top ? r0.y + r0.h : r0.y
        let w = clamp(left ? r0.w - dx : r0.w + dx, 0.05, left ? ax : 1 - ax)
        let h = clamp(top ? r0.h - dy : r0.h + dy, 0.05, top ? ay : 1 - ay)
        if (fracRatio) {
          // Follow the larger change, then fit inside the frame.
          if (w / fracRatio > h) h = w / fracRatio
          else w = h * fracRatio
          const maxW = left ? ax : 1 - ax
          const maxH = top ? ay : 1 - ay
          if (w > maxW) {
            w = maxW
            h = w / fracRatio
          }
          if (h > maxH) {
            h = maxH
            w = h * fracRatio
          }
        }
        r = { w, h, x: left ? ax - w : ax, y: top ? ay - h : ay }
      }
      p.onCrop(r, !first)
      first = false
    }
    const up = () => {
      window.removeEventListener('pointermove', move)
      window.removeEventListener('pointerup', up)
    }
    window.addEventListener('pointermove', move)
    window.addEventListener('pointerup', up)
  }

  const fullscreen = () => {
    const el = wrap.current
    if (!el) return
    if (document.fullscreenElement) document.exitFullscreen()
    else el.requestFullscreen?.()
  }

  return (
    <div ref={wrap} className="flex min-h-0 min-w-0 flex-1 flex-col bg-black">
      <div ref={area} className="relative min-h-0 flex-1 overflow-hidden" onDoubleClick={fullscreen}>
        <div className="absolute overflow-hidden" style={{ width: box.w, height: box.h, left: (size.w - box.w) / 2, top: (size.h - box.h) / 2 }}>
          <video
            ref={p.videoRef}
            src={p.src}
            preload="auto"
            playsInline
            className="absolute max-w-none bg-black"
            style={{ width: vid.w, height: vid.h, left: vid.x, top: vid.y }}
            onClick={p.onTogglePlay}
          />
          {crop && !p.previewing && (
            <div
              className="absolute cursor-move border-2 border-brand-300 shadow-[0_0_0_9999px_rgba(3,6,18,.62)]"
              style={{ left: crop.x * box.w, top: crop.y * box.h, width: crop.w * box.w, height: crop.h * box.h }}
              onPointerDown={(e) => beginCrop(e, 'move')}
              data-testid="crop-frame"
            >
              <div className="pointer-events-none absolute inset-0 grid grid-cols-3 grid-rows-3">
                {Array.from({ length: 9 }).map((_, i) => (
                  <div key={i} className="border border-white/15" />
                ))}
              </div>
              {(['nw', 'ne', 'sw', 'se'] as const).map((c) => (
                <div
                  key={c}
                  onPointerDown={(e) => beginCrop(e, c)}
                  className={cx(
                    'absolute size-3.5 rounded-sm border-2 border-white bg-brand-500',
                    c === 'nw' && '-top-2 -left-2 cursor-nwse-resize',
                    c === 'se' && '-right-2 -bottom-2 cursor-nwse-resize',
                    c === 'ne' && '-top-2 -right-2 cursor-nesw-resize',
                    c === 'sw' && '-bottom-2 -left-2 cursor-nesw-resize',
                  )}
                />
              ))}
              <span className="pointer-events-none absolute -top-6 left-0 rounded bg-brand-500 px-1.5 py-0.5 font-mono text-[10px] text-white">
                {Math.round(crop.w * W) & ~1}×{Math.round(crop.h * H) & ~1}
              </span>
            </div>
          )}
        </div>
        {p.previewing && p.clip && (
          <div className="pointer-events-none absolute top-3 left-3 rounded-lg bg-brand-600/90 px-2.5 py-1 text-xs font-medium text-white shadow-lg">
            Previewing #{p.clip.number}
            {p.clip.speed !== 1 && ` · ${p.clip.speed}x`}
          </div>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-1 border-t border-white/10 bg-[#0d1326] px-2 py-1.5">
        <CtlButton label="Previous frame (←)" onClick={() => p.onStep(-1)}>
          <ChevronFirst className="size-4" />
        </CtlButton>
        <button
          type="button"
          onClick={p.onTogglePlay}
          aria-label={p.playing ? 'Pause (Space)' : 'Play (Space)'}
          title={p.playing ? 'Pause (Space)' : 'Play (Space)'}
          className="flex size-10 items-center justify-center rounded-full bg-white text-[#0b1020] shadow transition hover:scale-105"
        >
          {p.playing ? <Pause className="size-4 fill-current" /> : <Play className="ml-0.5 size-4 fill-current" />}
        </button>
        <CtlButton label="Next frame (→)" onClick={() => p.onStep(1)}>
          <ChevronLast className="size-4" />
        </CtlButton>
        <div className="ml-2 font-mono text-sm text-slate-200 tabular-nums">
          {formatTime(p.time)} <span className="text-slate-500">/ {formatTime(p.info.duration)}</span>
        </div>
        <div className="ml-auto flex items-center gap-1">
          <CtlButton label={p.previewing ? 'Stop previewing the clip' : 'Preview the selected clip as exported (P)'} onClick={p.onPreview} active={p.previewing} disabled={!p.clip}>
            <Repeat className="size-4" />
          </CtlButton>
          <select
            aria-label="Playback speed"
            title="Playback speed (preview only)"
            value={p.rate}
            disabled={p.previewing}
            onChange={(e) => p.onRate(Number(e.target.value))}
            className="h-8 rounded-lg border border-white/10 bg-white/[.04] px-2 text-xs text-slate-200 disabled:opacity-40"
          >
            {PREVIEW_RATES.map((r) => (
              <option key={r} value={r} className="bg-slate-900">
                {r}x
              </option>
            ))}
          </select>
          <CtlButton label={p.muted ? 'Unmute (M)' : 'Mute (M)'} onClick={p.onMute}>
            {p.muted || p.volume === 0 ? <VolumeX className="size-4" /> : <Volume2 className="size-4" />}
          </CtlButton>
          <input type="range" aria-label="Volume" min={0} max={1} step={0.01} value={p.muted ? 0 : p.volume} onChange={(e) => p.onVolume(Number(e.target.value))} className="w-20 accent-brand-500" />
          <CtlButton label="Full screen (F)" onClick={fullscreen}>
            <Maximize className="size-4" />
          </CtlButton>
        </div>
      </div>
    </div>
  )
}
