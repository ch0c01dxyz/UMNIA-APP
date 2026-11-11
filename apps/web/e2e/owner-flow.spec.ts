// Alur pemilik end-to-end di browser NYATA melawan web + business-api +
// PG native terisolasi (umnia_e2e). Langkah pengguna memakai UI +
// proxy same-origin; psql HANYA untuk setup/fault injection. Grant
// entitlement memakai CLI operator SUNGGUHAN (bukan INSERT sintetis).
// Rahasia (kata sandi, token, kode undangan, DSN operator) tidak pernah
// di-log; kode undangan hanya hidup di memori tes ini.
import { execFileSync } from 'node:child_process'
import { existsSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type Page } from '@playwright/test'

const RUN = `${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`
const PASSWORD = 'E2eUiTest123'
const BOOTSTRAP_URL = process.env.E2E_BOOTSTRAP_URL ||
  'postgres://postgres:local-bootstrap-only@localhost:5432/umnia_e2e?sslmode=disable'
const SHOTS = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'owner-flow')

const ownerEmail = `pemilik-${RUN}@e2e.umnia.local`
const staffEmail = `staf-${RUN}@e2e.umnia.local`
const staff2Email = `staf2-${RUN}@e2e.umnia.local`
const staff3Email = `staf3-${RUN}@e2e.umnia.local`
const otherEmail = `lain-${RUN}@e2e.umnia.local`
const owner2Email = `pemilik2-${RUN}@e2e.umnia.local`

let wsA = ''
let wsB = ''
let wsShot = ''
let inviteCode = ''
let inviteCode2 = ''
let inviteCode3 = ''

function psql(sql: string): string {
  return execFileSync('psql', [BOOTSTRAP_URL, '-v', 'ON_ERROR_STOP=1', '-tAc', sql], { encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] })
}

// Grant operator LEWAT CLI SUNGGUHAN (F1-OPERATOR-ENTITLEMENT-IMPLEMENT):
// binary dibangun CI/dev (E2E_GRANT_CLI), DSN operator hanya hidup di
// env proses tes ini (tidak ditulis ke file/log). Prasyarat yang hilang
// (binary/DSN) adalah GAGAL, bukan dilewati.
const REPO_ROOT = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..')
const GRANT_CLI = process.env.E2E_GRANT_CLI || path.join(REPO_ROOT, 'services', 'business-api', 'grant-entitlement')
const OPERATOR_URL = process.env.E2E_OPERATOR_DATABASE_URL || ''

function grantRental(ws: string) {
  if (!OPERATOR_URL) {
    throw new Error('E2E_OPERATOR_DATABASE_URL belum diset — provisioning operator (lihat deploy/windows/README.md) belum dijalankan; tes gagal, bukan dilewati')
  }
  if (!existsSync(GRANT_CLI)) {
    throw new Error(`CLI grant-entitlement tidak ditemukan di ${GRANT_CLI} — bangun dulu: go build -o grant-entitlement ./cmd/grant-entitlement (di services/business-api)`)
  }
  const out = execFileSync(GRANT_CLI, ['--workspace', ws, '--module', 'rental.basic', '--version', '0.1.0', '--reason', 'tes E2E owner-flow'], {
    encoding: 'utf-8',
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: OPERATOR_URL },
  })
  if (!out.startsWith('granted ')) {
    throw new Error(`grant CLI: hasil tak terduga (${out.slice(0, 80)})`)
  }
}

async function uiRegister(page: Page, email: string) {
  await page.goto('/login')
  await page.getByRole('button', { name: 'Daftar' }).click()
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Kata sandi').fill(PASSWORD)
  await page.locator('form button[type="submit"]').click()
  await expect(page.getByRole('status')).toContainText('Akun dibuat')
}

async function uiLogin(page: Page, email: string, password = PASSWORD) {
  await page.goto('/login')
  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Kata sandi').fill(password)
  await page.locator('form button[type="submit"]').click()
  await expect(page).toHaveURL(/\/workspaces\/new/)
}

