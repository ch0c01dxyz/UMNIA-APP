# F1-FOUNDATION-REMEDIATION-REVIEW — laporan review independen (run 2)

**STATUS: PARTIAL (siap review sumber; belum boleh integrasi sebelum CI GitHub hijau + Compose Windows)**
**TASK_ID: F1-FOUNDATION-REMEDIATION-REVIEW**
**FILES CHANGED: 8 diubah + 2 baru (patch di bawah; belum commit, belum push)**
**TESTS: 14 pass / 0 fail / 0 skip (go test, role runtime, run baru); gate negatif lolos; anomali EXECUTE direproduksi, akar penyebab terbukti, perbaikan terverifikasi**
**RISK / BLOCKERS: CI GitHub asli dan `docker compose up` Windows masih NOT RUN; lihat "Yang belum terbukti"**
**NEXT ACTION: Leader review patch ini → setujui → commit + push → tunggu CI GitHub hijau → uji Compose di Windows**

> Klasifikasi klaim mengikuti AAHE: OBSERVED / OFFICIAL / TESTED / INFERRED /
> PROPOSED / UNKNOWN. Semua perintah dan hasil di bawah adalah run baru
> (2026-11-05, 18:57–19:15 WIB) pada patch final, bukan salinan laporan lama.
> Password yang dipakai hanya fixture sintetis (`review-*-only`,
> `ci-*-only`, `umnia-dev-only`, `<bootstrap-baru>`). Tidak ada `.env`,
> kredensial asli, atau database pengguna yang dipakai atau disertakan.

## 1. Baseline dan allowlist (OBSERVED)

- Repo `~/workspace/UMNIA-APP`, branch `f1-03-identity`, HEAD `577f0a5`
  (`577f0a5917559f5bb7c6829f8bfdb9c31fff0cc6`), upstream
  `origin/f1-03-identity` identik; `origin/main` masih `a107924`.
  Satu worktree, tidak ada penulis lain.
- `git diff --check` bersih (exit 0). `git status`: 8 modified + 2 untracked,
  sama seperti laporan sebelumnya — tidak ada file di luar allowlist.

| # | File | Tujuan |
|---|---|---|
| 1 | `.env.example` | Password bootstrap Windows (terpisah dari runtime) |
| 2 | `.github/workflows/ci.yml` | Service PostgreSQL 16 + gate integrasi anti-skip |
| 3 | `deploy/windows/compose.yml` | Bootstrap superuser, service `migrate`, API pakai role runtime |
| 4 | `deploy/windows/README.md` | **BARU.** Startup fresh + jalur upgrade volume lama (dump/restore) |
| 5 | `deploy/windows/initdb/01-runtime-roles.sh` | **BARU.** Role runtime + role `postgres` saat volume kosong |
| 6 | `services/business-api/README.md` | Kontrak role, gate CI, readiness |
| 7 | `services/business-api/identity_integration_test.go` | 4 tes baru (role runtime, invite kedaluwarsa, kasir, readyz) |
| 8 | `services/business-api/main.go` | `/readyz` wajib `002_identity` |
| 9 | `services/business-api/migrations/002_identity.sql` | Urutan OWNER TO → GRANT (perbaikan anomali, §3) |
| 10 | `services/business-api/workspace.go` | Daftar anggota wajib `view_members` |

Diff terhadap baseline (final): **8 files changed, 315 insertions, 12 deletions**
(+ 2 file baru). Perbedaan dari laporan sebelumnya (+8 insertions, +1 file
di `compose.yml`): tiga perbaikan yang ditemukan selama review ini (§2) —
bukan penulisan ulang sejarah.

## 2. Tiga bug yang ditemukan dan diperbaiki selama review ini

Semua ditemukan dengan mereproduksi urutan yang diajukan (bukan dari membaca
saja), diperbaiki hanya pada lapisan yang bertanggung jawab, dan diuji ulang.
Patch yang diserahkan sudah berisi perbaikannya.

### 2a. `initdb/01-runtime-roles.sh:13` — skrip gagal total di volume baru (TESTED)

Satu pernyataan `local role="$1" pw_var="$2" pw="${!pw_var:-}"` gagal dengan
`pw_var: invalid indirect expansion`, exit 1. Penyebab: bash mengekspansi
semua RHS sebelum `local` mana pun berlaku, jadi `pw_var` masih kosong saat
`${!pw_var:-}` dievaluasi (direproduksi di bash 5.2.21; bentuk dua-`local`
lolos). Dengan `set -euo pipefail`, skrip mati sebelum role apa pun dibuat —
di Compose, entrypoint postgres akan gagal dan stack tidak pernah naik.
**Perbaikan:** pecah menjadi dua pernyataan `local` + komentar alasannya.
**Uji ulang:** initdb exit 0 pada fixture fresh (§4).

