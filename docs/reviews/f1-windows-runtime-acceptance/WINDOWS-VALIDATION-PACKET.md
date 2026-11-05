# Windows Validation Packet — F1-WINDOWS-RUNTIME-ACCEPTANCE

**Status: NOT RUN anywhere. Prepared commands only (PROPOSED).**
**Tested revision: `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` (`f1-03-identity`).**
**Scope: scenarios 1–8 from the acceptance task. No merge/deploy/user-data migration is authorized by running this packet.**

Run everything in **Windows PowerShell 5.1** (built-in `powershell`, NOT `pwsh` 7.x). Start each scenario with `Start-Transcript` and record the exit code of every command. If any step's actual result differs from the expected result, STOP, record the difference as a finding, and do not force it green.

## 0. Prerequisites (verify first — all must pass)

```powershell
$PSVersionTable.PSVersion            # expect 5.1.x
docker --version                     # expect Docker version 29.x (or current Desktop version)
docker compose version               # expect v2.x
docker info                          # expect a Server section (daemon running); exit 0
git --version
Get-NetTCPConnection -LocalPort 5432,5433,8080,9000,3000 -State Listen -ErrorAction SilentlyContinue
# expect: no output (ports free on 127.0.0.1)
```

Also required: Windows 10/11 64-bit with virtualization enabled, Docker Desktop 4.x+ in Linux-containers mode with the WSL 2 backend, internet access to pull `postgres:16-bookworm` and `rustfs/rustfs:1.0.4` (or both images already cached), ~10 GB free disk.

## 1. Isolated checkout (do NOT use the user's working repo or `.env`)

```powershell
New-Item -ItemType Directory -Force -Path C:\umnia-validation\logs | Out-Null
git clone https://github.com/ch0c01dxyz/UMNIA-APP.git C:\umnia-validation\UMNIA-APP
cd C:\umnia-validation\UMNIA-APP
git checkout f1-03-identity
git rev-parse HEAD
# expect: 934ad4b8b79ae3cd5dca01121a270fd6f8f955f1
```

