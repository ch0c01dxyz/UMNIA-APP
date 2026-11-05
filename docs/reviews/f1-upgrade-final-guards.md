# F1-UPGRADE-FINAL-GUARDS — laporan perbaikan (2026-11-06)

STATUS: SELESAI (MF1+MF2 terverifikasi pada harness; eksekusi Windows nyata MASIH NOT RUN) | TASK_ID: F1-UPGRADE-FINAL-GUARDS | FILES CHANGED: lihat §7 | TESTS: repro baseline 3+1 checks, fixed baru 30 checks, regresi 49 checks, check-deployment 23/0/0 | RISK / BLOCKERS: `docker compose up` + skrip upgrade di host Windows nyata dan PowerShell 5.1 belum pernah dijalankan | NEXT ACTION: review leader, lalu CI untuk commit baru, lalu uji Windows nyata sebelum klaim F1 gate lolos

## 1. Baseline dan lingkungan (OBSERVED)

- Baseline yang direview: `eb25a8f1a1d3223dcbcca04960a4035f6f07d358` pada `f1-03-identity` (belum merge ke `main`). Skrip baseline yang direproduksi disalin byte-identik ke `deploy/windows/tests/baseline-eb25a8f-Upgrade-UmniaVolume.ps1`, SHA-256 `89835d2c42df4287ca677f0aa18515bb8531fc61664f145fb70e3d2f72683ec8` (sama dengan blob commit).
- Skrip yang diperbaiki: `deploy/windows/Upgrade-UmniaVolume.ps1`, SHA-256 `74dbe651b4836caa4a9d947145693222378e45752d2a20ccfc7f80b1d42c5057` (hash konten file; tidak berubah oleh commit).
- Versi teruji: Go 1.27.3, PostgreSQL 16.15 (image resmi `postgres:16-bookworm`), PowerShell 7.5.4 (parser + harness; PowerShell 5.1 NOT RUN). CI baseline eb25a8f: run 42086847379 SUCCESS, 14 PASS / 0 FAIL / 0 SKIP (dikonfirmasi reviewer; tidak memvalidasi skrip upgrade atau runtime Windows). CI untuk commit baru tugas ini BELUM dicek (menunggu push).
- Perbaikan MF1–MF4 dari F1-UPGRADE-SAFETY-REVIEW TIDAK dibuka ulang dan dipertahankan (identitas sumber, preflight, quiesce, verifikasi read-only) — regresi di §5 membuktikannya.

## 2. Kategori bukti (AAHE)

- **TESTED — ekstraksi AST:** MF2 direproduksi dengan mengekstrak `Get-ServiceContainerOrNull` dari AST PowerShell skrip commit dan memberi test double `Invoke-ComposeCapture` yang melempar error Docker sintetis (metode yang sama dengan reviewer).
- **TESTED — eksekusi skrip via faithful mock:** mock `docker` mengeksekusi binary postgres image asli dan API asli terhadap cluster nyata (fixture legacy + target + fixedsrc). Yang masih di-mock dan harus dilabeli: transport Docker itu sendiri, staging `/tmp` container, service `web` (flag saja), HTTP PowerShell (didelegasikan ke curl, dicatat).
- **TESTED — routing argv:** asersi membaca argv aktual (`-f <file> -p <project>`) dari log mock, bukan dari klaim routing di kode.
- **TESTED — parsing Windows PowerShell:** skrip lolos parser PowerShell (0 error). Eksekusi PowerShell 5.1 NOT RUN.
- **NOT RUN — eksekusi Windows nyata:** `docker run`/`docker compose up` dan skrip upgrade di host Windows tidak pernah dijalankan di mana pun (runc setns diblokir di VM ini). Jangan klaim F1 gate lolos dari laporan ini.

## 3. Per-finding: reproduksi baseline → perbaikan → bukti

### MF1 — operasi sumber memakai file compose baru (TESTED)

