import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { AlertTriangle, CheckCircle2, CircleDashed, FolderOpen, HardDrive, Loader2, Trash2, XCircle } from 'lucide-react'
import { del, errorMessage, get } from '../../../lib/api'
import { formatBytes } from '../../../lib/format'
import { Button, cx, Modal, ProgressBar, Toggle, useUI } from '../../../components/ui'
import { MoveModal } from '../../../components/files/MoveModal'
import type { Node } from '../../../components/files/types'
import { cancelExport, clipSpec, getExport, startExport, type ExportJob } from '../api'
import { clipMode, estimateSize, exportName, formatLength, outputDuration, type MediaInfo, type Project, type Quality } from '../model'

const QUALITIES: { id: Quality; label: string; hint: string }[] = [
  { id: 'high', label: 'High quality (recommended)', hint: 'Frame-accurate cuts, near-source quality, never above the source bitrate.' },
  { id: 'copy', label: 'Original (lossless copy)', hint: 'No re-encoding: fastest and exact source quality, but cuts snap to the nearest keyframe. Clips with speed, crop or freeze-frame changes are re-encoded at high quality.' },
  { id: 'balanced', label: 'Balanced', hint: 'Smaller files with little visible loss.' },
  { id: 'small', label: 'Smallest', hint: 'Much smaller files, for sharing.' },
]

function useFolderPath(id: number | null) {
  return useQuery({
    queryKey: ['folder-path', id],
    queryFn: async () => {
      const r = await get<{ breadcrumbs: { id: number; name: string }[] }>(`/files?parent=${id}&limit=1`)
      return '/' + r.breadcrumbs.slice(1).map((c) => c.name).join('/')
    },
    enabled: !!id,
  })
}

function StateIcon({ state }: { state: string }) {
  if (state === 'done') return <CheckCircle2 className="size-4 text-emerald-400" />
  if (state === 'failed') return <XCircle className="size-4 text-rose-400" />
  if (state === 'running') return <Loader2 className="size-4 animate-spin text-brand-300" />
  if (state === 'cancelled') return <XCircle className="size-4 text-slate-500" />
  return <CircleDashed className="size-4 text-slate-500" />
}

/**
 * Export Clips: confirms what will be written (every selection as its own file, sizes, the
 * destination and quality, optionally erasing the original), then follows the batch on the server.
 */
