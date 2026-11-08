<script setup lang="ts">
const nuxtApp = useNuxtApp()
const session = computed(() => nuxtApp.$session ?? null)
const authed = computed(() => session.value?.isAuthed.value ?? false)

function logout() {
  session.value?.clear()
  navigateTo('/login')
}
</script>

<template>
  <div>
    <a class="skip-link" href="#konten">Lewat ke konten utama</a>
    <header class="page site-header">
      <NuxtLink to="/" class="brand">UMNIA</NuxtLink>
      <nav class="site-nav" aria-label="Navigasi utama">
        <template v-if="authed">
          <NuxtLink to="/workspaces/new">Buat workspace</NuxtLink>
          <button type="button" class="link-button" @click="logout">Keluar</button>
        </template>
        <NuxtLink v-else to="/login">Masuk</NuxtLink>
      </nav>
    </header>
    <NuxtPage />
  </div>
</template>