**Reproduksi baseline (3/3, `mf1routing-repro.log`):** sumber legacy DIHENTIKAN, skrip baseline eb25a8f dipanggil dengan `-LegacyComposeFile` yang berbeda path (salinan berisi spasi) dari `-ComposeFile`. Baseline tetap men-start sumber dengan file BARU (`up -d postgres-business` memakai `-f <file-baru>`, bukan `-f <file-lama>`) dan readiness `pg_isready` sumber juga memakai file baru. `-LegacyComposeFile` pada baseline hanya mengubah teks pemulihan yang dicetak — tepat seperti temuan reviewer (OBSERVED di sumber: `Invoke-Compose`/`Invoke-ComposeCapture` baris 146–160 dan readiness baris 248 meng-hardcode `$ComposeFile`).

**Perbaikan:** (1) `-LegacyComposeFile` kini `[Parameter(Mandatory=$true)]` — tanpa nilai skrip gagal di binding; (2) helper baru `Get-ComposeFile($Project)` — OldProject → `-LegacyComposeFile`, NewProject → `-ComposeFile`, project lain → throw — dipakai `Invoke-Compose`, `Invoke-ComposeCapture`, dan readiness baru `Test-PostgresReady($Project,$User,$Db)` (menggantikan dua pemanggilan `docker compose -f $ComposeFile` langsung di loop readiness sumber dan target); (3) prasyarat di langkah 0 sebelum mutasi apa pun: `Test-Path` file lama, lalu `docker compose -f <file-lama> config -q` (validasi read-only; config rusak = berhenti sebelum mutasi). Urutan dijaga: cek kesamaan project (murni string, nol panggilan docker) tetap sebelum `config -q`, sehingga penolakan project sama tetap menghasilkan ZERO docker calls; (4) `Get-RecoveryText` tanpa fallback dan tanpa peringatan — file di bawah SELALU file asli — dan path file dalam perintah pemulihan dikutip (`-f "<path>"`), aman bila mengandung spasi. Semua interpolasi `${VAR:-default}` di compose memakai default, jadi `config -q` tidak gagal karena `.env` hilang (OBSERVED di `deploy/windows/compose.yml`).

**Bukti perbaikan:** `fixed-stopped-source` 6/6 (`fixed-stopsrc-run.log`): sumber berhenti di-upgrade penuh (exit 0), start sumber memakai file ASLI, dan `Test-Routing` membuktikan SETIAP operasi sumber memakai file lama dan SETIAP operasi target memakai file baru (0 pelanggaran). `fixed-stopped-source-recovery` 6/6 (`fixed-stopsrc-rec-run.log`): restore digagalkan sengaja; pemulihan memakai file asli yang dikutip dengan spasi (`-f "/tmp/umnia legacy/stopsrcrec/compose.yml" -p umnia-legacy up -d`); eksekusi jalur pemulihan via mock mengembalikan API lama 200 + fixture utuh. `fixed-happy` 14/14 dan `fixed-negative` 14/14 kini juga mengasersi routing (file lama = salinan ber-spasi, file baru = repo).

### MF2 — kegagalan inspeksi ditelan menjadi "penulis tidak ada" (TESTED)

**Reproduksi baseline (`mf2-ast-repro.log`):** fungsi `Get-ServiceContainerOrNull` diekstrak dari AST skrip eb25a8f; test double `Invoke-ComposeCapture` melempar "synthetic Docker error: Cannot connect to the Docker daemon"; fungsi mengembalikan `''` TANPA exception — reproduksi persis temuan reviewer. (OBSERVED di sumber: `try { ... } catch { return '' }` baris 167–171.)

**Perbaikan:** (1) `Get-ServiceContainerOrNull` tanpa `try/catch` — exception dari `Invoke-ComposeCapture` dibiarkan naik; `''` hanya dari `ps -q` yang SUKSES dengan output kosong; (2) helper baru `Test-ServiceDefined($Project,$Service)` — `docker compose config --services`, kegagalannya dibiarkan naik — dipakai langkah 0c untuk membedakan penulis yang memang tidak terdefinisi di config lama (mis. `web`) dari kegagalan query; daftar penulis yang dihentikan/diperiksa kini hanya service terdefinisi (`$writerSvcs`), termasuk loop quiesce. Pemeriksaan ketiadaan lain yang disentuh: `Test-VolumeExists` (`docker volume inspect` → `$false` bila gagal) DIREVIEW dan TIDAK diubah — kegagalan daemon di sana tetap fail-closed dalam urutan skrip (target: `ps -q` melempar lebih dulu; sumber: throw "Sumber tidak ditemukan"), hanya pesan error-nya yang kurang spesifik; di luar scope tugas ini untuk mengubahnya.

