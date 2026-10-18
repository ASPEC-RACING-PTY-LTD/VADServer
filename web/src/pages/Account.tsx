import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { KeyRound, LogOut, Monitor, UserCircle } from 'lucide-react'
import { del, errorMessage, get, post, put } from '../lib/api'
import { useAuth, useUser } from '../lib/auth'
import { formatBytes, formatDate, timeAgo } from '../lib/format'
import { Badge, Button, Field, PageHeader, PageLoader, useUI } from '../components/ui'

function uaSummary(ua: string) {
  const browser = /Edg\//.test(ua) ? 'Edge' : /Chrome\//.test(ua) ? 'Chrome' : /Firefox\//.test(ua) ? 'Firefox' : /Safari\//.test(ua) ? 'Safari' : 'Browser'
  const os = /Windows/.test(ua) ? 'Windows' : /Mac OS X/.test(ua) ? 'macOS' : /Android/.test(ua) ? 'Android' : /iPhone|iPad/.test(ua) ? 'iOS' : /Linux/.test(ua) ? 'Linux' : ''
  return os ? `${browser} on ${os}` : browser
}

export default function AccountPage() {
  const user = useUser()
  const { setUser } = useAuth()
  const { toast, confirm } = useUI()
  const qc = useQueryClient()
  const [profile, setProfile] = useState({ email: user.email, display_name: user.display_name })
  const [pw, setPw] = useState({ current_password: '', new_password: '', confirm: '' })
  const [busy, setBusy] = useState('')
  const sessions = useQuery({ queryKey: ['sessions'], queryFn: () => get('/account/sessions') })

  const saveProfile = async () => {
    setBusy('profile')
    try {
      const res = await put('/account/profile', profile)
      setUser(res.user)
      toast('success', 'Profile saved')
    } catch (e) {
      toast('error', 'Could not save profile', errorMessage(e))
    } finally {
      setBusy('')
    }
  }
  const changePassword = async () => {
    if (pw.new_password !== pw.confirm) {
      toast('error', 'Passwords do not match')
      return
    }
    setBusy('pw')
    try {
      const res = await put('/account/password', { current_password: pw.current_password, new_password: pw.new_password })
      toast('success', 'Password changed', `${res.sessions_revoked} other session(s) were signed out.`)
      setPw({ current_password: '', new_password: '', confirm: '' })
      qc.invalidateQueries({ queryKey: ['sessions'] })
    } catch (e) {
      toast('error', 'Could not change password', errorMessage(e))
    } finally {
      setBusy('')
    }
  }
  const revoke = async (id: string) => {
    await del(`/account/sessions/${id}`)
    qc.invalidateQueries({ queryKey: ['sessions'] })
  }
  const revokeOthers = async () => {
    if (!(await confirm({ title: 'Sign out other sessions?', message: 'All other browsers signed in to your account will be signed out. Desktop devices are not affected; manage them on the Devices page.', confirmLabel: 'Sign out others' }))) return
    const res = await post('/account/sessions/revoke-others')
    toast('success', `Signed out ${res.revoked} session(s)`)
    qc.invalidateQueries({ queryKey: ['sessions'] })
  }

  return (
    <div className="max-w-4xl">
      <PageHeader title="Account settings" description={`Signed in as ${user.username}${user.role === 'admin' ? ' (administrator)' : ''}.`} icon={<UserCircle className="size-5" />} />
      <div className="grid gap-6">
        <section className="card p-6">
          <h2 className="mb-1 font-semibold">Profile</h2>
          <p className="mb-5 text-sm text-slate-500">Storage used: {formatBytes(user.used_bytes)}{user.quota_bytes > 0 ? ` of ${formatBytes(user.quota_bytes)}` : ''}. Member since {formatDate(user.created_at)}.</p>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Display name">
              <input className="input" value={profile.display_name} onChange={(e) => setProfile({ ...profile, display_name: e.target.value })} />
            </Field>
            <Field label="Email">
              <input className="input" type="email" value={profile.email} onChange={(e) => setProfile({ ...profile, email: e.target.value })} />
            </Field>
          </div>
          <div className="mt-5 flex justify-end">
            <Button variant="primary" loading={busy === 'profile'} onClick={saveProfile}>
              Save profile
            </Button>
          </div>
        </section>

        <section className="card p-6">
          <h2 className="mb-1 flex items-center gap-2 font-semibold">
            <KeyRound className="size-4" /> Password
          </h2>
          <p className="mb-5 text-sm text-slate-500">Changing your password signs out your other browser sessions. Approved devices keep working because they use their own revocable keys.</p>
          <div className="grid gap-4 sm:grid-cols-3">
            <Field label="Current password">
              <input className="input" type="password" autoComplete="current-password" value={pw.current_password} onChange={(e) => setPw({ ...pw, current_password: e.target.value })} />
            </Field>
            <Field label="New password" hint="At least 10 characters">
              <input className="input" type="password" autoComplete="new-password" value={pw.new_password} onChange={(e) => setPw({ ...pw, new_password: e.target.value })} />
            </Field>
            <Field label="Confirm new password">
              <input className="input" type="password" autoComplete="new-password" value={pw.confirm} onChange={(e) => setPw({ ...pw, confirm: e.target.value })} />
            </Field>
          </div>
          <div className="mt-5 flex justify-end">
            <Button variant="primary" loading={busy === 'pw'} disabled={!pw.current_password || !pw.new_password} onClick={changePassword}>
              Change password
            </Button>
          </div>
        </section>

        <section className="card p-6">
          <div className="mb-4 flex items-center justify-between gap-3">
            <div>
              <h2 className="flex items-center gap-2 font-semibold">
                <Monitor className="size-4" /> Browser sessions
              </h2>
              <p className="text-sm text-slate-500">Where you're signed in to the web interface.</p>
            </div>
            <Button size="sm" icon={<LogOut className="size-3.5" />} onClick={revokeOthers}>
              Sign out others
            </Button>
          </div>
          {sessions.isLoading ? (
            <PageLoader />
          ) : (
            <div className="divide-y divide-slate-100 dark:divide-slate-800">
              {(sessions.data?.sessions ?? []).map((s: any) => (
                <div key={s.id} className="flex items-center gap-3 py-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-sm font-medium">
                      {uaSummary(s.user_agent)} {s.current && <Badge tone="green">This session</Badge>}
                    </div>
                    <div className="text-xs text-slate-500">
                      {s.ip} · signed in {formatDate(s.created_at)} · active {timeAgo(s.last_seen_at)}
                    </div>
                  </div>
                  {!s.current && (
                    <Button size="sm" variant="ghost" onClick={() => revoke(s.id)}>
                      Sign out
                    </Button>
                  )}
                </div>
              ))}
            </div>
          )}
        </section>
      </div>
    </div>
  )
}
