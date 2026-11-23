import { createHash } from 'node:crypto'
import { existsSync, mkdirSync, readFileSync, readdirSync, writeFileSync } from 'node:fs'
import { expect, type Locator, type Page, type Request, type Response as BrowserResponse, type Route } from '@playwright/test'
import type { SessionState } from '../plugins/session.client'

export const EMPTY = 'Belum ada harga yang disepakati di workspace ini.'
export const LOADING = 'Memuat ringkasan...'
export const OBSERVED = '2028-02-29T11:17:04.123456789Z'
const COUNT_LABELS = ['Jumlah catatan pembayaran', 'Jumlah catatan pembalikan',
  'Usulan pembayaran yang belum menghasilkan catatan', 'Usulan pembalikan yang belum menghasilkan catatan']
const LABELS = ['Jumlah harga disepakati', 'Pembayaran tercatat aktif', 'Sisa plafon acuan', ...COUNT_LABELS]
type Counts = [number, number, number, number]
export interface CurrencyRow {
  currency: string, total_minor: string, paid_minor: string, outstanding_minor: string
  payments_count: number, reversals_count: number, pending_payment_proposals_count: number, pending_reversal_proposals_count: number
}
export function currencyRow(currency: string, total: string, paid: string, outstanding: string,
  [payments, reversals, pendingPayments, pendingReversals]: Counts = [0, 0, 0, 0]): CurrencyRow {
  return { currency, total_minor: total, paid_minor: paid, outstanding_minor: outstanding, payments_count: payments,
    reversals_count: reversals, pending_payment_proposals_count: pendingPayments, pending_reversal_proposals_count: pendingReversals }
}
const LARGE = '18446744073709551614'
export const GOLDEN = [currencyRow('IDR', LARGE, LARGE, '0', [5, 3, 0, 0]), currencyRow('USD', '500000', '200000', '300000', [1, 0, 0, 0])] as const
export const ZERO = currencyRow('EUR', '0', '0', '0')
// Controlled display oracle, not a DB fact: the reader must not derive the third amount from the first two.
export const PASSTHROUGH = currencyRow('JPY', '9'.repeat(80), '1', '7'.repeat(80), [5, 3, 2, 1])
export const summaryPath = (ws: string): string => `/workspaces/${ws}/payment-summary`
export const summaryApi = (ws: string): string => `/api/workspaces/${ws}/finance/summary`
interface SummaryBody { workspace_id: string, observed_at: string, currencies: readonly CurrencyRow[] }
export function summaryBody(ws: string, currencies: readonly CurrencyRow[] = GOLDEN): SummaryBody {
  return { workspace_id: ws, observed_at: OBSERVED, currencies }
}
export function invalidSummaries(ws: string): [string, unknown][] {
  const good = summaryBody(ws)
  const badRow = (patch: Record<string, unknown>) => ({ ...good, currencies: [GOLDEN[0], { ...GOLDEN[1], ...patch }] })
  return [
    ['wrong workspace', { ...good, workspace_id: ws.replace(/^./, ws.startsWith('0') ? '1' : '0') }],
    ['impossible UTC date', { ...good, observed_at: '2027-02-29T11:17:04Z' }],
    ['non-UTC observation', { ...good, observed_at: '2028-02-29T11:17:04+07:00' }],
    ['not an array', { ...good, currencies: null }], ['duplicate currency', badRow({ currency: 'IDR' })],
    ['bad currency', badRow({ currency: 'Usd' })], ['noncanonical money', badRow({ paid_minor: '0200000' })],
    ['numeric money', badRow({ total_minor: 500000 })], ['negative money', badRow({ outstanding_minor: '-1' })],
    ['unsafe count', badRow({ payments_count: Number.MAX_SAFE_INTEGER + 1 })],
    ['fractional count', badRow({ pending_payment_proposals_count: 0.5 })],
    ['negative count', badRow({ reversals_count: -1 })], ['missing count', badRow({ pending_reversal_proposals_count: undefined })],
  ]
}

