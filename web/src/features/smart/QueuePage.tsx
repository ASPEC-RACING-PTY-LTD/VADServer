import { useState } from 'react'
import { Link, useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { AlertTriangle, Archive, Clapperboard, Copy, Eye, FolderPlus, HardDrive, ListVideo, Pause, Play, RefreshCw, RotateCcw, Trash2, Undo2, XCircle } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, Field, Modal, PageHeader, PageLoader, ProgressBar, Stat, Tabs, Toggle, useUI } from '../../components/ui'
import { MoveModal } from '../../components/files/MoveModal'
import { errorMessage } from '../../lib/api'
import { formatBytes, formatDate, formatDuration, timeAgo } from '../../lib/format'
import { categoryLabel, itemStateLabel, itemStateTone, queueStateTone, smartApi, smartKeys, usePresets, useQueue, useStats, type Filter, type OutputSettings, type Queue, type SegmentContext } from './api'
import { CapabilityReport, ContextEditor, DescribeBox, FilterEditor, OutputEditor, useFilterCheck } from './shared'

type Tab = 'items' | 'detection' | 'settings' | 'errors'

export default function QueuePage() {
  const id = Number(useParams().id)
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const q = useQueue(id)
  const stats = useStats(id)
  const [tab, setTab] = useState<Tab>('items')
  const [adding, setAdding] = useState(false)
  const [busy, setBusy] = useState('')
  if (q.isLoading) return <PageLoader />
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  const queue = q.data!
  const t = queue.tally
  const st = stats.data?.queues[0]
  const refresh = () => qc.invalidateQueries({ queryKey: smartKeys.all })

  const act = async (a: Parameters<typeof smartApi.queueAction>[1]) => {
    setBusy(a)
    try {
      const r = await smartApi.queueAction(id, a)
      refresh()
      if (a === 'duplicate') {
        toast('success', 'Queue duplicated', 'The copy is paused; resume it when ready.')
        navigate(`/smart/queues/${r.id}`)
      }
    } catch (e) {
      toast('error', 'The action failed', errorMessage(e))
    } finally {
      setBusy('')
    }
  }
  const restart = async () => {
    const ok = await confirm({
      title: 'Restart this queue from scratch?',
      message: 'Every recording still kept is analysed again from the beginning. Suggestions and review decisions are cleared. Clips already rendered stay in your files, and the queue statistics are kept.',
      confirmLabel: 'Restart queue',
    })
    if (ok) act('restart')
  }
  const remove = async () => {
    const ok = await confirm({ title: 'Delete this queue?', message: 'Its records, decisions and statistics are removed. Files (originals and clips) are not touched.', confirmLabel: 'Delete queue' })
    if (!ok) return
    await smartApi.deleteQueue(id)
    refresh()
    navigate('/smart/queues')
  }

  const failures = t?.item_states.failed ?? 0
  return (
    <div className="space-y-5">
      <PageHeader
        title={
          <span className="flex items-center gap-2">
            {queue.name} <Badge tone={queueStateTone(queue.state)}>{queue.state}</Badge>
          </span>
        }
        icon={<ListVideo className="size-5" />}
        description={queue.description || `Created ${formatDate(queue.created_at)}`}
        actions={
          <div className="flex flex-wrap gap-2">
            {queue.state === 'active' && (
              <Button icon={<Pause className="size-4" />} loading={busy === 'pause'} onClick={() => act('pause')}>
                Pause
              </Button>
            )}
            {(queue.state === 'paused' || queue.state === 'completed') && (
              <Button icon={<Play className="size-4" />} loading={busy === 'resume'} onClick={() => act('resume')}>
                Resume
              </Button>
            )}
            {failures > 0 && (
              <Button icon={<RotateCcw className="size-4" />} loading={busy === 'retry'} onClick={() => act('retry')}>
                Retry failed
              </Button>
            )}
            {(queue.state === 'active' || queue.state === 'paused') && (
              <Button icon={<XCircle className="size-4" />} loading={busy === 'cancel'} onClick={() => act('cancel')}>
                Cancel
              </Button>
            )}
            {['completed', 'cancelled', 'archived'].includes(queue.state) && (
              <Button icon={<Undo2 className="size-4" />} loading={busy === 'reopen'} onClick={() => act('reopen')}>
                Reopen
              </Button>
            )}
            {queue.state !== 'active' && queue.state !== 'archived' && (
              <Button icon={<Archive className="size-4" />} loading={busy === 'archive'} onClick={() => act('archive')}>
                Archive
              </Button>
            )}
            <Button icon={<RefreshCw className="size-4" />} loading={busy === 'restart'} onClick={restart}>
              Restart
            </Button>
            <Button icon={<Copy className="size-4" />} loading={busy === 'duplicate'} onClick={() => act('duplicate')}>
              Duplicate
            </Button>
            <Button variant="ghost" className="text-rose-600" icon={<Trash2 className="size-4" />} onClick={remove}>
              Delete
            </Button>
          </div>
        }
      />

      <div className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <Stat compact label="Recordings" value={t?.items ?? 0} icon={<ListVideo className="size-4" />} sub={`${formatBytes(t?.original_bytes ?? 0)} · ${t?.analysed ?? 0} analysed`} />
        <Stat compact label="To review" value={t?.item_states.ready ?? 0} icon={<Eye className="size-4" />} tone="amber" sub={t?.item_states.ready ? <Link className="text-brand-600" to={`/smart/review?queue=${id}`}>Review now</Link> : `${t?.segments.approved ?? 0} segments approved`} />
        <Stat compact label="Clips" value={queue.counters.generated_count} icon={<Clapperboard className="size-4" />} sub={formatBytes(queue.counters.generated_bytes)} />
        <Stat
          compact
          label="Storage"
          value={formatBytes(queue.counters.freed_bytes)}
          tone="green"
          icon={<HardDrive className="size-4" />}
          sub={st ? `reclaimed · ${formatBytes(st.estimated_bytes)} more possible` : 'reclaimed'}
        />
      </div>
      <div className="text-xs text-slate-500">
        Processing time {formatDuration((queue.counters.analysis_ms + queue.counters.render_ms) / 1000)} (analysis {formatDuration(queue.counters.analysis_ms / 1000)}, rendering {formatDuration(queue.counters.render_ms / 1000)}) · {queue.counters.failures} failure(s) ·{' '}
        {queue.counters.deleted_count} original(s) removed ({formatBytes(queue.counters.deleted_bytes)})
      </div>

      {(queue.running ?? []).length > 0 && (
        <div className="card space-y-2 p-4">
          {queue.running!.map((j) => (
            <div key={j.item_id} className="text-sm">
              <div className="flex justify-between">
                <span className="truncate">
                  {j.kind === 'render' ? 'Rendering' : 'Analysing'} {j.name}
                </span>
                <span className="text-xs text-slate-500">{j.stage}</span>
              </div>
              <ProgressBar value={j.progress * 100} />
            </div>
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Tabs
          value={tab}
          onChange={setTab}
          tabs={[
            { id: 'items', label: 'Recordings', count: t?.items },
            { id: 'detection', label: 'Detection' },
            { id: 'settings', label: 'Output & settings' },
            { id: 'errors', label: 'Errors', count: queue.counters.failures },
          ]}
        />
        <div className="flex-1" />
        <Button icon={<FolderPlus className="size-4" />} onClick={() => setAdding(true)} disabled={queue.state === 'archived'}>
          Add recordings
        </Button>
      </div>

      {tab === 'items' && <ItemsTab queue={queue} />}
      {tab === 'detection' && <DetectionTab queue={queue} />}
      {tab === 'settings' && <SettingsTab queue={queue} />}
      {tab === 'errors' && <ErrorsTab id={id} />}

      <AddRecordings id={id} open={adding} onClose={() => setAdding(false)} />
    </div>
  )
}

function AddRecordings({ id, open, onClose }: { id: number; open: boolean; onClose: () => void }) {
  const [recursive, setRecursive] = useState(true)
  const [pick, setPick] = useState(false)
  const qc = useQueryClient()
  const { toast } = useUI()
  return (
    <>
      <Modal
        open={open && !pick}
        onClose={onClose}
        title="Add recordings"
        footer={
          <>
            <Button onClick={onClose}>Close</Button>
            <Button variant="primary" onClick={() => setPick(true)}>
              Choose folder…
            </Button>
          </>
        }
      >
        <p className="mb-3 text-sm text-slate-500">Videos already in the queue are skipped. You can also add selected files from Files (Smart processing &gt; Add to queue).</p>
        <Toggle checked={recursive} onChange={setRecursive} label="Include subfolders" />
      </Modal>
      <MoveModal
        ids={pick ? [] : null}
        title="Add a folder of recordings"
        description="Its videos are added to the queue."
        actionLabel="Add this folder"
        onClose={() => setPick(false)}
        onMove={async (target) => {
          try {
            const r = await smartApi.addItems(id, { folder_ids: [target], recursive })
            toast('success', `${r.added} recording(s) added`, r.skipped ? `${r.skipped} already in the queue` : undefined)
            qc.invalidateQueries({ queryKey: smartKeys.all })
            onClose()
          } catch (e) {
            toast('error', 'Nothing was added', errorMessage(e))
          }
        }}
      />
    </>
  )
}

const stateFilters = ['', 'pending', 'analysing', 'ready', 'skipped', 'reviewed', 'rendering', 'done', 'failed', 'gone', 'cancelled'] as const

function ItemsTab({ queue }: { queue: Queue }) {
  const [state, setState] = useState('')
  const [sel, setSel] = useState<Set<number>>(new Set())
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const items = useQuery({ queryKey: smartKeys.sub('items', queue.id, state), queryFn: () => smartApi.items(queue.id, state || undefined), refetchInterval: 5000 })
  const list = items.data?.items ?? []
  const toggle = (i: number) => {
    const s = new Set(sel)
    if (s.has(i)) s.delete(i)
    else s.add(i)
    setSel(s)
  }
  const removeSel = async () => {
    const ok = await confirm({ title: `Remove ${sel.size} recording(s) from the queue?`, message: 'Their suggestions and decisions are removed from this queue. The files themselves are not changed.', confirmLabel: 'Remove', tone: 'primary' })
    if (!ok) return
    try {
      await smartApi.removeItems(queue.id, [...sel])
      setSel(new Set())
      qc.invalidateQueries({ queryKey: smartKeys.all })
    } catch (e) {
      toast('error', 'Not removed', errorMessage(e))
    }
  }
  const approveAll = async () => {
    const r = await smartApi.bulkSegments({ queue_id: queue.id, state: 'approved', finish: true })
    toast('success', `${r.updated} suggestion(s) approved`, `${r.reviewed} recording(s) marked reviewed; their clips will be rendered.`)
    qc.invalidateQueries({ queryKey: smartKeys.all })
  }
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-center gap-2">
        <select className="input h-8 w-auto py-0" aria-label="State" value={state} onChange={(e) => setState(e.target.value)}>
          {stateFilters.map((s) => (
            <option key={s} value={s}>
              {s ? itemStateLabel[s] : 'All states'}
            </option>
          ))}
        </select>
        {sel.size > 0 && (
          <Button size="sm" onClick={removeSel}>
            Remove {sel.size} from queue
          </Button>
        )}
        <div className="flex-1" />
        {(queue.tally?.item_states.ready ?? 0) > 0 && (
          <Button size="sm" onClick={approveAll}>
            Approve all suggestions
          </Button>
        )}
      </div>
      {items.isLoading ? (
        <PageLoader />
      ) : list.length === 0 ? (
        <EmptyState icon={<ListVideo className="size-6" />} title="No recordings" />
      ) : (
        <div className="card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500 uppercase">
              <tr>
                <th className="w-8 p-2">
                  <input type="checkbox" aria-label="Select all" checked={sel.size === list.length} onChange={(e) => setSel(e.target.checked ? new Set(list.map((i) => i.id)) : new Set())} />
                </th>
                <th className="p-2">Recording</th>
                <th className="p-2">State</th>
                <th className="p-2">Segments</th>
                <th className="p-2">Length</th>
                <th className="p-2">Original</th>
                <th className="p-2" />
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {list.map((i) => (
                <tr key={i.id}>
                  <td className="p-2">
                    <input type="checkbox" aria-label={`Select ${i.name}`} checked={sel.has(i.id)} onChange={() => toggle(i.id)} />
                  </td>
                  <td className="max-w-80 p-2">
                    <div className="truncate font-medium" title={i.path}>
                      {i.name}
                    </div>
                    <div className="truncate text-xs text-slate-500">{i.path}</div>
                    {i.warnings.length > 0 && (
                      <div className="truncate text-xs text-amber-600" title={i.warnings.join('\n')}>
                        <AlertTriangle className="inline size-3" /> {i.warnings[0]}
                        {i.warnings.length > 1 ? ` (+${i.warnings.length - 1})` : ''}
                      </div>
                    )}
                  </td>
                  <td className="p-2">
                    <Badge tone={itemStateTone(i.state)}>{itemStateLabel[i.state]}</Badge>
                    {(i.state === 'analysing' || i.state === 'rendering') && (
                      <div className="mt-1 w-28">
                        <ProgressBar value={i.progress * 100} />
                        <div className="truncate text-[11px] text-slate-500">{i.stage}</div>
                      </div>
                    )}
                    {i.error && <div className="max-w-60 truncate text-xs text-rose-600" title={i.error}>{i.error}</div>}
                  </td>
                  <td className="p-2 text-xs text-slate-600 dark:text-slate-300">
                    {i.segments ? `${i.segments.approved} kept · ${i.segments.suggested} open · ${i.segments.rejected} rejected` : '-'}
                  </td>
                  <td className="p-2 text-xs">{i.duration ? formatDuration(i.duration) : '-'}</td>
                  <td className="p-2 text-xs">
                    {formatBytes(i.size)}
                    {i.original_state !== 'kept' && (
                      <Badge tone="slate" className="ml-1">
                        {i.original_state}
                      </Badge>
                    )}
                  </td>
                  <td className="p-2 text-right">
                    {['ready', 'skipped', 'reviewed', 'done', 'failed'].includes(i.state) && (
                      <Link className="text-sm text-brand-600" to={`/smart/review/${i.id}`}>
                        {i.state === 'ready' || i.state === 'skipped' ? 'Review' : 'Open'}
                      </Link>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          {items.data && items.data.total > list.length && <p className="p-2 text-xs text-slate-500">Showing {list.length} of {items.data.total}</p>}
        </div>
      )}
    </div>
  )
}

function DetectionTab({ queue }: { queue: Queue }) {
  const [editing, setEditing] = useState(false)
  const presets = usePresets()
  const names = queue.presets.map((p) => presets.data?.presets.find((x) => x.id === p)?.name ?? p)
  return (
    <div className="space-y-4">
      <div className="card space-y-3 p-5">
        <div className="flex items-center gap-2">
          <h3 className="font-semibold">Conditions</h3>
          <div className="flex-1" />
          <Button size="sm" onClick={() => setEditing(true)}>
            Change detection
          </Button>
        </div>
        <p className="text-sm text-slate-500">Presets: {names.length ? names.join(', ') : 'none (custom conditions only)'}</p>
        <ul className="space-y-1 text-sm">
          {queue.filter.rules.map((r) => (
            <li key={r.id} className={r.disabled ? 'text-slate-400 line-through' : ''}>
              <Badge tone="slate">{categoryLabel[r.category] ?? r.category}</Badge> {r.name}
            </li>
          ))}
        </ul>
        <CapabilityReport components={queue.components ?? []} />
      </div>
      <div className="card p-5 text-sm text-slate-600 dark:text-slate-300">
        Threshold {Math.round(queue.filter.context.threshold * 100)}% · {queue.filter.context.pre}s before, {queue.filter.context.post}s after · longest {queue.filter.context.max_segment}s ·{' '}
        {queue.filter.context.merge ? `merge within ${queue.filter.context.merge_gap}s` : 'no merging'} · {queue.filter.context.trim_silence ? 'silent ends trimmed' : 'silence kept'} · duplicates over {Math.round(queue.filter.context.dedupe * 100)}% overlap dropped
      </div>
      {editing && <EditDetection queue={queue} onClose={() => setEditing(false)} />}
    </div>
  )
}

function EditDetection({ queue, onClose }: { queue: Queue; onClose: () => void }) {
  const presets = usePresets()
  const qc = useQueryClient()
  const { toast } = useUI()
  // Custom rules are the queue's rules not coming from presets (ids "c." or unprefixed).
  const [chosen, setChosen] = useState<string[]>(queue.presets)
  const [custom, setCustom] = useState<Filter>({ rules: queue.filter.rules.filter((r) => !/^p\d+\./.test(r.id)).map((r) => ({ ...r, id: r.id.replace(/^c\./, '') })), context: queue.filter.context })
  const [ctx, setCtx] = useState<SegmentContext>(queue.filter.context)
  const [busy, setBusy] = useState(false)
  const check = useFilterCheck()
  const save = async () => {
    setBusy(true)
    try {
      await smartApi.setQueueFilter(queue.id, { preset_ids: chosen, filter: custom.rules.length ? { rules: custom.rules, context: ctx } : undefined, context: ctx })
      toast('success', 'Detection updated', 'Recordings waiting for review are evaluated again from their stored measurements; reviewed ones keep their decisions.')
      qc.invalidateQueries({ queryKey: smartKeys.all })
      onClose()
    } catch (e) {
      toast('error', 'Not saved', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      size="xl"
      onClose={onClose}
      title="Change detection"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!chosen.length && !custom.rules.length} onClick={save}>
            Save and re-evaluate
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <div className="flex flex-wrap gap-2">
          {(presets.data?.presets ?? [])
            .filter((p) => p.id !== 'builtin:custom')
            .map((p) => {
              const on = chosen.includes(p.id)
              return (
                <button key={p.id} type="button" aria-pressed={on} onClick={() => setChosen(on ? chosen.filter((x) => x !== p.id) : [...chosen, p.id])} className={`rounded-full border px-3 py-1 text-sm ${on ? 'border-brand-500 bg-brand-50 dark:bg-brand-500/15' : 'border-slate-300 dark:border-slate-600'}`}>
                  {p.name}
                </button>
              )
            })}
        </div>
        <DescribeBox onPlan={(p) => setCustom({ ...custom, rules: [...custom.rules, ...p.filter.rules.filter((r) => !r.disabled)] })} />
        <FilterEditor
          value={custom}
          onChange={(f) => {
            setCustom(f)
            if (f.rules.length) check.check({ ...f, context: ctx })
          }}
          components={check.components}
        />
        <ContextEditor value={ctx} onChange={setCtx} />
      </div>
    </Modal>
  )
}

function SettingsTab({ queue }: { queue: Queue }) {
  const [name, setName] = useState(queue.name)
  const [description, setDescription] = useState(queue.description)
  const [output, setOutput] = useState<OutputSettings>(queue.output)
  const [auto, setAuto] = useState(queue.auto_approve)
  const [pick, setPick] = useState(false)
  const [busy, setBusy] = useState(false)
  const qc = useQueryClient()
  const { toast } = useUI()
  const save = async () => {
    setBusy(true)
    try {
      await smartApi.updateQueue(queue.id, { name, description, output, auto_approve: auto })
      toast('success', 'Saved', 'Clips rendered from now on use these settings.')
      qc.invalidateQueries({ queryKey: smartKeys.all })
    } catch (e) {
      toast('error', 'Not saved', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="card space-y-5 p-5">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Name">
          <input className="input" value={name} maxLength={120} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Notes">
          <input className="input" value={description} maxLength={2000} onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </div>
      <OutputEditor value={output} onChange={setOutput} onPickFolder={() => setPick(true)} />
      <Toggle checked={auto} onChange={setAuto} label="Auto-approve suggestions" description="New suggestions are approved and rendered without review. Originals are never deleted automatically." />
      <Button variant="primary" loading={busy} onClick={save}>
        Save
      </Button>
      <MoveModal ids={pick ? [] : null} title="Folder for the clips" description="Clips are saved here, in folders mirroring the originals." actionLabel="Choose this folder" onClose={() => setPick(false)} onMove={(target) => setOutput({ ...output, destination: 'folder', folder_id: target })} />
    </div>
  )
}

function ErrorsTab({ id }: { id: number }) {
  const errs = useQuery({ queryKey: smartKeys.sub('errors', id), queryFn: () => smartApi.errors(id) })
  if (errs.isLoading) return <PageLoader />
  const list = errs.data?.errors ?? []
  if (!list.length) return <EmptyState icon={<AlertTriangle className="size-6" />} title="No errors" />
  return (
    <div className="card divide-y divide-slate-100 dark:divide-slate-800">
      {list.map((e) => (
        <div key={e.id} className="flex gap-3 p-3 text-sm">
          <Badge tone="red">{e.stage}</Badge>
          <div className="min-w-0 flex-1 break-words">{e.message}</div>
          <span className="shrink-0 text-xs text-slate-500" title={formatDate(e.at)}>
            {timeAgo(e.at)}
          </span>
        </div>
      ))}
    </div>
  )
}
