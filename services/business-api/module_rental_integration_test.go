package main

import (
	"bytes"
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

// Tes integrasi kontrak rental DRAFT F2-01-B (migrasi 006) terhadap
// PostgreSQL asli: skema segar + rerun, RLS/hak runtime/FK komposit,
// HTTP create/list + guard, idempotency, fault guard/audit, konkurensi
// (kunci sama, create vs deactivate, create vs revoke), dan histori
// sesudah deaktivasi/pencabutan. Entitlement di sini HANYA fixture
// sintetis terisolasi lewat bootstrap (bukan grant nyata); tidak ada
// otoritas pemberian entitlement, billing, harga, stok, atau lifecycle
// yang diuji. Draft bukan rental operasional.

// setupRental memakai env master data (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris draft fixture DULU (LIFO: cleanup
// ini didaftarkan sesudah setupMasterData, jadi berjalan sebelum
// penghapusan parties/catalog_items — urutan FK-aman).
func setupRental(t *testing.T) *activationEnv {
	t.Helper()
	env := setupMasterData(t)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM rental_contracts WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

// doDraftReq seperti doJSON tetapi membawa nilai header Idempotency-Key
// (nil = tanpa header; >1 nilai = header ganda).
func doDraftReq(t *testing.T, srv *httptest.Server, method, path, token string, keys []string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
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

func createDraft(t *testing.T, srv *httptest.Server, ws, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts", token, []string{key}, body)
}

// createDraftRaw mengirim body mentah dengan header kunci (untuk uji
// JSON malformed/trailing dan body melebihi 16 KiB).
func createDraftRaw(t *testing.T, srv *httptest.Server, ws, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts", strings.NewReader(raw))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
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

func listDrafts(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/contracts"+query, token, nil)
}

// callDraftAPI adalah varian doDraftReq yang mengembalikan error (bukan
// t.Fatalf) supaya aman dipanggil dari goroutine pada tes konkurensi.
func callDraftAPI(srv *httptest.Server, method, path, token string, keys []string, body any) (int, map[string]any, error) {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return 0, nil, err
		}
	}
	req, err := http.NewRequest(method, srv.URL+path, &buf)
	if err != nil {
		return 0, nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, k := range keys {
		req.Header.Add("Idempotency-Key", k)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

type draftRow struct {
	ID, WorkspaceID, CustomerID, CatalogItemID, ClientRequestID, Status, StartsAt, EndsAt, CreatedAt, UpdatedAt string
}

func readDraft(t *testing.T, boot *sql.DB, ws, id string) (draftRow, bool) {
	t.Helper()
	var d draftRow
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, customer_id::text, catalog_item_id::text, client_request_id, status,
			starts_at::text, ends_at::text, created_at::text, updated_at::text
		 FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&d.ID, &d.WorkspaceID, &d.CustomerID, &d.CatalogItemID, &d.ClientRequestID, &d.Status,
		&d.StartsAt, &d.EndsAt, &d.CreatedAt, &d.UpdatedAt)
	if err == sql.ErrNoRows {
		return draftRow{}, false
	}
	if err != nil {
		t.Fatalf("read draft %s: %v", id, err)
	}
	return d, true
}

// seedDraftRefs membuat pelanggan + barang lewat API (bukan insert
// istimewa) dan mengembalikan id keduanya.
func seedDraftRefs(t *testing.T, env *activationEnv, sku string) (custID, itemID string) {
	t.Helper()
	code, body := createCustomer(t, env.srv, env.wsA, env.ownerToken, map[string]string{"display_name": "Pelanggan Rental"})
	if code != http.StatusCreated {
		t.Fatalf("seed customer: want 201, got %d (%v)", code, body)
	}
	custID, _ = body["id"].(string)
	code, body = createCatalogItem(t, env.srv, env.wsA, env.ownerToken, map[string]string{"sku": sku, "name": "Barang Rental", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("seed item: want 201, got %d (%v)", code, body)
	}
	itemID, _ = body["id"].(string)
	return custID, itemID
}

// activateRental memberi entitlement sintetis lalu mengaktifkan
// rental.basic 0.1.0; dipakai semua tes happy-path draft.
func activateRental(t *testing.T, env *activationEnv) {
	t.Helper()
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi rental: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
}

func draftBody(cust, item, starts, ends string) map[string]string {
	return map[string]string{
		"customer_id": cust, "catalog_item_id": item,
		"starts_at": starts, "ends_at": ends,
	}
}

func archiveItem(t *testing.T, boot *sql.DB, itemID string) {
	t.Helper()
	if _, err := boot.Exec(`UPDATE catalog_items SET status = 'archived' WHERE id = $1`, itemID); err != nil {
		t.Fatalf("arsipkan item: %v", err)
	}
}

// installPermissionFaultPerm seperti installPermissionFault tetapi untuk
// izin apa pun (bukan hanya view_workspace). Pemulihan memakai
// restorePermissionFault yang sama.
func installPermissionFaultPerm(t *testing.T, boot *sql.DB, perm string) string {
	t.Helper()
	var def string
	if err := boot.QueryRow(`SELECT pg_get_functiondef('app.has_permission(UUID,UUID,TEXT)'::regprocedure)`).Scan(&def); err != nil {
		t.Fatalf("baca definisi has_permission: %v", err)
	}
	q := fmt.Sprintf(`CREATE OR REPLACE FUNCTION app.has_permission(ws UUID, subj UUID, perm TEXT)
		RETURNS BOOLEAN LANGUAGE plpgsql STABLE SECURITY DEFINER AS $$
		BEGIN
			IF perm = %s THEN
				RAISE EXCEPTION 'synthetic permission fault';
			END IF;
			RETURN app.is_member(ws, subj) AND EXISTS (
				SELECT 1 FROM membership_roles mr
				JOIN role_permissions rp ON rp.role = mr.role
				WHERE mr.workspace_id = ws AND mr.subject_id = subj
					AND rp.permission = perm
			);
		END;
		$$`, "'"+perm+"'")
	if _, err := boot.Exec(q); err != nil {
		t.Fatalf("pasang permission fault %s: %v", perm, err)
	}
	return def
}

// revokeEntitlementErr adalah varian revokeEntitlement yang mengembalikan
// error (bukan t.Fatalf) supaya aman dipanggil dari goroutine pada tes
// race create vs revoke.
func revokeEntitlementErr(boot *sql.DB, ws, moduleID string) error {
	tx, err := boot.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, ws).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1 AND module_id = $2`, ws, moduleID); err != nil {
		return err
	}
	return tx.Commit()
}

// rentalSnapshot menangkap data/kebijakan/ACL/constraint/marker untuk
// uji rerun 006 (pola masterDataSnapshot F2-01-A).
func rentalSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"contracts":   `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || customer_id::text || '|' || catalog_item_id::text || '|' || client_request_id || '|' || status || '|' || starts_at::text || '|' || ends_at::text || '|' || created_at::text || '|' || updated_at::text, chr(10) ORDER BY id), '') FROM rental_contracts`,
		"permissions": `SELECT coalesce(string_agg(role || ':' || permission, ',' ORDER BY role, permission), '') FROM role_permissions`,
		"policies":    `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename = 'rental_contracts'`,
		"acl":         `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname = 'rental_contracts'`,
		"markers":     `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
		"unique_idx":  `SELECT coalesce(string_agg(conname, ',' ORDER BY conname), '') FROM pg_constraint WHERE conname IN ('parties_workspace_id_id_key', 'catalog_items_workspace_id_id_key')`,
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

// TestRentalFreshSchema: migrasi 006 pada database segar — enam marker,
// tabel kosong, pemilik bootstrap (bukan runtime), RLS ENABLE+FORCE,
// hak efektif runtime (SELECT/INSERT saja), kebijakan, CHECK/UNIQUE/FK,
// dan UNIQUE (workspace_id, id) pada parties/catalog_items.
func TestRentalFreshSchema(t *testing.T) {
	boot := bootstrapDB(t)

	var markers string
	if err := boot.QueryRow(
		`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`,
	).Scan(&markers); err != nil {
		t.Fatalf("baca markers: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts, got %s", markers)
	}
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM rental_contracts`).Scan(&n); err != nil {
		t.Fatalf("count rental_contracts: %v", err)
	}
	if n != 0 {
		t.Fatalf("rental_contracts: want 0 baris pada skema segar, got %d", n)
	}
	var owner string
	var super, rls, force bool
	if err := boot.QueryRow(
		`SELECT c.relowner::regrole::text, r.rolsuper, c.relrowsecurity, c.relforcerowsecurity
		 FROM pg_class c JOIN pg_roles r ON r.rolname = c.relowner::regrole::text
		 WHERE c.relname = 'rental_contracts'`,
	).Scan(&owner, &super, &rls, &force); err != nil {
		t.Fatalf("owner/rls rental_contracts: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("rental_contracts dimiliki runtime role; harus dimiliki bootstrap/migrasi")
	}
	if !super {
		t.Fatalf("pemilik rental_contracts (%q) harus superuser (bootstrap/migrasi), got super=%v", owner, super)
	}
	if !rls || !force {
		t.Fatalf("rental_contracts: RLS harus ENABLE+FORCE, got rls=%v force=%v", rls, force)
	}
	// Hak efektif runtime: SELECT/INSERT boleh; UPDATE/DELETE/TRUNCATE tidak.
	for _, p := range []struct {
		action string
		want   bool
	}{
		{"SELECT", true}, {"INSERT", true},
		{"UPDATE", false}, {"DELETE", false}, {"TRUNCATE", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', 'rental_contracts', $1)`, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege %s: %v", p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime rental_contracts %s: want %v, got %v", p.action, p.want, got)
		}
	}
	// Kebijakan: SELECT + INSERT terpisah, tepat dua.
	for _, want := range []struct{ policy, cmd string }{
		{"rental_contracts_select", "SELECT"},
		{"rental_contracts_insert", "INSERT"},
	} {
		var n int
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE tablename = 'rental_contracts' AND policyname = $1 AND cmd = $2`,
			want.policy, want.cmd).Scan(&n); err != nil {
			t.Fatalf("cek policy %s: %v", want.policy, err)
		}
		if n != 1 {
			t.Fatalf("policy %s: want 1, got %d", want.policy, n)
		}
	}
	if err := boot.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename = 'rental_contracts'`).Scan(&n); err != nil {
		t.Fatalf("count policies: %v", err)
	}
	if n != 2 {
		t.Fatalf("policies rental_contracts: want 2, got %d", n)
	}
	// CHECK 3 (status, client_request_id, ends_at>starts_at) + UNIQUE 1
	// (workspace_id, client_request_id) + FK 3 (workspace, komposit
	// customer, komposit item).
	var c, u, f int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f')
		 FROM pg_constraint WHERE conrelid = 'rental_contracts'::regclass`).Scan(&c, &u, &f); err != nil {
		t.Fatalf("constraints rental_contracts: %v", err)
	}
	if c != 3 || u != 1 || f != 3 {
		t.Fatalf("constraints rental_contracts: want CHECK 3 + UNIQUE 1 + FK 3, got CHECK %d + UNIQUE %d + FK %d", c, u, f)
	}
	// UNIQUE (workspace_id, id) pada kedua tabel core ada (target FK
	// komposit).
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_constraint WHERE conname IN ('parties_workspace_id_id_key', 'catalog_items_workspace_id_id_key') AND contype = 'u'`).Scan(&n); err != nil {
		t.Fatalf("count unique workspace_id_id: %v", err)
	}
	if n != 2 {
		t.Fatalf("UNIQUE (workspace_id, id) core: want 2, got %d", n)
	}
}

