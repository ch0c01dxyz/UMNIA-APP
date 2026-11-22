<script setup lang="ts">
// Pembayaran rental workspace (GET payments / 4 POST untuk satu draf;
// GET koleksi 50 baris hanya untuk konteks draf, GET label 100 baris
// untuk nama pelanggan/barang). Halaman ini TIDAK membuat/mengubah draf,
// TIDAK mencatat harga, TIDAK menyentuh custody. Pembayaran/pembalikan
// adalah catatan staf di dalam aplikasi — BUKAN pengembalian uang, BUKAN
// pendapatan/keuntungan, BUKAN utang hukum, BUKAN invoice, BUKAN
// penyelesaian bank, BUKAN pajak, dan BUKAN transisi status draf.
// Pembalikan adalah pembatalan catatan penuh (bukan refund sebagian);
// tidak ada alur koreksi harga di slice ini.
//
// Server otoritatif: riwayat + total dibaca dari SATU GET per draf
// (server yang menghitung total_minor/paid_minor/outstanding_minor);
// halaman TIDAK PERNAH menghitung saldo di klien. Usulan pending TIDAK
// menambah total posted. Tulis butuh manage_business_data + instalasi
// finance.basic aktif + entitlement versi aktif; baca riwayat TIDAK
// punya gerbang finance — riwayat tetap terbaca sesudah
// deactivate/revoke. Kasir melihat formulir dan menerima 403 saat tulis
// (bukan disembunyikan). Tanpa-fakta HANYA pesan 409 "harga disepakati
// belum ada" (GET payments) / 404 "harga yang disepakati tidak
// ditemukan" (GET agreed-price); draf hilang HANYA pesan 404 "draft
// tidak ditemukan"; bacaan yang gagal/tertunda tidak pernah mengiklankan
// tulis yang bergantung padanya.
//
// EMPAT tahap, EMPAT mesin: usulan pembayaran, persetujuan pembayaran,
// usulan pembalikan, persetujuan pembalikan — masing-masing punya
// snapshot, kunci, dan pagar generasi sendiri. EMPAT keyspace berbeda
// (pay-prop-, pay-appr-, pay-revprop-, pay-revappr-). Kunci dibuat
// dengan crypto.randomUUID() saat panel konfirmasi dibuka (alasan
// pembalikan dibekukan saat kirim pertama, karena diisi di dalam panel),
// lalu dibekukan bersama snapshot tahap itu. Hanya 200/201 dengan DTO
// yang tervalidasi yang mengonfirmasi tulis; respons malformed tetap
// tidak diketahui. Penyelesaian HANYA lewat GET (settlement): usulan
// pembayaran dicocokkan di pending_payment_proposals[], persetujuan
// pembayaran di payments[] (posting yang belakangan dibalik tetap
// mengonfirmasi persetujuan awal), usulan pembalikan di
// pending_reversal_proposals[], persetujuan pembalikan di reversals[].
// Usulan yang hilang dari pending TIDAK diselesaikan lewat GET —
// attempt dipertahankan dan kirim-ulang kunci-sama yang mengonfirmasi.
// Kunci sama dengan field tidak cocok mempertahankan ketidakpastian.
// Catatan aksi lain (kunci benar-benar berbeda) adalah riwayat
// independen: attempt persetujuan yang sudah ditutup posting pihak lain
// atau pembalikan terminal dibersihkan tanpa klaim sukses. Hasil tak
// diketahui mengunci aksi biasa dan hanya menawarkan muat-ulang GET,
// kirim-ulang kunci-sama, atau "Mulai … baru" yang diperingatkan dari
// konflik kunci definitif. Selama permintaan lama masih mungkin
// terkomit, tidak ada restart kunci-baru. Pemulihan hanya hidup di
// memori halaman ini.
//
// Persetujuan butuh aktor kedua (server menolak 403 bila penyetuju sama
// dengan pengusul); tombol persetujuan dinonaktifkan untuk pengusul
// sendiri. Persetujuan pembalikan juga dinonaktifkan bila target sudah
// dibalik, termasuk saat usulan pesaing masih tercantum pending.
//
// Konteks draf: fakta harga yang disepakati membawa pelanggan, barang,
// dan periode — fakta yang tervalidasi sudah cukup sebagai konteks.
// Baris draf dari lookup adalah pemeriksaan status tambahan, bukan
// gerbang: bila baris ditemukan ia wajib cocok, bila belum dimuat fakta
// tetap berlaku (server yang memutuskan 409 saat POST).
import { ApiError, apiFetch, loginUrl, redirectToLogin } from '~/lib/api'

useHead({ title: 'Pembayaran — UMNIA' })

interface Workspace { id: string, name: string, owner_subject_id: string }
interface DraftRow { id: string, workspace_id: string, customer_id: string, catalog_item_id: string, client_request_id: string, status: string, starts_at: string, ends_at: string, created_at: string, updated_at: string }
interface CustomerRow { id: string, workspace_id: string, display_name: string, created_at: string, updated_at: string }
interface CatalogRow { id: string, workspace_id: string, sku: string, name: string, unit: string, status: string, revision: number, created_at: string, updated_at: string }
interface FactRow { id: string, workspace_id: string, rental_contract_id: string, price_version_id: string, pricing_rule_version_id: string, catalog_item_id: string, customer_id: string, client_request_id: string, amount_minor: string, currency: string, units: number, total_minor: string, starts_at: string, ends_at: string, accepted_at: string, accepted_by: string }
interface PaymentProposalRow { id: string, rental_contract_id: string, rental_agreed_price_id: string, amount_minor: string, currency: string, method: string, reference: string | null, proposed_by: string, proposed_at: string, client_request_id: string, status: string }
interface PaymentRow { id: string, rental_contract_id: string, rental_agreed_price_id: string, proposal_id: string, amount_minor: string, currency: string, method: string, reference: string | null, proposed_by: string, proposed_at: string, approved_by: string, approved_at: string, client_request_id: string, reversed: boolean, reversal_id: string | null }
interface ReversalProposalRow { id: string, payment_id: string, rental_contract_id: string, reason: string, proposed_by: string, proposed_at: string, client_request_id: string, status: string }
interface ReversalRow { id: string, payment_id: string, rental_contract_id: string, reversal_proposal_id: string, reason: string, proposed_by: string, proposed_at: string, approved_by: string, approved_at: string, client_request_id: string }
interface HistoryRow { rental_contract_id: string, rental_agreed_price_id: string, currency: string, total_minor: string, paid_minor: string, outstanding_minor: string, payments: PaymentRow[], reversals: ReversalRow[], pending_payment_proposals: PaymentProposalRow[], pending_reversal_proposals: ReversalProposalRow[] }

const PAGE_LIMIT = 50
const PICKER_PAGE = 100
const MAX_INT64 = 9223372036854775807n
const FACT_NO_FACT = 'harga yang disepakati tidak ditemukan'
const FACT_INVALID = 'Fakta harga yang diterima tidak valid.'
const HIST_NO_DRAFT = 'draft tidak ditemukan'
const HIST_NO_FACT = 'harga disepakati belum ada'
const HIST_INVALID = 'Riwayat pembayaran yang diterima tidak valid.'
const KEY_MISMATCH = 'kunci idempotency sudah dipakai untuk permintaan yang berbeda'
const ALREADY_APPROVED = 'proposal sudah disetujui'
const ALREADY_REVERSED = 'pembayaran sudah dibatalkan'
const OVER_CAP = 'pembayaran melebihi sisa'
const FINANCE_INACTIVE = 'finance belum aktif di workspace ini'
const FINANCE_NO_ENTITLEMENT = 'finance belum menjadi hak workspace ini'
const FINANCE_GUIDANCE = 'Minta operator memberikan entitlement finance.basic dan owner mengaktifkan modul Finance Basic.'
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const MONEY_RE = /^(0|[1-9][0-9]*)$/
const CURRENCY_RE = /^[A-Z]{3}$/
const KEY_RE = /^[A-Za-z0-9_.:-]{1,128}$/

// PostgreSQL supplies microseconds. Accept equivalent offsets and zero padding, not silent sub-microsecond truncation.
const SERVER_INSTANT_RE = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,6})0{0,3})?(Z|[+-]\d{2}:\d{2})$/
function parseServerInstant(s: string): bigint | null {
  const m = SERVER_INSTANT_RE.exec(s)
  if (!m) return null
  const y = Number(m[1])
  const mo = Number(m[2])
  const d = Number(m[3])
  const h = Number(m[4])
  const mi = Number(m[5])
  const sec = Number(m[6])
  if (y < 1 || y > 9999 || mo < 1 || mo > 12 || d < 1 || d > 31 || h > 23 || mi > 59 || sec > 59) return null
  const dt = new Date(0)
  dt.setUTCFullYear(y, mo - 1, d)
  dt.setUTCHours(h, mi, sec, 0)
  if (dt.getUTCFullYear() !== y || dt.getUTCMonth() !== mo - 1 || dt.getUTCDate() !== d ||
    dt.getUTCHours() !== h || dt.getUTCMinutes() !== mi || dt.getUTCSeconds() !== sec) return null
  let micros = BigInt(dt.getTime()) * 1000n
  if (m[7]) micros += BigInt(m[7].padEnd(6, '0'))
  if (m[8] !== 'Z') {
    const sign = m[8][0] === '+' ? -1n : 1n
    const offH = BigInt(m[8].slice(1, 3))
    const offM = BigInt(m[8].slice(4, 6))
    if (offH > 23n || offM > 59n) return null
    micros += sign * (offH * 3600n + offM * 60n) * 1000000n
  }
  return micros
}

function runeCount(s: string): number {
  return [...s].length
}

function isCanonicalMoney(s: unknown): s is string {
  if (typeof s !== 'string' || !MONEY_RE.test(s)) return false
  try {
    return BigInt(s) <= MAX_INT64
  } catch {
    return false
  }
}

function isPositiveMoney(s: unknown): s is string {
  if (!isCanonicalMoney(s)) return false
  try {
    return BigInt(s) > 0n
  } catch {
    return false
  }
}

// Referensi: null (kosong) atau 1-128 rune dengan sedikitnya satu rune
// bukan-spasi — sama seperti server (disimpan apa adanya, tanpa trim).
function isValidReference(v: unknown): v is string | null {
  if (v === null) return true
  if (typeof v !== 'string') return false
  if (runeCount(v) < 1 || runeCount(v) > 128) return false
  return [...v].some(r => !/\s/.test(r))
}

// Alasan pembalikan: disimpan server sesudah trim, 1-500 rune.
function isValidReason(v: unknown): v is string {
  if (typeof v !== 'string') return false
  if (v !== v.trim()) return false
  const n = runeCount(v)
  return n >= 1 && n <= 500
}

function normalizeReason(v: string): string | null {
  const t = v.trim()
  const n = runeCount(t)
  if (n < 1 || n > 500) return null
  return t
}

function sameId(left: string, right: string): boolean {
  return left.toLowerCase() === right.toLowerCase()
}

function isValidFactRow(row: unknown, ws: string, draftId: string): row is FactRow {
  if (!row || typeof row !== 'object') return false
  const f = row as FactRow
  if (typeof f.id !== 'string' || !UUID_RE.test(f.id)) return false
  if (f.workspace_id !== ws) return false
  if (typeof f.rental_contract_id !== 'string' || f.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof f.price_version_id !== 'string' || !UUID_RE.test(f.price_version_id)) return false
  if (f.pricing_rule_version_id !== 'elapsed_24h_ceil_v1') return false
  if (typeof f.catalog_item_id !== 'string' || !UUID_RE.test(f.catalog_item_id)) return false
  if (typeof f.customer_id !== 'string' || !UUID_RE.test(f.customer_id)) return false
  if (typeof f.client_request_id !== 'string' || !KEY_RE.test(f.client_request_id)) return false
  if (!isCanonicalMoney(f.amount_minor) || !isCanonicalMoney(f.total_minor)) return false
  if (typeof f.currency !== 'string' || !CURRENCY_RE.test(f.currency)) return false
  if (typeof f.units !== 'number' || !Number.isInteger(f.units) || f.units < 1 || f.units > 3652059) return false
  const from = typeof f.starts_at === 'string' ? parseServerInstant(f.starts_at) : null
  const to = typeof f.ends_at === 'string' ? parseServerInstant(f.ends_at) : null
  if (from === null || to === null || !(to > from)) return false
  if (typeof f.accepted_at !== 'string' || parseServerInstant(f.accepted_at) === null) return false
  if (typeof f.accepted_by !== 'string' || !UUID_RE.test(f.accepted_by)) return false
  try {
    if (BigInt(f.total_minor) !== BigInt(f.amount_minor) * BigInt(f.units)) return false
  } catch {
    return false
  }
  return true
}

