<script setup lang="ts">
// Unit fisik workspace (GET/POST /workspaces/{id}/assets + GET katalog).
// LIST+CREATE saja; tanpa edit/hapus/arsip/cari, tanpa ketersediaan atau
// kondisi. Server otoritatif: baca butuh view_workspace, simpan butuh
// manage_business_data; kasir melihat formulir dan menerima 403 saat
// simpan (bukan disembunyikan). Hanya 201 dengan baris yang valid yang
// mengonfirmasi simpan; create TIDAK idempotent, jadi tidak ada ulang
// otomatis dan hasil tak diketahui ditangani eksplisit (seperti R1
// pelanggan/katalog).
//
// Dua sumber data independen sesudah pemeriksaan workspace:
// - Daftar unit (limit 50) memakai state halaman (loading/pageError).
// - Peta katalog (limit 100 per halaman) memakai state sendiri:
//   SATU GET awal, lalu SATU GET per klik "Muat lebih banyak barang".
//   Tanpa rantai otomatis, tanpa muat ulang katalog saat daftar unit
//   dimuat ulang atau sesudah simpan. Kegagalan katalog tidak pernah
//   menyembunyikan daftar unit yang berhasil dimuat.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Unit — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface AssetRow { id: string, workspace_id: string, catalog_item_id: string, serial: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }

const PAGE_LIMIT = 50
const CATALOG_PAGE = 100
const SERIAL_MAX_RUNES = 64
const SERIAL_TAKEN = 'serial sudah dipakai'
const ITEM_INACTIVE = 'barang tidak aktif'
const runeLen = (s: string) => [...s].length