// TestRentalRerunPreservesData: rerun 006 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// kebijakan, ACL, constraint, dan marker.
func TestRentalRerunPreservesData(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("rerun-rental-owner-%d@contoh.id", suffix)
	tokenOwner := registerAndLogin(t, srv, ownerEmail)
	ws := createWorkspace(t, srv, tokenOwner, "Usaha Rerun F2-01-B")
	ownerID := subjectID(t, db, ownerEmail)

	// Cleanup didaftarkan SEBELUM fixture disisipkan: bila fixture
	// gagal (mis. CHECK migration rusak), t.Fatalf tetap memicu
	// cleanup dan tidak meninggalkan baris yatim.
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM rental_contracts WHERE workspace_id = $1`, ws)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE workspace_id = $1`, ws)
		_, _ = boot.Exec(`DELETE FROM parties WHERE workspace_id = $1`, ws)
	})

	var custID, itemID string
	if err := boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan Rerun') RETURNING id`, ws).Scan(&custID); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RERUN-006', 'Barang Rerun', 'pcs', 'active') RETURNING id`, ws).Scan(&itemID); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(
		`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
		 VALUES ($1, $2, $3, 'rerun-key-1', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`,
		ws, custID, itemID); err != nil {
		t.Fatalf("fixture draft: %v", err)
	}

	before := rentalSnapshot(t, boot)
	code, out := runPsqlFile(t, bootURL, "migrations/006_rental_contracts.sql")
	if code != 0 {
		t.Fatalf("rerun 006 tanpa -1: want exit 0, got %d (%s)", code, out)
	}
	after := rentalSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		for k := range before {
			if before[k] != after[k] {
				t.Errorf("rerun mengubah %s:\nsebelum: %q\nsesudah: %q", k, before[k], after[k])
			}
		}
		t.Fatalf("rerun 006 mengubah data/kebijakan/seed/marker")
	}
	var allowed bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, ownerID).Scan(&allowed); err != nil {
		t.Fatalf("has_permission sesudah rerun: %v", err)
	}
	if !allowed {
		t.Fatalf("has_permission owner sesudah rerun: want true, got false")
	}
}

// TestRentalRLSAndPermissions: matriks izin + RLS/FK lewat koneksi
// runtime yang sebenarnya (bukan koneksi istimewa). Owner/manager boleh
// INSERT di tenant-nya; kasir/nonmember tidak boleh mutasi; bacaan
// lintas-tenant dan tanpa konteks mengembalikan nol baris; FK komposit
// menolak tautan lintas-tenant (23503); CHECK menolak periode/kunci
// tidak valid (23514); kunci duplikat ditolak (23505); UPDATE/DELETE/
// TRUNCATE ditolak di level hak (42501).
func TestRentalRLSAndPermissions(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("rental-rls-owner-%d@contoh.id", suffix)
	mgrEmail := fmt.Sprintf("rental-rls-mgr-%d@contoh.id", suffix)
	cashEmail := fmt.Sprintf("rental-rls-cash-%d@contoh.id", suffix)
	otherEmail := fmt.Sprintf("rental-rls-lain-%d@contoh.id", suffix)
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
		code, body := doJSON(t, srv, "POST", "/workspaces/"+wsA+"/invites", tokenOwner, map[string]string{"email": tc.email, "role": tc.role})
		if code != http.StatusCreated {
			t.Fatalf("invite %s: want 201, got %d (%v)", tc.email, code, body)
		}
		inviteToken, _ := body["token"].(string)
		code, _ = doJSON(t, srv, "POST", "/invites/accept", tc.token, map[string]string{"workspace_id": wsA, "token": inviteToken})
		if code != http.StatusOK {
			t.Fatalf("accept %s: want 200, got %d", tc.email, code)
		}
	}
	ownerID := subjectID(t, db, ownerEmail)
	mgrID := subjectID(t, db, mgrEmail)
	cashID := subjectID(t, db, cashEmail)
	otherID := subjectID(t, db, otherEmail)

	// Cleanup didaftarkan SEBELUM fixture disisipkan (lihat rerun test:
	// t.Fatalf sebelum registrasi meninggalkan baris yatim).
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM rental_contracts WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		_, _ = boot.Exec(`DELETE FROM parties WHERE workspace_id IN ($1, $2)`, wsA, wsB)
	})

	// Rujukan fixture lewat bootstrap (bypass RLS): satu
	// pelanggan+barang per workspace.
	var custA, itemA, custB, itemB string
	if err := boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan A') RETURNING id`, wsA).Scan(&custA); err != nil {
		t.Fatalf("fixture party A: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-A', 'Barang A', 'pcs', 'active') RETURNING id`, wsA).Scan(&itemA); err != nil {
		t.Fatalf("fixture item A: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, wsB).Scan(&custB); err != nil {
		t.Fatalf("fixture party B: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-B', 'Barang B', 'pcs', 'active') RETURNING id`, wsB).Scan(&itemB); err != nil {
		t.Fatalf("fixture item B: %v", err)
	}

	// Matriks has_permission manage_business_data.
	for _, tc := range []struct {
		subj string
		want bool
	}{
		{ownerID, true}, {mgrID, true}, {cashID, false}, {otherID, false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, wsA, tc.subj).Scan(&got); err != nil {
			t.Fatalf("has_permission %s: %v", tc.subj, err)
		}
		if got != tc.want {
			t.Fatalf("manage_business_data %s: want %v, got %v", tc.subj, tc.want, got)
		}
	}
	// Bacaan langsung: tanpa konteks → 0; konteks A membaca B → 0.
	countIn := func(ws, subj, target string) int {
		t.Helper()
		tx := tenantTx(t, db, ws, subj)
		defer tx.Rollback()
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM rental_contracts WHERE workspace_id = $1`, target).Scan(&n); err != nil {
			t.Fatalf("count rental di %s: %v", target, err)
		}
		return n
	}
	if n := countIn(wsA, ownerID, wsA); n != 0 {
		t.Fatalf("baca A dari A: want 0 (tabel kosong), got %d", n)
	}
	if n := countIn(wsA, ownerID, wsB); n != 0 {
		t.Fatalf("baca B dari A: want 0, got %d", n)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM rental_contracts`).Scan(&n); err != nil {
		t.Fatalf("baca tanpa konteks: %v", err)
	}
	if n != 0 {
		t.Fatalf("baca tanpa konteks: want 0, got %d", n)
	}
	// INSERT langsung lewat tenant runtime.
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
	draftSQL := `INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`
	if code, err := tryInsert(wsA, ownerID, draftSQL, wsA, custA, itemA, "rls-key-owner"); err != nil {
		t.Fatalf("owner INSERT draft: want ok, got %s (%v)", code, err)
	}
	if countRows(t, boot, "rental_contracts", wsA) != 1 {
		t.Fatalf("owner INSERT draft: want 1 baris di A, got %d", countRows(t, boot, "rental_contracts", wsA))
	}
	if code, err := tryInsert(wsA, mgrID, draftSQL, wsA, custA, itemA, "rls-key-mgr"); err != nil {
		t.Fatalf("manager INSERT draft: want ok, got %s (%v)", code, err)
	}
	if countRows(t, boot, "rental_contracts", wsA) != 2 {
		t.Fatalf("manager INSERT draft: want 2 baris di A, got %d", countRows(t, boot, "rental_contracts", wsA))
	}
	for name, subj := range map[string]string{"cashier": cashID, "nonmember": otherID} {
		if code, err := tryInsert(wsA, subj, draftSQL, wsA, custA, itemA, "rls-key-"+name); err == nil || code != "42501" {
			t.Fatalf("%s INSERT draft: want 42501, got code %q err %v", name, code, err)
		}
	}
	// Pindah tenant lewat INSERT langsung ditolak (WITH CHECK): konteks
	// A, workspace_id B.
	if code, err := tryInsert(wsA, ownerID, draftSQL, wsB, custB, itemB, "rls-key-pindah"); err == nil || code != "42501" {
		t.Fatalf("INSERT lintas tenant: want 42501, got code %q err %v", code, err)
	}
	// FK komposit menolak tautan lintas-tenant: workspace A dengan
	// pelanggan/barang B (RLS lolos, FK menolak → 23503).
	if code, err := tryInsert(wsA, ownerID, draftSQL, wsA, custB, itemB, "rls-key-fk"); err == nil || code != "23503" {
		t.Fatalf("INSERT FK lintas tenant: want 23503, got code %q err %v", code, err)
	}
	// CHECK langsung: periode terbalik dan kunci tidak valid → 23514.
	for name, tc := range map[string]struct {
		q    string
		args []any
	}{
		"periode terbalik": {
			`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
				VALUES ($1, $2, $3, 'rls-key-periode', 'draft', '2026-11-12T02:00:00Z', '2026-11-10T02:00:00Z')`,
			[]any{wsA, custA, itemA},
		},
		"kunci berisi spasi": {
			`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
				VALUES ($1, $2, $3, 'kunci buruk', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`,
			[]any{wsA, custA, itemA},
		},
		"kunci kosong": {
			`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
				VALUES ($1, $2, $3, '', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`,
			[]any{wsA, custA, itemA},
		},
		"kunci 129 karakter": {
			`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
				VALUES ($1, $2, $3, $4, 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`,
			[]any{wsA, custA, itemA, strings.Repeat("k", 129)},
		},
	} {
		if code, err := tryInsert(wsA, ownerID, tc.q, tc.args...); err == nil || code != "23514" {
			t.Fatalf("%s: want 23514, got code %q err %v", name, code, err)
		}
	}
	// Kunci duplikat di workspace yang sama → 23505.
	if code, err := tryInsert(wsA, ownerID, draftSQL, wsA, custA, itemA, "rls-key-owner"); err == nil || code != "23505" {
		t.Fatalf("INSERT kunci duplikat: want 23505, got code %q err %v", code, err)
	}
	// Kunci yang sama di workspace lain boleh (unik per workspace).
	if code, err := tryInsert(wsB, otherID, draftSQL, wsB, custB, itemB, "rls-key-owner"); err != nil {
		t.Fatalf("INSERT kunci sama di B: want ok, got %s (%v)", code, err)
	}
	// UPDATE/DELETE/TRUNCATE ditolak di level hak (42501).
	for name, q := range map[string]string{
		"UPDATE": `UPDATE rental_contracts SET status = 'draft' WHERE workspace_id = $1`,
		"DELETE": `DELETE FROM rental_contracts WHERE workspace_id = $1`,
	} {
		tx := tenantTx(t, db, wsA, ownerID)
		_, err := tx.Exec(q, wsA)
		tx.Rollback()
		if err == nil || pgCode(err) != "42501" {
			t.Fatalf("%s rental_contracts: want 42501, got %v", name, err)
		}
	}
	{
		tx := tenantTx(t, db, wsA, ownerID)
		_, err := tx.Exec(`TRUNCATE rental_contracts`)
		tx.Rollback()
		if err == nil || pgCode(err) != "42501" {
			t.Fatalf("TRUNCATE rental_contracts: want 42501, got %v", err)
		}
	}
}

// TestRentalDraftHTTP: happy path draft lewat API nyata (customer/item
// dibuat lewat API, bukan insert istimewa), matriks guard/role/tenant,
// rujukan hilang/lintas-tenant/arip, dan matriks validasi 400.
func TestRentalDraftHTTP(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-DRAFT-HTTP")
	starts, ends := "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"
	body := draftBody(cust, item, starts, ends)

	// (a) Rental belum terinstal → create 403.
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "http-k1", body)
	if code != http.StatusForbidden {
		t.Fatalf("create tanpa instalasi: want 403, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "belum aktif")
	activateRental(t, env)

	// (b) Owner create → 201; timestamp ternormalisasi ke UTC.
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "http-k1", body)
	if code != http.StatusCreated {
		t.Fatalf("owner create: want 201, got %d (%v)", code, resp)
	}
	id, _ := resp["id"].(string)
	if id == "" {
		t.Fatalf("owner create: id kosong (%v)", resp)
	}
	for k, want := range map[string]any{
		"workspace_id": env.wsA, "customer_id": cust, "catalog_item_id": item,
		"client_request_id": "http-k1", "status": "draft",
		"starts_at": "2026-11-10T02:00:00Z", "ends_at": "2026-11-12T02:00:00Z",
	} {
		if resp[k] != want {
			t.Fatalf("owner create %s: want %v, got %v", k, want, resp[k])
		}
	}
	if resp["created_at"] == "" || resp["updated_at"] == "" {
		t.Fatalf("owner create: timestamp kosong (%v)", resp)
	}

	// (c) Manager create kunci berbeda (item+periode sama) → 201: tidak
	// ada perlindungan overlap pada slice draft.
	code, resp = createDraft(t, env.srv, env.wsA, env.mgrToken, "http-k2", body)
	if code != http.StatusCreated {
		t.Fatalf("manager create: want 201, got %d (%v)", code, resp)
	}

	// (d) Kasir create → 403, tetapi list histori → 200.
	code, resp = createDraft(t, env.srv, env.wsA, env.cashToken, "http-k3", body)
	if code != http.StatusForbidden {
		t.Fatalf("kasir create: want 403, got %d (%v)", code, resp)
	}
	code, resp = listDrafts(t, env.srv, env.wsA, env.cashToken, "")
	if code != http.StatusOK {
		t.Fatalf("kasir list: want 200, got %d (%v)", code, resp)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("kasir list: want 2 draft, got %d (%v)", len(items), resp)
	}

	// (e) List owner + pagination.
	code, resp = listDrafts(t, env.srv, env.wsA, env.ownerToken, "?limit=1")
	if code != http.StatusOK {
		t.Fatalf("list limit=1: want 200, got %d (%v)", code, resp)
	}
	items, _ = resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list limit=1: want 1 item, got %d", len(items))
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?offset=-1", "?limit=abc"} {
		code, resp = listDrafts(t, env.srv, env.wsA, env.ownerToken, q)
		if code != http.StatusBadRequest {
			t.Fatalf("list %s: want 400, got %d (%v)", q, code, resp)
		}
	}

	// (f) Unauth 401; nonmember create/list 403.
	code, resp = createDraft(t, env.srv, env.wsA, "", "http-k4", body)
	if code != http.StatusUnauthorized {
		t.Fatalf("unauth create: want 401, got %d (%v)", code, resp)
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.otherToken, "http-k4", body)
	if code != http.StatusForbidden {
		t.Fatalf("nonmember create: want 403, got %d (%v)", code, resp)
	}
	code, resp = listDrafts(t, env.srv, env.wsA, env.otherToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("nonmember list: want 403, got %d (%v)", code, resp)
	}

	// (g) Rujukan lintas-tenant dan tidak dikenal → 404 dengan pesan
	// yang SAMA (tanpa membocorkan keberadaan).
	custB, itemB := func() (string, string) {
		code, b := createCustomer(t, env.srv, env.wsB, env.otherToken, map[string]string{"display_name": "Pelanggan B"})
		if code != http.StatusCreated {
			t.Fatalf("seed customer B: want 201, got %d (%v)", code, b)
		}
		c, _ := b["id"].(string)
		code, b = createCatalogItem(t, env.srv, env.wsB, env.otherToken, map[string]string{"sku": "SKU-DRAFT-B", "name": "Barang B", "unit": "pcs"})
		if code != http.StatusCreated {
			t.Fatalf("seed item B: want 201, got %d (%v)", code, b)
		}
		i, _ := b["id"].(string)
		return c, i
	}()
	unknownCust, _ := uuidNew()
	unknownItem, _ := uuidNew()
	for _, tc := range []struct {
		name, key string
		body      map[string]string
	}{
		{"pelanggan lintas tenant", "http-k5a", draftBody(custB, item, starts, ends)},
		{"pelanggan tidak dikenal", "http-k5b", draftBody(unknownCust, item, starts, ends)},
	} {
		code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, tc.key, tc.body)
		if code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d (%v)", tc.name, code, resp)
		}
		assertFlatError(t, resp, "pelanggan tidak ditemukan")
	}
	for _, tc := range []struct {
		name, key string
		body      map[string]string
	}{
		{"barang lintas tenant", "http-k6a", draftBody(cust, itemB, starts, ends)},
		{"barang tidak dikenal", "http-k6b", draftBody(cust, unknownItem, starts, ends)},
	} {
		code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, tc.key, tc.body)
		if code != http.StatusNotFound {
			t.Fatalf("%s: want 404, got %d (%v)", tc.name, code, resp)
		}
		assertFlatError(t, resp, "barang tidak ditemukan")
	}

	// (h) Barang milik sendiri yang diarsipkan → 409 (bukan 404).
	archiveItem(t, env.boot, item)
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "http-k7", body)
	if code != http.StatusConflict {
		t.Fatalf("item arsip: want 409, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "barang tidak aktif")

	// (i) Matriks validasi → semua 400.
	badBodies := []struct {
		name string
		keys []string
		body any
	}{
		{"tanpa kunci", nil, body},
		{"kunci ganda", []string{"val-dup", "val-dup"}, body},
		{"kunci kosong", []string{""}, body},
		{"kunci berisi spasi", []string{"kunci buruk"}, body},
		{"kunci berisi slash", []string{"a/b"}, body},
		{"kunci 129 karakter", []string{strings.Repeat("k", 129)}, body},
		{"field tak dikenal", []string{"val-1"}, map[string]any{"customer_id": cust, "catalog_item_id": item, "starts_at": starts, "ends_at": ends, "catatan": "x"}},
		{"customer_id angka", []string{"val-2"}, map[string]any{"customer_id": 123, "catalog_item_id": item, "starts_at": starts, "ends_at": ends}},
		{"customer_id hilang", []string{"val-3"}, map[string]string{"catalog_item_id": item, "starts_at": starts, "ends_at": ends}},
		{"customer_id bukan uuid", []string{"val-4"}, draftBody("bukan-uuid", item, starts, ends)},
		{"starts tanpa zona", []string{"val-5"}, draftBody(cust, item, "2026-11-10T09:00:00", ends)},
		{"starts malformed", []string{"val-6"}, draftBody(cust, item, "kemarin", ends)},
		{"ends sama dengan starts", []string{"val-7"}, draftBody(cust, item, starts, starts)},
		{"ends sebelum starts", []string{"val-8"}, draftBody(cust, item, ends, starts)},
		{"tahun 0000", []string{"val-9"}, draftBody(cust, item, "0000-01-01T00:00:00Z", ends)},
		{"tahun 10000", []string{"val-10"}, draftBody(cust, item, "10000-01-01T00:00:00Z", ends)},
	}
	for _, tc := range badBodies {
		code, resp = doDraftReq(t, env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts", env.ownerToken, tc.keys, tc.body)
		if code != http.StatusBadRequest {
			t.Fatalf("%s: want 400, got %d (%v)", tc.name, code, resp)
		}
	}
	// JSON trailing dan body melebihi 16 KiB → 400.
	validJSON := `{"customer_id":"` + cust + `","catalog_item_id":"` + item + `","starts_at":"` + starts + `","ends_at":"` + ends + `"}`
	code, resp = createDraftRaw(t, env.srv, env.wsA, env.ownerToken, []string{"val-raw-1"}, validJSON+` {"x":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("JSON trailing: want 400, got %d (%v)", code, resp)
	}
	code, resp = createDraftRaw(t, env.srv, env.wsA, env.ownerToken, []string{"val-raw-2"},
		`{"customer_id":"`+strings.Repeat("x", 17*1024)+`","catalog_item_id":"`+item+`","starts_at":"`+starts+`","ends_at":"`+ends+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("body oversized: want 400, got %d (%v)", code, resp)
	}
}

// TestRentalIdempotency: kunci sama + body sama → 201 lalu 200 baris
// yang sama (satu baris + satu audit); kunci sama + body berbeda
// ternormalisasi → 409; representasi zona waktu yang setara dan
// pemotongan nanodetik ke mikrodetik ikut replay 200; replay sesudah
// item diarsipkan tetap 200 selama hak masih berlaku; replay sesudah
// revoke → 403, dan pulih sesudah grant ulang.
func TestRentalIdempotency(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-IDEM")
	activateRental(t, env)
	starts, ends := "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"
	body := draftBody(cust, item, starts, ends)

	code, first := createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1", body)
	if code != http.StatusCreated {
		t.Fatalf("create pertama: want 201, got %d (%v)", code, first)
	}
	id, _ := first["id"].(string)
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 1 {
		t.Fatalf("sesudah create pertama: want 1 baris + 1 audit")
	}
	before, ok := readDraft(t, env.boot, env.wsA, id)
	if !ok {
		t.Fatalf("draft %s tidak terbaca", id)
	}

	// Replay body sama → 200, body identik, tanpa baris/audit baru.
	code, replay := createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1", body)
	if code != http.StatusOK {
		t.Fatalf("replay sama: want 200, got %d (%v)", code, replay)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay sama: body berbeda:\npertama: %v\nreplay: %v", first, replay)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 1 {
		t.Fatalf("sesudah replay: want tetap 1 baris + 1 audit")
	}
	after, _ := readDraft(t, env.boot, env.wsA, id)
	if before != after {
		t.Fatalf("replay mengubah baris:\nsebelum: %+v\nsesudah: %+v", before, after)
	}

	// Kunci sama + body berbeda ternormalisasi → 409, tanpa baris baru.
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1",
		draftBody(cust, item, starts, "2026-11-13T09:00:00+07:00"))
	if code != http.StatusConflict {
		t.Fatalf("replay beda body: want 409, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 {
		t.Fatalf("replay beda body: want tetap 1 baris")
	}

	// Representasi zona waktu setara dari instant yang sama → 200.
	code, _ = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-2",
		draftBody(cust, item, "2026-12-01T09:00:00+07:00", "2026-12-03T09:00:00+07:00"))
	if code != http.StatusCreated {
		t.Fatalf("create idem-2: want 201, got %d", code)
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-2",
		draftBody(cust, item, "2026-12-01T02:00:00Z", "2026-12-03T02:00:00Z"))
	if code != http.StatusOK {
		t.Fatalf("replay zona setara: want 200, got %d (%v)", code, resp)
	}

	// Nanodetik dipotong ke mikrodetik sebelum perbandingan: replay
	// dengan .123456Z atas create .123456789Z → 200.
	code, first3 := createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-3",
		draftBody(cust, item, "2026-12-10T09:00:00.123456789+07:00", "2026-12-12T09:00:00.123456789+07:00"))
	if code != http.StatusCreated {
		t.Fatalf("create idem-3: want 201, got %d (%v)", code, first3)
	}
	if first3["starts_at"] != "2026-12-10T02:00:00.123456Z" {
		t.Fatalf("create idem-3 starts_at: want 2026-12-10T02:00:00.123456Z, got %v", first3["starts_at"])
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-3",
		draftBody(cust, item, "2026-12-10T02:00:00.123456Z", "2026-12-12T02:00:00.123456Z"))
	if code != http.StatusOK {
		t.Fatalf("replay mikrodetik: want 200, got %d (%v)", code, resp)
	}

	// Replay sesudah item diarsipkan → 200 (replay tidak memvalidasi
	// ulang status referensi).
	archiveItem(t, env.boot, item)
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1", body)
	if code != http.StatusOK {
		t.Fatalf("replay sesudah arsip: want 200, got %d (%v)", code, resp)
	}

	// Replay sesudah revoke → 403 (guard berjalan sebelum replay).
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1", body)
	if code != http.StatusForbidden {
		t.Fatalf("replay sesudah revoke: want 403, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 3 {
		t.Fatalf("replay sesudah revoke: want tetap 3 baris, got %d", countRows(t, env.boot, "rental_contracts", env.wsA))
	}
	// Grant ulang → replay 200 lagi.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "idem-1", body)
	if code != http.StatusOK {
		t.Fatalf("replay sesudah grant ulang: want 200, got %d (%v)", code, resp)
	}
}

// TestRentalGuardFaults: setiap query guard yang gagal → 500
// terkendali tanpa draft/audit; sesudah fault dipulihkan, create 201.
func TestRentalGuardFaults(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-GUARD-FAULT")
	activateRental(t, env)
	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	// Fault query izin tulis (manage_business_data) → 500.
	def := installPermissionFaultPerm(t, env.boot, "manage_business_data")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "fault-1", body)
	if code != http.StatusInternalServerError || resp["error"] != "izin gagal diperiksa" {
		t.Fatalf("fault manage_business_data: want 500 izin gagal diperiksa, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 0 {
		t.Fatalf("fault manage_business_data: want 0 baris + 0 audit")
	}
	restorePermissionFault(t, env.boot, def)

	// Fault query izin baca (view_workspace) → 500.
	def = installPermissionFault(t, env.boot)
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "fault-2", body)
	if code != http.StatusInternalServerError || resp["error"] != "izin gagal diperiksa" {
		t.Fatalf("fault view_workspace: want 500 izin gagal diperiksa, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 0 {
		t.Fatalf("fault view_workspace: want 0 baris")
	}
	restorePermissionFault(t, env.boot, def)

	// Fault bacaan instalasi/entitlement/referensi lewat REVOKE SELECT
	// (pola F2-01-A): tiap fault → 500, lalu GRANT kembali. Cleanup
	// mengembalikan semua GRANT bila tes gagal di tengah jalan.
	t.Cleanup(func() {
		for _, table := range []string{"module_installations", "module_entitlements", "parties", "catalog_items"} {
			_, _ = env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`)
		}
	})
	for name, table := range map[string]string{
		"instalasi":   "module_installations",
		"entitlement": "module_entitlements",
		"referensi":   "parties",
		"barang":      "catalog_items",
	} {
		if _, err := env.boot.Exec(`REVOKE SELECT ON ` + table + ` FROM umnia_business`); err != nil {
			t.Fatalf("revoke SELECT %s: %v", table, err)
		}
		code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "fault-"+name, body)
		if code != http.StatusInternalServerError || resp["error"] != "transaksi gagal" {
			t.Fatalf("fault %s: want 500 transaksi gagal, got %d (%v)", name, code, resp)
		}
		if countRows(t, env.boot, "rental_contracts", env.wsA) != 0 {
			t.Fatalf("fault %s: want 0 baris", name)
		}
		if _, err := env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`); err != nil {
			t.Fatalf("grant kembali SELECT %s: %v", table, err)
		}
	}

	// Sesudah semua fault dipulihkan, create yang sama berhasil 201.
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "fault-ok", body)
	if code != http.StatusCreated {
		t.Fatalf("create sesudah pemulihan: want 201, got %d (%v)", code, resp)
	}
}

// TestRentalAuditFaultAtomicity: gagal tulis audit lewat handler → 500,
// tanpa draft dan tanpa audit; sesudah fault dicabut, create 201.
// Detail audit tidak menyalin nama/body/kunci.
func TestRentalAuditFaultAtomicity(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-AUDIT-FAULT")
	activateRental(t, env)
	boot := env.boot
	dropFault := func() {
		_, _ = boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_rental_audit ON audit_events`)
		_, _ = boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_rental_audit()`)
	}
	t.Cleanup(dropFault)
	dropFault() // bersihkan sisa run sebelumnya yang gagal di tengah jalan
	if _, err := boot.Exec(`CREATE OR REPLACE FUNCTION app.qa_fail_rental_audit()
		RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'rental_contract_created' THEN
				RAISE EXCEPTION 'synthetic rental audit fault';
			END IF;
			RETURN NEW;
		END;
		$$`); err != nil {
		t.Fatalf("buat fungsi fault audit: %v", err)
	}
	if _, err := boot.Exec(`CREATE TRIGGER qa_fault_rental_audit BEFORE INSERT ON audit_events
		FOR EACH ROW EXECUTE FUNCTION app.qa_fail_rental_audit()`); err != nil {
		t.Fatalf("buat trigger fault audit: %v", err)
	}

	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "audit-1", body)
	if code != http.StatusInternalServerError || resp["error"] != "audit gagal ditulis" {
		t.Fatalf("create dengan fault audit: want 500 audit gagal ditulis, got %d (%v)", code, resp)
	}
	if countRows(t, boot, "rental_contracts", env.wsA) != 0 ||
		countBusinessAudits(t, boot, env.wsA, "rental_contract_created") != 0 {
		t.Fatalf("fault audit: want 0 baris + 0 audit")
	}
	dropFault()
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "audit-1", body)
	if code != http.StatusCreated {
		t.Fatalf("create sesudah fault dicabut: want 201, got %d (%v)", code, resp)
	}
	id, _ := resp["id"].(string)
	// Detail audit hanya identifier, tanpa nama/kunci/body.
	var details string
	if err := boot.QueryRow(
		`SELECT details::text FROM audit_events WHERE workspace_id = $1 AND action = 'rental_contract_created'`, env.wsA).Scan(&details); err != nil {
		t.Fatalf("baca detail audit: %v", err)
	}
	if !strings.Contains(details, cust) || !strings.Contains(details, item) {
		t.Fatalf("detail audit: want memuat customer_id + catalog_item_id, got %s", details)
	}
	if strings.Contains(details, "Pelanggan") || strings.Contains(details, "audit-1") || strings.Contains(details, id) {
		t.Fatalf("detail audit: tidak boleh memuat nama/kunci/id kontrak, got %s", details)
	}
}

