# Provision operator UMNIA pada volume Windows yang SUDAH ADA (sebelum 011).
# Dijalankan deployment operator di host Windows, SEBELUM service migrate
# menerapkan 011 (011 gagal tertutup bila peran belum ada).
#
# Penggunaan (PowerShell 5.1, dari root repo):
#   powershell -NoProfile -ExecutionPolicy Bypass -File deploy\windows\provision-operator.ps1
#
# Membaca SATU roster deployment: deploy\operator-roster.env
# (gitignored, jangan commit). Format per baris: UMNIA_OP_<ID>=<password>
# (ID 1-32 karakter [A-Za-z0-9_]; password minimal 12 karakter).
# Roster boleh kosong: peran NOLOGIN tetap dibuat, tanpa login dan tanpa
# password default. Idempotent: aman dijalankan ulang.
# Password TIDAK pernah masuk argv/log: diteruskan ke psql lewat stdin
# (skrip SQL di memori, dengan escape quote dan tag dollar-quote acak),
# bukan -v/argumen. Byte password dipertahankan apa adanya (hanya CR
# baris yang dibuang); spasi di awal/akhir password bukan pemisah.
# $ErrorActionPreference sengaja 'Continue': pada PS 5.1, stderr native
# + 'Stop' menjadi terminating error sebelum $LASTEXITCODE terbaca;
# setiap pemanggilan native diperiksa eksplisit lewat $LASTEXITCODE.
# Skrip ini terstruktur sebagai fungsi + main guard, supaya
# deploy/windows/tests/test-operator-provisioning.ps1 bisa dot-source
# dan menguji fungsi-fungsinya tanpa menjalankan provisioning.
$ErrorActionPreference = 'Continue'

function Get-CurrentPrincipal {
    # SID pengguna saat ini — bentuk paling stabil untuk icacls.
    # Fallback ke nama pengguna bila SID tidak bisa dibaca.
    try {
        $sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        if ($sid) { return $sid }
    } catch {
    }
    return $env:USERNAME
}

function Format-IcaclsGrantArg {
    param([string]$Principal)
    # icacls membedakan nama akun dan SID numerik: SID HARUS diawali
    # '*' (tanpa itu icacls mencari nama akun dan gagal 1332 —
    # "No mapping between account names and security IDs was done").
    # Nama pengguna biasa tidak memakai '*'.
    if ($Principal -match '^S-1-') { return "*${Principal}:(F)" }
    return "${Principal}:(F)"
}

function Get-AllowedRosterSids {
    # Himpunan pembaca roster yang diizinkan (didokumentasikan):
    # pengguna provisioning saat ini (FullControl) + pengecualian
    # tepercaya SYSTEM (S-1-5-18) dan Administrators (S-1-5-32-544)
    # untuk layanan OS/administrasi. Semua ACE eksplisit lain dihapus
    # oleh Remove-DisallowedRosterAce.
    $sids = @()
    try {
        $sid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
        if ($sid) { $sids += $sid }
    } catch {
    }
    $sids += 'S-1-5-18'
    $sids += 'S-1-5-32-544'
    return $sids
}

function Get-RosterDacl {
    param([string]$RosterPath)
    # Baca DACL SAJA (Access) — tidak menyentuh SACL/audit, jadi
    # operasi ini didukung pemilik file tanpa hak istimewa
    # SeSecurityPrivilege. Set-Acl gagal di sini: ia menulis deskriptor
    # keamanan penuh (termasuk SACL) dan butuh hak audit.
    return [IO.File]::GetAccessControl($RosterPath, [Security.AccessControl.AccessControlSections]::Access)
}

function Set-RosterDacl {
    param([string]$RosterPath, $Acl)
    # Tulis DACL SAJA (Access) — jalur yang didukung pemilik file
    # tanpa elevasi. Dipisah sebagai fungsi supaya tes bisa
    # menginjeksikan kegagalan tulis dan membuktikan fail-closed.
    [IO.File]::SetAccessControl($RosterPath, $Acl)
}

