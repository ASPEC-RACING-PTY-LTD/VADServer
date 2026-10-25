import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router-dom'
import { AlertTriangle, ArrowLeft, Clapperboard, FileClock, Plus, Trash2 } from 'lucide-react'
import { errorMessage } from '../../lib/api'
import { formatDate } from '../../lib/format'
import { Button, PageLoader, Spinner, useUI } from '../../components/ui'
import { deleteDraft, getDraft, listDrafts, mediaInfo, mediaStatus, type Draft } from './api'
import { emptyProject, loadProject } from './model'
import { Editor } from './editor/Editor'

/** The editor is always dark (VaultDock's navy theme), whatever the app theme is. */
function useForceDark() {
  useEffect(() => {
    const root = document.documentElement
    const was = root.classList.contains('dark')
    root.classList.add('dark')
    return () => {
      root.classList.toggle('dark', was)
    }
  }, [])
}

function Screen({ children }: { children: React.ReactNode }) {
  return <div className="flex h-dvh items-center justify-center bg-[#0b1020] p-6 text-slate-200">{children}</div>
}

function Problem({ title, message, actions }: { title: string; message: React.ReactNode; actions?: React.ReactNode }) {
  return (
    <Screen>
      <div className="w-full max-w-md rounded-2xl border border-white/10 bg-slate-900 p-6 text-center shadow-2xl">
        <div className="mx-auto mb-3 flex size-11 items-center justify-center rounded-full bg-amber-500/15 text-amber-400">
          <AlertTriangle className="size-5" />
        </div>
        <h1 className="text-lg font-semibold text-white">{title}</h1>
        <div className="mt-2 text-sm text-slate-400">{message}</div>
        <div className="mt-5 flex flex-wrap justify-center gap-2">
          {actions}
          <Link to="/files">
            <Button icon={<ArrowLeft className="size-4" />}>Back to files</Button>
          </Link>
        </div>
      </div>
    </Screen>
  )
}

/** Offered when the video already has drafts: carry on with one or start over. */
function ResumeChooser({ drafts, onPick, onNew }: { drafts: Draft[]; onPick: (d: Draft) => void; onNew: () => void }) {
  return (
    <Screen>
      <div className="w-full max-w-lg rounded-2xl border border-white/10 bg-slate-900 p-6 shadow-2xl">
        <div className="mb-4 flex items-center gap-3">
          <div className="flex size-10 items-center justify-center rounded-xl bg-brand-500/15 text-brand-300">
            <Clapperboard className="size-5" />
          </div>
          <div>
            <h1 className="font-semibold text-white">Continue editing?</h1>
            <p className="text-sm text-slate-400">This video has saved drafts.</p>
          </div>
        </div>
        <div className="space-y-2">
          {drafts.map((d) => (
            <button key={d.id} onClick={() => onPick(d)} className="flex w-full items-center gap-3 rounded-xl border border-white/10 bg-white/[.03] px-4 py-3 text-left transition hover:border-brand-400/60 hover:bg-brand-500/10">
              <FileClock className="size-4 shrink-0 text-brand-300" />
              <span className="min-w-0 flex-1">
                <span className="block truncate text-sm font-medium text-white">{d.name}</span>
                <span className="block text-xs text-slate-400">Saved {formatDate(d.updated_at)}</span>
              </span>
              {d.source_state === 'changed' && <span className="rounded bg-amber-500/15 px-1.5 py-0.5 text-[11px] text-amber-300">Video replaced</span>}
            </button>
          ))}
        </div>
        <div className="mt-5 flex justify-between gap-2">
          <Link to="/files">
            <Button icon={<ArrowLeft className="size-4" />}>Back</Button>
          </Link>
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={onNew}>
            Start a new edit
          </Button>
        </div>
      </div>
    </Screen>
  )
}

/**
 * /video-editor/:id[?draft=N] — loads the source video's stream information and, when asked
 * for, a draft. A draft whose video was trashed, removed or replaced says so instead of opening.
 */
