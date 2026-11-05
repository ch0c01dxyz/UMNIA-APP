# mock-docker-faithful.ps1 — faithful Docker executor for Upgrade-UmniaVolume.ps1.
# Dot-source this file BEFORE invoking the upgrade script. It replaces the
# `docker` command with a function that executes the script's Docker calls
# against REAL local resources instead of returning canned success:
#
#  - `docker compose ... exec -T postgres-business <pg_isready|pg_dump|
#    pg_restore|psql|rm>` runs the OFFICIAL image's own postgres 16.15
#    binaries against a real cluster mapped to that project (socket trust:
#    the image initdb default pg_hba is `local all all trust` and
#    `host all all 127.0.0.1/32 trust` — TESTED via pg_hba.conf of the
#    fixture cluster — so in-container exec needs no password, exactly as
#    the real container behaves; the script itself passes no password).
#  - `docker compose ... up -d postgres-business` starts the mapped cluster
#    with pg_ctl when its PGDATA exists, or runs the real entrypoint
#    (run-entrypoint.sh, repo initdb) when the volume is empty — the same
#    distinction the real daemon makes.
#  - `docker compose ... up --build -d` additionally runs the real
#    migrations (psql -1, 001+002, as bootstrap) and starts the real Go API
#    binary for that project.
#  - `docker compose ... down|stop` stops the mapped services (pg_ctl stop /
#    API process kill). Volumes (PGDATA dirs) are never deleted.
#  - `docker cp` does REAL file copies between the container staging dir
#    (/tmp inside the container) and the host path.
#  - `docker volume ls|inspect` consults REAL PGDATA-dir existence.
#  - `docker inspect <cid>` returns REAL JSON (State.Running, Mounts with
#    the volume name, Config.Image) for the mapped container.
#  - Any other docker call FAILS loudly (exit 1) instead of silently
#    succeeding — an unimplemented path is a test failure, not a pass.
#  - `docker compose ps -q` reflects REAL running state (pg_isready /
#    API process alive). A service that does not exist in the mock state
#    is reported as not running (''), matching the script's error-tolerant
#    helper which treats a real "service not found" error the same way.
#
# What is STILL mocked (labelled in every report): the Docker transport
# itself (no daemon, no real containers — `docker run` is blocked in this
# VM), the container /tmp staging (a host dir per project), the `web`
# service (tracked only as running/stopped, no real Nuxt server), and
# PowerShell's own HTTP stack (delegated to /usr/bin/curl, as before).
# SQL, files, migrations, API processes and their exit codes are real.
#
# State: the caller sets $global:UpgradeTestState (hashtable) BEFORE
# dot-sourcing. See test-upgrade-scenarios.ps1 for the shape.
# Every docker call is appended to $global:DockerLog (list of strings) and
# to $global:DockerLogFile. Every HTTP call is appended to
# $global:HttpLog (list of "METHOD URI -> CODE").
# Exit codes: the function sets $global:LASTEXITCODE as its LAST action on
# every path, so the script reads the real outcome.

$global:DockerLog = @()
$global:HttpLog = @()
$global:DockerLogFile = Join-Path ([System.IO.Path]::GetTempPath()) "umnia-faithful-docker.log"
if (Test-Path $global:DockerLogFile) { Remove-Item $global:DockerLogFile -Force }

function Write-DockerLog($Line) {
  $global:DockerLog += $Line
  Add-Content -Path $global:DockerLogFile -Value $Line
}
function Write-HttpLog($Line) {
  $global:HttpLog += $Line
  Write-Host "  [http] $Line"
}

