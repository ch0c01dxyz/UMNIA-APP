package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Tes integrasi F1-04B-B1 terhadap PostgreSQL asli: penyimpanan
// instalasi modul + entitlement (migrasi 004), RLS, hak runtime, seed
// izin manage_modules, dan rerun 004.
// Memakai helper yang sama dengan tes lain (testDB, bootstrapDB,
// runPsqlFile, registerAndLogin, createWorkspace, doJSON).

// pgCode mengembalikan kode SQLSTATE dari galat pgx, atau "" bila bukan
// galat PostgreSQL.
func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

// subjectID mengembalikan id subjek lewat fungsi SECURITY DEFINER
// (bypass RLS, seperti login) pada koneksi runtime.
func subjectID(t *testing.T, db *sql.DB, email string) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`SELECT id FROM app.subject_by_email($1)`, email).Scan(&id); err != nil {
		t.Fatalf("subject_by_email %s: %v", email, err)
	}
	return id
}

// tenantTx membuka transaksi runtime dengan konteks tenant (workspace +
// subjek) seperti beginTenant, tanpa commit — pemanggil menutup dengan
// Rollback supaya fixture tidak tersisa.
func tenantTx(t *testing.T, db *sql.DB, ws, subject string) *sql.Tx {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin tenant tx: %v", err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_workspace_id', $1, true)`, ws); err != nil {
		tx.Rollback()
		t.Fatalf("set workspace: %v", err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_subject_id', $1, true)`, subject); err != nil {
		tx.Rollback()
		t.Fatalf("set subject: %v", err)
	}
	return tx
}

// TestModuleStorageFreshSchema: migrasi 004 pada database segar —
// enam marker (001-006), dua tabel kosong, pemilik bootstrap (bukan
// runtime), RLS ENABLE+FORCE, hak efektif runtime, kebijakan, dan seed
// izin.
func TestModuleStorageFreshSchema(t *testing.T) {
	boot := bootstrapDB(t)

	var markers string
	if err := boot.QueryRow(
		`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`,
	).Scan(&markers); err != nil {
		t.Fatalf("baca markers: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments, got %s", markers)
	}
	for _, table := range []string{"module_installations", "module_entitlements"} {
		var n int
		if err := boot.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("%s: want 0 baris pada skema segar, got %d", table, n)
		}
		var owner string
		var super, rls, force bool
		if err := boot.QueryRow(
			`SELECT c.relowner::regrole::text, r.rolsuper, c.relrowsecurity, c.relforcerowsecurity
			 FROM pg_class c JOIN pg_roles r ON r.rolname = c.relowner::regrole::text
			 WHERE c.relname = $1`, table,
		).Scan(&owner, &super, &rls, &force); err != nil {
			t.Fatalf("owner/rls %s: %v", table, err)
		}
		if owner == "umnia_business" {
			t.Fatalf("%s dimiliki runtime role; harus dimiliki bootstrap/migrasi", table)
		}
		if !super {
			t.Fatalf("pemilik %s (%q) harus superuser (bootstrap/migrasi), got super=%v", table, owner, super)
		}
		if !rls || !force {
			t.Fatalf("%s: RLS harus ENABLE+FORCE, got rls=%v force=%v", table, rls, force)
		}
	}
	// Hak efektif runtime (diperiksa untuk role itu, bukan lewat
	// koneksi istimewa yang menyamar): instalasi SELECT/INSERT/UPDATE
	// boleh, DELETE tidak; entitlement hanya SELECT.
	type priv struct {
		table, action string
		want          bool
	}
	for _, p := range []priv{
		{"module_installations", "SELECT", true},
		{"module_installations", "INSERT", true},
		{"module_installations", "UPDATE", true},
		{"module_installations", "DELETE", false},
		{"module_entitlements", "SELECT", true},
		{"module_entitlements", "INSERT", false},
		{"module_entitlements", "UPDATE", false},
		{"module_entitlements", "DELETE", false},
	} {
		var has bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', $1, $2)`, p.table, p.action).Scan(&has); err != nil {
			t.Fatalf("has_table_privilege %s %s: %v", p.table, p.action, err)
		}
		if has != p.want {
			t.Fatalf("hak runtime %s %s: want %v, got %v", p.table, p.action, p.want, has)
		}
	}
	// Kebijakan: instalasi punya select+insert+update (terpisah);
	// entitlement: select anggota + select/insert khusus eksekutor
	// operator (011) — tidak ada kebijakan DML untuk runtime bisnis.
	rows, err := boot.Query(`SELECT tablename, policyname, cmd FROM pg_policies WHERE tablename IN ('module_installations', 'module_entitlements') ORDER BY tablename, policyname`)
	if err != nil {
		t.Fatalf("baca pg_policies: %v", err)
	}
	defer rows.Close()
	got := map[string][]string{}
	for rows.Next() {
		var table, name, cmd string
		if err := rows.Scan(&table, &name, &cmd); err != nil {
			t.Fatalf("scan pg_policies: %v", err)
		}
		got[table] = append(got[table], name+":"+cmd)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows pg_policies: %v", err)
	}
	want := map[string][]string{
		"module_entitlements":  {"entitlements_exec_operator_insert:INSERT", "entitlements_exec_operator_select:SELECT", "entitlements_select:SELECT"},
		"module_installations": {"installations_insert:INSERT", "installations_select:SELECT", "installations_update:UPDATE"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("kebijakan: want %v, got %v", want, got)
	}
	// Seed izin manage_modules: owner saja.
	for _, tc := range []struct {
		role string
		want bool
	}{
		{"owner", true}, {"manager", false}, {"cashier", false},
	} {
		var n int
		if err := boot.QueryRow(`SELECT count(*) FROM role_permissions WHERE role = $1 AND permission = 'manage_modules'`, tc.role).Scan(&n); err != nil {
			t.Fatalf("seed manage_modules %s: %v", tc.role, err)
		}
		if (n == 1) != tc.want {
			t.Fatalf("seed manage_modules %s: want %v, got %d baris", tc.role, tc.want, n)
		}
	}
}

