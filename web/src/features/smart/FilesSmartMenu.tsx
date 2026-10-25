import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { Sparkles } from 'lucide-react'
import { Button, Field, Menu, Modal, Toggle, useUI } from '../../components/ui'
import type { Node } from '../../components/files/types'
import { errorMessage } from '../../lib/api'
import { smartApi, smartKeys, useQueues } from './api'

const isVideo = (n: Node) => !n.is_dir && n.mime.startsWith('video/')

/**
 * The file browser's smart processing menu: queue the selection (or this folder, with
 * subfolders), select videos, processed or cleanup-eligible originals, and open cleanup.
 */
export function FilesSmartMenu({ items, selected, folderId, onSelect }: { items: Node[]; selected: Set<number>; folderId?: number; onSelect: (ids: Set<number>) => void }) {
  const navigate = useNavigate()
  const { toast } = useUI()
  const [adding, setAdding] = useState(false)
  const chosen = items.filter((n) => selected.has(n.id))
  const files = chosen.filter((n) => !n.is_dir).map((n) => n.id)
  const folders = chosen.filter((n) => n.is_dir).map((n) => n.id)
  const useFolder = chosen.length === 0 && !!folderId

  const newQueue = () => {
    const p = new URLSearchParams()
    if (useFolder) p.set('folders', String(folderId))
    else {
      if (files.length) p.set('files', files.join(','))
      if (folders.length) p.set('folders', folders.join(','))
    }
    navigate(`/smart/new?${p}`)
  }
  const selectBy = async (want: 'processed' | 'eligible') => {
    const ids = items.filter(isVideo).map((n) => n.id)
    if (!ids.length) return toast('info', 'No videos in this folder')
    try {
      const r = await smartApi.files(ids)
      const pick = r.files.filter((f) => (want === 'processed' ? f.state === 'processed' : !!f.cleanup)).map((f) => f.node_id)
      onSelect(new Set(pick))
      toast('info', `${pick.length} ${want === 'processed' ? 'processed' : 'cleanup-eligible'} original(s) selected`, want === 'eligible' && pick.length ? 'Use Smart processing > Clean up to remove them after their clips are verified.' : undefined)
    } catch (e) {
      toast('error', 'Could not read the processing state', errorMessage(e))
    }
  }
  const hasTarget = chosen.length > 0 || useFolder
  return (
    <>
      <Menu
        trigger={(o) => (
          <Button size="sm" variant="ghost" icon={<Sparkles className="size-4" />} onClick={o}>
            Smart processing
          </Button>
        )}
        items={[
          { label: chosen.length ? `New queue from ${chosen.length} selected` : 'New queue from this folder', onClick: newQueue, disabled: !hasTarget },
          { label: chosen.length ? 'Add selection to a queue…' : 'Add this folder to a queue…', onClick: () => setAdding(true), disabled: !hasTarget },
          'divider',
          { label: 'Select all videos here', onClick: () => onSelect(new Set(items.filter(isVideo).map((n) => n.id))) },
          { label: 'Select processed originals', onClick: () => selectBy('processed') },
          { label: 'Select originals ready for cleanup', onClick: () => selectBy('eligible') },
          'divider',
          { label: folderId ? 'Clean up this folder…' : 'Clean up…', onClick: () => navigate(`/smart/cleanup${folderId ? `?folder=${folderId}` : ''}`) },
        ]}
      />
      {adding && <AddToQueue files={useFolder ? [] : files} folders={useFolder ? [folderId!] : folders} onClose={() => setAdding(false)} />}
    </>
  )
}

function AddToQueue({ files, folders, onClose }: { files: number[]; folders: number[]; onClose: () => void }) {
  const queues = useQueues('active,paused,completed')
  const [queue, setQueue] = useState(0)
  const [recursive, setRecursive] = useState(true)
  const [busy, setBusy] = useState(false)
  const qc = useQueryClient()
  const { toast } = useUI()
  const list = queues.data?.queues ?? []
  const target = queue || list[0]?.id || 0
  const add = async () => {
    setBusy(true)
    try {
      const r = await smartApi.addItems(target, { file_ids: files, folder_ids: folders, recursive })
      toast('success', `${r.added} recording(s) added`, r.skipped ? `${r.skipped} were already in the queue` : undefined)
      qc.invalidateQueries({ queryKey: smartKeys.all })
      onClose()
    } catch (e) {
      toast('error', 'Nothing was added', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      title="Add to a smart processing queue"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!target} onClick={add}>
            Add
          </Button>
        </>
      }
    >
      {list.length === 0 ? (
        <p className="text-sm text-slate-500">There is no open queue yet. Use “New queue” instead.</p>
      ) : (
        <div className="space-y-4">
          <Field label="Queue">
            <select className="input" value={target} onChange={(e) => setQueue(Number(e.target.value))}>
              {list.map((q) => (
                <option key={q.id} value={q.id}>
                  {q.name} ({q.state})
                </option>
              ))}
            </select>
          </Field>
          {folders.length > 0 && <Toggle checked={recursive} onChange={setRecursive} label="Include subfolders" />}
          <p className="text-xs text-slate-500">Only videos are added; ones already in the queue are skipped.</p>
        </div>
      )}
    </Modal>
  )
}
