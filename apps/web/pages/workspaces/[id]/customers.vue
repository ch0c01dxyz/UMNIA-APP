<script setup lang="ts">
// Pelanggan workspace (GET/POST /workspaces/{id}/customers).
// LIST+CREATE saja; tanpa edit/hapus/arsip/cari. Server otoritatif:
// baca butuh view_workspace, simpan butuh manage_business_data; kasir
// melihat formulir dan menerima 403 saat simpan (bukan disembunyikan).
// Hanya 201 dengan baris yang valid yang mengonfirmasi simpan; create
// TIDAK idempotent, jadi tidak ada ulang otomatis dan hasil tak
// diketahui ditangani eksplisit (R1).
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Pelanggan — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface CustomerRow { id: string, workspace_id: string, type: string, display_name: string, created_at: string, updated_at: string }

const PAGE_LIMIT = 50
const NAME_MAX_RUNES = 200
const NAME_ERROR = 'display_name wajib diisi dan maksimal 200 karakter'
const runeLen = (s: string) => [...s].length

// strings.TrimSpace Go: JS trim berbeda — Go memangkas U+0085 tapi TIDAK
// memangkas U+FEFF; JS sebaliknya. Samakan dengan Go sebelum validasi
// panjang (rune) dan pengiriman, supaya server menerima yang klien
// nyatakan valid dan identitas nilai tidak berubah diam-diam.
const GO_TRIM_RE = /^[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+|[\t\n\v\f\r \u0085\u00A0\u1680\u2000-\u200A\u2028\u2029\u202F\u205F\u3000]+$/g
const trimGoSpace = (s: string) => s.replace(GO_TRIM_RE, '')

// Baris tersimpan minimum: id ada, workspace cocok, display_name string
// dan sama dengan yang dikirim (server hanya trim, tanpa normalisasi lain).
function isValidCustomerRow(row: unknown, ws: string, name: string): row is CustomerRow {
  return !!row && typeof row === 'object' &&
    typeof (row as CustomerRow).id === 'string' && (row as CustomerRow).id !== '' &&
    (row as CustomerRow).workspace_id === ws &&
    (row as CustomerRow).display_name === name
}

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const workspace = ref<Workspace | null>(null)
const items = ref<CustomerRow[]>([])
const nextOffset = ref(0)
const hasMore = ref(false)
const loadingMore = ref(false)
const loadMoreError = ref<string | null>(null)
const reloadError = ref<string | null>(null)

const displayName = ref('')
const nameError = ref<string | null>(null)
const formError = ref<string | null>(null)
const formDenied = ref<string | null>(null)
const unknownOutcome = ref(false)
// Kunci kirim ulang (R1-1): sesudah hasil tak diketahui, kirim biasa
// dengan nilai yang sama tidak boleh POST lagi; hanya tombol "Kirim lagi
// (berisiko duplikat)" yang boleh. Nilai yang diubah adalah kiriman baru.
const submitLocked = ref(false)
const lockedPayload = ref('')
const submitBlocked = computed(() => submitLocked.value && trimGoSpace(displayName.value) === lockedPayload.value)
const busy = ref(false)
const saved = ref<CustomerRow | null>(null)
const savedHeading = ref<HTMLElement | null>(null)
const formAlert = ref<HTMLElement | null>(null)
const nameInput = ref<HTMLInputElement | null>(null)

// Penjaga generasi: hasil muat yang tiba sesudah reset/create/pindah
// workspace/logout diabaikan; muat-lebih lama tidak boleh menempel ke
// prefix baru. inFlight dijaga sinkron (bukan hanya tombol disabled).
// postGen: kepemilikan POST — kelanjutan POST basi (sukses, galat,
// nextTick, refresh) tidak boleh menyentuh halaman/sesi/daftar pengganti.
// loadMoreGen: kepemilikan flag loadingMore — finally basi tidak boleh
// menghapus busy milik permintaan baru, dan reset daftar membersihkannya.
let listGen = 0
let postGen = 0
let loadMoreGen = 0
let inFlight = false

function stale(gen: number) {
  return gen !== listGen
}

function stalePost(gen: number) {
  return gen !== postGen
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
  loadingMore.value = false
  loadMoreError.value = null
  submitLocked.value = false
  lockedPayload.value = ''
  saved.value = null
  unknownOutcome.value = false
  await load()
})

onUnmounted(() => {
  listGen++
  postGen++
  loadMoreGen++
})

