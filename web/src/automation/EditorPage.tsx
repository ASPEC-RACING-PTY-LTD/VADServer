import { useCallback, useEffect, useMemo, useRef, useState, type DragEvent } from 'react'
import { useLocation, useNavigate, useParams } from 'react-router-dom'
import { useQueryClient } from '@tanstack/react-query'
import { addEdge, Background, BackgroundVariant, Controls, MiniMap, ReactFlow, ReactFlowProvider, useEdgesState, useNodesState, useReactFlow, type Connection, type Edge as RFEdge } from '@xyflow/react'
import '@xyflow/react/dist/style.css'
import { ArrowLeft, BookOpen, Copy, Download, FileJson, FlaskConical, LayoutGrid, PanelRight, Save, Search, Trash2, X } from 'lucide-react'
import { ApiError, errorMessage, get, post, put } from '../lib/api'
import { Badge, Button, cx, ErrorState, Field, IconButton, PageLoader, Toggle, useUI } from '../components/ui'
import { autoLayout, defaultParams, edgeFor, emptyAutomation, fromFlow, newNodeID, specFor, summarize, toFlow, useReference, type Automation, type FlowNode, type KindSpec, type NodeType, type Reference, type Validation } from './model'
import { kindIcons, nodeTypes, typeStyle } from './nodes'
import { ParamsForm } from './ParamEditor'
import { DryRunModal, ImportModal, IssueList, ReferenceModal } from './Modals'

type Meta = Omit<Automation, 'nodes' | 'edges'>

