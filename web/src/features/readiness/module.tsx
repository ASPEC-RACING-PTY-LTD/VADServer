import { lazy } from 'react'
import { Gauge } from 'lucide-react'
import type { FeatureModule } from '../../app/types'

const ReadinessPage = lazy(() => import('./ReadinessPage'))

// There is no readiness flag in api-v1 §1.3 (P1-21); readiness is about backup jobs, so it follows backup.v1.
export default {
  id: 'readiness',
  routes: [{ path: '/readiness', component: ReadinessPage }],
  nav: { label: 'Readiness', icon: Gauge, order: 47 },
  requiredFeatureFlag: 'backup.v1',
} satisfies FeatureModule
