package main

import (
	"bytes"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Tes integrasi grant operator F1-OPERATOR-ENTITLEMENT-IMPLEMENT.
// Yang diuji adalah CLI dan rutin SUNGGUHAN (bukan helper SQL yang
// mengimplementasikan ulang grant, bukan grant bootstrap sintetis).
// Prasyarat yang hilang (011/peran/DSN operator) adalah GAGAL, bukan
// dilewati — kecuali tidak ada database sama sekali (pola testDB yang
// sudah ada). Baris audit dipertahankan (append-only); cleanup hanya
// menghapus baris entitlement fixture per workspace tes.

const (
	grantOpRole        = "umnia_op_test"
	grantOpPassword    = "ci-operator-only" // HANYA fixture CI/dev, bukan rahasia produksi.
	grantFixtureReason = "tes grant operator"
)

// Kode keluar CLI (cermin cmd/grant-entitlement/main.go; paket main
// yang berbeda tidak berbagi konstanta).
const (
	grantExitUnavailable = 1
	grantExitInput       = 2
	grantExitConflict    = 3
)

var migrations001to010 = []string{
	"001_init.sql", "002_identity.sql", "003_modules.sql",
	"004_module_installations.sql", "005_core_master_data.sql",
	"006_rental_contracts.sql", "007_asset_availability.sql",
	"008_price_versions.sql", "009_rental_agreed_prices.sql",
	"010_rental_custody.sql",
}

// grantTestEnv menerapkan 011 idempotent via psql asli sebagai bootstrap
// (autocommit — jalur Linux native) dan mengembalikan DSN operator.
// Gagal bila peran operator belum dibuat provisioning atau 011 gagal.
func grantTestEnv(t *testing.T) (db, boot *sql.DB, opURL string) {
	t.Helper()
	db = testDB(t)
	boot = bootstrapDB(t)
	opURL = os.Getenv("OPERATOR_TEST_DATABASE_URL")
	if opURL == "" {
		t.Fatalf("OPERATOR_TEST_DATABASE_URL kosong — provisioning operator (configure-postgres.sh / CI) belum dijalankan; tes gagal, bukan dilewati")
	}
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	code, out := applyMigration011(t, bootURL)
	if code != 0 {
		t.Fatalf("terapkan 011: exit %d (%s)", code, out)
	}
	return db, boot, opURL
}

// applyMigration011 menjalankan psql asli untuk 011 DALAM SATU
// transaksi (-1): gagal di tengah jalan me-rollback seluruh file.
// psql yang hilang adalah kegagalan prasyarat, bukan alasan dilewati.
func applyMigration011(t *testing.T, dbURL string) (int, string) {
	t.Helper()
	return applyMigrationFile(t, dbURL, filepath.Join("migrations", "011_operator_entitlement.sql"))
}

// applyMigrationFile menjalankan SATU file migrasi lewat psql -1 -f
// (satu transaksi milik pemanggil; tanpa COMMIT di dalam file).
func applyMigrationFile(t *testing.T, dbURL, path string) (int, string) {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql tidak ada di PATH — prasyarat tes migrasi (bukan dilewati)")
	}
	cmd := exec.Command(psql, dbURL, "-v", "ON_ERROR_STOP=1", "-1", "-f", path)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("jalankan psql %s: %v", path, err)
		}
	}
	return code, out.String()
}

// applyMigrationFiles menerapkan daftar file migrasi ke satu DB dalam
// satu pemanggilan psql -1 (satu transaksi untuk semua file).
func applyMigrationFiles(t *testing.T, dbURL string, files []string) {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql tidak ada di PATH — prasyarat tes (bukan dilewati)")
	}
	args := []string{dbURL, "-v", "ON_ERROR_STOP=1", "-1"}
	for _, f := range files {
		args = append(args, "-f", filepath.Join("migrations", f))
	}
	cmd := exec.Command(psql, args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("terapkan %v ke %s: %v (%s)", files, dbURL, err, out.String())
	}
}

// uniqueSuffix menghasilkan akhiran heks acak untuk nama fixture DB
// (nama unik per run; tabrakan yang sudah ada tidak pernah dihapus).
func uniqueSuffix(t *testing.T) string {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("acak suffix: %v", err)
	}
	return hex.EncodeToString(b[:])
}

// createOwnedScratchDB membuat DB fixture dengan nama unik. Bila nama
// sudah ada (tabrakan), tes GAGAL — tidak pernah DROP DB yang sudah
// ada. Cleanup hanya menghapus DB yang berhasil dibuat tes ini.
func createOwnedScratchDB(t *testing.T, boot *sql.DB, prefix string) (string, *sql.DB) {
	t.Helper()
	name := prefix + "_" + uniqueSuffix(t)
	var exists bool
	if err := boot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		t.Fatalf("cek tabrakan %s: %v", name, err)
	}
	if exists {
		t.Fatalf("nama scratch %q sudah ada (tabrakan) — tes tidak menghapus DB yang sudah ada", name)
	}
	if _, err := boot.Exec(`CREATE DATABASE ` + name + ` OWNER umnia_business`); err != nil {
		t.Fatalf("buat scratch db %s: %v", name, err)
	}
	created := true
	t.Cleanup(func() {
		if created {
			_, _ = boot.Exec(`DROP DATABASE IF EXISTS ` + name)
		}
	})
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	u, err := url.Parse(bootURL)
	if err != nil {
		t.Fatalf("parse bootstrap URL: %v", err)
	}
	u.Path = "/" + name
	scratchBoot, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatalf("buka bootstrap scratch %s: %v", name, err)
	}
	t.Cleanup(func() { scratchBoot.Close() })
	return name, scratchBoot
}

func buildGrantCLI(t *testing.T) string {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("go tidak ada di PATH (prasyarat tes CLI)")
	}
	bin := filepath.Join(t.TempDir(), "grant-entitlement")
	cmd := exec.Command(goBin, "build", "-o", bin, "./cmd/grant-entitlement")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("build CLI: %v (%s)", err, out.String())
	}
	return bin
}

type cliResult struct {
	code   int
	stdout string
	stderr string
}

func runGrantCLI(t *testing.T, bin, opURL string, args ...string) cliResult {
	t.Helper()
	cmd := exec.Command(bin, args...)
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "UMNIA_OPERATOR_DATABASE_URL=") {
			env = append(env, kv)
		}
	}
	env = append(env, "UMNIA_OPERATOR_DATABASE_URL="+opURL)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("jalankan CLI: %v", err)
		}
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

// assertCLIOutputClean membuktikan output CLI tidak membocorkan kata
// sandi operator (dari DSN) atau alasan grant.
func assertCLIOutputClean(t *testing.T, res cliResult, opURL, reason string) {
	t.Helper()
	u, err := url.Parse(opURL)
	if err != nil {
		t.Fatalf("parse DSN operator: %v", err)
	}
	if pw, ok := u.User.Password(); ok && pw != "" {
		if strings.Contains(res.stdout, pw) || strings.Contains(res.stderr, pw) {
			t.Fatalf("output CLI membocorkan kata sandi operator")
		}
	}
	if strings.Contains(res.stdout, reason) || strings.Contains(res.stderr, reason) {
		t.Fatalf("output CLI membocorkan alasan: out=%q err=%q", res.stdout, res.stderr)
	}
}

func openOperatorDB(t *testing.T, opURL string) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", opURL)
	if err != nil {
		t.Fatalf("open operator db: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping operator db: %v", err)
	}
	return db
}

func assertPermissionDenied(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: seharusnya ditolak, malah lolos", what)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code != "42501" {
			t.Fatalf("%s: want insufficient_privilege (42501), got %s (%v)", what, pgErr.Code, err)
		}
		return
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("%s: want permission denied, got %v", what, err)
	}
}

func dsnWithDB(t *testing.T, dsn, dbName string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u.Path = "/" + dbName
	return u.String()
}

func dsnWithPassword(t *testing.T, dsn, pw string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	u.User = url.UserPassword(u.User.Username(), pw)
	return u.String()
}

func countEntitlements(t *testing.T, boot *sql.DB, ws string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM module_entitlements WHERE workspace_id = $1`, ws).Scan(&n); err != nil {
		t.Fatalf("hitung entitlement: %v", err)
	}
	return n
}

func countOperatorAudit(t *testing.T, boot *sql.DB, ws string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'rental.basic' AND version = '0.1.0'`, ws).Scan(&n); err != nil {
		t.Fatalf("hitung audit operator: %v", err)
	}
	return n
}

