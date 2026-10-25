// Video editor model: the project (a draft's data), its clips and the edit
// history. Everything here is plain data and pure functions so it can be
// saved as a draft and tested without a browser.

export type Quality = 'copy' | 'high' | 'balanced' | 'small'
export type Aspect = 'original' | '16:9' | '9:16' | '1:1' | 'free'

export type Rect = { x: number; y: number; w: number; h: number }
export type Freeze = { at: number; duration: number }

export type Clip = {
  id: string
  /** Stable number shown as #1, #2... (kept when clips are reordered or removed). */
  number: number
  name: string
  start: number
  end: number
  speed: number
  preservePitch: boolean
  volume: number
  muted: boolean
  freezes: Freeze[]
  aspect: Aspect
  /** Fractions of the displayed frame; null = the whole frame. */
  crop: Rect | null
}

export type Project = {
  version: 1
  name: string
  clips: Clip[]
  nextNumber: number
  quality: Quality
}

export type MediaInfo = {
  duration: number
  width: number
  height: number
  fps: number
  video_codec: string
  audio_codec?: string
  audio_tracks: number
  sample_rate?: number
  bitrate: number
  video_bitrate: number
  audio_bitrate: number
  format: string
  size: number
}

export const MIN_CLIP = 0.1
export const SPEEDS = [0.5, 1, 1.5, 2] as const
export const ASPECTS: { id: Aspect; label: string; ratio: number | null }[] = [
  { id: 'original', label: 'Original', ratio: null },
  { id: '16:9', label: 'Landscape 16:9', ratio: 16 / 9 },
  { id: '9:16', label: 'Portrait 9:16', ratio: 9 / 16 },
  { id: '1:1', label: 'Square 1:1', ratio: 1 },
  { id: 'free', label: 'Free crop', ratio: null },
]

let seq = 0
export const newId = () => `c${Date.now().toString(36)}${(seq++).toString(36)}`

export function baseName(name: string) {
  const i = name.lastIndexOf('.')
  return i > 0 ? name.slice(0, i) : name
}

export function emptyProject(sourceName: string): Project {
  return { version: 1, name: baseName(sourceName), clips: [], nextNumber: 1, quality: 'high' }
}

/** Loads a draft's data, filling anything an older or damaged draft lacks. */
export function loadProject(data: unknown, sourceName: string, duration: number): Project {
  const p = emptyProject(sourceName)
  if (!data || typeof data !== 'object') return p
  const d = data as Partial<Project>
  if (typeof d.name === 'string' && d.name.trim()) p.name = d.name
  if (d.quality === 'copy' || d.quality === 'high' || d.quality === 'balanced' || d.quality === 'small') p.quality = d.quality
  const clips = Array.isArray(d.clips) ? d.clips : []
  for (const c of clips) {
    if (!c || typeof c !== 'object') continue
    const start = clamp(Number(c.start) || 0, 0, duration)
    const end = clamp(Number(c.end) || 0, 0, duration)
    if (end - start < MIN_CLIP) continue
    p.clips.push({
      id: typeof c.id === 'string' ? c.id : newId(),
      number: Number.isInteger(c.number) && c.number > 0 ? c.number : 0,
      name: typeof c.name === 'string' ? c.name : '',
      start,
      end,
      speed: clamp(Number(c.speed) || 1, 0.25, 4),
      preservePitch: c.preservePitch !== false,
      volume: clamp(Number.isFinite(Number(c.volume)) ? Number(c.volume) : 1, 0, 2),
      muted: !!c.muted,
      freezes: Array.isArray(c.freezes) ? c.freezes.filter((f) => f && f.at >= start && f.at <= end && f.duration > 0).map((f) => ({ at: Number(f.at), duration: clamp(Number(f.duration), 0.1, 30) })) : [],
      aspect: (['original', '16:9', '9:16', '1:1', 'free'] as Aspect[]).includes(c.aspect as Aspect) ? (c.aspect as Aspect) : 'original',
      crop: validRect(c.crop) ? c.crop : null,
    })
  }
  // Numbers stay unique; anything missing or repeated gets a fresh one.
  let next = Math.max(Number(d.nextNumber) || 1, 1, ...p.clips.map((c) => c.number + 1))
  const seen = new Set<number>()
  for (const c of p.clips) {
    if (!c.number || seen.has(c.number)) c.number = next++
    seen.add(c.number)
  }
  p.nextNumber = next
  return p
}

function validRect(r: unknown): r is Rect {
  if (!r || typeof r !== 'object') return false
  const { x, y, w, h } = r as Rect
  return [x, y, w, h].every((v) => typeof v === 'number' && isFinite(v)) && x >= 0 && y >= 0 && w > 0 && h > 0 && x + w <= 1.0001 && y + h <= 1.0001
}

export const clamp = (v: number, lo: number, hi: number) => Math.min(Math.max(v, lo), hi)

export function clipLength(c: Pick<Clip, 'start' | 'end'>) {
  return c.end - c.start
}

/** Length of the exported clip after speed and freeze frames. */
export function outputDuration(c: Clip) {
  return (c.end - c.start) / c.speed + c.freezes.reduce((a, f) => a + f.duration, 0)
}