export function ExportDialog({
  open,
  onClose,
  source,
  info,
  project,
  draftId,
  onQuality,
  onBeforeExport,
}: {
  open: boolean
  onClose: () => void
  source: Node
  info: MediaInfo
  project: Project
  draftId: number | null
  onQuality: (q: Quality) => void
  onBeforeExport: () => Promise<void>
}) {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const { confirm, toast } = useUI()
  const [folderId, setFolderId] = useState<number | null>(source.parent_id)
  const [picking, setPicking] = useState(false)
  const [erase, setErase] = useState(false)
  const [jobId, setJobId] = useState<number | null>(null)
  const [starting, setStarting] = useState(false)
  const [error, setError] = useState('')
  const [purged, setPurged] = useState(false)
  const folderPath = useFolderPath(folderId)
  const job = useQuery({
    queryKey: ['video-export', jobId],
    queryFn: () => getExport(jobId!),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && !['queued', 'running'].includes(q.state.data.state) ? false : 700),
  })
  const j: ExportJob | undefined = job.data
  const active = !!j && (j.state === 'queued' || j.state === 'running')
  useEffect(() => {
    if (j && !active) {
      qc.invalidateQueries({ queryKey: ['files'] })
      qc.invalidateQueries({ queryKey: ['trash'] })
    }
  }, [j?.state])
  useEffect(() => {
    if (open && !active) {
      setError('')
      if (j && !active) setJobId(null)
    }
  }, [open])

  const clips = project.clips
  const sizes = clips.map((c) => estimateSize(c, project.quality, info))
  const total = sizes.reduce((a, b) => a + b, 0)
  const savings = source.size - total

  const start = async () => {
    if (!folderId) return
    setStarting(true)
    setError('')
    try {
      await onBeforeExport()
      const r = await startExport({
        source_id: source.id,
        source_sha256: source.sha256 ?? '',
        draft_id: draftId ?? undefined,
        folder_id: folderId,
        quality: project.quality,
        erase_original: erase,
        clips: clips.map((c) => ({ number: c.number, name: exportName(project, c), clip: clipSpec(c) })),
      })
      setPurged(false)
      setJobId(r.id)
    } catch (e) {
      setError(errorMessage(e))
    } finally {
      setStarting(false)
    }
  }

  const purgeOriginal = async () => {
    if (!j) return
    const ok = await confirm({
      title: 'Permanently delete the original now?',
      message: (
        <>
          <b>{j.progress.original.name}</b> ({formatBytes(j.progress.original.size)}) will be deleted for good and its space freed at once. This cannot be undone; the exported clips are not affected.
        </>
      ),
      confirmLabel: 'Delete permanently',
      tone: 'danger',
      requireText: 'DELETE',
    })
    if (!ok) return
    try {
      await del(`/trash/${j.progress.original.node_id}`)
      qc.invalidateQueries({ queryKey: ['trash'] })
      toast('success', 'Original deleted', `${formatBytes(j.progress.original.size)} freed`)
      job.refetch()
      setPurged(true)
    } catch (e) {
      toast('error', 'Could not delete the original', errorMessage(e))
    }
  }
  // --- progress view ---
  if (j) {
    const n = j.progress.clips.length
    const done = j.progress.clips.filter((c) => c.state === 'done').length
    const failed = j.progress.clips.filter((c) => c.state === 'failed').length
    const overall = (j.progress.clips.reduce((a, c) => a + (c.state === 'done' ? 1 : c.state === 'running' ? c.progress : 0), 0) / Math.max(1, n)) * 100
    const current = j.progress.current >= 0 ? j.progress.clips[j.progress.current] : undefined
    const o = j.progress.original
    return (
      <Modal
        open={open}
        onClose={active ? () => {} : onClose}
        size="lg"
        title={active ? 'Exporting clips…' : j.state === 'done' ? 'Clips exported' : j.state === 'cancelled' ? 'Export cancelled' : 'Export finished with problems'}
        description={`${done} of ${n} clip${n === 1 ? '' : 's'} done${failed ? `, ${failed} failed` : ''} · to ${j.progress.folder}`}
        footer={
          active ? (
            <Button key="cancel" variant="danger" onClick={() => cancelExport(j.id)}>
              Cancel export
            </Button>
          ) : (
            <>
              <Button key="open" icon={<FolderOpen className="size-4" />} onClick={() => navigate(folderId ? `/files/${folderId}` : '/files')}>
                Open folder
              </Button>
              <Button key="done" variant="primary" onClick={onClose}>
                Done
              </Button>
            </>
          )
        }
      >
        <ProgressBar value={active ? overall : 100} tone={j.state === 'done' ? 'green' : active ? 'brand' : 'amber'} />
        {current && (
          <p className="mt-2 text-xs text-slate-400">
            Now: <span className="text-slate-200">#{current.number} {current.name}</span> ({Math.round(current.progress * 100)}%)
          </p>
        )}
        <ul className="scroll-thin mt-4 max-h-72 space-y-1.5 overflow-y-auto">
          {j.progress.clips.map((c) => (
            <li key={c.number} className="rounded-lg border border-slate-800 bg-slate-900/60 px-3 py-2">
              <div className="flex items-center gap-2.5 text-sm">
                <StateIcon state={c.state} />
                <span className="font-mono text-xs text-brand-300">#{c.number}</span>
                <span className="min-w-0 flex-1 truncate">{c.name}</span>
                <span className="font-mono text-xs text-slate-400 tabular-nums">{c.state === 'done' && c.size ? formatBytes(c.size) : c.state === 'running' ? `${Math.round(c.progress * 100)}%` : `~${formatBytes(c.estimate)}`}</span>
              </div>
              {c.state === 'running' && <ProgressBar value={c.progress * 100} className="mt-1.5 h-1" />}
              {c.error && <p className="mt-1 pl-6 text-xs text-rose-300">{c.error}</p>}
            </li>
          ))}
        </ul>
        {j.progress.error && !active && <p className="mt-3 text-sm text-amber-300">{j.progress.error}</p>}
        {j.state === 'interrupted' && <p className="mt-3 text-sm text-amber-300">The server restarted during the export. Clips marked done were saved; export the rest again.</p>}
        {j.progress.erase_original && !active && (
          <div className={cx('mt-4 rounded-xl border p-3.5 text-sm', o.state === 'trashed' ? 'border-emerald-500/30 bg-emerald-500/[.06]' : 'border-amber-500/30 bg-amber-500/[.06]')}>
            {o.state === 'trashed' ? (
              <>
                <p className="font-medium text-emerald-200">{purged ? 'The original was permanently deleted.' : 'The original was moved to the trash.'}</p>
                {!purged && (
                  <>
                    <p className="mt-1 text-xs leading-relaxed text-slate-400">
                      {o.name} stays in the trash for 7 days and can be restored until then. Its {formatBytes(o.size)} are only freed when it is permanently deleted.
                    </p>
                    <Button size="sm" variant="danger" className="mt-3" icon={<Trash2 className="size-3.5" />} onClick={purgeOriginal}>
                      Permanently delete original now
                    </Button>
                  </>
                )}
              </>
            ) : (
              <p className="flex gap-2 text-amber-200">
                <AlertTriangle className="mt-0.5 size-4 shrink-0" /> {o.error || 'The original was kept.'}
              </p>
            )}
          </div>
        )}
      </Modal>
    )
  }

  // --- confirmation ---
  return (
    <>
      <Modal
        open={open && !picking}
        onClose={onClose}
        size="lg"
        title={`Export ${clips.length} clip${clips.length === 1 ? '' : 's'}`}
        description={`From ${source.name}. Each selection becomes its own video file; the original is not modified.`}
        footer={
          <>
            <Button onClick={onClose}>Cancel</Button>
            <Button variant="primary" loading={starting} disabled={!clips.length || !folderId} onClick={start}>
              Export {clips.length} clip{clips.length === 1 ? '' : 's'}
            </Button>
          </>
        }
      >
        <div className="scroll-thin max-h-56 overflow-y-auto rounded-xl border border-slate-800">
          <table className="w-full table-fixed text-sm">
            <thead className="sticky top-0 bg-slate-900 text-left text-[11px] tracking-wide text-slate-500 uppercase">
              <tr>
                <th className="w-14 px-3 py-2">#</th>
                <th className="px-3 py-2">File</th>
                <th className="w-20 px-3 py-2 text-right">Length</th>
                <th className="w-24 px-3 py-2 text-right">Est. size</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-800/80">
              {clips.map((c, i) => (
                <tr key={c.id}>
                  <td className="px-3 py-1.5 font-mono text-xs text-brand-300">#{c.number}</td>
                  <td className="px-3 py-1.5">
                    <div className="truncate">{exportName(project, c)}</div>
                    {clipMode(c, project.quality) !== project.quality && <div className="text-[11px] text-violet-300">re-encoded (has edits)</div>}
                  </td>
                  <td className="px-3 py-1.5 text-right font-mono text-xs tabular-nums">{formatLength(outputDuration(c))}</td>
                  <td className="px-3 py-1.5 text-right font-mono text-xs tabular-nums">{formatBytes(sizes[i])}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <div className="mt-3 grid grid-cols-3 gap-2 text-center">
          {[
            ['Clips total', `~${formatBytes(total)}`],
            ['Original', formatBytes(source.size)],
            ['Saved if erased', savings > 0 ? `~${formatBytes(savings)}` : '—'],
          ].map(([k, v]) => (
            <div key={k} className="rounded-xl bg-slate-800/50 px-2 py-2">
              <div className="text-[11px] text-slate-400">{k}</div>
              <div className="font-mono text-sm text-slate-100 tabular-nums">{v}</div>
            </div>
          ))}
        </div>

        <div className="mt-4 grid gap-4 sm:grid-cols-2">
          <div>
            <span className="label">Destination</span>
            <button type="button" onClick={() => setPicking(true)} className="flex h-9 w-full items-center gap-2 rounded-lg border border-slate-700 bg-slate-900 px-3 text-left text-sm hover:border-brand-400">
              <FolderOpen className="size-4 shrink-0 text-brand-300" />
              <span className="truncate">{folderPath.data ?? '…'}</span>
              <span className="ml-auto text-xs text-slate-500">Change</span>
            </button>
          </div>
          <div>
            <span className="label">Quality</span>
            <select className="input h-9 py-1" value={project.quality} onChange={(e) => onQuality(e.target.value as Quality)} aria-label="Output quality">
              {QUALITIES.map((q) => (
                <option key={q.id} value={q.id}>
                  {q.label}
                </option>
              ))}
            </select>
          </div>
        </div>
        <p className="mt-1.5 text-xs text-slate-400">{QUALITIES.find((q) => q.id === project.quality)?.hint}</p>

        <div className="mt-4 rounded-xl border border-slate-800 p-3.5">
          <Toggle
            checked={erase}
            onChange={setErase}
            label="Erase original after export"
            description={
              <>
                Only once every clip was exported, checked to play and saved. The original then goes to the trash for 7 days (restorable until then). <b>The trash still uses disk space</b>: it is recovered when the original is permanently deleted, after 7 days or at once from the summary.
              </>
            }
          />
        </div>
        {erase && (
          <p className="mt-2 flex items-center gap-2 text-xs text-slate-400">
            <HardDrive className="size-3.5" /> If any clip fails, the original is kept.
          </p>
        )}
        {error && <p className="mt-3 rounded-lg bg-rose-500/10 px-3 py-2 text-sm text-rose-300">{error}</p>}
      </Modal>
      <MoveModal
        ids={picking ? [] : null}
        title="Export to folder"
        actionLabel="Use this folder"
        description="The clips are saved here."
        onClose={() => setPicking(false)}
        onMove={(target) => setFolderId(target)}
      />
    </>
  )
}
