# Windows (Docker Compose) — database dan migrasi

## Startup volume baru (fresh)

```powershell
Copy-Item .env.example .env
# Isi password dev di .env (jangan commit .env)
docker compose -f deploy/windows/compose.yml up --build
```

Urutan startup:

1. `postgres-business` / `postgres-admin` — akun bootstrap superuser
   (`umnia_bootstrap` / `umnia_admin_bootstrap`) dibuat image postgres
   dari `POSTGRES_USER`. Skrip `initdb/01-runtime-roles.sh` (hanya saat
   volume masih kosong) membuat role runtime `umnia_business` /
   `umnia_admin` (NOSUPERUSER, NOBYPASSRLS) dan role `postgres`
   superuser yang dibutuhkan migrasi 002.
2. `migrate` — menjalankan `001_init.sql` + `002_identity.sql` sebagai
   akun bootstrap, dalam satu transaksi untuk keseluruhan pemanggilan
   `psql -1` (bukan per file: `-1` membungkus semua file `-f` dalam satu
   transaksi, jadi kegagalan di 002 ikut membatalkan 001 pada run itu).
   Gagal di sini membuat `business-api` tidak start (depends_on
   `service_completed_successfully`).
3. `business-api` — terhubung sebagai akun runtime `umnia_business`.
   Readiness `/readyz` memeriksa migrasi `002_identity` sudah terapan,
   bukan hanya ping koneksi.

## Upgrade volume lama (dibuat sebelum perbaikan ini)

Volume lama dibuat dengan `POSTGRES_USER=umnia_business` (superuser) dan
tanpa service `migrate`. Jangan hapus volume (`down -v`) sebelum data
terselamatkan. **Jangan coba demosi role itu di tempat**: `umnia_business`
adalah bootstrap superuser volume tersebut, dan PostgreSQL menolak
mencabut SUPERUSER-nya — `ALTER ROLE ... NOSUPERUSER` gagal dengan
`permission denied to alter role` (bahkan dari superuser lain), dan
`REASSIGN OWNED` / `DROP ROLE` gagal dengan `required by the database
system` (terbukti pada PostgreSQL 16). Satu-satunya jalur upgrade yang
didukung adalah **dump/restore ke volume baru**:

```powershell
# 1. Dump database lama (dari dalam container postgres-business lama,
#    atau psql ke localhost:5432). Jangan sertakan owner (--no-owner):
docker compose -f deploy/windows/compose.yml exec -T postgres-business `
  pg_dump -U umnia_business -d umnia_business --no-owner > umnia-backup.sql

# 2. Hentikan stack lama dan pindahkan volumenya agar tidak tertukar
#    (cek nama volume sebenarnya dengan `docker volume ls`):
docker compose -f deploy/windows/compose.yml down
docker volume ls | findstr postgres-business-data
docker volume rename <nama-volume-lama> <nama-volume-lama>-old

# 3. Jalankan stack baru (volume kosong -> initdb membuat role runtime
#    umnia_business NOSUPERUSER/NOBYPASSRLS dan role postgres), lalu
#    restore dump sebagai bootstrap dan jalankan ulang migrasi:
docker compose -f deploy/windows/compose.yml up -d postgres-business
docker compose -f deploy/windows/compose.yml exec -T postgres-business `
  psql -U umnia_bootstrap -d umnia_business -v ON_ERROR_STOP=1 < umnia-backup.sql
docker compose -f deploy/windows/compose.yml up --build
# (service migrate menjalankan ulang 001+002 sebagai bootstrap, yang
#  menormalkan kepemilikan tabel/fungsi dan hak EXECUTE.)
```

Efeknya: data dan kepemilikan tabel dinormalkan (tabel milik
`umnia_business` yang kini non-superuser sehingga RLS FORCE berlaku),
fungsi helper dialihkan ke `postgres` saat `migrate` menjalankan ulang
002, dan API selanjutnya memakai akun runtime yang sama. Volume lama
tetap ada sebagai cadangan (`...-data-old`) sampai upgrade terverifikasi;
hapus hanya sesudah backup dan verifikasi di bawah lolos. Jangan
menyalurkan output `psql` ke `tail` saat menjalankan migrasi manual:
exit code `psql` akan tertutup oleh `tail` dan kegagalan bisa terlihat
seperti sukses. Verifikasi sesudah upgrade (termasuk hak EXECUTE fungsi
helper):

```sql
SELECT rolname, rolsuper, rolbypassrls FROM pg_roles
WHERE rolname IN ('umnia_business', 'umnia_bootstrap', 'postgres');
SELECT version FROM schema_migrations ORDER BY version;
SELECT routine_name, grantee FROM information_schema.routine_privileges
WHERE routine_schema = 'app' AND routine_name = 'register_subject';
-- Harus ada baris grantee umnia_business DAN postgres; bila hanya postgres,
-- migrasi yang berjalan bukan versi terbaru (urutan GRANT sudah diperbaiki
-- agar jalan setelah alih kepemilikan fungsi) — periksa versi file migrasi.
```

Jalur dump/restore ini diuji pada database uji (fixture workspace
dipertahankan, role runtime NOSUPERUSER/NOBYPASSRLS, hak EXECUTE 6/6,
API lolos), bukan pada data pengguna. Lihat laporan
F1-FOUNDATION-REMEDIATION-REVIEW.

## Yang belum terbukti

Compose ini belum pernah dijalankan di host Windows (UNVERIFIED).
Jalur initdb + migrasi + role runtime yang sama diverifikasi pada
PostgreSQL 16 native di Linux dalam tugas perbaikan; jalankan
`compose config` dan `compose up` di Windows sebelum mengklaim lulus
di sana.
