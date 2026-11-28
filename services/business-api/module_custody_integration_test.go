package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Tes integrasi custody serah terima + pengembalian F2-01-F (migrasi 010)
// + resolusi rusak/hilang F2-01-G (migrasi 012) terhadap PostgreSQL
// asli: skema segar + rerun + upgrade, RLS/hak runtime/FK komposit/
// partial UNIQUE, HTTP handover/return/custody, guard Rental +
// jendela waktu terencana, idempotency dua keyspace + outcome,
// konkurensi, custody-terbuka-memblokir-availability/hold/handover,
// aset-rusak/hilang-memblokir-availability/hold/handover, return
// sesudah revoke/deactivate, dan fault guard/audit/tulis. Entitlement
// di sini HANYA fixture sintetis terisolasi lewat bootstrap (bukan
// grant nyata). Custody adalah catatan staf in-app — BUKAN
// consent/tanda tangan pelanggan, perjanjian hukum, invoice,
// pembayaran, posting ledger, atau transisi draft. Draft, harga, dan
// fakta harga tidak dimutasi. Hold TTL bukan pengembalian fisik;
// periode berakhir bukan unit kembali.

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

// returnCustodyOutcome memanggil POST return dengan body
// {"outcome": "<outcome>"}.
func returnCustodyOutcome(t *testing.T, srv *httptest.Server, ws, draft, token, key, outcome string) (int, map[string]any) {
	t.Helper()
	return returnCustodyRaw(t, srv, ws, draft, token, []string{key}, `{"outcome":"`+outcome+`"}`)
}

// readAssetCondition membaca kondisi + updated_at satu aset (waktu
// sebagai time.Time supaya perbandingan memakai instan, bukan format
// string).
func readAssetCondition(t *testing.T, boot *sql.DB, ws, id string) (string, time.Time) {
	t.Helper()
	var cond string
	var upd time.Time
	if err := boot.QueryRow(`SELECT condition, updated_at FROM physical_assets WHERE workspace_id = $1 AND id = $2`, ws, id).Scan(&cond, &upd); err != nil {
		t.Fatalf("read asset condition %s: %v", id, err)
	}
	return cond, upd
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
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, AssetID, HoldID, ClientRequestID, ReturnClientRequestID, Status, HandedOutAt, ReturnedAt, HandedOutBy, ReturnedBy, ResolvedAt, ResolvedBy string
}

