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
didukung adalah **dump/restore ke volume baru**. Jalankan skripnya dari
root repository (jalur ini memakai project Compose yang berbeda untuk
lama vs baru, sehingga volume lama tidak pernah tersentuh):

```powershell
.\deploy\windows\Upgrade-UmniaVolume.ps1 -OldProject umnia-legacy -NewProject umnia -ConfirmMaintenance -LegacyComposeFile deploy/windows/compose.yml
```

**File compose lama (`-LegacyComposeFile`, WAJIB):** beri file compose
ASLI yang membuat volume lama. SEMUA operasi project lama (inspeksi,
start, readiness, query SQL, stop penulis, dump, shutdown, pemulihan)
memakai file ini; operasi project baru memakai `-ComposeFile`. Skrip
memeriksa file lama ada (`Test-Path`) dan valid
(`docker compose config -q`) SEBELUM perintah mutating apa pun — bila
tidak ada/tidak valid skrip berhenti. Tidak ada fallback ke file compose
baru. Perintah pemulihan yang dicetak mengutip path file (aman bila path
mengandung spasi).

**Syarat maintenance (`-ConfirmMaintenance`, wajib):** sebelum dump,
skrip menghentikan penulis yang dikelola Compose (`business-api` dan
`web` project lama) dan memeriksa tidak ada sesi aktif untuk akun sumber
di `pg_stat_activity`. Service penulis yang memang tidak terdefinisi di
file compose lama dilewati secara eksplisit (cek `config --services`),
bukan lewat menelan error. Penulis tetap berhenti sampai cutover selesai
atau pemulihan berhasil. Batas jaminan: skrip hanya bisa menghentikan
penulis yang dikelola Compose; klien DB langsung / instansi API di luar
Compose adalah tanggung jawab operator — pastikan tidak ada penulis
lain sebelum menjalankan skrip. Tanpa `-ConfirmMaintenance` skrip
berhenti sebelum menyentuh apa pun. Bila pemeriksaan status penulis
GAGAL (daemon mati, config rusak), skrip berhenti dan TIDAK menganggap
penulis sudah berhenti — ketiadaan hasil query bukan bukti.

Yang dilakukan skrip (berhenti bila ada langkah gagal; jalur pemulihan
dicetak bila gagal):

1. **Preflight, tanpa perintah mutating:** file compose lama
   (`-LegacyComposeFile`) harus ada dan valid (`config -q`); tolak bila
   `-OldProject` dan `-NewProject` sama sesudah normalisasi, bila
   project baru masih punya container berjalan atau volumenya sudah ada,
   dan bila volume sumber tidak ada. Default `-OldProject umnia-legacy`
   BUKAN bukti nama project asli Anda — bila volume sumber tidak
   ditemukan, isi `-OldProject` yang benar.
2. **Verifikasi identitas sumber:** postgres lama dijalankan bila
   berhenti, lalu skrip memeriksa dengan query SQL terautentikasi bahwa
   `current_user` adalah akun sumber (`-SourceDbUser`, default
   `umnia_business` — akun yang dipakai `POSTGRES_USER` saat volume lama
   dibuat) dan tabel `workspaces` ada. `pg_isready` saja TIDAK cukup:
   ia mengembalikan 0 bahkan untuk role yang tidak ada. Akun bootstrap
   baru (`umnia_bootstrap`) hanya dipakai pada target, tidak pernah pada
   sumber.
3. **Quiesce (lihat syarat maintenance di atas).**
4. Dump dengan `pg_dump -Fc --no-owner` sebagai akun sumber di dalam
   container lama, salin keluar dengan `docker cp` (binary-safe —
   PowerShell tidak punya operator `<`, dan `>` di Windows PowerShell
   5.1 menulis UTF-16), validasi backup dengan `pg_restore -l`
   **sebelum** stack lama dihentikan, `down` **tanpa `-v`**, jalankan
   postgres project baru (volume kosong -> initdb membuat role runtime
   `umnia_business` NOSUPERUSER/NOBYPASSRLS dan role `postgres`),
   restore sebagai bootstrap, `up --build` penuh (service `migrate`
   menjalankan ulang 001+002), lalu verifikasi **read-only**: role /
   migrasi / hak EXECUTE / `/readyz` / hitung workspaces hasil restore.
   Skrip TIDAK membuat akun atau workspace tes di database yang
   di-upgrade; uji isolasi tenant (register/login/403) dijalankan
   terpisah pada fixture sintetis terisolasi.
   Volume lama (`umnia-legacy_postgres-business-data`) tetap ada sampai
   upgrade terverifikasi; hapus hanya sesudah itu dengan
   `docker volume rm umnia-legacy_postgres-business-data`.
5. **Bila gagal sesudah stack lama berhenti**, jalur pemulihan yang
   dicetak: (1) hentikan project baru dulu (`-p <NewProject> down`,
   tanpa `-v`) supaya port host bebas, (2) jalankan kembali stack LAMA
   LENGKAP (termasuk `business-api`, supaya penulis bisa menulis lagi)
   dengan file compose ASLI volume lama (`-LegacyComposeFile` — parameter
   wajib, tidak ada fallback ke file baru). Path file dalam perintah
   pemulihan dikutip, jadi tetap benar bila mengandung spasi. Volume
   lama tidak pernah dihapus oleh skrip.

Kedua project memakai port host yang sama (5432/5433/8080/9000/3000),
jadi stack lama harus berhenti dulu sebelum stack baru jalan — skrip
menegakkan urutan itu. Skrip tidak pernah menulis/menimpa `.env` dan
tidak pernah menimpa file backup yang sudah ada (nama backup memakai
timestamp). Perintah `docker volume rename` **tidak ada** (Docker hanya
punya create/inspect/ls/prune/rm) — jangan pakai.