// strings.TrimSpace Go: JS trim berbeda — Go memangkas U+0085 tapi TIDAK
// memangkas U+FEFF; JS sebaliknya. Samakan dengan Go sebelum validasi
// panjang (rune) dan pengiriman, supaya server menerima yang klien
// nyatakan valid dan identitas nilai tidak berubah diam-diam.
const GO_TRIM_RE = /^[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+|[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+$/g
const trimGoSpace = (s: string) => s.replace(GO_TRIM_RE, '')

// Baris tersimpan minimum: id ada, workspace cocok, catalog_item_id sama
// dengan yang dipilih (dibandingkan kanonis — backend menormalisasi UUID
// valid ke huruf kecil), dan serial sama persis dengan yang dikirim
// (tanpa perubahan huruf).
function isValidAssetRow(row: unknown, ws: string, itemId: string, serial: string): row is AssetRow {
  return !!row && typeof row === 'object' &&
    typeof (row as AssetRow).id === 'string' && (row as AssetRow).id !== '' &&
    (row as AssetRow).workspace_id === ws &&
    typeof (row as AssetRow).catalog_item_id === 'string' &&
    (row as AssetRow).catalog_item_id.toLowerCase() === itemId.toLowerCase() &&
    (row as AssetRow).serial === serial
}

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const workspace = ref<Workspace | null>(null)
const items = ref<AssetRow[]>([])
const nextOffset = ref(0)
const hasMore = ref(false)
const loadingMore = ref(false)
const loadMoreError = ref<string | null>(null)
const reloadError = ref<string | null>(null)
// Transisi baca ditolak sesudah halaman termuat (403 pada GET daftar/
// katalog/refresh): daftar dan formulir yang sudah termuat disembunyikan
// dan kepemilikan yang tertunda dibatalkan (lihat denyRead).
const readDenied = ref(false)
// Daftar unit hanya boleh menampilkan "Belum ada unit." sesudah GET yang
// berhasil mengembalikan nol baris; refreshing menandai refresh yang
// sedang berjalan supaya jeda muat ulang tidak dibaca sebagai kosong.
const listLoaded = ref(false)
const refreshing = ref(false)

// Peta katalog: baris mentah (termasuk arsip) untuk label, pilihan aktif
// saja untuk create. Offset maju menurut jumlah mentah yang diterima,
// bukan jumlah render; render dedup berdasarkan id.
const catalogRows = ref<CatalogRow[]>([])
const catalogOffset = ref(0)
const catalogExhausted = ref(false)
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogStarted = ref(false)
const catalogById = computed(() => new Map(catalogRows.value.map(r => [r.id, r])))
const catalogActiveChoices = computed(() => catalogRows.value.filter(r => r.status === 'active'))

const selectedItemId = ref('')
const serialText = ref('')
const itemError = ref<string | null>(null)
const serialError = ref<string | null>(null)
const formError = ref<string | null>(null)
const formDenied = ref<string | null>(null)
const unknownOutcome = ref(false)
// Kunci kirim ulang: sesudah hasil tak diketahui, kirim biasa dengan
// nilai yang sama (barang + serial) tidak boleh POST lagi; hanya tombol
// "Kirim lagi (berisiko duplikat)" yang boleh. Nilai yang diubah adalah
// kiriman baru.
const submitLocked = ref(false)
const lockedPayload = ref('')
const payloadKey = (itemId: string, serial: string) => JSON.stringify([itemId, serial])
const submitBlocked = computed(() => submitLocked.value && payloadKey(selectedItemId.value, trimGoSpace(serialText.value)) === lockedPayload.value)
const busy = ref(false)
const saved = ref<AssetRow | null>(null)
const savedHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)
const itemSelect = ref<HTMLSelectElement | null>(null)
const serialInput = ref<HTMLInputElement | null>(null)

// Pilihan yang sempat dipilih lalu tidak aktif lagi (mis. diarsipkan di
// luar API): tetap tampil sebagai opsi nonaktif supaya pilihan tidak
// berganti diam-diam; simpan akan ditolak server dengan 409.
const selectedStaleLabel = computed(() => {
  const id = selectedItemId.value
  if (!id) return null
  if (catalogActiveChoices.value.some(r => r.id === id)) return null
  const row = catalogById.value.get(id)
  if (row) return `${row.sku} — ${row.name} (arsip)`
  return `Barang ${id.slice(0, 8)}… (belum dimuat)`
})

// Label baris daftar: dari peta bila ada; bila peta belum lengkap, katakan
// jujur belum dimuat — tanpa menebak terhapus/arsip/tersedia dan tanpa
// memaksa UUID ke alur pengguna. Bila katalog sudah habis dimuat dan
// tetap tak ada, katakan tidak tersedia.
function catalogLabel(catalogItemId: string): string {
  const row = catalogById.value.get(catalogItemId)
  if (row) return `${row.sku} — ${row.name}` + (row.status === 'active' ? '' : ' (arsip)')
  return catalogExhausted.value ? 'Nama barang tidak tersedia' : 'Nama barang belum dimuat'
}
const savedLabel = computed(() => (saved.value ? catalogLabel(saved.value.catalog_item_id) : ''))

// Penjaga generasi: hasil muat yang tiba sesudah reset/create/pindah
// workspace/logout diabaikan; muat-lebih lama tidak boleh menempel ke
// prefix baru. inFlight/catalogInFlight dijaga sinkron (bukan hanya
// tombol disabled). postGen: kepemilikan POST — kelanjutan POST basi
// (sukses, galat, nextTick, refresh) tidak boleh menyentuh
// halaman/sesi/daftar pengganti. loadMoreGen/catalogLoadGen: kepemilikan
// flag busy — finally basi tidak boleh menghapus busy milik permintaan
// baru, dan reset daftar membersihkannya.
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
  serialText.value = ''
  itemError.value = null
  serialError.value = null
  formError.value = null
  formDenied.value = null
  unknownOutcome.value = false
  submitLocked.value = false
  lockedPayload.value = ''
  saved.value = null
  listLoaded.value = false
  refreshing.value = false
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
  submitLocked.value = false
  lockedPayload.value = ''
  saved.value = null
  unknownOutcome.value = false
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  catalogInFlight = false
  catalogStarted.value = false
  selectedItemId.value = ''
  serialText.value = ''
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

async function loadAssetsPage(gen: number): Promise<boolean> {
  const s = session
  if (!s) return false
  try {
    const list = await apiFetch<{ items: AssetRow[] }>(s, `/api/workspaces/${wsId.value}/assets?limit=${PAGE_LIMIT}`)
    // Penyelesaian basi mengembalikan false: pemanggil tidak boleh
    // memulai permintaan baru (mis. GET katalog) dari konteks yang sudah
    // digantikan — permintaan baru itu akan menangkap generasi baru dan
    // 401-nya dapat membersihkan sesi pengganti.
    if (stale(gen)) return false
    items.value = list.data.items
    nextOffset.value = list.data.items.length
    hasMore.value = list.data.items.length === PAGE_LIMIT
    listLoaded.value = true
    return true
  } catch (err) {
    if (stale(gen)) return false
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return false
    }
    if (err instanceof ApiError && err.status === 403) {
      noPermission.value = true
      pageError.value = err.message
      return true
    }
    if (err instanceof ApiError) pageError.value = err.message
    else pageError.value = 'Terjadi kesalahan. Coba lagi.'
    return true
  }
}

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
  // Daftar unit gagal tidak menghalangi katalog: katalog independen dan
  // dimulai sekali per kunjungan workspace (kecuali sesi sudah mati).
  // Batas kepemilikan: penyelesaian basi tidak boleh meluncurkan GET
  // katalog — periksa lagi sesudah await, sebelum kelanjutan apa pun.
  const authAlive = await loadAssetsPage(gen)
  if (!stale(gen)) loading.value = false
  if (stale(gen)) return
  if (authAlive && !catalogStarted.value) {
    catalogStarted.value = true
    await loadCatalogPage()
  }
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

