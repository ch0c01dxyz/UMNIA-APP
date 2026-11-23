import { expect, test, type Locator, type Page, type Request } from '@playwright/test'
import type { PaymentSummaryHelpers } from './paymentSummaryCases'
import { expectNoFigures, expectRows, GOLDEN, holdReads, mountedReader, OBSERVED, observeReads, refreshButton,
  returnLink, sendReply, summaryApi, summaryBody, summaryPath, ZERO, type CurrencyRow } from './paymentSummaryFixtures'

type Reader = Awaited<ReturnType<typeof mountedReader>>
type Holds = Awaited<ReturnType<typeof holdReads>>
interface NavigationFixtures {
  helpers: PaymentSummaryHelpers
  factOnly: CurrencyRow
  enterSession(helpers: PaymentSummaryHelpers, page: Page, email: string): Promise<Reader>
  expectLogin(page: Page, path: string): Promise<void>
  submitLogin(helpers: PaymentSummaryHelpers, page: Page, email: string): Promise<void>
}
const SKIP_HASH = '#konten'
const MAX_TABS = 12
const skipLink = (page: Page): Locator => page.getByRole('link', { name: 'Lewat ke konten utama', exact: true })
  .and(page.locator('a[href="#konten"]'))
const mainTarget = (page: Page): Locator => page.locator('main#konten')

async function startReport(fixtures: NavigationFixtures, page: Page, tag: string) {
  const { ws, email } = await fixtures.helpers.r1Setup(page, tag)
  const reader = await fixtures.enterSession(fixtures.helpers, page, email)
  return { page, reader, ws, email, path: summaryPath(ws) }
}
type Report = Awaited<ReturnType<typeof startReport>>

async function tabTo(page: Page, target: Locator): Promise<void> {
  for (let tab = 0; tab < MAX_TABS; tab++) {
    await page.keyboard.press('Tab')
    if (await target.evaluate(element => element === document.activeElement)) break
  }
  await expect(target).toBeFocused()
}
async function expectLocation({ page, reader }: Report, path: string): Promise<void> {
  await expect.poll(() => reader.currentFullPath()).toBe(path)
  await expect(page).toHaveURL(path)
}
async function enterSkip(report: Report): Promise<void> {
  await expect(skipLink(report.page)).toBeFocused()
  await report.page.keyboard.press('Enter')
  await expectLocation(report, report.path + SKIP_HASH)
  await expect(mainTarget(report.page)).toBeFocused()
}
async function settle(held: Holds, index: number, status: 200 | 401 | 403 | 500, body: unknown = {}): Promise<void> {
  held.release(index, { status, body })
  await held.consumed(index)
}
async function expectPending(page: Page, held: Holds, index: number): Promise<void> {
  await held.arrived(index)
  await expectNoFigures(page, 'loading')
  await expect(refreshButton(page)).toHaveJSProperty('disabled', true)
}
async function startPopulated({ page, reader, ws }: Report, row: CurrencyRow) {
  await page.route(summaryApi(ws), route => sendReply(route, { status: 200, body: summaryBody(ws, [row]) }), { times: 1 })
  const reads = observeReads(page)
  await reader.open(ws)
  await expectRows(page, row)
  await reads.check(ws)
  return reads
}
async function rememberFigures(page: Page, row: CurrencyRow) {
  const region = page.getByRole('region', { name: `Mata uang ${row.currency}`, exact: true })
  const time = mainTarget(page).locator('time')
  await expect(time).toHaveAttribute('datetime', OBSERVED)
  await expect(time).toHaveText(/11[.:]17[.:]04/)
  const nodes = await Promise.all([region, time].map(locator => locator.evaluateHandle(node => node)))
  const observation = await time.innerText()
  return async () => {
    await expectRows(page, row)
    await expect(time).toHaveAttribute('datetime', OBSERVED)
    await expect(time).toHaveText(observation)
    for (const node of nodes) expect(await node.evaluate(element => element.isConnected)).toBe(true)
  }
}

