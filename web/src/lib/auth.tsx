import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { ApiError, get, onUnauthenticated, post, setCsrf } from './api'

export type User = {
  id: number
  username: string
  email: string
  display_name: string
  role: 'user' | 'admin'
  disabled: boolean
  quota_bytes: number
  used_bytes: number
  root_id: number
  created_at: number
  last_login_at: number | null
  csrf_token?: string
}

type State = { status: 'loading' } | { status: 'anonymous'; needsSetup: boolean; tokenRequired: boolean; registration: boolean; siteName: string } | { status: 'authenticated'; user: User; siteName: string }

const Ctx = createContext<{
  state: State
  setUser: (u: User) => void
  logout: () => Promise<void>
  refresh: () => Promise<void>
} | null>(null)

export function useAuth() {
  const c = useContext(Ctx)
  if (!c) throw new Error('AuthProvider missing')
  return c
}

export function useUser(): User {
  const { state } = useAuth()
  if (state.status !== 'authenticated') throw new Error('not authenticated')
  return state.user
}

export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<State>({ status: 'loading' })
  const queryClient = useQueryClient()

  const refresh = useCallback(async () => {
    let siteName = 'VaultDock'
    let registration = false
    try {
      const info = await get('/info')
      siteName = info.site_name || 'VaultDock'
      registration = !!info.registration_enabled
    } catch {
      /* server unreachable: handled below */
    }
    try {
      const me = await get<{ user: User }>('/auth/me')
      setCsrf(me.user.csrf_token ?? '')
      setState({ status: 'authenticated', user: me.user, siteName })
    } catch (e) {
      if (e instanceof ApiError && e.status === 401) {
        const setup = await get('/setup/status').catch(() => ({ needs_setup: false, token_required: false }))
        setState({ status: 'anonymous', needsSetup: setup.needs_setup, tokenRequired: setup.token_required, registration, siteName })
      } else {
        setState({ status: 'anonymous', needsSetup: false, tokenRequired: false, registration, siteName })
      }
    }
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  useEffect(
    () =>
      onUnauthenticated(() => {
        // Session gone (expired/revoked): drop every cached response so the next user starts clean.
        queryClient.clear()
        setState((s) => (s.status === 'authenticated' ? { status: 'anonymous', needsSetup: false, tokenRequired: false, registration: false, siteName: s.siteName } : s))
      }),
    [queryClient],
  )

  const setUser = useCallback((u: User) => {
    if (u.csrf_token) setCsrf(u.csrf_token)
    setState((s) => ({ status: 'authenticated', user: { ...(s.status === 'authenticated' ? s.user : {}), ...u }, siteName: s.status !== 'loading' ? s.siteName : 'VaultDock' }))
  }, [])

  const logout = useCallback(async () => {
    try {
      await post('/auth/logout')
    } catch {
      /* ignore */
    }
    setCsrf('')
    await refresh()
    // B5: never let the next user see the previous user's cached data.
    queryClient.clear()
  }, [refresh, queryClient])

  return <Ctx.Provider value={{ state, setUser, logout, refresh }}>{children}</Ctx.Provider>
}
