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

**Yang belum ada:** snapshot agreed amount yang mengikat (slice harga
F2-01-D baru menambah penjadwalan `price_versions` + quote tak
mengikat — lihat F2-01-D — bukan harga yang disepakati), stok
kuantitas/ledger (F2-01-C baru menambah aset fisik + hold
eksklusif + bacaan ketersediaan, bukan stok kuantitas), retur/rusak/
hilang, finance, lifecycle kontrak, UI, dan pemberian entitlement
nyata/billing. Gate F1 penuh tetap belum lengkap; gate double-booking
kini tertutup untuk hold HIDUP pada unit teridentifikasi (lihat
F2-01-C), bukan untuk booking sumber daya umum.

## F2-01-C — aset fisik + hold eksklusif + ketersediaan (migrasi 007)

Migrasi `007_asset_availability` menambah tabel `physical_assets`
dan `rental_holds` milik bootstrap dengan RLS (FORCE). Satu baris
`physical_assets` adalah satu unit fisik yang teridentifikasi
(serial, unik per workspace, case-sensitive seperti SKU) dari satu
`catalog_items` — tanpa kolom status dan tanpa kolom kuantitas:
ketersediaan diturunkan dari hold yang aktif. Satu baris
`rental_holds` menahan satu unit untuk periode satu draft yang sudah
ada (periode disalin dari draft, bukan input): status `held` /
`released` / `expired`, `expires_at` = cutoff pembuatan + 24 jam
(server-side, immutable). Hold BUKAN harga, perjanjian, handover,
pembayaran, atau rental operasional; tidak ada stok kuantitas/ledger,
status maintenance/retired, atau condition history di slice ini.
FK komposit tenant (`workspace_id, draft_id` → `rental_contracts`,
`workspace_id, asset_id` → `physical_assets`, `workspace_id,
catalog_item_id` → `catalog_items`) menolak tautan lintas-tenant;
`rental_contracts` dan `physical_assets` mendapat `UNIQUE
(workspace_id, id)` baru sebagai target FK. `client_request_id`
(aturan `Idempotency-Key` yang sama dengan draft) dan `serial`
(nonblank, maks 64 karakter) ditegakkan juga oleh CHECK. Overlap
dijaga constraint `rental_holds_no_overlap`: `EXCLUDE USING gist
(workspace_id WITH =, asset_id WITH =, period WITH &&) WHERE
(status = 'held')` pada kolom `period tstzrange` generated half-open
`[)` (butuh ekstensi `btree_gist`, dibuat oleh migrasi lewat akun
bootstrap). TIDAK ADA predikat berubah-waktu di dalam constraint:
baris `held` yang kedaluwarsa tetap `held` sampai sebuah tulis
membaliknya. Aset runtime hanya SELECT + INSERT; hold runtime SELECT
+ INSERT + UPDATE kolom (`status`, `updated_at`) saja — kebijakan
UPDATE membatasi baris yang boleh diubah pada yang sedang `held`
(USING) dan hasil pada `released`/`expired` (WITH CHECK), jadi
workspace/draft/aset/periode/kunci/`expires_at`/waktu pembuatan
tidak bisa diubah runtime. Tanpa UPDATE tabel, DELETE, TRUNCATE.

Enam rute terautentikasi, semua di bawah `authMiddleware` +
`beginTenant` dengan envelope error Bahasa Indonesia yang datar:

- `POST /workspaces/{id}/assets` — body strict JSON 16 KiB
  `{"catalog_item_id":"...","serial":"SN-001"}`. Registrasi aset
  adalah master data core: `manage_business_data` (owner + manager)
  + cek `view_workspace` eksplisit sebelum bacaan item; TANPA syarat
  aktivasi/entitlement modul. Item hilang/lintas-tenant → 404,
  item own archived → 409, serial duplikat → 409 (hanya SQLSTATE
  23505 pada constraint serial). 201 mengembalikan baris aset.
  Serial unik BUKAN idempotensi HTTP penuh.
