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

// Tes integrasi fakta harga disepakati F2-01-E (migrasi 009) terhadap
// PostgreSQL asli: skema segar + rerun, RLS/hak runtime/FK komposit/
// invarian DB, HTTP accept + bacaan histori, guard Rental,
// konfirmasi expected (mismatch 409), idempotency tiga-field,
// konkurensi, aritmetika eksak, dan fault guard/audit. Entitlement di
// sini HANYA fixture sintetis terisolasi lewat bootstrap (bukan grant
// nyata). Fakta ini adalah catatan staf in-app — BUKAN consent/
// tanda tangan pelanggan, perjanjian hukum, invoice, pembayaran,
// posting ledger, reservasi aset, atau otorisasi handover. Draft
// tidak dimutasi oleh penerimaan; tidak ada hold yang disyaratkan.

// setupAgreedPrice memakai env harga (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris rental_agreed_prices fixture DULU
// (LIFO: cleanup ini didaftarkan sesudah setupPrice, jadi berjalan
// sebelum penghapusan price_versions/draft — urutan FK-aman, karena
// fakta merujuk draft + versi harga + item + pelanggan).
func setupAgreedPrice(t *testing.T) *activationEnv {
	t.Helper()
	env := setupPrice(t)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM rental_agreed_prices WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

func acceptBody(versionID, total string) map[string]any {
	return map[string]any{
		"expected_price_version_id": versionID,
		"expected_total_minor":      total,
	}
}

func acceptPrice(t *testing.T, srv *httptest.Server, ws, draft, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/accept-price", token, []string{key}, body)
}

func acceptPriceRaw(t *testing.T, srv *httptest.Server, ws, draft, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/accept-price", strings.NewReader(raw))
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

func getAgreedPrice(t *testing.T, srv *httptest.Server, ws, draft, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/contracts/"+draft+"/agreed-price", token, nil)
}

// seedAgreedRefs menyiapkan rujukan lengkap lewat API nyata (tanpa
// seeding istimewa): pelanggan + item, aktivasi Rental, satu versi
// harga [base+1j, base+73j) tarif 150000, satu draft [base+2j,
// base+50j) = 48 jam = 2 unit, dan quote-nya. Mengembalikan id
// pelanggan, item, draft, versi harga, dan body quote.
func seedAgreedRefs(t *testing.T, env *activationEnv, sku, keyPrefix string) (custID, itemID, draftID, priceID string, quote map[string]any) {
	t.Helper()
	custID, itemID = seedDraftRefs(t, env, sku)
	activateRental(t, env)
	base := dbCutoff(t, env.boot)
	priceID = seedPrice(t, env, itemID, keyPrefix+"-price", base.Add(1*time.Hour), base.Add(73*time.Hour), "150000", "IDR")
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, keyPrefix+"-draft", draftBody(custID, itemID, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	draftID, _ = body["id"].(string)
	code, quote = getQuote(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("seed quote %s: want 200, got %d (%v)", keyPrefix, code, quote)
	}
	return custID, itemID, draftID, priceID, quote
}

type storedAgreedPrice struct {
	ID, WorkspaceID, RentalContractID, PriceVersionID, PricingRuleVersionID, CatalogItemID, CustomerID, ClientRequestID, AmountMinor, Currency, Units, TotalMinor, StartsAt, EndsAt, AcceptedAt, AcceptedBy string
}

func readAgreedPrice(t *testing.T, boot *sql.DB, ws, id string) (storedAgreedPrice, bool) {
	t.Helper()
	var p storedAgreedPrice
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, price_version_id::text, pricing_rule_version_id,
			catalog_item_id::text, customer_id::text, client_request_id, amount_minor::text, currency, units::text,
			total_minor::text, starts_at::text, ends_at::text, accepted_at::text, accepted_by::text
		 FROM rental_agreed_prices WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PriceVersionID, &p.PricingRuleVersionID,
		&p.CatalogItemID, &p.CustomerID, &p.ClientRequestID, &p.AmountMinor, &p.Currency, &p.Units,
		&p.TotalMinor, &p.StartsAt, &p.EndsAt, &p.AcceptedAt, &p.AcceptedBy)
	if err == sql.ErrNoRows {
		return storedAgreedPrice{}, false
	}
	if err != nil {
		t.Fatalf("read agreed price %s: %v", id, err)
	}
	return p, true
}

// agreedSnapshot menangkap data/kebijakan/ACL/constraint/marker untuk
// uji rerun 009 (pola priceSnapshot F2-01-D).
func agreedSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"facts":       `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || rental_contract_id::text || '|' || price_version_id::text || '|' || pricing_rule_version_id || '|' || catalog_item_id::text || '|' || customer_id::text || '|' || client_request_id || '|' || amount_minor::text || '|' || currency || '|' || units::text || '|' || total_minor::text || '|' || starts_at::text || '|' || ends_at::text || '|' || accepted_at::text || '|' || accepted_by::text, chr(10) ORDER BY id), '') FROM rental_agreed_prices`,
		"policies":    `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('rental_agreed_prices')`,
		"acl":         `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('rental_agreed_prices')`,
		"markers":     `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
		"constraints": `SELECT coalesce(string_agg(conrelid::regclass::text || ':' || conname || ':' || contype::text, chr(10) ORDER BY conrelid::regclass::text, conname), '') FROM pg_constraint WHERE conrelid IN ('rental_agreed_prices'::regclass)`,
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

// TestAgreedPriceFreshSchema: sembilan marker persis, tabel kosong,
// pemilik bootstrap + RLS FORCE, kolom tepat, hak runtime, kebijakan,
// dan invarian CHECK bernama.
func TestAgreedPriceFreshSchema(t *testing.T) {
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
	if err := boot.QueryRow(`SELECT count(*) FROM rental_agreed_prices`).Scan(&n); err != nil {
		t.Fatalf("count rental_agreed_prices: %v", err)
	}
	if n != 0 {
		t.Fatalf("rental_agreed_prices: want 0 baris pada skema segar, got %d", n)
	}
	var owner string
	var super, rls, force bool
	if err := boot.QueryRow(
		`SELECT c.relowner::regrole::text, r.rolsuper, c.relrowsecurity, c.relforcerowsecurity
		 FROM pg_class c JOIN pg_roles r ON r.rolname = c.relowner::regrole::text
		 WHERE c.relname = 'rental_agreed_prices'`).Scan(&owner, &super, &rls, &force); err != nil {
		t.Fatalf("owner/rls rental_agreed_prices: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("rental_agreed_prices dimiliki runtime role; harus dimiliki bootstrap/migrasi")
	}
	if !super {
		t.Fatalf("pemilik rental_agreed_prices (%q) harus superuser (bootstrap/migrasi), got super=%v", owner, super)
	}
	if !rls || !force {
		t.Fatalf("rental_agreed_prices: RLS harus ENABLE+FORCE, got rls=%v force=%v", rls, force)
	}
	// Kolom TEPAT seperti tiket: pricing_rule_version_id ikut, tanpa
	// kolom spekulatif (tanpa created_at/updated_at/status).
	var cols string
	if err := boot.QueryRow(
		`SELECT string_agg(column_name, ',' ORDER BY ordinal_position) FROM information_schema.columns WHERE table_name = 'rental_agreed_prices'`,
	).Scan(&cols); err != nil {
		t.Fatalf("baca kolom rental_agreed_prices: %v", err)
	}
	if cols != "id,workspace_id,rental_contract_id,price_version_id,pricing_rule_version_id,catalog_item_id,customer_id,client_request_id,amount_minor,currency,units,total_minor,starts_at,ends_at,accepted_at,accepted_by" {
		t.Fatalf("kolom rental_agreed_prices: want id,workspace_id,rental_contract_id,price_version_id,pricing_rule_version_id,catalog_item_id,customer_id,client_request_id,amount_minor,currency,units,total_minor,starts_at,ends_at,accepted_at,accepted_by, got %s", cols)
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
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', 'rental_agreed_prices', $1)`, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege rental_agreed_prices %s: %v", p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime rental_agreed_prices %s: want %v, got %v", p.action, p.want, got)
		}
	}
	// Kebijakan: SELECT + INSERT (tepat dua).
	for _, want := range []struct{ policy, cmd string }{
		{"rental_agreed_prices_select", "SELECT"},
		{"rental_agreed_prices_insert", "INSERT"},
	} {
		var m int
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE tablename = 'rental_agreed_prices' AND policyname = $1 AND cmd = $2`,
			want.policy, want.cmd).Scan(&m); err != nil {
			t.Fatalf("cek policy %s: %v", want.policy, err)
		}
		if m != 1 {
			t.Fatalf("policy %s: want 1, got %d", want.policy, m)
		}
	}
	// CHECK 9 (client_request_id, rule, amount>=0, currency, units,
	// total>=0, ends>starts, total numerik, finite times) + UNIQUE 3
	// (draft, key, workspace_id+id) + FK 6 (workspace + 4 komposit +
	// accepted_by) + EXCLUDE 0. UNIQUE (workspace_id, id)
	// ditambahkan 010 sebagai target FK komposit custody.
	var c, u, f, x int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f'), count(*) FILTER (WHERE contype = 'x')
		 FROM pg_constraint WHERE conrelid = 'rental_agreed_prices'::regclass`).Scan(&c, &u, &f, &x); err != nil {
		t.Fatalf("constraints rental_agreed_prices: %v", err)
	}
	if c != 9 || u != 3 || f != 6 || x != 0 {
		t.Fatalf("constraints rental_agreed_prices: want CHECK 9 + UNIQUE 3 + FK 6 + EXCLUDE 0, got CHECK %d + UNIQUE %d + FK %d + EXCLUDE %d", c, u, f, x)
	}
	// CHECK finite bernama: ketiga timestamp harus finite.
	var finiteDef string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_agreed_prices_finite_times' AND contype = 'c'`).Scan(&finiteDef); err != nil {
		t.Fatalf("cek CHECK finite bernama: %v", err)
	}
	if !strings.Contains(finiteDef, "isfinite(starts_at)") || !strings.Contains(finiteDef, "isfinite(ends_at)") || !strings.Contains(finiteDef, "isfinite(accepted_at)") {
		t.Fatalf("rental_agreed_prices_finite_times: want isfinite(starts_at) AND isfinite(ends_at) AND isfinite(accepted_at), got %s", finiteDef)
	}
	// CHECK total memakai cast numerik eksak (bukan perkalian BIGINT
	// yang bisa overflow di tengah).
	var totalDef string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_agreed_prices_total_check' AND contype = 'c'`).Scan(&totalDef); err != nil {
		t.Fatalf("cek CHECK total bernama: %v", err)
	}
	if !strings.Contains(totalDef, "(total_minor)::numeric") || !strings.Contains(totalDef, "(amount_minor)::numeric") || !strings.Contains(totalDef, "(units)::numeric") {
		t.Fatalf("rental_agreed_prices_total_check: want cast numeric atas total/amount/units, got %s", totalDef)
	}
	// CHECK rule: tepat satu identifier yang didukung tiket ini.
	var ruleDef string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_agreed_prices_rule_check' AND contype = 'c'`).Scan(&ruleDef); err != nil {
		t.Fatalf("cek CHECK rule bernama: %v", err)
	}
	if !strings.Contains(ruleDef, "elapsed_24h_ceil_v1") {
		t.Fatalf("rental_agreed_prices_rule_check: want elapsed_24h_ceil_v1, got %s", ruleDef)
	}
	// CHECK units: batas 1-3652059 (rentang yang didukung aritmetika).
	var unitsDef string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_agreed_prices_units_check' AND contype = 'c'`).Scan(&unitsDef); err != nil {
		t.Fatalf("cek CHECK units bernama: %v", err)
	}
	if !strings.Contains(unitsDef, "3652059") {
		t.Fatalf("rental_agreed_prices_units_check: want batas 3652059, got %s", unitsDef)
	}
	// FK komposit ke price_versions butuh UNIQUE(workspace_id, id)
	// pada price_versions — dipasang 009 (pola 006/007).
	if err := boot.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname = 'price_versions_workspace_id_id_key' AND contype = 'u'`).Scan(&n); err != nil {
		t.Fatalf("cek UNIQUE price_versions workspace+id: %v", err)
	}
	if n != 1 {
		t.Fatalf("price_versions_workspace_id_id_key: want 1, got %d", n)
	}
}

// TestAgreedPriceRerunPreservesData: rerun 009 TANPA -1 (jalur
// autocommit psql) mempertahankan baris/kebijakan/ACL/marker.
func TestAgreedPriceRerunPreservesData(t *testing.T) {
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak tersedia")
	}
	env := setupAgreedPrice(t)
	boot := env.boot
	ws := env.wsA

	_, _, draftA, _, quoteA := seedAgreedRefs(t, env, "SKU-RERUN-AG-A", "ag-rerun-a")
	code, body := acceptPrice(t, env.srv, ws, draftA, env.ownerToken, "ag-rerun-a-acc", acceptBody(quoteA["price_version_id"].(string), quoteA["total_minor"].(string)))
	if code != http.StatusCreated {
		t.Fatalf("accept rerun A: want 201, got %d (%v)", code, body)
	}
	factA, _ := body["id"].(string)
	_, _, draftB, _, quoteB := seedAgreedRefs(t, env, "SKU-RERUN-AG-B", "ag-rerun-b")
	code, body = acceptPrice(t, env.srv, ws, draftB, env.ownerToken, "ag-rerun-b-acc", acceptBody(quoteB["price_version_id"].(string), quoteB["total_minor"].(string)))
	if code != http.StatusCreated {
		t.Fatalf("accept rerun B: want 201, got %d (%v)", code, body)
	}
	factB, _ := body["id"].(string)
	rowA, okA := readAgreedPrice(t, boot, ws, factA)
	rowB, okB := readAgreedPrice(t, boot, ws, factB)
	if !okA || !okB {
		t.Fatalf("fakta rerun tidak terbaca: A=%v B=%v", okA, okB)
	}

	before := agreedSnapshot(t, boot)
	code, out := runPsqlFile(t, bootURL, "migrations/009_rental_agreed_prices.sql")
	if code != 0 {
		t.Fatalf("rerun 009: want exit 0, got %d (%s)", code, out)
	}
	after := agreedSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun mengubah snapshot:\nsebelum: %v\nsesudah: %v", before, after)
	}
	afterA, okA := readAgreedPrice(t, boot, ws, factA)
	afterB, okB := readAgreedPrice(t, boot, ws, factB)
	if !okA || !okB || !reflect.DeepEqual(rowA, afterA) || !reflect.DeepEqual(rowB, afterB) {
		t.Fatalf("baris fakta berubah sesudah rerun")
	}
	// has_permission tetap berfungsi sesudah rerun.
	var ok bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, env.ownerID).Scan(&ok); err != nil || !ok {
		t.Fatalf("has_permission sesudah rerun: want true, got %v (%v)", ok, err)
	}
}

// TestAgreedPriceRLSAndPermissions: matriks izin + RLS/FK/CHECK lewat
// koneksi runtime yang sebenarnya (bukan koneksi istimewa).
func TestAgreedPriceRLSAndPermissions(t *testing.T) {
	env := setupAgreedPrice(t)
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
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-AG-A', 'Barang A', 'pcs', 'active') RETURNING id`, wsA).Scan(&itemA); err != nil {
		t.Fatalf("fixture item A: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, wsB).Scan(&custB); err != nil {
		t.Fatalf("fixture party B: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-AG-B', 'Barang B', 'pcs', 'active') RETURNING id`, wsB).Scan(&itemB); err != nil {
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
			t.Fatalf("has_permission %s: want %v, got %v", tc.perm, tc.want, got)
		}
	}

	// Draft + versi harga lewat API nyata (bukan seeding istimewa).
	activateRental(t, env)
	base := dbCutoff(t, boot)
	priceA := seedPrice(t, env, itemA, "rls-ag-price-a", base.Add(1*time.Hour), base.Add(200*time.Hour), "150000", "IDR")
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, wsA, env.ownerToken, "rls-ag-draft-1", draftBody(custA, itemA, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft 1: want 201, got %d (%v)", code, body)
	}
	draft1, _ := body["id"].(string)
	code, body = createDraft(t, env.srv, wsA, env.ownerToken, "rls-ag-draft-2", draftBody(custA, itemA, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft 2: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	code, body = createDraft(t, env.srv, wsA, env.ownerToken, "rls-ag-draft-3", draftBody(custA, itemA, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft 3: want 201, got %d (%v)", code, body)
	}
	draft3, _ := body["id"].(string)

	// Draft + versi harga lintas-tenant (wsB) untuk kasus FK.
	grantEntitlement(t, boot, wsB, "rental.basic", "0.1.0")
	code, body = activateModule(t, env.srv, wsB, "rental.basic", env.otherToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi rental wsB: want 200, got %d (%v)", code, body)
	}
	// Versi harga wsB dibuat eksplisit lewat API wsB (seedPrice
	// selalu memakai wsA + ownerToken, jadi tidak bisa dipakai di
	// sini).
	priceBCode, priceBBody := createPrice(t, env.srv, wsB, itemB, env.otherToken, "rls-ag-price-b", priceBody(base.Add(1*time.Hour), base.Add(200*time.Hour), "150000", "IDR"))
	if priceBCode != http.StatusCreated {
		t.Fatalf("seed price wsB: want 201, got %d (%v)", priceBCode, priceBBody)
	}
	priceB, _ := priceBBody["id"].(string)
	code, body = createDraft(t, env.srv, wsB, env.otherToken, "rls-ag-draft-b", draftBody(custB, itemB, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft wsB: want 201, got %d (%v)", code, body)
	}
	draftB, _ := body["id"].(string)

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
	insertSQL := `INSERT INTO rental_agreed_prices
		(workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id,
		 client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	// Nilai valid bersama: 48 jam = 2 unit x 150000 = 300000.
	valid := func(draft, key string) []any {
		return []any{wsA, draft, priceA, "elapsed_24h_ceil_v1", itemA, custA, key,
			int64(150000), "IDR", int64(2), int64(300000), starts, ends,
			base.UTC().Format(time.RFC3339Nano), env.ownerID}
	}
	// Owner + manager berhasil (draft berbeda, kunci berbeda).
	if code, err := tryInsert(wsA, env.ownerID, insertSQL, valid(draft1, "rls-ag-owner")...); code != "" || err != nil {
		t.Fatalf("INSERT owner: want sukses, got code %q err %v", code, err)
	}
	if code, err := tryInsert(wsA, env.mgrID, insertSQL, valid(draft2, "rls-ag-manager")...); code != "" || err != nil {
		t.Fatalf("INSERT manager: want sukses, got code %q err %v", code, err)
	}
	// Kasir + nonmember ditolak RLS (42501).
	if code, _ := tryInsert(wsA, env.cashID, insertSQL, valid(draft3, "rls-ag-cashier")...); code != "42501" {
		t.Fatalf("INSERT kasir: want 42501, got %q", code)
	}
	if code, _ := tryInsert(wsA, env.otherID, insertSQL, valid(draft3, "rls-ag-other")...); code != "42501" {
		t.Fatalf("INSERT nonmember: want 42501, got %q", code)
	}
	// FK: rujukan hilang/lintas-tenant ditolak 23503.
	randomUUID := "11111111-2222-3333-4444-555555555555"
	args := valid(draft3, "rls-ag-fk-draft")
	args[1] = randomUUID
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, args...); code != "23503" {
		t.Fatalf("INSERT draft hilang: want 23503, got %q", code)
	}
	args = valid(draft3, "rls-ag-fk-draft-b")
	args[1] = draftB
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, args...); code != "23503" {
		t.Fatalf("INSERT draft lintas-tenant: want 23503, got %q", code)
	}
	args = valid(draft3, "rls-ag-fk-price")
	args[2] = priceB
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, args...); code != "23503" {
		t.Fatalf("INSERT versi harga lintas-tenant: want 23503, got %q", code)
	}
	args = valid(draft3, "rls-ag-fk-cust")
	args[5] = custB
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, args...); code != "23503" {
		t.Fatalf("INSERT pelanggan lintas-tenant: want 23503, got %q", code)
	}
	// CHECK: semua kasus berikut harus 23514 (CHECK dievaluasi
	// sebelum UNIQUE, jadi draft/kunci boleh dipakai ulang).
	checkCases := []struct {
		name string
		mut  func(a []any)
	}{
		{"units 0", func(a []any) { a[9] = int64(0) }},
		{"units 3652060", func(a []any) { a[9] = int64(3652060) }},
		{"total tidak cocok", func(a []any) { a[10] = int64(300001) }},
		{"currency huruf kecil", func(a []any) { a[8] = "idr" }},
		{"currency 4 huruf", func(a []any) { a[8] = "IDRR" }},
		{"rule lain", func(a []any) { a[3] = "other_rule_v1" }},
		{"rule kosong", func(a []any) { a[3] = "" }},
		{"starts_at infinity", func(a []any) { a[11] = "infinity" }},
		{"ends_at infinity", func(a []any) { a[12] = "infinity" }},
		{"accepted_at infinity", func(a []any) { a[13] = "infinity" }},
		{"ends sama dengan starts", func(a []any) { a[12] = starts }},
		{"amount negatif", func(a []any) { a[7] = int64(-1) }},
		{"total negatif", func(a []any) { a[10] = int64(-1) }},
		{"kunci kosong", func(a []any) { a[6] = "" }},
		{"kunci berisi spasi", func(a []any) { a[6] = "bad key" }},
	}
	for i, tc := range checkCases {
		args := valid(draft3, fmt.Sprintf("rls-ag-chk-%d", i))
		tc.mut(args)
		if code, _ := tryInsert(wsA, env.ownerID, insertSQL, args...); code != "23514" {
			t.Fatalf("CHECK %s: want 23514, got %q", tc.name, code)
		}
	}
	// Duplikat: draft yang sama (kunci baru) dan kunci yang sama
	// (draft lain) sama-sama 23505.
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, valid(draft1, "rls-ag-dup-draft")...); code != "23505" {
		t.Fatalf("INSERT draft duplikat: want 23505, got %q", code)
	}
	if code, _ := tryInsert(wsA, env.ownerID, insertSQL, valid(draft3, "rls-ag-owner")...); code != "23505" {
		t.Fatalf("INSERT kunci duplikat: want 23505, got %q", code)
	}
	// Runtime UPDATE/DELETE/TRUNCATE ditolak (42501).
	if code, _ := tryInsert(wsA, env.ownerID, `UPDATE rental_agreed_prices SET total_minor = 1 WHERE workspace_id = $1`, wsA); code != "42501" {
		t.Fatalf("UPDATE runtime: want 42501, got %q", code)
	}
	if code, _ := tryInsert(wsA, env.ownerID, `DELETE FROM rental_agreed_prices WHERE workspace_id = $1`, wsA); code != "42501" {
		t.Fatalf("DELETE runtime: want 42501, got %q", code)
	}
	if code, _ := tryInsert(wsA, env.ownerID, `TRUNCATE rental_agreed_prices`); code != "42501" {
		t.Fatalf("TRUNCATE runtime: want 42501, got %q", code)
	}
	// Hanya dua fakta valid yang bertahan (owner + manager).
	if got := countRows(t, boot, "rental_agreed_prices", wsA); got != 2 {
		t.Fatalf("fakta wsA: want 2, got %d", got)
	}
}

// TestAgreedPriceHTTP: auth/validasi bentuk/header/draft lintas-tenant.
func TestAgreedPriceHTTP(t *testing.T) {
	env := setupAgreedPrice(t)
	_, _, draft, _, quote := seedAgreedRefs(t, env, "SKU-HTTP-AG", "ag-http")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	wsA := env.wsA

	// POST: unauth, UUID buruk, header buruk.
	if code, _ := acceptPriceRaw(t, env.srv, wsA, draft, "", []string{"ag-http-1"}, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("accept unauth: want 401, got %d", code)
	}
	if code, _ := acceptPriceRaw(t, env.srv, "bukan-uuid", draft, env.ownerToken, []string{"ag-http-1"}, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accept ws buruk: want 400, got %d", code)
	}
	if code, _ := acceptPriceRaw(t, env.srv, wsA, "bukan-uuid", env.ownerToken, []string{"ag-http-1"}, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accept draft buruk: want 400, got %d", code)
	}
	if code, _ := acceptPriceRaw(t, env.srv, wsA, draft, env.ownerToken, nil, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accept tanpa kunci: want 400, got %d", code)
	}
	if code, _ := acceptPriceRaw(t, env.srv, wsA, draft, env.ownerToken, []string{"ag-http-1", "ag-http-2"}, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accept dua kunci: want 400, got %d", code)
	}
	if code, _ := acceptPriceRaw(t, env.srv, wsA, draft, env.ownerToken, []string{"bad key"}, `{"expected_price_version_id":"`+version+`","expected_total_minor":"`+total+`"}`); code != http.StatusBadRequest {
		t.Fatalf("accept kunci tidak valid: want 400, got %d", code)
	}
	// POST: body buruk (semua 400).
	badBodies := []struct {
		name string
		raw  string
	}{
		{"field tak dikenal", `{"expected_price_version_id":"` + version + `","expected_total_minor":"` + total + `","extra":1}`},
		{"total JSON number", `{"expected_price_version_id":"` + version + `","expected_total_minor":300000}`},
		{"versi hilang", `{"expected_total_minor":"300000"}`},
		{"total hilang", `{"expected_price_version_id":"` + version + `"}`},
		{"versi bukan UUID", `{"expected_price_version_id":"bukan-uuid","expected_total_minor":"300000"}`},
		{"total leading zero", `{"expected_price_version_id":"` + version + `","expected_total_minor":"0300000"}`},
		{"total negatif", `{"expected_price_version_id":"` + version + `","expected_total_minor":"-1"}`},
		{"total plus", `{"expected_price_version_id":"` + version + `","expected_total_minor":"+300000"}`},
		{"total fraksi", `{"expected_price_version_id":"` + version + `","expected_total_minor":"300000.0"}`},
		{"total eksponen", `{"expected_price_version_id":"` + version + `","expected_total_minor":"3e5"}`},
		{"total kosong", `{"expected_price_version_id":"` + version + `","expected_total_minor":""}`},
		{"total spasi", `{"expected_price_version_id":"` + version + `","expected_total_minor":" 300000"}`},
		{"total null", `{"expected_price_version_id":"` + version + `","expected_total_minor":null}`},
		{"dokumen ganda", `{"expected_price_version_id":"` + version + `","expected_total_minor":"` + total + `"} {}`},
	}
	for _, tc := range badBodies {
		if code, body := acceptPriceRaw(t, env.srv, wsA, draft, env.ownerToken, []string{"ag-http-bad"}, tc.raw); code != http.StatusBadRequest {
			t.Fatalf("accept body %s: want 400, got %d (%v)", tc.name, code, body)
		}
	}
	// Draft lintas-tenant + draft hilang → 404 identik.
	var custB, itemB string
	if err := env.boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, env.wsB).Scan(&custB); err != nil {
		t.Fatalf("fixture party B: %v", err)
	}
	if err := env.boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-HTTP-AG-B', 'Barang B', 'pcs', 'active') RETURNING id`, env.wsB).Scan(&itemB); err != nil {
		t.Fatalf("fixture item B: %v", err)
	}
	grantEntitlement(t, env.boot, env.wsB, "rental.basic", "0.1.0")
	if code, body := activateModule(t, env.srv, env.wsB, "rental.basic", env.otherToken, "0.1.0"); code != http.StatusOK {
		t.Fatalf("aktivasi rental wsB: want 200, got %d (%v)", code, body)
	}
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsB, env.otherToken, "ag-http-draft-b", draftBody(custB, itemB, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft wsB: want 201, got %d (%v)", code, body)
	}
	draftB, _ := body["id"].(string)
	code, body = acceptPrice(t, env.srv, wsA, draftB, env.ownerToken, "ag-http-cross", acceptBody(version, total))
	if code != http.StatusNotFound {
		t.Fatalf("accept draft lintas-tenant: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "draft tidak ditemukan")
	if code, body := acceptPrice(t, env.srv, wsA, "11111111-2222-3333-4444-555555555555", env.ownerToken, "ag-http-missing", acceptBody(version, total)); code != http.StatusNotFound {
		t.Fatalf("accept draft hilang: want 404, got %d (%v)", code, body)
	}
	// Peran: kasir + nonmember ditolak 403.
	if code, _ := acceptPrice(t, env.srv, wsA, draft, env.cashToken, "ag-http-cash", acceptBody(version, total)); code != http.StatusForbidden {
		t.Fatalf("accept kasir: want 403, got %d", code)
	}
	if code, _ := acceptPrice(t, env.srv, wsA, draft, env.otherToken, "ag-http-other", acceptBody(version, total)); code != http.StatusForbidden {
		t.Fatalf("accept nonmember: want 403, got %d", code)
	}
	// GET sebelum ada fakta: unauth 401, UUID buruk 400, lintas/hilang
	// 404, draft own tanpa fakta 404 spesifik, nonmember 403.
	if code, _ := getAgreedPrice(t, env.srv, wsA, draft, ""); code != http.StatusUnauthorized {
		t.Fatalf("GET unauth: want 401, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, "bukan-uuid", draft, env.ownerToken); code != http.StatusBadRequest {
		t.Fatalf("GET ws buruk: want 400, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, "bukan-uuid", env.ownerToken); code != http.StatusBadRequest {
		t.Fatalf("GET draft buruk: want 400, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, draftB, env.ownerToken); code != http.StatusNotFound {
		t.Fatalf("GET draft lintas-tenant: want 404, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, "11111111-2222-3333-4444-555555555555", env.ownerToken); code != http.StatusNotFound {
		t.Fatalf("GET draft hilang: want 404, got %d", code)
	}
	if code, body := getAgreedPrice(t, env.srv, wsA, draft, env.ownerToken); code != http.StatusNotFound {
		t.Fatalf("GET tanpa fakta: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "harga yang disepakati tidak ditemukan")
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, draft, env.cashToken); code != http.StatusNotFound {
		t.Fatalf("GET kasir tanpa fakta: want 404, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, draft, env.otherToken); code != http.StatusForbidden {
		t.Fatalf("GET nonmember: want 403, got %d", code)
	}
	// Sesudah accept: GET kasir 200, nonmember tetap 403.
	if code, body := acceptPrice(t, env.srv, wsA, draft, env.ownerToken, "ag-http-ok", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept ok: want 201, got %d (%v)", code, body)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, draft, env.cashToken); code != http.StatusOK {
		t.Fatalf("GET kasir sesudah fakta: want 200, got %d", code)
	}
	if code, _ := getAgreedPrice(t, env.srv, wsA, draft, env.otherToken); code != http.StatusForbidden {
		t.Fatalf("GET nonmember sesudah fakta: want 403, got %d", code)
	}
}

// TestAgreedPriceAccept: happy path lewat API nyata — DTO fakta sama
// dengan quote yang dilihat, baris DB cocok, satu audit.
func TestAgreedPriceAccept(t *testing.T) {
	env := setupAgreedPrice(t)
	_, _, draft, _, quote := seedAgreedRefs(t, env, "SKU-ACC-AG", "ag-acc")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	amount, _ := quote["amount_minor"].(string)
	currency, _ := quote["currency"].(string)
	units, _ := quote["units"].(float64)

	code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-acc-1", acceptBody(version, total))
	if code != http.StatusCreated {
		t.Fatalf("accept: want 201, got %d (%v)", code, body)
	}
	if body["rental_contract_id"] != draft || body["price_version_id"] != version ||
		body["pricing_rule_version_id"] != "elapsed_24h_ceil_v1" ||
		body["amount_minor"] != amount || body["currency"] != currency ||
		body["units"] != units || body["total_minor"] != total ||
		body["client_request_id"] != "ag-acc-1" || body["accepted_by"] != env.ownerID {
		t.Fatalf("DTO fakta tidak cocok dengan quote: %v", body)
	}
	if body["starts_at"] != quote["starts_at"] || body["ends_at"] != quote["ends_at"] {
		t.Fatalf("periode fakta tidak cocok dengan quote: %v vs %v", body, quote)
	}
	if _, err := time.Parse(time.RFC3339Nano, body["accepted_at"].(string)); err != nil {
		t.Fatalf("accepted_at bukan RFC3339Nano: %v", body["accepted_at"])
	}
	code, getBody := getAgreedPrice(t, env.srv, env.wsA, draft, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET fakta: want 200, got %d (%v)", code, getBody)
	}
	if !reflect.DeepEqual(body, getBody) {
		t.Fatalf("GET tidak sama dengan POST:\nPOST: %v\nGET: %v", body, getBody)
	}
	factID, _ := body["id"].(string)
	row, ok := readAgreedPrice(t, env.boot, env.wsA, factID)
	if !ok {
		t.Fatalf("baris fakta tidak terbaca di DB")
	}
	if row.RentalContractID != draft || row.PriceVersionID != version ||
		row.PricingRuleVersionID != "elapsed_24h_ceil_v1" || row.AmountMinor != amount ||
		row.Currency != currency || row.Units != strconv.FormatInt(int64(units), 10) ||
		row.TotalMinor != total || row.ClientRequestID != "ag-acc-1" || row.AcceptedBy != env.ownerID {
		t.Fatalf("baris DB tidak cocok dengan DTO: %+v", row)
	}
	for _, tc := range []struct{ db, api string }{
		{row.StartsAt, body["starts_at"].(string)},
		{row.EndsAt, body["ends_at"].(string)},
		{row.AcceptedAt, body["accepted_at"].(string)},
	} {
		dbT, err1 := time.Parse("2006-01-02 15:04:05.999999-07", tc.db)
		apiT, err2 := time.Parse(time.RFC3339Nano, tc.api)
		if err1 != nil || err2 != nil || !dbT.Equal(apiT) {
			t.Fatalf("timestamp DB vs API tidak sama: %q vs %q (%v %v)", tc.db, tc.api, err1, err2)
		}
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 1 {
		t.Fatalf("audit: want 1, got %d", got)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 1 {
		t.Fatalf("fakta wsA: want 1, got %d", got)
	}
}

// TestAgreedPriceExpectedMismatch: expected yang salah → 409 tanpa
// baris/audit; expected yang benar sesudahnya → 201.
func TestAgreedPriceExpectedMismatch(t *testing.T) {
	env := setupAgreedPrice(t)
	_, item, draft, _, quote := seedAgreedRefs(t, env, "SKU-MM-AG", "ag-mm")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	base := dbCutoff(t, env.boot)

	// Versi lain yang terdaftar (v2 adjacent, untuk starts nanti):
	// resolve tetap v1, jadi expected v2 → 409.
	v2 := seedPrice(t, env, item, "ag-mm-v2", base.Add(73*time.Hour), base.Add(145*time.Hour), "175000", "IDR")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-mm-wrong-v", acceptBody(v2, total)); code != http.StatusConflict {
		t.Fatalf("accept versi lain: want 409, got %d (%v)", code, body)
	}
	// Total diubah → 409.
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-mm-wrong-t", acceptBody(version, "300001")); code != http.StatusConflict {
		t.Fatalf("accept total diubah: want 409, got %d (%v)", code, body)
	}
	// Versi dari draft/item lain → 409.
	_, _, _, _, quoteB := seedAgreedRefs(t, env, "SKU-MM-AG-B", "ag-mm-b")
	versionB, _ := quoteB["price_version_id"].(string)
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-mm-wrong-d", acceptBody(versionB, total)); code != http.StatusConflict {
		t.Fatalf("accept versi draft lain: want 409, got %d (%v)", code, body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 0 {
		t.Fatalf("mismatch: want 0 fakta, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 0 {
		t.Fatalf("mismatch: want 0 audit, got %d", got)
	}
	// Expected yang benar → 201.
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-mm-ok", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept benar: want 201, got %d (%v)", code, body)
	}
}

// TestAgreedPriceIdempotency: replay 200 fakta asli; kunci sama untuk
// draft lain / expected lain → 409; kunci lain untuk draft yang sudah
// punya fakta → 409.
func TestAgreedPriceIdempotency(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft, _, quote := seedAgreedRefs(t, env, "SKU-IDEM-AG", "ag-idem")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)

	code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-idem-1", acceptBody(version, total))
	if code != http.StatusCreated {
		t.Fatalf("accept pertama: want 201, got %d (%v)", code, body)
	}
	code, replay := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-idem-1", acceptBody(version, total))
	if code != http.StatusOK {
		t.Fatalf("replay: want 200, got %d (%v)", code, replay)
	}
	if !reflect.DeepEqual(body, replay) {
		t.Fatalf("replay tidak identik:\npertama: %v\nreplay: %v", body, replay)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 1 {
		t.Fatalf("replay: want 1 audit, got %d", got)
	}
	// Draft kedua, periode + harga sama persis (versi + total sama):
	// kunci yang sama tetap 409 karena draft_id berbeda — identitas
	// replay mencakup KETIGA field (draft, versi, total).
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body2 := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-idem-draft-2", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft kedua: want 201, got %d (%v)", code, body2)
	}
	draft2, _ := body2["id"].(string)
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-idem-1", acceptBody(version, total)); code != http.StatusConflict {
		t.Fatalf("kunci sama draft lain: want 409, got %d (%v)", code, body)
	}
	// Kunci sama + expected beda → 409.
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-idem-1", acceptBody(version, "300001")); code != http.StatusConflict {
		t.Fatalf("kunci sama expected beda: want 409, got %d (%v)", code, body)
	}
	// Kunci lain + draft yang sudah punya fakta → 409.
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-idem-2", acceptBody(version, total)); code != http.StatusConflict {
		t.Fatalf("kunci lain draft sudah disepakati: want 409, got %d (%v)", code, body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 1 {
		t.Fatalf("idempotency: want 1 fakta, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 1 {
		t.Fatalf("idempotency: want 1 audit, got %d", got)
	}
	code, getBody := getAgreedPrice(t, env.srv, env.wsA, draft, env.ownerToken)
	if code != http.StatusOK || !reflect.DeepEqual(body, getBody) {
		t.Fatalf("GET sesudah replay: want 200 + fakta asli, got %d (%v)", code, getBody)
	}
}

// TestAgreedPriceConcurrent: kunci sama 201+200 satu fakta/audit;
// kunci beda draft sama 201+409.
func TestAgreedPriceConcurrent(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft, _, quote := seedAgreedRefs(t, env, "SKU-CONC-AG", "ag-conc")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	path := "/workspaces/" + env.wsA + "/rental/contracts/" + draft + "/accept-price"

	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", path, env.ownerToken, []string{"ag-conc-1"}, acceptBody(version, total))
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", path, env.ownerToken, []string{"ag-conc-1"}, acceptBody(version, total))
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{200, 201}) {
		t.Fatalf("konkuren kunci sama: want [200 201], got %v", codes)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 1 {
		t.Fatalf("konkuren kunci sama: want 1 fakta, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 1 {
		t.Fatalf("konkuren kunci sama: want 1 audit, got %d", got)
	}
	// Draft kedua, kunci berbeda paralel → 201 + 409.
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-conc-draft-2", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	path2 := "/workspaces/" + env.wsA + "/rental/contracts/" + draft2 + "/accept-price"
	codes, _ = parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", path2, env.ownerToken, []string{"ag-conc-2a"}, acceptBody(version, total))
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", path2, env.ownerToken, []string{"ag-conc-2b"}, acceptBody(version, total))
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{201, 409}) {
		t.Fatalf("konkuren kunci beda: want [201 409], got %v", codes)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 2 {
		t.Fatalf("konkuren kunci beda: want 2 fakta, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded"); got != 2 {
		t.Fatalf("konkuren kunci beda: want 2 audit, got %d", got)
	}
}

// TestAgreedPriceNoHoldNeeded: penerimaan TIDAK butuh hold — tanpa
// hold, sesudah release, dan sesudah hold kedaluwarsa.
func TestAgreedPriceNoHoldNeeded(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft1, _, quote := seedAgreedRefs(t, env, "SKU-NOHOLD-AG", "ag-nohold")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)

	// Tanpa hold sama sekali → 201.
	if code, body := acceptPrice(t, env.srv, env.wsA, draft1, env.ownerToken, "ag-nohold-1", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept tanpa hold: want 201, got %d (%v)", code, body)
	}
	// Draft kedua: hold dibuat lalu dirilis → accept tetap 201.
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-nohold-draft-2", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	asset := seedAsset(t, env, item, "SN-NOHOLD-2")
	holdID, _ := seedHold(t, env, draft2, asset, "ag-nohold-hold-2")
	if code, body := releaseHold(t, env.srv, env.wsA, holdID, env.ownerToken); code != http.StatusOK {
		t.Fatalf("release hold: want 200, got %d (%v)", code, body)
	}
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-nohold-2", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah release: want 201, got %d (%v)", code, body)
	}
	// Draft ketiga: hold dibuat lalu kedaluwarsa (fixture) → 201.
	code, body = createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-nohold-draft-3", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft ketiga: want 201, got %d (%v)", code, body)
	}
	draft3, _ := body["id"].(string)
	asset3 := seedAsset(t, env, item, "SN-NOHOLD-3")
	hold3, _ := seedHold(t, env, draft3, asset3, "ag-nohold-hold-3")
	expireHoldFixture(t, env.boot, hold3, "-1 hour")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft3, env.ownerToken, "ag-nohold-3", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah hold kedaluwarsa: want 201, got %d (%v)", code, body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 3 {
		t.Fatalf("no-hold: want 3 fakta, got %d", got)
	}
}

// TestAgreedPriceHistory: v2 adjacent untuk starts nanti TIDAK
// mengubah fakta lama; draft baru yang mulai di v2 memakai v2.
func TestAgreedPriceHistory(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft1, price1, quote := seedAgreedRefs(t, env, "SKU-HIST-AG", "ag-hist")
	version1, _ := quote["price_version_id"].(string)
	total1, _ := quote["total_minor"].(string)
	if version1 != price1 {
		t.Fatalf("quote bukan v1: got %s want %s", version1, price1)
	}
	code, body1 := acceptPrice(t, env.srv, env.wsA, draft1, env.ownerToken, "ag-hist-1", acceptBody(version1, total1))
	if code != http.StatusCreated {
		t.Fatalf("accept v1: want 201, got %d (%v)", code, body1)
	}
	row1, ok := readPrice(t, env.boot, env.wsA, price1)
	if !ok {
		t.Fatalf("v1 tidak terbaca")
	}
	// Interval v2 yang berdekatan, untuk starts yang lebih lambat.
	base := dbCutoff(t, env.boot)
	v2 := seedPrice(t, env, item, "ag-hist-v2", base.Add(73*time.Hour), base.Add(145*time.Hour), "175000", "IDR")
	// Fakta lama tidak berubah.
	code, getBody := getAgreedPrice(t, env.srv, env.wsA, draft1, env.ownerToken)
	if code != http.StatusOK || !reflect.DeepEqual(body1, getBody) {
		t.Fatalf("fakta lama berubah sesudah v2: got %d (%v)", code, getBody)
	}
	row1After, ok := readPrice(t, env.boot, env.wsA, price1)
	if !ok || !reflect.DeepEqual(row1, row1After) {
		t.Fatalf("baris v1 berubah sesudah v2 dijadwalkan")
	}
	// Draft baru yang mulai di v2: 26 jam = 2 unit x 175000 = 350000.
	starts2 := base.Add(74 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends2 := base.Add(100 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-hist-draft-2", draftBody(cust, item, starts2, ends2))
	if code != http.StatusCreated {
		t.Fatalf("seed draft v2: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	code, quote2 := getQuote(t, env.srv, env.wsA, draft2, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("quote draft v2: want 200, got %d (%v)", code, quote2)
	}
	if quote2["price_version_id"] != v2 || quote2["total_minor"] != "350000" {
		t.Fatalf("quote draft v2: want v2 + 350000, got %v", quote2)
	}
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-hist-2", acceptBody(v2, "350000")); code != http.StatusCreated {
		t.Fatalf("accept v2: want 201, got %d (%v)", code, body)
	} else if body["price_version_id"] != v2 || body["total_minor"] != "350000" {
		t.Fatalf("fakta v2 salah: %v", body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 2 {
		t.Fatalf("history: want 2 fakta, got %d", got)
	}
}

// TestAgreedPriceQuoteArithmetic: 24 jam / 24 jam+1µs / 49 jam / nol /
// MaxInt64 / overflow 422 / rentang maksimum sintetis / draft tahun
// 10000 → 500.
func TestAgreedPriceQuoteArithmetic(t *testing.T) {
	env := setupAgreedPrice(t)
	activateRental(t, env)
	base := dbCutoff(t, env.boot)

	acceptCase := func(sku, keyPrefix, amount string, draftFrom, draftTo time.Time, wantTotal string) (int, map[string]any) {
		t.Helper()
		cust, item := seedDraftRefs(t, env, sku)
		seedPrice(t, env, item, keyPrefix+"-price", base.Add(1*time.Hour), base.Add(200*time.Hour), amount, "IDR")
		starts := draftFrom.UTC().Format(time.RFC3339Nano)
		ends := draftTo.UTC().Format(time.RFC3339Nano)
		code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, keyPrefix+"-draft", draftBody(cust, item, starts, ends))
		if code != http.StatusCreated {
			t.Fatalf("seed draft %s: want 201, got %d (%v)", keyPrefix, code, body)
		}
		draft, _ := body["id"].(string)
		code, quote := getQuote(t, env.srv, env.wsA, draft, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("quote %s: want 200, got %d (%v)", keyPrefix, code, quote)
		}
		version, _ := quote["price_version_id"].(string)
		if quote["total_minor"] != wantTotal {
			t.Fatalf("quote %s: want total %s, got %v", keyPrefix, wantTotal, quote["total_minor"])
		}
		return acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, keyPrefix+"-acc", acceptBody(version, wantTotal))
	}
	// 24 jam → 1 unit.
	if code, body := acceptCase("SKU-AR-24H", "ag-ar-24h", "150000", base.Add(2*time.Hour), base.Add(26*time.Hour), "150000"); code != http.StatusCreated {
		t.Fatalf("24 jam: want 201, got %d (%v)", code, body)
	} else if body["units"] != float64(1) {
		t.Fatalf("24 jam: want 1 unit, got %v", body["units"])
	}
	// 24 jam + 1 mikrodetik → 2 unit.
	if code, body := acceptCase("SKU-AR-24H1US", "ag-ar-24h1us", "150000", base.Add(2*time.Hour), base.Add(26*time.Hour).Add(1*time.Microsecond), "300000"); code != http.StatusCreated {
		t.Fatalf("24 jam+1µs: want 201, got %d (%v)", code, body)
	} else if body["units"] != float64(2) {
		t.Fatalf("24 jam+1µs: want 2 unit, got %v", body["units"])
	}
	// 49 jam → 3 unit.
	if code, body := acceptCase("SKU-AR-49H", "ag-ar-49h", "150000", base.Add(2*time.Hour), base.Add(51*time.Hour), "450000"); code != http.StatusCreated {
		t.Fatalf("49 jam: want 201, got %d (%v)", code, body)
	} else if body["units"] != float64(3) {
		t.Fatalf("49 jam: want 3 unit, got %v", body["units"])
	}
	// Tarif nol → total "0".
	if code, body := acceptCase("SKU-AR-ZERO", "ag-ar-zero", "0", base.Add(2*time.Hour), base.Add(26*time.Hour), "0"); code != http.StatusCreated {
		t.Fatalf("tarif nol: want 201, got %d (%v)", code, body)
	} else if body["total_minor"] != "0" {
		t.Fatalf("tarif nol: want total 0, got %v", body["total_minor"])
	}
	// MaxInt64 x 1 unit → total MaxInt64.
	if code, body := acceptCase("SKU-AR-MAX1", "ag-ar-max1", "9223372036854775807", base.Add(2*time.Hour), base.Add(26*time.Hour), "9223372036854775807"); code != http.StatusCreated {
		t.Fatalf("MaxInt64 1 unit: want 201, got %d (%v)", code, body)
	}
	// MaxInt64 x 2 unit → 422 (bukan 201, bukan 500). Quote sendiri
	// 422 dan tidak membawa price_version_id, jadi expected memakai
	// id versi yang dibuat seedPrice di atas.
	cust, item := seedDraftRefs(t, env, "SKU-AR-OVERFLOW")
	ovfPrice := seedPrice(t, env, item, "ag-ar-ovf-price", base.Add(1*time.Hour), base.Add(200*time.Hour), "9223372036854775807", "IDR")
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-ar-ovf-draft", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft overflow: want 201, got %d (%v)", code, body)
	}
	draft, _ := body["id"].(string)
	code, quote := getQuote(t, env.srv, env.wsA, draft, env.ownerToken)
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("quote overflow: want 422, got %d (%v)", code, quote)
	}
	code, body = acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-ar-ovf-acc", acceptBody(ovfPrice, "9223372036854775807"))
	if code != http.StatusUnprocessableEntity {
		t.Fatalf("accept overflow: want 422, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "total di luar batas yang dapat diwakili")
	// Rentang maksimum sintetis (fixture bootstrap, BUKAN happy path
	// API no-backdate): 0001-01-01 s.d. 9999-12-31T23:59:59.999999Z =
	// 3652059 unit x 150000 = 547808850000.
	cust, item = seedDraftRefs(t, env, "SKU-AR-MAXRANGE")
	priceID := insertPriceFixture(t, env.boot, env.wsA, item, "ag-ar-maxrange-price", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z", "150000", "IDR")
	draft = insertDraftFixture(t, env.boot, env.wsA, cust, item, "ag-ar-maxrange-draft", "0001-01-01T00:00:00Z", "9999-12-31T23:59:59.999999Z")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-ar-maxrange-acc", acceptBody(priceID, "547808850000")); code != http.StatusCreated {
		t.Fatalf("rentang maksimum: want 201, got %d (%v)", code, body)
	} else if body["units"] != float64(3652059) || body["total_minor"] != "547808850000" {
		t.Fatalf("rentang maksimum: want 3652059 unit + 547808850000, got %v", body)
	}
	// Draft tersimpan tahun 10000 → 500 (invarian rusak), bukan
	// pembungkusan tahun.
	draft = insertDraftFixture(t, env.boot, env.wsA, cust, item, "ag-ar-y10000-draft", "10000-01-01T00:00:00Z", "10000-01-02T00:00:00Z")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-ar-y10000-acc", acceptBody(priceID, "150000")); code != http.StatusInternalServerError {
		t.Fatalf("draft tahun 10000: want 500, got %d (%v)", code, body)
	}
}

// TestAgreedPriceGuardDenials: nonaktif/dicabut → 403 untuk terima;
// GET histori tetap 200; grant/aktivasi ulang → 201.
func TestAgreedPriceGuardDenials(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft1, _, quote := seedAgreedRefs(t, env, "SKU-GD-AG", "ag-gd")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-gd-draft-2", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)

	if code, body := acceptPrice(t, env.srv, env.wsA, draft1, env.ownerToken, "ag-gd-1", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept pertama: want 201, got %d (%v)", code, body)
	}
	// Nonaktifkan Rental → terima draft kedua 403; GET fakta tetap 200.
	if code, body := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, body)
	}
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-gd-2", acceptBody(version, total)); code != http.StatusForbidden {
		t.Fatalf("accept sesudah deactivate: want 403, got %d (%v)", code, body)
	}
	if code, _ := getAgreedPrice(t, env.srv, env.wsA, draft1, env.ownerToken); code != http.StatusOK {
		t.Fatalf("GET sesudah deactivate: want 200, got %d", code)
	}
	// Aktivasi ulang, lalu cabut entitlement → 403; GET tetap 200.
	if code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0"); code != http.StatusOK {
		t.Fatalf("reactivate: want 200, got %d (%v)", code, body)
	}
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-gd-3", acceptBody(version, total)); code != http.StatusForbidden {
		t.Fatalf("accept sesudah revoke: want 403, got %d (%v)", code, body)
	}
	if code, _ := getAgreedPrice(t, env.srv, env.wsA, draft1, env.ownerToken); code != http.StatusOK {
		t.Fatalf("GET sesudah revoke: want 200, got %d", code)
	}
	// Grant ulang → terima berhasil.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-gd-4", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah re-grant: want 201, got %d (%v)", code, body)
	}
}

