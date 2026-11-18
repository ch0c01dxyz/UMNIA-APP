<script setup lang="ts">
// Fitur workspace (GET katalog modul + GET state rental.basic).
// Hanya rental.basic yang dapat diaktifkan di slice ini, dengan versi
// katalog aktual. API tetap otoritatif: 403 tampil sebagai penolakan
// yang membantu, bukan formulir grant.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Fitur workspace — UMNIA' })

interface Manifest { id: string, version: string, display: { name_id: string, description_id: string } }
interface ModuleState { module_id: string, version: string | null, status: string, activated_at: string | null, deactivated_at: string | null }

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const catalog = ref<Manifest[]>([])
const rentalManifest = computed(() => catalog.value.find(m => m.id === 'rental.basic') ?? null)
const rentalState = ref<ModuleState | null>(null)
const confirming = ref(false)
const activating = ref(false)
const activateError = ref<string | null>(null)
const activateDenied = ref(false)

onMounted(async () => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  await load()
})

async function load() {
  const s = session
  if (!s) return
  loading.value = true
  pageError.value = null
  noPermission.value = false
  activateError.value = null
  activateDenied.value = false
  try {
    const list = await apiFetch<{ modules: Manifest[] }>(s, `/api/workspaces/${wsId.value}/modules`)
    catalog.value = list.data.modules
    const state = await apiFetch<ModuleState>(s, `/api/workspaces/${wsId.value}/modules/rental.basic`)
    rentalState.value = state.data
  } catch (err) {
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
    loading.value = false
  }
}

async function activate() {
  const s = session
  const manifest = rentalManifest.value
  if (!s || !manifest) return
  activating.value = true
  activateError.value = null
  activateDenied.value = false
  try {
    await apiFetch<ModuleState>(s, `/api/workspaces/${wsId.value}/modules/rental.basic/activate`, {
      method: 'POST',
      body: { version: manifest.version }
    })
    // Tampilkan state hasil GET aktual, bukan hanya respons aktivasi.
    const state = await apiFetch<ModuleState>(s, `/api/workspaces/${wsId.value}/modules/rental.basic`)
    rentalState.value = state.data
    confirming.value = false
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      if (err.message.includes('belum menjadi hak')) {
        activateDenied.value = true
      } else {
        activateError.value = err.message
      }
      return
    }
    if (err instanceof ApiError) activateError.value = err.message
    else activateError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    activating.value = false
  }
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Fitur workspace</h1>
    <p v-if="checking">Memeriksa sesi masuk…</p>
    <template v-else>
      <p v-if="loading" role="status">Memuat…</p>
      <div v-else-if="noPermission" class="alert" role="alert">
        <h2>Tidak ada izin</h2>
        <p>{{ pageError }}</p>
        <p>
          <NuxtLink :to="`/workspaces/${wsId}/members`">Kembali ke anggota workspace</NuxtLink>
        </p>
      </div>
      <div v-else-if="pageError" class="alert" role="alert">
        <h2>Ada masalah</h2>
        <p>{{ pageError }}</p>
        <p><button type="button" class="btn btn-secondary" @click="load">Coba lagi</button></p>
      </div>
      <template v-else>
        <nav class="site-nav page-nav" aria-label="Navigasi workspace">
          <NuxtLink :to="`/workspaces/${wsId}/members`">Anggota</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Fitur</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/customers`">Pelanggan</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/catalog`">Barang</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/assets`">Unit</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/prices`">Tarif</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/rentals`">Draf sewa</NuxtLink>
        </nav>
        <section class="card" aria-labelledby="rental-heading">
          <template v-if="rentalManifest && rentalState">
            <h2 id="rental-heading">{{ rentalManifest.display.name_id }}</h2>
            <p>{{ rentalManifest.display.description_id }}</p>
            <p class="small muted">Versi katalog: {{ rentalManifest.version }}</p>
            <p v-if="rentalState.status === 'active'" class="ok" role="status">
              Aktif (versi {{ rentalState.version }}).
            </p>
            <template v-else>
              <p role="status">Belum aktif.</p>
              <div v-if="activateDenied" class="alert" role="alert">
                <p>Fitur ini belum tersedia untuk usaha Anda; hubungi pengelola layanan.</p>
              </div>
              <div v-if="activateError" class="alert" role="alert">
                <p>{{ activateError }}</p>
              </div>
              <template v-if="!confirming">
                <button type="button" class="btn" @click="confirming = true">
                  Aktifkan {{ rentalManifest.display.name_id }}
                </button>
              </template>
              <div v-else class="card">
                <h3>Periksa sebelum mengaktifkan</h3>
                <p>
                  Mengaktifkan <strong>{{ rentalManifest.display.name_id }}</strong>
                  versi {{ rentalManifest.version }} untuk workspace ini.
                  {{ rentalManifest.display.description_id }}.
                </p>
                <p>
                  <button type="button" class="btn" :disabled="activating" @click="activate">
                    {{ activating ? 'Memproses…' : 'Ya, aktifkan' }}
                  </button>
                  <button type="button" class="btn btn-secondary" :disabled="activating" @click="confirming = false">
                    Batal
                  </button>
                </p>
              </div>
            </template>
          </template>
          <p v-else class="muted">Fitur Persewaan Sederhana tidak ada di katalog.</p>
        </section>
        <section class="card" aria-labelledby="other-heading">
          <h2 id="other-heading">Fitur lain</h2>
          <ul class="plain">
            <li v-for="m in catalog.filter(x => x.id !== 'rental.basic')" :key="m.id">
              <strong>{{ m.display.name_id }}</strong> (versi {{ m.version }}) — {{ m.display.description_id }}
              <br>
              <span class="small muted">Modul ini tidak dapat diaktifkan dari halaman ini.</span>
            </li>
          </ul>
          <p v-if="catalog.length === 0" class="muted">Katalog kosong.</p>
        </section>
      </template>
    </template>
  </main>
</template>
