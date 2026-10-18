import { Suspense, useMemo } from 'react'
import { NavLink, Navigate, Route, Routes } from 'react-router-dom'
import { Shield } from 'lucide-react'
import { cx, PageHeader, PageLoader } from '../../components/ui'
import { useFeatureContext } from '../../app/useFeatureContext'
import { adminTabs, enabledAdminTabs, tabPath } from './tabs/registry'

// Tabs come from the admin tabs registry (tabs/registry.ts); features add tabs there via
// features/<x>/admin.tsx or an `adminTabs` export in features/<x>/module.tsx.
export default function AdminPage() {
  const feature = useFeatureContext()
  // Every admin is a superuser until a narrower admin role exists (see AdminTabContext).
  const tabs = useMemo(() => enabledAdminTabs(adminTabs, { ...feature, isSuperuser: feature.isAdmin }), [feature])
  return (
    <div>
      <PageHeader title="Admin console" description="Manage users, storage and the health of this VaultDock server." icon={<Shield className="size-5" />} />
      <nav className="no-scrollbar mb-6 flex gap-1 overflow-x-auto border-b border-slate-200 dark:border-slate-800">
        {tabs.map((t) => {
          const to = tabPath(t)
          return (
            <NavLink key={t.id} to={to === '' ? '/admin' : `/admin/${to}`} end={to === ''} className={({ isActive }) => cx('-mb-px flex items-center gap-2 border-b-2 px-3 py-2.5 text-sm font-medium whitespace-nowrap', isActive ? 'border-brand-600 text-brand-700 dark:text-brand-300' : 'border-transparent text-slate-500 hover:text-slate-800 dark:hover:text-slate-200')}>
              <t.icon className="size-4" />
              {t.label}
            </NavLink>
          )
        })}
      </nav>
      <Suspense fallback={<PageLoader />}>
        <Routes>
          {tabs.map((t) => {
            const to = tabPath(t)
            return to === '' ? <Route key={t.id} index element={<t.component />} /> : <Route key={t.id} path={to} element={<t.component />} />
          })}
          <Route path="*" element={<Navigate to="/admin" replace />} />
        </Routes>
      </Suspense>
    </div>
  )
}