func TestGrantEntitlementCLI_PositiveAndActivation(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})

	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("CLI grant: want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "granted workspace="+env.wsA+" ") {
		t.Fatalf("CLI grant: output tak terduga %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "granted_by=operator:"+grantOpRole) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("CLI grant: output tidak memuat aktor/audit: %q", res.stdout)
	}
	assertCLIOutputClean(t, res, opURL, grantFixtureReason)

	var grantedBy, source, reason string
	if err := boot.QueryRow(`SELECT granted_by, source, reason FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA).Scan(&grantedBy, &source, &reason); err != nil {
		t.Fatalf("baca entitlement: %v", err)
	}
	if grantedBy != "operator:"+grantOpRole || source != "operator-cli" || reason != grantFixtureReason {
		t.Fatalf("entitlement: granted_by=%q source=%q reason=%q", grantedBy, source, reason)
	}
	var operatorID, action, result string
	if err := boot.QueryRow(`SELECT operator_id, action, result FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'rental.basic' AND version = '0.1.0'`, env.wsA).Scan(&operatorID, &action, &result); err != nil {
		t.Fatalf("baca audit operator: %v", err)
	}
	if operatorID != grantOpRole || action != "entitlement_granted" || result != "granted" {
		t.Fatalf("audit: operator_id=%q action=%q result=%q", operatorID, action, result)
	}

	// Aktivasi pemilik lewat API yang sudah ada kini 200.
	code, body := doJSON(t, env.srv, "POST", "/workspaces/"+env.wsA+"/modules/rental.basic/activate", env.ownerToken, map[string]string{"version": "0.1.0"})
	if code != http.StatusOK {
		t.Fatalf("activate sesudah grant: want 200, got %d (%v)", code, body)
	}
}

func TestGrantEntitlementCLI_NoOpLegacyConflict(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	// Registry sintetis rental.basic/0.2.0: perlu untuk baris legacy
	// 0.2.0 (FK entitlement -> registry); bukan versi yang didukung.
	m020 := exampleManifest(t, "rental.basic")
	m020["version"] = "0.2.0"
	raw020, err := json.Marshal(m020)
	if err != nil {
		t.Fatalf("marshal rental 0.2.0: %v", err)
	}
	env := setupActivation(t, [][3]string{{"rental.basic", "0.2.0", string(raw020)}})
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})

	// Grant pertama, lalu ulangi: already_granted tanpa tulis baru.
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("grant pertama: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
	var grantedBy1, grantedAt1 string
	if err := boot.QueryRow(`SELECT granted_by, granted_at::text FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&grantedBy1, &grantedAt1); err != nil {
		t.Fatalf("baca entitlement pertama: %v", err)
	}
	res = runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", "alasan kedua yang berbeda")
	if res.code != 0 || !strings.HasPrefix(res.stdout, "already_granted workspace="+env.wsA+" ") {
		t.Fatalf("grant ulang: want exit 0 + already_granted, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "granted_by="+grantedBy1) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("grant ulang: output tidak memuat aktor asli: %q", res.stdout)
	}
	var grantedBy2, grantedAt2, source2, reason2 string
	if err := boot.QueryRow(`SELECT granted_by, granted_at::text, source, reason FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&grantedBy2, &grantedAt2, &source2, &reason2); err != nil {
		t.Fatalf("baca entitlement kedua: %v", err)
	}
	if grantedBy2 != grantedBy1 || grantedAt2 != grantedAt1 || source2 != "operator-cli" || reason2 != grantFixtureReason {
		t.Fatalf("no-op menulis ulang metadata: sebelum (%q,%q) sesudah (%q,%q,%q,%q)", grantedBy1, grantedAt1, grantedBy2, grantedAt2, source2, reason2)
	}
	if n := countOperatorAudit(t, boot, env.wsA); n != 1 {
		t.Fatalf("no-op: want 1 baris audit, got %d", n)
	}

	// Legacy: baris sintetis tanpa audit -> legacy_existing, tanpa
	// fabrikasi audit dan tanpa tulis ulang.
	grantEntitlement(t, boot, env.wsB, "rental.basic", "0.1.0")
	res = runGrantCLI(t, bin, opURL, "--workspace", env.wsB, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "legacy_existing workspace="+env.wsB+" ") {
		t.Fatalf("legacy: want exit 0 + legacy_existing, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "granted_by=operator:fixture") || !strings.Contains(res.stdout, "audit_present=false") {
		t.Fatalf("legacy: output tidak memuat aktor lama/audit_present=false: %q", res.stdout)
	}
	if n := countOperatorAudit(t, boot, env.wsB); n != 0 {
		t.Fatalf("legacy: want 0 baris audit (tanpa fabrikasi), got %d", n)
	}

	// Gate versi mendahului cabang no-op/konflik: permintaan 0.2.0
	// pada workspace yang sudah diberi 0.1.0 adalah invalid (exit 2),
	// bukan conflict — 0.2.0 di luar scope beku apa pun barisnya.
	res = runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.2.0", "--reason", grantFixtureReason)
	if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("versi tak didukung pada ws bergrant: want exit 2 + invalid input, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if n := countEntitlements(t, boot, env.wsA); n != 1 {
		t.Fatalf("invalid versi: baris entitlement berubah (want 1, got %d)", n)
	}
	var verA string
	if err := boot.QueryRow(`SELECT version FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&verA); err != nil || verA != "0.1.0" {
		t.Fatalf("invalid versi: versi baris berubah (want 0.1.0, got %q err=%v)", verA, err)
	}

	// Legacy 0.2.0: permintaan 0.1.0 yang didukung terhadap baris
	// versi berbeda tetap konflik (exit 3); permintaan 0.2.0 sendiri
	// tetap invalid (exit 2) — gate versi tidak peduli baris yang ada.
	wsC := createWorkspace(t, env.srv, env.ownerToken, "Usaha Grant C")
	t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, wsC) })
	grantEntitlement(t, boot, wsC, "rental.basic", "0.2.0")
	res = runGrantCLI(t, bin, opURL, "--workspace", wsC, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 3 || !strings.Contains(res.stderr, "conflict") {
		t.Fatalf("konflik versi: want exit 3 + conflict, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	var verC, byC string
	if err := boot.QueryRow(`SELECT version, granted_by FROM module_entitlements WHERE workspace_id = $1`, wsC).Scan(&verC, &byC); err != nil || verC != "0.2.0" || byC != "operator:fixture" {
		t.Fatalf("konflik: baris legacy berubah (want 0.2.0/operator:fixture, got %q/%q err=%v)", verC, byC, err)
	}
	if n := countOperatorAudit(t, boot, wsC); n != 0 {
		t.Fatalf("konflik: audit tertulis (want 0, got %d)", n)
	}
	res = runGrantCLI(t, bin, opURL, "--workspace", wsC, "--module", "rental.basic", "--version", "0.2.0", "--reason", grantFixtureReason)
	if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("legacy 0.2.0 + minta 0.2.0: want exit 2 + invalid input, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if err := boot.QueryRow(`SELECT version FROM module_entitlements WHERE workspace_id = $1`, wsC).Scan(&verC); err != nil || verC != "0.2.0" {
		t.Fatalf("legacy 0.2.0 + minta 0.2.0: baris berubah (want 0.2.0, got %q err=%v)", verC, err)
	}

	// Modul terdaftar lain dan yang tidak dikenal: exit 2, tanpa tulis.
	wsD := createWorkspace(t, env.srv, env.ownerToken, "Usaha Grant D")
	t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, wsD) })
	for _, mod := range []struct{ id, ver string }{
		{"spa.standard", "0.1.0"},
		{"modul.tidak.ada", "9.9.9"},
		{"rental.basic", "0.2.0"},
	} {
		res = runGrantCLI(t, bin, opURL, "--workspace", wsD, "--module", mod.id, "--version", mod.ver, "--reason", grantFixtureReason)
		if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
			t.Fatalf("modul %s %s: want exit 2 + invalid input, got %d (out=%q err=%q)", mod.id, mod.ver, res.code, res.stdout, res.stderr)
		}
	}
	if n := countEntitlements(t, boot, wsD); n != 0 {
		t.Fatalf("modul tak didukung: want 0 baris, got %d", n)
	}
}

