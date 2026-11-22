// Proxy same-origin ke business-api TETAP (runtime config privat).
// Hanya pasangan method/path di ALLOWLIST yang diteruskan; sisanya gagal
// lokal tanpa menghubungi upstream. Tanpa log body/kredensial, tanpa
// redirect yang mengikuti, tanpa header Cookie/Host/forwarded.
import { defineEventHandler, setResponseHeader, setResponseStatus } from 'h3'
import type { H3Event } from 'h3'
import type { IncomingMessage } from 'node:http'

const MAX_BODY = 16384
const UPSTREAM_TIMEOUT_MS = 10000
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const MODULE_RE = /^[a-z][a-z0-9]*(\.[a-z][a-z0-9]*)+$/

type Route = { method: 'GET' | 'POST', segments: Array<string | 'uuid' | 'module'>, query?: boolean }
const ALLOWLIST: Route[] = [
  { method: 'GET', segments: ['healthz'] },
  { method: 'POST', segments: ['auth', 'register'] },
  { method: 'POST', segments: ['auth', 'login'] },
  { method: 'POST', segments: ['workspaces'] },
  { method: 'GET', segments: ['workspaces', 'uuid'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'members'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'invites'] },
  { method: 'POST', segments: ['invites', 'accept'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'modules'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'modules', 'module'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'modules', 'module', 'activate'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'customers'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'customers'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'catalog', 'items'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'catalog', 'items'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'assets'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'assets'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'catalog', 'items', 'uuid', 'prices'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'catalog', 'items', 'uuid', 'prices'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'contracts'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts'] },
  // Harga draf (F2-UI-05): quote/fakta baca + pencatatan; tanpa query.
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'quote'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'accept-price'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'agreed-price'] },
  // Serah terima (F2-UI-06): baca custody + hold/handover/return; daftar
  // holds boleh query paginasi, empat lainnya tanpa query.
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'custody'] },
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'holds'], query: true },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'holds'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'handover'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'return'] },
  // Pembayaran (F2-UI-07): baca riwayat + 4 tulis (usulan/setujui
  // pembayaran, usulan/setujui pembalikan catatan); tanpa query.
  { method: 'GET', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'payments'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'payment-proposals'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'payment-proposals', 'uuid', 'approvals'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'payments', 'uuid', 'reversal-proposals'] },
  { method: 'POST', segments: ['workspaces', 'uuid', 'rental', 'contracts', 'uuid', 'payments', 'uuid', 'reversal-proposals', 'uuid', 'approvals'] }
]

// Validasi query daftar pelanggan/barang/unit/tarif/draf sewa (F2-UI-01/F2-UI-02/F2-UI-03/F2-UI-04,
// satu-satunya query yang diizinkan proxy): kosong, atau bagian limit=<n> dan/atau offset=<n>,
// masing-masing sekali, urutan bebas. Hanya digit desimal kanonis:
// limit 1-100; offset 0-9007199254740991. Tolak duplikat, nama tak
// dikenal/berbeda kasus, nilai kosong/flag, nol di depan, tanda,
// spasi, desimal, eksponen, persen-encoding/escape rusak, ';', '?'
// tambahan, '#', dan '&' kosong/ganda/di akhir. Query yang lolos
// diteruskan apa adanya, tanpa pembulatan/pengkodean ulang.
const MAX_OFFSET = 9007199254740991n
function validListQuery(query: string): boolean {
  if (query === '' || query.includes('?') || query.includes('#') || query.includes(';') || query.includes('%')) return false
  const parts = query.split('&')
  const seen = new Set<string>()
  for (const part of parts) {
    if (part === '') return false
    const eq = part.indexOf('=')
    if (eq === -1) return false
    const name = part.slice(0, eq)
    const value = part.slice(eq + 1)
    if (name !== 'limit' && name !== 'offset') return false
    if (seen.has(name)) return false
    seen.add(name)
    if (value === '' || !/^[0-9]+$/.test(value)) return false
    if (value.length > 1 && value.startsWith('0')) return false
    if (name === 'limit') {
      const n = Number(value)
      if (!Number.isInteger(n) || n < 1 || n > 100) return false
    } else if (BigInt(value) > MAX_OFFSET) {
      return false
    }
  }
  return parts.length > 0
}

function fail(event: H3Event, status: number, message: string) {
  setResponseStatus(event, status)
  return { error: message }
}

type BodyRead = { body?: Buffer, tooLarge?: boolean, streamError?: boolean }

