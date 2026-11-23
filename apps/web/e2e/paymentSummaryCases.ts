import { randomUUID } from 'node:crypto'
import { expect, test, type APIRequestContext, type Locator, type Page } from '@playwright/test'
import { captureState, checkLayout, currencyRow, expectNoFigures, expectRows, GOLDEN, holdReads, invalidSummaries, mountedReader,
  observeReads, PASSTHROUGH, refreshButton, returnLink, sendReply, summaryApi, summaryBody, summaryPath, ZERO, type Reply } from './paymentSummaryFixtures'
import { registerPaymentSummaryNavigationCases } from './paymentSummaryNavigationCases'

interface WorkspaceSeed { ws: string, email: string, token: string }
interface SeedRow { id: string }
interface PaymentSeed extends WorkspaceSeed {
  customer: SeedRow
  item: SeedRow
  draft: SeedRow
  fact: SeedRow & { currency: string, total_minor: string, accepted_by: string }
}

type Setup<Seed> = (page: Page, tag: string) => Promise<Seed>
type FixtureReply = Promise<{ status: number, body: unknown }>
type PaymentInput = { amount_minor: string, currency: string, method: 'cash' | 'transfer', reference?: string | null }

export interface PaymentSummaryHelpers {
  r1Setup: Setup<WorkspaceSeed>
  paymentSetup: Setup<PaymentSeed>
  seedPaymentReady: Setup<PaymentSeed>
  gotoAuthed(page: Page, path: string, email: string): Promise<void>
  uiRegister(page: Page, email: string): Promise<void>
  uiLogin(page: Page, email: string): Promise<void>
  fillLoginPassword(page: Page): Promise<void>
  apiToken(request: APIRequestContext, email: string): Promise<string>
  uiCreateWorkspace(page: Page, name: string, email: string): Promise<string>
  uiInvite(page: Page, workspaceId: string, email: string, role: 'manager' | 'cashier', ownerEmail: string): Promise<string>
  inviteManager(page: Page, workspaceId: string, email: string, ownerEmail: string): Promise<void>
  seedPriceOne(request: APIRequestContext, workspaceId: string, token: string, itemId: string,
    from: string, to: string, amount: string, currency: string): Promise<SeedRow>
  seedDraftOne(request: APIRequestContext, workspaceId: string, token: string, customerId: string,
    itemId: string, from: string, to: string): Promise<SeedRow>
  acceptPrice(page: Page, workspaceId: string, draftId: string, email: string, key: string,
    versionId: string, total: string): FixtureReply
  proposePayment(page: Page, workspaceId: string, draftId: string, email: string, key: string, body: PaymentInput): FixtureReply
  approvePayment(page: Page, workspaceId: string, draftId: string, proposalId: string, email: string, key: string): FixtureReply
  proposeReversal(page: Page, workspaceId: string, draftId: string, paymentId: string,
    email: string, key: string, reason: string): FixtureReply
  approveReversal(page: Page, workspaceId: string, draftId: string, paymentId: string,
    proposalId: string, email: string, key: string): FixtureReply
  revokeFinance(workspaceId: string): void
}

const heading = (page: Page) => page.getByRole('heading', { name: 'Ringkasan pembayaran', exact: true })
const fixtureEmail = (role: string) => `ui08-${role}-${randomUUID()}@e2e.umnia.local`
const factOnly = currencyRow('IDR', '300000', '0', '300000')
const success = (body: unknown): Reply => ({ status: 200, body })
const failure = (status: number): Reply => ({ status, body: { error: 'Pembacaan gagal.' } })
const repeated = (ws: string, count: number): string[] => Array<string>(count).fill(ws)

