#!/usr/bin/env bash
# assert-roles.sh <port> <bootstrap-user> <bootstrap-pw> <db> <expected-runtime-role> <absent-role>
# Exits non-zero on any failed assertion. Uses the image's psql over TCP.
set -euo pipefail
PORT="$1"; BUSER="$2"; BPW="$3"; DB="$4"; RROLE="$5"; ABSENT="$6"
H=/var/tmp/umnia-harness
export LD_LIBRARY_PATH=$H/rootfs/lib/x86_64-linux-gnu:$H/rootfs/usr/lib/x86_64-linux-gnu
PSQL="$H/rootfs/usr/lib/postgresql/16/bin/psql"
q() { PGPASSWORD="$BPW" "$PSQL" -h 127.0.0.1 -p "$PORT" -U "$BUSER" -d "$DB" -tAc "$1"; }
echo "--- roles on port $PORT (db $DB) ---"
q "SELECT rolname, rolsuper, rolbypassrls, rolcanlogin FROM pg_roles WHERE rolname IN ('$BUSER','$RROLE','$ABSENT','postgres') ORDER BY rolname;"
[ "$(q "SELECT rolsuper::text||'/'||rolbypassrls::text FROM pg_roles WHERE rolname='$RROLE'")" = "false/false" ] || { echo "FAIL: $RROLE is not NOSUPERUSER/NOBYPASSRLS"; exit 1; }
[ "$(q "SELECT rolcanlogin::text FROM pg_roles WHERE rolname='$RROLE'")" = "true" ] || { echo "FAIL: $RROLE cannot login"; exit 1; }
[ "$(q "SELECT count(*) FROM pg_roles WHERE rolname='$ABSENT'")" = "0" ] || { echo "FAIL: unexpected role $ABSENT present"; exit 1; }
[ "$(q "SELECT rolsuper::text FROM pg_roles WHERE rolname='postgres'")" = "true" ] || { echo "FAIL: postgres role not superuser"; exit 1; }
[ "$(q "SELECT datname FROM pg_database WHERE datname='$DB'")" = "$DB" ] || { echo "FAIL: db $DB missing"; exit 1; }
echo "PASS: $RROLE false/false + login, $ABSENT absent, postgres superuser, db $DB present"