async function finishSkipChecks(checks: Promise<void>[], cleanup?: () => Promise<void>): Promise<void> {
  const failures: unknown[] = []
  const record = (error: unknown): void => { failures.push(error) }
  try { await Promise.all(checks.map(check => check.catch(record))) }
  finally { try { await cleanup?.() } catch (error) { record(error) } }
  if (failures.length === 1) throw failures[0]
  if (failures.length > 1) throw new AggregateError(failures, 'Native skip checks or cleanup failed', { cause: failures[0] })
}
async function checkPendingSkip(report: Report, held: Holds, index: number, reads: ReturnType<typeof observeReads>): Promise<void> {
  const { page, path, ws } = report
  await expectPending(page, held, index)
  await tabTo(page, skipLink(page))
  await expectLocation(report, path)
  await enterSkip(report)
  await expectPending(page, held, index)
  await reads.check(...Array<string>(index + 1).fill(ws))
}
async function verifySkipReads(report: Report, row: CurrencyRow): Promise<void> {
  const { page, reader, path, ws } = report, retained = await reader.retained()
  const held = await holdReads(page, ws, ws), reads = observeReads(page)
  let requested = 0, released = 0
  const noteRequest = (request: Request) => { if (new URL(request.url()).pathname === summaryApi(ws)) requested++ }
  page.on('request', noteRequest)
  const skipPending = async (index: number, result: CurrencyRow) => {
    await held.arrived(index)
    // Even a broken skip must let the browser consume this response before the route is removed.
    await finishSkipChecks([checkPendingSkip(report, held, index, reads)], () => {
      released = index + 1
      return settle(held, index, 200, summaryBody(ws, [result]))
    })
    await expectRows(page, result)
    await expectRetainedFocus(report, retained, mainTarget(page), path + SKIP_HASH)
  }
  const checkReport = async () => {
    await finishSkipChecks([reader.open(ws), skipPending(0, row)])
    const unchanged = await rememberFigures(page, row)
    await page.goBack()
    await expectLocation(report, path)
    await tabTo(page, skipLink(page))
    await unchanged()
    await expectRetainedFocus(report, retained, skipLink(page))
    await reads.check(ws)
    await enterSkip(report)
    await unchanged()
    await page.keyboard.press('Tab')
    await expectRetainedFocus(report, retained, returnLink(page), path + SKIP_HASH)
    await reads.check(ws)
    await page.goBack()
    await expectLocation(report, path)
    await tabTo(page, refreshButton(page))
    await page.keyboard.press('Enter')
    await skipPending(1, GOLDEN[1])
    await reads.check(ws, ws)
  }
  await finishSkipChecks([checkReport()], async () => {
    // An unexpected GET can occupy the manual slot before that keyboard action is reached.
    const extra = [row, GOLDEN[1]].slice(released, requested).map(async (result, offset) => {
      const index = released + offset
      await held.arrived(index)
      await settle(held, index, 200, summaryBody(ws, [result]))
    })
    await finishSkipChecks(extra, async () => { page.off('request', noteRequest); reads.stop(); await held.close() })
  })
}

async function openGuest(fixtures: NavigationFixtures, page: Page, target: string, next: string): Promise<void> {
  await page.goto('/login') // Start counting on this origin, with a fresh document and no in-memory session.
  const reads = observeReads(page)
  await page.goto(target)
  await fixtures.expectLogin(page, next)
  await reads.check()
  reads.stop()
}
async function verifyInvalidRoutes(fixtures: NavigationFixtures, page: Page): Promise<void> {
  const report = await startReport(fixtures, page, 'nav-c1'), { ws, reader, path, email } = report
  const suffixes = ['?currency=IDR', `?currency=IDR${SKIP_HASH}`, '#other'], reads = await startPopulated(report, fixtures.factOnly)
  for (const suffix of suffixes) {
    await reader.openRawPath(path + suffix)
    await expectLocation(report, path + suffix)
    const main = mainTarget(page)
    await expect(main.getByRole('alert')).toContainText('Tautan workspace tidak valid')
    await expect(main).not.toContainText(/unit tarif|Waktu pembacaan \(UTC\)/)
    await expect(main.getByText(/^\s*\d+\s*$/)).toHaveCount(0)
    await reads.check(ws)
  }
  reads.stop()
  for (const suffix of suffixes) await openGuest(fixtures, page, path + suffix, '/workspaces/new')
  await page.goto(`/login?next=${encodeURIComponent(path + SKIP_HASH)}`)
  const resumed = observeReads(page, true)
  await fixtures.submitLogin(fixtures.helpers, page, email)
  await expect(page).toHaveURL('/workspaces/new')
  await expect(page.getByLabel('Nama workspace')).toBeVisible()
  await resumed.check()
  resumed.stop()
}

