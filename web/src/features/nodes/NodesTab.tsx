import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Copy, FolderPlus, Server, SquareTerminal, X } from 'lucide-react'
import { del, errorMessage, get, patch, post } from '../../lib/api'
import { formatBytes, formatDate, timeAgo } from '../../lib/format'
import { Badge, Button, EmptyState, ErrorState, Field, PageLoader, Toggle, useUI } from '../../components/ui'
import { NodeTerminal } from './Terminal'

interface StorageNode {
  id: number
  name: string
  status: 'pending' | 'approved' | 'rejected' | 'revoked'
  state: 'pending' | 'approved' | 'rejected' | 'revoked' | 'expired'
  hostname: string
  reported_ip: string
  request_ip: string
  request_location: string
  os: string
  arch: string
  port: number
  proxy: string
  public_url: string
  version: string
  auto_update: boolean
  update_requested: boolean
  update_available: boolean
  disk_total: number
  disk_free: number
  last_ip: string
  last_seen_at: number | null
  last_error: string
  online: boolean
  terminal_enabled: boolean
  storage_roots: string
  expires_at: number
  created_at: number
}

const INSTALL_CMD = 'curl -fsSL https://raw.githubusercontent.com/ASPEC-RACING-PTY-LTD/vad-server/main/install.sh | sudo bash -s -- install-node'

