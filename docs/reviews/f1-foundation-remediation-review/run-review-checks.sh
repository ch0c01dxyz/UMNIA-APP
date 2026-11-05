#!/usr/bin/env bash
# F1-FOUNDATION-REMEDIATION-REVIEW — reproducible review run (run 2, 2026-11-05).
# Runs the EXACT submitted sequence (initdb script + migrate service psql
# flags) once per fixture on isolated test databases, captures every psql /
# go test exit code WITHOUT piping psql output (no tail masking), and
# inspects owners / ACLs / search_path / schema_migrations immediately.
# Synthetic fixture passwords only. Never reads the repo .env.
# Usage: bash run-review-checks.sh   (repo must be at ~/workspace/UMNIA-APP,
#   working tree containing the uncommitted remediation patch)
set -u
REPO=~/workspace/UMNIA-APP
OUT=~/workspace/review/F1-FOUNDATION-REMEDIATION-REVIEW/evidence
mkdir -p "$OUT"
export PATH=~/workspace/tools/go/bin:/usr/lib/postgresql/16/bin:$PATH
BOOT_PW=review-bootstrap-only
RUN_PW=review-runtime-only
OTHER_PW=review-other-only
MIG="$REPO/services/business-api/migrations"

log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$OUT/run.log"; }
# run_step <label> <outfile> <cmd...>: runs cmd with stdout+stderr to outfile,
# logs the REAL exit code (no pipe involved).
run_step() {
  local label="$1" outfile="$2"; shift 2
  "$@" >"$outfile" 2>&1
  local rc=$?
  log "$label exit=$rc (output: $outfile)"
  return $rc
}
psql_super() { sudo -u postgres psql -v ON_ERROR_STOP=1 "$@"; }

echo "=== REVIEW RUN 2 — $(date -Iseconds) ===" > "$OUT/run.log"
log "go: $(go version)"
log "psql: $(psql --version)"
log "repo HEAD: $(cd "$REPO" && git rev-parse HEAD) branch: $(cd "$REPO" && git branch --show-current)"

# ---------------------------------------------------------------- fresh ---
log "== FRESH fixture (main cluster 5432, bootstrap=postgres superuser, as CI) =="
run_step "fresh: drop stale test DBs/roles" "$OUT/fresh-00-cleanup.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=0 -c "DROP DATABASE IF EXISTS review_fresh;" -c "DROP DATABASE IF EXISTS review_empty;" -c "DROP DATABASE IF EXISTS review_fail;" -c "DROP DATABASE IF EXISTS review_diag;" -c "DROP DATABASE IF EXISTS umnia_empty;" -c "DROP ROLE IF EXISTS review_other;"
# NOTE: umnia_business may exist from a previous run; drop only if nothing depends on it.
sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname='umnia_business'" | grep -q 1 && \
  run_step "fresh: drop stale umnia_business role" "$OUT/fresh-00b-drop-role.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=0 -c "DROP ROLE IF EXISTS umnia_business;"
# Step 1 — submitted initdb script, exactly as Compose runs it on empty volume:
# entrypoint runs it as $POSTGRES_USER (bootstrap) with the container env.
# Runs as OS user postgres (peer auth on the socket), like the container's
# entrypoint user; bootstrap password set first so TCP logins work too.
run_step "fresh: set postgres bootstrap password" "$OUT/fresh-00c-bootpw.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=1 -c "ALTER ROLE postgres PASSWORD '$BOOT_PW';"
log "== FRESH step 1: initdb/01-runtime-roles.sh as postgres bootstrap =="
sudo -u postgres env PGDATABASE=postgres PGUSER=postgres POSTGRES_PASSWORD="$BOOT_PW" \
  UMNIA_BUSINESS_DB_PASSWORD="$RUN_PW" UMNIA_ADMIN_DB_PASSWORD="$RUN_PW" \
  bash -s < "$REPO/deploy/windows/initdb/01-runtime-roles.sh" >"$OUT/fresh-01-initdb.log" 2>&1
# NOTE: stdin redirect (not `bash <path>`) because the postgres OS user
# cannot read the root-owned workspace on this host — the same permission
# workaround deploy/linux/configure-postgres.sh documents for psql -f.
# Byte-identical script content; the file itself is untouched.
log "fresh: initdb exit=$? (output: $OUT/fresh-01-initdb.log)"
run_step "fresh: create review_fresh + review_empty + umnia_empty + review_other" "$OUT/fresh-02-createdb.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE review_fresh OWNER umnia_business;" \
  -c "CREATE DATABASE review_empty;" \
  -c "CREATE DATABASE umnia_empty;" \
  -c "CREATE ROLE review_other NOLOGIN PASSWORD '$OTHER_PW';"
