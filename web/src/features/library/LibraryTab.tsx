import { useEffect, useState } from 'react'
import { useInfiniteQuery, useQuery } from '@tanstack/react-query'
import { Clapperboard, Download, FileIcon, Search } from 'lucide-react'
import { Badge, Button, cx, EmptyState, ErrorState, Modal, PageLoader } from '../../components/ui'
import { get, withQuery } from '../../lib/api'
import { formatBytes, formatDate } from '../../lib/format'

type Item = {
  id: number
  name: string
  size: number
  mime: string
  sha256?: string
  modified_at: number
  created_at: number
  file_created_at?: number
  path: string
  user_id: number
  username: string
  display_name: string
  clip: boolean
}
type Owner = { user_id: number; username: string; display_name: string; files: number; bytes: number }

const PAGE = 60
const contentURL = (id: number, download = false) => `/api/v1/admin/library/${id}/content${download ? '?download=1' : ''}`

/** Every user's files for administrators: browse, play and download (read-only; opening a file is audited). */
export default function LibraryTab() {
  const [owner, setOwner] = useState(0)
  const [kind, setKind] = useState('video')
  const [clips, setClips] = useState('')
  const [sort, setSort] = useState('modified')
  const [q, setQ] = useState('')
  const [search, setSearch] = useState('')
  const [open, setOpen] = useState<Item | null>(null)
  useEffect(() => {
    const t = setTimeout(() => setSearch(q.trim()), 300)
    return () => clearTimeout(t)
  }, [q])
  const owners = useQuery({ queryKey: ['library-owners', kind], queryFn: () => get<{ owners: Owner[] }>(withQuery('/admin/library/owners', { kind })) })
  const list = useInfiniteQuery({
    queryKey: ['library', owner, kind, clips, sort, search],
    initialPageParam: 0,
    queryFn: ({ pageParam }) =>
      get<{ items: Item[]; total: number; bytes: number }>(withQuery('/admin/library', { user_id: owner || undefined, kind, clips, sort, desc: sort === 'name' ? 0 : 1, q: search, limit: PAGE, offset: pageParam })),
    getNextPageParam: (last, pages) => (pages.length * PAGE < last.total ? pages.length * PAGE : undefined),
  })
  const items = list.data?.pages.flatMap((p) => p.items) ?? []
  const first = list.data?.pages[0]
  return (
    <div className="space-y-4">
      <p className="text-sm text-slate-500 dark:text-slate-400">Files of every user. Read-only: opening or downloading a file is recorded in the audit log.</p>
      <div className="card flex flex-wrap items-center gap-2 p-2">
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400" />
          <input className="input h-8 border-transparent bg-slate-100 pl-8 shadow-none dark:bg-slate-800" placeholder="Search names" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <select className="input h-8 w-auto py-0" aria-label="User" value={owner} onChange={(e) => setOwner(Number(e.target.value))}>
          <option value={0}>All users</option>
          {(owners.data?.owners ?? []).map((o) => (
            <option key={o.user_id} value={o.user_id}>
              {o.display_name || o.username} ({o.files})
            </option>
          ))}
        </select>
        <select className="input h-8 w-auto py-0" aria-label="Type" value={kind} onChange={(e) => setKind(e.target.value)}>
          <option value="video">Videos</option>
          <option value="image">Images</option>
          <option value="audio">Audio</option>
          <option value="document">Documents</option>
          <option value="all">All files</option>
        </select>
        <select className="input h-8 w-auto py-0" aria-label="Clips" value={clips} onChange={(e) => setClips(e.target.value)}>
          <option value="">Clips and originals</option>
          <option value="clips">Smart processing clips</option>
          <option value="originals">Everything else</option>
        </select>
        <select className="input h-8 w-auto py-0" aria-label="Sort" value={sort} onChange={(e) => setSort(e.target.value)}>
          <option value="modified">Newest</option>
          <option value="created">Recorded</option>
          <option value="size">Largest</option>
          <option value="name">Name</option>
        </select>
      </div>
      {first && (
        <p className="text-xs text-slate-500">
          {first.total} file{first.total === 1 ? '' : 's'}, {formatBytes(first.bytes)}
        </p>
      )}
      {list.isLoading ? (
        <PageLoader />
      ) : list.error ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : items.length === 0 ? (
        <EmptyState icon={<Clapperboard className="size-6" />} title="No files" />
      ) : (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4 xl:grid-cols-5">
          {items.map((it) => (
            <button key={it.id} type="button" onClick={() => setOpen(it)} className="card overflow-hidden text-left transition hover:ring-2 hover:ring-brand-500/40">
              <Thumb item={it} />
              <div className="space-y-0.5 p-2.5">
                <div className="truncate text-sm font-medium" title={it.path}>
                  {it.name}
                </div>
                <div className="flex items-center gap-1.5 truncate text-xs text-slate-500">
                  <span className="truncate">{it.display_name || it.username}</span>
                  {it.clip && <Badge tone="violet">clip</Badge>}
                </div>
                <div className="text-xs text-slate-400">
                  {formatBytes(it.size)} · {formatDate(it.file_created_at ?? it.modified_at)}
                </div>
              </div>
            </button>
          ))}
        </div>
      )}
      {list.hasNextPage && (
        <div className="text-center">
          <Button loading={list.isFetchingNextPage} onClick={() => list.fetchNextPage()}>
            Load more
          </Button>
        </div>
      )}
      <Modal
        open={!!open}
        onClose={() => setOpen(null)}
        size="xl"
        title={open?.name ?? ''}
        description={open ? `${open.display_name || open.username} · ${open.path} · ${formatBytes(open.size)}` : undefined}
        footer={
          open && (
            <a href={contentURL(open.id, true)}>
              <Button icon={<Download className="size-4" />}>Download</Button>
            </a>
          )
        }
      >
        {open && <Viewer item={open} />}
      </Modal>
    </div>
  )
}

/** Poster of a video (made on first request; retried while pending). */
function Thumb({ item }: { item: Item }) {
  const [attempt, setAttempt] = useState(0)
  const [failed, setFailed] = useState(false)
  if (!item.mime.startsWith('video/') && !item.mime.startsWith('image/')) {
    return (
      <div className="flex aspect-video items-center justify-center bg-slate-100 dark:bg-slate-800">
        <FileIcon className="size-8 text-slate-400" />
      </div>
    )
  }
  const src = item.mime.startsWith('image/') ? contentURL(item.id) : `/api/v1/admin/library/${item.id}/thumbnail?a=${attempt}`
  return (
    <div className="relative aspect-video bg-slate-900">
      {!failed && (
        <img
          src={src}
          alt=""
          loading="lazy"
          className="size-full object-cover"
          onError={() => (attempt < 4 ? setTimeout(() => setAttempt((a) => a + 1), 2500) : setFailed(true))}
        />
      )}
      {item.mime.startsWith('video/') && <Clapperboard className={cx('absolute right-2 bottom-2 size-4 text-white/80', failed && 'top-1/2 left-1/2 size-8 -translate-1/2')} />}
    </div>
  )
}

function Viewer({ item }: { item: Item }) {
  if (item.mime.startsWith('video/')) return <video src={contentURL(item.id)} controls autoPlay className="max-h-[70vh] w-full rounded-lg bg-black" />
  if (item.mime.startsWith('image/')) return <img src={contentURL(item.id)} alt={item.name} className="mx-auto max-h-[70vh] rounded-lg" />
  if (item.mime.startsWith('audio/')) return <audio src={contentURL(item.id)} controls className="w-full" />
  return <p className="text-sm text-slate-500">No preview for this type; download it to open it.</p>
}