async function load() {
  const s = session
  if (!s) return
  const gen = ++listGen
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  loading.value = true
  pageError.value = null
  noPermission.value = false
  reloadError.value = null
  try {
    const ws = await apiFetch<Workspace>(s, `/api/workspaces/${wsId.value}`)
    if (stale(gen)) return
    workspace.value = ws.data
    const list = await apiFetch<{ items: CustomerRow[] }>(s, `/api/workspaces/${wsId.value}/customers?limit=${PAGE_LIMIT}`)
    if (stale(gen)) return
    items.value = list.data.items
    nextOffset.value = list.data.items.length
    hasMore.value = list.data.items.length === PAGE_LIMIT
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      noPermission.value = true
      pageError.value = err.message
      return
    }
    if (err instanceof ApiError) pageError.value = err.message
    else pageError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (!stale(gen)) loading.value = false
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
    const res = await apiFetch<{ items: CustomerRow[] }>(s, `/api/workspaces/${wsId.value}/customers?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
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
    if (err instanceof ApiError) loadMoreError.value = err.message
    else loadMoreError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    // Hanya pemilik flag saat ini yang boleh mematikannya; finally basi
    // tidak boleh mematikan busy milik muat-lebih baru.
    if (myLoad === loadMoreGen) loadingMore.value = false
  }
}

async function refreshListAfterSave() {
  const s = session
  if (!s) return
  const gen = ++listGen
  // Reset daftar yang koheren: muat-lebih lama dibatalkan kepemilikannya
  // dan flag-nya dibersihkan supaya paginasi baru tetap bisa dipakai.
  loadMoreGen++
  loadingMore.value = false
  loadMoreError.value = null
  reloadError.value = null
  try {
    const list = await apiFetch<{ items: CustomerRow[] }>(s, `/api/workspaces/${wsId.value}/customers?limit=${PAGE_LIMIT}`)
    if (stale(gen)) return
    items.value = list.data.items
    nextOffset.value = list.data.items.length
    hasMore.value = list.data.items.length === PAGE_LIMIT
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    reloadError.value = 'Data tersimpan, tetapi daftar gagal dimuat ulang. Coba muat ulang daftar.'
  }
}

// Hasil tak diketahui (jaringan/timeout/500/502, status sukses yang tak
// terduga, atau baris 201 yang cacat): pertahankan isian, kunci kirim
// biasa atas nilai yang sama, tanpa ulang otomatis.
function setUnknownOutcome(payload: string, gen: number) {
  unknownOutcome.value = true
  submitLocked.value = true
  lockedPayload.value = payload
  nextTick().then(() => {
    if (!stalePost(gen)) formAlert.value?.focus()
  })
}

async function submitCreate(resend = false) {
  const s = session
  if (!s || inFlight) return
  const name = trimGoSpace(displayName.value)
  if (!resend && submitLocked.value && name === lockedPayload.value) return
  nameError.value = null
  formError.value = null
  formDenied.value = null
  if (name === '' || runeLen(name) > NAME_MAX_RUNES) {
    nameError.value = NAME_ERROR
    await nextTick()
    nameInput.value?.focus()
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
    const { status, data } = await apiFetch<CustomerRow>(s, `/api/workspaces/${wsId.value}/customers`, {
      method: 'POST',
      body: { display_name: name }
    })
    if (stalePost(gen)) return
    // Hanya 201 dengan baris yang valid yang mengonfirmasi simpan.
    if (status !== 201 || !isValidCustomerRow(data, wsId.value, name)) {
      setUnknownOutcome(name, gen)
      return
    }
    saved.value = data
    displayName.value = ''
    unknownOutcome.value = false
    submitLocked.value = false
    lockedPayload.value = ''
    items.value = []
    nextOffset.value = 0
    hasMore.value = false
    await nextTick()
    if (stalePost(gen)) return
    savedHeading.value?.focus()
    await refreshListAfterSave()
  } catch (err) {
    if (stalePost(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 400) {
      // Hanya pesan field yang dikenal yang diasosiasikan ke field;
      // 400 gabungan/tak terstruktur memakai alert level formulir.
      // Kegagalan yang diketahui mengembalikan formulir ke keadaan
      // koreksi normal: kunci kirim ulang dibuka.
      submitLocked.value = false
      lockedPayload.value = ''
      if (err.message === NAME_ERROR) {
        nameError.value = err.message
        await nextTick()
        if (!stalePost(gen)) nameInput.value?.focus()
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
    setUnknownOutcome(name, gen)
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
    <h1>Pelanggan</h1>
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
        </nav>

        <section class="card" aria-labelledby="add-heading">
          <h2 id="add-heading">Tambah pelanggan</h2>
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
              <button type="button" class="btn btn-secondary" @click="load">Muat ulang daftar</button>
              <button type="button" class="btn btn-secondary" @click="resendCreate">Kirim lagi (berisiko duplikat)</button>
            </p>
          </div>
          <form @submit.prevent="submitCreate()" novalidate>
            <div class="field">
              <label for="customer-name">Nama pelanggan</label>
              <input
                id="customer-name"
                ref="nameInput"
                v-model="displayName"
                type="text"
                autocomplete="off"
                :aria-invalid="nameError !== null"
                :aria-describedby="nameError ? 'customer-name-error' : undefined"
              >
              <p v-if="nameError" id="customer-name-error" class="field-error">{{ nameError }}</p>
              <p v-else class="hint">Maksimal 200 karakter. Nama yang sama boleh dipakai lebih dari sekali.</p>
            </div>
            <button type="submit" class="btn" :disabled="busy || submitBlocked">
              {{ busy ? 'Menyimpan…' : 'Simpan pelanggan' }}
            </button>
          </form>

          <div v-if="saved" class="card alert-ok" aria-labelledby="saved-heading">
            <h3 id="saved-heading" ref="savedHeading" tabindex="-1">Pelanggan tersimpan</h3>
            <p>
              <strong>{{ saved.display_name }}</strong> sudah tersimpan.
            </p>
          </div>
        </section>

        <section class="card" aria-labelledby="list-heading">
          <h2 id="list-heading">Daftar pelanggan</h2>
          <p v-if="reloadError" class="alert" role="alert">
            {{ reloadError }}
            <button type="button" class="btn btn-secondary" @click="refreshListAfterSave">Muat ulang daftar</button>
          </p>
          <p v-if="items.length === 0 && !reloadError" class="muted">Belum ada pelanggan.</p>
          <ul v-else class="plain">
            <li v-for="c in items" :key="c.id">{{ c.display_name }}</li>
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