// Baca body POST dengan batas BYTE sambil streaming: hitung byte (bukan
// karakter JS), dan berhenti mengonsumsi begitu melewati batas — tanpa
// menunggu EOF dan tanpa menampung body tak terbatas. Content-Length hanya
// petunjuk awal; body chunked tanpa Content-Length tetap dibatasi.
function readBodyBounded(req: IncomingMessage): Promise<BodyRead> {
  const declared = Number(req.headers['content-length'] || 0)
  if (Number.isFinite(declared) && declared > MAX_BODY) return Promise.resolve({ tooLarge: true })
  if (req.readableEnded) return Promise.resolve({})
  return new Promise((resolve) => {
    const chunks: Buffer[] = []
    let total = 0
    let done = false
    const cleanup = () => {
      req.off('data', onData)
      req.off('end', onEnd)
      req.off('error', onError)
      req.off('aborted', onAborted)
      req.off('close', onClose)
    }
    const finish = (result: BodyRead) => {
      if (done) return
      done = true
      cleanup()
      resolve(result)
    }
    const onData = (chunk: Buffer) => {
      total += chunk.length
      if (total > MAX_BODY) {
        // Berhenti mengonsumsi; respons 400 dikirim dengan Connection: close
        // supaya soket ditutup sesudah respons dan sisa byte terbuang aman.
        req.pause()
        finish({ tooLarge: true })
        return
      }
      chunks.push(chunk)
    }
    const onEnd = () => finish({ body: total === 0 ? undefined : Buffer.concat(chunks, total) })
    const onError = () => finish({ streamError: true })
    const onAborted = () => finish({ streamError: true })
    const onClose = () => finish({ streamError: true })
    req.on('data', onData)
    req.on('end', onEnd)
    req.on('error', onError)
    req.on('aborted', onAborted)
    req.on('close', onClose)
  })
}

export default defineEventHandler(async (event) => {
  const raw = event.node.req.url || ''
  // Tolak backslash dan '#' di mana pun sebelum validasi; '?' dipisah
  // sekali di bawah (query hanya untuk dua rute GET daftar, lihat bawah).
  if (!raw.startsWith('/api/') || raw.includes('\\') || raw.includes('#')) {
    return fail(event, 400, 'permintaan tidak valid')
  }
  const qPos = raw.indexOf('?')
  const pathPart = qPos === -1 ? raw : raw.slice(0, qPos)
  const queryPart = qPos === -1 ? null : raw.slice(qPos + 1)
  // '?' telanjang di akhir dan '?' tambahan ditolak lokal.
  if (queryPart !== null && (queryPart === '' || queryPart.includes('?'))) {
    return fail(event, 400, 'permintaan tidak valid')
  }
  const segments: string[] = []
  for (const part of pathPart.slice(5).split('/')) {
    if (part === '') return fail(event, 400, 'permintaan tidak valid')
    let decoded: string
    try {
      decoded = decodeURIComponent(part)
    } catch {
      return fail(event, 400, 'permintaan tidak valid')
    }
    if (decoded === '' || decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\') || decoded.includes('#')) {
      return fail(event, 400, 'permintaan tidak valid')
    }
    segments.push(decoded)
  }
  const method = event.node.req.method || ''
  const matched = ALLOWLIST.find(route =>
    route.method === method &&
    route.segments.length === segments.length &&
    route.segments.every((want, i) =>
      want === 'uuid' ? UUID_RE.test(segments[i])
      : want === 'module' ? MODULE_RE.test(segments[i])
      : want === segments[i]))
  // Query di luar rute GET daftar ditolak 400 SEBELUM upstream —
  // termasuk pada path yang tidak dikenal; path tak dikenal TANPA query
  // tetap 404. Query pada rute daftar yang dikenal divalidasi ketat.
  if (queryPart !== null && (!matched || !matched.query || !validListQuery(queryPart))) {
    return fail(event, 400, 'permintaan tidak valid')
  }
  if (!matched) return fail(event, 404, 'tidak ditemukan')

  let body: Buffer | undefined
  if (method === 'POST') {
    const read = await readBodyBounded(event.node.req)
    if (read.tooLarge) {
      setResponseHeader(event, 'Connection', 'close')
      return fail(event, 400, 'permintaan tidak valid')
    }
    if (read.streamError) {
      return fail(event, 400, 'permintaan tidak valid')
    }
    body = read.body
  }

  const base = String(useRuntimeConfig().businessApiBase || '').replace(/\/+$/, '')
  if (!/^https?:\/\/[A-Za-z0-9.-]+(:\d+)?$/.test(base)) {
    return fail(event, 502, 'layanan tidak tersedia')
  }
  const upstreamPath = '/' + segments.map(s => UUID_RE.test(s) ? s.toLowerCase() : s).join('/') + (queryPart !== null ? `?${queryPart}` : '')
  const headers: Record<string, string> = {}
  const contentType = event.node.req.headers['content-type']
  if (typeof contentType === 'string' && contentType !== '') headers['Content-Type'] = contentType
  const auth = event.node.req.headers.authorization
  if (typeof auth === 'string' && auth !== '') headers.Authorization = auth
  const idem = event.node.req.headers['idempotency-key']
  if (typeof idem === 'string' && idem !== '') headers['Idempotency-Key'] = idem

  let upstream: Response
  try {
    upstream = await fetch(base + upstreamPath, {
      method,
      headers,
      body,
      redirect: 'manual',
      signal: AbortSignal.timeout(UPSTREAM_TIMEOUT_MS)
    })
  } catch {
    return fail(event, 502, 'layanan tidak tersedia')
  }
  if (upstream.status >= 300 && upstream.status < 400) return fail(event, 502, 'layanan tidak tersedia')
  const text = await upstream.text()
  let data: unknown
  try {
    data = JSON.parse(text)
  } catch {
    return fail(event, 502, 'layanan tidak tersedia')
  }
  setResponseStatus(event, upstream.status)
  return data
})
