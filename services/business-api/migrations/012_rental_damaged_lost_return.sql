-- F2-01-G: pengembalian rusak/hilang (resolusi terminal custody).
-- Melanjutkan 010: custody 'out' kini bisa ditutup tiga arah —
-- 'returned' (utuh), 'damaged' (unit kembali rusak), 'lost' (unit
-- hilang; deklarasi staf, BUKAN pengembalian fisik). Lost TIDAK
-- pernah mengisi returned_at/returned_by (waktu/aktor pengembalian
-- fisik); waktu/aktor penutupan untuk KETIGA outcome tercatat di
-- kolom baru resolved_at/resolved_by. Untuk returned/damaged,
-- resolved_* SAMA dengan returned_* (waktu/aktor yang sama);
-- untuk lost, returned_* tetap NULL dan resolved_* mencatat
-- deklarasi. Terminal berarti final: tidak ada repair/recovery/
-- reopen di slice ini (tidak ada endpoint, tidak ada kebijakan
-- yang mengizinkannya).
-- Aset mendapat kolom condition ('ready'/'damaged'/'lost',
-- default 'ready' — baris lama terisi 'ready' saat ADD COLUMN,
-- tanpa UPDATE data terpisah). Hanya damaged/lost yang memutasi
-- kondisi aset (ready -> kondisi itu); return utuh TIDAK menyentuh
-- aset sama sekali, jadi kebijakan UPDATE aset TIDAK mengizinkan
-- ready -> ready. Kondisi damaged/lost mengecualikan aset dari
-- availability, hold baru, dan handover (dicek handler, bukan
-- sekadar endpoint advisory).
-- Backfill SEKALI untuk baris returned lama (sebelum CHECK baru
-- dipasang): resolved_* = returned_* — hanya baris yang belum
-- ter-resolve (resolved_at IS NULL). Baris yang sudah ter-resolve
-- tidak disentuh pada rerun. Baris yang tidak konsisten dengan
-- CHECK baru TIDAK diperbaiki di sini — instalasi CHECK di bawah
-- akan gagal (bukan auto-repair), dan marker 012 tidak tercatat.
-- Draft, fakta harga, versi harga, dan hold TIDAK dimutasi oleh
-- resolusi ini; hold yang sudah consumed tetap 'consumed'.
-- Custody adalah catatan in-app oleh staf workspace, BUKAN
-- consent/tanda tangan pelanggan, BUKAN perjanjian hukum, BUKAN
-- invoice/pembayaran, BUKAN posting ledger, dan BUKAN jumlah yang
-- disepakati baru. Tanpa uang di slice ini: tanpa deposit hangus,
-- biaya kerusakan, denda, atau pajak.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS. RLS ENABLE + FORCE pada kedua tabel.
-- Runtime custody: SELECT + INSERT + UPDATE kolom-terbatas (status,
-- returned_at, returned_by, resolved_at, resolved_by,
-- return_client_request_id) untuk transisi resolusi; tanpa UPDATE
-- tabel penuh, DELETE, TRUNCATE. Runtime aset: SELECT + INSERT +
-- UPDATE kolom-terbatas (condition, updated_at) untuk transisi
-- ready -> damaged/lost; tanpa UPDATE tabel penuh, DELETE, TRUNCATE.
-- Idempotent: ADD COLUMN IF NOT EXISTS, backfill bersyarat
-- resolved_at IS NULL DAN resolved_by IS NULL, penggantian CHECK dalam SATU ALTER (atomik),
-- kebijakan diganti dalam SATU pernyataan DO (atomik), GRANT/REVOKE
-- idempotent, marker memakai ON CONFLICT DO NOTHING — supaya
-- penerapan ulang (maupun psql TANPA -1 di Linux native)
-- mempertahankan data, timestamp, dan seed yang sudah ada. Jangan
-- bungkus dengan BEGIN/COMMIT di file ini: pemanggil Windows/CI
-- sudah membungkus semua file dalam satu transaksi psql -1; BEGIN di
-- sini akan memutus transaksi luar itu.
-- Marker 012 WAJIB untuk readiness (main.go checkDatabase): handler
-- return/handover/hold/availability memakai kolom baru, jadi
-- database yang berhenti di 010/011 belum siap. 011 tetap TIDAK
-- dipersyaratkan readiness (hanya CLI/routine operator yang
-- memeriksanya di dalam rutin).

-- Kolom baru custody: resolved_at/by (waktu/aktor penutupan untuk
-- ketiga outcome terminal), di akhir kolom sesudah returned_by.
ALTER TABLE rental_custody ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ;
ALTER TABLE rental_custody ADD COLUMN IF NOT EXISTS resolved_by UUID REFERENCES identity_subjects(id);

-- Backfill SEKALI baris returned lama (sebelum CHECK baru): resolved_*
-- = returned_*; hanya baris yang resolved_at DAN resolved_by-nya
-- SAMA-SAMA NULL. Baris parsial (satu terisi) atau tidak cocok TIDAK
-- disentuh — CHECK state baru akan menggagalkan migrasi, bukan
-- auto-repair (predikat hanya resolved_at IS NULL pernah menimpa
-- resolved_by berisi aktor lain secara diam-diam).
UPDATE rental_custody SET resolved_at = returned_at, resolved_by = returned_by
  WHERE status = 'returned' AND resolved_at IS NULL AND resolved_by IS NULL;

