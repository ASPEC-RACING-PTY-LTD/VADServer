import { lazy } from 'react'
import { Server } from 'lucide-react'
import type { AdminTab } from '../../pages/admin/tabs/registry'

/** Admin > Nodes: storage node agents (vad-node) on other machines. */
const tab: AdminTab = { id: 'nodes', label: 'Nodes', icon: Server, order: 35, component: lazy(() => import('./NodesTab')) }

export default tab
