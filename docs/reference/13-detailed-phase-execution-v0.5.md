# 13 — Detailed Executable Roadmap for Flash-tier Agents

**Status:** Planned work breakdown, not completed work. Relative effort sizes: S (bounded simple), M (interface + tests), L (multi-domain/high risk). Not calendar estimates. Each milestone is a release gate; **do not skip safety prerequisites** to implement screenshots or showcase features. Primary execution via Flash agents, leadership/review via frontier. Production legal/payroll/tax decisions need responsible human approval.

## Cross-cutting acceptance / Definition of Done (DoD)

Each task and milestone must deliver (a) code + tests + docs (b) exact execution evidence on supported runtime (c) no regressions in auth/tenant/money/AI (d) security and risk review (e) user-facing Bahasa Indonesia copy where applicable (f) accessible UI and rendered visual review (g) independently reviewed PR (h) explicit outstanding risks. An endpoint is not done until authorized/unauthorized integration tests exist. Infrastructure is not done until clean install and restart succeed. Migration not done until clean install + upgrade + failure recovery are exercised on test data.

### Cross-platform gates

- Every P0 change runs Windows Docker Compose smoke CI (or a Windows runner; if unavailable, mark NOT TESTED), and Linux-native smoke within milestone release gate. Do not falsely claim Linux parity based on container CI.
- Shared `.env.example` contract with adapter defaults and secret references; no hardcoded development secrets.
- Logging correlation IDs and trace boundaries for admin/business/worker; production keys never in logs.
- Optional Redis/PgBouncer disabled by default; parity tests re-run when enabled.

## Phase P0 — Repository and Developer Experience

**Goal:** one-command repeatable workspace for each OS, testable Go+Nuxt shell, enforce AGENTS.md and contract-first workflow.

**Preconditions:** architecture ADRs; repository owner chooses Go/Node/PostgreSQL minor versions. **Outputs:** monorepo with `apps/web`, `apps/admin`, `services/business-api`, `services/admin-api`, `services/worker`, `packages/contracts`, `packages/ui`, `modules`, `deploy/windows`, `deploy/linux`, docs, tests.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P0-01 | leader | Freeze version matrix, coding conventions, secret policy, dep management and API error envelope | signed ADR docs; no undecided blocking versions |
| P0-02 | Flash | Go health endpoints `GET /health/live`, `GET /health/ready` on business/admin API; graceful shutdown | go test, response status, bad DB readiness |
| P0-03 | Flash | Nuxt public welcome + empty dashboard shell; routes and basic i18n strings | unit + snapshot, mobile render |
| P0-04 | Flash | Windows Dockerfile multi-stage + Compose for web/admin/API/worker/2 DBs/RustFS | `docker compose config`, fresh startup + restart persistent data |
| P0-05 | Flash | Linux APT/native install plan + systemd units, RustFS native installation script with pin/checksum | apt plan lint, systemd unit verification, native smoke VM |
| P0-06 | Flash+review | Migration tool config & version tables separated per DB, no app role schema owner | disposable migration up + rerun; failure handling |
| P0-07 | Flash | Static checks: gofmt, govet, go test, npm lint/typecheck, JSON Schema checks, secret scan | CI run archived |
| P0-08 | Flash | Typed config validation, dotenv template, secret read through injected provider | invalid/missing config fails fast |
| P0-09 | Flash+QA | Fixtures/seed generator for synthetic workspaces, idempotent | deterministic seed checksum; label synthetic |
| P0-10 | leader QA | Windows/Linux parity matrix, baseline idle CPU/RAM measurements, freeze milestone | both host logs; list of untested items |

**Blockers:** Docker not available on Windows; native Linux unsupported package versions; missing secured configuration. **Exit:** fresh boot reproducible, DBs/admin clearly separated, no credentials in git, stable starter CI.

## Phase P1 — Identity, authorization, workspaces, admin isolation

**Depends on P0.** Core entities: `identity`, `verified_contact`, `auth_method`, `workspace`, `membership`, `role_binding`, `invite`, `audit_event`. Password/OTP channel choices only after contact verified; 2FA optional; passkey add later behind capability contract.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P1-01 | leader | threat model tenant/admin + permission taxonomy + contact-login specification | security ADR, misuse-case list |
| P1-02 | Flash | contact verification state machine and rate limiting adapter | OTP replay, expired, wrong channel, enumeration tests |
| P1-03 | Flash+review | password auth with hashing/rotation and sessions/revocation | brute force and session invalidation tests |
| P1-04 | Flash | single-use expiring staff invite issue/redeem | concurrent redemption only one winner |
| P1-05 | Flash+review | workspace/membership and separate workspace access role bindings | invitation cannot join wrong tenant |
| P1-06 | Frontier | DB role grants and RLS tenant policies, transaction-local context | cross tenant SQL + pooled connection contamination tests |
| P1-07 | Frontier | admin plane independent credentials/DB/network and narrow management broker | no admin blanket SELECT; denied broker scopes |
| P1-08 | Flash | audit event filter & retention controls | audit data not exposed to unauthorized roles |
| P1-09 | Flash | employee promotion/delegation effective time and role revocation | old token no longer grants revoked permission |
| P1-10 | leader QA | external REST, DB, storage, worker, export, AI workspace IDOR suite | randomized cross-tenant failure cases |

