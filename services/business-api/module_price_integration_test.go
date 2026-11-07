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
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tes integrasi versi harga + quote F2-01-D (migrasi 008) terhadap
// PostgreSQL asli: skema segar + rerun, RLS/hak runtime/FK komposit/
// EXCLUDE, HTTP penjadwalan/list, guard no-backdate + replay,
// seleksi quote, aritmetika eksak (ceil 24 jam, overflow 422),
// konkurensi, dan fault guard/audit. Entitlement di sini HANYA
// fixture sintetis terisolasi lewat bootstrap (bukan grant nyata);
// tidak ada otoritas pemberian entitlement, billing, harga yang
// disepakati, handover, atau lifecycle yang diuji. Quote adalah
// komputasi tak mengikat yang tidak disimpan — bukan agreed amount,
// bukan snapshot harga, bukan pengikat harga.

// setupPrice memakai env aset (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris price_versions fixture DULU
// (LIFO: cleanup ini didaftarkan sesudah setupAsset, jadi berjalan
// sebelum penghapusan draft/parties/items — urutan FK-aman, karena
// price_versions merujuk catalog_items).
func setupPrice(t *testing.T) *activationEnv {
	t.Helper()
	env := setupAsset(t)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM price_versions WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

// dbCutoff membaca jam database (cutoff acuan tes). Jadwal dalam tes
// SELALU diturunkan dari cutoff ini dengan margin — bukan tanggal
// 2026 yang ditulis tetap — supaya tes tidak rapuh terhadap waktu
// berjalan.
func dbCutoff(t *testing.T, boot *sql.DB) time.Time {
	t.Helper()
	var c time.Time
	if err := boot.QueryRow(`SELECT clock_timestamp()`).Scan(&c); err != nil {
		t.Fatalf("baca clock_timestamp: %v", err)
	}
	return c
}

func priceBody(from, to time.Time, amount, currency string) map[string]any {
	return map[string]any{
		"valid_from":   from.UTC().Format(time.RFC3339Nano),
		"valid_to":     to.UTC().Format(time.RFC3339Nano),
		"amount_minor": amount,
		"currency":     currency,
	}
}

func createPrice(t *testing.T, srv *httptest.Server, ws, item, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/catalog/items/"+item+"/prices", token, []string{key}, body)
}

func createPriceRaw(t *testing.T, srv *httptest.Server, ws, item, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/catalog/items/"+item+"/prices", strings.NewReader(raw))
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

func listPrices(t *testing.T, srv *httptest.Server, ws, item, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/catalog/items/"+item+"/prices"+query, token, nil)
}

func getQuote(t *testing.T, srv *httptest.Server, ws, draft, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/contracts/"+draft+"/quote", token, nil)
}

// seedPrice membuat satu versi harga lewat API dan mengembalikan id-nya.
func seedPrice(t *testing.T, env *activationEnv, item, key string, from, to time.Time, amount, currency string) string {
	t.Helper()
	code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, key, priceBody(from, to, amount, currency))
	if code != http.StatusCreated {
		t.Fatalf("seed price %s: want 201, got %d (%v)", key, code, body)
	}
	id, _ := body["id"].(string)
	return id
}

type storedPrice struct {
	ID, WorkspaceID, CatalogItemID, PriceListID, ClientRequestID, ValidFrom, ValidTo, RecordedAt, AmountMinor, Currency string
}

func readPrice(t *testing.T, boot *sql.DB, ws, id string) (storedPrice, bool) {
	t.Helper()
	var p storedPrice
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, catalog_item_id::text, price_list_id, client_request_id,
			valid_from::text, valid_to::text, recorded_at::text, amount_minor::text, currency
		 FROM price_versions WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.CatalogItemID, &p.PriceListID, &p.ClientRequestID,
		&p.ValidFrom, &p.ValidTo, &p.RecordedAt, &p.AmountMinor, &p.Currency)
	if err == sql.ErrNoRows {
		return storedPrice{}, false
	}
	if err != nil {
		t.Fatalf("read price %s: %v", id, err)
	}
	return p, true
}

// insertPriceFixture menyisipkan versi harga LANGSUNG lewat bootstrap
// (bypass API dan aturan no-backdate). Ini fixture sintetis terisolasi
// HANYA untuk bukti seleksi historis / aritmetika rentang panjang —
// BUKAN bukti bahwa backdate atau koreksi diotorisasi lewat API.
// Jangan pernah mengklaim happy path harga masa lalu dibuat lewat
// API no-backdate dari fixture ini.
func insertPriceFixture(t *testing.T, boot *sql.DB, ws, item, key, from, to, amount, currency string) string {
	t.Helper()
	var id string
	err := boot.QueryRow(
		`INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
		 VALUES ($1, $2, 'standard', $3, $4, $5, $6, $7) RETURNING id`,
		ws, item, key, from, to, amount, currency).Scan(&id)
	if err != nil {
		t.Fatalf("fixture price %s: %v", key, err)
	}
	return id
}

// insertDraftFixture menyisipkan draft LANGSUNG lewat bootstrap
// (bypass guard API). Dipakai hanya untuk invarian tersimpan yang
// tidak bisa dibuat lewat API (mis. tahun 10000, di luar rentang
// yang didukung handler).
func insertDraftFixture(t *testing.T, boot *sql.DB, ws, cust, item, key, starts, ends string) string {
	t.Helper()
	var id string
	err := boot.QueryRow(
		`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at)
		 VALUES ($1, $2, $3, $4, 'draft', $5, $6) RETURNING id`,
		ws, cust, item, key, starts, ends).Scan(&id)
	if err != nil {
		t.Fatalf("fixture draft %s: %v", key, err)
	}
	return id
}

// priceSnapshot menangkap data/kebijakan/ACL/constraint/marker untuk
// uji rerun 008 (pola assetSnapshot F2-01-C).
func priceSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"prices":      `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || catalog_item_id::text || '|' || price_list_id || '|' || client_request_id || '|' || valid_from::text || '|' || valid_to::text || '|' || recorded_at::text || '|' || amount_minor::text || '|' || currency, chr(10) ORDER BY id), '') FROM price_versions`,
		"policies":    `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('price_versions')`,
		"acl":         `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('price_versions')`,
		"markers":     `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
		"constraints": `SELECT coalesce(string_agg(conrelid::regclass::text || ':' || conname || ':' || contype::text, chr(10) ORDER BY conrelid::regclass::text, conname), '') FROM pg_constraint WHERE conrelid IN ('price_versions'::regclass)`,
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

func TestPriceFreshSchema(t *testing.T) {
	boot := bootstrapDB(t)

	var markers string
	if err := boot.QueryRow(
		`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`,
	).Scan(&markers); err != nil {
		t.Fatalf("baca markers: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody, got %s", markers)
	}
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM price_versions`).Scan(&n); err != nil {
		t.Fatalf("count price_versions: %v", err)
	}
	if n != 0 {
		t.Fatalf("price_versions: want 0 baris pada skema segar, got %d", n)
	}
	var owner string
	var super, rls, force bool
	if err := boot.QueryRow(
		`SELECT c.relowner::regrole::text, r.rolsuper, c.relrowsecurity, c.relforcerowsecurity
		 FROM pg_class c JOIN pg_roles r ON r.rolname = c.relowner::regrole::text
		 WHERE c.relname = 'price_versions'`).Scan(&owner, &super, &rls, &force); err != nil {
		t.Fatalf("owner/rls price_versions: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("price_versions dimiliki runtime role; harus dimiliki bootstrap/migrasi")
	}
	if !super {
		t.Fatalf("pemilik price_versions (%q) harus superuser (bootstrap/migrasi), got super=%v", owner, super)
	}
	if !rls || !force {
		t.Fatalf("price_versions: RLS harus ENABLE+FORCE, got rls=%v force=%v", rls, force)
	}
	// Kolom TEPAT seperti tiket: recorded_at satu-satunya fakta
	// temporal — TIDAK ada created_at/updated_at.
	var cols string
	if err := boot.QueryRow(
		`SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'price_versions'`,
	).Scan(&cols); err != nil {
		t.Fatalf("baca kolom price_versions: %v", err)
	}
	if cols != "id,workspace_id,catalog_item_id,price_list_id,client_request_id,valid_from,valid_to,recorded_at,amount_minor,currency" {
		t.Fatalf("kolom price_versions: want id,workspace_id,catalog_item_id,price_list_id,client_request_id,valid_from,valid_to,recorded_at,amount_minor,currency, got %s", cols)
	}
	// Hak efektif runtime: SELECT/INSERT boleh; UPDATE/DELETE/
	// TRUNCATE tidak.
	for _, p := range []struct {
		action string
		want   bool
	}{
		{"SELECT", true}, {"INSERT", true},
		{"UPDATE", false}, {"DELETE", false}, {"TRUNCATE", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', 'price_versions', $1)`, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege price_versions %s: %v", p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime price_versions %s: want %v, got %v", p.action, p.want, got)
		}
	}
	// Kebijakan: SELECT + INSERT (tepat dua).
	for _, want := range []struct{ policy, cmd string }{
		{"price_versions_select", "SELECT"},
		{"price_versions_insert", "INSERT"},
	} {
		var m int
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE tablename = 'price_versions' AND policyname = $1 AND cmd = $2`,
			want.policy, want.cmd).Scan(&m); err != nil {
			t.Fatalf("cek policy %s: %v", want.policy, err)
		}
		if m != 1 {
			t.Fatalf("policy %s: want 1, got %d", want.policy, m)
		}
	}
	// CHECK 6 (price_list_id, client_request_id, valid_to>valid_from,
	// amount_minor>=0, currency, finite bounds) + UNIQUE 2
	// (client_request_id + workspace_id/id dari 009, untuk FK komposit
	// fakta harga disepakati) + FK 2 + EXCLUDE 1.
	var c, u, f, x int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f'), count(*) FILTER (WHERE contype = 'x')
		 FROM pg_constraint WHERE conrelid = 'price_versions'::regclass`).Scan(&c, &u, &f, &x); err != nil {
		t.Fatalf("constraints price_versions: %v", err)
	}
	if c != 6 || u != 2 || f != 2 || x != 1 {
		t.Fatalf("constraints price_versions: want CHECK 6 + UNIQUE 2 + FK 2 + EXCLUDE 1, got CHECK %d + UNIQUE %d + FK %d + EXCLUDE %d", c, u, f, x)
	}
	// CHECK finite bernama: kedua batas harus finite (isfinite).
	// Tanpa ini, 'infinity' lolos sebagai valid_to/valid_from meski
	// interval diklaim finite — temuan reviewer F2-01-D.
	var finiteDef string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'price_versions_finite_bounds' AND contype = 'c'`).Scan(&finiteDef); err != nil {
		t.Fatalf("cek CHECK finite bernama: %v", err)
	}
	if !strings.Contains(finiteDef, "isfinite(valid_from)") || !strings.Contains(finiteDef, "isfinite(valid_to)") {
		t.Fatalf("price_versions_finite_bounds: want isfinite(valid_from) AND isfinite(valid_to), got %s", finiteDef)
	}
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_constraint WHERE conname = 'price_versions_no_overlap' AND contype = 'x'`).Scan(&n); err != nil {
		t.Fatalf("cek EXCLUDE bernama: %v", err)
	}
	if n != 1 {
		t.Fatalf("price_versions_no_overlap: want 1, got %d", n)
	}
	// btree_gist terpasang (dipakai EXCLUDE atas kesetaraan UUID).
	if err := boot.QueryRow(`SELECT count(*) FROM pg_extension WHERE extname = 'btree_gist'`).Scan(&n); err != nil {
		t.Fatalf("cek btree_gist: %v", err)
	}
	if n != 1 {
		t.Fatalf("btree_gist: want 1, got %d", n)
	}
}