# Step 2 — submitted migrate service command, ONCE, no pipe, exit code kept.
log "== FRESH step 2: migrate (psql -1 -f 001 -f 002) as postgres, ONCE =="
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_fresh \
  -v ON_ERROR_STOP=1 -1 \
  -f "$MIG/001_init.sql" -f "$MIG/002_identity.sql" >"$OUT/fresh-03-migrate.log" 2>&1
log "fresh: migrate exit=$? (output: $OUT/fresh-03-migrate.log)"
# Step 3 — immediate inspection as runtime role + superuser.
# stdin redirect (not -f): postgres OS user cannot read the root-owned
# workspace on this host; the shell (root) reads inspect.sql and pipes it
# to psql's stdin. Content is the canonical inspect.sql, unchanged.
run_step "fresh: inspect roles/functions/ACLs/search_path" "$OUT/fresh-04-inspect.log" \
  sudo -u postgres psql -d review_fresh -v ON_ERROR_STOP=1 < "$OUT/../inspect.sql"
# REGRESSION CHECK: all 6 helpers must grant EXECUTE to the runtime role.
GRANT_N=$(sudo -u postgres psql -d review_fresh -tAc \
  "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema='app' AND grantee='umnia_business' AND privilege_type='EXECUTE';")
log "fresh: helpers granting EXECUTE to umnia_business: $GRANT_N (want 6)"
if [ "$GRANT_N" != "6" ]; then log "REGRESSION FAIL: EXECUTE grants missing on fresh install"; exit 1; fi
log "== FRESH step 4: go test JSON (runtime role URL) =="
cd "$REPO/services/business-api"
gofmt -l . >"$OUT/fresh-05-gofmt.log" 2>&1; log "fresh: gofmt exit=$? (empty output = clean)"
run_step "fresh: go vet" "$OUT/fresh-06-vet.log" go vet ./...
BUSINESS_DATABASE_URL="postgres://umnia_business:$RUN_PW@localhost:5432/review_fresh?sslmode=disable" \
  go test -count=1 -json ./... >"$OUT/fresh-07-gotest.json" 2>"$OUT/fresh-07-gotest.err"
log "fresh: go test -json exit=$? (json: $OUT/fresh-07-gotest.json)"
python3 - "$OUT/fresh-07-gotest.json" <<'PY' | tee -a "$OUT/run.log"
import json, sys, collections
path = sys.argv[1]
counts = collections.Counter(); tests = {}
for line in open(path):
    try: e = json.loads(line)
    except ValueError: continue
    if e.get("Action") in ("pass", "fail", "skip") and e.get("Test"):
        tests[e["Test"]] = e["Action"]
    if e.get("Action") in ("pass", "fail", "skip") and not e.get("Test"):
        counts["package_" + e["Action"]] += 1
print("go test: tests pass=%d fail=%d skip=%d | packages: %s" % (
    sum(1 for v in tests.values() if v == "pass"),
    sum(1 for v in tests.values() if v == "fail"),
    sum(1 for v in tests.values() if v == "skip"), dict(counts)))
for name, action in sorted(tests.items()):
    if action != "pass": print("  NON-PASS:", name, action)
PY
log "== FRESH step 5: gate negatives (no URL; SKIP in log) =="
go test -count=1 -v ./... >"$OUT/fresh-08-nourl.log" 2>&1
log "fresh: go test -v WITHOUT url exit=$? (expect 0 with SKIP; unit-only mode)"
SKIP_N=$(grep -c SKIP "$OUT/fresh-08-nourl.log" || true)
log "fresh: SKIP lines without URL: $SKIP_N"
if grep -q "SKIP" "$OUT/fresh-08-nourl.log"; then log "fresh: CI anti-skip gate would FAIL on this log (as designed) exit=1"; else log "fresh: no SKIP found (unexpected)"; fi
test -n "${BUSINESS_DATABASE_URL:-}" || log "fresh: gate env check (empty URL) exit=1 as designed"
log "== FRESH step 6: live API workflow (runtime role) =="
cd "$REPO/services/business-api"
go build -o ~/workspace/tools/tmp/review-api . >"$OUT/fresh-09-build.log" 2>&1
log "fresh: go build exit=$?"
BUSINESS_DATABASE_URL="postgres://umnia_business:$RUN_PW@localhost:5432/review_fresh?sslmode=disable" \
  HTTP_LISTEN_ADDR=":18081" ~/workspace/tools/tmp/review-api >"$OUT/fresh-10-api.log" 2>&1 &
API_PID=$!
sleep 2
bash "$OUT/../api-workflow.sh" http://localhost:18081 >"$OUT/fresh-11-api-workflow.log" 2>&1
log "fresh: api workflow exit=$? (log: $OUT/fresh-11-api-workflow.log)"
kill $API_PID 2>/dev/null; wait $API_PID 2>/dev/null
log "== FRESH done =="
