#Requires -Version 5.1
<#
.SYNOPSIS
  Upgrade volume UMNIA Windows lama ke volume baru (dump/restore).

.DESCRIPTION
  Volume lama dibuat sebelum F1-FOUNDATION-REMEDIATION: POSTGRES_USER-nya
  umnia_business (superuser, melewati RLS) dan tanpa service migrate.
  Role bootstrap superuser TIDAK BISA didemosi di tempat (PostgreSQL
  menolak ALTER ... NOSUPERUSER / REASSIGN OWNED / DROP ROLE untuk user
  bootstrap) — satu-satunya jalur yang didukung adalah dump/restore ke
  volume baru seperti yang dilakukan skrip ini.

  Urutan (gagal di langkah mana pun = berhenti, jangan lanjut):
    0. Pastikan stack LAMA berjalan (hanya postgres-business) dan
       password .env cocok (pg_isready). TIDAK ADA langkah destruktif
       sebelum backup tervalidasi.
    1. Dump database lama dari DALAM container lama dengan pg_dump -Fc
       (custom format) --no-owner ke /tmp di dalam container.
    2. Salin file backup keluar dengan `docker cp` (binary-safe; TIDAK
       memakai redirect shell `>` atau `<` — PowerShell tidak punya
       operator `<`, dan `>` di Windows PowerShell 5.1 menulis UTF-16).
    3. VALIDASI backup dengan `pg_restore -l` (daftar isi harus memuat
       tabel workspaces) SEBELUM stack lama dihentikan.
    4. Hentikan stack lama (`down` TANPA -v — volume lama dipertahankan
       sebagai cadangan; skrip ini tidak pernah memakai -v).
    5. Jalankan postgres project BARU (volume kosong -> initdb membuat
       role runtime umnia_business NOSUPERUSER/NOBYPASSRLS + role postgres).
    6. Salin backup ke container baru dan restore sebagai bootstrap
       (`pg_restore --no-owner --exit-on-error`).
    7. Jalankan stack baru penuh (`up --build`): service migrate
       menjalankan ulang 001+002 sebagai bootstrap, menormalkan
       kepemilikan tabel/fungsi dan hak EXECUTE.
    8. Verifikasi: role runtime f/f, migrasi 002 tercatat, 6 fungsi helper
       punya EXECUTE untuk postgres DAN umnia_business, /readyz 200, dan
       probe isolasi API (pengguna baru tidak bisa membaca workspace lama).

  Project Compose yang berbeda => volume berbeda:
  volume lama  = <OldProject>_postgres-business-data  (disentuh? TIDAK)
  volume baru  = <NewProject>_postgres-business-data  (fresh)
  Kedua project memakai port host yang sama (5432/5433/8080/9000/3000),
  jadi stack lama HARUS berhenti dulu sebelum stack baru jalan — skrip
  ini menegakkannya lewat urutan langkah.

  JALUR PEMULIHAN bila gagal sesudah langkah 4: volume lama masih utuh.
  Jalankan (ganti nama project bila memakai -OldProject lain):
    docker compose -f deploy/windows/compose.yml -p umnia-legacy up -d
  lalu perbaiki penyebabnya dan ulangi skrip ini (file backup baru akan
  dibuat dengan timestamp baru; file lama tidak pernah ditimpa).

  Skrip ini TIDAK PERNAH menulis/menimpa .env. Satu file .env yang sama
  dipakai kedua project (password bootstrap/runtime harus sama dengan
  yang dipakai stack lama, kalau tidak langkah 0 sudah gagal).

.NOTES
  Sintaks dijaga kompatibel Windows PowerShell 5.1 (tanpa ??, ?:, dan
  tanpa -Encoding utf8NoBOM). Setiap panggilan docker dicek $LASTEXITCODE
  secara eksplisit — kegagalan native command tidak otomatis throw di
  PowerShell walau $ErrorActionPreference = 'Stop'.
#>
[CmdletBinding()]
param(
  [string]$ComposeFile = "deploy/windows/compose.yml",
  [string]$OldProject = "umnia-legacy",
  [string]$NewProject = "umnia",
  [string]$BackupDir = "backups",
  [string]$ApiBase = "http://localhost:8080"
)

$ErrorActionPreference = 'Stop'