- `GET /workspaces/{id}/assets` — list `view_workspace`,
  pagination seperti F2-01-A, urutan `(created_at, id)`.
- `GET /workspaces/{id}/assets/{asset_id}/availability?starts_at=...
  &ends_at=...` — `view_workspace` saja (tanpa aktivasi/entitlement).
  Validasi tanggal sama dengan draft (RFC3339 zona eksplisit, tahun
  input DAN tahun ternormalisasi UTC 1–9999, `ends>starts` sesudah
  normalisasi). `available` = tidak ada hold `held` yang belum
  kedaluwarsa (cutoff jam database) dengan periode tumpang tindih
  `[)`. Advisory (TOCTOU): hanya create hold yang menjamin.
- `POST /workspaces/{id}/rental/contracts/{draft_id}/holds` — body
  strict JSON 16 KiB `{"asset_id":"..."}` + header `Idempotency-Key`
  wajib (tepat satu nilai, case-sensitive). 201 hold / 200 replay
  kunci sama. Lihat guard di bawah.
- `POST /workspaces/{id}/rental/holds/{hold_id}/release` — release
  `held` → `released` + audit → 200. Release ulang (sudah released
  atau efektif expired) → 200 catatan saat ini, tanpa audit kedua
  dan tanpa perpanjangan TTL. Release TIDAK mensyaratkan instalasi/
  entitlement Rental aktif, supaya owner/manager yang sah tetap bisa
  membebaskan hold sesudah revoke/deactivate. Hold hilang/
  lintas-tenant → 404.
- `GET /workspaces/{id}/rental/holds` — list histori
  `view_workspace`, tanpa syarat aktivasi/entitlement; pagination
  seperti F2-01-A.

**Guard hold create (di dalam handler, bukan helper terpisah).**
Satu transaksi READ COMMITTED: otorisasi `manage_business_data` →
lock baris workspace yang sama dengan B3 (`FOR UPDATE`, sesudah
otorisasi, dipegang sampai commit/rollback) → instalasi
`rental.basic` harus aktif → entitlement versi aktif yang tepat
dicek ulang tiap request → cek `view_workspace` eksplisit → lookup
kunci idempotency di bawah lock → cek referensi → lazy expiry →
INSERT + audit → commit. Galat query guard → 500 terkendali.

**Jam (cutoff).** Satu nilai `clock_timestamp()` database diambil
dalam pernyataan TERPISAH sesudah lock didapat, lalu dipakai ulang
untuk expiry, `created_at`/`updated_at`, dan perhitungan TTL
(`expires_at` = cutoff + 24 jam). `now()`/`CURRENT_TIMESTAMP`
adalah `transaction_timestamp` (awal transaksi) dan TIDAK boleh
dipakai: permintaan yang menunggu lock melewati deadline harus
melihat hold lama sebagai kedaluwarsa. `expires_at` immutable
sesudah dibuat; tidak ada renewal — hold kedaluwarsa diganti hold
baru.

**Replay & status efektif.** Lookup kunci terjadi SEBELUM cek
kelayakan referensi dan SEBELUM mutasi lazy-expiry. Kunci sama +
`draft_id`/`asset_id` ternormalisasi yang sama → 200 hold EFEKTIF
saat ini (tanpa tulis/audit/perpanjangan TTL); kunci sama + payload
beda → 409. Status yang dibaca adalah status efektif: baris `held`
yang `expires_at`-nya sudah lewat pada cutoff diekspos sebagai
`expired` walau persistensi malasnya belum berjalan; pembacaan
(GET/list/replay) tidak memutasi baris dan `updated_at` tidak ikut
berubah oleh expiry turunan waktu. Replay adalah bacaan histori:
replay tetap 200 sesudah item diarsipkan, selama otorisasi/guard
masih berlaku (revoke/deactivate → 403 bahkan untuk kunci lama);
kunci yang sudah released/expired tetap terikat pada hold aslinya.

