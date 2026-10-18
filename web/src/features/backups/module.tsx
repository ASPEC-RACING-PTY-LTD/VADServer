import { lazy } from 'react'
import { HardDriveDownload } from 'lucide-react'
import type { FeatureModule } from '../../app/types'

const JobsPage = lazy(() => import('./JobsPage'))
const JobPage = lazy(() => import('./JobPage'))
const RunPage = lazy(() => import('./RunPage'))

export default {
  id: 'backups',
  routes: [
    { path: '/backups', component: JobsPage },
    { path: '/backups/jobs/:id', component: JobPage },
    { path: '/backups/runs/:id', component: RunPage },
  ],
  nav: { label: 'Backups', icon: HardDriveDownload, order: 45 },
  requiredFeatureFlag: 'backup.v1',
} satisfies FeatureModule
