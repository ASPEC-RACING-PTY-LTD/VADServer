import { lazy } from 'react'
import { Sparkles } from 'lucide-react'
import type { FeatureModule } from '../../app/types'

const SmartSection = lazy(() => import('./SmartSection'))

/** Smart processing: analyse recordings, review suggested segments, render clips, clean up originals. */
export default {
  id: 'smart',
  routes: [{ path: '/smart/*', component: SmartSection }],
  nav: { label: 'Smart Processing', icon: Sparkles, to: '/smart', order: 30 },
} satisfies FeatureModule
