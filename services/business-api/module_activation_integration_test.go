package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Tes integrasi handler state modul F1-04B-B3 (aktivasi/deaktivasi/baca
// state). Entitlement di sini HANYA fixture sintetis terisolasi lewat
// bootstrap (bukan grant nyata); tidak ada otoritas pemberian
// entitlement, billing, guard, atau endpoint bisnis yang diuji.

// Manifest sintetis untuk kasus resolver yang tidak diwakili empat
// contoh kanonis. Tidak ada perubahan pada manifest/skema kanonis.
const qaProviderManifest = `{"schema_version":"0.4","id":"qa.provider","version":"0.1.0","provides":["qa.cap.read"],"requires":[],"conflicts":[],"exclusive_group":null}`
const qaProvider2Manifest = `{"schema_version":"0.4","id":"qa.provider2","version":"0.1.0","provides":["qa.cap.read"],"requires":[],"conflicts":[],"exclusive_group":null}`
const qaConsumerManifest = `{"schema_version":"0.4","id":"qa.consumer","version":"0.1.0","provides":[],"requires":[{"capability":"qa.cap.read"}],"conflicts":[],"exclusive_group":null}`
const qaOptManifest = `{"schema_version":"0.4","id":"qa.opt","version":"0.1.0","provides":[],"requires":[{"capability":"catalog.item.read","optional":true}],"conflicts":[],"exclusive_group":null}`
const qaAlphaManifest = `{"schema_version":"0.4","id":"qa.alpha","version":"0.1.0","provides":[],"requires":[],"conflicts":["qa.beta"],"exclusive_group":null}`
const qaBetaManifest = `{"schema_version":"0.4","id":"qa.beta","version":"0.1.0","provides":[],"requires":[],"conflicts":[],"exclusive_group":null}`
const qaGroup1Manifest = `{"schema_version":"0.4","id":"qa.group1","version":"0.1.0","provides":[],"requires":[],"conflicts":[],"exclusive_group":"qa-edition"}`
const qaGroup2Manifest = `{"schema_version":"0.4","id":"qa.group2","version":"0.1.0","provides":[],"requires":[],"conflicts":[],"exclusive_group":"qa-edition"}`
const qaFaultManifest = `{"schema_version":"0.4","id":"qa.fault","version":"0.1.0","provides":[],"requires":[],"conflicts":[],"exclusive_group":null}`

type activationEnv struct {
	srv        *httptest.Server
	db, boot   *sql.DB
	wsA, wsB   string
	ownerToken string
	mgrToken   string
	cashToken  string
	otherToken string
	ownerID    string
	mgrID      string
	cashID     string
	otherID    string
}

// setupActivation membuat server + owner/manager/kasir/lain + dua
// workspace (wsA milik owner, wsB milik other; manager/kasir diundang ke
// wsA), mendaftarkan modul sintetis, dan membersihkan semua baris
// fixture (instalasi → entitlement → registry sintetis, urutan FK-aman).
func setupActivation(t *testing.T, synthetic [][3]string) *activationEnv {
	t.Helper()
	db := testDB(t)
	boot := bootstrapDB(t)
	srv := httptest.NewServer(newMux(db))
	t.Cleanup(srv.Close)
	suffix := time.Now().UnixNano()
	ownerEmail := fmt.Sprintf("b3-owner-%d@contoh.id", suffix)
	mgrEmail := fmt.Sprintf("b3-mgr-%d@contoh.id", suffix)
	cashEmail := fmt.Sprintf("b3-cash-%d@contoh.id", suffix)
	otherEmail := fmt.Sprintf("b3-lain-%d@contoh.id", suffix)
	ownerToken := registerAndLogin(t, srv, ownerEmail)
	mgrToken := registerAndLogin(t, srv, mgrEmail)
	cashToken := registerAndLogin(t, srv, cashEmail)
	otherToken := registerAndLogin(t, srv, otherEmail)
	wsA := createWorkspace(t, srv, ownerToken, "Usaha B3 A")
	wsB := createWorkspace(t, srv, otherToken, "Usaha B3 B")
	for _, tc := range []struct{ email, role, token string }{
		{mgrEmail, "manager", mgrToken},
		{cashEmail, "cashier", cashToken},
	} {
		code, body := doJSON(t, srv, "POST", "/workspaces/"+wsA+"/invites", ownerToken, map[string]string{"email": tc.email, "role": tc.role})
		if code != http.StatusCreated {
			t.Fatalf("invite %s: want 201, got %d (%v)", tc.email, code, body)
		}
		inviteToken, _ := body["token"].(string)
		code, _ = doJSON(t, srv, "POST", "/invites/accept", tc.token, map[string]string{"workspace_id": wsA, "token": inviteToken})
		if code != http.StatusOK {
			t.Fatalf("accept %s: want 200, got %d", tc.email, code)
		}
	}
	env := &activationEnv{
		srv: srv, db: db, boot: boot, wsA: wsA, wsB: wsB,
		ownerToken: ownerToken, mgrToken: mgrToken, cashToken: cashToken, otherToken: otherToken,
		ownerID: subjectID(t, db, ownerEmail), mgrID: subjectID(t, db, mgrEmail),
		cashID: subjectID(t, db, cashEmail), otherID: subjectID(t, db, otherEmail),
	}
	for _, s := range synthetic {
		registerSyntheticModule(t, boot, s[0], s[1], s[2])
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_installations WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, wsA, wsB)
		for _, s := range synthetic {
			_, _ = boot.Exec(`DELETE FROM module_registry WHERE module_id = $1 AND version = $2`, s[0], s[1])
		}
	})
	return env
}

