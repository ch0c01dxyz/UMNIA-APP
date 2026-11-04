// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2025-01-01',
  runtimeConfig: {
    public: {
      // Di-override lewat NUXT_PUBLIC_API_BASE, default untuk dev lokal.
      apiBase: process.env.NUXT_PUBLIC_API_BASE || 'http://localhost:8080'
    }
  }
})
