# 11 — Two Supported Development Runtimes (Windows Docker / Linux Native)

## Mandatory decision

| Dimension | Windows 11 dev | Ubuntu/Debian Linux dev/staging/prod |
|---|---|---|
| Containers | **MANDATORY** Docker Engine via Docker Desktop/WSL2, Docker Compose | **NOT USED** for UMNIA runtime; native services via APT/systemd |
| Go app | container | compiled native binary + systemd |
| Nuxt dev/build | container | Node + package manager native; build artifact/systemd or static host as appropriate |
| PostgreSQL | container with named persistent volume | `apt install postgresql ...` pinned compatible major; config/role DB separately |
| RustFS | container, private ports | official binary/service with config in `/etc/default/rustfs` |
| Redis | optional Compose profile (off by default) | optional APT + systemd (off by default) |
| PgBouncer | optional Compose profile (off by default) | optional APT + systemd (off by default) |
| Reverse proxy | container for local dev if needed | native nginx/caddy (APT where available) |
| Logs | container logs/stdout | journald + rotated application logs |
| Persistence | named volumes, explicit snapshots | filesystem paths, ownership, backups, systemd controls |

The same `/config` schema, environment variable names, migrations, API versions and database invariants apply across environments. **No Linux runtime dependency on Docker or Docker Compose**, including deployment scripts. Linux installation can use APT for available OS packages and **pinned/checksummed native binary for RustFS (verify publisher signatures if published)**; do not claim RustFS has an official APT package. For Go/Node, select tested supported versions; if distro APT version is too old, add a trusted vendor APT repository where feasible or document separately pinned binaries—never silently install an unsupported old version.

## Windows: Docker-only local development

Prerequisites: Windows 11, virtualization enabled, WSL2 integration, Docker Desktop and Compose v2. Docker development config uses bind mounts for code and named volumes for data, no host Postgres/RustFS required.

### Proposed Windows commands (run after scaffolding exists)

```powershell
Copy-Item .env.example .env
# Fill development secrets (do NOT commit .env)
docker compose -f deploy/windows/compose.yml config
docker compose -f deploy/windows/compose.yml up -d --build
docker compose -f deploy/windows/compose.yml ps
docker compose -f deploy/windows/compose.yml exec business-api go test ./...
docker compose -f deploy/windows/compose.yml logs --tail 80 business-api
```

`deploy/windows/compose.yml` is a **planned P0 artifact**, not present in this architecture-only zip. Expose app/UI only to localhost for dev; Admin API and RustFS console must not be publicly accessible. Keep service DB hostnames internal; healthcheck order should not be mistaken for guaranteed readiness. Windows file/watch performance should be tested with code in WSL2 filesystem if bind mounting is slow. Use pinned image tags/digests after verification; never `latest` in reproducible project lock.

Proposed services: `web`, `admin-web`, `business-api`, `admin-api`, `worker`, `postgres-business`, `postgres-admin`, `rustfs`, optional `redis`, `pgbouncer`. Logical admin/business DB segregation **requires distinct instances/credentials and networks for security-sensitive production**; dev Compose may run separate named services with separated volumes and accounts. Secrets via dev-only env file or secret mounts; no hard-coded defaults.

## Linux: native installation, no Docker

Validated OS targets at release time (planned): Ubuntu LTS and Debian stable that satisfy supported PostgreSQL/Go/Node/openssl requirements. Actual compatibility test must pin OS release and package versions, rather than assume every `apt` release works.

### Installation plan (commands illustrative, apt package names checked per distro)

```bash
sudo apt update
sudo apt install -y ca-certificates curl git unzip openssl postgresql postgresql-client nginx
# Install compatible Go/Node runtime using approved pinned OS/vendor packages.
# Review downloaded installer script/binary provenance before running.
# For RustFS follow official native Linux binary/systemd installation instructions.
sudo systemctl enable --now postgresql
sudo systemctl status postgresql --no-pager
```

Official RustFS guidance: native installer places `/usr/local/bin/rustfs`, registers `rustfs.service`, and uses `/etc/default/rustfs`; **review installer and checksums before execution**, set unique secret keys, configure private binding/TLS, then restart service. Production database, object store, worker and app run as distinct least-privileged system users. Create explicit `umnia-business-api.service`, `umnia-admin-api.service`, `umnia-worker.service`, `umnia-web.service` units with restart rules, filesystem restrictions and environment files; admin identity/control plane separated in auth and DB from tenant plane.

### Planned Linux provision output

```text
deploy/linux/
  README.md
  install-prerequisites.sh       # idempotent; apt and supported version checks
  install-rustfs.sh              # verified native download; never Docker
  configure-postgres.sh         # separately scoped admin & business roles
  install-umnia.sh              # place signed/pinned binaries/assets
  env/umnia-business.env.example
  systemd/{umnia-business-api,umnia-admin-api,umnia-worker,umnia-web}.service
  nginx/umnia.conf
  verify-host.sh                # compare health and prerequisites
  backup-restore-drill.sh       # test only; no accidental production rollback
```

