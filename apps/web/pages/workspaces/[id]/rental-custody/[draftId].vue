<script setup lang="ts">
// Serah terima unit workspace (GET custody / GET holds / POST holds /
// POST handover / POST return untuk satu draft; GET koleksi 50 baris
// hanya untuk konteks draf, GET holds 50 baris untuk hold yang hidup).
// Halaman ini TIDAK membuat/mengubah draf, TIDAK mencatat harga, TIDAK
// memproses pembayaran. Serah terima/pengembalian adalah catatan staf di
// dalam aplikasi — BUKAN consent/tanda tangan pelanggan, BUKAN perjanjian
// hukum, BUKAN invoice/pembayaran/posting ledger, BUKAN reservasi, dan
// BUKAN transisi status draf. Tidak ada alur koreksi harga di slice ini.
//
// Server otoritatif: custody/holds adalah bacaan core/history (hanya
// view_workspace); hold + handover butuh manage_business_data +
// instalasi rental.basic aktif + entitlement versi aktif; return TIDAK
// butuh instalasi aktif (guard menyempit) — return tetap bisa sesudah
// deactivate/revoke. Kasir melihat formulir dan menerima 403 saat tulis
// (bukan disembunyikan). Tanpa-fakta HANYA pesan 404 "harga yang
// disepakati tidak ditemukan"; tanpa-custody HANYA pesan 404 "custody
// tidak ditemukan"; bacaan yang gagal/tertunda tidak pernah mengiklankan
// tulis yang bergantung padanya — kecuali return, yang sengaja TIDAK
// bergantung pada bacaan fakta/aset/holds.
//
// TIGA tahap, TIGA mesin: hold, handover, dan return masing-masing punya
// snapshot, kunci, dan pagar generasi sendiri. Kunci dibuat dengan
// crypto.randomUUID() saat panel konfirmasi dibuka, lalu dibekukan
// bersama snapshot tahap itu. GET custody TIDAK PERNAH menyelesaikan
// attempt hold (hold hanya diselesaikan lewat daftar holds); GET holds
// workspace-wide dan berpaginasi — tidak ditemukan di satu halaman BUKAN
// bukti tidak ada. Replay boleh mengembalikan hold yang sudah terminal
// (200 bukan berarti hold masih bisa dipakai) dan custody yang sudah
// terminal (jangan tolak riwayat valid hanya karena status bukan `out`).
//
// R1: state unknown TERKUNCI — tidak ada jalur "Mulai ... baru" dari
// panel unknown (retirement hanya dari konflik kunci definitif, dan
// handler menolak retirement saat unknown). Refresh membaca ulang daftar
// holds dari server secara terbatas (halaman yang sudah dimuat + satu
// halaman berikut) dan pagination manual diizinkan saat unknown, supaya
// hold yang tercatat sesudah respons hilang bisa diselesaikan lewat GET.
// Kiriman pertama hold/handover mensyaratkan hasil baca no-custody yang
// spesifik (custodyMissing), bukan sekadar `!custody` — GET custody yang
// gagal atau DTO-nya cacat tidak boleh membuka jalan tulis.
//
// R2: observasi GET terbaru untuk ID hold yang sama menggantikan cache
// (status efektif server expired/released menggantikan held) — merge
// tidak lagi hanya menambah ID baru. Refresh holds terbatas (halaman yang
// sudah dimuat + satu halaman berikut) jalan di setiap muat-ulang, juga
// tanpa upaya hold, supaya serah terima yang perlu pulih melihat status
// efektif terbaru; tanpa hitung TTL di klien, tanpa scan tanpa batas,
// dan satu halaman kosong bukan bukti hold hilang. HTTP 409 `hold tidak
// aktif` pada handover adalah penolakan definitif (server memeriksa
// replay dulu, lalu menolak sebelum mutasi): alasan server ditampilkan,
// hold lama berhenti dipakai, dan hold pengganti + handover berikutnya
// butuh konfirmasi pengguna. Pada kirim-ulang sesudah unknown, 409 yang
// sama hanya melepas kunci sesudah GET custody mengonfirmasi tidak ada
// custody dengan kunci itu — 409 saja, absensi daftar saja, atau GET
// custody 404 saja tidak cukup; bila bacaan gagal/cacat, snapshot dan
// kunci dipertahankan.
// Kunci return (`return_client_request_id`) TIDAK PERNAH dibandingkan
// dengan kunci handover. Hanya 201 atau replay 200 dengan baris yang
// valid yang mengonfirmasi tulis; return hanya 200. Hasil tak diketahui
// mengunci aksi biasa dan hanya menawarkan muat-ulang GET, kirim-ulang
// kunci-sama, atau "Mulai … baru" yang diperingatkan. Selama permintaan
// lama masih mungkin terkomit, tidak ada restart kunci-baru. Pemulihan
// hanya hidup di memori halaman ini.
//
// Konteks draf: fakta harga yang disepakati membawa pelanggan, barang,
// dan periode — fakta yang tervalidasi sudah cukup sebagai konteks.
// Baris draf dari lookup adalah pemeriksaan status tambahan, bukan gerbang:
// bila baris ditemukan ia wajib cocok, bila belum dimuat fakta tetap
// berlaku (server yang memutuskan 409 saat POST).
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Serah terima — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface DraftRow { id: string, workspace_id: string, customer_id: string, catalog_item_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, created_at: string, updated_at: string }
interface CustomerRow { id: string, workspace_id: string, display_name: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }
interface AssetRow { id: string, workspace_id: string, catalog_item_id: string, serial: string, created_at: string, updated_at: string }
interface FactRow { id: string, workspace_id: string, rental_contract_id: string, price_version_id: string, pricing_rule_version_id: string, catalog_item_id: string, customer_id: string, client_request_id: string, amount_minor: string, currency: string, units: number, total_minor: string, starts_at: string, ends_at: string, accepted_at: string, accepted_by: string }
interface HoldRow { id: string, workspace_id: string, draft_id: string, asset_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, expires_at: string, created_at: string, updated_at: string }
interface CustodyRow { id: string, workspace_id: string, rental_contract_id: string, rental_agreed_price_id: string, asset_id: string, hold_id: string, client_request_id: string, return_client_request_id: string | null, status: string, handed_out_at: string, returned_at: string | null, handed_out_by: string, returned_by: string | null, resolved_at: string | null, resolved_by: string | null }

const PAGE_LIMIT = 50
const PICKER_PAGE = 100
const MAX_INT64 = 9223372036854775807n
const FACT_NO_FACT = 'harga yang disepakati tidak ditemukan'
const FACT_INVALID = 'Fakta harga yang diterima tidak valid.'
const CUSTODY_NO_CUSTODY = 'custody tidak ditemukan'
const CUSTODY_INVALID = 'Catatan serah terima yang diterima tidak valid.'
const KEY_MISMATCH = 'kunci idempotency sudah dipakai untuk permintaan yang berbeda'
const CUSTODY_EXISTS = 'draft ini sudah memiliki custody'
const HOLD_INACTIVE = 'hold tidak aktif'
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

interface HoldAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  customerId: string
  customerLabel: string
  itemId: string
  itemLabel: string
  assetId: string
  assetLabel: string
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
}

interface HandAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  customerId: string
  customerLabel: string
  itemId: string
  itemLabel: string
  assetId: string
  assetLabel: string
  holdId: string
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
}

interface RetAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  customerId: string
  customerLabel: string
  itemId: string
  itemLabel: string
  assetId: string
  assetLabel: string
  custodyId: string
  outcome: 'returned' | 'damaged' | 'lost'
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
}

// Status hold adalah status EFEKTIF dari server (held yang kedaluwarsa
// sudah dipetakan jadi `expired` di DTO) — halaman tidak pernah
// menghitung TTL sendiri dari expires_at.
function isValidHoldRow(row: unknown, ws: string, draftId: string): row is HoldRow {
  if (!row || typeof row !== 'object') return false
  const h = row as HoldRow
  if (typeof h.id !== 'string' || h.id === '') return false
  if (h.workspace_id !== ws) return false
  if (typeof h.draft_id !== 'string' || h.draft_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof h.asset_id !== 'string' || !UUID_RE.test(h.asset_id)) return false
  if (typeof h.client_request_id !== 'string' || !KEY_RE.test(h.client_request_id)) return false
  if (h.status !== 'held' && h.status !== 'expired' && h.status !== 'released' && h.status !== 'consumed') return false
  const from = typeof h.starts_at === 'string' ? parseServerInstant(h.starts_at) : null
  const to = typeof h.ends_at === 'string' ? parseServerInstant(h.ends_at) : null
  if (from === null || to === null || !(to > from)) return false
  if (typeof h.expires_at !== 'string' || parseServerInstant(h.expires_at) === null) return false
  if (typeof h.created_at !== 'string' || parseServerInstant(h.created_at) === null) return false
  if (typeof h.updated_at !== 'string' || parseServerInstant(h.updated_at) === null) return false
  return true
}

