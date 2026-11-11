package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tes integrasi custody serah terima + pengembalian F2-01-F (migrasi 010)
// terhadap PostgreSQL asli: skema segar + rerun, RLS/hak runtime/FK
// komposit/partial UNIQUE, HTTP handover/return/custody, guard Rental +
// jendela waktu terencana, idempotency dua keyspace, konkurensi,
// custody-terbuka-memblokir-availability/hold/handover, return sesudah
// revoke/deactivate, dan fault guard/audit. Entitlement di sini HANYA
// fixture sintetis terisolasi lewat bootstrap (bukan grant nyata).
// Custody adalah catatan staf in-app — BUKAN consent/tanda tangan
// pelanggan, perjanjian hukum, invoice, pembayaran, posting ledger,
// atau transisi draft. Draft, harga, dan fakta harga tidak dimutasi.
// Hold TTL bukan pengembalian fisik; periode berakhir bukan unit kembali.

// setupCustody memakai env fakta harga (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris rental_custody fixture DULU (LIFO:
// cleanup ini didaftarkan sesudah setupAgreedPrice, jadi berjalan
// sebelum penghapusan fakta harga/hold/aset — urutan FK-aman, karena
// custody merujuk draft + fakta + hold + aset).
func setupCustody(t *testing.T) *activationEnv {
	t.Helper()
	env := setupAgreedPrice(t)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM rental_custody WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

func handoverBody(asset, hold string) map[string]string {
	return map[string]string{"asset_id": asset, "hold_id": hold}
}

func handover(t *testing.T, srv *httptest.Server, ws, draft, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/handover", token, []string{key}, body)
}

func handoverRaw(t *testing.T, srv *httptest.Server, ws, draft, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/handover", strings.NewReader(raw))
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

func returnCustody(t *testing.T, srv *httptest.Server, ws, draft, token, key string) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/return", token, []string{key}, nil)
}

func returnCustodyRaw(t *testing.T, srv *httptest.Server, ws, draft, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	var body *strings.Reader
	if raw == "" {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(raw)
	}
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/return", body)
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

func getCustody(t *testing.T, srv *httptest.Server, ws, draft, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/contracts/"+draft+"/custody", token, nil)
}

// custodyRefs adalah seluruh rantai API nyata untuk satu draft yang siap
// diserahterimakan: pelanggan + item, Rental aktif, tarif, draft, fakta
// harga disepakati, aset, dan hold live.
type custodyRefs struct {
	custID, itemID, draftID, priceID, factID, assetID, holdID string
	starts, ends                                              time.Time
	quote                                                     map[string]any
}

// seedCustodyRefs menyiapkan rantai lengkap lewat API nyata (tanpa seeding
// istimewa) dengan offset waktu TERKENDALI dari jam DB saat seed: tarif
// [base+priceOff, base+73j), draft [base+startOff, base+endOff). Default
// (3 dtk, 6 dtk, 50 jam) membuat jendela handover terbuka beberapa detik
// sesudah seed; pemanggil menunggu via waitUntil sebelum handover.
func seedCustodyRefs(t *testing.T, env *activationEnv, sku, keyPrefix string, priceOff, startOff, endOff time.Duration) custodyRefs {
	t.Helper()
	custID, itemID := seedDraftRefs(t, env, sku)
	activateRental(t, env)
	base := dbCutoff(t, env.boot)
	priceID := seedPrice(t, env, itemID, keyPrefix+"-price", base.Add(priceOff), base.Add(73*time.Hour), "150000", "IDR")
	starts := base.Add(startOff)
	ends := base.Add(endOff)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, keyPrefix+"-draft",
		draftBody(custID, itemID, starts.UTC().Format(time.RFC3339Nano), ends.UTC().Format(time.RFC3339Nano)))
	if code != http.StatusCreated {
		t.Fatalf("seed draft %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	draftID, _ := body["id"].(string)
	code, quote := getQuote(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("seed quote %s: want 200, got %d (%v)", keyPrefix, code, quote)
	}
	version, _ := quote["price_version_id"].(string)
	total, _ := quote["total_minor"].(string)
	code, body = acceptPrice(t, env.srv, env.wsA, draftID, env.ownerToken, keyPrefix+"-accept", acceptBody(version, total))
	if code != http.StatusCreated {
		t.Fatalf("seed accept %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	factID, _ := body["id"].(string)
	assetID := seedAsset(t, env, itemID, "SN-"+keyPrefix)
	holdID, _ := seedHold(t, env, draftID, assetID, keyPrefix+"-hold")
	return custodyRefs{custID: custID, itemID: itemID, draftID: draftID, priceID: priceID,
		factID: factID, assetID: assetID, holdID: holdID, starts: starts, ends: ends, quote: quote}
}

// waitUntil menunggu jam DB mencapai target (polling 100 mdtk, batas 30
// dtk): dipakai untuk membuka jendela handover terencana tanpa sleep
// buta dan tanpa memalsukan waktu.
func waitUntil(t *testing.T, boot *sql.DB, target time.Time) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		now := dbCutoff(t, boot)
		if !now.Before(target) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waitUntil %s: jam DB tidak mencapai target dalam 30 dtk (terakhir %s)", target.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// storedCustody membaca baris custody mentah; kolom return yang NULL
// dibaca sebagai string kosong (COALESCE) dan dibedakan dari nilai ada.
type storedCustody struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, AssetID, HoldID, ClientRequestID, ReturnClientRequestID, Status, HandedOutAt, ReturnedAt, HandedOutBy, ReturnedBy string
}

func readCustody(t *testing.T, boot *sql.DB, ws, id string) (storedCustody, bool) {
	t.Helper()
	var c storedCustody
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, rental_agreed_price_id::text, asset_id::text,
			hold_id::text, client_request_id, COALESCE(return_client_request_id, ''), status,
			handed_out_at::text, COALESCE(returned_at::text, ''), handed_out_by::text, COALESCE(returned_by::text, '')
		 FROM rental_custody WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&c.ID, &c.WorkspaceID, &c.RentalContractID, &c.RentalAgreedPriceID, &c.AssetID,
		&c.HoldID, &c.ClientRequestID, &c.ReturnClientRequestID, &c.Status,
		&c.HandedOutAt, &c.ReturnedAt, &c.HandedOutBy, &c.ReturnedBy)
	if err == sql.ErrNoRows {
		return storedCustody{}, false
	}
	if err != nil {
		t.Fatalf("read custody %s: %v", id, err)
	}
	return c, true
}

// custodySnapshot menangkap data/kebijakan/ACL/constraint/marker untuk
// uji rerun 010 (pola agreedSnapshot F2-01-E); mencakup rental_holds
// karena 010 memperluas CHECK status + kebijakan UPDATE-nya.
func custodySnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"custody":     `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || rental_contract_id::text || '|' || rental_agreed_price_id::text || '|' || asset_id::text || '|' || hold_id::text || '|' || client_request_id || '|' || coalesce(return_client_request_id,'') || '|' || status || '|' || handed_out_at::text || '|' || coalesce(returned_at::text,'') || '|' || handed_out_by::text || '|' || coalesce(returned_by::text,''), chr(10) ORDER BY id), '') FROM rental_custody`,
		"holds":       `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || draft_id::text || '|' || asset_id::text || '|' || client_request_id || '|' || status || '|' || starts_at::text || '|' || ends_at::text || '|' || expires_at::text || '|' || created_at::text || '|' || updated_at::text || '|' || period::text, chr(10) ORDER BY id), '') FROM rental_holds`,
		"policies":    `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('rental_custody', 'rental_holds')`,
		"acl":         `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('rental_custody', 'rental_holds')`,
		"markers":     `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
		"constraints": `SELECT coalesce(string_agg(conrelid::regclass::text || ':' || conname || ':' || contype::text, chr(10) ORDER BY conrelid::regclass::text, conname), '') FROM pg_constraint WHERE conrelid IN ('rental_custody'::regclass, 'rental_holds'::regclass, 'rental_agreed_prices'::regclass)`,
		"indexes":     `SELECT coalesce(string_agg(indexrelid::regclass::text || ':' || coalesce(pg_get_expr(indpred, indrelid),''), chr(10) ORDER BY indexrelid::regclass::text), '') FROM pg_index WHERE indrelid = 'rental_custody'::regclass`,
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

// TestCustodyFreshSchema: sepuluh marker persis, tabel kosong, pemilik
// bootstrap + RLS FORCE, kolom tepat, hak runtime (termasuk UPDATE kolom
// saja), kebijakan, invarian CHECK bernama, partial UNIQUE INDEX, dan
// perluasan CHECK/kebijakan hold (consumed) + UNIQUE(workspace_id, id)
// pada fakta harga dan hold.
func TestCustodyFreshSchema(t *testing.T) {
	boot := bootstrapDB(t)

	var markers string
	if err := boot.QueryRow(
		`SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`).Scan(&markers); err != nil {
		t.Fatalf("markers: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement, got %s", markers)
	}
	var total int
	if err := boot.QueryRow(`SELECT count(*) FROM rental_custody`).Scan(&total); err != nil {
		t.Fatalf("rental_custody kosong: %v", err)
	}
	if total != 0 {
		t.Fatalf("rental_custody: want 0 baris pada skema segar, got %d", total)
	}
	// Pemilik bootstrap + RLS FORCE (pola FreshSchema F2-01-E:
	// boolean discan ke bool, bukan string — pgx merender bool
	// sebagai "true"/"false").
	var owner string
	var super, rls, force bool
	if err := boot.QueryRow(
		`SELECT c.relowner::regrole::text, r.rolsuper, c.relrowsecurity, c.relforcerowsecurity
		 FROM pg_class c JOIN pg_roles r ON r.rolname = c.relowner::regrole::text
		 WHERE c.relname = 'rental_custody'`).Scan(&owner, &super, &rls, &force); err != nil {
		t.Fatalf("pemilik/RLS rental_custody: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("rental_custody dimiliki runtime role; harus dimiliki bootstrap/migrasi")
	}
	if !super {
		t.Fatalf("pemilik rental_custody (%q) harus superuser (bootstrap/migrasi), got super=%v", owner, super)
	}
	if !rls || !force {
		t.Fatalf("rental_custody: RLS harus ENABLE+FORCE, got rls=%v force=%v", rls, force)
	}
	// Kolom tepat (urutan pembuatan).
	rows, err := boot.Query(
		`SELECT column_name FROM information_schema.columns WHERE table_name = 'rental_custody' ORDER BY ordinal_position`)
	if err != nil {
		t.Fatalf("kolom rental_custody: %v", err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			t.Fatalf("kolom rental_custody: %v", err)
		}
		cols = append(cols, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("kolom rental_custody: %v", err)
	}
	wantCols := []string{"id", "workspace_id", "rental_contract_id", "rental_agreed_price_id", "asset_id", "hold_id",
		"client_request_id", "return_client_request_id", "status", "handed_out_at", "returned_at", "handed_out_by", "returned_by"}
	if !reflect.DeepEqual(cols, wantCols) {
		t.Fatalf("kolom rental_custody: want %v, got %v", wantCols, cols)
	}
	// Hak runtime: SELECT + INSERT saja; UPDATE tabel ditolak (hanya
	// kolom yang diizinkan); DELETE/TRUNCATE ditolak.
	var sel, ins, upd, del, trnc bool
	if err := boot.QueryRow(
		`SELECT has_table_privilege('umnia_business', 'rental_custody', 'SELECT'),
			has_table_privilege('umnia_business', 'rental_custody', 'INSERT'),
			has_table_privilege('umnia_business', 'rental_custody', 'UPDATE'),
			has_table_privilege('umnia_business', 'rental_custody', 'DELETE'),
			has_table_privilege('umnia_business', 'rental_custody', 'TRUNCATE')`).Scan(&sel, &ins, &upd, &del, &trnc); err != nil {
		t.Fatalf("hak tabel rental_custody: %v", err)
	}
	if !sel || !ins || upd || del || trnc {
		t.Fatalf("hak tabel rental_custody: want SELECT+INSERT saja, got SELECT=%v INSERT=%v UPDATE=%v DELETE=%v TRUNCATE=%v", sel, ins, upd, del, trnc)
	}
	for _, tc := range []struct {
		col  string
		want bool
	}{
		{"status", true}, {"returned_at", true}, {"returned_by", true}, {"return_client_request_id", true},
		{"handed_out_at", false}, {"handed_out_by", false}, {"asset_id", false}, {"hold_id", false},
		{"rental_contract_id", false}, {"client_request_id", false},
	} {
		var got bool
		if err := boot.QueryRow(
			`SELECT has_column_privilege('umnia_business', 'rental_custody', $1, 'UPDATE')`, tc.col).Scan(&got); err != nil {
			t.Fatalf("hak kolom %s: %v", tc.col, err)
		}
		if got != tc.want {
			t.Fatalf("hak kolom UPDATE %s: want %v, got %v", tc.col, tc.want, got)
		}
	}
	// Kebijakan: tepat 3 (select/insert/update).
	var n int
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_policies WHERE tablename = 'rental_custody'`).Scan(&n); err != nil {
		t.Fatalf("kebijakan rental_custody: %v", err)
	}
	if n != 3 {
		t.Fatalf("kebijakan rental_custody: want 3, got %d", n)
	}
	var qual, withCheck string
	if err := boot.QueryRow(
		`SELECT coalesce(qual,''), coalesce(with_check,'') FROM pg_policies WHERE tablename = 'rental_custody' AND policyname = 'rental_custody_update'`).Scan(&qual, &withCheck); err != nil {
		t.Fatalf("kebijakan update rental_custody: %v", err)
	}
	if !strings.Contains(qual, "status = 'out'") || !strings.Contains(withCheck, "status = 'returned'") {
		t.Fatalf("kebijakan update rental_custody: USING harus status='out', WITH CHECK status='returned'; got USING %q WITH CHECK %q", qual, withCheck)
	}
	// CHECK 6 (kunci, kunci return, status, finite, state, returned>=
	// handed_out) + UNIQUE 3 + FK 7 + EXCLUDE 0 pada rental_custody.
	var c, u, f, x int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f'), count(*) FILTER (WHERE contype = 'x')
		 FROM pg_constraint WHERE conrelid = 'rental_custody'::regclass`).Scan(&c, &u, &f, &x); err != nil {
		t.Fatalf("constraints rental_custody: %v", err)
	}
	if c != 6 || u != 3 || f != 7 || x != 0 {
		t.Fatalf("constraints rental_custody: want CHECK 6 + UNIQUE 3 + FK 7 + EXCLUDE 0, got CHECK %d + UNIQUE %d + FK %d + EXCLUDE %d", c, u, f, x)
	}
	for _, name := range []string{"rental_custody_finite_times", "rental_custody_state_check", "rental_custody_returned_after_handover"} {
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_constraint WHERE conname = $1 AND contype = 'c'`, name).Scan(&n); err != nil {
			t.Fatalf("cek CHECK %s: %v", name, err)
		}
		if n != 1 {
			t.Fatalf("CHECK %s: want 1, got %d", name, n)
		}
	}
	// Partial UNIQUE INDEX satu custody terbuka per aset.
	var pred string
	if err := boot.QueryRow(
		`SELECT coalesce(pg_get_expr(indpred, indrelid),'') FROM pg_index WHERE indexrelid = 'rental_custody_one_out_per_asset'::regclass`).Scan(&pred); err != nil {
		t.Fatalf("partial UNIQUE INDEX custody: %v", err)
	}
	if !strings.Contains(pred, "status = 'out'") {
		t.Fatalf("partial UNIQUE INDEX custody: predikat harus status='out', got %q", pred)
	}
	// UNIQUE(workspace_id, id) baru pada fakta harga + hold (target FK
	// komposit 010).
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_constraint WHERE conname IN ('rental_agreed_prices_workspace_id_id_key', 'rental_holds_workspace_id_id_key') AND contype = 'u'`).Scan(&n); err != nil {
		t.Fatalf("count unique workspace_id_id fakta/hold: %v", err)
	}
	if n != 2 {
		t.Fatalf("UNIQUE (workspace_id, id) fakta/hold: want 2, got %d", n)
	}
	// CHECK status hold kini mengizinkan consumed; kebijakan UPDATE
	// hold WITH CHECK mengizinkan consumed.
	var def string
	if err := boot.QueryRow(
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_holds_status_check'`).Scan(&def); err != nil {
		t.Fatalf("CHECK status hold: %v", err)
	}
	if !strings.Contains(def, "'consumed'") {
		t.Fatalf("CHECK status hold: harus mengizinkan 'consumed', got %q", def)
	}
	if err := boot.QueryRow(
		`SELECT coalesce(with_check,'') FROM pg_policies WHERE tablename = 'rental_holds' AND policyname = 'rental_holds_update'`).Scan(&withCheck); err != nil {
		t.Fatalf("kebijakan update hold: %v", err)
	}
	if !strings.Contains(withCheck, "'consumed'") {
		t.Fatalf("kebijakan update hold: WITH CHECK harus mengizinkan 'consumed', got %q", withCheck)
	}
}