**Hold baru vs replay.** Hold BARU membaca ulang status item saat
ini: item yang diarsipkan sesudah draft dibuat → 409 (berbeda dengan
replay). Hold baru untuk periode draft yang sudah berakhir
(`ends_at <= cutoff`) → 409; `starts_at` TIDAK wajib di masa depan
— periode yang sedang berjalan boleh ditahan. Item aset harus sama
dengan item draft (beda → 409). Lazy expiry di bawah lock membalik
hold basi milik workspace itu (`held` + `expires_at <= cutoff` →
`expired`, `updated_at` = cutoff) sebelum INSERT; bila insert/audit
gagal, cleanup ikut rollback. Pelanggaran EXCLUDE (SQLSTATE 23P01
pada `rental_holds_no_overlap`) → 409 `unit tidak tersedia pada
periode itu`; galat lain → 500 tanpa detail SQL.

**Audit.** `asset_registered` (entity_type `physical_asset`),
`hold_created` dan `hold_released` (entity_type `rental_hold`,
details hanya id draft/aset) ditulis dalam transaksi yang sama
dengan mutasi; gagal audit → rollback penuh (500). Lazy expiry
TIDAK menulis audit: itu transisi waktu turunan, bukan aksi aktor;
status + `updated_at` baris adalah catatannya. Release yang
menemukan hold efektif kedaluwarsa boleh mem-persist expiry-nya
dalam transaksi yang sama, tanpa audit release.

**Konkurensi.** Workspace lock menyerikan semua mutasi hold dalam
satu workspace: dua staf, aset + periode sama, kunci berbeda → satu
201 + satu 409, satu audit. Kunci sama paralel → satu 201 + satu
200. EXCLUDE adalah backstop independen untuk penulis yang melewati
lock (termasuk SQL langsung). Create vs deactivate/revoke
diserialkan urutan lock seperti draft. Tes expiry dekat cutoff
dengan lock wait membuktikan TTL dijangkar SESUDAH lock: permintaan
yang dimulai sebelum deadline, menunggu lock, dan mendapat lock
sesudah deadline → hold lama tidak lagi memblokir (201, bukan 409).

**Yang belum ada:** snapshot agreed amount yang mengikat + transisi
draft (slice harga F2-01-D baru menambah penjadwalan `price_versions`
+ quote tak mengikat — lihat F2-01-D — bukan harga yang disepakati),
perjanjian mengikat, handover, retur/rusak/hilang, finance,
stock ledger / stok kuantitas / stok fraksional, status maintenance/
retired + condition history, lokasi/cabang, UI, dan pemberian
entitlement nyata/billing. "Rental bekerja" operasional masih
membutuhkan slice agreed amount, handover, dan retur.

## F2-01-D — penjadwalan tarif finite + quote tak mengikat (migrasi 008)