function isValidHoldForAttempt(row: unknown, ws: string, a: HoldAttempt): row is HoldRow {
  if (!isValidHoldRow(row, ws, a.draftId)) return false
  const h = row as HoldRow
  if (h.client_request_id !== a.key) return false
  if (h.asset_id.toLowerCase() !== a.assetId.toLowerCase()) return false
  return true
}

// Semantik null custody: `out` => returned_*/resolved_* semua null;
// `returned`/`damaged` => returned_* DAN resolved_* terisi; `lost` =>
// returned_* null, resolved_* terisi.
function isValidCustodyRow(row: unknown, ws: string, draftId: string): row is CustodyRow {
  if (!row || typeof row !== 'object') return false
  const c = row as CustodyRow
  if (typeof c.id !== 'string' || c.id === '') return false
  if (c.workspace_id !== ws) return false
  if (typeof c.rental_contract_id !== 'string' || c.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof c.rental_agreed_price_id !== 'string' || !UUID_RE.test(c.rental_agreed_price_id)) return false
  if (typeof c.asset_id !== 'string' || !UUID_RE.test(c.asset_id)) return false
  if (typeof c.hold_id !== 'string' || !UUID_RE.test(c.hold_id)) return false
  if (typeof c.client_request_id !== 'string' || !KEY_RE.test(c.client_request_id)) return false
  if (c.return_client_request_id !== null && (typeof c.return_client_request_id !== 'string' || !KEY_RE.test(c.return_client_request_id))) return false
  if (c.status !== 'out' && c.status !== 'returned' && c.status !== 'damaged' && c.status !== 'lost') return false
  if (typeof c.handed_out_at !== 'string' || parseServerInstant(c.handed_out_at) === null) return false
  if (typeof c.handed_out_by !== 'string' || c.handed_out_by === '') return false
  const retAt = c.returned_at === null ? null : (typeof c.returned_at === 'string' ? parseServerInstant(c.returned_at) : false)
  const resAt = c.resolved_at === null ? null : (typeof c.resolved_at === 'string' ? parseServerInstant(c.resolved_at) : false)
  if (retAt === false || resAt === false) return false
  if (c.status === 'out') {
    if (retAt !== null || c.returned_by !== null || resAt !== null || c.resolved_by !== null) return false
  } else if (c.status === 'returned' || c.status === 'damaged') {
    if (retAt === null || typeof c.returned_by !== 'string' || c.returned_by === '') return false
    if (resAt === null || typeof c.resolved_by !== 'string' || c.resolved_by === '') return false
  } else {
    if (retAt !== null || c.returned_by !== null) return false
    if (resAt === null || typeof c.resolved_by !== 'string' || c.resolved_by === '') return false
  }
  return true
}

function isValidCustodyForHandoverAttempt(row: unknown, ws: string, a: HandAttempt): row is CustodyRow {
  if (!isValidCustodyRow(row, ws, a.draftId)) return false
  const c = row as CustodyRow
  if (c.client_request_id !== a.key) return false
  if (c.asset_id.toLowerCase() !== a.assetId.toLowerCase()) return false
  if (c.hold_id.toLowerCase() !== a.holdId.toLowerCase()) return false
  return true
}

function isValidCustodyForReturnAttempt(row: unknown, ws: string, a: RetAttempt): row is CustodyRow {
  if (!isValidCustodyRow(row, ws, a.draftId)) return false
  const c = row as CustodyRow
  if (c.id.toLowerCase() !== a.custodyId.toLowerCase()) return false
  if (c.return_client_request_id !== a.key) return false
  if (c.status !== a.outcome) return false
  return true
}

function isValidDraftForFact(row: unknown, ws: string, draftId: string, f: FactRow): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id.toLowerCase() !== draftId.toLowerCase()) return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || r.customer_id === '') return false
  if (typeof r.catalog_item_id !== 'string' || r.catalog_item_id.toLowerCase() !== f.catalog_item_id.toLowerCase()) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
  return from === parseServerInstant(f.starts_at) && to === parseServerInstant(f.ends_at)
}

function isValidDraftBasic(row: unknown, ws: string, draftId: string): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id.toLowerCase() !== draftId.toLowerCase()) return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || r.customer_id === '') return false
  if (typeof r.catalog_item_id !== 'string' || !UUID_RE.test(r.catalog_item_id)) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
  return from !== null && to !== null && to > from
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

const fact = ref<FactRow | null>(null)
const factLoading = ref(false)
const factError = ref<string | null>(null)
const factMissing = ref(false)

const custody = ref<CustodyRow | null>(null)
const custodyLoading = ref(false)
const custodyError = ref<string | null>(null)
const custodyMissing = ref(false)

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

const assetRows = ref<AssetRow[]>([])
const assetOffset = ref(0)
const assetExhausted = ref(false)
const assetLoading = ref(false)
const assetLoadError = ref<string | null>(null)
const assetStarted = ref(false)
const assetById = computed(() => new Map(assetRows.value.map(r => [r.id, r])))
const selectedAssetId = ref('')

const holdRows = ref<HoldRow[]>([])
const holdOffset = ref(0)
const holdExhausted = ref(false)
const holdListLoading = ref(false)
const holdLoadError = ref<string | null>(null)
const holdStarted = ref(false)
// Hold yang server tolak definitif sebagai tidak aktif (409 handover):
// berhenti ditawarkan sampai halaman dimuat ulang, apa pun kata cache
// GET berikutnya — penolakan server lebih segar daripada observasi
// daftar. Bukan status yang ditebak: status efektif tetap dibaca dari
// GET; set ini hanya mengecualikan pemakaian ulang hold itu.
const deadHoldIds = ref<string[]>([])

// Tahap hold.
const holdAttempt = ref<HoldAttempt | null>(null)
const holdUnknown = ref(false)
const holdConflict = ref<'key' | null>(null)
const holdPostDenied = ref<string | null>(null)
const holdReplayError = ref<string | null>(null)
const holdSettleError = ref<string | null>(null)
const holdDeadNote = ref<string | null>(null)
const holdBusy = ref(false)

// Tahap handover.
const handAttempt = ref<HandAttempt | null>(null)
const handUnknown = ref(false)
const handConflict = ref<'key' | null>(null)
const handPostDenied = ref<string | null>(null)
const handReplayError = ref<string | null>(null)
const handSettleError = ref<string | null>(null)
const handRecoveryError = ref<string | null>(null)
const handInactiveNote = ref<string | null>(null)
const handBusy = ref(false)

// Tahap return.
const retAttempt = ref<RetAttempt | null>(null)
const retUnknown = ref(false)
const retConflict = ref<'key' | null>(null)
const retPostDenied = ref<string | null>(null)
const retReplayError = ref<string | null>(null)
const retSettleError = ref<string | null>(null)
const retRecoveryError = ref<string | null>(null)
const retBusy = ref(false)
const returnOutcome = ref<'' | 'returned' | 'damaged' | 'lost'>('')

const refreshing = ref(false)
const custodyHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)

// Konteks draf: baris draf bila lookup menemukannya; bila fakta sudah
// ada, fakta itu sendiri adalah konteksnya.
const contextCustomerId = computed(() => draftRow.value?.customer_id ?? fact.value?.customer_id ?? null)
const contextItemId = computed(() => draftRow.value?.catalog_item_id ?? fact.value?.catalog_item_id ?? null)
const contextFromIso = computed(() => draftRow.value?.starts_at ?? fact.value?.starts_at ?? null)
const contextToIso = computed(() => draftRow.value?.ends_at ?? fact.value?.ends_at ?? null)
// Baris draf yang ditemukan wajib cocok; bila belum dimuat, fakta yang
// tervalidasi sudah cukup — server yang memutuskan 409 saat POST.
const contextOk = computed(() => !draftRow.value || draftValid.value)
const showCustomerMore = computed(() => !!contextCustomerId.value && !customerById.value.get(contextCustomerId.value) && !customerExhausted.value)
const showCatalogMore = computed(() => !!contextItemId.value && !catalogById.value.get(contextItemId.value) && !catalogExhausted.value)
const filteredAssets = computed(() => contextItemId.value ? assetRows.value.filter(a => a.catalog_item_id.toLowerCase() === contextItemId.value!.toLowerCase()) : [])
const showAssetMore = computed(() => !!contextItemId.value && !custody.value && !assetExhausted.value && assetStarted.value)
// Hold yang masih hidup untuk (draf, unit terpilih) — dipakai ulang,
// bukan dibuat lagi.
const currentHold = computed(() => {
  if (!selectedAssetId.value) return null
  return holdRows.value.find(h => h.draft_id.toLowerCase() === draftId.value.toLowerCase()
    && h.asset_id.toLowerCase() === selectedAssetId.value.toLowerCase()
    && h.status === 'held'
    && !deadHoldIds.value.includes(h.id)) ?? null
})
const showHoldsMore = computed(() => !!selectedAssetId.value && !currentHold.value && !holdExhausted.value
  && holdStarted.value && !custody.value && (!holdAttempt.value || holdUnknown.value))
