package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
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
	if len(modules) != 4 {
		t.Fatalf("modules: want 4, got %d (%v)", len(modules), body)
	}
	// Urutan deterministik menurut identity/version; tiap manifest sama
	// persis (semantik) dengan contoh kanonis; tidak ada field status.
	wantIDs := []string{"finance.advanced", "finance.basic", "rental.basic", "spa.standard"}
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
		if mods, _ := body["modules"].([]any); len(mods) != 4 {
			t.Fatalf("%s GET modules: want 4, got %d", name, len(mods))
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
	if count != 4 {
		t.Fatalf("registry: want 4 baris, got %d", count)
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
	if seen != 4 {
		t.Fatalf("registry: want 4 manifest, got %d", seen)
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
