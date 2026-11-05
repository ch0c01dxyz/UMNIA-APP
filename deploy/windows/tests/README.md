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

## Upgrade: `mock-docker-faithful.ps1` + `test-upgrade-scenarios.ps1`

Skenario regresi untuk `../Upgrade-UmniaVolume.ps1` (F1-UPGRADE-SAFETY-REVIEW).
Mock `docker` di sini BUKAN canned-success: ia mengeksekusi binary
postgres image asli dan API asli terhadap cluster nyata (fixture legacy
dengan `POSTGRES_USER=umnia_business` saja, tanpa `umnia_bootstrap`).
Yang masih di-mock dan harus dilabeli sebagai itu: transport Docker
itu sendiri, staging `/tmp` container, service `web` (flag saja), dan
HTTP PowerShell (didelegasikan ke curl, semua panggilan dicatat).

```powershell
pwsh -NoProfile -File deploy/windows/tests/test-upgrade-scenarios.ps1 -Scenario fixed-negative
pwsh -NoProfile -File deploy/windows/tests/test-upgrade-scenarios.ps1 -Scenario all
```

Skenario: `baseline-mf1`, `baseline-mf2-equal`,
`baseline-mf2-existing-target`, `baseline-mf3-writer`,
`baseline-mf4-probe` (reproduksi cacat pada baseline 599e40e;
baseline disimpan di `baseline-Upgrade-UmniaVolume.ps1`),
`fixed-happy`, `fixed-negative`, `fixed-failure-after-down`,
`fixed-isolated-api` (perbaikan saat ini). `fixed-isolated-api` harus
jalan selagi API target dari `fixed-happy` masih hidup. Prasyarat sama
seperti di atas, ditambah pwsh, binary `business-api` (dibangun otomatis
bila belum ada di `tests/bin/`, gitignored), dan `manage-test-env.sh`
untuk membangun fixture. `docker run`/`compose up` di host Windows
tetap UNVERIFIED — harness ini membuktikan logika skrip + mekanika
postgres/API, bukan runtime Windows.
