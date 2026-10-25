import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, FolderSearch, HardDrive, ShieldCheck, Trash2, X } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, Modal, PageHeader, PageLoader, Stat, Toggle, useUI } from '../../components/ui'
import { MoveModal } from '../../components/files/MoveModal'
import { errorMessage } from '../../lib/api'
import { formatBytes } from '../../lib/format'
import { smartApi, smartKeys, useQueues, type CleanupResult, type CleanupState } from './api'

/** Reclaim storage: remove originals whose clips are rendered and verified. Never automatic. */
export default function CleanupPage() {
  const [params, setParams] = useSearchParams()
  const queueId = Number(params.get('queue')) || undefined
  const folderId = Number(params.get('folder')) || undefined
  const [showBlocked, setShowBlocked] = useState(false)
  const [sel, setSel] = useState<Set<number>>(new Set())
  const [picking, setPicking] = useState(false)
  const [confirming, setConfirming] = useState(false)
  const [mode, setMode] = useState<'delete' | 'trash'>('delete')
  const [busy, setBusy] = useState(false)
  const [results, setResults] = useState<{ results: CleanupResult[]; removed: number; bytes: number; freed: number } | null>(null)
  const qc = useQueryClient()
  const { toast } = useUI()
  const queues = useQueues()
  const list = useQuery({ queryKey: smartKeys.sub('cleanup', queueId ?? 0, folderId ?? 0), queryFn: () => smartApi.cleanup({ queue_id: queueId, folder_id: folderId }) })

  const items = useMemo(() => (list.data?.items ?? []).filter((c) => showBlocked || c.status !== 'blocked'), [list.data, showBlocked])
  const chosen = items.filter((c) => sel.has(c.item_id) && c.status !== 'blocked')
  const chosenBytes = chosen.reduce((n, c) => n + c.size, 0)
  const noSeg = chosen.filter((c) => c.status === 'no_segments').length
  const totals = list.data?.totals ?? {}

  const setFilter = (k: 'queue' | 'folder', v?: number) => {
    const p = new URLSearchParams(params)
    if (v) p.set(k, String(v))
    else p.delete(k)
    setParams(p, { replace: true })
    setSel(new Set())
  }
  const selectAll = (status: CleanupState['status']) => setSel(new Set(items.filter((c) => c.status === status).map((c) => c.item_id)))

  const run = async () => {
    setBusy(true)
    try {
      const r = await smartApi.runCleanup(
        chosen.map((c) => c.item_id),
        mode,
        noSeg > 0,
      )
      setResults(r)
      setSel(new Set())
      qc.invalidateQueries({ queryKey: smartKeys.all })
      qc.invalidateQueries({ queryKey: ['files'] })
    } catch (e) {
      toast('error', 'Cleanup failed', errorMessage(e))
    } finally {
      setBusy(false)
      setConfirming(false)
    }
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Cleanup"
        icon={<HardDrive className="size-5" />}
        description="Originals can be removed once every approved segment has a rendered clip. Each original is checked again (the original is unchanged, its clips exist and play back) right before it is removed. Nothing is removed automatically."
      />
      <div className="grid gap-4 sm:grid-cols-3">
        <Stat compact label="Ready to remove" value={totals.eligible?.count ?? 0} icon={<CheckCircle2 className="size-4" />} tone="green" sub={`${formatBytes(totals.eligible?.bytes ?? 0)} recoverable (clips use ${formatBytes(totals.eligible?.clip_bytes ?? 0)})`} />
        <Stat compact label="Nothing kept" value={totals.no_segments?.count ?? 0} icon={<X className="size-4" />} tone="amber" sub={`${formatBytes(totals.no_segments?.bytes ?? 0)} · reviewed, no approved segments`} />
        <Stat compact label="Not ready" value={totals.blocked?.count ?? 0} icon={<ShieldCheck className="size-4" />} tone="sky" sub="Waiting for review or rendering" />
      </div>

      <div className="card flex flex-wrap items-center gap-2 p-3">
        <select className="input h-8 w-auto py-0" aria-label="Queue" value={queueId ?? ''} onChange={(e) => setFilter('queue', Number(e.target.value) || undefined)}>
          <option value="">All queues</option>
          {(queues.data?.queues ?? []).map((q) => (
            <option key={q.id} value={q.id}>
              {q.name}
            </option>
          ))}
        </select>
        <Button size="sm" icon={<FolderSearch className="size-4" />} onClick={() => setPicking(true)}>
          {folderId ? `Folder #${folderId} (with subfolders)` : 'Limit to a folder…'}
        </Button>
        {folderId && (
          <Button size="sm" variant="ghost" onClick={() => setFilter('folder')}>
            Any folder
          </Button>
        )}
        <Toggle checked={showBlocked} onChange={setShowBlocked} label="Show originals not ready yet" />
        <div className="flex-1" />
        <Button size="sm" onClick={() => selectAll('eligible')}>
          Select all ready
        </Button>
        <Button size="sm" onClick={() => selectAll('no_segments')}>
          Select all with nothing kept
        </Button>
        <Button size="sm" variant="danger" icon={<Trash2 className="size-4" />} disabled={!chosen.length} onClick={() => setConfirming(true)}>
          Remove {chosen.length || ''} original{chosen.length === 1 ? '' : 's'} {chosen.length ? `(${formatBytes(chosenBytes)})` : ''}
        </Button>
      </div>

      {list.isLoading ? (
        <PageLoader />
      ) : list.error ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState icon={<HardDrive className="size-6" />} title="Nothing to clean up" description="Originals appear here after their review is finished and their clips are rendered." />
      ) : (
        <div className="card overflow-x-auto">
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500 uppercase">
              <tr>
                <th className="w-8 p-2" />
                <th className="p-2">Original</th>
                <th className="p-2">Size</th>
                <th className="p-2">Clips</th>
                <th className="p-2">Status</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100 dark:divide-slate-800">
              {items.map((c) => (
                <tr key={c.item_id} className={c.status === 'blocked' ? 'opacity-60' : ''}>
                  <td className="p-2">
                    <input
                      type="checkbox"
                      aria-label={`Select ${c.name}`}
                      disabled={c.status === 'blocked'}
                      checked={sel.has(c.item_id)}
                      onChange={() => {
                        const s = new Set(sel)
                        if (s.has(c.item_id)) s.delete(c.item_id)
                        else s.add(c.item_id)
                        setSel(s)
                      }}
                    />
                  </td>
                  <td className="max-w-96 p-2">
                    <div className="truncate font-medium">{c.name}</div>
                    <div className="truncate text-xs text-slate-500">{c.path}</div>
                  </td>
                  <td className="p-2 text-xs">{formatBytes(c.size)}</td>
                  <td className="p-2 text-xs">{c.clips ? `${c.clips} (${formatBytes(c.clip_bytes)})` : '-'}</td>
                  <td className="p-2">
                    {c.status === 'eligible' && <Badge tone="green">clips verified</Badge>}
                    {c.status === 'no_segments' && <Badge tone="amber">nothing kept</Badge>}
                    {c.status === 'blocked' && <span className="text-xs text-slate-500">{c.reason}</span>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <Modal
        open={confirming}
        onClose={() => setConfirming(false)}
        title={`Remove ${chosen.length} original${chosen.length === 1 ? '' : 's'}?`}
        size="sm"
        footer={
          <>
            <Button onClick={() => setConfirming(false)}>Cancel</Button>
            <Button variant="danger" loading={busy} onClick={run}>
              {mode === 'delete' ? 'Delete' : 'Move to trash'} ({formatBytes(chosenBytes)})
            </Button>
          </>
        }
      >
        <div className="space-y-3 text-sm text-slate-600 dark:text-slate-300">
          <p>
            {chosen.length} original recording{chosen.length === 1 ? '' : 's'}, {formatBytes(chosenBytes)} in total. Their clips stay.
            {noSeg > 0 && ` ${noSeg} of them had no segments approved, so nothing from them is kept.`}
          </p>
          <label className="flex items-start gap-2">
            <input type="radio" name="mode" checked={mode === 'delete'} onChange={() => setMode('delete')} className="mt-1" />
            <span>
              <b>Delete permanently</b>: the space is freed now.
            </span>
          </label>
          <label className="flex items-start gap-2">
            <input type="radio" name="mode" checked={mode === 'trash'} onChange={() => setMode('trash')} className="mt-1" />
            <span>
              <b>Move to trash</b>: restorable for 7 days; the space is freed when the trash is emptied.
            </span>
          </label>
          <p className="text-xs text-slate-500">Each original is checked again first; any that changed or whose clips cannot be verified is skipped. The desktop app will not upload removed originals again.</p>
        </div>
      </Modal>

      <Modal open={!!results} onClose={() => setResults(null)} title="Cleanup finished" footer={<Button onClick={() => setResults(null)}>Close</Button>}>
        {results && (
          <div className="space-y-3 text-sm">
            <p>
              {results.removed} original(s) removed ({formatBytes(results.bytes)}), {formatBytes(results.freed)} of storage freed now.
            </p>
            {results.results.some((r) => r.status === 'skipped' || r.status === 'failed') && (
              <ul className="max-h-60 space-y-1 overflow-y-auto text-xs">
                {results.results
                  .filter((r) => r.status === 'skipped' || r.status === 'failed')
                  .map((r) => (
                    <li key={r.item_id}>
                      <Badge tone={r.status === 'failed' ? 'red' : 'amber'}>{r.status}</Badge> {r.name}: {r.reason}
                    </li>
                  ))}
              </ul>
            )}
          </div>
        )}
      </Modal>

      <MoveModal ids={picking ? [] : null} title="Limit cleanup to a folder" description="Originals in this folder and its subfolders." actionLabel="Use this folder" onClose={() => setPicking(false)} onMove={(t) => setFilter('folder', t)} />
    </div>
  )
}
