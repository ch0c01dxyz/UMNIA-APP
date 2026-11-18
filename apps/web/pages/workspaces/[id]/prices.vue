<script setup lang="ts">
// Tarif efektif workspace (GET/POST /workspaces/{id}/catalog/items/{item_id}/prices
// + GET katalog). LIST+CREATE saja; tanpa edit/hapus/quote/stok. Server
// otoritatif: baca riwayat butuh view_workspace, jadwal baru butuh
// manage_business_data; kasir melihat formulir dan menerima 403 saat
// simpan (bukan disembunyikan). Satu interval [valid_from, valid_to),
// price_list 'standard', tanpa backdate, tanpa tumpang tindih.
//
// SATU percobaan logis, SATU kunci: kunci dibuat dengan
// crypto.randomUUID() sesudah input valid dan sebelum POST pertama, lalu
// dibekukan bersama muatan ternormalisasi (workspace, barang, waktu UTC,
// jumlah, mata uang). Hanya 201 atau replay 200 dengan baris yang valid
// yang mengonfirmasi simpan. Hasil tak diketahui mengunci field/picker
// dan hanya menawarkan kirim-ulang kunci-sama atau "Mulai jadwal baru"
// yang menebus snapshot; tidak ada ulang otomatis atau kunci baru buta.
// Pemulihan hanya hidup di memori halaman ini.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Tarif — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface PriceRow { id: string, workspace_id: string, catalog_item_id: string, price_list_id: string, client_request_id: string, valid_from: string, valid_to: string, amount_minor: string, currency: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }

const PAGE_LIMIT = 50
const CATALOG_PAGE = 100
const MAX_AMOUNT = 9223372036854775807n
const ITEM_INACTIVE = 'barang tidak aktif'
const KEY_MISMATCH = 'kunci idempotency sudah dipakai untuk permintaan yang berbeda'
const OVERLAP = 'interval harga bertabrakan'

// Waktu masuk: datetime-local "YYYY-MM-DDTHH:mm" ditafsir sebagai UTC.
// Konstruktor Date(y, m, d) dan Date.UTC(y, ...) memperlakukan tahun
// 0-99 sebagai 1900+ (jebakan klasik); selalu bangun dari epoch lalu
// setUTCFullYear, dan verifikasi pulang-pergi (round-trip) supaya tanggal
// kalender yang tidak nyata (mis. 30 Feb) ditolak, bukan digeser.
const DATETIME_LOCAL_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2})$/
function parseDateTimeLocal(s: string): { micros: bigint, iso: string } | null {
  const m = DATETIME_LOCAL_RE.exec(s)
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2])
  const d = Number(m[3])
  const h = Number(m[4])
  const mi = Number(m[5])
  if (y < 1 || y > 9999 || mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59) return null
  const dt = new Date(0)
  dt.setUTCFullYear(y, mo - 1, d)
  dt.setUTCHours(h, mi, 0, 0)
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d ||
    dt.getUTCHours() !== h || dt.getUTCMinutes() !== mi) return null
  const iso = `${m[1]}-${m[2]}-${m[3]}T${m[4]}:${m[5]}:00Z`
  return { micros: BigInt(dt.getTime()) * 1000n, iso }
}

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

// Jumlah: string kanonis "0" atau [1-9][0-9]*, tanpa spasi/tanda/pecahan/
// eksponen, paling besar MaxInt64. BigInt menjaga ketepatan di atas 2^53.
function amountProblem(s: string): 'empty' | 'format' | 'range' | null {
  if (s === '') return 'empty'
  if (!/^(0|[1-9][0-9]*)$/.test(s)) return 'format'
  if (BigInt(s) > MAX_AMOUNT) return 'range'
  return null
}

interface Attempt {
  key: string
  workspaceId: string
  itemId: string
  itemLabel: string
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
  amount: string
  currency: string
}

// Baris tersimpan minimum: id ada, workspace + barang cocok (kanonis
// huruf kecil), price_list 'standard', client_request_id sama dengan
// kunci yang dibekukan, jumlah/mata uang sama persis, dan kedua instan
// setara dengan yang dibekukan (banding mikrodetik, bukan string).
function isValidPriceRow(row: unknown, ws: string, a: Attempt): row is PriceRow {
  if (!row || typeof row !== 'object') return false
  const r = row as PriceRow
  if (typeof r.id !== 'string' || r.id === '') return false
  if (r.workspace_id !== ws) return false
  if (typeof r.catalog_item_id !== 'string' || r.catalog_item_id.toLowerCase() !== a.itemId.toLowerCase()) return false
  if (r.price_list_id !== 'standard') return false
  if (r.client_request_id !== a.key) return false
  if (r.amount_minor !== a.amount || r.currency !== a.currency) return false
  const from = typeof r.valid_from === 'string' ? parseServerInstant(r.valid_from) : null
  const to = typeof r.valid_to === 'string' ? parseServerInstant(r.valid_to) : null
  return from === a.fromMicros && to === a.toMicros
}

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const workspace = ref<Workspace | null>(null)
const items = ref<PriceRow[]>([])
const nextOffset = ref(0)
const hasMore = ref(false)
const loadingMore = ref(false)
const loadMoreError = ref<string | null>(null)
const reloadError = ref<string | null>(null)
// Transisi baca ditolak sesudah halaman termuat (403 pada GET daftar/
// katalog/refresh): daftar dan formulir yang sudah termuat disembunyikan
// dan kepemilikan yang tertunda dibatalkan (lihat denyRead).
const readDenied = ref(false)
// Riwayat hanya boleh menampilkan "Belum ada tarif." sesudah GET yang
// berhasil mengembalikan nol baris; refreshing menandai refresh yang
// sedang berjalan supaya jeda muat ulang tidak dibaca sebagai kosong.
const listLoaded = ref(false)
const refreshing = ref(false)

