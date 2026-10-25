import { lazy } from 'react'
import { Sparkles } from 'lucide-react'
import type { AdminTab } from '../../pages/admin/tabs/registry'

/** Administration > Smart processing: processing intensity, the inference worker and its models. */
export default {
  id: 'smart',
  label: 'Smart processing',
  icon: Sparkles,
  order: 65,
  component: lazy(() => import('./SettingsPage')),
  inSidebar: true,
} satisfies AdminTab