function isValidPaymentProposalRow(row: unknown, draftId: string): row is PaymentProposalRow {
  if (!row || typeof row !== 'object') return false
  const p = row as PaymentProposalRow
  if (typeof p.id !== 'string' || !UUID_RE.test(p.id)) return false
  if (typeof p.rental_contract_id !== 'string' || p.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof p.rental_agreed_price_id !== 'string' || !UUID_RE.test(p.rental_agreed_price_id)) return false
  if (!isPositiveMoney(p.amount_minor)) return false
  if (typeof p.currency !== 'string' || !CURRENCY_RE.test(p.currency)) return false
  if (p.method !== 'cash' && p.method !== 'transfer') return false
  if (!isValidReference(p.reference)) return false
  if (typeof p.proposed_by !== 'string' || !UUID_RE.test(p.proposed_by)) return false
  if (typeof p.proposed_at !== 'string' || parseServerInstant(p.proposed_at) === null) return false
  if (typeof p.client_request_id !== 'string' || !KEY_RE.test(p.client_request_id)) return false
  if (p.status !== 'pending') return false
  return true
}

function hasValidApproval(row: PaymentRow | ReversalRow): boolean {
  if (typeof row.proposed_by !== 'string' || !UUID_RE.test(row.proposed_by)) return false
  if (typeof row.approved_by !== 'string' || !UUID_RE.test(row.approved_by) || sameId(row.approved_by, row.proposed_by)) return false
  const proposedAt = typeof row.proposed_at === 'string' ? parseServerInstant(row.proposed_at) : null
  const approvedAt = typeof row.approved_at === 'string' ? parseServerInstant(row.approved_at) : null
  return proposedAt !== null && approvedAt !== null && approvedAt >= proposedAt
}

function isValidPaymentRow(row: unknown, draftId: string): row is PaymentRow {
  if (!row || typeof row !== 'object') return false
  const p = row as PaymentRow
  if (typeof p.id !== 'string' || !UUID_RE.test(p.id)) return false
  if (typeof p.rental_contract_id !== 'string' || p.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof p.rental_agreed_price_id !== 'string' || !UUID_RE.test(p.rental_agreed_price_id)) return false
  if (typeof p.proposal_id !== 'string' || !UUID_RE.test(p.proposal_id)) return false
  if (!isPositiveMoney(p.amount_minor)) return false
  if (typeof p.currency !== 'string' || !CURRENCY_RE.test(p.currency)) return false
  if (p.method !== 'cash' && p.method !== 'transfer') return false
  if (!isValidReference(p.reference)) return false
  if (!hasValidApproval(p)) return false
  if (typeof p.client_request_id !== 'string' || !KEY_RE.test(p.client_request_id)) return false
  if (typeof p.reversed !== 'boolean') return false
  if (p.reversal_id !== null && (typeof p.reversal_id !== 'string' || !UUID_RE.test(p.reversal_id))) return false
  return p.reversed === (p.reversal_id !== null)
}

function isValidReversalProposalRow(row: unknown, draftId: string): row is ReversalProposalRow {
  if (!row || typeof row !== 'object') return false
  const r = row as ReversalProposalRow
  if (typeof r.id !== 'string' || !UUID_RE.test(r.id)) return false
  if (typeof r.payment_id !== 'string' || !UUID_RE.test(r.payment_id)) return false
  if (typeof r.rental_contract_id !== 'string' || r.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (!isValidReason(r.reason)) return false
  if (typeof r.proposed_by !== 'string' || !UUID_RE.test(r.proposed_by)) return false
  if (typeof r.proposed_at !== 'string' || parseServerInstant(r.proposed_at) === null) return false
  if (typeof r.client_request_id !== 'string' || !KEY_RE.test(r.client_request_id)) return false
  if (r.status !== 'pending') return false
  return true
}

function isValidReversalRow(row: unknown, draftId: string): row is ReversalRow {
  if (!row || typeof row !== 'object') return false
  const r = row as ReversalRow
  if (typeof r.id !== 'string' || !UUID_RE.test(r.id)) return false
  if (typeof r.payment_id !== 'string' || !UUID_RE.test(r.payment_id)) return false
  if (typeof r.rental_contract_id !== 'string' || r.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof r.reversal_proposal_id !== 'string' || !UUID_RE.test(r.reversal_proposal_id)) return false
  if (!isValidReason(r.reason)) return false
  if (!hasValidApproval(r)) return false
  if (typeof r.client_request_id !== 'string' || !KEY_RE.test(r.client_request_id)) return false
  return true
}

function hasUniqueIds(ids: string[]): boolean {
  return new Set(ids.map(id => id.toLowerCase())).size === ids.length
}

function hasUniqueStageRows(rows: readonly { id: string, client_request_id: string }[]): boolean {
  return hasUniqueIds(rows.map(row => row.id)) && new Set(rows.map(row => row.client_request_id)).size === rows.length
}

function followsPayment(proposal: ReversalProposalRow | ReversalRow, payment: PaymentRow | undefined): boolean {
  if (!payment || !sameId(payment.id, proposal.payment_id)) return false
  const approvedAt = parseServerInstant(payment.approved_at)
  const proposedAt = parseServerInstant(proposal.proposed_at)
  return approvedAt !== null && proposedAt !== null && proposedAt >= approvedAt
}

function isLinkedReversal(reversal: ReversalRow, payment: PaymentRow | undefined): boolean {
  return !!payment?.reversed && payment.reversal_id !== null &&
    sameId(payment.reversal_id, reversal.id) && followsPayment(reversal, payment)
}

function hasValidReversalLinks(snapshot: HistoryRow): boolean {
  // With unique payment/reversal IDs, equal counts plus reciprocal links verify every flag in this GET.
  if (snapshot.payments.filter(payment => payment.reversed).length !== snapshot.reversals.length) return false
  const payments = new Map(snapshot.payments.map(payment => [payment.id.toLowerCase(), payment]))
  return snapshot.reversals.every(row => isLinkedReversal(row, payments.get(row.payment_id.toLowerCase()))) &&
    snapshot.pending_reversal_proposals.every(row => followsPayment(row, payments.get(row.payment_id.toLowerCase())))
}

function isValidHistoryRow(row: unknown, draftId: string, expectedFact: FactRow | null): row is HistoryRow {
  if (!row || typeof row !== 'object') return false
  const h = row as HistoryRow
  if (typeof h.rental_contract_id !== 'string' || h.rental_contract_id.toLowerCase() !== draftId.toLowerCase()) return false
  if (typeof h.rental_agreed_price_id !== 'string' || !UUID_RE.test(h.rental_agreed_price_id)) return false
  if (expectedFact && (!sameId(h.rental_agreed_price_id, expectedFact.id) ||
    h.currency !== expectedFact.currency || h.total_minor !== expectedFact.total_minor)) return false
  if (typeof h.currency !== 'string' || !CURRENCY_RE.test(h.currency)) return false
  if (!isCanonicalMoney(h.total_minor) || !isCanonicalMoney(h.paid_minor) || !isCanonicalMoney(h.outstanding_minor)) return false
  if (!Array.isArray(h.payments) || !Array.isArray(h.reversals) ||
    !Array.isArray(h.pending_payment_proposals) || !Array.isArray(h.pending_reversal_proposals)) return false
  const matchesFact = (payment: PaymentRow | PaymentProposalRow) =>
    sameId(payment.rental_agreed_price_id, h.rental_agreed_price_id) && payment.currency === h.currency
  if (!h.payments.every(r => isValidPaymentRow(r, draftId) && matchesFact(r))) return false
  if (!h.reversals.every(r => isValidReversalRow(r, draftId))) return false
  if (!h.pending_payment_proposals.every(r => isValidPaymentProposalRow(r, draftId) && matchesFact(r))) return false
  if (!h.pending_reversal_proposals.every(r => isValidReversalProposalRow(r, draftId))) return false
  // Keys are unique per stage, not across stages. A posted proposal cannot also be pending in this snapshot.
  return [h.payments, h.reversals, h.pending_payment_proposals, h.pending_reversal_proposals].every(hasUniqueStageRows) &&
    hasUniqueIds([...h.payments.map(p => p.proposal_id), ...h.pending_payment_proposals.map(p => p.id)]) &&
    hasUniqueIds([...h.reversals.map(r => r.reversal_proposal_id), ...h.pending_reversal_proposals.map(r => r.id)]) &&
    hasUniqueIds(h.reversals.map(r => r.payment_id)) &&
    hasUniqueIds(h.payments.flatMap(p => p.reversal_id === null ? [] : [p.reversal_id]))
}

function isValidDraftForFact(row: unknown, ws: string, draftId: string, f: FactRow): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id.toLowerCase() !== draftId.toLowerCase()) return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || !sameId(r.customer_id, f.customer_id)) return false
  if (typeof r.catalog_item_id !== 'string' || r.catalog_item_id.toLowerCase() !== f.catalog_item_id.toLowerCase()) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
  return from === parseServerInstant(f.starts_at) && to === parseServerInstant(f.ends_at)
}

function isValidDraftBasic(row: unknown, ws: string, draftId: string): row is DraftRow {
  if (!row || typeof row !== 'object') return false
  const r = row as DraftRow
  if (typeof r.id !== 'string' || r.id.toLowerCase() !== draftId.toLowerCase()) return false
  if (r.workspace_id !== ws) return false
  if (typeof r.customer_id !== 'string' || r.customer_id === '') return false
  if (typeof r.catalog_item_id !== 'string' || !UUID_RE.test(r.catalog_item_id)) return false
  if (r.status !== 'draft') return false
  const from = typeof r.starts_at === 'string' ? parseServerInstant(r.starts_at) : null
  const to = typeof r.ends_at === 'string' ? parseServerInstant(r.ends_at) : null
  return from !== null && to !== null && to > from
}

// Penjaga generasi: hasil muat yang tiba sesudah reset/pindah draf/
// workspace/logout diabaikan. inFlight dijaga sinkron (bukan hanya tombol
// disabled). *Gen: kepemilikan POST per tahap — kelanjutan POST basi
// (sukses, galat, nextTick, refresh) tidak boleh menyentuh halaman/sesi/
// daftar pengganti. *LoadGen: kepemilikan flag busy — finally basi tidak
// boleh menghapus busy milik permintaan baru.
interface PropAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  factId: string
  amount: string
  currency: string
  method: string
  reference: string | null
  customerLabel: string
  itemLabel: string
  fromIso: string
  toIso: string
}

interface ApprAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  factId: string
  proposalId: string
  proposedBy: string
  proposedAt: string
  amount: string
  currency: string
  method: string
  reference: string | null
  customerLabel: string
  itemLabel: string
  fromIso: string
  toIso: string
}

interface RevPropAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  factId: string
  paymentId: string
  paymentAmount: string
  currency: string
  reason: string
  customerLabel: string
  itemLabel: string
  fromIso: string
  toIso: string
}

interface RevApprAttempt {
  key: string
  workspaceId: string
  draftId: string
  actorId: string
  factId: string
  paymentId: string
  proposalId: string
  proposedBy: string
  proposedAt: string
  reason: string
  customerLabel: string
  itemLabel: string
  fromIso: string
  toIso: string
}

function isValidProposalForAttempt(row: unknown, a: PropAttempt): row is PaymentProposalRow {
  if (!isValidPaymentProposalRow(row, a.draftId)) return false
  const p = row as PaymentProposalRow
  return p.client_request_id === a.key &&
    p.rental_agreed_price_id.toLowerCase() === a.factId.toLowerCase() &&
    p.amount_minor === a.amount && p.currency === a.currency &&
    p.method === a.method && p.reference === a.reference &&
    p.proposed_by === a.actorId
}

function matchesPaymentFields(row: PaymentProposalRow | PaymentRow, attempt: ApprAttempt): boolean {
  return sameId(row.rental_contract_id, attempt.draftId) && sameId(row.rental_agreed_price_id, attempt.factId) &&
    row.amount_minor === attempt.amount && row.currency === attempt.currency &&
    row.method === attempt.method && row.reference === attempt.reference &&
    sameId(row.proposed_by, attempt.proposedBy) && parseServerInstant(row.proposed_at) === parseServerInstant(attempt.proposedAt)
}

