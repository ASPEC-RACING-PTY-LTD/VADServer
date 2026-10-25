import { lazy } from 'react'
import { KeyRound } from 'lucide-react'
import type { AdminTab } from '../../pages/admin/tabs/registry'

/** Administration > API keys: read-only diagnostics keys. */
export default {
  id: 'api-keys',
  label: 'API keys',
  icon: KeyRound,
  order: 85,
  component: lazy(() => import('./ApiKeysTab')),
} satisfies AdminTab
