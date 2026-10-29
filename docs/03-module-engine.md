# 03 — Adaptive Module Engine Specification

## Tipe

1. **Core services**: identity, workspace, audit, corrections, catalog references, permissions. Selalu ada; tidak dapat uninstall.
2. **Capabilities**: operasi stabil seperti `inventory.stock.read`, `rental.contract.write`, `booking.resource.hold`.
3. **Families**: rental, inventory, finance, spa, restaurant, cinema, karaoke, billiard, payroll.
4. **Editions**: basic/advanced; pada family tertentu **mutually exclusive**, menggunakan `exclusive_group`.
5. **Industry bundles**: komposisi edisi modul yang berhak diaktifkan, harga, default config; tidak membuat backend logic baru.

## Manifest contract

`schemas/module-manifest.schema.json` mengatur atribut identitas, versi semver, family, edition, provides, requires, conflicts, migrations, domain_ownership, simulation datasets, security constraints, dan UI labels. Contoh pada `examples/`.

Modul yang punya `provides` tidak otomatis memberi capability ke pengguna. Hak efektif adalah irisan: installed and healthy, purchased/trial entitlement, workspace permission, user role, branch scope, resource-level policy, feature flag.

## Lifecycle state machine

```text
not_installed -> planning -> awaiting_approval -> installing -> active
active -> planning_upgrade -> migrating -> active
active -> suspension_pending -> suspended/read_only -> reactivating -> active
active -> planning_downgrade -> migrating -> active
installing/migrating -> failed_recoverable -> retry or manual_resolution
```

Setiap transisi dicatat audit. `uninstall` awal berarti deactivation + retention, bukan cascade delete. Data yang hanya dimengerti advanced tetap tersimpan, read-only jika downgraded, dan bisa dibaca kembali jika upgraded.

## Dependency resolver

- Directed graph atas family + capabilities + semver constraints.
- Reject cycle, unresolved required capability, duplicate exclusive_group, incompatible schema, insufficient license, incompatible edition, atau modul yang mengganggu running workflows.
- Dependensi *headless* diperbolehkan: Rental memerlukan `catalog.item.read`, bukan seluruh UI Inventory.
- Dependency lock snapshot per workspace; upgrade melakukan dry run terhadap historical data dan active operations.
- Aturan `provides` hanya boleh dideklarasikan oleh modul yang tervalidasi, bukan self-asserted untrusted plugin.

## Workflow per change

1. PLAN: authorize admin action; cek dependensi, entitlement, running transactions, version constraints.
2. SNAPSHOT: buat recoverable checkpoint/manifest; bandingkan free space, backup status dan consistency.
3. DRY-RUN: clone logical data yang diperlukan ke isolasi; jalankan migrasi schema/data, verify row counts, hashes, invariants.
4. DIFF: jelaskan data yang bertambah, akses fitur yang disembunyikan, laporan yang berubah, endpoint deprecated, harga paket.
5. APPROVE: pengguna berhak setuju/batal. Beri warning terhadap consequence yang tidak reversibel.
6. EXECUTE: maintenance gate untuk workflow terdampak, lease + idempotency, migrasi batch, health check.
7. ACTIVATE: capability & UI registry update atomik menurut workspace state; outbox for integration notifications.
8. RECOVER: failed activation tidak dianggap aman hanya karena snapshot; rencanakan forward fix/reverse migration sesuai data dan efek eksternal.

## UI beginner-facing strings

- Install -> **Aktifkan Fitur**
- Upgrade -> **Tambah Kemampuan**
- Downgrade -> **Gunakan Versi Sederhana**
- Branch -> **Ruang Percobaan**
- Diff -> **Lihat Perubahan**
- Conflict -> **Fitur Tidak Cocok**
- Checkpoint -> **Titik Aman**

## Examples

- Finance Basic to Advanced: derive ledgers from verified source, mark unresolved historical records, never claim SAK compliant without complete prerequisites.
- Advanced to Basic: keep journal history in read-only archive, disable posting advanced transactions, account for API consumers.
- Spa: optional `display_name_mode` = `full_name`/`staff_code` per monitor; per-user access; package definitions effective-dated and permissioned.
- Cinema/Karaoke/Billiard/Spa: compose generic booking, resource, staff scheduling, taxes, FnD; per-industry adapters own concurrency/pricing rules.

## Bundle versioning

Bundle manifest references immutable module version ranges; at checkout compute actual chosen versions + locked price entitlement. Owner changing bundle does not silently retroactively change existing customer contracts. Proration/tariffs depend on billing rules.
