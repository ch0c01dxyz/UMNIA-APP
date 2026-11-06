# F1-04B — Activation Contract (PROPOSED, Revision R2)

**Status: PROPOSED Revision R2 — for reviewer freezing. Not frozen. No application code has been written under this contract. R2 is a complete concise rewrite of R1 (826 lines), made because the R1 handoff was truncated in transfer: the reviewer received 360 lines / 26,977 bytes (Part 1 only). The R1 source file was intact (hash matched the report); the loss was in transfer, not in the file. This R2 is delivered whole, in one block, with a canonical checksum. Do not treat an excerpt of it as the contract.**

| Item | Value |
|---|---|
| TASK_ID | F1-04B-CONTRACT-R2 |
| Baseline | `063cf44ea5b76e686a29bad5a389c7588f7c18c0`, branch `f1-03-identity`, tree clean except this untracked file |
| F1-04A scope (accepted) | Read-only registry/catalog only: `module_registry`, `GET /workspaces/{id}/modules`, null-safe CHECK, 500/403 separation. Not full F1, merge, or deployment. |
| This document | `docs/reviews/f1-04b-activation-contract.md` — the ONLY file changed by this task. No code, SQL, config, dependency, commit, push, merge, or deployment. |

**Canonical transport checksum.** SHA-256 of this contract text as UTF-8, normalized to LF line endings with exactly one final newline, without the fence or any wrapper. The value is reported in the delivery message (it cannot be inside the file itself — a self-hash is circular and unverifiable). The file on disk is stored in exactly that normalized form; verify with `sha256sum` after normalizing LF + one final newline.

## Evidence labels

- **VERIFIED** — checked in source at `063cf44` (path:line cited) or in a command actually executed in this session (command and result cited).
- **HARNESS-REPORTED** — measured by the Linux harness in this session on native PostgreSQL 16; not repeated by the reviewer, not integrated endpoint proof.
- **REVIEWER** — inherited from the Leader/QA/Reviewer; not executed here.
- **INFERENCE** — follows from verified facts; a design deduction, not a source fact.
- **PROPOSED** — new contract content, not yet existing behavior.
- **UNKNOWN** — cannot be established from available sources; stays open.
- Sources read: `FAST_TRACK.md`, repo `AGENTS.md`, `docs/03-module-engine.md`, `docs/02-core-data.md`, `docs/06-ux-api.md`, `docs/11-deployment-matrix.md`, `schemas/module-manifest.schema.json`, `examples/*.module.json`, `services/business-api/{main,auth,workspace,store,modules}.go`, `services/business-api/migrations/{001_init,002_identity,003_modules}.sql`, `services/business-api/README.md`. `prompts/F1-04A-frozen-registry-catalog.md` does **not** exist locally; D1–D6 come from the F1-04A task record and `MEMORY.md`. `docs/reviews/windows-local-qa-063cf44.md` does **not** exist locally. **REVIEWER** summary used: invalid autocommit migration exits 3 preserving the exact old CHECK and five rows; valid rerun and fresh 001–003 pass; CI 42183407160 at `063cf44`: 20 Go PASS / 0 FAIL / 0 SKIP. No new CI or Windows checks were performed for this documentation revision.

## 0. Frozen inputs

- **D1 — missing providers (VERIFIED).** No capability provider exists for `catalog.item.read`, `customer.profile.read`, `booking.resource.hold`, or `inventory.stock.read` (cross-reference of all four examples). Under strict resolution, `rental.basic` and `spa.standard` **cannot** activate; only `finance.basic` / `finance.advanced` have empty `requires`.
- **D2 — idempotency.** Activation/deactivation is idempotent with no duplicate audit for a no-op. Migration reruns preserve versioned records.
- **D3 — approval.** No confirm flag or approval framework in F1-04A. A control-plane confirmation cannot stand in for financial-mutation approval.
- **D4 — validation.** Canonical JSON Schema at build/test time; no handwritten Go validator; no runtime untrusted manifest ingestion.
- **D5 — seeding.** Registry seeded by migration 003; no startup loader, upload API, or admin-plane registration endpoint.
- **D6 — capability probe.** Deferred. `available:false` does not prove inactive endpoints reject requests.
- **No entitlement stub returning true is permitted, ever** (leader-frozen).
- **R1–R9 (REVIEWER, R1 prompt):** default-deny entitlement; declaration is not health; no premature guard slice; strict Rental gate; owner-only management; no financial approval framework for metadata state; flat errors, frozen catalog response, idempotent no-op, 409 on version change, API-only backend acceptance; concrete concurrency; new migration 004 with exact markers.
- **R2 corrections (REVIEWER, this prompt):** audit `entity_id` is UUID — the R1 proposal `entity_id=<module_id>` was invalid (§5); advisory-lock uniqueness was overclaimed — hash collisions are possible (§4); entitlement writers must share the concurrency design (§2, §4); the future guard must re-check entitlement, not just active+provides (§6); first activation requires an explicit registered version (§3, §5).
- **Lifecycle scope (INFERENCE from `docs/03-module-engine.md`).** The spec's full state machine (`planning → awaiting_approval → installing → active → suspension_pending → suspended/read_only → …`, dry-run, snapshot, outbox) is out of scope. F1-04B covers only `inactive → active → inactive` for already-registered versions. Upgrade/downgrade, suspension, install-time migrations, dry-run, and approval flows are separate tickets.

## 1. What "active" means — and what it does not (R2)

**PROPOSED definition.** `status='active'` on a `module_installations` row means exactly four recorded facts, nothing more: (1) an entitlement row exists for this workspace + module + version (§2); (2) dependency/conflict checks passed **at activation time** against the registry as it stood then (§5); (3) the workspace owner recorded the intent to use this module version; (4) an `module_activated` audit row was written in the same transaction.

