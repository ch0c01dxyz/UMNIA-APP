# Tes terarah provisioning operator (PS 5.1-safe).
#
# Menguji fungsi-fungsi deploy/windows/provision-operator.ps1 dengan
# dot-source — TIDAK menjalankan provisioning (tanpa docker, tanpa
# perubahan role/cluster), kecuali Test 4 yang memakai fixture
# terisolasi bila sudo+psql tersedia.
#
# Penggunaan (Linux pwsh 7.x, dari root repo):
#   pwsh -NoProfile -File deploy/windows/tests/test-operator-provisioning.ps1
# Penggunaan (Windows PS 5.1, dari root repo):
#   powershell -NoProfile -ExecutionPolicy Bypass -File deploy\windows\tests\test-operator-provisioning.ps1
#
# Keluaran membedakan PASS / FAIL / NOT RUN; exit 1 bila ada FAIL.
# NOT RUN bukan PASS: Test 2 hanya jalan di Windows, Test 4, Test 5
# dan Test 6 hanya jalan bila sudo -u postgres psql tersedia dan tanpa kata sandi.
$ErrorActionPreference = 'Continue'

$scriptDir = $PSScriptRoot
$provisionScript = Join-Path (Split-Path $scriptDir -Parent) 'provision-operator.ps1'
if (-not (Test-Path $provisionScript)) {
    Write-Host "FAIL: provision-operator.ps1 tidak ditemukan: $provisionScript"
    exit 1
}
. $provisionScript

$results = @()

function Add-Result {
    param([string]$Name, [string]$Status, [string]$Detail)
    $script:results += @{ Name = $Name; Status = $Status; Detail = $Detail }
    if ($Detail) {
        Write-Host "${Status}: $Name — $Detail"
    } else {
        Write-Host "${Status}: $Name"
    }
}

