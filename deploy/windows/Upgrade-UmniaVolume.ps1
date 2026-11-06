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

  Identitas sumber (MF1): volume lama HANYA punya akun bootstrap
  umnia_business. Skrip ini memakai -SourceDbUser (default
  umnia_business) untuk SEMUA perintah ke sumber lama, dan
  umnia_bootstrap hanya untuk target baru. Sebelum dump, skrip membaca
  identitas cluster aktual dengan query SQL terautentikasi (SELECT
  current_user + tabel workspaces); pg_isready saja tidak cukup karena
  ia tidak memverifikasi kredensial/identitas role. Skrip TIDAK PERNAH
  membuat superuser baru di database lama.

  Preflight (MF2): SEBELUM ada perintah yang mengubah keadaan, skrip
  menolak project lama/baru yang sama (setelah normalisasi nama Compose),
  menolak target yang sudah terisi (container berjalan atau volume
  <NewProject>_postgres-business-data / postgres-admin-data / rustfs-data
  sudah ada), dan memastikan sumber benar-benar ada (container +
  volume-nya, termasuk nama volume dari `docker inspect`). Target yang
  terisi TIDAK PERNAH dihapus/ditimpa/dibersihkan oleh skrip ini.

  Quiesce (MF3): penulis HARUS berhenti sebelum dump. Skrip menghentikan
  business-api dan web project lama, lalu memeriksa secara observabel
  (container berhenti + tidak ada sesi penulis aktif di pg_stat_activity)
  sebelum dump, dan penulis tetap berhenti sampai cutover (stack baru
  jalan) atau pemulihan. Penulis eksternal (klien DB langsung, instance
  API lain) TIDAK BISA dihentikan skrip ini: operator WAJIB menghentikan
  mereka dan mengonfirmasi dengan -ConfirmMaintenance. Tanpa flag itu
  skrip menolak jalan. Batas jaminan ini didokumentasikan di bawah.

  Validasi (MF4): verifikasi default skrip ini HANYA read-only
  (/readyz + query SELECT). Skrip TIDAK membuat akun/workspace tes di
  database pengguna. Skenario isolasi API (register/login/403) dijalankan
  terpisah pada fixture sintetis terisolasi, bukan di sini.

  Urutan (gagal di langkah mana pun = berhenti, jangan lanjut):
    0. Prasyarat + preflight: file compose lama WAJIB ada dan valid
       (Test-Path + `docker compose config -q`); normalisasi +
       bandingkan project, periksa target kosong, periksa sumber ada
       (termasuk nama volume via inspect). TIDAK ADA perintah mutating
       sebelum preflight lolos.
    0b. Jalankan postgres lama bila berhenti, lalu verifikasi identitas
       sumber dengan SQL terautentikasi.
    0c. Maintenance: -ConfirmMaintenance wajib; hentikan penulis
       (business-api, web) + periksa observabel tidak ada penulis aktif.
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
    8. Verifikasi read-only: role runtime f/f, migrasi 002 tercatat,
       6 fungsi helper punya EXECUTE untuk postgres DAN umnia_business,
       /readyz 200, dan hitung workspaces hasil restore (SELECT saja).

  Project Compose yang berbeda => volume berbeda:
  volume lama  = <OldProject>_postgres-business-data  (disentuh? TIDAK)
  volume baru  = <NewProject>_postgres-business-data  (fresh)
  Kedua project memakai port host yang sama (5432/5433/8080/9000/3000),
  jadi stack lama HARUS berhenti dulu sebelum stack baru jalan — skrip
  ini menegakkannya lewat urutan langkah.

  Routing file compose (FINAL-GUARDS): SEMUA operasi project LAMA
  memakai file compose ASLI yang membuat volume lama, lewat parameter
  WAJIB -LegacyComposeFile — tanpa fallback ke file compose baru.
  -LegacyComposeFile harus ada (Test-Path) dan valid (`docker compose
  config -q`) SEBELUM perintah mutating apa pun; bila tidak ada/tidak
  valid skrip berhenti. Operasi project BARU memakai -ComposeFile.
  Routing ini hidup di helper bersama (Get-ComposeFile, dipakai
  Invoke-Compose/Invoke-ComposeCapture), bukan di tiap langkah.

  Pemeriksaan inspeksi (FINAL-GUARDS): query `ps -q` yang GAGAL
  (daemon mati, config rusak) TIDAK ditelan menjadi "tidak berjalan".
  Get-ServiceContainerOrNull hanya mengubah output kosong dari query
  yang SUKSES menjadi '' — exception dibiarkan naik supaya quiescence
  gagal tertutup (fail-closed). Service penulis yang memang tidak
  terdefinisi di config lama dibedakan lewat cek eksplisit
  Test-ServiceDefined (`docker compose config --services`), bukan
  lewat menelan error.

  JALUR PEMULIHAN bila gagal sesudah langkah 4: volume lama masih utuh.
  Skrip mencetak perintah pemulihan lengkap saat gagal (lihat
  Get-RecoveryText): hentikan project baru dulu bila ia memakai port,
  lalu jalankan kembali stack LAMA LENGKAP (termasuk business-api,
  supaya penulis bisa menulis lagi) dengan file compose ASLI yang
  membuat volume lama (POSTGRES_USER=umnia_business) — BUKAN file
  compose baru. Path file dalam perintah pemulihan dikutip (aman bila
  path mengandung spasi).

  Skrip ini TIDAK PERNAH menulis/menimpa .env. Satu file .env yang sama
  dipakai kedua project (password bootstrap/runtime harus sama dengan
  yang dipakai stack lama, kalau tidak langkah 0b sudah gagal).