// storageSnapshot mengambil seluruh isi kedua tabel (semua kolom sebagai
// teks), seed izin, definisi kebijakan, ACL tabel, dan marker — dipakai
// untuk membuktikan rerun 004 tidak mengubah apa pun.
func storageSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"installations": `SELECT coalesce(string_agg(workspace_id::text || '|' || module_id || '|' || version || '|' || status || '|' || coalesce(activated_at::text,'') || '|' || coalesce(deactivated_at::text,'') || '|' || coalesce(activated_by::text,'') || '|' || updated_at::text, chr(10) ORDER BY workspace_id, module_id), '') FROM module_installations`,
		"entitlements":  `SELECT coalesce(string_agg(workspace_id::text || '|' || module_id || '|' || version || '|' || granted_at::text || '|' || granted_by || '|' || source || '|' || coalesce(reason,''), chr(10) ORDER BY workspace_id, module_id), '') FROM module_entitlements`,
		"permissions":   `SELECT coalesce(string_agg(role || ':' || permission, ',' ORDER BY role, permission), '') FROM role_permissions`,
		"policies":      `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('module_installations', 'module_entitlements')`,
		"acl":           `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('module_installations', 'module_entitlements')`,
		"markers":       `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
	}
	for name, q := range queries {
		var s string
		if err := boot.QueryRow(q).Scan(&s); err != nil {
			t.Fatalf("snapshot %s: %v", name, err)
		}
		out[name] = s
	}
	return out
}

// TestModuleStorageRerunPreservesData: rerun 004 lewat psql ASLI tanpa
// -1 (jalur autocommit Linux native) mempertahankan baris, timestamp,
// seed, kebijakan, ACL, dan marker — dan kebijakan tetap efektif
// sesudahnya (has_permission owner tetap true).
func TestModuleStorageRerunPreservesData(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("rerun-owner-%d@contoh.id", suffix)
	tokenOwner := registerAndLogin(t, srv, ownerEmail)
	ws := createWorkspace(t, srv, tokenOwner, "Usaha Rerun B1")
	ownerID := subjectID(t, db, ownerEmail)

	// Fixture lewat jalur bootstrap istimewa (operator): satu baris
	// instalasi + satu baris entitlement untuk workspace fixture.
	if _, err := boot.Exec(
		`INSERT INTO module_installations (workspace_id, module_id, version, status, activated_at, activated_by)
		 VALUES ($1, 'finance.basic', '0.1.0', 'active', now(), $2)`, ws, ownerID); err != nil {
		t.Fatalf("fixture instalasi: %v", err)
	}
	if _, err := boot.Exec(
		`INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source, reason)
		 VALUES ($1, 'finance.basic', '0.1.0', 'operator:fixture', 'fixture', 'tes rerun B1')`, ws); err != nil {
		t.Fatalf("fixture entitlement: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, ws)
		_, _ = boot.Exec(`DELETE FROM module_installations WHERE workspace_id = $1`, ws)
	})

	before := storageSnapshot(t, boot)
	code, out := runPsqlFile(t, bootURL, "migrations/004_module_installations.sql")
	if code != 0 {
		t.Fatalf("rerun 004 tanpa -1: want exit 0, got %d (%s)", code, out)
	}
	after := storageSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		for k := range before {
			if before[k] != after[k] {
				t.Errorf("rerun mengubah %s:\nsebelum: %q\nsesudah: %q", k, before[k], after[k])
			}
		}
		t.Fatalf("rerun 004 mengubah data/kebijakan/seed/marker")
	}
	// Kebijakan tetap efektif: owner masih punya manage_modules.
	var allowed bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_modules')`, ws, ownerID).Scan(&allowed); err != nil {
		t.Fatalf("has_permission sesudah rerun: %v", err)
	}
	if !allowed {
		t.Fatalf("has_permission owner sesudah rerun: want true, got false")
	}
}

