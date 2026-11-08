# F1 Gate Reconciliation — UMNIA (2026-11-08)

TASK_ID: F1-GATE-RECONCILIATION. Baseline `e3f48c6a512d7b3cd1036c3767dc54eb0a3cb8d3`
(f1-03-identity, VERIFIED: HEAD sama, tree bersih kecuali dokumen planning F2-01-F yang
untracked dan dipertahankan). Scope: rekonsiliasi gerbang F1 dari bukti aktual — bukan
rencana fase luas, implementasi slice berikutnya, atau pendefinisian ulang gerbang. Label:
VERIFIED = dibaca/dijalankan sesi ini; REVIEWER = bukti reviewer yang diwariskan; UNKNOWN =
belum ada bukti. Gerbang F1 (FAST_TRACK.md): login → buat workspace → undang staf →
aktifkan Rental, di Windows dan Linux; tes isolasi lintas-tenant dan admin vs bisnis lulus.
Revisi 2026-11-08 (F1-GATE-PACKET-AND-UI-BOUNDARY): batas transport/sesi/navigasi/
undangan/entitlement/desain untuk slice owner-UI ditambahkan; D-GATE-2 dikoreksi.

## Matriks bukti

1. Login/register API — DONE-PROVEN. `POST /auth/register`, `POST /auth/login` (bcrypt,
sesi 24 jam, token disimpan SHA-256; migrasi `002_identity`, `577f0a5`; CI #4). Bukti
runtime: reviewer HTTP Windows; suite lokal 118/0/0. UI: GAP (lihat #5).
2. Workspace API — DONE-PROVEN. `POST /workspaces`, `GET /workspaces/{id}`,
`GET /workspaces/{id}/members` (main.go:129-131); suite 118/0/0, CI #38 ID 42423289713.
UI: GAP.
3. Invite staf API — DONE-PROVEN. `POST /workspaces/{id}/invites`, `POST /invites/accept`
(main.go:156-157); sekali pakai 72 jam, SECURITY DEFINER (002). Mailer: GAP — token
dikembalikan ke pengundang (dev-only, tercatat sejak F1-03). UI: GAP.
4. Aktivasi modul API — DONE-PROVEN. Registry read-only `063cf44`; B1 `d7a37fe`; B3
`287c7bd` (activate/deactivate/state, workspace-row lock; CI #23, 33/0/0). UI: GAP.
5. UI alur pemilik — GAP. `apps/web` hanya `pages/index.vue` (skeleton health check;
komentar "Belum ada login/workspace") + `app.vue`. Tanpa halaman login/workspace/invite/
modul/dashboard; tanpa `packages/ui`, `design/`, `apps/admin`, `apps/web/layouts`.
`DESIGN.md` v0.5 PROPOSED, token belum dibekukan owner; `package.json` web tanpa test
runner. Ini gap terbesar: gerbang F1 tidak dapat didemonstrasikan pemilik non-teknis.
6. Isolasi lintas-tenant — DONE-PROVEN. RLS ENABLE+FORCE + konteks transaksi (002–010);
tes identitas/modul/rental/custody; CI #38 118/0/0; REVIEWER Windows di `e3f48c6`:
58 request API + 12 assertion + 12 assertion SQL. Job/object/export lintas-tenant:
UNKNOWN (tanpa worker; API belum memakai storage).
7. Admin vs bisnis — SEBAGIAN. DB/role terpisah (`deploy/linux/configure-postgres.sh`;
compose `postgres-business`/`postgres-admin`, volume + kredensial terpisah); business-api
tidak pernah membaca `ADMIN_DATABASE_URL` (grep: 0 hasil); `services/` hanya `business-api`.
TAPI: tanpa admin API/portal/broker, `PLATFORM_OWNER`, login admin, atau tes positif "admin
tidak bisa baca data bisnis" — segregasi berlaku karena ketiadaan, bukan karena tes.
8. Pemberian entitlement nyata — GAP. 0 `INSERT INTO module_entitlements` di kode
non-tes/migrasi (grep VERIFIED); satu-satunya `grantEntitlement` adalah helper tes.
Aktivasi workspace nyata hari ini butuh tulis DB langsung. D-GRANT-1 masih terbuka
(otoritas/prosedur/riwayat grant belum diputuskan reviewer).
9. Audit + migrasi — DONE-PROVEN. `audit_events` append-only (trigger; DELETE ditolak);
migrasi 001–010 idempotent + readiness 10 marker; rerun kumulatif 001–010 terverifikasi;
`umnia_business` 10 marker (VERIFIED sesi ini).
10. RustFS/storage — DONE-PROVEN (dev). `install-rustfs.sh` native; probe S3 SigV4 PASS
sesi ini; `rustfs` di compose Windows. Objek lintas-tenant: UNKNOWN (belum ada API
memakai storage).
11. Bukti runtime Windows — REVIEWER. Compose up + migrate + HTTP di banyak SHA; di `e3f48c6`:
58 request + 12 assertion + 12 SQL, cooperative revoke race, 010 retained/fresh/rerun/
readiness. Alur F1 oleh pengguna: belum pernah (tanpa UI); bukan approval menyeluruh.
12. Bukti runtime Linux — SEBAGIAN. PG 16.15 native + RustFS + suite lokal 118/0/0 +
check-deployment 23/23 (VERIFIED sesi ini). TAPI `deploy/linux` hanya 2 skrip
(`configure-postgres.sh`, `install-rustfs.sh`); tanpa systemd unit, `install-umnia.sh`,
nginx, `verify-host.sh`; tanpa bukti boot-tahan-restart; tanpa backup/restore drill
(`backup-restore-drill.sh` tidak ada; PITR: UNKNOWN).
13. Outbox/worker — GAP. 0 referensi outbox di migrasi/kode non-tes; tanpa worker. Bukan
blocker langsung gerbang F1, tapi P0 mensyaratkan outbox untuk kerja async.
14. OpenAPI — GAP. Tanpa direktori `api/`, tanpa spesifikasi (P0 mensyaratkan OpenAPI
generation; P3 mensyaratkan gating dokumentasi).
15. Rental vertikal F2-01 A–F — DONE-PROVEN dalam scope beku (CI #38). Bukan bukti F1
selesai. Retur utuh biasa DITERIMA (F2-01-F); rusak/hilang, finance, koreksi: masih gap.
Rerun migrasi mandiri bukan otomatis setara state akhir kumulatif 001–010.
16. Branch/merge — FAKTA, BUKAN GAP. `main` = `a107924` spec-only; `f1-03-identity` =
224 file, 41.102 insertions, belum merge — by design.

## Bukan gap (closed — jangan dibuka ulang tanpa reproduksi baru)

F1-WEB-CONTAINER-BUILD-FIX, F1-API-CONTAINER-TOOLCHAIN-FIX, F1-WINDOWS-SHELL-LINE-ENDINGS,
F1-COMPOSE-MIGRATION-COMMAND-FIX, F1-PS51-VOLUME-PROBE-FIX, F1-04A atomic CHECK,
F1-FOUNDATION-REMEDIATION, F1-04B-B1/B3, F2-01-A–F scope beku (diterima).

## Koreksi fixture lock (deliverable A, sesi ini)

`TestCustodyLockWaitRaces` diperbaiki: mutasi deactivate/revoke kini lewat `lockTx.Exec`
— satu transaksi yang sama memegang workspace lock DAN bermutasi, lalu `lockTx.Commit`.
Komentar lama ("revoke HARUS bypass lock") menyesatkan dan diganti. Tes fokus:
`go test -run 'TestCustodyLockWaitRaces' -count=1 -v .` → PASS (8,60 dtk, exit 0), leftover 0.
Fixture lama hanya membuktikan visibilitas sesudah menunggu, bukan serialisasi kooperatif.
REVIEWER: skenario kooperatif yang benar sudah dijalankan independen di Windows (satu
transaksi lock → DELETE entitlement → commit; handover menunggu → 403, tanpa mutasi
custody/audit) — bukti independen, bukan eksekusi saya.

## Catatan pemulihan infra sesi ini

VM swap #20 (boot ~10:54 WIB): cluster PG baru, DB fixture hilang. Dipulihkan per aturan
berdiri: `configure-postgres.sh` (password dari `.env`), `ALTER ROLE postgres` bootstrap,
`umnia_test` direkreasi 001–010. DB fixture tes bersifat ephemeral, bukan bagian recovery
berdiri — direkreasi manual untuk menjalankan tes fokus.

## Rekomendasi: SATU slice berikutnya — F1-OWNER-FLOW-UI

Alasan prioritas: gerbang F1 adalah gerbang demonstrasi oleh pemilik usaha non-teknis —
bukti API/CI sudah ada, tetapi tanpa UI tidak ada jalur pengguna yang dapat menutupnya.
Batas di bawah membuat slice dapat dibekukan tanpa menyembunyikan dependensinya.

### Batas slice owner-UI (VERIFIED dari sumber di e3f48c6)

1. Transport. `nuxt.config.ts` hanya `public.apiBase` default `http://localhost:8080`, tanpa proxy; `index.vue`
fetch lintas origin di `onMounted`; Go tanpa CORS (grep: 0) — bukti sumber, BUKAN reproduksi browser. Request
ber-`Authorization` lintas origin tidak dapat diasumsikan bekerja. Batas: proxy same-origin (`server/api/[...].ts`)
ke upstream bisnis TETAP (runtime config privat), hanya path bisnis di-allowlist; tanpa upstream arbitrer, open
proxy, atau forwarding admin-plane. Preflight dibuktikan e2e browser nyata, bukan Playwright request client.
2. Sesi. Login `POST /auth/login` (auth.go:146-187) mengembalikan JSON `{"token","subject_id"}`; sesi DB 24 jam,
token SHA-256; tanpa endpoint logout/revokasi (grep: 0); auth = header `Authorization: Bearer`. Batas: token hanya
di memori klien (composable/ref — tidak pernah localStorage, URL, atau markup SSR); login ulang eksplisit sesudah
reload; "Keluar" menghapus token dari perangkat ini, dilabeli jujur ("sesi server kedaluwarsa otomatis setelah
24 jam dan tidak dapat dicabut lebih awal"). Tes: token uji tidak pernah muncul di screenshot, URL, console,
trace, atau markup. Tanpa BFF/cookie server di slice ini.
3. Navigasi workspace. TIDAK ADA `GET /workspaces` list (main.go:129-130 hanya POST create + GET satu); create
mengembalikan 201 `{"id","name"}`; GET satu mengembalikan `{"id","name","owner_subject_id"}` (non-anggota → 403).
Batas: alur new-owner-only — sesudah create, lanjut ke `/workspaces/{id}` dari respons create (URL dikenal, bukan
open redirect); sesudah reload/login tanpa URL, pengguna tidak dapat menemukan workspace-nya (batas jujur).
Endpoint list (`GET /workspaces`, via membership, paginated) adalah tiket beku terpisah, bukan bagian slice ini.
4. Undangan. Tanpa mailer. Create invite mengembalikan 201 `{"id","token","email","role"}` (workspace.go:106-164,
token sekali ke pengundang); accept butuh akun + login (`POST /invites/accept`, workspace.go:165-192; akun salah
→ 403, token salah/kedaluwarsa → 409). Batas: UI menampilkan "salin tautan" (halaman accept + `workspace_id`
saja) dan "salin kode undangan" (token, terpisah); TIDAK PERNAH mengklaim email terkirim; token tidak pernah di
URL. Halaman accept TERMASUK di slice (form: token ditempel ke input password, bukan dari URL).
5. Entitlement dan otoritas. D-GATE-2 versi lama ("bootstrap owner + audit") DICABUT sebagai rumusan: workspace
owner BUKAN deployment operator dan TIDAK BOLEH self-entitle lewat UI. Otoritas/prosedur grant nyata tetap belum
beku (D-GRANT-1, keputusan reviewer terpisah). Batas: UI menampilkan 403 terkendali ("modul ini belum menjadi hak
workspace ini") dengan arahan hubungi operator; tes positive-path QA memakai grant sintetis terisolasi SAJA dan
TIDAK mengklaim aktivasi workspace nyata atau F1 penuh.
6. Desain dan verifikasi. `DESIGN.md` v0.5 PROPOSED; token baseline AKTUAL adalah `<style>` `index.vue` (primer
#171717, aksen #A16207, latar #FFFFFF, border #E5E5E5, muted #E8ECF0, font sistem, focus-visible,
prefers-reduced-motion) — dipakai sebagai provisional, BUKAN disetujui owner. Layar: 5 (login, workspace-new,
members, modules, invites-accept) + shell `app.vue`. Klien/proxy: `lib/api.ts` (fetch same-origin `/api/...`) +
`server/api/[...].ts`. Config/deps: `nuxt.config.ts` (runtime config privat), `package.json` + `package-lock.json`
(Playwright dev-only), TANPA framework unit-test baru. Playwright dibenarkan karena web tanpa test runner dan bukti
yang dibutuhkan adalah alur browser terhubung + screenshot, bukan unit DOM; job e2e CI (browser + business-api +
DB tes terisolasi) adalah perubahan CI yang dibekukan bersama tiket. Bukti: alur browser nyata; state 401/403/
validasi/loading/empty/error/no-permission; keyboard/fokus; screenshot 360/768/1440 tiap layar. TIDAK ADA klaim
MCP desain tervetting terpasang atau screenshot diambil (belum ada).
Allowlist: NEW `apps/web/pages/login.vue` (login + register), NEW `apps/web/pages/workspaces/new.vue`,
NEW `apps/web/pages/workspaces/[id]/members.vue` (daftar anggota + undang staf), NEW
`apps/web/pages/workspaces/[id]/modules.vue` (daftar modul + aktifkan rental.basic), NEW
`apps/web/pages/invites/accept.vue` (form tempel token), NEW `apps/web/server/api/[...].ts` (proxy
same-origin), NEW `apps/web/lib/api.ts` (fetch same-origin + pemetaan error flat), NEW
`apps/web/e2e/owner-flow.spec.ts` (Playwright), MODIFY `apps/web/app.vue` (shell + redirect logged-out),
`apps/web/nuxt.config.ts` (runtime config privat), `apps/web/package.json` + `package-lock.json`
(Playwright dev-only). TANPA: backend, migrasi, compose, freeze DESIGN.md, dashboard/katalog/rental/
finance/admin UI, endpoint list workspace, mailer, grant UI. (Job e2e CI dibekukan bersama tiket.)
Penerimaan/tes: `npm run build` lolos (job web CI); smoke Playwright browser nyata melawan business-api lokal:
register → login → buat workspace → undang staf (token tampil sekali, disalin) → accept akun kedua → aktifkan
rental.basic → 200 (dengan grant sintetis terisolasi); tanpa entitlement → 403 terkendali; tanpa login →
redirect login; token uji tidak bocor ke screenshot/URL/console/trace/markup; screenshot 360/768/1440 tiap
layar; copy Bahasa Indonesia per pemetaan docs/12, tanpa jargon backend; error login tidak enumerasi akun.
Risiko: sesi server tidak dapat dicabut sebelum 24 jam (label jujur di UI); pengguna kembali tanpa URL
workspace tidak dapat menemukan workspace-nya (batas new-owner-only); token desain provisional; modul lain
tetap 403/409 — di luar scope.
Keputusan (maks. 3, satu default tiap item): D-B1 — Transport: proxy same-origin allowlist (default) vs CORS
origin eksplisit sempit. D-B2 — Sesi + navigasi: token in-memory + alur new-owner-only + accept form (default)
vs cookie/BFF + endpoint list sebagai tiket lanjutan. D-B3 — Undangan + entitlement: accept page termasuk +
grant hanya operator (D-GRANT-1 terpisah) + UI 403 terkendali (default) vs accept ditunda sebagai gap dan
tanpa prosedur grant bernama.
Gerbang tetap seperti FAST_TRACK.md; slice ini menutup keterdemonstrasian, bukan mengubah definisi lulus.
Bukti segregasi admin positif, grant nyata, dan installer tetap butuh tiket beku reviewer terpisah; demo
frontend tidak menghapusnya.
