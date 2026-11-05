# F1-WINDOWS-RUNTIME-ACCEPTANCE — laporan lingkungan + paket validasi (2026-11-06)

STATUS: BLOCKED (tidak ada runtime Windows di VM ini) | TASK_ID: F1-WINDOWS-RUNTIME-ACCEPTANCE | FILES CHANGED: laporan ini (catatan run attempt, §10) — paket tidak diubah | TESTS: 0 pass / 0 fail / 0 skipped / 8 skenario NOT RUN (BLOCKED); paket terverifikasi: SHA-256 cocok dengan hash reviewer, HEAD `cfa8a64` | RISK / BLOCKERS: tidak ada Windows, tidak ada Windows PowerShell 5.1, tidak ada Docker daemon — runtime Windows tidak dapat dieksekusi dari VM ini | NEXT ACTION: operator membawa paket (versi `2026-11-06`, commit `cfa8a64`, SHA-256 `b6199d10…`) ke host Windows nyata (Docker Desktop) dan menjalankan skenario 1–8 di sana; lihat §10

Pengiriman: laporan ini di-commit dan di-push ke `origin/f1-03-identity` (ikuti aturan commit/push tetap; hash commit pada laporan akhir). Paket tidak diubah pada tugas ini.

## 1. Keputusan review yang diterima (reviewer, OBSERVED dari prompt tugas)

- Scoped source review `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1`: **PASS** untuk dua perbaikan (routing file compose asli; inspeksi fail-closed). Reviewer menjalankan fungsi hasil ekstraksi AST: routing sumber/target lolos, exception inspeksi sintetis propagates, query kosong yang sukses tetap absent; parsing skrip 0 error.
- GitHub Actions run 42091279923: SUCCESS, 14 PASS / 0 FAIL / 0 SKIP (reviewer; tidak diverifikasi ulang dari VM ini — `gh` tidak login).
- Semua 26 entri manifest final-guards cocok dengan blob Git yang di-commit — **diverifikasi ulang secara lokal pada tugas ini** (TESTED, §3).
- Reviewer menjalankan `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` secara eksplisit, versi **5.1.26100.9818**, dan mem-parse skrip upgrade `934ad4b` dengan **zero errors** (reviewer-observed; bukti parser saja, bukan eksekusi container). Runtime `powershell` generik mode konteks di lingkungan preparer ini sebenarnya **7.6.9** — selalu catat executable dan versinya, jangan simpulkan PS 5.1 dari label tool.
- Reviewer juga mem-parse **semua 16 blok PowerShell dalam paket (versi `2db7d6d`)** dengan executable yang sama (5.1.26100.9818): **16 parsed, 0 errors** — bukti sintaks independen saja. Proses PS 5.1 fresh tidak memiliki fungsi `Check`; tidak ada tes runtime Docker yang tersirat.
- Windows runtime acceptance: **PENDING**. Overall F1 gate: **NOT COMPLETE**. Tidak ada merge/deploy/migrasi data pengguna yang diotorisasi.

## 2. Lingkungan validasi aktual (OBSERVED — perintah nyata, VM ini)

| Komponen | Hasil aktual | Perintah |
|---|---|---|
| OS | **Linux Ubuntu 24.04.5 LTS**, kernel 7.0.0-39-generic, x86_64 — BUKAN Windows | `uname -a`, `/etc/os-release` |
| Windows PowerShell 5.1 (`powershell`) | **TIDAK ADA** | `command -v powershell` |
| PowerShell 7.5.4 Core (`pwsh`) | Ada di `~/workspace/tools/pwsh/pwsh`, edition Core — **bukan** pengganti PS 5.1 (dilarang tugas ini sebagai bukti Windows) | `pwsh -Command '$PSVersionTable'` |
| WSL / Windows host | **TIDAK ADA** (`wsl.exe` tidak ada, `/mnt/c` tidak ada) | `command -v wsl.exe`, `ls /mnt/c` |
| Docker client | 29.1.3 (binary statis) | `docker --version` |
| Docker Compose plugin | v2.40.1 | `docker compose version` |
| Docker daemon/engine | **TIDAK BERJALAN** — `docker info` exit 1: `failed to connect to the docker API at unix:///var/run/docker.sock`; tidak ada proses `dockerd` | `docker info`, `pgrep -a dockerd` |
| Port 5432/5433/8080/9000/3000 | Tidak ada yang listen di VM ini (bebas, tidak relevan untuk Windows) | `ss -tln` |
| HEAD / tree | `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1`, branch `f1-03-identity`, tree bersih | `git rev-parse`, `git status` |

