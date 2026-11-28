<script setup lang="ts">
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Stok — UMNIA' })

interface CatalogItem { id: string, sku: string, name: string, unit: string, status: string }
interface Stock { catalog_item_id: string, sku: string, name: string, unit: string, quantity: string }
interface Movement {
  id: string
  catalog_item_id: string
  unit: string
  kind: 'receipt' | 'issue' | 'reversal'
  delta: string
  source_reference: string
  reason?: string
  reverses_id?: string
  reversed?: boolean
  recorded_at: string
}
interface MovementInput {
  catalog_item_id: string
  kind: Movement['kind']
  source_reference: string
  quantity?: string
  reason?: string
  reverses_id?: string
}
interface Pending { body: MovementInput, key: string, item: string, quantity: string, uncertain: boolean }

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))
const root = computed(() => `/api/workspaces/${wsId.value}`)
const pageSize = 50
const maxQuantity = '9223372036854775807'
const labels = { receipt: 'Barang masuk', issue: 'Barang keluar', reversal: 'Pembalikan' }
const dateFormat = new Intl.DateTimeFormat('id-ID', { dateStyle: 'medium', timeStyle: 'short' })
let generation = 0

const stock = ref<Stock[]>([])
const history = ref<Movement[]>([])
const catalog = ref<CatalogItem[]>([])
const sections = reactive({
  stock: { loading: false, error: '', more: false },
  history: { loading: false, error: '', more: false },
  catalog: { loading: false, error: '', more: false }
})
type Section = keyof typeof sections
const access = ref(false)
const canPost = ref(false)
const canReverse = ref(false)
const selected = ref('')
const kind = ref<'receipt' | 'issue'>('receipt')
const quantity = ref('')
const reference = ref('')
const reason = ref('')
const original = ref<Movement | null>(null)
const pending = ref<Pending | null>(null)
const saving = ref(false)
const formError = ref('')
const message = ref('')
const result = ref<HTMLElement | null>(null)
const review = ref<HTMLElement | null>(null)
const itemField = ref<HTMLElement | null>(null)
const referenceField = ref<HTMLElement | null>(null)
let opener: HTMLElement | null = null

const movementReferences = computed(() => new Map(history.value.map(movement => [movement.id, movement.source_reference])))
const itemNames = computed(() => new Map([
  ...stock.value.map(item => [item.catalog_item_id, item.name] as const),
  ...catalog.value.map(item => [item.id, item.name] as const)
]))

onMounted(() => watch([wsId, () => session?.token.value], reset, { immediate: true }))
onBeforeUnmount(() => { generation++ })

function context() {
  const token = session?.token.value
  const path = root.value
  const gen = generation
  return session && token ? {
    session, path, current: () => generation === gen && path === root.value && session.token.value === token
  } : null
}

function reset() {
  generation++
  stock.value = []
  history.value = []
  catalog.value = []
  for (const state of Object.values(sections)) Object.assign(state, { loading: false, error: '', more: false })
  access.value = canPost.value = canReverse.value = saving.value = false
  pending.value = original.value = null
  opener = null
  kind.value = 'receipt'
  selected.value = quantity.value = reference.value = reason.value = formError.value = message.value = ''
  if (!context()) {
    void navigateTo(loginUrl(route.fullPath))
    return
  }
  void load('stock')
  void load('history')
  void load('catalog')
}

