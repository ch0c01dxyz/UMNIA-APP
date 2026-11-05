# F1-WINDOWS-REVIEW-FIXES — laporan pelaksana

STATUS: SELESAI (perubahan di working tree, **belum commit/push — tidak ada otorisasi commit/push pada tugas ini**)
TASK_ID: F1-WINDOWS-REVIEW-FIXES
Baseline: `d38d48f28b712e5d8b0f7de16d8f575c3ac1a88a` (branch `f1-03-identity`, terkonfirmasi remote)
Hasil: working tree di `~/workspace/UMNIA-APP`, status `M` pada 3 berkas + 3 berkas baru (+ `docs/reviews/`)

Label klaim mengikuti docs/09-advanced-anti-hallucination: OBSERVED / OFFICIAL / TESTED / INFERRED / PROPOSED / UNKNOWN.
Bagian "Bukti historis" memisahkan hasil review run 2 (sebelum tugas ini) dari bukti baru tugas ini.

## Ringkasan perubahan

| Berkas | Perubahan |
| --- | --- |
| `deploy/windows/initdb/01-runtime-roles.sh` | Ditulis ulang: semua psql eksplisit `--username/--dbname`, cek eksistensi role fail-closed, hanya membuat satu role runtime + role `postgres`, aman di mode executable maupun sourced. Mode 100644 → 100755. |
| `deploy/windows/compose.yml` | Tiap layanan postgres hanya menerima secret-nya sendiri; entri `UMNIA_*_DB_PASSWORD` yang disilangkan dihapus dari keduanya. |
| `deploy/windows/Upgrade-UmniaVolume.ps1` | **Baru.** Upgrade Windows yang executable di PowerShell: `pg_dump -Fc` + `docker cp` + `pg_restore -Fc`, validasi sebelum `down`, project `-p` berbeda, tanpa `docker volume rename`, tanpa `<`. |
| `deploy/windows/README.md` | Bagian upgrade ditulis ulang ke script `.ps1` + manual setara; status verifikasi diperbarui secara jujur. |
| `deploy/windows/check-deployment.sh` | **Baru.** Regression check statis + opsional `--container-test`. |
| `deploy/windows/tests/run-entrypoint.sh` | **Baru.** Harness untuk menjalankan entrypoint image resmi secara native (lihat "Lingkungan uji"). |
| `deploy/windows/tests/README.md` | **Baru.** Dokumentasi harness. |
| `docs/reviews/f1-foundation-remediation-review/` | **Baru.** Salinan bundle review run 2 (menjawab keluhan reviewer: `@REVIEW.md` / bundle 88 berkas tidak ditemukan). `sha256sum -c MANIFEST.sha256` semua OK di lokasi baru. |
| `docs/reviews/f1-windows-review-fixes/` | **Baru.** Bundle tugas ini: laporan ini + `scripts/` + `evidence/`. |

## Lingkungan uji (TESTED)

