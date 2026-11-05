# test-upgrade-scenarios.ps1 — regression scenarios for Upgrade-UmniaVolume.ps1
# against the FAITHFUL docker mock (mock-docker-faithful.ps1). Each scenario
# runs the REAL script (baseline or fixed) against REAL clusters/files/API.
# Lives in deploy/windows/tests/; paths are derived from its own location.
# Usage:
#   pwsh -NoProfile -File test-upgrade-scenarios.ps1 -Scenario <name|all> `
#     -ScriptPath <path-to-Upgrade-UmniaVolume.ps1>
# Scenarios: baseline-mf1, baseline-mf1-routing, baseline-mf2-equal,
#   baseline-mf2-existing-target, baseline-mf3-writer, baseline-mf4-probe,
#   fixed-happy, fixed-negative, fixed-failure-after-down,
#   fixed-stopped-source, fixed-stopped-source-recovery,
#   fixed-inspection-failure, fixed-missing-writer, fixed-isolated-api
# Exit 0 only if every CHECK in the selected scenarios passes.
param(
  [string]$Scenario = "all",
  [string]$ScriptPath = ""
)
$ErrorActionPreference = 'Stop'
$ReviewDir = $PSScriptRoot
$Repo = (Resolve-Path "$ReviewDir/../../..").Path
if ([string]::IsNullOrWhiteSpace($ScriptPath)) { $ScriptPath = "$Repo/deploy/windows/Upgrade-UmniaVolume.ps1" }
$Harness = "/var/tmp/umnia-harness"
$Rootfs = "$Harness/rootfs"
$global:Fail = 0
$global:Checks = 0

function Ensure-ApiBinary {
  # The mock starts the real business-api binary; build it once from the
  # repo when missing (Go from PATH, else the dev-VM toolchain location).
  $bin = "$ReviewDir/bin/umnia-api"
  if (Test-Path $bin) { return }
  $go = $null
  foreach ($cand in @('go', "$HOME/workspace/tools/go/bin/go")) {
    if (Get-Command $cand -ErrorAction SilentlyContinue) { $go = $cand; break }
  }
  if (-not $go) { throw "go toolchain not found (PATH or ~/workspace/tools/go/bin/go); build the API binary manually to $bin" }
  Write-Host "Building business-api -> $bin"
  Push-Location "$Repo/services/business-api"
  try { & $go build -o $bin ./... } finally { Pop-Location }
  if ($LASTEXITCODE -ne 0 -or -not (Test-Path $bin)) { throw "go build failed for business-api" }
}
Ensure-ApiBinary