async function loadMore() {
  const s = session
  if (!s || loadingMore.value || !hasMore.value) return
  const gen = listGen
  const myLoad = ++loadMoreGen
  const wantOffset = nextOffset.value
  loadingMore.value = true
  loadMoreError.value = null
  try {
    const res = await apiFetch<{ items: AssetRow[] }>(s, `/api/workspaces/${wsId.value}/assets?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
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

// Muat ulang daftar (GET saja): dipakai sesudah simpan 201 dan oleh
// tombol "Muat ulang daftar" saat hasil tak diketahui. Kegagalan
// menampilkan reloadError tanpa menyembunyikan konfirmasi tersimpan dan
// tanpa klaim daftar kosong; katalog tidak ikut dimuat ulang.
async function refreshAssets(failureMessage: string) {
  const s = session
  if (!s) return
  const gen = ++listGen
  // Reset daftar yang koheren: muat-lebih lama dibatalkan kepemilikannya
  // dan flag-nya dibersihkan supaya paginasi baru tetap bisa dipakai.
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  reloadError.value = null
  refreshing.value = true
  try {
    const list = await apiFetch<{ items: AssetRow[] }>(s, `/api/workspaces/${wsId.value}/assets?limit=${PAGE_LIMIT}`)
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

// Hasil tak diketahui (jaringan/timeout/500/502, status sukses yang tak
// terduga, atau baris 201 yang cacat): pertahankan isian, kunci kirim
// biasa atas nilai yang sama, tanpa ulang otomatis.
function setUnknownOutcome(key: string, gen: number) {
  unknownOutcome.value = true
  submitLocked.value = true
  lockedPayload.value = key
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

async function submitCreate(resend = false) {
  const s = session
  if (!s || inFlight) return
  const itemId = selectedItemId.value
  const serial = trimGoSpace(serialText.value)
  const key = payloadKey(itemId, serial)
  if (!resend && submitLocked.value && key === lockedPayload.value) return
  itemError.value = null
  serialError.value = null
  formError.value = null
  formDenied.value = null
  if (itemId === '') {
    itemError.value = 'Pilih barang dulu'
    await nextTick()
    itemSelect.value?.focus()
    return
  }
  if (serial === '' || runeLen(serial) > SERIAL_MAX_RUNES) {
    serialError.value = serial === '' ? 'serial wajib diisi dan maksimal 64 karakter' : 'serial maksimal 64 karakter'
    await nextTick()
    serialInput.value?.focus()
    return
  }
  const gen = postGen
  // Kiriman baru yang lolos validasi: kunci lama dibuka, banner tak
  // diketahui ditutup, lalu POST dikirim.
  submitLocked.value = false
  lockedPayload.value = ''
  unknownOutcome.value = false
  inFlight = true
  busy.value = true
  try {
    const { status, data } = await apiFetch<AssetRow>(s, `/api/workspaces/${wsId.value}/assets`, {
      method: 'POST',
      body: { catalog_item_id: itemId, serial }
    })
    if (stalePost(gen)) return
    // Hanya 201 dengan baris yang valid yang mengonfirmasi simpan.
    if (status !== 201 || !isValidAssetRow(data, wsId.value, itemId, serial)) {
      setUnknownOutcome(key, gen)
      return
    }
    saved.value = data
    serialText.value = ''
    // Pilihan barang dipertahankan: mendaftarkan banyak unit untuk barang
    // yang sama adalah alur yang wajar; wilayah tersimpan menyebut
    // barangnya secara eksplisit.
    unknownOutcome.value = false
    submitLocked.value = false
    lockedPayload.value = ''
    items.value = []
    // Daftar dikosongkan untuk dimuat ulang: tandai belum termuat supaya
    // jeda refresh tidak ditampilkan sebagai "Belum ada unit.".
    listLoaded.value = false
    nextOffset.value = 0
    hasMore.value = false
    await nextTick()
    if (stalePost(gen)) return
    savedHeading.value?.focus()
    await refreshAssets('Data tersimpan, tetapi daftar gagal dimuat ulang. Coba muat ulang daftar.')
  } catch (err) {
    if (stalePost(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    // Kegagalan yang diketahui mengembalikan formulir ke keadaan koreksi
    // normal: kunci kirim ulang dibuka, isian dipertahankan.
    if (err instanceof ApiError && err.status === 400) {
      submitLocked.value = false
      lockedPayload.value = ''
      if (err.message === 'serial tidak valid') {
        serialError.value = err.message
        await nextTick()
        if (!stalePost(gen)) serialInput.value?.focus()
      } else if (err.message === 'catalog_item_id tidak valid') {
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
    if (err instanceof ApiError && err.status === 404) {
      // Barang hilang/asing: isian dipertahankan; pesan server tidak
      // membocorkan tenan lain.
      submitLocked.value = false
      lockedPayload.value = ''
      formError.value = err.message
      await nextTick()
      if (!stalePost(gen)) formAlert.value?.focus()
      return
    }
    if (err instanceof ApiError && err.status === 409) {
      submitLocked.value = false
      lockedPayload.value = ''
      if (err.message === SERIAL_TAKEN) {
        // Konflik serial menuju field Serial. 409 kirim ulang TIDAK
        // pernah mengonfirmasi/mengadopsi tulis pertama yang tak pasti.
        serialError.value = err.message
        await nextTick()
        if (!stalePost(gen)) serialInput.value?.focus()
      } else if (err.message === ITEM_INACTIVE) {
        // Barang tidak aktif adalah galat pilihan barang, bukan konflik
        // serial.
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
      submitLocked.value = false
      lockedPayload.value = ''
      formDenied.value = err.message
      await nextTick()
      if (!stalePost(gen)) formAlert.value?.focus()
      return
    }
    // 500/502/jaringan: hasil tidak diketahui, mungkin sudah tersimpan.
    setUnknownOutcome(key, gen)
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
    <h1>Unit</h1>
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
          <h2 id="add-heading">Tambah unit</h2>
          <p class="small muted">
            Penambahan memerlukan izin pengelolaan; izin diperiksa saat menyimpan.
          </p>
          <div v-if="formDenied" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formDenied }}</p>
          </div>
          <div v-else-if="formError" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <p>{{ formError }}</p>
          </div>
          <div v-if="unknownOutcome" ref="formAlert" class="alert" role="alert" tabindex="-1">
            <h3>Hasil penyimpanan tidak diketahui</h3>
            <p>
              Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
              sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Periksa daftar
              sebelum mengirim lagi — pengiriman ulang dapat membuat duplikat.
            </p>
            <p>
              <button type="button" class="btn btn-secondary" @click="refreshAssets('Daftar gagal dimuat ulang. Coba muat ulang daftar.')">Muat ulang daftar</button>
              <button type="button" class="btn btn-secondary" @click="resendCreate">Kirim lagi (berisiko duplikat)</button>
            </p>
          </div>
          <form @submit.prevent="submitCreate()" novalidate>
            <div class="field">
              <label for="asset-item">Barang</label>
              <select
                id="asset-item"
                ref="itemSelect"
                v-model="selectedItemId"
                :disabled="catalogActiveChoices.length === 0"
                :aria-invalid="itemError !== null"
                :aria-describedby="itemError ? 'asset-item-error' : undefined"
              >
                <option value="">Pilih barang</option>
                <option v-for="item in catalogActiveChoices" :key="item.id" :value="item.id">{{ item.sku }} — {{ item.name }}</option>
                <option v-if="selectedStaleLabel" :value="selectedItemId" disabled>{{ selectedStaleLabel }}</option>
              </select>
              <p v-if="itemError" id="asset-item-error" class="field-error">{{ itemError }}</p>
              <p v-else class="hint">Barang dipilih dari katalog workspace; hanya barang aktif yang bisa dipilih.</p>
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
                <p v-else-if="catalogExhausted && catalogActiveChoices.length === 0" class="hint">
                  Semua barang yang dimuat sudah tidak aktif; tidak ada barang yang bisa dipilih.
                  <NuxtLink :to="`/workspaces/${wsId}/catalog`">Lihat halaman Barang</NuxtLink>.
                </p>
                <p v-else-if="!catalogExhausted && catalogRows.length > 0 && catalogActiveChoices.length === 0" class="hint">
                  Belum ada barang aktif yang dimuat — muat lebih banyak barang untuk melihat pilihan lain.
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
            <div class="field">
              <label for="asset-serial">Serial</label>
              <input
                id="asset-serial"
                ref="serialInput"
                v-model="serialText"
                type="text"
                autocomplete="off"
                :aria-invalid="serialError !== null"
                :aria-describedby="serialError ? 'asset-serial-error' : undefined"
              >
              <p v-if="serialError" id="asset-serial-error" class="field-error">{{ serialError }}</p>
              <p v-else class="hint">Maksimal 64 karakter. Huruf besar dan kecil dibedakan.</p>
            </div>
            <button type="submit" class="btn" :disabled="busy || submitBlocked">
              {{ busy ? 'Menyimpan…' : 'Simpan unit' }}
            </button>
          </form>

          <div v-if="saved" class="card alert-ok" aria-labelledby="saved-heading">
            <h3 id="saved-heading" ref="savedHeading" tabindex="-1">Unit tersimpan</h3>
            <p>
              <strong>{{ saved.serial }}</strong> untuk {{ savedLabel }} sudah tersimpan.
            </p>
          </div>
        </section>

        <section class="card" aria-labelledby="list-heading">
          <h2 id="list-heading">Daftar unit</h2>
          <p v-if="reloadError" class="alert" role="alert">
            {{ reloadError }}
            <button type="button" class="btn btn-secondary" @click="refreshAssets(reloadError)">Muat ulang daftar</button>
          </p>
          <p v-if="refreshing" role="status">Memuat daftar…</p>
          <p v-else-if="items.length === 0 && listLoaded && !reloadError" class="muted">Belum ada unit.</p>
          <ul v-else class="plain">
            <li v-for="a in items" :key="a.id"><strong>{{ a.serial }}</strong> — {{ catalogLabel(a.catalog_item_id) }}</li>
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
