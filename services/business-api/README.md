# F1-01 — Skeleton (bukan aplikasi jadi)

## Jalankan di Linux (native, tanpa Docker)

```bash
# Terminal 1 — business-api
go run ./services/business-api
# atau: go build -o /tmp/business-api ./services/business-api && /tmp/business-api
curl http://localhost:8080/healthz   # {"status":"ok","service":"business-api"}
curl http://localhost:8080/readyz    # database: "not_configured" sampai F1-02

# Terminal 2 — web
cd apps/web && npm install && npm run dev
# buka http://localhost:3000 — halaman menampilkan status business-api
```

## Jalankan di Windows (Docker Compose)

```powershell
Copy-Item .env.example .env
docker compose -f deploy/windows/compose.yml up --build
# web: http://localhost:3000, api: http://localhost:8080/healthz
```

Jalur Windows ini **UNVERIFIED** di VM Linux ini (tidak ada Docker di sini
dan Linux tidak boleh memakai Docker untuk runtime UMNIA); wajib dicoba
di host Windows sebelum diklaim lulus.

## Tes & lint

```bash
gofmt -l services/business-api   # harus kosong
go vet ./services/business-api/...
go test ./services/business-api/...
cd apps/web && npm run build
```

## F1-02 — database

Linux native (tanpa Docker):

```bash
sudo apt install -y postgresql postgresql-client
UMNIA_BUSINESS_DB_PASSWORD='...' UMNIA_ADMIN_DB_PASSWORD='...' \
  bash deploy/linux/configure-postgres.sh
# Script membuat role/database umnia_business + umnia_admin yang terpisah,
# lalu menerapkan services/business-api/migrations/001_init.sql
# (hanya tabel schema_migrations; skema domain menyusul di tiketnya).

BUSINESS_DATABASE_URL='postgres://umnia_business:PASSWORD@localhost:5432/umnia_business?sslmode=disable' \
  go run ./services/business-api
curl http://localhost:8080/readyz   # database: "ok" bila ping berhasil
```

Windows: `docker compose -f deploy/windows/compose.yml up --build`
menjalankan postgres-business, postgres-admin, dan RustFS (UNVERIFIED
di host Windows, seperti F1-01).

## F1-03 — identity, workspace, RBAC, undangan, audit, RLS

Kontrak role database: migrasi mengasumsikan role runtime bernama
`umnia_business` (pemilik tabel, penerima GRANT EXECUTE fungsi helper)
dan role superuser `postgres` (pemilik fungsi helper SECURITY DEFINER).
Deployment WAJIB membuat kedua role itu sebelum migrasi — di Linux
oleh `deploy/linux/configure-postgres.sh`, di Windows oleh initdb
Compose. Role runtime harus NOSUPERUSER dan NOBYPASSRLS; superuser
melewati RLS apa pun flag-nya, jadi API tidak boleh memakai akun
bootstrap/superuser.

Endpoint (semua kecuali `/auth/*` memakai `Authorization: Bearer <token>`):

```bash
POST /auth/register   # {email, password>=8} -> 201 {id, email}
POST /auth/login      # -> 200 {token, subject_id}; token berlaku 24 jam
POST /workspaces      # {name} -> 201; pembuat jadi owner + anggota aktif
GET  /workspaces/{id} # anggota saja; non-anggota -> 403 (bukan 404)
GET  /workspaces/{id}/members
POST /workspaces/{id}/invites  # owner/manager saja; {email, role} -> 201 {token}
POST /invites/accept  # {workspace_id, token} -> 200; token sekali pakai (72 jam)
GET  /workspaces/{id}/audit    # owner/manager saja; append-only di database
```

Isolasi ditegakkan dua lapis: transaksi API menyetel `app.current_subject_id`
dan `app.current_workspace_id`, dan PostgreSQL RLS (FORCE) menyaring baris
berdasarkan membership aktif. Fungsi helper RLS (`app.is_member`,
`app.has_permission`, `app.session_subject`, `app.accept_invite`) milik
postgres (superuser) supaya SECURITY DEFINER benar-benar melewati RLS;
kalau dimiliki role aplikasi, fungsi itu ikut tersaring dan gagal.

Tes integrasi dilewati bila `BUSINESS_DATABASE_URL` kosong (mode
unit-only lokal); jalankan lokal dengan URL dari `.env`:

