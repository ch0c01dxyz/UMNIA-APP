# F1-WINDOWS-RUNTIME-ACCEPTANCE — laporan lingkungan + paket validasi (2026-11-06)

STATUS: BLOCKED (tidak ada runtime Windows di VM ini) | TASK_ID: F1-WINDOWS-VALIDATION-PACKET-CORRECTIONS | FILES CHANGED: paket `docs/reviews/f1-windows-runtime-acceptance/WINDOWS-VALIDATION-PACKET.md` (ditulis ulang, 7 koreksi) + laporan ini | TESTS: 0 pass / 0 fail / 0 skipped / 8 skenario NOT RUN (BLOCKED); verifikasi statis paket: 16/16 blok PowerShell parse OK (pwsh 7.5.4, syntax-only), 11/11 string skrip terverifikasi | RISK / BLOCKERS: tidak ada Windows, tidak ada Windows PowerShell 5.1, tidak ada Docker daemon — runtime Windows tidak dapat diuji dari VM ini | NEXT ACTION: jalankan paket validasi (versi terkoreksi) pada host Windows nyata (Docker Desktop), lalu laporkan hasilnya ke reviewer

Pengiriman: laporan ini + paket validasi terkoreksi di-commit dan di-push ke `origin/f1-03-identity` (ikuti aturan commit/push tetap; hash commit pada laporan akhir).

## 1. Keputusan review yang diterima (reviewer, OBSERVED dari prompt tugas)

- Scoped source review `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1`: **PASS** untuk dua perbaikan (routing file compose asli; inspeksi fail-closed). Reviewer menjalankan fungsi hasil ekstraksi AST: routing sumber/target lolos, exception inspeksi sintetis propagates, query kosong yang sukses tetap absent; parsing skrip 0 error.
- GitHub Actions run 42091279923: SUCCESS, 14 PASS / 0 FAIL / 0 SKIP (reviewer; tidak diverifikasi ulang dari VM ini — `gh` tidak login).
- Semua 26 entri manifest final-guards cocok dengan blob Git yang di-commit — **diverifikasi ulang secara lokal pada tugas ini** (TESTED, §3).
- Reviewer menjalankan `C:\Windows\System32\WindowsPowerShell\v1.0\powershell.exe` secara eksplisit, versi **5.1.26100.9818**, dan mem-parse skrip upgrade `934ad4b` dengan **zero errors** (reviewer-observed; bukti parser saja, bukan eksekusi container). Runtime `powershell` generik mode konteks di lingkungan preparer ini sebenarnya **7.6.9** — selalu catat executable dan versinya, jangan simpulkan PS 5.1 dari label tool.
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

