<script setup lang="ts">
// Draf sewa workspace (GET/POST /workspaces/{id}/rental/contracts +
// GET pelanggan/katalog). LIST+CREATE saja; tanpa detail/edit/hapus,
// tanpa stok/unit/ketersediaan, tanpa holds/quote/harga, tanpa
// serah-terima/retur/kerusakan, tanpa pembayaran. Draf TIDAK mereservasi
// barang, TIDAK menjamin ketersediaan, TIDAK memilih unit fisik, dan
// TIDAK mengunci harga — salinan itu tampil tetap di halaman.
//
// Server otoritatif: baca riwayat butuh view_workspace; draf baru butuh
// manage_business_data + instalasi rental.basic aktif + entitlement
// versi aktif; kasir melihat formulir dan menerima 403 saat simpan
// (bukan disembunyikan). Tanpa aturan larangan backdate di server:
// interval lampau yang valid diterima.
//
// SATU percobaan logis, SATU kunci: kunci dibuat dengan
// crypto.randomUUID() sesudah input valid dan sebelum POST pertama, lalu
// dibekukan bersama muatan ternormalisasi (workspace, pelanggan, barang,
// waktu UTC). Hanya 201 atau replay 200 dengan baris yang valid yang
// mengonfirmasi simpan. Hasil tak diketahui mengunci field/picker dan
// hanya menawarkan muat-ulang daftar (GET saja), kirim-ulang kunci-sama,
// atau "Mulai draf baru" yang menebus snapshot; tidak ada ulang otomatis
// atau kunci baru buta. Pemulihan hanya hidup di memori halaman ini.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Draf sewa — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface DraftRow { id: string, workspace_id: string, customer_id: string, catalog_item_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, created_at: string, updated_at: string }
interface CustomerRow { id: string, workspace_id: string, display_name: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }

const PAGE_LIMIT = 50
const PICKER_PAGE = 100
const ITEM_INACTIVE = 'barang tidak aktif'
const KEY_MISMATCH = 'kunci idempotency sudah dipakai untuk permintaan yang berbeda'

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

interface Attempt {
  key: string
  workspaceId: string
  customerId: string
  customerLabel: string
  itemId: string
  itemLabel: string
  fromMicros: bigint
  toMicros: bigint
  fromIso: string
  toIso: string
}

// Baris tersimpan minimum: id ada, workspace + pelanggan + barang cocok
// (kanonis huruf kecil), client_request_id sama dengan kunci yang
// dibekukan, status 'draft', dan kedua instan setara dengan yang
// dibekukan (banding mikrodetik, bukan string).
function isValidDraftRow(row: unknown, ws: string, a: Attempt): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id === '') return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || r.customer_id.toLowerCase() !== a.customerId.toLowerCase()) return false
  if (typeof r.catalog_item_id !== 'string' || r.catalog_item_id.toLowerCase() !== a.itemId.toLowerCase()) return false
  if (r.client_request_id !== a.key) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
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
const items = ref<DraftRow[]>([])
const nextOffset = ref(0)
const hasMore = ref(false)
const loadingMore = ref(false)
const loadMoreError = ref<string | null>(null)
const reloadError = ref<string | null>(null)
// Transisi baca ditolak sesudah halaman termuat (403 pada GET daftar/
// pelanggan/katalog/refresh): daftar dan formulir yang sudah termuat
// disembunyikan dan kepemilikan yang tertunda dibatalkan (lihat denyRead).
const readDenied = ref(false)
// Riwayat hanya boleh menampilkan "Belum ada draf." sesudah GET yang
// berhasil mengembalikan nol baris; refreshing menandai refresh yang
// sedang berjalan supaya jeda muat ulang tidak dibaca sebagai kosong.
const listLoaded = ref(false)
const refreshing = ref(false)

