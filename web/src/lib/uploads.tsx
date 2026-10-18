import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, api, csrfHeader, errorMessage } from './api'

// Browser upload manager: chunked, resumable within the session, with
// retries and limited parallelism. Uploads continue while navigating.

export type UploadStatus = 'queued' | 'uploading' | 'paused' | 'finalizing' | 'done' | 'failed' | 'cancelled'

export type UploadItem = {
  id: number
  file: File
  path: string // destination path including file name
  status: UploadStatus
  sent: number
  speed: number // bytes per second (EMA)
  error?: string
  result?: string
  sessionId?: string
  startedAt?: number
}

type Ctx = {
  items: UploadItem[]
  enqueue: (files: { file: File; path: string }[]) => void
  pause: (id: number) => void
  resume: (id: number) => void
  cancel: (id: number) => void
  retry: (id: number) => void
  clearFinished: () => void
}

const UploadCtx = createContext<Ctx | null>(null)
export const useUploads = () => {
  const c = useContext(UploadCtx)
  if (!c) throw new Error('UploadProvider missing')
  return c
}

const MAX_FILES = 3
const CHUNK_PARALLEL = 3

export function UploadProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<UploadItem[]>([])
  const itemsRef = useRef<UploadItem[]>([])
  const controllers = useRef(new Map<number, AbortController>())
  const nextId = useRef(1)
  const qc = useQueryClient()

  const update = useCallback((id: number, patch: Partial<UploadItem> | ((it: UploadItem) => Partial<UploadItem>)) => {
    itemsRef.current = itemsRef.current.map((it) => (it.id === id ? { ...it, ...(typeof patch === 'function' ? patch(it) : patch) } : it))
    setItems(itemsRef.current)
  }, [])

  const enqueue = useCallback((files: { file: File; path: string }[]) => {
    const add = files.map((f) => ({ id: nextId.current++, file: f.file, path: f.path, status: 'queued' as UploadStatus, sent: 0, speed: 0 }))
    itemsRef.current = [...itemsRef.current, ...add]
    setItems(itemsRef.current)
  }, [])

  const runUpload = useCallback(
    async (item: UploadItem) => {
      const ctrl = new AbortController()
      controllers.current.set(item.id, ctrl)
      update(item.id, { status: 'uploading', error: undefined, startedAt: Date.now() })
      try {
        let sessionId = item.sessionId
        let session: any
        if (sessionId) {
          session = (await api('GET', `/uploads/${sessionId}`, undefined, { signal: ctrl.signal })).upload
        } else {
          const res = await api('POST', '/uploads', { path: item.path, size: item.file.size, modified_at: item.file.lastModified, on_conflict: 'rename' }, { signal: ctrl.signal })
          if (res.status === 'complete') {
            update(item.id, { status: 'done', sent: item.file.size, result: res.result })
            return
          }
          session = res.upload
          sessionId = session.id
          update(item.id, { sessionId })
        }
        const have = new Set<number>(session.received)
        const queue: number[] = []
        for (let i = 0; i < session.chunks_total; i++) if (!have.has(i)) queue.push(i)
        let done = item.file.size - queue.reduce((acc, i) => acc + Math.min(session.chunk_size, item.file.size - i * session.chunk_size), 0)
        update(item.id, { sent: done })
        let lastT = performance.now()
        let lastB = done
        const worker = async () => {
          while (queue.length) {
            if (ctrl.signal.aborted) throw new DOMException('aborted', 'AbortError')
            const idx = queue.shift()!
            const start = idx * session.chunk_size
            const blob = item.file.slice(start, Math.min(item.file.size, start + session.chunk_size))
            let attempt = 0
            for (;;) {
              try {
                const res = await fetch(`/api/v1/uploads/${sessionId}/chunks/${idx}`, { method: 'PUT', body: blob, headers: { ...csrfHeader(), 'Content-Type': 'application/octet-stream' }, signal: ctrl.signal, credentials: 'same-origin' })
                if (!res.ok) {
                  const body = await res.json().catch(() => ({}))
                  const err = new ApiError(res.status, body?.error?.code ?? 'http', body?.error?.message ?? `Chunk failed (${res.status})`)
                  if ((res.status >= 500 || res.status === 429 || res.status === 422) && res.status !== 507 && attempt < 8) throw Object.assign(err, { retry: true })
                  throw err
                }
                break
              } catch (e: any) {
                if (e?.name === 'AbortError') throw e
                if ((e instanceof TypeError || e?.retry) && attempt < 8) {
                  attempt++
                  await new Promise((r) => setTimeout(r, Math.min(30000, 500 * 2 ** attempt)))
                  continue
                }
                throw e
              }
            }
            done += blob.size
            const now = performance.now()
            const dt = (now - lastT) / 1000
            if (dt > 0.25) {
              const inst = (done - lastB) / dt
              lastT = now
              lastB = done
              update(item.id, (it) => ({ sent: done, speed: it.speed ? it.speed * 0.7 + inst * 0.3 : inst }))
            } else {
              update(item.id, { sent: done })
            }
          }
        }
        await Promise.all(Array.from({ length: Math.min(CHUNK_PARALLEL, Math.max(1, queue.length)) }, worker))
        update(item.id, { status: 'finalizing', sent: item.file.size })
        const res = await api('POST', `/uploads/${sessionId}/complete`, {}, { signal: ctrl.signal })
        update(item.id, { status: 'done', result: res.result, speed: 0 })
        qc.invalidateQueries({ queryKey: ['files'] })
        qc.invalidateQueries({ queryKey: ['stats'] })
      } catch (e: any) {
        const cur = itemsRef.current.find((x) => x.id === item.id)
        if (e?.name === 'AbortError' || ctrl.signal.aborted) {
          if (cur?.status === 'cancelled' && cur.sessionId) api('DELETE', `/uploads/${cur.sessionId}`).catch(() => {})
          return
        }
        update(item.id, { status: 'failed', error: errorMessage(e), speed: 0 })
      } finally {
        controllers.current.delete(item.id)
      }
    },
    [update, qc],
  )

  // scheduler
  useEffect(() => {
    const active = items.filter((i) => i.status === 'uploading' || i.status === 'finalizing').length
    if (active >= MAX_FILES) return
    const next = items.filter((i) => i.status === 'queued').slice(0, MAX_FILES - active)
    next.forEach((it) => runUpload(it))
  }, [items, runUpload])

  useEffect(() => {
    const busy = items.some((i) => i.status === 'uploading' || i.status === 'queued' || i.status === 'finalizing')
    if (!busy) return
    const h = (e: BeforeUnloadEvent) => {
      e.preventDefault()
    }
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [items])

  const pause = (id: number) => {
    update(id, { status: 'paused', speed: 0 })
    controllers.current.get(id)?.abort()
  }
  const resume = (id: number) => update(id, { status: 'queued' })
  const cancel = (id: number) => {
    const it = itemsRef.current.find((x) => x.id === id)
    update(id, { status: 'cancelled', speed: 0 })
    const c = controllers.current.get(id)
    if (c) c.abort()
    else if (it?.sessionId) api('DELETE', `/uploads/${it.sessionId}`).catch(() => {})
  }
  const retry = (id: number) => update(id, { status: 'queued', error: undefined })
  const clearFinished = () => {
    itemsRef.current = itemsRef.current.filter((i) => !['done', 'cancelled'].includes(i.status))
    setItems(itemsRef.current)
  }

  return <UploadCtx.Provider value={{ items, enqueue, pause, resume, cancel, retry, clearFinished }}>{children}</UploadCtx.Provider>
}

// Collects files from a drop event, including folder contents.
export async function filesFromDrop(dt: DataTransfer): Promise<{ file: File; rel: string }[]> {
  const out: { file: File; rel: string }[] = []
  const entries: any[] = []
  for (const item of Array.from(dt.items)) {
    const entry = (item as any).webkitGetAsEntry?.()
    if (entry) entries.push(entry)
  }
  if (entries.length === 0) {
    return Array.from(dt.files).map((f) => ({ file: f, rel: f.name }))
  }
  const walk = async (entry: any, prefix: string): Promise<void> => {
    if (entry.isFile) {
      const file: File = await new Promise((res, rej) => entry.file(res, rej))
      out.push({ file, rel: prefix + file.name })
    } else if (entry.isDirectory) {
      const reader = entry.createReader()
      for (;;) {
        const batch: any[] = await new Promise((res, rej) => reader.readEntries(res, rej))
        if (!batch.length) break
        for (const e of batch) await walk(e, prefix + entry.name + '/')
      }
    }
  }
  for (const e of entries) await walk(e, '')
  return out
}
