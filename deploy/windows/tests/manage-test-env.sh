#!/usr/bin/env bash
# manage-test-env.sh — fixture lifecycle for the faithful upgrade tests.
# All clusters are REAL postgres (official image binaries) on fixed ports.
# Passwords are synthetic fixtures only.
# Usage: manage-test-env.sh <command> [args]
#   reset-legacy        ensure legacy cluster (55436) + legacy API (18080) running
#   reset-target        stop new project services, delete target PGDATA (fresh target)
#   reset-fixedsrc      ensure fixed-source cluster (55438) + API (18082) running
#   reset-target-marker init target cluster (55437) with a marker workspace
#   stop-all            stop every test API + cluster (volumes preserved)
#   sql <port> <query>  psql -tAc against a cluster (socket trust)
#   api-pid <port>      print PID of the API on a port (empty if none)
set -u
H=/var/tmp/umnia-harness
R=$H/rootfs
export LD_LIBRARY_PATH=$R/lib/x86_64-linux-gnu:$R/usr/lib/x86_64-linux-gnu
B=$R/usr/lib/postgresql/16/bin
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
API_BIN="${UMNIA_API_BIN:-$(dirname "$0")/bin/umnia-api}"
MIG=$REPO/services/business-api/migrations

pg_running() { $B/pg_isready -h /var/run/postgresql -p "$1" -d umnia_business >/dev/null 2>&1; }
pg_start() { # pg_start <pgdata> <port> <log>
  sudo -u postgres env "LD_LIBRARY_PATH=$LD_LIBRARY_PATH" $B/pg_ctl -D "$1" -l "$H/pgctl-manage-$2.log" -o "-p $2 -c listen_addresses=127.0.0.1" start >/dev/null 2>&1
  for _ in $(seq 1 30); do pg_running "$2" && return 0; sleep 1; done
  echo "pg_start failed port=$2" >&2; return 1
}
pg_stop() { # pg_stop <pgdata> <port>
  [ -d "$1" ] || return 0
  sudo -u postgres env "LD_LIBRARY_PATH=$LD_LIBRARY_PATH" $B/pg_ctl -D "$1" -l /var/tmp/umnia-harness/pgctl-manage-$2.log stop -m fast >/dev/null 2>&1 || true
}
api_start() { # api-start <dbport> <apiport> <user> <pw> <pidfile> <log>
  local dbport="$1" apiport="$2" user="$3" pw="$4" pidfile="$5" log="$6"
  if [ -f "$pidfile" ] && kill -0 "$(cat "$pidfile")" 2>/dev/null; then return 0; fi
  BUSINESS_DATABASE_URL="postgres://$user:$pw@127.0.0.1:$dbport/umnia_business?sslmode=disable" \
  HTTP_LISTEN_ADDR=":$apiport" APP_ENV=development \
  nohup "$API_BIN" >"$log" 2>&1 &
  echo $! > "$pidfile"
  for _ in $(seq 1 30); do
    [ "$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$apiport/readyz")" = "200" ] && return 0
    sleep 1
  done
  echo "api_start failed port=$apiport" >&2; return 1
}
api_stop() { # api-stop <pidfile>
  [ -f "$1" ] || return 0
  kill "$(cat "$1")" 2>/dev/null || true
  rm -f "$1"
}
sql() { $B/psql -h /var/run/postgresql -p "$1" -U umnia_business -d umnia_business -tAc "$2"; }