function idOf(body: unknown): string {
  if (!body || typeof body !== 'object' || !('id' in body) || typeof body.id !== 'string') throw new Error('Expected a fixture record ID')
  return body.id
}
async function created(call: FixtureReply): Promise<string> {
  const result = await call
  expect(result.status, 'Fixture creation').toBe(201)
  return idOf(result.body)
}
async function joinWorkspace(h: PaymentSummaryHelpers, page: Page, seed: WorkspaceSeed, role: 'manager' | 'cashier') {
  const email = fixtureEmail(role)
  const [registered] = await Promise.all([
    page.waitForResponse(response => new URL(response.url()).pathname === '/api/auth/register' && response.status() === 201),
    h.uiRegister(page, email),
  ])
  const subject = idOf(await registered.json()), token = await h.apiToken(page.request, email)
  const invite = await page.request.post(`/api/workspaces/${seed.ws}/invites`, {
    headers: { Authorization: `Bearer ${seed.token}` }, data: { email, role },
  })
  expect(invite.status()).toBe(201)
  const invitation = await invite.json() as { token: string }
  const accept = await page.request.post('/api/invites/accept', {
    headers: { Authorization: `Bearer ${token}` }, data: { workspace_id: seed.ws, token: invitation.token },
  })
  expect(accept.status()).toBe(200)
  const membership = await accept.json() as { workspace_id: string, role: string }
  expect([membership.workspace_id, membership.role]).toEqual([seed.ws, role])
  return { email, subject, token }
}
async function enterSession(h: PaymentSummaryHelpers, page: Page, email: string) {
  await h.uiLogin(page, email)
  await expect(page.getByLabel('Nama workspace')).toBeVisible()
  return mountedReader(page)
}
async function seedUsdPayment(h: PaymentSummaryHelpers, page: Page, seed: PaymentSeed, manager: string): Promise<void> {
  const { ws, token, customer, item, email } = seed
  const start = '2047-03-01T00:00:00Z'
  const price = await h.seedPriceOne(page.request, ws, token, item.id, start, '2047-04-01T00:00:00Z', '250000', 'USD')
  const draft = await h.seedDraftOne(page.request, ws, token, customer.id, item.id, start, '2047-03-03T00:00:00Z')
  await created(h.acceptPrice(page, ws, draft.id, email, randomUUID(), price.id, '500000'))
  const proposal = await created(h.proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '200000', currency: 'USD', method: 'transfer' }))
  await created(h.approvePayment(page, ws, draft.id, proposal, manager, randomUUID()))
}

async function liveLifecycle(h: PaymentSummaryHelpers, page: Page): Promise<void> {
  const seed = await h.seedPaymentReady(page, 's8-life')
  const { ws, email, draft } = seed, manager = fixtureEmail('manager')
  const cashier = await joinWorkspace(h, page, seed, 'cashier')
  await h.inviteManager(page, ws, manager, email)
  await seedUsdPayment(h, page, seed, manager)
  const proposal = await created(h.proposePayment(page, ws, draft.id, email, randomUUID(), { amount_minor: '100000', currency: 'IDR', method: 'cash' }))
  const reader = await enterSession(h, page, manager), reads = observeReads(page)
  let count = 0
  const read = async (idr: typeof factOnly) => {
    if (count === 0) await reader.open(ws)
    else await refreshButton(page).click()
    count++
    await expectRows(page, idr, GOLDEN[1])
    await expect.poll(() => [...reads.statuses]).toEqual(Array<number>(count).fill(200))
    await reads.check(...repeated(ws, count))
  }
  await read({ ...factOnly, pending_payment_proposals_count: 1 })
  const payment = await created(h.approvePayment(page, ws, draft.id, proposal, manager, randomUUID()))
  const posted = currencyRow('IDR', '300000', '100000', '200000', [1, 0, 0, 0])
  await read(posted)
  const propose = () => created(h.proposeReversal(page, ws, draft.id, payment, email, randomUUID(), 'Salah catat'))
  const reversal = await propose()
  await propose() // An unposted competing proposal is still counted even after the other one posts.
  const pending = { ...posted, pending_reversal_proposals_count: 2 }
  await read(pending)
  for (const file of ['summary-360.png', 'summary-768.png', 'summary-1440.png'] as const) {
    await captureState(page, file, () => expectRows(page, pending, GOLDEN[1]))
  }
  await created(h.approveReversal(page, ws, draft.id, payment, reversal, manager, randomUUID()))
  const reversed = { ...factOnly, payments_count: 1, reversals_count: 1, pending_reversal_proposals_count: 1 }
  await read(reversed)
  h.revokeFinance(ws)
  await read(reversed)
  reads.stop()
  const cashierReader = await enterSession(h, page, cashier.email), cashierReads = observeReads(page)
  await cashierReader.open(ws)
  await expectRows(page, reversed, GOLDEN[1])
  await cashierReads.check(ws)
  expect(cashierReads.statuses).toEqual([200])
}

