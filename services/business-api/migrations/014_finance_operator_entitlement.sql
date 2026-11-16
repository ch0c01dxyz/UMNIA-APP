-- F2-02-B-CONTRACT (implementasi; keputusan beku 2026-11-16):
-- dua rutin Finance baru di migrasi 014; 011 TIDAK disentuh. Rutin
-- app.grant_finance_entitlement (uuid, text, text, text) dan
-- app.revoke_finance_entitlement (uuid, text, text) adalah SECURITY
-- DEFINER milik NOLOGIN umnia_grant_exec; EXECUTE hanya untuk grup
-- NOLOGIN umnia_operator (roster/grup yang sama dengan Rental — ini
-- sengaja memperluas otoritas operator yang sama ke penerbitan/
-- pencabutan Finance). Aktor audit = session_user (per-operator LOGIN
-- umnia_op_<id>). Grant/revoke + audit operator atomik dalam satu
-- pemanggilan rutin, di bawah kunci workspace FOR UPDATE yang sama
-- dengan aktivasi modul dan pembayaran. Kedua rutin hanya mengizinkan
-- finance.basic/0.1.0; modul/versi lain ditolak tanpa tulis. Marker
-- 011 DAN 014 diperiksa DI DALAM kedua rutin sebelum mutasi — CLI
-- tidak bisa membaca schema_migrations. Grant memakai jam DB yang
-- ditangkap sekali sesudah kunci dan dibagikan eksplisit antara baris
-- entitlement, event audit, dan hasil. Revoke MENGHAPUS baris
-- snapshot 0.1.0 (bukan menonaktifkan): deaktivasi owner saja tidak
-- mencabut hak, karena re-aktivasi langsung lolos tanpa operator.
-- Karena itu 014 memberi DELETE hanya atas module_entitlements kepada
-- pemilik rutin, plus kebijakan DELETE FORCE-RLS yang dibatasi
-- module_id='finance.basic' AND version='0.1.0'; kebijakan
-- SELECT/INSERT yang ada TIDAK disentuh, dan TIDAK ada DML operator/
-- grup/runtime, UPDATE entitlement, atau UPDATE/DELETE/TRUNCATE
-- audit. CHECK action audit diperluas secara atomik dengan
-- 'entitlement_revoked'. Riwayat finansial, instalasi, dan audit
-- TIDAK disentuh revoke. Idempotent: kebijakan diganti dalam SATU
-- pernyataan DO (atomik), GRANT/REVOKE idempotent, marker memakai
-- ON CONFLICT DO NOTHING, kedua rutin dibuat dalam SATU pernyataan
-- DO (atomik) supaya tidak ada jendela rutin terekspos dengan PUBLIC
-- EXECUTE — dan SELURUH file diterapkan dalam satu transaksi psql -1
-- oleh semua pemanggil. Jangan bungkus file ini dengan BEGIN/COMMIT:
-- pemanggil sudah membungkus semua file dalam satu transaksi psql -1;
-- COMMIT di sini akan memutus transaksi luar itu. Peran operator
-- dibuat skrip provisioning deployment (configure-postgres.sh /
-- initdb / provision-operator.ps1), BUKAN migrasi ini — migrasi ini
-- hanya MEMVALIDASI keberadaan/atributnya sebelum mengubah apa pun,
-- dan gagal tertutup bila peran belum ada. 014 harus diterapkan per
-- DB bisnis yang memakai grant/revoke Finance (umnia_test,
-- umnia_e2e); DB tanpa 014 tidak punya rutin/EXECUTE meski peran
-- klasternya ada. Rollback operasional = nonaktifkan akses
-- (login/keanggotaan/EXECUTE); TIDAK PERNAH DROP/TRUNCATE/DELETE
-- riwayat, tabel, trigger, atau marker. Tanpa perubahan pada
-- handler/API/web/registry/instalasi/audit_events/readiness
-- (main.go tetap menghitung 12 marker; 014 BUKAN marker readiness).

