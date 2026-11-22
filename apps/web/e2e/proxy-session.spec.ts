// Uji proxy same-origin + batas sesi, melawan server Nuxt yang berjalan.
// Setup fixture memakai API server (bukan UI); psql HANYA untuk fault
// injection. Rahasia (token, kode undangan) tidak pernah di-log.
import { execFileSync } from 'node:child_process'
import { randomUUID } from 'node:crypto'
import http from 'node:http'
import { expect, request as pwRequest, test } from '@playwright/test'

const RUN = `${Date.now().toString(36)}${Math.floor(Math.random() * 1296).toString(36)}`
const PASSWORD = 'E2eUiTest123'
const BOOTSTRAP_URL = process.env.E2E_BOOTSTRAP_URL ||
  'postgres://postgres:local-bootstrap-only@localhost:5432/umnia_e2e?sslmode=disable'
const BAD_UPSTREAM = process.env.PLAYWRIGHT_BAD_UPSTREAM_URL || 'http://localhost:3001'

const ownerEmail = `proxy-${RUN}@e2e.umnia.local`
const otherEmail = `proxy-lain-${RUN}@e2e.umnia.local`

let ownerToken = ''
let otherToken = ''
let wsId = ''
let wsName = ''
let inviteCode = ''

function psql(sql: string): string {
  return execFileSync('psql', [BOOTSTRAP_URL, '-v', 'ON_ERROR_STOP=1', '-tAc', sql], { encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'] })
}

function rawGet(path: string, port = 3000, headers: Record<string, string> = {}): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: 'localhost', port, path, method: 'GET', headers }, (res) => {
      let body = ''
      res.on('data', (c: Buffer) => { body += c.toString() })
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body }))
    })
    req.on('error', reject)
    req.end()
  })
}

// Method mentah apa pun (raw-wire): menguji penolakan proxy sebelum
// normalisasi klien Playwright.
function rawMethod(method: string, path: string, port = 3000): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: 'localhost', port, path, method }, (res) => {
      let body = ''
      res.on('data', (c: Buffer) => { body += c.toString() })
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body }))
    })
    req.on('error', reject)
    req.end()
  })
}

// POST mentah tanpa Content-Length (chunked): menguji jalur streaming proxy.
function rawPost(path: string, body: string): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: 'localhost', port: 3000, path, method: 'POST',
      headers: { 'Content-Type': 'application/json' } }, (res) => {
      let data = ''
      res.on('data', (c: Buffer) => { data += c.toString() })
      res.on('end', () => resolve({ status: res.statusCode ?? 0, body: data }))
    })
    req.on('error', reject)
    req.write(body)
    req.end()
  })
}

// Aliran yang sengaja tidak pernah diakhiri: membuktikan proxy menolak
// overflow SEBELUM EOF. Batas waktu eksplisit + cleanup; bukan stress test.
function rawPostNeverEnding(path: string, body: string, timeoutMs = 5000): Promise<number> {
  return new Promise((resolve, reject) => {
    let settled = false
    let status = 0
    const req = http.request({ host: 'localhost', port: 3000, path, method: 'POST',
      headers: { 'Content-Type': 'application/json' } }, (res) => {
      status = res.statusCode ?? 0
      res.resume()
      res.on('end', () => {
        clearTimeout(timer)
        if (!settled) { settled = true; req.destroy(); resolve(status) }
      })
    })
    const timer = setTimeout(() => {
      if (!settled) { settled = true; req.destroy(); reject(new Error('proxy tidak menolak overflow sebelum EOF dalam batas waktu')) }
    }, timeoutMs)
    req.on('error', (err) => {
      clearTimeout(timer)
      if (!settled) { settled = true; req.destroy(); status !== 0 ? resolve(status) : reject(err) }
    })
    req.write(body)
    // Sengaja tanpa req.end().
  })
}