Create `C:\umnia-validation\UMNIA-APP\.env` with **synthetic test-only credentials** (never the user's real `.env`):

```env
UMNIA_BUSINESS_BOOTSTRAP_PASSWORD=ValBootstrap123!
UMNIA_BUSINESS_DB_PASSWORD=ValRuntime123!
UMNIA_ADMIN_BOOTSTRAP_PASSWORD=ValAdminBoot123!
UMNIA_ADMIN_DB_PASSWORD=ValAdminRun123!
RUSTFS_ACCESS_KEY=val-access
RUSTFS_SECRET_KEY=val-secret-123
UMNIA_LEGACY_BOOTSTRAP_PASSWORD=ValLegacy123!
```

## 2. Legacy Compose file (created by the operator, in the isolated checkout only — DO NOT commit)

Save as `deploy\windows\compose.legacy.yml`. It reproduces a pre-remediation volume: `POSTGRES_USER=umnia_business` (superuser), **no** `migrate` service, **no** initdb mount. It must define `postgres-business`, `business-api`, and `web` so the upgrade script can start/stop the writers and the recovery path can restart the full old stack. Relative build paths are preserved (`../../services/business-api`, `../../apps/web`).

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

## 3. Helper scripts (save in `C:\umnia-validation\`, NOT in the repo)

### 3a. Controlled writer — `writer-val.ps1` (PowerShell 5.1 compatible)

```powershell
param([string]$ApiBase = 'http://localhost:8080', [string]$LogFile = 'C:\umnia-validation\logs\writer.log', [string]$Prefix = 'writer-val', [string]$Interval = '0.5')
$ErrorActionPreference = 'Stop'
$body = @{ email = "$Prefix@contoh.id"; password = 'WriterPass123' } | ConvertTo-Json
try { $r = Invoke-RestMethod -Uri "$ApiBase/auth/register" -Method Post -Body $body -ContentType 'application/json' } catch { $r = $null }
if (-not $r.token) {
  $r = Invoke-RestMethod -Uri "$ApiBase/auth/login" -Method Post -Body $body -ContentType 'application/json'
}
$token = $r.token
if (-not $token) { Add-Content $LogFile 'writer: no token'; exit 1 }
Add-Content $LogFile 'writer: token acquired'
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
  Add-Content $LogFile "$([DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds()) $code $name"
  Start-Sleep -Seconds $Interval
}
```

Start it with `Start-Process powershell -ArgumentList '-ExecutionPolicy','Bypass','-File','C:\umnia-validation\writer-val.ps1','-ApiBase','http://localhost:8080','-LogFile','C:\umnia-validation\logs\writer.log' -PassThru` and keep the returned process object to stop it later (`Stop-Process -Id <pid>`).

### 3b. API call helper for the tenant-isolation workflow (paste into the PS 5.1 session)

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

## 4. Scenario 1 — Fresh startup on Windows Docker Compose

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-1.log
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh config -q; $LASTEXITCODE
# expect: 0
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh up -d --build; $LASTEXITCODE
# expect: 0; record wall-clock time (build takes minutes on first run)
Start-Sleep -Seconds 60
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh ps
# expect: postgres-business, postgres-admin, migrate (exited 0), rustfs,
#   business-api, web — all healthy/running. RECORD every service state;
#   a failing unrelated service is a finding, not something to hide.
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT rolname, rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname IN ('umnia_business','postgres');"
# expect: umnia_business| false/false ; postgres| true/true (or true/false — record actual)
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM pg_roles WHERE rolname LIKE 'umnia_admin%';"
# expect: 0 (no admin roles in the business cluster)
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh exec -T postgres-business psql -U umnia_admin -d umnia_business -tAc "SELECT 1;"
# expect: FATAL role "umnia_admin" does not exist (cross-cluster login fails)
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT version FROM schema_migrations ORDER BY version;"
# expect: 001_init, 002_identity
Invoke-WebRequest -Uri http://localhost:8080/healthz -UseBasicParsing
# expect: 200
Invoke-WebRequest -Uri http://localhost:8080/readyz -UseBasicParsing
# expect: 200
Invoke-WebRequest -Uri http://localhost:3000 -UseBasicParsing
# expect: 200 (record actual status/body if different)
Stop-Transcript
```

Then stop (do NOT delete yet — scenario 5's isolation workflow can reuse this stack, or use a fresh one; record which): `docker compose -f deploy/windows/compose.yml -p umnia-val-fresh down` (no `-v`; volumes preserved until cleanup).

## 5. Scenario 2 — Readiness negative (no migration → not ready)

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-2.log
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.yml -p umnia-val-neg up -d postgres-business; $LASTEXITCODE
# expect: 0; wait until postgres-business is healthy:
#   docker compose -f deploy/windows/compose.yml -p umnia-val-neg ps
docker compose -f deploy/windows/compose.yml -p umnia-val-neg up -d --no-deps business-api; $LASTEXITCODE
# expect: 0 (starts the API WITHOUT the migrate service)
Start-Sleep -Seconds 15
Invoke-WebRequest -Uri http://localhost:8080/healthz -UseBasicParsing
# expect: 200 (process alive)
try { Invoke-WebRequest -Uri http://localhost:8080/readyz -UseBasicParsing; Write-Host "readyz: 200 (UNEXPECTED)" } catch { Write-Host "readyz: $([int]$_.Exception.Response.StatusCode) (expected non-200)" }
# expect: non-200 (record the actual code). NEVER drop tables to create this case.
docker compose -f deploy/windows/compose.yml -p umnia-val-neg up -d; $LASTEXITCODE
# expect: 0 (now migrate runs)
Start-Sleep -Seconds 20
Invoke-WebRequest -Uri http://localhost:8080/readyz -UseBasicParsing
# expect: 200
docker compose -f deploy/windows/compose.yml -p umnia-val-neg down
Stop-Transcript
```

## 6. Scenario 3 — Legacy upgrade through Windows PowerShell 5.1

### 6a. Build the legacy fixture (once)

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-3-fixture.log
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy up -d --build; $LASTEXITCODE
# expect: 0; wait until postgres-business is healthy
$cid = docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ps -q postgres-business
docker cp services/business-api/migrations/001_init.sql ${cid}:/migrations-001_init.sql
docker cp services/business-api/migrations/002_identity.sql ${cid}:/migrations-002_identity.sql
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -v ON_ERROR_STOP=1 -f /migrations-001_init.sql -f /migrations-002_identity.sql; $LASTEXITCODE
# EXPECT: non-zero, with: role "postgres" does not exist (002 fails mid-file).
# This is the pre-remediation half-applied state. Record the full output.
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT version FROM schema_migrations ORDER BY version;"
# Prior Linux evidence: 001_init + 002_identity recorded (half-applied). RECORD ACTUAL.
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT rolname, rolsuper FROM pg_roles WHERE rolname = 'umnia_business';"
# expect: umnia_business|t (bootstrap superuser — the legacy defect)
# Fixture through the API (no invented SQL): register owner, create "Toko Lama".
$ownerBody = @{ email = 'owner-val@contoh.id'; password = 'ValOwner123!' } | ConvertTo-Json
$owner = Invoke-RestMethod -Uri http://localhost:8080/auth/register -Method Post -Body $ownerBody -ContentType 'application/json'
$ownerLogin = Invoke-RestMethod -Uri http://localhost:8080/auth/login -Method Post -Body $ownerBody -ContentType 'application/json'
$wsBody = @{ name = 'Toko Lama' } | ConvertTo-Json
$ws = Invoke-RestMethod -Uri http://localhost:8080/workspaces -Method Post -Body $wsBody -ContentType 'application/json' -Headers @{ Authorization = "Bearer $($ownerLogin.token)" }
Write-Host "fixture workspace id: $($ws.id)"
# expect: register 201, login 200, workspace 201. Record the workspace id.
Stop-Transcript
```

### 6b. Running-source upgrade

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-3-running.log
cd C:\umnia-validation\UMNIA-APP
# Start the controlled writer against the legacy API (scenario 4 runs together with this).
$writer = Start-Process powershell -ArgumentList '-ExecutionPolicy','Bypass','-File','C:\umnia-validation\writer-val.ps1','-ApiBase','http://localhost:8080','-LogFile','C:\umnia-validation\logs\writer.log' -PassThru
Start-Sleep -Seconds 10
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose.yml -OldProject umnia-val-legacy -NewProject umnia-val-new -BackupDir C:\umnia-validation\backups -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: 0 and "UPGRADE SELESAI."
# ROUTING ASSERTION (from the transcript): every source line reads
#   "> docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ..."
# and every target line reads
#   "> docker compose -f deploy/windows/compose.yml -p umnia-val-new ..."
# MOUNT/VOLUME ASSERTION:
docker inspect (docker compose -f deploy/windows/compose.yml -p umnia-val-new ps -q postgres-business) | Select-String -Pattern 'postgres-business-data'
docker volume ls | Select-String -Pattern 'umnia-val-'
# expect: umnia-val-legacy_postgres-business-data (old, preserved) AND
#   umnia-val-new_postgres-business-data (new) both exist.
Stop-Transcript
# Leave the writer running until scenario 4's checks are done, then:
# Stop-Process -Id $writer.Id
```

### 6c. Stopped-source upgrade (reuses the preserved legacy volume from 6b)

After 6b the legacy stack is down (step 4) with its volume preserved — that IS the stopped-source state. Run the upgrade again with a fresh target project:

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-3-stopped.log
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy ps
# expect: all legacy containers stopped (no running containers), volume still exists
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose.yml -OldProject umnia-val-legacy -NewProject umnia-val-new2 -BackupDir C:\umnia-validation\backups -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: 0. The transcript must show the source postgres being started
#   via the LEGACY file ("postgres lama berhenti — start non-destruktif").
Stop-Transcript
```

## 7. Scenario 4 — Source maintenance before snapshot (with 6b)

After the 6b upgrade finishes, stop the writer and check its log:

```powershell
Stop-Process -Id $writer.Id
Get-Content C:\umnia-validation\logs\writer.log | Select-String -Pattern ' 201 ' | Measure-Object
# Count the 201 lines. Then compare with the target:
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name LIKE 'Writer-%';"
# expect: equal to the number of 201 lines in the writer log (all
#   acknowledged pre-snapshot writes survived). No 201 may appear in the
#   log after the transcript timestamp of "== 1. Dump" — check by time.
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"
# expect: 1 (fixture workspace survived)
# External writers: no other API client may write during the maintenance
# window (from -ConfirmMaintenance until cutover/recovery). Record that
# no other writer ran.
```

## 8. Scenario 5 — Target identity + tenant isolation (on the 6b target)

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-5.log
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname = 'umnia_business';"
# expect: false/false
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM schema_migrations WHERE version = '002_identity';"
# expect: 1
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema = 'app' AND routine_name IN ('accept_invite', 'has_permission', 'is_member', 'register_subject', 'session_subject', 'subject_by_email') AND grantee IN ('postgres', 'umnia_business');"
# expect: 12
Invoke-WebRequest -Uri http://localhost:8080/readyz -UseBasicParsing
# expect: 200
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM identity_subjects;"
# expect: fixture subjects + 1 writer account ONLY (no synthetic accounts
#   created by the upgrade script). Record the actual count and emails:
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT email FROM identity_subjects ORDER BY email;"
docker volume inspect umnia-val-legacy_postgres-business-data
# expect: volume exists (old volume preserved)
Stop-Transcript
```

### Tenant-isolation workflow (PowerShell 5.1 equivalent of `api-workflow.sh`, 14 steps)

Run against the upgraded API (`http://localhost:8080`). Paste the §3b helpers first. `$sfx` must be unique per run.

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-5-workflow.log
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

## 9. Scenario 6 — Negative cases (fresh fixtures; record exit code = 1 and no subsequent mutations each time)

```powershell
cd C:\umnia-validation\UMNIA-APP
# (a) Equal projects:
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-same -NewProject umnia-val-same -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero; ZERO docker calls (transcript shows the equality error only).
# (b) Occupied target (use the already-upgraded umnia-val-new from 6b):
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero ("Target tidak fresh"); target marker/data untouched — verify:
docker compose -f deploy/windows/compose.yml -p umnia-val-new exec -T postgres-business psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"
# expect: 1
# (c) Missing original Compose file:
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile C:\does\not\exist.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero; fails before ANY docker call.
# (d) Invalid original Compose file (garbage YAML):
'this is: [not valid yaml' | Out-File -Encoding ascii C:\umnia-validation\bad-compose.yml
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile C:\umnia-validation\bad-compose.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero (config -q fails); no mutating docker calls.
# (e) Inspection failure: legacy file WITHOUT the postgres-business service.
# Copy compose.legacy.yml to C:\umnia-validation\compose-no-pg.yml and delete
# the postgres-business service block from the copy, then:
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-val-legacy -NewProject umnia-val-new9 -LegacyComposeFile C:\umnia-validation\compose-no-pg.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero; abort before dump/restore/source shutdown. No security
# controls were disabled to inject this failure.
```

## 10. Scenario 7 — Controlled failure after source shutdown + printed recovery

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-7.log
cd C:\umnia-validation\UMNIA-APP
# Fresh legacy fixture + fresh target are required: rebuild the fixture per §6a
# (down -v the legacy project first, then rebuild), target umnia-val-new7.
# Hold the target postgres host port (5432) with a real listener in a
# SEPARATE PowerShell window (keep it running during the upgrade):
#   $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 5432)
#   $listener.Start()
powershell -ExecutionPolicy Bypass -File .\deploy\windows\Upgrade-UmniaVolume.ps1 -ComposeFile deploy/windows/compose.yml -OldProject umnia-val-legacy -NewProject umnia-val-new7 -BackupDir C:\umnia-validation\backups -ApiBase http://localhost:8080 -LegacyComposeFile deploy/windows/compose.legacy.yml -ConfirmMaintenance; $LASTEXITCODE
# expect: non-zero; the failure happens at step 5 (new postgres cannot bind
# 5432) AFTER the source was shut down at step 4. Copy the printed
# JALUR PEMULIHAN text verbatim into the evidence log.
# In the listener window: $listener.Stop()
# Then follow the PRINTED recovery commands exactly (example shape):
docker compose -f deploy/windows/compose.yml -p umnia-val-new7 down
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy up -d
# Prove recovery:
Invoke-WebRequest -Uri http://localhost:8080/readyz -UseBasicParsing
# expect: 200 (legacy API back)
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"
# expect: 1 (fixture preserved)
# Resume a writer (fresh writer-val.ps1 run) and confirm a 201 is logged.
# NEVER delete the original volume as a shortcut.
Stop-Transcript
```

## 11. Scenario 8 — Backup hash + restore evidence (use the backup from §6b or §10)

```powershell
Start-Transcript -Path C:\umnia-validation\logs\scenario-8.log
$backup = Get-ChildItem C:\umnia-validation\backups\*.dump | Sort-Object LastWriteTime -Descending | Select-Object -First 1
Get-FileHash -Algorithm SHA256 $backup.FullName
# Record the hash. Listing alone is NOT proof — restore into a disposable container:
docker run -d --name umnia-val-restore-check -e POSTGRES_PASSWORD=ValRestore123! -e POSTGRES_USER=umnia_bootstrap -e POSTGRES_DB=umnia_business postgres:16-bookworm
Start-Sleep -Seconds 10
docker cp $backup.FullName umnia-val-restore-check:/tmp/check.dump
docker exec umnia-val-restore-check pg_restore -U umnia_bootstrap -d umnia_business --no-owner --exit-on-error /tmp/check.dump; $LASTEXITCODE
# expect: 0
docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces;"
# expect: equal to the source workspace count recorded in scenario 4/5.
docker exec umnia-val-restore-check psql -U umnia_bootstrap -d umnia_business -tAc "SELECT count(*) FROM workspaces WHERE name = 'Toko Lama';"
# expect: 1
docker rm -f umnia-val-restore-check
Stop-Transcript
```

## 12. Evidence capture (every scenario)

- One transcript per scenario in `C:\umnia-validation\logs\` (do not overwrite).
- For every command: the exact command line, exit code (`$LASTEXITCODE`), and output.
- Record: OS build (`winver` / `Get-ComputerInfo`), `docker --version`, `docker compose version`, `docker info` Server section, image digests (`docker image inspect postgres:16-bookworm --format '{{.Id}}'`, same for rustfs).
- Record the backup SHA-256 hash and the restored row counts (§11).
- Redact real credentials before sharing logs (the packet uses synthetic ones only).

## 13. Cleanup proposal (SEPARATE — do NOT run without authorization)

These commands remove ONLY the `umnia-val-*` test projects/volumes and the isolated checkout. They must never be pointed at `umnia`/`umnia-legacy` or any user volume.

```powershell
cd C:\umnia-validation\UMNIA-APP
docker compose -f deploy/windows/compose.yml -p umnia-val-fresh down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-neg down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new2 down -v
docker compose -f deploy/windows/compose.yml -p umnia-val-new7 down -v
docker compose -f deploy/windows/compose.legacy.yml -p umnia-val-legacy down -v
docker volume ls | Select-String -Pattern 'umnia-val-'
# expect: no umnia-val-* volumes remain; if any remain, remove each by name
# with: docker volume rm <name>  (only after confirming the name starts with umnia-val-)
Remove-Item -Recurse -Force C:\umnia-validation\UMNIA-APP
# Keep C:\umnia-validation\logs\ and C:\umnia-validation\backups\ as evidence.
```

## 14. What this packet does NOT claim

- It was not executed anywhere; no PASS/FAIL counts exist yet.
- A successful run of this packet proves the Windows runtime for revision `934ad4b` only — not the full F1 gate (login/workspace UI, module registry, admin-plane proof remain separate work).
- Source-review PASS is not merge/deployment permission.
