// Uji proxy same-origin + batas sesi, melawan server Nuxt yang berjalan.
// Setup fixture memakai API server (bukan UI); psql HANYA untuk fault
// injection. Rahasia (token, kode undangan) tidak pernah di-log.
import { execFileSync } from 'node:child_process'
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

function rawGet(path: string, port = 3000): Promise<{ status: number, body: string }> {
  return new Promise((resolve, reject) => {
    const req = http.request({ host: 'localhost', port, path, method: 'GET' }, (res) => {
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
    await ctx.dispose()
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
})
