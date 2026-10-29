# 05 — AI, Guardrails, Research Datasets & Simulation

## Two AI egress paths

```text
User -> input authorization/data minimization -> mode choice
 Managed -> akanesia-router (configurable target ports 20128/20127) -> response validator
 BYOK -> direct provider adapter (user-controlled model/provider) -> response validator
 -> policy + tool registry -> domain service -> audit
```

Router ports are user-provided configuration, not asserted API protocols. BYOK permits changing **own provider/model settings only**, not admin model registry.

## Guardrails and anti-hallucination as enforceable behaviors

- Input boundary: classify request and tenant, minimize PII, scrub secrets, block unsupported provider domains and SSRF, cap token/media/request size.
- Sandbox: no direct arbitrary SQL, no shell for model output, no executable uploaded documents, bounded CPU/RAM/time/storage, egress allowlist; no production DB secret inside file-processing worker.
- Tool registry: typed schemas, allowlist, per-workspace/policy `allowed_actions`, approval thresholds, rate limits, audit. No arbitrary tool declaration by model.
- Output validator: schema checks, allowed IDs, cross-workspace ownership, parameter bounds, known source citations for facts, no invented transactions. Model text is *untrusted*.
- Business source of truth: denda, tarif, pajak, HPP, payroll dihitung **deterministic code** dengan versioned rules. The model can explain but cannot approve an accounting result.
- Uncertainty handling: `VERIFIED`, `DERIVED`, `ESTIMATED`, `UNKNOWN` tags at field/report level. If historical evidence insufficient, block 'exact' claim.
- Managed AI metering: per-model input/output tokens + cost ledger, credit/quota policy; BYOK provider costs paid by user, UMNIA may meter platform tool usage separately.
- Tool calls can prepare drafts; posted payment/refund, payroll deduction, closed-period corrections require explicit authorization.

## Import/export UIDX

1. File quarantined and scanned. Determine type, sizes, content, Excel formula/macro presence, HTML/XML/CSV injection.
2. Parse safely within sandbox. Build structural representation: sheets, headers, styles, formulas, cell types, named ranges.
3. Map to module fields via exact IDs/SKU first, then aliases, then AI suggestions. Mark low confidence for human review.
4. Dry-run into staging tables with row-level errors and total reconciliation.
5. For EXPORT to user template, allow document engine to fill mapped cells, preserve protected formatting, use domain calculation engine for math, and compare totals to source.
6. Provide preview/diff; requiring confirmation before production import.
7. Save mapping template version; avoid replaying a new template without detecting structural drift.

## Simulation Dataset Registry

Every module must declare `simulation_dataset` manifest (including future modules). Sources:
- `tenant_clone`: point-in-time **isolated** copy subject to consent, minimization, masking and TTL.
- `synthetic`: AI-generated sample data referencing curated sources and deterministic domain constraints.

Owner's **Data Referensi & Simulasi** panel includes `Cari Referensi Terbaru`, `Periksa Data Lama`, `Perbarui Contoh`, `Lihat Sumber`, `Uji Simulasi`, `Setujui`, `Kembalikan Versi`, `Jadwalkan Pemeriksaan`.

Provenance record: module version, dataset version, locale, source URL, publication and retrieval dates, applicable period, license, reviewer, facts/assumptions, validation suites, state (`draft`, `reviewed`, `published`, `deprecated`). Web research produces a **proposal**, not automatically-trusted production rule. Changes tested and approved before publication. Reseach jobs run under constrained egress and quotas.

## Simulator invariants

- No production database credentials; simulation service executes under distinct role/network, no payment gateway creds, no email/SMS/WhatsApp/webhook output.
- Use structural module contract and domain deterministic services with simulation adapters.
- Explicit watermark `DATA PERCOBAAN`, scenario id, timestamp, source version and limitations in every preview/export.
- During merge from simulation branch, accept only validated changeset of allowed master data; do NOT merge payment/journal/salary transactions wholesale.
- Case simulations cover price errors, refund, payroll deduction *eligibility*, Finance edition change, package discounts, resource occupancy, inventory effects.

## Security QA

Prompt-injection file contains instructions to exfiltrate keys -> no secrets sent; cross-tenant tool IDs -> denied; AI proposes total inconsistent with calculator -> reject; prompt asks transfer funds -> demand separate authorization; BYOK endpoint localhost/internal IP -> reject by policy; mock dataset triggers real webhook -> test failure.

## v0.5 anti-hallucination and agent boundary

Product AI outputs follow `09-advanced-anti-hallucination.md`: model creativity/brainstorming is supported; any numerical output must be grounded by versioned calculator results and transaction provenance. Coding agents follow a separate task-evidence process. No provider/model output is an executable financial decision.
