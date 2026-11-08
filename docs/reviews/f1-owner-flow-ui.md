# F1-OWNER-FLOW-UI — laporan implementasi + koreksi R1

TASK_ID: F1-OWNER-FLOW-UI / F1-OWNER-FLOW-UI-R1. Baseline awal
`e3f48c6a512d7b3cd1036c3767dc54eb0a3cb8d3`, branch `f1-03-identity`.
Implementasi awal di-commit sebagai `cde8680` (2026-11-08 21:02 WIB, otorisasi
eksplisit user; push `e3f48c6..cde8680`, remote HEAD terverifikasi). Koreksi R1
di bawah ini UNCOMMITTED (tiket R1 tidak memberi otorisasi commit/push).
Gate F1 tetap NOT COMPLETE.

## Scope yang dibangun

Lima layar alur pemilik, semuanya lewat proxy same-origin `/api/...` ke business
API yang sama (tanpa perubahan backend):

- `/login` — masuk/daftar, label Indonesia; daftar 201 lalu wajib masuk eksplisit
  (tanpa klaim verifikasi email); token hanya di memori browser (plugin
  `.client`, tanpa localStorage/cookie/payload SSR).
- `/workspaces/new` — POST workspaces; 201 navigasi ke `/workspaces/{id}/members`.
- `/workspaces/{id}/members` — GET workspace + anggota; form undang manager/kasir
  saja (tanpa injeksi owner); kode undangan tampil sekali di input `type=password`,
  tautan salin hanya berisi path accept + UUID, kode disalin terpisah; UI menyebut
  undangan DIBUAT/manual, tidak pernah "email terkirim".
- `/workspaces/{id}/modules` — katalog GET + state rental.basic GET; hanya aktivasi
  rental.basic; 403 tanpa entitlement memakai kalimat tiket; sudah-aktif = no-op
  (state dibaca ulang, tanpa tulis tersembunyi).
- `/invites/accept?workspace_id=UUID` — paste kode manual (type=password); POST
  accept hanya saat submit; sukses ke members, 403 kasir ditangani jujur dengan
  fallback ke modules.

Proxy `server/api/[...path].ts`: allowlist method+path saja (healthz, register/login,
workspace create/GET satu, members GET, invite create/accept, katalog modul GET,
state Rental GET, activate POST); UUID + segmen modul divalidasi; query apa pun
ditolak 400; path tak dikenal 404 lokal tanpa upstream; body mutasi dibatasi 16 KiB;
hanya Content-Type/Authorization/Idempotency-Key diteruskan; timeout upstream 10 dtk;
fault backend 502/disampaikan sebagai pesan generik tanpa URL/SQL.