// Peta pelanggan: semua baris yang dimuat untuk label riwayat.
// parties tidak punya kolom status di sumber, jadi tidak ada cabang
// arsip pelanggan. Offset maju menurut jumlah mentah yang diterima.
const customerRows = ref<CustomerRow[]>([])
const customerOffset = ref(0)
const customerExhausted = ref(false)
const customerLoading = ref(false)
const customerLoadError = ref<string | null>(null)
const customerStarted = ref(false)
const customerById = computed(() => new Map(customerRows.value.map(r => [r.id, r])))

// Peta katalog: baris mentah (termasuk arsip) untuk label riwayat.
// Pilihan baru hanya barang aktif; arsip tidak ditawarkan untuk draf
// baru tetapi label riwayatnya dipertahankan ("(arsip — riwayat)").
// Offset maju menurut jumlah mentah yang diterima, bukan jumlah render,
// supaya satu halaman penuh arsip tidak menyembunyikan barang aktif di
// halaman berikut; render dedup berdasarkan id.
const catalogRows = ref<CatalogRow[]>([])
const catalogOffset = ref(0)
const catalogExhausted = ref(false)
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogStarted = ref(false)
const catalogById = computed(() => new Map(catalogRows.value.map(r => [r.id, r])))
const activeItemChoices = computed(() => catalogRows.value.filter(r => r.status === 'active'))

const selectedCustomerId = ref('')
const selectedItemId = ref('')
const itemArchived = ref(false)
// Barang yang ditolak server 409 tidak aktif — penolakan terikat pada
// barang ini, bukan pada halaman (lihat archivedSelected).
const archivedItemId = ref<string | null>(null)
const startsAt = ref('')
const endsAt = ref('')
const customerError = ref<string | null>(null)
const itemError = ref<string | null>(null)
const fromError = ref<string | null>(null)
const toError = ref<string | null>(null)
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
const saved = ref<DraftRow | null>(null)
const savedHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)
const customerSelect = ref<HTMLSelectElement | null>(null)
const itemSelect = ref<HTMLSelectElement | null>(null)
const fromInput = ref<HTMLInputElement | null>(null)
const toInput = ref<HTMLInputElement | null>(null)

const selectedCustomerRow = computed(() => selectedCustomerId.value ? customerById.value.get(selectedCustomerId.value) ?? null : null)
const selectedItemRow = computed(() => selectedItemId.value ? catalogById.value.get(selectedItemId.value) ?? null : null)
const selectedItemArchived = computed(() => {
  const row = selectedItemRow.value
  return !!row && row.status !== 'active'
})
// Barang yang dipilih lalu diarsipkan di luar halaman (atau ditolak server
// 409 tidak aktif): draf baru dicegah; riwayat tetap dibaca dan pemulihan
// kunci-sama tetap tersedia. Penolakan server terikat pada barang penyebabnya
// (archivedItemId) supaya ganti pilihan biasa tidak ikut teracuni.
const archivedSelected = computed(() => selectedItemArchived.value || (itemArchived.value && archivedItemId.value !== null && archivedItemId.value.toLowerCase() === selectedItemId.value.toLowerCase()))

// Label pelanggan: dari peta bila ada; bila peta belum lengkap, katakan
// jujur belum dimuat — tanpa menebak terhapus/tersedia.
function customerLabel(customerId: string): string {
  const row = customerById.value.get(customerId)
  if (row) return row.display_name
  return customerExhausted.value ? 'Nama pelanggan tidak tersedia' : 'Nama pelanggan belum dimuat'
}

// Label barang: dari peta bila ada; bila peta belum lengkap, katakan
// jujur belum dimuat — tanpa menebak terhapus/arsip/tersedia.
function catalogLabel(catalogItemId: string): string {
  const row = catalogById.value.get(catalogItemId)
  if (row) return `${row.sku} — ${row.name}` + (row.status === 'active' ? '' : ' (arsip — riwayat)')
  return catalogExhausted.value ? 'Nama barang tidak tersedia' : 'Nama barang belum dimuat'
}
const savedCustomerLabel = computed(() => (saved.value ? customerLabel(saved.value.customer_id) : ''))
const savedItemLabel = computed(() => (saved.value ? catalogLabel(saved.value.catalog_item_id) : ''))

