import { useRef, useState, type DragEvent } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { CheckCircle2, ChevronDown, FolderUp, Pause, Play, RotateCcw, Upload, X, XCircle } from 'lucide-react'
import { del, errorMessage, get } from '../../lib/api'
import { formatBytes, formatDuration, formatSpeed, timeAgo } from '../../lib/format'
import { filesFromDrop, useUploads, type UploadItem } from '../../lib/uploads'
import { Badge, Button, cx, IconButton, Modal, ProgressBar, useUI } from '../ui'
import { FileIcon } from '../FileIcon'
import { NODES_MIME } from './types'
import type { PickedFile } from './UploadControls'

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

/**
 * Files > Uploads: drop files or folders, or pick them with the browser's file window, into the
 * open folder. Uploads keep running in the background after the dialog is closed; it also lists
 * this browser's uploads and the resumable sessions on the server.
 */
export function UploadsModal({ open, onClose, folderPath, canUpload, onFiles }: { open: boolean; onClose: () => void; folderPath: string; canUpload: boolean; onFiles: (files: PickedFile[]) => void }) {
  const u = useUploads()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const [over, setOver] = useState(false)
  const [showServer, setShowServer] = useState(false)
  const fileInput = useRef<HTMLInputElement>(null)
  const folderInput = useRef<HTMLInputElement>(null)
  const server = useQuery({ queryKey: ['uploads-server'], queryFn: () => get('/uploads'), refetchInterval: 5000, enabled: open })
  const active = u.items.filter((i) => ['queued', 'uploading', 'paused', 'finalizing'].includes(i.status))
  const finished = u.items.filter((i) => ['done', 'failed', 'cancelled'].includes(i.status))
  const totalSpeed = u.items.reduce((a, i) => a + (i.status === 'uploading' ? i.speed : 0), 0)
  const remaining = active.reduce((a, i) => a + (i.file.size - i.sent), 0)
  const browserSessions = new Set(u.items.map((i) => i.sessionId).filter(Boolean))
  const serverOnly = (server.data?.uploads ?? []).filter((s: any) => !browserSessions.has(s.id))
  const where = folderPath === '/' ? 'My files' : folderPath

  const cancelServer = async (id: string) => {
    if (!(await confirm({ title: 'Cancel this upload?', message: 'The partially uploaded data on the server will be discarded. A device will start again from the beginning next time.', confirmLabel: 'Cancel upload' }))) return
    try {
      await del(`/uploads/${id}`)
      qc.invalidateQueries({ queryKey: ['uploads-server'] })
    } catch (e) {
      toast('error', 'Could not cancel', errorMessage(e))
    }
  }
  const onDrop = async (e: DragEvent) => {
    e.preventDefault()
    e.stopPropagation()
    setOver(false)
    if (!canUpload || e.dataTransfer.types.includes(NODES_MIME)) return
    const files = await filesFromDrop(e.dataTransfer)
    if (files.length) onFiles(files)
  }

  return (
    <Modal open={open} onClose={onClose} size="lg" title="Uploads" description={canUpload ? `Into ${where}` : 'Open a folder to upload into it.'} footer={finished.length > 0 ? <Button onClick={u.clearFinished}>Clear finished</Button> : undefined}>
      <div className="space-y-5">
        {canUpload && (
          <div
            onDragOver={(e) => {
              if (e.dataTransfer.types.includes('Files')) {
                e.preventDefault()
                e.stopPropagation()
                setOver(true)
              }
            }}
            onDragLeave={() => setOver(false)}
            onDrop={onDrop}
            className={cx('flex flex-col items-center gap-3 rounded-2xl border-2 border-dashed px-4 py-8 text-center transition', over ? 'border-brand-500 bg-brand-50 dark:bg-brand-500/10' : 'border-slate-300 dark:border-slate-700')}
          >
            <Upload className="size-8 text-brand-500" />
            <div>
              <div className="font-medium">Drag files or folders here</div>
              <div className="text-sm text-slate-500">or choose them from your computer</div>
            </div>
            <div className="flex flex-wrap justify-center gap-2">
              <Button variant="primary" icon={<Upload className="size-4" />} onClick={() => fileInput.current?.click()}>
                Choose files
              </Button>
              <Button icon={<FolderUp className="size-4" />} onClick={() => folderInput.current?.click()}>
                Choose folder
              </Button>
            </div>
            <input
              ref={fileInput}
              type="file"
              multiple
              hidden
              onChange={(e) => {
                onFiles(Array.from(e.target.files ?? []).map((f) => ({ file: f, rel: f.name })))
                e.target.value = ''
              }}
            />
            <input
              ref={folderInput}
              type="file"
              hidden
              {...({ webkitdirectory: '', directory: '' } as any)}
              onChange={(e) => {
                onFiles(Array.from(e.target.files ?? []).map((f) => ({ file: f, rel: (f as any).webkitRelativePath || f.name })))
                e.target.value = ''
              }}
            />
          </div>
        )}

        {u.items.length > 0 && (
          <div>
            <div className="mb-2 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm">
              <span className="font-semibold">This browser</span>
              <span className="text-slate-500">{active.length} in progress</span>
              {totalSpeed > 0 && (
                <span className="text-slate-500">
                  {formatSpeed(totalSpeed)} · {formatDuration(remaining / totalSpeed)} left
                </span>
              )}
            </div>
            <div className="scroll-thin max-h-80 overflow-y-auto rounded-xl border border-slate-200 dark:border-slate-800">
              {[...active, ...finished].map((it) => (
                <Row key={it.id} it={it} />
              ))}
            </div>
          </div>
        )}

        <div>
          <button type="button" onClick={() => setShowServer((v) => !v)} className="flex w-full items-center gap-2 text-left text-sm font-semibold">
            <ChevronDown className={cx('size-4 transition', !showServer && '-rotate-90')} />
            Resumable sessions on the server
            <span className="font-normal text-slate-500">({serverOnly.length})</span>
          </button>
          {showServer &&
            (serverOnly.length === 0 ? (
              <div className="mt-2 flex items-center gap-2 text-sm text-slate-500">
                <CheckCircle2 className="size-4 text-emerald-500" /> No other open upload sessions.
              </div>
            ) : (
              <div className="mt-2 rounded-xl border border-slate-200 dark:border-slate-800">
                {serverOnly.map((s: any) => (
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
                ))}
              </div>
            ))}
        </div>
      </div>
    </Modal>
  )
}
