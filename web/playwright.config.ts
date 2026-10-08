import { defineConfig, devices } from '@playwright/test'

// Onboarding canary only (make canary-onboarding): one serial test that
// drives a fresh install from the token link to a finished mission.
export default defineConfig({
  testDir: 'e2e',
  outputDir: 'e2e/.out/test-results',
  retries: 0,
  workers: 1,
  reporter: [['list']],
  // Covers the whole walk: the mission stage alone may take 8 minutes.
  timeout: 15 * 60_000,
  use: {
    baseURL: process.env.CANARY_BASE_URL,
    trace: 'off',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