Migrasi `008_price_versions` menambah tabel `price_versions` milik
bootstrap dengan RLS (FORCE). Satu baris `price_versions` adalah
satu interval tarif finite `[valid_from, valid_to)` untuk satu
`catalog_items` pada `price_list_id` = `'standard'`, dengan
`amount_minor` BIGINT (integer minor, tanpa float) dan `currency`
kode 3 huruf ASCII `[A-Z]{3}` yang wajib diisi — tanpa default
server, tanpa klaim ISO tervalidasi, tanpa konversi. `recorded_at`
adalah SATU-SATUNYA fakta temporal baris ini (tidak ada kolom
`created_at`/`updated_at`); API mengikatnya ke cutoff
`clock_timestamp()` yang ditangkap sesudah workspace lock.
`valid_from` DAN `valid_to` keduanya NOT NULL dan finite
(ditegakkan CHECK bernama `price_versions_finite_bounds`:
`isfinite(valid_from) AND isfinite(valid_to)`; rerun 008 memasang
CHECK ini pada tabel 008 lama — bila ada baris nonfinite yang sudah
ter-commit, rerun gagal eksplisit dan tidak mengubah data): tidak ada
harga open-ended di slice ini (v1 dengan `valid_to` NULL tidak bisa
digantikan karena interval tak berbatas selalu overlap dengan versi
berikutnya). Koreksi umum tidak ada: yang diizinkan hanya interval
baru yang berdekatan (`valid_from` == `valid_to` interval
sebelumnya — "schedule a new tariff interval") atau tidak
bersinggungan; interval yang overlap → 409. Versi harga BUKAN harga
yang disepakati (agreed amount), BUKAN snapshot yang disimpan, BUKAN
pengikat harga, dan BUKAN transisi status draft. Quote dihitung dari
versi ini pada saat dibaca dan tidak disimpan; byte respons quote
bukan fakta yang disepakati. FK komposit tenant (`workspace_id,
catalog_item_id` → `catalog_items`) menolak tautan lintas-tenant.
Non-overlap dijaga constraint `price_versions_no_overlap`:
`EXCLUDE USING gist (workspace_id WITH =, catalog_item_id WITH =,
price_list_id WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)`
(butuh ekstensi `btree_gist`, dibuat oleh migrasi lewat akun
bootstrap). Runtime hanya SELECT + INSERT; baris/tarif/batas versi
lama tidak berubah lewat operasi runtime apa pun. Tanpa UPDATE,
DELETE, TRUNCATE.

Tiga rute terautentikasi, semua di bawah `authMiddleware` +
`beginTenant` dengan envelope error Bahasa Indonesia yang datar:

- `POST /workspaces/{id}/catalog/items/{item_id}/prices` — body
  strict JSON 16 KiB `{"valid_from":"...","valid_to":"...",
  "amount_minor":"150000","currency":"IDR"}` + header
  `Idempotency-Key` wajib (tepat satu nilai, case-sensitive).
  `amount_minor` WAJIB string desimal kanonis (`"0"` atau
  `[1-9][0-9]*`, maks `"9223372036854775807"`, tanpa leading zero,
  tanda, whitespace, fraksi, eksponen, null, atau JSON number —
  angka JSON gagal di decode); `currency` tepat 3 huruf A-Z, tanpa
  trim/default. Tanggal wajib RFC3339 zona eksplisit, tahun input
  DAN tahun ternormalisasi UTC 1–9999, `valid_to > valid_from`.
  Administrasi harga adalah master data core: `manage_business_data`
  (owner + manager, disetujui tiket ini untuk administrasi harga
  ini) + cek `view_workspace` eksplisit; TANPA syarat aktivasi/
  entitlement modul. Item hilang/lintas-tenant → 404, item own
  archived → 409 (hanya untuk jadwal BARU; replay kunci lama tetap
  200). 201 mengembalikan baris versi.
- `GET /workspaces/{id}/catalog/items/{item_id}/prices` — list
  `view_workspace`, pagination seperti F2-01-A, urutan
  `(valid_from, id)`. Item hilang/lintas-tenant → 404; item own
  yang diarsipkan tetap bisa di-list (histori harga terbaca).
- `GET /workspaces/{id}/rental/contracts/{draft_id}/quote` —
  `view_workspace` saja (owner/manager/kasir), tanpa lock, tanpa
  tulis/audit, tanpa syarat instalasi/entitlement Rental. Versi
  efektif dipilih pada `starts_at` draft tersimpan (`valid_from <=
  starts_at < valid_to`, daftar `'standard'`); draft hilang/
  lintas-tenant → 404, tanpa versi efektif → 404 `harga efektif
  tidak ditemukan`. Respons: `{draft_id, catalog_item_id,
  price_version_id, valid_from, valid_to, recorded_at,
  amount_minor, currency, unit:"24h", units, total_minor,
  starts_at, ends_at}` — uang string, `units` JSON number,
  tanggal UTC RFC3339Nano. Quote tetap tersedia untuk item yang
  diarsipkan dan sesudah revoke/deactivate. Quote TIDAK
  mengotorisasi penyewaan.