### 2b. `002_identity.sql` — urutan GRANT sebelum OWNER TO menghilangkan EXECUTE (TESTED, §3)

Lihat §3 (anomali). **Perbaikan:** blok OWNER TO dipindah sebelum blok
REVOKE/GRANT + `search_path`, dengan komentar urutan di file.

### 2c. `deploy/windows/README.md` — jalur upgrade volume lama mustahil (TESTED)

`ALTER ROLE umnia_business NOSUPERUSER NOBYPASSRLS` tidak bisa dijalankan
oleh siapa pun: `umnia_business` adalah bootstrap superuser volumenya, dan
PostgreSQL menolak demosi/drop/reassign bootstrap superuser (mekanisme
T1–T3, §3). **Perbaikan:** README kini mendokumentasikan satu-satunya
jalur yang didukung — **dump/restore ke volume baru**
(`pg_dump --no-owner` → volume baru → restore sebagai bootstrap →
migrate ulang) — dan jalur itu diuji ujung-ke-ujung (§4, part 3).

### 2d. Koreksi komentar `psql -1` (TESTED)

Komentar patch lama menyebut `-1` sebagai "satu transaksi per file". Yang
benar (OFFICIAL, `psql --help` / docs: `--single-transaction` membungkus
seluruh pemanggilan): **satu transaksi untuk keseluruhan pemanggilan**,
semua file `-f` sekaligus. Perilakunya lebih ketat dari klaim lama dan
terbukti di §5 — komentar di `compose.yml` dan `README.md` diperbaiki.

## 3. Anomali EXECUTE — direproduksi, akar penyebab TERBUKTI, perbaikan terverifikasi

### Reproduksi (TESTED)

Fixture legacy = cluster PostgreSQL terisolasi (port 55434, `initdb -U
umnia_business`, tanpa role `postgres`), mereplikasi volume Compose lama:

- **L1** — migrasi gaya lama tanpa `-1`, sebagai superuser `umnia_business`:
  exit 3, gagal di `ALTER FUNCTION ... OWNER TO postgres`. Keadaan setengah
  terapan: `schema_migrations` mencatat 001 **dan** 002, tabel ada, fungsi
  milik `umnia_business`, `routine_privileges` hanya `umnia_business`.
- **L3** — urutan migrate yang diajukan, SEKALI, tanpa pipe
  (`psql -v ON_ERROR_STOP=1 -1 -f 001 -f 002` sebagai `postgres`):
  exit 0. Inspeksi segera sesudahnya pada patch **lama**:
  `proacl = {postgres=X/postgres}` untuk keenam helper — `umnia_business`
  HILANG. `routine_privileges` hanya `postgres`. Inilah persis anomali yang
  dilaporkan ("hanya postgres"). Efektif `EXECUTE` untuk `umnia_business`
  tetap `t` hanya karena role itu masih superuser di fixture ini — jadi
  anomali ini **tidak terlihat dari perilaku API**, hanya dari ACL.
- **Sesudah perbaikan §2b**, L3 yang sama menghasilkan
  `proacl = {postgres=X/postgres, umnia_business=X/postgres}` untuk keenam
  helper, dan regression check menegaskan 6/6 grant (part 2, L4b).

### Akar penyebab (TESTED, mekanisme M1/M2, cluster port 55435)

- **M1:** fungsi dibuat `umnia_business`, `GRANT` oleh `umnia_business`
  → `proacl = {umnia_business=X/umnia_business}`. `ALTER FUNCTION ...
  OWNER TO postgres` → `proacl = {postgres=X/postgres}`. **Alih kepemilikan
  membuang entri ACL yang diberikan pemilik lama.**
- **M2:** `CREATE OR REPLACE FUNCTION` oleh `postgres` atas fungsi milik
  `umnia_business` → `proacl` **tidak berubah**. Bukan pelakunya.
- Rangkaian pada migrasi ulang: `CREATE OR REPLACE` (ACL dipertahankan) →
  `GRANT TO umnia_business` oleh `postgres` (digabung ke entri yang sudah
  ada, pemberi hibah tetap pemilik lama) → `OWNER TO postgres` (membuang
  entri pemberi-lama itu). Pada instalasi fresh, pemilik sudah `postgres`
  saat GRANT jalan, sehingga bug tidak muncul — itulah mengapa anomali
  hanya muncul pada upgrade volume lama.