// TestModuleStorageRLSAndPermissions: matriks izin + RLS lewat koneksi
// runtime yang sebenarnya (bukan koneksi istimewa). Owner boleh
// INSERT/UPDATE instalasi hanya di tenant-nya; manager/kasir/nonmember
// tidak boleh mutasi; anggota boleh membaca state workspace-nya;
// lintas-tenant dan tanpa konteks mengembalikan nol baris; FK/status
// tidak valid ditolak; DELETE instalasi dan semua DML entitlement
// ditolak di level hak.
func TestModuleStorageRLSAndPermissions(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("rls-owner-%d@contoh.id", suffix)
	mgrEmail := fmt.Sprintf("rls-mgr-%d@contoh.id", suffix)
	cashEmail := fmt.Sprintf("rls-cash-%d@contoh.id", suffix)
	otherEmail := fmt.Sprintf("rls-lain-%d@contoh.id", suffix)
	tokenOwner := registerAndLogin(t, srv, ownerEmail)
	tokenMgr := registerAndLogin(t, srv, mgrEmail)
	tokenCash := registerAndLogin(t, srv, cashEmail)
	tokenOther := registerAndLogin(t, srv, otherEmail)
	wsA := createWorkspace(t, srv, tokenOwner, "Usaha RLS A")
	wsB := createWorkspace(t, srv, tokenOther, "Usaha RLS B")
	for _, tc := range []struct{ email, role, token string }{
		{mgrEmail, "manager", tokenMgr},
		{cashEmail, "cashier", tokenCash},
	} {
		code, body := doJSON(t, srv, "POST", "/workspaces/"+wsA+"/invites", tokenOwner, map[string]string{
			"email": tc.email, "role": tc.role,
		})
		if code != http.StatusCreated {
			t.Fatalf("invite %s: want 201, got %d (%v)", tc.email, code, body)
		}
		inviteToken, _ := body["token"].(string)
		code, _ = doJSON(t, srv, "POST", "/invites/accept", tc.token, map[string]string{
			"workspace_id": wsA, "token": inviteToken,
		})
		if code != http.StatusOK {
			t.Fatalf("accept %s: want 200, got %d", tc.email, code)
		}
	}
	ownerID := subjectID(t, db, ownerEmail)
	mgrID := subjectID(t, db, mgrEmail)
	cashID := subjectID(t, db, cashEmail)
	otherID := subjectID(t, db, otherEmail)

	// Fixture lewat bootstrap: instalasi di kedua workspace + satu
	// entitlement di workspace A.
	for _, f := range []struct{ ws, subject string }{
		{wsA, ownerID}, {wsB, otherID},
	} {
		if _, err := boot.Exec(
			`INSERT INTO module_installations (workspace_id, module_id, version, status, activated_at, activated_by)
			 VALUES ($1, 'finance.basic', '0.1.0', 'active', now(), $2)`, f.ws, f.subject); err != nil {
			t.Fatalf("fixture instalasi %s: %v", f.ws, err)
		}
	}
	if _, err := boot.Exec(
		`INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source, reason)
		 VALUES ($1, 'finance.basic', '0.1.0', 'operator:fixture', 'fixture', 'tes RLS B1')`, wsA); err != nil {
		t.Fatalf("fixture entitlement: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		_, _ = boot.Exec(`DELETE FROM module_installations WHERE workspace_id IN ($1, $2)`, wsA, wsB)
	})

	// has_permission langsung: hanya owner yang punya manage_modules.
	for name, subj := range map[string]struct {
		id   string
		want bool
	}{
		"owner":     {ownerID, true},
		"manager":   {mgrID, false},
		"cashier":   {cashID, false},
		"nonmember": {otherID, false},
	} {
		var allowed bool
		if err := db.QueryRow(`SELECT app.has_permission($1, $2, 'manage_modules')`, wsA, subj.id).Scan(&allowed); err != nil {
			t.Fatalf("has_permission %s: %v", name, err)
		}
		if allowed != subj.want {
			t.Fatalf("has_permission %s manage_modules: want %v, got %v", name, subj.want, allowed)
		}
	}

	countIn := func(tx *sql.Tx, query string, args ...any) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("count %q: %v", query, err)
		}
		return n
	}
	// Baca: owner/manager/kasir melihat 1 baris state workspace A.
	for name, subj := range map[string]string{"owner": ownerID, "manager": mgrID, "cashier": cashID} {
		tx := tenantTx(t, db, wsA, subj)
		if n := countIn(tx, `SELECT count(*) FROM module_installations WHERE workspace_id = $1`, wsA); n != 1 {
			tx.Rollback()
			t.Fatalf("%s baca instalasi A: want 1, got %d", name, n)
		}
		if n := countIn(tx, `SELECT count(*) FROM module_entitlements WHERE workspace_id = $1`, wsA); n != 1 {
			tx.Rollback()
			t.Fatalf("%s baca entitlement A: want 1, got %d", name, n)
		}
		tx.Rollback()
	}
	// Lintas-tenant: owner A tidak melihat baris workspace B.
	tx := tenantTx(t, db, wsA, ownerID)
	if n := countIn(tx, `SELECT count(*) FROM module_installations WHERE workspace_id = $1`, wsB); n != 0 {
		tx.Rollback()
		t.Fatalf("owner A baca instalasi B: want 0, got %d", n)
	}
	tx.Rollback()
	// Tanpa konteks: koneksi mentah melihat nol baris di kedua tabel.
	for _, table := range []string{"module_installations", "module_entitlements"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("raw count %s: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("tanpa konteks %s: want 0, got %d", table, n)
		}
	}

	// Satu eksekusi dalam transaksi tenant baru; galat PostgreSQL
	// membatalkan transaksi, jadi tiap kasus gagal memakai tx sendiri.
	tryExec := func(ws, subj, query string, args ...any) (int64, error) {
		t.Helper()
		tx := tenantTx(t, db, ws, subj)
		defer tx.Rollback()
		res, err := tx.Exec(query, args...)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		return n, nil
	}
	expectFail := func(ws, subj, query, code, substr string, args ...any) {
		t.Helper()
		_, err := tryExec(ws, subj, query, args...)
		if err == nil {
			t.Fatalf("query %q seharusnya gagal, malah lolos", query)
		}
		if pgCode(err) != code {
			t.Fatalf("query %q: want SQLSTATE %s, got %s (%v)", query, code, pgCode(err), err)
		}
		if !strings.Contains(err.Error(), substr) {
			t.Fatalf("query %q: want pesan mengandung %q, got %v", query, substr, err)
		}
	}

	// Owner boleh INSERT lalu UPDATE instalasi di tenant-nya (rollback).
	tx = tenantTx(t, db, wsA, ownerID)
	res, err := tx.Exec(`INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'rental.basic', '0.1.0', 'inactive')`, wsA)
	if err != nil {
		tx.Rollback()
		t.Fatalf("owner INSERT instalasi: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		tx.Rollback()
		t.Fatalf("owner INSERT instalasi: want 1 baris, got %d", n)
	}
	res, err = tx.Exec(`UPDATE module_installations SET status = 'active', activated_at = now(), activated_by = $2 WHERE workspace_id = $1 AND module_id = 'rental.basic'`, wsA, ownerID)
	if err != nil {
		tx.Rollback()
		t.Fatalf("owner UPDATE instalasi: %v", err)
	}
	if n, _ := res.RowsAffected(); n != 1 {
		tx.Rollback()
		t.Fatalf("owner UPDATE instalasi: want 1 baris, got %d", n)
	}
	tx.Rollback()

	// Manager/kasir/nonmember tidak boleh INSERT instalasi.
	insertQ := `INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'spa.standard', '0.1.0', 'inactive')`
	for _, subj := range map[string]string{"manager": mgrID, "cashier": cashID, "nonmember": otherID} {
		expectFail(wsA, subj, insertQ, "42501", "row-level security", wsA)
	}
	// Owner tidak boleh memindahkan baris ke workspace lain (WITH CHECK).
	expectFail(wsA, ownerID, `INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'spa.standard', '0.1.0', 'inactive')`, "42501", "row-level security", wsB)
	// UPDATE lintas-tenant tidak menyentuh baris apa pun.
	if n, err := tryExec(wsA, ownerID, `UPDATE module_installations SET status = 'inactive' WHERE workspace_id = $1`, wsB); err != nil {
		t.Fatalf("UPDATE lintas-tenant: seharusnya 0 baris tanpa galat, got %v", err)
	} else if n != 0 {
		t.Fatalf("UPDATE lintas-tenant: want 0 baris, got %d", n)
	}
	// FK/version/status tidak valid ditolak.
	expectFail(wsA, ownerID, `INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'tidak.ada', '0.1.0', 'inactive')`, "23503", "violates foreign key", wsA)
	expectFail(wsA, ownerID, `INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'rental.basic', '9.9.9', 'inactive')`, "23503", "violates foreign key", wsA)
	expectFail(wsA, ownerID, `INSERT INTO module_installations (workspace_id, module_id, version, status) VALUES ($1, 'spa.standard', '0.1.0', 'bogus')`, "23514", "violates check", wsA)
	// DELETE instalasi + semua DML entitlement ditolak di level hak.
	expectFail(wsA, ownerID, `DELETE FROM module_installations WHERE workspace_id = $1`, "42501", "permission denied for table", wsA)
	expectFail(wsA, ownerID, `INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source) VALUES ($1, 'rental.basic', '0.1.0', 'x', 'x')`, "42501", "permission denied for table", wsA)
	expectFail(wsA, ownerID, `UPDATE module_entitlements SET reason = 'x' WHERE workspace_id = $1`, "42501", "permission denied for table", wsA)
	expectFail(wsA, ownerID, `DELETE FROM module_entitlements WHERE workspace_id = $1`, "42501", "permission denied for table", wsA)
}