function Get-ProjectState($Project) {
  if (-not $global:UpgradeTestState.Projects.ContainsKey($Project)) {
    throw "mock: unknown project '$Project' (state has: $($global:UpgradeTestState.Projects.Keys -join ', '))"
  }
  return $global:UpgradeTestState.Projects[$Project]
}
function Get-VolumePgData($VolumeName) {
  if ($global:UpgradeTestState.Volumes.ContainsKey($VolumeName)) {
    return $global:UpgradeTestState.Volumes[$VolumeName]
  }
  return $null
}
function Test-PostgresRunning($Proj) {
  $bin = Join-Path $global:UpgradeTestState.Rootfs "usr/lib/postgresql/16/bin/pg_isready"
  $env:LD_LIBRARY_PATH = $global:UpgradeTestState.LdPath
  & $bin -h /var/run/postgresql -p $Proj.Port -d $Proj.Db 2>$null | Out-Null
  return ($LASTEXITCODE -eq 0)
}
function Test-ApiRunning($Proj) {
  if (-not $Proj.ApiPidFile -or -not (Test-Path $Proj.ApiPidFile)) { return $false }
  $apiPid = [int](Get-Content $Proj.ApiPidFile -Raw).Trim()
  if ($apiPid -le 0) { return $false }
  return (Get-Process -Id $apiPid -ErrorAction SilentlyContinue) -ne $null
}
function Start-Postgres($Proj) {
  if (Test-PostgresRunning $Proj) { return }
  $binDir = Join-Path $global:UpgradeTestState.Rootfs "usr/lib/postgresql/16/bin"
  $env:LD_LIBRARY_PATH = $global:UpgradeTestState.LdPath
  if (Test-Path $Proj.PgData) {
    $ctlLog = "$($global:UpgradeTestState.Harness)/pgctl-$($Proj.Name).log"
    & sudo -u postgres env "LD_LIBRARY_PATH=$($global:UpgradeTestState.LdPath)" $binDir/pg_ctl -D $Proj.PgData -l $ctlLog -o "-p $($Proj.Port) -c listen_addresses=127.0.0.1" start | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "mock: pg_ctl start failed for $($Proj.Name) (exit $LASTEXITCODE)" }
  } else {
    # Empty volume: real entrypoint init (repo initdb) via the repo harness.
    $env:WINTEST_ROOTFS = $global:UpgradeTestState.Rootfs
    & bash $global:UpgradeTestState.RunEntrypoint $Proj.Name $Proj.Port $Proj.BootstrapUser $Proj.Db $Proj.BootstrapPw $Proj.RuntimeUser $Proj.RuntimePw | Out-Null
    if ($LASTEXITCODE -ne 0) { throw "mock: entrypoint init failed for $($Proj.Name) (exit $LASTEXITCODE)" }
  }
  for ($i = 0; $i -lt 30; $i++) {
    if (Test-PostgresRunning $Proj) { return }
    Start-Sleep -Seconds 1
  }
  throw "mock: postgres $($Proj.Name) did not become ready"
}
function Stop-Postgres($Proj) {
  if (-not (Test-Path $Proj.PgData)) { return }
  $binDir = Join-Path $global:UpgradeTestState.Rootfs "usr/lib/postgresql/16/bin"
  $env:LD_LIBRARY_PATH = $global:UpgradeTestState.LdPath
  $ctlLog = "$($global:UpgradeTestState.Harness)/pgctl-$($Proj.Name).log"
  & sudo -u postgres env "LD_LIBRARY_PATH=$($global:UpgradeTestState.LdPath)" $binDir/pg_ctl -D $Proj.PgData -l $ctlLog stop -m fast 2>$null | Out-Null
}
function Start-Api($Proj) {
  if (Test-ApiRunning $Proj) { return }
  $env:BUSINESS_DATABASE_URL = "postgres://$($Proj.RuntimeUser):$($Proj.RuntimePw)@127.0.0.1:$($Proj.Port)/$($Proj.Db)?sslmode=disable"
  $env:HTTP_LISTEN_ADDR = ":$($Proj.ApiPort)"
  $env:APP_ENV = "development"
  $p = Start-Process -FilePath $global:UpgradeTestState.ApiBinary -PassThru -RedirectStandardOutput $Proj.ApiLog -RedirectStandardError $Proj.ApiLogErr -WorkingDirectory $global:UpgradeTestState.Repo
  Set-Content -Path $Proj.ApiPidFile -Value $p.Id -NoNewline
  for ($i = 0; $i -lt 30; $i++) {
    $code = (& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$($Proj.ApiPort)/readyz")
    if ($code -eq '200') { return }
    Start-Sleep -Seconds 1
  }
  throw "mock: api $($Proj.Name) did not become ready"
}
function Stop-Api($Proj) {
  if (-not $Proj.ApiPidFile -or -not (Test-Path $Proj.ApiPidFile)) { return }
  $apiPid = [int](Get-Content $Proj.ApiPidFile -Raw).Trim()
  if ($apiPid -gt 0) { Stop-Process -Id $apiPid -Force -ErrorAction SilentlyContinue }
  Remove-Item $Proj.ApiPidFile -Force -ErrorAction SilentlyContinue
}
function Invoke-Migrate($Proj) {
  $binDir = Join-Path $global:UpgradeTestState.Rootfs "usr/lib/postgresql/16/bin"
  $env:LD_LIBRARY_PATH = $global:UpgradeTestState.LdPath
  $mig = $global:UpgradeTestState.Migrations
  & $binDir/psql -h /var/run/postgresql -p $Proj.Port -U $Proj.BootstrapUser -d $Proj.Db -v ON_ERROR_STOP=1 -1 -f (Join-Path $mig '001_init.sql') -f (Join-Path $mig '002_identity.sql') 2>&1 | Out-Null
  if ($LASTEXITCODE -ne 0) { throw "mock: migrate failed for $($Proj.Name) (exit $LASTEXITCODE)" }
}
function Invoke-ContainerExec($Proj, $CmdArgs) {
  # Runs a command "inside" the mapped postgres container: real image
  # binaries against the real cluster via the local socket (trust auth,
  # exactly as in-container exec behaves). Container path /tmp/... maps to
  # the project's staging dir. Returns exit code; stdout is emitted.
  if (-not (Test-PostgresRunning $Proj)) {
    Write-Host "mock docker exec: container $($Proj.Name) postgres-business is not running"
    $global:MockExecCode = 1
    return
  }
  $binDir = Join-Path $global:UpgradeTestState.Rootfs "usr/lib/postgresql/16/bin"
  $env:LD_LIBRARY_PATH = $global:UpgradeTestState.LdPath
  $env:PGHOST = '/var/run/postgresql'
  $env:PGPORT = "$($Proj.Port)"
  $tool = $CmdArgs[0]
  $rest = @()
  for ($i = 1; $i -lt $CmdArgs.Count; $i++) {
    $a = $CmdArgs[$i]
    if ($a -eq '/tmp/umnia-backup.dump') { $a = Join-Path $Proj.Staging 'umnia-backup.dump' }
    $rest += $a
  }
  if ($tool -eq 'pg_dump' -and -not $global:DumpTime) { $global:DumpTime = Get-Date }
  $code = 0
  if ($tool -eq 'pg_restore' -and ($rest -notcontains '-l') -and $global:UpgradeTestState.FailRestore) {
    # Injected fault (test only): the target restore fails, simulating a
    # real restore failure (corrupt dump, version mismatch, disk full).
    Write-Host "mock: injected pg_restore failure (FailRestore)"
    $global:MockExecCode = 1
    return
  }
  switch ($tool) {
    'pg_isready' { & $binDir/pg_isready @rest; $code = $LASTEXITCODE }
    'pg_dump'    { & $binDir/pg_dump @rest; $code = $LASTEXITCODE }
    'pg_restore' { & $binDir/pg_restore @rest; $code = $LASTEXITCODE }
    'psql'       { & $binDir/psql @rest; $code = $LASTEXITCODE }
    'rm'         { & rm @rest; $code = $LASTEXITCODE }
    default { Write-Host "mock: unimplemented container exec '$tool'"; $code = 1 }
  }
  # NOTE: the native command's stdout above is this function's output (the
  # script captures it); the exit code travels in $global:MockExecCode.
  $global:MockExecCode = $code
  return
}

function docker {
  $line = ($args -join ' ')
  Write-DockerLog $line
  $code = 0
  try {
    if ($args[0] -eq 'cp') {
      # docker cp <src> <dst>: exactly one side has a container prefix.
      $src = $args[1]; $dst = $args[2]
      $srcCid = $null; $dstCid = $null
      if ($src -match '^([^:]+):(.+)$') { $srcCid = $Matches[1]; $src = $Matches[2] }
      if ($dst -match '^([^:]+):(.+)$') { $dstCid = $Matches[1]; $dst = $Matches[2] }
      $proj = $null
      if ($srcCid) { $proj = $global:UpgradeTestState.Cids[$srcCid] }
      elseif ($dstCid) { $proj = $global:UpgradeTestState.Cids[$dstCid] }
      if (-not $proj) { throw "mock docker cp: unknown container id" }
      if ($srcCid) { $src = Join-Path $proj.Staging (Split-Path $src -Leaf) }
      if ($dstCid) { $dst = Join-Path $proj.Staging (Split-Path $dst -Leaf) }
      Copy-Item $src $dst -Force
      $code = 0
    }
    elseif ($args[0] -eq 'volume' -and $args[1] -eq 'ls') {
      # docker volume ls -q --filter name=^<name>$  -> print name iff PGDATA exists
      $filter = ($args | Where-Object { $_ -like 'name=*' }) -replace '^name=', ''
      $name = $filter -replace '^\^', '' -replace '\$$', ''
      $pgdata = Get-VolumePgData $name
      if ($pgdata -and (Test-Path $pgdata)) { $name }
      $code = 0
    }
    elseif ($args[0] -eq 'volume' -and $args[1] -eq 'inspect') {
      $pgdata = Get-VolumePgData $args[2]
      if ($pgdata -and (Test-Path $pgdata)) { "[{`"Name`": `"$($args[2])`"}]" } else { $code = 1 }
    }
    elseif ($args[0] -eq 'inspect') {
      $proj = $global:UpgradeTestState.Cids[$args[1]]
      if (-not $proj) { Write-Host "mock docker inspect: unknown container $($args[1])"; $code = 1 }
      else {
        $running = (Test-PostgresRunning $proj)
        $volName = "$($proj.ProjectName)_postgres-business-data"
        $mounts = "[{`"Name`": `"$volName`", `"Destination`": `"/var/lib/postgresql/data`"}]"
        "{`"Id`": `"$($args[1])`", `"Name`": `"/$($proj.ProjectName)_postgres-business_1`", `"State`": {`"Running`": $($running.ToString().ToLower())}, `"Config`": {`"Image`": `"postgres:16-bookworm`"}, `"Mounts`": $mounts}"
        $code = 0
      }
    }
    elseif ($args[0] -eq 'compose') {
      $pIdx = [Array]::IndexOf($args, '-p')
      $project = $args[$pIdx + 1]
      $sub = @()
      for ($i = $pIdx + 2; $i -lt $args.Count; $i++) { $sub += $args[$i] }
      $proj = Get-ProjectState $project
      $verb = $sub[0]
      switch ($verb) {
        'ps' {
          # ps -q [service]: CIDs of RUNNING services only.
          $svc = $null
          $qIdx = [Array]::IndexOf($sub, '-q')
          if ($qIdx -ge 0 -and $qIdx + 1 -lt $sub.Count) { $svc = $sub[$qIdx + 1] }
          $out = @()
          if (-not $svc -or $svc -eq 'postgres-business') { if (Test-PostgresRunning $proj) { $out += $proj.Cid } }
          if (-not $svc -or $svc -eq 'business-api') { if (Test-ApiRunning $proj) { $out += "$($proj.Cid)-api" } }
          if (-not $svc -or $svc -eq 'web') { if ($proj.WebRunning) { $out += "$($proj.Cid)-web" } }
          $out
          $code = 0
        }
        'up' {
          $svc = $null
          if ($sub -contains 'postgres-business') { $svc = 'postgres-business' }
          if ($sub -contains '--build') {
            Start-Postgres $proj
            Invoke-Migrate $proj
            Start-Api $proj
            $proj.WebRunning = $true
          } elseif ($svc -eq 'postgres-business') {
            Start-Postgres $proj
          } else {
            # plain `up -d`: full stack (recovery path).
            Start-Postgres $proj
            Start-Api $proj
            $proj.WebRunning = $true
          }
          $code = 0
        }
        'down' {
          Stop-Api $proj
          $proj.WebRunning = $false
          Stop-Postgres $proj
          $code = 0
        }
        'stop' {
          $svc = $sub[1]
          if ($svc -eq 'business-api') { Stop-Api $proj }
          elseif ($svc -eq 'web') { $proj.WebRunning = $false }
          elseif ($svc -eq 'postgres-business') { Stop-Postgres $proj }
          else { throw "mock docker compose stop: unknown service '$svc'" }
          $code = 0
        }
        'exec' {
          # exec -T postgres-business <cmd...>
          $tIdx = [Array]::IndexOf($sub, '-T')
          $svc = $sub[$tIdx + 1]
          if ($svc -ne 'postgres-business') { throw "mock docker compose exec: service '$svc' not modelled" }
          $cmd = @()
          for ($i = $tIdx + 2; $i -lt $sub.Count; $i++) { $cmd += $sub[$i] }
          Invoke-ContainerExec $proj $cmd
          $code = $global:MockExecCode
        }
        default { throw "mock: unimplemented docker compose verb '$verb'" }
      }
    }
    else { throw "mock: unimplemented docker call '$($args[0])'" }
  } catch {
    Write-Host "mock docker error: $($_.Exception.Message)"
    $code = 1
  }
  $global:LASTEXITCODE = $code
}