// TestCustodyRerunPreservesData: rerun 010 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// kebijakan, ACL, constraint, dan marker; rerun kumulatif 001-010
// sesudahnya mengembalikan keadaan akhir (kebijakan hold tetap
// mengizinkan consumed).
func TestCustodyRerunPreservesData(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}

	cust := "11111111-2222-3333-4444-555555555571"
	item := "11111111-2222-3333-4444-555555555572"
	draft := "11111111-2222-3333-4444-555555555573"
	price := "11111111-2222-3333-4444-555555555574"
	fact := "11111111-2222-3333-4444-555555555575"
	asset := "11111111-2222-3333-4444-555555555576"
	hold := "11111111-2222-3333-4444-555555555577"
	custody := "11111111-2222-3333-4444-555555555578"
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM rental_custody WHERE id = $1`, custody)
		_, _ = boot.Exec(`DELETE FROM rental_holds WHERE id = $1`, hold)
		_, _ = boot.Exec(`DELETE FROM physical_assets WHERE id = $1`, asset)
		_, _ = boot.Exec(`DELETE FROM rental_agreed_prices WHERE id = $1`, fact)
		_, _ = boot.Exec(`DELETE FROM price_versions WHERE id = $1`, price)
		_, _ = boot.Exec(`DELETE FROM rental_contracts WHERE id = $1`, draft)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE id = $1`, item)
		_, _ = boot.Exec(`DELETE FROM parties WHERE id = $1`, cust)
	})
	// Workspace fixture memakai workspace nyata pertama dari env
	// aktivasi (bukan UUID sintetis): ambil dari setupCustody.
	env := setupCustody(t)
	ws := env.wsA
	if _, err := boot.Exec(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Rerun')`, cust, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-RERUN-10', 'Barang Rerun', 'pcs', 'active')`, item, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'rerun-10', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft, ws, cust, item); err != nil {
		t.Fatalf("fixture draft: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'rerun-price-10', '2026-11-10T01:00:00Z', '2026-11-13T01:00:00Z', now(), 150000, 'IDR')`, price, ws, item); err != nil {
		t.Fatalf("fixture price: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'rerun-fact-10', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact, ws, draft, price, item, cust, env.ownerID); err != nil {
		t.Fatalf("fixture fact: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3, 'SN-RERUN-10')`, asset, ws, item); err != nil {
		t.Fatalf("fixture asset: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'rerun-hold-10', 'consumed', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold, ws, draft, asset); err != nil {
		t.Fatalf("fixture hold: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_custody (id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, status, handed_out_at, handed_out_by) VALUES ($1, $2, $3, $4, $5, $6, 'rerun-custody-10', 'out', '2026-11-10T02:00:00Z', $7)`, custody, ws, draft, fact, asset, hold, env.ownerID); err != nil {
		t.Fatalf("fixture custody: %v", err)
	}
	// Normalisasi keadaan akhir rantai SEBELUM snapshot: 010 memiliki
	// perluasan 'consumed' pada CHECK + kebijakan UPDATE hold, dan
	// rerun 007 oleh tes lain (yang berjalan lebih dulu) dapat
	// mengembalikan versi 007. Tes ini membuktikan rerun 010
	// mempertahankan state yang ditetapkan 010 sendiri — bukan state
	// yang diregresi file lain.
	if code, out := runPsqlFile(t, bootURL, "migrations/010_rental_custody.sql"); code != 0 {
		t.Fatalf("normalisasi 010: want exit 0, got %d (%s)", code, out)
	}
	before := custodySnapshot(t, boot)
	if !strings.Contains(before["custody"], "rerun-custody-10") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture custody")
	}

	code, out := runPsqlFile(t, bootURL, "migrations/010_rental_custody.sql")
	if code != 0 {
		t.Fatalf("rerun 010: want exit 0, got %d (%s)", code, out)
	}
	after := custodySnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun 010 mengubah state:\nsebelum=%v\nsesudah=%v", before, after)
	}

	// Rerun kumulatif 001-010 (autocommit, urutan file): keadaan akhir
	// harus sama — rerun 007 di tengah tidak boleh menghilangkan
	// 'consumed' dari kebijakan hold karena 010 berjalan terakhir.
	for _, f := range []string{"001_init.sql", "002_identity.sql", "003_modules.sql", "004_module_installations.sql", "005_core_master_data.sql", "006_rental_contracts.sql", "007_asset_availability.sql", "008_price_versions.sql", "009_rental_agreed_prices.sql", "010_rental_custody.sql"} {
		code, out := runPsqlFile(t, bootURL, "migrations/"+f)
		if code != 0 {
			t.Fatalf("rerun kumulatif %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	final := custodySnapshot(t, boot)
	if !reflect.DeepEqual(before, final) {
		t.Fatalf("rerun kumulatif 001-010 mengubah state:\nsebelum=%v\nsesudah=%v", before, final)
	}
	if c, ok := readCustody(t, boot, ws, custody); !ok || c.Status != "out" {
		t.Fatalf("custody fixture sesudah rerun kumulatif: want out, got %+v", c)
	}
}

// TestCustodyRLSAndPermissions: runtime role hanya bisa INSERT custody
// valid sebagai owner/manager (konteks tenant + manage_business_data);
// kasir/nonmember 42501; FK komposit lintas-tenant 23503; CHECK 23514;
// duplikat draft/kunci/return-key + partial UNIQUE satu custody terbuka
// per aset 23505; UPDATE kolom return pada baris out diizinkan
// (transisi return), UPDATE kolom lain/DELETE/TRUNCATE 42501; bacaan
// lintas-tenant dan tanpa konteks 0 baris.
func TestCustodyRLSAndPermissions(t *testing.T) {
	env := setupCustody(t)
	db := testDB(t)
	boot := env.boot
	wsA, wsB := env.wsA, env.wsB

	// Rujukan fixture lewat bootstrap (bypass RLS): pelanggan + barang
	// per workspace, aset A/B/D wsA, aset C wsB. Aset D dipakai kasus
	// FK/CHECK supaya partial UNIQUE aset-keluar (aset A/B sudah out)
	// tidak mendahului pelanggaran FK/CHECK yang sedang diuji.
	var custA, itemA, custB, itemB, assetA, assetB, assetC, assetD string
	mustID := func(q string, args ...any) string {
		t.Helper()
		var id string
		if err := boot.QueryRow(q, args...).Scan(&id); err != nil {
			t.Fatalf("fixture: %v", err)
		}
		return id
	}
	custA = mustID(`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan A') RETURNING id`, wsA)
	itemA = mustID(`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-CU-A', 'Barang A', 'pcs', 'active') RETURNING id`, wsA)
	custB = mustID(`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, wsB)
	itemB = mustID(`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-RLS-CU-B', 'Barang B', 'pcs', 'active') RETURNING id`, wsB)
	assetA = mustID(`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, 'SN-RLS-CU-A') RETURNING id`, wsA, itemA)
	assetB = mustID(`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, 'SN-RLS-CU-B') RETURNING id`, wsA, itemA)
	assetC = mustID(`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, 'SN-RLS-CU-C') RETURNING id`, wsB, itemB)
	assetD = mustID(`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, 'SN-RLS-CU-D') RETURNING id`, wsA, itemA)

	// Draft + versi harga lewat API nyata (bukan seeding istimewa).
	activateRental(t, env)
	base := dbCutoff(t, boot)
	priceA := seedPrice(t, env, itemA, "rls-cu-price-a", base.Add(1*time.Hour), base.Add(200*time.Hour), "150000", "IDR")
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	mkDraft := func(ws, token, key string) string {
		t.Helper()
		code, body := createDraft(t, env.srv, ws, token, key, draftBody(custA, itemA, starts, ends))
		if code != http.StatusCreated {
			t.Fatalf("seed draft %s: want 201, got %d (%v)", key, code, body)
		}
		id, _ := body["id"].(string)
		return id
	}
	draft1 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-1")
	draft2 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-2")
	draft3 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-3")
	draft4 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-4")
	// Draft + versi harga lintas-tenant (wsB) untuk kasus FK.
	grantEntitlement(t, boot, wsB, "rental.basic", "0.1.0")
	if code, body := activateModule(t, env.srv, wsB, "rental.basic", env.otherToken, "0.1.0"); code != http.StatusOK {
		t.Fatalf("aktivasi rental wsB: want 200, got %d (%v)", code, body)
	}
	priceBCode, priceBBody := createPrice(t, env.srv, wsB, itemB, env.otherToken, "rls-cu-price-b", priceBody(base.Add(1*time.Hour), base.Add(200*time.Hour), "150000", "IDR"))
	if priceBCode != http.StatusCreated {
		t.Fatalf("seed price wsB: want 201, got %d (%v)", priceBCode, priceBBody)
	}
	priceB, _ := priceBBody["id"].(string)
	code, body := createDraft(t, env.srv, wsB, env.otherToken, "rls-cu-draft-b", draftBody(custB, itemB, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft wsB: want 201, got %d (%v)", code, body)
	}
	draftB, _ := body["id"].(string)
	// Fakta wsB lewat bootstrap (untuk kasus FK lintas-tenant).
	factB := mustID(`INSERT INTO rental_agreed_prices (workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, 'elapsed_24h_ceil_v1', $4, $5, 'rls-cu-fact-b', 150000, 'IDR', 2, 300000, $6, $7, now(), $8) RETURNING id`, wsB, draftB, priceB, itemB, custB, starts, ends, env.otherID)

	// Fakta harga lewat INSERT runtime (pola RLS F2-01-E): satu fakta
	// per draft; nilai valid bersama: 48 jam = 2 unit x 150000.
	insertFactSQL := `INSERT INTO rental_agreed_prices
		(workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id,
		 client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`
	factArgs := func(draft, price, key string) []any {
		return []any{wsA, draft, price, "elapsed_24h_ceil_v1", itemA, custA, key,
			int64(150000), "IDR", int64(2), int64(300000), starts, ends,
			base.UTC().Format(time.RFC3339Nano), env.ownerID}
	}
	tryExec := func(ws, subj, query string, args ...any) (string, int64, error) {
		t.Helper()
		tx := tenantTx(t, db, ws, subj)
		res, err := tx.Exec(query, args...)
		if err != nil {
			tx.Rollback()
			return pgCode(err), 0, err
		}
		n, _ := res.RowsAffected()
		if err := tx.Commit(); err != nil {
			return pgCode(err), 0, err
		}
		return "", n, nil
	}
	mustFact := func(draft, key string) string {
		t.Helper()
		var id string
		tx := tenantTx(t, db, wsA, env.ownerID)
		if err := tx.QueryRow(insertFactSQL+` RETURNING id`, factArgs(draft, priceA, key)...).Scan(&id); err != nil {
			tx.Rollback()
			t.Fatalf("fixture fact %s: %v", key, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("fixture fact %s commit: %v", key, err)
		}
		return id
	}
	fact1 := mustFact(draft1, "rls-cu-fact-1")
	fact2 := mustFact(draft2, "rls-cu-fact-2")
	fact3 := mustFact(draft3, "rls-cu-fact-3")
	fact4 := mustFact(draft4, "rls-cu-fact-4")

	// Hold lewat bootstrap (periode berdampingan supaya EXCLUDE tidak
	// menolak; status held).
	mkHold := func(draft, asset, key, from, to string) string {
		t.Helper()
		var id string
		if err := boot.QueryRow(
			`INSERT INTO rental_holds (workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at)
			 VALUES ($1, $2, $3, $4, 'held', $5, $6, now() + interval '24 hours') RETURNING id`,
			wsA, draft, asset, key, from, to).Scan(&id); err != nil {
			t.Fatalf("fixture hold %s: %v", key, err)
		}
		return id
	}
	hold1 := mkHold(draft1, assetA, "rls-cu-hold-1", "2026-11-10T02:00:00Z", "2026-11-12T02:00:00Z")
	hold2 := mkHold(draft2, assetB, "rls-cu-hold-2", "2026-11-10T02:00:00Z", "2026-11-12T02:00:00Z")
	hold3 := mkHold(draft3, assetA, "rls-cu-hold-3", "2026-11-12T02:00:00Z", "2026-11-14T02:00:00Z")
	hold4 := mkHold(draft4, assetB, "rls-cu-hold-4", "2026-11-12T02:00:00Z", "2026-11-14T02:00:00Z")

	insertSQL := `INSERT INTO rental_custody
		(workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id,
		 return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`
	// Nilai valid bersama: out, handed_out 2026-11-10T02:00Z.
	valid := func(draft, fact, asset, hold, key string) []any {
		return []any{wsA, draft, fact, asset, hold, key, nil, "out", "2026-11-10T02:00:00Z", nil, env.ownerID, nil}
	}
	// Owner + manager berhasil (draft/aset berbeda).
	if code, _, err := tryExec(wsA, env.ownerID, insertSQL, valid(draft1, fact1, assetA, hold1, "rls-cu-owner")...); code != "" || err != nil {
		t.Fatalf("INSERT owner: want sukses, got code %q err %v", code, err)
	}
	if code, _, err := tryExec(wsA, env.mgrID, insertSQL, valid(draft2, fact2, assetB, hold2, "rls-cu-manager")...); code != "" || err != nil {
		t.Fatalf("INSERT manager: want sukses, got code %q err %v", code, err)
	}
	// Kasir + nonmember ditolak RLS (42501).
	if code, _, _ := tryExec(wsA, env.cashID, insertSQL, valid(draft3, fact3, assetA, hold3, "rls-cu-cashier")...); code != "42501" {
		t.Fatalf("INSERT kasir: want 42501, got %q", code)
	}
	if code, _, _ := tryExec(wsA, env.otherID, insertSQL, valid(draft3, fact3, assetA, hold3, "rls-cu-other")...); code != "42501" {
		t.Fatalf("INSERT nonmember: want 42501, got %q", code)
	}
	// FK: rujukan hilang/lintas-tenant ditolak 23503.
	randomUUID := "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		name string
		mut  func(a []any)
	}{
		{"draft hilang", func(a []any) { a[1] = randomUUID }},
		{"draft lintas-tenant", func(a []any) { a[1] = draftB }},
		{"fakta lintas-tenant", func(a []any) { a[2] = factB }},
		{"aset lintas-tenant", func(a []any) { a[3] = assetC }},
	} {
		// Basis aset D (tanpa custody keluar) supaya pelanggaran FK
		// yang diuji tidak tertutup 23505 partial UNIQUE aset A.
		args := valid(draft3, fact3, assetD, hold3, "rls-cu-fk-"+tc.name)
		// Kunci harus valid ASCII tanpa spasi: pakai indeks.
		args[5] = "rls-cu-fk-x"
		tc.mut(args)
		if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, args...); code != "23503" {
			t.Fatalf("INSERT FK %s: want 23503, got %q", tc.name, code)
		}
	}
	// CHECK: semua kasus berikut harus 23514.
	checkCases := []struct {
		name string
		mut  func(a []any)
	}{
		{"status lain", func(a []any) { a[7] = "lost" }},
		{"handed_out infinity", func(a []any) { a[8] = "infinity" }},
		{"returned_at infinity", func(a []any) { a[7] = "returned"; a[9] = "infinity"; a[6] = "rls-cu-ret-x"; a[11] = env.ownerID }},
		{"returned lebih awal", func(a []any) {
			a[7] = "returned"
			a[9] = "2026-11-09T02:00:00Z"
			a[6] = "rls-cu-ret-x"
			a[11] = env.ownerID
		}},
		{"returned tanpa kunci", func(a []any) { a[7] = "returned"; a[9] = "2026-11-11T02:00:00Z"; a[11] = env.ownerID }},
		{"returned tanpa waktu", func(a []any) { a[7] = "returned"; a[6] = "rls-cu-ret-x"; a[11] = env.ownerID }},
		{"returned tanpa aktor", func(a []any) { a[7] = "returned"; a[9] = "2026-11-11T02:00:00Z"; a[6] = "rls-cu-ret-x" }},
		{"out dengan returned_at", func(a []any) { a[9] = "2026-11-11T02:00:00Z" }},
		{"out dengan kunci return", func(a []any) { a[6] = "rls-cu-ret-x" }},
		{"out dengan returned_by", func(a []any) { a[11] = env.ownerID }},
		{"kunci kosong", func(a []any) { a[5] = "" }},
		{"kunci berisi spasi", func(a []any) { a[5] = "bad key" }},
		{"kunci return berisi spasi", func(a []any) { a[7] = "returned"; a[9] = "2026-11-11T02:00:00Z"; a[11] = env.ownerID; a[6] = "bad key" }},
	}
	for i, tc := range checkCases {
		// Basis aset D juga di sini: kasus "out dengan ..." tetap
		// status out, jadi aset A akan memicu 23505 partial UNIQUE
		// bila CHECK tidak dievaluasi lebih dulu.
		args := valid(draft3, fact3, assetD, hold3, fmt.Sprintf("rls-cu-chk-%d", i))
		tc.mut(args)
		if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, args...); code != "23514" {
			t.Fatalf("CHECK %s: want 23514, got %q", tc.name, code)
		}
	}
	// Duplikat: draft yang sama (kunci baru) dan kunci yang sama
	// (draft lain) sama-sama 23505.
	if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, valid(draft1, fact1, assetA, hold1, "rls-cu-dup-draft")...); code != "23505" {
		t.Fatalf("INSERT draft duplikat: want 23505, got %q", code)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, valid(draft3, fact3, assetA, hold3, "rls-cu-owner")...); code != "23505" {
		t.Fatalf("INSERT kunci duplikat: want 23505, got %q", code)
	}
	// Partial UNIQUE: aset A sudah keluar (draft1 out) — custody out
	// kedua untuk aset yang sama 23505 walau draft/hold berbeda.
	if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, valid(draft3, fact3, assetA, hold3, "rls-cu-partial")...); code != "23505" {
		t.Fatalf("INSERT partial UNIQUE aset keluar: want 23505, got %q", code)
	}
	// Baris returned valid + duplikat return key 23505.
	retArgs := func(draft, fact, asset, hold, key, retKey string) []any {
		return []any{wsA, draft, fact, asset, hold, key, retKey, "returned", "2026-11-10T02:00:00Z", "2026-11-11T02:00:00Z", env.ownerID, env.ownerID}
	}
	if code, _, err := tryExec(wsA, env.ownerID, insertSQL, retArgs(draft3, fact3, assetA, hold3, "rls-cu-ret-3", "rls-cu-retkey-1")...); code != "" || err != nil {
		t.Fatalf("INSERT returned: want sukses, got code %q err %v", code, err)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, retArgs(draft4, fact4, assetB, hold4, "rls-cu-ret-4", "rls-cu-retkey-1")...); code != "23505" {
		t.Fatalf("INSERT return key duplikat: want 23505, got %q", code)
	}
	// Hanya tiga custody valid yang bertahan (2 out + 1 returned).
	if got := countRows(t, boot, "rental_custody", wsA); got != 3 {
		t.Fatalf("custody wsA: want 3, got %d", got)
	}
	// UPDATE: transisi return pada baris out diizinkan (kolom grant +
	// kebijakan USING status='out'); UPDATE handed_out_at 42501;
	// UPDATE pada baris returned 0 baris (difilter USING); DELETE dan
	// TRUNCATE 42501.
	if code, n, err := tryExec(wsA, env.ownerID, `UPDATE rental_custody SET status = 'returned', returned_at = '2026-11-11T02:00:00Z', returned_by = $2, return_client_request_id = 'rls-cu-ret-2' WHERE workspace_id = $1 AND rental_contract_id = $3`, wsA, env.ownerID, draft2); code != "" || err != nil || n != 1 {
		t.Fatalf("UPDATE return baris out: want sukses 1 baris, got code %q n=%d err %v", code, n, err)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, `UPDATE rental_custody SET handed_out_at = '2026-11-10T03:00:00Z' WHERE workspace_id = $1`, wsA); code != "42501" {
		t.Fatalf("UPDATE handed_out_at: want 42501, got %q", code)
	}
	if code, n, err := tryExec(wsA, env.ownerID, `UPDATE rental_custody SET status = 'out' WHERE workspace_id = $1 AND rental_contract_id = $2`, wsA, draft3); code != "" || err != nil || n != 0 {
		t.Fatalf("UPDATE baris returned: want sukses 0 baris, got code %q n=%d err %v", code, n, err)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, `DELETE FROM rental_custody WHERE workspace_id = $1`, wsA); code != "42501" {
		t.Fatalf("DELETE runtime: want 42501, got %q", code)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, `TRUNCATE rental_custody`); code != "42501" {
		t.Fatalf("TRUNCATE runtime: want 42501, got %q", code)
	}
	// Bacaan: konteks wsB 0 baris; tanpa konteks 0 baris.
	txB := tenantTx(t, db, wsB, env.otherID)
	var countB int
	if err := txB.QueryRow(`SELECT count(*) FROM rental_custody`).Scan(&countB); err != nil {
		txB.Rollback()
		t.Fatalf("baca wsB: %v", err)
	}
	txB.Rollback()
	if countB != 0 {
		t.Fatalf("baca wsB: want 0, got %d", countB)
	}
	var countRaw int
	if err := db.QueryRow(`SELECT count(*) FROM rental_custody`).Scan(&countRaw); err != nil {
		t.Fatalf("baca tanpa konteks: %v", err)
	}
	if countRaw != 0 {
		t.Fatalf("baca tanpa konteks: want 0, got %d", countRaw)
	}
}

// seedCustodyNoFact seperti seedCustodyRefs tetapi TANPA penerimaan
// harga: dipakai untuk kasus "fakta harga belum ada -> 409".
func seedCustodyNoFact(t *testing.T, env *activationEnv, sku, keyPrefix string, priceOff, startOff, endOff time.Duration) custodyRefs {
	t.Helper()
	custID, itemID := seedDraftRefs(t, env, sku)
	activateRental(t, env)
	base := dbCutoff(t, env.boot)
	priceID := seedPrice(t, env, itemID, keyPrefix+"-price", base.Add(priceOff), base.Add(73*time.Hour), "150000", "IDR")
	starts := base.Add(startOff)
	ends := base.Add(endOff)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, keyPrefix+"-draft",
		draftBody(custID, itemID, starts.UTC().Format(time.RFC3339Nano), ends.UTC().Format(time.RFC3339Nano)))
	if code != http.StatusCreated {
		t.Fatalf("seed draft %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	draftID, _ := body["id"].(string)
	assetID := seedAsset(t, env, itemID, "SN-"+keyPrefix)
	holdID, _ := seedHold(t, env, draftID, assetID, keyPrefix+"-hold")
	return custodyRefs{custID: custID, itemID: itemID, draftID: draftID, priceID: priceID,
		assetID: assetID, holdID: holdID, starts: starts, ends: ends}
}

// TestCustodyHTTP: jalur bahagia draft berharga -> hold live -> handover
// 201 -> GET out -> return 200 -> GET returned; hold menjadi consumed;
// draft/harga/fakta tidak berubah; unit bisa dipakai lagi sesudah return.
func TestCustodyHTTP(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-HTTP-CU", "cu-http", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)

	factBefore, ok := readAgreedPrice(t, env.boot, env.wsA, refs.factID)
	if !ok {
		t.Fatalf("fakta sebelum handover tidak ditemukan")
	}
	draftBefore, ok := readDraft(t, env.boot, env.wsA, refs.draftID)
	if !ok {
		t.Fatalf("draft sebelum handover tidak ditemukan")
	}
	priceBefore, ok := readPrice(t, env.boot, env.wsA, refs.priceID)
	if !ok {
		t.Fatalf("harga sebelum handover tidak ditemukan")
	}

	code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-http-ho", handoverBody(refs.assetID, refs.holdID))
	if code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	custodyID, _ := body["id"].(string)
	if body["status"] != "out" || body["rental_contract_id"] != refs.draftID || body["rental_agreed_price_id"] != refs.factID ||
		body["asset_id"] != refs.assetID || body["hold_id"] != refs.holdID || body["client_request_id"] != "cu-http-ho" ||
		body["handed_out_by"] != env.ownerID {
		t.Fatalf("handover DTO tidak cocok: %v", body)
	}
	if body["returned_at"] != nil || body["returned_by"] != nil || body["return_client_request_id"] != nil {
		t.Fatalf("handover DTO: kolom return harus null saat out: %v", body)
	}
	handedOut, _ := time.Parse(time.RFC3339Nano, body["handed_out_at"].(string))
	if handedOut.Before(refs.starts) || !handedOut.Before(refs.ends) {
		t.Fatalf("handed_out_at %s harus dalam [starts, ends)", handedOut.Format(time.RFC3339Nano))
	}
	if c, ok := readCustody(t, env.boot, env.wsA, custodyID); !ok || c.Status != "out" || c.HoldID != refs.holdID || c.ReturnedAt != "" {
		t.Fatalf("custody tersimpan: want out, got %+v", c)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refs.holdID); !ok || h.Status != "consumed" {
		t.Fatalf("hold sesudah handover: want consumed, got %+v", h)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 1 {
		t.Fatalf("audit rental_handed_over: want 1, got %d", got)
	}
	code, body = getCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken)
	if code != http.StatusOK || body["id"] != custodyID || body["status"] != "out" {
		t.Fatalf("GET custody: want 200 out, got %d (%v)", code, body)
	}
	// Unit keluar: availability false untuk periode draft.
	code, body = getAvailability(t, env.srv, env.wsA, refs.assetID, env.ownerToken,
		availQuery(refs.starts.UTC().Format(time.RFC3339Nano), refs.ends.UTC().Format(time.RFC3339Nano)))
	if code != http.StatusOK || body["available"] != false {
		t.Fatalf("availability saat keluar: want false, got %d (%v)", code, body)
	}

	code, body = returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-http-ret")
	if code != http.StatusOK {
		t.Fatalf("return: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "returned" || body["return_client_request_id"] != "cu-http-ret" || body["returned_by"] != env.ownerID {
		t.Fatalf("return DTO tidak cocok: %v", body)
	}
	returnedAt, _ := time.Parse(time.RFC3339Nano, body["returned_at"].(string))
	if returnedAt.Before(handedOut) {
		t.Fatalf("returned_at %s harus >= handed_out_at %s", returnedAt.Format(time.RFC3339Nano), handedOut.Format(time.RFC3339Nano))
	}
	if c, ok := readCustody(t, env.boot, env.wsA, custodyID); !ok || c.Status != "returned" || c.ReturnClientRequestID != "cu-http-ret" || c.ReturnedAt == "" {
		t.Fatalf("custody tersimpan sesudah return: want returned, got %+v", c)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit rental_returned: want 1, got %d", got)
	}
	code, body = getCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken)
	if code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("GET custody sesudah return: want 200 returned, got %d (%v)", code, body)
	}
	// Draft/harga/fakta tidak berubah sama sekali.
	if factAfter, _ := readAgreedPrice(t, env.boot, env.wsA, refs.factID); factAfter != factBefore {
		t.Fatalf("fakta harga berubah:\nsebelum=%+v\nsesudah=%+v", factBefore, factAfter)
	}
	if draftAfter, _ := readDraft(t, env.boot, env.wsA, refs.draftID); draftAfter != draftBefore {
		t.Fatalf("draft berubah:\nsebelum=%+v\nsesudah=%+v", draftBefore, draftAfter)
	}
	if priceAfter, _ := readPrice(t, env.boot, env.wsA, refs.priceID); priceAfter != priceBefore {
		t.Fatalf("harga berubah:\nsebelum=%+v\nsesudah=%+v", priceBefore, priceAfter)
	}
	// Sesudah return unit bisa dipakai lagi: availability true untuk
	// periode jauh + hold baru untuk draft baru 201.
	farStarts := refs.ends.Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
	farEnds := refs.ends.Add(10 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body = getAvailability(t, env.srv, env.wsA, refs.assetID, env.ownerToken, availQuery(farStarts, farEnds))
	if code != http.StatusOK || body["available"] != true {
		t.Fatalf("availability sesudah return: want true, got %d (%v)", code, body)
	}
	code, body = createDraft(t, env.srv, env.wsA, env.ownerToken, "cu-http-draft-2",
		draftBody(refs.custID, refs.itemID, farStarts, farEnds))
	if code != http.StatusCreated {
		t.Fatalf("draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	code, body = createHold(t, env.srv, env.wsA, draft2, env.ownerToken, "cu-http-hold-2", map[string]string{"asset_id": refs.assetID})
	if code != http.StatusCreated {
		t.Fatalf("hold sesudah return: want 201, got %d (%v)", code, body)
	}
}

// TestCustodyHandoverGuards: matriks penolakan handover — auth/bentuk,
// peran/tenant, fakta hilang, item diarsipkan, jendela waktu, hold
// kedaluwarsa/released/cocok-salah, dan rujukan hilang.
func TestCustodyHandoverGuards(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-GD-CU", "cu-gd", 3*time.Second, 6*time.Second, 50*time.Hour)

	// Auth/bentuk/header (semua sebelum jendela: 400/401 mendahului 409).
	if code, _ := handoverRaw(t, env.srv, env.wsA, refs.draftID, "", []string{"cu-gd-1"}, `{"asset_id":"`+refs.assetID+`","hold_id":"`+refs.holdID+`"}`); code != http.StatusUnauthorized {
		t.Fatalf("handover unauth: want 401, got %d", code)
	}
	if code, _ := handoverRaw(t, env.srv, "bukan-uuid", refs.draftID, env.ownerToken, []string{"cu-gd-1"}, `{"asset_id":"`+refs.assetID+`","hold_id":"`+refs.holdID+`"}`); code != http.StatusBadRequest {
		t.Fatalf("handover ws buruk: want 400, got %d", code)
	}
	if code, _ := handoverRaw(t, env.srv, env.wsA, "bukan-uuid", env.ownerToken, []string{"cu-gd-1"}, `{"asset_id":"`+refs.assetID+`","hold_id":"`+refs.holdID+`"}`); code != http.StatusBadRequest {
		t.Fatalf("handover draft buruk: want 400, got %d", code)
	}
	for _, tc := range []struct {
		name string
		keys []string
		raw  string
	}{
		{"tanpa kunci", nil, `{"asset_id":"` + refs.assetID + `","hold_id":"` + refs.holdID + `"}`},
		{"dua kunci", []string{"cu-gd-1", "cu-gd-2"}, `{"asset_id":"` + refs.assetID + `","hold_id":"` + refs.holdID + `"}`},
		{"kunci tidak valid", []string{"bad key"}, `{"asset_id":"` + refs.assetID + `","hold_id":"` + refs.holdID + `"}`},
		{"asset bukan UUID", []string{"cu-gd-1"}, `{"asset_id":"bukan-uuid","hold_id":"` + refs.holdID + `"}`},
		{"hold bukan UUID", []string{"cu-gd-1"}, `{"asset_id":"` + refs.assetID + `","hold_id":"bukan-uuid"}`},
		{"field tak dikenal", []string{"cu-gd-1"}, `{"asset_id":"` + refs.assetID + `","hold_id":"` + refs.holdID + `","charge":1}`},
		{"hold hilang", []string{"cu-gd-1"}, `{"asset_id":"` + refs.assetID + `"}`},
		{"dokumen ganda", []string{"cu-gd-1"}, `{"asset_id":"` + refs.assetID + `","hold_id":"` + refs.holdID + `"} {}`},
	} {
		if code, body := handoverRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, tc.keys, tc.raw); code != http.StatusBadRequest {
			t.Fatalf("handover %s: want 400, got %d (%v)", tc.name, code, body)
		}
	}
	// Kasir dan nonmember ditolak (403) sebelum cek jendela.
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.cashToken, "cu-gd-cash", handoverBody(refs.assetID, refs.holdID)); code != http.StatusForbidden {
		t.Fatalf("handover kasir: want 403, got %d (%v)", code, body)
	}
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.otherToken, "cu-gd-other", handoverBody(refs.assetID, refs.holdID)); code != http.StatusForbidden {
		t.Fatalf("handover nonmember: want 403, got %d (%v)", code, body)
	}
	// Draft lintas-tenant + draft hilang -> 404 identik.
	var custB, itemB string
	if err := env.boot.QueryRow(
		`INSERT INTO parties (workspace_id, type, display_name) VALUES ($1, 'customer', 'Pelanggan B') RETURNING id`, env.wsB).Scan(&custB); err != nil {
		t.Fatalf("fixture party B: %v", err)
	}
	if err := env.boot.QueryRow(
		`INSERT INTO catalog_items (workspace_id, sku, name, unit, status) VALUES ($1, 'SKU-GD-CU-B', 'Barang B', 'pcs', 'active') RETURNING id`, env.wsB).Scan(&itemB); err != nil {
		t.Fatalf("fixture item B: %v", err)
	}
	grantEntitlement(t, env.boot, env.wsB, "rental.basic", "0.1.0")
	if code, body := activateModule(t, env.srv, env.wsB, "rental.basic", env.otherToken, "0.1.0"); code != http.StatusOK {
		t.Fatalf("aktivasi rental wsB: want 200, got %d (%v)", code, body)
	}
	base := dbCutoff(t, env.boot)
	startsB := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	endsB := base.Add(50 * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body := createDraft(t, env.srv, env.wsB, env.otherToken, "cu-gd-draft-b", draftBody(custB, itemB, startsB, endsB))
	if code != http.StatusCreated {
		t.Fatalf("seed draft wsB: want 201, got %d (%v)", code, body)
	}
	draftB, _ := body["id"].(string)
	if code, body := handover(t, env.srv, env.wsA, draftB, env.ownerToken, "cu-gd-cross", handoverBody(refs.assetID, refs.holdID)); code != http.StatusNotFound {
		t.Fatalf("handover draft lintas-tenant: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "draft tidak ditemukan")
	}
	if code, body := handover(t, env.srv, env.wsA, "11111111-2222-3333-4444-555555555555", env.ownerToken, "cu-gd-missing", handoverBody(refs.assetID, refs.holdID)); code != http.StatusNotFound {
		t.Fatalf("handover draft hilang: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "draft tidak ditemukan")
	}
	// Serah terima dini (sebelum starts) -> 409. HARUS dicek di sini,
	// sebelum penantian apa pun: refs.starts hanya ~6 dtk sesudah seed.
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gd-early", handoverBody(refs.assetID, refs.holdID)); code != http.StatusConflict {
		t.Fatalf("handover dini: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "periode draft belum dimulai")
	}
	// Fakta harga belum ada -> 409.
	noFact := seedCustodyNoFact(t, env, "SKU-GD-NF", "cu-gd-nf", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, noFact.starts)
	if code, body := handover(t, env.srv, env.wsA, noFact.draftID, env.ownerToken, "cu-gd-nf-ho", handoverBody(noFact.assetID, noFact.holdID)); code != http.StatusConflict {
		t.Fatalf("handover tanpa fakta: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "harga yang disepakati belum dicatat")
	}
	// Item diarsipkan sesudah hold dibuat -> 409.
	arch := seedCustodyRefs(t, env, "SKU-GD-AR", "cu-gd-ar", 3*time.Second, 6*time.Second, 50*time.Hour)
	archiveItem(t, env.boot, arch.itemID)
	waitUntil(t, env.boot, arch.starts)
	if code, body := handover(t, env.srv, env.wsA, arch.draftID, env.ownerToken, "cu-gd-ar-ho", handoverBody(arch.assetID, arch.holdID)); code != http.StatusConflict {
		t.Fatalf("handover item arsip: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "barang tidak aktif")
	}
	// Hold kedaluwarsa (fixture) -> 409; hold dilepas -> 409.
	exp := seedCustodyRefs(t, env, "SKU-GD-EX", "cu-gd-ex", 3*time.Second, 6*time.Second, 50*time.Hour)
	expireHoldFixture(t, env.boot, exp.holdID, "-1 hour")
	waitUntil(t, env.boot, exp.starts)
	if code, body := handover(t, env.srv, env.wsA, exp.draftID, env.ownerToken, "cu-gd-ex-ho", handoverBody(exp.assetID, exp.holdID)); code != http.StatusConflict {
		t.Fatalf("handover hold kedaluwarsa: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold tidak aktif")
	}
	rel := seedCustodyRefs(t, env, "SKU-GD-RL", "cu-gd-rl", 3*time.Second, 6*time.Second, 50*time.Hour)
	if code, body := releaseHold(t, env.srv, env.wsA, rel.holdID, env.ownerToken); code != http.StatusOK {
		t.Fatalf("release hold: want 200, got %d (%v)", code, body)
	}
	waitUntil(t, env.boot, rel.starts)
	if code, body := handover(t, env.srv, env.wsA, rel.draftID, env.ownerToken, "cu-gd-rl-ho", handoverBody(rel.assetID, rel.holdID)); code != http.StatusConflict {
		t.Fatalf("handover hold dilepas: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold tidak aktif")
	}
	// Hold milik draft lain / aset lain -> 409; aset hilang -> 404;
	// hold hilang -> 404.
	other := seedCustodyRefs(t, env, "SKU-GD-OT", "cu-gd-ot", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gd-mm1", handoverBody(refs.assetID, other.holdID)); code != http.StatusConflict {
		t.Fatalf("handover hold draft lain: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold tidak cocok")
	}
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gd-mm2", handoverBody(other.assetID, refs.holdID)); code != http.StatusConflict {
		t.Fatalf("handover aset lain: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold tidak cocok")
	}
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gd-mm3", handoverBody("11111111-2222-3333-4444-555555555555", refs.holdID)); code != http.StatusNotFound {
		t.Fatalf("handover aset hilang: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "aset tidak ditemukan")
	}
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gd-mm4", handoverBody(refs.assetID, "11111111-2222-3333-4444-555555555555")); code != http.StatusNotFound {
		t.Fatalf("handover hold hilang: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold tidak ditemukan")
	}
	// Pada/lewat akhir periode (draft pendek + tunggu nyata) -> 409.
	short := seedCustodyRefs(t, env, "SKU-GD-SH", "cu-gd-sh", 1*time.Second, 2*time.Second, 4*time.Second)
	waitUntil(t, env.boot, short.ends)
	if code, body := handover(t, env.srv, env.wsA, short.draftID, env.ownerToken, "cu-gd-sh-ho", handoverBody(short.assetID, short.holdID)); code != http.StatusConflict {
		t.Fatalf("handover lewat akhir: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "periode draft sudah berakhir")
	}
}

// TestCustodyReturnGuards: matriks penolakan return — auth/bentuk,
// peran/tenant, tanpa custody, dan dua bentuk body yang diizinkan
// (kosong atau {} ketat).
func TestCustodyReturnGuards(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-RG-CU", "cu-rg", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-rg-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	refs2 := seedCustodyRefs(t, env, "SKU-RG-2", "cu-rg-2", 3*time.Second, 6*time.Second, 50*time.Hour)

	// Auth/bentuk/header.
	if code, _ := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, "", []string{"cu-rg-1"}, ""); code != http.StatusUnauthorized {
		t.Fatalf("return unauth: want 401, got %d", code)
	}
	if code, _ := returnCustodyRaw(t, env.srv, "bukan-uuid", refs.draftID, env.ownerToken, []string{"cu-rg-1"}, ""); code != http.StatusBadRequest {
		t.Fatalf("return ws buruk: want 400, got %d", code)
	}
	if code, _ := returnCustodyRaw(t, env.srv, env.wsA, "bukan-uuid", env.ownerToken, []string{"cu-rg-1"}, ""); code != http.StatusBadRequest {
		t.Fatalf("return draft buruk: want 400, got %d", code)
	}
	if code, _ := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, nil, ""); code != http.StatusBadRequest {
		t.Fatalf("return tanpa kunci: want 400, got %d", code)
	}
	if code, _ := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"cu-rg-1", "cu-rg-2"}, ""); code != http.StatusBadRequest {
		t.Fatalf("return dua kunci: want 400, got %d", code)
	}
	if code, _ := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"bad key"}, ""); code != http.StatusBadRequest {
		t.Fatalf("return kunci tidak valid: want 400, got %d", code)
	}
	// Body: field apa pun -> 400; hanya kosong atau {} yang lolos
	// (dicek di akhir tes dengan dua draft yang berbeda).
	if code, body := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"cu-rg-1"}, `{"charge":1}`); code != http.StatusBadRequest {
		t.Fatalf("return body charge: want 400, got %d (%v)", code, body)
	}
	if code, body := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"cu-rg-1"}, `{"status":"returned"}`); code != http.StatusBadRequest {
		t.Fatalf("return body status: want 400, got %d (%v)", code, body)
	}
	// Kasir dan nonmember ditolak (403).
	if code, body := returnCustody(t, env.srv, env.wsA, refs.draftID, env.cashToken, "cu-rg-cash"); code != http.StatusForbidden {
		t.Fatalf("return kasir: want 403, got %d (%v)", code, body)
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refs.draftID, env.otherToken, "cu-rg-other"); code != http.StatusForbidden {
		t.Fatalf("return nonmember: want 403, got %d (%v)", code, body)
	}
	// Tanpa custody / draft lintas-tenant / draft hilang -> 404 identik.
	if code, body := returnCustody(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-rg-nocust"); code != http.StatusNotFound {
		t.Fatalf("return tanpa custody: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "custody tidak ditemukan")
	}
	if code, body := returnCustody(t, env.srv, env.wsA, "11111111-2222-3333-4444-555555555555", env.ownerToken, "cu-rg-missing"); code != http.StatusNotFound {
		t.Fatalf("return draft hilang: want 404, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "custody tidak ditemukan")
	}
	// Body {} -> 200; body kosong -> 200 (draft kedua).
	if code, body := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"cu-rg-obj"}, "{}"); code != http.StatusOK {
		t.Fatalf("return body {}: want 200, got %d (%v)", code, body)
	}
	waitUntil(t, env.boot, refs2.starts)
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-rg-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusCreated {
		t.Fatalf("handover kedua: want 201, got %d (%v)", code, body)
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-rg-empty"); code != http.StatusOK {
		t.Fatalf("return body kosong: want 200, got %d (%v)", code, body)
	}
}