function registerLive(h: PaymentSummaryHelpers): void {
  test('empty draft list links to the workspace payment summary', async ({ page }) => {
    const seed = await h.r1Setup(page, 's8-entry'), cashier = await joinWorkspace(h, page, seed, 'cashier')
    const rentals = `/workspaces/${seed.ws}/rentals`
    await h.gotoAuthed(page, `/workspaces/${seed.ws}/modules`, cashier.email)
    const nav = page.getByRole('navigation', { name: 'Navigasi workspace', exact: true })
    await nav.getByRole('link', { name: 'Draf sewa', exact: true }).click()
    await expect(page.getByText('Belum ada draf.', { exact: true })).toBeVisible()
    const link = page.getByRole('link', { name: 'Ringkasan pembayaran', exact: true })
    await expect(link).toHaveCount(1)
    await expect(nav.getByRole('link', { name: 'Ringkasan pembayaran', exact: true })).toHaveAttribute('href', summaryPath(seed.ws))
    const reads = observeReads(page)
    await link.click()
    await expect(page).toHaveURL(summaryPath(seed.ws))
    await captureState(page, 'empty-360.png', () => expectNoFigures(page, 'empty'))
    await expect(returnLink(page)).toHaveAttribute('href', rentals)
    await reads.check(seed.ws)
    expect(reads.statuses).toEqual([200])
    reads.stop() // The destination rental list legitimately loads its workspace, pickers and drafts.
    await returnLink(page).click()
    await expect(page).toHaveURL(rentals)
  })
  test('owner reads a fact before Finance; another workspace denies without stale figures or logout', async ({ page }) => {
    const other = await h.r1Setup(page, 's8-other'), seed = await h.paymentSetup(page, 's8-fact')
    const reader = await enterSession(h, page, seed.email), retained = await reader.retained(), reads = observeReads(page)
    await reader.open(seed.ws)
    await expectRows(page, factOnly)
    await reads.check(seed.ws)
    await reader.open(other.ws)
    await captureState(page, 'forbidden-360.png', () => expectNoFigures(page, 'denied'))
    await expect(page).toHaveURL(summaryPath(other.ws))
    await expect(page.getByRole('button', { name: 'Keluar', exact: true })).toBeVisible()
    expect(await retained()).toBe(true)
    await reads.check(seed.ws, other.ws)
    expect(reads.statuses).toEqual([200, 403])
  })
  test('manager reads pending, posted, reversed and post-revocation totals in two currencies', async ({ page }) => liveLifecycle(h, page))
}