- Go 1.27.3 (`~/workspace/tools/go`), PostgreSQL 16.15 (native + image), PowerShell 7.5.4 (`~/workspace/tools/pwsh/pwsh`).
- Docker 29.1.3 via apt (`docker.io`); daemon hanya jalan sebagai `dockerd --iptables=false --bridge=none --storage-driver=vfs` (default gagal: iptables "Permission denied", overlayfs mount gagal). Compose plugin v2.40.1 di `~/.docker/cli-plugins/docker-compose`.
- **`docker run` TIDAK MUNGKIN di VM ini** (TESTED): runc gagal `join container mntns: setns: operation not permitted`, juga dengan `--privileged`; chown sebagai root juga ditolak. Karena itu tidak ada `docker compose up` yang dijalankan — lihat "Belum dijalankan".
- Image `postgres:16-bookworm` diperoleh tanpa `dockerd pull` (pull selalu gagal "TLS handshake timeout" ke registry-1.docker.io dan mirror.gcr.io meski proxy ter-set; curl langsung ke registry juga hang). Diambil lewat mirror `https://dockerhub.timeweb.cloud` secara anonim (dengan token Docker Hub justru hang). **Digest manifest amd64 `sha256:5983db9a9c6ad225a48a46cfe2e03e6680b1826662befd0465eb05b34cb1c64e` terverifikasi identik dengan digest resmi dari API tag hub.docker.com** (OFFICIAL); config digest `sha256:cadebe7c08430c896edac883fa92f8de2c36e330805e770fbee8a7eae13f144f`; PG_VERSION=16.15-1.pgdg12+2; 14 layer, 155.129.186 byte. Script: `~/workspace/tools/docker-images/postgres16/fetch-image.sh` + `load-image.sh`; `image.tar` tersimpan di sana; `docker load` berhasil di bawah vfs.
- **Entrypoint (OFFICIAL/TESTED):** `/usr/local/bin/docker-entrypoint.sh` dari image **byte-identik** dengan docker-library/postgres master DAN sumber `16/bookworm` (masing-masing 389 baris, diff bersih). Fakta kunci: berkas `.sh` di initdb **dijalankan** bila executable, **di-source** bila bukan; entrypoint hanya mengekspor `PGPASSWORD` untuk init files (tanpa PGUSER/PGDATABASE); helper `docker_process_sql`-nya memakai `psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --no-password --no-psqlrc` dengan `PGHOST=` dikosongkan; berjalan sebagai root lalu `exec gosu postgres`.
- **Harness** (`deploy/windows/tests/run-entrypoint.sh`): menjalankan entrypoint image + biner postgres 16.15 image secara native sebagai user host `postgres` (uid 103) lewat `sudo -u postgres env ...`, karena `docker run` terblokir dan root chown ditolak. Rootfs di `/var/tmp/umnia-harness/rootfs` (salinan image); biner image butuh `LD_LIBRARY_PATH=<rootfs>/lib/x86_64-linux-gnu:<rootfs>/usr/lib/x86_64-linux-gnu` (sudo menghapus LD_LIBRARY_PATH, jadi dilewatkan lewat `env`). `/docker-entrypoint-initdb.d` adalah symlink ke `/var/tmp/umnia-harness/initdb`. Quirk: `/home/hatch` adalah `drwxrws---` (postgres tidak bisa traversal → harness di /var/tmp); /var/tmp butuh `chmod 777` pada dir harness agar postgres bisa mkdir; **chmod in-place tidak bisa menghapus other-x di tmpfs ini** → harness menyalin script initdb dengan `install -m 644|755` (mode saat pembuatan berlaku). Harness me-re-sync salinan initdb dari repo tiap run + hash-check. PGUSER/PGDATABASE sengaja tidak di-set. Parameter: `WINTEST_BASE`, `WINTEST_ROOTFS`; REPO diturunkan dari lokasi script.
- pwsh 7.5.4: tumpukan HTTP-nya **TERBLOKIR di VM ini** (runtime menolak TCP langsung; error menyebut "Other TCP connections is turned off… other_tcp"); curl bekerja. Jadi langkah HTTP `.ps1` tidak bisa jalan native di sini (lihat mock di bawah).
- Password fixture hanya sintetis (`bootstrap-only`, `runtime-only`, `admin-bootstrap-only`, `admin-runtime-only`, `legacy-bootstrap-only`, CI fixture `ci-bootstrap-only`/`ci-runtime-only`). `.env` lokal tidak pernah di-commit atau dikutip.

## Must fix 1 — identitas koneksi initdb implisit (TESTED)

### Reproduksi pada baseline d38d48f

Script baseline via harness (cluster baru, `POSTGRES_USER=umnia_bootstrap`, tanpa PGUSER/PGDATABASE): entrypoint **men-source** script; psql telanjang gagal dua kali `FATAL: role "postgres" does not exist`; entrypoint keluar lebih awal → volume Windows baru akan crash-loop. Kegagalan query eksistensi di dalam `$(...)` pada kondisi `if` tersembunyi sebagai hasil kosong (script lanjut ke CREATE, lalu gagal). Log: `evidence/entrypoint-baseline-business.log`, `evidence/evidence-baseline-initdb-failure.log`.

### Perbaikan

Semua psql lewat `psql_bootstrap()`: `psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" --dbname "$POSTGRES_DB" --no-password --no-psqlrc`. `role_exists()` memberi query lewat **heredoc di stdin** (fakta psql yang TERUJI: interpolasi variabel `:'role'` TIDAK bekerja pada argumen `-c`, hanya input stdin/file — implementasi pertama memakai `-tAc` dan gagal syntax error di ":") dan melakukan `exit 1` dengan pesan bila koneksi/query gagal (tidak pernah membaca kegagalan sebagai "role tidak ada"). Guard fail-closed `: "${VAR:?}"` untuk POSTGRES_USER/POSTGRES_DB/POSTGRES_PASSWORD/UMNIA_RUNTIME_ROLE/UMNIA_RUNTIME_DB_PASSWORD. Hanya membuat `$UMNIA_RUNTIME_ROLE` (via `CREATE ROLE:"role"... PASSWORD:'pw'`) + role superuser `postgres`. Aman di kedua mode entrypoint: `set -euo pipefail` hanya bila dieksekusi (`[[ "${BASH_SOURCE[0]}" == "${0}" ]]`); error fatal memakai `exit` (menggagalkan container) juga bila di-source.