1. **Pin revisi:** checkout terisolasi men-detach commit penuh `934ad4b8b79ae3cd5dca01121a270fd6f8f955f1` dan meng-assert `git rev-parse HEAD`; branch `f1-03-identity` yang bergerak tidak pernah di-checkout. Run directory unik (`run-<timestamp>`) dan gagal bila sudah ada. Checkout aktif pengguna tidak disentuh. Paket dibaca dari commit delivery `10bc3de`, terpisah dari checkout kode yang diuji.
2. **Lifecycle/port:** satu tabel lifecycle (§5 paket) menyatakan stack mana yang up/down sebelum dan sesudah tiap skenario; transisi memakai `stop` (volume dipertahankan), tidak pernah `down -v`. Target `umnia-val-new` dipertahankan (stopped) untuk cek occupied-target.
3. **Injeksi kegagalan S7:** mekanisme listener port 5432 DITOLAK dengan alasan (sumber sudah memiliki port sampai step 4; skrip lanjut ke step 5 dalam milidetik; tidak ada agen eksternal yang bisa menyela secara andal). Diganti kegagalan deterministik: salinan file compose baru dengan tag image yang tidak ada (`postgres:16-bookworm-VALIDATION-BAD-TAG-0000`), gagal pull di step 5 sesudah source shutdown, tanpa timing window. Injektor dihentikan dengan tidak lagi memakai file itu; checkout ter-pin tidak dimodifikasi.
4. **Cleanup terpisah:** `down -v` dan `docker rm -f` dihapus dari langkah tes; hanya ada di §13 paket (proposal cleanup, approval-required, identifier run-scoped eksak). Fixture baru memakai project terisolasi baru, bukan menghapus volume lama. Helper latar (writer) di-start hidden (`-WindowStyle Hidden`), PID dicatat, dan dihentikan hanya dengan PID yang dibuat run itu.
5. **Akuntansi writer:** writer dihentikan operator segera sesudah baris quiesce-sukses skrip (`penulis berhenti: tidak ada sesi aktif umnia_business`); log writer dibatasi ke jendela pre-dump; `201` sesudah waktu stop tercatat adalah FAIL. Bukti quiescence adalah output step-0c skrip sendiri (container penulis berhenti + 0 sesi aktif), bukan berhentinya writer. Traffic pasca-cutover memakai run/log terpisah.
6. **Restore self-contained:** `--no-owner` hanya melewati ownership; GRANT/REVOKE tetap di-restore kecuali `--no-privileges`/`--no-acl` — terverifikasi dari `pg_restore --help` terinstal (PostgreSQL 16.15, dari image `postgres:16-bookworm`) dan docs resmi `https://www.postgresql.org/docs/devel/app-pgrestore.html`. Skenario 8 kini menginventarisasi ACL dump (`pg_restore -f -`), pre-create role yang dirujuk (`umnia_business` NOLOGIN, meniru initdb target nyata), restore dengan `--no-owner --exit-on-error` TANPA `--no-privileges` (semantik ACL produksi divalidasi), dan memvalidasi jumlah baris + 12 grant EXECUTE secara terpisah. Error `pg_restore` tidak pernah diabaikan.
7. **Stop-on-error:** helper `Check`/`Expect-Fail`/`Get-Status`/`Confirm-Stack` di tiap skenario; exit non-zero menghentikan skenario (kecuali negatif yang di-assert); identitas stack diverifikasi sebelum memakai `localhost:8080`; hasil dicatat di `$run\results.md` (PASS/FAIL/NOT RUN). Paket TIDAK disebut "ready-to-run".

**Bukti verifikasi statis paket (preparer, 2026-11-06):** 16/16 blok ```powershell parse OK (parser `[System.Management.Automation.Language.Parser]::ParseFile`, PowerShell 7.5.4 di VM Linux preparer — syntax-only, BUKAN bukti parse 5.1; parse 5.1 atas blok-blok ini tetap untuk operator/reviewer di Windows). Scan sintaks khusus PS 7 (`??`, `?.`, `&&`, `||`, ternary): tidak ada operator PS 7 (dua `||` yang ditemukan adalah konkatenasi string SQL di dalam literal). 11/11 string literal skrip yang dikutip paket terverifikasi ada verbatim di `Upgrade-UmniaVolume.ps1` @`934ad4b`; 5/5 path repo yang dirujuk ada; `compose.yml` berisi tepat 3 baris `image: postgres:16-bookworm` (sesuai cek bad-tag). `down -v`/`docker rm -f` hanya ada di §13 paket (cleanup) plus satu larangan prose di §10.

## 8. Batasan yang belum teruji (UNKNOWN — tetap terbuka)

- Seluruh runtime Windows (startup, readiness negatif, upgrade PS 5.1, quiesce, negatif, pemulihan, bukti restore).
- Eksekusi PowerShell 5.1 atas skrip upgrade (parser 0 error bukan bukti eksekusi).
- UI login/workspace, mailer undangan, module registry (F1-04), dan bukti segregasi admin-plane — pekerjaan terpisah, bukan bagian tugas ini.
- CI run 42091279923 tidak diverifikasi ulang dari VM ini.
