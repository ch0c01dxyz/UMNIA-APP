# F1-OWNER-FLOW-UI — laporan implementasi (lokal, belum review)

TASK_ID: F1-OWNER-FLOW-UI. Baseline `e3f48c6a512d7b3cd1036c3767dc54eb0a3cb8d3`,
branch `f1-03-identity`. HEAD tidak berubah; semua perubahan UNCOMMITTED (tiket ini
tidak memberi otorisasi commit/push). Gate F1 tetap NOT COMPLETE.

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

## Verifikasi lokal (VERIFIED)

Lingkungan: Node v24.22.1 / npm 10.9.4, nuxt 3.21.11, h3 1.15.11,
@playwright/test 1.65.0, Chrome for Testing 158.0.8127.18, Go 1.27.3 (lokal;
CI memakai go 1.26 / node 22), PostgreSQL 16.15 native, DB terisolasi `umnia_e2e`
(migrasi 001–010, 10 marker). Business API + web preview:3000 (upstream nyata)
dan :3001 (upstream mati `127.0.0.1:9`) dijalankan lokal.

- `npm run build` — exit 0.
- Playwright E2E — **18 passed / 0 failed / 0 skipped (2.9 mnt)**, log
  `/tmp/pw-run5.log`: 13 tes alur pemilik (register/login, workspace, undang +
  terima sebagai kasir, lintas-tenant 403, aktivasi 403 jujur lalu 200 sesudah
  grant sintetis terisolasi, reload kehilangan token + lanjut URL dikenal,
  401 hapus sesi, undang akun-salah 403 + kedaluwarsa 409, keyboard/fokus error,
  state loading, screenshot) + 5 tes proxy/sesi (passthrough, matriks allowlist,
  upstream mati 502 bersih, fault backend 500 disanitasi, SSR tanpa serialisasi
  rahasia + tanpa bocor antar sesi).
- Screenshot — 15 file (5 layar × 360/768/1440), form kosong/bersih; diperiksa
  visual: tanpa kode undangan, kata sandi, atau token yang terlihat. Trace/video/
  screenshot Playwright untuk alur terautentikasi dimatikan di config.
- Cek overflow 360 px saat masuk — `documentElement.scrollWidth` = 360 =
  `innerWidth`; tanpa overflow horizontal.
- Suite Go tidak diulang penuh pada tiket ini (tanpa perubahan kode Go; terakhir
  118/118 lokal + CI run #38 ID 42423289713 SUCCESS pada `e3f48c6`; koreksi
  fixture TestCustodyLockWaitRaces lulus fokus 8,60 dtk).

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

- Job CI `owner-flow-e2e` ditulis (job ke-3 di `ci.yml`, DB `umnia_e2e` terisolasi,
  tanpa upload artefak) tetapi **NOT RUN**: kode belum dipublikasikan dan tiket
  ini tidak memberi otorisasi commit/push; eksekusi CI menunggu publikasi yang
  diotorisasi, lalu verifikasi SHA eksak seperti biasa.
- Windows reviewer menjalankan QA Compose/browser aktual atas kode yang
  diserahkan; demo browser lokal ini bukan penerimaan Windows.
- Tetap gap (di luar slice ini): workspace discovery, grant operator nyata,
  mailer, admin UI/portal, segregasi admin positif, installer Linux,
  outbox/worker, OpenAPI, finance/damage/loss. F1 gate NOT COMPLETE.
