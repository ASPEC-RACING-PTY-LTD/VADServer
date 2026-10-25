import { useEffect, useState } from 'react'
import { Link } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, Settings } from 'lucide-react'
import { Badge, Button, cx, ErrorState, PageHeader, PageLoader, Toggle, useUI } from '../../components/ui'
import { MoveModal } from '../../components/files/MoveModal'
import { errorMessage } from '../../lib/api'
import { useAuth } from '../../lib/auth'
import { smartApi, smartKeys, usePresets, useSmartStatus, type Prefs } from './api'
import { ContextEditor, OutputEditor } from './shared'

/** Your smart processing defaults, and what this server can detect (models are managed by administrators). */
export default function PreferencesPage() {
  const qc = useQueryClient()
  const { toast } = useUI()
  const { state } = useAuth()
  const isAdmin = state.status === 'authenticated' && state.user.role === 'admin'
  const prefs = useQuery({ queryKey: smartKeys.sub('prefs'), queryFn: smartApi.prefs })
  const presets = usePresets()
  const status = useSmartStatus()
  const [v, setV] = useState<Prefs | null>(null)
  const [pick, setPick] = useState(false)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (prefs.data && !v) setV(prefs.data)
  }, [prefs.data, v])
  if (prefs.isLoading || !v) return <PageLoader />
  if (prefs.error) return <ErrorState error={prefs.error} onRetry={() => prefs.refetch()} />

  const save = async () => {
    setBusy(true)
    try {
      setV(await smartApi.savePrefs(v))
      qc.invalidateQueries({ queryKey: smartKeys.sub('prefs') })
      toast('success', 'Saved', 'New queues start with these settings.')
    } catch (e) {
      toast('error', 'Not saved', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const detectors = status.data?.detectors ?? []
  return (
    <div className="space-y-5">
      <PageHeader title="Settings" icon={<Settings className="size-5" />} description="Your defaults for new queues. They apply to you only; each queue can still be changed when you create it." />

      <div className="card space-y-3 p-5">
        <h2 className="font-semibold">Default presets</h2>
        <div className="flex flex-wrap gap-2">
          {(presets.data?.presets ?? [])
            .filter((p) => p.id !== 'builtin:custom')
            .map((p) => {
              const on = v.preset_ids.includes(p.id)
              return (
                <button
                  key={p.id}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setV({ ...v, preset_ids: on ? v.preset_ids.filter((x) => x !== p.id) : [...v.preset_ids, p.id] })}
                  className={cx('flex items-center gap-1 rounded-full border px-3 py-1 text-sm', on ? 'border-brand-500 bg-brand-50 dark:bg-brand-500/15' : 'border-slate-300 dark:border-slate-600')}
                >
                  {on && <Check className="size-3.5" />}
                  {p.name}
                </button>
              )
            })}
        </div>
      </div>

      <div className="card space-y-5 p-5">
        <h2 className="font-semibold">Segments</h2>
        <ContextEditor value={v.context} onChange={(c) => setV({ ...v, context: c })} />
      </div>

      <div className="card space-y-5 p-5">
        <h2 className="font-semibold">Clips</h2>
        <OutputEditor value={v.output} onChange={(o) => setV({ ...v, output: o })} onPickFolder={() => setPick(true)} />
        <Toggle checked={v.auto_approve} onChange={(x) => setV({ ...v, auto_approve: x })} label="Auto-approve suggestions in new queues" description="Clips are rendered without review. Originals are never deleted automatically." />
      </div>

      <Button variant="primary" loading={busy} onClick={save}>
        Save settings
      </Button>

      <div className="card space-y-3 p-5">
        <div className="flex items-center gap-2">
          <h2 className="font-semibold">Available detection</h2>
          <div className="flex-1" />
          {isAdmin && (
            <Link to="/admin/smart" className="text-sm text-brand-600">
              Manage models and processing
            </Link>
          )}
        </div>
        <p className="text-sm text-slate-500">{isAdmin ? 'Install models under Administration > Smart processing.' : 'Detection marked not installed needs a model that an administrator can install.'}</p>
        <div className="grid gap-2 sm:grid-cols-2">
          {detectors.map((d) => (
            <div key={d.id} className="text-sm">
              <Badge tone={d.available ? 'green' : 'slate'}>{d.available ? 'available' : 'not installed'}</Badge> <span className="font-medium">{d.title}</span>
              <div className="text-xs text-slate-500">{d.description}</div>
            </div>
          ))}
        </div>
      </div>

      <MoveModal ids={pick ? [] : null} title="Folder for the clips" description="Clips are saved here, in folders mirroring the originals." actionLabel="Choose this folder" onClose={() => setPick(false)} onMove={(target) => setV({ ...v, output: { ...v.output, destination: 'folder', folder_id: target } })} />
    </div>
  )
}
