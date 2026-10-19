import { lazy } from 'react'
import { Copy } from 'lucide-react'
import type { AdminTab } from '../../pages/admin/tabs/registry'

/** Admin > Copies: copy rules ("RAID style" copies to other locations and nodes) and their health. */
const tab: AdminTab = { id: 'copies', label: 'Copies', icon: Copy, order: 36, component: lazy(() => import('./CopiesTab')) }

export default tab
