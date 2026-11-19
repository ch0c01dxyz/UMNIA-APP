<script setup lang="ts">
// Harga draf workspace (GET quote / POST accept-price / GET agreed-price
// untuk satu draft; GET koleksi 50 baris hanya untuk konteks draf).
// Halaman ini TIDAK membuat/mengubah draf, TIDAK memilih holds/aset,
// TIDAK memproses pembayaran. Fakta yang dicatat adalah catatan staf di
// dalam aplikasi — BUKAN consent/tanda tangan pelanggan, BUKAN perjanjian
// hukum, BUKAN invoice/pembayaran/posting ledger, BUKAN reservasi, dan
// BUKAN transisi status draf. Tidak ada alur koreksi harga di slice ini.
//
// Server otoritatif: quote dan fakta adalah bacaan core/history (hanya
// view_workspace); pencatatan butuh manage_business_data + instalasi
// rental.basic aktif + entitlement versi aktif. Kasir melihat formulir
// dan menerima 403 saat mencatat (bukan disembunyikan). Tanpa-fakta
// HANYA pesan 404 "harga yang disepakati tidak ditemukan"; bacaan fakta
// yang gagal/tertunda tidak pernah mengiklankan tulis.
//
// SATU percobaan logis, SATU kunci: kunci dibuat dengan
// crypto.randomUUID() saat panel konfirmasi dibuka, lalu dibekukan
// bersama snapshot (workspace, draf, aktor, pelanggan, barang, periode,
// versi/tarif/mata uang/unit/jumlah/total/aturan + label yang dibaca).
// Hanya 201 atau replay 200 dengan fakta yang valid yang mengonfirmasi
// pencatatan. GET fakta mengonfirmasi attempt HANYA bila kunci sama dan
// semua field snapshot cocok; fakta kunci-lain adalah riwayat independen
// (attempt mati, tanpa kirim-ulang). Hasil tak diketahui mengunci aksi
// biasa dan hanya menawarkan muat-ulang GET, kirim-ulang kunci-sama,
// atau "Mulai pencatatan baru" yang diperingatkan. Pemulihan hanya hidup
// di memori halaman ini.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Harga draf — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface DraftRow { id: string, workspace_id: string, customer_id: string, catalog_item_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, created_at: string, updated_at: string }
interface CustomerRow { id: string, workspace_id: string, display_name: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }
interface QuoteRow { draft_id: string, catalog_item_id: string, price_version_id: string, valid_from: string, valid_to: string, recorded_at: string, amount_minor: string, currency: string, unit: string, units: number, total_minor: string, starts_at: string, ends_at: string }
interface FactRow { id: string, workspace_id: string, rental_contract_id: string, price_version_id: string, pricing_rule_version_id: string, catalog_item_id: string, customer_id: string, client_request_id: string, amount_minor: string, currency: string, units: number, total_minor: string, starts_at: string, ends_at: string, accepted_at: string, accepted_by: string }

const PAGE_LIMIT = 50
const PICKER_PAGE = 100
const MAX_INT64 = 9223372036854775807n
const FACT_NO_FACT = 'harga yang disepakati tidak ditemukan'
const FACT_INVALID = 'Fakta harga yang diterima tidak valid.'
const KEY_MISMATCH = 'kunci idempotency sudah dipakai untuk permintaan yang berbeda'
const EXPECTED_MISMATCH = 'harga yang diharapkan tidak cocok dengan penawaran saat ini'
const EXISTING_FACT = 'draft ini sudah memiliki harga yang disepakati'
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const MONEY_RE = /^(0|[1-9][0-9]*)$/
const CURRENCY_RE = /^[A-Z]{3}$/
const KEY_RE = /^[A-Za-z0-9_.:-]{1,128}$/

// Waktu server: RFC3339 UTC persis seperti dikembalikan (termasuk detik
// dan mikrodetik bila ada), diubah ke mikrodetik epoch untuk perbandingan
// — tanpa membandingkan string (jebakan kesetaraan format) dan tanpa
// membulatkan batas submenit.
const SERVER_INSTANT_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d+))?(Z|[+-]\d{2}:\d{2})$/
function parseServerInstant(s: string): bigint | null {
  const m = SERVER_INSTANT_RE.exec(s)
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2])
  const d = Number(m[3])
  const h = Number(m[4])
  const mi = Number(m[5])
  const sec = Number(m[6])
  if (y < 1 || y > 9999 || mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || sec > 59) return null
  const dt = new Date(0)
  dt.setUTCFullYear(y, mo - 1, d)
  dt.setUTCHours(h, mi, sec, 0)
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d ||
    dt.getUTCHours() !== h || dt.getUTCMinutes() !== mi || dt.getUTCSeconds() !== sec) return null
  let micros = BigInt(dt.getTime()) * 1000n
  if (m[7]) micros += BigInt((m[7] + '000000').slice(0, 6))
  if (m[8] !== 'Z') {
    const sign = m[8][0] === '+' ? -1n : 1n
    const offH = BigInt(m[8].slice(1, 3))
    const offM = BigInt(m[8].slice(4, 6))
    micros += sign * (offH * 3600n + offM * 60n) * 1000000n
  }
  return micros
}

function isCanonicalMoney(s: unknown): s is string {
  if (typeof s !== 'string' || !MONEY_RE.test(s)) return false
  try {
    return BigInt(s) <= MAX_INT64
  } catch {
    return false
  }
}

function isValidQuoteRow(row: unknown, draftId: string): row is QuoteRow {
  if (!row || typeof row !== 'object') return false
  const q = row as QuoteRow
  if (typeof q.draft_id !== 'string' || q.draft_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof q.catalog_item_id !== 'string' || !UUID_RE.test(q.catalog_item_id)) return false
  if (typeof q.price_version_id !== 'string' || !UUID_RE.test(q.price_version_id)) return false
  if (!isCanonicalMoney(q.amount_minor) || !isCanonicalMoney(q.total_minor)) return false
  if (typeof q.currency !== 'string' || !CURRENCY_RE.test(q.currency)) return false
  if (q.unit !== '24h') return false
  if (typeof q.units !== 'number' || !Number.isInteger(q.units) || q.units < 1 || q.units > 3652059) return false
  const from = typeof q.starts_at === 'string' ? parseServerInstant(q.starts_at) : null
  const to = typeof q.ends_at === 'string' ? parseServerInstant(q.ends_at) : null
  const vf = typeof q.valid_from === 'string' ? parseServerInstant(q.valid_from) : null
  const vt = typeof q.valid_to === 'string' ? parseServerInstant(q.valid_to) : null
  if (from === null || to === null || vf === null || vt === null) return false
  if (!(to > from)) return false
  // Interval tarif half-open: berlaku mulai valid_from sampai sebelum valid_to.
  if (!(vf <= from && from < vt)) return false
  if (typeof q.recorded_at !== 'string' || parseServerInstant(q.recorded_at) === null) return false
  try {
    if (BigInt(q.total_minor) !== BigInt(q.amount_minor) * BigInt(q.units)) return false
  } catch {
    return false
  }
  return true
}

