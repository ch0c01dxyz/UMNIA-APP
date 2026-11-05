# Windows Validation Packet — F1-WINDOWS-RUNTIME-ACCEPTANCE (CORRECTED 2026-11-06)

**Status: CORRECTED procedure — static checks done (see §14); execution NOT RUN anywhere.**
**This packet is NOT "ready-to-run": its revision, command syntax, lifecycle and prerequisites have been checked statically (§14), but actual execution must occur on a Windows host with Docker Desktop running.**
**Tested revision (code under test): `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` (detached pin, §1).**
**Packet version: `2026-11-06` (bootstrap helper order, restore-check roles, delivery metadata).**
**Packet delivery revision: the commit that delivers THIS version is recorded in the delivery report (`docs/reviews/f1-windows-runtime-acceptance.md`). To verify you hold the corrected packet (not the `10bc3de` version), confirm this file contains the line `Packet version: 2026-11-06` and run `git log --oneline -3 -- docs/reviews/f1-windows-runtime-acceptance/WINDOWS-VALIDATION-PACKET.md` — the top entry must NOT be `2db7d6d` or `10bc3de`. If it is, STOP: you have an older packet. Never write a future commit hash into this file; record the actual delivery commit in the report, not here.**
**Scope: scenarios 1–8 from the acceptance task. No merge/deploy/user-data migration is authorized by running this packet.**

Run everything in **Windows PowerShell 5.1** at the explicit executable `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`. Never infer PS 5.1 from a tool label: the generic context-mode `powershell` runtime in the preparer's environment is 7.6.9. Always record the executable path and `$PSVersionTable.PSVersion` (reviewer-observed on their host: 5.1.26100.9818; the operator records their own).

## 0. Environment and prerequisites (one table — all must pass before §1)

| # | Prerequisite | Check (record actual output) |
|---|---|---|
| P1 | Windows 10/11 64-bit, virtualization enabled | `Get-ComputerInfo \| Select-Object WindowsProductName, WindowsVersion, OsHardwareAbstractionLayer` — record actual |
| P2 | Windows PowerShell 5.1 at the explicit path | `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe -NoProfile -Command '$PSVersionTable.PSVersion.ToString()'` — record actual (reviewer: 5.1.26100.9818) |
| P3 | Docker Desktop 4.x+ in Linux-containers mode, WSL 2 backend, daemon running | `docker info` — must show a Server section; exit 0. Record `docker --version` and `docker compose version` (actual versions; no specific patch version is required) |
| P4 | Git for Windows | `git --version` — record actual |
| P5 | Ports free on 127.0.0.1: 5432, 5433, 8080, 9000, 3000 | `Get-NetTCPConnection -LocalPort 5432,5433,8080,9000,3000 -State Listen -ErrorAction SilentlyContinue` — expect no output |
| P6 | Internet access to pull `postgres:16-bookworm` and `rustfs/rustfs:1.0.4` (or both images already cached) | `docker image inspect postgres:16-bookworm --format '{{.Id}}'` and the same for `rustfs/rustfs:1.0.4` — record digests, or record the pull output |
| P7 | ~10 GB free disk | `Get-PSDrive C \| Select-Object Free` — record actual |
| P8 | Unique run directory (see §1) | `Test-Path $run` must be `$false` before creation; creation fails if it already exists |

If any prerequisite fails, STOP. The run is BLOCKED; do not substitute another host, another PowerShell version, or a mock.

## 1. Run directory and revision pin (do this first; never touch the user's checkout)

The operator sets one variable, `$run`, and every later path uses it. The run directory name contains the start timestamp; if it already exists the setup fails instead of overwriting artifacts.

```powershell
# --- ENTRY SEQUENCE. Define the helpers FIRST, then the run directory. ---
# Every later block in this packet calls Check/Expect-Fail/Get-Status/
# Confirm-Stack. In a fresh PowerShell session an undefined function is an
# unknown command, and parser success cannot catch it. Paste this whole
# block first, in the SAME session used for all scenarios (see §2).
$ErrorActionPreference = 'Stop'
function Check($Name) {
  # $LASTEXITCODE is set only by native commands. It is $null before any
  # native command has run and is NOT reset by cmdlets (a cmdlet after a
  # failing native command leaves the old code in place). Guard the $null
  # case so Check never throws on a stale or absent code, and call Check
  # immediately after the native command it guards.
  if ($LASTEXITCODE -and ($LASTEXITCODE -ne 0)) { throw "FAILED: $Name (exit $LASTEXITCODE). Stop this scenario; do not run later steps against a leftover stack." }
}
function Expect-Fail($Name) {
  if (-not $LASTEXITCODE -or ($LASTEXITCODE -eq 0)) { throw "FAILED: $Name was expected to fail but exited 0." }
  Write-Host "NEGATIVE OK: $Name failed as expected (exit $LASTEXITCODE)"
}
function Get-Status($Uri) {
  try {
    $r = Invoke-WebRequest -Uri $Uri -UseBasicParsing
    return [int]$r.StatusCode
  } catch {
    if ($_.Exception.Response) { return [int]$_.Exception.Response.StatusCode }
    return 0
  }
}
function Confirm-Stack($File, $Project) {
  docker compose -f $File -p $Project ps; Check "ps $Project"
  docker compose -f $File -p $Project ps --format '{{.Name}} {{.State}}' | Out-File -Encoding ascii "$run\logs\ps-$Project.txt"; Check "ps-format $Project"
}

$run = "C:\umnia-validation\run-$(Get-Date -Format 'yyyyMMdd-HHmmss')"
if (Test-Path $run) { throw "Run directory already exists: $run" }
# New-Item and cd are cmdlets: under $ErrorActionPreference = 'Stop' (set
# above) a failure throws a terminating error on its own. Do NOT put Check
# after a cmdlet — Check reads $LASTEXITCODE, which cmdlets do not set, so
# it would report a stale code from an earlier native command (see §2).
New-Item -ItemType Directory -Force -Path "$run\logs" | Out-Null
New-Item -ItemType Directory -Force -Path "$run\backups" | Out-Null
git clone https://github.com/ch0c01dxyz/UMNIA-APP.git "$run\UMNIA-APP"; Check 'clone repo'
cd "$run\UMNIA-APP"
git checkout --detach 934ad4b8b79ae3cd5dca01121a270fd6f8f955f1; Check 'detach at reviewed revision'
if ((git rev-parse HEAD) -ne '934ad4b8b79ae3cd5dca01121a270fd6f8f955f1') { throw 'HEAD is not the reviewed revision 934ad4b' }
git rev-parse HEAD
```

Rules: the branch `f1-03-identity` is a moving target (it now points past `934ad4b`), so the packet NEVER checks it out. The code under test is the detached `934ad4b` commit only. The packet file itself is read from the delivery commit recorded in the delivery report (see the header of this file for how to verify you hold the corrected version); it is not part of the run checkout. Never run `git checkout`, `git reset`, or any git mutation in the user's own working checkout. Verify the pinned files stay unmodified before each upgrade run (see §6b/§6c/§10): `git -C "$run\UMNIA-APP" diff --quiet -- deploy/windows/compose.yml deploy/windows/Upgrade-UmniaVolume.ps1; Check 'pinned files unmodified'`.