// TestCustodyIdempotency: replay handover/return mengembalikan catatan
// saat ini tanpa tulis/audit ganda; kunci yang dipakai ulang untuk
// payload/draft berbeda -> 409; keyspace handover dan return terpisah.
func TestCustodyIdempotency(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-ID-CU", "cu-id", 3*time.Second, 6*time.Second, 50*time.Hour)
	refs2 := seedCustodyRefs(t, env, "SKU-ID-2", "cu-id-2", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)

	code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ho", handoverBody(refs.assetID, refs.holdID))
	if code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	custodyID, _ := body["id"].(string)
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 1 {
		t.Fatalf("audit handover: want 1, got %d", got)
	}
	// Replay kunci sama + identitas sama -> 200 catatan yang sama.
	code, body = handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ho", handoverBody(refs.assetID, refs.holdID))
	if code != http.StatusOK || body["id"] != custodyID || body["status"] != "out" {
		t.Fatalf("handover replay: want 200 out yang sama, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 1 {
		t.Fatalf("audit handover sesudah replay: want 1, got %d", got)
	}
	// Kunci sama, aset/hold/draft berbeda -> 409.
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ho", handoverBody(refs2.assetID, refs.holdID)); code != http.StatusConflict {
		t.Fatalf("handover kunci sama aset beda: want 409, got %d (%v)", code, body)
	}
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-id-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusConflict {
		t.Fatalf("handover kunci sama draft beda: want 409, got %d (%v)", code, body)
	}
	// Kunci berbeda untuk draft yang sudah punya custody -> 409.
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ho-2", handoverBody(refs.assetID, refs.holdID)); code != http.StatusConflict {
		t.Fatalf("handover kunci beda draft sudah custody: want 409, got %d (%v)", code, body)
	}
	// Return + replay return.
	code, body = returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ret")
	if code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("return: want 200 returned, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit return: want 1, got %d", got)
	}
	code, body = returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ret")
	if code != http.StatusOK || body["id"] != custodyID || body["status"] != "returned" {
		t.Fatalf("return replay: want 200 returned yang sama, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit return sesudah replay: want 1, got %d", got)
	}
	// Replay handover SESUDAH return -> 200 catatan SAAT INI (returned).
	code, body = handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ho", handoverBody(refs.assetID, refs.holdID))
	if code != http.StatusOK || body["id"] != custodyID || body["status"] != "returned" {
		t.Fatalf("handover replay sesudah return: want 200 returned, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 1 {
		t.Fatalf("audit handover sesudah replay-return: want 1, got %d", got)
	}
	// Return kunci berbeda pada custody yang sudah returned -> 409.
	if code, body := returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-id-ret-2"); code != http.StatusConflict {
		t.Fatalf("return kunci beda sesudah returned: want 409, got %d (%v)", code, body)
	}
	// Kunci return lintas-draft -> 409 (keyspace return terpisah dari
	// handover, tetapi satu kunci return hanya untuk satu draft).
	waitUntil(t, env.boot, refs2.starts)
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-id-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusCreated {
		t.Fatalf("handover kedua: want 201, got %d (%v)", code, body)
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-id-ret"); code != http.StatusConflict {
		t.Fatalf("return kunci lintas-draft: want 409, got %d (%v)", code, body)
	}
	// Kunci handover boleh dipakai ulang sebagai kunci return (keyspace
	// terpisah) — return refs2 dengan kunci "cu-id-ho" -> 200.
	if code, body := returnCustody(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-id-ho"); code != http.StatusOK {
		t.Fatalf("return kunci handover: want 200 (keyspace terpisah), got %d (%v)", code, body)
	}
}

