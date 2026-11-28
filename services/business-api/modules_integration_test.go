package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
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

// Tes integrasi F1-04A terhadap PostgreSQL asli: registry katalog modul
// read-only (migrasi 003) + endpoint GET /workspaces/{id}/modules.
// Memakai helper yang sama dengan identity_integration_test.go
// (testDB, doJSON, registerAndLogin, createWorkspace).

// exampleManifest membaca contoh manifest kanonis dari repo (bukan dari
// database) supaya tes membuktikan seed cocok dengan sumber kanonis.
// Nama file memakai strip (finance-advanced.module.json), sedangkan id
// modul memakai titik (finance.advanced).
func exampleManifest(t *testing.T, id string) map[string]any {
	t.Helper()
	// Tes berjalan dari services/business-api; contoh ada di repo root.
	path := filepath.Join("..", "..", "examples", strings.Replace(id, ".", "-", 1)+".module.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("baca contoh %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode contoh %s: %v", path, err)
	}
	return m
}

// listModules memanggil endpoint katalog dan mengembalikan status + body
// (modul dikembalikan sebagai objek manifest utuh).
func listModules(t *testing.T, srv *httptest.Server, ws, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/modules", token, nil)
}

func TestListModulesCatalog(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	token := registerAndLogin(t, srv, fmt.Sprintf("mod-owner-%d@contoh.id", suffix))
	ws := createWorkspace(t, srv, token, "Usaha Modul")

	code, body := listModules(t, srv, ws, token)
	if code != http.StatusOK {
		t.Fatalf("GET modules: want 200, got %d (%v)", code, body)
	}
	modules, _ := body["modules"].([]any)
	if len(modules) != 5 {
		t.Fatalf("modules: want 5, got %d (%v)", len(modules), body)
	}
	// Urutan deterministik menurut identity/version; tiap manifest sama
	// persis (semantik) dengan contoh kanonis; tidak ada field status.
	wantIDs := []string{"finance.advanced", "finance.basic", "inventory.basic", "rental.basic", "spa.standard"}
	forbidden := []string{"status", "active", "installed", "healthy", "entitled", "available"}
	for i, raw := range modules {
		m, _ := raw.(map[string]any)
		if m == nil {
			t.Fatalf("modules[%d]: bukan objek (%v)", i, raw)
		}
		id, _ := m["id"].(string)
		if id != wantIDs[i] {
			t.Fatalf("modules[%d].id: want %s, got %s", i, wantIDs[i], id)
		}
		if v, _ := m["version"].(string); v != "0.1.0" {
			t.Fatalf("modules[%d].version: want 0.1.0, got %s", i, v)
		}
		for _, key := range forbidden {
			if _, exists := m[key]; exists {
				t.Fatalf("modules[%d] tidak boleh punya field %q (katalog bukan status)", i, key)
			}
		}
		if want := exampleManifest(t, id); !reflect.DeepEqual(m, want) {
			t.Fatalf("modules[%d] (%s) tidak sama dengan contoh kanonis", i, id)
		}
	}
	// Body hanya berisi kunci "modules" (tidak ada status/entitlement).
	if len(body) != 1 {
		t.Fatalf("body: want hanya kunci modules, got %v", body)
	}
}

