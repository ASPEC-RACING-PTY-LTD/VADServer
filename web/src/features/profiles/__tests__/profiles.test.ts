import { describe, expect, it } from 'vitest'
import { blacklistReasonLabel, canRequestConsent, clearConfirmText, stateView, type DeviceProfile } from '../types'

const p = (o: Partial<DeviceProfile>) => ({ sid: 'S-1', username: 'bob', state: 'requested', ...o }) as DeviceProfile

describe('profile helpers', () => {
  it('labels every state and passes unknown ones through', () => {
    expect(stateView('blacklisted')).toEqual({ label: 'Blacklisted', tone: 'red' })
    expect(stateView('cleared_pending_consent').label).toBe('Cleared, awaiting consent')
    expect(stateView('future_state')).toEqual({ label: 'future_state', tone: 'slate' })
  })
  it('only offers consent requests for non-consented, non-blacklisted profiles', () => {
    for (const s of ['active_default', 'authorised', 'blacklisted']) expect(canRequestConsent(p({ state: s }))).toBe(false)
    for (const s of ['requested', 'revoked', 'cleared_pending_consent']) expect(canRequestConsent(p({ state: s }))).toBe(true)
  })
  it('confirm text is the username, else the SID', () => {
    expect(clearConfirmText(p({}))).toBe('bob')
    expect(clearConfirmText(p({ username: '' }))).toBe('S-1')
  })
  it('labels blacklist reasons', () => {
    expect(blacklistReasonLabel('sid_changed')).toBe('SID changed')
    expect(blacklistReasonLabel(null)).toBe('')
  })
})
