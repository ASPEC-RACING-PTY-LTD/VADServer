import { defineConfig, mergeConfig } from 'vitest/config'
import viteConfig from './vite.config'

// Unit/component tests run in jsdom. Playwright specs live in e2e/ and are
// excluded here; run them with `npm run e2e`.
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      setupFiles: ['./src/test/setup.ts'],
      include: ['src/**/*.test.{ts,tsx}'],
      css: false,
    },
  }),
)