export default function EditorPage() {
  useForceDark()
  const { id } = useParams()
  const sourceId = Number(id)
  const [params, setParams] = useSearchParams()
  const draftId = Number(params.get('draft')) || 0
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { confirm, toast } = useUI()
  const [fresh, setFresh] = useState(false)
  const [allowChanged, setAllowChanged] = useState(false)

  const status = useQuery({ queryKey: ['media-status'], queryFn: mediaStatus, staleTime: 60_000 })
  const draftQ = useQuery({ queryKey: ['video-draft', draftId], queryFn: () => getDraft(draftId), enabled: draftId > 0, staleTime: Infinity, gcTime: 0 })
  const sourceOK = !draftId || draftQ.data?.source_state === 'ok' || (draftQ.data?.source_state === 'changed' && allowChanged)
  const infoQ = useQuery({ queryKey: ['video-info', sourceId], queryFn: () => mediaInfo(sourceId), enabled: sourceId > 0 && sourceOK && (!draftId || !!draftQ.data), retry: false })
  const draftsQ = useQuery({ queryKey: ['video-drafts', sourceId], queryFn: () => listDrafts(sourceId), enabled: sourceId > 0 && !draftId && !fresh })

  if (!sourceId) return <Problem title="No video selected" message="Open the editor from a video's Edit video action." />
  if (status.data && !status.data.available)
    return <Problem title="Video editing is not available" message="This server has no FFmpeg, which thumbnails and clip exports need. The VaultDock server image includes it; on other installs an administrator can install ffmpeg." />
  if (draftQ.isError) return <Problem title="Draft not found" message={errorMessage(draftQ.error)} />
  const d = draftQ.data
  if (d && d.source_state !== 'ok' && !(d.source_state === 'changed' && allowChanged)) {
    const removeDraft = async () => {
      if (!(await confirm({ title: 'Delete this draft?', message: `"${d.name}" and its selections will be deleted. The video is not affected.`, confirmLabel: 'Delete draft', tone: 'danger' }))) return
      await deleteDraft(d.id)
      qc.invalidateQueries({ queryKey: ['video-drafts'] })
      toast('success', 'Draft deleted')
      navigate('/files')
    }
    const message =
      d.source_state === 'trashed' ? (
        <>
          The video this draft edits, <b className="text-slate-200">{d.source_name}</b>, is in the trash. Restore it from the trash to keep editing; the draft and its selections are kept.
        </>
      ) : d.source_state === 'changed' ? (
        <>
          <b className="text-slate-200">{d.source_name}</b> was replaced by a different file since this draft was saved, so its selections may no longer line up.
        </>
      ) : (
        <>
          The video this draft edits, <b className="text-slate-200">{d.source_name}</b>, no longer exists. The draft is kept until you delete it.
        </>
      )
    return (
      <Problem
        title={d.source_state === 'changed' ? 'The video has changed' : 'The source video is unavailable'}
        message={message}
        actions={
          <>
            {d.source_state === 'trashed' && (
              <Link to="/trash">
                <Button>Open trash</Button>
              </Link>
            )}
            {d.source_state === 'changed' && (
              <Button variant="primary" onClick={() => setAllowChanged(true)}>
                Open anyway
              </Button>
            )}
            <Button variant="danger" icon={<Trash2 className="size-4" />} onClick={removeDraft}>
              Delete draft
            </Button>
          </>
        }
      />
    )
  }
  if (infoQ.isError) return <Problem title="This video cannot be edited" message={errorMessage(infoQ.error)} />
  if (!infoQ.data || (draftId && !d) || status.isPending)
    return (
      <Screen>
        <div className="flex items-center gap-3 text-sm text-slate-400">
          <Spinner className="size-5" /> Opening the editor…
        </div>
      </Screen>
    )
  const drafts = draftsQ.data?.items ?? []
  if (!draftId && !fresh) {
    if (draftsQ.isPending) return <PageLoader />
    if (drafts.length > 0) return <ResumeChooser drafts={drafts} onPick={(x) => setParams({ draft: String(x.id) }, { replace: true })} onNew={() => setFresh(true)} />
  }
  const { node, info } = infoQ.data
  const project = d ? loadProject(d.data, node.name, info.duration) : emptyProject(node.name)
  return (
    <Editor
      key={`${node.id}:${draftId}`}
      source={node}
      info={info}
      initial={project}
      draftId={draftId || null}
      onDraftCreated={(newId) => {
        // The URL reopens this draft after a reload; the editor stays mounted (no navigation).
        setFresh(true)
        window.history.replaceState(window.history.state, '', `/video-editor/${node.id}?draft=${newId}`)
      }}
    />
  )
}
