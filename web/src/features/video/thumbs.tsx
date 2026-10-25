import { useMemo, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Film, Play } from 'lucide-react'
import { FileIcon } from '../../components/FileIcon'
import { cx } from '../../components/ui'
import type { Node } from '../../components/files/types'
import { mediaBatch, thumbnailURL, type MediaItem } from './api'
import { formatTime } from './model'

const isVideo = (n: Node) => !n.is_dir && n.mime.startsWith('video/')

/**
 * Thumbnail state and durations of the videos among `nodes` (one request for the whole view).
 * Thumbnails are made in the background on the server; while any is pending this asks again.
 */
export function useVideoMedia(nodes: Node[], enabled = true) {
  const ids = useMemo(() => nodes.filter(isVideo).map((n) => n.id).slice(0, 300), [nodes])
  const q = useQuery({
    queryKey: ['video-media', ids],
    queryFn: () => mediaBatch(ids),
    enabled: enabled && ids.length > 0,
    staleTime: 60_000,
    refetchInterval: (query) => (query.state.data?.items.some((i) => i.state === 'pending') ? 2500 : false),
  })
  return useMemo(() => new Map((q.data?.items ?? []).map((i) => [i.id, i])), [q.data])
}

/** Card thumbnail of a video: the server's poster frame with its duration, or an icon. */
export function VideoThumb({ node, media }: { node: Node; media?: MediaItem }) {
  const [broken, setBroken] = useState(false)
  const ready = media?.state === 'ready' && !broken
  return (
    <div className="relative size-full">
      {ready ? (
        <img src={thumbnailURL(node)} alt="" loading="lazy" decoding="async" onError={() => setBroken(true)} className="size-full bg-slate-900 object-cover" />
      ) : (
        <div className={cx('flex size-full items-center justify-center', media?.state === 'pending' && 'animate-pulse')}>
          {media?.state === 'failed' ? <FileIcon mime={node.mime} isDir={false} size="lg" /> : <Film className="size-8 text-slate-400" />}
        </div>
      )}
      {ready && (
        <span className="pointer-events-none absolute inset-0 flex items-center justify-center opacity-0 transition group-hover:opacity-100">
          <span className="flex size-9 items-center justify-center rounded-full bg-black/55 text-white backdrop-blur-sm">
            <Play className="ml-0.5 size-4 fill-current" />
          </span>
        </span>
      )}
      {media?.duration ? <span className="pointer-events-none absolute right-1.5 bottom-1.5 rounded bg-black/70 px-1.5 py-0.5 font-mono text-[11px] font-medium text-white tabular-nums">{formatTime(media.duration, 0)}</span> : null}
      {media?.state === 'failed' && <span className="absolute bottom-1.5 left-1.5 rounded bg-black/60 px-1.5 py-0.5 text-[10px] text-slate-200">No preview</span>}
    </div>
  )
}