function isValidFactRow(row: unknown, ws: string, draftId: string): row is FactRow {
  if (!row || typeof row !== 'object') return false
  const f = row as FactRow
  if (typeof f.id !== 'string' || f.id === '') return false
  if (f.workspace_id !== ws) return false
  if (typeof f.rental_contract_id !== 'string' || f.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof f.price_version_id !== 'string' || !UUID_RE.test(f.price_version_id)) return false
  if (f.pricing_rule_version_id !== 'elapsed_24h_ceil_v1') return false
  if (typeof f.catalog_item_id !== 'string' || !UUID_RE.test(f.catalog_item_id)) return false
  if (typeof f.customer_id !== 'string' || !UUID_RE.test(f.customer_id)) return false
  if (typeof f.client_request_id !== 'string' || !KEY_RE.test(f.client_request_id)) return false
  if (!isCanonicalMoney(f.amount_minor) || !isCanonicalMoney(f.total_minor)) return false
  if (typeof f.currency !== 'string' || !CURRENCY_RE.test(f.currency)) return false
  if (typeof f.units !== 'number' || !Number.isInteger(f.units) || f.units < 1 || f.units > 3652059) return false
  const from = typeof f.starts_at === 'string' ? parseServerInstant(f.starts_at) : null
  const to = typeof f.ends_at === 'string' ? parseServerInstant(f.ends_at) : null
  if (from === null || to === null || !(to > from)) return false
  if (typeof f.accepted_at !== 'string' || parseServerInstant(f.accepted_at) === null) return false
  if (typeof f.accepted_by !== 'string' || f.accepted_by === '') return false
  try {
    if (BigInt(f.total_minor) !== BigInt(f.amount_minor) * BigInt(f.units)) return false
  } catch {
    return false
  }
  return true
}

interface Attempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  customerId: string
  customerLabel: string
  itemId: string
  itemLabel: string
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
  validFromIso: string
  validToIso: string
  priceVersionId: string
  amountMinor: string
  currency: string
  unit: string
  units: number
  totalMinor: string
  rule: string
}

function isValidFactForAttempt(row: unknown, ws: string, a: Attempt): row is FactRow {
  if (!isValidFactRow(row, ws, a.draftId)) return false
  const f = row as FactRow
  if (f.client_request_id !== a.key) return false
  if (f.price_version_id.toLowerCase() !== a.priceVersionId.toLowerCase()) return false
  if (f.catalog_item_id.toLowerCase() !== a.itemId.toLowerCase()) return false
  if (f.customer_id.toLowerCase() !== a.customerId.toLowerCase()) return false
  if (f.amount_minor !== a.amountMinor || f.currency !== a.currency) return false
  if (f.units !== a.units || f.total_minor !== a.totalMinor) return false
  const from = parseServerInstant(f.starts_at)
  const to = parseServerInstant(f.ends_at)
  return from === a.fromMicros && to === a.toMicros
}

function isValidDraftForQuote(row: unknown, ws: string, draftId: string, q: QuoteRow): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id.toLowerCase() !== draftId.toLowerCase()) return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || r.customer_id === '') return false
  if (typeof r.catalog_item_id !== 'string' || r.catalog_item_id.toLowerCase() !== q.catalog_item_id.toLowerCase()) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
  return from === parseServerInstant(q.starts_at) && to === parseServerInstant(q.ends_at)
}

function quoteMatchesAttempt(q: QuoteRow, a: Attempt): boolean {
  return q.price_version_id.toLowerCase() === a.priceVersionId.toLowerCase()
    && q.amount_minor === a.amountMinor && q.currency === a.currency && q.unit === a.unit
    && q.units === a.units && q.total_minor === a.totalMinor
    && parseServerInstant(q.starts_at) === a.fromMicros
    && parseServerInstant(q.ends_at) === a.toMicros
    && q.catalog_item_id.toLowerCase() === a.itemId.toLowerCase()
}

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))
const draftId = computed(() => String(route.params.draftId ?? ''))
const routeKey = computed(() => `${wsId.value}/${draftId.value}`)

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const readDenied = ref(false)
const workspace = ref<Workspace | null>(null)

const quote = ref<QuoteRow | null>(null)
const quoteLoading = ref(false)
const quoteError = ref<string | null>(null)
const quoteErrorActionable = computed(() => quoteError.value === 'harga efektif tidak ditemukan' || quoteError.value === 'total di luar batas yang dapat diwakili')

const fact = ref<FactRow | null>(null)
const factLoading = ref(false)
const factError = ref<string | null>(null)
const factMissing = ref(false)

const draftRows = ref<DraftRow[]>([])
const draftRow = ref<DraftRow | null>(null)
const draftValid = ref(false)
const draftLoading = ref(false)
const draftError = ref<string | null>(null)
const draftOffset = ref(0)
const draftExhausted = ref(false)
const draftStarted = ref(false)

const customerRows = ref<CustomerRow[]>([])
const customerOffset = ref(0)
const customerExhausted = ref(false)
const customerLoading = ref(false)
const customerLoadError = ref<string | null>(null)
const customerStarted = ref(false)
const customerById = computed(() => new Map(customerRows.value.map(r => [r.id, r])))

const catalogRows = ref<CatalogRow[]>([])
const catalogOffset = ref(0)
const catalogExhausted = ref(false)
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogStarted = ref(false)
const catalogById = computed(() => new Map(catalogRows.value.map(r => [r.id, r])))

