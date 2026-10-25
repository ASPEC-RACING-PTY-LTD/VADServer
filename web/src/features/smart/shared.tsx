import { useState } from 'react'
import { AlertTriangle, CheckCircle2, ChevronDown, ChevronRight, Info, Plus, Sparkles, Trash2, Wand2, XCircle } from 'lucide-react'
import { Badge, Button, cx, Field, IconButton, Toggle, useUI } from '../../components/ui'
import { errorMessage } from '../../lib/api'
import { categoryLabel, categoryTone, smartApi, useCatalog, type Catalog, type Component, type Filter, type FilterNode, type KindSpec, type OutputSettings, type Plan, type Rule, type SegmentContext } from './api'

export const CATEGORIES = ['emotion', 'conversation', 'gaming', 'visual', 'audio', 'speech', 'custom'] as const

export const emptyFilter = (ctx?: SegmentContext): Filter => ({ rules: [], context: ctx ?? defaultContext })

export const defaultContext: SegmentContext = { threshold: 0.5, min_event: 0.3, max_segment: 90, pre: 8, post: 6, merge: true, merge_gap: 6, trim_silence: true, snap_speech: true, dedupe: 0.6, max_suggestions: 40 }

/** What each condition needs and whether it runs here. */
export function CapabilityReport({ components, unsupported, notes, compact }: { components: Component[]; unsupported?: string[]; notes?: string[]; compact?: boolean }) {
  if (!components.length && !unsupported?.length && !notes?.length) return null
  return (
    <div className="space-y-2">
      {components.map((c, i) => (
        <div key={i} className={cx('flex items-start gap-2 rounded-lg border p-2.5 text-sm', c.available ? 'border-slate-200 dark:border-slate-700' : 'border-amber-300 bg-amber-50 dark:border-amber-500/40 dark:bg-amber-500/10')}>
          {c.available ? <CheckCircle2 className="mt-0.5 size-4 shrink-0 text-emerald-500" /> : <AlertTriangle className="mt-0.5 size-4 shrink-0 text-amber-500" />}
          <div className="min-w-0 flex-1">
            <div className="font-medium text-slate-800 dark:text-slate-100">
              {c.rule}: {c.summary}
              {c.fallback && (
                <Badge tone="amber" className="ml-2">
                  fallback
                </Badge>
              )}
            </div>
            {!compact && <div className="text-xs text-slate-500 dark:text-slate-400">{c.reliability}</div>}
            <div className="mt-0.5 text-xs text-slate-500 dark:text-slate-400">
              Uses {c.detectors.join(', ') || 'no detector'}
              {!c.available && c.missing?.length ? <span className="text-amber-700 dark:text-amber-300"> · needs {c.missing.join(', ')} (install in Smart Processing settings)</span> : null}
            </div>
          </div>
        </div>
      ))}
      {unsupported?.map((u, i) => (
        <div key={`u${i}`} className="flex items-start gap-2 rounded-lg border border-rose-200 bg-rose-50 p-2.5 text-sm text-rose-800 dark:border-rose-500/40 dark:bg-rose-500/10 dark:text-rose-200">
          <XCircle className="mt-0.5 size-4 shrink-0" />
          <span>{u}</span>
        </div>
      ))}
      {notes?.map((n, i) => (
        <div key={`n${i}`} className="flex items-start gap-2 text-xs text-slate-500 dark:text-slate-400">
          <Info className="mt-0.5 size-3.5 shrink-0" />
          <span>{n}</span>
        </div>
      ))}
    </div>
  )
}