-- 0. Validasi peran SEBELUM perubahan apa pun. Gagal di sini berarti
-- tidak ada objek yang berubah (pernyataan pertama file; di bawah -1
-- seluruh transaksi dibatalkan, di autocommit belum ada yang ditulis).
-- Sama dengan 011 §0 (disalin, bukan dirujuk: 014 harus berdiri
-- sendiri saat dibaca/diaudit).
DO $$
DECLARE
    v_missing text;
BEGIN
    -- Kedua peran NOLOGIN wajib ada dengan atribut least-privilege.
    -- Login per-operator (umnia_op_<id>) TIDAK divalidasi di sini:
    -- roster boleh kosong, dan login dibuat provisioning.
    FOR v_missing IN
        SELECT rolname FROM (VALUES ('umnia_operator'), ('umnia_grant_exec')) AS need(rolname)
        WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE pg_roles.rolname = need.rolname)
    LOOP
        RAISE EXCEPTION '014_finance_operator_entitlement: peran operator % belum dibuat provisioning (jalankan provisioning operator sebelum 014)', v_missing;
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('umnia_operator', 'umnia_grant_exec')
               AND (rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication)) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: peran operator harus NOLOGIN/NOSUPERUSER/NOBYPASSRLS/NOCREATEROLE/NOCREATEDB/NOREPLICATION';
    END IF;
    -- Pemilik rutin tidak boleh menjadi anggota peran lain (tidak ada
    -- jalur asumsi peran menuju umnia_grant_exec).
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = 'umnia_grant_exec'::regrole) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: umnia_grant_exec tidak boleh menjadi anggota peran lain';
    END IF;
    -- Tidak ada yang boleh menjadi anggota umnia_grant_exec: satu
    -- anggota saja sudah memberi DML mentah lewat hak pemilik rutin.
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE roleid = 'umnia_grant_exec'::regrole) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: tidak ada peran yang boleh menjadi anggota umnia_grant_exec';
    END IF;
    -- Grup kapabilitas tidak boleh menjadi anggota peran lain (tidak
    -- ada jalur warisan hak lewat umnia_operator).
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = 'umnia_operator'::regrole) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: umnia_operator tidak boleh menjadi anggota peran lain';
    END IF;
    -- Runtime bisnis/admin tidak boleh menjadi anggota grup operator
    -- maupun pemilik rutin (otoritas operator tidak boleh menyatu
    -- dengan akses aplikasi). Join berdasarkan NAMA peran, BUKAN
    -- cast ::regrole: umnia_admin adalah peran OPSIONAL — pada
    -- klaster bisnis saja peran itu tidak ada, dan cast ::regrole
    -- atas peran yang tidak ada gagal. Join nama aman terhadap peran
    -- yang tidak ada.
    IF EXISTS (SELECT 1 FROM pg_auth_members m
                JOIN pg_roles member_role ON member_role.oid = m.member
                JOIN pg_roles granted_role ON granted_role.oid = m.roleid
               WHERE member_role.rolname IN ('umnia_business', 'umnia_admin')
                 AND granted_role.rolname IN ('umnia_operator', 'umnia_grant_exec')) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: umnia_business/umnia_admin tidak boleh menjadi anggota peran operator';
    END IF;
    -- Setiap anggota grup kapabilitas harus LOGIN per-operator dengan
    -- atribut least-privilege.
    IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member
                WHERE m.roleid = 'umnia_operator'::regrole
                  AND (NOT r.rolcanlogin OR r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication)) THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: anggota umnia_operator harus LOGIN least-privilege';
    END IF;
    -- Anchor: role runtime bisnis harus ada (sanity; tidak diubah).
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_business') THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: peran runtime umnia_business belum ada';
    END IF;
END;
$$;

