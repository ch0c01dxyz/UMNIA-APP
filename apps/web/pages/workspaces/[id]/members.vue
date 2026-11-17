<script setup lang="ts">
// Anggota + undang staf (GET workspace, GET members, POST invites).
// Undangan DIBUAT, bukan dikirim email: kode sekali tampil ke pengundang
// (input password, tersembunyi) dan tautan accept disalin terpisah.
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Anggota workspace — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface MemberRow { subject_id: string, email: string, status: string, role: string }
interface InviteCreated { id: string, token: string, email: string, role: string }

const ROLE_LABEL: Record<string, string> = { owner: 'Pemilik', manager: 'Pengelola', cashier: 'Kasir' }

const route = useRoute()
const session = useNuxtApp().$session ?? null
const wsId = computed(() => String(route.params.id ?? ''))

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const workspace = ref<Workspace | null>(null)
const members = ref<MemberRow[]>([])
const myRole = computed(() => {
  const me = members.value.find(m => m.subject_id === session?.subjectId.value)
  return me?.role ?? null
})
const canInvite = computed(() => myRole.value === 'owner' || myRole.value === 'manager')

const inviteEmail = ref('')
const inviteRole = ref('manager')
const inviteBusy = ref(false)
const inviteError = ref<string | null>(null)
const inviteEmailError = ref<string | null>(null)
const inviteResult = ref<InviteCreated | null>(null)
const copiedLink = ref(false)
const copiedCode = ref(false)
const resultHeading = ref<HTMLElement | null>(null)

onMounted(async () => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  await load()
})

onUnmounted(() => {
  inviteResult.value = null
})

async function load() {
  const s = session
  if (!s) return
  loading.value = true
  pageError.value = null
  noPermission.value = false
  try {
    const ws = await apiFetch<Workspace>(s, `/api/workspaces/${wsId.value}`)
    workspace.value = ws.data
    const list = await apiFetch<{ members: MemberRow[] }>(s, `/api/workspaces/${wsId.value}/members`)
    members.value = list.data.members
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      noPermission.value = true
      pageError.value = err.message
      return
    }
    if (err instanceof ApiError) pageError.value = err.message
    else pageError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    loading.value = false
  }
}

async function submitInvite() {
  const s = session
  if (!s) return
  inviteError.value = null
  inviteEmailError.value = null
  if (!inviteEmail.value.trim()) {
    inviteEmailError.value = 'Email wajib diisi.'
    return
  }
  inviteBusy.value = true
  try {
    const { data } = await apiFetch<InviteCreated>(s, `/api/workspaces/${wsId.value}/invites`, {
      method: 'POST',
      body: { email: inviteEmail.value.trim(), role: inviteRole.value }
    })
    inviteResult.value = data
    inviteEmail.value = ''
    await nextTick()
    resultHeading.value?.focus()
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError) inviteError.value = err.message
    else inviteError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    inviteBusy.value = false
  }
}

async function copyText(text: string, which: 'link' | 'code') {
  try {
    await navigator.clipboard.writeText(text)
    if (which === 'link') {
      copiedLink.value = true
      setTimeout(() => { copiedLink.value = false }, 3000)
    } else {
      copiedCode.value = true
      setTimeout(() => { copiedCode.value = false }, 3000)
    }
  } catch {
    inviteError.value = 'Salin otomatis gagal — pilih teks pada kolom, lalu salin manual.'
  }
}

function acceptLink() {
  return `${window.location.origin}/invites/accept?workspace_id=${wsId.value}`
}

