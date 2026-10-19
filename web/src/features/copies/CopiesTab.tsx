import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus, RefreshCw, RotateCcw, ShieldCheck } from 'lucide-react'
import { del, errorMessage, get, post, put } from '../../lib/api'
import { formatBytes, formatDate, timeAgo } from '../../lib/format'
import { Badge, Button, EmptyState, ErrorState, Field, Modal, PageLoader, Toggle, useUI } from '../../components/ui'

interface Target {
  target: string
  label: string
  kind: 'local' | 'node'
  ready: boolean
}

interface Rule {
  id: number
  name: string
  enabled: boolean
  user_id: number | null
  source_location_id: number | null
  target: string
  path_pattern: string
  extensions: string
  min_size: number
  max_size: number
  delete_delay_hours: number
}

interface Summary {
  targets: { target: string; label: string; ready: boolean; pending: number; ok: number; failed: number; deleting: number; bytes: number }[]
  primary_checked: number
  primary_bad: number
  running: boolean
  last_run: number
  last_plan: number
  wanted: number
  last_error: string
  verify_days: number
}

interface CopyRow {
  user_id: number
  username: string
  sha256: string
  target: string
  state: string
  size: number
  last_path: string
  attempts: number
  delete_at: number | null
  last_error: string
  updated_at: number
}

const MB = 1024 * 1024
const emptyRule = (): Rule => ({ id: 0, name: '', enabled: true, user_id: null, source_location_id: null, target: '', path_pattern: '', extensions: '', min_size: 0, max_size: 0, delete_delay_hours: 168 })

