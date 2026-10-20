import { lazy } from 'react'
import { Navigate } from 'react-router-dom'
import type { FeatureModule } from '../../app/types'

const JobPage = lazy(() => import('./JobPage'))
const RunPage = lazy(() => import('./RunPage'))

// Backups live on the Sync page (Backups & Restore tab); job and run pages keep their own routes.
export default {
  id: 'backups',
  routes: [
    { path: '/backups', component: () => <Navigate to="/sync/backups" replace /> },
    { path: '/backups/jobs/:id', component: JobPage },
    { path: '/backups/runs/:id', component: RunPage },
  ],
  requiredFeatureFlag: 'backup.v1',
} satisfies FeatureModule
