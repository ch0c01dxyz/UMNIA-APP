-- F2-01-B: kontrak rental DRAFT saja (bukan rental operasional).
-- HANYA penyimpanan draft: satu baris mencatat pelanggan, barang, dan
-- periode yang dimaksud — tanpa stok, harga, uang, handover,
-- pembayaran, ketersediaan fisik, atau lifecycle (retur/rusak/hilang).
-- Satu baris catalog_items BUKAN unit fisik (physical_asset belum ada);
-- draft tidak mereservasi apa pun dan tidak membuktikan overlap aman.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada tabel ini.
-- Runtime hanya SELECT + INSERT (hanya create/list yang ada): tanpa
-- kebijakan UPDATE/DELETE, jadi RLS menolak mutasi lain; tanpa grant
-- UPDATE/DELETE/TRUNCATE/kepemilikan. Draft immutable pada slice ini;
-- tidak ada penghapusan history pada operasi modul.
-- Idempotent: CREATE TABLE IF NOT EXISTS, constraint UNIQUE pada
-- parties/catalog_items ditambah hanya bila belum ada (satu blok DO),
-- kebijakan diganti dalam SATU pernyataan DO (atomik), GRANT/REVOKE
-- idempotent, marker memakai ON CONFLICT DO NOTHING — supaya penerapan
-- ulang (maupun psql TANPA -1 di Linux native) mempertahankan data,
-- timestamp, dan seed yang sudah ada. Jangan bungkus dengan
-- BEGIN/COMMIT di file ini: pemanggil Windows/CI sudah membungkus
-- semua file dalam satu transaksi psql -1; BEGIN di sini akan
-- memutus transaksi luar itu.
-- FK komposit tenant: rujukan pelanggan/barang harus berada di
-- workspace yang sama dengan kontrak (docs/02: foreign key menyertakan
-- konteks tenant). Karena itu dua tabel core mendapat UNIQUE
-- (workspace_id, id) dulu — selalu terpenuhi karena id adalah PK —
-- supaya FK komposit punya target unik. FK adalah backstop, bukan
-- jalur galat handler: handler memeriksa rujukan tenant-scoped dulu
-- (404 identik untuk hilang/lintas-tenant), pelanggaran FK akan
-- muncul sebagai 500 bila cek itu dilewati.
-- Invarian client_request_id juga ditegakkan di level database (CHECK):
-- 1-128 karakter ASCII [huruf, digit, -, _, ., :] tanpa whitespace,
-- supaya INSERT langsung lewat role runtime pun ditolak bila kuncinya
-- tidak valid. API tetap memvalidasi sebelum INSERT.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'parties_workspace_id_id_key') THEN
    ALTER TABLE parties ADD CONSTRAINT parties_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'catalog_items_workspace_id_id_key') THEN
    ALTER TABLE catalog_items ADD CONSTRAINT catalog_items_workspace_id_id_key UNIQUE (workspace_id, id);
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS rental_contracts (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id      UUID NOT NULL REFERENCES workspaces(id),
  customer_id       UUID NOT NULL,
  catalog_item_id   UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  status            TEXT NOT NULL DEFAULT 'draft' CHECK (status = 'draft'),
  starts_at         TIMESTAMPTZ NOT NULL,
  ends_at           TIMESTAMPTZ NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (workspace_id, client_request_id),
  CHECK (ends_at > starts_at),
  FOREIGN KEY (workspace_id, customer_id) REFERENCES parties (workspace_id, id),
  FOREIGN KEY (workspace_id, catalog_item_id) REFERENCES catalog_items (workspace_id, id)
);

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity/004/005). FORCE penting karena tabel dimiliki
-- bootstrap (superuser melewati RLS), sedangkan role runtime harus
-- tetap tersaring.
ALTER TABLE rental_contracts ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_contracts FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan INSERT sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT): kebijakan baca membership tidak bisa menjadi
-- otorisasi mutasi. SELECT (bacaan histori) mensyaratkan tenant +
-- membership aktif + view_workspace — TIDAK mensyaratkan instalasi
-- atau entitlement Rental aktif, supaya draft yang sudah tercatat
-- tetap terbaca sesudah deaktivasi/pencabutan. INSERT memakai WITH
-- CHECK yang mensyaratkan tenant + manage_business_data (yang sudah
-- mensyaratkan membership aktif lewat app.has_permission); hak modul
-- (instalasi aktif + entitlement) ditegakkan kode guard, bukan RLS.
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_contracts_select ON rental_contracts;
  CREATE POLICY rental_contracts_select ON rental_contracts FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_contracts_insert ON rental_contracts;
  CREATE POLICY rental_contracts_insert ON rental_contracts FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
END $$;

-- Hak runtime: tabel ini boleh SELECT/INSERT saja (tanpa UPDATE,
-- DELETE, TRUNCATE). REVOKE ALL dulu supaya set hak selalu sama persis
-- dengan yang diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON rental_contracts FROM umnia_business;
GRANT SELECT, INSERT ON rental_contracts TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 006.
INSERT INTO schema_migrations (version) VALUES ('006_rental_contracts')
ON CONFLICT (version) DO NOTHING;
