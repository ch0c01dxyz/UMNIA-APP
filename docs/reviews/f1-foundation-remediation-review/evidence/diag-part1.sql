-- F1-03: identity, workspace, membership, RBAC, undangan, audit, RLS.
--
-- Dijalankan sebagai postgres (lihat deploy/linux/configure-postgres.sh),
-- lalu kepemilikan diserahkan ke umnia_business di akhir file supaya API
-- (yang terhubung sebagai umnia_business) bisa membaca/menulis tabel ini.
-- Idempotent: aman dijalankan ulang (IF NOT EXISTS / ON CONFLICT).

CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE SCHEMA IF NOT EXISTS app;
-- Role aplikasi harus bisa memanggil fungsi helper di skema app.
GRANT USAGE ON SCHEMA app TO umnia_business;

CREATE TABLE IF NOT EXISTS identity_subjects (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS identity_subjects_email_lower_idx
    ON identity_subjects (lower(email));

CREATE TABLE IF NOT EXISTS workspaces (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name             TEXT NOT NULL,
    owner_subject_id UUID NOT NULL REFERENCES identity_subjects (id),
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS workspace_memberships (
    workspace_id UUID NOT NULL REFERENCES workspaces (id),
    subject_id   UUID NOT NULL REFERENCES identity_subjects (id),
    status       TEXT NOT NULL CHECK (status IN ('pending', 'active', 'suspended')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (workspace_id, subject_id)
);

CREATE TABLE IF NOT EXISTS roles (
    name        TEXT PRIMARY KEY,
    description TEXT NOT NULL DEFAULT ''
);

CREATE TABLE IF NOT EXISTS role_permissions (
    role       TEXT NOT NULL REFERENCES roles (name),
    permission TEXT NOT NULL,
    PRIMARY KEY (role, permission)
);

CREATE TABLE IF NOT EXISTS membership_roles (
    workspace_id UUID NOT NULL,
    subject_id   UUID NOT NULL,
    role         TEXT NOT NULL REFERENCES roles (name),
    PRIMARY KEY (workspace_id, subject_id, role),
    FOREIGN KEY (workspace_id, subject_id)
        REFERENCES workspace_memberships (workspace_id, subject_id)
);

CREATE TABLE IF NOT EXISTS sessions (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subject_id UUID NOT NULL REFERENCES identity_subjects (id),
    token_hash TEXT NOT NULL UNIQUE,
    expires_at TIMESTAMPTZ NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS staff_invites (
    id         UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id UUID NOT NULL REFERENCES workspaces (id),
    email      TEXT NOT NULL,
    role       TEXT NOT NULL REFERENCES roles (name),
    token_hash TEXT NOT NULL UNIQUE,
    invited_by UUID NOT NULL REFERENCES identity_subjects (id),
    expires_at TIMESTAMPTZ NOT NULL,
    used_at    TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS audit_events (
    id               UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id     UUID NOT NULL REFERENCES workspaces (id),
    actor_subject_id UUID NOT NULL REFERENCES identity_subjects (id),
    action           TEXT NOT NULL,
    entity_type      TEXT NOT NULL DEFAULT '',
    entity_id        UUID,
    details          JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Seed role + izin minimal F1-03.
INSERT INTO roles (name, description) VALUES
    ('owner', 'Pemilik workspace'),
    ('manager', 'Manajer workspace'),
    ('cashier', 'Kasir')
ON CONFLICT (name) DO NOTHING;

INSERT INTO role_permissions (role, permission) VALUES
    ('owner', 'invite_members'),
    ('owner', 'view_workspace'),
    ('owner', 'view_members'),
    ('owner', 'view_audit'),
    ('manager', 'invite_members'),
    ('manager', 'view_workspace'),
    ('manager', 'view_members'),
    ('manager', 'view_audit'),
    ('cashier', 'view_workspace')
ON CONFLICT (role, permission) DO NOTHING;

-- Audit append-only: UPDATE/DELETE ditolak di level database.
CREATE OR REPLACE FUNCTION app.reject_audit_change()
RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;
DROP TRIGGER IF EXISTS audit_events_append_only ON audit_events;
CREATE TRIGGER audit_events_append_only
    BEFORE UPDATE OR DELETE ON audit_events
    FOR EACH ROW EXECUTE FUNCTION app.reject_audit_change();

-- Helper RLS: SECURITY DEFINER milik postgres (superuser) supaya
-- pembacaan membership tidak terkena RLS tabel itu sendiri (dan tidak
-- rekursif). Superuser melewati RLS bahkan yang di-FORCE.
CREATE OR REPLACE FUNCTION app.is_member(ws UUID, subj UUID)
RETURNS BOOLEAN LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT EXISTS (
        SELECT 1 FROM workspace_memberships
        WHERE workspace_id = ws AND subject_id = subj AND status = 'active'
    );
$$;

CREATE OR REPLACE FUNCTION app.has_permission(ws UUID, subj UUID, perm TEXT)
RETURNS BOOLEAN LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT app.is_member(ws, subj) AND EXISTS (
        SELECT 1 FROM membership_roles mr
        JOIN role_permissions rp ON rp.role = mr.role
        WHERE mr.workspace_id = ws AND mr.subject_id = subj
          AND rp.permission = perm
    );
$$;

-- Login dan autentikasi sesi harus bisa mencari subjek sebelum ada subjek
-- aktif di konteks, jadi lewat fungsi SECURITY DEFINER ini, bukan query
-- langsung yang akan disaring RLS identity_subjects/sessions.
CREATE OR REPLACE FUNCTION app.subject_by_email(p_email TEXT)
RETURNS TABLE (id UUID, password_hash TEXT)
LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT id, password_hash FROM identity_subjects
    WHERE lower(email) = lower(p_email);
$$;

-- Registrasi lewat fungsi ini karena INSERT langsung ke identity_subjects
-- ditolak RLS yang di-FORCE (kebijakan self_only tidak punya WITH CHECK).
-- Duplikat email gagal oleh unique index lower(email).
CREATE OR REPLACE FUNCTION app.register_subject(p_email TEXT, p_password_hash TEXT)
RETURNS UUID LANGUAGE sql SECURITY DEFINER AS $$
    INSERT INTO identity_subjects (email, password_hash)
    VALUES (p_email, p_password_hash)
    RETURNING id;
$$;

CREATE OR REPLACE FUNCTION app.session_subject(p_token_hash TEXT)
RETURNS UUID LANGUAGE sql STABLE SECURITY DEFINER AS $$
    SELECT subject_id FROM sessions
    WHERE token_hash = p_token_hash AND expires_at > now();
$$;

-- Terima undangan: satu fungsi SECURITY DEFINER karena penerima belum
-- menjadi anggota (RLS akan menolak INSERT membership-nya sendiri).
-- Sekali pakai dijamin atomik oleh UPDATE ... WHERE used_at IS NULL:
-- terima kedua yang bersamaan hanya satu yang mendapat baris.
-- Email penerima harus cocok dengan email undangan.
CREATE OR REPLACE FUNCTION app.accept_invite(p_workspace_id UUID, p_subject_id UUID, p_token_hash TEXT)
RETURNS TEXT LANGUAGE plpgsql SECURITY DEFINER AS $$
DECLARE
    v_invite_id UUID;
    v_role TEXT;
    v_email TEXT;
    v_subject_email TEXT;
BEGIN
    -- Cek dulu (termasuk kecocokan email) supaya undangan yang sah tidak
    -- hangus oleh percobaan terima dengan akun yang salah.
    SELECT id, role, email INTO v_invite_id, v_role, v_email
    FROM staff_invites
    WHERE workspace_id = p_workspace_id
      AND token_hash = p_token_hash
      AND used_at IS NULL
      AND expires_at > now();
    IF v_invite_id IS NULL THEN
        RAISE EXCEPTION 'invite invalid, expired, or already used';
    END IF;
    SELECT email INTO v_subject_email FROM identity_subjects WHERE id = p_subject_id;
    IF v_subject_email IS NULL OR lower(v_subject_email) <> lower(v_email) THEN
        RAISE EXCEPTION 'invite email does not match subject';
    END IF;
    -- Tandai terpakai secara atomik; pesaing yang bersamaan gagal di sini.
    UPDATE staff_invites SET used_at = now()
    WHERE id = v_invite_id AND used_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'invite invalid, expired, or already used';
    END IF;
    INSERT INTO workspace_memberships (workspace_id, subject_id, status)
    VALUES (p_workspace_id, p_subject_id, 'active')
    ON CONFLICT (workspace_id, subject_id)
    DO UPDATE SET status = 'active';
    INSERT INTO membership_roles (workspace_id, subject_id, role)
    VALUES (p_workspace_id, p_subject_id, v_role)
    ON CONFLICT (workspace_id, subject_id, role) DO NOTHING;
    INSERT INTO audit_events (workspace_id, actor_subject_id, action, entity_type, entity_id)
    VALUES (p_workspace_id, p_subject_id, 'invite_accepted', 'staff_invite', v_invite_id);
    RETURN v_role;
END;
$$;

-- RLS: semua akses data tenant harus lewat workspace aktif + membership.
-- FORCE penting: tabel dimiliki umnia_business (role aplikasi), dan
-- pemilik tabel melewati RLS kecuali FORCE. Tanpa FORCE, RLS di sini
-- tidak berlaku sama sekali untuk koneksi API.
ALTER TABLE identity_subjects ENABLE ROW LEVEL SECURITY;
ALTER TABLE identity_subjects FORCE ROW LEVEL SECURITY;
ALTER TABLE workspaces ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspaces FORCE ROW LEVEL SECURITY;
ALTER TABLE workspace_memberships ENABLE ROW LEVEL SECURITY;
ALTER TABLE workspace_memberships FORCE ROW LEVEL SECURITY;
ALTER TABLE membership_roles ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_roles FORCE ROW LEVEL SECURITY;
ALTER TABLE sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE staff_invites ENABLE ROW LEVEL SECURITY;
ALTER TABLE staff_invites FORCE ROW LEVEL SECURITY;
ALTER TABLE audit_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE audit_events FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS self_only ON identity_subjects;
CREATE POLICY self_only ON identity_subjects
    USING (id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid);

-- Anggota workspace aktif boleh melihat subjek lain di workspace yang sama
-- (untuk daftar anggota); subjek di luar workspace tetap tidak terlihat.
DROP POLICY IF EXISTS member_subject ON identity_subjects;
CREATE POLICY member_subject ON identity_subjects
    USING (
        EXISTS (
            SELECT 1 FROM workspace_memberships m
            WHERE m.subject_id = identity_subjects.id
              AND m.workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
              AND app.is_member(m.workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
        )
    );

DROP POLICY IF EXISTS member_workspace ON workspaces;
CREATE POLICY member_workspace ON workspaces
    USING (
        id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND (
            owner_subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
            OR app.is_member(id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
        )
    )
    WITH CHECK (
        id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND owner_subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
    );

DROP POLICY IF EXISTS member_membership ON workspace_memberships;
CREATE POLICY member_membership ON workspace_memberships
    USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    )
    WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND (
            EXISTS (
                SELECT 1 FROM workspaces w
                WHERE w.id = workspace_id
                  AND w.owner_subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
            )
            OR app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
        )
    );

DROP POLICY IF EXISTS member_roles ON membership_roles;
CREATE POLICY member_roles ON membership_roles
    USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    )
    WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND (
            EXISTS (
                SELECT 1 FROM workspaces w
                WHERE w.id = workspace_id
                  AND w.owner_subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
            )
            OR app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
        )
    );

DROP POLICY IF EXISTS self_session ON sessions;
CREATE POLICY self_session ON sessions
    USING (subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    WITH CHECK (subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid);

DROP POLICY IF EXISTS member_invite ON staff_invites;
CREATE POLICY member_invite ON staff_invites
    USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    )
    WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.has_permission(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid, 'invite_members')
    );

DROP POLICY IF EXISTS member_audit ON audit_events;
CREATE POLICY member_audit ON audit_events
    USING (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
    )
    WITH CHECK (
        workspace_id = NULLIF(current_setting('app.current_workspace_id', true), '')::uuid
        AND (
            actor_subject_id = NULLIF(current_setting('app.current_subject_id', true), '')::uuid
            AND app.is_member(workspace_id, NULLIF(current_setting('app.current_subject_id', true), '')::uuid)
        )
    );

INSERT INTO schema_migrations (version) VALUES ('002_identity')
ON CONFLICT (version) DO NOTHING;

-- Serahkan kepemilikan ke role aplikasi (lihat 001_init untuk pola yang sama).
ALTER TABLE identity_subjects OWNER TO umnia_business;
ALTER TABLE workspaces OWNER TO umnia_business;
ALTER TABLE workspace_memberships OWNER TO umnia_business;
ALTER TABLE roles OWNER TO umnia_business;
ALTER TABLE role_permissions OWNER TO umnia_business;
ALTER TABLE membership_roles OWNER TO umnia_business;
ALTER TABLE sessions OWNER TO umnia_business;
ALTER TABLE staff_invites OWNER TO umnia_business;
ALTER TABLE audit_events OWNER TO umnia_business;
