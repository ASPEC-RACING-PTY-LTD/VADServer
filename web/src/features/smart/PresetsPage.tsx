import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Copy, Pencil, SlidersHorizontal, Trash2 } from 'lucide-react'
import { Badge, Button, ErrorState, Field, Modal, PageHeader, PageLoader, useUI } from '../../components/ui'
import { errorMessage } from '../../lib/api'
import { categoryLabel, smartApi, smartKeys, usePresets, useSmartStatus, type Filter, type Preset } from './api'
import { CapabilityReport, ContextEditor, DescribeBox, emptyFilter, FilterEditor, useFilterCheck } from './shared'

/** Built-in and saved detection presets, with a builder that shows what each condition needs. */
export default function PresetsPage() {
  const presets = usePresets()
  const status = useSmartStatus()
  const qc = useQueryClient()
  const { confirm, toast } = useUI()
  const [editing, setEditing] = useState<{ id: string | null; name: string; description: string; source: string; filter: Filter } | null>(null)
  if (presets.isLoading) return <PageLoader />
  if (presets.error) return <ErrorState error={presets.error} onRetry={() => presets.refetch()} />
  const list = presets.data?.presets ?? []

  const edit = (p: Preset, copy: boolean) => setEditing({ id: copy || p.builtin ? null : p.id, name: copy || p.builtin ? `${p.name} (copy)` : p.name, description: p.description, source: p.source ?? '', filter: p.filter })
  const remove = async (p: Preset) => {
    if (!(await confirm({ title: `Delete "${p.name}"?`, message: 'Queues already using it keep their conditions.', confirmLabel: 'Delete preset' }))) return
    try {
      await smartApi.deletePreset(p.id)
      qc.invalidateQueries({ queryKey: smartKeys.sub('presets') })
    } catch (e) {
      toast('error', 'Not deleted', errorMessage(e))
    }
  }
  return (
    <div className="space-y-5">
      <PageHeader
        title="Detection presets"
        icon={<SlidersHorizontal className="size-5" />}
        description="Each preset is a combination of real detectors. Conditions whose models are not installed are listed and skipped, never replaced with something else."
        actions={
          <Button variant="primary" onClick={() => setEditing({ id: null, name: '', description: '', source: '', filter: emptyFilter() })}>
            New preset
          </Button>
        }
      />
      <div className="card p-4">
        <h2 className="mb-2 text-sm font-semibold">Detectors on this server</h2>
        <div className="grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {(status.data?.detectors ?? []).map((d) => (
            <div key={d.id} className="text-xs">
              <Badge tone={d.available ? 'green' : 'slate'}>{d.available ? 'available' : 'not installed'}</Badge> <span className="font-medium">{d.title}</span>
              <div className="text-slate-500">{d.available ? d.description : d.reason}</div>
            </div>
          ))}
        </div>
      </div>
      <div className="grid gap-4 lg:grid-cols-2">
        {list.map((p) => (
          <div key={p.id} className="card space-y-3 p-4">
            <div className="flex items-start gap-2">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2 font-semibold">
                  {p.name} {p.builtin ? <Badge tone="slate">built-in</Badge> : <Badge tone="blue">mine</Badge>}
                </div>
                <p className="text-sm text-slate-500">{p.description}</p>
              </div>
              <Button size="sm" variant="ghost" icon={p.builtin ? <Copy className="size-3.5" /> : <Pencil className="size-3.5" />} onClick={() => edit(p, false)}>
                {p.builtin ? 'Customise' : 'Edit'}
              </Button>
              {!p.builtin && (
                <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={() => remove(p)} aria-label={`Delete ${p.name}`} />
              )}
            </div>
            <ul className="text-sm">
              {p.filter.rules.map((r) => (
                <li key={r.id}>
                  <Badge tone="slate">{categoryLabel[r.category] ?? r.category}</Badge> {r.name}
                </li>
              ))}
            </ul>
            <details>
              <summary className="cursor-pointer text-xs text-slate-500">What it needs</summary>
              <div className="mt-2">
                <CapabilityReport components={p.components} compact />
              </div>
            </details>
          </div>
        ))}
      </div>
      {editing && <PresetEditor init={editing} onClose={() => setEditing(null)} />}
    </div>
  )
}

function PresetEditor({ init, onClose }: { init: { id: string | null; name: string; description: string; source: string; filter: Filter }; onClose: () => void }) {
  const [v, setV] = useState(init)
  const [busy, setBusy] = useState(false)
  const check = useFilterCheck()
  const qc = useQueryClient()
  const { toast } = useUI()
  const save = async () => {
    setBusy(true)
    try {
      await smartApi.savePreset(v.id, { name: v.name, description: v.description, source: v.source, filter: v.filter })
      qc.invalidateQueries({ queryKey: smartKeys.sub('presets') })
      toast('success', 'Preset saved')
      onClose()
    } catch (e) {
      toast('error', 'Not saved', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const setFilter = (f: Filter) => {
    setV({ ...v, filter: f })
    if (f.rules.length) check.check(f)
  }
  return (
    <Modal
      open
      size="xl"
      onClose={onClose}
      title={v.id ? 'Edit preset' : 'New preset'}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant="primary" loading={busy} disabled={!v.name.trim() || !v.filter.rules.length} onClick={save}>
            Save preset
          </Button>
        </>
      }
    >
      <div className="space-y-5">
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Name">
            <input className="input" value={v.name} maxLength={120} onChange={(e) => setV({ ...v, name: e.target.value })} />
          </Field>
          <Field label="Description">
            <input className="input" value={v.description} maxLength={1000} onChange={(e) => setV({ ...v, description: e.target.value })} />
          </Field>
        </div>
        <DescribeBox initial={v.source} onPlan={(p) => setV({ ...v, source: p.description, filter: { ...v.filter, rules: [...v.filter.rules, ...p.filter.rules.filter((r) => !r.disabled)] } })} />
        <FilterEditor value={v.filter} onChange={setFilter} components={check.components} />
        {check.error && <p className="text-sm text-rose-600">{check.error}</p>}
        <CapabilityReport components={check.components} compact />
        <details>
          <summary className="cursor-pointer text-sm font-medium">Segment settings</summary>
          <div className="mt-3">
            <ContextEditor value={v.filter.context} onChange={(c) => setV({ ...v, filter: { ...v.filter, context: c } })} />
          </div>
        </details>
      </div>
    </Modal>
  )
}
