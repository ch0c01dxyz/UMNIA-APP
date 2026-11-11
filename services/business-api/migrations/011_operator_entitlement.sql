-- F1-OPERATOR-ENTITLEMENT-IMPLEMENT (kontrak R2 + amendemen mengikat A-E):
-- satu-satunya jalur grant operator adalah EXECUTE atas rutin
-- app.grant_module_entitlement (SECURITY DEFINER, pemilik NOLOGIN
-- umnia_grant_exec). Operator login TIDAK mendapat DML mentah atas
-- module_entitlements/operator_audit_events; runtime bisnis/admin TIDAK
-- mendapat EXECUTE atas rutin. Aktor audit = session_user (per-operator
-- LOGIN umnia_op_<id>, anggota grup NOLOGIN umnia_operator). Grant +
-- audit operator atomik dalam satu pemanggilan rutin, di bawah kunci
-- workspace FOR UPDATE yang sama dengan aktivasi modul. Rutin hanya
-- mengizinkan rental.basic/0.1.0 DAN memeriksa tuple katalog ada
-- (amendemen B); modul/versi terdaftar lain maupun yang tidak dikenal
-- ditolak tanpa tulis. Marker 011 diperiksa DI DALAM rutin sebelum
-- mutasi (amendemen A) — CLI tidak bisa membaca schema_migrations.
-- Idempotent: CREATE TABLE IF NOT EXISTS, kebijakan diganti dalam SATU
-- pernyataan DO (atomik), GRANT/REVOKE idempotent, marker memakai
-- ON CONFLICT DO NOTHING, rutin dibuat dalam SATU pernyataan DO
-- (atomik) supaya tidak ada jendela rutin terekspos dengan PUBLIC
-- EXECUTE — dan SELURUH file diterapkan dalam satu transaksi psql -1
-- oleh semua pemanggil (Windows/CI/Linux native; configure-postgres.sh
-- memakai staging + -1 -f). Jangan bungkus file ini dengan
-- BEGIN/COMMIT: pemanggil sudah membungkus semua file dalam satu
-- transaksi psql -1; COMMIT di sini akan memutus transaksi luar itu. Peran operator
-- (umnia_operator, umnia_grant_exec, umnia_op_<id>) dibuat skrip
-- provisioning deployment (configure-postgres.sh / initdb /
-- provision-operator.ps1), BUKAN migrasi ini — migrasi ini hanya
-- MEMVALIDASI keberadaan/atributnya sebelum mengubah apa pun, dan gagal
-- tertutup bila peran belum ada. Role adalah global klaster, sedangkan
-- skema/izin ini per-database: 011 harus diterapkan per DB bisnis yang
-- memakai grant operator (umnia_test, umnia_e2e); DB tanpa 011 tidak
-- punya rutin/EXECUTE meski peran klasternya ada (amendemen D).
-- Rollback operasional = nonaktifkan akses (login/keanggotaan/EXECUTE);
-- TIDAK PERNAH DROP/TRUNCATE/DELETE riwayat, tabel, trigger, atau
-- marker. Tanpa perubahan pada handler/API/web/registry/instalasi/
-- audit_events/readiness (main.go tetap menghitung 10 marker).

