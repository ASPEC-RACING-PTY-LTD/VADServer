import { describe, expect, it } from 'vitest'
import { Activity } from 'lucide-react'
import { adminTabs, coreAdminTabs, enabledAdminTabs, tabPath, type AdminTab, type AdminTabContext } from './registry'

const Dummy = () => null
const ctx = (over: Partial<AdminTabContext> = {}): AdminTabContext => ({ isAdmin: true, isSuperuser: true, flags: new Set(), ...over })
const tab = (over: Partial<AdminTab>): AdminTab => ({ id: 'x', label: 'X', icon: Activity, order: 100, component: Dummy, ...over })

describe('admin tabs registry', () => {
  it('keeps the built-in tab order and URLs', () => {
    expect(enabledAdminTabs(coreAdminTabs, ctx()).map(tabPath)).toEqual(['', 'users', 'storage', 'devices', 'transfers', 'settings', 'updates', 'audit'])
  })

  it('has unique ids and paths', () => {
    expect(new Set(adminTabs.map((t) => t.id)).size).toBe(adminTabs.length)
    expect(new Set(adminTabs.map(tabPath)).size).toBe(adminTabs.length)
  })

  it('sorts by order and filters superuser and flag tabs', () => {
    const all = [tab({ id: 'b', order: 35 }), tab({ id: 's', order: 5, superuserOnly: true }), tab({ id: 'f', order: 1, requiredFeatureFlag: 'gc' })]
    expect(enabledAdminTabs(all, ctx()).map((t) => t.id)).toEqual(['s', 'b'])
    expect(enabledAdminTabs(all, ctx({ isSuperuser: false, flags: new Set(['gc']) })).map((t) => t.id)).toEqual(['f', 'b'])
  })
})
