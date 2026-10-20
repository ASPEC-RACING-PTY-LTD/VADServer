import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import './index.css'
import { App } from './App'
import { AuthProvider } from './lib/auth'
import { UIProvider } from './components/ui'
import { UploadProvider } from './lib/uploads'
import { ApiError } from './lib/api'
import { resetOnHardReload } from './lib/navState'

// Theme: explicit choice in localStorage, otherwise follow the system.
const applyTheme = () => {
  const pref = localStorage.getItem('vd-theme')
  const dark = pref ? pref === 'dark' : window.matchMedia('(prefers-color-scheme: dark)').matches
  document.documentElement.classList.toggle('dark', dark)
}
applyTheme()
resetOnHardReload()
window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', applyTheme)

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 10_000,
      refetchOnWindowFocus: true,
      retry: (count, err) => !(err instanceof ApiError && err.status < 500) && count < 2,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <UIProvider>
          <AuthProvider>
            <UploadProvider>
              <App />
            </UploadProvider>
          </AuthProvider>
        </UIProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
