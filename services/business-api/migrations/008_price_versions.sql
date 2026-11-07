-- F2-01-D: penjadwalan tarif finite per catalog item + quote tak mengikat.
-- HANYA penyimpanan versi harga: satu baris price_versions mencatat
-- satu interval tarif [valid_from, valid_to) yang finite untuk satu
-- catalog item pada price_list 'standard', dengan amount_minor integer
-- eksak dan currency kode 3 huruf ASCII. Versi harga BUKAN harga yang
-- disepakati (agreed amount), BUKAN snapshot yang disimpan, BUKAN
-- pengikat harga, dan BUKAN transisi status draft: quote dihitung dari
-- versi ini pada saat dibaca dan tidak disimpan di mana pun.
-- Interval WAJIB finite: valid_from dan valid_to keduanya NOT NULL,
-- dan tidak ada harga open-ended di slice ini — v1 yang valid_to-nya
-- NULL tidak bisa digantikan (interval tak berbatas selalu overlap
-- dengan versi berikutnya), jadi R0 yang mengizinkan NULL dicabut.
-- Batas finite ditegakkan di database oleh CHECK bernama
-- price_versions_finite_bounds (isfinite kedua batas), bukan hanya
-- oleh validasi HTTP: INSERT langsung lewat role runtime dengan
-- valid_to/valid_from infinity ditolak 23514. Rerun file ini memasang
-- CHECK tersebut juga pada tabel 008 lama (lihat ALTER di bawah).
-- Koreksi umum tidak ada: interval baru yang overlap dengan interval
-- yang sudah ada ditolak; yang diizinkan hanya interval baru yang
-- berdekatan (valid_from == valid_to interval sebelumnya) atau tidak
-- bersinggungan. Tidak ada endpoint edit/delete/shorten dan tidak ada
-- penulisan ulang migrasi untuk mengubah riwayat.
-- recorded_at adalah SATU-SATUNYA fakta temporal baris ini: tidak ada
-- kolom created_at/updated_at. API mengikat recorded_at ke cutoff
-- clock_timestamp() yang ditangkap sesudah workspace lock; DEFAULT
-- now() di sini hanya untuk fixture bootstrap sintetis langsung, dan
-- tidak ada input klien yang boleh mengisinya.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada tabel ini.
-- Runtime hanya SELECT + INSERT (administrasi harga/list/quote):
-- tanpa kebijakan UPDATE/DELETE, jadi RLS menolak mutasi lain. Tanpa
-- grant DELETE/TRUNCATE/kepemilikan. Tidak ada penghapusan history
-- pada operasi modul.
-- Idempotent: CREATE TABLE IF NOT EXISTS, kebijakan diganti dalam
-- SATU pernyataan DO (atomik), GRANT/REVOKE idempotent, marker
-- memakai ON CONFLICT DO NOTHING — supaya penerapan ulang (maupun
-- psql TANPA -1 di Linux native) mempertahankan data, timestamp, dan
-- seed yang sudah ada. Jangan bungkus dengan BEGIN/COMMIT di file
-- ini: pemanggil Windows/CI sudah membungkus semua file dalam satu
-- transaksi psql -1; BEGIN di sini akan memutus transaksi luar itu.
-- FK komposit tenant: rujukan item harus berada di workspace yang
-- sama dengan versi harga (docs/02: foreign key menyertakan konteks
-- tenant). FK adalah backstop, bukan jalur galat handler: handler
-- memeriksa rujukan tenant-scoped dulu (404 identik untuk
-- hilang/lintas-tenant), pelanggaran FK akan muncul sebagai 500 bila
-- cek itu dilewati.
-- Non-overlap: constraint EXCLUDE pada tstzrange(valid_from,
-- valid_to, '[)') mencegah dua versi untuk item yang sama di workspace
-- yang sama bertabrakan. Interval half-open '[)': interval yang
-- bersebelahan (v2.valid_from == v1.valid_to) TIDAK overlap. Butuh
-- ekstensi btree_gist (untuk kesetaraan UUID di dalam GiST); dibuat di
-- sini oleh akun bootstrap (superuser di semua jalur). TIDAK ADA
-- predikat berubah-waktu di dalam constraint.
-- Invarian client_request_id dan currency juga ditegakkan di level
-- database: kunci 1-128 karakter ASCII [huruf, digit, -, _, ., :]
-- tanpa whitespace, currency tepat 3 huruf ASCII A-Z — supaya INSERT
-- langsung lewat role runtime pun ditolak bila nilainya tidak valid.
-- API tetap memvalidasi sebelum INSERT.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE IF NOT EXISTS price_versions (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id      UUID NOT NULL REFERENCES workspaces(id),
  catalog_item_id   UUID NOT NULL,
  price_list_id     TEXT NOT NULL CHECK (price_list_id = 'standard'),
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  valid_from        TIMESTAMPTZ NOT NULL,
  valid_to          TIMESTAMPTZ NOT NULL,
  recorded_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  amount_minor      BIGINT NOT NULL CHECK (amount_minor >= 0),
  currency          TEXT NOT NULL CHECK (currency ~ '\A[A-Z]{3}\Z'),
  CHECK (valid_to > valid_from),
  CONSTRAINT price_versions_finite_bounds CHECK (isfinite(valid_from) AND isfinite(valid_to)),
  UNIQUE (workspace_id, client_request_id),
  FOREIGN KEY (workspace_id, catalog_item_id) REFERENCES catalog_items (workspace_id, id),
  CONSTRAINT price_versions_no_overlap EXCLUDE USING gist (workspace_id WITH =, catalog_item_id WITH =, price_list_id WITH =, tstzrange(valid_from, valid_to, '[)') WITH &&)
);