Keputusan artefak terlingkup: memakai stylesheet bersama `assets/main.css` + shell
`app.vue` yang sudah ada; TIDAK membuat `packages/ui`, admin scaffolding, atau paket
design-system. Token warna/tipografi memakai baseline netral `index.vue` yang ada
(#171717/#A16207/#FFFFFF/#E5E5E5/#E8ECF0, system font) sebagai PROVISIONAL — DESIGN.md
v0.5 tetap PROPOSED, tidak dibekukan oleh tiket ini.

## File (allowlist tiket)

BARU: `apps/web/pages/login.vue`, `pages/workspaces/new.vue`,
`pages/workspaces/[id]/members.vue`, `pages/workspaces/[id]/modules.vue`,
`pages/invites/accept.vue`, `apps/web/server/api/[...path].ts`, `apps/web/lib/api.ts`,
`apps/web/plugins/session.client.ts`, `apps/web/assets/main.css`,
`apps/web/playwright.config.ts`, `apps/web/e2e/owner-flow.spec.ts`,
`apps/web/e2e/proxy-session.spec.ts`, `docs/reviews/f1-owner-flow-ui.md` (file ini),
15 screenshot di `design/screenshots/owner-flow/`.

UBAH: `apps/web/app.vue`, `apps/web/pages/index.vue`, `apps/web/nuxt.config.ts`
(`runtimeConfig.businessApiBase` privat; `public.apiBase` DIHAPUS),
`apps/web/package.json` + `package-lock.json` (Playwright dev-only),
`.github/workflows/ci.yml` (job `owner-flow-e2e`), `deploy/windows/compose.yml`
(web: `NUXT_BUSINESS_API_BASE=http://business-api:8080`, ganti
`NUXT_PUBLIC_API_BASE` lama). Dipertahankan utuh: koreksi tes
`module_custody_integration_test.go` (TestCustodyLockWaitRaces) dan
`docs/reviews/f1-gate-reconciliation.md` + catatan singkat baris 15 (retur utuh
biasa DITERIMA; rerun mandiri bukan setara kumulatif) — kini 160 baris,
SHA-256 `b3f5a9b98fb335051a8d89622fbefce1f382b722a92c3869f4dd9291e845540d`.

Tanpa perubahan Go backend, migrasi, RLS, skema, manifest, admin, worker, grant nyata,
atau mailer. Tanpa upgrade Nuxt/runtime.

## Verifikasi (HARNESS lokal + CI + REVIEWER Windows)

Lingkungan lokal: Node v24.22.1 / npm 10.9.4, nuxt 3.21.11, h3 1.15.11,
@playwright/test 1.65.0, Chrome for Testing 158.0.8127.18, Go 1.27.3 (lokal;
CI memakai go 1.26 / node 22), PostgreSQL 16.15 native, DB terisolasi `umnia_e2e`
(migrasi 001–010, 10 marker). Business API + web preview:3000 (upstream nyata)
dan :3001 (upstream mati `127.0.0.1:9`) dijalankan lokal.

### Implementasi awal (commit `cde8680`, HARNESS + CI)

- `npm run build` — exit 0.
- Playwright E2E lokal — **18 passed / 0 failed / 0 skipped (2.9 mnt)**, log
  `/tmp/pw-run5.log`: 13 tes alur pemilik (register/login, workspace, undang +
  terima sebagai kasir, lintas-tenant 403, aktivasi 403 jujur lalu 200 sesudah
  grant sintetis terisolasi, reload kehilangan token + lanjut URL dikenal,
  401 hapus sesi, undang akun-salah 403 + kedaluwarsa 409, keyboard/fokus error,
  state loading, screenshot) + 5 tes proxy/sesi (passthrough, matriks allowlist,
  upstream mati 502 bersih, fault backend 500 disanitasi, SSR tanpa serialisasi
  rahasia + tanpa bocor antar sesi).
- CI run #39, ID 42487853428, https://github.com/ch0c01dxyz/UMNIA-APP/actions/runs/42487853428 —
  **SUCCESS (4m 42s, 2026-11-08 23:09 WIB)**, SHA `cde8680` (diperiksa read-only
  via browser): business-api **118 PASS / 0 FAIL / 0 SKIP** + anti-skip gate
  lolos; web build sukses; owner-flow-e2e **18 passed (2.9 mnt)**; tanpa langkah
  gagal (5 peringatan + 3 notice non-fatal: Node 20 deprecated, cache Go miss,
  migrasi ubuntu-latest).
- REVIEWER Windows (bukti independen, bukan HARNESS): Docker Compose build/start
  + alur owner/staf Chromium nyata lolos **22 checks eksplisit**, termasuk
  unentitled 403, aktivasi 200 sesudah grant sintetis QA, reload/lanjut login,
  dan fallback izin kasir. Reviewer TIDAK menyatakan penerimaan final atas
  slice ini; temuan R1 di bawah berasal dari review itu.
- Screenshot awal — 15 file (5 layar × 360/768/1440), form kosong/bersih;
  diperiksa visual: tanpa kode undangan, kata sandi, atau token yang terlihat.
  Trace/video/screenshot Playwright untuk alur terautentikasi dimatikan di config.
- Cek overflow 360 px saat masuk — `documentElement.scrollWidth` = 360 =
  `innerWidth`; tanpa overflow horizontal.
- Suite Go tidak diulang penuh pada tiket awal (tanpa perubahan kode Go;
  terakhir 118/118 lokal + CI run #38 ID 42423289713 SUCCESS pada `e3f48c6`;
  koreksi fixture TestCustodyLockWaitRaces lulus fokus 8,60 dtk).

### Koreksi R1 (HARNESS lokal, UNCOMMITTED — CI NOT RUN)

Lima perbaikan atas temuan review Windows: (1) batas body proxy 16 KiB kini
ditegakkan per byte sambil streaming — RED direproduksi (chunked UTF-8 18033
byte → 401; aliran 20000 byte tanpa EOF → tanpa respons dalam 2 dtk), GREEN
sesudah perbaikan (keduanya → 400; aliran tanpa EOF ditolak dalam 1 ms);
(2) klaim "Tidak ada perubahan yang ditulis." dihapus dari status aktif +
wording "slice ini" diganti kalimat Indonesia biasa; (3) job CI
`owner-flow-e2e` diganti dari Docker PostgreSQL service ke PostgreSQL 16
native via APT (`apt-get install postgresql-16`, `pg_ctlcluster 16 main
start`); job business-api/web tidak diubah; (4) assertion rahasia diubah ke
boolean + pesan tetap — self-check sentinel palsu membuktikan bentuk baru
tidak mencetak nilai yang diperiksa saat gagal, sementara bentuk lama
mencetaknya; (5) dokumen ini diperbarui dengan fakta commit/CI di atas.

- `npm run build` — exit 0 (sesudah perbaikan proxy + modules.vue).
- Playwright E2E lokal — **19 passed / 0 failed / 0 skipped (2.9 mnt)**, log
  `/tmp/pw-run-r1b.log`: 13 alur pemilik + 6 proxy/sesi (termasuk tes baru
  batas body streaming: chunked UTF-8, aliran tanpa EOF, batas tepat 16384
  byte, body kecil chunked). Satu run perantara gagal 2 tes hanya karena
  password role bootstrap postgres lokal ter-reset (bukan bug kode);
  diperbaiki via `ALTER ROLE postgres` lalu suite hijau penuh.
- Screenshot — 15 file ditulis ulang dari aplikasi berjalan pasca-fix
  (2026-11-09 01:42–01:43 WIB, data sintetis); 6 file berubah byte dari set
  `cde8680` (03-members ×3 dengan RUN baru, 04-modules-1440/768 dengan salinan
  baru, 02-workspace-new-768); 9 file byte-identik (render form kosong yang
  deterministik; salinan baru di 04-modules-360 berada di bawah lipatan
  viewport sehingga tangkapannya tidak berubah). Diperiksa visual
  (04-modules-1440/768/360, 03-members-1440): tanpa kode undangan, kata sandi,
  atau token. Asal: baseline `cde8680` + working tree R1 yang belum di-commit
  — BUKAN dari commit baru. Pemeriksaan visual independen menunggu reviewer
  Windows; lolos screenshot tidak menggantikan pengujian fungsi/keamanan.
- **CI untuk perubahan R1: NOT RUN** — kode R1 belum dipublikasikan dan tiket
  R1 tidak memberi otorisasi commit/push. Jangan mengatribusikan run #39
  (SHA `cde8680`) ke kode R1.

### Verifikasi paket transfer R1 (REVIEWER Windows, 2026-11-09)

REVIEWER-REPORTED: ZIP transfer R1 terverifikasi — 21 checksum cocok, patch
diterapkan pada clone terisolasi, ke-20 gambar diperiksa (15 utama + 5
suplemen), web build Windows lolos, **30 browser checks + 8 stream checks
lolos**. Bukti terlingkup atas paket yang diserahkan; BUKAN penerimaan penuh
F1 dan BUKAN bukti CI R1 sukses. Temuan lanjutan dari review itu adalah
koreksi bootstrap CI native di bawah.

### Koreksi bootstrap CI native R1 (HARNESS lokal, UNCOMMITTED — CI NOT RUN)

Masalah (INFERENCE dari sumber, lalu DIBUKTIKAN pada cluster bersih, bukan
kegagalan hosted yang teramati): job `owner-flow-e2e` R1 memasang/menjalankan
PostgreSQL 16 native, tetapi tidak menetapkan password peran postgres sebelum
memakai `PGPASSWORD=ci-bootstrap-only` lewat TCP psql. `POSTGRES_PASSWORD`
pada service Docker yang dihapus adalah inisialisasi server; PGPASSWORD klien
bukan penggantinya. `pg_isready` tidak membuktikan autentikasi password.

Perbaikan (allowlist: job `owner-flow-e2e` + dokumen ini saja): langkah
install kini (a) menampilkan `pg_lsclusters` (cluster 16/main, port 5432),
(b) menetapkan password bootstrap lewat koneksi lokal istimewa — `sudo -u
postgres psql` (peer auth OS postgres), BUKAN via TCP — sebelum pemakaian TCP,
(c) membuktikan autentikasi TCP + versi server dengan `SELECT version()`
via TCP sebelum langkah Prepare. Tanpa trust, tanpa Docker Linux runtime,
tanpa ubah job lain; role runtime tetap NOSUPERUSER/NOBYPASSRLS, migrasi
001–010 tidak berubah.

Bukti (emulasi langkah CI pada cluster bersih yang dibuang — BUKAN run GitHub
Actions): cluster baru `16/ciproof` port 5433 (tanpa password preset) dibuat
dengan `pg_createcluster`; TCP `PGPASSWORD=ci-bootstrap-only` GAGAL dulu
dengan `FATAL: password authentication failed for user "postgres"` (28P01)
— membuktikan masalahnya nyata pada setup segar. Sesudah langkah bootstrap
di atas: TCP `SELECT version()` mengembalikan `PostgreSQL 16.15 (Ubuntu
16.15-0ubuntu0.24.04.1)`; Prepare membuat role `umnia_business`
(rolsuper=f, rolbypassrls=f, rolcanlogin=t) + DB `umnia_e2e_proof` dengan 10
marker migrasi. Lalu build Go + `npm run build` exit 0, dan Playwright E2E
penuh lawan cluster bersih itu: **19 passed / 0 failed / 0 skipped (3.0
mnt)**, log `/tmp/pw-run-proof.log`. Cluster dibuang sesudahnya
(`pg_dropcluster`); API/preview dikembalikan ke DB dev `umnia_e2e` (5432).
Catatan setup bukti: nama cluster tidak boleh mengandung `-` (unit systemd
`postgresql@.service` memetakan `-` ke `/` pada path config); cluster pertama
`ci-proof` gagal start karena itu, dibuat ulang sebagai `ciproof`.

Selama run bukti, 15 screenshot ditulis ulang oleh tes (RUN sintetis baru);
dipulihkan byte-per-byte dari inventaris transfer R1 (`sha256sum -c`: 15/15
OK) — artefak R1 yang diserahkan tidak berubah. Insiden: `git checkout --`
pada PNG sempat mengembalikan 6 PNG ke baseline `cde8680` (checkout membaca
dari HEAD, bukan dari working tree R1 yang belum di-commit); dipulihkan dari
salinan staging transfer. Pelajaran: jangan `git checkout --` artefak R1 yang
belum di-commit — salinan staging transfer adalah sumber pemulihan.

- **CI untuk koreksi ini: NOT RUN** — belum dipublikasikan; tidak ada
  otorisasi commit/push dalam tugas ini. Jangan klaim run hosted sukses.

### Kegagalan hosted run #40 + pin runner (HARNESS + REVIEWER)

Run #40, ID 42524082882, head `e3770ee` — **FAILED (4m 18s)**:
business-api SUCCESS (Go 118/0/0, anti-skip lolos); web SUCCESS (build
Nuxt); owner-flow-e2e FAILED 14 dtk pada langkah install — `E: Unable to
locate package postgresql-16` (exit 100); Playwright tidak jalan (0/0/0).
Log job independen mengonfirmasi runner Ubuntu 26.04.1 / image ubuntu-26.04
(REVIEWER-REPORTED): repo default Ubuntu 26 tidak membawa `postgresql-16`,
jadi `apt-get install` gagal sebelum pg_lsclusters/ALTER ROLE/TCP version
sempat jalan. Echo script bukan bukti langkah itu tereksekusi.

Koreksi (commit terpisah, CI-only): job `owner-flow-e2e` di-pin ke
`ubuntu-24.04` — repo default-nya membawa postgresql-16. Job business-api
dan web tetap `ubuntu-latest`. Tanpa PGDG, tanpa Docker Linux runtime, tanpa
ubah versi PostgreSQL, autentikasi, bootstrap, kode, dependensi, migrasi,
atau screenshot.

Temuan saat verifikasi (semuanya bug tes, bukan bug aplikasi — diperbaiki, lalu
suite hijau): (1) `page.goto()` full reload menghapus sesi memori by design,
jadi helper `gotoAuthed` menunggu URL stabil lalu masuk lagi lewat alur
login-target; (2) halaman accept tidak redirect otomatis — helper kini
mengklik prompt "Masuk / Daftar" bila terlihat; (3) cek lintas-sesi harus ke
`/api/...` (rute halaman SSR selalu 200 netral); (4) pesan fault audit backend
yang benar adalah `audit gagal ditulis`, bukan `transaksi gagal` (dibuktikan
dari `workspace.go:64`).

Catatan lingkungan: `npx playwright install chromium` gagal mengunduh (installer);
browser dipasang manual via `curl` dari CDN Playwright ke
`~/.cache/ms-playwright/` (chrome 158.0.8127.18 + headless shell); CI memakai
`npx playwright install --with-deps chromium` seperti biasa. `npm i -D
--legacy-peer-deps @playwright/test` memangkas subtree vite — JANGAN dipakai di
repo ini; lockfile diperbaiki via `npm install --package-lock-only`
(tanpa flag) lalu `npm ci --legacy-peer-deps`. `/tmp` adalah tmpfs 512M —
ekstraksi besar dilakukan di disk home.

## Batas dan next action

- CI run #39 (SHA `cde8680`) SUCCESS — lihat di atas. CI untuk perubahan R1
  dan koreksi bootstrap CI native NOT RUN (belum dipublikasikan; menunggu
  otorisasi publikasi terpisah). Koreksi bootstrap dibuktikan pada cluster
  bersih yang dibuang (emulasi langkah CI), bukan pada run hosted.
- Windows reviewer menjalankan QA Compose/browser aktual atas kode yang
  diserahkan; demo browser lokal ini bukan penerimaan Windows. Reviewer
  memeriksa ulang 15 screenshot terbaru secara independen.
- Tetap gap (di luar slice ini): workspace discovery, grant operator nyata,
  mailer, admin UI/portal, segregasi admin positif, installer Linux,
  outbox/worker, OpenAPI, finance/damage/loss. F1 gate NOT COMPLETE.