case "${1:?command}" in
  reset-legacy)
    mkdir -p $H/staging-legacy $H/staging-new $H/staging-fixedsrc
    [ -d $H/pgdata-legacy-old ] || { echo "legacy PGDATA missing — rebuild fixture first" >&2; exit 1; }
    pg_running 55436 || pg_start $H/pgdata-legacy-old 55436 $H/entrypoint-legacy-old.log
    api_start 55436 18080 umnia_business legacy-bootstrap-only $H/api-legacy.pid $H/api-legacy.log
    ;;
  rebuild-legacy)
    # Deterministic reset: stop, wipe PGDATA, re-init original-style
    # (POSTGRES_USER=umnia_business, empty initdb), migrate WITHOUT -1
    # (half-applied, exit 3 at role postgres), insert canonical fixture.
    api_stop $H/api-legacy.pid
    pg_stop $H/pgdata-legacy-old 55436
    rm -rf $H/pgdata-legacy-old
    mkdir -p $H/empty-initdb $H/staging-legacy
    WINTEST_ROOTFS=$R INITDB_SRC=$H/empty-initdb $REPO/deploy/windows/tests/run-entrypoint.sh legacy-old 55436 umnia_business umnia_business legacy-bootstrap-only "" "" >/dev/null || exit 1
    $B/psql -h 127.0.0.1 -p 55436 -U umnia_business -d umnia_business -v ON_ERROR_STOP=1 -f $MIG/001_init.sql -f $MIG/002_identity.sql >/dev/null 2>&1
    # exit 3 expected (role postgres missing); state is half-applied by design
    sql 55436 "INSERT INTO identity_subjects (id,email,password_hash) VALUES ('11111111-1111-1111-1111-111111111111','toko-lama@contoh.id','x') ON CONFLICT (id) DO NOTHING; INSERT INTO workspaces (id,name,owner_subject_id) VALUES ('22222222-2222-2222-2222-222222222222','Toko Lama','11111111-1111-1111-1111-111111111111') ON CONFLICT (id) DO NOTHING; INSERT INTO workspace_memberships (workspace_id,subject_id,status) VALUES ('22222222-2222-2222-2222-222222222222','11111111-1111-1111-1111-111111111111','active') ON CONFLICT DO NOTHING;" >/dev/null || exit 1
    api_start 55436 18080 umnia_business legacy-bootstrap-only $H/api-legacy.pid $H/api-legacy.log
    ;;
  stop-target)
    # Stop new-project services WITHOUT deleting the volume (for the
    # volume-occupancy preflight path: containers stopped, volume exists).
    api_stop $H/api-new.pid
    pg_stop $H/pgdata-upgraded-new 55437
    ;;
  stop-legacy)
    # Stop legacy-project services WITHOUT deleting the volume (for the
    # stopped-source upgrade path: source postgres + writers stopped,
    # data intact).
    api_stop $H/api-legacy.pid
    pg_stop $H/pgdata-legacy-old 55436
    ;;
  reset-target)
    api_stop $H/api-new.pid
    pg_stop $H/pgdata-upgraded-new 55437
    rm -rf $H/pgdata-upgraded-new $H/staging-new
    mkdir -p $H/staging-new
    ;;
  reset-fixedsrc)
    mkdir -p $H/staging-fixedsrc
    if [ ! -d $H/pgdata-fixedsrc ]; then
      WINTEST_ROOTFS=$R INITDB_EXECUTABLE=1 $REPO/deploy/windows/tests/run-entrypoint.sh fixedsrc 55438 umnia_bootstrap umnia_business bootstrap-only umnia_business runtime-only >/dev/null || exit 1
      $B/psql -h /var/run/postgresql -p 55438 -U umnia_bootstrap -d umnia_business -v ON_ERROR_STOP=1 -1 -f $MIG/001_init.sql -f $MIG/002_identity.sql >/dev/null || exit 1
    fi
    pg_running 55438 || pg_start $H/pgdata-fixedsrc 55438 $H/entrypoint-fixedsrc.log
    # Fixture insert is idempotent (ON CONFLICT DO NOTHING) and always runs
    # as the bootstrap superuser (the runtime user is RLS-filtered).
    $B/psql -h /var/run/postgresql -p 55438 -U umnia_bootstrap -d umnia_business -v ON_ERROR_STOP=1 -c "INSERT INTO identity_subjects (id,email,password_hash) VALUES ('33333333-3333-3333-3333-333333333333','fixedsrc@contoh.id','x') ON CONFLICT (id) DO NOTHING; INSERT INTO workspaces (id,name,owner_subject_id) VALUES ('44444444-4444-4444-4444-444444444444','Toko FixedSrc','33333333-3333-3333-3333-333333333333') ON CONFLICT (id) DO NOTHING; INSERT INTO workspace_memberships (workspace_id,subject_id,status) VALUES ('44444444-4444-4444-4444-444444444444','33333333-3333-3333-3333-333333333333','active') ON CONFLICT DO NOTHING;" >/dev/null || exit 1
    api_start 55438 18082 umnia_business runtime-only $H/api-fixedsrc.pid $H/api-fixedsrc.log
    ;;
  reset-target-marker)
    api_stop $H/api-new.pid
    pg_stop $H/pgdata-upgraded-new 55437
    rm -rf $H/pgdata-upgraded-new
    WINTEST_ROOTFS=$R INITDB_EXECUTABLE=1 $REPO/deploy/windows/tests/run-entrypoint.sh upgraded-new 55437 umnia_bootstrap umnia_business bootstrap-only umnia_business runtime-only >/dev/null || exit 1
    $B/psql -h /var/run/postgresql -p 55437 -U umnia_bootstrap -d umnia_business -v ON_ERROR_STOP=1 -1 -f $MIG/001_init.sql -f $MIG/002_identity.sql >/dev/null || exit 1
    $B/psql -h /var/run/postgresql -p 55437 -U umnia_bootstrap -d umnia_business -v ON_ERROR_STOP=1 -c "INSERT INTO identity_subjects (id,email,password_hash) VALUES ('55555555-5555-5555-5555-555555555555','target-marker@contoh.id','x') ON CONFLICT (id) DO NOTHING; INSERT INTO workspaces (id,name,owner_subject_id) VALUES ('66666666-6666-6666-6666-666666666666','Toko Target Lama','55555555-5555-5555-5555-555555555555') ON CONFLICT (id) DO NOTHING; INSERT INTO workspace_memberships (workspace_id,subject_id,status) VALUES ('66666666-6666-6666-6666-666666666666','55555555-5555-5555-5555-555555555555','active') ON CONFLICT DO NOTHING;" >/dev/null || exit 1
    ;;
  stop-all)
    api_stop $H/api-legacy.pid; api_stop $H/api-new.pid; api_stop $H/api-fixedsrc.pid
    pg_stop $H/pgdata-legacy-old 55436
    pg_stop $H/pgdata-upgraded-new 55437
    pg_stop $H/pgdata-fixedsrc 55438
    ;;
  sql) sql "$2" "$3" ;;
  *) echo "unknown command: $1" >&2; exit 2 ;;
esac