function isValidPaymentForApproveAttempt(row: unknown, a: ApprAttempt): row is PaymentRow {
  return isValidPaymentRow(row, a.draftId) && row.client_request_id === a.key &&
    sameId(row.approved_by, a.actorId) && sameId(row.proposal_id, a.proposalId) && matchesPaymentFields(row, a)
}

function isValidReversalProposalForAttempt(row: unknown, a: RevPropAttempt): row is ReversalProposalRow {
  if (!isValidReversalProposalRow(row, a.draftId)) return false
  const r = row as ReversalProposalRow
  return r.client_request_id === a.key &&
    r.payment_id.toLowerCase() === a.paymentId.toLowerCase() &&
    r.reason === a.reason && r.proposed_by === a.actorId
}

function matchesReversalFields(row: ReversalProposalRow | ReversalRow, attempt: RevApprAttempt): boolean {
  return sameId(row.rental_contract_id, attempt.draftId) && sameId(row.payment_id, attempt.paymentId) &&
    row.reason === attempt.reason && sameId(row.proposed_by, attempt.proposedBy) &&
    parseServerInstant(row.proposed_at) === parseServerInstant(attempt.proposedAt)
}

function isValidReversalForApproveAttempt(row: unknown, a: RevApprAttempt): row is ReversalRow {
  return isValidReversalRow(row, a.draftId) && row.client_request_id === a.key &&
    sameId(row.approved_by, a.actorId) && sameId(row.reversal_proposal_id, a.proposalId) && matchesReversalFields(row, a)
}

type ApprovalEvidence = 'own' | 'independent' | 'inconsistent' | 'absent'

function classifyPaymentApproval(snapshot: HistoryRow, a: ApprAttempt): ApprovalEvidence {
  const keyed = snapshot.payments.find(payment => payment.client_request_id === a.key)
  if (keyed) return isValidPaymentForApproveAttempt(keyed, a) ? 'own' : 'inconsistent'
  const other = snapshot.payments.find(payment => sameId(payment.proposal_id, a.proposalId))
  if (!other) return 'absent'
  return matchesPaymentFields(other, a) ? 'independent' : 'inconsistent'
}

function classifyReversalApproval(snapshot: HistoryRow, a: RevApprAttempt): ApprovalEvidence {
  const reversal = snapshot.reversals.find(row => row.client_request_id === a.key) ??
    snapshot.reversals.find(row => sameId(row.reversal_proposal_id, a.proposalId)) ??
    snapshot.reversals.find(row => sameId(row.payment_id, a.paymentId))
  if (!reversal) return 'absent'
  if (!isLinkedReversal(reversal, snapshot.payments.find(payment => sameId(payment.id, a.paymentId)))) return 'inconsistent'
  if (reversal.client_request_id === a.key) return isValidReversalForApproveAttempt(reversal, a) ? 'own' : 'inconsistent'
  if (sameId(reversal.reversal_proposal_id, a.proposalId) && !matchesReversalFields(reversal, a)) return 'inconsistent'
  return 'independent'
}

const route = useRoute()
const session = useNuxtApp().$session ?? null

const wsId = computed(() => String(route.params.id ?? ''))
const draftId = computed(() => String(route.params.draftId ?? ''))
const routeKey = computed(() => `${wsId.value}/${draftId.value}`)
const subjectId = computed(() => session?.subjectId.value ?? '')

type AttemptScope = Pick<PropAttempt, 'workspaceId' | 'draftId' | 'actorId' | 'factId'>

function isCurrentAttempt(a: AttemptScope): boolean {
  return !!session?.token.value && a.workspaceId === wsId.value && a.draftId === draftId.value && a.actorId === subjectId.value
}

function isAttemptHistory(snapshot: HistoryRow, a: AttemptScope): boolean {
  return isCurrentAttempt(a) && sameId(snapshot.rental_contract_id, a.draftId) && sameId(snapshot.rental_agreed_price_id, a.factId)
}

const checking = ref(true)
const loading = ref(false)
const pageError = ref<string | null>(null)
const noPermission = ref(false)
const readDenied = ref(false)
const workspace = ref<Workspace | null>(null)

const fact = ref<FactRow | null>(null)
const factLoading = ref(false)
const factError = ref<string | null>(null)
const factMissing = ref(false)

const history = ref<HistoryRow | null>(null)
const historyLoading = ref(false)
const historyError = ref<string | null>(null)

const draftRows = ref<DraftRow[]>([])
const draftRow = ref<DraftRow | null>(null)
const draftValid = ref(false)
const draftLoading = ref(false)
const draftError = ref<string | null>(null)
const draftOffset = ref(0)
const draftExhausted = ref(false)
const draftStarted = ref(false)

const customerRows = ref<CustomerRow[]>([])
const customerOffset = ref(0)
const customerExhausted = ref(false)
const customerLoading = ref(false)
const customerLoadError = ref<string | null>(null)
const customerStarted = ref(false)
const customerById = computed(() => new Map(customerRows.value.map(r => [r.id, r])))

const catalogRows = ref<CatalogRow[]>([])
const catalogOffset = ref(0)
const catalogExhausted = ref(false)
const catalogLoading = ref(false)
const catalogError = ref<string | null>(null)
const catalogStarted = ref(false)
const catalogById = computed(() => new Map(catalogRows.value.map(r => [r.id, r])))

const amountInput = ref('')
const methodInput = ref<'cash' | 'transfer'>('cash')
const referenceInput = ref('')
const reversalReasonInput = ref('')
const propFormError = ref<string | null>(null)
const revPropFormError = ref<string | null>(null)

const propAttempt = ref<PropAttempt | null>(null)
const propUnknown = ref(false)
const propConflict = ref<'key' | null>(null)
const propPostDenied = ref<string | null>(null)
const propReplayError = ref<string | null>(null)
const propSettleError = ref<string | null>(null)
const propBusy = ref(false)

const apprAttempt = ref<ApprAttempt | null>(null)
const apprUnknown = ref(false)
const apprConflict = ref<'key' | null>(null)
const apprPostDenied = ref<string | null>(null)
const apprReplayError = ref<string | null>(null)
const apprSettleError = ref<string | null>(null)
const paymentPostedElsewhere = ref<Pick<ApprAttempt, 'proposalId' | 'amount' | 'currency'> | null>(null)
const apprBusy = ref(false)

const revPropAttempt = ref<RevPropAttempt | null>(null)
const revPropUnknown = ref(false)
const revPropConflict = ref<'key' | null>(null)
const revPropPostDenied = ref<string | null>(null)
const revPropReplayError = ref<string | null>(null)
const revPropSettleError = ref<string | null>(null)
const revPropBusy = ref(false)

const revApprAttempt = ref<RevApprAttempt | null>(null)
const revApprUnknown = ref(false)
const revApprConflict = ref<'key' | null>(null)
const revApprPostDenied = ref<string | null>(null)
const revApprReplayError = ref<string | null>(null)
const revApprSettleError = ref<string | null>(null)
const paymentReversedElsewhere = ref<string | null>(null)
const revApprBusy = ref(false)

const refreshing = ref(false)
const paymentsHeading = ref<HTMLElement | null>(null)
const paymentsSection = ref<HTMLElement | null>(null)

const contextCustomerId = computed(() => draftRow.value?.customer_id ?? fact.value?.customer_id ?? null)
const contextItemId = computed(() => draftRow.value?.catalog_item_id ?? fact.value?.catalog_item_id ?? null)
const contextFromIso = computed(() => draftRow.value?.starts_at ?? fact.value?.starts_at ?? null)
const contextToIso = computed(() => draftRow.value?.ends_at ?? fact.value?.ends_at ?? null)

const showCustomerMore = computed(() => !!contextCustomerId.value && !customerById.value.get(contextCustomerId.value) && !customerExhausted.value)
const showCatalogMore = computed(() => !!contextItemId.value && !catalogById.value.get(contextItemId.value) && !catalogExhausted.value)

// Keep immutable posting evidence separate: inconsistent reversal status blocks display and new writes, not proof of posting.
const historyLinksVerified = computed(() => {
  const snapshot = history.value
  if (!snapshot || !hasValidReversalLinks(snapshot)) return false
  const attempt = revApprAttempt.value
  return !attempt || !isAttemptHistory(snapshot, attempt) || classifyReversalApproval(snapshot, attempt) !== 'inconsistent'
})

const writeReadsReady = computed(() => {
  const price = fact.value
  const snapshot = history.value
  if (!session?.token.value || !UUID_RE.test(subjectId.value) || loading.value || noPermission.value || readDenied.value) return false
  if (refreshing.value || factLoading.value || historyLoading.value || !price || !snapshot || !historyLinksVerified.value) return false
  return sameId(price.workspace_id, wsId.value) && sameId(price.rental_contract_id, draftId.value) &&
    sameId(price.id, snapshot.rental_agreed_price_id) && sameId(snapshot.rental_contract_id, draftId.value) &&
    price.currency === snapshot.currency && price.total_minor === snapshot.total_minor &&
    (!draftRow.value || isValidDraftForFact(draftRow.value, wsId.value, draftId.value, price))
})
const propTargetLive = computed(() => writeReadsReady.value && !!propAttempt.value && !!history.value &&
  isAttemptHistory(history.value, propAttempt.value) && propAttempt.value.currency === history.value.currency)
const apprTargetLive = computed(() => {
  const a = apprAttempt.value
  if (!writeReadsReady.value || !a || !history.value || !isAttemptHistory(history.value, a)) return false
  if (sameId(a.proposedBy, a.actorId)) return false
  return history.value.pending_payment_proposals.some(p => sameId(p.id, a.proposalId) && matchesPaymentFields(p, a))
})
const revPropTargetLive = computed(() => {
  const a = revPropAttempt.value
  if (!writeReadsReady.value || !a || !history.value || !isAttemptHistory(history.value, a)) return false
  const pay = history.value.payments.find(p => p.id.toLowerCase() === a.paymentId.toLowerCase())
  return !!pay && !pay.reversed && pay.amount_minor === a.paymentAmount && pay.currency === a.currency
})
const revApprTargetLive = computed(() => {
  const a = revApprAttempt.value
  if (!writeReadsReady.value || !a || !history.value || !isAttemptHistory(history.value, a)) return false
  if (sameId(a.proposedBy, a.actorId)) return false
  if (!history.value.pending_reversal_proposals.some(r => sameId(r.id, a.proposalId) && matchesReversalFields(r, a))) return false
  const pay = history.value.payments.find(p => p.id.toLowerCase() === a.paymentId.toLowerCase())
  return !!pay && !pay.reversed
})

function isPaymentReversed(paymentId: string): boolean {
  const pay = history.value?.payments.find(p => p.id.toLowerCase() === paymentId.toLowerCase())
  return !!pay && pay.reversed
}

function isFinanceDenied(message: string | null): boolean {
  return message === FINANCE_INACTIVE || message === FINANCE_NO_ENTITLEMENT
}

function customerLabel(customerId: string): string {
  const row = customerById.value.get(customerId)
  if (row) return row.display_name
  return customerExhausted.value ? 'Nama pelanggan tidak tersedia' : 'Nama pelanggan belum dimuat'
}

function catalogLabel(catalogItemId: string): string {
  const row = catalogById.value.get(catalogItemId)
  if (row) return `${row.sku} — ${row.name}` + (row.status === 'active' ? '' : ' (arsip — riwayat)')
  return catalogExhausted.value ? 'Nama barang tidak tersedia' : 'Nama barang belum dimuat'
}

function methodLabel(method: string): string {
  return method === 'transfer' ? 'Transfer' : 'Tunai'
}

let listGen = 0
let factGen = 0
let historyGen = 0
let draftGen = 0
let draftLoadGen = 0
let customerGen = 0
let customerLoadGen = 0
let catalogGen = 0
let catalogLoadGen = 0
let refreshGen = 0
let propGen = 0
let propLoadGen = 0
let apprGen = 0
let apprLoadGen = 0
let revPropGen = 0
let revPropLoadGen = 0
let revApprGen = 0
let revApprLoadGen = 0
let draftInFlight = false
let customerInFlight = false
let catalogInFlight = false
let propInFlight = false
let apprInFlight = false
let revPropInFlight = false
let revApprInFlight = false
let lastFocusedStage: string | undefined

function stale(gen: number) {
  return gen !== listGen
}

