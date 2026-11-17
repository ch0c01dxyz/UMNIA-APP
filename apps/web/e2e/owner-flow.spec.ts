// Alur pemilik end-to-end di browser NYATA melawan web + business-api +
// PG native terisolasi (umnia_e2e). Langkah pengguna memakai UI +
// proxy same-origin; psql HANYA untuk setup/fault injection. Grant
// entitlement memakai CLI operator SUNGGUHAN (bukan INSERT sintetis).
// Rahasia (kata sandi, token, kode undangan, DSN operator) tidak pernah
// di-log; kode undangan hanya hidup di memori tes ini.
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type APIRequestContext, type Page } from '@playwright/test'

const RUN = `${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`
const PASSWORD = 'E2eUiTest123'
const BOOTSTRAP_URL = process.env.E2E_BOOTSTRAP_URL ||
  'postgres://postgres:local-bootstrap-only@localhost:5432/umnia_e2e?sslmode=disable'
const SHOTS = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'owner-flow')
const SHOTS_UI01 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-01')
const SHOTS_UI02 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-02')

const ownerEmail = `pemilik-${RUN}@e2e.umnia.local`
const staffEmail = `staf-${RUN}@e2e.umnia.local`
const staff2Email = `staf2-${RUN}@e2e.umnia.local`
const staff3Email = `staf3-${RUN}@e2e.umnia.local`
const otherEmail = `lain-${RUN}@e2e.umnia.local`
const owner2Email = `pemilik2-${RUN}@e2e.umnia.local`
const owner3Email = `pemilik3-${RUN}@e2e.umnia.local`
const owner4Email = `pemilik4-${RUN}@e2e.umnia.local`

let wsA = ''
let wsB = ''
let wsShot = ''
let wsFresh = ''
let wsAssets = ''
let wsAssetsB = ''
let wsCat = ''
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

async function apiToken(req: APIRequestContext, email: string): Promise<string> {
  const res = await req.post('/api/auth/login', { data: { email, password: PASSWORD } })
  expect(res.status()).toBe(200)
  return (await res.json() as { token: string }).token
}

async function seedCustomers(req: APIRequestContext, ws: string, token: string, count: number, prefix: string): Promise<void> {
  for (let i = 0; i < count; i++) {
    const res = await req.post(`/api/workspaces/${ws}/customers`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { display_name: `${prefix} ${i + 1}` }
    })
    expect(res.status()).toBe(201)
  }
}

async function seedItems(req: APIRequestContext, ws: string, token: string, count: number, prefix: string): Promise<void> {
  for (let i = 0; i < count; i++) {
    const res = await req.post(`/api/workspaces/${ws}/catalog/items`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { sku: `${prefix}-${i + 1}`, name: `Barang ${prefix} ${i + 1}`, unit: 'pcs' }
    })
    expect(res.status()).toBe(201)
  }
}

async function listRows(page: Page, which: 'customers' | 'catalog'): Promise<string[]> {
  const res = await page.request.get(`/api/workspaces/${wsA}/${which === 'customers' ? 'customers' : 'catalog/items'}?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, ownerEmail)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: Array<{ id: string, display_name?: string, sku?: string }> }
  return body.items.map(i => i.id)
}

function listItems(page: Page) {
  return page.locator('section[aria-labelledby="list-heading"] li')
}

async function listCustomerNames(page: Page, ws: string): Promise<string[]> {
  const res = await page.request.get(`/api/workspaces/${ws}/customers?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, ownerEmail)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: Array<{ display_name: string }> }
  return body.items.map(i => i.display_name)
}

async function listItemSkus(page: Page, ws: string): Promise<string[]> {
  const res = await page.request.get(`/api/workspaces/${ws}/catalog/items?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, ownerEmail)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: Array<{ sku: string }> }
  return body.items.map(i => i.sku)
}

async function seedAssets(req: APIRequestContext, ws: string, token: string, itemId: string, count: number, prefix: string): Promise<void> {
  for (let i = 0; i < count; i++) {
    const res = await req.post(`/api/workspaces/${ws}/assets`, {
      headers: { Authorization: `Bearer ${token}` },
      data: { catalog_item_id: itemId, serial: `${prefix}-${i + 1}` }
    })
    expect(res.status()).toBe(201)
  }
}

async function listAssetRows(page: Page, ws: string): Promise<Array<{ id: string, catalog_item_id: string, serial: string }>> {
  const res = await page.request.get(`/api/workspaces/${ws}/assets?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, ownerEmail)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: Array<{ id: string, catalog_item_id: string, serial: string }> }
  return body.items
}