**Bukti perbaikan:** cek AST pada skrip yang diperbaiki (`mf2-ast-fixed.log`): error sintetis yang sama kini PROPAGATES ("exception propagated"). `fixed-inspection-failure` 14/14 (`fixed-insp-run.log`): (a) error query container sumber → abort, no dump, no `down`, ZERO mutating calls; (b) error query status penulis → abort, no dump, no `down`, ZERO mutating calls; (c) config lama malformed (`config -q` gagal) → abort sebelum mutasi apa pun, error menyebut file lama; (d) file lama tidak ada → abort dengan ZERO docker calls. `fixed-missing-writer` 4/4 (`fixed-noweb-run.log`): config lama tanpa `web` tetap upgrade penuh (exit 0), `ps -q web` TIDAK PERNAH dipanggil untuk project lama, fixture utuh, API baru 200 — hasil kosong yang sah tetap diizinkan.

## 4. Bug harness yang ditemukan dan diperbaiki selama tugas ini (TESTED)

Semua di bawah ini adalah bug harness/pengujian, BUKAN cacat produk:

1. Skenario repro MF1-routing awalnya memanggil skrip baseline 599e40e — skrip itu TIDAK PUNYA parameter `-LegacyComposeFile` (ditambahkan di eb25a8f), sehingga splatting gagal di binding. Perbaikan: salinan baseline eb25a8f dibuat (`baseline-eb25a8f-Upgrade-UmniaVolume.ps1`, hash terverifikasi sama dengan blob commit) dan dipakai skenario repro.
2. Akuntansi writer di `fixed-happy` (dan laten di `baseline-mf3-writer`): `writer.sh` mencatat timestamp SESUDAH curl kembali, jadi write yang commit sebelum `n0Ms` bisa membawa timestamp log sesudah `n0Ms` dan dihitung dua kali (sekali di `n0`, sekali di `wPre`) — terbukti: target=11 BENAR (fixture + 10 writer), ekspektasi skenario 12. Perbaikan: `n0`/`s0`/`n0Ms` dibaca SEBELUM writer di-start (n0 = fixture saja), dan cek subjek mengecualikan akun writer (`writer-happy@contoh.id`, satu akun registrasi fixture yang sah, bukan akun sintetis skrip). Sesudah perbaikan `fixed-happy` 14/14 dan `baseline-mf3-writer` tetap mereproduksi kehilangan write (2/2).

## 5. Hasil tes — ringkasan

| Suite | Hasil |
|---|---|
| MF1 repro baseline eb25a8f (stopped source, routing) | 3/3 |
| MF2 repro baseline eb25a8f (AST, error ditelan) | 1/1 (direproduksi) |
| MF2 AST check skrip diperbaiki (error propagates) | 1/1 |
| fixed-stopped-source | 6/6 |
| fixed-stopped-source-recovery | 6/6 |
| fixed-inspection-failure (4 sub-kasus) | 14/14 |
| fixed-missing-writer | 4/4 |
| fixed-happy (incl. routing) | 14/14 |
| fixed-isolated-api (14-step workflow) | 2/2 |
| fixed-negative (incl. routing) | 14/14 |
| fixed-failure-after-down (incl. routing + quoted path) | 10/10 |
| baseline-mf1 / mf2-equal / mf2-target / mf3 / mf4 (repro lama) | 6/6, 2/2, 3/3, 2/2, 4/4 |
| `check-deployment.sh` | 23 pass / 0 fail / 0 skip |
| **Total** | **110 checks, 0 gagal** |

`go test` NOT RUN pada tugas ini (tidak ada kode Go diubah). `docker compose up` Windows NOT RUN. PowerShell 5.1 NOT RUN.

