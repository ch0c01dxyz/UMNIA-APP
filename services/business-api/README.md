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
instalasi. Readiness `/readyz` kini mewajibkan keempat marker
001–004.

**Yang belum ada:** endpoint aktivasi/deaktivasi dan baca-state
(B3), grant entitlement nyata, resolver dependensi, dan capability
guard (F2-01). Penyimpanan ini saja tidak memberi kemampuan yang bisa
dieksekusi pada modul mana pun.

## Yang sengaja TIDAK ada di F1-03

UI login/workspace (web masih skeleton F1-01), mailer undangan (token
undangan dikembalikan sekali ke pengundang — di produksi harus lewat
email), logout/refresh token, dan endpoint aktivasi/deaktivasi modul,
grant entitlement nyata, serta capability guard (F1-04B ke atas;
F1-04A baru menyediakan katalog read-only, F1-04B-B1 baru menyediakan
penyimpanan instalasi/entitlement tanpa endpoint aktivasi, tanpa grant
nyata, tanpa guard). Linter khusus web (ESLint) belum dipasang; lint =
gofmt + go vet (API) + build Nuxt (web).
