import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { errorMessage, get, put } from '../../../lib/api'
import { formatBytes } from '../../../lib/format'
import { Button, ErrorState, Field, PageLoader, Toggle, useUI } from '../../../components/ui'
import { QuotaInput } from './QuotaInput'
import { SizeInput } from '../../../components/SizeInput'

export default function SettingsTab() {
  const { toast } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['admin-settings'], queryFn: () => get('/admin/settings') })
  const [s, setS] = useState<any>(null)
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (data) setS(data.settings)
  }, [data])
  // Error first: `s` stays null when the load fails, so checking `!s` first spun forever.
  if (error) return <ErrorState error={error} onRetry={refetch} />
  if (isLoading || !s) return <PageLoader />
  const save = async () => {
    setBusy(true)
    try {
      await put('/admin/settings', s)
      toast('success', 'Settings saved')
    } catch (e) {
      toast('error', 'Could not save settings', errorMessage(e))
    } finally {
      setBusy(false)
    }
  }
  const num = (k: string) => (e: React.ChangeEvent<HTMLInputElement>) => setS({ ...s, [k]: Number(e.target.value) })
  return (
    <div className="grid gap-6 xl:grid-cols-[1fr_360px]">
      <div className="card space-y-6 p-6">
        <Field label="Site name">
          <input className="input" value={s.site_name} onChange={(e) => setS({ ...s, site_name: e.target.value })} />
        </Field>
        <Toggle checked={s.registration_enabled} onChange={(v) => setS({ ...s, registration_enabled: v })} label="Allow self-registration" description="When off, only administrators can create accounts. Keep off for internet-facing servers unless you trust everyone who can reach it." />
        <Field label="Default quota for new users">
          <QuotaInput value={s.default_quota_bytes} onChange={(v) => setS((x: any) => ({ ...x, default_quota_bytes: v }))} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Maximum file size (0 = unlimited)">
            <SizeInput value={s.max_file_bytes} onChange={(v) => setS({ ...s, max_file_bytes: v })} />
          </Field>
          <Field label="Always keep free on every disk" hint="Hard floor that protects the host from filling up.">
            <SizeInput value={s.min_free_bytes} onChange={(v) => setS({ ...s, min_free_bytes: v })} />
          </Field>
          <Field label="Trash retention (days, 0 = keep)">
            <input className="input" type="number" min={0} value={s.trash_retention_days} onChange={num('trash_retention_days')} />
          </Field>
          <Field label="Activity retention (days)">
            <input className="input" type="number" min={7} value={s.activity_retention_days} onChange={num('activity_retention_days')} />
          </Field>
          <Field label="Device request expiry (minutes)">
            <input className="input" type="number" min={2} max={1440} value={s.device_request_ttl_minutes} onChange={num('device_request_ttl_minutes')} />
          </Field>
        </div>
        <Toggle checked={s.keep_replaced_versions} onChange={(v) => setS({ ...s, keep_replaced_versions: v })} label="Keep replaced versions in trash" description="When a synced file changes, the previous version moves to the owner's trash instead of being deleted immediately." />
        <div className="flex justify-end">
          <Button variant="primary" loading={busy} onClick={save}>
            Save settings
          </Button>
        </div>
      </div>
      <div className="card h-fit p-6">
        <h3 className="mb-3 font-semibold">Server configuration</h3>
        <p className="mb-4 text-xs text-slate-500">Set through environment variables in the .env file. Restart VaultDock after changing them.</p>
        <dl className="space-y-2 text-sm">
          {Object.entries(data.config).map(([k, v]) => (
            <div key={k} className="flex justify-between gap-3">
              <dt className="text-slate-500">{k.replace(/_/g, ' ')}</dt>
              <dd className="text-right font-mono text-xs break-all">{Array.isArray(v) ? v.join(', ') : typeof v === 'boolean' ? (v ? 'yes' : 'no') : k === 'chunk_size' ? formatBytes(v as number) : String(v || '-')}</dd>
            </div>
          ))}
        </dl>
      </div>
    </div>
  )
}
