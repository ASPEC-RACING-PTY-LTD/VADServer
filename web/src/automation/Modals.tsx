import { useMemo, useRef, useState } from 'react'
import { AlertCircle, AlertTriangle, CheckCircle2, FileJson, Play, Search, SkipForward, Upload } from 'lucide-react'
import { errorMessage, post } from '../lib/api'
import type { Automation, Reference, Validation } from './model'
import { Badge, Button, CopyButton, cx, Modal, PageLoader, Tabs } from '../components/ui'

export function IssueList({ v }: { v: Validation }) {
  if (v.valid && v.warnings.length === 0)
    return (
      <div className="flex items-center gap-2 rounded-lg bg-emerald-50 px-3 py-2 text-sm text-emerald-800 dark:bg-emerald-500/10 dark:text-emerald-300">
        <CheckCircle2 className="size-4" /> Valid automation
      </div>
    )
  return (
    <div className="space-y-1.5">
      {v.errors.map((e, i) => (
        <div key={'e' + i} className="flex gap-2 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-800 dark:bg-rose-500/10 dark:text-rose-300">
          <AlertCircle className="mt-0.5 size-4 shrink-0" />
          <div className="min-w-0">
            <code className="text-xs font-semibold break-all">{e.path}</code>
            <div>{e.message}</div>
          </div>
        </div>
      ))}
      {v.warnings.map((e, i) => (
        <div key={'w' + i} className="flex gap-2 rounded-lg bg-amber-50 px-3 py-2 text-sm text-amber-900 dark:bg-amber-500/10 dark:text-amber-200">
          <AlertTriangle className="mt-0.5 size-4 shrink-0" />
          <div className="min-w-0">
            <code className="text-xs font-semibold break-all">{e.path}</code>
            <div>{e.message}</div>
          </div>
        </div>
      ))}
    </div>
  )
}

