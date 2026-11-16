-- F2-02-A: pencatatan pembayaran manual + pembalikan catatan penuh,
-- dengan approval dua aktor (model proposal append-only -> fakta
-- posted yang disetujui; D3 diamandemen, celah maker-checker v1
-- DITUTUP oleh slice ini, bukan ditunda).
-- Pembayaran adalah penerimaan manual staf (cash/transfer) yang
-- merujuk fakta harga disepakati draft YANG SAMA sebagai plafon
-- acuan internal (rental_agreed_prices.total_minor). Fakta harga
-- tetap acuan, BUKAN invoice/utang hukum/persetujuan pelanggan/
-- konfirmasi bank/gateway. Tidak ada gate status custody:
-- pembayaran bisa dicatat sesudah fakta harga ada, independen
-- dari custody; damaged/lost TIDAK menambah tagihan apa pun.
-- Empat tabel append-only, masing-masing satu keyspace
-- Idempotency-Key sendiri: rental_payment_proposals (usulan),
-- rental_payments (fakta posted), rental_payment_reversal_proposals
-- (usulan pembalikan), rental_payment_reversals (fakta pembalikan
-- posted). Proposal yang pending TIDAK PERNAH memengaruhi total
-- paid/outstanding/reserved dan TIDAK mereservasi saldo: total
-- hanya dihitung dari fakta posted yang belum dibatalkan. Tidak
-- ada kolom status yang dimutasi, tidak ada penolakan/pembatalan/
-- kedaluwarsa/kerangka approval umum: proposal yang tidak disetujui
-- tetap tersimpan sebagai riwayat inert.
-- Approval WAJIB oleh identitas owner/manager KEDUA yang berbeda
-- dari pengusul, untuk posting pembayaran DAN pembalikan penuh.
-- Reason + audit, eksekusi owner-only, atau aktivasi modul BUKAN
-- approval. Workspace satu aktor gagal tertutup: proposal bisa
-- dibuat tetapi tidak pernah bisa disetujui sampai ada aktor kedua
-- yang berwenang; tidak ada pengecualian self-approval.
-- Pembalikan adalah pembatalan CATATAN (penuh saja), BUKAN refund/
-- uang keluar; tidak ada parsial, tidak ada un-reverse. Pembalikan
-- posted mengeluarkan pembayaran itu dari agregasi aktif; baris
-- pembayaran maupun pembalikan TIDAK PERNAH dihapus.
-- Uang: amount_minor BIGINT > 0 (string desimal kanonis di API,
-- parseCanonicalMoney; tanpa float/FX). Currency tepat 3 huruf
-- A-Z dan HARUS sama dengan currency fakta. Method cash/transfer
-- saja. Reference opsional: absen/null -> NULL; bila string,
-- disimpan VERBATIM 1-128 rune dengan konten non-whitespace.
-- Reason pembalikan wajib string yang di-trim 1-500 rune.
-- Agregasi aktif (handler, numeric PostgreSQL 16): paid =
-- COALESCE(SUM(amount_minor) atas pembayaran posted yang TIDAK
-- punya pembalikan posted, 0); sum(bigint) -> numeric dan sum
-- tanpa baris -> NULL, jadi gross riwayat pay/reverse berulang
-- BOLEH melebihi BIGINT walau net aktif <= total — handler TIDAK
-- PERNAH menjumlah gross dalam int64 dan TIDAK PERNAH clamp/
-- perbaiki-otomatis data inkonsisten (500).
-- Ikatan relasional (bukan FK independen per workspace yang
-- memungkinkan substitusi draft/proposal/pembayaran lain):
-- fakta pembayaran merujuk (workspace, draft, proposal) dan
-- (workspace, draft, fakta harga); usulan pembalikan merujuk
-- (workspace, draft, pembayaran); fakta pembalikan merujuk
-- (workspace, draft, pembayaran) DAN (workspace, draft,
-- pembayaran, usulan pembalikan). Target UNIQUE sempit yang
-- dibutuhkan ditambah di sini, termasuk UNIQUE 3-kolom pada
-- rental_agreed_prices (workspace, draft, id); file 001-012
-- TIDAK diubah kecuali penambahan UNIQUE itu.
-- RLS: ENABLE + FORCE pada keempat tabel. SELECT: tenant +
-- membership + view_workspace (histori tetap terbaca sesudah
-- deactivate/revoke). INSERT: tenant + manage_business_data +
-- view_workspace DAN ikatan aktor/payload — proposed_by =
-- current_subject pada proposal, approved_by = current_subject
-- pada fakta, plus kesamaan payload/metadata usulan yang disalin
-- (reference IS NOT DISTINCT FROM) lewat EXISTS atas usulan
-- terkait. CHECK proposer != approver saja TIDAK cukup karena
-- kedua kolom bisa dipalsukan; kebijakan WITH CHECK inilah yang
-- mengikat aktor ke subjek sesi. TIDAK ADA kebijakan UPDATE/
-- DELETE; runtime hanya SELECT + INSERT; tanpa SECURITY DEFINER,
-- tanpa role DB baru, tanpa perubahan operator/CLI.
-- Tabel dimiliki akun bootstrap/migrasi yang menjalankan file ini
-- (postgres di CI/Linux, umnia_bootstrap di Windows); role runtime
-- umnia_business TIDAK boleh memiliki tabel ini dan TIDAK mendapat
-- superuser/BYPASSRLS.
-- Idempotent: CREATE TABLE IF NOT EXISTS, penambahan UNIQUE
-- dalam DO IF NOT EXISTS (pola 010), kebijakan diganti dalam
-- SATU pernyataan DO per tabel (atomik, pola 009/010/012),
-- GRANT/REVOKE idempotent, marker memakai ON CONFLICT DO NOTHING
-- — supaya penerapan ulang (maupun psql TANPA -1 di Linux native)
-- mempertahankan data, timestamp, dan hak yang sudah ada. Jangan
-- bungkus dengan BEGIN/COMMIT di file ini: pemanggil Windows/CI
-- sudah membungkus semua file dalam satu transaksi psql -1; BEGIN
-- di sini akan memutus transaksi luar itu.
-- Marker 013 WAJIB untuk readiness (main.go checkDatabase):
-- handler pembayaran memakai keempat tabel, jadi database yang
-- berhenti di 012 belum siap. 011 tetap TIDAK dipersyaratkan
-- readiness (hanya CLI/routine operator yang memeriksanya).

