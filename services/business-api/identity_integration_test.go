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
	"strings"
	"testing"
	"time"
)

// Tes integrasi F1-03 terhadap PostgreSQL asli. Dilewati (skip) bila
// BUSINESS_DATABASE_URL kosong, supaya CI tanpa database tetap hijau;
// dijalankan lokal dengan URL dari .env. Setiap tes memakai workspace
// dan akun baru (email unik per tes) supaya tidak saling mengganggu.

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	url := os.Getenv("BUSINESS_DATABASE_URL")
	if url == "" {
		t.Skip("BUSINESS_DATABASE_URL kosong, tes integrasi dilewati")
	}
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping db: %v", err)
	}
	return db
}

func doJSON(t *testing.T, srv *httptest.Server, method, path, token string, body any) (int, map[string]any) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func registerAndLogin(t *testing.T, srv *httptest.Server, email string) string {
	t.Helper()
	code, _ := doJSON(t, srv, "POST", "/auth/register", "", map[string]string{
		"email": email, "password": "password-kuat-123",
	})
	if code != http.StatusCreated {
		t.Fatalf("register %s: want 201, got %d", email, code)
	}
	code, body := doJSON(t, srv, "POST", "/auth/login", "", map[string]string{
		"email": email, "password": "password-kuat-123",
	})
	if code != http.StatusOK {
		t.Fatalf("login %s: want 200, got %d", email, code)
	}
	token, _ := body["token"].(string)
	if token == "" {
		t.Fatalf("login %s: token kosong", email)
	}
	return token
}

func createWorkspace(t *testing.T, srv *httptest.Server, token, name string) string {
	t.Helper()
	code, body := doJSON(t, srv, "POST", "/workspaces", token, map[string]string{"name": name})
	if code != http.StatusCreated {
		t.Fatalf("create workspace: want 201, got %d (%v)", code, body)
	}
	id, _ := body["id"].(string)
	if id == "" {
		t.Fatalf("create workspace: id kosong (%v)", body)
	}
	return id
}

func TestCrossTenantRejected(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	emailA := fmt.Sprintf("a-%d@contoh.id", suffix)
	emailB := fmt.Sprintf("b-%d@contoh.id", suffix)
	tokenA := registerAndLogin(t, srv, emailA)
	tokenB := registerAndLogin(t, srv, emailB)
	wsA := createWorkspace(t, srv, tokenA, "Usaha A")
	wsB := createWorkspace(t, srv, tokenB, "Usaha B")

	// A tidak boleh membaca workspace B, dan sebaliknya.
	code, _ := doJSON(t, srv, "GET", "/workspaces/"+wsB, tokenA, nil)
	if code != http.StatusForbidden {
		t.Fatalf("A GET workspace B: want 403, got %d", code)
	}
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+wsA, tokenB, nil)
	if code != http.StatusForbidden {
		t.Fatalf("B GET workspace A: want 403, got %d", code)
	}
	// Tanpa token sama sekali: 401.
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+wsA, "", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("tanpa token: want 401, got %d", code)
	}
	// Pemiliknya sendiri boleh.
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+wsA, tokenA, nil)
	if code != http.StatusOK {
		t.Fatalf("A GET workspace sendiri: want 200, got %d", code)
	}
}

