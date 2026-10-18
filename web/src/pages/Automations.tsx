import { useState } from 'react'
import { Link, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { BookOpen, Copy, Download, FileJson, LayoutTemplate, MoreHorizontal, Pencil, Plus, Trash2, Workflow } from 'lucide-react'
import { del, errorMessage, get, post, put } from '../lib/api'
import { timeAgo } from '../lib/format'
import { Badge, Button, EmptyState, ErrorState, IconButton, Menu, PageHeader, PageLoader, Toggle, useUI } from '../components/ui'
import { ImportModal, ReferenceModal } from '../automation/Modals'
import { useReference } from '../automation/model'
import { kindIcons } from '../automation/nodes'

export default function AutomationsPage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const list = useQuery({ queryKey: ['automations'], queryFn: () => get('/automations') })
  const templates = useQuery({ queryKey: ['automation-templates'], queryFn: () => get('/automations/templates') })
  const { data: ref_ } = useReference()
  const [showImport, setShowImport] = useState(false)
  const [showRef, setShowRef] = useState(false)
  const title = (kind: string) => ref_ && [...ref_.catalog.trigger, ...ref_.catalog.action].find((k) => k.kind === kind)?.title

  const refresh = () => {
    qc.invalidateQueries({ queryKey: ['automations'] })
    qc.invalidateQueries({ queryKey: ['automation-templates'] })
  }

  const toggle = async (a: any, enabled: boolean) => {
    try {
      const full = await get(`/automations/${a.id}`)
      await put(`/automations/${a.id}`, { definition: { ...full.definition, enabled }, is_template: false })
      refresh()
    } catch (e) {
      toast('error', enabled ? 'Cannot enable' : 'Cannot disable', errorMessage(e))
    }
  }
  const duplicate = async (a: any) => {
    const full = await get(`/automations/${a.id}`)
    await post('/automations', { definition: { ...full.definition, name: `${full.definition.name} (copy)`, enabled: false }, is_template: false })
    refresh()
  }
  const remove = async (a: any, isTemplate = false) => {
    if (!(await confirm({ title: `Delete ${isTemplate ? 'template' : 'automation'} "${a.name}"?`, message: 'This cannot be undone. Export it first if you may need it again.', confirmLabel: 'Delete' }))) return
    await del(`/automations/${a.id}`)
    refresh()
  }

  return (
    <div>
      <PageHeader
        title="Automations"
        description="Visual workflows that decide what gets uploaded, where it goes and who hears about it."
        icon={<Workflow className="size-5" />}
        actions={
          <>
            <Button icon={<BookOpen className="size-4" />} onClick={() => setShowRef(true)}>
              Variable reference
            </Button>
            <Button icon={<FileJson className="size-4" />} onClick={() => setShowImport(true)}>
              Import JSON
            </Button>
            <Link to="/automations/new">
              <Button variant="primary" icon={<Plus className="size-4" />}>
                New automation
              </Button>
            </Link>
          </>
        }
      />

      {list.isLoading ? (
        <PageLoader />
      ) : list.error ? (
        <ErrorState error={list.error} onRetry={() => list.refetch()} />
      ) : list.data.automations.length === 0 ? (
        <div className="card mb-8">
          <EmptyState icon={<Workflow className="size-6" />} title="No automations yet" description="Start from a template below, build one visually, or import JSON written by you or an AI assistant." />
        </div>
      ) : (
        <div className="mb-10 grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {list.data.automations.map((a: any) => (
            <div key={a.id} className="card flex flex-col p-5">
              <div className="flex items-start gap-3">
                <div className="min-w-0 flex-1">
                  <Link to={`/automations/${a.id}`} className="font-semibold hover:text-brand-600 hover:underline">
                    {a.name}
                  </Link>
                  <div className="mt-1 flex flex-wrap gap-1.5">
                    {!a.valid ? <Badge tone="red">draft · has errors</Badge> : a.enabled ? <Badge tone="green">enabled</Badge> : <Badge>disabled</Badge>}
                    {a.priority !== 0 && <Badge tone="blue">priority {a.priority}</Badge>}
                  </div>
                </div>
                <Toggle checked={a.enabled} disabled={!a.valid} onChange={(v) => toggle(a, v)} />
                <Menu
                  trigger={(o) => (
                    <IconButton label="More" onClick={o}>
                      <MoreHorizontal className="size-4" />
                    </IconButton>
                  )}
                  items={[
                    { label: 'Edit', icon: <Pencil className="size-4" />, onClick: () => navigate(`/automations/${a.id}`) },
                    { label: 'Duplicate', icon: <Copy className="size-4" />, onClick: () => duplicate(a) },
                    { label: 'Export JSON', icon: <Download className="size-4" />, onClick: () => (window.location.href = `/api/v1/automations/${a.id}/export`) },
                    'divider',
                    { label: 'Delete', icon: <Trash2 className="size-4" />, danger: true, onClick: () => remove(a) },
                  ]}
                />
              </div>
              {a.description && <p className="mt-2 line-clamp-2 text-sm text-slate-500">{a.description}</p>}
              <div className="mt-4 flex flex-wrap items-center gap-1.5 text-xs">
                {a.triggers.map((t: string, i: number) => {
                  const I = kindIcons[t]
                  return (
                    <span key={'t' + i} className="inline-flex items-center gap-1 rounded-md bg-brand-50 px-1.5 py-0.5 text-brand-700 dark:bg-brand-500/10 dark:text-brand-300">
                      {I && <I className="size-3" />}
                      {title(t) ?? t}
                    </span>
                  )
                })}
                <span className="text-slate-400">→</span>
                {[...new Set<string>(a.actions)].map((t: string) => {
                  const I = kindIcons[t]
                  return (
                    <span key={t} className="inline-flex items-center gap-1 rounded-md bg-emerald-50 px-1.5 py-0.5 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300">
                      {I && <I className="size-3" />}
                      {title(t) ?? t}
                    </span>
                  )
                })}
              </div>
              <div className="mt-auto pt-4 text-xs text-slate-400">Updated {timeAgo(a.updated_at)}</div>
            </div>
          ))}
        </div>
      )}

      <h2 className="mb-3 flex items-center gap-2 text-sm font-semibold tracking-wide text-slate-500 uppercase">
        <LayoutTemplate className="size-4" /> Templates
      </h2>
      {templates.isLoading ? (
        <PageLoader />
      ) : (
        <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-3">
          {[...(templates.data?.builtin ?? []).map((t: any) => ({ ...t, builtin: true })), ...(templates.data?.user ?? [])].map((t: any) => (
            <div key={t.id} className="card flex flex-col p-5">
              <div className="flex items-start justify-between gap-2">
                <span className="font-semibold">{t.name}</span>
                {t.builtin ? <Badge tone="violet">built-in</Badge> : <Badge tone="blue">yours</Badge>}
              </div>
              <p className="mt-1 flex-1 text-sm text-slate-500">{t.description || t.definition?.description || 'Saved template'}</p>
              <div className="mt-4 flex gap-2">
                <Button size="sm" variant="primary" onClick={() => navigate('/automations/new', { state: { definition: { ...(t.automation ?? t.definition), enabled: false } } })}>
                  Use template
                </Button>
                {!t.builtin && (
                  <>
                    <Button size="sm" onClick={() => navigate(`/automations/${t.id}`)}>
                      Edit
                    </Button>
                    <Button size="sm" variant="ghost" className="text-rose-600" onClick={() => remove(t, true)}>
                      Delete
                    </Button>
                  </>
                )}
              </div>
            </div>
          ))}
        </div>
      )}

      <ImportModal
        open={showImport}
        onClose={() => setShowImport(false)}
        onImport={(a) => {
          setShowImport(false)
          navigate('/automations/new', { state: { definition: a } })
        }}
      />
      <ReferenceModal open={showRef} onClose={() => setShowRef(false)} ref_={ref_} />
    </div>
  )
}