function Check($Name, $Cond) {
  $global:Checks++
  if ($Cond) { Write-Host "CHECK PASS: $Name" }
  else { Write-Host "CHECK FAIL: $Name"; $global:Fail = 1 }
}
function New-ProjectState($ProjectName, $Port, $PgData, $Cid, $Staging, $BootstrapUser, $BootstrapPw, $RuntimeUser, $RuntimePw, $ApiPort, $WebRunning, $ApiName) {
  return @{
    Name = $ProjectName; ProjectName = $ProjectName; Port = $Port; Db = 'umnia_business'
    PgData = $PgData; Cid = $Cid; Staging = $Staging
    BootstrapUser = $BootstrapUser; BootstrapPw = $BootstrapPw
    RuntimeUser = $RuntimeUser; RuntimePw = $RuntimePw
    ApiPort = $ApiPort; ApiPidFile = "$Harness/api-$ApiName.pid"
    ApiLog = "$Harness/api-$ApiName.log"; ApiLogErr = "$Harness/api-$ApiName.err"
    PgLog = "$Harness/entrypoint-$ProjectName.log"; WebRunning = $WebRunning
    # Services defined by this project's compose file (drives
    # `config --services` and the no-such-service behaviour of `ps -q`).
    Services = @('postgres-business', 'business-api', 'web')
  }
}
function New-State {
  $legacy = New-ProjectState 'legacy-old' 55436 "$Harness/pgdata-legacy-old" 'deadbeef0001' "$Harness/staging-legacy" 'umnia_business' 'legacy-bootstrap-only' 'umnia_business' 'legacy-bootstrap-only' 18080 $true 'legacy'
  $new = New-ProjectState 'upgraded-new' 55437 "$Harness/pgdata-upgraded-new" 'cafef00d0002' "$Harness/staging-new" 'umnia_bootstrap' 'bootstrap-only' 'umnia_business' 'runtime-only' 18081 $false 'new'
  $fixedsrc = New-ProjectState 'fixedsrc' 55438 "$Harness/pgdata-fixedsrc" 'ab12cd340003' "$Harness/staging-fixedsrc" 'umnia_bootstrap' 'bootstrap-only' 'umnia_business' 'runtime-only' 18082 $false 'fixedsrc'
  # NOTE: mock project keys are the Compose project names used on the CLI.
  $projects = @{ 'umnia-legacy' = $legacy; 'umnia' = $new; 'umnia-fixedsrc' = $fixedsrc }
  $volumes = @{
    'umnia-legacy_postgres-business-data' = $legacy.PgData
    'umnia_postgres-business-data' = $new.PgData
    'umnia_postgres-admin-data' = "$Harness/pgdata-upgraded-new-admin"
    'umnia-legacy_postgres-admin-data' = "$Harness/pgdata-legacy-old-admin"
    'umnia_postgres-rustfs-data' = "$Harness/pgdata-upgraded-new-rustfs"
    'umnia-fixedsrc_postgres-business-data' = $fixedsrc.PgData
  }
  $cids = @{ 'deadbeef0001' = $legacy; 'cafef00d0002' = $new; 'ab12cd340003' = $fixedsrc }
  return @{
    Harness = $Harness; Rootfs = $Rootfs; LdPath = "$Rootfs/lib/x86_64-linux-gnu:$Rootfs/usr/lib/x86_64-linux-gnu"
    ApiBinary = "$ReviewDir/bin/umnia-api"; Migrations = "$Repo/services/business-api/migrations"
    Repo = $Repo; RunEntrypoint = "$Repo/deploy/windows/tests/run-entrypoint.sh"
    Projects = $projects; Volumes = $volumes; Cids = $cids; FailRestore = $false
    # Fault injection (test only): FailPs 'project/service' makes that
    # `ps -q` query fail (daemon/config error simulation); FailConfig
    # 'project' makes that project's `config` commands fail (malformed
    # compose file simulation). Both are '' when disabled.
    FailPs = ''; FailConfig = ''
  }
}
function Invoke-Sql($Port, $User, $Query) {
  $bin = Join-Path $Rootfs "usr/lib/postgresql/16/bin/psql"
  $env:LD_LIBRARY_PATH = "$Rootfs/lib/x86_64-linux-gnu:$Rootfs/usr/lib/x86_64-linux-gnu"
  $out = & $bin -h /var/run/postgresql -p $Port -U $User -d umnia_business -tAc $Query 2>&1
  return ($out | Out-String).Trim()
}
function Get-MutatingCalls($Log) {
  # Mutating docker calls: anything that creates, starts, stops, deletes,
  # copies, dumps or restores. Read-only: ps, volume ls/inspect, inspect,
  # pg_isready, psql SELECT, pg_restore -l.
  return @($Log | Where-Object {
    $_ -match ' up[ -]' -or $_ -match ' down(\s|$)' -or $_ -match ' stop ' -or
    $_ -match '^cp ' -or $_ -match ' pg_dump' -or
    ($_ -match ' pg_restore' -and $_ -notmatch 'pg_restore -l') -or
    $_ -match ' rm ' -or $_ -match ' volume rm'
  })
}
function Get-DumpTimeMs {
  # The mock records the first pg_dump call time in $global:DumpTime.
  if ($global:DumpTime) { return [int64]([datetimeoffset]$global:DumpTime).ToUnixTimeMilliseconds() }
  return $null
}
function Start-Writer($ApiBase, $LogFile, $Prefix, $Interval = '0.5') {
  $p = Start-Process -FilePath 'bash' -ArgumentList @("$ReviewDir/writer.sh", $ApiBase, $LogFile, $Prefix, $Interval) -PassThru -NoNewWindow
  Start-Sleep -Seconds 3
  return $p
}
function Stop-Writer($Proc) {
  if ($Proc -and -not $Proc.HasExited) { Stop-Process -Id $Proc.Id -Force -ErrorAction SilentlyContinue }
}
function Invoke-Upgrade($ArgHash, $OutLog) {
  $global:DockerLog = @(); $global:HttpLog = @()
  $global:DumpTime = $null
  # *>&1 captures Write-Host (information stream) too, so the log file
  # exists even when the script fails at preflight with no stdout.
  # Tee-Object passes objects through, so pipe to Out-Null: otherwise the
  # function's return value would be the whole script output + the code,
  # and `$code -eq 0` on that array is falsy (single-element @(0)).
  & $global:RunScript @ArgHash *>&1 | Tee-Object -FilePath $OutLog | Out-Null
  return $LASTEXITCODE
}