// TestPriceRerunPreservesData: rerun 008 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// kebijakan, ACL, constraint, dan marker.
func TestPriceRerunPreservesData(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}

	// Cleanup didaftarkan SEBELUM fixture disisipkan (pola rerun
	// F2-01-B: t.Fatalf sebelum registrasi meninggalkan baris yatim).
	ws := "22222222-3333-4444-5555-666666666666"
	cust := "22222222-3333-4444-5555-666666666667"
	item := "22222222-3333-4444-5555-666666666668"
	price := "22222222-3333-4444-5555-666666666669"
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM price_versions WHERE id = $1`, price)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE id = $1`, item)
		_, _ = boot.Exec(`DELETE FROM parties WHERE id = $1`, cust)
	})
	// Workspace fixture memakai workspace nyata pertama dari env
	// aktivasi (bukan UUID sintetis): ambil dari setupPrice.
	env := setupPrice(t)
	ws = env.wsA
	if _, err := boot.Exec(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Rerun')`, cust, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-RERUN-8', 'Barang Rerun', 'pcs', 'active')`, item, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		VALUES ($1, $2, $3, 'standard', 'rerun-8', '2026-11-10T02:00:00Z', '2026-12-10T02:00:00Z', '2026-11-07T02:00:00Z', 150000, 'IDR')`, price, ws, item); err != nil {
		t.Fatalf("fixture price: %v", err)
	}
	before := priceSnapshot(t, boot)
	if !strings.Contains(before["prices"], "rerun-8") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture harga: %q", before["prices"])
	}

	code, out := runPsqlFile(t, bootURL, "migrations/008_price_versions.sql")
	if code != 0 {
		t.Fatalf("rerun 008: want exit 0, got %d (%s)", code, out)
	}
	after := priceSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun mengubah snapshot:\nsebelum: %v\nsesudah: %v", before, after)
	}
	// has_permission tetap berfungsi sesudah rerun.
	var ok bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, env.ownerID).Scan(&ok); err != nil || !ok {
		t.Fatalf("has_permission sesudah rerun: want true, got %v (%v)", ok, err)
	}
}