**It does NOT mean (VERIFIED as unavailable today):** the module's services are running/healthy (no module service is deployed); its providers are healthy (none defined; 4 of 5 required capabilities have no provider, D1); its simulation dataset exists/validates (validation runs only at CI build time for the seed manifests, D4; nothing validates at activation); its capabilities are executable (no business endpoint exists, §6); its data tables exist (none — F2 creates them); endpoints enforce the state (no guard is wired, §6). Activation is a *record of entitled intent*, not proof of operation. No API response, README, or UI copy may call an active module "healthy", "operational", "ready", or "verified working". The honest user-facing label is the spec's own beginner string "Diaktifkan" (`docs/03-module-engine.md`). The catalog response gains no health or status fields (frozen F1-04A). The F1 gate step "aktifkan Rental" (§10) is a recorded activation, not proof Rental works — Rental working is F2-01's acceptance.

## 2. Entitlement — default deny (R1; corrected in R2)

R0 proposed finance activation without an entitlement check ("free during the F1 pilot"). **Withdrawn: rejected by R1.** Missing billing does not authorize activation without rights; `docs/03-module-engine.md` requires entitlement in effective rights and license checks in the resolver/change plan, and `docs/02-core-data.md:50` distinguishes `module_entitlements` from `module_installations`. Unknown/absent entitlement denies activation.

### 2.1 The boundary (PROPOSED)

- **Table `module_entitlements`** (name from `docs/02-core-data.md:50` — a spec name, not an implementation; VERIFIED: no such table exists in any migration):

```sql
CREATE TABLE IF NOT EXISTS module_entitlements (
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  module_id    TEXT NOT NULL,
  version      TEXT NOT NULL,
  granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by   TEXT NOT NULL,   -- operator identity, e.g. 'operator:nevada'
  source       TEXT NOT NULL,   -- e.g. 'operator-grant'
  reason       TEXT,
  PRIMARY KEY (workspace_id, module_id),
  FOREIGN KEY (module_id, version) REFERENCES module_registry (module_id, version)
);
```

- **Writer: the deployment operator only.** Runtime role `umnia_business` gets `SELECT` only — no `INSERT`/`UPDATE`/`DELETE` grant. There is **no** entitlement API endpoint, **no** `entitlement` request field on activation, and **no** self-grant path for workspace members (owner included). A member cannot entitle their own workspace.
- **Grant/revoke procedure (PROPOSED form; freezing it is decision D-ENT-3).** The *generic procedure* — template, rules, required fields (workspace, module, version, reason, operator) — is a reviewed document. Each *actual grant record* is a private operational record held by the operator, **not** stored in public Git: real workspace grant files and personal entitlement records must not be committed to the repo. Auditability rests on three things: the reviewed procedure, the row's `granted_by`/`source`/`reason`/`granted_at`, and the operator's private record. No granting authority (endpoint, UI, or job) is implemented in any F1-04B slice — implementing one before its source/authorization is frozen is forbidden by R1.
- **Seed: empty.** Migration 004 creates the table with **zero** entitlement rows. Nothing can activate until an operator grants an entitlement under a frozen procedure (D-ENT-3) and a recorded first-grant decision (D-ENT-2).
- **Activation check (PROPOSED).** Inside the activation transaction, after the permission check and before dependency checks: `EXISTS (SELECT 1 FROM module_entitlements WHERE workspace_id=$1 AND module_id=$2 AND version=$3)`. Absent → **403** `{"error":"modul ini belum menjadi hak workspace ini"}`. 403 (not 409): an authorization denial; the module's existence is already established (unknown module returns 404 earlier, §3). An entitlement for a *different* version does not satisfy the check — entitlement is per version.
- **Concurrency of grants (PROPOSED, §4).** Operator grants and revocations must acquire the **same** workspace lock (§4.2) in the same order as activation (BEGIN → lock workspace → modify entitlement → COMMIT), run as the bootstrap role via `psql`. Otherwise a grant/revoke could interleave with an activation's entitlement check, and the lock would guarantee nothing about checked rights. A grant that does not follow the lock ordering is a procedure violation, not a design property.
- **Revocation (PROPOSED).** Deleting an entitlement row (operator only, same procedure and lock) does not by itself deactivate an already-active module; it blocks *future* activations and re-activations. Whether revocation must force deactivation is decision **D-ENT-4** (open; default: no forced deactivation — a mistaken revocation must not silently switch off a running workspace; the leader may freeze the opposite). Revocation never grants future protected actions (§6): the future guard re-checks entitlement at request time.

### 2.2 Boundary vs deferral (PROPOSED)

| | (A) Boundary above (recommended) | (B) Defer activation entirely |
|---|---|---|
| Ships in F1-04B | Activate/deactivate/state-read with deny-by-default | No activation endpoints; storage + read only |
| Can any activation succeed? | Yes, only after an explicit operator grant | No |
| Is the denial testable? | Yes: activation without entitlement → 403 is runnable | No: nothing to deny against |
| What stops unauthorized activation? | The in-transaction check; empty seed; members have no write path | The absence of the endpoint |
| Trust basis | Reviewed procedure + row metadata + private operator record; partly procedural until an admin plane exists | No activation surface at all |

**Recommendation: (A).** It makes the denial testable and the mechanism provable before any grant. **Stated without overclaim (INFERENCE):** this design alone does not *guarantee* no unauthorized activation — the operator procedure is partly procedural, and nothing technical stops the operator from granting an entitlement that was never decided. The mitigation is review of the procedure plus the row's recorded reason/operator. If the leader judges even a deny-by-default endpoint to be premature surface, (B) remains available and slice B3 does not ship; the leader picks, the contract does not bypass.

## 3. Routes, permission, errors (R5, R7; revised in R2)

### 3.1 Routes (PROPOSED) — each justified by a consumer

