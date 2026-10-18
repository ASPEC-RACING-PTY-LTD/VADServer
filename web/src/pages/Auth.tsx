import { useState, type FormEvent, type ReactNode } from 'react'
import { KeyRound, ShieldCheck } from 'lucide-react'
import { post, errorMessage } from '../lib/api'
import { useAuth } from '../lib/auth'
import { Button, Field } from '../components/ui'

function Shell({ title, subtitle, children }: { title: string; subtitle: ReactNode; children: ReactNode }) {
  return (
    <div className="relative flex min-h-screen items-center justify-center overflow-hidden px-4 py-12">
      <div className="pointer-events-none absolute inset-0 bg-[radial-gradient(ellipse_at_top,rgba(99,102,241,0.18),transparent_60%),radial-gradient(ellipse_at_bottom_right,rgba(14,165,233,0.14),transparent_55%)]" />
      <div className="relative w-full max-w-md">
        <div className="mb-8 flex flex-col items-center text-center">
          <img src="/logo.png" alt="" className="mb-4 size-20 object-contain drop-shadow-lg" />
          <h1 className="text-2xl font-semibold tracking-tight text-slate-900 dark:text-white">{title}</h1>
          <p className="mt-1.5 text-sm text-slate-500 dark:text-slate-400">{subtitle}</p>
        </div>
        <div className="card p-6 sm:p-8">{children}</div>
        <p className="mt-6 text-center text-xs text-slate-400">VaultDock · self-hosted storage and sync</p>
      </div>
    </div>
  )
}

export function LoginPage({ registration, siteName }: { registration: boolean; siteName: string }) {
  const { setUser } = useAuth()
  const [mode, setMode] = useState<'login' | 'register'>('login')
  const [login, setLogin] = useState('')
  const [password, setPassword] = useState('')
  const [email, setEmail] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      const res = mode === 'login' ? await post('/auth/login', { login, password }) : await post('/auth/register', { username: login, email, password })
      setUser(res.user)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell title={mode === 'login' ? `Sign in to ${siteName}` : 'Create your account'} subtitle={mode === 'login' ? 'Your private storage, on your own hardware.' : 'Registration is open on this server.'}>
      <form onSubmit={submit} className="space-y-4">
        <Field label={mode === 'login' ? 'Username or email' : 'Username'}>
          <input className="input" autoComplete="username" autoFocus required value={login} onChange={(e) => setLogin(e.target.value)} />
        </Field>
        {mode === 'register' && (
          <Field label="Email (optional)">
            <input className="input" type="email" autoComplete="email" value={email} onChange={(e) => setEmail(e.target.value)} />
          </Field>
        )}
        <Field label="Password" hint={mode === 'register' ? 'At least 10 characters.' : undefined}>
          <input className="input" type="password" autoComplete={mode === 'login' ? 'current-password' : 'new-password'} required value={password} onChange={(e) => setPassword(e.target.value)} />
        </Field>
        {error && <div className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-500/10 dark:text-rose-300">{error}</div>}
        <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} icon={<KeyRound className="size-4" />}>
          {mode === 'login' ? 'Sign in' : 'Create account'}
        </Button>
      </form>
      {registration && (
        <div className="mt-5 text-center text-sm text-slate-500">
          {mode === 'login' ? (
            <>
              No account?{' '}
              <button className="font-medium text-brand-600 hover:underline" onClick={() => setMode('register')}>
                Create one
              </button>
            </>
          ) : (
            <button className="font-medium text-brand-600 hover:underline" onClick={() => setMode('login')}>
              Back to sign in
            </button>
          )}
        </div>
      )}
    </Shell>
  )
}

export function SetupPage({ tokenRequired }: { tokenRequired: boolean }) {
  const { setUser } = useAuth()
  const [form, setForm] = useState({ username: '', email: '', password: '', confirm: '', setup_token: '' })
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const set = (k: keyof typeof form) => (e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, [k]: e.target.value })

  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (form.password !== form.confirm) {
      setError('Passwords do not match')
      return
    }
    setBusy(true)
    setError('')
    try {
      const res = await post('/setup', { username: form.username, email: form.email, password: form.password, setup_token: form.setup_token })
      setUser(res.user)
    } catch (err) {
      setError(errorMessage(err))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Shell title="Welcome to VaultDock" subtitle="Create the administrator account to finish setting up this server.">
      <form onSubmit={submit} className="space-y-4">
        {tokenRequired && (
          <Field label="Setup token" hint="Printed by the installer and stored in .env as VAULTDOCK_SETUP_TOKEN.">
            <input className="input font-mono" required value={form.setup_token} onChange={set('setup_token')} autoFocus />
          </Field>
        )}
        <Field label="Administrator username">
          <input className="input" required autoComplete="username" value={form.username} onChange={set('username')} autoFocus={!tokenRequired} />
        </Field>
        <Field label="Email (optional)">
          <input className="input" type="email" value={form.email} onChange={set('email')} />
        </Field>
        <div className="grid gap-4 sm:grid-cols-2">
          <Field label="Password">
            <input className="input" type="password" required autoComplete="new-password" value={form.password} onChange={set('password')} />
          </Field>
          <Field label="Confirm">
            <input className="input" type="password" required autoComplete="new-password" value={form.confirm} onChange={set('confirm')} />
          </Field>
        </div>
        <p className="text-xs text-slate-500">Use at least 10 characters. A passphrase of several words works well.</p>
        {error && <div className="rounded-lg bg-rose-50 px-3 py-2 text-sm text-rose-700 dark:bg-rose-500/10 dark:text-rose-300">{error}</div>}
        <Button type="submit" variant="primary" size="lg" className="w-full" loading={busy} icon={<ShieldCheck className="size-4" />}>
          Create administrator
        </Button>
      </form>
    </Shell>
  )
}
