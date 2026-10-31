import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, MoreHorizontal, RefreshCw, Settings, Shield, ShieldOff, Trash2, UserPlus } from 'lucide-react'
import { del, errorMessage, get, patch, post, put } from '../../../lib/api'
import { useUser } from '../../../lib/auth'
import { formatBytes, timeAgo } from '../../../lib/format'
import { Badge, Button, cx, ErrorState, Field, IconButton, Menu, Modal, PageLoader, ProgressBar, useUI } from '../../../components/ui'
import { QuotaInput } from './QuotaInput'

export default function UsersTab() {
  const me = useUser()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-users'], queryFn: () => get('/admin/users') })
  const locs = useQuery({ queryKey: ['admin-storage'], queryFn: () => get('/admin/storage') })
  const nodes = useQuery({ queryKey: ['admin-nodes'], queryFn: () => get('/admin/nodes') })
  const remoteNodes: any[] = (nodes.data?.items ?? []).filter((n: any) => n.state === 'approved')
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<any>(null)
  const [resetting, setResetting] = useState<any>(null)
  const [form, setForm] = useState<any>({})
  const [busy, setBusy] = useState(false)
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-users'] })

  const act = async (fn: () => Promise<any>, ok: string) => {
    setBusy(true)
    try {
      await fn()
      toast('success', ok)
      refresh()
      return true
    } catch (e) {
      toast('error', 'Action failed', errorMessage(e))
      return false
    } finally {
      setBusy(false)
    }
  }

  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const locName = (u: any) =>
    u.upload_origin_node_id
      ? `${remoteNodes.find((n) => n.id === u.upload_origin_node_id)?.name ?? 'Remote node'} (remote)`
      : (locs.data?.locations.find((l: any) => l.id === u.location_id)?.name ?? '-')

  // Saves the edit form; a change of remote node goes through its own endpoint
  // and moves the user's existing files there.
  const saveEdit = async () => {
    const { origin, ...fields } = form
    await patch(`/admin/users/${editing.id}`, fields)
    if ((origin ?? null) !== (editing.upload_origin_node_id ?? null)) {
      await put(`/admin/users/${editing.id}/upload-origin`, { node_id: origin ?? null })
      if (origin) {
        await post(`/admin/users/${editing.id}/move-to-node`, {}).catch((e) => toast('warning', 'Files will move on the next cycle', errorMessage(e)))
      }
    }
  }

  return (
    <div>
      <div className="mb-4 flex justify-end">
        <Button
          variant="primary"
          icon={<UserPlus className="size-4" />}
          onClick={() => {
            setForm({ username: '', email: '', password: '', role: 'user', quota_bytes: 0 })
            setCreating(true)
          }}
        >
          Add user
        </Button>
      </div>
      <div className="card overflow-x-auto">
        <table className="w-full text-sm">
          <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
            <tr>
              <th className="px-4 py-2.5">User</th>
              <th className="px-4 py-2.5">Role</th>
              <th className="px-4 py-2.5">Usage</th>
              <th className="hidden px-4 py-2.5 lg:table-cell">Storage</th>
              <th className="hidden px-4 py-2.5 md:table-cell">Last sign-in</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {data.users.map((u: any) => (
              <tr key={u.id} className={cx('border-b border-slate-100 last:border-0 dark:border-slate-800', u.disabled && 'opacity-60')}>
                <td className="px-4 py-3">
                  <div className="font-medium">
                    {u.display_name || u.username} {u.id === me.id && <Badge>you</Badge>} {u.disabled && <Badge tone="red">disabled</Badge>}
                  </div>
                  <div className="text-xs text-slate-500">
                    {u.username}
                    {u.email && ` · ${u.email}`}
                  </div>
                </td>
                <td className="px-4 py-3">
                  <Badge tone={u.role === 'admin' ? 'violet' : 'slate'}>{u.role}</Badge>
                </td>
                <td className="w-56 px-4 py-3">
                  <div className="text-xs text-slate-600 dark:text-slate-300">
                    {formatBytes(u.used_bytes)} {u.quota_bytes > 0 ? `of ${formatBytes(u.quota_bytes)}` : '· unlimited'}
                  </div>
                  {u.quota_bytes > 0 && <ProgressBar value={(u.used_bytes / u.quota_bytes) * 100} className="mt-1 h-1.5" tone={u.used_bytes / u.quota_bytes > 0.9 ? 'red' : 'brand'} />}
                </td>
                <td className="hidden px-4 py-3 text-xs text-slate-500 lg:table-cell">{locName(u)}</td>
                <td className="hidden px-4 py-3 text-xs text-slate-500 md:table-cell">{timeAgo(u.last_login_at)}</td>
                <td className="px-4 py-3 text-right">
                  <Menu
                    trigger={(o) => (
                      <IconButton label="Actions" onClick={o}>
                        <MoreHorizontal className="size-4" />
                      </IconButton>
                    )}
                    items={[
                      { label: 'Edit user, quota & storage', icon: <Settings className="size-4" />, onClick: () => (setForm({ email: u.email, display_name: u.display_name, role: u.role, quota_bytes: u.quota_bytes, location_id: u.location_id, origin: u.upload_origin_node_id ?? null }), setEditing(u)) },
                      { label: 'Reset password', icon: <KeyRound className="size-4" />, onClick: () => (setForm({ password: '' }), setResetting(u)) },
                      { label: 'Sign out all sessions', icon: <ShieldOff className="size-4" />, onClick: () => act(() => post(`/admin/users/${u.id}/revoke-sessions`), 'Sessions revoked') },
                      { label: 'Recalculate usage', icon: <RefreshCw className="size-4" />, onClick: () => act(() => post(`/admin/users/${u.id}/recalculate`), 'Usage recalculated') },
                      { label: u.disabled ? 'Enable account' : 'Disable account', icon: <Shield className="size-4" />, disabled: u.id === me.id, onClick: () => act(() => patch(`/admin/users/${u.id}`, { disabled: !u.disabled }), u.disabled ? 'Account enabled' : 'Account disabled; sessions signed out') },
                      'divider',
                      {
                        label: 'Delete user and files',
                        icon: <Trash2 className="size-4" />,
                        danger: true,
                        disabled: u.id === me.id,
                        onClick: async () => {
                          const ok = await confirm({ title: `Permanently delete ${u.username}?`, message: `This deletes the account, its devices and all ${formatBytes(u.used_bytes)} of stored files. This cannot be undone. To keep the files, disable the account instead.`, confirmLabel: 'Delete permanently', requireText: u.username })
                          if (ok) act(() => del(`/admin/users/${u.id}`, { confirm_username: u.username }), 'User deleted')
                        },
                      },
                    ]}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <Modal
        open={creating}
        onClose={() => setCreating(false)}
        title="Add user"
        footer={
          <>
            <Button onClick={() => setCreating(false)}>Cancel</Button>
            <Button variant="primary" loading={busy} onClick={async () => (await act(() => post('/admin/users', form), 'User created')) && setCreating(false)}>
              Create user
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Username">
              <input className="input" value={form.username ?? ''} onChange={(e) => setForm({ ...form, username: e.target.value })} />
            </Field>
            <Field label="Email (optional)">
              <input className="input" value={form.email ?? ''} onChange={(e) => setForm({ ...form, email: e.target.value })} />
            </Field>
          </div>
          <Field label="Initial password" hint="At least 10 characters. Share it securely; the user can change it in Account settings.">
            <input className="input" type="text" autoComplete="off" value={form.password ?? ''} onChange={(e) => setForm({ ...form, password: e.target.value })} />
          </Field>
          <Field label="Role">
            <select className="input" value={form.role} onChange={(e) => setForm({ ...form, role: e.target.value })}>
              <option value="user">User</option>
              <option value="admin">Administrator</option>
            </select>
          </Field>
          <Field label="Storage quota">
            <QuotaInput value={form.quota_bytes ?? 0} onChange={(v) => setForm((f: any) => ({ ...f, quota_bytes: v }))} />
          </Field>
        </div>
      </Modal>

      <Modal
        open={!!editing}
        onClose={() => setEditing(null)}
        title={`Edit ${editing?.username}`}
        footer={
          <>
            <Button onClick={() => setEditing(null)}>Cancel</Button>
            <Button variant="primary" loading={busy} onClick={async () => (await act(saveEdit, 'User updated')) && setEditing(null)}>
              Save changes
            </Button>
          </>
        }
      >
        {editing && (
          <div className="space-y-4">
            <div className="grid gap-4 sm:grid-cols-2">
              <Field label="Display name">
                <input className="input" value={form.display_name ?? ''} onChange={(e) => setForm({ ...form, display_name: e.target.value })} />
              </Field>
              <Field label="Email">
                <input className="input" value={form.email ?? ''} onChange={(e) => setForm({ ...form, email: e.target.value })} />
              </Field>
            </div>
            <Field label="Role">
              <select className="input" value={form.role} disabled={editing.id === me.id} onChange={(e) => setForm({ ...form, role: e.target.value })}>
                <option value="user">User</option>
                <option value="admin">Administrator</option>
              </select>
            </Field>
            <Field label="Storage quota" hint={`Currently using ${formatBytes(editing.used_bytes)}. Lowering the quota below usage blocks new uploads but keeps existing files.`}>
              <QuotaInput value={form.quota_bytes ?? 0} onChange={(v) => setForm((f: any) => ({ ...f, quota_bytes: v }))} />
            </Field>
            <Field
              label="Where this user's files are kept"
              hint={
                form.origin
                  ? 'Existing and new files move to the remote node (checked there, then removed here). Playback streams through this server.'
                  : editing.upload_origin_node_id
                    ? 'New uploads stay on this server. Files already on the remote node stay there and keep working.'
                    : 'Applies to new uploads; existing files stay where they are.'
              }
            >
              <select
                className="input"
                value={form.origin ? `node:${form.origin}` : `loc:${form.location_id ?? ''}`}
                onChange={(e) => {
                  const [kind, id] = e.target.value.split(':')
                  setForm(kind === 'node' ? { ...form, origin: Number(id) } : { ...form, origin: null, location_id: Number(id) })
                }}
              >
                <optgroup label="This server">
                  {(locs.data?.locations ?? []).map((l: any) => (
                    <option key={l.id} value={`loc:${l.id}`} disabled={!l.enabled}>
                      {l.name} ({l.path})
                    </option>
                  ))}
                </optgroup>
                {remoteNodes.length > 0 && (
                  <optgroup label="Remote nodes">
                    {remoteNodes.map((n) => (
                      <option key={n.id} value={`node:${n.id}`}>
                        {n.name}
                      </option>
                    ))}
                  </optgroup>
                )}
              </select>
            </Field>
          </div>
        )}
      </Modal>

      <Modal
        open={!!resetting}
        onClose={() => setResetting(null)}
        title={`Reset password for ${resetting?.username}`}
        size="sm"
        footer={
          <>
            <Button onClick={() => setResetting(null)}>Cancel</Button>
            <Button variant="primary" loading={busy} onClick={async () => (await act(() => post(`/admin/users/${resetting.id}/reset-password`, { password: form.password }), 'Password reset; sessions signed out')) && setResetting(null)}>
              Reset password
            </Button>
          </>
        }
      >
        <Field label="New password" hint="At least 10 characters. All of the user's browser sessions are signed out.">
          <input className="input" type="text" autoComplete="off" value={form.password ?? ''} onChange={(e) => setForm({ ...form, password: e.target.value })} />
        </Field>
      </Modal>
    </div>
  )
}