| Method + path | Purpose | Justified by |
|---|---|---|
| `POST /workspaces/{id}/modules/{module_id}/activate` | Activate one module version. Body **required**: `{"version": "0.1.0"}` — an explicit registered version (reviewer proposal, §5.2; no default, no "highest"). | F1 gate demo + future UI + B3 acceptance need a way to record activation. |
| `POST /workspaces/{id}/modules/{module_id}/deactivate` | Deactivate. No body. | Same consumers; B3 acceptance (state flip + history preservation). |
| `GET /workspaces/{id}/modules/{module_id}` | Read one module's state. `{"module_id", "version", "status", "activated_at", "deactivated_at"}`; never-activated returns `status: "inactive"` with null timestamps (not 404 — the module is known, its state is inactive). | Gate demo, future UI, and B3 acceptance need to *observe* state; otherwise activation success would only be provable by direct DB reads. |

Shape rationale (INFERENCE): follows the existing mux style (`main.go:124-130`, method + pattern strings, `authMiddleware` wrapper) and POST-with-body style (`POST /workspaces/{id}/invites`, `workspace.go:123`). Dotted module ids are a single path segment. `GET /workspaces/{id}/modules` is **not** changed — F1-04A froze its response to `{"modules": [...]}` only (`modules_integration_test.go:73,95`). No other route is added: no list-active route, no entitlement route, no capability-probe route (D6).

### 3.2 Permission — owner-only (R5)

- New permission **`manage_modules`**, seeded by migration 004 (§4) for role **owner only** (PROPOSED). Manager and cashier do not receive it initially.
- **Explicit mapping (VERIFIED).** Only four permissions exist — `invite_members`, `view_workspace`, `view_members`, `view_audit` (`002_identity.sql:95-104`); owner and manager hold all four, cashier holds only `view_workspace`. No activation permission exists anywhere. `has_permission` is `is_member AND EXISTS membership_roles ⋈ role_permissions` (`002_identity.sql:130-141`). The owner is the workspace creator (`workspace.go:handleCreateWorkspace` inserts the `owner` role).
- **Effective check (PROPOSED).** Inside `beginTenant`, after `requireDB`, `authedSubject`, and `isValidUUID(id)`: `hasPermission(tx, id, subject, "manage_modules")`. A permission-query error returns **500** (`{"error":"izin gagal diperiksa"}`, no SQL detail) per the `f72eac0` convention; only a successful query with `allowed=false` returns **403**. Membership is checked twice in effect: by the permission function (requires active membership) and by RLS on the state tables (§4).
- **Why not manager (R5).** No manager grant merely to simplify testing. **No authoritative requirement mandating manager module access was found (VERIFIED by search):** the 002 seed gives manager the four *existing* permissions, not a mandate for new ones; `docs/06-ux-api.md` requires special multi-party approval for *transferring workspace ownership*, a different act; `FAST_TRACK.md` assigns module management to no role. If the reviewer knows of such a requirement, it must be cited. Manager access is a future decision (D-PERM-1), not an initial-slice default.
- **Catalog read unchanged (R5).** `GET /workspaces/{id}/modules` stays `view_workspace` for owner/manager/cashier (VERIFIED). The state-read route also uses `view_workspace` (PROPOSED): reading state is workspace information, not a capability grant.

### 3.3 Tenant scope (VERIFIED pattern, PROPOSED application)

Every handler runs inside `beginTenant(db, subject, workspace)` (`store.go:29`), which sets `app.current_subject_id` and `app.current_workspace_id` (SET LOCAL). RLS is the second layer; the permission check is the first. No cross-workspace activation route and no admin-plane route exist in this contract. Non-member and cross-workspace requests return **403, never 404** (`workspace.go:handleGetWorkspace` comment). The 404s in §3.4 are only for unknown modules/versions *inside* a workspace the caller is a member of — module ids are global catalog ids.

### 3.4 Errors (PROPOSED; flat envelope preserved, R7)

Current API uses the flat envelope `{"error": "<pesan>"}` (`writeError`, `auth.go:22`). `docs/06-ux-api.md` sketches a nested envelope (`MODULE_NOT_ACTIVE` with `code`, `request_id`, `details`) — **not** current code, **not** introduced here (deferred alignment, D-ERR-1).

| Case | Status | Body (flat, Indonesian, no internals) |
|---|---|---|
| No/invalid/expired token | 401 | `{"error":"token tidak ada"}` / `{"error":"token tidak valid atau kedaluwarsa"}` (VERIFIED, `auth.go:90`) |
| DB not configured | 503 | `{"error":"database tidak dikonfigurasi"}` (VERIFIED, `auth.go:79`) |
| Malformed workspace id / JSON body / missing version | 400 | `{"error":"id workspace tidak valid"}` / `{"error":"body tidak valid"}` (VERIFIED patterns) |
| Non-member / cross-workspace / wrong role (manager, cashier) | 403 | `{"error":"bukan anggota workspace ini"}` / `{"error":"tidak punya izin mengelola modul"}` |
| Unknown `module_id` | 404 | `{"error":"modul tidak dikenal"}` |
| Unknown `version` for a known module | 404 | `{"error":"versi modul tidak terdaftar"}` |
| No entitlement row | 403 | `{"error":"modul ini belum menjadi hak workspace ini"}` (§2) |
| Unresolved non-optional `requires` | 409 | `{"error":"kapabilitas <cap> belum tersedia di workspace ini"}` |
| `conflicts` active / same non-null `exclusive_group` active | 409 | `{"error":"modul <id> bentrok dengan <other> yang sedang aktif"}` |
| Deactivate blocked by an active dependent | 409 | `{"error":"modul <dependent> masih membutuhkan <cap> dari modul ini"}` |
| Different version while a version is pinned | 409 | `{"error":"versi berbeda: gunakan alur upgrade"}` (§5.2) |
| Permission-query / DB / transaction failure | 500 | `{"error":"izin gagal diperiksa"}` / `{"error":"transaksi gagal"}` (VERIFIED patterns, no SQL detail) |

