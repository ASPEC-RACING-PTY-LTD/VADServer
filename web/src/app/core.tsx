import { lazy } from 'react'
import { Activity, FolderOpen, HardDriveUpload, LayoutDashboard, Monitor, RefreshCw, Shield, Trash2, Workflow } from 'lucide-react'
import { DashboardPage } from '../pages/Dashboard'
import { FilesPage } from '../pages/Files'
import type { FeatureModule } from './types'

const UploadsPage = lazy(() => import('../pages/Uploads'))
const SyncPage = lazy(() => import('../pages/Sync'))
const DevicesPage = lazy(() => import('../pages/Devices'))
const AutomationsPage = lazy(() => import('../pages/Automations'))
const AutomationEditorPage = lazy(() => import('../automation/EditorPage'))
const ActivityPage = lazy(() => import('../pages/Activity'))
const TrashPage = lazy(() => import('../pages/Trash'))
const AccountPage = lazy(() => import('../pages/Account'))
const AdminPage = lazy(() => import('../pages/admin/Admin'))

/** The pre-existing pages, expressed as modules. Nav orders are spaced by 10 so features can slot in between. */
export const coreModules: FeatureModule[] = [
  { id: 'dashboard', routes: [{ path: '/', component: DashboardPage }], nav: { label: 'Dashboard', icon: LayoutDashboard, order: 10, end: true } },
  {
    id: 'files',
    routes: [
      { path: '/files', component: FilesPage },
      { path: '/files/:folderId', component: FilesPage },
    ],
    nav: { label: 'Files', icon: FolderOpen, order: 20 },
  },
  { id: 'uploads', routes: [{ path: '/uploads', component: UploadsPage }], nav: { label: 'Uploads', icon: HardDriveUpload, order: 30 } },
  { id: 'sync', routes: [{ path: '/sync', component: SyncPage }], nav: { label: 'Synchronisation', icon: RefreshCw, order: 40 } },
  { id: 'devices', routes: [{ path: '/devices', component: DevicesPage }], nav: { label: 'Devices', icon: Monitor, order: 50 } },
  {
    id: 'automations',
    routes: [
      { path: '/automations', component: AutomationsPage },
      { path: '/automations/new', component: AutomationEditorPage },
      { path: '/automations/:id', component: AutomationEditorPage },
    ],
    nav: { label: 'Automations', icon: Workflow, order: 60 },
  },
  { id: 'activity', routes: [{ path: '/activity', component: ActivityPage }], nav: { label: 'Activity', icon: Activity, order: 70 } },
  { id: 'trash', routes: [{ path: '/trash', component: TrashPage }], nav: { label: 'Trash', icon: Trash2, order: 80 } },
  { id: 'account', routes: [{ path: '/account', component: AccountPage }] },
  { id: 'admin', adminOnly: true, routes: [{ path: '/admin/*', component: AdminPage }], nav: { label: 'Admin console', icon: Shield, to: '/admin', order: 10, section: 'admin' } },
]
