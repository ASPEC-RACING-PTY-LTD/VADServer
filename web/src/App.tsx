import { Suspense } from 'react'
import { Navigate, Route, Routes } from 'react-router-dom'
import { useAuth } from './lib/auth'
import { Layout } from './components/Layout'
import { PageLoader } from './components/ui'
import { LoginPage, SetupPage } from './pages/Auth'
import { enabledModules } from './app/registry'
import { useFeatureContext } from './app/useFeatureContext'

// Routes come from the feature registry (src/app/registry.ts); add pages there, not here.
function AppRoutes() {
  const ctx = useFeatureContext()
  return (
    <Routes>
      {enabledModules(ctx).flatMap((m) => m.routes.map((r) => <Route key={`${m.id}:${r.path}`} path={r.path} element={<r.component />} />))}
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export function App() {
  const { state } = useAuth()
  if (state.status === 'loading') {
    return (
      <div className="flex h-screen items-center justify-center">
        <PageLoader label="Connecting to VaultDock" />
      </div>
    )
  }
  if (state.status === 'anonymous') {
    return state.needsSetup ? <SetupPage tokenRequired={state.tokenRequired} /> : <LoginPage registration={state.registration} siteName={state.siteName} />
  }
  return (
    <Layout>
      <Suspense fallback={<PageLoader />}>
        <AppRoutes />
      </Suspense>
    </Layout>
  )
}