function Write-Step($Text) { Write-Host ""; Write-Host "== $Text" }
function Get-RecoveryText {
  return @"
JALUR PEMULIHAN: volume lama masih utuh.
  docker compose -f $ComposeFile -p $OldProject up -d
  (volume: ${OldProject}_postgres-business-data)
  Perbaiki penyebab kegagalan, lalu ulangi skrip ini.
"@
}
function Invoke-Compose($Project, $Arguments) {
  # Jalankan `docker compose -f <file> -p <project> <args>`, throw bila gagal.
  $cmd = "docker compose -f $ComposeFile -p $Project $Arguments"
  Write-Host "  > $cmd"
  & docker compose -f $ComposeFile -p $Project @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "Perintah gagal (exit $LASTEXITCODE): $cmd"
  }
}
function Invoke-ComposeCapture($Project, $Arguments) {
  $out = & docker compose -f $ComposeFile -p $Project @Arguments 2>&1
  if ($LASTEXITCODE -ne 0) {
    throw "Perintah gagal (exit $LASTEXITCODE): docker compose -f $ComposeFile -p $Project $($Arguments -join ' ')"
  }
  return ($out | Out-String).Trim()
}
function Get-ServiceContainer($Project, $Service) {
  $id = Invoke-ComposeCapture $Project @('ps', '-q', $Service)
  if ([string]::IsNullOrWhiteSpace($id)) { throw "Container $Service project $Project tidak ditemukan/berjalan." }
  return $id.Trim()
}
function Invoke-DockerCp($Source, $Destination) {
  Write-Host "  > docker cp $Source $Destination"
  & docker cp $Source $Destination
  if ($LASTEXITCODE -ne 0) { throw "docker cp gagal (exit $LASTEXITCODE): $Source -> $Destination" }
}

