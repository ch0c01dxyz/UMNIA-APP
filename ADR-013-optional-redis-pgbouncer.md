# ADR-013 — Redis/PgBouncer deferred until evidence requires them

Status: Accepted for blueprint v0.5, revisit after representative benchmarks.

Context: UMNIA starts as cost-sensitive Go modular monolith using PostgreSQL outbox, with RLS tenant isolation and limited operations budget.

Decision: **Redis=off, PgBouncer=off by default in P0**. Build small interfaces for optional cache, rate limiting, job dispatch and connection DSNs. Activate after benchmarks show a specific problem. Always retain PostgreSQL as system of record; durability/retry semantics unchanged when Redis added. PgBouncer transaction pooling requires proven tenant context safety (`SET LOCAL` per transaction), driver prepared-statement compatibility tests, and isolated migration connection.

Consequences: lower RAM/ops complexity early; may need scaling before load grows. This is a conscious trade-off, not a claim Redis/PgBouncer are unnecessary at all scales.

Refs: https://www.pgbouncer.org/features.html ; https://redis.io/docs/latest/develop/use-cases/