```bash
BUSINESS_DATABASE_URL='postgres://umnia_business:PASSWORD@localhost:5432/umnia_business?sslmode=disable' \
  go test ./services/business-api/...
```

Gate CI (`.github/workflows/ci.yml`) berbeda: ia memakai service
PostgreSQL, membuat role runtime + database tes, menjalankan migrasi
sebagai bootstrap, lalu menjalankan tes dengan URL terisi dan GAGAL
bila ada tes yang ter-SKIP atau URL kosong. Jangan menyamakan hijau
unit-only lokal dengan lulus gate integrasi.

Readiness `/readyz` memeriksa migrasi `001_init`, `002_identity`,
`003_modules`, dan `004_module_installations` sudah terapan, bukan
hanya ping: database kosong tanpa skema atau yang berhenti di 003
dilaporkan `error` (503), bukan `ok`; marker tambahan di luar keempat
nama itu tidak menggagalkan readiness.

Windows: lihat `deploy/windows/README.md` — akun bootstrap vs runtime,
service `migrate`, dan jalur upgrade volume lama. Compose Windows
masih UNVERIFIED di host Windows.

## F1-04A — registry katalog modul (read-only)

Migrasi `003_modules` menambah tabel global `module_registry`
(module_id, version, manifest JSONB, registered_at; kunci komposit
module_id+version) dan men-seed empat contoh manifest yang ada
(finance.basic, finance.advanced, rental.basic, spa.standard, masing-
masing 0.1.0) sebagai DEKLARASI — bukan bukti layanan domain atau
simulation dataset-nya ada. Tabel dimiliki akun bootstrap/migrasi;
role runtime hanya mendapat SELECT (INSERT/UPDATE/DELETE ditolak).

Endpoint baru:

```bash
GET /workspaces/{id}/modules   # anggota dengan view_workspace (termasuk kasir) -> 200 {"modules": [manifest utuh, ...]}
```

Daftar diurutkan menurut identity/version; tidak ada field status
aktif/entitled/available, tidak ada endpoint mutasi, tidak ada
aktivasi/deaktivasi, tidak ada entitlement, dan katalog tidak memberi
izin pengguna maupun kemampuan yang bisa dieksekusi. Non-anggota ->
403, tanpa token -> 401, id workspace malformed -> 400.

Manifest contoh divalidasi di CI terhadap skema kanonis
(`schemas/module-manifest.schema.json`) lewat helper
`scripts/validate_blueprint.py` (plus tiga cek negatif: field tak
dikenal, field required bersarang hilang, versi malformed).

## F1-04B-B1 — penyimpanan instalasi modul + entitlement (tanpa endpoint aktivasi)

Migrasi `004_module_installations` menambah dua tabel tenant
(`module_installations` dan `module_entitlements`), keduanya dimiliki
akun bootstrap/migrasi dan dilindungi RLS (FORCE). `module_installations`
satu baris per workspace+modul (status `active`/`inactive`,
`activated_by` merujuk `identity_subjects`, tanpa DELETE untuk runtime);
`module_entitlements` satu baris per workspace+modul sebagai snapshot
hak saat ini (tanpa baris di-seed — tidak ada yang bisa diaktifkan
sebelum operator memberi grant lewat prosedur yang dibekukan di luar
B1). Anggota workspace boleh membaca state workspace-nya sendiri;
hanya owner yang punya izin baru `manage_modules` dan boleh menulis
instalasi. Readiness `/readyz` kini mewajibkan kelima marker 001–005
(F2-01-A menambah `005_core_master_data`).

**Yang belum ada:** grant entitlement nyata, resolver dependensi, dan
capability guard (F2-01). Penyimpanan ini saja tidak memberi kemampuan
yang bisa dieksekusi pada modul mana pun.

## F1-04B-B3 — handler state instalasi (entitled intent, metadata-only)

Tiga rute terautentikasi di bawah `/workspaces/{id}/modules/{module_id}`:

- `POST .../activate` — body JSON `{"version":"0.1.0"}` (versi eksplisit
  dan terdaftar, wajib; tanpa default versi terbaru). Tulis memakai
  `manage_modules` (owner saja).
- `POST .../deactivate` — tanpa body; tidak butuh entitlement.
- `GET ...` — baca state memakai `view_workspace` (anggota mana pun).

