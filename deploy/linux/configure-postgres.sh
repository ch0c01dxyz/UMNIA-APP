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

ensure_role() {
  # $1 = role, $2 = password. Password diteruskan lewat variabel psql
  # (:'pw' diinterpolasi psql menjadi literal ber-quote), bukan
  # interpolasi shell ke dalam string SQL.
  # Catatan: psql -c TIDAK menginterpolasi variabel :'pw' (terbukti
  # gagal dengan syntax error di ':'), jadi perintah dikirim lewat stdin
  # heredoc, tempat interpolasi :'pw' bekerja. Delimiter heredoc tidak
  # dikutip supaya $1 (nama role, hardcoded di pemanggil) mengembang.
  if [ -z "$(sudo -u postgres psql -tAc "SELECT 1 FROM pg_roles WHERE rolname = '$1'")" ]; then
    sudo -u postgres psql -v ON_ERROR_STOP=1 -v pw="$2" <<SQL
CREATE ROLE $1 LOGIN PASSWORD :'pw';
SQL
  else
    sudo -u postgres psql -v ON_ERROR_STOP=1 -v pw="$2" <<SQL
ALTER ROLE $1 WITH LOGIN PASSWORD :'pw';
SQL
  fi
}

ensure_db() {
  # $1 = database, $2 = owner
  if ! sudo -u postgres psql -lqt | cut -d'|' -f1 | grep -qw "$1"; then
    sudo -u postgres createdb -O "$2" "$1"
  fi
}

ensure_role umnia_business "$UMNIA_BUSINESS_DB_PASSWORD"
ensure_role umnia_admin "$UMNIA_ADMIN_DB_PASSWORD"
ensure_db umnia_business umnia_business
ensure_db umnia_admin umnia_admin

# Migrasi business DB, berurutan berdasarkan nama file (001, 002, ...).
# Jalankan dari root repo. SQL dialirkan lewat stdin, bukan -f, supaya
# user postgres tidak perlu hak baca atas direktori workspace.
# Setiap migrasi idempotent dan mencatat versinya di schema_migrations
# (001) atau aman dijalankan ulang (002+).
for migration in services/business-api/migrations/*.sql; do
  echo "Menerapkan $migration ..."
  sudo -u postgres psql -v ON_ERROR_STOP=1 -d umnia_business < "$migration"
done

echo "PostgreSQL siap: umnia_business + umnia_admin (terpisah)."
echo "Contoh URL bisnis (isi password dari .env, jangan commit):"
echo "  postgres://umnia_business:PASSWORD@localhost:5432/umnia_business?sslmode=disable"