export default function NodesTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const [busy, setBusy] = useState<string | null>(null)
  const [terminal, setTerminal] = useState<StorageNode | null>(null)
  const { data, isLoading, error, refetch } = useQuery({
    queryKey: ['admin-nodes'],
    queryFn: () => get<{ items: StorageNode[]; server_version: string }>('/admin/nodes'),
    refetchInterval: 5_000,
  })
  if (isLoading) return <PageLoader />
  if (error || !data) return <ErrorState error={error} onRetry={refetch} />

  const act = async (key: string, fn: () => Promise<unknown>, done?: string) => {
    setBusy(key)
    try {
      await fn()
      if (done) toast('success', done)
      await qc.invalidateQueries({ queryKey: ['admin-nodes'] })
    } catch (e) {
      toast('error', errorMessage(e))
    } finally {
      setBusy(null)
    }
  }
  const pending = data.items.filter((n) => n.state === 'pending')
  const others = data.items.filter((n) => n.state !== 'pending')

  return (
    <div className="space-y-6">
      <section className="card space-y-3 p-5">
        <h2 className="font-semibold">Add a storage node</h2>
        <p className="text-sm text-slate-500">
          Run this on the other machine (Linux with systemd, as root) in the folder it should live in. It installs only the node agent into <code>./vad-node</code>, asks for this server&apos;s address (
          <code>{window.location.origin}</code>), then sends a join request that appears here for one hour.
        </p>
        <div className="flex items-center gap-2">
          <code className="min-w-0 flex-1 overflow-x-auto rounded-md bg-slate-100 px-3 py-2 text-xs whitespace-nowrap dark:bg-slate-800">{INSTALL_CMD}</code>
          <Button
            size="sm"
            icon={<Copy className="size-4" />}
            onClick={() => {
              void navigator.clipboard.writeText(INSTALL_CMD).then(() => toast('success', 'Command copied'))
            }}
          >
            Copy
          </Button>
        </div>
      </section>

      {pending.length > 0 && (
        <section className="space-y-3">
          <h2 className="font-semibold">Join requests</h2>
          {pending.map((n) => (
            <div key={n.id} className="card flex flex-wrap items-start gap-4 border-amber-300 p-4 dark:border-amber-700">
              <div className="min-w-0 flex-1 space-y-1 text-sm">
                <div className="flex items-center gap-2">
                  <span className="font-medium">{n.name}</span>
                  <Badge tone="amber">waiting for approval</Badge>
                </div>
                <dl className="grid grid-cols-[auto_1fr] gap-x-4 gap-y-0.5 text-xs text-slate-500">
                  <dt>Machine IP</dt>
                  <dd className="font-mono">{n.reported_ip || '-'}</dd>
                  <dt>Request came from</dt>
                  <dd>
                    <span className="font-mono">{n.request_ip}</span> {n.request_location && `(${n.request_location})`}
                  </dd>
                  <dt>Host</dt>
                  <dd>
                    {n.hostname || '-'} · {n.os}/{n.arch} · port {n.port || '-'}
                  </dd>
                  <dt>Reached via</dt>
                  <dd>
                    {n.proxy === 'none' ? 'direct' : n.proxy}
                    {n.public_url && ` · ${n.public_url}`}
                  </dd>
                  <dt>Requested</dt>
                  <dd>{formatDate(n.created_at)}</dd>
                  <dt>Expires</dt>
                  <dd>{timeAgo(n.expires_at)}</dd>
                </dl>
              </div>
              <div className="flex gap-2">
                <Button
                  size="sm"
                  variant="primary"
                  icon={<Check className="size-4" />}
                  loading={busy === `approve-${n.id}`}
                  onClick={() => act(`approve-${n.id}`, () => post(`/admin/nodes/${n.id}/approve`), `${n.name} approved`)}
                >
                  Approve
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  icon={<X className="size-4" />}
                  loading={busy === `reject-${n.id}`}
                  onClick={() => act(`reject-${n.id}`, () => post(`/admin/nodes/${n.id}/reject`), `${n.name} rejected`)}
                >
                  Reject
                </Button>
              </div>
            </div>
          ))}
        </section>
      )}

      <section className="card overflow-x-auto">
        {others.length === 0 ? (
          <EmptyState icon={<Server className="size-6" />} title="No storage nodes yet" description="Install a node on another machine with the command above." />
        ) : (
          <table className="w-full text-sm">
            <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
              <tr>
                <th className="px-4 py-2.5">Node</th>
                <th className="px-4 py-2.5">Status</th>
                <th className="hidden px-4 py-2.5 md:table-cell">Version</th>
                <th className="hidden px-4 py-2.5 lg:table-cell">Disk</th>
                <th className="hidden px-4 py-2.5 md:table-cell">Auto-update</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {others.map((n) => (
                <tr key={n.id} className="border-b border-slate-100 align-top last:border-0 dark:border-slate-800">
                  <td className="px-4 py-2.5">
                    <div className="font-medium">{n.name}</div>
                    <div className="text-xs text-slate-500">
                      {n.hostname || '-'} · {n.reported_ip || n.last_ip || '-'}
                      {n.port ? `:${n.port}` : ''} · {n.proxy === 'none' ? 'direct' : n.proxy}
                    </div>
                    {n.last_error && <div className="text-xs text-rose-600">{n.last_error}</div>}
                  </td>
                  <td className="px-4 py-2.5">
                    {n.state === 'approved' ? (
                      <>
                        <Badge tone={n.online ? 'green' : 'red'}>{n.online ? 'online' : 'offline'}</Badge>
                        <div className="mt-1 text-xs text-slate-500">seen {timeAgo(n.last_seen_at)}</div>
                      </>
                    ) : (
                      <Badge tone="slate">{n.state}</Badge>
                    )}
                  </td>
                  <td className="hidden px-4 py-2.5 md:table-cell">
                    <div className="font-mono text-xs">{n.version || '-'}</div>
                    {n.update_requested ? (
                      <Badge tone="blue">updating</Badge>
                    ) : (
                      n.update_available && <Badge tone="amber">update to {data.server_version}</Badge>
                    )}
                  </td>
                  <td className="hidden px-4 py-2.5 text-xs text-slate-500 lg:table-cell">{n.disk_total ? `${formatBytes(n.disk_free)} free of ${formatBytes(n.disk_total)}` : '-'}</td>
                  <td className="hidden px-4 py-2.5 md:table-cell">
                    {n.state === 'approved' && (
                      <Toggle
                        checked={n.auto_update}
                        disabled={busy === `auto-${n.id}`}
                        onChange={(v) => act(`auto-${n.id}`, () => patch(`/admin/nodes/${n.id}`, { auto_update: v }))}
                      />
                    )}
                  </td>
                  <td className="px-4 py-2.5 text-right whitespace-nowrap">
                    {n.state === 'approved' && n.terminal_enabled && (
                      <Button size="sm" variant="ghost" icon={<SquareTerminal className="size-4" />} onClick={() => setTerminal(n)}>
                        Terminal
                      </Button>
                    )}
                    {n.state === 'approved' && n.update_available && !n.update_requested && (
                      <Button size="sm" variant="ghost" loading={busy === `update-${n.id}`} onClick={() => act(`update-${n.id}`, () => post(`/admin/nodes/${n.id}/update`), `${n.name} updates on its next check-in`)}>
                        Update now
                      </Button>
                    )}
                    {n.state === 'approved' ? (
                      <Button
                        size="sm"
                        variant="ghost"
                        className="text-rose-600"
                        onClick={async () => {
                          if (!(await confirm({ title: `Revoke ${n.name}?`, message: 'The node loses access immediately. It sends a new join request, which you can approve or reject.', confirmLabel: 'Revoke' }))) return
                          await act(`revoke-${n.id}`, () => post(`/admin/nodes/${n.id}/revoke`), `${n.name} revoked`)
                        }}
                      >
                        Revoke
                      </Button>
                    ) : (
                      <Button size="sm" variant="ghost" onClick={() => act(`delete-${n.id}`, () => del(`/admin/nodes/${n.id}`))}>
                        Delete
                      </Button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <NodeLocations nodes={data.items.filter((n) => n.state === 'approved')} />
      {terminal && <NodeTerminal nodeId={terminal.id} nodeName={terminal.name} onClose={() => setTerminal(null)} />}
    </div>
  )
}

interface NodeLocation {
  id: number
  node_id: number
  node_name: string
  name: string
  path: string
  status: 'preparing' | 'ok' | 'error'
  error: string
  disk_total: number
  disk_free: number
}

/** Folders on nodes that hold copies (targets for copy rules). */
function NodeLocations({ nodes }: { nodes: StorageNode[] }) {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data } = useQuery({ queryKey: ['admin-node-locations'], queryFn: () => get<{ items: NodeLocation[] }>('/admin/node-locations'), refetchInterval: 5_000 })
  const [nodeId, setNodeId] = useState<number | ''>('')
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const [saving, setSaving] = useState(false)
  const node = nodes.find((n) => n.id === nodeId)
  const roots = (node?.storage_roots ?? '').split('\n').filter(Boolean)
  const refresh = () => qc.invalidateQueries({ queryKey: ['admin-node-locations'] })

  return (
    <section className="card space-y-4 p-5">
      <div>
        <h2 className="font-semibold">Storage on nodes</h2>
        <p className="text-sm text-slate-500">Folders on nodes that keep copies of files (see Admin &gt; Copies). Copies are encrypted before they leave this server, so the node&apos;s owner cannot read them.</p>
      </div>
      {(data?.items ?? []).length > 0 && (
        <ul className="divide-y divide-slate-100 rounded-md border border-slate-200 dark:divide-slate-800 dark:border-slate-800">
          {data!.items.map((l) => (
            <li key={l.id} className="flex flex-wrap items-center gap-3 px-3 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="font-medium">
                  {l.name} <span className="font-normal text-slate-500">on {l.node_name}</span>
                </div>
                <div className="font-mono text-xs text-slate-500">{l.path}</div>
                {l.error && <div className="text-xs text-rose-600">{l.error}</div>}
              </div>
              <Badge tone={l.status === 'ok' ? 'green' : l.status === 'error' ? 'red' : 'amber'}>{l.status}</Badge>
              <span className="text-xs text-slate-500">{l.disk_total ? `${formatBytes(l.disk_free)} free of ${formatBytes(l.disk_total)}` : ''}</span>
              {l.status !== 'ok' && (
                <Button size="sm" variant="ghost" onClick={() => post(`/admin/node-locations/${l.id}/prepare`).then(refresh, (e) => toast('error', errorMessage(e)))}>
                  Try again
                </Button>
              )}
              <Button
                size="sm"
                variant="ghost"
                className="text-rose-600"
                onClick={async () => {
                  if (!(await confirm({ title: `Remove ${l.name}?`, message: 'Only possible when no copies are stored there. Files on the node are not deleted.', confirmLabel: 'Remove' }))) return
                  try {
                    await del(`/admin/node-locations/${l.id}`)
                    await refresh()
                  } catch (e) {
                    toast('error', errorMessage(e))
                  }
                }}
              >
                Remove
              </Button>
            </li>
          ))}
        </ul>
      )}
      {nodes.length === 0 ? (
        <p className="text-sm text-slate-500">Approve a node first.</p>
      ) : (
        <form
          className="grid gap-3 md:grid-cols-[1fr_1fr_2fr_auto] md:items-end"
          onSubmit={async (e) => {
            e.preventDefault()
            setSaving(true)
            try {
              await post('/admin/node-locations', { node_id: nodeId, name, path })
              setName('')
              setPath('')
              toast('success', 'Location added; the node is preparing the folder')
              await refresh()
            } catch (err) {
              toast('error', errorMessage(err))
            } finally {
              setSaving(false)
            }
          }}
        >
          <Field label="Node">
            <select
              className="input"
              value={nodeId}
              onChange={(e) => {
                const id = Number(e.target.value) || ''
                setNodeId(id)
                const r = (nodes.find((n) => n.id === id)?.storage_roots ?? '').split('\n').filter(Boolean)
                if (!path && r[0]) setPath(r[0])
              }}
            >
              <option value="">Choose…</option>
              {nodes.map((n) => (
                <option key={n.id} value={n.id}>
                  {n.name}
                </option>
              ))}
            </select>
          </Field>
          <Field label="Name">
            <input className="input" value={name} onChange={(e) => setName(e.target.value)} placeholder="Friend's tank" />
          </Field>
          <Field label="Folder on the node" hint={roots.length ? `Allowed: ${roots.join(', ')}` : undefined}>
            <input className="input font-mono" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/srv/vad-node/storage" />
          </Field>
          <Button type="submit" variant="primary" icon={<FolderPlus className="size-4" />} loading={saving} disabled={!nodeId || !name || !path}>
            Add
          </Button>
        </form>
      )}
    </section>
  )
}