## 6. Yang masih NOT RUN / batasan

- Eksekusi `docker compose up` + skrip upgrade di host Windows nyata; eksekusi PowerShell 5.1.
- CI GitHub untuk commit baru tugas ini (dicek sesudah push).
- `Test-VolumeExists` masih menyamakan kegagalan daemon dengan volume tidak ada (fail-closed dalam urutan skrip, pesan kurang spesifik) — dicatat di §3 MF2, tidak diubah.
- Admin-plane segregation tetap level layanan/kredensial; migrasi 002 tetap hardcode role; `checkDatabase` tetap hardcode `002_identity` — di luar scope dan tidak diubah.

## 7. File yang diubah

| File | Perubahan |
|---|---|
| `deploy/windows/Upgrade-UmniaVolume.ps1` | MF1+MF2 (§3). SHA-256 konten `74dbe651b4836caa4a9d947145693222378e45752d2a20ccfc7f80b1d42c5057` |
| `deploy/windows/README.md` | `-LegacyComposeFile` wajib + routing semua operasi sumber; prasyarat `config -q`; inspeksi fail-closed; pemulihan dikutip (spasi); rujukan laporan baru |
| `deploy/windows/check-deployment.sh` | Bagian 6 baru: 5 checks final-guards (23/23 lolos) |
| `deploy/windows/tests/mock-docker-faithful.ps1` | Verb `config` (`-q`/`--services`), injeksi `FailPs`/`FailConfig`, `ps -q` error untuk service tak terdefinisi, field `Services` per-project |
| `deploy/windows/tests/test-upgrade-scenarios.ps1` | Skenario baru: `baseline-mf1-routing`, `fixed-stopped-source`, `fixed-stopped-source-recovery`, `fixed-inspection-failure`, `fixed-missing-writer`; helper `New-LegacyComposeCopy` (path ber-spasi) + `Test-Routing`; call site fixed-* kini memakai `LegacyComposeFile` + asersi routing; perbaikan akuntansi writer (§4) |
| `deploy/windows/tests/manage-test-env.sh` | Perintah baru `stop-legacy` (hentikan sumber tanpa hapus volume) |
| `deploy/windows/tests/baseline-eb25a8f-Upgrade-UmniaVolume.ps1` | BARU (baseline eb25a8f untuk reproduksi) |
| `deploy/windows/tests/README.md` | (tidak diubah pada tugas ini — dokumentasi harness upgrade sudah ada dari tugas sebelumnya; skenario baru tercantum di header runner) |

Diff vs baseline eb25a8f (pra-commit): 6 file diubah + 1 file baru (+ laporan dan bukti); `Upgrade-UmniaVolume.ps1` +119/−~60 baris. Tidak ada file di luar allowlist yang diubah. Initdb, compose, dan kredensial tidak disentuh.

## 8. Bukti

`docs/reviews/f1-upgrade-final-guards/` (log runner + output skrip + `check-deployment.log`, dengan `MANIFEST.sha256` 26 entri): `mf1routing-repro.log`, `mf2-ast-repro.log`, `mf2-ast-fixed.log`, `fixed-stopsrc-run.log`, `fixed-stopsrc-rec-run.log`, `fixed-insp-run.log`, `fixed-noweb-run.log`, `fixed-happy-run4.log`, `fixed-isolated-run3.log`, `fixed-neg-run2.log`, `fixed-fail-run2.log`, `base-mf1-rerun.log`, `base-mf3-rerun.log`, `baseline-mf2-equal-rerun2.log`, `baseline-mf2-existing-target-rerun2.log`, `baseline-mf4-probe-rerun2.log`, `stopsrc-output.log`, `stopsrcrec-output.log`, `insp-a-output.log`, `insp-b-output.log`, `insp-c-output.log`, `insp-d-output.log`, `noweb-output.log`, `happy-output.log`, `faildown-output.log`, `check-deployment.log`. Log tidak mengandung kredensial (diperiksa dengan grep terhadap password fixture). Cluster/API uji sudah dihentikan; PGDATA uji ada di `/var/tmp/umnia-harness/` (ephemeral, hilang bila VM diganti).