-- 1. Perluas CHECK action audit operator secara atomik: ganti HANYA
-- CHECK action yang dikenal — tanpa nama dari 011
-- (operator_audit_events_action_check) atau bernama dari rerun 014
-- (operator_audit_action_check) — dan hanya bila definisinya sama
-- persis dengan bentuk action-only kanonis yang diharapkan; lalu
-- tambah SATU constraint bernama yang memuat kedua action. CHECK
-- lain (mis. buatan QA) TIDAK PERNAH disentuh. Idempotent: pada
-- rerun, constraint bernama yang sudah ada ikut terhapus lalu
-- ditambahkan kembali dengan definisi yang sama. Gagal tertutup bila
-- 011 belum diterapkan (tabel audit belum ada), bila tidak ada CHECK
-- action yang dikenal, atau bila nama yang dikenal punya definisi
-- tak dikenal (tabrakan).
DO $$
DECLARE
    v_names text[];
    v_defs text[];
    v_found boolean := false;
    i int;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '011_operator_entitlement') THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: 011 belum diterapkan (terapkan 011 sebelum 014)';
    END IF;
    -- Hanya dua nama yang dikenal yang boleh diganti. Definisi tiap
    -- nama HARUS sama persis dengan bentuk action-only kanonis PG16
    -- yang diharapkan; nama yang dikenal dengan definisi lain =
    -- tabrakan -> gagal tertutup. CHECK lain apa pun (mis. buatan QA)
    -- TIDAK PERNAH disentuh. Nama/definisi dikumpulkan dulu ke
    -- array; drop TIDAK PERNAH dilakukan sambil mengiterasi kursor
    -- atas pg_constraint.
    SELECT array_agg(conname ORDER BY conname), array_agg(pg_get_constraintdef(oid) ORDER BY conname)
      INTO v_names, v_defs
      FROM pg_constraint
     WHERE conrelid = 'public.operator_audit_events'::regclass
       AND contype = 'c'
       AND conname IN ('operator_audit_events_action_check', 'operator_audit_action_check');
    IF v_names IS NOT NULL THEN
        FOR i IN 1 .. array_length(v_names, 1) LOOP
            v_found := true;
            IF v_names[i] = 'operator_audit_events_action_check'
               AND v_defs[i] <> 'CHECK ((action = ''entitlement_granted''::text))' THEN
                RAISE EXCEPTION '014_finance_operator_entitlement: CHECK 011 punya definisi tak dikenal; gagal tertutup';
            END IF;
            IF v_names[i] = 'operator_audit_action_check'
               AND v_defs[i] <> 'CHECK ((action = ANY (ARRAY[''entitlement_granted''::text, ''entitlement_revoked''::text])))' THEN
                RAISE EXCEPTION '014_finance_operator_entitlement: CHECK 014 punya definisi tak dikenal; gagal tertutup';
            END IF;
            EXECUTE format('ALTER TABLE public.operator_audit_events DROP CONSTRAINT %I', v_names[i]);
        END LOOP;
    END IF;
    IF NOT v_found THEN
        RAISE EXCEPTION '014_finance_operator_entitlement: CHECK action audit 011 tidak ditemukan; gagal tertutup';
    END IF;
    ALTER TABLE public.operator_audit_events
      ADD CONSTRAINT operator_audit_action_check
      CHECK (action IN ('entitlement_granted', 'entitlement_revoked'));
END;
$$;

-- 2. DELETE terbatas untuk pemilik rutin + kebijakan DELETE FORCE-RLS
-- yang dibatasi finance.basic/0.1.0. Kebijakan SELECT/INSERT dari
-- 011 TIDAK disentuh. RLS sudah ENABLE + FORCE sejak 004, jadi
-- kebijakan ini adalah satu-satunya jalur DELETE yang bisa dipakai
-- pemilik rutin — dan hanya untuk snapshot Finance 0.1.0.
GRANT DELETE ON module_entitlements TO umnia_grant_exec;
DO $$
BEGIN
    DROP POLICY IF EXISTS entitlements_exec_owner_delete_finance ON module_entitlements;
    CREATE POLICY entitlements_exec_owner_delete_finance ON module_entitlements
        FOR DELETE TO umnia_grant_exec
        USING (module_id = 'finance.basic' AND version = '0.1.0');
END;
$$;

-- 3. Kedua rutin Finance — dibuat, dialihkan kepemilikannya, dan
-- dikunci ACL-nya dalam SATU pernyataan DO (atomik): tidak ada
-- jendela di mana rutin ada dengan PUBLIC EXECUTE, pada caller mana
-- pun. CREATE atas skema app hanya sementara untuk pengalihan
-- kepemilikan, lalu dicabut sebelum DO selesai.
DO $migrate014$
BEGIN
    EXECUTE 'GRANT CREATE ON SCHEMA app TO umnia_grant_exec';
    EXECUTE $func$