try {
  Write-Step "0. Prasyarat"
  foreach ($tool in @('docker')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "Perintah '$tool' tidak ditemukan di PATH." }
  }
  if (-not (Test-Path $ComposeFile)) { throw "Compose file tidak ditemukan: $ComposeFile" }
  if (-not (Test-Path ".env")) {
    Write-Warning ".env tidak ditemukan — memakai password default compose. Bila stack lama memakai password kustom, langkah 0 akan gagal; isi .env dulu."
  }

  $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
  if (-not (Test-Path $BackupDir)) { New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null }
  $backupName = "umnia-backup-$stamp.dump"
  $backupPath = Join-Path $BackupDir $backupName
  $backupFull = [System.IO.Path]::GetFullPath($backupPath)
  if (Test-Path $backupFull) { throw "File backup sudah ada (tidak pernah ditimpa): $backupFull" }

  Write-Step "0b. Jalankan postgres lama dan periksa password .env"
  Invoke-Compose $OldProject @('up', '-d', 'postgres-business')
  $ready = $false
  for ($i = 0; $i -lt 12; $i++) {
    & docker compose -f $ComposeFile -p $OldProject exec -T postgres-business pg_isready -U umnia_bootstrap -d umnia_business | Out-Null
    if ($LASTEXITCODE -eq 0) { $ready = $true; break }
    Start-Sleep -Seconds 5
  }
  if (-not $ready) { throw "postgres lama tidak ready (periksa password .env / log container)." }

  Write-Step "1. Dump database lama (custom format, --no-owner)"
  Invoke-Compose $OldProject @('exec', '-T', 'postgres-business', 'pg_dump', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '--no-owner', '-Fc', '-f', '/tmp/umnia-backup.dump')

  Write-Step "2. Salin backup keluar (docker cp, binary-safe)"
  $oldCid = Get-ServiceContainer $OldProject 'postgres-business'
  Invoke-DockerCp "${oldCid}:/tmp/umnia-backup.dump" $backupFull

  Write-Step "3. Validasi backup SEBELUM menghentikan stack lama"
  $localSize = (Get-Item $backupFull).Length
  if ($localSize -le 0) { throw "File backup kosong: $backupFull" }
  Write-Host "  ukuran backup: $localSize byte"
  $listing = Invoke-ComposeCapture $OldProject @('exec', '-T', 'postgres-business', 'pg_restore', '-l', '/tmp/umnia-backup.dump')
  if ($listing -notmatch 'TABLE.*\bworkspaces\b') { throw "Validasi pg_restore -l gagal: tabel workspaces tidak ada di daftar isi backup." }
  Write-Host "  pg_restore -l OK (memuat tabel workspaces)"
  Invoke-Compose $OldProject @('exec', '-T', 'postgres-business', 'rm', '-f', '/tmp/umnia-backup.dump')

  Write-Step "4. Hentikan stack lama (volume DIPERTAHANKAN, tanpa -v)"
  Invoke-Compose $OldProject @('down')
  $still = Invoke-ComposeCapture $OldProject @('ps', '-q')
  if (-not [string]::IsNullOrWhiteSpace($still)) { throw "Container lama masih berjalan sesudah down." }

  Write-Step "5. Jalankan postgres project baru (volume kosong -> initdb)"
  Invoke-Compose $NewProject @('up', '-d', 'postgres-business')
  $ready = $false
  for ($i = 0; $i -lt 12; $i++) {
    & docker compose -f $ComposeFile -p $NewProject exec -T postgres-business pg_isready -U umnia_bootstrap -d umnia_business | Out-Null
    if ($LASTEXITCODE -eq 0) { $ready = $true; break }
    Start-Sleep -Seconds 5
  }
  if (-not $ready) { throw "postgres baru tidak ready (periksa log initdb)." }

  Write-Step "6. Restore backup ke database baru"
  $newCid = Get-ServiceContainer $NewProject 'postgres-business'
  Invoke-DockerCp $backupFull "${newCid}:/tmp/umnia-backup.dump"
  Invoke-Compose $NewProject @('exec', '-T', 'postgres-business', 'pg_restore', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '--no-owner', '--exit-on-error', '/tmp/umnia-backup.dump')
  Invoke-Compose $NewProject @('exec', '-T', 'postgres-business', 'rm', '-f', '/tmp/umnia-backup.dump')

  Write-Step "7. Jalankan stack baru penuh (migrate menormalkan kepemilikan)"
  Invoke-Compose $NewProject @('up', '--build', '-d')

  Write-Step "8a. Verifikasi database"
  $roleCheck = Invoke-ComposeCapture $NewProject @('exec', '-T', 'postgres-business', 'psql', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '-tAc', "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname = 'umnia_business';")
  if ($roleCheck -ne 'false/false') { throw "Role umnia_business bukan NOSUPERUSER/NOBYPASSRLS (dapat: '$roleCheck')." }
  $migCheck = Invoke-ComposeCapture $NewProject @('exec', '-T', 'postgres-business', 'psql', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '-tAc', "SELECT count(*) FROM schema_migrations WHERE version = '002_identity';")
  if ($migCheck -ne '1') { throw "Migrasi 002_identity tidak tercatat (dapat: '$migCheck')." }
  $grantCheck = Invoke-ComposeCapture $NewProject @('exec', '-T', 'postgres-business', 'psql', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '-tAc', "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema = 'app' AND routine_name IN ('accept_invite', 'has_permission', 'is_member', 'register_subject', 'session_subject', 'subject_by_email') AND grantee IN ('postgres', 'umnia_business');")
  if ($grantCheck -ne '12') { throw "Hak EXECUTE helper bukan 6 fungsi x 2 grantee (dapat: '$grantCheck', harap 12)." }
  Write-Host "  role f/f OK, 002_identity OK, EXECUTE 12/12 OK"

  Write-Step "8b. Verifikasi API (readyz + isolasi)"
  $ready = $false
  for ($i = 0; $i -lt 24; $i++) {
    try {
      $r = Invoke-WebRequest -Uri "$ApiBase/readyz" -UseBasicParsing -TimeoutSec 5
      if ($r.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Seconds 5
  }
  if (-not $ready) { throw "/readyz tidak 200 sesudah 120 detik." }
  $suffix = Get-Date -Format 'yyyyMMddHHmmss'
  $ownerBody = @{ email = "owner-upgrade-$suffix@contoh.id"; password = 'UpgradePass123' } | ConvertTo-Json
  $owner = Invoke-RestMethod -Uri "$ApiBase/auth/register" -Method Post -ContentType 'application/json' -Body $ownerBody
  $ownerLogin = Invoke-RestMethod -Uri "$ApiBase/auth/login" -Method Post -ContentType 'application/json' -Body $ownerBody
  $staffBody = @{ email = "staff-upgrade-$suffix@contoh.id"; password = 'UpgradePass123' } | ConvertTo-Json
  $staff = Invoke-RestMethod -Uri "$ApiBase/auth/register" -Method Post -ContentType 'application/json' -Body $staffBody
  $staffLogin = Invoke-RestMethod -Uri "$ApiBase/auth/login" -Method Post -ContentType 'application/json' -Body $staffBody
  $wsBody = @{ name = "Toko Upgrade $suffix" } | ConvertTo-Json
  $ws = Invoke-RestMethod -Uri "$ApiBase/workspaces" -Method Post -ContentType 'application/json' -Headers @{ Authorization = "Bearer $($ownerLogin.token)" } -Body $wsBody
  try {
    Invoke-RestMethod -Uri "$ApiBase/workspaces/$($ws.id)" -Method Get -Headers @{ Authorization = "Bearer $($staffLogin.token)" } | Out-Null
    throw "Isolasi GAGAL: pengguna baru bisa membaca workspace orang lain (harap 403)."
  } catch {
    $status = $null
    if ($_.Exception.Response) { $status = [int]$_.Exception.Response.StatusCode }
    if ($status -ne 403) { throw "Isolasi: harap 403, dapat $status." }
  }
  Write-Host "  /readyz 200 OK, isolasi API OK (pengguna baru 403 sebelum undangan)"

  Write-Host ""
  Write-Host "UPGRADE SELESAI."
  Write-Host "  Backup       : $backupFull"
  Write-Host "  Volume lama  : ${OldProject}_postgres-business-data (masih ada; hapus hanya sesudah yakin)"
  Write-Host "  Volume baru  : ${NewProject}_postgres-business-data"
  Write-Host "  Hapus volume lama (HANYA bila sudah yakin, TIDAK bisa dibatalkan):"
  Write-Host "    docker volume rm ${OldProject}_postgres-business-data"
}
catch {
  Write-Host ""
  Write-Host "UPGRADE GAGAL: $($_.Exception.Message)" -ForegroundColor Red
  Write-Host (Get-RecoveryText)
  exit 1
}
