#!/usr/bin/env bash
# Harness: run the OFFICIAL postgres:16-bookworm image's docker-entrypoint.sh
# natively (no container runtime — `docker run` is blocked in this VM:
# runc fails "join container mntns: setns: operation not permitted", and
# chown as root is denied, so the entrypoint runs directly as the host
# `postgres` user instead of via the entrypoint's root->gosu re-exec).
# The entrypoint file is extracted from the image itself (amd64 manifest
# digest verified against hub.docker.com; entrypoint byte-identical to
# docker-library/postgres 16/bookworm source), and the postgres binaries
# are the image's own (Debian 16.15-1.pgdg12+2) with the image's libs.
#
# Prerequisites (see tests/README.md): the image rootfs extracted at
# $WINTEST_ROOTFS (default /var/tmp/umnia-harness/rootfs), a host
# `postgres` user with sudo, and a writable scratch dir $WINTEST_BASE.
#
# Usage:
#   run-entrypoint.sh <name> <port> <POSTGRES_USER> <POSTGRES_DB> \
#       <BOOTSTRAP_PW> [RUNTIME_ROLE] [RUNTIME_PW] [EXTRA_ENV=val ...]
# The initdb copy under /var/tmp/umnia-harness/initdb is re-synced from the
# repo before every run and hash-checked, so the EXACT submitted script runs.
# PGUSER/PGDATABASE are deliberately UNSET to prove the initdb script does
# not depend on hidden psql defaults.
# Prints the entrypoint PID + log path; exit 0 only when pg_isready passes.
# Caller stops the server with: kill <pid>; wait <pid> 2>/dev/null
set -euo pipefail
NAME="$1"; PORT="$2"; PUSER="$3"; PDB="$4"; BPW="$5"
RROLE="${6:-}"; RPW="${7:-}"; shift 7 || true
H="${WINTEST_BASE:-/var/tmp/umnia-harness}"
ROOTFS="${WINTEST_ROOTFS:-$H/rootfs}"
REPO="$(cd "$(dirname "$0")/../../.." && pwd)"
PGDATA="$H/pgdata-$NAME"
LOG="$H/entrypoint-$NAME.log"
mkdir -p "$H"; chmod 777 "$H"
# (Re)create the initdb copy with `install -m` so its mode is set at
# creation time: in-place chmod cannot clear other-x on this VM's tmpfs.
# Default mode follows the repo file; INITDB_SOURCED=1 forces 644 (the mode
# a Windows git checkout produces, so the entrypoint SOURCES the script),
# INITDB_EXECUTABLE=1 forces 755 (entrypoint RUNS it).
# INITDB_SRC overrides the initdb source dir (default: repo initdb).
# An empty dir simulates a pre-remediation volume (no initdb script).
INITDB_SRC="${INITDB_SRC:-$REPO/deploy/windows/initdb}"
mkdir -p "$H/initdb"
chmod 777 "$H/initdb"
rm -f "$H/initdb/01-runtime-roles.sh"
if [ "${INITDB_SOURCED:-0}" = "1" ]; then MODE=644;
elif [ "${INITDB_EXECUTABLE:-0}" = "1" ]; then MODE=755;
elif [ -x "$REPO/deploy/windows/initdb/01-runtime-roles.sh" ]; then MODE=755;
else MODE=644; fi
if [ -f "$INITDB_SRC/01-runtime-roles.sh" ]; then
  install -m "$MODE" "$INITDB_SRC/01-runtime-roles.sh" "$H/initdb/01-runtime-roles.sh"
fi
chmod a+rX "$H/initdb"
if [ -f "$INITDB_SRC/01-runtime-roles.sh" ]; then
  diff -q "$INITDB_SRC/01-runtime-roles.sh" "$H/initdb/01-runtime-roles.sh" >/dev/null \
    || { echo "initdb copy MISMATCH vs source" >&2; exit 2; }
fi
rm -rf "$PGDATA"
# Stale socket files from a killed earlier run block the temp server
# ("lock file already exists"); the port is unique per run name.
rm -f /var/run/postgresql/.s.PGSQL.${PORT}*
sudo -u postgres mkdir -p "$PGDATA"
ln -sfn "$H/initdb" /docker-entrypoint-initdb.d
sudo -u postgres env -u PGUSER -u PGDATABASE \
  PGDATA="$PGDATA" PGPORT="$PORT" \
  POSTGRES_USER="$PUSER" POSTGRES_DB="$PDB" POSTGRES_PASSWORD="$BPW" \
  UMNIA_RUNTIME_ROLE="$RROLE" UMNIA_RUNTIME_DB_PASSWORD="$RPW" \
  "$@" \
  PATH="$ROOTFS/usr/lib/postgresql/16/bin:$ROOTFS/usr/local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin" \
  LD_LIBRARY_PATH="$ROOTFS/lib/x86_64-linux-gnu:$ROOTFS/usr/lib/x86_64-linux-gnu" \
  "$ROOTFS/usr/local/bin/docker-entrypoint.sh" postgres -p "$PORT" -c listen_addresses='127.0.0.1' \
  >"$LOG" 2>&1 &
PID=$!
echo "entrypoint pid=$PID log=$LOG pgdata=$PGDATA"
for _ in $(seq 1 90); do
  if LD_LIBRARY_PATH="$ROOTFS/lib/x86_64-linux-gnu:$ROOTFS/usr/lib/x86_64-linux-gnu" \
     "$ROOTFS/usr/lib/postgresql/16/bin/pg_isready" -h 127.0.0.1 -p "$PORT" -d "$PDB" >/dev/null 2>&1; then
    echo "READY port=$PORT pid=$PID"
    exit 0
  fi
  if ! kill -0 "$PID" 2>/dev/null; then
    echo "ENTRYPOINT EXITED EARLY (pid $PID dead); see $LOG"
    exit 1
  fi
  sleep 1
done
echo "TIMEOUT waiting for readiness; see $LOG"
exit 1