CREATE OR REPLACE FUNCTION app.grant_finance_entitlement(
    p_workspace_id uuid,
    p_module_id text,
    p_version text,
    p_reason text
)
RETURNS TABLE(result text, workspace_id uuid, module_id text, version text, granted_by text, granted_at timestamptz, audit_present boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $body$
#variable_conflict use_column
DECLARE
    v_actor text := session_user;
    v_reason text;
    v_existing_version text;
    v_existing_granted_by text;
    v_existing_granted_at timestamptz;
    v_existing_source text;
    v_existing_reason text;
    v_audit_present boolean;
    v_now timestamptz;
BEGIN
    -- (a) Marker 011 DAN 014 diperiksa DI DALAM rutin sebelum mutasi
    -- apa pun: DB tanpa salah satu marker gagal tertutup.
    IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '011_operator_entitlement')
       OR NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '014_finance_operator_entitlement') THEN
        RAISE EXCEPTION 'not_ready: finance operator schema is not provisioned' USING ERRCODE = 'U14NR';
    END IF;
    -- (b) Keanggotaan kapabilitas diperiksa per pemanggilan dari
    -- session_user (bukan current_user: di dalam SECURITY DEFINER
    -- current_user adalah pemilik rutin, bukan manusia).
    IF NOT pg_has_role(session_user, 'umnia_operator', 'member') THEN
        RAISE EXCEPTION 'not_ready: caller lacks operator capability' USING ERRCODE = 'U14NR';
    END IF;
    -- (c) Batas input: NULL ditolak; trim; panjang sesudah trim.
    IF p_workspace_id IS NULL OR p_module_id IS NULL OR p_version IS NULL OR p_reason IS NULL THEN
        RAISE EXCEPTION 'invalid: missing input' USING ERRCODE = 'U14IV';
    END IF;
    v_reason := btrim(p_reason);
    IF btrim(p_module_id) = '' OR char_length(btrim(p_module_id)) > 128
       OR btrim(p_version) = '' OR char_length(btrim(p_version)) > 64
       OR v_reason = '' OR char_length(v_reason) > 500 THEN
        RAISE EXCEPTION 'invalid: input out of bounds' USING ERRCODE = 'U14IV';
    END IF;
    -- (d) Modul harus finance.basic. Ditolak di sini, sebelum
    -- kunci/baris dibaca.
    IF btrim(p_module_id) <> 'finance.basic' THEN
        RAISE EXCEPTION 'invalid: unsupported module/version' USING ERRCODE = 'U14IV';
    END IF;
    -- (e) Kunci baris workspace (kunci yang sama dengan aktivasi dan
    -- pembayaran).
    PERFORM 1 FROM public.workspaces WHERE id = p_workspace_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'not_found: workspace' USING ERRCODE = 'U14NF';
    END IF;
    -- (e2) Gate versi SEBELUM cabang baris yang ada: permintaan versi
    -- selain 0.1.0 selalu invalid, apa pun isi baris yang ada.
    IF btrim(p_version) <> '0.1.0' THEN
        RAISE EXCEPTION 'invalid: unsupported module/version' USING ERRCODE = 'U14IV';
    END IF;
    -- (f) Baris entitlement yang ada (modul finance.basic).
    SELECT version, granted_by, granted_at, source, reason INTO v_existing_version, v_existing_granted_by, v_existing_granted_at, v_existing_source, v_existing_reason
      FROM public.module_entitlements
     WHERE workspace_id = p_workspace_id AND module_id = 'finance.basic';
    IF FOUND THEN
        IF v_existing_version <> btrim(p_version) THEN
            RAISE EXCEPTION 'conflict: different version already granted' USING ERRCODE = 'U14CF';
        END IF;
        -- audit_present mencerminkan event grant ASLI untuk baris
        -- ini: aktor, source, reason, DAN waktu harus sama persis
        -- dengan provenance baris entitlement. Event grant lain —
        -- walau workspace/modul/versi/action sama — tidak dihitung.
        -- Regrant sesudah revoke membuat baris + audit baru; event
        -- lama tetap ada tapi tidak cocok dengan provenance baru.
        SELECT EXISTS (SELECT 1 FROM public.operator_audit_events
                        WHERE workspace_id = p_workspace_id
                          AND module_id = 'finance.basic'
                          AND version = v_existing_version
                          AND action = 'entitlement_granted'
                          AND ('operator:' || operator_id) = v_existing_granted_by
                          AND source = v_existing_source
                          AND reason = v_existing_reason
                          AND created_at = v_existing_granted_at)
          INTO v_audit_present;
        IF v_audit_present THEN
            RETURN QUERY SELECT 'already_granted', p_workspace_id, 'finance.basic'::text, v_existing_version, v_existing_granted_by, v_existing_granted_at, true;
        ELSE
            RETURN QUERY SELECT 'legacy_existing', p_workspace_id, 'finance.basic'::text, v_existing_version, v_existing_granted_by, v_existing_granted_at, false;
        END IF;
        RETURN;
    END IF;
    -- (g) Grant baru: tuple katalog harus ada (versi sudah divalidasi
    -- di (e2)).
    IF NOT EXISTS (SELECT 1 FROM public.module_registry WHERE module_id = 'finance.basic' AND version = '0.1.0') THEN
        RAISE EXCEPTION 'not_found: catalog tuple missing' USING ERRCODE = 'U14NF';
    END IF;
    -- (h) Grant baru + audit atomik. Jam DB ditangkap SEKALI sesudah
    -- kunci dan dibagikan eksplisit antara baris entitlement, event
    -- audit, dan hasil — bukan dua pemanggilan now() yang terpisah.
    v_now := clock_timestamp();
    INSERT INTO public.module_entitlements (workspace_id, module_id, version, granted_at, granted_by, source, reason)
    VALUES (p_workspace_id, 'finance.basic', '0.1.0', v_now, 'operator:' || v_actor, 'operator-cli', v_reason);
    INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result, created_at)
    VALUES (p_workspace_id, 'finance.basic', '0.1.0', v_actor, 'entitlement_granted', 'operator-cli', v_reason, 'granted', v_now);
    RETURN QUERY SELECT 'granted', p_workspace_id, 'finance.basic'::text, '0.1.0'::text, 'operator:' || v_actor, v_now, true;
