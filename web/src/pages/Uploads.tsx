import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, CloudUpload, Pause, Play, RotateCcw, X, XCircle } from 'lucide-react'
import { del, errorMessage, get } from '../lib/api'
import { formatBytes, formatDuration, formatSpeed, timeAgo } from '../lib/format'
import { useUploads, type UploadItem } from '../lib/uploads'
import { Badge, Button, EmptyState, IconButton, PageHeader, ProgressBar, useUI } from '../components/ui'
import { FileIcon } from '../components/FileIcon'

const statusTone: Record<string, any> = { queued: 'slate', uploading: 'blue', paused: 'amber', finalizing: 'blue', done: 'green', failed: 'red', cancelled: 'slate' }

function Row({ it }: { it: UploadItem }) {
  const u = useUploads()
  const pct = it.file.size ? (it.sent / it.file.size) * 100 : it.status === 'done' ? 100 : 0
  const eta = it.speed > 0 ? (it.file.size - it.sent) / it.speed : NaN
  return (
    <div className="flex items-center gap-4 border-b border-slate-100 px-4 py-3 last:border-0 dark:border-slate-800">
      <FileIcon mime={it.file.type} isDir={false} size="sm" />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate text-sm font-medium">{it.file.name}</span>
          <Badge tone={statusTone[it.status]}>{it.status === 'done' && it.result ? it.result : it.status}</Badge>
        </div>
        <div className="truncate text-xs text-slate-500">{it.path}</div>
        <div className="mt-2 flex items-center gap-3">
          <ProgressBar value={pct} tone={it.status === 'failed' ? 'red' : it.status === 'done' ? 'green' : 'brand'} indeterminate={it.status === 'finalizing'} />
          <span className="w-28 shrink-0 text-right text-xs text-slate-500 tabular-nums">
            {formatBytes(it.sent)} / {formatBytes(it.file.size)}
          </span>
        </div>
        {it.status === 'uploading' && (
          <div className="mt-1 text-xs text-slate-500">
            {formatSpeed(it.speed)} · {isFinite(eta) ? `${formatDuration(eta)} remaining` : 'estimating…'}
          </div>
        )}
        {it.error && <div className="mt-1 text-xs text-rose-600">{it.error}</div>}
      </div>
      <div className="flex shrink-0 gap-1">
        {it.status === 'uploading' && (
          <IconButton label="Pause" onClick={() => u.pause(it.id)}>
            <Pause className="size-4" />
          </IconButton>
        )}
        {it.status === 'paused' && (
          <IconButton label="Resume" onClick={() => u.resume(it.id)}>
            <Play className="size-4" />
          </IconButton>
        )}
        {it.status === 'failed' && (
          <IconButton label="Retry" onClick={() => u.retry(it.id)}>
            <RotateCcw className="size-4" />
          </IconButton>
        )}
        {['queued', 'uploading', 'paused', 'failed'].includes(it.status) && (
          <IconButton label="Cancel" onClick={() => u.cancel(it.id)}>
            <X className="size-4" />
          </IconButton>
        )}
      </div>
    </div>
  )
}

export default function UploadsPage() {
  const u = useUploads()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const server = useQuery({ queryKey: ['uploads-server'], queryFn: () => get('/uploads'), refetchInterval: 5000 })
  const active = u.items.filter((i) => ['queued', 'uploading', 'paused', 'finalizing'].includes(i.status))
  const finished = u.items.filter((i) => ['done', 'failed', 'cancelled'].includes(i.status))
  const totalSpeed = u.items.reduce((a, i) => a + (i.status === 'uploading' ? i.speed : 0), 0)
  const remaining = active.reduce((a, i) => a + (i.file.size - i.sent), 0)
  const browserSessions = new Set(u.items.map((i) => i.sessionId).filter(Boolean))
  const serverOnly = (server.data?.uploads ?? []).filter((s: any) => !browserSessions.has(s.id))

  const cancelServer = async (id: string) => {
    if (!(await confirm({ title: 'Cancel this upload?', message: 'The partially uploaded data on the server will be discarded. A device will start again from the beginning next time.', confirmLabel: 'Cancel upload' }))) return
    try {
      await del(`/uploads/${id}`)
      qc.invalidateQueries({ queryKey: ['uploads-server'] })
    } catch (e) {
      toast('error', 'Could not cancel', errorMessage(e))
    }
  }

  return (
    <div>
      <PageHeader title="Upload manager" description="Browser uploads run in the background while you use VaultDock. Device uploads are managed by the desktop app." icon={<CloudUpload className="size-5" />} actions={finished.length > 0 && <Button onClick={u.clearFinished}>Clear finished</Button>} />
      <div className="mb-6 grid gap-4 sm:grid-cols-3">
        <div className="card p-4">
          <div className="text-xs font-medium text-slate-500 uppercase">In progress</div>
          <div className="mt-1 text-xl font-semibold">{active.length}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs font-medium text-slate-500 uppercase">Speed</div>
          <div className="mt-1 text-xl font-semibold">{formatSpeed(totalSpeed)}</div>
        </div>
        <div className="card p-4">
          <div className="text-xs font-medium text-slate-500 uppercase">Time remaining</div>
          <div className="mt-1 text-xl font-semibold">{totalSpeed > 0 ? formatDuration(remaining / totalSpeed) : '-'}</div>
        </div>
      </div>

      <div className="card mb-6 overflow-hidden">
        <div className="border-b border-slate-200 px-4 py-3 text-sm font-semibold dark:border-slate-800">This browser</div>
        {u.items.length === 0 ? <EmptyState icon={<CloudUpload className="size-6" />} title="No uploads in this session" description="Upload from the Files page by dragging files in or using the upload buttons." /> : [...active, ...finished].map((it) => <Row key={it.id} it={it} />)}
      </div>

      <div className="card overflow-hidden">
        <div className="border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <div className="text-sm font-semibold">Resumable sessions on the server</div>
          <div className="text-xs text-slate-500">Uploads started by your devices or other browser sessions that can continue where they left off.</div>
        </div>
        {serverOnly.length === 0 ? (
          <div className="flex items-center gap-2 px-4 py-6 text-sm text-slate-500">
            <CheckCircle2 className="size-4 text-emerald-500" /> No other open upload sessions.
          </div>
        ) : (
          serverOnly.map((s: any) => (
            <div key={s.id} className="flex items-center gap-4 border-b border-slate-100 px-4 py-3 last:border-0 dark:border-slate-800">
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium">{s.path}</div>
                <div className="mt-2 flex items-center gap-3">
                  <ProgressBar value={s.size ? (s.bytes_received / s.size) * 100 : 0} />
                  <span className="w-36 shrink-0 text-right text-xs text-slate-500">
                    {formatBytes(s.bytes_received)} / {formatBytes(s.size)}
                  </span>
                </div>
                <div className="mt-1 text-xs text-slate-500">Expires {timeAgo(s.expires_at)}</div>
              </div>
              <IconButton label="Cancel upload" onClick={() => cancelServer(s.id)}>
                <XCircle className="size-4" />
              </IconButton>
            </div>
          ))
        )}
      </div>
    </div>
  )
}
