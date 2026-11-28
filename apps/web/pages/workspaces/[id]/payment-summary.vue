<script setup lang="ts">
import { onBeforeRouteLeave, onBeforeRouteUpdate, type RouteLocationNormalized } from 'vue-router'
import tokens from '~/assets/payment-summary/tokens.json'
import { ApiError, apiFetch, loginUrl, redirectToLogin, resolveLoginTarget } from '~/lib/api'
import { decodePaymentSummary, type PaymentSummary } from '~/lib/paymentSummary'

definePageMeta({ key: route => route.path })
useHead({ title: 'Ringkasan pembayaran — UMNIA' })

const route = useRoute()
const router = useRouter()
const session = useNuxtApp().$session ?? null
const pagePath = route.path
const candidateId = route.params.id
const workspaceId = typeof candidateId === 'string' && pagePath === `/workspaces/${candidateId}/payment-summary` &&
  resolveLoginTarget(pagePath) === pagePath ? candidateId : null

function resolveReaderPath(fullPath: string): string {
  return workspaceId !== null && fullPath === `${pagePath}#konten` ? pagePath : fullPath
}

const amounts = [
  ['total_minor', 'Jumlah harga disepakati'], ['paid_minor', 'Pembayaran tercatat aktif'],
  ['outstanding_minor', 'Sisa plafon acuan']
] as const
const countGroups = [
  { title: 'Catatan yang sudah dibukukan', fields: [
    ['payments_count', 'Jumlah catatan pembayaran'], ['reversals_count', 'Jumlah catatan pembalikan']
  ] },
  { title: 'Usulan tanpa catatan', fields: [
    ['pending_payment_proposals_count', 'Usulan pembayaran yang belum menghasilkan catatan'],
    ['pending_reversal_proposals_count', 'Usulan pembalikan yang belum menghasilkan catatan']
  ] }
] as const
const issues = {
  invalid: ['Tautan workspace tidak valid', 'Buka ringkasan dari halaman Draf sewa di workspace Anda.'],
  denied: ['Tidak ada izin', 'Akun Anda tidak dapat melihat ringkasan pembayaran di workspace ini.'],
  failed: ['Ringkasan tidak dapat ditampilkan', 'Pilih Muat ulang untuk mencoba membaca data kembali.']
} as const
const RGB_CHANNEL_MAX = 255
const pageStyle = Object.fromEntries(Object.entries(tokens).map(([name, { $value }]) => [
  `--report-${name}`, 'colorSpace' in $value
    ? `rgb(${$value.components.map(channel => Math.round(channel * RGB_CHANNEL_MAX)).join(', ')})` : `${$value.value}${$value.unit}`
] as const))
const utcFormat = new Intl.DateTimeFormat('id-ID', {
  dateStyle: 'long', timeStyle: 'medium', timeZone: 'UTC', hourCycle: 'h23'
})
const report = shallowRef<PaymentSummary | null>(null)
const observation = computed(() => {
  if (!report.value) return ''
  const wholeSeconds = report.value.observed_at.replace(/\.\d+Z$/, 'Z')
  return utcFormat.format(new Date(wholeSeconds))
})
const issue = ref<keyof typeof issues | null>(null)
const busy = ref(false)
const refreshable = computed(() => !busy.value && (!!report.value || issue.value === 'failed' || issue.value === 'denied'))
const reportMain = ref<HTMLElement | null>(null)

type Identity = Readonly<{ token: string, subjectId: string }>
let owner: Identity | null = null
let pendingReloadFocus: HTMLButtonElement | null = null
let generation = 0
let mounted = false
let redirecting = false
let leavingFor: string | null = null
let removeNavigationListener: (() => void) | undefined

function invalidateReader(): void {
  generation++
  owner = null
  pendingReloadFocus = null
  report.value = null
  issue.value = null
  busy.value = false
}