func TestListModulesPermissions(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("modperm-owner-%d@contoh.id", suffix)
	mgrEmail := fmt.Sprintf("modperm-mgr-%d@contoh.id", suffix)
	cashEmail := fmt.Sprintf("modperm-cash-%d@contoh.id", suffix)
	otherEmail := fmt.Sprintf("modperm-lain-%d@contoh.id", suffix)
	tokenOwner := registerAndLogin(t, srv, ownerEmail)
	tokenMgr := registerAndLogin(t, srv, mgrEmail)
	tokenCash := registerAndLogin(t, srv, cashEmail)
	tokenOther := registerAndLogin(t, srv, otherEmail)
	ws := createWorkspace(t, srv, tokenOwner, "Usaha Modul Perm")
	wsOther := createWorkspace(t, srv, tokenOther, "Usaha Lain")

	// Undang manager + kasir, lalu mereka menerima undangan.
	for _, tc := range []struct{ email, role, token string }{
		{mgrEmail, "manager", tokenMgr},
		{cashEmail, "cashier", tokenCash},
	} {
		code, body := doJSON(t, srv, "POST", "/workspaces/"+ws+"/invites", tokenOwner, map[string]string{
			"email": tc.email, "role": tc.role,
		})
		if code != http.StatusCreated {
			t.Fatalf("invite %s: want 201, got %d (%v)", tc.email, code, body)
		}
		inviteToken, _ := body["token"].(string)
		code, _ = doJSON(t, srv, "POST", "/invites/accept", tc.token, map[string]string{
			"workspace_id": ws, "token": inviteToken,
		})
		if code != http.StatusOK {
			t.Fatalf("accept %s: want 200, got %d", tc.email, code)
		}
	}

	// Owner, manager, dan kasir punya view_workspace: semuanya boleh
	// membaca katalog (tindakan ini tidak memberi kuasa baru).
	for name, tok := range map[string]string{"owner": tokenOwner, "manager": tokenMgr, "cashier": tokenCash} {
		code, body := listModules(t, srv, ws, tok)
		if code != http.StatusOK {
			t.Fatalf("%s GET modules: want 200, got %d (%v)", name, code, body)
		}
		if mods, _ := body["modules"].([]any); len(mods) != 5 {
			t.Fatalf("%s GET modules: want 5, got %d", name, len(mods))
		}
	}
	// Tanpa token: 401. Bukan anggota: 403. Anggota workspace lain
	// lewat route workspace ini: 403. UUID valid tapi tidak ada: 403
	// (tidak membocorkan keberadaan workspace).
	code, _ := listModules(t, srv, ws, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("tanpa token: want 401, got %d", code)
	}
	code, _ = listModules(t, srv, ws, tokenOther)
	if code != http.StatusForbidden {
		t.Fatalf("bukan anggota: want 403, got %d", code)
	}
	code, _ = listModules(t, srv, wsOther, tokenOwner)
	if code != http.StatusForbidden {
		t.Fatalf("owner A lewat route workspace B: want 403, got %d", code)
	}
	unknown, err := uuidNew()
	if err != nil {
		t.Fatalf("uuidNew: %v", err)
	}
	code, _ = listModules(t, srv, unknown, tokenOwner)
	if code != http.StatusForbidden {
		t.Fatalf("UUID tidak ada: want 403, got %d", code)
	}
	// Input malformed: 400 yang terkendali, bukan 403/500 atau bocoran SQL.
	code, body := listModules(t, srv, "bukan-uuid", tokenOwner)
	if code != http.StatusBadRequest {
		t.Fatalf("id malformed: want 400, got %d (%v)", code, body)
	}
	// Membaca katalog tidak memutasi apa pun: jumlah event audit
	// workspace sama sebelum dan sesudah daftar dibaca.
	_, before := doJSON(t, srv, "GET", "/workspaces/"+ws+"/audit", tokenOwner, nil)
	code, _ = listModules(t, srv, ws, tokenOwner)
	if code != http.StatusOK {
		t.Fatalf("baca katalog ulang: want 200, got %d", code)
	}
	_, after := doJSON(t, srv, "GET", "/workspaces/"+ws+"/audit", tokenOwner, nil)
	beforeEvents, _ := before["events"].([]any)
	afterEvents, _ := after["events"].([]any)
	if len(beforeEvents) != len(afterEvents) {
		t.Fatalf("audit berubah setelah baca katalog: %d -> %d", len(beforeEvents), len(afterEvents))
	}
}

