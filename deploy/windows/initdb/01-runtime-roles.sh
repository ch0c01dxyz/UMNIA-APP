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
if role_exists "$UMNIA_RUNTIME_ROLE"; then
  echo "initdb: role $UMNIA_RUNTIME_ROLE already exists, skipping" >&2
else
  psql_bootstrap -v role="$UMNIA_RUNTIME_ROLE" -v pw="$UMNIA_RUNTIME_DB_PASSWORD" <<SQL
CREATE ROLE :"role" NOSUPERUSER NOBYPASSRLS LOGIN PASSWORD :'pw';
SQL
fi

# Role postgres superuser harus ada agar migrasi 002 bisa mengalihkan
# kepemilikan fungsi helper ke postgres (lihat migrasi 002).
if role_exists postgres; then
  echo "initdb: role postgres already exists, skipping" >&2
else
  psql_bootstrap -v pw="$POSTGRES_PASSWORD" <<SQL
CREATE ROLE postgres SUPERUSER LOGIN PASSWORD :'pw';
SQL
fi
