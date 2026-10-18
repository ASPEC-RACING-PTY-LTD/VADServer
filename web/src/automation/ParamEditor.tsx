import { useState } from 'react'
import { Braces, Plus, Trash2, X } from 'lucide-react'
import type { KindSpec, ParamSpec, Reference } from './model'
import { defaultParams } from './model'
import { Badge, Button, cx, Menu, Toggle } from '../components/ui'

const units = [
  { label: 'B', mult: 1 },
  { label: 'KB', mult: 1024 },
  { label: 'MB', mult: 1024 ** 2 },
  { label: 'GB', mult: 1024 ** 3 },
]

function BytesInput({ value, onChange }: { value: any; onChange: (v: number) => void }) {
  const n = typeof value === 'number' ? value : Number(value) || 0
  const initialUnit = [...units].reverse().find((u) => n >= u.mult && n % u.mult === 0) ?? units[0]
  const [unit, setUnit] = useState(initialUnit)
  return (
    <div className="flex gap-2">
      <input className="input" type="number" min={0} step="any" value={Number((n / unit.mult).toFixed(3))} onChange={(e) => onChange(Math.round((Number(e.target.value) || 0) * unit.mult))} />
      <select
        className="input w-24"
        value={unit.label}
        onChange={(e) => {
          const u = units.find((x) => x.label === e.target.value)!
          setUnit(u)
          onChange(Math.round((n / unit.mult) * u.mult))
        }}
      >
        {units.map((u) => (
          <option key={u.label}>{u.label}</option>
        ))}
      </select>
    </div>
  )
}

function ListInput({ value, onChange, placeholder }: { value: any; onChange: (v: string[]) => void; placeholder?: string }) {
  const list: string[] = Array.isArray(value) ? value : []
  const [text, setText] = useState('')
  const add = () => {
    const parts = text.split(',').map((s) => s.trim()).filter(Boolean)
    if (parts.length) onChange([...list, ...parts.filter((p) => !list.includes(p))])
    setText('')
  }
  return (
    <div>
      <div className="mb-2 flex flex-wrap gap-1.5">
        {list.map((v) => (
          <Badge key={v} tone="violet" className="font-mono">
            {v}
            <button onClick={() => onChange(list.filter((x) => x !== v))} aria-label={`Remove ${v}`}>
              <X className="size-3" />
            </button>
          </Badge>
        ))}
      </div>
      <input
        className="input"
        value={text}
        placeholder={placeholder ?? 'Type and press Enter'}
        onChange={(e) => setText(e.target.value)}
        onBlur={add}
        onKeyDown={(e) => {
          if (e.key === 'Enter' || e.key === ',') {
            e.preventDefault()
            add()
          }
        }}
      />
    </div>
  )
}

function VariableMenu({ ref_, context, onPick }: { ref_: Reference; context?: string; onPick: (v: string) => void }) {
  const vars = ref_.variables.filter((v) => !context || v.contexts.includes(context))
  return (
    <Menu
      trigger={(open) => (
        <button type="button" onClick={open} className="flex shrink-0 items-center gap-1 rounded-lg border border-slate-300 px-2 text-xs text-slate-600 hover:bg-slate-50 dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800" title="Insert variable">
          <Braces className="size-3.5" /> Var
        </button>
      )}
      items={vars.map((v) => ({ label: v.name, onClick: () => onPick(v.name) }))}
    />
  )
}

