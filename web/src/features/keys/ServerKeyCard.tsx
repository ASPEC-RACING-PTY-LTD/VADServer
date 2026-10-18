import { useState } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { Download, KeyRound } from 'lucide-react'
import { Badge, Button, Spinner } from '../../components/ui'
import { downloadKEK, keyKeys, useKEKExportStatus, useServerKeyStatus } from './api'

/** Server key status (api-v1 §5A): SIK fingerprint and current SEK version, or unavailable. */
export function ServerKeyCard() {
  const q = useServerKeyStatus()
  return (
    <section className="card mb-8 p-5" aria-label="Server keys">
      <div className="mb-2 flex items-center gap-2">
        <KeyRound className="size-4 text-slate-400" />
        <h2 className="text-sm font-semibold tracking-wide text-slate-500 uppercase">Server keys</h2>
        {q.data?.state === 'available' && <Badge tone="green">Available</Badge>}
        {q.data?.state === 'unavailable' && <Badge tone="amber">Unavailable</Badge>}
      </div>
      {q.isPending ? (
        <Spinner />
      ) : q.isError ? (
        <p role="alert" className="text-sm text-rose-600">
          <code className="font-mono">{q.error.code}</code>: {q.error.message}
        </p>
      ) : q.data.state === 'unavailable' ? (
        <p className="text-sm text-slate-600 dark:text-slate-300">
          The server key store is not available (<code className="font-mono">escrow_unavailable</code>): {q.data.message}. Backups that need server escrow cannot run until an administrator configures the server key encryption key.
        </p>
      ) : (
        <div className="grid gap-3 sm:grid-cols-2">
          <div>
            <div className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">Server identity (SIK) fingerprint</div>
            <div className="font-mono text-sm break-all" data-testid="sik-fingerprint">
              {q.data.identity.sik_fingerprint}
            </div>
            <p className="mt-1 text-xs text-slate-500">Devices pin this fingerprint. It must match the one shown in the VaultDock desktop app.</p>
          </div>
          <div>
            <div className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">Escrow key (SEK) version</div>
            <div className="font-mono text-sm" data-testid="sek-version">
              v{q.data.identity.sek_version}
            </div>
          </div>
        </div>
      )}
      <KEKExport />
    </section>
  )
}

/** One-time download of the server key file (superusers only). */
function KEKExport() {
  const q = useKEKExportStatus()
  const qc = useQueryClient()
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState<string | null>(null)
  if (!q.data || !q.data.available) return null
  if (q.data.exported) {
    return (
      <p className="mt-4 text-xs text-slate-500">
        The server key file was downloaded{q.data.exported_at ? ` on ${new Date(q.data.exported_at).toLocaleString()}` : ''}. It cannot be downloaded again from the portal.
      </p>
    )
  }
  const go = async () => {
    if (!window.confirm('Download the server key file now? This works only once. Keep the file somewhere safe and private.')) return
    setBusy(true)
    setErr(null)
    try {
      await downloadKEK()
    } catch (e) {
      setErr(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
      void qc.invalidateQueries({ queryKey: keyKeys.sub('kek') })
    }
  }
  return (
    <div className="mt-4 rounded-md border border-amber-300 bg-amber-50 p-3 text-sm dark:border-amber-700 dark:bg-amber-950/40">
      <p className="mb-2">
        Download the server key file and keep it somewhere safe (a password manager or an encrypted drive). You need it to rebuild this server. It can be downloaded <strong>only once</strong>.
      </p>
      <Button size="sm" variant="primary" icon={<Download className="size-4" />} loading={busy} onClick={() => void go()}>
        Download server key
      </Button>
      {err && <p className="mt-2 text-rose-600">{err}</p>}
    </div>
  )
}