func TestGrantEntitlementCLI_Negatives(t *testing.T) {
	db, boot, opURL := grantTestEnv(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	opDB := openOperatorDB(t, opURL)

	// (a) DML mentah dan baca tabel sebagai operator ditolak.
	for _, q := range []string{
		`INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source, reason) VALUES ($1, 'rental.basic', '0.1.0', 'x', 'x', 'x')`,
		`INSERT INTO operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result) VALUES ($1, 'rental.basic', '0.1.0', 'x', 'entitlement_granted', 'x', 'x', 'granted')`,
		`SELECT 1 FROM workspaces WHERE id = $1`,
		`SELECT 1 FROM module_entitlements WHERE workspace_id = $1`,
		`SELECT 1 FROM operator_audit_events WHERE workspace_id = $1`,
	} {
		_, err := opDB.Exec(q, env.wsA)
		assertPermissionDenied(t, err, "operator: "+q)
	}

	// (b) Runtime bisnis memanggil rutin: tanpa EXECUTE.
	_, err := db.Exec(`SELECT * FROM app.grant_module_entitlement($1, 'rental.basic', '0.1.0', 'tes')`, env.wsA)
	assertPermissionDenied(t, err, "runtime invoke")

	// (c) SET ROLE pemilik ditolak (bukan anggota).
	_, err = opDB.Exec(`SET ROLE umnia_grant_exec`)
	assertPermissionDenied(t, err, "SET ROLE pemilik")

	// (d) GUC palsu + SET ROLE kapabilitas tidak mengubah aktor: grant
	// tetap tercatat atas session_user. Dijalankan dalam satu transaksi
	// supaya pasti satu koneksi yang sama.
	tx, err := opDB.Begin()
	if err != nil {
		t.Fatalf("begin spoof: %v", err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SET app.current_subject_id = 'subjek-palsu'`); err != nil {
		t.Fatalf("SET GUC: %v", err)
	}
	if _, err := tx.Exec(`SET ROLE umnia_operator`); err != nil {
		t.Fatalf("SET ROLE grup: %v", err)
	}
	var result, grantedBy string
	if err := tx.QueryRow(`SELECT result, granted_by FROM app.grant_module_entitlement($1, 'rental.basic', '0.1.0', 'tes spoof')`, env.wsA).Scan(&result, &grantedBy); err != nil {
		t.Fatalf("grant sesudah spoof: %v", err)
	}
	if result != "granted" || grantedBy != "operator:"+grantOpRole {
		t.Fatalf("aktor sesudah spoof: result=%q granted_by=%q", result, grantedBy)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit spoof: %v", err)
	}
	var operatorID string
	if err := boot.QueryRow(`SELECT operator_id FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'rental.basic'`, env.wsA).Scan(&operatorID); err != nil {
		t.Fatalf("baca audit spoof: %v", err)
	}
	if operatorID != grantOpRole {
		t.Fatalf("audit actor sesudah spoof: want %q, got %q", grantOpRole, operatorID)
	}

	// (e) Tabel temporer bayangan tidak mengubah target: grant ke wsB
	// mendarat di public.module_entitlements, temp tetap kosong.
	if _, err := opDB.Exec(`CREATE TEMP TABLE module_entitlements (workspace_id uuid, module_id text, version text, granted_by text, source text, reason text)`); err != nil {
		t.Fatalf("buat temp table: %v", err)
	}
	var resultB string
	if err := opDB.QueryRow(`SELECT result FROM app.grant_module_entitlement($1, 'rental.basic', '0.1.0', 'tes shadow')`, env.wsB).Scan(&resultB); err != nil {
		t.Fatalf("grant sesudah shadow: %v", err)
	}
	if resultB != "granted" {
		t.Fatalf("grant sesudah shadow: want granted, got %q", resultB)
	}
	var tempCount int
	if err := opDB.QueryRow(`SELECT count(*) FROM module_entitlements`).Scan(&tempCount); err != nil {
		t.Fatalf("hitung temp: %v", err)
	}
	if tempCount != 0 {
		t.Fatalf("temp table bayangan terisi (want 0, got %d)", tempCount)
	}
	if n := countEntitlements(t, boot, env.wsB); n != 1 {
		t.Fatalf("public entitlement sesudah shadow: want 1, got %d", n)
	}
}

func TestGrantEntitlementCLI_AuditFaultRollsBack(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, env.wsA)
	})
	// Fault: cabut INSERT audit dari peran eksekusi; kembalikan di
	// Cleanup (idempotent) apa pun hasil tesnya.
	t.Cleanup(func() {
		_, _ = boot.Exec(`GRANT INSERT ON operator_audit_events TO umnia_grant_exec`)
	})
	if _, err := boot.Exec(`REVOKE INSERT ON operator_audit_events FROM umnia_grant_exec`); err != nil {
		t.Fatalf("pasang fault audit: %v", err)
	}
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != grantExitUnavailable || !strings.Contains(res.stderr, "unavailable") {
		t.Fatalf("fault audit: want exit 1 + unavailable, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if n := countEntitlements(t, boot, env.wsA); n != 0 {
		t.Fatalf("fault audit: grant ikut tertulis (want 0, got %d)", n)
	}
	if n := countOperatorAudit(t, boot, env.wsA); n != 0 {
		t.Fatalf("fault audit: baris audit tertulis (want 0, got %d)", n)
	}
}

func TestGrantEntitlementCLI_LockSerialization(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, env.wsA)
	})
	opDB := openOperatorDB(t, opURL)

	// Grant dalam transaksi yang ditahan: kunci workspace dipegang.
	tx, err := opDB.Begin()
	if err != nil {
		t.Fatalf("begin grant: %v", err)
	}
	defer tx.Rollback()
	var result string
	if err := tx.QueryRow(`SELECT result FROM app.grant_module_entitlement($1, 'rental.basic', '0.1.0', 'tes lock')`, env.wsA).Scan(&result); err != nil {
		t.Fatalf("grant dalam tx: %v", err)
	}
	if result != "granted" {
		t.Fatalf("grant dalam tx: want granted, got %q", result)
	}

	// Aktivasi lewat API harus menunggu kunci (tanpa t di goroutine).
	type actOut struct {
		code int
		body map[string]any
		err  error
	}
	done := make(chan actOut, 1)
	go func() {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(map[string]string{"version": "0.1.0"})
		req, reqErr := http.NewRequest("POST", env.srv.URL+"/workspaces/"+env.wsA+"/modules/rental.basic/activate", &buf)
		if reqErr != nil {
			done <- actOut{err: reqErr}
			return
		}
		req.Header.Set("Authorization", "Bearer "+env.ownerToken)
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			done <- actOut{err: doErr}
			return
		}
		defer resp.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&out)
		done <- actOut{code: resp.StatusCode, body: out}
	}()
	select {
	case r := <-done:
		t.Fatalf("aktivasi tidak menunggu kunci grant: %+v", r)
	case <-time.After(2 * time.Second):
		// Menunggu seperti yang diharapkan.
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit grant: %v", err)
	}
	select {
	case r := <-done:
		if r.err != nil {
			t.Fatalf("aktivasi sesudah commit: %v", r.err)
		}
		if r.code != http.StatusOK {
			t.Fatalf("aktivasi sesudah grant commit: want 200, got %d (%v)", r.code, r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("aktivasi tidak selesai sesudah grant commit")
	}
}

func TestGrantEntitlementCLI_CrossWorkspace(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("grant wsA: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
	// Aktivasi di wsB (pemilik lain) tetap 403: grant A tidak
	// memberi hak ke B.
	code, body := doJSON(t, env.srv, "POST", "/workspaces/"+env.wsB+"/modules/rental.basic/activate", env.otherToken, map[string]string{"version": "0.1.0"})
	if code != http.StatusForbidden {
		t.Fatalf("activate wsB sesudah grant wsA: want 403, got %d (%v)", code, body)
	}
	// Grant ke wsB sendiri tetap bisa (operator terskop per workspace).
	res = runGrantCLI(t, bin, opURL, "--workspace", env.wsB, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("grant wsB: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
}

func TestGrantEntitlementCLI_Missing011FailClosed(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	// DB scratch milik tes ini: nama unik, dibuat hanya bila belum ada,
	// dihapus di Cleanup hanya bila tes ini yang membuatnya. Tidak ada
	// DROP atas nama tetap/DB yang sudah ada.
	scratch, scratchBoot := createOwnedScratchDB(t, boot, "umnia_test_no011")
	scratchBootURL := dsnWithDB(t, bootURL, scratch)
	// Sentinel: DB tetap yang dibuat tes ini; harus bertahan sampai
	// akhir tes (bukti tes tidak pernah DROP buta nama tetap). Bila
	// sudah ada dari run sebelumnya yang crash, jangan dihapus —
	// cukup buktikan ia bertahan.
	sentinel := "umnia_test_no011_sentinel"
	var sentinelExists bool
	if err := boot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, sentinel).Scan(&sentinelExists); err != nil {
		t.Fatalf("cek sentinel: %v", err)
	}
	sentinelCreated := false
	if !sentinelExists {
		if _, err := boot.Exec(`CREATE DATABASE ` + sentinel + ` OWNER umnia_business`); err != nil {
			t.Fatalf("buat sentinel db: %v", err)
		}
		sentinelCreated = true
	}
	t.Cleanup(func() {
		if sentinelCreated {
			_, _ = boot.Exec(`DROP DATABASE IF EXISTS ` + sentinel)
		}
	})
	applyMigrationFiles(t, scratchBootURL, migrations001to010)
	// Sentinel harus masih ada sesudah semua operasi di atas.
	if err := boot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, sentinel).Scan(&sentinelExists); err != nil || !sentinelExists {
		t.Fatalf("sentinel %q hilang (err=%v) — tes tidak boleh menghapus DB yang bukan miliknya", sentinel, err)
	}
	// Peran klaster ada, tapi rutin/011 tidak ada di DB ini: CLI
	// gagal tertutup (not ready), tanpa tulis.
	scratchOpURL := dsnWithDB(t, opURL, scratch)
	env := setupActivation(t, nil)
	res := runGrantCLI(t, bin, scratchOpURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != grantExitInput || !strings.Contains(res.stderr, "not ready") {
		t.Fatalf("scratch tanpa 011: want exit 2 + not ready, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	// Operator bisa connect (CONNECT PUBLIC) tapi tidak bisa baca
	// tabel apa pun di DB tanpa 011 — bukti peran global klaster,
	// izin/skema per-database.
	opScratch := openOperatorDB(t, scratchOpURL)
	_, err := opScratch.Exec(`SELECT 1 FROM workspaces LIMIT 1`)
	assertPermissionDenied(t, err, "operator SELECT scratch")
	// Jalur fresh: terapkan 011 ke scratch, grant kini berhasil.
	// Workspace harus dibuat DI scratch (ws utama hidup di
	// umnia_test; rutin mengunci baris workspace di DB yang sama).
	code, outStr := applyMigration011(t, scratchBootURL)
	if code != 0 {
		t.Fatalf("terapkan 011 ke scratch: exit %d (%s)", code, outStr)
	}
	// scratchBoot sudah dibuka createOwnedScratchDB di atas.
	var scratchSubj, scratchWS string
	if err := scratchBoot.QueryRow(`INSERT INTO identity_subjects (email, password_hash) VALUES ('scratch-op@test.local', 'x') RETURNING id`).Scan(&scratchSubj); err != nil {
		t.Fatalf("buat subjek scratch: %v", err)
	}
	if err := scratchBoot.QueryRow(`INSERT INTO workspaces (name, owner_subject_id) VALUES ('scratch', $1) RETURNING id`, scratchSubj).Scan(&scratchWS); err != nil {
		t.Fatalf("buat workspace scratch: %v", err)
	}
	res = runGrantCLI(t, bin, scratchOpURL, "--workspace", scratchWS, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("grant sesudah 011 di scratch: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
	var scratchEntitlements int
	if err := scratchBoot.QueryRow(`SELECT count(*) FROM module_entitlements WHERE workspace_id = $1`, scratchWS).Scan(&scratchEntitlements); err != nil || scratchEntitlements != 1 {
		t.Fatalf("grant di scratch: want 1 baris entitlement, got %d (err=%v)", scratchEntitlements, err)
	}
}

func TestGrantEntitlementACLsAfterRerun(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	for i := 0; i < 2; i++ {
		code, out := applyMigration011(t, bootURL)
		if code != 0 {
			t.Fatalf("rerun 011 ke-%d: exit %d (%s)", i+1, code, out)
		}
	}
	fn := "app.grant_module_entitlement(uuid, text, text, text)"
	boolCheck := func(q string, want bool, what string) {
		t.Helper()
		var got bool
		if err := boot.QueryRow(q).Scan(&got); err != nil {
			t.Fatalf("cek ACL %s: %v", what, err)
		}
		if got != want {
			t.Fatalf("cek ACL %s: want %v, got %v", what, want, got)
		}
	}
	boolCheck(fmt.Sprintf(`SELECT has_schema_privilege('umnia_grant_exec', 'app', 'CREATE')`), false, "owner CREATE app (harus dicabut)")
	boolCheck(fmt.Sprintf(`SELECT has_function_privilege('umnia_operator', '%s', 'EXECUTE')`, fn), true, "grup EXECUTE")
	boolCheck(fmt.Sprintf(`SELECT has_function_privilege('umnia_business', '%s', 'EXECUTE')`, fn), false, "runtime EXECUTE")
	// Admin yang tidak ada tidak punya hak apa pun — tetapi
	// has_function_privilege(nama, ...) melempar 42704 bila role tidak
	// ada (klaster business-only CI tidak membuat umnia_admin). Kueri
	// ini absent-safe tanpa mengandalkan urutan evaluasi AND: fungsi
	// hanya dipanggil dengan OID role yang benar-benar ada di pg_roles,
	// jadi masukan nama yang tidak ada tidak pernah mencapai fungsi.
	boolCheck(fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = 'umnia_admin' AND has_function_privilege(r.oid, '%s'::regprocedure, 'EXECUTE'))`, fn), false, "admin EXECUTE")
	boolCheck(`SELECT has_column_privilege('umnia_grant_exec', 'public.workspaces', 'id', 'SELECT')`, true, "owner SELECT workspaces.id")
	boolCheck(`SELECT has_column_privilege('umnia_grant_exec', 'public.workspaces', 'id', 'UPDATE')`, true, "owner UPDATE workspaces.id (kunci)")
	boolCheck(`SELECT has_column_privilege('umnia_grant_exec', 'public.workspaces', 'name', 'SELECT')`, false, "owner TIDAK punya SELECT workspaces.name (terbatas)")
	boolCheck(`SELECT has_table_privilege('umnia_business', 'public.operator_audit_events', 'SELECT')`, false, "runtime SELECT audit")
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.operator_audit_events', 'INSERT')`, true, "owner INSERT audit")
	var proacl string
	if err := boot.QueryRow(`SELECT proacl::text FROM pg_proc WHERE oid = 'app.grant_module_entitlement(uuid, text, text, text)'::regprocedure`).Scan(&proacl); err != nil {
		t.Fatalf("baca proacl rutin: %v", err)
	}
	if !strings.Contains(proacl, "umnia_operator=X/") {
		t.Fatalf("proacl rutin tidak memuat EXECUTE grup: %q", proacl)
	}
	// Entri PUBLIC berbentuk "=X/<grantor>" — grantee kosong di awal
	// entri (sesudah "{" atau ","). Entri pemilik
	// "umnia_grant_exec=X/..." mengandung "=X/" di tengah dan BUKAN
	// PUBLIC, jadi pemeriksaan substring polos salah positif.
	if strings.Contains(proacl, "{=X/") || strings.Contains(proacl, ",=X/") {
		t.Fatalf("proacl rutin memuat PUBLIC EXECUTE: %q", proacl)
	}
	// File 011 memuat validasi peran (guard terhadap penghapusan
	// tak sengaja). Negatif nyata (peran tidak ada) butuh klaster
	// tanpa peran operator — diuji reviewer Windows pada volume baru
	// sebelum provisioning; di sini ditutup jalur marker (rutin
	// memeriksa marker) + jalur scratch tanpa 011 di atas.
	raw, err := os.ReadFile(filepath.Join("migrations", "011_operator_entitlement.sql"))
	if err != nil {
		t.Fatalf("baca 011: %v", err)
	}
	for _, needle := range []string{"umnia_operator", "umnia_grant_exec", "RAISE EXCEPTION", "rolbypassrls", "pg_auth_members", "011_operator_entitlement"} {
		if !strings.Contains(string(raw), needle) {
			t.Fatalf("011 kehilangan penanda validasi %q", needle)
		}
	}
}

func TestGrantEntitlementRotationDisable(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, env.wsA)
	})

	// (a) Nonaktifkan keanggotaan: sesi operator yang SUDAH terbuka
	// harus ditolak pada pemanggilan berikutnya (guard per-call).
	opDB := openOperatorDB(t, opURL)
	t.Cleanup(func() {
		_, _ = boot.Exec(`GRANT umnia_operator TO ` + grantOpRole)
	})
	if _, err := boot.Exec(`REVOKE umnia_operator FROM ` + grantOpRole); err != nil {
		t.Fatalf("revoke membership: %v", err)
	}
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != grantExitInput || !strings.Contains(res.stderr, "not ready") {
		t.Fatalf("CLI sesudah disable: want exit 2 + not ready, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	var directErr error
	_, directErr = opDB.Exec(`SELECT * FROM app.grant_module_entitlement($1, 'rental.basic', '0.1.0', 'tes disable')`, env.wsA)
	if directErr == nil {
		t.Fatalf("panggilan rutin sesudah disable seharusnya ditolak")
	}
	// Kembalikan keanggotaan sebelum subtes rotasi.
	if _, err := boot.Exec(`GRANT umnia_operator TO ` + grantOpRole); err != nil {
		t.Fatalf("restore membership: %v", err)
	}

	// (b) Rotasi kata sandi: DSN lama ditolak 28P01, DSN baru bekerja.
	t.Cleanup(func() {
		_, _ = boot.Exec(`ALTER ROLE ` + grantOpRole + ` WITH PASSWORD '` + grantOpPassword + `'`)
	})
	newPW := "ci-operator-rotated-only"
	if _, err := boot.Exec(`ALTER ROLE ` + grantOpRole + ` WITH PASSWORD '` + newPW + `'`); err != nil {
		t.Fatalf("rotasi password: %v", err)
	}
	oldDB, err := sql.Open("pgx", opURL)
	if err != nil {
		t.Fatalf("open DSN lama: %v", err)
	}
	defer oldDB.Close()
	pingErr := oldDB.Ping()
	if pingErr == nil {
		t.Fatalf("DSN lama sesudah rotasi seharusnya ditolak")
	}
	var pgErr *pgconn.PgError
	if !errors.As(pingErr, &pgErr) || pgErr.Code != "28P01" {
		t.Fatalf("DSN lama: want 28P01, got %v", pingErr)
	}
	newURL := dsnWithPassword(t, opURL, newPW)
	res = runGrantCLI(t, bin, newURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 {
		t.Fatalf("CLI dengan password baru: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
}

func TestGrantEntitlementMigrationRejectsUnsafeRoles(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	// Setiap seed: terapkan 011 -> harus GAGAL tertutup; cabut seed ->
	// 011 lolos lagi (bukti kegagalan disebabkan seed itu, bukan file).
	// Subtes admin TIDAK ada di sini lagi: ia menyentuh akun admin
	// bersama klaster dev (GRANT umnia_grant_exec TO umnia_admin).
	// Cakupan admin pindah ke TestMigration011BusinessOnlyFreshSetup,
	// yang membuat peran umnia_admin sendiri di klaster sementara.
	seeds := []struct {
		name   string
		seed   string
		unseed string
	}{
		{"exec-owner-member", `GRANT umnia_grant_exec TO umnia_op_test`, `REVOKE umnia_grant_exec FROM umnia_op_test`},
		{"runtime-in-operator-group", `GRANT umnia_operator TO umnia_business`, `REVOKE umnia_operator FROM umnia_business`},
		{"operator-group-as-member", `GRANT umnia_business TO umnia_operator`, `REVOKE umnia_business FROM umnia_operator`},
	}
	for _, s := range seeds {
		t.Run(s.name, func(t *testing.T) {
			t.Cleanup(func() { _, _ = boot.Exec(s.unseed) })
			if _, err := boot.Exec(s.seed); err != nil {
				t.Fatalf("seed %s: %v", s.name, err)
			}
			code, out := applyMigration011(t, bootURL)
			if code == 0 {
				t.Fatalf("011 lolos dengan %s (harus gagal tertutup)", s.name)
			}
			if _, err := boot.Exec(s.unseed); err != nil {
				t.Fatalf("unseed %s: %v", s.name, err)
			}
			code, out = applyMigration011(t, bootURL)
			if code != 0 {
				t.Fatalf("011 sesudah unseed %s: exit %d (%s)", s.name, code, out)
			}
		})
	}
	// Anggota grup kapabilitas dengan atribut tidak aman (superuser):
	// juga ditolak, meski keanggotaannya sendiri sah.
	t.Run("superuser-member", func(t *testing.T) {
		t.Cleanup(func() {
			_, _ = boot.Exec(`REVOKE umnia_operator FROM umnia_op_bad`)
			_, _ = boot.Exec(`DROP ROLE IF EXISTS umnia_op_bad`)
		})
		if _, err := boot.Exec(`CREATE ROLE umnia_op_bad LOGIN SUPERUSER PASSWORD 'x-sentinel-tes-123'`); err != nil {
			t.Fatalf("buat role buruk: %v", err)
		}
		if _, err := boot.Exec(`GRANT umnia_operator TO umnia_op_bad`); err != nil {
			t.Fatalf("grant grup ke role buruk: %v", err)
		}
		code, out := applyMigration011(t, bootURL)
		if code == 0 {
			t.Fatalf("011 lolos dengan anggota superuser (harus gagal tertutup): %s", out)
		}
		if _, err := boot.Exec(`REVOKE umnia_operator FROM umnia_op_bad`); err != nil {
			t.Fatalf("revoke anggota buruk: %v", err)
		}
		if _, err := boot.Exec(`DROP ROLE umnia_op_bad`); err != nil {
			t.Fatalf("drop role buruk: %v", err)
		}
		code, out = applyMigration011(t, bootURL)
		if code != 0 {
			t.Fatalf("011 sesudah bersih: exit %d (%s)", code, out)
		}
	})
}

func TestMigration011AtomicLateFailure(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	scratch, scratchBoot := createOwnedScratchDB(t, boot, "umnia_test_atomic")
	scratchBootURL := dsnWithDB(t, bootURL, scratch)
	applyMigrationFiles(t, scratchBootURL, migrations001to010)
	// Salin 011 + gagal di AKHIR file: bila aplikasi tidak atomik,
	// semua pernyataan sebelum kegagalan sudah ter-commit.
	raw, err := os.ReadFile(filepath.Join("migrations", "011_operator_entitlement.sql"))
	if err != nil {
		t.Fatalf("baca 011: %v", err)
	}
	sabotagedPath := filepath.Join(t.TempDir(), "011_sabotaged.sql")
	if err := os.WriteFile(sabotagedPath, append(append([]byte{}, raw...), []byte("\nSELECT 1/0;\n")...), 0600); err != nil {
		t.Fatalf("tulis 011 sabotase: %v", err)
	}
	code, out := applyMigrationFile(t, scratchBootURL, sabotagedPath)
	if code == 0 {
		t.Fatalf("011 sabotase lolos (harus gagal): %s", out)
	}
	// State harus utuh: tanpa marker/tabel/rutin 011; 001-010 utuh.
	var marker011, markers010 int
	if err := scratchBoot.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = '011_operator_entitlement'`).Scan(&marker011); err != nil {
		t.Fatalf("cek marker 011: %v", err)
	}
	if marker011 != 0 {
		t.Fatalf("gagal di akhir: marker 011 ikut tertulis (want 0, got %d) — aplikasi tidak atomik", marker011)
	}
	var auditTable bool
	if err := scratchBoot.QueryRow(`SELECT to_regclass('public.operator_audit_events') IS NOT NULL`).Scan(&auditTable); err != nil {
		t.Fatalf("cek tabel audit: %v", err)
	}
	if auditTable {
		t.Fatalf("gagal di akhir: tabel operator_audit_events ikut tertulis — aplikasi tidak atomik")
	}
	var routine bool
	if err := scratchBoot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_proc WHERE proname = 'grant_module_entitlement' AND pronamespace = 'app'::regnamespace)`).Scan(&routine); err != nil {
		t.Fatalf("cek rutin: %v", err)
	}
	if routine {
		t.Fatalf("gagal di akhir: rutin ikut tertulis — aplikasi tidak atomik")
	}
	// Marker 011 sudah terbukti 0 di atas, jadi LIKE '0%' di sini
	// tepat menghitung 001-010 ('010_...' tidak cocok '00%').
	if err := scratchBoot.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version LIKE '0%'`).Scan(&markers010); err != nil {
		t.Fatalf("cek marker 001-010: %v", err)
	}
	if markers010 != 10 {
		t.Fatalf("gagal di akhir: marker 001-010 berubah (want 10, got %d)", markers010)
	}
	// Rerun 011 asli sesudah kegagalan: harus sukses (bukti tidak ada
	// state parsial yang menghalangi).
	code, out = applyMigration011(t, scratchBootURL)
	if code != 0 {
		t.Fatalf("rerun 011 sesudah gagal: exit %d (%s)", code, out)
	}
	if err := scratchBoot.QueryRow(`SELECT count(*) FROM schema_migrations WHERE version = '011_operator_entitlement'`).Scan(&marker011); err != nil || marker011 != 1 {
		t.Fatalf("rerun 011: marker want 1, got %d (err=%v)", marker011, err)
	}
}

func TestGrantEntitlementLegacyAuditProvenanceMismatch(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, env.wsA)
	})
	// Baris legacy (helper): granted_by 'operator:fixture', source
	// 'fixture', reason 'tes B3' — tanpa event audit dari grant itu.
	grantEntitlement(t, boot, env.wsA, "rental.basic", "0.1.0")
	var legacyAt string
	if err := boot.QueryRow(`SELECT granted_at::text FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&legacyAt); err != nil {
		t.Fatalf("baca granted_at legacy: %v", err)
	}
	// Event audit yang TIDAK cocok provenance-nya: workspace/modul/
	// versi/action sama, tapi aktor/source/reason/waktu BERBEDA —
	// ini event grant lain, bukan event grant asli baris itu.
	if _, err := boot.Exec(`INSERT INTO operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result, created_at) VALUES ($1, 'rental.basic', '0.1.0', $2, 'entitlement_granted', 'operator-cli', 'alasan lain', 'granted', $3::timestamptz + interval '1 day')`, env.wsA, grantOpRole, legacyAt); err != nil {
		t.Fatalf("sisip audit mismatch: %v", err)
	}
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", grantFixtureReason)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "legacy_existing workspace="+env.wsA+" ") {
		t.Fatalf("mismatch: want exit 0 + legacy_existing, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "audit_present=false") {
		t.Fatalf("mismatch: audit_present harus false: %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "granted_by=operator:fixture") {
		t.Fatalf("mismatch: aktor asli harus operator:fixture: %q", res.stdout)
	}
	if n := countOperatorAudit(t, boot, env.wsA); n != 1 {
		t.Fatalf("mismatch: want 1 baris audit (tanpa tulis baru), got %d", n)
	}
	var by, src, rsn string
	if err := boot.QueryRow(`SELECT granted_by, source, reason FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&by, &src, &rsn); err != nil {
		t.Fatalf("baca entitlement mismatch: %v", err)
	}
	if by != "operator:fixture" || src != "fixture" || rsn != "tes B3" {
		t.Fatalf("mismatch: baris legacy berubah (%q,%q,%q)", by, src, rsn)
	}
}

func TestGrantEntitlementCLI_MultibyteReason(t *testing.T) {
	_, boot, opURL := grantTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, env.wsA)
	})
	// 265 karakter, ~515 byte: di bawah batas 500 KARAKTER tapi di
	// atas 500 byte — harus lolos bila batas dihitung per karakter.
	reason := strings.Repeat("é", 250) + " alasan café ☕"
	if len(reason) <= 500 {
		t.Fatalf("fixture multibyte tidak melebihi 500 byte (got %d)", len(reason))
	}
	res := runGrantCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "rental.basic", "--version", "0.1.0", "--reason", reason)
	if res.code != 0 {
		t.Fatalf("grant multibyte: want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	assertCLIOutputClean(t, res, opURL, reason)
	var stored string
	if err := boot.QueryRow(`SELECT reason FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&stored); err != nil {
		t.Fatalf("baca reason: %v", err)
	}
	if stored != reason {
		t.Fatalf("reason multibyte tidak utuh: want %q, got %q", reason, stored)
	}
}

// TestConfigurePostgresOperatorNoPasswordArgv menjalankan skrip
// provisioning Linux SUNGGUHAN dengan psql/sudo/createdb PALSU:
// password sentinel harus sampai ke psql lewat stdin, TIDAK PERNAH
// lewat argv, dan tidak boleh muncul di diagnostik skrip.
func TestConfigurePostgresOperatorNoPasswordArgv(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash tidak ada di PATH (prasyarat tes)")
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "deploy", "linux", "configure-postgres.sh"))
	if err != nil {
		t.Fatalf("path skrip: %v", err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("skrip tidak ada: %v", err)
	}
	root := t.TempDir()
	migLink := filepath.Join(root, "services", "business-api", "migrations")
	if err := os.MkdirAll(filepath.Dir(migLink), 0755); err != nil {
		t.Fatalf("mkdir tiruan: %v", err)
	}
	realMig, err := filepath.Abs("migrations")
	if err != nil {
		t.Fatalf("path migrasi: %v", err)
	}
	if err := os.Symlink(realMig, migLink); err != nil {
		t.Fatalf("symlink migrasi: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0755); err != nil {
		t.Fatalf("mkdir deploy tiruan: %v", err)
	}
	sentinelOp := "SENTINEL-op-argv-987654321"
	sentinelBisnis := "SENTINEL-bisnis-argv-987654321"
	sentinelAdmin := "SENTINEL-admin-argv-987654321"
	if err := os.WriteFile(filepath.Join(root, "deploy", "operator-roster.env"), []byte("UMNIA_OP_SENTINEL="+sentinelOp+"\n"), 0600); err != nil {
		t.Fatalf("tulis roster sentinel: %v", err)
	}
	fakeBin := filepath.Join(root, "bin")
	if err := os.MkdirAll(fakeBin, 0755); err != nil {
		t.Fatalf("mkdir fake bin: %v", err)
	}
	argvCap := filepath.Join(root, "argv.txt")
	stdinCap := filepath.Join(root, "stdin.txt")
	fakePsql := "#!/bin/sh\n" +
		"printf '%s\\n' \"$@\" >> \"$FAKE_PSQL_ARGV\"\n" +
		"for a in \"$@\"; do case \"$a\" in -tAc|-lqt) exit 0;; esac; done\n" +
		"cat >> \"$FAKE_PSQL_STDIN\"\n" +
		"exit 0\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "psql"), []byte(fakePsql), 0755); err != nil {
		t.Fatalf("tulis fake psql: %v", err)
	}
	fakeSudo := "#!/bin/sh\nif [ \"$1\" = \"-u\" ]; then shift 2; fi\nexec \"$@\"\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "sudo"), []byte(fakeSudo), 0755); err != nil {
		t.Fatalf("tulis fake sudo: %v", err)
	}
	fakeCreatedb := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> \"$FAKE_PSQL_ARGV\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(fakeBin, "createdb"), []byte(fakeCreatedb), 0755); err != nil {
		t.Fatalf("tulis fake createdb: %v", err)
	}
	cmd := exec.Command(bash, script)
	cmd.Dir = root
	env := []string{"PATH=" + fakeBin + ":" + os.Getenv("PATH")}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") || strings.HasPrefix(kv, "UMNIA_BUSINESS_DB_PASSWORD=") || strings.HasPrefix(kv, "UMNIA_ADMIN_DB_PASSWORD=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"UMNIA_BUSINESS_DB_PASSWORD="+sentinelBisnis,
		"UMNIA_ADMIN_DB_PASSWORD="+sentinelAdmin,
		"FAKE_PSQL_ARGV="+argvCap,
		"FAKE_PSQL_STDIN="+stdinCap,
	)
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("skrip provisioning: %v (out=%q)", err, out.String())
	}
	argv, err := os.ReadFile(argvCap)
	if err != nil {
		t.Fatalf("baca argv capture: %v", err)
	}
	for _, s := range []string{sentinelOp, sentinelBisnis, sentinelAdmin} {
		if strings.Contains(string(argv), s) {
			t.Fatalf("password sentinel muncul di argv psql/createdb: %q", s)
		}
	}
	stdin, err := os.ReadFile(stdinCap)
	if err != nil {
		t.Fatalf("baca stdin capture: %v", err)
	}
	if !strings.Contains(string(stdin), sentinelOp) {
		t.Fatalf("password operator TIDAK sampai ke psql lewat stdin (mekanisme rusak)")
	}
	if !strings.Contains(string(stdin), sentinelBisnis) || !strings.Contains(string(stdin), sentinelAdmin) {
		t.Fatalf("password bisnis/admin TIDAK sampai ke psql lewat stdin")
	}
	for _, s := range []string{sentinelOp, sentinelBisnis, sentinelAdmin} {
		if strings.Contains(out.String(), s) {
			t.Fatalf("diagnostik skrip membocorkan password sentinel: %q", s)
		}
	}
}