// TestReadinessRequires012: checkDatabase menolak skema tanpa 012
// ("error" -> /readyz 503) — termasuk skema 001-010 + 013 tanpa 012
// — dan menerima skema lengkap 001-010 + 012 + 013 ("ok" -> 200);
// marker ekstra yang tidak dikenal tetap "ok". Database scratch
// dipakai supaya tes tidak bergantung pada fixture
// umnia_test/umnia_extra yang berubah-ubah.
func TestReadinessRequires012(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	scratchURL, scratch := scratchDB(t, "umnia_g_readiness")
	for _, f := range migrations001to010 {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	if got := checkDatabase(scratch); got != "error" {
		t.Fatalf("checkDatabase tanpa 012+013: want error, got %q", got)
	}
	// 013 tidak bergantung pada 012 (FK ke kontrak/fakta harga saja),
	// jadi bisa diterapkan tanpa 012: readiness tetap "error".
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "013_rental_payments.sql")); code != 0 {
		t.Fatalf("terapkan 013: want exit 0, got %d (%s)", code, out)
	}
	if got := checkDatabase(scratch); got != "error" {
		t.Fatalf("checkDatabase tanpa 012: want error, got %q", got)
	}
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "012_rental_damaged_lost_return.sql")); code != 0 {
		t.Fatalf("terapkan 012: want exit 0, got %d (%s)", code, out)
	}
	if got := checkDatabase(scratch); got != "ok" {
		t.Fatalf("checkDatabase dengan 012+013: want ok, got %q", got)
	}
	if _, err := scratch.Exec(`INSERT INTO schema_migrations (version) VALUES ('999_future')`); err != nil {
		t.Fatalf("sisip marker ekstra: %v", err)
	}
	if got := checkDatabase(scratch); got != "ok" {
		t.Fatalf("checkDatabase 012+013 + ekstra: want ok, got %q", got)
	}
}

