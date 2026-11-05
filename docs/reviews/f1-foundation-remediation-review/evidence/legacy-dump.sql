--
-- PostgreSQL database dump
--

\restrict np0qE9SwnhkVNSoCLQXurDo7ErM7aXCRKZUiaMv5VghHIGKietU5iyEs2oz3cJa

-- Dumped from database version 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1)
-- Dumped by pg_dump version 16.15 (Ubuntu 16.15-0ubuntu0.24.04.1)

SET statement_timeout = 0;
SET lock_timeout = 0;
SET idle_in_transaction_session_timeout = 0;
SET client_encoding = 'UTF8';
SET standard_conforming_strings = on;
SELECT pg_catalog.set_config('search_path', '', false);
SET check_function_bodies = false;
SET xmloption = content;
SET client_min_messages = warning;
SET row_security = off;

--
-- Name: app; Type: SCHEMA; Schema: -; Owner: -
--

CREATE SCHEMA app;


--
-- Name: pgcrypto; Type: EXTENSION; Schema: -; Owner: -
--

CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA public;


--
-- Name: EXTENSION pgcrypto; Type: COMMENT; Schema: -; Owner: -
--

COMMENT ON EXTENSION pgcrypto IS 'cryptographic functions';


--
-- Name: accept_invite(uuid, uuid, text); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.accept_invite(p_workspace_id uuid, p_subject_id uuid, p_token_hash text) RETURNS text
    LANGUAGE plpgsql SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
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


--
-- Name: has_permission(uuid, uuid, text); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.has_permission(ws uuid, subj uuid, perm text) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
    SELECT app.is_member(ws, subj) AND EXISTS (
        SELECT 1 FROM membership_roles mr
        JOIN role_permissions rp ON rp.role = mr.role
        WHERE mr.workspace_id = ws AND mr.subject_id = subj
          AND rp.permission = perm
    );
$$;


--
-- Name: is_member(uuid, uuid); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.is_member(ws uuid, subj uuid) RETURNS boolean
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
    SELECT EXISTS (
        SELECT 1 FROM workspace_memberships
        WHERE workspace_id = ws AND subject_id = subj AND status = 'active'
    );
$$;


--
-- Name: register_subject(text, text); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.register_subject(p_email text, p_password_hash text) RETURNS uuid
    LANGUAGE sql SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
    INSERT INTO identity_subjects (email, password_hash)
    VALUES (p_email, p_password_hash)
    RETURNING id;
$$;


--
-- Name: reject_audit_change(); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.reject_audit_change() RETURNS trigger
    LANGUAGE plpgsql
    AS $$
BEGIN
    RAISE EXCEPTION 'audit_events is append-only';
END;
$$;


--
-- Name: session_subject(text); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.session_subject(p_token_hash text) RETURNS uuid
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
    SELECT subject_id FROM sessions
    WHERE token_hash = p_token_hash AND expires_at > now();
$$;


--
-- Name: subject_by_email(text); Type: FUNCTION; Schema: app; Owner: -
--

CREATE FUNCTION app.subject_by_email(p_email text) RETURNS TABLE(id uuid, password_hash text)
    LANGUAGE sql STABLE SECURITY DEFINER
    SET search_path TO 'public'
    AS $$
    SELECT id, password_hash FROM identity_subjects
    WHERE lower(email) = lower(p_email);
$$;


SET default_tablespace = '';

SET default_table_access_method = heap;

