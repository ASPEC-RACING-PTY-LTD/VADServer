import { lazy } from 'react'
import { Navigate } from 'react-router-dom'
import { Activity, ArrowUpCircle, FolderOpen, HardDrive, LayoutDashboard, Monitor, RefreshCw, Shield, ShieldCheck, Trash2, Users, Workflow } from 'lucide-react'
import { DashboardPage } from '../pages/Dashboard'
import { FilesPage } from '../pages/Files'
import type { FeatureModule } from './types'

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
  // Uploads moved into Files (the Uploads dialog); old links land there.
  { id: 'uploads', routes: [{ path: '/uploads', component: () => <Navigate to="/files?uploads=1" replace /> }] },
  {
    id: 'sync',
    routes: [
      { path: '/sync', component: SyncPage },
      { path: '/sync/:tab', component: SyncPage },
    ],
    nav: { label: 'Sync', icon: RefreshCw, order: 40 },
  },
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
  // Management: Administration (the console's remaining tabs) and the console pages used most,
  // linked straight from the sidebar (their tabs are hidden in the console, see tabs/registry.ts).
  { id: 'admin', adminOnly: true, routes: [{ path: '/admin/*', component: AdminPage }], nav: { label: 'Administration', icon: Shield, to: '/admin', order: 10, section: 'admin', end: true } },
  { id: 'admin-users', adminOnly: true, routes: [], nav: { label: 'Users', icon: Users, to: '/admin/users', order: 20, section: 'admin' } },
  { id: 'admin-devices', adminOnly: true, routes: [], nav: { label: 'Devices', icon: Monitor, to: '/admin/devices', order: 30, section: 'admin' } },
  { id: 'admin-storage', adminOnly: true, routes: [], nav: { label: 'Storage', icon: HardDrive, to: '/admin/storage', order: 40, section: 'admin' } },
  { id: 'admin-updates', adminOnly: true, routes: [], nav: { label: 'Updates', icon: ArrowUpCircle, to: '/admin/updates', order: 50, section: 'admin' } },
  { id: 'admin-audit', adminOnly: true, routes: [], nav: { label: 'Audit log', icon: ShieldCheck, to: '/admin/audit', order: 60, section: 'admin' } },
]