function Remove-DisallowedRosterAce {
    param([string]$RosterPath)
    # Hapus ACE eksplisit yang bukan milik himpunan pembaca yang
    # diizinkan. /inheritance:r + /grant:r saja MEMPERTAHANKAN ACE
    # eksplisit yang tidak terkait (mis. Everyone:Read) — itulah
    # yang ditutup di sini. Perbandingan dikanonikalisasi ke
    # SecurityIdentifier sebelum dibandingkan (Get-Acl bisa
    # mengembalikan NTAccount, bukan SID).
    # Windows saja (dijaga pemanggil Lock-RosterAcl): di Linux tidak
    # ada ACL Windows untuk ditegakkan. Setiap kegagalan baca/ubah/
    # verifikasi melempar galat terminating yang tersanitasi (bukan
    # galat non-terminating yang lolos dengan ErrorActionPreference
    # Continue lalu membiarkan SQL provisioning jalan).
    $allowed = Get-AllowedRosterSids
    try {
        $acl = Get-RosterDacl -RosterPath $RosterPath
    } catch {
        throw 'Gagal membaca ACL roster (tahap baca ACL).'
    }
    $toRemove = @()
    foreach ($rule in @($acl.Access)) {
        try {
            $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
        } catch {
            $sid = $rule.IdentityReference.Value
        }
        if ($allowed -notcontains $sid) { $toRemove += $rule }
    }
    if ($toRemove.Count -gt 0) {
        foreach ($rule in $toRemove) {
            [void]$acl.RemoveAccessRuleSpecific($rule)
        }
        try {
            Set-RosterDacl -RosterPath $RosterPath -Acl $acl
        } catch {
            throw 'Gagal menulis ACL roster (tahap ubah ACL).'
        }
    }
    # Verifikasi pasca: baca ulang dan tegakkan inheritance yang
    # dilindungi, semua ACE tersisa milik himpunan yang diizinkan, dan
    # pemanggil punya FullControl. Gagal verifikasi = throw.
    try {
        $after = Get-RosterDacl -RosterPath $RosterPath
    } catch {
        throw 'Gagal memverifikasi ACL roster (tahap verifikasi baca).'
    }
    if (-not $after.AreAccessRulesProtected) {
        throw 'Verifikasi ACL roster gagal: inheritance tidak dilindungi.'
    }
    $callerSid = $null
    try {
        $callerSid = [System.Security.Principal.WindowsIdentity]::GetCurrent().User.Value
    } catch {
    }
    if (-not $callerSid) {
        throw 'Verifikasi ACL roster gagal: SID pemanggil tidak bisa dibaca.'
    }
    $callerRights = [System.Security.AccessControl.FileSystemRights]0
    foreach ($rule in @($after.Access)) {
        try {
            $sid = $rule.IdentityReference.Translate([System.Security.Principal.SecurityIdentifier]).Value
        } catch {
            $sid = $rule.IdentityReference.Value
        }
        if ($allowed -notcontains $sid) {
            throw 'Verifikasi ACL roster gagal: ACE di luar himpunan yang diizinkan masih ada.'
        }
        if ($sid -eq $callerSid) {
            $callerRights = $callerRights -bor $rule.FileSystemRights
        }
    }
    if (($callerRights -band [System.Security.AccessControl.FileSystemRights]::FullControl) -ne [System.Security.AccessControl.FileSystemRights]::FullControl) {
        throw 'Verifikasi ACL roster gagal: pemanggil tidak punya FullControl.'
    }
}