async function load(section: Section, append = false) {
  const ctx = context()
  const state = sections[section]
  if (!ctx || state.loading) return
  const offset = append ? (section === 'stock' ? stock.value.length : section === 'history' ? history.value.length : catalog.value.length) : 0
  const query = `?limit=${pageSize}&offset=${offset}`
  state.loading = true
  state.error = ''
  try {
    if (section === 'stock') {
      const { data } = await apiFetch<{ items: Stock[], can_post: boolean, can_reverse: boolean }>(ctx.session, `${ctx.path}/inventory/stock${query}`)
      if (!ctx.current()) return
      stock.value = append ? [...stock.value, ...data.items] : data.items
      access.value = true
      canPost.value = data.can_post
      canReverse.value = data.can_reverse
      state.more = data.items.length === pageSize
    } else if (section === 'history') {
      const { data } = await apiFetch<{ items: Movement[] }>(ctx.session, `${ctx.path}/inventory/movements${query}`)
      if (!ctx.current()) return
      history.value = append ? [...history.value, ...data.items] : data.items
      state.more = data.items.length === pageSize
    } else {
      const { data } = await apiFetch<{ items: CatalogItem[] }>(ctx.session, `${ctx.path}/catalog/items${query}`)
      if (!ctx.current()) return
      catalog.value = append ? [...catalog.value, ...data.items] : data.items
      state.more = data.items.length === pageSize
    }
  } catch (err) {
    if (!ctx.current()) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(ctx.session, route.fullPath)
      return
    }
    if (section === 'stock') access.value = false
    state.error = err instanceof ApiError ? err.message : 'Data stok gagal dibaca.'
  } finally {
    if (ctx.current()) state.loading = false
  }
}

async function openReversal(movement: Movement, event: MouseEvent) {
  opener = event.currentTarget as HTMLElement
  original.value = movement
  reference.value = reason.value = formError.value = message.value = ''
  await nextTick()
  referenceField.value?.focus()
}

async function cancelReversal() {
  original.value = null
  reference.value = reason.value = formError.value = ''
  await nextTick()
  opener?.focus()
  opener = null
}

async function prepare() {
  if (!access.value || pending.value) return
  formError.value = message.value = ''
  const movement = original.value
  const item = movement
    ? { id: movement.catalog_item_id, name: itemNames.value.get(movement.catalog_item_id) ?? movement.catalog_item_id, unit: movement.unit }
    : catalog.value.find(item => item.id === selected.value)
  if (!item || !reference.value.trim() || (movement && !reason.value.trim())) {
    formError.value = 'Lengkapi barang, referensi sumber, dan alasan jika diperlukan.'
    return
  }
  if (!movement && (!/^[1-9][0-9]{0,18}$/.test(quantity.value) || (quantity.value.length === maxQuantity.length && quantity.value > maxQuantity))) {
    formError.value = 'Jumlah harus berupa bilangan bulat positif dalam batas stok.'
    return
  }
  const body: MovementInput = { catalog_item_id: item.id, kind: movement ? 'reversal' : kind.value, source_reference: reference.value.trim() }
  if (movement) {
    body.reverses_id = movement.id
    body.reason = reason.value.trim()
  } else {
    body.quantity = quantity.value
  }
  if (typeof globalThis.crypto?.randomUUID !== 'function') {
    formError.value = 'Browser memerlukan koneksi aman untuk membuat kunci pengiriman.'
    return
  }
  const unit = stock.value.find(entry => entry.catalog_item_id === item.id)?.unit ?? item.unit
  pending.value = {
    body, key: crypto.randomUUID(), item: item.name,
    quantity: `${movement ? movement.delta.replace(/^-/, '') : quantity.value} ${unit}`,
    uncertain: false
  }
  await nextTick()
  review.value?.focus()
}

async function cancelReview() {
  if (pending.value?.uncertain || saving.value) return
  pending.value = null
  await nextTick()
  const field = original.value ? referenceField.value : itemField.value
  field?.focus()
}

async function save() {
  const request = pending.value
  const ctx = context()
  if (!ctx || !request || saving.value) return
  saving.value = true
  formError.value = ''
  try {
    const { data } = await apiFetch<Movement>(ctx.session, `${ctx.path}/inventory/movements`, {
      method: 'POST', body: request.body, idempotencyKey: request.key
    })
    if (!ctx.current()) return
    if (!data?.id || data.kind !== request.body.kind || data.catalog_item_id !== request.body.catalog_item_id) throw new Error()
    pending.value = original.value = null
    quantity.value = reference.value = reason.value = ''
    message.value = `${labels[data.kind]} berhasil dicatat.`
    await Promise.all([load('stock'), load('history')])
  } catch (err) {
    if (!ctx.current()) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(ctx.session, route.fullPath)
      return
    }
    if (err instanceof ApiError && [400, 403, 404, 409, 422].includes(err.status)) {
      if (request.uncertain) {
        formError.value = `${err.message} Periksa riwayat untuk memastikan hasil pengiriman pertama.`
      } else {
        pending.value = null
        formError.value = err.message
      }
    } else {
      if (pending.value) pending.value.uncertain = true
      formError.value = 'Hasil belum dapat dipastikan. Kirim ulang catatan yang sama atau periksa riwayat.'
    }
  } finally {
    if (ctx.current()) {
      saving.value = false
      await nextTick()
      if (ctx.current()) result.value?.focus()
    }
  }
}
</script>