**API-only acceptance (R7)** covers this frozen backend slice only. It never closes the full F1 UI/workflow gate, which additionally needs a web UI and the Rental workflow (F2-01).

## 4. Persistent state, transaction, and concurrency (R8; rewritten in R2)

### 4.1 New migration 004 (PROPOSED)

**New file** `services/business-api/migrations/004_module_installations.sql`, version string `004_module_installations`. Editing 003 is forbidden: 003 is accepted at `063cf44` and already applied to the reviewer's retained QA databases. The migration is rerunnable/idempotent per repo policy (`deploy/linux/configure-postgres.sh:51-52`): `CREATE TABLE IF NOT EXISTS`, `INSERT … ON CONFLICT DO NOTHING`, `DROP POLICY IF EXISTS` before `CREATE POLICY` (the 002 pattern, `002_identity.sql:232-316`). It creates **both** tables: `module_installations` (§4.2) and `module_entitlements` (§2.1, empty).

### 4.2 Table `module_installations` (PROPOSED; name from `docs/02-core-data.md:50`)

```sql
CREATE TABLE IF NOT EXISTS module_installations (
  workspace_id   UUID NOT NULL REFERENCES workspaces(id),
  module_id      TEXT NOT NULL,
  version        TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('active', 'inactive')),
  activated_at   TIMESTAMPTZ,
  deactivated_at TIMESTAMPTZ,
  activated_by   UUID,
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, module_id),
  FOREIGN KEY (module_id, version) REFERENCES module_registry (module_id, version)
);
```

One row per workspace+module; version is a column, not part of the key (only one version active at a time; switching versions is an upgrade, §3.4/§5.2). RLS: `ENABLE` + `FORCE ROW LEVEL SECURITY` (`002_identity.sql:217-230`) with one policy `member_installation` mirroring `member_workspace` (`002_identity.sql:250`): visible/writable only when `workspace_id` equals the transaction's `app.current_workspace_id` and the subject is a member. Same RLS shape for `module_entitlements`. Grants: bootstrap owns both tables; runtime `umnia_business` gets `SELECT, INSERT, UPDATE` on `module_installations` (**no DELETE**) and `SELECT` only on `module_entitlements`. Permission seed in the same migration: `INSERT INTO role_permissions (role, permission) VALUES ('owner','manage_modules') ON CONFLICT DO NOTHING` (owner-only, R5).

### 4.3 Locking — workspace-row lock is primary (PROPOSED; R8)

**R1's advisory-lock proposal is revised.** R1 claimed different workspaces always have different keys and never block. That was wrong: a UUID domain mapped to a 32-bit hash cannot guarantee uniqueness, so collisions are possible in principle. A collision would only serialize (delay) unrelated workspaces — tenant data cannot mix through the lock, because isolation comes from RLS + permission checks, not from the key — but the R1 guarantee is withdrawn.

**Primary (PROPOSED): the workspace row.** The workspace row always exists, so locking it always locks something, with no hash and no magic constant:

```sql
SELECT id FROM workspaces WHERE id = $1 FOR UPDATE;  -- $1 = workspace id (Go pgx placeholder)
```

- **Isolation assumption (VERIFIED):** READ COMMITTED — Go `sql.DB` default; `beginTenant` (`store.go:29`) sets no other level. Under READ COMMITTED, `FOR UPDATE` waits for the locker's commit/rollback, then locks the latest committed row version.
- **Lock order (single lock, single transaction).** One transaction per request inside `beginTenant` (VERIFIED pattern: state change + audit INSERT in the same tx, single `Commit`; `defer tx.Rollback()`): (1) `beginTenant`; (2) authorization — `authedSubject` + `hasPermission(…, "manage_modules")`, before the lock, so a denied caller never holds it (query failure → 500; denial → 403); (3) acquire the workspace-row lock; (4) read current installation state (under the lock); (5) state-dependent branch (§4.4); (6) entitlement check (§2.1) — under the lock, only on a real state change; (7) dependency/conflict checks (§5.1) — under the lock; (8) state upsert; (9) audit INSERT (§5.3) — same tx; (10) single `Commit`.
- **Failure rollback:** any error → `Rollback`; the row lock is released with the transaction; no partial state, no audit row (audit is in the same tx).
- **Cross-workspace independence:** different workspaces are different rows, so their mutations never block each other. One lock per transaction, always the same row, acquired in the same position — no lock-order cycle, no deadlock between module mutations.
- **Considered alternative (PROPOSED, documented):** advisory lock `SELECT pg_advisory_xact_lock(727001, hashtext($1))` — namespaced, no coupling to the workspace row. Tradeoff: 32-bit hash collisions are possible in principle and would spuriously serialize unrelated workspaces (delay only, never a data mix). Kept as the documented alternative if the leader prefers decoupling from the workspace row; the R1 claim that different workspaces "never block" is withdrawn for both primitives as an absolute — for the primary it holds by construction (distinct rows), for the advisory form it does not.
- **Operator grants/revocations (§2.1) use the same lock and order:** BEGIN → workspace-row lock → entitlement INSERT/DELETE → COMMIT, as bootstrap via `psql`. A grant that skips the lock is a procedure violation (§2.1).

### 4.4 Interleavings and no-op ordering (PROPOSED design; primitive HARNESS-REPORTED)

