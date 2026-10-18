import { useEffect, useState, type FormEvent } from 'react'
import { errorMessage, post } from '../../lib/api'
import { Button, Field, Modal } from '../ui'

export function NewFolderModal({ open, parentId, onClose, onDone }: { open: boolean; parentId: number; onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (open) {
      setName('')
      setError('')
    }
  }, [open])
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    try {
      await post('/files/folder', { parent_id: parentId, name })
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
      open={open}
      onClose={onClose}
      title="New folder"
      size="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={() => submit()}>
            Create
          </Button>
        </>
      }
    >
      <form onSubmit={submit}>
        <Field label="Folder name" error={error}>
          <input className="input" autoFocus value={name} onChange={(e) => setName(e.target.value)} />
        </Field>
      </form>
    </Modal>
  )
}