**Guard create (di dalam handler, bukan helper terpisah).** Satu
transaksi READ COMMITTED: otorisasi `manage_business_data` → lock
baris workspace yang sama dengan B3 (`FOR UPDATE`, sesudah
otorisasi, dipegang sampai commit/rollback) → tangkap SATU
`clock_timestamp()` dalam pernyataan terpisah SESUDAH lock →
cek `view_workspace` eksplisit → lookup kunci idempotency di bawah
lock → cek referensi item → cek no-backdate → cek overlap → INSERT
+ audit → commit. Galat query guard → 500 terkendali.

**Tanpa backdating.** `valid_from` yang sudah lewat pada cutoff
(jam DB sesudah lock) → 400, tanpa baris/audit. Tidak ada grace
period, clamping, atau pengisian historis; alur backdate/retroaktif
khusus tetap pekerjaan masa depan yang dibekukan terpisah. Replay
didahulukan SEBELUM cek no-backdate: kunci sama + payload
ternormalisasi sama → 200 baris asli yang tak berubah SEKALIPUN
`valid_from` kini sudah lewat; kunci sama + payload beda → 409.
Tes lock-wait cutoff membuktikan cutoff dijangkar SESUDAH lock:
permintaan yang dimulai saat `valid_from` masih di masa depan,
menunggu lock, dan mendapat lock sesudah `valid_from` lewat → 400.

**Aritmetika eksak.** Tarif = `amount_minor` per unit 24 jam waktu
berlalu dari instant UTC ternormalisasi draft; unit = ceil exact
integer dari selisih UnixMicro (`diff_us / 86400000000` + 1 bila
ada sisa), minimum 1 unit; total = rate × unit dengan pemeriksaan
overflow SEBELUM perkalian. `time.Duration`/`Sub` dan float
Hours/Ceil dilarang (Sub jenuh ~292 tahun; selisih UnixMicro
maksimum 315537897599999999 < MaxInt64; units maksimum 3652059).
Total di luar batas int64 → 422 `total di luar batas yang dapat
diwakili` (bukan 500). Rate 0 → total `"0"`. Tanpa prorata per jam,
tanpa pembulatan kalender, tanpa zona waktu workspace, tanpa
pembagian 100. Contoh sintetis: rate `"150000"`, 72 jam → 3 unit →
total `"450000"`; 49 jam → 3 unit; 24 jam + 1 mikrodetik → 2 unit.

**Replay & overlap.** Lookup kunci terjadi SEBELUM cek arsip,
no-backdate, dan overlap. Kunci sama + item/tanggal/amount/currency
ternormalisasi yang sama → 200 baris asli (tanpa tulis/audit/
perubahan timestamp); kunci sama + payload beda → 409. Overlap
dicek di bawah lock; pelanggaran EXCLUDE (SQLSTATE 23P01 pada
`price_versions_no_overlap`) → 409 `interval harga bertabrakan`;
galat lain → 500 tanpa detail SQL.

**Audit.** `price_version_created` (entity_type `price_version`,
details hanya `catalog_item_id`) ditulis dalam transaksi yang sama
dengan INSERT; gagal audit → rollback penuh (500). Quote dan list
tidak menulis audit.

**Konkurensi.** Workspace lock menyerikan semua create versi harga
dalam satu workspace: dua create yang overlap → satu 201 + satu
409, satu audit. Kunci sama paralel → satu 201 + satu 200.
EXCLUDE adalah backstop independen untuk penulis yang melewati
lock (termasuk SQL langsung).

**Yang belum ada:** snapshot agreed amount yang mengikat + transisi
draft (gate berikutnya, tiket terpisah), handover, retur/rusak/
hilang, finance/invoice/pembayaran, stock ledger/kuantitas,
perhitungan pajak/biaya/deposit, administrasi multi `price_list`,
konversi mata uang, harga open-ended, koreksi harga umum, alur
backdate/retroaktif, framework billing/bitemporal luas, kondisi/
maintenance/lokasi, UI, otoritas grant nyata/entitlement. Tidak ada
yang dibangun di sini.

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
