import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { X } from 'lucide-react'
import { errorMessage, get, put } from '../../lib/api'
import { Badge, Button, Modal, useUI } from '../ui'
import type { Node } from './types'

export function TagsModal({ node, onClose, onDone }: { node: Node | null; onClose: () => void; onDone: () => void }) {
  const [tags, setTags] = useState<string[]>([])
  const [input, setInput] = useState('')
  const [busy, setBusy] = useState(false)
  const { toast } = useUI()
  const { data } = useQuery({ queryKey: ['tags'], queryFn: () => get('/files/tags'), enabled: !!node })
  useEffect(() => {
    if (node) {
      setTags(node.tags)
      setInput('')
    }
  }, [node])
  const add = (t: string) => {
    t = t.trim()
    if (t && !tags.some((x) => x.toLowerCase() === t.toLowerCase())) setTags([...tags, t])
    setInput('')
  }
  const save = async () => {
    if (!node) return
    setBusy(true)
    try {
      await put(`/files/${node.id}/tags`, { tags: input.trim() ? [...tags, input.trim()] : tags })
      onDone()
      onClose()
    } catch (e) {
      toast('error', 'Could not save tags', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const suggestions = (data?.tags ?? []).map((t: any) => t.tag).filter((t: string) => !tags.includes(t) && t.toLowerCase().includes(input.toLowerCase())).slice(0, 8)
  return (
    <Modal
      open={!!node}
      onClose={onClose}
      title={`Tags for ${node?.name ?? ''}`}
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} onClick={save}>
            Save tags
          </Button>
        </>
      }
    >
      <div className="mb-3 flex min-h-8 flex-wrap gap-1.5">
        {tags.length === 0 && <span className="text-sm text-slate-500">No tags yet.</span>}
        {tags.map((t) => (
          <Badge key={t} tone="violet">
            {t}
            <button onClick={() => setTags(tags.filter((x) => x !== t))} aria-label={`Remove ${t}`}>
              <X className="size-3" />
            </button>
          </Badge>
        ))}
      </div>
      <input
        className="input"
        placeholder="Add a tag and press Enter"
        value={input}
        autoFocus
        onChange={(e) => setInput(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault()
            add(input)
          }
        }}
      />
      {suggestions.length > 0 && (
        <div className="mt-3 flex flex-wrap gap-1.5">
          {suggestions.map((s: string) => (
            <button key={s} onClick={() => add(s)} className="rounded-full border border-slate-300 px-2 py-0.5 text-xs text-slate-600 hover:border-brand-500 hover:text-brand-600 dark:border-slate-700 dark:text-slate-300">
              + {s}
            </button>
          ))}
        </div>
      )}
    </Modal>
  )
}
