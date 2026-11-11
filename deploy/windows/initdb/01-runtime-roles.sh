#!/usr/bin/env bash
# Initdb Windows (hanya berjalan sekali, saat volume data masih kosong).
# Membuat SATU role runtime (non-superuser) yang dikonfigurasi untuk
# container ini, dan role postgres superuser yang dibutuhkan migrasi 002
# (ALTER FUNCTION ... OWNER TO postgres).
#
# Koneksi (perbaikan F1-WINDOWS-REVIEW-FIXES #1): entrypoint postgres
# menjalankan file .sh init sebagai OS user `postgres` dan hanya mengekspor
# PGPASSWORD — ia TIDAK menyetel PGUSER/PGDATABASE untuk file-file itu
# (lihat docker_process_sql di docker-entrypoint.sh: helper entrypoint
# sendiri memakai --username "$POSTGRES_USER" --dbname "$POSTGRES_DB").
# `psql` polos akan menghubungi DB user `postgres` ke DB `postgres`, yang
# tidak ada pada volume baru yang bootstrap user-nya umnia_bootstrap /
# umnia_admin_bootstrap — terbukti: FATAL: role "postgres" does not exist
# dan init container gagal. Karena itu SEMUA panggilan psql di sini memakai
# --username/--dbname eksplisit lewat psql_bootstrap().
#
# Mode eksekusi: entrypoint MENJALANKAN file .sh yang executable dan
# MEN-SOURCE file yang non-executable (mis. hasil checkout git Windows
# yang tidak mempertahankan bit +x). Skrip ini aman di kedua mode:
# `set -euo pipefail` hanya dipasang saat dieksekusi (tidak bocor ke shell
# entrypoint saat di-source), dan kegagalan fatal memakai `exit` sehingga
# container gagal start, bukan jalan setengah terinisialisasi.
if [[ "${BASH_SOURCE[0]}" == "${0}" ]]; then
  set -euo pipefail
fi

# Fail closed: tiap container hanya mendapat SATU secret runtime
# (UMNIA_RUNTIME_ROLE + UMNIA_RUNTIME_DB_PASSWORD, diset per service di
# compose.yml). Secret kosong/belum diset bukan inisialisasi sukses.
: "${POSTGRES_USER:?initdb: POSTGRES_USER (bootstrap user) is required}"
: "${POSTGRES_DB:?initdb: POSTGRES_DB is required}"
: "${POSTGRES_PASSWORD:?initdb: POSTGRES_PASSWORD is required}"
: "${UMNIA_RUNTIME_ROLE:?initdb: UMNIA_RUNTIME_ROLE is required}"
: "${UMNIA_RUNTIME_DB_PASSWORD:?initdb: UMNIA_RUNTIME_DB_PASSWORD is required}"

# Jalur roster operator: SATU override sempit untuk tes; default tetap
# mount container (/docker-operator-roster.env) dan tidak berubah bila
# variabel tidak diset. Hanya jalur file yang dioverride — format roster,
# validasi, dan perilaku fail-closed tidak berubah.
ROSTER_PATH="${UMNIA_OPERATOR_ROSTER_PATH:-/docker-operator-roster.env}"

# sql_literal <nilai>: kembalikan literal SQL single-quote dengan
# escaping '' untuk nilai mentah. Password hanya boleh diteruskan
# lewat literal ini di dalam heredoc stdin — tidak pernah lewat
# argv (-v pw=), log, atau interpolasi tanpa escaping.
sql_literal() {
  printf "'%s'" "${1//\'/\'\'}"
}

# psql sebagai bootstrap user ke database bootstrap, selalu eksplisit.
# --no-password: jangan pernah meminta password interaktif;
# --no-psqlrc: abaikan file rc pengguna.
psql_bootstrap() {
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --no-password --no-psqlrc "$@"
}

# role_exists <role>: exit 0 bila ada, 1 bila tidak ada.
# Kegagalan koneksi/query MEMATIKAN skrip (exit 1) — tidak pernah dibaca
# sebagai "role tidak ada". (Perangkap lama: $(psql ...) di dalam kondisi
# `if` menyembunyikan kegagalan koneksi sebagai hasil kosong.)
# Catatan: interpolasi variabel psql (:'role') HANYA bekerja pada input
# stdin/file, TIDAK pada argumen -c — jadi query ini lewat heredoc.
role_exists() {
  local out
  if ! out=$(psql_bootstrap -tA -v role="$1" <<SQL
SELECT 1 FROM pg_roles WHERE rolname = :'role';
SQL
); then
    echo "initdb: failed to check role '$1' (connection/query error)" >&2
    exit 1
  fi
  [ -n "$out" ]
}

