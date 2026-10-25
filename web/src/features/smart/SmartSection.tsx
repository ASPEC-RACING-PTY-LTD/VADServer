import { lazy, Suspense } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { cx, PageLoader } from '../../components/ui'

const OverviewPage = lazy(() => import('./OverviewPage'))
const NewQueuePage = lazy(() => import('./NewQueuePage'))
const QueuesPage = lazy(() => import('./QueuesPage'))
const QueuePage = lazy(() => import('./QueuePage'))
const ReviewPage = lazy(() => import('./ReviewPage'))
const CleanupPage = lazy(() => import('./CleanupPage'))
const PresetsPage = lazy(() => import('./PresetsPage'))
const PreferencesPage = lazy(() => import('./PreferencesPage'))

const tabs = [
  { to: '/smart', label: 'Overview', end: true },
  { to: '/smart/new', label: 'New Queue' },
  { to: '/smart/queues', label: 'Active Queues' },
  { to: '/smart/review', label: 'Review' },
  { to: '/smart/cleanup', label: 'Cleanup' },
  { to: '/smart/completed', label: 'Completed Queues' },
  { to: '/smart/presets', label: 'Detection Presets' },
  { to: '/smart/settings', label: 'Settings' },
]

/** The Smart Processing section: its tabs and pages. */
export default function SmartSection() {
  return (
    <div>
      <nav aria-label="Smart processing" className="mb-5 flex gap-1 overflow-x-auto border-b border-slate-200 dark:border-slate-800">
        {tabs.map((t) => (
          <NavLink
            key={t.to}
            to={t.to}
            end={t.end}
            className={({ isActive }) =>
              cx(
                '-mb-px border-b-2 px-3 py-2 text-sm font-medium whitespace-nowrap transition',
                isActive ? 'border-brand-500 text-brand-600 dark:text-brand-300' : 'border-transparent text-slate-500 hover:text-slate-800 dark:hover:text-slate-200',
              )
            }
          >
            {t.label}
          </NavLink>
        ))}
      </nav>
      <Suspense fallback={<PageLoader />}>
        <Routes>
          <Route index element={<OverviewPage />} />
          <Route path="new" element={<NewQueuePage />} />
          <Route path="queues" element={<QueuesPage view="active" />} />
          <Route path="completed" element={<QueuesPage view="completed" />} />
          <Route path="queues/:id" element={<QueuePage />} />
          <Route path="review" element={<ReviewPage />} />
          <Route path="review/:itemId" element={<ReviewPage />} />
          <Route path="cleanup" element={<CleanupPage />} />
          <Route path="presets" element={<PresetsPage />} />
          <Route path="settings" element={<PreferencesPage />} />
          <Route path="*" element={<Navigate to="/smart" replace />} />
        </Routes>
      </Suspense>
    </div>
  )
}