--
-- Name: audit_events; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.audit_events (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    actor_subject_id uuid NOT NULL,
    action text NOT NULL,
    entity_type text DEFAULT ''::text NOT NULL,
    entity_id uuid,
    details jsonb DEFAULT '{}'::jsonb NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.audit_events FORCE ROW LEVEL SECURITY;


--
-- Name: identity_subjects; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.identity_subjects (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    email text NOT NULL,
    password_hash text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.identity_subjects FORCE ROW LEVEL SECURITY;


--
-- Name: membership_roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.membership_roles (
    workspace_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    role text NOT NULL
);

ALTER TABLE ONLY public.membership_roles FORCE ROW LEVEL SECURITY;


--
-- Name: role_permissions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.role_permissions (
    role text NOT NULL,
    permission text NOT NULL
);


--
-- Name: roles; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.roles (
    name text NOT NULL,
    description text DEFAULT ''::text NOT NULL
);


--
-- Name: schema_migrations; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.schema_migrations (
    version text NOT NULL,
    applied_at timestamp with time zone DEFAULT now() NOT NULL
);


--
-- Name: sessions; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.sessions (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    subject_id uuid NOT NULL,
    token_hash text NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.sessions FORCE ROW LEVEL SECURITY;


--
-- Name: staff_invites; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.staff_invites (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    workspace_id uuid NOT NULL,
    email text NOT NULL,
    role text NOT NULL,
    token_hash text NOT NULL,
    invited_by uuid NOT NULL,
    expires_at timestamp with time zone NOT NULL,
    used_at timestamp with time zone,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.staff_invites FORCE ROW LEVEL SECURITY;


--
-- Name: workspace_memberships; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspace_memberships (
    workspace_id uuid NOT NULL,
    subject_id uuid NOT NULL,
    status text NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL,
    CONSTRAINT workspace_memberships_status_check CHECK ((status = ANY (ARRAY['pending'::text, 'active'::text, 'suspended'::text])))
);

ALTER TABLE ONLY public.workspace_memberships FORCE ROW LEVEL SECURITY;


--
-- Name: workspaces; Type: TABLE; Schema: public; Owner: -
--

CREATE TABLE public.workspaces (
    id uuid DEFAULT gen_random_uuid() NOT NULL,
    name text NOT NULL,
    owner_subject_id uuid NOT NULL,
    created_at timestamp with time zone DEFAULT now() NOT NULL
);

ALTER TABLE ONLY public.workspaces FORCE ROW LEVEL SECURITY;


--
-- Data for Name: audit_events; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.audit_events (id, workspace_id, actor_subject_id, action, entity_type, entity_id, details, created_at) FROM stdin;
9455eeda-4062-4bc4-b52f-858bbd9d4744	ec4e636d-a7d9-4f55-975f-589f4c09f03f	772f56e5-56b9-443e-88ed-0c28c9529e97	workspace_created	workspace	ec4e636d-a7d9-4f55-975f-589f4c09f03f	{}	2026-11-05 19:15:01.329401+07
\.


--
-- Data for Name: identity_subjects; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.identity_subjects (id, email, password_hash, created_at) FROM stdin;
11111111-1111-1111-1111-111111111111	toko-lama@contoh.id	x	2026-11-05 19:14:58.644413+07
772f56e5-56b9-443e-88ed-0c28c9529e97	owner-1793880900805829464@contoh.id	$2a$10$0N6PBsN2CRcomlxXER8KEe/fv0lMtXLG0YQLhrjdqC7hVLXB5fQiO	2026-11-05 19:15:00.932972+07
73674ce7-1809-44e3-8c90-80918820ef8c	staff-1793880900805829464@contoh.id	$2a$10$.HIbyaka7sYWTP1jlAqBzekXLdfXKgxX7tK8ogS4l0JgcAzFsd1Ku	2026-11-05 19:15:01.187951+07
\.


--
-- Data for Name: membership_roles; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.membership_roles (workspace_id, subject_id, role) FROM stdin;
ec4e636d-a7d9-4f55-975f-589f4c09f03f	772f56e5-56b9-443e-88ed-0c28c9529e97	owner
\.


--
-- Data for Name: role_permissions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.role_permissions (role, permission) FROM stdin;
owner	invite_members
owner	view_workspace
owner	view_members
owner	view_audit
manager	invite_members
manager	view_workspace
manager	view_members
manager	view_audit
cashier	view_workspace
\.


--
-- Data for Name: roles; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.roles (name, description) FROM stdin;
owner	Pemilik workspace
manager	Manajer workspace
cashier	Kasir
\.


--
-- Data for Name: schema_migrations; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.schema_migrations (version, applied_at) FROM stdin;
001_init	2026-11-05 19:14:58.578283+07
002_identity	2026-11-05 19:14:58.614074+07
\.


--
-- Data for Name: sessions; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.sessions (id, subject_id, token_hash, expires_at, created_at) FROM stdin;
1094be66-ce12-42e0-a590-bf8f6c813da3	772f56e5-56b9-443e-88ed-0c28c9529e97	6867f8745c7e4cc177b46f602ee4ffa51bda4e7b50b008e7fdcf29abd702e16c	2026-11-06 19:15:01.056567+07	2026-11-05 19:15:01.055786+07
55dfb703-e290-4ecd-a707-ca1c58b474e4	73674ce7-1809-44e3-8c90-80918820ef8c	5cb4ddc081a27a5fde3ffcc25aaea0a2783751fb4aaa07b6cf81fcb1a4baf22d	2026-11-06 19:15:01.310963+07	2026-11-05 19:15:01.310621+07
\.


--
-- Data for Name: staff_invites; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.staff_invites (id, workspace_id, email, role, token_hash, invited_by, expires_at, used_at, created_at) FROM stdin;
\.


--
-- Data for Name: workspace_memberships; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.workspace_memberships (workspace_id, subject_id, status, created_at) FROM stdin;
22222222-2222-2222-2222-222222222222	11111111-1111-1111-1111-111111111111	active	2026-11-05 19:14:58.646809+07
ec4e636d-a7d9-4f55-975f-589f4c09f03f	772f56e5-56b9-443e-88ed-0c28c9529e97	active	2026-11-05 19:15:01.329401+07
\.


--
-- Data for Name: workspaces; Type: TABLE DATA; Schema: public; Owner: -
--

COPY public.workspaces (id, name, owner_subject_id, created_at) FROM stdin;
22222222-2222-2222-2222-222222222222	Toko Lama	11111111-1111-1111-1111-111111111111	2026-11-05 19:14:58.645804+07
ec4e636d-a7d9-4f55-975f-589f4c09f03f	Toko Probe	772f56e5-56b9-443e-88ed-0c28c9529e97	2026-11-05 19:15:01.329401+07
\.


--
-- Name: audit_events audit_events_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_pkey PRIMARY KEY (id);


--
-- Name: identity_subjects identity_subjects_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.identity_subjects
    ADD CONSTRAINT identity_subjects_pkey PRIMARY KEY (id);


--
-- Name: membership_roles membership_roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_pkey PRIMARY KEY (workspace_id, subject_id, role);


--
-- Name: role_permissions role_permissions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_pkey PRIMARY KEY (role, permission);


--
-- Name: roles roles_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.roles
    ADD CONSTRAINT roles_pkey PRIMARY KEY (name);


--
-- Name: schema_migrations schema_migrations_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.schema_migrations
    ADD CONSTRAINT schema_migrations_pkey PRIMARY KEY (version);


--
-- Name: sessions sessions_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_pkey PRIMARY KEY (id);


--
-- Name: sessions sessions_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_token_hash_key UNIQUE (token_hash);


--
-- Name: staff_invites staff_invites_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.staff_invites
    ADD CONSTRAINT staff_invites_pkey PRIMARY KEY (id);


--
-- Name: staff_invites staff_invites_token_hash_key; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.staff_invites
    ADD CONSTRAINT staff_invites_token_hash_key UNIQUE (token_hash);


--
-- Name: workspace_memberships workspace_memberships_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_memberships
    ADD CONSTRAINT workspace_memberships_pkey PRIMARY KEY (workspace_id, subject_id);


--
-- Name: workspaces workspaces_pkey; Type: CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT workspaces_pkey PRIMARY KEY (id);


--
-- Name: identity_subjects_email_lower_idx; Type: INDEX; Schema: public; Owner: -
--

CREATE UNIQUE INDEX identity_subjects_email_lower_idx ON public.identity_subjects USING btree (lower(email));


--
-- Name: audit_events audit_events_append_only; Type: TRIGGER; Schema: public; Owner: -
--

CREATE TRIGGER audit_events_append_only BEFORE DELETE OR UPDATE ON public.audit_events FOR EACH ROW EXECUTE FUNCTION app.reject_audit_change();


--
-- Name: audit_events audit_events_actor_subject_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_actor_subject_id_fkey FOREIGN KEY (actor_subject_id) REFERENCES public.identity_subjects(id);


--
-- Name: audit_events audit_events_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.audit_events
    ADD CONSTRAINT audit_events_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);


--
-- Name: membership_roles membership_roles_role_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_role_fkey FOREIGN KEY (role) REFERENCES public.roles(name);


--
-- Name: membership_roles membership_roles_workspace_id_subject_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.membership_roles
    ADD CONSTRAINT membership_roles_workspace_id_subject_id_fkey FOREIGN KEY (workspace_id, subject_id) REFERENCES public.workspace_memberships(workspace_id, subject_id);


--
-- Name: role_permissions role_permissions_role_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.role_permissions
    ADD CONSTRAINT role_permissions_role_fkey FOREIGN KEY (role) REFERENCES public.roles(name);


--
-- Name: sessions sessions_subject_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.sessions
    ADD CONSTRAINT sessions_subject_id_fkey FOREIGN KEY (subject_id) REFERENCES public.identity_subjects(id);


--
-- Name: staff_invites staff_invites_invited_by_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.staff_invites
    ADD CONSTRAINT staff_invites_invited_by_fkey FOREIGN KEY (invited_by) REFERENCES public.identity_subjects(id);


--
-- Name: staff_invites staff_invites_role_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.staff_invites
    ADD CONSTRAINT staff_invites_role_fkey FOREIGN KEY (role) REFERENCES public.roles(name);


--
-- Name: staff_invites staff_invites_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.staff_invites
    ADD CONSTRAINT staff_invites_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);


--
-- Name: workspace_memberships workspace_memberships_subject_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_memberships
    ADD CONSTRAINT workspace_memberships_subject_id_fkey FOREIGN KEY (subject_id) REFERENCES public.identity_subjects(id);


--
-- Name: workspace_memberships workspace_memberships_workspace_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspace_memberships
    ADD CONSTRAINT workspace_memberships_workspace_id_fkey FOREIGN KEY (workspace_id) REFERENCES public.workspaces(id);


--
-- Name: workspaces workspaces_owner_subject_id_fkey; Type: FK CONSTRAINT; Schema: public; Owner: -
--

ALTER TABLE ONLY public.workspaces
    ADD CONSTRAINT workspaces_owner_subject_id_fkey FOREIGN KEY (owner_subject_id) REFERENCES public.identity_subjects(id);


--
-- Name: audit_events; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.audit_events ENABLE ROW LEVEL SECURITY;

--
-- Name: identity_subjects; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.identity_subjects ENABLE ROW LEVEL SECURITY;

--
-- Name: audit_events member_audit; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_audit ON public.audit_events USING (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))) WITH CHECK (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND ((actor_subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid) AND app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))));


