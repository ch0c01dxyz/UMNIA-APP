# 07 — Pricing, Tax, Storage, Backup & Operations

## Billing model

`product = module | bundle | ai_usage | storage | white_label | api` with per-day/week/month/year rates, free trials, entitlements, per-workspace quota, time-bound promo. Platform Owner controls list prices, effective dates, discounts and approval changes. Prices and entitlements versioned; old invoices never silently repriced.

Example user-proposed seed prices (not validated margin): Inventory Basic Rp1.000/day; Rp5.000/week; Rp25.000/month. AI first month free then per request/time/tokens based on selected exclusive billing plan; white-label 7-day trial, recurring duration pricing; API free; workspace included 10GB logical attribution. Storage overage Rp5.000/GB/month or Rp50.000/GB/year. Do not charge per-request and per-token simultaneously without explicit agreement.

Metering: usage event id, workspace, meter, period, units, token input/output model, provider cost, billed amount, deduplication. Use credit ledger/prepaid balance or accumulated invoices for micropayments to avoid gateway fees per Rp10 event. BYOK usage is provider-billed, but platform service charges if any must be disclosed.

## Adaptive Tax Engine

- Tax rules by jurisdiction (national/region/city), service taxonomy, tax obligation/status, place, effective interval, version, tax-included/excluded flag, DPP basis, rounding, overrides approval.
- PPN (Pajak Pertambahan Nilai) is distinct from PBJT regional taxes and other sector obligations. Do not default all business to 11%, do not infer PKP status.
- Always compute tax **before final total is finalized** when exclusive; display price + tax = total; for inclusive prices back-calculate according to legal rule. Snapshot applied rule on transaction, not re-evaluate historical posted invoices on updates.
- Tax rules require source evidence, effective date, review/approval, validation tests and legal/accountant review; rule updates cannot silently rewrite already-posted documents.

## Financial reporting

- Finance Basic: kas masuk/keluar, operational margin; do not label complete SAK EMKM accounting unless required recognition and books are present.
- Finance Advanced: proper chart of accounts, journal, ledger, reconciliation, position statement, profit/loss, notes, period comparison, discrepancy notes.
- Verify framework applicability and completeness for SAK Indonesia untuk EMKM on official IAI references before compliance claims.

## Storage adapter

Application stores durable metadata in PostgreSQL: `object_id`, `workspace_id`, `provider`, `bucket`, `key`, `sha256`, `size`, `mime`, `encrypted`, `classification`, `retention_until`, `created_at`. Use opaque keys, not user filenames; scan uploads; presigned URL TTL and access checks; quota logical attribution by tenant rather than raw shared DB disk metrics.

RustFS deployment constraints: ensure selected object operations are covered by current compatibility matrix; encrypt and authenticate; external backup; validate GET/PUT/HEAD, multipart, presigned, object versioning, list, overwrite, metadata, retention. Future AWS S3 migration: copy through API, checksum and object metadata roundtrip, mapping version identifiers, cutover read-only window, verification and rollback plan. Encrypted RustFS object formats are not automatically portable at file-system level.

## Backup/restore

- `Titik Aman` = business checkpoint; **not** disaster recovery backup.
- Business history correction = compensating records; never rewrite posted payments.
- Workspace export/restore = consistency-group with metadata+object manifests+schema versions; restore to isolated staging first, validate hashes/row counts/journal, then authorized merge/recovery plan.
- Platform DR = PostgreSQL base backups + WAL/PITR and separate RustFS object backup. PG PITR restores full cluster not arbitrary workspace: select tenant restore requires staging cluster + controlled extraction.
- Off-host encrypted backups and practiced restore drill are mandatory; source and backup credentials separated.
- Track backup schedule, health, last verified restore, integrity, RPO/RTO (targets are **TBD** until benchmark and budget), retention and legal deletion requirements.

## Operational readiness

Metrics: request latency p50/p95/p99, DB connections, memory per service, storage latency, queue lag, failed module transitions, corrected vs failed transactions, AI validation blocks, lost webhook retries, backup freshness, restore duration, cross-tenant access denials. No logs with raw secrets or unauthorized sensitive PII.
