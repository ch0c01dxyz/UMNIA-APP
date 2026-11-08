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

type Route = { method: 'GET' | 'POST', segments: Array<string | 'uuid' | 'module'> }
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
  { method: 'POST', segments: ['workspaces', 'uuid', 'modules', 'module', 'activate'] }
]

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
  // Tolak query string, backslash, dan path di luar /api/ sebelum validasi.
  if (!raw.startsWith('/api/') || raw.includes('\\') || raw.includes('?')) {
    return fail(event, 400, 'permintaan tidak valid')
  }
  const segments: string[] = []
  for (const part of raw.slice(5).split('/')) {
    if (part === '') return fail(event, 400, 'permintaan tidak valid')
    let decoded: string
    try {
      decoded = decodeURIComponent(part)
    } catch {
      return fail(event, 400, 'permintaan tidak valid')
    }
    if (decoded === '' || decoded === '.' || decoded === '..' || decoded.includes('/') || decoded.includes('\\')) {
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
  const upstreamPath = '/' + segments.map(s => UUID_RE.test(s) ? s.toLowerCase() : s).join('/')
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
