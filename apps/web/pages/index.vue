<script setup lang="ts">
// Entri jujur: status layanan via proxy same-origin + tautan masuk/daftar.
// Tidak ada daftar workspace di slice ini.
useHead({ title: 'UMNIA — Buku catatan usaha' })

const nuxtApp = useNuxtApp()
const session = computed(() => nuxtApp.$session ?? null)
const authed = computed(() => session.value?.isAuthed.value ?? false)
const status = ref<'loading' | 'ok' | 'error'>('loading')

onMounted(async () => {
  try {
    const res = await fetch('/api/healthz')
    status.value = res.ok ? 'ok' : 'error'
  } catch {
    status.value = 'error'
  }
})
</script>

<template>
  <main id="konten" class="page">
    <h1>UMNIA</h1>
    <p class="tagline">
      Buku catatan usaha yang sederhana, dengan kemampuan sistem bisnis profesional.
    </p>
    <section class="card" aria-live="polite">
      <h2>Status layanan</h2>
      <p v-if="status === 'loading'">
        Memeriksa layanan…
      </p>
      <p v-else-if="status === 'ok'" class="ok">
        Layanan terhubung.
      </p>
      <p v-else class="error">
        Layanan belum terhubung. Muat ulang halaman ini.
      </p>
    </section>
    <section class="card">
      <h2>Mulai</h2>
      <p v-if="authed">
        Anda sudah masuk. Buat workspace baru untuk mulai mengelola usaha Anda.
      </p>
      <p v-else>
        Buat akun, masuk, lalu buat workspace usaha Anda.
      </p>
      <p>
        <NuxtLink v-if="authed" class="btn" to="/workspaces/new">Buat workspace</NuxtLink>
        <NuxtLink v-else class="btn" to="/login">Masuk / Daftar</NuxtLink>
      </p>
      <p class="small muted">
        Workspace tidak dapat dicari dari halaman ini: sesudah membuat workspace,
        simpan alamatnya. Menutup atau memuat ulang halaman mengharuskan Anda
        masuk kembali.
      </p>
    </section>
  </main>
</template>
