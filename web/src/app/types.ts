import type { ComponentType } from 'react'
import type { LucideIcon } from 'lucide-react'

/** One route a feature mounts. `component` may be eager or React.lazy(); App wraps routes in Suspense. */
export interface FeatureRoute {
  path: string
  component: ComponentType
  /** Full-screen workspace: rendered without the sidebar and header (e.g. the video editor). */
  fullscreen?: boolean
}

/** Sidebar entry for a feature. */
export interface FeatureNav {
  label: string
  icon: LucideIcon
  /** Link target; defaults to the first route path. */
  to?: string
  /** Lower sorts first within its section. */
  order: number
  /** Exact-match active state (NavLink `end`). */
  end?: boolean
  /** 'main' (default) or the admin-only 'admin' section. */
  section?: 'main' | 'admin'
  /**
   * Optional count shown as a pill next to the label (hidden when undefined or 0). It is a React
   * hook: Layout calls it unconditionally inside a per-entry component, so it may use queries.
   * Keep it cheap and shared (e.g. `summaryBadge('pending_approvals')` from app/summary.ts).
   */
  useBadge?: () => number | undefined
}

export interface FeatureModule {
  /** Unique, stable id (kebab-case). */
  id: string
  routes: FeatureRoute[]
  /** Omit for route-only modules (e.g. Account, reached from the user menu). */
  nav?: FeatureNav
  /** Routes and nav are only registered for admins. */
  adminOnly?: boolean
  /** Module is hidden unless this flag is in the active feature-flag set. */
  requiredFeatureFlag?: string
}

/** What decides module visibility for the current session. */
export interface FeatureContext {
  isAdmin: boolean
  /** Server feature flags from `GET /info` `features` (api-v1 §1.3); empty while loading. */
  flags: ReadonlySet<string>
}