// TestCustodyConcurrent: dua staf serah terima unit sama bersamaan —
// satu 201 + satu 409, satu custody, satu audit; dua return kunci sama
// bersamaan — dua 200, satu audit.
func TestCustodyConcurrent(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-CC-CU", "cu-cc", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)

	type raceResult struct {
		code int
		body map[string]any
		err  error
	}
	run := func(method, path, token string, keys []string, body any) chan raceResult {
		ch := make(chan raceResult, 1)
		go func() {
			code, b, err := callHoldAPI(env.srv, method, path, token, keys, body)
			ch <- raceResult{code, b, err}
		}()
		return ch
	}
	hoPath := "/workspaces/" + env.wsA + "/rental/contracts/" + refs.draftID + "/handover"
	ch1 := run("POST", hoPath, env.ownerToken, []string{"cu-cc-ho-1"}, handoverBody(refs.assetID, refs.holdID))
	ch2 := run("POST", hoPath, env.mgrToken, []string{"cu-cc-ho-2"}, handoverBody(refs.assetID, refs.holdID))
	r1, r2 := <-ch1, <-ch2
	if r1.err != nil || r2.err != nil {
		t.Fatalf("handover konkuren: err %v / %v", r1.err, r2.err)
	}
	codes := map[int]int{r1.code: 1, r2.code: 1}
	if len(codes) != 2 || codes[http.StatusCreated] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("handover konkuren: want satu 201 + satu 409, got %d + %d", r1.code, r2.code)
	}
	if got := countRows(t, env.boot, "rental_custody", env.wsA); got != 1 {
		t.Fatalf("custody konkuren: want 1, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 1 {
		t.Fatalf("audit handover konkuren: want 1, got %d", got)
	}

	retPath := "/workspaces/" + env.wsA + "/rental/contracts/" + refs.draftID + "/return"
	ch3 := run("POST", retPath, env.ownerToken, []string{"cu-cc-ret"}, nil)
	ch4 := run("POST", retPath, env.mgrToken, []string{"cu-cc-ret"}, nil)
	r3, r4 := <-ch3, <-ch4
	if r3.err != nil || r4.err != nil {
		t.Fatalf("return konkuren: err %v / %v", r3.err, r4.err)
	}
	if r3.code != http.StatusOK || r4.code != http.StatusOK {
		t.Fatalf("return konkuren kunci sama: want 200 + 200, got %d + %d", r3.code, r4.code)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit return konkuren: want 1, got %d", got)
	}
	if c, ok := readCustody(t, env.boot, env.wsA, r3.body["id"].(string)); !ok || c.Status != "returned" {
		t.Fatalf("custody sesudah return konkuren: want returned, got %+v", c)
	}
}

