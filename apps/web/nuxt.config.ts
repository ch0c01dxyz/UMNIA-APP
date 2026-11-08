// https://nuxt.com/docs/api/configuration/nuxt-config
export default defineNuxtConfig({
  compatibilityDate: '2025-01-01',
  css: ['~/assets/main.css'],
  runtimeConfig: {
    // Privat, hanya server: basis upstream bisnis TETAP untuk proxy
    // same-origin server/api/[...path].ts. Di-override lewat
    // NUXT_BUSINESS_API_BASE. TIDAK ADA public.apiBase lagi: browser
    // hanya memanggil /api/... (same-origin), tidak pernah langsung
    // ke business-api.
    businessApiBase: process.env.NUXT_BUSINESS_API_BASE || 'http://localhost:8080',
    public: {}
  }
})