**Kesimpulan lingkungan: BLOCKED.** Tidak ada satu pun prasyarat runtime Windows yang tersedia. Sesuai instruksi tugas, pengujian dependen (skenario 1–8) DIHENTIKAN di sini; tidak ada substitusi (PostgreSQL native Linux, pwsh di Linux, mock Docker, atau sukses parser) yang dipakai sebagai bukti Windows.

## 3. Verifikasi independen yang masih bisa dilakukan di VM ini (TESTED)

- **Integritas bundle final-guards:** `cd docs/reviews/f1-upgrade-final-guards && sha256sum -c MANIFEST.sha256` → semua 26 entri OK, exit 0. `git ls-files -s` menunjukkan 27 file terlacak di direktori itu (26 entri manifest + `MANIFEST.sha256` sendiri) pada commit `934ad4b`. Ini membuktikan integritas artefak, BUKAN kebenaran perilaku (sesuai peringatan tugas).
- **Hash skrip yang diuji:** `deploy/windows/Upgrade-UmniaVolume.ps1` SHA-256 `74dbe651b4836caa4a9d947145693222378e45752d2a20ccfc7f80b1d42c5057` — sama dengan yang tercatat di laporan final-guards.
- **Kontrak compose yang dipakai paket (OBSERVED):** `deploy/windows/compose.yml` — postgres-business `127.0.0.1:5432`, postgres-admin `127.0.0.1:5433`, migrate (`psql -1` 001+002 sebagai `umnia_bootstrap`, `depends_on` postgres healthy, `restart: "no"`), rustfs `127.0.0.1:9000`, business-api build `../../services/business-api` (`127.0.0.1:8080`, `depends_on` postgres healthy + migrate `service_completed_successfully`, healthcheck `curl -f /healthz`), web build `../../apps/web` (`127.0.0.1:3000`). Volume `<project>_postgres-business-data` / `postgres-admin-data` / `rustfs-data`. Readiness: `/readyz` mensyaratkan `schema_migrations` versi `002_identity` (`services/business-api/main.go:84`).

## 4. Per-scenario evidence (semua NOT RUN — BLOCKED)

| # | Skenario | Status | Bukti / alasan |
|---|---|---|---|
| 1 | Fresh startup Windows Compose | NOT RUN (BLOCKED) | Tidak ada Docker daemon/engine Windows |
| 2 | Readiness negative (migrasi belum jalan → tidak ready) | NOT RUN (BLOCKED) | Sama |
| 3 | Legacy upgrade via PS 5.1 (running + stopped source) | NOT RUN (BLOCKED) | Tidak ada PS 5.1, tidak ada daemon |
| 4 | Maintenance sumber sebelum snapshot (writer terkendali) | NOT RUN (BLOCKED) | Sama |
| 5 | Identitas runtime target + isolasi tenant (14 langkah) | NOT RUN (BLOCKED) | Sama |
| 6 | Negatif: equal/occupied/missing/invalid file/inspection failure | NOT RUN (BLOCKED) | Sama |
| 7 | Kegagalan terkendali sesudah source shutdown + pemulihan | NOT RUN (BLOCKED) | Sama |
| 8 | Hash backup + bukti restore (bukan hanya `pg_restore -l`) | NOT RUN (BLOCKED) | Sama |

