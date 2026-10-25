import { lazy } from 'react'
import type { FeatureModule } from '../../app/types'

/** Video editor: a full-screen workspace opened from a video's "Edit video" action. */
export default {
  id: 'video-editor',
  routes: [{ path: '/video-editor/:id', component: lazy(() => import('./EditorPage')), fullscreen: true }],
} satisfies FeatureModule
