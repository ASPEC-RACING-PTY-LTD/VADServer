import { useState } from 'react'
import { ShieldCheck, ShieldX, UserCheck, Users } from 'lucide-react'
import { ApiError } from '../../lib/api'
import { formatDate } from '../../lib/format'
import { Badge, Button, Spinner, useUI } from '../../components/ui'
import { useClearBlacklist, useDeviceProfiles, useRequestConsent } from './api'
import { blacklistReasonLabel, canRequestConsent, clearConfirmText, stateView, type DeviceProfile } from './types'

/** Server error as `code: message`, verbatim (e.g. `not_superuser`). */
function ApiErrorLine({ error }: { error: unknown }) {
  if (!error) return null
  const text = error instanceof ApiError ? `${error.code}: ${error.message}` : error instanceof Error ? error.message : String(error)
  return (
    <p role="alert" className="mb-2 rounded-lg bg-rose-50 px-3 py-2 font-mono text-xs text-rose-700 dark:bg-rose-500/10 dark:text-rose-300">
      {text}
    </p>
  )
}

/**
 * Windows profiles of one device (api-v1 §6B): state badges, consent requests
 * for selected or all profiles (admin) and blacklist clearing (superuser).
 */
export function ProfilesPanel({ deviceId, deviceName, isAdmin, isSuperuser }: { deviceId: number; deviceName: string; isAdmin: boolean; isSuperuser: boolean | undefined }) {
  const { toast, confirm } = useUI()
  const q = useDeviceProfiles(deviceId)
  const request = useRequestConsent()
  const clear = useClearBlacklist()
  const [selected, setSelected] = useState<Set<string>>(new Set())
  const [error, setError] = useState<unknown>(null)

  const toggle = (sid: string) =>
    setSelected((s) => {
      const n = new Set(s)
      if (n.has(sid)) n.delete(sid)
      else n.add(sid)
      return n
    })

  const sendRequest = async (all: boolean) => {
    setError(null)
    try {
      await request.mutateAsync({ deviceId, body: all ? { all: true } : { sids: [...selected] } })
      setSelected(new Set())
      toast('success', 'Consent requested', `Each Windows user must sign in on ${deviceName}, open VaultDock and consent.`)
    } catch (e) {
      setError(e)
    }
  }

  const doClear = async (p: DeviceProfile) => {
    const text = clearConfirmText(p)
    const ok = await confirm({
      title: `Clear blacklist for ${p.username || p.sid}?`,
      message: 'The profile moves to "cleared, awaiting consent". It is not re-enabled by this: the Windows user must sign in and consent again on the device.',
      confirmLabel: 'Clear blacklist',
      requireText: text,
    })
    if (!ok) return
    setError(null)
    try {
      await clear.mutateAsync({ profileId: p.id, confirmText: text })
      toast('success', 'Blacklist cleared', 'Waiting for the Windows user to consent again.')
    } catch (e) {
      setError(e)
    }
  }

  if (q.isPending) return <Spinner />
  if (q.isError) return <ApiErrorLine error={q.error} />
  const profiles = q.data
  const showClear = isSuperuser !== false && isAdmin

  return (
    <div className="mt-3 rounded-xl border border-slate-200 p-3 dark:border-slate-800" aria-label={`Profiles on ${deviceName}`} role="region">
      <ApiErrorLine error={error} />
      {profiles.length === 0 ? (
        <p className="text-sm text-slate-500">No Windows profiles reported yet.</p>
      ) : (
        <table className="w-full text-sm">
          <thead className="text-left text-xs text-slate-500 uppercase">
            <tr>
              {isAdmin && <th className="w-8 py-1.5" />}
              <th className="py-1.5">User</th>
              <th className="py-1.5">State</th>
              <th className="hidden py-1.5 md:table-cell">Details</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {profiles.map((p) => {
              const v = stateView(p.state)
              return (
                <tr key={p.id} className="border-t border-slate-100 dark:border-slate-800">
                  {isAdmin && (
                    <td className="py-1.5">
                      {canRequestConsent(p) && <input type="checkbox" aria-label={`Select ${p.username || p.sid}`} checked={selected.has(p.sid)} onChange={() => toggle(p.sid)} />}
                    </td>
                  )}
                  <td className="py-1.5">
                    <div className="font-medium">{p.username || '(unknown user)'}</div>
                    <div className="font-mono text-[11px] text-slate-400">{p.sid}</div>
                  </td>
                  <td className="py-1.5">
                    <Badge tone={v.tone}>{v.label}</Badge>
                  </td>
                  <td className="hidden py-1.5 text-xs text-slate-500 md:table-cell">
                    {p.state === 'blacklisted' && (
                      <>
                        {blacklistReasonLabel(p.blacklist_reason)} · {formatDate(p.blacklisted_at)}
                      </>
                    )}
                    {p.state === 'cleared_pending_consent' && <>Cleared {formatDate(p.cleared_at)}</>}
                    {(p.state === 'authorised' || p.state === 'active_default') && p.consent_at && <>Consented {formatDate(p.consent_at)}</>}
                    {p.state === 'requested' && p.request_scope && <>Requested ({p.request_scope})</>}
                  </td>
                  <td className="py-1.5 text-right">
                    {p.state === 'blacklisted' && showClear && (
                      <Button size="sm" variant="ghost" className="text-rose-600" icon={<ShieldX className="size-4" />} onClick={() => doClear(p)} loading={clear.isPending && clear.variables?.profileId === p.id}>
                        Clear blacklist
                      </Button>
                    )}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      )}
      {isAdmin && (
        <div className="mt-3 flex flex-wrap gap-2">
          <Button size="sm" icon={<UserCheck className="size-4" />} disabled={selected.size === 0 || request.isPending} onClick={() => sendRequest(false)}>
            Request consent ({selected.size})
          </Button>
          <Button size="sm" icon={<Users className="size-4" />} disabled={request.isPending} onClick={() => sendRequest(true)}>
            Request consent for all profiles
          </Button>
          {isSuperuser === false && profiles.some((p) => p.state === 'blacklisted') && (
            <span className="flex items-center gap-1 text-xs text-slate-500">
              <ShieldCheck className="size-3.5" /> Only a superuser can clear a blacklisted profile.
            </span>
          )}
        </div>
      )}
    </div>
  )
}
