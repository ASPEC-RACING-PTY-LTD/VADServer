import { useMemo } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useUser } from '../lib/auth'
import { FEATURE_FLAGS_REFRESH_MS, featureFlagsKey, fetchFeatureFlags, noFeatureFlags } from './registry'
import type { FeatureContext } from './types'

/**
 * Server feature flags (`GET /info` `features`), cached per user and refreshed every few minutes.
 * `ready` is false until the first answer (or failure); until then `flags` is empty.
 */
export function useFeatureFlags(): { flags: ReadonlySet<string>; ready: boolean } {
  const user = useUser()
  const q = useQuery({
    queryKey: featureFlagsKey(user.id),
    queryFn: fetchFeatureFlags,
    staleTime: FEATURE_FLAGS_REFRESH_MS,
    refetchInterval: FEATURE_FLAGS_REFRESH_MS,
  })
  return { flags: q.data ?? noFeatureFlags, ready: !q.isPending || q.isError }
}

/** Visibility context for the signed-in user. Only valid inside the authenticated tree. */
export function useFeatureContext(): FeatureContext {
  const user = useUser()
  const isAdmin = user.role === 'admin'
  const { flags } = useFeatureFlags()
  return useMemo(() => ({ isAdmin, flags }), [isAdmin, flags])
}
