import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Check, ChevronDown, ChevronRight, Clock, Fingerprint, Globe, Laptop, MapPin, Monitor, Pencil, ShieldAlert, ShieldOff, Trash2, Users, X } from 'lucide-react'
import { del, errorMessage, get, patch, post } from '../lib/api'
import { formatDate, timeAgo } from '../lib/format'
import { Badge, Button, EmptyState, ErrorState, Field, IconButton, Modal, PageHeader, PageLoader, useUI } from '../components/ui'
import { useUser } from '../lib/auth'
import { useFeatureFlags } from '../app/useFeatureContext'
import { ModeBadges } from '../features/devices/ModeBadges'
import type { DeviceModeFields } from '../features/devices/types'
import { ProfilesPanel } from '../features/profiles/ProfilesPanel'
import { useIsSuperuser } from '../features/profiles/api'
import { ServerKeyCard } from '../features/keys/ServerKeyCard'

export type Device = DeviceModeFields & {
  id: number
  name: string
  os: string
  client_version: string
  identifier: string
  key_fingerprint: string
  status: 'pending' | 'approved' | 'rejected' | 'revoked' | 'expired'
  user_code: string
  request_ip: string
  request_location: string
  created_at: number
  expires_at: number
  decided_at: number | null
  last_seen_at: number | null
  last_ip: string
  username?: string
  sync_state: any
}

const statusTone = { pending: 'amber', approved: 'green', rejected: 'red', revoked: 'red', expired: 'slate' } as const

function Countdown({ until }: { until: number }) {
  const [now, setNow] = useState(Date.now())
  useEffect(() => {
    const t = setInterval(() => setNow(Date.now()), 1000)
    return () => clearInterval(t)
  }, [])
  const s = Math.max(0, Math.floor((until - now) / 1000))
  return (
    <span className="tabular-nums">
      {Math.floor(s / 60)}:{String(s % 60).padStart(2, '0')}
    </span>
  )
}

function PendingCard({ d, onDecide }: { d: Device; onDecide: (d: Device, action: 'approve' | 'reject') => void }) {
  return (
    <div className="card overflow-hidden border-amber-300 dark:border-amber-500/40">
      <div className="flex items-center gap-2 bg-amber-50 px-5 py-2.5 text-sm font-medium text-amber-900 dark:bg-amber-500/10 dark:text-amber-200">
        <ShieldAlert className="size-4" /> New device requesting access
        <span className="ml-auto flex items-center gap-1 text-xs font-normal">
          <Clock className="size-3.5" /> Expires in <Countdown until={d.expires_at} />
        </span>
      </div>
      <div className="grid gap-5 p-5 md:grid-cols-[1fr_auto]">
        <div className="grid gap-3 sm:grid-cols-2">
          <Info icon={<Laptop className="size-4" />} label="Device name" value={d.name} />
          <Info icon={<Monitor className="size-4" />} label="Operating system" value={d.os || 'Unknown'} />
          <Info icon={<Fingerprint className="size-4" />} label="Device identifier" value={<span className="font-mono text-xs break-all">{d.identifier}</span>} />
          <Info icon={<Fingerprint className="size-4" />} label="Key fingerprint" value={<span className="font-mono text-xs">{d.key_fingerprint}</span>} />
          <Info icon={<Globe className="size-4" />} label="IP address" value={<span className="font-mono text-xs">{d.request_ip}</span>} />
          <Info icon={<MapPin className="size-4" />} label="Approximate location" value={d.request_location} />
          <Info icon={<Clock className="size-4" />} label="Requested" value={`${formatDate(d.created_at)} (${timeAgo(d.created_at)})`} />
          <Info icon={<ShieldAlert className="size-4" />} label="Verification code" value={<span className="font-mono text-base font-semibold tracking-widest text-brand-700 dark:text-brand-300">{d.user_code}</span>} />
        </div>
        <div className="flex flex-col justify-between gap-4 md:w-64">
          <p className="text-xs text-slate-500 dark:text-slate-400">Only approve if you just signed in from this device and the verification code matches the one shown in the VaultDock desktop app.</p>
          <div className="flex gap-2">
            <Button variant="danger" className="flex-1" icon={<X className="size-4" />} onClick={() => onDecide(d, 'reject')}>
              Reject
            </Button>
            <Button variant="success" className="flex-1" icon={<Check className="size-4" />} onClick={() => onDecide(d, 'approve')}>
              Approve
            </Button>
          </div>
        </div>
      </div>
    </div>
  )
}

