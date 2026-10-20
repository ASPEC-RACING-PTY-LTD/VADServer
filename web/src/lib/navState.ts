/*
 * Sidebar sections the user opened, remembered in localStorage across visits and normal
 * refreshes. A hard refresh starts over: the server marks the page (<meta name="vd-reload"
 * content="hard">, see internal/api/web.go), and resetOnHardReload then forgets the sections
 * and returns to the dashboard.
 */
const KEY = 'vd-nav-open'

export type NavSection = 'management'

export function readOpenSections(): Partial<Record<NavSection, boolean>> {
  try {
    return JSON.parse(localStorage.getItem(KEY) ?? '{}') ?? {}
  } catch {
    return {}
  }
}

export function writeOpenSections(v: Partial<Record<NavSection, boolean>>) {
  try {
    localStorage.setItem(KEY, JSON.stringify(v))
  } catch {
    // not remembered (storage blocked); the sidebar still works
  }
}

/** Runs once at start-up, before the router reads the URL. */
export function resetOnHardReload() {
  if (document.querySelector('meta[name="vd-reload"][content="hard"]') === null) return
  try {
    localStorage.removeItem(KEY)
  } catch {
    // nothing remembered
  }
  if (window.location.pathname !== '/') window.history.replaceState(null, '', '/')
}
