-- F1-02: migrasi awal business DB. Hanya tabel pelacak migrasi, BUKAN
-- skema domain. Skema domain (workspace, customer, dst.) masuk di tiketnya
-- masing-masing (F1-03 dst.); sql/001_core_reference.sql di repo ini
-- eksplisit berstatus referensi v0.4 yang belum siap produksi, jadi tidak
-- dipakai di sini.
CREATE TABLE IF NOT EXISTS schema_migrations (
  version    TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO schema_migrations (version) VALUES ('001_init')
ON CONFLICT (version) DO NOTHING;

-- Migrasi dijalankan sebagai postgres (F1-02); serahkan kepemilikan ke
-- role aplikasi bisnis supaya aplikasi bisa membaca/mencatat migrasi.
ALTER TABLE schema_migrations OWNER TO umnia_business;