// TestRentalConcurrentSameKey: dua create paralel dengan kunci + body
// yang sama → satu 201 + satu 200 baris yang sama, tepat satu baris +
// satu audit (bukan dua draft).
func TestRentalConcurrentSameKey(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-RACE-KEY")
	activateRental(t, env)
	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	path := "/workspaces/" + env.wsA + "/rental/contracts"
	call := func() (int, map[string]any, error) {
		return callDraftAPI(env.srv, "POST", path, env.ownerToken, []string{"race-key-1"}, body)
	}
	codes, bodies := parallel(t, call, call)
	sort.Ints(codes)
	if codes[0] != 200 || codes[1] != 201 {
		t.Fatalf("race kunci sama: want kode [200, 201], got %v", codes)
	}
	if bodies[0]["id"] != bodies[1]["id"] {
		t.Fatalf("race kunci sama: id berbeda (%v vs %v)", bodies[0]["id"], bodies[1]["id"])
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 1 {
		t.Fatalf("race kunci sama: want tepat 1 baris + 1 audit")
	}
}

// TestRentalCreateVsDeactivateRace: create vs deactivate paralel —
// lock baris workspace menserialisasi keduanya. Hasil yang sah hanya
// dua: (create 201 + deactivate 200 + draft tercatat) ATAU (create 403
// + deactivate 200 + tanpa draft). Deactivate selalu 200; tidak pernah
// ada draft setengah tertulis, dan tidak pernah ada draft yang dibuat
// sesudah deaktivasi commit.
func TestRentalCreateVsDeactivateRace(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-RACE-DEACT")
	activateRental(t, env)
	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callDraftAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts", env.ownerToken, []string{"race-deact-1"}, body)
		},
		func() (int, map[string]any, error) {
			return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/rental.basic/deactivate", env.ownerToken, nil)
		},
	)
	createCode, deactCode := codes[0], codes[1]
	if deactCode != http.StatusOK {
		t.Fatalf("race create vs deactivate: deactivate want 200, got %d", deactCode)
	}
	inst, ok := readInstallation(t, env.boot, env.wsA, "rental.basic")
	if !ok || inst.Status != "inactive" {
		t.Fatalf("race create vs deactivate: instalasi harus inactive sesudahnya, got %+v", inst)
	}
	rows := countRows(t, env.boot, "rental_contracts", env.wsA)
	audits := countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created")
	switch {
	case createCode == http.StatusCreated && rows == 1 && audits == 1:
		// Create commit dulu, deactivate sesudahnya: draft tercatat
		// sebagai histori yang valid.
	case createCode == http.StatusForbidden && rows == 0 && audits == 0:
		// Deactivate commit dulu: create melihat instalasi nonaktif.
	default:
		t.Fatalf("race create vs deactivate: hasil tak konsisten: create=%d rows=%d audits=%d", createCode, rows, audits)
	}
	// Draft yang tercatat (bila ada) tetap terbaca sebagai histori.
	code, resp := listDrafts(t, env.srv, env.wsA, env.ownerToken, "")
	if code != http.StatusOK {
		t.Fatalf("list sesudah race: want 200, got %d (%v)", code, resp)
	}
	items, _ := resp["items"].([]any)
	if len(items) != rows {
		t.Fatalf("list sesudah race: want %d draft, got %d", rows, len(items))
	}
}