The row-lock primitive was **HARNESS-REPORTED** in this session on native PostgreSQL 16 (system cluster `16/main`, synthetic DB dropped after the probe; no user data involved): session B requesting the **same** row waited **4s** while A held it 5s; session C requesting a **different** row waited **0s**; session E after D's `ROLLBACK` waited **0s**. The reviewer did not repeat this probe; it is not integrated endpoint concurrency proof. (The R1 advisory-lock probe — same-key 4s, different-key 0s, post-rollback 0s, two writers one row — is likewise harness-reported only, and is not claimed as reviewer evidence.)

**No-op ordering, stated explicitly (PROPOSED).** The contract does not promise unconditional 200 and entitlement-first 403 at the same time. For ACTIVATE, the order is: permission → lock → read state → **if active with the same version: 200 no-op, no entitlement check, no audit** (the request asks for the state that already holds; a no-op grants nothing and changes nothing) → **if active with a different version: 409** → **if inactive: entitlement check (403 if absent), then dependency checks (409), then upsert + audit**. For DEACTIVATE, **no entitlement check at all** (reviewer direction): permission → lock → read state → if inactive: 200 no-op → if active: deactivate + audit. An authorized owner must still be able to turn a module off after rights are revoked.

Walk-throughs (each transaction re-reads state only after acquiring the lock): **Activate `finance.basic` (A) vs `finance.advanced` (B), same workspace.** A locks the workspace row; B blocks. A commits (`basic` active + audit). B locks, re-reads (`basic` active), exclusive-group conflict → **409**. **Activate `finance.basic` twice, same version, no row yet.** A locks, inserts the row `active`, commits. B locks, re-reads `active` → **200 no-op, no audit.** One row, one audit row total. **Deactivate (A) vs activate (B) the same module.** Serialized; the later committer's intent wins; each response reports the state as of its own commit. **Different workspaces.** Different rows; concurrent, no interaction. RLS + the workspace-scoped permission check keep their states disjoint.

## 5. Dependencies, version, history (PROPOSED unless VERIFIED)

### 5.1 Dependency and conflict handling (strict)

- **Resolver input (VERIFIED):** `requires[]` (each `capability` + `optional`, schema `schemas/module-manifest.schema.json:78-97`), `conflicts[]` (module ids), `exclusive_group` (string|null).
- **Requires:** every **non-optional** required capability must be provided by an **active** module in the *same* workspace. **Core provides no capabilities today** (VERIFIED) — a require can only be satisfied by another active module. Optional requires never block activation.
- **Conflicts / exclusive_group:** any `conflicts` id active → 409; any other module with the same non-null `exclusive_group` active → 409. Both checks run; `exclusive_group` is structural (it subsumes `finance.basic` ↔ `finance.advanced`).
- **Deactivate:** blocked (409) when an **active** module requires a capability this module provides. Inactive dependents do not block.
- **Consequence (VERIFIED + INFERENCE):** `finance.basic` and `finance.advanced` can activate (empty `requires`, mutually exclusive via `finance-edition`). `rental.basic` cannot (requires `catalog.item.read`, `customer.profile.read`, no provider). `spa.standard` cannot (requires `booking.resource.hold`, `customer.profile.read`; `inventory.stock.read` is optional).

### 5.2 Version determinism (reviewer proposal, R2 §5 — PROPOSED, not implemented)

"Highest registered version" is dropped: it needs a SemVer rule and has no defined interaction with an existing pinned inactive version. The smallest boundary: **first activation requires an explicit registered version in the request body** (§3.1); the named version becomes the pinned version in the installation row. Same-version state changes preserve the pinned version. **A different version returns 409 rather than silently upgrading on reactivation** — including re-activating an inactive module with a version other than its pinned one. Version changes are a future upgrade ticket, not a side effect of activation. Unknown version → 404 (§3.4).

### 5.3 History and the exact audit row (PROPOSED; schema VERIFIED)

**Correction to R1:** R1 proposed `entity_id=<module_id>` (e.g. `finance.basic`). That is invalid: `audit_events.entity_id` is **UUID** (`002_identity.sql:77-85`, line 83), and a module id string cannot be inserted there. The schema is **not** widened and 002 is **not** altered for convenience. The existing schema already has what is needed: `entity_id UUID` (nullable, line 83) and `details JSONB NOT NULL DEFAULT '{}'` (line 84). Smallest proposal — `entity_id = NULL`, module facts in `details`:

```sql
INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id, details)
VALUES ($1, $2, 'module_activated', 'module_installation', NULL,
        '{"module_id":"finance.basic","version":"0.1.0","old_status":null,"new_status":"active"}'::jsonb);
```

Deactivation uses action `'module_deactivated'` with `old_status:"active"`, `new_status:"inactive"`; re-activation after deactivation uses `old_status:"inactive"`, `new_status:"active"`. (Alternative, not chosen: give `module_installations` a surrogate UUID `id` and use it as `entity_id`. It is not justified — the natural PK is `(workspace_id, module_id)` — so the NULL + `details` form is the proposal.) `audit_events` is append-only at the database level: `app.reject_audit_change()` + trigger `audit_events_append_only` reject UPDATE/DELETE (`002_identity.sql:108-117`). The audit INSERT is in the same transaction as the state change (VERIFIED pattern, `workspace.go:62,149`). **Rollback acceptance:** a transaction that writes the state change and the audit row and then rolls back must leave both the installation row and the audit count unchanged (tested in B3 with a rolled-back fixture transaction). No-op requests write nothing (D2). Deactivation **never** deletes rows, drops tables, or rewrites history. Module data does not exist yet; the F2 rule is pinned: every module data table carries `workspace_id`, and deactivation/downgrade keeps its rows (read-only where `downgrade_strategy` is `retain_read_only`, shared where `retain_shared` — `finance.basic` is `retain_shared`, the other three `retain_read_only`; VERIFIED from the examples).

## 6. Capability consumers and the future guard (R3; rewritten in R2)

