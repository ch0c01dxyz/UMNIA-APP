package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Tes integrasi aset fisik + hold F2-01-C (migrasi 007) terhadap
// PostgreSQL asli: skema segar + rerun, RLS/hak runtime/FK komposit/
// EXCLUDE, HTTP registrasi/list/ketersediaan, guard hold, idempotency,
// konkurensi (overlap, kunci sama, create vs deactivate/revoke),
// expiry dekat cutoff dengan lock wait, release idempotent, histori,
// dan fault guard/audit. Entitlement di sini HANYA fixture sintetis
// terisolasi lewat bootstrap (bukan grant nyata); tidak ada otoritas
// pemberian entitlement, billing, harga, stok, atau lifecycle yang
// diuji. Hold bukan rental operasional.

// setupAsset memakai env rental (owner/manager/kasir/lain + dua
// workspace) dan membersihkan baris hold/aset fixture DULU (LIFO:
// cleanup ini didaftarkan sesudah setupRental, jadi berjalan sebelum
// penghapusan draft/parties/items — urutan FK-aman).
func setupAsset(t *testing.T) *activationEnv {
	t.Helper()
	env := setupRental(t)
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM rental_holds WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
		_, _ = env.boot.Exec(`DELETE FROM physical_assets WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	return env
}

func createAsset(t *testing.T, srv *httptest.Server, ws, token string, body any) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/assets", token, body)
}

func createAssetRaw(t *testing.T, srv *httptest.Server, ws, token, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/assets", strings.NewReader(raw))
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

func listAssets(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/assets"+query, token, nil)
}

func getAvailability(t *testing.T, srv *httptest.Server, ws, asset, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/assets/"+asset+"/availability"+query, token, nil)
}

// availQuery membangun query ketersediaan dengan nilai yang
// di-URL-escape: '+' pada zona waktu (mis. +07:00) HARUS di-escape,
// kalau tidak query parser mengubahnya menjadi spasi dan tanggal
// gagal di-parse (bukan salah handler).
func availQuery(starts, ends string) string {
	return "?starts_at=" + url.QueryEscape(starts) + "&ends_at=" + url.QueryEscape(ends)
}

func createHold(t *testing.T, srv *httptest.Server, ws, draft, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/holds", token, []string{key}, body)
}

func createHoldRaw(t *testing.T, srv *httptest.Server, ws, draft, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/holds", strings.NewReader(raw))
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

func releaseHold(t *testing.T, srv *httptest.Server, ws, hold, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/rental/holds/"+hold+"/release", token, nil)
}

func listHolds(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/holds"+query, token, nil)
}

// callHoldAPI adalah varian doDraftReq yang mengembalikan error (bukan
// t.Fatalf) supaya aman dipanggil dari goroutine pada tes konkurensi
// dan tes lock-wait expiry.
func callHoldAPI(srv *httptest.Server, method, path, token string, keys []string, body any) (int, map[string]any, error) {
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

type storedAsset struct {
	ID, WorkspaceID, CatalogItemID, Serial, CreatedAt, UpdatedAt string
}

func readAsset(t *testing.T, boot *sql.DB, ws, id string) (storedAsset, bool) {
	t.Helper()
	var a storedAsset
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, catalog_item_id::text, serial, created_at::text, updated_at::text
		 FROM physical_assets WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&a.ID, &a.WorkspaceID, &a.CatalogItemID, &a.Serial, &a.CreatedAt, &a.UpdatedAt)
	if err == sql.ErrNoRows {
		return storedAsset{}, false
	}
	if err != nil {
		t.Fatalf("read asset %s: %v", id, err)
	}
	return a, true
}

type storedHold struct {
	ID, WorkspaceID, DraftID, AssetID, ClientRequestID, Status, StartsAt, EndsAt, ExpiresAt, CreatedAt, UpdatedAt string
}

func readHold(t *testing.T, boot *sql.DB, ws, id string) (storedHold, bool) {
	t.Helper()
	var h storedHold
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, draft_id::text, asset_id::text, client_request_id, status,
			starts_at::text, ends_at::text, expires_at::text, created_at::text, updated_at::text
		 FROM rental_holds WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&h.ID, &h.WorkspaceID, &h.DraftID, &h.AssetID, &h.ClientRequestID, &h.Status,
		&h.StartsAt, &h.EndsAt, &h.ExpiresAt, &h.CreatedAt, &h.UpdatedAt)
	if err == sql.ErrNoRows {
		return storedHold{}, false
	}
	if err != nil {
		t.Fatalf("read hold %s: %v", id, err)
	}
	return h, true
}

// seedAssetRefs membuat pelanggan + barang lewat API dan mengembalikan
// id keduanya.
func seedAssetRefs(t *testing.T, env *activationEnv, sku string) (custID, itemID string) {
	t.Helper()
	return seedDraftRefs(t, env, sku)
}

// seedDraftForHold membuat draft lewat API untuk periode yang diminta
// dan mengembalikan id draft + body yang dipakai (periode ternormalisasi
// UTC ada di respons).
func seedDraftForHold(t *testing.T, env *activationEnv, cust, item, starts, ends string) (string, map[string]any) {
	t.Helper()
	code, body := createDraft(t, env.srv, env.wsA, env.ownerToken, "hold-draft-"+fmt.Sprint(time.Now().UnixNano()), draftBody(cust, item, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft: want 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	return id, body
}

// seedAsset membuat satu aset lewat API dan mengembalikan id-nya.
func seedAsset(t *testing.T, env *activationEnv, item, serial string) string {
	t.Helper()
	code, body := createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": item, "serial": serial})
	if code != http.StatusCreated {
		t.Fatalf("seed asset: want 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	return id
}

// seedHold membuat satu hold lewat API dan mengembalikan id + responsnya.
func seedHold(t *testing.T, env *activationEnv, draft, asset, key string) (string, map[string]any) {
	t.Helper()
	code, body := createHold(t, env.srv, env.wsA, draft, env.ownerToken, key, map[string]string{"asset_id": asset})
	if code != http.StatusCreated {
		t.Fatalf("seed hold: want 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	return id, body
}

// expireHoldFixture menggeser expires_at satu hold lewat bootstrap
// (fixture sintetis terisolasi, bukan perilaku API): durasi berupa
// interval SQL ('2 seconds', '-1 hour').
func expireHoldFixture(t *testing.T, boot *sql.DB, holdID, interval string) {
	t.Helper()
	if _, err := boot.Exec(`UPDATE rental_holds SET expires_at = clock_timestamp() + $2::interval WHERE id = $1`, holdID, interval); err != nil {
		t.Fatalf("geser expires_at: %v", err)
	}
}

// assetSnapshot menangkap data/kebijakan/ACL/constraint/marker untuk
// uji rerun 007 (pola rentalSnapshot F2-01-B).
func assetSnapshot(t *testing.T, boot *sql.DB) map[string]string {
	t.Helper()
	out := map[string]string{}
	queries := map[string]string{
		"assets":      `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || catalog_item_id::text || '|' || serial || '|' || created_at::text || '|' || updated_at::text, chr(10) ORDER BY id), '') FROM physical_assets`,
		"holds":       `SELECT coalesce(string_agg(id::text || '|' || workspace_id::text || '|' || draft_id::text || '|' || asset_id::text || '|' || client_request_id || '|' || status || '|' || starts_at::text || '|' || ends_at::text || '|' || expires_at::text || '|' || created_at::text || '|' || updated_at::text || '|' || period::text, chr(10) ORDER BY id), '') FROM rental_holds`,
		"policies":    `SELECT coalesce(string_agg(tablename || ':' || policyname || ':' || cmd || ':' || coalesce(qual,'') || ':' || coalesce(with_check,''), chr(10) ORDER BY tablename, policyname), '') FROM pg_policies WHERE tablename IN ('physical_assets', 'rental_holds')`,
		"acl":         `SELECT coalesce(string_agg(relname || ':' || coalesce(relacl::text,''), chr(10) ORDER BY relname), '') FROM pg_class WHERE relname IN ('physical_assets', 'rental_holds')`,
		"markers":     `SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`,
		"constraints": `SELECT coalesce(string_agg(conrelid::regclass::text || ':' || conname || ':' || contype::text, chr(10) ORDER BY conrelid::regclass::text, conname), '') FROM pg_constraint WHERE conrelid IN ('physical_assets'::regclass, 'rental_holds'::regclass, 'rental_contracts'::regclass)`,
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

// TestAssetFreshSchema: migrasi 007 pada database segar — tujuh marker,
// tabel kosong, pemilik bootstrap (bukan runtime), RLS ENABLE+FORCE,
// btree_gist terpasang, hak efektif runtime (aset SELECT/INSERT saja;
// hold SELECT/INSERT + UPDATE kolom status/updated_at saja), kebijakan,
// CHECK/UNIQUE/FK/EXCLUDE, kolom period generated, dan UNIQUE
// (workspace_id, id) pada physical_assets + rental_contracts.
func TestAssetFreshSchema(t *testing.T) {
	boot := bootstrapDB(t)

	var markers string
	if err := boot.QueryRow(
		`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`,
	).Scan(&markers); err != nil {
		t.Fatalf("baca markers: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices" {
		t.Fatalf("markers: want 001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices, got %s", markers)
	}
	for _, table := range []string{"physical_assets", "rental_holds"} {
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
			 WHERE c.relname = $1`, table).Scan(&owner, &super, &rls, &force); err != nil {
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
	// btree_gist terpasang (dipakai EXCLUDE atas kesetaraan UUID).
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM pg_extension WHERE extname = 'btree_gist'`).Scan(&n); err != nil {
		t.Fatalf("cek btree_gist: %v", err)
	}
	if n != 1 {
		t.Fatalf("btree_gist: want 1, got %d", n)
	}
	// Hak efektif runtime pada aset: SELECT/INSERT boleh;
	// UPDATE/DELETE/TRUNCATE tidak.
	for _, p := range []struct {
		action string
		want   bool
	}{
		{"SELECT", true}, {"INSERT", true},
		{"UPDATE", false}, {"DELETE", false}, {"TRUNCATE", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', 'physical_assets', $1)`, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege aset %s: %v", p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime physical_assets %s: want %v, got %v", p.action, p.want, got)
		}
	}
	// Hak efektif runtime pada hold: SELECT/INSERT boleh; UPDATE
	// tabel dilaporkan false oleh has_table_privilege karena grant
	// UPDATE di sini HANYA kolom (status, updated_at) — grant kolom
	// tidak terlihat lewat has_table_privilege, hanya lewat
	// has_column_privilege di bawah. DELETE/TRUNCATE tidak.
	for _, p := range []struct {
		action string
		want   bool
	}{
		{"SELECT", true}, {"INSERT", true}, {"UPDATE", false},
		{"DELETE", false}, {"TRUNCATE", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_table_privilege('umnia_business', 'rental_holds', $1)`, p.action).Scan(&got); err != nil {
			t.Fatalf("has_table_privilege hold %s: %v", p.action, err)
		}
		if got != p.want {
			t.Fatalf("hak runtime rental_holds %s: want %v, got %v", p.action, p.want, got)
		}
	}
	for _, p := range []struct {
		column string
		want   bool
	}{
		{"status", true}, {"updated_at", true},
		{"workspace_id", false}, {"draft_id", false}, {"asset_id", false},
		{"client_request_id", false}, {"starts_at", false}, {"ends_at", false},
		{"expires_at", false}, {"created_at", false}, {"period", false},
	} {
		var got bool
		if err := boot.QueryRow(`SELECT has_column_privilege('umnia_business', 'rental_holds', $1, 'UPDATE')`, p.column).Scan(&got); err != nil {
			t.Fatalf("has_column_privilege hold %s UPDATE: %v", p.column, err)
		}
		if got != p.want {
			t.Fatalf("hak UPDATE kolom rental_holds.%s: want %v, got %v", p.column, p.want, got)
		}
	}
	// Kebijakan: aset SELECT + INSERT (tepat dua); hold SELECT +
	// INSERT + UPDATE (tepat tiga).
	for _, want := range []struct{ table, policy, cmd string }{
		{"physical_assets", "physical_assets_select", "SELECT"},
		{"physical_assets", "physical_assets_insert", "INSERT"},
		{"rental_holds", "rental_holds_select", "SELECT"},
		{"rental_holds", "rental_holds_insert", "INSERT"},
		{"rental_holds", "rental_holds_update", "UPDATE"},
	} {
		var m int
		if err := boot.QueryRow(
			`SELECT count(*) FROM pg_policies WHERE tablename = $1 AND policyname = $2 AND cmd = $3`,
			want.table, want.policy, want.cmd).Scan(&m); err != nil {
			t.Fatalf("cek policy %s: %v", want.policy, err)
		}
		if m != 1 {
			t.Fatalf("policy %s: want 1, got %d", want.policy, m)
		}
	}
	// CHECK 1 (serial) + UNIQUE 2 (serial + workspace_id,id) + FK 2
	// pada physical_assets.
	var c, u, f int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f')
		 FROM pg_constraint WHERE conrelid = 'physical_assets'::regclass`).Scan(&c, &u, &f); err != nil {
		t.Fatalf("constraints physical_assets: %v", err)
	}
	if c != 1 || u != 2 || f != 2 {
		t.Fatalf("constraints physical_assets: want CHECK 1 + UNIQUE 2 + FK 2, got CHECK %d + UNIQUE %d + FK %d", c, u, f)
	}
	// CHECK 3 (status, client_request_id, ends_at>starts_at) +
	// UNIQUE 1 (workspace_id, client_request_id) + FK 3 +
	// EXCLUDE 1 pada rental_holds. (UNIQUE (workspace_id, id) hanya
	// ditambahkan pada physical_assets + rental_contracts sebagai
	// target FK komposit; tidak ada FK yang merujuk rental_holds.)
	var x int
	if err := boot.QueryRow(
		`SELECT count(*) FILTER (WHERE contype = 'c'), count(*) FILTER (WHERE contype = 'u'), count(*) FILTER (WHERE contype = 'f'), count(*) FILTER (WHERE contype = 'x')
		 FROM pg_constraint WHERE conrelid = 'rental_holds'::regclass`).Scan(&c, &u, &f, &x); err != nil {
		t.Fatalf("constraints rental_holds: %v", err)
	}
	if c != 3 || u != 1 || f != 3 || x != 1 {
		t.Fatalf("constraints rental_holds: want CHECK 3 + UNIQUE 1 + FK 3 + EXCLUDE 1, got CHECK %d + UNIQUE %d + FK %d + EXCLUDE %d", c, u, f, x)
	}
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_constraint WHERE conname = 'rental_holds_no_overlap' AND contype = 'x'`).Scan(&n); err != nil {
		t.Fatalf("cek EXCLUDE bernama: %v", err)
	}
	if n != 1 {
		t.Fatalf("rental_holds_no_overlap: want 1, got %d", n)
	}
	// Kolom period adalah generated stored.
	var gen string
	if err := boot.QueryRow(
		`SELECT attgenerated FROM pg_attribute WHERE attrelid = 'rental_holds'::regclass AND attname = 'period'`).Scan(&gen); err != nil {
		t.Fatalf("cek kolom period: %v", err)
	}
	if gen != "s" {
		t.Fatalf("kolom period: want generated stored ('s'), got %q", gen)
	}
	// UNIQUE (workspace_id, id) pada physical_assets dan
	// rental_contracts ada (target FK komposit).
	if err := boot.QueryRow(
		`SELECT count(*) FROM pg_constraint WHERE conname IN ('physical_assets_workspace_id_id_key', 'rental_contracts_workspace_id_id_key') AND contype = 'u'`).Scan(&n); err != nil {
		t.Fatalf("count unique workspace_id_id: %v", err)
	}
	if n != 2 {
		t.Fatalf("UNIQUE (workspace_id, id) aset/draft: want 2, got %d", n)
	}
}

// TestAssetRerunPreservesData: rerun 007 lewat psql ASLI tanpa -1
// (jalur autocommit Linux native) mempertahankan baris, timestamp,
// kebijakan, ACL, constraint, dan marker.
func TestAssetRerunPreservesData(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if bootURL == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL tidak disetel")
	}

	// Cleanup didaftarkan SEBELUM fixture disisipkan (pola rerun
	// F2-01-B: t.Fatalf sebelum registrasi meninggalkan baris yatim).
	ws := "11111111-2222-3333-4444-555555555555"
	cust := "11111111-2222-3333-4444-555555555556"
	item := "11111111-2222-3333-4444-555555555557"
	draft := "11111111-2222-3333-4444-555555555558"
	asset := "11111111-2222-3333-4444-555555555559"
	hold := "11111111-2222-3333-4444-555555555560"
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM rental_holds WHERE id = $1`, hold)
		_, _ = boot.Exec(`DELETE FROM physical_assets WHERE id = $1`, asset)
		_, _ = boot.Exec(`DELETE FROM rental_contracts WHERE id = $1`, draft)
		_, _ = boot.Exec(`DELETE FROM catalog_items WHERE id = $1`, item)
		_, _ = boot.Exec(`DELETE FROM parties WHERE id = $1`, cust)
	})
	// Workspace fixture memakai workspace nyata pertama dari env
	// aktivasi (bukan UUID sintetis): ambil dari setupAsset.
	env := setupAsset(t)
	ws = env.wsA
	if _, err := boot.Exec(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Rerun')`, cust, ws); err != nil {
		t.Fatalf("fixture party: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-RERUN-7', 'Barang Rerun', 'pcs', 'active')`, item, ws); err != nil {
		t.Fatalf("fixture item: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'rerun-7', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft, ws, cust, item); err != nil {
		t.Fatalf("fixture draft: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO physical_assets (id, workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3, 'SN-RERUN-7')`, asset, ws, item); err != nil {
		t.Fatalf("fixture asset: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO rental_holds (id, workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at) VALUES ($1, $2, $3, $4, 'rerun-hold-7', 'held', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`, hold, ws, draft, asset); err != nil {
		t.Fatalf("fixture hold: %v", err)
	}
	before := assetSnapshot(t, boot)
	if !strings.Contains(before["assets"], "SN-RERUN-7") {
		t.Fatalf("snapshot sebelum rerun tidak memuat fixture aset: %q", before["assets"])
	}

	code, out := runPsqlFile(t, bootURL, "migrations/007_asset_availability.sql")
	if code != 0 {
		t.Fatalf("rerun 007: want exit 0, got %d (%s)", code, out)
	}
	after := assetSnapshot(t, boot)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun mengubah snapshot:\nsebelum: %v\nsesudah: %v", before, after)
	}
	// has_permission tetap berfungsi sesudah rerun.
	var ok bool
	if err := boot.QueryRow(`SELECT app.has_permission($1, $2, 'manage_business_data')`, ws, env.ownerID).Scan(&ok); err != nil || !ok {
		t.Fatalf("has_permission sesudah rerun: want true, got %v (%v)", ok, err)
	}
}

// TestAssetRLSAndPermissions: matriks izin + RLS/FK/EXCLUDE lewat
// koneksi runtime yang sebenarnya (bukan koneksi istimewa).
func TestAssetRLSAndPermissions(t *testing.T) {
	env := setupAsset(t)
	db := testDB(t)
	boot := env.boot
	wsA, wsB := env.wsA, env.wsB

	// Rujukan fixture lewat bootstrap (bypass RLS): satu
	// pelanggan+barang+draft+aset per workspace.
	var custA, itemA, custB, itemB, draftA, draftB, assetA string
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
	if err := boot.QueryRow(
		`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, 'rls-draft-a', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z') RETURNING id`, wsA, custA, itemA).Scan(&draftA); err != nil {
		t.Fatalf("fixture draft A: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO rental_contracts (workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, 'rls-draft-b', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z') RETURNING id`, wsB, custB, itemB).Scan(&draftB); err != nil {
		t.Fatalf("fixture draft B: %v", err)
	}
	if err := boot.QueryRow(
		`INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, 'SN-RLS-A') RETURNING id`, wsA, itemA).Scan(&assetA); err != nil {
		t.Fatalf("fixture asset A: %v", err)
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
	countIn := func(ws, subj, table, target string) int {
		t.Helper()
		tx := tenantTx(t, db, ws, subj)
		defer tx.Rollback()
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM `+table+` WHERE workspace_id = $1`, target).Scan(&n); err != nil {
			t.Fatalf("count %s di %s: %v", table, target, err)
		}
		return n
	}
	for _, table := range []string{"physical_assets", "rental_holds"} {
		want := 0
		if table == "physical_assets" {
			want = 1
		}
		if n := countIn(wsA, env.ownerID, table, wsA); n != want {
			t.Fatalf("baca %s A dari A: want %d, got %d", table, want, n)
		}
		if n := countIn(wsA, env.ownerID, table, wsB); n != 0 {
			t.Fatalf("baca %s B dari A: want 0, got %d", table, n)
		}
	}
	for _, table := range []string{"physical_assets", "rental_holds"} {
		var n int
		if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("baca %s tanpa konteks: %v", table, err)
		}
		if n != 0 {
			t.Fatalf("baca %s tanpa konteks: want 0, got %d", table, n)
		}
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
	assetSQL := `INSERT INTO physical_assets (workspace_id, catalog_item_id, serial) VALUES ($1, $2, $3)`
	if code, err := tryInsert(wsA, env.ownerID, assetSQL, wsA, itemA, "SN-RLS-OWNER"); err != nil {
		t.Fatalf("owner INSERT aset: want ok, got %s (%v)", code, err)
	}
	if code, err := tryInsert(wsA, env.mgrID, assetSQL, wsA, itemA, "SN-RLS-MGR"); err != nil {
		t.Fatalf("manager INSERT aset: want ok, got %s (%v)", code, err)
	}
	for name, subj := range map[string]string{"cashier": env.cashID, "nonmember": env.otherID} {
		if code, err := tryInsert(wsA, subj, assetSQL, wsA, itemA, "SN-RLS-"+name); err == nil || code != "42501" {
			t.Fatalf("%s INSERT aset: want 42501, got code %q err %v", name, code, err)
		}
	}
	// FK komposit menolak item lintas-tenant (RLS lolos, FK 23503).
	if code, err := tryInsert(wsA, env.ownerID, assetSQL, wsA, itemB, "SN-RLS-FK"); err == nil || code != "23503" {
		t.Fatalf("INSERT aset FK lintas tenant: want 23503, got code %q err %v", code, err)
	}
	// CHECK serial: kosong dan 65 karakter → 23514.
	for name, serial := range map[string]string{"kosong": "", "65 karakter": strings.Repeat("S", 65)} {
		if code, err := tryInsert(wsA, env.ownerID, assetSQL, wsA, itemA, serial); err == nil || code != "23514" {
			t.Fatalf("INSERT aset serial %s: want 23514, got code %q err %v", name, code, err)
		}
	}
	// Serial duplikat di workspace sama → 23505; di workspace lain boleh.
	if code, err := tryInsert(wsA, env.ownerID, assetSQL, wsA, itemA, "SN-RLS-A"); err == nil || code != "23505" {
		t.Fatalf("INSERT aset serial duplikat: want 23505, got code %q err %v", code, err)
	}
	if code, err := tryInsert(wsB, env.otherID, assetSQL, wsB, itemB, "SN-RLS-A"); err != nil {
		t.Fatalf("INSERT aset serial sama di B: want ok, got %s (%v)", code, err)
	}
	// Hold langsung: owner/manager boleh; kasir/nonmember 42501.
	holdSQL := `INSERT INTO rental_holds (workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at)
		VALUES ($1, $2, $3, $4, 'held', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now() + interval '24 hours')`
	if code, err := tryInsert(wsA, env.ownerID, holdSQL, wsA, draftA, assetA, "rls-hold-owner"); err != nil {
		t.Fatalf("owner INSERT hold: want ok, got %s (%v)", code, err)
	}
	if countRows(t, boot, "rental_holds", wsA) != 1 {
		t.Fatalf("owner INSERT hold: want 1 baris di A, got %d", countRows(t, boot, "rental_holds", wsA))
	}
	for name, subj := range map[string]string{"cashier": env.cashID, "nonmember": env.otherID} {
		if code, err := tryInsert(wsA, subj, holdSQL, wsA, draftA, assetA, "rls-hold-"+name); err == nil || code != "42501" {
			t.Fatalf("%s INSERT hold: want 42501, got code %q err %v", name, code, err)
		}
	}
	// FK komposit menolak draft/aset lintas-tenant → 23503. Periode
	// dibuat berbeda dari hold fixture supaya EXCLUDE overlap tidak
	// mendahului FK (keduanya menolak; yang diuji di sini adalah FK).
	if code, err := tryInsert(wsA, env.ownerID,
		`INSERT INTO rental_holds (workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at)
		 VALUES ($1, $2, $3, 'rls-hold-fk-draft', 'held', '2026-11-20T02:00:00Z', '2026-11-22T02:00:00Z', now() + interval '24 hours')`,
		wsA, draftB, assetA); err == nil || code != "23503" {
		t.Fatalf("INSERT hold FK draft lintas tenant: want 23503, got code %q err %v", code, err)
	}
	// EXCLUDE menolak overlap hold hidup pada aset yang sama → 23P01.
	if code, err := tryInsert(wsA, env.ownerID, holdSQL, wsA, draftA, assetA, "rls-hold-overlap"); err == nil || code != "23P01" {
		t.Fatalf("INSERT hold overlap: want 23P01, got code %q err %v", code, err)
	}
	// Periode bersebelahan dan aset berbeda boleh.
	if code, err := tryInsert(wsA, env.ownerID,
		`INSERT INTO rental_holds (workspace_id, draft_id, asset_id, client_request_id, status, starts_at, ends_at, expires_at)
		 VALUES ($1, $2, $3, 'rls-hold-adjacent', 'held', '2026-11-12T02:00:00Z', '2026-11-14T02:00:00Z', now() + interval '24 hours')`,
		wsA, draftA, assetA); err != nil {
		t.Fatalf("INSERT hold bersebelahan: want ok, got %s (%v)", code, err)
	}
	// UPDATE kolom: status boleh pada baris held; kolom lain 42501;
	// baris released tidak bisa diubah (USING status='held' → 0 baris).
	var heldID string
	if err := boot.QueryRow(`SELECT id FROM rental_holds WHERE workspace_id = $1 AND client_request_id = 'rls-hold-owner'`, wsA).Scan(&heldID); err != nil {
		t.Fatalf("cari hold fixture: %v", err)
	}
	tryUpdate := func(ws, subj, query string, args ...any) (string, int64, error) {
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
	if code, n, err := tryUpdate(wsA, env.ownerID,
		`UPDATE rental_holds SET status = 'released', updated_at = now() WHERE id = $1`, heldID); err != nil || n != 1 {
		t.Fatalf("UPDATE status hold held: want 1 baris, got code %q n=%d err %v", code, n, err)
	}
	for name, q := range map[string]string{
		"expires_at":        `UPDATE rental_holds SET expires_at = now() WHERE id = $1`,
		"client_request_id": `UPDATE rental_holds SET client_request_id = 'x' WHERE id = $1`,
		"starts_at":         `UPDATE rental_holds SET starts_at = now() WHERE id = $1`,
		"asset_id":          `UPDATE rental_holds SET asset_id = $2 WHERE id = $1`,
	} {
		args := []any{heldID}
		if name == "asset_id" {
			args = append(args, assetA)
		}
		if code, _, err := tryUpdate(wsA, env.ownerID, q, args...); err == nil || code != "42501" {
			t.Fatalf("UPDATE hold kolom %s: want 42501, got code %q err %v", name, code, err)
		}
	}
	if code, n, err := tryUpdate(wsA, env.ownerID,
		`UPDATE rental_holds SET status = 'expired', updated_at = now() WHERE id = $1`, heldID); err != nil || n != 0 {
		t.Fatalf("UPDATE status hold released: want 0 baris (USING held), got code %q n=%d err %v", code, n, err)
	}
	// UPDATE pada aset dan DELETE/TRUNCATE kedua tabel → 42501.
	{
		tx := tenantTx(t, db, wsA, env.ownerID)
		_, err := tx.Exec(`UPDATE physical_assets SET serial = 'SN-X' WHERE workspace_id = $1`, wsA)
		tx.Rollback()
		if err == nil || pgCode(err) != "42501" {
			t.Fatalf("UPDATE physical_assets: want 42501, got %v", err)
		}
	}
	for _, table := range []string{"physical_assets", "rental_holds"} {
		{
			tx := tenantTx(t, db, wsA, env.ownerID)
			_, err := tx.Exec(`DELETE FROM `+table+` WHERE workspace_id = $1`, wsA)
			tx.Rollback()
			if err == nil || pgCode(err) != "42501" {
				t.Fatalf("DELETE %s: want 42501, got %v", table, err)
			}
		}
		{
			tx := tenantTx(t, db, wsA, env.ownerID)
			_, err := tx.Exec(`TRUNCATE ` + table)
			tx.Rollback()
			if err == nil || pgCode(err) != "42501" {
				t.Fatalf("TRUNCATE %s: want 42501, got %v", table, err)
			}
		}
	}
}

// TestAssetHTTP: registrasi/list lewat API nyata (barang dibuat lewat
// API, bukan insert istimewa), matriks guard/role/tenant, rujukan
// hilang/lintas-tenant/arip, dan matriks validasi 400.
func TestAssetHTTP(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-HTTP-A")

	// Happy path owner 201, field lengkap.
	code, body := createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": item, "serial": "SN-HTTP-1"})
	if code != http.StatusCreated {
		t.Fatalf("create aset owner: want 201, got %d (%v)", code, body)
	}
	assetID, _ := body["id"].(string)
	if body["workspace_id"] != env.wsA || body["catalog_item_id"] != item || body["serial"] != "SN-HTTP-1" {
		t.Fatalf("create aset: field tidak cocok: %v", body)
	}
	if body["created_at"] == nil || body["updated_at"] == nil {
		t.Fatalf("create aset: timestamp tidak ada: %v", body)
	}
	if countRows(t, env.boot, "physical_assets", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "asset_registered") != 1 {
		t.Fatalf("create aset: want 1 baris + 1 audit")
	}
	// Manager boleh; barang aktif lain di wsA untuk validasi nanti.
	_, itemC := seedAssetRefs(t, env, "SKU-HTTP-C")
	// Barang lintas-tenant: dibuat lewat API di wsB (owner-nya other).
	code, body = doJSON(t, env.srv, "POST", "/workspaces/"+env.wsB+"/catalog/items", env.otherToken,
		map[string]string{"sku": "SKU-HTTP-B", "name": "Barang B", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("seed item wsB: want 201, got %d (%v)", code, body)
	}
	itemB, _ := body["id"].(string)
	code, _ = createAsset(t, env.srv, env.wsA, env.mgrToken, map[string]string{"catalog_item_id": item, "serial": "SN-HTTP-2"})
	if code != http.StatusCreated {
		t.Fatalf("create aset manager: want 201, got %d", code)
	}
	// Serial duplikat di workspace sama → 409 (hanya 23505 asli).
	code, body = createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": item, "serial": "SN-HTTP-1"})
	if code != http.StatusConflict {
		t.Fatalf("serial duplikat: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "serial sudah dipakai")
	// Barang hilang / lintas-tenant → 404 identik.
	unknown := "99999999-8888-7777-6666-555555555555"
	code, body = createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": unknown, "serial": "SN-HTTP-3"})
	if code != http.StatusNotFound {
		t.Fatalf("item hilang: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak ditemukan")
	code, body = createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": itemB, "serial": "SN-HTTP-4"})
	if code != http.StatusNotFound {
		t.Fatalf("item lintas tenant: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak ditemukan")
	_ = cust
	// Item own archived → 409.
	archiveItem(t, env.boot, item)
	code, body = createAsset(t, env.srv, env.wsA, env.ownerToken, map[string]string{"catalog_item_id": item, "serial": "SN-HTTP-5"})
	if code != http.StatusConflict {
		t.Fatalf("item archived: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak aktif")
	// List: owner + kasir 200; pagination limit=1; query invalid 400.
	code, body = listAssets(t, env.srv, env.wsA, env.ownerToken, "")
	if code != http.StatusOK {
		t.Fatalf("list aset: want 200, got %d (%v)", code, body)
	}
	items, _ := body["items"].([]any)
	if len(items) != 2 {
		t.Fatalf("list aset: want 2, got %d (%v)", len(items), body)
	}
	code, body = listAssets(t, env.srv, env.wsA, env.cashToken, "")
	if code != http.StatusOK {
		t.Fatalf("list aset kasir: want 200, got %d (%v)", code, body)
	}
	code, body = listAssets(t, env.srv, env.wsA, env.ownerToken, "?limit=1&offset=1")
	if code != http.StatusOK {
		t.Fatalf("list aset limit=1: want 200, got %d (%v)", code, body)
	}
	if items, _ := body["items"].([]any); len(items) != 1 {
		t.Fatalf("list aset limit=1 offset=1: want 1, got %d", len(items))
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?offset=-1", "?limit=abc"} {
		code, _ := listAssets(t, env.srv, env.wsA, env.ownerToken, q)
		if code != http.StatusBadRequest {
			t.Fatalf("list aset %s: want 400, got %d", q, code)
		}
	}
	// Kasir tidak boleh registrasi (403); tanpa token 401; nonmember 403.
	code, _ = createAsset(t, env.srv, env.wsA, env.cashToken, map[string]string{"catalog_item_id": itemB, "serial": "SN-HTTP-6"})
	if code != http.StatusForbidden {
		t.Fatalf("create aset kasir: want 403, got %d", code)
	}
	code, _ = createAsset(t, env.srv, env.wsA, "", map[string]string{"catalog_item_id": itemB, "serial": "SN-HTTP-7"})
	if code != http.StatusUnauthorized {
		t.Fatalf("create aset tanpa token: want 401, got %d", code)
	}
	code, _ = createAsset(t, env.srv, env.wsA, env.otherToken, map[string]string{"catalog_item_id": itemB, "serial": "SN-HTTP-8"})
	if code != http.StatusForbidden {
		t.Fatalf("create aset nonmember: want 403, got %d", code)
	}
	code, _ = listAssets(t, env.srv, env.wsA, env.otherToken, "")
	if code != http.StatusForbidden {
		t.Fatalf("list aset nonmember: want 403, got %d", code)
	}
	// Matriks validasi 400 (barang masih aktif = itemC).
	for name, raw := range map[string]string{
		"field tidak dikenal":  `{"catalog_item_id":"` + itemC + `","serial":"SN-X","extra":1}`,
		"item bukan UUID":      `{"catalog_item_id":"bukan-uuid","serial":"SN-X"}`,
		"serial hilang":        `{"catalog_item_id":"` + itemC + `"}`,
		"serial kosong":        `{"catalog_item_id":"` + itemC + `","serial":"   "}`,
		"serial 65 karakter":   `{"catalog_item_id":"` + itemC + `","serial":"` + strings.Repeat("S", 65) + `"}`,
		"dokumen ganda":        `{"catalog_item_id":"` + itemC + `","serial":"SN-X"} {}`,
		"workspace bukan UUID": `{"catalog_item_id":"` + itemC + `","serial":"SN-X"}`,
	} {
		code, _ := createAssetRaw(t, env.srv, env.wsA, env.ownerToken, raw)
		if name == "workspace bukan UUID" {
			code, _ = createAssetRaw(t, env.srv, "bukan-uuid", env.ownerToken, raw)
		}
		if code != http.StatusBadRequest {
			t.Fatalf("validasi %s: want 400, got %d", name, code)
		}
	}
	// Body > 16KiB → 400.
	code, _ = createAssetRaw(t, env.srv, env.wsA, env.ownerToken,
		`{"catalog_item_id":"`+itemC+`","serial":"`+strings.Repeat("S", 17<<10)+`"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("body oversize: want 400, got %d", code)
	}
	_ = assetID
}

// TestAssetAvailability: bacaan ketersediaan advisory — hold hidup
// memblokir, bersebelahan/beda aset/release/kedaluwarsa tidak.
func TestAssetAvailability(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-AVAIL-A")
	_, item2 := seedAssetRefs(t, env, "SKU-AVAIL-B")
	activateRental(t, env)
	assetA := seedAsset(t, env, item, "SN-AVAIL-A")
	assetB := seedAsset(t, env, item2, "SN-AVAIL-B")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	holdID, _ := seedHold(t, env, draft, assetA, "avail-hold-1")

	check := func(asset, starts, ends string, want bool) {
		t.Helper()
		code, body := getAvailability(t, env.srv, env.wsA, asset, env.ownerToken, availQuery(starts, ends))
		if code != http.StatusOK {
			t.Fatalf("availability %s %s-%s: want 200, got %d (%v)", asset, starts, ends, code, body)
		}
		got, _ := body["available"].(bool)
		if got != want {
			t.Fatalf("availability %s %s-%s: want %v, got %v (%v)", asset, starts, ends, want, got, body)
		}
		if body["asset_id"] != asset {
			t.Fatalf("availability: want asset_id %s, got %v", asset, body["asset_id"])
		}
		// Tanggal respons harus ternormalisasi ke UTC dan sama
		// instannya dengan input.
		for _, field := range []string{"starts_at", "ends_at"} {
			s, _ := body[field].(string)
			if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
				t.Fatalf("availability %s: want RFC3339Nano, got %q", field, s)
			}
		}
	}
	// Periode tumpang tindih → false; bersebelahan (mulai tepat di
	// ends) → true; sebelum periode → true; aset berbeda → true.
	check(assetA, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00", false)
	check(assetA, "2026-11-11T09:00:00+07:00", "2026-11-13T09:00:00+07:00", false)
	check(assetA, "2026-11-12T09:00:00+07:00", "2026-11-14T09:00:00+07:00", true)
	check(assetA, "2026-11-08T09:00:00+07:00", "2026-11-10T09:00:00+07:00", true)
	check(assetB, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00", true)
	// Kasir boleh membaca; aset hilang/lintas-tenant → 404 identik.
	code, _ := getAvailability(t, env.srv, env.wsA, assetA, env.cashToken,
		availQuery("2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusOK {
		t.Fatalf("availability kasir: want 200, got %d", code)
	}
	code, body := getAvailability(t, env.srv, env.wsA, "99999999-8888-7777-6666-555555555555", env.ownerToken,
		availQuery("2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusNotFound {
		t.Fatalf("availability aset hilang: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "aset tidak ditemukan")
	code, _ = getAvailability(t, env.srv, env.wsA, assetB, env.otherToken,
		availQuery("2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusForbidden {
		t.Fatalf("availability nonmember: want 403, got %d", code)
	}
	// Matriks tanggal 400.
	for name, q := range map[string]string{
		"tanpa zona":                  availQuery("2026-11-10T09:00:00", "2026-11-12T09:00:00+07:00"),
		"ends<=starts":                availQuery("2026-11-12T09:00:00+07:00", "2026-11-10T09:00:00+07:00"),
		"tahun input 10000":           availQuery("10000-01-01T09:00:00+07:00", "10000-01-02T09:00:00+07:00"),
		"tahun input 0000":            availQuery("0000-01-01T09:00:00+07:00", "0000-01-02T09:00:00+07:00"),
		"lintas atas ternormalisasi":  availQuery("9999-12-31T23:30:00-02:00", "9999-12-31T23:45:00-02:00"),
		"lintas bawah ternormalisasi": availQuery("0001-01-01T00:15:00+02:00", "0001-01-01T00:30:00+02:00"),
		"param hilang":                "",
	} {
		code, _ := getAvailability(t, env.srv, env.wsA, assetA, env.ownerToken, q)
		if code != http.StatusBadRequest {
			t.Fatalf("availability %s: want 400, got %d", name, code)
		}
	}
	// Release → tersedia lagi; expiry fixture → tersedia lagi.
	code, _ = releaseHold(t, env.srv, env.wsA, holdID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release untuk availability: want 200, got %d", code)
	}
	check(assetA, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00", true)
	holdID2, _ := seedHold(t, env, draft, assetA, "avail-hold-2")
	check(assetA, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00", false)
	expireHoldFixture(t, env.boot, holdID2, "-1 hour")
	check(assetA, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00", true)
}

// TestHoldCreate: happy path + guard — periode disalin dari draft,
// expires_at = created_at + 24 jam persis; rujukan 404 identik;
// mismatch item 409; arsip 409; periode berakhir 409; periode sedang
// berjalan boleh; inactive/revoked/read-denial 403; fault 500.
func TestHoldCreate(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-HOLD-A")
	_, item2 := seedAssetRefs(t, env, "SKU-HOLD-B")
	activateRental(t, env)
	assetA := seedAsset(t, env, item, "SN-HOLD-A")
	assetB := seedAsset(t, env, item2, "SN-HOLD-B")
	draft, draftBody := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	code, body := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "hold-1", map[string]string{"asset_id": assetA})
	if code != http.StatusCreated {
		t.Fatalf("create hold: want 201, got %d (%v)", code, body)
	}
	holdID, _ := body["id"].(string)
	if body["workspace_id"] != env.wsA || body["draft_id"] != draft || body["asset_id"] != assetA ||
		body["client_request_id"] != "hold-1" || body["status"] != "held" {
		t.Fatalf("create hold: field tidak cocok: %v", body)
	}
	// Periode disalin dari draft (UTC ternormalisasi).
	if body["starts_at"] != draftBody["starts_at"] || body["ends_at"] != draftBody["ends_at"] {
		t.Fatalf("hold periode: want sama dengan draft %v/%v, got %v/%v",
			draftBody["starts_at"], draftBody["ends_at"], body["starts_at"], body["ends_at"])
	}
	// TTL: expires_at - created_at harus persis 24 jam (keduanya
	// berasal dari satu cutoff jam database).
	created, _ := time.Parse(time.RFC3339Nano, body["created_at"].(string))
	expires, _ := time.Parse(time.RFC3339Nano, body["expires_at"].(string))
	if expires.Sub(created) != 24*time.Hour {
		t.Fatalf("hold TTL: want 24h, got %v (created %v expires %v)", expires.Sub(created), created, expires)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 1 {
		t.Fatalf("create hold: want 1 baris + 1 audit")
	}
	// Rujukan hilang/lintas-tenant → 404 identik, tanpa tulis/audit.
	unknown := "99999999-8888-7777-6666-555555555555"
	cases404 := []struct {
		name string
		d, a string
	}{
		{"draft hilang", unknown, assetA},
		{"aset hilang", draft, unknown},
		{"draft lintas tenant", unknown, assetB},
	}
	for i, tc := range cases404 {
		code, body := createHold(t, env.srv, env.wsA, tc.d, env.ownerToken, "hold-404-"+fmt.Sprint(i), map[string]string{"asset_id": tc.a})
		if code != http.StatusNotFound {
			t.Fatalf("create hold %s: want 404, got %d (%v)", tc.name, code, body)
		}
	}
	// Aset milik item lain → 409 (item tidak cocok dengan draft).
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "hold-mismatch", map[string]string{"asset_id": assetB})
	if code != http.StatusConflict {
		t.Fatalf("aset item beda: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "aset tidak cocok dengan draft")
	// Item diarsipkan sesudah draft dibuat → hold BARU 409.
	archiveItem(t, env.boot, item)
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "hold-archived", map[string]string{"asset_id": assetA})
	if code != http.StatusConflict {
		t.Fatalf("hold baru item archived: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak aktif")
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 {
		t.Fatalf("hold ditolak: want tetap 1 baris, got %d", countRows(t, env.boot, "rental_holds", env.wsA))
	}
	// Draft dengan periode yang sudah berakhir → 409; periode yang
	// sedang berjalan (starts lalu, ends depan) → 201.
	_, itemC := seedAssetRefs(t, env, "SKU-HOLD-C")
	assetC := seedAsset(t, env, itemC, "SN-HOLD-C")
	pastDraft, _ := seedDraftForHold(t, env, cust, itemC, "2026-01-01T09:00:00+07:00", "2026-01-02T09:00:00+07:00")
	code, body = createHold(t, env.srv, env.wsA, pastDraft, env.ownerToken, "hold-ended", map[string]string{"asset_id": assetC})
	if code != http.StatusConflict {
		t.Fatalf("hold periode berakhir: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "periode draft sudah berakhir")
	ongoingDraft, _ := seedDraftForHold(t, env, cust, itemC, "2020-01-01T09:00:00+07:00", "2999-01-02T09:00:00+07:00")
	code, body = createHold(t, env.srv, env.wsA, ongoingDraft, env.ownerToken, "hold-ongoing", map[string]string{"asset_id": assetC})
	if code != http.StatusCreated {
		t.Fatalf("hold periode berjalan: want 201, got %d (%v)", code, body)
	}
	_ = holdID
}

// TestHoldGuardDenials: inactive/uninstalled/revoked → 403; izin baca
// core eksplisit ditolak → 403; fault query izin → 500.
func TestHoldGuardDenials(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-GUARD-A")
	asset := seedAsset(t, env, item, "SN-GUARD-A")

	// Belum diaktivasi sama sekali → 403.
	code, body := createHold(t, env.srv, env.wsA, "00000000-0000-0000-0000-000000000000", env.ownerToken, "guard-1", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("hold tanpa instalasi: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "rental belum aktif di workspace ini")

	// Aktifkan, buat draft, lalu deaktivasi → 403.
	activateRental(t, env)
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	if code, _ := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate prasyarat: want 200, got %d", code)
	}
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "guard-2", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("hold sesudah deactivate: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "rental belum aktif di workspace ini")
	// Aktifkan lagi, lalu revoke entitlement → 403.
	activateRental(t, env)
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "guard-3", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("hold sesudah revoke: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "rental belum menjadi hak workspace ini")
	// Role sintetis: manage_business_data saja, tanpa view_workspace
	// → 403 izin baca core, tanpa mutasi/audit.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	if _, err := env.boot.Exec(`INSERT INTO roles (name, description) VALUES ('qa-hold-manage-only', 'fixture: manage_business_data saja') ON CONFLICT (name) DO NOTHING`); err != nil {
		t.Fatalf("buat role sintetis: %v", err)
	}
	if _, err := env.boot.Exec(`DELETE FROM role_permissions WHERE role = 'qa-hold-manage-only'`); err != nil {
		t.Fatalf("reset izin role sintetis: %v", err)
	}
	if _, err := env.boot.Exec(`INSERT INTO role_permissions (role, permission) VALUES ('qa-hold-manage-only', 'manage_business_data')`); err != nil {
		t.Fatalf("beri manage_business_data: %v", err)
	}
	if _, err := env.boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2`, env.wsA, env.mgrID); err != nil {
		t.Fatalf("hapus role manager: %v", err)
	}
	if _, err := env.boot.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'qa-hold-manage-only')`, env.wsA, env.mgrID); err != nil {
		t.Fatalf("pasang role sintetis: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2 AND role = 'qa-hold-manage-only'`, env.wsA, env.mgrID)
		_, _ = env.boot.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'manager') ON CONFLICT (workspace_id, subject_id, role) DO NOTHING`, env.wsA, env.mgrID)
		_, _ = env.boot.Exec(`DELETE FROM role_permissions WHERE role = 'qa-hold-manage-only'`)
		_, _ = env.boot.Exec(`DELETE FROM roles WHERE name = 'qa-hold-manage-only'`)
	})
	code, body = createHold(t, env.srv, env.wsA, draft, env.mgrToken, "guard-4", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("hold tanpa izin baca core: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "tidak punya izin membaca data core")
	if countRows(t, env.boot, "rental_holds", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 0 {
		t.Fatalf("hold ditolak: want 0 baris + 0 audit")
	}
	// Fault query izin baca → 500 (pakai owner agar role asli).
	def := installPermissionFault(t, env.boot)
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "guard-5", map[string]string{"asset_id": asset})
	if code != http.StatusInternalServerError || body["error"] != "izin gagal diperiksa" {
		t.Fatalf("fault view_workspace: want 500 izin gagal diperiksa, got %d (%v)", code, body)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 0 {
		t.Fatalf("fault view_workspace: want 0 baris")
	}
	restorePermissionFault(t, env.boot, def)
	// Fault query izin tulis → 500.
	def = installPermissionFaultPerm(t, env.boot, "manage_business_data")
	t.Cleanup(func() { restorePermissionFault(t, env.boot, def) })
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "guard-6", map[string]string{"asset_id": asset})
	if code != http.StatusInternalServerError || body["error"] != "izin gagal diperiksa" {
		t.Fatalf("fault manage_business_data: want 500 izin gagal diperiksa, got %d (%v)", code, body)
	}
	restorePermissionFault(t, env.boot, def)
	// Sesudah pemulihan, hold yang sama berhasil 201.
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "guard-ok", map[string]string{"asset_id": asset})
	if code != http.StatusCreated {
		t.Fatalf("hold sesudah pemulihan: want 201, got %d (%v)", code, body)
	}
}

// TestHoldIdempotency: replay 200 tanpa tulis/audit; kunci sama +
// payload beda → 409; replay tetap berlaku sesudah arsip/revoke;
// kunci released/expired tetap terikat; kunci case-sensitive.
func TestHoldIdempotency(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-IDEM-A")
	_, item2 := seedAssetRefs(t, env, "SKU-IDEM-B")
	activateRental(t, env)
	assetA := seedAsset(t, env, item, "SN-IDEM-A")
	assetB := seedAsset(t, env, item2, "SN-IDEM-B")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	// Pertama 201; replay persis → 200 dengan DTO yang sama, tanpa
	// baris/audit tambahan.
	code, first := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetA})
	if code != http.StatusCreated {
		t.Fatalf("hold pertama: want 201, got %d (%v)", code, first)
	}
	holdID, _ := first["id"].(string)
	code, replay := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetA})
	if code != http.StatusOK {
		t.Fatalf("replay sama: want 200, got %d (%v)", code, replay)
	}
	if !reflect.DeepEqual(first, replay) {
		t.Fatalf("replay: want DTO sama persis, got %v vs %v", first, replay)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 1 {
		t.Fatalf("replay: want tetap 1 baris + 1 audit")
	}
	// Kunci sama + aset berbeda (draft sama) → 409, tanpa tulis.
	code, body := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetB})
	if code != http.StatusConflict {
		t.Fatalf("kunci sama payload beda: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 {
		t.Fatalf("409: want tetap 1 baris")
	}
	// Replay tetap 200 sesudah item diarsipkan (bacaan histori, bukan
	// hold baru).
	archiveItem(t, env.boot, item)
	code, _ = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetA})
	if code != http.StatusOK {
		t.Fatalf("replay sesudah arsip: want 200, got %d", code)
	}
	// Replay sesudah revoke → 403 (hak saat ini dicek dulu).
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	code, _ = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetA})
	if code != http.StatusForbidden {
		t.Fatalf("replay sesudah revoke: want 403, got %d", code)
	}
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	// Kunci yang sudah released tetap terikat: replay → 200 dengan
	// status released, tanpa audit baru.
	code, _ = releaseHold(t, env.srv, env.wsA, holdID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release prasyarat: want 200, got %d", code)
	}
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "idem-1", map[string]string{"asset_id": assetA})
	if code != http.StatusOK {
		t.Fatalf("replay kunci released: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "released" {
		t.Fatalf("replay kunci released: want status released, got %v", body)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 1 {
		t.Fatalf("replay kunci released: want tetap 1 baris + 1 audit")
	}
	// Kunci case-sensitive: kunci yang hanya beda huruf besar/kecil
	// adalah kunci berbeda — buktikan pada draft/aset lain: bila
	// kunci dibandingkan case-insensitive, permintaan kedua adalah
	// replay → 200; bila case-sensitive, ini hold baru yang overlap
	// dengan hold pertama → 409 EXCLUDE.
	draft2, _ := seedDraftForHold(t, env, cust, item2, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	code, _ = createHold(t, env.srv, env.wsA, draft2, env.ownerToken, "IDEM-CASE-1", map[string]string{"asset_id": assetB})
	if code != http.StatusCreated {
		t.Fatalf("hold kunci huruf besar: want 201, got %d", code)
	}
	code, body = createHold(t, env.srv, env.wsA, draft2, env.ownerToken, "idem-case-1", map[string]string{"asset_id": assetB})
	if code != http.StatusConflict {
		t.Fatalf("kunci beda kasus: want 409 overlap (kunci berbeda), got %d (%v)", code, body)
	}
	assertFlatError(t, body, "unit tidak tersedia pada periode itu")
	// Header: dua nilai / nilai tidak valid / tanpa header → 400.
	for name, keys := range map[string][]string{
		"tanpa header": {},
		"dua nilai":    {"idem-x", "idem-y"},
		"nilai spasi":  {"kunci buruk"},
		"nilai kosong": {""},
	} {
		code, _ := createHoldRaw(t, env.srv, env.wsA, draft2, env.ownerToken, keys, `{"asset_id":"`+assetB+`"}`)
		if code != http.StatusBadRequest {
			t.Fatalf("header %s: want 400, got %d", name, code)
		}
	}
	// Body: field tidak dikenal / asset_id bukan UUID → 400.
	code, _ = createHoldRaw(t, env.srv, env.wsA, draft2, env.ownerToken, []string{"idem-body-1"}, `{"asset_id":"`+assetB+`","extra":1}`)
	if code != http.StatusBadRequest {
		t.Fatalf("body field tidak dikenal: want 400, got %d", code)
	}
	code, _ = createHoldRaw(t, env.srv, env.wsA, draft2, env.ownerToken, []string{"idem-body-2"}, `{"asset_id":"bukan-uuid"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("body asset bukan UUID: want 400, got %d", code)
	}
}

// TestHoldConcurrentSameAsset: dua staf, aset + periode sama, kunci
// berbeda → satu 201 + satu 409, satu hold + satu audit. Kunci sama
// paralel → satu 201 + satu 200, tetap satu hold + satu audit.
func TestHoldConcurrentSameAsset(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-RACE-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-RACE-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	// Kunci berbeda, aset+periode sama.
	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/holds", env.ownerToken, []string{"race-1"}, map[string]string{"asset_id": asset})
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/holds", env.mgrToken, []string{"race-2"}, map[string]string{"asset_id": asset})
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{http.StatusCreated, http.StatusConflict}) {
		t.Fatalf("konkurren kunci beda: want [201 409], got %v", codes)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 1 {
		t.Fatalf("konkurren kunci beda: want 1 hold + 1 audit")
	}
	// Kunci sama paralel pada draft/aset lain → satu 201 + satu 200.
	_, item2 := seedAssetRefs(t, env, "SKU-RACE-B")
	asset2 := seedAsset(t, env, item2, "SN-RACE-B")
	draft2, _ := seedDraftForHold(t, env, cust, item2, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	codes, _ = parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft2+"/holds", env.ownerToken, []string{"race-same"}, map[string]string{"asset_id": asset2})
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft2+"/holds", env.mgrToken, []string{"race-same"}, map[string]string{"asset_id": asset2})
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{http.StatusOK, http.StatusCreated}) {
		t.Fatalf("konkurren kunci sama: want [200 201], got %v", codes)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 2 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 2 {
		t.Fatalf("konkurren kunci sama: want 2 hold + 2 audit")
	}
	// Periode bersebelahan dan aset berbeda → dua 201 (bukan overlap).
	draft3, _ := seedDraftForHold(t, env, cust, item, "2026-11-12T09:00:00+07:00", "2026-11-14T09:00:00+07:00")
	code, _ := createHold(t, env.srv, env.wsA, draft3, env.ownerToken, "race-adjacent", map[string]string{"asset_id": asset})
	if code != http.StatusCreated {
		t.Fatalf("hold bersebelahan: want 201, got %d", code)
	}
	_, item3 := seedAssetRefs(t, env, "SKU-RACE-C")
	asset3 := seedAsset(t, env, item3, "SN-RACE-C")
	draft4, _ := seedDraftForHold(t, env, cust, item3, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	code, _ = createHold(t, env.srv, env.wsA, draft4, env.ownerToken, "race-other-asset", map[string]string{"asset_id": asset3})
	if code != http.StatusCreated {
		t.Fatalf("hold aset berbeda: want 201, got %d", code)
	}
}

// TestHoldCreateVsDeactivateRace + TestHoldCreateVsRevokeRace:
// balapan create hold vs deactivate/revoke ditentukan urutan lock:
// create commit dulu → hold tercatat (201, 1 hold, 1 audit); mutasi
// commit dulu → create 403 (0 hold, 0 audit). Urutan commit tidak
// disimpulkan dari created_at (bukan kolom urutan commit).
func TestHoldCreateVsDeactivateRace(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-RACE-D")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-RACE-D")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/holds", env.ownerToken, []string{"race-deact"}, map[string]string{"asset_id": asset})
		},
		func() (int, map[string]any, error) {
			return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/rental.basic/deactivate", env.ownerToken, nil)
		},
	)
	sort.Ints(codes)
	holds := countRows(t, env.boot, "rental_holds", env.wsA)
	audits := countBusinessAudits(t, env.boot, env.wsA, "hold_created")
	switch {
	case reflect.DeepEqual(codes, []int{http.StatusOK, http.StatusCreated}) && holds == 1 && audits == 1:
		// Create commit dulu: hold tercatat sebagai histori valid.
	case reflect.DeepEqual(codes, []int{http.StatusOK, http.StatusForbidden}) && holds == 0 && audits == 0:
		// Deactivate commit dulu: create 403.
	default:
		t.Fatalf("balapan create vs deactivate: want (201+200, 1 hold, 1 audit) atau (403+200, 0, 0), got codes %v holds=%d audits=%d", codes, holds, audits)
	}
}

func TestHoldCreateVsRevokeRace(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-RACE-R")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-RACE-R")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			// Revoke lewat bootstrap dengan lock yang sama: balapan
			// nyata antara revoke dan create hold.
			if err := revokeEntitlementErr(env.boot, env.wsA, "rental.basic"); err == nil {
				return
			}
		}
	}()
	code, _ := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "race-revoke", map[string]string{"asset_id": asset})
	<-done
	holds := countRows(t, env.boot, "rental_holds", env.wsA)
	audits := countBusinessAudits(t, env.boot, env.wsA, "hold_created")
	switch code {
	case http.StatusCreated:
		if holds != 1 || audits != 1 {
			t.Fatalf("create menang balapan: want 1 hold + 1 audit, got %d + %d", holds, audits)
		}
	case http.StatusForbidden:
		if holds != 0 || audits != 0 {
			t.Fatalf("revoke menang balapan: want 0 hold + 0 audit, got %d + %d", holds, audits)
		}
	default:
		t.Fatalf("balapan create vs revoke: want 201 atau 403, got %d", code)
	}
}

// TestHoldExpiryNearCutoffWithLockWait: permintaan create dimulai
// SEBELUM deadline, menunggu workspace lock, dan mendapat lock
// SESUDAH deadline → hold lama yang kedaluwarsa tidak lagi memblokir.
// Ini membuktikan cutoff (clock_timestamp) diambil SESUDAH lock,
// bukan di awal transaksi/permintaan: bila cutoff diambil sebelum
// lock, hold lama masih "held" dan EXCLUDE menolak dengan 409.
// Tanpa sleep 24 jam: deadline fixture sintetis 2 detik.
func TestHoldExpiryNearCutoffWithLockWait(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-EXP-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-EXP-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	oldHold, _ := seedHold(t, env, draft, asset, "exp-old")
	// Deadline 2 detik ke depan (fixture bootstrap, bukan TTL API).
	expireHoldFixture(t, env.boot, oldHold, "2 seconds")

	// Pegang workspace lock dari transaksi bootstrap terpisah.
	lockTx, err := env.boot.Begin()
	if err != nil {
		t.Fatalf("begin lock tx: %v", err)
	}
	defer lockTx.Rollback()
	var locked string
	if err := lockTx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, env.wsA).Scan(&locked); err != nil {
		t.Fatalf("pegang workspace lock: %v", err)
	}
	// Permintaan create dimulai SEKARANG (sebelum deadline) dan akan
	// menunggu lock di dalam handler.
	type result struct {
		code int
		body map[string]any
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		code, body, err := callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/contracts/"+draft+"/holds", env.ownerToken, []string{"exp-new"}, map[string]string{"asset_id": asset})
		ch <- result{code, body, err}
	}()
	// Tidur melewati deadline selagi permintaan masih menunggu lock.
	time.Sleep(4 * time.Second)
	// Lepas lock: permintaan kini mendapat lock SESUDAH deadline.
	if err := lockTx.Commit(); err != nil {
		t.Fatalf("commit lock tx: %v", err)
	}
	res := <-ch
	if res.err != nil {
		t.Fatalf("create sesudah lock: %v", res.err)
	}
	if res.code != http.StatusCreated {
		t.Fatalf("create sesudah deadline lock-wait: want 201, got %d (%v)", res.code, res.body)
	}
	// Hold lama berstatus expired di database (lazy expiry
	// ter-persist oleh create yang berhasil).
	old, ok := readHold(t, env.boot, env.wsA, oldHold)
	if !ok {
		t.Fatalf("hold lama hilang dari database")
	}
	if old.Status != "expired" {
		t.Fatalf("hold lama: want status expired di DB, got %q", old.Status)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 2 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 2 {
		t.Fatalf("expiry lock-wait: want 2 hold + 2 audit")
	}
	// Availability untuk periode itu: hold baru masih hidup → false.
	code, body := getAvailability(t, env.srv, env.wsA, asset, env.ownerToken,
		availQuery("2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00"))
	if code != http.StatusOK || body["available"] != false {
		t.Fatalf("availability sesudah hold baru: want 200 false, got %d (%v)", code, body)
	}
}

// TestHoldRelease: release hidup → 200 + audit; release ulang
// idempotent 200 tanpa audit kedua; release sesudah revoke/deactivate
// → 200 (tanpa syarat entitlement); release hold yang efektif
// kedaluwarsa → 200 expired tanpa audit release; 404/403 dijaga.
func TestHoldRelease(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-REL-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-REL-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	holdID, _ := seedHold(t, env, draft, asset, "rel-1")

	code, body := releaseHold(t, env.srv, env.wsA, holdID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "released" || body["id"] != holdID {
		t.Fatalf("release: want status released untuk hold %s, got %v", holdID, body)
	}
	if countBusinessAudits(t, env.boot, env.wsA, "hold_released") != 1 {
		t.Fatalf("release: want 1 audit hold_released")
	}
	// Release ulang: 200 catatan yang sama, tanpa audit kedua dan
	// tanpa perubahan updated_at (tidak ada tulis). Bandingkan
	// mentah DB-ke-DB (format teks PG yang sama).
	before, _ := readHold(t, env.boot, env.wsA, holdID)
	code, body = releaseHold(t, env.srv, env.wsA, holdID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release ulang: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "released" {
		t.Fatalf("release ulang: want status released, got %v", body)
	}
	after, _ := readHold(t, env.boot, env.wsA, holdID)
	if after.UpdatedAt != before.UpdatedAt {
		t.Fatalf("release ulang: want updated_at tidak berubah (%s), got %s", before.UpdatedAt, after.UpdatedAt)
	}
	if countBusinessAudits(t, env.boot, env.wsA, "hold_released") != 1 {
		t.Fatalf("release ulang: want tetap 1 audit")
	}
	// Release paralel (dua staf bersamaan): keduanya 200, tepat
	// satu audit — UPDATE kondisional status='held' menyerikan
	// transisi; yang kedua melihat 0 baris dan mengembalikan keadaan
	// saat ini tanpa audit kedua.
	holdPar, _ := seedHold(t, env, draft, asset, "rel-par")
	codes, _ := parallel(t,
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/holds/"+holdPar+"/release", env.ownerToken, nil, nil)
		},
		func() (int, map[string]any, error) {
			return callHoldAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/rental/holds/"+holdPar+"/release", env.mgrToken, nil, nil)
		},
	)
	sort.Ints(codes)
	if !reflect.DeepEqual(codes, []int{http.StatusOK, http.StatusOK}) {
		t.Fatalf("release paralel: want [200 200], got %v", codes)
	}
	if countBusinessAudits(t, env.boot, env.wsA, "hold_released") != 2 {
		t.Fatalf("release paralel: want tepat 2 audit total, got %d", countBusinessAudits(t, env.boot, env.wsA, "hold_released"))
	}
	// Release sesudah revoke → 200 (release bukan aksi rental baru).
	hold2, _ := seedHold(t, env, draft, asset, "rel-2")
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	code, body = releaseHold(t, env.srv, env.wsA, hold2, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release sesudah revoke: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "released" {
		t.Fatalf("release sesudah revoke: want released, got %v", body)
	}
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	// Release sesudah deactivate → 200.
	hold3, _ := seedHold(t, env, draft, asset, "rel-3")
	if code, _ := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate prasyarat: want 200, got %d", code)
	}
	code, body = releaseHold(t, env.srv, env.wsA, hold3, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release sesudah deactivate: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "released" {
		t.Fatalf("release sesudah deactivate: want released, got %v", body)
	}
	activateRental(t, env)
	// Hold yang efektif kedaluwarsa: release → 200 expired, tanpa
	// audit release, dan status expired ter-persist di database.
	hold4, _ := seedHold(t, env, draft, asset, "rel-4")
	expireHoldFixture(t, env.boot, hold4, "-1 hour")
	code, body = releaseHold(t, env.srv, env.wsA, hold4, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("release hold kedaluwarsa: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "expired" {
		t.Fatalf("release hold kedaluwarsa: want status expired, got %v", body)
	}
	stored4, _ := readHold(t, env.boot, env.wsA, hold4)
	if stored4.Status != "expired" {
		t.Fatalf("release hold kedaluwarsa: want status expired ter-persist, got %q", stored4.Status)
	}
	if countBusinessAudits(t, env.boot, env.wsA, "hold_released") != 4 {
		t.Fatalf("release hold kedaluwarsa: want tetap 4 audit release, got %d", countBusinessAudits(t, env.boot, env.wsA, "hold_released"))
	}
	// Hold hilang/lintas-tenant → 404 identik; kasir → 403.
	code, body = releaseHold(t, env.srv, env.wsA, "99999999-8888-7777-6666-555555555555", env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("release hold hilang: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "hold tidak ditemukan")
	code, _ = releaseHold(t, env.srv, env.wsA, holdID, env.cashToken)
	if code != http.StatusForbidden {
		t.Fatalf("release kasir: want 403, got %d", code)
	}
}

// TestHoldHistory: daftar hold terbaca sesudah deactivate/revoke
// (tanpa syarat aktivasi/entitlement); hold baru sesudah itu 403;
// list memetakan status efektif.
func TestHoldHistory(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-HIST-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-HIST-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	holdID, _ := seedHold(t, env, draft, asset, "hist-1")
	// Hold kedua pada aset BERBEDA (item yang sama, draft yang sama,
	// periode yang sama — tidak overlap) lalu dibuat kedaluwarsa lewat
	// fixture: list harus menampilkannya sebagai expired.
	asset2 := seedAsset(t, env, item, "SN-HIST-B")
	hold2, _ := seedHold(t, env, draft, asset2, "hist-2")
	expireHoldFixture(t, env.boot, hold2, "-1 hour")

	if code, _ := deactivateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken); code != http.StatusOK {
		t.Fatalf("deactivate prasyarat: want 200, got %d", code)
	}
	revokeEntitlement(t, env.boot, env.wsA, "rental.basic")
	for name, token := range map[string]string{"owner": env.ownerToken, "kasir": env.cashToken} {
		code, body := listHolds(t, env.srv, env.wsA, token, "")
		if code != http.StatusOK {
			t.Fatalf("list hold %s sesudah deactivate/revoke: want 200, got %d (%v)", name, code, body)
		}
		items, _ := body["items"].([]any)
		if len(items) != 2 {
			t.Fatalf("list hold %s: want 2, got %d (%v)", name, len(items), body)
		}
		statuses := map[string]bool{}
		for _, it := range items {
			m, _ := it.(map[string]any)
			statuses[m["status"].(string)] = true
		}
		if !statuses["held"] || !statuses["expired"] {
			t.Fatalf("list hold %s: want status held + expired (efektif), got %v", name, statuses)
		}
	}
	// Baris hold2 masih mentah 'held' di database (pembacaan tidak
	// memutasi).
	stored2, _ := readHold(t, env.boot, env.wsA, hold2)
	if stored2.Status != "held" {
		t.Fatalf("list tidak boleh memutasi: hold2 mentah want held, got %q", stored2.Status)
	}
	// Hold baru sesudah deactivate/revoke → 403.
	code, _ := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "hist-3", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("hold baru sesudah deactivate/revoke: want 403, got %d", code)
	}
	// Replay kunci lama sesudah deactivate/revoke → 403 (hak saat ini
	// dicek dulu), bukan 200 histori.
	code, _ = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "hist-1", map[string]string{"asset_id": asset})
	if code != http.StatusForbidden {
		t.Fatalf("replay sesudah deactivate/revoke: want 403, got %d", code)
	}
	_ = holdID
}

// TestHoldGuardFaults: fault bacaan guard lewat REVOKE SELECT → 500
// terkendali, tanpa baris/audit parsial; sesudah GRANT kembali,
// create berhasil.
func TestHoldGuardFaults(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-HFAULT-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-HFAULT-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	t.Cleanup(func() {
		for _, table := range []string{"module_installations", "module_entitlements", "rental_contracts", "physical_assets", "catalog_items", "rental_holds"} {
			_, _ = env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`)
		}
	})
	for name, table := range map[string]string{
		"instalasi":   "module_installations",
		"entitlement": "module_entitlements",
		"draft":       "rental_contracts",
		"aset":        "physical_assets",
		"barang":      "catalog_items",
		"hold":        "rental_holds",
	} {
		if _, err := env.boot.Exec(`REVOKE SELECT ON ` + table + ` FROM umnia_business`); err != nil {
			t.Fatalf("revoke SELECT %s: %v", table, err)
		}
		code, resp := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "fault-"+name, map[string]string{"asset_id": asset})
		if code != http.StatusInternalServerError || resp["error"] != "transaksi gagal" {
			t.Fatalf("fault %s: want 500 transaksi gagal, got %d (%v)", name, code, resp)
		}
		if countRows(t, env.boot, "rental_holds", env.wsA) != 0 {
			t.Fatalf("fault %s: want 0 hold", name)
		}
		if _, err := env.boot.Exec(`GRANT SELECT ON ` + table + ` TO umnia_business`); err != nil {
			t.Fatalf("grant kembali SELECT %s: %v", table, err)
		}
	}
	// Sesudah semua fault dipulihkan, create yang sama berhasil 201.
	code, _ := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "fault-ok", map[string]string{"asset_id": asset})
	if code != http.StatusCreated {
		t.Fatalf("hold sesudah pemulihan: want 201, got %d", code)
	}
}

// TestHoldAuditFaultAtomicity: fault trigger audit → 500 tanpa
// baris/audit parsial; bila insert gagal, cleanup lazy-expiry ikut
// rollback (baris basi tetap 'held' di database).
func TestHoldAuditFaultAtomicity(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-AFAULT-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-AFAULT-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")

	// Trigger fault: INSERT audit hold_created selalu gagal.
	_, err := env.boot.Exec(`
		CREATE OR REPLACE FUNCTION app.qa_fail_hold_audit() RETURNS trigger AS $$
		BEGIN
			IF NEW.action = 'hold_created' THEN
				RAISE EXCEPTION 'synthetic audit failure';
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`)
	if err != nil {
		t.Fatalf("buat fungsi fault audit: %v", err)
	}
	_, err = env.boot.Exec(`CREATE TRIGGER qa_fault_hold_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_hold_audit()`)
	if err != nil {
		t.Fatalf("pasang trigger fault audit: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_hold_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_hold_audit()`)
	})
	code, body := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "afault-1", map[string]string{"asset_id": asset})
	if code != http.StatusInternalServerError {
		t.Fatalf("hold dengan fault audit: want 500, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "audit gagal ditulis")
	if countRows(t, env.boot, "rental_holds", env.wsA) != 0 ||
		countBusinessAudits(t, env.boot, env.wsA, "hold_created") != 0 {
		t.Fatalf("fault audit: want 0 hold + 0 audit")
	}
	// Cleanup ikut rollback: siapkan hold basi (kedaluwarsa), lalu
	// create baru dengan fault audit → 500 DAN hold basi tetap 'held'.
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_hold_audit ON audit_events`); err != nil {
		t.Fatalf("lepas trigger sementara: %v", err)
	}
	oldHold, _ := seedHold(t, env, draft, asset, "afault-old")
	expireHoldFixture(t, env.boot, oldHold, "-1 hour")
	if _, err := env.boot.Exec(`CREATE TRIGGER qa_fault_hold_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_hold_audit()`); err != nil {
		t.Fatalf("pasang ulang trigger fault audit: %v", err)
	}
	code, _ = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "afault-2", map[string]string{"asset_id": asset})
	if code != http.StatusInternalServerError {
		t.Fatalf("hold kedua dengan fault audit: want 500, got %d", code)
	}
	stored, ok := readHold(t, env.boot, env.wsA, oldHold)
	if !ok || stored.Status != "held" {
		t.Fatalf("cleanup ikut rollback: hold basi want tetap held, got %v", stored)
	}
	if countRows(t, env.boot, "rental_holds", env.wsA) != 1 {
		t.Fatalf("fault audit kedua: want tetap 1 hold (basi), got %d", countRows(t, env.boot, "rental_holds", env.wsA))
	}
	// Lepas fault: create berhasil, dan hold basi kini expired
	// (lazy expiry ter-persist oleh create yang sukses).
	if _, err := env.boot.Exec(`DROP TRIGGER qa_fault_hold_audit ON audit_events`); err != nil {
		t.Fatalf("lepas trigger fault: %v", err)
	}
	code, _ = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "afault-ok", map[string]string{"asset_id": asset})
	if code != http.StatusCreated {
		t.Fatalf("hold sesudah pemulihan fault: want 201, got %d", code)
	}
	stored, _ = readHold(t, env.boot, env.wsA, oldHold)
	if stored.Status != "expired" {
		t.Fatalf("hold basi sesudah create sukses: want expired, got %q", stored.Status)
	}
}

// TestHoldNewVsReplayArchive: hold BARU sesudah item diarsipkan → 409;
// replay hold lama sesudah arsip → 200 (bacaan histori).
func TestHoldNewVsReplayArchive(t *testing.T) {
	env := setupAsset(t)
	cust, item := seedAssetRefs(t, env, "SKU-ARCH-A")
	activateRental(t, env)
	asset := seedAsset(t, env, item, "SN-ARCH-A")
	draft, _ := seedDraftForHold(t, env, cust, item, "2026-11-10T09:00:00+07:00", "2026-11-12T09:00:00+07:00")
	_, _ = seedHold(t, env, draft, asset, "arch-1")
	archiveItem(t, env.boot, item)
	code, body := createHold(t, env.srv, env.wsA, draft, env.ownerToken, "arch-new", map[string]string{"asset_id": asset})
	if code != http.StatusConflict {
		t.Fatalf("hold baru item archived: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "barang tidak aktif")
	code, body = createHold(t, env.srv, env.wsA, draft, env.ownerToken, "arch-1", map[string]string{"asset_id": asset})
	if code != http.StatusOK {
		t.Fatalf("replay sesudah arsip: want 200, got %d (%v)", code, body)
	}
	if body["status"] != "held" {
		t.Fatalf("replay sesudah arsip: want status held, got %v", body)
	}
}
