/*
 * Dashboard widget slots. pages/Dashboard.tsx renders every enabled widget, sorted by `order`,
 * into a single responsive grid (1 column, 3 columns at xl).
 *
 * Adding a widget from a feature (no edits to Dashboard.tsx or this file):
 *   - create `src/features/<x>/dashboard.tsx` that default-exports a `DashboardWidget`
 *     or an array of them, or
 *   - add `export const dashboardWidgets: DashboardWidget[] = [...]` to `src/features/<x>/module.tsx`.
 * Use `lazy()` for heavy widget components. Each slot is wrapped in Suspense and an error
 * boundary, so a failing widget never takes the dashboard down.
 *
 * Built-in widgets (Dashboard.tsx) use orders 10 (stats), 20 (storage), 30 (sync), 40 (recent),
 * 50 (automation CTA); pick in-between values to slot in. Ids must be unique (DEV logs duplicates).
 */
import type { ComponentType } from 'react'
import type { FeatureContext } from '../app/types'

export interface DashboardWidget {
  id: string
  /** Lower first; built-ins use 10..50. */
  order: number
  /** Rendered with no props; fetches its own data (React Query dedupes shared keys such as ['stats']). */
  component: ComponentType
  /** Grid columns at xl (the grid has 3). Default 1; 3 = full width. */
  span?: 1 | 2 | 3
  adminOnly?: boolean
  requiredFeatureFlag?: string
}

type WidgetExport = DashboardWidget | DashboardWidget[] | undefined

const fromDashboardFiles = import.meta.glob<WidgetExport>('../features/*/dashboard.tsx', { eager: true, import: 'default' })
// Namespace import (not `import: 'dashboardWidgets'`): most module.tsx files don't export it, and a
// named-import glob would fail the build for them.
const moduleNamespaces = import.meta.glob<{ dashboardWidgets?: WidgetExport }>('../features/*/module.tsx', { eager: true })
const fromModules = Object.fromEntries(Object.entries(moduleNamespaces).map(([k, m]) => [k, m.dashboardWidgets]))

function flatten(exports: Record<string, WidgetExport>): DashboardWidget[] {
  return Object.values(exports).flatMap((w) => (w ? (Array.isArray(w) ? w : [w]) : []))
}

/** Widgets contributed by feature folders (built-ins are added by Dashboard.tsx). */
export const featureWidgets: readonly DashboardWidget[] = [...flatten(fromDashboardFiles), ...flatten(fromModules)]

export function isWidgetEnabled(w: DashboardWidget, ctx: FeatureContext): boolean {
  if (w.adminOnly && !ctx.isAdmin) return false
  if (w.requiredFeatureFlag && !ctx.flags.has(w.requiredFeatureFlag)) return false
  return true
}

/** Enabled widgets from `all`, sorted by order (ties by id). */
export function enabledWidgets(all: readonly DashboardWidget[], ctx: FeatureContext): DashboardWidget[] {
  if (import.meta.env.DEV) {
    const ids = new Set<string>()
    for (const w of all) {
      if (ids.has(w.id)) console.error(`dashboard widgets: duplicate widget id "${w.id}"`)
      ids.add(w.id)
    }
  }
  return all.filter((w) => isWidgetEnabled(w, ctx)).sort((a, b) => a.order - b.order || a.id.localeCompare(b.id))
}
