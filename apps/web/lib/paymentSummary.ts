const AMOUNT_FIELDS = ['total_minor', 'paid_minor', 'outstanding_minor'] as const
const COUNT_FIELDS = [
  'payments_count', 'reversals_count', 'pending_payment_proposals_count', 'pending_reversal_proposals_count'
] as const
const CURRENCY_FIELDS = ['currency', ...AMOUNT_FIELDS, ...COUNT_FIELDS] as const
const SUMMARY_FIELDS = ['workspace_id', 'observed_at', 'currencies'] as const

export type PaymentSummaryCurrency = Readonly<
  { currency: string } &
  Record<(typeof AMOUNT_FIELDS)[number], string> &
  Record<(typeof COUNT_FIELDS)[number], number>
>

export interface PaymentSummary {
  readonly workspace_id: string
  readonly observed_at: string
  readonly currencies: readonly PaymentSummaryCurrency[]
}

const UUID_PATTERN = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i
const UTC_TIMESTAMP = /^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2})(?:\.\d{1,9})?Z$/
const CURRENCY_CODE = /^[A-Z]{3}$/
const MINOR_AMOUNT = /^(?:0|[1-9][0-9]*)$/

function hasExactFields<Field extends string>(value: unknown, fields: readonly Field[]): value is Record<Field, unknown> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const keys = Object.keys(value)
  return keys.length === fields.length && fields.every(field => keys.includes(field))
}

function isWorkspaceId(value: unknown): value is string {
  return typeof value === 'string' && UUID_PATTERN.test(value)
}

function isUtcTimestamp(value: unknown): value is string {
  if (typeof value !== 'string') return false
  const wholeSeconds = UTC_TIMESTAMP.exec(value)?.[1]
  if (!wholeSeconds) return false
  // A whole-second round trip detects calendar rollover without truncating the wire fraction.
  const expected = `${wholeSeconds}.000Z`
  const instant = new Date(expected)
  return Number.isFinite(instant.getTime()) && instant.toISOString() === expected
}

function isMinorAmount(value: unknown): value is string {
  return typeof value === 'string' && MINOR_AMOUNT.test(value)
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0
}

function isSummaryCurrency(value: unknown): value is PaymentSummaryCurrency {
  if (!hasExactFields(value, CURRENCY_FIELDS)) return false
  return typeof value.currency === 'string' && CURRENCY_CODE.test(value.currency) &&
    AMOUNT_FIELDS.every(field => isMinorAmount(value[field])) &&
    COUNT_FIELDS.every(field => isCount(value[field]))
}

export function decodePaymentSummary(value: unknown, expectedWorkspaceId: string): PaymentSummary | null {
  if (!isWorkspaceId(expectedWorkspaceId) || !hasExactFields(value, SUMMARY_FIELDS)) return null
  const { workspace_id, observed_at, currencies } = value
  if (!isWorkspaceId(workspace_id) || workspace_id.toLowerCase() !== expectedWorkspaceId.toLowerCase()) return null
  if (!isUtcTimestamp(observed_at) || !Array.isArray(currencies)) return null

  const wireRows: readonly unknown[] = currencies
  const seenCodes = new Set<string>()
  const rows: PaymentSummaryCurrency[] = []
  for (const row of wireRows) {
    if (!isSummaryCurrency(row) || seenCodes.has(row.currency)) return null
    seenCodes.add(row.currency)
    rows.push(Object.freeze({ ...row }))
  }
  return Object.freeze({ workspace_id, observed_at, currencies: Object.freeze(rows) })
}
