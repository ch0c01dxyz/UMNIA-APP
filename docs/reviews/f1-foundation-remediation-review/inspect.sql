-- inspect.sql — immediate post-migration inspection (run as superuser).
-- Distinguishes explicit ACLs from effective privileges; do not read
-- routine_privileges alone as "effective EXECUTE".
\echo '--- role attributes ---'
SELECT rolname, rolsuper, rolbypassrls, rolcanlogin FROM pg_roles
WHERE rolname IN ('umnia_business','postgres','umnia_bootstrap','review_other') ORDER BY rolname;
\echo '--- schema_migrations ---'
SELECT version FROM schema_migrations ORDER BY version;
\echo '--- function owners + search_path (proconfig) ---'
SELECT n.nspname AS schema, p.proname, pg_get_userbyid(p.proowner) AS owner,
  p.proconfig AS search_path_setting, p.prosecdef AS security_definer
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'app' AND p.proname IN
 ('is_member','has_permission','subject_by_email','register_subject','session_subject','accept_invite')
ORDER BY p.proname;
\echo '--- explicit ACLs (proacl) on the 6 helpers ---'
SELECT p.proname, p.proacl FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'app' AND p.proname IN
 ('is_member','has_permission','subject_by_email','register_subject','session_subject','accept_invite')
ORDER BY p.proname;
\echo '--- routine_privileges grantees (explicit grants only) ---'
SELECT routine_name, grantee, privilege_type FROM information_schema.routine_privileges
WHERE routine_schema = 'app'
  AND routine_name IN ('is_member','has_permission','subject_by_email','register_subject','session_subject','accept_invite')
ORDER BY routine_name, grantee;
\echo '--- effective EXECUTE for runtime role / other role / PUBLIC ---'
SELECT 'umnia_business register_subject' AS chk,
  has_function_privilege('umnia_business', 'app.register_subject(TEXT,TEXT)', 'EXECUTE') AS has_exec;
SELECT 'review_other register_subject' AS chk,
  has_function_privilege('review_other', 'app.register_subject(TEXT,TEXT)', 'EXECUTE') AS has_exec;
-- has_*_privilege does not accept 'PUBLIC' as a role name; PUBLIC access is
-- instead proven by the proacl above (no PUBLIC entry on any helper) and by
-- review_other (an ordinary non-runtime role) returning false.
SELECT 'helpers with any PUBLIC ACL entry' AS chk,
  count(*) AS n FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'app' AND p.proacl::text LIKE '%PUBLIC%';
SELECT 'umnia_business accept_invite' AS chk,
  has_function_privilege('umnia_business', 'app.accept_invite(UUID,UUID,TEXT)', 'EXECUTE') AS has_exec;
\echo '--- CREATE privilege on referenced schemas ---'
SELECT nspname AS schema, pg_get_userbyid(nspowner) AS owner,
  nspacl AS acl FROM pg_namespace WHERE nspname IN ('public','app');
SELECT 'umnia_business CREATE on public' AS chk, has_schema_privilege('umnia_business','public','CREATE') AS has_create;
SELECT 'umnia_business CREATE on app' AS chk, has_schema_privilege('umnia_business','app','CREATE') AS has_create;
-- PUBLIC CREATE is read from the schema ACLs above (nspacl): no PUBLIC
-- entry means no PUBLIC CREATE. has_schema_privilege('PUBLIC',...) errors
-- because PUBLIC is not a role name.
SELECT 'public schema ACL mentions PUBLIC' AS chk,
  count(*) AS n FROM pg_namespace WHERE nspname='public' AND nspacl::text LIKE '%PUBLIC%';
SELECT 'app schema ACL mentions PUBLIC' AS chk,
  count(*) AS n FROM pg_namespace WHERE nspname='app' AND nspacl::text LIKE '%PUBLIC%';
SELECT 'postgres CREATE on public' AS chk, has_schema_privilege('postgres','public','CREATE') AS has_create;
\echo '--- table owners (spot check) ---'
SELECT tablename, tableowner FROM pg_tables WHERE schemaname='public'
  AND tablename IN ('workspaces','identity_subjects','audit_events') ORDER BY tablename;
\echo '--- RLS forced? ---'
SELECT relname, relrowsecurity, relforcerowsecurity FROM pg_class
WHERE relname IN ('workspaces','identity_subjects','workspace_memberships') ORDER BY relname;