// TestReadinessRequires013: checkDatabase menolak skema tanpa 013
// ("error" -> /readyz 503) — termasuk skema lengkap 001-010 + 012 —
// dan menerima skema lengkap 001-010 + 012 + 013 ("ok" -> 200);
// marker ekstra yang tidak dikenal tetap "ok". Database scratch
// dipakai supaya tes tidak bergantung pada fixture
// umnia_test/umnia_extra yang berubah-ubah.
func TestReadinessRequires013(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	scratchURL, scratch := scratchDB(t, "umnia_pay_readiness")
	for _, f := range migrations001to010 {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	if got := checkDatabase(scratch); got != "error" {
		t.Fatalf("checkDatabase tanpa 012+013: want error, got %q", got)
	}
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "012_rental_damaged_lost_return.sql")); code != 0 {
		t.Fatalf("terapkan 012: want exit 0, got %d (%s)", code, out)
	}
	if got := checkDatabase(scratch); got != "error" {
		t.Fatalf("checkDatabase tanpa 013: want error, got %q", got)
	}
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "013_rental_payments.sql")); code != 0 {
		t.Fatalf("terapkan 013: want exit 0, got %d (%s)", code, out)
	}
	if got := checkDatabase(scratch); got != "ok" {
		t.Fatalf("checkDatabase dengan 013: want ok, got %q", got)
	}
	if _, err := scratch.Exec(`INSERT INTO schema_migrations (version) VALUES ('999_future')`); err != nil {
		t.Fatalf("sisip marker ekstra: %v", err)
	}
	if got := checkDatabase(scratch); got != "ok" {
		t.Fatalf("checkDatabase 013 + ekstra: want ok, got %q", got)
	}
}
