<script setup lang="ts">
// Buat workspace (POST /workspaces). 201 {id, name} -> lanjut ke
// /workspaces/{id}/members (bukan /workspaces/{id} — halaman itu tidak ada).
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Buat workspace — UMNIA' })

const route = useRoute()
const session = useNuxtApp().$session ?? null

const name = ref('')
const busy = ref(false)
const checking = ref(true)
const formError = ref<string | null>(null)
const nameError = ref<string | null>(null)
const errorBox = ref<HTMLElement | null>(null)

onMounted(() => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
})

async function showError(message: string) {
  formError.value = message
  await nextTick()
  errorBox.value?.focus()
}

async function submit() {
  const s = session
  if (!s) return
  formError.value = null
  nameError.value = null
  if (!name.value.trim()) {
    nameError.value = 'Nama workspace wajib diisi.'
    await showError('Ada masalah pada isian Anda.')
    return
  }
  busy.value = true
  try {
    const { data } = await apiFetch<{ id: string, name: string }>(s, '/api/workspaces', {
      method: 'POST',
      body: { name: name.value.trim() }
    })
    await navigateTo(`/workspaces/${data.id}/members`)
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) await showError(err.message)
    else await showError('Terjadi kesalahan. Coba lagi.')
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Buat workspace</h1>
    <p class="tagline">
      Workspace adalah tempat usaha Anda: anggota, undangan, dan fitur
      dikelola per workspace.
    </p>
    <p v-if="checking">Memeriksa sesi masuk…</p>
    <template v-else>
      <div v-if="formError" ref="errorBox" class="alert" role="alert" tabindex="-1">
        <h2>Ada masalah</h2>
        <p>{{ formError }}</p>
      </div>
      <form @submit.prevent="submit" novalidate>
        <div class="field">
          <label for="ws-name">Nama workspace</label>
          <input
            id="ws-name"
            v-model="name"
            type="text"
            autocomplete="organization"
            :aria-invalid="nameError !== null"
            :aria-describedby="nameError ? 'ws-name-error' : undefined"
          >
          <p v-if="nameError" id="ws-name-error" class="field-error">{{ nameError }}</p>
          <p class="hint">Contoh: Toko Kopi Sore, Bengkel Jaya.</p>
        </div>
        <button type="submit" class="btn" :disabled="busy">
          {{ busy ? 'Memproses…' : 'Buat workspace' }}
        </button>
      </form>
      <p class="small muted">
        Sesudah dibuat, Anda diarahkan ke halaman anggota workspace ini.
        Simpan alamat halaman itu — workspace tidak dapat dicari dari
        halaman utama.
      </p>
    </template>
  </main>
</template>