.NOTES
  Sintaks dijaga kompatibel Windows PowerShell 5.1 (tanpa ??, ?:, dan
  tanpa -Encoding utf8NoBOM). Setiap panggilan docker dicek $LASTEXITCODE
  secara eksplisit. PERINGATAN: di Windows PowerShell 5.1 dengan
  $ErrorActionPreference = 'Stop', stderr dari native command BISA
  menjadi terminating error sebelum $LASTEXITCODE dibaca — jadi perintah
  yang hasil negatifnya menulis ke stderr (mis. `docker volume inspect`
  untuk volume yang tidak ada) TIDAK BOLEH dipakai sebagai probe
  "tidak ada". Probe semacam itu harus memakai perintah yang SUKSES
  dengan nol hasil (mis. `docker volume ls -q`) dan membandingkan nama
  secara eksak; kegagalan perintahnya sendiri tetap throw.
#>
[CmdletBinding()]
param(
  [string]$ComposeFile = "deploy/windows/compose.yml",
  [string]$OldProject = "umnia-legacy",
  [string]$NewProject = "umnia",
  [string]$BackupDir = "backups",
  [string]$ApiBase = "http://localhost:8080",
  [string]$SourceDbUser = "umnia_business",
  [string]$SourceDbName = "umnia_business",
  [Parameter(Mandatory=$true)][string]$LegacyComposeFile,
  [switch]$ConfirmMaintenance
)

$ErrorActionPreference = 'Stop'
$script:backupFull = "(belum dibuat)"

