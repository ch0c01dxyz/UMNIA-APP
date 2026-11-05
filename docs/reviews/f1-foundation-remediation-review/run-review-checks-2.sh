#!/usr/bin/env bash
# F1-FOUNDATION-REMEDIATION-REVIEW — part 2: legacy upgrade (anomaly
# reproduction), OWNER-TO vs ACL diagnostic, controlled migration failure.
# Synthetic fixture passwords only. Never reads the repo .env.
set -u
REPO=~/workspace/UMNIA-APP
OUT=~/workspace/review/F1-FOUNDATION-REMEDIATION-REVIEW/evidence
mkdir -p "$OUT"
export PATH=~/workspace/tools/go/bin:/usr/lib/postgresql/16/bin:$PATH
BOOT_PW=review-bootstrap-only
RUN_PW=review-runtime-only
OTHER_PW=review-other-only
MIG="$REPO/services/business-api/migrations"
FAILS=0
LEGACY_DIR=/var/lib/postgresql/review-legacy
LEGACY_PORT=55434
log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$OUT/run2.log"; }
run_step() {
  local label="$1" outfile="$2"; shift 2
  "$@" >"$outfile" 2>&1
  local rc=$?
  log "$label exit=$rc (output: $outfile)"
  return $rc
}
echo "=== REVIEW RUN 2 part 2 — $(date -Iseconds) ===" > "$OUT/run2.log"
log "go: $(go version)"
log "psql: $(psql --version)"

# ------------------------------------------------------- legacy cluster ---
log "== LEGACY fixture: isolated cluster, superuser umnia_business, NO postgres role =="
sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$LEGACY_DIR" stop -m fast >"$OUT/legacy-00-stop.log" 2>&1 || true
sudo -u postgres rm -rf "$LEGACY_DIR"
run_step "legacy: initdb -U umnia_business" "$OUT/legacy-01-initdb.log" \
  sudo -u postgres /usr/lib/postgresql/16/bin/initdb -D "$LEGACY_DIR" -U umnia_business --auth=trust
run_step "legacy: start cluster port $LEGACY_PORT" "$OUT/legacy-02-start.log" \
  sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$LEGACY_DIR" -o "-p $LEGACY_PORT -c listen_addresses='localhost'" -l "$LEGACY_DIR/server.log" start
run_step "legacy: create DB review_legacy" "$OUT/legacy-03-createdb.log" \
  psql -h localhost -p "$LEGACY_PORT" -U umnia_business -d postgres -v ON_ERROR_STOP=1 \
  -c "CREATE DATABASE review_legacy;"
# L1 — old-style run: NO -1 (as the pre-patch configure script did), as the
# superuser umnia_business, on an instance without a postgres role.
log "== LEGACY L1: 001+002 WITHOUT -1 as superuser umnia_business (expect FAIL at OWNER TO postgres) =="
psql -h localhost -p "$LEGACY_PORT" -U umnia_business -d review_legacy \
  -v ON_ERROR_STOP=1 -f "$MIG/001_init.sql" -f "$MIG/002_identity.sql" >"$OUT/legacy-04-oldrun.log" 2>&1
log "legacy L1: old-style migrate exit=$? (output: $OUT/legacy-04-oldrun.log)"
run_step "legacy: inspect half-applied state" "$OUT/legacy-05-halfstate.log" \
  psql -h localhost -p "$LEGACY_PORT" -U umnia_business -d review_legacy -v ON_ERROR_STOP=1 \
  -c "SELECT version FROM schema_migrations ORDER BY version;" \
  -c "SELECT proname, pg_get_userbyid(proowner) AS owner FROM pg_proc JOIN pg_namespace ON pg_namespace.oid=pg_proc.pronamespace WHERE nspname='app' AND proname='register_subject';" \
  -c "SELECT count(*) AS workspaces_table FROM information_schema.tables WHERE table_name='workspaces';" \
  -c "SELECT grantee FROM information_schema.routine_privileges WHERE routine_schema='app' AND routine_name='register_subject' ORDER BY grantee;"
# Fixture: workspace 'Toko Lama' + membership, inserted as superuser.
run_step "legacy: insert fixture Toko Lama" "$OUT/legacy-06-fixture.log" \
  psql -h localhost -p "$LEGACY_PORT" -U umnia_business -d review_legacy -v ON_ERROR_STOP=1 \
  -c "INSERT INTO identity_subjects (id, email, password_hash) VALUES ('11111111-1111-1111-1111-111111111111','toko-lama@contoh.id','x') RETURNING id;" \
  -c "INSERT INTO workspaces (id, name, owner_subject_id) VALUES ('22222222-2222-2222-2222-222222222222','Toko Lama','11111111-1111-1111-1111-111111111111') RETURNING id;" \
  -c "INSERT INTO workspace_memberships (workspace_id, subject_id, status) VALUES ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111','active');"
