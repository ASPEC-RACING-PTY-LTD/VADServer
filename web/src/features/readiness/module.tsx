import { Navigate } from 'react-router-dom'
import type { FeatureModule } from '../../app/types'

// Readiness is part of Administration > Overview (admins only); old links go there.
export default {
  id: 'readiness',
  adminOnly: true,
  routes: [{ path: '/readiness', component: () => <Navigate to="/admin" replace /> }],
  requiredFeatureFlag: 'backup.v1',
} satisfies FeatureModule