// TestRentalCreateVsRevokeRace: create vs pencabutan entitlement
// (kooperatif, lewat bootstrap dengan lock yang sama) paralel. Hasil
// yang sah hanya dua: (create 201 + draft tercatat) ATAU (create 403 +
// tanpa draft). Tidak pernah ada draft baru sesudah revoke commit.
func TestRentalCreateVsRevokeRace(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-RACE-REVOKE")
	activateRental(t, env)
	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callDraftAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts", env.ownerToken, []string{"race-revoke-1"}, body)
		},
		func() (int, map[string]any, error) {
			if err := revokeEntitlementErr(env.boot, env.wsA, "rental.basic"); err != nil {
				return 0, nil, err
			}
			return http.StatusOK, nil, nil
		},
	)
	createCode := codes[0]
	rows := countRows(t, env.boot, "rental_contracts", env.wsA)
	audits := countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created")
	switch {
	case createCode == http.StatusCreated && rows == 1 && audits == 1:
		// Create commit dulu; revoke sesudahnya tidak menghapus draft.
	case createCode == http.StatusForbidden && rows == 0 && audits == 0:
		// Revoke commit dulu: create ditolak guard entitlement.
	default:
		t.Fatalf("race create vs revoke: hasil tak konsisten: create=%d rows=%d audits=%d", createCode, rows, audits)
	}
	// Sesudah race, entitlement sudah dicabut: create baru apa pun 403.
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "race-revoke-2", body)
	if code != http.StatusForbidden {
		t.Fatalf("create sesudah revoke: want 403, got %d (%v)", code, resp)
	}
}