-- 0. Validasi peran SEBELUM perubahan apa pun. Gagal di sini berarti
-- tidak ada objek yang berubah (pernyataan pertama file; di bawah -1
-- seluruh transaksi dibatalkan, di autocommit belum ada yang ditulis).
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
        RAISE EXCEPTION '011_operator_entitlement: peran operator % belum dibuat provisioning (jalankan provisioning operator sebelum 011)', v_missing;
    END LOOP;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('umnia_operator', 'umnia_grant_exec')
               AND (rolcanlogin OR rolsuper OR rolbypassrls OR rolcreaterole OR rolcreatedb OR rolreplication)) THEN
        RAISE EXCEPTION '011_operator_entitlement: peran operator harus NOLOGIN/NOSUPERUSER/NOBYPASSRLS/NOCREATEROLE/NOCREATEDB/NOREPLICATION';
    END IF;
    -- Pemilik rutin tidak boleh menjadi anggota peran lain (tidak ada
    -- jalur asumsi peran menuju umnia_grant_exec).
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = 'umnia_grant_exec'::regrole) THEN
        RAISE EXCEPTION '011_operator_entitlement: umnia_grant_exec tidak boleh menjadi anggota peran lain';
    END IF;
    -- Tidak ada yang boleh menjadi anggota umnia_grant_exec: satu
    -- anggota saja sudah memberi DML mentah lewat hak pemilik rutin.
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE roleid = 'umnia_grant_exec'::regrole) THEN
        RAISE EXCEPTION '011_operator_entitlement: tidak ada peran yang boleh menjadi anggota umnia_grant_exec';
    END IF;
    -- Grup kapabilitas tidak boleh menjadi anggota peran lain (tidak
    -- ada jalur warisan hak lewat umnia_operator).
    IF EXISTS (SELECT 1 FROM pg_auth_members WHERE member = 'umnia_operator'::regrole) THEN
        RAISE EXCEPTION '011_operator_entitlement: umnia_operator tidak boleh menjadi anggota peran lain';
    END IF;
    -- Runtime bisnis/admin tidak boleh menjadi anggota grup operator
    -- maupun pemilik rutin (otoritas operator tidak boleh menyatu
    -- dengan akses aplikasi). Join berdasarkan NAMA peran, BUKAN
    -- cast ::regrole: umnia_admin adalah peran OPSIONAL — pada
    -- klaster bisnis saja (daftar peran CI segar, klaster business
    -- Windows) peran itu tidak ada, dan cast ::regrole atas peran
    -- yang tidak ada gagal (ERROR: role "umnia_admin" does not
    -- exist). Join nama aman terhadap peran yang tidak ada: bila
    -- umnia_admin tidak ada, tidak ada baris yang cocok; bila ada
    -- dan menjadi anggota operator/pemilik rutin, tetap ditolak.
    IF EXISTS (SELECT 1 FROM pg_auth_members m
                JOIN pg_roles member_role ON member_role.oid = m.member
                JOIN pg_roles granted_role ON granted_role.oid = m.roleid
               WHERE member_role.rolname IN ('umnia_business', 'umnia_admin')
                 AND granted_role.rolname IN ('umnia_operator', 'umnia_grant_exec')) THEN
        RAISE EXCEPTION '011_operator_entitlement: umnia_business/umnia_admin tidak boleh menjadi anggota peran operator';
    END IF;
    -- Setiap anggota grup kapabilitas harus LOGIN per-operator dengan
    -- atribut least-privilege (tanpa superuser/BYPASSRLS/createrole/
    -- createdb/replication). Seed yang tidak aman tidak diadopsi
    -- diam-diam: provisioning harus memperbaikinya dulu.
    IF EXISTS (SELECT 1 FROM pg_auth_members m JOIN pg_roles r ON r.oid = m.member
                WHERE m.roleid = 'umnia_operator'::regrole
                  AND (NOT r.rolcanlogin OR r.rolsuper OR r.rolbypassrls OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication)) THEN
        RAISE EXCEPTION '011_operator_entitlement: anggota umnia_operator harus LOGIN least-privilege';
    END IF;
    -- Anchor: role runtime bisnis harus ada (sanity; tidak diubah).
    IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'umnia_business') THEN
        RAISE EXCEPTION '011_operator_entitlement: peran runtime umnia_business belum ada';
    END IF;
END;
$$;