interface MountedNuxt {
  $session: SessionState
  $router: {
    push(path: string): Promise<unknown>
    readonly currentRoute: { readonly value: { readonly fullPath: string } }
  }
}
interface SummaryReader {
  open(ws: string): Promise<void>
  openRawPath(path: string): Promise<void>
  currentFullPath(): Promise<string>
  replace(token: string, subject?: string): Promise<void>
  retained(): Promise<() => Promise<boolean>>
  signedOut(): Promise<boolean>
}
export async function mountedReader(page: Page): Promise<SummaryReader> {
  // Keep the real app handle: a document reload cannot accidentally pass a session/race assertion.
  const app = await page.evaluateHandle(() => {
    type Root = Element & { __vue_app__?: { config: { globalProperties: { $nuxt?: MountedNuxt } } } }
    const root = document.getElementById('__nuxt') as Root | null
    const nuxt = root?.__vue_app__?.config.globalProperties.$nuxt
    if (!nuxt?.$router || !nuxt.$session) throw new Error('Mounted Nuxt router/session unavailable')
    return nuxt
  })
  const openRawPath = (path: string): Promise<void> =>
    app.evaluate(async (nuxt, targetPath) => { await nuxt.$router.push(targetPath) }, path)
  return {
    open: ws => openRawPath(summaryPath(ws)),
    openRawPath,
    currentFullPath: () => app.evaluate(nuxt => nuxt.$router.currentRoute.value.fullPath),
    replace: (token, subject) => app.evaluate(({ $session }, actor) => {
      const id = actor.subject ?? $session.subjectId.value
      if (!id || !$session.token.value || actor.token === $session.token.value) throw new Error('Expected a distinct fixture session')
      $session.set(actor.token, id)
    }, { token, subject }),
    async retained() {
      const matches = await app.evaluateHandle(({ $session }) => {
        const token = $session.token.value, subject = $session.subjectId.value
        return () => !!token && !!subject && $session.token.value === token && $session.subjectId.value === subject
      })
      return () => matches.evaluate(check => check())
    },
    signedOut: () => app.evaluate(({ $session }) => $session.token.value === null && $session.subjectId.value === null),
  }
}

const SUMMARY_API = /^\/api\/workspaces\/([0-9a-f-]{36})\/finance\/summary$/
interface Observation { check(...ids: string[]): Promise<void>, statuses: number[], stop(): void }
export function observeReads(page: Page, afterLogin = false): Observation {
  const origin = new URL(page.url()).origin, requests: string[] = [], statuses: number[] = []
  let active = !afterLogin
  const record = (request: Request) => {
    if (!active) return
    const url = new URL(request.url())
    const asset = url.origin === origin && (url.pathname.startsWith('/_nuxt/') || url.pathname.endsWith('/_payload.json'))
    const dataRequest = !asset && ['fetch', 'xhr'].includes(request.resourceType())
    if (!dataRequest && !/^\/api(?:\/|$)/.test(url.pathname) && ['GET', 'HEAD'].includes(request.method())) return
    const id = SUMMARY_API.exec(url.pathname)?.[1]
    const plain = id && url.href === origin + summaryApi(id) && request.method() === 'GET' && request.postDataBuffer() === null
    requests.push(id && plain ? id : 'unexpected API request')
  }
  const response = (reply: BrowserResponse) => {
    if (afterLogin && reply.url() === origin + '/api/auth/login' && reply.status() === 200) active = true
    if (active && SUMMARY_API.test(new URL(reply.url()).pathname)) statuses.push(reply.status())
  }
  page.on('request', record).on('response', response)
  return { statuses, async check(...ids) {
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => resolve())))
    expect(requests, 'Only the same-origin, bodyless and queryless summary GET is allowed').toEqual(ids)
  }, stop() { page.off('request', record).off('response', response) } }
}

export type Reply = { status: number, body: unknown } | 'offline'
export function sendReply(route: Route, reply: Reply, marker?: string): Promise<void> {
  if (reply === 'offline') return route.abort('failed')
  return route.fulfill({ status: reply.status, json: reply.body, headers: marker ? { 'x-ui08-read': marker } : {} })
}
function readSlot(ws: string) {
  let release!: (reply: Reply) => void
  const reply = new Promise<Reply>(resolve => { release = resolve })
  return { ws, release, reply, request: null as Request | null }
}
interface HeldReads {
  arrived(index: number): Promise<void>
  usesToken(index: number, token: string): Promise<boolean>
  release(index: number, reply: Reply): void
  consumed(index: number): Promise<void>
  close(): Promise<void>
}
export async function holdReads(page: Page, ...ids: string[]): Promise<HeldReads> {
  const slots = ids.map(readSlot), pending: Promise<void>[] = []
  let next = 0
  // apiFetch awaits Response.json() for both success and error. A later frame runs AFTER its continuations and Vue's microtasks.
  await page.evaluate(() => {
    const parse = Response.prototype.json
    Response.prototype.json = function (this: Response) {
      const marker = this.headers.get('x-ui08-read')
      const result: Promise<unknown> = parse.call(this)
      if (!marker || !this.url.endsWith('/finance/summary')) return result
      return result.then(body => { requestAnimationFrame(() => performance.mark(marker)); return body })
    }
  })
  const matches = (url: URL) => SUMMARY_API.test(url.pathname)
  const handle = (route: Route) => {
    const slot = slots[next++]
    if (!slot || new URL(route.request().url()).pathname !== summaryApi(slot.ws)) return route.abort()
    slot.request = route.request()
    const marker = `ui08-read-${next}`
    const delivery = slot.reply.then(reply => sendReply(route, reply, marker))
    pending.push(delivery)
    return delivery
  }
  await page.route(matches, handle)
  return {
    arrived: index => expect.poll(() => slots[index].request !== null, { message: `Expected summary GET ${index + 1}` }).toBe(true),
    usesToken: async (index, token) => await slots[index].request?.headerValue('authorization') === `Bearer ${token}`,
    release: (index, reply) => slots[index].release(reply),
    async consumed(index) {
      await page.waitForFunction(marker => performance.getEntriesByName(marker, 'mark').length > 0, `ui08-read-${index + 1}`, { timeout: 10000 })
    },
    async close() {
      for (const slot of slots) slot.release('offline')
      await page.unroute(matches, handle)
      await Promise.all(pending)
    },
  }
}

