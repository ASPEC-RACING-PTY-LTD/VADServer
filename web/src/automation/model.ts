import { useQuery } from '@tanstack/react-query'
import type { Edge as RFEdge, Node as RFNode } from '@xyflow/react'
import { get } from '../lib/api'

export const SCHEMA_ID = 'vaultdock.automation/v1'

export type NodeType = 'trigger' | 'condition' | 'action'

export type ANode = {
  id: string
  type: NodeType
  kind: string
  label?: string
  params?: Record<string, any>
  position?: { x: number; y: number }
}
export type AEdge = { from: string; to: string; port?: string }
export type Automation = {
  schema: string
  name: string
  description?: string
  enabled: boolean
  priority?: number
  nodes: ANode[]
  edges: AEdge[]
}

export type ParamSpec = {
  name: string
  type: 'string' | 'template' | 'string_list' | 'integer' | 'bytes' | 'enum' | 'boolean' | 'date' | 'glob' | 'rules' | 'variable'
  required: boolean
  default?: any
  options?: string[]
  min?: number
  max?: number
  description: string
  example?: any
  templated?: boolean
}
export type KindSpec = {
  type: NodeType
  kind: string
  title: string
  description: string
  category: string
  params: ParamSpec[]
  context?: string
  contexts?: string[]
  ports: string[]
}
export type VariableSpec = { name: string; type: string; description: string; example: string; contexts: string[]; applies_to: string }
export type Reference = {
  schema_id: string
  catalog: Record<NodeType, KindSpec[]>
  variables: VariableSpec[]
  filters: { name: string; usage: string; description: string }[]
  json_schema: any
  example: string
  ai_guide: string
}
export type Issue = { path: string; node_id?: string; message: string }
export type Validation = { valid: boolean; errors: Issue[]; warnings: Issue[] }

export function useReference() {
  return useQuery<Reference>({ queryKey: ['automation-reference'], queryFn: () => get('/automations/reference'), staleTime: Infinity })
}

export function specFor(ref: Reference | undefined, type: NodeType, kind: string): KindSpec | undefined {
  return ref?.catalog[type]?.find((k) => k.kind === kind)
}

export type FlowData = { node: ANode; spec?: KindSpec; errors: string[]; warnings: string[]; summary: string }
export type FlowNode = RFNode<FlowData>

export function emptyAutomation(): Automation {
  return { schema: SCHEMA_ID, name: 'New automation', description: '', enabled: false, priority: 0, nodes: [], edges: [] }
}

// Layered auto-layout for documents without positions (e.g. AI-generated).
export function autoLayout(a: Automation): Record<string, { x: number; y: number }> {
  const out: Record<string, string[]> = {}
  const indeg: Record<string, number> = {}
  a.nodes.forEach((n) => {
    out[n.id] = []
    indeg[n.id] = 0
  })
  a.edges.forEach((e) => {
    if (out[e.from] && indeg[e.to] !== undefined) {
      out[e.from].push(e.to)
      indeg[e.to]++
    }
  })
  const depth: Record<string, number> = {}
  const queue = a.nodes.filter((n) => indeg[n.id] === 0).map((n) => n.id)
  queue.forEach((id) => (depth[id] = 0))
  const remaining = { ...indeg }
  while (queue.length) {
    const id = queue.shift()!
    for (const t of out[id]) {
      depth[t] = Math.max(depth[t] ?? 0, depth[id] + 1)
      if (--remaining[t] === 0) queue.push(t)
    }
  }
  const cols: Record<number, string[]> = {}
  a.nodes.forEach((n) => {
    const d = depth[n.id] ?? 0
    ;(cols[d] ??= []).push(n.id)
  })
  const pos: Record<string, { x: number; y: number }> = {}
  Object.entries(cols).forEach(([d, ids]) => {
    ids.forEach((id, i) => {
      pos[id] = { x: Number(d) * 320, y: i * 170 - ((ids.length - 1) * 170) / 2 + 200 }
    })
  })
  return pos
}

