import { get, listAll, post } from '../../lib/api'
import type { User } from '../../lib/auth'
import { createKeys, queryKeys, useApiMutation, useApiQuery } from '../../lib/query'
import type { DeviceProfile, ProfileClearRequest, ProfileConsentRequest } from './types'

export const profileKeys = createKeys('profiles')

/** GET /devices/{id}/profiles (all pages). */
export function useDeviceProfiles(deviceId: number, enabled = true) {
  return useApiQuery<DeviceProfile[]>(profileKeys.list({ deviceId }), () => listAll<DeviceProfile>(`/devices/${deviceId}/profiles`), { enabled, retry: false })
}

/** POST /devices/{id}/profiles/requests (admin) → 201 {task_id}. */
export function useRequestConsent() {
  return useApiMutation<{ task_id: number }, { deviceId: number; body: ProfileConsentRequest }>(({ deviceId, body }) => post(`/devices/${deviceId}/profiles/requests`, body), {
    invalidate: [profileKeys.all],
  })
}

/** POST /admin/device-profiles/{id}/clear (super) → 200 Profile in `cleared_pending_consent`. */
export function useClearBlacklist() {
  return useApiMutation<DeviceProfile, { profileId: number; confirmText: string }>(
    ({ profileId, confirmText }) => {
      const body: ProfileClearRequest = { confirm_text: confirmText }
      return post(`/admin/device-profiles/${profileId}/clear`, body)
    },
    { invalidate: [profileKeys.all, queryKeys.summary] },
  )
}

/**
 * Whether the signed-in user is a superuser (api-v1 §11B). `/auth/me` does not
 * carry `is_superuser`, so admins read it from GET /admin/users. Returns
 * `undefined` while unknown; the server enforces the rule either way.
 */
export function useIsSuperuser(user: User): boolean | undefined {
  const own = (user as User & { is_superuser?: boolean }).is_superuser
  const isAdmin = user.role === 'admin'
  const q = useApiQuery<boolean>(
    profileKeys.sub('superuser', user.id),
    async () => {
      const r = await get<{ users: { id: number; is_superuser?: boolean }[] }>('/admin/users')
      return !!r.users?.find((u) => u.id === user.id)?.is_superuser
    },
    { enabled: isAdmin && typeof own !== 'boolean', retry: false, staleTime: 5 * 60_000 },
  )
  if (typeof own === 'boolean') return own
  if (!isAdmin) return false
  return q.data
}
