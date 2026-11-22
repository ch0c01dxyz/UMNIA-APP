// Alur pemilik end-to-end di browser NYATA melawan web + business-api +
// PG native terisolasi (umnia_e2e). Langkah pengguna memakai UI +
// proxy same-origin; psql HANYA untuk setup/fault injection. Grant
// entitlement memakai CLI operator SUNGGUHAN (bukan INSERT sintetis).
// Rahasia (kata sandi, token, kode undangan, DSN operator) tidak pernah
// di-log; kode undangan hanya hidup di memori tes ini.
import { execFileSync } from 'node:child_process'
import { createHash, randomUUID } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'
import { expect, test, type APIRequestContext, type Page, type Route } from '@playwright/test'

const RUN = `${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`
const PASSWORD = 'E2eUiTest123'
const BOOTSTRAP_URL = process.env.E2E_BOOTSTRAP_URL ||
  'postgres://postgres:local-bootstrap-only@localhost:5432/umnia_e2e?sslmode=disable'
const SHOTS = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'owner-flow')
const SHOTS_UI01 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-01')
const SHOTS_UI02 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-02')
const SHOTS_UI03 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-03')
const SHOTS_UI04 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-04')
const SHOTS_UI05 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-05')
const SHOTS_UI06 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-06')
const SHOTS_UI07 = path.join(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', 'design', 'screenshots', 'f2-ui-07')

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
let wsPrices = ''
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

async function listAssetRows(page: Page, ws: string, email: string = ownerEmail): Promise<Array<{ id: string, catalog_item_id: string, serial: string }>> {
  const res = await page.request.get(`/api/workspaces/${ws}/assets?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: Array<{ id: string, catalog_item_id: string, serial: string }> }
  return body.items
}

// Cari satu item katalog berdasarkan SKU; halaman API dipaginasi 100
// baris, jadi telusuri offset 0/100/200 sampai halaman pendek.
async function catalogItemBySku(page: Page, ws: string, sku: string, email: string = ownerEmail): Promise<{ id: string, sku: string, name: string, status: string }> {
  const token = await apiToken(page.request, email)
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

interface PriceRow { id: string, workspace_id: string, catalog_item_id: string, price_list_id: string, client_request_id: string, valid_from: string, valid_to: string, amount_minor: string, currency: string }

async function listPriceRows(page: Page, ws: string, itemId: string, email: string = ownerEmail): Promise<PriceRow[]> {
  const res = await page.request.get(`/api/workspaces/${ws}/catalog/items/${itemId}/prices?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: PriceRow[] }
  return body.items
}

// Buat N tarif bulanan berurutan lewat API nyata, masing-masing dengan
// kunci baru; interval ke-i: 2040-MM-01 s.d. bulan berikut (tanpa tumpang).
async function seedPrices(req: APIRequestContext, ws: string, token: string, itemId: string, count: number, amountPrefix = 1000): Promise<void> {
  for (let i = 0; i < count; i++) {
    const y = 2040 + Math.floor(i / 12)
    const m = (i % 12) + 1
    const m2 = m === 12 ? 1 : m + 1
    const y2 = m === 12 ? y + 1 : y
    const from = `${y}-${String(m).padStart(2, '0')}-01T00:00:00Z`
    const to = `${y2}-${String(m2).padStart(2, '0')}-01T00:00:00Z`
    const res = await req.post(`/api/workspaces/${ws}/catalog/items/${itemId}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { valid_from: from, valid_to: to, amount_minor: String(amountPrefix + i), currency: 'IDR' }
    })
    expect(res.status(), `seed harga ${i}`).toBe(201)
  }
}

// R1: workspace + pemilik mandiri per tes regresi (tanpa wsPrices bersama)
// supaya tiap tes R1 bisa jalan sendiri lewat --grep.
async function r1Setup(page: Page, tag: string): Promise<{ email: string, ws: string, token: string }> {
  const email = `r1-${tag}-${RUN}@e2e.umnia.local`
  await uiRegister(page, email)
  const ws = await uiCreateWorkspace(page, `Toko R1 ${tag} ${RUN}`, email)
  const token = await apiToken(page.request, email)
  return { email, ws, token }
}

interface DraftRow { id: string, workspace_id: string, customer_id: string, catalog_item_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, created_at: string, updated_at: string }

async function listDraftRows(page: Page, ws: string, email: string = ownerEmail): Promise<DraftRow[]> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/contracts?limit=100`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  expect(res.status()).toBe(200)
  const body = await res.json() as { items: DraftRow[] }
  return body.items
}

// Buat N draf bulanan berurutan lewat API nyata, masing-masing dengan
// kunci baru; interval ke-i: 2040-MM-01 s.d. bulan berikut.
async function seedDrafts(req: APIRequestContext, ws: string, token: string, customerId: string, itemId: string, count: number): Promise<void> {
  for (let i = 0; i < count; i++) {
    const y = 2040 + Math.floor(i / 12)
    const m = (i % 12) + 1
    const m2 = m === 12 ? 1 : m + 1
    const y2 = m === 12 ? y + 1 : y
    const from = `${y}-${String(m).padStart(2, '0')}-01T00:00:00Z`
    const to = `${y2}-${String(m2).padStart(2, '0')}-01T00:00:00Z`
    const res = await req.post(`/api/workspaces/${ws}/rental/contracts`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
    })
    expect(res.status(), `seed draf ${i}`).toBe(201)
  }
}

// Cari satu pelanggan berdasarkan display_name; halaman API dipaginasi
// 100 baris, jadi telusuri offset 0/100/200 sampai halaman pendek.
async function customerByName(page: Page, ws: string, name: string, email: string = ownerEmail): Promise<{ id: string, display_name: string }> {
  const token = await apiToken(page.request, email)
  for (const offset of [0, 100, 200]) {
    const res = await page.request.get(`/api/workspaces/${ws}/customers?limit=100&offset=${offset}`, {
      headers: { Authorization: `Bearer ${token}` }
    })
    expect(res.status()).toBe(200)
    const body = await res.json() as { items: Array<{ id: string, display_name: string }> }
    const found = body.items.find(c => c.display_name === name)
    if (found) return found
    if (body.items.length < 100) break
  }
  throw new Error(`pelanggan ${name} tidak ditemukan di workspace ${ws}`)
}

// F2-UI-04: workspace + pemilik mandiri per tes draf (tanpa ws bersama)
// supaya tiap tes draf bisa jalan sendiri lewat --grep. Rental di-grant
// lewat CLI sungguhan lalu diaktifkan lewat API (alur aktivasi UI sendiri
// sudah tercakup tes 'grant operator lewat CLI lalu aktivasi 200').
async function draftSetup(page: Page, tag: string): Promise<{ email: string, ws: string, token: string }> {
  const email = `d4-${tag}-${RUN}@e2e.umnia.local`
  await uiRegister(page, email)
  const ws = await uiCreateWorkspace(page, `Toko D4 ${tag} ${RUN}`, email)
  grantRental(ws)
  const token = await apiToken(page.request, email)
  const act = await page.request.post(`/api/workspaces/${ws}/modules/rental.basic/activate`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { version: '0.1.0' }
  })
  expect(act.status()).toBe(200)
  return { email, ws, token }
}

interface QuoteRow { draft_id: string, catalog_item_id: string, price_version_id: string, valid_from: string, valid_to: string, recorded_at: string, amount_minor: string, currency: string, unit: string, units: number, total_minor: string, starts_at: string, ends_at: string }
interface FactRow { id: string, workspace_id: string, rental_contract_id: string, price_version_id: string, pricing_rule_version_id: string, catalog_item_id: string, customer_id: string, client_request_id: string, amount_minor: string, currency: string, units: number, total_minor: string, starts_at: string, ends_at: string, accepted_at: string, accepted_by: string }

async function getQuote(page: Page, ws: string, draftId: string, email: string = ownerEmail): Promise<{ status: number, body: any }> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/contracts/${draftId}/quote`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  return { status: res.status(), body: await res.json() }
}

async function getFact(page: Page, ws: string, draftId: string, email: string = ownerEmail): Promise<{ status: number, body: any }> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/contracts/${draftId}/agreed-price`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  return { status: res.status(), body: await res.json() }
}

async function acceptPrice(page: Page, ws: string, draftId: string, email: string, key: string, versionId: string, total: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/accept-price`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: { expected_price_version_id: versionId, expected_total_minor: total }
  })
  return { status: res.status(), body: await res.json() }
}

// Satu tarif lebar lewat API nyata (tanpa 85 panggilan seedPrices).
async function seedPriceOne(req: APIRequestContext, ws: string, token: string, itemId: string, from: string, to: string, amount: string, currency = 'IDR'): Promise<PriceRow> {
  const res = await req.post(`/api/workspaces/${ws}/catalog/items/${itemId}/prices`, {
    headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
    data: { valid_from: from, valid_to: to, amount_minor: amount, currency }
  })
  expect(res.status(), 'seed harga satu').toBe(201)
  return (await res.json()) as PriceRow
}

// Satu draf lewat API nyata; kembalikan baris yang dibuat.
async function seedDraftOne(req: APIRequestContext, ws: string, token: string, customerId: string, itemId: string, from: string, to: string): Promise<DraftRow> {
  const res = await req.post(`/api/workspaces/${ws}/rental/contracts`, {
    headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
    data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
  })
  expect(res.status(), 'seed draf satu').toBe(201)
  return (await res.json()) as DraftRow
}

// F2-UI-05: workspace + pemilik mandiri per tes harga (tanpa ws bersama)
// supaya tiap tes harga bisa jalan sendiri lewat --grep.
async function priceSetup(page: Page, tag: string): Promise<{ email: string, ws: string, token: string, customer: { id: string, display_name: string }, item: { id: string, sku: string, name: string, status: string } }> {
  const { email, ws, token } = await draftSetup(page, `hp-${tag}`)
  await seedCustomers(page.request, ws, token, 1, `HP${tag}`)
  await seedItems(page.request, ws, token, 1, `HP${tag}`)
  const customer = await customerByName(page, ws, `HP${tag} 1`, email)
  const item = await catalogItemBySku(page, ws, `HP${tag}-1`, email)
  return { email, ws, token, customer, item }
}

function countAudits(ws: string, action: string): number {
  return Number(psql(`SELECT count(*) FROM audit_events WHERE workspace_id='${ws}' AND action='${action}'`).trim())
}

// F2-UI-06: baris hold/custody + panggilan API nyata untuk tes serah terima.
interface HoldRow { id: string, workspace_id: string, draft_id: string, asset_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, expires_at: string, created_at: string, updated_at: string }
interface CustodyRow { id: string, workspace_id: string, rental_contract_id: string, rental_agreed_price_id: string, asset_id: string, hold_id: string, client_request_id: string, return_client_request_id: string | null, status: string, handed_out_at: string, returned_at: string | null, handed_out_by: string, returned_by: string | null, resolved_at: string | null, resolved_by: string | null }

async function getCustody(page: Page, ws: string, draftId: string, email: string = ownerEmail): Promise<{ status: number, body: any }> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/contracts/${draftId}/custody`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  return { status: res.status(), body: await res.json() }
}

async function getHolds(page: Page, ws: string, email: string = ownerEmail, query = ''): Promise<{ status: number, body: any }> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/holds${query}`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  return { status: res.status(), body: await res.json() }
}

async function createHold(page: Page, ws: string, draftId: string, email: string, key: string, assetId: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/holds`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: { asset_id: assetId }
  })
  return { status: res.status(), body: await res.json() }
}

async function handoverCustody(page: Page, ws: string, draftId: string, email: string, key: string, assetId: string, holdId: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/handover`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: { asset_id: assetId, hold_id: holdId }
  })
  return { status: res.status(), body: await res.json() }
}

async function returnCustody(page: Page, ws: string, draftId: string, email: string, key: string, outcome: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/return`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: { outcome }
  })
  return { status: res.status(), body: await res.json() }
}

// F2-UI-06: workspace + pemilik mandiri per tes custody (tanpa ws bersama)
// supaya tiap tes custody bisa jalan sendiri lewat --grep.
async function custodySetup(page: Page, tag: string): Promise<{ email: string, ws: string, token: string, customer: { id: string, display_name: string }, item: { id: string, sku: string, name: string, status: string }, asset: { id: string, catalog_item_id: string, serial: string } }> {
  const { email, ws, token } = await draftSetup(page, `cu-${tag}`)
  await seedCustomers(page.request, ws, token, 1, `CU${tag}`)
  await seedItems(page.request, ws, token, 1, `CU${tag}`)
  const customer = await customerByName(page, ws, `CU${tag} 1`, email)
  const item = await catalogItemBySku(page, ws, `CU${tag}-1`, email)
  await seedAssets(page.request, ws, token, item.id, 1, `CU${tag}`)
  const assets = await listAssetRows(page, ws, email)
  const asset = assets.find(a => a.catalog_item_id === item.id)
  if (!asset) throw new Error('aset custody tidak ditemukan sesudah seed')
  return { email, ws, token, customer, item, asset }
}

// Draf + harga disepakati siap serah terima (tarif 150000, draf 2 unit,
// total 300000 — contoh kerja yang sama dengan tes harga).
// F2-UI-06: jendela handover ketat server — cutoff harus di dalam
// [starts_at, ends_at). Tanggal relatif supaya tes tidak basi (draf 2047
// tidak akan pernah bisa diserahkan). Tarif 150000, draf 48 jam = 2 unit,
// total 300000 — sama seperti contoh harga.
function custodyWindow(): { priceFrom: string, priceTo: string, draftFrom: string, draftTo: string } {
  // Pola backend (seedCustodyRefs): tarif valid_from sedikit di masa depan
  // (tanpa backdate), draf mulai sesudah tarif berlaku, 48 jam tepat =
  // 2 unit. `now` ditangkap TEPAT sebelum seed harga — bukan di awal
  // setup — supaya valid_from belum lewat saat POST harga tiba.
  const now = Date.now()
  const hour = 3600000
  return {
    priceFrom: new Date(now + 5 * 1000).toISOString(),
    priceTo: new Date(now + 73 * hour).toISOString(),
    draftFrom: new Date(now + 15 * 1000).toISOString(),
    draftTo: new Date(now + 15 * 1000 + 48 * hour).toISOString()
  }
}

async function seedCustodyReady(page: Page, tag: string): Promise<{ email: string, ws: string, token: string, customer: { id: string, display_name: string }, item: { id: string, sku: string, name: string, status: string }, asset: { id: string, catalog_item_id: string, serial: string }, draft: DraftRow, fact: FactRow }> {
  const s = await custodySetup(page, tag)
  const w = custodyWindow()
  await seedPriceOne(page.request, s.ws, s.token, s.item.id, w.priceFrom, w.priceTo, '150000')
  const draft = await seedDraftOne(page.request, s.ws, s.token, s.customer.id, s.item.id, w.draftFrom, w.draftTo)
  const quote = await getQuote(page, s.ws, draft.id, s.email)
  expect(quote.status).toBe(200)
  const accepted = await acceptPrice(page, s.ws, draft.id, s.email, randomUUID(), (quote.body as QuoteRow).price_version_id, '300000')
  expect(accepted.status).toBe(201)
  const fact = accepted.body as FactRow
  // Tunggu jendela handover terbuka (cutoff server sudah di dalam
  // [draftFrom, draftTo)) sebelum tes memakai draf ini — pola waitUntil
  // backend, bukan sleep buta.
  const waitMs = new Date(w.draftFrom).getTime() - Date.now() + 500
  if (waitMs > 0) await new Promise(r => setTimeout(r, waitMs))
  return { ...s, draft, fact }
}

// F2-UI-07: baris pembayaran/pembalikan + panggilan API nyata untuk tes pembayaran.
interface PaymentProposalDTO { id: string, rental_contract_id: string, rental_agreed_price_id: string, amount_minor: string, currency: string, method: string, reference: string | null, proposed_by: string, proposed_at: string, client_request_id: string, status: string }
interface PaymentDTO { id: string, proposal_id: string, rental_contract_id: string, rental_agreed_price_id: string, amount_minor: string, currency: string, method: string, reference: string | null, proposed_by: string, proposed_at: string, approved_by: string, approved_at: string, client_request_id: string, reversed: boolean, reversal_id: string | null }
interface ReversalProposalDTO { id: string, payment_id: string, rental_contract_id: string, reason: string, proposed_by: string, proposed_at: string, client_request_id: string, status: string }
interface ReversalDTO { id: string, payment_id: string, reversal_proposal_id: string, rental_contract_id: string, reason: string, proposed_by: string, proposed_at: string, approved_by: string, approved_at: string, client_request_id: string }
interface PaymentsHistoryDTO { rental_contract_id: string, rental_agreed_price_id: string, currency: string, total_minor: string, paid_minor: string, outstanding_minor: string, payments: PaymentDTO[], reversals: ReversalDTO[], pending_payment_proposals: PaymentProposalDTO[], pending_reversal_proposals: ReversalProposalDTO[] }

function grantFinance(ws: string) {
  if (!OPERATOR_URL) {
    throw new Error('E2E_OPERATOR_DATABASE_URL belum diset — provisioning operator belum dijalankan; tes gagal, bukan dilewati')
  }
  if (!existsSync(GRANT_CLI)) {
    throw new Error(`CLI grant-entitlement tidak ditemukan di ${GRANT_CLI} — bangun dulu: go build -o grant-entitlement ./cmd/grant-entitlement (di services/business-api)`)
  }
  const out = execFileSync(GRANT_CLI, ['--workspace', ws, '--module', 'finance.basic', '--version', '0.1.0', '--reason', 'tes E2E owner-flow'], {
    encoding: 'utf-8',
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: OPERATOR_URL },
  })
  if (!out.startsWith('granted ') && !out.startsWith('already_granted')) {
    throw new Error(`grant finance CLI: hasil tak terduga (${out.slice(0, 80)})`)
  }
}

async function getPayments(page: Page, ws: string, draftId: string, email: string = ownerEmail): Promise<{ status: number, body: any }> {
  const res = await page.request.get(`/api/workspaces/${ws}/rental/contracts/${draftId}/payments`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}` }
  })
  return { status: res.status(), body: await res.json() }
}

async function proposePayment(page: Page, ws: string, draftId: string, email: string, key: string, body: { amount_minor: string, currency: string, method: string, reference?: string | null }): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/payment-proposals`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: body
  })
  return { status: res.status(), body: await res.json() }
}

async function approvePayment(page: Page, ws: string, draftId: string, proposalId: string, email: string, key: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/payment-proposals/${proposalId}/approvals`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: {}
  })
  return { status: res.status(), body: await res.json() }
}

async function proposeReversal(page: Page, ws: string, draftId: string, paymentId: string, email: string, key: string, reason: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/payments/${paymentId}/reversal-proposals`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: { reason }
  })
  return { status: res.status(), body: await res.json() }
}

async function approveReversal(page: Page, ws: string, draftId: string, paymentId: string, proposalId: string, email: string, key: string): Promise<{ status: number, body: any }> {
  const res = await page.request.post(`/api/workspaces/${ws}/rental/contracts/${draftId}/payments/${paymentId}/reversal-proposals/${proposalId}/approvals`, {
    headers: { Authorization: `Bearer ${await apiToken(page.request, email)}`, 'Idempotency-Key': key },
    data: {}
  })
  return { status: res.status(), body: await res.json() }
}

