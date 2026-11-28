<script setup lang="ts">
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Fitur workspace — UMNIA' })

interface Manifest { id: string, version: string, display: { name_id: string, description_id: string } }
interface ModuleState { module_id: string, version: string | null, status: 'active' | 'inactive' }
interface CardState { installation: ModuleState | null, loading: boolean, readError: string | null, activationError: string | null }

const activationModules = ['rental.basic', 'finance.basic'] as const
type ActivationModule = typeof activationModules[number]
const emptyCard = (): CardState => ({ installation: null, loading: false, readError: null, activationError: null })

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const catalog = ref<Manifest[]>([])
const cards = reactive<Record<ActivationModule, CardState>>({ 'rental.basic': emptyCard(), 'finance.basic': emptyCard() })
const availableCards = computed(() => activationModules.flatMap(id => {
  const manifest = catalog.value.find(item => item.id === id)
  return manifest ? [{ id, manifest, card: cards[id] }] : []
}))
const otherModules = computed(() => catalog.value.filter(item => !activationModules.some(id => id === item.id)))
const confirming = ref<ActivationModule | null>(null)
const activating = ref<ActivationModule | null>(null)
let generation = 0

onMounted(() => {
  watch([wsId, () => session?.token.value], load, { immediate: true })
})
onBeforeUnmount(() => { generation++ })

function context() {
  const s = session
  const token = s?.token.value
  const workspace = wsId.value
  const gen = generation
  return s && token ? {
    s,
    workspace,
    current: () => gen === generation && workspace === wsId.value && token === s.token.value
  } : null
}

function errorMessage(err: unknown) {
  return err instanceof ApiError ? err.message : 'Terjadi kesalahan. Coba lagi.'
}

async function load() {
  generation++
  const ctx = context()
  if (!ctx) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  loading.value = true
  pageError.value = null
  noPermission.value = false
  confirming.value = null
  activating.value = null
  catalog.value = []
  for (const id of activationModules) cards[id] = emptyCard()
  try {
    const list = await apiFetch<{ modules: Manifest[] }>(ctx.s, `/api/workspaces/${ctx.workspace}/modules`)
    if (!ctx.current()) return
    catalog.value = list.data.modules
    for (const { id } of availableCards.value) void readState(id)
  } catch (err) {
    if (!ctx.current()) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(ctx.s, route.fullPath)
      return
    }
    noPermission.value = err instanceof ApiError && err.status === 403
    pageError.value = errorMessage(err)
  } finally {
    if (ctx.current()) loading.value = false
  }
}

async function readState(id: ActivationModule) {
  const ctx = context()
  const card = cards[id]
  if (!ctx || card.loading) return
  card.loading = true
  card.readError = null
  card.installation = null
  try {
    const { data } = await apiFetch<ModuleState>(ctx.s, `/api/workspaces/${ctx.workspace}/modules/${id}`)
    if (!ctx.current()) return
    if (data?.module_id !== id || (data.status !== 'inactive' && data.status !== 'active') ||
      (data.status === 'active' && (typeof data.version !== 'string' || !data.version))) {
      card.readError = 'Status fitur tidak dapat dibaca.'
      return
    }
    card.installation = data
    card.activationError = null
  } catch (err) {
    if (!ctx.current()) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(ctx.s, route.fullPath)
      return
    }
    card.readError = errorMessage(err)
  } finally {
    if (ctx.current()) card.loading = false
  }
}

async function retryState(id: ActivationModule) {
  const ctx = context()
  if (!ctx) return
  await readState(id)
  await nextTick()
  if (!ctx.current() || document.activeElement !== document.body) return
  const target = cards[id].readError ? `retry-${id}` : `status-${id}`
  document.getElementById(target)?.focus()
}

async function changeConfirmation(id: ActivationModule, open: boolean) {
  const ctx = context()
  if (!ctx || activating.value !== null || (open && cards[id].installation?.status !== 'inactive')) return
  confirming.value = open ? id : null
  cards[id].activationError = null
  await nextTick()
  if (ctx.current()) document.getElementById(`${open ? 'confirm' : 'activate'}-${id}`)?.focus()
}