function isCurrent(identity: Identity, requestGeneration: number): boolean {
  const current = router.currentRoute.value
  return mounted && !redirecting && !leavingFor && generation === requestGeneration && owner === identity &&
    resolveReaderPath(current.fullPath) === pagePath && current.params.id === workspaceId &&
    session?.token.value === identity.token && session.subjectId.value === identity.subjectId
}

async function readSummary(event?: MouseEvent): Promise<void> {
  const identity = owner
  if (!session || !workspaceId || !identity || busy.value || !isCurrent(identity, generation)) return
  const requestGeneration = ++generation
  busy.value = true
  report.value = null
  issue.value = null
  // Park existing button focus on main before Vue applies the native disabled state.
  const button = event?.currentTarget
  pendingReloadFocus = button instanceof HTMLButtonElement && button === document.activeElement && document.hasFocus()
    ? button : null
  if (pendingReloadFocus) {
    reportMain.value?.focus({ preventScroll: true })
    if (!isCurrent(identity, requestGeneration)) return
  }
  try {
    // apiFetch throws on 401 without redirecting; only this reader may act on its captured session.
    const { status, data } = await apiFetch<unknown>(session, `/api/workspaces/${workspaceId}/finance/summary`, { method: 'GET' })
    if (!isCurrent(identity, requestGeneration)) return
    report.value = status === 200 ? decodePaymentSummary(data, workspaceId) : null
    if (!report.value) issue.value = 'failed'
  } catch (error: unknown) {
    if (!isCurrent(identity, requestGeneration)) return
    if (error instanceof ApiError && error.status === 401) {
      redirecting = true
      redirectToLogin(session, pagePath)
      return
    }
    issue.value = error instanceof ApiError && error.status === 403 ? 'denied' : 'failed'
  } finally {
    if (isCurrent(identity, requestGeneration)) busy.value = false
  }
  if (!pendingReloadFocus) return
  await nextTick()
  if (isCurrent(identity, requestGeneration) && document.hasFocus() && document.activeElement === reportMain.value) {
    pendingReloadFocus?.focus({ preventScroll: true })
  }
}

async function synchronizeReader(): Promise<void> {
  invalidateReader()
  const scheduledGeneration = generation
  // Invalidate synchronously; start at most one read for the two fields changed by session.set.
  await nextTick()
  const { path, fullPath } = router.currentRoute.value
  if (!mounted || redirecting || leavingFor || path !== pagePath || scheduledGeneration !== generation) return
  const readerPath = resolveReaderPath(fullPath)
  const token = session?.token.value
  const subjectId = session?.subjectId.value
  if (!token || !subjectId) {
    redirecting = true
    await navigateTo(loginUrl(readerPath), { replace: true })
    return
  }
  if (!workspaceId || readerPath !== pagePath) { issue.value = 'invalid'; return }
  owner = { token, subjectId }
  await readSummary()
}

function suspendReader(to: RouteLocationNormalized): void {
  leavingFor = to.fullPath
  invalidateReader()
}
onBeforeRouteLeave(suspendReader)
onBeforeRouteUpdate((to, from) => {
  pendingReloadFocus = null
  if (workspaceId && resolveReaderPath(from.fullPath) === pagePath && resolveReaderPath(to.fullPath) === pagePath) return
  suspendReader(to)
})
onBeforeUnmount(() => { mounted = false; invalidateReader(); removeNavigationListener?.() })
onMounted(() => {
  mounted = true
  removeNavigationListener = router.afterEach((to, _from, failure) => {
    if (leavingFor !== to.fullPath) return
    leavingFor = null
    if (failure) void synchronizeReader()
  })
  watch([() => resolveReaderPath(router.currentRoute.value.fullPath), () => session?.token.value, () => session?.subjectId.value],
    synchronizeReader, { immediate: true, flush: 'sync' })
})
</script>

