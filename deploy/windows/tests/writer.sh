#!/usr/bin/env bash
# writer.sh — controlled writer for the upgrade-window test (MF3).
# Registers one user on the OLD api, then POSTs a new workspace every
# ~0.5s, logging "<epoch-ms> <http-code> <workspace-name>" per attempt.
# A 201 means the write was ACKNOWLEDGED (must be preserved or blocked
# before the dump snapshot); any other code means it was NOT acknowledged.
# Usage: writer.sh <api-base> <logfile> <email-prefix> [interval-seconds]
set -u
BASE="${1:?api base}"; LOG="${2:?log}"; PREFIX="${3:-writer}"; INTERVAL="${4:-0.5}"
: > "$LOG"
TOKEN=$(curl -s -X POST "$BASE/auth/register" -H 'Content-Type: application/json' -d "{\"email\":\"$PREFIX@contoh.id\",\"password\":\"WriterPass123\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
if [ -z "$TOKEN" ]; then
  TOKEN=$(curl -s -X POST "$BASE/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$PREFIX@contoh.id\",\"password\":\"WriterPass123\"}" | python3 -c "import sys,json; print(json.load(sys.stdin).get('token',''))" 2>/dev/null)
fi
[ -n "$TOKEN" ] || { echo "writer: no token" >> "$LOG"; exit 1; }
echo "writer: token acquired" >> "$LOG"
n=0
while true; do
  n=$((n+1))
  name="Writer-$n"
  code=$(curl -s -o /dev/null -w '%{http_code}' --max-time 3 -X POST "$BASE/workspaces" -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' -d "{\"name\":\"$name\"}" 2>/dev/null)
  code="${code:-000}"
  echo "$(date +%s%3N) $code $name" >> "$LOG"
  sleep "$INTERVAL"
done
