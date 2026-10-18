import { useEffect, useState, type ReactNode } from 'react'
import { Link, NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Bell, ChevronDown, HardDriveUpload, LogOut, Menu as MenuIcon, Moon, Search, Settings, ShieldAlert, Sun, UserCircle, X } from 'lucide-react'
import { useAuth, useUser } from '../lib/auth'
import { get, post } from '../lib/api'
import { formatBytes, timeAgo } from '../lib/format'
import { useUploads } from '../lib/uploads'
import { Badge, cx, IconButton, Menu, PageLoader, ProgressBar } from './ui'
import { navEntries } from '../app/registry'
import { useFeatureContext, useFeatureFlags } from '../app/useFeatureContext'

function Logo({ name }: { name: string }) {
  return (
    <Link to="/" className="flex items-center gap-2.5 px-2">
      <img src="/logo.png" alt="" className="size-8 object-contain" />
      <span className="text-lg font-semibold tracking-tight text-slate-900 dark:text-white">{name}</span>
    </Link>
  )
}

function useTheme() {
  const [dark, setDark] = useState(() => document.documentElement.classList.contains('dark'))
  const toggle = () => {
    const next = !dark
    setDark(next)
    localStorage.setItem('vd-theme', next ? 'dark' : 'light')
    document.documentElement.classList.toggle('dark', next)
  }
  return { dark, toggle }
}

