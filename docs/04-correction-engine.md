# 04 — Business Correction, Payment Integrity & Payroll Review

## Foundation: distinguish four concepts

1. `input_error`: salah ketik atau pengubahan master data.
2. `financial_discrepancy`: selisih matematis / sistem, belum otomatis kerugian aktual.
3. `actual_loss`: kerugian yang diverifikasi dari dokumen dan tindakan penyelesaian.
4. `employee_liability`: kewajiban hukum pekerja, **tidak bisa disimpulkan langsung dari audit input**.

Semua kasus dicatat di `incident_cases` Core, sehingga dapat dibaca kembali setelah Finance/Payroll aktif. Finance Advanced menambahkan discrepancy register, drilldown modal (Kronologi, Bukti, Perhitungan, Keputusan), export footnote sesuai hak akses. Payroll menambahkan *proposal* penyesuaian, bukan auto-deduction.

## Correction state machine

```text
reported -> collecting_evidence -> investigating -> impact_ready
impact_ready -> rejected / awaiting_approval
awaiting_approval -> approved / rejected / disputed
approved -> applying -> reconciled
applying -> failed_retryable -> applying / manual_review
disputed -> mediation_or_review -> approved / rejected
```

`resolved` hanya jika reconciled across affected sources (payment, ledger, inventory, tax). Pisahkan `case_status` dan `execution_status`. Setiap transisi membutuhkan reason, actor, timestamp dan revision.

## Example A: erroneous price 7 days ago

1. Manajer mencari harga berdasarkan `effective_date` dan `recorded_at`; lihat actor dan sumber bukti.
2. Impact Analyzer menelusuri *source reference graph*: affected rental contracts, invoices, payments, journals, taxes, discount, inventory cost — bukan semata nama barang.
3. Kelompokkan: draft/unpaid; invoiced; paid; refunded; settled; closed accounting period.
4. Generate tentative corrections (not yet committed); mark uncertain or missing historical prices.
5. Preview per transaction and aggregate (delta, potential refund, tax consequences); save correction plan hash.
6. Maker-checker approval: prohibit proposer=approver for high-risk monetary changes.
7. Execute idempotently. Paid invoices require credit/debit note and, when appropriate, refund/collect; don't overwrite payment evidence. Closed periods follow accountant-approved adjustment or formal reopening process.
8. Reconcile balances, allocations, invoice status, reporting, inventory, tax documents; produce traceable case report.

## Example B: customer already paid

- `payment.captured` immutable, tie to gateway reference; `refund.requested` not `refund.succeeded` until provider confirms final status.
- In-person cash refunds need recorded cash movement and supervisor approval.
- Never double-refund: unique `(provider, external_refund_id)` and workspace `idempotency_key`.
- If stock was delivered or rental commenced, reconcile physical effects separately (returns, damage, loss).
- If tax invoice issued, distinguish business receipt correction from required tax document correction and use verified rules.

## Maker-checker authorization

- proposer and approver identity must differ for risk-tagged operations; support numeric thresholds, per workspace policies, delegation with expiry, time-of-day/risk context.
- Escalation stages: employee reports -> supervisor reviews -> manager validates -> owner approves where necessary.
- Each approval records evidence reference and exact plan digest; any material change invalidates previous approval.
- `step_up_auth` for transferring ownership, changing bank account, mass correction, high-value refund.

## Payroll deduction / loss claim guardrail

Payroll installed? **No** => case stays in Core without deduction action. **Yes** => display `Ajukan Pemeriksaan Potongan`, **not** `Potong Gaji Sekarang`.

Before posting payroll adjustment validate:
- verified applicable law and effective date, basis in contract/company rules/CBA, wage deduction types, statutory limits, legal review;
- employee's opportunity to explain/dispute evidence; proportionality and any appropriate dispute procedure;
- deduction approval scope, payment period, all existing deductions, total capped according to law;
- distinguish employer reimbursement from fines earmarked for employee benefit under applicable rules;
- do not assume 'employee used account X' proves negligence or liability.

A legal/policy engine must return decision = `permitted`, `blocked`, or `needs_review` with rule IDs and citations. Fail closed if legal basis or relevant facts are missing. No AI adjudication. Applicable Indonesian wage rules must be revalidated at implementation; see PP 36/2021 as amended, including PP 49/2025.

## Reconciliation invariant tests

- Double-entry journals balanced and immutable after posting.
- `paid + refunded - chargebacks` reconciles provider state; never treat request as settled.
- Invoice total, tax snapshot, allocations, outstanding amount reconcile.
- Case `reconciled` iff each required domain validator returns success, with persistent evidence/report.
- In concurrent cases against same transaction, only one active correction applying; revision check rejects stale proposals.
- Disabling Finance Advanced/Payroll retains incidents in Core.

## Simulation

Clone affected data to isolated environment; run exact deterministic engines + mocked payment/provider responses. Show delta on profit, stock, tax, ledger, payroll, reporting. Simulation approval **does not constitute** authorization to execute real-world refunds/payroll deduction. Copy of customer PII should be masked wherever unnecessary.