# L2 — NEGATIVE CONTROL: in-place demotion of the old bootstrap role.
# The pre-fix README told the operator to ALTER the old superuser; that is
# IMPOSSIBLE in PostgreSQL (bootstrap superuser cannot be demoted, dropped
# or reassigned — proven again here, both as self and as another superuser).
# Expect BOTH attempts to fail; the supported path is dump/restore (part 3).
log "== LEGACY L2-neg: in-place demotion attempts (both expect FAIL) =="
run_step "legacy: self-demote as umnia_business" "$OUT/legacy-07a-neg.log" \
  psql -h localhost -p "$LEGACY_PORT" -U umnia_business -d review_legacy -v ON_ERROR_STOP=1 \
  -c "CREATE ROLE umnia_bootstrap SUPERUSER LOGIN PASSWORD '$BOOT_PW';" \
  -c "CREATE ROLE postgres SUPERUSER LOGIN PASSWORD '$BOOT_PW';" \
  -c "CREATE ROLE review_other NOLOGIN PASSWORD '$OTHER_PW';" \
  -c "ALTER ROLE umnia_business NOSUPERUSER NOBYPASSRLS;"
PGPASSWORD="$BOOT_PW" psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy \
  -v ON_ERROR_STOP=1 -c "ALTER ROLE umnia_business NOSUPERUSER NOBYPASSRLS;" >"$OUT/legacy-07b-neg.log" 2>&1
log "legacy: demote as new superuser postgres exit=$? (want non-zero; output: $OUT/legacy-07b-neg.log)"
# L3 — THE anomaly reproduction: submitted migrate sequence, ONCE, no pipe.
log "== LEGACY L3: submitted migrate ONCE as postgres bootstrap (psql -1, no pipe) =="
PGPASSWORD="$BOOT_PW" psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy \
  -v ON_ERROR_STOP=1 -1 \
  -f "$MIG/001_init.sql" -f "$MIG/002_identity.sql" >"$OUT/legacy-08-migrate-once.log" 2>&1
log "legacy L3: migrate-once exit=$? (output: $OUT/legacy-08-migrate-once.log)"
# L4 — immediate inspection (same canonical inspect.sql via stdin).
run_step "legacy: inspect after single migrate" "$OUT/legacy-09-inspect.log" \
  psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy -v ON_ERROR_STOP=1 < "$OUT/../inspect.sql"
# L4b — REGRESSION CHECK (anomaly): after ONE migrate re-run on the
# half-applied DB, all 6 helpers must grant EXECUTE to umnia_business.
# Before the 002 ordering fix this was 0 (only postgres) — see REVIEW.md.
GRANT_N=$(PGPASSWORD="$BOOT_PW" psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy -tAc \
  "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema='app' AND grantee='umnia_business' AND privilege_type='EXECUTE';")
log "legacy L4b: helpers granting EXECUTE to umnia_business: $GRANT_N (want 6)"
if [ "$GRANT_N" != "6" ]; then log "REGRESSION FAIL: EXECUTE grants lost on migrate re-run"; FAILS=$((FAILS + 1)); fi
ROLE_ATTRS=$(PGPASSWORD="$BOOT_PW" psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy -tAc \
  "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname='umnia_business';")
# OBSERVED (not a failure of this fixture): the role is STILL superuser here
# because in-place demotion is impossible (L2-neg). The demoted runtime role
# is proven only in part 3's fresh DB, after dump/restore.
log "legacy L4b: umnia_business super/bypass = $ROLE_ATTRS (expected true/true in this fixture: demotion impossible in place)"
run_step "legacy: fixture preserved?" "$OUT/legacy-10-fixture-check.log" \
  psql -h localhost -p "$LEGACY_PORT" -U postgres -d review_legacy -v ON_ERROR_STOP=1 \
  -c "SELECT id, name FROM workspaces WHERE id='22222222-2222-2222-2222-222222222222';" \
  -c "SELECT count(*) AS memberships FROM workspace_memberships WHERE workspace_id='22222222-2222-2222-2222-222222222222';"
# L5 — PROBE (not a pass/fail workflow): the API must NOT serve this old
# volume as-is, because the runtime role is still superuser and bypasses RLS.
# Expect: readyz 200 (migrations fine) BUT cross-tenant read 200 (bypass).
log "== LEGACY L5: probe — old volume must NOT serve API (superuser bypass) =="
BUSINESS_DATABASE_URL="postgres://umnia_business:$RUN_PW@localhost:$LEGACY_PORT/review_legacy?sslmode=disable" \
  HTTP_LISTEN_ADDR=":18082" ~/workspace/tools/tmp/review-api >"$OUT/legacy-11-api.log" 2>&1 &