const attempt = ref<Attempt | null>(null)
const unknownOutcome = ref(false)
const conflict = ref<'key' | 'expected' | null>(null)
const postDenied = ref<string | null>(null)
const replayError = ref<string | null>(null)
const settleError = ref<string | null>(null)
const factRecoveryError = ref<string | null>(null)
const quoteChangedNotice = ref<string | null>(null)
const quoteChangedNew = ref<QuoteRow | null>(null)
const busy = ref(false)
const refreshing = ref(false)
const saved = ref<FactRow | null>(null)
const savedHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)

const canConfirm = computed(() => !!quote.value && factMissing.value && !fact.value
  && !!draftRow.value && draftValid.value && !saved.value && !attempt.value
  && !unknownOutcome.value && conflict.value === null && !busy.value
  && !quoteLoading.value && !factLoading.value && !refreshing.value)
const confirmHint = computed(() => {
  if (saved.value || fact.value) return ''
  if (!quote.value && !quoteError.value) return 'Penawaran belum dimuat.'
  if (quoteError.value) return 'Penawaran tidak dapat dibaca; pencatatan belum tersedia.'
  if (!factMissing.value) return 'Status harga yang dicatat belum dapat dipastikan; pencatatan belum tersedia.'
  if (!draftRow.value) return 'Draf belum ditemukan dalam daftar; pencatatan belum tersedia.'
  if (!draftValid.value) return 'Draf yang ditemukan tidak cocok dengan penawaran; pencatatan belum tersedia.'
  return ''
})
// Konteks draf: baris draf bila lookup menemukannya; bila fakta sudah
// ada, fakta itu sendiri adalah konteksnya (lookup tidak berjalan karena
// tanpa-fakta tidak terverifikasi) — seksi Draf tidak boleh mengklaim
// pencarian yang tidak akan terjadi.
const contextCustomerId = computed(() => draftRow.value?.customer_id ?? fact.value?.customer_id ?? null)
const contextItemId = computed(() => draftRow.value?.catalog_item_id ?? fact.value?.catalog_item_id ?? null)
const contextFromIso = computed(() => draftRow.value?.starts_at ?? fact.value?.starts_at ?? null)
const contextToIso = computed(() => draftRow.value?.ends_at ?? fact.value?.ends_at ?? null)
const showCustomerMore = computed(() => !!contextCustomerId.value && !customerById.value.get(contextCustomerId.value) && !customerExhausted.value)
const showCatalogMore = computed(() => !!contextItemId.value && !catalogById.value.get(contextItemId.value) && !catalogExhausted.value)
const savedCustomerLabel = computed(() => (saved.value ? customerLabel(saved.value.customer_id) : ''))
const savedItemLabel = computed(() => (saved.value ? catalogLabel(saved.value.catalog_item_id) : ''))

function customerLabel(customerId: string): string {
  const row = customerById.value.get(customerId)
  if (row) return row.display_name
  return customerExhausted.value ? 'Nama pelanggan tidak tersedia' : 'Nama pelanggan belum dimuat'
}

function catalogLabel(catalogItemId: string): string {
  const row = catalogById.value.get(catalogItemId)
  if (row) return `${row.sku} — ${row.name}` + (row.status === 'active' ? '' : ' (arsip — riwayat)')
  return catalogExhausted.value ? 'Nama barang tidak tersedia' : 'Nama barang belum dimuat'
}

// Penjaga generasi: hasil muat yang tiba sesudah reset/pindah draf/
// workspace/logout diabaikan. inFlight dijaga sinkron (bukan hanya tombol
// disabled). postGen: kepemilikan POST — kelanjutan POST basi (sukses,
// galat, nextTick, refresh) tidak boleh menyentuh halaman/sesi/daftar
// pengganti. *LoadGen: kepemilikan flag busy — finally basi tidak boleh
// menghapus busy milik permintaan baru.
let listGen = 0
let postGen = 0
let postLoadGen = 0
let quoteGen = 0
let quoteLoadGen = 0
let factGen = 0
let factLoadGen = 0
let draftGen = 0
let draftLoadGen = 0
let customerGen = 0
let customerLoadGen = 0
let catalogGen = 0
let catalogLoadGen = 0
let refreshGen = 0
let inFlight = false
let quoteInFlight = false
let factInFlight = false
let draftInFlight = false
let customerInFlight = false
let catalogInFlight = false

function stale(gen: number) {
  return gen !== listGen
}

function stalePost(gen: number) {
  return gen !== postGen
}

function bumpAllGens() {
  listGen++
  postGen++
  postLoadGen++
  quoteGen++
  quoteLoadGen++
  factGen++
  factLoadGen++
  draftGen++
  draftLoadGen++
  customerGen++
  customerLoadGen++
  catalogGen++
  catalogLoadGen++
  refreshGen++
}

// Transisi baca ditolak (GET 403 sesudah halaman termuat): beri umpan
// balik izin workspace, sembunyikan konten yang sudah termuat, dan
// batalkan kepemilikan baca/POST yang tertunda supaya penyelesaian
// terlambat tidak mengisi ulang halaman pengganti. Sesi dipertahankan
// — 403 bukan 401. Berbeda dengan POST 403 yang mempertahankan data
// yang terbaca + snapshot percobaan.
function denyRead(message: string) {
  bumpAllGens()
  quoteInFlight = false
  factInFlight = false
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  inFlight = false
  busy.value = false
  refreshing.value = false
  quote.value = null
  quoteLoading.value = false
  quoteError.value = null
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftLoading.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  customerLoading.value = false
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  attempt.value = null
  unknownOutcome.value = false
  conflict.value = null
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  factRecoveryError.value = null
  quoteChangedNotice.value = null
  quoteChangedNew.value = null
  saved.value = null
  // Penolakan saat ini menyelesaikan loader global: penolakan awal
  // memanggil denyRead yang menaikkan listGen, sehingga finalizer load()
  // yang lama tidak bisa lagi mematikan loading — tanpa baris ini
  // "Memuat…" bertahan selamanya di atas penolakan.
  loading.value = false
  readDenied.value = true
  pageError.value = message
}