function Write-Step($Text) { Write-Host ""; Write-Host "== $Text" }
function Normalize-ProjectName($Name) {
  # Compose menormalkan nama project: huruf kecil, hanya [a-z0-9_-].
  $n = $Name.ToLower() -replace '[^a-z0-9_-]', ''
  return $n.TrimStart('-', '_')
}
function Get-ComposeFile($Project) {
  # Routing: operasi OldProject SELALU memakai file compose ASLI
  # (-LegacyComposeFile); operasi NewProject memakai -ComposeFile.
  # Project yang bukan keduanya adalah kesalahan pemanggil — throw.
  if ($Project -eq $OldProject) { return $LegacyComposeFile }
  if ($Project -eq $NewProject) { return $ComposeFile }
  throw "Project tidak dikenal: $Project (harap '$OldProject' atau '$NewProject')."
}
function Test-ServiceDefined($Project, $Service) {
  # Cek eksplisit apakah service terdefinisi di file compose project
  # itu (untuk penulis OPSIONAL di konfigurasi legacy). Kegagalan
  # `config --services` (konfig rusak/daemon mati) dibiarkan naik —
  # bukan ditelan menjadi "tidak terdefinisi".
  $services = Invoke-ComposeCapture $Project @('config', '--services')
  return (($services -split "`n") | ForEach-Object { $_.Trim() }) -contains $Service
}
function Test-PostgresReady($Project, $User, $Db) {
  # pg_isready via file compose project itu (routing lewat helper).
  # Mengembalikan $true bila server menerima koneksi (ingat: ini hanya
  # liveness — identitas role tetap diverifikasi query SQL terpisah).
  $file = Get-ComposeFile $Project
  & docker compose -f $file -p $Project exec -T postgres-business pg_isready -U $User -d $Db | Out-Null
  return ($LASTEXITCODE -eq 0)
}
function Test-VolumeExists($VolumeName) {
  # JANGAN pakai `docker volume inspect`: volume yang tidak ada menulis
  # ke stderr, dan di Windows PowerShell 5.1 dengan
  # $ErrorActionPreference = 'Stop' stderr native menjadi terminating
  # error sebelum $LASTEXITCODE sempat dibaca — padahal "tidak ada"
  # adalah hasil false yang sah (terbukti QA Windows: preflight abort
  # pada volume target yang memang belum ada). Sebagai gantinya, daftar
  # semua volume (read-only; perintah ini SUKSES dengan nol hasil bila
  # tidak ada volume) dan bandingkan nama secara eksak. Filter `name=`
  # TIDAK dipakai: menurut dokumentasi Docker ia mencocokkan "all or
  # part of a volume's name" (substring), jadi volume bernama mirip
  # bisa salah dikenali. Kegagalan `volume ls` sendiri (daemon mati,
  # konfigurasi rusak) tetap throw — bukan false.
  $names = & docker volume ls -q 2>$null
  if ($LASTEXITCODE -ne 0) {
    throw "docker volume ls gagal (exit $LASTEXITCODE): tidak dapat memastikan keberadaan volume '$VolumeName'. Periksa Docker daemon."
  }
  if ($null -eq $names) { return $false }
  return ((@($names) | ForEach-Object { "$_".Trim() }) -contains $VolumeName)
}
function Get-RecoveryText {
  # -LegacyComposeFile bersifat wajib, jadi tidak ada fallback ke file
  # baru dan tidak ada peringatan — file di bawah SELALU file asli.
  # Path dikutip supaya perintah tetap benar bila mengandung spasi.
  return @"
JALUR PEMULIHAN: volume lama masih utuh (tidak pernah dihapus).
  Backup       : $script:backupFull
  Volume lama  : ${OldProject}_postgres-business-data
  1. Bila project baru sempat jalan dan memakai port host yang sama,
     hentikan dulu (tanpa -v):
       docker compose -f "$ComposeFile" -p $NewProject down
  2. Jalankan kembali stack LAMA LENGKAP (termasuk business-api, supaya
     penulis bisa menulis lagi) dengan file compose ASLI volume lama:
       docker compose -f "$LegacyComposeFile" -p $OldProject up -d
  3. Perbaiki penyebab kegagalan, lalu ulangi skrip ini (file backup
     baru akan dibuat dengan timestamp baru; file lama tidak ditimpa).
"@
}
function Invoke-Compose($Project, $Arguments) {
  # Jalankan `docker compose -f <file> -p <project> <args>` dengan file
  # yang di-routing Get-ComposeFile; throw bila gagal.
  $file = Get-ComposeFile $Project
  $cmd = "docker compose -f $file -p $Project $Arguments"
  Write-Host "  > $cmd"
  & docker compose -f $file -p $Project @Arguments
  if ($LASTEXITCODE -ne 0) {
    throw "Perintah gagal (exit $LASTEXITCODE): $cmd"
  }
}
function Invoke-ComposeCapture($Project, $Arguments) {
  $file = Get-ComposeFile $Project
  $out = & docker compose -f $file -p $Project @Arguments 2>&1
  if ($LASTEXITCODE -ne 0) {
    throw "Perintah gagal (exit $LASTEXITCODE): docker compose -f $file -p $Project $($Arguments -join ' ')"
  }
  return ($out | Out-String).Trim()
}
function Get-ServiceContainer($Project, $Service) {
  $id = Invoke-ComposeCapture $Project @('ps', '-q', $Service)
  if ([string]::IsNullOrWhiteSpace($id)) { throw "Container $Service project $Project tidak ditemukan/berjalan." }
  return $id.Trim()
}
function Get-ServiceContainerOrNull($Project, $Service) {
  # '' HANYA bila `ps -q` SUKSES dengan output kosong (service tidak
  # berjalan). Kegagalan perintah/konfig/daemon TIDAK ditelan: exception
  # dari Invoke-ComposeCapture dibiarkan naik supaya quiescence gagal
  # tertutup (fail-closed) — ketiadaan hasil BUKAN bukti penulis berhenti.
  $id = Invoke-ComposeCapture $Project @('ps', '-q', $Service)
  if ([string]::IsNullOrWhiteSpace($id)) { return '' }
  return $id.Trim()
}
function Invoke-DockerCp($Source, $Destination) {
  Write-Host "  > docker cp $Source $Destination"
  & docker cp $Source $Destination
  if ($LASTEXITCODE -ne 0) { throw "docker cp gagal (exit $LASTEXITCODE): $Source -> $Destination" }
}
function Invoke-SourceSql($Query) {
  return Invoke-ComposeCapture $OldProject @('exec', '-T', 'postgres-business', 'psql', '-U', $SourceDbUser, '-d', $SourceDbName, '-tAc', $Query)
}

