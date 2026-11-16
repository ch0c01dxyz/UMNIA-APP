package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Tes integrasi master data core F2-01-A (customers + catalog items,
// migrasi 005) terhadap PostgreSQL asli: skema segar + rerun, RLS/hak
// runtime, HTTP create/list, audit fault, konkurensi SKU, dan aktivasi
// rental lewat penyedia core nyata. Entitlement di sini HANYA fixture
// sintetis terisolasi lewat bootstrap (bukan grant nyata); tidak ada
// otoritas pemberian entitlement, billing, guard, atau endpoint bisnis
// yang diuji.

// setupMasterData memakai env B3 (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris master data fixture (instalasi +
// entitlement dibersihkan setupActivation).
func setupMasterData(t *testing.T) *activationEnv {
	t.Helper()
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM catalog_items WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
		_, _ = env.boot.Exec(`DELETE FROM parties WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

func createCustomer(t *testing.T, srv *httptest.Server, ws, token string, body any) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/customers", token, body)
}

func createCatalogItem(t *testing.T, srv *httptest.Server, ws, token string, body any) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/catalog/items", token, body)
}

func listCustomers(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/customers"+query, token, nil)
}

func listCatalogItems(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/catalog/items"+query, token, nil)
}

// doRaw mengirim body mentah (untuk uji JSON malformed/trailing dan
// body melebihi 16 KiB) dan mengembalikan status + body ter-decode.
func doRaw(t *testing.T, srv *httptest.Server, method, path, token, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+path, strings.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func countBusinessAudits(t *testing.T, boot *sql.DB, ws, action string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = $2`, ws, action).Scan(&n); err != nil {
		t.Fatalf("count audit %s: %v", action, err)
	}
	return n
}

func countRows(t *testing.T, boot *sql.DB, table, ws string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestMasterDataFreshSchema: migrasi 005+006 pada database segar — enam
// marker, dua tabel kosong, pemilik bootstrap (bukan runtime), RLS
// ENABLE+FORCE, hak efektif runtime (SELECT/INSERT saja), kebijakan,
// CHECK, dan seed izin manage_business_data.
func TestMasterDataFreshSchema(t *testing.T) {
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
	for _, table := range []string{"parties", "catalog_items"} {
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
	// Hak efektif runtime (diperiksa untuk role itu): SELECT/INSERT
	// boleh; UPDATE/DELETE/TRUNCATE tidak.
	type priv struct {
		table, action string
		want          bool
	}
	for _, p := range []priv{
		{"parties", "SELECT", true},
		{"parties", "INSERT", true},
		{"parties", "UPDATE", false},
		{"parties", "DELETE", false},
		{"parties", "TRUNCATE", false},
		{"catalog_items", "SELECT", true},
		{"catalog_items", "INSERT", true},
		{"catalog_items", "UPDATE", false},
		{"catalog_items", "DELETE", false},
		{"catalog_items", "TRUNCATE", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', $1, $2)`, p.table, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege %s %s: %v", p.table, p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime %s %s: want %v, got %v", p.table, p.action, p.want, got)
		}
	}
	// Kebijakan: SELECT + INSERT terpisah di tiap tabel.
	for _, want := range []struct{ table, policy, cmd string }{
		{"parties", "parties_select", "SELECT"},
		{"parties", "parties_insert", "INSERT"},
		{"catalog_items", "catalog_items_select", "SELECT"},
		{"catalog_items", "catalog_items_insert", "INSERT"},
	} {
		var n int
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = $2 AND cmd = $3`,
			want.table, want.policy, want.cmd).Scan(&n); err != nil {
			t.Fatalf("cek policy %s: %v", want.policy, err)
		}
		if n != 1 {
			t.Fatalf("policy %s %s: want 1, got %d", want.table, want.policy, n)
		}
	}
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename IN ('parties', 'catalog_items')`).Scan(&n); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if n != 4 {
		t.Fatalf("policies master data: want 4, got %d", n)
	}
	// CHECK: parties 2 (type, display_name), catalog_items 4 (sku,
	// name, unit, status) + 1 UNIQUE (workspace_id, sku). Migrasi 006
	// menambah UNIQUE (workspace_id, id) pada kedua tabel sebagai
	// target FK komposit rental_contracts, jadi UNIQUE menjadi 1 dan 2.
	for _, want := range []struct {
		table  string
		checks int
		unique int
	}{
		{"parties", 2, 1},
		{"catalog_items", 4, 2},
	} {
		var c, u int
		if err := boot.QueryRow(
			`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u')
			 FROM pg_constraint WHERE conrelid = $1::regclass`, want.table).Scan(&c, &u); err != nil {
			t.Fatalf("constraints %s: %v", want.table, err)
		}
		if c != want.checks || u != want.unique {
			t.Fatalf("constraints %s: want CHECK %d + UNIQUE %d, got CHECK %d + UNIQUE %d", want.table, want.checks, want.unique, c, u)
		}
	}
	// Seed izin: manage_business_data hanya owner + manager.
	var roles string
	if err := boot.QueryRow(
		`SELECT string_agg(role, ',' ORDER BY role) FROM role_permissions WHERE permission = 'manage_business_data'`,
	).Scan(&roles); err != nil {
		t.Fatalf("seed manage_business_data: %v", err)
	}
	if roles != "manager,owner" {
		t.Fatalf("seed manage_business_data: want manager,owner, got %q", roles)
	}
}

// masterDataSnapshot menangkap data/kebijakan/ACL/seed/marker untuk
// uji rerun 005 (pola storageSnapshot B1).
func masterDataSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"parties":       `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || type || '|' || display_name || '|' || created_at::text || '|' || updated_at::text, chr(10) ORDER BY id), '') FROM parties`,
		"catalog_items": `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || sku || '|' || name || '|' || unit || '|' || status || '|' || revision::text || '|' || created_at::text || '|' || updated_at::text, chr(10) ORDER BY id), '') FROM catalog_items`,
		"permissions":   `SELECT coalesce(string_agg(role || ':' || permission, ',' ORDER BY role, permission), '') FROM role_permissions`,
		"policies":      `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('parties', 'catalog_items')`,
		"acl":           `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('parties', 'catalog_items')`,
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

// TestMasterDataRerunPreservesData: rerun 005 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// seed, kebijakan, ACL, dan marker — dan kebijakan tetap efektif
// sesudahnya (has_permission owner tetap true).
func TestMasterDataRerunPreservesData(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("rerun-md-owner-%d@contoh.id", suffix)
	tokenOwner := registerAndLogin(t, srv, ownerEmail)
	ws := createWorkspace(t, srv, tokenOwner, "Usaha Rerun F2-01-A")
	ownerID := subjectID(t, db, ownerEmail)

	if _, err := boot.Exec(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan Rerun')`, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RERUN', 'Barang Rerun', 'pcs', 'active')`, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE workspace_id = $1`, ws)
		_, _ = boot.Exec(`DELETE FROM parties WHERE workspace_id = $1`, ws)
	})

	before := masterDataSnapshot(t, boot)
	code, out := runPsqlFile(t, bootURL, "migrations/005_core_master_data.sql")
	if code != 0 {
		t.Fatalf("rerun 005 tanpa -1: want exit 0, got %d (%s)", code, out)
	}
	after := masterDataSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		for k := range before {
			if before[k] != after[k] {
				t.Errorf("rerun mengubah %s:\nsebelum: %q\nsesudah: %q", k, before[k], after[k])
			}
		}
		t.Fatalf("rerun 005 mengubah data/kebijakan/seed/marker")
	}
	var allowed bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, ownerID).Scan(&allowed); err != nil {
		t.Fatalf("has_permission sesudah rerun: %v", err)
	}
	if !allowed {
		t.Fatalf("has_permission owner sesudah rerun: want true, got false")
	}
}