function staleProp(gen: number) {
  return gen !== propGen
}

function staleAppr(gen: number) {
  return gen !== apprGen
}

function staleRevProp(gen: number) {
  return gen !== revPropGen
}

function staleRevAppr(gen: number) {
  return gen !== revApprGen
}

function bumpAllGens() {
  lastFocusedStage = undefined
  listGen++
  factGen++
  historyGen++
  draftGen++
  draftLoadGen++
  customerGen++
  customerLoadGen++
  catalogGen++
  catalogLoadGen++
  refreshGen++
  propGen++
  propLoadGen++
  apprGen++
  apprLoadGen++
  revPropGen++
  revPropLoadGen++
  revApprGen++
  revApprLoadGen++
}

function clearPropStage() {
  propAttempt.value = null
  propUnknown.value = false
  propConflict.value = null
  propPostDenied.value = null
  propReplayError.value = null
  propSettleError.value = null
}

function clearApprStage() {
  apprAttempt.value = null
  apprUnknown.value = false
  apprConflict.value = null
  apprPostDenied.value = null
  apprReplayError.value = null
  apprSettleError.value = null
  paymentPostedElsewhere.value = null
}

function clearRevPropStage() {
  revPropAttempt.value = null
  revPropUnknown.value = false
  revPropConflict.value = null
  revPropPostDenied.value = null
  revPropReplayError.value = null
  revPropSettleError.value = null
}

function clearRevApprStage() {
  revApprAttempt.value = null
  revApprUnknown.value = false
  revApprConflict.value = null
  revApprPostDenied.value = null
  revApprReplayError.value = null
  revApprSettleError.value = null
  paymentReversedElsewhere.value = null
}

// Transisi baca ditolak (GET 403 sesudah halaman termuat): beri umpan
// balik izin workspace, sembunyikan konten yang sudah termuat, dan
// batalkan kepemilikan baca/POST yang tertunda supaya penyelesaian
// terlambat tidak mengisi ulang halaman pengganti. Sesi dipertahankan
// — 403 bukan 401. Berbeda dengan POST 403 yang mempertahankan data
// yang terbaca + snapshot percobaan. Hanya berlaku untuk bacaan inti
// (fakta/riwayat/draf/label pelanggan/katalog).
function denyRead(message: string) {
  bumpAllGens()
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  propInFlight = false
  apprInFlight = false
  revPropInFlight = false
  revApprInFlight = false
  propBusy.value = false
  apprBusy.value = false
  revPropBusy.value = false
  revApprBusy.value = false
  refreshing.value = false
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  history.value = null
  historyLoading.value = false
  historyError.value = null
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftLoading.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  customerLoading.value = false
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  clearPropStage()
  clearApprStage()
  clearRevPropStage()
  clearRevApprStage()
  // Penolakan saat ini menyelesaikan loader global: penolakan awal
  // memanggil denyRead yang menaikkan listGen, sehingga finalizer load()
  // yang lama tidak bisa lagi mematikan loading — tanpa baris ini
  // "Memuat…" bertahan selamanya di atas penolakan.
  loading.value = false
  readDenied.value = true
  pageError.value = message
}

function resetState() {
  bumpAllGens()
  draftInFlight = false
  customerInFlight = false
  catalogInFlight = false
  propInFlight = false
  apprInFlight = false
  revPropInFlight = false
  revApprInFlight = false
  propBusy.value = false
  apprBusy.value = false
  revPropBusy.value = false
  revApprBusy.value = false
  refreshing.value = false
  loading.value = false
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  workspace.value = null
  fact.value = null
  factLoading.value = false
  factError.value = null
  factMissing.value = false
  history.value = null
  historyLoading.value = false
  historyError.value = null
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftLoading.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
  customerRows.value = []
  customerOffset.value = 0
  customerExhausted.value = false
  customerLoadError.value = null
  customerLoading.value = false
  customerStarted.value = false
  catalogRows.value = []
  catalogOffset.value = 0
  catalogExhausted.value = false
  catalogError.value = null
  catalogLoading.value = false
  catalogStarted.value = false
  amountInput.value = ''
  methodInput.value = 'cash'
  referenceInput.value = ''
  reversalReasonInput.value = ''
  propFormError.value = null
  revPropFormError.value = null
  clearPropStage()
  clearApprStage()
  clearRevPropStage()
  clearRevApprStage()
}

onMounted(async () => {
  if (!session?.token.value) {
    navigateTo(loginUrl(route.fullPath))
    return
  }
  checking.value = false
  await load()
})

watch([routeKey, subjectId], async () => {
  resetState()
  if (session?.token.value) await load()
})

onUnmounted(() => {
  bumpAllGens()
})

async function load() {
  const s = session
  if (!s) return
  const gen = ++listGen
  loading.value = true
  pageError.value = null
  noPermission.value = false
  readDenied.value = false
  if (!UUID_RE.test(wsId.value) || !UUID_RE.test(draftId.value)) {
    pageError.value = 'id workspace atau id draf tidak valid'
    if (!stale(gen)) loading.value = false
    return
  }
  try {
    const ws = await apiFetch<Workspace>(s, `/api/workspaces/${wsId.value}`)
    if (stale(gen)) return
    workspace.value = ws.data
  } catch (err) {
    if (stale(gen)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
    } else if (err instanceof ApiError && err.status === 403) {
      noPermission.value = true
      pageError.value = err.message
    } else if (err instanceof ApiError) pageError.value = err.message
    else pageError.value = 'Terjadi kesalahan. Coba lagi.'
    if (!stale(gen)) loading.value = false
    return
  }
  if (!stale(gen)) await fetchFact()
  if (readDenied.value) return
  if (!stale(gen)) await fetchHistory()
  if (readDenied.value) return
  if (!stale(gen) && !customerStarted.value) {
    customerStarted.value = true
    await loadCustomerPage()
  }
  if (readDenied.value) return
  if (!stale(gen) && !catalogStarted.value) {
    catalogStarted.value = true
    await loadItemPage()
  }
  if (readDenied.value) return
  if (!stale(gen)) await maybeStartDraftLookup()
  if (readDenied.value) return
  if (!stale(gen)) loading.value = false
}

// Satu bacaan fakta. Tanpa-fakta HANYA pesan 404 spesifik; 404 lain
// (draf hilang) dan galat lain adalah kegagalan baca, bukan "belum ada".
async function fetchFact() {
  const s = session
  if (!s?.token.value) return
  const gen = ++factGen
  factLoading.value = true
  try {
    const res = await apiFetch<FactRow>(s, `/api/workspaces/${wsId.value}/rental/contracts/${draftId.value}/agreed-price`)
    if (gen !== factGen || !s.token.value) return
    if (res.status === 200 && isValidFactRow(res.data, wsId.value, draftId.value)) {
      fact.value = res.data
      factMissing.value = false
      factError.value = null
    } else {
      fact.value = null
      factMissing.value = false
      factError.value = FACT_INVALID
    }
  } catch (err) {
    if (gen !== factGen || !s.token.value) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    fact.value = null
    if (err instanceof ApiError && err.status === 404 && err.message === FACT_NO_FACT) {
      factMissing.value = true
      factError.value = null
    } else if (err instanceof ApiError) {
      factMissing.value = false
      factError.value = err.message
    } else {
      factMissing.value = false
      factError.value = 'Terjadi kesalahan. Coba lagi.'
    }
  } finally {
    if (gen === factGen) factLoading.value = false
  }
}

// Satu bacaan riwayat + total. Tanpa-fakta HANYA pesan 409 spesifik;
// draf hilang HANYA pesan 404 spesifik; galat lain adalah kegagalan
// baca, bukan "belum ada". Total dibaca dari server apa adanya.
async function fetchHistory(): Promise<HistoryRow | null> {
  const s = session
  if (!s?.token.value) return null
  const gen = ++historyGen
  historyLoading.value = true
  try {
    const res = await apiFetch<HistoryRow>(s, `/api/workspaces/${wsId.value}/rental/contracts/${draftId.value}/payments`)
    if (gen !== historyGen || !s.token.value) return null
    if (res.status === 200 && isValidHistoryRow(res.data, draftId.value, fact.value)) {
      history.value = res.data
      historyError.value = null
      return res.data
    }
    history.value = null
    historyError.value = HIST_INVALID
  } catch (err) {
    if (gen !== historyGen || !s.token.value) return null
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return null
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return null
    }
    history.value = null
    if (err instanceof ApiError && err.status === 409 && err.message === HIST_NO_FACT) {
      factMissing.value = true
      historyError.value = null
    } else if (err instanceof ApiError) {
      historyError.value = err.message
    } else {
      historyError.value = 'Terjadi kesalahan. Coba lagi.'
    }
  } finally {
    if (gen === historyGen) historyLoading.value = false
  }
  return null
}

// Satu halaman pelanggan (100 baris). Tanpa rantai otomatis: pemanggil
// berikutnya hanyalah tombol "Muat lebih banyak pelanggan" atau "Coba
// lagi" (yang memakai offset yang sama). Kegagalan mempertahankan baris,
// peta, dan kursor yang sudah ada.
async function loadCustomerPage() {
  const s = session
  if (!s || customerInFlight) return
  const gen = customerGen
  const myLoad = ++customerLoadGen
  const wantOffset = customerOffset.value
  customerInFlight = true
  customerLoading.value = true
  customerLoadError.value = null
  try {
    const res = await apiFetch<{ items: CustomerRow[] }>(s, `/api/workspaces/${wsId.value}/customers?limit=${PICKER_PAGE}&offset=${wantOffset}`)
    if (gen !== customerGen) return
    const seen = new Set(customerRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        customerRows.value.push(row)
        seen.add(row.id)
      }
    }
    customerOffset.value = wantOffset + res.data.items.length
    customerExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== customerGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) customerLoadError.value = err.message
    else customerLoadError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === customerLoadGen) {
      customerInFlight = false
      customerLoading.value = false
    }
  }
}

// Satu halaman katalog (100 baris). Tanpa rantai otomatis; offset maju
// menurut jumlah mentah yang diterima, bukan jumlah render.
async function loadItemPage() {
  const s = session
  if (!s || catalogInFlight) return
  const gen = catalogGen
  const myLoad = ++catalogLoadGen
  const wantOffset = catalogOffset.value
  catalogInFlight = true
  catalogLoading.value = true
  catalogError.value = null
  try {
    const res = await apiFetch<{ items: CatalogRow[] }>(s, `/api/workspaces/${wsId.value}/catalog/items?limit=${PICKER_PAGE}&offset=${wantOffset}`)
    if (gen !== catalogGen) return
    const seen = new Set(catalogRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        catalogRows.value.push(row)
        seen.add(row.id)
      }
    }
    catalogOffset.value = wantOffset + res.data.items.length
    catalogExhausted.value = res.data.items.length < PICKER_PAGE
  } catch (err) {
    if (gen !== catalogGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) catalogError.value = err.message
    else catalogError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === catalogLoadGen) {
      catalogInFlight = false
      catalogLoading.value = false
    }
  }
}

// Satu halaman koleksi draf (50 baris) untuk konteks draf yang dipilih.
// Hanya berjalan sesudah status fakta terverifikasi (ada atau tanpa);
// tanpa rantai otomatis dan tanpa sapuan sampai habis. Baris yang
// ditemukan harus cocok dengan fakta (bila ada) sebelum dipakai sebagai
// konteks; bila fakta belum ada, cukup id/workspace/status `draft`.
async function loadDraftPage() {
  const s = session
  if (!s || draftInFlight) return
  const gen = draftGen
  const myLoad = ++draftLoadGen
  const wantOffset = draftOffset.value
  draftInFlight = true
  draftLoading.value = true
  draftError.value = null
  try {
    const res = await apiFetch<{ items: DraftRow[] }>(s, `/api/workspaces/${wsId.value}/rental/contracts?limit=${PAGE_LIMIT}&offset=${wantOffset}`)
    if (gen !== draftGen) return
    const seen = new Set(draftRows.value.map(r => r.id))
    for (const row of res.data.items) {
      if (!seen.has(row.id)) {
        draftRows.value.push(row)
        seen.add(row.id)
      }
    }
    draftOffset.value = wantOffset + res.data.items.length
    draftExhausted.value = res.data.items.length < PAGE_LIMIT
    const found = draftRows.value.find(r => r.id.toLowerCase() === draftId.value.toLowerCase())
    if (found) {
      draftRow.value = found
      draftValid.value = fact.value
        ? isValidDraftForFact(found, wsId.value, draftId.value, fact.value)
        : isValidDraftBasic(found, wsId.value, draftId.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan harga yang dicatat.'
    } else if (draftExhausted.value) {
      draftError.value = `Draf tidak ditemukan setelah memuat seluruh daftar (${draftRows.value.length} baris).`
    }
  } catch (err) {
    if (gen !== draftGen) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 403) {
      denyRead(err.message)
      return
    }
    if (err instanceof ApiError) draftError.value = err.message
    else draftError.value = 'Terjadi kesalahan. Coba lagi.'
  } finally {
    if (myLoad === draftLoadGen) {
      draftInFlight = false
      draftLoading.value = false
    }
  }
}