Respons state: `{"module_id","version":null-atau-string,"status":
"active"|"inactive","activated_at":null-atau-timestamp,"deactivated_at":
null-atau-timestamp}`. Modul dikenal yang belum pernah diinstal
mengembalikan 200 `inactive` tanpa membuat baris. Modul tak dikenal
404; versi tak terdaftar 404; versi pin berbeda 409 (walau inactive);
tanpa entitlement 403; dependensi/konflik tak terpenuhi 409.

**Entitled intent, bukan hak efektif atau kesehatan layanan.** Aktivasi
mencatat maksud instalasi yang berhak: entitlement dicek dulu (403 bila
absen), lalu resolver metadata — requires non-optional harus dipenuhi
penyedia eligible (aktif + entitled) di workspace yang sama, konflik
ditolak dari kedua arah daftar, satu exclusive_group hanya boleh satu
modul aktif. Aktivasi yang sudah aktif dengan versi sama adalah no-op
200 tanpa cek entitlement/audit/perubahan timestamp — snapshot maksud
itu tetap ada walau entitlement dicabut; deaktivasi sesudah revoke
tetap berhasil. Otorisasi, cek state, mutasi, dan audit berjalan dalam
satu transaksi dengan kunci baris workspace (`SELECT ... FOR UPDATE`)
sesudah otorisasi; isolasi efektif read committed (diverifikasi tes).
Audit `module_activated`/`module_deactivated` memakai `entity_id`
NULL, `entity_type` `module_installation`, dan details
`{module_id, version, old_status, new_status}`; no-op tidak menambah
audit; riwayat tidak pernah dihapus.

**Batas:** tidak ada otoritas pemberian entitlement nyata atau billing
(entitlement hanya dibaca dari snapshot `module_entitlements`), tidak
ada guard capability, dan tidak ada endpoint bisnis — handler bisnis
masa depan (F2) tetap wajib memeriksa hak, instalasi aktif, kesiapan
penyedia nyata, dan kebijakan pengguna/sumber daya. Sejak F2-01-A,
`rental.basic` berhak bisa aktif lewat penyedia core nyata (baca
`parties`/`catalog_items`); `spa.standard` tetap tidak bisa karena
`booking.resource.hold` tidak punya penyedia.

## F2-01-A — master data core (customers + catalog items)

Migrasi `005_core_master_data` menambah dua tabel core milik bootstrap
dengan RLS (FORCE): `parties` (satu baris per pelanggan; `type` dikunci
`customer` untuk slice ini — tanpa supplier dan tanpa kolom kontak,
jadi tidak ada PII yang tidak dibutuhkan) dan `catalog_items` (sku
unik per workspace, `name`, `unit` label bisnis, `status`
active/archived, `revision` mulai 0). Runtime hanya SELECT + INSERT;
tanpa UPDATE/DELETE/TRUNCATE. Izin baru `manage_business_data` hanya
untuk owner + manager.

Empat rute terautentikasi, semua di bawah `authMiddleware` +
`beginTenant` dengan envelope error Bahasa Indonesia yang datar:

- `POST /workspaces/{id}/customers` — body `{"display_name":"..."}`;
  type dikunci server-side `customer`. 201 mengembalikan baris yang
  tersimpan (`id`, `workspace_id`, `type`, `display_name`,
  `created_at`, `updated_at`, timestamp UTC).
- `GET /workspaces/{id}/customers` — baca memakai `view_workspace`.
- `POST /workspaces/{id}/catalog/items` — body `{"sku":"...","name":
  "...","unit":"pcs"}`; unit label wajib, bukan aturan konversi.
  201 mengembalikan baris tersimpan (`status` `active`, `revision`
  0). Duplikat SKU di workspace yang sama → 409 (case-sensitive,
  sesudah trim); SKU sama di workspace lain boleh. 409 hanya untuk
  pelanggaran unik nyata (SQLSTATE 23505 pada constraint SKU);
  galat backend lain → 500.
- `GET /workspaces/{id}/catalog/items` — baca memakai `view_workspace`.

Validasi: trim whitespace luar; display_name/name maks 200 karakter,
sku maks 64, unit maks 32; field tak dikenal, tipe salah, dokumen JSON
tambahan, nilai kosong/terlalu panjang, dan body di atas 16 KiB → 400
terkendali. Invarian nonblank/panjang juga ditegakkan CHECK di database
supaya INSERT langsung lewat runtime pun ditolak. List memakai
`{"items":[...]}` terurut `(created_at, id)` dengan pagination
`limit` default 50 (1–100) dan `offset` default 0 (nonnegatif);
query tidak valid → 400. Offset pagination bukan snapshot: baris yang
disisipkan bersamaan dapat menggeser halaman. Keunikan SKU bukan
idempotensi HTTP penuh; create customer tidak idempotent.

