#!/usr/bin/env bash
# F1-FOUNDATION-REMEDIATION-REVIEW — part 3: supported old-volume upgrade
# via dump/restore into a fresh database.
# WHY NOT in-place demotion (TESTED 2026-11-05, mech cluster port 55435):
# the old umnia_business IS the bootstrap superuser of its volume, and
# PostgreSQL refuses to demote/drop/reassign it:
#   "permission denied to alter role / The bootstrap user must have the
#    SUPERUSER attribute" and "cannot drop role ... required by the
#    database system". So the README's old ALTER ROLE path is impossible
# and is replaced by: pg_dump --no-owner -> fresh DB -> re-run migrations.
# Synthetic fixture passwords only. Never reads the repo .env.
set -u
REPO=~/workspace/UMNIA-APP
OUT=~/workspace/review/F1-FOUNDATION-REMEDIATION-REVIEW/evidence
mkdir -p "$OUT"
export PATH=~/workspace/tools/go/bin:/usr/lib/postgresql/16/bin:$PATH
BOOT_PW=review-bootstrap-only
RUN_PW=review-runtime-only
MIG="$REPO/services/business-api/migrations"
LEGACY_DIR=/var/lib/postgresql/review-legacy
LEGACY_PORT=55434
FAILS=0
log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$OUT/run3.log"; }
run_step() {
  local label="$1" outfile="$2"; shift 2
  "$@" >"$outfile" 2>&1
  local rc=$?
  log "$label exit=$rc (output: $outfile)"
  return $rc
}
echo "=== REVIEW RUN 2 part 3 — $(date -Iseconds) ===" > "$OUT/run3.log"
log "go: $(go version)"
log "psql: $(psql --version)"

log "== RESTORE-UPGRADE: start legacy cluster, dump review_legacy =="
run_step "restore: start legacy cluster" "$OUT/restore-00-start.log" \
  sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$LEGACY_DIR" -o "-p $LEGACY_PORT -c listen_addresses='localhost'" -l "$LEGACY_DIR/server.log" start
run_step "restore: pg_dump --no-owner review_legacy" "$OUT/restore-01-dump.log" \
  pg_dump -h localhost -p "$LEGACY_PORT" -U umnia_business -d review_legacy --no-owner -f "$OUT/legacy-dump.sql"
TOKO_N=$(grep -c "Toko Lama" "$OUT/legacy-dump.sql" || true)
log "restore: 'Toko Lama' occurrences in dump: $TOKO_N (want >=1)"
log "== RESTORE-UPGRADE: fresh DB on main cluster, restore, re-migrate =="
run_step "restore: recreate review_restored" "$OUT/restore-02-createdb.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=0 -c "DROP DATABASE IF EXISTS review_restored;" -c "CREATE DATABASE review_restored OWNER umnia_business;"
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_restored \
  -v ON_ERROR_STOP=1 -f "$OUT/legacy-dump.sql" >"$OUT/restore-03-restore.log" 2>&1
log "restore: restore dump exit=$? (output: $OUT/restore-03-restore.log)"
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_restored \
  -v ON_ERROR_STOP=1 -1 \
  -f "$MIG/001_init.sql" -f "$MIG/002_identity.sql" >"$OUT/restore-04-migrate.log" 2>&1
log "restore: re-run migrations exit=$? (output: $OUT/restore-04-migrate.log)"
run_step "restore: inspect restored DB" "$OUT/restore-05-inspect.log" \
  sudo -u postgres psql -d review_restored -v ON_ERROR_STOP=1 < "$OUT/../inspect.sql"
GRANT_N=$(sudo -u postgres psql -d review_restored -tAc \
  "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema='app' AND grantee='umnia_business' AND privilege_type='EXECUTE';")
log "restore: helpers granting EXECUTE to umnia_business: $GRANT_N (want 6)"
if [ "$GRANT_N" != "6" ]; then log "REGRESSION FAIL: EXECUTE grants missing after restore"; FAILS=$((FAILS + 1)); fi
ROLE_ATTRS=$(sudo -u postgres psql -d review_restored -tAc \
  "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname='umnia_business';")
log "restore: umnia_business super/bypass = $ROLE_ATTRS (want false/false)"
if [ "$ROLE_ATTRS" != "false/false" ]; then log "REGRESSION FAIL: runtime role still superuser/bypass"; FAILS=$((FAILS + 1)); fi
run_step "restore: fixture preserved?" "$OUT/restore-06-fixture-check.log" \
  sudo -u postgres psql -d review_restored -v ON_ERROR_STOP=1 \
  -c "SELECT id, name FROM workspaces WHERE id='22222222-2222-2222-2222-222222222222';" \
  -c "SELECT count(*) AS memberships FROM workspace_memberships WHERE workspace_id='22222222-2222-2222-2222-222222222222';" \
  -c "SELECT version FROM schema_migrations ORDER BY version;"
log "== RESTORE-UPGRADE: live API on restored DB (runtime role) =="
BUSINESS_DATABASE_URL="postgres://umnia_business:$RUN_PW@localhost:5432/review_restored?sslmode=disable" \
  HTTP_LISTEN_ADDR=":18084" ~/workspace/tools/tmp/review-api >"$OUT/restore-07-api.log" 2>&1 &
API_PID=$!
sleep 2
bash "$OUT/../api-workflow.sh" http://localhost:18084 >"$OUT/restore-08-api-workflow.log" 2>&1
log "restore: api workflow exit=$? (log: $OUT/restore-08-api-workflow.log)"
SUFFIX=$(date +%s%N)
TOKEN_NEW=$(curl -s -X POST http://localhost:18084/auth/register -H 'Content-Type: application/json' -d "{\"email\":\"baru-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" >/dev/null; curl -s -X POST http://localhost:18084/auth/login -H 'Content-Type: application/json' -d "{\"email\":\"baru-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
CODE_OLD_WS=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18084/workspaces/22222222-2222-2222-2222-222222222222 -H "Authorization: Bearer $TOKEN_NEW")
log "restore: new user GET old workspace Toko Lama -> HTTP $CODE_OLD_WS (want 403)"
if [ "$CODE_OLD_WS" != "403" ]; then log "REGRESSION FAIL: new user can read old workspace"; FAILS=$((FAILS + 1)); fi
CODE_READY=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18084/readyz)
log "restore: readyz on restored DB -> HTTP $CODE_READY (want 200)"
kill $API_PID 2>/dev/null; wait $API_PID 2>/dev/null
run_step "restore: stop legacy cluster" "$OUT/restore-09-stop.log" \
  sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$LEGACY_DIR" stop -m fast
if [ "$FAILS" != "0" ]; then log "PART 3: $FAILS REGRESSION FAILURE(S)"; exit 1; fi
log "== PART 3 done (0 regression failures) =="