### Bukti perbaikan

- Cluster **bisnis** baru via harness, **mode executed**: READY; role — `umnia_business` f/f + LOGIN, `postgres` superuser, `umnia_admin` TIDAK ADA; migrate 001+002 exit 0; `schema_migrations` = 001_init + 002_identity; EXECUTE grants 6/6 × 2; workflow API (`scripts/api-workflow.sh`, dari review run 2): **14/14 langkah lolos** (readyz 200, register/login 201/200, lintas-tenant 403 sebelum undangan, invite 201, accept 200, sesudah undangan 200, kasir audit 403, kasir members 403, owner members 200). Log: `evidence/entrypoint-fixed-business-exec.log`, `evidence/evidence-api-workflow.log`.
- Cluster **admin** baru via harness, **mode sourced** (`INITDB_SOURCED=1`): log entrypoint menyebut "sourcing"; `umnia_admin` f/f + login, `umnia_business` TIDAK ADA, `postgres` superuser. PASS. Log: `evidence/entrypoint-fixed-admin-src.log`.
- **Fail-closed:** UMNIA_RUNTIME_ROLE hilang → entrypoint keluar lebih awal, log "UMNIA_RUNTIME_ROLE is required"; UMNIA_RUNTIME_DB_PASSWORD hilang → pola sama. Keduanya PASS. Log: `evidence/entrypoint-failclosed-norole.log`, `evidence/entrypoint-failclosed-nopw.log`.

## Must fix 2 — instruksi upgrade tidak bisa dijalankan (TESTED)

### Reproduksi pada baseline

- `docker volume --help` (Docker 29.1.3) hanya mencantumkan create/inspect/ls/prune/rm — **tidak ada `rename`** (0 hasil untuk "rename").
- Parser PowerShell (pwsh 7.5.4) pada blok README lama: "The '<' operator is reserved for future use" ×3 (redirect `psql ... < umnia-backup.sql` DAN kedua placeholder `<nama-volume-lama>`). Reviewer mengamati hal sama di Windows PowerShell 5.1 (perbedaan versi dicatat).

### Perbaikan — `deploy/windows/Upgrade-UmniaVolume.ps1` (baru, kompatibel PS 5.1: tanpa `??`/ternary/`utf8NoBOM`)

Langkah: 0 prasyarat (+ peringatan `.env`, tidak pernah menulis `.env`); 0b jalankan postgres lama saja + cek password `pg_isready`; 1 `pg_dump -Fc --no-owner` di dalam container lama ke /tmp; 2 `docker cp` keluar (binary-safe, tanpa `<`/`>`); 3 validasi `pg_restore -l` (harus mencantumkan `workspaces`) + berkas non-kosong **sebelum** menghentikan stack lama; 4 `down` TANPA `-v`; 5 project baru `up -d postgres-business`; 6 `docker cp` masuk + `pg_restore --no-owner --exit-on-error`; 7 `up --build -d` penuh; 8a verifikasi DB (role f/f, `002_identity` tercatat, EXECUTE grants); 8b `/readyz` 200 + probe isolasi API (pengguna baru GET workspace orang lain harus 403). Project `-p` berbeda (`umnia-legacy`/`umnia`, parameter) ⇒ volume berbeda; port host sama ⇒ urutan sekuensial ditegakkan; nama backup bertimestamp, menolak menimpa backup yang ada; setiap panggilan docker memeriksa `$LASTEXITCODE` eksplisit; bila gagal, mencetak jalur pemulihan (volume lama utuh: `docker compose -f … -p umnia-legacy up -d`) dan exit 1.

**Bug yang tertangkap saat pengujian:** asersi grants semula menghitung 12 untuk grantee (postgres, umnia_business) di seluruh schema `app` = sebenarnya **13** (6 helper × 2 = 12 DITAMBAH `reject_audit_change|postgres` sebagai owner); diperbaiki menjadi hanya 6 helper bernama × 2 grantee = 12 (terverifikasi mengembalikan 12 pada DB yang benar). Parse bersih di pwsh 7.5.4 (0 error).