- **Mekanisme penutup (T1–T3):** bootstrap superuser tidak bisa didemosi
  (`ALTER` oleh superuser lain → `permission denied to alter role / The
  bootstrap user must have the SUPERUSER attribute`), tidak bisa
  `REASSIGN OWNED` / `DROP ROLE` (`required by the database system`).
  Demote diri sendiri juga gagal dengan pesan yang sama. Ini yang membuat
  §2c mustahil di tempat.

### search_path dan CREATE (OBSERVED, `inspect.sql`)

- Keenam helper: owner `postgres`, `proconfig = {search_path=public}`,
  `SECURITY DEFINER`. `has_permission` memanggil `app.is_member`
  (qualified); badan fungsi hanya merujuk tabel `public` yang sudah ada.
- `umnia_business`: `CREATE` di `public` = **t** (via `pg_database_owner`,
  karena ia pemilik database), `CREATE` di `app` = **f**;
  `PUBLIC`: `CREATE` di `public`/`app` = tidak ada. INFERRED: risiko
  tabel-bayangan lewat `search_path=public` terbatas — tabel yang dirujuk
  sudah ada dan dimiliki runtime role itu sendiri (desain F1-03, bukan
  patch ini); menanam objek bayangan tidak memberi hak baru di luar
  kepemilikan tabel yang sudah ada. Bukan klaim "aman" absolut.
- OBSERVED (tindak lanjut untuk leader, bukan perubahan patch ini): pada
  volume lama, skema `app` tetap milik `umnia_business` (tidak ada
  `ALTER SCHEMA ... OWNER TO` di migrasi), sehingga runtime role punya
  `CREATE` di `app`. Ia tidak bisa mengganti fungsi milik `postgres`, tapi
  bisa membuat objek baru di `app`. PROPOSED: pertimbangkan alih
  kepemilikan skema `app` ke `postgres` di migrasi berikut.

## 4. Bukti eksekusi baru (run 2, final)

Lingkungan: Go 1.27.3, PostgreSQL 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1),
Linux native (tanpa Docker — sesuai aturan repo). VM sempat berganti di
tengah tugas: PostgreSQL hilang dan dipasang ulang dari cache apt lokal
(`postgresql-16_16.15`, cluster `16/main` port 5432 online). Semua password
fixture sintetis; tidak ada database pengguna.

| # | Pemeriksaan | Hasil |
|---|---|---|
| F1 | Fresh: initdb script (persis file patch, via stdin karena user postgres tak bisa baca workspace root-owned — lihat catatan) | exit 0 |
| F2 | Fresh: migrate SEKALI (`psql -1 -f 001 -f 002`, bootstrap `postgres`, tanpa pipe) | exit 0 |
| F3 | Fresh: inspeksi — `umnia_business` NOSUPERUSER/NOBYPASSRLS; 6 helper owner `postgres`, `search_path=public`, SECURITY DEFINER; `proacl` 6/6 `{postgres=X, umnia_business=X}`; EXECUTE efektif runtime `t`, role lain `f`, PUBLIC tanpa entri; RLS FORCE `t` | exit 0 |
| F4 | `gofmt -l` bersih, `go vet` | exit 0 |
| F5 | `go test -count=1 -json` (URL role runtime) | exit 0 — **14 pass / 0 fail / 0 skip** (`fresh-07-gotest.json`) |
| F6 | `go test -count=1 -v` **tanpa URL** (mode unit-only lokal) | exit 0, **7 SKIP** — gate anti-skip CI akan GAGAL pada log ini (sesuai rancangan) |
| F7 | Cuplikan gate CI **harfiah** (`test -n URL`, `go test -v \| tee`, `PIPESTATUS`, `grep SKIP`) pada `review_fresh` | exit 0, 14 PASS, 0 SKIP (`fresh-12-gate.log`) |
| F8 | Live API workflow (runtime role): register→login→workspace→403 sebelum invite→accept 200→200 sesudah→kasir audit 403→kasir members 403→owner members 200→readyz 200 | exit 0 |
| L1–L5 | Legacy: half-state (exit 3) → demosi mustahil (2× exit 1) → migrate SEKALI exit 0 → grant 6/6 → fixture `Toko Lama` utuh → probe: non-member GET workspace 200 (MEMBUKTIKAN bypass superuser; volume lama tidak boleh melayani API) | sesuai harapan |
| D1 | Diag 3-tahap: proacl sebelum OWNER TO = kosong; sesudah OWNER TO = kosong; sesudah GRANT = `{postgres=X, umnia_business=X}` | exit 0 |
| R1 | Restore-upgrade: `pg_dump --no-owner` (exit 0, `Toko Lama` ada) → restore ke DB baru (exit 0) → migrate ulang (exit 0) → grant 6/6, role `false/false`, fixture utuh, API workflow exit 0, user baru GET `Toko Lama` **403**, readyz 200 | 0 regression failure |
| M1–M2, T1–T3 | Mekanisme PostgreSQL (§3) | sesuai harapan |