<template>
  <main id="konten" ref="reportMain" tabindex="-1" lang="id" class="page report" :style="pageStyle" @blur="pendingReloadFocus = null">
    <header class="intro">
      <div>
        <p class="kicker">Laporan workspace</p>
        <h1>Ringkasan pembayaran</h1>
        <p class="description">Harga dan pembayaran dari draf sewa di workspace ini, dipisahkan per mata uang.</p>
      </div>
      <div class="actions">
        <NuxtLink v-if="workspaceId" class="action back" :to="`/workspaces/${workspaceId.toLowerCase()}/rentals`" no-prefetch>Kembali ke Draf sewa</NuxtLink>
        <button type="button" class="action refresh" :disabled="!refreshable" @click="readSummary">Muat ulang</button>
      </div>
    </header>

    <div v-if="issue" class="message problem" role="alert">
      <h2>{{ issues[issue][0] }}</h2>
      <p>{{ issues[issue][1] }}</p>
    </div>
    <div v-else class="message" role="status" aria-atomic="true">
      <template v-if="report">
        <div class="stamp"><span>Waktu pembacaan (UTC)</span> <time :datetime="report.observed_at">{{ observation }}</time></div>
        <p v-if="report.currencies.length === 0" class="empty">Belum ada harga yang disepakati di workspace ini.</p>
      </template>
      <p v-else>{{ busy ? 'Memuat ringkasan...' : 'Memeriksa sesi masuk…' }}</p>
    </div>

    <template v-if="report">
      <section v-for="row in report.currencies" :key="row.currency" class="currency" :aria-labelledby="`currency-${row.currency}`">
        <h2 :id="`currency-${row.currency}`"><span>Mata uang</span> {{ row.currency }}</h2>
        <dl class="amounts">
          <div v-for="[field, label] in amounts" :key="field">
            <dt>{{ label }}</dt>
            <dd><strong>{{ row[field] }}</strong> <span>unit tarif ({{ row.currency }})</span></dd>
          </div>
        </dl>
        <div class="records">
          <div v-for="group in countGroups" :key="group.title">
            <h3>{{ group.title }}</h3>
            <dl class="counts">
              <div v-for="[field, label] in group.fields" :key="field">
                <dt>{{ label }}</dt><dd>{{ row[field] }}</dd>
              </div>
            </dl>
          </div>
        </div>
      </section>
      <aside v-if="report.currencies.length" class="guide" aria-labelledby="guide-title">
        <h2 id="guide-title">Cara membaca ringkasan</h2>
        <p>Harga yang disepakati menjadi plafon acuan tiap draf. Sisa plafon adalah bagian yang belum tertutup pembayaran aktif, bukan saldo kas.</p>
        <p>Pembayaran yang dibalik tidak lagi masuk jumlah aktif, tetapi catatan pembayarannya tetap dihitung. Pembalikan catatan bukan pengembalian uang.</p>
        <p>Usulan tanpa catatan tidak mengubah jumlah pembayaran dan bukan daftar yang perlu disetujui. Usulan pembalikan lain tetap terhitung meski pembayarannya sudah dibalik.</p>
      </aside>
    </template>
  </main>
</template>