Tidak ada skenario yang dijalankan, sebagian dijalankan, atau diklaim lolos dari VM ini. Total suite 110-check dari tugas sebelumnya TIDAK dijalankan ulang di sini dan TIDAK diklaim.

## 5. Inventaris resource (OBSERVED)

- **Tidak ada resource tes yang dibuat pada tugas ini.** Tidak ada container, volume, project Compose, file backup, atau layanan yang di-start. Tidak ada perubahan pada database/volume pengguna (tidak ada yang disentuh). Tidak ada software yang di-install/di-upgrade.
- Satu-satunya artefak baru: laporan ini + paket validasi di bawah (file dokumentasi di repo).

## 6. Prasyarat yang hilang — daftar eksak (untuk host Windows)

1. Host Windows 10/11 (64-bit) dengan virtualisasi aktif; Docker Desktop 4.x+ mode Linux containers + WSL 2 backend; daemon berjalan dan `docker info` menunjukkan Server.
2. Windows PowerShell 5.1 bawaan (`$PSVersionTable.PSVersion` = 5.1.x); Git untuk Windows.
3. Port `127.0.0.1:5432, 5433, 8080, 9000, 3000` bebas.
4. Akses internet untuk pull image `postgres:16-bookworm` dan `rustfs/rustfs:1.0.4` (atau image sudah ter-cache).
5. ~10 GB ruang disk kosong; checkout terisolasi (bukan repo kerja pengguna).

## 7. Paket validasi — versi terkoreksi (2026-11-06, TASK_ID: F1-WINDOWS-VALIDATION-PACKET-CORRECTIONS)

`f1-windows-runtime-acceptance/WINDOWS-VALIDATION-PACKET.md` ditulis ulang untuk 7 koreksi reviewer (application source tidak berubah sejak `934ad4b`; scoped source PASS tetap valid):

1. **Pin revisi:** checkout terisolasi men-detach commit penuh `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` dan meng-assert `git rev-parse HEAD`; branch `f1-03-identity` yang bergerak tidak pernah di-checkout. Run directory unik (`run-<timestamp>`) dan gagal bila sudah ada. Checkout aktif pengguna tidak disentuh. Paket dibaca dari commit delivery yang tercatat di laporan ini (lihat header laporan); versi paket `2026-11-06` — verifikasi dengan isi file, bukan dengan hash commit yang ditebak (lihat §8).
2. **Lifecycle/port:** satu tabel lifecycle (§5 paket) menyatakan stack mana yang up/down sebelum dan sesudah tiap skenario; transisi memakai `stop` (volume dipertahankan), tidak pernah `down -v`. Target `umnia-val-new` dipertahankan (stopped) untuk cek occupied-target.
3. **Injeksi kegagalan S7:** mekanisme listener port 5432 DITOLAK dengan alasan (sumber sudah memiliki port sampai step 4; skrip lanjut ke step 5 dalam milidetik; tidak ada agen eksternal yang bisa menyela secara andal). Diganti kegagalan deterministik: salinan file compose baru dengan tag image yang tidak ada (`postgres:16-bookworm-VALIDATION-BAD-TAG-0000`), gagal pull di step 5 sesudah source shutdown, tanpa timing window. Injektor dihentikan dengan tidak lagi memakai file itu; checkout ter-pin tidak dimodifikasi.
4. **Cleanup terpisah:** `down -v` dan `docker rm -f` dihapus dari langkah tes; hanya ada di §13 paket (proposal cleanup, approval-required, identifier run-scoped eksak). Fixture baru memakai project terisolasi baru, bukan menghapus volume lama. Helper latar (writer) di-start hidden (`-WindowStyle Hidden`), PID dicatat, dan dihentikan hanya dengan PID yang dibuat run itu.
5. **Akuntansi writer:** writer dihentikan operator segera sesudah baris quiesce-sukses skrip (`penulis berhenti: tidak ada sesi aktif umnia_business`); log writer dibatasi ke jendela pre-dump; `201` sesudah waktu stop tercatat adalah FAIL. Bukti quiescence adalah output step-0c skrip sendiri (container penulis berhenti + 0 sesi aktif), bukan berhentinya writer. Traffic pasca-cutover memakai run/log terpisah.
6. **Restore self-contained (diperbarui §8):** `--no-owner` hanya melewati ownership; GRANT/REVOKE tetap di-restore kecuali `--no-privileges`/`--no-acl` — terverifikasi dari `pg_restore --help` terinstal (PostgreSQL 16.15, dari image `postgres:16-bookworm`) dan docs resmi `https://www.postgresql.org/docs/devel/app-pgrestore.html`. Skenario 8 menginventarisasi ACL dump (`pg_restore -f -`) dan membuat HANYA role yang ditunjukkan inventory sebagai `NOLOGIN` (inventory adalah otoritas; ekspektasi berbasis sumber: tidak ada baris GRANT/REVOKE — ACL legacy semuanya default — dan `OWNER TO postgres` di inventory berarti backup salah, STOP). Restore dengan `--no-owner --exit-on-error` TANPA `--no-privileges` (semantik ACL produksi divalidasi). Grant divalidasi terhadap keadaan LEGACY (6× `EXECUTE TO PUBLIC`, 0 grant pasca-migrasi); cek 12 grant milik skenario 5 / step 8a, bukan skenario 8. Error `pg_restore` tidak pernah diabaikan.
7. **Stop-on-error:** helper `Check`/`Expect-Fail`/`Get-Status`/`Confirm-Stack` di tiap skenario; exit non-zero menghentikan skenario (kecuali negatif yang di-assert); identitas stack diverifikasi sebelum memakai `localhost:8080`; hasil dicatat di `$run\results.md` (PASS/FAIL/NOT RUN). Paket TIDAK disebut "ready-to-run".