### Bukti perbaikan — dua lapis

**Lapis A — mekanika dump/restore nyata dengan biner image (TESTED).** Fixture legacy dibangun setia (entrypoint dengan POSTGRES_USER=umnia_business, dir initdb KOSONG, migrasi dijalankan TANPA `-1` → exit 3 di baris 350 `role "postgres" does not exist`, namun `schema_migrations` mencatat 001 DAN 002 = setengah terapan; umnia_business t/t; fixture workspace `Toko Lama` id `22222222-2222-2222-2222-222222222222` + membership `active` untuk subject `11111111-1111-1111-1111-111111111111`). Lalu: `pg_dump -Fc --no-owner` exit 0 (31.651 byte); `pg_restore -l` mencantumkan workspaces + workspace_memberships; server legacy dihentikan (volume dipertahankan); cluster baru yang diperbaiki (55437) → `pg_restore --no-owner --exit-on-error` exit 0 → re-migrate `-1` exit 0 → fixture utuh, umnia_business f/f, grants benar; API di 18081: readyz 200, pengguna **baru** GET workspace lama = **403**. **Jalur pemulihan terbukti:** cluster legacy di-restart sesudahnya — fixture utuh, role masih superuser t, migrasi masih tercatat — lalu dihentikan. Log: `evidence/entrypoint-legacy-old.log`, `evidence/entrypoint-upgraded-new.log`, `evidence/legacy-restart.log`.

**Lapis B — mock logic test `.ps1` (TESTED, `scripts/test-upgrade-script-mock.ps1`, log `evidence/evidence-mock-ps1.log`).** Mock `docker` mencatat argv + mengembalikan sukses kalengan (`ps -q` → CID palsu `deadbeef0001`/`cafef00d0002`, `pg_restore -l` → baris TABLE termasuk workspaces, cek psql → 'false/false'/'1'/'12'); mock `Invoke-WebRequest`/`Invoke-RestMethod` mendelegasikan ke `/usr/bin/curl` (HTTP native pwsh terblokir di VM) dan probe API **nyata** di `-ApiBase http://127.0.0.1:18080`. Hasil: **MOCK LOGIC TEST OK, 11 checks PASS** — urutan (validasi sebelum down), `down` tanpa `-v`, project berbeda, docker cp dua arah, tanpa `volume rename`, backup dibuat; probe API nyata: register 201, login 200, workspace 201, GET sebagai non-member **403**; 19 panggilan docker tercatat dalam urutan yang benar. **Tiga bug harness mock (bukan bug produk) ditemukan dan diperbaiki selama pengujian:** baris `$tmp = Join-Path` telanjang yang membuat pwsh meminta input interaktif; `curl` adalah alias Invoke-WebRequest di pwsh (mock harus memanggil `/usr/bin/curl` eksplisit); parameter mock `UseBasicParsing` harus `[switch]`; dan `-X` harus memakai `$Method.ToUpper()` karena mux Go membedakan huruf besar/kecil (`Post` → 404, `POST` → 201). Uji tabrakan no-clobber TIDAK dijalankan (hanya terverifikasi dari kode).

README bagian upgrade ditulis ulang: jalur utama script `.ps1`; manual setara memakai `docker cp` + `pg_restore -Fc` + project `-p` berbeda; menyatakan `docker volume rename` tidak ada; referensi artefak diperbarui ke `docs/reviews/f1-foundation-remediation-review/REVIEW.md`; "Yang belum terbukti" diperbarui (compose up di Windows masih UNVERIFIED; yang sudah terverifikasi dicantumkan tepat).

## Must fix 3 — kredensial/role bisnis dan admin disilangkan (TESTED)

### Perbaikan

Compose: business = `UMNIA_RUNTIME_ROLE: umnia_business` + `UMNIA_RUNTIME_DB_PASSWORD: ${UMNIA_BUSINESS_DB_PASSWORD:-umnia-dev-only}`; admin = setara `umnia_admin`. Entri `UMNIA_*_DB_PASSWORD` yang disilangkan dihapus dari keduanya. Initdb membuat hanya role yang dimaksud tiap cluster. Komentar header compose diperbarui (segregasi + status verifikasi: initdb/migrate terverifikasi terhadap entrypoint/biner image resmi secara native; `compose up` di Windows masih UNVERIFIED). Layanan migrate + business-api tidak berubah.

### Bukti perbaikan

