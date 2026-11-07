-- F2-01-F: custody serah terima + pengembalian unit serial per draft.
-- HANYA catatan custody: satu baris rental_custody mencatat unit
-- fisik yang diserahkan staf untuk satu draft (status out), lalu
-- dikembalikan utuh (status returned). Draft, fakta harga, versi
-- harga, dan hold TIDAK dimutasi oleh custody — kecuali hold yang
-- dipakai serah terima berubah menjadi 'consumed' (status baru di
-- sini). Custody adalah catatan in-app oleh staf workspace, BUKAN
-- consent/tanda tangan pelanggan, BUKAN perjanjian hukum, BUKAN
-- invoice/pembayaran, BUKAN posting ledger, dan BUKAN jumlah yang
-- disepakati baru.
-- Satu custody per draft selamanya pada slice ini (UNIQUE workspace +
-- rental_contract_id); satu unit hanya boleh keluar sekali dalam satu
-- waktu (partial UNIQUE INDEX workspace + asset_id WHERE status =
-- 'out'). Koreksi/revisi/supersesi di luar slice — custody yang
-- salah catat butuh alur koreksi terotorisasi di masa depan, bukan
-- UPDATE runtime sembarangan.
-- Hold 'consumed' adalah terminal seperti released/expired: tidak
-- pernah diubah menjadi expired oleh cleanup, tidak dibuka lagi oleh
-- return. Selama custody out ada untuk sebuah aset, unit itu keluar
-- secara fisik apa pun periode yang direncanakan atau TTL hold.
-- Waktu finite: handed_out_at NOT NULL dan isfinite; returned_at
-- NULL saat out, NOT NULL dan isfinite saat returned, dan
-- returned_at >= handed_out_at. Status dan kolom return saling
-- mengikat (CHECK bernama): out berarti ketiga kolom return NULL;
-- returned berarti ketiganya terisi.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada tabel ini.
-- Runtime SELECT + INSERT + UPDATE kolom-terbatas (status,
-- returned_at, returned_by, return_client_request_id) untuk transisi
-- return; tanpa kebijakan DELETE, jadi RLS menolak mutasi lain.
-- Tanpa grant DELETE/TRUNCATE/kepemilikan. Tanpa penghapusan
-- history pada operasi modul.
-- Idempotent: CREATE TABLE IF NOT EXISTS, UNIQUE tambahan +
-- kebijakan diganti dalam SATU pernyataan DO (atomik), penggantian
-- CHECK status hold dalam SATU ALTER (atomik), GRANT/REVOKE
-- idempotent, marker memakai ON CONFLICT DO NOTHING — supaya
-- penerapan ulang (maupun psql TANPA -1 di Linux native)
-- mempertahankan data, timestamp, dan seed yang sudah ada. Jangan
-- bungkus dengan BEGIN/COMMIT di file ini: pemanggil Windows/CI
-- sudah membungkus semua file dalam satu transaksi psql -1; BEGIN di
-- sini akan memutus transaksi luar itu.
-- FK komposit tenant: rujukan draft/fakta harga/aset/hold harus
-- berada di workspace yang sama dengan custody (docs/02: foreign
-- key menyertakan konteks tenant). FK adalah backstop, bukan jalur
-- galat handler: handler memeriksa rujukan tenant-scoped dulu (404
-- identik untuk hilang/lintas-tenant), pelanggaran FK akan muncul
-- sebagai 500 bila cek itu dilewati. UNIQUE(workspace_id, id) pada
-- rental_agreed_prices dan rental_holds dipasang di sini (pola
-- 006/007/009) karena 007/009 hanya punya UNIQUE workspace +
-- client_request_id.
-- Invarian client_request_id dan return_client_request_id juga
-- ditegakkan di level database: kunci 1-128 karakter ASCII [huruf,
-- digit, -, _, ., :] tanpa whitespace (return key NULL bila belum
-- dikembalikan), supaya INSERT/UPDATE langsung lewat role runtime
-- pun ditolak bila nilainya tidak valid. API tetap memvalidasi
-- sebelum INSERT/UPDATE.

-- Target FK komposit: UNIQUE(workspace_id, id) pada
-- rental_agreed_prices dan rental_holds (pola penambahan UNIQUE
-- 006/007/009, atomik dalam satu DO).
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rental_agreed_prices_workspace_id_id_key') THEN
    ALTER TABLE rental_agreed_prices ADD CONSTRAINT rental_agreed_prices_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rental_holds_workspace_id_id_key') THEN
    ALTER TABLE rental_holds ADD CONSTRAINT rental_holds_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
END
$$;

-- Perluas CHECK status hold: 'consumed' untuk hold yang dipakai
-- serah terima. Satu ALTER atomik (DROP + ADD) supaya rerun 010
-- memasangnya pada tabel 007 lama tanpa jendela tanpa CHECK.
ALTER TABLE rental_holds
  DROP CONSTRAINT IF EXISTS rental_holds_status_check,
  ADD CONSTRAINT rental_holds_status_check CHECK (status IN ('held', 'released', 'expired', 'consumed'));