func readCustody(t *testing.T, boot *sql.DB, ws, id string) (storedCustody, bool) {
	t.Helper()
	var c storedCustody
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, rental_agreed_price_id::text, asset_id::text,
			hold_id::text, client_request_id, COALESCE(return_client_request_id, ''), status,
			handed_out_at::text, COALESCE(returned_at::text, ''), handed_out_by::text, COALESCE(returned_by::text, ''),
			COALESCE(resolved_at::text, ''), COALESCE(resolved_by::text, '')
		 FROM rental_custody WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&c.ID, &c.WorkspaceID, &c.RentalContractID, &c.RentalAgreedPriceID, &c.AssetID,
		&c.HoldID, &c.ClientRequestID, &c.ReturnClientRequestID, &c.Status,
		&c.HandedOutAt, &c.ReturnedAt, &c.HandedOutBy, &c.ReturnedBy, &c.ResolvedAt, &c.ResolvedBy)
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
		"custody":     `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || rental_contract_id::text || '|' || rental_agreed_price_id::text || '|' || asset_id::text || '|' || hold_id::text || '|' || client_request_id || '|' || coalesce(return_client_request_id,'') || '|' || status || '|' || handed_out_at::text || '|' || coalesce(returned_at::text,'') || '|' || handed_out_by::text || '|' || coalesce(returned_by::text,'') || '|' || coalesce(resolved_at::text,'') || '|' || coalesce(resolved_by::text,''), chr(10) ORDER BY id), '') FROM rental_custody`,
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
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments,014_finance_operator_entitlement,015_inventory_basic" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments,014_finance_operator_entitlement,015_inventory_basic, got %s", markers)
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
		"client_request_id", "return_client_request_id", "status", "handed_out_at", "returned_at", "handed_out_by", "returned_by",
		"resolved_at", "resolved_by"}
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
		{"status", true}, {"returned_at", true}, {"returned_by", true}, {"resolved_at", true}, {"resolved_by", true}, {"return_client_request_id", true},
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
	if !strings.Contains(qual, "status = 'out'") || !strings.Contains(withCheck, "'returned'") || !strings.Contains(withCheck, "'damaged'") || !strings.Contains(withCheck, "'lost'") {
		t.Fatalf("kebijakan update rental_custody: USING harus status='out', WITH CHECK status IN ('returned','damaged','lost'); got USING %q WITH CHECK %q", qual, withCheck)
	}
	// CHECK 7 (kunci, kunci return, status, finite, state, returned>=
	// handed_out, resolved>=handed_out) + UNIQUE 3 + FK 8 + EXCLUDE 0
	// pada rental_custody (012 menambah CHECK resolved_after_handover
	// dan FK resolved_by -> identity_subjects).
	var c, u, f, x int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f'), count(*) FILTER (WHERE contype = 'x')
		 FROM pg_constraint WHERE conrelid = 'rental_custody'::regclass`).Scan(&c, &u, &f, &x); err != nil {
		t.Fatalf("constraints rental_custody: %v", err)
	}
	if c != 7 || u != 3 || f != 8 || x != 0 {
		t.Fatalf("constraints rental_custody: want CHECK 7 + UNIQUE 3 + FK 8 + EXCLUDE 0, got CHECK %d + UNIQUE %d + FK %d + EXCLUDE %d", c, u, f, x)
	}
	for _, name := range []string{"rental_custody_finite_times", "rental_custody_state_check", "rental_custody_returned_after_handover", "rental_custody_resolved_after_handover"} {
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

// TestCustodyRerunPreservesData: rerun 012 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// kebijakan, ACL, constraint, dan marker; rerun 010 lama (legacy)
// sesudah 012 meregresi CHECK/kebijakan custody ke versi 010 —
// karena itu 012 harus diterapkan lagi sebelum keadaan akhir
// ditegaskan; rerun kumulatif 001-012 sesudahnya mengembalikan
// keadaan akhir (kebijakan hold tetap mengizinkan consumed, kebijakan
// custody tetap tiga status terminal).
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
	// mengembalikan versi 007; 012 memiliki CHECK/kebijakan custody
	// tiga status + kebijakan/grant UPDATE aset. Normalisasi memakai
	// urutan kumulatif (010 lalu 012) supaya keadaan akhir adalah
	// yang ditetapkan 012 — bukan state yang diregresi file lain.
	if code, out := runPsqlFile(t, bootURL, "migrations/010_rental_custody.sql"); code != 0 {
		t.Fatalf("normalisasi 010: want exit 0, got %d (%s)", code, out)
	}
	if code, out := runPsqlFile(t, bootURL, "migrations/012_rental_damaged_lost_return.sql"); code != 0 {
		t.Fatalf("normalisasi 012: want exit 0, got %d (%s)", code, out)
	}
	before := custodySnapshot(t, boot)
	if !strings.Contains(before["custody"], "rerun-custody-10") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture custody")
	}

	code, out := runPsqlFile(t, bootURL, "migrations/012_rental_damaged_lost_return.sql")
	if code != 0 {
		t.Fatalf("rerun 012: want exit 0, got %d (%s)", code, out)
	}
	after := custodySnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun 012 mengubah state:\nsebelum=%v\nsesudah=%v", before, after)
	}

	// Rerun 010 lama sesudah 012 meregresi CHECK/kebijakan custody
	// ke versi 010 (satu status terminal); 012 harus diterapkan lagi
	// sebelum keadaan akhir ditegaskan — ini aturan yang sama dengan
	// rerun 007 (bukti: snapshot sesudah 010+012 sama dengan sebelum).
	code, out = runPsqlFile(t, bootURL, "migrations/010_rental_custody.sql")
	if code != 0 {
		t.Fatalf("rerun 010 legacy: want exit 0, got %d (%s)", code, out)
	}
	code, out = runPsqlFile(t, bootURL, "migrations/012_rental_damaged_lost_return.sql")
	if code != 0 {
		t.Fatalf("pulihkan 012 sesudah rerun 010: want exit 0, got %d (%s)", code, out)
	}
	after = custodySnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun 010 + pulihkan 012 mengubah state:\nsebelum=%v\nsesudah=%v", before, after)
	}

	// Rerun kumulatif 001-012 (autocommit, urutan file): keadaan akhir
	// harus sama — rerun 007 di tengah tidak boleh menghilangkan
	// 'consumed' dari kebijakan hold karena 010 berjalan sesudahnya,
	// dan rerun 010 di tengah tidak boleh menghilangkan tiga status
	// terminal karena 012 berjalan terakhir.
	for _, f := range []string{"001_init.sql", "002_identity.sql", "003_modules.sql", "004_module_installations.sql", "005_core_master_data.sql", "006_rental_contracts.sql", "007_asset_availability.sql", "008_price_versions.sql", "009_rental_agreed_prices.sql", "010_rental_custody.sql", "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql"} {
		code, out := runPsqlFile(t, bootURL, "migrations/"+f)
		if code != 0 {
			t.Fatalf("rerun kumulatif %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	final := custodySnapshot(t, boot)
	if !reflect.DeepEqual(before, final) {
		t.Fatalf("rerun kumulatif 001-012 mengubah state:\nsebelum=%v\nsesudah=%v", before, final)
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
		 return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by,
		 resolved_at, resolved_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)`
	// Nilai valid bersama: out, handed_out 2026-11-10T02:00Z.
	valid := func(draft, fact, asset, hold, key string) []any {
		return []any{wsA, draft, fact, asset, hold, key, nil, "out", "2026-11-10T02:00:00Z", nil, env.ownerID, nil, nil, nil}
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
		{"status lain", func(a []any) { a[7] = "archived" }},
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
		{"returned tanpa resolved", func(a []any) {
			a[7] = "returned"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
		}},
		{"returned resolved beda waktu", func(a []any) {
			a[7] = "returned"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
			a[12] = "2026-11-12T02:00:00Z"
			a[13] = env.ownerID
		}},
		{"returned resolved beda aktor", func(a []any) {
			a[7] = "returned"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
			a[12] = "2026-11-11T02:00:00Z"
			a[13] = env.mgrID
		}},
		{"damaged tanpa resolved", func(a []any) {
			a[7] = "damaged"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
		}},
		{"damaged resolved infinity", func(a []any) {
			a[7] = "damaged"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
			a[12] = "infinity"
			a[13] = env.ownerID
		}},
		{"damaged resolved lebih awal", func(a []any) {
			a[7] = "damaged"
			a[9] = "2026-11-11T02:00:00Z"
			a[11] = env.ownerID
			a[6] = "rls-cu-ret-x"
			a[12] = "2026-11-09T02:00:00Z"
			a[13] = env.ownerID
		}},
		{"lost dengan returned_at", func(a []any) {
			a[7] = "lost"
			a[9] = "2026-11-11T02:00:00Z"
			a[12] = "2026-11-11T02:00:00Z"
			a[13] = env.ownerID
			a[6] = "rls-cu-ret-x"
		}},
		{"lost tanpa resolved", func(a []any) { a[7] = "lost"; a[6] = "rls-cu-ret-x" }},
		{"out dengan resolved_at", func(a []any) { a[12] = "2026-11-11T02:00:00Z" }},
		{"out dengan resolved_by", func(a []any) { a[13] = env.ownerID }},
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
	// Baris returned valid + duplikat return key 23505. Baris
	// returned valid harus mengisi resolved_* SAMA dengan returned_*
	// (aturan kesamaan 012) — tanpa itu CHECK state baru menolak.
	retArgs := func(draft, fact, asset, hold, key, retKey string) []any {
		return []any{wsA, draft, fact, asset, hold, key, retKey, "returned", "2026-11-10T02:00:00Z", "2026-11-11T02:00:00Z", env.ownerID, env.ownerID, "2026-11-11T02:00:00Z", env.ownerID}
	}
	if code, _, err := tryExec(wsA, env.ownerID, insertSQL, retArgs(draft3, fact3, assetA, hold3, "rls-cu-ret-3", "rls-cu-retkey-1")...); code != "" || err != nil {
		t.Fatalf("INSERT returned: want sukses, got code %q err %v", code, err)
	}
	if code, _, _ := tryExec(wsA, env.ownerID, insertSQL, retArgs(draft4, fact4, assetB, hold4, "rls-cu-ret-4", "rls-cu-retkey-1")...); code != "23505" {
		t.Fatalf("INSERT return key duplikat: want 23505, got %q", code)
	}
	// Baris damaged/lost valid (draft baru + fakta + hold baru; aset
	// D tanpa custody keluar supaya partial UNIQUE tidak menutup).
	draft5 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-5")
	draft6 := mkDraft(wsA, env.ownerToken, "rls-cu-draft-6")
	fact5 := mustFact(draft5, "rls-cu-fact-5")
	fact6 := mustFact(draft6, "rls-cu-fact-6")
	hold5 := mkHold(draft5, assetD, "rls-cu-hold-5", "2026-11-10T02:00:00Z", "2026-11-12T02:00:00Z")
	hold6 := mkHold(draft6, assetD, "rls-cu-hold-6", "2026-11-12T02:00:00Z", "2026-11-14T02:00:00Z")
	termArgs := func(draft, fact, asset, hold, key, retKey, status string) []any {
		if status == "lost" {
			// Lost: returned_* NULL; resolved_* + kunci terisi.
			return []any{wsA, draft, fact, asset, hold, key, retKey, "lost", "2026-11-10T02:00:00Z", nil, env.ownerID, nil, "2026-11-11T02:00:00Z", env.ownerID}
		}
		// Damaged: returned_* == resolved_* (aturan kesamaan 012).
		return []any{wsA, draft, fact, asset, hold, key, retKey, status, "2026-11-10T02:00:00Z", "2026-11-11T02:00:00Z", env.ownerID, env.ownerID, "2026-11-11T02:00:00Z", env.ownerID}
	}
	if code, _, err := tryExec(wsA, env.ownerID, insertSQL, termArgs(draft5, fact5, assetD, hold5, "rls-cu-dmg-5", "rls-cu-retkey-5", "damaged")...); code != "" || err != nil {
		t.Fatalf("INSERT damaged: want sukses, got code %q err %v", code, err)
	}
	if code, _, err := tryExec(wsA, env.ownerID, insertSQL, termArgs(draft6, fact6, assetD, hold6, "rls-cu-lost-6", "rls-cu-retkey-6", "lost")...); code != "" || err != nil {
		t.Fatalf("INSERT lost: want sukses, got code %q err %v", code, err)
	}
	// Lima custody valid yang bertahan (2 out + 1 returned + 1 damaged + 1 lost).
	if got := countRows(t, boot, "rental_custody", wsA); got != 5 {
		t.Fatalf("custody wsA: want 5, got %d", got)
	}
	// UPDATE: transisi return pada baris out diizinkan (kolom grant +
	// kebijakan USING status='out'); resolved_* wajib diisi sama
	// dengan returned_* (aturan kesamaan 012). UPDATE handed_out_at
	// 42501; UPDATE pada baris returned 0 baris (difilter USING);
	// DELETE dan TRUNCATE 42501.
	if code, n, err := tryExec(wsA, env.ownerID, `UPDATE rental_custody SET status = 'returned', returned_at = '2026-11-11T02:00:00Z', returned_by = $2, resolved_at = '2026-11-11T02:00:00Z', resolved_by = $2, return_client_request_id = 'rls-cu-ret-2' WHERE workspace_id = $1 AND rental_contract_id = $3`, wsA, env.ownerID, draft2); code != "" || err != nil || n != 1 {
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

// TestCustodyDamagedLostOutcomes: tiga outcome terminal (utuh eksplisit,
// damaged, lost) — DTO eksak (returned_at/resolved_*), kondisi aset,
// audit per outcome, fakta/draft/hold tidak berubah.
func TestCustodyDamagedLostOutcomes(t *testing.T) {
	env := setupCustody(t)
	refsD := seedCustodyRefs(t, env, "SKU-DMG-CU", "cu-dmg", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsL := seedCustodyRefs(t, env, "SKU-LOST-CU", "cu-lost", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsR := seedCustodyRefs(t, env, "SKU-RET-CU", "cu-ret", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsR.starts)
	for _, tc := range []struct {
		refs custodyRefs
		key  string
	}{
		{refsD, "cu-dmg-ho"}, {refsL, "cu-lost-ho"}, {refsR, "cu-ret-ho"},
	} {
		if code, body := handover(t, env.srv, env.wsA, tc.refs.draftID, env.ownerToken, tc.key, handoverBody(tc.refs.assetID, tc.refs.holdID)); code != http.StatusCreated {
			t.Fatalf("handover %s: want 201, got %d (%v)", tc.key, code, body)
		}
	}
	// Aset refsR: updated_at sebelum resolusi (return utuh TIDAK boleh
	// menyentuhnya).
	_, retUpdatedBefore := readAssetCondition(t, env.boot, env.wsA, refsR.assetID)
	// Fakta refsD sebelum resolusi (jumlah tidak boleh berubah).
	var factTotalBefore int64
	if err := env.boot.QueryRow(`SELECT total_minor FROM rental_agreed_prices WHERE workspace_id = $1 AND id = $2`, env.wsA, refsD.factID).Scan(&factTotalBefore); err != nil {
		t.Fatalf("baca fakta sebelum: %v", err)
	}

	// Utuh eksplisit: {"outcome":"returned"} sama dengan body kosong.
	code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsR.draftID, env.ownerToken, "cu-ret-1", "returned")
	if code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("return explicit returned: want 200 returned, got %d (%v)", code, body)
	}
	retAt, _ := body["returned_at"].(string)
	resAt, _ := body["resolved_at"].(string)
	if retAt == "" || retAt != resAt {
		t.Fatalf("return explicit: returned_at harus == resolved_at, got %q vs %q", retAt, resAt)
	}
	if body["returned_by"] != body["resolved_by"] || body["returned_by"] == nil {
		t.Fatalf("return explicit: returned_by harus == resolved_by dan terisi, got %v vs %v", body["returned_by"], body["resolved_by"])
	}
	if cond, upd := readAssetCondition(t, env.boot, env.wsA, refsR.assetID); cond != "ready" || !upd.Equal(retUpdatedBefore) {
		t.Fatalf("aset sesudah return utuh: want ready + updated_at tidak berubah, got %q %v (sebelum %v)", cond, upd, retUpdatedBefore)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit returned: want 1, got %d", got)
	}

	// Damaged: returned_* == resolved_* (waktu/aktor yang sama).
	code, body = returnCustodyOutcome(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-dmg-1", "damaged")
	if code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("return damaged: want 200 damaged, got %d (%v)", code, body)
	}
	dmgRetAt, _ := body["returned_at"].(string)
	dmgResAt, _ := body["resolved_at"].(string)
	if dmgRetAt == "" || dmgRetAt != dmgResAt {
		t.Fatalf("damaged: returned_at harus == resolved_at, got %q vs %q", dmgRetAt, dmgResAt)
	}
	if body["returned_by"] != body["resolved_by"] || body["returned_by"] == nil {
		t.Fatalf("damaged: returned_by harus == resolved_by dan terisi, got %v vs %v", body["returned_by"], body["resolved_by"])
	}
	dmgRetTime, err := time.Parse(time.RFC3339Nano, dmgRetAt)
	if err != nil {
		t.Fatalf("parse returned_at damaged: %v", err)
	}
	if cond, upd := readAssetCondition(t, env.boot, env.wsA, refsD.assetID); cond != "damaged" || !upd.Equal(dmgRetTime) {
		t.Fatalf("aset damaged: want damaged + updated_at == returned_at, got %q %v (returned_at %v)", cond, upd, dmgRetTime)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned_damaged"); got != 1 {
		t.Fatalf("audit damaged: want 1, got %d", got)
	}
	// Fakta/draft/hold tidak berubah oleh resolusi damaged.
	var factTotalAfter int64
	if err := env.boot.QueryRow(`SELECT total_minor FROM rental_agreed_prices WHERE workspace_id = $1 AND id = $2`, env.wsA, refsD.factID).Scan(&factTotalAfter); err != nil {
		t.Fatalf("baca fakta sesudah: %v", err)
	}
	if factTotalAfter != factTotalBefore {
		t.Fatalf("fakta sesudah damaged: want %d, got %d", factTotalBefore, factTotalAfter)
	}
	if h, ok := readHold(t, env.boot, env.wsA, refsD.holdID); !ok || h.Status != "consumed" {
		t.Fatalf("hold sesudah damaged: want consumed, got %+v", h)
	}
	if c, ok := readCustody(t, env.boot, env.wsA, body["id"].(string)); !ok || c.Status != "damaged" || c.ReturnedAt == "" || c.ReturnedAt != c.ResolvedAt || c.ReturnedBy == "" || c.ReturnedBy != c.ResolvedBy {
		t.Fatalf("custody mentah damaged: want damaged + returned==resolved, got %+v", c)
	}

	// Lost: returned_* NULL; resolved_* terisi.
	code, body = returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-lost-1", "lost")
	if code != http.StatusOK || body["status"] != "lost" {
		t.Fatalf("return lost: want 200 lost, got %d (%v)", code, body)
	}
	if body["returned_at"] != nil || body["returned_by"] != nil {
		t.Fatalf("lost: returned_at/by harus null, got %v / %v", body["returned_at"], body["returned_by"])
	}
	lostResAt, _ := body["resolved_at"].(string)
	if lostResAt == "" || body["resolved_by"] == nil {
		t.Fatalf("lost: resolved_at/by harus terisi, got %q / %v", lostResAt, body["resolved_by"])
	}
	lostResTime, err := time.Parse(time.RFC3339Nano, lostResAt)
	if err != nil {
		t.Fatalf("parse resolved_at lost: %v", err)
	}
	if cond, upd := readAssetCondition(t, env.boot, env.wsA, refsL.assetID); cond != "lost" || !upd.Equal(lostResTime) {
		t.Fatalf("aset lost: want lost + updated_at == resolved_at, got %q %v (resolved_at %v)", cond, upd, lostResTime)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_loss_declared"); got != 1 {
		t.Fatalf("audit lost: want 1, got %d", got)
	}
	if c, ok := readCustody(t, env.boot, env.wsA, body["id"].(string)); !ok || c.Status != "lost" || c.ReturnedAt != "" || c.ReturnedBy != "" || c.ResolvedAt == "" || c.ResolvedBy == "" {
		t.Fatalf("custody mentah lost: want lost + returned null + resolved terisi, got %+v", c)
	}
	// GET custody membaca DTO yang sama (resolved_* ikut).
	if code, body := getCustody(t, env.srv, env.wsA, refsL.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "lost" || body["returned_at"] != nil || body["resolved_at"] == nil {
		t.Fatalf("GET custody lost: want 200 lost + returned null + resolved terisi, got %d (%v)", code, body)
	}
}

// TestCustodyReturnOutcomeValidation: body/outcome malformed ditolak 400
// tanpa mengubah custody/audit; explicit returned yang valid -> 200.
func TestCustodyReturnOutcomeValidation(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-OV-CU", "cu-ov", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-ov-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}
	for i, tc := range []struct {
		name string
		raw  string
	}{
		{"outcome null", `{"outcome":null}`},
		{"outcome kosong", `{"outcome":""}`},
		{"outcome angka", `{"outcome":123}`},
		{"outcome bool", `{"outcome":true}`},
		{"outcome objek", `{"outcome":{}}`},
		{"outcome array", `{"outcome":[]}`},
		{"outcome tak dikenal", `{"outcome":"broken"}`},
		{"outcome kapital", `{"outcome":"Returned"}`},
		{"kunci tak dikenal", `{"outcome":"returned","extra":1}`},
		{"body null", `null`},
		{"body string", `"x"`},
		{"body angka", `123`},
		{"body array", `[]`},
		{"body json ganda", `{}{}`},
	} {
		key := fmt.Sprintf("cu-ov-bad-%d", i)
		if code, body := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{key}, tc.raw); code != http.StatusBadRequest {
			t.Fatalf("validasi %s: want 400, got %d (%v)", tc.name, code, body)
		} else {
			assertFlatError(t, body, "body tidak valid")
		}
	}
	// Body oversize (>16 KiB) -> 400.
	big := `{"outcome":"returned"` + strings.Repeat(" ", 17*1024) + `}`
	if code, body := returnCustodyRaw(t, env.srv, env.wsA, refs.draftID, env.ownerToken, []string{"cu-ov-big"}, big); code != http.StatusBadRequest {
		t.Fatalf("body oversize: want 400, got %d (%v)", code, body)
	}
	if code, body := getCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "out" {
		t.Fatalf("custody sesudah validasi 400: want out, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 0 {
		t.Fatalf("audit sesudah validasi 400: want 0, got %d", got)
	}
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-ov-ok", "returned"); code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("return explicit valid: want 200 returned, got %d (%v)", code, body)
	}
}

// TestCustodyDamagedLostReplay: replay membandingkan kunci + draft +
// outcome ternormalisasi (bukan kunci saja); kunci beda pada custody
// terminal -> 409 per outcome tercatat; kosong/{} == explicit returned.
func TestCustodyDamagedLostReplay(t *testing.T) {
	env := setupCustody(t)
	refsD := seedCustodyRefs(t, env, "SKU-RP-D", "cu-rp-d", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsL := seedCustodyRefs(t, env, "SKU-RP-L", "cu-rp-l", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsR := seedCustodyRefs(t, env, "SKU-RP-R", "cu-rp-r", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsR.starts)
	for _, tc := range []struct {
		refs custodyRefs
		key  string
	}{
		{refsD, "cu-rp-d-ho"}, {refsL, "cu-rp-l-ho"}, {refsR, "cu-rp-r-ho"},
	} {
		if code, body := handover(t, env.srv, env.wsA, tc.refs.draftID, env.ownerToken, tc.key, handoverBody(tc.refs.assetID, tc.refs.holdID)); code != http.StatusCreated {
			t.Fatalf("handover %s: want 201, got %d (%v)", tc.key, code, body)
		}
	}

	// Damaged refsD dengan kunci K1.
	code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rp-d-1", "damaged")
	if code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("return damaged: want 200 damaged, got %d (%v)", code, body)
	}
	dmgID, _ := body["id"].(string)
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned_damaged"); got != 1 {
		t.Fatalf("audit damaged: want 1, got %d", got)
	}
	// Replay K1 + damaged -> 200, id sama, tanpa audit ganda.
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rp-d-1", "damaged"); code != http.StatusOK || body["id"] != dmgID || body["status"] != "damaged" {
		t.Fatalf("replay damaged sama: want 200 damaged yang sama, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned_damaged"); got != 1 {
		t.Fatalf("audit damaged sesudah replay: want 1, got %d", got)
	}
	// K1 + outcome beda (lost / kosong) -> 409 kunci.
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rp-d-1", "lost"); code != http.StatusConflict {
		t.Fatalf("replay K1 outcome lost: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rp-d-1"); code != http.StatusConflict {
		t.Fatalf("replay K1 body kosong: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	// K1 pada draft lain -> 409 kunci.
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rp-d-1", "damaged"); code != http.StatusConflict {
		t.Fatalf("replay K1 draft beda: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	// Kunci beda pada custody damaged -> 409 per outcome tercatat.
	if code, body := returnCustody(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rp-d-2"); code != http.StatusConflict {
		t.Fatalf("kunci beda sesudah damaged: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "draft ini sudah ditutup sebagai rusak")
	}

	// Lost refsL dengan kunci K3.
	code, body = returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rp-l-1", "lost")
	if code != http.StatusOK || body["status"] != "lost" {
		t.Fatalf("return lost: want 200 lost, got %d (%v)", code, body)
	}
	lostID, _ := body["id"].(string)
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rp-l-1", "lost"); code != http.StatusOK || body["id"] != lostID || body["status"] != "lost" {
		t.Fatalf("replay lost sama: want 200 lost yang sama, got %d (%v)", code, body)
	}
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rp-l-1", "damaged"); code != http.StatusConflict {
		t.Fatalf("replay K3 outcome damaged: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rp-l-2"); code != http.StatusConflict {
		t.Fatalf("kunci beda sesudah lost: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "draft ini sudah ditutup sebagai hilang")
	}

	// Kosong/{} == explicit returned: body kosong kunci K5 -> 200;
	// replay K5 + explicit returned -> 200 (sama, tanpa audit ganda);
	// replay K5 + damaged -> 409 kunci.
	code, body = returnCustody(t, env.srv, env.wsA, refsR.draftID, env.ownerToken, "cu-rp-r-1")
	if code != http.StatusOK || body["status"] != "returned" {
		t.Fatalf("return kosong: want 200 returned, got %d (%v)", code, body)
	}
	retID, _ := body["id"].(string)
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsR.draftID, env.ownerToken, "cu-rp-r-1", "returned"); code != http.StatusOK || body["id"] != retID || body["status"] != "returned" {
		t.Fatalf("replay K5 explicit returned: want 200 returned yang sama, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned"); got != 1 {
		t.Fatalf("audit returned sesudah replay: want 1, got %d", got)
	}
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsR.draftID, env.ownerToken, "cu-rp-r-1", "damaged"); code != http.StatusConflict {
		t.Fatalf("replay K5 outcome damaged: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	if code, body := returnCustody(t, env.srv, env.wsA, refsR.draftID, env.ownerToken, "cu-rp-r-2"); code != http.StatusConflict {
		t.Fatalf("kunci beda sesudah returned: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "draft ini sudah dikembalikan")
	}
}

// TestCustodyConcurrentMixedOutcomes: resolusi konkuren dengan outcome
// berbeda — kunci sama/outcome beda dan kunci beda/outcome beda sama-sama
// menghasilkan satu outcome terminal + satu audit.
func TestCustodyConcurrentMixedOutcomes(t *testing.T) {
	env := setupCustody(t)
	refs := seedCustodyRefs(t, env, "SKU-CM-CU", "cu-cm", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs.starts)
	if code, body := handover(t, env.srv, env.wsA, refs.draftID, env.ownerToken, "cu-cm-ho", handoverBody(refs.assetID, refs.holdID)); code != http.StatusCreated {
		t.Fatalf("handover: want 201, got %d (%v)", code, body)
	}

	type raceResult struct {
		code int
		body map[string]any
		err  error
	}
	run := func(path, token string, keys []string, body any) chan raceResult {
		ch := make(chan raceResult, 1)
		go func() {
			code, b, err := callHoldAPI(env.srv, "POST", path, token, keys, body)
			ch <- raceResult{code, b, err}
		}()
		return ch
	}
	auditTotal := func() int {
		t.Helper()
		total := 0
		for _, a := range []string{"rental_returned", "rental_returned_damaged", "rental_loss_declared"} {
			total += countBusinessAudits(t, env.boot, env.wsA, a)
		}
		return total
	}
	winner := func(r1, r2 raceResult) map[string]any {
		t.Helper()
		if r1.code == http.StatusOK {
			return r1.body
		}
		return r2.body
	}
	retPath := "/workspaces/" + env.wsA + "/rental/contracts/" + refs.draftID + "/return"
	// Kunci SAMA, outcome beda (kosong vs damaged) -> satu 200 + satu 409.
	ch1 := run(retPath, env.ownerToken, []string{"cu-cm-k1"}, nil)
	ch2 := run(retPath, env.mgrToken, []string{"cu-cm-k1"}, map[string]string{"outcome": "damaged"})
	r1, r2 := <-ch1, <-ch2
	if r1.err != nil || r2.err != nil {
		t.Fatalf("return konkuren kunci sama outcome beda: err %v / %v", r1.err, r2.err)
	}
	codes := map[int]int{r1.code: 1, r2.code: 1}
	if len(codes) != 2 || codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("return konkuren kunci sama outcome beda: want satu 200 + satu 409, got %d + %d", r1.code, r2.code)
	}
	if got := auditTotal(); got != 1 {
		t.Fatalf("audit konkuren kunci sama: want 1, got %d", got)
	}
	if w := winner(r1, r2); w["status"] == "out" {
		t.Fatalf("custody sesudah return konkuren kunci sama: want terminal, got %v", w)
	}
	// Kunci BEDA, outcome beda (damaged vs lost) -> satu 200 + satu 409.
	refs2 := seedCustodyRefs(t, env, "SKU-CM-2", "cu-cm-2", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refs2.starts)
	if code, body := handover(t, env.srv, env.wsA, refs2.draftID, env.ownerToken, "cu-cm-2-ho", handoverBody(refs2.assetID, refs2.holdID)); code != http.StatusCreated {
		t.Fatalf("handover kedua: want 201, got %d (%v)", code, body)
	}
	retPath2 := "/workspaces/" + env.wsA + "/rental/contracts/" + refs2.draftID + "/return"
	ch3 := run(retPath2, env.ownerToken, []string{"cu-cm-2-a"}, map[string]string{"outcome": "damaged"})
	ch4 := run(retPath2, env.mgrToken, []string{"cu-cm-2-b"}, map[string]string{"outcome": "lost"})
	r3, r4 := <-ch3, <-ch4
	if r3.err != nil || r4.err != nil {
		t.Fatalf("return konkuren kunci beda outcome beda: err %v / %v", r3.err, r4.err)
	}
	codes = map[int]int{r3.code: 1, r4.code: 1}
	if len(codes) != 2 || codes[http.StatusOK] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("return konkuren kunci beda outcome beda: want satu 200 + satu 409, got %d + %d", r3.code, r4.code)
	}
	if got := auditTotal(); got != 2 {
		t.Fatalf("audit konkuren kunci beda: want 2, got %d", got)
	}
	if w := winner(r3, r4); w["status"] != "damaged" && w["status"] != "lost" {
		t.Fatalf("custody sesudah return konkuren kunci beda: want damaged/lost, got %v", w)
	}
}

// TestCustodyDamagedLostAfterRevoke: resolusi damaged/lost tetap bisa
// sesudah revoke + deactivate (jalur menyempit D-HAND-3); handover
// sesudah revoke tetap 403.
func TestCustodyDamagedLostAfterRevoke(t *testing.T) {
	env := setupCustody(t)
	refsD := seedCustodyRefs(t, env, "SKU-RV-D", "cu-rv-d", 3*time.Second, 6*time.Second, 50*time.Hour)
	refsL := seedCustodyRefs(t, env, "SKU-RV-L", "cu-rv-l", 3*time.Second, 6*time.Second, 50*time.Hour)
	refs3 := seedCustodyRefs(t, env, "SKU-RV-3", "cu-rv-3", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsL.starts)
	for _, tc := range []struct {
		refs custodyRefs
		key  string
	}{
		{refsD, "cu-rv-d-ho"}, {refsL, "cu-rv-l-ho"},
	} {
		if code, body := handover(t, env.srv, env.wsA, tc.refs.draftID, env.ownerToken, tc.key, handoverBody(tc.refs.assetID, tc.refs.holdID)); code != http.StatusCreated {
			t.Fatalf("handover %s: want 201, got %d (%v)", tc.key, code, body)
		}
	}
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	if code, body := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, body)
	}
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsD.draftID, env.ownerToken, "cu-rv-d-ret", "damaged"); code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("damaged sesudah revoke: want 200 damaged, got %d (%v)", code, body)
	}
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsL.draftID, env.ownerToken, "cu-rv-l-ret", "lost"); code != http.StatusOK || body["status"] != "lost" {
		t.Fatalf("lost sesudah revoke: want 200 lost, got %d (%v)", code, body)
	}
	if code, body := handover(t, env.srv, env.wsA, refs3.draftID, env.ownerToken, "cu-rv-3-ho", handoverBody(refs3.assetID, refs3.holdID)); code != http.StatusForbidden {
		t.Fatalf("handover sesudah revoke: want 403, got %d (%v)", code, body)
	}
	if code, body := getCustody(t, env.srv, env.wsA, refsD.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("GET custody damaged sesudah revoke: want 200 damaged, got %d (%v)", code, body)
	}
	if code, body := getCustody(t, env.srv, env.wsA, refsL.draftID, env.ownerToken); code != http.StatusOK || body["status"] != "lost" {
		t.Fatalf("GET custody lost sesudah revoke: want 200 lost, got %d (%v)", code, body)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_returned_damaged"); got != 1 {
		t.Fatalf("audit damaged sesudah revoke: want 1, got %d", got)
	}
	if got := countBusinessAudits(t, env.boot, env.wsA, "rental_loss_declared"); got != 1 {
		t.Fatalf("audit lost sesudah revoke: want 1, got %d", got)
	}
}