- **VERIFIED: no real capability consumer exists.** No rental/finance/spa/catalog/booking/inventory endpoint exists in `services/business-api` (grep over `*.go`, excluding tests: only the catalog read and identity/workspace routes, `main.go:124-130`). The catalog list by design "tidak memberi izin pengguna maupun kemampuan yang bisa dieksekusi" (`modules.go:4-11`).
- **Consequence (INFERENCE):** the F1-04 acceptance item "penolakan endpoint modul tidak aktif" cannot be tested against a real business endpoint inside F1-04B. Its status is **NOT IMPLEMENTED / NOT RUN**.
- **Smallest real future consumer (PROPOSED, named).** `POST /workspaces/{id}/rental/contracts` — sketched in `docs/06-ux-api.md` (a documented example, not existing code). First endpoint needing a capability (`rental.contract.write`, provided by `rental.basic`); first endpoint that must reject requests when the module is inactive.
- **Guard boundary (PROPOSED, specified now, implemented in F2-01).** When F2-01 builds that endpoint, it **must** also build and wire a server-side guard in `modules.go` (name fixed then, e.g. `requireActiveCapability`) that, inside the handler's `beginTenant` transaction and after the permission check, allows the request **only if all three hold**: (a) an entitlement row exists for (workspace, module, version); (b) the installation row is `active`; (c) the active module's manifest provides the required capability. Otherwise **403** `{"error":"modul <id> belum diaktifkan untuk workspace ini"}`. The R1 guard (active+provides only) is corrected: **retaining an active state row after revocation must not grant future protected actions** — the guard re-checks entitlement at request time, so existing-active/revoked → **403 deny**. A backend error inside the guard returns **500** (never allow, never 403-as-error). F2-01's acceptance **must** include the disabled-module 403 against that live endpoint, including the revoked-entitlement case. Until then the guard does not exist; no unused helper is built now (R3). No synthetic probe (D6).
- Active catalog declarations are not working/healthy providers (§1).

## 7. Approval — lifecycle vs financial mutations (R6)

No financial approval framework is built for metadata state changes: activation/deactivation changes a state row and an audit row; no money moves, because no finance endpoints exist. Lifecycle approval (whether *activating* needs a second approver) and financial-mutation approval (whether a *money movement* needs approval) are different decisions. The manifest flag `needs_approval_for_mutations` is a **domain** flag constraining future financial mutations through F2-02 endpoints. Activation **neither satisfies it nor drops it** — it stays in the manifest and registry, and F2-02 must enforce it when those endpoints are built. **Lifecycle-policy exception: none is approved.** Activation by the workspace owner without a second approver is the defined behavior of the initial slice (the owner is the accountable actor, §3.2), not an approved exception to any policy, because no lifecycle approval policy for activation has been frozen. If the leader requires a second approver for activation itself (e.g. `risk_class: high` modules — both finance manifests are `high`), that is decision **D-LIFE-1**, open, and slice B3 does not ship until it is specified.

## 8. Implementation slices (PROPOSED)

Order is dependency order. Each slice lands with its own tests green before the next starts. **The guard is not a slice here** (§6) — it is an F2-01 dependency.

### Slice B1 — State + entitlement storage (migration 004)

- **Files:** `services/business-api/migrations/004_module_installations.sql` (new; both tables, RLS, grants, `manage_modules` seed for owner only); `deploy/windows/compose.yml` (migrate argv gains 004); `.github/workflows/ci.yml` (Prepare step gains 004 in the `psql -1` list); `services/business-api/main.go` (`checkDatabase` only: require the four markers, §9).
- **Depends on:** this frozen contract. **Blocks:** B2, B3.
- **Acceptance:** fresh 001–004 under one `psql -1` succeeds; rerun of 004 preserves installation and entitlement rows; a 003-only database makes `/readyz` report degraded (§9); runtime grants verified (installation: INSERT/UPDATE allowed, DELETE denied; entitlement: SELECT only — INSERT as runtime role denied); direct-DB cross-tenant read of both tables returns 0 rows; `docker compose config` argv shows 001–004; `check-deployment.sh` green.

### Slice B2 — Activation permission (owner-only)