// TestPriceRLSAndPermissions: matriks izin + RLS/FK/EXCLUDE/CHECK
// lewat koneksi runtime yang sebenarnya (bukan koneksi istimewa).
func TestPriceRLSAndPermissions(t *testing.T) {
	env := setupPrice(t)
	db := testDB(t)
	boot := env.boot
	wsA, wsB := env.wsA, env.wsB

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

	// Matriks has_permission manage_business_data + view_workspace.
	for _, tc := range []struct {
		subj, perm string
		want       bool
	}{
		{env.ownerID, "manage_business_data", true}, {env.mgrID, "manage_business_data", true},
		{env.cashID, "manage_business_data", false}, {env.otherID, "manage_business_data", false},
		{env.ownerID, "view_workspace", true}, {env.mgrID, "view_workspace", true},
		{env.cashID, "view_workspace", true}, {env.otherID, "view_workspace", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT app.has_permission($1, $2, $3)`, wsA, tc.subj, tc.perm).Scan(&got); err != nil {
			t.Fatalf("has_permission %s %s: %v", tc.subj, tc.perm, err)
		}
		if got != tc.want {
			t.Fatalf("%s %s: want %v, got %v", tc.perm, tc.subj, tc.want, got)
		}
	}
	// Bacaan langsung: tanpa konteks → 0; konteks A membaca B → 0.
	if n := countRows(t, boot, "price_versions", wsA); n != 0 {
		t.Fatalf("price_versions A sebelum insert: want 0, got %d", n)
	}
	priceSQL := `INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
		VALUES ($1, $2, 'standard', $3, $4, $5, 150000, 'IDR')`
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
	// INSERT langsung lewat tenant runtime: owner/manager boleh.
	// Interval sengaja bersebelahan (bukan tumpang tindih) supaya
	// EXCLUDE tidak ikut menyala pada insert yang sah.
	if code, err := tryInsert(wsA, env.ownerID, priceSQL, wsA, itemA, "rls-price-owner", "2026-11-10T02:00:00Z", "2026-12-10T02:00:00Z"); err != nil {
		t.Fatalf("owner INSERT harga: want ok, got %s (%v)", code, err)
	}
	if code, err := tryInsert(wsA, env.mgrID, priceSQL, wsA, itemA, "rls-price-mgr", "2026-12-10T02:00:00Z", "2027-01-10T02:00:00Z"); err != nil {
		t.Fatalf("manager INSERT harga: want ok, got %s (%v)", code, err)
	}
	// Kasir/nonmember ditolak RLS (42501), bukan dibiarkan 0 baris.
	for name, subj := range map[string]string{"cashier": env.cashID, "nonmember": env.otherID} {
		if code, err := tryInsert(wsA, subj, priceSQL, wsA, itemA, "rls-price-"+name, "2026-11-10T02:00:00Z", "2026-12-10T02:00:00Z"); err == nil || code != "42501" {
			t.Fatalf("%s INSERT harga: want 42501, got code %q err %v", name, code, err)
		}
	}
	// FK komposit menolak item lintas-tenant (RLS lolos, FK 23503).
	if code, err := tryInsert(wsA, env.ownerID, priceSQL, wsA, itemB, "rls-price-fk", "2026-11-10T02:00:00Z", "2026-12-10T02:00:00Z"); err == nil || code != "23503" {
		t.Fatalf("INSERT harga FK lintas tenant: want 23503, got code %q err %v", code, err)
	}
	// CHECK: amount negatif, currency bukan [A-Z]{3}, price_list
	// bukan 'standard', kunci berkoma/spasi, valid_to <= valid_from,
	// batas infinity → 23514. (CHECK dievaluasi sebelum
	// UNIQUE/EXCLUDE, jadi interval boleh apa saja di sini; interval
	// infinity sengaja tidak overlap fixture supaya hanya CHECK
	// finite yang bisa menolak.)
	type checkCase struct {
		name      string
		list, key string
		from, to  string
		amount    int64
		currency  string
	}
	for _, tc := range []checkCase{
		{"amount negatif", "standard", "chk-neg", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", -1, "IDR"},
		{"currency huruf kecil", "standard", "chk-cur-lo", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "idr"},
		{"currency 2 huruf", "standard", "chk-cur-2", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "ID"},
		{"currency 4 huruf", "standard", "chk-cur-4", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "IDRR"},
		{"currency berangka", "standard", "chk-cur-num", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "ID1"},
		{"price_list bukan standard", "premium", "chk-list", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "IDR"},
		{"kunci berspasi", "standard", "chk key", "2027-01-01T00:00:00Z", "2027-02-01T00:00:00Z", 150000, "IDR"},
		{"valid_to sama dengan valid_from", "standard", "chk-eq", "2027-01-01T00:00:00Z", "2027-01-01T00:00:00Z", 150000, "IDR"},
		{"valid_to sebelum valid_from", "standard", "chk-rev", "2027-02-01T00:00:00Z", "2027-01-01T00:00:00Z", 150000, "IDR"},
		{"valid_to infinity", "standard", "chk-inf-to", "2030-01-01T00:00:00Z", "infinity", 150000, "IDR"},
		{"valid_from -infinity", "standard", "chk-inf-from", "-infinity", "2026-11-01T00:00:00Z", 150000, "IDR"},
	} {
		q := `INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`
		if code, err := tryInsert(wsA, env.ownerID, q, wsA, itemA, tc.list, tc.key, tc.from, tc.to, tc.amount, tc.currency); err == nil || code != "23514" {
			t.Fatalf("INSERT harga %s: want 23514, got code %q err %v", tc.name, code, err)
		}
	}
	// Kunci duplikat di workspace sama → 23505; di workspace lain boleh.
	// Interval duplikat sengaja tidak overlap supaya hanya UNIQUE
	// yang menyala (bukan EXCLUDE).
	if code, err := tryInsert(wsA, env.ownerID, priceSQL, wsA, itemA, "rls-price-owner", "2027-01-10T02:00:00Z", "2027-02-10T02:00:00Z"); err == nil || code != "23505" {
		t.Fatalf("INSERT harga kunci duplikat: want 23505, got code %q err %v", code, err)
	}
	if code, err := tryInsert(wsB, env.otherID, priceSQL, wsB, itemB, "rls-price-owner", "2026-11-10T02:00:00Z", "2026-12-10T02:00:00Z"); err != nil {
		t.Fatalf("INSERT harga kunci sama di B: want ok, got %s (%v)", code, err)
	}
	// EXCLUDE langsung lewat bootstrap (bypass RLS, constraint tetap
	// berlaku): interval yang tumpang tindih dengan fixture owner
	// → 23P01; interval yang bersebelahan → lolos.
	if _, err := boot.Exec(
		`INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
		 VALUES ($1, $2, 'standard', 'rls-excl-overlap', '2026-11-11T02:00:00Z', '2026-11-13T02:00:00Z', 150000, 'IDR')`,
		wsA, itemA); err == nil || pgCode(err) != "23P01" {
		t.Fatalf("INSERT harga EXCLUDE overlap: want 23P01, got code %q err %v", pgCode(err), err)
	}
	if _, err := boot.Exec(
		`INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
		 VALUES ($1, $2, 'standard', 'rls-excl-adjacent', '2027-01-10T02:00:00Z', '2027-02-10T02:00:00Z', 150000, 'IDR')`,
		wsA, itemA); err != nil {
		t.Fatalf("INSERT harga EXCLUDE adjacent: want ok, got %v", err)
	}
	// Runtime UPDATE/DELETE/TRUNCATE ditolak (tidak ada grant):
	// 42501 lewat tenant runtime. TRUNCATE tidak memakai parameter.
	for _, q := range []string{
		`UPDATE price_versions SET amount_minor = 1 WHERE workspace_id = $1`,
		`DELETE FROM price_versions WHERE workspace_id = $1`,
	} {
		tx := tenantTx(t, db, wsA, env.ownerID)
		_, err := tx.Exec(q, wsA)
		tx.Rollback()
		if err == nil || pgCode(err) != "42501" {
			t.Fatalf("runtime %s: want 42501, got code %q err %v", q[:12], pgCode(err), err)
		}
	}
	{
		tx := tenantTx(t, db, wsA, env.ownerID)
		_, err := tx.Exec(`TRUNCATE price_versions`)
		tx.Rollback()
		if err == nil || pgCode(err) != "42501" {
			t.Fatalf("runtime TRUNCATE: want 42501, got code %q err %v", pgCode(err), err)
		}
	}
	// Bacaan langsung runtime: konteks A melihat baris A saja.
	tx := tenantTx(t, db, wsA, env.ownerID)
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM price_versions WHERE workspace_id = $1`, wsA).Scan(&n); err != nil {
		t.Fatalf("count harga A dari A: %v", err)
	}
	if n != 3 {
		t.Fatalf("baca harga A dari A: want 3 (owner + manager + adjacent), got %d", n)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM price_versions WHERE workspace_id = $1`, wsB).Scan(&n); err != nil {
		t.Fatalf("count harga B dari A: %v", err)
	}
	if n != 0 {
		t.Fatalf("baca harga B dari A: want 0, got %d", n)
	}
	// Tanpa konteks tenant → 0 baris.
	if err := db.QueryRow(`SELECT count(*) FROM price_versions`).Scan(&n); err != nil {
		t.Fatalf("baca harga tanpa konteks: %v", err)
	}
	if n != 0 {
		t.Fatalf("baca harga tanpa konteks: want 0, got %d", n)
	}
}

func TestPriceHTTP(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PHTTP-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)
	valid := priceBody(from, to, "150000", "IDR")

	// Header Idempotency-Key: hilang, ganda, atau tidak valid → 400.
	if code, body := createPriceRaw(t, env.srv, env.wsA, item, env.ownerToken, nil, `{"valid_from":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("tanpa kunci: want 400, got %d (%v)", code, body)
	}
	if code, body := createPriceRaw(t, env.srv, env.wsA, item, env.ownerToken, []string{"k1", "k2"}, `{"valid_from":"x"}`); code != http.StatusBadRequest {
		t.Fatalf("dua kunci: want 400, got %d (%v)", code, body)
	}
	if code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "bad key", valid); code != http.StatusBadRequest {
		t.Fatalf("kunci berspasi: want 400, got %d (%v)", code, body)
	}
	// UUID workspace/barang tidak valid → 400.
	if code, _ := createPrice(t, env.srv, "bukan-uuid", item, env.ownerToken, "h-1", valid); code != http.StatusBadRequest {
		t.Fatalf("ws bukan UUID: want 400, got %d", code)
	}
	if code, _ := createPrice(t, env.srv, env.wsA, "bukan-uuid", env.ownerToken, "h-2", valid); code != http.StatusBadRequest {
		t.Fatalf("item bukan UUID: want 400, got %d", code)
	}
	// Item hilang/lintas-tenant → 404 identik.
	unknown, _ := uuidNew()
	if code, body := createPrice(t, env.srv, env.wsA, unknown, env.ownerToken, "h-3", valid); code != http.StatusNotFound {
		t.Fatalf("item hilang: want 404, got %d (%v)", code, body)
	}
	code, body := createCatalogItem(t, env.srv, env.wsB, env.otherToken, map[string]string{"sku": "SKU-PHTTP-B", "name": "Barang B", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("seed item B: want 201, got %d (%v)", code, body)
	}
	itemB, _ := body["id"].(string)
	if code, body := createPrice(t, env.srv, env.wsA, itemB, env.ownerToken, "h-4", valid); code != http.StatusNotFound {
		t.Fatalf("item lintas-tenant: want 404, got %d (%v)", code, body)
	}
	// Tanggal tidak valid → 400 field-spesifik.
	dateCases := []struct {
		name, from, to string
		want           string
	}{
		{"bukan tanggal", "bukan-tanggal", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"tanpa zona", "2026-11-10T09:00:00", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"tahun 0", "0000-06-01T00:00:00Z", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"tahun 10000 input", "10000-01-01T00:00:00Z", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"tahun ternormalisasi atas", "9999-12-31T23:30:00-02:00", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"tahun ternormalisasi bawah", "0001-01-01T00:15:00+02:00", to.Format(time.RFC3339Nano), "valid_from tidak valid"},
		{"valid_to sama", from.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), "valid_to harus sesudah valid_from"},
		{"valid_to sebelum", to.Format(time.RFC3339Nano), from.Format(time.RFC3339Nano), "valid_to harus sesudah valid_from"},
	}
	for i, tc := range dateCases {
		b := map[string]any{"valid_from": tc.from, "valid_to": tc.to, "amount_minor": "150000", "currency": "IDR"}
		code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, fmt.Sprintf("h-date-%d", i), b)
		if code != http.StatusBadRequest {
			t.Fatalf("tanggal %s: want 400, got %d (%v)", tc.name, code, body)
		}
		assertFlatError(t, body, tc.want)
	}
	// amount_minor: semua bentuk non-kanonis → 400.
	for i, amount := range []string{"0150000", "-1", "+150000", " 150000", "150000 ", "150000.0", "1.5e5", "9223372036854775808", ""} {
		b := priceBody(from, to, amount, "IDR")
		code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, fmt.Sprintf("h-amt-%d", i), b)
		if code != http.StatusBadRequest {
			t.Fatalf("amount %q: want 400, got %d (%v)", amount, code, body)
		}
		assertFlatError(t, body, "amount_minor tidak valid")
	}
	// JSON number untuk amount → 400 body (decode gagal); null →
	// 400 amount (decode null menjadi string kosong).
	raw := fmt.Sprintf(`{"valid_from":%q,"valid_to":%q,"amount_minor":150000,"currency":"IDR"}`,
		from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	if code, body := createPriceRaw(t, env.srv, env.wsA, item, env.ownerToken, []string{"h-amt-num"}, raw); code != http.StatusBadRequest {
		t.Fatalf("amount JSON number: want 400, got %d (%v)", code, body)
	}
	raw = fmt.Sprintf(`{"valid_from":%q,"valid_to":%q,"amount_minor":null,"currency":"IDR"}`,
		from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano))
	if code, body := createPriceRaw(t, env.srv, env.wsA, item, env.ownerToken, []string{"h-amt-null"}, raw); code != http.StatusBadRequest {
		t.Fatalf("amount null: want 400, got %d (%v)", code, body)
	}
	// Currency: semua bentuk non-[A-Z]{3} → 400.
	for i, cur := range []string{"idr", "ID", "IDRR", "ID1", " IDR", ""} {
		b := priceBody(from, to, "150000", cur)
		code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, fmt.Sprintf("h-cur-%d", i), b)
		if code != http.StatusBadRequest {
			t.Fatalf("currency %q: want 400, got %d (%v)", cur, code, body)
		}
		assertFlatError(t, body, "currency tidak valid")
	}
	// Field tak dikenal → 400 body.
	bad := priceBody(from, to, "150000", "IDR")
	bad["foo"] = "bar"
	if code, _ := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "h-unk", bad); code != http.StatusBadRequest {
		t.Fatalf("field tak dikenal: want 400, got %d", code)
	}
	// Peran: kasir tidak boleh membuat (403); tanpa token → 401;
	// nonmember → 403; workspace tak dikenal → 403.
	if code, body := createPrice(t, env.srv, env.wsA, item, env.cashToken, "h-cash", valid); code != http.StatusForbidden {
		t.Fatalf("kasir create: want 403, got %d (%v)", code, body)
	}
	if code, _ := createPrice(t, env.srv, env.wsA, item, "", "h-noauth", valid); code != http.StatusUnauthorized {
		t.Fatalf("tanpa token create: want 401, got %d", code)
	}
	if code, _ := createPrice(t, env.srv, env.wsA, item, env.otherToken, "h-other", valid); code != http.StatusForbidden {
		t.Fatalf("nonmember create: want 403, got %d", code)
	}
	if code, _ := createPrice(t, env.srv, unknown, item, env.ownerToken, "h-nows", valid); code != http.StatusForbidden {
		t.Fatalf("workspace tak dikenal: want 403, got %d", code)
	}
	// Create sah oleh manager → 201; DTO memakai string uang dan
	// recorded_at yang bisa di-parse.
	code, body = createPrice(t, env.srv, env.wsA, item, env.mgrToken, "h-ok", valid)
	if code != http.StatusCreated {
		t.Fatalf("manager create: want 201, got %d (%v)", code, body)
	}
	if body["price_list_id"] != "standard" || body["amount_minor"] != "150000" || body["currency"] != "IDR" {
		t.Fatalf("DTO create: want standard/150000/IDR, got %v", body)
	}
	rec, _ := body["recorded_at"].(string)
	if _, err := time.Parse(time.RFC3339Nano, rec); err != nil {
		t.Fatalf("recorded_at tidak bisa di-parse: %q (%v)", rec, err)
	}
	if body["valid_from"] != from.UTC().Format(time.RFC3339Nano) || body["valid_to"] != to.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("tanggal DTO: want %s/%s, got %v/%v", from.UTC().Format(time.RFC3339Nano), to.UTC().Format(time.RFC3339Nano), body["valid_from"], body["valid_to"])
	}
	// List: kasir boleh membaca (200); urutan (valid_from, id)
	// bukan urutan insert; item tanpa harga → list kosong. Uji
	// urutan memakai item lain supaya interval h-ok di atas tidak
	// bertabrakan dengan interval yang diuji.
	_, itemOrd := seedAssetRefs(t, env, "SKU-PHTTP-ORD")
	seedPrice(t, env, itemOrd, "h-ord-2", base.Add(25*time.Hour), base.Add(49*time.Hour), "175000", "IDR")
	seedPrice(t, env, itemOrd, "h-ord-1", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	if code, body := listPrices(t, env.srv, env.wsA, itemOrd, env.cashToken, ""); code != http.StatusOK {
		t.Fatalf("kasir list: want 200, got %d (%v)", code, body)
	}
	code, body = listPrices(t, env.srv, env.wsA, itemOrd, env.ownerToken, "")
	if code != http.StatusOK {
		t.Fatalf("list: want 200, got %d (%v)", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list: want 2 harga, got %d (%v)", len(items), body)
	}
	first, _ := items[0].(map[string]any)
	second, _ := items[1].(map[string]any)
	if first["client_request_id"] != "h-ord-1" || second["client_request_id"] != "h-ord-2" {
		t.Fatalf("list: want urutan h-ord-1 lalu h-ord-2, got %v", items)
	}
	if first["amount_minor"] != "150000" || second["amount_minor"] != "175000" {
		t.Fatalf("list: amount harus string eksak, got %v", items)
	}
	_, itemEmpty := seedAssetRefs(t, env, "SKU-PHTTP-EMPTY")
	if code, body := listPrices(t, env.srv, env.wsA, itemEmpty, env.ownerToken, ""); code != http.StatusOK {
		t.Fatalf("list kosong: want 200, got %d (%v)", code, body)
	} else if got, _ := body["items"].([]any); len(got) != 0 {
		t.Fatalf("list kosong: want 0 item, got %v", body)
	}
	if code, _ := listPrices(t, env.srv, env.wsA, unknown, env.ownerToken, ""); code != http.StatusNotFound {
		t.Fatalf("list item hilang: want 404, got %d", code)
	}
	if code, _ := listPrices(t, env.srv, env.wsA, itemB, env.ownerToken, ""); code != http.StatusNotFound {
		t.Fatalf("list item lintas-tenant: want 404, got %d", code)
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?offset=-1"} {
		if code, _ := listPrices(t, env.srv, env.wsA, item, env.ownerToken, q); code != http.StatusBadRequest {
			t.Fatalf("list %s: want 400, got %d", q, code)
		}
	}
	// Quote: draft hilang/lintas-tenant → 404 identik.
	if code, body := getQuote(t, env.srv, env.wsA, unknown, env.ownerToken); code != http.StatusNotFound {
		t.Fatalf("quote draft hilang: want 404, got %d (%v)", code, body)
	}
	// Draft wsB (fixture bootstrap, tanpa guard rental) tetap 404
	// bila dibaca dari wsA.
	var custB string
	if err := env.boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, env.wsB).Scan(&custB); err != nil {
		t.Fatalf("fixture party B: %v", err)
	}
	draftB := insertDraftFixture(t, env.boot, env.wsB, custB, itemB, "cross-1", "2026-11-10T02:00:00Z", "2026-11-12T02:00:00Z")
	code, body = getQuote(t, env.srv, env.wsA, draftB, env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("quote draft lintas-tenant: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "draft tidak ditemukan")
}

// TestPriceScheduling: v1 201, v2 adjacent 201, overlap 409,
// koreksi prematur 409, baris lama tidak berubah.
func TestPriceScheduling(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PSCH-A")
	base := dbCutoff(t, env.boot)

	v0 := seedPrice(t, env, item, "sch-v0", base.Add(30*time.Minute), base.Add(time.Hour), "100000", "IDR")
	v1 := seedPrice(t, env, item, "sch-v1", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	v2 := seedPrice(t, env, item, "sch-v2", base.Add(25*time.Hour), base.Add(49*time.Hour), "175000", "IDR")
	before0, _ := readPrice(t, env.boot, env.wsA, v0)
	before1, _ := readPrice(t, env.boot, env.wsA, v1)
	before2, _ := readPrice(t, env.boot, env.wsA, v2)

	// Overlap dengan v0/v1/v2 → 409; koreksi prematur v1 → 409.
	overlapCases := []struct {
		key    string
		from   time.Time
		to     time.Time
		amount string
	}{
		{"sch-ov1", base.Add(2 * time.Hour), base.Add(26 * time.Hour), "150000"},
		{"sch-ov2", base.Add(30 * time.Minute), base.Add(2 * time.Hour), "150000"},
		{"sch-ov3", base.Add(24 * time.Hour), base.Add(26 * time.Hour), "150000"},
		{"sch-ov4", base.Add(15 * time.Minute), base.Add(45 * time.Minute), "150000"},
		{"sch-correct", base.Add(time.Hour), base.Add(25 * time.Hour), "999999"},
	}
	for _, tc := range overlapCases {
		code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, tc.key, priceBody(tc.from, tc.to, tc.amount, "IDR"))
		if code != http.StatusConflict {
			t.Fatalf("overlap %s: want 409, got %d (%v)", tc.key, code, body)
		}
		assertFlatError(t, body, "interval harga bertabrakan")
	}
	// Adjacent sesudah v2 → 201 (bukan koreksi, interval baru).
	seedPrice(t, env, item, "sch-v3", base.Add(49*time.Hour), base.Add(50*time.Hour), "200000", "IDR")
	// Baris lama tidak berubah sedikit pun oleh upaya overlap.
	after0, _ := readPrice(t, env.boot, env.wsA, v0)
	after1, _ := readPrice(t, env.boot, env.wsA, v1)
	after2, _ := readPrice(t, env.boot, env.wsA, v2)
	if !reflect.DeepEqual(before0, after0) || !reflect.DeepEqual(before1, after1) || !reflect.DeepEqual(before2, after2) {
		t.Fatalf("baris lama berubah oleh upaya overlap:\nv0 %v -> %v\nv1 %v -> %v\nv2 %v -> %v", before0, after0, before1, after1, before2, after2)
	}
	if countRows(t, env.boot, "price_versions", env.wsA) != 4 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 4 {
		t.Fatalf("scheduling: want 4 harga + 4 audit, got %d + %d",
			countRows(t, env.boot, "price_versions", env.wsA),
			countBusinessAudits(t, env.boot, env.wsA, "price_version_created"))
	}
}

// TestPriceNewVsReplayArchive: jadwal BARU sesudah item diarsipkan →
// 409; replay kunci lama sesudah arsip → 200 (bacaan histori); list
// harga item yang diarsipkan tetap 200.
func TestPriceNewVsReplayArchive(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PARCH-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)
	id := seedPrice(t, env, item, "arch-1", from, to, "150000", "IDR")
	before, _ := readPrice(t, env.boot, env.wsA, id)
	archiveItem(t, env.boot, item)

	code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "arch-new", priceBody(base.Add(26*time.Hour), base.Add(50*time.Hour), "175000", "IDR"))
	if code != http.StatusConflict {
		t.Fatalf("jadwal baru item archived: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak aktif")
	if countRows(t, env.boot, "price_versions", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 1 {
		t.Fatalf("jadwal baru archived: want tetap 1 harga + 1 audit")
	}
	code, body = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "arch-1", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusOK {
		t.Fatalf("replay sesudah arsip: want 200, got %d (%v)", code, body)
	}
	if body["id"] != id {
		t.Fatalf("replay sesudah arsip: want id %s, got %v", id, body)
	}
	// Bandingkan mentah DB-ke-DB (format teks PG yang sama), bukan
	// teks DB melawan RFC3339Nano respons — keduanya instant yang
	// sama dalam format berbeda (pelajaran B3).
	after, _ := readPrice(t, env.boot, env.wsA, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("replay sesudah arsip: want baris tidak berubah, got %v -> %v", before, after)
	}
	if countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 1 {
		t.Fatalf("replay sesudah arsip: want tetap 1 audit")
	}
	if code, body := listPrices(t, env.srv, env.wsA, item, env.ownerToken, ""); code != http.StatusOK {
		t.Fatalf("list sesudah arsip: want 200, got %d (%v)", code, body)
	} else if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("list sesudah arsip: want 1 harga, got %v", body)
	}
}

func TestPriceQuote(t *testing.T) {
	env := setupPrice(t)
	cust, item := seedAssetRefs(t, env, "SKU-PQ-A")
	base := dbCutoff(t, env.boot)
	v1 := seedPrice(t, env, item, "q-v1", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	v2 := seedPrice(t, env, item, "q-v2", base.Add(25*time.Hour), base.Add(49*time.Hour), "175000", "IDR")
	activateRental(t, env)

	// Draft di dalam v1: 72 jam → 3 unit × 150000 = 450000.
	d1, d1body := seedDraftForHold(t, env, cust, item,
		base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(74*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body := getQuote(t, env.srv, env.wsA, d1, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("quote v1: want 200, got %d (%v)", code, body)
	}
	if body["price_version_id"] != v1 || body["amount_minor"] != "150000" || body["total_minor"] != "450000" ||
		body["units"] != float64(3) || body["unit"] != "24h" || body["currency"] != "IDR" {
		t.Fatalf("quote v1: want v1/150000/450000/3 unit, got %v", body)
	}
	if body["starts_at"] != d1body["starts_at"] || body["ends_at"] != d1body["ends_at"] {
		t.Fatalf("quote v1: periode harus sama dengan draft, got %v vs %v", body, d1body)
	}
	// Draft di dalam v2: 72 jam → 3 unit × 175000 = 525000.
	d2, _ := seedDraftForHold(t, env, cust, item,
		base.Add(26*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(98*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body = getQuote(t, env.srv, env.wsA, d2, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("quote v2: want 200, got %d (%v)", code, body)
	}
	if body["price_version_id"] != v2 || body["total_minor"] != "525000" || body["units"] != float64(3) {
		t.Fatalf("quote v2: want v2/525000/3 unit, got %v", body)
	}
	// Batas half-open: starts tepat pada v1.valid_from → v1; tepat
	// pada v1.valid_to (= v2.valid_from) → v2.
	d3, _ := seedDraftForHold(t, env, cust, item,
		base.Add(time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(25*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body = getQuote(t, env.srv, env.wsA, d3, env.ownerToken)
	if code != http.StatusOK || body["price_version_id"] != v1 {
		t.Fatalf("quote batas bawah v1: want 200 v1, got %d (%v)", code, body)
	}
	d4, _ := seedDraftForHold(t, env, cust, item,
		base.Add(25*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(49*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body = getQuote(t, env.srv, env.wsA, d4, env.ownerToken)
	if code != http.StatusOK || body["price_version_id"] != v2 {
		t.Fatalf("quote batas v1/v2: want 200 v2, got %d (%v)", code, body)
	}
	// Di luar semua interval → 404 harga efektif; sebelum v1 juga 404.
	d5, _ := seedDraftForHold(t, env, cust, item,
		base.Add(50*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(74*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body = getQuote(t, env.srv, env.wsA, d5, env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("quote tanpa interval: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "harga efektif tidak ditemukan")
	d6, _ := seedDraftForHold(t, env, cust, item,
		base.Add(30*time.Minute).UTC().Format(time.RFC3339Nano),
		base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano))
	code, body = getQuote(t, env.srv, env.wsA, d6, env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("quote sebelum v1: want 404, got %d (%v)", code, body)
	}
	// Kasir boleh membaca quote (view_workspace).
	if code, _ := getQuote(t, env.srv, env.wsA, d1, env.cashToken); code != http.StatusOK {
		t.Fatalf("kasir quote: want 200, got %d", code)
	}
}

// TestPriceQuoteHistory: quote/list tetap terbaca sesudah item
// diarsipkan dan sesudah entitlement dicabut + modul dinonaktifkan —
// tanpa mengubah guard Rental yang sebenarnya.
func TestPriceQuoteHistory(t *testing.T) {
	env := setupPrice(t)
	cust, itemA := seedAssetRefs(t, env, "SKU-PQH-A")
	base := dbCutoff(t, env.boot)
	seedPrice(t, env, itemA, "qh-a", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	activateRental(t, env)
	draftA, _ := seedDraftForHold(t, env, cust, itemA,
		base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(74*time.Hour).UTC().Format(time.RFC3339Nano))
	if code, _ := getQuote(t, env.srv, env.wsA, draftA, env.ownerToken); code != http.StatusOK {
		t.Fatalf("quote sebelum arsip: want 200, got %d", code)
	}
	archiveItem(t, env.boot, itemA)
	code, body := getQuote(t, env.srv, env.wsA, draftA, env.ownerToken)
	if code != http.StatusOK || body["total_minor"] != "450000" {
		t.Fatalf("quote sesudah arsip: want 200 total 450000, got %d (%v)", code, body)
	}
	if code, body := listPrices(t, env.srv, env.wsA, itemA, env.ownerToken, ""); code != http.StatusOK {
		t.Fatalf("list sesudah arsip: want 200, got %d (%v)", code, body)
	} else if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("list sesudah arsip: want 1 harga, got %v", body)
	}

	// Item/draft kedua: revoke + deactivate, lalu quote/list tetap 200.
	_, itemB := seedAssetRefs(t, env, "SKU-PQH-B")
	seedPrice(t, env, itemB, "qh-b", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	draftB, _ := seedDraftForHold(t, env, cust, itemB,
		base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(74*time.Hour).UTC().Format(time.RFC3339Nano))
	if code, _ := getQuote(t, env.srv, env.wsA, draftB, env.ownerToken); code != http.StatusOK {
		t.Fatalf("quote sebelum revoke: want 200, got %d", code)
	}
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	if code, body := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate rental: want 200, got %d (%v)", code, body)
	}
	code, body = getQuote(t, env.srv, env.wsA, draftB, env.ownerToken)
	if code != http.StatusOK || body["total_minor"] != "450000" {
		t.Fatalf("quote sesudah revoke/deactivate: want 200 total 450000, got %d (%v)", code, body)
	}
	if code, _ := listPrices(t, env.srv, env.wsA, itemB, env.ownerToken, ""); code != http.StatusOK {
		t.Fatalf("list sesudah revoke/deactivate: want 200, got %d", code)
	}
}

// TestPriceQuoteArithmetic: 24 jam / 24 jam+1µs / 49 jam, rate nol,
// MaxInt64, round-trip string di luar 2^53-1, rentang >292 tahun,
// rentang maksimum, rate maksimum yang aman, dan overflow → 422.
func TestPriceQuoteArithmetic(t *testing.T) {
	env := setupPrice(t)
	cust, itemW := seedAssetRefs(t, env, "SKU-PAR-W")
	base := dbCutoff(t, env.boot)
	activateRental(t, env)
	seedPrice(t, env, itemW, "ar-wide", base.Add(time.Hour), base.Add(31*24*time.Hour), "150000", "IDR")
	starts := base.Add(2 * time.Hour).UTC()
	quoteFor := func(item, startsStr, endsStr string) (int, map[string]any) {
		t.Helper()
		draft, _ := seedDraftForHold(t, env, cust, item, startsStr, endsStr)
		return getQuote(t, env.srv, env.wsA, draft, env.ownerToken)
	}
	cases := []struct {
		name  string
		ends  time.Time
		units float64
		total string
	}{
		{"24 jam", starts.Add(24 * time.Hour), 1, "150000"},
		{"24 jam + 1 mikrodetik", starts.Add(24*time.Hour + time.Microsecond), 2, "300000"},
		{"49 jam", starts.Add(49 * time.Hour), 3, "450000"},
	}
	for _, tc := range cases {
		code, body := quoteFor(itemW, starts.Format(time.RFC3339Nano), tc.ends.Format(time.RFC3339Nano))
		if code != http.StatusOK {
			t.Fatalf("quote %s: want 200, got %d (%v)", tc.name, code, body)
		}
		if body["units"] != tc.units || body["total_minor"] != tc.total {
			t.Fatalf("quote %s: want units %v total %s, got %v", tc.name, tc.units, tc.total, body)
		}
	}
	// Rate nol → total "0" (tanpa pembagian).
	_, itemZ := seedAssetRefs(t, env, "SKU-PAR-Z")
	seedPrice(t, env, itemZ, "ar-zero", base.Add(time.Hour), base.Add(31*24*time.Hour), "0", "IDR")
	code, body := quoteFor(itemZ, starts.Format(time.RFC3339Nano), starts.Add(72*time.Hour).Format(time.RFC3339Nano))
	if code != http.StatusOK || body["units"] != float64(3) || body["total_minor"] != "0" {
		t.Fatalf("quote rate nol: want 200 units 3 total 0, got %d (%v)", code, body)
	}
	// MaxInt64 dengan 1 unit → total MaxInt64 (tanpa overflow).
	_, itemM := seedAssetRefs(t, env, "SKU-PAR-M")
	seedPrice(t, env, itemM, "ar-max", base.Add(time.Hour), base.Add(31*24*time.Hour), "9223372036854775807", "IDR")
	code, body = quoteFor(itemM, starts.Format(time.RFC3339Nano), starts.Add(time.Hour).Format(time.RFC3339Nano))
	if code != http.StatusOK || body["total_minor"] != "9223372036854775807" {
		t.Fatalf("quote MaxInt64: want 200 total 9223372036854775807, got %d (%v)", code, body)
	}
	// Round-trip string di luar 2^53-1: "9007199254740993" harus
	// dipertahankan eksak di list maupun quote (tanpa float).
	_, itemR := seedAssetRefs(t, env, "SKU-PAR-R")
	seedPrice(t, env, itemR, "ar-big", base.Add(time.Hour), base.Add(31*24*time.Hour), "9007199254740993", "IDR")
	if code, body := listPrices(t, env.srv, env.wsA, itemR, env.ownerToken, ""); code != http.StatusOK {
		t.Fatalf("list round-trip: want 200, got %d (%v)", code, body)
	} else if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("list round-trip: want 1 harga, got %v", body)
	} else if first, _ := items[0].(map[string]any); first["amount_minor"] != "9007199254740993" {
		t.Fatalf("list round-trip: want 9007199254740993, got %v", first)
	}
	code, body = quoteFor(itemR, starts.Format(time.RFC3339Nano), starts.Add(time.Hour).Format(time.RFC3339Nano))
	if code != http.StatusOK || body["total_minor"] != "9007199254740993" {
		t.Fatalf("quote round-trip: want 200 total 9007199254740993, got %d (%v)", code, body)
	}
	// Rentang panjang memakai fixture bootstrap istimewa (bukan API
	// no-backdate): versi historis + draft lewat API (draft API
	// menerima tahun 1-9999).
	_, itemL := seedAssetRefs(t, env, "SKU-PAR-L")
	insertPriceFixture(t, env.boot, env.wsA, itemL, "ar-long", "0001-01-01T00:00:00Z", "0295-01-01T00:00:00Z", "150000", "IDR")
	code, body = quoteFor(itemL, "0001-01-01T00:00:00Z", "0294-01-01T00:00:00Z")
	if code != http.StatusOK || body["units"] != float64(107016) || body["total_minor"] != "16052400000" {
		t.Fatalf("quote >292 tahun: want 200 units 107016 total 16052400000, got %d (%v)", code, body)
	}
	_, itemX := seedAssetRefs(t, env, "SKU-PAR-X")
	insertPriceFixture(t, env.boot, env.wsA, itemX, "ar-maxrange", "0001-01-01T00:00:00Z", "9999-12-31T00:00:00Z", "150000", "IDR")
	code, body = quoteFor(itemX, "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z")
	if code != http.StatusOK || body["units"] != float64(3652059) || body["total_minor"] != "547808850000" {
		t.Fatalf("quote rentang maksimum: want 200 units 3652059 total 547808850000, got %d (%v)", code, body)
	}
	// Rate maksimum yang muat pada units maksimum → 200; satu
	// integer di atasnya → 422.
	_, itemS := seedAssetRefs(t, env, "SKU-PAR-S")
	insertPriceFixture(t, env.boot, env.wsA, itemS, "ar-safemax", "0001-01-01T00:00:00Z", "9999-12-31T00:00:00Z", "2525526569218", "IDR")
	wantTotal := strconv.FormatInt(int64(2525526569218)*int64(3652059), 10)
	code, body = quoteFor(itemS, "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z")
	if code != http.StatusOK || body["total_minor"] != wantTotal {
		t.Fatalf("quote rate maksimum aman: want 200 total %s, got %d (%v)", wantTotal, code, body)
	}
	_, itemO := seedAssetRefs(t, env, "SKU-PAR-O")
	insertPriceFixture(t, env.boot, env.wsA, itemO, "ar-overflow", "0001-01-01T00:00:00Z", "9999-12-31T00:00:00Z", "2525526569219", "IDR")
	code, body = quoteFor(itemO, "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z")
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("quote overflow: want 422, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "total di luar batas yang dapat diwakili")
}

// TestPriceBackdate: jadwal baru dengan valid_from di masa lalu →
// 400 tanpa baris/audit; cutoff dijangkar SESUDAH lock wait → 400;
// replay kunci lama tetap 200 sesudah valid_from lewat.
func TestPriceBackdate(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PBD-A")
	base := dbCutoff(t, env.boot)

	// valid_from 1 jam di masa lalu → 400, tanpa baris/audit.
	code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "bd-past",
		priceBody(base.Add(-time.Hour), base.Add(24*time.Hour), "150000", "IDR"))
	if code != http.StatusBadRequest {
		t.Fatalf("backdate: want 400, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "valid_from tidak boleh di masa lalu")
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 0 {
		t.Fatalf("backdate: want 0 harga + 0 audit")
	}

	// Lock-wait cutoff: permintaan dimulai saat valid_from masih di
	// masa depan, menunggu lock, dan mendapat lock sesudah valid_from
	// lewat → 400 (cutoff dijangkar sesudah lock, bukan awal request).
	lockTx, err := env.boot.Begin()
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer lockTx.Rollback()
	var locked string
	if err := lockTx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, env.wsA).Scan(&locked); err != nil {
		t.Fatalf("pegang workspace lock: %v", err)
	}
	type result struct {
		code int
		body map[string]any
		err  error
	}
	ch := make(chan result, 1)
	from := base.Add(3 * time.Second)
	to := base.Add(25 * time.Hour)
	go func() {
		code, body, err := callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items/"+item+"/prices", env.ownerToken, []string{"bd-wait"}, priceBody(from, to, "150000", "IDR"))
		ch <- result{code, body, err}
	}()
	time.Sleep(5 * time.Second)
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	res := <-ch
	if res.err != nil {
		t.Fatalf("create sesudah lock: %v", res.err)
	}
	if res.code != http.StatusBadRequest {
		t.Fatalf("backdate lock-wait: want 400, got %d (%v)", res.code, res.body)
	}
	assertFlatError(t, res.body, "valid_from tidak boleh di masa lalu")
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 0 {
		t.Fatalf("backdate lock-wait: want 0 harga + 0 audit")
	}

	// Replay: jadwal dibuat saat valid_from masih di masa depan,
	// lalu valid_from lewat — replay kunci yang sama tetap 200
	// baris asli, tanpa audit baru.
	from2 := dbCutoff(t, env.boot).Add(2 * time.Second)
	to2 := base.Add(25 * time.Hour)
	id := seedPrice(t, env, item, "bd-replay", from2, to2, "150000", "IDR")
	before, _ := readPrice(t, env.boot, env.wsA, id)
	time.Sleep(3 * time.Second)
	code, body = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "bd-replay", priceBody(from2, to2, "150000", "IDR"))
	if code != http.StatusOK {
		t.Fatalf("replay sesudah valid_from lewat: want 200, got %d (%v)", code, body)
	}
	if body["id"] != id {
		t.Fatalf("replay sesudah valid_from lewat: want id %s, got %v", id, body)
	}
	// Bandingkan mentah DB-ke-DB (format teks PG yang sama), bukan
	// teks DB melawan RFC3339Nano respons.
	after, _ := readPrice(t, env.boot, env.wsA, id)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("replay sesudah valid_from lewat: want baris tidak berubah, got %v -> %v", before, after)
	}
	if countRows(t, env.boot, "price_versions", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 1 {
		t.Fatalf("replay sesudah valid_from lewat: want tetap 1 harga + 1 audit")
	}
}

// TestPriceIdempotency: replay UUID huruf besar, ekuivalen zona
// waktu/mikrodetik, kunci case-sensitive, dan payload beda → 409.
func TestPriceIdempotency(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PID-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)

	// UUID huruf besar di path → 201; respons memakai huruf kecil;
	// retry persis → 200 baris yang sama.
	upper := strings.ToUpper(item)
	code, body := createPrice(t, env.srv, env.wsA, upper, env.ownerToken, "idem-1", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusCreated {
		t.Fatalf("create UUID huruf besar: want 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if body["catalog_item_id"] != item {
		t.Fatalf("create UUID huruf besar: want item huruf kecil %s, got %v", item, body)
	}
	code, body = createPrice(t, env.srv, env.wsA, upper, env.ownerToken, "idem-1", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusOK || body["id"] != id {
		t.Fatalf("replay UUID huruf besar: want 200 id %s, got %d (%v)", id, code, body)
	}
	// Buat huruf kecil lalu retry huruf besar → 200 juga.
	seedPrice(t, env, item, "idem-2", base.Add(26*time.Hour), base.Add(50*time.Hour), "175000", "IDR")
	code, body = createPrice(t, env.srv, env.wsA, upper, env.ownerToken, "idem-2", priceBody(base.Add(26*time.Hour), base.Add(50*time.Hour), "175000", "IDR"))
	if code != http.StatusOK {
		t.Fatalf("replay kecil->besar: want 200, got %d (%v)", code, body)
	}
	// Item yang benar-benar berbeda dengan kunci yang sama → 409.
	_, item2 := seedAssetRefs(t, env, "SKU-PID-B")
	code, body = createPrice(t, env.srv, env.wsA, item2, env.ownerToken, "idem-1", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusConflict {
		t.Fatalf("kunci sama item beda: want 409, got %d (%v)", code, body)
	}
	// Kunci case-sensitive: "IDEM-1" adalah kunci yang berbeda.
	code, body = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "IDEM-1", priceBody(base.Add(51*time.Hour), base.Add(75*time.Hour), "200000", "IDR"))
	if code != http.StatusCreated {
		t.Fatalf("kunci beda case: want 201, got %d (%v)", code, body)
	}
	// Ekuivalen zona waktu: valid_from yang sama ditulis +07:00 lalu
	// di-replay dalam bentuk Z → 200; selisih 1 mikrodetik → 409.
	// Interval [base+1h, base+25h) sudah dipakai idem-1 pada item
	// ini, jadi pakai item segar SKU-PID-C.
	_, itemC := seedAssetRefs(t, env, "SKU-PID-C")
	wib := time.FixedZone("WIB", 7*3600)
	fromWIB := from.In(wib).Format(time.RFC3339Nano)
	toWIB := to.In(wib).Format(time.RFC3339Nano)
	code, body = createPrice(t, env.srv, env.wsA, itemC, env.ownerToken, "idem-3",
		map[string]any{"valid_from": fromWIB, "valid_to": toWIB, "amount_minor": "150000", "currency": "IDR"})
	if code != http.StatusCreated {
		t.Fatalf("create +07:00: want 201, got %d (%v)", code, body)
	}
	code, body = createPrice(t, env.srv, env.wsA, itemC, env.ownerToken, "idem-3", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusOK {
		t.Fatalf("replay ekuivalen zona: want 200, got %d (%v)", code, body)
	}
	code, body = createPrice(t, env.srv, env.wsA, itemC, env.ownerToken, "idem-3", priceBody(from.Add(time.Microsecond), to, "150000", "IDR"))
	if code != http.StatusConflict {
		t.Fatalf("replay selisih 1 mikrodetik: want 409, got %d (%v)", code, body)
	}
	if countRows(t, env.boot, "price_versions", env.wsA) != 4 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 4 {
		t.Fatalf("idempotency: want 4 harga + 4 audit, got %d + %d",
			countRows(t, env.boot, "price_versions", env.wsA),
			countBusinessAudits(t, env.boot, env.wsA, "price_version_created"))
	}
}

// TestPriceConcurrent: overlap paralel → satu 201 + satu 409;
// kunci sama paralel → satu 201 + satu 200, satu audit.
func TestPriceConcurrent(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PCON-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)

	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items/"+item+"/prices", env.ownerToken, []string{"conc-a"}, priceBody(from, to, "150000", "IDR"))
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items/"+item+"/prices", env.mgrToken, []string{"conc-b"}, priceBody(from.Add(time.Hour), to.Add(time.Hour), "150000", "IDR"))
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{http.StatusCreated, http.StatusConflict}) {
		t.Fatalf("overlap paralel: want [201 409], got %v", codes)
	}
	if countRows(t, env.boot, "price_versions", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 1 {
		t.Fatalf("overlap paralel: want 1 harga + 1 audit")
	}

	codes, _ = parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items/"+item+"/prices", env.ownerToken, []string{"conc-same"}, priceBody(base.Add(26*time.Hour), base.Add(50*time.Hour), "175000", "IDR"))
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/catalog/items/"+item+"/prices", env.mgrToken, []string{"conc-same"}, priceBody(base.Add(26*time.Hour), base.Add(50*time.Hour), "175000", "IDR"))
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{http.StatusOK, http.StatusCreated}) {
		t.Fatalf("kunci sama paralel: want [200 201], got %v", codes)
	}
	if countRows(t, env.boot, "price_versions", env.wsA) != 2 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 2 {
		t.Fatalf("kunci sama paralel: want 2 harga + 2 audit")
	}
}

// TestPriceGuardFaults: fault izin guard dan fault insert → 500
// terkendali tanpa baris/audit parsial; sesudah pemulihan, create
// yang sama berhasil.
func TestPriceGuardFaults(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PFAULT-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)

	// Fault has_permission untuk manage_business_data → 500.
	def := installPermissionFaultPerm(t, env.boot, "manage_business_data")
	code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "fault-mbd", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusInternalServerError {
		t.Fatalf("fault manage_business_data: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "izin gagal diperiksa")
	restorePermissionFault(t, env.boot, def)
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 {
		t.Fatalf("fault manage_business_data: want 0 harga")
	}

	// Fault has_permission untuk view_workspace → 500.
	def = installPermissionFault(t, env.boot)
	code, body = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "fault-vw", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusInternalServerError {
		t.Fatalf("fault view_workspace: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "izin gagal diperiksa")
	restorePermissionFault(t, env.boot, def)
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 {
		t.Fatalf("fault view_workspace: want 0 harga")
	}

	// REVOKE SELECT pada price_versions / catalog_items → 500
	// transaksi gagal; grant dikembalikan lewat cleanup + eksplisit.
	t.Cleanup(func() {
		for _, table := range []string{"price_versions", "catalog_items"} {
			_, _ = env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`)
		}
	})
	for name, table := range map[string]string{
		"harga":  "price_versions",
		"barang": "catalog_items",
	} {
		if _, err := env.boot.Exec(`REVOKE SELECT ON ` + table + ` FROM umnia_business`); err != nil {
			t.Fatalf("revoke SELECT %s: %v", table, err)
		}
		code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "fault-"+name, priceBody(from, to, "150000", "IDR"))
		if code != http.StatusInternalServerError || body["error"] != "transaksi gagal" {
			t.Fatalf("fault %s: want 500 transaksi gagal, got %d (%v)", name, code, body)
		}
		if countRows(t, env.boot, "price_versions", env.wsA) != 0 {
			t.Fatalf("fault %s: want 0 harga", name)
		}
		if _, err := env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`); err != nil {
			t.Fatalf("grant kembali SELECT %s: %v", table, err)
		}
	}

	// Fault insert (trigger): hanya kunci fault-ins yang gagal.
	_, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_price_insert() RETURNS trigger AS $$
		BEGIN
			IF NEW.client_request_id = 'fault-ins' THEN
				RAISE EXCEPTION 'synthetic insert failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatalf("buat fungsi fault insert: %v", err)
	}
	_, err = env.boot.Exec(`CREATE TRIGGER qa_fault_price_insert BEFORE INSERT ON price_versions FOR EACH ROW EXECUTE FUNCTION app.qa_fail_price_insert()`)
	if err != nil {
		t.Fatalf("pasang trigger fault insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_price_insert ON price_versions`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_price_insert()`)
	})
	code, body = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "fault-ins", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusInternalServerError {
		t.Fatalf("fault insert: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "harga gagal dibuat")
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 0 {
		t.Fatalf("fault insert: want 0 harga + 0 audit")
	}
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_price_insert ON price_versions`); err != nil {
		t.Fatalf("lepas trigger fault insert: %v", err)
	}
	code, _ = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "fault-ok", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusCreated {
		t.Fatalf("create sesudah pemulihan fault insert: want 201, got %d", code)
	}
}

// TestPriceAuditFaultAtomicity: fault trigger audit → 500 tanpa
// baris/audit parsial; sesudah pemulihan, create berhasil.
func TestPriceAuditFaultAtomicity(t *testing.T) {
	env := setupPrice(t)
	_, item := seedAssetRefs(t, env, "SKU-PAFAULT-A")
	base := dbCutoff(t, env.boot)
	from := base.Add(time.Hour)
	to := base.Add(25 * time.Hour)

	_, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_price_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action = 'price_version_created' THEN
				RAISE EXCEPTION 'synthetic audit failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatalf("buat fungsi fault audit: %v", err)
	}
	_, err = env.boot.Exec(`CREATE TRIGGER qa_fault_price_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_price_audit()`)
	if err != nil {
		t.Fatalf("pasang trigger fault audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_price_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_price_audit()`)
	})
	code, body := createPrice(t, env.srv, env.wsA, item, env.ownerToken, "afault-1", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusInternalServerError {
		t.Fatalf("create dengan fault audit: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "audit gagal ditulis")
	if countRows(t, env.boot, "price_versions", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "price_version_created") != 0 {
		t.Fatalf("fault audit: want 0 harga + 0 audit")
	}
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_price_audit ON audit_events`); err != nil {
		t.Fatalf("lepas trigger fault audit: %v", err)
	}
	code, _ = createPrice(t, env.srv, env.wsA, item, env.ownerToken, "afault-ok", priceBody(from, to, "150000", "IDR"))
	if code != http.StatusCreated {
		t.Fatalf("create sesudah pemulihan fault audit: want 201, got %d", code)
	}
}

// TestPriceQuoteFaults: fault izin baca pada quote → 500 (bukan
// 422/404); invarian draft tersimpan yang dilanggar (tahun 10000)
// → 500 terkendali, bukan nilai yang dibungkus.
func TestPriceQuoteFaults(t *testing.T) {
	env := setupPrice(t)
	cust, item := seedAssetRefs(t, env, "SKU-PQFAULT-A")
	base := dbCutoff(t, env.boot)
	seedPrice(t, env, item, "qf-1", base.Add(time.Hour), base.Add(25*time.Hour), "150000", "IDR")
	activateRental(t, env)
	draft, _ := seedDraftForHold(t, env, cust, item,
		base.Add(2*time.Hour).UTC().Format(time.RFC3339Nano),
		base.Add(74*time.Hour).UTC().Format(time.RFC3339Nano))
	if code, _ := getQuote(t, env.srv, env.wsA, draft, env.ownerToken); code != http.StatusOK {
		t.Fatalf("quote sebelum fault: want 200, got %d", code)
	}

	def := installPermissionFault(t, env.boot)
	code, body := getQuote(t, env.srv, env.wsA, draft, env.ownerToken)
	if code != http.StatusInternalServerError {
		t.Fatalf("quote fault view_workspace: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "izin gagal diperiksa")
	restorePermissionFault(t, env.boot, def)
	if code, _ := getQuote(t, env.srv, env.wsA, draft, env.ownerToken); code != http.StatusOK {
		t.Fatalf("quote sesudah pemulihan fault: want 200, got %d", code)
	}

	// Draft tersimpan dengan starts tahun 10000 (fixture bootstrap,
	// di luar rentang yang didukung) → 500, bukan quote yang dibungkus.
	bad := insertDraftFixture(t, env.boot, env.wsA, cust, item, "qf-year10000", "10000-01-01T00:00:00Z", "10000-01-02T00:00:00Z")
	code, body = getQuote(t, env.srv, env.wsA, bad, env.ownerToken)
	if code != http.StatusInternalServerError {
		t.Fatalf("quote draft tahun 10000: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "quote gagal dihitung")
	// Perilaku 422 tetap berbeda dari 500: total overflow pada draft
	// yang sah (bukan fault) sudah dibuktikan TestPriceQuoteArithmetic.
}

// TestPriceFiniteBoundsRerunInstallsOnOld008: tabel 008 LAMA (tanpa
// CHECK finite — keadaan reviewer: 008 sudah diterapkan sebelum CHECK
// ini ada) + baris finite nyata: rerun file 008 BARU lewat psql ASLI
// tanpa -1 (jalur autocommit Linux native) exit 0, memasang constraint
// bernama, dan mempertahankan baris/recorded_at/keamanan/constraint
// lain/marker. Yang diverifikasi definisi hasil, bukan hanya jumlah
// marker — marker 008 lama tidak membuktikan validasi baru ini ada.
func TestPriceFiniteBoundsRerunInstallsOnOld008(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}

	// Cleanup didaftarkan SEBELUM fixture disisipkan.
	cust := "33333333-4444-5555-6666-777777777778"
	item := "33333333-4444-5555-6666-777777777779"
	price1 := "33333333-4444-5555-6666-77777777777a"
	price2 := "33333333-4444-5555-6666-77777777777b"
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM price_versions WHERE id IN ($1, $2)`, price1, price2)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE id = $1`, item)
		_, _ = boot.Exec(`DELETE FROM parties WHERE id = $1`, cust)
		// Jaring pengaman: pastikan CHECK finite ada lagi sesudah tes,
		// apa pun hasil langkah di tengah (abaikan galat).
		_, _ = boot.Exec(`ALTER TABLE price_versions DROP CONSTRAINT IF EXISTS price_versions_finite_bounds, ADD CONSTRAINT price_versions_finite_bounds CHECK (isfinite(valid_from) AND isfinite(valid_to))`)
	})

	env := setupPrice(t)
	ws := env.wsA
	// Keadaan 008 LAMA: tanpa CHECK finite. Satu-satunya perbedaan
	// dari 008 lama yang sesungguhnya adalah riwayat file, bukan
	// definisi tabel — definisi tanpa constraint INILAH keadaan lama.
	if _, err := boot.Exec(`ALTER TABLE price_versions DROP CONSTRAINT IF EXISTS price_versions_finite_bounds`); err != nil {
		t.Fatalf("bangun keadaan 008 lama: %v", err)
	}
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname = 'price_versions_finite_bounds'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("keadaan 008 lama: want 0 CHECK finite, got %d (%v)", n, err)
	}
	if _, err := boot.Exec(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Finite Rerun')`, cust, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-FINITE-RERUN', 'Barang Finite Rerun', 'pcs', 'active')`, item, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		VALUES ($1, $2, $3, 'standard', 'fb-rerun-1', '2026-11-10T02:00:00Z', '2026-12-10T02:00:00Z', '2026-11-07T02:00:00Z', 150000, 'IDR')`, price1, ws, item); err != nil {
		t.Fatalf("fixture price 1: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		VALUES ($1, $2, $3, 'standard', 'fb-rerun-2', '2026-12-10T02:00:00Z', '2027-01-10T02:00:00Z', '2026-11-07T02:00:00Z', 175000, 'IDR')`, price2, ws, item); err != nil {
		t.Fatalf("fixture price 2: %v", err)
	}
	before := priceSnapshot(t, boot)
	if !strings.Contains(before["prices"], "fb-rerun-1") || !strings.Contains(before["prices"], "fb-rerun-2") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture harga: %q", before["prices"])
	}
	if strings.Contains(before["constraints"], "price_versions_finite_bounds") {
		t.Fatalf("snapshot sebelum rerun seharusnya tanpa CHECK finite: %q", before["constraints"])
	}

	code, out := runPsqlFile(t, bootURL, "migrations/008_price_versions.sql")
	if code != 0 {
		t.Fatalf("rerun 008: want exit 0, got %d (%s)", code, out)
	}
	// Definisi hasil: CHECK bernama ada dan memakai isfinite pada
	// kedua batas — bukan hanya jumlah marker.
	var def string
	if err := boot.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'price_versions_finite_bounds' AND contype = 'c'`).Scan(&def); err != nil {
		t.Fatalf("cek CHECK finite sesudah rerun: %v", err)
	}
	if !strings.Contains(def, "isfinite(valid_from)") || !strings.Contains(def, "isfinite(valid_to)") {
		t.Fatalf("price_versions_finite_bounds: want isfinite(valid_from) AND isfinite(valid_to), got %s", def)
	}
	after := priceSnapshot(t, boot)
	// Baris (termasuk recorded_at), kebijakan, ACL, marker tidak
	// berubah; semua constraint lama tetap ada — hanya CHECK finite
	// yang bertambah.
	if before["prices"] != after["prices"] || before["policies"] != after["policies"] || before["acl"] != after["acl"] || before["markers"] != after["markers"] {
		t.Fatalf("rerun mengubah data/keamanan/marker:\nsebelum: %v\nsesudah: %v", before, after)
	}
	beforeSet := map[string]bool{}
	for _, c := range strings.Split(before["constraints"], "\n") {
		beforeSet[c] = true
	}
	afterSet := map[string]bool{}
	for _, c := range strings.Split(after["constraints"], "\n") {
		afterSet[c] = true
	}
	for c := range beforeSet {
		if !afterSet[c] {
			t.Fatalf("rerun menghilangkan constraint %q:\nsebelum: %q\nsesudah: %q", c, before["constraints"], after["constraints"])
		}
	}
	if !afterSet["price_versions:price_versions_finite_bounds:c"] {
		t.Fatalf("rerun tidak memasang CHECK finite: %q", after["constraints"])
	}
	// has_permission tetap berfungsi sesudah rerun.
	var ok bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, env.ownerID).Scan(&ok); err != nil || !ok {
		t.Fatalf("has_permission sesudah rerun: want true, got %v (%v)", ok, err)
	}
}

// TestPriceFiniteBoundsRerunRejectsInvalid: tabel 008 LAMA + satu
// baris nonfinite yang sudah COMMITTED (fixture sintetis terisolasi
// milik tes ini saja): rerun file 008 BARU lewat psql ASLI tanpa -1
// HARUS gagal nonzero dan TIDAK mengubah baris apa pun atau
// menghilangkan constraint yang sudah ada — satu ALTER DROP...ADD
// atomik: ADD gagal validasi, DROP ikut batal. Sesudah baris fixture
// invalid dihapus, rerun valid berhasil dan memasang CHECK finite.
// Transaksi implisit satu Exec driver bukan bukti jalur autocommit
// CLI — tes ini memakai psql asli.
func TestPriceFiniteBoundsRerunRejectsInvalid(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}

	// Cleanup didaftarkan SEBELUM fixture disisipkan.
	cust := "44444444-5555-6666-7777-888888888889"
	item := "44444444-5555-6666-7777-88888888888a"
	priceOK := "44444444-5555-6666-7777-88888888888b"
	priceBad := "44444444-5555-6666-7777-88888888888c"
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM price_versions WHERE id IN ($1, $2)`, priceOK, priceBad)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE id = $1`, item)
		_, _ = boot.Exec(`DELETE FROM parties WHERE id = $1`, cust)
		// Jaring pengaman: pulihkan CHECK finite bila baris invalid
		// sudah tidak ada (abaikan galat bila masih ada).
		_, _ = boot.Exec(`ALTER TABLE price_versions DROP CONSTRAINT IF EXISTS price_versions_finite_bounds, ADD CONSTRAINT price_versions_finite_bounds CHECK (isfinite(valid_from) AND isfinite(valid_to))`)
	})

	env := setupPrice(t)
	ws := env.wsA
	// Keadaan 008 LAMA: tanpa CHECK finite.
	if _, err := boot.Exec(`ALTER TABLE price_versions DROP CONSTRAINT IF EXISTS price_versions_finite_bounds`); err != nil {
		t.Fatalf("bangun keadaan 008 lama: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Finite Invalid')`, cust, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-FINITE-INVALID', 'Barang Finite Invalid', 'pcs', 'active')`, item, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		VALUES ($1, $2, $3, 'standard', 'fb-ok', '2026-11-10T02:00:00Z', '2026-12-10T02:00:00Z', '2026-11-07T02:00:00Z', 150000, 'IDR')`, priceOK, ws, item); err != nil {
		t.Fatalf("fixture price finite: %v", err)
	}
	// Baris nonfinite yang sudah COMMITTED (Exec bootstrap autocommit):
	// valid_to infinity. Hanya fixture tes ini; bukan data reviewer.
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		VALUES ($1, $2, $3, 'standard', 'fb-invalid', '2027-01-01T00:00:00Z', 'infinity', '2026-11-07T02:00:00Z', 150000, 'IDR')`, priceBad, ws, item); err != nil {
		t.Fatalf("fixture price nonfinite: %v", err)
	}
	before := priceSnapshot(t, boot)
	if !strings.Contains(before["prices"], "fb-ok") || !strings.Contains(before["prices"], "fb-invalid") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture harga: %q", before["prices"])
	}
	beforeOK, ok := readPrice(t, boot, ws, priceOK)
	if !ok {
		t.Fatalf("baca baris finite sebelum rerun: tidak ditemukan")
	}

	// Rerun HARUS gagal nonzero — gagal eksplisit untuk review, bukan
	// penghapusan/perbaikan diam-diam atas baris invalid.
	code, out := runPsqlFile(t, bootURL, "migrations/008_price_versions.sql")
	if code == 0 {
		t.Fatalf("rerun 008 dengan baris nonfinite: want exit bukan 0, got 0 (%s)", out)
	}
	t.Logf("rerun 008 dengan baris nonfinite: psql exit %d (diharapkan bukan 0)", code)
	after := priceSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun gagal mengubah snapshot:\nsebelum: %v\nsesudah: %v", before, after)
	}
	// CHECK finite tetap tidak ada (ADD gagal → DROP ikut batal);
	// constraint lain + baris utuh (sudah dibuktikan DeepEqual).
	if strings.Contains(after["constraints"], "price_versions_finite_bounds") {
		t.Fatalf("rerun gagal seharusnya tidak memasang CHECK finite: %q", after["constraints"])
	}

	// Hapus HANYA baris fixture invalid milik tes ini, lalu rerun
	// valid harus berhasil dan memasang CHECK finite.
	if _, err := boot.Exec(`DELETE FROM price_versions WHERE id = $1`, priceBad); err != nil {
		t.Fatalf("hapus baris fixture invalid: %v", err)
	}
	code, out = runPsqlFile(t, bootURL, "migrations/008_price_versions.sql")
	if code != 0 {
		t.Fatalf("rerun valid sesudah hapus fixture: want exit 0, got %d (%s)", code, out)
	}
	var def string
	if err := boot.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'price_versions_finite_bounds' AND contype = 'c'`).Scan(&def); err != nil {
		t.Fatalf("cek CHECK finite sesudah rerun valid: %v", err)
	}
	if !strings.Contains(def, "isfinite(valid_from)") || !strings.Contains(def, "isfinite(valid_to)") {
		t.Fatalf("price_versions_finite_bounds: want isfinite(valid_from) AND isfinite(valid_to), got %s", def)
	}
	// Baris finite tetap ada dengan recorded_at utuh.
	afterOK, ok := readPrice(t, boot, ws, priceOK)
	if !ok || !reflect.DeepEqual(beforeOK, afterOK) {
		t.Fatalf("baris finite sesudah rerun valid: want tidak berubah %v, got %v (ok=%v)", beforeOK, afterOK, ok)
	}
}