// TestListModulesPermissionQueryErrorIsServerError: kesalahan query izin
// (bukan penolakan izin) harus 500, bukan 403. Fault disuntik lewat
// subject INTERNAL yang sengaja tidak valid — fixture tes saja, bukan
// konteks yang dikendalikan pengguna. app.has_permission memakai
// argumen UUID, jadi subject ini gagal di dalam PostgreSQL (bukan mock).
func TestListModulesPermissionQueryErrorIsServerError(t *testing.T) {
	db := testDB(t)
	srv := &server{db: db}
	ws, err := uuidNew()
	if err != nil {
		t.Fatalf("uuidNew: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/workspaces/"+ws+"/modules", nil)
	req.SetPathValue("id", ws)
	ctx := context.WithValue(req.Context(), ctxSubject, "bukan-uuid")
	rec := httptest.NewRecorder()
	srv.handleListModules(rec, req.WithContext(ctx))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("permission query error: want 500, got %d (%s)", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode 500 body: %v", err)
	}
	for _, leaked := range []string{"uuid", "has_permission", "invalid input"} {
		if strings.Contains(body["error"], leaked) {
			t.Fatalf("500 body membocorkan detail SQL: %q", body["error"])
		}
	}
}

// bootstrapDB membuka koneksi pemilik tabel (bootstrap/superuser) untuk
// fixture CHECK yang harus mencapai constraint — role runtime ditolak
// sebelum CHECK (permission denied), jadi koneksi ini satu-satunya cara
// mengamati check_violation di gate integrasi. URL diisi CI
// (BUSINESS_BOOTSTRAP_DATABASE_URL); tanpa fixture ini tes dilewati
// secara jujur (bukan PASS).
func bootstrapDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if url == "" {
		t.Skip("BUSINESS_BOOTSTRAP_DATABASE_URL kosong, tes fixture bootstrap dilewati")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open bootstrap db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping bootstrap db: %v", err)
	}
	return db
}

// CHECK lama yang lemah (menerima JSON null): dipakai fixture untuk
// membuktikan rerun 003 menggantinya pada database yang sudah 003.
const oldWeakRegistryCheck = `CHECK (manifest ? 'id' AND manifest ? 'version'
  AND manifest->>'id' = module_id
  AND manifest->>'version' = version)`

func reapplyMigration003(t *testing.T, db *sql.DB) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("migrations", "003_modules.sql"))
	if err != nil {
		t.Fatalf("baca 003_modules.sql: %v", err)
	}
	if _, err := db.Exec(string(raw)); err != nil {
		t.Fatalf("terapkan ulang 003: %v", err)
	}
}

func tryRegistryInsert(t *testing.T, db *sql.DB, id, version, manifest string) error {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	_, err = tx.Exec(`INSERT INTO module_registry (module_id, version, manifest) VALUES ($1, $2, $3::jsonb)`, id, version, manifest)
	return err
}

func assertCheckViolation(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: seharusnya ditolak check_violation, malah lolos", what)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
		t.Fatalf("%s: want check_violation (23514), got %v", what, err)
	}
}