-- 1. Tabel audit operator: bootstrap-owned, append-only, RLS
-- ENABLE + FORCE. Kolom operator_id menyimpan session_user (bukan FK
-- ke identity_subjects: operator bukan subjek bisnis).
CREATE TABLE IF NOT EXISTS operator_audit_events (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces(id),
    module_id TEXT NOT NULL,
    version TEXT NOT NULL,
    operator_id TEXT NOT NULL,
    action TEXT NOT NULL CHECK (action IN ('entitlement_granted')),
    source TEXT NOT NULL,
    reason TEXT NOT NULL,
    result TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Append-only: UPDATE/DELETE ditolak di level database (fungsi khusus
-- agar pesan galat menyebut tabel yang benar).
CREATE OR REPLACE FUNCTION app.reject_operator_audit_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'operator_audit_events is append-only';
END;
$$;
DROP TRIGGER IF EXISTS operator_audit_append_only ON operator_audit_events;
CREATE TRIGGER operator_audit_append_only
    BEFORE UPDATE OR DELETE ON operator_audit_events
    FOR EACH ROW EXECUTE FUNCTION app.reject_operator_audit_change();

ALTER TABLE operator_audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE operator_audit_events FORCE ROW LEVEL SECURITY;

-- 2. Kebijakan RLS untuk peran eksekusi, diganti dalam SATU pernyataan
-- DO (atomik). Kebijakan membership/bisnis yang ada TIDAK disentuh.
-- workspaces butuh kebijakan SELECT DAN UPDATE: tabel Policies Applied
-- by Command Type (CREATE POLICY, PG16) menyatakan SELECT FOR UPDATE
-- memeriksa kebijakan SELECT (baris terlihat) DAN kebijakan UPDATE
-- (baris yang boleh dikunci). Tanpa keduanya kunci gagal.
DO $$
BEGIN
    DROP POLICY IF EXISTS workspaces_exec_operator_select ON workspaces;
    CREATE POLICY workspaces_exec_operator_select ON workspaces
        FOR SELECT TO umnia_grant_exec USING (true);
    DROP POLICY IF EXISTS workspaces_exec_operator_update ON workspaces;
    CREATE POLICY workspaces_exec_operator_update ON workspaces
        FOR UPDATE TO umnia_grant_exec USING (true);

    DROP POLICY IF EXISTS entitlements_exec_operator_select ON module_entitlements;
    CREATE POLICY entitlements_exec_operator_select ON module_entitlements
        FOR SELECT TO umnia_grant_exec USING (true);
    DROP POLICY IF EXISTS entitlements_exec_operator_insert ON module_entitlements;
    CREATE POLICY entitlements_exec_operator_insert ON module_entitlements
        FOR INSERT TO umnia_grant_exec WITH CHECK (true);

    DROP POLICY IF EXISTS operator_audit_exec_operator_select ON operator_audit_events;
    CREATE POLICY operator_audit_exec_operator_select ON operator_audit_events
        FOR SELECT TO umnia_grant_exec USING (true);
    DROP POLICY IF EXISTS operator_audit_exec_operator_insert ON operator_audit_events;
    CREATE POLICY operator_audit_exec_operator_insert ON operator_audit_events
        FOR INSERT TO umnia_grant_exec WITH CHECK (true);
END;
$$;

-- 3. Hak peran eksekusi: hanya yang dibutuhkan rutin. Tanpa DELETE/
-- UPDATE baris, tanpa BYPASSRLS/superuser. SELECT(version) atas
-- schema_migrations HANYA untuk peran eksekusi (pemeriksaan marker di
-- dalam rutin, amendemen A); CLI tidak mendapat akses itu.
GRANT USAGE ON SCHEMA app TO umnia_grant_exec;
GRANT USAGE ON SCHEMA public TO umnia_grant_exec;
GRANT USAGE ON SCHEMA app TO umnia_operator;
GRANT SELECT (id), UPDATE (id) ON workspaces TO umnia_grant_exec;
GRANT SELECT ON module_registry TO umnia_grant_exec;
GRANT SELECT, INSERT ON module_entitlements TO umnia_grant_exec;
GRANT SELECT, INSERT ON operator_audit_events TO umnia_grant_exec;
GRANT SELECT (version) ON schema_migrations TO umnia_grant_exec;
-- Hak runtime/admin TIDAK berubah: tanpa EXECUTE atas rutin, tanpa
-- akses operator_audit_events, tanpa INSERT entitlement (sudah
-- demikian sejak 004; ditegaskan di sini supaya rerun tidak melonggarkan).
REVOKE ALL ON operator_audit_events FROM umnia_business;
REVOKE ALL ON operator_audit_events FROM PUBLIC;

-- 4. Rutin grant — dibuat, dialihkan kepemilikannya, dan dikunci
-- ACL-nya dalam SATU pernyataan DO (atomik): tidak ada jendela di mana
-- rutin ada dengan PUBLIC EXECUTE, pada caller mana pun. CREATE atas
-- skema app hanya sementara untuk pengalihan kepemilikan, lalu dicabut
-- sebelum DO selesai (amendemen D).
DO $migrate011$
BEGIN
    EXECUTE 'GRANT CREATE ON SCHEMA app TO umnia_grant_exec';
    EXECUTE $func$
CREATE OR REPLACE FUNCTION app.grant_module_entitlement(
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
    v_granted_at timestamptz;
BEGIN
    -- (a) Marker 011 diperiksa DI DALAM rutin sebelum mutasi apa pun:
    -- DB tanpa marker (migrasi belum/gagal terapan) gagal tertutup.
    IF NOT EXISTS (SELECT 1 FROM public.schema_migrations WHERE version = '011_operator_entitlement') THEN
        RAISE EXCEPTION 'not_ready: operator schema is not provisioned' USING ERRCODE = 'U11NR';
    END IF;
    -- (b) Keanggotaan kapabilitas diperiksa per pemanggilan dari
    -- session_user (bukan current_user: di dalam SECURITY DEFINER
    -- current_user adalah pemilik rutin, bukan manusia).
    IF NOT pg_has_role(session_user, 'umnia_operator', 'member') THEN
        RAISE EXCEPTION 'not_ready: caller lacks operator capability' USING ERRCODE = 'U11NR';
    END IF;
    -- (c) Batas input: NULL ditolak; trim; panjang sesudah trim.
    IF p_workspace_id IS NULL OR p_module_id IS NULL OR p_version IS NULL OR p_reason IS NULL THEN
        RAISE EXCEPTION 'invalid: missing input' USING ERRCODE = 'U11IV';
    END IF;
    v_reason := btrim(p_reason);
    IF btrim(p_module_id) = '' OR char_length(btrim(p_module_id)) > 128
       OR btrim(p_version) = '' OR char_length(btrim(p_version)) > 64
       OR v_reason = '' OR char_length(v_reason) > 500 THEN
        RAISE EXCEPTION 'invalid: input out of bounds' USING ERRCODE = 'U11IV';
    END IF;
    -- (d) Modul harus rental.basic; versi yang diminta harus 0.1.0
    -- (satu-satunya versi beku). Keduanya ditolak di sini, sebelum
    -- kunci/baris dibaca — grant legacy 0.2.0 yang sudah ada tidak
    -- membuat permintaan 0.2.0 menjadi valid.
    IF btrim(p_module_id) <> 'rental.basic' THEN
        RAISE EXCEPTION 'invalid: unsupported module/version' USING ERRCODE = 'U11IV';
    END IF;
    -- (e) Kunci baris workspace (kunci yang sama dengan aktivasi).
    PERFORM 1 FROM public.workspaces WHERE id = p_workspace_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'not_found: workspace' USING ERRCODE = 'U11NF';
    END IF;
    -- (e2) Gate versi SEBELUM cabang baris yang ada: permintaan versi
    -- selain 0.1.0 selalu invalid, apa pun isi baris yang ada.
    IF btrim(p_version) <> '0.1.0' THEN
        RAISE EXCEPTION 'invalid: unsupported module/version' USING ERRCODE = 'U11IV';
    END IF;
    -- (f) Baris entitlement yang ada (modul rental.basic).
    SELECT version, granted_by, granted_at, source, reason INTO v_existing_version, v_existing_granted_by, v_existing_granted_at, v_existing_source, v_existing_reason
      FROM public.module_entitlements
     WHERE workspace_id = p_workspace_id AND module_id = 'rental.basic';
    IF FOUND THEN
        IF v_existing_version <> btrim(p_version) THEN
            RAISE EXCEPTION 'conflict: different version already granted' USING ERRCODE = 'U11CF';
        END IF;
        -- audit_present mencerminkan event grant ASLI untuk baris
        -- ini: aktor, source, reason, DAN waktu harus sama persis
        -- dengan provenance baris entitlement (now() stabil dalam
        -- satu transaksi, jadi created_at audit buatan rutin sama
        -- dengan granted_at). Event grant lain — walau workspace/
        -- modul/versi/action sama — tidak dihitung.
        SELECT EXISTS (SELECT 1 FROM public.operator_audit_events
                        WHERE workspace_id = p_workspace_id
                          AND module_id = 'rental.basic'
                          AND version = v_existing_version
                          AND action = 'entitlement_granted'
                          AND ('operator:' || operator_id) = v_existing_granted_by
                          AND source = v_existing_source
                          AND reason = v_existing_reason
                          AND created_at = v_existing_granted_at)
          INTO v_audit_present;
        IF v_audit_present THEN
            RETURN QUERY SELECT 'already_granted', p_workspace_id, 'rental.basic'::text, v_existing_version, v_existing_granted_by, v_existing_granted_at, true;
        ELSE
            RETURN QUERY SELECT 'legacy_existing', p_workspace_id, 'rental.basic'::text, v_existing_version, v_existing_granted_by, v_existing_granted_at, false;
        END IF;
        RETURN;
    END IF;
    -- (g) Grant baru: tuple katalog harus ada (versi sudah divalidasi
    -- di (e2)).
    IF NOT EXISTS (SELECT 1 FROM public.module_registry WHERE module_id = 'rental.basic' AND version = '0.1.0') THEN
        RAISE EXCEPTION 'not_found: catalog tuple missing' USING ERRCODE = 'U11NF';
    END IF;
    -- (h) Grant baru + audit atomik (satu pernyataan = satu transaksi).
    INSERT INTO public.module_entitlements (workspace_id, module_id, version, granted_by, source, reason)
    VALUES (p_workspace_id, 'rental.basic', '0.1.0', 'operator:' || v_actor, 'operator-cli', v_reason)
    RETURNING granted_at INTO v_granted_at;
    INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result)
    VALUES (p_workspace_id, 'rental.basic', '0.1.0', v_actor, 'entitlement_granted', 'operator-cli', v_reason, 'granted');
    RETURN QUERY SELECT 'granted', p_workspace_id, 'rental.basic'::text, '0.1.0'::text, 'operator:' || v_actor, v_granted_at, true;
END;
$body$;
    $func$;
    EXECUTE 'ALTER FUNCTION app.grant_module_entitlement(uuid, text, text, text) OWNER TO umnia_grant_exec';
    EXECUTE 'REVOKE ALL ON FUNCTION app.grant_module_entitlement(uuid, text, text, text) FROM PUBLIC';
    EXECUTE 'GRANT EXECUTE ON FUNCTION app.grant_module_entitlement(uuid, text, text, text) TO umnia_operator';
    EXECUTE 'REVOKE CREATE ON SCHEMA app FROM umnia_grant_exec';
END;
$migrate011$;

-- 5. Marker 011 (idempotent). Ditempatkan SETELAH rutin: DB yang
-- rutinnya ada tanpa marker tetap gagal tertutup lewat pemeriksaan
-- marker di dalam rutin (amendemen A).
INSERT INTO schema_migrations (version) VALUES ('011_operator_entitlement')
ON CONFLICT (version) DO NOTHING;