-- Target FK komposit: UNIQUE(workspace_id, rental_contract_id, id)
-- pada rental_agreed_prices (pola penambahan UNIQUE 010, atomik
-- dalam satu DO). Target ini mengikat fakta pembayaran ke
-- workspace + draft YANG SAMA dengan fakta harganya, di DB.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'rental_agreed_prices_workspace_draft_id_key') THEN
    ALTER TABLE rental_agreed_prices ADD CONSTRAINT rental_agreed_prices_workspace_draft_id_key UNIQUE (workspace_id, rental_contract_id, id);
  END IF;
END
$$;

-- Usulan pembayaran: rujukan fakta harga draft yang sama, jumlah
-- positif, method cash/transfer, reference opsional verbatim.
CREATE TABLE IF NOT EXISTS rental_payment_proposals (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id UUID NOT NULL,
  rental_agreed_price_id UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  currency TEXT NOT NULL CHECK (currency ~ '\A[A-Z]{3}\Z'),
  method TEXT NOT NULL CHECK (method IN ('cash', 'transfer')),
  reference TEXT CHECK (reference IS NULL OR (char_length(reference) BETWEEN 1 AND 128 AND reference ~ '\S')),
  proposed_by UUID NOT NULL REFERENCES identity_subjects(id),
  proposed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(proposed_at)),
  UNIQUE (workspace_id, id),
  UNIQUE (workspace_id, client_request_id),
  UNIQUE (workspace_id, rental_contract_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, rental_agreed_price_id) REFERENCES rental_agreed_prices (workspace_id, rental_contract_id, id)
);

-- Fakta pembayaran posted: satu fakta per usulan; payload dan
-- metadata pengusul disalin verbatim dari usulan; penyetuju
-- berbeda dari pengusul dan approved_at tidak sebelum proposed_at.
CREATE TABLE IF NOT EXISTS rental_payments (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id UUID NOT NULL,
  rental_agreed_price_id UUID NOT NULL,
  proposal_id UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  amount_minor BIGINT NOT NULL CHECK (amount_minor > 0),
  currency TEXT NOT NULL CHECK (currency ~ '\A[A-Z]{3}\Z'),
  method TEXT NOT NULL CHECK (method IN ('cash', 'transfer')),
  reference TEXT CHECK (reference IS NULL OR (char_length(reference) BETWEEN 1 AND 128 AND reference ~ '\S')),
  proposed_by UUID NOT NULL REFERENCES identity_subjects(id),
  proposed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(proposed_at)),
  approved_by UUID NOT NULL REFERENCES identity_subjects(id),
  approved_at TIMESTAMPTZ NOT NULL CHECK (isfinite(approved_at)),
  UNIQUE (workspace_id, id),
  UNIQUE (workspace_id, client_request_id),
  UNIQUE (workspace_id, proposal_id),
  UNIQUE (workspace_id, rental_contract_id, id),
  CHECK (proposed_by <> approved_by),
  CHECK (approved_at >= proposed_at),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, rental_agreed_price_id) REFERENCES rental_agreed_prices (workspace_id, rental_contract_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, proposal_id) REFERENCES rental_payment_proposals (workspace_id, rental_contract_id, id)
);