async function submitLogin(h: PaymentSummaryHelpers, page: Page, email: string): Promise<void> {
  await page.getByLabel('Email', { exact: true }).fill(email)
  await h.fillLoginPassword(page)
  await page.locator('form').getByRole('button', { name: 'Masuk', exact: true }).click()
}
async function expectLogin(page: Page, path: string): Promise<void> {
  await expect(page).toHaveURL(url => url.pathname === '/login' && !url.hash &&
    url.searchParams.size === 1 && url.searchParams.get('next') === path)
  await expect(page.getByRole('button', { name: 'Keluar', exact: true })).toHaveCount(0)
}
function registerLogin(h: PaymentSummaryHelpers): void {
  test('only the exact local summary is a successful login destination', async ({ page }) => {
    const { ws, email } = await h.r1Setup(page, 's8-next'), path = summaryPath(ws)
    const invalid = [summaryPath('not-a-uuid'), `${path}?currency=IDR`, `${path}#hash`, `${path}/extra`,
      `${path}%2Fextra`, `//${new URL(page.url()).host}${path}`]
    for (const next of [path, ...invalid]) {
      await page.goto(`/login?next=${encodeURIComponent(next)}`)
      await submitLogin(h, page, email)
      await expect(page).toHaveURL(next === path ? path : '/workspaces/new')
    }
  })
  test('missing session sends no GET; current 401 clears session and login returns to the same reader', async ({ page }) => {
    const { ws, email } = await h.paymentSetup(page, 's8-401'), path = summaryPath(ws)
    await enterSession(h, page, email)
    const guest = observeReads(page)
    await page.goto(path)
    await expectLogin(page, path)
    await guest.check()
    guest.stop()
    const reads = observeReads(page, true)
    await submitLogin(h, page, email)
    await expectRows(page, factOnly)
    await reads.check(ws)
    const reader = await mountedReader(page)
    // Invalidate only this outgoing read. The still-current app session is cleared by the real API's 401.
    await page.route(summaryApi(ws), route => route.continue({ headers: { authorization: `Bearer ${randomUUID()}` } }), { times: 1 })
    await refreshButton(page).click()
    await expectLogin(page, path)
    expect(await reader.signedOut()).toBe(true)
    await expect(page.getByRole('main')).not.toContainText('unit tarif')
    await reads.check(ws, ws)
    expect(reads.statuses).toEqual([200, 401])
    reads.stop()
    const resumed = observeReads(page, true)
    await submitLogin(h, page, email)
    await expect(page).toHaveURL(path)
    await expectRows(page, factOnly)
    await resumed.check(ws)
    expect(resumed.statuses).toEqual([200])
  })
}