Setiap create menulis baris + audit append-only (`customer_created` /
`catalog_item_created`, `entity_id` UUID baris baru, details minimal
`{"type":...}` tanpa nama/body/kontak) dalam satu transaksi dan baru
membalas 201 sesudah commit; gagal audit → rollback penuh (500).

**Penyedia core nyata.** Hanya `customer.profile.read` dan
`catalog.item.read` yang dikenali sebagai kapabilitas core, didukung
bacaan `parties`/`catalog_items` yang diimplementasikan. Resolver
aktivasi menjalankan bacaan nyata (`SELECT ... LIMIT 1`, tanpa
`COUNT(*)`) di dalam transaksi aktivasi sesudah otorisasi — tabel
kosong tetap valid, tanpa fake row. Galat query core → 500, bukan 409
dan bukan "tersedia". Sebelum bacaan core, resolver memeriksa izin baca
(`view_workspace`) subjek di transaksi yang sama: tanpa izin baca → 403
tanpa mutasi/audit; galat query izin → 500. Sukses bacaan itu sendiri bukan bukti RLS,
membership, izin efektif, kesehatan provider, atau enforcement
consumer: otorisasi divalidasi terpisah dan RLS diuji terpisah.
Aktivasi tetap entitled intent; consumer rental masa depan wajib
memeriksa ulang entitlement/state aktif dan memilih tiap customer/item
yang dirujuk di dalam transaksinya sendiri. Tidak ada guard helper
atau endpoint kontrak di slice ini.

**Yang belum ada:** stok/harga/finance, lifecycle dan endpoint kontrak
rental, UI, supplier, dan pemberian entitlement nyata/billing.

## F2-01-B — kontrak rental DRAFT + guard (migrasi 006)

Migrasi `006_rental_contracts` menambah tabel `rental_contracts`
milik bootstrap dengan RLS (FORCE): satu baris mencatat pelanggan,
barang, dan periode yang dimaksud — status `draft` saja (CHECK),
tanpa stok, harga, uang, handover, pembayaran, ketersediaan fisik,
atau lifecycle. Satu baris `catalog_items` BUKAN unit fisik
(`physical_asset` belum ada); draft tidak mereservasi apa pun dan
tidak membuktikan overlap aman. Runtime hanya SELECT + INSERT; tanpa
UPDATE/DELETE/TRUNCATE. Draft immutable pada slice ini; tidak ada
penghapusan history pada operasi modul. FK komposit tenant
(`workspace_id, customer_id` → `parties`, `workspace_id,
catalog_item_id` → `catalog_items`) menolak tautan lintas-tenant;
keduanya memakai `UNIQUE (workspace_id, id)` baru pada tabel core
sebagai target. `client_request_id` (1–128 karakter ASCII
[huruf, digit, `-`, `_`, `.`, `:`]) ditegakkan juga oleh CHECK.

Dua rute terautentikasi, semua di bawah `authMiddleware` +
`beginTenant` dengan envelope error Bahasa Indonesia yang datar:

- `POST /workspaces/{id}/rental/contracts` — body strict JSON 16 KiB
  `{"customer_id":"...","catalog_item_id":"...","starts_at":"...",
  "ends_at":"..."}` (tanggal RFC3339 dengan zona waktu eksplisit,
  tahun 1–9999) + header `Idempotency-Key` wajib (tepat satu nilai).
  201 mengembalikan baris tersimpan (`id`, `workspace_id`,
  `customer_id`, `catalog_item_id`, `client_request_id`, `status`
  `draft`, `starts_at`, `ends_at`, `created_at`, `updated_at`).
  Timestamp dikembalikan dalam UTC dengan presisi mikrodetik bila ada
  (RFC3339Nano): `Format(RFC3339)` menghilangkan fraksi detik,
  sehingga respons akan tidak konsisten dengan nilai ternormalisasi
  yang dipakai perbandingan replay.