// TestCustodyResolutionFaults: fault audit/tulis-custody/update-aset
// pada jalur resolusi -> 500 tanpa perubahan custody/aset/audit
// (snapshot eksak sama); sesudah trigger dilepas, retry berhasil.
// Trigger SELALU dilepas lewat t.Cleanup (dan eksplisit sebelum retry)
// supaya Fatalf tidak meninggalkan fault di umnia_test.
func TestCustodyResolutionFaults(t *testing.T) {
	env := setupCustody(t)

	installTrigger := func(name, fn, table, when, cond string) {
		t.Helper()
		if _, err := env.boot.Exec(fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION app.%s() RETURNS trigger AS $$
			BEGIN
				IF %s THEN
					RAISE EXCEPTION 'synthetic %s failure';
				END IF;
				RETURN NEW;
			END;
			$$ LANGUAGE plpgsql`, fn, cond, name)); err != nil {
			t.Fatalf("buat fungsi fault %s: %v", name, err)
		}
		if _, err := env.boot.Exec(fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH ROW EXECUTE FUNCTION app.%s()`, name, when, table, fn)); err != nil {
			t.Fatalf("pasang trigger fault %s: %v", name, err)
		}
		t.Cleanup(func() {
			_, _ = env.boot.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table))
			_, _ = env.boot.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, fn))
		})
	}
	dropTrigger := func(name, fn, table string) {
		t.Helper()
		if _, err := env.boot.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table)); err != nil {
			t.Fatalf("lepas trigger %s: %v", name, err)
		}
		if _, err := env.boot.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, fn)); err != nil {
			t.Fatalf("hapus fungsi %s: %v", name, err)
		}
	}
	// Snapshot eksak (custody + aset + audit) untuk satu rantai.
	snapshot := func(refs custodyRefs) (storedCustody, string, time.Time, int) {
		t.Helper()
		code, body := getCustody(t, env.srv, env.wsA, refs.draftID, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("snapshot GET custody: want 200, got %d (%v)", code, body)
		}
		id, _ := body["id"].(string)
		c, ok := readCustody(t, env.boot, env.wsA, id)
		if !ok {
			t.Fatalf("snapshot custody tidak ada")
		}
		cond, upd := readAssetCondition(t, env.boot, env.wsA, refs.assetID)
		total := 0
		for _, a := range []string{"rental_returned", "rental_returned_damaged", "rental_loss_declared"} {
			total += countBusinessAudits(t, env.boot, env.wsA, a)
		}
		return c, cond, upd, total
	}
	assertUnchanged := func(refs custodyRefs, before storedCustody, beforeCond string, beforeUpd time.Time, beforeAudits int) {
		t.Helper()
		after, afterCond, afterUpd, afterAudits := snapshot(refs)
		if after != before || afterCond != beforeCond || !afterUpd.Equal(beforeUpd) || afterAudits != beforeAudits {
			t.Fatalf("fault mengubah state: sebelum=%+v/%s/%v/%d sesudah=%+v/%s/%v/%d",
				before, beforeCond, beforeUpd, beforeAudits, after, afterCond, afterUpd, afterAudits)
		}
	}

	// (a) Fault audit: INSERT audit damaged/lost digagalkan.
	refsA := seedCustodyRefs(t, env, "SKU-FA-A", "cu-fa-a", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsA.starts)
	if code, body := handover(t, env.srv, env.wsA, refsA.draftID, env.ownerToken, "cu-fa-a-ho", handoverBody(refsA.assetID, refsA.holdID)); code != http.StatusCreated {
		t.Fatalf("handover (a): want 201, got %d (%v)", code, body)
	}
	installTrigger("qa_fault_resolution_audit", "qa_fail_resolution_audit", "audit_events", "BEFORE INSERT",
		`NEW.action IN ('rental_returned_damaged', 'rental_loss_declared')`)
	before, beforeCond, beforeUpd, beforeAudits := snapshot(refsA)
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsA.draftID, env.ownerToken, "cu-fa-a-ret", "damaged"); code != http.StatusInternalServerError {
		t.Fatalf("return fault audit: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "audit gagal ditulis")
	}
	assertUnchanged(refsA, before, beforeCond, beforeUpd, beforeAudits)
	dropTrigger("qa_fault_resolution_audit", "qa_fail_resolution_audit", "audit_events")
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsA.draftID, env.ownerToken, "cu-fa-a-ret", "damaged"); code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("return sesudah lepas fault audit: want 200 damaged, got %d (%v)", code, body)
	}

	// (b) Fault tulis custody: UPDATE custody ke damaged/lost digagalkan.
	refsB := seedCustodyRefs(t, env, "SKU-FA-B", "cu-fa-b", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsB.starts)
	if code, body := handover(t, env.srv, env.wsA, refsB.draftID, env.ownerToken, "cu-fa-b-ho", handoverBody(refsB.assetID, refsB.holdID)); code != http.StatusCreated {
		t.Fatalf("handover (b): want 201, got %d (%v)", code, body)
	}
	installTrigger("qa_fault_custody_write", "qa_fail_custody_write", "rental_custody", "BEFORE UPDATE",
		`NEW.status IN ('damaged', 'lost')`)
	before, beforeCond, beforeUpd, beforeAudits = snapshot(refsB)
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsB.draftID, env.ownerToken, "cu-fa-b-ret", "lost"); code != http.StatusInternalServerError {
		t.Fatalf("return fault tulis custody: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "transaksi gagal")
	}
	assertUnchanged(refsB, before, beforeCond, beforeUpd, beforeAudits)
	dropTrigger("qa_fault_custody_write", "qa_fail_custody_write", "rental_custody")
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsB.draftID, env.ownerToken, "cu-fa-b-ret", "lost"); code != http.StatusOK || body["status"] != "lost" {
		t.Fatalf("return sesudah lepas fault tulis: want 200 lost, got %d (%v)", code, body)
	}

	// (c) Fault update aset: UPDATE condition ke damaged/lost digagalkan
	// — custody yang sudah ter-update di transaksi yang sama HARUS
	// ikut rollback (custody tetap 'out', bukan damaged).
	refsC := seedCustodyRefs(t, env, "SKU-FA-C", "cu-fa-c", 3*time.Second, 6*time.Second, 50*time.Hour)
	waitUntil(t, env.boot, refsC.starts)
	if code, body := handover(t, env.srv, env.wsA, refsC.draftID, env.ownerToken, "cu-fa-c-ho", handoverBody(refsC.assetID, refsC.holdID)); code != http.StatusCreated {
		t.Fatalf("handover (c): want 201, got %d (%v)", code, body)
	}
	installTrigger("qa_fault_asset_update", "qa_fail_asset_update", "physical_assets", "BEFORE UPDATE",
		`NEW.condition IN ('damaged', 'lost')`)
	before, beforeCond, beforeUpd, beforeAudits = snapshot(refsC)
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsC.draftID, env.ownerToken, "cu-fa-c-ret", "damaged"); code != http.StatusInternalServerError {
		t.Fatalf("return fault update aset: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "transaksi gagal")
	}
	assertUnchanged(refsC, before, beforeCond, beforeUpd, beforeAudits)
	dropTrigger("qa_fault_asset_update", "qa_fail_asset_update", "physical_assets")
	if code, body := returnCustodyOutcome(t, env.srv, env.wsA, refsC.draftID, env.ownerToken, "cu-fa-c-ret", "damaged"); code != http.StatusOK || body["status"] != "damaged" {
		t.Fatalf("return sesudah lepas fault aset: want 200 damaged, got %d (%v)", code, body)
	}
}