-- Usulan pembalikan penuh: satu pembayaran posted, alasan wajib
-- (disimpan sudah di-trim handler, 1-500 rune, tidak kosong).
-- proposed_at tidak boleh sebelum approved_at pembayaran target:
-- dijamin handler (cutoff DB sesudah lock >= approved_at yang
-- juga dari jam DB) dan diuji, bukan CHECK lintas-tabel.
CREATE TABLE IF NOT EXISTS rental_payment_reversal_proposals (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id UUID NOT NULL,
  payment_id UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500 AND reason ~ '\S'),
  proposed_by UUID NOT NULL REFERENCES identity_subjects(id),
  proposed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(proposed_at)),
  UNIQUE (workspace_id, id),
  UNIQUE (workspace_id, client_request_id),
  UNIQUE (workspace_id, rental_contract_id, payment_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, payment_id) REFERENCES rental_payments (workspace_id, rental_contract_id, id)
);

-- Fakta pembalikan posted: satu pembalikan per pembayaran dan per
-- usulan; alasan dan metadata pengusul disalin verbatim dari usulan
-- pembalikan; jumlah penuh diturunkan dari pembayaran immutable,
-- TIDAK PERNAH jumlah baru dari klien.
CREATE TABLE IF NOT EXISTS rental_payment_reversals (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  workspace_id UUID NOT NULL REFERENCES workspaces(id),
  rental_contract_id UUID NOT NULL,
  payment_id UUID NOT NULL,
  reversal_proposal_id UUID NOT NULL,
  client_request_id TEXT NOT NULL CHECK (char_length(client_request_id) BETWEEN 1 AND 128 AND client_request_id ~ '\A[A-Za-z0-9_.:-]+\Z'),
  reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 500 AND reason ~ '\S'),
  proposed_by UUID NOT NULL REFERENCES identity_subjects(id),
  proposed_at TIMESTAMPTZ NOT NULL CHECK (isfinite(proposed_at)),
  approved_by UUID NOT NULL REFERENCES identity_subjects(id),
  approved_at TIMESTAMPTZ NOT NULL CHECK (isfinite(approved_at)),
  UNIQUE (workspace_id, id),
  UNIQUE (workspace_id, client_request_id),
  UNIQUE (workspace_id, reversal_proposal_id),
  UNIQUE (workspace_id, payment_id),
  CHECK (proposed_by <> approved_by),
  CHECK (approved_at >= proposed_at),
  FOREIGN KEY (workspace_id, rental_contract_id) REFERENCES rental_contracts (workspace_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, payment_id) REFERENCES rental_payments (workspace_id, rental_contract_id, id),
  FOREIGN KEY (workspace_id, rental_contract_id, payment_id, reversal_proposal_id) REFERENCES rental_payment_reversal_proposals (workspace_id, rental_contract_id, payment_id, id)
);

-- RLS: FORCE sudah harus aktif pada keempat tabel baru (dibuat di
-- sini); pastikan tetap aktif sesudah definisi di atas.
ALTER TABLE rental_payment_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_payment_proposals FORCE ROW LEVEL SECURITY;
ALTER TABLE rental_payments ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_payments FORCE ROW LEVEL SECURITY;
ALTER TABLE rental_payment_reversal_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_payment_reversal_proposals FORCE ROW LEVEL SECURITY;
ALTER TABLE rental_payment_reversals ENABLE ROW LEVEL SECURITY;
ALTER TABLE rental_payment_reversals FORCE ROW LEVEL SECURITY;

-- Kebijakan usulan pembayaran dalam SATU pernyataan DO supaya
-- penggantian atomik (pola 009/010/012). SELECT: tenant +
-- membership + view_workspace. INSERT: tenant +
-- manage_business_data + view_workspace DAN proposed_by =
-- current_subject — aktor usulan diikat ke subjek sesi, bukan
-- kolom yang bisa dipalsukan.
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_payment_proposals_select ON rental_payment_proposals;
  CREATE POLICY rental_payment_proposals_select ON rental_payment_proposals FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_payment_proposals_insert ON rental_payment_proposals;
  CREATE POLICY rental_payment_proposals_insert ON rental_payment_proposals FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
      AND proposed_by = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
    );
END $$;