- `GET /workspaces/{id}/rental/contracts` — list histori terurut
  `(created_at, id)`, pagination seperti F2-01-A. Hanya membership +
  `view_workspace`; TIDAK mensyaratkan instalasi/entitlement Rental
  aktif, supaya draft tetap terbaca sesudah deaktivasi/pencabutan.

**Guard (di dalam handler create, bukan helper terpisah).** Satu
transaksi READ COMMITTED: otorisasi `manage_business_data` (owner +
manager) → lock baris workspace yang sama dengan B3 (`FOR UPDATE`,
sesudah otorisasi, dipegang sampai commit/rollback) → instalasi
`rental.basic` harus aktif → entitlement versi aktif yang tepat dicek
ulang tiap request (revoke memblokir create baru walau baris instalasi
masih aktif) → cek `view_workspace` eksplisit sebelum payload draft
maupun referensi core diakses (sukses bacaan bukan bukti otorisasi)
→ lookup kunci idempotency → cek referensi → INSERT + audit →
commit. Galat query guard → 500 terkendali, bukan 403 dan bukan
"diizinkan". Tidak ada grant nyata; entitlement di tes hanya fixture
sintetis terisolasi.

**Idempotensi.** Kunci yang sama + keempat field ternormalisasi yang
sama (customer, item, starts, ends) → 200 baris asli, tanpa tulis /
audit / perubahan timestamp. Kunci yang sama + body berbeda → 409.
Instant dinormalisasi ke UTC presisi mikrodetik SEBELUM perbandingan
periode dan replay (presisi penyimpanan TIMESTAMPTZ): representasi
zona waktu yang setara dan nanodetik yang terpotong ikut replay 200.
Replay TIDAK memvalidasi ulang status referensi: barang yang
belakangan diarsipkan tidak menghalangi replay selama otorisasi/guard
masih berlaku. Otorisasi, instalasi aktif, entitlement, dan izin baca
selalu diperiksa SEBELUM replay: caller yang dicabut/dinonaktifkan
mendapat 403 bahkan untuk kunci lama. `ends_at <= starts_at` sesudah
normalisasi → 400. Kunci duplikat di DB (`UNIQUE (workspace_id,
client_request_id)`) hanya backstop; galat insert tak terduga → 500
tanpa menebak jenis galat dari teks pesan.

**Rujukan.** Pelanggan dibaca `workspace_id + id + type='customer'`;
hilang/lintas-tenant → 404 dengan pesan yang sama. Barang dibaca
`workspace_id + id` TANPA filter status di WHERE supaya arsip bisa
dibedakan dari hilang: hilang/lintas-tenant → 404, milik sendiri tapi
`archived` → 409. Audit `rental_contract_created` (entity_id UUID
draft, details hanya `customer_id` + `catalog_item_id`) ditulis dalam
transaksi yang sama; gagal audit → rollback penuh (500).

**Perilaku B3 dipertahankan.** Aktivasi ulang versi sama yang sudah
aktif sesudah revoke tetap 200 snapshot intent (tanpa audit baru);
aktivasi dari inactive sesudah revoke 403; draft baru sesudah revoke
403. Create vs deactivate dan create vs revoke (kooperatif) diserialkan
lock baris workspace: create commit dulu → draft tercatat sebagai
histori; mutasi commit dulu → create 403. Kunci sama yang dibuat
bersamaan → satu 201 + satu 200, tepat satu baris + satu audit. Kunci
berbeda dengan item+periode yang sama boleh keduanya 201: TIDAK ada
perlindungan overlap/reservasi pada slice ini.

**Yang belum ada:** harga (`price_versions` + snapshot agreed amount),
stok/ketersediaan/reservasi, retur/rusak/hilang, finance, lifecycle
kontrak, UI, dan pemberian entitlement nyata/billing. Gate F1 penuh
dan gate double-booking tetap belum lengkap.

## Yang sengaja TIDAK ada di F1-03

UI login/workspace (web masih skeleton F1-01), mailer undangan (token
undangan dikembalikan sekali ke pengundang — di produksi harus lewat
email), logout/refresh token, grant entitlement nyata, serta capability
guard (F1-04B ke atas; F1-04A baru menyediakan katalog read-only,
F1-04B-B1 menyediakan penyimpanan instalasi/entitlement, F1-04B-B3
menyediakan handler state instalasi metadata-only, F2-01-A menyediakan
master data core customers + catalog items). Linter khusus web
(ESLint) belum dipasang; lint = gofmt + go vet (API) + build Nuxt
(web).
