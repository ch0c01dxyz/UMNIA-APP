# UMNIA — Technical Foundation Blueprint v0.6 — Fast-Track

**UMNIA** = UMKM Modular Nusantara Indonesia  
Status: **implementation in progress; see the verified checkpoint below**
Tanggal: 2026-10-25  
Bahasa: Indonesia; identitas teknis memakai bahasa Inggris untuk portabilitas.

## MULAI DI SINI

**Langsung baca [`FAST_TRACK.md`](FAST_TRACK.md)** dan [status implementasi terverifikasi](docs/development/current-status.md). Lanjutkan tugas aktif tanpa mengulang gate yang sudah diterima. Empat fase merupakan rencana eksekusi utama; P0–P8 dalam dokumen lama hanya referensi.

Pengerjaan berikutnya memakai [alur GitHub](docs/development/github-workflow.md): task branch, commit, push, review, dan CI pada SHA yang sama. Laporan yang dipertahankan tersedia di [indeks review](docs/reviews/README.md).

## Tujuan

Membentuk fondasi SaaS manajemen usaha UMKM yang modular, mudah dipakai pemula, dengan isolasi workspace, keamanan transaksi, audit, AI terbatas, simulasi terpisah, billing adaptif, serta perluasan modul industri.

## Isi paket

1. `docs/01-architecture.md` — boundary layanan, trust zones, data/event flow.
2. `docs/02-core-data.md` — domain model, invariant, harga efektif, contoh SQL.
3. `docs/03-module-engine.md` — kontrak modul/edisi/bundle, lifecycle, migrasi, konflik.
4. `docs/04-correction-engine.md` — human error, pembayaran, audit, payroll, reconciliation.
5. `docs/05-ai-simulation.md` — managed AI/BYOK, guardrails, dataset, import/export.
6. `docs/06-ux-api.md` — UI pemula, RBAC, API adaptif, akses mobile.
7. `docs/07-pricing-tax-operations.md` — harga modul, pajak, storage, backup/restore.
8. `docs/08-roadmap-tests.md` — ringkasan urutan pengerjaan, acceptance gates, benchmark.
9. `docs/09-advanced-anti-hallucination.md` — verifikasi tanpa membatasi kreativitas/penalaran; rules coding + product AI.
10. `docs/10-agent-orchestration.md` — peran frontier/Flash, kontrak task, review, escalation, starter tickets.
11. `docs/11-deployment-matrix.md` — **Windows Docker wajib**, **Linux native APT/systemd tanpa Docker**, optional Redis/PgBouncer.
12. `docs/12-design-system-ux.md` — OpenDesign MCP / Impeccable, workflow UX, tes visual dan aksesibilitas.
13. `docs/13-detailed-phase-execution.md` — pengarah ke rencana 4 fase; P0–P8 disimpan sebagai referensi di `docs/reference/`.
14. `AGENTS.md`, `DESIGN.md` — instruksi universal semua coding/design agents.
15. `ADR-013-optional-redis-pgbouncer.md` — keputusan scaling berdasarkan hasil benchmark.
16. `schemas/agent-task.schema.json`, `tasks/*.json` — kontrak task packet dan contoh yang bisa divalidasi.
17. `schemas/module-manifest.schema.json` — JSON Schema kontrak manifest.
18. `examples/*.module.json` — contoh manifest Rental, Finance Basic/Advanced, Spa.
19. `sql/001_core_reference.sql`, `sql/002_correction_reference.sql` — **contoh skema parsial**, tidak siap langsung migrasi produksi.

## Revisi v0.5 (keputusan baru)

- **AAHE**: anti-halusinasi tingkat lanjut dengan label bukti (`OBSERVED`, `OFFICIAL`, `TESTED`, `INFERRED`, `PROPOSED`, `UNKNOWN`), bukti test/diff nyata, dan ruang kreatif yang luas sebagai proposal.
- **Orkestrasi**: frontier leader/architect/reviewer, Flash agent worker untuk tugas kecil dan verifiable; high-risk coding by frontier bila dibutuhkan. Template task, checkpoint review, independensi QA.
- **Redis**: opsional dan tidak wajib pada P0; PostgreSQL transactional outbox + worker menjadi default.
- **PgBouncer**: opsional dan tidak wajib pada P0; connection pooling dari Go lebih dulu; RLS tenant-context test wajib bila transaction pooling dipakai.
- **Windows dev**: semua layanan melalui Docker Compose. **Linux**: native APT/systemd, tanpa Docker (RustFS native binary dengan systemd).
- **UI**: sertakan workflow dengan Impeccable atau OpenDesign MCP jika tool tersedia, dengan `DESIGN.md` wajib dan QA visual.

## Architecture Decision Records (ringkas)

