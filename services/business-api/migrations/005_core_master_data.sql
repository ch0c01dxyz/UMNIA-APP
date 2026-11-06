-- F2-01-A: master data core (customers + catalog items).
-- HANYA penyimpanan core: tidak ada modul/instalasi/entitlement baru,
-- tidak ada grant nyata, tidak ada guard, tidak ada endpoint rental,
-- tidak ada stok/harga/finance, tidak ada supplier (type dikunci
-- 'customer' untuk slice ini; tanpa kolom kontak/email/telepon/notes —
-- PII yang tidak dibutuhkan tidak dikumpulkan).
-- Kedua tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada kedua tabel.
-- Runtime hanya SELECT + INSERT (hanya create/list yang ada): tanpa
-- kebijakan UPDATE/DELETE, jadi RLS menolak mutasi lain; tanpa grant
-- UPDATE/DELETE/TRUNCATE/kepemilikan.
-- Idempotent: CREATE TABLE IF NOT EXISTS, kebijakan diganti dalam SATU
-- pernyataan DO (atomik), GRANT/REVOKE idempotent, seed dan marker
-- memakai ON CONFLICT DO NOTHING — supaya penerapan ulang (mis.
-- configure-postgres.sh yang menjalankan semua migrasi tiap kali,
-- maupun psql TANPA -1 di Linux native) mempertahankan data,
-- timestamp, dan seed yang sudah ada. Jangan bungkus dengan
-- BEGIN/COMMIT di file ini: pemanggil Windows/CI sudah membungkus
-- semua file dalam satu transaksi psql -1; BEGIN di sini akan
-- memutus transaksi luar itu.
-- Invarian nonblank/panjang juga ditegakkan di level database (CHECK):
-- nilai harus mengandung satu karakter non-whitespace (regex \S) dan
-- tidak melebihi panjang maksimum, supaya INSERT langsung lewat role
-- runtime pun ditolak bila kosong/terlalu panjang. API tetap
-- memangkas whitespace dan memvalidasi sebelum INSERT.
CREATE TABLE IF NOT EXISTS parties (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  type         TEXT NOT NULL DEFAULT 'customer' CHECK (type = 'customer'),
  display_name TEXT NOT NULL CHECK (char_length(display_name) <= 200 AND display_name ~ '\S'),
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS catalog_items (
  id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  sku          TEXT NOT NULL CHECK (char_length(sku) <= 64 AND sku ~ '\S'),
  name         TEXT NOT NULL CHECK (char_length(name) <= 200 AND name ~ '\S'),
  unit         TEXT NOT NULL CHECK (char_length(unit) <= 32 AND unit ~ '\S'),
  status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
  revision     BIGINT NOT NULL DEFAULT 0,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, sku)
);

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity/004). FORCE penting karena tabel dimiliki bootstrap
-- (superuser melewati RLS), sedangkan role runtime harus tetap tersaring.
ALTER TABLE parties ENABLE ROW LEVEL SECURITY;
ALTER TABLE parties FORCE ROW LEVEL SECURITY;
ALTER TABLE catalog_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE catalog_items FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan INSERT sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT): kebijakan permissive di-OR-kan hanya dalam
-- perintah yang sama, jadi kebijakan baca membership tidak bisa
-- menjadi otorisasi mutasi. SELECT mensyaratkan tenant + membership
-- aktif + view_workspace; INSERT memakai WITH CHECK yang mensyaratkan
-- tenant + manage_business_data (yang sudah mensyaratkan membership
-- aktif lewat app.has_permission).
DO $$
BEGIN
  DROP POLICY IF EXISTS parties_select ON parties;
  CREATE POLICY parties_select ON parties FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS parties_insert ON parties;
  CREATE POLICY parties_insert ON parties FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
  DROP POLICY IF EXISTS catalog_items_select ON catalog_items;
  CREATE POLICY catalog_items_select ON catalog_items FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS catalog_items_insert ON catalog_items;
  CREATE POLICY catalog_items_insert ON catalog_items FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
END $$;

-- Hak runtime: kedua tabel boleh SELECT/INSERT saja (tanpa UPDATE,
-- DELETE, TRUNCATE). REVOKE ALL dulu supaya set hak selalu sama persis
-- dengan yang diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON parties FROM umnia_business;
GRANT SELECT, INSERT ON parties TO umnia_business;
REVOKE ALL PRIVILEGES ON catalog_items FROM umnia_business;
GRANT SELECT, INSERT ON catalog_items TO umnia_business;

-- Izin manage_business_data hanya untuk owner + manager (keputusan
-- leader pada tiket beku F2-01-A; bukan izin yang sudah ada sebelumnya,
-- dan bukan pemberian ke kasir).
INSERT INTO role_permissions (role, permission) VALUES
  ('owner', 'manage_business_data'),
  ('manager', 'manage_business_data')
ON CONFLICT (role, permission) DO NOTHING;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 005.
INSERT INTO schema_migrations (version) VALUES ('005_core_master_data')
ON CONFLICT (version) DO NOTHING;