**Exit:** unauthorized identity cannot read/modify another workspace; admin plane cannot directly read business details; recovery path documented and rate limited.

## Phase P2 — Adaptive Module Engine / Bundle Contract

**Depends on P1.** Persist family/edition/capability/dependencies, module state per workspace. Install not equal to deployment of arbitrary code. All manifests pass JSON Schema and trusted approval pipeline.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P2-01 | Flash | manifest parser/validator; registry CRUD admin-scoped | invalid/missing simulation manifest rejected |
| P2-02 | Flash | capability dependency topological resolver and cycle detector | cycle, missing capability, version mismatch |
| P2-03 | Flash+review | family edition mutual exclusion; dry-run upgrade plan | Basic+Advanced conflict, data unchanged |
| P2-04 | Flash | install/disable capability gating across API and jobs | disabled module 403/404 policy; history preserved |
| P2-05 | Flash+review | module transition preview and checkpoint planning | fault injection before apply; correct states |
| P2-06 | Flash | admin bundle composer manifest incl existing entitlements | no duplicate billing of already-owned module |
| P2-07 | Flash | business module catalog UX novice language | states: free/active/conflict/migration/expired |
| P2-08 | Frontier QA | migration orchestration invariants + state machine hardening | partial failure recovery and no cascade delete |

**Exit:** install/disable safe, simulated migration explains impact, route gating and data retention tested.

## Phase P3 — Rental/Inventory/Customer Vertical Slice

**Depends on P2.** First actual end-to-end business transaction. Entities: `customer`, `catalog_item`, `serialized_asset`, `inventory_movement`, `price_version`, `rental_contract`, `rental_line`, `return_condition`, `incident` reference.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P3-01 | leader | asset vs item vs consumable contract, status transitions | invariant table approval |
| P3-02 | Flash | customer and item CRUD with permission & ID checks | IDOR, soft deletion, dup SKU |
| P3-03 | Flash+review | effective-dated tariff lookup; signed contract rate snapshot | historical price remains same after update |
| P3-04 | Frontier | asset booking exclusion/transaction locks | simultaneous reservation: at most one commit |
| P3-05 | Flash | checkout/checkin and condition/photo attach via RustFS | happy path & missing object authorization |
| P3-06 | Flash | overdue/damage/missing status & notification drafts | no automatic allegation of theft |
| P3-07 | Flash | mobile responsive workflow + QR optional behind flag | role-only action and 360px screenshot |
| P3-08 | QA | API e2e entire rental lifecycle | report based on true stored money/date |

**Exit:** first usable vertical slice; inventory UI deactivation doesn't delete contract references.

## Phase P4 — Safe Restore, Correction and Simulation Base

**Depends on P3.** Core incident records remain even without Finance/Payroll. Posted money immutable. Recovery from backups separate from business reversals.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P4-01 | Flash+review | incident/evidence/case state machine | append-only case event provenance |
| P4-02 | Frontier | correction impact dependency traversal | list all affected invoice, payment, stock, tax refs |
| P4-03 | Flash | maker-checker approval, expiry and stale proposal validation | requestor cannot self-approve risk tier T3 |
| P4-04 | Frontier | immutable paid transaction correction / reversal | double refund and stale correction negative tests |
| P4-05 | Flash | simple Restore Center UI and contextual correction controls | no invalid 'undo all' promise; novice comprehension |
| P4-06 | Frontier+ops | PostgreSQL backup, RustFS off-host copy, recover into isolate | measured RPO/RTO drill, checksums, no overwrite live |
| P4-07 | Frontier+QA | simulation zone isolated credentials/mock egress | simulated payment/WhatsApp impossible |

**Exit:** recovery/reversal not conflated, audit evidence durable, restore verified on disposable environment.

## Phase P5 — Finance Basic/Advanced, HPP and Tax Engine

**Depends on P4.** Use verified domain rule manifests effective-dated. Distinguish operational cash view vs SAK EMKM claim.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P5-01 | Frontier | ledger/accounting model + immutable postings | debit=credit; concurrent duplicate post denied |
| P5-02 | Flash+review | Finance Basic transaction/report projection | cross-check direct ledger aggregates |
| P5-03 | Frontier | Finance Advanced edition projection / lossless downgrade | old journals viewable read-only on Basic |
| P5-04 | Flash+review | effective tax rate/rule registry incl jurisdiction/effective date | dates, exempt, PPN/PBJT non-interchangeable |
| P5-05 | Flash+review | historical rental price CSV mapping + uncertainty flags | future tariffs not retroactive by default |
| P5-06 | Flash+review | HPP cost rule and unit conversion for scoped vertical | fractional precision, rounding, no float |
| P5-07 | Frontier+QA | SAK readiness and formal report mapping with official verification | missing data blocks 'fully compliant' label |
| P5-08 | Flash | incident discrepancy register & export confidentiality flags | no employee confidential notes in public report |

