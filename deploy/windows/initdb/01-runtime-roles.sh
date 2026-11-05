#!/usr/bin/env bash
# Initdb Windows (hanya berjalan sekali, saat volume data masih kosong).
# Membuat role runtime non-superuser dan role postgres superuser yang
# dibutuhkan migrasi 002 (ALTER FUNCTION ... OWNER TO postgres).
# Dijalankan entrypoint postgres sebagai $POSTGRES_USER (bootstrap).
# Password diambil dari environment container, bukan hardcoded di sini.
# Catatan: skrip ini TIDAK berjalan ulang pada volume lama — untuk volume
# lama, ikuti deploy/windows/README.md (jalur upgrade).
set -euo pipefail

# $1 = nama role runtime, $2 = variabel env password runtime.
create_runtime_role() {
  # Dua pernyataan local terpisah, sengaja: bila pw_var dan pw digabung
  # dalam satu `local`, bash mengekspansi semua RHS sebelum local mana
  # pun berlaku, sehingga ${!pw_var:-} melihat pw_var masih kosong dan
  # gagal "invalid indirect expansion" (terbukti di bash 5.2).
  local role="$1" pw_var="$2"
  local pw="${!pw_var:-}"
  if [ -z "$pw" ]; then
    # Variabel password service lain mungkin tidak diset di container ini;
    # lewati role itu daripada menggagalkan initdb service ini.
    echo "warning: $pw_var kosong, lewati role $role" >&2
    return 0
  fi
  if [ -z "$(psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$role'")" ]; then
    psql -v ON_ERROR_STOP=1 -v pw="$pw" <<SQL
CREATE ROLE $role NOSUPERUSER NOBYPASSRLS LOGIN PASSWORD :'pw';
SQL
  fi
}

# Role postgres superuser harus ada agar migrasi 002 bisa mengalihkan
# kepemilikan fungsi helper ke postgres (lihat migrasi 002).
if [ -z "$(psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = 'postgres'")" ]; then
  psql -v ON_ERROR_STOP=1 -v pw="$POSTGRES_PASSWORD" <<SQL
CREATE ROLE postgres SUPERUSER LOGIN PASSWORD :'pw';
SQL
fi

create_runtime_role umnia_business UMNIA_BUSINESS_DB_PASSWORD
create_runtime_role umnia_admin UMNIA_ADMIN_DB_PASSWORD