async function maybeStartDraftLookup() {
  if (!fact.value && !factMissing.value) return
  if (draftStarted.value) return
  draftStarted.value = true
  await loadDraftPage()
}

function resetDraftLookup() {
  draftGen++
  draftLoadGen++
  draftRows.value = []
  draftRow.value = null
  draftValid.value = false
  draftError.value = null
  draftOffset.value = 0
  draftExhausted.value = false
  draftStarted.value = false
}

async function settlePaymentProposal(snapshot: HistoryRow): Promise<void> {
  const a = propAttempt.value
  if (!a || propConflict.value || !isAttemptHistory(snapshot, a)) return
  const row = snapshot.pending_payment_proposals.find(proposal => proposal.client_request_id === a.key)
  if (!row) return
  if (isValidProposalForAttempt(row, a)) return confirmProp(++propGen, 'history')
  propSettleError.value = 'Usulan pembayaran yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; usulan pembayaran tetap tidak diketahui.'
}

async function settlePaymentApproval(snapshot: HistoryRow): Promise<void> {
  const a = apprAttempt.value
  if (!a || apprConflict.value || !isAttemptHistory(snapshot, a)) return
  const evidence = classifyPaymentApproval(snapshot, a)
  if (evidence === 'own' || evidence === 'independent') return finishPaymentApproval(++apprGen, evidence, 'history')
  if (evidence === 'inconsistent') apprSettleError.value = 'Catatan pembayaran tidak cocok dengan permintaan ini; hasil persetujuan belum dapat dipastikan.'
}

async function settleReversalProposal(snapshot: HistoryRow): Promise<void> {
  const a = revPropAttempt.value
  if (!a || revPropConflict.value || !isAttemptHistory(snapshot, a)) return
  const row = snapshot.pending_reversal_proposals.find(proposal => proposal.client_request_id === a.key)
  if (!row) return
  if (isValidReversalProposalForAttempt(row, a)) return confirmRevProp(++revPropGen, 'history')
  revPropSettleError.value = 'Usulan pembalikan yang dibaca memakai kunci yang sama tetapi tidak cocok dengan permintaan ini; usulan pembalikan tetap tidak diketahui.'
}

async function settleReversalApproval(snapshot: HistoryRow): Promise<void> {
  const a = revApprAttempt.value
  if (!a || revApprConflict.value || !isAttemptHistory(snapshot, a)) return
  const evidence = classifyReversalApproval(snapshot, a)
  if (evidence === 'own' || evidence === 'independent') return finishReversalApproval(++revApprGen, evidence, 'history')
  if (evidence === 'inconsistent') revApprSettleError.value = 'Catatan pembalikan tidak cocok dengan permintaan ini; hasil persetujuan belum dapat dipastikan.'
}

async function refreshReads(failureMessage: string, afterWrite = false): Promise<void> {
  const s = session
  if (!s?.token.value || readDenied.value || (!afterWrite && (loading.value || refreshing.value))) return
  const gen = ++refreshGen
  if (afterWrite) {
    // The in-flight GET can predate this POST. Fence it now, not after the new price read.
    historyGen++
    history.value = null
    historyError.value = null
    historyLoading.value = true
  }
  refreshing.value = true
  propSettleError.value = null
  apprSettleError.value = null
  revPropSettleError.value = null
  revApprSettleError.value = null
  try {
    await fetchFact()
    if (gen !== refreshGen || readDenied.value || !s.token.value) return
    const snapshot = await fetchHistory()
    if (gen !== refreshGen || readDenied.value || !s.token.value) return
    if (snapshot) {
      // Fence a settled stage before yielding to a delayed POST response or moving focus.
      await Promise.all([
        settlePaymentProposal(snapshot), settlePaymentApproval(snapshot),
        settleReversalProposal(snapshot), settleReversalApproval(snapshot)
      ])
      if (gen !== refreshGen) return
    } else if (historyError.value) {
      if (propAttempt.value) propSettleError.value = 'Riwayat pembayaran tidak dapat dibaca saat ini; usulan pembayaran tetap tidak diketahui.'
      if (apprAttempt.value) apprSettleError.value = 'Riwayat pembayaran tidak dapat dibaca saat ini; persetujuan pembayaran tetap tidak diketahui.'
      if (revPropAttempt.value) revPropSettleError.value = 'Riwayat pembayaran tidak dapat dibaca saat ini; usulan pembalikan tetap tidak diketahui.'
      if (revApprAttempt.value) revApprSettleError.value = 'Riwayat pembayaran tidak dapat dibaca saat ini; persetujuan pembalikan tetap tidak diketahui.'
    }
    if (!propAttempt.value && !apprAttempt.value && !revPropAttempt.value && !revApprAttempt.value && fact.value && draftRow.value) {
      draftValid.value = isValidDraftForFact(draftRow.value, wsId.value, draftId.value, fact.value)
      if (!draftValid.value) draftError.value = 'Baris draf yang ditemukan tidak cocok dengan harga yang dicatat.'
    }
    if (!propAttempt.value && !apprAttempt.value && !revPropAttempt.value && !revApprAttempt.value) await maybeStartDraftLookup()
  } catch {
    if (gen === refreshGen && !readDenied.value) pageError.value = failureMessage
  } finally {
    if (gen === refreshGen) refreshing.value = false
  }
}

const paymentStages = {
  proposal: { attempt: propAttempt, stale: staleProp },
  approval: { attempt: apprAttempt, stale: staleAppr },
  'reversal-proposal': { attempt: revPropAttempt, stale: staleRevProp },
  'reversal-approval': { attempt: revApprAttempt, stale: staleRevAppr }
}
type PaymentStage = keyof typeof paymentStages
type CompletionSource = 'post' | 'history'

function stageOfControl(control: EventTarget | null): string | undefined {
  return control instanceof Element ? control.closest<HTMLElement>('[data-payment-stage]')?.dataset.paymentStage : undefined
}

function rememberStageFocus(event: FocusEvent): void {
  // Disabling a focused submit button may move focus to the body before its response arrives.
  lastFocusedStage = stageOfControl(event.target)
}

function focusStage(stage: PaymentStage, gen: number, target: 'alert' | 'heading' = 'alert'): void {
  const focused = document.activeElement
  const owner = focused === document.body ? lastFocusedStage : stageOfControl(focused)
  if (owner && owner !== stage) return
  if (focused && focused !== document.body && !paymentsSection.value?.contains(focused)) return
  if (!owner && Object.entries(paymentStages).some(([name, other]) => name !== stage && other.attempt.value)) return
  // Only restore focus if this interaction still owns it after Vue replaces its panel.
  void nextTick(() => {
    if (paymentStages[stage].stale(gen) || !session?.token.value) return
    if (document.activeElement !== focused && document.activeElement !== document.body) return
    if (document.activeElement === document.body && lastFocusedStage && lastFocusedStage !== stage) return
    if (target === 'heading') paymentsHeading.value?.focus()
    else paymentsSection.value?.querySelector<HTMLElement>(`[data-payment-stage="${stage}"] h3[tabindex="-1"]`)?.focus()
  })
}

function setPropUnknown(gen: number) {
  propUnknown.value = true
  focusStage('proposal', gen)
}

function setApprUnknown(gen: number) {
  apprUnknown.value = true
  focusStage('approval', gen)
}

function setRevPropUnknown(gen: number) {
  revPropUnknown.value = true
  focusStage('reversal-proposal', gen)
}

function setRevApprUnknown(gen: number) {
  revApprUnknown.value = true
  focusStage('reversal-approval', gen)
}

// Kunci tercemar (409 mismatch): kunci sudah terikat pada permintaan lain.
// Satu status yang koheren: panel tak diketahui disembunyikan
// (kirim-ulang tidak lagi ditawarkan), snapshot dipertahankan untuk
// ringkasan beku, kirim terkunci, dan hanya "Mulai … baru" yang boleh
// menebus — tanpa kirim-ulang buta.
function setPropKeyMismatch(gen: number) {
  propConflict.value = 'key'
  propUnknown.value = false
  propPostDenied.value = null
  focusStage('proposal', gen)
}

function setApprKeyMismatch(gen: number) {
  apprConflict.value = 'key'
  apprUnknown.value = false
  apprPostDenied.value = null
  focusStage('approval', gen)
}

function setRevPropKeyMismatch(gen: number) {
  revPropConflict.value = 'key'
  revPropUnknown.value = false
  revPropPostDenied.value = null
  focusStage('reversal-proposal', gen)
}

function setRevApprKeyMismatch(gen: number) {
  revApprConflict.value = 'key'
  revApprUnknown.value = false
  revApprPostDenied.value = null
  focusStage('reversal-approval', gen)
}

async function completeStage(stage: PaymentStage, gen: number, source: CompletionSource): Promise<void> {
  focusStage(stage, gen, 'heading')
  if (source === 'history') return
  clearApprovalNotices()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.', true)
}

function confirmProp(gen: number, source: CompletionSource): Promise<void> {
  clearPropStage()
  amountInput.value = ''
  referenceInput.value = ''
  return completeStage('proposal', gen, source)
}

function finishPaymentApproval(gen: number, outcome: 'own' | 'independent', source: CompletionSource): Promise<void> {
  const attempt = apprAttempt.value
  clearApprStage()
  if (outcome === 'independent' && attempt) {
    const { proposalId, amount, currency } = attempt
    paymentPostedElsewhere.value = { proposalId, amount, currency }
  }
  return completeStage('approval', gen, source)
}

function confirmRevProp(gen: number, source: CompletionSource): Promise<void> {
  clearRevPropStage()
  reversalReasonInput.value = ''
  return completeStage('reversal-proposal', gen, source)
}

function finishReversalApproval(gen: number, outcome: 'own' | 'independent', source: CompletionSource): Promise<void> {
  const attempt = revApprAttempt.value
  clearRevApprStage()
  if (outcome === 'independent' && attempt) paymentReversedElsewhere.value = attempt.paymentId
  return completeStage('reversal-approval', gen, source)
}

function attemptContext(): { customerId: string, customerLabel: string, itemId: string, itemLabel: string, fromIso: string, toIso: string } | null {
  const actor = session?.subjectId.value ?? ''
  const cid = contextCustomerId.value
  const iid = contextItemId.value
  const fromIso = contextFromIso.value
  const toIso = contextToIso.value
  if (!actor || !cid || !iid || !fromIso || !toIso) return null
  return { customerId: cid, customerLabel: customerLabel(cid), itemId: iid, itemLabel: catalogLabel(iid), fromIso, toIso }
}

function clearApprovalNotices(): void {
  paymentPostedElsewhere.value = null
  paymentReversedElsewhere.value = null
}