// Peta katalog: baris mentah (termasuk arsip) untuk label dan pilihan.
// Arsip DITAWARKAN untuk riwayat (label "(arsip — riwayat)"); jadwal baru
// untuk barang arsip dicegah di formulir dan ditolak server 409. Offset
// maju menurut jumlah mentah yang diterima, bukan jumlah render; render
// dedup berdasarkan id.
const catalogRows = ref<CatalogRow[]>([])
const catalogOffset = ref(0)
const catalogExhausted = ref(false)
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogStarted = ref(false)
const catalogById = computed(() => new Map(catalogRows.value.map(r => [r.id, r])))

const selectedItemId = ref('')
const itemNotFound = ref(false)
const itemArchived = ref(false)
const validFrom = ref('')
const validTo = ref('')
const amount = ref('')
const currency = ref('')
const itemError = ref<string | null>(null)
const fromError = ref<string | null>(null)
const toError = ref<string | null>(null)
const amountError = ref<string | null>(null)
const currencyError = ref<string | null>(null)
const formError = ref<string | null>(null)
const formDenied = ref<string | null>(null)
// Penolakan kirim-ulang (mis. 403 sesudah hasil tak diketahui): ditampilkan
// di dalam panel tak diketahui — snapshot + ketidakpastian dipertahankan,
// bukan formError terpisah (dua alert bersamaan menggandakan pesan).
const replayError = ref<string | null>(null)
const unknownOutcome = ref(false)
const keyMismatch = ref(false)
// Snapshot percobaan yang dibekukan (tidak pernah diubah; hanya diganti
// null saat ditebus/dikonfirmasi). Selama snapshot hidup, field/picker
// terkunci supaya identitas pemulihan tidak rusak diam-diam.
const attempt = ref<Attempt | null>(null)
// Selama POST berjalan atau hasil tak diketahui/kunci tercemar, kirim
// biasa (tombol, Enter, submit langsung) tidak boleh POST lagi.
const fieldsLocked = computed(() => busy.value || unknownOutcome.value || keyMismatch.value)
const submitBlocked = computed(() => unknownOutcome.value || keyMismatch.value)
const busy = ref(false)
const saved = ref<PriceRow | null>(null)
const savedHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)
const itemSelect = ref<HTMLSelectElement | null>(null)
const fromInput = ref<HTMLInputElement | null>(null)
const toInput = ref<HTMLInputElement | null>(null)
const amountInput = ref<HTMLInputElement | null>(null)
const currencyInput = ref<HTMLInputElement | null>(null)

const selectedRow = computed(() => selectedItemId.value ? catalogById.value.get(selectedItemId.value) ?? null : null)
const selectedArchived = computed(() => {
  const row = selectedRow.value
  return !!row && row.status !== 'active'
})
// Barang yang dipilih lalu diarsipkan di luar halaman (atau ditolak server
// 409 tidak aktif): jadwal baru dicegah; riwayat tetap dibaca dan
// pemulihan kunci-sama tetap tersedia.
const archivedSelected = computed(() => selectedArchived.value || itemArchived.value)

// Label barang: dari peta bila ada; bila peta belum lengkap, katakan
// jujur belum dimuat — tanpa menebak terhapus/arsip/tersedia.
function catalogLabel(catalogItemId: string): string {
  const row = catalogById.value.get(catalogItemId)
  if (row) return `${row.sku} — ${row.name}` + (row.status === 'active' ? '' : ' (arsip — riwayat)')
  return catalogExhausted.value ? 'Nama barang tidak tersedia' : 'Nama barang belum dimuat'
}
const savedLabel = computed(() => (saved.value ? catalogLabel(saved.value.catalog_item_id) : ''))

// Penjaga generasi: hasil muat yang tiba sesudah reset/pindah workspace/
// logout diabaikan; muat-lebih lama tidak boleh menempel ke prefix baru.
// inFlight/catalogInFlight dijaga sinkron (bukan hanya tombol disabled).
// postGen: kepemilikan POST — kelanjutan POST basi (sukses, galat,
// nextTick, refresh) tidak boleh menyentuh halaman/sesi/daftar pengganti.
// loadMoreGen/catalogLoadGen: kepemilikan flag busy — finally basi tidak
// boleh menghapus busy milik permintaan baru.
let listGen = 0
let postGen = 0
let loadMoreGen = 0
let catalogGen = 0
let catalogLoadGen = 0
let inFlight = false
let catalogInFlight = false

function stale(gen: number) {
  return gen !== listGen
}