function resetState() {
  bumpAllGens()
  quoteInFlight = false
  factInFlight = false
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  inFlight = false
  busy.value = false
  refreshing.value = false
  loading.value = false
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  workspace.value = null
  quote.value = null
  quoteLoading.value = false
  quoteError.value = null
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftLoading.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  customerLoading.value = false
  customerStarted.value = false
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  catalogStarted.value = false
  attempt.value = null
  unknownOutcome.value = false
  conflict.value = null
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  factRecoveryError.value = null
  quoteChangedNotice.value = null
  quoteChangedNew.value = null
  saved.value = null
}

onMounted(async () => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  await load()
})

watch(routeKey, async () => {
  resetState()
  await load()
})

onUnmounted(() => {
  bumpAllGens()
})

async function load() {
  const s = session
  if (!s) return
  const gen = ++listGen
  loading.value = true
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  if (!UUID_RE.test(wsId.value) || !UUID_RE.test(draftId.value)) {
    pageError.value = 'id workspace atau id draf tidak valid'
    if (!stale(gen)) loading.value = false
    return
  }
  try {
    const ws = await apiFetch<Workspace>(s, `/api/workspaces/${wsId.value}`)
    if (stale(gen)) return
    workspace.value = ws.data
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
    } else if (err instanceof ApiError && err.status === 403) {
      noPermission.value = true
      pageError.value = err.message
    } else if (err instanceof ApiError) pageError.value = err.message
    else pageError.value = 'Terjadi kesalahan. Coba lagi.'
    if (!stale(gen)) loading.value = false
    return
  }
  if (!stale(gen)) await fetchQuote()
  if (readDenied.value) return
  if (!stale(gen)) await fetchFact()
  if (readDenied.value) return
  if (!stale(gen) && !customerStarted.value) {
    customerStarted.value = true
    await loadCustomerPage()
  }
  if (readDenied.value) return
  if (!stale(gen) && !catalogStarted.value) {
    catalogStarted.value = true
    await loadItemPage()
  }
  if (readDenied.value) return
  if (!stale(gen)) await maybeStartDraftLookup()
  if (!stale(gen)) loading.value = false
}

// Satu bacaan quote. Quote yang diterima tidak valid diperlakukan sebagai
// galat baca, bukan sebagai harga nol.
async function fetchQuote() {
  const s = session
  if (!s || quoteInFlight) return
  const gen = ++quoteGen
  const myLoad = ++quoteLoadGen
  quoteInFlight = true
  quoteLoading.value = true
  try {
    const res = await apiFetch<QuoteRow>(s, `/api/workspaces/${wsId.value}/rental/contracts/${draftId.value}/quote`)
    if (gen !== quoteGen) return
    if (isValidQuoteRow(res.data, draftId.value)) {
      quote.value = res.data
      quoteError.value = null
    } else {
      quote.value = null
      quoteError.value = 'Penawaran yang diterima tidak valid.'
    }
  } catch (err) {
    if (gen !== quoteGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    quote.value = null
    if (err instanceof ApiError) quoteError.value = err.message
    else quoteError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === quoteLoadGen) {
      quoteInFlight = false
      quoteLoading.value = false
    }
  }
}

// Satu bacaan fakta. Tanpa-fakta HANYA pesan 404 spesifik; 404 lain
// (draf hilang) dan galat lain adalah kegagalan baca, bukan "belum ada".
async function fetchFact() {
  const s = session
  if (!s || factInFlight) return
  const gen = ++factGen
  const myLoad = ++factLoadGen
  factInFlight = true
  factLoading.value = true
  try {
    const res = await apiFetch<FactRow>(s, `/api/workspaces/${wsId.value}/rental/contracts/${draftId.value}/agreed-price`)
    if (gen !== factGen) return
    if (isValidFactRow(res.data, wsId.value, draftId.value)) {
      fact.value = res.data
      factMissing.value = false
      factError.value = null
    } else {
      fact.value = null
      factMissing.value = false
      factError.value = FACT_INVALID
    }
  } catch (err) {
    if (gen !== factGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    fact.value = null
    if (err instanceof ApiError && err.status === 404 && err.message === FACT_NO_FACT) {
      factMissing.value = true
      factError.value = null
    } else if (err instanceof ApiError) {
      factMissing.value = false
      factError.value = err.message
    } else {
      factMissing.value = false
      factError.value = 'Terjadi kesalahan. Coba lagi.'
    }
  } finally {
    if (myLoad === factLoadGen) {
      factInFlight = false
      factLoading.value = false
    }
  }
}

// Satu halaman pelanggan (100 baris). Tanpa rantai otomatis: pemanggil
// berikutnya hanyalah tombol "Muat lebih banyak pelanggan" atau "Coba
// lagi" (yang memakai offset yang sama). Kegagalan mempertahankan baris,
// peta, dan kursor yang sudah ada.
async function loadCustomerPage() {
  const s = session
  if (!s || customerInFlight) return
  const gen = customerGen
  const myLoad = ++customerLoadGen
  const wantOffset = customerOffset.value
  customerInFlight = true
  customerLoading.value = true
  customerLoadError.value = null
  try {
    const res = await apiFetch<{ items: CustomerRow[] }>(s, `/api/workspaces/${wsId.value}/customers?limit=${PICKER_PAGE}&offset=${wantOffset}`)
    if (gen !== customerGen) return
    const seen = new Set(customerRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        customerRows.value.push(row)
        seen.add(row.id)
      }
    }
    customerOffset.value = wantOffset + res.data.items.length
    customerExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== customerGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) customerLoadError.value = err.message
    else customerLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === customerLoadGen) {
      customerInFlight = false
      customerLoading.value = false
    }
  }
}