Catatan lingkungan (OBSERVED, bukan cacat patch): user OS `postgres`
tidak bisa membaca workspace yang dimiliki root di host ini, jadi
`psql -f <path-di-workspace>` dan `bash <skrip-initdb>` gagal
`Permission denied`; skrip review memakai stdin redirect (workaround yang
sama dengan yang didokumentasikan `deploy/linux/configure-postgres.sh`).
Di container Windows, initdb dimount ke dalam container dan dibaca user
postgres di sana — jalur itu tetap UNVERIFIED (§6).

## 5. Kegagalan migrasi terkontrol (TESTED)

File `002` buruk: pernyataan gagal disisipkan **tepat sesudah** pernyataan
`INSERT INTO schema_migrations` yang lengkap (dua baris: `VALUES ...` /
`ON CONFLICT ...`).

| Mode | Exit migrasi | Keadaan sesudahnya |
|---|---|---|
| **Dengan `-1`** (yang diajukan) | 3 (non-zero) | Rollback total: tabel `schema_migrations` 0, tabel `workspaces` 0, fungsi `app` 0. **002 tidak tercatat.** |
| Tanpa `-1` (kontrol, perilaku lama) | 3 (non-zero) | Setengah terapan: `schema_migrations` mencatat **001 dan 002**, tabel `workspaces` ada. **Migrasi gagal tercatat selesai** — persis bug yang diperbaiki `-1`. |

Juga: `/readyz` pada DB gagal (`review_fail`) → HTTP **503** (bukan 200),
jadi gate/startup gagal secara terlihat. Koreksi penting vs laporan lama:
`-1` membungkus **seluruh pemanggilan** (001+002 sekaligus), bukan per file
— pada run yang gagal, 001 ikut terguling. Itu lebih ketat, bukan lebih
longgar; komentar patch sudah diperbaiki (§2d).

## 6. Yang belum terbukti (NOT RUN — bukan PASS)

- **CI GitHub asli** untuk workflow baru (butuh push; dilarang tugas ini).
  Yang ada: simulasi langkah-demi-langkah (§4 F7) + pemetaan di bawah.
- **`docker compose up` di host Windows** (fresh maupun upgrade): initdb,
  mount, line ending, dependensi service, healthcheck. Jalur yang sama
  diverifikasi pada PostgreSQL 16 native di Linux, bukan di Docker Windows.
- Build web (tidak ada perubahan web di patch ini).
- `deploy/linux/configure-postgres.sh` (tidak diubah patch ini).

Pemetaan simulasi CI → workflow yang diajukan (`.github/workflows/ci.yml`):

| Langkah CI | Simulasi di sini | Delta |
|---|---|---|
| Service `postgres:16-bookworm`, `POSTGRES_USER=postgres` | Cluster 16.15 native, bootstrap `postgres` | Bukan container; versi image = 16, sama mayor |
| Buat role runtime + `umnia_test` + `umnia_empty` via SQL | Skrip initdb yang diajukan + `CREATE DATABASE` (peran sama, password fixture) | Initdb di CI tidak dipakai; peran dibuat via SQL di CI |
| Migrasi sebagai bootstrap `psql -1 -f 001 -f 002` | Persis (F2) | — |
| Lint `gofmt` + `go vet` | Persis (F4) | — |
| Gate: `test -n URL`, `go test -v \| tee`, `PIPESTATUS`, `grep SKIP` | Persis, harfiah (F7) | Nama DB `review_fresh` bukan `umnia_test`; hasil sama (14/0/0) |
| Job web (`npm ci --legacy-peer-deps`, build) | NOT RUN | Tidak ada perubahan web |

## 7. Pemulihan (recovery) — batas yang didukung

- **Membuang diff kode bukan rollback database.** Patch ini tidak
  menyediakan `down` migration. Bila migrasi 002 gagal di tengah jalan
  pada database yang sudah dimigrasi 001, jalurnya: (a) dengan `-1`,
  transaksi gagal utuh — tidak ada yang perlu di-rollback (terbukti §5);
  (b) pada keadaan setengah terapan lama (tanpa `-1`, 002 tercatat tapi
  fungsi belum dialihkan), jalankan ulang migrasi 002 sebagai bootstrap —
  idempotent (terbukti L3) — lalu verifikasi grant 6/6.