-- Perluas CHECK status: damaged + lost. Satu ALTER atomik.
ALTER TABLE rental_custody
  DROP CONSTRAINT IF EXISTS rental_custody_status_check,
  ADD CONSTRAINT rental_custody_status_check CHECK (status IN ('out', 'returned', 'damaged', 'lost'));

-- Perluas CHECK finite: resolved_at finite bila terisi. Satu ALTER atomik.
ALTER TABLE rental_custody
  DROP CONSTRAINT IF EXISTS rental_custody_finite_times,
  ADD CONSTRAINT rental_custody_finite_times CHECK (isfinite(handed_out_at) AND (returned_at IS NULL OR isfinite(returned_at)) AND (resolved_at IS NULL OR isfinite(resolved_at)));

-- Ganti CHECK state: out berarti kelima kolom terminal NULL;
-- returned/damaged berarti kelima kolom terisi DAN returned_* =
-- resolved_* (waktu/aktor yang sama); lost berarti returned_* NULL
-- dan resolved_*/kunci terisi. Satu ALTER atomik.
ALTER TABLE rental_custody
  DROP CONSTRAINT IF EXISTS rental_custody_state_check,
  ADD CONSTRAINT rental_custody_state_check CHECK (
    (status = 'out' AND returned_at IS NULL AND returned_by IS NULL AND resolved_at IS NULL AND resolved_by IS NULL AND return_client_request_id IS NULL)
    OR (status IN ('returned', 'damaged') AND returned_at IS NOT NULL AND returned_by IS NOT NULL AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL AND return_client_request_id IS NOT NULL AND returned_at = resolved_at AND returned_by = resolved_by)
    OR (status = 'lost' AND returned_at IS NULL AND returned_by IS NULL AND resolved_at IS NOT NULL AND resolved_by IS NOT NULL AND return_client_request_id IS NOT NULL)
  );

-- CHECK baru: resolved_at (bila terisi) tidak boleh sebelum handover.
-- CHECK lama returned_after_handover tidak berubah. DROP IF EXISTS
-- dulu supaya rerun 012 idempotent (pola 010: DROP + ADD dalam satu
-- ALTER); tanpa itu rerun gagal "constraint already exists".
ALTER TABLE rental_custody
  DROP CONSTRAINT IF EXISTS rental_custody_resolved_after_handover,
  ADD CONSTRAINT rental_custody_resolved_after_handover CHECK (resolved_at IS NULL OR resolved_at >= handed_out_at);

-- Kolom kondisi aset: 'ready' bisa disewa; 'damaged'/'lost'
-- dikecualikan. Backfill = DEFAULT itu sendiri (baris lama terisi
-- 'ready' saat ADD COLUMN). CHECK inline bernama otomatis
-- physical_assets_condition_check.
ALTER TABLE physical_assets ADD COLUMN IF NOT EXISTS condition TEXT NOT NULL DEFAULT 'ready' CHECK (condition IN ('ready', 'damaged', 'lost'));

-- RLS: FORCE sudah aktif pada kedua tabel (007/010); pastikan tetap
-- aktif sesudah perubahan kolom (tidak ada perubahan kepemilikan).
ALTER TABLE rental_custody ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_custody FORCE ROW LEVEL SECURITY;
ALTER TABLE physical_assets ENABLE ROW LEVEL SECURITY;
ALTER TABLE physical_assets FORCE ROW LEVEL SECURITY;

-- Kebijakan dalam SATU pernyataan DO supaya penggantian atomik (pola
-- 010): kebijakan UPDATE custody diganti (WITH CHECK kini tiga
-- status terminal); kebijakan UPDATE aset BARU (USING hanya baris
-- condition='ready', WITH CHECK hanya damaged/lost — ready -> ready
-- TIDAK diizinkan, jadi return utuh tidak bisa menyentuh aset).
-- SELECT/INSERT kedua tabel tidak berubah.
DO $$
BEGIN
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
      AND status IN ('returned', 'damaged', 'lost')
    );
  DROP POLICY IF EXISTS physical_assets_update ON physical_assets;
  CREATE POLICY physical_assets_update ON physical_assets FOR UPDATE
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND condition = 'ready'
    )
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND condition IN ('damaged', 'lost')
    );
END $$;

-- Hak runtime custody: SELECT/INSERT + UPDATE kolom-terbatas untuk
-- transisi resolusi saja (tanpa UPDATE tabel penuh, DELETE, TRUNCATE).
-- REVOKE ALL dulu supaya set hak selalu sama persis dengan yang
-- diniatkan, bukan akumulasi dari keadaan sebelumnya.
REVOKE ALL PRIVILEGES ON rental_custody FROM umnia_business;
GRANT SELECT, INSERT ON rental_custody TO umnia_business;
GRANT UPDATE (status, returned_at, returned_by, resolved_at, resolved_by, return_client_request_id) ON rental_custody TO umnia_business;

-- Hak runtime aset: SELECT/INSERT + UPDATE kolom-terbatas (condition,
-- updated_at) untuk transisi ready -> damaged/lost saja.
REVOKE ALL PRIVILEGES ON physical_assets FROM umnia_business;
GRANT SELECT, INSERT ON physical_assets TO umnia_business;
GRANT UPDATE (condition, updated_at) ON physical_assets TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 012.
INSERT INTO schema_migrations (version) VALUES ('012_rental_damaged_lost_return')
ON CONFLICT (version) DO NOTHING;
