# 02 — Core Data Contract & Invariants

## Tenancy & identity

- `identity_subjects`: akun business user; satu orang dapat memiliki membership di beberapa workspace.
- `workspaces`: organisasi usaha; `owner_subject_id` dan aturan maksimum workspace per pengguna dari admin control plane.
- `workspace_memberships`: `(workspace_id,subject_id)` unik; status pending/active/suspended; role assignment scoped.
- `roles`, `permissions`, `role_permissions`, `membership_roles`; mendukung pemilik, penanggung jawab, supervisor, manager, kasir, terapis; pemindahan jabatan menggunakan effective dates dan audit.
- Admin identities berada pada **DB berbeda**, tidak tercampur dengan data employee/business users.

## Master identifiers

- `party`: legal entity/customer/supplier dengan tipe dan kontak; PII dilindungi.
- `catalog_item`: satu canonical item id, unit, SKU workspace-unique, status.
- `physical_asset`: unit fisik serializable yang mengacu item; condition history.
- `location`: lokasi/stok/tempat penyimpanan; dapat dipakai gudang, cabang, atau rental.
- `resource`: ruangan karaoke, meja billiard, kursi bioskop, ruang spa, terapist schedule; tipe dan atribut khusus pada extension.
- `reservation`: start/end timezone-aware, hold TTL dan state; hindari overlap menurut rule resource.

## Historical price & tax

- `price_versions`: `(workspace_id,item_id,price_list_id,valid_from,valid_to,amount_minor,currency,recorded_at)`; interval `[from,to)` tidak boleh overlap dalam scope yang sama.
- `business_rule_versions`: formula/tax/pricing/commission dengan `effective_at`, `recorded_at`, `source_evidence`, `approval`.
- Order/contract menyimpan `pricing_rule_version_id` **dan agreed unit amount**. Harga master yang diperbaiki tidak mengubah kontrak posted secara diam-diam.
- Kebutuhan bitemporal: simpan kapan berlaku (`valid_time`) dan kapan sistem mengetahui (`recorded_at`); backdating memerlukan hak akses khusus.

## Stock semantics

Inventory movement harus mereferensikan dokumen asal, event id dan identitas item/aset. Hindari mutasi `current_quantity` tanpa stock ledger. Quantity dapat bernilai pecahan untuk resep; gunakan decimal fixed precision. Definisikan conversion map (g, kg, ml, liter, pcs) dengan unit type, toleransi, dan rounding eksplisit.

## Keuangan & pembayaran

- `sales_orders/contract`, `invoices`, `payments`, `payment_allocations`, `refunds`, `credit_notes` dipisah.
- `journal_entries`, `journal_lines`: mata uang, posted state, period, source document; debit/credit wajib balance.
- `period_closures`: periode akuntansi ditutup; posted revision immutable, koreksi lewat adjusted journal dan/atau reopening berizin sesuai kebijakan akuntansi.
- `tax_calculation_snapshot`: basis, tarif, formula version, kategori, lokasi, waktu, evidence; snapshot tidak berganti ketika tarif hari ini berubah.
- Semua transaksi memiliki `workspace_id` dan foreign key yang menyertakan konteks tenant atau enforcement service yang diuji mencegah cross-tenant FK.

## Correction & evidence

- `incident_cases` dibuat pada Core meski Finance Advanced/Payroll belum terpasang.
- `incident_evidence`: object metadata dan checksum SHA-256 di PostgreSQL, binary di RustFS. Evidence retention & privacy policy.
- `correction_proposals`: before/after values, dependencies, approvals, status.
- `correction_ledger`: linking source document -> adjustment/reversal/refund reference. Tidak delete bukti posted.
- `employee_liability_reviews`: bukti, status investigasi, aturan kerja yang relevan; terpisah dari `payroll_deductions`. Selisih transaksi != otomatis kesalahan/utang pegawai.

## Workspace module state

- `module_catalog` (platform admin origin), `module_installations` (workspace, edition, version, lifecycle, installed_at).
- `module_entitlements`: fitur yang berhak diakses berdasarkan pembelian/promosi, berbeda dari `module_installations`.
- `module_migration_jobs`: plan hash, checkpoint, simulation report, approval, resume token, rollback/forward recovery.
- `module_data_ownership`: core IDs stabil; modul memiliki tabel extension yang dapat dipertahankan read-only ketika edition diturunkan.

## Audit and data lifecycle

- `audit_events` append-only (actor, action, before/after reference, timestamp, causation id, request id, workspace id). Enkripsi/retensi/redaksi PII terkontrol.
- `outbox_events` diinsert atomik bersama perubahan bisnis, lalu di-ack oleh worker setelah efek penerima dianggap berhasil.
- `retention_policies`: by data type + legal constraints; “hapus modul” tidak berarti hapus data historis.
- `backups` dan `restore_jobs` berisi manifest versi schema+file+checksums; tidak menyimpan key langsung dalam arsip.

## Minimal SQL rujukan

Lihat `sql/001_core_reference.sql` dan `sql/002_correction_reference.sql` untuk contoh struktur database. **Belum dilengkapi seluruh constraint, index, perizinan, triggers, journal balanced-posted guarantee atau row-level grants.** Agent harus menambah migration dan tes sebelum produksi.
