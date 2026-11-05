#!/usr/bin/env bash
# check-deployment.sh — small reusable deployment regression check for
# deploy/windows (initdb, compose, upgrade script).
# Exits 0 only if every APPLICABLE check passes; a check whose runtime is
# absent is reported SKIP (never counted as PASS).
# Usage: deploy/windows/check-deployment.sh [--container-test]
#   --container-test also runs the official-image entrypoint test
#   (needs docker + postgres:16-bookworm image + deploy/windows/tests/).
set -u
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WIN="$ROOT/deploy/windows"
PASS=0; FAIL=0; SKIP=0
ok()   { PASS=$((PASS+1)); echo "PASS: $1"; }
bad()  { FAIL=$((FAIL+1)); echo "FAIL: $1"; }
skip() { SKIP=$((SKIP+1)); echo "SKIP: $1"; }

# 1. initdb script: syntax + explicit connection + fail-closed guards.
if bash -n "$WIN/initdb/01-runtime-roles.sh"; then ok "initdb bash -n"; else bad "initdb bash -n"; fi
if grep -q -- '--username "$POSTGRES_USER" --dbname "$POSTGRES_DB"' "$WIN/initdb/01-runtime-roles.sh"; then
  ok "initdb psql uses explicit --username/--dbname"
else bad "initdb psql uses explicit --username/--dbname"; fi
if grep -q 'psql -tAc "SELECT' "$WIN/initdb/01-runtime-roles.sh"; then
  bad "initdb still uses bare psql -tAc (no variable interpolation in -c)"
else ok "initdb has no bare psql -tAc existence query"; fi
if grep -q 'UMNIA_RUNTIME_ROLE:?' "$WIN/initdb/01-runtime-roles.sh" \
   && grep -q 'UMNIA_RUNTIME_DB_PASSWORD:?' "$WIN/initdb/01-runtime-roles.sh"; then
  ok "initdb fails closed on missing runtime role/secret"
else bad "initdb fails closed on missing runtime role/secret"; fi
if grep -q 'create_runtime_role umnia_business' "$WIN/initdb/01-runtime-roles.sh" \
   || grep -q 'create_runtime_role umnia_admin' "$WIN/initdb/01-runtime-roles.sh"; then
  bad "initdb still creates both roles (segregation)"
else ok "initdb creates only its configured runtime role"; fi

# 2. Compose: parses + per-service secrets segregated.
if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  if docker compose -f "$WIN/compose.yml" config -q 2>/dev/null; then ok "compose config parses"; else bad "compose config parses"; fi
  if docker compose -f "$WIN/compose.yml" config --format json 2>/dev/null | python3 -c "
import json,sys
c=json.load(sys.stdin)
b=c['services']['postgres-business']['environment']
a=c['services']['postgres-admin']['environment']
assert 'UMNIA_ADMIN_DB_PASSWORD' not in b and 'UMNIA_BUSINESS_DB_PASSWORD' not in b
assert 'UMNIA_BUSINESS_DB_PASSWORD' not in a and 'UMNIA_ADMIN_DB_PASSWORD' not in a
assert b['UMNIA_RUNTIME_ROLE']=='umnia_business' and a['UMNIA_RUNTIME_ROLE']=='umnia_admin'
"; then ok "compose secrets segregated per service"; else bad "compose secrets segregated per service"; fi
else skip "compose config (docker compose not available)"; fi

# 3. Upgrade script: parses + contains no unsupported constructs.
# "docker volume rename" may only appear in prose stating it does not exist,
# never as an invoked command (anchored at line start, inside code blocks).
if ! grep -Eq '^\s*docker volume rename\b' "$WIN/Upgrade-UmniaVolume.ps1" "$WIN/README.md"; then
  ok "no 'docker volume rename' command anywhere"
else bad "no 'docker volume rename' command anywhere"; fi
if grep -Eq 'psql .* < |< [A-Za-z]' "$WIN/Upgrade-UmniaVolume.ps1"; then
  bad "upgrade script uses PowerShell '<' redirect"
else ok "upgrade script uses no '<' redirect (docker cp instead)"; fi
if grep -q 'docker cp' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'pg_restore -l' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q '\$LASTEXITCODE' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade script uses docker cp + pg_restore -l validation + exit-code checks"
else bad "upgrade script uses docker cp + pg_restore -l validation + exit-code checks"; fi
if command -v pwsh >/dev/null 2>&1; then
  if UMWIN_PS1="$WIN/Upgrade-UmniaVolume.ps1" pwsh -NoProfile -Command '$e=$null;$t=$null;[System.Management.Automation.Language.Parser]::ParseFile($env:UMWIN_PS1,[ref]$t,[ref]$e)|Out-Null; if($e){$e|ForEach-Object{$_.Message};exit 1}'; then
    ok "upgrade script parses (pwsh)"
  else bad "upgrade script parses (pwsh)"; fi
else skip "upgrade script parses (pwsh not available)"; fi

# 5. Upgrade-safety guarantees (F1-UPGRADE-SAFETY-REVIEW, MF1-MF4).
# MF1: source identity is the legacy bootstrap account by default.
if grep -q '\[string\]\$SourceDbUser = "umnia_business"' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'current_user' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: source identity defaults to legacy umnia_business + verified by SQL"
else bad "upgrade: source identity defaults to legacy umnia_business + verified by SQL"; fi
# MF1: README manual path dumps with the legacy source identity, not bootstrap.
if grep -q 'pg_dump -U umnia_business' "$WIN/README.md" \
   && ! grep -q -- '-p umnia-legacy exec -T postgres-business pg_dump -U umnia_bootstrap' "$WIN/README.md"; then
  ok "README manual dump uses the legacy source identity"