function Lock-RosterAcl {
    param([string]$RosterPath)
    # Kunci ACL file roster ke pengguna saat ini saja (idempotent).
    # Prinsipal HARUS dalam variabel sendiri: "$env:USERNAME:(F)"
    # rusak di PS 5.1 — tokenizer memakan ':' ke dalam token variabel
    # sehingga argumen yang sampai ke icacls menjadi "(F)".
    # Bentuk "${principal}:(F)" aman. SID numerik memakai prefix '*'
    # (Format-IcaclsGrantArg) — tanpanya icacls gagal 1332.
    # Sesudah icacls, tegakkan himpunan pembaca yang diizinkan
    # (hapus ACE eksplisit yang tidak terkait). Windows saja: di Linux
    # (tes dengan icacls stub) tidak ada ACL Windows untuk ditegakkan,
    # jadi penegakan DACL dilewati — icacls stub tetap diperiksa.
    $principal = Get-CurrentPrincipal
    $grantArg = Format-IcaclsGrantArg -Principal $principal
    $aclOut = icacls $RosterPath /inheritance:r /grant:r $grantArg 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "Gagal mengunci ACL roster (exit $LASTEXITCODE): $aclOut"
    }
    if ([System.Environment]::OSVersion.Platform -eq [System.PlatformID]::Win32NT) {
        Remove-DisallowedRosterAce -RosterPath $RosterPath
    }
}

function Read-OperatorRoster {
    param([string]$RosterPath)
    # Parse roster baris demi baris (tidak pernah di-source/di-eval).
    $operators = @()
    $lineno = 0
    foreach ($rawLine in (Get-Content -Path $RosterPath)) {
        $lineno++
        # HANYA buang CR baris (CRLF Windows). TrimEnd() tanpa argumen
        # ikut membuang spasi/tab di akhir password dan mengubah byte
        # rahasia — jangan pernah dipakai pada baris roster.
        $line = $rawLine.TrimEnd("`r")
        if ($line -eq '' -or $line.StartsWith('#')) { continue }
        $idx = $line.IndexOf('=')
        if ($idx -le 0) {
            throw "Roster baris ${lineno}: harus UMNIA_OP_<ID>=<password>"
        }
        $key = $line.Substring(0, $idx)
        $value = $line.Substring($idx + 1)
        if ($key -notmatch '^UMNIA_OP_([A-Za-z0-9_]{1,32})$') {
            throw "Roster baris ${lineno}: kunci harus UMNIA_OP_<ID> (ID 1-32 karakter [A-Za-z0-9_])"
        }
        $id = $Matches[1].ToLower()
        if ($id -eq 'operator' -or $id -eq 'grant_exec') {
            throw "Roster baris ${lineno}: ID '$id' menyerupai peran cadangan"
        }
        if ([string]::IsNullOrEmpty($value) -or $value.Length -lt 12) {
            throw "Roster baris ${lineno}: password operator minimal 12 karakter"
        }
        $operators += @{ Id = $id; Role = "umnia_op_$id"; Password = $value }
    }
    return $operators
}

