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
  sesudah trim); SKU sama di workspace lain boleh.
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
dan bukan "tersedia". Sukses bacaan itu sendiri bukan bukti RLS,
membership, izin efektif, kesehatan provider, atau enforcement
consumer: otorisasi divalidasi terpisah dan RLS diuji terpisah.
Aktivasi tetap entitled intent; consumer rental masa depan wajib
memeriksa ulang entitlement/state aktif dan memilih tiap customer/item
yang dirujuk di dalam transaksinya sendiri. Tidak ada guard helper
atau endpoint kontrak di slice ini.

**Yang belum ada:** stok/harga/finance, lifecycle dan endpoint kontrak
rental, UI, supplier, dan pemberian entitlement nyata/billing.

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
