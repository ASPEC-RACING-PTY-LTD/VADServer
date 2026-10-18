import { lazy } from 'react'
import { ShieldCheck } from 'lucide-react'
import type { FeatureModule } from '../../app/types'
import { summaryBadge } from '../../app/summary'

const ApprovalsPage = lazy(() => import('./ApprovalsPage'))

export default {
  id: 'approvals',
  routes: [
    { path: '/approvals', component: ApprovalsPage },
    { path: '/approvals/:id', component: ApprovalsPage },
  ],
  nav: { label: 'Approvals', icon: ShieldCheck, order: 65, useBadge: summaryBadge('pending_approvals') },
  requiredFeatureFlag: 'approvals.v1',
} satisfies FeatureModule