Create `$run\UMNIA-APP\.env` with **synthetic test-only credentials** (never the user's real `.env`):

```env
UMNIA_BUSINESS_BOOTSTRAP_PASSWORD=ValBootstrap123!
UMNIA_BUSINESS_DB_PASSWORD=ValRuntime123!
UMNIA_ADMIN_BOOTSTRAP_PASSWORD=ValAdminBoot123!
UMNIA_ADMIN_DB_PASSWORD=ValAdminRun123!
RUSTFS_ACCESS_KEY=val-access
RUSTFS_SECRET_KEY=val-secret-123
UMNIA_LEGACY_BOOTSTRAP_PASSWORD=ValLegacy123!
```

## 2. Stop-on-error semantics, stack identity, and results recording (read before any scenario)

**One session.** The helpers defined in §1 (`Check`, `Expect-Fail`, `Get-Status`, `Confirm-Stack`) are session state. Run every scenario in the SAME PowerShell 5.1 session started in §1. If a scenario ever needs a new session, re-paste the §1 helper block (from `$ErrorActionPreference = 'Stop'` through the `Confirm-Stack` definition) before any other command; never depend on functions from a previous operator session. The §6b Window B watcher uses only built-in cmdlets, never the packet helpers, so it needs no definition. The writer (§4a) is a separate script file and is self-contained. The API helpers (`Call-Api`, `Expect-Step`, `Get-Json`) are defined in §4b, before their first use in §8.

**Stop-on-error (mandatory).** A native command's non-zero exit stops the current scenario immediately, except for steps that explicitly assert a failure (negatives, §9). A failed startup must never lead to tests against a different leftover stack. Use the helpers in every scenario; do not rely on "expect:" comments.

**Native commands vs cmdlets.** Native commands (`docker`, `git`, `psql`, `pg_restore`, `powershell`) report through `$LASTEXITCODE`; call `Check` immediately after the native command it guards. Cmdlets (`New-Item`, `cd`, `Copy-Item`, `Get-Content`, `Start-Process`, ...) do NOT set `$LASTEXITCODE` and do NOT reset it — a `Check` after a cmdlet would report a stale code from an earlier native command, so the packet never does that. Instead, cmdlet failures are terminating errors under `$ErrorActionPreference = 'Stop'` (set in §1); where the packet must handle a cmdlet failure it says so explicitly (`try`/`catch` in `Get-Status` and in the writer; `-ErrorAction SilentlyContinue` only for the P5 port probe in §0, where "no output" is the expected result). In a pipeline whose first element is a native command (`docker exec ... | Select-String ... | Out-File`), `$LASTEXITCODE` is that native command's code — the trailing cmdlets do not reset it — so `Check` after such a pipeline guards the native command (verified on pwsh 7.5.4; see §14).

**Stack identity.** Before any scenario that uses `localhost:8080`, confirm the responding API is the intended stack: run `Confirm-Stack` for that project AND query one known marker (stated per scenario, e.g. the fixture workspace "Toko Lama" for the legacy stack, zero workspaces for a fresh stack). If the marker does not match, STOP — a leftover stack owns the port.

**Results recording.** Every scenario gets its own transcript in `$run\logs\` (never overwrite another scenario's transcript). The operator maintains `$run\results.md` with one line per scenario: `scenario N: PASS|FAIL|NOT RUN — <transcript file> — <one-line evidence>`. A FAIL line records the failing command and the transcript line number. A scenario that was not attempted is NOT RUN, never PASS.

**Transcripts.** Start each scenario with `Start-Transcript -Path "$run\logs\scenario-N.log"` and end it with `Stop-Transcript`. The transcript is the evidence; the operator does not edit it.

## 3. Legacy Compose file (created by the operator, in the run checkout only — DO NOT commit)

Save as `$run\UMNIA-APP\deploy\windows\compose.legacy.yml`. It reproduces a pre-remediation volume: `POSTGRES_USER=umnia_business` (superuser), **no** `migrate` service, **no** initdb mount. It must define `postgres-business`, `business-api`, and `web` so the upgrade script can start/stop the writers and the recovery path can restart the full old stack. Relative build paths are preserved (`../../services/business-api`, `../../apps/web`).

```yaml
services:
  postgres-business:
    image: postgres:16-bookworm
    ports:
      - "127.0.0.1:5432:5432"
    environment:
      POSTGRES_DB: umnia_business
      POSTGRES_USER: umnia_business
      POSTGRES_PASSWORD: ${UMNIA_LEGACY_BOOTSTRAP_PASSWORD:-val-legacy-only}
    volumes:
      - postgres-business-data:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U umnia_business -d umnia_business"]
      interval: 5s
      timeout: 3s
      retries: 10
  business-api:
    build:
      context: ../../services/business-api
      dockerfile: Dockerfile
    ports:
      - "127.0.0.1:8080:8080"
    environment:
      APP_ENV: development
      HTTP_LISTEN_ADDR: ":8080"
      BUSINESS_DATABASE_URL: postgres://umnia_business:${UMNIA_LEGACY_BOOTSTRAP_PASSWORD:-val-legacy-only}@postgres-business:5432/umnia_business?sslmode=disable
      STORAGE_ENDPOINT: http://rustfs:9000
    depends_on:
      postgres-business:
        condition: service_healthy
    healthcheck:
      test: ["CMD", "curl", "-f", "http://localhost:8080/healthz"]
      interval: 5s
      timeout: 3s
      retries: 10
  web:
    build:
      context: ../../apps/web
      dockerfile: Dockerfile
    ports:
      - "127.0.0.1:3000:3000"
    environment:
      NUXT_PUBLIC_API_BASE: http://localhost:8080
    depends_on:
      business-api:
        condition: service_healthy
volumes:
  postgres-business-data:
```

## 4. Helpers (save in `$run\`, NOT in the repo)

### 4a. Controlled writer — `$run\writer-val.ps1` (PowerShell 5.1 compatible)

The writer logs ISO-8601 local timestamps (comparable with the operator's recorded observation times). It writes its own PID to `writer.pid` next to its log file. It is started hidden (see §6b) and stopped only by the exact PID the run created (see §7).

```powershell
param([string]$ApiBase = 'http://localhost:8080', [string]$LogFile = '', [string]$Prefix = 'writer-val', [string]$Interval = '0.5')
$ErrorActionPreference = 'Stop'
if ([string]::IsNullOrWhiteSpace($LogFile)) { throw 'LogFile is required' }
$pidFile = Join-Path (Split-Path $LogFile -Parent) 'writer.pid'
$PID | Out-File -Encoding ascii $pidFile
$body = @{ email = "$Prefix@contoh.id"; password = 'WriterPass123' } | ConvertTo-Json
try { $r = Invoke-RestMethod -Uri "$ApiBase/auth/register" -Method Post -Body $body -ContentType 'application/json' } catch { $r = $null }
if (-not $r.token) {
  $r = Invoke-RestMethod -Uri "$ApiBase/auth/login" -Method Post -Body $body -ContentType 'application/json'
}
$token = $r.token
if (-not $token) { Add-Content $LogFile 'writer: no token'; exit 1 }
Add-Content $LogFile "$([DateTime]::Now.ToString('o')) START token acquired"
$n = 0
while ($true) {
  $n++
  $name = "Writer-$n"
  $code = '000'
  try {
    $wb = @{ name = $name } | ConvertTo-Json
    $null = Invoke-RestMethod -Uri "$ApiBase/workspaces" -Method Post -Body $wb -ContentType 'application/json' -Headers @{ Authorization = "Bearer $token" }
    $code = '201'
  } catch { $code = '000' }
  Add-Content $LogFile "$([DateTime]::Now.ToString('o')) $code $name"
  Start-Sleep -Seconds $Interval
}
```

### 4b. API call helpers (paste into the PowerShell 5.1 session before the tenant-isolation workflow, §8)

```powershell
function Call-Api($Method, $Path, $Token, $Body) {
  $headers = @{ 'Content-Type' = 'application/json' }
  if ($Token) { $headers['Authorization'] = "Bearer $Token" }
  try {
    $r = Invoke-WebRequest -Uri "http://localhost:8080$Path" -Method $Method -Headers $headers -Body $Body -UseBasicParsing
    return @{ Code = [int]$r.StatusCode; Body = $r.Content }
  } catch {
    $code = 0
    if ($_.Exception.Response) { $code = [int]$_.Exception.Response.StatusCode }
    return @{ Code = $code; Body = '' }
  }
}
function Expect-Step($Name, $Want, $Got) {
  Write-Host "STEP $Name want=$Want got=$Got"
  if ($Got -ne $Want) { throw "FAIL: $Name (want $Want, got $Got)" }
}
function Get-Json($Text, $Field) {
  if ([string]::IsNullOrWhiteSpace($Text)) { return '' }
  return ((ConvertFrom-Json $Text).$Field)
}
```

## 5. Scenario lifecycle and port ownership (the whole run, in order)

Host ports 5432/5433/8080/9000/3000 are owned by exactly one stack at a time. A scenario starts only when the table's "before" state holds (verified with `Confirm-Stack`); a scenario ends in its "after" state. `stop` stops containers without deleting volumes; no scenario deletes any volume (cleanup is §13, approval-required).

| Scenario | Before (must hold) | After | Ports owned after |
|---|---|---|---|
| 1 Fresh startup | No stack running; ports free | `umnia-val-fresh` stopped (volumes preserved) | None |
| 2 Readiness negative | No stack running; ports free | `umnia-val-neg` stopped (volumes preserved) | None |
| 3a Legacy fixture | No stack running; ports free | `umnia-val-legacy` UP (legacy API on 8080) | legacy: 5432, 8080, 3000 |
| 3b Running-source upgrade | `umnia-val-legacy` UP | `umnia-val-legacy` down (volume preserved); `umnia-val-new` UP (target API on 8080) | new: 5432, 5433, 8080, 9000, 3000 |
| 4 Writer accounting | `umnia-val-new` UP (from 3b) | `umnia-val-new` UP (unchanged) | new (unchanged) |
| 5 Target identity + workflow | `umnia-val-new` UP (from 3b) | `umnia-val-new` UP (unchanged) | new (unchanged) |
| — transition — | Stop `umnia-val-new` (`stop`, volumes preserved) so the ports are free for 3c. The target's volume and data are PRESERVED for the occupied-target check (§9b). | — | None |
| 3c Stopped-source upgrade | No stack running; `umnia-val-legacy` volume exists (stopped); ports free | `umnia-val-legacy` down (volume preserved); `umnia-val-new2` UP | new2: 5432, 5433, 8080, 9000, 3000 |
| — transition — | Stop `umnia-val-new2` (`stop`, volumes preserved). | — | None |
| 6 Negatives | No stack running; `umnia-val-new` volume exists (stopped) | No stack running (negatives start nothing) | None |
| 7 Controlled failure | No stack running; ports free | `umnia-val-legacy7` UP (recovered); `umnia-val-new7` down | legacy7: 5432, 8080, 3000 |
| — transition — | Stop `umnia-val-legacy7` (`stop`, volumes preserved). | — | None |
| 8 Restore verification | No stack running | `umnia-val-restore-check` container STOPPED (left for cleanup, §13) | None (container publishes no host port) |

## 6. Scenario 3 — Legacy upgrade through Windows PowerShell 5.1

### 6a. Build the legacy fixture (project `umnia-val-legacy`)

```powershell
Start-Transcript -Path "$run\logs\scenario-3a-fixture.log"
cd "$run\UMNIA-APP"
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy up -d --build; Check 'legacy up'
Start-Sleep -Seconds 60
Confirm-Stack 'deploy/windows/compose.legacy.yml' 'umnia-val-legacy'
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ps --format '{{.Name}} {{.State}}'; Check 'legacy ps-format'
$cid = docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ps -q postgres-business; Check 'legacy cid'
if ([string]::IsNullOrWhiteSpace($cid)) { throw 'FAILED: legacy postgres container id is empty' }
docker cp services/business-api/migrations/001_init.sql ${cid}:/migrations-001_init.sql; Check 'cp 001'
docker cp services/business-api/migrations/002_identity.sql ${cid}:/migrations-002_identity.sql; Check 'cp 002'
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -v ON_ERROR_STOP=1 -f /migrations-001_init.sql -f /migrations-002_identity.sql; Expect-Fail 'migrations without -1 (002 must fail)'
# The output above must contain: role "postgres" does not exist (002 fails mid-file).
# This is the pre-remediation half-applied state. Record the full output.
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT version FROM schema_migrations ORDER BY version;"; Check 'schema_migrations'
# Prior Linux evidence: 001_init + 002_identity recorded (half-applied). RECORD ACTUAL.
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT rolname, rolsuper FROM pg_roles WHERE rolname = 'umnia_business';"; Check 'legacy role'
# Expect: umnia_business|t (bootstrap superuser — the legacy defect). RECORD ACTUAL.
if ((Get-Status 'http://localhost:8080/healthz') -ne 200) { throw 'FAILED: legacy /healthz is not 200' }
if ((Get-Status 'http://localhost:8080/readyz') -ne 200) { throw 'FAILED: legacy /readyz is not 200' }
# Fixture through the API (no invented SQL): register owner, create "Toko Lama".
$ownerBody = @{ email = 'owner-val@contoh.id'; password = 'ValOwner123!' } | ConvertTo-Json
$owner = Invoke-RestMethod -Uri http://localhost:8080/auth/register -Method Post -Body $ownerBody -ContentType 'application/json'
if (-not $owner.id) { throw 'FAILED: fixture register returned no id' }
$ownerLogin = Invoke-RestMethod -Uri http://localhost:8080/auth/login -Method Post -Body $ownerBody -ContentType 'application/json'
if (-not $ownerLogin.token) { throw 'FAILED: fixture login returned no token' }
$wsBody = @{ name = 'Toko Lama' } | ConvertTo-Json
$ws = Invoke-RestMethod -Uri http://localhost:8080/workspaces -Method Post -Body $wsBody -ContentType 'application/json' -Headers @{ Authorization = "Bearer $($ownerLogin.token)" }
if (-not $ws.id) { throw 'FAILED: fixture workspace returned no id' }
Write-Host "fixture workspace id: $($ws.id)"
"fixture workspace id: $($ws.id)" | Out-File -Encoding ascii "$run\logs\fixture-workspace.txt"
Stop-Transcript
```

### 6b. Running-source upgrade (project `umnia-val-new`)

Two PowerShell windows are required. Window A runs the upgrade. Window B watches the transcript for the quiesce-success line and stops the writer (see §7 for why, and for the accounting rules).

Window A:

```powershell
Start-Transcript -Path "$run\logs\scenario-3b-running.log"
cd "$run\UMNIA-APP"
git -C "$run\UMNIA-APP" diff --quiet -- deploy/windows/compose.yml deploy/windows/Upgrade-UmniaVolume.ps1; Check 'pinned files unmodified'
Confirm-Stack 'deploy/windows/compose.legacy.yml' 'umnia-val-legacy'
$writer = Start-Process powershell -WindowStyle Hidden -ArgumentList '-ExecutionPolicy','Bypass','-File',"$run\writer-val.ps1",'-ApiBase','http://localhost:8080','-LogFile',"$run\logs\writer.log" -PassThru
$writer.Id | Out-File -Encoding ascii "$run\logs\writer.pid"
Start-Sleep -Seconds 10
if (-not (Select-String -Path "$run\logs\writer.log" -Pattern 'START token acquired' -Quiet)) { throw 'FAILED: writer did not acquire a token' }
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose.yml -OldProject umnia-val-legacy -NewProject umnia-val-new -BackupDir "$run\backups" -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; Check 'upgrade running-source'
# The transcript must contain "UPGRADE SELESAI." — verify after the run:
if (-not (Select-String -Path "$run\logs\scenario-3b-running.log" -Pattern 'UPGRADE SELESAI' -Quiet)) { throw 'FAILED: transcript does not contain UPGRADE SELESAI' }
# ROUTING ASSERTION (from the transcript): every source line reads
#   "> docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ..."
# and every target line reads
#   "> docker compose -f deploy/windows/compose.yml -p umnia-val-new ..."
# MOUNT/VOLUME ASSERTION:
docker volume ls | Select-String -Pattern 'umnia-val-'
# Expect: umnia-val-legacy_postgres-business-data (old, preserved) AND
#   umnia-val-new_postgres-business-data (new) both exist. RECORD ACTUAL.
$backup6b = Get-ChildItem "$run\backups\*.dump" | Sort-Object LastWriteTime -Descending | Select-Object -First 1
if (-not $backup6b) { throw 'FAILED: no backup file in run backups directory' }
$backup6b.FullName | Out-File -Encoding ascii "$run\logs\backup-6b.txt"
Get-FileHash -Algorithm SHA256 $backup6b.FullName
Stop-Transcript
```

Window B (started before Window A runs the upgrade; closed after it exits):

```powershell
while (-not (Select-String -Path "$run\logs\scenario-3b-running.log" -Pattern 'penulis berhenti: tidak ada sesi aktif' -Quiet)) { Start-Sleep -Seconds 2 }
Get-Date -Format 'o' | Out-File -Encoding ascii "$run\logs\writer-stop.txt"
$writerPid = Get-Content "$run\logs\writer.pid"
Stop-Process -Id $writerPid
```

### 6c. Stopped-source upgrade (project `umnia-val-new2`)

After §6b the legacy stack is down (upgrade step 4) with its volume preserved — that IS the stopped-source state. Before this step, stop the §6b target so the ports are free (see §5 transition). The target's volume and data are preserved for the occupied-target check (§9b).

```powershell
docker compose -f deploy/windows/compose.yml -p umnia-val-new stop; Check 'stop new target before 6c'
```

```powershell
Start-Transcript -Path "$run\logs\scenario-3c-stopped.log"
cd "$run\UMNIA-APP"
git -C "$run\UMNIA-APP" diff --quiet -- deploy/windows/compose.yml deploy/windows/Upgrade-UmniaVolume.ps1; Check 'pinned files unmodified'
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ps --format '{{.Name}} {{.State}}'; Check 'legacy ps (must show stopped)'
# Expect: no running legacy containers; the volume still exists. RECORD ACTUAL.
docker volume inspect umnia-val-legacy_postgres-business-data; Check 'legacy volume exists'
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose.yml -OldProject umnia-val-legacy -NewProject umnia-val-new2 -BackupDir "$run\backups" -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; Check 'upgrade stopped-source'
# The transcript must show the source postgres being started via the LEGACY file:
#   "postgres lama berhenti — start non-destruktif untuk verifikasi identitas"
if (-not (Select-String -Path "$run\logs\scenario-3c-stopped.log" -Pattern 'UPGRADE SELESAI' -Quiet)) { throw 'FAILED: transcript does not contain UPGRADE SELESAI' }
docker volume ls | Select-String -Pattern 'umnia-val-'
# Expect: umnia-val-new2_postgres-business-data exists alongside the preserved legacy volume. RECORD ACTUAL.
Stop-Transcript
# Transition (see §5): stop umnia-val-new2 before the negatives:
docker compose -f deploy/windows/compose.yml -p umnia-val-new2 stop; Check 'stop new2 target'
```

## 7. Scenario 4 — Source maintenance before snapshot (writer accounting, with §6b)

**Accounting rules (read first).** The writer is an EXTERNAL writer: it talks to `localhost:8080`, which points to the source API before cutover and to the target API after cutover. Its session token was issued by the source API and restored to the target, so a writer left running could create workspaces in the TARGET after the dump. Therefore:

1. The writer is stopped by the operator (Window B, §6b) as soon as the upgrade transcript shows the quiesce-success line `penulis berhenti: tidak ada sesi aktif umnia_business`. The writer's log is then bounded to the pre-dump window BY CONSTRUCTION: no successful write can occur after that line, because the script's step 0c already stopped the API containers. Any `201` in the log with a timestamp after the time recorded in `writer-stop.txt` is a FAIL (it would mean the writer wrote to a different stack).
2. A stopped writer does NOT prove server-side quiescence. The quiescence evidence is the SCRIPT's own step-0c output, which the operator records from the transcript: `penulis dihentikan: business-api`, `penulis dihentikan: web` (or `penulis tidak berjalan (dilewati): <svc>` where a writer was not running), and `penulis berhenti: tidak ada sesi aktif umnia_business`. That line is printed only after the script's loop verified zero running writer containers AND zero active sessions for the source user (5 attempts, 2 s apart). Record those three lines verbatim in `results.md`.
3. Only acknowledged pre-snapshot writes count. Count the `201` lines in the writer log; compare with the target's `Writer-*` workspaces (below). Post-cutover traffic is a SEPARATE run with a SEPARATE log file (see §10 recovery proof), never mixed into this count.

```powershell
Start-Transcript -Path "$run\logs\scenario-4.log"
cd "$run\UMNIA-APP"
Confirm-Stack 'deploy/windows/compose.yml' 'umnia-val-new'
# (a) No 201 after the recorded writer-stop time:
$stopTime = [DateTime]::Parse((Get-Content "$run\logs\writer-stop.txt"))
$late = Get-Content "$run\logs\writer.log" | Where-Object { $_ -match ' 201 ' -and ([DateTime]::Parse(($_ -split ' ')[0])) -gt $stopTime }
if ($late) { throw "FAILED: writer 201 after stop time: $($late -join '; ')" }
Write-Host 'writer accounting: no 201 after stop time'
# (b) Acknowledged pre-snapshot writes survived:
$writer201 = (Select-String -Path "$run\logs\writer.log" -Pattern ' 201 ' | Measure-Object).Count
$targetWriter = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name LIKE 'Writer-%';"; Check 'count Writer workspaces in target'
if ($writer201 -ne [int]$targetWriter) { throw "FAILED: writer 201 count ($writer201) != target Writer workspaces ($targetWriter)" }
Write-Host "writer accounting: $writer201 acknowledged writes survived"
# (c) Fixture workspace survived:
$fixture = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"; Check 'fixture workspace in target'
if ($fixture -ne '1') { throw "FAILED: fixture workspace count is $fixture, expected 1" }
# External writers: no other API client may write during the maintenance
# window (from -ConfirmMaintenance until cutover/recovery). Record that
# no other writer ran.
Stop-Transcript
```

## 8. Scenario 5 — Target identity + tenant isolation (on the §6b target)

```powershell
Start-Transcript -Path "$run\logs\scenario-5.log"
cd "$run\UMNIA-APP"
Confirm-Stack 'deploy/windows/compose.yml' 'umnia-val-new'
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname = 'umnia_business';"; Check 'target role f/f query'
# Expect output: false/false. RECORD ACTUAL; a different value is a FAIL (throw below):
$role = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname = 'umnia_business';"; Check 'target role f/f value'
if ($role -ne 'false/false') { throw "FAILED: target role is $role, expected false/false" }
$mig = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM schema_migrations WHERE version = '002_identity';"; Check '002 count'
if ($mig -ne '1') { throw "FAILED: 002_identity count is $mig, expected 1" }
$grants = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema = 'app' AND routine_name IN ('accept_invite', 'has_permission', 'is_member', 'register_subject', 'session_subject', 'subject_by_email') AND grantee IN ('postgres', 'umnia_business');"; Check 'EXECUTE grants count'
if ($grants -ne '12') { throw "FAILED: EXECUTE grants count is $grants, expected 12" }
if ((Get-Status 'http://localhost:8080/readyz') -ne 200) { throw 'FAILED: target /readyz is not 200' }
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT email FROM identity_subjects ORDER BY email;"; Check 'target subjects'
# Expect exactly 2 subjects: owner-val@contoh.id (fixture) and writer-val@contoh.id
# (the writer account). No synthetic accounts may be created by the upgrade
# script. RECORD ACTUAL; any other subject is a FAIL.
docker volume inspect umnia-val-legacy_postgres-business-data; Check 'old volume preserved'
Stop-Transcript
```

### Tenant-isolation workflow (PowerShell 5.1 equivalent of `api-workflow.sh`, 14 steps)

Run against the upgraded target API (`http://localhost:8080`) — the designated synthetic test database. Paste the §4b helpers first. `$sfx` must be unique per run. The workflow's accounts are test-only.

```powershell
Start-Transcript -Path "$run\logs\scenario-5-workflow.log"
$sfx = [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()
$r = Call-Api 'GET' '/readyz' '' ''; Expect-Step 'readyz' 200 $r.Code
$r = Call-Api 'POST' '/auth/register' '' (@{ email = "owner-$sfx@contoh.id"; password = 'ReviewPass123' } | ConvertTo-Json); Expect-Step 'register-owner' 201 $r.Code
$r = Call-Api 'POST' '/auth/login' '' (@{ email = "owner-$sfx@contoh.id"; password = 'ReviewPass123' } | ConvertTo-Json); Expect-Step 'login-owner' 200 $r.Code
$tokenA = Get-Json $r.Body 'token'; if (-not $tokenA) { throw 'FAIL: no owner token' }
$r = Call-Api 'POST' '/auth/register' '' (@{ email = "staff-$sfx@contoh.id"; password = 'ReviewPass123' } | ConvertTo-Json); Expect-Step 'register-staff' 201 $r.Code
$r = Call-Api 'POST' '/auth/login' '' (@{ email = "staff-$sfx@contoh.id"; password = 'ReviewPass123' } | ConvertTo-Json); Expect-Step 'login-staff' 200 $r.Code
$tokenB = Get-Json $r.Body 'token'
$r = Call-Api 'POST' '/workspaces' $tokenA (@{ name = 'Toko Review' } | ConvertTo-Json); Expect-Step 'create-workspace' 201 $r.Code
$ws = Get-Json $r.Body 'id'
$r = Call-Api 'GET' "/workspaces/$ws" $tokenB ''; Expect-Step 'cross-tenant-before-invite' 403 $r.Code
$r = Call-Api 'POST' "/workspaces/$ws/invites" $tokenA (@{ email = "staff-$sfx@contoh.id"; role = 'cashier' } | ConvertTo-Json); Expect-Step 'invite-cashier' 201 $r.Code
$inv = Get-Json $r.Body 'token'
$r = Call-Api 'POST' '/invites/accept' $tokenB (@{ workspace_id = $ws; token = $inv } | ConvertTo-Json); Expect-Step 'accept-invite' 200 $r.Code
$r = Call-Api 'GET' "/workspaces/$ws" $tokenB ''; Expect-Step 'cross-tenant-after-invite' 200 $r.Code
$r = Call-Api 'GET' "/workspaces/$ws/audit" $tokenB ''; Expect-Step 'cashier-audit' 403 $r.Code
$r = Call-Api 'GET' "/workspaces/$ws/members" $tokenB ''; Expect-Step 'cashier-members' 403 $r.Code
$r = Call-Api 'GET' "/workspaces/$ws/members" $tokenA ''; Expect-Step 'owner-members' 200 $r.Code
$r = Call-Api 'GET' '/readyz' '' ''; Expect-Step 'readyz-after' 200 $r.Code
Write-Host "WORKFLOW OK workspace=$ws"
Stop-Transcript
```

## 9. Scenario 6 — Negative cases (no stack running; record exit code and no subsequent mutations each time)

Preconditions: no stack running (see §5); the `umnia-val-new` volume exists (stopped) for the occupied-target check. Before each negative, record the state; after each, verify nothing changed (same `docker ps`, same `docker volume ls` output).

```powershell
Start-Transcript -Path "$run\logs\scenario-6-negatives.log"
cd "$run\UMNIA-APP"
docker ps --format '{{.Names}}'; Check 'ps before negatives'
docker volume ls | Select-String -Pattern 'umnia-val-'
# (a) Equal projects:
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-same -NewProject umnia-val-same -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; Expect-Fail 'equal projects'
# Expect: rejection with ZERO docker calls (transcript shows the equality error only).
# (b) Occupied target (the already-upgraded umnia-val-new from §6b):
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; Expect-Fail 'occupied target'
# Expect: "Target tidak fresh"; target data untouched — verify (target is stopped; start only its postgres for this read, then stop it again):
docker compose -f deploy/windows/compose.yml -p umnia-val-new up -d postgres-business; Check 'start target postgres for read'
Start-Sleep -Seconds 15
$marker = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"; Check 'occupied-target marker read'
if ($marker -ne '1') { throw "FAILED: occupied-target marker is $marker, expected 1" }
docker compose -f deploy/windows/compose.yml -p umnia-val-new stop; Check 'stop target after read'
# (c) Missing original Compose file:
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile C:\does\not\exist.yml -ConfirmMaintenance; Expect-Fail 'missing legacy file'
# Expect: abort before ANY docker call.
# (d) Invalid original Compose file (garbage YAML):
'this is: [not valid yaml' | Out-File -Encoding ascii "$run\bad-compose.yml"
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile "$run\bad-compose.yml" -ConfirmMaintenance; Expect-Fail 'invalid legacy file'
# Expect: config -q failure; no mutating docker calls.
# (e) Inspection failure: legacy file WITHOUT the postgres-business service.
Copy-Item "$run\UMNIA-APP\deploy\windows\compose.legacy.yml" "$run\compose-no-pg.yml"
# Delete the postgres-business service block from the copy: remove lines from
# "  postgres-business:" up to (but not including) "  business-api:".
$lines = Get-Content "$run\compose-no-pg.yml"
$start = ($lines | Select-String -Pattern '^  postgres-business:' | Select-Object -First 1).LineNumber - 1
$end = ($lines | Select-String -Pattern '^  business-api:' | Select-Object -First 1).LineNumber - 1
$lines[0..($start - 1)] + $lines[$end..($lines.Count - 1)] | Set-Content "$run\compose-no-pg.yml"
if (Select-String -Path "$run\compose-no-pg.yml" -Pattern 'postgres-business:' -Quiet) { throw 'FAILED: postgres-business still present in no-pg copy' }
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile "$run\compose-no-pg.yml" -ConfirmMaintenance; Expect-Fail 'inspection failure (no postgres-business service)'
# Expect: abort before dump/restore/source shutdown. No security controls
# were disabled to inject this failure.
docker ps --format '{{.Names}}'; Check 'ps after negatives'
docker volume ls | Select-String -Pattern 'umnia-val-'
# Expect: identical to the "before" output above. Any difference is a FAIL.
Stop-Transcript
```

## 10. Scenario 7 — Controlled failure after source shutdown + printed recovery

**Why the port-listener injection is REJECTED (do not use it).** The previous version of this packet told the operator to bind port 5432 before the upgrade. That cannot work: the source PostgreSQL already owns 5432 until the script's step 4 (`== 4. Hentikan stack lama`) stops it, and the script proceeds to step 5 (`== 5. Jalankan postgres project baru`) in milliseconds — no external agent can reliably interpose a listener in that window. Any "success" of that approach would be a race, not a controlled failure.

**Deterministic mechanism (used here).** The failure is injected by running the upgrade with a COPY of the new Compose file whose `postgres:16-bookworm` image tag is replaced with a tag that does not exist (`postgres:16-bookworm-VALIDATION-BAD-TAG-0000`). Docker fails to pull the image at step 5 — after the source was shut down at step 4 — deterministically, with no timing window. The pinned checkout is never modified (the copy lives in the same `deploy\windows\` directory so relative build paths still resolve; the original file is untouched and verified with `git diff --quiet`). The "injector" is that copy: it is stopped by not using it for any later run, and it is removed only under the cleanup approval (§13). The failure mode this simulates is a real one (target image unavailable after source shutdown); the recovery path exercised is identical.

**Fixture.** Build a FRESH legacy fixture under NEW project names (`umnia-val-legacy7` / `umnia-val-new7`) by repeating the §6a steps with `-p umnia-val-legacy7`. Do NOT delete the §6b/§6c fixtures to make room (no `down -v`; new isolated projects instead).

```powershell
Start-Transcript -Path "$run\logs\scenario-7.log"
cd "$run\UMNIA-APP"
# Fixture umnia-val-legacy7 was built per §6a (same steps, -p umnia-val-legacy7).
# The fixture workspace "Toko Lama" exists there; record its id from $run\logs\fixture-workspace-7.txt.
# Create the bad-tag copy (same directory as the original, so relative build paths resolve):
Copy-Item "$run\UMNIA-APP\deploy\windows\compose.yml" "$run\UMNIA-APP\deploy\windows\compose-badtag.yml"
# Copy-Item is a cmdlet: under $ErrorActionPreference = 'Stop' (set in §1) a
# failure throws on its own. Assert the copy exists instead of Check (which
# would read a stale $LASTEXITCODE — see §2).
if (-not (Test-Path "$run\UMNIA-APP\deploy\windows\compose-badtag.yml")) { throw 'FAILED: bad-tag compose copy missing' }
(Get-Content "$run\UMNIA-APP\deploy\windows\compose.yml") -replace 'image: postgres:16-bookworm', 'image: postgres:16-bookworm-VALIDATION-BAD-TAG-0000' | Set-Content "$run\UMNIA-APP\deploy\windows\compose-badtag.yml"
if ((Select-String -Path "$run\UMNIA-APP\deploy\windows\compose-badtag.yml" -Pattern 'VALIDATION-BAD-TAG-0000').Count -ne 3) { throw 'FAILED: bad-tag edit did not apply to all 3 postgres image lines' }
git -C "$run\UMNIA-APP" diff --quiet -- deploy/windows/compose.yml; Check 'original compose unmodified'
# Start a FRESH writer run against the legacy7 API (new log file; the §6b writer is long stopped):
$writer7 = Start-Process powershell -WindowStyle Hidden -ArgumentList '-ExecutionPolicy','Bypass','-File',"$run\writer-val.ps1",'-ApiBase','http://localhost:8080','-LogFile',"$run\logs\writer-7.log" -PassThru
$writer7.Id | Out-File -Encoding ascii "$run\logs\writer-7.pid"
Start-Sleep -Seconds 10
if (-not (Select-String -Path "$run\logs\writer-7.log" -Pattern 'START token acquired' -Quiet)) { throw 'FAILED: writer-7 did not acquire a token' }
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose-badtag.yml -OldProject umnia-val-legacy7 -NewProject umnia-val-new7 -BackupDir "$run\backups" -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; Expect-Fail 'upgrade with bad image tag (must fail at step 5)'
# Observe the failure stage in the transcript: the "== 5. Jalankan postgres project baru"
# marker followed by a Docker pull error (manifest unknown / not found), then
# "UPGRADE GAGAL:" and the printed JALUR PEMULIHAN text. Copy that recovery
# text verbatim into results.md. The source was already shut down at step 4.
if (-not (Select-String -Path "$run\logs\scenario-7.log" -Pattern 'UPGRADE GAGAL' -Quiet)) { throw 'FAILED: transcript does not contain UPGRADE GAGAL' }
# Stop the writer-7 process (only the PID this run created):
Stop-Process -Id (Get-Content "$run\logs\writer-7.pid")
# Follow the PRINTED recovery commands exactly (shape; use the paths as printed):
docker compose -f deploy/windows/compose-badtag.yml -p umnia-val-new7 down; Check 'recovery: down new7'
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy7 up -d; Check 'recovery: up legacy7'
Start-Sleep -Seconds 30
Confirm-Stack 'deploy/windows/compose.legacy.yml' 'umnia-val-legacy7'
if ((Get-Status 'http://localhost:8080/readyz') -ne 200) { throw 'FAILED: recovered legacy /readyz is not 200' }
$recFixture = docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy7 exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"; Check 'recovered fixture'
if ($recFixture -ne '1') { throw "FAILED: recovered fixture count is $recFixture, expected 1" }
# Prove resumed writer operation with a FRESH writer run (new log file, post-recovery traffic only):
$writer7b = Start-Process powershell -WindowStyle Hidden -ArgumentList '-ExecutionPolicy','Bypass','-File',"$run\writer-val.ps1",'-ApiBase','http://localhost:8080','-LogFile',"$run\logs\writer-7-after.log" -PassThru
$writer7b.Id | Out-File -Encoding ascii "$run\logs\writer-7-after.pid"
Start-Sleep -Seconds 10
if (-not (Select-String -Path "$run\logs\writer-7-after.log" -Pattern ' 201 ' -Quiet)) { throw 'FAILED: resumed writer produced no 201' }
Stop-Process -Id (Get-Content "$run\logs\writer-7-after.pid")
# NEVER delete the original volume as a shortcut. The bad-tag copy stays in
# place until the cleanup approval (§13).
Stop-Transcript
# Transition (see §5): stop umnia-val-legacy7 before scenario 8:
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy7 stop; Check 'stop legacy7'
```

## 11. Scenario 8 — Backup hash + restore evidence (self-contained)

**Why the previous recipe was incomplete.** It restored the custom dump into a bare postgres container with `--no-owner` and expected success, and it claimed `postgres` "already exists in every postgres container" while only pre-creating `umnia_business`. That claim is false for a cluster initialized with a custom bootstrap user: the official entrypoint creates exactly the configured `POSTGRES_USER` as superuser and nothing else. Evidence in this repo: the project initdb explicitly creates the role (`deploy/windows/initdb/01-runtime-roles.sh:72-78`, `CREATE ROLE postgres SUPERUSER LOGIN`) precisely because the entrypoint does not, and the old initdb failure `FATAL: role "postgres" does not exist` was observed. Installed `pg_restore --help` (PostgreSQL 16.15, from the `postgres:16-bookworm` image) and the official docs state: `-O, --no-owner` skips restoration of object OWNERSHIP only; access privileges (GRANT/REVOKE) are restored unless `-x, --no-privileges` (`--no-acl`) is also given. Sources: `pg_restore --help` from the image binaries (OBSERVED on the preparer's VM); PostgreSQL docs, `https://www.postgresql.org/docs/devel/app-pgrestore.html` (OFFICIAL, retrieved 2026-11-06).

**Source-based role inventory (the runtime inventory below is the authority).** The §6b backup is a `pg_dump --no-owner -Fc` of the LEGACY database. In that database, migration 002 ran without `-1` and stopped at its first `ALTER FUNCTION ... OWNER TO postgres` (`services/business-api/migrations/002_identity.sql:350`; harness-observed exit at 002:350, `role "postgres" does not exist`). Everything from line 350 on never applied: the `OWNER TO postgres` statements (350-356), the `REVOKE EXECUTE ... FROM PUBLIC` (371-376), and the `GRANT EXECUTE ... TO umnia_business` (377-382). What did apply: all of 001 (including `OWNER TO umnia_business` on `schema_migrations`, line 16) and 002 lines 1-349, including `GRANT USAGE ON SCHEMA app TO umnia_business` (line 11) and `OWNER TO umnia_business` on the 9 tables (333-341). Consequence for the dump: its ACLs are all at PostgreSQL defaults — the schema USAGE grant to the owner does not change `proacl`, and the functions keep the default `EXECUTE TO PUBLIC` (the 002 comment at lines 359-362 states the default explicitly). So the runtime inventory is EXPECTED to show `OWNER TO umnia_business` lines (ownership, skipped at restore by `--no-owner`) and NO `GRANT`/`REVOKE` lines. `postgres` is NOT expected as a grantee: no `GRANT ... TO postgres` exists in either migration file. If the runtime inventory differs, follow the inventory, not this expectation.

**Fixture roles vs the deployment contract.** Roles created in this disposable container are scaffolding only: `NOLOGIN`, no `SUPERUSER`, no `LOGIN`. In the real deployment, `postgres` is the SUPERUSER owner of the SECURITY DEFINER helper functions (created by initdb) and `umnia_business` is the NOSUPERUSER/NOBYPASSRLS runtime role (created by initdb). This scenario validates dump restorability and faithful ACL reproduction, NOT the deployment ownership contract — that is scenario 5 / upgrade step 8a.

**Recipe (self-contained).** The restore uses `--no-owner --exit-on-error` WITHOUT `--no-privileges`, so the production ACL semantics are validated (the GRANTs must succeed), not skipped. Any `pg_restore` error fails the scenario; errors are never ignored. The backup used is the §6b backup recorded in `$run\logs\backup-6b.txt`; if that file is unavailable, this scenario is BLOCKED (do not silently substitute another backup).

```powershell
Start-Transcript -Path "$run\logs\scenario-8.log"
cd "$run\UMNIA-APP"
$backup = Get-Content "$run\logs\backup-6b.txt"
if (-not (Test-Path $backup)) { throw "FAILED: recorded §6b backup not found: $backup" }
Get-FileHash -Algorithm SHA256 $backup
# Record the hash in results.md. A pg_restore listing alone is NOT proof — restore below.
docker run -d --name umnia-val-restore-check -e POSTGRES_PASSWORD=ValRestore123! -e POSTGRES_USER=umnia_bootstrap -e POSTGRES_DB=umnia_business postgres:16-bookworm; Check 'start restore-check container'
Start-Sleep -Seconds 15
docker cp $backup umnia-val-restore-check:/tmp/check.dump; Check 'cp backup into restore-check'
# (a) Inventory the roles/ACLs the dump references. The listing WITHOUT
# --no-owner shows OWNER TO lines too; the restore in (c) skips those via
# --no-owner, so only GRANT/REVOKE grantees need roles to exist.
docker exec umnia-val-restore-check pg_restore -f - /tmp/check.dump | Select-String -Pattern 'GRANT|REVOKE|OWNER TO' | Out-File -Encoding ascii "$run\logs\restore-acl-inventory.txt"; Check 'ACL inventory'
Get-Content "$run\logs\restore-acl-inventory.txt"
# OWNER TO postgres in the inventory means this is NOT the §6b legacy
# backup (the legacy 002 never applied OWNER TO postgres): STOP. Do not
# create the role to paper over a wrong backup.
if (Select-String -Path "$run\logs\restore-acl-inventory.txt" -Pattern 'OWNER TO postgres' -Quiet) { throw 'FAILED: inventory shows OWNER TO postgres — wrong backup (not the §6b legacy dump). Do not proceed.' }
# (b) Create every role a GRANT/REVOKE line names (except umnia_bootstrap,
# which exists, and PUBLIC, which needs no creation) as NOLOGIN. The
# inventory decides — do NOT pre-create roles unconditionally. Source-derived
# expectation: NO GRANT/REVOKE lines at all (legacy ACLs are all at
# PostgreSQL defaults), so in the expected case this loop creates nothing.
# Any role created here is recorded in restore-roles.txt.
$needed = @()
foreach ($m in (Select-String -Path "$run\logs\restore-acl-inventory.txt" -Pattern 'GRANT .* TO ([A-Za-z_][A-Za-z0-9_]*)')) { $needed += $m.Matches.Groups[1].Value }
foreach ($m in (Select-String -Path "$run\logs\restore-acl-inventory.txt" -Pattern 'REVOKE .* FROM ([A-Za-z_][A-Za-z0-9_]*)')) { $needed += $m.Matches.Groups[1].Value }
$needed = $needed | Where-Object { $_ -and ($_ -ne 'umnia_bootstrap') -and ($_ -ne 'PUBLIC') } | Sort-Object -Unique
foreach ($r in $needed) {
  docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "CREATE ROLE $r NOLOGIN;"; Check "pre-create role $r"
  Add-Content "$run\logs\restore-roles.txt" $r
}
# (c) Restore with production ACL semantics (no --no-privileges), failing loudly:
docker exec umnia-val-restore-check pg_restore -U umnia_bootstrap -d umnia_business --no-owner --exit-on-error /tmp/check.dump; Check 'pg_restore with ACLs'
# (d) Validate counts and grants against the LEGACY state — NOT the
# post-migrate state. The §6b backup is the legacy dump: 002 stopped at its
# first OWNER TO postgres (line 350), so the REVOKE FROM PUBLIC and GRANT
# EXECUTE TO umnia_business (lines 371-382) never applied. The functions
# therefore keep PostgreSQL's default EXECUTE TO PUBLIC, and there are NO
# post-migrate grants in this dump. The 12-grant check belongs to scenario 5
# / upgrade step 8a (after migrate re-runs 002), not here. The umnia-val-new
# target is stopped at this point (see §5), so start only its postgres for
# the read, then stop it again (same pattern as §9b):
docker compose -f deploy/windows/compose.yml -p umnia-val-new up -d postgres-business; Check 'start target postgres for read'
Start-Sleep -Seconds 15
$wsCount = docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces;"; Check 'restored workspaces count'
$wsTarget = docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces;"; Check 'target workspaces count'
if ($wsCount -ne $wsTarget) { throw "FAILED: restored workspaces ($wsCount) != target workspaces ($wsTarget)" }
docker compose -f deploy/windows/compose.yml -p umnia-val-new stop; Check 'stop target after read'
$fixtureRestored = docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"; Check 'restored fixture'
if ($fixtureRestored -ne '1') { throw "FAILED: restored fixture count is $fixtureRestored, expected 1" }
$publicGrants = docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema = 'app' AND routine_name IN ('accept_invite', 'has_permission', 'is_member', 'register_subject', 'session_subject', 'subject_by_email') AND grantee = 'PUBLIC' AND privilege_type = 'EXECUTE';"; Check 'restored PUBLIC EXECUTE grants'
if ($publicGrants -ne '6') { throw "FAILED: restored PUBLIC EXECUTE grants count is $publicGrants, expected 6 (legacy default)" }
$migratedGrants = docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema = 'app' AND routine_name IN ('accept_invite', 'has_permission', 'is_member', 'register_subject', 'session_subject', 'subject_by_email') AND grantee IN ('postgres', 'umnia_business');"; Check 'no post-migrate grants in dump'
if ($migratedGrants -ne '0') { throw "FAILED: post-migrate grants found in the legacy dump: $migratedGrants (expected 0)" }
# Capture the effective grants for the record (all routines, all grantees):
docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -c "SELECT routine_name, grantee, privilege_type FROM information_schema.routine_privileges WHERE routine_schema = 'app' ORDER BY routine_name, grantee;" | Out-File -Encoding ascii "$run\logs\restore-effective-grants.txt"; Check 'capture effective grants'
Write-Host "RESTORE OK workspaces=$wsCount public_grants=$publicGrants migrated_grants=$migratedGrants"
# Do NOT remove the container here. Stop it and leave it for the cleanup approval (§13):
docker stop umnia-val-restore-check; Check 'stop restore-check container'
Stop-Transcript
```

## 12. Evidence capture (every scenario)

- One transcript per scenario in `$run\logs\` (do not overwrite).
- For every command: the exact command line, exit code (`$LASTEXITCODE`), and output.
- `$run\results.md`: one line per scenario (`scenario N: PASS|FAIL|NOT RUN — <transcript> — <evidence>`); a FAIL line records the failing command and transcript line. Scenarios not attempted are NOT RUN.
- Record: OS build (`Get-ComputerInfo`), the exact PowerShell executable path and version, `docker --version`, `docker compose version`, `docker info` Server section, image digests (`docker image inspect postgres:16-bookworm --format '{{.Id}}'`, same for `rustfs/rustfs:1.0.4`).
- Record the backup SHA-256 hash (§6b) and the restored row counts (§11).
- Redact real credentials before sharing logs (the packet uses synthetic ones only).

## 13. Cleanup proposal — APPROVAL REQUIRED (separate; never part of the test steps)

Do NOT run anything in this section without explicit approval. It removes ONLY the resources created by this run, identified by their exact run-scoped names. Never point these commands at `umnia`/`umnia-legacy` or any user volume. A synthetic-looking name is not permission by itself — the approval must name this run directory.

```powershell
cd "$run\UMNIA-APP"
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-neg down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new2 down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new7 down -v
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy down -v
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy7 down -v
docker rm -f umnia-val-restore-check
docker volume ls | Select-String -Pattern 'umnia-val-'
# Expect: no umnia-val-* volumes remain. If any remain, remove each by name
# with: docker volume rm <name>  (only after confirming the name starts with umnia-val-)
Remove-Item -Force "$run\UMNIA-APP\deploy\windows\compose-badtag.yml"
Remove-Item -Force "$run\bad-compose.yml"
Remove-Item -Force "$run\compose-no-pg.yml"
Remove-Item -Recurse -Force "$run\UMNIA-APP"
# Keep $run\logs\, $run\backups\, and $run\results.md as evidence.
```

## 14. Static checks performed on this packet (preparer, 2026-11-06)

- **Revision pin:** the `$run` checkout detaches the full reviewed commit `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` and asserts `git rev-parse HEAD` equals it. The packet never checks out the moving branch. The delivery revision is recorded in the delivery report, not guessed in this file (see the header).
- **PowerShell syntax:** every ```powershell block in this file (15 blocks) was extracted and parsed with `[System.Management.Automation.Language.Parser]::ParseFile` under PowerShell 7.5.4 on the preparer's Linux VM — syntax only. Result: **15/15 PARSE OK, 0 failures**. A scan for PowerShell 7-only syntax (`??`, `?.`, `&&`, `||`, ternary) found no PowerShell operators: the only `||` occurrences are PostgreSQL `||` string concatenation inside SQL string literals. This is NOT a PowerShell 5.1 parse: 5.1 parsing of these blocks remains for the operator/reviewer on Windows. (For reference, the reviewer already parsed the 934ad4b upgrade script with explicit `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe`, version 5.1.26100.9818, zero errors — parser evidence only. The reviewer also parsed all 16 blocks of the previous packet version with the same executable, 16 parsed, 0 errors — independent syntax evidence only.)
- **Helper order (definition before use):** a static order check over the blocks in document order verified that every call to `Check`, `Expect-Fail`, `Get-Status`, `Confirm-Stack`, `Call-Api`, `Expect-Step`, `Get-Json` is preceded by its definition (helpers defined in §1, API helpers in §4b). The previous order bug (§1 calling `Check` before §2 defined it) is gone: §1 now defines the helpers before its first use, and §2 contains only policy. Parser success cannot catch a missing function; this order check is separate from the parse.
- **Helper semantics (fresh session):** the §1 helpers were loaded in a FRESH pwsh 7.5.4 process (no prior session state) and tested: `Check` throws after a failing native command and passes after a succeeding one; `Expect-Fail` throws on exit 0 and passes on non-zero; a failing cmdlet throws under `$ErrorActionPreference = 'Stop'`; calling `Check` BEFORE its definition fails as an unknown command (demonstrating the original bug class). This is pwsh 7.5.4, NOT PowerShell 5.1 — a fresh 5.1 session run of §1 remains for the operator/reviewer on Windows.
- **`$LASTEXITCODE` semantics:** verified in a fresh pwsh 7.5.4 process: cmdlets neither set nor reset `$LASTEXITCODE` (a `Check` after a cmdlet would read a stale code — the packet never does that); a pipeline whose first element is a native command keeps that command's code through trailing cmdlets (`/bin/false | Out-Null` leaves `$LASTEXITCODE` = 1). Hence `Check` after the `docker exec ... | Select-String ... | Out-File` inventory pipeline guards the `docker` call.
- **Restore-check role inventory (source-based):** from `services/business-api/migrations/001_init.sql` and `002_identity.sql` at the reviewed revision: 002 stopped at its first `ALTER FUNCTION ... OWNER TO postgres` (line 350), so the `REVOKE`/`GRANT` block (371-382) never applied in the legacy database; the dump's ACLs are therefore all at PostgreSQL defaults. `postgres` is not referenced as a grantee in either migration file. The entrypoint does not create a `postgres` role for a custom `POSTGRES_USER` — the project initdb creates it explicitly (`deploy/windows/initdb/01-runtime-roles.sh:72-78`). The runtime inventory (`pg_restore -f -`) in §11 is the authority at execution time; the packet creates only the roles it names, as `NOLOGIN`.
- **Cited script strings:** all 11 literal strings cited from the upgrade script (`penulis berhenti: tidak ada sesi aktif`, `UPGRADE SELESAI`, `UPGRADE GAGAL`, step markers, etc.) were verified to exist verbatim in `deploy/windows/Upgrade-UmniaVolume.ps1` at `934ad4b` (11/11). All 5 referenced repo paths exist. `deploy/windows/compose.yml` contains exactly 3 `image: postgres:16-bookworm` lines, matching the bad-tag check in §10.
- **Destructive commands:** `down -v` and `docker rm -f` occur ONLY in §13 (approval-required cleanup) plus one prose prohibition in §10; no test step deletes anything.
- **Command review:** every `docker`/`docker compose`/`psql`/`pg_dump`/`pg_restore` command was checked against the submitted script's actual commands (`deploy/windows/Upgrade-UmniaVolume.ps1` at `934ad4b`, OBSERVED) and the Compose contract (`deploy/windows/compose.yml`, OBSERVED). Step markers cited (`== 0c.`, `== 1.`, `== 4.`, `== 5.`, `UPGRADE SELESAI.`/`UPGRADE GAGAL:`) are the script's literal `Write-Step`/`Write-Host` strings.
- **pg_restore semantics:** `--no-owner` skips ownership only; `--no-privileges`/`--no-acl` skips GRANT/REVOKE — verified from installed `pg_restore --help` (PostgreSQL 16.15, from the `postgres:16-bookworm` image) and the official docs, `https://www.postgresql.org/docs/devel/app-pgrestore.html`.
- **Not checked:** actual execution on Windows (NOT RUN); PowerShell 5.1 parsing of these blocks (NOT RUN here — the reviewer's 5.1 parse of the PREVIOUS version is not a parse of this version); PowerShell 5.1 fresh-session execution of §1 (NOT RUN here); Docker Desktop behavior on Windows (NOT RUN).

## 15. What this packet does NOT claim

- It was not executed anywhere; no PASS/FAIL counts exist yet.
- A successful run of this packet proves the Windows runtime for revision `934ad4b` only — not the full F1 gate (login/workspace UI, module registry, admin-plane proof remain separate work).
- Source-review PASS is not merge/deployment permission.