// TestMasterDataRLSAndPermissions: matriks izin + RLS lewat koneksi
// runtime yang sebenarnya (bukan koneksi istimewa). Owner/manager boleh
// INSERT di tenant-nya; kasir/nonmember tidak boleh mutasi; anggota
// boleh membaca state workspace-nya; lintas-tenant dan tanpa konteks
// mengembalikan nol baris; INSERT blank/oversized/type/status tidak
// valid ditolak CHECK; UPDATE/DELETE/TRUNCATE ditolak di level hak.
func TestMasterDataRLSAndPermissions(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("md-rls-owner-%d@contoh.id", suffix)
	mgrEmail := fmt.Sprintf("md-rls-mgr-%d@contoh.id", suffix)
	cashEmail := fmt.Sprintf("md-rls-cash-%d@contoh.id", suffix)
	otherEmail := fmt.Sprintf("md-rls-lain-%d@contoh.id", suffix)
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

	// Fixture lewat bootstrap: satu pelanggan + satu barang di tiap
	// workspace.
	for _, ws := range []string{wsA, wsB} {
		if _, err := boot.Exec(`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan RLS')`, ws); err != nil {
			t.Fatalf("fixture party %s: %v", ws, err)
		}
		if _, err := boot.Exec(`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS', 'Barang RLS', 'pcs', 'active')`, ws); err != nil {
			t.Fatalf("fixture item %s: %v", ws, err)
		}
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		_, _ = boot.Exec(`DELETE FROM parties WHERE workspace_id IN ($1, $2)`, wsA, wsB)
	})

	// has_permission langsung: manage_business_data hanya owner+manager;
	// view_workspace untuk ketiga role anggota, bukan nonmember.
	for name, tc := range map[string]struct {
		id   string
		perm string
		want bool
	}{
		"owner/manage":     {ownerID, "manage_business_data", true},
		"manager/manage":   {mgrID, "manage_business_data", true},
		"cashier/manage":   {cashID, "manage_business_data", false},
		"nonmember/manage": {otherID, "manage_business_data", false},
		"owner/view":       {ownerID, "view_workspace", true},
		"manager/view":     {mgrID, "view_workspace", true},
		"cashier/view":     {cashID, "view_workspace", true},
		"nonmember/view":   {otherID, "view_workspace", false},
	} {
		var allowed bool
		if err := db.QueryRow(`SELECT app.has_permission($1, $2, $3)`, wsA, tc.id, tc.perm).Scan(&allowed); err != nil {
			t.Fatalf("has_permission %s: %v", name, err)
		}
		if allowed != tc.want {
			t.Fatalf("has_permission %s %s: want %v, got %v", name, tc.perm, tc.want, allowed)
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
	// Baca: owner/manager/kasir melihat 1 baris tiap tabel di A.
	for name, subj := range map[string]string{"owner": ownerID, "manager": mgrID, "cashier": cashID} {
		tx := tenantTx(t, db, wsA, subj)
		if n := countIn(tx, `SELECT count(*) FROM parties WHERE workspace_id = $1`, wsA); n != 1 {
			tx.Rollback()
			t.Fatalf("%s baca parties A: want 1, got %d", name, n)
		}
		if n := countIn(tx, `SELECT count(*) FROM catalog_items WHERE workspace_id = $1`, wsA); n != 1 {
			tx.Rollback()
			t.Fatalf("%s baca items A: want 1, got %d", name, n)
		}
		tx.Rollback()
	}
	// Nonmember A dan koneksi tanpa konteks melihat 0 baris.
	tx := tenantTx(t, db, wsA, otherID)
	if n := countIn(tx, `SELECT count(*) FROM parties WHERE workspace_id = $1`, wsA); n != 0 {
		tx.Rollback()
		t.Fatalf("nonmember baca parties A: want 0, got %d", n)
	}
	if n := countIn(tx, `SELECT count(*) FROM catalog_items WHERE workspace_id = $1`, wsA); n != 0 {
		tx.Rollback()
		t.Fatalf("nonmember baca items A: want 0, got %d", n)
	}
	tx.Rollback()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM parties`).Scan(&n); err != nil {
		t.Fatalf("raw count parties: %v", err)
	}
	if n != 0 {
		t.Fatalf("RLS: koneksi tanpa konteks seharusnya melihat 0 parties, got %d", n)
	}
	if err := db.QueryRow(`SELECT count(*) FROM catalog_items`).Scan(&n); err != nil {
		t.Fatalf("raw count items: %v", err)
	}
	if n != 0 {
		t.Fatalf("RLS: koneksi tanpa konteks seharusnya melihat 0 items, got %d", n)
	}

	// INSERT lewat runtime: owner + manager boleh (commit + verifikasi
	// baris), kasir/nonmember ditolak 42501.
	tryInsert := func(ws, subj, query string, args ...any) (string, error) {
		t.Helper()
		tx := tenantTx(t, db, ws, subj)
		_, err := tx.Exec(query, args...)
		if err != nil {
			tx.Rollback()
			return pgCode(err), err
		}
		if err := tx.Commit(); err != nil {
			return pgCode(err), err
		}
		return "", nil
	}
	if code, err := tryInsert(wsA, ownerID, `INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan Owner')`, wsA); err != nil {
		t.Fatalf("owner INSERT party: want ok, got %s (%v)", code, err)
	}
	if countRows(t, boot, "parties", wsA) != 2 {
		t.Fatalf("owner INSERT party: want 2 baris di A, got %d", countRows(t, boot, "parties", wsA))
	}
	if code, err := tryInsert(wsA, mgrID, `INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-MGR', 'Barang Mgr', 'pcs', 'active')`, wsA); err != nil {
		t.Fatalf("manager INSERT item: want ok, got %s (%v)", code, err)
	}
	if countRows(t, boot, "catalog_items", wsA) != 2 {
		t.Fatalf("manager INSERT item: want 2 baris di A, got %d", countRows(t, boot, "catalog_items", wsA))
	}
	for name, subj := range map[string]string{"cashier": cashID, "nonmember": otherID} {
		if code, err := tryInsert(wsA, subj, `INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan X')`, wsA); err == nil || code != "42501" {
			t.Fatalf("%s INSERT party: want 42501, got code %q err %v", name, code, err)
		}
		if code, err := tryInsert(wsA, subj, `INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-X', 'Barang X', 'pcs', 'active')`, wsA); err == nil || code != "42501" {
			t.Fatalf("%s INSERT item: want 42501, got code %q err %v", name, code, err)
		}
	}
	// Pindah tenant lewat INSERT langsung ditolak (WITH CHECK): konteks
	// A, workspace_id B.
	if code, err := tryInsert(wsA, ownerID, `INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pindah')`, wsB); err == nil || code != "42501" {
		t.Fatalf("INSERT lintas tenant: want 42501, got code %q err %v", code, err)
	}
	if code, err := tryInsert(wsA, ownerID, `INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-PINDAH', 'Pindah', 'pcs', 'active')`, wsB); err == nil || code != "42501" {
		t.Fatalf("INSERT item lintas tenant: want 42501, got code %q err %v", code, err)
	}
	// CHECK langsung: blank/oversized/type/status tidak valid → 23514.
	for name, tc := range map[string]struct {
		q    string
		args []any
	}{
		"party blank":         {`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', '')`, []any{wsA}},
		"party spasi":         {`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', '   ')`, []any{wsA}},
		"party panjang":       {`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', $2)`, []any{wsA, strings.Repeat("x", 201)}},
		"party supplier":      {`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'supplier', 'Pemasok')`, []any{wsA}},
		"item sku blank":      {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, '', 'B', 'pcs', 'active')`, []any{wsA}},
		"item sku panjang":    {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, $2, 'B', 'pcs', 'active')`, []any{wsA, strings.Repeat("x", 65)}},
		"item name panjang":   {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-N', $2, 'pcs', 'active')`, []any{wsA, strings.Repeat("x", 201)}},
		"item unit blank":     {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-U', 'B', '', 'active')`, []any{wsA}},
		"item unit panjang":   {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-U2', 'B', $2, 'active')`, []any{wsA, strings.Repeat("x", 33)}},
		"item status invalid": {`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-S', 'B', 'pcs', 'deleted')`, []any{wsA}},
	} {
		if code, err := tryInsert(wsA, ownerID, tc.q, tc.args...); err == nil || code != "23514" {
			t.Fatalf("%s: want 23514, got code %q err %v", name, code, err)
		}
	}
	// Duplikat SKU di workspace yang sama → 23505; SKU sama di
	// workspace lain boleh (fixture B sudah memakai SKU-RLS).
	if code, err := tryInsert(wsA, ownerID, `INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS', 'Duplikat', 'pcs', 'active')`, wsA); err == nil || code != "23505" {
		t.Fatalf("duplikat SKU: want 23505, got code %q err %v", code, err)
	}
	// UPDATE/DELETE/TRUNCATE lewat runtime ditolak 42501.
	tx2 := tenantTx(t, db, wsA, ownerID)
	if _, err := tx2.Exec(`UPDATE parties SET display_name = 'Ubah' WHERE workspace_id = $1`, wsA); pgCode(err) != "42501" {
		tx2.Rollback()
		t.Fatalf("UPDATE party: want 42501, got %v", err)
	}
	tx2.Rollback()
	tx2 = tenantTx(t, db, wsA, ownerID)
	if _, err := tx2.Exec(`DELETE FROM catalog_items WHERE workspace_id = $1`, wsA); pgCode(err) != "42501" {
		tx2.Rollback()
		t.Fatalf("DELETE item: want 42501, got %v", err)
	}
	tx2.Rollback()
	if _, err := db.Exec(`TRUNCATE parties`); pgCode(err) != "42501" {
		t.Fatalf("TRUNCATE parties: want 42501, got %v", err)
	}
}

// TestMasterDataHTTP: matriks HTTP create/list — izin per role, isolasi
// lintas tenant, validasi body/pagination, duplikat SKU, dan bentuk
// respons (tanpa metadata request yang bocor, timestamp UTC).
func TestMasterDataHTTP(t *testing.T) {
	env := setupMasterData(t)
	// Create customer (owner) → 201, type dikunci customer, nama
	// ter-trim, timestamp UTC (Z), kunci respons tepat.
	code, body := createCustomer(t, env.srv, env.wsA, env.ownerToken, map[string]string{"display_name": "  Pelanggan Satu  "})
	if code != http.StatusCreated {
		t.Fatalf("create customer owner: want 201, got %d (%v)", code, body)
	}
	for _, k := range []string{"id", "workspace_id", "type", "display_name", "created_at", "updated_at"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("create customer: kunci %q hilang (%v)", k, body)
		}
	}
	if len(body) != 6 {
		t.Fatalf("create customer: want 6 kunci, got %d (%v)", len(body), body)
	}
	if body["workspace_id"] != env.wsA || body["type"] != "customer" || body["display_name"] != "Pelanggan Satu" {
		t.Fatalf("create customer: field tidak sesuai (%v)", body)
	}
	custID, _ := body["id"].(string)
	if custID == "" {
		t.Fatalf("create customer: id kosong")
	}
	for _, k := range []string{"created_at", "updated_at"} {
		ts, _ := body[k].(string)
		if !strings.HasSuffix(ts, "Z") {
			t.Fatalf("create customer %s: want UTC (Z), got %q", k, ts)
		}
	}
	// Create customer (manager) → 201; kasir → 403; tanpa token → 401.
	code, _ = createCustomer(t, env.srv, env.wsA, env.mgrToken, map[string]string{"display_name": "Pelanggan Dua"})
	if code != http.StatusCreated {
		t.Fatalf("create customer manager: want 201, got %d", code)
	}
	code, body = createCustomer(t, env.srv, env.wsA, env.cashToken, map[string]string{"display_name": "Pelanggan Kasir"})
	if code != http.StatusForbidden {
		t.Fatalf("create customer kasir: want 403, got %d (%v)", code, body)
	}
	code, _ = createCustomer(t, env.srv, env.wsA, "", map[string]string{"display_name": "Pelanggan Anon"})
	if code != http.StatusUnauthorized {
		t.Fatalf("create customer tanpa token: want 401, got %d", code)
	}
	// Create item (owner) → 201, status active, revision 0.
	code, body = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": "  ABC-1 ", "name": "Barang Satu", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("create item owner: want 201, got %d (%v)", code, body)
	}
	if body["sku"] != "ABC-1" || body["status"] != "active" || body["revision"] != float64(0) {
		t.Fatalf("create item: field tidak sesuai (%v)", body)
	}
	if len(body) != 9 {
		t.Fatalf("create item: want 9 kunci, got %d (%v)", len(body), body)
	}
	code, _ = createCatalogItem(t, env.srv, env.wsA, env.mgrToken, map[string]string{"sku": "ABC-2", "name": "Barang Dua", "unit": "box"})
	if code != http.StatusCreated {
		t.Fatalf("create item manager: want 201, got %d", code)
	}
	code, _ = createCatalogItem(t, env.srv, env.wsA, env.cashToken, map[string]string{"sku": "ABC-3", "name": "Barang Kasir", "unit": "pcs"})
	if code != http.StatusForbidden {
		t.Fatalf("create item kasir: want 403, got %d", code)
	}
	// List (kasir boleh baca): 2 pelanggan + 2 barang di wsA.
	code, body = listCustomers(t, env.srv, env.wsA, env.cashToken, "")
	if code != http.StatusOK {
		t.Fatalf("list customers kasir: want 200, got %d (%v)", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list customers: want 2, got %d (%v)", len(items), body)
	}
	code, body = listCatalogItems(t, env.srv, env.wsA, env.cashToken, "")
	if code != http.StatusOK {
		t.Fatalf("list items kasir: want 200, got %d (%v)", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list items: want 2, got %d (%v)", len(items), body)
	}
	// Nonmember/lintas-tenant: 403 tanpa baris bocor; tanpa token: 401.
	code, body = listCustomers(t, env.srv, env.wsA, env.otherToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("list customers nonmember: want 403, got %d (%v)", code, body)
	}
	code, _ = createCustomer(t, env.srv, env.wsA, env.otherToken, map[string]string{"display_name": "Pelanggan Lain"})
	if code != http.StatusForbidden {
		t.Fatalf("create customer nonmember: want 403, got %d", code)
	}
	code, _ = listCustomers(t, env.srv, env.wsA, "", "")
	if code != http.StatusUnauthorized {
		t.Fatalf("list customers tanpa token: want 401, got %d", code)
	}
	// Anggota dua workspace (other diundang ke wsA sebagai manager)
	// hanya melihat baris workspace yang diminta.
	var otherEmail string
	if err := env.boot.QueryRow(`SELECT email FROM identity_subjects WHERE id = $1`, env.otherID).Scan(&otherEmail); err != nil {
		t.Fatalf("email other: %v", err)
	}
	code, body = doJSON(t, env.srv, "POST", "/workspaces/"+env.wsA+"/invites", env.ownerToken, map[string]string{
		"email": otherEmail, "role": "manager",
	})
	if code != http.StatusCreated {
		t.Fatalf("invite other ke wsA: want 201, got %d (%v)", code, body)
	}
	// `other` sudah anggota wsB (pemilik) dan kini diundang ke wsA;
	// terima undangan dengan token other.
	inviteToken, _ := body["token"].(string)
	code, _ = doJSON(t, env.srv, "POST", "/invites/accept", env.otherToken, map[string]string{
		"workspace_id": env.wsA, "token": inviteToken,
	})
	if code != http.StatusOK {
		t.Fatalf("accept other ke wsA: want 200, got %d", code)
	}
	code, _ = createCustomer(t, env.srv, env.wsB, env.otherToken, map[string]string{"display_name": "Pelanggan B"})
	if code != http.StatusCreated {
		t.Fatalf("create customer wsB: want 201, got %d", code)
	}
	code, body = listCustomers(t, env.srv, env.wsA, env.otherToken, "")
	if code != http.StatusOK {
		t.Fatalf("list wsA sebagai anggota dua workspace: want 200, got %d (%v)", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list wsA: want 2, got %d (%v)", len(items), body)
	}
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["workspace_id"] != env.wsA {
			t.Fatalf("list wsA bocor baris workspace lain: %v", m)
		}
	}
	code, body = listCustomers(t, env.srv, env.wsB, env.otherToken, "")
	if code != http.StatusOK {
		t.Fatalf("list wsB: want 200, got %d (%v)", code, body)
	}
	items, _ = body["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list wsB: want 1, got %d (%v)", len(items), body)
	}
	// Validasi body → 400, tanpa baris baru.
	badBodies := []struct {
		name string
		raw  string
	}{
		{"malformed", `{"display_name":}`},
		{"trailing", `{"display_name":"X"} {"display_name":"Y"}`},
		{"unknown workspace_id", `{"display_name":"X","workspace_id":"00000000-0000-0000-0000-000000000000"}`},
		{"unknown type", `{"display_name":"X","type":"supplier"}`},
		{"unknown id", `{"display_name":"X","id":"00000000-0000-0000-0000-000000000000"}`},
		{"wrong type", `{"display_name":123}`},
		{"blank", `{"display_name":"   "}`},
		{"oversized name", `{"display_name":"` + strings.Repeat("x", 201) + `"}`},
		{"empty", ``},
		{"oversized body", `{"display_name":"` + strings.Repeat("x", 17*1024) + `"}`},
	}
	for _, tc := range badBodies {
		code, _ := doRaw(t, env.srv, "POST", "/workspaces/"+env.wsA+"/customers", env.ownerToken, tc.raw)
		if code != http.StatusBadRequest {
			t.Fatalf("validasi customer %s: want 400, got %d", tc.name, code)
		}
	}
	badItems := []struct {
		name string
		raw  string
	}{
		{"unknown status", `{"sku":"S1","name":"B","unit":"pcs","status":"archived"}`},
		{"unknown revision", `{"sku":"S1","name":"B","unit":"pcs","revision":5}`},
		{"sku blank", `{"sku":"  ","name":"B","unit":"pcs"}`},
		{"sku panjang", `{"sku":"` + strings.Repeat("x", 65) + `","name":"B","unit":"pcs"}`},
		{"name panjang", `{"sku":"S1","name":"` + strings.Repeat("x", 201) + `","unit":"pcs"}`},
		{"unit blank", `{"sku":"S1","name":"B","unit":""}`},
		{"unit panjang", `{"sku":"S1","name":"B","unit":"` + strings.Repeat("x", 33) + `"}`},
		{"unit wrong type", `{"sku":"S1","name":"B","unit":5}`},
	}
	for _, tc := range badItems {
		code, _ := doRaw(t, env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items", env.ownerToken, tc.raw)
		if code != http.StatusBadRequest {
			t.Fatalf("validasi item %s: want 400, got %d", tc.name, code)
		}
	}
	if n := countRows(t, env.boot, "parties", env.wsA); n != 2 {
		t.Fatalf("validasi gagal menambah party: want 2, got %d", n)
	}
	if n := countRows(t, env.boot, "catalog_items", env.wsA); n != 2 {
		t.Fatalf("validasi gagal menambah item: want 2, got %d", n)
	}
	// Pagination: invalid → 400; valid membagi halaman tanpa duplikat.
	for _, q := range []string{"?limit=0", "?limit=101", "?limit=abc", "?offset=-1", "?offset=abc", "?offset=99999999999999999999999"} {
		code, _ := listCustomers(t, env.srv, env.wsA, env.ownerToken, q)
		if code != http.StatusBadRequest {
			t.Fatalf("pagination %s: want 400, got %d", q, code)
		}
	}
	code, body = listCustomers(t, env.srv, env.wsA, env.ownerToken, "?limit=1&offset=0")
	if code != http.StatusOK {
		t.Fatalf("page 1: want 200, got %d", code)
	}
	page1, _ := body["items"].([]any)
	if len(page1) != 1 {
		t.Fatalf("page 1: want 1, got %d", len(page1))
	}
	code, body = listCustomers(t, env.srv, env.wsA, env.ownerToken, "?limit=1&offset=1")
	if code != http.StatusOK {
		t.Fatalf("page 2: want 200, got %d", code)
	}
	page2, _ := body["items"].([]any)
	if len(page2) != 1 {
		t.Fatalf("page 2: want 1, got %d", len(page2))
	}
	if page1[0].(map[string]any)["id"] == page2[0].(map[string]any)["id"] {
		t.Fatalf("pagination: halaman 1 dan 2 mengembalikan baris yang sama")
	}
	// Duplikat SKU sesudah trim → 409; beda huruf besar/kecil boleh;
	// SKU sama di workspace lain boleh.
	code, _ = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": "abc-1", "name": "Beda Case", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("create sku beda case: want 201, got %d", code)
	}
	code, body = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": " ABC-1 ", "name": "Duplikat", "unit": "pcs"})
	if code != http.StatusConflict {
		t.Fatalf("duplikat SKU trim: want 409, got %d (%v)", code, body)
	}
	code, _ = createCatalogItem(t, env.srv, env.wsB, env.otherToken, map[string]string{"sku": "ABC-1", "name": "SKU Sama B", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("SKU sama di workspace lain: want 201, got %d", code)
	}
	// Baris yang tersimpan: type customer, status active, revision 0.
	var ptype string
	if err := env.boot.QueryRow(`SELECT type FROM parties WHERE id = $1`, custID).Scan(&ptype); err != nil {
		t.Fatalf("baca type party: %v", err)
	}
	if ptype != "customer" {
		t.Fatalf("type party: want customer, got %q", ptype)
	}
}

// TestMasterDataAuditFaultAtomicity: kegagalan audit (trigger) membuat
// create customer/item membalas 500 tanpa baris maupun audit yang
// lolos; sesudah fault dipulihkan, create berhasil. Detail audit tidak
// menyalin nama pelanggan/barang.
func TestMasterDataAuditFaultAtomicity(t *testing.T) {
	env := setupMasterData(t)
	if _, err := env.boot.Exec(`CREATE OR REPLACE FUNCTION app.qa_fail_master_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action IN ('customer_created', 'catalog_item_created') THEN
				RAISE EXCEPTION 'injected audit fault (fixture)';
			END IF;
			RETURN NEW;
		END;
	$$`); err != nil {
		t.Fatalf("create fault function: %v", err)
	}
	if _, err := env.boot.Exec(`CREATE TRIGGER qa_fault_master_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_master_audit()`); err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_master_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_master_audit()`)
	})
	auditsBefore := countBusinessAudits(t, env.boot, env.wsA, "customer_created") +
		countBusinessAudits(t, env.boot, env.wsA, "catalog_item_created")
	code, body := createCustomer(t, env.srv, env.wsA, env.ownerToken, map[string]string{"display_name": "Pelanggan Fault"})
	if code != http.StatusInternalServerError {
		t.Fatalf("create customer dengan audit gagal: want 500, got %d (%v)", code, body)
	}
	msg, _ := body["error"].(string)
	if msg != "audit gagal ditulis" {
		t.Fatalf("500 fault: want %q, got %q", "audit gagal ditulis", msg)
	}
	if strings.Contains(msg, "qa_fail") || strings.Contains(msg, "injected") {
		t.Fatalf("500 fault membocorkan detail: %q", msg)
	}
	code, body = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": "SKU-FAULT", "name": "Barang Fault", "unit": "pcs"})
	if code != http.StatusInternalServerError {
		t.Fatalf("create item dengan audit gagal: want 500, got %d (%v)", code, body)
	}
	if countRows(t, env.boot, "parties", env.wsA) != 0 || countRows(t, env.boot, "catalog_items", env.wsA) != 0 {
		t.Fatalf("fault: baris master data lolos walau audit gagal")
	}
	auditsAfter := countBusinessAudits(t, env.boot, env.wsA, "customer_created") +
		countBusinessAudits(t, env.boot, env.wsA, "catalog_item_created")
	if auditsAfter != auditsBefore {
		t.Fatalf("fault: audit bertambah walau transaksi gagal (%d -> %d)", auditsBefore, auditsAfter)
	}
	code, body = listCustomers(t, env.srv, env.wsA, env.ownerToken, "")
	if code != http.StatusOK {
		t.Fatalf("list sesudah fault: want 200, got %d", code)
	}
	items, _ := body["items"].([]any)
	if len(items) != 0 {
		t.Fatalf("list sesudah fault: want 0, got %d", len(items))
	}
	// Pulihkan fault secara manual, lalu create berhasil — dan detail
	// audit hanya berisi tipe, tanpa nama.
	if _, err := env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_master_audit ON audit_events`); err != nil {
		t.Fatalf("drop fault trigger: %v", err)
	}
	if _, err := env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_master_audit()`); err != nil {
		t.Fatalf("drop fault function: %v", err)
	}
	code, body = createCustomer(t, env.srv, env.wsA, env.ownerToken, map[string]string{"display_name": "Pelanggan Pulih"})
	if code != http.StatusCreated {
		t.Fatalf("create sesudah fault dipulihkan: want 201, got %d (%v)", code, body)
	}
	var details string
	if err := env.boot.QueryRow(
		`SELECT details::text FROM audit_events WHERE workspace_id = $1 AND action = 'customer_created' ORDER BY created_at DESC LIMIT 1`,
		env.wsA).Scan(&details); err != nil {
		t.Fatalf("baca details audit: %v", err)
	}
	if details != `{"type": "customer"}` {
		t.Fatalf("details audit: want %q, got %q", `{"type": "customer"}`, details)
	}
	if strings.Contains(details, "Pelanggan Pulih") {
		t.Fatalf("details audit menyalin nama pelanggan: %q", details)
	}
}

// TestMasterDataConcurrentSameSKU: dua create item dengan SKU sama yang
// bersamaan — satu 201, satu 409, tepat satu barang + satu audit.
func TestMasterDataConcurrentSameSKU(t *testing.T) {
	env := setupMasterData(t)
	createSame := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items", env.ownerToken,
			map[string]string{"sku": "SKU-RACE", "name": "Barang Race", "unit": "pcs"})
	}
	codes, _ := parallel(t, createSame, createSame)
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != http.StatusCreated || codes[1] != http.StatusConflict {
		t.Fatalf("SKU bersamaan: want [201 409], got %v", codes)
	}
	if n := countRows(t, env.boot, "catalog_items", env.wsA); n != 1 {
		t.Fatalf("SKU bersamaan: want 1 barang, got %d", n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "catalog_item_created"); n != 1 {
		t.Fatalf("SKU bersamaan: want 1 audit, got %d", n)
	}
}

// TestMasterDataRentalActivationViaCore: rental.basic berhak kini
// aktif 200 lewat penyedia core nyata tanpa seeding pelanggan/barang
// (tabel kosong valid); tanpa entitlement 403; no-op sesudah revoke
// tetap 200; inactive-to-active sesudah revoke 403; spa tetap 409
// (booking.resource.hold); fault bacaan core → 500 tanpa mutasi.
func TestMasterDataRentalActivationViaCore(t *testing.T) {
	env := setupMasterData(t)
	// (a) Tanpa seeding pelanggan/barang: aktivasi rental 200.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("rental lewat core: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
	if n := countRows(t, env.boot, "parties", env.wsA); n != 0 {
		t.Fatalf("rental aktif tanpa seeding: parties harus 0, got %d", n)
	}
	// (b) Tanpa entitlement (wsB): 403 mendahului dependensi.
	code, body = activateModule(t, env.srv, env.wsB, "rental.basic", env.otherToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("rental tanpa entitlement: want 403, got %d (%v)", code, body)
	}
	// (c) No-op sesudah revoke: aktif + versi sama → 200, tanpa audit
	// baru (snapshot maksud B3, bukan bukti hak efektif).
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	auditsBefore := len(readModuleAudits(t, env.boot, env.wsA))
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("rental no-op sesudah revoke: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
	if len(readModuleAudits(t, env.boot, env.wsA)) != auditsBefore {
		t.Fatalf("no-op sesudah revoke menambah audit")
	}
	// (d) Inactive-to-active sesudah revoke: 403.
	code, _ = deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate rental: want 200, got %d", code)
	}
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("rental aktif ulang sesudah revoke: want 403, got %d (%v)", code, body)
	}
	// (e) Spa tetap 409: booking.resource.hold tak ada penyedia.
	grantEntitlement(t, env.boot, env.wsA, "spa.standard", "0.1.0")
	code, body = activateModule(t, env.srv, env.wsA, "spa.standard", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("spa: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "booking.resource.hold")
	// (f) Fault bacaan core: cabut SELECT runtime pada tiap tabel core
	// → aktivasi 500 tanpa mutasi; pulihkan, lalu aktivasi 200.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	for _, table := range []string{"parties", "catalog_items"} {
		if _, err := env.boot.Exec(`REVOKE SELECT ON ` + table + ` FROM umnia_business`); err != nil {
			t.Fatalf("revoke SELECT %s: %v", table, err)
		}
		auditsBefore := len(readModuleAudits(t, env.boot, env.wsA))
		code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
		if code != http.StatusInternalServerError {
			t.Fatalf("rental fault %s: want 500, got %d (%v)", table, code, body)
		}
		msg, _ := body["error"].(string)
		if msg != "transaksi gagal" {
			t.Fatalf("500 fault %s: want %q, got %q", table, "transaksi gagal", msg)
		}
		code, body = getModuleState(t, env.srv, env.wsA, "rental.basic", env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("GET sesudah fault %s: want 200, got %d", table, code)
		}
		assertState(t, body, "rental.basic", "0.1.0", "inactive")
		if len(readModuleAudits(t, env.boot, env.wsA)) != auditsBefore {
			t.Fatalf("fault %s menambah audit", table)
		}
		if _, err := env.boot.Exec(`GRANT SELECT, INSERT ON ` + table + ` TO umnia_business`); err != nil {
			t.Fatalf("restore grant %s: %v", table, err)
		}
	}
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("rental sesudah fault dipulihkan: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
}

// installPermissionFault mengganti app.has_permission dengan wrapper yang
// melempar galat hanya untuk perm='view_workspace' (cek izin baca data
// core di resolver aktivasi); izin lain didelegasikan ke logika asli.
// Mengembalikan definisi asli (pg_get_functiondef) untuk dipulihkan.
// Fixture sintetis terisolasi; pemulihan wajib di finally/cleanup.
func installPermissionFault(t *testing.T, boot *sql.DB) string {
	t.Helper()
	var def string
	if err := boot.QueryRow(`SELECT pg_get_functiondef('app.has_permission(UUID,UUID,TEXT)'::regprocedure)`).Scan(&def); err != nil {
		t.Fatalf("baca definisi has_permission: %v", err)
	}
	if _, err := boot.Exec(`CREATE OR REPLACE FUNCTION app.has_permission(ws UUID, subj UUID, perm TEXT)
		RETURNS BOOLEAN LANGUAGE plpgsql STABLE SECURITY DEFINER AS $$
		BEGIN
			IF perm = 'view_workspace' THEN
				RAISE EXCEPTION 'synthetic permission fault';
			END IF;
			RETURN app.is_member(ws, subj) AND EXISTS (
				SELECT 1 FROM membership_roles mr
				JOIN role_permissions rp ON rp.role = mr.role
				WHERE mr.workspace_id = ws AND mr.subject_id = subj
					AND rp.permission = perm
			);
		END;
		$$`); err != nil {
		t.Fatalf("pasang permission fault: %v", err)
	}
	return def
}

// restorePermissionFault memulihkan definisi asli app.has_permission.
// No-op bila def kosong (fault tidak pernah terpasang); memutar ulang
// definisi yang sama dua kali tidak berbahaya (CREATE OR REPLACE).
func restorePermissionFault(t *testing.T, boot *sql.DB, def string) {
	t.Helper()
	if def == "" {
		return
	}
	if _, err := boot.Exec(def); err != nil {
		t.Fatalf("pulihkan has_permission: %v", err)
	}
}

// TestCoreReadRequiresViewPermission: subjek anggota dengan manage_modules
// saja (tanpa view_workspace) TIDAK boleh mengaktifkan modul yang
// membutuhkan kapabilitas core. Bacaan core yang sukses — termasuk hasil
// kosong yang disaring RLS — bukan bukti otorisasi baca. Aktivasi pertama
// harus 403 tanpa mutasi/audit; sesudah izin baca dipulihkan, aktivasi
// yang sama 200 pada tabel core kosong yang valid. Fault pada query izin
// baca → 500 tanpa mutasi; sesudah fault dipulihkan, aktivasi 200.
func TestCoreReadRequiresViewPermission(t *testing.T) {
	env := setupMasterData(t)
	boot := env.boot

	// Role sintetis: manage_modules saja, tanpa view_workspace. Reset
	// dulu supaya sisa run sebelumnya tidak membawa izin baca.
	if _, err := boot.Exec(`INSERT INTO roles (name, description) VALUES ('qa-manage-only', 'fixture: manage_modules saja') ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("buat role sintetis: %v", err)
	}
	if _, err := boot.Exec(`DELETE FROM role_permissions WHERE role = 'qa-manage-only'`); err != nil {
		t.Fatalf("reset izin role sintetis: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO role_permissions (role, permission) VALUES ('qa-manage-only', 'manage_modules')`); err != nil {
		t.Fatalf("beri manage_modules ke role sintetis: %v", err)
	}
	// Ganti role manager di wsA dengan role sintetis (membership tetap aktif).
	if _, err := boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2`, env.wsA, env.mgrID); err != nil {
		t.Fatalf("hapus role manager: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'qa-manage-only')`, env.wsA, env.mgrID); err != nil {
		t.Fatalf("pasang role sintetis: %v", err)
	}
	t.Cleanup(func() {
		// Kembalikan role manager, lalu hapus fixture sintetis (FK:
		// membership dulu, baru role_permissions dan roles).
		_, _ = boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2 AND role = 'qa-manage-only'`, env.wsA, env.mgrID)
		_, _ = boot.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'manager') ON CONFLICT (workspace_id, subject_id, role) DO NOTHING`, env.wsA, env.mgrID)
		_, _ = boot.Exec(`DELETE FROM role_permissions WHERE role = 'qa-manage-only'`)
		_, _ = boot.Exec(`DELETE FROM roles WHERE name = 'qa-manage-only'`)
	})

	// (a) Tanpa view_workspace: baca core 403 — izin baca memang tidak
	// ada, jadi aktivasi tidak boleh "membaca lewat belakang".
	code, _ := listCustomers(t, env.srv, env.wsA, env.mgrToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("GET customers tanpa view: want 403, got %d", code)
	}
	code, _ = listCatalogItems(t, env.srv, env.wsA, env.mgrToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("GET items tanpa view: want 403, got %d", code)
	}

	// (b) Entitlement sintetis terisolasi untuk rental.basic; belum ada
	// instalasi (bukan no-op aktif — itu punya semantik lain).
	grantEntitlement(t, boot, env.wsA, "rental.basic", "0.1.0")
	if _, ok := readInstallation(t, boot, env.wsA, "rental.basic"); ok {
		t.Fatalf("prasyarat: rental sudah terinstal")
	}
	auditsBefore := readModuleAudits(t, boot, env.wsA)

	// (c) Aktivasi pertama → 403, tanpa mutasi/audit.
	code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.mgrToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("aktivasi tanpa izin baca core: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "tidak punya izin membaca data core")
	if _, ok := readInstallation(t, boot, env.wsA, "rental.basic"); ok {
		t.Fatalf("aktivasi ditolak tapi baris instalasi terbentuk")
	}
	if auditsAfter := readModuleAudits(t, boot, env.wsA); len(auditsAfter) != len(auditsBefore) {
		t.Fatalf("aktivasi ditolak tapi audit bertambah (%d -> %d)", len(auditsBefore), len(auditsAfter))
	}

	// (d) Pulihkan izin baca (tambah view_workspace ke role sintetis);
	// aktivasi yang sama → 200 pada tabel core kosong yang valid.
	if _, err := boot.Exec(`INSERT INTO role_permissions (role, permission) VALUES ('qa-manage-only', 'view_workspace') ON CONFLICT (role, permission) DO NOTHING`); err != nil {
		t.Fatalf("pulihkan view_workspace: %v", err)
	}
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.mgrToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi sesudah izin baca dipulihkan: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")

	// (e) Fault query izin baca → 500 tanpa mutasi. Nonaktifkan dulu
	// (dengan owner) supaya aktivasi berikut adalah transisi nyata,
	// bukan no-op 200 dari state aktif.
	code, _ = deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deaktivasi sebelum fault: want 200, got %d", code)
	}
	auditsBeforeFault := readModuleAudits(t, boot, env.wsA)
	def := installPermissionFault(t, boot)
	defer restorePermissionFault(t, boot, def)
	t.Cleanup(func() { restorePermissionFault(t, boot, def) })
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.mgrToken, "0.1.0")
	if code != http.StatusInternalServerError {
		t.Fatalf("aktivasi dengan fault izin baca: want 500, got %d (%v)", code, body)
	}
	msg, _ := body["error"].(string)
	if msg != "transaksi gagal" {
		t.Fatalf("500 fault izin baca: want %q, got %q", "transaksi gagal", msg)
	}
	if inst, ok := readInstallation(t, boot, env.wsA, "rental.basic"); !ok {
		t.Fatalf("prasyarat fault: baris instalasi hilang")
	} else if inst.Status != "inactive" {
		t.Fatalf("fault izin baca mengubah state: %q", inst.Status)
	}
	if auditsAfter := readModuleAudits(t, boot, env.wsA); len(auditsAfter) != len(auditsBeforeFault) {
		t.Fatalf("fault izin baca menambah audit (%d -> %d)", len(auditsBeforeFault), len(auditsAfter))
	}

	// (f) Pulihkan fault; aktivasi → 200 (bukti pemulihan).
	restorePermissionFault(t, boot, def)
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.mgrToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi sesudah fault dipulihkan: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
}