- **Files:** migration 004 (the `manage_modules` seed row — part of B1's file, reviewed with B1); `services/business-api/modules_integration_test.go` (permission-matrix tests only).
- **Depends on:** B1. **Blocks:** B3.
- **Acceptance:** owner can activate; manager gets 403 on activate/deactivate (R5); cashier gets 403; non-member and cross-workspace get 403; a permission-query fault returns 500 (same regression shape as F1-04A fix 1); `has_permission` returns true for the new permission only for the owner role (direct-DB check).

### Slice B3 — Activate / deactivate / state read

- **Files:** `services/business-api/modules.go` (handlers + resolver + row lock); `services/business-api/main.go` (three routes only); `services/business-api/modules_integration_test.go` (handler tests); `services/business-api/README.md` (F1-04B section: routes, errors, what `active` means per §1, limits).
- **Depends on:** B1, B2, and frozen D-ENT-2/D-ENT-3 (first grants + procedure — without them no activation can succeed in any environment, by design).
- **Acceptance:** owner activates `finance.basic` with explicit version → 200 + state row `active` + one `module_activated` audit row with the exact §5.3 shape (fixture inserts the entitlement row via the bootstrap connection, mimicking the operator grant — a fixture, not a granting authority); activation **without** an entitlement row → 403; activation without a version in the body → 400; activation with an unregistered version → 404; re-activate same version → 200, no new audit row, `updated_at` unchanged; activate `finance.advanced` while `finance.basic` is active → 409 (exclusive group); deactivate `finance.basic` → 200 + `module_deactivated` audit, row kept `inactive`; re-deactivate → 200, no new audit; deactivate after entitlement revocation → 200 (no entitlement needed, §4.4); activate `rental.basic` → 409 naming `catalog.item.read`; activate `spa.standard` → 409 naming `booking.resource.hold`; unknown module → 404; different-version activate while pinned → 409; deactivate blocked by an active dependent → 409 (tested with a synthetic registry row in the fixture, NOT by editing the four examples); concurrent same-version activates → both 200, one row, one audit row; concurrent activate `finance.basic` vs `finance.advanced` → one 200, one 409; concurrent activate vs deactivate → serialized, last committer wins, each response matches its own commit; rolled-back transaction leaves state row and audit count unchanged (§5.3); audit history unchanged by deactivation (append-only trigger still rejects UPDATE/DELETE); full suite green, no SKIP in CI.
- **NOT in B3's acceptance (explicit):** endpoint rejection for disabled modules — that belongs to F2-01 (§6). B3 claims state/audit/denial coverage only.

## 9. Readiness and migrations (R9)

- **New migration for new schema (PROPOSED).** 004 is a new file (§4.1). Accepted 001–003 preserved untouched. No edit to 003, no edit to any example manifest.
- **Exact required markers (VERIFIED names; PROPOSED requirement).** `schema_migrations` must contain exactly: `001_init`, `002_identity`, `003_modules`, `004_module_installations`. `version` is `TEXT PRIMARY KEY` (`001_init.sql:6-8`), so versions are unique; a count over an exact `IN (…` list of the four therefore proves the set — but the contract requires the check to name the four markers explicitly and compare the returned set, not to assert a bare number. `checkDatabase` (`main.go:70-78`) must require the four-marker set; a database missing any one reports `degraded`.
- **All actual invocation paths (VERIFIED).** (1) Windows Compose: the `migrate` service argv list, one `psql -1` invocation with `-f` per file (004 appended). (2) CI: the `business-api` job Prepare step, same `psql -1` invocation. (3) Linux native: `deploy/linux/configure-postgres.sh:53` glob (`services/business-api/migrations/*.sql`), one `psql -v ON_ERROR_STOP=1` invocation **per file, without `-1`** — per-statement autocommit; 004 statements must each be individually safe on rerun (atomic-CHECK lesson, `063cf44`). (4) Manual: direct `psql -f` by the operator (same semantics as (3) unless `-1` is passed explicitly).
- **Incomplete-schema checks (PROPOSED).** `/readyz` returns 503 `degraded` while any marker is missing. The API still starts without a database (no ping at boot, `main.go`); handlers do not paper over a missing schema — a handler touching a missing table fails with the controlled 500 convention, a deployment error fixed by applying the migration, not a runtime condition. Applying the migration is a deployment precondition before serving traffic. A count of versions alone is never cited as proof; the proof is the named four-marker set above.

## 10. Recommendation and decisions

### Recommendation (PROPOSED)

Ship B1 → B2 → B3 in order, finance-only activation first (D1), owner-only `manage_modules` (R5), deny-by-default entitlement with operator-only grants (R1), strict resolver, no approval framework for the metadata state change (R6), guard specified now but built and wired by F2-01 (R3). Smallest complete slice that makes "activate a module" real without inventing a billing system, an approval system, a granting authority, or a capability provider that does not exist.

### The F1-gate tension (R4)

`FAST_TRACK.md` defines the F1 gate as: login → workspace → invite staff → **aktifkan Rental**. Under the strict resolver (D1), `rental.basic` **cannot** activate until `catalog.item.read` and `customer.profile.read` have providers — F2-01 work (Customer + Inventory Basic). Full F1 therefore remains **incomplete** until the real providers exist and the Rental gate passes. This is a dependency, not a redefined gate: the gate text is unchanged. (a) The plan: keep the resolver strict; F1-04B proves activation on `finance.basic`/`finance.advanced` (once entitled); the gate completes when F2-01 delivers the providers and `rental.basic` activates. (b) Redefine the gate to "activate any module": **REJECTED by R4** — completing F1 by redefinition while Rental is unactivatable. (c) Temporarily relax the resolver: **REJECTED by R4** — no fake providers, no weakening of requirements. Reopening (b) or (c) needs a new explicit leader decision.

### Decisions — one finite table (PROPOSED defaults; none is approved yet)

| ID | Question | Proposed default | Depends on / blocks |
|---|---|---|---|
| D-PERM-1 | Does `manager` get `manage_modules`? | Owner-only initially (R5) | Blocks B2 if changed |
| D-ENT-1 | Entitlement model: boundary (§2.1) or defer (§2.2 B)? | Boundary (A): deny-by-default + operator grants | Blocks B3 if (B) is chosen |
| D-ENT-2 | Which entitlements are granted first, by whose recorded decision? | None seeded; first grants are a recorded leader decision | Blocks any successful activation |
| D-ENT-3 | Grant procedure form + private records? | Generic reviewed procedure; actual records private, never in public Git | Must freeze before any grant; blocks B3 in practice |
| D-ENT-4 | Does revocation force deactivation? | No — forward-looking only | Blocks §6 guard semantics if changed |
| D-LIFE-1 | Does activation itself need a second approver? | No second approver; no lifecycle exception approved | If required, B3 does not ship until specified |
| D-ERR-1 | Nested error envelope (`MODULE_NOT_ACTIVE`) now or stay flat? | Stay flat; separate alignment ticket | — |
| D-UI-1 | Is API-only demo enough for the backend slice? | API-only for the slice; never closes the F1 UI/workflow gate | — |
| D-GATE-1 | How is "aktifkan Rental" satisfied? | Option (a) only; (b)/(c) rejected by R4 | Depends on F2-01 providers |
| D-VER-1 | Version switching while active? | 409; explicit version required (§5.2, reviewer proposal) | — |

### Explicitly out of scope (PROPOSED)

Full lifecycle states (`planning`, `awaiting_approval`, `installing`, `suspended`, `migrating`), module install-time migrations, dry-run/simulation, outbox/integration notifications, billing/pricing, admin-plane endpoints, any granting authority (endpoint/UI/job), startup loaders, manifest upload, UI, F2 business endpoints, the capability guard's implementation (F2-01, §6), and any change to accepted 003 or the four example manifests.

## Appendix — verified ground facts (at `063cf44`, plus session checks)

- Routes today: `GET /`, `/healthz`, `/readyz`, `POST /auth/register`, `POST /auth/login`, `POST /workspaces`, `GET /workspaces/{id}`, `GET /workspaces/{id}/members`, `GET /workspaces/{id}/modules`, `POST /workspaces/{id}/invites`, `POST /invites/accept`, `GET /workspaces/{id}/audit` (`main.go:118-133`). No other business routes.
- Registry: `module_registry(module_id, version, manifest, registered_at)`, PK `(module_id, version)`, null-safe CHECK, owner = bootstrap role, `GRANT SELECT` only to `umnia_business` (`003_modules.sql:16-55`). No RLS.
- Readiness requires `002_identity` + `003_modules` (`main.go:70-78`). `schema_migrations.version` is `TEXT PRIMARY KEY` (`001_init.sql:6-8`).
- Roles/permissions: owner/manager = `invite_members, view_workspace, view_members, view_audit`; cashier = `view_workspace` only (`002_identity.sql:95-104`).
- Audit: `audit_events` (`002_identity.sql:77-85`): `id UUID PK`, `workspace_id UUID`, `actor_subject_id UUID`, `action TEXT`, `entity_type TEXT`, `entity_id UUID` (nullable), `details JSONB NOT NULL DEFAULT '{}'`, `created_at`. Append-only trigger (`002_identity.sql:108-117`); audit INSERT inside the business transaction (`workspace.go:62,149`, five columns, no `details` — current code leaves the default `'{}'`).
- Manifests (VERIFIED, all `0.1.0`, schema v0.4): `finance.basic` (provides `finance.summary.read`, `finance.cashflow.read`; requires []; conflicts `finance.advanced`; group `finance-edition`; `risk_class: high`, `needs_approval_for_mutations: true`; downgrade `retain_shared`); `finance.advanced` (adds `finance.journal.post`, `finance.discrepancy.view`, `finance.discrepancy.export`; conflicts `finance.basic`; group `finance-edition`; high/true; downgrade `retain_read_only`); `rental.basic` (provides `rental.contract.read/write`, `rental.return.manage`; requires `catalog.item.read`, `customer.profile.read`; conflicts `rental.advanced`; group `rental-edition`; medium/false; downgrade `retain_read_only`); `spa.standard` (provides `spa.therapist.display_policy`, `spa.packages.manage/versioning`, `spa.appointments.manage`; requires `booking.resource.hold`, `customer.profile.read`, `inventory.stock.read` optional; conflicts []; group `spa-edition`; medium/false; downgrade `retain_read_only`).
- No provider exists for `catalog.item.read`, `customer.profile.read`, `booking.resource.hold`, `inventory.stock.read` (cross-reference of the four `provides` lists). No advisory lock is used in the repo (VERIFIED: grep for `pg_advisory` over `services/` and `deploy/` is empty).
- Row-lock probe **HARNESS-REPORTED** this session on native PostgreSQL 16 (system cluster `16/main`, synthetic DB `umnia_r2_lockcheck`, dropped after; no user data): same-row request waited 4s while the holder slept 5s; different-row request waited 0s; post-`ROLLBACK` request waited 0s. The reviewer did not repeat this probe; it is not integrated endpoint proof. The R1 advisory-lock probe is likewise harness-reported only.
- **REVIEWER** (inherited, not executed here): F1-04A accepted at `063cf44` in read-only scope; invalid autocommit migration exits 3 preserving the exact old CHECK and five rows; valid rerun and fresh 001–003 pass; CI 42183407160: 20 Go PASS / 0 FAIL / 0 SKIP. This acceptance is preserved as-is; it is not upgraded to full F1 acceptance.

## R2 delivery reconciliation + B1 status (note, 2026-11-07)

- **R2 source (VERIFIED by read-back + re-hash, 2026-11-06):** 286 lines, 46,726 bytes, SHA-256 `61b5d26fa226850387709a89b4f0a8a998310052e1d6a09cea35869341bdb5c0`.
- **Reviewer-extracted body (REVIEWER):** 255 lines, 44,898 bytes, SHA-256 `e08d9deae6120bdbe86468c61280f90bf983edfccd9e03af366bb4f5c4ece488` — 31 lines / 1,828 bytes short of the sent text. This is a contract-body comparison, not a wrapper-hash comparison.
- **Reconciliation (VERIFIED 2026-11-07):** the contract block in the stored sent message was extracted from chat history and is byte-identical to the source file (286 lines, 46,726 bytes, SHA-256 `61b5d26f…`). The loss did not happen in transcription. Cause of the reviewer's shortfall: **UNKNOWN** — their output.txt / extraction method is not available here. Candidate transforms of the source file do not reproduce it (removing the four SQL code blocks gives 251 lines / 45,310 bytes; removing blank lines gives 206 lines). Plausible hazard class (hypothesis, not a finding): the contract contains nested ```sql fenced blocks inside the outer ```markdown fence, which break naive fence parsing — a first-fence extraction demonstrably yields only 49 lines. The full source file is the authoritative copy; verify it by hashing the file, not the chat text.
- **B1 status (frozen ticket F1-04B-B1-STORAGE):** B1 implementation follows the frozen ticket, which supersedes conflicting B1 proposals in this R2 document. This note is the only change to the contract under the B1 ticket. The whole F1-04B contract, B3 endpoints, real entitlement grants, and the full F1 gate remain NOT approved.