export default function CopiesTab() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const [editing, setEditing] = useState<Rule | null>(null)
  const summary = useQuery({ queryKey: ['copies-summary'], queryFn: () => get<Summary>('/admin/copies/summary'), refetchInterval: 5_000 })
  const rules = useQuery({ queryKey: ['copy-rules'], queryFn: () => get<{ items: Rule[] }>('/admin/copy-rules') })
  const targets = useQuery({ queryKey: ['copy-targets'], queryFn: () => get<{ items: Target[] }>('/admin/copy-targets') })
  const failed = useQuery({ queryKey: ['copies', 'failed'], queryFn: () => get<{ items: CopyRow[] }>('/admin/copies?state=failed'), refetchInterval: 10_000 })
  const deleting = useQuery({ queryKey: ['copies', 'deleting'], queryFn: () => get<{ items: CopyRow[] }>('/admin/copies?state=deleting'), refetchInterval: 15_000 })

  if (summary.isLoading || rules.isLoading || targets.isLoading) return <PageLoader />
  if (summary.error || !summary.data) return <ErrorState error={summary.error} onRetry={summary.refetch} />
  const s = summary.data
  const tlabel = (t: string) => targets.data?.items.find((x) => x.target === t)?.label ?? t
  const refresh = () => qc.invalidateQueries({ predicate: (q) => ['copies-summary', 'copy-rules', 'copies'].includes(String(q.queryKey[0])) })
  const action = async (path: string, done: string) => {
    try {
      await post(path)
      toast('success', done)
      await refresh()
    } catch (e) {
      toast('error', errorMessage(e))
    }
  }

  return (
    <div className="space-y-6">
      <section className="card space-y-3 p-5">
        <div className="flex flex-wrap items-start gap-3">
          <div className="min-w-0 flex-1">
            <h2 className="font-semibold">File copies</h2>
            <p className="text-sm text-slate-500">
              Rules keep extra copies of matching files on other storage locations or nodes. Every copy and original is checked every {s.verify_days} days; a damaged copy is made again and a damaged or
              missing original is repaired from a copy. Copies of deleted files are kept for the rule&apos;s delay so they can be recovered.
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button size="sm" icon={<RefreshCw className="size-4" />} onClick={() => action('/admin/copies/run', 'Copy run started')}>
              Run now
            </Button>
            <Button size="sm" icon={<ShieldCheck className="size-4" />} onClick={() => action('/admin/copies/verify', 'Checking every copy and original')}>
              Check everything
            </Button>
          </div>
        </div>
        <div className="text-xs text-slate-500">
          {s.running ? 'Running now · ' : ''}Last run {timeAgo(s.last_run)} · {s.wanted.toLocaleString()} copies wanted by the rules · originals checked: {s.primary_checked.toLocaleString()}
          {s.primary_bad > 0 && <span className="text-rose-600"> ({s.primary_bad} damaged or missing)</span>}
          {s.last_error && <span className="text-rose-600"> · {s.last_error}</span>}
        </div>
        {s.targets.length > 0 && (
          <table className="w-full text-sm">
            <thead className="text-left text-xs text-slate-500 uppercase">
              <tr>
                <th className="py-1.5">Target</th>
                <th className="py-1.5">Copied</th>
                <th className="py-1.5">Waiting</th>
                <th className="py-1.5">Failed</th>
                <th className="py-1.5">To delete</th>
              </tr>
            </thead>
            <tbody>
              {s.targets.map((t) => (
                <tr key={t.target} className="border-t border-slate-100 dark:border-slate-800">
                  <td className="py-1.5">
                    {t.label} {!t.ready && <Badge tone="amber">not ready</Badge>}
                  </td>
                  <td className="py-1.5">
                    {t.ok.toLocaleString()} <span className="text-xs text-slate-500">({formatBytes(t.bytes)})</span>
                  </td>
                  <td className="py-1.5">{t.pending.toLocaleString()}</td>
                  <td className={'py-1.5 ' + (t.failed ? 'text-rose-600' : '')}>{t.failed.toLocaleString()}</td>
                  <td className="py-1.5">{t.deleting.toLocaleString()}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </section>

      <section className="card overflow-hidden">
        <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
          <h2 className="font-semibold">Rules</h2>
          <Button size="sm" variant="primary" icon={<Plus className="size-4" />} onClick={() => setEditing(emptyRule())} disabled={!targets.data?.items.length}>
            New rule
          </Button>
        </div>
        {(rules.data?.items ?? []).length === 0 ? (
          <EmptyState icon={<ShieldCheck className="size-6" />} title="No copy rules yet" description='For example: "videos in /Medal stored on Default also go to the node location Friend&apos;s tank".' />
        ) : (
          <ul className="divide-y divide-slate-100 dark:divide-slate-800">
            {rules.data!.items.map((r) => (
              <li key={r.id} className="flex flex-wrap items-center gap-3 px-4 py-3 text-sm">
                <div className="min-w-0 flex-1">
                  <div className="font-medium">
                    {r.name} {!r.enabled && <Badge tone="slate">off</Badge>}
                  </div>
                  <div className="text-xs text-slate-500">
                    {r.path_pattern || 'all files'}
                    {r.extensions && ` · ${r.extensions}`}
                    {r.source_location_id ? ` · from ${tlabel(`local:${r.source_location_id}`)}` : ''} → {tlabel(r.target)} · deleted copies kept {Math.round(r.delete_delay_hours / 24)} days
                  </div>
                </div>
                <Button size="sm" variant="ghost" onClick={() => setEditing(r)}>
                  Edit
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  className="text-rose-600"
                  onClick={async () => {
                    if (!(await confirm({ title: `Delete the rule ${r.name}?`, message: `Its copies are deleted after ${Math.round(r.delete_delay_hours / 24)} days unless another rule wants them.`, confirmLabel: 'Delete rule' }))) return
                    try {
                      await del(`/admin/copy-rules/${r.id}`)
                      await refresh()
                    } catch (e) {
                      toast('error', errorMessage(e))
                    }
                  }}
                >
                  Delete
                </Button>
              </li>
            ))}
          </ul>
        )}
      </section>

      {(failed.data?.items ?? []).length > 0 && (
        <section className="card overflow-hidden">
          <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <h2 className="font-semibold">Problems</h2>
            <Button size="sm" icon={<RotateCcw className="size-4" />} onClick={() => action('/admin/copies/retry', 'Retrying failed copies')}>
              Retry all
            </Button>
          </div>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {failed.data!.items.map((c) => (
              <li key={`${c.user_id}-${c.sha256}-${c.target}`} className="px-4 py-2">
                <div className="truncate font-medium">{c.last_path || c.sha256}</div>
                <div className="text-xs text-slate-500">
                  {c.username} → {tlabel(c.target)} · {c.attempts} attempts · {timeAgo(c.updated_at)}
                </div>
                <div className="text-xs text-rose-600">{c.last_error}</div>
              </li>
            ))}
          </ul>
        </section>
      )}

      {(deleting.data?.items ?? []).length > 0 && (
        <section className="card overflow-hidden">
          <div className="border-b border-slate-200 px-4 py-3 dark:border-slate-800">
            <h2 className="font-semibold">Copies waiting to be deleted</h2>
            <p className="text-xs text-slate-500">The original was deleted (or no rule wants the copy any more). Recover puts the file back into its owner&apos;s /Recovered folder.</p>
          </div>
          <ul className="divide-y divide-slate-100 text-sm dark:divide-slate-800">
            {deleting.data!.items.map((c) => (
              <li key={`${c.user_id}-${c.sha256}-${c.target}`} className="flex items-center gap-3 px-4 py-2">
                <div className="min-w-0 flex-1">
                  <div className="truncate font-medium">{c.last_path || c.sha256}</div>
                  <div className="text-xs text-slate-500">
                    {c.username} · {tlabel(c.target)} · {formatBytes(c.size)} · deleted {c.delete_at ? formatDate(c.delete_at) : 'soon'}
                  </div>
                </div>
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={async () => {
                    try {
                      await post('/admin/copies/recover', { user_id: c.user_id, sha256: c.sha256, target: c.target })
                      toast('success', 'Recovering; you get a notification when it is done')
                    } catch (e) {
                      toast('error', errorMessage(e))
                    }
                  }}
                >
                  Recover
                </Button>
              </li>
            ))}
          </ul>
        </section>
      )}

      {editing && <RuleEditor rule={editing} targets={targets.data?.items ?? []} onClose={() => setEditing(null)} onSaved={refresh} />}
    </div>
  )
}

function RuleEditor({ rule, targets, onClose, onSaved }: { rule: Rule; targets: Target[]; onClose: () => void; onSaved: () => void }) {
  const { toast } = useUI()
  const [r, setR] = useState<Rule>(rule)
  const [saving, setSaving] = useState(false)
  const users = useQuery({ queryKey: ['admin-users'], queryFn: () => get<{ users: { id: number; username: string }[] }>('/admin/users') })
  const set = (p: Partial<Rule>) => setR((x) => ({ ...x, ...p }))
  const locals = targets.filter((t) => t.kind === 'local')
  const save = async () => {
    setSaving(true)
    try {
      if (r.id) await put(`/admin/copy-rules/${r.id}`, r)
      else await post('/admin/copy-rules', r)
      toast('success', 'Rule saved; copies start on the next run')
      onSaved()
      onClose()
    } catch (e) {
      toast('error', errorMessage(e))
    } finally {
      setSaving(false)
    }
  }
  return (
    <Modal
      open
      onClose={onClose}
      size="lg"
      title={r.id ? `Edit ${rule.name}` : 'New copy rule'}
      description="Files that match get a copy on the target. Copies follow the files: renamed or moved files keep their copy; deleted files lose it after the delay."
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" loading={saving} disabled={!r.name || !r.target} onClick={save}>
            Save
          </Button>
        </>
      }
    >
      <div className="grid gap-4 md:grid-cols-2">
        <Field label="Name">
          <input className="input" value={r.name} onChange={(e) => set({ name: e.target.value })} placeholder="Videos to Friend's tank" />
        </Field>
        <Field label="Copy to">
          <select className="input" value={r.target} onChange={(e) => set({ target: e.target.value })}>
            <option value="">Choose…</option>
            {targets.map((t) => (
              <option key={t.target} value={t.target}>
                {t.label}
                {t.ready ? '' : ' (not ready)'}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Files stored on" hint="Only files whose original is on this location.">
          <select className="input" value={r.source_location_id ?? ''} onChange={(e) => set({ source_location_id: e.target.value ? Number(e.target.value) : null })}>
            <option value="">Any location</option>
            {locals.map((t) => (
              <option key={t.target} value={t.target.slice(6)}>
                {t.label}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Of user">
          <select className="input" value={r.user_id ?? ''} onChange={(e) => set({ user_id: e.target.value ? Number(e.target.value) : null })}>
            <option value="">Every user</option>
            {(users.data?.users ?? []).map((u) => (
              <option key={u.id} value={u.id}>
                {u.username}
              </option>
            ))}
          </select>
        </Field>
        <Field label="Path pattern" hint={'"/Medal/" = everything in /Medal; "*.mp4" = anywhere; "**" crosses folders. Empty = all files.'}>
          <input className="input font-mono" value={r.path_pattern} onChange={(e) => set({ path_pattern: e.target.value })} placeholder="/Medal/" />
        </Field>
        <Field label="File types" hint="Extensions, comma separated. Empty = all.">
          <input className="input" value={r.extensions} onChange={(e) => set({ extensions: e.target.value })} placeholder="mp4, mkv, mov" />
        </Field>
        <Field label="Minimum size (MB)">
          <input className="input" type="number" min={0} value={Math.round(r.min_size / MB)} onChange={(e) => set({ min_size: Math.max(0, Number(e.target.value)) * MB })} />
        </Field>
        <Field label="Maximum size (MB)" hint="0 = no limit.">
          <input className="input" type="number" min={0} value={Math.round(r.max_size / MB)} onChange={(e) => set({ max_size: Math.max(0, Number(e.target.value)) * MB })} />
        </Field>
        <Field label="Keep copies of deleted files for (days)" hint="Lets you recover a file after it was deleted by mistake.">
          <input className="input" type="number" min={0} max={365} value={Math.round(r.delete_delay_hours / 24)} onChange={(e) => set({ delete_delay_hours: Math.max(0, Number(e.target.value)) * 24 })} />
        </Field>
        <div className="flex items-end">
          <Toggle checked={r.enabled} onChange={(v) => set({ enabled: v })} label="Rule is on" />
        </div>
      </div>
    </Modal>
  )
}
