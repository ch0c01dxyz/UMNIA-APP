<script setup lang="ts">
// Terima undangan (POST /invites/accept). Kode ditempel manual ke input
// kata sandi — tidak pernah di URL, tidak pernah di-log. Terima hanya
// terjadi saat tombol ditekan, bukan saat halaman dibaca.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Terima undangan — UMNIA' })

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

const route = useRoute()
const session = useNuxtApp().$session ?? null

const wsId = computed(() => {
  const q = route.query.workspace_id
  return typeof q === 'string' && UUID_RE.test(q) ? q.toLowerCase() : null
})
const authed = computed(() => session?.isAuthed.value ?? false)
const code = ref('')
const busy = ref(false)
const formError = ref<string | null>(null)
const codeError = ref<string | null>(null)
const errorBox = ref<HTMLElement | null>(null)

async function showError(message: string) {
  formError.value = message
  await nextTick()
  errorBox.value?.focus()
}

async function submit() {
  const s = session
  if (!s || !wsId.value) return
  formError.value = null
  codeError.value = null
  if (!code.value.trim()) {
    codeError.value = 'Kode undangan wajib diisi.'
    await showError('Ada masalah pada isian Anda.')
    return
  }
  busy.value = true
  try {
    const { data } = await apiFetch<{ workspace_id: string, role: string }>(s, '/api/invites/accept', {
      method: 'POST',
      body: { workspace_id: wsId.value, token: code.value.trim() }
    })
    await navigateTo(`/workspaces/${data.workspace_id}/members`)
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) await showError(err.message)
    else await showError('Terjadi kesalahan. Coba lagi.')
  } finally {
    code.value = ''
    busy.value = false
  }
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Terima undangan</h1>
    <template v-if="!wsId">
      <div class="alert" role="alert">
        <h2>Tautan tidak valid</h2>
        <p>Tautan undangan ini tidak memuat alamat workspace yang benar. Minta tautan baru kepada pengundang.</p>
      </div>
    </template>
    <template v-else-if="!authed">
      <p>
        Untuk menerima undangan ini, Anda perlu masuk — atau daftar dulu
        dengan email yang sama dengan alamat undangan.
      </p>
      <p>
        <NuxtLink class="btn" :to="loginUrl(`/invites/accept?workspace_id=${wsId}`)">Masuk / Daftar</NuxtLink>
      </p>
    </template>
    <template v-else>
      <p class="tagline">
        Tempel kode undangan yang Anda terima secara manual. Kode tidak
        pernah dikirim lewat email oleh sistem ini.
      </p>
      <div v-if="formError" ref="errorBox" class="alert" role="alert" tabindex="-1">
        <h2>Ada masalah</h2>
        <p>{{ formError }}</p>
        <p v-if="formError.includes('bukan untuk akun ini')" class="small">
          Masuk dengan akun yang emailnya sama dengan alamat undangan.
        </p>
      </div>
      <form @submit.prevent="submit" novalidate>
        <div class="field">
          <label for="accept-code">Kode undangan</label>
          <input
            id="accept-code"
            v-model="code"
            type="password"
            autocomplete="one-time-code"
            :aria-invalid="codeError !== null"
            :aria-describedby="codeError ? 'accept-code-error' : undefined"
          >
          <p v-if="codeError" id="accept-code-error" class="field-error">{{ codeError }}</p>
          <p class="hint">Tempel kode dari pengundang. Kode tersembunyi saat diketik.</p>
        </div>
        <button type="submit" class="btn" :disabled="busy">
          {{ busy ? 'Memproses…' : 'Terima undangan' }}
        </button>
      </form>
    </template>
  </main>
</template>