# HTTP: pwsh's own stack is blocked in this VM; delegate to /usr/bin/curl
# and LOG every call (method + URI + status) for the no-mutating-HTTP proof.
function Invoke-WebRequest {
  param($Uri, [switch]$UseBasicParsing, $TimeoutSec)
  $code = (& /usr/bin/curl -s -o /dev/null -w '%{http_code}' --max-time 10 $Uri)
  Write-HttpLog "GET $Uri -> $code"
  return [pscustomobject]@{ StatusCode = [int]$code }
}
function Invoke-RestMethod {
  param($Uri, $Method = 'Get', $ContentType, $Headers, $Body)
  $a = @('/usr/bin/curl', '-s', '-w', "`n%{http_code}", '--max-time', '10', '-X', $Method.ToUpper(), $Uri)
  if ($ContentType) { $a += @('-H', "Content-Type: $ContentType") }
  if ($Headers -and $Headers.Authorization) { $a += @('-H', "Authorization: $($Headers.Authorization)") }
  if ($Body) { $a += @('-d', $Body) }
  $out = (& $a[0] $a[1..($a.Count - 1)])
  $code = [int]($out[-1])
  $body = ($out[0..($out.Count - 2)] -join "`n")
  Write-HttpLog "$Method $Uri -> $code"
  if ($code -ge 200 -and $code -lt 300) { return ($body | ConvertFrom-Json) }
  $ex = New-Object System.Exception("HTTP $code")
  $resp = [pscustomobject]@{ StatusCode = $code }
  $ex | Add-Member -NotePropertyName Response -NotePropertyValue $resp -Force
  throw $ex
}
