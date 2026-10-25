import { useCallback, useEffect, useReducer, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { ArrowLeft, Clapperboard, Download, Keyboard, ListVideo, PanelRightClose, PanelRightOpen, Redo2, Save, SlidersHorizontal, Undo2 } from 'lucide-react'
import { errorMessage } from '../../../lib/api'
import { formatBytes } from '../../../lib/format'
import { Button, cx, Modal, useUI } from '../../../components/ui'
import { contentURL, type Node } from '../../../components/files/types'
import { createDraft, updateDraft } from '../api'
import { clamp, clipAt, editKey, formatTime, historyReducer, initHistory, type Edit, type MediaInfo, type Project, type Quality, type Rect } from '../model'
import { ClipList, ClipSettings } from './ClipPanel'
import { ExportDialog } from './ExportDialog'
import { Player } from './Player'
import { Timeline } from './Timeline'

const NEW_CLIP_SECONDS = 10

function readPref<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key)
    return v === null ? fallback : (JSON.parse(v) as T)
  } catch {
    return fallback
  }
}
function writePref(key: string, v: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(v))
  } catch {
    // not remembered
  }
}

const SHORTCUTS: [string, string][] = [
  ['Space / K', 'Play or pause'],
  ['← / →', 'Previous / next frame'],
  ['Shift + ← / →', 'Back / forward 1 second'],
  ['J / L', 'Back / forward 5 seconds'],
  ['Home / End', 'Start / end of the recording'],
  ['A', 'Add a selection at the playhead'],
  ['S', 'Split the selection at the playhead'],
  ['I / O', 'Set the selected clip’s start / end to the playhead'],
  ['[ / ]', 'Previous / next selection'],
  ['P', 'Preview the selected clip as exported'],
  ['Delete', 'Remove the selected clip'],
  ['+ / -', 'Zoom the timeline'],
  ['M', 'Mute the preview'],
  ['F', 'Full screen'],
  ['Ctrl + Z / Ctrl + Shift + Z', 'Undo / redo'],
  ['Ctrl + S', 'Save draft'],
]

/**
 * Full-screen video editor: toolbar on top, the recording in the middle, a contextual panel on the
 * right and the timeline along the bottom. Edits are instructions (a Project); the source file is
 * only read, and exports happen on the server.
 */
