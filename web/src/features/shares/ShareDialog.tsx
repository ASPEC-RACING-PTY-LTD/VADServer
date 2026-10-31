import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link2 } from 'lucide-react'
import { Badge, Button, CopyButton, Field, Modal, Toggle, useUI } from '../../components/ui'
import type { Node } from '../../components/files/types'
import { errorMessage } from '../../lib/api'
import { formatDate, timeAgo } from '../../lib/format'
import { EXPIRY, shareApi, shareLink } from './api'

const stripExt = (n: string) => n.replace(/\.[^.]+$/, '')

/** Create a public link to a video, and see or revoke its existing links. */
export function ShareDialog({ node, onClose }: { node: Node | null; onClose: () => void }) {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const [title, setTitle] = useState('')
  const [description, setDescription] = useState('')
  const [expires, setExpires] = useState<number>(7 * 86400)
  const [download, setDownload] = useState(false)
  const [busy, setBusy] = useState(false)
  const [lastNode, setLastNode] = useState<number | null>(null)
  if (node && node.id !== lastNode) {
    setLastNode(node.id)
    setTitle(stripExt(node.name))
    setDescription('')
  }
  const existing = useQuery({ queryKey: ['shares', node?.id], queryFn: () => shareApi.list(node!.id), enabled: !!node })
  const active = (existing.data?.shares ?? []).filter((s) => s.state === 'active')

  const create = async () => {
    if (!node) return
    setBusy(true)
    try {
      const s = await shareApi.create({ node_id: node.id, title: title.trim(), description: description.trim(), expires_in: expires, allow_download: download })
      await navigator.clipboard?.writeText(shareLink(s)).catch(() => {})
      toast('success', 'Link created and copied', shareLink(s))
      qc.invalidateQueries({ queryKey: ['shares'] })
    } catch (e) {
      toast('error', 'The link was not created', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const revoke = async (id: number) => {
    if (!(await confirm({ title: 'Remove this link?', message: 'Anyone using it can no longer watch the video.', confirmLabel: 'Remove link' }))) return
    await shareApi.revoke(id)
    qc.invalidateQueries({ queryKey: ['shares'] })
  }
  return (
    <Modal
      open={!!node}
      onClose={onClose}
      title="Share video"
      description="Anyone with the link can watch it, without an account, until it expires or you remove it."
      footer={
        <>
          <Button onClick={onClose}>Close</Button>
          <Button variant="primary" icon={<Link2 className="size-4" />} loading={busy} disabled={!title.trim()} onClick={create}>
            Create link
          </Button>
        </>
      }
    >
      <div className="space-y-4">
        <Field label="Title">
          <input className="input" value={title} maxLength={120} onChange={(e) => setTitle(e.target.value)} />
        </Field>
        <Field label="Description (optional)">
          <textarea className="input min-h-20" value={description} maxLength={2000} onChange={(e) => setDescription(e.target.value)} />
        </Field>
        <Field label="Link works for">
          <select className="input" value={expires} onChange={(e) => setExpires(Number(e.target.value))}>
            {EXPIRY.map((x) => (
              <option key={x.seconds} value={x.seconds}>
                {x.label}
              </option>
            ))}
          </select>
        </Field>
        <Toggle checked={download} onChange={setDownload} label="Allow downloading" description="Viewers can save the video file." />
        {active.length > 0 && (
          <div className="space-y-2 border-t border-slate-200 pt-4 dark:border-slate-700">
            <div className="text-sm font-medium">Existing links</div>
            {active.map((s) => (
              <div key={s.id} className="flex items-center gap-2 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="truncate">{s.title}</div>
                  <div className="text-xs text-slate-500">
                    {s.views} view{s.views === 1 ? '' : 's'} · {s.expires_at ? `expires ${timeAgo(s.expires_at)}` : 'never expires'} · made {formatDate(s.created_at)}
                    {s.allow_download && (
                      <Badge tone="slate" className="ml-1">
                        download
                      </Badge>
                    )}
                  </div>
                </div>
                <CopyButton text={shareLink(s)} label="Copy link" />
                <Button size="sm" variant="ghost" className="text-rose-600" onClick={() => revoke(s.id)}>
                  Remove
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>
    </Modal>
  )
}