// TestCustodyOutAfterEnds: unit masih keluar sesudah akhir periode yang
// direncanakan DAN TTL hold — availability false untuk periode apa pun,
// hold baru 409, handover draft lain 409; histori + hold consumed tidak
// berubah. Akhir periode dilewati nyata (draft pendek); TTL hold yang
// sudah consumed digeser fixture (pola expireHoldFixture F2-01-C).
func TestCustodyOutAfterEnds(t *testing.T) {
	env := setupCustody(t)
	refs1 := seedCustodyRefs(t, env, "SKU-OE-1", "cu-oe-1", 3*time.Second, 6*time.Second, 8*time.Second)
	// Draft kedua memakai item + aset yang SAMA, periode sesudah draft
	// pertama (hold tidak overlap), lengkap dengan fakta + hold — semua
	// SEBELUM handover pertama, karena hold baru diblokir sesudahnya.
	d2Starts := refs1.ends.Add(1 * time.Second)
	d2Ends := refs1.ends.Add(50 * time.Hour)
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "cu-oe-2-draft",
		draftBody(refs1.custID, refs1.itemID, d2Starts.UTC().Format(time.RFC3339Nano), d2Ends.UTC().Format(time.RFC3339Nano)))
	if code != http.StatusCreated {
		t.Fatalf("draft kedua: want 201, got %d (%v)", code, body)
	}
	draft2, _ := body["id"].(string)
	code, quote2 := getQuote(t, env.srv, env.wsA, draft2, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("quote kedua: want 200, got %d (%v)", code, quote2)
	}
	version2, _ := quote2["price_version_id"].(string)
	total2, _ := quote2["total_minor"].(string)
	if code, body := acceptPrice(t, env.srv, env.wsA, draft2, env.ownerToken, "cu-oe-2-accept", acceptBody(version2, total2)); code != http.StatusCreated {
		t.Fatalf("accept kedua: want 201, got %d (%v)", code, body)
	}
	hold2ID, _ := seedHold(t, env, draft2, refs1.assetID, "cu-oe-2-hold")

	waitUntil(t, env.boot, refs1.starts)
	code, body = handover(t, env.srv, env.wsA, refs1.draftID, env.ownerToken, "cu-oe-1-ho", handoverBody(refs1.assetID, refs1.holdID))
	if code != http.StatusCreated {
		t.Fatalf("handover pertama: want 201, got %d (%v)", code, body)
	}
	handedOut, _ := body["handed_out_at"].(string)
	// Lewati akhir periode secara nyata, lalu geser TTL hold consumed.
	waitUntil(t, env.boot, refs1.ends)
	expireHoldFixture(t, env.boot, refs1.holdID, "-1 hour")

	code, body = getCustody(t, env.srv, env.wsA, refs1.draftID, env.ownerToken)
	if code != http.StatusOK || body["status"] != "out" || body["handed_out_at"] != handedOut {
		t.Fatalf("custody sesudah akhir periode: want out tanpa perubahan, got %d (%v)", code, body)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refs1.holdID); !ok || h.Status != "consumed" {
		t.Fatalf("hold sesudah akhir periode: want consumed, got %+v", h)
	}
	for _, tc := range []struct {
		name     string
		from, to time.Time
	}{
		{"periode draft kedua", d2Starts, d2Starts.Add(1 * time.Hour)},
		{"periode jauh", refs1.ends.Add(100 * time.Hour), refs1.ends.Add(110 * time.Hour)},
	} {
		code, body = getAvailability(t, env.srv, env.wsA, refs1.assetID, env.ownerToken,
			availQuery(tc.from.UTC().Format(time.RFC3339Nano), tc.to.UTC().Format(time.RFC3339Nano)))
		if code != http.StatusOK || body["available"] != false {
			t.Fatalf("availability %s saat keluar: want false, got %d (%v)", tc.name, code, body)
		}
	}
	// Hold baru untuk draft ketiga -> 409 (unit keluar).
	code, body = createDraft(t, env.srv, env.wsA, env.ownerToken, "cu-oe-3-draft",
		draftBody(refs1.custID, refs1.itemID, refs1.ends.Add(20*time.Hour).UTC().Format(time.RFC3339Nano), refs1.ends.Add(30*time.Hour).UTC().Format(time.RFC3339Nano)))
	if code != http.StatusCreated {
		t.Fatalf("draft ketiga: want 201, got %d (%v)", code, body)
	}
	draft3, _ := body["id"].(string)
	if code, body := createHold(t, env.srv, env.wsA, draft3, env.ownerToken, "cu-oe-3-hold", map[string]string{"asset_id": refs1.assetID}); code != http.StatusConflict {
		t.Fatalf("hold baru saat keluar: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "unit sedang dipinjam")
	}
	// Handover draft kedua (jendela sudah terbuka, hold live) -> 409
	// karena unit sedang dipinjam — bukan karena jendela/hold.
	waitUntil(t, env.boot, d2Starts)
	if code, body := handover(t, env.srv, env.wsA, draft2, env.ownerToken, "cu-oe-2-ho", handoverBody(refs1.assetID, hold2ID)); code != http.StatusConflict {
		t.Fatalf("handover draft kedua saat keluar: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "unit sedang dipinjam")
	}
	_ = hold2ID
}

// TestCustodyReturnAfterRevoke: return 200 sesudah revoke/deactivate
// (guard menyempit, tanpa gate Rental), sementara handover baru 403 dan
// histori tetap terbaca.
func TestCustodyReturnAfterRevoke(t *testing.T) {
	env := setupCustody(t)
	refs1 := seedCustodyRefs(t, env, "SKU-RV-1", "cu-rv-1", 3*time.Second, 6*time.Second, 50*time.Hour)
	refs2 := seedCustodyRefs(t, env, "SKU-RV-2", "cu-rv-2", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs2.starts)
	if code, body := handover(t, env.srv, env.wsA, refs1.draftID, env.ownerToken, "cu-rv-1-ho", handoverBody(refs1.assetID, refs1.holdID)); code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	if code, body := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, body)
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refs1.draftID, env.ownerToken, "cu-rv-1-ret"); code != http.StatusOK {
		t.Fatalf("return sesudah revoke: want 200, got %d (%v)", code, body)
	} else if body["status"] != "returned" {
		t.Fatalf("return sesudah revoke: want returned, got %v", body)
	}
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-rv-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusForbidden {
		t.Fatalf("handover sesudah revoke: want 403, got %d (%v)", code, body)
	}
	if code, body := getCustody(t, env.srv, env.wsA, refs1.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("GET custody sesudah revoke: want 200 returned, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit return sesudah revoke: want 1, got %d", got)
	}
}