# Role runtime: hanya role yang dikonfigurasi untuk container ini.
# JANGAN membuat role service lain di sini (segregasi bisnis/admin).
# Nama role lewat -v role= (bukan rahasia); password lewat literal
# stdin (sql_literal), bukan argv.

# Operator — preflight SEBELUM mutasi apa pun (termasuk pembuatan
# peran runtime di bawah). Roster di-parse sekali di sini; bila file
# tidak ada, daftar login kosong dan preflight tetap memeriksa peran
# NOLOGIN/keanggotaan yang sudah ada (volume segar lolos secara vakum).
# Peran yang tidak aman harus DITOLAK, bukan diperbaiki diam-diam
# (tanpa REVOKE/ALTER adopsi). Mode file TIDAK ditegakkan di sini:
# mount Windows tidak membawa mode Unix 0600; kontrol aksesnya adalah
# ACL Windows pada file roster (lihat deploy/windows/README.md +
# provision-operator.ps1).
ROSTER_ROLES=()
ROSTER_PASSWORDS=()

read_roster() {
  ROSTER_ROLES=()
  ROSTER_PASSWORDS=()
  if [ ! -f "$ROSTER_PATH" ]; then
    return 0
  fi
  lineno=0
  while IFS='=' read -r key value || [ -n "$key" ]; do
    lineno=$((lineno + 1))
    value="${value%$'\r'}"
    case "$key" in
      ''|'#'*) continue ;;
    esac
    case "$key" in
      UMNIA_OP_*) ;;
      *) echo "initdb: roster baris $lineno: kunci harus UMNIA_OP_<ID>" >&2; exit 1 ;;
    esac
    id="$(printf '%s' "$key" | sed 's/^UMNIA_OP_//')"
    if [ -z "$id" ] || [ "${#id}" -gt 32 ]; then
      echo "initdb: roster baris $lineno: ID operator 1-32 karakter" >&2; exit 1
    fi
    case "$id" in
      *[!A-Za-z0-9_]*)
        echo "initdb: roster baris $lineno: ID operator hanya [A-Za-z0-9_]" >&2; exit 1 ;;
    esac
    id="$(printf '%s' "$id" | tr 'A-Z' 'a-z')"
    case "$id" in
      operator|grant_exec)
        echo "initdb: roster baris $lineno: ID '$id' menyerupai peran cadangan" >&2; exit 1 ;;
    esac
    if [ -z "$value" ] || [ "${#value}" -lt 12 ]; then
      echo "initdb: roster baris $lineno: password operator minimal 12 karakter" >&2; exit 1
    fi
    ROSTER_ROLES+=("umnia_op_$id")
    ROSTER_PASSWORDS+=("$value")
  done < "$ROSTER_PATH"
}

preflight_sql() {
  local roles_list="'umnia_operator', 'umnia_grant_exec'"
  local logins_list=""
  local role
  for role in "${ROSTER_ROLES[@]}"; do
    roles_list="$roles_list, '$role'"
    if [ -n "$logins_list" ]; then logins_list="$logins_list, "; fi
    logins_list="$logins_list'$role'"
  done
  local v_roles="ARRAY[$roles_list]"
  local v_logins="ARRAY[$logins_list]"
  if [ -z "$logins_list" ]; then v_logins="ARRAY[]::text[]"; fi
  local sql
  sql="$(cat <<'SQL'
DO $preflight$
DECLARE
  v_login text;
  v_roles text[] := __V_ROLES__;
  v_logins text[] := __V_LOGINS__;
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
  IF EXISTS (SELECT 1 FROM pg_proc p
              JOIN pg_roles r ON r.oid = p.proowner
              JOIN pg_namespace n ON n.oid = p.pronamespace
             WHERE r.rolname = ANY(v_roles)
               AND NOT (r.rolname = 'umnia_grant_exec' AND n.nspname = 'app' AND p.proname = 'grant_module_entitlement')) THEN
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
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_grant_exec') THEN
    IF EXISTS (
      SELECT 1 FROM pg_class c
      JOIN pg_namespace n ON n.oid = c.relnamespace AND n.nspname = 'public'
      WHERE c.relkind IN ('r', 'p')
        AND (has_table_privilege('umnia_grant_exec', c.oid, 'DELETE')
             OR has_table_privilege('umnia_grant_exec', c.oid, 'TRUNCATE')
             OR (c.relname IN ('operator_audit_events', 'module_entitlements')
                 AND has_table_privilege('umnia_grant_exec', c.oid, 'UPDATE')))
    ) THEN
      RAISE EXCEPTION 'preflight operator: umnia_grant_exec tidak boleh DELETE/TRUNCATE/UPDATE tabel audit/entitlement';
    END IF;
  END IF;
END;
$preflight$;
SQL
)"
  sql="${sql//__V_ROLES__/$v_roles}"
  sql="${sql//__V_LOGINS__/$v_logins}"
  printf '%s\n' "$sql"
}

