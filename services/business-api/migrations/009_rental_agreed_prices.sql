-- F2-01-E: fakta harga disepakati (staff-recorded) per draft Rental.
-- HANYA penyimpanan fakta: satu baris rental_agreed_prices mencatat
-- harga yang DILIHAT staf pada quote lalu dikonfirmasi untuk satu
-- draft yang tidak berubah — versi harga efektif, tarif, mata uang,
-- unit, total, periode draft, identitas aturan, aktor, dan waktu
-- pencatatan. Draft, hold, dan versi harga TIDAK dimutasi oleh
-- penerimaan ini; tidak ada transisi status draft.
-- Fakta ini adalah catatan in-app oleh staf workspace, BUKAN consent/
-- tanda tangan pelanggan, BUKAN perjanjian hukum, BUKAN invoice/
-- pembayaran, BUKAN posting ledger, BUKAN reservasi aset, dan BUKAN
-- otorisasi handover. Kata "accept" di API tidak menambah otoritas
-- keuangan apa pun.
-- Satu fakta per draft selamanya pada slice ini (UNIQUE workspace +
-- rental_contract_id); koreksi/revisi/supersesi di luar slice — jalan
-- yang didefinisikan hanyalah draft baru + penerimaan baru, tanpa
-- membatalkan/mengubah fakta lama secara otomatis.
-- pricing_rule_version_id adalah identifier aturan milik kode yang
-- diperkenalkan tiket ini untuk algoritma elapsed-24h/ceil yang sudah
-- diimplementasikan F2-01-D (durasi elapsed mikrodetik UTC, ceil 24
-- jam, minimum 1 unit, total int64 terperiksa). Nilainya TEPAT
-- 'elapsed_24h_ceil_v1' — BUKAN baris business_rule_versions, BUKAN
-- UUID, BUKAN SDK/API, BUKAN rule engine generik. Perubahan formula
-- di masa depan butuh identifier baru + review. price_version_id
-- mengidentifikasi interval tarif/mata uang, BUKAN pengganti identitas
-- aturan yang disyaratkan docs/02:24.
-- Invarian total lintas-kolom memakai cast numerik eksak
-- (total_minor::numeric = amount_minor::numeric * units::numeric)
-- supaya perkalian BIGINT besar tidak overflow di tengah sebelum
-- dibandingkan; tanpa float.
-- Waktu finite: starts_at/ends_at/accepted_at ketiganya NOT NULL dan
-- isfinite (CHECK bernama), ends_at > starts_at. Units dibatasi
-- 1-3652059 (rentang maksimum yang didukung aritmetika F2-01-D).
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada tabel ini.
-- Runtime hanya SELECT + INSERT: tanpa kebijakan UPDATE/DELETE, jadi
-- RLS menolak mutasi lain. Tanpa grant DELETE/TRUNCATE/kepemilikan.
-- Tidak ada penghapusan history pada operasi modul.
-- Idempotent: CREATE TABLE IF NOT EXISTS, UNIQUE tambahan pada
-- price_versions + kebijakan diganti dalam SATU pernyataan DO
-- (atomik), GRANT/REVOKE idempotent, marker memakai ON CONFLICT DO
-- NOTHING — supaya penerapan ulang (maupun psql TANPA -1 di Linux
-- native) mempertahankan data, timestamp, dan seed yang sudah ada.
-- Jangan bungkus dengan BEGIN/COMMIT di file ini: pemanggil
-- Windows/CI sudah membungkus semua file dalam satu transaksi psql
-- -1; BEGIN di sini akan memutus transaksi luar itu.
-- FK komposit tenant: rujukan draft/versi harga/item/pelanggan harus
-- berada di workspace yang sama dengan fakta (docs/02: foreign key
-- menyertakan konteks tenant). FK adalah backstop, bukan jalur galat
-- handler: handler memeriksa rujukan tenant-scoped dulu (404 identik
-- untuk hilang/lintas-tenant), pelanggaran FK akan muncul sebagai 500
-- bila cek itu dilewati. UNIQUE(workspace_id, id) pada price_versions
-- dipasang di sini (pola 006/007) karena 008 hanya punya
-- UNIQUE(workspace_id, client_request_id).
-- Invarian client_request_id, currency, rule, dan units juga
-- ditegakkan di level database: kunci 1-128 karakter ASCII [huruf,
-- digit, -, _, ., :] tanpa whitespace, currency tepat 3 huruf ASCII
-- A-Z — supaya INSERT langsung lewat role runtime pun ditolak bila
-- nilainya tidak valid. API tetap memvalidasi sebelum INSERT.