async function activate(id: ActivationModule) {
  const ctx = context()
  const manifest = availableCards.value.find(item => item.id === id)?.manifest
  const card = cards[id]
  if (!ctx || !manifest || card.installation?.status !== 'inactive' || confirming.value !== id || activating.value !== null) return
  activating.value = id
  card.activationError = null
  try {
    try {
      await apiFetch<ModuleState>(ctx.s, `/api/workspaces/${ctx.workspace}/modules/${id}/activate`, {
        method: 'POST', body: { version: manifest.version }
      })
    } catch (err) {
      if (!ctx.current()) return
      if (err instanceof ApiError && err.status === 401) {
        redirectToLogin(ctx.s, route.fullPath)
        return
      }
      if (err instanceof ApiError && [400, 403, 404, 409, 422].includes(err.status)) {
        card.activationError = err.status === 403 && err.message.includes('belum menjadi hak')
          ? 'Fitur ini belum tersedia untuk usaha Anda; hubungi pengelola layanan.' : err.message
        return
      }
      // The write may have reached the server. Recover with a read, never a second POST.
    }
    if (!ctx.current()) return
    confirming.value = null
    await readState(id)
    if (!ctx.current()) return
    const observed = cards[id].installation
    if (observed?.status !== 'active' || observed.version !== manifest.version) {
      card.activationError = 'Hasil aktivasi belum dapat dipastikan. Periksa status fitur sebelum mencoba lagi.'
    }
  } finally {
    if (ctx.current()) {
      activating.value = null
      await nextTick()
      if (ctx.current()) document.getElementById(`status-${id}`)?.focus()
    }
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
        <section v-for="{ id, manifest, card } in availableCards" :key="id" class="card" :aria-labelledby="`heading-${id}`">
          <h2 :id="`heading-${id}`">{{ manifest.display.name_id }}</h2>
          <p>{{ manifest.display.description_id }}</p>
          <p class="small muted">Versi katalog: {{ manifest.version }}</p>
          <p v-if="card.activationError" :id="`status-${id}`" tabindex="-1" class="alert" role="alert">{{ card.activationError }}</p>
          <p v-if="card.loading" role="status">Memuat status fitur…</p>
          <div v-else-if="card.readError" class="alert" :role="card.activationError ? undefined : 'alert'">
            <p>{{ card.readError }}</p>
            <button :id="`retry-${id}`" type="button" class="btn btn-secondary" :disabled="activating !== null" @click="retryState(id)">
              Muat ulang status {{ manifest.display.name_id }}
            </button>
          </div>
          <template v-else-if="card.installation">
            <p v-if="card.installation.status === 'active'" :id="card.activationError ? undefined : `status-${id}`" tabindex="-1" class="ok" role="status">
              {{ id === 'rental.basic' ? `Aktif (versi ${card.installation.version}).` : `Keuangan Sederhana aktif · versi ${card.installation.version}` }}
            </p>
            <template v-else>
              <p :id="card.activationError ? undefined : `status-${id}`" tabindex="-1" role="status">
                {{ id === 'rental.basic' ? 'Belum aktif.' : 'Keuangan Sederhana belum diaktifkan.' }}
              </p>
              <template v-if="confirming !== id">
                <button :id="`activate-${id}`" type="button" class="btn" :disabled="activating !== null" @click="changeConfirmation(id, true)">
                  Aktifkan {{ manifest.display.name_id }}
                </button>
              </template>
              <div v-else class="card">
                <h3 :id="`confirm-${id}`" tabindex="-1">Periksa sebelum mengaktifkan</h3>
                <p>
                  Mengaktifkan <strong>{{ manifest.display.name_id }}</strong>
                  versi {{ manifest.version }} untuk workspace ini.
                </p>
                <p v-if="id === 'finance.basic'">Staf yang berwenang dapat mengusulkan pembayaran. Persetujuan tetap memerlukan orang lain yang berwenang.</p>
                <p>
                  <button type="button" class="btn" :disabled="activating !== null" @click="activate(id)">
                    {{ activating === id ? 'Memproses…' : 'Ya, aktifkan' }}
                  </button>
                  <button type="button" class="btn btn-secondary" :disabled="activating !== null" @click="changeConfirmation(id, false)">
                    Batal
                  </button>
                </p>
              </div>
            </template>
          </template>
        </section>
        <section class="card" aria-labelledby="other-heading">
          <h2 id="other-heading">Fitur lain</h2>
          <ul class="plain">
            <li v-for="m in otherModules" :key="m.id">
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
