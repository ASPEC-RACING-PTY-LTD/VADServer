import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Plus } from 'lucide-react'
import { Badge, Button, CopyButton, EmptyState, ErrorState, Field, Modal, PageLoader, useUI } from '../../components/ui'
import { del, errorMessage, get, post } from '../../lib/api'
import { formatDate, timeAgo } from '../../lib/format'

type ApiKey = { id: number; name: string; prefix: string; scope: string; created_at: number; expires_at: number; last_used_at?: number; last_used_ip?: string; revoked_at?: number }

/** Read-only diagnostics API keys. */
export default function ApiKeysTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const keys = useQuery({ queryKey: ['api-keys'], queryFn: () => get<{ keys: ApiKey[] }>('/admin/api-keys') })
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [days, setDays] = useState(7)
  const [busy, setBusy] = useState(false)
  const [token, setToken] = useState('')
  if (keys.isLoading) return <PageLoader />
  if (keys.error) return <ErrorState error={keys.error} onRetry={() => keys.refetch()} />
  const list = keys.data?.keys ?? []
  const now = Date.now()

  const create = async () => {
    setBusy(true)
    try {
      const r = await post<{ token: string }>('/admin/api-keys', { name: name.trim(), days })
      setToken(r.token)
      setCreating(false)
      setName('')
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    } catch (e) {
      toast('error', 'The key was not created', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const revoke = async (k: ApiKey) => {
    if (!(await confirm({ title: `Revoke "${k.name}"?`, message: 'Anything using this key loses access immediately.', confirmLabel: 'Revoke key' }))) return
    try {
      await del(`/admin/api-keys/${k.id}`)
      qc.invalidateQueries({ queryKey: ['api-keys'] })
    } catch (e) {
      toast('error', 'Not revoked', errorMessage(e))
    }
  }
  return (
    <div className="space-y-4">
      <div className="flex flex-wrap items-start gap-3">
        <p className="min-w-60 flex-1 text-sm text-slate-500 dark:text-slate-400">
          Keys give read-only access to the diagnostics API (<code>/api/v1/diag/</code>): server and container resources, storage, smart processing state and timings, and recent logs. They cannot change anything, read files or reach any other API, and never see secrets. Send them as <code>Authorization: Bearer &lt;key&gt;</code>.
        </p>
        <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setCreating(true)}>
          New key
        </Button>
      </div>
      {list.length === 0 ? (
        <EmptyState icon={<KeyRound className="size-6" />} title="No API keys" />
      ) : (
        <div className="card divide-y divide-slate-100 dark:divide-slate-800">
          {list.map((k) => {
            const state = k.revoked_at ? 'revoked' : k.expires_at < now ? 'expired' : 'active'
            return (
              <div key={k.id} className="flex flex-wrap items-center gap-3 p-3 text-sm">
                <div className="min-w-60 flex-1">
                  <div className="font-medium">
                    {k.name} <Badge tone={state === 'active' ? 'green' : 'slate'}>{state}</Badge>
                  </div>
                  <div className="text-xs text-slate-500">
                    <code>{k.prefix}…</code> · read-only diagnostics · created {formatDate(k.created_at)} · {state === 'active' ? `expires ${timeAgo(k.expires_at)}` : ''}
                    {k.last_used_at ? ` · last used ${timeAgo(k.last_used_at)} from ${k.last_used_ip}` : ' · never used'}
                  </div>
                </div>
                {state === 'active' && (
                  <Button size="sm" variant="ghost" className="text-rose-600" onClick={() => revoke(k)}>
                    Revoke
                  </Button>
                )}
              </div>
            )
          })}
        </div>
      )}

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="New diagnostics key"
        footer={
          <>
            <Button onClick={() => setCreating(false)}>Cancel</Button>
            <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={create}>
              Create key
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="Name" hint="Who or what uses it, for example Support.">
            <input className="input" value={name} maxLength={80} onChange={(e) => setName(e.target.value)} />
          </Field>
          <Field label="Expires after">
            <select className="input" value={days} onChange={(e) => setDays(Number(e.target.value))}>
              {[1, 7, 30, 90].map((d) => (
                <option key={d} value={d}>
                  {d} day{d === 1 ? '' : 's'}
                </option>
              ))}
            </select>
          </Field>
        </div>
      </Modal>

      <Modal open={!!token} onClose={() => setToken('')} title="Copy the key now" footer={<Button onClick={() => setToken('')}>Done</Button>}>
        <p className="mb-3 text-sm text-slate-600 dark:text-slate-300">It is shown only once; VaultDock keeps just a hash of it.</p>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 rounded-lg bg-slate-100 p-2 text-xs break-all dark:bg-slate-800">{token}</code>
          <CopyButton text={token} />
        </div>
      </Modal>
    </div>
  )
}