export const refreshButton = (page: Page): Locator => page.getByRole('main').getByRole('button', { name: 'Muat ulang', exact: true })
export const returnLink = (page: Page): Locator => page.getByRole('link', { name: 'Kembali ke Draf sewa', exact: true })
const MONEY = /[0-9]+ unit tarif \([A-Z]{3}\)/g
function displayFields(row: CurrencyRow): string[] {
  return [...[row.total_minor, row.paid_minor, row.outstanding_minor].map(value => `${value} unit tarif (${row.currency})`),
    ...[row.payments_count, row.reversals_count, row.pending_payment_proposals_count, row.pending_reversal_proposals_count].map(String)]
}
async function readFields(page: Page, text: string): Promise<string[][]> {
  const table = page.getByRole('main').getByRole('table').filter({ has: page.getByRole('columnheader', { name: LABELS[0], exact: true }) })
  if (await table.count()) {
    const headers = await table.getByRole('columnheader').allInnerTexts()
    const rows = await table.getByRole('row').filter({ has: page.getByRole('cell') }).all()
    const cells = await Promise.all(rows.map(row => row.getByRole('cell').or(row.getByRole('rowheader')).allInnerTexts()))
    return LABELS.map(label => cells.map(values => (values[headers.indexOf(label)] ?? '').replace(/\s+/g, ' ').trim()))
  }
  // Read visible label/value pairs in server order, independent of card/list/definition markup.
  return LABELS.map((label, index) => Array.from(text.matchAll(new RegExp(
    `${label}\\s*:?\\s*(${index < 3 ? MONEY.source : '[0-9]+(?:[.,][0-9]+)*'})`, 'g')), match => match[1]))
}
export async function expectRows(page: Page, ...rows: CurrencyRow[]): Promise<void> {
  const main = page.getByRole('main')
  const fields = rows.map(displayFields)
  await expect(main).toContainText('Waktu pembacaan (UTC)')
  await expect(main.getByRole('status').filter({ hasText: /\S/ }).first()).toBeAttached()
  await expect.poll(async () => {
    const text = (await main.innerText()).replace(/\s+/g, ' ')
    return { fields: await readFields(page, text), money: (text.match(MONEY) ?? []).sort() }
  }).toEqual({ fields: LABELS.map((_, index) => fields.map(row => row[index])), money: fields.flatMap(row => row.slice(0, 3)).sort() })
  await expect(main).not.toContainText(EMPTY)
  await expect(main).not.toContainText(LOADING)
  await expect(main.getByRole('alert')).toHaveCount(0)
  await expect(refreshButton(page)).toBeEnabled()
}
export async function expectNoFigures(page: Page, state: 'empty' | 'loading' | 'error' | 'denied'): Promise<void> {
  const main = page.getByRole('main')
  if (state === 'empty') {
    await expect(main.getByText(EMPTY, { exact: true })).toBeVisible()
    await expect(main.getByRole('status').filter({ hasText: /\S/ }).first()).toBeAttached()
    await expect(refreshButton(page)).toBeEnabled()
  }
  if (state === 'loading') {
    await expect(main.getByRole('status').filter({ hasText: LOADING })).toBeVisible()
    await expect(main.getByRole('button', { name: /Muat ulang|Memuat/i })).toBeDisabled()
  }
  if (state === 'denied') await expect(main.getByRole('alert')).toContainText('Tidak ada izin')
  if (state === 'error') {
    await expect(main.getByRole('alert')).toHaveText(/\S/)
    await expect(refreshButton(page)).toBeEnabled()
  }
  if (state !== 'empty') await expect(main).not.toContainText(EMPTY)
  if (state !== 'loading') await expect(main).not.toContainText(LOADING)
  if (state === 'loading' || state === 'empty') await expect(main.getByRole('alert')).toHaveCount(0)
  await expect(main).not.toContainText(LARGE)
  await expect(main).not.toContainText(/[0-9]+\s*unit tarif/i)
  await expect(main).not.toContainText(new RegExp(`(?:${COUNT_LABELS.join('|')})\\s*:?\\s*[0-9]`))
  await expect(main.getByText(/^\s*\d+\s*$/)).toHaveCount(0)
}