export function Editor({ source, info, initial, draftId: initialDraft, onDraftCreated }: { source: Node; info: MediaInfo; initial: Project; draftId: number | null; onDraftCreated: (id: number) => void }) {
  const navigate = useNavigate()
  const { toast, confirm } = useUI()
  const [h, dispatch] = useReducer(historyReducer, initial, initHistory)
  const project = h.present
  const duration = info.duration
  const selected = project.clips.find((c) => c.id === h.selected)

  // --- playback ---
  const videoRef = useRef<HTMLVideoElement>(null)
  const [time, setTime] = useState(0)
  const [playing, setPlaying] = useState(false)
  const [rate, setRate] = useState(1)
  const [volume, setVolume] = useState(() => readPref('vd-editor-volume', 0.8))
  const [muted, setMuted] = useState(false)
  const [previewing, setPreviewing] = useState<string | null>(null)
  const previewClip = project.clips.find((c) => c.id === previewing)
  const hold = useRef<{ timer: number | null; last: number }>({ timer: null, last: 0 })

  const seek = useCallback(
    (t: number) => {
      const v = videoRef.current
      const x = clamp(t, 0, duration)
      if (v) v.currentTime = x
      hold.current.last = x
      setTime(x)
    },
    [duration],
  )
  const togglePlay = () => {
    const v = videoRef.current
    if (!v) return
    if (v.paused) {
      if (v.currentTime >= duration - 0.05) seek(0)
      v.play().catch(() => {})
    } else v.pause()
  }
  const step = (frames: number) => {
    const v = videoRef.current
    v?.pause()
    seek((v?.currentTime ?? time) + frames / (info.fps || 30))
  }

  useEffect(() => {
    const v = videoRef.current
    if (!v) return
    const on = () => setPlaying(!v.paused)
    const upd = () => !playing && setTime(v.currentTime)
    v.addEventListener('play', on)
    v.addEventListener('pause', on)
    v.addEventListener('seeked', upd)
    v.addEventListener('timeupdate', upd)
    return () => {
      v.removeEventListener('play', on)
      v.removeEventListener('pause', on)
      v.removeEventListener('seeked', upd)
      v.removeEventListener('timeupdate', upd)
    }
  }, [playing])

  // Smooth playhead while playing; previews loop their clip and hold freeze frames.
  useEffect(() => {
    if (!playing) return
    let raf = 0
    const tick = () => {
      const v = videoRef.current
      if (v) {
        let t = v.currentTime
        const c = previewClip
        if (c) {
          if (t >= c.end - 0.02 || t < c.start - 0.3) {
            v.currentTime = c.start
            t = c.start
          } else {
            const f = c.freezes.find((f) => hold.current.last < f.at && t >= f.at)
            if (f && hold.current.timer === null) {
              v.pause()
              hold.current.timer = window.setTimeout(() => {
                hold.current.timer = null
                videoRef.current?.play().catch(() => {})
              }, f.duration * 1000)
            }
          }
        }
        hold.current.last = t
        setTime(t)
      }
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [playing, previewClip])

  // Preview settings: the clip's speed, pitch and volume while previewing it.
  useEffect(() => {
    const v = videoRef.current as (HTMLVideoElement & { preservesPitch?: boolean }) | null
    if (!v) return
    const c = previewClip
    v.playbackRate = c ? c.speed : rate
    v.preservesPitch = c ? c.preservePitch : true
    v.volume = clamp(c ? volume * c.volume : volume, 0, 1)
    v.muted = muted || (!!c && (c.muted || c.volume === 0))
  }, [previewClip, rate, volume, muted])
  useEffect(() => writePref('vd-editor-volume', volume), [volume])

  const startPreview = (id: string) => {
    const c = project.clips.find((x) => x.id === id)
    if (!c) return
    if (hold.current.timer !== null) {
      clearTimeout(hold.current.timer)
      hold.current.timer = null
    }
    dispatch({ type: 'select', id })
    setPreviewing(id)
    seek(c.start)
    videoRef.current?.play().catch(() => {})
  }
  const stopPreview = () => {
    if (hold.current.timer !== null) clearTimeout(hold.current.timer)
    hold.current.timer = null
    setPreviewing(null)
    videoRef.current?.pause()
  }
  useEffect(() => {
    if (previewing && !previewClip) stopPreview()
  }, [previewing, previewClip])

  // --- panels ---
  const [panelOpen, setPanelOpen] = useState(() => readPref('vd-editor-panel', true))
  const [panelTab, setPanelTab] = useState<'clip' | 'clips'>(initial.clips.length ? 'clips' : 'clip')

  // --- edits ---
  const keyRef = useRef<{ key: string | null; n: number }>({ key: null, n: 0 })
  const onEdit = useCallback(
    (e: Edit, coalesce = false) => {
      const base = editKey(e)
      let key: string
      if (coalesce && keyRef.current.key?.startsWith(base + '#')) key = keyRef.current.key
      else key = `${base}#${++keyRef.current.n}`
      keyRef.current.key = key
      dispatch({ type: 'edit', edit: e, duration, key: coalesce || e.type === 'update' ? key : undefined })
    },
    [duration],
  )
  const addAtPlayhead = () => {
    let start = time
    if (duration - start < 1) start = Math.max(0, duration - NEW_CLIP_SECONDS)
    onEdit({ type: 'add', start, end: Math.min(duration, start + NEW_CLIP_SECONDS) })
    setPanelTab('clip')
    setPanelOpen(true)
  }
  const splitTarget = clipAt(project.clips, time, h.selected)
  const split = () => splitTarget && onEdit({ type: 'split', id: splitTarget.id, at: time })
  const setCrop = (r: Rect, coalesce: boolean) => selected && onEdit({ type: 'update', id: selected.id, patch: { crop: r } }, coalesce)

  // --- drafts ---
  const [draftId, setDraftId] = useState(initialDraft)
  const [saving, setSaving] = useState(false)
  const save = async (quiet = false) => {
    setSaving(true)
    try {
      const name = project.name.trim() || source.name
      if (draftId) await updateDraft(draftId, name, project)
      else {
        const d = await createDraft(source.id, name, project)
        setDraftId(d.id)
        onDraftCreated(d.id)
      }
      dispatch({ type: 'saved' })
      if (!quiet) toast('success', 'Draft saved', 'Your selections are kept; reopen this video to continue.')
    } catch (e) {
      toast('error', 'Could not save the draft', errorMessage(e))
      throw e
    } finally {
      setSaving(false)
    }
  }

  // --- layout ---
  useEffect(() => writePref('vd-editor-panel', panelOpen), [panelOpen])
  const [exportOpen, setExportOpen] = useState(false)
  const [helpOpen, setHelpOpen] = useState(false)

  const exit = async () => {
    if (h.dirty && project.clips.length > 0) {
      const ok = await confirm({ title: 'Leave the editor?', message: 'Your changes since the last save will be lost. Save a draft first to keep them.', confirmLabel: 'Leave without saving', tone: 'danger' })
      if (!ok) return
    }
    navigate(source.parent_id ? `/files/${source.parent_id}?select=${source.id}` : '/files')
  }
  useEffect(() => {
    if (!h.dirty || project.clips.length === 0) return
    const h2 = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', h2)
    return () => window.removeEventListener('beforeunload', h2)
  }, [h.dirty, project.clips.length])

  // --- keyboard ---
  const keys = useRef<(e: KeyboardEvent) => void>(() => {})
  keys.current = (e: KeyboardEvent) => {
    const mod = e.ctrlKey || e.metaKey
    if (mod && e.key.toLowerCase() === 's') {
      e.preventDefault()
      save().catch(() => {})
      return
    }
    const el = e.target as HTMLElement
    if (el && (el.tagName === 'INPUT' || el.tagName === 'TEXTAREA' || el.tagName === 'SELECT' || el.isContentEditable)) return
    if (document.querySelector('[role="dialog"]')) return
    const k = e.key
    const handled = () => e.preventDefault()
    if (mod && k.toLowerCase() === 'z') {
      handled()
      dispatch({ type: e.shiftKey ? 'redo' : 'undo' })
    } else if (mod && k.toLowerCase() === 'y') {
      handled()
      dispatch({ type: 'redo' })
    } else if (mod) return
    else if (k === ' ' || k === 'k') {
      handled()
      togglePlay()
    } else if (k === 'ArrowLeft' || k === 'ArrowRight') {
      handled()
      const dir = k === 'ArrowLeft' ? -1 : 1
      if (e.shiftKey) seek(time + dir)
      else step(dir)
    } else if (k === 'j' || k === 'l') seek(time + (k === 'j' ? -5 : 5))
    else if (k === 'Home') seek(0)
    else if (k === 'End') seek(duration)
    else if (k === 'a') addAtPlayhead()
    else if (k === 's') split()
    else if (k === 'i' && selected) onEdit({ type: 'update', id: selected.id, patch: { start: Math.min(time, selected.end - 0.1) } })
    else if (k === 'o' && selected) onEdit({ type: 'update', id: selected.id, patch: { end: Math.max(time, selected.start + 0.1) } })
    else if ((k === 'Delete' || k === 'Backspace') && selected) {
      handled()
      onEdit({ type: 'remove', id: selected.id })
    } else if (k === 'p' && selected) (previewing ? stopPreview() : startPreview(selected.id))
    else if (k === '[' || k === ']') {
      const list = project.clips
      if (!list.length) return
      const i = list.findIndex((c) => c.id === h.selected)
      const next = list[(i + (k === ']' ? 1 : -1) + list.length) % list.length]
      dispatch({ type: 'select', id: next.id })
      seek(next.start)
    } else if (k === '+' || k === '=' || k === '-') window.dispatchEvent(new CustomEvent('vd-timeline-zoom', { detail: k === '-' ? -1 : 1 }))
    else if (k === 'm') setMuted((m) => !m)
    else if (k === 'f') {
      const wrap = videoRef.current?.closest('.flex-col') as HTMLElement | null
      if (document.fullscreenElement) document.exitFullscreen()
      else wrap?.requestFullscreen?.()
    } else if (k === '?') setHelpOpen(true)
  }
  useEffect(() => {
    const h3 = (e: KeyboardEvent) => keys.current(e)
    window.addEventListener('keydown', h3)
    return () => window.removeEventListener('keydown', h3)
  }, [])

  const aspect = info.width / info.height
  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-[#0b1020] text-slate-200">
      {/* top toolbar */}
      <header className="flex h-14 shrink-0 items-center gap-2 border-b border-white/10 bg-[#0d1326] px-3">
        <button type="button" onClick={exit} className="flex h-9 items-center gap-1.5 rounded-lg px-2.5 text-sm text-slate-300 hover:bg-white/[.07] hover:text-white" title="Exit editor">
          <ArrowLeft className="size-4" /> <span className="hidden md:inline">Exit</span>
        </button>
        <div className="mx-1 h-6 w-px bg-white/10" />
        <div className="flex size-8 shrink-0 items-center justify-center rounded-lg bg-gradient-to-br from-brand-500 to-violet-600 text-white shadow-md shadow-brand-900/50">
          <Clapperboard className="size-4" />
        </div>
        <div className="min-w-0">
          <input
            value={project.name}
            onChange={(e) => onEdit({ type: 'rename-project', name: e.target.value }, true)}
            aria-label="Project name"
            className="h-7 w-[min(28rem,40vw)] rounded-md border border-transparent bg-transparent px-1.5 text-sm font-semibold text-white hover:border-white/10 focus:border-brand-400 focus:bg-white/[.04] focus:outline-none"
          />
          <div className="truncate px-1.5 text-[11px] text-slate-400" title={source.name}>
            {source.name} · {info.width}×{info.height} · {Math.round(info.fps)} fps · {formatTime(duration, 0)} · {formatBytes(source.size)}
          </div>
        </div>
        <div className="ml-auto flex items-center gap-1">
          <button type="button" title="Undo (Ctrl+Z)" aria-label="Undo" disabled={!h.past.length} onClick={() => dispatch({ type: 'undo' })} className="flex size-9 items-center justify-center rounded-lg text-slate-300 hover:bg-white/[.07] disabled:opacity-30">
            <Undo2 className="size-4" />
          </button>
          <button type="button" title="Redo (Ctrl+Shift+Z)" aria-label="Redo" disabled={!h.future.length} onClick={() => dispatch({ type: 'redo' })} className="flex size-9 items-center justify-center rounded-lg text-slate-300 hover:bg-white/[.07] disabled:opacity-30">
            <Redo2 className="size-4" />
          </button>
          <button type="button" title="Keyboard shortcuts (?)" aria-label="Keyboard shortcuts" onClick={() => setHelpOpen(true)} className="hidden size-9 items-center justify-center rounded-lg text-slate-300 hover:bg-white/[.07] sm:flex">
            <Keyboard className="size-4" />
          </button>
          <div className="mx-1 h-6 w-px bg-white/10" />
          <span className="hidden text-[11px] text-slate-500 lg:inline">{h.dirty ? 'Unsaved changes' : draftId ? 'Draft saved' : ''}</span>
          <Button size="sm" icon={<Save className="size-3.5" />} loading={saving} onClick={() => save().catch(() => {})} className="!border-white/10 !bg-white/[.05] !text-slate-100 hover:!bg-white/[.1]">
            Save draft
          </Button>
          <Button size="sm" variant="primary" icon={<Download className="size-3.5" />} disabled={!project.clips.length} onClick={() => setExportOpen(true)}>
            Export clips{project.clips.length ? ` (${project.clips.length})` : ''}
          </Button>
          <button type="button" title={panelOpen ? 'Hide the side panel' : 'Show the side panel'} onClick={() => setPanelOpen(!panelOpen)} className="ml-1 flex size-9 items-center justify-center rounded-lg text-slate-300 hover:bg-white/[.07]">
            {panelOpen ? <PanelRightClose className="size-4" /> : <PanelRightOpen className="size-4" />}
          </button>
        </div>
      </header>

      {/* preview + side panel */}
      <div className="flex min-h-0 flex-1">
        <Player
          src={contentURL(source.id)}
          info={info}
          videoRef={videoRef}
          time={time}
          playing={playing}
          rate={rate}
          volume={volume}
          muted={muted}
          clip={selected}
          previewing={!!previewClip}
          onTogglePlay={togglePlay}
          onSeek={seek}
          onStep={step}
          onRate={setRate}
          onVolume={(v) => {
            setVolume(v)
            if (v > 0) setMuted(false)
          }}
          onMute={() => setMuted(!muted)}
          onPreview={() => (previewClip ? stopPreview() : selected && startPreview(selected.id))}
          onCrop={setCrop}
        />
        {panelOpen && (
          <aside className="flex w-[340px] shrink-0 flex-col border-l border-white/10 bg-[#0f1630] max-lg:absolute max-lg:top-14 max-lg:right-0 max-lg:bottom-0 max-lg:z-30 max-lg:shadow-2xl">
            <div className="flex shrink-0 gap-1 border-b border-white/[.06] p-2" role="tablist">
              {(
                [
                  ['clip', 'Selected clip', <SlidersHorizontal key="i" className="size-3.5" />],
                  ['clips', `Clips (${project.clips.length})`, <ListVideo key="i" className="size-3.5" />],
                ] as const
              ).map(([id, label, icon]) => (
                <button
                  key={id}
                  role="tab"
                  aria-selected={panelTab === id}
                  onClick={() => setPanelTab(id)}
                  className={cx('flex h-8 flex-1 items-center justify-center gap-1.5 rounded-lg text-xs font-medium transition', panelTab === id ? 'bg-white/[.09] text-white' : 'text-slate-400 hover:text-slate-200')}
                >
                  {icon}
                  {label}
                </button>
              ))}
            </div>
            <div className="scroll-thin min-h-0 flex-1 overflow-y-auto">
              {panelTab === 'clip' && selected ? (
                <ClipSettings
                  key={selected.id}
                  clip={selected}
                  project={project}
                  info={info}
                  time={time}
                  onEdit={onEdit}
                  onSeek={seek}
                  previewing={previewing === selected.id}
                  onPreview={() => (previewing === selected.id ? stopPreview() : startPreview(selected.id))}
                />
              ) : panelTab === 'clip' ? (
                <div className="px-6 py-10 text-center text-sm text-slate-400">Select a clip on the timeline to adjust it.</div>
              ) : (
                <ClipList
                  project={project}
                  info={info}
                  time={time}
                  selected={h.selected}
                  onSelect={(id) => dispatch({ type: 'select', id })}
                  onEdit={onEdit}
                  onSeek={seek}
                  onPreview={startPreview}
                />
              )}
            </div>
          </aside>
        )}
      </div>

      {/* timeline */}
      <div className="h-[clamp(190px,30vh,300px)] shrink-0">
        <Timeline
          duration={duration}
          time={time}
          playing={playing}
          clips={project.clips}
          selected={h.selected}
          sourceId={source.id}
          sha={source.sha256}
          aspect={aspect}
          onSeek={seek}
          onSelect={(id) => {
            dispatch({ type: 'select', id })
            if (id) setPanelTab('clip')
          }}
          onEdit={onEdit}
          onAddAtPlayhead={addAtPlayhead}
          onSplit={split}
          canSplit={!!splitTarget}
        />
      </div>

      <ExportDialog
        open={exportOpen}
        onClose={() => setExportOpen(false)}
        source={source}
        info={info}
        project={project}
        draftId={draftId}
        onQuality={(q: Quality) => onEdit({ type: 'quality', quality: q })}
        onBeforeExport={async () => {
          if (draftId && h.dirty) await save(true)
        }}
      />
      <Modal open={helpOpen} onClose={() => setHelpOpen(false)} title="Keyboard shortcuts" size="md">
        <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-1.5 text-sm">
          {SHORTCUTS.map(([k, v]) => (
            <div key={k} className="contents">
              <dt>
                <kbd className="kbd">{k}</kbd>
              </dt>
              <dd className="text-slate-300">{v}</dd>
            </div>
          ))}
        </dl>
      </Modal>
    </div>
  )
}
