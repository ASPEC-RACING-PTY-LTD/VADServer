import { useEffect, useRef, useState, type FormEvent } from 'react'
import { errorMessage, patch } from '../../lib/api'
import { Button, Field, Modal } from '../ui'
import type { Node } from './types'

export function RenameModal({ node, onClose, onDone }: { node: Node | null; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  useEffect(() => {
    if (node) {
      setName(node.name)
      setError('')
      setTimeout(() => {
        const dot = node.is_dir ? -1 : node.name.lastIndexOf('.')
        inputRef.current?.setSelectionRange(0, dot > 0 ? dot : node.name.length)
      }, 30)
    }
  }, [node])
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    if (!node) return
    setBusy(true)
    try {
      await patch(`/files/${node.id}`, { name })
      onDone()
      onClose()
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={!!node}
      onClose={onClose}
      title="Rename"
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim() || name === node?.name} onClick={() => submit()}>
            Rename
          </Button>
        </>
      }
    >
      <form onSubmit={submit}>
        <Field label="Name" error={error}>
          <input ref={inputRef} className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </form>
    </Modal>
  )
}