function stalePost(gen: number) {
  return gen !== postGen
}

// Transisi baca ditolak (GET daftar/katalog/refresh 403 sesudah halaman
// termuat): beri umpan balik izin workspace, sembunyikan daftar dan
// formulir yang sudah termuat, dan batalkan kepemilikan daftar/katalog/
// POST yang tertunda supaya penyelesaian terlambat tidak mengisi ulang
// halaman pengganti. Sesi dipertahankan — 403 bukan 401. Berbeda dengan
// POST 403 (formDenied) yang mempertahankan isian/daftar/sesi pembaca.
function denyRead(message: string) {
  listGen++
  postGen++
  loadMoreGen++
  catalogGen++
  catalogLoadGen++
  loadingMore.value = false
  loadMoreError.value = null
  catalogInFlight = false
  catalogLoading.value = false
  inFlight = false
  busy.value = false
  items.value = []
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  nextOffset.value = 0
  hasMore.value = false
  selectedItemId.value = ''
  itemNotFound.value = false
  itemArchived.value = false
  validFrom.value = ''
  validTo.value = ''
  amount.value = ''
  currency.value = ''
  itemError.value = null
  fromError.value = null
  toError.value = null
  amountError.value = null
  currencyError.value = null
  formError.value = null
  formDenied.value = null
  replayError.value = null
  unknownOutcome.value = false
  keyMismatch.value = false
  attempt.value = null
  saved.value = null
  listLoaded.value = false
  refreshing.value = false
  // Penolakan saat ini menyelesaikan loader global: katalog 403 awal
  // memanggil denyRead yang menaikkan listGen, sehingga finalizer load()
  // yang lama tidak bisa lagi mematikan loading — tanpa baris ini
  // "Memuat…" bertahan selamanya di atas penolakan.
  loading.value = false
  readDenied.value = true
  pageError.value = message
}

onMounted(async () => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  await load()
})

watch(wsId, async () => {
  listGen++
  postGen++
  loadMoreGen++
  catalogGen++
  catalogLoadGen++
  loadingMore.value = false
  loadMoreError.value = null
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  catalogInFlight = false
  catalogStarted.value = false
  selectedItemId.value = ''
  itemNotFound.value = false
  itemArchived.value = false
  attempt.value = null
  unknownOutcome.value = false
  keyMismatch.value = false
  saved.value = null
  readDenied.value = false
  listLoaded.value = false
  refreshing.value = false
  await load()
})

onUnmounted(() => {
  listGen++
  postGen++
  loadMoreGen++
  catalogGen++
  catalogLoadGen++
})

async function load() {
  const s = session
  if (!s) return
  const gen = ++listGen
  loading.value = true
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  reloadError.value = null
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
  // Katalog adalah konten utama halaman ini (pemilih barang): status
  // memuat dipertahankan sampai GET awal selesai supaya jeda muat tidak
  // dibaca sebagai halaman kosong.
  if (!stale(gen) && !catalogStarted.value) {
    catalogStarted.value = true
    await loadCatalogPage()
  }
  if (!stale(gen)) loading.value = false
}

// Satu halaman katalog (100 baris). Tanpa rantai otomatis: pemanggil
// berikutnya hanyalah tombol "Muat lebih banyak barang" atau "Coba lagi"
// (yang memakai offset yang sama). Kegagalan mempertahankan baris, peta,
// pilihan, dan kursor yang sudah ada.
async function loadCatalogPage() {
  const s = session
  if (!s || catalogInFlight) return
  const gen = catalogGen
  const myLoad = ++catalogLoadGen
  const wantOffset = catalogOffset.value
  catalogInFlight = true
  catalogLoading.value = true
  catalogError.value = null
  try {
    const res = await apiFetch<{ items: CatalogRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items?limit=${CATALOG_PAGE}&offset=${wantOffset}`)
    if (gen !== catalogGen) return
    const seen = new Set(catalogRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        catalogRows.value.push(row)
        seen.add(row.id)
      }
    }
    // Kursor dari jumlah mentah yang diterima, bukan panjang render.
    catalogOffset.value = wantOffset + res.data.items.length
    catalogExhausted.value = res.data.items.length < CATALOG_PAGE
  } catch (err) {
    if (gen !== catalogGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      // Baca katalog ditolak sesudah muat: sembunyikan halaman, jangan
      // tampilkan galat katalog di atas daftar lama.
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) catalogError.value = err.message
    else catalogError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    // Hanya pemilik flag saat ini yang boleh mematikannya; finally basi
    // tidak boleh mematikan busy milik muat-katalog baru.
    if (myLoad === catalogLoadGen) {
      catalogInFlight = false
      catalogLoading.value = false
    }
  }
}

// Ganti barang: riwayat/kursor/hasil barang lama dibersihkan tanpa
// memuat ulang katalog; halaman pertama barang baru dimuat.
watch(selectedItemId, async (next, prev) => {
  if (next === prev) return
  listGen++
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  items.value = []
  nextOffset.value = 0
  hasMore.value = false
  listLoaded.value = false
  reloadError.value = null
  itemNotFound.value = false
  itemArchived.value = false
  saved.value = null
  itemError.value = null
  formError.value = null
  formDenied.value = null
  if (!next) return
  await loadPricesPage(listGen)
})

async function loadPricesPage(gen: number) {
  const s = session
  if (!s || !selectedItemId.value) return
  const itemId = selectedItemId.value
  // Ganti barang adalah pemuatan daftar: tampilkan "Memuat daftar…" selama
  // GET tertunda, bukan daftar kosong atau area kosong.
  refreshing.value = true
  try {
    const list = await apiFetch<{ items: PriceRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items/${itemId}/prices?limit=${PAGE_LIMIT}`)
    if (stale(gen)) return
    items.value = list.data.items
    nextOffset.value = list.data.items.length
    hasMore.value = list.data.items.length === PAGE_LIMIT
    listLoaded.value = true
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError && err.status === 404) {
      // Barang hilang/asing: bukan arsip — bersihkan form/daftar barang
      // lama dan tampilkan salinan tidak ditemukan.
      items.value = []
      listLoaded.value = false
      itemNotFound.value = true
      return
    }
    // Galat baca lain: tampil di bagian daftar dengan salinan ramah yang
    // sama seperti retry refresh (bukan pesan mentah server), dengan retry
    // GET-only, tanpa klaim kosong.
    reloadError.value = 'Daftar gagal dimuat ulang. Coba muat ulang daftar.'
  } finally {
    // Hanya pemilik pemuatan saat ini yang boleh mematikan status muat;
    // finally basi tidak boleh menghapus status milik pemuatan baru.
    if (gen === listGen) refreshing.value = false
  }
}

