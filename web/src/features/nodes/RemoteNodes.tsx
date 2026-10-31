import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, Trash2, UserPlus } from 'lucide-react'
import { Badge, Button, CopyButton, Field, Modal, ProgressBar, useUI } from '../../components/ui'
import { del, errorMessage, get, post, put } from '../../lib/api'
import { formatBytes, formatDate, timeAgo } from '../../lib/format'

interface Pairing {
  id: number
  name: string
  public_url: string
  created_at: number
  expires_at: number
  used_at?: number
  node_id?: number
}

interface MoveState {
  node_id: number
  running: boolean
  moved: number
  failed: number
  bytes: number
  error?: string
  started_at: number
  ended_at?: number
}

interface RemoteInfo {
  ready: boolean
  reason: string
  key_id: string
  users: { id: number; username: string; display_name: string }[]
  stats: { files: Record<string, number>; bytes: Record<string, number>; local_files: number; local_bytes: number }
  moving: Record<string, MoveState>
}

/** Pairing secrets for remote storage nodes (a node at another site, approved by its secret). */
export function RemotePairing() {
  const qc = useQueryClient()
  const { toast } = useUI()
  const list = useQuery({ queryKey: ['node-pairings'], queryFn: () => get<{ pairings: Pairing[] }>('/admin/node-pairings'), refetchInterval: 10_000 })
  const [open, setOpen] = useState(false)
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [hours, setHours] = useState(24)
  const [busy, setBusy] = useState(false)
  const [made, setMade] = useState<{ secret: string; command: string } | null>(null)
  const now = Date.now()
  const pairings = list.data?.pairings ?? []

  const create = async () => {
    setBusy(true)
    try {
      const r = await post<{ secret: string; command: string }>('/admin/node-pairings', { name: name.trim(), public_url: url.trim(), hours })
      setMade(r)
      setOpen(false)
      setName('')
      setUrl('')
      qc.invalidateQueries({ queryKey: ['node-pairings'] })
    } catch (e) {
      toast('error', 'Not created', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <section className="card space-y-3 p-5">
      <div className="flex flex-wrap items-center gap-2">
        <h2 className="font-semibold">Pair a remote node</h2>
        <div className="flex-1" />
        <Button size="sm" variant="primary" icon={<KeyRound className="size-4" />} onClick={() => setOpen(true)}>
          New pairing secret
        </Button>
      </div>
      <p className="text-sm text-slate-500">
        For a node at another site that keeps users&apos; files. Its owner runs the installer and enters the secret; the node is approved straight away. They get no account and no settings. Everything stored there is encrypted for a key only this server has.
      </p>
      {pairings.length > 0 && (
        <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
          {pairings.map((p) => {
            const state = p.used_at ? 'used' : p.expires_at < now ? 'expired' : 'waiting'
            return (
              <li key={p.id} className="flex items-center gap-3 py-2">
                <div className="min-w-0 flex-1">
                  <div className="font-medium">
                    {p.name} <Badge tone={state === 'used' ? 'green' : state === 'waiting' ? 'amber' : 'slate'}>{state === 'used' ? 'paired' : state}</Badge>
                  </div>
                  <div className="text-xs text-slate-500">
                    {p.public_url || 'no address given'} · made {formatDate(p.created_at)}
                    {state === 'waiting' && ` · expires ${timeAgo(p.expires_at)}`}
                    {p.used_at && ` · paired ${timeAgo(p.used_at)}`}
                  </div>
                </div>
                {state === 'waiting' && (
                  <Button size="sm" variant="ghost" icon={<Trash2 className="size-4" />} aria-label="Delete pairing secret" onClick={async () => {
                    await del(`/admin/node-pairings/${p.id}`)
                    qc.invalidateQueries({ queryKey: ['node-pairings'] })
                  }} />
                )}
              </li>
            )
          })}
        </ul>
      )}

      <Modal
        open={open}
        onClose={() => setOpen(false)}
        title="New pairing secret"
        footer={
          <>
            <Button onClick={() => setOpen(false)}>Cancel</Button>
            <Button variant="primary" loading={busy} disabled={!name.trim()} onClick={create}>
              Create
            </Button>
          </>
        }
      >
        <div className="space-y-4">
          <Field label="Node name">
            <input className="input" value={name} maxLength={64} onChange={(e) => setName(e.target.value)} placeholder="hater-storage" />
          </Field>
          <Field label="Node's https address (optional)" hint="Its tunnel or proxy address, for example https://storage.example.com. The installer asks for it too.">
            <input className="input" value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://" />
          </Field>
          <Field label="Valid for">
            <select className="input" value={hours} onChange={(e) => setHours(Number(e.target.value))}>
              {[1, 24, 72, 168].map((h) => (
                <option key={h} value={h}>
                  {h < 24 ? `${h} hour` : `${h / 24} day${h > 24 ? 's' : ''}`}
                </option>
              ))}
            </select>
          </Field>
        </div>
      </Modal>

      <Modal open={!!made} onClose={() => setMade(null)} title="Send these to the node's owner" footer={<Button onClick={() => setMade(null)}>Done</Button>}>
        {made && (
          <div className="space-y-4 text-sm">
            <p className="text-slate-600 dark:text-slate-300">The secret is shown only once and works once. Send it privately; the installer asks for it with hidden input.</p>
            <div>
              <div className="label">1. Run on the node (Linux, as root)</div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 rounded-lg bg-slate-100 p-2 text-xs break-all dark:bg-slate-800">{made.command}</code>
                <CopyButton text={made.command} />
              </div>
            </div>
            <div>
              <div className="label">2. Pairing secret</div>
              <div className="flex items-center gap-2">
                <code className="min-w-0 flex-1 rounded-lg bg-slate-100 p-2 text-xs break-all dark:bg-slate-800">{made.secret}</code>
                <CopyButton text={made.secret} />
              </div>
            </div>
            <p className="text-xs text-slate-500">The installer installs what it needs, asks for this server&apos;s https address, the secret, the folder on the disk to use and the node&apos;s own https address (its Cloudflare tunnel).</p>
          </div>
        )}
      </Modal>
    </section>
  )
}

/** Which users' files a node keeps, and moving them there. */
export function RemoteNodePanel({ nodeId, nodeName, onClose }: { nodeId: number; nodeName: string; onClose: () => void }) {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const info = useQuery({ queryKey: ['node-remote', nodeId], queryFn: () => get<RemoteInfo>(`/admin/nodes/${nodeId}/remote`), refetchInterval: 3_000 })
  const users = useQuery({ queryKey: ['admin-users'], queryFn: () => get<{ users: { id: number; username: string; display_name: string }[] }>('/admin/users') })
  const [pick, setPick] = useState(0)
  const d = info.data
  const assigned = new Set(d?.users.map((u) => u.id))
  const refresh = () => qc.invalidateQueries({ queryKey: ['node-remote', nodeId] })

  const assign = async (userId: number, node: number | null) => {
    try {
      await put(`/admin/users/${userId}/upload-origin`, { node_id: node })
      if (node) await post(`/admin/users/${userId}/move-to-node`, {}).catch((e) => toast('warning', 'Assigned, but the move has not started', errorMessage(e)))
      refresh()
    } catch (e) {
      toast('error', 'Not changed', errorMessage(e))
    }
  }
  const files = (k: string) => d?.stats.files[k] ?? 0
  const bytes = (k: string) => d?.stats.bytes[k] ?? 0
  return (
    <Modal open onClose={onClose} size="lg" title={`Users' files on ${nodeName}`} footer={<Button onClick={onClose}>Close</Button>}>
      {!d ? (
        <p className="text-sm text-slate-500">Loading…</p>
      ) : (
        <div className="space-y-5 text-sm">
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={d.ready ? 'green' : 'amber'}>{d.ready ? 'ready' : 'not ready'}</Badge>
            {!d.ready && <span className="text-slate-500">{d.reason}</span>}
            {d.key_id && <span className="ml-auto font-mono text-xs text-slate-400">key {d.key_id}</span>}
          </div>
          <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
            <div className="card p-3">
              <div className="text-xs text-slate-500">On the node</div>
              <div className="font-semibold">{files('ok')} files</div>
              <div className="text-xs text-slate-500">{formatBytes(bytes('ok'))}</div>
            </div>
            <div className="card p-3">
              <div className="text-xs text-slate-500">Still here</div>
              <div className="font-semibold">{d.stats.local_files} files</div>
              <div className="text-xs text-slate-500">{formatBytes(d.stats.local_bytes)}</div>
            </div>
            <div className="card p-3">
              <div className="text-xs text-slate-500">Moving</div>
              <div className="font-semibold">{files('moving')}</div>
            </div>
            <div className="card p-3">
              <div className="text-xs text-slate-500">Problems</div>
              <div className={files('missing') ? 'font-semibold text-rose-600' : 'font-semibold'}>{files('missing')}</div>
              <div className="text-xs text-slate-500">missing or damaged</div>
            </div>
          </div>
          <div>
            <div className="mb-2 font-medium">Users whose files it keeps</div>
            {d.users.length === 0 && <p className="text-slate-500">Nobody yet.</p>}
            <ul className="divide-y divide-slate-100 dark:divide-slate-800">
              {d.users.map((u) => {
                const m = d.moving[String(u.id)]
                return (
                  <li key={u.id} className="space-y-1 py-2">
                    <div className="flex items-center gap-2">
                      <span className="flex-1 font-medium">{u.display_name || u.username}</span>
                      <Button size="sm" variant="ghost" disabled={m?.running} onClick={() => post(`/admin/users/${u.id}/move-to-node`, {}).then(refresh).catch((e) => toast('error', errorMessage(e)))}>
                        Move now
                      </Button>
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-rose-600"
                        onClick={async () => {
                          if (!(await confirm({ title: `Stop keeping ${u.username}'s files on ${nodeName}?`, message: 'New files stay on this server. Files already moved stay on the node and keep working.', confirmLabel: 'Stop', tone: 'primary' }))) return
                          assign(u.id, null)
                        }}
                      >
                        Remove
                      </Button>
                    </div>
                    {m && (
                      <div className="text-xs text-slate-500">
                        {m.running ? 'Moving' : 'Last move'}: {m.moved} moved ({formatBytes(m.bytes)}){m.failed ? `, ${m.failed} failed` : ''}
                        {m.error && <span className="text-rose-600"> · {m.error}</span>}
                        {m.running && <ProgressBar indeterminate value={0} className="mt-1" />}
                      </div>
                    )}
                  </li>
                )
              })}
            </ul>
          </div>
          <div className="flex items-end gap-2 border-t border-slate-200 pt-4 dark:border-slate-700">
            <Field label="Keep a user's files on this node">
              <select className="input" value={pick} onChange={(e) => setPick(Number(e.target.value))}>
                <option value={0}>Choose a user…</option>
                {(users.data?.users ?? [])
                  .filter((u) => !assigned.has(u.id))
                  .map((u) => (
                    <option key={u.id} value={u.id}>
                      {u.display_name || u.username}
                    </option>
                  ))}
              </select>
            </Field>
            <Button icon={<UserPlus className="size-4" />} disabled={!pick || !d.ready} onClick={() => assign(pick, nodeId).then(() => setPick(0))}>
              Add and move files
            </Button>
          </div>
          <p className="text-xs text-slate-500">
            Their existing files are moved now and new uploads afterwards (every 10 minutes): each file is copied encrypted, checked, then removed here. Playback streams through this server, which decrypts in memory. While the node is offline, its files can&apos;t be opened.
          </p>
        </div>
      )}
    </Modal>
  )
}