// TestCatalogItemBackendErrorIsNotConflict: galat backend P0001 yang
// pesannya mengandung kata "unique" (tanpa duplikat SKU nyata) TIDAK
// boleh dipetakan ke 409 — hanya pelanggaran unik 23505 pada constraint
// (workspace_id, sku) yang berarti "sku sudah dipakai". Sesudah fault
// dipulihkan, create yang sama 201 (SKU memang tidak pernah duplikat).
func TestCatalogItemBackendErrorIsNotConflict(t *testing.T) {
	env := setupMasterData(t)
	boot := env.boot

	// Trigger sintetis: gagalkan INSERT sku SKU-FAULT di wsA dengan
	// P0001 yang pesannya mengandung "unique" (meniru temuan reviewer).
	if _, err := boot.Exec(fmt.Sprintf(`CREATE OR REPLACE FUNCTION app.qa_fail_catalog_insert() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.workspace_id = '%s'::uuid AND NEW.sku = 'SKU-FAULT' THEN
				RAISE EXCEPTION 'synthetic unique backend failure';
			END IF;
			RETURN NEW;
		END;
		$$`, env.wsA)); err != nil {
		t.Fatalf("create fault function: %v", err)
	}
	if _, err := boot.Exec(`CREATE TRIGGER qa_fault_catalog_insert BEFORE INSERT ON catalog_items FOR EACH ROW EXECUTE FUNCTION app.qa_fail_catalog_insert()`); err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_catalog_insert ON catalog_items`)
		_, _ = boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_catalog_insert()`)
	})

	code, body := createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": "SKU-FAULT", "name": "Barang Fault", "unit": "pcs"})
	if code != http.StatusInternalServerError {
		t.Fatalf("create dengan backend fault: want 500, got %d (%v)", code, body)
	}
	msg, _ := body["error"].(string)
	if msg != "barang gagal dibuat" {
		t.Fatalf("500 backend fault: want %q, got %q", "barang gagal dibuat", msg)
	}
	if strings.Contains(msg, "unique") || strings.Contains(msg, "synthetic") {
		t.Fatalf("500 backend fault membocorkan detail: %q", msg)
	}
	if countRows(t, boot, "catalog_items", env.wsA) != 0 {
		t.Fatalf("backend fault: baris barang lolos")
	}
	if countBusinessAudits(t, boot, env.wsA, "catalog_item_created") != 0 {
		t.Fatalf("backend fault: audit lolos")
	}

	// Pulihkan fault; create yang sama → 201.
	if _, err := boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_catalog_insert ON catalog_items`); err != nil {
		t.Fatalf("drop fault trigger: %v", err)
	}
	if _, err := boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_catalog_insert()`); err != nil {
		t.Fatalf("drop fault function: %v", err)
	}
	code, body = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": "SKU-FAULT", "name": "Barang Fault", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("create sesudah fault dipulihkan: want 201, got %d (%v)", code, body)
	}
}
