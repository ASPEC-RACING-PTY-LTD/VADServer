import { useEffect, useMemo, useState } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Check, Folder, FolderPlus, ListPlus, X } from 'lucide-react'
import { Badge, Button, cx, Field, IconButton, PageHeader, PageLoader, Toggle, useUI } from '../../components/ui'
import { MoveModal } from '../../components/files/MoveModal'
import { get, errorMessage } from '../../lib/api'
import { formatBytes } from '../../lib/format'
import { smartApi, smartKeys, usePresets, type Filter, type OutputSettings, type SegmentContext } from './api'
import { CapabilityReport, ContextEditor, defaultContext, DescribeBox, emptyFilter, FilterEditor, OutputEditor, useFilterCheck } from './shared'

type Src = { id: number; name: string; dir: boolean }

function parseIds(v: string | null) {
  return (v ?? '')
    .split(',')
    .map(Number)
    .filter((n) => Number.isInteger(n) && n > 0)
}

/** Create a queue: recordings, what to find, how segments and clips are made. */
export default function NewQueuePage() {
  const [params] = useSearchParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast } = useUI()
  const presets = usePresets()
  const [sources, setSources] = useState<Src[]>([])
  const [recursive, setRecursive] = useState(params.get('recursive') !== '0')
  const [picking, setPicking] = useState<'source' | 'output' | null>(null)
  const [chosen, setChosen] = useState<string[]>([])
  const [custom, setCustom] = useState<Filter>(emptyFilter())
  const [ctx, setCtx] = useState<SegmentContext>(defaultContext)
  const [ctxTouched, setCtxTouched] = useState(false)
  const [output, setOutput] = useState<OutputSettings>({ quality: 'auto', destination: 'beside', subfolder: 'Highlights' })
  const [outFolder, setOutFolder] = useState('')
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [autoApprove, setAutoApprove] = useState(false)
  const [busy, setBusy] = useState(false)
  const check = useFilterCheck()
  // Start from the user's saved defaults (Smart Processing > Settings).
  const prefs = useQuery({ queryKey: smartKeys.sub('prefs'), queryFn: smartApi.prefs })
  const [prefsApplied, setPrefsApplied] = useState(false)
  useEffect(() => {
    if (!prefs.data || prefsApplied) return
    setPrefsApplied(true)
    setOutput(prefs.data.output)
    setAutoApprove(prefs.data.auto_approve)
    if (prefs.data.preset_ids.length) setChosen(prefs.data.preset_ids)
    setCtx(prefs.data.context)
    setCtxTouched(true)
  }, [prefs.data, prefsApplied])

  // Files and folders handed over from the file browser.
  const initFiles = params.get('files')
  const initFolders = params.get('folders')
  useEffect(() => {
    const ids = [...parseIds(initFiles), ...parseIds(initFolders)]
    if (!ids.length) return
    Promise.all(ids.map((id) => get<{ id: number; name: string; is_dir: boolean }>(`/files/${id}`).catch(() => null))).then((ns) =>
      setSources(ns.filter((n): n is { id: number; name: string; is_dir: boolean } => !!n).map((n) => ({ id: n.id, name: n.name, dir: n.is_dir }))),
    )
  }, [initFiles, initFolders])

  // Segment settings follow the first chosen preset until edited.
  useEffect(() => {
    if (ctxTouched) return
    const p = presets.data?.presets.find((x) => x.id === chosen[0])
    setCtx(p?.filter.context ?? defaultContext)
  }, [chosen, presets.data, ctxTouched])

  const folders = sources.filter((s) => s.dir)
  const files = sources.filter((s) => !s.dir)
  const count = useQuery({
    queryKey: smartKeys.sub('count', folders.map((f) => f.id), recursive),
    queryFn: async () => {
      let n = 0
      let bytes = 0
      for (const f of folders) {
        const r = await smartApi.folderFiles(f.id, recursive)
        n += r.count
        bytes += r.bytes
      }
      return { n, bytes }
    },
    enabled: folders.length > 0,
  })

  const merged = useMemo(() => ({ ...custom, context: ctx }), [custom, ctx])
  const customRules = custom.rules.length
  useEffect(() => {
    if (customRules) check.check(merged)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [merged, customRules])

  if (presets.isLoading) return <PageLoader />
  const list = presets.data?.presets ?? []
  const totalRecordings = files.length + (count.data?.n ?? 0)
  const canCreate = name.trim() && sources.length > 0 && (chosen.length > 0 || customRules > 0) && !(output.destination === 'folder' && !output.folder_id)

  const create = async () => {
    setBusy(true)
    try {
      const q = await smartApi.createQueue({
        name: name.trim(),
        description,
        preset_ids: chosen,
        filter: customRules ? { rules: custom.rules, context: ctx } : undefined,
        context: ctx,
        output,
        auto_approve: autoApprove,
        file_ids: files.map((f) => f.id),
        folder_ids: folders.map((f) => f.id),
        recursive,
      })
      qc.invalidateQueries({ queryKey: smartKeys.all })
      toast('success', 'Queue created', `${q.tally?.items ?? 0} recordings queued. Processing runs in the background.`)
      navigate(`/smart/queues/${q.id}`)
    } catch (e) {
      toast('error', 'The queue was not created', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="space-y-6">
      <PageHeader title="New queue" icon={<ListPlus className="size-5" />} description="Nothing is rendered or deleted automatically: suggestions wait for your review unless you turn on auto-approve." />

      <section className="card space-y-4 p-5">
        <h2 className="font-semibold">1. Recordings</h2>
        <div className="flex flex-wrap gap-2">
          {sources.map((s) => (
            <span key={s.id} className="inline-flex items-center gap-1.5 rounded-full bg-slate-100 py-1 pr-1 pl-3 text-sm dark:bg-slate-800">
              {s.dir && <Folder className="size-3.5" />}
              {s.name || '/'}
              <IconButton label={`Remove ${s.name}`} onClick={() => setSources(sources.filter((x) => x.id !== s.id))}>
                <X className="size-3.5" />
              </IconButton>
            </span>
          ))}
          <Button size="sm" icon={<FolderPlus className="size-4" />} onClick={() => setPicking('source')}>
            Add folder
          </Button>
        </div>
        <Toggle checked={recursive} onChange={setRecursive} label="Include subfolders" description="Every video in the chosen folders and their subfolders." />
        <p className="text-sm text-slate-500">
          {sources.length === 0 ? 'Choose folders here, or select files and folders in Files and use Smart processing > New queue.' : count.isFetching ? 'Counting videos…' : `${totalRecordings} video(s)${count.data ? `, ${formatBytes(count.data.bytes)} in folders` : ''}`}
        </p>
      </section>

      <section className="card space-y-4 p-5">
        <h2 className="font-semibold">2. What to find</h2>
        <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {list.map((p) => {
            const on = chosen.includes(p.id)
            const missing = p.components.filter((c) => !c.available).length
            if (p.id === 'builtin:custom') return null
            return (
              <button
                key={p.id}
                type="button"
                aria-pressed={on}
                onClick={() => setChosen(on ? chosen.filter((x) => x !== p.id) : [...chosen, p.id])}
                className={cx('rounded-xl border p-3 text-left transition', on ? 'border-brand-500 ring-2 ring-brand-500/30' : 'border-slate-200 hover:border-slate-300 dark:border-slate-700')}
              >
                <div className="flex items-center gap-2 font-medium">
                  {on && <Check className="size-4 text-brand-600" />}
                  {p.name}
                  {!p.builtin && <Badge tone="blue">mine</Badge>}
                </div>
                <p className="mt-1 text-xs text-slate-500">{p.description}</p>
                <p className={cx('mt-2 text-xs', missing ? 'text-amber-600' : 'text-emerald-600')}>
                  {missing ? (
                    <>
                      <AlertTriangle className="inline size-3" /> {missing} of {p.components.length} conditions need models that are not installed
                    </>
                  ) : (
                    `${p.components.length} conditions, all available`
                  )}
                </p>
              </button>
            )
          })}
        </div>
        <details className="rounded-xl border border-slate-200 p-4 dark:border-slate-700" open={customRules > 0}>
          <summary className="cursor-pointer font-medium">Custom conditions</summary>
          <div className="mt-4 space-y-5">
            <DescribeBox onPlan={(p) => setCustom({ ...custom, rules: [...custom.rules, ...p.filter.rules.filter((r) => !r.disabled)] })} />
            <FilterEditor value={custom} onChange={setCustom} components={check.components} />
            {check.error && <p className="text-sm text-rose-600">{check.error}</p>}
            {customRules > 0 && <CapabilityReport components={check.components} compact />}
          </div>
        </details>
      </section>

      <section className="card space-y-5 p-5">
        <h2 className="font-semibold">3. Segments and clips</h2>
        <ContextEditor
          value={ctx}
          onChange={(c) => {
            setCtx(c)
            setCtxTouched(true)
          }}
        />
        <OutputEditor value={output} onChange={setOutput} onPickFolder={() => setPicking('output')} folderLabel={outFolder} />
        <Toggle
          checked={autoApprove}
          onChange={setAutoApprove}
          label="Auto-approve suggestions"
          description="Clips are rendered without review. Originals are still never deleted automatically."
        />
      </section>

      <section className="card grid gap-4 p-5 sm:grid-cols-2">
        <Field label="Queue name">
          <input className="input" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} placeholder="OmeTV, October" />
        </Field>
        <Field label="Notes (optional)">
          <input className="input" value={description} maxLength={2000} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <div className="sm:col-span-2">
          <Button variant="primary" loading={busy} disabled={!canCreate} onClick={create}>
            Create queue
          </Button>
        </div>
      </section>

      <MoveModal
        ids={picking ? [] : null}
        title={picking === 'output' ? 'Folder for the clips' : 'Add a folder of recordings'}
        actionLabel="Choose this folder"
        description={picking === 'output' ? 'Clips are saved here, in folders mirroring the originals.' : 'Its videos are added to the queue.'}
        onClose={() => setPicking(null)}
        onMove={async (target) => {
          const n = await get<{ id: number; name: string; path?: string }>(`/files/${target}`).catch(() => ({ id: target, name: `Folder #${target}` }))
          if (picking === 'output') {
            setOutput({ ...output, destination: 'folder', folder_id: target })
            setOutFolder(n.name || '/')
          } else if (!sources.some((s) => s.id === target)) setSources([...sources, { id: target, name: n.name || '/', dir: true }])
          setPicking(null)
        }}
      />
    </div>
  )
}