export function needsEncode(c: Clip) {
  return c.speed !== 1 || (!c.muted && c.volume !== 1 && c.volume !== 0) || c.freezes.length > 0 || c.crop !== null
}

/** Output mode of a clip (mirrors the server's media.Mode). */
export function clipMode(c: Clip, q: Quality): Quality {
  if (q === 'copy') return needsEncode(c) ? 'high' : 'copy'
  return q
}

/** Estimated output bytes (mirrors the server's media.EstimateSize). */
export function estimateSize(c: Clip, q: Quality, info: MediaInfo): number {
  const mode = clipMode(c, q)
  const vbr = info.video_bitrate > 0 ? info.video_bitrate : 8e6
  const muted = c.muted || c.volume === 0 || info.audio_tracks === 0
  let abr = muted ? 0 : info.audio_bitrate > 0 ? info.audio_bitrate : 160e3
  if (mode === 'copy') {
    if (!muted) abr *= Math.max(1, info.audio_tracks)
    return ((vbr + abr) / 8) * (c.end - c.start) * 1.01
  }
  const share = mode === 'balanced' ? 0.8 : mode === 'small' ? 0.5 : 1
  const area = c.crop ? Math.max(c.crop.w * c.crop.h, 0.15) : 1
  const v = vbr * share * area
  abr = Math.min(abr, 192e3)
  const moving = (c.end - c.start) / c.speed
  return (((v + abr) * moving + abr * (outputDuration(c) - moving)) / 8) * 1.02
}

/** Crop rectangle of the given aspect ratio, as large as fits, centred on the old one. */
export function cropForAspect(aspect: Aspect, info: Pick<MediaInfo, 'width' | 'height'>, prev: Rect | null): Rect | null {
  const preset = ASPECTS.find((a) => a.id === aspect)
  if (aspect === 'original') return null
  if (aspect === 'free') return prev ?? { x: 0.1, y: 0.1, w: 0.8, h: 0.8 }
  const ratio = preset!.ratio!
  const src = info.width / info.height
  let w = 1
  let h = 1
  if (ratio < src) w = ratio / src
  else h = src / ratio
  const cx = prev ? prev.x + prev.w / 2 : 0.5
  const cy = prev ? prev.y + prev.h / 2 : 0.5
  return { w, h, x: clamp(cx - w / 2, 0, 1 - w), y: clamp(cy - h / 2, 0, 1 - h) }
}

// --- time ---

/** 1:02:03.45 / 2:03.45 / 0:03.45 */
export function formatTime(s: number, decimals = 2) {
  if (!isFinite(s) || s < 0) s = 0
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  const ss = sec.toFixed(decimals).padStart(decimals ? 3 + decimals : 2, '0')
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${ss}` : `${m}:${ss}`
}

/** "17.0s", "2m 05s" */
export function formatLength(s: number) {
  if (s < 60) return `${s.toFixed(1)}s`
  const m = Math.floor(s / 60)
  return `${m}m ${String(Math.round(s % 60)).padStart(2, '0')}s`
}

/** Parses "1:02.5", "62.5" or "1:01:02" into seconds; null when invalid. */
export function parseTime(v: string): number | null {
  const parts = v.trim().split(':')
  if (parts.length > 3 || parts.some((p) => p.trim() === '' || isNaN(Number(p)))) return null
  return parts.reduce((acc, p) => acc * 60 + Number(p), 0)
}

// --- edits (pure) ---

export type Edit =
  | { type: 'rename-project'; name: string }
  | { type: 'add'; start: number; end: number; select?: boolean }
  | { type: 'update'; id: string; patch: Partial<Omit<Clip, 'id' | 'number'>> }
  | { type: 'remove'; id: string }
  | { type: 'split'; id: string; at: number }
  | { type: 'move-order'; id: string; dir: -1 | 1 }
  | { type: 'quality'; quality: Quality }

export function defaultClipName(project: Project, number: number) {
  return `${project.name} #${number}`
}

