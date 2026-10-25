import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Cpu, Download, RefreshCw, Trash2 } from 'lucide-react'
import { Badge, Button, ErrorState, Field, PageLoader, ProgressBar, Toggle, useUI } from '../../components/ui'
import { errorMessage } from '../../lib/api'
import { formatBytes } from '../../lib/format'
import { smartApi, smartKeys, type SmartSettings } from './api'

/** Administrator settings: processing intensity, the inference worker and its models. */
export default function SettingsPage() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const settings = useQuery({ queryKey: smartKeys.sub('admin-settings'), queryFn: smartApi.adminSettings })
  const status = useQuery({ queryKey: smartKeys.sub('status-fresh'), queryFn: () => smartApi.status(true), refetchInterval: (q) => (Object.values(q.state.data?.worker.models ?? {}).some((m) => m.downloading) ? 2000 : 15000) })
  const [v, setV] = useState<SmartSettings | null>(null)
  const [busy, setBusy] = useState('')
  useEffect(() => {
    if (settings.data && !v) setV(settings.data.settings)
  }, [settings.data, v])
  if (settings.isLoading || !v) return <PageLoader />
  if (settings.error) return <ErrorState error={settings.error} onRetry={() => settings.refetch()} />
  const set = <K extends keyof SmartSettings>(k: K, x: SmartSettings[K]) => setV({ ...v, [k]: x })
  const num = (k: keyof SmartSettings, label: string, hint: string, min: number, max: number, step = 1) => (
    <Field label={label} hint={hint}>
      <input className="input" type="number" min={min} max={max} step={step} value={v[k] as number} onChange={(e) => set(k, Number(e.target.value) as never)} />
    </Field>
  )
  const save = async () => {
    setBusy('save')
    try {
      await smartApi.saveAdminSettings(v)
      toast('success', 'Settings saved')
      qc.invalidateQueries({ queryKey: smartKeys.all })
    } catch (e) {
      toast('error', 'Not saved', errorMessage(e))
    } finally {
      setBusy('')
    }
  }
  const models = status.data?.worker.models ?? {}
  const modelAction = async (name: string, install: boolean) => {
    if (!install && !(await confirm({ title: `Remove ${models[name]?.title ?? name}?`, message: 'Detections that need it stop working until it is installed again. Measurements already made stay cached.', confirmLabel: 'Remove model' }))) return
    setBusy(name)
    try {
      await smartApi.models(install ? { install: [name] } : { remove: [name] })
      status.refetch()
    } catch (e) {
      toast('error', 'The worker refused', errorMessage(e))
    } finally {
      setBusy('')
    }
  }
  const missing = Object.entries(models).filter(([, m]) => !m.installed && !m.optional && !m.downloading)
  const res = status.data?.resources ?? {}
  return (
    <div className="space-y-5">
      <p className="text-sm text-slate-500 dark:text-slate-400">Server-wide settings for every user: processing intensity, the inference worker and its models. Everything runs on this server; nothing is sent elsewhere.</p>

      <div className="card space-y-4 p-5">
        <h2 className="font-semibold">Processing</h2>
        <Toggle checked={v.enabled} onChange={(x) => set('enabled', x)} label="Process queues" description="Off: queues, reviews and cleanup stay available, but nothing is analysed or rendered." />
        <div className="grid gap-4 sm:grid-cols-3">
          {num('analysis_concurrency', 'Recordings analysed at once', '1 keeps the server responsive', 1, 16)}
          {num('render_concurrency', 'Recordings rendered at once', 'Clips rendered in parallel', 1, 8)}
          {num('ffmpeg_threads', 'FFmpeg threads', '0 lets FFmpeg decide', 0, 64)}
          {num('max_minutes', 'Longest recording (minutes)', '0 for no limit', 0, 1440)}
          {num('frame_rate', 'Frames per second analysed', 'Higher finds short visual events, costs more', 0.1, 5, 0.1)}
          {num('frame_width', 'Frame width (pixels)', 'Larger reads smaller on-screen text', 224, 1920, 16)}
          {num('ocr_every', 'Seconds between OCR reads', 'Lower catches brief text', 0.5, 60, 0.5)}
          {num('chunk_seconds', 'Audio chunk (seconds)', 'Checkpoint interval for long recordings', 60, 1800, 60)}
          <Field label="Hardware decoding" hint="GPU decoding when available; falls back to the CPU">
            <select className="input" value={v.hwaccel} onChange={(e) => set('hwaccel', e.target.value)}>
              <option value="">Off (CPU)</option>
              <option value="auto">Automatic</option>
              <option value="cuda">NVIDIA (cuda)</option>
              <option value="vaapi">VA-API</option>
              <option value="qsv">Intel Quick Sync</option>
            </select>
          </Field>
          <Field label="Speech language" hint="Empty detects it per recording (two-letter code, e.g. en)">
            <input className="input" value={v.language} maxLength={3} onChange={(e) => set('language', e.target.value.toLowerCase())} />
          </Field>
        </div>
        <Toggle checked={v.low_priority} onChange={(x) => set('low_priority', x)} label="Low priority" description="Analysis yields the CPU to file transfers and other work." />
        <Toggle checked={v.use_language_model} onChange={(x) => set('use_language_model', x)} label="Interpret descriptions with the local language model" description="Needs the optional language model below; the built-in interpreter is used otherwise." />
      </div>

      <div className="card space-y-4 p-5">
        <div className="flex items-center gap-2">
          <h2 className="font-semibold">Inference worker</h2>
          {status.data?.worker.reachable ? <Badge tone="green">online</Badge> : status.data?.worker.configured ? <Badge tone="red">unreachable</Badge> : <Badge tone="slate">not configured</Badge>}
          <div className="flex-1" />
          <Button size="sm" variant="ghost" icon={<RefreshCw className="size-3.5" />} onClick={() => status.refetch()}>
            Refresh
          </Button>
        </div>
        <Field label="Worker URL" hint={`Empty uses ${settings.data?.env_worker_url || 'VAULTDOCK_SMART_WORKER_URL (not set)'}. The token comes from VAULTDOCK_SMART_TOKEN${settings.data?.token_set ? ' (set)' : ' (not set)'}. Enable the worker with: install.sh integrations smart on`}>
          <input className="input" value={v.worker_url} placeholder={settings.data?.env_worker_url || 'http://smart-worker:8790'} onChange={(e) => set('worker_url', e.target.value)} />
        </Field>
        {status.data?.worker.error && <p className="text-sm text-rose-600">{status.data.worker.error}</p>}
        {status.data?.worker.reachable && (
          <p className="flex items-center gap-2 text-xs text-slate-500">
            <Cpu className="size-3.5" /> {status.data.worker.device}
            {status.data.worker.gpu ? ` (${status.data.worker.gpu})` : ''} · {status.data.worker.threads} threads · {formatBytes(status.data.worker.rss ?? 0)} memory · {status.data.worker.cpu_percent}% CPU
          </p>
        )}
        <div className="text-xs text-slate-500">
          Server: {String(res.cpus ?? '?')} CPUs{res.load1 !== undefined ? `, load ${res.load1}` : ''}
          {res.mem_available ? `, ${formatBytes(Number(res.mem_available))} memory free` : ''}
        </div>
      </div>

      {Object.keys(models).length > 0 && (
        <div className="card space-y-3 p-5">
          <div className="flex items-center gap-2">
            <h2 className="font-semibold">Models</h2>
            <div className="flex-1" />
            {missing.length > 0 && (
              <Button size="sm" icon={<Download className="size-3.5" />} loading={busy === 'all'} onClick={async () => {
                setBusy('all')
                try {
                  await smartApi.models({ install: missing.map(([n]) => n) })
                  status.refetch()
                } catch (e) {
                  toast('error', 'The worker refused', errorMessage(e))
                } finally {
                  setBusy('')
                }
              }}>
                Install all standard models ({formatBytes(missing.reduce((n, [, m]) => n + m.estimate, 0))})
              </Button>
            )}
          </div>
          <p className="text-xs text-slate-500">Models are downloaded only when you install them, then run offline.</p>
          <div className="divide-y divide-slate-100 dark:divide-slate-800">
            {Object.entries(models).map(([name, m]) => (
              <div key={name} className="flex flex-wrap items-center gap-3 py-2.5 text-sm">
                <div className="min-w-60 flex-1">
                  <div className="font-medium">
                    {m.title} {m.optional && <Badge tone="slate">optional</Badge>}
                  </div>
                  <div className="text-xs text-slate-500">
                    {m.capability} · {m.license} · {m.source}
                  </div>
                  {m.error && <div className="text-xs text-rose-600">{m.error}</div>}
                  {m.downloading && <ProgressBar className="mt-1 w-60" value={m.progress * 100} />}
                </div>
                <div className="text-xs text-slate-500">{m.installed ? formatBytes(m.bytes || m.estimate) : m.estimate ? `~${formatBytes(m.estimate)}` : ''}</div>
                {m.installed ? (
                  <>
                    <Badge tone="green">{m.loaded ? 'loaded' : 'installed'}</Badge>
                    {m.bytes > 0 && (
                      <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} loading={busy === name} onClick={() => modelAction(name, false)}>
                        Remove
                      </Button>
                    )}
                  </>
                ) : m.downloading ? (
                  <Badge tone="blue">downloading {Math.round(m.progress * 100)}%</Badge>
                ) : m.estimate > 0 ? (
                  <Button size="sm" icon={<Download className="size-3.5" />} loading={busy === name} onClick={() => modelAction(name, true)}>
                    Install
                  </Button>
                ) : (
                  <Badge tone="slate">not available in this image</Badge>
                )}
              </div>
            ))}
          </div>
        </div>
      )}

      <Button variant="primary" loading={busy === 'save'} onClick={save}>
        Save settings
      </Button>
    </div>
  )
}
