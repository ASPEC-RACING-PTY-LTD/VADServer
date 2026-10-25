import { useEffect, useState } from 'react'
import { ArrowDown, ArrowUp, Crop, Gauge, Image, ListVideo, Play, Repeat, SlidersHorizontal, Snowflake, Trash2, Volume2, VolumeX, X } from 'lucide-react'
import { formatBytes } from '../../../lib/format'
import { cx } from '../../../components/ui'
import { ASPECTS, clipMode, cropForAspect, defaultClipName, estimateSize, formatLength, formatTime, outputDuration, parseTime, SPEEDS, type Clip, type Edit, type MediaInfo, type Project } from '../model'

type Common = {
  project: Project
  info: MediaInfo
  time: number
  onEdit: (e: Edit, coalesce?: boolean) => void
  onSeek: (t: number) => void
}

function Section({ icon, title, children, right }: { icon: React.ReactNode; title: string; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <section className="border-b border-white/[.06] px-4 py-3.5">
      <div className="mb-2.5 flex items-center gap-2 text-[11px] font-semibold tracking-wider text-slate-400 uppercase">
        {icon}
        {title}
        <span className="ml-auto normal-case">{right}</span>
      </div>
      {children}
    </section>
  )
}

const field = 'h-8 min-w-0 rounded-lg border border-white/10 bg-white/[.04] px-2.5 text-sm text-slate-100 placeholder:text-slate-500 focus:border-brand-400 focus:ring-2 focus:ring-brand-500/25 focus:outline-none'
const chip = (on: boolean) => cx('h-8 rounded-lg px-2.5 text-xs font-medium transition', on ? 'bg-brand-500 text-white shadow-sm shadow-brand-900/50' : 'bg-white/[.05] text-slate-300 hover:bg-white/[.1]')

/** Time field: shows m:ss.cc, accepts seconds or m:ss on Enter / blur. */
function TimeField({ label, value, onChange, onPlayhead }: { label: string; value: number; onChange: (v: number) => void; onPlayhead: () => void }) {
  const [text, setText] = useState(formatTime(value))
  useEffect(() => setText(formatTime(value)), [value])
  const commit = () => {
    const v = parseTime(text)
    if (v === null) setText(formatTime(value))
    else onChange(v)
  }
  return (
    <label className="block min-w-0">
      <span className="mb-1 block text-[11px] text-slate-400">{label}</span>
      <div className="flex gap-1">
        <input className={cx(field, 'flex-1 font-mono tabular-nums')} value={text} onChange={(e) => setText(e.target.value)} onBlur={commit} onKeyDown={(e) => e.key === 'Enter' && (e.currentTarget as HTMLInputElement).blur()} aria-label={label} />
        <button type="button" title={`Set ${label.toLowerCase()} to the playhead`} onClick={onPlayhead} className="h-8 shrink-0 rounded-lg bg-white/[.05] px-2 text-[11px] text-slate-300 hover:bg-white/[.1]">
          ⟟
        </button>
      </div>
    </label>
  )
}