// TestAgreedPriceGuardFaults: galat query izin/insert → 500 tanpa
// fakta/audit parsial; sesudah restore → sukses.
func TestAgreedPriceGuardFaults(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draft, _, quote := seedAgreedRefs(t, env, "SKU-GF-AG", "ag-gf")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)

	// Fault has_permission manage_business_data → 500. Restore
	// didaftarkan di t.Cleanup JUGA supaya kegagalan di tengah tes
	// tidak meninggalkan fault di database (restorePermissionFault
	// idempotent: menjalankan ulang definisi asli).
	def := installPermissionFaultPerm(t, env.boot, "manage_business_data")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-gf-1", acceptBody(version, total))
	if code != http.StatusInternalServerError {
		t.Fatalf("accept fault manage_business_data: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "izin gagal diperiksa")
	restorePermissionFault(t, env.boot, def)
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-gf-1", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah restore manage: want 201, got %d (%v)", code, body)
	}

	// Fault has_permission view_workspace → 500 (draft kedua, karena
	// draft pertama sudah punya fakta). Draft kedua memakai item yang
	// sama (sudah punya versi harga) supaya sesudah restore bisa 201.
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body = createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-gf-draft-2", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	def = installPermissionFaultPerm(t, env.boot, "view_workspace")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, body = acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-gf-2", acceptBody(version, total))
	if code != http.StatusInternalServerError {
		t.Fatalf("accept fault view_workspace: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "izin gagal diperiksa")
	restorePermissionFault(t, env.boot, def)
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "ag-gf-2", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah restore view: want 201, got %d (%v)", code, body)
	}

	// Fault INSERT fakta → 500 tanpa fakta/audit; sesudah lepas → 201.
	_, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_agreed_insert() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'synthetic agreed insert failure';
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatalf("buat fungsi fault insert: %v", err)
	}
	_, err = env.boot.Exec(`CREATE TRIGGER qa_fault_agreed_insert BEFORE INSERT ON rental_agreed_prices FOR EACH ROW EXECUTE FUNCTION app.qa_fail_agreed_insert()`)
	if err != nil {
		t.Fatalf("pasang trigger fault insert: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_agreed_insert ON rental_agreed_prices`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_agreed_insert()`)
	})
	code, body = createDraft(t, env.srv, env.wsA, env.ownerToken, "ag-gf-draft-3", draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft ketiga: want 201, got %d (%v)", code, body)
	}
	draft3, _ := body["id"].(string)
	code, body = acceptPrice(t, env.srv, env.wsA, draft3, env.ownerToken, "ag-gf-3", acceptBody(version, total))
	if code != http.StatusInternalServerError {
		t.Fatalf("accept fault insert: want 500, got %d (%v)", code, body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 2 {
		t.Fatalf("fault insert: want tetap 2 fakta, got %d", got)
	}
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_agreed_insert ON rental_agreed_prices`); err != nil {
		t.Fatalf("lepas trigger fault insert: %v", err)
	}
	if code, body := acceptPrice(t, env.srv, env.wsA, draft3, env.ownerToken, "ag-gf-3", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah lepas fault insert: want 201, got %d (%v)", code, body)
	}
}

// TestAgreedPriceAuditFaultAtomicity: fault trigger audit → 500 tanpa
// fakta/audit; sesudah restore → sukses.
func TestAgreedPriceAuditFaultAtomicity(t *testing.T) {
	env := setupAgreedPrice(t)
	_, _, draft, _, quote := seedAgreedRefs(t, env, "SKU-AF-AG", "ag-af")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)

	_, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_agreed_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action = 'agreed_price_recorded' THEN
				RAISE EXCEPTION 'synthetic audit failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatalf("buat fungsi fault audit: %v", err)
	}
	_, err = env.boot.Exec(`CREATE TRIGGER qa_fault_agreed_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_agreed_audit()`)
	if err != nil {
		t.Fatalf("pasang trigger fault audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_agreed_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_agreed_audit()`)
	})
	code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-af-1", acceptBody(version, total))
	if code != http.StatusInternalServerError {
		t.Fatalf("accept fault audit: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "audit gagal ditulis")
	if countRows(t, env.boot, "rental_agreed_prices", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "agreed_price_recorded") != 0 {
		t.Fatalf("fault audit: want 0 fakta + 0 audit")
	}
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_agreed_audit ON audit_events`); err != nil {
		t.Fatalf("lepas trigger fault audit: %v", err)
	}
	if code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, "ag-af-1", acceptBody(version, total)); code != http.StatusCreated {
		t.Fatalf("accept sesudah lepas fault audit: want 201, got %d (%v)", code, body)
	}
}

// TestAgreedPriceLockWaitRaces: penerimaan yang menunggu workspace
// lock membaca keadaan SESUDAH lock — deactivate/revoke yang terjadi
// selagi menunggu → 403; tarif baru yang terjadi selagi menunggu
// TIDAK mengubah versi efektif draft (tetap v1 → 201).
func TestAgreedPriceLockWaitRaces(t *testing.T) {
	env := setupAgreedPrice(t)
	cust, item, draftA, _, quote := seedAgreedRefs(t, env, "SKU-RACE-AG", "ag-race")
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	base := dbCutoff(t, env.boot)
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	mkDraft := func(key string) string {
		t.Helper()
		code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, key, draftBody(cust, item, starts, ends))
		if code != http.StatusCreated {
			t.Fatalf("seed draft %s: want 201, got %d (%v)", key, code, body)
		}
		id, _ := body["id"].(string)
		return id
	}
	draftB := mkDraft("ag-race-draft-b")
	draftC := mkDraft("ag-race-draft-c")

	type raceResult struct {
		code int
		body map[string]any
		err  error
	}
	waitAccept := func(draft, key string) chan raceResult {
		ch := make(chan raceResult, 1)
		go func() {
			code, body, err := callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/accept-price", env.ownerToken, []string{key}, acceptBody(version, total))
			ch <- raceResult{code, body, err}
		}()
		return ch
	}
	holdLock := func() *sql.Tx {
		t.Helper()
		lockTx, err := env.boot.Begin()
		if err != nil {
			t.Fatalf("begin lock tx: %v", err)
		}
		var locked string
		if err := lockTx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, env.wsA).Scan(&locked); err != nil {
			lockTx.Rollback()
			t.Fatalf("pegang workspace lock: %v", err)
		}
		return lockTx
	}

	// (a) Deactivate selagi accept menunggu lock → 403.
	lockTx := holdLock()
	ch := waitAccept(draftA, "ag-race-a")
	time.Sleep(500 * time.Millisecond)
	if _, err := env.boot.Exec(`UPDATE module_installations SET status = 'inactive', updated_at = now() WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA); err != nil {
		lockTx.Rollback()
		t.Fatalf("deactivate selagi menunggu: %v", err)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (a): %v", err)
	}
	res := <-ch
	if res.err != nil {
		t.Fatalf("accept (a): %v", res.err)
	}
	if res.code != http.StatusForbidden {
		t.Fatalf("accept sesudah deactivate lock-wait: want 403, got %d (%v)", res.code, res.body)
	}
	if got := countRows(t, env.boot, "rental_agreed_prices", env.wsA); got != 0 {
		t.Fatalf("race (a): want 0 fakta, got %d", got)
	}
	if code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0"); code != http.StatusOK {
		t.Fatalf("reactivate (a): want 200, got %d (%v)", code, body)
	}
	// (b) Revoke selagi accept menunggu lock → 403.
	lockTx = holdLock()
	ch = waitAccept(draftB, "ag-race-b")
	time.Sleep(500 * time.Millisecond)
	if _, err := env.boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA); err != nil {
		lockTx.Rollback()
		t.Fatalf("revoke selagi menunggu: %v", err)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (b): %v", err)
	}
	res = <-ch
	if res.err != nil {
		t.Fatalf("accept (b): %v", res.err)
	}
	if res.code != http.StatusForbidden {
		t.Fatalf("accept sesudah revoke lock-wait: want 403, got %d (%v)", res.code, res.body)
	}
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	// (c) Tarif v2 adjacent dibuat selagi accept menunggu lock →
	// accept tetap memakai v1 (versi efektif pada starts_at draft).
	// INSERT v2 dijalankan DI DALAM lockTx: INSERT lewat koneksi
	// bootstrap terpisah akan menunggu FOR KEY SHARE pada baris
	// workspaces (FK workspace_id) yang sedang dipegang lockTx —
	// dan lockTx tidak akan pernah commit karena test menunggu
	// INSERT itu (gantung tanpa deadlock yang terdeteksi PG).
	// Di dalam transaksi yang sama, kunci itu tidak berkonflik.
	lockTx = holdLock()
	if _, err := lockTx.Exec(
		`INSERT INTO price_versions (workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, amount_minor, currency)
		 VALUES ($1, $2, 'standard', 'ag-race-v2', $3, $4, 175000, 'IDR')`,
		env.wsA, item, base.Add(73*time.Hour), base.Add(145*time.Hour)); err != nil {
		lockTx.Rollback()
		t.Fatalf("insert v2 selagi menunggu: %v", err)
	}
	ch = waitAccept(draftC, "ag-race-c")
	time.Sleep(500 * time.Millisecond)
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (c): %v", err)
	}
	res = <-ch
	if res.err != nil {
		t.Fatalf("accept (c): %v", res.err)
	}
	if res.code != http.StatusCreated {
		t.Fatalf("accept sesudah tarif baru lock-wait: want 201, got %d (%v)", res.code, res.body)
	}
	if res.body["price_version_id"] != version || res.body["total_minor"] != total {
		t.Fatalf("accept (c) harus tetap v1: got %v", res.body)
	}
}
