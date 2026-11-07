-- F2-01-C: aset fisik + hold eksklusif terikat draft + ketersediaan.
-- HANYA penyimpanan aset/hold: satu baris physical_assets mencatat
-- satu unit fisik yang teridentifikasi (serial) dari satu catalog item;
-- satu baris rental_holds mencatat satu unit yang ditahan untuk satu
-- draft pada periode draft itu. Hold BUKAN harga, perjanjian, handover,
-- pembayaran, atau rental operasional; tidak ada stok kuantitas/ledger,
-- status maintenance/retired, atau condition history di slice ini.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada kedua tabel.
-- Aset runtime hanya SELECT + INSERT (registrasi/list): tanpa kebijakan
-- UPDATE/DELETE, jadi RLS menolak mutasi lain. Hold runtime SELECT +
-- INSERT + UPDATE KOLOM (status, updated_at) SAJA: hanya transisi
-- status held->released/expired yang diizinkan; workspace, draft,
-- aset, periode, kunci, expires_at, dan waktu pembuatan TIDAK bisa
-- diubah runtime. Tanpa grant DELETE/TRUNCATE/kepemilikan. Tidak ada
-- penghapusan history pada operasi modul.
-- Idempotent: CREATE TABLE IF NOT EXISTS, constraint UNIQUE
-- (workspace_id, id) ditambah hanya bila belum ada (satu blok DO),
-- kebijakan diganti dalam SATU pernyataan DO (atomik), GRANT/REVOKE
-- idempotent, marker memakai ON CONFLICT DO NOTHING — supaya penerapan
-- ulang (maupun psql TANPA -1 di Linux native) mempertahankan data,
-- timestamp, dan seed yang sudah ada. Jangan bungkus dengan
-- BEGIN/COMMIT di file ini: pemanggil Windows/CI sudah membungkus
-- semua file dalam satu transaksi psql -1; BEGIN di sini akan
-- memutus transaksi luar itu.
-- FK komposit tenant: rujukan draft/aset harus berada di workspace
-- yang sama dengan hold; rujukan item aset harus berada di workspace
-- yang sama dengan aset (docs/02: foreign key menyertakan konteks
-- tenant). Karena itu rental_contracts dan physical_assets mendapat
-- UNIQUE (workspace_id, id) dulu — selalu terpenuhi karena id adalah
-- PK — supaya FK komposit punya target unik. FK adalah backstop,
-- bukan jalur galat handler: handler memeriksa rujukan tenant-scoped
-- dulu (404 identik untuk hilang/lintas-tenant), pelanggaran FK akan
-- muncul sebagai 500 bila cek itu dilewati.
-- Overlap: kolom period tstzrange GENERATED ALWAYS AS
-- (tstzrange(starts_at, ends_at, '[)')) STORED + constraint EXCLUDE
-- parsial (hanya status='held') mencegah dua hold HIDUP tumpang
-- tindih pada unit yang sama di workspace yang sama. Interval
-- half-open '[)': periode bersebelahan TIDAK overlap. Butuh ekstensi
-- btree_gist (untuk kesetaraan UUID di dalam GiST); dibuat di sini
-- oleh akun bootstrap (superuser di semua jalur). TIDAK ADA predikat
-- berubah-waktu di dalam constraint: baris held yang kedaluwarsa
-- tetap berstatus held sampai sebuah tulis (lazy expiry di handler,
-- di bawah workspace lock) membaliknya; constraint hanya backstop.
-- Invarian client_request_id juga ditegakkan di level database (CHECK):
-- 1-128 karakter ASCII [huruf, digit, -, _, ., :] tanpa whitespace,
-- supaya INSERT langsung lewat role runtime pun ditolak bila kuncinya
-- tidak valid. API tetap memvalidasi sebelum INSERT. Serial aset juga
-- ditegakkan di level database (nonblank, maks 64 karakter).
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS physical_assets (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id    UUID NOT NULL REFERENCES workspaces(id),
  catalog_item_id UUID NOT NULL,
  serial          TEXT NOT NULL CHECK (char_length(serial) BETWEEN 1 AND 64 AND serial ~ '\S'),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, serial),
  FOREIGN KEY (workspace_id, catalog_item_id) REFERENCES catalog_items (workspace_id, id)
);

DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'physical_assets_workspace_id_id_key') THEN
    ALTER TABLE physical_assets ADD CONSTRAINT physical_assets_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rental_contracts_workspace_id_id_key') THEN
    ALTER TABLE rental_contracts ADD CONSTRAINT rental_contracts_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS rental_holds (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id      UUID NOT NULL REFERENCES workspaces(id),
  draft_id          UUID NOT NULL,
  asset_id          UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  status            TEXT NOT NULL DEFAULT 'held' CHECK (status IN ('held', 'released', 'expired')),
  starts_at         TIMESTAMPTZ NOT NULL,
  ends_at           TIMESTAMPTZ NOT NULL,
  expires_at        TIMESTAMPTZ NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  period            tstzrange GENERATED ALWAYS AS (tstzrange(starts_at, ends_at, '[)')) STORED,
  UNIQUE (workspace_id, client_request_id),
  CHECK (ends_at > starts_at),
  FOREIGN KEY (workspace_id, draft_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, asset_id) REFERENCES physical_assets (workspace_id, id),
  CONSTRAINT rental_holds_no_overlap EXCLUDE USING gist (workspace_id WITH =, asset_id WITH =, period WITH &&) WHERE (status = 'held')
);

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity/004/005/006). FORCE penting karena tabel dimiliki
-- bootstrap (superuser melewati RLS), sedangkan role runtime harus
-- tetap tersaring.
ALTER TABLE physical_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE physical_assets FORCE ROW LEVEL SECURITY;
ALTER TABLE rental_holds ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_holds FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan INSERT sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT): kebijakan baca membership tidak bisa menjadi
-- otorisasi mutasi. SELECT (bacaan histori) mensyaratkan tenant +
-- membership aktif + view_workspace — TIDAK mensyaratkan instalasi
-- atau entitlement Rental aktif, supaya aset/hold yang sudah tercatat
-- tetap terbaca sesudah deaktivasi/pencabutan. INSERT memakai WITH
-- CHECK yang mensyaratkan tenant + manage_business_data (yang sudah
-- mensyaratkan membership aktif lewat app.has_permission); hak modul
-- (instalasi aktif + entitlement) ditegakkan kode guard, bukan RLS.
-- UPDATE hold hanya untuk transisi status: USING membatasi baris yang
-- boleh diubah pada baris yang sedang held (plus tenant + membership +
-- manage_business_data); WITH CHECK membatasi baris hasil pada status
-- released/expired. Kolom lain (workspace, draft, aset, periode,
-- kunci, expires_at, created_at) tidak tercakup grant UPDATE di bawah,
-- jadi tidak bisa diubah runtime walau kebijakan mengizinkan barisnya.
DO $$
BEGIN
  DROP POLICY IF EXISTS physical_assets_select ON physical_assets;
  CREATE POLICY physical_assets_select ON physical_assets FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS physical_assets_insert ON physical_assets;
  CREATE POLICY physical_assets_insert ON physical_assets FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
  DROP POLICY IF EXISTS rental_holds_select ON rental_holds;
  CREATE POLICY rental_holds_select ON rental_holds FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_holds_insert ON rental_holds;
  CREATE POLICY rental_holds_insert ON rental_holds FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
  DROP POLICY IF EXISTS rental_holds_update ON rental_holds;
  CREATE POLICY rental_holds_update ON rental_holds FOR UPDATE
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND status = 'held'
    )
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND status IN ('released', 'expired')
    );
END $$;

-- Hak runtime: aset boleh SELECT/INSERT saja; hold boleh SELECT/INSERT
-- dan UPDATE KOLOM (status, updated_at) saja (tanpa UPDATE tabel,
-- DELETE, TRUNCATE). REVOKE ALL dulu supaya set hak selalu sama persis
-- dengan yang diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON physical_assets FROM umnia_business;
GRANT SELECT, INSERT ON physical_assets TO umnia_business;
REVOKE ALL PRIVILEGES ON rental_holds FROM umnia_business;
GRANT SELECT, INSERT ON rental_holds TO umnia_business;
GRANT UPDATE (status, updated_at) ON rental_holds TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 007.
INSERT INTO schema_migrations (version) VALUES ('007_asset_availability')
ON CONFLICT (version) DO NOTHING;
