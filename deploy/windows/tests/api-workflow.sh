#!/usr/bin/env bash
# api-workflow.sh <base-url> — minimal live API workflow on a migrated DB.
# Prints one line per step: STEP <name> <http-code>. Fails loudly (exit 1)
# on any unexpected code. Uses synthetic fixture emails only.
set -u
BASE="${1:?base url}"
SUFFIX=$(date +%s%N)
jqget() { python3 -c "import sys,json; print(json.load(sys.stdin).get('$1',''))" 2>/dev/null; }
call() { # method path token json -> prints code + body to stdout, code to stderr line
  local method="$1" path="$2" token="$3" data="$4"
  if [ -n "$token" ]; then
    curl -s -w "\n%{http_code}" -X "$method" "$BASE$path" -H "Authorization: Bearer $token" -H 'Content-Type: application/json' ${data:+ -d "$data"}
  else
    curl -s -w "\n%{http_code}" -X "$method" "$BASE$path" -H 'Content-Type: application/json' ${data:+ -d "$data"}
  fi
}
expect() { # expect <label> <want-code> <got-code>
  echo "STEP $1 want=$2 got=$3"
  [ "$3" = "$2" ] || { echo "FAIL: $1"; exit 1; }
}
R=$(call GET /readyz "" ""); CODE=$(echo "$R" | tail -1); expect readyz 200 "$CODE"
R=$(call POST /auth/register "" "{\"email\":\"owner-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}"); CODE=$(echo "$R" | tail -1); expect register-owner 201 "$CODE"
R=$(call POST /auth/login "" "{\"email\":\"owner-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}"); CODE=$(echo "$R" | tail -1); expect login-owner 200 "$CODE"
TOKEN_A=$(echo "$R" | head -n -1 | jqget token)
[ -n "$TOKEN_A" ] || { echo "FAIL: no owner token"; exit 1; }
R=$(call POST /auth/register "" "{\"email\":\"staff-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}"); CODE=$(echo "$R" | tail -1); expect register-staff 201 "$CODE"
R=$(call POST /auth/login "" "{\"email\":\"staff-$SUFFIX@contoh.id\",\"password\":\"ReviewPass123\"}"); CODE=$(echo "$R" | tail -1); expect login-staff 200 "$CODE"
TOKEN_B=$(echo "$R" | head -n -1 | jqget token)
R=$(call POST /workspaces "$TOKEN_A" "{\"name\":\"Toko Review\"}"); CODE=$(echo "$R" | tail -1); expect create-workspace 201 "$CODE"
WS=$(echo "$R" | head -n -1 | jqget id)
R=$(call GET "/workspaces/$WS" "$TOKEN_B" ""); CODE=$(echo "$R" | tail -1); expect cross-tenant-before-invite 403 "$CODE"
R=$(call POST "/workspaces/$WS/invites" "$TOKEN_A" "{\"email\":\"staff-$SUFFIX@contoh.id\",\"role\":\"cashier\"}"); CODE=$(echo "$R" | tail -1); expect invite-cashier 201 "$CODE"
INV=$(echo "$R" | head -n -1 | jqget token)
R=$(call POST /invites/accept "$TOKEN_B" "{\"workspace_id\":\"$WS\",\"token\":\"$INV\"}"); CODE=$(echo "$R" | tail -1); expect accept-invite 200 "$CODE"
R=$(call GET "/workspaces/$WS" "$TOKEN_B" ""); CODE=$(echo "$R" | tail -1); expect cross-tenant-after-invite 200 "$CODE"
R=$(call GET "/workspaces/$WS/audit" "$TOKEN_B" ""); CODE=$(echo "$R" | tail -1); expect cashier-audit 403 "$CODE"
R=$(call GET "/workspaces/$WS/members" "$TOKEN_B" ""); CODE=$(echo "$R" | tail -1); expect cashier-members 403 "$CODE"
R=$(call GET "/workspaces/$WS/members" "$TOKEN_A" ""); CODE=$(echo "$R" | tail -1); expect owner-members 200 "$CODE"
R=$(call GET /readyz "" ""); CODE=$(echo "$R" | tail -1); expect readyz-after 200 "$CODE"
echo "WORKFLOW OK workspace=$WS"