/** Settings of the selected clip (Medal-style contextual panel). */
export function ClipSettings({ clip, project, info, time, onEdit, onSeek, previewing, onPreview }: Common & { clip: Clip; previewing: boolean; onPreview: () => void }) {
  const set = (patch: Partial<Clip>, coalesce = false) => onEdit({ type: 'update', id: clip.id, patch }, coalesce)
  const [customSpeed, setCustomSpeed] = useState(!SPEEDS.includes(clip.speed as (typeof SPEEDS)[number]))
  const [freezeLen, setFreezeLen] = useState(2)
  const inClip = time >= clip.start && time <= clip.end
  const mode = clipMode(clip, project.quality)
  const est = estimateSize(clip, project.quality, info)
  return (
    <div>
      <Section icon={<SlidersHorizontal className="size-3.5" />} title={`Clip #${clip.number}`} right={<span className={cx('rounded px-1.5 py-0.5 text-[10px] font-semibold', mode === 'copy' ? 'bg-emerald-500/15 text-emerald-300' : 'bg-violet-500/20 text-violet-200')}>{mode === 'copy' ? 'Lossless copy' : 'Re-encoded'}</span>}>
        <label className="block">
          <span className="mb-1 block text-[11px] text-slate-400">File name</span>
          <input className={cx(field, 'w-full')} value={clip.name} placeholder={defaultClipName(project, clip.number)} maxLength={180} onChange={(e) => set({ name: e.target.value }, true)} aria-label="Clip file name" />
        </label>
        <div className="mt-3 grid grid-cols-2 gap-2">
          <TimeField label="Start" value={clip.start} onChange={(v) => set({ start: Math.min(v, clip.end - 0.1) })} onPlayhead={() => set({ start: Math.min(time, clip.end - 0.1) })} />
          <TimeField label="End" value={clip.end} onChange={(v) => set({ end: Math.max(v, clip.start + 0.1) })} onPlayhead={() => set({ end: Math.max(time, clip.start + 0.1) })} />
        </div>
        <div className="mt-3 grid grid-cols-3 gap-2 text-center">
          {[
            ['Selection', formatLength(clip.end - clip.start)],
            ['Exported', formatLength(outputDuration(clip))],
            ['Est. size', formatBytes(est)],
          ].map(([k, v]) => (
            <div key={k} className="rounded-lg bg-white/[.04] px-1 py-1.5">
              <div className="text-[10px] text-slate-500">{k}</div>
              <div className="font-mono text-xs text-slate-100 tabular-nums">{v}</div>
            </div>
          ))}
        </div>
        <div className="mt-3 flex gap-2">
          <button type="button" onClick={onPreview} className={cx('flex h-8 flex-1 items-center justify-center gap-1.5 rounded-lg text-xs font-medium', previewing ? 'bg-brand-500 text-white' : 'bg-white/[.06] text-slate-200 hover:bg-white/[.1]')}>
            <Repeat className="size-3.5" /> {previewing ? 'Stop preview' : 'Preview clip'}
          </button>
          <button type="button" onClick={() => onSeek(clip.start)} className="flex h-8 items-center gap-1.5 rounded-lg bg-white/[.06] px-3 text-xs text-slate-200 hover:bg-white/[.1]">
            <Play className="size-3.5" /> Go to start
          </button>
        </div>
      </Section>

      <Section icon={<Gauge className="size-3.5" />} title="Speed">
        <div className="flex flex-wrap gap-1.5">
          {SPEEDS.map((s) => (
            <button
              key={s}
              type="button"
              className={chip(!customSpeed && clip.speed === s)}
              onClick={() => {
                setCustomSpeed(false)
                set({ speed: s })
              }}
            >
              {s}x
            </button>
          ))}
          <button type="button" className={chip(customSpeed)} onClick={() => setCustomSpeed(true)}>
            Custom
          </button>
        </div>
        {customSpeed && (
          <div className="mt-2 flex items-center gap-2">
            <input type="range" min={0.25} max={4} step={0.05} value={clip.speed} onChange={(e) => set({ speed: Number(e.target.value) }, true)} className="flex-1 accent-brand-500" aria-label="Custom speed" />
            <input type="number" min={0.25} max={4} step={0.05} value={clip.speed} onChange={(e) => set({ speed: Math.min(4, Math.max(0.25, Number(e.target.value) || 1)) })} className={cx(field, 'w-20 shrink-0 font-mono')} aria-label="Custom speed value" />
          </div>
        )}
        <label className="mt-2.5 flex items-center gap-2 text-xs text-slate-300">
          <input type="checkbox" checked={clip.preservePitch} onChange={(e) => set({ preservePitch: e.target.checked })} className="size-4 accent-brand-500" />
          Preserve audio pitch
        </label>
      </Section>

      <Section icon={clip.muted ? <VolumeX className="size-3.5" /> : <Volume2 className="size-3.5" />} title="Audio" right={info.audio_tracks === 0 ? <span className="text-[11px] text-slate-500">no audio track</span> : null}>
        <div className="flex items-center gap-2">
          <button type="button" onClick={() => set({ muted: !clip.muted })} className={chip(clip.muted)} aria-pressed={clip.muted}>
            {clip.muted ? 'Muted' : 'Mute'}
          </button>
          <input type="range" min={0} max={2} step={0.05} disabled={clip.muted} value={clip.volume} onChange={(e) => set({ volume: Number(e.target.value) }, true)} className="flex-1 accent-brand-500 disabled:opacity-40" aria-label="Clip volume" />
          <span className="w-11 text-right font-mono text-xs text-slate-300 tabular-nums">{Math.round(clip.volume * 100)}%</span>
        </div>
        {info.audio_tracks > 1 && <p className="mt-2 text-[11px] leading-relaxed text-slate-500">Lossless copies keep all {info.audio_tracks} audio tracks; re-encoded clips keep the first one.</p>}
      </Section>

      <Section icon={<Snowflake className="size-3.5" />} title="Freeze frames">
        {clip.freezes.length > 0 && (
          <ul className="mb-2 space-y-1">
            {clip.freezes.map((f, i) => (
              <li key={i} className="flex items-center gap-2 rounded-lg bg-white/[.04] px-2.5 py-1.5 text-xs">
                <button type="button" className="font-mono text-brand-200 tabular-nums hover:underline" onClick={() => onSeek(f.at)}>
                  {formatTime(f.at)}
                </button>
                <span className="text-slate-400">hold</span>
                <input type="number" min={0.1} max={30} step={0.5} value={f.duration} onChange={(e) => set({ freezes: clip.freezes.map((x, j) => (j === i ? { ...x, duration: Math.min(30, Math.max(0.1, Number(e.target.value) || 1)) } : x)) })} className={cx(field, 'h-7 w-16 font-mono text-xs')} aria-label="Freeze length (seconds)" />
                <span className="text-slate-500">s</span>
                <button type="button" onClick={() => set({ freezes: clip.freezes.filter((_, j) => j !== i) })} className="ml-auto text-slate-500 hover:text-rose-300" aria-label="Remove freeze frame">
                  <X className="size-3.5" />
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="flex items-center gap-2">
          <button type="button" disabled={!inClip || clip.freezes.length >= 8} onClick={() => set({ freezes: [...clip.freezes, { at: time, duration: freezeLen }].sort((a, b) => a.at - b.at) })} className="h-8 min-w-0 flex-1 truncate rounded-lg bg-white/[.06] px-2 text-xs text-slate-200 hover:bg-white/[.1] disabled:opacity-40">
            Freeze frame at playhead
          </button>
          <input type="number" min={0.5} max={30} step={0.5} value={freezeLen} onChange={(e) => setFreezeLen(Math.min(30, Math.max(0.5, Number(e.target.value) || 2)))} className={cx(field, 'w-16 shrink-0 font-mono')} aria-label="New freeze length (seconds)" />
          <span className="text-xs text-slate-500">s</span>
        </div>
        {!inClip && <p className="mt-1.5 text-[11px] text-slate-500">Move the playhead inside the clip to add one.</p>}
      </Section>

      <Section icon={<Crop className="size-3.5" />} title="Crop & aspect">
        <div className="grid grid-cols-2 gap-1.5">
          {ASPECTS.map((a) => (
            <button key={a.id} type="button" className={chip(clip.aspect === a.id)} onClick={() => set({ aspect: a.id, crop: cropForAspect(a.id, info, clip.crop) })}>
              {a.label}
            </button>
          ))}
        </div>
        {clip.crop ? (
          <p className="mt-2 text-[11px] leading-relaxed text-slate-400">
            Drag the frame on the video to reposition it, or a corner to resize. Output {Math.round(clip.crop.w * info.width) & ~1}×{Math.round(clip.crop.h * info.height) & ~1}.
          </p>
        ) : (
          <p className="mt-2 text-[11px] text-slate-500">Full frame, {info.width}×{info.height}.</p>
        )}
      </Section>

      <div className="px-4 py-4">
        <button type="button" onClick={() => onEdit({ type: 'remove', id: clip.id })} className="flex h-9 w-full items-center justify-center gap-2 rounded-lg border border-rose-500/30 text-sm text-rose-300 transition hover:bg-rose-500/10">
          <Trash2 className="size-4" /> Remove selection
        </button>
      </div>
    </div>
  )
}

/** Every numbered selection: jump to it, rename, reorder, remove. */
export function ClipList({ project, info, selected, onSelect, onEdit, onSeek, onPreview }: Common & { selected: string | null; onSelect: (id: string) => void; onPreview: (id: string) => void }) {
  const total = project.clips.reduce((a, c) => a + estimateSize(c, project.quality, info), 0)
  const length = project.clips.reduce((a, c) => a + outputDuration(c), 0)
  if (!project.clips.length)
    return (
      <div className="px-6 py-10 text-center">
        <ListVideo className="mx-auto mb-3 size-8 text-slate-600" />
        <p className="text-sm text-slate-300">No selections yet</p>
        <p className="mt-1 text-xs leading-relaxed text-slate-500">Move the playhead to a moment you want to keep and press A (or Add selection). Every selection becomes its own video file.</p>
      </div>
    )
  return (
    <div>
      <ul className="divide-y divide-white/[.05]">
        {project.clips.map((c, i) => {
          const on = c.id === selected
          return (
            <li key={c.id} className={cx('group px-3 py-2.5 transition', on ? 'bg-brand-500/[.12]' : 'hover:bg-white/[.03]')}>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => {
                    onSelect(c.id)
                    onSeek(c.start)
                  }}
                  className={cx('flex h-7 min-w-9 items-center justify-center rounded-md px-1.5 font-mono text-xs font-bold', on ? 'bg-brand-500 text-white' : 'bg-white/[.07] text-slate-200')}
                  title="Jump to this selection"
                >
                  #{c.number}
                </button>
                <input
                  value={c.name}
                  placeholder={defaultClipName(project, c.number)}
                  onFocus={() => onSelect(c.id)}
                  onChange={(e) => onEdit({ type: 'update', id: c.id, patch: { name: e.target.value } }, true)}
                  className="h-7 min-w-0 flex-1 rounded-md border border-transparent bg-transparent px-1.5 text-sm text-slate-100 placeholder:text-slate-500 hover:border-white/10 focus:border-brand-400 focus:bg-white/[.04] focus:outline-none"
                  aria-label={`Name of clip #${c.number}`}
                />
                <div className="flex opacity-60 transition group-hover:opacity-100">
                  <button type="button" title="Preview" onClick={() => onPreview(c.id)} className="rounded p-1 text-slate-400 hover:text-white">
                    <Play className="size-3.5" />
                  </button>
                  <button type="button" title="Move up" disabled={i === 0} onClick={() => onEdit({ type: 'move-order', id: c.id, dir: -1 })} className="rounded p-1 text-slate-400 hover:text-white disabled:opacity-30">
                    <ArrowUp className="size-3.5" />
                  </button>
                  <button type="button" title="Move down" disabled={i === project.clips.length - 1} onClick={() => onEdit({ type: 'move-order', id: c.id, dir: 1 })} className="rounded p-1 text-slate-400 hover:text-white disabled:opacity-30">
                    <ArrowDown className="size-3.5" />
                  </button>
                  <button type="button" title="Remove" onClick={() => onEdit({ type: 'remove', id: c.id })} className="rounded p-1 text-slate-400 hover:text-rose-300">
                    <Trash2 className="size-3.5" />
                  </button>
                </div>
              </div>
              <div className="mt-1 flex items-center gap-2 pl-11 font-mono text-[11px] text-slate-400 tabular-nums">
                <span>
                  {formatTime(c.start, 1)} – {formatTime(c.end, 1)}
                </span>
                <span className="text-slate-600">·</span>
                <span>{formatLength(outputDuration(c))}</span>
                <span className="ml-auto">{formatBytes(estimateSize(c, project.quality, info))}</span>
              </div>
            </li>
          )
        })}
      </ul>
      <div className="flex items-center justify-between border-t border-white/[.06] px-4 py-3 text-xs text-slate-400">
        <span>
          {project.clips.length} clip{project.clips.length === 1 ? '' : 's'} · {formatLength(length)}
        </span>
        <span className="flex items-center gap-1.5">
          <Image className="size-3.5" /> ~{formatBytes(total)}
        </span>
      </div>
    </div>
  )
}