function openPropConfirmation() {
  if (!writeReadsReady.value || propAttempt.value) return
  const ctx = attemptContext()
  if (!ctx || !fact.value || !history.value) return
  const amount = amountInput.value.trim()
  if (!isPositiveMoney(amount)) {
    propFormError.value = 'Nominal wajib angka kanonis positif (tanpa nol di depan, tanpa pemisah).'
    return
  }
  const reference = referenceInput.value === '' ? null : referenceInput.value
  if (!isValidReference(reference)) {
    propFormError.value = 'Referensi wajib 1–128 karakter dan mengandung sedikitnya satu karakter bukan-spasi.'
    return
  }
  propAttempt.value = {
    key: `pay-prop-${crypto.randomUUID()}`,
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    factId: fact.value.id,
    amount,
    currency: fact.value.currency,
    method: methodInput.value,
    reference,
    customerLabel: ctx.customerLabel,
    itemLabel: ctx.itemLabel,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  propFormError.value = null
  propPostDenied.value = null
  propReplayError.value = null
  propSettleError.value = null
  clearApprovalNotices()
  focusStage('proposal', propGen)
}

function openApprConfirmation(p: PaymentProposalRow) {
  if (apprAttempt.value || !writeReadsReady.value) return
  if (sameId(p.proposed_by, subjectId.value)) return
  const ctx = attemptContext()
  if (!ctx) return
  apprAttempt.value = {
    key: `pay-appr-${crypto.randomUUID()}`,
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    factId: p.rental_agreed_price_id,
    proposalId: p.id,
    proposedBy: p.proposed_by,
    proposedAt: p.proposed_at,
    amount: p.amount_minor,
    currency: p.currency,
    method: p.method,
    reference: p.reference,
    customerLabel: ctx.customerLabel,
    itemLabel: ctx.itemLabel,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  apprPostDenied.value = null
  apprReplayError.value = null
  apprSettleError.value = null
  clearApprovalNotices()
  focusStage('approval', apprGen)
}

function openRevPropConfirmation(pay: PaymentRow) {
  if (revPropAttempt.value || !writeReadsReady.value || !fact.value) return
  if (pay.reversed) return
  const ctx = attemptContext()
  if (!ctx) return
  revPropAttempt.value = {
    key: `pay-revprop-${crypto.randomUUID()}`,
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    factId: fact.value.id,
    paymentId: pay.id,
    paymentAmount: pay.amount_minor,
    currency: fact.value.currency,
    reason: '',
    customerLabel: ctx.customerLabel,
    itemLabel: ctx.itemLabel,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  reversalReasonInput.value = ''
  revPropFormError.value = null
  revPropPostDenied.value = null
  revPropReplayError.value = null
  revPropSettleError.value = null
  clearApprovalNotices()
  focusStage('reversal-proposal', revPropGen)
}

function openRevApprConfirmation(rp: ReversalProposalRow) {
  if (revApprAttempt.value || !writeReadsReady.value || !fact.value) return
  if (sameId(rp.proposed_by, subjectId.value)) return
  if (isPaymentReversed(rp.payment_id)) return
  const ctx = attemptContext()
  if (!ctx) return
  revApprAttempt.value = {
    key: `pay-revappr-${crypto.randomUUID()}`,
    workspaceId: wsId.value,
    draftId: draftId.value,
    actorId: session?.subjectId.value ?? '',
    factId: fact.value.id,
    paymentId: rp.payment_id,
    proposalId: rp.id,
    proposedBy: rp.proposed_by,
    proposedAt: rp.proposed_at,
    reason: rp.reason,
    customerLabel: ctx.customerLabel,
    itemLabel: ctx.itemLabel,
    fromIso: ctx.fromIso,
    toIso: ctx.toIso
  }
  revApprPostDenied.value = null
  revApprReplayError.value = null
  revApprSettleError.value = null
  clearApprovalNotices()
  focusStage('reversal-approval', revApprGen)
}

// Batalkan konfirmasi yang BELUM dikirim: buang snapshot yang belum
// dipakai; konteks tidak berubah, jadi tidak perlu muat ulang.
function cancelPropConfirmation() {
  if (propBusy.value || propUnknown.value || propConflict.value !== null) return
  propAttempt.value = null
  propPostDenied.value = null
}

function cancelApprConfirmation() {
  if (apprBusy.value || apprUnknown.value || apprConflict.value !== null) return
  apprAttempt.value = null
  apprPostDenied.value = null
}

function cancelRevPropConfirmation() {
  if (revPropBusy.value || revPropUnknown.value || revPropConflict.value !== null) return
  revPropAttempt.value = null
  revPropPostDenied.value = null
}

function cancelRevApprConfirmation() {
  if (revApprBusy.value || revApprUnknown.value || revApprConflict.value !== null) return
  revApprAttempt.value = null
  revApprPostDenied.value = null
}

// Menebus snapshot secara eksplisit: pemulihan ditinggalkan, lalu baca
// ulang keadaan saat ini sebelum kunci baru dibuat. Tulis sebelumnya
// mungkin sudah tersimpan dan TIDAK dibatalkan oleh tindakan ini.
// Hanya dari konflik kunci definitif: state unknown TERKUNCI —
// retirement tidak boleh membuang snapshot yang hasilnya belum pasti.
// Terkunci selama POST berjalan: kelanjutan POST lama tidak boleh
// menghapus tahap baru atau mengumumkan simpan untuknya.
async function retirePropAttempt() {
  if (propBusy.value || propUnknown.value) return
  clearPropStage()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function retireApprAttempt() {
  if (apprBusy.value || apprUnknown.value) return
  clearApprStage()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function retireRevPropAttempt() {
  if (revPropBusy.value || revPropUnknown.value) return
  clearRevPropStage()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function retireRevApprAttempt() {
  if (revApprBusy.value || revApprUnknown.value) return
  clearRevApprStage()
  await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
}

async function submitProp(resend = false) {
  const s = session
  if (!s || propInFlight) return
  if ((propUnknown.value || propConflict.value !== null) && !resend) return
  if (resend && (!propUnknown.value || propConflict.value !== null)) return
  const a = propAttempt.value
  if (!a || !isCurrentAttempt(a)) return
  if (!resend && !propTargetLive.value) return
  const gen = propGen
  const myPost = ++propLoadGen
  propReplayError.value = null
  propSettleError.value = null
  if (!resend) propPostDenied.value = null
  propInFlight = true
  propBusy.value = true
  try {
    const { status, data } = await apiFetch<PaymentProposalRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/payment-proposals`, {
      method: 'POST',
      body: { amount_minor: a.amount, currency: a.currency, method: a.method, reference: a.reference },
      idempotencyKey: a.key
    })
    if (staleProp(gen) || !isCurrentAttempt(a)) return
    // 201, atau replay 200 dengan usulan yang cocok, mengonfirmasi.
    if ((status === 201 || status === 200) && isValidProposalForAttempt(data, a)) {
      await confirmProp(gen, 'post')
      return
    }
    setPropUnknown(gen)
  } catch (err) {
    if (staleProp(gen) || !isCurrentAttempt(a)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setPropKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 409 || err.status === 422)) {
      if (!resend) {
        // Kiriman pertama: penolakan pra-sisip — tulis tidak terjadi.
        // Snapshot dipertahankan + panel tetap terbuka; kirim-ulang
        // kunci-sama tetap tersedia sesudah penyebabnya diperbaiki.
        propPostDenied.value = err.message
        focusStage('proposal', gen)
        return
      }
      // Penolakan kirim-ulang yang tegas: TIDAK membuktikan tulis awal
      // gagal — pemeriksaan izin server mendahului replay — jadi
      // snapshot dan ketidakpastian dipertahankan.
      propReplayError.value = err.message
      propUnknown.value = true
      focusStage('proposal', gen)
      return
    }
    // 500/502/jaringan: hasil tetap tak diketahui, snapshot dipertahankan.
    setPropUnknown(gen)
  } finally {
    if (myPost === propLoadGen) {
      propInFlight = false
      propBusy.value = false
    }
  }
}

async function resendProp() {
  await submitProp(true)
}

async function submitAppr(resend = false) {
  const s = session
  if (!s || apprInFlight) return
  if ((apprUnknown.value || apprConflict.value !== null) && !resend) return
  if (resend && (!apprUnknown.value || apprConflict.value !== null)) return
  const a = apprAttempt.value
  if (!a || !isCurrentAttempt(a)) return
  if (!resend && !apprTargetLive.value) return
  const gen = apprGen
  const myPost = ++apprLoadGen
  apprReplayError.value = null
  apprSettleError.value = null
  if (!resend) apprPostDenied.value = null
  apprInFlight = true
  apprBusy.value = true
  try {
    const { status, data } = await apiFetch<PaymentRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/payment-proposals/${a.proposalId}/approvals`, {
      method: 'POST',
      body: {},
      idempotencyKey: a.key
    })
    if (staleAppr(gen) || !isCurrentAttempt(a)) return
    // 201, atau replay 200 dengan pembayaran yang cocok, mengonfirmasi
    // — termasuk pembayaran yang belakangan sudah dibalik.
    if ((status === 201 || status === 200) && isValidPaymentForApproveAttempt(data, a)) {
      await finishPaymentApproval(gen, 'own', 'post')
      return
    }
    setApprUnknown(gen)
  } catch (err) {
    if (staleAppr(gen) || !isCurrentAttempt(a)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setApprKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 409 || err.status === 422)) {
      if (resend) {
        apprReplayError.value = err.message
        apprUnknown.value = true
      } else {
        apprPostDenied.value = err.message
      }
      focusStage('approval', gen)
      if (err.status === 409 && err.message === ALREADY_APPROVED) await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
      return
    }
    setApprUnknown(gen)
  } finally {
    if (myPost === apprLoadGen) {
      apprInFlight = false
      apprBusy.value = false
    }
  }
}

async function resendAppr() {
  await submitAppr(true)
}

async function submitRevProp(resend = false) {
  const s = session
  if (!s || revPropInFlight) return
  if ((revPropUnknown.value || revPropConflict.value !== null) && !resend) return
  if (resend && (!revPropUnknown.value || revPropConflict.value !== null)) return
  const attempt = revPropAttempt.value
  if (!attempt || !isCurrentAttempt(attempt)) return
  if (!resend && !revPropTargetLive.value) return
  const reason = attempt.reason || (!resend ? normalizeReason(reversalReasonInput.value) : null)
  if (!reason) {
    revPropFormError.value = 'Alasan pembalikan wajib diisi 1–500 karakter.'
    return
  }
  const a = attempt.reason ? attempt : { ...attempt, reason }
  if (!attempt.reason) revPropAttempt.value = a
  const gen = revPropGen
  const myPost = ++revPropLoadGen
  revPropReplayError.value = null
  revPropSettleError.value = null
  if (!resend) revPropPostDenied.value = null
  revPropInFlight = true
  revPropBusy.value = true
  try {
    const { status, data } = await apiFetch<ReversalProposalRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/payments/${a.paymentId}/reversal-proposals`, {
      method: 'POST',
      body: { reason: a.reason },
      idempotencyKey: a.key
    })
    if (staleRevProp(gen) || !isCurrentAttempt(a)) return
    if ((status === 201 || status === 200) && isValidReversalProposalForAttempt(data, a)) {
      await confirmRevProp(gen, 'post')
      return
    }
    setRevPropUnknown(gen)
  } catch (err) {
    if (staleRevProp(gen) || !isCurrentAttempt(a)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setRevPropKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 409 || err.status === 422)) {
      if (resend) {
        revPropReplayError.value = err.message
        revPropUnknown.value = true
      } else {
        revPropPostDenied.value = err.message
      }
      focusStage('reversal-proposal', gen)
      if (err.status === 409 && err.message === ALREADY_REVERSED) await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
      return
    }
    setRevPropUnknown(gen)
  } finally {
    if (myPost === revPropLoadGen) {
      revPropInFlight = false
      revPropBusy.value = false
    }
  }
}

async function resendRevProp() {
  await submitRevProp(true)
}

async function submitRevAppr(resend = false) {
  const s = session
  if (!s || revApprInFlight) return
  if ((revApprUnknown.value || revApprConflict.value !== null) && !resend) return
  if (resend && (!revApprUnknown.value || revApprConflict.value !== null)) return
  const a = revApprAttempt.value
  if (!a || !isCurrentAttempt(a)) return
  if (!resend && !revApprTargetLive.value) return
  const gen = revApprGen
  const myPost = ++revApprLoadGen
  revApprReplayError.value = null
  revApprSettleError.value = null
  if (!resend) revApprPostDenied.value = null
  revApprInFlight = true
  revApprBusy.value = true
  try {
    const { status, data } = await apiFetch<ReversalRow>(s, `/api/workspaces/${a.workspaceId}/rental/contracts/${a.draftId}/payments/${a.paymentId}/reversal-proposals/${a.proposalId}/approvals`, {
      method: 'POST',
      body: {},
      idempotencyKey: a.key
    })
    if (staleRevAppr(gen) || !isCurrentAttempt(a)) return
    if ((status === 201 || status === 200) && isValidReversalForApproveAttempt(data, a)) {
      await finishReversalApproval(gen, 'own', 'post')
      return
    }
    setRevApprUnknown(gen)
  } catch (err) {
    if (staleRevAppr(gen) || !isCurrentAttempt(a)) return
    if (err instanceof ApiError && err.status === 401) {
      redirectToLogin(s, route.fullPath)
      return
    }
    if (err instanceof ApiError && err.status === 409 && err.message === KEY_MISMATCH) {
      setRevApprKeyMismatch(gen)
      return
    }
    if (err instanceof ApiError && (err.status === 400 || err.status === 403 || err.status === 404 || err.status === 409 || err.status === 422)) {
      if (resend) {
        revApprReplayError.value = err.message
        revApprUnknown.value = true
      } else {
        revApprPostDenied.value = err.message
      }
      focusStage('reversal-approval', gen)
      if (err.status === 409 && err.message === ALREADY_REVERSED) await refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')
      return
    }
    setRevApprUnknown(gen)
  } finally {
    if (myPost === revApprLoadGen) {
      revApprInFlight = false
      revApprBusy.value = false
    }
  }
}

async function resendRevAppr() {
  await submitRevAppr(true)
}
</script>

<template>
  <main id="konten" class="page">
    <h1>Pembayaran</h1>
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
      <div v-else-if="readDenied" class="alert" role="alert">
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
          <NuxtLink :to="`/workspaces/${wsId}/prices`">Tarif</NuxtLink>
          <NuxtLink :to="`/workspaces/${wsId}/rentals`">Draf sewa</NuxtLink>
        </nav>
        <p>
          <NuxtLink class="btn btn-secondary" :to="`/workspaces/${wsId}/rentals`">Kembali ke Draf sewa</NuxtLink>
        </p>
        <p class="small">
          Pembayaran di sini hanya mencatat pembayaran yang disetujui untuk draf ini. Ini bukan pengembalian uang,
          bukan pendapatan, bukan invoice, dan tidak mengubah status draf. Pembalikan membatalkan catatan
          pembayaran (bukan pengembalian uang).
        </p>

        <section class="card" aria-labelledby="draft-heading">
          <h2 id="draft-heading">Draf</h2>
          <div v-if="draftRow || fact">
            <p>
              <strong>{{ customerLabel(contextCustomerId ?? '') }} — {{ catalogLabel(contextItemId ?? '') }}</strong><br>
              Periode yang dicatat: mulai {{ contextFromIso }}; berakhir {{ contextToIso }} (UTC)<br>
              Status: Draf
            </p>
          </div>
          <p v-else-if="draftLoading" role="status">Mencari draf dalam daftar…</p>
          <div v-else-if="draftError" class="alert" role="alert">
            <p>{{ draftError }}</p>
            <p v-if="!draftExhausted">
              <button type="button" class="btn btn-secondary" :disabled="draftLoading" @click="loadDraftPage">Coba lagi</button>
            </p>
          </div>
          <p v-else class="hint">Draf dicari dalam daftar sesudah harga dibaca.</p>
          <p v-if="draftStarted && !draftRow && !draftError">Mencari draf dalam daftar: {{ draftRows.length }} baris dimuat sejauh ini.</p>
          <p v-if="draftStarted && !draftRow && !draftExhausted && !draftLoading && !draftError">
            <button type="button" class="btn btn-secondary" @click="loadDraftPage">Muat lebih banyak draf</button>
          </p>
          <p v-if="customerLoadError" class="alert" role="alert">
            {{ customerLoadError }}
            <button type="button" class="btn btn-secondary" @click="loadCustomerPage">Coba lagi</button>
          </p>
          <p v-if="showCustomerMore">
            <button type="button" class="btn btn-secondary" :disabled="customerLoading" @click="loadCustomerPage">
              {{ customerLoading ? 'Memuat…' : 'Muat lebih banyak pelanggan' }}
            </button>
          </p>
          <p v-if="catalogError" class="alert" role="alert">
            {{ catalogError }}
            <button type="button" class="btn btn-secondary" @click="loadItemPage">Coba lagi</button>
          </p>
          <p v-if="showCatalogMore">
            <button type="button" class="btn btn-secondary" :disabled="catalogLoading" @click="loadItemPage">
              {{ catalogLoading ? 'Memuat…' : 'Muat lebih banyak barang' }}
            </button>
          </p>
        </section>

        <section class="card" aria-labelledby="fact-heading">
          <h2 id="fact-heading">Harga yang disepakati</h2>
          <p v-if="factLoading" role="status">Memuat harga yang dicatat…</p>
          <div v-else-if="fact">
            <p>
              Harga yang dicatat: {{ fact.total_minor }} unit tarif · {{ fact.currency }} ({{ fact.units }} × 24 jam @ {{ fact.amount_minor }} unit tarif · {{ fact.currency }}), dicatat {{ fact.accepted_at }} (UTC).
            </p>
            <p class="hint">
              Fakta ini hanya mencatat harga yang dilihat untuk draf ini. Ini bukan persetujuan pelanggan, bukan
              perjanjian, bukan pembayaran, bukan invoice, dan tidak mengubah status draf.
            </p>
          </div>
          <p v-else-if="factMissing">Belum ada harga yang dicatat untuk draf ini.</p>
          <div v-else-if="factError" class="alert" role="alert">
            <p>{{ factError }}</p>
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Harga gagal dimuat ulang. Coba muat ulang data.')">Coba lagi</button></p>
          </div>
        </section>

        <section ref="paymentsSection" class="card" aria-labelledby="payments-heading" @focusin="rememberStageFocus">
          <h2 id="payments-heading" ref="paymentsHeading" tabindex="-1">Pembayaran</h2>
          <div role="status" aria-atomic="true">
            <p v-if="paymentPostedElsewhere">
              Usulan {{ paymentPostedElsewhere.amount }} unit tarif · {{ paymentPostedElsewhere.currency }} (ID: {{ paymentPostedElsewhere.proposalId }}).
              <span>Pembayaran untuk usulan ini sudah dicatat melalui permintaan lain.</span>
            </p>
            <p v-if="paymentReversedElsewhere">
              Pembayaran (ID: {{ paymentReversedElsewhere }}).
              <span>Catatan pembayaran ini sudah dibatalkan melalui permintaan lain.</span>
            </p>
          </div>
          <p>
            <button type="button" class="btn btn-secondary" :disabled="refreshing || loading" @click="refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')">Muat ulang data</button>
          </p>
          <p v-if="historyLoading" role="status">Memuat riwayat pembayaran…</p>
          <div v-else-if="history && historyLinksVerified">
            <p>Jumlah harga disepakati (plafon acuan internal): {{ history.total_minor }} unit tarif · {{ history.currency }}</p>
            <p>Pembayaran posted aktif: {{ history.paid_minor }} unit tarif · {{ history.currency }}</p>
            <p>Sisa plafon acuan: {{ history.outstanding_minor }} unit tarif · {{ history.currency }}</p>
            <p class="hint">
              Total dihitung server dari catatan yang sudah dicatat untuk draf ini. Usulan yang masih menunggu
              persetujuan tidak menambah total posted. Pembalikan membatalkan catatan pembayaran (bukan
              pengembalian uang).
            </p>

            <div v-if="history.pending_payment_proposals.length">
              <h3>Usulan pembayaran menunggu persetujuan</h3>
              <ul>
                <li v-for="p in history.pending_payment_proposals" :key="p.id">
                  <p>Usulan pembayaran: {{ p.amount_minor }} unit tarif · {{ p.currency }} — {{ methodLabel(p.method) }}<span v-if="p.reference"> · {{ p.reference }}</span><br>Diusulkan: {{ p.proposed_at }} (UTC)</p>
                  <p v-if="sameId(p.proposed_by, subjectId)">Persetujuan membutuhkan aktor kedua.</p>
                  <p v-if="!apprAttempt" data-payment-stage="approval">
                    <button type="button" class="btn" :disabled="!writeReadsReady || apprBusy || sameId(p.proposed_by, subjectId)" @click="openApprConfirmation(p)">Setujui</button>
                  </p>
                </li>
              </ul>
            </div>

            <div v-if="history.payments.length">
              <h3>Pembayaran yang sudah dicatat</h3>
              <ul>
                <li v-for="pay in history.payments" :key="pay.id">
                  <p>{{ pay.amount_minor }} unit tarif · {{ pay.currency }} — {{ methodLabel(pay.method) }}<span v-if="pay.reference"> · {{ pay.reference }}</span><br>Disetujui: {{ pay.approved_at }} (UTC)</p>
                  <p v-if="pay.reversed"><strong>Dibatalkan</strong></p>
                  <p v-if="!pay.reversed && !revPropAttempt" data-payment-stage="reversal-proposal">
                    <button type="button" class="btn" :disabled="!writeReadsReady || revPropBusy" @click="openRevPropConfirmation(pay)">Usulkan pembalikan</button>
                  </p>
                </li>
              </ul>
            </div>

            <div v-if="history.pending_reversal_proposals.length">
              <h3>Usulan pembalikan menunggu persetujuan</h3>
              <ul>
                <li v-for="rp in history.pending_reversal_proposals" :key="rp.id">
                  <p>Usulan pembalikan: {{ rp.reason }}<br>Diusulkan: {{ rp.proposed_at }} (UTC)</p>
                  <p v-if="sameId(rp.proposed_by, subjectId)">Persetujuan membutuhkan aktor kedua.</p>
                  <p v-else-if="isPaymentReversed(rp.payment_id)">Persetujuan tidak lagi tersedia untuk pembayaran ini.</p>
                  <p v-if="!revApprAttempt" data-payment-stage="reversal-approval">
                    <button type="button" class="btn" :disabled="!writeReadsReady || revApprBusy || sameId(rp.proposed_by, subjectId) || isPaymentReversed(rp.payment_id)" @click="openRevApprConfirmation(rp)">Setujui pembalikan</button>
                  </p>
                </li>
              </ul>
            </div>

            <div v-if="history.reversals.length">
              <h3>Pembalikan yang sudah dicatat</h3>
              <ul>
                <li v-for="rev in history.reversals" :key="rev.id">
                  <p>Pembatalan catatan: {{ rev.reason }}<br>Disetujui: {{ rev.approved_at }} (UTC)</p>
                </li>
              </ul>
            </div>

            <p v-if="!history.pending_payment_proposals.length && !history.payments.length && !history.pending_reversal_proposals.length && !history.reversals.length" class="hint">Belum ada pembayaran untuk draf ini.</p>
          </div>
          <p v-else-if="factMissing && !history && !historyError">Riwayat pembayaran belum tersedia sebelum harga dicatat untuk draf ini.</p>
          <div v-else-if="historyError || history" class="alert" role="alert">
            <p>{{ historyError || 'Status pembalikan belum dapat dipastikan dari riwayat ini. Muat ulang data sebelum melanjutkan.' }}</p>
            <p><button type="button" class="btn btn-secondary" @click="refreshReads('Data gagal dimuat ulang. Coba muat ulang data.')">Coba lagi</button></p>
          </div>

          <div v-if="(historyLinksVerified || historyLoading) && fact" data-payment-stage="proposal">
            <p>
              <label for="pay-amount">Nominal (unit tersimpan)</label>
              <input id="pay-amount" v-model="amountInput" inputmode="numeric" :disabled="propBusy || !!propAttempt">
            </p>
            <p>
              <label for="pay-method">Metode</label>
              <select id="pay-method" v-model="methodInput" :disabled="propBusy || !!propAttempt">
                <option value="cash">Tunai</option>
                <option value="transfer">Transfer</option>
              </select>
            </p>
            <p>
              <label for="pay-reference">Referensi (opsional)</label>
              <input id="pay-reference" v-model="referenceInput" :disabled="propBusy || !!propAttempt">
            </p>
            <p v-if="propFormError" class="field-error">{{ propFormError }}</p>
            <p v-if="writeReadsReady && !propAttempt">
              <button type="button" class="btn" :disabled="propBusy" @click="openPropConfirmation">Usulkan pembayaran</button>
            </p>
          </div>
            <div v-if="propAttempt && !propUnknown && propConflict === null" class="alert" role="alert" data-payment-stage="proposal">
              <h3 tabindex="-1">Konfirmasi usulan pembayaran</h3>
              <p>
                {{ propAttempt.customerLabel }} — {{ propAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ propAttempt.fromIso }}; berakhir {{ propAttempt.toIso }} (UTC).
              </p>
              <p>Nominal: {{ propAttempt.amount }} unit tarif · {{ propAttempt.currency }} — {{ methodLabel(propAttempt.method) }}<span v-if="propAttempt.reference"> · {{ propAttempt.reference }}</span></p>
              <p>
                Usulan pembayaran mencatat pembayaran untuk draf ini. Pembayaran hanya tercatat sesudah
                disetujui aktor kedua.
              </p>
              <p v-if="propPostDenied" class="field-error">{{ propPostDenied }}</p>
              <p v-if="isFinanceDenied(propPostDenied)">{{ FINANCE_GUIDANCE }}</p>
              <p>
                <button type="button" class="btn" :disabled="propBusy || !propTargetLive" @click="submitProp(false)">
                  {{ propBusy ? 'Mengusulkan…' : 'Usulkan pembayaran' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="propBusy" @click="cancelPropConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="propUnknown" class="alert" role="alert" data-payment-stage="proposal">
              <h3 tabindex="-1">Hasil usulan pembayaran tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="propAttempt">
                Permintaan yang belum pasti: {{ propAttempt.customerLabel }} — {{ propAttempt.itemLabel }}.
                Nominal: {{ propAttempt.amount }} unit tarif · {{ propAttempt.currency }} — {{ methodLabel(propAttempt.method) }}<span v-if="propAttempt.reference"> · {{ propAttempt.reference }}</span>.
                Periode yang dicatat: mulai {{ propAttempt.fromIso }}; berakhir {{ propAttempt.toIso }} (UTC).
              </p>
              <p v-if="propReplayError" class="field-error">Kirim ulang ditolak: {{ propReplayError }}</p>
              <p v-if="propSettleError" class="field-error">{{ propSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="propBusy" @click="resendProp">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="propConflict === 'key'" class="alert" role="alert" data-payment-stage="proposal">
              <h3 tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="propAttempt">
                Permintaan yang belum pasti: {{ propAttempt.customerLabel }} — {{ propAttempt.itemLabel }}.
                Nominal: {{ propAttempt.amount }} unit tarif · {{ propAttempt.currency }} — {{ methodLabel(propAttempt.method) }}<span v-if="propAttempt.reference"> · {{ propAttempt.reference }}</span>.
                Periode yang dicatat: mulai {{ propAttempt.fromIso }}; berakhir {{ propAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="propBusy" @click="retirePropAttempt">Mulai usulan pembayaran baru</button>
                Mulai usulan pembayaran baru membatalkan pemulihan ini. Usulan sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>

            <div v-if="apprAttempt && !apprUnknown && apprConflict === null" class="alert" role="alert" data-payment-stage="approval">
              <h3 tabindex="-1">Konfirmasi persetujuan pembayaran</h3>
              <p>
                {{ apprAttempt.customerLabel }} — {{ apprAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ apprAttempt.fromIso }}; berakhir {{ apprAttempt.toIso }} (UTC).
              </p>
              <p>Nominal: {{ apprAttempt.amount }} unit tarif · {{ apprAttempt.currency }} — {{ methodLabel(apprAttempt.method) }}<span v-if="apprAttempt.reference"> · {{ apprAttempt.reference }}</span></p>
              <p>
                Persetujuan mencatat pembayaran ini sebagai pembayaran posted aktif. Pastikan nominal,
                metode, dan referensi sudah benar sebelum menyetujui.
              </p>
              <p v-if="apprPostDenied" class="field-error">{{ apprPostDenied }}</p>
              <p v-if="isFinanceDenied(apprPostDenied)">{{ FINANCE_GUIDANCE }}</p>
              <p>
                <button type="button" class="btn" :disabled="apprBusy || !apprTargetLive" @click="submitAppr(false)">
                  {{ apprBusy ? 'Menyetujui…' : 'Setujui' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="apprBusy" @click="cancelApprConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="apprUnknown" class="alert" role="alert" data-payment-stage="approval">
              <h3 tabindex="-1">Hasil persetujuan pembayaran tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="apprAttempt">
                Permintaan yang belum pasti: {{ apprAttempt.customerLabel }} — {{ apprAttempt.itemLabel }}.
                Nominal: {{ apprAttempt.amount }} unit tarif · {{ apprAttempt.currency }} — {{ methodLabel(apprAttempt.method) }}<span v-if="apprAttempt.reference"> · {{ apprAttempt.reference }}</span>.
                Periode yang dicatat: mulai {{ apprAttempt.fromIso }}; berakhir {{ apprAttempt.toIso }} (UTC).
              </p>
              <p v-if="apprReplayError" class="field-error">Kirim ulang ditolak: {{ apprReplayError }}</p>
              <p v-if="apprSettleError" class="field-error">{{ apprSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="apprBusy" @click="resendAppr">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="apprConflict === 'key'" class="alert" role="alert" data-payment-stage="approval">
              <h3 tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="apprAttempt">
                Permintaan yang belum pasti: {{ apprAttempt.customerLabel }} — {{ apprAttempt.itemLabel }}.
                Nominal: {{ apprAttempt.amount }} unit tarif · {{ apprAttempt.currency }} — {{ methodLabel(apprAttempt.method) }}<span v-if="apprAttempt.reference"> · {{ apprAttempt.reference }}</span>.
                Periode yang dicatat: mulai {{ apprAttempt.fromIso }}; berakhir {{ apprAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="apprBusy" @click="retireApprAttempt">Mulai persetujuan pembayaran baru</button>
                Mulai persetujuan pembayaran baru membatalkan pemulihan ini. Persetujuan sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>

            <div v-if="revPropAttempt && !revPropUnknown && revPropConflict === null" class="alert" role="alert" data-payment-stage="reversal-proposal">
              <h3 tabindex="-1">Konfirmasi usulan pembalikan</h3>
              <p>
                {{ revPropAttempt.customerLabel }} — {{ revPropAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ revPropAttempt.fromIso }}; berakhir {{ revPropAttempt.toIso }} (UTC).
              </p>
              <p>Pembayaran: {{ revPropAttempt.paymentAmount }} unit tarif · {{ revPropAttempt.currency }}</p>
              <p>
                Pembalikan penuh membatalkan catatan pembayaran ini. Ini bukan pengembalian uang, dan
                pembalikan hanya tercatat sesudah disetujui aktor kedua.
              </p>
              <p>
                <label for="pay-reason">Alasan pembalikan</label>
                <input id="pay-reason" v-model="reversalReasonInput" :disabled="revPropBusy || !!revPropAttempt.reason">
              </p>
              <p v-if="revPropFormError" class="field-error">{{ revPropFormError }}</p>
              <p v-if="revPropPostDenied" class="field-error">{{ revPropPostDenied }}</p>
              <p v-if="isFinanceDenied(revPropPostDenied)">{{ FINANCE_GUIDANCE }}</p>
              <p>
                <button type="button" class="btn" :disabled="revPropBusy || !revPropTargetLive" @click="submitRevProp(false)">
                  {{ revPropBusy ? 'Mengusulkan…' : 'Usulkan pembalikan' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="revPropBusy" @click="cancelRevPropConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="revPropUnknown" class="alert" role="alert" data-payment-stage="reversal-proposal">
              <h3 tabindex="-1">Hasil usulan pembalikan tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="revPropAttempt">
                Permintaan yang belum pasti: {{ revPropAttempt.customerLabel }} — {{ revPropAttempt.itemLabel }}.
                Pembayaran: {{ revPropAttempt.paymentAmount }} unit tarif · {{ revPropAttempt.currency }}.
                Alasan: {{ revPropAttempt.reason }}.
                Periode yang dicatat: mulai {{ revPropAttempt.fromIso }}; berakhir {{ revPropAttempt.toIso }} (UTC).
              </p>
              <p v-if="revPropReplayError" class="field-error">Kirim ulang ditolak: {{ revPropReplayError }}</p>
              <p v-if="revPropSettleError" class="field-error">{{ revPropSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="revPropBusy" @click="resendRevProp">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="revPropConflict === 'key'" class="alert" role="alert" data-payment-stage="reversal-proposal">
              <h3 tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="revPropAttempt">
                Permintaan yang belum pasti: {{ revPropAttempt.customerLabel }} — {{ revPropAttempt.itemLabel }}.
                Pembayaran: {{ revPropAttempt.paymentAmount }} unit tarif · {{ revPropAttempt.currency }}.
                Alasan: {{ revPropAttempt.reason }}.
                Periode yang dicatat: mulai {{ revPropAttempt.fromIso }}; berakhir {{ revPropAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="revPropBusy" @click="retireRevPropAttempt">Mulai usulan pembalikan baru</button>
                Mulai usulan pembalikan baru membatalkan pemulihan ini. Usulan sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>

            <div v-if="revApprAttempt && !revApprUnknown && revApprConflict === null" class="alert" role="alert" data-payment-stage="reversal-approval">
              <h3 tabindex="-1">Konfirmasi persetujuan pembalikan</h3>
              <p>
                {{ revApprAttempt.customerLabel }} — {{ revApprAttempt.itemLabel }}. Periode yang dicatat:
                mulai {{ revApprAttempt.fromIso }}; berakhir {{ revApprAttempt.toIso }} (UTC).
              </p>
              <p>Alasan pembalikan: {{ revApprAttempt.reason }}</p>
              <p>
                Persetujuan membatalkan catatan pembayaran ini. Ini bukan pengembalian uang. Pastikan
                alasan pembalikan sudah benar sebelum menyetujui.
              </p>
              <p v-if="revApprPostDenied" class="field-error">{{ revApprPostDenied }}</p>
              <p v-if="isFinanceDenied(revApprPostDenied)">{{ FINANCE_GUIDANCE }}</p>
              <p>
                <button type="button" class="btn" :disabled="revApprBusy || !revApprTargetLive" @click="submitRevAppr(false)">
                  {{ revApprBusy ? 'Menyetujui…' : 'Setujui pembalikan' }}
                </button>
                <button type="button" class="btn btn-secondary" :disabled="revApprBusy" @click="cancelRevApprConfirmation">Batalkan</button>
              </p>
            </div>
            <div v-if="revApprUnknown" class="alert" role="alert" data-payment-stage="reversal-approval">
              <h3 tabindex="-1">Hasil persetujuan pembalikan tidak diketahui</h3>
              <p>
                Permintaan mungkin sudah tersimpan atau belum. Daftar yang tampil mungkin hanya
                sebagian; data yang belum terlihat tetap mungkin sudah tersimpan. Pemulihan ini
                hanya tersimpan di memori halaman ini; pindah halaman, muat ulang, atau keluar
                akan menghilangkannya.
              </p>
              <p v-if="revApprAttempt">
                Permintaan yang belum pasti: {{ revApprAttempt.customerLabel }} — {{ revApprAttempt.itemLabel }}.
                Alasan pembalikan: {{ revApprAttempt.reason }}.
                Periode yang dicatat: mulai {{ revApprAttempt.fromIso }}; berakhir {{ revApprAttempt.toIso }} (UTC).
              </p>
              <p v-if="revApprReplayError" class="field-error">Kirim ulang ditolak: {{ revApprReplayError }}</p>
              <p v-if="revApprSettleError" class="field-error">{{ revApprSettleError }}</p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="revApprBusy" @click="resendRevAppr">Kirim lagi (permintaan sama)</button>
              </p>
              <p>
                Selama hasil belum pasti, permintaan ini terkunci: tidak ada kunci baru yang dibuat.
                Muat ulang data atau kirim lagi dengan permintaan yang sama.
              </p>
            </div>
            <div v-if="revApprConflict === 'key'" class="alert" role="alert" data-payment-stage="reversal-approval">
              <h3 tabindex="-1">Kunci idempotency sudah dipakai</h3>
              <p>
                {{ KEY_MISMATCH }}. Kunci pengiriman ini sudah terikat pada permintaan lain, jadi permintaan ini
                tidak dapat dikirim ulang dengan kunci yang sama.
              </p>
              <p v-if="revApprAttempt">
                Permintaan yang belum pasti: {{ revApprAttempt.customerLabel }} — {{ revApprAttempt.itemLabel }}.
                Alasan pembalikan: {{ revApprAttempt.reason }}.
                Periode yang dicatat: mulai {{ revApprAttempt.fromIso }}; berakhir {{ revApprAttempt.toIso }} (UTC).
              </p>
              <p>
                <button type="button" class="btn btn-secondary" :disabled="revApprBusy" @click="retireRevApprAttempt">Mulai persetujuan pembalikan baru</button>
                Mulai persetujuan pembalikan baru membatalkan pemulihan ini. Persetujuan sebelumnya mungkin sudah
                tersimpan dan tidak dibatalkan oleh tindakan ini.
              </p>
            </div>
        </section>
      </template>
    </template>
  </main>
</template>