// Penjaga generasi: hasil muat yang tiba sesudah reset/pindah workspace/
// logout diabaikan; muat-lebih lama tidak boleh menempel ke prefix baru.
// inFlight/customerInFlight/catalogInFlight dijaga sinkron (bukan hanya
// tombol disabled). postGen: kepemilikan POST — kelanjutan POST basi
// (sukses, galat, nextTick, refresh) tidak boleh menyentuh halaman/sesi/
// daftar pengganti. loadMoreGen/customerLoadGen/catalogLoadGen:
// kepemilikan flag busy — finally basi tidak boleh menghapus busy milik
// permintaan baru.
let listGen = 0
let postGen = 0
let loadMoreGen = 0
let customerGen = 0
let customerLoadGen = 0
let catalogGen = 0
let catalogLoadGen = 0
let inFlight = false
let customerInFlight = false
let catalogInFlight = false

function stale(gen: number) {
  return gen !== listGen
}

function stalePost(gen: number) {
  return gen !== postGen
}

// Transisi baca ditolak (GET daftar/pelanggan/katalog/refresh 403 sesudah
// halaman termuat): beri umpan balik izin workspace, sembunyikan daftar
// dan formulir yang sudah termuat, dan batalkan kepemilikan daftar/
// picker/POST yang tertunda supaya penyelesaian terlambat tidak mengisi
// ulang halaman pengganti. Sesi dipertahankan — 403 bukan 401. Berbeda
// dengan POST 403 (formDenied) yang mempertahankan isian/daftar/sesi
// pembaca.
function denyRead(message: string) {
  listGen++
  postGen++
  loadMoreGen++
  customerGen++
  customerLoadGen++
  catalogGen++
  catalogLoadGen++
  loadingMore.value = false
  loadMoreError.value = null
  customerInFlight = false
  customerLoading.value = false
  catalogInFlight = false
  catalogLoading.value = false
  inFlight = false
  busy.value = false
  items.value = []
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  nextOffset.value = 0
  hasMore.value = false
  selectedCustomerId.value = ''
  selectedItemId.value = ''
  itemArchived.value = false
  archivedItemId.value = null
  startsAt.value = ''
  endsAt.value = ''
  customerError.value = null
  itemError.value = null
  fromError.value = null
  toError.value = null
  formError.value = null
  formDenied.value = null
  replayError.value = null
  unknownOutcome.value = false
  keyMismatch.value = false
  attempt.value = null
  saved.value = null
  listLoaded.value = false
  refreshing.value = false
  // Penolakan saat ini menyelesaikan loader global: penolakan awal
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
  customerGen++
  customerLoadGen++
  catalogGen++
  catalogLoadGen++
  loadingMore.value = false
  loadMoreError.value = null
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  customerLoading.value = false
  catalogLoading.value = false
  customerInFlight = false
  catalogInFlight = false
  customerStarted.value = false
  catalogStarted.value = false
  selectedCustomerId.value = ''
  selectedItemId.value = ''
  itemArchived.value = false
  archivedItemId.value = null
  attempt.value = null
  unknownOutcome.value = false
  keyMismatch.value = false
  saved.value = null
  readDenied.value = false
  listLoaded.value = false
  refreshing.value = false
  await load()
})

// Ganti pilihan barang biasa ke barang lain menghapus penolakan basi
// (termasuk pesan galatnya) supaya koreksi tidak terkunci; kembali ke
// barang yang sama tetap dibatasi lewat archivedSelected. Tidak menyentuh
// snapshot/pemulihan — field terkunci selama tak diketahui/mismatch, jadi
// watch ini hanya jalan untuk perubahan biasa.
watch(selectedItemId, (next) => {
  if (archivedItemId.value !== null && next.toLowerCase() !== archivedItemId.value.toLowerCase()) {
    itemArchived.value = false
    archivedItemId.value = null
    if (itemError.value === ITEM_INACTIVE) itemError.value = null
  }
})