<template>
  <main id="konten" class="page">
    <nav class="site-nav page-nav" aria-label="Navigasi workspace">
      <NuxtLink :to="`/workspaces/${wsId}/modules`">Fitur</NuxtLink>
      <NuxtLink :to="`/workspaces/${wsId}/catalog`">Barang</NuxtLink>
      <NuxtLink :to="`/workspaces/${wsId}/inventory`" aria-current="page">Stok</NuxtLink>
    </nav>
    <h1>Stok</h1>
    <p v-if="message || formError" ref="result" tabindex="-1" :role="formError ? 'alert' : 'status'" :class="formError ? 'alert' : 'ok'">
      {{ formError || message }}
    </p>

    <section v-if="pending || (access && (canPost || original))" class="card" aria-labelledby="post-stock">
      <h2 id="post-stock">{{ original ? 'Balik mutasi' : 'Catat mutasi' }}</h2>
      <div v-if="pending">
        <h3 ref="review" tabindex="-1">Periksa mutasi</h3>
        <dl>
          <dt>Jenis</dt><dd>{{ labels[pending.body.kind] }}</dd>
          <dt>Barang</dt><dd>{{ pending.item }}</dd>
          <dt>Jumlah</dt><dd>{{ pending.quantity }}</dd>
          <dt>Referensi sumber</dt><dd>{{ pending.body.source_reference }}</dd>
          <template v-if="pending.body.reason"><dt>Alasan</dt><dd>{{ pending.body.reason }}</dd></template>
        </dl>
        <p>Catatan yang tersimpan dapat dikoreksi melalui pembalikan.</p>
        <button type="button" class="btn" :disabled="saving || sections.stock.loading || sections.history.loading" @click="save">
          {{ saving ? 'Menyimpan…' : pending.uncertain ? 'Kirim ulang mutasi yang sama' : 'Simpan mutasi' }}
        </button>
        <button v-if="!pending.uncertain" type="button" class="btn btn-secondary" :disabled="saving" @click="cancelReview">Kembali mengedit</button>
      </div>
      <form v-else @submit.prevent="prepare">
        <p v-if="original">
          {{ itemNames.get(original.catalog_item_id) ?? original.catalog_item_id }} · {{ original.delta }} {{ original.unit }}
          · {{ original.source_reference }}
        </p>
        <template v-else>
          <div class="field">
            <label for="stock-item">Barang</label>
            <select id="stock-item" ref="itemField" v-model="selected" required>
              <option value="">Pilih barang</option>
              <option v-for="item in catalog.filter(item => item.status === 'active')" :key="item.id" :value="item.id">
                {{ item.name }} ({{ item.sku }}) · {{ item.unit }}
              </option>
            </select>
          </div>
          <p v-if="sections.catalog.error" role="alert">{{ sections.catalog.error }}</p>
          <button v-if="sections.catalog.more || sections.catalog.error" type="button" class="btn btn-secondary"
            :disabled="sections.catalog.loading" @click="load('catalog', catalog.length > 0)">Muat barang berikutnya</button>
          <p v-if="sections.catalog.loading" role="status">Memuat barang…</p>
          <p v-else-if="!catalog.length && !sections.catalog.error"><NuxtLink :to="`/workspaces/${wsId}/catalog`">Tambahkan barang ke katalog</NuxtLink></p>
          <div class="field">
            <label for="stock-kind">Jenis mutasi</label>
            <select id="stock-kind" v-model="kind">
              <option value="receipt">Barang masuk</option>
              <option value="issue">Barang keluar</option>
            </select>
          </div>
          <div class="field">
            <label for="stock-quantity">Jumlah bulat</label>
            <input id="stock-quantity" v-model="quantity" inputmode="numeric" maxlength="19" required>
          </div>
        </template>
        <div class="field">
          <label for="stock-reference">Referensi sumber</label>
          <input id="stock-reference" ref="referenceField" v-model="reference" maxlength="200" required>
        </div>
        <div v-if="original" class="field">
          <label for="stock-reason">Alasan pembalikan</label>
          <input id="stock-reason" v-model="reason" maxlength="500" required>
        </div>
        <p>
          <button class="btn" type="submit" :disabled="!original && !catalog.length">Periksa mutasi</button>
          <button v-if="original" type="button" class="btn btn-secondary" @click="cancelReversal">Batal membalik</button>
        </p>
      </form>
    </section>

    <section class="card" aria-labelledby="stock-balance">
      <h2 id="stock-balance">Saldo stok</h2>
      <p v-if="sections.stock.loading" role="status">Memuat saldo…</p>
      <p v-if="sections.stock.error" role="alert">{{ sections.stock.error }}</p>
      <NuxtLink v-if="sections.stock.error && !access" :to="`/workspaces/${wsId}/modules`">Buka pengaturan fitur</NuxtLink>
      <template v-if="access">
        <p v-if="!stock.length && !sections.stock.loading">Belum ada barang yang tercatat.</p>
        <ul class="records">
          <li v-for="item in stock" :key="item.catalog_item_id">
            <span>{{ item.name }} · {{ item.sku }}</span>
            <strong>{{ item.quantity }} {{ item.unit }}</strong>
          </li>
        </ul>
      </template>
      <button v-if="sections.stock.more || sections.stock.error" type="button" class="btn btn-secondary"
        :disabled="saving || sections.stock.loading" @click="load('stock', access && stock.length > 0)">
        {{ sections.stock.error ? 'Muat ulang saldo' : 'Muat saldo berikutnya' }}
      </button>
    </section>

    <section class="card" aria-labelledby="stock-history">
      <h2 id="stock-history">Riwayat mutasi</h2>
      <p v-if="sections.history.loading" role="status">Memuat riwayat…</p>
      <p v-if="sections.history.error" role="alert">{{ sections.history.error }}</p>
      <p v-if="!history.length && !sections.history.loading && !sections.history.error">Belum ada mutasi.</p>
      <ul class="records">
        <li v-for="movement in history" :id="`movement-${movement.id}`" :key="movement.id" tabindex="-1">
          <strong>{{ labels[movement.kind] }} · {{ movement.delta }} {{ movement.unit }}</strong>
          <span>{{ itemNames.get(movement.catalog_item_id) ?? movement.catalog_item_id }} · {{ movement.source_reference }}</span>
          <small><time :datetime="movement.recorded_at">{{ dateFormat.format(new Date(movement.recorded_at)) }}</time></small>
          <span v-if="movement.reason">{{ movement.reason }}</span>
          <span v-if="movement.reverses_id">
            Mutasi asal
            <a v-if="movementReferences.has(movement.reverses_id)" :href="`#movement-${movement.reverses_id}`">
              {{ movementReferences.get(movement.reverses_id) }}
            </a>
            <template v-else>{{ movement.reverses_id }}</template>
          </span>
          <span v-if="movement.reversed">Sudah dibalik</span>
          <button v-if="access && canReverse && !movement.reversed && movement.kind !== 'reversal'" type="button" class="btn btn-secondary"
            :disabled="!!original || !!pending || saving" @click="openReversal(movement, $event)">Balik mutasi</button>
        </li>
      </ul>
      <button v-if="sections.history.more || sections.history.error || pending?.uncertain" type="button" class="btn btn-secondary"
        :disabled="saving || sections.history.loading" @click="load('history', sections.history.more && !pending?.uncertain)">
        {{ sections.history.more && !pending?.uncertain ? 'Muat riwayat berikutnya' : 'Muat ulang riwayat' }}
      </button>
    </section>
  </main>
</template>

<style scoped>
.records { list-style: none; padding: 0; margin: 0; }
.records li { display: grid; justify-items: start; gap: .25rem; padding: .75rem 0; border-top: 1px solid #e2e8f0; }
li, dd { overflow-wrap: anywhere; }
dt { font-weight: 600; }
dd { margin: 0 0 .5rem; }
button + button { margin-inline-start: .5rem; }
</style>