function Palette({ ref_, onAdd }: { ref_: Reference; onAdd: (spec: KindSpec) => void }) {
  const [q, setQ] = useState('')
  return (
    <div className="flex h-full flex-col">
      <div className="border-b border-slate-200 p-3 dark:border-slate-800">
        <div className="relative">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400" />
          <input className="input h-8 pl-8 text-xs" placeholder="Find a step" value={q} onChange={(e) => setQ(e.target.value)} />
        </div>
        <p className="mt-2 text-[11px] text-slate-500">Drag onto the canvas or click to add.</p>
      </div>
      <div className="scroll-thin flex-1 space-y-4 overflow-y-auto p-3">
        {(['trigger', 'condition', 'action'] as NodeType[]).map((t) => {
          const items = ref_.catalog[t].filter((k) => !q || (k.title + k.kind + k.description).toLowerCase().includes(q.toLowerCase()))
          if (!items.length) return null
          const st = typeStyle[t]
          return (
            <div key={t}>
              <div className="mb-1.5 flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-slate-500 uppercase">
                <span className={cx('size-2 rounded-full', st.bar)} />
                {st.label}s
              </div>
              <div className="space-y-1">
                {items.map((k) => {
                  const Icon = kindIcons[k.kind]
                  return (
                    <button
                      key={k.kind}
                      draggable
                      onDragStart={(e) => {
                        e.dataTransfer.setData('application/x-vd-step', JSON.stringify({ type: t, kind: k.kind }))
                        e.dataTransfer.effectAllowed = 'move'
                      }}
                      onClick={() => onAdd(k)}
                      title={k.description}
                      className="flex w-full items-center gap-2 rounded-lg border border-transparent px-2 py-1.5 text-left text-xs hover:border-slate-200 hover:bg-white dark:hover:border-slate-700 dark:hover:bg-slate-800"
                    >
                      <span className={cx('flex size-6 shrink-0 items-center justify-center rounded-md', st.chip)}>{Icon && <Icon className="size-3.5" />}</span>
                      <span className="truncate font-medium text-slate-700 dark:text-slate-200">{k.title}</span>
                    </button>
                  )
                })}
              </div>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function contextFor(nodeId: string, nodes: FlowNode[], edges: RFEdge[]): string | undefined {
  const incoming: Record<string, string[]> = {}
  edges.forEach((e) => (incoming[e.target] ??= []).push(e.source))
  const seen = new Set<string>()
  const stack = [nodeId]
  const ctxs = new Set<string>()
  while (stack.length) {
    const id = stack.pop()!
    if (seen.has(id)) continue
    seen.add(id)
    const n = nodes.find((x) => x.id === id)
    if (n?.data.node.type === 'trigger' && n.data.spec?.context) ctxs.add(n.data.spec.context)
    ;(incoming[id] ?? []).forEach((s) => stack.push(s))
  }
  return ctxs.size === 1 ? [...ctxs][0] : undefined
}

function Editor() {
  const { id } = useParams()
  const location = useLocation()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data: ref_, error: refError } = useReference()
  const rf = useReactFlow()
  const wrapper = useRef<HTMLDivElement>(null)

  const [loaded, setLoaded] = useState(false)
  const [loadError, setLoadError] = useState<unknown>(null)
  const [meta, setMeta] = useState<Meta>(() => {
    const { nodes: _n, edges: _e, ...m } = emptyAutomation()
    return m
  })
  const [nodes, setNodes, onNodesChange] = useNodesState<FlowNode>([])
  const [edges, setEdges, onEdgesChange] = useEdgesState<RFEdge>([])
  const [selected, setSelected] = useState<string | null>(null)
  const [validation, setValidation] = useState<Validation | null>(null)
  const [dirty, setDirty] = useState(false)
  const [saving, setSaving] = useState(false)
  const [savedId, setSavedId] = useState<number | null>(id ? Number(id) : null)
  const [isTemplate, setIsTemplate] = useState(false)
  const [showRef, setShowRef] = useState(false)
  const [showImport, setShowImport] = useState(false)
  const [showTest, setShowTest] = useState(false)
  const [showDetails, setShowDetails] = useState(false)

  const load = useCallback(
    (a: Automation, v?: Validation) => {
      const { nodes: an, edges: ae, ...m } = a
      setMeta({ ...m, schema: m.schema || 'vaultdock.automation/v1', priority: m.priority ?? 0 })
      const flow = toFlow({ ...a, nodes: an ?? [], edges: ae ?? [] }, ref_, v)
      setNodes(flow.nodes)
      setEdges(flow.edges)
      setSelected(null)
      setTimeout(() => rf.fitView({ padding: 0.2, maxZoom: 1.1 }), 50)
    },
    [ref_, rf, setNodes, setEdges],
  )

  // initial load
  useEffect(() => {
    if (!ref_ || loaded) return
    const st = location.state as { definition?: Automation; template?: boolean } | null
    if (id) {
      get(`/automations/${id}`)
        .then((res) => {
          setIsTemplate(res.automation.is_template)
          load(res.definition, res.validation)
          setLoaded(true)
        })
        .catch((e) => setLoadError(e))
    } else if (st?.definition) {
      load(st.definition)
      setIsTemplate(!!st.template)
      setDirty(true)
      setLoaded(true)
    } else {
      const trig = specFor(ref_, 'trigger', 'file.created')!
      load({ ...emptyAutomation(), nodes: [{ id: 'trigger-1', type: 'trigger', kind: trig.kind, params: defaultParams(trig), position: { x: 0, y: 200 } }] })
      setLoaded(true)
    }
  }, [ref_, id, loaded, location.state, load])

  const current = useCallback((): Automation => fromFlow(meta, nodes, edges), [meta, nodes, edges])

  // debounced server validation
  useEffect(() => {
    if (!loaded) return
    const t = setTimeout(async () => {
      try {
        const v: Validation = await post('/automations/validate', current())
        setValidation(v)
        const errs: Record<string, string[]> = {}
        const warns: Record<string, string[]> = {}
        v.errors.forEach((e) => e.node_id && (errs[e.node_id] ??= []).push(e.message))
        v.warnings.forEach((e) => e.node_id && (warns[e.node_id] ??= []).push(e.message))
        setNodes((ns) =>
          ns.map((n) => {
            const ne = errs[n.id] ?? []
            const nw = warns[n.id] ?? []
            if (ne.join() === n.data.errors.join() && nw.join() === n.data.warnings.join()) return n
            return { ...n, data: { ...n.data, errors: ne, warnings: nw } }
          }),
        )
      } catch {
        /* network issue: keep last result */
      }
    }, 450)
    return () => clearTimeout(t)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [meta, nodes.map((n) => JSON.stringify(n.data.node)).join('|'), edges.map((e) => e.id).join('|'), loaded])

  useEffect(() => {
    if (!dirty) return
    const h = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', h)
    return () => window.removeEventListener('beforeunload', h)
  }, [dirty])

  const addNode = (spec: KindSpec, position?: { x: number; y: number }) => {
    const ids = nodes.map((n) => n.id)
    const nid = newNodeID(ids, spec.kind.split('.').pop() ?? spec.type)
    let pos = position
    if (!pos) {
      const rect = wrapper.current?.getBoundingClientRect()
      pos = rf.screenToFlowPosition({ x: (rect?.left ?? 0) + (rect?.width ?? 800) / 2 - 120, y: (rect?.top ?? 0) + (rect?.height ?? 600) / 2 - 40 })
      pos = { x: pos.x + (nodes.length % 5) * 20, y: pos.y + (nodes.length % 5) * 20 }
    }
    const node = { id: nid, type: spec.type, kind: spec.kind, params: defaultParams(spec) }
    const fn: FlowNode = { id: nid, type: spec.type, position: pos, data: { node, spec, errors: [], warnings: [], summary: summarize(node, spec) } }
    setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), { ...fn, selected: true }])
    setSelected(nid)
    setDirty(true)
  }

  const onDrop = (e: DragEvent) => {
    e.preventDefault()
    const raw = e.dataTransfer.getData('application/x-vd-step')
    if (!raw || !ref_) return
    const { type, kind } = JSON.parse(raw)
    const spec = specFor(ref_, type, kind)
    if (spec) addNode(spec, rf.screenToFlowPosition({ x: e.clientX - 120, y: e.clientY - 30 }))
  }

  const isValidConnection = useCallback(
    (c: Connection | RFEdge) => {
      if (c.source === c.target) return false
      const target = nodes.find((n) => n.id === c.target)
      if (!target || target.data.node.type === 'trigger') return false
      if (edges.some((e) => e.source === c.source && e.target === c.target && e.sourceHandle === c.sourceHandle)) return false
      return true
    },
    [nodes, edges],
  )

  const onConnect = useCallback(
    (c: Connection) => {
      const e = edgeFor({ from: c.source!, to: c.target!, port: c.sourceHandle ?? 'next' })
      setEdges((es) => addEdge(e, es))
      setDirty(true)
    },
    [setEdges],
  )

  const sel = nodes.find((n) => n.id === selected)
  const updateSelected = (patch: Partial<FlowNode['data']['node']>) => {
    if (!sel) return
    setNodes((ns) => ns.map((n) => (n.id === sel.id ? { ...n, data: { ...n.data, node: { ...n.data.node, ...patch }, summary: summarize({ ...n.data.node, ...patch }, n.data.spec) } } : n)))
    setDirty(true)
  }
  const removeSelected = () => {
    if (!sel) return
    setNodes((ns) => ns.filter((n) => n.id !== sel.id))
    setEdges((es) => es.filter((e) => e.source !== sel.id && e.target !== sel.id))
    setSelected(null)
    setDirty(true)
  }
  const duplicateSelected = () => {
    if (!sel || !sel.data.spec) return
    const nid = newNodeID(
      nodes.map((n) => n.id),
      sel.id.replace(/-\d+$/, ''),
    )
    const node = { ...sel.data.node, id: nid, params: JSON.parse(JSON.stringify(sel.data.node.params ?? {})) }
    setNodes((ns) => [...ns.map((n) => ({ ...n, selected: false })), { ...sel, id: nid, selected: true, position: { x: sel.position.x + 40, y: sel.position.y + 60 }, data: { ...sel.data, node } }])
    setSelected(nid)
    setDirty(true)
  }

  const tidy = () => {
    const pos = autoLayout({ ...current(), nodes: current().nodes.map((n) => ({ ...n, position: undefined })) })
    setNodes((ns) => ns.map((n) => ({ ...n, position: pos[n.id] ?? n.position })))
    setTimeout(() => rf.fitView({ padding: 0.2, maxZoom: 1.1 }), 50)
    setDirty(true)
  }

  const save = async (asTemplate = isTemplate) => {
    setSaving(true)
    try {
      const body = { definition: current(), is_template: asTemplate }
      const res = savedId && asTemplate === isTemplate ? await put(`/automations/${savedId}`, body) : await post('/automations', body)
      setSavedId(res.automation.id)
      setIsTemplate(asTemplate)
      setDirty(false)
      setMeta((m) => ({ ...m, enabled: res.automation.enabled }))
      qc.invalidateQueries({ queryKey: ['automations'] })
      toast('success', asTemplate ? 'Template saved' : 'Automation saved', res.automation.enabled ? 'Enabled. Desktop apps pick it up on their next refresh.' : res.automation.valid ? 'Saved as disabled.' : 'Saved as a draft. Fix the errors to enable it.')
      if (!id || String(res.automation.id) !== id) navigate(`/automations/${res.automation.id}`, { replace: true })
    } catch (e) {
      if (e instanceof ApiError && e.body?.validation) setValidation(e.body.validation)
      toast('error', 'Could not save', errorMessage(e))
    } finally {
      setSaving(false)
    }
  }

  const exportJSON = () => {
    const blob = new Blob([JSON.stringify(current(), null, 2)], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${(meta.name || 'automation').replace(/[^\w.-]+/g, '_')}.vaultdock.json`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  const back = async () => {
    if (dirty && !(await confirm({ title: 'Discard unsaved changes?', message: 'Your changes to this automation have not been saved.', confirmLabel: 'Discard' }))) return
    setDirty(false)
    navigate('/automations')
  }

  const selContext = useMemo(() => (sel ? contextFor(sel.id, nodes, edges) : undefined), [sel, nodes, edges])

  if (refError || loadError) return <ErrorState error={refError ?? loadError} />
  if (!ref_ || !loaded) return <PageLoader label="Loading automation builder" />

  const errorCount = validation?.errors.length ?? 0
  const warnCount = validation?.warnings.length ?? 0

  return (
    <div className="-mx-4 -my-6 flex h-[calc(100vh-4rem)] flex-col sm:-mx-6 lg:-mx-8">
      {/* toolbar */}
      <div className="flex flex-wrap items-center gap-2 border-b border-slate-200 bg-white px-4 py-2.5 dark:border-slate-800 dark:bg-slate-950">
        <IconButton label="Back to automations" onClick={back}>
          <ArrowLeft className="size-5" />
        </IconButton>
        <input className="min-w-48 flex-1 rounded-lg border border-transparent bg-transparent px-2 py-1 text-lg font-semibold hover:border-slate-200 focus:border-brand-500 focus:outline-none dark:hover:border-slate-700" value={meta.name} onChange={(e) => (setMeta({ ...meta, name: e.target.value }), setDirty(true))} aria-label="Automation name" />
        {isTemplate && <Badge tone="blue">Template</Badge>}
        {errorCount > 0 ? <Badge tone="red">{errorCount} error{errorCount > 1 ? 's' : ''}</Badge> : warnCount > 0 ? <Badge tone="amber">{warnCount} warning{warnCount > 1 ? 's' : ''}</Badge> : validation && <Badge tone="green">valid</Badge>}
        {dirty && <span className="text-xs text-slate-500">unsaved</span>}
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {!isTemplate && <Toggle checked={meta.enabled} onChange={(v) => (setMeta({ ...meta, enabled: v }), setDirty(true))} label="Enabled" />}
          <Button size="sm" variant="ghost" className="2xl:hidden" icon={<PanelRight className="size-4" />} onClick={() => setShowDetails((v) => !v)}>
            Details
          </Button>
          <Button size="sm" variant="ghost" icon={<BookOpen className="size-4" />} onClick={() => setShowRef(true)}>
            Reference
          </Button>
          <Button size="sm" variant="ghost" icon={<FileJson className="size-4" />} onClick={() => setShowImport(true)}>
            Import
          </Button>
          <Button size="sm" variant="ghost" icon={<Download className="size-4" />} onClick={exportJSON}>
            Export
          </Button>
          <Button size="sm" icon={<FlaskConical className="size-4" />} onClick={() => setShowTest(true)}>
            Test
          </Button>
          {!isTemplate && (
            <Button size="sm" onClick={() => save(true)} loading={saving}>
              Save as template
            </Button>
          )}
          <Button size="sm" variant="primary" icon={<Save className="size-4" />} loading={saving} onClick={() => save()}>
            Save
          </Button>
        </div>
      </div>

      <div className="relative flex min-h-0 flex-1">
        <aside className="hidden w-52 shrink-0 border-r xl:w-60 border-slate-200 bg-slate-50 md:block dark:border-slate-800 dark:bg-slate-900/50">
          <Palette ref_={ref_} onAdd={(s) => addNode(s)} />
        </aside>
        <div ref={wrapper} className="relative min-w-0 flex-1" onDragOver={(e) => (e.preventDefault(), (e.dataTransfer.dropEffect = 'move'))} onDrop={onDrop}>
          <ReactFlow
            nodes={nodes}
            edges={edges}
            nodeTypes={nodeTypes}
            onNodesChange={(c) => {
              onNodesChange(c)
              if (c.some((x) => x.type === 'position' || x.type === 'remove')) setDirty(true)
              const rm = c.find((x) => x.type === 'remove')
              if (rm && 'id' in rm && rm.id === selected) setSelected(null)
            }}
            onEdgesChange={(c) => {
              onEdgesChange(c)
              if (c.some((x) => x.type === 'remove')) setDirty(true)
            }}
            onConnect={onConnect}
            isValidConnection={isValidConnection}
            onSelectionChange={({ nodes: sn }) => setSelected(sn.length === 1 ? sn[0].id : null)}
            fitView
            minZoom={0.2}
            maxZoom={1.75}
            deleteKeyCode={['Delete', 'Backspace']}
            proOptions={{ hideAttribution: true }}
            defaultEdgeOptions={{ type: 'smoothstep' }}
          >
            <Background variant={BackgroundVariant.Dots} gap={20} size={1.2} color="#94a3b8" />
            <Controls showInteractive={false} />
            <MiniMap pannable zoomable nodeColor={(n) => (n.type === 'trigger' ? '#6366f1' : n.type === 'condition' ? '#f59e0b' : '#10b981')} maskColor={document.documentElement.classList.contains('dark') ? 'rgba(2,6,23,0.7)' : 'rgba(241,245,249,0.7)'} className="!hidden 2xl:!block" />
          </ReactFlow>
          <div className="absolute top-3 left-3 flex gap-2">
            <Button size="sm" icon={<LayoutGrid className="size-3.5" />} onClick={tidy}>
              Tidy layout
            </Button>
          </div>
          {nodes.length === 0 && (
            <div className="pointer-events-none absolute inset-0 flex items-center justify-center">
              <div className="rounded-2xl border border-dashed border-slate-300 bg-white/80 px-6 py-5 text-center text-sm text-slate-500 dark:border-slate-700 dark:bg-slate-900/80">Drag a trigger from the left to start building.</div>
            </div>
          )}
        </div>

        {/* inspector */}
        <aside className={cx('scroll-thin w-80 shrink-0 overflow-y-auto border-l border-slate-200 bg-white p-4 2xl:w-96 dark:border-slate-800 dark:bg-slate-950', sel || showDetails ? 'absolute inset-y-0 right-0 z-10 shadow-2xl 2xl:static 2xl:shadow-none' : 'hidden 2xl:block')}>
          {(sel || showDetails) && (
            <div className="mb-2 flex justify-end 2xl:hidden">
              <IconButton
                label="Close panel"
                onClick={() => {
                  setShowDetails(false)
                  setSelected(null)
                  setNodes((ns) => ns.map((n) => ({ ...n, selected: false })))
                }}
              >
                <X className="size-4" />
              </IconButton>
            </div>
          )}
          {sel && sel.data.spec ? (
            <div>
              <div className="mb-4 flex items-start gap-3">
                <span className={cx('flex size-9 shrink-0 items-center justify-center rounded-lg', typeStyle[sel.data.node.type].chip)}>
                  {(() => {
                    const I = kindIcons[sel.data.node.kind]
                    return I ? <I className="size-5" /> : null
                  })()}
                </span>
                <div className="min-w-0 flex-1">
                  <div className="text-[10px] font-semibold tracking-wider text-slate-400 uppercase">{typeStyle[sel.data.node.type].label}</div>
                  <div className="font-semibold">{sel.data.spec.title}</div>
                  <code className="text-[11px] text-slate-500">
                    {sel.data.node.kind} · id {sel.id}
                  </code>
                </div>
                <IconButton label="Duplicate step" onClick={duplicateSelected}>
                  <Copy className="size-4" />
                </IconButton>
                <IconButton label="Delete step" onClick={removeSelected}>
                  <Trash2 className="size-4 text-rose-500" />
                </IconButton>
              </div>
              <p className="mb-4 text-xs text-slate-500">{sel.data.spec.description}</p>
              {sel.data.node.type === 'condition' && <p className="mb-4 rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-900 dark:bg-amber-500/10 dark:text-amber-200">Connect the green YES handle for files that match and the red NO handle for files that don't.</p>}
              <div className="mb-4">
                <Field label="Label (optional)">
                  <input className="input" value={sel.data.node.label ?? ''} placeholder={sel.data.spec.title} onChange={(e) => updateSelected({ label: e.target.value || undefined })} />
                </Field>
              </div>
              <ParamsForm spec={sel.data.spec} params={sel.data.node.params ?? {}} onChange={(p) => updateSelected({ params: p })} ref_={ref_} context={sel.data.node.type === 'trigger' ? sel.data.spec.context : selContext} />
              {(sel.data.errors.length > 0 || sel.data.warnings.length > 0) && (
                <div className="mt-5">
                  <IssueList v={{ valid: !sel.data.errors.length, errors: sel.data.errors.map((m) => ({ path: sel.id, message: m })), warnings: sel.data.warnings.map((m) => ({ path: sel.id, message: m })) }} />
                </div>
              )}
            </div>
          ) : (
            <div className="space-y-4">
              <h3 className="font-semibold">Automation settings</h3>
              <Field label="Description">
                <textarea className="input h-24" value={meta.description ?? ''} onChange={(e) => (setMeta({ ...meta, description: e.target.value }), setDirty(true))} />
              </Field>
              <Field label="Priority" hint="When several automations act on the same file, the higher priority wins conflicting decisions. At equal priority, skip beats upload.">
                <input className="input" type="number" min={-1000} max={1000} value={meta.priority ?? 0} onChange={(e) => (setMeta({ ...meta, priority: Number(e.target.value) || 0 }), setDirty(true))} />
              </Field>
              <div>
                <div className="label">Validation</div>
                {validation ? <IssueList v={validation} /> : <PageLoader label="Validating" />}
              </div>
              <div className="rounded-xl bg-slate-50 p-3 text-xs text-slate-600 dark:bg-slate-900 dark:text-slate-400">
                <strong className="text-slate-800 dark:text-slate-200">How it works.</strong> The desktop app evaluates enabled automations when files change in its sync folders. Files that pass the sync configuration's filters are uploaded unless an automation skips them. Automations never delete, move or rename files on your computer.
              </div>
            </div>
          )}
        </aside>
      </div>

      <ReferenceModal open={showRef} onClose={() => setShowRef(false)} ref_={ref_} />
      <ImportModal
        open={showImport}
        onClose={() => setShowImport(false)}
        onImport={(a, v) => {
          load(a, v)
          setValidation(v)
          setDirty(true)
          setShowImport(false)
          toast('success', 'Imported', v.valid ? 'The automation is valid.' : 'Fix the highlighted problems before enabling it.')
        }}
      />
      <DryRunModal open={showTest} onClose={() => setShowTest(false)} automation={current()} />
    </div>
  )
}

export default function EditorPage() {
  return (
    <ReactFlowProvider>
      <Editor />
    </ReactFlowProvider>
  )
}