const SHOTS = {
  'summary-360.png': [360, 640, 'active payments and pending reversal', 'live'],
  'summary-768.png': [768, 1024, 'active payments and pending reversal', 'live'],
  'summary-1440.png': [1440, 900, 'active payments and pending reversal', 'live'],
  'empty-360.png': [360, 640, 'no agreed-price facts', 'live'],
  'forbidden-360.png': [360, 640, 'nonmember denied', 'live'],
  'large-currencies-360.png': [360, 640, 'exact IDR and USD', 'injected'],
  'loading-360.png': [360, 640, 'held manual refresh', 'injected'],
  'error-360.png': [360, 640, 'failed manual refresh (500)', 'injected'],
} as const
type Shot = keyof typeof SHOTS
const DIRECTORY = '/repo/design/screenshots/f2-ui-08'
let captureDirectoryCreated = false
function requirePrivateCapture(): void {
  if (process.env.UI08_PRIVATE_CAPTURE !== '1' || process.env.CI !== '1' || process.platform !== 'linux' ||
      process.cwd() !== '/repo/apps/web' || !existsSync('/.dockerenv') || existsSync('/repo/.git')) throw new Error('UI08 capture requires the dedicated private runner')
}
export async function checkLayout(page: Page): Promise<void> {
  const main = page.getByRole('main')
  const fits = await main.evaluate(root => {
    const width = document.documentElement.clientWidth
    const overflows = (el: HTMLElement) => {
      const bounds = el.getBoundingClientRect()
      return bounds.left < -1 || bounds.right > width + 1 || (el.clientWidth > 0 && el.scrollWidth > el.clientWidth + 1)
    }
    const clipped = (el: HTMLElement) => el.clientHeight > 0 && el.scrollHeight > el.clientHeight + 1 && /hidden|clip/.test(getComputedStyle(el).overflowY)
    const visibleContent = Array.from(root.querySelectorAll<HTMLElement>('*')).filter(el => el.getBoundingClientRect().width > 1)
    return document.documentElement.scrollWidth <= width && !visibleContent.some(el => overflows(el) || clipped(el))
  })
  expect(fits, 'Summary content must wrap without horizontal overflow or clipped figures').toBe(true)
  for (const control of await returnLink(page).or(main.getByRole('button')).all()) {
    const box = await control.boundingBox()
    expect(box && box.width >= 48 && box.height >= 48, 'Summary control must be at least 48×48 CSS px').toBe(true)
  }
}
export async function captureState(page: Page, file: Shot, verifyState: () => Promise<void>): Promise<void> {
  const [width, height] = SHOTS[file]
  await page.setViewportSize({ width, height })
  await verifyState()
  await checkLayout(page)
  if (process.env.UI08_PRIVATE_CAPTURE !== '1') return
  requirePrivateCapture()
  if (!captureDirectoryCreated) {
    mkdirSync(DIRECTORY) // Exclusive, even across aborted runs. Never combine or overwrite evidence sets.
    captureDirectoryCreated = true
  }
  const png = await page.screenshot({ fullPage: true, animations: 'disabled', scale: 'css' })
  writeFileSync(`${DIRECTORY}/${file}`, png, { flag: 'wx' })
}
export function writeCaptureManifest(): void {
  requirePrivateCapture()
  const names = Object.keys(SHOTS) as Shot[]
  expect(readdirSync(DIRECTORY).sort()).toEqual([...names].sort())
  const files = names.map(file => {
    const [width, height, state, provenance] = SHOTS[file]
    const png = readFileSync(`${DIRECTORY}/${file}`)
    expect(png.subarray(0, 8).toString('hex')).toBe('89504e470d0a1a0a')
    const dimensions = { width: png.readUInt32BE(16), height: png.readUInt32BE(20) }
    expect(dimensions.width).toBe(width)
    expect(dimensions.height).toBeGreaterThanOrEqual(height)
    return { file, viewport: { width, height }, dimensions, state, provenance, sha256: createHash('sha256').update(png).digest('hex') }
  })
  const manifest = { generated_by: 'apps/web/e2e/paymentSummaryCases.ts (UI08, owner-flow / Chromium)', files }
  writeFileSync(`${DIRECTORY}/manifest.json`, JSON.stringify(manifest, null, 2) + '\n', { flag: 'wx' })
}
