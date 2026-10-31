import { lazy } from 'react'
import { Library } from 'lucide-react'
import type { AdminTab } from '../../pages/admin/tabs/registry'

/** Administration > Full Library: every user's files (read-only, audited). */
export default {
  id: 'library',
  label: 'Full Library',
  icon: Library,
  order: 25,
  component: lazy(() => import('./LibraryTab')),
  inSidebar: true,
} satisfies AdminTab
