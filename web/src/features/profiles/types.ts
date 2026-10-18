// Hand-written mirror of wire.Profile (sdk/wire/profiles.go, api-v1 §6B).

export type ProfileState = 'active_default' | 'requested' | 'authorised' | 'blacklisted' | 'cleared_pending_consent' | 'revoked'

export type BlacklistReason = 'password_changed' | 'password_reset' | 'sid_changed' | 'account_disabled' | 'account_removed' | 'profile_moved' | 'consent_unverifiable'

export interface DeviceProfile {
  id: number
  device_id: number
  sid: string
  username: string
  profile_path: string
  vaultdock_user_id: number | null
  state: ProfileState | (string & {})
  consent_id: string | null
  consent_at: number | null
  requested_by_user_id: number | null
  request_scope: 'selected' | 'all' | null
  blacklisted_at: number | null
  blacklist_reason: BlacklistReason | (string & {}) | null
  cleared_by: number | null
  cleared_at: number | null
  updated_at: number
}

/** POST /devices/{id}/profiles/requests (admin): exactly one of `sids` or `all`. */
export type ProfileConsentRequest = { sids: string[] } | { all: true }

/** POST /admin/device-profiles/{id}/clear (super). `confirm_text` must equal the profile's SID or username (P1-20). */
export interface ProfileClearRequest {
  confirm_text: string
}

type Tone = 'slate' | 'green' | 'amber' | 'red' | 'blue' | 'violet'

export const profileStateView: Record<ProfileState, { label: string; tone: Tone }> = {
  active_default: { label: 'Active user', tone: 'green' },
  authorised: { label: 'Authorised', tone: 'green' },
  requested: { label: 'Consent requested', tone: 'amber' },
  blacklisted: { label: 'Blacklisted', tone: 'red' },
  cleared_pending_consent: { label: 'Cleared, awaiting consent', tone: 'blue' },
  revoked: { label: 'Revoked', tone: 'slate' },
}

export function stateView(s: string): { label: string; tone: Tone } {
  return profileStateView[s as ProfileState] ?? { label: s, tone: 'slate' }
}

export const blacklistReasonLabels: Record<BlacklistReason, string> = {
  password_changed: 'Password changed',
  password_reset: 'Password reset',
  sid_changed: 'SID changed',
  account_disabled: 'Account disabled',
  account_removed: 'Account removed',
  profile_moved: 'Profile moved',
  consent_unverifiable: 'Consent unverifiable',
}

export function blacklistReasonLabel(r: string | null): string {
  if (!r) return ''
  return blacklistReasonLabels[r as BlacklistReason] ?? r
}

/** Profiles a consent request can target: not already consented and not blacklisted (clear first). */
export function canRequestConsent(p: DeviceProfile): boolean {
  return p.state !== 'active_default' && p.state !== 'authorised' && p.state !== 'blacklisted'
}

/** The text the superuser types to confirm a clear (username when known, else SID). */
export function clearConfirmText(p: DeviceProfile): string {
  return p.username || p.sid
}
