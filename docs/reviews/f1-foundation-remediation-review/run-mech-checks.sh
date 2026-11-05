#!/usr/bin/env bash
# F1-FOUNDATION-REMEDIATION-REVIEW — mech checks: PostgreSQL rules the
# anomaly/recovery findings depend on. Each test prints its exit code.
# T1: bootstrap superuser demotes a non-bootstrap superuser (expect 0).
# T2: non-bootstrap superuser demotes the bootstrap superuser (expect 1).
# T3: REASSIGN/DROP of the bootstrap superuser (expect 1, both).
# M1: ALTER FUNCTION OWNER TO drops ACL entries granted by the old owner.
# M2: CREATE OR REPLACE FUNCTION preserves the ACL.
# Synthetic roles only, isolated scratch cluster on port 55435.
set -u
OUT=~/workspace/review/F1-FOUNDATION-REMEDIATION-REVIEW/evidence
mkdir -p "$OUT"
export PATH=/usr/lib/postgresql/16/bin:$PATH
MECH_DIR=/var/lib/postgresql/review-mech-cluster
log() { echo "[$(date '+%H:%M:%S')] $*" | tee -a "$OUT/mech.log"; }
echo "=== MECH CHECKS — $(date -Iseconds) ===" > "$OUT/mech.log"
sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$MECH_DIR" stop -m fast >"$OUT/mech-00-stop.log" 2>&1 || true
sudo -u postgres rm -rf "$MECH_DIR"
sudo -u postgres /usr/lib/postgresql/16/bin/initdb -D "$MECH_DIR" -U alice --auth=trust >"$OUT/mech-01-initdb.log" 2>&1
log "mech: initdb -U alice exit=$?"
sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$MECH_DIR" -o "-p 55435 -c listen_addresses='localhost'" -l "$MECH_DIR/server.log" start >"$OUT/mech-02-start.log" 2>&1
log "mech: start exit=$?"
psql -h localhost -p 55435 -U alice -d postgres -v ON_ERROR_STOP=1 \
  -c "CREATE ROLE bob SUPERUSER LOGIN PASSWORD 'x';" \
  -c "CREATE TABLE t_keep(id int PRIMARY KEY);" >"$OUT/mech-03-setup.log" 2>&1
log "mech: setup exit=$?"
psql -h localhost -p 55435 -U alice -d postgres -c "ALTER ROLE bob NOSUPERUSER NOBYPASSRLS;" >"$OUT/mech-04-t1.log" 2>&1
log "mech T1: bootstrap demotes non-bootstrap exit=$? (want 0)"
psql -h localhost -p 55435 -U alice -d postgres -c "ALTER ROLE bob SUPERUSER BYPASSRLS;" >/dev/null 2>&1
psql -h localhost -p 55435 -U bob -d postgres -c "ALTER ROLE alice NOSUPERUSER NOBYPASSRLS;" >"$OUT/mech-05-t2.log" 2>&1
log "mech T2: non-bootstrap demotes bootstrap exit=$? (want 1)"
cat "$OUT/mech-05-t2.log" | tee -a "$OUT/mech.log"
psql -h localhost -p 55435 -U bob -d postgres -v ON_ERROR_STOP=1 -c "REASSIGN OWNED BY alice TO bob;" -c "DROP OWNED BY alice;" >"$OUT/mech-06-t3a.log" 2>&1
log "mech T3a: REASSIGN/DROP OWNED bootstrap exit=$? (want 1)"
psql -h localhost -p 55435 -U bob -d postgres -c "DROP ROLE alice;" >"$OUT/mech-07-t3b.log" 2>&1
log "mech T3b: DROP ROLE bootstrap exit=$? (want 1)"
cat "$OUT/mech-06-t3a.log" "$OUT/mech-07-t3b.log" | tee -a "$OUT/mech.log"
# M1/M2 need a scratch DB owned by a non-superuser role; use main cluster.
sudo -u postgres psql -v ON_ERROR_STOP=0 -c "DROP DATABASE IF EXISTS review_mech;" -c "CREATE DATABASE review_mech OWNER umnia_business;" >"$OUT/mech-08-db.log" 2>&1
log "mech: scratch DB review_mech exit=$?"
PGPASSWORD=review-runtime-only psql -h localhost -U umnia_business -d review_mech -v ON_ERROR_STOP=1 \
  -c "CREATE SCHEMA app;" \
  -c "CREATE FUNCTION app.f1(INT) RETURNS INT LANGUAGE sql AS \$\$ SELECT \$1 \$\$;" \
  -c "REVOKE EXECUTE ON FUNCTION app.f1(INT) FROM PUBLIC;" \
  -c "GRANT EXECUTE ON FUNCTION app.f1(INT) TO umnia_business;" >"$OUT/mech-09-m1-setup.log" 2>&1
log "mech M1 setup exit=$?"
sudo -u postgres psql -d review_mech -tAc "SELECT proacl FROM pg_proc WHERE proname='f1';" | tee -a "$OUT/mech.log"
PGPASSWORD=review-bootstrap-only psql -h localhost -U postgres -d review_mech -v ON_ERROR_STOP=1 \
  -c "ALTER FUNCTION app.f1(INT) OWNER TO postgres;" >"$OUT/mech-10-m1.log" 2>&1
log "mech M1: OWNER TO postgres exit=$?"
sudo -u postgres psql -d review_mech -tAc "SELECT proacl FROM pg_proc WHERE proname='f1';" | tee -a "$OUT/mech.log"
PGPASSWORD=review-runtime-only psql -h localhost -U umnia_business -d review_mech -v ON_ERROR_STOP=1 \
  -c "CREATE FUNCTION app.f2(INT) RETURNS INT LANGUAGE sql AS \$\$ SELECT \$1 \$\$;" \
  -c "REVOKE EXECUTE ON FUNCTION app.f2(INT) FROM PUBLIC;" \
  -c "GRANT EXECUTE ON FUNCTION app.f2(INT) TO umnia_business;" >"$OUT/mech-11-m2-setup.log" 2>&1
log "mech M2 setup exit=$?"
PGPASSWORD=review-bootstrap-only psql -h localhost -U postgres -d review_mech -v ON_ERROR_STOP=1 \
  -c "CREATE OR REPLACE FUNCTION app.f2(INT) RETURNS INT LANGUAGE sql AS \$\$ SELECT \$1 + 1 \$\$;" >"$OUT/mech-12-m2.log" 2>&1
log "mech M2: CREATE OR REPLACE by postgres exit=$?"
sudo -u postgres psql -d review_mech -tAc "SELECT proname, pg_get_userbyid(proowner), proacl FROM pg_proc WHERE proname='f2';" | tee -a "$OUT/mech.log"
sudo -u postgres /usr/lib/postgresql/16/bin/pg_ctl -D "$MECH_DIR" stop -m fast >"$OUT/mech-13-stop.log" 2>&1
log "mech: stop exit=$?"
log "== MECH done =="