// Satu halaman katalog (100 baris). Tanpa rantai otomatis; offset maju
// menurut jumlah mentah yang diterima, bukan jumlah render.
async function loadItemPage() {
  const s = session
  if (!s || catalogInFlight) return
  const gen = catalogGen
  const myLoad = ++catalogLoadGen
  const wantOffset = catalogOffset.value
  catalogInFlight = true
  catalogLoading.value = true
  catalogError.value = null
  try {
    const res = await apiFetch<{ items: CatalogRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items?limit=${PICKER_PAGE}&offset=${wantOffset}`)
    if (gen !== catalogGen) return
    const seen = new Set(catalogRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        catalogRows.value.push(row)
        seen.add(row.id)
      }
    }
    catalogOffset.value = wantOffset + res.data.items.length
    catalogExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== catalogGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) catalogError.value = err.message
    else catalogError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === catalogLoadGen) {
      catalogInFlight = false
      catalogLoading.value = false
    }
  }
}

// Satu halaman koleksi draf (50 baris) untuk konteks draf yang dipilih.
// Hanya berjalan sesudah quote 200 + tanpa-fakta terverifikasi; tanpa
// rantai otomatis dan tanpa sapuan sampai habis. Prefix yang belum lengkap
// bukan penghapusan/404. Baris yang ditemukan harus cocok dengan quote
// (barang + periode) sebelum konfirmasi dibuka.
async function loadDraftPage() {
  const s = session
  if (!s || draftInFlight) return
  if (!quote.value || !factMissing.value || fact.value) return
  const gen = draftGen
  const myLoad = ++draftLoadGen
  const wantOffset = draftOffset.value
  draftInFlight = true
  draftLoading.value = true
  draftError.value = null
  try {
    const res = await apiFetch<{ items: DraftRow[] }>(s, `/api/workspaces/${wsId.value}/rental/contracts?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
    if (gen !== draftGen) return
    const seen = new Set(draftRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        draftRows.value.push(row)
        seen.add(row.id)
      }
    }
    draftOffset.value = wantOffset + res.data.items.length
    draftExhausted.value = res.data.items.length < PAGE_LIMIT
    const found = draftRows.value.find(r => r.id.toLowerCase() === draftId.value.toLowerCase())
    if (found) {
      draftRow.value = found
      draftValid.value = isValidDraftForQuote(found, wsId.value, draftId.value, quote.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan penawaran yang ditampilkan.'
    } else if (draftExhausted.value) {
      draftError.value = `Draf tidak ditemukan setelah memuat seluruh daftar (${draftRows.value.length} baris).`
    }
  } catch (err) {
    if (gen !== draftGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) draftError.value = err.message
    else draftError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === draftLoadGen) {
      draftInFlight = false
      draftLoading.value = false
    }
  }
}

async function maybeStartDraftLookup() {
  if (!quote.value || !factMissing.value || fact.value) return
  if (draftStarted.value) return
  draftStarted.value = true
  await loadDraftPage()
}

function resetDraftLookup() {
  draftGen++
  draftLoadGen++
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftLoading.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
}

