/*
 * Admin console tabs. pages/admin/Admin.tsx renders the tab bar and the nested routes from
 * `enabledAdminTabs(...)`, sorted by `order`. URLs are `/admin` (path '') and `/admin/<path>`.
 *
 * Adding a tab from a feature (no edits to Admin.tsx or this file), either:
 *   - create `src/features/<x>/admin.tsx` that default-exports an `AdminTab` or an array of them, or
 *   - add `export const adminTabs: AdminTab[] = [...]` to `src/features/<x>/module.tsx`.
 * Both are discovered with eager `import.meta.glob`. Use `lazy()` for the tab component; Admin.tsx
 * wraps the tab routes in Suspense.
 *
 * Built-in tabs use orders 10 (Overview), 20 (Users), 30 (Storage), 40 (Devices), 50 (Transfers),
 * 60 (Settings), 70 (System health), 80 (Updates), 90 (Audit log); pick in-between values to slot
 * in (for example 35 for a storage-related tab). Ids and paths must be unique (DEV logs duplicates).
 */
import type { ComponentType } from 'react'
import type { LucideIcon } from 'lucide-react'
import { Activity, ArrowUpCircle, Cpu, HardDrive, Monitor, RefreshCw, Settings, Shield, Users } from 'lucide-react'
import type { FeatureContext } from '../../../app/types'
import { UpdatesTab } from '../Updates'
import AuditTab from './Audit'
import DevicesTab from './Devices'
import HealthTab from './Health'
import OverviewTab from './Overview'
import SettingsTab from './Settings'
import StorageTab from './Storage'
import TransfersTab from './Transfers'
import UsersTab from './Users'

export interface AdminTab {
  /** Unique, stable id (kebab-case). */
  id: string
  /** URL segment under /admin; defaults to `id`. '' is the index tab (/admin). */
  path?: string
  label: string
  icon: LucideIcon
  /** Lower first; built-ins use 10..90. */
  order: number
  /** Rendered with no props; fetches its own data. May be React.lazy(). */
  component: ComponentType
  /** Only for superusers (see `AdminTabContext.isSuperuser`). */
  superuserOnly?: boolean
  /** Hidden unless this flag is in the active feature-flag set. */
  requiredFeatureFlag?: string
}

/** Visibility context for admin tabs. The console itself is only reachable by admins. */
export interface AdminTabContext extends FeatureContext {
  /**
   * There is no separate superuser role yet (roles are 'user' | 'admin'), so every admin is a
   * superuser today and `superuserOnly` only takes effect once a narrower admin role exists.
   */
  isSuperuser: boolean
}

export const coreAdminTabs: readonly AdminTab[] = [
  { id: 'overview', path: '', label: 'Overview', icon: Activity, order: 10, component: OverviewTab },
  { id: 'users', label: 'Users', icon: Users, order: 20, component: UsersTab },
  { id: 'storage', label: 'Storage', icon: HardDrive, order: 30, component: StorageTab },
  { id: 'devices', label: 'Devices', icon: Monitor, order: 40, component: DevicesTab },
  { id: 'transfers', label: 'Transfers', icon: RefreshCw, order: 50, component: TransfersTab },
  { id: 'settings', label: 'Settings', icon: Settings, order: 60, component: SettingsTab },
  { id: 'health', label: 'System health', icon: Cpu, order: 70, component: HealthTab },
  { id: 'updates', label: 'Updates', icon: ArrowUpCircle, order: 80, component: UpdatesTab },
  { id: 'audit', label: 'Audit log', icon: Shield, order: 90, component: AuditTab },
]

type TabExport = AdminTab | AdminTab[] | undefined

const fromAdminFiles = import.meta.glob<TabExport>('../../../features/*/admin.tsx', { eager: true, import: 'default' })
// Namespace import (not `import: 'adminTabs'`): most module.tsx files don't export it, and a
// named-import glob would fail the build for them.
const moduleNamespaces = import.meta.glob<{ adminTabs?: TabExport }>('../../../features/*/module.tsx', { eager: true })

function flatten(exports: TabExport[]): AdminTab[] {
  return exports.flatMap((t) => (t ? (Array.isArray(t) ? t : [t]) : []))
}

/** Tabs contributed by feature folders. */
export const featureAdminTabs: readonly AdminTab[] = [...flatten(Object.values(fromAdminFiles)), ...flatten(Object.values(moduleNamespaces).map((m) => m.adminTabs))]

/** Every registered tab: built-ins plus feature tabs. */
export const adminTabs: readonly AdminTab[] = [...coreAdminTabs, ...featureAdminTabs]

export function tabPath(t: AdminTab): string {
  return t.path ?? t.id
}

export function isAdminTabEnabled(t: AdminTab, ctx: AdminTabContext): boolean {
  if (t.superuserOnly && !ctx.isSuperuser) return false
  if (t.requiredFeatureFlag && !ctx.flags.has(t.requiredFeatureFlag)) return false
  return true
}

/** Enabled tabs from `all`, sorted by order (ties by id). */
export function enabledAdminTabs(all: readonly AdminTab[], ctx: AdminTabContext): AdminTab[] {
  if (import.meta.env.DEV) {
    const ids = new Set<string>()
    const paths = new Set<string>()
    for (const t of all) {
      if (ids.has(t.id)) console.error(`admin tabs: duplicate tab id "${t.id}"`)
      if (paths.has(tabPath(t))) console.error(`admin tabs: duplicate tab path "${tabPath(t)}"`)
      ids.add(t.id)
      paths.add(tabPath(t))
    }
  }
  return all.filter((t) => isAdminTabEnabled(t, ctx)).sort((a, b) => a.order - b.order || a.id.localeCompare(b.id))
}