// grantEntitlement menyisipkan entitlement sintetis lewat bootstrap
// dengan mengunci baris workspace dulu (urutan yang sama seperti handler).
func grantEntitlement(t *testing.T, boot *sql.DB, ws, moduleID, version string) {
	t.Helper()
	tx, err := boot.Begin()
	if err != nil {
		t.Fatalf("grant begin: %v", err)
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, ws).Scan(&id); err != nil {
		t.Fatalf("grant lock %s: %v", ws, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source, reason)
		 VALUES ($1, $2, $3, 'operator:fixture', 'fixture', 'tes B3') ON CONFLICT (workspace_id, module_id) DO NOTHING`,
		ws, moduleID, version); err != nil {
		t.Fatalf("grant %s %s: %v", moduleID, version, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("grant commit: %v", err)
	}
}

// revokeEntitlement menghapus entitlement (pencabutan sintetis) dengan
// urutan kunci workspace yang sama.
func revokeEntitlement(t *testing.T, boot *sql.DB, ws, moduleID string) {
	t.Helper()
	tx, err := boot.Begin()
	if err != nil {
		t.Fatalf("revoke begin: %v", err)
	}
	defer tx.Rollback()
	var id string
	if err := tx.QueryRow(`SELECT id FROM workspaces WHERE id = $1 FOR UPDATE`, ws).Scan(&id); err != nil {
		t.Fatalf("revoke lock %s: %v", ws, err)
	}
	if _, err := tx.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1 AND module_id = $2`, ws, moduleID); err != nil {
		t.Fatalf("revoke %s: %v", moduleID, err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("revoke commit: %v", err)
	}
}

func registerSyntheticModule(t *testing.T, boot *sql.DB, id, version, manifest string) {
	t.Helper()
	if _, err := boot.Exec(`INSERT INTO module_registry (module_id, version, manifest) VALUES ($1, $2, $3::jsonb)`, id, version, manifest); err != nil {
		t.Fatalf("register synthetic %s %s: %v", id, version, err)
	}
}

// financeBasic020 membangun manifest finance.basic versi 0.2.0 dari
// contoh kanonis (untuk uji pin versi yang valid dan terdaftar).
func financeBasic020(t *testing.T) string {
	t.Helper()
	m := exampleManifest(t, "finance.basic")
	m["version"] = "0.2.0"
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal 0.2.0: %v", err)
	}
	return string(raw)
}

func activateModule(t *testing.T, srv *httptest.Server, ws, moduleID, token, version string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/modules/"+moduleID+"/activate", token, map[string]string{"version": version})
}

func deactivateModule(t *testing.T, srv *httptest.Server, ws, moduleID, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "POST", "/workspaces/"+ws+"/modules/"+moduleID+"/deactivate", token, nil)
}

func getModuleState(t *testing.T, srv *httptest.Server, ws, moduleID, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/modules/"+moduleID, token, nil)
}

func postRaw(t *testing.T, srv *httptest.Server, path, token, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, strings.NewReader(raw))
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

// callModuleAPI adalah varian doJSON yang mengembalikan error (bukan
// t.Fatalf) supaya aman dipanggil dari goroutine pada tes konkurensi.
func callModuleAPI(srv *httptest.Server, method, path, token string, body any) (int, map[string]any, error) {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out, nil
}

type installSnapshot struct {
	Version, Status, ActivatedAt, DeactivatedAt, ActivatedBy, UpdatedAt string
}

func readInstallation(t *testing.T, boot *sql.DB, ws, moduleID string) (installSnapshot, bool) {
	t.Helper()
	var s installSnapshot
	err := boot.QueryRow(
		`SELECT version, status, COALESCE(activated_at::text,''), COALESCE(deactivated_at::text,''),
			COALESCE(activated_by::text,''), COALESCE(updated_at::text,'')
		 FROM module_installations WHERE workspace_id = $1 AND module_id = $2`,
		ws, moduleID).Scan(&s.Version, &s.Status, &s.ActivatedAt, &s.DeactivatedAt, &s.ActivatedBy, &s.UpdatedAt)
	if err == sql.ErrNoRows {
		return installSnapshot{}, false
	}
	if err != nil {
		t.Fatalf("read installation %s: %v", moduleID, err)
	}
	return s, true
}