function closeInvite() {
  inviteResult.value = null
  copiedLink.value = false
  copiedCode.value = false
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Anggota workspace</h1>
    <p v-if="checking">Memeriksa sesi masuk…</p>
    <template v-else>
      <p v-if="loading" role="status">Memuat…</p>
      <div v-else-if="noPermission" class="alert" role="alert">
        <h2>Tidak ada izin</h2>
        <p>{{ pageError }}</p>
        <p>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Lihat fitur workspace</NuxtLink>
        </p>
      </div>
      <div v-else-if="pageError" class="alert" role="alert">
        <h2>Ada masalah</h2>
        <p>{{ pageError }}</p>
        <p><button type="button" class="btn btn-secondary" @click="load">Coba lagi</button></p>
      </div>
      <template v-else-if="workspace">
        <p class="tagline">Workspace: <strong>{{ workspace.name }}</strong></p>
        <nav class="site-nav page-nav" aria-label="Navigasi workspace">
          <NuxtLink :to="`/workspaces/${wsId}/members`">Anggota</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/modules`">Fitur</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/customers`">Pelanggan</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/catalog`">Barang</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/assets`">Unit</NuxtLink>
        </nav>

        <section class="card" aria-labelledby="members-heading">
          <h2 id="members-heading">Anggota</h2>
          <ul class="plain">
            <li v-for="m in members" :key="m.subject_id">
              {{ m.email }} — {{ ROLE_LABEL[m.role] ?? m.role }}<span v-if="m.status !== 'active'"> ({{ m.status }})</span>
            </li>
          </ul>
        </section>

        <section v-if="canInvite" class="card" aria-labelledby="invite-heading">
          <h2 id="invite-heading">Undang staf</h2>
          <p class="small muted">
            Undangan hanya dibuat di sini — tidak ada email yang dikirim.
            Kirim tautan dan kode di bawah secara manual kepada staf.
          </p>
          <div v-if="inviteError" class="alert" role="alert">
            <p>{{ inviteError }}</p>
          </div>
          <form @submit.prevent="submitInvite" novalidate>
            <div class="field">
              <label for="invite-email">Email staf</label>
              <input
                id="invite-email"
                v-model="inviteEmail"
                type="email"
                autocomplete="email"
                :aria-invalid="inviteEmailError !== null"
                :aria-describedby="inviteEmailError ? 'invite-email-error' : undefined"
              >
              <p v-if="inviteEmailError" id="invite-email-error" class="field-error">{{ inviteEmailError }}</p>
            </div>
            <div class="field">
              <label for="invite-role">Peran</label>
              <select id="invite-role" v-model="inviteRole">
                <option value="manager">Pengelola</option>
                <option value="cashier">Kasir</option>
              </select>
              <p class="hint">Pemilik tidak dapat diundang — pemilik adalah pembuat workspace.</p>
            </div>
            <button type="submit" class="btn" :disabled="inviteBusy">
              {{ inviteBusy ? 'Memproses…' : 'Buat undangan' }}
            </button>
          </form>

          <div v-if="inviteResult" class="card" aria-labelledby="invite-result-heading">
            <h3 id="invite-result-heading" ref="resultHeading" tabindex="-1">Undangan dibuat</h3>
            <p>
              Undangan untuk <strong>{{ inviteResult.email }}</strong> sebagai
              {{ ROLE_LABEL[inviteResult.role] ?? inviteResult.role }} sudah dibuat.
              Kode di bawah hanya tampil sekali di sini.
            </p>
            <div class="field">
              <label for="invite-code">Kode undangan (tersembunyi)</label>
              <input id="invite-code" type="password" readonly :value="inviteResult.token" autocomplete="off">
            </div>
            <p>
              <button type="button" class="btn btn-secondary" @click="copyText(acceptLink(), 'link')">
                {{ copiedLink ? 'Tautan disalin' : 'Salin tautan' }}
              </button>
              <button type="button" class="btn btn-secondary" @click="copyText(inviteResult.token, 'code')">
                {{ copiedCode ? 'Kode disalin' : 'Salin kode' }}
              </button>
              <button type="button" class="btn btn-secondary" @click="closeInvite">Tutup</button>
            </p>
            <p class="small muted">
              Tautan berisi alamat halaman terima dan workspace saja — tanpa kode.
              Kode disalin terpisah. Menutup kartu ini menghapus kode dari layar.
            </p>
          </div>
        </section>
      </template>
    </template>
  </main>
</template>