onUnmounted(() => {
  listGen++
  postGen++
  loadMoreGen++
  customerGen++
  customerLoadGen++
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
  // Picker dan daftar adalah konten utama halaman ini: status memuat
  // dipertahankan sampai halaman awal ketiganya selesai supaya jeda
  // muat tidak dibaca sebagai halaman kosong.
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
  if (!stale(gen)) await loadDraftsPage(gen)
  if (!stale(gen)) loading.value = false
}

// Satu halaman pelanggan (100 baris). Tanpa rantai otomatis: pemanggil
// berikutnya hanyalah tombol "Muat lebih banyak pelanggan" atau "Coba
// lagi" (yang memakai offset yang sama). Kegagalan mempertahankan baris,
// peta, pilihan, dan kursor yang sudah ada.
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
    // Kursor dari jumlah mentah yang diterima, bukan panjang render.
    customerOffset.value = wantOffset + res.data.items.length
    customerExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== customerGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      // Baca pelanggan ditolak sesudah muat: sembunyikan halaman, jangan
      // tampilkan galat picker di atas daftar lama.
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) customerLoadError.value = err.message
    else customerLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    // Hanya pemilik flag saat ini yang boleh mematikannya; finally basi
    // tidak boleh mematikan busy milik muat-pelanggan baru.
    if (myLoad === customerLoadGen) {
      customerInFlight = false
      customerLoading.value = false
    }
  }
}

// Satu halaman katalog (100 baris). Tanpa rantai otomatis: pemanggil
// berikutnya hanyalah tombol "Muat lebih banyak barang" atau "Coba lagi"
// (yang memakai offset yang sama). Kegagalan mempertahankan baris, peta,
// pilihan, dan kursor yang sudah ada.
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
    // Kursor dari jumlah mentah yang diterima, bukan panjang render.
    catalogOffset.value = wantOffset + res.data.items.length
    catalogExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== catalogGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      // Baca katalog ditolak sesudah muat: sembunyikan halaman, jangan
      // tampilkan galat picker di atas daftar lama.
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

// Halaman pertama riwayat draf. GET koleksi 404 adalah galat baca yang
// tak terduga, bukan penghapusan pelanggan/barang: tampilkan salinan
// galat ramah dengan pemulihan GET-only, tanpa klaim kosong dan tanpa
// mensintesis entitas yang hilang.
async function loadDraftsPage(gen: number) {
  const s = session
  if (!s) return
  // Muat daftar: tampilkan "Memuat daftar…" selama GET tertunda, bukan
  // daftar kosong atau area kosong.
  refreshing.value = true
  try {
    const list = await apiFetch<{ items: DraftRow[] }>(s, `/api/workspaces/${wsId.value}/rental/contracts?limit=${PAGE_LIMIT}`)
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
    // Galat baca lain (termasuk 404 koleksi): tampil di bagian daftar
    // dengan salinan ramah, dengan retry GET-only, tanpa klaim kosong.
    reloadError.value = 'Daftar draf gagal dimuat. Coba muat ulang daftar.'
  } finally {
    // Hanya pemilik pemuatan saat ini yang boleh mematikan status muat;
    // finally basi tidak boleh menghapus status milik pemuatan baru.
    if (gen === listGen) refreshing.value = false
  }
}