// TestModuleRegistryCheckRejectsNullIdentity: CHECK null-safe — JSON null,
// kunci hilang, dan mismatch ditolak check_violation; baris valid lolos;
// rerun 003 mengganti CHECK lama sambil mempertahankan data/timestamp
// baris yang sudah ada.
func TestModuleRegistryCheckRejectsNullIdentity(t *testing.T) {
	db := testDB(t)
	boot := bootstrapDB(t)
	// Jaring pengaman: bila tes gagal di tengah, kembalikan constraint
	// baru dengan menerapkan ulang 003 (cleanup berjalan sebelum
	// koneksi bootstrap ditutup, karena didaftarkan sesudahnya).
	t.Cleanup(func() { reapplyMigration003(t, boot) })

	type registryRow struct{ id, version, manifest, registered string }
	snapshot := func() []registryRow {
		rows, err := boot.Query(`SELECT module_id, version, manifest::text, registered_at::text FROM module_registry ORDER BY module_id`)
		if err != nil {
			t.Fatalf("snapshot registry: %v", err)
		}
		defer rows.Close()
		var out []registryRow
		for rows.Next() {
			var r registryRow
			if err := rows.Scan(&r.id, &r.version, &r.manifest, &r.registered); err != nil {
				t.Fatalf("scan snapshot: %v", err)
			}
			out = append(out, r)
		}
		if err := rows.Err(); err != nil {
			t.Fatalf("rows snapshot: %v", err)
		}
		return out
	}
	before := snapshot()
	if len(before) != 5 {
		t.Fatalf("snapshot: want 5 baris, got %d", len(before))
	}

	// 1. Pasang kembali CHECK lama (simulasi DB yang sudah menerapkan
	// 003 sebelum perbaikan) dan buktikan kelemahannya nyata: insert
	// id JSON null lolos pada constraint lama.
	if _, err := boot.Exec(`ALTER TABLE module_registry DROP CONSTRAINT IF EXISTS module_registry_check`); err != nil {
		t.Fatalf("drop CHECK lama: %v", err)
	}
	if _, err := boot.Exec(`ALTER TABLE module_registry ADD CONSTRAINT module_registry_check ` + oldWeakRegistryCheck); err != nil {
		t.Fatalf("pasang CHECK lama: %v", err)
	}
	if err := tryRegistryInsert(t, boot, "qa.null-id", "0.1.0", `{"id":null,"version":"0.1.0"}`); err != nil {
		t.Fatalf("fixture CHECK lama: insert null-id seharusnya lolos pada constraint lama, got %v", err)
	}

	// 2. Rerun 003: mengganti CHECK lama dengan yang null-safe.
	reapplyMigration003(t, boot)

	// 3. Data/timestamp baris yang sudah ada dipertahankan persis.
	after := snapshot()
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("rerun 003 mengubah baris registry: sebelum %v, sesudah %v", before, after)
	}

	// 4. JSON null / kunci hilang / mismatch kini ditolak check_violation.
	cases := []struct{ id, version, manifest string }{
		{"qa.null-id", "0.1.0", `{"id":null,"version":"0.1.0"}`},
		{"qa.null-version", "0.1.0", `{"id":"qa.null-version","version":null}`},
		{"qa.no-id", "0.1.0", `{"version":"0.1.0"}`},
		{"qa.no-version", "0.1.0", `{"id":"qa.no-version"}`},
		{"qa.mismatch-id", "0.1.0", `{"id":"other","version":"0.1.0"}`},
		{"qa.mismatch-version", "0.2.0", `{"id":"qa.mismatch-version","version":"0.1.0"}`},
	}
	for _, c := range cases {
		assertCheckViolation(t, tryRegistryInsert(t, boot, c.id, c.version, c.manifest), c.id)
	}

	// 5. Kontrol positif: baris valid tetap lolos (membuktikan tes
	// mencapai constraint, bukan gagal lebih awal). Rollback supaya
	// tidak ada baris yang tersisa.
	if err := tryRegistryInsert(t, boot, "qa.valid", "0.1.0", `{"id":"qa.valid","version":"0.1.0"}`); err != nil {
		t.Fatalf("insert valid seharusnya lolos: %v", err)
	}

	// 6. Runtime DML tetap ditolak (bukan bukti CHECK, tapi bagian dari
	// bukti bahwa perbaikan tidak melonggarkan grant runtime).
	if _, err := db.Exec(`INSERT INTO module_registry (module_id, version, manifest) VALUES ('qa.rt', '0.1.0', '{}'::jsonb)`); err == nil {
		t.Fatalf("INSERT runtime seharusnya ditolak")
	} else if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("INSERT runtime: want permission denied, got %v", err)
	}
}

// runPsqlFile menjalankan psql ASLI (bukan db.Exec) dengan semantik
// runner skrip: tiap pernyataan autocommit kecuali -1 diminta.
// Mengembalikan exit code dan output gabungan.
func runPsqlFile(t *testing.T, dbURL, file string) (int, string) {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	cmd := exec.Command(psql, dbURL, "-v", "ON_ERROR_STOP=1", "-f", file)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("jalankan psql: %v", err)
		}
	}
	return code, out.String()
}