END;
$body$;
    $func$;
    EXECUTE $func$
CREATE OR REPLACE FUNCTION app.revoke_finance_entitlement(
    p_workspace_id uuid,
    p_module_id text,
    p_reason text
)
RETURNS TABLE(result text, workspace_id uuid, module_id text, version text, revoked_by text, revoked_at timestamptz, audit_present boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, pg_temp
AS $body$
#variable_conflict use_column
DECLARE
    v_actor text := session_user;
    v_reason text;
    v_existing_version text;
    v_now timestamptz;
    v_deleted int;
BEGIN
    -- (a) Marker 011 DAN 014 diperiksa DI DALAM rutin sebelum mutasi
    -- apa pun.
    IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '011_operator_entitlement')
       OR NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '014_finance_operator_entitlement') THEN
        RAISE EXCEPTION 'not_ready: finance operator schema is not provisioned' USING ERRCODE = 'U14NR';
    END IF;
    -- (b) Keanggotaan kapabilitas diperiksa per pemanggilan dari
    -- session_user.
    IF NOT pg_has_role(session_user, 'umnia_operator', 'member') THEN
        RAISE EXCEPTION 'not_ready: caller lacks operator capability' USING ERRCODE = 'U14NR';
    END IF;
    -- (c) Batas input: NULL ditolak; trim; panjang sesudah trim.
    IF p_workspace_id IS NULL OR p_module_id IS NULL OR p_reason IS NULL THEN
        RAISE EXCEPTION 'invalid: missing input' USING ERRCODE = 'U14IV';
    END IF;
    v_reason := btrim(p_reason);
    IF btrim(p_module_id) = '' OR char_length(btrim(p_module_id)) > 128
       OR v_reason = '' OR char_length(v_reason) > 500 THEN
        RAISE EXCEPTION 'invalid: input out of bounds' USING ERRCODE = 'U14IV';
    END IF;
    -- (d) Modul harus finance.basic. Ditolak di sini, sebelum
    -- kunci/baris dibaca — revoke rental TIDAK didukung rutin ini.
    IF btrim(p_module_id) <> 'finance.basic' THEN
        RAISE EXCEPTION 'invalid: unsupported module' USING ERRCODE = 'U14IV';
    END IF;
    -- (e) Kunci baris workspace (kunci yang sama dengan aktivasi dan
    -- pembayaran).
    PERFORM 1 FROM public.workspaces WHERE id = p_workspace_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'not_found: workspace' USING ERRCODE = 'U14NF';
    END IF;
    -- (f) Baris entitlement Finance yang ada. Tidak ada baris =
    -- already_revoked (ketidakhadiran saat ini, BUKAN bukti revoke
    -- historis): tanpa tulis, tanpa audit.
    SELECT version INTO v_existing_version
      FROM public.module_entitlements
     WHERE workspace_id = p_workspace_id AND module_id = 'finance.basic';
    IF NOT FOUND THEN
        RETURN QUERY SELECT 'already_revoked', p_workspace_id, 'finance.basic'::text, '0.1.0'::text, NULL::text, NULL::timestamptz, false;
        RETURN;
    END IF;
    -- (g) Versi lain yang ada (mis. legacy 0.2.0) = konflik, tanpa
    -- tulis: revoke ini hanya menghapus snapshot 0.1.0.
    IF v_existing_version <> '0.1.0' THEN
        RAISE EXCEPTION 'conflict: different version already granted' USING ERRCODE = 'U14CF';
    END IF;
    -- (h) Hapus + audit atomik. Jam DB ditangkap SEKALI sesudah kunci
    -- dan dibagikan antara event audit dan hasil. Jumlah baris yang
    -- terhapus HARUS tepat 1: DELETE nol-baris (mis. tersaring RLS
    -- yang tak terduga) tidak boleh diam-diam sukses.
    v_now := clock_timestamp();
    DELETE FROM public.module_entitlements
     WHERE workspace_id = p_workspace_id AND module_id = 'finance.basic' AND version = '0.1.0';
    GET DIAGNOSTICS v_deleted = ROW_COUNT;
    IF v_deleted <> 1 THEN
        RAISE EXCEPTION 'revoke failed: entitlement row not deleted';
    END IF;
    INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result, created_at)
    VALUES (p_workspace_id, 'finance.basic', '0.1.0', v_actor, 'entitlement_revoked', 'operator-cli', v_reason, 'revoked', v_now);
    RETURN QUERY SELECT 'revoked', p_workspace_id, 'finance.basic'::text, '0.1.0'::text, 'operator:' || v_actor, v_now, true;