- **Volume lama → volume baru:** hanya dump/restore (§4 R1). Volume lama
  dipertahankan sebagai cadangan (`...-data-old`) sampai verifikasi lolos;
  penghapusan volume lama HANYA sesudah backup + verifikasi — NOT RUN
  sebagai aksi destruktif dalam tugas ini (tidak ada volume pengguna
  yang dihapus).
- **Bila API sudah berjalan dengan role superuser (volume lama):** hentikan
  API terhadap volume itu (terbukti bypass RLS di L5); tidak ada
  "perbaikan" berbasis GRANT/REVOKE yang bisa mendemosi bootstrap role.
- **Backup sebelum upgrade:** `pg_dump --no-owner` dari database lama
  (langkah 1 di README baru). Restore yang gagal: ulangi dari dump yang
  sama; dump tidak diubah oleh restore. NOT RUN: restore dari backup
  biner Fisik (`pg_basebackup`) / PITR — tidak ada klaim di sini.

## 8. Asumsi dan keterbatasan yang dibawa (dari laporan sebelumnya)

- Nama role `umnia_business` dan superuser `postgres` adalah kontrak
  deployment yang di-hardcode migrasi (parameterisasi PROPOSED, di luar
  scope tugas ini).
- `checkDatabase` (`main.go`) meng-hardcode versi wajib `002_identity`;
  migrasi berikutnya harus memperbarui daftar itu.
- CI mengasumsikan `psql` tersedia di `ubuntu-latest` (bila tidak, langkah
  prepare gagal terlihat dan perlu `postgresql-client`).
- Segregasi admin plane masih sebatas service/kredensial terpisah; belum
  ada endpoint admin yang membuktikannya.
- UI login/workspace dan mailer undangan tetap di luar scope (gap menuju
  gate F1, bukan bagian patch ini).
- Fungsi trigger `app.reject_audit_change()` tidak masuk daftar hardening
  EXECUTE (6 helper saja); ia tidak mengakses data, hanya menolak
  UPDATE/DELETE audit — OBSERVED, tidak diubah.

## 9. Artefak (semua di `~/workspace/review/F1-FOUNDATION-REMEDIATION-REVIEW/`)

| Artefak | SHA-256 |
|---|---|
| `f1-remediation.patch` (10 file, 641 baris; `git apply --check` lolos pada baseline `577f0a5` di clone baru, lalu `git apply` menghasilkan status identik) | `a513734ac886e885cd3b1e7f8874594a983246852de2b0a774dec7e08336c742` |
| `REVIEW.md` (file ini) | lihat `MANIFEST.sha256` |
| `run-review-checks.sh` (part 1: fresh + gate) | `1079a2d8a52b228da34bd7a02dfdf2c6e1dc89b9f288239b5bfa01eac382543e` |
| `run-review-checks-2.sh` (part 2: legacy + diag + fail-injection) | `681bd85dbbb163c0d51d003f89c672ebd993115ae3ee5ec0d90ce7779445f998` |
| `run-review-checks-3.sh` (part 3: dump/restore upgrade) | `fe3c6b77c94f991d6739975ea5da82ae5694404ecc74a8e74c66fe1c25512228` |
| `run-mech-checks.sh` (mekanisme PostgreSQL T1–T3/M1–M2) | lihat `MANIFEST.sha256` |
| `inspect.sql`, `api-workflow.sh` | lihat `MANIFEST.sha256` |
| `evidence/` (80 file: log run + `run.log`/`run2.log`/`run3.log`/`mech.log`, `fresh-07-gotest.json`, `fresh-12-gate.log`, `legacy-dump.sql`) | lihat `MANIFEST.sha256` |

Verifikasi manifest: `cd <direktori-di-atas> && sha256sum -c MANIFEST.sha256`
(manifest tidak mencantumkan dirinya sendiri). Cara memakai patch:
`git checkout 577f0a5 && git apply f1-remediation.patch` lalu
`git status` harus menunjukkan 8 modified + 2 untracked yang sama dengan §1.

## 10. Keputusan yang diminta dari leader

1. Setujui / tolak patch ini (termasuk tiga perbaikan §2).
2. Bila setuju: commit + push → verifikasi **CI GitHub asli hijau**
   (termasuk job web) → uji `docker compose up` di Windows (fresh +
   upgrade dump/restore) sebelum klaim gate deployment lolos.
3. Tindak lanjut terpisah (bukan patch ini): parameterisasi nama role,
   alih kepemilikan skema `app` (§3), UI login/workspace, mailer undangan,
   bukti admin plane.
