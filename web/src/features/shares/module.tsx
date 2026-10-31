import { lazy } from 'react'
import { Link2 } from 'lucide-react'
import type { FeatureModule } from '../../app/types'

const SharesPage = lazy(() => import('./SharesPage'))

/** Public video links the user made (shared from Files). */
export default {
  id: 'shares',
  routes: [{ path: '/shares', component: SharesPage }],
  nav: { label: 'Shared links', icon: Link2, order: 25 },
} satisfies FeatureModule