END;
$body$;
    $func$;
    EXECUTE 'ALTER FUNCTION app.grant_finance_entitlement(uuid, text, text, text) OWNER TO umnia_grant_exec';
    EXECUTE 'ALTER FUNCTION app.revoke_finance_entitlement(uuid, text, text) OWNER TO umnia_grant_exec';
    EXECUTE 'REVOKE ALL ON FUNCTION app.grant_finance_entitlement(uuid, text, text, text) FROM PUBLIC';
    EXECUTE 'REVOKE ALL ON FUNCTION app.revoke_finance_entitlement(uuid, text, text) FROM PUBLIC';
    EXECUTE 'GRANT EXECUTE ON FUNCTION app.grant_finance_entitlement(uuid, text, text, text) TO umnia_operator';
    EXECUTE 'GRANT EXECUTE ON FUNCTION app.revoke_finance_entitlement(uuid, text, text) TO umnia_operator';
    EXECUTE 'REVOKE CREATE ON SCHEMA app FROM umnia_grant_exec';
END;
$migrate014$;

-- 4. Marker 014 (idempotent). Ditempatkan SETELAH kedua rutin: DB
-- yang rutinnya ada tanpa marker tetap gagal tertutup lewat
-- pemeriksaan marker di dalam rutin.
INSERT INTO schema_migrations (version) VALUES ('014_finance_operator_entitlement')
ON CONFLICT (version) DO NOTHING;
