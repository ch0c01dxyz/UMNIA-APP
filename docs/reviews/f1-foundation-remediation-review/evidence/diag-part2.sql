-- Fungsi helper tetap milik postgres (superuser) supaya SECURITY DEFINER
-- benar-benar melewati RLS yang di-FORCE; kalau dimiliki umnia_business,
-- fungsi itu ikut tersaring RLS dan is_member akan rekursif/terblokir.
-- URUTAN INI PENTING: alih kepemilikan HARUS sebelum REVOKE/GRANT di bawah.
-- ALTER ... OWNER TO membuang entri ACL yang diberikan pemilik lama (terbukti:
-- migrasi ulang pada volume lama yang fungsinya masih milik umnia_business
-- membuat GRANT ke umnia_business hilang, proacl hanya {postgres=X}).
-- Bila GRANT jalan dulu, grant itu ikut terbuang saat kepemilikan dialihkan.
ALTER FUNCTION app.reject_audit_change() OWNER TO postgres;
ALTER FUNCTION app.is_member(UUID, UUID) OWNER TO postgres;
ALTER FUNCTION app.has_permission(UUID, UUID, TEXT) OWNER TO postgres;
ALTER FUNCTION app.subject_by_email(TEXT) OWNER TO postgres;
ALTER FUNCTION app.register_subject(TEXT, TEXT) OWNER TO postgres;
ALTER FUNCTION app.session_subject(TEXT) OWNER TO postgres;
ALTER FUNCTION app.accept_invite(UUID, UUID, TEXT) OWNER TO postgres;

