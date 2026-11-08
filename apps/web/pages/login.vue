<script setup lang="ts">
// Masuk / daftar dengan POST /auth/login dan POST /auth/register.
// Registrasi hanya membuat akun (201 {id, email}) — tanpa token, tanpa
// email verifikasi; masuk eksplisit tetap diperlukan sesudahnya.
import { ApiError, apiFetch, resolveLoginTarget } from '~/lib/api'

useHead({ title: 'Masuk — UMNIA' })

const route = useRoute()
const session = useNuxtApp().$session ?? null

const mode = ref<'login' | 'register'>('login')
const email = ref('')
const password = ref('')
const busy = ref(false)
const formError = ref<string | null>(null)
const notice = ref<string | null>(null)
const emailError = ref<string | null>(null)
const passwordError = ref<string | null>(null)
const errorBox = ref<HTMLElement | null>(null)

async function showError(message: string) {
  formError.value = message
  await nextTick()
  errorBox.value?.focus()
}

async function submit() {
  notice.value = null
  formError.value = null
  emailError.value = null
  passwordError.value = null
  if (!email.value.trim()) {
    emailError.value = 'Email wajib diisi.'
    await showError('Ada masalah pada isian Anda.')
    return
  }
  if (!password.value) {
    passwordError.value = 'Kata sandi wajib diisi.'
    await showError('Ada masalah pada isian Anda.')
    return
  }
  busy.value = true
  try {
    if (mode.value === 'login') {
      const { data } = await apiFetch<{ token: string, subject_id: string }>(session, '/api/auth/login', {
        method: 'POST',
        body: { email: email.value.trim(), password: password.value }
      })
      session?.set(data.token, data.subject_id)
      const next = typeof route.query.next === 'string' ? route.query.next : null
      await navigateTo(resolveLoginTarget(next))
    } else {
      await apiFetch<{ id: string, email: string }>(session, '/api/auth/register', {
        method: 'POST',
        body: { email: email.value.trim(), password: password.value }
      })
      notice.value = 'Akun dibuat. Masuk dengan email dan kata sandi Anda.'
      mode.value = 'login'
    }
  } catch (err) {
    if (err instanceof ApiError) await showError(err.message)
    else await showError('Terjadi kesalahan. Coba lagi.')
  } finally {
    password.value = ''
    busy.value = false
  }
}
</script>

<template>
  <main id="konten" class="page">
    <h1>{{ mode === 'login' ? 'Masuk' : 'Daftar' }}</h1>
    <p class="tagline">
      <template v-if="mode === 'login'">
        Masuk untuk mengelola workspace usaha Anda.
      </template>
      <template v-else>
        Buat akun untuk mulai memakai UMNIA.
      </template>
    </p>

    <p>
      <button type="button" class="btn btn-secondary" :aria-pressed="mode === 'login'" :disabled="mode === 'login'" @click="mode = 'login'; notice = null; formError = null">Masuk</button>
      <button type="button" class="btn btn-secondary" :aria-pressed="mode === 'register'" :disabled="mode === 'register'" @click="mode = 'register'; notice = null; formError = null">Daftar</button>
    </p>

    <div v-if="notice" class="alert alert-ok" role="status">
      {{ notice }}
    </div>
    <div v-if="formError" ref="errorBox" class="alert" role="alert" tabindex="-1">
      <h2>Ada masalah</h2>
      <p>{{ formError }}</p>
    </div>

    <form @submit.prevent="submit" novalidate>
      <div class="field">
        <label for="login-email">Email</label>
        <input
          id="login-email"
          v-model="email"
          type="email"
          autocomplete="email"
          :aria-invalid="emailError !== null"
          :aria-describedby="emailError ? 'login-email-error' : undefined"
        >
        <p v-if="emailError" id="login-email-error" class="field-error">{{ emailError }}</p>
      </div>
      <div class="field">
        <label for="login-password">Kata sandi</label>
        <input
          id="login-password"
          v-model="password"
          type="password"
          :autocomplete="mode === 'login' ? 'current-password' : 'new-password'"
          :aria-invalid="passwordError !== null"
          :aria-describedby="passwordError ? 'login-password-error' : undefined"
        >
        <p v-if="passwordError" id="login-password-error" class="field-error">{{ passwordError }}</p>
        <p v-if="mode === 'register'" class="hint">Kata sandi minimal 8 karakter.</p>
      </div>
      <button type="submit" class="btn" :disabled="busy">
        {{ busy ? 'Memproses…' : mode === 'login' ? 'Masuk' : 'Buat akun' }}
      </button>
    </form>

    <p v-if="mode === 'register'" class="small muted">
      Mendaftar hanya membuat akun — tidak ada email verifikasi yang dikirim.
      Anda tetap perlu masuk sesudahnya.
    </p>
    <p class="small muted">
      Menutup atau memuat ulang halaman mengharuskan Anda masuk kembali;
      sesi server kedaluwarsa otomatis setelah 24 jam.
    </p>
  </main>
</template>
