#!/usr/bin/env bash
# F1-02 — siapkan PostgreSQL Linux native (tanpa Docker) untuk UMNIA.
# Membuat role + database TERPISAH untuk bisnis dan admin, sesuai
# ADR-003 (segregasi admin plane vs business plane).
#
# Penggunaan (di host Linux, sebagai user dengan akses sudo ke postgres):
#   UMNIA_BUSINESS_DB_PASSWORD='...' UMNIA_ADMIN_DB_PASSWORD='...' \
#     bash deploy/linux/configure-postgres.sh
#
# Password TIDAK boleh di-hardcode di script ini dan TIDAK boleh di-commit.
# Script idempotent: role/database yang sudah ada tidak dibuat ulang.
set -euo pipefail

: "${UMNIA_BUSINESS_DB_PASSWORD:?set UMNIA_BUSINESS_DB_PASSWORD}"
: "${UMNIA_ADMIN_DB_PASSWORD:?set UMNIA_ADMIN_DB_PASSWORD}"

# sql_literal <nilai>: kembalikan literal SQL single-quote dengan
# escaping '' untuk nilai mentah. Password hanya boleh diteruskan
# lewat literal ini di dalam heredoc stdin — tidak pernah lewat
# argv (-v pw=), log, atau interpolasi tanpa escaping.
sql_literal() {
  printf "'%s'" "${1//\'/\'\'}"
}

ensure_role() {
  # $1 = role, $2 = password. Password diteruskan sebagai literal
  # SQL single-quote (''-escaped) yang dialirkan lewat stdin
  # heredoc — BUKAN lewat argv psql (-v pw=) dan BUKAN interpolasi
  # shell ke dalam string SQL tanpa escaping. psql tidak men-echo
  # stdin (tanpa -e), jadi password tidak muncul di log/diagnostik.
  # Catatan: psql -c TIDAK menginterpolasi variabel :'pw' (terbukti
  # gagal dengan syntax error di ':'), jadi perintah dikirim lewat
  # stdin heredoc. Delimiter heredoc tidak dikutip supaya $1 (nama
  # role, hardcoded di pemanggil) dan literal password mengembang.
  local pw_literal
  pw_literal="$(sql_literal "$2")"
  if [ -z "$(sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" ]; then
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
CREATE ROLE $1 LOGIN PASSWORD $pw_literal;
SQL
  else
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
ALTER ROLE $1 WITH LOGIN PASSWORD $pw_literal;
SQL
  fi
}

ensure_db() {
  # $1 = database, $2 = owner
  if ! sudo -u postgres psql -lqt | cut -d'|' -f1 | grep -qw "$1"; then
    sudo -u postgres createdb -O "$2" "$1"
  fi
}

# Operator (F1-OPERATOR-ENTITLEMENT-IMPLEMENT): peran NOLOGIN +
# login per-operator dari roster deployment, SEBELUM migrasi 011
# (011 memvalidasi peran ini dan gagal tertutup bila belum ada).
# Roster: deploy/operator-roster.env (gitignored, mode 0600, tidak
# pernah di-commit). Format per baris: UMNIA_OP_<ID>=<password>
# (ID 1-32 karakter [A-Za-z0-9_], dilipat ke huruf kecil; password
# minimal 12 karakter). Roster boleh kosong/tidak ada: peran NOLOGIN
# tetap dibuat, tanpa login dan tanpa password default. File TIDAK
# pernah di-source/di-eval: dibaca baris demi baris. Password tidak
# pernah masuk argv/log; hanya lewat literal SQL single-quote yang
# dialirkan lewat stdin (sql_literal).
ROSTER_FILE="deploy/operator-roster.env"
ROSTER_ROLES=()
ROSTER_PASSWORDS=()

read_roster() {
  # Parse SELURUH roster ke ROSTER_ROLES/ROSTER_PASSWORDS dan validasi
  # semua entri SEBELUM mutasi apa pun (mode file, format kunci/ID,
  # password minimal 12). Kegagalan di sini menghentikan skrip sebelum
  # satu peran pun diubah.
  ROSTER_ROLES=()
  ROSTER_PASSWORDS=()
  if [ ! -f "$ROSTER_FILE" ]; then
    echo "Roster operator tidak ada ($ROSTER_FILE): hanya peran NOLOGIN yang dibuat, tanpa login operator."
    return 0
  fi
  # Fail closed: file rahasia harus mode 0600/0400.
  mode="$(stat -c %a "$ROSTER_FILE")"
  if [ "$mode" != "600" ] && [ "$mode" != "400" ]; then
    echo "Roster operator harus mode 0600/0400, bukan $mode: chmod 600 $ROSTER_FILE" >&2
    exit 1
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
      *) echo "Roster baris $lineno: kunci harus UMNIA_OP_<ID>" >&2; exit 1 ;;
    esac
    id="$(printf '%s' "$key" | sed 's/^UMNIA_OP_//')"
    if [ -z "$id" ] || [ "${#id}" -gt 32 ]; then
      echo "Roster baris $lineno: ID operator 1-32 karakter" >&2; exit 1
    fi
    case "$id" in
      *[!A-Za-z0-9_]*)
        echo "Roster baris $lineno: ID operator hanya [A-Za-z0-9_]" >&2; exit 1 ;;
    esac
    id="$(printf '%s' "$id" | tr 'A-Z' 'a-z')"
    case "$id" in
      operator|grant_exec)
        echo "Roster baris $lineno: ID '$id' menyerupai peran cadangan" >&2; exit 1 ;;
    esac
    if [ -z "$value" ] || [ "${#value}" -lt 12 ]; then
      echo "Roster baris $lineno: password operator minimal 12 karakter" >&2; exit 1
    fi
    ROSTER_ROLES+=("umnia_op_$id")
    ROSTER_PASSWORDS+=("$value")
  done < "$ROSTER_FILE"
}