API_PID=$!
sleep 2
SUFFIX=$(date +%s%N)
curl -s -X POST http://localhost:18082/auth/register -H 'Content-Type: application/json' -d "{\"email\":\"owner-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" >/dev/null
TOKEN_A=$(curl -s -X POST http://localhost:18082/auth/login -H 'Content-Type: application/json' -d "{\"email\":\"owner-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
curl -s -X POST http://localhost:18082/auth/register -H 'Content-Type: application/json' -d "{\"email\":\"staff-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" >/dev/null
TOKEN_B=$(curl -s -X POST http://localhost:18082/auth/login -H 'Content-Type: application/json' -d "{\"email\":\"staff-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))")
WS=$(curl -s -X POST http://localhost:18082/workspaces -H "Authorization: Bearer $TOKEN_A" -H 'Content-Type: application/json' -d '{"name":"Toko Probe"}' | python3 -c "import sys,json; print(json.load(sys.stdin).get('id',''))")
CODE_X=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18082/workspaces/$WS -H "Authorization: Bearer $TOKEN_B")
log "legacy L5: non-member GET other workspace -> HTTP $CODE_X (want 200 here: PROVES superuser bypass; old volume unsafe for API)"
CODE_OLD_WS=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18082/workspaces/22222222-2222-2222-2222-222222222222 -H "Authorization: Bearer $TOKEN_B")
log "legacy L5: non-member GET old workspace Toko Lama -> HTTP $CODE_OLD_WS (want 200 here: same bypass proof)"
CODE_READY=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18082/readyz)
log "legacy L5: readyz on upgraded DB -> HTTP $CODE_READY (want 200)"
kill $API_PID 2>/dev/null; wait $API_PID 2>/dev/null
run_step "legacy: stop cluster" "$OUT/legacy-13-stop.log" \
  sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$LEGACY_DIR" stop -m fast
log "== LEGACY done =="

# ------------------------------------------- diagnostic: OWNER TO vs ACL ---
log "== DIAG: does ALTER FUNCTION OWNER TO reset proacl? (review_diag, main cluster) =="
run_step "diag: recreate review_diag" "$OUT/diag-00-setup.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=0 -c "DROP DATABASE IF EXISTS review_diag;" -c "CREATE DATABASE review_diag OWNER umnia_business;"
CUT=$(grep -n "Fungsi helper tetap milik postgres" "$MIG/002_identity.sql" | cut -d: -f1)
CUT2=$(grep -n "Pengerasan SECURITY DEFINER" "$MIG/002_identity.sql" | cut -d: -f1)
log "diag: OWNER TO block at 002 lines $CUT..$((CUT2 - 1)); GRANT block from line $CUT2"
head -n $((CUT - 1)) "$MIG/002_identity.sql" > "$OUT/diag-part1.sql"
sed -n "${CUT},$((CUT2 - 1))p" "$MIG/002_identity.sql" > "$OUT/diag-part2.sql"
tail -n +$CUT2 "$MIG/002_identity.sql" > "$OUT/diag-part3.sql"
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_diag \
  -v ON_ERROR_STOP=1 -1 -f "$MIG/001_init.sql" -f "$OUT/diag-part1.sql" >"$OUT/diag-01-part1.log" 2>&1
log "diag: 001 + 002-up-to-OWNER-TO exit=$? (output: $OUT/diag-01-part1.log)"
run_step "diag: proacl BEFORE owner transfer" "$OUT/diag-02-proacl-before.log" \
  sudo -u postgres psql -d review_diag -v ON_ERROR_STOP=1 \
  -c "SELECT proname, pg_get_userbyid(proowner) AS owner, proacl FROM pg_proc WHERE proname='register_subject';"
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_diag \
  -v ON_ERROR_STOP=1 -1 -f "$OUT/diag-part2.sql" >"$OUT/diag-03-part2.log" 2>&1
log "diag: OWNER TO block exit=$? (output: $OUT/diag-03-part2.log)"
run_step "diag: proacl AFTER owner transfer (before GRANT block)" "$OUT/diag-04-proacl-after.log" \
  sudo -u postgres psql -d review_diag -v ON_ERROR_STOP=1 \
  -c "SELECT proname, pg_get_userbyid(proowner) AS owner, proacl FROM pg_proc WHERE proname='register_subject';"
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_diag \
  -v ON_ERROR_STOP=1 -1 -f "$OUT/diag-part3.sql" >"$OUT/diag-05-part3.log" 2>&1