**Bukti verifikasi statis paket (preparer, 2026-11-06):** 16/16 blok ```powershell parse OK (parser `[System.Management.Automation.Language.Parser]::ParseFile`, PowerShell 7.5.4 di VM Linux preparer — syntax-only, BUKAN bukti parse 5.1; parse 5.1 atas blok-blok ini tetap untuk operator/reviewer di Windows). Scan sintaks khusus PS 7 (`??`, `?.`, `&&`, `||`, ternary): tidak ada operator PS 7 (dua `||` yang ditemukan adalah konkatenasi string SQL di dalam literal). 11/11 string literal skrip yang dikutip paket terverifikasi ada verbatim di `Upgrade-UmniaVolume.ps1` @`934ad4b`; 5/5 path repo yang dirujuk ada; `compose.yml` berisi tepat 3 baris `image: postgres:16-bookworm` (sesuai cek bad-tag). `down -v`/`docker rm -f` hanya ada di §13 paket (cleanup) plus satu larangan prose di §10.

## 8. Koreksi bootstrap/restore/metadata (2026-11-06, TASK_ID: F1-PACKET-BOOTSTRAP-FIXES)

Application source tidak berubah sejak `934ad4b`; scoped source PASS tetap valid. Tiga koreksi pada paket (versi `2026-11-06`):

1. **Urutan bootstrap helper (§1–§2 paket).** Bug: §1 memanggil `Check` padahal §2 baru mendefinisikannya; di sesi PowerShell fresh, pemanggilan itu gagal sebagai unknown command — sukses parser tidak bisa menangkap fungsi yang hilang. Perbaikan: definisi keempat helper (`Check`, `Expect-Fail`, `Get-Status`, `Confirm-Stack`) + `$ErrorActionPreference = 'Stop'` dipindah ke ATAS §1, sebelum pemakaian pertama; §2 kini hanya berisi kebijakan. `Check`/`Expect-Fail` diperkuat untuk kasus `$LASTEXITCODE` yang `$null` (sebelum ada native command). Aturan native vs cmdlet ditegakkan: `Check` hanya sesudah native command (`docker`/`git`/`psql`/`pg_restore`/`powershell`); cmdlet (`New-Item`, `cd`, `Copy-Item`) mengandalkan terminating error di bawah `$ErrorActionPreference = 'Stop'` — `Check` sesudah cmdlet dihapus di 12 tempat (9× `cd`, 2× `New-Item`, 1× `Copy-Item` diganti assert `Test-Path` eksplisit). Pipeline `docker ... | Select-String | Out-File` tetap memakai `Check` karena `$LASTEXITCODE`-nya adalah kode `docker` (terverifikasi, lihat di bawah). Semua skenario berjalan dalam SATU sesi yang dimulai di §1; sesi baru wajib menempel ulang blok helper §1 dulu. Window B §6b hanya memakai cmdlet bawaan (tidak butuh helper).
2. **Role container restore (§11 paket).** Klaim lama ("`postgres` sudah ada di semua container postgres") SALAH: entrypoint resmi hanya membuat `POSTGRES_USER` yang dikonfigurasi. Bukti di repo: initdb proyek membuat role itu secara eksplisit (`deploy/windows/initdb/01-runtime-roles.sh:72-78`, `CREATE ROLE postgres SUPERUSER LOGIN`) karena entrypoint tidak membuatnya; kegagalan lama `FATAL: role "postgres" does not exist` pernah teramati. Inventaris berbasis sumber (migrasi @`934ad4b`): 002 berhenti di `ALTER FUNCTION ... OWNER TO postgres` pertama (baris 350) karena role `postgres` tidak ada di cluster legacy — maka blok `REVOKE FROM PUBLIC` + `GRANT EXECUTE TO umnia_business` (371-382) tidak pernah diterapkan; ACL dump semuanya default PostgreSQL (`EXECUTE TO PUBLIC` untuk fungsi, sesuai komentar 002 baris 359-362). Inventory runtime (`pg_restore -f -`) adalah otoritas saat eksekusi: loop di §11 membuat SETIAP role yang disebut baris GRANT/REVOKE sebagai `NOLOGIN` (tanpa LOGIN/SUPERUSER); `OWNER TO postgres` di inventory berarti backup salah → STOP (bukan membuat role untuk menutupi backup yang salah). Role fixture dibedakan dari kontrak deployment (di deployment nyata, `postgres` adalah SUPERUSER pemilik fungsi SECURITY DEFINER dan `umnia_business` adalah role runtime NOSUPERUSER/NOBYPASSRLS — keduanya dibuat initdb). Validasi grant skenario 8 dikoreksi ke keadaan LEGACY: 6× `EXECUTE TO PUBLIC`, 0 grant pasca-migrasi; cek 12 grant tetap di skenario 5 / step 8a. Inventory role, exit status restore, dan grant efektif ditangkap ke `$run\logs\restore-*.txt`.
3. **Metadata delivery.** Paket tidak lagi menyebut `10bc3de` sebagai delivery commit-nya. Header paket kini membedakan commit aplikasi yang diuji (`934ad4b`, tetap) dari revisi delivery dokumen (`Packet version: 2026-11-06`) dan memberi instruksi verifikasi yang dapat diputuskan operator (`Select-String` untuk baris versi + `git log --oneline -3 -- <file>` — entri teratas tidak boleh `2db7d6d` atau `10bc3de`). Tidak ada hash commit masa depan yang ditulis ke dalam file; commit delivery aktual dicatat di laporan ini.

**Bukti verifikasi statis paket versi `2026-11-06` (preparer, 2026-11-06):** 15/15 blok ```powershell parse OK (pwsh 7.5.4, syntax-only — BUKAN bukti parse 5.1; parse 5.1 atas versi ini tetap untuk operator/reviewer; parse 5.1 reviewer atas 16 blok versi `2db7d6d` bukan parse atas versi ini). Order check statis atas blok-blok dalam urutan dokumen: 95 call site helper diperiksa, 0 pelanggaran — setiap pemanggilan `Check`/`Expect-Fail`/`Get-Status`/`Confirm-Stack`/`Call-Api`/`Expect-Step`/`Get-Json` didahului definisinya. Fresh-session test helper (pwsh 7.5.4, proses baru): T1 `Check` sebelum definisi gagal (unknown command — kelas bug asal); T2 `Check` throw sesudah native gagal; T3 `Check` lolos sesudah native sukses; T4 `Check` tidak throw saat `$LASTEXITCODE` `$null`; T5 `Expect-Fail` throw pada exit 0; T6 lolos pada non-zero; T7 cmdlet gagal throw di bawah `$ErrorActionPreference = 'Stop'`; T8 `Check` sesudah cmdlet membaca kode stale (membuktikan bahaya yang dihindari paket); T9/T9b regex ekstraksi role GRANT/REVOKE benar. Semantik `$LASTEXITCODE` terverifikasi di pwsh 7.5.4: cmdlet tidak me-reset-nya; pipeline native-pertama mempertahankan kode native-nya. Scan sintaks khusus PS 7: tidak ada operator PS 7 (hanya `||` konkatenasi SQL). 11/11 string skrip terverifikasi ulang; `down -v`/`docker rm -f` hanya di §13 paket.

