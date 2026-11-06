-- F1-04A: registry katalog manifest modul (READ-ONLY untuk role runtime).
-- Tabel global seperti katalog referensi (roles/role_permissions): tanpa
-- RLS, karena isinya bukan data tenant. Akses HTTP tetap dijaga izin
-- view_workspace (lihat modules.go). Pemilik tabel adalah akun
-- bootstrap/migrasi yang menjalankan file ini (postgres di CI/Linux,
-- umnia_bootstrap di Windows); role runtime umnia_business hanya
-- mendapat SELECT — INSERT/UPDATE/DELETE ditolak.
-- Seed = empat contoh manifest yang ada, sebagai DEKLARASI (bukan bukti
-- layanan domain atau simulation dataset ada). rental.basic dan
-- spa.standard mendeklarasikan requires tanpa provider (lihat D1);
-- katalog tidak mengaktifkan apa pun.
-- Idempotent: CREATE TABLE IF NOT EXISTS + INSERT ... ON CONFLICT DO
-- NOTHING, supaya penerapan ulang (mis. configure-postgres.sh yang
-- menjalankan semua migrasi tiap kali) mempertahankan baris yang sudah
-- terdaftar tanpa menulis ulang versi yang ada.
CREATE TABLE IF NOT EXISTS module_registry (
  module_id     TEXT NOT NULL,
  version       TEXT NOT NULL,
  manifest      JSONB NOT NULL,
  registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (module_id, version),
  -- Identitas/versi kolom harus cocok dengan isi manifest itu sendiri.
  -- IS NOT NULL wajib: ->> menghasilkan SQL NULL untuk nilai JSON null
  -- maupun kunci yang hilang, dan CHECK menerima hasil NULL — tanpa
  -- penjaga ini, {"id":null} lolos.
  CHECK (manifest->>'id' IS NOT NULL
    AND manifest->>'version' IS NOT NULL
    AND manifest->>'id' = module_id
    AND manifest->>'version' = version)
);

-- Perbaiki CHECK lama pada database yang sudah menerapkan 003: CREATE
-- TABLE IF NOT EXISTS di atas tidak menyentuh tabel yang sudah ada,
-- jadi constraint diganti eksplisit di sini. Aman di-rerun (DROP ...
-- IF EXISTS + ADD kembali); baris valid yang sudah ada memenuhi
-- kondisi baru, sedangkan baris tidak valid (bila ada) membuat ADD
-- gagal eksplisit untuk review — tidak pernah ditulis ulang/dihapus
-- diam-diam. Di Windows/CI seluruh file berjalan dalam satu transaksi
-- psql -1, jadi DROP+ADD atomik; di Linux native tiap pernyataan
-- commit sendiri, tetapi rerun berikutnya mengulang ADD yang gagal.
ALTER TABLE module_registry DROP CONSTRAINT IF EXISTS module_registry_check;
ALTER TABLE module_registry ADD CONSTRAINT module_registry_check CHECK (
  manifest->>'id' IS NOT NULL
    AND manifest->>'version' IS NOT NULL
    AND manifest->>'id' = module_id
    AND manifest->>'version' = version
);

-- Hanya SELECT untuk runtime; tanpa GRANT INSERT/UPDATE/DELETE.
GRANT SELECT ON module_registry TO umnia_business;