function Reset-Env([string[]]$Commands) {
  foreach ($c in $Commands) {
    & bash "$ReviewDir/manage-test-env.sh" $c | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "manage-test-env.sh $c failed (exit $LASTEXITCODE)" }
  }
}
function New-BackupDir($Scenario) {
  $d = Join-Path ([System.IO.Path]::GetTempPath()) ("umnia-upg-$Scenario-" + [Guid]::NewGuid().ToString('N').Substring(0, 8))
  New-Item -ItemType Directory -Force -Path $d | Out-Null
  return Join-Path $d 'backups'
}
function New-LegacyComposeCopy($Tag) {
  # A distinct copy of the repo compose file at a path CONTAINING SPACES,
  # standing in for the user's original legacy compose file. Same content
  # as the file the fixture was built with; a different path, so -f
  # routing assertions can tell source ops from target ops.
  $dir = Join-Path ([System.IO.Path]::GetTempPath()) "umnia legacy/$Tag"
  New-Item -ItemType Directory -Force -Path $dir | Out-Null
  $dst = Join-Path $dir 'compose.yml'
  Copy-Item "$Repo/deploy/windows/compose.yml" $dst -Force
  return $dst
}
function Test-Routing($LegacyFile, $NewFile) {
  # Assert actual argv: every compose call for the OLD project used the
  # legacy file, every call for the NEW project used the new file.
  # Returns the number of routing violations found.
  $bad = 0
  foreach ($line in $global:DockerLog) {
    if ($line -match 'compose -f ') {
      if ($line -match '-p umnia-legacy ') {
        if ($line -notmatch [regex]::Escape("-f $LegacyFile")) { Write-Host "  ROUTING VIOLATION (source): $line"; $bad++ }
      } elseif ($line -match '-p umnia ') {
        if ($line -notmatch [regex]::Escape("-f $NewFile")) { Write-Host "  ROUTING VIOLATION (target): $line"; $bad++ }
      }
    }
  }
  return $bad
}
function Invoke-ScenarioBaselineMf1 {
  Write-Host "`n=== baseline-mf1: wrong source account (MF1 repro) ==="
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $backupDir = New-BackupDir 'mf1'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf1-output.log'
  $global:RunScript = "$ReviewDir/baseline-Upgrade-UmniaVolume.ps1"
  # Staging is shared across runs: remove any dump left by an earlier
  # scenario, or the "no dump produced" check would read a stale file.
  $legacyStaging = $global:UpgradeTestState.Projects['umnia-legacy'].Staging
  Remove-Item (Join-Path $legacyStaging 'umnia-backup.dump') -ErrorAction SilentlyContinue
  $code = Invoke-Upgrade @{ ComposeFile="$Repo/deploy/windows/compose.yml"; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081' } $outLog
  $out = Get-Content $outLog -Raw
  Check 'MF1 repro: script fails on legacy source' ($code -ne 0)
  Check 'MF1 repro: readiness used the wrong source account (umnia_bootstrap)' ($global:DockerLog -match 'pg_isready -U umnia_bootstrap')
  # pg_isready does NOT verify the role: measured directly, it returns 0
  # ("accepting connections") for the nonexistent umnia_bootstrap on the
  # legacy cluster. The baseline therefore passes its readiness loop and
  # fails at pg_dump with FATAL: role "umnia_bootstrap" does not exist.
  # The repro is that failure + no dump file, not the readiness loop.
  Check 'MF1 repro: pg_dump used the wrong account and failed' (($global:DockerLog -match 'pg_dump -U umnia_bootstrap') -and ($out -match 'role "umnia_bootstrap" does not exist'))
  # pg_dump creates the -f output file (0 byte) before connecting, so a
  # failed dump leaves an empty file: assert no non-empty dump instead.
  $dumpFile = Join-Path $legacyStaging 'umnia-backup.dump'
  $dumpBytes = if (Test-Path $dumpFile) { (Get-Item $dumpFile).Length } else { 0 }
  Check "MF1 repro: no dump data was produced (bytes=$dumpBytes)" ($dumpBytes -eq 0)
  Check 'MF1 repro: legacy fixture intact (1 workspace)' ((Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM workspaces;") -eq '1')
  Check 'MF1 repro: legacy API still running' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18080/readyz') -eq '200')
}
function Invoke-ScenarioBaselineMf2Equal {
  Write-Host "`n=== baseline-mf2-equal: equal projects accepted (MF2 repro) ==="
  Reset-Env 'reset-fixedsrc','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $backupDir = New-BackupDir 'mf2eq'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf2eq-output.log'
  $global:RunScript = "$ReviewDir/baseline-Upgrade-UmniaVolume.ps1"
  $code = Invoke-Upgrade @{ ComposeFile="$Repo/deploy/windows/compose.yml"; OldProject='umnia-fixedsrc'; NewProject='umnia-fixedsrc'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18082' } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'MF2 repro: script does NOT reject equal projects' (($global:DockerLog -match 'pg_dump') -or ($global:DockerLog -match ' down'))
  Check 'MF2 repro: mutating calls happened before any rejection' ($mut.Count -ge 1)
  Write-Host "  mutating calls: $($mut.Count) (e.g. $($mut[0]))"
}
function Invoke-ScenarioBaselineMf2ExistingTarget {
  Write-Host "`n=== baseline-mf2-existing-target: occupied target not checked (MF2 repro) ==="
  Reset-Env 'reset-fixedsrc','reset-target-marker'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $backupDir = New-BackupDir 'mf2tgt'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf2tgt-output.log'
  $global:RunScript = "$ReviewDir/baseline-Upgrade-UmniaVolume.ps1"
  $code = Invoke-Upgrade @{ ComposeFile="$Repo/deploy/windows/compose.yml"; OldProject='umnia-fixedsrc'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081' } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'MF2 repro: script shut the SOURCE down despite occupied target' ($global:DockerLog -match '-p umnia-fixedsrc down')
  Check 'MF2 repro: mutating calls happened before any target check' ($mut.Count -ge 1)
  Check 'MF2 repro: target marker workspace still present' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name='Toko Target Lama';") -eq '1')
  # Restore the source the baseline script stopped, so later scenarios start clean.
  Reset-Env 'reset-fixedsrc'
}
function Invoke-ScenarioBaselineMf3Writer {
  Write-Host "`n=== baseline-mf3-writer: writes lost between dump and cutover (MF3 repro) ==="
  Reset-Env 'reset-fixedsrc','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $writerLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf3-writer.log'
  # Baseline counts BEFORE the writer starts (same boundary-race note
  # as fixed-happy: writer.sh timestamps AFTER curl returns).
  $n0Ms = [int64]([datetimeoffset](Get-Date)).ToUnixTimeMilliseconds()
  $n0 = [int](Invoke-Sql 55438 'umnia_bootstrap' "SELECT count(*) FROM workspaces;")
  $writer = Start-Writer 'http://127.0.0.1:18082' $writerLog 'writer-mf3' '0.1'
  $backupDir = New-BackupDir 'mf3'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf3-output.log'
  $global:RunScript = "$ReviewDir/baseline-Upgrade-UmniaVolume.ps1"
  $code = Invoke-Upgrade @{ ComposeFile="$Repo/deploy/windows/compose.yml"; OldProject='umnia-fixedsrc'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081' } $outLog
  Stop-Writer $writer
  $dumpMs = Get-DumpTimeMs
  $writes = @(Get-Content $writerLog | Where-Object { $_ -match '^\d+ 201 ' })
  $between = @($writes | Where-Object { $ts = [int64]($_ -split ' ')[0]; $ts -gt $n0Ms -and $ts -le $dumpMs })
  $afterDump = @($writes | Where-Object { [int64]($_ -split ' ')[0] -gt $dumpMs })
  $nTarget = [int](Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name NOT LIKE 'Toko Upgrade %';")
  $expected = $n0 + $between.Count
  Check 'MF3 repro: writer acknowledged writes AFTER the dump snapshot' ($afterDump.Count -ge 1)
  Check "MF3 repro: target is missing acknowledged writes (target=$nTarget expected=$expected, after-dump lost=$($afterDump.Count))" ($nTarget -eq $expected -and $afterDump.Count -ge 1)
  Write-Host "  writes n0->dump: $($between.Count), after dump: $($afterDump.Count), target=$nTarget expected=$expected"
  Reset-Env 'reset-fixedsrc'
}
function Invoke-ScenarioBaselineMf4Probe {
  Write-Host "`n=== baseline-mf4-probe: production upgrade creates test accounts (MF4 repro) ==="
  Reset-Env 'reset-fixedsrc','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $backupDir = New-BackupDir 'mf4'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf4-output.log'
  $global:RunScript = "$ReviewDir/baseline-Upgrade-UmniaVolume.ps1"
  $code = Invoke-Upgrade @{ ComposeFile="$Repo/deploy/windows/compose.yml"; OldProject='umnia-fixedsrc'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081' } $outLog
  Check 'MF4 repro: baseline upgrade itself succeeds' ($code -eq 0)
  $synthSubjects = [int](Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM identity_subjects WHERE email LIKE '%-upgrade-%';")
  $synthWs = [int](Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name LIKE 'Toko Upgrade %';")
  Check "MF4 repro: synthetic accounts created in restored DB (subjects=$synthSubjects)" ($synthSubjects -eq 2)
  Check "MF4 repro: synthetic workspace created in restored DB (ws=$synthWs)" ($synthWs -eq 1)
  $mutHttp = @($global:HttpLog | Where-Object { $_ -match '^(Post|Put|Patch|Delete) ' })
  Check 'MF4 repro: upgrade path sent mutating HTTP requests' ($mutHttp.Count -ge 3)
  Reset-Env 'reset-fixedsrc'
}

function Invoke-ScenarioFixedHappy {
  Write-Host "`n=== fixed-happy: legacy upgrade with quiesce + correct source identity ==="
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $writerLog = Join-Path ([System.IO.Path]::GetTempPath()) 'happy-writer.log'
  # Baseline counts BEFORE the writer starts: writer.sh logs the
  # timestamp AFTER curl returns, so a write that committed just before
  # n0Ms could carry a log timestamp after n0Ms and be counted twice
  # (once in n0, once in wPre). Reading n0 first makes the split exact.
  $n0Ms = [int64]([datetimeoffset](Get-Date)).ToUnixTimeMilliseconds()
  $n0 = [int](Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM workspaces;")
  $s0 = [int](Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM identity_subjects;")
  $writer = Start-Writer 'http://127.0.0.1:18080' $writerLog 'writer-happy'
  Start-Sleep -Seconds 2
  $backupDir = New-BackupDir 'happy'
  $legacyFile = New-LegacyComposeCopy 'happy'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'happy-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  Stop-Writer $writer
  $out = Get-Content $outLog -Raw
  Check 'fixed happy: script exits 0' ($code -eq 0)
  Check 'fixed happy: source ops used the legacy file, target ops the new file' ((Test-Routing $legacyFile $newFile) -eq 0)
  Check 'fixed happy: dump used the legacy source identity' ($global:DockerLog -match 'pg_dump -U umnia_business -d umnia_business')
  Check 'fixed happy: no umnia_bootstrap on the source path' (-not ($global:DockerLog -match '-p umnia-legacy exec .* -U umnia_bootstrap'))
  Check 'fixed happy: writers stopped before dump' (
    ([Array]::IndexOf($global:DockerLog, ($global:DockerLog -match 'stop business-api')[0]) -lt [Array]::IndexOf($global:DockerLog, ($global:DockerLog -match 'pg_dump')[0]))
  )
  $dumpMs = Get-DumpTimeMs
  $writes = @(Get-Content $writerLog | Where-Object { $_ -match '^\d+ 201 ' })
  $afterDump = @($writes | Where-Object { [int64]($_ -split ' ')[0] -gt $dumpMs })
  $wPre = @($writes | Where-Object { $ts = [int64]($_ -split ' ')[0]; $ts -gt $n0Ms -and $ts -le $dumpMs }).Count
  $nTarget = [int](Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name NOT LIKE 'Toko Upgrade %';")
  # The writer registers exactly one account (writer-happy@contoh.id)
  # as part of the controlled-writer fixture; it is a legitimate
  # pre-dump write, not a synthetic account created by the upgrade.
  $sTargetReal = [int](Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM identity_subjects WHERE email NOT LIKE 'writer-happy%';")
  Check "fixed happy: no acknowledged write after dump (after=$($afterDump.Count))" ($afterDump.Count -eq 0)
  Check "fixed happy: target preserves all pre-dump writes (target=$nTarget expected=$($n0 + $wPre))" ($nTarget -eq ($n0 + $wPre))
  Check "fixed happy: no synthetic accounts added (subjects=$sTargetReal expected=$s0, writer account excluded)" ($sTargetReal -eq $s0)
  Check 'fixed happy: fixture workspace preserved' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name='Toko Lama';") -eq '1')
  Check 'fixed happy: role f/f' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT rolsuper::text || '/' || rolbypassrls::text FROM pg_roles WHERE rolname='umnia_business';") -eq 'false/false')
  Check 'fixed happy: 002_identity recorded' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM schema_migrations WHERE version='002_identity';") -eq '1')
  Check 'fixed happy: EXECUTE grants 12' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM information_schema.routine_privileges WHERE routine_schema='app' AND routine_name IN ('accept_invite','has_permission','is_member','register_subject','session_subject','subject_by_email') AND grantee IN ('postgres','umnia_business');") -eq '12')
  $mutHttp = @($global:HttpLog | Where-Object { $_ -match '^(Post|Put|Patch|Delete) ' })
  Check 'fixed happy: upgrade sent no mutating HTTP request' ($mutHttp.Count -eq 0)
  Check 'fixed happy: new API readyz 200' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18081/readyz') -eq '200')
}
function Invoke-ScenarioFixedNegative {
  Write-Host "`n=== fixed-negative: preflight rejects equal/occupied/wrong-source ==="
  $legacyFile = New-LegacyComposeCopy 'neg'
  $newFile = "$Repo/deploy/windows/compose.yml"
  # (a) equal projects: zero docker calls at all.
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'neg-eq-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia'; NewProject='umnia'; BackupDir=(New-BackupDir 'negeq'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  Check 'fixed negative: equal projects rejected' ($code -ne 0)
  Check 'fixed negative: equal projects produced ZERO docker calls' ($global:DockerLog.Count -eq 0)
  # (b) existing target: zero mutating docker calls.
  Reset-Env 'rebuild-legacy','reset-target-marker'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'neg-tgt-output.log'
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'negtgt'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed negative: occupied target rejected' ($code -ne 0)
  Check 'fixed negative: occupied target produced ZERO mutating docker calls' ($mut.Count -eq 0)
  Check 'fixed negative: target marker untouched' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name='Toko Target Lama';") -eq '1')
  Check 'fixed negative: legacy API still running' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18080/readyz') -eq '200')
  # (b2) same occupied target, but containers STOPPED: the volume check
  # must still reject (fresh-target means no volume either).
  Reset-Env 'stop-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'neg-tgt2-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'negtgt2'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $out2 = Get-Content $outLog -Raw
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed negative: occupied volume rejected with containers stopped' ($code -ne 0)
  Check 'fixed negative: volume rejection names the volume' ($out2 -match 'postgres-business-data.*sudah ada')
  Check 'fixed negative: volume rejection produced ZERO mutating docker calls' ($mut.Count -eq 0)
  # (c) wrong source identity: zero mutating docker calls.
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'neg-src-output.log'
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'negsrc'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile; SourceDbUser='umnia_bootstrap' } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed negative: wrong source identity rejected' ($code -ne 0)
  Check 'fixed negative: source ops used the legacy file, target ops the new file' ((Test-Routing $legacyFile $newFile) -eq 0)
  Check 'fixed negative: wrong source produced ZERO mutating docker calls' ($mut.Count -eq 0)
  Check 'fixed negative: no dump on wrong source' (-not ($global:DockerLog -match 'pg_dump'))
  Check 'fixed negative: legacy fixture intact' ((Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM workspaces;") -eq '1')
}
function Invoke-ScenarioFixedFailureAfterDown {
  Write-Host "`n=== fixed-failure-after-down: restore fails, recovery restores writer state ==="
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.FailRestore = $true
  . "$ReviewDir/mock-docker-faithful.ps1"
  $writerLog = Join-Path ([System.IO.Path]::GetTempPath()) 'faildown-writer.log'
  $writer = Start-Writer 'http://127.0.0.1:18080' $writerLog 'writer-faildown'
  Start-Sleep -Seconds 2
  $backupDir = New-BackupDir 'faildown'
  $legacyFile = New-LegacyComposeCopy 'faildown'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'faildown-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  Stop-Writer $writer
  $out = Get-Content $outLog -Raw
  Check 'fixed failure: script exits non-zero on restore failure' ($code -ne 0)
  Check 'fixed failure: source was stopped (down happened before failure)' ($global:DockerLog -match '-p umnia-legacy down')
  Check 'fixed failure: recovery text names the legacy compose file' ($out -match 'file compose ASLI volume lama')
  Check 'fixed failure: recovery quotes the legacy path with spaces' ($out -match [regex]::Escape("-f `"$legacyFile`""))
  Check 'fixed failure: source ops used the legacy file, target ops the new file' ((Test-Routing $legacyFile $newFile) -eq 0)
  Check 'fixed failure: recovery text tells to stop the new project first' ($out -match '-p umnia down')
  Check 'fixed failure: recovery text restarts the full old stack (writers)' ($out -match "-p umnia-legacy up -d")
  # Execute the recovery path the script printed: stop new, start old (full).
  docker compose -f $newFile -p umnia down | Out-Null
  docker compose -f $legacyFile -p umnia-legacy up -d | Out-Null
  Check 'fixed failure: legacy API responds after recovery' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18080/readyz') -eq '200')
  Check 'fixed failure: legacy fixture intact after recovery' ((Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM workspaces WHERE name='Toko Lama';") -eq '1')
  $t = & /usr/bin/curl -s -X POST 'http://127.0.0.1:18080/auth/register' -H 'Content-Type: application/json' -d '{"email":"after-recovery@contoh.id","password":"RecoveryPass123"}'
  $tok = ($t | ConvertFrom-Json).token
  if (-not $tok) {
    $t = & /usr/bin/curl -s -X POST 'http://127.0.0.1:18080/auth/login' -H 'Content-Type: application/json' -d '{"email":"after-recovery@contoh.id","password":"RecoveryPass123"}'
    $tok = ($t | ConvertFrom-Json).token
  }
  $wcode = (& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 -X POST 'http://127.0.0.1:18080/workspaces' -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' -d '{"name":"Toko Sesudah Pemulihan"}')
  Check 'fixed failure: writer state restored (new write acknowledged after recovery)' ($wcode -eq '201')
}
function Invoke-ScenarioBaselineMf1Routing {
  Write-Host "`n=== baseline-mf1-routing: stopped source starts with the NEW config (MF1 repro) ==="
  Reset-Env 'rebuild-legacy','reset-target','stop-legacy'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'mf1routing'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $backupDir = New-BackupDir 'mf1routing'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'mf1routing-output.log'
  $global:RunScript = "$ReviewDir/baseline-eb25a8f-Upgrade-UmniaVolume.ps1"
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $upLine = @($global:DockerLog | Where-Object { $_ -match ' up -d postgres-business' })[0]
  Check 'MF1 repro: stopped source is started at all' ($null -ne $upLine)
  Check 'MF1 repro: source start used the NEW compose file (not the legacy one)' (($null -ne $upLine) -and ($upLine -match [regex]::Escape("-f $newFile")) -and ($upLine -notmatch [regex]::Escape("-f $legacyFile")))
  Check 'MF1 repro: source readiness used the NEW compose file' ((@($global:DockerLog | Where-Object { $_ -match 'pg_isready' }) | Where-Object { $_ -match [regex]::Escape("-f $newFile") }).Count -ge 1)
}
function Invoke-ScenarioFixedStoppedSource {
  Write-Host "`n=== fixed-stopped-source: stopped source upgrades via the ORIGINAL config ==="
  Reset-Env 'rebuild-legacy','reset-target','stop-legacy'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'stopsrc'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $backupDir = New-BackupDir 'stopsrc'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'stopsrc-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  Check 'fixed stopped-source: script exits 0' ($code -eq 0)
  Check 'fixed stopped-source: source start used the ORIGINAL compose file' ((@($global:DockerLog | Where-Object { $_ -match ' up -d postgres-business' }) | Where-Object { $_ -match [regex]::Escape("-f $legacyFile") }).Count -ge 1)
  Check 'fixed stopped-source: every source op used the legacy file, every target op the new file' ((Test-Routing $legacyFile $newFile) -eq 0)
  Check 'fixed stopped-source: dump used the legacy source identity' ($global:DockerLog -match 'pg_dump -U umnia_business -d umnia_business')
  Check 'fixed stopped-source: fixture workspace preserved' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name='Toko Lama';") -eq '1')
  Check 'fixed stopped-source: new API readyz 200' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18081/readyz') -eq '200')
}
function Invoke-ScenarioFixedStoppedSourceRecovery {
  Write-Host "`n=== fixed-stopped-source-recovery: restore failure, recovery uses the ORIGINAL config (with spaces) ==="
  Reset-Env 'rebuild-legacy','reset-target','stop-legacy'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.FailRestore = $true
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'stopsrcrec'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $backupDir = New-BackupDir 'stopsrcrec'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'stopsrcrec-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $out = Get-Content $outLog -Raw
  Check 'fixed stopped-source-recovery: script exits non-zero on restore failure' ($code -ne 0)
  Check 'fixed stopped-source-recovery: source was started via the ORIGINAL file' ((@($global:DockerLog | Where-Object { $_ -match ' up -d postgres-business' }) | Where-Object { $_ -match [regex]::Escape("-f $legacyFile") }).Count -ge 1)
  Check 'fixed stopped-source-recovery: recovery quotes the legacy path with spaces' (($out -match [regex]::Escape("-f `"$legacyFile`"")) -and ($out -match '-p umnia-legacy up -d'))
  Check 'fixed stopped-source-recovery: recovery stops the new project first' ($out -match '-p umnia down')
  # Execute the printed recovery via the mock and prove the source comes back.
  docker compose -f $newFile -p umnia down | Out-Null
  docker compose -f $legacyFile -p umnia-legacy up -d | Out-Null
  Check 'fixed stopped-source-recovery: legacy API responds after recovery' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18080/readyz') -eq '200')
  Check 'fixed stopped-source-recovery: legacy fixture intact after recovery' ((Invoke-Sql 55436 'umnia_business' "SELECT count(*) FROM workspaces WHERE name='Toko Lama';") -eq '1')
}
function Invoke-ScenarioFixedInspectionFailure {
  Write-Host "`n=== fixed-inspection-failure: inspection errors fail closed (MF2) ==="
  $newFile = "$Repo/deploy/windows/compose.yml"
  # (a) source-container query error: ps -q postgres-business fails in 0b.
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.FailPs = 'umnia-legacy/postgres-business'
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'insp-a'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'insp-a-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'inspa'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed inspection: source query error aborts' ($code -ne 0)
  Check 'fixed inspection: no dump after source query error' (-not ($global:DockerLog -match 'pg_dump'))
  Check 'fixed inspection: no source shutdown after source query error' (-not ($global:DockerLog -match '-p umnia-legacy down'))
  Check 'fixed inspection: zero mutating calls after source query error' ($mut.Count -eq 0)
  # (b) writer-status query error: ps -q business-api fails in 0c.
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.FailPs = 'umnia-legacy/business-api'
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'insp-b'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'insp-b-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'inspb'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed inspection: writer query error aborts' ($code -ne 0)
  Check 'fixed inspection: no dump after writer query error' (-not ($global:DockerLog -match 'pg_dump'))
  Check 'fixed inspection: no source shutdown after writer query error' (-not ($global:DockerLog -match '-p umnia-legacy down'))
  Check 'fixed inspection: zero mutating calls after writer query error' ($mut.Count -eq 0)
  # (c) malformed legacy config: config -q fails at the precondition.
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.FailConfig = 'umnia-legacy'
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'insp-c'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'insp-c-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'inspc'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  $out = Get-Content $outLog -Raw
  $mut = Get-MutatingCalls $global:DockerLog
  Check 'fixed inspection: malformed legacy config aborts' ($code -ne 0)
  Check 'fixed inspection: malformed config fails before any mutating command' ($mut.Count -eq 0)
  Check 'fixed inspection: malformed config error names the legacy file' ($out -match [regex]::Escape($legacyFile))
  # (d) missing legacy file: Test-Path fails before ANY docker call.
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  . "$ReviewDir/mock-docker-faithful.ps1"
  $missingFile = Join-Path ([System.IO.Path]::GetTempPath()) 'umnia legacy/does-not-exist/compose.yml'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'insp-d-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=(New-BackupDir 'inspd'); ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$missingFile } $outLog
  $out = Get-Content $outLog -Raw
  Check 'fixed inspection: missing legacy file aborts' ($code -ne 0)
  Check 'fixed inspection: missing legacy file fails with ZERO docker calls' ($global:DockerLog.Count -eq 0)
  Check 'fixed inspection: missing legacy file error names the file' ($out -match [regex]::Escape($missingFile))
}
function Invoke-ScenarioFixedMissingWriter {
  Write-Host "`n=== fixed-missing-writer: legacy config without web still upgrades ==="
  Reset-Env 'rebuild-legacy','reset-target'
  $global:UpgradeTestState = New-State
  $global:UpgradeTestState.Projects['umnia-legacy'].Services = @('postgres-business', 'business-api')
  . "$ReviewDir/mock-docker-faithful.ps1"
  $legacyFile = New-LegacyComposeCopy 'noweb'
  $newFile = "$Repo/deploy/windows/compose.yml"
  $backupDir = New-BackupDir 'noweb'
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'noweb-output.log'
  $global:RunScript = $ScriptPath
  $code = Invoke-Upgrade @{ ComposeFile=$newFile; OldProject='umnia-legacy'; NewProject='umnia'; BackupDir=$backupDir; ApiBase='http://127.0.0.1:18081'; ConfirmMaintenance=$true; LegacyComposeFile=$legacyFile } $outLog
  Check 'fixed missing-writer: script exits 0' ($code -eq 0)
  Check 'fixed missing-writer: web was never queried on the legacy project' (-not ($global:DockerLog -match '-p umnia-legacy ps -q web'))
  Check 'fixed missing-writer: fixture workspace preserved' ((Invoke-Sql 55437 'umnia_bootstrap' "SELECT count(*) FROM workspaces WHERE name='Toko Lama';") -eq '1')
  Check 'fixed missing-writer: new API readyz 200' ((& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 5 'http://127.0.0.1:18081/readyz') -eq '200')
}
function Invoke-ScenarioFixedIsolatedApi {
  Write-Host "`n=== fixed-isolated-api: tenant isolation on an isolated fixture (MF4 coverage) ==="
  # Runs the isolated API workflow against the NEW cluster API left running
  # by fixed-happy. Labels the environment as an isolated synthetic fixture.
  $outLog = Join-Path ([System.IO.Path]::GetTempPath()) 'isolated-api.log'
  & bash "$Repo/docs/reviews/f1-windows-review-fixes/scripts/api-workflow.sh" 'http://127.0.0.1:18081' 2>&1 | Tee-Object -FilePath $outLog | Out-Null
  $code = $LASTEXITCODE
  $steps = @(Get-Content $outLog | Where-Object { $_ -match '^STEP ' })
  Check 'fixed isolated-api: workflow exits 0 on the isolated fixture' ($code -eq 0)
  Check "fixed isolated-api: 14/14 steps pass (got $($steps.Count))" ($steps.Count -eq 14)
}

switch ($Scenario) {
  'baseline-mf1' { Invoke-ScenarioBaselineMf1 }
  'baseline-mf1-routing' { Invoke-ScenarioBaselineMf1Routing }
  'baseline-mf2-equal' { Invoke-ScenarioBaselineMf2Equal }
  'baseline-mf2-existing-target' { Invoke-ScenarioBaselineMf2ExistingTarget }
  'baseline-mf3-writer' { Invoke-ScenarioBaselineMf3Writer }
  'baseline-mf4-probe' { Invoke-ScenarioBaselineMf4Probe }
  'fixed-happy' { Invoke-ScenarioFixedHappy }
  'fixed-negative' { Invoke-ScenarioFixedNegative }
  'fixed-failure-after-down' { Invoke-ScenarioFixedFailureAfterDown }
  'fixed-stopped-source' { Invoke-ScenarioFixedStoppedSource }
  'fixed-stopped-source-recovery' { Invoke-ScenarioFixedStoppedSourceRecovery }
  'fixed-inspection-failure' { Invoke-ScenarioFixedInspectionFailure }
  'fixed-missing-writer' { Invoke-ScenarioFixedMissingWriter }
  'fixed-isolated-api' { Invoke-ScenarioFixedIsolatedApi }
  'all' {
    Invoke-ScenarioBaselineMf1
    Invoke-ScenarioBaselineMf1Routing
    Invoke-ScenarioBaselineMf2Equal
    Invoke-ScenarioBaselineMf2ExistingTarget
    Invoke-ScenarioBaselineMf3Writer
    Invoke-ScenarioBaselineMf4Probe
    Invoke-ScenarioFixedHappy
    Invoke-ScenarioFixedNegative
    Invoke-ScenarioFixedFailureAfterDown
    Invoke-ScenarioFixedStoppedSource
    Invoke-ScenarioFixedStoppedSourceRecovery
    Invoke-ScenarioFixedInspectionFailure
    Invoke-ScenarioFixedMissingWriter
    Invoke-ScenarioFixedIsolatedApi
  }
  default { throw "unknown scenario: $Scenario" }
}
Write-Host "`n--- $global:Checks checks, failures: $global:Fail ---"
exit $global:Fail
