import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { ChevronRight, Download, File as FileIcon, Folder, History } from 'lucide-react'
import { Badge, Button, EmptyState, ErrorState, Modal, PageHeader, PageLoader } from '../../components/ui'
import { errorMessage } from '../../lib/api'
import { formatBytes } from '../../lib/format'
import { cancelRestore, downloadZip, executeRestore, listRestores, listSnapshots, listTree, planRestore, type PlanResponse, type RestorePlan, type Snapshot } from './api'

const fmtTime = (ms: number) => new Date(ms).toLocaleString()
const isDir = (t: string) => t === 'dir' || t === 'd'

/** Snapshots: browse, download as zip, or restore to a device (api-v1 rev 1.1c). */
export default function RestorePage() {
  const snaps = useQuery({ queryKey: ['restore', 'snapshots'], queryFn: listSnapshots })
  const [snap, setSnap] = useState<Snapshot | null>(null)
  return (
    <div className="flex flex-col gap-6">
      <PageHeader title="Restore" description="Browse backup snapshots, download files, or restore them to a device." icon={<History className="size-5" />} />
      <RestoreList />
      {snaps.error ? (
        <ErrorState error={snaps.error} onRetry={() => void snaps.refetch()} />
      ) : snaps.isPending ? (
        <PageLoader />
      ) : snap ? (
        <Browser snap={snap} onBack={() => setSnap(null)} />
      ) : snaps.data.length === 0 ? (
        <EmptyState icon={<History className="size-6" />} title="No snapshots" description="Snapshots appear here after a backup completes." />
      ) : (
        <div className="overflow-x-auto rounded-lg border border-slate-200 dark:border-slate-700">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-left dark:bg-slate-800">
              <tr>
                <th className="p-2">Time</th>
                <th className="p-2">Device</th>
                <th className="p-2">User</th>
                <th className="p-2">Files</th>
                <th className="p-2">Size</th>
              </tr>
            </thead>
            <tbody>
              {[...snaps.data]
                .sort((a, b) => b.committed_at - a.committed_at)
                .map((s) => (
                  <tr key={s.id} className="cursor-pointer border-t border-slate-100 hover:bg-slate-50 dark:border-slate-800 dark:hover:bg-slate-800" onClick={() => setSnap(s)}>
                    <td className="p-2">{fmtTime(s.committed_at)}</td>
                    <td className="p-2">{s.hostname}</td>
                    <td className="p-2">{s.os_user}</td>
                    <td className="p-2">{s.stats.file_count ?? 0}</td>
                    <td className="p-2">{formatBytes(s.stats.logical_bytes)}</td>
                  </tr>
                ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function Browser({ snap, onBack }: { snap: Snapshot; onBack: () => void }) {
  const [path, setPath] = useState('')
  const [picked, setPicked] = useState<string[]>([])
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const [restoring, setRestoring] = useState(false)
  const tree = useQuery({ queryKey: ['restore', 'tree', snap.id, path], queryFn: () => listTree(snap.id, path) })
  const toggle = (p: string) => setPicked((x) => (x.includes(p) ? x.filter((y) => y !== p) : [...x, p]))
  const crumbs = path ? path.split('/') : []

  const download = async () => {
    setBusy(true)
    setErr(null)
    try {
      window.location.href = await downloadZip(snap.id, picked)
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-1 text-sm">
        <Button size="sm" onClick={onBack}>
          All snapshots
        </Button>
        <button className="ml-2 hover:underline" onClick={() => setPath('')}>
          {snap.hostname} · {fmtTime(snap.committed_at)}
        </button>
        {crumbs.map((c, i) => (
          <span key={i} className="flex items-center gap-1">
            <ChevronRight className="size-3" />
            <button className="hover:underline" onClick={() => setPath(crumbs.slice(0, i + 1).join('/'))}>
              {c}
            </button>
          </span>
        ))}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-sm text-slate-500">{picked.length} selected</span>
        <Button size="sm" icon={<Download className="size-4" />} loading={busy} disabled={picked.length === 0 || picked.length > 100} onClick={() => void download()}>
          Download zip
        </Button>
        <Button size="sm" variant="primary" disabled={picked.length === 0} onClick={() => setRestoring(true)}>
          Restore to device…
        </Button>
        {err && <span className="text-sm text-red-600">{err}</span>}
      </div>
      {tree.error ? (
        <ErrorState error={tree.error} onRetry={() => void tree.refetch()} />
      ) : tree.isPending ? (
        <PageLoader />
      ) : (
        <ul className="divide-y divide-slate-100 rounded-lg border border-slate-200 text-sm dark:divide-slate-800 dark:border-slate-700">
          {tree.data.items.map((e) => (
            <li key={e.path} className="flex items-center gap-2 p-2">
              <input type="checkbox" checked={picked.includes(e.path)} onChange={() => toggle(e.path)} aria-label={`Select ${e.name}`} />
              {isDir(e.type) ? (
                <button className="flex items-center gap-2 hover:underline" onClick={() => setPath(e.path)}>
                  <Folder className="size-4" /> {e.name}
                </button>
              ) : (
                <a className="flex items-center gap-2 hover:underline" href={`/api/v1/snapshots/${snap.id}/content?path=${encodeURIComponent(e.path)}`}>
                  <FileIcon className="size-4" /> {e.name}
                </a>
              )}
              <span className="ml-auto text-slate-500">{isDir(e.type) ? '' : formatBytes(e.size)}</span>
            </li>
          ))}
        </ul>
      )}
      {restoring && <RestoreWizard snap={snap} paths={picked} onClose={() => setRestoring(false)} />}
    </div>
  )
}

function RestoreWizard({ snap, paths, onClose }: { snap: Snapshot; paths: string[]; onClose: () => void }) {
  const [policy, setPolicy] = useState('rename')
  const [plan, setPlan] = useState<PlanResponse<RestorePlan> | null>(null)
  const [done, setDone] = useState(false)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  const run = async (fn: () => Promise<void>) => {
    setBusy(true)
    setErr(null)
    try {
      await fn()
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const preview = () =>
    run(async () => {
      setPlan(await planRestore(snap.id, snap.device_id, paths, policy))
    })
  const execute = (token: string) =>
    run(async () => {
      await executeRestore(token)
      setDone(true)
    })
  return (
    <Modal
      open
      onClose={onClose}
      title="Restore to device"
      description={`${paths.length} item(s) to ${snap.hostname}, into the original user's profile.`}
      footer={
        done ? (
          <Button onClick={onClose}>Close</Button>
        ) : plan ? (
          <Button variant="primary" loading={busy} onClick={() => void execute(plan.plan_token)}>
            Request restore
          </Button>
        ) : (
          <Button variant="primary" loading={busy} onClick={() => void preview()}>
            Preview
          </Button>
        )
      }
    >
      <div className="flex flex-col gap-3 text-sm">
        {!plan && (
          <label className="flex flex-col gap-1">
            If a file already exists
            <select className="rounded border border-slate-300 p-1 dark:border-slate-600 dark:bg-slate-800" value={policy} onChange={(e) => setPolicy(e.target.value)}>
              <option value="rename">Keep both (restore as “(restored)”)</option>
              <option value="skip">Skip it</option>
              <option value="overwrite">Replace it (the old file is kept as “(replaced …)”)</option>
            </select>
          </label>
        )}
        {plan && (
          <>
            <p>
              {plan.plan.summary.files} files, {formatBytes(plan.plan.summary.bytes)}, {plan.plan.summary.conflicts} conflicts.
            </p>
            {(plan.plan.guard?.length ?? 0) > 0 && <p className="text-amber-700">{plan.plan.guard.length} path(s) blocked by system-file protection will be skipped.</p>}
            {(plan.plan.warnings ?? []).map((w) => (
              <p key={w.code} className="text-amber-700">
                {w.message}
              </p>
            ))}
          </>
        )}
        {done && <p className="text-green-700">Restore requested. It starts on the device after the owner approves it.</p>}
        {err && <p className="text-red-600">{err}</p>}
      </div>
    </Modal>
  )
}

const tone = (s: string) => (s === 'completed' ? 'green' : s === 'failed' ? 'red' : s === 'partial' || s === 'cancelled' ? 'amber' : 'blue')
const active = (s: string) => s === 'running' || s === 'queued' || s === 'awaiting_approval'

function RestoreList() {
  const q = useQuery({
    queryKey: ['restore', 'list'],
    queryFn: listRestores,
    refetchInterval: (x) => (x.state.data?.items.some((r) => active(r.status)) ? 3000 : false),
  })
  if (!q.data || q.data.items.length === 0) return null
  return (
    <div className="flex flex-col gap-1 text-sm">
      <h2 className="font-medium">Recent restores</h2>
      {q.data.items.slice(0, 10).map((r) => (
        <div key={r.id} className="flex flex-wrap items-center gap-2">
          <Badge tone={tone(r.status)}>{r.status}</Badge>
          <span>
            #{r.id} · {r.files_done}/{r.files_total} files · {formatBytes(r.bytes_done)} / {formatBytes(r.bytes_total)}
          </span>
          {r.error && <span className="text-red-600">{r.error}</span>}
          {active(r.status) && (
            <Button size="sm" onClick={() => void cancelRestore(r.id).then(() => q.refetch())}>
              Cancel
            </Button>
          )}
        </div>
      ))}
    </div>
  )
}
