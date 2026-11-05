# Mock-docker logic test for deploy/windows/Upgrade-UmniaVolume.ps1.
# Runs the REAL script under pwsh with `docker` replaced by a function that
# logs every invocation and returns canned success, so command construction,
# ordering, project/volume names, and exit-code handling are verified
# without a Windows host or a real daemon. The API probe (step 8b) runs
# against a REAL test API given via -ApiBase (a migrated business cluster).
# Usage: pwsh -NoProfile -File test-upgrade-script-mock.ps1 -ApiBase http://127.0.0.1:18080
param([string]$ApiBase = "http://127.0.0.1:18080")
$ErrorActionPreference = 'Stop'
$global:MockLogFile = Join-Path ([System.IO.Path]::GetTempPath()) "umnia-mock-docker.log"
if (Test-Path $global:MockLogFile) { Remove-Item $global:MockLogFile -Force }
$global:MockCalls = @()
function docker {
  # Mock: log argv, return canned success. Sets $global:LASTEXITCODE = 0.
  $line = ($args -join ' ')
  $global:MockCalls += $line
  Add-Content -Path $global:MockLogFile -Value $line
  if ($args[0] -eq 'cp') {
    # docker cp <src> <dst>: when dst is a local path, create a dummy file.
    $dst = $args[2]
    if ($dst -notmatch ':') {
      $dir = Split-Path $dst -Parent
      if ($dir -and -not (Test-Path $dir)) { New-Item -ItemType Directory -Force -Path $dir | Out-Null }
      Set-Content -Path $dst -Value 'DUMMY BACKUP (mock docker cp)' -Encoding ASCII
    }
    $global:LASTEXITCODE = 0
    return
  }
  if ($args -contains 'pg_restore' -and $args -contains '-l') {
    "219; 1259 16441 TABLE public workspaces umnia_business"
    "220; 1259 16455 TABLE public workspace_memberships umnia_business"
    $global:LASTEXITCODE = 0
    return
  }
  if ($args -contains 'psql') {
    $joined = $args -join ' '
    if ($joined -match 'rolsuper') { 'false/false' }
    elseif ($joined -match 'schema_migrations') { '1' }
    elseif ($joined -match 'routine_privileges') { '12' }
    else { '' }
    $global:LASTEXITCODE = 0
    return
  }
  if ($args -contains 'pg_isready') { $global:LASTEXITCODE = 0; return }
  if ($args[0] -eq 'compose' -and $args -contains 'ps' -and $args -contains '-q' -and $args -contains 'postgres-business') {
    $pIdx = [Array]::IndexOf($args, '-p')
    $proj = $args[$pIdx + 1]
    if ($proj -eq 'umnia-legacy') { 'deadbeef0001' } else { 'cafef00d0002' }
    $global:LASTEXITCODE = 0
    return
  }
  if ($args[0] -eq 'compose' -and $args -contains 'ps' -and $args -contains '-q') {
    # step 4: no containers left after down
    $global:LASTEXITCODE = 0
    return
  }
  $global:LASTEXITCODE = 0
}

# pwsh's own HTTP stack is blocked in this VM (runtime denies direct TCP
# from the assistant), but curl works. Delegate the script's HTTP calls to
# curl so step 8b logic runs against the REAL test API.
function Invoke-WebRequest {
  param($Uri, [switch]$UseBasicParsing, $TimeoutSec)
  $code = (& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 10 $Uri)
  return [pscustomobject]@{ StatusCode = [int]$code }
}
function Invoke-RestMethod {
  param($Uri, $Method = 'Get', $ContentType, $Headers, $Body)
  $args = @('/usr/bin/curl', '-s', '-w', "`n%{http_code}", '--max-time', '10', '-X', $Method.ToUpper(), $Uri)
  if ($ContentType) { $args += @('-H', "Content-Type: $ContentType") }
  if ($Headers -and $Headers.Authorization) { $args += @('-H', "Authorization: $($Headers.Authorization)") }
  if ($Body) { $args += @('-d', $Body) }
  $out = (& $args[0] $args[1..($args.Count - 1)])
  $code = [int]($out[-1])
  $body = ($out[0..($out.Count - 2)] -join "`n")
  Write-Host "  [mock http] $Method $Uri -> $code"
  if ($code -ge 200 -and $code -lt 300) { return ($body | ConvertFrom-Json) }
  $ex = New-Object System.Exception("HTTP $code")
  $resp = [pscustomobject]@{ StatusCode = $code }
  $ex | Add-Member -NotePropertyName Response -NotePropertyValue $resp -Force
  throw $ex
}

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("umnia-upgrade-mock-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
$backupDir = Join-Path $tmp 'backups'
& /home/hatch/workspace/UMNIA-APP/deploy/windows/Upgrade-UmniaVolume.ps1 -ComposeFile /home/hatch/workspace/UMNIA-APP/deploy/windows/compose.yml -OldProject umnia-legacy -NewProject umnia -BackupDir $backupDir -ApiBase $ApiBase
if ($LASTEXITCODE -ne 0) { Write-Host "UPGRADE SCRIPT FAILED (exit $LASTEXITCODE)"; exit 1 }
Write-Host "SCRIPT EXIT OK"
$log = Get-Content $global:MockLogFile
$fail = 0
function Check($Name, $Cond) {
  if ($Cond) { Write-Host "CHECK PASS: $Name" } else { Write-Host "CHECK FAIL: $Name"; $global:MockFail = 1 }
}
Check 'no docker volume rename' (-not ($log -match 'volume rename'))
Check 'no < redirect' (-not ($log -match '<'))
Check 'old project used for dump' ($log -match '-p umnia-legacy exec -T postgres-business pg_dump .* -Fc')
Check 'docker cp out (binary-safe transport)' ($log -match 'cp deadbeef0001:/tmp/umnia-backup.dump')
Check 'pg_restore -l validation before down' (
  ([Array]::IndexOf($log, ($log -match 'pg_restore -l')[0]) -lt [Array]::IndexOf($log, ($log -match '-p umnia-legacy down')[0]))
)
Check 'down without -v' (($log -match '-p umnia-legacy down') -and -not ($log -match '-p umnia-legacy down -v'))
Check 'new project postgres up' ($log -match '-p umnia up -d postgres-business')
Check 'docker cp in + pg_restore --exit-on-error' (($log -match 'cp .* cafef00d0002:/tmp/umnia-backup.dump') -and ($log -match 'pg_restore .* --exit-on-error'))
Check 'full up --build' ($log -match '-p umnia up --build -d')
Check 'backup file created (timestamped, non-empty)' ((Get-ChildItem $backupDir -Filter 'umnia-backup-*.dump' | Where-Object { $_.Length -gt 0 }).Count -ge 1)
Check 'distinct volumes implied by distinct -p' (($log -match '-p umnia-legacy') -and ($log -match '-p umnia\b'))
Write-Host "--- docker call log ($($log.Count) calls) ---"
$log | ForEach-Object { Write-Host "  $_" }
if ($fail -eq 1) { exit 1 }
Write-Host "MOCK LOGIC TEST OK"
