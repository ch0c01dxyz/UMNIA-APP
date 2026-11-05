-- Pengerasan SECURITY DEFINER: kunci search_path ke public supaya pemanggil
-- tidak bisa mengarahkan fungsi ke tabel bayangan lewat search_path-nya
-- sendiri, dan batasi EXECUTE hanya ke role aplikasi (default PostgreSQL
-- adalah EXECUTE TO PUBLIC, yang akan membiarkan role mana pun memanggil
-- subject_by_email dan membaca hash password).
-- Sengaja SETELAH alih kepemilikan di atas, supaya GRANT ini (pemberinya
-- postgres, pemilik baru) tidak terbuang oleh ALTER ... OWNER TO.
ALTER FUNCTION app.is_member(UUID, UUID) SET search_path = public;
ALTER FUNCTION app.has_permission(UUID, UUID, TEXT) SET search_path = public;
ALTER FUNCTION app.subject_by_email(TEXT) SET search_path = public;
ALTER FUNCTION app.register_subject(TEXT, TEXT) SET search_path = public;
ALTER FUNCTION app.session_subject(TEXT) SET search_path = public;
ALTER FUNCTION app.accept_invite(UUID, UUID, TEXT) SET search_path = public;
REVOKE EXECUTE ON FUNCTION app.is_member(UUID, UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION app.has_permission(UUID, UUID, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION app.subject_by_email(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION app.register_subject(TEXT, TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION app.session_subject(TEXT) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION app.accept_invite(UUID, UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.is_member(UUID, UUID) TO umnia_business;
GRANT EXECUTE ON FUNCTION app.has_permission(UUID, UUID, TEXT) TO umnia_business;
GRANT EXECUTE ON FUNCTION app.subject_by_email(TEXT) TO umnia_business;
GRANT EXECUTE ON FUNCTION app.register_subject(TEXT, TEXT) TO umnia_business;
GRANT EXECUTE ON FUNCTION app.session_subject(TEXT) TO umnia_business;
GRANT EXECUTE ON FUNCTION app.accept_invite(UUID, UUID, TEXT) TO umnia_business;
