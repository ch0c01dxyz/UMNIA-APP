# UMNIA — Universal Agent Instructions (v0.6 Fast-Track)

Applies to frontier leaders, Flash coding workers, reviewers, QA workers, and UI agents in any compatible harness.

1. **Start with `FAST_TRACK.md` and the relevant task only.** Open detailed domain docs as needed; do not delay coding by reading everything.
2. The repository is **not** an implemented app yet. Specs, SQL reference files and JSON examples are **not** evidence of runtime success.
3. Frontier orchestrates architecture/interface/QA, Flash implements small frozen tasks. Frontier codes directly only when security/complexity requires. Never self-approve security-critical code.
4. Maintain creativity: propose alternatives in `PROPOSALS`, but distinguish observation from hypothesis and do not change frozen scope silently.
5. Never invent SDK APIs, command output, citations, model abilities, installed plugins or passed tests. If unknown, label UNKNOWN, inspect actual environment, and report blocker.
6. Windows must develop with Docker Compose; Linux must run natively, installed with APT for appropriate packages and RustFS official native binary/service. No Docker Linux runtime.
7. Redis and PgBouncer are optional, **disabled initially**. PostgreSQL is transactional truth; use outbox for async work. If PgBouncer enabled, test RLS with transaction-local tenant scope and concurrent cross-tenant requests.
8. Business DB and Admin DB/auth/network are segregated. User BYOK calls provider directly, never `akanesia-router`; managed AI uses router. Sandbox both before request and after response.
9. Money/posted ledger/payment records are immutable; corrections through controlled adjustments. No module uninstall may delete history. Simulation never affects production.
10. Use `DESIGN.md` and `docs/12-design-system-ux.md` for every UI task. If available, use vetted Impeccable or OpenDesign MCP; capture render evidence at mobile/tablet/desktop widths.
11. Every task: scoped file allowlist, tests, review, evidence, assumptions, unresolved limitations; create only the smallest complete change.
12. No legal compliance, payroll deduction, tax declaration, SAK EMKM certification, security guarantee without verified rule sources and authorized review.

## Start sequence

`FAST_TRACK.md` -> F1-01 -> implement, test and merge. Continue independent tasks in parallel and use the next phase when dependencies are met.

## Non-negotiable report

`TASK_ID | FILES CHANGED | TESTS (pass/fail) | RISK / BLOCKERS | NEXT ACTION` (tambahkan bukti keamanan/detail hanya saat relevan).
