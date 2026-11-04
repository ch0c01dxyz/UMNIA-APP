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

## Yang sengaja TIDAK ada di F1-01/F1-02

Auth, workspace, RBAC/RLS, modul, finance — masuk F1-03 dan seterusnya
sesuai `FAST_TRACK.md`. Linter khusus web (ESLint) belum dipasang;
lint = gofmt + go vet (API) + build Nuxt (web).