function appearance(control: Locator): Promise<string> {
  return control.evaluate(el => {
    const style = getComputedStyle(el)
    return [style.color, style.background, style.border, style.boxShadow, style.outline, style.opacity,
      style.filter, style.transform, style.translate, style.textDecoration, style.fontWeight].join('|')
  })
}
async function checkContrast(control: Locator): Promise<void> {
  const ratios = await control.evaluate(el => {
    const swatch = document.createElement('canvas').getContext('2d', { willReadFrequently: true })
    if (!swatch) throw new Error('Cannot sample the reader palette')
    const paint = (color: string) => { swatch.fillStyle = color; swatch.fillRect(0, 0, 1, 1) }
    const luminance = () => {
      const [r, g, b] = Array.from(swatch.getImageData(0, 0, 1, 1).data.slice(0, 3), byte => byte / 255)
        .map(value => value <= 0.04045 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4)
      return 0.2126 * r + 0.7152 * g + 0.0722 * b
    }
    const ratio = (a: number, b: number) => (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05)
    const colors = ['white']
    for (let parent = el.parentElement; parent; parent = parent.parentElement) colors.splice(1, 0, getComputedStyle(parent).backgroundColor)
    colors.forEach(paint)
    const adjacent = luminance(), style = getComputedStyle(el)
    paint(style.outlineColor)
    const ring = el.matches(':focus-visible') && parseFloat(style.outlineWidth) > 0 ? ratio(adjacent, luminance()) : null
    colors.forEach(paint)
    paint(style.backgroundColor)
    const surface = luminance()
    paint(style.color)
    return { text: ratio(surface, luminance()), ring }
  })
  expect(ratios.text, 'Ordinary text contrast').toBeGreaterThanOrEqual(4.5)
  if (ratios.ring !== null) expect(ratios.ring, 'Focus outline contrast').toBeGreaterThanOrEqual(3)
}
async function pressRefresh(page: Page): Promise<void> {
  const refresh = refreshButton(page), back = returnLink(page), controls = [back, refresh]
  await page.emulateMedia({ reducedMotion: 'reduce' })
  await heading(page).click()
  const idle = await Promise.all(controls.map(appearance)), visited = new Set<number>()
  const maxTabs = 16
  for (let tab = 0; tab < maxTabs && visited.size < controls.length; tab++) {
    await page.keyboard.press('Tab')
    for (const [index, control] of controls.entries()) {
      if (!await control.evaluate(el => el.matches(':focus-visible'))) continue
      expect(await appearance(control), 'Keyboard focus needs a visible change').not.toBe(idle[index])
      await checkContrast(control)
      visited.add(index)
    }
  }
  expect(visited.size, 'Both the back link and refresh must be reachable with Tab').toBe(controls.length)
  await heading(page).click()
  await refresh.hover()
  await expect.poll(() => appearance(refresh), { message: 'Visible hover feedback' }).not.toBe(idle[1])
  const hover = await appearance(refresh)
  await page.mouse.down()
  try { await expect.poll(() => appearance(refresh), { message: 'Visible pressed feedback' }).not.toBe(hover) }
  finally { await page.mouse.up() }
}
async function controlledInteraction(h: PaymentSummaryHelpers, page: Page): Promise<void> {
  const { ws, email } = await h.r1Setup(page, 's8-large')
  const reader = await enterSession(h, page, email)
  await page.setViewportSize({ width: 360, height: 640 })
  await page.route(summaryApi(ws), route => sendReply(route, success(summaryBody(ws))), { times: 1 })
  const reads = observeReads(page)
  await reader.open(ws)
  await captureState(page, 'large-currencies-360.png', () => expectRows(page, ...GOLDEN))
  await expect(page.getByRole('main')).toContainText(/11[:.]17/)
  await checkContrast(page.getByText('Waktu pembacaan (UTC)').first())
  await reads.check(ws)
  const held = await holdReads(page, ws, ws, ws)
  try {
    await pressRefresh(page)
    await held.arrived(0)
    await expectNoFigures(page, 'loading')
    const disabled = page.getByRole('main').getByRole('button', { disabled: true })
    const box = await disabled.boundingBox()
    if (!box) throw new Error('Disabled refresh has no touch target')
    await page.mouse.dblclick(box.x + box.width / 2, box.y + box.height / 2)
    await captureState(page, 'loading-360.png', () => expectNoFigures(page, 'loading'))
    await reads.check(ws, ws)
    held.release(0, failure(500))
    await captureState(page, 'error-360.png', () => expectNoFigures(page, 'error'))
    await refreshButton(page).click()
    await held.arrived(1)
    await expectNoFigures(page, 'loading')
    held.release(1, success(summaryBody(ws, [ZERO])))
    await expectRows(page, ZERO)
    await reads.check(ws, ws, ws)
    await refreshButton(page).click()
    await held.arrived(2)
    held.release(2, success(summaryBody(ws, [])))
    await expectNoFigures(page, 'empty')
    await reads.check(ws, ws, ws, ws)
  } finally { await held.close() }
}
function registerControlled(h: PaymentSummaryHelpers): void {
  test('large exact currencies, accessible refresh states, 500 retry, zero row versus empty', async ({ page }) => controlledInteraction(h, page))
  test('one invalid row invalidates the whole read; malformed, network and unexpected status retry cleanly', async ({ page }) => {
    const { ws, email } = await h.r1Setup(page, 's8-invalid'), reader = await enterSession(h, page, email)
    const rows = [GOLDEN[1], PASSTHROUGH, GOLDEN[0]], valid = success(summaryBody(ws, rows))
    let reply = valid
    await page.setViewportSize({ width: 360, height: 640 })
    await page.route(summaryApi(ws), route => sendReply(route, reply))
    const reads = observeReads(page)
    await reader.open(ws)
    await expectRows(page, ...rows)
    await checkLayout(page)
    let count = 1
    await reads.check(ws)
    const faults: [string, Reply][] = invalidSummaries(ws).map(([name, body]) => [name, success(body)])
    faults.push(['network loss', 'offline'], ['unexpected 201', { status: 201, body: summaryBody(ws) }])
    for (const [name, fault] of faults) {
      await test.step(name, async () => {
        reply = fault
        await refreshButton(page).click()
        await expectNoFigures(page, 'error')
        await reads.check(...repeated(ws, ++count))
        reply = valid
        await refreshButton(page).click()
        await expectRows(page, ...rows)
        await reads.check(...repeated(ws, ++count))
      })
    }
  })
}