const canHold = computed(() => !!fact.value && custodyMissing.value && contextOk.value
  && !!selectedAssetId.value && !currentHold.value
  && !holdAttempt.value && !holdUnknown.value && holdConflict.value === null && !holdBusy.value
  && !handAttempt.value && !handUnknown.value
  && !factLoading.value && !custodyLoading.value && !refreshing.value)
const canHandover = computed(() => !!fact.value && custodyMissing.value && contextOk.value
  && !!currentHold.value
  && !handAttempt.value && !handUnknown.value && handConflict.value === null && !handBusy.value
  && !holdAttempt.value && !holdUnknown.value
  && !factLoading.value && !custodyLoading.value && !refreshing.value)
// Return sengaja TIDAK mensyaratkan fakta/aset/holds: guard backend
// menyempit, dan custody + izin sudah divalidasi di tempat lain.
const retFormVisible = computed(() => !!custody.value && custody.value.status === 'out'
  && !retAttempt.value && !retUnknown.value && retConflict.value === null)
const canReturn = computed(() => retFormVisible.value && returnOutcome.value !== ''
  && !retBusy.value && !refreshing.value)
const holdHint = computed(() => {
  if (custody.value) return ''
  if (!fact.value && !factError.value) return 'Harga yang disepakati belum dimuat.'
  if (factError.value) return 'Harga yang disepakati tidak dapat dibaca; serah terima belum tersedia.'
  if (!factMissing.value) return 'Status harga yang dicatat belum dapat dipastikan; serah terima belum tersedia.'
  if (!selectedAssetId.value && !currentHold.value) return 'Pilih unit untuk membuat hold atau memakai hold yang masih aktif.'
  return ''
})
const custodyAssetLabel = computed(() => custody.value ? assetLabelFor(custody.value.asset_id) : '')

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

function assetLabelFor(assetId: string): string {
  const row = assetById.value.get(assetId)
  if (row) return row.serial
  return assetExhausted.value ? 'Unit tidak tersedia' : 'Unit belum dimuat'
}

// Penjaga generasi: hasil muat yang tiba sesudah reset/pindah draf/
// workspace/logout diabaikan. inFlight dijaga sinkron (bukan hanya tombol
// disabled). *Gen: kepemilikan POST per tahap — kelanjutan POST basi
// (sukses, galat, nextTick, refresh) tidak boleh menyentuh halaman/sesi/
// daftar pengganti. *LoadGen: kepemilikan flag busy — finally basi tidak
// boleh menghapus busy milik permintaan baru.
let listGen = 0
let factGen = 0
let factLoadGen = 0
let custodyGen = 0
let custodyLoadGen = 0
let draftGen = 0
let draftLoadGen = 0
let customerGen = 0
let customerLoadGen = 0
let catalogGen = 0
let catalogLoadGen = 0
let assetGen = 0
let assetLoadGen = 0
let holdListGen = 0
let holdListLoadGen = 0
let refreshGen = 0
let holdGen = 0
let holdLoadGen = 0
let handGen = 0
let handLoadGen = 0
let retGen = 0
let retLoadGen = 0
let factInFlight = false
let custodyInFlight = false
let draftInFlight = false
let customerInFlight = false
let catalogInFlight = false
let assetInFlight = false
let holdListInFlight = false
let holdInFlight = false
let handInFlight = false
let retInFlight = false

function stale(gen: number) {
  return gen !== listGen
}

function staleHold(gen: number) {
  return gen !== holdGen
}

function staleHand(gen: number) {
  return gen !== handGen
}

function staleRet(gen: number) {
  return gen !== retGen
}

function bumpAllGens() {
  listGen++
  factGen++
  factLoadGen++
  custodyGen++
  custodyLoadGen++
  draftGen++
  draftLoadGen++
  customerGen++
  customerLoadGen++
  catalogGen++
  catalogLoadGen++
  assetGen++
  assetLoadGen++
  holdListGen++
  holdListLoadGen++
  refreshGen++
  holdGen++
  holdLoadGen++
  handGen++
  handLoadGen++
  retGen++
  retLoadGen++
}

function clearHoldStage() {
  holdAttempt.value = null
  holdUnknown.value = false
  holdConflict.value = null
  holdPostDenied.value = null
  holdReplayError.value = null
  holdSettleError.value = null
}

function clearHandStage() {
  handAttempt.value = null
  handUnknown.value = false
  handConflict.value = null
  handPostDenied.value = null
  handReplayError.value = null
  handSettleError.value = null
}

function clearRetStage() {
  retAttempt.value = null
  retUnknown.value = false
  retConflict.value = null
  retPostDenied.value = null
  retReplayError.value = null
  retSettleError.value = null
}