func TestInviteAcceptAndSingleUse(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	emailA := fmt.Sprintf("owner-%d@contoh.id", suffix)
	emailB := fmt.Sprintf("staff-%d@contoh.id", suffix)
	tokenA := registerAndLogin(t, srv, emailA)
	tokenB := registerAndLogin(t, srv, emailB)
	ws := createWorkspace(t, srv, tokenA, "Usaha Invite")

	// Owner mengundang B sebagai kasir; token undangan dikembalikan sekali.
	code, body := doJSON(t, srv, "POST", "/workspaces/"+ws+"/invites", tokenA, map[string]string{
		"email": emailB, "role": "cashier",
	})
	if code != http.StatusCreated {
		t.Fatalf("create invite: want 201, got %d (%v)", code, body)
	}
	inviteToken, _ := body["token"].(string)
	if inviteToken == "" {
		t.Fatalf("invite token kosong (%v)", body)
	}
	// Sebelum menerima, B masih ditolak.
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+ws, tokenB, nil)
	if code != http.StatusForbidden {
		t.Fatalf("B sebelum accept: want 403, got %d", code)
	}
	// B menerima undangan.
	code, body = doJSON(t, srv, "POST", "/invites/accept", tokenB, map[string]string{
		"workspace_id": ws, "token": inviteToken,
	})
	if code != http.StatusOK {
		t.Fatalf("accept invite: want 200, got %d (%v)", code, body)
	}
	// Sesudah menerima, B boleh membaca workspace.
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+ws, tokenB, nil)
	if code != http.StatusOK {
		t.Fatalf("B sesudah accept: want 200, got %d", code)
	}
	// Terima kedua dengan token yang sama harus ditolak (sekali pakai).
	code, _ = doJSON(t, srv, "POST", "/invites/accept", tokenB, map[string]string{
		"workspace_id": ws, "token": inviteToken,
	})
	if code != http.StatusConflict {
		t.Fatalf("accept kedua: want 409, got %d", code)
	}
	// Kasir tidak boleh mengundang orang lain.
	code, _ = doJSON(t, srv, "POST", "/workspaces/"+ws+"/invites", tokenB, map[string]string{
		"email": fmt.Sprintf("lain-%d@contoh.id", suffix), "role": "cashier",
	})
	if code != http.StatusForbidden {
		t.Fatalf("kasir mengundang: want 403, got %d", code)
	}
	// Audit workspace memuat peristiwa-peristiwa di atas.
	code, body = doJSON(t, srv, "GET", "/workspaces/"+ws+"/audit", tokenA, nil)
	if code != http.StatusOK {
		t.Fatalf("GET audit: want 200, got %d", code)
	}
	events, _ := body["events"].([]any)
	if len(events) < 3 {
		t.Fatalf("audit: want >=3 events, got %d (%v)", len(events), body)
	}
}

// Koneksi tes harus memakai role runtime (bukan superuser): RLS hanya
// berlaku untuk role itu, dan superuser melewati RLS apa pun flag-nya.
func TestRuntimeRoleIsNotSuperuser(t *testing.T) {
	db := testDB(t)
	var user string
	var super, bypass bool
	err := db.QueryRow(
		`SELECT current_user, rolsuper, rolbypassrls FROM pg_roles WHERE rolname = current_user`,
	).Scan(&user, &super, &bypass)
	if err != nil {
		t.Fatalf("role lookup: %v", err)
	}
	if super || bypass {
		t.Fatalf("role runtime %q harus NOSUPERUSER/NOBYPASSRLS, got super=%v bypass=%v", user, super, bypass)
	}
}