type Change = 'workspace' | 'actor'
async function staleLast(h: PaymentSummaryHelpers, page: Page, change: Change, status: number): Promise<void> {
  const seed = await h.r1Setup(page, `s8-${change}-${status}`)
  const actor = change === 'actor' ? await joinWorkspace(h, page, seed, 'manager') : null
  const nextWs = actor ? seed.ws : await h.uiCreateWorkspace(page, 'UI08 workspace B', seed.email)
  const reader = await enterSession(h, page, seed.email), original = await reader.retained()
  const held = await holdReads(page, seed.ws, nextWs), reads = observeReads(page)
  const finish = async () => {
    await held.arrived(1)
    const retained = await reader.retained()
    held.release(1, success(summaryBody(nextWs, [ZERO])))
    await held.consumed(1)
    if (actor) expect(await held.usesToken(1, actor.token), 'B uses the replacement actor').toBe(true)
    await expectRows(page, ZERO)
    held.release(0, status === 200 ? success(summaryBody(seed.ws)) : failure(status))
    await held.consumed(0)
    await expectRows(page, ZERO)
    expect(await retained(), 'Late A must not replace or clear B’s session').toBe(true)
    expect(await original()).toBe(actor === null)
    await expect(page).toHaveURL(summaryPath(nextWs))
    await reads.check(seed.ws, nextWs)
  }
  const switchContext = async () => {
    await expect(heading(page)).toBeVisible()
    await held.arrived(0)
    await expectNoFigures(page, 'loading')
    const switching = actor ? reader.replace(actor.token, actor.subject) : reader.open(nextWs)
    await Promise.all([switching, finish()])
  }
  try { await Promise.all([reader.open(seed.ws), switchContext()]) }
  finally { await held.close() }
}
async function staleFirst(h: PaymentSummaryHelpers, page: Page): Promise<void> {
  const { ws, email } = await h.r1Setup(page, 's8-token')
  const replacement = await h.apiToken(page.request, email)
  const reader = await enterSession(h, page, email)
  const held = await holdReads(page, ws, ws), reads = observeReads(page)
  const finish = async () => {
    await expect(heading(page)).toBeVisible()
    await held.arrived(0)
    await reader.replace(replacement) // Same mounted page, workspace and subject; only the token changes.
    await held.arrived(1)
    const retained = await reader.retained()
    held.release(0, success(summaryBody(ws)))
    await held.consumed(0)
    await expectNoFigures(page, 'loading')
    await expect(page).toHaveURL(summaryPath(ws))
    expect(await retained()).toBe(true)
    await reads.check(ws, ws)
    held.release(1, success(summaryBody(ws, [ZERO])))
    await held.consumed(1)
    expect(await held.usesToken(0, replacement)).toBe(false)
    expect(await held.usesToken(1, replacement)).toBe(true)
    await expectRows(page, ZERO)
    expect(await retained()).toBe(true)
    await reads.check(ws, ws)
  }
  try { await Promise.all([reader.open(ws), finish()]) }
  finally { await held.close() }
}
export function registerPaymentSummaryCases(helpers: PaymentSummaryHelpers): void {
  test.describe('UI08', () => {
    registerLive(helpers)
    registerLogin(helpers)
    registerControlled(helpers)
    registerPaymentSummaryNavigationCases({ helpers, enterSession, expectLogin, submitLogin, factOnly })
    for (const change of ['workspace', 'actor'] as const) {
      for (const status of [200, 401, 403, 500]) {
        test(`late A ${status} cannot overwrite B after a ${change} change`, async ({ page }) => staleLast(helpers, page, change, status))
      }
    }
    test('same-subject token replacement stays busy after A is consumed and until B settles', async ({ page }) => staleFirst(helpers, page))
  })
}