**Exit:** trial-balance reconciliation and Finance Basic/Advanced switch documented and tested; legal/tax claims source-reviewed.

## Phase P6 — AI Paths, Anti-hallucination and Import/Export

**Depends on P1,P3,P4,P5 foundations.** AI managed through akanesia-router; BYOK direct provider; never route BYOK through akanesia. Pre-request minimization and provider allowlist precede egress; both response paths use same validator/guarded tools.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P6-01 | leader | provider contract, error taxonomy, response parsing, budget policy | mock contract fixtures provider-specific |
| P6-02 | Flash+review | managed client for configured akanesia-router 20128/20127 | timeout, retry budget, no router hardcode |
| P6-03 | Flash+review | direct BYOK adapter + encrypted key reference | SSRF/loopback denied; logs redact keys |
| P6-04 | Frontier | policy decision point & typed tool registry | AI cannot bypass tenant/roles/missing modules |
| P6-05 | Frontier+QA | sandbox import XLSX/CSV, preview, mapping approvals | macro never executed, formula injection tests |
| P6-06 | Flash | export data into user-provided template preserved layout | source-to-cell mapping/diff reproducible |
| P6-07 | Flash | UI ask/explain/preview/approve and lineage of each number | unverified numerical claim marked estimate |
| P6-08 | Frontier+QA | adversarial prompt injection suite, model-output fuzzing | attempts cannot execute shell/SQL or cross-tenant tools |

**Exit:** LLM free to reason and suggest; mutation only via deterministic tools + authorization; AI errors fail safe.

## Phase P7 — Booking and Industry Reuse

**Depends on P3/P5.** Reusable resource scheduling (seat, room, table, therapist, equipment) with conflict-safe allocations.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P7-01 | Frontier | generalized booking interval overlap & capacity rules | no double-booking under race |
| P7-02 | Flash | karaoke room/timer/FnD session adapter | tariff rounding, per-session settlement |
| P7-03 | Flash | spa therapist monitor names/codes, permission control | public code default; names restricted |
| P7-04 | Flash | spa package versioning + multi-resource workflow | existing reservation retains version |
| P7-05 | Flash+review | billiard time/open/packs/room transfer | changes billed deterministically |
| P7-06 | Frontier+Flash | cinema seat map hold/payment confirmation | multi-user seat race and hold expiry |
| P7-07 | Flash | kitchen recipe/supplier/inventory connector | stock quantity units and waste cases |

**Exit:** new industry modules reuse capability engine, pricing, tax, incidents, simulations and dataset manifest.

## Phase P8 — Billing, PWA, White-label and Operational Hardening

**Depends on stable module catalog/P5.** All prices configured by Platform Owner effective date, with clear per-day/week/month and usage ceilings.

| Ticket | Worker | Precise deliverables | Automated test/evidence |
|---|---|---|---|
| P8-01 | Flash+review | module rate-card versioning/promo/bundle entitlement rules | overlapping promo, expired module, double charge |
| P8-02 | Flash+review | token/request/storage metering with anti-fraud and idempotency | duplicate telemetry doesn't double bill |
| P8-03 | Flash | PWA install + role-aware navigation and offline notice | mobile installed app role changes; no offline settled-payment lie |
| P8-04 | Flash | white-label brand/identity editor + CSP assets | URL/upload sanitation, route/user scope |
| P8-05 | Frontier+QA | security/backup/legal/privacy pre-launch reviews | live-like restore, data deletion/retention logic |
| P8-06 | Leader/owner | controlled pilot, support runbooks, SLOs and incident drills | measured latency, error, restore times, cost per tenant |

**Exit:** documented operational readiness, not blanket 'production ready' before external dependency/legal reviews.

## Review classes and mandatory tests

- T0-T1: Flash implementation + CI + L1 review.
- T2: contract tests + independent reviewer + leader on architecture changes.
- T3: leader owns architecture, may directly code; security/negative tests, owner approval before irreversible migration or financial release.
- Smoke commands: `go test ./...`, `go vet ./...`, `npm run lint`/`typecheck`/`test` after scripts exist; integration test needs disposable real Postgres/RustFS, not memory-only mock.
- Visual screens: screenshots at 360/768/1440, keyboard and reduced-motion check; UX microcopy novice review.
- If a task cannot be tested, status `PARTIAL` and strict blocker; no false PASS.

## Epic completion reporting

Each epic report: task IDs -> merged PR refs, evidence command outputs with OS labels, acceptance tested, screenshots where relevant, remaining failures, migration/rollback readiness, new ADRs, owner action needed, go/no-go. Maintain `docs/CHANGELOG.md` after actual implemented commits, **not** as pretend completion.

## Next task after this blueprint

Leader starts with `P0-01`: compare existing repo state, choose pinned versions and compose/native layouts, freeze API contract, create actual task packets `tasks/P0-*.yaml`, allocate nonoverlapping workers, and enforce CI. Do not automatically skip to P3, regardless of how capable an agent claims to be.