No script above has yet been implemented/tested. Never run bootstrap from downloaded repo unreviewed as root. Production rollouts require a non-root deploy user, checksums/signatures, least-privilege DB roles, backups and staged migrations.

## Configuration parity / contract

```text
APP_ENV                  # development|staging|production
HTTP_LISTEN_ADDR
BUSINESS_DATABASE_URL    # business role, business DB only
ADMIN_DATABASE_URL       # admin-plane role, admin DB only; not injected into business service
STORAGE_DRIVER           # s3-compatible (RustFS now; AWS S3 later)
STORAGE_ENDPOINT
STORAGE_BUCKET
STORAGE_REGION
STORAGE_CREDENTIAL_REF    # secret reference, no key in git
AI_MANAGED_ROUTER_URL    # akanesia-router configured port 20128/20127
AI_BYOK_ALLOWED_PROVIDERS # allowlist; no arbitrary localhost/private routes
REDIS_ENABLED            # false default
PGBOUNCER_ENABLED        # false default
OUTBOX_BACKEND           # postgres default
```

Secrets **never** logged, checked into `env.example`, placed in client builds, or passed to model prompts. Protect admin credentials independently.

## Redis ADR: optional / not installed in initial P0

Start with PostgreSQL outbox + worker job table (`FOR UPDATE SKIP LOCKED`, retry_count, available_at, dead-letter status). Measure DB load, queue latency, backlog and contention before introducing another service. Add Redis when genuinely useful for ephemeral cache/rate limiting, distributed presence, high fan-out, or worker queue distribution if needed. Even without Redis, multi-instance rate limiting for login/OTP must use a coordinated persistent policy store (for example a PostgreSQL-backed limiter), not isolated in-process counters. **Never** rely on Redis cache as the authoritative source for money, inventory or legal audit. If Redis enabled, give TTL, eviction, outage fallback, and key prefix per workspace; encrypted transport/network restricted; no tenant ID provided by client used unvalidated. Backend feature interface must hide whether cache is PostgreSQL-backed/in-memory/Redis. Document job dedupe and durable outbox even after Redis added.

## PgBouncer ADR: optional / not installed in initial P0

Use native Go `database/sql` / pgx pool and metrics first (`max_open`, `max_idle`, lifetime, wait_count, pg_stat_activity). Enable PgBouncer only if connections are the bottleneck. Session pooling safest for feature compatibility but savings smaller; transaction pooling possible **after** end-to-end session state tests.

### RLS transaction context mandatory

Never set tenant context with a session-wide `SET app.workspace_id = ...` on a pooled connection. For each business operation:

```sql
BEGIN;
SELECT set_config('app.workspace_id', $1, true); -- true = transaction-local
-- execute workspace-scope queries and domain changes
COMMIT;
```

This statement is illustrative: ensure only trusted backend supplies validated workspace UUID; ensure query and `set_config` share exactly the same transaction + connection. RLS policy must **fail closed** when tenant context is missing; DB role has no BYPASSRLS. Use independent `workspace_id` ownership checks for object storage and AI tool calls. Different client/session pooling modes must test tenant A->B contamination with concurrency. `SET LOCAL` is transaction-bound; prepared statement interoperability depends on PgBouncer version/config. For transaction pooling, no session-state features, SQL-level named prepare, session advisory locks, long session temp tables. Use direct connection for privileged schema migrations/maintenance, not tenant runtime pool. Keep admin and business pools separate.

### Performance activation gates

- Baseline before enabling: request p50/p95/p99 latency, connection utilization, wait time, DB CPU, memory, lock waits, worker lag, queue backlog and errors.
- Enable only if baseline identifies avoidable connection contention or repeated expensive reads, then compare under same load.
- Record cost: resident RAM, processes, monitoring, failure modes, attack surface and operational burden.
- Disable/revert path documented. No universal numeric threshold without real workload/hardware.

## Cross-environment parity tests

- Contract/API identical across Windows Compose and Linux native.
- Both environments start with secrets from separate provider, migrations `up` tested and invalid rollback guarded.
- Install/deactivate module behaviors identical.
- DB RLS, job delivery, RustFS objects, backup restore, AI managed/BYOK gating, admin separation tested.
- Kill worker mid job and show eventual retry without double posting.
- Linux native boot survives restart; Windows Compose boot survives host restart in dev with data retained.
- Manual disaster recovery tested on disposable replicas, not first on live business data.

## References

- RustFS native Linux installation: https://docs.rustfs.com/en/installation/linux/quick-start
- PgBouncer feature compatibility: https://www.pgbouncer.org/features.html
- PgBouncer FAQ prepared statements: https://www.pgbouncer.org/faq.html
- Redis reference use cases: https://redis.io/docs/latest/develop/use-cases/
