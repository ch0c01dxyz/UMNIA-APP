<script setup lang="ts">
// Skeleton F1-01: satu route yang menampilkan status business-api.
// Belum ada login/workspace — itu F1-03.
const config = useRuntimeConfig()
const status = ref<'loading' | 'ok' | 'error'>('loading')

onMounted(async () => {
  try {
    const res = await fetch(`${config.public.apiBase}/healthz`)
    status.value = res.ok ? 'ok' : 'error'
  } catch {
    status.value = 'error'
  }
})
</script>

<template>
  <main class="page">
    <h1>UMNIA</h1>
    <p class="tagline">
      Buku catatan usaha yang sederhana, dengan kemampuan sistem bisnis profesional.
    </p>
    <section class="card" aria-live="polite">
      <h2>Status fondasi (F1-01)</h2>
      <p v-if="status === 'loading'">
        Memeriksa business-api…
      </p>
      <p v-else-if="status === 'ok'" class="ok">
        Business-api terhubung (health 200).
      </p>
      <p v-else class="error">
        Business-api belum terhubung. Pastikan business-api berjalan di
        {{ config.public.apiBase }}, lalu muat ulang halaman ini.
      </p>
      <p class="note">
        Ini skeleton: belum ada login, workspace, atau modul. Lanjut ke F1-02/F1-03.
      </p>
    </section>
  </main>
</template>

<style>
/* Token minimal dari UI/UX Pro Max (Minimalism & Swiss Style):
   primer #171717, aksen #A16207, latar #FFFFFF, border #E5E5E5,
   muted #E8ECF0. Font sistem agar skeleton tidak bergantung unduhan font. */
.page {
  max-width: 640px;
  margin: 3rem auto;
  padding: 0 1.25rem;
  font-family: system-ui, -apple-system, "Segoe UI", sans-serif;
  color: #171717;
  background: #fff;
}
h1 { font-size: 2rem; letter-spacing: -0.02em; margin-bottom: 0.25rem; }
.tagline { color: #475569; margin-top: 0; }
.card {
  border: 1px solid #e5e5e5;
  background: #fff;
  padding: 1.25rem;
  margin-top: 1.5rem;
}
.card h2 { font-size: 1.125rem; margin-top: 0; }
.ok { color: #166534; }
.error { color: #dc2626; }
.note { color: #475569; background: #e8ecf0; padding: 0.75rem; }
a:focus-visible, button:focus-visible { outline: 2px solid #171717; outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) {
  * { transition: none !important; }
}
</style>