try {
  Write-Step "0. Prasyarat + preflight (tanpa perintah mutating)"
  foreach ($tool in @('docker')) {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) { throw "Perintah '$tool' tidak ditemukan di PATH." }
  }
  if (-not (Test-Path $ComposeFile)) { throw "Compose file tidak ditemukan: $ComposeFile" }
  # FINAL-GUARDS: file compose ASLI wajib ada — tanpa fallback ke file baru.
  if (-not (Test-Path $LegacyComposeFile)) { throw "File compose lama tidak ditemukan: $LegacyComposeFile (parameter -LegacyComposeFile WAJIB — beri file compose ASLI yang membuat volume lama)." }
  if (-not (Test-Path ".env")) {
    Write-Warning ".env tidak ditemukan — memakai password default compose. Bila stack lama memakai password kustom, langkah 0b akan gagal; isi .env dulu."
  }

  # MF2: tolak project yang sama (setelah normalisasi nama Compose).
  # Pemeriksaan ini murni string — nol panggilan docker.
  $oldNorm = Normalize-ProjectName $OldProject
  $newNorm = Normalize-ProjectName $NewProject
  if ([string]::IsNullOrWhiteSpace($oldNorm) -or [string]::IsNullOrWhiteSpace($newNorm)) {
    throw "Nama project tidak valid sesudah normalisasi: OldProject='$OldProject' NewProject='$NewProject'."
  }
  if ($oldNorm -eq $newNorm) {
    throw "OldProject dan NewProject sama sesudah normalisasi ('$oldNorm'): upgrade butuh dua project berbeda (volume berbeda). Periksa -OldProject/-NewProject."
  }
  Write-Host "  project lama: $OldProject (normalisasi: $oldNorm)"
  Write-Host "  project baru: $NewProject (normalisasi: $newNorm)"

  # FINAL-GUARDS: file compose lama harus VALID sebelum perintah
  # mutating apa pun (read-only; config rusak = berhenti di sini).
  Invoke-Compose $OldProject @('config', '-q')
  Write-Host "  file compose lama valid: $LegacyComposeFile"

  # MF2: tolak target yang sudah terisi (read-only saja; tidak ada yang
  # dihapus/ditimpa/dibersihkan). Dicek sebelum sumber disentuh.
  $still = Invoke-ComposeCapture $NewProject @('ps', '-q')
  if (-not [string]::IsNullOrWhiteSpace($still)) {
    throw "Target tidak fresh: project $NewProject masih punya container berjalan. Hentikan/hapus project itu secara manual dulu, atau pilih -NewProject lain. Skrip tidak menghapus target."
  }
  foreach ($vol in @("${NewProject}_postgres-business-data", "${NewProject}_postgres-admin-data", "${NewProject}_rustfs-data")) {
    if (Test-VolumeExists $vol) {
      throw "Target tidak fresh: volume $vol sudah ada. Pilih -NewProject lain, atau hapus volume itu secara manual sesudah yakin. Skrip tidak menghapus volume."
    }
  }
  Write-Host "  target fresh: tidak ada container/volume $NewProject"

  # MF1/MF2: pastikan volume sumber benar-benar ada. Volume adalah
  # identitas durable sumber (container bisa berhenti, volume tetap ada).
  # Default -OldProject 'umnia-legacy' BUKAN bukti nama project asli
  # pengguna: bila volume sumber tidak ditemukan, skrip berhenti di sini
  # dan meminta -OldProject yang benar.
  if (-not (Test-VolumeExists "${OldProject}_postgres-business-data")) {
    throw "Sumber tidak ditemukan: volume ${OldProject}_postgres-business-data tidak ada. Periksa -OldProject (default 'umnia-legacy' bukan bukti nama project asli Anda)."
  }
  Write-Host "  sumber: volume ${OldProject}_postgres-business-data ada"

  $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
  if (-not (Test-Path $BackupDir)) { New-Item -ItemType Directory -Force -Path $BackupDir | Out-Null }
  $backupName = "umnia-backup-$stamp.dump"
  $backupPath = Join-Path $BackupDir $backupName
  $backupFull = [System.IO.Path]::GetFullPath($backupPath)
  if (Test-Path $backupFull) { throw "File backup sudah ada (tidak pernah ditimpa): $backupFull" }
  $script:backupFull = $backupFull

  Write-Step "0b. Jalankan postgres lama dan verifikasi identitas sumber"
  $oldCidPre = Get-ServiceContainerOrNull $OldProject 'postgres-business'
  if ([string]::IsNullOrWhiteSpace($oldCidPre)) {
    # Satu-satunya mutasi sebelum dump bila sumber berhenti: start
    # non-destruktif (tidak mengubah data; perlu untuk verifikasi identitas).
    Write-Host "  postgres lama berhenti — start non-destruktif untuk verifikasi identitas"
    Invoke-Compose $OldProject @('up', '-d', 'postgres-business')
  } else {
    Write-Host "  postgres lama sudah berjalan (tidak di-start ulang)"
  }
  $ready = $false
  for ($i = 0; $i -lt 12; $i++) {
    if (Test-PostgresReady $OldProject $SourceDbUser $SourceDbName) { $ready = $true; break }
    Start-Sleep -Seconds 5
  }
  if (-not $ready) { throw "postgres lama tidak ready sebagai $SourceDbUser (periksa -SourceDbUser/-SourceDbName, password .env / log container)." }
  # MF1: identitas cluster aktual via SQL terautentikasi — pg_isready saja
  # tidak memverifikasi kredensial/identitas role.
  $who = Invoke-SourceSql "SELECT current_user;"
  if ($who -ne $SourceDbUser) { throw "Identitas sumber tidak cocok: current_user='$who', harap '$SourceDbUser'. Periksa -SourceDbUser." }
  $wsTable = Invoke-SourceSql "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = 'workspaces';"
  if ($wsTable -ne '1') { throw "Database sumber tidak berisi tabel workspaces (dapat: '$wsTable'). Periksa -SourceDbName / -OldProject." }
  Write-Host "  identitas sumber OK: current_user=$who, tabel workspaces ada"
  # Catat nama volume aktual dari `docker inspect` (bukan asumsi nama).
  $oldCid = Get-ServiceContainer $OldProject 'postgres-business'
  $inspectRaw = & docker inspect $oldCid 2>&1
  if ($LASTEXITCODE -ne 0) { throw "docker inspect gagal untuk container sumber $oldCid." }
  $inspect = ($inspectRaw | Out-String) | ConvertFrom-Json
  $srcVol = $null
  foreach ($m in @($inspect[0].Mounts)) {
    if ($m.Destination -eq '/var/lib/postgresql/data') { $srcVol = $m.Name }
  }
  if ([string]::IsNullOrWhiteSpace($srcVol)) {
    throw "Volume sumber tidak bisa dipastikan dari docker inspect (mount /var/lib/postgresql/data tidak ditemukan)."
  }
  Write-Host "  sumber: container $oldCid, volume $srcVol"

  Write-Step "0c. Maintenance: hentikan penulis sebelum dump"
  if (-not $ConfirmMaintenance) {
    throw "Mode maintenance belum dikonfirmasi: hentikan SEMUA penulis eksternal (klien DB langsung, instance API lain di luar Compose ini) lalu jalankan lagi dengan -ConfirmMaintenance. Skrip hanya bisa menghentikan penulis yang dikelola Compose (business-api, web); penulis langsung ke database adalah tanggung jawab operator."
  }
  # Penulis yang terdefinisi di config lama saja yang dihentikan dan
  # diperiksa. Service yang memang tidak ada di config lama (mis. web)
  # dilewati secara eksplisit — BUKAN lewat menelan error query.
  $writerSvcs = @()
  foreach ($svc in @('business-api', 'web')) {
    if (Test-ServiceDefined $OldProject $svc) { $writerSvcs += $svc }
    else { Write-Host "  penulis tidak terdefinisi di config lama (dilewati): $svc" }
  }
  foreach ($svc in $writerSvcs) {
    $id = Get-ServiceContainerOrNull $OldProject $svc
    if (-not [string]::IsNullOrWhiteSpace($id)) {
      Invoke-Compose $OldProject @('stop', $svc)
      Write-Host "  penulis dihentikan: $svc"
    } else {
      Write-Host "  penulis tidak berjalan (dilewati): $svc"
    }
  }
  # Pemeriksaan observabel: container penulis berhenti + tidak ada sesi
  # penulis aktif di database (diulang sebentar karena koneksi menutup
  # asinkron sesudah stop).
  $quiet = $false
  for ($i = 0; $i -lt 5; $i++) {
    $left = @()
    foreach ($svc in $writerSvcs) { $left += Get-ServiceContainerOrNull $OldProject $svc }
    $anyLeft = @($left | Where-Object { -not [string]::IsNullOrWhiteSpace($_) }).Count -gt 0
    $active = Invoke-SourceSql "SELECT count(*) FROM pg_stat_activity WHERE usename = '$SourceDbUser' AND state = 'active' AND pid <> pg_backend_pid();"
    if (-not $anyLeft -and $active -eq '0') { $quiet = $true; break }
    Start-Sleep -Seconds 2
  }
  if (-not $quiet) { throw "Penulis masih aktif sesudah stop (api/web berjalan atau sesi $SourceDbUser aktif). Jangan dump selagi ada penulis; periksa container dan koneksi database lama." }
  Write-Host "  penulis berhenti: tidak ada sesi aktif $SourceDbUser"

  Write-Step "1. Dump database lama (custom format, --no-owner)"
  Invoke-Compose $OldProject @('exec', '-T', 'postgres-business', 'pg_dump', '-U', $SourceDbUser, '-d', $SourceDbName, '--no-owner', '-Fc', '-f', '/tmp/umnia-backup.dump')

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
    if (Test-PostgresReady $NewProject 'umnia_bootstrap' 'umnia_business') { $ready = $true; break }
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

  Write-Step "8b. Verifikasi API (read-only: /readyz + hitung workspaces)"
  $ready = $false
  for ($i = 0; $i -lt 24; $i++) {
    try {
      $r = Invoke-WebRequest -Uri "$ApiBase/readyz" -UseBasicParsing -TimeoutSec 5
      if ($r.StatusCode -eq 200) { $ready = $true; break }
    } catch { }
    Start-Sleep -Seconds 5
  }
  if (-not $ready) { throw "/readyz tidak 200 sesudah 120 detik." }
  $wsCount = Invoke-ComposeCapture $NewProject @('exec', '-T', 'postgres-business', 'psql', '-U', 'umnia_bootstrap', '-d', 'umnia_business', '-tAc', "SELECT count(*) FROM workspaces;")
  Write-Host "  /readyz 200 OK, workspaces hasil restore: $wsCount (read-only; tidak ada akun/workspace tes yang dibuat)"
  Write-Host "  Uji isolasi tenant (register/login/403) TIDAK dijalankan di sini: jalankan terpisah pada fixture sintetis terisolasi."

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