/** Natural-language description turned into conditions (local interpretation). */
export function DescribeBox({ onPlan, initial = '' }: { onPlan: (p: Plan) => void; initial?: string }) {
  const [text, setText] = useState(initial)
  const [busy, setBusy] = useState(false)
  const [plan, setPlan] = useState<Plan | null>(null)
  const { toast } = useUI()
  const run = async () => {
    if (!text.trim()) return
    setBusy(true)
    try {
      const p = await smartApi.interpret(text.trim())
      setPlan(p)
      onPlan(p)
    } catch (e) {
      toast('error', 'Could not interpret the description', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const tone = plan?.status === 'supported' ? 'green' : plan?.status === 'partial' ? 'amber' : plan ? 'red' : 'slate'
  return (
    <div className="space-y-3">
      <Field label="Describe what to find" hint='For example: "laughing after someone tells a joke", "when I say clip that", "scoreboard changes or loud cheering". Interpreted on this server; nothing is sent anywhere.'>
        <textarea className="input min-h-20" value={text} maxLength={1000} onChange={(e) => setText(e.target.value)} placeholder="Moments where both people laugh at the same time" />
      </Field>
      <div className="flex items-center gap-2">
        <Button icon={<Wand2 className="size-4" />} loading={busy} disabled={!text.trim()} onClick={run}>
          Interpret
        </Button>
        {plan && (
          <span className="text-xs text-slate-500">
            <Badge tone={tone}>{plan.status}</Badge> {plan.interpreter === 'rules' ? 'built-in interpreter' : plan.interpreter.replace('model:', 'local model ')}
          </span>
        )}
      </div>
      {plan && <CapabilityReport components={plan.components} unsupported={plan.unsupported} notes={plan.notes} compact />}
    </div>
  )
}

/** Segment settings: thresholds, padding, merging, trimming, duplicates. */
export function ContextEditor({ value, onChange }: { value: SegmentContext; onChange: (c: SegmentContext) => void }) {
  const set = <K extends keyof SegmentContext>(k: K, v: SegmentContext[K]) => onChange({ ...value, [k]: v })
  const num = (k: keyof SegmentContext, label: string, hint: string, min: number, max: number, step: number) => (
    <Field label={label} hint={hint}>
      <input className="input" type="number" min={min} max={max} step={step} value={value[k] as number} onChange={(e) => set(k, Number(e.target.value) as never)} />
    </Field>
  )
  return (
    <div className="space-y-4">
      <Field label={`Sensitivity: confidence threshold ${Math.round(value.threshold * 100)}%`} hint="Lower finds more moments (more false positives); higher keeps only confident ones.">
        <input type="range" min={0.05} max={0.95} step={0.05} value={value.threshold} onChange={(e) => set('threshold', Number(e.target.value))} className="w-full" />
      </Field>
      <div className="grid gap-4 sm:grid-cols-3">
        {num('pre', 'Footage before (s)', 'Lead-up kept before a moment', 0, 300, 1)}
        {num('post', 'Footage after (s)', 'Reaction kept after a moment', 0, 300, 1)}
        {num('max_segment', 'Longest segment (s)', 'Longer moments are cut to this', 5, 3600, 5)}
        {num('min_event', 'Shortest moment (s)', 'Shorter detections are ignored', 0, 600, 0.1)}
        {num('merge_gap', 'Merge gap (s)', 'Moments closer than this become one segment', 0, 600, 1)}
        {num('max_suggestions', 'Suggestions per recording', '0 for no limit', 0, 500, 1)}
      </div>
      <Field label={`Duplicates: drop a weaker suggestion overlapping a stronger one by more than ${Math.round(value.dedupe * 100)}%`}>
        <input type="range" min={0} max={1} step={0.05} value={value.dedupe} onChange={(e) => set('dedupe', Number(e.target.value))} className="w-full" />
      </Field>
      <div className="grid gap-3 sm:grid-cols-3">
        <Toggle checked={value.merge} onChange={(v) => set('merge', v)} label="Merge nearby moments" />
        <Toggle checked={value.trim_silence} onChange={(v) => set('trim_silence', v)} label="Trim silent ends" />
        <Toggle checked={value.snap_speech} onChange={(v) => set('snap_speech', v)} label="Do not cut mid-sentence" />
      </div>
    </div>
  )
}

export function OutputEditor({ value, onChange, onPickFolder, folderLabel }: { value: OutputSettings; onChange: (o: OutputSettings) => void; onPickFolder: () => void; folderLabel?: string }) {
  return (
    <div className="grid gap-4 sm:grid-cols-2">
      <Field label="Clip quality" hint="Automatic copies the original stream when a cut lands on a keyframe (fast, lossless) and re-encodes frame-accurately otherwise.">
        <select className="input" value={value.quality} onChange={(e) => onChange({ ...value, quality: e.target.value as OutputSettings['quality'] })}>
          <option value="auto">Automatic (copy when exact, else high quality)</option>
          <option value="copy">Stream copy only (fastest, cuts at keyframes)</option>
          <option value="high">High quality re-encode</option>
          <option value="balanced">Balanced re-encode</option>
          <option value="small">Small files</option>
        </select>
      </Field>
      <Field label="Where clips are saved" hint="Clips keep the original's dates and mirror its folders.">
        <select className="input" value={value.destination} onChange={(e) => onChange({ ...value, destination: e.target.value as OutputSettings['destination'] })}>
          <option value="beside">Beside each original</option>
          <option value="subfolder">In a subfolder next to each original</option>
          <option value="folder">In one folder (mirroring the originals' folders)</option>
        </select>
      </Field>
      {value.destination === 'subfolder' && (
        <Field label="Subfolder name">
          <input className="input" value={value.subfolder ?? 'Highlights'} maxLength={120} onChange={(e) => onChange({ ...value, subfolder: e.target.value })} />
        </Field>
      )}
      {value.destination === 'folder' && (
        <Field label="Destination folder">
          <Button onClick={onPickFolder}>{folderLabel || (value.folder_id ? `Folder #${value.folder_id}` : 'Choose folder…')}</Button>
        </Field>
      )}
    </div>
  )
}

// --- rule builder ---

function kindOf(cat: Catalog | undefined, kind?: string): KindSpec | undefined {
  return cat?.kinds.find((k) => k.kind === kind)
}

const leafDefaults = (k: KindSpec): FilterNode => {
  const n: FilterNode = { kind: k.kind }
  if (k.modes?.length) n.mode = k.modes[0]
  if (k.labels?.length) n.label = k.labels[0]
  if (k.kind === 'audio_event') n.labels = ['laughter']
  if (k.kind === 'keyword') n.terms = []
  return n
}

/** One condition: a detector leaf or a combination of conditions. */
export function NodeEditor({ node, onChange, onRemove, depth = 0 }: { node: FilterNode; onChange: (n: FilterNode) => void; onRemove?: () => void; depth?: number }) {
  const { data: cat } = useCatalog()
  const isOp = !!node.op
  const spec = kindOf(cat, node.kind)
  const set = (p: Partial<FilterNode>) => onChange({ ...node, ...p })
  return (
    <div className={cx('rounded-lg border p-3', depth % 2 ? 'border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900' : 'border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/40')}>
      <div className="flex flex-wrap items-center gap-2">
        <select
          className="input h-8 w-auto py-0"
          aria-label="Condition type"
          value={isOp ? `op:${node.op}` : (node.kind ?? '')}
          onChange={(e) => {
            const v = e.target.value
            if (v.startsWith('op:')) onChange({ op: v.slice(3) as FilterNode['op'], window: node.window ?? 10, children: node.op ? node.children : [node.kind ? node : { kind: 'speech', mode: 'active' }] })
            else {
              const k = kindOf(cat, v)
              if (k) onChange(leafDefaults(k))
            }
          }}
        >
          <optgroup label="Combine">
            <option value="op:and">All of (together)</option>
            <option value="op:or">Any of</option>
            <option value="op:then">In sequence (one after another)</option>
            <option value="op:without">First, without the others</option>
          </optgroup>
          {CATEGORIES.map((c) => (
            <optgroup key={c} label={categoryLabel[c]}>
              {cat?.kinds
                .filter((k) => k.category === c)
                .map((k) => (
                  <option key={k.kind} value={k.kind}>
                    {k.title}
                  </option>
                ))}
            </optgroup>
          ))}
        </select>
        {isOp && node.op !== 'or' && (
          <label className="flex items-center gap-1 text-xs text-slate-500">
            within
            <input className="input h-8 w-20 py-0" type="number" min={0} max={600} step={0.5} value={node.window ?? 10} onChange={(e) => set({ window: Number(e.target.value) })} />s
          </label>
        )}
        {!isOp && spec?.modes && spec.modes.length > 0 && (
          <select className="input h-8 w-auto py-0" aria-label="Mode" value={node.mode ?? spec.modes[0]} onChange={(e) => set({ mode: e.target.value })}>
            {spec.modes.map((m) => (
              <option key={m} value={m}>
                {m.replace(/_/g, ' ')}
              </option>
            ))}
          </select>
        )}
        {!isOp && spec?.labels && spec.labels.length > 0 && (
          <select className="input h-8 w-auto py-0" aria-label="Label" value={node.label ?? spec.labels[0]} onChange={(e) => set({ label: e.target.value })}>
            {spec.labels.map((m) => (
              <option key={m} value={m}>
                {m}
              </option>
            ))}
          </select>
        )}
        <div className="flex-1" />
        {onRemove && (
          <IconButton label="Remove condition" onClick={onRemove}>
            <Trash2 className="size-4" />
          </IconButton>
        )}
      </div>
      {!isOp && spec && <LeafFields node={node} spec={spec} sounds={cat?.sounds ?? []} set={set} />}
      {isOp && (
        <div className="mt-2 space-y-2 pl-3">
          {(node.children ?? []).map((c, i) => (
            <NodeEditor
              key={i}
              node={c}
              depth={depth + 1}
              onChange={(n) => set({ children: (node.children ?? []).map((x, j) => (j === i ? n : x)) })}
              onRemove={(node.children ?? []).length > 1 ? () => set({ children: (node.children ?? []).filter((_, j) => j !== i) }) : undefined}
            />
          ))}
          {depth < 4 && (node.children?.length ?? 0) < 8 && (
            <Button size="sm" variant="ghost" icon={<Plus className="size-3.5" />} onClick={() => set({ children: [...(node.children ?? []), { kind: 'speech', mode: 'active' }] })}>
              Add condition
            </Button>
          )}
        </div>
      )}
    </div>
  )
}

function LeafFields({ node, spec, sounds, set }: { node: FilterNode; spec: KindSpec; sounds: string[]; set: (p: Partial<FilterNode>) => void }) {
  const k = spec.kind
  return (
    <div className="mt-2 space-y-2">
      <p className="text-xs text-slate-500 dark:text-slate-400">
        {spec.description} <span className="text-slate-400">· {spec.reliability}</span>
      </p>
      <div className="flex flex-wrap items-end gap-3">
        {k === 'audio_event' && (
          <div className="min-w-60 flex-1">
            <span className="label">Sounds</span>
            <div className="flex flex-wrap gap-1">
              {sounds.map((s) => {
                const on = (node.labels ?? []).includes(s)
                return (
                  <button
                    key={s}
                    type="button"
                    aria-pressed={on}
                    onClick={() => set({ labels: on ? (node.labels ?? []).filter((x) => x !== s) : [...(node.labels ?? []), s] })}
                    className={cx('rounded-full border px-2 py-0.5 text-xs', on ? 'border-brand-500 bg-brand-50 text-brand-700 dark:bg-brand-500/15 dark:text-brand-200' : 'border-slate-300 text-slate-600 dark:border-slate-600 dark:text-slate-300')}
                  >
                    {s.replace(/_/g, ' ')}
                  </button>
                )
              })}
            </div>
          </div>
        )}
        {(spec.needs_text || ['audio_description', 'visual_description', 'dialogue', 'onscreen_text'].includes(k)) && (
          <label className="min-w-60 flex-1">
            <span className="label">{k === 'onscreen_text' ? 'Text to find (A|B for alternatives)' : 'Description'}</span>
            <input className="input" value={node.text ?? ''} maxLength={400} onChange={(e) => set({ text: e.target.value })} />
          </label>
        )}
        {k === 'keyword' && (
          <>
            <label className="min-w-60 flex-1">
              <span className="label">Words or phrases (comma separated)</span>
              <input className="input" value={(node.terms ?? []).join(', ')} onChange={(e) => set({ terms: e.target.value.split(',').map((t) => t.trim()).filter(Boolean) })} />
            </label>
            <label>
              <span className="label">Match</span>
              <select className="input" value={node.match ?? 'any'} onChange={(e) => set({ match: e.target.value as 'any' | 'all' })}>
                <option value="any">any</option>
                <option value="all">all (within window)</option>
              </select>
            </label>
          </>
        )}
        {k === 'killfeed' && (
          <label>
            <span className="label">Player name</span>
            <input className="input" value={node.name ?? ''} maxLength={60} onChange={(e) => set({ name: e.target.value })} />
          </label>
        )}
        {(k === 'killfeed' || k === 'score_change') && (
          <label>
            <span className="label">{k === 'killfeed' ? 'Role' : 'Side'}</span>
            <select className="input" value={node.side ?? 'any'} onChange={(e) => set({ side: e.target.value })}>
              {(k === 'killfeed' ? ['any', 'kill', 'death'] : ['any', 'left', 'right']).map((v) => (
                <option key={v}>{v}</option>
              ))}
            </select>
          </label>
        )}
        {k === 'loudness' && (
          <label className="w-28">
            <span className="label">{node.mode === 'sudden_rise' || node.mode === 'sudden_drop' ? 'Change (dB)' : 'Level (dBFS)'}</span>
            <input className="input" type="number" step={1} value={node.db ?? ''} placeholder="auto" onChange={(e) => set({ db: e.target.value === '' ? undefined : Number(e.target.value) })} />
          </label>
        )}
        {k === 'repeated_phrase' && (
          <label className="w-28">
            <span className="label">Repetitions</span>
            <input className="input" type="number" min={2} max={20} value={node.count ?? 3} onChange={(e) => set({ count: Number(e.target.value) })} />
          </label>
        )}
        <label className="w-28">
          <span className="label">Min score</span>
          <input className="input" type="number" min={0} max={1} step={0.05} value={node.min_score ?? ''} placeholder={String(spec.default_score)} onChange={(e) => set({ min_score: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </label>
        <label className="w-28">
          <span className="label">Min length (s)</span>
          <input className="input" type="number" min={0} max={600} step={0.1} value={node.min_duration ?? ''} placeholder="0" onChange={(e) => set({ min_duration: e.target.value === '' ? undefined : Number(e.target.value) })} />
        </label>
      </div>
    </div>
  )
}

/** Rules of a filter, with a natural-language shortcut. */
export function FilterEditor({ value, onChange, components }: { value: Filter; onChange: (f: Filter) => void; components?: Component[] }) {
  const [open, setOpen] = useState<Record<number, boolean>>({})
  const setRule = (i: number, r: Rule) => onChange({ ...value, rules: value.rules.map((x, j) => (j === i ? r : x)) })
  const addRule = (r?: Partial<Rule>) => onChange({ ...value, rules: [...value.rules, { id: `r${Date.now().toString(36)}`, name: r?.name ?? `Condition ${value.rules.length + 1}`, category: r?.category ?? 'custom', when: r?.when ?? { kind: 'loudness', mode: 'loud' } }] })
  return (
    <div className="space-y-3">
      {value.rules.length === 0 && <p className="text-sm text-slate-500">No conditions yet. Add one, or describe what to find and interpret it.</p>}
      {value.rules.map((r, i) => {
        const comps = components?.filter((c) => c.rule === r.name) ?? []
        const missing = comps.some((c) => !c.available)
        const expanded = open[i] ?? value.rules.length <= 2
        return (
          <div key={r.id || i} className="card p-3">
            <div className="flex flex-wrap items-center gap-2">
              <button type="button" className="text-slate-400" onClick={() => setOpen({ ...open, [i]: !expanded })} aria-label={expanded ? 'Collapse' : 'Expand'}>
                {expanded ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />}
              </button>
              <input className="input h-8 max-w-64 py-0 font-medium" aria-label="Rule name" value={r.name} maxLength={120} onChange={(e) => setRule(i, { ...r, name: e.target.value })} />
              <select className="input h-8 w-auto py-0" aria-label="Category" value={r.category} onChange={(e) => setRule(i, { ...r, category: e.target.value })}>
                {CATEGORIES.map((c) => (
                  <option key={c} value={c}>
                    {categoryLabel[c]}
                  </option>
                ))}
              </select>
              <Badge tone={categoryTone[r.category] ?? 'slate'}>{r.category}</Badge>
              {missing && <Badge tone="amber">needs models</Badge>}
              <div className="flex-1" />
              <Toggle checked={!r.disabled} onChange={(v) => setRule(i, { ...r, disabled: !v })} label="On" />
              <IconButton label="Remove rule" onClick={() => onChange({ ...value, rules: value.rules.filter((_, j) => j !== i) })}>
                <Trash2 className="size-4" />
              </IconButton>
            </div>
            {expanded && (
              <div className="mt-3">
                <NodeEditor node={r.when} onChange={(n) => setRule(i, { ...r, when: n })} />
              </div>
            )}
          </div>
        )
      })}
      <div className="flex flex-wrap gap-2">
        <Button size="sm" icon={<Plus className="size-3.5" />} onClick={() => addRule()}>
          Add condition
        </Button>
        <Button size="sm" variant="ghost" icon={<Sparkles className="size-3.5" />} onClick={() => addRule({ name: 'Reaction after speech', category: 'emotion', when: { op: 'then', window: 4, children: [{ kind: 'speech', mode: 'active' }, { kind: 'audio_event', labels: ['laughter'] }] } })}>
          Add sequence example
        </Button>
      </div>
    </div>
  )
}

/** Live capability check of a filter. */
export function useFilterCheck() {
  const [components, setComponents] = useState<Component[]>([])
  const [error, setError] = useState('')
  const check = async (f: Filter) => {
    try {
      const r = await smartApi.check(f)
      setComponents(r.components)
      setError('')
      return r.filter
    } catch (e) {
      setError(errorMessage(e))
      return null
    }
  }
  return { components, error, check }
}

export function pct(v: number) {
  return `${Math.round(v * 100)}%`
}