/** Applies an edit; returns the new project and the id to select (if it changes). */
export function applyEdit(p: Project, e: Edit, duration: number): { project: Project; select?: string | null } {
  switch (e.type) {
    case 'rename-project':
      return { project: { ...p, name: e.name } }
    case 'quality':
      return { project: { ...p, quality: e.quality } }
    case 'add': {
      const start = clamp(Math.min(e.start, e.end), 0, duration)
      const end = clamp(Math.max(e.start, e.end), 0, duration)
      if (end - start < MIN_CLIP) return { project: p }
      const c: Clip = { id: newId(), number: p.nextNumber, name: '', start, end, speed: 1, preservePitch: true, volume: 1, muted: false, freezes: [], aspect: 'original', crop: null }
      return { project: { ...p, clips: [...p.clips, c], nextNumber: p.nextNumber + 1 }, select: c.id }
    }
    case 'update': {
      const clips = p.clips.map((c) => {
        if (c.id !== e.id) return c
        const next = { ...c, ...e.patch }
        next.start = clamp(next.start, 0, duration)
        next.end = clamp(next.end, 0, duration)
        if (next.end - next.start < MIN_CLIP) {
          // Keep the edge that moved valid instead of dropping the edit.
          if (e.patch.start !== undefined && e.patch.end === undefined) next.start = next.end - MIN_CLIP
          else next.end = Math.min(duration, next.start + MIN_CLIP)
        }
        next.freezes = next.freezes.filter((f) => f.at >= next.start && f.at <= next.end)
        return next
      })
      return { project: { ...p, clips } }
    }
    case 'remove':
      return { project: { ...p, clips: p.clips.filter((c) => c.id !== e.id) }, select: null }
    case 'split': {
      const i = p.clips.findIndex((c) => c.id === e.id)
      const c = p.clips[i]
      if (!c || e.at - c.start < MIN_CLIP || c.end - e.at < MIN_CLIP) return { project: p }
      const left: Clip = { ...c, end: e.at, freezes: c.freezes.filter((f) => f.at <= e.at) }
      const right: Clip = { ...c, id: newId(), number: p.nextNumber, name: c.name ? `${c.name} (2)` : '', start: e.at, freezes: c.freezes.filter((f) => f.at > e.at) }
      const clips = [...p.clips]
      clips.splice(i, 1, left, right)
      return { project: { ...p, clips, nextNumber: p.nextNumber + 1 }, select: right.id }
    }
    case 'move-order': {
      const i = p.clips.findIndex((c) => c.id === e.id)
      const j = i + e.dir
      if (i < 0 || j < 0 || j >= p.clips.length) return { project: p }
      const clips = [...p.clips]
      ;[clips[i], clips[j]] = [clips[j], clips[i]]
      return { project: { ...p, clips } }
    }
  }
}

// --- history ---

export type History = { past: Project[]; present: Project; future: Project[]; selected: string | null; dirty: boolean; lastKey: string | null }

const HISTORY_LIMIT = 200

export function initHistory(p: Project): History {
  return { past: [], present: p, future: [], selected: p.clips[0]?.id ?? null, dirty: false, lastKey: null }
}

export type HistoryAction =
  | { type: 'edit'; edit: Edit; duration: number; /** Edits with the same key as the previous one merge into one undo step (a drag, typing). */ key?: string }
  | { type: 'undo' }
  | { type: 'redo' }
  | { type: 'select'; id: string | null }
  | { type: 'saved' }
  | { type: 'reset'; project: Project }

export function historyReducer(h: History, a: HistoryAction): History {
  switch (a.type) {
    case 'edit': {
      const { project, select } = applyEdit(h.present, a.edit, a.duration)
      if (project === h.present) return h
      const merge = !!a.key && a.key === h.lastKey
      const past = merge ? h.past : [...h.past, h.present].slice(-HISTORY_LIMIT)
      return { past, present: project, future: [], selected: select !== undefined ? select : h.selected, dirty: true, lastKey: a.key ?? null }
    }
    case 'undo': {
      if (!h.past.length) return h
      const present = h.past[h.past.length - 1]
      return { past: h.past.slice(0, -1), present, future: [h.present, ...h.future], selected: keepSelection(present, h.selected), dirty: true, lastKey: null }
    }
    case 'redo': {
      if (!h.future.length) return h
      const present = h.future[0]
      return { past: [...h.past, h.present], present, future: h.future.slice(1), selected: keepSelection(present, h.selected), dirty: true, lastKey: null }
    }
    case 'select':
      return { ...h, selected: a.id, lastKey: null }
    case 'saved':
      return { ...h, dirty: false }
    case 'reset':
      return initHistory(a.project)
  }
}

/** Merge key base of an edit: same clip, same kind of change. */
export function editKey(e: Edit) {
  if (e.type === 'update') return `update:${e.id}:${Object.keys(e.patch).sort().join(',')}`
  return e.type
}

function keepSelection(p: Project, id: string | null) {
  return id && p.clips.some((c) => c.id === id) ? id : (p.clips[0]?.id ?? null)
}

/** Lanes for overlapping clips: each clip gets the first lane free at its start. */
export function laneLayout(clips: Clip[]): { lanes: Map<string, number>; count: number } {
  const sorted = [...clips].sort((a, b) => a.start - b.start || a.number - b.number)
  const ends: number[] = []
  const lanes = new Map<string, number>()
  for (const c of sorted) {
    let lane = ends.findIndex((e) => e <= c.start + 1e-6)
    if (lane < 0) {
      lane = ends.length
      ends.push(c.end)
    } else ends[lane] = c.end
    lanes.set(c.id, lane)
  }
  return { lanes, count: Math.max(1, ends.length) }
}

/** The clip under the playhead: the selected one if it covers it, else the topmost. */
export function clipAt(clips: Clip[], t: number, selected: string | null): Clip | undefined {
  const sel = clips.find((c) => c.id === selected)
  if (sel && t > sel.start && t < sel.end) return sel
  return clips.find((c) => t > c.start && t < c.end)
}

/** Export file name of a clip (the extension is the server's to choose). */
export function exportName(p: Project, c: Clip) {
  return (c.name.trim() || defaultClipName(p, c.number)).replace(/[\\/:*?"<>|]/g, '-')
}
