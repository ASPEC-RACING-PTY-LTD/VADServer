import { expect, test } from '@playwright/test'

test.skip(!process.env.E2E_BASE_URL, 'E2E_BASE_URL not set')

test('web app loads', async ({ page }) => {
  await page.goto('/')
  await expect(page).toHaveTitle(/.+/)
})