// TestModuleRegistryCheckReplacementIsAtomic: penggantian CHECK harus
// atomik pada jalur autocommit Linux native (psql TANPA -1). Dengan
// baris legacy invalid yang sudah COMMITTED: file dua pernyataan lama
// gagal DAN menghapus CHECK (baseline); file satu ALTER baru gagal
// tetapi CHECK lama + semua baris tetap ada; rerun valid sesudah baris
// fixture dihapus berhasil dan memasang CHECK baru.
func TestModuleRegistryCheckReplacementIsAtomic(t *testing.T) {
	boot := bootstrapDB(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}

	canonical := func() string {
		var s string
		err := boot.QueryRow(`SELECT coalesce(string_agg(module_id || '|' || manifest::text || '|' || registered_at::text, chr(10) ORDER BY module_id), '') FROM module_registry WHERE module_id <> 'qa.null-legacy'`).Scan(&s)
		if err != nil {
			t.Fatalf("snapshot kanonis: %v", err)
		}
		return s
	}
	checkState := func() (int, string) {
		var n int
		var def string
		err := boot.QueryRow(`SELECT count(*), COALESCE(max(pg_get_constraintdef(oid)), '') FROM pg_constraint WHERE conrelid = 'module_registry'::regclass AND contype = 'c'`).Scan(&n, &def)
		if err != nil {
			t.Fatalf("baca CHECK: %v", err)
		}
		return n, def
	}
	rowCount := func() int {
		var n int
		if err := boot.QueryRow(`SELECT count(*) FROM module_registry`).Scan(&n); err != nil {
			t.Fatalf("hitung baris: %v", err)
		}
		return n
	}

	// Jaring pengaman: hapus baris fixture + terapkan ulang 003.
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_registry WHERE module_id = 'qa.null-legacy'`)
		reapplyMigration003(t, boot)
	})

	before := canonical()
	if rowCount() != 5 {
		t.Fatalf("awal: want 5 baris, got %d", rowCount())
	}

	// 1. Pasang CHECK lama + satu baris legacy invalid yang COMMITTED.
	if _, err := boot.Exec(`ALTER TABLE module_registry DROP CONSTRAINT IF EXISTS module_registry_check`); err != nil {
		t.Fatalf("drop CHECK: %v", err)
	}
	if _, err := boot.Exec(`ALTER TABLE module_registry ADD CONSTRAINT module_registry_check ` + oldWeakRegistryCheck); err != nil {
		t.Fatalf("pasang CHECK lama: %v", err)
	}
	if _, err := boot.Exec(`INSERT INTO module_registry (module_id, version, manifest) VALUES ('qa.null-legacy', '0.1.0', '{"id":null,"version":"0.1.0"}'::jsonb)`); err != nil {
		t.Fatalf("insert baris legacy invalid: %v", err)
	}
	nOld, defOld := checkState()
	if nOld != 1 || rowCount() != 6 {
		t.Fatalf("fixture: want 1 CHECK + 6 baris, got %d CHECK + %d baris", nOld, rowCount())
	}

	// 2. Baseline: file DUA pernyataan (f72eac0) lewat psql TANPA -1 —
	// DROP ter-commit, ADD gagal, CHECK hilang.
	oldTwoStmt := "ALTER TABLE module_registry DROP CONSTRAINT IF EXISTS module_registry_check;\n" +
		"ALTER TABLE module_registry ADD CONSTRAINT module_registry_check CHECK (\n" +
		"  manifest->>'id' IS NOT NULL\n" +
		"    AND manifest->>'version' IS NOT NULL\n" +
		"    AND manifest->>'id' = module_id\n" +
		"    AND manifest->>'version' = version\n" +
		");\n"
	oldFile := filepath.Join(t.TempDir(), "003-old-two-statement.sql")
	if err := os.WriteFile(oldFile, []byte(oldTwoStmt), 0o600); err != nil {
		t.Fatalf("tulis file baseline: %v", err)
	}
	code, out := runPsqlFile(t, bootURL, oldFile)
	if code == 0 {
		t.Fatalf("baseline dua pernyataan: seharusnya gagal, malah exit 0 (%s)", out)
	}
	t.Logf("baseline psql exit %d (diharapkan bukan 0)", code)
	n, _ := checkState()
	if n != 0 {
		t.Fatalf("baseline: CHECK seharusnya hilang (0), got %d", n)
	}
	if rowCount() != 6 {
		t.Fatalf("baseline: baris seharusnya tetap 6, got %d", rowCount())
	}

	// 3. Pulihkan CHECK lama (ADD lama lolos karena baris invalid
	// diizinkan CHECK lama) — titik awal untuk pengujian file baru.
	if _, err := boot.Exec(`ALTER TABLE module_registry ADD CONSTRAINT module_registry_check ` + oldWeakRegistryCheck); err != nil {
		t.Fatalf("pulihkan CHECK lama: %v", err)
	}
	n, def := checkState()
	if n != 1 || def != defOld {
		t.Fatalf("pulihkan CHECK lama: want 1 CHECK dengan definisi lama, got %d (%s)", n, def)
	}

	// 4. File 003 yang diperbaiki lewat psql TANPA -1 — satu ALTER
	// atomik: gagal, tetapi CHECK lama + semua baris tetap ada.
	code, out = runPsqlFile(t, bootURL, filepath.Join("migrations", "003_modules.sql"))
	if code == 0 {
		t.Fatalf("003 baru: seharusnya gagal pada baris invalid, malah exit 0 (%s)", out)
	}
	t.Logf("003 baru psql exit %d (diharapkan bukan 0)", code)
	n, def = checkState()
	if n != 1 {
		t.Fatalf("003 baru: CHECK seharusnya tetap 1, got %d", n)
	}
	if def != defOld {
		t.Fatalf("003 baru: definisi CHECK seharusnya tetap yang lama, got %s", def)
	}
	if rowCount() != 6 {
		t.Fatalf("003 baru: baris seharusnya tetap 6, got %d", rowCount())
	}
	if canonical() != before {
		t.Fatalf("003 baru: baris kanonis berubah")
	}

	// 5. Rerun valid lewat psql TANPA -1 sesudah baris fixture dihapus:
	// exit 0, CHECK baru terpasang, 5 baris kanonis + timestamp utuh.
	if _, err := boot.Exec(`DELETE FROM module_registry WHERE module_id = 'qa.null-legacy'`); err != nil {
		t.Fatalf("hapus baris fixture: %v", err)
	}
	code, out = runPsqlFile(t, bootURL, filepath.Join("migrations", "003_modules.sql"))
	if code != 0 {
		t.Fatalf("rerun valid: want exit 0, got %d (%s)", code, out)
	}
	n, def = checkState()
	if n != 1 {
		t.Fatalf("rerun valid: want 1 CHECK, got %d", n)
	}
	if !strings.Contains(def, "IS NOT NULL") {
		t.Fatalf("rerun valid: CHECK seharusnya yang baru (null-safe), got %s", def)
	}
	if rowCount() != 5 || canonical() != before {
		t.Fatalf("rerun valid: 5 baris kanonis + timestamp harus utuh")
	}
}

// Grant efektif role runtime terhadap registry: SELECT boleh,
// INSERT/UPDATE/DELETE ditolak. Bukan sekadar pengecekan nama role.
func TestModuleRegistryGrants(t *testing.T) {
	db := testDB(t)
	var owner string
	if err := db.QueryRow(
		`SELECT relowner::regrole::text FROM pg_class WHERE relname = 'module_registry'`,
	).Scan(&owner); err != nil {
		t.Fatalf("owner registry: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("registry dimiliki runtime role %q; harus dimiliki bootstrap/migrasi", owner)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM module_registry`).Scan(&count); err != nil {
		t.Fatalf("SELECT registry sebagai runtime: %v", err)
	}
	if count != 5 {
		t.Fatalf("registry: want 5 baris, got %d", count)
	}
	// Seed di database sama dengan contoh kanonis (semantik JSONB).
	rows, err := db.Query(`SELECT module_id, manifest FROM module_registry ORDER BY module_id`)
	if err != nil {
		t.Fatalf("SELECT manifest: %v", err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			t.Fatalf("scan registry: %v", err)
		}
		var got map[string]any
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decode manifest %s: %v", id, err)
		}
		if want := exampleManifest(t, id); !reflect.DeepEqual(got, want) {
			t.Fatalf("manifest %s di DB tidak sama dengan contoh kanonis", id)
		}
		seen++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows registry: %v", err)
	}
	if seen != 5 {
		t.Fatalf("registry: want 5 manifest, got %d", seen)
	}
	for name, q := range map[string]string{
		"INSERT": `INSERT INTO module_registry (module_id, version, manifest) VALUES ('x.y', '0.1.0', '{}'::jsonb)`,
		"UPDATE": `UPDATE module_registry SET manifest = '{}'::jsonb WHERE module_id = 'finance.basic'`,
		"DELETE": `DELETE FROM module_registry WHERE module_id = 'finance.basic'`,
	} {
		if _, err := db.Exec(q); err == nil {
			t.Fatalf("%s registry sebagai runtime seharusnya ditolak", name)
		} else if !strings.Contains(err.Error(), "permission denied") {
			t.Fatalf("%s registry: want permission denied, got %v", name, err)
		}
	}
}
