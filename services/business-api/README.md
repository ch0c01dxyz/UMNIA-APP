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

## Yang sengaja TIDAK ada di F1-01

Auth, workspace, RBAC/RLS, database, RustFS, modul, finance — masuk F1-02
dan seterusnya sesuai `FAST_TRACK.md`. Linter khusus web (ESLint) belum
dipasang; lint F1-01 = gofmt + go vet (API) + build Nuxt (web).