function Get-OperatorPreflightSql {
    param($Operators)
    # DO preflight operator (fail-closed SEBELUM mutasi apa pun).
    # Template memakai here-string single-quote (tanpa ekspansi
    # variabel — $preflight$ harus sampai ke psql apa adanya), lalu
    # placeholder __V_ROLES__/__V_LOGINS__ diganti literal array
    # (Replace literal, bukan -replace, supaya $ tidak diartikan).
    $roles = @("'umnia_operator'", "'umnia_grant_exec'")
    $logins = @()
    foreach ($op in $Operators) {
        $roles += "'$($op.Role)'"
        $logins += "'$($op.Role)'"
    }
    $vRoles = 'ARRAY[' + ($roles -join ', ') + ']'
    if ($logins.Count -eq 0) { $vLogins = 'ARRAY[]::text[]' } else { $vLogins = 'ARRAY[' + ($logins -join ', ') + ']' }
    $sql = @'
DO $preflight$
DECLARE
  v_login text;
  v_roles text[] := __V_ROLES__;
  v_logins text[] := __V_LOGINS__;
  v_finance_delete_ok boolean;
BEGIN
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('umnia_operator', 'umnia_grant_exec')
             AND (rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication)) THEN
    RAISE EXCEPTION 'preflight operator: peran operator harus NOLOGIN/NOSUPERUSER/NOBYPASSRLS/NOCREATEROLE/NOCREATEDB/NOREPLICATION';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member WHERE r.rolname = 'umnia_grant_exec') THEN
    RAISE EXCEPTION 'preflight operator: umnia_grant_exec tidak boleh menjadi anggota peran lain';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.roleid WHERE r.rolname = 'umnia_grant_exec') THEN
    RAISE EXCEPTION 'preflight operator: tidak ada peran yang boleh menjadi anggota umnia_grant_exec';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member WHERE r.rolname = 'umnia_operator') THEN
    RAISE EXCEPTION 'preflight operator: umnia_operator tidak boleh menjadi anggota peran lain';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members m
              JOIN pg_roles mr ON mr.oid = m.member
              JOIN pg_roles gr ON gr.oid = m.roleid
             WHERE mr.rolname IN ('umnia_business', 'umnia_admin')
               AND gr.rolname IN ('umnia_operator', 'umnia_grant_exec')) THEN
    RAISE EXCEPTION 'preflight operator: umnia_business/umnia_admin tidak boleh menjadi anggota peran operator';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_auth_members m
              JOIN pg_roles gr ON gr.oid = m.roleid AND gr.rolname = 'umnia_operator'
              JOIN pg_roles r ON r.oid = m.member
             WHERE NOT r.rolcanlogin OR r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication) THEN
    RAISE EXCEPTION 'preflight operator: anggota umnia_operator harus LOGIN least-privilege';
  END IF;
  -- Allowlist parent: setiap anggota umnia_operator HANYA boleh
  -- menjadi anggota umnia_operator. Parent kustom (mis. peran NOLOGIN
  -- yang memberi SELECT(id) lewat pewarisan) lolos dari pemeriksaan
  -- hak tabel/kolom langsung — has_table_privilege tidak melihat hak
  -- kolom, dan attacl hanya mencatat peran yang diberi hak langsung.
  IF EXISTS (SELECT 1 FROM pg_auth_members m
              JOIN pg_roles parent_role ON parent_role.oid = m.roleid
             WHERE parent_role.rolname <> 'umnia_operator'
               AND m.member IN (SELECT member FROM pg_auth_members
                                 JOIN pg_roles gr ON gr.oid = roleid
                                WHERE gr.rolname = 'umnia_operator')) THEN
    RAISE EXCEPTION 'preflight operator: anggota umnia_operator hanya boleh menjadi anggota umnia_operator';
  END IF;
  FOREACH v_login IN ARRAY v_logins LOOP
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_login
               AND (NOT rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication)) THEN
      RAISE EXCEPTION 'preflight operator: login % harus LOGIN least-privilege', v_login;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_auth_members m
                JOIN pg_roles mr ON mr.oid = m.member AND mr.rolname = v_login
                JOIN pg_roles gr ON gr.oid = m.roleid
               WHERE gr.rolname IN ('umnia_grant_exec', 'umnia_business', 'umnia_admin')) THEN
      RAISE EXCEPTION 'preflight operator: login % tidak boleh menjadi anggota umnia_grant_exec/umnia_business/umnia_admin', v_login;
    END IF;
    -- Allowlist parent (lihat komentar di atas): login roster HANYA
    -- boleh menjadi anggota umnia_operator. Parent lain apa pun —
    -- termasuk parent kustom yang memberi hak lewat pewarisan —
    -- ditolak sebelum mutasi.
    IF EXISTS (SELECT 1 FROM pg_auth_members m
                JOIN pg_roles mr ON mr.oid = m.member AND mr.rolname = v_login
                JOIN pg_roles gr ON gr.oid = m.roleid
               WHERE gr.rolname <> 'umnia_operator') THEN
      RAISE EXCEPTION 'preflight operator: login % hanya boleh menjadi anggota umnia_operator', v_login;
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = v_login) THEN
      IF EXISTS (
        SELECT 1 FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
        WHERE c.relkind IN ('r', 'p')
          AND c.relname IN ('workspaces', 'module_entitlements', 'operator_audit_events', 'module_registry', 'schema_migrations')
          AND (has_table_privilege(v_login, c.oid, 'SELECT')
               OR has_table_privilege(v_login, c.oid, 'INSERT')
               OR has_table_privilege(v_login, c.oid, 'UPDATE')
               OR has_table_privilege(v_login, c.oid, 'DELETE'))
      ) THEN
        RAISE EXCEPTION 'preflight operator: login % tidak boleh memiliki hak tabel', v_login;
      END IF;
      IF EXISTS (
        SELECT 1 FROM pg_attribute a
        JOIN pg_class c ON c.oid = a.attrelid
        JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
        WHERE c.relname IN ('workspaces', 'module_entitlements', 'operator_audit_events', 'module_registry', 'schema_migrations')
          AND a.attacl IS NOT NULL
          AND EXISTS (SELECT 1 FROM unnest(a.attacl) AS x WHERE x::text LIKE v_login || '=%')
      ) THEN
        RAISE EXCEPTION 'preflight operator: login % tidak boleh memiliki hak kolom', v_login;
      END IF;
    END IF;
  END LOOP;
  IF EXISTS (SELECT 1 FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba WHERE r.rolname = ANY(v_roles)) THEN
    RAISE EXCEPTION 'preflight operator: peran operator tidak boleh memiliki database';
  END IF;
  -- Kepemilikan fungsi HANYA untuk tiga tanda tangan eksak milik
  -- umnia_grant_exec (011 + 014): overload dengan nama sama tapi
  -- tanda tangan lain tetap ditolak.
  IF EXISTS (SELECT 1 FROM pg_proc p
              JOIN pg_roles r ON r.oid = p.proowner
              JOIN pg_namespace n ON n.oid = p.pronamespace
             WHERE r.rolname = ANY(v_roles)
               AND NOT (r.rolname = 'umnia_grant_exec' AND n.nspname = 'app'
                        AND p.oid::regprocedure::text IN ('app.grant_module_entitlement(uuid,text,text,text)',
                                                          'app.grant_finance_entitlement(uuid,text,text,text)',
                                                          'app.revoke_finance_entitlement(uuid,text,text)'))) THEN
    RAISE EXCEPTION 'preflight operator: peran operator memiliki fungsi di luar rutin grant';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE r.rolname = ANY(v_roles)) THEN
    RAISE EXCEPTION 'preflight operator: peran operator memiliki tabel/indeks di DB ini';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner WHERE r.rolname = ANY(v_roles)) THEN
    RAISE EXCEPTION 'preflight operator: peran operator memiliki skema di DB ini';
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_operator') THEN
    IF EXISTS (
      SELECT 1 FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
      WHERE c.relkind IN ('r', 'p')
        AND c.relname IN ('workspaces', 'module_entitlements', 'operator_audit_events', 'module_registry', 'schema_migrations')
        AND (has_table_privilege('umnia_operator', c.oid, 'SELECT')
             OR has_table_privilege('umnia_operator', c.oid, 'INSERT')
             OR has_table_privilege('umnia_operator', c.oid, 'UPDATE')
             OR has_table_privilege('umnia_operator', c.oid, 'DELETE'))
    ) THEN
      RAISE EXCEPTION 'preflight operator: umnia_operator tidak boleh memiliki hak tabel';
    END IF;
    IF EXISTS (
      SELECT 1 FROM pg_attribute a
      JOIN pg_class c ON c.oid = a.attrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
      WHERE c.relname IN ('workspaces', 'module_entitlements', 'operator_audit_events', 'module_registry', 'schema_migrations')
        AND a.attacl IS NOT NULL
        AND EXISTS (SELECT 1 FROM unnest(a.attacl) AS x WHERE x::text LIKE 'umnia_operator=%')
    ) THEN
      RAISE EXCEPTION 'preflight operator: umnia_operator tidak boleh memiliki hak kolom';
    END IF;
  END IF;
  -- DELETE terbatas 014 diizinkan hanya bila upgrade 014 valid:
  -- marker ada, RLS AKTIF + FORCE atas module_entitlements, dan
  -- SATU-SATUNYA jalur DELETE/ALL atas tabel itu adalah kebijakan
  -- bernama 014 yang bentuknya persis disetujui (lihat bawah).
  -- Tanpa itu, DELETE apa pun tetap ditolak seperti sebelum 014.
  -- Penjaga pg_class di sini penting: preflight juga jalan di DB
  -- yang tabelnya belum ada (klaster segar), dan acuan langsung ke
  -- public.schema_migrations / 'public.module_entitlements'::regclass
  -- akan galat bila tabelnya tidak ada.
  v_finance_delete_ok := false;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_grant_exec')
     AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'schema_migrations' AND relnamespace = 'public'::regnamespace)
     AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'module_entitlements' AND relnamespace = 'public'::regnamespace) THEN
    -- DELETE atas module_entitlements diizinkan HANYA bila bentuknya
    -- persis yang disetujui 014: marker 014 ada, RLS AKTIF (enabled)
    -- DAN FORCE (DISABLE RLS saja membuat relforcerowsecurity tetap
    -- true), dan SATU-SATUNYA jalur DELETE/ALL adalah kebijakan
    -- bernama yang disetujui — polcmd 'd', permissive, peran
    -- TEPAT {umnia_grant_exec} (bukan ANY — menolak PUBLIC {0} dan
    -- campuran), predikat SAMA PERSIS dengan bentuk kanonis PG16
    -- (bukan LIKE — menolak module_id <> 'finance.basic'), tanpa
    -- WITH CHECK. Kebijakan DELETE/ALL lain apa pun (FOR ALL '*',
    -- PUBLIC, peran campuran, restriktif tak dikenal) menolak.
    v_finance_delete_ok := EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '014_finance_operator_entitlement')
      AND EXISTS (SELECT 1 FROM pg_class WHERE relname = 'module_entitlements' AND relnamespace = 'public'::regnamespace AND relrowsecurity AND relforcerowsecurity)
      AND EXISTS (SELECT 1 FROM pg_policy
                   WHERE polrelid = 'public.module_entitlements'::regclass
                     AND polname = 'entitlements_exec_owner_delete_finance'
                     AND polcmd = 'd' AND polpermissive
                     AND polroles = ARRAY['umnia_grant_exec'::regrole::oid]
                     AND pg_get_expr(polqual, polrelid) = '((module_id = ''finance.basic''::text) AND (version = ''0.1.0''::text))'
                     AND polwithcheck IS NULL)
      AND NOT EXISTS (SELECT 1 FROM pg_policy
                       WHERE polrelid = 'public.module_entitlements'::regclass AND polcmd IN ('d', '*')
                         AND polname <> 'entitlements_exec_owner_delete_finance');
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_grant_exec') THEN
    IF EXISTS (
      SELECT 1 FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
      WHERE c.relkind IN ('r', 'p')
        AND ((has_table_privilege('umnia_grant_exec', c.oid, 'DELETE')
              AND NOT (c.relname = 'module_entitlements' AND v_finance_delete_ok))
             OR has_table_privilege('umnia_grant_exec', c.oid, 'TRUNCATE')
             OR (c.relname IN ('operator_audit_events', 'module_entitlements')
                 AND has_table_privilege('umnia_grant_exec', c.oid, 'UPDATE')))
    ) THEN
      RAISE EXCEPTION 'preflight operator: umnia_grant_exec tidak boleh DELETE/TRUNCATE/UPDATE tabel audit/entitlement';
    END IF;
  END IF;
