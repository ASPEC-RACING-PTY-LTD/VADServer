import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { ExternalLink, Link2 } from 'lucide-react'
import { Badge, Button, CopyButton, EmptyState, ErrorState, Field, Modal, PageHeader, PageLoader, Tabs, Toggle, useUI } from '../../components/ui'
import { errorMessage } from '../../lib/api'
import { formatDate, timeAgo } from '../../lib/format'
import { EXPIRY, shareApi, shareLink, type Share } from './api'

const tone = { active: 'green', expired: 'slate', revoked: 'slate', unavailable: 'amber' } as const

/** The user's public video links: views, editing, copying and removing them. */
export default function SharesPage() {
  const qc = useQueryClient()
  const { confirm, toast } = useUI()
  const q = useQuery({ queryKey: ['shares', 'all'], queryFn: () => shareApi.list() })
  const [view, setView] = useState<'active' | 'ended'>('active')
  const [editing, setEditing] = useState<Share | null>(null)
  if (q.isLoading) return <PageLoader />
  if (q.error) return <ErrorState error={q.error} onRetry={() => q.refetch()} />
  const all = q.data?.shares ?? []
  const list = all.filter((s) => (view === 'active' ? s.state === 'active' : s.state !== 'active'))
  const remove = async (s: Share) => {
    if (!(await confirm({ title: `Remove the link to "${s.title}"?`, message: 'Anyone using it can no longer watch the video.', confirmLabel: 'Remove link' }))) return
    try {
      await shareApi.revoke(s.id)
      qc.invalidateQueries({ queryKey: ['shares'] })
    } catch (e) {
      toast('error', 'Not removed', errorMessage(e))
    }
  }
  return (
    <div className="space-y-4">
      <PageHeader title="Shared links" icon={<Link2 className="size-5" />} description="Public links to your videos. Share a video from Files (right-click > Share link)." />
      <Tabs
        value={view}
        onChange={setView}
        tabs={[
          { id: 'active', label: 'Active', count: all.filter((s) => s.state === 'active').length },
          { id: 'ended', label: 'Expired or removed' },
        ]}
      />
      {list.length === 0 ? (
        <EmptyState icon={<Link2 className="size-6" />} title={view === 'active' ? 'No active links' : 'Nothing here'} />
      ) : (
        <div className="card divide-y divide-slate-100 dark:divide-slate-800">
          {list.map((s) => (
            <div key={s.id} className="flex flex-wrap items-center gap-3 p-3 text-sm">
              <div className="min-w-60 flex-1">
                <div className="flex items-center gap-2 font-medium">
                  {s.title} <Badge tone={tone[s.state]}>{s.state === 'unavailable' ? 'file changed or removed' : s.state}</Badge>
                </div>
                <div className="truncate text-xs text-slate-500">
                  {s.path || s.name} · {s.views} view{s.views === 1 ? '' : 's'}
                  {s.last_viewed_at ? ` (last ${timeAgo(s.last_viewed_at)})` : ''} · {s.expires_at ? `${s.state === 'active' ? 'expires' : 'expired'} ${formatDate(s.expires_at)}` : 'never expires'}
                  {s.allow_download ? ' · download allowed' : ''}
                </div>
              </div>
              {s.state === 'active' && (
                <>
                  <a href={s.url} target="_blank" rel="noreferrer" className="text-slate-400 hover:text-slate-600" aria-label="Open the public page">
                    <ExternalLink className="size-4" />
                  </a>
                  <CopyButton text={shareLink(s)} label="Copy link" />
                  <Button size="sm" variant="ghost" onClick={() => setEditing(s)}>
                    Edit
                  </Button>
                  <Button size="sm" variant="ghost" className="text-rose-600" onClick={() => remove(s)}>
                    Remove
                  </Button>
                </>
              )}
            </div>
          ))}
        </div>
      )}
      {editing && <EditShare share={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function EditShare({ share, onClose }: { share: Share; onClose: () => void }) {
  const qc = useQueryClient()
  const { toast } = useUI()
  const [title, setTitle] = useState(share.title)
  const [description, setDescription] = useState(share.description)
  const [expires, setExpires] = useState<number | -1>(-1)
  const [download, setDownload] = useState(share.allow_download)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    try {
      await shareApi.update(share.id, { title: title.trim(), description: description.trim(), allow_download: download, ...(expires >= 0 ? { expires_in: expires } : {}) })
      qc.invalidateQueries({ queryKey: ['shares'] })
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
      onClose={onClose}
      title="Edit link"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!title.trim()} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Title">
          <input className="input" value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field label="Description">
          <textarea className="input min-h-20" value={description} maxLength={2000} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Expiry">
          <select className="input" value={expires} onChange={(e) => setExpires(Number(e.target.value))}>
            <option value={-1}>Keep ({share.expires_at ? formatDate(share.expires_at) : 'never'})</option>
            {EXPIRY.map((x) => (
              <option key={x.seconds} value={x.seconds}>
                {x.seconds ? `${x.label} from now` : 'Never'}
              </option>
            ))}
          </select>
        </Field>
        <Toggle checked={download} onChange={setDownload} label="Allow downloading" />
      </div>
    </Modal>
  )
}