else bad "README manual dump uses the legacy source identity"; fi
# MF2: preflight rejects equal projects and occupied targets before any mutation.
if grep -q 'Normalize-ProjectName' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'sama sesudah normalisasi' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'Target tidak fresh' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: preflight rejects equal projects + occupied target"
else bad "upgrade: preflight rejects equal projects + occupied target"; fi
# MF2: recovery stops the new project first, then restarts the full old stack.
if grep -q -- '-p $NewProject down' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q -- '-p $OldProject up -d' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: recovery stops new project before restarting old stack"
else bad "upgrade: recovery stops new project before restarting old stack"; fi
# MF3: maintenance gate is mandatory before the dump.
if grep -q '\[switch\]\$ConfirmMaintenance' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'if (-not $ConfirmMaintenance)' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q "@('business-api', 'web')" "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q "@('stop', \$svc)" "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: -ConfirmMaintenance gate + writers stopped before dump"
else bad "upgrade: -ConfirmMaintenance gate + writers stopped before dump"; fi
# MF4: upgrade verification is read-only — no mutating HTTP, no test
# accounts/workspaces in user data (the old UpgradePass123 probe is gone).
if ! grep -q 'Invoke-RestMethod' "$WIN/Upgrade-UmniaVolume.ps1" \
   && ! grep -q 'UpgradePass123' "$WIN/Upgrade-UmniaVolume.ps1" \
   && ! grep -q -- '-Method Post' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: verification is read-only (no mutating HTTP, no test accounts)"
else bad "upgrade: verification is read-only (no mutating HTTP, no test accounts)"; fi
# Upgrade regression harness is present in the repo.
if [ -f "$WIN/tests/mock-docker-faithful.ps1" ] \
   && [ -f "$WIN/tests/test-upgrade-scenarios.ps1" ] \
   && [ -f "$WIN/tests/manage-test-env.sh" ]; then
  ok "upgrade regression harness present (faithful mock + scenarios)"
else bad "upgrade regression harness present (faithful mock + scenarios)"; fi

# 6. Final guards (F1-UPGRADE-FINAL-GUARDS).
# MF1: legacy compose file is mandatory and routed via a shared helper:
# OldProject ops use -LegacyComposeFile, NewProject ops use -ComposeFile.
if grep -q 'Mandatory.*\$LegacyComposeFile' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'function Get-ComposeFile' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'return \$LegacyComposeFile' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'return \$ComposeFile' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: legacy file mandatory + routed via Get-ComposeFile"
else bad "upgrade: legacy file mandatory + routed via Get-ComposeFile"; fi
# MF1: legacy file must exist and be valid (config -q) before any mutation;
# no silent fallback to the new compose file remains.
if grep -q 'Test-Path \$LegacyComposeFile' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q "config', '-q'" "$WIN/Upgrade-UmniaVolume.ps1" \
   && ! grep -q 'LegacyComposeFile tidak diisi' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: legacy file precondition (exists + config -q), no fallback"
else bad "upgrade: legacy file precondition (exists + config -q), no fallback"; fi
# MF1: recovery commands quote the file paths (safe when paths contain spaces).
if grep -q -- '-f "$ComposeFile"' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q -- '-f "$LegacyComposeFile"' "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: recovery commands quote compose file paths"
else bad "upgrade: recovery commands quote compose file paths"; fi
# MF2: failed inspection is NOT swallowed into "not running"; optional
# writers are distinguished by an explicit config --services check.
if ! grep -q "catch { return '' }" "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q 'function Test-ServiceDefined' "$WIN/Upgrade-UmniaVolume.ps1" \
   && grep -q "config', '--services'" "$WIN/Upgrade-UmniaVolume.ps1"; then
  ok "upgrade: inspection fails closed + optional writers via config --services"
else bad "upgrade: inspection fails closed + optional writers via config --services"; fi
# Final-guards regression harness: eb25a8f baseline + new scenarios.
if [ -f "$WIN/tests/baseline-eb25a8f-Upgrade-UmniaVolume.ps1" ] \
   && grep -q 'fixed-stopped-source' "$WIN/tests/test-upgrade-scenarios.ps1" \
   && grep -q 'fixed-inspection-failure' "$WIN/tests/test-upgrade-scenarios.ps1" \
   && grep -q 'fixed-missing-writer' "$WIN/tests/test-upgrade-scenarios.ps1" \
   && grep -q 'stop-legacy' "$WIN/tests/manage-test-env.sh"; then
  ok "upgrade final-guards harness present (eb25a8f baseline + new scenarios)"
else bad "upgrade final-guards harness present (eb25a8f baseline + new scenarios)"; fi

# 4. Optional container test (official image entrypoint).
if [ "${1:-}" = "--container-test" ]; then
  if [ -x "$WIN/tests/run-entrypoint.sh" ] \
     && docker image inspect postgres:16-bookworm >/dev/null 2>&1; then
    if "$WIN/tests/run-entrypoint.sh" check-business 55432 umnia_bootstrap umnia_business test-bootstrap-only umnia_business test-runtime-only >/tmp/check-entrypoint.log 2>&1; then
      ok "entrypoint test: fresh business cluster (image entrypoint)"
    else bad "entrypoint test: fresh business cluster (see /tmp/check-entrypoint.log)"; fi
  else skip "entrypoint test (docker image or tests/run-entrypoint.sh missing)"; fi
fi

echo "--- $PASS pass / $FAIL fail / $SKIP skip ---"
[ "$FAIL" -eq 0 ]
