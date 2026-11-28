// Klien API browser: hanya memanggil /api/... same-origin. Token dibaca
// dari sesi memori (parameter), tidak pernah disimpan atau di-log di sini.
// Pesan error backend diteruskan apa adanya (amplop datar {"error": ...}).
import { navigateTo } from '#app'
import type { SessionState } from '~/plugins/session.client'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i

// Tujuan sesudah login: hanya rute dikenal + UUID tervalidasi. Absolute,
// protocol-relative, backslash, dan escape encoded ditolak -> /workspaces/new.
export function resolveLoginTarget(raw: string | null | undefined): string {
  const fallback = '/workspaces/new'
  if (!raw || typeof raw !== 'string') return fallback
  if (raw.includes('\\') || raw.includes('%') || !raw.startsWith('/')) return fallback
  // Match before query stripping: the summary continuation must be queryless.
  const summary = raw.match(/^\/workspaces\/([0-9a-fA-F-]{36})\/payment-summary$/)
  if (summary && UUID_RE.test(summary[1])) return raw
  let path = raw
  let query = ''
  const q = raw.indexOf('?')
  if (q !== -1) {
    path = raw.slice(0, q)
    query = raw.slice(q + 1)
  }
  if (path === '/workspaces/new') return path
  const m = path.match(/^\/workspaces\/([0-9a-f-]{36})\/(members|modules|customers|catalog|assets|prices|rentals|inventory)$/)
  if (m && UUID_RE.test(m[1])) return path
  // Harga draf (F2-UI-05): rute sibling dengan workspace DAN draft yang
  // keduanya divalidasi UUID; bukan pelonggaran pola umum.
  const price = path.match(/^\/workspaces\/([0-9a-f-]{36})\/rental-prices\/([0-9a-f-]{36})$/)
  if (price && UUID_RE.test(price[1]) && UUID_RE.test(price[2])) return path
  // Serah terima (F2-UI-06): rute sibling dengan pola yang sama.
  const custody = path.match(/^\/workspaces\/([0-9a-f-]{36})\/rental-custody\/([0-9a-f-]{36})$/)
  if (custody && UUID_RE.test(custody[1]) && UUID_RE.test(custody[2])) return path
  // Pembayaran (F2-UI-07): rute sibling dengan pola yang sama.
  const payments = path.match(/^\/workspaces\/([0-9a-f-]{36})\/rental-payments\/([0-9a-f-]{36})$/)
  if (payments && UUID_RE.test(payments[1]) && UUID_RE.test(payments[2])) return path
  if (path === '/invites/accept') {
    const params = new URLSearchParams(query)
    const ws = params.get('workspace_id')
    if (ws && UUID_RE.test(ws) && [...params.keys()].every(k => k === 'workspace_id')) {
      return `/invites/accept?workspace_id=${ws.toLowerCase()}`
    }
  }
  return fallback
}

export function loginUrl(currentPath: string): string {
  return `/login?next=${encodeURIComponent(resolveLoginTarget(currentPath))}`
}

// 401: bersihkan rahasia klien lalu minta masuk, dengan tujuan aman.
export function redirectToLogin(session: SessionState, currentPath: string): void {
  session.clear()
  if (import.meta.client) navigateTo(loginUrl(currentPath))
}

export async function apiFetch<T>(
  session: SessionState | null,
  path: string,
  opts: { method?: 'GET' | 'POST', body?: unknown, idempotencyKey?: string } = {}
): Promise<{ status: number, data: T }> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (session?.token.value) headers.Authorization = `Bearer ${session.token.value}`
  // Kunci idempotency hanya untuk panggilan yang memintanya secara
  // eksplisit (F2-UI-03); pemanggil lama tidak mengirim header ini.
  if (opts.idempotencyKey) headers['Idempotency-Key'] = opts.idempotencyKey
  let res: Response
  try {
    res = await fetch(path, {
      method: opts.method ?? 'GET',
      headers,
      body: opts.body === undefined ? undefined : JSON.stringify(opts.body)
    })
  } catch {
    throw new ApiError(502, 'Layanan tidak tersedia. Coba lagi sebentar lagi.')
  }
  let data: unknown = null
  try {
    data = await res.json()
  } catch {
    data = null
  }
  if (!res.ok) {
    const message = data !== null && typeof data === 'object' && 'error' in data &&
      typeof (data as { error: unknown }).error === 'string'
      ? (data as { error: string }).error
      : 'Terjadi kesalahan. Coba lagi.'
    throw new ApiError(res.status, message)
  }
  return { status: res.status, data: data as T }
}