-- Batas finite pada tabel 008 LAMA: CREATE TABLE IF NOT EXISTS di
-- atas tidak mengubah tabel yang sudah dibuat sebelum CHECK ini ada,
-- jadi rerun file ini memasangnya lewat SATU ALTER DROP...ADD berikut
-- (pola penggantian CHECK atomik F1-04A). Satu statement: bila ada
-- baris nonfinite yang sudah ter-commit, ADD gagal validasi dan DROP
-- ikut batal, sehingga tidak ada data/constraint yang hilang dan
-- rerun gagal eksplisit (bukan perbaikan diam-diam). Jangan pecah
-- menjadi DROP dan ADD terpisah: pada jalur autocommit psql tanpa
-- -1, DROP yang sudah ter-commit tidak bisa dibatalkan bila ADD
-- sesudahnya gagal.
ALTER TABLE price_versions
    DROP CONSTRAINT IF EXISTS price_versions_finite_bounds,
    ADD CONSTRAINT price_versions_finite_bounds
        CHECK (isfinite(valid_from) AND isfinite(valid_to));

-- RLS: semua akses data tenant harus lewat workspace aktif + membership
-- (pola 002_identity/004/005/006/007). FORCE penting karena tabel
-- dimiliki bootstrap (superuser melewati RLS), sedangkan role runtime
-- harus tetap tersaring.
ALTER TABLE price_versions ENABLE ROW LEVEL SECURITY;
ALTER TABLE price_versions FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik: bila
-- satu CREATE POLICY gagal saat rerun, DROP yang sudah jalan di dalam
-- DO ikut rollback dan kebijakan lama tetap ada — tidak pernah ada
-- jendela tanpa kebijakan (atau dengan kebijakan setengah jadi) di
-- antara DROP dan CREATE terpisah pada jalur autocommit Linux native.
-- Kebijakan SELECT dan INSERT sengaja TERPISAH per perintah (FOR
-- SELECT / FOR INSERT): kebijakan baca membership tidak bisa menjadi
-- otorisasi mutasi. SELECT (bacaan histori/quote) mensyaratkan tenant
-- + membership aktif + view_workspace — TIDAK mensyaratkan instalasi
-- atau entitlement Rental aktif, supaya harga/quote yang sudah
-- tercatat tetap terbaca sesudah deaktivasi/pencabutan; quote bukan
-- izin aksi Rental. INSERT memakai WITH CHECK yang mensyaratkan
-- tenant + manage_business_data (yang sudah mensyaratkan membership
-- aktif lewat app.has_permission).
DO $$
BEGIN
  DROP POLICY IF EXISTS price_versions_select ON price_versions;
  CREATE POLICY price_versions_select ON price_versions FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS price_versions_insert ON price_versions;
  CREATE POLICY price_versions_insert ON price_versions FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
    );
END $$;

-- Hak runtime: SELECT/INSERT saja (tanpa UPDATE, DELETE, TRUNCATE).
-- REVOKE ALL dulu supaya set hak selalu sama persis dengan yang
-- diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON price_versions FROM umnia_business;
GRANT SELECT, INSERT ON price_versions TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 008.
INSERT INTO schema_migrations (version) VALUES ('008_price_versions')
ON CONFLICT (version) DO NOTHING;