test.describe.serial('proxy + sesi', () => {
  test.beforeAll(async () => {
    const ctx = await pwRequest.newContext({ baseURL: 'http://localhost:3000' })
    const reg = await ctx.post('/api/auth/register', { data: { email: ownerEmail, password: PASSWORD } })
    expect(reg.status()).toBe(201)
    const login = await ctx.post('/api/auth/login', { data: { email: ownerEmail, password: PASSWORD } })
    expect(login.status()).toBe(200)
    const loginBody = await login.json() as { token: string }
    ownerToken = loginBody.token
    wsName = `Proxy E2E ${RUN}`
    const ws = await ctx.post('/api/workspaces', {
      headers: { Authorization: `Bearer ${ownerToken}` },
      data: { name: wsName }
    })
    expect(ws.status()).toBe(201)
    wsId = ((await ws.json()) as { id: string }).id
    const invite = await ctx.post(`/api/workspaces/${wsId}/invites`, {
      headers: { Authorization: `Bearer ${ownerToken}` },
      data: { email: otherEmail, role: 'cashier' }
    })
    expect(invite.status()).toBe(201)
    inviteCode = ((await invite.json()) as { token: string }).token
    const reg2 = await ctx.post('/api/auth/register', { data: { email: otherEmail, password: PASSWORD } })
    expect(reg2.status()).toBe(201)
    const login2 = await ctx.post('/api/auth/login', { data: { email: otherEmail, password: PASSWORD } })
    expect(login2.status()).toBe(200)
    otherToken = ((await login2.json()) as { token: string }).token
    // Terima undangan: otherToken adalah kasir anggota workspace, bukan
    // nonanggota (tanpa ini, GET aset sebagai kasir 403 dengan benar).
    const accept = await ctx.post('/api/invites/accept', {
      headers: { Authorization: `Bearer ${otherToken}` },
      data: { workspace_id: wsId, token: inviteCode }
    })
    expect(accept.status()).toBe(200)
    for (let i = 1; i <= 3; i++) {
      const c = await ctx.post(`/api/workspaces/${wsId}/customers`, {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { display_name: `Proxy Pelanggan ${i}` }
      })
      expect(c.status()).toBe(201)
      const it = await ctx.post(`/api/workspaces/${wsId}/catalog/items`, {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { sku: `PRX-${i}`, name: `Proxy Barang ${i}`, unit: 'pcs' }
      })
      expect(it.status()).toBe(201)
    }
    await ctx.dispose()
  })

  // The beforeAll workspace has no agreed-price facts until later serial cases run.
  test('UI08 proxy: authenticated GET reads the empty workspace payment summary', async ({ request }) => {
    const summaryPath = `/api/workspaces/${wsId}/finance/summary`
    const summary = await request.get(summaryPath, { headers: { Authorization: `Bearer ${ownerToken}` }, maxRedirects: 0 })
    expect(summary.status()).toBe(200)
    expect(await summary.json()).toEqual(expect.objectContaining({
      workspace_id: wsId, observed_at: expect.any(String), currencies: []
    }))
    const unauthenticated = await request.get(summaryPath, { maxRedirects: 0 })
    expect(unauthenticated.status()).toBe(401)
    for (const response of [summary, unauthenticated]) {
      expect(response.headers()['cache-control']).toBe('private, no-store')
    }
  })

  test('proxy: healthz + login diteruskan apa adanya', async ({ request }) => {
    const health = await request.get('/api/healthz')
    expect(health.status()).toBe(200)
    expect(await health.json()).toEqual({ status: 'ok', service: 'business-api' })
    const login = await request.post('/api/auth/login', { data: { email: ownerEmail, password: PASSWORD } })
    expect(login.status()).toBe(200)
    const body = await login.json() as Record<string, unknown>
    expect(typeof body.token).toBe('string')
  })

  test('proxy: allowlist menolak method/path/query/segmen', async ({ request }) => {
    const cases: Array<[string, string, number]> = [
      ['GET', '/api/workspaces', 404],
      ['PUT', '/api/auth/login', 404],
      ['GET', '/api/admin/users', 404],
      ['GET', `/api/workspaces/${wsId}/modules?foo=bar`, 400],
      ['GET', '/api/workspaces/..%2F..%2Fadmin', 400],
      ['GET', '/api/workspaces/not-a-uuid/members', 404],
      ['GET', `/api/workspaces/${wsId}/modules/rental.basic%2Factivate`, 400]
    ]
    for (const [method, path, want] of cases) {
      const res = method === 'GET'
        ? await request.get(path)
        : await request.fetch(path, { method })
      expect(res.status(), `${method} ${path}`).toBe(want)
    }
    const big = await request.post(`/api/workspaces/${wsId}/modules/rental.basic/activate`, {
      headers: { Authorization: `Bearer ${ownerToken}` },
      data: { version: 'x'.repeat(17000) }
    })
    expect(big.status()).toBe(400)
    const backslash = await rawGet('/api/workspaces\\x/members')
    expect(backslash.status).toBe(400)
  })

  test('proxy: batas body dihitung per byte saat streaming', async () => {
    // Chunked UTF-8: 18033 byte tapi 9033 karakter — harus 400, bukan 401.
    const utf8Body = JSON.stringify({ email: 'é'.repeat(9000), password: 'invalid' })
    expect(Buffer.byteLength(utf8Body)).toBeGreaterThan(16384)
    expect(utf8Body.length).toBeLessThan(16384)
    const over = await rawPost('/api/auth/login', utf8Body)
    expect(over.status).toBe(400)
    // Aliran yang tidak pernah berakhir: ditolak sebelum EOF.
    const held = await rawPostNeverEnding('/api/auth/login', 'x'.repeat(20000))
    expect(held).toBe(400)
    // Batas tepat 16384 byte diteruskan (backend menjawab 401, bukan 400).
    const exact = JSON.stringify({ email: 'x'.repeat(16384 - 33), password: 'invalid' })
    expect(Buffer.byteLength(exact)).toBe(16384)
    const atLimit = await rawPost('/api/auth/login', exact)
    expect(atLimit.status).toBe(401)
    // Body kecil chunked tetap diteruskan ke backend.
    const small = await rawPost('/api/auth/login', JSON.stringify({ email: ownerEmail, password: PASSWORD }))
    expect(small.status).toBe(200)
  })

  test('proxy: upstream mati -> 502 bersih; deny tetap lokal', async () => {
    const bad = await pwRequest.newContext({ baseURL: BAD_UPSTREAM })
    const down = await bad.get('/api/healthz')
    expect(down.status()).toBe(502)
    const body = await down.text()
    expect(body).toContain('layanan tidak tersedia')
    expect(body).not.toContain('127.0.0.1')
    expect(body).not.toContain('localhost:8080')
    const denied = await bad.get('/api/admin/users')
    expect(denied.status()).toBe(404)
    await bad.dispose()
  })

  test('proxy: fault backend 500 disanitasi', async ({ request }) => {
    psql(`CREATE OR REPLACE FUNCTION app.qa_fail_ws_audit() RETURNS trigger LANGUAGE plpgsql AS $$ ` +
      `BEGIN IF NEW.action = 'workspace_created' THEN RAISE EXCEPTION 'injected audit fault (fixture)'; END IF; RETURN NEW; END; $$; ` +
      `CREATE TRIGGER qa_fault_ws_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_ws_audit()`)
    try {
      const res = await request.post('/api/workspaces', {
        headers: { Authorization: `Bearer ${ownerToken}` },
        data: { name: `Fault ${RUN}` }
      })
      expect(res.status()).toBe(500)
      const body = await res.text()
      expect(body).toContain('audit gagal ditulis')
      expect(body).not.toContain('injected audit fault')
      expect(body).not.toContain('localhost')
      expect(body).not.toContain('SELECT')
    } finally {
      psql(`DROP TRIGGER IF EXISTS qa_fault_ws_audit ON audit_events; DROP FUNCTION IF EXISTS app.qa_fail_ws_audit()`)
    }
  })

  test('SSR tidak menyerialisasi rahasia; tanpa bocor antar sesi', async ({ request }) => {
    const home = await request.get('/')
    expect(home.status()).toBe(200)
    const homeHtml = await home.text()
    // Boolean + pesan tetap: kegagalan assertion tidak boleh mencetak rahasia.
    expect(homeHtml.includes(inviteCode), 'HTML beranda tidak boleh memuat kode undangan').toBe(false)
    expect(homeHtml.includes(ownerToken), 'HTML beranda tidak boleh memuat token').toBe(false)
    const members = await request.get(`/workspaces/${wsId}/members`)
    expect(members.status()).toBe(200)
    const membersHtml = await members.text()
    expect(membersHtml.includes(inviteCode), 'HTML anggota tidak boleh memuat kode undangan').toBe(false)
    expect(membersHtml.includes(ownerToken), 'HTML anggota tidak boleh memuat token').toBe(false)
    expect(membersHtml.includes(wsName), 'HTML anggota tidak boleh memuat nama workspace').toBe(false)
    const cross = await request.get(`/api/workspaces/${wsId}/members`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cross.status()).toBe(403)
    const own = await request.get(`/api/workspaces/${wsId}/members`, {
      headers: { Authorization: `Bearer ${ownerToken}` }
    })
    expect(own.status()).toBe(200)
  })

  test('proxy: query limit/offset valid diteruskan + paginasi nyata', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    for (const base of [`/api/workspaces/${wsId}/customers`, `/api/workspaces/${wsId}/catalog/items`]) {
      const p2 = await request.get(`${base}?limit=2`, { headers: auth })
      expect(p2.status()).toBe(200)
      expect(((await p2.json()) as { items: unknown[] }).items.length).toBe(2)
      const p2o2 = await request.get(`${base}?limit=2&offset=2`, { headers: auth })
      expect(p2o2.status()).toBe(200)
      expect(((await p2o2.json()) as { items: unknown[] }).items.length).toBe(1)
      const o2l1 = await request.get(`${base}?offset=2&limit=1`, { headers: auth })
      expect(o2l1.status()).toBe(200)
      expect(((await o2l1.json()) as { items: unknown[] }).items.length).toBe(1)
      const all = await request.get(`${base}?limit=100`, { headers: auth })
      expect(((await all.json()) as { items: unknown[] }).items.length).toBe(3)
      const o0 = await request.get(`${base}?offset=0`, { headers: auth })
      expect(((await o0.json()) as { items: unknown[] }).items.length).toBe(3)
      const far = await request.get(`${base}?offset=9007199254740991`, { headers: auth })
      expect(far.status()).toBe(200)
      expect(((await far.json()) as { items: unknown[] }).items.length).toBe(0)
    }
  })

  test('proxy: query malformed ditolak lokal 400', async () => {
    const badQueries = [
      'limit=0', 'limit=101', 'limit=007', 'limit=+10', 'limit=10.5', 'limit=1e2',
      'limit=', 'limit', 'LIMIT=10', 'Limit=10', 'offset=-1', 'offset=007',
      'offset=9007199254740992', 'limit=10&limit=20', 'limit=10&foo=1',
      'limit=10;', 'limit=10?', 'limit=10#', 'limit=10&', 'limit=10&&offset=0',
      '&limit=10', 'limit=10%31', 'limit=%31%30', 'limit=10%20'
    ]
    for (const q of badQueries) {
      const res = await rawGet(`/api/workspaces/${wsId}/customers?${q}`)
      expect(res.status, `customers?${q}`).toBe(400)
      expect(res.body).toContain('permintaan tidak valid')
    }
    // Query kosong (tanda ? tanpa isi) juga 400.
    const bare = await rawGet(`/api/workspaces/${wsId}/customers?`)
    expect(bare.status).toBe(400)
    expect(bare.body).toContain('permintaan tidak valid')
  })

  test('proxy: query di rute lain 400; path tak dikenal 404', async ({ request }) => {
    const denied: Array<[string, string, number]> = [
      ['POST', `/api/workspaces/${wsId}/customers?limit=10`, 400],
      ['GET', `/api/workspaces/${wsId}/members?limit=10`, 400],
      ['GET', `/api/workspaces/${wsId}/modules?limit=10`, 400],
      // R1-7: query pada path tak dikenal juga 400, bukan 404.
      ['GET', '/api/admin?limit=10', 400]
    ]
    for (const [method, path, want] of denied) {
      const res = method === 'GET'
        ? await request.get(path)
        : await request.fetch(path, { method })
      expect(res.status(), `${method} ${path}`).toBe(want)
    }
    const unknown: Array<[string, number]> = [
      [`/api/workspaces/${wsId}/customers/nope`, 404],
      [`/api/workspaces/${wsId}/catalog/items/extra`, 404]
    ]
    for (const [path, want] of unknown) {
      const res = await request.get(path)
      expect(res.status(), `GET ${path}`).toBe(want)
    }
  })

  test('proxy R1: query di luar rute daftar 400 sebelum upstream; tanpa query tetap 404', async () => {
    // Raw-wire: method/path tak didukung, dengan dan tanpa query.
    const cases: Array<[string, string, number]> = [
      ['GET', '/api/not-allowed?limit=1', 400],
      ['GET', '/api/not-allowed', 404],
      ['PUT', `/api/workspaces/${wsId}/customers?limit=1`, 400],
      ['PUT', `/api/workspaces/${wsId}/customers`, 404],
      ['DELETE', `/api/workspaces/${wsId}/catalog/items?offset=0`, 400],
      ['DELETE', `/api/workspaces/${wsId}/catalog/items`, 404],
      ['POST', '/api/not-allowed?x=1', 400],
      ['POST', '/api/not-allowed', 404],
      ['GET', '/api/workspaces?limit=1', 400],
      ['GET', '/api/auth/login?limit=1', 400]
    ]
    for (const [method, path, want] of cases) {
      const res = await rawMethod(method, path)
      expect(res.status, `${method} ${path}`).toBe(want)
      if (want === 400) expect(res.body).toContain('permintaan tidak valid')
      if (want === 404) expect(res.body).toContain('tidak ditemukan')
    }
    // 400 lokal bahkan saat upstream mati: tidak ada kontak upstream.
    const dead400 = await rawGet('/api/not-allowed?limit=1', 3001)
    expect(dead400.status).toBe(400)
    const dead404 = await rawGet('/api/not-allowed', 3001)
    expect(dead404.status).toBe(404)
  })

  test('proxy: pelanggan/katalog upstream mati 502; query invalid tetap 400 lokal', async () => {
    const ok = await rawGet(`/api/workspaces/${wsId}/customers?limit=10`, 3001)
    expect(ok.status).toBe(502)
    expect(ok.body).toContain('layanan tidak tersedia')
    expect(ok.body).not.toContain('127.0.0.1')
    const bad = await rawGet(`/api/workspaces/${wsId}/customers?limit=0`, 3001)
    expect(bad.status).toBe(400)
    expect(bad.body).toContain('permintaan tidak valid')
    const denied = await rawGet('/api/admin/users', 3001)
    expect(denied.status).toBe(404)
  })

  test('proxy: aset GET/POST diteruskan + paginasi nyata', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth,
      data: { sku: 'PRX-AST', name: 'Proxy Unit Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    for (let i = 1; i <= 3; i++) {
      const a = await request.post(`/api/workspaces/${wsId}/assets`, {
        headers: auth,
        data: { catalog_item_id: itemId, serial: `PRX-UNIT-${i}` }
      })
      expect(a.status()).toBe(201)
      expect(((await a.json()) as { serial: string }).serial).toBe(`PRX-UNIT-${i}`)
    }
    const p2 = await request.get(`/api/workspaces/${wsId}/assets?limit=2`, { headers: auth })
    expect(p2.status()).toBe(200)
    expect(((await p2.json()) as { items: unknown[] }).items.length).toBe(2)
    const p2o2 = await request.get(`/api/workspaces/${wsId}/assets?limit=2&offset=2`, { headers: auth })
    expect(p2o2.status()).toBe(200)
    expect(((await p2o2.json()) as { items: unknown[] }).items.length).toBe(1)
    const all = await request.get(`/api/workspaces/${wsId}/assets?limit=100`, { headers: auth })
    expect(((await all.json()) as { items: unknown[] }).items.length).toBe(3)
    const far = await request.get(`/api/workspaces/${wsId}/assets?offset=9007199254740991`, { headers: auth })
    expect(far.status()).toBe(200)
    expect(((await far.json()) as { items: unknown[] }).items.length).toBe(0)
    // Kasir baca 200; nonanggota 403.
    const cashier = await request.get(`/api/workspaces/${wsId}/assets?limit=100`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cashier.status()).toBe(200)
    const nomemberEmail = `proxy-nomember-${RUN}@e2e.umnia.local`
    const reg3 = await request.post('/api/auth/register', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(reg3.status()).toBe(201)
    const login3 = await request.post('/api/auth/login', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(login3.status()).toBe(200)
    const nomember = await request.get(`/api/workspaces/${wsId}/assets?limit=100`, {
      headers: { Authorization: `Bearer ${((await login3.json()) as { token: string }).token}` }
    })
    expect(nomember.status()).toBe(403)
  })

  test('proxy: query aset malformed 400; query di POST aset 400; availability/detail di luar allowlist', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    for (const q of ['limit=0', 'limit=101', 'limit=007', 'offset=-1', 'limit=10&foo=1', 'limit=10;']) {
      const res = await rawGet(`/api/workspaces/${wsId}/assets?${q}`)
      expect(res.status, `assets?${q}`).toBe(400)
      expect(res.body).toContain('permintaan tidak valid')
    }
    const bare = await rawGet(`/api/workspaces/${wsId}/assets?`)
    expect(bare.status).toBe(400)
    const postQ = await request.post(`/api/workspaces/${wsId}/assets?limit=10`, {
      headers: auth,
      data: { catalog_item_id: '00000000-0000-4000-8000-000000000001', serial: 'X-1' }
    })
    expect(postQ.status()).toBe(400)
    // Availability dan detail aset TIDAK ada di allowlist proxy: query
    // di luar rute daftar ditolak 400 SEBELUM pencocokan rute, jadi
    // availability ber-query 400; tanpa query jalurnya tak dikenal (404).
    const avail = await request.get(
      `/api/workspaces/${wsId}/assets/00000000-0000-4000-8000-000000000001/availability?starts_at=2026-11-17T00:00:00Z&ends_at=2026-11-18T00:00:00Z`,
      { headers: auth }
    )
    expect(avail.status()).toBe(400)
    const availBare = await request.get(
      `/api/workspaces/${wsId}/assets/00000000-0000-4000-8000-000000000001/availability`,
      { headers: auth }
    )
    expect(availBare.status()).toBe(404)
    const detail = await request.get(`/api/workspaces/${wsId}/assets/00000000-0000-4000-8000-000000000001`, { headers: auth })
    expect(detail.status()).toBe(404)
  })

  test('proxy: aset upstream mati 502; query invalid tetap 400 lokal', async () => {
    const ok = await rawGet(`/api/workspaces/${wsId}/assets?limit=10`, 3001)
    expect(ok.status).toBe(502)
    expect(ok.body).toContain('layanan tidak tersedia')
    expect(ok.body).not.toContain('127.0.0.1')
    const bad = await rawGet(`/api/workspaces/${wsId}/assets?limit=0`, 3001)
    expect(bad.status).toBe(400)
    expect(bad.body).toContain('permintaan tidak valid')
  })

  test('proxy: tarif GET/POST diteruskan + kunci per panggilan + paginasi nyata', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth,
      data: { sku: 'PRX-TRF', name: 'Proxy Tarif Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    const key = `prx-key-${RUN}`
    const payload = { valid_from: '2048-01-01T00:00:00Z', valid_to: '2048-02-01T00:00:00Z', amount_minor: '12345', currency: 'IDR' }
    const first = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: payload
    })
    expect(first.status()).toBe(201)
    const firstId = ((await first.json()) as { id: string }).id
    // Kunci yang sama + payload sama diteruskan ke backend: replay 200
    // dengan baris yang sama, tanpa tulis kedua.
    const replay = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: payload
    })
    expect(replay.status()).toBe(200)
    expect(((await replay.json()) as { id: string }).id).toBe(firstId)
    // Kunci sama + payload berbeda: 409 mismatch dari backend.
    const mismatch = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: { ...payload, amount_minor: '999' }
    })
    expect(mismatch.status()).toBe(409)
    // Tanpa kunci: backend menolak 400 — proxy tidak membuat kunci.
    const noKey = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: auth,
      data: { ...payload, valid_from: '2048-03-01T00:00:00Z', valid_to: '2048-04-01T00:00:00Z' }
    })
    expect(noKey.status()).toBe(400)
    // Paginasi nyata: dua interval lagi, lalu limit/offset.
    for (const [from, to] of [['2048-03-01T00:00:00Z', '2048-04-01T00:00:00Z'], ['2048-04-01T00:00:00Z', '2048-05-01T00:00:00Z']] as const) {
      const p = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
        headers: { ...auth, 'Idempotency-Key': `prx-key-${RUN}-${from}` },
        data: { valid_from: from, valid_to: to, amount_minor: '100', currency: 'IDR' }
      })
      expect(p.status()).toBe(201)
    }
    const p2 = await request.get(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?limit=2`, { headers: auth })
    expect(p2.status()).toBe(200)
    expect(((await p2.json()) as { items: unknown[] }).items.length).toBe(2)
    const p2o2 = await request.get(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?limit=2&offset=2`, { headers: auth })
    expect(((await p2o2.json()) as { items: unknown[] }).items.length).toBe(1)
    // Kasir baca 200; nonanggota 403.
    const cashier = await request.get(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?limit=100`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cashier.status()).toBe(200)
    const nomemberEmail = `proxy-nomember-trf-${RUN}@e2e.umnia.local`
    const reg3 = await request.post('/api/auth/register', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(reg3.status()).toBe(201)
    const login3 = await request.post('/api/auth/login', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(login3.status()).toBe(200)
    const nomember = await request.get(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?limit=100`, {
      headers: { Authorization: `Bearer ${((await login3.json()) as { token: string }).token}` }
    })
    expect(nomember.status()).toBe(403)
    // Pemanggil lama tanpa kunci tetap jalan (POST pelanggan tanpa kunci).
    const old = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth,
      data: { display_name: `Proxy Tanpa Kunci ${RUN}` }
    })
    expect(old.status()).toBe(201)
  })

  test('proxy: query tarif malformed 400; query di POST tarif 400; quote di luar allowlist', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth,
      data: { sku: 'PRX-TRF-Q', name: 'Proxy Tarif Query', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    for (const q of ['limit=0', 'limit=101', 'limit=007', 'offset=-1', 'limit=10&foo=1', 'limit=10;']) {
      const res = await rawGet(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?${q}`)
      expect(res.status, `prices?${q}`).toBe(400)
      expect(res.body).toContain('permintaan tidak valid')
    }
    const bare = await rawGet(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?`)
    expect(bare.status).toBe(400)
    const postQ = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices?limit=10`, {
      headers: { ...auth, 'Idempotency-Key': `prx-q-${RUN}` },
      data: { valid_from: '2048-01-01T00:00:00Z', valid_to: '2048-02-01T00:00:00Z', amount_minor: '100', currency: 'IDR' }
    })
    expect(postQ.status()).toBe(400)
    // Quote TIDAK ada di allowlist proxy: ber-query 400 sebelum rute,
    // tanpa query jalurnya tak dikenal (404).
    const quote = await request.get(
      `/api/workspaces/${wsId}/catalog/items/${itemId}/prices/quote?starts_at=2048-01-01T00:00:00Z&ends_at=2048-01-02T00:00:00Z`,
      { headers: auth }
    )
    expect(quote.status()).toBe(400)
    const quoteBare = await request.get(
      `/api/workspaces/${wsId}/catalog/items/${itemId}/prices/quote`,
      { headers: auth }
    )
    expect(quoteBare.status()).toBe(404)
    // UUID tidak valid pada segmen barang DENGAN query: query di luar rute
    // daftar yang dikenal ditolak 400 SEBELUM pencocokan rute (proxy:162-166).
    const badUuid = await request.get(`/api/workspaces/${wsId}/catalog/items/bukan-uuid/prices?limit=10`, { headers: auth })
    expect(badUuid.status()).toBe(400)
    // Path tak dikenal TANPA query tetap 404.
    const badUuidNoQuery = await request.get(`/api/workspaces/${wsId}/catalog/items/bukan-uuid/prices`, { headers: auth })
    expect(badUuidNoQuery.status()).toBe(404)
  })

  test('proxy: tarif upstream mati 502; query invalid tetap 400 lokal', async () => {
    const ok = await rawGet(`/api/workspaces/${wsId}/catalog/items/00000000-0000-4000-8000-000000000001/prices?limit=10`, 3001)
    expect(ok.status).toBe(502)
    expect(ok.body).toContain('layanan tidak tersedia')
    expect(ok.body).not.toContain('127.0.0.1')
    const bad = await rawGet(`/api/workspaces/${wsId}/catalog/items/00000000-0000-4000-8000-000000000001/prices?limit=0`, 3001)
    expect(bad.status).toBe(400)
    expect(bad.body).toContain('permintaan tidak valid')
  })

  test('proxy: draf sewa GET/POST diteruskan + kunci per panggilan + paginasi nyata', async ({ request }) => {
    // Rental butuh entitlement + aktivasi (CLI operator sungguhan, seperti
    // grantRental di owner-flow); tanpa keduanya POST 403 dari backend.
    const grantCli = process.env.E2E_GRANT_CLI || ''
    const operatorUrl = process.env.E2E_OPERATOR_DATABASE_URL || ''
    expect(grantCli.length).toBeGreaterThan(0)
    expect(operatorUrl.length).toBeGreaterThan(0)
    const out = execFileSync(grantCli, ['--workspace', wsId, '--module', 'rental.basic', '--version', '0.1.0', '--reason', 'tes E2E proxy'], {
      encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: operatorUrl },
    })
    expect(out.startsWith('granted ')).toBe(true)
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const act = await request.post(`/api/workspaces/${wsId}/modules/rental.basic/activate`, {
      headers: auth, data: { version: '0.1.0' }
    })
    expect(act.status()).toBe(200)
    const customer = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth, data: { display_name: `Proxy Draf Pelanggan ${RUN}` }
    })
    expect(customer.status()).toBe(201)
    const customerId = ((await customer.json()) as { id: string }).id
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth, data: { sku: 'PRX-DRF', name: 'Proxy Draf Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    const key = `prx-draft-key-${RUN}`
    const payload = { customer_id: customerId, catalog_item_id: itemId, starts_at: '2048-01-01T00:00:00Z', ends_at: '2048-02-01T00:00:00Z' }
    const first = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: { ...auth, 'Idempotency-Key': key }, data: payload
    })
    expect(first.status()).toBe(201)
    const firstId = ((await first.json()) as { id: string }).id
    // Kunci yang sama + payload sama diteruskan ke backend: replay 200
    // dengan baris yang sama, tanpa tulis kedua.
    const replay = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: { ...auth, 'Idempotency-Key': key }, data: payload
    })
    expect(replay.status()).toBe(200)
    expect(((await replay.json()) as { id: string }).id).toBe(firstId)
    // Kunci sama + payload berbeda: 409 mismatch dari backend.
    const mismatch = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: { ...payload, starts_at: '2048-03-01T00:00:00Z', ends_at: '2048-04-01T00:00:00Z' }
    })
    expect(mismatch.status()).toBe(409)
    // Tanpa kunci: backend menolak 400 — proxy tidak membuat kunci.
    const noKey = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: auth,
      data: { ...payload, starts_at: '2048-05-01T00:00:00Z', ends_at: '2048-06-01T00:00:00Z' }
    })
    expect(noKey.status()).toBe(400)
    // Paginasi nyata: satu draf lagi, lalu limit/offset.
    const second = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: { ...auth, 'Idempotency-Key': `prx-draft-key-${RUN}-2` },
      data: { ...payload, starts_at: '2048-03-01T00:00:00Z', ends_at: '2048-04-01T00:00:00Z' }
    })
    expect(second.status()).toBe(201)
    const p1 = await request.get(`/api/workspaces/${wsId}/rental/contracts?limit=1`, { headers: auth })
    expect(p1.status()).toBe(200)
    expect(((await p1.json()) as { items: unknown[] }).items.length).toBe(1)
    const p1o1 = await request.get(`/api/workspaces/${wsId}/rental/contracts?limit=1&offset=1`, { headers: auth })
    expect(((await p1o1.json()) as { items: unknown[] }).items.length).toBe(1)
    // Kasir baca 200; nonanggota 403.
    const cashier = await request.get(`/api/workspaces/${wsId}/rental/contracts?limit=100`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cashier.status()).toBe(200)
    const nomemberEmail = `proxy-nomember-drf-${RUN}@e2e.umnia.local`
    const reg3 = await request.post('/api/auth/register', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(reg3.status()).toBe(201)
    const login3 = await request.post('/api/auth/login', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(login3.status()).toBe(200)
    const nomember = await request.get(`/api/workspaces/${wsId}/rental/contracts?limit=100`, {
      headers: { Authorization: `Bearer ${((await login3.json()) as { token: string }).token}` }
    })
    expect(nomember.status()).toBe(403)
  })

  test('proxy: query draf sewa malformed 400; query di POST draf 400; detail di luar allowlist', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    for (const q of ['limit=0', 'limit=101', 'limit=007', 'offset=-1', 'limit=10&foo=1', 'limit=10;']) {
      const res = await rawGet(`/api/workspaces/${wsId}/rental/contracts?${q}`)
      expect(res.status, `rental/contracts?${q}`).toBe(400)
      expect(res.body).toContain('permintaan tidak valid')
    }
    const bare = await rawGet(`/api/workspaces/${wsId}/rental/contracts?`)
    expect(bare.status).toBe(400)
    const postQ = await request.post(`/api/workspaces/${wsId}/rental/contracts?limit=10`, {
      headers: { ...auth, 'Idempotency-Key': `prx-draft-q-${RUN}` },
      data: { customer_id: '00000000-0000-4000-8000-000000000001', catalog_item_id: '00000000-0000-4000-8000-000000000002', starts_at: '2048-01-01T00:00:00Z', ends_at: '2048-02-01T00:00:00Z' }
    })
    expect(postQ.status()).toBe(400)
    // Detail kontrak TIDAK ada di allowlist proxy: ber-query 400 sebelum
    // rute, tanpa query jalurnya tak dikenal (404).
    const detail = await request.get(
      `/api/workspaces/${wsId}/rental/contracts/00000000-0000-4000-8000-000000000001?limit=10`,
      { headers: auth }
    )
    expect(detail.status()).toBe(400)
    const detailBare = await request.get(
      `/api/workspaces/${wsId}/rental/contracts/00000000-0000-4000-8000-000000000001`,
      { headers: auth }
    )
    expect(detailBare.status()).toBe(404)
  })

  test('proxy: draf sewa upstream mati 502; query invalid tetap 400 lokal', async () => {
    const ok = await rawGet(`/api/workspaces/${wsId}/rental/contracts?limit=10`, 3001)
    expect(ok.status).toBe(502)
    expect(ok.body).toContain('layanan tidak tersedia')
    expect(ok.body).not.toContain('127.0.0.1')
    const bad = await rawGet(`/api/workspaces/${wsId}/rental/contracts?limit=0`, 3001)
    expect(bad.status).toBe(400)
    expect(bad.body).toContain('permintaan tidak valid')
  })

  test('proxy: harga draf GET quote + POST accept-price + GET agreed-price diteruskan', async ({ request }) => {
    const grantCli = process.env.E2E_GRANT_CLI || ''
    const operatorUrl = process.env.E2E_OPERATOR_DATABASE_URL || ''
    expect(grantCli.length).toBeGreaterThan(0)
    expect(operatorUrl.length).toBeGreaterThan(0)
    const out = execFileSync(grantCli, ['--workspace', wsId, '--module', 'rental.basic', '--version', '0.1.0', '--reason', 'tes E2E proxy harga'], {
      encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: operatorUrl },
    })
    expect(out.startsWith('granted ') || out.startsWith('already_granted') || out.startsWith('legacy_existing')).toBe(true)
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const act = await request.post(`/api/workspaces/${wsId}/modules/rental.basic/activate`, {
      headers: auth, data: { version: '0.1.0' }
    })
    expect(act.status()).toBe(200)
    const customer = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth, data: { display_name: `Proxy Harga Pelanggan ${RUN}` }
    })
    expect(customer.status()).toBe(201)
    const customerId = ((await customer.json()) as { id: string }).id
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth, data: { sku: 'PRX-HRG', name: 'Proxy Harga Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    const price = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-price-${RUN}` },
      data: { valid_from: '2048-01-01T00:00:00Z', valid_to: '2048-03-01T00:00:00Z', amount_minor: '150000', currency: 'IDR' }
    })
    expect(price.status()).toBe(201)
    const priceId = ((await price.json()) as { id: string }).id
    const mkDraft = async (from: string, to: string, key: string) => {
      const d = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
        headers: { ...auth, 'Idempotency-Key': key },
        data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
      })
      expect(d.status()).toBe(201)
      return ((await d.json()) as { id: string }).id
    }
    const draftId = await mkDraft('2048-01-01T00:00:00Z', '2048-01-03T00:00:00Z', `prx-hrg-draft-${RUN}`)
    const quote = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/quote`, { headers: auth })
    expect(quote.status()).toBe(200)
    const q = (await quote.json()) as { draft_id: string, price_version_id: string, amount_minor: string, currency: string, unit: string, units: number, total_minor: string }
    expect(q.draft_id).toBe(draftId)
    expect(q.price_version_id).toBe(priceId)
    expect(q.amount_minor).toBe('150000')
    expect(q.currency).toBe('IDR')
    expect(q.unit).toBe('24h')
    expect(q.units).toBe(2)
    expect(q.total_minor).toBe('300000')
    // Tanpa kunci: backend menolak 400 — proxy tidak membuat kunci.
    const noKey = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
      headers: auth, data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(noKey.status()).toBe(400)
    const key = `prx-hrg-key-${RUN}`
    const first = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(first.status()).toBe(201)
    const firstBody = (await first.json()) as { id: string, client_request_id: string, pricing_rule_version_id: string }
    expect(firstBody.client_request_id).toBe(key)
    expect(firstBody.pricing_rule_version_id).toBe('elapsed_24h_ceil_v1')
    const replay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(replay.status()).toBe(200)
    expect(((await replay.json()) as { id: string }).id).toBe(firstBody.id)
    const fact = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/agreed-price`, { headers: auth })
    expect(fact.status()).toBe(200)
    expect(((await fact.json()) as { id: string }).id).toBe(firstBody.id)
    // Tiga 409 berbeda (backend nyata).
    const draft2 = await mkDraft('2048-01-05T00:00:00Z', '2048-01-07T00:00:00Z', `prx-hrg-draft2-${RUN}`)
    const keyMismatch = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': key },
      data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(keyMismatch.status()).toBe(409)
    expect(((await keyMismatch.json()) as { error: string }).error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const existingFact = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-key2-${RUN}` },
      data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(existingFact.status()).toBe(409)
    expect(((await existingFact.json()) as { error: string }).error).toBe('draft ini sudah memiliki harga yang disepakati')
    const wrongVersion = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-key3-${RUN}` },
      data: { expected_price_version_id: '00000000-0000-4000-8000-000000000001', expected_total_minor: '300000' }
    })
    expect(wrongVersion.status()).toBe(409)
    expect(((await wrongVersion.json()) as { error: string }).error).toBe('harga yang diharapkan tidak cocok dengan penawaran saat ini')
    const wrongTotal = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-key4-${RUN}` },
      data: { expected_price_version_id: priceId, expected_total_minor: '1' }
    })
    expect(wrongTotal.status()).toBe(409)
    expect(((await wrongTotal.json()) as { error: string }).error).toBe('harga yang diharapkan tidak cocok dengan penawaran saat ini')
    // Kasir baca 200; tulis 403. Nonanggota 403.
    const cashierQuote = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draft2}/quote`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cashierQuote.status()).toBe(200)
    const cashierFact = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/agreed-price`, {
      headers: { Authorization: `Bearer ${otherToken}` }
    })
    expect(cashierFact.status()).toBe(200)
    const cashierPost = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/accept-price`, {
      headers: { Authorization: `Bearer ${otherToken}`, 'Idempotency-Key': `prx-hrg-key5-${RUN}` },
      data: { expected_price_version_id: priceId, expected_total_minor: '300000' }
    })
    expect(cashierPost.status()).toBe(403)
    const nomemberEmail = `proxy-nomember-hrg-${RUN}@e2e.umnia.local`
    const reg3 = await request.post('/api/auth/register', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(reg3.status()).toBe(201)
    const login3 = await request.post('/api/auth/login', { data: { email: nomemberEmail, password: PASSWORD } })
    expect(login3.status()).toBe(200)
    const nomemberToken = ((await login3.json()) as { token: string }).token
    const nomemberQuote = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/quote`, {
      headers: { Authorization: `Bearer ${nomemberToken}` }
    })
    expect(nomemberQuote.status()).toBe(403)
    // 404 berbeda: draf hilang vs tanpa tarif vs tanpa fakta.
    const ghost = '00000000-0000-4000-8000-000000000001'
    const ghostQuote = await request.get(`/api/workspaces/${wsId}/rental/contracts/${ghost}/quote`, { headers: auth })
    expect(ghostQuote.status()).toBe(404)
    expect(((await ghostQuote.json()) as { error: string }).error).toBe('draft tidak ditemukan')
    const ghostFact = await request.get(`/api/workspaces/${wsId}/rental/contracts/${ghost}/agreed-price`, { headers: auth })
    expect(ghostFact.status()).toBe(404)
    expect(((await ghostFact.json()) as { error: string }).error).toBe('draft tidak ditemukan')
    const item2 = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth, data: { sku: 'PRX-HRG2', name: 'Proxy Harga Barang Tanpa Tarif', unit: 'pcs' }
    })
    expect(item2.status()).toBe(201)
    const item2Id = ((await item2.json()) as { id: string }).id
    const draftNoTariff = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-draft3-${RUN}` },
      data: { customer_id: customerId, catalog_item_id: item2Id, starts_at: '2048-01-01T00:00:00Z', ends_at: '2048-01-03T00:00:00Z' }
    })
    expect(draftNoTariff.status()).toBe(201)
    const draftNoTariffId = ((await draftNoTariff.json()) as { id: string }).id
    const noTariffQuote = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftNoTariffId}/quote`, { headers: auth })
    expect(noTariffQuote.status()).toBe(404)
    expect(((await noTariffQuote.json()) as { error: string }).error).toBe('harga efektif tidak ditemukan')
    const noFact = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draft2}/agreed-price`, { headers: auth })
    expect(noFact.status()).toBe(404)
    expect(((await noFact.json()) as { error: string }).error).toBe('harga yang disepakati tidak ditemukan')
  })

  test('proxy: harga draf quote 422 + uang eksak di atas 2^53 + nol + maksimum; batas 24 jam mikrodetik', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const customer = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth, data: { display_name: `Proxy Harga Uang ${RUN}` }
    })
    expect(customer.status()).toBe(201)
    const customerId = ((await customer.json()) as { id: string }).id
    const mkItem = async (sku: string) => {
      const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
        headers: auth, data: { sku, name: `Proxy ${sku}`, unit: 'pcs' }
      })
      expect(item.status()).toBe(201)
      return ((await item.json()) as { id: string }).id
    }
    const mkPrice = async (itemId: string, from: string, to: string, amount: string) => {
      const price = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
        headers: { ...auth, 'Idempotency-Key': randomUUID() },
        data: { valid_from: from, valid_to: to, amount_minor: amount, currency: 'IDR' }
      })
      expect(price.status()).toBe(201)
      return ((await price.json()) as { id: string }).id
    }
    const mkDraft = async (itemId: string, from: string, to: string) => {
      const d = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
        headers: { ...auth, 'Idempotency-Key': randomUUID() },
        data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
      })
      expect(d.status()).toBe(201)
      return ((await d.json()) as { id: string }).id
    }
    const getQuote = async (draftId: string) => {
      const q = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/quote`, { headers: auth })
      return { status: q.status(), body: (await q.json()) as { amount_minor: string, total_minor: string, units: number, price_version_id: string } }
    }
    // Overflow 422: tarif maksimum × 2 unit.
    const itemOv = await mkItem('PRX-OVF')
    await mkPrice(itemOv, '2048-01-01T00:00:00Z', '2048-02-01T00:00:00Z', '9223372036854775807')
    const draftOv = await mkDraft(itemOv, '2048-01-01T00:00:00Z', '2048-01-03T00:00:00Z')
    const qOv = await getQuote(draftOv)
    expect(qOv.status).toBe(422)
    expect((qOv.body as unknown as { error: string }).error).toBe('total di luar batas yang dapat diwakili')
    // Di atas 2^53: string eksak dipertahankan (tanpa float).
    const itemBig = await mkItem('PRX-BIG')
    const priceBig = await mkPrice(itemBig, '2048-01-01T00:00:00Z', '2048-02-01T00:00:00Z', '9007199254740993')
    const draftBig = await mkDraft(itemBig, '2048-01-01T00:00:00Z', '2048-01-02T00:00:00Z')
    const qBig = await getQuote(draftBig)
    expect(qBig.status).toBe(200)
    expect(qBig.body.units).toBe(1)
    expect(qBig.body.total_minor).toBe('9007199254740993')
    expect(qBig.body.price_version_id).toBe(priceBig)
    const accBig = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftBig}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-big-${RUN}` },
      data: { expected_price_version_id: priceBig, expected_total_minor: '9007199254740993' }
    })
    expect(accBig.status()).toBe(201)
    expect(((await accBig.json()) as { total_minor: string }).total_minor).toBe('9007199254740993')
    // Nol dan maksimum (1 unit) eksak.
    const itemZero = await mkItem('PRX-ZERO')
    const priceZero = await mkPrice(itemZero, '2048-01-01T00:00:00Z', '2048-02-01T00:00:00Z', '0')
    const draftZero = await mkDraft(itemZero, '2048-01-01T00:00:00Z', '2048-01-02T00:00:00Z')
    const qZero = await getQuote(draftZero)
    expect(qZero.status).toBe(200)
    expect(qZero.body.total_minor).toBe('0')
    const accZero = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftZero}/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-zero-${RUN}` },
      data: { expected_price_version_id: priceZero, expected_total_minor: '0' }
    })
    expect(accZero.status()).toBe(201)
    const itemMax = await mkItem('PRX-MAX')
    const priceMax = await mkPrice(itemMax, '2048-01-01T00:00:00Z', '2048-02-01T00:00:00Z', '9223372036854775807')
    const draftMax = await mkDraft(itemMax, '2048-01-01T00:00:00Z', '2048-01-02T00:00:00Z')
    const qMax = await getQuote(draftMax)
    expect(qMax.status).toBe(200)
    expect(qMax.body.total_minor).toBe('9223372036854775807')
    // Batas 24 jam: tepat 24 jam = 1 unit; 24 jam + 1 mikrodetik = 2 unit.
    const itemMicro = await mkItem('PRX-MICRO')
    await mkPrice(itemMicro, '2048-01-01T00:00:00Z', '2048-02-01T00:00:00Z', '150000')
    const draftMicro = await mkDraft(itemMicro, '2048-01-01T00:00:00Z', '2048-01-02T00:00:00.000001Z')
    const qMicro = await getQuote(draftMicro)
    expect(qMicro.status).toBe(200)
    expect(qMicro.body.units).toBe(2)
    expect(qMicro.body.total_minor).toBe('300000')
  })

  test('proxy: query harga draf 400; UUID buruk 400/404; upstream mati 502', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const ghost = '00000000-0000-4000-8000-000000000001'
    for (const path of [
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/quote`,
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/agreed-price`
    ]) {
      const res = await rawGet(`${path}?limit=10`, 3000, auth)
      expect(res.status, `${path}?limit=10`).toBe(400)
      expect(res.body).toContain('permintaan tidak valid')
    }
    const postQ = await request.post(`/api/workspaces/${wsId}/rental/contracts/${ghost}/accept-price?limit=10`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-q-${RUN}` },
      data: { expected_price_version_id: ghost, expected_total_minor: '1' }
    })
    expect(postQ.status()).toBe(400)
    // UUID buruk tanpa query: jalur tak dikenal (404); ber-query: 400 dulu.
    const badQuote = await rawGet(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/quote`, 3000, auth)
    expect(badQuote.status).toBe(404)
    const badQuoteQ = await rawGet(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/quote?limit=10`, 3000, auth)
    expect(badQuoteQ.status).toBe(400)
    const badPost = await request.post(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/accept-price`, {
      headers: { ...auth, 'Idempotency-Key': `prx-hrg-q2-${RUN}` },
      data: { expected_price_version_id: ghost, expected_total_minor: '1' }
    })
    expect(badPost.status()).toBe(404)
    const extra = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/quote/extra`, 3000, auth)
    expect(extra.status).toBe(404)
    // Upstream mati 502; query invalid tetap 400 lokal.
    const ok = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/quote`, 3001, auth)
    expect(ok.status).toBe(502)
    expect(ok.body).toContain('layanan tidak tersedia')
    expect(ok.body).not.toContain('127.0.0.1')
    const bad = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/quote?limit=0`, 3001, auth)
    expect(bad.status).toBe(400)
    expect(bad.body).toContain('permintaan tidak valid')
  })

  test('proxy: serah terima GET custody + POST holds + POST handover + POST return diteruskan', async ({ request }) => {
    const grantCli = process.env.E2E_GRANT_CLI || ''
    const operatorUrl = process.env.E2E_OPERATOR_DATABASE_URL || ''
    expect(grantCli.length).toBeGreaterThan(0)
    expect(operatorUrl.length).toBeGreaterThan(0)
    const out = execFileSync(grantCli, ['--workspace', wsId, '--module', 'rental.basic', '--version', '0.1.0', '--reason', 'tes E2E proxy custody'], {
      encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'],
      env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: operatorUrl },
    })
    expect(out.startsWith('granted ') || out.startsWith('already_granted') || out.startsWith('legacy_existing')).toBe(true)
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const act = await request.post(`/api/workspaces/${wsId}/modules/rental.basic/activate`, {
      headers: auth, data: { version: '0.1.0' }
    })
    expect(act.status()).toBe(200)
    const customer = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth, data: { display_name: `Proxy Custody Pelanggan ${RUN}` }
    })
    expect(customer.status()).toBe(201)
    const customerId = ((await customer.json()) as { id: string }).id
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth, data: { sku: 'PRX-CUS', name: 'Proxy Custody Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    const mkAsset = async (serial: string) => {
      const a = await request.post(`/api/workspaces/${wsId}/assets`, {
        headers: auth, data: { catalog_item_id: itemId, serial }
      })
      expect(a.status()).toBe(201)
      return ((await a.json()) as { id: string }).id
    }
    const assetId = await mkAsset(`PRX-CUS-1-${RUN}`)
    const asset2Id = await mkAsset(`PRX-CUS-2-${RUN}`)
    // Jendela handover ketat server: tarif valid_from sedikit di masa depan
    // (tanpa backdate), draf mulai sesudah tarif berlaku, 48 jam tepat =
    // 2 unit, total 300000. `nowMs` ditangkap tepat sebelum seed harga.
    const nowMs = Date.now()
    const hourMs = 3600000
    const winFrom = new Date(nowMs + 15 * 1000).toISOString()
    const winTo = new Date(nowMs + 15 * 1000 + 48 * hourMs).toISOString()
    // Draf ketiga butuh quote (ada fact) untuk uji hold kedaluwarsa/salah:
    // periodenya harus tercakup jendela tarif relatif DAN tidak tumpang
    // tindih dengan periode hold2 (asset2, draf utama) — EXCLUDE
    // rental_holds_no_overlap menolak hold baru pada periode itu.
    const win3From = new Date(nowMs + 49 * hourMs).toISOString()
    const win3To = new Date(nowMs + 72 * hourMs).toISOString()
    const priceFrom = new Date(nowMs + 5 * 1000).toISOString()
    const priceTo = new Date(nowMs + 73 * hourMs).toISOString()
    const price = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-price-${RUN}` },
      data: { valid_from: priceFrom, valid_to: priceTo, amount_minor: '150000', currency: 'IDR' }
    })
    expect(price.status()).toBe(201)
    const priceId = ((await price.json()) as { id: string }).id
    const mkDraft = async (from: string, to: string, key: string) => {
      const d = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
        headers: { ...auth, 'Idempotency-Key': key },
        data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
      })
      expect(d.status()).toBe(201)
      return ((await d.json()) as { id: string }).id
    }
    const mkFact = async (draftId: string, key: string) => {
      const q = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/quote`, { headers: auth })
      expect(q.status()).toBe(200)
      const qb = (await q.json()) as { price_version_id: string, total_minor: string }
      const f = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
        headers: { ...auth, 'Idempotency-Key': key },
        data: { expected_price_version_id: qb.price_version_id, expected_total_minor: qb.total_minor }
      })
      expect(f.status()).toBe(201)
      return ((await f.json()) as { id: string }).id
    }
    const draftId = await mkDraft(winFrom, winTo, `prx-cus-draft-${RUN}`)
    await mkFact(draftId, `prx-cus-fact-${RUN}`)
    // Tanpa custody: 404 spesifik (bukan fakta kosong).
    const noCustody = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/custody`, { headers: auth })
    expect(noCustody.status()).toBe(404)
    expect(((await noCustody.json()) as { error: string }).error).toBe('custody tidak ditemukan')
    // Tanpa kunci: backend menolak 400 — proxy tidak membuat kunci.
    const holdNoKey = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/holds`, {
      headers: auth, data: { asset_id: assetId }
    })
    expect(holdNoKey.status()).toBe(400)
    const holdKey = `prx-cus-hold-${RUN}`
    const hold = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/holds`, {
      headers: { ...auth, 'Idempotency-Key': holdKey }, data: { asset_id: assetId }
    })
    expect(hold.status()).toBe(201)
    const holdBody = (await hold.json()) as { id: string, workspace_id: string, draft_id: string, asset_id: string, client_request_id: string, status: string, expires_at: string }
    expect(holdBody.workspace_id).toBe(wsId)
    expect(holdBody.draft_id).toBe(draftId)
    expect(holdBody.asset_id).toBe(assetId)
    expect(holdBody.client_request_id).toBe(holdKey)
    expect(holdBody.status).toBe('held')
    expect(typeof holdBody.expires_at).toBe('string')
    const holdReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/holds`, {
      headers: { ...auth, 'Idempotency-Key': holdKey }, data: { asset_id: assetId }
    })
    expect(holdReplay.status()).toBe(200)
    expect(((await holdReplay.json()) as { id: string }).id).toBe(holdBody.id)
    const handNoKey = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/handover`, {
      headers: auth, data: { asset_id: assetId, hold_id: holdBody.id }
    })
    expect(handNoKey.status()).toBe(400)
    // Tunggu jendela handover terbuka sebelum POST handover pertama.
    const waitMs = new Date(winFrom).getTime() - Date.now() + 500
    if (waitMs > 0) await new Promise(r => setTimeout(r, waitMs))
    const handKey = `prx-cus-hand-${RUN}`
    const hand = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/handover`, {
      headers: { ...auth, 'Idempotency-Key': handKey }, data: { asset_id: assetId, hold_id: holdBody.id }
    })
    expect(hand.status()).toBe(201)
    const handBody = (await hand.json()) as { id: string, rental_contract_id: string, asset_id: string, hold_id: string, client_request_id: string, return_client_request_id: string | null, status: string, handed_out_at: string, returned_at: string | null, resolved_at: string | null }
    expect(handBody.rental_contract_id).toBe(draftId)
    expect(handBody.asset_id).toBe(assetId)
    expect(handBody.hold_id).toBe(holdBody.id)
    expect(handBody.client_request_id).toBe(handKey)
    expect(handBody.return_client_request_id).toBeNull()
    expect(handBody.status).toBe('out')
    expect(handBody.returned_at).toBeNull()
    expect(handBody.resolved_at).toBeNull()
    const handReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/handover`, {
      headers: { ...auth, 'Idempotency-Key': handKey }, data: { asset_id: assetId, hold_id: holdBody.id }
    })
    expect(handReplay.status()).toBe(200)
    expect(((await handReplay.json()) as { id: string }).id).toBe(handBody.id)
    const gotCustody = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/custody`, { headers: auth })
    expect(gotCustody.status()).toBe(200)
    expect(((await gotCustody.json()) as { id: string }).id).toBe(handBody.id)
    const retNoKey = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/return`, {
      headers: auth, data: { outcome: 'returned' }
    })
    expect(retNoKey.status()).toBe(400)
    const retKey = `prx-cus-ret-${RUN}`
    const ret = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/return`, {
      headers: { ...auth, 'Idempotency-Key': retKey }, data: { outcome: 'returned' }
    })
    expect(ret.status()).toBe(200)
    const retBody = (await ret.json()) as { id: string, status: string, return_client_request_id: string | null, returned_at: string | null, resolved_at: string | null }
    expect(retBody.id).toBe(handBody.id)
    expect(retBody.status).toBe('returned')
    expect(retBody.return_client_request_id).toBe(retKey)
    expect(retBody.returned_at).not.toBeNull()
    expect(retBody.resolved_at).not.toBeNull()
    const retReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/return`, {
      headers: { ...auth, 'Idempotency-Key': retKey }, data: { outcome: 'returned' }
    })
    expect(retReplay.status()).toBe(200)
    expect(((await retReplay.json()) as { id: string }).id).toBe(handBody.id)
    // 409 berbeda (backend nyata).
    const draft2 = await mkDraft('2048-01-05T00:00:00Z', '2048-01-07T00:00:00Z', `prx-cus-draft2-${RUN}`)
    const holdMisuse = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/holds`, {
      headers: { ...auth, 'Idempotency-Key': holdKey }, data: { asset_id: assetId }
    })
    expect(holdMisuse.status()).toBe(409)
    expect(((await holdMisuse.json()) as { error: string }).error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    const hold2 = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/holds`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hold2-${RUN}` }, data: { asset_id: asset2Id }
    })
    expect(hold2.status()).toBe(201)
    const hold2Id = ((await hold2.json()) as { id: string }).id
    const handExists = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hand2-${RUN}` }, data: { asset_id: asset2Id, hold_id: hold2Id }
    })
    expect(handExists.status()).toBe(409)
    expect(((await handExists.json()) as { error: string }).error).toBe('draft ini sudah memiliki custody')
    const holdNoFact = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/holds`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hold3-${RUN}` }, data: { asset_id: asset2Id }
    })
    expect(holdNoFact.status()).toBe(201)
    const handNoFact = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft2}/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hand3-${RUN}` }, data: { asset_id: asset2Id, hold_id: ((await holdNoFact.json()) as { id: string }).id }
    })
    expect(handNoFact.status()).toBe(409)
    expect(((await handNoFact.json()) as { error: string }).error).toBe('harga yang disepakati belum dicatat')
    const draft3 = await mkDraft(win3From, win3To, `prx-cus-draft3-${RUN}`)
    await mkFact(draft3, `prx-cus-fact3-${RUN}`)
    const hold3 = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/holds`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hold4-${RUN}` }, data: { asset_id: asset2Id }
    })
    expect(hold3.status()).toBe(201)
    const hold3Id = ((await hold3.json()) as { id: string }).id
    psql(`UPDATE rental_holds SET expires_at = now() - interval '1 hour' WHERE id='${hold3Id}'`)
    const handExpired = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hand4-${RUN}` }, data: { asset_id: asset2Id, hold_id: hold3Id }
    })
    expect(handExpired.status()).toBe(409)
    expect(((await handExpired.json()) as { error: string }).error).toBe('hold tidak aktif')
    const handWrong = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hand5-${RUN}` }, data: { asset_id: assetId, hold_id: hold3Id }
    })
    expect(handWrong.status()).toBe(409)
    expect(((await handWrong.json()) as { error: string }).error).toBe('hold tidak cocok dengan draft atau aset')
    const retTerminal = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/return`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-ret2-${RUN}` }, data: { outcome: 'damaged' }
    })
    expect(retTerminal.status()).toBe(409)
    expect(((await retTerminal.json()) as { error: string }).error).toBe('draft ini sudah dikembalikan')
    // Kasir baca 200, tulis 403; nonanggota baca 403.
    const cashierAuth = { Authorization: `Bearer ${otherToken}` }
    const cashierCustody = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/custody`, { headers: cashierAuth })
    expect(cashierCustody.status()).toBe(200)
    const cashierHolds = await request.get(`/api/workspaces/${wsId}/rental/holds?limit=10`, { headers: cashierAuth })
    expect(cashierHolds.status()).toBe(200)
    const cashierHold = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/holds`, {
      headers: { ...cashierAuth, 'Idempotency-Key': `prx-cus-kasir-${RUN}` }, data: { asset_id: asset2Id }
    })
    expect(cashierHold.status()).toBe(403)
    const cashierHand = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/handover`, {
      headers: { ...cashierAuth, 'Idempotency-Key': `prx-cus-kasir2-${RUN}` }, data: { asset_id: asset2Id, hold_id: hold3Id }
    })
    expect(cashierHand.status()).toBe(403)
    const cashierRet = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/return`, {
      headers: { ...cashierAuth, 'Idempotency-Key': `prx-cus-kasir3-${RUN}` }, data: { outcome: 'returned' }
    })
    expect(cashierRet.status()).toBe(403)
    const ghostEmail = `proxy-hantu-${RUN}@e2e.umnia.local`
    const regGhost = await request.post('/api/auth/register', { data: { email: ghostEmail, password: PASSWORD } })
    expect(regGhost.status()).toBe(201)
    const loginGhost = await request.post('/api/auth/login', { data: { email: ghostEmail, password: PASSWORD } })
    expect(loginGhost.status()).toBe(200)
    const ghostAuth = { Authorization: `Bearer ${((await loginGhost.json()) as { token: string }).token}` }
    const ghostCustody = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/custody`, { headers: ghostAuth })
    expect(ghostCustody.status()).toBe(403)
    // 404 berbeda.
    const ghost = randomUUID()
    const ghostDraftCustody = await request.get(`/api/workspaces/${wsId}/rental/contracts/${ghost}/custody`, { headers: auth })
    expect(ghostDraftCustody.status()).toBe(404)
    expect(((await ghostDraftCustody.json()) as { error: string }).error).toBe('draft tidak ditemukan')
    const ghostHoldHand = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hand6-${RUN}` }, data: { asset_id: asset2Id, hold_id: ghost }
    })
    expect(ghostHoldHand.status()).toBe(404)
    expect(((await ghostHoldHand.json()) as { error: string }).error).toBe('hold tidak ditemukan')
    const ghostAssetHold = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draft3}/holds`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-hold5-${RUN}` }, data: { asset_id: ghost }
    })
    expect(ghostAssetHold.status()).toBe(404)
    expect(((await ghostAssetHold.json()) as { error: string }).error).toBe('aset tidak ditemukan')
  })

  test('proxy: query holds + UUID buruk + upstream mati', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const ghost = randomUUID()
    const ok = await request.get(`/api/workspaces/${wsId}/rental/holds?limit=10`, { headers: auth })
    expect(ok.status()).toBe(200)
    for (const q of ['?limit=0', '?limit=101', '?foo=1', '?limit=10&limit=20', '?offset=abc', '?limit=10&']) {
      const res = await request.get(`/api/workspaces/${wsId}/rental/holds${q}`, { headers: auth })
      expect(res.status(), `holds${q}`).toBe(400)
      expect(await res.text()).toContain('permintaan tidak valid')
    }
    for (const path of [
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/custody?limit=10`,
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/holds?limit=10`,
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/handover?limit=10`,
      `/api/workspaces/${wsId}/rental/contracts/${ghost}/return?limit=10`
    ]) {
      const res = await request.get(path, { headers: auth })
      expect(res.status(), path).toBe(400)
      expect(await res.text()).toContain('permintaan tidak valid')
    }
    const postQ = await request.post(`/api/workspaces/${wsId}/rental/contracts/${ghost}/holds?limit=10`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-q-${RUN}` }, data: { asset_id: ghost }
    })
    expect(postQ.status()).toBe(400)
    const badCustody = await rawGet(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/custody`, 3000, auth)
    expect(badCustody.status).toBe(404)
    const badCustodyQ = await rawGet(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/custody?limit=10`, 3000, auth)
    expect(badCustodyQ.status).toBe(400)
    const badHand = await request.post(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/handover`, {
      headers: { ...auth, 'Idempotency-Key': `prx-cus-q2-${RUN}` }, data: { asset_id: ghost, hold_id: ghost }
    })
    expect(badHand.status()).toBe(404)
    const extra = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/custody/extra`, 3000, auth)
    expect(extra.status).toBe(404)
    const dead = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/custody`, 3001, auth)
    expect(dead.status).toBe(502)
    expect(dead.body).toContain('layanan tidak tersedia')
    expect(dead.body).not.toContain('127.0.0.1')
    const deadBad = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/custody?limit=0`, 3001, auth)
    expect(deadBad.status).toBe(400)
    expect(deadBad.body).toContain('permintaan tidak valid')
  })

  test('proxy: pembayaran GET payments + POST 4 tulis diteruskan + replay + kunci', async ({ request }) => {
    const grantCli = process.env.E2E_GRANT_CLI || ''
    const operatorUrl = process.env.E2E_OPERATOR_DATABASE_URL || ''
    expect(grantCli.length).toBeGreaterThan(0)
    expect(operatorUrl.length).toBeGreaterThan(0)
    const auth = { Authorization: `Bearer ${ownerToken}` }
    for (const mod of ['rental.basic', 'finance.basic']) {
      const out = execFileSync(grantCli, ['--workspace', wsId, '--module', mod, '--version', '0.1.0', '--reason', 'tes E2E proxy pembayaran'], {
        encoding: 'utf-8', stdio: ['ignore', 'pipe', 'pipe'],
        env: { ...process.env, UMNIA_OPERATOR_DATABASE_URL: operatorUrl },
      })
      expect(out.startsWith('granted ') || out.startsWith('already_granted') || out.startsWith('legacy_existing')).toBe(true)
      const act = await request.post(`/api/workspaces/${wsId}/modules/${mod}/activate`, {
        headers: auth, data: { version: '0.1.0' }
      })
      expect(act.status()).toBe(200)
    }
    // Manajer kedua untuk approval dua aktor (lewat API undangan).
    const mgr2Email = `proxy-man2-${RUN}@e2e.umnia.local`
    const invite = await request.post(`/api/workspaces/${wsId}/invites`, {
      headers: auth, data: { email: mgr2Email, role: 'manager' }
    })
    expect(invite.status()).toBe(201)
    const inviteToken = ((await invite.json()) as { token: string }).token
    const reg2 = await request.post('/api/auth/register', { data: { email: mgr2Email, password: PASSWORD } })
    expect(reg2.status()).toBe(201)
    const login2 = await request.post('/api/auth/login', { data: { email: mgr2Email, password: PASSWORD } })
    expect(login2.status()).toBe(200)
    const mgr2Token = ((await login2.json()) as { token: string }).token
    const accept = await request.post('/api/invites/accept', {
      headers: { Authorization: `Bearer ${mgr2Token}` },
      data: { workspace_id: wsId, token: inviteToken }
    })
    expect(accept.status()).toBe(200)
    const auth2 = { Authorization: `Bearer ${mgr2Token}` }
    const customer = await request.post(`/api/workspaces/${wsId}/customers`, {
      headers: auth, data: { display_name: `Proxy Pembayaran Pelanggan ${RUN}` }
    })
    expect(customer.status()).toBe(201)
    const customerId = ((await customer.json()) as { id: string }).id
    const item = await request.post(`/api/workspaces/${wsId}/catalog/items`, {
      headers: auth, data: { sku: 'PRX-PAY', name: 'Proxy Pembayaran Barang', unit: 'pcs' }
    })
    expect(item.status()).toBe(201)
    const itemId = ((await item.json()) as { id: string }).id
    const price = await request.post(`/api/workspaces/${wsId}/catalog/items/${itemId}/prices`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-price-${RUN}` },
      data: { valid_from: '2047-01-01T00:00:00Z', valid_to: '2047-02-01T00:00:00Z', amount_minor: '150000', currency: 'IDR' }
    })
    expect(price.status()).toBe(201)
    const mkDraft = async (from: string, to: string, key: string) => {
      const d = await request.post(`/api/workspaces/${wsId}/rental/contracts`, {
        headers: { ...auth, 'Idempotency-Key': key },
        data: { customer_id: customerId, catalog_item_id: itemId, starts_at: from, ends_at: to }
      })
      expect(d.status()).toBe(201)
      return ((await d.json()) as { id: string }).id
    }
    const mkFact = async (draftId: string, key: string) => {
      const q = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/quote`, { headers: auth })
      expect(q.status()).toBe(200)
      const qb = (await q.json()) as { price_version_id: string, total_minor: string }
      const f = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/accept-price`, {
        headers: { ...auth, 'Idempotency-Key': key },
        data: { expected_price_version_id: qb.price_version_id, expected_total_minor: qb.total_minor }
      })
      expect(f.status()).toBe(201)
      return ((await f.json()) as { id: string }).id
    }
    const draftId = await mkDraft('2047-01-01T00:00:00Z', '2047-01-03T00:00:00Z', `prx-pay-draft-${RUN}`)
    const factId = await mkFact(draftId, `prx-pay-fact-${RUN}`)
    const draftNoFact = await mkDraft('2047-01-05T00:00:00Z', '2047-01-07T00:00:00Z', `prx-pay-draft-nf-${RUN}`)
    const ghost = randomUUID()
    // GET awal: total 300000, paid 0, outstanding 300000, daftar kosong.
    const hist0 = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments`, { headers: auth })
    expect(hist0.status()).toBe(200)
    const h0 = (await hist0.json()) as { rental_contract_id: string, rental_agreed_price_id: string, currency: string, total_minor: string, paid_minor: string, outstanding_minor: string, payments: unknown[], reversals: unknown[], pending_payment_proposals: unknown[], pending_reversal_proposals: unknown[] }
    expect(h0.rental_contract_id).toBe(draftId)
    expect(h0.rental_agreed_price_id).toBe(factId)
    expect(h0.currency).toBe('IDR')
    expect(h0.total_minor).toBe('300000')
    expect(h0.paid_minor).toBe('0')
    expect(h0.outstanding_minor).toBe('300000')
    expect(h0.payments).toEqual([])
    expect(h0.pending_payment_proposals).toEqual([])
    // Draf tanpa fakta: GET 409; draf hilang: GET 404.
    const noFact = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftNoFact}/payments`, { headers: auth })
    expect(noFact.status()).toBe(409)
    expect(((await noFact.json()) as { error: string }).error).toBe('harga disepakati belum ada')
    const missing = await request.get(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payments`, { headers: auth })
    expect(missing.status()).toBe(404)
    expect(((await missing.json()) as { error: string }).error).toBe('draft tidak ditemukan')
    // Tanpa kunci: backend menolak 400 — proxy tidak membuat kunci.
    const noKey = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: auth, data: { amount_minor: '100000', currency: 'IDR', method: 'cash' }
    })
    expect(noKey.status()).toBe(400)
    const propKey = `prx-pay-prop-${RUN}`
    const prop = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: { ...auth, 'Idempotency-Key': propKey },
      data: { amount_minor: '100000', currency: 'IDR', method: 'transfer', reference: 'INV-1' }
    })
    expect(prop.status()).toBe(201)
    const propBody = (await prop.json()) as { id: string, rental_contract_id: string, rental_agreed_price_id: string, amount_minor: string, currency: string, method: string, reference: string | null, proposed_by: string, client_request_id: string, status: string }
    expect(propBody.rental_contract_id).toBe(draftId)
    expect(propBody.rental_agreed_price_id).toBe(factId)
    expect(propBody.amount_minor).toBe('100000')
    expect(propBody.currency).toBe('IDR')
    expect(propBody.method).toBe('transfer')
    expect(propBody.reference).toBe('INV-1')
    expect(propBody.client_request_id).toBe(propKey)
    expect(propBody.status).toBe('pending')
    const propReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: { ...auth, 'Idempotency-Key': propKey },
      data: { amount_minor: '100000', currency: 'IDR', method: 'transfer', reference: 'INV-1' }
    })
    expect(propReplay.status()).toBe(200)
    expect(((await propReplay.json()) as { id: string }).id).toBe(propBody.id)
    const propMisuse = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: { ...auth, 'Idempotency-Key': propKey },
      data: { amount_minor: '50000', currency: 'IDR', method: 'cash' }
    })
    expect(propMisuse.status()).toBe(409)
    expect(((await propMisuse.json()) as { error: string }).error).toBe('kunci idempotency sudah dipakai untuk permintaan yang berbeda')
    // Self-approval → 403; body approval bukan {} → 400.
    const selfAppr = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${propBody.id}/approvals`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-self-${RUN}` }, data: {}
    })
    expect(selfAppr.status()).toBe(403)
    expect(((await selfAppr.json()) as { error: string }).error).toBe('penyetuju harus berbeda dari pengusul')
    const badBody = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${propBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': `prx-pay-badbody-${RUN}` }, data: { amount_minor: '100000' }
    })
    expect(badBody.status()).toBe(400)
    const apprKey = `prx-pay-appr-${RUN}`
    const appr = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${propBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': apprKey }, data: {}
    })
    expect(appr.status()).toBe(201)
    const apprBody = (await appr.json()) as { id: string, proposal_id: string, amount_minor: string, client_request_id: string, reversed: boolean, reversal_id: string | null }
    expect(apprBody.proposal_id).toBe(propBody.id)
    expect(apprBody.amount_minor).toBe('100000')
    expect(apprBody.client_request_id).toBe(apprKey)
    expect(apprBody.reversed).toBe(false)
    expect(apprBody.reversal_id).toBeNull()
    const apprReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${propBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': apprKey }, data: {}
    })
    expect(apprReplay.status()).toBe(200)
    expect(((await apprReplay.json()) as { id: string }).id).toBe(apprBody.id)
    const apprAgain = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${propBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': `prx-pay-appr2-${RUN}` }, data: {}
    })
    expect(apprAgain.status()).toBe(409)
    expect(((await apprAgain.json()) as { error: string }).error).toBe('proposal sudah disetujui')
    // Kasir: GET 200, tulis 403.
    const cashierAuth = { Authorization: `Bearer ${otherToken}` }
    const cashierGet = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments`, { headers: cashierAuth })
    expect(cashierGet.status()).toBe(200)
    const cashierProp = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: { ...cashierAuth, 'Idempotency-Key': `prx-pay-cash-${RUN}` },
      data: { amount_minor: '1000', currency: 'IDR', method: 'cash' }
    })
    expect(cashierProp.status()).toBe(403)
    // Over-cap: usulan 400000 boleh, approval → 409.
    const bigProp = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-big-${RUN}` },
      data: { amount_minor: '400000', currency: 'IDR', method: 'cash' }
    })
    expect(bigProp.status()).toBe(201)
    const bigAppr = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payment-proposals/${((await bigProp.json()) as { id: string }).id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': `prx-pay-big-appr-${RUN}` }, data: {}
    })
    expect(bigAppr.status()).toBe(409)
    expect(((await bigAppr.json()) as { error: string }).error).toBe('pembayaran melebihi sisa')
    // Pembalikan penuh: usulkan 201, replay 200, setujui 201, replay 200.
    const revKey = `prx-pay-rev-${RUN}`
    const rev = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments/${apprBody.id}/reversal-proposals`, {
      headers: { ...auth, 'Idempotency-Key': revKey }, data: { reason: 'Salah catat' }
    })
    expect(rev.status()).toBe(201)
    const revBody = (await rev.json()) as { id: string, payment_id: string, reason: string, client_request_id: string, status: string }
    expect(revBody.payment_id).toBe(apprBody.id)
    expect(revBody.reason).toBe('Salah catat')
    expect(revBody.client_request_id).toBe(revKey)
    expect(revBody.status).toBe('pending')
    const revReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments/${apprBody.id}/reversal-proposals`, {
      headers: { ...auth, 'Idempotency-Key': revKey }, data: { reason: 'Salah catat' }
    })
    expect(revReplay.status()).toBe(200)
    expect(((await revReplay.json()) as { id: string }).id).toBe(revBody.id)
    const revApprKey = `prx-pay-revappr-${RUN}`
    const revAppr = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments/${apprBody.id}/reversal-proposals/${revBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': revApprKey }, data: {}
    })
    expect(revAppr.status()).toBe(201)
    const revApprBody = (await revAppr.json()) as { id: string, payment_id: string, reversal_proposal_id: string, reason: string, client_request_id: string }
    expect(revApprBody.payment_id).toBe(apprBody.id)
    expect(revApprBody.reversal_proposal_id).toBe(revBody.id)
    expect(revApprBody.reason).toBe('Salah catat')
    expect(revApprBody.client_request_id).toBe(revApprKey)
    const revApprReplay = await request.post(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments/${apprBody.id}/reversal-proposals/${revBody.id}/approvals`, {
      headers: { ...auth2, 'Idempotency-Key': revApprKey }, data: {}
    })
    expect(revApprReplay.status()).toBe(200)
    expect(((await revApprReplay.json()) as { id: string }).id).toBe(revApprBody.id)
    // GET akhir: pembayaran reversed + reversal_id; paid kembali 0.
    const hist1 = await request.get(`/api/workspaces/${wsId}/rental/contracts/${draftId}/payments`, { headers: auth })
    expect(hist1.status()).toBe(200)
    const h1 = (await hist1.json()) as { paid_minor: string, outstanding_minor: string, payments: Array<{ id: string, reversed: boolean, reversal_id: string | null }>, reversals: Array<{ id: string, payment_id: string }> }
    expect(h1.paid_minor).toBe('0')
    expect(h1.outstanding_minor).toBe('300000')
    expect(h1.payments.length).toBe(1)
    expect(h1.payments[0].reversed).toBe(true)
    expect(h1.payments[0].reversal_id).toBe(revApprBody.id)
    expect(h1.reversals.length).toBe(1)
    expect(h1.reversals[0].payment_id).toBe(apprBody.id)
  })

  test('proxy: query pembayaran 400; UUID buruk 400/404; upstream mati 502', async ({ request }) => {
    const auth = { Authorization: `Bearer ${ownerToken}` }
    const ghost = randomUUID()
    for (const q of ['?limit=10', '?foo=1']) {
      const res = await request.get(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payments${q}`, { headers: auth })
      expect(res.status(), `payments${q}`).toBe(400)
      expect(await res.text()).toContain('permintaan tidak valid')
    }
    const postQ = await request.post(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payment-proposals?limit=10`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-q-${RUN}` }, data: { amount_minor: '1000', currency: 'IDR', method: 'cash' }
    })
    expect(postQ.status()).toBe(400)
    const badGet = await rawGet(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/payments`, 3000, auth)
    expect(badGet.status).toBe(404)
    const badPost = await request.post(`/api/workspaces/${wsId}/rental/contracts/bukan-uuid/payment-proposals`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-q2-${RUN}` }, data: { amount_minor: '1000', currency: 'IDR', method: 'cash' }
    })
    expect(badPost.status()).toBe(404)
    const badAppr = await request.post(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payment-proposals/bukan-uuid/approvals`, {
      headers: { ...auth, 'Idempotency-Key': `prx-pay-q3-${RUN}` }, data: {}
    })
    expect(badAppr.status()).toBe(404)
    const extra = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payments/extra`, 3000, auth)
    expect(extra.status).toBe(404)
    const dead = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payments`, 3001, auth)
    expect(dead.status).toBe(502)
    expect(dead.body).toContain('layanan tidak tersedia')
    expect(dead.body).not.toContain('127.0.0.1')
    const deadBad = await rawGet(`/api/workspaces/${wsId}/rental/contracts/${ghost}/payments?limit=0`, 3001, auth)
    expect(deadBad.status).toBe(400)
    expect(deadBad.body).toContain('permintaan tidak valid')
  })
})

test('UI08 proxy: only the queryless summary GET reaches the unavailable upstream', async () => {
  const summaryPath = `/api/workspaces/${randomUUID()}/finance/summary`
  const liveProxyPort = 3000
  const deadProxyPort = 3001
  const controls = [
    ['GET', `${summaryPath}?`, 400],
    ['GET', `${summaryPath}?currency=IDR`, 400],
    ['GET', `${summaryPath}?limit=1`, 400],
    ['POST', summaryPath, 404],
    ['GET', '/api/workspaces/not-a-uuid/finance/summary', 404],
    ['GET', `${summaryPath}/extra`, 404]
  ] as const
  for (const port of [liveProxyPort, deadProxyPort]) {
    for (const [method, path, status] of controls) {
      const response = await rawMethod(method, path, port)
      expect(response.status, `${method} ${path} on ${port}`).toBe(status)
    }
  }
  const dead = await rawGet(summaryPath, deadProxyPort)
  expect(dead.status).toBe(502)
  expect(JSON.parse(dead.body)).toEqual({ error: 'layanan tidak tersedia' })
})
