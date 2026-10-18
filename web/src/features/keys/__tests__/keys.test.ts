import { afterEach, describe, expect, it, vi } from 'vitest'
import { fetchServerKeyStatus } from '../api'
import { normalizeFingerprint, sameFingerprint } from '../types'

function reply(status: number, body: unknown) {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })))
}
afterEach(() => vi.unstubAllGlobals())

describe('server keys', () => {
  it('compares fingerprints case-insensitively ignoring separators', () => {
    expect(normalizeFingerprint('abcd-0123 45:67')).toBe('ABCD01234567')
    expect(sameFingerprint('ABCD-0123', 'abcd:0123')).toBe(true)
    expect(sameFingerprint('ABCD-0123', 'ABCD-0124')).toBe(false)
  })
  it('maps 409 escrow_unavailable to the unavailable state', async () => {
    reply(409, { error: { code: 'escrow_unavailable', message: 'no kek' } })
    expect(await fetchServerKeyStatus()).toEqual({ state: 'unavailable', message: 'no kek' })
  })
  it('returns the identity', async () => {
    const id = { sik_pub: 'a', sik_fingerprint: 'F', sek_pub: 'b', sek_version: 2, sek_sig: 'c' }
    reply(200, { server_identity: id })
    expect(await fetchServerKeyStatus()).toEqual({ state: 'available', identity: id })
  })
  it('rethrows other errors', async () => {
    reply(409, { error: { code: 'sik_mismatch', message: 'x' } })
    await expect(fetchServerKeyStatus()).rejects.toMatchObject({ code: 'sik_mismatch' })
  })
})
