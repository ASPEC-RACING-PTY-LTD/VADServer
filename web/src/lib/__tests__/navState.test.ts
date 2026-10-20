import { afterEach, describe, expect, it } from 'vitest'
import { readOpenSections, resetOnHardReload, writeOpenSections } from '../navState'

afterEach(() => {
  document.head.querySelectorAll('meta[name="vd-reload"]').forEach((m) => m.remove())
  localStorage.clear()
  window.history.replaceState(null, '', '/')
})

describe('sidebar sections', () => {
  it('are remembered across normal loads', () => {
    writeOpenSections({ management: true })
    window.history.replaceState(null, '', '/admin/users')
    resetOnHardReload()
    expect(readOpenSections()).toEqual({ management: true })
    expect(window.location.pathname).toBe('/admin/users')
  })

  it('start over on a hard refresh, on the dashboard', () => {
    writeOpenSections({ management: true })
    window.history.replaceState(null, '', '/admin/users')
    const m = document.createElement('meta')
    m.name = 'vd-reload'
    m.content = 'hard'
    document.head.appendChild(m)
    resetOnHardReload()
    expect(readOpenSections()).toEqual({})
    expect(window.location.pathname).toBe('/')
  })
})