// F2-UI-07: workspace + pemilik mandiri per tes pembayaran (tanpa ws bersama)
// supaya tiap tes pembayaran bisa jalan sendiri lewat --grep. Tarif 150000,
// draf 2047 2 unit, total 300000 — contoh kerja yang sama dengan tes harga.
async function paymentSetup(page: Page, tag: string): Promise<{ email: string, ws: string, token: string, customer: { id: string, display_name: string }, item: { id: string, sku: string, name: string, status: string }, draft: DraftRow, fact: FactRow }> {
  const s = await priceSetup(page, `py-${tag}`)
  await seedPriceOne(page.request, s.ws, s.token, s.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
  const draft = await seedDraftOne(page.request, s.ws, s.token, s.customer.id, s.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
  const quote = await getQuote(page, s.ws, draft.id, s.email)
  expect(quote.status).toBe(200)
  const accepted = await acceptPrice(page, s.ws, draft.id, s.email, randomUUID(), (quote.body as QuoteRow).price_version_id, '300000')
  expect(accepted.status).toBe(201)
  return { ...s, draft, fact: accepted.body as FactRow }
}

// Draf + fakta siap dibayar: finance di-grant lewat CLI sungguhan lalu
// diaktifkan lewat API (alur aktivasi UI sendiri sudah tercakup tes modul).
async function seedPaymentReady(page: Page, tag: string): Promise<{ email: string, ws: string, token: string, customer: { id: string, display_name: string }, item: { id: string, sku: string, name: string, status: string }, draft: DraftRow, fact: FactRow }> {
  const s = await paymentSetup(page, tag)
  grantFinance(s.ws)
  const act = await page.request.post(`/api/workspaces/${s.ws}/modules/finance.basic/activate`, {
    headers: { Authorization: `Bearer ${s.token}` },
    data: { version: '0.1.0' }
  })
  expect(act.status()).toBe(200)
  return s
}

// Undang manajer kedua lalu gabung lewat UI (alur terima-undangan yang
// sama seperti tes izin custody) supaya approval dua aktor bisa lewat UI.
// Pemanggil harus dalam keadaan belum masuk (atau masuk sebagai `by`).
async function inviteManager(page: Page, ws: string, email: string, by: string): Promise<void> {
  const code = await uiInvite(page, ws, email, 'manager', by)
  await uiRegister(page, email)
  await uiLogin(page, email)
  await gotoAuthed(page, `/invites/accept?workspace_id=${ws}`, email)
  await page.getByLabel('Kode undangan').fill(code)
  await page.locator('form button[type="submit"]').click()
  await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/members`))
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
    // F2-UI-03: 6 URL (Tarif ikut) x 3 lebar + probe fokus = 19 gotoAuthed
    // dengan login ulang tiap reload; batas 120 dtk bawaan mepet — 180 dtk.
    test.setTimeout(180_000)
    await uiLogin(page, ownerEmail)
    for (const width of [360, 768, 1440]) {
      await page.setViewportSize({ width, height: 900 })
      for (const url of [`/workspaces/${wsA}/customers`, `/workspaces/${wsA}/catalog`, `/workspaces/${wsA}/members`, `/workspaces/${wsA}/modules`, `/workspaces/${wsA}/assets`, `/workspaces/${wsA}/prices`]) {
        await gotoAuthed(page, url, ownerEmail)
        const nav = page.getByRole('navigation', { name: 'Navigasi workspace' })
        await expect(nav).toBeVisible()
        for (const name of ['Anggota', 'Fitur', 'Pelanggan', 'Barang', 'Unit', 'Tarif']) {
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

  test('tarif: daftar kosong, buat 201, tampil + persist', async ({ page }) => {
    wsPrices = await uiCreateWorkspace(page, `Toko Tarif ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsPrices, token, 1, 'TRF')
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await expect(page.getByRole('heading', { name: 'Tarif', exact: true })).toBeVisible()
    // Tanpa pilihan barang: ajakan memilih, bukan riwayat kosong.
    await expect(page.getByText('Pilih barang dulu untuk melihat tarif.')).toBeVisible()
    await expect(page.getByText('Belum ada tarif.')).toHaveCount(0)
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    await page.getByLabel('Mulai (UTC)').fill('2032-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2032-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('150000')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('saved-heading')
    await expect(listItems(page)).toHaveCount(1)
    await expect(listItems(page).first()).toContainText('150000 unit tarif · IDR')
    await expect(listItems(page).first()).toContainText('2032-01-01T00:00:00Z')
    await expect(listItems(page).first()).toContainText('2032-02-01T00:00:00Z')
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(listItems(page)).toHaveCount(1)
    const rows = await listPriceRows(page, wsPrices, item.id)
    expect(rows.length).toBe(1)
    expect(rows[0].price_list_id).toBe('standard')
    expect(rows[0].amount_minor).toBe('150000')
    expect(rows[0].currency).toBe('IDR')
    expect(rows[0].valid_from).toBe('2032-01-01T00:00:00Z')
    expect(rows[0].valid_to).toBe('2032-02-01T00:00:00Z')
    expect(rows[0].client_request_id).not.toBe('')
  })

  test('tarif: validasi 400 per-field + batas amount/currency', async ({ page, request }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-item-error')).toContainText('Pilih barang dulu')
    expect(await page.evaluate(() => document.activeElement?.id)).toBe('price-item')
    await page.getByLabel('Barang').selectOption(item.id)
    // Jumlah: kosong, nol di depan, negatif, pecahan, eksponen, spasi.
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-amount-error')).toContainText('wajib diisi')
    for (const bad of ['01', '-5', '1.5', '1e3', ' 150']) {
      await page.getByLabel('Nilai tarif (unit tersimpan)').fill(bad)
      await page.getByRole('button', { name: 'Simpan tarif' }).click()
      await expect(page.locator('#price-amount-error')).toContainText('bilangan bulat')
    }
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('9223372036854775808')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-amount-error')).toContainText('paling besar')
    // Mata uang: huruf kecil, terlalu pendek/panjang.
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('150000')
    for (const bad of ['idr', 'ID', 'IDRR']) {
      await page.getByLabel('Mata uang').fill(bad)
      await page.getByRole('button', { name: 'Simpan tarif' }).click()
      await expect(page.locator('#price-currency-error')).toContainText('3 huruf kapital')
    }
    await page.getByLabel('Mata uang').fill('IDR')
    // Waktu: akhir sebelum awal, akhir sama dengan awal.
    await page.getByLabel('Mulai (UTC)').fill('2032-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2032-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-to-error')).toContainText('sesudah tanggal mulai')
    await page.getByLabel('Berakhir (UTC)').fill('2032-03-01T00:00')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-to-error')).toContainText('sesudah tanggal mulai')
    // Koreksi menjadi valid: 201.
    await page.getByLabel('Berakhir (UTC)').fill('2032-04-01T00:00')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    // Server menolak langsung (bukan lewat UI): overflow, angka JSON,
    // mata uang huruf kecil — masing-masing 400.
    const token = await apiToken(request, ownerEmail)
    const badAmount = await request.post(`/api/workspaces/${wsPrices}/catalog/items/${item.id}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { valid_from: '2032-05-01T00:00:00Z', valid_to: '2032-06-01T00:00:00Z', amount_minor: '9223372036854775808', currency: 'IDR' }
    })
    expect(badAmount.status()).toBe(400)
    const jsonNumber = await request.post(`/api/workspaces/${wsPrices}/catalog/items/${item.id}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID(), 'Content-Type': 'application/json' },
      data: '{"valid_from":"2032-05-01T00:00:00Z","valid_to":"2032-06-01T00:00:00Z","amount_minor":150,"currency":"IDR"}'
    })
    expect(jsonNumber.status()).toBe(400)
    const lowerCurrency = await request.post(`/api/workspaces/${wsPrices}/catalog/items/${item.id}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { valid_from: '2032-05-01T00:00:00Z', valid_to: '2032-06-01T00:00:00Z', amount_minor: '150', currency: 'idr' }
    })
    expect(lowerCurrency.status()).toBe(400)
    // Nilai di atas 2^53 tetap eksak sebagai string lewat API.
    const big = await request.post(`/api/workspaces/${wsPrices}/catalog/items/${item.id}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { valid_from: '2032-05-01T00:00:00Z', valid_to: '2032-06-01T00:00:00Z', amount_minor: '9007199254740993', currency: 'IDR' }
    })
    expect(big.status()).toBe(201)
    expect(((await big.json()) as { amount_minor: string }).amount_minor).toBe('9007199254740993')
  })

  test('tarif: UTC + presisi — zona browser, tahun, menit, riwayat submenit', async ({ page, request }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    // Zona browser di VM ini bukan UTC; nilai yang dikirim harus tetap
    // UTC persis seperti yang dimasukkan (tanpa geser zona lokal).
    const zone = await page.evaluate(() => Intl.DateTimeFormat().resolvedOptions().timeZone)
    expect(zone).not.toBe('UTC')
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2033-03-15T10:30')
    await page.getByLabel('Berakhir (UTC)').fill('2033-04-15T10:30')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('250000')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    const rows = await listPriceRows(page, wsPrices, item.id)
    const row = rows.find(r => r.amount_minor === '250000')
    expect(row?.valid_from).toBe('2033-03-15T10:30:00Z')
    expect(row?.valid_to).toBe('2033-04-15T10:30:00Z')
    // Tahun 9999 diterima; tahun kecil yang sudah lewat ditolak server
    // tanpa clamp (isian dipertahankan, bukan digeser).
    await page.getByLabel('Mulai (UTC)').fill('9999-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('9999-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    await page.getByLabel('Mulai (UTC)').fill('0026-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('0026-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-from-error')).toContainText('tidak boleh di masa lalu')
    expect(await page.getByLabel('Mulai (UTC)').inputValue()).toBe('0026-01-01T00:00')
    // Detik dalam nilai mentah ditolak, bukan dipotong diam-diam: set
    // lewat JS lalu kirim — tidak ada POST, ada galat field.
    let posts = 0
    await page.route((url) => url.pathname.endsWith('/prices'), async (route) => {
      if (route.request().method() === 'POST') posts++
      await route.continue()
    })
    await page.evaluate(() => {
      const el = document.getElementById('price-from') as HTMLInputElement
      el.value = '2033-05-01T00:00:30'
      el.dispatchEvent(new Event('input', { bubbles: true }))
    })
    await page.getByLabel('Berakhir (UTC)').fill('2033-06-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.locator('#price-from-error')).toBeVisible()
    expect(posts).toBe(0)
    await page.unrouteAll({ behavior: 'wait' })
    // Riwayat submenit dari API: tampil persis, tidak dibulatkan ke menit.
    const token = await apiToken(request, ownerEmail)
    const sub = await request.post(`/api/workspaces/${wsPrices}/catalog/items/${item.id}/prices`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { valid_from: '2033-06-01T10:30:45.123456Z', valid_to: '2033-07-01T10:30:45.123456Z', amount_minor: '777', currency: 'IDR' }
    })
    expect(sub.status()).toBe(201)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(listItems(page).filter({ hasText: '777 unit tarif' }).first()).toContainText('2033-06-01T10:30:45.123456Z')
    await expect(listItems(page).filter({ hasText: '777 unit tarif' }).first()).toContainText('2033-07-01T10:30:45.123456Z')
  })

  test('tarif: adjacency 201 + overlap 409 + no-backdate 400 tanpa clamp', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const before = (await listPriceRows(page, wsPrices, item.id)).length
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    const create = async (from: string, to: string, amount: string) => {
      await page.getByLabel('Mulai (UTC)').fill(from)
      await page.getByLabel('Berakhir (UTC)').fill(to)
      await page.getByLabel('Nilai tarif (unit tersimpan)').fill(amount)
      await page.getByLabel('Mata uang').fill('IDR')
      await page.getByRole('button', { name: 'Simpan tarif' }).click()
    }
    await create('2035-01-01T00:00', '2035-02-01T00:00', '100')
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    // Berdampingan persis di ujung interval lama: legal, 201.
    await create('2035-02-01T00:00', '2035-03-01T00:00', '200')
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    // Tumpang tindih: 409 interval, isian dipertahankan, tanpa baris baru.
    await create('2035-01-15T00:00', '2035-03-01T00:00', '300')
    await expect(page.getByRole('alert')).toContainText('interval harga bertabrakan')
    expect(await page.getByLabel('Nilai tarif (unit tersimpan)').inputValue()).toBe('300')
    expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before + 2)
    // Masa lalu: 400 tanpa clamp — nilai tidak digeser ke masa kini.
    await create('2020-01-01T00:00', '2020-02-01T00:00', '400')
    await expect(page.locator('#price-from-error')).toContainText('tidak boleh di masa lalu')
    expect(await page.getByLabel('Mulai (UTC)').inputValue()).toBe('2020-01-01T00:00')
    expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before + 2)
  })

  test('tarif: tulis-terkomit/respons-hilang — kirim lagi kunci sama 200, tanpa tulis kedua', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const before = await listPriceRows(page, wsPrices, item.id)
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          postAttempts++
          if (postAttempts === 1) {
            // Tulis sungguhan sampai ke API nyata lalu responsnya hilang
            // (injeksi kegagalan browser-mocked, bukan bukti database/RLS).
            await route.fetch()
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2036-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2036-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('500')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('alert')).toContainText('mungkin sudah tersimpan')
    // Isian + ringkasan beku dipertahankan; field/picker terkunci.
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toHaveValue('500')
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toBeDisabled()
    await expect(page.getByLabel('Barang')).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toBeDisabled()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Mulai jadwal baru' })).toBeVisible()
    // Tulis pertama SUDAH terkomit di server meski responsnya hilang.
    const committed = await listPriceRows(page, wsPrices, item.id)
    expect(committed.length).toBe(before.length + 1)
    const committedRow = committed.find(r => r.amount_minor === '500')
    expect(committedRow).toBeDefined()
    // Enter dan submit biasa tidak mengirim ulang.
    await page.getByLabel('Mata uang').focus()
    await page.keyboard.press('Enter')
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    await page.evaluate(() => {
      document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(1)
    // GET-only refresh membuktikan baris ada tanpa POST baru.
    await page.locator('section[aria-labelledby="add-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page).filter({ hasText: '500 unit tarif' })).toHaveCount(1)
    expect(postAttempts).toBe(1)
    // Kirim lagi dengan kunci SAMA: replay 200 mengonfirmasi baris yang
    // sama, tanpa tulis kedua.
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    const after = await listPriceRows(page, wsPrices, item.id)
    expect(after.length).toBe(before.length + 1)
    expect(after.find(r => r.amount_minor === '500')?.id).toBe(committedRow!.id)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: 200/202 dan 201 cacat bukan sukses', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const before = (await listPriceRows(page, wsPrices, item.id)).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    const goodRow = {
      id: '00000000-0000-4000-8000-000000000001',
      workspace_id: wsPrices,
      catalog_item_id: item.id,
      price_list_id: 'standard',
      client_request_id: 'kunci-yang-benar',
      valid_from: '2037-01-01T00:00:00Z',
      valid_to: '2037-02-01T00:00:00Z',
      amount_minor: '600',
      currency: 'IDR'
    }
    let attempt = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          attempt++
          if (attempt === 1) {
            // 200 dengan kunci yang salah: bukan konfirmasi.
            await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...goodRow, client_request_id: 'kunci-salah' }) })
            return
          }
          if (attempt === 2) {
            // 202 bukan sukses.
            await route.fulfill({ status: 202, contentType: 'application/json', body: JSON.stringify(goodRow) })
            return
          }
          if (attempt === 3) {
            // 201 dengan jumlah berbeda: bukan konfirmasi.
            await route.fulfill({ status: 201, contentType: 'application/json', body: JSON.stringify({ ...goodRow, amount_minor: '999' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2037-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2037-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('600')
    await page.getByLabel('Mata uang').fill('IDR')
    for (let i = 1; i <= 3; i++) {
      await page.getByRole('button', { name: 'Simpan tarif' }).click()
      await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
      await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toHaveCount(0)
      expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before)
      // Aksi baru menebus snapshot yang dicurigai; kiriman berikutnya
      // memakai kunci baru.
      await page.getByRole('button', { name: 'Mulai jadwal baru' }).click()
      await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toBeEnabled()
    }
    // Percobaan keempat sampai ke API nyata: 201 sungguhan.
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(attempt).toBe(4)
    expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: kunci salah 409 — hanya Mulai jadwal baru yang menebus', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const before = (await listPriceRows(page, wsPrices, item.id)).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'kunci idempotency sudah dipakai untuk permintaan yang berbeda' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2038-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2038-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('700')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('alert')).toContainText('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toHaveCount(0)
    // Kunci tercemar: tidak ada kirim-ulang buta; hanya aksi baru.
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toBeDisabled()
    expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before)
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Mulai jadwal baru' }).click()
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toBeEnabled()
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect((await listPriceRows(page, wsPrices, item.id)).length).toBe(before + 1)
  })

  test('tarif: Mulai jadwal baru membatalkan pemulihan; jadwal lama tetap ada', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const before = (await listPriceRows(page, wsPrices, item.id)).length
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
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
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2039-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2039-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('800')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    // Peringatan aksi baru terlihat sebelum diklik: jadwal lama mungkin
    // sudah tersimpan dan tidak dibatalkan.
    await expect(page.getByText('mungkin sudah tersimpan dan tidak dibatalkan')).toBeVisible()
    await page.getByRole('button', { name: 'Mulai jadwal baru' }).click()
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toBeEnabled()
    expect(postAttempts).toBe(1)
    // Jadwal lain (interval berbeda) dengan kunci baru: 201.
    await page.getByLabel('Mulai (UTC)').fill('2039-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2039-04-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('801')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    const rows = await listPriceRows(page, wsPrices, item.id)
    expect(rows.length).toBe(before + 2)
    expect(rows.some(r => r.amount_minor === '800')).toBe(true)
    expect(rows.some(r => r.amount_minor === '801')).toBe(true)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: barang arsip dipilih untuk riwayat; jadwal baru diblokir; pemulihan kunci-sama tetap jalan', async ({ page }) => {
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsPrices, token, 1, 'ARS-T')
    const ars = await catalogItemBySku(page, wsPrices, 'ARS-T-1')
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(ars.id)
    await page.getByLabel('Mulai (UTC)').fill('2045-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2045-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('900')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${wsPrices}' AND sku='ARS-T-1'`)
    // Sesudah reload: arsip tetap ditawarkan untuk RIWAYAT.
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    const options = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(options.some(t => t.includes('ARS-T-1') && t.includes('(arsip — riwayat)')), 'item arsip harus ditawarkan untuk riwayat').toBe(true)
    await page.getByLabel('Barang').selectOption(ars.id)
    await expect(listItems(page)).toHaveCount(1)
    await expect(page.getByText('Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(0)
    // Pemulihan kunci-sama sesudah pengarsipan: replay 200, bukan 409.
    await seedItems(page.request, wsPrices, token, 1, 'ARS-R')
    const arsR = await catalogItemBySku(page, wsPrices, 'ARS-R-1')
    // Katalog dimuat sekali per halaman: barang yang baru dibuat baru
    // muncul di pemilih sesudah muat ulang halaman.
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    const beforeR = (await listPriceRows(page, wsPrices, arsR.id)).length
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
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
    await page.getByLabel('Barang').selectOption(arsR.id)
    await page.getByLabel('Mulai (UTC)').fill('2045-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2045-04-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('901')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${wsPrices}' AND sku='ARS-R-1'`)
    expect((await listPriceRows(page, wsPrices, arsR.id)).length).toBe(beforeR + 1)
    // Panel pemulihan tetap terlihat meski barang kini diarsipkan.
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    expect((await listPriceRows(page, wsPrices, arsR.id)).length).toBe(beforeR + 1)
    await page.unrouteAll({ behavior: 'wait' })
    // Percobaan BARU pada barang arsip tetap 409 'barang tidak aktif'.
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(ars.id)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(0)
  })

  test('tarif: kasir baca 200 + simpan 403, nonanggota 403, 401 lanjut ke prices', async ({ page }) => {
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const code = await uiInvite(page, wsPrices, staff3Email, 'cashier', ownerEmail)
    // staff3 sudah terdaftar pada tes undangan kedaluwarsa; langsung
    // masuk dan terima undangan workspace ini.
    await uiLogin(page, staff3Email)
    await gotoAuthed(page, `/invites/accept?workspace_id=${wsPrices}`, staff3Email)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsPrices}/members`)
    await uiLogin(page, staff3Email)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, staff3Email)
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(listItems(page).first()).toBeVisible()
    await page.getByLabel('Mulai (UTC)').fill('2046-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2046-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1100')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('alert')).toContainText('tidak punya izin mengelola data usaha')
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toHaveValue('1100')
    await expect(listItems(page).first()).toBeVisible()
    // Nonanggota: GET 403 menyembunyikan halaman, sesi dipertahankan.
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    // 401 menghapus sesi dan minta masuk lewat rute prices yang sama.
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    psql(`DELETE FROM sessions WHERE subject_id = (SELECT id FROM identity_subjects WHERE email = '${ownerEmail}')`)
    await page.getByRole('link', { name: 'Pelanggan' }).click()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsPrices}/customers`)
    // Lanjut login langsung ke prices (resolveLoginTarget 'prices').
    await page.getByRole('link', { name: 'Tarif' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsPrices}/prices`)
    await page.reload()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(ownerEmail)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${wsPrices}/prices`)
  })

  test('tarif: respons basi lintas barang/workspace/sesi tidak menyentuh halaman pengganti', async ({ page }) => {
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsPrices, token, 1, 'BAS')
    const bas = await catalogItemBySku(page, wsPrices, 'BAS-1')
    const trf = await catalogItemBySku(page, wsPrices, 'TRF-1')
    await uiLogin(page, ownerEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    // GET riwayat barang pertama ditahan; pindah pilihan barang sebelum
    // respons tiba — respons basi tidak boleh mengisi daftar barang kedua.
    let releaseFirst!: () => void
    const firstHeld = new Promise<void>((resolve) => { releaseFirst = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/prices') && u.searchParams.get('offset') === null) {
          await firstHeld
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(trf.id)
    await page.waitForTimeout(300)
    await page.getByLabel('Barang').selectOption(bas.id)
    // GET barang kedua masih tertunda: daftar menunjukkan status muat,
    // bukan klaim kosong palsu dan bukan baris barang pertama.
    await expect(page.getByText('Memuat daftar…')).toBeVisible()
    releaseFirst()
    await page.waitForTimeout(300)
    // Respons basi barang pertama dibuang; GET barang kedua (0 baris)
    // yang mengisi daftar — kini benar-benar kosong.
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // GET awal basi sesudah pindah workspace: tidak mulai GET katalog
    // untuk workspace lama dan tidak menyentuh sesi pengganti.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items') && !u.searchParams.has('offset')) {
          await new Promise(r => setTimeout(r, 1500))
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByRole('link', { name: 'Anggota' }).click()
    await expect(page).toHaveURL(`/workspaces/${wsPrices}/members`)
    await expect(page.getByRole('heading', { name: 'Anggota', exact: true })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: 51 baris riwayat + simpan dan replay reset ke halaman 1 + muat semua', async ({ page }) => {
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsPrices, token, 1, 'TRF-51')
    const item = await catalogItemBySku(page, wsPrices, 'TRF-51-1')
    await seedPrices(page.request, wsPrices, token, item.id, 51)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(listItems(page)).toHaveCount(50)
    await expect(listItems(page).first()).toContainText('2040-01-01T00:00:00Z')
    await page.getByRole('button', { name: 'Muat lebih', exact: true }).click()
    await expect(listItems(page)).toHaveCount(51)
    const ids = await page.evaluate(() => Array.from(document.querySelectorAll('section[aria-labelledby="list-heading"] li')).map(li => li.textContent))
    expect(new Set(ids).size).toBe(51)
    // Simpan baru mereset ke halaman 1; baris tersimpan tampil di
    // wilayah tersimpan, daftar menunjukkan 50 pertama tanpa duplikat.
    await page.getByLabel('Mulai (UTC)').fill('2044-04-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2044-05-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('5151')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    await expect(listItems(page)).toHaveCount(50)
    await expect(listItems(page).first()).toContainText('2040-01-01T00:00:00Z')
    await page.getByRole('button', { name: 'Muat lebih', exact: true }).click()
    await expect(listItems(page)).toHaveCount(52)
    // Replay sesudah tulis tak diketahui juga mereset ke halaman 1.
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
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
    await page.getByLabel('Mulai (UTC)').fill('2044-05-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2044-06-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('5152')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    await expect(listItems(page)).toHaveCount(50)
    await page.getByRole('button', { name: 'Muat lebih', exact: true }).click()
    await expect(listItems(page)).toHaveCount(53)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: katalog 101 barang — halaman 2, jumlah mentah termasuk arsip, pilihan stabil, retry galat, tanpa N+1', async ({ page }) => {
    const wsCatT = await uiCreateWorkspace(page, `Toko Katalog Tarif ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsCatT, token, 101, 'KTT')
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${wsCatT}' AND sku='KTT-50'`)
    await uiLogin(page, ownerEmail)
    let catalogGets = 0
    let failNext = false
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items')) {
          catalogGets++
          if (failNext && u.searchParams.get('offset') === '100') {
            failNext = false
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsCatT}/prices`, ownerEmail)
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(101)
    await expect(page.getByText('100 barang dimuat sejauh ini')).toBeVisible()
    expect(catalogGets).toBe(1)
    // Halaman kedua gagal sekali: pilihan/kursor dipertahankan, tanpa
    // rantai otomatis; retry memakai offset yang sama.
    failNext = true
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByRole('alert')).toContainText('gagal')
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(101)
    expect(catalogGets).toBe(2)
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByLabel('Barang').locator('option')).toHaveCount(102)
    expect(catalogGets).toBe(3)
    await expect(page.getByText('barang dimuat sejauh ini')).toHaveCount(0)
    // Arsip di halaman pertama berlabel riwayat dan tetap bisa dipilih;
    // barang halaman kedua juga bisa dipilih.
    const options = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(options.some(t => t.includes('KTT-50') && t.includes('(arsip — riwayat)'))).toBe(true)
    const item101 = await catalogItemBySku(page, wsCatT, 'KTT-101')
    await page.getByLabel('Barang').selectOption(item101.id)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item101.id)
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    const item50 = await catalogItemBySku(page, wsCatT, 'KTT-50')
    await page.getByLabel('Barang').selectOption(item50.id)
    await expect(page.getByText('Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif: pending/error/kosong + retry GET-only', async ({ page }) => {
    const wsEmpty = await uiCreateWorkspace(page, `Toko Tarif Kosong ${RUN}`, ownerEmail)
    const token = await apiToken(page.request, ownerEmail)
    await seedItems(page.request, wsEmpty, token, 1, 'EMP-T')
    const item = await catalogItemBySku(page, wsEmpty, 'EMP-T-1')
    await uiLogin(page, ownerEmail)
    let holdGets!: () => void
    const held = new Promise<void>((resolve) => { holdGets = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && (u.pathname.endsWith('/prices') || u.pathname.includes('/catalog/items'))) {
          await held
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsEmpty}/prices`, ownerEmail)
    await expect(page.getByText('Memuat…')).toBeVisible()
    holdGets()
    await expect(page.getByText('Pilih barang dulu untuk melihat tarif.')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // Refresh riwayat gagal 500: galat tampil, tanpa klaim kosong.
    let failOnce = true
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (failOnce && route.request().method() === 'GET' && u.pathname.endsWith('/prices') && !u.searchParams.has('offset')) {
          failOnce = false
          await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByRole('alert')).toContainText('Daftar gagal dimuat ulang')
    await expect(page.getByText('Belum ada tarif.')).toHaveCount(0)
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif R1-A: kirim-ulang 403 mempertahankan snapshot + ketidakpastian; kunci sama replay 200', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'a')
    await seedItems(page.request, ws, token, 1, 'R1A')
    const item = await catalogItemBySku(page, ws, 'R1A-1', email)
    const before = (await listPriceRows(page, ws, item.id, email)).length
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    let postAttempts = 0
    let firstKey = ''
    let firstBody: unknown = null
    let replayKey = ''
    let replayBody: unknown = null
    let denyReplay = true
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          postAttempts++
          if (postAttempts === 1) {
            firstKey = route.request().headers()['idempotency-key'] ?? ''
            firstBody = route.request().postDataJSON()
            // Tulis sungguhan sampai ke API nyata lalu responsnya hilang
            // (injeksi kegagalan browser-mocked, bukan bukti database/RLS).
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2 && denyReplay) {
            // Kirim-ulang ditolak 403: pemeriksaan izin server mendahului
            // replay, jadi penolakan ini TIDAK membuktikan tulis awal gagal.
            await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'tidak punya izin mengelola data usaha' }) })
            return
          }
          if (postAttempts === 3) {
            replayKey = route.request().headers()['idempotency-key'] ?? ''
            replayBody = route.request().postDataJSON()
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1300')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    expect(firstKey.length).toBeGreaterThan(0)
    // Tulis pertama SUDAH terkomit di server meski responsnya hilang.
    const committed = await listPriceRows(page, ws, item.id, email)
    expect(committed.length).toBe(before + 1)
    const committedRow = committed.find(r => r.amount_minor === '1300')
    expect(committedRow).toBeDefined()
    // Kirim-ulang ditolak 403: snapshot + ketidakpastian dipertahankan,
    // kirim biasa tetap terkunci, bukan kunci baru diam-diam.
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('Kirim ulang ditolak')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('tidak punya izin mengelola data usaha')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toBeDisabled()
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toHaveValue('1300')
    expect(postAttempts).toBe(2)
    // Endpoint dipulihkan: kirim-ulang kunci SAMA mengonfirmasi baris
    // yang sama, tanpa tulis kedua.
    denyReplay = false
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(3)
    expect(replayKey).toBe(firstKey)
    expect(replayBody).toEqual(firstBody)
    const after = await listPriceRows(page, ws, item.id, email)
    expect(after.length).toBe(before + 1)
    expect(after.find(r => r.amount_minor === '1300')?.id).toBe(committedRow!.id)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif R1-B: kirim-ulang 409 mismatch — satu status koheren, tanpa kirim-ulang', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'b')
    await seedItems(page.request, ws, token, 1, 'R1B')
    const item = await catalogItemBySku(page, ws, 'R1B-1', email)
    const before = (await listPriceRows(page, ws, item.id, email)).length
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    let postAttempts = 0
    let firstKey = ''
    let newKey = ''
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          postAttempts++
          if (postAttempts === 1) {
            firstKey = route.request().headers()['idempotency-key'] ?? ''
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2) {
            await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'kunci idempotency sudah dipakai untuk permintaan yang berbeda' }) })
            return
          }
          if (postAttempts === 3) {
            newKey = route.request().headers()['idempotency-key'] ?? ''
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-04-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1400')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    // Satu status mismatch yang koheren: ringkasan snapshot dipertahankan,
    // panel tak diketahui + kirim-ulang hilang.
    await expect(page.getByRole('heading', { name: 'Kunci idempotency sudah dipakai' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('Permintaan yang belum pasti:')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('1400 unit tarif · IDR')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toBeDisabled()
    // Submit biasa dari status mismatch tidak boleh POST lagi.
    await page.evaluate(() => {
      document.querySelector('form')?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(2)
    expect(firstKey.length).toBeGreaterThan(0)
    // Hanya Mulai jadwal baru yang menebus, dengan kunci baru + interval
    // baru (interval lama sudah terkomit dari tulis pertama).
    await page.getByRole('button', { name: 'Mulai jadwal baru' }).click()
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toBeEnabled()
    await page.getByLabel('Mulai (UTC)').fill('2047-05-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-06-01T00:00')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(3)
    expect(newKey.length).toBeGreaterThan(0)
    expect(newKey).not.toBe(firstKey)
    const after = await listPriceRows(page, ws, item.id, email)
    expect(after.length).toBe(before + 2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif R1-C: Mulai jadwal baru terkunci selama POST kirim-ulang berjalan', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'c')
    await seedItems(page.request, ws, token, 1, 'R1C')
    const item = await catalogItemBySku(page, ws, 'R1C-1', email)
    const before = (await listPriceRows(page, ws, item.id, email)).length
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    let postAttempts = 0
    let releaseReplay!: () => void
    const replayHeld = new Promise<void>((resolve) => { releaseReplay = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          postAttempts++
          if (postAttempts === 1) {
            await route.fetch()
            await route.abort()
            return
          }
          // Kirim-ulang ditahan: POST tertunda di browser.
          await replayHeld
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-07-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-08-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1500')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(2)
    // Selama kirim-ulang berjalan, Mulai jadwal baru terkunci sehingga
    // kelanjutan lama tidak bisa menghapus draf baru atau mengumumkan
    // simpan untuknya.
    await expect(page.getByRole('button', { name: 'Mulai jadwal baru' })).toBeDisabled()
    releaseReplay()
    await expect(page.getByRole('heading', { name: 'Tarif tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    const after = await listPriceRows(page, ws, item.id, email)
    expect(after.length).toBe(before + 1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('tarif R1-D: arsip — pemilih tetap ada saat ganti aktif→arsip→aktif→arsip lain tanpa reload', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'd')
    await seedItems(page.request, ws, token, 3, 'R1D')
    const itemA = await catalogItemBySku(page, ws, 'R1D-1', email)
    const itemB = await catalogItemBySku(page, ws, 'R1D-2', email)
    const itemC = await catalogItemBySku(page, ws, 'R1D-3', email)
    // Satu tarif untuk barang arsip supaya riwayatnya terisi.
    await seedPrices(page.request, ws, token, itemB.id, 1)
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND sku IN ('R1D-2', 'R1D-3')`)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    // Aktif: formulir tampil.
    await page.getByLabel('Barang').selectOption(itemA.id)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(1)
    // Arsip: pemberitahuan tampil, pemilih TETAP ada, formulir hilang.
    await page.getByLabel('Barang').selectOption(itemB.id)
    await expect(page.getByText('Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.')).toBeVisible()
    await expect(page.getByLabel('Barang')).toBeVisible()
    await expect(listItems(page)).toHaveCount(1)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(0)
    // Kembali aktif tanpa reload: formulir kembali.
    await page.getByLabel('Barang').selectOption(itemA.id)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(1)
    await expect(page.getByText('Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.')).toHaveCount(0)
    // Arsip lain tanpa reload: pemilih tetap, formulir hilang.
    await page.getByLabel('Barang').selectOption(itemC.id)
    await expect(page.getByText('Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.')).toBeVisible()
    await expect(page.getByLabel('Barang')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(0)
  })

  test('tarif R1-E: GET 404 — draf barang lama disembunyikan, pemilih tetap, draf kembali sesudah pilih ulang', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'e')
    await seedItems(page.request, ws, token, 2, 'R1E')
    const itemA = await catalogItemBySku(page, ws, 'R1E-1', email)
    const itemB = await catalogItemBySku(page, ws, 'R1E-2', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    await page.getByLabel('Barang').selectOption(itemA.id)
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    await page.getByLabel('Mulai (UTC)').fill('2047-09-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-10-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('1200')
    await page.getByLabel('Mata uang').fill('IDR')
    // GET riwayat barang berikutnya dijawab 404.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/prices')) {
          await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'barang tidak ditemukan' }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(itemB.id)
    await expect(page.getByText('Barang tidak ditemukan di workspace ini.')).toBeVisible()
    // Formulir jadwal lama tidak lagi ditampilkan sebagai bisa dipakai;
    // pemilih tetap tersedia untuk koreksi; tidak ada panel pemulihan baru.
    await expect(page.getByText('Barang yang dipilih tidak ada di workspace ini.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(0)
    await expect(page.getByLabel('Mulai (UTC)')).toHaveCount(0)
    await expect(page.getByLabel('Barang')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toHaveCount(0)
    await page.unrouteAll({ behavior: 'wait' })
    // Pilih ulang barang pertama: formulir kembali dengan draf yang sama.
    await page.getByLabel('Barang').selectOption(itemA.id)
    await expect(page.getByRole('button', { name: 'Simpan tarif' })).toHaveCount(1)
    await expect(page.getByLabel('Nilai tarif (unit tersimpan)')).toHaveValue('1200')
    await expect(page.getByLabel('Mata uang')).toHaveValue('IDR')
  })

  test('tarif R1-F: katalog awal 403 — penolakan tampil, Memuat… hilang, sesi dipertahankan', async ({ page }) => {
    const { email, ws, token } = await r1Setup(page, 'f')
    await seedItems(page.request, ws, token, 1, 'R1F')
    // GET katalog awal ditolak 403; workspace GET tetap 200.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.includes('/catalog/items')) {
          await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
          return
        }
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/prices`, email)
    // Penolakan tampil; loader global selesai; formulir tersembunyi.
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByText('bukan anggota workspace ini')).toBeVisible()
    await expect(page.getByText('Memuat…')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Jadwalkan tarif' })).toHaveCount(0)
    await expect(page.getByLabel('Barang')).toHaveCount(0)
    // Sesi dipertahankan (bukan 401): tombol Keluar tetap ada.
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
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

  test('screenshot f2-ui-03: tiga utama + kosong + tanpa-izin + picker + tak-diketahui + muat + galat + manifest', async ({ page }) => {
    mkdirSync(SHOTS_UI03, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI03, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI03, name))).digest('hex')
    const token = await apiToken(page.request, ownerEmail)
    const item = await catalogItemBySku(page, wsPrices, 'TRF-1')
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogin(page, ownerEmail)
      await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, ownerEmail)
      await page.getByLabel('Barang').selectOption(item.id)
      await expect(listItems(page).first()).toBeVisible()
      await shot('01-prices', 'populated-live', 'live', size.width)
    }
    await page.setViewportSize({ width: 360, height: 640 })
    // owner4Email sudah terdaftar di tes screenshot f2-ui-02 (:3076);
    // registrasi ulang akan 409, jadi langsung masuk saja.
    await uiLogin(page, owner4Email)
    const wsEmpty = await uiCreateWorkspace(page, `Toko Kosong Tarif ${RUN}`, owner4Email)
    const token4 = await apiToken(page.request, owner4Email)
    await seedItems(page.request, wsEmpty, token4, 1, 'SHT')
    const shotItem = await catalogItemBySku(page, wsEmpty, 'SHT-1', owner4Email)
    await gotoAuthed(page, `/workspaces/${wsEmpty}/prices`, owner4Email)
    await page.getByLabel('Barang').selectOption(shotItem.id)
    await expect(page.getByText('Belum ada tarif.')).toBeVisible()
    await shot('04-prices-empty', 'empty-live', 'live', 360)
    await uiLogin(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${wsPrices}/prices`, otherEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('05-prices-noperm', 'no-permission-live', 'live', 360)
    await uiLogin(page, ownerEmail)
    // Picker: barang halaman kedua sesudah >100 barang + catatan muat
    // sebagian (workspace katalog UI02 yang sudah berisi 201 barang).
    await gotoAuthed(page, `/workspaces/${wsCat}/prices`, ownerEmail)
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('barang dimuat sejauh ini')).toBeVisible()
    const item150 = await catalogItemBySku(page, wsCat, 'KAT-150')
    await page.getByLabel('Barang').selectOption(item150.id)
    expect(await page.getByLabel('Barang').inputValue()).toBe(item150.id)
    await shot('06-prices-picker', 'picker-live', 'live', 360)
    // Tak diketahui: POST sampai ke API nyata lalu responsnya hilang;
    // panel membeku + tombol kirim-ulang permintaan sama terlihat.
    await gotoAuthed(page, `/workspaces/${wsEmpty}/prices`, owner4Email)
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/prices')) {
          await route.fetch()
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByLabel('Barang').selectOption(shotItem.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByLabel('Nilai tarif (unit tersimpan)').fill('123456')
    await page.getByLabel('Mata uang').fill('IDR')
    await page.getByRole('button', { name: 'Simpan tarif' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await shot('07-prices-unknown', 'unknown-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Muat: respons ditahan 2,5 dtk, tangkap status memuat.
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.endsWith('/prices') || url.pathname.includes('/catalog/items')),
      async (route) => {
        await new Promise(r => setTimeout(r, 2500))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${wsEmpty}/prices`, owner4Email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('08-prices-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    await expect(page.getByText('Pilih barang dulu untuk melihat tarif.')).toBeVisible()
    // Galat: respons dibatalkan, tangkap halaman galat.
    await page.route(
      (url) => url.pathname.startsWith('/api/') && (url.pathname.endsWith('/prices') || url.pathname.includes('/catalog/items')),
      async (route) => { await route.abort() }
    )
    await gotoAuthed(page, `/workspaces/${wsEmpty}/prices`, owner4Email)
    await expect(page.getByText('Layanan tidak tersedia').first()).toBeVisible()
    await shot('09-prices-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    const withSums = manifest.map(m => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI03, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-03', files: withSums }, null, 2) + '\n')
    expect(withSums.length).toBe(9)
  })

  test('draf sewa: buat 201, tampil + persist + label pelanggan/barang', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'a')
    await seedCustomers(page.request, ws, token, 1, 'D4A')
    await seedItems(page.request, ws, token, 1, 'D4A')
    const customer = await customerByName(page, ws, 'D4A 1', email)
    const item = await catalogItemBySku(page, ws, 'D4A-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.getByText('Draf ini tidak mereservasi barang, tidak menjamin ketersediaan, tidak memilih unit fisik, dan tidak mengunci harga.')).toBeVisible()
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('D4A 1')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('D4A-1 — Barang D4A 1')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('2047-01-01T00:00:00Z')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('2047-02-01T00:00:00Z')).toBeVisible()
    const rows = await listDraftRows(page, ws, email)
    expect(rows.length).toBe(1)
    expect(rows[0].status).toBe('draft')
    expect(rows[0].customer_id).toBe(customer.id)
    expect(rows[0].catalog_item_id).toBe(item.id)
  })

  test('draf sewa: validasi 400 per-field + berakhir harus sesudah mulai', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'b')
    await seedCustomers(page.request, ws, token, 1, 'D4B')
    await seedItems(page.request, ws, token, 1, 'D4B')
    const customer = await customerByName(page, ws, 'D4B 1', email)
    const item = await catalogItemBySku(page, ws, 'D4B-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByText('Pilih pelanggan dulu')).toBeVisible()
    await expect(page.getByText('Pilih barang dulu')).toBeVisible()
    await expect(page.getByText('Tanggal mulai wajib diisi')).toBeVisible()
    await expect(page.getByText('Tanggal berakhir wajib diisi')).toBeVisible()
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-02-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-01-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByText('Tanggal berakhir harus sesudah tanggal mulai')).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(0)
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toHaveCount(0)
  })

  test('draf sewa: kirim-ulang 200 baris sama sesudah respons hilang', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'c')
    await seedCustomers(page.request, ws, token, 1, 'D4C')
    await seedItems(page.request, ws, token, 1, 'D4C')
    const customer = await customerByName(page, ws, 'D4C 1', email)
    const item = await catalogItemBySku(page, ws, 'D4C-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    let postAttempts = 0
    let firstKey = ''
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
          postAttempts++
          if (postAttempts === 1) {
            firstKey = route.request().headers()['idempotency-key'] ?? ''
            // Tulis sungguhan sampai ke API nyata lalu responsnya hilang
            // (injeksi kegagalan browser-mocked, bukan bukti database/RLS).
            await route.fetch()
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    expect(firstKey.length).toBeGreaterThan(0)
    // Tulis pertama SUDAH terkomit di server meski responsnya hilang.
    const committed = await listDraftRows(page, ws, email)
    expect(committed.length).toBe(1)
    const committedId = committed[0].id
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(2)
    const after = await listDraftRows(page, ws, email)
    expect(after.length).toBe(1)
    expect(after[0].id).toBe(committedId)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: kirim-ulang 403 mempertahankan snapshot; kunci sama replay 200', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'd')
    await seedCustomers(page.request, ws, token, 1, 'D4D')
    await seedItems(page.request, ws, token, 1, 'D4D')
    const customer = await customerByName(page, ws, 'D4D 1', email)
    const item = await catalogItemBySku(page, ws, 'D4D-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    let postAttempts = 0
    let firstKey = ''
    let firstBody: unknown = null
    let replayKey = ''
    let replayBody: unknown = null
    let denyReplay = true
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
          postAttempts++
          if (postAttempts === 1) {
            firstKey = route.request().headers()['idempotency-key'] ?? ''
            firstBody = route.request().postDataJSON()
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2 && denyReplay) {
            // Kirim-ulang ditolak 403: pemeriksaan izin server mendahului
            // replay, jadi penolakan ini TIDAK membuktikan tulis awal gagal.
            await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'tidak punya izin mengelola data usaha' }) })
            return
          }
          if (postAttempts === 3) {
            replayKey = route.request().headers()['idempotency-key'] ?? ''
            replayBody = route.request().postDataJSON()
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    expect(firstKey.length).toBeGreaterThan(0)
    const committed = await listDraftRows(page, ws, email)
    expect(committed.length).toBe(1)
    const committedId = committed[0].id
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('Kirim ulang ditolak')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('tidak punya izin mengelola data usaha')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan draf sewa' })).toBeDisabled()
    await expect(page.getByLabel('Berakhir (UTC)')).toHaveValue('2047-02-01T00:00')
    expect(postAttempts).toBe(2)
    denyReplay = false
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(3)
    expect(replayKey).toBe(firstKey)
    expect(replayBody).toEqual(firstBody)
    const after = await listDraftRows(page, ws, email)
    expect(after.length).toBe(1)
    expect(after[0].id).toBe(committedId)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: kirim-ulang 409 mismatch — satu status koheren, tanpa kirim-ulang', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'e')
    await seedCustomers(page.request, ws, token, 1, 'D4E')
    await seedItems(page.request, ws, token, 1, 'D4E')
    const customer = await customerByName(page, ws, 'D4E 1', email)
    const item = await catalogItemBySku(page, ws, 'D4E-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    let postAttempts = 0
    let firstKey = ''
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
          postAttempts++
          if (postAttempts === 1) {
            firstKey = route.request().headers()['idempotency-key'] ?? ''
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2) {
            await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'kunci idempotency sudah dipakai untuk permintaan yang berbeda' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Kunci idempotency sudah dipakai' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('Permintaan yang belum pasti:')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('Periode yang dicatat: mulai 2047-01-01T00:00:00Z; berakhir 2047-02-01T00:00:00Z (UTC).')).toBeVisible()
    await expect(page.getByText('sampai sebelum')).toHaveCount(0)
    // Submit biasa yang dikirim paksa tidak boleh POST lagi.
    await page.locator('section[aria-labelledby="add-heading"] form').dispatchEvent('submit')
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(2)
    // Tindakan baru yang eksplisit memakai kunci baru dan berhasil.
    await page.getByRole('button', { name: 'Mulai draf baru' }).click()
    await page.getByLabel('Mulai (UTC)').fill('2047-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-04-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect(postAttempts).toBe(3)
    const rows = await listDraftRows(page, ws, email)
    expect(rows.length).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: Mulai draf baru terkunci selama POST kirim-ulang berjalan', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'f')
    await seedCustomers(page.request, ws, token, 1, 'D4F')
    await seedItems(page.request, ws, token, 1, 'D4F')
    const customer = await customerByName(page, ws, 'D4F 1', email)
    const item = await catalogItemBySku(page, ws, 'D4F-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    let postAttempts = 0
    let releaseReplay!: () => void
    const replayHeld = new Promise<void>((resolve) => { releaseReplay = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
          postAttempts++
          if (postAttempts === 1) {
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2) {
            await replayHeld
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    const resend = page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })
    await resend.click()
    await expect.poll(() => postAttempts).toBe(2)
    await expect(page.getByRole('button', { name: 'Mulai draf baru' })).toBeDisabled()
    releaseReplay()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: arsip — label riwayat (arsip — riwayat), bukan pilihan baru; capture tetap pulih', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'g')
    await seedCustomers(page.request, ws, token, 1, 'D4G')
    await seedItems(page.request, ws, token, 2, 'D4G')
    const customer = await customerByName(page, ws, 'D4G 1', email)
    const item1 = await catalogItemBySku(page, ws, 'D4G-1', email)
    const item2 = await catalogItemBySku(page, ws, 'D4G-2', email)
    await seedDrafts(page.request, ws, token, customer.id, item1.id, 1)
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND id='${item1.id}'`)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('(arsip — riwayat)')).toBeVisible()
    const itemOptions = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(itemOptions.join(' ')).not.toContain('D4G-1')
    expect(itemOptions.join(' ')).toContain('D4G-2')
    // Capture yang belum pasti atas barang yang kini diarsipkan tetap
    // pulih lewat kirim-ulang kunci-sama sesudah guard lolos.
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
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
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item2.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-05-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-06-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND id='${item2.id}'`)
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: pelanggan/barang asing 404 identik', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'h')
    const other = await draftSetup(page, 'hx')
    await seedCustomers(page.request, ws, token, 1, 'D4H')
    await seedItems(page.request, ws, token, 1, 'D4H')
    await seedCustomers(page.request, other.ws, other.token, 1, 'D4HX')
    await seedItems(page.request, other.ws, other.token, 1, 'D4HX')
    const customer = await customerByName(page, ws, 'D4H 1', email)
    const item = await catalogItemBySku(page, ws, 'D4H-1', email)
    const foreignCustomer = await customerByName(page, other.ws, 'D4HX 1', other.email)
    const foreignItem = await catalogItemBySku(page, other.ws, 'D4HX-1', other.email)
    const missingCustomer = randomUUID()
    const missingItem = randomUUID()
    const postDraft = async (customerId: string, itemId: string) => {
      return await page.request.post(`/api/workspaces/${ws}/rental/contracts`, {
        headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
        data: { customer_id: customerId, catalog_item_id: itemId, starts_at: '2047-01-01T00:00:00Z', ends_at: '2047-02-01T00:00:00Z' }
      })
    }
    const r1 = await postDraft(missingCustomer, item.id)
    expect(r1.status()).toBe(404)
    expect(((await r1.json()) as { error: string }).error).toBe('pelanggan tidak ditemukan')
    const r2 = await postDraft(foreignCustomer.id, item.id)
    expect(r2.status()).toBe(404)
    expect(((await r2.json()) as { error: string }).error).toBe('pelanggan tidak ditemukan')
    const r3 = await postDraft(customer.id, missingItem)
    expect(r3.status()).toBe(404)
    expect(((await r3.json()) as { error: string }).error).toBe('barang tidak ditemukan')
    const r4 = await postDraft(customer.id, foreignItem.id)
    expect(r4.status()).toBe(404)
    expect(((await r4.json()) as { error: string }).error).toBe('barang tidak ditemukan')
    expect((await listDraftRows(page, ws, email)).length).toBe(0)
  })

  test('draf sewa: pelanggan 403 awal — penolakan tampil, Memuat… hilang, sesi dipertahankan', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'i')
    await seedCustomers(page.request, ws, token, 1, 'D4I')
    await seedItems(page.request, ws, token, 1, 'D4I')
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.endsWith('/customers') && url.pathname.includes(`/workspaces/${ws}/`),
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
          return
        }
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByText('bukan anggota workspace ini')).toBeVisible()
    await expect(page.getByText('Memuat…')).toHaveCount(0)
    await expect(page.getByText('Buat draf sewa')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: katalog 403 awal — penolakan tampil, Memuat… hilang, sesi dipertahankan', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'j')
    await seedCustomers(page.request, ws, token, 1, 'D4J')
    await seedItems(page.request, ws, token, 1, 'D4J')
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.endsWith('/catalog/items') && url.pathname.includes(`/workspaces/${ws}/`),
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.fulfill({ status: 403, contentType: 'application/json', body: JSON.stringify({ error: 'bukan anggota workspace ini' }) })
          return
        }
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.getByRole('heading', { name: 'Tidak ada izin' })).toBeVisible()
    await expect(page.getByText('bukan anggota workspace ini')).toBeVisible()
    await expect(page.getByText('Memuat…')).toHaveCount(0)
    await expect(page.getByText('Buat draf sewa')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Keluar' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: pagination pelanggan 101 + barang 101 (arsip dulu) + draf 51', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'k')
    await seedCustomers(page.request, ws, token, 101, 'D4K')
    await seedItems(page.request, ws, token, 100, 'D4K')
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND sku LIKE 'D4K-%'`)
    await seedItems(page.request, ws, token, 1, 'D4KX')
    const customer101 = await customerByName(page, ws, 'D4K 101', email)
    const activeItem = await catalogItemBySku(page, ws, 'D4KX-1', email)
    const firstCustomer = await customerByName(page, ws, 'D4K 1', email)
    await seedDrafts(page.request, ws, token, firstCustomer.id, activeItem.id, 51)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    // Pelanggan: halaman pertama 100, klik muat-lebih -> ke-101 muncul.
    await expect(page.getByText('100 pelanggan dimuat sejauh ini.')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak pelanggan' }).click()
    await expect(page.getByText('101 pelanggan dimuat sejauh ini.')).toBeVisible()
    const customerOptions = await page.getByLabel('Pelanggan').locator('option').allTextContents()
    expect(customerOptions.join(' ')).toContain('D4K 101')
    // Barang: halaman mentah pertama penuh arsip -> tanpa pilihan aktif;
    // muat-lebih (kursor mentah) -> barang aktif muncul, arsip tak ditawarkan.
    await expect(page.getByText('100 barang dimuat sejauh ini.')).toBeVisible()
    let itemOptions = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(itemOptions.length).toBe(1)
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('101 barang dimuat sejauh ini.')).toBeVisible()
    itemOptions = await page.getByLabel('Barang').locator('option').allTextContents()
    expect(itemOptions.join(' ')).toContain('D4KX-1')
    expect(itemOptions.join(' ')).not.toContain('D4K-1')
    // Daftar: 50 baris + Muat lebih -> 51 baris.
    await expect(listItems(page)).toHaveCount(50)
    await page.getByRole('button', { name: 'Muat lebih' }).click()
    await expect(listItems(page)).toHaveCount(51)
  })

  test('draf sewa: tanggal lampau diizinkan; UTC menit; riwayat submenit tampil apa adanya', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'l')
    await seedCustomers(page.request, ws, token, 1, 'D4L')
    await seedItems(page.request, ws, token, 1, 'D4L')
    const customer = await customerByName(page, ws, 'D4L 1', email)
    const item = await catalogItemBySku(page, ws, 'D4L-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2020-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2020-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('2020-01-01T00:00:00Z')).toBeVisible()
    const sub = await page.request.post(`/api/workspaces/${ws}/rental/contracts`, {
      headers: { Authorization: `Bearer ${token}`, 'Idempotency-Key': randomUUID() },
      data: { customer_id: customer.id, catalog_item_id: item.id, starts_at: '2033-06-01T10:30:45.123456Z', ends_at: '2033-07-01T10:30:45.123456Z' }
    })
    expect(sub.status()).toBe(201)
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('2033-06-01T10:30:45.123456Z')).toBeVisible()
  })

  test('draf sewa: simpan 201 mereset prefix daftar; refresh gagal bukan kosong', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'm')
    await seedCustomers(page.request, ws, token, 1, 'D4M')
    await seedItems(page.request, ws, token, 1, 'D4M')
    const customer = await customerByName(page, ws, 'D4M 1', email)
    const item = await catalogItemBySku(page, ws, 'D4M-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.getByText('Belum ada draf.')).toBeVisible()
    let listGets = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/contracts')) {
          listGets++
          if (listGets === 1) {
            // Rute dipasang sesudah muat awal: GET pertama yang tertangkap
            // adalah refresh sesudah simpan 201.
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'daftar kontrak gagal dibaca' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('Daftar gagal dimuat ulang. Coba muat ulang daftar.')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('Belum ada draf.')).toHaveCount(0)
    await page.unrouteAll({ behavior: 'wait' })
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(listItems(page)).toHaveCount(1)
  })

  test('draf sewa: GET koleksi 404 = galat baca, bukan penghapusan', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'n')
    await seedCustomers(page.request, ws, token, 1, 'D4N')
    await seedItems(page.request, ws, token, 1, 'D4N')
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/contracts')) {
          await route.fulfill({ status: 404, contentType: 'application/json', body: JSON.stringify({ error: 'tidak ditemukan' }) })
          return
        }
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('Daftar draf gagal dimuat. Coba muat ulang daftar.')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('Belum ada draf.')).toHaveCount(0)
    await page.unrouteAll({ behavior: 'wait' })
    await page.locator('section[aria-labelledby="list-heading"]').getByRole('button', { name: 'Muat ulang daftar' }).click()
    await expect(page.getByText('Belum ada draf.')).toBeVisible()
  })

  test('draf sewa: POST 403 pertama mempertahankan riwayat + isian', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'o')
    await seedCustomers(page.request, ws, token, 1, 'D4O')
    await seedItems(page.request, ws, token, 1, 'D4O')
    const customer = await customerByName(page, ws, 'D4O 1', email)
    const item = await catalogItemBySku(page, ws, 'D4O-1', email)
    await seedDrafts(page.request, ws, token, customer.id, item.id, 1)
    // Kasir baru per tes (bukan staf bersama): bisa jalan sendiri lewat
    // --grep maupun dalam suite penuh.
    const cashierEmail = `d4-kasir-${RUN}@e2e.umnia.local`
    await uiRegister(page, cashierEmail)
    const code = await uiInvite(page, ws, cashierEmail, 'cashier', email)
    await uiLogin(page, cashierEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${ws}`, cashierEmail)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/members$`))
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, cashierEmail)
    await expect(listItems(page)).toHaveCount(1)
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.locator('section[aria-labelledby="add-heading"]').getByText('tidak punya izin mengelola data usaha')).toBeVisible()
    await expect(page.getByLabel('Berakhir (UTC)')).toHaveValue('2047-02-01T00:00')
    await expect(listItems(page)).toHaveCount(1)
  })

  test('draf sewa R1-1: barang ditolak 409 tidak meracuni pilihan lain — pilih B lalu 201', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'r1a')
    await seedCustomers(page.request, ws, token, 1, 'D4R1A')
    await seedItems(page.request, ws, token, 2, 'D4R1A')
    const customer = await customerByName(page, ws, 'D4R1A 1', email)
    const itemA = await catalogItemBySku(page, ws, 'D4R1A-1', email)
    const itemB = await catalogItemBySku(page, ws, 'D4R1A-2', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    // Pilih A + pelanggan + periode selagi A masih aktif di peta halaman.
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(itemA.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    // A menjadi tidak aktif di antara pilihan termuat dan tulis pertama.
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND id='${itemA.id}'`)
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    // Penolakan server 409 yang sebenarnya; tidak ada baris A yang terbuat.
    await expect(page.getByText('barang tidak aktif')).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(0)
    // Tanpa reload: pilih B yang aktif. Pemberitahuan arsip hilang, tanggal
    // + tombol simpan kembali, pelanggan/periode dipertahankan.
    await page.getByLabel('Barang').selectOption(itemB.id)
    await expect(page.getByText('Barang diarsipkan — riwayat draf tetap dapat dibaca; draf baru tidak dapat dibuat.')).toHaveCount(0)
    await expect(page.getByText('barang tidak aktif')).toHaveCount(0)
    await expect(page.getByLabel('Pelanggan')).toHaveValue(customer.id)
    await expect(page.getByLabel('Mulai (UTC)')).toHaveValue('2047-01-01T00:00')
    await expect(page.getByLabel('Berakhir (UTC)')).toHaveValue('2047-02-01T00:00')
    await expect(page.getByLabel('Berakhir (UTC)')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Simpan draf sewa' })).toBeVisible()
    // Kirim biasa (bukan kirim-ulang): 201 sungguhan untuk B.
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    const rows = await listDraftRows(page, ws, email)
    expect(rows.length).toBe(1)
    expect(rows[0].catalog_item_id).toBe(itemB.id)
    expect(rows[0].customer_id).toBe(customer.id)
    expect(rows[0].status).toBe('draft')
    // Kembali ke A yang benar-benar tidak aktif: tetap ditolak server 409,
    // tanpa baris baru (A masih ditawarkan dari peta yang belum dimuat ulang).
    await page.getByLabel('Barang').selectOption(itemA.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-05-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-06-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByText('barang tidak aktif')).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(1)
  })

  test('draf sewa R1-2: salinan periode netral — bantuan input, riwayat, simpan, ringkasan pemulihan', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'r1b')
    await seedCustomers(page.request, ws, token, 1, 'D4R1B')
    await seedItems(page.request, ws, token, 1, 'D4R1B')
    const customer = await customerByName(page, ws, 'D4R1B 1', email)
    const item = await catalogItemBySku(page, ws, 'D4R1B-1', email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    // Bantuan input berakhir memakai salinan netral, tanpa klaim eksklusif.
    await expect(page.getByText('Tanggal dan jam berakhir yang dicatat untuk draf ini (UTC).')).toBeVisible()
    await expect(page.getByText('tanggal berakhir tidak termasuk')).toHaveCount(0)
    await expect(page.getByText('sampai sebelum')).toHaveCount(0)
    await page.getByLabel('Pelanggan').selectOption(customer.id)
    await page.getByLabel('Barang').selectOption(item.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    await expect(page.getByText('Periode yang dicatat: mulai 2047-01-01T00:00:00Z; berakhir 2047-02-01T00:00:00Z (UTC). Status: Draf.')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="list-heading"]').getByText('Periode yang dicatat: mulai 2047-01-01T00:00:00Z; berakhir 2047-02-01T00:00:00Z (UTC)')).toBeVisible()
    await expect(page.getByText('sampai sebelum')).toHaveCount(0)
    // Ringkasan pemulihan (unknown) memakai salinan netral yang sama.
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
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
    await page.getByLabel('Mulai (UTC)').fill('2047-03-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-04-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Periode yang dicatat: mulai 2047-03-01T00:00:00Z; berakhir 2047-04-01T00:00:00Z (UTC).')).toBeVisible()
    await expect(page.getByText('sampai sebelum')).toHaveCount(0)
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Draf tersimpan' })).toBeVisible()
    expect((await listDraftRows(page, ws, email)).length).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('draf sewa: navigasi tujuh tautan 44x44 + fokus keyboard di 360/768/1440', async ({ page }) => {
    // 21 muat halaman penuh (7 halaman x 3 lebar) — butuh lebih dari
    // batas 120 dtk per tes; bukan pelonggaran assertion.
    test.setTimeout(300000)
    const { email, ws } = await draftSetup(page, 'p')
    const pages = ['members', 'modules', 'customers', 'catalog', 'assets', 'prices', 'rentals']
    const labels = ['Anggota', 'Fitur', 'Pelanggan', 'Barang', 'Unit', 'Tarif', 'Draf sewa']
    for (const size of [{ width: 360, height: 640 }, { width: 768, height: 1024 }, { width: 1440, height: 900 }]) {
      await page.setViewportSize(size)
      for (const p of pages) {
        await gotoAuthed(page, `/workspaces/${ws}/${p}`, email)
        for (const label of labels) {
          const link = page.getByRole('link', { name: label, exact: true })
          await expect(link).toBeVisible()
          const box = await link.boundingBox()
          expect(box !== null && box.width >= 44 && box.height >= 44, `${p} ${size.width} ${label}`).toBe(true)
        }
      }
    }
    await page.setViewportSize({ width: 1440, height: 900 })
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    // Fokus keyboard terlihat: dari tombol Keluar (tepat sebelum nav
    // workspace dalam urutan Tab), satu Tab nyata harus mendarat di
    // Anggota. Tunggu nav tampil dulu supaya Tab tidak ditekan saat
    // halaman masih memuat (nav belum ter-render).
    const workspaceNav = page.getByRole('navigation', { name: 'Navigasi workspace' })
    await expect(workspaceNav).toBeVisible()
    const anggota = workspaceNav.getByRole('link', { name: 'Anggota', exact: true })
    await expect(anggota).toBeVisible()
    const keluar = page.getByRole('button', { name: 'Keluar', exact: true })
    await keluar.focus()
    await expect(keluar).toBeFocused()
    await page.keyboard.press('Tab')
    await expect(anggota).toBeFocused()
  })

  test('screenshot f2-ui-04: tiga utama + kosong + tanpa-izin + picker + tak-diketahui + muat + galat + manifest', async ({ page }) => {
    mkdirSync(SHOTS_UI04, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI04, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI04, name))).digest('hex')
    const { email, ws, token } = await draftSetup(page, 'shot')
    await seedCustomers(page.request, ws, token, 1, 'SHT')
    await seedItems(page.request, ws, token, 1, 'SHT')
    const customer = await customerByName(page, ws, 'SHT 1', email)
    const item = await catalogItemBySku(page, ws, 'SHT-1', email)
    await seedDrafts(page.request, ws, token, customer.id, item.id, 1)
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogin(page, email)
      await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
      await expect(listItems(page).first()).toBeVisible()
      await shot('01-rentals', 'populated-live', 'live', size.width)
    }
    await page.setViewportSize({ width: 360, height: 640 })
    const emptySetup = await draftSetup(page, 'shot-empty')
    await seedCustomers(page.request, emptySetup.ws, emptySetup.token, 1, 'SHTE')
    await seedItems(page.request, emptySetup.ws, emptySetup.token, 1, 'SHTE')
    await gotoAuthed(page, `/workspaces/${emptySetup.ws}/rentals`, emptySetup.email)
    await expect(page.getByText('Belum ada draf.')).toBeVisible()
    await shot('04-rentals-empty', 'empty-live', 'live', 360)
    // Non-anggota baru per tes (bukan otherEmail bersama): bisa jalan
    // sendiri lewat --grep maupun dalam suite penuh.
    const nomemberEmail = `d4-bukan-anggota-${RUN}@e2e.umnia.local`
    await uiRegister(page, nomemberEmail)
    await uiLogin(page, nomemberEmail)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, nomemberEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('05-rentals-noperm', 'no-permission-live', 'live', 360)
    await uiLogin(page, email)
    // Picker: >100 pelanggan + >100 barang, kedua tombol muat-lebih ditekan.
    const pickSetup = await draftSetup(page, 'shot-pick')
    await seedCustomers(page.request, pickSetup.ws, pickSetup.token, 101, 'SHTP')
    await seedItems(page.request, pickSetup.ws, pickSetup.token, 101, 'SHTP')
    await gotoAuthed(page, `/workspaces/${pickSetup.ws}/rentals`, pickSetup.email)
    await page.getByRole('button', { name: 'Muat lebih banyak pelanggan' }).click()
    await expect(page.getByText('101 pelanggan dimuat sejauh ini.')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('101 barang dimuat sejauh ini.')).toBeVisible()
    await shot('06-rentals-pickers', 'picker-live', 'live', 360)
    // Tak diketahui: POST sampai ke API nyata lalu responsnya hilang.
    await gotoAuthed(page, `/workspaces/${emptySetup.ws}/rentals`, emptySetup.email)
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/rental/contracts')) {
          await route.fetch()
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    const emptyCustomer = await customerByName(page, emptySetup.ws, 'SHTE 1', emptySetup.email)
    const emptyItem = await catalogItemBySku(page, emptySetup.ws, 'SHTE-1', emptySetup.email)
    await page.getByLabel('Pelanggan').selectOption(emptyCustomer.id)
    await page.getByLabel('Barang').selectOption(emptyItem.id)
    await page.getByLabel('Mulai (UTC)').fill('2047-01-01T00:00')
    await page.getByLabel('Berakhir (UTC)').fill('2047-02-01T00:00')
    await page.getByRole('button', { name: 'Simpan draf sewa' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil penyimpanan tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await shot('07-rentals-unknown', 'unknown-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Muat: respons ditahan 2,5 dtk, tangkap status memuat.
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.endsWith('/rental/contracts'),
      async (route) => {
        await new Promise(r => setTimeout(r, 2500))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${emptySetup.ws}/rentals`, emptySetup.email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('08-rentals-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Langkah tak-diketahui di atas menulis draf sungguhan ke workspace
    // kosong ini, jadi sesudah pemulihan daftar menampilkan baris itu,
    // bukan "Belum ada draf.".
    await expect(listItems(page).first()).toBeVisible()
    // Galat: respons GET dibatalkan, tangkap halaman galat. POST login
    // tetap diteruskan supaya gotoAuthed bisa masuk.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${emptySetup.ws}/rentals`, emptySetup.email)
    await expect(page.getByText('Layanan tidak tersedia').first()).toBeVisible()
    await shot('09-rentals-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    const withSums = manifest.map(m => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI04, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-04', files: withSums }, null, 2) + '\n')
    expect(withSums.length).toBe(9)
  })

  test('harga draf: tautan Lihat harga membuka halaman harga; quote tampil; konfirmasi 201 + fakta terbaca', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp1')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await expect(listItems(page).first()).toBeVisible()
    await page.getByRole('link', { name: 'Lihat harga' }).click()
    await expect(page).toHaveURL(`/workspaces/${ws}/rental-prices/${draft.id}`)
    await expect(page.getByRole('heading', { name: 'Harga draf' })).toBeVisible()
    await expect(page.getByText('Tarif: 150000 unit tarif · IDR per 24 jam')).toBeVisible()
    await expect(page.getByText('2 unit 24 jam')).toBeVisible()
    await expect(page.getByText('Total: 300000 unit tarif · IDR')).toBeVisible()
    await expect(page.locator('section[aria-labelledby="quote-heading"]').getByText('Periode yang dicatat: mulai 2047-01-01T00:00:00Z; berakhir 2047-01-03T00:00:00Z (UTC)')).toBeVisible()
    await expect(page.getByText('Berlaku tarif: 2047-01-01T00:00:00Z sampai sebelum 2047-02-01T00:00:00Z (UTC)')).toBeVisible()
    await expect(page.getByText('Belum ada harga yang dicatat untuk draf ini.')).toBeVisible()
    // Panel konfirmasi: ringkasan + dampak eksplisit, tanpa POST saat dibuka.
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    await expect(page.getByText('Fakta ini hanya mencatat harga yang dilihat untuk draf ini. Ini bukan persetujuan pelanggan, bukan perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.')).toBeVisible()
    expect((await getFact(page, ws, draft.id, email)).status).toBe(404)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    const fact = await getFact(page, ws, draft.id, email)
    expect(fact.status).toBe(200)
    expect(fact.body.price_version_id).toBeTruthy()
    expect(fact.body.amount_minor).toBe('150000')
    expect(fact.body.total_minor).toBe('300000')
    expect(fact.body.units).toBe(2)
    expect(fact.body.pricing_rule_version_id).toBe('elapsed_24h_ceil_v1')
    expect(fact.body.rental_contract_id).toBe(draft.id)
    expect(countAudits(ws, 'agreed_price_recorded')).toBe(1)
  })

  test('harga draf: respons hilang lalu kirim-ulang kunci sama 200; tanpa baris/audit kedua', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp2')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
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
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    // POST pertama sungguhan sudah terkomit (responsnya yang hilang).
    expect((await getFact(page, ws, draft.id, email)).status).toBe(200)
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    const fact = await getFact(page, ws, draft.id, email)
    expect(fact.status).toBe(200)
    expect(fact.body.total_minor).toBe('300000')
    expect(countAudits(ws, 'agreed_price_recorded')).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf: tiga 409 berbeda — fakta sudah ada, kunci salah, expected tidak cocok', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp3')
    const price = await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-03-01T00:00:00Z', '150000')
    const draftA = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    const draftB = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-05T00:00:00Z', '2047-01-07T00:00:00Z')
    const draftC = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-10T00:00:00Z', '2047-01-12T00:00:00Z')
    await uiLogin(page, email)
    // (a) Fakta sudah ada (nyata): konfirmasi dibuka, fakta dicatat aksi
    // lain, kirim UI ditolak 409 — halaman memulihkan lewat GET fakta.
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftA.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    const quoteA = await getQuote(page, ws, draftA.id, email)
    const recorded = await acceptPrice(page, ws, draftA.id, email, randomUUID(), quoteA.body.price_version_id, quoteA.body.total_minor)
    expect(recorded.status).toBe(201)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toHaveCount(0)
    expect(countAudits(ws, 'agreed_price_recorded')).toBe(1)
    // (b) Kunci salah (injeksi): panel mismatch, tanpa kirim-ulang; sesudah
    // retirement, maksud baru dapat dicatat sungguhan.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'kunci idempotency sudah dipakai untuk permintaan yang berbeda' }) })
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftB.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Kunci idempotency sudah dipakai' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Mulai pencatatan baru' }).click()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    // (c) Expected tidak cocok (injeksi): panel berbeda, tanpa kirim-ulang.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'harga yang diharapkan tidak cocok dengan penawaran saat ini' }) })
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftC.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga yang diharapkan tidak cocok' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Mulai pencatatan baru' }).click()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    expect(price.id).toBeTruthy()
  })

  test('harga draf: kasir baca quote/fakta; simpan 403; nonanggota 403; 401 lanjut ke halaman harga', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp4')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    const cashierEmail = `hp4-kasir-${RUN}@e2e.umnia.local`
    await uiLogin(page, email)
    const code = await uiInvite(page, ws, cashierEmail, 'cashier', email)
    await uiRegister(page, cashierEmail)
    await uiLogin(page, cashierEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${ws}`, cashierEmail)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${ws}/members`)
    await uiLogin(page, cashierEmail)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, cashierEmail)
    await expect(page.getByText('Tarif: 150000 unit tarif · IDR per 24 jam')).toBeVisible()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('alert')).toContainText('tidak punya izin mengelola data usaha')
    await expect(page.getByRole('button', { name: 'Batalkan' })).toBeVisible()
    expect((await getFact(page, ws, draft.id, email)).status).toBe(404)
    // Nonanggota: GET 403 menyembunyikan halaman, sesi dipertahankan.
    const nomemberEmail = `hp4-bukan-anggota-${RUN}@e2e.umnia.local`
    await uiRegister(page, nomemberEmail)
    await uiLogin(page, nomemberEmail)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, nomemberEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    // 401 menghapus sesi dan minta masuk lewat halaman harga yang sama.
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    psql(`DELETE FROM sessions WHERE subject_id = (SELECT id FROM identity_subjects WHERE email = '${email}')`)
    await page.getByRole('link', { name: 'Pelanggan' }).click()
    await expect(page).toHaveURL(/\/login\?next=/)
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(`/workspaces/${ws}/customers`)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await expect(page.getByRole('heading', { name: 'Harga draf' })).toBeVisible()
    await expect(page.getByText('Tarif: 150000 unit tarif · IDR per 24 jam')).toBeVisible()
  })

  test('harga draf: draf di luar 50 pertama butuh paging eksplisit; tanpa rantai otomatis', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp5')
    await seedPriceOne(page.request, ws, token, item.id, '2040-01-01T00:00:00Z', '2050-01-01T00:00:00Z', '100000')
    await seedDrafts(page.request, ws, token, customer.id, item.id, 51)
    const rows = await listDraftRows(page, ws, email)
    expect(rows.length).toBe(51)
    const target = rows[50]
    let collectionGets = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/contracts')) collectionGets++
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${target.id}`, email)
    await expect(page.getByText('Mencari draf dalam daftar:')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Muat lebih banyak draf' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    expect(collectionGets).toBe(1)
    await page.getByRole('button', { name: 'Muat lebih banyak draf' }).click()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toBeVisible()
    expect(collectionGets).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf: peta label di luar 100 + arsip tetap dapat dibaca/dicatat', async ({ page }) => {
    const { email, ws, token } = await draftSetup(page, 'hp-hp6')
    await seedCustomers(page.request, ws, token, 101, 'HP6C')
    await seedItems(page.request, ws, token, 101, 'HP6I')
    const customer = await customerByName(page, ws, 'HP6C 101', email)
    const item = await catalogItemBySku(page, ws, 'HP6I-101', email)
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await expect(page.getByText('Nama pelanggan belum dimuat')).toBeVisible()
    await expect(page.getByText('Nama barang belum dimuat')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak pelanggan' }).click()
    await expect(page.getByText('HP6C 101')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('HP6I-101')).toBeVisible()
    // Barang diarsipkan sesudah draf dibuat: label riwayat berarsip,
    // quote + pencatatan tetap jalan (tidak ada larangan arsip di sini).
    psql(`UPDATE catalog_items SET status='archived' WHERE workspace_id='${ws}' AND id='${item.id}'`)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await page.getByRole('button', { name: 'Muat lebih banyak pelanggan' }).click()
    await expect(page.getByText('HP6C 101')).toBeVisible()
    await page.getByRole('button', { name: 'Muat lebih banyak barang' }).click()
    await expect(page.getByText('(arsip — riwayat)')).toBeVisible()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    expect((await getFact(page, ws, draft.id, email)).status).toBe(200)
  })

  test('harga draf: quote 404 tanpa tarif + 422 overflow; tanpa harga nol', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp7a')
    const draftNoTariff = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftNoTariff.id}`, email)
    await expect(page.getByText('harga efektif tidak ditemukan')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Lihat tarif' })).toBeVisible()
    await expect(page.getByText('Total: 0 unit tarif')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    // Overflow: tarif maksimum × 2 unit melampaui int64.
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '9223372036854775807')
    const draftOverflow = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftOverflow.id}`, email)
    await expect(page.getByText('total di luar batas yang dapat diwakili')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Lihat tarif' })).toBeVisible()
    await expect(page.getByText('Total: 0 unit tarif')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
  })

  test('harga draf: uang eksak di atas 2^53 + nol + maksimum; batas 24 jam mikrodetik', async ({ page }) => {
    const { email, ws, token, customer } = await priceSetup(page, 'hp7b')
    const cases: Array<{ tag: string, amount: string, from: string, to: string, unitsText: string, totalText: string }> = [
      { tag: 'big', amount: '9007199254740993', from: '2047-01-01T00:00:00Z', to: '2047-01-02T00:00:00Z', unitsText: '1 unit 24 jam', totalText: 'Total: 9007199254740993 unit tarif · IDR' },
      { tag: 'zero', amount: '0', from: '2047-02-01T00:00:00Z', to: '2047-02-02T00:00:00Z', unitsText: '1 unit 24 jam', totalText: 'Total: 0 unit tarif · IDR' },
      { tag: 'max', amount: '9223372036854775807', from: '2047-03-01T00:00:00Z', to: '2047-03-02T00:00:00Z', unitsText: '1 unit 24 jam', totalText: 'Total: 9223372036854775807 unit tarif · IDR' },
      { tag: 'micro', amount: '150000', from: '2047-04-01T00:00:00Z', to: '2047-04-02T00:00:00.000001Z', unitsText: '2 unit 24 jam', totalText: 'Total: 300000 unit tarif · IDR' }
    ]
    await uiLogin(page, email)
    for (let i = 0; i < cases.length; i++) {
      const c = cases[i]
      await seedItems(page.request, ws, token, 1, `HP7${c.tag}`)
      const item = await catalogItemBySku(page, ws, `HP7${c.tag}-1`, email)
      await seedPriceOne(page.request, ws, token, item.id, c.from.slice(0, 8) + '01T00:00:00Z', '2047-12-01T00:00:00Z', c.amount)
      const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, c.from, c.to)
      await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
      await expect(page.getByText(c.unitsText)).toBeVisible()
      await expect(page.getByText(c.totalText)).toBeVisible()
      await page.getByRole('button', { name: 'Catat harga ini' }).click()
      await page.getByRole('button', { name: 'Catat harga ini' }).click()
      await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
      const fact = await getFact(page, ws, draft.id, email)
      expect(fact.status).toBe(200)
      expect(fact.body.total_minor).toBe(c.totalText.split(': ')[1].split(' unit')[0])
    }
  })

  test('harga draf: GET fakta mengonfirmasi attempt vs fakta kunci-lain vs fakta cacat', async ({ page }) => {
    // (a) Respons hilang, lalu muat-ulang GET-only mengonfirmasi (tanpa kirim-ulang).
    const a = await priceSetup(page, 'hp8a')
    await seedPriceOne(page.request, a.ws, a.token, a.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftA = await seedDraftOne(page.request, a.ws, a.token, a.customer.id, a.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
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
    await uiLogin(page, a.email)
    await gotoAuthed(page, `/workspaces/${a.ws}/rental-prices/${draftA.id}`, a.email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await page.getByRole('alert').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    expect(countAudits(a.ws, 'agreed_price_recorded')).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
    // (b) Fakta kunci-lain: attempt mati, kirim-ulang hilang, fakta tampil.
    // POST pertama di-ABORT tanpa fetch: permintaan tidak pernah sampai
    // ke server, jadi tidak ada fakta yang terkomit — pencatatan API
    // dengan kunci lain lalu deterministik 201 (bukan 409 fakta-sudah-ada).
    const b = await priceSetup(page, 'hp8b')
    await seedPriceOne(page.request, b.ws, b.token, b.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftB = await seedDraftOne(page.request, b.ws, b.token, b.customer.id, b.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttempts++
          if (postAttempts === 1) {
            await route.abort()
            return
          }
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-prices/${draftB.id}`, b.email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    const quoteB = await getQuote(page, b.ws, draftB.id, b.email)
    const other = await acceptPrice(page, b.ws, draftB.id, b.email, randomUUID(), quoteB.body.price_version_id, quoteB.body.total_minor)
    expect(other.status).toBe(201)
    await page.getByRole('alert').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    expect(countAudits(b.ws, 'agreed_price_recorded')).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
    // (c) Fakta cacat (draf salah): ketidakpastian dipertahankan + galat tampil.
    const c = await priceSetup(page, 'hp8c')
    await seedPriceOne(page.request, c.ws, c.token, c.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftC = await seedDraftOne(page.request, c.ws, c.token, c.customer.id, c.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    // Rute dipasang SESUDAH halaman termuat: bacaan fakta awal harus
    // nyata (404 tanpa-fakta) supaya konfirmasi tersedia; fulfill cacat
    // hanya boleh meracuni bacaan SETELAH attempt berjalan.
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-prices/${draftC.id}`, c.email)
    postAttempts = 0
    // Fulfill cacat hanya sesudah POST pertama dilepas: bacaan fakta awal
    // (sebelum attempt) harus terus ke server nyata, bukan diracuni.
    let factPoisoned = false
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttempts++
          if (postAttempts === 1) {
            factPoisoned = true
            await route.fetch()
            await route.abort()
            return
          }
        }
        if (factPoisoned && route.request().method() === 'GET' && u.pathname.endsWith('/agreed-price')) {
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
            id: randomUUID(), workspace_id: c.ws, rental_contract_id: '00000000-0000-4000-8000-000000000099',
            price_version_id: randomUUID(), pricing_rule_version_id: 'elapsed_24h_ceil_v1', catalog_item_id: c.item.id,
            customer_id: c.customer.id, client_request_id: randomUUID(), amount_minor: '150000', currency: 'IDR',
            units: 2, total_minor: '300000', starts_at: '2047-01-01T00:00:00Z', ends_at: '2047-01-03T00:00:00Z',
            accepted_at: '2047-01-01T00:00:00Z', accepted_by: randomUUID()
          }) })
          return
        }
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await page.getByRole('alert').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Fakta yang dibaca tidak cocok dengan permintaan ini; pencatatan tetap tidak diketahui.')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf: snapshot tidak berubah oleh refresh; batal hanya pra-POST; retirement terkunci selama POST', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp9')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    // (a) Quote baru saat refresh: pemberitahuan tampil, snapshot tetap 300000.
    // Injeksi harus konsisten secara internal (total = tarif × unit) —
    // penawaran cacat ditolak halaman sebagai bacaan tidak valid, bukan
    // ditampilkan sebagai harga. Di sini tarif berubah 150000 → 999999.
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/quote')) {
          const real = await route.fetch()
          const body = await real.json() as QuoteRow
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ ...body, amount_minor: '999999', total_minor: '1999998' }) })
          return
        }
        await route.continue()
      }
    )
    await page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByText('Penawaran berubah sejak konfirmasi dibuka.')).toBeVisible()
    await expect(page.getByText('Penawaran terbaru: 1999998 unit tarif · IDR')).toBeVisible()
    await expect(page.getByText('Total: 300000 unit tarif · IDR').first()).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // (b) Batal pra-POST membuang konfirmasi; panel dapat dibuka lagi lalu 201.
    await page.getByRole('button', { name: 'Batalkan' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toHaveCount(0)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    // (c) Retirement terkunci selama POST kirim-ulang berjalan.
    let postAttempts = 0
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttempts++
          if (postAttempts === 1) {
            await route.fetch()
            await route.abort()
            return
          }
          if (postAttempts === 2) {
            await new Promise(r => setTimeout(r, 2500))
            await route.continue()
            return
          }
        }
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    const resend = page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })
    const retire = page.getByRole('button', { name: 'Mulai pencatatan baru' })
    await resend.click()
    await expect(retire).toBeDisabled()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf: navigasi + fokus keyboard 44px di 360/768/1440', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp10')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
      const entry = page.getByRole('link', { name: 'Lihat harga' })
      await expect(entry).toBeVisible()
      const entryBox = await entry.boundingBox()
      expect(entryBox).not.toBeNull()
      expect(entryBox!.height).toBeGreaterThanOrEqual(44)
      expect(entryBox!.width).toBeGreaterThanOrEqual(44)
      await entry.click()
      await expect(page).toHaveURL(`/workspaces/${ws}/rental-prices/${draft.id}`)
      const back = page.getByRole('link', { name: 'Kembali ke Draf sewa' })
      const backBox = await back.boundingBox()
      expect(backBox).not.toBeNull()
      expect(backBox!.height).toBeGreaterThanOrEqual(44)
      expect(backBox!.width).toBeGreaterThanOrEqual(44)
      await back.focus()
      await page.keyboard.press('Tab')
      await expect(page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' })).toBeFocused()
      const record = page.getByRole('button', { name: 'Catat harga ini' })
      const recordBox = await record.boundingBox()
      expect(recordBox).not.toBeNull()
      expect(recordBox!.height).toBeGreaterThanOrEqual(44)
      await record.click()
      await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeFocused()
      const cancelBox = await page.getByRole('button', { name: 'Batalkan' }).boundingBox()
      expect(cancelBox).not.toBeNull()
      expect(cancelBox!.height).toBeGreaterThanOrEqual(44)
      await page.getByRole('button', { name: 'Batalkan' }).click()
    }
  })

  test('harga draf: tautan lanjut login menolak URL luar (tanpa open redirect)', async ({ page }) => {
    const email = `hp11-redirect-${RUN}@e2e.umnia.local`
    await uiRegister(page, email)
    for (const next of ['https://evil.example/', '//evil.example/']) {
      await page.goto(`/login?next=${encodeURIComponent(next)}`)
      await page.getByLabel('Email').fill(email)
      await page.getByLabel('Kata sandi').fill(PASSWORD)
      await page.locator('form button[type="submit"]').click()
      await expect(page).toHaveURL(/\/workspaces\/new/)
      await expect(page.getByRole('heading', { name: 'Buat workspace' })).toBeVisible()
    }
  })

  test('harga draf: fakta tetap terbaca sesudah modul dinonaktifkan; tulis diblokir', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'hp12')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-03-01T00:00:00Z', '150000')
    const draftA = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    const draftB = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-05T00:00:00Z', '2047-01-07T00:00:00Z')
    const quoteA = await getQuote(page, ws, draftA.id, email)
    const recorded = await acceptPrice(page, ws, draftA.id, email, randomUUID(), quoteA.body.price_version_id, quoteA.body.total_minor)
    expect(recorded.status).toBe(201)
    psql(`UPDATE module_installations SET status='inactive', deactivated_at=now() WHERE workspace_id='${ws}' AND module_id='rental.basic'`)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftA.id}`, email)
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await expect(page.getByText('Tarif: 150000 unit tarif · IDR per 24 jam')).toBeVisible()
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draftB.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('alert')).toContainText('rental belum aktif di workspace ini')
    const denied = await acceptPrice(page, ws, draftB.id, email, randomUUID(), (await getQuote(page, ws, draftB.id, email)).body.price_version_id, '300000')
    expect(denied.status).toBe(403)
    expect((await getFact(page, ws, draftB.id, email)).status).toBe(404)
  })

  test('screenshot f2-ui-05: tiga utama + tanpa-fakta/konfirmasi + tanpa-izin + tak-diketahui + muat + galat + manifest', async ({ page }) => {
    mkdirSync(SHOTS_UI05, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI05, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI05, name))).digest('hex')
    const { email, ws, token, customer, item } = await priceSetup(page, 'shot')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    const quote = await getQuote(page, ws, draft.id, email)
    const recorded = await acceptPrice(page, ws, draft.id, email, randomUUID(), quote.body.price_version_id, quote.body.total_minor)
    expect(recorded.status).toBe(201)
    const sizes = [
      { width: 360, height: 640 },
      { width: 768, height: 1024 },
      { width: 1440, height: 900 }
    ]
    for (const size of sizes) {
      await page.setViewportSize(size)
      await uiLogin(page, email)
      await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
      await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
      await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
      await shot('01-price', 'recorded-fact-live', 'live', size.width)
    }
    await page.setViewportSize({ width: 360, height: 640 })
    const nofactSetup = await priceSetup(page, 'shot-nf')
    await seedPriceOne(page.request, nofactSetup.ws, nofactSetup.token, nofactSetup.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftNf = await seedDraftOne(page.request, nofactSetup.ws, nofactSetup.token, nofactSetup.customer.id, nofactSetup.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await gotoAuthed(page, `/workspaces/${nofactSetup.ws}/rental-prices/${draftNf.id}`, nofactSetup.email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    await shot('04-price-nofact', 'nofact-confirmation-live', 'live', 360)
    // Non-anggota baru per tes (bukan otherEmail bersama): bisa jalan
    // sendiri lewat --grep maupun dalam suite penuh.
    const nomemberEmail = `hp5-bukan-anggota-${RUN}@e2e.umnia.local`
    await uiRegister(page, nomemberEmail)
    await uiLogin(page, nomemberEmail)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, nomemberEmail)
    await expect(page.getByRole('alert')).toContainText('bukan anggota workspace ini')
    await shot('05-price-noperm', 'no-permission-live', 'live', 360)
    await uiLogin(page, nofactSetup.email)
    // Tak diketahui: POST sampai ke API nyata lalu responsnya hilang.
    await gotoAuthed(page, `/workspaces/${nofactSetup.ws}/rental-prices/${draftNf.id}`, nofactSetup.email)
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          await route.fetch()
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await shot('06-price-unknown', 'unknown-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Muat: respons ditahan 2,5 dtk, tangkap status memuat.
    await page.route(
      (url) => url.pathname.startsWith('/api/') && url.pathname.endsWith('/quote'),
      async (route) => {
        await new Promise(r => setTimeout(r, 2500))
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${nofactSetup.ws}/rental-prices/${draftNf.id}`, nofactSetup.email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('07-price-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // Galat: respons GET dibatalkan, tangkap halaman galat. POST login
    // tetap diteruskan supaya gotoAuthed bisa masuk.
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        if (route.request().method() === 'GET') {
          await route.abort()
          return
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${nofactSetup.ws}/rental-prices/${draftNf.id}`, nofactSetup.email)
    await expect(page.getByText('Layanan tidak tersedia').first()).toBeVisible()
    await shot('08-price-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    const withSums = manifest.map(m => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI05, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-05', files: withSums }, null, 2) + '\n')
    expect(withSums.length).toBe(8)
  })

  test('harga draf R1-1A: fakta kunci-sama yang tidak cocok tidak dipromosikan; GET cocok kemudian menyelesaikan', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'r1a')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '100')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    // POST pertama hilang (fetch+abort); kunci ditangkap dari header.
    // GET fakta sesudahnya dipenuhi kandidat kunci-sama yang tidak cocok
    // (tarif 999 × 2 unit = 1998, aritmetika valid) — BUKAN fakta cacat.
    let postAttempts = 0
    let capturedKey = ''
    let craftedVersionId = ''
    let sameKeyPoison = false
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttempts++
          if (postAttempts === 1) {
            capturedKey = route.request().headers()['idempotency-key'] ?? ''
            sameKeyPoison = true
            await route.fetch()
            await route.abort()
            return
          }
        }
        if (sameKeyPoison && craftedVersionId !== '' && route.request().method() === 'GET' && u.pathname.endsWith('/agreed-price')) {
          await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
            id: randomUUID(), workspace_id: ws, rental_contract_id: draft.id,
            price_version_id: craftedVersionId, pricing_rule_version_id: 'elapsed_24h_ceil_v1', catalog_item_id: item.id,
            customer_id: customer.id, client_request_id: capturedKey, amount_minor: '999', currency: 'IDR',
            units: 2, total_minor: '1998', starts_at: '2047-01-01T00:00:00Z', ends_at: '2047-01-03T00:00:00Z',
            accepted_at: '2047-01-01T00:00:00Z', accepted_by: randomUUID()
          }) })
          return
        }
        await route.continue()
      }
    )
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    const quote = await getQuote(page, ws, draft.id, email)
    craftedVersionId = quote.body.price_version_id
    await page.getByRole('alert').getByRole('button', { name: 'Muat ulang harga' }).click()
    // Kandidat kunci-sama yang tidak cocok: ketidakpastian dipertahankan,
    // ringkasan/kunci asli selamat, kandidat TIDAK tampil sebagai fakta.
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Fakta yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; pencatatan tetap tidak diketahui.')).toBeVisible()
    await expect(page.getByText('Harga yang dicatat: 1998 unit tarif · IDR')).toHaveCount(0)
    await expect(page.getByRole('alert').getByText('Total: 200 unit tarif · IDR')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // GET asli berikutnya mengembalikan fakta cocok (kunci sama, total 200).
    await page.getByRole('alert').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    await expect(page.getByText('Harga yang dicatat: 200 unit tarif · IDR (2 × 24 jam @ 100 unit tarif · IDR)')).toBeVisible()
    expect(countAudits(ws, 'agreed_price_recorded')).toBe(1)
  })

  test('harga draf R1-1B: GET menyelesaikan attempt memagari POST lama; tanpa panel pemulihan', async ({ page }) => {
    // (a) Fakta kunci-lain menyelesaikan attempt; POST lama dilepas 500.
    const a = await priceSetup(page, 'r1b-a')
    await seedPriceOne(page.request, a.ws, a.token, a.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftA = await seedDraftOne(page.request, a.ws, a.token, a.customer.id, a.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    let postAttemptsA = 0
    let releasePostA!: () => void
    const postHeldA = new Promise<void>((resolve) => { releasePostA = resolve })
    let resolvePostDispatchedA!: () => void
    const postDispatchedA = new Promise<void>((resolve) => { resolvePostDispatchedA = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttemptsA++
          if (postAttemptsA === 1) {
            resolvePostDispatchedA()
            await postHeldA
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'kesalahan server' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await uiLogin(page, a.email)
    await gotoAuthed(page, `/workspaces/${a.ws}/rental-prices/${draftA.id}`, a.email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await postDispatchedA
    const quoteA = await getQuote(page, a.ws, draftA.id, a.email)
    const otherA = await acceptPrice(page, a.ws, draftA.id, a.email, randomUUID(), quoteA.body.price_version_id, quoteA.body.total_minor)
    expect(otherA.status).toBe(201)
    await page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    releasePostA()
    await page.waitForTimeout(1000)
    // POST lama 500 tidak boleh membangkitkan panel pemulihan tanpa attempt.
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toHaveCount(0)
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    expect(postAttemptsA).toBe(1)
    expect(countAudits(a.ws, 'agreed_price_recorded')).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
    // (b) Fakta kunci-sama menyelesaikan attempt; POST lama dilepas 500.
    const b = await priceSetup(page, 'r1b-b')
    await seedPriceOne(page.request, b.ws, b.token, b.item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draftB = await seedDraftOne(page.request, b.ws, b.token, b.customer.id, b.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    let postAttemptsB = 0
    let capturedKeyB = ''
    let releasePostB!: () => void
    const postHeldB = new Promise<void>((resolve) => { releasePostB = resolve })
    let resolvePostDispatchedB!: () => void
    const postDispatchedB = new Promise<void>((resolve) => { resolvePostDispatchedB = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttemptsB++
          if (postAttemptsB === 1) {
            capturedKeyB = route.request().headers()['idempotency-key'] ?? ''
            resolvePostDispatchedB()
            await postHeldB
            await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'kesalahan server' }) })
            return
          }
        }
        await route.continue()
      }
    )
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-prices/${draftB.id}`, b.email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await postDispatchedB
    const quoteB = await getQuote(page, b.ws, draftB.id, b.email)
    const sameB = await acceptPrice(page, b.ws, draftB.id, b.email, capturedKeyB, quoteB.body.price_version_id, quoteB.body.total_minor)
    expect(sameB.status).toBe(201)
    await page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    releasePostB()
    await page.waitForTimeout(1000)
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil pencatatan tidak diketahui' })).toHaveCount(0)
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    expect(postAttemptsB).toBe(1)
    expect(countAudits(b.ws, 'agreed_price_recorded')).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf R1-2a: konfirmasi baru diblokir selama GET fakta ditahan', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'r1c-a')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(1)
    // GET fakta refresh ditahan; bacaan belum selesai = bukan tanpa-fakta.
    let factHeldCount = 0
    let releaseFact!: () => void
    const factHeld = new Promise<void>((resolve) => { releaseFact = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/agreed-price')) {
          factHeldCount++
          await factHeld
        }
        await route.continue()
      }
    )
    await page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect.poll(() => factHeldCount, { timeout: 10000 }).toBe(1)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(0)
    releaseFact()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toHaveCount(1)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('harga draf R1-2b: tulis panel diblokir selama refresh ditahan; tanpa POST', async ({ page }) => {
    const { email, ws, token, customer, item } = await priceSetup(page, 'r1c-b')
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-prices/${draft.id}`, email)
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pencatatan harga' })).toBeVisible()
    // Refresh ditahan di GET fakta; tombol tulis panel nonaktif dan
    // handler menolak kiriman pertama selama bacaan belum selesai.
    let factHeldCount = 0
    let postAttempts = 0
    let releaseFact!: () => void
    const factHeld = new Promise<void>((resolve) => { releaseFact = resolve })
    await page.route(
      (url) => url.pathname.startsWith('/api/'),
      async (route) => {
        const u = new URL(route.request().url())
        if (route.request().method() === 'GET' && u.pathname.endsWith('/agreed-price')) {
          factHeldCount++
          await factHeld
        }
        if (route.request().method() === 'POST' && u.pathname.endsWith('/accept-price')) {
          postAttempts++
        }
        await route.continue()
      }
    )
    await page.locator('section[aria-labelledby="quote-heading"]').getByRole('button', { name: 'Muat ulang harga' }).click()
    await expect.poll(() => factHeldCount, { timeout: 10000 }).toBe(1)
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toBeDisabled()
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.trim() === 'Catat harga ini')
      btn?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(0)
    releaseFact()
    await expect(page.getByRole('button', { name: 'Catat harga ini' })).toBeEnabled()
    await page.getByRole('button', { name: 'Catat harga ini' }).click()
    await expect(page.getByRole('heading', { name: 'Harga tercatat' })).toBeVisible()
    expect(postAttempts).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima: tautan Serah terima membuka halaman custody; hold → handover 201 → custody out → return 200 terminal', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'happy')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await page.getByRole('link', { name: 'Serah terima' }).click()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/rental-custody/${draft.id}`))
    await expect(page.locator('h1')).toBeVisible()
    await expect(page.getByText('Belum ada serah terima untuk draf ini.')).toBeVisible()
    await expect(page.getByText('Harga yang dicatat: 300000 unit tarif · IDR (2 × 24 jam @ 150000 unit tarif · IDR)')).toBeVisible()
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pembuatan hold' })).toBeVisible()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi serah terima' })).toBeVisible()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Utuh').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pengembalian' })).toBeVisible()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
    const got = await getCustody(page, ws, draft.id, email)
    expect(got.status).toBe(200)
    const c = got.body as CustodyRow
    expect(c.status).toBe('returned')
    expect(c.returned_at).not.toBeNull()
    expect(c.resolved_at).not.toBeNull()
    expect(c.returned_by).toBeTruthy()
    expect(countAudits(ws, 'rental_handed_over')).toBe(1)
    expect(countAudits(ws, 'rental_returned')).toBe(1)
    const holds = await getHolds(page, ws, email)
    expect(holds.status).toBe(200)
    const held = (holds.body.items as HoldRow[]).find(h => h.asset_id === asset.id)
    expect(held?.status).toBe('consumed')
  })

  test('serah terima: return Rusak mengubah kondisi aset; return Hilang memakai resolved', async ({ page }) => {
    // Rusak: unit kembali dengan kondisi rusak; resolved terisi.
    const a = await seedCustodyReady(page, 'rusak')
    const holdA = await createHold(page, a.ws, a.draft.id, a.email, randomUUID(), a.asset.id)
    expect(holdA.status).toBe(201)
    const handA = await handoverCustody(page, a.ws, a.draft.id, a.email, randomUUID(), a.asset.id, (holdA.body as HoldRow).id)
    expect(handA.status).toBe(201)
    await uiLogin(page, a.email)
    await gotoAuthed(page, `/workspaces/${a.ws}/rental-custody/${a.draft.id}`, a.email)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Rusak').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Dikembalikan rusak')).toBeVisible()
    expect(psql(`SELECT condition FROM physical_assets WHERE id='${a.asset.id}'`).trim()).toBe('damaged')
    expect(countAudits(a.ws, 'rental_returned_damaged')).toBe(1)
    // Hilang: returned_* tetap null; resolved_* terisi.
    const b = await seedCustodyReady(page, 'hilang')
    const holdB = await createHold(page, b.ws, b.draft.id, b.email, randomUUID(), b.asset.id)
    expect(holdB.status).toBe(201)
    const handB = await handoverCustody(page, b.ws, b.draft.id, b.email, randomUUID(), b.asset.id, (holdB.body as HoldRow).id)
    expect(handB.status).toBe(201)
    await uiLogin(page, b.email)
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-custody/${b.draft.id}`, b.email)
    await page.getByLabel('Hilang').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Dinyatakan hilang')).toBeVisible()
    const gotB = await getCustody(page, b.ws, b.draft.id, b.email)
    expect(gotB.status).toBe(200)
    const cb = gotB.body as CustodyRow
    expect(cb.status).toBe('lost')
    expect(cb.returned_at).toBeNull()
    expect(cb.returned_by).toBeNull()
    expect(cb.resolved_at).not.toBeNull()
    expect(cb.resolved_by).toBeTruthy()
    expect(psql(`SELECT condition FROM physical_assets WHERE id='${b.asset.id}'`).trim()).toBe('lost')
    expect(countAudits(b.ws, 'rental_loss_declared')).toBe(1)
  })

  test('serah terima: replay identik hold/handover/return; kunci dipakai ulang 409; tanpa audit ganda', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'replay')
    const holdKey = randomUUID()
    const hold1 = await createHold(page, ws, draft.id, email, holdKey, asset.id)
    expect(hold1.status).toBe(201)
    const holdId = (hold1.body as HoldRow).id
    const holdReplay = await createHold(page, ws, draft.id, email, holdKey, asset.id)
    expect(holdReplay.status).toBe(200)
    expect((holdReplay.body as HoldRow).id).toBe(holdId)
    const holdMisuse = await createHold(page, ws, draft.id, email, holdKey, randomUUID())
    expect(holdMisuse.status).toBe(409)
    expect(holdMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const handKey = randomUUID()
    const hand1 = await handoverCustody(page, ws, draft.id, email, handKey, asset.id, holdId)
    expect(hand1.status).toBe(201)
    const custodyId = (hand1.body as CustodyRow).id
    const handReplay = await handoverCustody(page, ws, draft.id, email, handKey, asset.id, holdId)
    expect(handReplay.status).toBe(200)
    expect((handReplay.body as CustodyRow).id).toBe(custodyId)
    const handMisuse = await handoverCustody(page, ws, draft.id, email, handKey, asset.id, randomUUID())
    expect(handMisuse.status).toBe(409)
    expect(handMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const retKey = randomUUID()
    const ret1 = await returnCustody(page, ws, draft.id, email, retKey, 'returned')
    expect(ret1.status).toBe(200)
    expect((ret1.body as CustodyRow).id).toBe(custodyId)
    const retReplay = await returnCustody(page, ws, draft.id, email, retKey, 'returned')
    expect(retReplay.status).toBe(200)
    expect((retReplay.body as CustodyRow).id).toBe(custodyId)
    const retMisuse = await returnCustody(page, ws, draft.id, email, retKey, 'damaged')
    expect(retMisuse.status).toBe(409)
    expect(retMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    expect(countAudits(ws, 'rental_handed_over')).toBe(1)
    expect(countAudits(ws, 'rental_returned')).toBe(1)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
  })

  test('serah terima: kasir baca custody; tulis 403; tanpa fakta handover 409; hold kedaluwarsa/salah 409', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'izin')
    const cashierEmail = `kasir-cu-${RUN}@e2e.umnia.local`
    const code = await uiInvite(page, ws, cashierEmail, 'cashier', email)
    await uiRegister(page, cashierEmail)
    await uiLogin(page, cashierEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${ws}`, cashierEmail)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/members`))
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, cashierEmail)
    await expect(page.locator('h1')).toBeVisible()
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('tidak punya izin mengelola data usaha')).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/rental-custody/${draft.id}`))
    // Tanpa fakta: handover diblokir di halaman; server menolak 409.
    const nf = await custodySetup(page, 'tanpafakta')
    const draftNf = await seedDraftOne(page.request, nf.ws, nf.token, nf.customer.id, nf.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await uiLogin(page, nf.email)
    await gotoAuthed(page, `/workspaces/${nf.ws}/rental-custody/${draftNf.id}`, nf.email)
    await expect(page.getByText('Belum ada harga yang dicatat untuk draf ini.')).toBeVisible()
    expect(await page.getByRole('button', { name: 'Buat hold' }).count()).toBe(0)
    const holdNf = await createHold(page, nf.ws, draftNf.id, nf.email, randomUUID(), nf.asset.id)
    expect(holdNf.status).toBe(201)
    const handNf = await handoverCustody(page, nf.ws, draftNf.id, nf.email, randomUUID(), nf.asset.id, (holdNf.body as HoldRow).id)
    expect(handNf.status).toBe(409)
    expect(handNf.body.error).toBe('harga yang disepakati belum dicatat')
    // Hold kedaluwarsa: status efektif expired; handover 409.
    const holdExp = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(holdExp.status).toBe(201)
    psql(`UPDATE rental_holds SET expires_at = now() - interval '1 hour' WHERE id='${(holdExp.body as HoldRow).id}'`)
    const handExp = await handoverCustody(page, ws, draft.id, email, randomUUID(), asset.id, (holdExp.body as HoldRow).id)
    expect(handExp.status).toBe(409)
    expect(handExp.body.error).toBe('hold tidak aktif')
    // Hold salah: hold milik aset lain dipakai untuk aset ini.
    await seedAssets(page.request, ws, token, item.id, 1, 'CUsalah')
    const assets = await listAssetRows(page, ws, email)
    const otherAsset = assets.find(a => a.id !== asset.id)
    if (!otherAsset) throw new Error('aset kedua tidak ditemukan')
    const holdWrong = await createHold(page, ws, draft.id, email, randomUUID(), otherAsset.id)
    expect(holdWrong.status).toBe(201)
    const handWrong = await handoverCustody(page, ws, draft.id, email, randomUUID(), asset.id, (holdWrong.body as HoldRow).id)
    expect(handWrong.status).toBe(409)
    expect(handWrong.body.error).toBe('hold tidak cocok dengan draft atau aset')
  })

  test('serah terima: return sesudah deactivate tetap 200; handover sesudah deactivate 403', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'nonaktif')
    // Draf kedua + fakta disiapkan SEBELUM deactivate (accept butuh rental aktif).
    await seedPriceOne(page.request, ws, token, item.id, '2047-01-01T00:00:00Z', '2047-02-01T00:00:00Z', '150000')
    const draft2 = await seedDraftOne(page.request, ws, token, customer.id, item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    const quote2 = await getQuote(page, ws, draft2.id, email)
    expect(quote2.status).toBe(200)
    const accepted2 = await acceptPrice(page, ws, draft2.id, email, randomUUID(), (quote2.body as QuoteRow).price_version_id, '300000')
    expect(accepted2.status).toBe(201)
    const hold1 = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold1.status).toBe(201)
    const hand1 = await handoverCustody(page, ws, draft.id, email, randomUUID(), asset.id, (hold1.body as HoldRow).id)
    expect(hand1.status).toBe(201)
    psql(`UPDATE module_installations SET status='inactive', deactivated_at=now() WHERE workspace_id='${ws}' AND module_id='rental.basic'`)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Utuh').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
    const got = await getCustody(page, ws, draft.id, email)
    expect(got.status).toBe(200)
    expect((got.body as CustodyRow).status).toBe('returned')
    // Handover draf kedua sesudah deactivate: guard menyempit TIDAK berlaku
    // untuk handover — server menolak 403, dan UI menampilkan penolakan itu.
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft2.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('rental belum aktif di workspace ini')).toBeVisible()
  })

  test('serah terima: respons hilang ketiga stage lalu kirim-ulang kunci sama 200/201', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'hilang-resp')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    // Hold hilang.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) await route.abort()
      else await route.continue()
    })
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    // Handover hilang.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) await route.abort()
      else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    // Return hilang.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/return')) await route.abort()
      else await route.continue()
    })
    await page.getByLabel('Utuh').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pengembalian tidak diketahui' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
    expect(countAudits(ws, 'rental_handed_over')).toBe(1)
    expect(countAudits(ws, 'rental_returned')).toBe(1)
  })

  test('serah terima: sukses malformed tetap tidak diketahui; fakta kunci-sama tak cocok tidak dipromosikan', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft, fact } = await seedCustodyReady(page, 'cacat')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const holdId = (hold.body as HoldRow).id
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    // Sukses 201 malformed (DTO kosong) bukan bukti tercatat.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        await route.fulfill({ status: 201, contentType: 'application/json', body: '{}' })
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Unit sedang dipinjam')).toHaveCount(0)
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    // Fakta kunci-sama tak cocok: GET custody dipalsukan dengan kunci
    // attempt yang sama tetapi aset berbeda — tidak boleh dipromosikan.
    const b = await seedCustodyReady(page, 'cacat2')
    const holdB = await createHold(page, b.ws, b.draft.id, b.email, randomUUID(), b.asset.id)
    expect(holdB.status).toBe(201)
    const holdBId = (holdB.body as HoldRow).id
    await uiLogin(page, b.email)
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-custody/${b.draft.id}`, b.email)
    let capturedKey = ''
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        capturedKey = route.request().headers()['idempotency-key'] ?? ''
        await route.abort()
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    expect(capturedKey).not.toBe('')
    await page.unrouteAll({ behavior: 'wait' })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/custody')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
          id: randomUUID(), workspace_id: b.ws, rental_contract_id: b.draft.id,
          rental_agreed_price_id: b.fact.id, asset_id: randomUUID(), hold_id: holdBId,
          client_request_id: capturedKey, return_client_request_id: null, status: 'out',
          handed_out_at: '2047-01-02T00:00:00Z', returned_at: null, handed_out_by: randomUUID(),
          returned_by: null, resolved_at: null, resolved_by: null
        }) })
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('kunci yang sama tetapi tidak cocok')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Unit sedang dipinjam')).toHaveCount(0)
    await page.unrouteAll({ behavior: 'wait' })
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
  })

  test('serah terima: GET menyelesaikan handover memagari POST lama; tanpa panel pemulihan', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft, fact } = await seedCustodyReady(page, 'pagar')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const holdId = (hold.body as HoldRow).id
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    let capturedKey = ''
    let releasePost!: () => void
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        capturedKey = route.request().headers()['idempotency-key'] ?? ''
        await postHeld
        await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'transaksi gagal' }) })
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect.poll(() => capturedKey, { timeout: 10000 }).not.toBe('')
    // GET custody dipalsukan dengan fakta yang cocok: settlement otoritatif.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/custody')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({
          id: randomUUID(), workspace_id: ws, rental_contract_id: draft.id,
          rental_agreed_price_id: fact.id, asset_id: asset.id, hold_id: holdId,
          client_request_id: capturedKey, return_client_request_id: null, status: 'out',
          handed_out_at: '2047-01-02T00:00:00Z', returned_at: null, handed_out_by: randomUUID(),
          returned_by: null, resolved_at: null, resolved_by: null
        }) })
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toHaveCount(0)
    // POST lama akhirnya 500 — sudah dipagari, panel pemulihan tidak muncul.
    releasePost()
    await page.waitForTimeout(800)
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toHaveCount(0)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima: tulis pertama diblokir selama bacaan ditahan; return tetap bisa saat bacaan konteks gagal', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'siap-baca')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const hand = await handoverCustody(page, ws, draft.id, email, randomUUID(), asset.id, (hold.body as HoldRow).id)
    expect(hand.status).toBe(201)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Utuh').check()
    // Refresh ditahan di GET fakta; tombol tulis panel nonaktif dan
    // handler menolak kiriman pertama selama bacaan belum selesai.
    let factHeldCount = 0
    let postAttempts = 0
    let releaseFact!: () => void
    const factHeld = new Promise<void>((resolve) => { releaseFact = resolve })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/agreed-price')) {
        factHeldCount++
        await factHeld
      }
      if (route.request().method() === 'POST' && u.pathname.endsWith('/return')) postAttempts++
      await route.continue()
    })
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect.poll(() => factHeldCount, { timeout: 10000 }).toBe(1)
    await expect(page.getByRole('button', { name: 'Kembalikan unit' })).toBeDisabled()
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.trim() === 'Kembalikan unit')
      btn?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(postAttempts).toBe(0)
    releaseFact()
    await expect(page.getByRole('button', { name: 'Kembalikan unit' })).toBeEnabled()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pengembalian' })).toBeVisible()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
    expect(postAttempts).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
    // Bacaan konteks (fakta/aset/hold) gagal total — return tetap bisa.
    const b = await seedCustodyReady(page, 'konteks-gagal')
    const holdB = await createHold(page, b.ws, b.draft.id, b.email, randomUUID(), b.asset.id)
    expect(holdB.status).toBe(201)
    const handB = await handoverCustody(page, b.ws, b.draft.id, b.email, randomUUID(), b.asset.id, (holdB.body as HoldRow).id)
    expect(handB.status).toBe(201)
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && (u.pathname.endsWith('/agreed-price') || u.pathname.endsWith('/assets') || u.pathname.endsWith('/rental/holds'))) {
        await route.abort()
      } else await route.continue()
    })
    await uiLogin(page, b.email)
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-custody/${b.draft.id}`, b.email)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Rusak').check()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByText('Dikembalikan rusak')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima: hold di luar 50 pertama butuh paging eksplisit; tanpa rantai otomatis', async ({ page }) => {
    const { email, ws, token, customer, item } = await custodySetup(page, 'paging')
    await seedAssets(page.request, ws, token, item.id, 50, 'CUpage')
    const allAssets = await listAssetRows(page, ws, email)
    expect(allAssets.length).toBe(51)
    await seedDrafts(page.request, ws, token, customer.id, item.id, 51)
    const drafts = await listDraftRows(page, ws, email)
    expect(drafts.length).toBe(51)
    // Satu hold per draf; hold target adalah yang terakhir dibuat.
    for (let i = 0; i < 51; i++) {
      const h = await createHold(page, ws, drafts[i].id, email, randomUUID(), allAssets[i].id)
      expect(h.status, `hold ${i + 1}`).toBe(201)
    }
    const targetDraft = drafts[50]
    const targetAsset = allAssets[50]
    // Tarif lebar (2040–2050) menutupi periode draf ke-51 apa pun; total
    // diambil dari quote agar accept memakai angka server yang sama.
    await seedPriceOne(page.request, ws, token, item.id, '2040-01-01T00:00:00Z', '2050-01-01T00:00:00Z', '150000')
    const quote = await getQuote(page, ws, targetDraft.id, email)
    expect(quote.status).toBe(200)
    const accepted = await acceptPrice(page, ws, targetDraft.id, email, randomUUID(), (quote.body as QuoteRow).price_version_id, (quote.body as QuoteRow).total_minor)
    expect(accepted.status).toBe(201)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${targetDraft.id}`, email)
    await page.getByLabel('Unit').selectOption(targetAsset.id)
    // Halaman pertama (50 baris) tidak memuat hold target; halaman
    // TIDAK otomatis merangkak — tombol eksplisit yang memuat lanjut.
    await expect(page.getByRole('button', { name: 'Muat lebih banyak hold' })).toBeVisible()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    await page.getByRole('button', { name: 'Muat lebih banyak hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toBeEnabled()
  })

  test('serah terima: navigasi + fokus keyboard 44px di 360/768/1440', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'kbd')
    await uiLogin(page, email)
    await page.setViewportSize({ width: 360, height: 640 })
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await expect(page.locator('h1')).toBeVisible()
    // Tunggu navigasi workspace selesai dimuat: h1 tampil selama `Memuat…`,
    // dan Tab dari Keluar hanya mendarat di Anggota bila tautan sudah ada.
    await expect(page.getByRole('link', { name: 'Anggota', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Keluar' }).focus()
    await page.keyboard.press('Tab')
    const focused = await page.evaluate(() => document.activeElement?.textContent)
    expect(focused).toBe('Anggota')
    const outline = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      return el ? getComputedStyle(el).outlineWidth : ''
    })
    expect(outline).toBe('2px')
    for (const size of [{ width: 360, height: 640 }, { width: 768, height: 1024 }, { width: 1440, height: 900 }]) {
      await page.setViewportSize(size)
      await page.waitForTimeout(300)
      const boxes = await page.locator('section[aria-labelledby="custody-heading"] button:visible').evaluateAll((els) =>
        els.map((el) => { const r = (el as HTMLElement).getBoundingClientRect(); return { w: r.width, h: r.height } }))
      expect(boxes.length).toBeGreaterThan(0)
      for (const b of boxes) {
        expect(b.w).toBeGreaterThanOrEqual(44)
        expect(b.h).toBeGreaterThanOrEqual(44)
      }
    }
  })

  test('serah terima: tautan lanjut login menolak URL luar (tanpa open redirect)', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'lanjut')
    const target = `/workspaces/${ws}/rental-custody/${draft.id}`
    await page.goto(`/login?next=${encodeURIComponent(target)}`)
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(new RegExp(target.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    await expect(page.locator('h1')).toBeVisible()
    await uiLogout(page)
    await page.goto('/login?next=https://evil.example/x')
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
    await uiLogout(page)
    await page.goto(`/login?next=/workspaces/${ws}/rental-custody/bukan-uuid`)
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
  })


  test('serah terima R1: hold tak-diketahui terkunci; kirim-ulang kunci sama tanpa kunci baru', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-hold-kunci')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    const seenKeys = new Set<string>()
    let postCount = 0
    let aborted = false
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) {
        postCount++
        const k = route.request().headers()['idempotency-key']
        if (k) seenKeys.add(k)
        // Hanya POST pertama yang digagalkan; kirim-ulang diteruskan
        // supaya tetap terhitung (rute tidak dilepas sebelum kirim-ulang).
        if (!aborted) { aborted = true; await route.abort() }
        else await route.continue()
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toBeVisible()
    // State unknown terkunci: tidak ada jalur "Mulai hold baru".
    await expect(page.getByRole('button', { name: 'Mulai hold baru' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    // Hanya dua POST (asli + kirim-ulang) dengan SATU kunci yang sama.
    expect(postCount).toBe(2)
    expect(seenKeys.size).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R1: handover tak-diketahui terkunci; kirim-ulang kunci sama tanpa kunci baru', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-hand-kunci')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    const seenKeys = new Set<string>()
    let postCount = 0
    let aborted = false
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        postCount++
        const k = route.request().headers()['idempotency-key']
        if (k) seenKeys.add(k)
        if (!aborted) { aborted = true; await route.abort() }
        else await route.continue()
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Mulai serah terima baru' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    expect(postCount).toBe(2)
    expect(seenKeys.size).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R1: return tak-diketahui terkunci; kirim-ulang kunci sama tanpa kunci baru', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-ret-kunci')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const hand = await handoverCustody(page, ws, draft.id, email, randomUUID(), asset.id, (hold.body as HoldRow).id)
    expect(hand.status).toBe(201)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    await page.getByLabel('Utuh').check()
    const seenKeys = new Set<string>()
    let postCount = 0
    let aborted = false
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/return')) {
        postCount++
        const k = route.request().headers()['idempotency-key']
        if (k) seenKeys.add(k)
        if (!aborted) { aborted = true; await route.abort() }
        else await route.continue()
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await page.getByRole('button', { name: 'Kembalikan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pengembalian tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Mulai pengembalian baru' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Sudah dikembalikan')).toBeVisible()
    expect(postCount).toBe(2)
    expect(seenKeys.size).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R1: GET holds menyelesaikan hold hilang; POST lama terpagari', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-hold-get')
    await uiLogin(page, email)
    let capturedKey = ''
    let postCount = 0
    let postCaptured = false
    let releasePost!: () => void
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve })
    const craftedHold = () => ({
      id: randomUUID(), workspace_id: ws, draft_id: draft.id, asset_id: asset.id,
      client_request_id: capturedKey, status: 'held',
      starts_at: draft.starts_at, ends_at: draft.ends_at,
      expires_at: new Date(Date.now() + 24 * 3600 * 1000).toISOString(),
      created_at: new Date().toISOString(), updated_at: new Date().toISOString()
    })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) {
        postCount++
        capturedKey = route.request().headers()['idempotency-key'] ?? ''
        postCaptured = true
        await postHeld
        await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'transaksi gagal' }) })
      } else if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/holds')) {
        if (!postCaptured) await route.continue()
        else await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [craftedHold()] }) })
      } else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect.poll(() => capturedKey, { timeout: 10000 }).not.toBe('')
    // GET baru (Muat ulang data) menyelesaikan attempt tanpa POST kedua.
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toHaveCount(0)
    // POST lama akhirnya 500 — sudah dipagari, panel pemulihan tidak muncul.
    releasePost()
    await page.waitForTimeout(800)
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toHaveCount(0)
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    expect(postCount).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R1: hold hilang dipulihkan lewat pagination manual holds', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-hold-page')
    await uiLogin(page, email)
    let capturedKey = ''
    let postCount = 0
    const filler = () => ({
      id: randomUUID(), workspace_id: ws, draft_id: randomUUID(), asset_id: randomUUID(),
      client_request_id: randomUUID(), status: 'held',
      starts_at: '2047-01-01T00:00:00Z', ends_at: '2047-01-03T00:00:00Z',
      expires_at: '2047-01-02T00:00:00Z',
      created_at: '2047-01-01T00:00:00Z', updated_at: '2047-01-01T00:00:00Z'
    })
    const page1 = Array.from({ length: 50 }, () => filler())
    const page2 = Array.from({ length: 50 }, () => filler())
    const craftedHold = () => ({
      id: randomUUID(), workspace_id: ws, draft_id: draft.id, asset_id: asset.id,
      client_request_id: capturedKey, status: 'held',
      starts_at: draft.starts_at, ends_at: draft.ends_at,
      expires_at: new Date(Date.now() + 24 * 3600 * 1000).toISOString(),
      created_at: new Date().toISOString(), updated_at: new Date().toISOString()
    })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) {
        postCount++
        capturedKey = route.request().headers()['idempotency-key'] ?? ''
        await route.abort()
      } else if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/holds')) {
        const off = Number(u.searchParams.get('offset') ?? '0')
        if (off === 0) await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: page1 }) })
        else if (off === 50) await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: page2 }) })
        else if (off === 100) await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [craftedHold()] }) })
        else await route.continue()
      } else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toBeVisible()
    // Refresh memuat satu halaman berikut (offset 50) tanpa kecocokan:
    // upaya tetap unknown, bukan bukti tidak ada.
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toBeVisible()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    // Pagination manual memuat halaman offset 100 yang berisi hold yang cocok.
    await page.getByRole('button', { name: 'Muat lebih banyak hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil pembuatan hold tidak diketahui' })).toHaveCount(0)
    expect(postCount).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R1: GET custody gagal/cacat memblokir kirim pertama; no-custody membuka kembali', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r1-cust-miss')
    await uiLogin(page, email)
    let custodyMode: 'real' | 'fail500' | 'malformed' = 'fail500'
    let holdPostAttempts = 0
    let handPostAttempts = 0
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/custody')) {
        if (custodyMode === 'fail500') await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
        else if (custodyMode === 'malformed') await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({}) })
        else await route.continue()
      } else if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) {
        holdPostAttempts++
        await route.continue()
      } else if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        handPostAttempts++
        await route.continue()
      } else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    // GET custody gagal: pembuka hold tidak tampil (bukan tanpa-custody).
    await expect(page.getByRole('button', { name: 'Buat hold' })).toHaveCount(0)
    // GET no-custody spesifik berhasil: alur normal dapat dilanjutkan.
    custodyMode = 'real'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeVisible()
    // Panel dibuka, lalu GET custody cacat: tombol konfirmasi terkunci
    // dan handler menolak kiriman pertama (klik sintetis pada tombol
    // nonaktif tetap memicu handler Vue — bukti ada di handler, bukan
    // hanya di atribut disabled).
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pembuatan hold' })).toBeVisible()
    custodyMode = 'malformed'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeDisabled()
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.trim() === 'Buat hold')
      btn?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(holdPostAttempts).toBe(0)
    custodyMode = 'real'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeEnabled()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    expect(holdPostAttempts).toBe(1)
    // Handover: panel dibuka, GET custody gagal, kirim pertama terkunci.
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi serah terima' })).toBeVisible()
    custodyMode = 'fail500'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toBeDisabled()
    await page.evaluate(() => {
      const btn = Array.from(document.querySelectorAll('button')).find(b => b.textContent?.trim() === 'Serahkan unit')
      btn?.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    })
    await page.waitForTimeout(500)
    expect(handPostAttempts).toBe(0)
    custodyMode = 'real'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toBeEnabled()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    expect(handPostAttempts).toBe(1)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R2: GET holds mengganti held menjadi expired lalu released; hold tidak lagi ditawarkan', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r2-hold-recon')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const realHold = hold.body as HoldRow
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toBeVisible()
    // GET terbaru mengembalikan ID yang sama dengan status efektif berubah.
    let holdStatus: 'expired' | 'released' = 'expired'
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/rental/holds')) {
        await route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ items: [{ ...realHold, status: holdStatus }] }) })
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeVisible()
    // Status efektif berubah lagi (released): tetap tidak ditawarkan.
    holdStatus = 'released'
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R2: hold kedaluwarsa ditolak 409; hold pengganti lalu serah terima baru', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r2-hand-409')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const holdId = (hold.body as HoldRow).id
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    // Hold kedaluwarsa sesudah halaman dimuat (bukti server nyata, bukan
    // injeksi): POST handover berikutnya ditolak 409 `hold tidak aktif`.
    psql(`UPDATE rental_holds SET expires_at = now() - interval '1 hour' WHERE id='${holdId}'`)
    const seenHandKeys = new Set<string>()
    let handPostCount = 0
    let holdPostCount = 0
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        handPostCount++
        const k = route.request().headers()['idempotency-key']
        if (k) seenHandKeys.add(k)
        await route.continue()
      } else if (route.request().method() === 'POST' && u.pathname.endsWith('/holds')) {
        holdPostCount++
        await route.continue()
      } else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi serah terima' })).toBeVisible()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    // Penolakan definitif: alasan server tampil, hold lama tidak lagi
    // ditawarkan, panel konfirmasi tertutup, hold pengganti bisa dibuat.
    await expect(page.getByText(/hold tidak aktif/)).toBeVisible()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    await expect(page.getByRole('heading', { name: 'Konfirmasi serah terima' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Serahkan unit' })).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeVisible()
    expect(handPostCount).toBe(1)
    // Hold pengganti lewat UI (konfirmasi pengguna), lalu serah terima baru.
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi pembuatan hold' })).toBeVisible()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    expect(holdPostCount).toBe(1)
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi serah terima' })).toBeVisible()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    // Dua POST handover dengan DUA kunci berbeda: upaya baru, bukan
    // kirim-ulang kunci yang ditolak.
    expect(handPostCount).toBe(2)
    expect(seenHandKeys.size).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('serah terima R2: 409 saat kirim-ulang tidak melepas kunci sebelum GET custody membuktikan', async ({ page }) => {
    const { email, ws, token, customer, item, asset, draft } = await seedCustodyReady(page, 'r2-hand-retry409')
    const hold = await createHold(page, ws, draft.id, email, randomUUID(), asset.id)
    expect(hold.status).toBe(201)
    const holdId = (hold.body as HoldRow).id
    await uiLogin(page, email)
    let custodyMode: 'real' | 'fail500' = 'real'
    let custodyGetCount = 0
    const seenHandKeys = new Set<string>()
    let handPostCount = 0
    let handPhase = 0 // 0 = POST pertama digagalkan, 1-2 = kirim-ulang dijawab 409, 3+ = diteruskan
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/custody')) {
        custodyGetCount++
        if (custodyMode === 'fail500') await route.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ error: 'gagal' }) })
        else await route.continue()
      } else if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) {
        handPostCount++
        const k = route.request().headers()['idempotency-key']
        if (k) seenHandKeys.add(k)
        if (handPhase === 0) { handPhase = 1; await route.abort() }
        else if (handPhase <= 2) { handPhase++; await route.fulfill({ status: 409, contentType: 'application/json', body: JSON.stringify({ error: 'hold tidak aktif' }) }) }
        else await route.continue()
      } else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${ws}/rental-custody/${draft.id}`, email)
    await page.getByLabel('Unit').selectOption(asset.id)
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Mulai serah terima baru' })).toHaveCount(0)
    const custodyGetsAfterLoad = custodyGetCount
    // Fase A: GET custody gagal saat konfirmasi — kunci dipertahankan,
    // panel unknown tetap tampil, tidak ada catatan penolakan. (Alasan
    // server tetap tampil sebagai "Kirim ulang ditolak" di panel unknown;
    // yang diuji di sini adalah catatan pelepasannya belum ada.)
    custodyMode = 'fail500'
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toBeEnabled()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await expect(page.getByText(/Hold lama tidak dapat dipakai lagi/)).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Buat hold' })).toHaveCount(0)
    expect(custodyGetCount).toBeGreaterThan(custodyGetsAfterLoad)
    // Fase B: GET custody spesifik (tanpa custody) + 409 — kombinasi
    // keduanya adalah bukti otoritatif: kunci dilepas, hold lama berhenti
    // ditawarkan, hold pengganti bisa dibuat.
    custodyMode = 'real'
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText(/hold tidak aktif/)).toBeVisible()
    await expect(page.getByText(/Hold lama tidak dapat dipakai lagi/)).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toHaveCount(0)
    await expect(page.getByText('Hold aktif untuk unit ini.')).toHaveCount(0)
    await expect(page.getByRole('button', { name: 'Buat hold' })).toBeVisible()
    // Dua kirim-ulang memakai kunci yang SAMA (satu kunci asli).
    expect(handPostCount).toBe(3)
    expect(seenHandKeys.size).toBe(1)
    // Hold pengganti + serah terima baru dengan kunci baru (hold lama
    // sudah kedaluwarsa di DB supaya lazy expiry mengizinkan pengganti).
    psql(`UPDATE rental_holds SET expires_at = now() - interval '1 hour' WHERE id='${holdId}'`)
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await page.getByRole('button', { name: 'Buat hold' }).click()
    await expect(page.getByText('Hold aktif untuk unit ini.')).toBeVisible()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
    expect(handPostCount).toBe(4)
    expect(seenHandKeys.size).toBe(2)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('screenshot f2-ui-06: tiga utama + tanpa-fakta + tanpa-izin + tak-diketahui + muat + galat + manifest', async ({ page }) => {
    const mkdirSync = (await import('node:fs')).mkdirSync
    const writeFileSync = (await import('node:fs')).writeFileSync
    const readFileSync = (await import('node:fs')).readFileSync
    const createHash = (await import('node:crypto')).createHash
    mkdirSync(SHOTS_UI06, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI06, name))).digest('hex')
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI06, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    // 01: custody out live, tiga lebar.
    const a = await seedCustodyReady(page, 'shot')
    const holdA = await createHold(page, a.ws, a.draft.id, a.email, randomUUID(), a.asset.id)
    expect(holdA.status).toBe(201)
    const handA = await handoverCustody(page, a.ws, a.draft.id, a.email, randomUUID(), a.asset.id, (holdA.body as HoldRow).id)
    expect(handA.status).toBe(201)
    await uiLogin(page, a.email)
    for (const width of [360, 768, 1440]) {
      await page.setViewportSize({ width, height: width === 360 ? 640 : width === 768 ? 1024 : 900 })
      await gotoAuthed(page, `/workspaces/${a.ws}/rental-custody/${a.draft.id}`, a.email)
      await expect(page.getByText('Unit sedang dipinjam')).toBeVisible()
      await expect(page.getByRole('button', { name: 'Kembalikan unit' })).toBeVisible()
      await shot('01-custody', 'out-live', 'live', width)
    }
    // 04: tanpa fakta (handover diblokir).
    const b = await custodySetup(page, 'shot-nofact')
    const draftB = await seedDraftOne(page.request, b.ws, b.token, b.customer.id, b.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await page.setViewportSize({ width: 360, height: 640 })
    await uiLogin(page, b.email)
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-custody/${draftB.id}`, b.email)
    await expect(page.getByText('Belum ada harga yang dicatat untuk draf ini.')).toBeVisible()
    await shot('04-custody-nofact', 'nofact-live', 'live', 360)
    // 05: tanpa izin (bukan anggota).
    const otherEmail = `lain-cu-${RUN}@e2e.umnia.local`
    await uiRegister(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${a.ws}/rental-custody/${a.draft.id}`, otherEmail)
    await expect(page.getByText('bukan anggota workspace ini')).toBeVisible()
    await shot('05-custody-noperm', 'no-permission-live', 'live', 360)
    // 06: tak-diketahui (injeksi: POST handover dibatalkan).
    const c = await seedCustodyReady(page, 'shot-unknown')
    const holdC = await createHold(page, c.ws, c.draft.id, c.email, randomUUID(), c.asset.id)
    expect(holdC.status).toBe(201)
    await uiLogin(page, c.email)
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-custody/${c.draft.id}`, c.email)
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/handover')) await route.abort()
      else await route.continue()
    })
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await page.getByRole('button', { name: 'Serahkan unit' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil serah terima tidak diketahui' })).toBeVisible()
    await shot('06-custody-unknown', 'unknown-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // 07: muat (injeksi: GET custody ditahan).
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/custody')) {
        await new Promise((r) => setTimeout(r, 2500))
      }
      await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-custody/${c.draft.id}`, c.email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('07-custody-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // 08: galat (injeksi: GET dibatalkan).
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET') await route.abort()
      else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-custody/${c.draft.id}`, c.email)
    await expect(page.getByText('Layanan tidak tersedia')).toBeVisible()
    await shot('08-custody-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    const files = manifest.map((m) => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI06, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-06', files }, null, 2) + '\n')
    expect(files.length).toBe(8)
  })

  test('pembayaran: tautan Pembayaran membuka halaman; usulkan 201 → aktor kedua setujui 201 → total berubah + audit', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'happy')
    const mgr2 = `man2-py-happy-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rentals`, email)
    await page.getByRole('link', { name: 'Pembayaran' }).first().click()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/rental-payments/${draft.id}`))
    await expect(page.locator('h1')).toHaveText('Pembayaran')
    await expect(page.getByText('Jumlah harga disepakati (plafon acuan internal): 300000 unit tarif · IDR')).toBeVisible()
    await expect(page.getByText('Pembayaran posted aktif: 0 unit tarif · IDR')).toBeVisible()
    await expect(page.getByText('Sisa plafon acuan: 300000 unit tarif · IDR')).toBeVisible()
    await page.getByLabel('Nominal (unit tersimpan)').fill('100000')
    await page.getByLabel('Metode').selectOption('transfer')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi usulan pembayaran' })).toBeVisible()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByText('Usulan pembayaran: 100000 unit tarif · IDR')).toBeVisible()
    // Usulan sendiri tidak bisa disetujui sendiri: tombol nonaktif + penjelasan.
    await expect(page.getByRole('button', { name: 'Setujui' })).toBeDisabled()
    await expect(page.getByText('Persetujuan membutuhkan aktor kedua.')).toBeVisible()
    const hist1 = await getPayments(page, ws, draft.id, email)
    expect(hist1.status).toBe(200)
    expect((hist1.body as PaymentsHistoryDTO).pending_payment_proposals.length).toBe(1)
    expect((hist1.body as PaymentsHistoryDTO).paid_minor).toBe('0')
    // Aktor kedua menyetujui lewat UI.
    await uiLogout(page)
    await uiLogin(page, mgr2)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, mgr2)
    await expect(page.getByText('Usulan pembayaran: 100000 unit tarif · IDR')).toBeVisible()
    await page.getByRole('button', { name: 'Setujui' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi persetujuan pembayaran' })).toBeVisible()
    await page.getByRole('button', { name: 'Setujui' }).click()
    await expect(page.getByText('Pembayaran posted aktif: 100000 unit tarif · IDR')).toBeVisible()
    await expect(page.getByText('Sisa plafon acuan: 200000 unit tarif · IDR')).toBeVisible()
    const hist2 = await getPayments(page, ws, draft.id, email)
    expect(hist2.status).toBe(200)
    const h2 = hist2.body as PaymentsHistoryDTO
    expect(h2.payments.length).toBe(1)
    expect(h2.payments[0].amount_minor).toBe('100000')
    expect(h2.payments[0].method).toBe('transfer')
    expect(h2.payments[0].reversed).toBe(false)
    expect(h2.pending_payment_proposals.length).toBe(0)
    expect(h2.paid_minor).toBe('100000')
    expect(h2.outstanding_minor).toBe('200000')
    expect(countAudits(ws, 'rental_payment_proposed')).toBe(1)
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
  })

  test('pembayaran: pembalikan penuh — usulkan 201 → aktor kedua setujui 201 → reversed + paid kembali + audit', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'balik')
    const mgr2 = `man2-py-balik-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    const prop = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(prop.status).toBe(201)
    const propId = (prop.body as PaymentProposalDTO).id
    const appr = await approvePayment(page, ws, draft.id, propId, mgr2, randomUUID())
    expect(appr.status).toBe(201)
    const payId = (appr.body as PaymentDTO).id
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByText('Pembayaran posted aktif: 100000 unit tarif · IDR')).toBeVisible()
    await page.getByRole('button', { name: 'Usulkan pembalikan' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi usulan pembalikan' })).toBeVisible()
    await page.getByLabel('Alasan pembalikan').fill('Salah catat nominal')
    await page.getByRole('button', { name: 'Usulkan pembalikan' }).click()
    await expect(page.getByText('Usulan pembalikan: Salah catat nominal')).toBeVisible()
    await uiLogout(page)
    await uiLogin(page, mgr2)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, mgr2)
    await page.getByRole('button', { name: 'Setujui pembalikan' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi persetujuan pembalikan' })).toBeVisible()
    await page.getByRole('button', { name: 'Setujui pembalikan' }).click()
    await expect(page.getByText('Dibatalkan')).toBeVisible()
    await expect(page.getByText('Pembayaran posted aktif: 0 unit tarif · IDR')).toBeVisible()
    await expect(page.getByText('Sisa plafon acuan: 300000 unit tarif · IDR')).toBeVisible()
    const hist = await getPayments(page, ws, draft.id, email)
    expect(hist.status).toBe(200)
    const h = hist.body as PaymentsHistoryDTO
    expect(h.payments.length).toBe(1)
    expect(h.payments[0].reversed).toBe(true)
    expect(h.payments[0].reversal_id).toBeTruthy()
    expect(h.reversals.length).toBe(1)
    expect(h.reversals[0].reason).toBe('Salah catat nominal')
    expect(h.reversals[0].payment_id).toBe(payId)
    expect(h.paid_minor).toBe('0')
    expect(countAudits(ws, 'rental_payment_reversal_proposed')).toBe(1)
    expect(countAudits(ws, 'rental_payment_reversal_posted')).toBe(1)
  })

  test('pembayaran: replay keempat tahap; kunci dipakai ulang 409; tanpa audit ganda', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'replay')
    const mgr2 = `man2-py-replay-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    const pk = randomUUID()
    const p1 = await proposePayment(page, ws, draft.id, email, pk, { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: 'INV-1' })
    expect(p1.status).toBe(201)
    const propId = (p1.body as PaymentProposalDTO).id
    const pReplay = await proposePayment(page, ws, draft.id, email, pk, { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: 'INV-1' })
    expect(pReplay.status).toBe(200)
    expect((pReplay.body as PaymentProposalDTO).id).toBe(propId)
    const pMisuse = await proposePayment(page, ws, draft.id, email, pk, { amount_minor: '50000', currency: 'IDR', method: 'cash', reference: 'INV-1' })
    expect(pMisuse.status).toBe(409)
    expect(pMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const ak = randomUUID()
    const a1 = await approvePayment(page, ws, draft.id, propId, mgr2, ak)
    expect(a1.status).toBe(201)
    const payId = (a1.body as PaymentDTO).id
    expect((a1.body as PaymentDTO).client_request_id).toBe(ak)
    const aReplay = await approvePayment(page, ws, draft.id, propId, mgr2, ak)
    expect(aReplay.status).toBe(200)
    expect((aReplay.body as PaymentDTO).id).toBe(payId)
    const p2 = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '50000', currency: 'IDR', method: 'cash', reference: null })
    expect(p2.status).toBe(201)
    const aMisuse = await approvePayment(page, ws, draft.id, (p2.body as PaymentProposalDTO).id, mgr2, ak)
    expect(aMisuse.status).toBe(409)
    expect(aMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const rk = randomUUID()
    const r1 = await proposeReversal(page, ws, draft.id, payId, email, rk, 'Salah catat')
    expect(r1.status).toBe(201)
    const revPropId = (r1.body as ReversalProposalDTO).id
    const rReplay = await proposeReversal(page, ws, draft.id, payId, email, rk, 'Salah catat')
    expect(rReplay.status).toBe(200)
    expect((rReplay.body as ReversalProposalDTO).id).toBe(revPropId)
    const rMisuse = await proposeReversal(page, ws, draft.id, payId, email, rk, 'Alasan lain')
    expect(rMisuse.status).toBe(409)
    expect(rMisuse.body.error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const rak = randomUUID()
    const ra1 = await approveReversal(page, ws, draft.id, payId, revPropId, mgr2, rak)
    expect(ra1.status).toBe(201)
    const revId = (ra1.body as ReversalDTO).id
    expect((ra1.body as ReversalDTO).client_request_id).toBe(rak)
    const raReplay = await approveReversal(page, ws, draft.id, payId, revPropId, mgr2, rak)
    expect(raReplay.status).toBe(200)
    expect((raReplay.body as ReversalDTO).id).toBe(revId)
    expect(countAudits(ws, 'rental_payment_proposed')).toBe(2)
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
    expect(countAudits(ws, 'rental_payment_reversal_proposed')).toBe(1)
    expect(countAudits(ws, 'rental_payment_reversal_posted')).toBe(1)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByText('Dibatalkan')).toBeVisible()
  })

  test('pembayaran: self-approval 403 kedua alur; kasir tulis 403; nominal melebihi sisa 409', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'tolak')
    const prop = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(prop.status).toBe(201)
    const propId = (prop.body as PaymentProposalDTO).id
    const selfAppr = await approvePayment(page, ws, draft.id, propId, email, randomUUID())
    expect(selfAppr.status).toBe(403)
    expect(selfAppr.body.error).toBe('penyetuju harus berbeda dari pengusul')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByRole('button', { name: 'Setujui' })).toBeDisabled()
    await expect(page.getByText('Persetujuan membutuhkan aktor kedua.')).toBeVisible()
    // Kasir: baca 200, tulis 403 (data dipertahankan, sesi tidak dibersihkan).
    const cashierEmail = `kasir-py-tolak-${RUN}@e2e.umnia.local`
    const code = await uiInvite(page, ws, cashierEmail, 'cashier', email)
    await uiRegister(page, cashierEmail)
    await uiLogin(page, cashierEmail)
    await gotoAuthed(page, `/invites/accept?workspace_id=${ws}`, cashierEmail)
    await page.getByLabel('Kode undangan').fill(code)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/members`))
    const cashierGet = await getPayments(page, ws, draft.id, cashierEmail)
    expect(cashierGet.status).toBe(200)
    const cashierProp = await proposePayment(page, ws, draft.id, cashierEmail, randomUUID(), { amount_minor: '1000', currency: 'IDR', method: 'cash', reference: null })
    expect(cashierProp.status).toBe(403)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, cashierEmail)
    await page.getByLabel('Nominal (unit tersimpan)').fill('1000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByText('tidak punya izin mengelola data usaha')).toBeVisible()
    await expect(page).toHaveURL(new RegExp(`/workspaces/${ws}/rental-payments/${draft.id}`))
    // Over-cap: usulkan 400000 (boleh), setujui → 409.
    await uiLogout(page)
    const mgr2 = `man2-py-tolak-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    const big = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '400000', currency: 'IDR', method: 'cash', reference: null })
    expect(big.status).toBe(201)
    const bigAppr = await approvePayment(page, ws, draft.id, (big.body as PaymentProposalDTO).id, mgr2, randomUUID())
    expect(bigAppr.status).toBe(409)
    expect(bigAppr.body.error).toBe('pembayaran melebihi sisa')
    // Self-approval pembalikan → 403.
    const pay2 = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '50000', currency: 'IDR', method: 'cash', reference: null })
    expect(pay2.status).toBe(201)
    const pay2Appr = await approvePayment(page, ws, draft.id, (pay2.body as PaymentProposalDTO).id, mgr2, randomUUID())
    expect(pay2Appr.status).toBe(201)
    const pay2Id = (pay2Appr.body as PaymentDTO).id
    const rev = await proposeReversal(page, ws, draft.id, pay2Id, email, randomUUID(), 'Koreksi')
    expect(rev.status).toBe(201)
    const selfRev = await approveReversal(page, ws, draft.id, pay2Id, (rev.body as ReversalProposalDTO).id, email, randomUUID())
    expect(selfRev.status).toBe(403)
    expect(selfRev.body.error).toBe('penyetuju harus berbeda dari pengusul')
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
  })

  test('pembayaran: riwayat tetap terbaca sesudah revoke/deactivate; tulis baru 403', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'nonaktif')
    const prop = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(prop.status).toBe(201)
    const out = execFileSync(GRANT_CLI, ['--workspace', ws, '--module', 'finance.basic', '--version', '0.1.0', '--operation', 'revoke', '--reason', 'tes E2E revoke'], {
      encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: OPERATOR_URL },
    })
    expect(out.startsWith('revoked ')).toBe(true)
    const hist = await getPayments(page, ws, draft.id, email)
    expect(hist.status).toBe(200)
    expect((hist.body as PaymentsHistoryDTO).pending_payment_proposals.length).toBe(1)
    const blocked = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '1000', currency: 'IDR', method: 'cash', reference: null })
    expect(blocked.status).toBe(403)
    expect(blocked.body.error).toBe('finance belum menjadi hak workspace ini')
    psql(`UPDATE module_installations SET status='inactive', deactivated_at=now() WHERE workspace_id='${ws}' AND module_id='finance.basic'`)
    const hist2 = await getPayments(page, ws, draft.id, email)
    expect(hist2.status).toBe(200)
    const blocked2 = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '1000', currency: 'IDR', method: 'cash', reference: null })
    expect(blocked2.status).toBe(403)
    expect(blocked2.body.error).toBe('finance belum aktif di workspace ini')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByText('Usulan pembayaran: 100000 unit tarif · IDR')).toBeVisible()
    await page.getByLabel('Nominal (unit tersimpan)').fill('1000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByText('finance belum aktif di workspace ini')).toBeVisible()
    await expect(page.getByText('Minta operator memberikan entitlement finance.basic dan owner mengaktifkan modul Finance Basic.')).toBeVisible()
  })

  test('pembayaran: approval pembalikan pesaing sesudah target sudah dibalik', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'pesaing')
    const mgr2 = `man2-py-pesaing-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    const prop = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(prop.status).toBe(201)
    const appr = await approvePayment(page, ws, draft.id, (prop.body as PaymentProposalDTO).id, mgr2, randomUUID())
    expect(appr.status).toBe(201)
    const payId = (appr.body as PaymentDTO).id
    const r1 = await proposeReversal(page, ws, draft.id, payId, email, randomUUID(), 'Alasan pertama')
    expect(r1.status).toBe(201)
    const r2 = await proposeReversal(page, ws, draft.id, payId, mgr2, randomUUID(), 'Alasan kedua')
    expect(r2.status).toBe(201)
    const ra1 = await approveReversal(page, ws, draft.id, payId, (r1.body as ReversalProposalDTO).id, mgr2, randomUUID())
    expect(ra1.status).toBe(201)
    const ra2 = await approveReversal(page, ws, draft.id, payId, (r2.body as ReversalProposalDTO).id, email, randomUUID())
    expect(ra2.status).toBe(409)
    expect(ra2.body.error).toBe('pembayaran sudah dibatalkan')
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByText('Dibatalkan')).toBeVisible()
    await expect(page.getByText('Usulan pembalikan: Alasan kedua')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Setujui pembalikan' })).toBeDisabled()
    expect(countAudits(ws, 'rental_payment_reversal_posted')).toBe(1)
  })

  test('pembayaran: respons hilang keempat tahap lalu kirim-ulang kunci sama 200/201; tanpa POST baru', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'hilang')
    const mgr2 = `man2-py-hilang-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    // Satu pencegat mencatat SEMUA POST (URL + kunci + body) dan hanya
    // membatalkan POST pertama tiap tahap (abortSuffix dikonsumsi sekali).
    const posts: Array<{ url: string, key: string, body: string }> = []
    let abortSuffix: string | null = null
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const req = route.request()
      const u = new URL(req.url())
      if (req.method() === 'POST') {
        posts.push({ url: u.pathname, key: req.headers()['idempotency-key'] ?? '', body: req.postData() ?? '' })
        if (abortSuffix && u.pathname.endsWith(abortSuffix)) {
          abortSuffix = null
          await route.abort()
          return
        }
      }
      await route.continue()
    })
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    // Tahap 1: usulkan pembayaran.
    abortSuffix = '/payment-proposals'
    await page.getByLabel('Nominal (unit tersimpan)').fill('100000')
    await page.getByLabel('Metode').selectOption('transfer')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Usulan pembayaran: 100000 unit tarif · IDR')).toBeVisible()
    // Tahap 2: setujui pembayaran (aktor kedua).
    await uiLogout(page)
    await uiLogin(page, mgr2)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, mgr2)
    abortSuffix = '/approvals'
    await page.getByRole('button', { name: 'Setujui' }).click()
    await page.getByRole('button', { name: 'Setujui' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil persetujuan pembayaran tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Pembayaran posted aktif: 100000 unit tarif · IDR')).toBeVisible()
    // Tahap 3: usulkan pembalikan (pengusul pertama).
    await uiLogout(page)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    abortSuffix = '/reversal-proposals'
    await page.getByRole('button', { name: 'Usulkan pembalikan' }).click()
    await page.getByLabel('Alasan pembalikan').fill('Salah catat')
    await page.getByRole('button', { name: 'Usulkan pembalikan' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembalikan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Usulan pembalikan: Salah catat')).toBeVisible()
    // Tahap 4: setujui pembalikan (aktor kedua).
    await uiLogout(page)
    await uiLogin(page, mgr2)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, mgr2)
    abortSuffix = '/approvals'
    await page.getByRole('button', { name: 'Setujui pembalikan' }).click()
    await page.getByRole('button', { name: 'Setujui pembalikan' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil persetujuan pembalikan tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Dibatalkan')).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // Bukti POST: tiap tahap tepat 2 POST (batal + kirim-ulang) dengan
    // kunci dan body yang SAMA — tanpa kunci baru, tanpa POST ketiga.
    const stagePosts = (suffix: string) => posts.filter(p => p.url.endsWith(suffix))
    for (const suffix of ['/payment-proposals', '/reversal-proposals']) {
      const rows = stagePosts(suffix)
      expect(rows.length, suffix).toBe(2)
      expect(rows[0].key).not.toBe('')
      expect(rows[1].key).toBe(rows[0].key)
      expect(rows[1].body).toBe(rows[0].body)
    }
    const approvals = posts.filter(p => p.url.endsWith('/approvals'))
    expect(approvals.length).toBe(4)
    expect(approvals[0].key).not.toBe('')
    expect(approvals[1].key).toBe(approvals[0].key)
    expect(approvals[1].body).toBe(approvals[0].body)
    expect(approvals[2].key).not.toBe('')
    expect(approvals[3].key).toBe(approvals[2].key)
    expect(approvals[3].body).toBe(approvals[2].body)
    expect(approvals[0].key).not.toBe(approvals[2].key)
    expect(countAudits(ws, 'rental_payment_proposed')).toBe(1)
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
    expect(countAudits(ws, 'rental_payment_reversal_proposed')).toBe(1)
    expect(countAudits(ws, 'rental_payment_reversal_posted')).toBe(1)
  })

  test('pembayaran: sukses malformed tetap tidak diketahui; usulan hilang dari pending → replay kunci sama', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'malformed')
    const mgr2 = `man2-py-malformed-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    await uiLogin(page, email)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    // Respons 201 malformed (DTO kosong) → tetap tidak diketahui.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/payment-proposals')) {
        await route.fulfill({ status: 201, contentType: 'application/json', body: '{}' })
      } else await route.continue()
    })
    await page.getByLabel('Nominal (unit tersimpan)').fill('100000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    // Fulfill palsu tidak tercatat: kirim ulang kunci sama berhasil 201.
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Usulan pembayaran: 100000 unit tarif · IDR')).toBeVisible()
    // Usulan kedua hilang dari pending karena disetujui aktor kedua lewat
    // API; muat ulang mempertahankan upaya, replay kunci sama mengonfirmasi.
    // Simulasi "tercatat tetapi respons hilang": permintaan diteruskan ke
    // server (route.fetch) lalu klien hanya menerima DTO cacat — abort
    // tidak cocok di sini karena permintaan tidak pernah sampai ke server.
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/payment-proposals')) {
        await route.fetch()
        await route.fulfill({ status: 201, contentType: 'application/json', body: '{}' })
      } else await route.continue()
    })
    await page.getByLabel('Nominal (unit tersimpan)').fill('50000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toBeVisible()
    await page.unrouteAll({ behavior: 'wait' })
    const hist = await getPayments(page, ws, draft.id, email)
    expect(hist.status).toBe(200)
    const pending = (hist.body as PaymentsHistoryDTO).pending_payment_proposals
    const second = pending.find(p => p.amount_minor === '50000')
    if (!second) throw new Error('usulan kedua tidak tercatat sesudah respons hilang')
    const appr = await approvePayment(page, ws, draft.id, second.id, mgr2, randomUUID())
    expect(appr.status).toBe(201)
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toBeVisible()
    await page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' }).click()
    await expect(page.getByText('Pembayaran posted aktif: 50000 unit tarif · IDR')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toHaveCount(0)
    expect(countAudits(ws, 'rental_payment_proposed')).toBe(2)
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
  })

  test('pembayaran: GET menyelesaikan approval memagari POST lama; tanpa panel pemulihan', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'settle')
    const mgr2 = `man2-py-settle-${RUN}@e2e.umnia.local`
    await inviteManager(page, ws, mgr2, email)
    const prop = await proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(prop.status).toBe(201)
    const propId = (prop.body as PaymentProposalDTO).id
    await uiLogin(page, mgr2)
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, mgr2)
    let releasePost!: () => void
    const postHeld = new Promise<void>((resolve) => { releasePost = resolve })
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/approvals')) {
        await postHeld
      }
      await route.continue()
    })
    await page.getByRole('button', { name: 'Setujui' }).click()
    await page.getByRole('button', { name: 'Setujui' }).click()
    // POST tertahan; setujui lewat API dengan kunci berbeda.
    const appr = await approvePayment(page, ws, draft.id, propId, mgr2, randomUUID())
    expect(appr.status).toBe(201)
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Pembayaran posted aktif: 100000 unit tarif · IDR')).toBeVisible()
    await expect(page.getByRole('heading', { name: 'Hasil persetujuan pembayaran tidak diketahui' })).toHaveCount(0)
    releasePost()
    await page.unrouteAll({ behavior: 'wait' })
    await page.waitForTimeout(1000)
    await expect(page.getByRole('heading', { name: 'Hasil persetujuan pembayaran tidak diketahui' })).toHaveCount(0)
    expect(countAudits(ws, 'rental_payment_posted')).toBe(1)
  })

  test.describe('Riwayat pembayaran terkendali', () => {
    const stages = {
      payment: ['Setujui', 'persetujuan pembayaran'],
      reversal: ['Setujui pembalikan', 'persetujuan pembalikan'],
      proposal: ['Usulkan pembalikan', 'usulan pembalikan']
    } as const
    type Stage = keyof typeof stages
    type Rows = Partial<Pick<PaymentsHistoryDTO, 'payments' | 'reversals' | 'pending_payment_proposals' | 'pending_reversal_proposals'>>
    type Read = Rows | 'failed' | 'malformed'
    const notices = {
      payment: 'Pembayaran untuk usulan ini sudah dicatat melalui permintaan lain.',
      reversal: 'Catatan pembayaran ini sudah dibatalkan melalui permintaan lain.'
    }

    async function seedActors(page: Page) {
      const { email, ws, token, fact } = await seedPaymentReady(page, 'r1')
      const invite = async (role: string): Promise<string> => {
        const address = `r1-pay-${role}-${RUN}@e2e.umnia.local`
        await inviteManager(page, ws, address, email)
        const res = await page.request.get(`/api/workspaces/${ws}/members`, { headers: { Authorization: `Bearer ${token}` } })
        expect(res.status()).toBe(200)
        const { members } = await res.json() as { members: { email: string, subject_id: string, role: string, status: string }[] }
        const member = members.find(m => m.email === address && m.role === 'manager' && m.status === 'active')
        if (!member) throw new Error(`Missing active R1 ${role} manager`)
        return member.subject_id
      }
      const proposer = await invite('proposer')
      const alternate = await invite('alternate')
      return { email, ws, fact, actor: fact.accepted_by, proposer, alternate }
    }
    let fixture: Awaited<ReturnType<typeof seedActors>>
    test.beforeAll(async ({ browser }, info) => {
      const page = await browser.newPage({ baseURL: info.project.use.baseURL })
      try { fixture = await seedActors(page) } finally { await page.close() }
    })

    function makeRecords() {
      const { fact, actor, proposer } = fixture
      const identity = { rental_contract_id: fact.rental_contract_id, proposed_by: proposer, proposed_at: fact.starts_at }
      const fields = { ...identity, rental_agreed_price_id: fact.id, amount_minor: '100000', currency: fact.currency, method: 'transfer', reference: ' R1 ' }
      const proposal: PaymentProposalDTO = { ...fields, id: randomUUID(), client_request_id: randomUUID(), status: 'pending' }
      const payment: PaymentDTO = { ...fields, id: randomUUID(), proposal_id: proposal.id, approved_by: actor, approved_at: '2047-01-01T12:00:00Z', client_request_id: randomUUID(), reversed: false, reversal_id: null }
      const reason = { ...identity, payment_id: payment.id, reason: 'Salah catat nominal', proposed_at: '2047-01-02T00:00:00Z' }
      const reversalProposal: ReversalProposalDTO = { ...reason, id: randomUUID(), client_request_id: randomUUID(), status: 'pending' }
      const reversal: ReversalDTO = { ...reason, id: randomUUID(), reversal_proposal_id: reversalProposal.id, approved_by: actor, approved_at: fact.ends_at, client_request_id: randomUUID() }
      return { proposal, payment, reversalProposal, reversal }
    }

    function history(rows: Rows): PaymentsHistoryDTO {
      const { fact } = fixture
      const paid = (rows.payments ?? []).reduce((sum, p) => p.reversed ? sum : sum + BigInt(p.amount_minor), 0n)
      return { rental_contract_id: fact.rental_contract_id, rental_agreed_price_id: fact.id, total_minor: fact.total_minor, currency: fact.currency,
        paid_minor: String(paid), outstanding_minor: String(BigInt(fact.total_minor) - paid),
        payments: [], reversals: [], pending_payment_proposals: [], pending_reversal_proposals: [], ...rows }
    }

    async function intercept(page: Page, initial: Rows, suffix: string) {
      const workspace = `/api/workspaces/${fixture.ws}`
      const contract = `${workspace}/rental/contracts/${fixture.fact.rental_contract_id}`
      const writePath = contract + suffix
      const readPath = `${contract}/payments`
      const wire: { read: Read, reply: (route: Route) => Promise<void> | void } = {
        read: initial, reply: route => route.fulfill({ status: 201, json: {} })
      }
      const posts: { url: string, key: string, body: string | null }[] = []
      let loginExpected = true
      await page.route('**/api/**', route => {
        const request = route.request()
        const { origin, pathname: path } = new URL(request.url())
        if (origin !== new URL(page.url()).origin) return route.abort()
        if (request.method() === 'GET' && path === readPath) {
          if (wire.read === 'failed') return route.fulfill({ status: 503, json: { error: 'Riwayat tidak tersedia.' } })
          return route.fulfill({ json: wire.read === 'malformed' ? {} : history(wire.read) })
        }
        if (request.method() === 'GET' && (path === workspace || path.startsWith(`${workspace}/`))) return route.continue()
        if (loginExpected && request.method() === 'POST' && path === '/api/auth/login') {
          loginExpected = false
          return route.continue()
        }
        if (request.method() === 'POST') {
          posts.push({ url: request.url(), key: request.headers()['idempotency-key'] ?? '', body: request.postData() })
          if (path === writePath) return wire.reply(route)
        }
        return route.abort()
      })
      return { posts, wire, writePath, readPath }
    }

    async function openStage(page: Page, stage: Stage, records = makeRecords()) {
      const { proposal, payment, reversalProposal } = records
      const initial = stage === 'payment' ? { pending_payment_proposals: [proposal] }
        : { payments: [payment], pending_reversal_proposals: stage === 'reversal' ? [reversalProposal] : [] }
      const reversalPath = `/payments/${payment.id}/reversal-proposals`
      const path = { payment: `/payment-proposals/${proposal.id}/approvals`, proposal: reversalPath, reversal: `${reversalPath}/${reversalProposal.id}/approvals` }
      const traffic = await intercept(page, initial, path[stage])
      const target = `/workspaces/${fixture.ws}/rental-payments/${fixture.fact.rental_contract_id}`
      await page.goto(`/login?next=${encodeURIComponent(target)}`)
      await page.getByLabel('Email').fill(fixture.email)
      await page.getByLabel('Kata sandi').fill(PASSWORD)
      await page.locator('form button[type="submit"]').click()
      await expect(page).toHaveURL(target)
      const [action, label] = stages[stage]
      const button = { name: action, exact: true }
      await page.getByRole('button', button).click()
      const confirmation = page.getByRole('alert').filter({ has: page.getByRole('heading', { name: `Konfirmasi ${label}`, exact: true }) })
      if (stage === 'proposal') await confirmation.getByLabel('Alasan pembalikan').fill(` ${reversalProposal.reason} `)
      return { page, stage, records, traffic, button, label, confirmation, readPath: traffic.readPath,
        reload: page.getByRole('button', { name: 'Muat ulang data', exact: true }) }
    }
    async function sendUncertain({ page, stage, records, traffic, button, label, confirmation, reload }: Awaited<ReturnType<typeof openStage>>) {
      const { reversalProposal } = records
      await confirmation.getByRole('button', button).click()
      const panel = page.getByRole('alert').filter({ has: page.getByRole('heading', { name: `Hasil ${label} tidak diketahui`, exact: true }) })
      await expect(panel, `Malformed first ${stage} POST must reach recovery before testing GET`).toBeVisible()
      const body = stage === 'proposal' ? JSON.stringify({ reason: reversalProposal.reason }) : '{}'
      expect(traffic.posts).toEqual([{ url: new URL(traffic.writePath, page.url()).href, key: expect.stringMatching(/\S/), body }])
      return { ...records, ...traffic, page, stage, key: traffic.posts[0].key, panel,
        retry: panel.getByRole('button', { name: 'Kirim lagi (permintaan sama)', exact: true }), reload }
    }
    async function begin(page: Page, stage: Stage, records = makeRecords()) {
      return sendUncertain(await openStage(page, stage, records))
    }
    type Attempt = Awaited<ReturnType<typeof begin>>

    async function afterRead(a: Pick<Attempt, 'page' | 'readPath' | 'reload'>, action: () => Promise<void>): Promise<void> {
      await Promise.all([a.page.waitForResponse(r => new URL(r.url()).pathname === a.readPath && r.request().method() === 'GET'), action()])
      await expect(a.reload).toBeEnabled()
    }
    async function refresh(a: Attempt, rows: Read): Promise<void> {
      a.wire.read = rows
      const before = a.posts.length
      await afterRead(a, () => a.reload.click())
      expect(a.posts, 'Refresh must not send a write').toHaveLength(before)
    }
    async function replay(a: Attempt): Promise<void> {
      const before = a.posts.length
      const [response] = await Promise.all([a.page.waitForResponse(r => new URL(r.url()).pathname === a.writePath && r.request().method() === 'POST'), a.retry.click()])
      await response.finished()
      expect(a.posts.slice(before), 'Retry must preserve the stage, key and raw bytes').toEqual([a.posts[0]])
    }
    async function locked(a: Attempt): Promise<void> {
      await expect(a.panel).toBeVisible()
      await expect(a.retry).toBeEnabled()
      await expect(a.page.getByRole('button', { name: stages[a.stage][0], exact: true, disabled: false })).toHaveCount(0)
      await expect(a.page.getByRole('button', { name: /^Mulai .* baru$/ })).toHaveCount(0)
      await expect(a.page.getByText(/melalui permintaan lain\./)).toHaveCount(0)
    }
    async function retain(a: Attempt, label: string, rows: Rows): Promise<void> {
      await test.step(label, async () => {
        await refresh(a, rows)
        await locked(a)
        await replay(a)
        await locked(a)
      })
    }
    async function settled(a: Attempt, independent: boolean): Promise<void> {
      await expect(a.panel).toHaveCount(0)
      await expect(a.page.getByRole('button', { name: 'Kirim lagi (permintaan sama)' })).toHaveCount(0)
      if (!independent) {
        await expect(a.page.getByText(/melalui permintaan lain\./)).toHaveCount(0)
        return
      }
      await expect(a.page.getByText(notices[a.stage === 'payment' ? 'payment' : 'reversal'], { exact: true })).toBeVisible()
      await expect(a.page.getByText(/\bberhasil\b|(?:permintaan|persetujuan) (?:ini|Anda) (?:sudah |telah )?(?:dikonfirmasi|disetujui|dicatat)/i)).toHaveCount(0)
    }

    test('pembayaran R1-P1: konflik atau ambiguitas kunci sendiri mengunci persetujuan', async ({ page }) => {
      const a = await begin(page, 'payment')
      const own = { ...a.payment, client_request_id: a.key }
      const conflict = { ...own, id: randomUUID(), amount_minor: '99999' }
      const other = { ...a.payment, id: randomUUID(), approved_by: fixture.alternate }
      const histories = { amount: [conflict], conflictFirst: [conflict, other], conflictLast: [other, conflict],
        exactFirst: [own, conflict], exactLast: [conflict, own], duplicate: [own, { ...own, id: randomUUID() }],
        wrongActor: [{ ...own, approved_by: fixture.alternate }] }
      for (const [label, payments] of Object.entries(histories)) await retain(a, label, { payments })
    })

    test('pembayaran R1-P2: kunci lain harus menyalin usulan utuh meski aktor berbeda', async ({ page }) => {
      const a = await begin(page, 'payment')
      const other = { ...a.payment, approved_by: fixture.alternate }
      const changes: Record<string, Partial<PaymentDTO>> = {
        unrelated: { proposal_id: randomUUID() }, amount: { amount_minor: '99999' }, fact: { rental_agreed_price_id: randomUUID() },
        currency: { currency: 'USD' }, method: { method: 'cash' }, reference: { reference: other.reference?.trim() ?? null },
        nullReference: { reference: null }, proposer: { proposed_by: fixture.actor }, time: { proposed_at: other.approved_at },
        selfApproval: { approved_by: other.proposed_by }
      }
      for (const [label, change] of Object.entries(changes)) await retain(a, label, { payments: [{ ...other, ...change }] })
      await refresh(a, { payments: [other] })
      await settled(a, true)
    })

    function linked(payment: PaymentDTO, reversal: ReversalDTO): Rows {
      return { payments: [{ ...payment, reversed: true, reversal_id: reversal.id }], reversals: [reversal] }
    }
    test('pembayaran R1-R1: konflik alasan berkunci sama menang atas pembalikan pesaing', async ({ page }) => {
      const a = await begin(page, 'reversal')
      const own = { ...a.reversal, client_request_id: a.key }
      const conflict = { ...own, id: randomUUID(), reason: 'Alasan berbeda' }
      const other = { ...a.reversal, reversal_proposal_id: randomUUID(), approved_by: fixture.alternate, reason: 'Pembayaran ganda' }
      await retain(a, 'same key, changed reason, valid backlink', linked(a.payment, conflict))
      for (const reversals of [[conflict, other], [other, conflict]]) {
        await retain(a, 'fully linked competitor cannot override same-key conflict', { ...linked(a.payment, other), reversals })
      }
      await retain(a, 'exact-looking row beside conflicting same-key row', { ...linked(a.payment, own), reversals: [own, conflict] })
      await retain(a, 'same key requires the original approver', linked(a.payment, { ...own, approved_by: fixture.alternate }))
    })

    test('pembayaran R1-R2: kedua arah tautan harus membuktikan pembayaran yang tepat', async ({ page }) => {
      const a = await begin(page, 'reversal')
      for (const [origin, key] of [['own', a.key], ['independent', a.reversal.client_request_id]]) {
        const reversal = { ...a.reversal, client_request_id: key }
        const payment = { ...a.payment, reversed: true, reversal_id: reversal.id }
        const unrelated = { ...payment, id: randomUUID(), proposal_id: randomUUID() }
        const histories: Record<string, Rows> = {
          orphan: { reversals: [reversal] }, wrongPayment: { payments: [unrelated], reversals: [reversal] },
          flagOnly: { payments: [payment] }, notReversed: { payments: [{ ...payment, reversed: false }], reversals: [reversal] },
          nullBacklink: { payments: [{ ...payment, reversal_id: null }], reversals: [reversal] },
          wrongBacklink: { payments: [{ ...payment, reversal_id: randomUUID() }], reversals: [reversal] },
          wrongForwardLink: { payments: [payment], reversals: [{ ...reversal, payment_id: unrelated.id }] },
          unrelatedPair: linked(unrelated, { ...reversal, payment_id: unrelated.id, reversal_proposal_id: randomUUID() })
        }
        for (const [label, rows] of Object.entries(histories)) await retain(a, `${origin}: ${label}`, rows)
      }
    })

    test('pembayaran R1-R3: usulan pembalikan sama berkunci lain harus utuh', async ({ page }) => {
      const a = await begin(page, 'reversal')
      const other = { ...a.reversal, approved_by: fixture.alternate }
      const changes = { reason: { reason: 'Alasan berbeda' }, proposer: { proposed_by: fixture.actor },
        time: { proposed_at: other.approved_at }, selfApproval: { approved_by: other.proposed_by } }
      for (const [label, change] of Object.entries(changes)) await retain(a, label, linked(a.payment, { ...other, ...change }))
      await refresh(a, linked(a.payment, other))
      await settled(a, true)
    })

    for (const [id, stage, independent] of [
      ['P3', 'payment', false], ['R6', 'reversal', false], ['P4', 'payment', true], ['R4', 'reversal', true]
    ] as const) {
      test(`pembayaran R1-${id}: aktor sama, ${independent ? 'permintaan lain' : 'bukti sendiri'} pada riwayat dibalik`, async ({ page }) => {
        const a = await begin(page, stage)
        const key = independent ? randomUUID() : a.key
        const payment = stage === 'payment' ? { ...a.payment, client_request_id: key } : a.payment
        const reversal = stage === 'reversal' ? { ...a.reversal, client_request_id: key } : a.reversal
        await refresh(a, linked(payment, reversal))
        await expect(page.getByText('Dibatalkan', { exact: true })).toBeVisible()
        await settled(a, independent)
        if (!independent) await expect(page.getByRole('heading', { level: 2, name: 'Pembayaran', exact: true })).toBeFocused()
      })
    }

    test('pembayaran R1-R5: pembalikan pesaing tetap independen sesudah POST lama ditolak', async ({ page }) => {
      const a = await begin(page, 'reversal')
      const held: Route[] = []
      a.wire.reply = route => { held.push(route) }
      await a.retry.click()
      await expect.poll(() => held.length).toBe(1)
      const other = { ...a.reversal, reversal_proposal_id: randomUUID(), reason: 'Pembayaran ganda',
        proposed_at: a.payment.approved_at, proposed_by: fixture.actor, approved_by: fixture.alternate }
      try {
        await refresh(a, { ...linked(a.payment, other), pending_reversal_proposals: [a.reversalProposal] })
        await settled(a, true)
        await expect(page.getByRole('button', { name: 'Setujui pembalikan', exact: true })).toBeDisabled()
      } finally {
        const [response] = await Promise.all([page.waitForResponse(r => r.request() === held[0].request()),
          held[0].fulfill({ status: 409, json: { error: 'kunci idempotency sudah dipakai untuk permintaan yang berbeda' } })])
        await response.finished()
      }
      // Let the browser render the late rejection before checking for a reopened panel.
      await page.waitForFunction(() => new Promise<boolean>(done => requestAnimationFrame(() => requestAnimationFrame(() => done(true)))), undefined, { timeout: 5000 })
      await settled(a, true)
      await expect(page.getByRole('heading', { name: 'Kunci idempotency sudah dipakai' })).toHaveCount(0)
      await expect(page.getByRole('button', { name: 'Setujui pembalikan', exact: true })).toBeDisabled()
      expect(a.posts).toEqual([a.posts[0], a.posts[0]])
    })

    for (const [id, stage] of [['T1', 'payment'], ['T2', 'reversal'], ['T3', 'proposal']] as const) {
      test(`pembayaran R1-${id}: 409 sesudah hasil ${stages[stage][1]} tidak pasti`, async ({ page }) => {
        const a = await begin(page, stage)
        const error = stage === 'payment' ? 'proposal sudah disetujui' : 'pembayaran sudah dibatalkan'
        a.wire.reply = route => route.fulfill({ status: 409, json: { error } })
        const reads: Read[] = ['failed', {}]
        if (stage === 'proposal') {
          // Even the same string in the *approval* keyspace cannot prove this proposal.
          reads.push('malformed', linked(a.payment, { ...a.reversal, client_request_id: a.key, proposed_by: fixture.actor, approved_by: fixture.alternate }))
        }
        for (const read of reads) {
          a.wire.read = read
          const before = a.posts.length
          await afterRead(a, () => replay(a))
          await locked(a)
          expect(a.posts).toHaveLength(before + 1)
        }
        const dto = { payment: a.payment, reversal: a.reversal, proposal: { ...a.reversalProposal, proposed_by: fixture.actor } }[stage]
        a.wire.reply = route => route.fulfill({ status: 200, json: { ...dto, client_request_id: a.key } })
        a.wire.read = {}
        await afterRead(a, () => replay(a))
        await settled(a, false)
        expect(a.posts).toHaveLength(2 + reads.length)
      })
    }

    test('pembayaran R1-Q1: usulan pesaing satu mikrodetik sebelum pembayaran bukan bukti', async ({ page }) => {
      const records = makeRecords()
      const payment = { ...records.payment, approved_at: '2047-01-01T12:00:00.000002Z' }
      const a = await begin(page, 'reversal', { ...records, payment })
      const other = { ...a.reversal, reversal_proposal_id: randomUUID(), proposed_at: '2047-01-01T12:00:00.000001Z',
        proposed_by: fixture.actor, approved_by: fixture.alternate, reason: 'Pembayaran ganda' }
      await retain(a, 'correct keyspace and links, impossible proposal time', linked(payment, other))
      await expect(page.getByText('Dibatalkan', { exact: true })).toBeHidden()
    })

    test('pembayaran R1-Q1b: tautan balik keliru tidak boleh menampilkan status Dibatalkan', async ({ page }) => {
      const a = await begin(page, 'reversal')
      await retain(a, 'wrong backlink on the only displayed payment', {
        payments: [{ ...a.payment, reversed: true, reversal_id: randomUUID() }], reversals: [a.reversal]
      })
      await expect(page.getByText('Dibatalkan', { exact: true })).toBeHidden()
    })

    async function duringHeldRead(a: Attempt, snapshot: Rows, action: () => Promise<void>): Promise<void> {
      let release!: () => void
      const gate = new Promise<void>(resolve => { release = resolve })
      const held: Route[] = []
      await a.page.route(url => url.pathname === a.readPath, async route => {
        held.push(route)
        await gate
        await route.fulfill({ json: history(snapshot) })
      }, { times: 1 })
      try {
        await a.reload.click()
        await expect.poll(() => held.length).toBe(1)
        await action()
      } finally {
        release()
        const response = await held[0]?.request().response()
        await response?.finished()
      }
      await expect(a.reload).toBeEnabled()
    }

    test('pembayaran R1-Q2: GET sebelum replay tidak menghidupkan persetujuan baru', async ({ page }) => {
      const a = await begin(page, 'payment')
      const posted = { ...a.payment, client_request_id: a.key }
      a.wire.read = { payments: [posted] }
      a.wire.reply = route => route.fulfill({ status: 200, json: posted })
      await duringHeldRead(a, { pending_payment_proposals: [a.proposal] }, () => replay(a))
      await settled(a, false)
      await expect(page.getByRole('button', { name: 'Setujui', exact: true, disabled: false }),
        'The old GET must not offer a fresh approval of the posted proposal').toHaveCount(0)
      // A queued read and explicit invalidation are both safe; the next GET must show the posting.
      await refresh(a, { payments: [posted] })
      await expect(page.getByText(`Pembayaran posted aktif: ${posted.amount_minor} unit tarif · ${posted.currency}`, { exact: true })).toBeVisible()
      expect(a.posts).toEqual([a.posts[0], a.posts[0]])
    })

    for (const [id, stage] of [['Q3', 'payment'], ['Q3b', 'proposal'], ['Q3c', 'reversal']] as const) {
      test(`pembayaran R1-${id}: fakta gagal memblokir kiriman pertama ${stages[stage][1]}, bukan replay`, async ({ page }) => {
        const opened = await openStage(page, stage)
        await expect(opened.confirmation.getByRole('button', opened.button)).toBeEnabled()
        const failure = 'Harga uji tidak tersedia.'
        const factPath = opened.readPath.replace(/\/payments$/, '/agreed-price')
        const failNextFact = () => page.route(url => url.pathname === factPath, route =>
          route.fulfill({ status: 503, json: { error: failure } }), { times: 1 })
        await failNextFact()
        await afterRead(opened, () => opened.reload.click())
        await expect(page.getByText(failure, { exact: true })).toBeVisible()
        const enabled = opened.confirmation.getByRole('button', { ...opened.button, disabled: false })
        if (await enabled.count()) await enabled.click()
        await afterRead(opened, () => opened.reload.click())
        expect(opened.traffic.posts, 'A good history GET cannot authorize the first write without the price fact').toHaveLength(0)

        if (!await opened.confirmation.isVisible()) await page.getByRole('button', opened.button).click()
        if (stage === 'proposal') await opened.confirmation.getByLabel('Alasan pembalikan').fill(opened.records.reversalProposal.reason)
        const a = await sendUncertain(opened)
        await failNextFact()
        await refresh(a, a.wire.read)
        await expect(page.getByText(failure, { exact: true })).toBeVisible()
        await locked(a)
        await replay(a)
        await locked(a)
      })
    }

    test('pembayaran R1-Q4: hasil usulan A tidak mengalihkan fokus atau mengklaim hasil usulan B', async ({ page }) => {
      const a = await begin(page, 'payment')
      const amountB = '25000'
      const referenceB = 'Kwitansi B'
      const actionB = { name: 'Usulkan pembayaran', exact: true }
      await page.getByLabel('Nominal (unit tersimpan)').fill(amountB)
      await page.getByLabel('Referensi (opsional)').fill(referenceB)
      await page.getByRole('button', actionB).click()
      const panelB = page.getByRole('alert').filter({ has: page.getByRole('heading', { name: 'Konfirmasi usulan pembayaran', exact: true }) })
      const cancelB = panelB.getByRole('button', { name: 'Batalkan', exact: true })
      const postedA = { ...a.payment, approved_by: fixture.alternate }
      a.wire.read = { payments: [postedA] }
      await duringHeldRead(a, a.wire.read, async () => {
        await cancelB.focus()
        await expect(cancelB).toBeFocused()
      })
      await expect(a.panel).toBeHidden()
      await expect(page.getByText(/melalui permintaan lain/i)).toBeVisible()
      await expect.soft(panelB.locator(':focus'), 'Resolving A must leave focus inside the active confirmation for B').toHaveCount(1)

      const pathB = a.readPath.replace(/\/payments$/, '/payment-proposals')
      await page.route(url => url.pathname === pathB, route => {
        const proposalB = { ...a.proposal, id: randomUUID(), client_request_id: route.request().headers()['idempotency-key'],
          proposed_by: fixture.actor, amount_minor: amountB, method: 'cash', reference: referenceB }
        a.wire.read = { payments: [postedA], pending_payment_proposals: [proposalB] }
        return route.fulfill({ status: 201, json: proposalB })
      }, { times: 1 })
      await panelB.getByRole('button', actionB).click()
      const pendingB = page.getByRole('listitem').filter({ hasText: referenceB })
      await expect(pendingB).toContainText(`Usulan pembayaran: ${amountB}`)
      await expect(panelB).toBeHidden()
      await expect(pendingB).not.toContainText(/permintaan lain/i)
      // A notice may expire or remain visibly identified with A, but not as a bare "usulan ini".
      const generic = page.getByText(notices.payment, { exact: true }).filter({ visible: true })
      const contextA = page.locator('main *').filter({ hasText: new RegExp(`${postedA.amount_minor}|${postedA.proposal_id}|${postedA.id}|\\bR1\\b`, 'i'), hasNotText: referenceB })
      await expect.poll(async () => (await generic.count()) - (await contextA.locator(generic).count()),
        { message: 'Unqualified independent-payment feedback survived B’s own confirmed write' }).toBe(0)
      expect(a.posts).toHaveLength(1)
    })
  })

  test('pembayaran: tulis pertama diblokir selama bacaan ditahan; panel dibuka sebelum bacaan wajib gagal', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'gate')
    await uiLogin(page, email)
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/payments')) {
        await new Promise((r) => setTimeout(r, 3000))
      }
      await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    expect(await page.getByRole('button', { name: 'Usulkan pembayaran' }).count()).toBe(0)
    await page.unrouteAll({ behavior: 'wait' })
    await expect(page.getByText('Pembayaran posted aktif: 0 unit tarif · IDR')).toBeVisible()
    // Panel dibuka, lalu bacaan wajib gagal saat muat ulang: kirim diblokir, tanpa POST.
    const posts: string[] = []
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const req = route.request()
      const u = new URL(req.url())
      if (req.method() === 'POST') posts.push(u.pathname)
      if (req.method() === 'GET' && u.pathname.endsWith('/payments')) await route.abort()
      else await route.continue()
    })
    await page.getByLabel('Nominal (unit tersimpan)').fill('100000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Konfirmasi usulan pembayaran' })).toBeVisible()
    await page.getByRole('button', { name: 'Muat ulang data' }).click()
    await expect(page.getByText('Layanan tidak tersedia')).toBeVisible()
    await expect(page.getByRole('button', { name: 'Usulkan pembayaran' })).toBeDisabled()
    expect(posts.length).toBe(0)
    await page.unrouteAll({ behavior: 'wait' })
  })

  test('pembayaran: navigasi + fokus keyboard 44px di 360/768/1440', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'kbd')
    await uiLogin(page, email)
    await page.setViewportSize({ width: 360, height: 640 })
    await gotoAuthed(page, `/workspaces/${ws}/rental-payments/${draft.id}`, email)
    await expect(page.locator('h1')).toBeVisible()
    await expect(page.getByRole('link', { name: 'Anggota', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Keluar' }).focus()
    await page.keyboard.press('Tab')
    const focused = await page.evaluate(() => document.activeElement?.textContent)
    expect(focused).toBe('Anggota')
    const outline = await page.evaluate(() => {
      const el = document.activeElement as HTMLElement | null
      return el ? getComputedStyle(el).outlineWidth : ''
    })
    expect(outline).toBe('2px')
    for (const size of [{ width: 360, height: 640 }, { width: 768, height: 1024 }, { width: 1440, height: 900 }]) {
      await page.setViewportSize(size)
      await page.waitForTimeout(300)
      const boxes = await page.locator('section[aria-labelledby="payments-heading"] button:visible').evaluateAll((els) =>
        els.map((el) => { const r = (el as HTMLElement).getBoundingClientRect(); return { w: r.width, h: r.height } }))
      expect(boxes.length).toBeGreaterThan(0)
      for (const b of boxes) {
        expect(b.w).toBeGreaterThanOrEqual(44)
        expect(b.h).toBeGreaterThanOrEqual(44)
      }
    }
  })

  test('pembayaran: tautan lanjut login menuju halaman pembayaran; URL luar ditolak', async ({ page }) => {
    const { email, ws, token, customer, item, draft } = await seedPaymentReady(page, 'lanjut')
    const target = `/workspaces/${ws}/rental-payments/${draft.id}`
    await page.goto(`/login?next=${encodeURIComponent(target)}`)
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(new RegExp(target.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')))
    await expect(page.locator('h1')).toHaveText('Pembayaran')
    await uiLogout(page)
    await page.goto('/login?next=https://evil.example/x')
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
    await uiLogout(page)
    await page.goto(`/login?next=/workspaces/${ws}/rental-payments/bukan-uuid`)
    await page.getByLabel('Email').fill(email)
    await page.getByLabel('Kata sandi').fill(PASSWORD)
    await page.locator('form button[type="submit"]').click()
    await expect(page).toHaveURL(/\/workspaces\/new/)
  })

  test('screenshot f2-ui-07: tiga utama + tanpa-fakta + tanpa-izin + tak-diketahui + muat + galat + dibatalkan + manifest', async ({ page }) => {
    const mkdirSync = (await import('node:fs')).mkdirSync
    const writeFileSync = (await import('node:fs')).writeFileSync
    const readFileSync = (await import('node:fs')).readFileSync
    const createHash = (await import('node:crypto')).createHash
    mkdirSync(SHOTS_UI07, { recursive: true })
    const manifest: Array<{ file: string, viewport: string, state: string, provenance: string }> = []
    const sha = (name: string) => createHash('sha256').update(readFileSync(path.join(SHOTS_UI07, name))).digest('hex')
    const shot = async (name: string, state: string, provenance: string, width: number) => {
      const file = `${name}-${width}.png`
      await page.screenshot({ path: path.join(SHOTS_UI07, file), fullPage: true })
      manifest.push({ file, viewport: `${width}`, state, provenance })
    }
    // 01: riwayat live (posted + pending), tiga lebar.
    const a = await seedPaymentReady(page, 'shot')
    const mgrA = `man2-py-shot-${RUN}@e2e.umnia.local`
    await inviteManager(page, a.ws, mgrA, a.email)
    const propA = await proposePayment(page, a.ws, a.draft.id, a.email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'transfer', reference: 'INV-9' })
    expect(propA.status).toBe(201)
    const apprA = await approvePayment(page, a.ws, a.draft.id, (propA.body as PaymentProposalDTO).id, mgrA, randomUUID())
    expect(apprA.status).toBe(201)
    const propA2 = await proposePayment(page, a.ws, a.draft.id, a.email, randomUUID(), { amount_minor: '50000', currency: 'IDR', method: 'cash', reference: null })
    expect(propA2.status).toBe(201)
    await uiLogin(page, a.email)
    for (const width of [360, 768, 1440]) {
      await page.setViewportSize({ width, height: width === 360 ? 640 : width === 768 ? 1024 : 900 })
      await gotoAuthed(page, `/workspaces/${a.ws}/rental-payments/${a.draft.id}`, a.email)
      await expect(page.getByText('Pembayaran posted aktif: 100000 unit tarif · IDR')).toBeVisible()
      await expect(page.getByText('Usulan pembayaran: 50000 unit tarif · IDR')).toBeVisible()
      await shot('01-payments', 'out-live', 'live', width)
    }
    // 04: tanpa fakta (tulis diblokir).
    const b = await paymentSetup(page, 'shot-nofact')
    const draftB = await seedDraftOne(page.request, b.ws, b.token, b.customer.id, b.item.id, '2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z')
    await page.setViewportSize({ width: 360, height: 640 })
    await uiLogin(page, b.email)
    await gotoAuthed(page, `/workspaces/${b.ws}/rental-payments/${draftB.id}`, b.email)
    await expect(page.getByText('Belum ada harga yang dicatat untuk draf ini.')).toBeVisible()
    await shot('04-payments-nofact', 'nofact-live', 'live', 360)
    // 05: tanpa izin (bukan anggota).
    const otherEmail = `lain-py-${RUN}@e2e.umnia.local`
    await uiRegister(page, otherEmail)
    await gotoAuthed(page, `/workspaces/${a.ws}/rental-payments/${a.draft.id}`, otherEmail)
    await expect(page.getByText('bukan anggota workspace ini')).toBeVisible()
    await shot('05-payments-noperm', 'no-permission-live', 'live', 360)
    // 06: tak-diketahui (injeksi: POST usulan dibatalkan).
    const c = await seedPaymentReady(page, 'shot-unknown')
    await uiLogin(page, c.email)
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-payments/${c.draft.id}`, c.email)
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'POST' && u.pathname.endsWith('/payment-proposals')) await route.abort()
      else await route.continue()
    })
    await page.getByLabel('Nominal (unit tersimpan)').fill('100000')
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await page.getByRole('button', { name: 'Usulkan pembayaran' }).click()
    await expect(page.getByRole('heading', { name: 'Hasil usulan pembayaran tidak diketahui' })).toBeVisible()
    await shot('06-payments-unknown', 'unknown-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // 07: muat (injeksi: GET payments ditahan).
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET' && u.pathname.endsWith('/payments')) {
        await new Promise((r) => setTimeout(r, 2500))
      }
      await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-payments/${c.draft.id}`, c.email)
    await expect(page.getByText('Memuat…')).toBeVisible()
    await shot('07-payments-loading', 'loading-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // 08: galat (injeksi: GET dibatalkan).
    await page.route((url) => url.pathname.startsWith('/api/'), async (route) => {
      const u = new URL(route.request().url())
      if (route.request().method() === 'GET') await route.abort()
      else await route.continue()
    })
    await gotoAuthed(page, `/workspaces/${c.ws}/rental-payments/${c.draft.id}`, c.email)
    await expect(page.getByText('Layanan tidak tersedia')).toBeVisible()
    await shot('08-payments-error', 'error-mocked', 'mocked-route', 360)
    await page.unrouteAll({ behavior: 'wait' })
    // 09: dibatalkan (posted lalu dibalik lewat API).
    const d = await seedPaymentReady(page, 'shot-rev')
    const mgrD = `man2-py-shot-rev-${RUN}@e2e.umnia.local`
    await inviteManager(page, d.ws, mgrD, d.email)
    const propD = await proposePayment(page, d.ws, d.draft.id, d.email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash', reference: null })
    expect(propD.status).toBe(201)
    const apprD = await approvePayment(page, d.ws, d.draft.id, (propD.body as PaymentProposalDTO).id, mgrD, randomUUID())
    expect(apprD.status).toBe(201)
    const revD = await proposeReversal(page, d.ws, d.draft.id, (apprD.body as PaymentDTO).id, d.email, randomUUID(), 'Salah catat')
    expect(revD.status).toBe(201)
    const apprRevD = await approveReversal(page, d.ws, d.draft.id, (apprD.body as PaymentDTO).id, (revD.body as ReversalProposalDTO).id, mgrD, randomUUID())
    expect(apprRevD.status).toBe(201)
    await page.setViewportSize({ width: 360, height: 640 })
    await uiLogin(page, d.email)
    await gotoAuthed(page, `/workspaces/${d.ws}/rental-payments/${d.draft.id}`, d.email)
    await expect(page.getByText('Dibatalkan')).toBeVisible()
    await shot('09-payments-reversed', 'reversed-live', 'live', 360)
    const files = manifest.map((m) => ({ ...m, sha256: sha(m.file) }))
    writeFileSync(path.join(SHOTS_UI07, 'manifest.json'), JSON.stringify({ generated_by: 'owner-flow.spec.ts f2-ui-07', files }, null, 2) + '\n')
    expect(files.length).toBe(9)
  })
})