-- Kebijakan fakta pembayaran: SELECT sama seperti usulan; INSERT
-- mengikat approved_by = current_subject DAN menyamakan payload/
-- metadata yang disalin dengan usulan terkait (amount, currency,
-- method, reference IS NOT DISTINCT FROM, proposed_by,
-- proposed_at, draft, fakta harga) lewat EXISTS — INSERT langsung
-- dengan aktor/payload palsu DITOLAK kebijakan, bukan hanya CHECK.
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_payments_select ON rental_payments;
  CREATE POLICY rental_payments_select ON rental_payments FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_payments_insert ON rental_payments;
  CREATE POLICY rental_payments_insert ON rental_payments FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
      AND approved_by = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
      AND EXISTS (
        SELECT 1 FROM rental_payment_proposals p
        WHERE p.workspace_id = rental_payments.workspace_id
          AND p.id = rental_payments.proposal_id
          AND p.rental_contract_id = rental_payments.rental_contract_id
          AND p.rental_agreed_price_id = rental_payments.rental_agreed_price_id
          AND p.amount_minor = rental_payments.amount_minor
          AND p.currency = rental_payments.currency
          AND p.method = rental_payments.method
          AND p.reference IS NOT DISTINCT FROM rental_payments.reference
          AND p.proposed_by = rental_payments.proposed_by
          AND p.proposed_at = rental_payments.proposed_at
      )
    );
END $$;

-- Kebijakan usulan pembalikan: SELECT sama; INSERT mengikat
-- proposed_by = current_subject. Ikatan ke pembayaran target
-- dijaga FK (workspace, draft, pembayaran).
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_payment_reversal_proposals_select ON rental_payment_reversal_proposals;
  CREATE POLICY rental_payment_reversal_proposals_select ON rental_payment_reversal_proposals FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_payment_reversal_proposals_insert ON rental_payment_reversal_proposals;
  CREATE POLICY rental_payment_reversal_proposals_insert ON rental_payment_reversal_proposals FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
      AND proposed_by = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
    );
END $$;

-- Kebijakan fakta pembalikan: SELECT sama; INSERT mengikat
-- approved_by = current_subject DAN menyamakan target/alasan/
-- metadata yang disalin dengan usulan pembalikan terkait
-- (payment_id, rental_contract_id, reason, proposed_by,
-- proposed_at) lewat EXISTS.
DO $$
BEGIN
  DROP POLICY IF EXISTS rental_payment_reversals_select ON rental_payment_reversals;
  CREATE POLICY rental_payment_reversals_select ON rental_payment_reversals FOR SELECT
    USING (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
    );
  DROP POLICY IF EXISTS rental_payment_reversals_insert ON rental_payment_reversals;
  CREATE POLICY rental_payment_reversals_insert ON rental_payment_reversals FOR INSERT
    WITH CHECK (
      workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'manage_business_data')
      AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'view_workspace')
      AND approved_by = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
      AND EXISTS (
        SELECT 1 FROM rental_payment_reversal_proposals p
        WHERE p.workspace_id = rental_payment_reversals.workspace_id
          AND p.id = rental_payment_reversals.reversal_proposal_id
          AND p.rental_contract_id = rental_payment_reversals.rental_contract_id
          AND p.payment_id = rental_payment_reversals.payment_id
          AND p.reason = rental_payment_reversals.reason
          AND p.proposed_by = rental_payment_reversals.proposed_by
          AND p.proposed_at = rental_payment_reversals.proposed_at
      )
    );
END $$;

-- Hak runtime keempat tabel: SELECT + INSERT saja (tanpa UPDATE
-- penuh, DELETE, TRUNCATE). REVOKE ALL dulu supaya set hak selalu
-- sama persis dengan yang diniatkan, bukan akumulasi dari keadaan
-- sebelumnya.
REVOKE ALL PRIVILEGES ON rental_payment_proposals FROM umnia_business;
GRANT SELECT, INSERT ON rental_payment_proposals TO umnia_business;
REVOKE ALL PRIVILEGES ON rental_payments FROM umnia_business;
GRANT SELECT, INSERT ON rental_payments TO umnia_business;
REVOKE ALL PRIVILEGES ON rental_payment_reversal_proposals FROM umnia_business;
GRANT SELECT, INSERT ON rental_payment_reversal_proposals TO umnia_business;
REVOKE ALL PRIVILEGES ON rental_payment_reversals FROM umnia_business;
GRANT SELECT, INSERT ON rental_payment_reversals TO umnia_business;

-- Marker TERAKHIR, sesudah semua skema/keamanan di atas berhasil:
-- database yang gagal di tengah file ini tidak tercatat sebagai 013.
INSERT INTO schema_migrations (version) VALUES ('013_rental_payments')
ON CONFLICT (version) DO NOTHING;
