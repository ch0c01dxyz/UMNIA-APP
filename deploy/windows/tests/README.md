# tests/ — deployment regression harness (Linux)

`run-entrypoint.sh` menjalankan `docker-entrypoint.sh` dari image resmi
`postgres:16-bookworm` secara native (tanpa `docker run`, yang diblokir
di VM ini). Dipakai `../check-deployment.sh --container-test` dan oleh
laporan review F1-WINDOWS-REVIEW-FIXES.

## Prasyarat

- Rootfs image terekstrak di `$WINTEST_ROOTFS`
  (default `/var/tmp/umnia-harness/rootfs`): unduh image
  `postgres:16-bookworm` (registry API + `docker load`, atau `docker pull`
  bila daemon bisa mencapai registry), lalu ekstrak layer-layernya.
  Entrypoint image harus identik dengan sumber 16/bookworm
  (`docker-library/postgres`).
- User `postgres` di host + sudo; direktori scratch `$WINTEST_BASE`
  (default `/var/tmp/umnia-harness`) yang bisa ditulis user postgres.
- Tool `psql`/`pg_isready` image dipakai lewat PATH yang diset skrip.

## Pakai

```bash
deploy/windows/tests/run-entrypoint.sh <nama> <port> <POSTGRES_USER> <POSTGRES_DB> \
  <BOOTSTRAP_PW> [RUNTIME_ROLE] [RUNTIME_PW] [EXTRA_ENV=val ...]
# contoh: business, sourced mode (seperti checkout git Windows):
INITDB_SOURCED=1 deploy/windows/tests/run-entrypoint.sh t1 55432 \
  umnia_bootstrap umnia_business pw-bootstrap umnia_business pw-runtime
# hentikan server: kill <pid dari output>; wait <pid>
```

PGUSER/PGDATABASE sengaja TIDAK diset — skrip initdb harus bekerja
tanpa default tersembunyi. `INITDB_SRC` (dir initdb alternatif) dan
`INITDB_SOURCED=1` / `INITDB_EXECUTABLE=1` tersedia untuk fixture
lama dan kedua mode entrypoint (.sh di-source vs dijalankan).