preflight_operator() {
  local out
  if ! out="$(preflight_sql | psql_bootstrap 2>&1)"; then
    echo "initdb: preflight operator GAGAL (state asli dipertahankan):" >&2
    echo "$out" | grep -o "preflight operator: [^\"']*" | head -1 >&2
    echo "initdb: perbaiki state peran operator yang tidak aman secara manual sebelum menjalankan provisioning." >&2
    exit 1
  fi
}

read_roster
preflight_operator

if role_exists "$UMNIA_RUNTIME_ROLE"; then
  echo "initdb: role $UMNIA_RUNTIME_ROLE already exists, skipping" >&2
else
  runtime_pw="$(sql_literal "$UMNIA_RUNTIME_DB_PASSWORD")"
  psql_bootstrap -v role="$UMNIA_RUNTIME_ROLE" <<SQL
CREATE ROLE :"role" NOSUPERUSER NOBYPASSRLS LOGIN PASSWORD $runtime_pw;
SQL
fi

# Role postgres superuser harus ada agar migrasi 002 bisa mengalihkan
# kepemilikan fungsi helper ke postgres (lihat migrasi 002).
if role_exists postgres; then
  echo "initdb: role postgres already exists, skipping" >&2
else
  bootstrap_pw="$(sql_literal "$POSTGRES_PASSWORD")"
  psql_bootstrap <<SQL
CREATE ROLE postgres SUPERUSER LOGIN PASSWORD $bootstrap_pw;
SQL
fi

# Operator (F1-OPERATOR-ENTITLEMENT-IMPLEMENT): hanya untuk container
# bisnis — file roster di-mount read-only HANYA ke service
# postgres-business (compose.yml); container admin tidak mendapat mount
# itu dan melewati bagian ini. Roster boleh kosong: peran NOLOGIN tetap
# dibuat, tanpa login dan tanpa password default. Preflight sudah
# berjalan di atas (sebelum peran runtime dibuat): peran yang sudah ada
# dan tidak aman sudah ditolak di sana, jadi bagian ini HANYA membuat
# peran yang belum ada dan merotasi password login sehat yang sudah
# dikenal — tanpa ALTER atribut dan tanpa REVOKE adopsi.
if [ -f "$ROSTER_PATH" ]; then
  ensure_op_nologin_role() {
    if role_exists "$1"; then
      return 0
    else
      psql_bootstrap <<SQL
CREATE ROLE $1 NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;
SQL
    fi
  }

  ensure_op_login() {
    local pw_literal
    pw_literal="$(sql_literal "$2")"
    if role_exists "$1"; then
      psql_bootstrap <<SQL
ALTER ROLE $1 WITH PASSWORD $pw_literal;
SQL
    else
      psql_bootstrap <<SQL
CREATE ROLE $1 LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD $pw_literal;
SQL
    fi
    psql_bootstrap <<SQL
GRANT umnia_operator TO $1;
SQL
  }

  ensure_op_nologin_role umnia_operator
  ensure_op_nologin_role umnia_grant_exec
  i=0
  for role in "${ROSTER_ROLES[@]}"; do
    ensure_op_login "$role" "${ROSTER_PASSWORDS[$i]}"
    i=$((i + 1))
  done
fi
