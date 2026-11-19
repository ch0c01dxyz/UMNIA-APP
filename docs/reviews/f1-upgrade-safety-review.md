# F1-UPGRADE-SAFETY-REVIEW — laporan perbaikan (2026-11-06)

> Historical report. Its original evidence package is [pinned in Git history](https://github.com/ch0c01dxyz/UMNIA-APP/tree/be34a24e6f73e04c77263cecb1d43dd59e72c616/docs/reviews/f1-upgrade-safety-review). See the [review index](README.md) for relocated helpers and current status.

STATUS: SELESAI (perbaikan MF1–MF4 terverifikasi pada harness; eksekusi Windows nyata MASIH NOT RUN) | TASK_ID: F1-UPGRADE-SAFETY-REVIEW | FILES CHANGED: lihat §7 | TESTS: baseline repro 17 checks lolos, fixed 36 checks lolos, check-deployment 18/0/0 | RISK / BLOCKERS: `docker compose up` + skrip upgrade di host Windows nyata dan PowerShell 5.1 belum pernah dijalankan | NEXT ACTION: review leader, lalu CI untuk commit baru, lalu uji Windows nyata sebelum klaim F1 gate lolos

## 1. Baseline dan lingkungan (OBSERVED)

- Baseline: `599e40e6916971856e384d25721ec42a4e43c271` pada `origin/f1-03-identity` (branch `f1-03-identity`, belum merge ke `main`).
- Skrip baseline yang direproduksi: `deploy/windows/tests/baseline-Upgrade-UmniaVolume.ps1`, SHA-256 `5e669b20106ed99a47091cbb05c84b8b6f7584dc9c53f79345fcd71dda90e275` (byte-identik dengan 599e40e).
- Skrip yang diperbaiki: `deploy/windows/Upgrade-UmniaVolume.ps1`, SHA-256 `89835d2c42df4287ca677f0aa18515bb8531fc61664f145fb70e3d2f72683ec8` (hash konten file; tidak berubah oleh commit).
- Versi teruji: Go 1.27.3, PostgreSQL 16.15 (image resmi `postgres:16-bookworm`, Id `sha256:cadebe7c08430c896edac883fa92f8de2c36e330805e770fbee8a7eae13f144f`), PowerShell 7.5.4 (parser + harness; PowerShell 5.1 NOT RUN), Node tidak relevan untuk tugas ini.
- CI baseline 599e40e: run 42079731471 SUCCESS, 14 PASS / 0 FAIL / 0 SKIP (dikonfirmasi reviewer; tidak memvalidasi skrip upgrade atau runtime Windows). CI untuk commit baru tugas ini BELUM dicek (menunggu push).

## 2. Kategori bukti (AAHE)

- **TESTED — mekanika SQL/postgres/API nyata:** semua skenario menjalankan binary postgres image asli dan binary `business-api` asli terhadap cluster nyata (fixture legacy + target + fixedsrc), dengan `pg_dump`/`pg_restore`/`psql`/`migrate` sungguhan.
- **TESTED — eksekusi skrip PowerShell via faithful mock:** `deploy/windows/tests/mock-docker-faithful.ps1` menggantikan transport `docker` dengan eksekusi nyata perintah-perintah di atas (compose `exec`/`up`/`down`/`stop`/`cp`/`ps`/`volume`/`inspect`). Mock ini MENOLAK canned-success: perintah yang tidak dimodelkan gagal keras (exit 1).
- **TESTED — parsing Windows PowerShell:** skrip lolos parser PowerShell (0 error). Eksekusi PowerShell 5.1 NOT RUN.
- **INFERRED — semantik `pg_isready`:** terukur langsung (lihat §3 MF1), bukan asumsi.
- **NOT RUN — eksekusi Windows nyata:** `docker run`/`docker compose up` dan skrip upgrade di host Windows tidak pernah dijalankan di mana pun (runc setns diblokir di VM ini). Jangan klaim F1 gate lolos dari laporan ini.

## 3. Per-finding: reproduksi baseline → perbaikan → bukti

### MF1 — akun sumber legacy salah (TESTED)

**Reproduksi baseline (6/6 checks, `base-mf1-rerun4.log` + `mf1-output.log`):**
skrip baseline gagal pada sumber legacy sejati (exit ≠ 0); `pg_dump -U umnia_bootstrap` gagal dengan `FATAL: role "umnia_bootstrap" does not exist`; tidak ada data dump yang dihasilkan (file output 0 byte); fixture utuh (1 workspace); API lama tetap 200.
**Koreksi terhadap laporan awal:** reproduksi pertama mencatat loop `pg_isready` 12× sebagai titik gagal. Pengukuran langsung sesudahnya membuktikan `pg_isready -U umnia_bootstrap` mengembalikan 0 ("accepting connections") untuk role yang TIDAK ADA — `pg_isready` tidak memverifikasi kredensial sama sekali. Titik gagal baseline yang sebenarnya adalah `pg_dump`, bukan loop readiness. Klaim 12× itu adalah artefak harness awal, bukan perilaku baseline; laporan ini menggantikannya dengan bukti di atas. Temuan MF1 justru diperkuat: readiness loop baseline tidak pernah bisa menangkap akun yang salah.

**Perbaikan:** parameter baru `-SourceDbUser` (default `umnia_business`) dan `-SourceDbName` (default `umnia_business`); langkah 0b memverifikasi identitas dengan query SQL terautentikasi (`SELECT current_user` harus sama dengan `-SourceDbUser`, dan tabel `workspaces` harus ada) sebelum dump apa pun; `umnia_bootstrap` hanya dipakai pada target. Nama volume sumber dicatat dari `docker inspect` (bukan asumsi nama).

**Bukti perbaikan (`fixed-happy-run.log`, 13/13; `fixed-neg-run.log`, 13/13):** dump memakai `pg_dump -U umnia_business -d umnia_business`; tidak ada `-U umnia_bootstrap` pada jalur sumber; sumber salah (`-SourceDbUser umnia_bootstrap`) ditolak dengan ZERO mutating docker calls dan tanpa dump.

### MF2 — tanpa preflight, pemulihan rusak (TESTED)

**Reproduksi baseline:** equal project (`baseline-mf2-equal-final.log`, 2/2): baseline TIDAK menolak; 8 mutating docker calls, dump, lalu gagal di `pg_restore` ke database-nya sendiri. Target sudah terisi (`baseline-mf2-existing-target-final.log`, 3/3): baseline mematikan SOURCE walau target terisi, lalu gagal restore ke target yang terisi; marker target tetap ada sesudahnya.

**Perbaikan:** preflight langkah 0 — SEBELUM perintah mutating apa pun: (a) tolak project sama sesudah normalisasi (`Normalize-ProjectName`: lowercase, strip `[^a-z0-9_-]`, trim `-`/`_`) dengan ZERO docker calls; (b) tolak bila project baru masih punya container berjalan (`ps -q`) atau volumenya sudah ada (`docker volume inspect` untuk `postgres-business-data`/`postgres-admin-data`/`rustfs-data`) — tanpa menghapus/menimpa apa pun; (c) sumber harus ada (volume sumber ada; bila tidak, throw dan minta `-OldProject` yang benar — default `umnia-legacy` BUKAN bukti nama project asli). Pemulihan (`Get-RecoveryText`): langkah 1 hentikan project baru dulu (`-p <NewProject> down`, tanpa `-v`) supaya port host bebas; langkah 2 jalankan kembali stack LAMA LENGKAP (termasuk `business-api`, supaya penulis bisa menulis lagi) dengan file compose ASLI volume lama (`-LegacyComposeFile`; bila kosong, pakai `-ComposeFile` + peringatan eksplisit bahwa file itu harus file asli, bukan file compose baru).

**Bukti perbaikan (`fixed-neg-run.log`, 13/13):** equal → ditolak + ZERO docker calls; target terisi (container berjalan) → ditolak + ZERO mutating calls + marker utuh + API lama tetap jalan; target terisi dengan container BERHENTI (hanya volume ada) → tetap ditolak lewat cek volume + ZERO mutating calls. **Bukti pemulihan (`fixed-fail-run.log` + `faildown-output.log`, 8/8):** restore digagalkan sengaja (injeksi `FailRestore`); skrip exit ≠ 0; source sudah `down`; teks pemulihan menyebut file compose ASLI + `-p umnia down` + `-p umnia-legacy up -d`; eksekusi jalur pemulihan via mock mengembalikan API lama 200, fixture utuh, dan write baru 201.

### MF3 — write hilang antara dump dan cutover (TESTED)

**Reproduksi baseline (`baseline-mf3-writer-final.log`, 2/2):** writer terkendali (interval 0,1 dtk) terhadap API lama selama upgrade baseline — write yang diakui (HTTP 201) SESUDAH snapshot dump HILANG dari target (pada run ini 2 write hilang; run pertama 1 write hilang — jumlahnya tergantung timing, cacatnya sama).

**Perbaikan:** parameter switch `-ConfirmMaintenance` (wajib; tanpa itu skrip berhenti sebelum menyentuh apa pun). Langkah 0c: hentikan `business-api` dan `web` project lama bila berjalan, lalu loop observabel (5×): kedua service `ps -q` kosong DAN `pg_stat_activity` menunjukkan 0 sesi aktif untuk `-SourceDbUser` (di luar backend-nya sendiri), bila tidak throw. Penulis tetap berhenti sampai cutover (langkah 7) atau pemulihan berhasil. Batas jaminan didokumentasikan di skrip dan README: skrip hanya bisa menghentikan penulis yang dikelola Compose; klien DB langsung / instansi API di luar Compose adalah tanggung jawab operator.

**Bukti perbaikan (`fixed-happy-run.log`, 13/13):** `stop business-api` tercatat SEBELUM `pg_dump`; zero acknowledged writes sesudah dump (after=0); target mempertahankan SEMUA pre-dump writes (target=12, expected=12 — termasuk write writer sebelum dump).

### MF4 — upgrade membuat akun/data tes permanen (TESTED)

**Reproduksi baseline (`baseline-mf4-probe-final.log`, 4/4):** upgrade baseline sukses tetapi meninggalkan 2 synthetic subjects (`%-upgrade-%`), 1 synthetic workspace (`Toko Upgrade %`), dan ≥3 mutating HTTP calls di database hasil restore.

**Perbaikan:** langkah 8 skrip ditulis ulang menjadi read-only: role f/f, `002_identity` tercatat, EXECUTE grants 12 (6 helper × postgres+umnia_business), `/readyz` 200, dan `SELECT count(*)` workspaces hasil restore. Tidak ada register/login/pembuatan workspace di skrip mana pun (`UpgradePass123` dihapus; tidak ada `Invoke-RestMethod`).

**Bukti perbaikan (`fixed-happy-run.log`, 13/13):** subject count tidak berubah vs sebelum upgrade (subjects=2); zero mutating HTTP selama upgrade. **Isolasi tenant tetap tercakup terpisah (`fixed-isolated-run.log`, 2/2):** workflow API `docs/reviews/f1-windows-review-fixes/scripts/api-workflow.sh` terhadap API target hasil upgrade (fixture sintetis terisolasi, BUKAN di dalam skrip upgrade): exit 0, 14/14 STEP lolos (register/login/403 lintas tenant).

## 4. Bug harness yang ditemukan dan diperbaiki selama tugas ini (TESTED)

Semua di bawah ini adalah bug harness/pengujian, BUKAN cacat produk — diperbaiki di `deploy/windows/tests/` dan diverifikasi oleh skenario yang lolos:

1. `Invoke-Upgrade` mengembalikan array (seluruh output skrip + exit code) karena `Tee-Object` meneruskan objek; `$code -eq 0` pada array itu bernilai falsy di PowerShell (array satu elemen `@(0)` dievaluasi sebagai skalar 0 → false). Perbaikan: `| Out-Null` sesudah `Tee-Object` sehingga return value adalah skalar int. Ini menjelaskan CHECK FAIL palsu pertama pada `fixed-happy`.
2. Pidfile API mock vs `manage-test-env.sh` tidak selaras (`api-upgraded-new.pid` vs `api-new.pid`): mock kini memakai nama pendek API (`legacy`/`new`/`fixedsrc`) yang sama dengan sisi bash.
3. `pg_dump` membuat file output 0 byte SEBELUM connect (terukur langsung): asersi repro MF1 diubah dari "file tidak ada" menjadi "tidak ada data dump (bytes=0)".
4. Staging dump antar-run tidak dibersihkan: skenario MF1 kini menghapus dump lama sebelum run.
5. `*>&1` diperlukan di `Invoke-Upgrade` agar log output ada walau skrip gagal di preflight tanpa stdout (Write-Host memakai information stream).

## 5. Hasil tes — ringkasan

| Suite | Hasil |
|---|---|
| Baseline repro MF1 (mode gagal terkoreksi) | 6/6 |
| Baseline repro MF2 equal | 2/2 |
| Baseline repro MF2 existing target | 3/3 |
| Baseline repro MF3 writer | 2/2 |
| Baseline repro MF4 probe | 4/4 |
| Fixed happy (legacy + writer + quiesce) | 13/13 |
| Fixed negative (equal / occupied / wrong source) | 13/13 |
| Fixed failure-after-down + recovery | 8/8 |
| Fixed isolated API (14-step workflow) | 2/2 |
| `check-deployment.sh` | 18 pass / 0 fail / 0 skip |
| **Total** | **71 checks, 0 gagal** |

`go test` NOT RUN pada tugas ini (tidak ada kode Go diubah; suite 14 tes yang sama lolos di CI baseline run 42079731471). `docker compose up` Windows NOT RUN. PowerShell 5.1 NOT RUN.

## 6. Yang masih NOT RUN / batasan

- Eksekusi `docker compose up` + skrip upgrade di host Windows nyata; eksekusi PowerShell 5.1.
- CI GitHub untuk commit baru tugas ini (dicek sesudah push).
- Admin-plane segregation tetap level layanan/kredensial (tidak ada endpoint admin); migrasi 002 tetap hardcode role `umnia_business`/`postgres`; `checkDatabase` tetap hardcode `002_identity` — semua di luar scope allowlist tugas ini dan tidak diubah.

## 7. File yang diubah

| File | Perubahan |
|---|---|
| `deploy/windows/Upgrade-UmniaVolume.ps1` | Ditulis ulang: MF1–MF4 (§3). SHA-256 konten `89835d2c42df4287ca677f0aa18515bb8531fc61664f145fb70e3d2f72683ec8` |
| `deploy/windows/README.md` | Jalur manual memakai identitas sumber legacy; `-ConfirmMaintenance` + batas quiesce; pemulihan terkoreksi; bagian "Yang belum terbukti" diperbarui |
| `deploy/windows/check-deployment.sh` | Bagian 5 baru: 7 checks MF1–MF4 (18/18 lolos) |
| `deploy/windows/tests/README.md` | Dokumentasi harness upgrade |
| `deploy/windows/tests/mock-docker-faithful.ps1` | BARU (disalin dari harness review; path diparameterisasi) |
| `deploy/windows/tests/test-upgrade-scenarios.ps1` | BARU (9 skenario; `Ensure-ApiBinary` membangun binary bila belum ada) |
| `deploy/windows/tests/manage-test-env.sh` | BARU (fixture legacy/target/fixedsrc; perintah baru `stop-target`) |
| `deploy/windows/tests/writer.sh` | BARU (controlled writer MF3) |
| `deploy/windows/tests/baseline-Upgrade-UmniaVolume.ps1` | BARU (baseline 599e40e untuk reproduksi) |
| `.gitignore` | `deploy/windows/tests/bin/` (binary build artifact, dibangun ulang otomatis) |

Diff vs baseline 599e40e (pra-commit): 5 file diubah + 5 file baru; `Upgrade-UmniaVolume.ps1` 193 insertions / 42 deletions. Tidak ada file di luar allowlist yang diubah. Initdb, compose, dan kredensial dari F1-WINDOWS-REVIEW-FIXES tidak disentuh.

## 8. Bukti

`docs/reviews/f1-upgrade-safety-review/` (log runner + output skrip + `check-deployment.log`, dengan `MANIFEST.sha256`): `base-mf1-rerun4.log`, `baseline-mf2-equal-final.log`, `baseline-mf2-existing-target-final.log`, `baseline-mf3-writer-final.log`, `baseline-mf4-probe-final.log`, `fixed-happy-run.log`, `fixed-isolated-run.log`, `fixed-neg-run.log`, `fixed-fail-run.log`, `repo-fixed-neg.log`, `happy-output.log`, `faildown-output.log`, `mf1-output.log`, `neg-src-output.log`, `check-deployment.log`. Log tidak mengandung kredensial (diperiksa dengan grep terhadap password fixture; password hanya ada sebagai literal di file harness, bukan di log). Cluster/API uji sudah dihentikan; PGDATA uji ada di `/var/tmp/umnia-harness/` (ephemeral, hilang bila VM diganti).