export function ParamField({ ps, value, onChange, ref_, context }: { ps: ParamSpec; value: any; onChange: (v: any) => void; ref_: Reference; context?: string }) {
  switch (ps.type) {
    case 'boolean':
      return <Toggle checked={!!value} onChange={onChange} label={ps.name === 'negate' ? 'Invert result (NOT)' : ps.name.replace(/_/g, ' ')} description={ps.description} />
    case 'enum':
      return (
        <select className="input" value={value ?? ''} onChange={(e) => onChange(e.target.value)}>
          {!ps.required && <option value="">(default)</option>}
          {ps.options?.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      )
    case 'integer':
      return <input className="input" type="number" min={ps.min} max={ps.max} value={value ?? ''} onChange={(e) => onChange(e.target.value === '' ? undefined : Math.round(Number(e.target.value)))} />
    case 'bytes':
      return <BytesInput value={value} onChange={onChange} />
    case 'string_list':
      return <ListInput value={value} onChange={onChange} placeholder={ps.name === 'extensions' ? 'mp4, mkv…' : undefined} />
    case 'date':
      return <input className="input" type="date" value={typeof value === 'string' ? value.slice(0, 10) : ''} onChange={(e) => onChange(e.target.value)} />
    case 'variable':
      return (
        <select className="input font-mono" value={value ?? ''} onChange={(e) => onChange(e.target.value)}>
          <option value="">Choose a variable…</option>
          {ref_.variables
            .filter((v) => !context || v.contexts.includes(context))
            .map((v) => (
              <option key={v.name} value={v.name}>
                {v.name} ({v.type})
              </option>
            ))}
        </select>
      )
    case 'template':
      return (
        <div className="flex gap-2">
          <input className="input font-mono text-xs" value={value ?? ''} placeholder={typeof ps.example === 'string' ? ps.example : ''} onChange={(e) => onChange(e.target.value)} />
          <VariableMenu ref_={ref_} context={context} onPick={(v) => onChange(`${value ?? ''}{{${v}}}`)} />
        </div>
      )
    case 'rules':
      return <RulesEditor value={value} onChange={onChange} ref_={ref_} context={context} depth={0} />
    default:
      return <input className={cx('input', ps.type === 'glob' && 'font-mono text-xs')} value={value ?? ''} placeholder={typeof ps.example === 'string' ? ps.example : ''} onChange={(e) => onChange(e.target.value)} />
  }
}

export function ParamsForm({ spec, params, onChange, ref_, context }: { spec: KindSpec; params: Record<string, any>; onChange: (p: Record<string, any>) => void; ref_: Reference; context?: string }) {
  if (!spec.params.length) return <p className="text-xs text-slate-500">This step has no settings.</p>
  return (
    <div className="space-y-4">
      {spec.params.map((ps) => (
        <div key={ps.name}>
          {ps.type !== 'boolean' && (
            <label className="label flex items-center gap-1">
              {ps.name.replace(/_/g, ' ')}
              {ps.required && <span className="text-rose-500">*</span>}
            </label>
          )}
          <ParamField
            ps={ps}
            value={params[ps.name]}
            ref_={ref_}
            context={context}
            onChange={(v) => {
              const next = { ...params }
              if (v === undefined || (Array.isArray(v) && v.length === 0 && !ps.required)) delete next[ps.name]
              else next[ps.name] = v
              onChange(next)
            }}
          />
          {ps.type !== 'boolean' && <p className="mt-1 text-[11px] text-slate-500">{ps.description}</p>}
        </div>
      ))}
    </div>
  )
}

function RulesEditor({ value, onChange, ref_, context, depth }: { value: any; onChange: (v: any[]) => void; ref_: Reference; context?: string; depth: number }) {
  const rules: { kind: string; params?: Record<string, any> }[] = Array.isArray(value) ? value : []
  const conds = ref_.catalog.condition.filter((c) => (depth < 4 || c.kind !== 'group') && (!context || !c.contexts || c.contexts.includes(context)))
  const set = (i: number, r: any) => onChange(rules.map((x, j) => (j === i ? r : x)))
  return (
    <div className="space-y-2">
      {rules.map((r, i) => {
        const spec = ref_.catalog.condition.find((c) => c.kind === r.kind)
        return (
          <div key={i} className={cx('rounded-xl border p-3', depth % 2 ? 'border-slate-200 bg-white dark:border-slate-700 dark:bg-slate-900' : 'border-slate-200 bg-slate-50 dark:border-slate-700 dark:bg-slate-800/60')}>
            <div className="mb-2 flex items-center gap-2">
              <select
                className="input h-8 py-0 text-xs"
                value={r.kind}
                onChange={(e) => {
                  const s = ref_.catalog.condition.find((c) => c.kind === e.target.value)!
                  set(i, { kind: s.kind, params: defaultParams(s) })
                }}
              >
                {conds.map((c) => (
                  <option key={c.kind} value={c.kind}>
                    {c.title}
                  </option>
                ))}
              </select>
              <button className="rounded-md p-1.5 text-slate-400 hover:bg-rose-50 hover:text-rose-600 dark:hover:bg-rose-500/10" onClick={() => onChange(rules.filter((_, j) => j !== i))} aria-label="Remove rule">
                <Trash2 className="size-4" />
              </button>
            </div>
            {spec && <ParamsForm spec={spec} params={r.params ?? {}} onChange={(p) => set(i, { ...r, params: p })} ref_={ref_} context={context} />}
          </div>
        )
      })}
      <Button
        size="sm"
        icon={<Plus className="size-3.5" />}
        onClick={() => {
          const s = ref_.catalog.condition.find((c) => c.kind === 'name.contains')!
          onChange([...rules, { kind: s.kind, params: defaultParams(s) }])
        }}
      >
        Add rule
      </Button>
    </div>
  )
}