async function loadMore() {
  const s = session
  if (!s || loadingMore.value || !hasMore.value || !selectedItemId.value) return
  const gen = listGen
  const myLoad = ++loadMoreGen
  const wantOffset = nextOffset.value
  const itemId = selectedItemId.value
  loadingMore.value = true
  loadMoreError.value = null
  try {
    const res = await apiFetch<{ items: PriceRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items/${itemId}/prices?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
    if (stale(gen)) return
    const seen = new Set(items.value.map(i => i.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        items.value.push(row)
        seen.add(row.id)
      }
    }
    // Kursor dari jumlah mentah yang diterima, bukan panjang render.
    nextOffset.value = wantOffset + res.data.items.length
    hasMore.value = res.data.items.length === PAGE_LIMIT
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError && err.status === 404) {
      items.value = []
      listLoaded.value = false
      itemNotFound.value = true
      return
    }
    if (err instanceof ApiError) loadMoreError.value = err.message
    else loadMoreError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    // Hanya pemilik flag saat ini yang boleh mematikannya; finally basi
    // tidak boleh mematikan busy milik muat-lebih baru.
    if (myLoad === loadMoreGen) loadingMore.value = false
  }
}

// Muat ulang riwayat (GET saja): dipakai sesudah simpan 201/200 dan oleh
// tombol "Muat ulang daftar" saat hasil tak diketahui. Kegagalan
// menampilkan reloadError tanpa menyembunyikan konfirmasi tersimpan dan
// tanpa klaim riwayat kosong; katalog tidak ikut dimuat ulang.
async function refreshPrices(failureMessage: string) {
  const s = session
  if (!s || !selectedItemId.value) return
  const gen = ++listGen
  // Reset riwayat yang koheren: muat-lebih lama dibatalkan kepemilikannya
  // dan flag-nya dibersihkan supaya paginasi baru tetap bisa dipakai.
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  reloadError.value = null
  refreshing.value = true
  const itemId = selectedItemId.value
  try {
    const list = await apiFetch<{ items: PriceRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items/${itemId}/prices?limit=${PAGE_LIMIT}`)
    if (stale(gen)) return
    items.value = list.data.items
    nextOffset.value = list.data.items.length
    hasMore.value = list.data.items.length === PAGE_LIMIT
    listLoaded.value = true
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError && err.status === 404) {
      items.value = []
      listLoaded.value = false
      itemNotFound.value = true
      return
    }
    reloadError.value = failureMessage
  } finally {
    // Hanya pemilik refresh saat ini yang boleh mematikan status muat;
    // finally basi tidak boleh menghapus status milik refresh baru.
    if (gen === listGen) refreshing.value = false
  }
}

// Hasil tak diketahui (jaringan/timeout/500/502, 200/201 lain, atau baris
// yang cacat): snapshot dibekukan apa adanya, kirim biasa terkunci, tanpa
// ulang otomatis. Hanya kirim-ulang kunci-sama atau "Mulai jadwal baru"
// yang boleh melanjutkan.
function setUnknownOutcome(gen: number) {
  unknownOutcome.value = true
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Kunci tercemar (409 mismatch): kunci sudah terikat pada permintaan lain.
// Satu status mismatch yang koheren: panel tak diketahui disembunyikan
// (kirim-ulang tidak lagi ditawarkan), snapshot dipertahankan untuk
// ringkasan beku, kirim terkunci, dan hanya "Mulai jadwal baru" yang boleh
// menebus — tanpa kirim-ulang buta.
function setKeyMismatch(gen: number) {
  keyMismatch.value = true
  unknownOutcome.value = false
  // Pesan server tampil SATU kali di div mismatch di bawah (bukan juga di
  // formError): dua role="alert" bersamaan menggandakan pesan yang sama di
  // layar dan membuat pencocokan strict-mode gagal.
  formError.value = null
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Menebus snapshot secara eksplisit: pemulihan ditinggalkan, field
// dibuka lagi dengan nilai yang dipertahankan. Jadwal sebelumnya mungkin
// sudah tersimpan dan TIDAK dibatalkan oleh tindakan ini. Kunci baru
// hanya dibuat pada kiriman valid berikutnya. Terkunci selama POST berjalan:
// kelanjutan POST lama tidak boleh menghapus draf baru atau mengumumkan
// simpan untuknya.
function retireAttempt() {
  if (busy.value) return
  attempt.value = null
  unknownOutcome.value = false
  keyMismatch.value = false
  formError.value = null
  replayError.value = null
}

async function confirmSave(row: PriceRow, gen: number) {
  saved.value = row
  validFrom.value = ''
  validTo.value = ''
  amount.value = ''
  // Mata uang dipertahankan: jadwal berikut untuk barang yang sama
  // umumnya memakai mata uang yang sama; wilayah tersimpan menyebut
  // barangnya secara eksplisit.
  unknownOutcome.value = false
  keyMismatch.value = false
  attempt.value = null
  replayError.value = null
  items.value = []
  // Riwayat dikosongkan untuk dimuat ulang: tandai belum termuat supaya
  // jeda refresh tidak ditampilkan sebagai "Belum ada tarif.".
  listLoaded.value = false
  nextOffset.value = 0
  hasMore.value = false
  await nextTick()
  if (stalePost(gen)) return
  savedHeading.value?.focus()
  await refreshPrices('Data tersimpan, tetapi daftar gagal dimuat ulang. Coba muat ulang daftar.')
}

async function submitCreate(resend = false) {
  const s = session
  if (!s || inFlight) return
  if ((unknownOutcome.value || keyMismatch.value) && !resend) return
  // Kirim-ulang dari status mismatch dilarang: kunci sudah terikat pada
  // permintaan lain; hanya Mulai jadwal baru yang boleh melanjutkan.
  if (resend && keyMismatch.value) return
  const gen = postGen
  // Kirim-ulang kunci-sama: pakai snapshot yang dibekukan — bukan nilai
  // field saat ini (field terkunci, tetapi pertahanan ini yang menjamin
  // identitas pemulihan).
  if (resend) {
    const a = attempt.value
    if (!a) return
    replayError.value = null
    inFlight = true
    busy.value = true
    try {
      const { status, data } = await apiFetch<PriceRow>(s, `/api/workspaces/${a.workspaceId}/catalog/items/${a.itemId}/prices`, {
        method: 'POST',
        body: { valid_from: a.fromIso, valid_to: a.toIso, amount_minor: a.amount, currency: a.currency },
        idempotencyKey: a.key
      })
      if (stalePost(gen)) return
      // Replay 200 atau 201 dengan baris yang valid mengonfirmasi tulis
      // yang sudah terkomit; 2xx lain atau baris cacat tetap tak diketahui.
      if ((status === 200 || status === 201) && isValidPriceRow(data, a.workspaceId, a)) {
        await confirmSave(data, gen)
        return
      }
      if (status === 200 || status === 201) {
        setUnknownOutcome(gen)
        return
      }
      setUnknownOutcome(gen)
    } catch (err) {
      if (stalePost(gen)) return
      if (err instanceof ApiError && err.status === 401) {
        redirectToLogin(s, route.fullPath)
        return
      }
      // Kunci tercemar: satu status mismatch yang koheren (lihat
      // setKeyMismatch); snapshot dipertahankan, panel tak diketahui hilang.
      if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
        setKeyMismatch(gen)
        return
      }
      // Penolakan kirim-ulang yang tegas (400/403/404/409): TIDAK membuktikan
      // tulis awal gagal — pemeriksaan izin server mendahului replay — jadi
      // snapshot dan ketidakpastian dipertahankan; penolakan tampil di dalam
      // panel tak diketahui dan kirim biasa tetap terkunci.
      if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 409)) {
        if (err.status === 409 && err.message === ITEM_INACTIVE) itemArchived.value = true
        replayError.value = err.message
        unknownOutcome.value = true
        await nextTick()
        if (!stalePost(gen)) formAlert.value?.focus()
        return
      }
      // 500/502/jaringan: hasil tetap tak diketahui, snapshot dipertahankan.
      setUnknownOutcome(gen)
    } finally {
      inFlight = false
      busy.value = false
    }
    return
  }
  const itemId = selectedItemId.value
  const fromRaw = validFrom.value
  const toRaw = validTo.value
  const amountRaw = amount.value
  const currencyRaw = currency.value
  itemError.value = null
  fromError.value = null
  toError.value = null
  amountError.value = null
  currencyError.value = null
  formError.value = null
  formDenied.value = null
  let firstBad: HTMLElement | null = null
  if (itemId === '') {
    itemError.value = 'Pilih barang dulu'
    firstBad = itemSelect.value
  } else if (archivedSelected.value) {
    itemError.value = ITEM_INACTIVE
    if (!firstBad) firstBad = itemSelect.value
  }
  const fromParsed = parseDateTimeLocal(fromRaw)
  if (!fromParsed) {
    fromError.value = fromRaw === '' ? 'Tanggal mulai wajib diisi' : 'Tanggal mulai (UTC) tidak valid — gunakan tanggal dan jam menit yang benar'
    if (!firstBad) firstBad = fromInput.value
  }
  const toParsed = parseDateTimeLocal(toRaw)
  if (!toParsed) {
    toError.value = toRaw === '' ? 'Tanggal berakhir wajib diisi' : 'Tanggal berakhir (UTC) tidak valid — gunakan tanggal dan jam menit yang benar'
    if (!firstBad) firstBad = toInput.value
  } else if (fromParsed && toParsed.micros <= fromParsed.micros) {
    toError.value = 'Tanggal berakhir harus sesudah tanggal mulai'
    if (!firstBad) firstBad = toInput.value
  }
  const amountBad = amountProblem(amountRaw)
  if (amountBad) {
    amountError.value = amountBad === 'empty'
      ? 'Nilai tarif wajib diisi'
      : amountBad === 'range'
        ? 'Nilai tarif paling besar 9223372036854775807'
        : 'Nilai tarif harus bilangan bulat tanpa spasi, tanda, atau pecahan'
    if (!firstBad) firstBad = amountInput.value
  }
  if (currencyRaw === '') {
    currencyError.value = 'Mata uang wajib diisi'
    if (!firstBad) firstBad = currencyInput.value
  } else if (!/^[A-Z]{3}$/.test(currencyRaw)) {
    currencyError.value = 'Mata uang harus 3 huruf kapital (A-Z)'
    if (!firstBad) firstBad = currencyInput.value
  }
  if (firstBad) {
    await nextTick()
    firstBad.focus()
    return
  }
  // Kunci dibuat SESUDAH input valid dan SEBELUM POST pertama. Bila
  // pembuatan aman tidak tersedia, kirim diblokir — tanpa fallback lemah.
  if (typeof crypto === 'undefined' || typeof crypto.randomUUID !== 'function') {
    formError.value = 'Pembuatan kunci idempotency tidak tersedia di browser ini. Coba lagi di browser yang mendukung crypto.randomUUID.'
    await nextTick()
    if (!stalePost(gen)) formAlert.value?.focus()
    return
  }
  const snapshot: Attempt = {
    key: crypto.randomUUID(),
    workspaceId: wsId.value,
    itemId,
    itemLabel: catalogLabel(itemId),
    fromMicros: fromParsed!.micros,
    toMicros: toParsed!.micros,
    fromIso: fromParsed!.iso,
    toIso: toParsed!.iso,
    amount: amountRaw,
    currency: currencyRaw
  }
  attempt.value = snapshot
  inFlight = true
  busy.value = true
  try {
    const { status, data } = await apiFetch<PriceRow>(s, `/api/workspaces/${wsId.value}/catalog/items/${itemId}/prices`, {
      method: 'POST',
      body: { valid_from: snapshot.fromIso, valid_to: snapshot.toIso, amount_minor: snapshot.amount, currency: snapshot.currency },
      idempotencyKey: snapshot.key
    })
    if (stalePost(gen)) return
    // Hanya 201 (atau replay 200) dengan baris yang valid yang
    // mengonfirmasi simpan.
    if ((status === 201 || status === 200) && isValidPriceRow(data, wsId.value, snapshot)) {
      await confirmSave(data, gen)
      return
    }
    if (status === 200 || status === 201) {
      setUnknownOutcome(gen)
      return
    }
    setUnknownOutcome(gen)
  } catch (err) {
    if (stalePost(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    // Kegagalan yang diketahui: kunci tidak pernah terkomit, snapshot
    // dibersihkan, isian dipertahankan untuk koreksi — kecuali mismatch
    // yang mengunci snapshot sampai ditebus eksplisit.
    if (err instanceof ApiError && err.status === 400) {
      attempt.value = null
      const msg = err.message
      if (msg.includes('valid_from')) {
        fromError.value = msg
        await nextTick()
        if (!stalePost(gen)) fromInput.value?.focus()
      } else if (msg.includes('valid_to')) {
        toError.value = msg
        await nextTick()
        if (!stalePost(gen)) toInput.value?.focus()
      } else if (msg.includes('amount_minor')) {
        amountError.value = msg
        await nextTick()
        if (!stalePost(gen)) amountInput.value?.focus()
      } else if (msg.includes('currency')) {
        currencyError.value = msg
        await nextTick()
        if (!stalePost(gen)) currencyInput.value?.focus()
      } else {
        formError.value = msg
        await nextTick()
        if (!stalePost(gen)) formAlert.value?.focus()
      }
      return
    }
    if (err instanceof ApiError && err.status === 404) {
      attempt.value = null
      formError.value = err.message
      await nextTick()
      if (!stalePost(gen)) formAlert.value?.focus()
      return
    }
    if (err instanceof ApiError && err.status === 409) {
      if (err.message === KEY_MISMATCH) {
        setKeyMismatch(gen)
        return
      }
      attempt.value = null
      if (err.message === OVERLAP) {
        formError.value = err.message
        await nextTick()
        if (!stalePost(gen)) formAlert.value?.focus()
      } else if (err.message === ITEM_INACTIVE) {
        itemArchived.value = true
        itemError.value = err.message
        await nextTick()
        if (!stalePost(gen)) itemSelect.value?.focus()
      } else {
        formError.value = err.message
        await nextTick()
        if (!stalePost(gen)) formAlert.value?.focus()
      }
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      // Hasil izin formulir: data/daftar tetap, sesi tidak dibersihkan.
      attempt.value = null
      formDenied.value = err.message
      await nextTick()
      if (!stalePost(gen)) formAlert.value?.focus()
      return
    }
    // 500/502/jaringan: hasil tidak diketahui, mungkin sudah tersimpan.
    setUnknownOutcome(gen)
  } finally {
    inFlight = false
    busy.value = false
  }
}

async function resendCreate() {
  await submitCreate(true)
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Tarif</h1>
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

        <section class="card" aria-labelledby="add-heading">
          <h2 id="add-heading">Jadwalkan tarif</h2>
          <p class="small muted">
            Penjadwalan memerlukan izin pengelolaan; izin diperiksa saat menyimpan.
          </p>
          <div v-if="formDenied" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formDenied }}</p>
          </div>
          <div v-else-if="formError" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formError }}</p>
          </div>
          <div v-if="keyMismatch" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <h3>Kunci idempotency sudah dipakai</h3>
            <p>
              {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
              tidak dapat dikirim ulang dengan kunci yang sama. Buat jadwal baru untuk
              melanjutkan dengan kunci baru.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.itemLabel }} — {{ attempt.fromIso }}
              sampai sebelum {{ attempt.toIso }} — {{ attempt.amount }} unit tarif · {{ attempt.currency }}.
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai jadwal baru</button>
            </p>
          </div>
          <div v-if="unknownOutcome" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <h3>Hasil penyimpanan tidak diketahui</h3>
            <p>
              Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
              sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
              hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
              akan menghilangkannya.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.itemLabel }} — {{ attempt.fromIso }}
              sampai sebelum {{ attempt.toIso }} — {{ attempt.amount }} unit tarif · {{ attempt.currency }}.
            </p>
            <p v-if="replayError" class="field-error">Kirim ulang ditolak: {{ replayError }}</p>
            <p>
              <button type="button" class="btn btn-secondary" @click="refreshPrices('Daftar gagal dimuat ulang. Coba muat ulang daftar.')">Muat ulang daftar</button>
              <button type="button" class="btn btn-secondary" @click="resendCreate">Kirim lagi (permintaan sama)</button>
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai jadwal baru</button>
              Mulai jadwal baru membatalkan pemulihan ini. Jadwal sebelumnya mungkin sudah
              tersimpan dan tidak dibatalkan oleh tindakan ini.
            </p>
          </div>
          <form @submit.prevent="submitCreate()" novalidate>
            <div class="field">
              <label for="price-item">Barang</label>
              <select
                id="price-item"
                ref="itemSelect"
                v-model="selectedItemId"
                :disabled="fieldsLocked || catalogRows.length === 0"
                :aria-invalid="itemError !== null"
                :aria-describedby="itemError ? 'price-item-error' : undefined"
              >
                <option value="">Pilih barang</option>
                <option v-for="item in catalogRows" :key="item.id" :value="item.id">{{ item.sku }} — {{ item.name }}{{ item.status === 'active' ? '' : ' (arsip — riwayat)' }}</option>
              </select>
              <p v-if="itemError" id="price-item-error" class="field-error">{{ itemError }}</p>
              <p v-else class="hint">Barang dipilih dari katalog workspace; barang arsip hanya untuk melihat riwayat.</p>
              <p v-if="catalogLoading && catalogRows.length === 0" role="status">Memuat barang…</p>
              <div v-else-if="catalogError && catalogRows.length === 0" class="alert" role="alert">
                <p>{{ catalogError }}</p>
                <p><button type="button" class="btn btn-secondary" @click="loadCatalogPage">Coba lagi</button></p>
              </div>
              <template v-else>
                <p v-if="catalogError" class="alert" role="alert">
                  {{ catalogError }}
                  <button type="button" class="btn btn-secondary" @click="loadCatalogPage">Coba lagi</button>
                </p>
                <p v-if="catalogExhausted && catalogRows.length === 0" class="hint">
                  Belum ada barang.
                  <NuxtLink :to="`/workspaces/${wsId}/catalog`">Tambah barang dulu di halaman Barang</NuxtLink>.
                </p>
                <p v-if="!catalogExhausted && catalogRows.length > 0" class="hint">
                  {{ catalogRows.length }} barang dimuat sejauh ini.
                </p>
                <p v-if="!catalogExhausted && catalogRows.length > 0">
                  <button type="button" class="btn btn-secondary" :disabled="catalogLoading" @click="loadCatalogPage">
                    {{ catalogLoading ? 'Memuat…' : 'Muat lebih banyak barang' }}
                  </button>
                </p>
              </template>
            </div>
            <template v-if="selectedItemId && archivedSelected">
              <p class="alert" role="alert">Barang diarsipkan — riwayat tarif tetap dapat dibaca; jadwal baru tidak dapat dibuat.</p>
            </template>
            <template v-else-if="selectedItemId && itemNotFound">
              <p class="alert" role="alert">Barang yang dipilih tidak ada di workspace ini. Pilih barang lain untuk menjadwalkan tarif.</p>
            </template>
            <template v-else>
            <div class="field">
              <label for="price-from">Mulai (UTC)</label>
              <input
                id="price-from"
                ref="fromInput"
                v-model="validFrom"
                type="datetime-local"
                :disabled="fieldsLocked"
                :aria-invalid="fromError !== null"
                :aria-describedby="fromError ? 'price-from-error' : undefined"
              >
              <p v-if="fromError" id="price-from-error" class="field-error">{{ fromError }}</p>
              <p v-else class="hint">Tanggal dan jam (UTC), menit saja; tanpa detik. Dinilai server saat menyimpan.</p>
            </div>
            <div class="field">
              <label for="price-to">Berakhir (UTC)</label>
              <input
                id="price-to"
                ref="toInput"
                v-model="validTo"
                type="datetime-local"
                :disabled="fieldsLocked"
                :aria-invalid="toError !== null"
                :aria-describedby="toError ? 'price-to-error' : undefined"
              >
              <p v-if="toError" id="price-to-error" class="field-error">{{ toError }}</p>
              <p v-else class="hint">Tarif berlaku mulai tanggal mulai sampai sebelum tanggal berakhir; tanggal berakhir tidak termasuk.</p>
            </div>
            <div class="field">
              <label for="price-amount">Nilai tarif (unit tersimpan)</label>
              <input
                id="price-amount"
                ref="amountInput"
                v-model="amount"
                type="text"
                inputmode="numeric"
                autocomplete="off"
                :disabled="fieldsLocked"
                :aria-invalid="amountError !== null"
                :aria-describedby="amountError ? 'price-amount-error' : undefined"
              >
              <p v-if="amountError" id="price-amount-error" class="field-error">{{ amountError }}</p>
              <p v-else class="hint">Bilangan bulat 0 sampai 9223372036854775807, dikirim sebagai teks. Konversi satuan ke rupiah tidak didefinisikan; nilai ditampilkan apa adanya.</p>
            </div>
            <div class="field">
              <label for="price-currency">Mata uang</label>
              <input
                id="price-currency"
                ref="currencyInput"
                v-model="currency"
                type="text"
                autocomplete="off"
                :disabled="fieldsLocked"
                :aria-invalid="currencyError !== null"
                :aria-describedby="currencyError ? 'price-currency-error' : undefined"
              >
              <p v-if="currencyError" id="price-currency-error" class="field-error">{{ currencyError }}</p>
              <p v-else class="hint">Tepat 3 huruf kapital (A-Z), contoh: IDR. Tanpa nilai bawaan.</p>
            </div>
            <button type="submit" class="btn" :disabled="busy || submitBlocked">
              {{ busy ? 'Menyimpan…' : 'Simpan tarif' }}
            </button>
            </template>
          </form>

          <div v-if="saved" class="card alert-ok" aria-labelledby="saved-heading">
            <h3 id="saved-heading" ref="savedHeading" tabindex="-1">Tarif tersimpan</h3>
            <p>
              <strong>{{ saved.amount_minor }} unit tarif · {{ saved.currency }}</strong> untuk {{ savedLabel }} sudah tersimpan.
              Berlaku {{ saved.valid_from }} sampai sebelum {{ saved.valid_to }} (UTC).
            </p>
          </div>
        </section>

        <section class="card" aria-labelledby="list-heading">
          <h2 id="list-heading">Riwayat tarif</h2>
          <p v-if="!selectedItemId" class="muted">Pilih barang dulu untuk melihat tarif.</p>
          <template v-else-if="itemNotFound">
            <p class="alert" role="alert">Barang tidak ditemukan di workspace ini.</p>
          </template>
          <template v-else>
            <p v-if="reloadError" class="alert" role="alert">
              {{ reloadError }}
              <button type="button" class="btn btn-secondary" @click="refreshPrices(reloadError)">Muat ulang daftar</button>
            </p>
            <p v-if="refreshing" role="status">Memuat daftar…</p>
            <p v-else-if="items.length === 0 && listLoaded && !reloadError" class="muted">Belum ada tarif.</p>
            <ul v-else class="plain">
              <li v-for="p in items" :key="p.id"><strong>{{ p.amount_minor }} unit tarif · {{ p.currency }}</strong><br>Berlaku {{ p.valid_from }} sampai sebelum {{ p.valid_to }} (UTC)</li>
            </ul>
            <p v-if="loadMoreError" class="alert" role="alert">{{ loadMoreError }}</p>
            <p v-if="hasMore">
              <button type="button" class="btn btn-secondary" :disabled="loadingMore" @click="loadMore">
                {{ loadingMore ? 'Memuat…' : 'Muat lebih' }}
              </button>
            </p>
          </template>
        </section>
      </template>
    </template>
  </main>
</template>