// ---- Klaster sementara untuk bukti business-only (F2) ----

// pgBinDir mencari direktori bin PostgreSQL 16 (initdb/pg_ctl/psql).
// Prasyarat yang hilang adalah GAGAL, bukan dilewati (bukan t.Skip:
// anti-skip gate CI menghitung SKIP sebagai kegagalan, dan tiket
// menuntut prasyarat hilang = FAIL/NOT RUN, bukan PASS yang dilewati).
func pgBinDir(t *testing.T) string {
	t.Helper()
	for _, dir := range []string{"/usr/lib/postgresql/16/bin", "/usr/lib/postgresql/15/bin", "/usr/lib/postgresql/14/bin"} {
		if st, err := os.Stat(filepath.Join(dir, "initdb")); err == nil && !st.IsDir() {
			return dir
		}
	}
	if p, err := exec.LookPath("initdb"); err == nil {
		return filepath.Dir(p)
	}
	t.Fatalf("initdb PostgreSQL tidak ditemukan (cari /usr/lib/postgresql/16/bin atau PATH) — prasyarat tes klaster sementara")
	return ""
}

// freeTCPPort memilih port TCP bebas di 127.0.0.1.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("cari port bebas: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// tempCluster adalah klaster PostgreSQL sementara (initdb + pg_ctl,
// trust localhost, port acak). Seluruh state hidup di datadir
// sementara; cleanup menghentikan klaster dan menghapus datadir.
// Klaster utama/dev TIDAK disentuh.
type tempCluster struct {
	dir  string
	port int
	log  string
}