async function resumeReport(fixtures: NavigationFixtures, page: Page, { ws, email, path }: Report): Promise<void> {
  const reads = observeReads(page, true)
  await fixtures.submitLogin(fixtures.helpers, page, email)
  await expect(page).toHaveURL(path)
  await expectNoFigures(page, 'empty')
  await reads.check(ws)
  expect(reads.statuses).toEqual([200])
  reads.stop()
}
async function verifySkipLogin(fixtures: NavigationFixtures, report: Report): Promise<void> {
  const { page, reader, path, ws } = report, guest = await page.context().newPage()
  try {
    await openGuest(fixtures, guest, path + SKIP_HASH, path)
    await resumeReport(fixtures, guest, report)
    const guestRetained = await (await mountedReader(guest)).retained()
    await page.bringToFront()
    await expectLocation(report, path + SKIP_HASH)
    const expired = observeReads(page)
    await page.route(summaryApi(ws), route => sendReply(route, { status: 401, body: {} }), { times: 1 })
    await tabTo(page, refreshButton(page))
    await page.keyboard.press('Enter')
    await fixtures.expectLogin(page, path)
    expect(await reader.signedOut()).toBe(true)
    expect(await guestRetained()).toBe(true)
    await expired.check(ws)
    expect(expired.statuses).toEqual([401])
    expired.stop()
    await resumeReport(fixtures, page, report)
  } finally { await guest.close() }
}

async function verifyRentalsContinuation(fixtures: NavigationFixtures, page: Page): Promise<void> {
  let seed = await fixtures.helpers.r1Setup(page, 'nav-r2')
  const maxCandidates = 3
  for (let attempt = 1; !/[a-f].*[a-f]/.test(seed.ws) && attempt < maxCandidates; attempt++) {
    seed = { ...seed, ws: await fixtures.helpers.uiCreateWorkspace(page, `UI08 mixed spelling ${attempt}`, seed.email) }
  }
  const mixedId = seed.ws.replace(/[a-f]/, letter => letter.toUpperCase())
  expect(mixedId, 'Disposable UUID must have both letter cases').toMatch(/[A-F].*[a-f]/)
  const reader = await fixtures.enterSession(fixtures.helpers, page, seed.email)
  const origin = new URL(page.url()).origin, endpoint = origin + summaryApi(mixedId), plainReads: boolean[] = []
  const record = (request: Request) => {
    const url = new URL(request.url())
    const asset = url.origin === origin && (url.pathname.startsWith('/_nuxt/') || url.pathname.endsWith('/_payload.json'))
    const data = !asset && ['fetch', 'xhr'].includes(request.resourceType())
    if (!data && !/^\/api(?:\/|$)/.test(url.pathname) && ['GET', 'HEAD'].includes(request.method())) return
    plainReads.push(request.url() === endpoint && request.method() === 'GET' && request.postDataBuffer() === null)
  }
  page.on('request', record)
  try {
    const [response] = await Promise.all([
      page.waitForResponse(reply => reply.url() === endpoint && reply.request().method() === 'GET'),
      reader.open(mixedId),
    ])
    expect(response.status()).toBe(200)
    const body: unknown = await response.json()
    expect(body).toMatchObject({ workspace_id: seed.ws, currencies: [] })
    expect(seed.ws).toBe(mixedId.toLowerCase())
    await expect(page).toHaveURL(summaryPath(mixedId))
    await expectNoFigures(page, 'empty')
    expect(plainReads, 'Only the exact mixed-case plain GET').toEqual([true])
  } finally { page.off('request', record) }
  const href = await returnLink(page).getAttribute('href') ?? ''
  expect(href).toBe(`/workspaces/${seed.ws}/rentals`)
  const guest = await page.context().newPage()
  try {
    await openGuest(fixtures, guest, href, href)
    await fixtures.submitLogin(fixtures.helpers, guest, seed.email)
    await expect(guest).toHaveURL(href)
    await expect(guest.getByRole('heading', { name: 'Draf sewa', exact: true })).toBeVisible()
  } finally { await guest.close() }
}