--
-- Name: staff_invites member_invite; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_invite ON public.staff_invites USING (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))) WITH CHECK (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.has_permission(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid, 'invite_members'::text)));


--
-- Name: workspace_memberships member_membership; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_membership ON public.workspace_memberships USING (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))) WITH CHECK (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND ((EXISTS ( SELECT 1
   FROM public.workspaces w
  WHERE ((w.id = workspace_memberships.workspace_id) AND (w.owner_subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)))) OR app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))));


--
-- Name: membership_roles member_roles; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_roles ON public.membership_roles USING (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))) WITH CHECK (((workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND ((EXISTS ( SELECT 1
   FROM public.workspaces w
  WHERE ((w.id = membership_roles.workspace_id) AND (w.owner_subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)))) OR app.is_member(workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid))));


--
-- Name: identity_subjects member_subject; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_subject ON public.identity_subjects USING ((EXISTS ( SELECT 1
   FROM public.workspace_memberships m
  WHERE ((m.subject_id = identity_subjects.id) AND (m.workspace_id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND app.is_member(m.workspace_id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)))));


--
-- Name: workspaces member_workspace; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY member_workspace ON public.workspaces USING (((id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND ((owner_subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid) OR app.is_member(id, (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)))) WITH CHECK (((id = (NULLIF(current_setting('app.current_workspace_id'::text, true), ''::text))::uuid) AND (owner_subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)));


--
-- Name: membership_roles; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.membership_roles ENABLE ROW LEVEL SECURITY;

--
-- Name: identity_subjects self_only; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY self_only ON public.identity_subjects USING ((id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid));


--
-- Name: sessions self_session; Type: POLICY; Schema: public; Owner: -
--

CREATE POLICY self_session ON public.sessions USING ((subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid)) WITH CHECK ((subject_id = (NULLIF(current_setting('app.current_subject_id'::text, true), ''::text))::uuid));


--
-- Name: sessions; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.sessions ENABLE ROW LEVEL SECURITY;

--
-- Name: staff_invites; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.staff_invites ENABLE ROW LEVEL SECURITY;

--
-- Name: workspace_memberships; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.workspace_memberships ENABLE ROW LEVEL SECURITY;

--
-- Name: workspaces; Type: ROW SECURITY; Schema: public; Owner: -
--

ALTER TABLE public.workspaces ENABLE ROW LEVEL SECURITY;

--
-- Name: FUNCTION accept_invite(p_workspace_id uuid, p_subject_id uuid, p_token_hash text); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.accept_invite(p_workspace_id uuid, p_subject_id uuid, p_token_hash text) FROM PUBLIC;
GRANT ALL ON FUNCTION app.accept_invite(p_workspace_id uuid, p_subject_id uuid, p_token_hash text) TO umnia_business;


--
-- Name: FUNCTION has_permission(ws uuid, subj uuid, perm text); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.has_permission(ws uuid, subj uuid, perm text) FROM PUBLIC;
GRANT ALL ON FUNCTION app.has_permission(ws uuid, subj uuid, perm text) TO umnia_business;


--
-- Name: FUNCTION is_member(ws uuid, subj uuid); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.is_member(ws uuid, subj uuid) FROM PUBLIC;
GRANT ALL ON FUNCTION app.is_member(ws uuid, subj uuid) TO umnia_business;


--
-- Name: FUNCTION register_subject(p_email text, p_password_hash text); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.register_subject(p_email text, p_password_hash text) FROM PUBLIC;
GRANT ALL ON FUNCTION app.register_subject(p_email text, p_password_hash text) TO umnia_business;


--
-- Name: FUNCTION session_subject(p_token_hash text); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.session_subject(p_token_hash text) FROM PUBLIC;
GRANT ALL ON FUNCTION app.session_subject(p_token_hash text) TO umnia_business;


--
-- Name: FUNCTION subject_by_email(p_email text); Type: ACL; Schema: app; Owner: -
--

REVOKE ALL ON FUNCTION app.subject_by_email(p_email text) FROM PUBLIC;
GRANT ALL ON FUNCTION app.subject_by_email(p_email text) TO umnia_business;


--
-- PostgreSQL database dump complete
--

\unrestrict np0qE9SwnhkVNSoCLQXurDo7ErM7aXCRKZUiaMv5VghHIGKietU5iyEs2oz3cJa