-- Target FK komposit ke price_versions: UNIQUE(workspace_id, id)
-- (pola penambahan UNIQUE 006/007, atomik dalam satu DO).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'price_versions_workspace_id_id_key') THEN
    ALTER TABLE price_versions ADD CONSTRAINT price_versions_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
END
$$;

CREATE TABLE IF NOT EXISTS rental_agreed_prices (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id            UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id      UUID NOT NULL,
  price_version_id        UUID NOT NULL,
  pricing_rule_version_id TEXT NOT NULL,
  catalog_item_id         UUID NOT NULL,
  customer_id             UUID NOT NULL,
  client_request_id       TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  amount_minor            BIGINT NOT NULL CHECK (amount_minor >= 0),
  currency                TEXT NOT NULL CHECK (currency ~ '\A[A-Z]{3}\Z'),
  units                   BIGINT NOT NULL,
  total_minor             BIGINT NOT NULL CHECK (total_minor >= 0),
  starts_at               TIMESTAMPTZ NOT NULL,
  ends_at                 TIMESTAMPTZ NOT NULL,
  accepted_at             TIMESTAMPTZ NOT NULL,
  accepted_by             UUID NOT NULL REFERENCES identity_subjects(id),
  CHECK (ends_at > starts_at),
  CONSTRAINT rental_agreed_prices_finite_times CHECK (isfinite(starts_at) AND isfinite(ends_at) AND isfinite(accepted_at)),
  CONSTRAINT rental_agreed_prices_total_check CHECK (total_minor::numeric = amount_minor::numeric * units::numeric),
  CONSTRAINT rental_agreed_prices_rule_check CHECK (pricing_rule_version_id = 'elapsed_24h_ceil_v1'),
  CONSTRAINT rental_agreed_prices_units_check CHECK (units BETWEEN 1 AND 3652059),
  UNIQUE (workspace_id, rental_contract_id),
  UNIQUE (workspace_id, client_request_id),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, price_version_id) REFERENCES price_versions (workspace_id, id),
  FOREIGN KEY (workspace_id, catalog_item_id) REFERENCES catalog_items (workspace_id, id),
  FOREIGN KEY (workspace_id, customer_id) REFERENCES parties (workspace_id, id)
);

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity/004/005/006/007/008). FORCE penting karena tabel
-- dimiliki bootstrap (superuser melewati RLS), sedangkan role runtime
-- harus tetap tersaring.
ALTER TABLE rental_agreed_prices ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_agreed_prices FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan INSERT sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT): kebijakan baca membership tidak bisa menjadi
-- otorisasi mutasi. SELECT (bacaan histori) mensyaratkan tenant +
-- membership aktif + view_workspace — TIDAK mensyaratkan instalasi
-- atau entitlement Rental aktif, supaya fakta yang sudah tercatat
-- tetap terbaca sesudah deaktivasi/pencabutan; fakta bukan izin aksi
-- Rental. INSERT memakai WITH CHECK yang mensyaratkan tenant +
-- manage_business_data (yang sudah mensyaratkan membership aktif
-- lewat app.has_permission).
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_agreed_prices_select ON rental_agreed_prices;
  CREATE POLICY rental_agreed_prices_select ON rental_agreed_prices FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_agreed_prices_insert ON rental_agreed_prices;
  CREATE POLICY rental_agreed_prices_insert ON rental_agreed_prices FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
END $$;

-- Hak runtime: SELECT/INSERT saja (tanpa UPDATE, DELETE, TRUNCATE).
-- REVOKE ALL dulu supaya set hak selalu sama persis dengan yang
-- diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON rental_agreed_prices FROM umnia_business;
GRANT SELECT, INSERT ON rental_agreed_prices TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 009.
INSERT INTO schema_migrations (version) VALUES ('009_rental_agreed_prices')
ON CONFLICT (version) DO NOTHING;