CREATE TABLE IF NOT EXISTS rental_custody (
  id                      UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id            UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id      UUID NOT NULL,
  rental_agreed_price_id  UUID NOT NULL,
  asset_id                UUID NOT NULL,
  hold_id                 UUID NOT NULL,
  client_request_id       TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  return_client_request_id TEXT CHECK (return_client_request_id IS NULL OR (char_length(return_client_request_id) BETWEEN 1 AND 128 AND return_client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z')),
  status                  TEXT NOT NULL CHECK (status IN ('out', 'returned')),
  handed_out_at           TIMESTAMPTZ NOT NULL,
  returned_at             TIMESTAMPTZ,
  handed_out_by           UUID NOT NULL REFERENCES identity_subjects(id),
  returned_by             UUID REFERENCES identity_subjects(id),
  CONSTRAINT rental_custody_finite_times CHECK (isfinite(handed_out_at) AND (returned_at IS NULL OR isfinite(returned_at))),
  CONSTRAINT rental_custody_state_check CHECK (
    (status = 'out' AND returned_at IS NULL AND returned_by IS NULL AND return_client_request_id IS NULL)
    OR (status = 'returned' AND returned_at IS NOT NULL AND returned_by IS NOT NULL AND return_client_request_id IS NOT NULL)
  ),
  CONSTRAINT rental_custody_returned_after_handover CHECK (returned_at IS NULL OR returned_at >= handed_out_at),
  UNIQUE (workspace_id, rental_contract_id),
  UNIQUE (workspace_id, client_request_id),
  UNIQUE (workspace_id, return_client_request_id),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, rental_agreed_price_id) REFERENCES rental_agreed_prices (workspace_id, id),
  FOREIGN KEY (workspace_id, asset_id) REFERENCES physical_assets (workspace_id, id),
  FOREIGN KEY (workspace_id, hold_id) REFERENCES rental_holds (workspace_id, id)
);

-- Backstop ganda serah terima: satu unit hanya boleh keluar sekali.
CREATE UNIQUE INDEX IF NOT EXISTS rental_custody_one_out_per_asset
  ON rental_custody (workspace_id, asset_id) WHERE status = 'out';

-- RLS: semua akses data tenant harus lewat workspace aktif +
-- membership (pola 002_identity/004/005/006/007/008/009). FORCE
-- penting karena tabel dimiliki bootstrap (superuser melewati RLS),
-- sedangkan role runtime harus tetap tersaring.
ALTER TABLE rental_custody ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_custody FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT, INSERT, dan UPDATE sengaja TERPISAH per perintah
-- (FOR SELECT / FOR INSERT / FOR UPDATE): kebijakan baca membership
-- tidak bisa menjadi otorisasi mutasi. SELECT (bacaan histori)
-- mensyaratkan tenant + membership aktif + view_workspace — TIDAK
-- mensyaratkan instalasi atau entitlement Rental aktif, supaya
-- custody yang sudah tercatat tetap terbaca sesudah
-- deaktivasi/pencabutan. INSERT memakai WITH CHECK yang
-- mensyaratkan tenant + manage_business_data (yang sudah
-- mensyaratkan membership aktif lewat app.has_permission). UPDATE
-- hanya mengizinkan transisi out -> returned: USING mensyaratkan
-- tenant + membership + manage_business_data + status='out', WITH
-- CHECK mensyaratkan tenant + manage_business_data +
-- status='returned'. Kebijakan UPDATE hold 007 ikut diganti di sini
-- (WITH CHECK menambah 'consumed'); USING tetap status='held'.
DO $$
BEGIN
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
      AND status IN ('released', 'expired', 'consumed')
    );
  DROP POLICY IF EXISTS rental_custody_select ON rental_custody;
  CREATE POLICY rental_custody_select ON rental_custody FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_custody_insert ON rental_custody;
  CREATE POLICY rental_custody_insert ON rental_custody FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
  DROP POLICY IF EXISTS rental_custody_update ON rental_custody;
  CREATE POLICY rental_custody_update ON rental_custody FOR UPDATE
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND status = 'out'
    )
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND status = 'returned'
    );
END $$;

-- Hak runtime: SELECT/INSERT + UPDATE kolom-terbatas untuk transisi
-- return saja (tanpa UPDATE tabel penuh, DELETE, TRUNCATE).
-- REVOKE ALL dulu supaya set hak selalu sama persis dengan yang
-- diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON rental_custody FROM umnia_business;
GRANT SELECT, INSERT ON rental_custody TO umnia_business;
GRANT UPDATE (status, returned_at, returned_by, return_client_request_id) ON rental_custody TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 010.
INSERT INTO schema_migrations (version) VALUES ('010_rental_custody')
ON CONFLICT (version) DO NOTHING;