function Test-IsWindows {
    return [System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT
}

# ---- Test 1: argumen ACL icacls (bukan "(F)", SID memakai '*') ----
# HANYA format/penangkapan argumen icacls yang diuji di sini, TERPISAH
# dari penegakan DACL efektif: icacls yang di-mock mengembalikan 0
# TANPA mengubah DACL, sehingga penegakan nyata
# (Remove-DisallowedRosterAce) akan menolak ACL warisan file temp dan
# menggagalkan tes argumen sebelum asersi SID/nama sempat jalan.
# Penegakan DACL efektif tetap diuji NYATA oleh Test 2 (acl-efektif,
# Windows saja) — tidak dilemahkan di sini. Stub Remove-DisallowedRosterAce
# di bawah adalah seam sempit khusus tes ini (mencatat pemanggilan:
# 1x di Windows, 0x di Linux karena guard platform di Lock-RosterAcl),
# lalu dihapus sesudah tes dan fungsi asli dipulihkan dengan dot-source
# ulang di finally (tanpa itu Test 2 gagal: helper tidak dikenali).
try {
    $tempFile = [System.IO.Path]::GetTempFileName()
    [System.IO.File]::WriteAllText($tempFile, 'roster-sentinel')
    $savedUsername = $env:USERNAME
    $env:USERNAME = 'tes-user-sentinel'
    $script:icaclsArgs = $null
    function icacls {
        $script:icaclsArgs = $args
        $global:LASTEXITCODE = 0
    }
    $script:removeAceCalls = 0
    function Remove-DisallowedRosterAce {
        param([string]$RosterPath)
        $script:removeAceCalls++
    }
    # Prinsipal ekspektasi dibaca SELAGI $env:USERNAME masih sentinel:
    # Get-CurrentPrincipal memakai env yang sama dengan Lock-RosterAcl.
    $expectedPrincipal = Get-CurrentPrincipal
    # Jumlah pemanggilan seam penegakan yang diharapkan: Lock-RosterAcl
    # memanggil Remove-DisallowedRosterAce sekali di Windows (sesudah
    # icacls sukses), dan tidak pernah di Linux (guard platform).
    $wantRemoveCalls = 0
    if (Test-IsWindows) { $wantRemoveCalls = 1 }
    try {
        Lock-RosterAcl -RosterPath $tempFile
    } finally {
        $env:USERNAME = $savedUsername
    }
    if (-not $script:icaclsArgs) {
        Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail 'icacls tidak dipanggil'
    } elseif ($script:removeAceCalls -ne $wantRemoveCalls) {
        Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail "seam penegakan: Remove-DisallowedRosterAce dipanggil $($script:removeAceCalls)x, want $wantRemoveCalls"
    } else {
        $grantArgs = @($script:icaclsArgs | Where-Object { $_ -like '*:(F)' })
        $bareF = @($script:icaclsArgs | Where-Object { $_ -eq '(F)' })
        # Bentuk yang diharapkan memakai helper yang sama dengan
        # Lock-RosterAcl: SID numerik memakai prefix '*', nama tidak.
        $expectedGrant = Format-IcaclsGrantArg -Principal $expectedPrincipal
        if ($bareF.Count -gt 0) {
            Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail "argumen '(F)' polos masih ada: $($script:icaclsArgs -join ' ')"
        } elseif ($grantArgs.Count -ne 1) {
            Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail "argumen grant :(F) want 1, got $($grantArgs.Count): $($script:icaclsArgs -join ' ')"
        } elseif ($grantArgs[0] -ne $expectedGrant) {
            Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail "grant want '$expectedGrant', got '$($grantArgs[0])'"
        } else {
            Add-Result -Name 'acl-argumen' -Status 'PASS' -Detail "grant '$($grantArgs[0])'"
        }
    }
    Remove-Item Function:\icacls -ErrorAction SilentlyContinue
    Remove-Item Function:\Remove-DisallowedRosterAce -ErrorAction SilentlyContinue
    # Format-IcaclsGrantArg langsung: SID numerik HARUS memakai '*'
    # (tanpa itu icacls gagal 1332), nama akun tidak memakai '*'.
    $sidArg = Format-IcaclsGrantArg -Principal 'S-1-5-21-1234567890-1234567890-1234567890-1001'
    if ($sidArg -ne '*S-1-5-21-1234567890-1234567890-1234567890-1001:(F)') {
        Add-Result -Name 'acl-sid-argumen' -Status 'FAIL' -Detail "want '*S-1-5-21-...:(F)', got '$sidArg'"
    } else {
        Add-Result -Name 'acl-sid-argumen' -Status 'PASS' -Detail "grant '$sidArg'"
    }
    $nameArg = Format-IcaclsGrantArg -Principal 'tes-user-sentinel'
    if ($nameArg -ne 'tes-user-sentinel:(F)') {
        Add-Result -Name 'acl-nama-argumen' -Status 'FAIL' -Detail "want 'tes-user-sentinel:(F)', got '$nameArg'"
    } else {
        Add-Result -Name 'acl-nama-argumen' -Status 'PASS' -Detail "grant '$nameArg'"
    }
} catch {
    Remove-Item Function:\icacls -ErrorAction SilentlyContinue
    Remove-Item Function:\Remove-DisallowedRosterAce -ErrorAction SilentlyContinue
    Add-Result -Name 'acl-argumen' -Status 'FAIL' -Detail "$_"
} finally {
    # Pulihkan fungsi asli: stub di atas menimpa Remove-DisallowedRosterAce
    # dari provision-operator.ps1; Remove-Item hanya menghapus stub tanpa
    # memulihkan yang asli, sehingga Test 2 (acl-efektif) gagal di Windows
    # ("helper not recognized"). Dot-source ulang tanpa syarat — finally
    # jalan baik saat sukses maupun saat exception — sebelum Test 2.
    . $provisionScript
    if ($tempFile -and (Test-Path $tempFile)) { Remove-Item $tempFile -Force -ErrorAction SilentlyContinue }
}

# ---- Test 2: ACL efektif (Windows saja) ----
if (-not (Test-IsWindows)) {
    Add-Result -Name 'acl-efektif' -Status 'NOT RUN' -Detail 'bukan Windows (butuh icacls + Get-Acl asli)'
} else {
    try {
        $tempFile = [System.IO.Path]::GetTempFileName()
        [System.IO.File]::WriteAllText($tempFile, 'roster-sentinel')
        # Seed ACE baca eksplisit yang luas (Everyone:Read) SEBELUM
        # penguncian: /inheritance:r + /grant:r saja mempertahankannya,
        # Lock-RosterAcl harus menghapusnya.
        $seedOut = icacls $tempFile /grant:r '*S-1-1-0:(R)' 2>&1
        if ($LASTEXITCODE -ne 0) {
            Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail "seed Everyone:Read gagal (exit $LASTEXITCODE): $seedOut"
        } else {
            Lock-RosterAcl -RosterPath $tempFile
            $acl = Get-Acl $tempFile
            $principal = Get-CurrentPrincipal
            # Kanonikalisasi ke SecurityIdentifier sebelum membandingkan
            # (Get-Acl bisa mengembalikan NTAccount, bukan SID).
            $callerSid = $null
            try {
                $callerSid = ([System.Security.Principal.NTAccount]$principal).Translate([System.Security.Principal.SecurityIdentifier]).Value
            } catch {
                $callerSid = $principal
            }
            $allowed = Get-AllowedRosterSids
            $callerFullControl = $false
            $disallowed = @()
            $everyonePresent = $false
            foreach ($rule in @($acl.Access)) {
                try {
                    $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
                } catch {
                    $sid = $rule.IdentityReference.Value
                }
                if ($sid -eq $callerSid -and ($rule.FileSystemRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -ne 0) {
                    $callerFullControl = $true
                }
                if ($sid -eq 'S-1-1-0') { $everyonePresent = $true }
                if ($allowed -notcontains $sid) { $disallowed += $sid }
            }
            if (-not $acl.AreAccessRulesProtected) {
                Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail 'inheritance masih aktif (AreAccessRulesProtected=false)'
            } elseif (-not $callerFullControl) {
                Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail "tidak ada aturan FullControl untuk $callerSid"
            } elseif ($everyonePresent) {
                Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail 'ACE Everyone:Read yang di-seed masih ada (tidak dihapus)'
            } elseif ($disallowed.Count -gt 0) {
                Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail "ACE di luar himpunan yang diizinkan masih ada: $($disallowed -join ', ')"
            } else {
                Add-Result -Name 'acl-efektif' -Status 'PASS' -Detail "inheritance dimatikan, FullControl untuk $callerSid, Everyone:Read terhapus, semua ACE dalam himpunan yang diizinkan"
            }
        }
    } catch {
        Add-Result -Name 'acl-efektif' -Status 'FAIL' -Detail "$_"
    } finally {
        if ($tempFile -and (Test-Path $tempFile)) { Remove-Item $tempFile -Force -ErrorAction SilentlyContinue }
    }
}

# ---- Test 3: builder password — quote/dollar/whitespace (round-trip string) ----
try {
    $cases = @(
        @{ Label = 'quote'; Password = "kutip'12-karakter" },
        @{ Label = 'dollar'; Password = 'dollar$$tengah-12' },
        @{ Label = 'spasi'; Password = '  spasi-depan-belakang  ' },
        @{ Label = 'campuran'; Password = "campur'$$ spasi-12 " }
    )
    $allOk = $true
    $tags = @()
    foreach ($case in $cases) {
        $op = @{ Id = 'pstest'; Role = 'umnia_op_pstest'; Password = $case.Password }
        $sql = Build-OperatorProvisionSql -Operators @($op)
        # Tag dollar-quote acak harus ada pada blok operator.
        $tagMatch = [regex]::Match($sql, 'DO \$(umnia_pw_[0-9a-f]{32})\$')
        if (-not $tagMatch.Success) {
            Add-Result -Name "password-builder-$($case.Label)" -Status 'FAIL' -Detail 'tag dollar-quote acak tidak ditemukan'
            $allOk = $false
            continue
        }
        $tags += $tagMatch.Groups[1].Value
        if ($sql -notmatch 'END \$umnia_pw_[0-9a-f]{32}\$;') {
            Add-Result -Name "password-builder-$($case.Label)" -Status 'FAIL' -Detail 'penutup DO tidak memakai tag yang sama'
            $allOk = $false
            continue
        }
        # Round-trip: literal PASSWORD di-un escape '' dan harus
        # sama byte dengan password asli (kedua cabang CREATE/ALTER).
        $matches = [regex]::Matches($sql, "PASSWORD '((?:[^']|'')*)'")
        if ($matches.Count -ne 2) {
            Add-Result -Name "password-builder-$($case.Label)" -Status 'FAIL' -Detail "literal PASSWORD want 2, got $($matches.Count)"
            $allOk = $false
            continue
        }
        $roundTripOk = $true
        foreach ($m in $matches) {
            $back = $m.Groups[1].Value -replace "''", "'"
            if ($back -cne $case.Password) { $roundTripOk = $false }
        }
        if (-not $roundTripOk) {
            Add-Result -Name "password-builder-$($case.Label)" -Status 'FAIL' -Detail 'round-trip password tidak byte-identik'
            $allOk = $false
            continue
        }
        Add-Result -Name "password-builder-$($case.Label)" -Status 'PASS' -Detail ''
    }
    if ($tags.Count -ge 2 -and $tags[0] -eq $tags[1]) {
        Add-Result -Name 'password-builder-tag-acak' -Status 'FAIL' -Detail 'dua build memakai tag yang sama'
    } elseif ($tags.Count -ge 2) {
        Add-Result -Name 'password-builder-tag-acak' -Status 'PASS' -Detail 'tag berbeda antar build'
    }
    # Blok NOLOGIN tetap memakai tag tetap $$ (tanpa password di dalam).
    $sqlNologin = Build-OperatorProvisionSql -Operators @()
    if ($sqlNologin -notmatch 'DO \$\$ BEGIN') {
        Add-Result -Name 'password-builder-nologin' -Status 'FAIL' -Detail 'blok NOLOGIN tidak lagi memakai DO $$'
    } else {
        Add-Result -Name 'password-builder-nologin' -Status 'PASS' -Detail ''
    }
} catch {
    Add-Result -Name 'password-builder' -Status 'FAIL' -Detail "$_"
}

# ---- Test 3b: parse roster — byte password dipertahankan ----
try {
    $rosterFile = [System.IO.Path]::GetTempFileName()
    $text = "UMNIA_OP_QUOTE=kutip'12-karakter`r`nUMNIA_OP_SPASI=  spasi-depan-belakang  `r`n"
    [System.IO.File]::WriteAllText($rosterFile, $text)
    $parsed = Read-OperatorRoster -RosterPath $rosterFile
    $byRole = @{}
    foreach ($op in $parsed) { $byRole[$op.Role] = $op.Password }
    if ($byRole['umnia_op_quote'] -cne "kutip'12-karakter") {
        Add-Result -Name 'roster-parse-quote' -Status 'FAIL' -Detail "want kutip'12-karakter, got '$($byRole['umnia_op_quote'])'"
    } else {
        Add-Result -Name 'roster-parse-quote' -Status 'PASS' -Detail ''
    }
    if ($byRole['umnia_op_spasi'] -cne '  spasi-depan-belakang  ') {
        Add-Result -Name 'roster-parse-spasi' -Status 'FAIL' -Detail "spasi awal/akhir berubah: '$($byRole['umnia_op_spasi'])'"
    } else {
        Add-Result -Name 'roster-parse-spasi' -Status 'PASS' -Detail 'spasi awal/akhir utuh'
    }
} catch {
    Add-Result -Name 'roster-parse' -Status 'FAIL' -Detail "$_"
} finally {
    if ($rosterFile -and (Test-Path $rosterFile)) { Remove-Item $rosterFile -Force -ErrorAction SilentlyContinue }
}

# ---- Test 4: round-trip Postgres nyata (bila sudo+psql tersedia) ----
# Login tes UNIK + tervalidasi; tabrakan DITOLAK (tidak pernah ALTER/
# DROP login yang sudah ada). Sentinel login yang sudah ada sebelumnya
# harus dipertahankan (verifier + keanggotaan tidak berubah). Provisioning
# berjalan dalam konteks aman yang dimiliki tes ini, dengan preflight
# SEBELUM SQL provisioning diterapkan. Bersihkan HANYA fixture yang
# dibuat tes ini.
$sudoOk = $false
try {
    sudo -n true 2>$null
    if ($LASTEXITCODE -eq 0) {
        sudo -u postgres psql -tAc 'SELECT 1' 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $sudoOk = $true }
    }
} catch {
    $sudoOk = $false
}
if (-not $sudoOk) {
    Add-Result -Name 'postgres-round-trip' -Status 'NOT RUN' -Detail 'sudo -u postgres psql tidak tersedia/tanpa kata sandi'
} else {
    $suffix = [System.Guid]::NewGuid().ToString('N').Substring(0, 8)
    $scratchDb = 'umnia_pstest_' + $suffix
    $testRole = 'umnia_op_pstest_' + $suffix
    $sentinelRole = 'umnia_op_pstest_sentinel_' + $suffix
    $testPassword = "te'st `$$ spasi-12 "
    $sentinelPassword = 'x-pstest-sentinel-123'
    $createdDb = $false
    $createdRole = $false
    $createdSentinel = $false
    function Get-RoleVerifier {
        param([string]$Role)
        # Verifier ASLI dari pg_authid (hash SCRAM). pg_roles.rolpassword
        # selalu termask '********' sehingga TIDAK BISA mendeteksi
        # perubahan password — pemeriksaan lama adalah vakum. Dibaca
        # lewat sudo psql superuser KHUSUS tes ini (tidak ada hak baca
        # verifier untuk peran app/operator). Nilai hash hanya
        # dibandingkan di memori; pesan gagal menyebut nama peran saja.
        return (sudo -u postgres psql -tAc "SELECT coalesce(rolpassword,'') FROM pg_authid WHERE rolname = '$Role'" 2>$null)
    }
    function Get-RoleMemberships {
        param([string]$Role)
        return (sudo -u postgres psql -tAc "SELECT coalesce(string_agg(roleid::regrole::text, ',' ORDER BY roleid::regrole::text), '') FROM pg_auth_members WHERE member = '$Role'::regrole" 2>$null)
    }
    try {
        # Tolak tabrakan: login tes atau sentinel sudah ada sebelumnya.
        $testExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$testRole'" 2>$null
        $sentinelExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$sentinelRole'" 2>$null
        if ($testExists -eq '1' -or $sentinelExists -eq '1') {
            Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "tabrakan login yang sudah ada ($testRole/$sentinelRole) — ditolak, tidak diubah"
        } else {
            $dbExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname = '$scratchDb'" 2>$null
            if ($dbExists -eq '1') {
                Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "tabrakan nama DB $scratchDb (tidak dihapus)"
            } else {
                sudo -u postgres createdb $scratchDb 2>$null
                if ($LASTEXITCODE -ne 0) {
                    Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "gagal membuat DB $scratchDb"
                } else {
                    $createdDb = $true
                    # Sentinel login yang sudah ada SEBELUM provisioning.
                    # Password lewat stdin (bukan argv -c).
                    "CREATE ROLE $sentinelRole LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD '$sentinelPassword'" | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $scratchDb --no-password --no-psqlrc 2>$null | Out-Null
                    if ($LASTEXITCODE -ne 0) {
                        Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "gagal membuat sentinel $sentinelRole"
                    } else {
                        $createdSentinel = $true
                        $sentinelVerifierBefore = Get-RoleVerifier -Role $sentinelRole
                        $sentinelMembershipsBefore = Get-RoleMemberships -Role $sentinelRole
                        $op = @{ Id = "pstest_$suffix"; Role = $testRole; Password = $testPassword }
                        # Preflight SEBELUM SQL provisioning: konteks
                        # aman yang dimiliki tes ini, bukan shared role.
                        $preflightThrew = $false
                        $preflightMsg = ''
                        try {
                            Invoke-OperatorPreflight -Operators @($op) -RunPsql {
                                $input | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $scratchDb --no-password --no-psqlrc 2>&1
                            }
                        } catch {
                            $preflightThrew = $true
                            $preflightMsg = "$_"
                        }
                        if ($preflightThrew) {
                            Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "preflight gagal padahal state sehat: $preflightMsg"
                        } else {
                            $sql = Build-OperatorProvisionSql -Operators @($op)
                            $sql | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $scratchDb --no-password --no-psqlrc 2>$null | Out-Null
                            if ($LASTEXITCODE -ne 0) {
                                Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail 'aplikasi SQL provisioning gagal'
                            } else {
                                # Ownership: role dibuat OLEH tes ini
                                # (tidak ada sebelumnya, ada sesudahnya).
                                $roleNow = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$testRole'" 2>$null
                                if ($roleNow -ne '1') {
                                    Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "login $testRole tidak ada sesudah provisioning"
                                } else {
                                    $createdRole = $true
                                    $env:PGPASSWORD = $testPassword
                                    try {
                                        $loginOut = psql -h localhost -U $testRole -d $scratchDb -tAc "SELECT 'pstest-ok'" 2>&1
                                    } finally {
                                        Remove-Item Env:\PGPASSWORD -ErrorAction SilentlyContinue
                                    }
                                    if ($LASTEXITCODE -ne 0 -or $loginOut -ne 'pstest-ok') {
                                        Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "login password eksak gagal (exit $LASTEXITCODE): $loginOut"
                                    } else {
                                        # Sentinel yang sudah ada harus
                                        # dipertahankan (verifier +
                                        # keanggotaan tidak berubah).
                                        $sentinelVerifierAfter = Get-RoleVerifier -Role $sentinelRole
                                        $sentinelMembershipsAfter = Get-RoleMemberships -Role $sentinelRole
                                        if ($sentinelVerifierAfter -cne $sentinelVerifierBefore) {
                                            Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail 'sentinel login diubah (verifier password berubah)'
                                        } elseif ($sentinelMembershipsAfter -cne $sentinelMembershipsBefore) {
                                            Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail 'sentinel login diubah (keanggotaan berubah)'
                                        } else {
                                            Add-Result -Name 'postgres-round-trip' -Status 'PASS' -Detail 'login unik dengan byte password eksak (quote+$$+spasi); sentinel dipertahankan'
                                        }
                                    }
                                }
                            }
                        }
                    }
                }
            }
        }
    } catch {
        Add-Result -Name 'postgres-round-trip' -Status 'FAIL' -Detail "$_"
    } finally {
        # Bersihkan HANYA fixture yang dibuat tes ini.
        if ($createdRole) {
            sudo -u postgres psql -tAc "DROP ROLE IF EXISTS $testRole" 2>$null | Out-Null
        }
        if ($createdSentinel) {
            sudo -u postgres psql -tAc "DROP ROLE IF EXISTS $sentinelRole" 2>$null | Out-Null
        }
        if ($createdDb) {
            sudo -u postgres psql -tAc "DROP DATABASE IF EXISTS $scratchDb" 2>$null | Out-Null
        }
    }
}

# ---- Test 5: preflight provisioning (PS) menolak state tidak aman ----
# Cermin Go TestConfigurePostgresPreflightRejectsUnsafeRoles, lewat
# fungsi PS yang sama dengan provision-operator.ps1 (transport sudo psql;
# transport docker tetap NOT RUN di Linux). Preflight HARUS menolak
# SEBELUM mutasi apa pun; state asli (verifier/keanggotaan/kepemilikan)
# harus tidak berubah. Bersihkan HANYA fixture yang dibuat tes ini.
if (-not $sudoOk) {
    Add-Result -Name 'ps-preflight' -Status 'NOT RUN' -Detail 'sudo -u postgres psql tidak tersedia/tanpa kata sandi'
} else {
    $pfSuffix = [System.Guid]::NewGuid().ToString('N').Substring(0, 8)
    $pfDb = 'umnia_pstest_preflight_' + $pfSuffix
    $pfDbCreated = $false
    try {
        $pfDbExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_database WHERE datname = '$pfDb'" 2>$null
        if ($pfDbExists -eq '1') {
            Add-Result -Name 'ps-preflight' -Status 'FAIL' -Detail "tabrakan nama DB $pfDb (tidak dihapus)"
        } else {
            sudo -u postgres createdb $pfDb 2>$null
            if ($LASTEXITCODE -ne 0) {
                Add-Result -Name 'ps-preflight' -Status 'FAIL' -Detail "gagal membuat DB $pfDb"
            } else {
                $pfDbCreated = $true
                function Invoke-PsqlScratch {
                    param([string]$Sql)
                    return (sudo -u postgres psql -v ON_ERROR_STOP=1 -d $pfDb -tAc $Sql 2>$null)
                }
                $pfCases = @(
                    @{ Name = 'exec-owner-member'; Parent = ''; Seed = "CREATE ROLE {0} LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-preflight-ps-123'; GRANT umnia_grant_exec TO {0};"; Unseed = "DROP TABLE IF EXISTS preflight_owned_ps; REVOKE ALL ON ALL TABLES IN SCHEMA public FROM {0}; REVOKE umnia_grant_exec FROM {0}; DROP ROLE {0};" },
                    @{ Name = 'owned-table'; Parent = ''; Seed = "CREATE ROLE {0} LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-preflight-ps-123'; CREATE TABLE preflight_owned_ps (id int); ALTER TABLE preflight_owned_ps OWNER TO {0};"; Unseed = "DROP TABLE IF EXISTS preflight_owned_ps; REVOKE ALL ON ALL TABLES IN SCHEMA public FROM {0}; REVOKE umnia_grant_exec FROM {0}; DROP ROLE {0};" },
                    @{ Name = 'custom-parent'; Parent = 'parent'; Seed = "CREATE ROLE {1} NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION; CREATE ROLE {0} LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-preflight-ps-123'; CREATE TABLE preflight_parent_target_ps (id int); GRANT SELECT (id) ON preflight_parent_target_ps TO {1}; GRANT {1} TO {0};"; Unseed = "REVOKE SELECT (id) ON preflight_parent_target_ps FROM {1}; REVOKE {1} FROM {0}; DROP TABLE IF EXISTS preflight_parent_target_ps; DROP ROLE {0}; DROP ROLE {1};" }
                )
                foreach ($case in $pfCases) {
                    $role = 'umnia_op_pstest_pf_' + $pfSuffix + '_' + $case.Name.Replace('-', '_')
                    $parent = ''
                    if ($case.Parent -ne '') { $parent = $role + '_' + $case.Parent }
                    $roleExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$role'" 2>$null
                    if ($roleExists -eq '1') {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "tabrakan login $role (tidak diubah)"
                        continue
                    }
                    if ($parent -ne '') {
                        $parentExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$parent'" 2>$null
                        if ($parentExists -eq '1') {
                            Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "tabrakan parent $parent (tidak diubah)"
                            continue
                        }
                    }
                    $seedSql = $case.Seed -f $role, $parent
                    # Password seed lewat stdin (bukan argv -c).
                    $seedSql | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $pfDb --no-password --no-psqlrc 2>$null | Out-Null
                    if ($LASTEXITCODE -ne 0) {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "gagal men-seed state tidak aman untuk $role"
                        continue
                    }
                    # Bukti celah untuk custom-parent: hak kolom TERWARISI
                    # dan efektif, tapi pemeriksaan tabel/kolom langsung
                    # tidak melihatnya (has_table_privilege = false).
                    if ($case.Name -eq 'custom-parent') {
                        $colPriv = Invoke-PsqlScratch -Sql "SELECT has_column_privilege('$role', 'public.preflight_parent_target_ps', 'id', 'SELECT')"
                        $tablePriv = Invoke-PsqlScratch -Sql "SELECT has_table_privilege('$role', 'public.preflight_parent_target_ps', 'SELECT')"
                        if ($colPriv -ne 't' -or $tablePriv -ne 'f') {
                            Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "prasyarat celah: has_column=$colPriv has_table=$tablePriv (want t/f)"
                            continue
                        }
                    }
                    $verifierBefore = Get-RoleVerifier -Role $role
                    $membershipsBefore = Get-RoleMemberships -Role $role
                    $ownerBefore = ''
                    if ($case.Name -eq 'owned-table') {
                        $ownerBefore = Invoke-PsqlScratch -Sql "SELECT relowner::regrole::text FROM pg_class WHERE relname = 'preflight_owned_ps'"
                    }
                    $op = @{ Id = "pstest_pf_$($pfSuffix)"; Role = $role; Password = 'x-preflight-ps-123' }
                    $threw = $false
                    $thrownMsg = ''
                    try {
                        Invoke-OperatorPreflight -Operators @($op) -RunPsql {
                            $input | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $pfDb --no-password --no-psqlrc 2>&1
                        }
                    } catch {
                        $threw = $true
                        $thrownMsg = "$_"
                    }
                    $verifierAfter = Get-RoleVerifier -Role $role
                    $membershipsAfter = Get-RoleMemberships -Role $role
                    $ownerAfter = ''
                    if ($case.Name -eq 'owned-table') {
                        $ownerAfter = Invoke-PsqlScratch -Sql "SELECT relowner::regrole::text FROM pg_class WHERE relname = 'preflight_owned_ps'"
                    }
                    if (-not $threw) {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail 'preflight LOLOS padahal state tidak aman (harus menolak)'
                    } elseif ($thrownMsg -notmatch 'preflight operator') {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "pesan penolakan tidak tersanitasi: $thrownMsg"
                    } elseif ($case.Name -eq 'custom-parent' -and $thrownMsg -notmatch 'hanya boleh menjadi anggota umnia_operator') {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "pesan penolakan bukan allowlist parent: $thrownMsg"
                    } elseif ($verifierAfter -cne $verifierBefore) {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail 'verifier password login berubah (preflight bermutasi)'
                    } elseif ($membershipsAfter -cne $membershipsBefore) {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail 'keanggotaan login berubah (preflight bermutasi)'
                    } elseif ($case.Name -eq 'owned-table' -and $ownerAfter -cne $ownerBefore) {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'FAIL' -Detail "pemilik tabel berubah ($ownerBefore -> $ownerAfter)"
                    } else {
                        Add-Result -Name "ps-preflight-$($case.Name)" -Status 'PASS' -Detail 'ditolak sebelum mutasi; state asli utuh'
                    }
                    # Cabut seed (hanya fixture tes ini).
                    ($case.Unseed -f $role, $parent) | sudo -u postgres psql -v ON_ERROR_STOP=1 -d $pfDb --no-password --no-psqlrc 2>$null | Out-Null
                }
            }
        }
    } catch {
        Add-Result -Name 'ps-preflight' -Status 'FAIL' -Detail "$_"
    } finally {
        if ($pfDbCreated) {
            sudo -u postgres psql -tAc "DROP DATABASE IF EXISTS $pfDb" 2>$null | Out-Null
        }
    }
}

# ---- Test 6: kontrol mutasi verifier (Issue 3) ----
# Membuktikan Get-RoleVerifier (pg_authid) BENAR-BENAR mendeteksi
# perubahan password: login uji diubah password-nya (verifier HARUS
# berbeda), login kontrol yang tidak diubah (verifier HARUS sama —
# tanpa false positive). Nilai hash hanya dibandingkan di memori;
# pesan gagal menyebut nama peran saja. Login dibuat/dihapus tes ini.
if (-not $sudoOk) {
    Add-Result -Name 'verifier-mutation-control' -Status 'NOT RUN' -Detail 'sudo -u postgres psql tidak tersedia/tanpa kata sandi'
} else {
    $vSuffix = [System.Guid]::NewGuid().ToString('N').Substring(0, 8)
    $vLogin = 'umnia_op_pstest_verifier_' + $vSuffix
    $vControl = 'umnia_op_pstest_verifier_ctl_' + $vSuffix
    try {
        $vExists = sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname IN ('$vLogin', '$vControl')" 2>$null
        if ($vExists -eq '1') {
            Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail "tabrakan login verifier $vLogin/$vControl (tidak diubah)"
        } else {
            $createSql = "CREATE ROLE $vLogin LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-verifier-ps-awal-123'; CREATE ROLE $vControl LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-verifier-ps-awal-123';"
            $createSql | sudo -u postgres psql -v ON_ERROR_STOP=1 --no-password --no-psqlrc 2>$null | Out-Null
            if ($LASTEXITCODE -ne 0) {
                Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail 'gagal membuat login verifier'
            } else {
                $vBefore = Get-RoleVerifier -Role $vLogin
                $cBefore = Get-RoleVerifier -Role $vControl
                if ([string]::IsNullOrEmpty($vBefore) -or [string]::IsNullOrEmpty($cBefore)) {
                    Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail 'verifier awal kosong (pg_authid tidak terbaca)'
                } else {
                    "ALTER ROLE $vLogin WITH PASSWORD 'x-verifier-ps-ubah-456';" | sudo -u postgres psql -v ON_ERROR_STOP=1 --no-password --no-psqlrc 2>$null | Out-Null
                    if ($LASTEXITCODE -ne 0) {
                        Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail 'gagal mengubah password login uji'
                    } else {
                        $vAfter = Get-RoleVerifier -Role $vLogin
                        $cAfter = Get-RoleVerifier -Role $vControl
                        if ($vAfter -ceq $vBefore) {
                            Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail "kontrol mutasi GAGAL: perubahan password $vLogin TIDAK terdeteksi (verifier sama)"
                        } elseif ($cAfter -cne $cBefore) {
                            Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail "false positive: verifier $vControl berubah padahal password tidak diubah"
                        } else {
                            Add-Result -Name 'verifier-mutation-control' -Status 'PASS' -Detail 'perubahan password terdeteksi; kontrol tidak berubah'
                        }
                    }
                }
            }
            "DROP ROLE IF EXISTS $vLogin; DROP ROLE IF EXISTS $vControl;" | sudo -u postgres psql -v ON_ERROR_STOP=1 --no-password --no-psqlrc 2>$null | Out-Null
        }
    } catch {
        Add-Result -Name 'verifier-mutation-control' -Status 'FAIL' -Detail "$_"
        "DROP ROLE IF EXISTS $vLogin; DROP ROLE IF EXISTS $vControl;" | sudo -u postgres psql -v ON_ERROR_STOP=1 --no-password --no-psqlrc 2>$null | Out-Null
    }
}

# ---- Test 7: kegagalan ACL memblokir provisioning (Issue 1) ----
# Membuktikan tidak ada pemanggilan mutasi (Invoke-ProvisionSql)
# sesudah kegagalan ACL yang diinjeksikan. Pipeline provisioning yang
# ASLI dipanggil (Invoke-OperatorProvisioning dari salinan deployment
# sementara), dengan stub: docker (sukses untuk pg_isready + preflight),
# Lock-RosterAcl (throw = kegagalan ACL), Invoke-ProvisionSql
# (mencatat pemanggilan; tidak boleh dipanggil).
#
# Virgin checkout TIDAK punya deploy/operator-roster.env (gitignored,
# dibuat deployment) — pipeline nyata gagal "Roster tidak ada" SEBELUM
# mencapai ACL, jadi tes ini tidak boleh bergantung pada roster
# deployment. Tes ini memakai salinan deployment SEMENTARA yang dimiliki
# tes ini (roster sintetis + compose): tidak pernah membaca/menulis/
# membuat/menghapus roster deployment nyata, dan tidak pernah memanggil
# docker/SQL nyata. Salinan dihapus sesudah tes; definisi fungsi produk
# yang asli dipulihkan dengan dot-source ulang (termasuk bila gagal).
# $PSScriptRoot salinan membuat pipeline memakai roster/compose
# sintetis — terbukti oleh pesan ACL (bila roster hilang, pesannya
# "Roster tidak ada", bukan ACL).
$test7TempRoot = $null
try {
    $test7TempRoot = Join-Path ([System.IO.Path]::GetTempPath()) ('umnia-op-test7-' + [System.Guid]::NewGuid().ToString('N').Substring(0, 8))
    $test7Windows = Join-Path $test7TempRoot 'windows'
    New-Item -ItemType Directory -Force -Path $test7Windows | Out-Null
    Copy-Item -Path $provisionScript -Destination (Join-Path $test7Windows 'provision-operator.ps1')
    Copy-Item -Path (Join-Path (Split-Path $scriptDir -Parent) 'compose.yml') -Destination (Join-Path $test7Windows 'compose.yml')
    # Roster sintetis MILIK tes ini (disposable, password sintetis).
    [System.IO.File]::WriteAllText((Join-Path $test7TempRoot 'operator-roster.env'), "UMNIA_OP_FIXTURE=x-test7-synthetic-123`n")
    # Dot-source SALINAN supaya $PSScriptRoot pipeline menjadi
    # direktori salinan (roster/compose sintetis).
    . (Join-Path $test7Windows 'provision-operator.ps1')
    $script:provisionSqlCalls = 0
    function docker { $global:LASTEXITCODE = 0 }
    function Lock-RosterAcl { param([string]$RosterPath) throw 'Gagal membaca ACL roster (tahap baca ACL).' }
    function Invoke-ProvisionSql { param([string]$Sql, [string]$ComposePath, $Operators) $script:provisionSqlCalls++ }
    $threw = $false
    $thrownMsg = ''
    try {
        Invoke-OperatorProvisioning
    } catch {
        $threw = $true
        $thrownMsg = "$_"
    }
    if (-not $threw) {
        Add-Result -Name 'acl-failure-blocks-provisioning' -Status 'FAIL' -Detail 'provisioning LOLOS padahal ACL gagal (harus berhenti)'
    } elseif ($thrownMsg -notmatch 'ACL roster') {
        Add-Result -Name 'acl-failure-blocks-provisioning' -Status 'FAIL' -Detail "pesan kegagalan bukan ACL: $thrownMsg"
    } elseif ($script:provisionSqlCalls -ne 0) {
        Add-Result -Name 'acl-failure-blocks-provisioning' -Status 'FAIL' -Detail "Invoke-ProvisionSql dipanggil $($script:provisionSqlCalls)x sesudah ACL gagal (harus 0)"
    } else {
        Add-Result -Name 'acl-failure-blocks-provisioning' -Status 'PASS' -Detail 'ACL gagal -> provisioning berhenti; tidak ada pemanggilan SQL mutasi'
    }
} catch {
    Add-Result -Name 'acl-failure-blocks-provisioning' -Status 'FAIL' -Detail "$_"
} finally {
    Remove-Item Function:\docker -ErrorAction SilentlyContinue
    Remove-Item Function:\Lock-RosterAcl -ErrorAction SilentlyContinue
    Remove-Item Function:\Invoke-ProvisionSql -ErrorAction SilentlyContinue
    # Pulihkan definisi fungsi produk yang asli (salinan temp sempat
    # menimpa semuanya saat di-dot-source); guard main script mencegah
    # provisioning ikut jalan saat dot-source.
    . $provisionScript
    if ($test7TempRoot -and (Test-Path $test7TempRoot)) {
        Remove-Item $test7TempRoot -Recurse -Force -ErrorAction SilentlyContinue
    }
}
# Bukti cleanup: salinan deployment sementara harus sudah hilang.
if ($test7TempRoot) {
    if (Test-Path $test7TempRoot) {
        Add-Result -Name 'acl-failure-blocks-provisioning-cleanup' -Status 'FAIL' -Detail "salinan deployment sementara masih ada: $test7TempRoot"
    } else {
        Add-Result -Name 'acl-failure-blocks-provisioning-cleanup' -Status 'PASS' -Detail 'salinan deployment sementara dibersihkan'
    }
}

# ---- Test 8: DACL fail-closed tersanitasi (Issue 1) ----
# Membuktikan setiap kegagalan baca/ubah ACL melempar galat terminating
# yang tersanitasi (bukan galat non-terminating yang lolos dengan
# ErrorActionPreference Continue). Get/Set-RosterDacl di-stub; ACL
# palsu adalah PSCustomObject (FileSecurity asli tidak bisa dibuat di
# Linux). Dua kasus: baca gagal, dan tulis gagal (Set-RosterDacl throw
# seperti PrivilegeNotHeld/SeSecurityPrivilege).
    $aclCases = @(
        @{ Name = 'read-fails'; Want = 'Gagal membaca ACL roster' },
        @{ Name = 'write-fails'; Want = 'Gagal menulis ACL roster' }
    )
    foreach ($case in $aclCases) {
        if ($case.Name -eq 'read-fails') {
            function Get-RosterDacl { param([string]$RosterPath) throw 'injected read failure' }
            function Set-RosterDacl { param([string]$RosterPath, $Acl) throw 'must not be called' }
        } else {
            $fakeRule = [PSCustomObject]@{ IdentityReference = [PSCustomObject]@{ Value = 'S-1-1-0' } }
            $fakeAcl = [PSCustomObject]@{ Access = @($fakeRule); AreAccessRulesProtected = $true }
            $fakeAcl | Add-Member -MemberType ScriptMethod -Name 'RemoveAccessRuleSpecific' -Value { param($r) }
            function Get-RosterDacl { param([string]$RosterPath) return $fakeAcl }
            function Set-RosterDacl { param([string]$RosterPath, $Acl) throw 'injected write failure (PrivilegeNotHeld)' }
        }
        $threw = $false
        $thrownMsg = ''
        try {
            Remove-DisallowedRosterAce -RosterPath 'C:\temp\roster-fixture.env'
        } catch {
            $threw = $true
            $thrownMsg = "$_"
        }
        if (-not $threw) {
            Add-Result -Name "acl-dacl-fail-closed-$($case.Name)" -Status 'FAIL' -Detail 'Remove-DisallowedRosterAce tidak melempar (harus terminating)'
        } elseif ($thrownMsg -notmatch [regex]::Escape($case.Want)) {
            Add-Result -Name "acl-dacl-fail-closed-$($case.Name)" -Status 'FAIL' -Detail "pesan tidak tersanitasi: $thrownMsg"
        } elseif ($thrownMsg -match 'injected') {
            Add-Result -Name "acl-dacl-fail-closed-$($case.Name)" -Status 'FAIL' -Detail "pesan membocorkan detail internal: $thrownMsg"
        } else {
            Add-Result -Name "acl-dacl-fail-closed-$($case.Name)" -Status 'PASS' -Detail "throw terminating tersanitasi: $($case.Want)"
        }
        Remove-Item Function:\Get-RosterDacl -ErrorAction SilentlyContinue
        Remove-Item Function:\Set-RosterDacl -ErrorAction SilentlyContinue
    }

# ---- Ringkasan ----
$passed = @($results | Where-Object { $_.Status -eq 'PASS' }).Count
$failed = @($results | Where-Object { $_.Status -eq 'FAIL' }).Count
$notRun = @($results | Where-Object { $_.Status -eq 'NOT RUN' }).Count
Write-Host "test-operator-provisioning: $passed passed, $failed failed, $notRun not run"
if ($failed -gt 0) { exit 1 }
exit 0