// Muat-ulang GET saja (quote + fakta): tidak mengganti snapshot, tidak
// mengonfirmasi apa pun, tidak menulis. Dipakai tombol "Muat ulang
// harga" dan sesudah retirement. Fakta yang cocok dengan attempt
// mengonfirmasi lewat bacaan independen; fakta kunci-lain mengakhiri
// attempt; quote baru ditampilkan terpisah dengan pemberitahuan.
async function refreshReads(failureMessage: string) {
  const s = session
  if (!s || refreshing.value) return
  const gen = ++refreshGen
  refreshing.value = true
  settleError.value = null
  try {
    const displayedQuote = quote.value
    const hadAttempt = attempt.value
    await fetchQuote()
    if (readDenied.value) return
    await fetchFact()
    if (readDenied.value) return
    if (gen !== refreshGen) return
    if (hadAttempt && quote.value && !quoteMatchesAttempt(quote.value, hadAttempt)) {
      quoteChangedNotice.value = 'Penawaran berubah sejak konfirmasi dibuka.'
      quoteChangedNew.value = quote.value
      quote.value = displayedQuote
    } else {
      quoteChangedNotice.value = null
      quoteChangedNew.value = null
    }
    if (attempt.value && fact.value) {
      if (isValidFactForAttempt(fact.value, wsId.value, attempt.value)) {
        await confirmSave(fact.value, postGen)
        // GET menyelesaikan attempt: pagar kelanjutan POST lama agar
        // hasil/salah lamanya tidak membangkitkan panel pemulihan.
        postGen++
        return
      }
      if (fact.value.client_request_id !== attempt.value.key) {
        // Kunci benar-benar berbeda: fakta pihak lain — riwayat yang
        // dicatat aksi lain, bukan sukses aksi ini. Attempt mati; tanpa
        // kirim-ulang dan tanpa klaim sukses atas aksi ini.
        attempt.value = null
        unknownOutcome.value = false
        conflict.value = null
        postDenied.value = null
        replayError.value = null
        settleError.value = null
        // GET menyelesaikan attempt: pagar kelanjutan POST lama.
        postGen++
        return
      }
      // Kunci sama tetapi field tidak cocok: tidak mengonfirmasi apa pun.
      // Snapshot/kunci/ketidakpastian dipertahankan; kandidat TIDAK
      // dipromosikan sebagai fakta tervalidasi.
      fact.value = null
      factMissing.value = false
      factError.value = 'Fakta yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini.'
      settleError.value = 'Fakta yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; pencatatan tetap tidak diketahui.'
      return
    }
    if (attempt.value && factError.value) {
      settleError.value = factError.value === FACT_INVALID
        ? 'Fakta yang dibaca tidak cocok dengan permintaan ini; pencatatan tetap tidak diketahui.'
        : 'Fakta tidak dapat dibaca saat ini; pencatatan tetap tidak diketahui.'
    }
    if (!attempt.value && quote.value && draftRow.value) {
      draftValid.value = isValidDraftForQuote(draftRow.value, wsId.value, draftId.value, quote.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan penawaran yang ditampilkan.'
    }
    if (!attempt.value) await maybeStartDraftLookup()
  } catch {
    if (gen === refreshGen && !readDenied.value) pageError.value = failureMessage
  } finally {
    if (gen === refreshGen) refreshing.value = false
  }
}

function setUnknownOutcome(gen: number) {
  unknownOutcome.value = true
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Kunci tercemar (409 mismatch): kunci sudah terikat pada permintaan lain.
// Satu status yang koheren: panel tak diketahui disembunyikan
// (kirim-ulang tidak lagi ditawarkan), snapshot dipertahankan untuk
// ringkasan beku, kirim terkunci, dan hanya "Mulai pencatatan baru" yang
// boleh menebus — tanpa kirim-ulang buta.
function setKeyMismatch(gen: number) {
  conflict.value = 'key'
  unknownOutcome.value = false
  postDenied.value = null
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Penawaran yang diharapkan tidak cocok (409): snapshot basi atau konteks
// salah, bukan transient untuk kirim-ulang. Sama seperti kunci tercemar:
// tanpa kirim-ulang, hanya retirement diperingatkan + baca ulang.
function setExpectedMismatch(gen: number) {
  conflict.value = 'expected'
  unknownOutcome.value = false
  postDenied.value = null
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Fakta sudah ada (409): tulis ini pasti tidak terjadi; attempt tidak
// lagi relevan. Pulihkan fakta lewat GET — tampilkan bila terbaca, beri
// galat pemulihan bila tidak.
async function recoverFact() {
  attempt.value = null
  unknownOutcome.value = false
  conflict.value = null
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  await fetchFact()
  if (readDenied.value) return
  if (!fact.value) {
    factRecoveryError.value = 'Harga sudah dicatat untuk draf ini, tetapi fakta tidak dapat dibaca. Coba muat ulang harga.'
  }
}

async function confirmSave(row: FactRow, gen: number) {
  saved.value = row
  fact.value = row
  factMissing.value = false
  attempt.value = null
  unknownOutcome.value = false
  conflict.value = null
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  factRecoveryError.value = null
  quoteChangedNotice.value = null
  quoteChangedNew.value = null
  await nextTick()
  if (stalePost(gen)) return
  savedHeading.value?.focus()
}

// Buka panel konfirmasi: buat kunci SEKALI dan bekukan snapshot dari
// quote + baris draf + label yang dibaca saat itu. Tanpa POST saat dibuka.
function openConfirmation() {
  if (!canConfirm.value || attempt.value) return
  const q = quote.value
  const d = draftRow.value
  const actor = session?.subjectId.value ?? ''
  if (!q || !d || !actor) return
  const fromMicros = parseServerInstant(q.starts_at)
  const toMicros = parseServerInstant(q.ends_at)
  if (fromMicros === null || toMicros === null) return
  attempt.value = {
    key: crypto.randomUUID(),
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: actor,
    customerId: d.customer_id,
    customerLabel: customerLabel(d.customer_id),
    itemId: d.catalog_item_id,
    itemLabel: catalogLabel(d.catalog_item_id),
    fromMicros,
    toMicros,
    fromIso: q.starts_at,
    toIso: q.ends_at,
    validFromIso: q.valid_from,
    validToIso: q.valid_to,
    priceVersionId: q.price_version_id,
    amountMinor: q.amount_minor,
    currency: q.currency,
    unit: q.unit,
    units: q.units,
    totalMinor: q.total_minor,
    rule: 'elapsed_24h_ceil_v1'
  }
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  factRecoveryError.value = null
  nextTick().then(() => {
    formAlert.value?.focus()
  })
}

// Batalkan konfirmasi yang BELUM dikirim: buang snapshot yang belum
// dipakai; quote/konteks tidak berubah, jadi tidak perlu muat ulang.
function cancelConfirmation() {
  if (busy.value || unknownOutcome.value || conflict.value !== null) return
  attempt.value = null
  postDenied.value = null
}

// Menebus snapshot secara eksplisit: pemulihan ditinggalkan, lalu baca
// ulang keadaan saat ini sebelum kunci baru dibuat. Pencatatan
// sebelumnya mungkin sudah tersimpan dan TIDAK dibatalkan oleh tindakan
// ini. Terkunci selama POST berjalan: kelanjutan POST lama tidak boleh
// menghapus pencatatan baru atau mengumumkan simpan untuknya.
async function retireAttempt() {
  if (busy.value) return
  attempt.value = null
  unknownOutcome.value = false
  conflict.value = null
  postDenied.value = null
  replayError.value = null
  settleError.value = null
  quoteChangedNotice.value = null
  quoteChangedNew.value = null
  resetDraftLookup()
  await refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')
}

async function submitAccept(resend = false) {
  const s = session
  if (!s || inFlight) return
  if ((unknownOutcome.value || conflict.value !== null) && !resend) return
  if (resend && conflict.value !== null) return
  const a = attempt.value
  if (!a) return
  if (!resend) {
    // Kiriman pertama: bacaan wajib selesai dengan keadaan saat ini
    // (quote/fakta/konteks valid + tanpa-fakta terverifikasi spesifik).
    // Bacaan tertunda/gagal bukan tanpa-fakta. Kirim-ulang adalah
    // pemulihan permintaan yang sudah dikirim, bukan kiriman baru, jadi
    // tidak diblokir oleh bacaan sesudahnya yang gagal/berubah.
    if (quoteLoading.value || factLoading.value || refreshing.value) return
    if (!quote.value || !factMissing.value || fact.value) return
    if (!draftRow.value || !draftValid.value) return
  }
  const gen = postGen
  const myPost = ++postLoadGen
  replayError.value = null
  settleError.value = null
  if (!resend) postDenied.value = null
  inFlight = true
  busy.value = true
  try {
    const { status, data } = await apiFetch<FactRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/accept-price`, {
      method: 'POST',
      body: { expected_price_version_id: a.priceVersionId, expected_total_minor: a.totalMinor },
      idempotencyKey: a.key
    })
    if (stalePost(gen) || saved.value) return
    // Replay 200 atau 201 dengan fakta yang valid mengonfirmasi tulis
    // yang sudah terkomit; 2xx lain atau fakta cacat tetap tak diketahui.
    if ((status === 200 || status === 201) && isValidFactForAttempt(data, a.workspaceId, a)) {
      await confirmSave(data, gen)
      return
    }
    setUnknownOutcome(gen)
  } catch (err) {
    if (stalePost(gen) || saved.value) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === EXPECTED_MISMATCH) {
      setExpectedMismatch(gen)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === EXISTING_FACT) {
      await recoverFact()
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 422)) {
      if (!resend) {
        // Kiriman pertama: penolakan pra-sisip — tulis tidak terjadi.
        // Snapshot dipertahankan + panel tetap terbuka; kirim-ulang
        // kunci-sama tetap tersedia sesudah penyebabnya diperbaiki.
        postDenied.value = err.message
        await nextTick()
        if (!stalePost(gen)) formAlert.value?.focus()
        return
      }
      // Penolakan kirim-ulang yang tegas: TIDAK membuktikan tulis awal
      // gagal — pemeriksaan izin server mendahului replay — jadi
      // snapshot dan ketidakpastian dipertahankan.
      replayError.value = err.message
      unknownOutcome.value = true
      await nextTick()
      if (!stalePost(gen)) formAlert.value?.focus()
      return
    }
    // 500/502/jaringan: hasil tetap tak diketahui, snapshot dipertahankan.
    setUnknownOutcome(gen)
  } finally {
    if (myPost === postLoadGen) {
      inFlight = false
      busy.value = false
    }
  }
}

async function resendAccept() {
  await submitAccept(true)
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Harga draf</h1>
    <p v-if="checking">Memeriksa sesi masuk…</p>
    <template v-else>
      <p v-if="loading" role="status">Memuat…</p>
      <div v-else-if="noPermission" class="alert" role="alert">
        <h2>Tidak ada izin</h2>
        <p>{{ pageError }}</p>
        <p>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Lihat fitur workspace</NuxtLink>
        </p>
      </div>
      <div v-else-if="readDenied" class="alert" role="alert">
        <h2>Tidak ada izin</h2>
        <p>{{ pageError }}</p>
        <p>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Lihat fitur workspace</NuxtLink>
        </p>
      </div>
      <div v-else-if="pageError" class="alert" role="alert">
        <h2>Ada masalah</h2>
        <p>{{ pageError }}</p>
        <p><button type="button" class="btn btn-secondary" @click="load">Coba lagi</button></p>
      </div>
      <template v-else-if="workspace">
        <p class="tagline">Workspace: <strong>{{ workspace.name }}</strong></p>
        <nav class="site-nav page-nav" aria-label="Navigasi workspace">
          <NuxtLink :to="`/workspaces/${wsId}/members`">Anggota</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Fitur</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/customers`">Pelanggan</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/catalog`">Barang</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/assets`">Unit</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/prices`">Tarif</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/rentals`">Draf sewa</NuxtLink>
        </nav>
        <p>
          <NuxtLink class="btn btn-secondary" :to="`/workspaces/${wsId}/rentals`">Kembali ke Draf sewa</NuxtLink>
        </p>
        <p class="small">
          Harga yang dicatat di sini hanya mencatat harga yang dilihat untuk draf ini. Ini bukan persetujuan pelanggan,
          bukan perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
        </p>

        <section class="card" aria-labelledby="draft-heading">
          <h2 id="draft-heading">Draf</h2>
          <div v-if="draftRow || fact">
            <p>
              <strong>{{ customerLabel(contextCustomerId ?? '') }} — {{ catalogLabel(contextItemId ?? '') }}</strong><br>
              Periode yang dicatat: mulai {{ contextFromIso }}; berakhir {{ contextToIso }} (UTC)<br>
              Status: Draf
            </p>
          </div>
          <p v-else-if="draftLoading" role="status">Mencari draf dalam daftar…</p>
          <div v-else-if="draftError" class="alert" role="alert">
            <p>{{ draftError }}</p>
            <p v-if="!draftExhausted">
              <button type="button" class="btn btn-secondary" :disabled="draftLoading" @click="loadDraftPage">Coba lagi</button>
            </p>
          </div>
          <p v-else class="hint">Draf dicari dalam daftar sesudah penawaran dibaca.</p>
          <p v-if="draftStarted && !draftRow && !draftError">Mencari draf dalam daftar: {{ draftRows.length }} baris dimuat sejauh ini.</p>
          <p v-if="draftStarted && !draftRow && !draftExhausted && !draftLoading && !draftError">
            <button type="button" class="btn btn-secondary" @click="loadDraftPage">Muat lebih banyak draf</button>
          </p>
          <p v-if="customerLoadError" class="alert" role="alert">
            {{ customerLoadError }}
            <button type="button" class="btn btn-secondary" @click="loadCustomerPage">Coba lagi</button>
          </p>
          <p v-if="showCustomerMore">
            <button type="button" class="btn btn-secondary" :disabled="customerLoading" @click="loadCustomerPage">
              {{ customerLoading ? 'Memuat…' : 'Muat lebih banyak pelanggan' }}
            </button>
          </p>
          <p v-if="catalogError" class="alert" role="alert">
            {{ catalogError }}
            <button type="button" class="btn btn-secondary" @click="loadItemPage">Coba lagi</button>
          </p>
          <p v-if="showCatalogMore">
            <button type="button" class="btn btn-secondary" :disabled="catalogLoading" @click="loadItemPage">
              {{ catalogLoading ? 'Memuat…' : 'Muat lebih banyak barang' }}
            </button>
          </p>
        </section>

        <section class="card" aria-labelledby="quote-heading">
          <h2 id="quote-heading">Penawaran harga</h2>
          <p v-if="quoteLoading" role="status">Memuat penawaran…</p>
          <div v-else-if="quoteError" class="alert" role="alert">
            <p>{{ quoteError }}</p>
            <p v-if="quoteErrorActionable">
              <NuxtLink :to="`/workspaces/${wsId}/prices`">Lihat tarif</NuxtLink>
            </p>
          </div>
          <template v-else-if="quote">
            <p>
              Tarif: {{ quote.amount_minor }} unit tarif · {{ quote.currency }} per 24 jam<br>
              {{ quote.units }} unit 24 jam<br>
              Total: {{ quote.total_minor }} unit tarif · {{ quote.currency }}
            </p>
            <p>Periode yang dicatat: mulai {{ quote.starts_at }}; berakhir {{ quote.ends_at }} (UTC)</p>
            <p>Berlaku tarif: {{ quote.valid_from }} sampai sebelum {{ quote.valid_to }} (UTC)</p>
            <p class="hint">
              Satu unit = 24 jam penuh; sisa waktu dihitung satu unit penuh; minimal 1 unit. Periode dihitung dari
              selisih waktu UTC draft, bukan hari kalender lokal. Nilai tampil apa adanya sebagai unit tersimpan,
              tanpa konversi mata uang.
            </p>
          </template>
          <p>
            <button type="button" class="btn btn-secondary" :disabled="refreshing || loading" @click="refreshReads('Penawaran gagal dimuat ulang. Coba muat ulang harga.')">Muat ulang harga</button>
          </p>
        </section>

        <section class="card" aria-labelledby="fact-heading">
          <h2 id="fact-heading">Harga yang dicatat</h2>
          <p v-if="factLoading" role="status">Memuat harga yang dicatat…</p>
          <div v-else-if="fact">
            <p>
              Harga yang dicatat: {{ fact.total_minor }} unit tarif · {{ fact.currency }} ({{ fact.units }} × 24 jam @ {{ fact.amount_minor }} unit tarif · {{ fact.currency }}), dicatat {{ fact.accepted_at }} (UTC).
            </p>
            <p class="hint">
              Fakta ini hanya mencatat harga yang dilihat untuk draf ini. Ini bukan persetujuan pelanggan, bukan
              perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
            </p>
          </div>
          <p v-else-if="factMissing">Belum ada harga yang dicatat untuk draf ini.</p>
          <div v-else-if="factError" class="alert" role="alert">
            <p>{{ factError }}</p>
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')">Coba lagi</button></p>
          </div>
          <div v-if="factRecoveryError" class="alert" role="alert">
            <p>{{ factRecoveryError }}</p>
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')">Muat ulang harga</button></p>
          </div>
        </section>

        <section class="card" aria-labelledby="record-heading">
          <h2 id="record-heading">Catat harga</h2>
          <p v-if="confirmHint" class="hint">{{ confirmHint }}</p>
          <p v-if="canConfirm && !attempt">
            <button type="button" class="btn" :disabled="busy" @click="openConfirmation">Catat harga ini</button>
          </p>
          <div v-if="quoteChangedNotice" class="alert" role="alert">
            <p>{{ quoteChangedNotice }} Ringkasan di bawah tetap memakai penawaran yang dikonfirmasi.</p>
            <p v-if="quoteChangedNew">Penawaran terbaru: {{ quoteChangedNew.total_minor }} unit tarif · {{ quoteChangedNew.currency }} ({{ quoteChangedNew.units }} × 24 jam @ {{ quoteChangedNew.amount_minor }} unit tarif · {{ quoteChangedNew.currency }}).</p>
          </div>
          <div v-if="attempt && !unknownOutcome && conflict === null" class="alert" role="alert">
            <h3 ref="formAlert" tabindex="-1">Konfirmasi pencatatan harga</h3>
            <p>
              {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC).
            </p>
            <p>
              Tarif: {{ attempt.amountMinor }} unit tarif · {{ attempt.currency }} per 24 jam;
              {{ attempt.units }} unit 24 jam.
            </p>
            <p>Total: {{ attempt.totalMinor }} unit tarif · {{ attempt.currency }}</p>
            <p>Berlaku tarif: {{ attempt.validFromIso }} sampai sebelum {{ attempt.validToIso }} (UTC)</p>
            <p>
              Fakta ini hanya mencatat harga yang dilihat untuk draf ini. Ini bukan persetujuan pelanggan, bukan
              perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
            </p>
            <p v-if="postDenied" class="field-error">{{ postDenied }}</p>
            <p>
              <button type="button" class="btn" :disabled="busy || refreshing" @click="submitAccept(false)">
                {{ busy ? 'Mencatat…' : 'Catat harga ini' }}
              </button>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="cancelConfirmation">Batalkan</button>
            </p>
          </div>
          <div v-if="unknownOutcome" class="alert" role="alert">
            <h3 ref="formAlert" tabindex="-1">Hasil pencatatan tidak diketahui</h3>
            <p>
              Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
              sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
              hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
              akan menghilangkannya.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC). Total: {{ attempt.totalMinor }} unit tarif · {{ attempt.currency }}.
            </p>
            <p v-if="replayError" class="field-error">Kirim ulang ditolak: {{ replayError }}</p>
            <p v-if="settleError" class="field-error">{{ settleError }}</p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="refreshing || busy" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')">Muat ulang harga</button>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="resendAccept">Kirim lagi (permintaan sama)</button>
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai pencatatan baru</button>
              Mulai pencatatan baru membatalkan pemulihan ini. Pencatatan sebelumnya mungkin sudah
              tersimpan dan tidak dibatalkan oleh tindakan ini.
            </p>
          </div>
          <div v-if="conflict === 'key'" class="alert" role="alert">
            <h3 ref="formAlert" tabindex="-1">Kunci idempotency sudah dipakai</h3>
            <p>
              {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
              tidak dapat dikirim ulang dengan kunci yang sama.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC). Total: {{ attempt.totalMinor }} unit tarif · {{ attempt.currency }}.
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="refreshing || busy" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')">Muat ulang harga</button>
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai pencatatan baru</button>
              Mulai pencatatan baru membatalkan pemulihan ini. Pencatatan sebelumnya mungkin sudah
              tersimpan dan tidak dibatalkan oleh tindakan ini.
            </p>
          </div>
          <div v-if="conflict === 'expected'" class="alert" role="alert">
            <h3 ref="formAlert" tabindex="-1">Harga yang diharapkan tidak cocok</h3>
            <p>
              {{ EXPECTED_MISMATCH }}. Penawaran yang dikonfirmasi sudah tidak sama dengan penawaran server
              saat ini; kirim-ulang dengan kunci yang sama tidak akan mengubah hasil.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC). Total: {{ attempt.totalMinor }} unit tarif · {{ attempt.currency }}.
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="refreshing || busy" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang harga.')">Muat ulang harga</button>
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai pencatatan baru</button>
              Mulai pencatatan baru membatalkan pemulihan ini. Pencatatan sebelumnya mungkin sudah
              tersimpan dan tidak dibatalkan oleh tindakan ini.
            </p>
          </div>
        </section>

        <div v-if="saved" class="card alert-ok" aria-labelledby="saved-heading">
          <h3 id="saved-heading" ref="savedHeading" tabindex="-1">Harga tercatat</h3>
          <p>
            Harga untuk <strong>{{ savedCustomerLabel }} — {{ savedItemLabel }}</strong> sudah tercatat:
            {{ saved.total_minor }} unit tarif · {{ saved.currency }} ({{ saved.units }} × 24 jam @ {{ saved.amount_minor }} unit tarif · {{ saved.currency }}),
            dicatat {{ saved.accepted_at }} (UTC).
          </p>
        </div>
      </template>
    </template>
  </main>
</template>