func TestInviteExpiredRejected(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	emailA := fmt.Sprintf("exp-owner-%d@contoh.id", suffix)
	emailB := fmt.Sprintf("exp-staff-%d@contoh.id", suffix)
	tokenA := registerAndLogin(t, srv, emailA)
	tokenB := registerAndLogin(t, srv, emailB)
	ws := createWorkspace(t, srv, tokenA, "Usaha Expired")
	code, body := doJSON(t, srv, "POST", "/workspaces/"+ws+"/invites", tokenA, map[string]string{
		"email": emailB, "role": "cashier",
	})
	if code != http.StatusCreated {
		t.Fatalf("create invite: want 201, got %d (%v)", code, body)
	}
	inviteToken, _ := body["token"].(string)
	// Kedaluwarsakan undangan lewat transaksi tenant owner (RLS
	// mengizinkan UPDATE ini karena owner punya izin invite_members).
	var subjectA string
	if err := db.QueryRow(`SELECT id FROM app.subject_by_email($1)`, emailA).Scan(&subjectA); err != nil {
		t.Fatalf("subject_by_email: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_workspace_id', $1, true)`, ws); err != nil {
		tx.Rollback()
		t.Fatalf("set workspace: %v", err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_subject_id', $1, true)`, subjectA); err != nil {
		tx.Rollback()
		t.Fatalf("set subject: %v", err)
	}
	if _, err := tx.Exec(`UPDATE staff_invites SET expires_at = now() - interval '1 hour' WHERE workspace_id = $1`, ws); err != nil {
		tx.Rollback()
		t.Fatalf("expire invite: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	code, _ = doJSON(t, srv, "POST", "/invites/accept", tokenB, map[string]string{
		"workspace_id": ws, "token": inviteToken,
	})
	if code != http.StatusConflict {
		t.Fatalf("accept expired: want 409, got %d", code)
	}
}

func TestCashierCannotReadAuditOrMembers(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	emailA := fmt.Sprintf("perm-owner-%d@contoh.id", suffix)
	emailB := fmt.Sprintf("perm-staff-%d@contoh.id", suffix)
	tokenA := registerAndLogin(t, srv, emailA)
	tokenB := registerAndLogin(t, srv, emailB)
	ws := createWorkspace(t, srv, tokenA, "Usaha Perm")
	code, body := doJSON(t, srv, "POST", "/workspaces/"+ws+"/invites", tokenA, map[string]string{
		"email": emailB, "role": "cashier",
	})
	if code != http.StatusCreated {
		t.Fatalf("create invite: want 201, got %d (%v)", code, body)
	}
	inviteToken, _ := body["token"].(string)
	code, _ = doJSON(t, srv, "POST", "/invites/accept", tokenB, map[string]string{
		"workspace_id": ws, "token": inviteToken,
	})
	if code != http.StatusOK {
		t.Fatalf("accept: want 200, got %d", code)
	}
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+ws+"/audit", tokenB, nil)
	if code != http.StatusForbidden {
		t.Fatalf("kasir GET audit: want 403, got %d", code)
	}
	code, _ = doJSON(t, srv, "GET", "/workspaces/"+ws+"/members", tokenB, nil)
	if code != http.StatusForbidden {
		t.Fatalf("kasir GET members: want 403, got %d", code)
	}
}

// Readiness harus 200 pada database yang sudah dimigrasi, dan "error"
// pada database kosong tanpa skema (ping saja tidak cukup).
func TestReadyzRequiresMigratedSchema(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	code, body := doJSON(t, srv, "GET", "/readyz", "", nil)
	if code != http.StatusOK {
		t.Fatalf("readyz pada DB termigrasi: want 200, got %d (%v)", code, body)
	}
	// Database kosong: ganti nama database di URL tes menjadi umnia_empty
	// (disediakan setup CI/lokal); bila tidak tersedia, lewati tes negatif
	// ini saja — tes integrasi utama tidak dilewati.
	rawURL := os.Getenv("BUSINESS_DATABASE_URL")
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	u.Path = "/umnia_empty"
	emptyDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_empty: %v", err)
	}
	defer emptyDB.Close()
	if err := emptyDB.Ping(); err != nil {
		t.Skipf("umnia_empty tidak tersedia: %v", err)
	}
	if got := checkDatabase(emptyDB); got != "error" {
		t.Fatalf("checkDatabase pada DB kosong: want error, got %q", got)
	}
	// Database yang berhenti di 002 (tanpa registry 003) juga belum
	// siap: readiness wajib memeriksa kedua versi, bukan hanya 002.
	// umnia_partial disediakan setup CI/lokal (001+002 saja).
	u.Path = "/umnia_partial"
	partialDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_partial: %v", err)
	}
	defer partialDB.Close()
	if err := partialDB.Ping(); err != nil {
		t.Skipf("umnia_partial tidak tersedia: %v", err)
	}
	if got := checkDatabase(partialDB); got != "error" {
		t.Fatalf("checkDatabase pada DB 002-saja: want error, got %q", got)
	}
	// Database yang berhenti di 003 (tanpa penyimpanan instalasi 004)
	// juga belum siap (F1-04B-B1). umnia_003only disediakan setup
	// CI/lokal (001+002+003 saja).
	u.Path = "/umnia_003only"
	only003DB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_003only: %v", err)
	}
	defer only003DB.Close()
	if err := only003DB.Ping(); err != nil {
		t.Skipf("umnia_003only tidak tersedia: %v", err)
	}
	if got := checkDatabase(only003DB); got != "error" {
		t.Fatalf("checkDatabase pada DB 003-saja: want error, got %q", got)
	}
	// Database yang berhenti di 004 (tanpa master data core 005) juga
	// belum siap (F2-01-A). umnia_004only disediakan setup CI/lokal
	// (001+002+003+004 saja).
	u.Path = "/umnia_004only"
	only004DB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_004only: %v", err)
	}
	defer only004DB.Close()
	if err := only004DB.Ping(); err != nil {
		t.Skipf("umnia_004only tidak tersedia: %v", err)
	}
	if got := checkDatabase(only004DB); got != "error" {
		t.Fatalf("checkDatabase pada DB 004-saja: want error, got %q", got)
	}
	// Database yang berhenti di 005 (tanpa kontrak rental draft 006)
	// juga belum siap (F2-01-B). umnia_005only disediakan setup
	// CI/lokal (001+002+003+004+005 saja).
	u.Path = "/umnia_005only"
	only005DB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_005only: %v", err)
	}
	defer only005DB.Close()
	if err := only005DB.Ping(); err != nil {
		t.Skipf("umnia_005only tidak tersedia: %v", err)
	}
	if got := checkDatabase(only005DB); got != "error" {
		t.Fatalf("checkDatabase pada DB 005-saja: want error, got %q", got)
	}
	// Database dengan keenam marker + satu marker tambahan yang tidak
	// dikenal tetap siap (F2-01-B). umnia_extra disediakan setup
	// CI/lokal (001-006 + marker '999_future').
	u.Path = "/umnia_extra"
	extraDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Skipf("tidak bisa membuka umnia_extra: %v", err)
	}
	defer extraDB.Close()
	if err := extraDB.Ping(); err != nil {
		t.Skipf("umnia_extra tidak tersedia: %v", err)
	}
	if got := checkDatabase(extraDB); got != "ok" {
		t.Fatalf("checkDatabase pada DB 6 marker + ekstra: want ok, got %q", got)
	}
}