export function summarize(n: ANode, spec?: KindSpec): string {
  const p = n.params ?? {}
  const fmtBytes = (v: number) => (v >= 1 << 30 ? `${(v / (1 << 30)).toFixed(1)} GB` : v >= 1 << 20 ? `${(v / (1 << 20)).toFixed(1)} MB` : v >= 1024 ? `${(v / 1024).toFixed(0)} KB` : `${v} B`)
  const ops: Record<string, string> = { gt: '>', gte: '≥', lt: '<', lte: '≤', eq: '=' }
  const neg = p.negate ? 'NOT ' : ''
  switch (n.kind) {
    case 'extension.in':
    case 'file.extension_matched':
      return neg + (p.extensions ?? []).map((e: string) => '.' + e).join(', ')
    case 'size.compare':
      return `${neg}size ${ops[p.operator] ?? p.operator ?? '?'} ${typeof p.bytes === 'number' ? fmtBytes(p.bytes) : p.bytes ?? '?'}`
    case 'file.size_threshold':
      return `≥ ${typeof p.bytes === 'number' ? fmtBytes(p.bytes) : p.bytes ?? '?'}`
    case 'name.contains':
      return `${neg}name contains “${p.text ?? ''}”`
    case 'name.matches':
    case 'path.matches':
      return `${neg}${p.pattern ?? ''}`
    case 'file.age':
      return `${neg}age ${ops[p.operator] ?? '?'} ${p.days ?? '?'} days`
    case 'date.compare':
      return `${neg}${p.field ?? '?'} ${p.operator ?? '?'} ${p.date ?? '?'}`
    case 'variable.compare':
      return `${neg}${p.variable ?? '?'} ${p.operator ?? ''} ${p.value ?? ''}`.trim()
    case 'group':
      return `${neg}${p.mode === 'any' ? 'ANY' : 'ALL'} of ${(p.rules ?? []).length} rules`
    case 'dest.set_folder':
    case 'dest.create_folder':
      return p.folder ?? ''
    case 'dest.rename':
      return p.name ?? ''
    case 'tag.apply':
      return (p.tags ?? []).join(', ')
    case 'sync.start_task':
      return p.config ?? ''
    case 'notify.send':
      return p.title ?? ''
    case 'sync.skip':
      return p.reason ?? ''
    case 'schedule.interval':
      return `every ${p.every_minutes ?? '?'} min`
    case 'folder.changed':
      return p.folder || '**'
  }
  if (p.configs?.length) return `only: ${p.configs.join(', ')}`
  return spec?.description.split('.')[0] ?? ''
}

export function toFlow(a: Automation, ref: Reference | undefined, v?: Validation): { nodes: FlowNode[]; edges: RFEdge[] } {
  const layout = a.nodes.some((n) => !n.position) ? autoLayout(a) : {}
  const errs: Record<string, string[]> = {}
  const warns: Record<string, string[]> = {}
  v?.errors.forEach((e) => e.node_id && (errs[e.node_id] ??= []).push(e.message))
  v?.warnings.forEach((e) => e.node_id && (warns[e.node_id] ??= []).push(e.message))
  const nodes: FlowNode[] = a.nodes.map((n) => {
    const spec = specFor(ref, n.type, n.kind)
    return {
      id: n.id,
      type: n.type,
      position: n.position ?? layout[n.id] ?? { x: 0, y: 0 },
      data: { node: n, spec, errors: errs[n.id] ?? [], warnings: warns[n.id] ?? [], summary: summarize(n, spec) },
    }
  })
  const edges: RFEdge[] = a.edges.map((e) => edgeFor(e))
  return { nodes, edges }
}

export function edgeFor(e: AEdge): RFEdge {
  const port = e.port || 'next'
  const color = port === 'true' ? '#10b981' : port === 'false' ? '#f43f5e' : undefined
  return {
    id: `${e.from}:${port}:${e.to}`,
    source: e.from,
    sourceHandle: port,
    target: e.to,
    targetHandle: 'in',
    type: 'smoothstep',
    animated: false,
    label: port === 'next' ? undefined : port === 'true' ? 'yes' : 'no',
    labelStyle: { fontSize: 11, fontWeight: 600, fill: color },
    labelBgStyle: { fill: 'var(--label-bg, #fff)' },
    style: color ? { stroke: color } : undefined,
    data: { port },
  }
}

export function fromFlow(meta: Omit<Automation, 'nodes' | 'edges'>, nodes: FlowNode[], edges: RFEdge[]): Automation {
  return {
    ...meta,
    nodes: nodes.map((n) => {
      const out: ANode = { id: n.id, type: n.data.node.type, kind: n.data.node.kind }
      if (n.data.node.label) out.label = n.data.node.label
      out.params = n.data.node.params ?? {}
      out.position = { x: Math.round(n.position.x), y: Math.round(n.position.y) }
      return out
    }),
    edges: edges.map((e) => ({ from: e.source, to: e.target, port: (e.sourceHandle as string) || 'next' })),
  }
}

export function newNodeID(existing: string[], base: string) {
  const clean = base.replace(/[^A-Za-z0-9_-]/g, '-').replace(/-+/g, '-').slice(0, 40) || 'node'
  let i = 1
  while (existing.includes(`${clean}-${i}`)) i++
  return `${clean}-${i}`
}

export function defaultParams(spec: KindSpec): Record<string, any> {
  const p: Record<string, any> = {}
  for (const ps of spec.params) {
    if (ps.default !== undefined) p[ps.name] = ps.default
    else if (ps.required) {
      switch (ps.type) {
        case 'string_list':
          p[ps.name] = Array.isArray(ps.example) ? [...ps.example] : []
          break
        case 'rules':
          p[ps.name] = [{ kind: 'extension.in', params: { extensions: ['mp4'] } }]
          break
        case 'enum':
          p[ps.name] = ps.example ?? ps.options?.[0]
          break
        case 'integer':
        case 'bytes':
          p[ps.name] = typeof ps.example === 'number' ? ps.example : 0
          break
        default:
          p[ps.name] = ps.example ?? ''
      }
    }
  }
  return p
}