INSERT INTO module_registry (module_id, version, manifest) VALUES
  ('finance.advanced', '0.1.0', $${
  "schema_version": "0.4",
  "id": "finance.advanced",
  "version": "0.1.0",
  "family": "finance",
  "edition": "advanced",
  "exclusive_group": "finance-edition",
  "display": {
    "name_id": "Keuangan Lengkap",
    "description_id": "Buku besar, neraca, jurnal dan rekonsiliasi"
  },
  "provides": [
    "finance.summary.read",
    "finance.cashflow.read",
    "finance.journal.post",
    "finance.discrepancy.view",
    "finance.discrepancy.export"
  ],
  "requires": [],
  "conflicts": [
    "finance.basic"
  ],
  "data_ownership": {
    "schemas": [
      "finance",
      "ledger"
    ],
    "downgrade_strategy": "retain_read_only"
  },
  "simulation_dataset": {
    "required": true,
    "schema_id": "finance-advanced-simulation-v1",
    "validation_profile": "double-entry-balance",
    "refresh_policy": "on_module_change_and_scheduled"
  },
  "security": {
    "risk_class": "high",
    "needs_approval_for_mutations": true
  },
  "ui": {
    "primary_nav": "Keuangan Lengkap"
  },
  "events": [
    "finance.journal.posted",
    "finance.discrepancy.recorded"
  ]
}$$::jsonb),
  ('finance.basic', '0.1.0', $${
  "schema_version": "0.4",
  "id": "finance.basic",
  "version": "0.1.0",
  "family": "finance",
  "edition": "basic",
  "exclusive_group": "finance-edition",
  "display": {
    "name_id": "Keuangan Sederhana",
    "description_id": "Catat uang masuk/keluar dan ringkasan operasional"
  },
  "provides": [
    "finance.summary.read",
    "finance.cashflow.read"
  ],
  "requires": [],
  "conflicts": [
    "finance.advanced"
  ],
  "data_ownership": {
    "schemas": [
      "finance"
    ],
    "downgrade_strategy": "retain_shared"
  },
  "simulation_dataset": {
    "required": true,
    "schema_id": "finance-basic-simulation-v1",
    "validation_profile": "cashflow-basic-rules",
    "refresh_policy": "on_module_change_and_scheduled"
  },
  "security": {
    "risk_class": "high",
    "needs_approval_for_mutations": true
  },
  "ui": {
    "primary_nav": "Keuangan"
  },
  "events": [
    "finance.summary.generated"
  ]
}$$::jsonb),
  ('rental.basic', '0.1.0', $${
  "schema_version": "0.4",
  "id": "rental.basic",
  "version": "0.1.0",
  "family": "rental",
  "edition": "basic",
  "exclusive_group": "rental-edition",
  "display": {
    "name_id": "Persewaan Sederhana",
    "description_id": "Kelola barang sewa, status dan pengembalian"
  },
  "provides": [
    "rental.contract.read",
    "rental.contract.write",
    "rental.return.manage"
  ],
  "requires": [
    {
      "capability": "catalog.item.read"
    },
    {
      "capability": "customer.profile.read"
    }
  ],
  "conflicts": [
    "rental.advanced"
  ],
  "data_ownership": {
    "schemas": [
      "rental"
    ],
    "downgrade_strategy": "retain_read_only"
  },
  "simulation_dataset": {
    "required": true,
    "schema_id": "rental-simulation-v1",
    "validation_profile": "rental-core-rules",
    "refresh_policy": "on_module_change_and_scheduled"
  },
  "security": {
    "risk_class": "medium",
    "needs_approval_for_mutations": false
  },
  "ui": {
    "primary_nav": "Persewaan"
  },
  "events": [
    "rental.contract.created",
    "rental.contract.returned"
  ]
}$$::jsonb),
  ('spa.standard', '0.1.0', $${
  "schema_version": "0.4",
  "id": "spa.standard",
  "version": "0.1.0",
  "family": "spa",
  "edition": "standard",
  "exclusive_group": "spa-edition",
  "display": {
    "name_id": "Manajemen Spa & Pijat",
    "description_id": "Reservasi layanan, ruang dan jadwal terapis"
  },
  "provides": [
    "spa.therapist.display_policy",
    "spa.packages.manage",
    "spa.packages.versioning",
    "spa.appointments.manage"
  ],
  "requires": [
    {
      "capability": "booking.resource.hold"
    },
    {
      "capability": "customer.profile.read"
    },
    {
      "capability": "inventory.stock.read",
      "optional": true
    }
  ],
  "conflicts": [],
  "data_ownership": {
    "schemas": [
      "spa"
    ],
    "downgrade_strategy": "retain_read_only"
  },
  "simulation_dataset": {
    "required": true,
    "schema_id": "spa-simulation-v1",
    "validation_profile": "multi-resource-scheduling",
    "refresh_policy": "on_module_change_and_scheduled"
  },
  "security": {
    "risk_class": "medium",
    "needs_approval_for_mutations": false
  },
  "ui": {
    "primary_nav": "Spa & Pijat",
    "display_policy": "therapist: staff_code default on public screens"
  },
  "events": [
    "spa.appointment.created",
    "spa.package.versioned"
  ]
}$$::jsonb)
ON CONFLICT (module_id, version) DO NOTHING;

INSERT INTO schema_migrations (version) VALUES ('003_modules')
ON CONFLICT (version) DO NOTHING;