async function loadMore() {
  const s = session
  if (!s || loadingMore.value || !hasMore.value) return
  const gen = listGen
  const myLoad = ++loadMoreGen
  const wantOffset = nextOffset.value
  loadingMore.value = true
  loadMoreError.value = null
  try {
    const res = await apiFetch<{ items: DraftRow[] }>(s, `/api/workspaces/${wsId.value}/rental/contracts?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
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
    if (err instanceof ApiError) loadMoreError.value = err.message
    else loadMoreError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    // Hanya pemilik flag saat ini yang boleh mematikannya; finally basi
    // tidak boleh mematikan busy milik muat-lebih baru.
    if (myLoad === loadMoreGen) loadingMore.value = false
  }
}

// Muat ulang riwayat (GET saja): dipakai sesudah simpan 201/200 dan oleh
// tombol "Muat ulang daftar" saat hasil tak diketahui atau daftar gagal.
// Kegagalan menampilkan reloadError tanpa menyembunyikan konfirmasi
// tersimpan dan tanpa klaim riwayat kosong; picker tidak ikut dimuat ulang.
async function refreshDrafts(failureMessage: string) {
  const s = session
  if (!s) return
  const gen = ++listGen
  // Reset riwayat yang koheren: muat-lebih lama dibatalkan kepemilikannya
  // dan flag-nya dibersihkan supaya paginasi baru tetap bisa dipakai.
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  reloadError.value = null
  refreshing.value = true
  try {
    const list = await apiFetch<{ items: DraftRow[] }>(s, `/api/workspaces/${wsId.value}/rental/contracts?limit=${PAGE_LIMIT}`)
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
    reloadError.value = failureMessage
  } finally {
    // Hanya pemilik refresh saat ini yang boleh mematikan status muat;
    // finally basi tidak boleh menghapus status milik refresh baru.
    if (gen === listGen) refreshing.value = false
  }
}

// Hasil tak diketahui (jaringan/timeout/500/502, 200/201 lain, atau baris
// yang cacat): snapshot dibekukan apa adanya, kirim biasa terkunci, tanpa
// ulang otomatis. Hanya muat-ulang daftar, kirim-ulang kunci-sama, atau
// "Mulai draf baru" yang boleh melanjutkan.
function setUnknownOutcome(gen: number) {
  unknownOutcome.value = true
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

// Kunci tercemar (409 mismatch): kunci sudah terikat pada permintaan lain.
// Satu status mismatch yang koheren: panel tak diketahui disembunyikan
// (kirim-ulang tidak lagi ditawarkan), snapshot dipertahankan untuk
// ringkasan beku, kirim terkunci, dan hanya "Mulai draf baru" yang boleh
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
// dibuka lagi dengan nilai yang dipertahankan. Draf sebelumnya mungkin
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

async function confirmSave(row: DraftRow, gen: number) {
  saved.value = row
  startsAt.value = ''
  endsAt.value = ''
  unknownOutcome.value = false
  keyMismatch.value = false
  attempt.value = null
  replayError.value = null
  items.value = []
  // Riwayat dikosongkan untuk dimuat ulang: tandai belum termuat supaya
  // jeda refresh tidak ditampilkan sebagai "Belum ada draf.".
  listLoaded.value = false
  nextOffset.value = 0
  hasMore.value = false
  await nextTick()
  if (stalePost(gen)) return
  savedHeading.value?.focus()
  await refreshDrafts('Data tersimpan, tetapi daftar gagal dimuat ulang. Coba muat ulang daftar.')
}

async function submitCreate(resend = false) {
  const s = session
  if (!s || inFlight) return
  if ((unknownOutcome.value || keyMismatch.value) && !resend) return
  // Kirim-ulang dari status mismatch dilarang: kunci sudah terikat pada
  // permintaan lain; hanya Mulai draf baru yang boleh melanjutkan.
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
      const { status, data } = await apiFetch<DraftRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts`, {
        method: 'POST',
        body: { customer_id: a.customerId, catalog_item_id: a.itemId, starts_at: a.fromIso, ends_at: a.toIso },
        idempotencyKey: a.key
      })
      if (stalePost(gen)) return
      // Replay 200 atau 201 dengan baris yang valid mengonfirmasi tulis
      // yang sudah terkomit; 2xx lain atau baris cacat tetap tak diketahui.
      if ((status === 200 || status === 201) && isValidDraftRow(data, a.workspaceId, a)) {
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
        if (err.status === 409 && err.message === ITEM_INACTIVE) {
          itemArchived.value = true
          archivedItemId.value = a.itemId
        }
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
  const customerId = selectedCustomerId.value
  const itemId = selectedItemId.value
  const fromRaw = startsAt.value
  const toRaw = endsAt.value
  customerError.value = null
  itemError.value = null
  fromError.value = null
  toError.value = null
  formError.value = null
  formDenied.value = null
  let firstBad: HTMLElement | null = null
  if (customerId === '') {
    customerError.value = 'Pilih pelanggan dulu'
    firstBad = customerSelect.value
  }
  if (itemId === '') {
    itemError.value = 'Pilih barang dulu'
    if (!firstBad) firstBad = itemSelect.value
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
    customerId,
    customerLabel: customerLabel(customerId),
    itemId,
    itemLabel: catalogLabel(itemId),
    fromMicros: fromParsed!.micros,
    toMicros: toParsed!.micros,
    fromIso: fromParsed!.iso,
    toIso: toParsed!.iso
  }
  attempt.value = snapshot
  inFlight = true
  busy.value = true
  try {
    const { status, data } = await apiFetch<DraftRow>(s, `/api/workspaces/${wsId.value}/rental/contracts`, {
      method: 'POST',
      body: { customer_id: snapshot.customerId, catalog_item_id: snapshot.itemId, starts_at: snapshot.fromIso, ends_at: snapshot.toIso },
      idempotencyKey: snapshot.key
    })
    if (stalePost(gen)) return
    // Hanya 201 (atau replay 200) dengan baris yang valid yang
    // mengonfirmasi simpan.
    if ((status === 201 || status === 200) && isValidDraftRow(data, wsId.value, snapshot)) {
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
      if (msg.includes('customer_id')) {
        customerError.value = msg
        await nextTick()
        if (!stalePost(gen)) customerSelect.value?.focus()
      } else if (msg.includes('catalog_item_id')) {
        itemError.value = msg
        await nextTick()
        if (!stalePost(gen)) itemSelect.value?.focus()
      } else if (msg.includes('ends_at')) {
        toError.value = msg
        await nextTick()
        if (!stalePost(gen)) toInput.value?.focus()
      } else if (msg.includes('starts_at')) {
        fromError.value = msg
        await nextTick()
        if (!stalePost(gen)) fromInput.value?.focus()
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
      if (err.message === ITEM_INACTIVE) {
        itemArchived.value = true
        archivedItemId.value = itemId
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
    <h1>Draf sewa</h1>
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
        <p class="small">
          Draf ini tidak mereservasi barang, tidak menjamin ketersediaan, tidak memilih unit fisik, dan tidak mengunci harga.
        </p>

        <section class="card" aria-labelledby="add-heading">
          <h2 id="add-heading">Buat draf sewa</h2>
          <p class="small muted">
            Membuat draf memerlukan izin pengelolaan; izin diperiksa saat menyimpan.
          </p>
          <div v-if="formDenied" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formDenied }}</p>
            <p>
              <NuxtLink :to="`/workspaces/${wsId}/modules`">Lihat fitur workspace</NuxtLink>
            </p>
          </div>
          <div v-else-if="formError" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formError }}</p>
          </div>
          <div v-if="keyMismatch" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <h3>Kunci idempotency sudah dipakai</h3>
            <p>
              {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
              tidak dapat dikirim ulang dengan kunci yang sama. Buat draf baru untuk
              melanjutkan dengan kunci baru.
            </p>
            <p v-if="attempt">
              Permintaan yang belum pasti: {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC).
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai draf baru</button>
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
              Permintaan yang belum pasti: {{ attempt.customerLabel }} — {{ attempt.itemLabel }}. Periode yang dicatat:
              mulai {{ attempt.fromIso }}; berakhir {{ attempt.toIso }} (UTC).
            </p>
            <p v-if="replayError" class="field-error">Kirim ulang ditolak: {{ replayError }}</p>
            <p>
              <button type="button" class="btn btn-secondary" @click="refreshDrafts('Daftar gagal dimuat ulang. Coba muat ulang daftar.')">Muat ulang daftar</button>
              <button type="button" class="btn btn-secondary" @click="resendCreate">Kirim lagi (permintaan sama)</button>
            </p>
            <p>
              <button type="button" class="btn btn-secondary" :disabled="busy" @click="retireAttempt">Mulai draf baru</button>
              Mulai draf baru membatalkan pemulihan ini. Draf sebelumnya mungkin sudah
              tersimpan dan tidak dibatalkan oleh tindakan ini.
            </p>
          </div>
          <form @submit.prevent="submitCreate()" novalidate>
            <div class="field">
              <label for="draft-customer">Pelanggan</label>
              <select
                id="draft-customer"
                ref="customerSelect"
                v-model="selectedCustomerId"
                :disabled="fieldsLocked || customerRows.length === 0"
                :aria-invalid="customerError !== null"
                :aria-describedby="customerError ? 'draft-customer-error' : undefined"
              >
                <option value="">Pilih pelanggan</option>
                <option v-for="c in customerRows" :key="c.id" :value="c.id">{{ c.display_name }}</option>
              </select>
              <p v-if="customerError" id="draft-customer-error" class="field-error">{{ customerError }}</p>
              <p v-else class="hint">Pelanggan dipilih dari daftar pelanggan workspace.</p>
              <p v-if="customerLoading && customerRows.length === 0" role="status">Memuat pelanggan…</p>
              <div v-else-if="customerLoadError && customerRows.length === 0" class="alert" role="alert">
                <p>{{ customerLoadError }}</p>
                <p><button type="button" class="btn btn-secondary" @click="loadCustomerPage">Coba lagi</button></p>
              </div>
              <template v-else>
                <p v-if="customerLoadError" class="alert" role="alert">
                  {{ customerLoadError }}
                  <button type="button" class="btn btn-secondary" @click="loadCustomerPage">Coba lagi</button>
                </p>
                <p v-if="customerExhausted && customerRows.length === 0" class="hint">
                  Belum ada pelanggan.
                  <NuxtLink :to="`/workspaces/${wsId}/customers`">Tambah pelanggan dulu di halaman Pelanggan</NuxtLink>.
                </p>
                <p v-if="customerRows.length > 0" class="hint">
                  {{ customerRows.length }} pelanggan dimuat sejauh ini.
                </p>
                <p v-if="!customerExhausted && customerRows.length > 0">
                  <button type="button" class="btn btn-secondary" :disabled="customerLoading" @click="loadCustomerPage">
                    {{ customerLoading ? 'Memuat…' : 'Muat lebih banyak pelanggan' }}
                  </button>
                </p>
              </template>
            </div>
            <div class="field">
              <label for="draft-item">Barang</label>
              <select
                id="draft-item"
                ref="itemSelect"
                v-model="selectedItemId"
                :disabled="fieldsLocked || activeItemChoices.length === 0"
                :aria-invalid="itemError !== null"
                :aria-describedby="itemError ? 'draft-item-error' : undefined"
              >
                <option value="">Pilih barang</option>
                <option v-for="item in activeItemChoices" :key="item.id" :value="item.id">{{ item.sku }} — {{ item.name }}</option>
              </select>
              <p v-if="itemError" id="draft-item-error" class="field-error">{{ itemError }}</p>
              <p v-else class="hint">Barang dipilih dari katalog workspace; hanya barang aktif yang dapat dibuat draf baru.</p>
              <p v-if="catalogLoading && catalogRows.length === 0" role="status">Memuat barang…</p>
              <div v-else-if="catalogError && catalogRows.length === 0" class="alert" role="alert">
                <p>{{ catalogError }}</p>
                <p><button type="button" class="btn btn-secondary" @click="loadItemPage">Coba lagi</button></p>
              </div>
              <template v-else>
                <p v-if="catalogError" class="alert" role="alert">
                  {{ catalogError }}
                  <button type="button" class="btn btn-secondary" @click="loadItemPage">Coba lagi</button>
                </p>
                <p v-if="catalogExhausted && catalogRows.length === 0" class="hint">
                  Belum ada barang.
                  <NuxtLink :to="`/workspaces/${wsId}/catalog`">Tambah barang dulu di halaman Barang</NuxtLink>.
                </p>
                <p v-if="catalogRows.length > 0" class="hint">
                  {{ catalogRows.length }} barang dimuat sejauh ini.
                </p>
                <p v-if="!catalogExhausted && catalogRows.length > 0">
                  <button type="button" class="btn btn-secondary" :disabled="catalogLoading" @click="loadItemPage">
                    {{ catalogLoading ? 'Memuat…' : 'Muat lebih banyak barang' }}
                  </button>
                </p>
              </template>
            </div>
            <template v-if="selectedItemId && archivedSelected">
              <p class="alert" role="alert">Barang diarsipkan — riwayat draf tetap dapat dibaca; draf baru tidak dapat dibuat.</p>
            </template>
            <template v-else>
            <div class="field">
              <label for="draft-from">Mulai (UTC)</label>
              <input
                id="draft-from"
                ref="fromInput"
                v-model="startsAt"
                type="datetime-local"
                :disabled="fieldsLocked"
                :aria-invalid="fromError !== null"
                :aria-describedby="fromError ? 'draft-from-error' : undefined"
              >
              <p v-if="fromError" id="draft-from-error" class="field-error">{{ fromError }}</p>
              <p v-else class="hint">Tanggal dan jam (UTC), menit saja; tanpa detik. Interval lampau yang valid tetap diterima server.</p>
            </div>
            <div class="field">
              <label for="draft-to">Berakhir (UTC)</label>
              <input
                id="draft-to"
                ref="toInput"
                v-model="endsAt"
                type="datetime-local"
                :disabled="fieldsLocked"
                :aria-invalid="toError !== null"
                :aria-describedby="toError ? 'draft-to-error' : undefined"
              >
              <p v-if="toError" id="draft-to-error" class="field-error">{{ toError }}</p>
              <p v-else class="hint">Tanggal dan jam berakhir yang dicatat untuk draf ini (UTC).</p>
            </div>
            <button type="submit" class="btn" :disabled="busy || submitBlocked">
              {{ busy ? 'Menyimpan…' : 'Simpan draf sewa' }}
            </button>
            </template>
          </form>

          <div v-if="saved" class="card alert-ok" aria-labelledby="saved-heading">
            <h3 id="saved-heading" ref="savedHeading" tabindex="-1">Draf tersimpan</h3>
            <p>
              Draf untuk <strong>{{ savedCustomerLabel }} — {{ savedItemLabel }}</strong> sudah tersimpan.
              Periode yang dicatat: mulai {{ saved.starts_at }}; berakhir {{ saved.ends_at }} (UTC). Status: Draf.
            </p>
          </div>
        </section>

        <section class="card" aria-labelledby="list-heading">
          <h2 id="list-heading">Riwayat draf</h2>
          <p>
            <button type="button" class="btn btn-secondary" :disabled="refreshing" @click="refreshDrafts('Daftar gagal dimuat ulang. Coba muat ulang daftar.')">
              {{ refreshing ? 'Memuat…' : 'Muat ulang daftar' }}
            </button>
          </p>
          <p v-if="reloadError" class="alert" role="alert">
            {{ reloadError }}
          </p>
          <p v-if="refreshing" role="status">Memuat daftar…</p>
          <p v-else-if="items.length === 0 && listLoaded && !reloadError" class="muted">Belum ada draf.</p>
          <ul v-else class="plain">
            <li v-for="d in items" :key="d.id"><strong>{{ customerLabel(d.customer_id) }} — {{ catalogLabel(d.catalog_item_id) }}</strong><br>Periode yang dicatat: mulai {{ d.starts_at }}; berakhir {{ d.ends_at }} (UTC)<br>Status: Draf</li>
          </ul>
          <p v-if="loadMoreError" class="alert" role="alert">{{ loadMoreError }}</p>
          <p v-if="hasMore">
            <button type="button" class="btn btn-secondary" :disabled="loadingMore" @click="loadMore">
              {{ loadingMore ? 'Memuat…' : 'Muat lebih' }}
            </button>
          </p>
        </section>
      </template>
    </template>
  </main>
</template>