<style scoped>
.report {
  max-width: var(--report-page-width); padding: var(--report-section) var(--report-inset) var(--report-control);
  background: var(--report-surface); color: var(--report-ink); overflow-wrap: anywhere;
}
.report :where(h1, h2, h3, p, dl, dd) { margin: 0; }
.intro, .actions, .stamp { display: flex; flex-wrap: wrap; align-items: center; }
.intro { justify-content: space-between; gap: var(--report-inset) var(--report-section); }
.intro > div:first-child { flex: 1 1 44ch; min-width: 0; max-width: 64ch; }
.kicker { color: var(--report-muted); font-weight: 600; font-size: var(--report-caption); }
.kicker::before {
  content: ''; display: inline-block; vertical-align: middle; width: var(--report-inset); height: var(--report-ring);
  margin-inline-end: var(--report-step); background: var(--report-accent);
}
.intro h1 { margin-block: var(--report-step); line-height: 1.2; text-wrap: balance; }
.description, .amounts dt, .amounts dd span, .counts dt { color: var(--report-muted); }
.actions { gap: var(--report-step) var(--report-inset); max-width: 100%; }
.action {
  display: inline-flex; align-items: center; justify-content: center; min-height: var(--report-control);
  min-width: var(--report-control); max-width: 100%; padding-block: var(--report-step);
  font: inherit; font-weight: 600; cursor: pointer;
  transition: color var(--report-feedback) ease-out, background-color var(--report-feedback) ease-out;
}
.action:focus-visible { outline: var(--report-ring) solid var(--report-ink); outline-offset: var(--report-ring); }
.back { color: var(--report-accent); text-underline-offset: 0.2em; }
.back:hover { color: var(--report-ink); text-decoration-thickness: var(--report-ring); }
.back:active { color: var(--report-ink); background: var(--report-tint); }
.refresh {
  padding-inline: var(--report-inset); color: var(--report-surface); background: var(--report-ink);
  border: var(--report-rule) solid var(--report-ink);
}
.refresh:enabled:hover { background: var(--report-muted); }
.refresh:enabled:active { background: var(--report-ink); box-shadow: inset 0 0 0 var(--report-ring) var(--report-muted); }
.refresh:disabled { color: var(--report-muted); background: var(--report-tint); border-color: var(--report-line); cursor: not-allowed; }

.message, .currency, .guide { margin-top: var(--report-section); }
.message, .guide { padding: var(--report-inset); background: var(--report-tint); }
.stamp { gap: var(--report-step) var(--report-inset); }
.stamp span, .currency h2 span { font-size: var(--report-caption); color: var(--report-muted); }
.stamp time, .amounts dd, .counts dd { font-variant-numeric: tabular-nums; }
.stamp time, .empty { font-weight: 600; }
.empty, .problem p, .guide p { margin-top: var(--report-step); }
.problem { border-inline-start: var(--report-ring) solid var(--report-danger); color: var(--report-danger); background: var(--report-danger-surface); }
.currency { border: var(--report-rule) solid var(--report-line); }
.currency > h2 {
  display: flex; gap: var(--report-step); align-items: baseline; padding: var(--report-step) var(--report-inset);
  background: var(--report-tint); border-top: var(--report-ring) solid var(--report-ink); font-size: var(--report-figure);
}
.amounts, .records { display: grid; grid-template-columns: minmax(0, 1fr); gap: var(--report-inset); padding: var(--report-inset); }
.amounts > div, .records > div { min-width: 0; }
.amounts dd { margin-top: var(--report-step); }
.amounts strong { display: block; font-size: var(--report-figure); line-height: 1.25; }
.amounts dd span, .counts dt { font-size: var(--report-caption); }
.records { border-top: var(--report-rule) solid var(--report-line); }
.counts > div {
  display: grid; grid-template-columns: minmax(0, 1fr) fit-content(40%); align-items: baseline;
  gap: var(--report-inset); padding-block: var(--report-step);
}
.counts > div + div { border-top: var(--report-rule) solid var(--report-line); }
.counts dd { font-weight: 600; text-align: end; }
.guide { border-inline-start: var(--report-ring) solid var(--report-accent); }
.guide p { max-width: 72ch; color: var(--report-muted); }

@media (min-width: 44rem) {
  .amounts { grid-template-columns: repeat(3, minmax(0, 1fr)); }
  .amounts > div + div { border-inline-start: var(--report-rule) solid var(--report-line); padding-inline-start: var(--report-inset); }
  .records { grid-template-columns: repeat(2, minmax(0, 1fr)); gap: var(--report-section); }
}
@media (prefers-reduced-motion: reduce) {
  .action { transition: none; }
}
</style>
