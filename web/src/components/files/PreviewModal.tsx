import { useEffect, useState } from 'react'
import { ArrowLeft, ArrowRight, Download, Eye } from 'lucide-react'
import { ApiError, errorMessage } from '../../lib/api'
import { fileKind, formatBytes, formatDate, previewable } from '../../lib/format'
import { Badge, Button, EmptyState, ErrorState, Modal, PageLoader } from '../ui'
import { contentURL as defaultContentURL, type ContentURL, type Node } from './types'

function TextPreview({ id, contentURL }: { id: number; contentURL: ContentURL }) {
  const [text, setText] = useState<string | null>(null)
  const [err, setErr] = useState('')
  useEffect(() => {
    const ctrl = new AbortController()
    fetch(contentURL(id), { headers: { Range: 'bytes=0-524287' }, signal: ctrl.signal })
      .then(async (r) => {
        if (!r.ok && r.status !== 206) throw new ApiError(r.status, 'http', `Preview failed (${r.status})`)
        setText(await r.text())
      })
      .catch((e) => e.name !== 'AbortError' && setErr(errorMessage(e)))
    return () => ctrl.abort()
  }, [id, contentURL])
  if (err) return <ErrorState error={err} />
  if (text === null) return <PageLoader />
  return <pre className="scroll-thin max-h-[70vh] overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-100">{text}</pre>
}

/** Full-screen preview with previous/next navigation over `list` (arrow keys too). */
export function PreviewModal({ node, list, onNavigate, onClose, contentURL = defaultContentURL }: { node: Node; list: Node[]; onNavigate: (n: Node) => void; onClose: () => void; contentURL?: ContentURL }) {
  const k = fileKind(node.mime, false)
  const idx = list.findIndex((n) => n.id === node.id)
  const prev = idx > 0 ? list[idx - 1] : null
  const next = idx >= 0 && idx < list.length - 1 ? list[idx + 1] : null
  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.target as HTMLElement)?.tagName === 'VIDEO') return
      if (e.key === 'ArrowLeft' && prev) onNavigate(prev)
      if (e.key === 'ArrowRight' && next) onNavigate(next)
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [prev, next, onNavigate])
  return (
    <Modal
      open
      onClose={onClose}
      size="full"
      title={node.name}
      description={`${formatBytes(node.size)} · modified ${formatDate(node.modified_at)}`}
      footer={
        <div className="flex w-full flex-wrap items-center justify-between gap-2">
          <div className="flex flex-wrap gap-1.5">
            {node.tags.map((t) => (
              <Badge key={t} tone="violet">
                {t}
              </Badge>
            ))}
          </div>
          <div className="flex gap-2">
            <Button icon={<ArrowLeft className="size-4" />} disabled={!prev} onClick={() => prev && onNavigate(prev)}>
              Previous
            </Button>
            <Button icon={<ArrowRight className="size-4" />} disabled={!next} onClick={() => next && onNavigate(next)}>
              Next
            </Button>
            <a href={contentURL(node.id, true)}>
              <Button variant="primary" icon={<Download className="size-4" />}>
                Download
              </Button>
            </a>
          </div>
        </div>
      }
    >
      <div className="flex min-h-[50vh] items-center justify-center">
        {k === 'image' && node.mime !== 'image/svg+xml' && <img src={contentURL(node.id)} alt={node.name} className="max-h-[72vh] max-w-full rounded-lg object-contain" />}
        {k === 'video' && (
          // The browser streams via HTTP range requests; nothing is buffered in full.
          <video key={node.id} src={contentURL(node.id)} controls autoPlay preload="metadata" className="max-h-[72vh] w-full rounded-lg bg-black">
            Your browser cannot play this video format. Download it instead.
          </video>
        )}
        {k === 'audio' && <audio key={node.id} src={contentURL(node.id)} controls autoPlay className="w-full max-w-xl" />}
        {k === 'pdf' && <iframe title={node.name} src={contentURL(node.id)} className="h-[72vh] w-full rounded-lg border border-slate-200 dark:border-slate-800" />}
        {k === 'text' && (
          <div className="w-full">
            <TextPreview id={node.id} contentURL={contentURL} />
          </div>
        )}
        {!previewable(node.mime) && <EmptyState icon={<Eye className="size-6" />} title="No preview available" description="Download the file to open it." />}
      </div>
      {k === 'video' && <p className="mt-3 text-center text-xs text-slate-500">MP4 (H.264/AAC) and WebM play in all modern browsers. MKV playback depends on the browser and codecs used.</p>}
    </Modal>
  )
}