Jalur manual yang setara (bila tidak memakai skrip; ganti
`<cid-lama>`/`<cid-baru>` dengan ID container dari `docker compose ps -q`,
dan ganti `deploy/windows/compose.yml` pada perintah `-p umnia-legacy`
dengan file compose ASLI yang membuat volume lama — skrip melakukan
routing yang sama untuk semua operasi sumber).
**Identitas sumber:** dump dan pemeriksaan kesiapan pada volume lama
memakai akun ASLI volume itu (`umnia_business`, dari `POSTGRES_USER`
saat volume dibuat) — JANGAN memakai `umnia_bootstrap` pada sumber,
karena role itu tidak ada di volume lama. `umnia_bootstrap` hanya untuk
target. **Maintenance:** hentikan penulis (`business-api`, `web`)
sebelum dump dan pastikan tidak ada sesi aktif untuk akun sumber;
penulis baru boleh jalan lagi sesudah cutover atau pemulihan.

```powershell
# 0. Verifikasi identitas sumber (pg_isready saja TIDAK cukup — ia
#    mengembalikan 0 bahkan untuk role yang tidak ada):
docker compose -f deploy/windows/compose.yml -p umnia-legacy exec -T postgres-business psql -U umnia_business -d umnia_business -tAc "SELECT current_user;"
#    (harus mengembalikan umnia_business; bila FATAL role tidak ada,
#     periksa -OldProject dan akun sumber yang benar)
#    Hentikan penulis sebelum dump:
docker compose -f deploy/windows/compose.yml -p umnia-legacy stop business-api web
# 1. Dump di dalam container lama, lalu salin keluar (binary-safe):
docker compose -f deploy/windows/compose.yml -p umnia-legacy exec -T postgres-business pg_dump -U umnia_business -d umnia_business --no-owner -Fc -f /tmp/umnia-backup.dump
docker cp <cid-lama>:/tmp/umnia-backup.dump .\backups\umnia-backup.dump
# 2. Validasi SEBELUM menghentikan stack lama:
docker compose -f deploy/windows/compose.yml -p umnia-legacy exec -T postgres-business pg_restore -l /tmp/umnia-backup.dump
#    (daftar isi harus memuat tabel workspaces; bila gagal, JANGAN lanjut)
# 3. Hentikan stack lama (tanpa -v) dan jalankan postgres baru:
docker compose -f deploy/windows/compose.yml -p umnia-legacy down
docker compose -f deploy/windows/compose.yml -p umnia up -d postgres-business
# 4. Restore sebagai bootstrap, lalu jalankan stack baru penuh:
docker cp .\backups\umnia-backup.dump <cid-baru>:/tmp/umnia-backup.dump
docker compose -f deploy/windows/compose.yml -p umnia exec -T postgres-business pg_restore -U umnia_bootstrap -d umnia_business --no-owner --exit-on-error /tmp/umnia-backup.dump
docker compose -f deploy/windows/compose.yml -p umnia up --build
# (service migrate menjalankan ulang 001+002 sebagai bootstrap, yang
#  menormalkan kepemilikan tabel/fungsi dan hak EXECUTE.)
# Pemulihan bila gagal sesudah langkah 3: hentikan dulu project baru
# (docker compose -f deploy/windows/compose.yml -p umnia down, tanpa -v),
# lalu jalankan kembali stack LAMA LENGKAP dengan file compose ASLI
# volume lama (docker compose -f <file-compose-asli> -p umnia-legacy up -d).
```

Efeknya: data dan kepemilikan tabel dinormalkan (tabel milik
`umnia_business` yang kini non-superuser sehingga RLS FORCE berlaku),
fungsi helper dialihkan ke `postgres` saat `migrate` menjalankan ulang
002, dan API selanjutnya memakai akun runtime yang sama. Volume lama
(`umnia-legacy_postgres-business-data`) tetap ada sebagai cadangan
sampai upgrade terverifikasi; hapus hanya sesudah backup dan verifikasi
di bawah lolos. Jangan
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
`docs/reviews/f1-foundation-remediation-review/REVIEW.md`,
`docs/reviews/f1-windows-review-fixes.md`,
`docs/reviews/f1-upgrade-safety-review.md` (skrip upgrade diperbaiki:
identitas sumber terverifikasi, preflight tanpa mutasi, quiesce
penulis, verifikasi read-only — diuji dengan mock docker yang
mengeksekusi binary postgres/API asli terhadap cluster nyata, bukan
mock canned-success), dan
`docs/reviews/f1-upgrade-final-guards.md` (routing file compose lama
untuk semua operasi sumber, file lama wajib + valid sebelum mutasi,
pemeriksaan penulis fail-closed).

## Yang belum terbukti

`compose up` dan skrip upgrade di host Windows MASIH UNVERIFIED (belum
pernah dijalankan di Windows; eksekusi PowerShell 5.1 juga belum
teruji). Yang sudah terverifikasi (F1-UPGRADE-SAFETY-REVIEW,
2026-11-06): skrip initdb + migrasi + role runtime terhadap entrypoint
dan binary image resmi `postgres:16-bookworm` (entrypoint image identik
dengan sumber 16/bookworm; digest manifest amd64 terverifikasi ke
hub.docker.com), dijalankan native di Linux karena `docker run` diblokir
VM ini — dan jalur dump/restore `-Fc` + skrip PowerShell (parser +
logika dengan mock docker yang mengeksekusi perintah postgres/API asli
+ mekanika dump/restore/migrasi/API nyata, termasuk kasus negatif dan
penulis terkendali). Jalankan `compose config` dan `compose up` di
Windows sebelum mengklaim lulus di sana.