log "diag: GRANT/search_path block exit=$? (output: $OUT/diag-05-part3.log)"
run_step "diag: proacl AFTER grant block" "$OUT/diag-06-proacl-final.log" \
  sudo -u postgres psql -d review_diag -v ON_ERROR_STOP=1 \
  -c "SELECT proname, pg_get_userbyid(proowner) AS owner, proacl FROM pg_proc WHERE proname='register_subject';"
log "== DIAG done =="

# ---------------------------------------- controlled migration failure ---
log "== FAIL-INJECTION: bad 002 with -1 must roll back and stay unrecorded =="
run_step "fail: recreate review_fail + review_fail_noflag" "$OUT/fail-00-setup.log" \
  sudo -u postgres psql -v ON_ERROR_STOP=0 \
  -c "DROP DATABASE IF EXISTS review_fail;" -c "DROP DATABASE IF EXISTS review_fail_noflag;" \
  -c "CREATE DATABASE review_fail OWNER umnia_business;" -c "CREATE DATABASE review_fail_noflag OWNER umnia_business;"
python3 - "$MIG/002_identity.sql" "$OUT/fail-bad002.sql" <<'PY'
import sys
src, dst = sys.argv[1], sys.argv[2]
lines = open(src).read().splitlines(keepends=True)
# The 002 INSERT is a TWO-line statement (VALUES ... / ON CONFLICT ...);
# inject only after the statement completes, else the INSERT itself breaks.
out = []
for i, ln in enumerate(lines):
    out.append(ln)
    if ln.startswith("INSERT INTO schema_migrations (version) VALUES ('002_identity')"):
        out.append(lines[i + 1])  # the ON CONFLICT line
        out.append("CREATE TABLE __fail_injected (id int PRIMARY KEY, id int);\n")
        lines[i + 1] = ""  # already emitted; do not duplicate
open(dst, "w").write("".join(out))
print("injected bad statement right after completed schema_migrations INSERT")
PY
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_fail \
  -v ON_ERROR_STOP=1 -1 -f "$MIG/001_init.sql" -f "$OUT/fail-bad002.sql" >"$OUT/fail-01-migrate.log" 2>&1
log "fail: migrate bad002 WITH -1 exit=$? (want non-zero; output: $OUT/fail-01-migrate.log)"
run_step "fail: verify rollback (whole -1 tx gone: no schema_migrations, no tables)" "$OUT/fail-02-verify.log" \
  sudo -u postgres psql -d review_fail -v ON_ERROR_STOP=1 \
  -c "SELECT count(*) AS schema_migrations_table FROM information_schema.tables WHERE table_name='schema_migrations';" \
  -c "SELECT count(*) AS workspaces_table FROM information_schema.tables WHERE table_name='workspaces';" \
  -c "SELECT count(*) AS app_functions FROM pg_proc JOIN pg_namespace ON pg_namespace.oid=pg_proc.pronamespace WHERE nspname='app';"
log "== FAIL-INJECTION control: same bad file WITHOUT -1 (old behaviour) =="
PGPASSWORD="$BOOT_PW" psql -h localhost -U postgres -d review_fail_noflag \
  -v ON_ERROR_STOP=1 -f "$MIG/001_init.sql" -f "$OUT/fail-bad002.sql" >"$OUT/fail-03-noflag.log" 2>&1
log "fail-control: migrate bad002 WITHOUT -1 exit=$? (want non-zero; output: $OUT/fail-03-noflag.log)"
run_step "fail-control: verify half-applied state" "$OUT/fail-04-verify-noflag.log" \
  sudo -u postgres psql -d review_fail_noflag -v ON_ERROR_STOP=1 \
  -c "SELECT version FROM schema_migrations ORDER BY version;" \
  -c "SELECT count(*) AS workspaces_table FROM information_schema.tables WHERE table_name='workspaces';"
log "== FAIL-INJECTION readyz on failed DB must NOT be 200 =="
BUSINESS_DATABASE_URL="postgres://umnia_business:$RUN_PW@localhost:5432/review_fail?sslmode=disable" \
  HTTP_LISTEN_ADDR=":18083" ~/workspace/tools/tmp/review-api >"$OUT/fail-05-api.log" 2>&1 &
API_PID=$!
sleep 2
CODE_FAIL_READY=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:18083/readyz)
log "fail: readyz on review_fail -> HTTP $CODE_FAIL_READY (want non-200)"
kill $API_PID 2>/dev/null; wait $API_PID 2>/dev/null
if [ "$FAILS" != "0" ]; then log "PART 2: $FAILS REGRESSION FAILURE(S)"; exit 1; fi
log "== PART 2 done (0 regression failures) =="