async function uiLogout(page: Page) {
  await page.getByRole('button', { name: 'Keluar' }).click()
  await expect(page).toHaveURL(/\/login/)
}

// Navigasi ke rute terproteksi: full reload selalu menghapus sesi memori,
// jadi sesudah goto yang berakhir di /login, masuk lagi — alur login
// target (next) yang memang dirancang produk, bukan jalan pintas.
async function gotoAuthed(page: Page, url: string, email: string) {
  await page.goto(url)
  // Tunggu URL stabil: redirect pasca-hydration (sesi hilang) butuh waktu
  // setelah dokumen termuat; baca URL terlalu dini akan melewatkan login.
  let last = page.url()
  let stableSince = Date.now()
  const deadline = Date.now() + 10000
  while (Date.now() < deadline) {
    await page.waitForTimeout(200)
    const now = page.url()
    if (now === last) {
      if (Date.now() - stableSince >= 1000) break
    } else {
      last = now
      stableSince = Date.now()
    }
  }
  // Halaman terima-undangan tidak redirect otomatis: ia menampilkan prompt
  // "Masuk / Daftar" bila belum masuk. Klik prompt itu bila terlihat.
  const loginLink = page.getByRole('link', { name: 'Masuk / Daftar' })
  const promptVisible = await loginLink.waitFor({ state: 'visible', timeout: 5000 }).then(() => true).catch(() => false)
  if (promptVisible) {
    await loginLink.click()
    await page.waitForURL(/\/login/)
  }
  if (/\/login/.test(page.url())) {
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await page.waitForURL(`**${url}`)
  }
  await expect(page).toHaveURL(new RegExp(`${url.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')}$`))
}

async function uiCreateWorkspace(page: Page, name: string, email: string): Promise<string> {
  await gotoAuthed(page, '/workspaces/new', email)
  await page.getByLabel('Nama workspace').fill(name)
  await page.locator('form button[type="submit"]').click()
  await expect(page).toHaveURL(/\/workspaces\/[0-9a-f-]{36}\/members/)
  const m = page.url().match(/\/workspaces\/([0-9a-f-]{36})\/members/)
  if (!m) throw new Error('URL workspace tidak dikenali sesudah create')
  return m[1]
}

async function uiInvite(page: Page, ws: string, email: string, role: 'manager' | 'cashier', by: string): Promise<string> {
  await gotoAuthed(page, `/workspaces/${ws}/members`, by)
  await expect(page.getByRole('heading', { name: 'Undang staf' })).toBeVisible()
  await page.getByLabel('Email staf').fill(email)
  await page.getByLabel('Peran').selectOption(role)
  await page.locator('form button[type="submit"]').click()
  await expect(page.getByRole('heading', { name: 'Undangan dibuat' })).toBeVisible()
  const codeInput = page.locator('#invite-code')
  await expect(codeInput).toHaveAttribute('type', 'password')
  const code = await codeInput.inputValue()
  if (!code) throw new Error('kode undangan kosong')
  // Kode tidak boleh muncul sebagai teks terlihat di markup.
  // Boolean + pesan tetap: kegagalan assertion tidak boleh mencetak rahasia.
  const visible = await page.locator('body').innerText()
  expect(visible.includes(code), 'teks terlihat tidak boleh memuat kode undangan').toBe(false)
  return code
}