// tempClusterURL membangun DSN TCP trust ke DB tertentu di klaster.
func (c *tempCluster) tempClusterURL(db string) string {
	return fmt.Sprintf("postgres://postgres@127.0.0.1:%d/%s?sslmode=disable", c.port, db)
}

// tempPsql menjalankan psql asli (TCP, trust) terhadap klaster
// sementara; mengembalikan stdout+stderr gabungan dan exit code.
func (c *tempCluster) tempPsql(t *testing.T, dbURL string, args ...string) (int, string) {
	t.Helper()
	psql, err := exec.LookPath("psql")
	if err != nil {
		t.Fatalf("psql tidak ada di PATH (prasyarat tes)")
	}
	cmd := exec.Command(psql, append([]string{dbURL}, args...)...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("jalankan psql klaster sementara: %v", err)
		}
	}
	return code, out.String()
}

// tempPsqlSQL menjalankan satu pernyataan SQL lewat psql -tAc.
func (c *tempCluster) tempPsqlSQL(t *testing.T, db, sql string) (int, string) {
	t.Helper()
	return c.tempPsql(t, c.tempClusterURL(db), "-v", "ON_ERROR_STOP=1", "-tAc", sql)
}

// startTempCluster membuat klaster sementara dan menunggu siap.
// Gagal bila sudo/initdb/pg_ctl tidak tersedia atau klaster tidak
// menyala — prasyarat hilang adalah FAIL, bukan dilewati.
func startTempCluster(t *testing.T) *tempCluster {
	t.Helper()
	binDir := pgBinDir(t)
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		t.Fatalf("sudo tidak ada di PATH (prasyarat klaster sementara)")
	}
	if out, err := exec.Command(sudo, "-n", "true").CombinedOutput(); err != nil {
		t.Fatalf("sudo tanpa kata sandi tidak tersedia: %v (%s)", err, out)
	}
	if out, err := exec.Command(sudo, "-u", "postgres", "true").CombinedOutput(); err != nil {
		t.Fatalf("sudo -u postgres tidak tersedia (user postgres?): %v (%s)", err, out)
	}
	parent, err := os.MkdirTemp("", "umnia-tempcluster-")
	if err != nil {
		t.Fatalf("buat dir klaster sementara: %v", err)
	}
	// Parent 0777: initdb/pg_ctl berjalan sebagai user postgres dan
	// harus bisa membuat datadir + log di dalamnya (0700 root-owned
	// akan ditolak Permission denied). Datadir sendiri dibuat initdb
	// dan dimiliki postgres; cleanup (root) tetap bisa menghapusnya.
	if err := os.Chmod(parent, 0777); err != nil {
		t.Fatalf("chmod dir klaster sementara: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(parent) })
	dir := filepath.Join(parent, "data")
	log := filepath.Join(parent, "postgres.log")
	port := freeTCPPort(t)
	c := &tempCluster{dir: dir, port: port, log: log}
	t.Cleanup(func() { c.stop(t) })
	run := func(name string, args ...string) string {
		cmd := exec.Command(name, args...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		if err := cmd.Run(); err != nil {
			t.Fatalf("klaster sementara %s %v: %v (%s)", name, args, err, out.String())
		}
		return out.String()
	}
	run(sudo, "-u", "postgres", filepath.Join(binDir, "initdb"), "-D", dir, "-E", "UTF8", "--no-instructions")
	// Trust localhost saja (klaster tes, port acak, dihapus sesudah
	// tes): tanpa ini psql TCP butuh kata sandi SCRAM.
	hba := "local all all trust\nhost all all 127.0.0.1/32 trust\nhost all all ::1/128 trust\n"
	hbaCmd := exec.Command(sudo, "-u", "postgres", "tee", filepath.Join(dir, "pg_hba.conf"))
	hbaCmd.Stdin = strings.NewReader(hba)
	var hbaOut bytes.Buffer
	hbaCmd.Stdout, hbaCmd.Stderr = &hbaOut, &hbaOut
	if err := hbaCmd.Run(); err != nil {
		t.Fatalf("tulis pg_hba.conf klaster sementara: %v (%s)", err, hbaOut.String())
	}
	run(sudo, "-u", "postgres", filepath.Join(binDir, "pg_ctl"), "-D", dir,
		"-o", fmt.Sprintf("-p %d -c listen_addresses=127.0.0.1", port),
		"-l", log, "start")
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, _ := c.tempPsql(t, c.tempClusterURL("postgres"), "-tAc", "SELECT 1")
		if code == 0 {
			return c
		}
		if time.Now().After(deadline) {
			logBytes, _ := os.ReadFile(log)
			t.Fatalf("klaster sementara tidak siap dalam 30 detik (log: %s)", logBytes)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// stop menghentikan klaster sementara (dipanggil cleanup; best-effort).
func (c *tempCluster) stop(t *testing.T) {
	t.Helper()
	binDir := ""
	for _, dir := range []string{"/usr/lib/postgresql/16/bin", "/usr/lib/postgresql/15/bin", "/usr/lib/postgresql/14/bin"} {
		if st, err := os.Stat(filepath.Join(dir, "pg_ctl")); err == nil && !st.IsDir() {
			binDir = dir
			break
		}
	}
	if binDir == "" {
		return
	}
	sudo, err := exec.LookPath("sudo")
	if err != nil {
		return
	}
	_ = exec.Command(sudo, "-u", "postgres", filepath.Join(binDir, "pg_ctl"), "-D", c.dir, "-m", "fast", "stop").Run()
}

// TestMigration011BusinessOnlyFreshSetup membuktikan 011 diterapkan
// pada klaster segar yang HANYA punya peran bisnis (tanpa umnia_admin)
// — setup persis daftar peran CI segar dan klaster bisnis Windows.
// 011 lama gagal di sini: 'umnia_admin'::regrole pada §0 melempar
// ERROR: role "umnia_admin" does not exist. Bukti ini TIDAK bisa
// memakai klaster dev (umnia_admin selalu ada di sana), jadi tes ini
// memakai klaster sementara yang dibuat dan dihapus tes ini sendiri.
// Subtes admin membuktikan peran admin yang ADA dan tidak aman tetap
// ditolak — memakai peran umnia_admin yang dibuat tes ini di klaster
// sementara (bukan akun admin klaster dev yang dipakai bersama).
func TestMigration011BusinessOnlyFreshSetup(t *testing.T) {
	c := startTempCluster(t)
	bizURL := c.tempClusterURL("umnia_business")
	setup := []string{
		`CREATE ROLE umnia_business NOSUPERUSER NOBYPASSRLS LOGIN PASSWORD 'ci-runtime-only'`,
		`CREATE ROLE umnia_operator NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION`,
		`CREATE ROLE umnia_grant_exec NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION`,
		`CREATE ROLE umnia_op_test NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION LOGIN PASSWORD 'ci-operator-only'`,
		`GRANT umnia_operator TO umnia_op_test`,
		`CREATE DATABASE umnia_business OWNER umnia_business`,
	}
	for _, sql := range setup {
		if code, out := c.tempPsqlSQL(t, "postgres", sql); code != 0 {
			t.Fatalf("setup peran business-only: exit %d (%s) untuk %q", code, out, sql)
		}
	}
	// Bukti 1: 001-011 lolos pada klaster tanpa umnia_admin.
	files := append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql")
	applyMigrationFiles(t, bizURL, files)
	var marker int
	if code, out := c.tempPsqlSQL(t, "umnia_business", `SELECT count(*) FROM schema_migrations WHERE version = '011_operator_entitlement'`); code != 0 {
		t.Fatalf("cek marker 011: exit %d (%s)", code, out)
	} else if _, err := fmt.Sscanf(strings.TrimSpace(out), "%d", &marker); err != nil || marker != 1 {
		t.Fatalf("marker 011: want 1, got %q (err %v)", out, err)
	}
	if code, out := c.tempPsqlSQL(t, "umnia_business", `SELECT has_function_privilege('umnia_op_test', 'app.grant_module_entitlement(uuid, text, text, text)'::regprocedure, 'EXECUTE')`); code != 0 || strings.TrimSpace(out) != "t" {
		t.Fatalf("EXECUTE rutin untuk umnia_op_test: exit %d (%s)", code, out)
	}
	// Bukti 2: peran admin yang ADA dan menjadi anggota grup operator
	// tetap ditolak — peran ini dibuat dan dimiliki tes ini, di klaster
	// sementara; akun admin klaster dev tidak disentuh. Seed memakai
	// keanggotaan umnia_operator (bukan grant_exec): anggota pemilik
	// rutin sudah ditangkap lebih dulu oleh pemeriksaan §0 yang lebih
	// ketat ("tidak ada peran yang boleh menjadi anggota
	// umnia_grant_exec"), sehingga pesan admin spesifik hanya muncul
	// pada jalur grup operator.
	if code, out := c.tempPsqlSQL(t, "postgres", `CREATE ROLE umnia_admin NOSUPERUSER NOBYPASSRLS LOGIN PASSWORD 'x-admin-fixture-123'`); code != 0 {
		t.Fatalf("buat peran admin fixture: exit %d (%s)", code, out)
	}
	t.Cleanup(func() { _, _ = c.tempPsqlSQL(t, "postgres", `DROP ROLE IF EXISTS umnia_admin`) })
	if code, out := c.tempPsqlSQL(t, "postgres", `GRANT umnia_operator TO umnia_admin`); code != 0 {
		t.Fatalf("seed admin ke grup operator: exit %d (%s)", code, out)
	}
	if code, out := applyMigration011(t, bizURL); code == 0 {
		t.Fatalf("011 lolos dengan umnia_admin anggota grup operator (harus gagal tertutup)")
	} else if !strings.Contains(out, "umnia_business/umnia_admin") {
		t.Fatalf("011 gagal dengan pesan yang salah (want penolakan admin): exit %d (%s)", code, out)
	}
	if code, out := c.tempPsqlSQL(t, "postgres", `REVOKE umnia_operator FROM umnia_admin`); code != 0 {
		t.Fatalf("unseed admin: exit %d (%s)", code, out)
	}
	if code, out := applyMigration011(t, bizURL); code != 0 {
		t.Fatalf("011 sesudah unseed admin: exit %d (%s)", code, out)
	}
}

// ---- Preflight operator (F1) ----

// snapshotOperatorState mengembalikan ringkasan kanonis state operator
// yang relevan dengan preflight: atribut semua peran operator/runtime,
// keanggotaan (pg_auth_members) yang melibatkan peran itu, dan objek
// yang dimiliki peran itu di database koneksi ini. Dipakai untuk
// membuktikan preflight yang menolak TIDAK mengubah apa pun
// (flag/keanggotaan/aset tetap identik). CATATAN: rolpassword TIDAK
// ada di sini — pg_roles.rolpassword selalu termask '********' bagi
// non-superuser sehingga tidak bisa mendeteksi perubahan password;
// bukti kredensial memakai captureVerifiers (pg_authid) secara terpisah.
func snapshotOperatorState(t *testing.T, db *sql.DB) string {
	t.Helper()
	var sb strings.Builder
	rows, err := db.Query(`SELECT rolname, rolcanlogin, rolsuper, rolbypassrls, rolcreaterole, rolcreatedb, rolreplication
		FROM pg_roles WHERE rolname LIKE 'umnia_op_%' OR rolname IN ('umnia_operator', 'umnia_grant_exec', 'umnia_business', 'umnia_admin') ORDER BY rolname`)
	if err != nil {
		t.Fatalf("snapshot peran: %v", err)
	}
	for rows.Next() {
		var name string
		var login, super, bypass, cr, cdb, repl bool
		if err := rows.Scan(&name, &login, &super, &bypass, &cr, &cdb, &repl); err != nil {
			t.Fatalf("snapshot peran scan: %v", err)
		}
		fmt.Fprintf(&sb, "role %s login=%v super=%v bypass=%v cr=%v cdb=%v repl=%v\n", name, login, super, bypass, cr, cdb, repl)
	}
	rows.Close()
	mrows, err := db.Query(`SELECT mr.rolname, gr.rolname FROM pg_auth_members m
		JOIN pg_roles mr ON mr.oid = m.member JOIN pg_roles gr ON gr.oid = m.roleid
		WHERE mr.rolname LIKE 'umnia_op_%' OR mr.rolname IN ('umnia_operator', 'umnia_grant_exec', 'umnia_business', 'umnia_admin')
		   OR gr.rolname LIKE 'umnia_op_%' OR gr.rolname IN ('umnia_operator', 'umnia_grant_exec', 'umnia_business', 'umnia_admin')
		ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("snapshot keanggotaan: %v", err)
	}
	for mrows.Next() {
		var member, granted string
		if err := mrows.Scan(&member, &granted); err != nil {
			t.Fatalf("snapshot keanggotaan scan: %v", err)
		}
		fmt.Fprintf(&sb, "member %s -> %s\n", member, granted)
	}
	mrows.Close()
	orows, err := db.Query(`SELECT 'proc', n.nspname, p.proname FROM pg_proc p JOIN pg_roles r ON r.oid = p.proowner JOIN pg_namespace n ON n.oid = p.pronamespace
		WHERE r.rolname LIKE 'umnia_op_%' OR r.rolname IN ('umnia_operator', 'umnia_grant_exec')
		UNION ALL SELECT 'class', n.nspname, c.relname FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE r.rolname LIKE 'umnia_op_%' OR r.rolname IN ('umnia_operator', 'umnia_grant_exec')
		UNION ALL SELECT 'nsp', '', n.nspname FROM pg_namespace n JOIN pg_roles r ON r.oid = n.nspowner
		WHERE r.rolname LIKE 'umnia_op_%' OR r.rolname IN ('umnia_operator', 'umnia_grant_exec')
		UNION ALL SELECT 'db', '', d.datname FROM pg_database d JOIN pg_roles r ON r.oid = d.datdba
		WHERE r.rolname LIKE 'umnia_op_%' OR r.rolname IN ('umnia_operator', 'umnia_grant_exec')
		ORDER BY 1, 2, 3`)
	if err != nil {
		t.Fatalf("snapshot kepemilikan: %v", err)
	}
	for orows.Next() {
		var kind, nsp, name string
		if err := orows.Scan(&kind, &nsp, &name); err != nil {
			t.Fatalf("snapshot kepemilikan scan: %v", err)
		}
		fmt.Fprintf(&sb, "owns %s %s %s\n", kind, nsp, name)
	}
	orows.Close()
	return sb.String()
}

// captureVerifiers membaca password verifier ASLI (hash SCRAM) semua
// peran operator/runtime dari pg_authid. HARUS dipanggil lewat koneksi
// superuser (bootstrap) — pg_authid hanya bisa dibaca superuser, dan
// TIDAK ada hak baca verifier yang diberikan ke peran app/operator.
// Nilai hash TIDAK PERNAH dicetak/dilog/diformat ke string snapshot:
// peta ini hanya dibandingkan di memori oleh assertVerifiersUnchanged,
// yang melaporkan NAMA peran saja bila ada perbedaan.
func captureVerifiers(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, err := db.Query(`SELECT rolname, COALESCE(rolpassword, '') FROM pg_authid
		WHERE rolname LIKE 'umnia_op_%' OR rolname IN ('umnia_operator', 'umnia_grant_exec', 'umnia_business', 'umnia_admin') ORDER BY rolname`)
	if err != nil {
		t.Fatalf("baca verifier pg_authid: %v", err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, verifier string
		if err := rows.Scan(&name, &verifier); err != nil {
			t.Fatalf("baca verifier scan: %v", err)
		}
		out[name] = verifier
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("baca verifier rows: %v", err)
	}
	return out
}

// assertVerifiersUnchanged membuktikan tidak ada verifier password
// yang berubah antara dua tangkapan. Pesan gagal hanya menyebut NAMA
// peran (berubah/hilang/muncul) — nilai hash tidak pernah dicetak.
func assertVerifiersUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	var changed []string
	for name, v := range before {
		av, ok := after[name]
		if !ok {
			changed = append(changed, name+" (hilang)")
			continue
		}
		if av != v {
			changed = append(changed, name+" (berubah)")
		}
	}
	for name := range after {
		if _, ok := before[name]; !ok {
			changed = append(changed, name+" (baru)")
		}
	}
	if len(changed) > 0 {
		sort.Strings(changed)
		t.Fatalf("verifier password BERUBAH sesudah preflight menolak (hanya nama peran): %s", strings.Join(changed, ", "))
	}
}

// runRealScript menjalankan skrip shell asli (bash) dengan env dan
// direktori kerja yang diberikan; mengembalikan exit code + output.
func runRealScript(t *testing.T, script, dir string, env []string) (int, string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatalf("bash tidak ada di PATH (prasyarat tes)")
	}
	cmd := exec.Command(bash, script)
	cmd.Dir = dir
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("jalankan skrip %s: %v", script, err)
		}
	}
	return code, out.String()
}

// mimicRepoRoot membuat tiruan root repo untuk menjalankan
// configure-postgres.sh: symlink migrasi + roster sentinel.
// Mengembalikan direktori tiruan (dihapus cleanup).
func mimicRepoRoot(t *testing.T, rosterContent string) string {
	t.Helper()
	root := t.TempDir()
	migLink := filepath.Join(root, "services", "business-api", "migrations")
	if err := os.MkdirAll(filepath.Dir(migLink), 0755); err != nil {
		t.Fatalf("mkdir tiruan: %v", err)
	}
	realMig, err := filepath.Abs("migrations")
	if err != nil {
		t.Fatalf("path migrasi: %v", err)
	}
	if err := os.Symlink(realMig, migLink); err != nil {
		t.Fatalf("symlink migrasi: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "deploy"), 0755); err != nil {
		t.Fatalf("mkdir deploy tiruan: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "deploy", "operator-roster.env"), []byte(rosterContent), 0600); err != nil {
		t.Fatalf("tulis roster sentinel: %v", err)
	}
	return root
}

// cleanEnvForScript membangun env untuk skrip provisioning: PATH
// normal + dua password sentinel (tidak pernah diterapkan karena
// preflight gagal dulu) + tambahan yang diberikan.
func cleanEnvForScript(t *testing.T, extra ...string) []string {
	t.Helper()
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "UMNIA_BUSINESS_DB_PASSWORD=") || strings.HasPrefix(kv, "UMNIA_ADMIN_DB_PASSWORD=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"UMNIA_BUSINESS_DB_PASSWORD=SENTINEL-bisnis-preflight-987654321",
		"UMNIA_ADMIN_DB_PASSWORD=SENTINEL-admin-preflight-987654321",
	)
	return append(env, extra...)
}

// TestPreflightVerifierDetectsPasswordChange adalah kontrol mutasi
// untuk Issue 3: membuktikan captureVerifiers/assertVerifiersUnchanged
// BENAR-BENAR mendeteksi perubahan password (bukan pemeriksaan vakum
// seperti pg_roles.rolpassword yang selalu '********'). Login uji
// dibuat dan dihapus tes ini; kontrol yang tidak diubah harus tetap
// sama (tanpa false positive). Nilai hash tidak pernah dicetak.
// (SCRAM memakai salt acak tiap ALTER, jadi memulihkan password yang
// sama TIDAK mengembalikan hash yang sama — tidak ada pemeriksaan
// "kembali sama" di sini.)
func TestPreflightVerifierDetectsPasswordChange(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	suffix := uniqueSuffix(t)
	login := "umnia_op_verifier_" + suffix
	control := "umnia_op_verifier_ctl_" + suffix
	for _, role := range []string{login, control} {
		if _, err := boot.Exec(`CREATE ROLE ` + role + ` LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-verifier-awal-123'`); err != nil {
			t.Fatalf("buat login %s: %v", role, err)
		}
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + login)
		_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + control)
	})
	before := captureVerifiers(t, boot)
	// Mutasi yang disengaja: ubah password login uji SAJA.
	if _, err := boot.Exec(`ALTER ROLE ` + login + ` WITH PASSWORD 'x-verifier-ubah-456'`); err != nil {
		t.Fatalf("ubah password login uji: %v", err)
	}
	after := captureVerifiers(t, boot)
	if after[login] == before[login] {
		t.Fatalf("kontrol mutasi GAGAL: perubahan password login %s TIDAK terdeteksi (verifier sama)", login)
	}
	if after[control] != before[control] {
		t.Fatalf("false positive: verifier kontrol %s berubah padahal password tidak diubah", control)
	}
	if _, err := boot.Exec(`DROP ROLE ` + login); err != nil {
		t.Fatalf("drop login uji: %v", err)
	}
	if _, err := boot.Exec(`DROP ROLE ` + control); err != nil {
		t.Fatalf("drop login kontrol: %v", err)
	}
}

