// Playwright E2E untuk alur pemilik UMNIA. Browser NYATA melawan web +
// business-api yang benar-benar berjalan + PG native terisolasi.
// Tanpa trace/video/HAR/screenshot otomatis: rahasia (token, kata sandi,
// kode undangan) tidak boleh masuk artefak tersimpan. Screenshot hanya
// diambil eksplisit di spec, dari form kosong/tersembunyi.
import { defineConfig } from '@playwright/test'

const baseURL = process.env.PLAYWRIGHT_BASE_URL || 'http://localhost:3000'
const badUpstreamURL = process.env.PLAYWRIGHT_BAD_UPSTREAM_URL || 'http://localhost:3001'

export default defineConfig({
  testDir: './e2e',
  timeout: 120 * 1000,
  expect: { timeout: 10 * 1000 },
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: 0,
  reporter: [['list']],
  use: {
    baseURL,
    trace: 'off',
    video: 'off',
    screenshot: 'off'
  },
  projects: [
    { name: 'owner-flow', testMatch: 'owner-flow.spec.ts', use: { timezoneId: 'Asia/Jakarta' } },
    { name: 'proxy-session', testMatch: 'proxy-session.spec.ts' }
  ],
  webServer: [
    {
      // Server utama: business-api di http://localhost:8080 (default
      // NUXT_BUSINESS_API_BASE di nuxt.config.ts).
      command: 'npm run preview -- --port 3000',
      url: baseURL,
      reuseExistingServer: !process.env.CI,
      timeout: 120 * 1000
    },
    {
      // Server kedua khusus uji proxy 502: upstream mati (port 9 tertutup).
      // Dipakai hanya oleh proxy-session.spec.ts.
      command: 'npm run preview -- --port 3001',
      url: badUpstreamURL,
      reuseExistingServer: !process.env.CI,
      timeout: 120 * 1000,
      env: { NUXT_BUSINESS_API_BASE: 'http://127.0.0.1:9' }
    }
  ]
})