// Cari satu item katalog berdasarkan SKU; halaman API dipaginasi 100
// baris, jadi telusuri offset 0/100/200 sampai halaman pendek.
async function catalogItemBySku(page: Page, ws: string, sku: string): Promise<{ id: string, sku: string, name: string, status: string }> {
  const token = await apiToken(page.request, ownerEmail)
  for (const offset of [0, 100, 200]) {
    const res = await page.request.get(`/api/workspaces/${ws}/catalog/items?limit=100&offset=${offset}`, {
      headers: { Authorization: `Bearer ${token}` }
    })
    expect(res.status()).toBe(200)
    const body = await res.json() as { items: Array<{ id: string, sku: string, name: string, status: string }> }
    const found = body.items.find(i => i.sku === sku)
    if (found) return found
    if (body.items.length < 100) break
  }
  throw new Error(`item ${sku} tidak ditemukan di katalog ${ws}`)
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
    // Tautan utama "Lihat fitur workspace" diganti nav workspace (F2-UI-01);
    // "Fitur" menuju halaman yang sama dan tetap menguji 401 saat navigasi.
    await page.getByRole('link', { name: 'Fitur' }).click()
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

  test('pelanggan: daftar kosong, buat 201, tampil + persist', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(page.getByRole('heading', { name: 'Pelanggan', exact: true })).toBeVisible()
    await expect(page.getByText('Belum ada pelanggan.')).toBeVisible()
    await page.getByLabel('Nama pelanggan').fill('Budi Santoso')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('Budi Santoso')
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(listItems(page)).toHaveCount(1)
  })

  test('pelanggan: nama duplikat boleh, markup tersimpan literal', async ({ page }) => {
    let dialogFired = false
    page.on('dialog', () => { dialogFired = true })
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await page.getByLabel('Nama pelanggan').fill('Budi Santoso')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    await expect(listItems(page).filter({ hasText: 'Budi Santoso' })).toHaveCount(2)
    const markup = '<img src=x onerror=alert(1)><b>Tebal</b>'
    await page.getByLabel('Nama pelanggan').fill(markup)
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    const listHtml = await page.locator('section[aria-labelledby="list-heading"]').innerHTML()
    expect(listHtml.includes(markup), 'markup harus tampil sebagai teks literal, bukan HTML').toBe(false)
    expect(await page.locator('section[aria-labelledby="list-heading"] img').count()).toBe(0)
    expect(await page.locator('section[aria-labelledby="list-heading"] b').count()).toBe(0)
    expect(dialogFired, 'tidak boleh ada dialog dari markup tersimpan').toBe(false)
  })

  test('pelanggan: validasi 400 per-field + rune Unicode', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.locator('#customer-name-error')).toContainText('display_name wajib diisi dan maksimal 200 karakter')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('customer-name')
    await page.getByLabel('Nama pelanggan').fill('é'.repeat(201))
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.locator('#customer-name-error')).toContainText('display_name wajib diisi dan maksimal 200 karakter')
    await page.getByLabel('Nama pelanggan').fill('😀'.repeat(201))
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.locator('#customer-name-error')).toContainText('display_name wajib diisi dan maksimal 200 karakter')
    // 200 emoji = 200 rune (Go) meski 400 unit UTF-16 JS: harus 201.
    await page.getByLabel('Nama pelanggan').fill('😀'.repeat(200))
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
  })

  test('katalog: buat 201, SKU trim/case, duplikat 409, case berbeda boleh', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await expect(page.getByText('Belum ada barang.')).toBeVisible()
    await page.getByLabel('Kode barang (SKU)').fill('  AbC-1  ')
    await page.getByLabel('Nama barang').fill('Kabel HDMI')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    await expect(listItems(page).first()).toContainText('AbC-1 — Kabel HDMI (pcs)')
    await page.getByLabel('Kode barang (SKU)').fill('AbC-1')
    await page.getByLabel('Nama barang').fill('Kabel lain')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.locator('#item-sku-error')).toContainText('sku sudah dipakai di workspace ini')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('item-sku')
    await expect(listItems(page)).toHaveCount(1)
    await page.getByLabel('Kode barang (SKU)').fill('abc-1')
    await page.getByLabel('Nama barang').fill('Kabel kecil')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    await expect(listItems(page)).toHaveCount(2)
    await page.getByLabel('Kode barang (SKU)').fill('X'.repeat(65))
    await page.getByLabel('Nama barang').fill('Terlalu panjang')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.locator('#item-sku-error')).toContainText('maksimal 64 karakter')
  })

  test('pelanggan/katalog: kasir baca 200 + create 403, nonanggota 403', async ({ page }) => {
    await uiLogin(page, staffEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, staffEmail)
    await expect(page.getByRole('heading', { name: 'Pelanggan', exact: true })).toBeVisible()
    await expect(page.getByLabel('Nama pelanggan')).toBeVisible()
    const before = await listItems(page).count()
    await page.getByLabel('Nama pelanggan').fill('Kasir Coba')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('alert').first()).toContainText('tidak punya izin mengelola data usaha')
    await expect(page).toHaveURL(new RegExp(`/workspaces/${wsA}/customers$`))
    await expect(listItems(page)).toHaveCount(before)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, staffEmail)
    await page.getByLabel('Kode barang (SKU)').fill('KASIR-1')
    await page.getByLabel('Nama barang').fill('Coba kasir')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('alert').first()).toContainText('tidak punya izin mengelola data usaha')
    await expect(page).toHaveURL(new RegExp(`/workspaces/${wsA}/catalog$`))
    await uiLogout(page)
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
  })

  test('pelanggan: 51 baris + buat + muat semua tanpa hilang/duplikat', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    await seedCustomers(request, wsA, token, 51, 'Uji Muat')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    await expect(page.getByRole('button', { name: 'Muat lebih' })).toBeVisible()
    await page.getByLabel('Nama pelanggan').fill('Pelanggan Baru 52')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    // Sesudah simpan daftar di-reset ke halaman 1 (50 baris), bukan
    // ditempeli baris baru pada prefix lama.
    await expect(listItems(page)).toHaveCount(50)
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(listItems(page)).toHaveCount(56)
    const ids = await listRows(page, 'customers')
    expect(ids.length).toBe(56)
    expect(new Set(ids).size).toBe(56)
  })

  test('pelanggan: muat-lebih gagal pertahankan kursor; respons basi diabaikan', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    await page.route(
      (url) => url.pathname.includes('/customers') && url.searchParams.has('offset'),
      async (route) => { await route.abort() }
    )
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    // Route dibatalkan -> apiFetch melempar ApiError 502 dengan pesan
    // "Layanan tidak tersedia..."; baris dan kursor harus tetap utuh.
    await expect(page.getByRole('alert')).toContainText('Layanan tidak tersedia')
    await expect(listItems(page)).toHaveCount(50)
    await expect(page.getByRole('button', { name: 'Muat lebih' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(listItems(page)).toHaveCount(56)
    // Respons basi: muat-lebih ditunda 800ms, lalu create me-reset daftar.
    await page.route(
      (url) => url.pathname.includes('/customers') && url.searchParams.has('offset'),
      async (route) => {
        await new Promise(r => setTimeout(r, 800))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await page.getByLabel('Nama pelanggan').fill('Pelanggan Basi')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    await page.waitForTimeout(1500)
    await expect(listItems(page)).toHaveCount(50)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('pelanggan: kirim ganda sekali + hasil tak diketahui tanpa ulang otomatis', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const countBefore = (await listRows(page, 'customers')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await page.route(
      (url) => url.pathname.endsWith('/customers') && url.searchParams.toString() === '',
      async (route) => {
        if (route.request().method() === 'POST') {
          await new Promise(r => setTimeout(r, 600))
        }
        await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('Kirim Ganda')
    // Dua submit sinkron pada form (klik ganda Playwright tidak bisa
    // menembus tombol yang sudah disabled; dispatch menguji guard
    // inFlight yang sebenarnya, bukan hanya atribut disabled).
    await page.evaluate(() => {
      const form = document.querySelector('form')
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    const afterDouble = (await listRows(page, 'customers')).length
    expect(afterDouble).toBe(countBefore + 1)
    // Hasil tak diketahui: POST dibatalkan di jaringan.
    await page.route(
      (url) => url.pathname.endsWith('/customers'),
      async (route) => {
        if (route.request().method() === 'POST') await route.abort()
        else await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('Tak Diketahui')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByLabel('Nama pelanggan')).toHaveValue('Tak Diketahui')
    await page.waitForTimeout(1000)
    expect((await listRows(page, 'customers')).length).toBe(afterDouble)
    await page.getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(50)
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect((await listRows(page, 'customers')).length).toBe(afterDouble + 1)
  })

  test('navigasi workspace 360 membungkus + lanjut login rute baru + next ditolak', async ({ page }) => {
    await page.setViewportSize({ width: 360, height: 640 })
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(page.getByRole('navigation', { name: 'Navigasi workspace' })).toBeVisible()
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360)
    await page.getByRole('link', { name: 'Barang' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/catalog`)
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(360)
    await page.reload()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/catalog`)
    // 401 menghapus sesi dan minta masuk lewat rute baru yang sama.
    psql(`DELETE FROM sessions WHERE subject_id = (SELECT id FROM identity_subjects WHERE email = '${ownerEmail}')`)
    await page.getByRole('link', { name: 'Pelanggan' }).click()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsA}/customers`)
    // Tujuan next yang ditolak jatuh ke /workspaces/new.
    await uiLogout(page)
    await page.goto(`/login?next=${encodeURIComponent('https://evil.example/x')}`)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
    await page.setViewportSize({ width: 1440, height: 900 })
  })

  test('pelanggan R1: tulis-terkomit/respons-hilang — Simpan/Enter/submit biasa tidak kirim ulang', async ({ page, request }) => {
    const before = (await listRows(page, 'customers')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/customers'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          if (postAttempts === 1) {
            // Tulis sungguhan sampai ke server, lalu responsnya hilang.
            await route.fetch()
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('Tulis Hilang R1')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    // R2: peringatan menjelaskan daftar sebagian bisa menyembunyikan baris tersimpan.
    await expect(page.getByRole('alert')).toContainText('Daftar yang tampil mungkin hanya sebagian; data yang belum terlihat tetap mungkin sudah tersimpan.')
    await expect(page.getByLabel('Nama pelanggan')).toHaveValue('Tulis Hilang R1')
    // Tulis pertama SUDAH terkomit di server meski responsnya hilang.
    expect((await listRows(page, 'customers')).length).toBe(before + 1)
    expect(await page.getByRole('button', { name: 'Simpan pelanggan' }).isDisabled()).toBe(true)
    // Enter di input: submit implisit tetap diblokir kunci kirim ulang.
    await page.getByLabel('Nama pelanggan').focus()
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    // Submit form langsung (bypass tombol disabled): juga diblokir.
    await page.evaluate(() => {
      document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    expect((await listRows(page, 'customers')).length).toBe(before + 1)
    // GET-only refresh membuktikan baris ada tanpa POST baru.
    await page.getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(50)
    expect(postAttempts).toBe(1)
    // Hanya aksi berlabel duplikat yang boleh mengirim lagi.
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    expect((await listRows(page, 'customers')).length).toBe(before + 2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('katalog R1: tulis-terkomit/respons-hilang — kirim ulang sadar berujung 409, bukan sukses palsu', async ({ page, request }) => {
    const before = (await listRows(page, 'catalog')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          if (postAttempts === 1) {
            await route.fetch()
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Kode barang (SKU)').fill('HILANG-1')
    await page.getByLabel('Nama barang').fill('Barang Hilang')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    // R2: peringatan menjelaskan daftar sebagian bisa menyembunyikan baris tersimpan.
    await expect(page.getByRole('alert')).toContainText('Daftar yang tampil mungkin hanya sebagian; data yang belum terlihat tetap mungkin sudah tersimpan.')
    expect((await listRows(page, 'catalog')).length).toBe(before + 1)
    expect(await page.getByRole('button', { name: 'Simpan barang' }).isDisabled()).toBe(true)
    await page.getByLabel('Kode barang (SKU)').focus()
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    await page.evaluate(() => {
      document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    // Kirim lagi sadar: SKU sudah terpakai oleh tulis pertama -> 409.
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.locator('#item-sku-error')).toContainText('sku sudah dipakai di workspace ini')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('item-sku')
    expect(postAttempts).toBe(2)
    expect((await listRows(page, 'catalog')).length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('pelanggan R1: 200/202 dan 201 cacat bukan simpan', async ({ page, request }) => {
    const before = (await listRows(page, 'customers')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    const validRow = {
      id: '00000000-0000-4000-8000-000000000001', workspace_id: wsA, type: 'customer',
      display_name: 'Status Dua Ratus', created_at: '2026-11-17T00:00:00Z', updated_at: '2026-11-17T00:00:00Z'
    }
    // Rencana respons mock per POST: 200 baris valid, 202 baris valid,
    // 201 tanpa baris, 201 workspace salah — semuanya harus berakhir di
    // "hasil tidak diketahui", bukan "tersimpan".
    const planned: Array<{ status: number, body: unknown }> = [
      { status: 200, body: validRow },
      { status: 202, body: validRow },
      { status: 201, body: {} },
      { status: 201, body: { ...validRow, workspace_id: '00000000-0000-4000-8000-000000000099' } }
    ]
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/customers'),
      async (route) => {
        if (route.request().method() === 'POST') {
          const plan = planned[postAttempts] ?? null
          postAttempts++
          if (plan) {
            await route.fulfill({ status: plan.status, contentType: 'application/json', body: JSON.stringify(plan.body) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('Status Dua Ratus')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByLabel('Nama pelanggan')).toHaveValue('Status Dua Ratus')
    expect((await listRows(page, 'customers')).length).toBe(before)
    for (let i = 0; i < 3; i++) {
      await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
      await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
      expect((await listRows(page, 'customers')).length).toBe(before)
    }
    expect(postAttempts).toBe(4)
    await page.unrouteAll({ behavior: 'wait' })
    // Tanpa mock: kirim lagi sungguhan -> 201 nyata dengan baris ternormalisasi.
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect((await listRows(page, 'customers')).length).toBe(before + 1)
  })

  test('katalog R1: 200/202 dan 201 cacat bukan simpan', async ({ page, request }) => {
    const before = (await listRows(page, 'catalog')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    const validRow = {
      id: '00000000-0000-4000-8000-000000000002', workspace_id: wsA, sku: 'ST-200',
      name: 'Barang Status', unit: 'pcs', status: 'active', revision: 0,
      created_at: '2026-11-17T00:00:00Z', updated_at: '2026-11-17T00:00:00Z'
    }
    const planned: Array<{ status: number, body: unknown }> = [
      { status: 200, body: validRow },
      { status: 202, body: validRow },
      { status: 201, body: {} },
      { status: 201, body: { ...validRow, workspace_id: '00000000-0000-4000-8000-000000000099' } }
    ]
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'POST') {
          const plan = planned[postAttempts] ?? null
          postAttempts++
          if (plan) {
            await route.fulfill({ status: plan.status, contentType: 'application/json', body: JSON.stringify(plan.body) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Kode barang (SKU)').fill('ST-200')
    await page.getByLabel('Nama barang').fill('Barang Status')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    expect((await listRows(page, 'catalog')).length).toBe(before)
    for (let i = 0; i < 3; i++) {
      await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
      await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
      expect((await listRows(page, 'catalog')).length).toBe(before)
    }
    expect(postAttempts).toBe(4)
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    expect((await listRows(page, 'catalog')).length).toBe(before + 1)
  })

  test('pelanggan/katalog R1: trim Go — U+0085 dipangkas, U+FEFF dipertahankan', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    // Nama 200 rune dibungkus U+0085: klien harus menerima (Go memangkasnya).
    await page.getByLabel('Nama pelanggan').fill('\u0085' + 'é'.repeat(200) + '\u0085')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    const names = await listCustomerNames(page, wsA)
    expect(names).toContain('é'.repeat(200))
    expect(names.some(n => n.includes('\u0085'))).toBe(false)
    // U+FEFF di awal nama: Go TIDAK memangkasnya — identitas dipertahankan.
    await page.getByLabel('Nama pelanggan').fill('\uFEFFNama FEFF R1')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect(await listCustomerNames(page, wsA)).toContain('\uFEFFNama FEFF R1')
    // Katalog: SKU dibungkus U+0085 diterima; SKU ber-FEFF berbeda identitas.
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await page.getByLabel('Kode barang (SKU)').fill('\u0085U85-1\u0085')
    await page.getByLabel('Nama barang').fill('Barang U85')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    expect(await listItemSkus(page, wsA)).toContain('U85-1')
    await page.getByLabel('Kode barang (SKU)').fill('\uFEFFFEFF-1')
    await page.getByLabel('Nama barang').fill('Barang FEFF')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    expect(await listItemSkus(page, wsA)).toContain('\uFEFFFEFF-1')
    await page.getByLabel('Kode barang (SKU)').fill('FEFF-1')
    await page.getByLabel('Nama barang').fill('Barang Tanpa FEFF')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    const skus = await listItemSkus(page, wsA)
    expect(skus).toContain('FEFF-1')
    expect(skus).toContain('\uFEFFFEFF-1')
  })

  test('pelanggan R1: POST basi 401 tidak bersihkan sesi halaman baru', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await page.route(
      (url) => url.pathname.endsWith('/customers'),
      async (route) => {
        if (route.request().method() === 'POST') {
          await new Promise(r => setTimeout(r, 2500))
          await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'sesi tidak valid' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('POST Basi R1')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    // Pindah halaman selagi POST ditahan; POST dilepas sebagai 401 sesudahnya.
    await page.getByRole('link', { name: 'Buat workspace' }).click()
    await expect(page).toHaveURL('/workspaces/new')
    await page.waitForTimeout(3000)
    // Halaman baru tidak boleh dilempar ke login dan sesi harus tetap hidup.
    await expect(page).toHaveURL('/workspaces/new')
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.getByLabel('Nama workspace').fill(`Toko Basi ${RUN}`)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/[0-9a-f-]{36}\/members/)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('katalog R1: POST basi 401 tidak ganggu sesi login ulang', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await page.route(
      (url) => url.pathname.endsWith('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'POST') {
          await new Promise(r => setTimeout(r, 5000))
          await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'sesi tidak valid' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Kode barang (SKU)').fill('BASI-1')
    await page.getByLabel('Nama barang').fill('Barang Basi')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await page.getByRole('button', { name: 'Keluar' }).click()
    await expect(page).toHaveURL(/\/login/)
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await expect(page.getByRole('heading', { name: 'Barang', exact: true })).toBeVisible()
    // Tunggu pelepasan 401 basi, lalu pastikan tidak ada redirect ke login.
    await page.waitForTimeout(3000)
    await expect(page).toHaveURL(`/workspaces/${wsA}/catalog`)
    await expect(page.getByRole('heading', { name: 'Barang', exact: true })).toBeVisible()
    await expect(listItems(page).first()).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('pelanggan R1: GET offset basi tidak lumpuhkan paginasi sesudah create', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const before = (await listRows(page, 'customers')).length
    if (before < 51) await seedCustomers(request, wsA, token, 51 - before, 'R1 Muat')
    const total = (await listRows(page, 'customers')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    let releaseOldGet!: () => void
    const held = new Promise<void>((resolve) => { releaseOldGet = resolve })
    await page.route(
      (url) => url.pathname.endsWith('/customers') && url.searchParams.has('offset'),
      async (route) => {
        await held
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(page.getByRole('button', { name: 'Memuat…' })).toBeVisible()
    await page.getByLabel('Nama pelanggan').fill('Paginasi R1')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    // Refresh halaman 1 selesai: paginasi baru harus bisa dipakai.
    await expect(listItems(page)).toHaveCount(50)
    const moreBtn = page.getByRole('button', { name: 'Muat lebih' })
    await expect(moreBtn).toBeVisible()
    await expect(moreBtn).toBeEnabled()
    // Lepas GET offset lama: baris basi tidak boleh menempel.
    releaseOldGet()
    await page.waitForTimeout(1000)
    await expect(listItems(page)).toHaveCount(50)
    await expect(moreBtn).toBeEnabled()
    await moreBtn.click()
    await expect(listItems(page)).toHaveCount(total + 1)
    const ids = await listRows(page, 'customers')
    expect(ids.length).toBe(total + 1)
    expect(new Set(ids).size).toBe(total + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('katalog R1: GET offset basi tidak lumpuhkan paginasi sesudah create', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const before = (await listRows(page, 'catalog')).length
    if (before < 51) await seedItems(request, wsA, token, 51 - before, 'R1MUAT')
    const total = (await listRows(page, 'catalog')).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    let releaseOldGet!: () => void
    const held = new Promise<void>((resolve) => { releaseOldGet = resolve })
    await page.route(
      (url) => url.pathname.endsWith('/catalog/items') && url.searchParams.has('offset'),
      async (route) => {
        await held
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(page.getByRole('button', { name: 'Memuat…' })).toBeVisible()
    await page.getByLabel('Kode barang (SKU)').fill('R1PG-1')
    await page.getByLabel('Nama barang').fill('Barang Paginasi R1')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    await expect(listItems(page)).toHaveCount(50)
    const moreBtn = page.getByRole('button', { name: 'Muat lebih' })
    await expect(moreBtn).toBeVisible()
    await expect(moreBtn).toBeEnabled()
    releaseOldGet()
    await page.waitForTimeout(1000)
    await expect(listItems(page)).toHaveCount(50)
    await expect(moreBtn).toBeEnabled()
    await moreBtn.click()
    await expect(listItems(page)).toHaveCount(total + 1)
    const ids = await listRows(page, 'catalog')
    expect(ids.length).toBe(total + 1)
    expect(new Set(ids).size).toBe(total + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('pelanggan R1: refresh gagal sesudah 201 — konfirmasi tetap, retry GET-only', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const wsRes = await request.post('/api/workspaces', {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: `Toko Segar ${RUN}` }
    })
    expect(wsRes.status()).toBe(201)
    wsFresh = ((await wsRes.json()) as { id: string }).id
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsFresh}/customers`, ownerEmail)
    await expect(page.getByText('Belum ada pelanggan.')).toBeVisible()
    let postAttempts = 0
    let getAborted = false
    await page.route(
      (url) => url.pathname.endsWith('/customers'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          await route.continue()
          return
        }
        if (!getAborted) {
          getAborted = true
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Nama pelanggan').fill('Retry Satu')
    await page.getByRole('button', { name: 'Simpan pelanggan' }).click()
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    // Refresh gagal: konfirmasi tersimpan tetap, daftar tidak diklaim kosong.
    await expect(page.getByRole('alert')).toContainText('daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada pelanggan.')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    // Retry GET-only memulihkan daftar + kursor tanpa POST kedua.
    await page.locator('section[aria-labelledby="list-heading"] button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('Retry Satu')
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Pelanggan tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    expect(await listCustomerNames(page, wsFresh)).toContain('Retry Satu')
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('katalog R1: refresh gagal sesudah 201 — konfirmasi tetap, retry GET-only', async ({ page, request }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsFresh}/catalog`, ownerEmail)
    await expect(page.getByText('Belum ada barang.')).toBeVisible()
    let postAttempts = 0
    let getAborted = false
    await page.route(
      (url) => url.pathname.endsWith('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          await route.continue()
          return
        }
        if (!getAborted) {
          getAborted = true
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Kode barang (SKU)').fill('RTY-1')
    await page.getByLabel('Nama barang').fill('Barang Retry')
    await page.getByLabel('Satuan').fill('pcs')
    await page.getByRole('button', { name: 'Simpan barang' }).click()
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada barang.')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    await page.locator('section[aria-labelledby="list-heading"] button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('RTY-1 — Barang Retry (pcs)')
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Barang tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    expect(await listItemSkus(page, wsFresh)).toContain('RTY-1')
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('navigasi workspace R2: tautan 44x44, fokus terlihat, bungkus di 360/768/1440', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    for (const width of [360, 768, 1440]) {
      await page.setViewportSize({ width, height: 900 })
      for (const url of [`/workspaces/${wsA}/customers`, `/workspaces/${wsA}/catalog`, `/workspaces/${wsA}/members`, `/workspaces/${wsA}/modules`, `/workspaces/${wsA}/assets`]) {
        await gotoAuthed(page, url, ownerEmail)
        const nav = page.getByRole('navigation', { name: 'Navigasi workspace' })
        await expect(nav).toBeVisible()
        for (const name of ['Anggota', 'Fitur', 'Pelanggan', 'Barang', 'Unit']) {
          const box = await nav.getByRole('link', { name, exact: true }).boundingBox()
          expect(box, `tautan ${name} di ${width}px`).not.toBeNull()
          // R2: lebar DAN tinggi minimal 44px (Fitur sempat 29,75px lebar).
          expect(box!.width).toBeGreaterThanOrEqual(44)
          expect(box!.height).toBeGreaterThanOrEqual(44)
        }
        expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(width)
      }
    }
    // Fokus keyboard terlihat: dari tombol Keluar (tepat sebelum nav workspace
    // dalam urutan Tab), satu Tab nyata harus mendarat di Anggota.
    await page.setViewportSize({ width: 360, height: 640 })
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    const workspaceNav = page.getByRole('navigation', { name: 'Navigasi workspace' })
    await expect(workspaceNav).toBeVisible()
    const anggota = workspaceNav.getByRole('link', { name: 'Anggota', exact: true })
    await expect(anggota).toBeVisible()
    const keluar = page.getByRole('button', { name: 'Keluar', exact: true })
    await keluar.focus()
    await expect(keluar).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(anggota).toBeFocused()
    const outline = await page.evaluate(() => {
      const cs = getComputedStyle(document.activeElement as HTMLElement)
      return { width: cs.outlineWidth, style: cs.outlineStyle }
    })
    expect(outline.width).toBe('2px')
    expect(outline.style).not.toBe('none')
    await page.setViewportSize({ width: 1440, height: 900 })
  })

  test('screenshot f2-ui-01: enam utama + delapan tambahan + dua nav + manifest', async ({ page }) => {
    mkdirSync(SHOTS_UI01, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      // fullPage: daftar panjang/formulir (satuan + Simpan) harus terlihat
      // penuh di 360 — tangkapan viewport saja memotong wilayah itu (R1-9).
      await page.screenshot({ path: path.join(SHOTS_UI01, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI01, name))).digest('hex')
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsA, token, 3, 'SHOT')
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogin(page, ownerEmail)
      await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
      await expect(listItems(page).first()).toBeVisible()
      await shot('01-customers', 'populated-live', 'live', size.width)
      await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
      await expect(listItems(page).first()).toBeVisible()
      await shot('02-catalog', 'populated-live', 'live', size.width)
    }
    await page.setViewportSize({ width: 360, height: 640 })
    // Kosong: workspace milik pemilik ketiga yang dibuat di tes ini sendiri
    // (belum ada data master). Sengaja tidak memakai owner2/wsShot dari tes
    // screenshot lama supaya tes ini mandiri saat tes lama dikecualikan.
    await uiRegister(page, owner3Email)
    await uiLogin(page, owner3Email)
    const wsEmpty = await uiCreateWorkspace(page, `Toko Kosong ${RUN}`, owner3Email)
    await gotoAuthed(page, `/workspaces/${wsEmpty}/customers`, owner3Email)
    await expect(page.getByText('Belum ada pelanggan.')).toBeVisible()
    await shot('03-customers-empty', 'empty-live', 'live', 360)
    await gotoAuthed(page, `/workspaces/${wsEmpty}/catalog`, owner3Email)
    await expect(page.getByText('Belum ada barang.')).toBeVisible()
    await shot('04-catalog-empty', 'empty-live', 'live', 360)
    // Tanpa izin: bukan anggota.
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('05-customers-noperm', 'no-permission-live', 'live', 360)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('06-catalog-noperm', 'no-permission-live', 'live', 360)
    // Galat muat (rute dibatalkan = mocked) untuk KEDUA halaman.
    // Predikat dibatasi ke /api/...: navigasi dokumen ke halaman yang sama
    // tidak boleh ikut dibatalkan/ditunda oleh route ini.
    await uiLogin(page, ownerEmail)
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.includes('/customers') || url.pathname.includes('/catalog')),
      async (route) => { await route.abort() }
    )
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    // Route dibatalkan -> load() menampilkan pesan ApiError 502.
    await expect(page.getByRole('alert')).toContainText('Layanan tidak tersedia')
    await shot('07-customers-error', 'error-mocked', 'mocked-route', 360)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await expect(page.getByRole('alert')).toContainText('Layanan tidak tersedia')
    await shot('08-catalog-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Muat lambat (mocked) untuk KEDUA halaman; tunda 2500ms supaya
    // 'Memuat…' masih terlihat saat expect berjalan (fullPage butuh waktu
    // lebih lama daripada tangkapan viewport).
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.includes('/customers') || url.pathname.includes('/catalog')),
      async (route) => {
        await new Promise(r => setTimeout(r, 2500))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsA}/customers`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('09-customers-loading', 'loading-mocked', 'mocked-route', 360)
    await gotoAuthed(page, `/workspaces/${wsA}/catalog`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('10-catalog-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    await expect(listItems(page).first()).toBeVisible()
    // Navigasi baru di members/modules 360.
    await gotoAuthed(page, `/workspaces/${wsA}/members`, ownerEmail)
    await expect(page.getByRole('navigation', { name: 'Navigasi workspace' })).toBeVisible()
    await shot('11-members-nav', 'nav-live', 'live', 360)
    await gotoAuthed(page, `/workspaces/${wsA}/modules`, ownerEmail)
    await expect(page.getByRole('navigation', { name: 'Navigasi workspace' })).toBeVisible()
    await shot('12-modules-nav', 'nav-live', 'live', 360)
    const withSums = manifest.map(m => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI01, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-01', files: withSums }, null, 2) + '\n')
    expect(withSums.length).toBe(16)
  })

  test('aset: daftar kosong, buat 201, tampil + persist', async ({ page }) => {
    wsAssets = await uiCreateWorkspace(page, `Toko Unit ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsAssets, token, 1, 'AST')
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(page.getByRole('heading', { name: 'Unit', exact: true })).toBeVisible()
    await expect(page.getByText('Belum ada unit.')).toBeVisible()
    await page.getByLabel('Barang').selectOption(item.id)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item.id)
    await page.getByLabel('Serial').fill('SN-001')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('saved-heading')
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('SN-001')
    await expect(listItems(page).first()).toContainText('AST-1 — Barang AST 1')
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page)).toHaveCount(1)
    expect((await listAssetRows(page, wsAssets)).length).toBe(1)
  })

  test('aset: validasi 400 per-field + rune Unicode', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.locator('#asset-item-error')).toContainText('Pilih barang dulu')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('asset-item')
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.locator('#asset-serial-error')).toContainText('serial wajib diisi')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('asset-serial')
    await page.getByLabel('Serial').fill('😀'.repeat(65))
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.locator('#asset-serial-error')).toContainText('maksimal 64 karakter')
    // 64 emoji = 64 rune (Go) meski 128 unit UTF-16 JS: harus 201.
    await page.getByLabel('Serial').fill('😀'.repeat(64))
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    // Sesudah 201 serial dikosongkan, pilihan barang dipertahankan (daftar
    // banyak unit untuk barang yang sama adalah alur yang wajar).
    expect(await page.getByLabel('Serial').inputValue()).toBe('')
    expect(await page.getByLabel('Barang').inputValue()).toBe(item.id)
    // Trim Go: spasi + U+0085 di ujung dipangkas, U+FEFF dipertahankan.
    await page.getByLabel('Serial').fill('  SN-0085\u0085')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect(listItems(page).filter({ hasText: 'SN-0085' })).toHaveCount(1)
    await page.getByLabel('Serial').fill('\uFEFFSN-FEFF')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    const rows = await listAssetRows(page, wsAssets)
    expect(rows.some(r => r.serial === '\uFEFFSN-FEFF'), 'U+FEFF awal harus dipertahankan apa adanya').toBe(true)
    expect(rows.some(r => r.serial === 'SN-0085'), 'U+0085 akhir harus dipangkas seperti Go TrimSpace').toBe(true)
  })

  test('aset: serial duplikat 409 case-sensitive + serial sama di workspace lain boleh', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    const before = (await listAssetRows(page, wsAssets)).length
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('DUP-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await page.getByLabel('Serial').fill('DUP-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.locator('#asset-serial-error')).toContainText('serial sudah dipakai')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('asset-serial')
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    // Huruf besar/kecil dibedakan: 'dup-1' bukan duplikat 'DUP-1'.
    await page.getByLabel('Serial').fill('dup-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 2)
    // Serial yang sama di workspace lain boleh.
    wsAssetsB = await uiCreateWorkspace(page, `Toko Unit B ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsAssetsB, token, 1, 'BBB')
    const itemB = await catalogItemBySku(page, wsAssetsB, 'BBB-1')
    await gotoAuthed(page, `/workspaces/${wsAssetsB}/assets`, ownerEmail)
    await page.getByLabel('Barang').selectOption(itemB.id)
    await page.getByLabel('Serial').fill('DUP-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect((await listAssetRows(page, wsAssetsB)).length).toBe(1)
  })

  test('aset: kasir baca 200 + create 403, nonanggota 403', async ({ page }) => {
    const code = await uiInvite(page, wsAssets, staff2Email, 'cashier', ownerEmail)
    await uiRegister(page, staff2Email)
    await uiLogin(page, staff2Email)
    await gotoAuthed(page, `/invites/accept?workspace_id=${wsAssets}`, staff2Email)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/members`)
    const before = (await listAssetRows(page, wsAssets)).length
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, staff2Email)
    await expect(page.getByRole('heading', { name: 'Unit', exact: true })).toBeVisible()
    await expect(page.getByLabel('Barang')).toBeVisible()
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('KASIR-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('alert').first()).toContainText('tidak punya izin mengelola data usaha')
    await expect(page).toHaveURL(new RegExp(`/workspaces/${wsAssets}/assets$`))
    expect((await listAssetRows(page, wsAssets)).length).toBe(before)
    await uiLogout(page)
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
  })

  test('aset: item asing 404 + tidak aktif 409 dibedakan, tanpa bocor tenant', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    const before = (await listAssetRows(page, wsAssets)).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    // 404 mock: item asing/hilang — isian dipertahankan, tanpa bocor tenant.
    await page.route(
      (url) => url.pathname.endsWith('/assets'),
      async (route) => {
        if (route.request().method() === 'POST') {
          await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'barang tidak ditemukan' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('ASING-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('alert')).toContainText('barang tidak ditemukan')
    await expect(page.getByLabel('Serial')).toHaveValue('ASING-1')
    expect(await page.getByLabel('Barang').inputValue()).toBe(item.id)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toHaveCount(0)
    expect((await listAssetRows(page, wsAssets)).length).toBe(before)
    await page.unrouteAll({ behavior: 'wait' })
    // Tidak aktif: pilih selagi aktif, ubah status via SQL khusus tes,
    // lalu simpan — server 409 'barang tidak aktif' (galat pilihan barang,
    // bukan konflik serial).
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsAssets, token, 1, 'ARS')
    const ars = await catalogItemBySku(page, wsAssets, 'ARS-1')
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await page.getByLabel('Barang').selectOption(ars.id)
    await page.getByLabel('Serial').fill('ARS-UNIT-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${wsAssets}' AND sku='ARS-1'`)
    await page.getByLabel('Serial').fill('ARS-UNIT-2')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.locator('#asset-item-error')).toContainText('barang tidak aktif')
    await expect(page.getByLabel('Serial')).toHaveValue('ARS-UNIT-2')
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    // Sesudah reload: baris lama berlabel arsip, picker tak tawarkan ARS-1.
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).filter({ hasText: 'ARS-UNIT-1' }).first()).toContainText('(arsip)')
    const options = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(options.some(t => t.includes('ARS-1')), 'item arsip tidak boleh ditawarkan untuk create').toBe(false)
  })

  test('aset: 51 unit + buat + muat semua tanpa hilang/duplikat', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const before = (await listAssetRows(page, wsAssets)).length
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await seedAssets(request, wsAssets, token, item.id, 51, 'MUAT')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    await expect(page.getByRole('button', { name: 'Muat lebih' })).toBeVisible()
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('UNIT-52')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    // Sesudah simpan daftar di-reset ke halaman 1 (50 baris), bukan
    // ditempeli baris baru pada prefix lama.
    await expect(listItems(page)).toHaveCount(50)
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(listItems(page)).toHaveCount(before + 52)
    const ids = (await listAssetRows(page, wsAssets)).map(r => r.id)
    expect(ids.length).toBe(before + 52)
    expect(new Set(ids).size).toBe(before + 52)
  })

  test('aset: katalog 201 barang — halaman 2/3, pilihan stabil, offset mentah', async ({ page }) => {
    wsCat = await uiCreateWorkspace(page, `Toko Katalog ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsCat, token, 201, 'KAT')
    // Satu item halaman pertama diarsipkan via SQL khusus tes: offset
    // tetap maju menurut jumlah mentah (100), pilihan aktif berkurang.
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${wsCat}' AND sku='KAT-50'`)
    let catGets = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.includes('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'GET') catGets++
        await route.continue()
      }
    )
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsCat}/assets`, ownerEmail)
    // Muat awal SATU GET (100 baris): 99 pilihan aktif + placeholder.
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(100)
    expect(catGets).toBe(1)
    await expect(page.getByText('barang dimuat sejauh ini')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(200)
    expect(catGets).toBe(2)
    // Pilihan halaman kedua dapat dipilih berdasarkan ID, tanpa ketik UUID.
    const item150 = await catalogItemBySku(page, wsCat, 'KAT-150')
    await page.getByLabel('Barang').selectOption(item150.id)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item150.id)
    // Muat halaman ketiga: pilihan tidak berganti diam-diam.
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(201)
    expect(catGets).toBe(3)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item150.id)
    const item201 = await catalogItemBySku(page, wsCat, 'KAT-201')
    const options = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(options.some(t => t.includes('KAT-201')), 'baris terakhir harus termuat sesudah halaman ketiga').toBe(true)
    await page.getByLabel('Serial').fill('KAT-150-UNIT')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect(listItems(page).first()).toContainText('KAT-150 — Barang KAT 150')
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: katalog gagal tidak sembunyikan daftar unit; label belum dimuat + retry', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.includes('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'GET') { await route.abort(); return }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    // getByText tanpa .first() melanggar strict mode: 50 baris daftar
    // semuanya memuat label fallback yang sama.
    await expect(page.getByText('Nama barang belum dimuat').first()).toBeVisible()
    const formSection = page.locator('section[aria-labelledby="add-heading"]')
    await expect(formSection).toContainText('Layanan tidak tersedia')
    await expect(page.getByLabel('Barang')).toBeDisabled()
    await page.unrouteAll({ behavior: 'wait' })
    // Retry katalog memakai offset yang sama — tanpa muat ulang daftar unit.
    await formSection.getByRole('button', { name: 'Coba lagi' }).click()
    await expect(page.getByLabel('Barang').locator('option').nth(1)).toContainText('AST-1 — Barang AST 1')
    await expect(page.getByText('Nama barang belum dimuat')).toHaveCount(0)
    await expect(listItems(page).first()).toContainText('AST-1 — Barang AST 1')
  })

  test('aset: tulis-terkomit/respons-hilang — Simpan/Enter/submit biasa tidak kirim ulang', async ({ page, request }) => {
    const before = (await listAssetRows(page, wsAssets)).length
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/assets'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          if (postAttempts === 1) {
            // Tulis sungguhan sampai ke server, lalu responsnya hilang.
            await route.fetch()
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('HILANG-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('Daftar yang tampil mungkin hanya sebagian; data yang belum terlihat tetap mungkin sudah tersimpan.')
    await expect(page.getByLabel('Serial')).toHaveValue('HILANG-1')
    expect(await page.getByLabel('Barang').inputValue()).toBe(item.id)
    // Tulis pertama SUDAH terkomit di server meski responsnya hilang.
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    expect(await page.getByRole('button', { name: 'Simpan unit' }).isDisabled()).toBe(true)
    await page.getByLabel('Serial').focus()
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    await page.evaluate(() => {
      document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    // GET-only refresh membuktikan baris ada tanpa POST baru.
    await page.locator('section[aria-labelledby="add-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(50)
    expect(postAttempts).toBe(1)
    // Hanya aksi berlabel duplikat yang boleh mengirim lagi — dan 409
    // serial TIDAK mengonfirmasi/mengadopsi tulis pertama.
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.locator('#asset-serial-error')).toContainText('serial sudah dipakai')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('asset-serial')
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toHaveCount(0)
    expect(postAttempts).toBe(2)
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: 200/202 dan 201 cacat bukan simpan', async ({ page, request }) => {
    const before = (await listAssetRows(page, wsAssets)).length
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    const validRow = {
      id: '00000000-0000-4000-8000-000000000003', workspace_id: wsAssets, catalog_item_id: item.id,
      serial: 'Status Satu', created_at: '2026-11-17T00:00:00Z', updated_at: '2026-11-17T00:00:00Z'
    }
    // Rencana respons mock per POST: 200 baris valid, 202 baris valid,
    // 201 tanpa baris, 201 workspace salah, 201 item salah, 201 serial
    // salah — semuanya harus berakhir di "hasil tidak diketahui".
    const planned: Array<{ status: number, body: unknown }> = [
      { status: 200, body: validRow },
      { status: 202, body: validRow },
      { status: 201, body: {} },
      { status: 201, body: { ...validRow, workspace_id: '00000000-0000-4000-8000-000000000099' } },
      { status: 201, body: { ...validRow, catalog_item_id: '00000000-0000-4000-8000-000000000098' } },
      { status: 201, body: { ...validRow, serial: 'Status Lain' } }
    ]
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.endsWith('/assets'),
      async (route) => {
        if (route.request().method() === 'POST') {
          const plan = planned[postAttempts] ?? null
          postAttempts++
          if (plan) {
            await route.fulfill({ status: plan.status, contentType: 'application/json', body: JSON.stringify(plan.body) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('Status Satu')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByLabel('Serial')).toHaveValue('Status Satu')
    expect((await listAssetRows(page, wsAssets)).length).toBe(before)
    for (let i = 0; i < 5; i++) {
      await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
      await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
      expect((await listAssetRows(page, wsAssets)).length).toBe(before)
    }
    expect(postAttempts).toBe(6)
    await page.unrouteAll({ behavior: 'wait' })
    // Tanpa mock: kirim lagi sungguhan -> 201 nyata dengan baris ternormalisasi.
    await page.getByRole('button', { name: 'Kirim lagi (berisiko duplikat)' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
  })

  test('aset: kirim ganda sinkron sekali + POST basi 401 tidak bersihkan sesi halaman baru', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const countBefore = (await listAssetRows(page, wsAssets)).length
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await page.route(
      (url) => url.pathname.endsWith('/assets') && url.searchParams.toString() === '',
      async (route) => {
        if (route.request().method() === 'POST') {
          await new Promise(r => setTimeout(r, 600))
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('Ganda Satu')
    // Dua submit sinkron pada form (klik ganda Playwright tidak bisa
    // menembus tombol yang sudah disabled; dispatch menguji guard
    // inFlight yang sebenarnya, bukan hanya atribut disabled).
    await page.evaluate(() => {
      const form = document.querySelector('form')
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
      form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    expect((await listAssetRows(page, wsAssets)).length).toBe(countBefore + 1)
    // POST basi 401: ditahan, lalu dilepas sesudah pindah halaman.
    await page.route(
      (url) => url.pathname.endsWith('/assets'),
      async (route) => {
        if (route.request().method() === 'POST') {
          await new Promise(r => setTimeout(r, 2500))
          await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'sesi tidak valid' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Serial').fill('Basi Satu')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await page.getByRole('link', { name: 'Buat workspace' }).click()
    await expect(page).toHaveURL('/workspaces/new')
    await page.waitForTimeout(3000)
    await expect(page).toHaveURL('/workspaces/new')
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.getByLabel('Nama workspace').fill(`Toko Basi ${RUN}`)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/[0-9a-f-]{36}\/members/)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: refresh gagal sesudah 201 — konfirmasi tetap, retry GET-only', async ({ page, request }) => {
    const token = await apiToken(request, ownerEmail)
    const wsRes = await request.post('/api/workspaces', {
      headers: { Authorization: `Bearer ${token}` },
      data: { name: `Toko Unit Segar ${RUN}` }
    })
    expect(wsRes.status()).toBe(201)
    const wsSegar = ((await wsRes.json()) as { id: string }).id
    await seedItems(request, wsSegar, token, 1, 'SGR')
    const item = await catalogItemBySku(page, wsSegar, 'SGR-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsSegar}/assets`, ownerEmail)
    await expect(page.getByText('Belum ada unit.')).toBeVisible()
    let postAttempts = 0
    let getAborted = false
    await page.route(
      (url) => url.pathname.endsWith('/assets'),
      async (route) => {
        if (route.request().method() === 'POST') {
          postAttempts++
          await route.continue()
          return
        }
        if (!getAborted) {
          getAborted = true
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('Retry Satu')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    // Refresh gagal: konfirmasi tersimpan tetap, daftar tidak diklaim kosong.
    await expect(page.getByRole('alert')).toContainText('daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    // Retry GET-only memulihkan daftar + kursor tanpa POST kedua.
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('Retry Satu')
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(1)
    expect((await listAssetRows(page, wsSegar)).map(r => r.serial)).toContain('Retry Satu')
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: sesi habis + lanjut ke assets + next ditolak', async ({ page }) => {
    await gotoAuthed(page, `/workspaces/${wsAssets}/customers`, ownerEmail)
    await page.getByRole('link', { name: 'Unit' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    await page.reload()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    // 401 menghapus sesi dan minta masuk lewat rute assets yang sama.
    psql(`DELETE FROM sessions WHERE subject_id = (SELECT id FROM identity_subjects WHERE email = '${ownerEmail}')`)
    await page.getByRole('link', { name: 'Pelanggan' }).click()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/customers`)
    // Tujuan next yang ditolak jatuh ke /workspaces/new.
    await uiLogout(page)
    await page.goto(`/login?next=${encodeURIComponent('https://evil.example/x')}`)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
  })

  test('aset: muat-lebih daftar gagal pertahankan kursor', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    let aborted = false
    await page.route(
      (url) => url.pathname.endsWith('/assets') && url.searchParams.has('offset'),
      async (route) => {
        if (route.request().method() === 'GET' && !aborted) {
          aborted = true
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(page.getByRole('alert')).toContainText('Layanan tidak tersedia')
    await expect(listItems(page)).toHaveCount(50)
    // Retry memakai kursor yang sama — tanpa kehilangan baris.
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    const total = (await listAssetRows(page, wsAssets)).length
    await expect(listItems(page)).toHaveCount(total)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: respons muat-lebih basi tidak menempel sesudah create', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    const before = (await listAssetRows(page, wsAssets)).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page)).toHaveCount(50)
    await page.route(
      (url) => url.pathname.endsWith('/assets') && url.searchParams.has('offset'),
      async (route) => {
        if (route.request().method() === 'GET') {
          await new Promise(r => setTimeout(r, 2000))
        }
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    // Selagi muat-lebih lama ditahan, create mereset daftar ke halaman 1.
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('Basi Muat')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect(listItems(page)).toHaveCount(50)
    await page.waitForTimeout(2500)
    // Respons basi tidak boleh menempel ke prefix baru.
    await expect(listItems(page)).toHaveCount(50)
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset: respons katalog basi sesudah pindah halaman tidak gandakan pilihan', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.includes('/catalog/items'),
      async (route) => {
        if (route.request().method() === 'GET') {
          await new Promise(r => setTimeout(r, 2000))
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsCat}/assets`, ownerEmail)
    // Pindah halaman selagi GET katalog pertama ditahan (komponen lepas),
    // lalu kembali — peta katalog halaman baru tidak boleh digandakan.
    await page.getByRole('link', { name: 'Pelanggan' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsCat}/customers`)
    await page.getByRole('link', { name: 'Unit' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsCat}/assets`)
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(100)
    const texts = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(new Set(texts).size).toBe(texts.length)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: GET awal basi sesudah navigasi tidak mulai GET katalog + tidak sentuh sesi pengganti', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    let catalogGets = 0
    let releaseAssets!: () => void
    const assetsHeld = new Promise<void>((resolve) => { releaseAssets = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items')) {
          catalogGets++
          await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'sesi berakhir' }) })
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          await assetsHeld
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    // Navigasi SPA selagi GET awal ditahan (header selalu tampil).
    await page.getByRole('link', { name: 'Buat workspace' }).click()
    await expect(page).toHaveURL('/workspaces/new')
    releaseAssets()
    await page.waitForTimeout(1000)
    expect(catalogGets).toBe(0)
    await expect(page).toHaveURL('/workspaces/new')
    // Sesi pengganti masih hidup: header Keluar tampil, bukan /login.
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: GET awal basi sesudah keluar/masuk lagi + pindah workspace tidak sentuh sesi pengganti', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    let staleCatalogGets = 0
    let releaseAssets!: () => void
    const assetsHeld = new Promise<void>((resolve) => { releaseAssets = resolve })
    let heldOnce = false
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        const isAssetsGet = route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')
        if (isAssetsGet && u.pathname.includes(`/workspaces/${wsAssets}/`) && !heldOnce) {
          heldOnce = true
          await assetsHeld
          await route.continue()
          return
        }
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items') && u.pathname.includes(`/workspaces/${wsAssets}/`)) {
          staleCatalogGets++
          await route.fulfill({ status: 401, contentType: 'application/json', body: JSON.stringify({ error: 'sesi berakhir' }) })
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    // Keluar (header selalu tampil) lalu masuk lagi sebagai pemilik.
    await page.getByRole('button', { name: 'Keluar' }).click()
    await expect(page).toHaveURL(/\/login/)
    await uiLogin(page, ownerEmail)
    // Pindah workspace: muat halaman Unit workspace lain.
    await gotoAuthed(page, `/workspaces/${wsAssetsB}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    releaseAssets()
    await page.waitForTimeout(1000)
    expect(staleCatalogGets).toBe(0)
    await expect(page).toHaveURL(`/workspaces/${wsAssetsB}/assets`)
    await expect(listItems(page).first()).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: katalog halaman berikut 403 menyembunyikan daftar/formulir; respons muat-lebih basi tidak mengisi ulang', async ({ page }) => {
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsAssets, token, 101, 'R1C')
    await uiLogin(page, ownerEmail)
    let releaseMore!: () => void
    const moreHeld = new Promise<void>((resolve) => { releaseMore = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items') && u.searchParams.get('offset') === '100') {
          await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && u.searchParams.get('offset') === '50') {
          await moreHeld
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    // Muat-lebih daftar ditahan dulu, lalu katalog halaman berikut 403.
    // exact: "Muat lebih" adalah awalan "Muat lebih banyak barang".
    await page.getByRole('button', { name: 'Muat lebih', exact: true }).click()
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await expect(listItems(page)).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Tambah unit' })).toHaveCount(0)
    await expect(page.getByLabel('Serial')).toHaveCount(0)
    // Sesi dipertahankan: masih di /assets, bukan /login.
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    // Respons muat-lebih yang datang terlambat tidak boleh mengisi ulang.
    releaseMore()
    await page.waitForTimeout(1000)
    await expect(listItems(page)).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: halaman daftar berikut 403 menyembunyikan daftar/formulir', async ({ page }) => {
    await uiLogin(page, ownerEmail)
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && u.searchParams.get('offset') === '50') {
          await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    // exact: "Muat lebih" adalah awalan "Muat lebih banyak barang".
    await page.getByRole('button', { name: 'Muat lebih', exact: true }).click()
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await expect(listItems(page)).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Tambah unit' })).toHaveCount(0)
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: refresh daftar 403 sesudah 201 menyembunyikan daftar/formulir', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    const token = await apiToken(page.request, ownerEmail)
    const before = (await listAssetRows(page, wsAssets)).length
    await uiLogin(page, ownerEmail)
    let assetsGets = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          assetsGets++
          if (assetsGets > 1) {
            await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R1-REF403')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await expect(listItems(page)).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Tambah unit' })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toHaveCount(0)
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    expect((await listAssetRows(page, wsAssets)).length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: kasir 403 saat simpan mempertahankan isian/daftar/sesi', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, staff2Email)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, staff2Email)
    await expect(listItems(page).first()).toBeVisible()
    const rowsBefore = await listItems(page).count()
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R1-KASIR-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('alert')).toContainText('tidak punya izin mengelola data usaha')
    await expect(page.getByLabel('Serial')).toHaveValue('R1-KASIR-1')
    expect(await page.getByLabel('Barang').inputValue()).toBe(item.id)
    await expect(listItems(page)).toHaveCount(rowsBefore)
    await expect(page.getByRole('heading', { name: 'Tambah unit' })).toBeVisible()
    await expect(page).toHaveURL(`/workspaces/${wsAssets}/assets`)
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
  })

  test('aset R1: refresh sesudah 201 yang ditahan tidak menampilkan daftar kosong + tanpa POST ganda', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    let postCount = 0
    let releaseRefresh!: () => void
    const refreshHeld = new Promise<void>((resolve) => { releaseRefresh = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/assets')) {
          postCount++
          await route.continue()
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          await refreshHeld
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R1-REF-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    expect(postCount).toBe(1)
    releaseRefresh()
    // Refresh memuat halaman pertama (50 tertua); serial baru yang
    // termuda ada di halaman berikut — pulihnya daftar dibuktikan lewat
    // baris pertama yang tampil + API, bukan baris baru di halaman 1.
    await expect(listItems(page).first()).toBeVisible()
    expect((await listAssetRows(page, wsAssets)).map((r) => r.serial)).toContain('R1-REF-1')
    await expect(page.getByText('Memuat daftar…')).toHaveCount(0)
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    expect(postCount).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: refresh gagal tidak menampilkan daftar kosong; retry yang ditahan lalu berhasil memulihkan daftar', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    let postCount = 0
    let refreshGets = 0
    let releaseRetry!: () => void
    const retryHeld = new Promise<void>((resolve) => { releaseRetry = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/assets')) {
          postCount++
          await route.continue()
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          refreshGets++
          if (refreshGets === 1) {
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
            return
          }
          await retryHeld
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R1-REF-2')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    expect(postCount).toBe(1)
    // Retry GET-only yang ditahan: tetap tanpa daftar kosong.
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    releaseRetry()
    // Sama seperti tes sebelumnya: halaman pertama memuat 50 tertua.
    await expect(listItems(page).first()).toBeVisible()
    expect((await listAssetRows(page, wsAssets)).map((r) => r.serial)).toContain('R1-REF-2')
    await expect(page.getByRole('alert')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    expect(postCount).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R1: finally refresh basi tidak menghapus status muat milik refresh baru', async ({ page }) => {
    const item = await catalogItemBySku(page, wsAssets, 'AST-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsAssets}/assets`, ownerEmail)
    await expect(listItems(page).first()).toBeVisible()
    let postCount = 0
    const releases: Array<() => void> = []
    const held: Array<Promise<void>> = []
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/assets')) {
          postCount++
          if (postCount === 1) {
            // Tulis pertama sampai ke server, lalu responsnya hilang.
            await route.fetch()
            await route.abort()
            return
          }
          await route.continue()
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          const p = new Promise<void>((resolve) => { releases.push(resolve) })
          held.push(p)
          await p
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R1-STALE-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    // Refresh GET-only pertama ditahan.
    await page.locator('section[aria-labelledby="add-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    // Kiriman baru (serial berbeda) → 201 → refresh kedua, juga ditahan.
    await page.getByLabel('Serial').fill('R1-STALE-2')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Unit tersimpan' })).toBeVisible()
    await expect.poll(() => held.length).toBe(2)
    // Lepas refresh pertama (basi): status muat milik refresh kedua tetap.
    releases[0]()
    await page.waitForTimeout(800)
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    // Lepas refresh kedua: daftar pulih berisi kedua serial.
    releases[1]()
    await expect(listItems(page).first()).toBeVisible()
    const serials = (await listAssetRows(page, wsAssets)).map((r) => r.serial)
    expect(serials).toContain('R1-STALE-1')
    expect(serials).toContain('R1-STALE-2')
    await expect(page.getByText('Memuat daftar…')).toHaveCount(0)
    expect(postCount).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('aset R2: refresh gagal sesudah tulis tak diketahui tidak menampilkan daftar kosong; retry berhasil memulihkan baris', async ({ page }) => {
    // Workspace kosong milik pemilik + satu barang nyata (data sintetis).
    const token = await apiToken(page.request, ownerEmail)
    const wsEmpty = await uiCreateWorkspace(page, `Toko Kosong R2 ${RUN}`, ownerEmail)
    await seedItems(page.request, wsEmpty, token, 1, 'R2E')
    const item = await catalogItemBySku(page, wsEmpty, 'R2E-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsEmpty}/assets`, ownerEmail)
    // GET awal sukses dengan nol baris: pesan kosong netral tampil.
    await expect(page.getByText('Belum ada unit.')).toBeVisible()
    let postCount = 0
    let refreshGets = 0
    let releaseRetry!: () => void
    const retryHeld = new Promise<void>((resolve) => { releaseRetry = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/assets')) {
          postCount++
          if (postCount === 1) {
            // POST sampai ke API nyata lalu responsnya hilang (injeksi
            // kegagalan browser-mocked, bukan bukti database/RLS).
            await route.fetch()
            await route.abort()
            return
          }
          await route.continue()
          return
        }
        if (route.request().method() === 'GET' && u.pathname.endsWith('/assets') && !u.searchParams.has('offset')) {
          refreshGets++
          if (refreshGets === 1) {
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
            return
          }
          await retryHeld
          await route.continue()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Serial').fill('R2-EMPTY-1')
    await page.getByRole('button', { name: 'Simpan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    // Isian dipertahankan dan kirim biasa atas nilai yang sama terkunci.
    await expect(page.getByLabel('Serial')).toHaveValue('R2-EMPTY-1')
    await expect(page.getByRole('button', { name: 'Simpan unit' })).toBeDisabled()
    // Refresh GET-only gagal 500: galat tampil, tanpa klaim kosong.
    await page.locator('section[aria-labelledby="add-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByRole('alert')).toContainText('Daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    // Retry yang ditahan: tetap memuat, tanpa klaim kosong.
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    await expect(page.getByText('Belum ada unit.')).toHaveCount(0)
    releaseRetry()
    await expect(listItems(page).filter({ hasText: 'R2-EMPTY-1' })).toHaveCount(1)
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByRole('alert')).toHaveCount(0)
    expect((await listAssetRows(page, wsEmpty)).map((r) => r.serial)).toContain('R2-EMPTY-1')
    expect(postCount).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('screenshot f2-ui-02: tiga utama + kosong + tanpa-izin + galat + muat + picker + manifest', async ({ page }) => {
    mkdirSync(SHOTS_UI02, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI02, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI02, name))).digest('hex')
    const token = await apiToken(page.request, ownerEmail)
    const shotItem = await catalogItemBySku(page, wsA, 'SHOT-1')
    await seedAssets(page.request, wsA, token, shotItem.id, 3, 'SHOT-UNIT')
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogin(page, ownerEmail)
      await gotoAuthed(page, `/workspaces/${wsA}/assets`, ownerEmail)
      await expect(listItems(page).first()).toBeVisible()
      await shot('01-assets', 'populated-live', 'live', size.width)
    }
    await page.setViewportSize({ width: 360, height: 640 })
    await uiRegister(page, owner4Email)
    await uiLogin(page, owner4Email)
    const wsEmpty = await uiCreateWorkspace(page, `Toko Kosong Unit ${RUN}`, owner4Email)
    await gotoAuthed(page, `/workspaces/${wsEmpty}/assets`, owner4Email)
    await expect(page.getByText('Belum ada unit.')).toBeVisible()
    await shot('04-assets-empty', 'empty-live', 'live', 360)
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsA}/assets`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('05-assets-noperm', 'no-permission-live', 'live', 360)
    await uiLogin(page, ownerEmail)
    // Predikat dibatasi ke jalur data halaman Unit: POST login
    // (/api/auth/login) TIDAK boleh ikut dibatalkan — gotoAuthed masuk
    // lagi sesudah full reload, jadi login yang dibatalkan menggantungkan
    // navigasi sampai timeout (pelajaran yang sama dengan F2-UI-01).
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.includes('/assets') || url.pathname.includes('/catalog/items')),
      async (route) => { await route.abort() }
    )
    await gotoAuthed(page, `/workspaces/${wsA}/assets`, ownerEmail)
    await expect(page.getByText('Layanan tidak tersedia').first()).toBeVisible()
    await shot('06-assets-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.includes('/assets') || url.pathname.includes('/catalog/items')),
      async (route) => {
        await new Promise(r => setTimeout(r, 2500))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsA}/assets`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('07-assets-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    await expect(listItems(page).first()).toBeVisible()
    // Picker: item halaman kedua sesudah >100 barang + catatan muat sebagian.
    await gotoAuthed(page, `/workspaces/${wsCat}/assets`, ownerEmail)
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('barang dimuat sejauh ini')).toBeVisible()
    const item150 = await catalogItemBySku(page, wsCat, 'KAT-150')
    await page.getByLabel('Barang').selectOption(item150.id)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item150.id)
    await shot('08-assets-picker', 'picker-live', 'live', 360)
    const withSums = manifest.map(m => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI02, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-02', files: withSums }, null, 2) + '\n')
    expect(withSums.length).toBe(8)
  })
})
