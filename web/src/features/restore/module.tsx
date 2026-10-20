import { Navigate } from 'react-router-dom'
import type { FeatureModule } from '../../app/types'

// Restore is part of Sync > Backups & Restore; old links go there.
export default {
  id: 'restore',
  routes: [{ path: '/restore', component: () => <Navigate to="/sync/backups" replace /> }],
  requiredFeatureFlag: 'restore.v1',
} satisfies FeatureModule