- `docker compose config --format json` + assert Python membuktikan segregasi (bagian dari `check-deployment.sh`, lihat bawah).
- Cluster bisnis (55432): `umnia_admin` TIDAK ADA; cluster admin (55433): `umnia_business` TIDAK ADA; hanya 3 role login di admin (postgres, umnia_admin, umnia_admin_bootstrap).
- Login silang gagal: `umnia_business` ke server admin → `FATAL: role "umnia_business" does not exist`; `umnia_admin` ke server bisnis → `FATAL: role "umnia_admin" does not exist`. Kedua cluster adalah server terpisah (PGDATA dan port berbeda) — tidak ada akses lintas-DB secara konstruksi.
- ACL schema dalam cluster bisnis: `app` dimiliki `umnia_bootstrap`, ACL `{umnia_bootstrap=UC, umnia_business=U}` (runtime hanya USAGE, tanpa CREATE); `public` dimiliki `pg_database_owner`, PUBLIC hanya USAGE. Cluster admin tidak memiliki schema `app` sama sekali (tidak ada migrasi di sana).
- **Batasan yang tersisa (OBSERVED/INFERRED, jangan diklaim lebih):** kedua container berbagi network default Compose tanpa network policy — container business-api yang terkompromi bisa TCP-connect ke port postgres-admin (tetapi tidak memiliki kredensial admin: password admin tidak ada di env business-api; secret admin hanya ada di container postgres-admin + `.env` di host). Admin plane masih tanpa endpoint — segregasi adalah level layanan/kredensial, **BUKAN** isolasi penuh.

## Regression check — `deploy/windows/check-deployment.sh` (TESTED)

**12 pass / 0 fail / 0 skip** (dengan pwsh di PATH + `--container-test`). Memeriksa: bash -n, grep `--username/--dbname` eksplisit, tanpa `psql -tAc` telanjang, grep fail-closed, grep satu-role, `docker compose config` + assert JSON segregasi, tanpa perintah `docker volume rename` (grep berjangkar — kalimat prosa di README diperbolehkan; versi pertama salah gagal pada kalimat prosa dan diperbaiki), tanpa redirect `<`, grep docker cp + `pg_restore -l` + `$LASTEXITCODE`, cek parse pwsh (path dilewatkan lewat env var `UMWIN_PS1` — bentuk `$args[0]` gagal), opsional `--container-test` via `tests/run-entrypoint.sh`.

## Tes — ringkasan

| Tes | Hasil |
| --- | --- |
| Baseline initdb d38d48f via harness | GAGAL seperti dilaporkan reviewer (FATAL role "postgres" does not exist, entrypoint keluar) — reproduksi berhasil |
| Initdb baru, cluster bisnis, mode executed | PASS (role f/f, migrate 001+002, grants 6/6×2) |
| Initdb baru, cluster admin, mode sourced | PASS (umnia_admin f/f, umnia_business tidak ada) |
| Fail-closed (role hilang / password hilang) | PASS ×2 |
| Workflow API pada cluster bisnis baru | **14/14 PASS** |
| Mekanika upgrade dump/restore (biner image) | PASS (dump 31.651 byte, restore exit 0, fixture utuh, role f/f, pengguna baru GET workspace lama 403, pemulihan legacy terbukti) |
| Mock logic test `Upgrade-UmniaVolume.ps1` | **11 checks PASS** (urutan, docker cp, validasi sebelum down, down tanpa -v, probe API nyata 201/200/201/403) |
| `check-deployment.sh` | **12 pass / 0 fail / 0 skip** |
| `go test ./...` | **NOT RUN pada tugas ini** — tidak ada kode Go yang diubah; suite 14 tes yang sama lolos di review run 2 pada kode API yang sama (bukti historis) |
| `docker run` / `docker compose up` | **NOT RUN** — mustahil di VM ini (runc setns); lihat bawah |
| Eksekusi PowerShell 5.1 | **NOT RUN** — hanya parse + logika di pwsh 7.5.4 |
| HTTP `.ps1` native di pwsh | **NOT RUN** — tumpukan HTTP pwsh terblokir di VM; diuji lewat delegasi curl pada mock |

## Bukti historis (review run 2, sebelum tugas ini — dipisahkan dari bukti baru)