// Transisi baca ditolak (GET 403 sesudah halaman termuat): beri umpan
// balik izin workspace, sembunyikan konten yang sudah termuat, dan
// batalkan kepemilikan baca/POST yang tertunda supaya penyelesaian
// terlambat tidak mengisi ulang halaman pengganti. Sesi dipertahankan
// — 403 bukan 401. Berbeda dengan POST 403 yang mempertahankan data
// yang terbaca + snapshot percobaan. Hanya berlaku untuk bacaan inti
// (fakta/custody/draf/label pelanggan/katalog); bacaan konteks handover
// (aset/holds) yang 403 hanya jadi galat lokal agar return tetap bisa.
function denyRead(message: string) {
  bumpAllGens()
  factInFlight = false
  custodyInFlight = false
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  assetInFlight = false
  holdListInFlight = false
  holdInFlight = false
  handInFlight = false
  retInFlight = false
  holdBusy.value = false
  handBusy.value = false
  retBusy.value = false
  refreshing.value = false
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  custody.value = null
  custodyLoading.value = false
  custodyError.value = null
  custodyMissing.value = false
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
  assetRows.value = []
  assetOffset.value = 0
  assetExhausted.value = false
  assetLoadError.value = null
  assetLoading.value = false
  selectedAssetId.value = ''
  holdRows.value = []
  holdOffset.value = 0
  holdExhausted.value = false
  holdLoadError.value = null
  holdListLoading.value = false
  clearHoldStage()
  clearHandStage()
  clearRetStage()
  holdDeadNote.value = null
  handInactiveNote.value = null
  deadHoldIds.value = []
  handRecoveryError.value = null
  retRecoveryError.value = null
  returnOutcome.value = ''
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
  factInFlight = false
  custodyInFlight = false
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  assetInFlight = false
  holdListInFlight = false
  holdInFlight = false
  handInFlight = false
  retInFlight = false
  holdBusy.value = false
  handBusy.value = false
  retBusy.value = false
  refreshing.value = false
  loading.value = false
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  workspace.value = null
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  custody.value = null
  custodyLoading.value = false
  custodyError.value = null
  custodyMissing.value = false
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
  assetRows.value = []
  assetOffset.value = 0
  assetExhausted.value = false
  assetLoadError.value = null
  assetLoading.value = false
  assetStarted.value = false
  selectedAssetId.value = ''
  holdRows.value = []
  holdOffset.value = 0
  holdExhausted.value = false
  holdLoadError.value = null
  holdListLoading.value = false
  holdStarted.value = false
  clearHoldStage()
  clearHandStage()
  clearRetStage()
  holdDeadNote.value = null
  handInactiveNote.value = null
  deadHoldIds.value = []
  handRecoveryError.value = null
  retRecoveryError.value = null
  returnOutcome.value = ''
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
  if (!stale(gen)) await fetchFact()
  if (readDenied.value) return
  if (!stale(gen)) await fetchCustody()
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
  if (!stale(gen) && !assetStarted.value) {
    assetStarted.value = true
    await loadAssetPage()
  }
  if (readDenied.value) return
  if (!stale(gen)) await maybeStartDraftLookup()
  if (readDenied.value) return
  if (!stale(gen) && !holdStarted.value) {
    holdStarted.value = true
    await loadHoldsPage()
  }
  if (!stale(gen)) loading.value = false
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

// Satu bacaan custody. Tanpa-custody HANYA pesan 404 spesifik; 404 lain
// (draf hilang) dan galat lain adalah kegagalan baca, bukan "belum ada".
async function fetchCustody() {
  const s = session
  if (!s || custodyInFlight) return
  const gen = ++custodyGen
  const myLoad = ++custodyLoadGen
  custodyInFlight = true
  custodyLoading.value = true
  try {
    const res = await apiFetch<CustodyRow>(s, `/api/workspaces/${wsId.value}/rental/contracts/${draftId.value}/custody`)
    if (gen !== custodyGen) return
    if (isValidCustodyRow(res.data, wsId.value, draftId.value)) {
      custody.value = res.data
      custodyMissing.value = false
      custodyError.value = null
    } else {
      custody.value = null
      custodyMissing.value = false
      custodyError.value = CUSTODY_INVALID
    }
  } catch (err) {
    if (gen !== custodyGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    custody.value = null
    if (err instanceof ApiError && err.status === 404 && err.message === CUSTODY_NO_CUSTODY) {
      custodyMissing.value = true
      custodyError.value = null
    } else if (err instanceof ApiError) {
      custodyMissing.value = false
      custodyError.value = err.message
    } else {
      custodyMissing.value = false
      custodyError.value = 'Terjadi kesalahan. Coba lagi.'
    }
  } finally {
    if (myLoad === custodyLoadGen) {
      custodyInFlight = false
      custodyLoading.value = false
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

// Satu halaman unit (100 baris). Daftar unit TIDAK membawa kondisi —
// picker hanya menyaring menurut barang draf; server yang memutuskan
// 409 bila unit tidak bisa dipakai. Galat baca (termasuk 403) hanya jadi
// galat lokal: return tidak bergantung pada bacaan ini.
async function loadAssetPage() {
  const s = session
  if (!s || assetInFlight) return
  const gen = assetGen
  const myLoad = ++assetLoadGen
  const wantOffset = assetOffset.value
  assetInFlight = true
  assetLoading.value = true
  assetLoadError.value = null
  try {
    const res = await apiFetch<{ items: AssetRow[] }>(s, `/api/workspaces/${wsId.value}/assets?limit=${PICKER_PAGE}&offset=${wantOffset}`)
    if (gen !== assetGen) return
    const seen = new Set(assetRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        assetRows.value.push(row)
        seen.add(row.id)
      }
    }
    assetOffset.value = wantOffset + res.data.items.length
    assetExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== assetGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) assetLoadError.value = err.message
    else assetLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === assetLoadGen) {
      assetInFlight = false
      assetLoading.value = false
    }
  }
}

// Satu halaman koleksi draf (50 baris) untuk konteks draf yang dipilih.
// Hanya berjalan sesudah status fakta terverifikasi (ada atau tanpa);
// tanpa rantai otomatis dan tanpa sapuan sampai habis. Baris yang
// ditemukan harus cocok dengan fakta (bila ada) sebelum dipakai sebagai
// konteks; bila fakta belum ada, cukup id/workspace/status `draft`.
async function loadDraftPage() {
  const s = session
  if (!s || draftInFlight) return
  if (!fact.value && !factMissing.value) return
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
      draftValid.value = fact.value
        ? isValidDraftForFact(found, wsId.value, draftId.value, fact.value)
        : isValidDraftBasic(found, wsId.value, draftId.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan harga yang dicatat.'
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
  if (!fact.value && !factMissing.value) return
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

// Satu halaman holds workspace (50 baris, urutan server: dibuat paling
// dulu). Tanpa rantai otomatis: halaman berikut hanya lewat tombol
// "Muat lebih banyak hold". Tidak ditemukan di halaman yang dimuat BUKAN
// bukti tidak ada. Galat baca (termasuk 403) hanya jadi galat lokal:
// return tidak bergantung pada bacaan ini. Sesudah menggabungkan halaman,
// attempt hold yang belum pasti dicocokkan ulang — pagination manual bisa
// menyelesaikan hold yang tercatat sesudah respons hilang.
async function fetchHoldsPage(offset: number): Promise<HoldRow[]> {
  const s = session
  if (!s) return []
  const res = await apiFetch<{ items: HoldRow[] }>(s, `/api/workspaces/${wsId.value}/rental/holds?limit=${PAGE_LIMIT}&offset=${offset}`)
  return res.data.items
}

function mergeHoldRows(items: HoldRow[]) {
  // Rekonsiliasi: observasi GET terbaru untuk ID yang sama MENGGANTIKAN
  // baris cache — status efektif server (expired/released) menggantikan
  // held yang tersimpan. Tanpa ini, hold yang sudah mati tetap
  // ditawarkan sampai halaman dimuat ulang.
  for (const row of items) {
    const i = holdRows.value.findIndex(h => h.id === row.id)
    if (i >= 0) holdRows.value[i] = row
    else holdRows.value.push(row)
  }
}

async function loadHoldsPage() {
  const s = session
  if (!s || holdListInFlight) return
  const gen = holdListGen
  const myLoad = ++holdListLoadGen
  const wantOffset = holdOffset.value
  holdListInFlight = true
  holdListLoading.value = true
  holdLoadError.value = null
  try {
    const items = await fetchHoldsPage(wantOffset)
    if (gen !== holdListGen) return
    mergeHoldRows(items)
    holdOffset.value = wantOffset + items.length
    holdExhausted.value = items.length < PAGE_LIMIT
    adoptLiveHold()
    await settleHoldFromRows()
  } catch (err) {
    if (gen !== holdListGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) holdLoadError.value = err.message
    else holdLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === holdListLoadGen) {
      holdListInFlight = false
      holdListLoading.value = false
    }
  }
}

// Bila unit belum dipilih dan ada hold hidup untuk draf ini di halaman
// yang sudah dimuat, pakai hold itu: pilih unitnya supaya serah terima
// bisa langsung jalan tanpa membuat hold baru.
function adoptLiveHold() {
  if (selectedAssetId.value) return
  const live = holdRows.value.find(h => h.draft_id.toLowerCase() === draftId.value.toLowerCase() && h.status === 'held' && !deadHoldIds.value.includes(h.id))
  if (live) selectedAssetId.value = live.asset_id
}

// Cocokkan attempt hold dengan baris holds yang sudah dimuat.
// Terkonfirmasi hidup / dinyatakan mati: kembalikan true (attempt selesai
// dan POST lamanya dipagari lewat holdGen++). Kunci sama tetapi field
// tidak cocok: setel holdSettleError, kembalikan false — snapshot/kunci/
// ketidakpastian dipertahankan. Tidak ditemukan: bukan bukti tidak ada,
// kembalikan false.
async function settleHoldFromRows(): Promise<boolean> {
  const a = holdAttempt.value
  if (!a) return false
  const found = holdRows.value.find(h => h.client_request_id === a.key)
  if (!found) return false
  if (isValidHoldForAttempt(found, wsId.value, a)) {
    if (found.status === 'held') {
      await confirmHold(found, holdGen)
      // GET menyelesaikan attempt: pagar kelanjutan POST lama.
      holdGen++
    } else {
      // Kunci sama, hold sudah terminal: permintaan lama selesai dengan
      // hold yang tidak lagi aktif — bukan sukses, bukan ketidakpastian.
      // Kunci baru boleh dibuat sesudahnya.
      clearHoldStage()
      holdDeadNote.value = 'Hold ini sudah tidak aktif dan tidak dapat dipakai. Buat hold baru bila masih perlu.'
      holdGen++
    }
    return true
  }
  holdSettleError.value = 'Catatan hold yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; pembuatan hold tetap tidak diketahui.'
  return false
}

// Baca ulang halaman holds yang sudah dimuat (dari offset 0) dengan bacaan
// server yang terbatas: hanya halaman yang pernah dimuat — tanpa POST dan
// tanpa rantai otomatis. Cache bisa basi karena hold baru dibuat sesudah
// halaman dimuat (daftar diurutkan created_at menaik, jadi hold baru
// mendarat di halaman yang sudah dimuat atau tepat sesudahnya). Galat baca
// (termasuk 403) hanya jadi galat lokal; attempt tetap unknown.
async function reloadHoldsLoaded() {
  const s = session
  if (!s || holdListInFlight) return
  const gen = holdListGen
  const myLoad = ++holdListLoadGen
  holdListInFlight = true
  holdListLoading.value = true
  holdLoadError.value = null
  try {
    const limit = Math.max(holdOffset.value, PAGE_LIMIT)
    for (let off = 0; off < limit; off += PAGE_LIMIT) {
      if (gen !== holdListGen) return
      const items = await fetchHoldsPage(off)
      if (gen !== holdListGen) return
      mergeHoldRows(items)
    }
  } catch (err) {
    if (gen !== holdListGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) holdLoadError.value = err.message
    else holdLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === holdListLoadGen) {
      holdListInFlight = false
      holdListLoading.value = false
    }
  }
}

// Baca ulang holds secara terbatas: halaman yang sudah dimuat +
// satu halaman berikut, lalu cocokkan attempt hold. Tanpa POST, tanpa
// rantai otomatis; satu halaman kosong bukan bukti hold hilang.
// Dipakai muat-ulang dan pemulihan 409 — mergeHoldRows merekonsiliasi
// status efektif server untuk ID yang sama.
async function refreshHoldsBounded() {
  await reloadHoldsLoaded()
  if (readDenied.value) return
  if (!holdExhausted.value) {
    await loadHoldsPage()
    if (readDenied.value) return
  }
  await settleHoldFromRows()
}

// Muat-ulang GET saja (fakta + custody + baca ulang holds terbatas):
// tidak mengganti snapshot, tidak mengonfirmasi apa pun, tidak menulis.
// Dipakai tombol "Muat ulang data" dan sesudah retirement. Penyelesaian
// per tahap, urut ketergantungan hold -> handover -> return. Hold HANYA
// diselesaikan lewat daftar holds (GET custody tidak pernah menyelesaikan
// hold). Setiap penyelesaian otoritatif memagari POST lama tahap itu supaya
// hasil/salah lamanya tidak membangkitkan panel.
async function refreshReads(failureMessage: string) {
  const s = session
  if (!s || refreshing.value) return
  const gen = ++refreshGen
  refreshing.value = true
  holdSettleError.value = null
  handSettleError.value = null
  retSettleError.value = null
  try {
    await fetchFact()
    if (readDenied.value) return
    await fetchCustody()
    if (readDenied.value) return
    if (gen !== refreshGen) return
    // Tahap hold: baca ulang daftar holds dari server (terbatas: halaman
    // yang sudah dimuat + satu halaman berikut), lalu cocokkan kunci.
    // Jalan di setiap muat-ulang — juga tanpa upaya hold — supaya serah
    // terima yang perlu pulih melihat status efektif terbaru. Tidak
    // ditemukan di halaman yang dimuat: bukan bukti tidak ada.
    // Ketidakpastian dipertahankan; pengguna bisa memuat halaman holds
    // berikut atau mengirim ulang dengan kunci yang sama.
    await refreshHoldsBounded()
    if (readDenied.value) return
    if (gen !== refreshGen) return
    // Tahap handover: cocokkan kunci + identitas custody.
    if (handAttempt.value && custody.value) {
      if (isValidCustodyForHandoverAttempt(custody.value, wsId.value, handAttempt.value)) {
        await confirmHand(custody.value, handGen)
        // GET menyelesaikan attempt: pagar kelanjutan POST lama agar
        // hasil/salah lamanya tidak membangkitkan panel pemulihan.
        handGen++
        return
      }
      if (custody.value.client_request_id !== handAttempt.value.key) {
        // Kunci benar-benar berbeda: catatan pihak lain — riwayat yang
        // dicatat aksi lain, bukan sukses aksi ini. Attempt mati; tanpa
        // kirim-ulang dan tanpa klaim sukses atas aksi ini.
        clearHandStage()
        handGen++
        return
      }
      // Kunci sama tetapi field tidak cocok: tidak mengonfirmasi apa pun.
      // Snapshot/kunci/ketidakpastian dipertahankan; kandidat TIDAK
      // dipromosikan sebagai catatan tervalidasi.
      custody.value = null
      custodyMissing.value = false
      // custodyError TIDAK diisi di sini: pesan yang sama sudah tampil
      // sekali sebagai handSettleError di panel pemulihan (pola UI-05
      // R1-1A); mengisinya juga menduplikasi tampilan.
      handSettleError.value = 'Catatan serah terima yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; serah terima tetap tidak diketahui.'
      return
    }
    if (handAttempt.value && custodyError.value) {
      handSettleError.value = custodyError.value === CUSTODY_INVALID
        ? 'Catatan serah terima yang dibaca tidak cocok dengan permintaan ini; serah terima tetap tidak diketahui.'
        : 'Catatan serah terima tidak dapat dibaca saat ini; serah terima tetap tidak diketahui.'
    }
    // Tahap return: cocokkan kunci return + hasil yang diminta.
    if (retAttempt.value && custody.value) {
      if (isValidCustodyForReturnAttempt(custody.value, wsId.value, retAttempt.value)) {
        await confirmReturn(custody.value, retGen)
        retGen++
        return
      }
      if (custody.value.return_client_request_id !== null && custody.value.return_client_request_id !== retAttempt.value.key) {
        // Kunci return benar-benar berbeda: pengembalian pihak lain.
        // Attempt mati; tanpa kirim-ulang dan tanpa klaim sukses.
        clearRetStage()
        retGen++
        return
      }
      if (custody.value.return_client_request_id !== null) {
        // Kunci return sama tetapi hasil tidak cocok: tidak mengonfirmasi
        // apa pun. Snapshot/kunci/ketidakpastian dipertahankan.
        custody.value = null
        custodyMissing.value = false
        custodyError.value = 'Catatan serah terima yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini.'
        retSettleError.value = 'Catatan serah terima yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; pengembalian tetap tidak diketahui.'
        return
      }
      // Kunci return masih null: pengembalian ini belum tercatat.
      // Ketidakpastian dipertahankan.
    }
    if (retAttempt.value && custodyError.value) {
      retSettleError.value = custodyError.value === CUSTODY_INVALID
        ? 'Catatan serah terima yang dibaca tidak cocok dengan permintaan ini; pengembalian tetap tidak diketahui.'
        : 'Catatan serah terima tidak dapat dibaca saat ini; pengembalian tetap tidak diketahui.'
    }
    if (!handAttempt.value && !retAttempt.value && fact.value && draftRow.value) {
      draftValid.value = isValidDraftForFact(draftRow.value, wsId.value, draftId.value, fact.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan harga yang dicatat.'
    }
    if (!handAttempt.value && !retAttempt.value) await maybeStartDraftLookup()
  } catch {
    if (gen === refreshGen && !readDenied.value) pageError.value = failureMessage
  } finally {
    if (gen === refreshGen) refreshing.value = false
  }
}

function focusFormAlert(gen: number, staleFn: (g: number) => boolean) {
  nextTick().then(() => {
    if (!staleFn(gen)) formAlert.value?.focus()
  })
}

function setHoldUnknown(gen: number) {
  holdUnknown.value = true
  focusFormAlert(gen, staleHold)
}

function setHandUnknown(gen: number) {
  handUnknown.value = true
  focusFormAlert(gen, staleHand)
}

function setRetUnknown(gen: number) {
  retUnknown.value = true
  focusFormAlert(gen, staleRet)
}

// Kunci tercemar (409 mismatch): kunci sudah terikat pada permintaan lain.
// Satu status yang koheren: panel tak diketahui disembunyikan
// (kirim-ulang tidak lagi ditawarkan), snapshot dipertahankan untuk
// ringkasan beku, kirim terkunci, dan hanya "Mulai … baru" yang boleh
// menebus — tanpa kirim-ulang buta.
function setHoldKeyMismatch(gen: number) {
  holdConflict.value = 'key'
  holdUnknown.value = false
  holdPostDenied.value = null
  focusFormAlert(gen, staleHold)
}

function setHandKeyMismatch(gen: number) {
  handConflict.value = 'key'
  handUnknown.value = false
  handPostDenied.value = null
  focusFormAlert(gen, staleHand)
}

function setRetKeyMismatch(gen: number) {
  retConflict.value = 'key'
  retUnknown.value = false
  retPostDenied.value = null
  focusFormAlert(gen, staleRet)
}

// Custody sudah ada/terminal (409): tulis ini pasti tidak terjadi;
// attempt tidak lagi relevan. Pulihkan catatan lewat GET — tampilkan
// bila terbaca, beri galat pemulihan bila tidak.
async function recoverCustody(stage: 'hand' | 'ret') {
  if (stage === 'hand') clearHandStage()
  else clearRetStage()
  await fetchCustody()
  if (readDenied.value) return
  if (!custody.value) {
    if (stage === 'hand') handRecoveryError.value = 'Serah terima sudah tercatat untuk draf ini, tetapi catatannya tidak dapat dibaca. Coba muat ulang data.'
    else retRecoveryError.value = 'Pengembalian sudah tercatat untuk draf ini, tetapi catatannya tidak dapat dibaca. Coba muat ulang data.'
  }
}

async function confirmHold(row: HoldRow, gen: number) {
  const i = holdRows.value.findIndex(h => h.id === row.id)
  if (i >= 0) holdRows.value[i] = row
  else holdRows.value.push(row)
  selectedAssetId.value = row.asset_id
  clearHoldStage()
  holdDeadNote.value = null
  handInactiveNote.value = null
  await nextTick()
  if (staleHold(gen)) return
  custodyHeading.value?.focus()
}

async function confirmHand(row: CustodyRow, gen: number) {
  custody.value = row
  custodyMissing.value = false
  const i = holdRows.value.findIndex(h => h.id === row.hold_id)
  if (i >= 0) holdRows.value[i] = { ...holdRows.value[i], status: 'consumed' }
  clearHandStage()
  handRecoveryError.value = null
  handInactiveNote.value = null
  await nextTick()
  if (staleHand(gen)) return
  custodyHeading.value?.focus()
}

async function confirmReturn(row: CustodyRow, gen: number) {
  custody.value = row
  custodyMissing.value = false
  clearRetStage()
  retRecoveryError.value = null
  returnOutcome.value = ''
  await nextTick()
  if (staleRet(gen)) return
  custodyHeading.value?.focus()
}

function attemptContext(): { customerId: string, customerLabel: string, itemId: string, itemLabel: string, fromMicros: bigint, toMicros: bigint, fromIso: string, toIso: string } | null {
  const actor = session?.subjectId.value ?? ''
  const cid = contextCustomerId.value
  const iid = contextItemId.value
  const fromIso = contextFromIso.value
  const toIso = contextToIso.value
  if (!actor || !cid || !iid || !fromIso || !toIso) return null
  const fromMicros = parseServerInstant(fromIso)
  const toMicros = parseServerInstant(toIso)
  if (fromMicros === null || toMicros === null) return null
  return { customerId: cid, customerLabel: customerLabel(cid), itemId: iid, itemLabel: catalogLabel(iid), fromMicros, toMicros, fromIso, toIso }
}

// Buka panel konfirmasi hold: buat kunci SEKALI dan bekukan snapshot dari
// fakta + konteks draf + unit yang dipilih saat itu. Tanpa POST saat dibuka.
function openHoldConfirmation() {
  if (!canHold.value || !custodyMissing.value || holdAttempt.value) return
  const ctx = attemptContext()
  if (!ctx) return
  holdAttempt.value = {
    key: crypto.randomUUID(),
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    customerId: ctx.customerId,
    customerLabel: ctx.customerLabel,
    itemId: ctx.itemId,
    itemLabel: ctx.itemLabel,
    assetId: selectedAssetId.value,
    assetLabel: assetLabelFor(selectedAssetId.value),
    fromMicros: ctx.fromMicros,
    toMicros: ctx.toMicros,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  holdPostDenied.value = null
  holdReplayError.value = null
  holdSettleError.value = null
  holdDeadNote.value = null
  focusFormAlert(holdGen, staleHold)
}

// Buka panel konfirmasi serah terima: bekukan hold yang masih hidup.
function openHandConfirmation() {
  if (!canHandover.value || !custodyMissing.value || handAttempt.value) return
  const ctx = attemptContext()
  const hold = currentHold.value
  if (!ctx || !hold) return
  handAttempt.value = {
    key: crypto.randomUUID(),
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    customerId: ctx.customerId,
    customerLabel: ctx.customerLabel,
    itemId: ctx.itemId,
    itemLabel: ctx.itemLabel,
    assetId: hold.asset_id,
    assetLabel: assetLabelFor(hold.asset_id),
    holdId: hold.id,
    fromMicros: ctx.fromMicros,
    toMicros: ctx.toMicros,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  handPostDenied.value = null
  handReplayError.value = null
  handSettleError.value = null
  focusFormAlert(handGen, staleHand)
}

// Buka panel konfirmasi pengembalian: bekukan hasil yang dipilih.
// Klarifikasi 6: return TIDAK mensyaratkan bacaan fakta/aset/holds.
// Bila konteks (fakta) tidak terbaca, panel tetap dibuka dengan konteks
// "belum dimuat" — POST return hanya butuh { outcome } + kunci, dan
// settlement hanya mencocokkan id custody + kunci return + hasil.
function openRetConfirmation() {
  if (!canReturn.value || retAttempt.value) return
  const ctx = attemptContext()
  const c = custody.value
  if (!c || c.status !== 'out' || returnOutcome.value === '') return
  retAttempt.value = {
    key: crypto.randomUUID(),
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    customerId: ctx?.customerId ?? '',
    customerLabel: ctx?.customerLabel ?? 'Nama pelanggan belum dimuat',
    itemId: ctx?.itemId ?? '',
    itemLabel: ctx?.itemLabel ?? 'Nama barang belum dimuat',
    assetId: c.asset_id,
    assetLabel: assetLabelFor(c.asset_id),
    custodyId: c.id,
    outcome: returnOutcome.value,
    fromMicros: ctx?.fromMicros ?? 0n,
    toMicros: ctx?.toMicros ?? 0n,
    fromIso: ctx?.fromIso ?? 'belum dimuat',
    toIso: ctx?.toIso ?? 'belum dimuat'
  }
  retPostDenied.value = null
  retReplayError.value = null
  retSettleError.value = null
  focusFormAlert(retGen, staleRet)
}

// Batalkan konfirmasi yang BELUM dikirim: buang snapshot yang belum
// dipakai; konteks tidak berubah, jadi tidak perlu muat ulang.
function cancelHoldConfirmation() {
  if (holdBusy.value || holdUnknown.value || holdConflict.value !== null) return
  holdAttempt.value = null
  holdPostDenied.value = null
}

function cancelHandConfirmation() {
  if (handBusy.value || handUnknown.value || handConflict.value !== null) return
  handAttempt.value = null
  handPostDenied.value = null
}

function cancelRetConfirmation() {
  if (retBusy.value || retUnknown.value || retConflict.value !== null) return
  retAttempt.value = null
  retPostDenied.value = null
}

// Menebus snapshot secara eksplisit: pemulihan ditinggalkan, lalu baca
// ulang keadaan saat ini sebelum kunci baru dibuat. Tulis sebelumnya
// mungkin sudah tersimpan dan TIDAK dibatalkan oleh tindakan ini.
// Hanya dari konflik kunci definitif: state unknown TERKUNCI —
// retirement tidak boleh membuang snapshot yang hasilnya belum pasti.
// Terkunci selama POST berjalan: kelanjutan POST lama tidak boleh
// menghapus tahap baru atau mengumumkan simpan untuknya.
async function retireHoldAttempt() {
  if (holdBusy.value || holdUnknown.value) return
  clearHoldStage()
  holdDeadNote.value = null
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function retireHandAttempt() {
  if (handBusy.value || handUnknown.value) return
  clearHandStage()
  resetDraftLookup()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function retireRetAttempt() {
  if (retBusy.value || retUnknown.value) return
  clearRetStage()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function submitHold(resend = false) {
  const s = session
  if (!s || holdInFlight) return
  if ((holdUnknown.value || holdConflict.value !== null) && !resend) return
  if (resend && holdConflict.value !== null) return
  const a = holdAttempt.value
  if (!a) return
  if (!resend) {
    // Kiriman pertama: bacaan wajib selesai dengan keadaan saat ini
    // (fakta ada, belum ada custody, konteks cocok). Bacaan
    // tertunda/gagal bukan tanpa-fakta. Kirim-ulang adalah pemulihan
    // permintaan yang sudah dikirim, bukan kiriman baru, jadi tidak
    // diblokir oleh bacaan sesudahnya yang gagal/berubah.
    if (factLoading.value || custodyLoading.value || refreshing.value) return
    if (!fact.value || !custodyMissing.value) return
    if (!contextOk.value) return
  }
  const gen = holdGen
  const myPost = ++holdLoadGen
  holdReplayError.value = null
  holdSettleError.value = null
  if (!resend) holdPostDenied.value = null
  holdInFlight = true
  holdBusy.value = true
  try {
    const { status, data } = await apiFetch<HoldRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/holds`, {
      method: 'POST',
      body: { asset_id: a.assetId },
      idempotencyKey: a.key
    })
    if (staleHold(gen)) return
    // 201, atau replay 200 dengan hold yang masih hidup, mengonfirmasi.
    // Replay 200 dengan hold terminal adalah penyelesaian yang diketahui
    // (hold mati), bukan sukses dan bukan ketidakpastian.
    if ((status === 201 || status === 200) && isValidHoldForAttempt(data, a.workspaceId, a)) {
      if (data.status === 'held') {
        await confirmHold(data, gen)
        return
      }
      clearHoldStage()
      holdDeadNote.value = 'Hold ini sudah tidak aktif dan tidak dapat dipakai. Buat hold baru bila masih perlu.'
      return
    }
    setHoldUnknown(gen)
  } catch (err) {
    if (staleHold(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setHoldKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 422)) {
      if (!resend) {
        // Kiriman pertama: penolakan pra-sisip — tulis tidak terjadi.
        // Snapshot dipertahankan + panel tetap terbuka; kirim-ulang
        // kunci-sama tetap tersedia sesudah penyebabnya diperbaiki.
        holdPostDenied.value = err.message
        await nextTick()
        if (!staleHold(gen)) formAlert.value?.focus()
        return
      }
      // Penolakan kirim-ulang yang tegas: TIDAK membuktikan tulis awal
      // gagal — pemeriksaan izin server mendahului replay — jadi
      // snapshot dan ketidakpastian dipertahankan.
      holdReplayError.value = err.message
      holdUnknown.value = true
      await nextTick()
      if (!staleHold(gen)) formAlert.value?.focus()
      return
    }
    // 500/502/jaringan: hasil tetap tak diketahui, snapshot dipertahankan.
    setHoldUnknown(gen)
  } finally {
    if (myPost === holdLoadGen) {
      holdInFlight = false
      holdBusy.value = false
    }
  }
}

async function resendHold() {
  await submitHold(true)
}

// Tandai hold sebagai tidak aktif definitif (409 handover): berhenti
// ditawarkan, tampilkan alasan server, tutup attempt. Kunci baru boleh
// dibuat sesudahnya — hold pengganti + handover berikutnya butuh
// konfirmasi pengguna.
function markHoldInactive(holdId: string, message: string) {
  if (!deadHoldIds.value.includes(holdId)) deadHoldIds.value.push(holdId)
  clearHandStage()
  handInactiveNote.value = `Serah terima ditolak: ${message}. Hold lama tidak dapat dipakai lagi; buat hold baru bila masih perlu.`
}

async function submitHand(resend = false) {
  const s = session
  if (!s || handInFlight) return
  if ((handUnknown.value || handConflict.value !== null) && !resend) return
  if (resend && handConflict.value !== null) return
  const a = handAttempt.value
  if (!a) return
  if (!resend) {
    if (factLoading.value || custodyLoading.value || refreshing.value) return
    if (!fact.value || !custodyMissing.value) return
    if (!currentHold.value || currentHold.value.id !== a.holdId) return
    if (!contextOk.value) return
  }
  const gen = handGen
  const myPost = ++handLoadGen
  handReplayError.value = null
  handSettleError.value = null
  if (!resend) handPostDenied.value = null
  handInFlight = true
  handBusy.value = true
  try {
    const { status, data } = await apiFetch<CustodyRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/handover`, {
      method: 'POST',
      body: { asset_id: a.assetId, hold_id: a.holdId },
      idempotencyKey: a.key
    })
    if (staleHand(gen)) return
    // Replay 200 atau 201 dengan custody yang valid mengonfirmasi tulis
    // yang sudah terkomit — termasuk custody yang sudah terminal karena
    // unit sudah dikembalikan; 2xx lain atau catatan cacat tetap tak
    // diketahui.
    if ((status === 200 || status === 201) && isValidCustodyForHandoverAttempt(data, a.workspaceId, a)) {
      await confirmHand(data, gen)
      return
    }
    setHandUnknown(gen)
  } catch (err) {
    if (staleHand(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setHandKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === CUSTODY_EXISTS) {
      await recoverCustody('hand')
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === HOLD_INACTIVE) {
      // Penolakan definitif: server memeriksa replay dulu, lalu menolak
      // hold tidak aktif sebelum mutasi — tulis ini tidak terjadi.
      if (!resend) {
        markHoldInactive(a.holdId, err.message)
        handGen++
        await refreshHoldsBounded()
        await nextTick()
        custodyHeading.value?.focus()
        return
      }
      // Kirim-ulang sesudah unknown: 409 membuktikan replay tidak cocok,
      // tetapi kunci hanya dilepas sesudah bacaan otoritatif mengonfirmasi
      // tidak ada custody dengan kunci ini. Absensi dari daftar atau GET
      // custody 404 saja tidak cukup.
      handReplayError.value = err.message
      if (!refreshing.value) {
        await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
        if (staleHand(gen)) return
        if (!handAttempt.value) return
        if (custodyMissing.value) {
          markHoldInactive(a.holdId, err.message)
          handGen++
          await nextTick()
          custodyHeading.value?.focus()
          return
        }
      }
      // Bacaan gagal/cacat, atau muat-ulang sedang berjalan:
      // ketidakpastian dipertahankan; kirim-ulang tetap tersedia.
      handUnknown.value = true
      await nextTick()
      if (!staleHand(gen)) formAlert.value?.focus()
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 422)) {
      if (!resend) {
        handPostDenied.value = err.message
        await nextTick()
        if (!staleHand(gen)) formAlert.value?.focus()
        return
      }
      handReplayError.value = err.message
      handUnknown.value = true
      await nextTick()
      if (!staleHand(gen)) formAlert.value?.focus()
      return
    }
    setHandUnknown(gen)
  } finally {
    if (myPost === handLoadGen) {
      handInFlight = false
      handBusy.value = false
    }
  }
}

async function resendHand() {
  await submitHand(true)
}

async function submitRet(resend = false) {
  const s = session
  if (!s || retInFlight) return
  if ((retUnknown.value || retConflict.value !== null) && !resend) return
  if (resend && retConflict.value !== null) return
  const a = retAttempt.value
  if (!a) return
  if (!resend) {
    // Kiriman pertama: hanya gerbang pengembalian yang berlaku — refresh
    // tidak sedang berjalan dan custody masih `out`. Sengaja TIDAK
    // mensyaratkan bacaan fakta/aset/holds (guard backend menyempit).
    if (refreshing.value || custodyLoading.value) return
    if (!custody.value || custody.value.status !== 'out') return
  }
  const gen = retGen
  const myPost = ++retLoadGen
  retReplayError.value = null
  retSettleError.value = null
  if (!resend) retPostDenied.value = null
  retInFlight = true
  retBusy.value = true
  try {
    // Sukses return HANYA HTTP 200 dengan DTO yang valid; 201 atau
    // status lain bukan bukti tercatat.
    const { status, data } = await apiFetch<CustodyRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/return`, {
      method: 'POST',
      body: { outcome: a.outcome },
      idempotencyKey: a.key
    })
    if (staleRet(gen)) return
    if (status === 200 && isValidCustodyForReturnAttempt(data, a.workspaceId, a)) {
      await confirmReturn(data, gen)
      return
    }
    setRetUnknown(gen)
  } catch (err) {
    if (staleRet(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setRetKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message.startsWith('draft ini sudah ')) {
      await recoverCustody('ret')
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 422)) {
      if (!resend) {
        retPostDenied.value = err.message
        await nextTick()
        if (!staleRet(gen)) formAlert.value?.focus()
        return
      }
      retReplayError.value = err.message
      retUnknown.value = true
      await nextTick()
      if (!staleRet(gen)) formAlert.value?.focus()
      return
    }
    setRetUnknown(gen)
  } finally {
    if (myPost === retLoadGen) {
      retInFlight = false
      retBusy.value = false
    }
  }
}

async function resendRet() {
  await submitRet(true)
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Serah terima</h1>
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
          Serah terima di sini hanya mencatat unit mana yang dipinjam dan kapan kembali untuk draf ini. Ini bukan
          persetujuan pelanggan, bukan perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
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
          <p v-else class="hint">Draf dicari dalam daftar sesudah harga dibaca.</p>
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

        <section class="card" aria-labelledby="fact-heading">
          <h2 id="fact-heading">Harga yang disepakati</h2>
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
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang data.')">Coba lagi</button></p>
          </div>
        </section>

        <section class="card" aria-labelledby="custody-heading">
          <h2 id="custody-heading" ref="custodyHeading" tabindex="-1">Serah terima</h2>
          <p>
            <button type="button" class="btn btn-secondary" :disabled="refreshing || loading" @click="refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')">Muat ulang data</button>
          </p>
          <p v-if="custodyLoading" role="status">Memuat serah terima…</p>
          <div v-else-if="custody">
            <p v-if="custody.status === 'out'">
              <strong>Unit sedang dipinjam</strong><br>
              Unit: {{ custodyAssetLabel }}<br>
              Diserahkan: {{ custody.handed_out_at }} (UTC)
            </p>
            <p v-else-if="custody.status === 'returned'">
              <strong>Sudah dikembalikan</strong><br>
              Unit: {{ custodyAssetLabel }}<br>
              Diserahkan: {{ custody.handed_out_at }} (UTC)<br>
              Dikembalikan: {{ custody.returned_at }} (UTC)
            </p>
            <p v-else-if="custody.status === 'damaged'">
              <strong>Dikembalikan rusak</strong><br>
              Unit: {{ custodyAssetLabel }}<br>
              Diserahkan: {{ custody.handed_out_at }} (UTC)<br>
              Dikembalikan: {{ custody.returned_at }} (UTC)
            </p>
            <p v-else-if="custody.status === 'lost'">
              <strong>Dinyatakan hilang</strong><br>
              Unit: {{ custodyAssetLabel }}<br>
              Diserahkan: {{ custody.handed_out_at }} (UTC)<br>
              Dinyatakan hilang: {{ custody.resolved_at }} (UTC)
            </p>
            <p class="hint">
              Catatan ini hanya mencatat serah terima unit untuk draf ini. Ini bukan persetujuan pelanggan, bukan
              perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
            </p>
          </div>
          <p v-else-if="custodyMissing">Belum ada serah terima untuk draf ini.</p>
          <div v-else-if="custodyError" class="alert" role="alert">
            <p>{{ custodyError }}</p>
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')">Coba lagi</button></p>
          </div>
          <div v-if="handRecoveryError" class="alert" role="alert">
            <p>{{ handRecoveryError }}</p>
          </div>
          <div v-if="retRecoveryError" class="alert" role="alert">
            <p>{{ retRecoveryError }}</p>
          </div>

          <template v-if="!custody">
            <p v-if="holdHint" class="hint">{{ holdHint }}</p>
            <p>
              <label for="custody-asset">Unit</label>
              <select id="custody-asset" v-model="selectedAssetId" :disabled="holdBusy || handBusy || !!holdAttempt || !!handAttempt">
                <option value="" disabled>Pilih unit</option>
                <option v-for="a in filteredAssets" :key="a.id" :value="a.id">{{ a.serial }}</option>
              </select>
            </p>
            <p v-if="assetLoadError" class="alert" role="alert">
              {{ assetLoadError }}
              <button type="button" class="btn btn-secondary" @click="loadAssetPage">Coba lagi</button>
            </p>
            <p v-if="showAssetMore">
              <button type="button" class="btn btn-secondary" :disabled="assetLoading" @click="loadAssetPage">
                {{ assetLoading ? 'Memuat…' : 'Muat lebih banyak unit' }}
              </button>
            </p>
            <p v-if="currentHold">Hold aktif untuk unit ini.</p>
            <p v-if="holdDeadNote" class="hint">{{ holdDeadNote }}</p>
            <p v-if="holdLoadError" class="alert" role="alert">
              {{ holdLoadError }}
              <button type="button" class="btn btn-secondary" @click="loadHoldsPage">Coba lagi</button>
            </p>
            <p v-if="showHoldsMore">
              <button type="button" class="btn btn-secondary" :disabled="holdListLoading" @click="loadHoldsPage">
                {{ holdListLoading ? 'Memuat…' : 'Muat lebih banyak hold' }}
              </button>
            </p>
            <p v-if="canHold && !holdAttempt">
              <button type="button" class="btn" :disabled="holdBusy" @click="openHoldConfirmation">Buat hold</button>
            </p>
            <div v-if="holdAttempt && !holdUnknown && holdConflict === null" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Konfirmasi pembuatan hold</h3>
              <p>
                {{ holdAttempt.customerLabel }} — {{ holdAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ holdAttempt.fromIso }}; berakhir {{ holdAttempt.toIso }} (UTC).
              </p>
              <p>Unit: {{ holdAttempt.assetLabel }}</p>
              <p>
                Hold menahan unit ini untuk draf ini selama 24 jam. Unit yang ditahan tidak dapat dipakai
                draf lain sebelum hold dipakai atau berakhir.
              </p>
              <p v-if="holdPostDenied" class="field-error">{{ holdPostDenied }}</p>
              <p>
                <button type="button" class="btn" :disabled="holdBusy || refreshing || !custodyMissing" @click="submitHold(false)">
                  {{ holdBusy ? 'Membuat…' : 'Buat hold' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="holdBusy" @click="cancelHoldConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="holdUnknown" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Hasil pembuatan hold tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="holdAttempt">
                Permintaan yang belum pasti: {{ holdAttempt.customerLabel }} — {{ holdAttempt.itemLabel }}. Unit:
                {{ holdAttempt.assetLabel }}. Periode yang dicatat: mulai {{ holdAttempt.fromIso }}; berakhir {{ holdAttempt.toIso }} (UTC).
              </p>
              <p v-if="holdReplayError" class="field-error">Kirim ulang ditolak: {{ holdReplayError }}</p>
              <p v-if="holdSettleError" class="field-error">{{ holdSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="holdBusy" @click="resendHold">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="holdConflict === 'key'" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="holdAttempt">
                Permintaan yang belum pasti: {{ holdAttempt.customerLabel }} — {{ holdAttempt.itemLabel }}. Unit:
                {{ holdAttempt.assetLabel }}. Periode yang dicatat: mulai {{ holdAttempt.fromIso }}; berakhir {{ holdAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="holdBusy" @click="retireHoldAttempt">Mulai hold baru</button>
                Mulai hold baru membatalkan pemulihan ini. Hold sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>
            <p v-if="canHandover && !handAttempt">
              <button type="button" class="btn" :disabled="handBusy" @click="openHandConfirmation">Serahkan unit</button>
            </p>
            <p v-if="handInactiveNote" class="hint">{{ handInactiveNote }}</p>
            <div v-if="handAttempt && !handUnknown && handConflict === null" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Konfirmasi serah terima</h3>
              <p>
                {{ handAttempt.customerLabel }} — {{ handAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ handAttempt.fromIso }}; berakhir {{ handAttempt.toIso }} (UTC).
              </p>
              <p>Unit: {{ handAttempt.assetLabel }}</p>
              <p>
                Serah terima mencatat unit ini sedang dipinjam untuk draf ini. Pastikan unit sudah
                berada di tangan pelanggan sebelum mencatat.
              </p>
              <p v-if="handPostDenied" class="field-error">{{ handPostDenied }}</p>
              <p>
                <button type="button" class="btn" :disabled="handBusy || refreshing || !custodyMissing" @click="submitHand(false)">
                  {{ handBusy ? 'Menyerahkan…' : 'Serahkan unit' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="handBusy" @click="cancelHandConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="handUnknown" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Hasil serah terima tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="handAttempt">
                Permintaan yang belum pasti: {{ handAttempt.customerLabel }} — {{ handAttempt.itemLabel }}. Unit:
                {{ handAttempt.assetLabel }}. Periode yang dicatat: mulai {{ handAttempt.fromIso }}; berakhir {{ handAttempt.toIso }} (UTC).
              </p>
              <p v-if="handReplayError" class="field-error">Kirim ulang ditolak: {{ handReplayError }}</p>
              <p v-if="handSettleError" class="field-error">{{ handSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="handBusy" @click="resendHand">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="handConflict === 'key'" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="handAttempt">
                Permintaan yang belum pasti: {{ handAttempt.customerLabel }} — {{ handAttempt.itemLabel }}. Unit:
                {{ handAttempt.assetLabel }}. Periode yang dicatat: mulai {{ handAttempt.fromIso }}; berakhir {{ handAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="handBusy" @click="retireHandAttempt">Mulai serah terima baru</button>
                Mulai serah terima baru membatalkan pemulihan ini. Serah terima sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>
          </template>

          <template v-if="custody && custody.status === 'out'">
            <fieldset>
              <legend>Hasil pengembalian</legend>
              <p>
                <label><input type="radio" v-model="returnOutcome" value="returned" :disabled="!!retAttempt || retBusy"> Utuh</label>
              </p>
              <p>
                <label><input type="radio" v-model="returnOutcome" value="damaged" :disabled="!!retAttempt || retBusy"> Rusak</label>
              </p>
              <p>
                <label><input type="radio" v-model="returnOutcome" value="lost" :disabled="!!retAttempt || retBusy"> Hilang</label>
              </p>
            </fieldset>
            <p v-if="retFormVisible">
              <button type="button" class="btn" :disabled="!canReturn" @click="openRetConfirmation">Kembalikan unit</button>
            </p>
            <div v-if="retAttempt && !retUnknown && retConflict === null" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Konfirmasi pengembalian</h3>
              <p>
                {{ retAttempt.customerLabel }} — {{ retAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ retAttempt.fromIso }}; berakhir {{ retAttempt.toIso }} (UTC).
              </p>
              <p>Unit: {{ retAttempt.assetLabel }}</p>
              <p>Hasil: {{ retAttempt.outcome === 'returned' ? 'Utuh' : retAttempt.outcome === 'damaged' ? 'Rusak' : 'Hilang' }}</p>
              <p>
                Pengembalian mencatat hasil pemeriksaan unit untuk draf ini. Unit yang dikembalikan
                rusak atau hilang tidak dapat dipinjam lagi sebelum diperbaiki atau diganti.
              </p>
              <p v-if="retPostDenied" class="field-error">{{ retPostDenied }}</p>
              <p>
                <button type="button" class="btn" :disabled="retBusy || refreshing" @click="submitRet(false)">
                  {{ retBusy ? 'Mengembalikan…' : 'Kembalikan unit' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="retBusy" @click="cancelRetConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="retUnknown" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Hasil pengembalian tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="retAttempt">
                Permintaan yang belum pasti: {{ retAttempt.customerLabel }} — {{ retAttempt.itemLabel }}. Unit:
                {{ retAttempt.assetLabel }}. Hasil: {{ retAttempt.outcome === 'returned' ? 'Utuh' : retAttempt.outcome === 'damaged' ? 'Rusak' : 'Hilang' }}.
                Periode yang dicatat: mulai {{ retAttempt.fromIso }}; berakhir {{ retAttempt.toIso }} (UTC).
              </p>
              <p v-if="retReplayError" class="field-error">Kirim ulang ditolak: {{ retReplayError }}</p>
              <p v-if="retSettleError" class="field-error">{{ retSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="retBusy" @click="resendRet">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="retConflict === 'key'" class="alert" role="alert">
              <h3 ref="formAlert" tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="retAttempt">
                Permintaan yang belum pasti: {{ retAttempt.customerLabel }} — {{ retAttempt.itemLabel }}. Unit:
                {{ retAttempt.assetLabel }}. Hasil: {{ retAttempt.outcome === 'returned' ? 'Utuh' : retAttempt.outcome === 'damaged' ? 'Rusak' : 'Hilang' }}.
                Periode yang dicatat: mulai {{ retAttempt.fromIso }}; berakhir {{ retAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="retBusy" @click="retireRetAttempt">Mulai pengembalian baru</button>
                Mulai pengembalian baru membatalkan pemulihan ini. Pengembalian sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>
          </template>
        </section>
      </template>
    </template>
  </main>
</template>