- ADR-001: **Go modular monolith**, domain modules internal; jangan langsung microservices.
- ADR-002: **PostgreSQL** transaksi; RustFS objek; penyimpanan diabstraksikan lewat adapter; production RustFS diarahkan ke Linux.
- ADR-003: **Admin Control Plane** mempunyai database dan identitas admin terpisah dari pengguna bisnis. Tidak berbagi DB credential.
- ADR-004: **RLS dan policy service** diwajibkan bersama; setiap query bisnis ber-scope workspace.
- ADR-005: Module registry = family/edition/capability; bundle adalah komposisi, bukan duplikasi backend.
- ADR-006: Semua uang disimpan sebagai **minor unit integer** atau tipe decimal yang ketat; mata uang eksplisit; tanpa float dalam perhitungan uang.
- ADR-007: Pembayaran/jurnal posted tidak ditimpa. Koreksi melalui compensating actions, approval, dan ledger.
- ADR-008: AI terkelola ke `akanesia-router` di konfigurasi deployment; **BYOK langsung ke provider adapter**, bukan router, tetapi dua jalur dibatasi input-policy, response validation, tools, dan audit.
- ADR-009: Simulasi membaca copy/synthetic dataset; **tidak punya credential/route produksi**.
- ADR-010: Event delivery menggunakan transactional outbox + idempotent consumers; bukan asumsi exactly-once.
- ADR-011: API workspace dibatasi capability, edition, role, dan entitlements; route server tidak dihapus fisik.
- ADR-012: Rencana peluncuran melalui vertical slice Rental+Inventory+Customer; perluas belakangan.
- ADR-013: Redis dan PgBouncer ditunda sampai ada metrik yang membenarkan; RLS transaction local dan compatibility testing wajib jika PgBouncer diaktifkan.
- ADR-014: Windows wajib Docker Compose; Linux tanpa Docker, native services.
- ADR-015: Frontier-first orchestration + Flash-bounded tasks; independent review/test evidence.
- ADR-016: UI wajib mengikuti DESIGN.md + visual QA; Impeccable/OpenDesign adalah bantuan desain yang harus diverifikasi bila tersedia.

## Non-negotiable safety gates

- Isolasi tenant diuji dengan akses cross-workspace berulang (IDOR, SQL, storage, jobs, AI, export).
- Koreksi tidak menghilangkan bukti transaksi/payment yang sudah posted.
- Deaktivasi modul tidak melakukan cascade delete pada data historis.
- Semua AI tool execution memerlukan policy check independen dari model.
- Data simulasi tidak dapat mengeksekusi pembayaran, pesan keluar, webhook, atau mutasi database produksi.
- Legal rules (pajak/payroll) dikonfigurasi bertanggal efektif dan **harus diverifikasi** sebelum digunakan produksi.

## Batasan & status

Dokumen ini adalah spesifikasi untuk implementasi oleh agent dan engineering team. JSON Schema dan contoh JSON **perlu divalidasi pada setiap rilis blueprint**; lihat laporan validasi v0.5. SQL adalah referensi awal, belum dieksekusi pada PostgreSQL produksi. Tidak ada kode aplikasi atau benchmark riil dalam paket ini. Target waktu, kapasitas, serta RPO/RTO belum dikunci karena perangkat dan pola trafik belum ditentukan.

## Rujukan

- https://www.postgresql.org/docs/current/ddl-rowsecurity.html
- https://www.postgresql.org/docs/current/continuous-archiving.html
- https://docs.rustfs.com/en/reference/s3-compatibility
- https://docs.rustfs.com/en/installation
- https://spec.openapis.org/oas/
- https://web.iaiglobal.or.id/Berita-IAI/detail/standar_akuntansi_keuangan_indonesia_untuk_entitas_mikro_kecil_dan_menengah
- https://jdih.kemnaker.go.id/peraturan/detail/2802/peraturan-pemerintah-nomor-49-tahun-2025

## Cara mengerjakan

Mulai dari `AGENTS.md` → `docs/09-advanced-anti-hallucination.md` → `docs/10-agent-orchestration.md` → `docs/13-detailed-phase-execution.md`. Frontier leader membekukan kontrak P0-01, membagikan task packet bounded ke Flash workers dan reviewer, menjalankan QA gate per milestone. Setup Windows wajib Docker Compose; Linux native APT/systemd tanpa Docker. Jangan menganggap SQL contoh sebagai migrasi final.

### Referensi baru yang diverifikasi untuk v0.5

- https://www.pgbouncer.org/features.html
- https://www.pgbouncer.org/faq.html
- https://redis.io/docs/latest/develop/use-cases/
- https://docs.rustfs.com/en/installation/linux/quick-start
- https://github.com/nexu-io/open-design
- https://github.com/pbakaus/impeccable

## Revisi v0.6 Fast-Track

- Empat fase saja, dengan **MVP rental** sebagai vertical slice pertama.
- Perencanaan secukupnya lalu langsung implementasi; QA berbasis tes, bukan laporan panjang.
- Keamanan fondasi dan integritas uang wajib dari hari pertama fitur, tidak boleh ditunda.
- Dokumen v0.5 dipertahankan sebagai referensi, `FAST_TRACK.md` menjadi sumber urutan kerja yang otoritatif.