async function expectRetainedFocus(report: Report, retained: () => Promise<boolean>, target: Locator, path = report.path): Promise<void> {
  await expectLocation(report, path)
  expect(await retained()).toBe(true)
  await expect(target).toBeFocused()
}
async function verifyKeyboardRetry(fixtures: NavigationFixtures, page: Page): Promise<void> {
  const report = await startReport(fixtures, page, 'nav-r3'), { ws, reader } = report
  const retained = await reader.retained(), reads = await startPopulated(report, fixtures.factOnly)
  const held = await holdReads(page, ws, ws, ws), refresh = refreshButton(page), back = returnLink(page)
  const activate = () => refresh.evaluate((button: HTMLButtonElement) => button.click())
  try {
    await tabTo(page, refresh)
    await page.keyboard.press('Enter')
    await expectPending(page, held, 0)
    await reads.check(ws, ws)
    await settle(held, 0, 403)
    await expectNoFigures(page, 'denied')
    await expect(refresh).toBeEnabled()
    await reads.check(ws, ws)
    await expectRetainedFocus(report, retained, refresh)
    await page.keyboard.press('Space')
    await expectPending(page, held, 1)
    await tabTo(page, back)
    await reads.check(ws, ws, ws)
    await settle(held, 1, 403)
    await expectNoFigures(page, 'denied')
    await expect(refresh).toBeEnabled()
    await expectRetainedFocus(report, retained, back)
    await reads.check(ws, ws, ws)
    await activate()
    await expectPending(page, held, 2)
    await activate()
    await expect(back).toBeFocused()
    await reads.check(ws, ws, ws, ws)
    await settle(held, 2, 500)
    await expectNoFigures(page, 'error')
    await expect(mainTarget(page).getByRole('alert')).toContainText('Ringkasan tidak dapat ditampilkan')
    await expectRetainedFocus(report, retained, back)
    await reads.check(ws, ws, ws, ws)
  } finally { reads.stop(); await held.close() }
}

async function verifyStaleManualRead(fixtures: NavigationFixtures, page: Page): Promise<void> {
  const report = await startReport(fixtures, page, 'nav-c2'), { ws, reader } = report
  const replacement = await fixtures.helpers.apiToken(page.request, report.email)
  const reads = await startPopulated(report, fixtures.factOnly)
  const held = await holdReads(page, ws, ws), back = returnLink(page)
  try {
    await tabTo(page, refreshButton(page))
    await page.keyboard.press('Enter')
    await expectPending(page, held, 0)
    await reads.check(ws, ws)
    await reader.replace(replacement)
    await expectPending(page, held, 1)
    expect(await held.usesToken(0, replacement)).toBe(false)
    expect(await held.usesToken(1, replacement)).toBe(true)
    const retained = await reader.retained()
    await tabTo(page, back)
    await expectRetainedFocus(report, retained, back)
    await settle(held, 0, 401)
    await expectPending(page, held, 1)
    await expectRetainedFocus(report, retained, back)
    await reads.check(ws, ws, ws)
    await settle(held, 1, 200, summaryBody(ws, [ZERO]))
    await expectRows(page, ZERO)
    await expectRetainedFocus(report, retained, back)
    await reads.check(ws, ws, ws)
  } finally { reads.stop(); await held.close() }
}

export function registerPaymentSummaryNavigationCases(fixtures: NavigationFixtures): void {
  test('NAV08-R1: native skip', async ({ page }) => {
    const report = await startReport(fixtures, page, 'nav-r1')
    await verifySkipReads(report, fixtures.factOnly)
    await verifySkipLogin(fixtures, report)
  })
  test('NAV08-R2: rentals continuation', async ({ page }) => verifyRentalsContinuation(fixtures, page))
  test('NAV08-R3: keyboard retry', async ({ page }) => verifyKeyboardRetry(fixtures, page))
  test('NAV08-C1: invalid routes', async ({ page }) => verifyInvalidRoutes(fixtures, page))
  test('NAV08-C2: stale manual 401', async ({ page }) => verifyStaleManualRead(fixtures, page))
}