Bundle `docs/reviews/f1-foundation-remediation-review/` (`REVIEW.md` + `f1-remediation.patch` SHA-256 `a513734ac886e885cd3b1e7f8874594a983246852de2b0a774dec7e08336c742`, 10 berkas, 641 baris; `MANIFEST.sha256` 88 berkas, semua terverifikasi di lokasi baru). Commit `d38d48f` + push 2026-11-05 22:26 WIB atas izin eksplisit user; CI run #5 (https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/42054919498) LULUS 1m 6s. Rerun lokal: go test **14 pass / 0 fail / 0 skip**. Tiga bug diperbaiki saat itu: (1) `local` gabungan + indirect expansion di initdb; (2) **anomali EXECUTE terbukti** — GRANT sebelum ALTER OWNER TO dibuang alih kepemilikan (eksperimen M1), diperbaiki dengan memindah OWNER TO sebelum GRANT; (3) demosi bootstrap superuser in-place MUSTAHIL di PG16 → upgrade = dump/restore ke volume baru. Batasan yang dibawa: migrasi men-hardcode role `umnia_business`/`postgres` (parameterisasi PROPOSED, belum diimplementasikan); `checkDatabase` men-hardcode versi `002_identity`; segregasi admin plane = pemisahan layanan/kredensial saja (tanpa endpoint admin yang membuktikannya); fungsi trigger `app.reject_audit_change()` tidak termasuk daftar hardening EXECUTE 6 fungsi dan masih PUBLIC execute (OBSERVED, tidak berubah); pada volume legacy, schema `app` tetap dimiliki `umnia_business` (PROPOSED follow-up: `ALTER SCHEMA app OWNER TO postgres`).

## Penyimpangan dari aturan repo (dinyatakan eksplisit)

`AGENTS.md` repo aturan 6: "Windows must develop with Docker Compose; Linux must run natively… No Docker Linux runtime." Pada tugas ini Docker dipakai **hanya sebagai harness uji untuk artefak deployment Windows** (image resmi + entrypoint + biner postgres dijalankan native lewat harness), karena prompt tugas ini secara eksplisit menuntut bukti container untuk jalur Windows. Runtime aplikasi Linux tetap native. Otorisasi tugas ini mengalahkan berkas repo untuk cakupan ini.

## Batasan dan risiko yang tersisa

- **`docker compose up` di host Windows nyata masih UNVERIFIED di mana pun** (tidak di VM ini, tidak di host Windows). Yang terverifikasi: entrypoint image resmi byte-identik dengan upstream, initdb baru bekerja di bawah entrypoint itu (native), migrasi + API bekerja pada cluster hasil initdb itu, dan mekanika dump/restore + logika `.ps1` teruji.
- PowerShell 5.1: hanya kompatibilitas sintaks yang dijaga (tanpa `??`/ternary/`utf8NoBOM`); eksekusi 5.1 belum dijalankan.
- Uji tabrakan no-clobber `.ps1` hanya terverifikasi dari kode, belum dieksekusi.
- Segregasi admin plane tetap level layanan/kredensial (lihat Must fix 3); tidak ada network policy antar container; tidak ada endpoint admin.
- Batasan lama dari review run 2 tetap berlaku (hardcode nama role, `checkDatabase` hardcode `002_identity`, `reject_audit_change` PUBLIC execute, schema `app` legacy dimiliki `umnia_business`).

## Artefak

- Laporan ini: `docs/reviews/f1-windows-review-fixes.md`
- Script: `docs/reviews/f1-windows-review-fixes/scripts/` (`run-entrypoint.sh`, `assert-roles.sh`, `api-workflow.sh`, `test-upgrade-script-mock.ps1`)
- Bukti: `docs/reviews/f1-windows-review-fixes/evidence/` (11 log: baseline initdb, bisnis executed, admin sourced, fail-closed ×2, legacy, upgraded, legacy-restart, workflow API, mock `.ps1`)
- Bundle review sebelumnya: `docs/reviews/f1-foundation-remediation-review/` (`REVIEW.md`, `f1-remediation.patch`, `MANIFEST.sha256`, 88 berkas)
- Working tree: perubahan di atas belum commit/push (tidak ada otorisasi). Server uji (port 55432/55433/55437, API 18080/18081) sudah dihentikan; PGDATA uji tetap di `/var/tmp/umnia-harness/` (ephemeral, hilang bila VM diganti).

## NEXT ACTION

Leader/user meninjau perubahan working tree ini; commit/push hanya atas izin eksplisit, lalu verifikasi CI GitHub nyata. Setelah itu, verifikasi `docker compose up` + jalur upgrade `.ps1` di host Windows nyata sebelum mengklaim gate deployment Windows selesai. F1-04 (module registry) belum dimulai.