function Info({ icon, label, value }: { icon: React.ReactNode; label: string; value: React.ReactNode }) {
  return (
    <div className="flex gap-2.5">
      <span className="mt-0.5 text-slate-400">{icon}</span>
      <div className="min-w-0">
        <div className="text-[11px] font-semibold tracking-wide text-slate-500 uppercase">{label}</div>
        <div className="text-sm text-slate-800 dark:text-slate-100">{value}</div>
      </div>
    </div>
  )
}

export default function DevicesPage() {
  const qc = useQueryClient()
  const { toast, confirm } = useUI()
  const { data, isLoading, error, refetch } = useQuery({ queryKey: ['devices'], queryFn: () => get('/devices'), refetchInterval: 5000 })
  const [renaming, setRenaming] = useState<Device | null>(null)
  const [name, setName] = useState('')
  const [openProfiles, setOpenProfiles] = useState<Set<number>>(new Set())
  const user = useUser()
  const isAdmin = user.role === 'admin'
  const isSuperuser = useIsSuperuser(user)
  const { flags } = useFeatureFlags()
  const profilesOn = flags.has('profiles.v1')
  const toggleProfiles = (id: number) =>
    setOpenProfiles((s) => {
      const n = new Set(s)
      if (n.has(id)) n.delete(id)
      else n.add(id)
      return n
    })
  if (isLoading) return <PageLoader />
  if (error) return <ErrorState error={error} onRetry={refetch} />
  const devices: Device[] = data.devices
  const now = Date.now()
  const pending = devices.filter((d) => d.status === 'pending' && d.expires_at > now)
  const approved = devices.filter((d) => d.status === 'approved')
  const history = devices.filter((d) => d.status !== 'approved' && !(d.status === 'pending' && d.expires_at > now))

  const refresh = () => qc.invalidateQueries({ queryKey: ['devices'] })
  const decide = async (d: Device, action: 'approve' | 'reject') => {
    if (action === 'approve') {
      const ok = await confirm({ title: `Approve ${d.name}?`, message: <>This device will be able to upload files to your storage until you revoke it. Verification code: <strong className="font-mono">{d.user_code}</strong></>, confirmLabel: 'Approve device', tone: 'primary' })
      if (!ok) return
    }
    try {
      await post(`/devices/${d.id}/${action}`)
      toast('success', action === 'approve' ? `${d.name} approved` : `${d.name} rejected`, action === 'approve' ? 'The desktop app continues automatically.' : undefined)
      refresh()
    } catch (e) {
      toast('error', 'Action failed', errorMessage(e))
      refresh()
    }
  }
  const revoke = async (d: Device) => {
    const ok = await confirm({ title: `Revoke ${d.name}?`, message: 'The device immediately loses access. Its in-progress uploads are cancelled. Files it already uploaded stay in your storage. To use it again, sign in from the desktop app and approve it again.', confirmLabel: 'Revoke access' })
    if (!ok) return
    try {
      await post(`/devices/${d.id}/revoke`)
      toast('success', `${d.name} revoked`)
      refresh()
    } catch (e) {
      toast('error', 'Could not revoke', errorMessage(e))
    }
  }
  const remove = async (d: Device) => {
    try {
      await del(`/devices/${d.id}`)
      refresh()
    } catch (e) {
      toast('error', 'Could not remove', errorMessage(e))
    }
  }
  const saveName = async () => {
    if (!renaming) return
    try {
      await patch(`/devices/${renaming.id}`, { name })
      setRenaming(null)
      refresh()
    } catch (e) {
      toast('error', 'Rename failed', errorMessage(e))
    }
  }

  return (
    <div>
      <PageHeader title="Devices" description="Computers connected to your VaultDock storage. New devices need your approval before they can sync." icon={<Monitor className="size-5" />} />
      <ServerKeyCard />
      {pending.length > 0 && <div className="mb-8 space-y-4">{pending.map((d) => <PendingCard key={d.id} d={d} onDecide={decide} />)}</div>}

      <h2 className="mb-3 text-sm font-semibold tracking-wide text-slate-500 uppercase">Authorised devices</h2>
      <div className="card mb-8 overflow-hidden">
        {approved.length === 0 ? (
          <EmptyState icon={<Laptop className="size-6" />} title="No devices yet" description="Install the VaultDock desktop app on Windows, enter this server's address and your credentials, then approve the request here." />
        ) : (
          approved.map((d) => {
            const online = d.last_seen_at && now - d.last_seen_at < 3 * 60 * 1000
            return (
              <div key={d.id} className="flex flex-col gap-3 border-b border-slate-100 px-5 py-4 last:border-0 sm:flex-row sm:items-center dark:border-slate-800">
                <div className="flex size-10 shrink-0 items-center justify-center rounded-xl bg-slate-100 text-slate-600 dark:bg-slate-800 dark:text-slate-300">
                  <Laptop className="size-5" />
                </div>
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2">
                    <span className="font-medium">{d.name}</span>
                    <Badge tone={online ? 'green' : 'slate'}>{online ? 'Online' : 'Offline'}</Badge>
                  </div>
                  <div className="mt-0.5 text-xs text-slate-500">
                    {d.os} · app {d.client_version || 'unknown'} · approved {timeAgo(d.decided_at)} · last seen {timeAgo(d.last_seen_at)}
                    {d.last_ip && ` from ${d.last_ip}`}
                  </div>
                  <div className="mt-0.5 font-mono text-[11px] text-slate-400">key {d.key_fingerprint}</div>
                  <ModeBadges device={d} />
                  {profilesOn && openProfiles.has(d.id) && <ProfilesPanel deviceId={d.id} deviceName={d.name} isAdmin={isAdmin} isSuperuser={isSuperuser} />}
                </div>
                <div className="flex gap-2 self-start">
                  {profilesOn && (
                    <Button size="sm" variant="ghost" icon={openProfiles.has(d.id) ? <ChevronDown className="size-4" /> : <ChevronRight className="size-4" />} aria-expanded={openProfiles.has(d.id)} onClick={() => toggleProfiles(d.id)}>
                      <Users className="size-4" /> Profiles
                    </Button>
                  )}
                  <IconButton
                    label="Rename"
                    onClick={() => {
                      setRenaming(d)
                      setName(d.name)
                    }}
                  >
                    <Pencil className="size-4" />
                  </IconButton>
                  <Button size="sm" variant="ghost" className="text-rose-600" icon={<ShieldOff className="size-4" />} onClick={() => revoke(d)}>
                    Revoke
                  </Button>
                </div>
              </div>
            )
          })
        )}
      </div>

      {history.length > 0 && (
        <>
          <h2 className="mb-3 text-sm font-semibold tracking-wide text-slate-500 uppercase">Request history</h2>
          <div className="card overflow-x-auto">
            <table className="w-full text-sm">
              <thead className="border-b border-slate-200 text-left text-xs text-slate-500 uppercase dark:border-slate-800">
                <tr>
                  <th className="px-4 py-2.5">Device</th>
                  <th className="px-4 py-2.5">Status</th>
                  <th className="hidden px-4 py-2.5 md:table-cell">IP / location</th>
                  <th className="px-4 py-2.5">Requested</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {history.map((d) => (
                  <tr key={d.id} className="border-b border-slate-100 last:border-0 dark:border-slate-800">
                    <td className="px-4 py-2.5">
                      <div className="font-medium">{d.name}</div>
                      <div className="text-xs text-slate-500">{d.os}</div>
                    </td>
                    <td className="px-4 py-2.5">
                      <Badge tone={statusTone[d.status]}>{d.status === 'pending' ? 'expired' : d.status}</Badge>
                    </td>
                    <td className="hidden px-4 py-2.5 text-xs text-slate-500 md:table-cell">
                      {d.request_ip} · {d.request_location}
                    </td>
                    <td className="px-4 py-2.5 text-xs text-slate-500">{formatDate(d.created_at)}</td>
                    <td className="px-4 py-2.5 text-right">
                      <IconButton label="Remove from history" onClick={() => remove(d)}>
                        <Trash2 className="size-4" />
                      </IconButton>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      <Modal
        open={!!renaming}
        onClose={() => setRenaming(null)}
        title="Rename device"
        size="sm"
        footer={
          <>
            <Button onClick={() => setRenaming(null)}>Cancel</Button>
            <Button variant="primary" onClick={saveName} disabled={!name.trim()}>
              Save
            </Button>
          </>
        }
      >
        <Field label="Name">
          <input className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
        </Field>
      </Modal>
    </div>
  )
}