func countInstallations(t *testing.T, boot *sql.DB, ws string, status string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM module_installations WHERE workspace_id = $1 AND status = $2`, ws, status).Scan(&n); err != nil {
		t.Fatalf("count installations: %v", err)
	}
	return n
}

type moduleAudit struct {
	Action     string
	EntityType string
	EntityID   *string
	Details    string
}

func readModuleAudits(t *testing.T, boot *sql.DB, ws string) []moduleAudit {
	t.Helper()
	rows, err := boot.Query(
		`SELECT action, entity_type, entity_id::text, COALESCE(details::text,'') FROM audit_events
		 WHERE workspace_id = $1 AND action LIKE 'module_%' ORDER BY created_at`, ws)
	if err != nil {
		t.Fatalf("read audits: %v", err)
	}
	defer rows.Close()
	var out []moduleAudit
	for rows.Next() {
		var a moduleAudit
		var entityID sql.NullString
		if err := rows.Scan(&a.Action, &a.EntityType, &entityID, &a.Details); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		if entityID.Valid {
			a.EntityID = &entityID.String
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows audit: %v", err)
	}
	return out
}

func auditDetails(t *testing.T, raw string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		t.Fatalf("decode audit details %q: %v", raw, err)
	}
	return m
}

func assertState(t *testing.T, body map[string]any, moduleID string, version any, status string) {
	t.Helper()
	if body["module_id"] != moduleID {
		t.Fatalf("state module_id: want %q, got %v", moduleID, body["module_id"])
	}
	if body["version"] != version {
		t.Fatalf("state version: want %v, got %v", version, body["version"])
	}
	if body["status"] != status {
		t.Fatalf("state status: want %q, got %v", status, body["status"])
	}
}

func assertTimestamp(t *testing.T, body map[string]any, field string, wantSet bool) {
	t.Helper()
	v := body[field]
	if wantSet {
		s, _ := v.(string)
		if s == "" {
			t.Fatalf("state %s: want timestamp, got %v", field, v)
		}
	} else if v != nil {
		t.Fatalf("state %s: want null, got %v", field, v)
	}
}

func assertFlatError(t *testing.T, body map[string]any, want string) {
	t.Helper()
	msg, _ := body["error"].(string)
	if msg == "" {
		t.Fatalf("error envelope kosong (%v)", body)
	}
	if want != "" && !strings.Contains(msg, want) {
		t.Fatalf("error: want mengandung %q, got %q", want, msg)
	}
}

func assertModuleAudit(t *testing.T, a moduleAudit, action, moduleID, version string, oldStatus any, newStatus string) {
	t.Helper()
	if a.Action != action {
		t.Fatalf("audit action: want %q, got %q", action, a.Action)
	}
	if a.EntityType != "module_installation" {
		t.Fatalf("audit entity_type: want module_installation, got %q", a.EntityType)
	}
	if a.EntityID != nil {
		t.Fatalf("audit entity_id: want NULL, got %q", *a.EntityID)
	}
	d := auditDetails(t, a.Details)
	if d["module_id"] != moduleID || d["version"] != version || d["old_status"] != oldStatus || d["new_status"] != newStatus {
		t.Fatalf("audit details: want {%s %s %v %s}, got %v", moduleID, version, oldStatus, newStatus, d)
	}
}

func TestModuleActivationHappyPathAndPermissions(t *testing.T) {
	env := setupActivation(t, nil)
	// GET modul yang belum pernah diinstal: 200 inactive, tanpa baris.
	code, body := getModuleState(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET never-installed: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", nil, "inactive")
	assertTimestamp(t, body, "activated_at", false)
	assertTimestamp(t, body, "deactivated_at", false)
	if _, found := readInstallation(t, env.boot, env.wsA, "finance.basic"); found {
		t.Fatalf("GET never-installed membuat baris instalasi")
	}
	// Tanpa entitlement: 403 default-deny.
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("activate tanpa entitlement: want 403, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "hak")
	// Dengan entitlement sintetis: 200 + satu baris + satu audit benar.
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	assertTimestamp(t, body, "activated_at", true)
	assertTimestamp(t, body, "deactivated_at", false)
	row, found := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if !found {
		t.Fatalf("activate tidak membuat baris instalasi")
	}
	if row.Status != "active" || row.Version != "0.1.0" || row.ActivatedBy != env.ownerID || row.ActivatedAt == "" || row.DeactivatedAt != "" {
		t.Fatalf("baris instalasi salah: %+v", row)
	}
	audits := readModuleAudits(t, env.boot, env.wsA)
	if len(audits) != 1 {
		t.Fatalf("audit: want 1, got %d (%v)", len(audits), audits)
	}
	assertModuleAudit(t, audits[0], "module_activated", "finance.basic", "0.1.0", nil, "active")
	// State boleh dibaca anggota (owner/manager/kasir), bukan non-anggota.
	for _, tc := range []struct {
		name, token string
		want        int
	}{
		{"owner", env.ownerToken, 200}, {"manager", env.mgrToken, 200}, {"cashier", env.cashToken, 200}, {"nonmember", env.otherToken, 403},
	} {
		code, body := getModuleState(t, env.srv, env.wsA, "finance.basic", tc.token)
		if code != tc.want {
			t.Fatalf("GET %s: want %d, got %d (%v)", tc.name, tc.want, code, body)
		}
		if tc.want == 200 {
			assertState(t, body, "finance.basic", "0.1.0", "active")
		}
	}
	// Tulis hanya owner: manager/kasir/non-anggota/lintas-tenant 403.
	for _, tc := range []struct{ name, token, ws string }{
		{"manager", env.mgrToken, env.wsA},
		{"cashier", env.cashToken, env.wsA},
		{"nonmember", env.otherToken, env.wsA},
		{"cross-tenant", env.ownerToken, env.wsB},
	} {
		code, body := activateModule(t, env.srv, tc.ws, "finance.basic", tc.token, "0.1.0")
		if code != http.StatusForbidden {
			t.Fatalf("activate %s: want 403, got %d (%v)", tc.name, code, body)
		}
	}
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", "", "0.1.0")
	if code != http.StatusUnauthorized {
		t.Fatalf("activate tanpa token: want 401, got %d (%v)", code, body)
	}
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.mgrToken)
	if code != http.StatusForbidden {
		t.Fatalf("deactivate manager: want 403, got %d (%v)", code, body)
	}
	// Workspace lain tidak tercampur: finance.basic di wsB tetap inactive.
	code, body = getModuleState(t, env.srv, env.wsB, "finance.basic", env.otherToken)
	if code != http.StatusOK {
		t.Fatalf("GET wsB: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", nil, "inactive")
}

func TestModuleActivationValidation(t *testing.T) {
	env := setupActivation(t, nil)
	path := "/workspaces/" + env.wsA + "/modules/finance.basic/activate"
	for _, tc := range []struct{ name, raw string }{
		{"malformed", `{`},
		{"array", `["0.1.0"]`},
		{"missing-version", `{}`},
		{"empty-version", `{"version":""}`},
		{"blank-version", `{"version":"   "}`},
		{"numeric-version", `{"version":123}`},
	} {
		code, body := postRaw(t, env.srv, path, env.ownerToken, tc.raw)
		if code != http.StatusBadRequest {
			t.Fatalf("body %s: want 400, got %d (%v)", tc.name, code, body)
		}
		assertFlatError(t, body, "versi")
	}
	// Modul tak dikenal 404; versi tak terdaftar 404.
	code, body := activateModule(t, env.srv, env.wsA, "tidak.ada", env.ownerToken, "0.1.0")
	if code != http.StatusNotFound {
		t.Fatalf("activate modul tak dikenal: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "tidak dikenal")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "9.9.9")
	if code != http.StatusNotFound {
		t.Fatalf("activate versi tak terdaftar: want 404, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "tidak terdaftar")
	code, body = getModuleState(t, env.srv, env.wsA, "tidak.ada", env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("GET modul tak dikenal: want 404, got %d (%v)", code, body)
	}
	code, body = deactivateModule(t, env.srv, env.wsA, "tidak.ada", env.ownerToken)
	if code != http.StatusNotFound {
		t.Fatalf("deactivate modul tak dikenal: want 404, got %d (%v)", code, body)
	}
	// ID workspace malformed 400 (sebelum otorisasi DB).
	code, body = getModuleState(t, env.srv, "bukan-uuid", "finance.basic", env.ownerToken)
	if code != http.StatusBadRequest {
		t.Fatalf("GET ws malformed: want 400, got %d (%v)", code, body)
	}
	code, body = activateModule(t, env.srv, "bukan-uuid", "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusBadRequest {
		t.Fatalf("activate ws malformed: want 400, got %d (%v)", code, body)
	}
	// Otorisasi mendahului 404: non-anggota + modul tak dikenal tetap 403.
	code, body = activateModule(t, env.srv, env.wsA, "tidak.ada", env.otherToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("activate non-anggota modul tak dikenal: want 403, got %d (%v)", code, body)
	}
}

func TestModuleActivationPermissionQueryErrorIsServerError(t *testing.T) {
	db := testDB(t)
	srv := &server{db: db}
	ws, err := uuidNew()
	if err != nil {
		t.Fatalf("uuidNew: %v", err)
	}
	ctx := context.WithValue(context.Background(), ctxSubject, "bukan-uuid")
	leaks := []string{"uuid", "has_permission", "invalid input"}
	activateReq := httptest.NewRequest(http.MethodPost, "/workspaces/"+ws+"/modules/finance.basic/activate", strings.NewReader(`{"version":"0.1.0"}`))
	activateReq.SetPathValue("id", ws)
	activateReq.SetPathValue("module_id", "finance.basic")
	deactivateReq := httptest.NewRequest(http.MethodPost, "/workspaces/"+ws+"/modules/finance.basic/deactivate", nil)
	deactivateReq.SetPathValue("id", ws)
	deactivateReq.SetPathValue("module_id", "finance.basic")
	stateReq := httptest.NewRequest(http.MethodGet, "/workspaces/"+ws+"/modules/finance.basic", nil)
	stateReq.SetPathValue("id", ws)
	stateReq.SetPathValue("module_id", "finance.basic")
	for _, tc := range []struct {
		name string
		fn   func(http.ResponseWriter, *http.Request)
		req  *http.Request
	}{
		{"activate", srv.handleActivateModule, activateReq},
		{"deactivate", srv.handleDeactivateModule, deactivateReq},
		{"state", srv.handleGetModuleState, stateReq},
	} {
		rec := httptest.NewRecorder()
		tc.fn(rec, tc.req.WithContext(ctx))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("%s permission fault: want 500, got %d (%s)", tc.name, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
			t.Fatalf("%s decode 500 body: %v", tc.name, err)
		}
		for _, leaked := range leaks {
			if strings.Contains(body["error"], leaked) {
				t.Fatalf("%s 500 body membocorkan detail SQL: %q", tc.name, body["error"])
			}
		}
	}
}

func TestModuleActivationIdempotencyAndVersionPin(t *testing.T) {
	manifest020 := financeBasic020(t)
	env := setupActivation(t, [][3]string{{"finance.basic", "0.2.0", manifest020}})
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, body := activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	firstActivated := body["activated_at"]
	rowBefore, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if len(readModuleAudits(t, env.boot, env.wsA)) != 1 {
		t.Fatalf("audit: want 1, got %d", len(readModuleAudits(t, env.boot, env.wsA)))
	}
	// Ulang versi sama: 200, baris + audit persis sama.
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("repeat activate: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	if body["activated_at"] != firstActivated {
		t.Fatalf("no-op mengubah activated_at: %v vs %v", body["activated_at"], firstActivated)
	}
	rowAfter, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if rowAfter != rowBefore {
		t.Fatalf("no-op mengubah baris: %+v vs %+v", rowAfter, rowBefore)
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 1 {
		t.Fatalf("no-op menambah audit: got %d", len(readModuleAudits(t, env.boot, env.wsA)))
	}
	// Pin berbeda saat aktif (versi terdaftar valid): 409.
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.2.0")
	if code != http.StatusConflict {
		t.Fatalf("activate 0.2.0 saat aktif: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "versi berbeda")
	// Deaktivasi nyata, lalu ulang deaktivasi: 200 no-op.
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "inactive")
	assertTimestamp(t, body, "activated_at", true)
	assertTimestamp(t, body, "deactivated_at", true)
	rowDeact, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if rowDeact.ActivatedAt != rowBefore.ActivatedAt {
		t.Fatalf("deaktivasi mengubah activated_at")
	}
	if rowDeact.DeactivatedAt == "" {
		t.Fatalf("deaktivasi tidak mengisi deactivated_at")
	}
	audits := readModuleAudits(t, env.boot, env.wsA)
	if len(audits) != 2 {
		t.Fatalf("audit: want 2, got %d", len(audits))
	}
	assertModuleAudit(t, audits[1], "module_deactivated", "finance.basic", "0.1.0", "active", "inactive")
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("repeat deactivate: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "inactive")
	rowAfter2, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if rowAfter2 != rowDeact {
		t.Fatalf("deaktivasi no-op mengubah baris: %+v vs %+v", rowAfter2, rowDeact)
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 2 {
		t.Fatalf("deaktivasi no-op menambah audit")
	}
	// Pin berbeda saat inactive: 409 juga (tanpa entitlement 0.2.0).
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.2.0")
	if code != http.StatusConflict {
		t.Fatalf("activate 0.2.0 saat inactive: want 409, got %d (%v)", code, body)
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 2 {
		t.Fatalf("409 menambah audit")
	}
}

func TestModuleActivationDependenciesAndConflicts(t *testing.T) {
	env := setupActivation(t, [][3]string{
		{"qa.provider", "0.1.0", qaProviderManifest},
		{"qa.provider2", "0.1.0", qaProvider2Manifest},
		{"qa.consumer", "0.1.0", qaConsumerManifest},
		{"qa.opt", "0.1.0", qaOptManifest},
		{"qa.alpha", "0.1.0", qaAlphaManifest},
		{"qa.beta", "0.1.0", qaBetaManifest},
		{"qa.group1", "0.1.0", qaGroup1Manifest},
		{"qa.group2", "0.1.0", qaGroup2Manifest},
	})
	// Rental/Spa tanpa entitlement: 403 mendahului dependensi.
	for _, m := range []string{"rental.basic", "spa.standard"} {
		code, body := activateModule(t, env.srv, env.wsA, m, env.ownerToken, "0.1.0")
		if code != http.StatusForbidden {
			t.Fatalf("activate %s tanpa entitlement: want 403, got %d (%v)", m, code, body)
		}
	}
	// Dengan entitlement: rental kini 200 lewat penyedia core nyata
	// (F2-01-A: probe parties/catalog_items; tabel kosong pun valid),
	// spa tetap 409 karena booking.resource.hold tak ada penyedia.
	grantEntitlement(t, env.boot, env.wsA, "rental.basic", "0.1.0")
	grantEntitlement(t, env.boot, env.wsA, "spa.standard", "0.1.0")
	code, body := activateModule(t, env.srv, env.wsA, "rental.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("rental: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
	code, body = activateModule(t, env.srv, env.wsA, "spa.standard", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("spa: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "booking.resource.hold")
	// Konflik eksklusif finance: basic aktif, advanced 409.
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	grantEntitlement(t, env.boot, env.wsA, "finance.advanced", "0.1.0")
	code, _ = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate finance.basic: want 200, got %d", code)
	}
	code, body = activateModule(t, env.srv, env.wsA, "finance.advanced", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("finance.advanced: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "bentrok")
	// Requires optional tidak memblokir.
	grantEntitlement(t, env.boot, env.wsA, "qa.opt", "0.1.0")
	code, _ = activateModule(t, env.srv, env.wsA, "qa.opt", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.opt: want 200, got %d", code)
	}
	// Requires wajib tanpa penyedia: 409.
	grantEntitlement(t, env.boot, env.wsA, "qa.consumer", "0.1.0")
	code, body = activateModule(t, env.srv, env.wsA, "qa.consumer", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("qa.consumer tanpa penyedia: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "qa.cap.read")
	// Dengan penyedia aktif + entitled: 200.
	grantEntitlement(t, env.boot, env.wsA, "qa.provider", "0.1.0")
	code, _ = activateModule(t, env.srv, env.wsA, "qa.provider", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.provider: want 200, got %d", code)
	}
	code, _ = activateModule(t, env.srv, env.wsA, "qa.consumer", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.consumer: want 200, got %d", code)
	}
	// Konflik simetris: alpha aktif → beta 409 (arah sebaliknya:
	// daftar conflicts beta kosong, daftar alpha memuat beta).
	grantEntitlement(t, env.boot, env.wsA, "qa.alpha", "0.1.0")
	grantEntitlement(t, env.boot, env.wsA, "qa.beta", "0.1.0")
	code, _ = activateModule(t, env.srv, env.wsA, "qa.alpha", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.alpha: want 200, got %d", code)
	}
	code, body = activateModule(t, env.srv, env.wsA, "qa.beta", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("qa.beta: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "bentrok")
	// Arah maju: beta aktif → alpha 409 (daftar conflicts alpha).
	code, _ = deactivateModule(t, env.srv, env.wsA, "qa.alpha", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate qa.alpha: want 200, got %d", code)
	}
	code, _ = activateModule(t, env.srv, env.wsA, "qa.beta", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.beta: want 200, got %d", code)
	}
	code, body = activateModule(t, env.srv, env.wsA, "qa.alpha", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("qa.alpha: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "bentrok")
	// Exclusive group tanpa conflicts: group1 aktif → group2 409.
	grantEntitlement(t, env.boot, env.wsA, "qa.group1", "0.1.0")
	grantEntitlement(t, env.boot, env.wsA, "qa.group2", "0.1.0")
	code, _ = activateModule(t, env.srv, env.wsA, "qa.group1", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate qa.group1: want 200, got %d", code)
	}
	code, body = activateModule(t, env.srv, env.wsA, "qa.group2", env.ownerToken, "0.1.0")
	if code != http.StatusConflict {
		t.Fatalf("qa.group2: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "bentrok")
}

func TestModuleDeactivationDependencySafety(t *testing.T) {
	env := setupActivation(t, [][3]string{
		{"qa.provider", "0.1.0", qaProviderManifest},
		{"qa.provider2", "0.1.0", qaProvider2Manifest},
		{"qa.consumer", "0.1.0", qaConsumerManifest},
	})
	for _, m := range []string{"qa.provider", "qa.provider2", "qa.consumer"} {
		grantEntitlement(t, env.boot, env.wsA, m, "0.1.0")
		code, _ := activateModule(t, env.srv, env.wsA, m, env.ownerToken, "0.1.0")
		if code != http.StatusOK {
			t.Fatalf("activate %s: want 200, got %d", m, code)
		}
	}
	// Hapus satu penyedia: alternatif masih ada → 200.
	code, body := deactivateModule(t, env.srv, env.wsA, "qa.provider", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate qa.provider: want 200, got %d (%v)", code, body)
	}
	code, body = getModuleState(t, env.srv, env.wsA, "qa.consumer", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET qa.consumer: want 200, got %d", code)
	}
	assertState(t, body, "qa.consumer", "0.1.0", "active")
	// Hapus penyedia terakhir: consumer terdampar → 409, tetap aktif.
	code, body = deactivateModule(t, env.srv, env.wsA, "qa.provider2", env.ownerToken)
	if code != http.StatusConflict {
		t.Fatalf("deactivate qa.provider2: want 409, got %d (%v)", code, body)
	}
	assertFlatError(t, body, "qa.consumer")
	assertFlatError(t, body, "qa.cap.read")
	code, body = getModuleState(t, env.srv, env.wsA, "qa.provider2", env.ownerToken)
	assertState(t, body, "qa.provider2", "0.1.0", "active")
	// Sesudah consumer dimatikan, penyedia terakhir boleh dimatikan.
	code, _ = deactivateModule(t, env.srv, env.wsA, "qa.consumer", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate qa.consumer: want 200, got %d", code)
	}
	code, _ = deactivateModule(t, env.srv, env.wsA, "qa.provider2", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate qa.provider2 sesudah consumer mati: want 200, got %d", code)
	}
}

func TestModuleActivationRevocation(t *testing.T) {
	env := setupActivation(t, nil)
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, _ := activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate: want 200, got %d", code)
	}
	revokeEntitlement(t, env.boot, env.wsA, "finance.basic")
	// State tetap jujur: masih aktif sesudah hak dicabut.
	code, body := getModuleState(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah revoke: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	// Ulang aktivasi versi sama: 200 no-op tanpa audit baru.
	rowBefore, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("repeat sesudah revoke: want 200, got %d (%v)", code, body)
	}
	rowAfter, _ := readInstallation(t, env.boot, env.wsA, "finance.basic")
	if rowAfter != rowBefore {
		t.Fatalf("repeat sesudah revoke mengubah baris")
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 1 {
		t.Fatalf("repeat sesudah revoke menambah audit")
	}
	// Deaktivasi nyata sesudah revoke: 200, tanpa entitlement.
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate sesudah revoke: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "inactive")
	// Aktivasi ulang tanpa hak: 403.
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("activate sesudah revoke+deactivate: want 403, got %d (%v)", code, body)
	}
	// Grant ulang lalu aktivasi: 200, audit ketiga old_status inactive.
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate sesudah grant ulang: want 200, got %d (%v)", code, body)
	}
	audits := readModuleAudits(t, env.boot, env.wsA)
	if len(audits) != 3 {
		t.Fatalf("audit: want 3, got %d", len(audits))
	}
	assertModuleAudit(t, audits[2], "module_activated", "finance.basic", "0.1.0", "inactive", "active")
}

// parallel menjalankan pemanggilan API secara bersamaan dan mengembalikan
// kode + body tiap pemanggilan (tanpa t.Fatalf dari goroutine).
func parallel(t *testing.T, fns ...func() (int, map[string]any, error)) ([]int, []map[string]any) {
	t.Helper()
	codes := make([]int, len(fns))
	bodies := make([]map[string]any, len(fns))
	var wg sync.WaitGroup
	for i, fn := range fns {
		wg.Add(1)
		go func(i int, fn func() (int, map[string]any, error)) {
			defer wg.Done()
			code, body, err := fn()
			if err != nil {
				codes[i] = 0
				return
			}
			codes[i] = code
			bodies[i] = body
		}(i, fn)
	}
	wg.Wait()
	return codes, bodies
}

func TestModuleActivationConcurrency(t *testing.T) {
	env := setupActivation(t, nil)
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	grantEntitlement(t, env.boot, env.wsA, "finance.advanced", "0.1.0")
	grantEntitlement(t, env.boot, env.wsB, "finance.basic", "0.1.0")
	activateBasicA := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/finance.basic/activate", env.ownerToken, map[string]string{"version": "0.1.0"})
	}
	activateAdvancedA := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/finance.advanced/activate", env.ownerToken, map[string]string{"version": "0.1.0"})
	}
	// (a) Duplikat aktivasi: dua 200, satu transisi/audit.
	codes, _ := parallel(t, activateBasicA, activateBasicA)
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("duplikat aktivasi: want [200 200], got %v", codes)
	}
	if n := countInstallations(t, env.boot, env.wsA, "active"); n != 1 {
		t.Fatalf("duplikat aktivasi: want 1 instalasi aktif, got %d", n)
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 1 {
		t.Fatalf("duplikat aktivasi: want 1 audit, got %d", len(readModuleAudits(t, env.boot, env.wsA)))
	}
	// (b) Aktivasi eksklusif: satu 200, satu 409.
	code, _ := deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate sebelum eksklusif: want 200, got %d", code)
	}
	codes, _ = parallel(t, activateBasicA, activateAdvancedA)
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != 200 || codes[1] != 409 {
		t.Fatalf("aktivasi eksklusif: want [200 409], got %v", codes)
	}
	if n := countInstallations(t, env.boot, env.wsA, "active"); n != 1 {
		t.Fatalf("aktivasi eksklusif: want 1 instalasi aktif, got %d", n)
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != 3 {
		t.Fatalf("aktivasi eksklusif: want 3 audit, got %d", len(readModuleAudits(t, env.boot, env.wsA)))
	}
	// (c) Aktivasi vs deaktivasi pada modul yang AKTIF (pemenang (b) —
	// basic atau advanced, nondeterministik). Dua 200; respons tiap
	// operasi cocok dengan state yang di-commit operasi itu (aktivasi
	// selalu melaporkan active, deaktivasi selalu inactive).
	var activeModule string
	if err := env.boot.QueryRow(
		`SELECT module_id FROM module_installations WHERE workspace_id = $1 AND status = 'active'`, env.wsA).Scan(&activeModule); err != nil {
		t.Fatalf("modul aktif pemenang (b): %v", err)
	}
	activateActive := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/"+activeModule+"/activate", env.ownerToken, map[string]string{"version": "0.1.0"})
	}
	deactivateActive := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsA+"/modules/"+activeModule+"/deactivate", env.ownerToken, nil)
	}
	auditsBeforeC := len(readModuleAudits(t, env.boot, env.wsA))
	codes, bodies := parallel(t, activateActive, deactivateActive)
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("aktivasi vs deaktivasi: want [200 200], got %v", codes)
	}
	// bodies[0] = respons aktivasi, bodies[1] = respons deaktivasi
	// (parallel mempertahankan urutan fungsi, bukan urutan selesai).
	if bodies[0]["status"] != "active" {
		t.Fatalf("respons aktivasi: want status active, got %v", bodies[0])
	}
	if bodies[1]["status"] != "inactive" {
		t.Fatalf("respons deaktivasi: want status inactive, got %v", bodies[1])
	}
	code, body := getModuleState(t, env.srv, env.wsA, activeModule, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah aktivasi vs deaktivasi: want 200, got %d", code)
	}
	finalStatus, _ := body["status"].(string)
	// State akhir bergantung pada urutan serialisasi, tapi konsisten
	// dengan jumlah audit yang ditambahkan: aktivasi dulu → no-op
	// (0) + deaktivasi (1) = +1 dan akhir inactive; deaktivasi dulu →
	// (1) + aktivasi ulang (1) = +2 dan akhir active. Urutan audit
	// TIDAK ditegaskan lewat created_at: itu now() awal transaksi,
	// bukan urutan commit, jadi tidak mencerminkan serialisasi.
	delta := len(readModuleAudits(t, env.boot, env.wsA)) - auditsBeforeC
	if (delta == 1 && finalStatus != "inactive") || (delta == 2 && finalStatus != "active") || (delta != 1 && delta != 2) {
		t.Fatalf("aktivasi vs deaktivasi: delta audit %d tidak konsisten dengan state akhir %q", delta, finalStatus)
	}
	row, found := readInstallation(t, env.boot, env.wsA, activeModule)
	if !found {
		t.Fatalf("aktivasi vs deaktivasi: modul %s tidak punya baris", activeModule)
	}
	if row.Status != finalStatus {
		t.Fatalf("baris %s status %q tidak cocok dengan GET %q", activeModule, row.Status, finalStatus)
	}
	// Normalisasi: matikan modul yang masih aktif supaya (d) mulai
	// dari kedua modul inactive (deterministik).
	if finalStatus == "active" {
		code, _ := deactivateModule(t, env.srv, env.wsA, activeModule, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("deactivate normalisasi: want 200, got %d", code)
		}
	}
	// (d) Workspace berbeda bersamaan: dua 200, state independen, tanpa
	// bocor konteks tenant antar koneksi pool.
	auditsWsBBefore := len(readModuleAudits(t, env.boot, env.wsB))
	activateBasicB := func() (int, map[string]any, error) {
		return callModuleAPI(env.srv, "POST", "/workspaces/"+env.wsB+"/modules/finance.basic/activate", env.otherToken, map[string]string{"version": "0.1.0"})
	}
	codes, _ = parallel(t, activateBasicA, activateBasicB)
	sort.Ints(codes)
	if len(codes) != 2 || codes[0] != 200 || codes[1] != 200 {
		t.Fatalf("aktivasi lintas workspace: want [200 200], got %v", codes)
	}
	code, body = getModuleState(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET wsA: want 200, got %d", code)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	code, body = getModuleState(t, env.srv, env.wsB, "finance.basic", env.otherToken)
	if code != http.StatusOK {
		t.Fatalf("GET wsB: want 200, got %d", code)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
	if len(readModuleAudits(t, env.boot, env.wsB)) != auditsWsBBefore+1 {
		t.Fatalf("audit wsB: want %d, got %d", auditsWsBBefore+1, len(readModuleAudits(t, env.boot, env.wsB)))
	}
}

func TestModuleActivationAuditFaultAtomicity(t *testing.T) {
	env := setupActivation(t, [][3]string{{"qa.fault", "0.1.0", qaFaultManifest}})
	grantEntitlement(t, env.boot, env.wsA, "qa.fault", "0.1.0")
	// Suntik kegagalan audit: trigger BEFORE INSERT yang menolak audit
	// aktivasi qa.fault saja; dipulihkan di cleanup.
	if _, err := env.boot.Exec(`CREATE OR REPLACE FUNCTION app.qa_fail_module_audit() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			IF NEW.action = 'module_activated' AND NEW.details->>'module_id' = 'qa.fault' THEN
				RAISE EXCEPTION 'injected audit fault (fixture)';
			END IF;
			RETURN NEW;
		END;
	$$`); err != nil {
		t.Fatalf("create fault function: %v", err)
	}
	if _, err := env.boot.Exec(`CREATE TRIGGER qa_fault_module_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION app.qa_fail_module_audit()`); err != nil {
		t.Fatalf("create fault trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_module_audit ON audit_events`)
		_, _ = env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_module_audit()`)
	})
	auditsBefore := len(readModuleAudits(t, env.boot, env.wsA))
	code, body := activateModule(t, env.srv, env.wsA, "qa.fault", env.ownerToken, "0.1.0")
	if code != http.StatusInternalServerError {
		t.Fatalf("activate dengan audit gagal: want 500, got %d (%v)", code, body)
	}
	msg, _ := body["error"].(string)
	if msg != "transaksi gagal" {
		t.Fatalf("500 fault: want %q, got %q", "transaksi gagal", msg)
	}
	// Tidak ada mutasi instalasi maupun audit yang lolos.
	if _, found := readInstallation(t, env.boot, env.wsA, "qa.fault"); found {
		t.Fatalf("fault: baris instalasi lolos walau audit gagal")
	}
	if len(readModuleAudits(t, env.boot, env.wsA)) != auditsBefore {
		t.Fatalf("fault: audit bertambah walau transaksi gagal")
	}
	code, body = getModuleState(t, env.srv, env.wsA, "qa.fault", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah fault: want 200, got %d", code)
	}
	assertState(t, body, "qa.fault", nil, "inactive")
	// Pulihkan fault secara manual, lalu aktivasi modul nyata tetap
	// berfungsi — bukti fault tidak meninggalkan kerusakan.
	if _, err := env.boot.Exec(`DROP TRIGGER IF EXISTS qa_fault_module_audit ON audit_events`); err != nil {
		t.Fatalf("drop fault trigger: %v", err)
	}
	if _, err := env.boot.Exec(`DROP FUNCTION IF EXISTS app.qa_fail_module_audit()`); err != nil {
		t.Fatalf("drop fault function: %v", err)
	}
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("activate sesudah fault dipulihkan: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
}

func TestBeginTenantIsReadCommitted(t *testing.T) {
	db := testDB(t)
	tx, err := beginTenant(db, "00000000-0000-0000-0000-000000000000", "")
	if err != nil {
		t.Fatalf("beginTenant: %v", err)
	}
	defer tx.Rollback()
	var level string
	if err := tx.QueryRow(`SHOW transaction_isolation`).Scan(&level); err != nil {
		t.Fatalf("SHOW transaction_isolation: %v", err)
	}
	if level != "read committed" {
		t.Fatalf("isolasi transaksi: want read committed, got %q", level)
	}
}