## 9. Batasan yang belum teruji (UNKNOWN — tetap terbuka)

- Seluruh runtime Windows (startup, readiness negatif, upgrade PS 5.1, quiesce, negatif, pemulihan, bukti restore).
- Eksekusi PowerShell 5.1 atas skrip upgrade (parser 0 error bukan bukti eksekusi).
- UI login/workspace, mailer undangan, module registry (F1-04), dan bukti segregasi admin-plane — pekerjaan terpisah, bukan bagian tugas ini.
- CI run 42091279923 tidak diverifikasi ulang dari VM ini.

## 10. Run attempt 2026-11-06 (TASK_ID: F1-WINDOWS-RUNTIME-ACCEPTANCE) — BLOCKED, no resources created

**Packet verification (TESTED, sebelum inspeksi lingkungan).** HEAD `cfa8a646e6f1c3d309ceca70495be143bddc986c` (`cfa8a64`, "F1-PACKET-BOOTSTRAP-FIXES"), `origin/f1-03-identity` sama, working tree bersih. Paket `docs/reviews/f1-windows-runtime-acceptance/WINDOWS-VALIDATION-PACKET.md` SHA-256 `b6199d106649b9a94bb0a4fd8345911986b02c07394c905c48c74f54a45a08b7` — **cocok persis** dengan hash yang diverifikasi reviewer. Code under test tetap `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1`. Reviewer: 15/15 blok PowerShell parse dengan Windows PowerShell 5.1.26100.9818 eksplisit; helper terfokus (sukses, native failure, unexpected success pada negatif) terverifikasi; restore role inventory hanya di-review di level sumber — perilaku `pg_restore` aktual tetap harus diuji.

