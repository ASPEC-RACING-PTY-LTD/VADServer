import { lazy } from 'react'
import { History } from 'lucide-react'
import type { FeatureModule } from '../../app/types'

const RestorePage = lazy(() => import('./RestorePage'))

export default {
  id: 'restore',
  routes: [{ path: '/restore', component: RestorePage }],
  nav: { label: 'Restore', icon: History, order: 46 },
  requiredFeatureFlag: 'restore.v1',
} satisfies FeatureModule