func TestRLSBlocksDirectCrossTenantRead(t *testing.T) {
	db := testDB(t)
	srv := httptest.NewServer(newMux(db))
	defer srv.Close()
	suffix := time.Now().UnixNano()
	tokenA := registerAndLogin(t, srv, fmt.Sprintf("rls-a-%d@contoh.id", suffix))
	tokenB := registerAndLogin(t, srv, fmt.Sprintf("rls-b-%d@contoh.id", suffix))
	wsA := createWorkspace(t, srv, tokenA, "Usaha RLS A")
	wsB := createWorkspace(t, srv, tokenB, "Usaha RLS B")

	// Bukti RLS langsung: koneksi mentah tanpa SET app.current_* tidak
	// boleh melihat baris workspace mana pun.
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM workspaces`).Scan(&count); err != nil {
		t.Fatalf("raw count workspaces: %v", err)
	}
	if count != 0 {
		t.Fatalf("RLS: koneksi tanpa konteks seharusnya melihat 0 workspace, got %d", count)
	}
	// Dengan konteks workspace A + subjek A, hanya workspace A terlihat.
	// Subjek diambil lewat fungsi SECURITY DEFINER (bypass RLS, seperti login).
	var subjectA string
	if err := db.QueryRow(`SELECT id FROM app.subject_by_email($1)`, fmt.Sprintf("rls-a-%d@contoh.id", suffix)).Scan(&subjectA); err != nil {
		t.Fatalf("subject_by_email: %v", err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT set_config('app.current_workspace_id', $1, true)`, wsA); err != nil {
		t.Fatalf("set workspace: %v", err)
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_subject_id', $1, true)`, subjectA); err != nil {
		t.Fatalf("set subject: %v", err)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM workspaces WHERE id = $1`, wsB).Scan(&count); err != nil {
		t.Fatalf("RLS count wsB: %v", err)
	}
	if count != 0 {
		t.Fatalf("RLS: subjek A dengan konteks A seharusnya tidak melihat workspace B, got %d", count)
	}
	if err := tx.QueryRow(`SELECT count(*) FROM workspaces WHERE id = $1`, wsA).Scan(&count); err != nil {
		t.Fatalf("RLS count wsA: %v", err)
	}
	if count != 1 {
		t.Fatalf("RLS: subjek A dengan konteks A seharusnya melihat workspace A, got %d", count)
	}
	// Audit append-only: UPDATE harus gagal oleh trigger.
	if _, err := tx.Exec(`UPDATE audit_events SET action = 'tampered' WHERE workspace_id = $1`, wsA); err == nil {
		t.Fatalf("UPDATE audit_events seharusnya ditolak trigger append-only")
	} else if !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("UPDATE audit_events: want append-only error, got %v", err)
	}
}