test.describe.serial('alur pemilik', () => {
  test('registrasi + login pemilik, buat workspace', async ({ page }) => {
    await uiRegister(page, ownerEmail)
    await uiLogin(page, ownerEmail)
    wsA = await uiCreateWorkspace(page, `Toko E2E ${RUN}`, ownerEmail)
    await expect(page.getByText(`Toko E2E ${RUN}`)).toBeVisible()
  })

  test('login salah + duplikat registrasi ditolak generik', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await uiLogout(page)
    await page.goto('/login')
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill('salah-salah')
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toContainText('email atau password salah')
    await page.getByRole('button', { name: 'Daftar' }).click()
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toContainText('email sudah terdaftar')
    await page.getByRole('button', { name: 'Masuk' }).click()
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
  })

  test('undang staf: kode tersembunyi, tautan dan kode disalin terpisah', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await page.context().grantPermissions(['clipboard-read', 'clipboard-write'])
    inviteCode = await uiInvite(page, wsA, staffEmail, 'cashier', ownerEmail)
    await page.getByRole('button', { name: 'Salin tautan' }).click()
    await expect(page.getByRole('button', { name: 'Tautan disalin' })).toBeVisible()
    const pasted = await page.evaluate(() => navigator.clipboard.readText())
    expect(pasted).toBe(`${new URL(page.url()).origin}/invites/accept?workspace_id=${wsA}`)
    await page.getByRole('button', { name: 'Salin kode' }).click()
    await expect(page.getByRole('button', { name: 'Kode disalin' })).toBeVisible()
  })

  test('staf terima undangan; kasir 403 anggota, fallback ke fitur', async ({ page }) => {
    await uiRegister(page, staffEmail)
    await uiLogin(page, staffEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${wsA}`, staffEmail)
    await page.getByLabel('Kode undangan').fill(inviteCode)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/members`)
    await expect(page.getByRole('alert')).toContainText('tidak punya izin melihat anggota')
    await page.getByRole('link', { name: 'Lihat fitur workspace' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/modules`)
    await expect(page.getByRole('heading', { name: 'Persewaan Sederhana' })).toBeVisible()
    await expect(page.getByText('Belum aktif.')).toBeVisible()
  })

  test('workspace tidak dikenal + lintas-tenant 403', async ({ page }) => {
    await uiLogin(page, staffEmail)
    await gotoAuthed(page, '/workspaces/00000000-0000-4000-8000-000000000000/members', staffEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await uiLogout(page)
    await uiLogin(page, ownerEmail)
    wsB = await uiCreateWorkspace(page, `Toko E2E Kedua ${RUN}`, ownerEmail)
    await uiLogout(page)
    await uiLogin(page, staffEmail)
    await gotoAuthed(page, `/workspaces/${wsB}/members`, staffEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await gotoAuthed(page, `/workspaces/${wsB}/modules`, staffEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
  })

  test('aktivasi tanpa entitlement: 403 jujur', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/modules`, ownerEmail)
    await expect(page.getByText('Belum aktif.')).toBeVisible()
    await page.getByRole('button', { name: 'Aktifkan Persewaan Sederhana' }).click()
    await page.getByRole('button', { name: 'Ya, aktifkan' }).click()
    await expect(page.getByRole('alert')).toContainText('Fitur ini belum tersedia untuk usaha Anda; hubungi pengelola layanan.')
    await expect(page.getByText('Belum aktif.')).toBeVisible()
  })

  test('grant operator lewat CLI lalu aktivasi 200', async ({ page }) => {
    grantRental(wsA)
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/modules`, ownerEmail)
    await page.getByRole('button', { name: 'Aktifkan Persewaan Sederhana' }).click()
    await page.getByRole('button', { name: 'Ya, aktifkan' }).click()
    await expect(page.getByText('Aktif (versi 0.1.0).')).toBeVisible()
    // Status aktif faktual: tanpa klaim tidak ada perubahan yang ditulis.
    await expect(page.getByText('Tidak ada perubahan yang ditulis.')).toHaveCount(0)
  })

  test('reload kehilangan token; lanjut dengan URL dikenal', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/modules`, ownerEmail)
    await expect(page.getByText('Aktif (versi 0.1.0).')).toBeVisible()
    await page.reload()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/modules`)
    await expect(page.getByText('Aktif (versi 0.1.0).')).toBeVisible()
  })

  test('401 menghapus sesi dan minta masuk', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/members`, ownerEmail)
    await expect(page.getByRole('heading', { name: 'Anggota', exact: true })).toBeVisible()
    psql(`DELETE FROM sessions WHERE subject_id = (SELECT id FROM identity_subjects WHERE email = '${ownerEmail}')`)
    await page.getByRole('link', { name: 'Lihat fitur workspace' }).click()
    await expect(page).toHaveURL(/\/login\?next=/)
    await expect(page.getByLabel('Email')).toBeVisible()
    await uiLogin(page, ownerEmail)
  })

  test('undang akun salah 403 + undangan kedaluwarsa 409', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    inviteCode2 = await uiInvite(page, wsA, staff2Email, 'manager', ownerEmail)
    await uiRegister(page, otherEmail)
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${wsA}`, otherEmail)
    await page.getByLabel('Kode undangan').fill(inviteCode2)
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toContainText('undangan ini bukan untuk akun ini')
    await uiLogout(page)
    await uiLogin(page, ownerEmail)
    inviteCode3 = await uiInvite(page, wsA, staff3Email, 'cashier', ownerEmail)
    psql(`UPDATE staff_invites SET expires_at = now() - interval '1 hour' WHERE email = '${staff3Email}' AND used_at IS NULL`)
    await uiLogout(page)
    await uiRegister(page, staff3Email)
    await uiLogin(page, staff3Email)
    await gotoAuthed(page, `/invites/accept?workspace_id=${wsA}`, staff3Email)
    await page.getByLabel('Kode undangan').fill(inviteCode3)
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toContainText('undangan tidak valid, kedaluwarsa, atau sudah dipakai')
  })

  test('keyboard: tab berurutan + fokus ringkasan error', async ({ page }) => {
    await page.goto('/login')
    await page.locator('#login-email').click()
    await page.keyboard.press('Tab')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('login-password')
    await page.keyboard.press('Tab')
    const focused = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      return el ? `${el.tagName}:${el.getAttribute('type')}` : ''
    })
    expect(focused).toBe('BUTTON:submit')
    await page.locator('form button[type="submit"]').click()
    await expect(page.getByRole('alert')).toBeVisible()
    expect(await page.evaluate(() => document.activeElement?.getAttribute('role'))).toBe('alert')
  })

  test('[terisolasi] state loading saat respons lambat', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await page.route('**/api/workspaces/*/members', async (route) => {
      await new Promise(r => setTimeout(r, 600))
      await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${wsA}/members`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await page.unroute('**/api/workspaces/*/members')
    await expect(page.getByRole('heading', { name: 'Anggota', exact: true })).toBeVisible()
  })

  test('screenshot lima layar 360/768/1440', async ({ page }) => {
    await uiRegister(page, owner2Email)
    await uiLogin(page, owner2Email)
    wsShot = await uiCreateWorkspace(page, `Toko Screenshot ${RUN}`, owner2Email)
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogout(page)
      await page.goto('/login')
      await expect(page.getByLabel('Email')).toBeVisible()
      await page.screenshot({ path: path.join(SHOTS, `01-login-${size.width}.png`) })
      await uiLogin(page, owner2Email)
      await gotoAuthed(page, '/workspaces/new', owner2Email)
      await expect(page.getByLabel('Nama workspace')).toBeVisible()
      await page.screenshot({ path: path.join(SHOTS, `02-workspace-new-${size.width}.png`) })
      await gotoAuthed(page, `/workspaces/${wsShot}/members`, owner2Email)
      await expect(page.getByRole('heading', { name: 'Anggota', exact: true })).toBeVisible()
      await page.screenshot({ path: path.join(SHOTS, `03-members-${size.width}.png`) })
      await gotoAuthed(page, `/workspaces/${wsShot}/modules`, owner2Email)
      await expect(page.getByText('Belum aktif.')).toBeVisible()
      await page.screenshot({ path: path.join(SHOTS, `04-modules-${size.width}.png`) })
      await gotoAuthed(page, `/invites/accept?workspace_id=${wsShot}`, owner2Email)
      await expect(page.getByLabel('Kode undangan')).toBeVisible()
      await page.screenshot({ path: path.join(SHOTS, `05-accept-${size.width}.png`) })
    }
  })
})
