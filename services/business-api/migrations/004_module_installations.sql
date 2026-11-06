-- F1-04B-B1: penyimpanan status instalasi modul + snapshot entitlement.
-- HANYA penyimpanan (storage): tidak ada endpoint aktivasi/deaktivasi,
-- tidak ada grant nyata, tidak ada capability guard, tidak ada resolver.
-- Kedua tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada kedua tabel.
-- Idempotent: CREATE TABLE IF NOT EXISTS, kebijakan diganti dalam SATU
-- pernyataan DO (atomik), GRANT/REVOKE idempotent, seed dan marker
-- memakai ON CONFLICT DO NOTHING — supaya penerapan ulang (mis.
-- configure-postgres.sh yang menjalankan semua migrasi tiap kali,
-- maupun psql TANPA -1 di Linux native) mempertahankan data,
-- timestamp, dan seed yang sudah ada. Jangan bungkus dengan
-- BEGIN/COMMIT di file ini: pemanggil Windows/CI sudah membungkus
-- semua file dalam satu transaksi psql -1; BEGIN di sini akan
-- memutus transaksi luar itu.
CREATE TABLE IF NOT EXISTS module_installations (
  workspace_id   UUID NOT NULL REFERENCES workspaces(id),
  module_id      TEXT NOT NULL,
  version        TEXT NOT NULL,
  status         TEXT NOT NULL CHECK (status IN ('active', 'inactive')),
  activated_at   TIMESTAMPTZ,
  deactivated_at TIMESTAMPTZ,
  activated_by   UUID REFERENCES identity_subjects(id),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (workspace_id, module_id),
  FOREIGN KEY (module_id, version) REFERENCES module_registry (module_id, version)
);

-- Snapshot entitlement saat ini (bukan billing, bukan riwayat
-- pencabutan, bukan otoritas operator): satu baris per workspace+modul.
-- Baris kosong di-seed: tidak ada yang bisa diaktifkan sebelum operator
-- memberi grant lewat prosedur yang dibekukan di luar B1.
CREATE TABLE IF NOT EXISTS module_entitlements (
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  module_id    TEXT NOT NULL,
  version      TEXT NOT NULL,
  granted_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  granted_by   TEXT NOT NULL,
  source       TEXT NOT NULL,
  reason       TEXT,
  PRIMARY KEY (workspace_id, module_id),
  FOREIGN KEY (module_id, version) REFERENCES module_registry (module_id, version)
);

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity). FORCE penting karena tabel dimiliki bootstrap
-- (superuser melewati RLS), sedangkan role runtime harus tetap tersaring.
ALTER TABLE module_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_installations FORCE ROW LEVEL SECURITY;
ALTER TABLE module_entitlements ENABLE ROW LEVEL SECURITY;
ALTER TABLE module_entitlements FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan mutasi sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT / FOR UPDATE): kebijakan permissive di-OR-kan
-- hanya dalam perintah yang sama, jadi kebijakan baca membership tidak
-- bisa menjadi otorisasi mutasi. UPDATE memakai USING (baris lama) DAN
-- WITH CHECK (baris hasil) yang sama-sama mengikat workspace konteks
-- + izin manage_modules, sehingga baris tidak bisa dipindah tenant.
DO $$
BEGIN
  DROP POLICY IF EXISTS installations_select ON module_installations;
  CREATE POLICY installations_select ON module_installations FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    );
  DROP POLICY IF EXISTS installations_insert ON module_installations;
  CREATE POLICY installations_insert ON module_installations FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_modules')
    );
  DROP POLICY IF EXISTS installations_update ON module_installations;
  CREATE POLICY installations_update ON module_installations FOR UPDATE
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_modules')
    )
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_modules')
    );
  -- Entitlements: hanya SELECT untuk anggota; TIDAK ADA kebijakan
  -- INSERT/UPDATE/DELETE, jadi RLS menolak semua DML runtime di sini
  -- (ditambah penolakan di level GRANT di bawah). Maintenance operator
  -- lewat jalur bootstrap istimewa, bukan lewat RLS aplikasi.
  DROP POLICY IF EXISTS entitlements_select ON module_entitlements;
  CREATE POLICY entitlements_select ON module_entitlements FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    );
END $$;

-- Hak runtime: instalasi boleh SELECT/INSERT/UPDATE (tanpa DELETE);
-- entitlement hanya SELECT. REVOKE ALL dulu supaya set hak selalu sama
-- persis dengan yang diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON module_installations FROM umnia_business;
GRANT SELECT, INSERT, UPDATE ON module_installations TO umnia_business;
REVOKE ALL PRIVILEGES ON module_entitlements FROM umnia_business;
GRANT SELECT ON module_entitlements TO umnia_business;

-- Izin manage_modules hanya untuk owner (tiket beku B1; tidak ada
-- grant manager/kasir, tidak ada desain ulang role).
INSERT INTO role_permissions (role, permission) VALUES ('owner', 'manage_modules')
ON CONFLICT (role, permission) DO NOTHING;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 004.
INSERT INTO schema_migrations (version) VALUES ('004_module_installations')
ON CONFLICT (version) DO NOTHING;