// scratchDB membuat database kosong bernama name lewat koneksi postgres
// (bootstrap URL dengan path /postgres) dan mengembalikan DSN-nya.
// Cleanup (DROP) didaftarkan SEBELUM pembuatan supaya Fatalf di tengah
// tidak meninggalkan database yatim.
func scratchDB(t *testing.T, name string) (string, *sql.DB) {
	t.Helper()
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}
	u, err := url.Parse(bootURL)
	if err != nil {
		t.Fatalf("parse bootstrap URL: %v", err)
	}
	u.Path = "/postgres"
	pg, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("buka postgres: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pg.Exec(`DROP DATABASE IF EXISTS ` + name)
		_ = pg.Close()
	})
	if _, err := pg.Exec(`DROP DATABASE IF EXISTS ` + name); err != nil {
		t.Fatalf("drop scratch %s: %v", name, err)
	}
	if _, err := pg.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatalf("create scratch %s: %v", name, err)
	}
	u.Path = "/" + name
	scratch, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("buka scratch %s: %v", name, err)
	}
	t.Cleanup(func() { _ = scratch.Close() })
	return u.String(), scratch
}

// TestCustodyUpgrade012: upgrade dari skema 001-011 dengan baris legacy
// out + returned — backfill resolved_* sekali dari returned_*,
// timestamp/kunci/audit asli dipertahankan; rerun 012 mempertahankan
// semuanya; marker 12.
func TestCustodyUpgrade012(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	scratchURL, scratch := scratchDB(t, "umnia_g_upgrade")
	// Jalur produksi: 001-011 lalu 012, masing-masing satu transaksi
	// psql -1 (applyMigrationFile).
	for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql") {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	subj := "11111111-2222-3333-4444-555555555581"
	ws := "11111111-2222-3333-4444-555555555582"
	cust := "11111111-2222-3333-4444-555555555583"
	item := "11111111-2222-3333-4444-555555555584"
	draft1 := "11111111-2222-3333-4444-555555555585"
	draft2 := "11111111-2222-3333-4444-555555555586"
	price1 := "11111111-2222-3333-4444-555555555587"
	price2 := "11111111-2222-3333-4444-555555555588"
	fact1 := "11111111-2222-3333-4444-555555555589"
	fact2 := "11111111-2222-3333-4444-555555555590"
	asset := "11111111-2222-3333-4444-555555555591"
	hold1 := "11111111-2222-3333-4444-555555555592"
	hold2 := "11111111-2222-3333-4444-555555555593"
	outCustody := "11111111-2222-3333-4444-555555555594"
	retCustody := "11111111-2222-3333-4444-555555555595"
	execMust := func(q string, args ...any) {
		t.Helper()
		if _, err := scratch.Exec(q, args...); err != nil {
			t.Fatalf("fixture upgrade: %v (%s)", err, q)
		}
	}
	execMust(`INSERT INTO identity_subjects (id, email, password_hash) VALUES ($1, 'upgrade@example.test', 'x')`, subj)
	execMust(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES ($1, 'WS Upgrade', $2)`, ws, subj)
	execMust(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Upgrade')`, cust, ws)
	execMust(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-UPGRADE-12', 'Barang Upgrade', 'pcs', 'active')`, item, ws)
	execMust(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'upgrade-12-draft-1', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft1, ws, cust, item)
	execMust(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'upgrade-12-draft-2', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft2, ws, cust, item)
	execMust(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'upgrade-12-price-1', '2026-11-10T01:00:00Z', '2026-11-13T01:00:00Z', now(), 150000, 'IDR')`, price1, ws, item)
	execMust(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'upgrade-12-price-2', '2026-11-13T01:00:00Z', '2026-11-16T01:00:00Z', now(), 150000, 'IDR')`, price2, ws, item)
	execMust(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'upgrade-12-fact-1', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact1, ws, draft1, price1, item, cust, subj)
	execMust(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'upgrade-12-fact-2', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact2, ws, draft2, price2, item, cust, subj)
	execMust(`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3, 'SN-UPGRADE-12')`, asset, ws, item)
	execMust(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'upgrade-12-hold-1', 'consumed', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold1, ws, draft1, asset)
	execMust(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'upgrade-12-hold-2', 'consumed', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold2, ws, draft2, asset)
	execMust(`INSERT INTO rental_custody (id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, status, handed_out_at, handed_out_by) VALUES ($1, $2, $3, $4, $5, $6, 'upgrade-12-custody-out', 'out', '2026-11-10T02:00:00Z', $7)`, outCustody, ws, draft1, fact1, asset, hold1, subj)
	execMust(`INSERT INTO rental_custody (id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by) VALUES ($1, $2, $3, $4, $5, $6, 'upgrade-12-custody-ret', 'upgrade-12-ret-1', 'returned', '2026-11-10T02:00:00Z', '2026-11-11T02:00:00Z', $7, $7)`, retCustody, ws, draft2, fact2, asset, hold2, subj)

	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "012_rental_damaged_lost_return.sql")); code != 0 {
		t.Fatalf("terapkan 012: want exit 0, got %d (%s)", code, out)
	}
	// Backfill: returned row resolved_* == returned_* (asli
	// dipertahankan); out row resolved_* NULL.
	var retAt, resAt, retBy, resBy string
	if err := scratch.QueryRow(`SELECT returned_at::text, COALESCE(resolved_at::text,''), returned_by::text, COALESCE(resolved_by::text,'') FROM rental_custody WHERE id = $1`, retCustody).Scan(&retAt, &resAt, &retBy, &resBy); err != nil {
		t.Fatalf("baca returned sesudah 012: %v", err)
	}
	if resAt != retAt || resBy != retBy {
		t.Fatalf("backfill returned: resolved harus == returned, got %q/%q vs %q/%q", resAt, resBy, retAt, retBy)
	}
	var outResAt, outResBy, outKey string
	if err := scratch.QueryRow(`SELECT COALESCE(resolved_at::text,''), COALESCE(resolved_by::text,''), COALESCE(return_client_request_id,'') FROM rental_custody WHERE id = $1`, outCustody).Scan(&outResAt, &outResBy, &outKey); err != nil {
		t.Fatalf("baca out sesudah 012: %v", err)
	}
	if outResAt != "" || outResBy != "" || outKey != "" {
		t.Fatalf("backfill out: resolved/kunci harus NULL, got %q/%q/%q", outResAt, outResBy, outKey)
	}
	// Marker 12 + CHECK/kebijakan/grant baru + kondisi aset ready.
	var markers string
	if err := scratch.QueryRow(`SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`).Scan(&markers); err != nil {
		t.Fatalf("baca markers upgrade: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return" {
		t.Fatalf("markers upgrade: want 12 marker, got %s", markers)
	}
	var n int
	if err := scratch.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname = 'rental_custody_resolved_after_handover' AND contype = 'c'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("CHECK resolved_after_handover: want 1, got %d (%v)", n, err)
	}
	var def string
	if err := scratch.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname = 'rental_custody_status_check'`).Scan(&def); err != nil {
		t.Fatalf("CHECK status: %v", err)
	}
	if !strings.Contains(def, "'damaged'") || !strings.Contains(def, "'lost'") {
		t.Fatalf("CHECK status: harus memuat damaged/lost, got %q", def)
	}
	var withCheck string
	if err := scratch.QueryRow(`SELECT coalesce(with_check,'') FROM pg_policies WHERE tablename = 'rental_custody' AND policyname = 'rental_custody_update'`).Scan(&withCheck); err != nil {
		t.Fatalf("kebijakan update custody: %v", err)
	}
	if !strings.Contains(withCheck, "'returned'") || !strings.Contains(withCheck, "'damaged'") || !strings.Contains(withCheck, "'lost'") {
		t.Fatalf("kebijakan update custody: WITH CHECK harus tiga status, got %q", withCheck)
	}
	if err := scratch.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename = 'physical_assets' AND policyname = 'physical_assets_update' AND cmd = 'UPDATE'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("kebijakan update aset: want 1, got %d (%v)", n, err)
	}
	for _, tc := range []struct{ table, col string }{
		{"rental_custody", "resolved_at"}, {"rental_custody", "resolved_by"},
		{"physical_assets", "condition"}, {"physical_assets", "updated_at"},
	} {
		var got bool
		if err := scratch.QueryRow(`SELECT has_column_privilege('umnia_business', $1, $2, 'UPDATE')`, tc.table, tc.col).Scan(&got); err != nil {
			t.Fatalf("hak kolom %s.%s: %v", tc.table, tc.col, err)
		}
		if !got {
			t.Fatalf("hak UPDATE %s.%s: want true, got false", tc.table, tc.col)
		}
	}
	var cond string
	if err := scratch.QueryRow(`SELECT condition FROM physical_assets WHERE id = $1`, asset).Scan(&cond); err != nil {
		t.Fatalf("baca kondisi aset upgrade: %v", err)
	}
	if cond != "ready" {
		t.Fatalf("kondisi aset upgrade: want ready, got %q", cond)
	}
	// Rerun 012 (autocommit) mempertahankan semuanya (backfill tidak
	// mengulang: resolved_* sudah terisi).
	before := custodySnapshot(t, scratch)
	if code, out := runPsqlFile(t, scratchURL, "migrations/012_rental_damaged_lost_return.sql"); code != 0 {
		t.Fatalf("rerun 012 upgrade: want exit 0, got %d (%s)", code, out)
	}
	after := custodySnapshot(t, scratch)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun 012 upgrade mengubah state:\nsebelum=%v\nsesudah=%v", before, after)
	}
}

// TestCustodyUpgrade012RejectsInconsistent: baris legacy yang tidak
// konsisten (returned tanpa returned_at, dibuat sesudah CHECK state
// lama dicabut) membuat 012 GAGAL — bukan auto-repair: exit != 0,
// marker 012 tidak ada, baris tidak diperbaiki, kolom resolved_*
// tidak ada (rollback penuh psql -1).
func TestCustodyUpgrade012RejectsInconsistent(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	scratchURL, scratch := scratchDB(t, "umnia_g_upgrade_bad")
	for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql") {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	subj := "11111111-2222-3333-4444-5555555555a1"
	ws := "11111111-2222-3333-4444-5555555555a2"
	cust := "11111111-2222-3333-4444-5555555555a3"
	item := "11111111-2222-3333-4444-5555555555a4"
	draft := "11111111-2222-3333-4444-5555555555a5"
	price := "11111111-2222-3333-4444-5555555555a6"
	fact := "11111111-2222-3333-4444-5555555555a7"
	asset := "11111111-2222-3333-4444-5555555555a8"
	hold := "11111111-2222-3333-4444-5555555555a9"
	badCustody := "11111111-2222-3333-4444-5555555555b0"
	execMust := func(q string, args ...any) {
		t.Helper()
		if _, err := scratch.Exec(q, args...); err != nil {
			t.Fatalf("fixture inconsistent: %v (%s)", err, q)
		}
	}
	execMust(`INSERT INTO identity_subjects (id, email, password_hash) VALUES ($1, 'bad@example.test', 'x')`, subj)
	execMust(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES ($1, 'WS Bad', $2)`, ws, subj)
	execMust(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Bad')`, cust, ws)
	execMust(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-BAD-12', 'Barang Bad', 'pcs', 'active')`, item, ws)
	execMust(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'bad-12-draft', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft, ws, cust, item)
	execMust(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'bad-12-price', '2026-11-10T01:00:00Z', '2026-11-13T01:00:00Z', now(), 150000, 'IDR')`, price, ws, item)
	execMust(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'bad-12-fact', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact, ws, draft, price, item, cust, subj)
	execMust(`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3, 'SN-BAD-12')`, asset, ws, item)
	execMust(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'bad-12-hold', 'consumed', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold, ws, draft, asset)
	execMust(`INSERT INTO rental_custody (id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by) VALUES ($1, $2, $3, $4, $5, $6, 'bad-12-custody', 'bad-12-ret', 'returned', '2026-11-10T02:00:00Z', '2026-11-11T02:00:00Z', $7, $7)`, badCustody, ws, draft, fact, asset, hold, subj)
	// Rusak baris: cabut CHECK state lama, lalu NULL-kan returned_at.
	execMust(`ALTER TABLE rental_custody DROP CONSTRAINT rental_custody_state_check`)
	execMust(`UPDATE rental_custody SET returned_at = NULL WHERE id = $1`, badCustody)

	code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "012_rental_damaged_lost_return.sql"))
	if code == 0 {
		t.Fatalf("terapkan 012 pada data inkonsisten: want exit != 0, got 0 (%s)", out)
	}
	var n int
	if err := scratch.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = '012_rental_damaged_lost_return'`).Scan(&n); err != nil {
		t.Fatalf("cek marker 012: %v", err)
	}
	if n != 0 {
		t.Fatalf("marker 012 sesudah gagal: want 0, got %d", n)
	}
	var retAt string
	if err := scratch.QueryRow(`SELECT COALESCE(returned_at::text,'') FROM rental_custody WHERE id = $1`, badCustody).Scan(&retAt); err != nil {
		t.Fatalf("baca baris inkonsisten: %v", err)
	}
	if retAt != "" {
		t.Fatalf("baris inkonsisten diperbaiki: want returned_at tetap NULL, got %q", retAt)
	}
	if err := scratch.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_name = 'rental_custody' AND column_name = 'resolved_at'`).Scan(&n); err != nil {
		t.Fatalf("cek kolom resolved_at: %v", err)
	}
	if n != 0 {
		t.Fatalf("kolom resolved_at sesudah gagal: want 0 (rollback penuh), got %d", n)
	}
}

// TestCustodyUpgrade012RejectsPartialResolved: metadata resolved
// parsial pada baris legacy 'returned' HARUS menggagalkan 012 tanpa
// auto-repair (kontrak beku): backfill hanya boleh mengisi baris yang
// resolved_at DAN resolved_by-nya SAMA-SAMA NULL. Tiga varian parsial
// (resolved_by saja — kasus reviewer, resolved_at saja, keduanya tapi
// tidak cocok) masing-masing di scratch DB sendiri: 012 exit != 0,
// marker absen, baris + snapshot (data/kebijakan/ACL/constraint/
// marker) tidak berubah — rollback penuh file, bukan sekadar logika
// helper. Predikat lama (hanya resolved_at IS NULL) menimpa
// resolved_by yang sudah terisi aktor lain lalu lolos exit 0.
func TestCustodyUpgrade012RejectsPartialResolved(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	type partialCase struct {
		name string
		db   string
		// setupPartial dipanggil sesudah kolom resolved_* ditambah dan
		// sebelum snapshot: mengisi sebagian metadata resolved.
		setupPartial func(t *testing.T, scratch *sql.DB, retCustody, subj, other string)
	}
	cases := []partialCase{
		{
			name: "resolved_by saja (kasus reviewer)",
			db:   "umnia_g_partial_by",
			setupPartial: func(t *testing.T, scratch *sql.DB, retCustody, subj, other string) {
				t.Helper()
				// resolved_at NULL, resolved_by = aktor lain (bukan returned_by).
				if _, err := scratch.Exec(`UPDATE rental_custody SET resolved_by = $2 WHERE id = $1`, retCustody, other); err != nil {
					t.Fatalf("isi resolved_by parsial: %v", err)
				}
			},
		},
		{
			name: "resolved_at saja",
			db:   "umnia_g_partial_at",
			setupPartial: func(t *testing.T, scratch *sql.DB, retCustody, subj, other string) {
				t.Helper()
				// resolved_at = returned_at, resolved_by NULL.
				if _, err := scratch.Exec(`UPDATE rental_custody SET resolved_at = '2026-11-11T02:00:00Z' WHERE id = $1`, retCustody); err != nil {
					t.Fatalf("isi resolved_at parsial: %v", err)
				}
			},
		},
		{
			name: "keduanya tidak cocok",
			db:   "umnia_g_partial_both",
			setupPartial: func(t *testing.T, scratch *sql.DB, retCustody, subj, other string) {
				t.Helper()
				// resolved_at = handed_out_at (bukan returned_at),
				// resolved_by = aktor lain.
				if _, err := scratch.Exec(`UPDATE rental_custody SET resolved_at = '2026-11-10T02:00:00Z', resolved_by = $2 WHERE id = $1`, retCustody, other); err != nil {
					t.Fatalf("isi resolved tidak cocok: %v", err)
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			scratchURL, scratch := scratchDB(t, tc.db)
			for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql") {
				if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
					t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
				}
			}
			subj := "11111111-2222-3333-4444-5555555555c1"
			other := "11111111-2222-3333-4444-5555555555c2"
			ws := "11111111-2222-3333-4444-5555555555c3"
			cust := "11111111-2222-3333-4444-5555555555c4"
			item := "11111111-2222-3333-4444-5555555555c5"
			draft := "11111111-2222-3333-4444-5555555555c6"
			price := "11111111-2222-3333-4444-5555555555c7"
			fact := "11111111-2222-3333-4444-5555555555c8"
			asset := "11111111-2222-3333-4444-5555555555c9"
			hold := "11111111-2222-3333-4444-5555555555ca"
			retCustody := "11111111-2222-3333-4444-5555555555cb"
			execMust := func(q string, args ...any) {
				t.Helper()
				if _, err := scratch.Exec(q, args...); err != nil {
					t.Fatalf("fixture parsial: %v (%s)", err, q)
				}
			}
			execMust(`INSERT INTO identity_subjects (id, email, password_hash) VALUES ($1, 'partial@example.test', 'x')`, subj)
			execMust(`INSERT INTO identity_subjects (id, email, password_hash) VALUES ($1, 'partial-other@example.test', 'x')`, other)
			execMust(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES ($1, 'WS Partial', $2)`, ws, subj)
			execMust(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Partial')`, cust, ws)
			execMust(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-PARTIAL-12', 'Barang Partial', 'pcs', 'active')`, item, ws)
			execMust(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'partial-12-draft', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft, ws, cust, item)
			execMust(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'partial-12-price', '2026-11-10T01:00:00Z', '2026-11-13T01:00:00Z', now(), 150000, 'IDR')`, price, ws, item)
			execMust(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'partial-12-fact', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact, ws, draft, price, item, cust, subj)
			execMust(`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3, 'SN-PARTIAL-12')`, asset, ws, item)
			execMust(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'partial-12-hold', 'consumed', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold, ws, draft, asset)
			execMust(`INSERT INTO rental_custody (id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by) VALUES ($1, $2, $3, $4, $5, $6, 'partial-12-custody', 'partial-12-ret', 'returned', '2026-11-10T02:00:00Z', '2026-11-11T02:00:00Z', $7, $7)`, retCustody, ws, draft, fact, asset, hold, subj)
			// Tiru penerapan parsial sebelumnya: kolom resolved_*
			// sudah ada (seperti kasus reviewer), lalu isi sebagian.
			execMust(`ALTER TABLE rental_custody ADD COLUMN IF NOT EXISTS resolved_at TIMESTAMPTZ`)
			execMust(`ALTER TABLE rental_custody ADD COLUMN IF NOT EXISTS resolved_by UUID REFERENCES identity_subjects(id)`)
			tc.setupPartial(t, scratch, retCustody, subj, other)

			readRow := func() (retAt, resAt, retBy, resBy, key string) {
				t.Helper()
				if err := scratch.QueryRow(`SELECT returned_at::text, COALESCE(resolved_at::text,''), returned_by::text, COALESCE(resolved_by::text,''), COALESCE(return_client_request_id,'') FROM rental_custody WHERE id = $1`, retCustody).Scan(&retAt, &resAt, &retBy, &resBy, &key); err != nil {
					t.Fatalf("baca baris parsial: %v", err)
				}
				return
			}
			retAt0, resAt0, retBy0, resBy0, key0 := readRow()
			beforeSnap := custodySnapshot(t, scratch)

			code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "012_rental_damaged_lost_return.sql"))
			if code == 0 {
				t.Fatalf("terapkan 012 pada metadata parsial %s: want exit != 0, got 0 (%s)", tc.name, out)
			}
			var n int
			if err := scratch.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = '012_rental_damaged_lost_return'`).Scan(&n); err != nil {
				t.Fatalf("cek marker 012: %v", err)
			}
			if n != 0 {
				t.Fatalf("marker 012 sesudah gagal %s: want 0, got %d", tc.name, n)
			}
			retAt1, resAt1, retBy1, resBy1, key1 := readRow()
			if retAt0 != retAt1 || resAt0 != resAt1 || retBy0 != retBy1 || resBy0 != resBy1 || key0 != key1 {
				t.Fatalf("baris parsial berubah sesudah 012 gagal %s:\nsebelum=%q/%q/%q/%q/%q\nsesudah=%q/%q/%q/%q/%q", tc.name, retAt0, resAt0, retBy0, resBy0, key0, retAt1, resAt1, retBy1, resBy1, key1)
			}
			afterSnap := custodySnapshot(t, scratch)
			if !reflect.DeepEqual(beforeSnap, afterSnap) {
				t.Fatalf("snapshot berubah sesudah 012 gagal %s (bukan rollback penuh):\nsebelum=%v\nsesudah=%v", tc.name, beforeSnap, afterSnap)
			}
		})
	}
}

