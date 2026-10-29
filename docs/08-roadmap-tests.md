> **FAST-TRACK v0.6:** Mulai dari `FAST_TRACK.md` yang menetapkan empat fase. Rencana P0–P8 lama hanya backlog referensi, bukan gate wajib. Lakukan implementasi dan tes sesegera mungkin.

# 08 — Implementation Plan, Test Gates & Agent Handoff

## Sequence (vertical-slice first)

**See `docs/13-detailed-phase-execution.md` for authoritative ticket-level plans, dependencies, evidence and agent assignments.** `docs/10-agent-orchestration.md` defines frontier/Flash roles; `docs/09-advanced-anti-hallucination.md` is mandatory. Redis/PgBouncer are optional and disabled initially; see `docs/11-deployment-matrix.md`.

### P0 — Repository/quality scaffolding

Set up monorepo, Go modules, Nuxt UI, **Windows Docker Compose for all services**, **Linux native APT/systemd (no Docker)**, lint/test/static security checks, OpenAPI generation and reproducible synthetic seeds. CI checks independent of production.

**Gate:** dev Windows 11 and Ubuntu can run a smoke test; no hardcoded secrets; DB migrations repeatable.

### P1 — Identity, tenant and access foundation

Business identity/auth, invite token, workspace/membership/roles/entitlement, separate admin API/database, RLS context, audit/events, storage adapter.

**Gate:** cross-tenant requests rejected across REST, SQL, job, object, file export; admin has no blanket business data read.

### P2 — Module Engine MVP

Module registry schema, manifest validation, capability/dependency graph, edition exclusivity, activation/deactivation, package composer skeleton, migration plan/dry-run.

**Gate:** cycle/exclusive conflict rejected with beginner-friendly explanation; inactive capability cannot be reached by API, AI, or background workers.

### P3 — Rental vertical slice

Customer master, item catalog, physical asset, rental contract, pricing effective dates, inventory movement, statuses, returns/damage/loss, basic reports.

**Gate:** 2 employees editing same asset cannot double-book; prices on historic contracts remain unchanged; deactivate Inventory UI but Rental references still valid.

### P4 — Corrections & snapshots

Core incidents, evidence, impact analysis, maker-checker workflow, balance validations, safe corrections, PITR/backup/restore drills, isolated simulation seed.

**Gate:** paid invoice not overwritten; refund retries no duplicates; stale proposal blocked; malformed backup fails restore; incidents persist without Finance/Payroll.

### P5 — Finance and reconciliation

Cash operational views, chart of accounts/ledger, Finance Basic/Advanced edition migration, historical price import and uncertainty, tax engine and financial readiness.

**Gate:** balance reconciles, missing historical evidence marked unknown, Advanced->Basic keeps immutable journal history read-only.

### P6 — AI and import/export

Managed AI vs BYOK direct, input/output policies, typed tools, file sandbox, spreadsheet mapping and formatting, template memory, tool audit.

**Gate:** prompt injection cannot invoke unauthorized tool; no cross-tenant file access; money calculated via domain code not model.

### P7 — Industry and operations expansion

Generic resource booking -> spa/karaoke/billiard/cinema; restaurant FnD/recipes, supplier, staffing, reports. Billing pricing, promotion, PWA, advanced payroll (after legal review).

**Gate:** overlapping rooms/seats rejected, session billing deterministic, public spa monitor defaults to code, changed packages don't rewrite bookings, payroll deductions fail closed when legal conditions incomplete.

### P8 — Billing, PWA, White-label and hardening

Implement metering, module pricing/bundles, white-label, mobile PWA, pilot operations, security/legal reviews. Never declare production readiness without pilot evidence.

## Test matrix (minimal required)

| Test | Preconditions | Expected |
| --- | --- | --- |
| Cross tenant ID | A token requests B item | Denied, audited, no payload leak |
| RLS bypass role | Business app role without bypass | No B data visible |
| Module conflict | finance.basic + finance.advanced exclusive | Plan proposes migration, not coexistence |
| Downgrade finance | Posted advanced journals exist | Immutable/read-only records preserved |
| Effective price | 2024 tariff, 2025 update, 2024 contract | Original contracted price unchanged |
| Booking race | Two checkout flows same resource/time | One succeeds; other retry/clear denial |
| Payment retry | Duplicate capture/refund callback | One ledger effect |
| Incident case without payroll | Payroll off | Case recorded; deduction unavailable |
| Incident case with payroll | Payroll on, legal conditions missing | Deduction BLOCKED |
| AI cross-tenant | Provider returns foreign record ID | Validator/tool authorization rejects |
| BYOK endpoint SSRF | User adds loopback/private endpoint | Rejected by provider policy |
| Simulation safety | Scenario sends message/payment | No external side effect |
| Storage portability | RustFS -> S3 adapter | Objects & checksum read correctly |
| PITR | Database test cluster failure | Restore succeeds with measured RPO/RTO |
| OpenAPI gating | Finance inactive | Workspace documentation omits unavailable endpoints |
| Workspace quota | Excess upload/storage estimate | Policy defined and accurately enforced |

## NFR & initial benchmarks (targets TBD)

- Benchmark 1 workspace, 100 workspaces, and representative concurrency; pick realistic payloads and traffic from pilot data. Record baseline CPU/RAM/request throughput/latency, plus database IO/storage/worker throughput.
- Load test atomic inventory + booking, invoice settlement, export/report generation, AI request concurrency, bulk import.
- Chaos drills: container restart mid migration, worker crash after DB commit, gateway timeout, object store unavailable, duplicate webhook, corrupted import, expired invite.
- Resource budget requires user hardware information; do not invent guaranteed capacity or costs.

## Coding-agent execution prompt (handoff)

> Implement UMNIA foundation in small, verifiable increments. Begin at P0, then P1 security tests, then P2 module graph. Read all docs and validated manifests before code. Preserve Go modular-monolith boundaries; PostgreSQL business vs admin databases separate; BYOK bypasses akanesia-router; all financial records posted immutable; simulation cannot mutate production. For each phase: write tests first or alongside code, document ADR changes, keep schema migrations reversible where safe, inspect plan/apply mismatch, report failing validations honestly. Do not enable production AI actions/payroll deductions or legal compliance claims without explicit verified policies. Model roles: frontier leader/reviewer, bounded Flash workers; creative proposals welcome but separated from implemented scope. Windows development all Docker Compose, Linux native without Docker; use DESIGN.md and vetted OpenDesign MCP or Impeccable for UI.

## Definition of Done per phase

- Unit/integration tests pass and coverage meaningful on domain invariants.
- Tenant/risk acceptance tests run in CI.
- Migration up and safe rollback/forward recovery documented.
- UX words reviewed for nontechnical users (Bahasa Indonesia).
- Performance sampled and security findings resolved/accepted explicitly.
- Documentation/OpenAPI/examples updated.

## Sources to re-verify before implementation

Current official PostgreSQL docs, RustFS compatibility test matrix, IAI SAK EMKM, JDIH employment rules, official DJP/municipal tax rules, browser/app-store PWA distribution policies. Regulatory and vendor details are time-dependent.