preflight_sql() {
  # Cetak DO preflight operator dengan daftar peran roster yang
  # sudah di-parse. Template memakai heredoc dikutip (tanpa ekspansi),
  # lalu placeholder __V_ROLES__/__V_LOGINS__ diganti literal array.
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
  # Jalankan preflight SEBELUM mutasi apa pun (termasuk pembuatan
  # peran bisnis/admin di bawah). Pelanggaran -> exit 1 dengan pesan
  # yang sudah disanitasi (hanya nama peran, tanpa password).
  # Pemeriksaan kepemilikan/hak berjalan terhadap DB bisnis bila
  # sudah ada; bila belum ada (klaster segar), terhadap DB postgres
  # (tidak ada objek bisnis di sana, jadi pemeriksaan itu lolos
  # secara vakum). Semua lookup berbasis nama, bukan ::regrole.
  local db="postgres"
  if sudo -u postgres psql -lqt | cut -d'|' -f1 | grep -qw "umnia_business"; then
    db="umnia_business"
  fi
  local out
  if ! out="$(preflight_sql | sudo -u postgres psql -v ON_ERROR_STOP=1 -d "$db" 2>&1)"; then
    echo "preflight operator GAGAL (state asli dipertahankan):" >&2
    echo "$out" | grep -o "preflight operator: [^\"']*" | head -1 >&2
    echo "Perbaiki state peran operator yang tidak aman secara manual sebelum menjalankan provisioning." >&2
    exit 1
  fi
}

# Preflight operator berjalan SEBELUM pembuatan peran/database apa pun:
# roster yang tidak aman harus DITOLAK, bukan diperbaiki diam-diam
# (tanpa REVOKE/ALTER adopsi). Rotasi password peran sehat yang sudah
# dikenal tetap diizinkan sesudah preflight lolos.
read_roster
preflight_operator

ensure_role umnia_business "$UMNIA_BUSINESS_DB_PASSWORD"
ensure_role umnia_admin "$UMNIA_ADMIN_DB_PASSWORD"
ensure_db umnia_business umnia_business
ensure_db umnia_admin umnia_admin

ensure_nologin_role() {
  # $1 = role. Buat bila belum ada. Bila SUDAH ada, TIDAK diubah:
  # preflight sudah memvalidasi atributnya aman; ALTER di sini akan
  # menjadi perbaikan/adopsi diam-diam atas state yang tidak aman.
  if [ -z "$(sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" ]; then
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
CREATE ROLE $1 NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION;
SQL
  fi
}

ensure_operator_login() {
  # $1 = role (umnia_op_<id>), $2 = password. Buat bila belum ada;
  # bila sudah ada, HANYA rotasi password (preflight sudah
  # memvalidasi atribut/keanggotaan aman — tanpa ALTER atribut dan
  # tanpa REVOKE adopsi). Password lewat literal stdin (sql_literal),
  # bukan argv. Keanggotaan grup kapabilitas diberikan (idempotent).
  local pw_literal
  pw_literal="$(sql_literal "$2")"
  if [ -z "$(sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" ]; then
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
CREATE ROLE $1 LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD $pw_literal;
SQL
  else
    sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
ALTER ROLE $1 WITH PASSWORD $pw_literal;
SQL
  fi
  sudo -u postgres psql -v ON_ERROR_STOP=1 <<SQL
GRANT umnia_operator TO $1;
SQL
}

ensure_operator_roles() {
  ensure_nologin_role umnia_operator
  ensure_nologin_role umnia_grant_exec
  local i
  for i in "${!ROSTER_ROLES[@]}"; do
    ensure_operator_login "${ROSTER_ROLES[$i]}" "${ROSTER_PASSWORDS[$i]}"
  done
}

ensure_operator_roles

# Migrasi business DB, berurutan berdasarkan nama file (001, 002, ...).
# Jalankan dari root repo. Setiap file diterapkan dalam SATU transaksi
# (psql -1): kegagalan di akhir file membatalkan seluruh file (loop
# lama memakai stdin tanpa -1, sehingga pernyataan awal sudah
# ter-commit saat pernyataan akhir gagal). psql berjalan sebagai user
# postgres dan tidak bisa membaca direktori workspace, jadi file
# disalin dulu ke staging 0644 di bawah /tmp (direktori 0755).
# Setiap migrasi idempotent dan mencatat versinya di schema_migrations
# (001) atau aman dijalankan ulang (002+).
stage_dir="$(mktemp -d /tmp/umnia-migrations.XXXXXX)"
chmod 755 "$stage_dir"
trap 'rm -rf "$stage_dir"' EXIT
for migration in services/business-api/migrations/*.sql; do
  echo "Menerapkan $migration ..."
  staged="$stage_dir/$(basename "$migration")"
  cp "$migration" "$staged"
  chmod 644 "$staged"
  sudo -u postgres psql -v ON_ERROR_STOP=1 -1 -d umnia_business -f "$staged"
done

echo "PostgreSQL siap: umnia_business + umnia_admin (terpisah)."
echo "Contoh URL bisnis (isi password dari .env, jangan commit):"
echo "  postgres://umnia_business:PASSWORD@localhost:5432/umnia_business?sslmode=disable"