// TestRentalHistoryAfterDeactivateRevoke: draft yang sudah tercatat
// tetap terbaca (owner dan kasir) sesudah deaktivasi maupun pencabutan;
// create baru sesudah revoke 403; tidak ada penghapusan.
func TestRentalHistoryAfterDeactivateRevoke(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-HISTORY")
	activateRental(t, env)
	body := draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "hist-1", body)
	if code != http.StatusCreated {
		t.Fatalf("create draft: want 201, got %d (%v)", code, resp)
	}

	code, resp = deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, resp)
	}
	for name, token := range map[string]string{"owner": env.ownerToken, "kasir": env.cashToken} {
		code, resp = listDrafts(t, env.srv, env.wsA, token, "")
		if code != http.StatusOK {
			t.Fatalf("list %s sesudah deactivate: want 200, got %d (%v)", name, code, resp)
		}
		items, _ := resp["items"].([]any)
		if len(items) != 1 {
			t.Fatalf("list %s sesudah deactivate: want 1 draft, got %d", name, len(items))
		}
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "hist-2", body)
	if code != http.StatusForbidden {
		t.Fatalf("create sesudah deactivate: want 403, got %d (%v)", code, resp)
	}

	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	code, resp = listDrafts(t, env.srv, env.wsA, env.ownerToken, "")
	if code != http.StatusOK {
		t.Fatalf("list sesudah revoke: want 200, got %d (%v)", code, resp)
	}
	items, _ := resp["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("list sesudah revoke: want 1 draft, got %d", len(items))
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "hist-3", body)
	if code != http.StatusForbidden {
		t.Fatalf("create sesudah revoke: want 403, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 {
		t.Fatalf("histori: want tetap 1 draft, tanpa penghapusan")
	}
}