function Notifications() {
  const [open, setOpen] = useState(false)
  const qc = useQueryClient()
  const { data } = useQuery({ queryKey: ['notifications'], queryFn: () => get('/notifications'), refetchInterval: 30_000 })
  const unread = data?.unread ?? 0
  const markAll = async () => {
    await post('/notifications/read', { id: 0 })
    qc.invalidateQueries({ queryKey: ['notifications'] })
  }
  const tone: Record<string, string> = { error: 'bg-rose-500', warning: 'bg-amber-500', success: 'bg-emerald-500', info: 'bg-sky-500' }
  return (
    <div className="relative">
      <IconButton label="Notifications" onClick={() => setOpen((o) => !o)} className="relative">
        <Bell className="size-[18px]" />
        {unread > 0 && <span className="absolute -top-0.5 -right-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-rose-500 px-1 text-[10px] font-bold text-white">{unread > 9 ? '9+' : unread}</span>}
      </IconButton>
      {open && (
        <>
          <div className="fixed inset-0 z-30" onClick={() => setOpen(false)} />
          <div className="absolute right-0 z-40 mt-2 w-[min(380px,calc(100vw-1.5rem))] rounded-2xl border border-slate-200 bg-white shadow-xl dark:border-slate-700 dark:bg-slate-900">
            <div className="flex items-center justify-between border-b border-slate-200 px-4 py-3 dark:border-slate-800">
              <span className="text-sm font-semibold">Notifications</span>
              {unread > 0 && (
                <button className="text-xs font-medium text-brand-600 hover:underline" onClick={markAll}>
                  Mark all read
                </button>
              )}
            </div>
            <div className="scroll-thin max-h-96 overflow-y-auto">
              {(data?.items ?? []).length === 0 && <div className="px-4 py-10 text-center text-sm text-slate-500">You're all caught up.</div>}
              {(data?.items ?? []).map((n: any) => (
                <div key={n.id} className={cx('flex gap-3 border-b border-slate-100 px-4 py-3 last:border-0 dark:border-slate-800', !n.read_at && 'bg-brand-50/50 dark:bg-brand-500/5')}>
                  <span className={cx('mt-1.5 size-2 shrink-0 rounded-full', tone[n.level] ?? 'bg-slate-400')} />
                  <div className="min-w-0">
                    <div className="text-sm font-medium text-slate-900 dark:text-slate-100">{n.title}</div>
                    {n.message && <div className="mt-0.5 text-xs break-words text-slate-500 dark:text-slate-400">{n.message}</div>}
                    <div className="mt-1 text-[11px] text-slate-400">
                      {timeAgo(n.created_at)}
                      {n.source && ` · ${n.source}`}
                    </div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        </>
      )}
    </div>
  )
}

function UploadIndicator() {
  const { items } = useUploads()
  const active = items.filter((i) => ['queued', 'uploading', 'finalizing'].includes(i.status))
  if (!active.length) return null
  const total = active.reduce((a, i) => a + i.file.size, 0)
  const sent = active.reduce((a, i) => a + i.sent, 0)
  return (
    <Link to="/uploads" className="hidden items-center gap-2 rounded-lg border border-slate-200 px-2.5 py-1.5 text-xs text-slate-600 hover:bg-slate-50 md:flex dark:border-slate-700 dark:text-slate-300 dark:hover:bg-slate-800">
      <HardDriveUpload className="size-4 text-brand-500" />
      <span>{active.length} uploading</span>
      <ProgressBar value={total ? (sent / total) * 100 : 0} className="w-20" />
    </Link>
  )
}

function PendingDevicesBanner() {
  const loc = useLocation()
  const { data } = useQuery({ queryKey: ['devices'], queryFn: () => get('/devices'), refetchInterval: 10_000 })
  const pending = (data?.devices ?? []).filter((d: any) => d.status === 'pending' && d.expires_at > Date.now())
  if (!pending.length || loc.pathname === '/devices') return null
  return (
    <Link to="/devices" className="mb-6 flex items-center gap-3 rounded-2xl border border-amber-300 bg-amber-50 px-4 py-3 text-sm text-amber-900 transition hover:bg-amber-100 dark:border-amber-500/40 dark:bg-amber-500/10 dark:text-amber-100">
      <ShieldAlert className="size-5 shrink-0 text-amber-600" />
      <span className="flex-1">
        <strong>{pending.length === 1 ? `${pending[0].name} is` : `${pending.length} devices are`} waiting for approval.</strong> Review the request before it expires.
      </span>
      <span className="font-medium underline">Review</span>
    </Link>
  )
}

/** Count pill for a nav entry. Mounted per entry (keyed by module id), so `useCount` is a stable hook. */
function NavBadge({ useCount }: { useCount: () => number | undefined }) {
  const n = useCount()
  if (!n || n <= 0) return null
  const text = n > 99 ? '99+' : String(n)
  return (
    <span aria-label={`${n} pending`} className="ml-auto flex h-5 min-w-5 items-center justify-center rounded-full bg-rose-500 px-1.5 text-[11px] font-semibold text-white">
      {text}
    </span>
  )
}

export function Layout({ children }: { children: ReactNode }) {
  const user = useUser()
  const { logout, state } = useAuth()
  const { dark, toggle } = useTheme()
  const [mobileOpen, setMobileOpen] = useState(false)
  const [search, setSearch] = useState('')
  const navigate = useNavigate()
  const loc = useLocation()
  const siteName = state.status === 'authenticated' ? state.siteName : 'VaultDock'
  useEffect(() => setMobileOpen(false), [loc.pathname])
  const { data: stats } = useQuery({ queryKey: ['stats'], queryFn: () => get('/stats'), refetchInterval: 60_000 })
  const used = stats?.usage?.used_bytes ?? user.used_bytes
  const quota = stats?.usage?.quota_bytes ?? user.quota_bytes
  // Sidebar entries come from the feature registry (src/app/registry.ts).
  const featureCtx = useFeatureContext()
  const mainNav = navEntries(featureCtx, 'main')
  const adminNav = navEntries(featureCtx, 'admin')
  // Hold page content until the server flags are known, so a deep link to a flag-gated page
  // isn't redirected by the catch-all route while /info is still loading.
  const { ready: flagsReady } = useFeatureFlags()

  const sidebar = (
    <div className="flex h-full flex-col gap-6 px-3 py-5">
      <Logo name={siteName} />
      <nav className="flex flex-col gap-0.5">
        {mainNav.map((n) => (
          <NavLink key={n.id} to={n.to} end={n.end} className={({ isActive }) => cx('flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition', isActive ? 'bg-brand-50 text-brand-700 dark:bg-brand-500/10 dark:text-brand-300' : 'text-slate-600 hover:bg-slate-100 hover:text-slate-900 dark:text-slate-400 dark:hover:bg-slate-800/70 dark:hover:text-slate-100')}>
            <n.icon className="size-[18px]" />
            {n.label}
            {n.useBadge && <NavBadge useCount={n.useBadge} />}
          </NavLink>
        ))}
        {adminNav.length > 0 && (
          <>
            <div className="mt-4 mb-1 px-3 text-[11px] font-semibold tracking-wider text-slate-400 uppercase">Administration</div>
            {adminNav.map((n) => (
              <NavLink key={n.id} to={n.to} end={n.end} className={({ isActive }) => cx('flex items-center gap-3 rounded-lg px-3 py-2 text-sm font-medium transition', isActive ? 'bg-brand-50 text-brand-700 dark:bg-brand-500/10 dark:text-brand-300' : 'text-slate-600 hover:bg-slate-100 dark:text-slate-400 dark:hover:bg-slate-800/70')}>
                <n.icon className="size-[18px]" />
                {n.label}
                {n.useBadge && <NavBadge useCount={n.useBadge} />}
              </NavLink>
            ))}
          </>
        )}
      </nav>
      <div className="mt-auto rounded-xl border border-slate-200 bg-slate-50 p-3.5 dark:border-slate-800 dark:bg-slate-900/60">
        <div className="mb-2 flex items-center justify-between text-xs">
          <span className="font-medium text-slate-600 dark:text-slate-300">Storage</span>
          <span className="text-slate-500">{quota > 0 ? `${Math.round((used / quota) * 100)}%` : 'No quota'}</span>
        </div>
        <ProgressBar value={quota > 0 ? (used / quota) * 100 : 0} tone={quota > 0 && used / quota > 0.9 ? 'red' : 'brand'} />
        <div className="mt-2 text-xs text-slate-500 dark:text-slate-400">
          {formatBytes(used)} {quota > 0 ? `of ${formatBytes(quota)}` : 'used'}
        </div>
      </div>
    </div>
  )

  return (
    <div className="flex min-h-screen">
      <aside className="sticky top-0 hidden h-screen w-64 shrink-0 border-r border-slate-200 bg-white lg:block dark:border-slate-800 dark:bg-slate-950">{sidebar}</aside>
      {mobileOpen && (
        <div className="fixed inset-0 z-40 lg:hidden">
          <div className="absolute inset-0 bg-slate-950/50" onClick={() => setMobileOpen(false)} />
          <aside className="absolute inset-y-0 left-0 w-72 bg-white shadow-xl dark:bg-slate-950">
            <button className="absolute top-4 right-3 p-1 text-slate-500" onClick={() => setMobileOpen(false)} aria-label="Close menu">
              <X className="size-5" />
            </button>
            {sidebar}
          </aside>
        </div>
      )}
      <div className="flex min-w-0 flex-1 flex-col">
        <header className="sticky top-0 z-20 flex h-16 items-center gap-3 border-b border-slate-200 bg-white/85 px-4 backdrop-blur sm:px-6 dark:border-slate-800 dark:bg-slate-950/85">
          <IconButton label="Open menu" className="lg:hidden" onClick={() => setMobileOpen(true)}>
            <MenuIcon className="size-5" />
          </IconButton>
          <form
            className="relative max-w-md flex-1"
            onSubmit={(e) => {
              e.preventDefault()
              if (search.trim()) navigate(`/files?search=${encodeURIComponent(search.trim())}`)
            }}
          >
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-slate-400" />
            <input className="input pl-9" placeholder="Search all files" value={search} onChange={(e) => setSearch(e.target.value)} />
          </form>
          <div className="ml-auto flex items-center gap-1.5">
            <UploadIndicator />
            <IconButton label={dark ? 'Light theme' : 'Dark theme'} onClick={toggle}>
              {dark ? <Sun className="size-[18px]" /> : <Moon className="size-[18px]" />}
            </IconButton>
            <Notifications />
            <Menu
              trigger={(open) => (
                <button onClick={open} className="flex items-center gap-2 rounded-lg py-1 pr-1.5 pl-1 hover:bg-slate-100 dark:hover:bg-slate-800">
                  <span className="flex size-8 items-center justify-center rounded-full bg-gradient-to-br from-brand-500 to-sky-500 text-sm font-semibold text-white uppercase">{(user.display_name || user.username).slice(0, 1)}</span>
                  <span className="hidden text-left sm:block">
                    <span className="block text-sm leading-tight font-medium text-slate-800 dark:text-slate-100">{user.display_name || user.username}</span>
                    <span className="block text-[11px] leading-tight text-slate-500">{user.role === 'admin' ? 'Administrator' : 'User'}</span>
                  </span>
                  <ChevronDown className="size-4 text-slate-400" />
                </button>
              )}
              items={[
                { label: 'Account settings', icon: <UserCircle className="size-4" />, onClick: () => navigate('/account') },
                ...(user.role === 'admin' ? [{ label: 'Admin console', icon: <Settings className="size-4" />, onClick: () => navigate('/admin') }] : []),
                'divider',
                { label: 'Sign out', icon: <LogOut className="size-4" />, onClick: logout, danger: true },
              ]}
            />
          </div>
        </header>
        <main className="mx-auto w-full max-w-[1400px] flex-1 px-4 py-6 sm:px-6 lg:px-8">
          <PendingDevicesBanner />
          {flagsReady ? children : <PageLoader />}
        </main>
      </div>
    </div>
  )
}

export { Badge }