**Environment inspection (OBSERVED, perintah nyata, VM ini — prasyarat paket §0):**

| Prasyarat paket | Hasil aktual | Perintah |
|---|---|---|
| P1 Windows 10/11 | **GAGAL — Linux Ubuntu 24.04.5 LTS**, kernel 7.0.0-39-generic | `uname -a`, `/etc/os-release` |
| P2 Windows PowerShell 5.1 | **GAGAL — `powershell` tidak ditemukan**; `/mnt/c` tidak ada; `wsl.exe` tidak ditemukan | `command -v powershell`, `ls /mnt/c`, `command -v wsl.exe` |
| P3 Docker Desktop daemon | **GAGAL — tidak ada `dockerd`, tidak ada `/var/run/docker.sock`, `docker info` tanpa Server** (client statis 29.1.3 ada di `~/workspace/tools/docker/`) | `~/workspace/tools/docker/docker info`, `pgrep -a dockerd`, `ls /var/run/docker.sock` |
| pwsh 7.5.4 Core | Ada — **bukan** pengganti PS 5.1 (dilarang sebagai bukti Windows) | `pwsh -Command '$PSVersionTable'` |

**Keputusan: BLOCKED.** Tiga prasyarat paket gagal sekaligus (P1, P2, P3). Sesuai instruksi tugas, pekerjaan dependen DIHENTIKAN di sini; tidak ada skenario yang dijalankan, tidak ada substitusi (PostgreSQL native Linux, pwsh di Linux, mock Docker, sukses parser) yang dipakai sebagai bukti Windows. Tidak ada rewrite aplikasi atau dokumentasi spekulatif yang dibuat karena lingkungan yang sama masih belum tersedia.