END;
$preflight$;
'@
    $sql = $sql.Replace('__V_ROLES__', $vRoles).Replace('__V_LOGINS__', $vLogins)
    return $sql
}

function Invoke-OperatorPreflight {
    param($Operators, [scriptblock]$RunPsql)
    # Jalankan preflight SEBELUM mutasi apa pun. $RunPsql adalah
    # pengangkut psql pemanggil (docker exec di produksi; sudo psql di
    # tes Linux): ia menerima SQL lewat stdin dan mengembalikan
    # $LASTEXITCODE + output. Pesan galat hanya berisi nama peran
    # (preflight tidak pernah membawa password).
    $sql = Get-OperatorPreflightSql -Operators $Operators
    $out = $sql | & $RunPsql 2>&1
    if ($LASTEXITCODE -ne 0) {
        $msg = "$out" | Select-String -Pattern 'preflight operator: [^"'''']*' | Select-Object -First 1
        if ($msg) {
            throw "preflight operator GAGAL (state asli dipertahankan): $($msg.Matches[0].Value) — perbaiki state peran operator yang tidak aman secara manual sebelum menjalankan provisioning."
        }
        throw "preflight operator GAGAL (state asli dipertahankan, exit $LASTEXITCODE)."
    }
}

function New-ProvisionSqlTag {
    # Tag dollar-quote acak per pemakaian (128-bit RNG kriptografis).
    # Tag memakai huruf/angka/underscore saja, jadi valid sebagai
    # pembatas dollar-quote PostgreSQL.
    $bytes = New-Object byte[] 16
    [System.Security.Cryptography.RandomNumberGenerator]::Create().GetBytes($bytes)
    $hex = -join ($bytes | ForEach-Object { $_.ToString('x2') })
    return "umnia_pw_$hex"
}

function Build-OperatorProvisionSql {
    param($Operators)
    # Bangun skrip SQL di memori. Kata sandi di-escape quote (''),
    # tidak pernah masuk argv; psql tidak meng-echo input (tanpa -e).
    $sql = New-Object System.Text.StringBuilder
    foreach ($role in @('umnia_operator', 'umnia_grant_exec')) {
        # Buat bila belum ada; bila SUDAH ada, TIDAK diubah (preflight
        # sudah memvalidasi atributnya aman — ALTER di sini akan
        # menjadi perbaikan/adopsi diam-diam atas state yang tidak aman).
        [void]$sql.AppendLine('DO $$ BEGIN')
        [void]$sql.AppendLine("  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$role') THEN")
        [void]$sql.AppendLine("    CREATE ROLE $role NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;")
        [void]$sql.AppendLine('  END IF;')
        [void]$sql.AppendLine('END $$;')
    }
    foreach ($op in $Operators) {
        # Tag dollar-quote acak PER OPERATOR: password yang mengandung
        # $$ tidak lagi bisa menutup badan DO lebih awal (tag tetap
        # $$ akan tertutup password semacam itu). Bila password
        # mengandung tag yang terpilih, regenerasi sampai tidak ada
        # tabrakan (peluang praktis nol; 8 percobaan sebagai pengaman).
        $tag = $null
        for ($attempt = 0; $attempt -lt 8; $attempt++) {
            $candidate = New-ProvisionSqlTag
            if (-not $op.Password.Contains($candidate)) {
                $tag = $candidate
                break
            }
        }
        if (-not $tag) {
            throw "Tidak dapat memilih tag dollar-quote yang aman untuk $($op.Role)"
        }
        $pw = $op.Password.Replace("'", "''")
        [void]$sql.AppendLine('DO $' + $tag + '$ BEGIN')
        [void]$sql.AppendLine("  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '$($op.Role)') THEN")
        [void]$sql.AppendLine("    CREATE ROLE $($op.Role) LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD '$pw';")
        [void]$sql.AppendLine('  ELSE')
        # Rotasi password SAJA untuk login sehat yang sudah dikenal
        # (preflight sudah memvalidasi atribut/keanggotaan aman —
        # tanpa ALTER atribut dan tanpa REVOKE adopsi).
        [void]$sql.AppendLine("    ALTER ROLE $($op.Role) WITH PASSWORD '$pw';")
        [void]$sql.AppendLine('  END IF;')
        [void]$sql.AppendLine('END $' + $tag + '$;')
        [void]$sql.AppendLine("GRANT umnia_operator TO $($op.Role);")
    }
    return $sql.ToString()
}

function Protect-Output {
    param([string]$Text, $Operators)
    # Saring password dari output gagal sebelum ditampilkan.
    $safe = $Text
    foreach ($op in $Operators) {
        if ($op.Password -ne '') { $safe = $safe.Replace($op.Password, '[REDACTED]') }
    }
    return $safe
}

function Invoke-ProvisionSql {
    param([string]$Sql, [string]$ComposePath, $Operators)
    # Terapkan skrip provisioning peran ke postgres-business lewat
    # stdin (password tidak pernah argv). Dipisah sebagai fungsi supaya
    # tes bisa membuktikan tidak ada pemanggilan mutasi ini sesudah
    # kegagalan ACL/preflight. Kegagalan di sini melempar (terminating)
    # dengan output yang sudah disaring password-nya.
    $out = $Sql | docker compose -f $ComposePath exec -T postgres-business psql -v ON_ERROR_STOP=1 --username umnia_bootstrap --dbname umnia_business --no-password --no-psqlrc 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "provision-operator gagal (exit $LASTEXITCODE): $(Protect-Output -Text "$out" -Operators $Operators)"
    }
}

function Invoke-OperatorProvisioning {
    $scriptDir = $PSScriptRoot
    $roster = Join-Path (Split-Path $scriptDir -Parent) 'operator-roster.env'
    $composeFile = Join-Path $scriptDir 'compose.yml'

    if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
        throw 'docker tidak ditemukan di PATH.'
    }
    if (-not (Test-Path $roster)) {
        throw "Roster operator tidak ada: $roster — buat dulu (lihat deploy/windows/README.md)."
    }
    if (-not (Test-Path $composeFile)) {
        throw "compose.yml tidak ditemukan: $composeFile"
    }

    $operators = Read-OperatorRoster -RosterPath $roster

    # postgres-business harus jalan; migrasi 011 BELUM boleh jalan sebelum ini.
    $readyOut = docker compose -f $composeFile exec -T postgres-business pg_isready -U umnia_bootstrap -d umnia_business 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "postgres-business belum siap (exit $LASTEXITCODE): $readyOut — jalankan docker compose up -d postgres-business dulu."
    }

    # Preflight operator SEBELUM mutasi apa pun (termasuk penguncian
    # ACL file roster dan pembuatan/perubahan peran): state peran yang
    # tidak aman DITOLAK, bukan diperbaiki diam-diam.
    $runPsql = {
        $input | docker compose -f $composeFile exec -T postgres-business psql -v ON_ERROR_STOP=1 --username umnia_bootstrap --dbname umnia_business --no-password --no-psqlrc 2>&1
    }
    Invoke-OperatorPreflight -Operators $operators -RunPsql $runPsql

    Lock-RosterAcl -RosterPath $roster
    $sql = Build-OperatorProvisionSql -Operators $operators

    Invoke-ProvisionSql -Sql $sql -ComposePath $composeFile -Operators $operators

    $names = ($operators | ForEach-Object { $_.Role }) -join ', '
    Write-Host "Operator roles provisioned: umnia_operator, umnia_grant_exec$(if ($names) { ", $names" })."
    Write-Host 'Langkah berikut: docker compose up migrate (menerapkan 011), lalu uji CLI operator (lihat deploy/windows/README.md).'
}

if ($MyInvocation.InvocationName -ne '.') {
    Invoke-OperatorProvisioning
}