// TestCustodyDamagedLostAllocationGuards: sesudah serah terima lalu
// resolve damaged/lost, ketiga guard kondisi (availability, hold baru,
// handover baru) HARUS tetap menolak alokasi atas aset yang sama,
// sementara reservasi lama (hold kedua yang sudah held) tetap utuh
// persis — tidak dilepas, tidak kedaluwarsa, tidak dikonsumsi, tanpa
// audit baru. Dua draft bersebelahan pada SATU aset (draft kedua mulai
// tepat saat draft pertama berakhir) + satu draft ketiga pada periode
// bebas (sesudah draft kedua berakhir) supaya guard kondisi tidak
// tertutupi hold yang tumpang tindih: periode bebas itu available=true
// sebelum resolusi dan false sesudahnya HANYA lewat kondisi (custody
// sudah terminal, bukan out; tidak ada hold yang tumpang tindih di
// sana). seedCustodyRefs tidak bisa dipakai: ia membuat aset baru per
// draft — rantai di sini disusun dari helper yang sama (seedDraftRefs,
// seedPrice, createDraft, getQuote, acceptPrice, seedAsset, seedHold)
// dengan satu aset bersama.
func TestCustodyDamagedLostAllocationGuards(t *testing.T) {
	cases := []struct{ outcome, pfx string }{
		{"damaged", "alloc-dmg"},
		{"lost", "alloc-lost"},
	}
	for _, tc := range cases {
		t.Run(tc.outcome, func(t *testing.T) {
			runAllocationGuards(t, tc.outcome, tc.pfx)
		})
	}
}