// TestRentalActivationNoOpAfterRevoke: perilaku B3 dipertahankan —
// aktivasi ulang versi sama yang sudah aktif sesudah revoke tetap 200
// snapshot intent (tanpa audit baru); aktivasi dari inactive sesudah
// revoke 403; draft baru sesudah revoke 403.
func TestRentalActivationNoOpAfterRevoke(t *testing.T) {
	env := setupRental(t)
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi rental: want 200, got %d (%v)", code, body)
	}
	before := readModuleAudits(t, env.boot, env.wsA)
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")

	// No-op aktif versi sama sesudah revoke: 200, tanpa audit baru.
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("no-op sesudah revoke: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
	if after := readModuleAudits(t, env.boot, env.wsA); len(after) != len(before) {
		t.Fatalf("no-op sesudah revoke: want audit tetap %d, got %d", len(before), len(after))
	}

	// Dari inactive sesudah revoke: 403.
	code, body = deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, body)
	}
	code, body = activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("aktivasi dari inactive sesudah revoke: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "belum menjadi hak")

	// Draft baru sesudah revoke: 403.
	cust, item := seedDraftRefs(t, env, "SKU-NOOP-REVOKE")
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "noop-1",
		draftBody(cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusForbidden {
		t.Fatalf("draft sesudah revoke: want 403, got %d (%v)", code, resp)
	}
}

// TestRentalIdempotencyUppercaseUUIDs: UUID referensi huruf besar adalah
// identitas yang sama dengan ejaan huruf kecilnya (PostgreSQL menyimpan
// UUID dalam bentuk kanonis huruf kecil). Create pertama huruf besar →
// 201; retry body/kunci yang sama persis → 200 dengan respons, baris,
// timestamp, dan satu audit yang sama; ejaan campuran huruf besar/kecil
// dari UUID yang sama ikut replay 200; identitas UUID yang benar-benar
// berbeda dengan kunci sama tetap 409 tanpa tulis/audit. Kunci
// idempotency sendiri tetap case-sensitive: kunci yang hanya berbeda
// huruf besar/kecil adalah permintaan baru (201).
func TestRentalIdempotencyUppercaseUUIDs(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-UUID-CASE")
	activateRental(t, env)
	upperCust, upperItem := strings.ToUpper(cust), strings.ToUpper(item)
	body := draftBody(upperCust, upperItem, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	// Create pertama huruf besar → 201.
	code, first := createDraft(t, env.srv, env.wsA, env.ownerToken, "uuid-case-1", body)
	if code != http.StatusCreated {
		t.Fatalf("create huruf besar: want 201, got %d (%v)", code, first)
	}
	id, _ := first["id"].(string)
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 1 {
		t.Fatalf("sesudah create pertama: want 1 baris + 1 audit")
	}
	before, ok := readDraft(t, env.boot, env.wsA, id)
	if !ok {
		t.Fatalf("draft %s tidak terbaca", id)
	}

	// Retry body/kunci yang sama persis → 200, respons identik,
	// tanpa baris/audit baru.
	code, replay := createDraft(t, env.srv, env.wsA, env.ownerToken, "uuid-case-1", body)
	if code != http.StatusOK {
		t.Fatalf("replay huruf besar: want 200, got %d (%v)", code, replay)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay huruf besar: body berbeda:\npertama: %v\nreplay: %v", first, replay)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 1 {
		t.Fatalf("sesudah replay: want tetap 1 baris + 1 audit")
	}
	after, _ := readDraft(t, env.boot, env.wsA, id)
	if before != after {
		t.Fatalf("replay mengubah baris:\nsebelum: %+v\nsesudah: %+v", before, after)
	}
	// Identitas tersimpan/respons memakai bentuk kanonis huruf kecil.
	if first["customer_id"] != cust || first["catalog_item_id"] != item {
		t.Fatalf("respons kanonis: want customer_id %s item %s, got %v", cust, item, first)
	}

	// Ejaan campuran: create huruf kecil, retry huruf besar → 200.
	code, _ = createDraft(t, env.srv, env.wsA, env.ownerToken, "uuid-case-2",
		draftBody(cust, item, "2026-11-20T09:00:00+07:00", "2026-11-22T09:00:00+07:00"))
	if code != http.StatusCreated {
		t.Fatalf("create huruf kecil: want 201, got %d", code)
	}
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "uuid-case-2",
		draftBody(upperCust, upperItem, "2026-11-20T09:00:00+07:00", "2026-11-22T09:00:00+07:00"))
	if code != http.StatusOK {
		t.Fatalf("replay campuran: want 200, got %d (%v)", code, resp)
	}

	// Identitas UUID yang benar-benar berbeda, kunci sama → 409,
	// tanpa baris/audit baru.
	cust2, item2 := seedDraftRefs(t, env, "SKU-UUID-DIFF")
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "uuid-case-1",
		draftBody(cust2, item2, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusConflict {
		t.Fatalf("replay identitas beda: want 409, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 2 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 2 {
		t.Fatalf("sesudah replay beda: want tetap 2 baris + 2 audit")
	}

	// Kunci tetap case-sensitive: kunci yang hanya berbeda huruf
	// besar/kecil adalah permintaan baru → 201.
	code, _ = createDraft(t, env.srv, env.wsA, env.ownerToken, "UUID-CASE-1", body)
	if code != http.StatusCreated {
		t.Fatalf("kunci beda huruf: want 201, got %d", code)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 3 {
		t.Fatalf("sesudah kunci beda huruf: want 3 baris")
	}
}

// TestRentalNormalizedYearRange: tahun dicek SEBELUM normalisasi saja
// tidak cukup — konversi ke UTC bisa menggeser tahun di tepi 0001/9999
// (mis. 9999-12-31T23:30:00-02:00 menjadi 10000-01-01T01:30:00Z, tahun
// yang parser RFC3339 handler tidak bisa terima saat replay). Lintasan
// ke atas maupun ke bawah, di satu maupun kedua ujung, → 400 tanpa
// baris/audit; tanggal batas tahun yang tetap valid sesudah normalisasi
// tetap 201 dan replay 200 memakai tanggal yang dikembalikan.
func TestRentalNormalizedYearRange(t *testing.T) {
	env := setupRental(t)
	cust, item := seedDraftRefs(t, env, "SKU-YEAR-RANGE")
	activateRental(t, env)
	noRows := func(step string) {
		t.Helper()
		if countRows(t, env.boot, "rental_contracts", env.wsA) != 0 ||
			countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 0 {
			t.Fatalf("%s: want 0 baris + 0 audit", step)
		}
	}

	// Lintasan ke atas di kedua ujung (contoh reviewer) → 400.
	code, resp := createDraft(t, env.srv, env.wsA, env.ownerToken, "year-up",
		draftBody(cust, item, "9999-12-31T23:30:00-02:00", "9999-12-31T23:45:00-02:00"))
	if code != http.StatusBadRequest {
		t.Fatalf("lintasan atas: want 400, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "starts_at tidak valid")
	noRows("lintasan atas")

	// Lintasan ke bawah di kedua ujung → 400.
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-down",
		draftBody(cust, item, "0001-01-01T00:15:00+02:00", "0001-01-01T00:30:00+02:00"))
	if code != http.StatusBadRequest {
		t.Fatalf("lintasan bawah: want 400, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "starts_at tidak valid")
	noRows("lintasan bawah")

	// Hanya ujung akhir yang keluar rentang → 400 ends_at.
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-ends",
		draftBody(cust, item, "9999-12-30T23:30:00Z", "9999-12-31T23:45:00-02:00"))
	if code != http.StatusBadRequest {
		t.Fatalf("lintasan ends saja: want 400, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "ends_at tidak valid")
	noRows("lintasan ends saja")

	// Hanya ujung awal yang keluar rentang → 400 starts_at.
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-starts",
		draftBody(cust, item, "0001-01-01T00:15:00+02:00", "0001-01-02T00:30:00+02:00"))
	if code != http.StatusBadRequest {
		t.Fatalf("lintasan starts saja: want 400, got %d (%v)", code, resp)
	}
	assertFlatError(t, resp, "starts_at tidak valid")
	noRows("lintasan starts saja")

	// Batas tahun atas yang tetap valid sesudah normalisasi → 201,
	// replay 200 memakai tanggal yang dikembalikan.
	code, first := createDraft(t, env.srv, env.wsA, env.ownerToken, "year-ok-top",
		draftBody(cust, item, "9999-12-31T23:30:00Z", "9999-12-31T23:45:00Z"))
	if code != http.StatusCreated {
		t.Fatalf("batas atas valid: want 201, got %d (%v)", code, first)
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-ok-top",
		draftBody(cust, item, first["starts_at"].(string), first["ends_at"].(string)))
	if code != http.StatusOK {
		t.Fatalf("replay batas atas: want 200, got %d (%v)", code, resp)
	}

	// Batas tahun bawah yang tetap valid sesudah normalisasi → 201,
	// replay 200 memakai tanggal yang dikembalikan.
	code, first = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-ok-bottom",
		draftBody(cust, item, "0001-01-01T00:15:00Z", "0001-01-01T00:30:00Z"))
	if code != http.StatusCreated {
		t.Fatalf("batas bawah valid: want 201, got %d (%v)", code, first)
	}
	code, resp = createDraft(t, env.srv, env.wsA, env.ownerToken, "year-ok-bottom",
		draftBody(cust, item, first["starts_at"].(string), first["ends_at"].(string)))
	if code != http.StatusOK {
		t.Fatalf("replay batas bawah: want 200, got %d (%v)", code, resp)
	}
	if countRows(t, env.boot, "rental_contracts", env.wsA) != 2 ||
		countBusinessAudits(t, env.boot, env.wsA, "rental_contract_created") != 2 {
		t.Fatalf("sesudah batas valid: want 2 baris + 2 audit")
	}
}