export function ReferenceModal({ open, onClose, ref_ }: { open: boolean; onClose: () => void; ref_?: Reference }) {
  const [tab, setTab] = useState<'variables' | 'steps' | 'ai' | 'schema'>('variables')
  const [q, setQ] = useState('')
  const vars = useMemo(() => (ref_?.variables ?? []).filter((v) => !q || (v.name + v.description).toLowerCase().includes(q.toLowerCase())), [ref_, q])
  const allVarsText = () => ['name\ttype\tdescription\texample\tapplies to', ...(ref_?.variables ?? []).map((v) => `${v.name}\t${v.type}\t${v.description}\t${v.example}\t${v.applies_to}`)].join('\n')
  return (
    <Modal open={open} onClose={onClose} size="full" title="Automation reference" description="Everything an automation can use, plus material you can give to an AI assistant.">
      {!ref_ ? (
        <PageLoader />
      ) : (
        <>
          <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
            <Tabs
              value={tab}
              onChange={setTab}
              tabs={[
                { id: 'variables', label: 'Variables' },
                { id: 'steps', label: 'Triggers, conditions & actions' },
                { id: 'ai', label: 'AI assistant guide' },
                { id: 'schema', label: 'JSON schema & example' },
              ]}
            />
            {tab === 'variables' && (
              <div className="flex gap-2">
                <div className="relative">
                  <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-slate-400" />
                  <input className="input h-8 w-56 pl-8" placeholder="Filter variables" value={q} onChange={(e) => setQ(e.target.value)} />
                </div>
                <CopyButton text={allVarsText} label="Copy all variables" />
              </div>
            )}
          </div>
          {tab === 'variables' && (
            <>
              <div className="overflow-x-auto rounded-xl border border-slate-200 dark:border-slate-800">
                <table className="w-full text-sm">
                  <thead className="bg-slate-50 text-left text-xs text-slate-500 uppercase dark:bg-slate-800/60">
                    <tr>
                      <th className="px-3 py-2">Variable</th>
                      <th className="px-3 py-2">Type</th>
                      <th className="px-3 py-2">Description</th>
                      <th className="px-3 py-2">Example</th>
                      <th className="px-3 py-2">Applies to</th>
                      <th className="px-3 py-2" />
                    </tr>
                  </thead>
                  <tbody>
                    {vars.map((v) => (
                      <tr key={v.name} className="border-t border-slate-100 align-top dark:border-slate-800">
                        <td className="px-3 py-2 font-mono text-xs font-semibold whitespace-nowrap text-brand-700 dark:text-brand-300">{v.name}</td>
                        <td className="px-3 py-2">
                          <Badge>{v.type}</Badge>
                        </td>
                        <td className="px-3 py-2 text-slate-600 dark:text-slate-300">{v.description}</td>
                        <td className="max-w-56 px-3 py-2 font-mono text-xs break-all text-slate-500">{v.example}</td>
                        <td className="px-3 py-2 text-xs text-slate-500">{v.applies_to}</td>
                        <td className="px-3 py-2">
                          <CopyButton text={`{{${v.name}}}`} label="Copy" />
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <h3 className="mt-6 mb-2 text-sm font-semibold">Template filters</h3>
              <div className="grid gap-2 md:grid-cols-2">
                {ref_.filters.map((f) => (
                  <div key={f.name} className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
                    <div className="flex items-center justify-between gap-2">
                      <code className="text-xs font-semibold">{f.usage}</code>
                      <CopyButton text={f.usage} label="Copy" />
                    </div>
                    <p className="mt-1 text-xs text-slate-500">{f.description}</p>
                  </div>
                ))}
              </div>
            </>
          )}
          {tab === 'steps' && (
            <div className="grid gap-6 lg:grid-cols-3">
              {(['trigger', 'condition', 'action'] as const).map((t) => (
                <div key={t}>
                  <h3 className="mb-2 text-sm font-semibold capitalize">{t}s</h3>
                  <div className="space-y-2">
                    {ref_.catalog[t].map((k) => (
                      <div key={k.kind} className="rounded-lg border border-slate-200 p-3 dark:border-slate-800">
                        <div className="flex items-center justify-between gap-2">
                          <span className="text-sm font-medium">{k.title}</span>
                          <code className="text-[11px] text-slate-500">{k.kind}</code>
                        </div>
                        <p className="mt-1 text-xs text-slate-500">{k.description}</p>
                        {k.params.filter((p) => p.name !== 'negate').length > 0 && (
                          <ul className="mt-2 space-y-0.5 text-[11px] text-slate-600 dark:text-slate-400">
                            {k.params
                              .filter((p) => p.name !== 'negate')
                              .map((p) => (
                                <li key={p.name}>
                                  <code className="font-semibold">{p.name}</code> <span className="text-slate-400">({p.type}{p.required ? ', required' : ''})</span>
                                </li>
                              ))}
                          </ul>
                        )}
                      </div>
                    ))}
                  </div>
                </div>
              ))}
            </div>
          )}
          {tab === 'ai' && (
            <div>
              <div className="mb-3 flex flex-wrap items-center justify-between gap-2 rounded-xl bg-brand-50 p-4 text-sm text-brand-900 dark:bg-brand-500/10 dark:text-brand-200">
                <span>Copy this guide into an AI assistant, describe what you want, then paste the JSON it produces into Import. VaultDock validates it before anything runs.</span>
                <CopyButton text={ref_.ai_guide} label="Copy AI guide" size="md" />
              </div>
              <pre className="scroll-thin max-h-[55vh] overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-xs leading-relaxed whitespace-pre-wrap text-slate-100">{ref_.ai_guide}</pre>
            </div>
          )}
          {tab === 'schema' && (
            <div className="grid gap-4 lg:grid-cols-2">
              <div>
                <div className="mb-2 flex items-center justify-between">
                  <h3 className="text-sm font-semibold">Example automation</h3>
                  <CopyButton text={ref_.example} label="Copy example" />
                </div>
                <pre className="scroll-thin max-h-[55vh] overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-xs text-slate-100">{ref_.example}</pre>
              </div>
              <div>
                <div className="mb-2 flex items-center justify-between">
                  <h3 className="text-sm font-semibold">JSON Schema (draft 2020-12)</h3>
                  <CopyButton text={() => JSON.stringify(ref_.json_schema, null, 2)} label="Copy schema" />
                </div>
                <pre className="scroll-thin max-h-[55vh] overflow-auto rounded-xl bg-slate-950 p-4 font-mono text-xs text-slate-100">{JSON.stringify(ref_.json_schema, null, 2)}</pre>
              </div>
            </div>
          )}
        </>
      )}
    </Modal>
  )
}

export function ImportModal({ open, onClose, onImport }: { open: boolean; onClose: () => void; onImport: (a: Automation, v: Validation) => void }) {
  const [text, setText] = useState('')
  const [result, setResult] = useState<(Validation & { definition: Automation | null }) | null>(null)
  const [busy, setBusy] = useState(false)
  const [parseError, setParseError] = useState('')
  const fileRef = useRef<HTMLInputElement>(null)

  const validate = async (raw = text) => {
    setParseError('')
    setResult(null)
    let parsed: unknown
    try {
      parsed = JSON.parse(raw)
    } catch (e) {
      setParseError(`Not valid JSON: ${errorMessage(e)}`)
      return
    }
    setBusy(true)
    try {
      setResult(await post('/automations/validate', parsed))
    } catch (e) {
      setParseError(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Modal
      open={open}
      onClose={onClose}
      size="lg"
      title="Import automation JSON"
      description="Paste JSON exported from VaultDock or generated by an AI assistant."
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button icon={<CheckCircle2 className="size-4" />} loading={busy} disabled={!text.trim()} onClick={() => validate()}>
            Validate
          </Button>
          <Button
            variant="primary"
            icon={<FileJson className="size-4" />}
            disabled={!result?.definition}
            onClick={() => {
              if (result?.definition) {
                onImport(result.definition, result)
                setText('')
                setResult(null)
              }
            }}
          >
            {result && !result.valid ? 'Open with errors' : 'Open in editor'}
          </Button>
        </>
      }
    >
      <div className="mb-2 flex justify-end">
        <Button size="sm" icon={<Upload className="size-3.5" />} onClick={() => fileRef.current?.click()}>
          Load file
        </Button>
        <input
          ref={fileRef}
          type="file"
          accept=".json,application/json"
          hidden
          onChange={async (e) => {
            const f = e.target.files?.[0]
            if (f) {
              const t = await f.text()
              setText(t)
              validate(t)
            }
            e.target.value = ''
          }}
        />
      </div>
      <textarea className="input h-64 font-mono text-xs" spellCheck={false} placeholder='{ "schema": "vaultdock.automation/v1", ... }' value={text} onChange={(e) => setText(e.target.value)} />
      {parseError && <div className="mt-3 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-800 dark:bg-rose-500/10 dark:text-rose-300">{parseError}</div>}
      {result && (
        <div className="mt-3">
          <IssueList v={result} />
          {!result.valid && result.definition && <p className="mt-2 text-xs text-slate-500">You can open it in the editor to fix the problems visually. It cannot be enabled until it is valid.</p>}
        </div>
      )}
    </Modal>
  )
}

type Sample = { type: string; rel_path: string; size: string; created: string; modified: string; config: string; destination: string }

const nowLocal = () => new Date(Date.now() - new Date().getTimezoneOffset() * 60000).toISOString().slice(0, 16)

export function DryRunModal({ open, onClose, automation }: { open: boolean; onClose: () => void; automation: Automation }) {
  const [samples, setSamples] = useState<Sample[]>([
    { type: 'file.created', rel_path: 'Valorant/Valorant 2026.10.16 - 21.04.11.mp4', size: '250', created: nowLocal(), modified: nowLocal(), config: 'Medal Clips', destination: '/Medal' },
    { type: 'file.created', rel_path: 'Valorant/recording.mp4.tmp', size: '40', created: nowLocal(), modified: nowLocal(), config: 'Medal Clips', destination: '/Medal' },
  ])
  const [results, setResults] = useState<any[] | null>(null)
  const [validation, setValidation] = useState<Validation | null>(null)
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const set = (i: number, k: keyof Sample, v: string) => setSamples(samples.map((s, j) => (j === i ? { ...s, [k]: v } : s)))
  const run = async () => {
    setBusy(true)
    setErr('')
    try {
      const events = samples.map((s) => {
        const ev: any = { type: s.type, config: s.config, destination: s.destination || '/', root: 'C:\\Sync', device: 'DRY-RUN', now: new Date().toISOString() }
        if (s.type.startsWith('file.')) {
          ev.file = { path: 'C:\\Sync\\' + s.rel_path.replace(/\//g, '\\'), rel_path: s.rel_path, size: Math.round(Number(s.size) * 1024 * 1024), created: new Date(s.created).toISOString(), modified: new Date(s.modified).toISOString() }
        } else if (s.type.startsWith('sync.')) {
          ev.sync = { files_uploaded: 10, files_failed: s.type === 'sync.failed' ? 2 : 0, bytes_uploaded: 1 << 30, error: s.type === 'sync.failed' ? 'connection reset' : '' }
        }
        return ev
      })
      const res = await post('/automations/dry-run', { definition: automation, events })
      setValidation(res.validation)
      setResults(res.results)
    } catch (e) {
      setErr(errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Modal
      open={open}
      onClose={onClose}
      size="full"
      title="Test automation (dry run)"
      description="Simulates events against this automation. Nothing is uploaded, moved or sent."
      footer={
        <>
          <Button onClick={onClose}>Close</Button>
          <Button variant="primary" icon={<Play className="size-4" />} loading={busy} onClick={run}>
            Run test
          </Button>
        </>
      }
    >
      <div className="space-y-2">
        {samples.map((s, i) => (
          <div key={i} className="grid gap-2 rounded-xl border border-slate-200 p-3 md:grid-cols-[150px_1fr_90px_190px_130px_110px_auto] dark:border-slate-800">
            <select className="input h-8 py-0 text-xs" value={s.type} onChange={(e) => set(i, 'type', e.target.value)}>
              {['file.created', 'file.modified', 'file.renamed', 'file.deleted', 'sync.completed', 'sync.failed'].map((t) => (
                <option key={t}>{t}</option>
              ))}
            </select>
            <input className="input h-8 font-mono text-xs" placeholder="Folder/file.mp4 (relative to sync root)" value={s.rel_path} onChange={(e) => set(i, 'rel_path', e.target.value)} disabled={!s.type.startsWith('file.')} />
            <input className="input h-8 text-xs" type="number" title="Size in MB" value={s.size} onChange={(e) => set(i, 'size', e.target.value)} disabled={!s.type.startsWith('file.')} />
            <input className="input h-8 text-xs" type="datetime-local" title="Created / modified" value={s.created} onChange={(e) => setSamples(samples.map((x, j) => (j === i ? { ...x, created: e.target.value, modified: e.target.value } : x)))} disabled={!s.type.startsWith('file.')} />
            <input className="input h-8 text-xs" placeholder="Sync config" value={s.config} onChange={(e) => set(i, 'config', e.target.value)} />
            <input className="input h-8 font-mono text-xs" placeholder="/Destination" value={s.destination} onChange={(e) => set(i, 'destination', e.target.value)} />
            <Button size="sm" variant="ghost" onClick={() => setSamples(samples.filter((_, j) => j !== i))} disabled={samples.length === 1}>
              Remove
            </Button>
          </div>
        ))}
        <div className="flex items-center justify-between">
          <Button size="sm" onClick={() => setSamples([...samples, { ...samples[samples.length - 1] }])}>
            Add sample
          </Button>
          <span className="text-xs text-slate-500">Size in MB. Paths use forward slashes relative to the sync folder.</span>
        </div>
      </div>
      {err && <div className="mt-4 rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-800">{err}</div>}
      {validation && !validation.valid && (
        <div className="mt-4">
          <IssueList v={validation} />
        </div>
      )}
      {results && (
        <div className="mt-5 space-y-3">
          {results.map((r, i) => {
            const plan = r.plan
            const out = plan.outcomes?.[0]
            const skip = plan.decision === 'skip'
            return (
              <div key={i} className="rounded-xl border border-slate-200 p-4 dark:border-slate-800">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge tone={out?.triggered ? 'violet' : 'slate'}>{out?.triggered ? 'triggered' : 'not triggered'}</Badge>
                  {r.event.file && (
                    <Badge tone={skip ? 'amber' : 'green'}>
                      {skip ? <SkipForward className="size-3" /> : <Upload className="size-3" />} {skip ? 'skip' : 'upload'}
                    </Badge>
                  )}
                  <span className="font-mono text-xs text-slate-600 dark:text-slate-300">{r.event.file?.rel_path ?? r.event.type}</span>
                </div>
                {r.event.file && !skip && (
                  <div className="mt-2 text-sm">
                    → <span className="font-mono text-xs font-semibold text-emerald-700 dark:text-emerald-300">{(plan.remote_dir === '/' ? '' : plan.remote_dir) + '/' + plan.remote_name}</span>
                  </div>
                )}
                {skip && plan.skip_reason && <div className="mt-2 text-sm text-amber-700 dark:text-amber-300">Reason: {plan.skip_reason}</div>}
                <div className="mt-2 flex flex-wrap gap-1.5">
                  {(plan.tags ?? []).map((t: string) => (
                    <Badge key={t} tone="violet">
                      #{t}
                    </Badge>
                  ))}
                  {(plan.create_folders ?? []).map((f: string) => (
                    <Badge key={f}>mkdir {f}</Badge>
                  ))}
                  {(plan.start_tasks ?? []).map((f: string) => (
                    <Badge key={f} tone="blue">
                      run {f}
                    </Badge>
                  ))}
                  {(plan.notifications ?? []).map((n: any, k: number) => (
                    <Badge key={k} tone="amber">
                      notify: {n.title}
                    </Badge>
                  ))}
                </div>
                {out?.trace?.length > 0 && (
                  <details className="mt-3">
                    <summary className="cursor-pointer text-xs font-medium text-slate-500">Execution trace ({out.trace.length} steps)</summary>
                    <ol className="mt-2 space-y-1 text-xs">
                      {out.trace.map((t: any, k: number) => (
                        <li key={k} className="flex gap-2">
                          <span className={cx('w-20 shrink-0 font-semibold', t.result === 'true' || t.result === 'matched' || t.result === 'applied' ? 'text-emerald-600' : t.result === 'false' ? 'text-rose-600' : 'text-slate-400')}>{t.result}</span>
                          <code className="text-slate-600 dark:text-slate-300">{t.node_id}</code>
                          <span className="text-slate-500">{t.detail}</span>
                        </li>
                      ))}
                    </ol>
                  </details>
                )}
              </div>
            )
          })}
        </div>
      )}
    </Modal>
  )
}