**Eight-scenario results matrix (semua NOT RUN — BLOCKED):**

| # | Skenario | Status | Bukti / alasan |
|---|---|---|---|
| 1 | Fresh startup Windows Compose | NOT RUN (BLOCKED) | P1+P2+P3 gagal (lihat tabel di atas) |
| 2 | Readiness negative (migrasi belum jalan → tidak ready) | NOT RUN (BLOCKED) | Sama |
| 3 | Legacy upgrade via PS 5.1 (running + stopped source) | NOT RUN (BLOCKED) | Sama |
| 4 | Maintenance sumber sebelum snapshot (writer terkendali) | NOT RUN (BLOCKED) | Sama |
| 5 | Identitas runtime target + isolasi tenant (14 langkah) | NOT RUN (BLOCKED) | Sama |
| 6 | Negatif: equal/occupied/missing/invalid file/inspection failure | NOT RUN (BLOCKED) | Sama |
| 7 | Kegagalan terkendali sesudah source shutdown + pemulihan | NOT RUN (BLOCKED) | Sama |
| 8 | Hash backup + bukti restore (bukan hanya `pg_restore -l`) | NOT RUN (BLOCKED) | Sama; perilaku `pg_restore` aktual tetap UNKNOWN |

**Resource inventory (OBSERVED): tidak ada resource tes yang dibuat.** Tidak ada container, volume, project Compose, file backup, run directory, atau layanan yang di-start. Checkout pengguna, `.env`, services, database, dan volume tidak disentuh. Tidak ada software yang di-install/di-upgrade. Tidak ada cleanup yang dibutuhkan dan tidak ada cleanup yang dilakukan (tidak ada yang dibuat untuk dibersihkan).

**Handoff ke operator (melalui user, bukan pesan eksternal otomatis).** Paket yang harus dibawa ke host Windows yang mampu: `docs/reviews/f1-windows-runtime-acceptance/WINDOWS-VALIDATION-PACKET.md`, versi `2026-11-06`, delivery commit `cfa8a64`, SHA-256 `b6199d106649b9a94bb0a4fd8345911986b02c07394c905c48c74f54a45a08b7`, code under test `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` (di-pin di checkout run terisolasi sesuai §1 paket). Prasyarat host (paket §0): Windows 10/11 64-bit + virtualisasi aktif; Windows PowerShell 5.1 di `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` (catat versi aktual); Docker Desktop 4.x+ mode Linux containers + WSL 2 backend, daemon berjalan (`docker info` menunjukkan Server); Git untuk Windows; port 5432/5433/8080/9000/3000 bebas; image `postgres:16-bookworm` + `rustfs/rustfs:1.0.4`; ~10 GB disk. Operator menjalankan skenario 1–8 sesuai urutan paket dengan kredensial/proyek/data sintetis terisolasi, dan mengembalikan transcript + `results.md` + hash backup per §12 paket. Tidak ada pesan eksternal yang dikirim otomatis oleh tugas ini.
