import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { del, errorMessage, get, patch, post } from '../../../lib/api'
import { Button, ErrorState, Field, Modal, PageLoader, Toggle, useUI } from '../../../components/ui'
import { LocationBar } from '../LocationBar'
import { GB } from './QuotaInput'
import { StorageCompression } from './StorageCompression'
import { SizeInput } from '../../../components/SizeInput'

export default function StorageTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-storage'], queryFn: () => get('/admin/storage'), refetchInterval: 20_000 })
  const [adding, setAdding] = useState(false)
  const [editing, setEditing] = useState<any>(null)
  const [form, setForm] = useState<any>({})
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-storage'] })
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const save = async (fn: () => Promise<any>, ok: string) => {
    try {
      await fn()
      toast('success', ok)
      refresh()
      setAdding(false)
      setEditing(null)
    } catch (e) {
      toast('error', 'Could not save', errorMessage(e))
    }
  }
  return (
    <div>
      <StorageCompression />
      <div className="mb-4 flex flex-col gap-3 rounded-2xl bg-slate-100 p-4 text-sm text-slate-600 sm:flex-row sm:items-center dark:bg-slate-900 dark:text-slate-300">
        <span className="flex-1">
          Storage locations are directories on HDDs, SSDs or mounted volumes. New locations must be inside the allowed roots: <code className="font-mono text-xs">{(data.allowed_roots ?? []).join(', ') || 'any path'}</code>. With Docker, mount the disk into the container under one of these roots first.
        </span>
        <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => (setForm({ name: '', path: (data.allowed_roots?.[0] ?? '') + '/', reserve_bytes: 10 * GB, is_default: false }), setAdding(true))}>
          Add location
        </Button>
      </div>
      <div className="space-y-4">
        {data.locations.map((l: any) => (
          <div key={l.id} className="card p-5">
            <LocationBar l={l} />
            <div className="mt-4 flex flex-wrap items-center gap-2 text-xs text-slate-500">
              <span>
                {l.blob_count.toLocaleString()} stored objects · {l.user_count} user(s) assigned
              </span>
              <span className="ml-auto flex gap-2">
                <Button size="sm" onClick={() => (setForm({ name: l.name, reserve_bytes: l.reserve_bytes, enabled: l.enabled, is_default: l.is_default }), setEditing(l))}>
                  Edit
                </Button>
                {!l.is_default && (
                  <Button
                    size="sm"
                    variant="ghost"
                    className="text-rose-600"
                    disabled={l.blob_count > 0 || l.user_count > 0}
                    title={l.blob_count > 0 ? 'Locations holding files cannot be removed' : undefined}
                    onClick={async () => (await confirm({ title: `Remove ${l.name}?`, message: 'The location is removed from VaultDock. No files on the disk are deleted.', confirmLabel: 'Remove' })) && save(() => del(`/admin/storage/${l.id}`), 'Location removed')}
                  >
                    Remove
                  </Button>
                )}
              </span>
            </div>
          </div>
        ))}
      </div>
      <Modal
        open={adding}
        onClose={() => setAdding(false)}
        title="Add storage location"
        footer={
          <>
            <Button onClick={() => setAdding(false)}>Cancel</Button>
            <Button variant="primary" onClick={() => save(() => post('/admin/storage', form), 'Location added')}>
              Add location
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="Name">
            <input className="input" placeholder="Media HDD" value={form.name} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </Field>
          <Field label="Path inside the server/container" hint="The directory must exist and be writable by VaultDock. VaultDock creates blobs/ and tmp/ inside it.">
            <input className="input font-mono" value={form.path} onChange={(e) => setForm({ ...form, path: e.target.value })} />
          </Field>
          <Field label="Keep free (reserve)" hint="Uploads are refused when free space would drop below this.">
            <SizeInput value={form.reserve_bytes ?? 0} onChange={(v) => setForm({ ...form, reserve_bytes: v })} />
          </Field>
          <Toggle checked={form.is_default} onChange={(v) => setForm({ ...form, is_default: v })} label="Use for new users by default" />
        </div>
      </Modal>
      <Modal
        open={!!editing}
        onClose={() => setEditing(null)}
        title={`Edit ${editing?.name}`}
        footer={
          <>
            <Button onClick={() => setEditing(null)}>Cancel</Button>
            <Button variant="primary" onClick={() => save(() => patch(`/admin/storage/${editing.id}`, form), 'Location updated')}>
              Save
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="Name">
            <input className="input" value={form.name ?? ''} onChange={(e) => setForm({ ...form, name: e.target.value })} />
          </Field>
          <Field label="Keep free (reserve)">
            <SizeInput value={form.reserve_bytes ?? 0} onChange={(v) => setForm({ ...form, reserve_bytes: v })} />
          </Field>
          <Toggle checked={!!form.enabled} disabled={editing?.is_default} onChange={(v) => setForm({ ...form, enabled: v })} label="Accept new uploads" description="Disabled locations still serve existing files." />
          <Toggle checked={!!form.is_default} disabled={editing?.is_default} onChange={(v) => setForm({ ...form, is_default: v })} label="Default location for new users" />
        </div>
      </Modal>
    </div>
  )
}
