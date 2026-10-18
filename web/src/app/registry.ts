/*
 * Web feature registry. App.tsx builds its routes and Layout.tsx its sidebar from here.
 *
 * Adding a feature module:
 *   1. Create `src/features/<x>/module.tsx` that default-exports a `FeatureModule` (see ./types.ts).
 *      Wrap page components in `lazy(() => import('./pages/...'))` to keep code splitting.
 *   2. Pick a unique `id`, route paths that don't collide, and a nav `order` (core uses 10, 20, ...).
 *   3. Set `adminOnly` for admin pages (nav `section: 'admin'`) and `requiredFeatureFlag` to gate it.
 * It is picked up automatically by the glob below; no edits to App.tsx, Layout.tsx or this file.
 */
import { get } from '../lib/api'
import { coreModules } from './core'
import type { FeatureContext, FeatureModule, FeatureNav } from './types'

const discovered = import.meta.glob<{ default: FeatureModule }>('../features/*/module.tsx', { eager: true })

export const modules: readonly FeatureModule[] = [...coreModules, ...Object.values(discovered).map((m) => m.default)]

if (import.meta.env.DEV) {
  const ids = new Set<string>()
  const paths = new Set<string>()
  for (const m of modules) {
    if (ids.has(m.id)) console.error(`feature registry: duplicate module id "${m.id}"`)
    ids.add(m.id)
    for (const r of m.routes) {
      if (paths.has(r.path)) console.error(`feature registry: duplicate route "${r.path}" (module "${m.id}")`)
      paths.add(r.path)
    }
  }
}

/** Flag set used until `/info` has answered (and if it fails): flag-gated modules stay hidden. */
export const noFeatureFlags: ReadonlySet<string> = new Set<string>()

/** How often the flag set is refreshed; flags only change on server upgrade/config change. */
export const FEATURE_FLAGS_REFRESH_MS = 5 * 60_000

/** Query key of the flag set. Scoped by user id so a fresh login always refetches. */
export const featureFlagsKey = (userId: number) => ['info', 'features', userId] as const

/** `GET /info` (public) -> its `features` array as a set (api-v1 §1.3). Non-string entries are ignored. */
export async function fetchFeatureFlags(): Promise<ReadonlySet<string>> {
  const info = await get<{ features?: unknown }>('/info')
  const list = Array.isArray(info?.features) ? info.features : []
  return new Set(list.filter((f): f is string => typeof f === 'string'))
}

export function isEnabled(m: FeatureModule, ctx: FeatureContext): boolean {
  if (m.adminOnly && !ctx.isAdmin) return false
  if (m.requiredFeatureFlag && !ctx.flags.has(m.requiredFeatureFlag)) return false
  return true
}

export function enabledModules(ctx: FeatureContext): FeatureModule[] {
  return modules.filter((m) => isEnabled(m, ctx))
}

export type NavEntry = Required<Pick<FeatureNav, 'label' | 'icon' | 'to' | 'order' | 'section'>> & Pick<FeatureNav, 'useBadge'> & { id: string; end: boolean }

/** Nav entries for one sidebar section, sorted by order (ties by id). */
export function navEntries(ctx: FeatureContext, section: 'main' | 'admin'): NavEntry[] {
  return enabledModules(ctx)
    .filter((m) => m.nav && (m.nav.section ?? 'main') === section && (m.nav.to ?? m.routes[0]?.path))
    .map((m) => {
      const n = m.nav!
      return { id: m.id, label: n.label, icon: n.icon, to: n.to ?? m.routes[0].path, order: n.order, end: !!n.end, section, useBadge: n.useBadge }
    })
    .sort((a, b) => a.order - b.order || a.id.localeCompare(b.id))
}