// TestConfigurePostgresPreflightRejectsUnsafeRoles membuktikan
// configure-postgres.sh MENOLAK (bukan memperbaiki) state operator
// yang tidak aman, dan membuktikan state asli tidak berubah. Setiap
// subtes: seed tidak aman -> jalankan skrip ASLI -> harus gagal
// dengan pesan preflight yang disanitasi -> snapshot sebelum/sesudah
// harus identik -> cabut seed. Skrip dijalankan dengan sudo/psql
// ASLI terhadap klaster dev (bukan fake): preflight gagal sebelum
// loop migrasi, jadi tidak ada migrasi yang diterapkan ulang di sini.
func TestConfigurePostgresPreflightRejectsUnsafeRoles(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	script, err := filepath.Abs(filepath.Join("..", "..", "deploy", "linux", "configure-postgres.sh"))
	if err != nil {
		t.Fatalf("path skrip: %v", err)
	}
	// Koneksi ke umnia_business untuk snapshot kepemilikan di sana.
	bizURL := strings.Replace(bootURL, "/umnia_test?", "/umnia_business?", 1)
	bizDB, err := sql.Open("pgx", bizURL)
	if err != nil {
		t.Fatalf("buka umnia_business: %v", err)
	}
	t.Cleanup(func() { bizDB.Close() })
	if err := bizDB.Ping(); err != nil {
		t.Fatalf("ping umnia_business: %v", err)
	}
	suffix := uniqueSuffix(t)
	login := "umnia_op_preflight_" + suffix
	if _, err := boot.Exec(`CREATE ROLE ` + login + ` LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-preflight-sentinel-123'`); err != nil {
		t.Fatalf("buat login preflight: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`REVOKE umnia_operator FROM ` + login)
		_, _ = boot.Exec(`REVOKE umnia_grant_exec FROM ` + login)
		_, _ = boot.Exec(`DROP TABLE IF EXISTS preflight_owned`)
		_, _ = bizDB.Exec(`DROP TABLE IF EXISTS preflight_owned`)
		_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + login)
	})
	roster := "UMNIA_OP_PREFLIGHT_" + strings.ToUpper(suffix) + "=x-preflight-sentinel-123\n"
	root := mimicRepoRoot(t, roster)
	run := func() (int, string) {
		return runRealScript(t, script, root, cleanEnvForScript(t))
	}
	assertUnchanged := func(before string, verifiersBefore map[string]string) {
		t.Helper()
		after := snapshotOperatorState(t, bizDB)
		if after != before {
			t.Fatalf("state operator BERUBAH sesudah preflight menolak:\nsebelum:\n%s\nsesudah:\n%s", before, after)
		}
		assertVerifiersUnchanged(t, verifiersBefore, captureVerifiers(t, boot))
	}
	t.Run("exec-owner-member", func(t *testing.T) {
		if _, err := boot.Exec(`GRANT umnia_grant_exec TO ` + login); err != nil {
			t.Fatalf("seed: %v", err)
		}
		t.Cleanup(func() { _, _ = boot.Exec(`REVOKE umnia_grant_exec FROM ` + login) })
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan login anggota pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		if strings.Contains(out, "x-preflight-sentinel-123") {
			t.Fatalf("diagnostik membocorkan password sentinel")
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := boot.Exec(`REVOKE umnia_grant_exec FROM ` + login); err != nil {
			t.Fatalf("unseed: %v", err)
		}
	})
	t.Run("superuser-member", func(t *testing.T) {
		bad := "umnia_op_preflight_bad_" + suffix
		if _, err := boot.Exec(`CREATE ROLE ` + bad + ` LOGIN SUPERUSER PASSWORD 'x-preflight-bad-123'`); err != nil {
			t.Fatalf("buat role buruk: %v", err)
		}
		t.Cleanup(func() {
			_, _ = boot.Exec(`REVOKE umnia_operator FROM ` + bad)
			_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + bad)
		})
		if _, err := boot.Exec(`GRANT umnia_operator TO ` + bad); err != nil {
			t.Fatalf("seed grup ke role buruk: %v", err)
		}
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan anggota grup superuser (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := boot.Exec(`REVOKE umnia_operator FROM ` + bad); err != nil {
			t.Fatalf("unseed: %v", err)
		}
		if _, err := boot.Exec(`DROP ROLE ` + bad); err != nil {
			t.Fatalf("drop role buruk: %v", err)
		}
	})
	t.Run("owned-table", func(t *testing.T) {
		if _, err := bizDB.Exec(`CREATE TABLE preflight_owned (id int)`); err != nil {
			t.Fatalf("buat tabel seed: %v", err)
		}
		if _, err := bizDB.Exec(`ALTER TABLE preflight_owned OWNER TO ` + login); err != nil {
			t.Fatalf("alihkan pemilik tabel: %v", err)
		}
		t.Cleanup(func() { _, _ = bizDB.Exec(`DROP TABLE IF EXISTS preflight_owned`) })
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan tabel milik login operator (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		var owner string
		if err := bizDB.QueryRow(`SELECT rolname FROM pg_class c JOIN pg_roles r ON r.oid = c.relowner WHERE c.relname = 'preflight_owned'`).Scan(&owner); err != nil || owner != login {
			t.Fatalf("tabel seed hilang/berpindah pemilik sesudah preflight: owner=%q err=%v", owner, err)
		}
		if _, err := bizDB.Exec(`DROP TABLE preflight_owned`); err != nil {
			t.Fatalf("drop tabel seed: %v", err)
		}
	})
	t.Run("table-privilege", func(t *testing.T) {
		if _, err := bizDB.Exec(`GRANT SELECT ON workspaces TO ` + login); err != nil {
			t.Fatalf("seed hak tabel: %v", err)
		}
		t.Cleanup(func() { _, _ = bizDB.Exec(`REVOKE SELECT ON workspaces FROM ` + login) })
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan login berh hak tabel (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := bizDB.Exec(`REVOKE SELECT ON workspaces FROM ` + login); err != nil {
			t.Fatalf("unseed hak tabel: %v", err)
		}
	})
	t.Run("custom-parent", func(t *testing.T) {
		// Parent kustom NOLOGIN yang tampak aman + hak kolom yang
		// diwarisi: login TIDAK punya hak tabel/kolom langsung, tapi
		// has_column_privilege(login, workspaces.id) = true lewat
		// pewarisan. Pemeriksaan lama (blacklist nama parent +
		// has_table_privilege + attacl LIKE login) lolos dari state
		// ini; allowlist parent (hanya umnia_operator) harus menolak.
		parent := "umnia_op_parent_" + suffix
		if _, err := boot.Exec(`CREATE ROLE ` + parent + ` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION`); err != nil {
			t.Fatalf("buat parent kustom: %v", err)
		}
		t.Cleanup(func() {
			_, _ = bizDB.Exec(`REVOKE SELECT (id) ON workspaces FROM ` + parent)
			_, _ = boot.Exec(`REVOKE ` + parent + ` FROM ` + login)
			_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + parent)
		})
		if _, err := bizDB.Exec(`GRANT SELECT (id) ON workspaces TO ` + parent); err != nil {
			t.Fatalf("seed hak kolom ke parent: %v", err)
		}
		if _, err := boot.Exec(`GRANT ` + parent + ` TO ` + login); err != nil {
			t.Fatalf("seed parent ke login: %v", err)
		}
		var colPriv bool
		if err := bizDB.QueryRow(`SELECT has_column_privilege($1, 'public.workspaces', 'id', 'SELECT')`, login).Scan(&colPriv); err != nil || !colPriv {
			t.Fatalf("prasyarat celah: has_column_privilege login = %v err=%v (harus true)", colPriv, err)
		}
		var tablePriv bool
		if err := bizDB.QueryRow(`SELECT has_table_privilege($1, 'public.workspaces', 'SELECT')`, login).Scan(&tablePriv); err != nil {
			t.Fatalf("cek has_table_privilege: %v", err)
		}
		if tablePriv {
			t.Fatalf("prasyarat celah: has_table_privilege login = true (harus false — celah tidak ada)")
		}
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan login anggota parent kustom berh hak kolom (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		if !strings.Contains(out, "hanya boleh menjadi anggota umnia_operator") {
			t.Fatalf("pesan penolakan bukan allowlist parent: (%s)", out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := bizDB.Exec(`REVOKE SELECT (id) ON workspaces FROM ` + parent); err != nil {
			t.Fatalf("unseed hak kolom: %v", err)
		}
		if _, err := boot.Exec(`REVOKE ` + parent + ` FROM ` + login); err != nil {
			t.Fatalf("unseed parent: %v", err)
		}
		if _, err := boot.Exec(`DROP ROLE ` + parent); err != nil {
			t.Fatalf("drop parent: %v", err)
		}
	})
	t.Run("hostile-overload", func(t *testing.T) {
		// Overload musuh: nama rutin Finance sama, tanda tangan lain
		// (satu argumen), pemilik umnia_grant_exec. Pemeriksaan lama
		// (nama saja) lolos dari state ini; allowlist tanda tangan
		// eksak (011 + 014) harus menolak.
		if _, err := bizDB.Exec(`CREATE FUNCTION app.grant_finance_entitlement(uuid) RETURNS void LANGUAGE sql AS 'SELECT 1'`); err != nil {
			t.Fatalf("buat overload musuh: %v", err)
		}
		if _, err := bizDB.Exec(`ALTER FUNCTION app.grant_finance_entitlement(uuid) OWNER TO umnia_grant_exec`); err != nil {
			t.Fatalf("alihkan overload musuh: %v", err)
		}
		t.Cleanup(func() { _, _ = bizDB.Exec(`DROP FUNCTION IF EXISTS app.grant_finance_entitlement(uuid)`) })
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan overload musuh milik pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := bizDB.Exec(`DROP FUNCTION app.grant_finance_entitlement(uuid)`); err != nil {
			t.Fatalf("unseed overload: %v", err)
		}
	})
	t.Run("foreign-delete", func(t *testing.T) {
		// DELETE asing: pemilik rutin diberi DELETE atas workspaces
		// (bukan module_entitlements, bukan 014). Harus ditolak
		// meski tidak ada kebijakan RLS yang terlibat.
		if _, err := bizDB.Exec(`GRANT DELETE ON workspaces TO umnia_grant_exec`); err != nil {
			t.Fatalf("seed DELETE asing: %v", err)
		}
		t.Cleanup(func() { _, _ = bizDB.Exec(`REVOKE DELETE ON workspaces FROM umnia_grant_exec`) })
		before := snapshotOperatorState(t, bizDB)
		verifiersBefore := captureVerifiers(t, boot)
		code, out := run()
		if code == 0 {
			t.Fatalf("skrip LOLOS dengan DELETE asing milik pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("skrip gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := bizDB.Exec(`REVOKE DELETE ON workspaces FROM umnia_grant_exec`); err != nil {
			t.Fatalf("unseed DELETE asing: %v", err)
		}
	})
	// Catatan: tidak ada subtes "healthy" yang menjalankan skrip penuh
	// di sini — skrip penuh akan memutar password bisnis/admin ke
	// nilai sentinel env dan merusak klaster dev. Jalur sehat sudah
	// dibuktikan terpisah: skrip lolos (exit 0) pada klaster sehat
	// saat restrukturisasi, dan TestConfigurePostgresOperatorNoPasswordArgv
	// menjalankan skrip penuh dengan fake psql.
}

// TestInitdbPreflightRejectsUnsafeRoles membuktikan initdb Windows
// (01-runtime-roles.sh) MENOLAK state operator yang tidak aman
// sebelum membuat peran runtime apa pun. Skrip ASLI dijalankan via
// bash dengan koneksi TCP yang diturunkan dari
// BUSINESS_BOOTSTRAP_DATABASE_URL yang sudah divalidasi (host/port/
// user/password — bukan asumsi localhost/local-bootstrap-only) dan
// roster fixture milik tes ini di file temp 0600 lewat override
// UMNIA_OPERATOR_ROSTER_PATH (tidak pernah menulis roster deployment
// atau path absolut root). Scratch DB terisolasi menampung objek
// seed; snapshot membuktikan state asli tidak berubah.
func TestInitdbPreflightRejectsUnsafeRoles(t *testing.T) {
	_, boot, _ := grantTestEnv(t)
	script, err := filepath.Abs(filepath.Join("..", "..", "deploy", "windows", "initdb", "01-runtime-roles.sh"))
	if err != nil {
		t.Fatalf("path skrip: %v", err)
	}
	// Koneksi skrip diturunkan dari BUSINESS_BOOTSTRAP_DATABASE_URL
	// yang sudah divalidasi: host/port/user/password harus cocok dengan
	// klaster yang dipakai tes (termasuk port non-default). Password
	// hanya hidup di env proses — tidak pernah dicetak.
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	u, err := url.Parse(bootURL)
	if err != nil || u.Scheme != "postgres" || u.Hostname() == "" || u.User == nil || u.User.Username() == "" {
		t.Fatalf("BUSINESS_BOOTSTRAP_DATABASE_URL tidak valid untuk skrip initdb")
	}
	bootPW, ok := u.User.Password()
	if !ok || bootPW == "" {
		t.Fatalf("BUSINESS_BOOTSTRAP_DATABASE_URL tanpa password untuk skrip initdb")
	}
	bootPort := u.Port()
	if bootPort == "" {
		bootPort = "5432"
	}
	tmpDir := t.TempDir()
	rosterFile := filepath.Join(tmpDir, "operator-roster.env")
	if _, err := os.Stat(rosterFile); err == nil {
		t.Fatalf("roster temp sudah ada — tes tidak menimpa file yang sudah ada")
	}
	scratchName, scratchBoot := createOwnedScratchDB(t, boot, "umnia_test_initdb_pf")
	suffix := uniqueSuffix(t)
	login := "umnia_op_initdb_" + suffix
	if _, err := boot.Exec(`CREATE ROLE ` + login + ` LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION PASSWORD 'x-initdb-sentinel-123'`); err != nil {
		t.Fatalf("buat login initdb: %v", err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(`REVOKE umnia_operator FROM ` + login)
		_, _ = boot.Exec(`REVOKE umnia_grant_exec FROM ` + login)
		_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + login)
	})
	roster := "UMNIA_OP_INITDB_" + strings.ToUpper(suffix) + "=x-initdb-sentinel-123\n"
	if err := os.WriteFile(rosterFile, []byte(roster), 0600); err != nil {
		t.Fatalf("tulis roster initdb: %v", err)
	}
	t.Cleanup(func() { os.Remove(rosterFile) })
	run := func() (int, string) {
		env := cleanEnvForScript(t,
			"PGHOST="+u.Hostname(),
			"PGPORT="+bootPort,
			"PGPASSWORD="+bootPW,
			"POSTGRES_USER="+u.User.Username(),
			"POSTGRES_DB="+scratchName,
			"POSTGRES_PASSWORD=SENTINEL-postgres-initdb-987654321",
			"UMNIA_RUNTIME_ROLE=umnia_business",
			"UMNIA_RUNTIME_DB_PASSWORD=SENTINEL-runtime-initdb-987654321",
			"UMNIA_OPERATOR_ROSTER_PATH="+rosterFile,
		)
		return runRealScript(t, script, tmpDir, env)
	}
	assertUnchanged := func(before string, verifiersBefore map[string]string) {
		t.Helper()
		after := snapshotOperatorState(t, scratchBoot)
		if after != before {
			t.Fatalf("state operator BERUBAH sesudah preflight initdb menolak:\nsebelum:\n%s\nsesudah:\n%s", before, after)
		}
		assertVerifiersUnchanged(t, verifiersBefore, captureVerifiers(t, scratchBoot))
	}
	t.Run("exec-owner-member", func(t *testing.T) {
		if _, err := boot.Exec(`GRANT umnia_grant_exec TO ` + login); err != nil {
			t.Fatalf("seed: %v", err)
		}
		t.Cleanup(func() { _, _ = boot.Exec(`REVOKE umnia_grant_exec FROM ` + login) })
		before := snapshotOperatorState(t, scratchBoot)
		verifiersBefore := captureVerifiers(t, scratchBoot)
		code, out := run()
		if code == 0 {
			t.Fatalf("initdb LOLOS dengan login anggota pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("initdb gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := boot.Exec(`REVOKE umnia_grant_exec FROM ` + login); err != nil {
			t.Fatalf("unseed: %v", err)
		}
	})
	t.Run("owned-table", func(t *testing.T) {
		if _, err := scratchBoot.Exec(`CREATE TABLE preflight_owned (id int)`); err != nil {
			t.Fatalf("buat tabel seed: %v", err)
		}
		if _, err := scratchBoot.Exec(`ALTER TABLE preflight_owned OWNER TO ` + login); err != nil {
			t.Fatalf("alihkan pemilik tabel: %v", err)
		}
		t.Cleanup(func() { _, _ = scratchBoot.Exec(`DROP TABLE IF EXISTS preflight_owned`) })
		before := snapshotOperatorState(t, scratchBoot)
		verifiersBefore := captureVerifiers(t, scratchBoot)
		code, out := run()
		if code == 0 {
			t.Fatalf("initdb LOLOS dengan tabel milik login operator (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("initdb gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := scratchBoot.Exec(`DROP TABLE preflight_owned`); err != nil {
			t.Fatalf("drop tabel seed: %v", err)
		}
	})
	t.Run("custom-parent", func(t *testing.T) {
		// Parent kustom + hak kolom yang diwarisi (cermin subtes
		// configure; tabel target di scratch DB karena initdb
		// preflight berjalan di sana). Allowlist parent harus menolak
		// sebelum mutasi apa pun.
		parent := "umnia_op_initdb_parent_" + suffix
		if _, err := boot.Exec(`CREATE ROLE ` + parent + ` NOLOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION`); err != nil {
			t.Fatalf("buat parent kustom: %v", err)
		}
		t.Cleanup(func() {
			_, _ = scratchBoot.Exec(`REVOKE SELECT (id) ON preflight_parent_target FROM ` + parent)
			_, _ = boot.Exec(`REVOKE ` + parent + ` FROM ` + login)
			_, _ = boot.Exec(`DROP ROLE IF EXISTS ` + parent)
			_, _ = scratchBoot.Exec(`DROP TABLE IF EXISTS preflight_parent_target`)
		})
		if _, err := scratchBoot.Exec(`CREATE TABLE preflight_parent_target (id int)`); err != nil {
			t.Fatalf("buat tabel target: %v", err)
		}
		if _, err := scratchBoot.Exec(`GRANT SELECT (id) ON preflight_parent_target TO ` + parent); err != nil {
			t.Fatalf("seed hak kolom ke parent: %v", err)
		}
		if _, err := boot.Exec(`GRANT ` + parent + ` TO ` + login); err != nil {
			t.Fatalf("seed parent ke login: %v", err)
		}
		var colPriv bool
		if err := scratchBoot.QueryRow(`SELECT has_column_privilege($1, 'public.preflight_parent_target', 'id', 'SELECT')`, login).Scan(&colPriv); err != nil || !colPriv {
			t.Fatalf("prasyarat celah: has_column_privilege login = %v err=%v (harus true)", colPriv, err)
		}
		var tablePriv bool
		if err := scratchBoot.QueryRow(`SELECT has_table_privilege($1, 'public.preflight_parent_target', 'SELECT')`, login).Scan(&tablePriv); err != nil {
			t.Fatalf("cek has_table_privilege: %v", err)
		}
		if tablePriv {
			t.Fatalf("prasyarat celah: has_table_privilege login = true (harus false — celah tidak ada)")
		}
		before := snapshotOperatorState(t, scratchBoot)
		verifiersBefore := captureVerifiers(t, scratchBoot)
		code, out := run()
		if code == 0 {
			t.Fatalf("initdb LOLOS dengan login anggota parent kustom berh hak kolom (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("initdb gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		if !strings.Contains(out, "hanya boleh menjadi anggota umnia_operator") {
			t.Fatalf("pesan penolakan bukan allowlist parent: (%s)", out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := scratchBoot.Exec(`REVOKE SELECT (id) ON preflight_parent_target FROM ` + parent); err != nil {
			t.Fatalf("unseed hak kolom: %v", err)
		}
		if _, err := boot.Exec(`REVOKE ` + parent + ` FROM ` + login); err != nil {
			t.Fatalf("unseed parent: %v", err)
		}
		if _, err := boot.Exec(`DROP ROLE ` + parent); err != nil {
			t.Fatalf("drop parent: %v", err)
		}
		if _, err := scratchBoot.Exec(`DROP TABLE preflight_parent_target`); err != nil {
			t.Fatalf("drop tabel target: %v", err)
		}
	})
	t.Run("hostile-overload", func(t *testing.T) {
		// Overload musuh di scratch DB (skema app dibuat tes ini):
		// nama rutin Finance sama, tanda tangan lain, pemilik
		// umnia_grant_exec. Allowlist tanda tangan eksak harus menolak.
		if _, err := scratchBoot.Exec(`CREATE SCHEMA IF NOT EXISTS app`); err != nil {
			t.Fatalf("buat skema app: %v", err)
		}
		if _, err := scratchBoot.Exec(`CREATE FUNCTION app.grant_finance_entitlement(uuid) RETURNS void LANGUAGE sql AS 'SELECT 1'`); err != nil {
			t.Fatalf("buat overload musuh: %v", err)
		}
		if _, err := scratchBoot.Exec(`ALTER FUNCTION app.grant_finance_entitlement(uuid) OWNER TO umnia_grant_exec`); err != nil {
			t.Fatalf("alihkan overload musuh: %v", err)
		}
		t.Cleanup(func() {
			_, _ = scratchBoot.Exec(`DROP FUNCTION IF EXISTS app.grant_finance_entitlement(uuid)`)
			_, _ = scratchBoot.Exec(`DROP SCHEMA IF EXISTS app`)
		})
		before := snapshotOperatorState(t, scratchBoot)
		verifiersBefore := captureVerifiers(t, scratchBoot)
		code, out := run()
		if code == 0 {
			t.Fatalf("initdb LOLOS dengan overload musuh milik pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("initdb gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := scratchBoot.Exec(`DROP FUNCTION app.grant_finance_entitlement(uuid)`); err != nil {
			t.Fatalf("unseed overload: %v", err)
		}
	})
	t.Run("foreign-delete", func(t *testing.T) {
		// DELETE asing di scratch DB: pemilik rutin diberi DELETE
		// atas tabel workspaces seed. Harus ditolak.
		if _, err := scratchBoot.Exec(`CREATE TABLE workspaces (id int)`); err != nil {
			t.Fatalf("buat tabel seed: %v", err)
		}
		if _, err := scratchBoot.Exec(`GRANT DELETE ON workspaces TO umnia_grant_exec`); err != nil {
			t.Fatalf("seed DELETE asing: %v", err)
		}
		t.Cleanup(func() {
			_, _ = scratchBoot.Exec(`REVOKE DELETE ON workspaces FROM umnia_grant_exec`)
			_, _ = scratchBoot.Exec(`DROP TABLE IF EXISTS workspaces`)
		})
		before := snapshotOperatorState(t, scratchBoot)
		verifiersBefore := captureVerifiers(t, scratchBoot)
		code, out := run()
		if code == 0 {
			t.Fatalf("initdb LOLOS dengan DELETE asing milik pemilik rutin (harus ditolak)")
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("initdb gagal tanpa pesan preflight: exit %d (%s)", code, out)
		}
		assertUnchanged(before, verifiersBefore)
		if _, err := scratchBoot.Exec(`REVOKE DELETE ON workspaces FROM umnia_grant_exec`); err != nil {
			t.Fatalf("unseed DELETE asing: %v", err)
		}
		if _, err := scratchBoot.Exec(`DROP TABLE workspaces`); err != nil {
			t.Fatalf("drop tabel seed: %v", err)
		}
	})
}
