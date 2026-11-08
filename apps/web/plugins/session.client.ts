// Sesi hanya di memori browser. Plugin .client: tidak pernah jalan di
// server, jadi token tidak pernah masuk payload SSR, localStorage,
// sessionStorage, IndexedDB, atau cookie. Tutup/muat ulang = masuk lagi;
// sesi server tetap kedaluwarsa otomatis setelah 24 jam.
import { computed, ref } from 'vue'
import type { ComputedRef, Ref } from 'vue'

export interface SessionState {
  token: Ref<string | null>
  subjectId: Ref<string | null>
  isAuthed: ComputedRef<boolean>
  set: (token: string, subjectId: string) => void
  clear: () => void
}

declare module '#app' {
  interface NuxtApp {
    $session: SessionState
  }
}

export default defineNuxtPlugin((nuxtApp) => {
  const token = ref<string | null>(null)
  const subjectId = ref<string | null>(null)
  const session: SessionState = {
    token,
    subjectId,
    isAuthed: computed(() => token.value !== null),
    set(t, s) {
      token.value = t
      subjectId.value = s
    },
    clear() {
      token.value = null
      subjectId.value = null
    }
  }
  nuxtApp.provide('session', session)
})