// TestCustodyGuardFaults: fault izin -> 500 tanpa custody/hold/audit;
// sesudah restore -> sukses. Fault audit -> 500 tanpa custody/audit;
// sesudah lepas trigger -> sukses.
func TestCustodyGuardFaults(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-GF-CU", "cu-gf", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)

	// Fault manage_business_data HARUS dipulihkan lewat t.Cleanup juga
	// (pelajaran F2-01-E: Fatalf sebelum restore eksplisit meninggalkan
	// fault di umnia_test dan mempolusi tes berikutnya).
	def := installPermissionFaultPerm(t, env.boot, "manage_business_data")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gf-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusInternalServerError {
		t.Fatalf("handover fault izin: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "izin gagal diperiksa")
	}
	if got := countRows(t, env.boot, "rental_custody", env.wsA); got != 0 {
		t.Fatalf("custody fault izin: want 0, got %d", got)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refs.holdID); !ok || h.Status != "held" {
		t.Fatalf("hold fault izin: want held, got %+v", h)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); got != 0 {
		t.Fatalf("audit fault izin: want 0, got %d", got)
	}
	restorePermissionFault(t, env.boot, def)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gf-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusCreated {
		t.Fatalf("handover sesudah restore: want 201, got %d (%v)", code, body)
	}

	def2 := installPermissionFaultPerm(t, env.boot, "manage_business_data")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def2) })
	if code, body := returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gf-ret"); code != http.StatusInternalServerError {
		t.Fatalf("return fault izin: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "izin gagal diperiksa")
	}
	if code, body := getCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "out" {
		t.Fatalf("custody fault return: want out, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 0 {
		t.Fatalf("audit return fault izin: want 0, got %d", got)
	}
	restorePermissionFault(t, env.boot, def2)
	if code, body := returnCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-gf-ret"); code != http.StatusOK {
		t.Fatalf("return sesudah restore: want 200, got %d (%v)", code, body)
	}

	// Fault audit: trigger gagalkan INSERT audit handover/return.
	refs2 := seedCustodyRefs(t, env, "SKU-GF-2", "cu-gf-2", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs2.starts)
	if _, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_custody_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action IN ('rental_handed_over', 'rental_returned') THEN
				RAISE EXCEPTION 'synthetic audit failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`); err != nil {
		t.Fatalf("buat fungsi fault audit: %v", err)
	}
	if _, err := env.boot.Exec(`CREATE TRIGGER qa_fault_custody_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_custody_audit()`); err != nil {
		t.Fatalf("pasang trigger fault audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_custody_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_custody_audit()`)
	})
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-gf-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusInternalServerError {
		t.Fatalf("handover fault audit: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "audit gagal ditulis")
	}
	if got := countRows(t, env.boot, "rental_custody", env.wsA); got != 1 {
		t.Fatalf("custody fault audit: want 1 (hanya draft pertama), got %d", got)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refs2.holdID); !ok || h.Status != "held" {
		t.Fatalf("hold fault audit: want held, got %+v", h)
	}
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_custody_audit ON audit_events`); err != nil {
		t.Fatalf("lepas trigger fault audit: %v", err)
	}
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-gf-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusCreated {
		t.Fatalf("handover sesudah lepas fault audit: want 201, got %d (%v)", code, body)
	}
}

// TestCustodyLockWaitRaces: handover yang menunggu workspace lock
// membaca keadaan SESUDAH lock — deactivate/revoke selagi menunggu ->
// 403; return yang menunggu lock -> 200 sesudah lock dilepas.
func TestCustodyLockWaitRaces(t *testing.T) {
	env := setupCustody(t)
	refsA := seedCustodyRefs(t, env, "SKU-LW-A", "cu-lw-a", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsB := seedCustodyRefs(t, env, "SKU-LW-B", "cu-lw-b", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsB.starts)

	type raceResult struct {
		code int
		body map[string]any
		err  error
	}
	waitHandover := func(draft, key, asset, hold string) chan raceResult {
		ch := make(chan raceResult, 1)
		go func() {
			code, body, err := callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/handover", env.ownerToken, []string{key}, handoverBody(asset, hold))
			ch <- raceResult{code, body, err}
		}()
		return ch
	}
	waitReturn := func(draft, key string) chan raceResult {
		ch := make(chan raceResult, 1)
		go func() {
			code, body, err := callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/return", env.ownerToken, []string{key}, nil)
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

	// (a) Deactivate selagi handover menunggu lock -> 403.
	// Mutasi lewat lockTx yang sama (bukan koneksi lain): satu
	// transaksi memegang workspace lock + menonaktifkan modul.
	lockTx := holdLock()
	ch := waitHandover(refsA.draftID, "cu-lw-a", refsA.assetID, refsA.holdID)
	time.Sleep(500 * time.Millisecond)
	if _, err := lockTx.Exec(`UPDATE module_installations SET status = 'inactive', updated_at = now() WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA); err != nil {
		lockTx.Rollback()
		t.Fatalf("deactivate selagi menunggu: %v", err)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (a): %v", err)
	}
	res := <-ch
	if res.err != nil {
		t.Fatalf("handover (a): %v", res.err)
	}
	if res.code != http.StatusForbidden {
		t.Fatalf("handover (a): want 403, got %d (%v)", res.code, res.body)
	}
	assertFlatError(t, res.body, "rental belum aktif di workspace ini")
	// Aktifkan lagi untuk subtes berikut (UPDATE langsung, bukan API).
	if _, err := env.boot.Exec(`UPDATE module_installations SET status = 'active', updated_at = now() WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA); err != nil {
		t.Fatalf("reaktivasi: %v", err)
	}

	// (b) Revoke entitlement selagi handover menunggu lock -> 403.
	// Mutasi dan kepemilikan lock berada dalam SATU transaksi yang
	// sama (lockTx): lockTx memegang workspace lock DAN menjalankan
	// DELETE entitlement, lalu lockTx.Commit. Jangan panggil
	// revokeEntitlement() di sini — helper itu membuka transaksi
	// kedua dan mencoba mengambil lock yang sama, sehingga deadlock.
	// DELETE pada tabel perujuk tidak mengunci baris workspaces yang
	// dirujuk, jadi DELETE lewat lockTx aman.
	lockTx = holdLock()
	ch = waitHandover(refsB.draftID, "cu-lw-b", refsB.assetID, refsB.holdID)
	time.Sleep(500 * time.Millisecond)
	if _, err := lockTx.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA); err != nil {
		lockTx.Rollback()
		t.Fatalf("revoke selagi menunggu: %v", err)
	}
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (b): %v", err)
	}
	res = <-ch
	if res.err != nil {
		t.Fatalf("handover (b): %v", res.err)
	}
	if res.code != http.StatusForbidden {
		t.Fatalf("handover (b): want 403, got %d (%v)", res.code, res.body)
	}
	assertFlatError(t, res.body, "rental belum menjadi hak workspace ini")
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")

	// (c) Handover dulu (sukses), lalu return menunggu lock -> 200.
	if code, body := handover(t, env.srv, env.wsA, refsA.draftID, env.ownerToken, "cu-lw-a", handoverBody(refsA.assetID, refsA.holdID)); code != http.StatusCreated {
		t.Fatalf("handover (c): want 201, got %d (%v)", code, body)
	}
	lockTx = holdLock()
	ch = waitReturn(refsA.draftID, "cu-lw-ret")
	time.Sleep(500 * time.Millisecond)
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx (c): %v", err)
	}
	res = <-ch
	if res.err != nil {
		t.Fatalf("return (c): %v", res.err)
	}
	if res.code != http.StatusOK || res.body["status"] != "returned" {
		t.Fatalf("return (c): want 200 returned, got %d (%v)", res.code, res.body)
	}
}

// TestCustodyHoldReleaseConsumed: release hold yang sudah consumed ->
// 409 eksplisit; histori hold menampilkan consumed (bukan expired)
// walau deadline lewat; release hold held biasa tetap 200.
func TestCustodyHoldReleaseConsumed(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-HR-CU", "cu-hr", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-hr-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	if code, body := releaseHold(t, env.srv, env.wsA, refs.holdID, env.ownerToken); code != http.StatusConflict {
		t.Fatalf("release hold consumed: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "hold sudah dipakai serah terima")
	}
	findHold := func() (string, bool) {
		t.Helper()
		code, body := listHolds(t, env.srv, env.wsA, env.ownerToken, "")
		if code != http.StatusOK {
			t.Fatalf("list holds: want 200, got %d (%v)", code, body)
		}
		items, _ := body["items"].([]any)
		for _, it := range items {
			m, _ := it.(map[string]any)
			if m["id"] == refs.holdID {
				s, _ := m["status"].(string)
				return s, true
			}
		}
		return "", false
	}
	if s, ok := findHold(); !ok || s != "consumed" {
		t.Fatalf("status hold sesudah handover: want consumed, got %q (found=%v)", s, ok)
	}
	// Deadline lewat (fixture): consumed tetap consumed, tidak pernah
	// diubah menjadi expired oleh cleanup.
	expireHoldFixture(t, env.boot, refs.holdID, "-1 hour")
	if s, ok := findHold(); !ok || s != "consumed" {
		t.Fatalf("status hold consumed sesudah deadline: want consumed, got %q (found=%v)", s, ok)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refs.holdID); !ok || h.Status != "consumed" {
		t.Fatalf("hold mentah: want consumed, got %+v", h)
	}
	// Perilaku lama tidak berubah: release hold held -> 200.
	refs2 := seedCustodyRefs(t, env, "SKU-HR-2", "cu-hr-2", 3*time.Second, 6*time.Second, 50*time.Hour)
	if code, body := releaseHold(t, env.srv, env.wsA, refs2.holdID, env.ownerToken); code != http.StatusOK {
		t.Fatalf("release hold held: want 200, got %d (%v)", code, body)
	} else if body["status"] != "released" {
		t.Fatalf("release hold held: want released, got %v", body)
	}
}