func runAllocationGuards(t *testing.T, outcome, pfx string) {
	t.Helper()
	env := setupCustody(t)
	custID, itemID := seedDraftRefs(t, env, "SKU-"+strings.ToUpper(pfx))
	activateRental(t, env)
	base := dbCutoff(t, env.boot)
	assetID := seedAsset(t, env, itemID, "SN-"+pfx)
	_ = seedPrice(t, env, itemID, pfx+"-price", base.Add(2*time.Second), base.Add(73*time.Hour), "150000", "IDR")
	s1, e1 := base.Add(6*time.Second), base.Add(14*time.Second)
	s2, e2 := base.Add(14*time.Second), base.Add(22*time.Second)
	s3, e3 := base.Add(22*time.Second), base.Add(30*time.Second)
	rfc := func(tm time.Time) string { return tm.UTC().Format(time.RFC3339Nano) }
	mkDraft := func(key, starts, ends string) string {
		t.Helper()
		code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, key, draftBody(custID, itemID, starts, ends))
		if code != http.StatusCreated {
			t.Fatalf("seed draft %s: want 201, got %d (%v)", key, code, body)
		}
		id, _ := body["id"].(string)
		return id
	}
	draft1 := mkDraft(pfx+"-draft1", rfc(s1), rfc(e1))
	draft2 := mkDraft(pfx+"-draft2", rfc(s2), rfc(e2))
	draft3 := mkDraft(pfx+"-draft3", rfc(s3), rfc(e3))
	mkFact := func(draft, key string) {
		t.Helper()
		code, quote := getQuote(t, env.srv, env.wsA, draft, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("seed quote %s: want 200, got %d (%v)", key, code, quote)
		}
		version, _ := quote["price_version_id"].(string)
		total, _ := quote["total_minor"].(string)
		code, body := acceptPrice(t, env.srv, env.wsA, draft, env.ownerToken, key, acceptBody(version, total))
		if code != http.StatusCreated {
			t.Fatalf("seed accept %s: want 201, got %d (%v)", key, code, body)
		}
	}
	mkFact(draft1, pfx+"-accept1")
	mkFact(draft2, pfx+"-accept2")
	// Kedua hold dibuat SEBELUM handover pertama; periode
	// bersebelahan persis (bukan tumpang tindih) supaya EXCLUDE
	// tidak ikut campur.
	hold1, _ := seedHold(t, env, draft1, assetID, pfx+"-hold1")
	hold2, _ := seedHold(t, env, draft2, assetID, pfx+"-hold2")

	avail := func(starts, ends time.Time) bool {
		t.Helper()
		code, body := getAvailability(t, env.srv, env.wsA, assetID, env.ownerToken, availQuery(rfc(starts), rfc(ends)))
		if code != http.StatusOK {
			t.Fatalf("availability %s..%s: want 200, got %d (%v)", rfc(starts), rfc(ends), code, body)
		}
		v, _ := body["available"].(bool)
		return v
	}
	// Periode bebas available SEBELUM resolusi: kondisi ready, tidak
	// ada custody out, tidak ada hold yang tumpang tindih.
	if !avail(s3, e3) {
		t.Fatalf("periode bebas sebelum resolusi: want available=true, got false")
	}
	// Handover pertama di dalam jendela nyatanya.
	waitUntil(t, env.boot, s1.Add(500*time.Millisecond))
	code, body := handover(t, env.srv, env.wsA, draft1, env.ownerToken, pfx+"-handover1", handoverBody(assetID, hold1))
	if code != http.StatusCreated {
		t.Fatalf("handover pertama: want 201, got %d (%v)", code, body)
	}
	custodyID, _ := body["id"].(string)
	if custodyID == "" {
		t.Fatalf("handover pertama: respons tanpa id custody (%v)", body)
	}
	// Snapshot hold SESUDAH handover (hold1 consumed, hold2 held)
	// dan hitungan audit SEBELUM resolusi.
	hold1After, ok := readHold(t, env.boot, env.wsA, hold1)
	if !ok {
		t.Fatalf("hold1 hilang sesudah handover")
	}
	if hold1After.Status != "consumed" {
		t.Fatalf("hold1 sesudah handover: want consumed, got %s", hold1After.Status)
	}
	hold2Before, ok := readHold(t, env.boot, env.wsA, hold2)
	if !ok {
		t.Fatalf("hold2 hilang sesudah handover")
	}
	if hold2Before.Status != "held" {
		t.Fatalf("hold2 sesudah handover: want held, got %s", hold2Before.Status)
	}
	handedOverBefore := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over")
	holdCreatedBefore := countBusinessAudits(t, env.boot, env.wsA, "hold_created")
	returnAction := "rental_returned_damaged"
	if outcome == "lost" {
		returnAction = "rental_loss_declared"
	}
	returnedBefore := countBusinessAudits(t, env.boot, env.wsA, returnAction)

	// Resolusi damaged/lost.
	code, body = returnCustodyOutcome(t, env.srv, env.wsA, draft1, env.ownerToken, pfx+"-return1", outcome)
	if code != http.StatusOK {
		t.Fatalf("resolve %s: want 200, got %d (%v)", outcome, code, body)
	}
	c, ok := readCustody(t, env.boot, env.wsA, custodyID)
	if !ok {
		t.Fatalf("custody hilang sesudah resolve")
	}
	if c.Status != outcome || c.ResolvedAt == "" || c.ResolvedBy == "" {
		t.Fatalf("custody sesudah resolve: want status=%s + resolved terisi, got %+v", outcome, c)
	}
	cond, _ := readAssetCondition(t, env.boot, env.wsA, assetID)
	if cond != outcome {
		t.Fatalf("kondisi aset: want %s, got %s", outcome, cond)
	}
	// Reservasi lama UTUH PERSIS: hold kedua tetap held dengan
	// status/kunci/periode/expiry/timestamp yang sama; hold pertama
	// tetap consumed.
	hold2After, _ := readHold(t, env.boot, env.wsA, hold2)
	if !reflect.DeepEqual(hold2Before, hold2After) {
		t.Fatalf("hold2 berubah sesudah resolve:\nsebelum=%+v\nsesudah=%+v", hold2Before, hold2After)
	}
	hold1After2, _ := readHold(t, env.boot, env.wsA, hold1)
	if !reflect.DeepEqual(hold1After, hold1After2) {
		t.Fatalf("hold1 berubah sesudah resolve:\nsebelum=%+v\nsesudah=%+v", hold1After, hold1After2)
	}
	// Replay hold lama tetap 200 tanpa tulis/audit.
	code, body = createHold(t, env.srv, env.wsA, draft2, env.ownerToken, pfx+"-hold2", map[string]string{"asset_id": assetID})
	if code != http.StatusOK {
		t.Fatalf("replay hold2: want 200, got %d (%v)", code, body)
	}
	if id, _ := body["id"].(string); id != hold2 {
		t.Fatalf("replay hold2: want id %s, got %s (%v)", hold2, id, body)
	}
	hold2Replay, _ := readHold(t, env.boot, env.wsA, hold2)
	if !reflect.DeepEqual(hold2Before, hold2Replay) {
		t.Fatalf("hold2 berubah sesudah replay:\nsebelum=%+v\nsesudah=%+v", hold2Before, hold2Replay)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "hold_created"); n != holdCreatedBefore {
		t.Fatalf("audit hold_created sesudah replay: want %d, got %d", holdCreatedBefore, n)
	}
	// Availability: periode kedua false (hold + kondisi), periode
	// bebas false HANYA lewat kondisi — tidak ada hold yang tumpang
	// tindih di sana dan custody sudah terminal (bukan out).
	if avail(s2, e2) {
		t.Fatalf("availability periode kedua sesudah resolve: want false, got true")
	}
	if avail(s3, e3) {
		t.Fatalf("availability periode bebas sesudah resolve: want false, got true (guard kondisi tidak bekerja)")
	}
	// Hold BARU pada draft ketiga (periode bebas, kunci baru) → 409
	// persis "unit tidak tersedia" — bukan galat EXCLUDE overlap
	// ("unit tidak tersedia pada periode itu").
	code, body = createHold(t, env.srv, env.wsA, draft3, env.ownerToken, pfx+"-hold3", map[string]string{"asset_id": assetID})
	if code != http.StatusConflict {
		t.Fatalf("hold baru sesudah resolve: want 409, got %d (%v)", code, body)
	}
	if errMsg, _ := body["error"].(string); errMsg != "unit tidak tersedia" {
		t.Fatalf("hold baru sesudah resolve: want error %q, got %q", "unit tidak tersedia", errMsg)
	}
	// Handover BARU pada draft kedua — tunggu jendela nyatanya
	// terbuka supaya bukan galat "periode draft belum dimulai";
	// Rental tetap aktif + fakta ada + hold masih held, jadi 409
	// persis "unit tidak tersedia" hanya bisa dari guard kondisi.
	waitUntil(t, env.boot, s2.Add(500*time.Millisecond))
	code, body = handover(t, env.srv, env.wsA, draft2, env.ownerToken, pfx+"-handover2", handoverBody(assetID, hold2))
	if code != http.StatusConflict {
		t.Fatalf("handover baru sesudah resolve: want 409, got %d (%v)", code, body)
	}
	if errMsg, _ := body["error"].(string); errMsg != "unit tidak tersedia" {
		t.Fatalf("handover baru sesudah resolve: want error %q, got %q", "unit tidak tersedia", errMsg)
	}
	// Tidak ada custody/alokasi/audit baru; hold lama tetap utuh.
	var custodyCount int
	if err := env.boot.QueryRow(`SELECT count(*) FROM rental_custody WHERE workspace_id = $1`, env.wsA).Scan(&custodyCount); err != nil {
		t.Fatalf("hitung custody: %v", err)
	}
	if custodyCount != 1 {
		t.Fatalf("custody sesudah penolakan: want 1, got %d", custodyCount)
	}
	code, _ = getCustody(t, env.srv, env.wsA, draft2, env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("custody draft2 sesudah penolakan: want 404, got %d", code)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_handed_over"); n != handedOverBefore {
		t.Fatalf("audit handed_over sesudah penolakan: want %d, got %d", handedOverBefore, n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, returnAction); n != returnedBefore+1 {
		t.Fatalf("audit %s sesudah penolakan: want %d, got %d", returnAction, returnedBefore+1, n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "hold_created"); n != holdCreatedBefore {
		t.Fatalf("audit hold_created sesudah penolakan: want %d, got %d", holdCreatedBefore, n)
	}
	hold2Final, _ := readHold(t, env.boot, env.wsA, hold2)
	if !reflect.DeepEqual(hold2Before, hold2Final) {
		t.Fatalf("hold2 berubah sesudah penolakan:\nsebelum=%+v\nsesudah=%+v", hold2Before, hold2Final)
	}
}
