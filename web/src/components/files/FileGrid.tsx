import { MoreHorizontal } from 'lucide-react'
import { fileKind, formatBytes } from '../../lib/format'
import { cx, IconButton } from '../ui'
import { FileIcon } from '../FileIcon'
import type { FileViewProps } from './FileTable'
import { contentURL as defaultContentURL, type ContentURL } from './types'
import { useVideoMedia, VideoThumb } from '../../features/video/thumbs'

/**
 * Grid view with image and video thumbnails. Video thumbnails come from the server (FFmpeg) for
 * the user's own files; other listings (snapshots) show an icon.
 */
export function FileGrid({ items, selected, onRowClick, onToggle, onOpen, onContextMenu, onActions, contentURL = defaultContentURL }: FileViewProps & { contentURL?: ContentURL }) {
  const videoMedia = useVideoMedia(items, contentURL === defaultContentURL)
  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 md:grid-cols-4 xl:grid-cols-6">
      {items.map((n, idx) => (
        <div
          key={n.id}
          onClick={(e) => onRowClick(e, n, idx)}
          onDoubleClick={() => onOpen(n)}
          onContextMenu={(e) => {
            e.preventDefault()
            onContextMenu(n, e.clientX, e.clientY)
          }}
          className={cx('card group relative flex cursor-default flex-col overflow-hidden transition', selected.has(n.id) ? 'ring-2 ring-brand-500' : 'hover:shadow-md')}
        >
          <div className="flex aspect-[4/3] items-center justify-center overflow-hidden bg-slate-100 dark:bg-slate-800/60">
            {fileKind(n.mime, n.is_dir) === 'image' && n.mime !== 'image/svg+xml' && n.size < 25 * 1024 * 1024 ? (
              <img src={contentURL(n.id)} alt="" loading="lazy" className="size-full object-cover" />
            ) : fileKind(n.mime, n.is_dir) === 'video' && contentURL === defaultContentURL ? (
              <VideoThumb node={n} media={videoMedia.get(n.id)} />
            ) : (
              <FileIcon mime={n.mime} isDir={n.is_dir} size="lg" />
            )}
          </div>
          <div className="flex items-center gap-2 p-2.5">
            <div className="min-w-0 flex-1">
              <div className="truncate text-sm font-medium">{n.name}</div>
              <div className="text-xs text-slate-500">{n.is_dir ? `${n.child_count ?? 0} items` : formatBytes(n.size)}</div>
            </div>
            <IconButton
              label="Actions"
              onClick={(e) => {
                e.stopPropagation()
                const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
                onActions(n, r.right, r.bottom + 4)
              }}
            >
              <MoreHorizontal className="size-4" />
            </IconButton>
          </div>
          <input type="checkbox" className={cx('absolute top-2 left-2 size-4 accent-brand-600', selected.has(n.id) ? 'opacity-100' : 'opacity-0 group-hover:opacity-100')} checked={selected.has(n.id)} onClick={(e) => e.stopPropagation()} onChange={() => onToggle(n.id)} aria-label={`Select ${n.name}`} />
        </div>
      ))}
    </div>
  )
}
