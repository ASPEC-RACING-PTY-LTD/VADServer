import { defineConfig, devices } from '@playwright/test'

// Skeleton only. Specs run against an already running server given by
// E2E_BASE_URL (e.g. http://localhost:8080); browsers are not bundled, install
// them with `npx playwright install chromium` before the first run.
export default defineConfig({
  testDir: '.',
  testMatch: '**/*.spec.ts',
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: 'list',
  use: {
    baseURL: process.env.E2E_BASE_URL,
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
