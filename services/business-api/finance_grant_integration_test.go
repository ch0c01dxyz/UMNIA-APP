package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Tes integrasi grant/revoke Finance F2-02-B (migrasi 014).
// Yang diuji adalah CLI dan rutin SUNGGUHAN (bukan grant bootstrap
// sintetis): grant finance -> aktivasi owner -> pembayaran dua aktor,
// revoke eksplisit, lifecycle, konkurensi, fault rollback, dan
// preflight provisioning 014. Prasyarat yang hilang (011/014/peran/
// DSN operator) adalah GAGAL, bukan dilewati — kecuali tidak ada
// database sama sekali (pola testDB yang sudah ada). Baris audit
// dipertahankan (append-only); cleanup hanya menghapus baris
// entitlement fixture per workspace tes dan rantai pembayaran tes.

const financeFixtureReason = "tes grant finance"

// financeTestEnv menerapkan 011 + 014 idempotent via psql asli
// sebagai bootstrap (autocommit — jalur Linux native) dan
// mengembalikan DSN operator. Gagal bila peran operator belum
// dibuat provisioning atau 011/014 gagal.
func financeTestEnv(t *testing.T) (db, boot *sql.DB, opURL string) {
	t.Helper()
	db = testDB(t)
	boot = bootstrapDB(t)
	opURL = os.Getenv("OPERATOR_TEST_DATABASE_URL")
	if opURL == "" {
		t.Fatalf("OPERATOR_TEST_DATABASE_URL kosong — provisioning operator (configure-postgres.sh / CI) belum dijalankan; tes gagal, bukan dilewati")
	}
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	if code, out := applyMigration011(t, bootURL); code != 0 {
		t.Fatalf("terapkan 011: exit %d (%s)", code, out)
	}
	if code, out := applyMigration014(t, bootURL); code != 0 {
		t.Fatalf("terapkan 014: exit %d (%s)", code, out)
	}
	return db, boot, opURL
}

// applyMigration014 menjalankan psql asli untuk 014 DALAM SATU
// transaksi (-1): gagal di tengah jalan me-rollback seluruh file.
func applyMigration014(t *testing.T, dbURL string) (int, string) {
	t.Helper()
	return applyMigrationFile(t, dbURL, filepath.Join("migrations", "014_finance_operator_entitlement.sql"))
}

func countFinanceEntitlements(t *testing.T, boot *sql.DB, ws string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'finance.basic'`, ws).Scan(&n); err != nil {
		t.Fatalf("hitung entitlement finance: %v", err)
	}
	return n
}

func countFinanceOperatorAudit(t *testing.T, boot *sql.DB, ws, action string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'finance.basic' AND version = '0.1.0' AND action = $2`, ws, action).Scan(&n); err != nil {
		t.Fatalf("hitung audit operator finance: %v", err)
	}
	return n
}

func countFinanceOperatorAuditAll(t *testing.T, boot *sql.DB, ws string) int {
	t.Helper()
	var n int
	if err := boot.QueryRow(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'finance.basic' AND version = '0.1.0'`, ws).Scan(&n); err != nil {
		t.Fatalf("hitung audit operator finance: %v", err)
	}
	return n
}

// financeGrantRow membaca baris entitlement finance saat ini.
func financeGrantRow(t *testing.T, boot *sql.DB, ws string) (grantedBy, source, reason, grantedAt string, found bool) {
	t.Helper()
	err := boot.QueryRow(`SELECT granted_by, source, reason, granted_at::text FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'finance.basic'`, ws).Scan(&grantedBy, &source, &reason, &grantedAt)
	if err == sql.ErrNoRows {
		return "", "", "", "", false
	}
	if err != nil {
		t.Fatalf("baca entitlement finance: %v", err)
	}
	return grantedBy, source, reason, grantedAt, true
}

// financeAuditRow membaca SATU event audit operator finance; gagal
// bila jumlahnya bukan tepat satu.
func financeAuditRow(t *testing.T, boot *sql.DB, ws, action string) (operatorID, source, reason, result, createdAt string) {
	t.Helper()
	if n := countFinanceOperatorAudit(t, boot, ws, action); n != 1 {
		t.Fatalf("audit finance %s: want 1 baris, got %d", action, n)
	}
	if err := boot.QueryRow(`SELECT operator_id, source, reason, result, created_at::text FROM operator_audit_events WHERE workspace_id = $1 AND module_id = 'finance.basic' AND version = '0.1.0' AND action = $2`, ws, action).Scan(&operatorID, &source, &reason, &result, &createdAt); err != nil {
		t.Fatalf("baca audit finance %s: %v", action, err)
	}
	return operatorID, source, reason, result, createdAt
}

// runFinanceCLI menjalankan CLI nyata untuk grant finance
// (--operation grant eksplisit, kecuali bila args sudah membawa
// --operation sendiri).
func runFinanceCLI(t *testing.T, bin, opURL string, args ...string) cliResult {
	t.Helper()
	return runGrantCLI(t, bin, opURL, args...)
}

func financeGrantArgs(ws, reason string) []string {
	return []string{"--operation", "grant", "--workspace", ws, "--module", "finance.basic", "--version", "0.1.0", "--reason", reason}
}

func financeRevokeArgs(ws, reason string) []string {
	return []string{"--operation", "revoke", "--workspace", ws, "--module", "finance.basic", "--reason", reason}
}

// runGrantCLIGo adalah runner CLI yang aman dari goroutine (tanpa
// t.Fatalf di dalam goroutine; galat dikembalikan).
func runGrantCLIGo(bin, opURL string, args ...string) (cliResult, error) {
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
			return cliResult{}, err
		}
	}
	return cliResult{code: code, stdout: stdout.String(), stderr: stderr.String()}, nil
}

// grantFinanceViaCLI memberi entitlement finance lewat CLI nyata dan
// memastikan exit 0 + output granted.
func grantFinanceViaCLI(t *testing.T, bin, opURL, ws, reason string) {
	t.Helper()
	res := runFinanceCLI(t, bin, opURL, financeGrantArgs(ws, reason)...)
	if res.code != 0 {
		t.Fatalf("CLI grant finance: want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "granted workspace="+ws+" ") {
		t.Fatalf("CLI grant finance: output tak terduga %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "module=finance.basic version=0.1.0") || !strings.Contains(res.stdout, "granted_by=operator:"+grantOpRole) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("CLI grant finance: output tidak memuat modul/aktor/audit: %q", res.stdout)
	}
	assertCLIOutputClean(t, res, opURL, reason)
}

// revokeFinanceViaCLI mencabut entitlement finance lewat CLI nyata
// dan memastikan exit 0 + output revoked.
func revokeFinanceViaCLI(t *testing.T, bin, opURL, ws, reason string) {
	t.Helper()
	res := runFinanceCLI(t, bin, opURL, financeRevokeArgs(ws, reason)...)
	if res.code != 0 {
		t.Fatalf("CLI revoke finance: want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "revoked workspace="+ws+" ") {
		t.Fatalf("CLI revoke finance: output tak terduga %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "module=finance.basic version=0.1.0") || !strings.Contains(res.stdout, "revoked_by=operator:"+grantOpRole) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("CLI revoke finance: output tidak memuat modul/aktor/audit: %q", res.stdout)
	}
	assertCLIOutputClean(t, res, opURL, reason)
}

// activateFinanceViaCLI mengaktifkan finance lewat API nyata sesudah
// grant CLI (owner-only, manage_modules).
func activateFinanceViaCLI(t *testing.T, env *activationEnv, ws, token string) {
	t.Helper()
	code, body := activateModule(t, env.srv, ws, "finance.basic", token, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi finance sesudah grant: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
}

// seedFinanceChain menyiapkan rantai pembayaran lewat API nyata pada
// workspace yang diberikan TANPA aktivasi finance: pelanggan + item,
// aktivasi Rental, satu versi harga, satu draft, quote, accept-price
// (total dari quote). Aktivasi finance diserahkan ke grant CLI +
// owner activate oleh pemanggil. amount adalah tarif per unit
// (string kanonis); draftHours adalah lama draft dalam jam.
func seedFinanceChain(t *testing.T, env *activationEnv, ws, token, sku, keyPrefix, amount string, draftHours int) (draftID, factID, total string) {
	t.Helper()
	code, body := createCustomer(t, env.srv, ws, token, map[string]string{"display_name": "Pelanggan Bayar"})
	if code != http.StatusCreated {
		t.Fatalf("seed customer %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	custID, _ := body["id"].(string)
	code, body = createCatalogItem(t, env.srv, ws, token, map[string]string{"sku": sku, "name": "Barang Bayar", "unit": "pcs"})
	if code != http.StatusCreated {
		t.Fatalf("seed item %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	itemID, _ := body["id"].(string)
	activateRentalOn(t, env, ws, token)
	base := dbCutoff(t, env.boot)
	priceID := seedPriceOn(t, env, ws, token, itemID, keyPrefix+"-price", base.Add(1*time.Hour), base.Add(73*time.Hour), amount, "IDR")
	starts := base.Add(2 * time.Hour).UTC().Format(time.RFC3339Nano)
	ends := base.Add(time.Duration(2+draftHours) * time.Hour).UTC().Format(time.RFC3339Nano)
	code, body = createDraft(t, env.srv, ws, token, keyPrefix+"-draft", draftBody(custID, itemID, starts, ends))
	if code != http.StatusCreated {
		t.Fatalf("seed draft %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	draftID, _ = body["id"].(string)
	code, quote := getQuote(t, env.srv, ws, draftID, token)
	if code != http.StatusOK {
		t.Fatalf("seed quote %s: want 200, got %d (%v)", keyPrefix, code, quote)
	}
	total, _ = quote["total_minor"].(string)
	if total == "" {
		t.Fatalf("quote %s tanpa total_minor: %v", keyPrefix, quote)
	}
	code, body = acceptPrice(t, env.srv, ws, draftID, token, keyPrefix+"-accept", acceptBody(priceID, total))
	if code != http.StatusCreated {
		t.Fatalf("accept %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	code, body = getAgreedPrice(t, env.srv, ws, draftID, token)
	if code != http.StatusOK {
		t.Fatalf("baca fakta %s: want 200, got %d (%v)", keyPrefix, code, body)
	}
	factID, _ = body["id"].(string)
	return draftID, factID, total
}

// financeChainEnv menyiapkan env aktivasi + rantai wsA standar
// (tarif 150000 IDR, draft 48 jam = 2 unit, total 300000) tanpa
// grant/aktivasi finance; cleanup rantai pembayaran + entitlement.
// Dibangun di atas setupAgreedPrice supaya cleanup berantai
// (parties, catalog_items, rental_contracts, holds/aset,
// price_versions, rental_agreed_prices) ikut terdaftar; cleanup
// tabel pembayaran + entitlement di sini terdaftar sesudahnya
// sehingga berjalan LEBIH DULU (LIFO). Tanpa rantai ini, 6 chain
// wsA bocor ke umnia_test dan menggagalkan tes FreshSchema yang
// mengharapkan 0 baris pada skema segar.
func financeChainEnv(t *testing.T, sku, keyPrefix string) (*activationEnv, string, string, string) {
	t.Helper()
	env := setupAgreedPrice(t)
	t.Cleanup(func() {
		for _, tbl := range []string{"rental_payment_reversals", "rental_payment_reversal_proposals", "rental_payments", "rental_payment_proposals"} {
			_, _ = env.boot.Exec(`DELETE FROM `+tbl+` WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
		}
		_, _ = env.boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	draftID, factID, total := seedFinanceChain(t, env, env.wsA, env.ownerToken, sku, keyPrefix, "150000", 48)
	return env, draftID, factID, total
}

// assertFinanceWrites403 membuktikan keempat rute tulis Finance
// ditolak 403 pada draft yang diberikan (termasuk replay approval
// bila proposalID diberikan).
func assertFinanceWrites403(t *testing.T, env *activationEnv, draftID string, proposalID string) {
	t.Helper()
	code, _ := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-403-prop", map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash"})
	if code != http.StatusForbidden {
		t.Fatalf("propose sesudah revoke/tanpa aktivasi: want 403, got %d", code)
	}
	if proposalID != "" {
		code, _ = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "fin-403-appr-replay")
		if code != http.StatusForbidden {
			t.Fatalf("replay approval sesudah revoke: want 403, got %d", code)
		}
	}
}

// financeSnapshot adalah hitungan baris yang harus tidak berubah
// pada rollback fault (entitlement, audit operator, instalasi,
// keuangan, fakta referensi).
type financeSnapshot struct {
	entitlements, opAudit, installations, proposals, payments, revProposals, reversals, facts, audits int
}

func snapshotFinance(t *testing.T, boot *sql.DB, ws string) financeSnapshot {
	t.Helper()
	q := func(query string, args ...any) int {
		var n int
		if err := boot.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("snapshot finance (%s): %v", query, err)
		}
		return n
	}
	return financeSnapshot{
		entitlements:  q(`SELECT count(*) FROM module_entitlements WHERE workspace_id = $1`, ws),
		opAudit:       q(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1`, ws),
		installations: q(`SELECT count(*) FROM module_installations WHERE workspace_id = $1`, ws),
		proposals:     q(`SELECT count(*) FROM rental_payment_proposals WHERE workspace_id = $1`, ws),
		payments:      q(`SELECT count(*) FROM rental_payments WHERE workspace_id = $1`, ws),
		revProposals:  q(`SELECT count(*) FROM rental_payment_reversal_proposals WHERE workspace_id = $1`, ws),
		reversals:     q(`SELECT count(*) FROM rental_payment_reversals WHERE workspace_id = $1`, ws),
		facts:         q(`SELECT count(*) FROM rental_agreed_prices WHERE workspace_id = $1`, ws),
		audits:        q(`SELECT count(*) FROM audit_events WHERE workspace_id = $1`, ws),
	}
}

func assertFinanceSnapshotUnchanged(t *testing.T, before, after financeSnapshot, what string) {
	t.Helper()
	if before != after {
		t.Fatalf("snapshot berubah (%s): sebelum %+v, sesudah %+v", what, before, after)
	}
}

// --- Grant positif + aktivasi + pembayaran dua aktor (CLI nyata) ---

// parsePGTimestamptz mengurai teks timestamptz PostgreSQL
// ("2006-01-02 15:04:05.999999+00").
func parsePGTimestamptz(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse("2006-01-02 15:04:05.999999999-07", s)
	if err != nil {
		t.Fatalf("parse waktu PG %q: %v", s, err)
	}
	return ts
}

// assertSameSecond membandingkan dua waktu pada presisi detik —
// CLI mencetak RFC3339 (detik), DB menyimpan mikrodetik.
func assertSameSecond(t *testing.T, a, b time.Time, what string) {
	t.Helper()
	if !a.Truncate(time.Second).Equal(b.Truncate(time.Second)) {
		t.Fatalf("%s berbeda detik: %v vs %v", what, a, b)
	}
}

// cliTimeField mengambil nilai field key=value dari satu baris output
// CLI dan mengurainya sebagai RFC3339.
func cliTimeField(t *testing.T, out, field string) time.Time {
	t.Helper()
	idx := strings.Index(out, field+"=")
	if idx < 0 {
		t.Fatalf("output CLI tidak memuat %s=: %q", field, out)
	}
	rest := out[idx+len(field)+1:]
	if end := strings.IndexAny(rest, " \n"); end >= 0 {
		rest = rest[:end]
	}
	ts, err := time.Parse(time.RFC3339, rest)
	if err != nil {
		t.Fatalf("parse %s %q: %v", field, rest, err)
	}
	return ts
}

func TestFinanceGrantCLI_PositiveAndActivation(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, factID, total := financeChainEnv(t, "FIN-POS-001", "fin-pos")
	if total != "300000" {
		t.Fatalf("total rantai: want 300000, got %q", total)
	}
	_ = factID

	// Sebelum grant: tulis Finance 403 (guard: belum menjadi hak).
	assertFinanceWrites403(t, env, draftID, "")

	// Grant tanpa --operation eksplisit: default adalah grant.
	res := runFinanceCLI(t, bin, opURL, "--workspace", env.wsA, "--module", "finance.basic", "--version", "0.1.0", "--reason", financeFixtureReason)
	if res.code != 0 {
		t.Fatalf("CLI grant finance (default operation): want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if !strings.HasPrefix(res.stdout, "granted workspace="+env.wsA+" ") {
		t.Fatalf("CLI grant finance: output tak terduga %q", res.stdout)
	}
	if !strings.Contains(res.stdout, "module=finance.basic version=0.1.0") || !strings.Contains(res.stdout, "granted_by=operator:"+grantOpRole) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("CLI grant finance: output tidak memuat modul/aktor/audit: %q", res.stdout)
	}
	assertCLIOutputClean(t, res, opURL, financeFixtureReason)

	grantedBy, source, reason, grantedAt, found := financeGrantRow(t, boot, env.wsA)
	if !found {
		t.Fatalf("entitlement finance tidak ada sesudah grant")
	}
	if grantedBy != "operator:"+grantOpRole || source != "operator-cli" || reason != financeFixtureReason {
		t.Fatalf("entitlement finance: granted_by=%q source=%q reason=%q", grantedBy, source, reason)
	}
	opID, opSource, opReason, opResult, opAt := financeAuditRow(t, boot, env.wsA, "entitlement_granted")
	if opID != grantOpRole || opSource != "operator-cli" || opReason != financeFixtureReason || opResult != "granted" {
		t.Fatalf("audit finance: operator_id=%q source=%q reason=%q result=%q", opID, opSource, opReason, opResult)
	}
	// Jam grant dibagikan eksplisit antara entitlement, audit, dan
	// hasil CLI (bukan dua pemanggilan jam yang berbeda).
	if grantedAt != opAt {
		t.Fatalf("jam grant tidak dibagikan: entitlement %q, audit %q", grantedAt, opAt)
	}
	assertSameSecond(t, parsePGTimestamptz(t, grantedAt), cliTimeField(t, res.stdout, "granted_at"), "granted_at CLI vs DB")

	// Sesudah grant tapi sebelum aktivasi owner: tulis masih 403.
	assertFinanceWrites403(t, env, draftID, "")

	// Aktivasi owner lewat API nyata kini 200; pembayaran dua aktor
	// berjalan: owner mengusul 100000, manager menyetujui -> 201.
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-pos-prop", map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah aktivasi: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "fin-pos-appr")
	if code != http.StatusCreated {
		t.Fatalf("approve sesudah aktivasi: want 201, got %d (%v)", code, body)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET histori: want 200, got %d (%v)", code, body)
	}
	if body["paid_minor"] != "100000" || body["outstanding_minor"] != "200000" || body["total_minor"] != "300000" {
		t.Fatalf("histori: paid=%v outstanding=%v total=%v", body["paid_minor"], body["outstanding_minor"], body["total_minor"])
	}
}

func TestFinanceGrantCLI_NoOpLegacyConflict(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	// Registry sintetis finance.basic/0.2.0: perlu untuk baris legacy
	// 0.2.0 (FK entitlement -> registry); bukan versi yang didukung.
	m020 := exampleManifest(t, "finance.basic")
	m020["version"] = "0.2.0"
	raw020, err := json.Marshal(m020)
	if err != nil {
		t.Fatalf("marshal finance 0.2.0: %v", err)
	}
	env := setupActivation(t, [][3]string{{"finance.basic", "0.2.0", string(raw020)}})
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})

	// Grant pertama, lalu ulangi: already_granted tanpa tulis baru.
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	_, _, _, grantedAt1, _ := financeGrantRow(t, boot, env.wsA)
	res := runFinanceCLI(t, bin, opURL, financeGrantArgs(env.wsA, "alasan kedua yang berbeda")...)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "already_granted workspace="+env.wsA+" ") {
		t.Fatalf("grant ulang: want exit 0 + already_granted, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "granted_by=operator:"+grantOpRole) || !strings.Contains(res.stdout, "audit_present=true") {
		t.Fatalf("grant ulang: output tidak memuat aktor asli: %q", res.stdout)
	}
	by2, src2, rsn2, at2, _ := financeGrantRow(t, boot, env.wsA)
	if by2 != "operator:"+grantOpRole || at2 != grantedAt1 || src2 != "operator-cli" || rsn2 != financeFixtureReason {
		t.Fatalf("no-op menulis ulang metadata: (%q,%q,%q,%q)", by2, at2, src2, rsn2)
	}
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_granted"); n != 1 {
		t.Fatalf("no-op: want 1 baris audit grant, got %d", n)
	}

	// Legacy: baris sintetis tanpa audit -> legacy_existing, tanpa
	// fabrikasi audit dan tanpa tulis ulang.
	grantEntitlement(t, boot, env.wsB, "finance.basic", "0.1.0")
	res = runFinanceCLI(t, bin, opURL, financeGrantArgs(env.wsB, financeFixtureReason)...)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "legacy_existing workspace="+env.wsB+" ") {
		t.Fatalf("legacy: want exit 0 + legacy_existing, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "granted_by=operator:fixture") || !strings.Contains(res.stdout, "audit_present=false") {
		t.Fatalf("legacy: output tidak memuat aktor lama/audit_present=false: %q", res.stdout)
	}
	if n := countFinanceOperatorAuditAll(t, boot, env.wsB); n != 0 {
		t.Fatalf("legacy: want 0 baris audit (tanpa fabrikasi), got %d", n)
	}

	// Gate versi mendahului cabang no-op/konflik: permintaan 0.2.0
	// pada workspace yang sudah diberi 0.1.0 adalah invalid (exit 2),
	// bukan conflict — 0.2.0 di luar scope beku apa pun barisnya.
	res = runFinanceCLI(t, bin, opURL, "--operation", "grant", "--workspace", env.wsA, "--module", "finance.basic", "--version", "0.2.0", "--reason", financeFixtureReason)
	if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("versi tak didukung pada ws bergrant: want exit 2 + invalid input, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if n := countFinanceEntitlements(t, boot, env.wsA); n != 1 {
		t.Fatalf("invalid versi: baris entitlement berubah (want 1, got %d)", n)
	}

	// Legacy 0.2.0: permintaan 0.1.0 yang didukung terhadap baris
	// versi berbeda tetap konflik (exit 3); permintaan 0.2.0 sendiri
	// tetap invalid (exit 2) — gate versi tidak peduli baris yang ada.
	wsC := createWorkspace(t, env.srv, env.ownerToken, "Usaha Finance C")
	t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, wsC) })
	grantEntitlement(t, boot, wsC, "finance.basic", "0.2.0")
	res = runFinanceCLI(t, bin, opURL, financeGrantArgs(wsC, financeFixtureReason)...)
	if res.code != 3 || !strings.Contains(res.stderr, "conflict") {
		t.Fatalf("konflik versi: want exit 3 + conflict, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	byC, _, _, _, _ := financeGrantRow(t, boot, wsC)
	// Baris legacy 0.2.0 harus utuh (versi dibaca dari kolom version).
	var verC string
	if err := boot.QueryRow(`SELECT version FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'finance.basic'`, wsC).Scan(&verC); err != nil || verC != "0.2.0" || byC != "operator:fixture" {
		t.Fatalf("konflik: baris legacy berubah (want 0.2.0/operator:fixture, got %q/%q err=%v)", verC, byC, err)
	}
	if n := countFinanceOperatorAuditAll(t, boot, wsC); n != 0 {
		t.Fatalf("konflik: audit tertulis (want 0, got %d)", n)
	}
	res = runFinanceCLI(t, bin, opURL, "--operation", "grant", "--workspace", wsC, "--module", "finance.basic", "--version", "0.2.0", "--reason", financeFixtureReason)
	if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("legacy 0.2.0 + minta 0.2.0: want exit 2 + invalid input, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}

	// Modul terdaftar lain dan yang tidak dikenal: exit 2, tanpa tulis.
	wsD := createWorkspace(t, env.srv, env.ownerToken, "Usaha Finance D")
	t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, wsD) })
	for _, mod := range []struct{ id, ver string }{
		{"finance.advanced", "0.1.0"},
		{"spa.standard", "0.1.0"},
		{"modul.tidak.ada", "9.9.9"},
		{"finance.basic", "0.2.0"},
	} {
		res = runFinanceCLI(t, bin, opURL, "--operation", "grant", "--workspace", wsD, "--module", mod.id, "--version", mod.ver, "--reason", financeFixtureReason)
		if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
			t.Fatalf("modul %s %s: want exit 2 + invalid input, got %d (out=%q err=%q)", mod.id, mod.ver, res.code, res.stdout, res.stderr)
		}
	}
	if n := countFinanceEntitlements(t, boot, wsD); n != 0 {
		t.Fatalf("modul tak didukung: want 0 baris, got %d", n)
	}
}

// --- Revoke positif + lifecycle ---

func TestFinanceRevokeCLI_PositiveAndLifecycle(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, _, _ := financeChainEnv(t, "FIN-REV-001", "fin-rev")
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)

	// Satu pembayaran penuh sebelum revoke: usulan owner, approval
	// manager, lalu pembalikan penuh (supaya ada fakta keuangan yang
	// harus tetap utuh sesudah revoke).
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-rev-prop", map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "fin-rev-appr")
	if code != http.StatusCreated {
		t.Fatalf("approve: want 201, got %d (%v)", code, body)
	}
	paymentID, _ := body["id"].(string)
	_, _, _, grantedAt, _ := financeGrantRow(t, boot, env.wsA)

	// Audit instalasi sebelum revoke: jumlah event aktivasi modul.
	var auditsBefore int
	if err := boot.QueryRow(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'module_activated'`, env.wsA).Scan(&auditsBefore); err != nil {
		t.Fatalf("hitung audit aktivasi: %v", err)
	}
	var instBefore string
	if err := boot.QueryRow(`SELECT status || '|' || version || '|' || COALESCE(activated_at::text, '') || '|' || COALESCE(deactivated_at::text, '') FROM module_installations WHERE workspace_id = $1 AND module_id = 'finance.basic'`, env.wsA).Scan(&instBefore); err != nil {
		t.Fatalf("baca instalasi sebelum revoke: %v", err)
	}

	// Revoke lewat CLI nyata.
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "cabut finance tes")
	if n := countFinanceEntitlements(t, boot, env.wsA); n != 0 {
		t.Fatalf("revoke: baris entitlement masih ada (want 0, got %d)", n)
	}
	revID, revSource, revReason, revResult, revAt := financeAuditRow(t, boot, env.wsA, "entitlement_revoked")
	if revID != grantOpRole || revSource != "operator-cli" || revReason != "cabut finance tes" || revResult != "revoked" {
		t.Fatalf("audit revoke: operator_id=%q source=%q reason=%q result=%q", revID, revSource, revReason, revResult)
	}
	if revAt == "" || revAt == grantedAt {
		t.Fatalf("jam revoke tidak valid/berbeda dari grant: revoke %q, grant %q", revAt, grantedAt)
	}
	// Grant audit lama tetap ada (riwayat tidak dihapus revoke).
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_granted"); n != 1 {
		t.Fatalf("revoke menghapus audit grant lama (want 1, got %d)", n)
	}

	// Aktif + revoked: keempat tulis Finance 403, termasuk replay
	// approval yang sudah disetujui sebelum revoke.
	assertFinanceWrites403(t, env, draftID, proposalID)

	// Riwayat tetap bisa dibaca member; pembayaran yang sudah
	// disetujui sebelum revoke tetap utuh (immutable).
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET histori sesudah revoke: want 200, got %d (%v)", code, body)
	}
	if body["paid_minor"] != "100000" || body["outstanding_minor"] != "200000" {
		t.Fatalf("histori sesudah revoke berubah: paid=%v outstanding=%v", body["paid_minor"], body["outstanding_minor"])
	}
	payments, _ := body["payments"].([]any)
	if len(payments) != 1 {
		t.Fatalf("histori sesudah revoke: want 1 payment, got %d", len(payments))
	}
	// Pembalikan catatan sesudah revoke juga 403 (bukan 201).
	code, _ = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.ownerToken, "fin-rev-revprop", map[string]any{"reason": "batal sesudah revoke"})
	if code != http.StatusForbidden {
		t.Fatalf("propose reversal sesudah revoke: want 403, got %d", code)
	}

	// Aktivasi owner yang cocok sesudah revoke: 200 no-op TANPA cek
	// entitlement, tanpa audit/timestamp baru (modules.go sengaja
	// mengembalikan no-op sebelum lookup entitlement).
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi cocok sesudah revoke: want 200 no-op, got %d (%v)", code, body)
	}
	var instAfter string
	if err := boot.QueryRow(`SELECT status || '|' || version || '|' || COALESCE(activated_at::text, '') || '|' || COALESCE(deactivated_at::text, '') FROM module_installations WHERE workspace_id = $1 AND module_id = 'finance.basic'`, env.wsA).Scan(&instAfter); err != nil {
		t.Fatalf("baca instalasi sesudah aktivasi no-op: %v", err)
	}
	if instAfter != instBefore {
		t.Fatalf("aktivasi no-op mengubah instalasi: sebelum %q, sesudah %q", instBefore, instAfter)
	}
	var auditsAfter int
	if err := boot.QueryRow(`SELECT count(*) FROM audit_events WHERE workspace_id = $1 AND action = 'module_activated'`, env.wsA).Scan(&auditsAfter); err != nil {
		t.Fatalf("hitung audit aktivasi sesudah: %v", err)
	}
	if auditsAfter != auditsBefore {
		t.Fatalf("aktivasi no-op menulis audit: sebelum %d, sesudah %d", auditsBefore, auditsAfter)
	}

	// Owner tetap bisa menonaktifkan sesudah revoke.
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deaktivasi sesudah revoke: want 200, got %d (%v)", code, body)
	}
	// Inactive + revoked: aktivasi kini 403.
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("aktivasi inactive+revoked: want 403, got %d (%v)", code, body)
	}
}

func TestFinanceRevokeCLI_IdempotentAndConflict(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})

	// Revoke tanpa baris: already_revoked, tanpa tulis/audit,
	// aktor/waktu NULL (bukan bukti pembatalan historis).
	res := runFinanceCLI(t, bin, opURL, financeRevokeArgs(env.wsA, "cabut tanpa baris")...)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "already_revoked workspace="+env.wsA+" ") {
		t.Fatalf("revoke tanpa baris: want exit 0 + already_revoked, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, "audit_present=false") || strings.Contains(res.stdout, "revoked_by=operator:") {
		t.Fatalf("already_revoked: aktor harus kosong + audit_present=false: %q", res.stdout)
	}
	if n := countFinanceOperatorAuditAll(t, boot, env.wsA); n != 0 {
		t.Fatalf("already_revoked menulis audit (want 0, got %d)", n)
	}
	// Revoke kedua tanpa baris: tetap already_revoked.
	res = runFinanceCLI(t, bin, opURL, financeRevokeArgs(env.wsA, "cabut kedua")...)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "already_revoked workspace="+env.wsA+" ") {
		t.Fatalf("revoke kedua tanpa baris: want already_revoked, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}

	// Grant -> revoke -> revoke lagi: revoked lalu already_revoked.
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "cabut pertama")
	res = runFinanceCLI(t, bin, opURL, financeRevokeArgs(env.wsA, "cabut kedua")...)
	if res.code != 0 || !strings.HasPrefix(res.stdout, "already_revoked workspace="+env.wsA+" ") {
		t.Fatalf("revoke sesudah revoke: want already_revoked, got %d (%q %q)", res.code, res.stdout, res.stderr)
	}
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_revoked"); n != 1 {
		t.Fatalf("revoke ganda: want 1 audit revoke, got %d", n)
	}

	// Regrant sesudah revoke: baris + audit BARU; event lama tetap.
	grantFinanceViaCLI(t, bin, opURL, env.wsA, "grant ulang sesudah revoke")
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_granted"); n != 2 {
		t.Fatalf("regrant: want 2 audit grant (lama + baru), got %d", n)
	}
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_revoked"); n != 1 {
		t.Fatalf("regrant: audit revoke harus tetap 1, got %d", n)
	}

	// Revoke rental.basic ditolak sebelum akses DB (exit 2).
	res = runFinanceCLI(t, bin, opURL, "--operation", "revoke", "--workspace", env.wsA, "--module", "rental.basic", "--reason", "cabut rental")
	if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("revoke rental: want exit 2 + invalid input, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	// Revoke modul tak dikenal / versi salah: exit 2.
	for _, args := range [][]string{
		{"--operation", "revoke", "--workspace", env.wsA, "--module", "modul.tidak.ada", "--reason", "x"},
		{"--operation", "revoke", "--workspace", env.wsA, "--module", "finance.basic", "--version", "0.2.0", "--reason", "x"},
		{"--operation", "revoke", "--workspace", env.wsA, "--module", "finance.basic", "--version", "9.9.9", "--reason", "x"},
		{"--operation", "bogus", "--workspace", env.wsA, "--module", "finance.basic", "--reason", "x"},
	} {
		res = runFinanceCLI(t, bin, opURL, args...)
		if res.code != 2 || !strings.Contains(res.stderr, "invalid input") {
			t.Fatalf("revoke invalid %v: want exit 2 + invalid input, got %d (out=%q err=%q)", args, res.code, res.stdout, res.stderr)
		}
	}
	// Baris finance wsA harus utuh sesudah semua penolakan.
	if n := countFinanceEntitlements(t, boot, env.wsA); n != 1 {
		t.Fatalf("penolakan revoke mengubah baris (want 1, got %d)", n)
	}

	// Revoke terhadap baris versi berbeda (legacy 0.2.0): U14CF
	// tanpa tulis.
	m020 := exampleManifest(t, "finance.basic")
	m020["version"] = "0.2.0"
	raw020, err := json.Marshal(m020)
	if err != nil {
		t.Fatalf("marshal finance 0.2.0: %v", err)
	}
	env2 := setupActivation(t, [][3]string{{"finance.basic", "0.2.0", string(raw020)}})
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env2.wsA, env2.wsB)
	})
	grantEntitlement(t, boot, env2.wsA, "finance.basic", "0.2.0")
	res = runFinanceCLI(t, bin, opURL, financeRevokeArgs(env2.wsA, "cabut 0.2.0")...)
	if res.code != 3 || !strings.Contains(res.stderr, "conflict") {
		t.Fatalf("revoke versi berbeda: want exit 3 + conflict, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	var ver string
	if err := boot.QueryRow(`SELECT version FROM module_entitlements WHERE workspace_id = $1 AND module_id = 'finance.basic'`, env2.wsA).Scan(&ver); err != nil || ver != "0.2.0" {
		t.Fatalf("revoke konflik mengubah baris (want 0.2.0, got %q err=%v)", ver, err)
	}
	if n := countFinanceOperatorAuditAll(t, boot, env2.wsA); n != 0 {
		t.Fatalf("revoke konflik menulis audit (want 0, got %d)", n)
	}
}

// --- Lifecycle: empat keadaan grant/aktivasi/revoke ---

func TestFinanceGrant_LifecycleStates(t *testing.T) {
	_, _, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, _, _ := financeChainEnv(t, "FIN-LIFE-001", "fin-life")

	// (1) Grant segar sebelum aktivasi pertama: tulis 403 sampai
	// owner mengaktifkan.
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	assertFinanceWrites403(t, env, draftID, "")
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-life-prop1", map[string]any{"amount_minor": "50000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah aktivasi pertama: want 201, got %d (%v)", code, body)
	}

	// (2) Regrant saat instalasi tetap aktif memulihkan eligibility
	// TANPA aktivasi owner baru.
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "cabut sementara")
	assertFinanceWrites403(t, env, draftID, "")
	grantFinanceViaCLI(t, bin, opURL, env.wsA, "grant ulang saat aktif")
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-life-prop2", map[string]any{"amount_minor": "50000", "currency": "IDR", "method": "transfer"})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah regrant saat aktif: want 201, got %d (%v)", code, body)
	}

	// (3) Regrant saat inactive tetap butuh aktivasi.
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "cabut kedua")
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deaktivasi: want 200, got %d (%v)", code, body)
	}
	grantFinanceViaCLI(t, bin, opURL, env.wsA, "grant ulang saat inactive")
	assertFinanceWrites403(t, env, draftID, "")
	code, body = activateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi sesudah regrant saat inactive: want 200, got %d (%v)", code, body)
	}
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-life-prop3", map[string]any{"amount_minor": "50000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah regrant+aktivasi: want 201, got %d (%v)", code, body)
	}
}

// --- Negatif: hak, spoof, batas input ---

func TestFinanceGrantCLI_Negatives(t *testing.T) {
	db, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	opDB := openOperatorDB(t, opURL)

	// (a) DML mentah operator atas tabel finance/entitlement/audit
	// ditolak (tanpa DELETE mentah, tanpa UPDATE, tanpa INSERT audit).
	for _, q := range []string{
		`DELETE FROM module_entitlements WHERE workspace_id = $1`,
		`UPDATE module_entitlements SET version = '0.1.0' WHERE workspace_id = $1`,
		`INSERT INTO module_entitlements (workspace_id, module_id, version, granted_by, source, reason) VALUES ($1, 'finance.basic', '0.1.0', 'x', 'x', 'x')`,
		`INSERT INTO operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result) VALUES ($1, 'finance.basic', '0.1.0', 'x', 'entitlement_revoked', 'x', 'x', 'revoked')`,
		`DELETE FROM operator_audit_events WHERE workspace_id = $1`,
		`UPDATE operator_audit_events SET result = 'revoked' WHERE workspace_id = $1`,
		`TRUNCATE operator_audit_events`,
	} {
		// TRUNCATE tidak memakai parameter; yang lain memakai $1.
		var err error
		if strings.HasPrefix(q, "TRUNCATE") {
			_, err = opDB.Exec(q)
		} else {
			_, err = opDB.Exec(q, env.wsA)
		}
		assertPermissionDenied(t, err, "operator: "+q)
	}

	// (b) Runtime bisnis memanggil kedua rutin finance: tanpa EXECUTE.
	_, err := db.Exec(`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', 'tes')`, env.wsA)
	assertPermissionDenied(t, err, "runtime grant finance")
	_, err = db.Exec(`SELECT * FROM app.revoke_finance_entitlement($1, 'finance.basic', 'tes')`, env.wsA)
	assertPermissionDenied(t, err, "runtime revoke finance")
	// Admin yang tidak ada tidak punya EXECUTE (absent-safe).
	var adminExec bool
	if err := boot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = 'umnia_admin' AND has_function_privilege(r.oid, 'app.grant_finance_entitlement(uuid, text, text, text)'::regprocedure, 'EXECUTE'))`).Scan(&adminExec); err != nil {
		t.Fatalf("cek admin EXECUTE grant: %v", err)
	}
	if adminExec {
		t.Fatalf("admin punya EXECUTE atas rutin grant finance")
	}
	if err := boot.QueryRow(`SELECT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname = 'umnia_admin' AND has_function_privilege(r.oid, 'app.revoke_finance_entitlement(uuid, text, text)'::regprocedure, 'EXECUTE'))`).Scan(&adminExec); err != nil {
		t.Fatalf("cek admin EXECUTE revoke: %v", err)
	}
	if adminExec {
		t.Fatalf("admin punya EXECUTE atas rutin revoke finance")
	}

	// (c) SET ROLE pemilik ditolak (bukan anggota).
	_, err = opDB.Exec(`SET ROLE umnia_grant_exec`)
	assertPermissionDenied(t, err, "SET ROLE pemilik")

	// (d) GUC palsu + SET ROLE kapabilitas tidak mengubah aktor:
	// grant dan revoke tetap tercatat atas session_user.
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
	if err := tx.QueryRow(`SELECT result, granted_by FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', 'tes spoof')`, env.wsA).Scan(&result, &grantedBy); err != nil {
		t.Fatalf("grant sesudah spoof: %v", err)
	}
	if result != "granted" || grantedBy != "operator:"+grantOpRole {
		t.Fatalf("aktor grant sesudah spoof: result=%q granted_by=%q", result, grantedBy)
	}
	var rresult, revokedBy string
	if err := tx.QueryRow(`SELECT result, revoked_by FROM app.revoke_finance_entitlement($1, 'finance.basic', 'tes spoof revoke')`, env.wsA).Scan(&rresult, &revokedBy); err != nil {
		t.Fatalf("revoke sesudah spoof: %v", err)
	}
	if rresult != "revoked" || revokedBy != "operator:"+grantOpRole {
		t.Fatalf("aktor revoke sesudah spoof: result=%q revoked_by=%q", rresult, revokedBy)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit spoof: %v", err)
	}

	// (e) Tabel temporer bayangan tidak mengubah target: grant ke wsB
	// mendarat di public.module_entitlements, temp tetap kosong.
	if _, err := opDB.Exec(`CREATE TEMP TABLE module_entitlements (workspace_id uuid, module_id text, version text, granted_by text, source text, reason text)`); err != nil {
		t.Fatalf("buat temp table: %v", err)
	}
	var resultB string
	if err := opDB.QueryRow(`SELECT result FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', 'tes shadow')`, env.wsB).Scan(&resultB); err != nil {
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
	if n := countFinanceEntitlements(t, boot, env.wsB); n != 1 {
		t.Fatalf("public entitlement sesudah shadow: want 1, got %d", n)
	}

	// (f) Input tidak valid langsung ke rutin: U14IV/U14NF, tanpa tulis.
	unknownWS := "11111111-2222-3333-4444-555555555555"
	for _, tc := range []struct {
		q    string
		args []any
		want string
	}{
		{`SELECT * FROM app.grant_finance_entitlement($1, NULL, '0.1.0', 'x')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', NULL, 'x')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', NULL)`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', '')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', '   ')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.advanced', '0.1.0', 'x')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.2.0', 'x')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', 'x')`, []any{unknownWS}, "U14NF"},
		{`SELECT * FROM app.revoke_finance_entitlement($1, 'finance.basic', NULL)`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.revoke_finance_entitlement($1, 'finance.basic', '')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.revoke_finance_entitlement($1, 'rental.basic', 'x')`, []any{env.wsA}, "U14IV"},
		{`SELECT * FROM app.revoke_finance_entitlement($1, 'finance.basic', 'x')`, []any{unknownWS}, "U14NF"},
	} {
		_, err := opDB.Exec(tc.q, tc.args...)
		if err == nil {
			t.Fatalf("rutin %s: seharusnya ditolak %s, malah lolos", tc.q, tc.want)
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != tc.want {
			t.Fatalf("rutin %s: want SQLSTATE %s, got %v", tc.q, tc.want, err)
		}
	}
	// Reason 501 rune langsung ke rutin: U14IV.
	longReason := strings.Repeat("x", 501)
	_, err = opDB.Exec(`SELECT * FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', $2)`, env.wsA, longReason)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "U14IV" {
		t.Fatalf("reason 501 rune: want U14IV, got %v", err)
	}

	// (g) Reason multibyte via CLI: 265 karakter, ~515 byte — di
	// bawah batas 500 KARAKTER tapi di atas 500 byte — harus lolos
	// bila batas dihitung per karakter; tersimpan verbatim.
	reason := strings.Repeat("é", 250) + " alasan café ☕"
	if len(reason) <= 500 {
		t.Fatalf("fixture multibyte tidak melebihi 500 byte (got %d)", len(reason))
	}
	wsM := createWorkspace(t, env.srv, env.ownerToken, "Usaha Finance MB")
	t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, wsM) })
	res := runFinanceCLI(t, bin, opURL, financeGrantArgs(wsM, reason)...)
	if res.code != 0 {
		t.Fatalf("grant multibyte: want exit 0, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	assertCLIOutputClean(t, res, opURL, reason)
	_, _, stored, _, _ := financeGrantRow(t, boot, wsM)
	if stored != reason {
		t.Fatalf("reason multibyte tidak utuh: want %q, got %q", reason, stored)
	}
}

// --- Fault rollback ---

func TestFinanceGrantRevoke_FaultsRollback(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, _, _ := financeChainEnv(t, "FIN-FAULT-001", "fin-fault")
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-fault-prop", map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, _ = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "fin-fault-appr")
	if code != http.StatusCreated {
		t.Fatalf("approve: want 201, got %d", code)
	}

	// Fault A: cabut INSERT audit dari peran eksekusi; revoke wsA
	// harus gagal exit 1 dengan SELURUH snapshot utuh, lalu retry
	// sesudah restore berhasil.
	beforeA := snapshotFinance(t, boot, env.wsA)
	t.Cleanup(func() { _, _ = boot.Exec(`GRANT INSERT ON operator_audit_events TO umnia_grant_exec`) })
	if _, err := boot.Exec(`REVOKE INSERT ON operator_audit_events FROM umnia_grant_exec`); err != nil {
		t.Fatalf("pasang fault audit: %v", err)
	}
	res := runFinanceCLI(t, bin, opURL, financeRevokeArgs(env.wsA, "cabut fault audit")...)
	if res.code != grantExitUnavailable || !strings.Contains(res.stderr, "unavailable") {
		t.Fatalf("fault audit revoke: want exit 1 + unavailable, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	assertFinanceSnapshotUnchanged(t, beforeA, snapshotFinance(t, boot, env.wsA), "fault audit revoke")
	if _, err := boot.Exec(`GRANT INSERT ON operator_audit_events TO umnia_grant_exec`); err != nil {
		t.Fatalf("restore INSERT audit: %v", err)
	}
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "cabut sesudah restore")

	// Fault B: cabut INSERT entitlement; grant wsB harus gagal exit 1
	// tanpa tulis, lalu retry berhasil.
	wsB := env.wsB
	beforeB := snapshotFinance(t, boot, wsB)
	t.Cleanup(func() { _, _ = boot.Exec(`GRANT INSERT ON module_entitlements TO umnia_grant_exec`) })
	if _, err := boot.Exec(`REVOKE INSERT ON module_entitlements FROM umnia_grant_exec`); err != nil {
		t.Fatalf("pasang fault INSERT entitlement: %v", err)
	}
	res = runFinanceCLI(t, bin, opURL, financeGrantArgs(wsB, financeFixtureReason)...)
	if res.code != grantExitUnavailable || !strings.Contains(res.stderr, "unavailable") {
		t.Fatalf("fault INSERT grant: want exit 1 + unavailable, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	assertFinanceSnapshotUnchanged(t, beforeB, snapshotFinance(t, boot, wsB), "fault INSERT grant")
	if _, err := boot.Exec(`GRANT INSERT ON module_entitlements TO umnia_grant_exec`); err != nil {
		t.Fatalf("restore INSERT entitlement: %v", err)
	}
	grantFinanceViaCLI(t, bin, opURL, wsB, financeFixtureReason)

	// Fault C: cabut DELETE entitlement; revoke wsB harus gagal exit 1
	// dengan baris + audit utuh, lalu retry berhasil.
	beforeC := snapshotFinance(t, boot, wsB)
	t.Cleanup(func() { _, _ = boot.Exec(`GRANT DELETE ON module_entitlements TO umnia_grant_exec`) })
	if _, err := boot.Exec(`REVOKE DELETE ON module_entitlements FROM umnia_grant_exec`); err != nil {
		t.Fatalf("pasang fault DELETE entitlement: %v", err)
	}
	res = runFinanceCLI(t, bin, opURL, financeRevokeArgs(wsB, "cabut fault DELETE")...)
	if res.code != grantExitUnavailable || !strings.Contains(res.stderr, "unavailable") {
		t.Fatalf("fault DELETE revoke: want exit 1 + unavailable, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	assertFinanceSnapshotUnchanged(t, beforeC, snapshotFinance(t, boot, wsB), "fault DELETE revoke")
	if n := countFinanceEntitlements(t, boot, wsB); n != 1 {
		t.Fatalf("fault DELETE: baris entitlement hilang (want 1, got %d)", n)
	}
	if _, err := boot.Exec(`GRANT DELETE ON module_entitlements TO umnia_grant_exec`); err != nil {
		t.Fatalf("restore DELETE entitlement: %v", err)
	}
	revokeFinanceViaCLI(t, bin, opURL, wsB, "cabut sesudah restore DELETE")
}

// --- Konkurensi ---

func TestFinanceGrantRevoke_Concurrency(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})

	// (a) Grant bersamaan pada workspace yang sama: tepat satu
	// granted, sisanya already_granted; 1 baris, 1 audit.
	const n = 8
	start := make(chan struct{})
	results := make([]cliResult, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := runGrantCLIGo(bin, opURL, financeGrantArgs(env.wsA, financeFixtureReason)...)
			if err != nil {
				results[i] = cliResult{code: -1, stderr: err.Error()}
				return
			}
			results[i] = res
		}(i)
	}
	close(start)
	wg.Wait()
	granted, already := 0, 0
	for _, res := range results {
		if res.code != 0 {
			t.Fatalf("grant bersamaan: want exit 0, got %d (%q %q)", res.code, res.stdout, res.stderr)
		}
		switch {
		case strings.HasPrefix(res.stdout, "granted "):
			granted++
		case strings.HasPrefix(res.stdout, "already_granted "):
			already++
		default:
			t.Fatalf("grant bersamaan: output tak dikenal %q", res.stdout)
		}
	}
	if granted != 1 || already != n-1 {
		t.Fatalf("grant bersamaan: want 1 granted + %d already_granted, got %d + %d", n-1, granted, already)
	}
	if c := countFinanceEntitlements(t, boot, env.wsA); c != 1 {
		t.Fatalf("grant bersamaan: want 1 baris, got %d", c)
	}
	if c := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_granted"); c != 1 {
		t.Fatalf("grant bersamaan: want 1 audit grant, got %d", c)
	}

	// (b) Revoke bersamaan: tepat satu revoked, sisanya
	// already_revoked; 0 baris, 1 audit revoke (+1 audit grant).
	results = make([]cliResult, n)
	start = make(chan struct{})
	wg = sync.WaitGroup{}
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			res, err := runGrantCLIGo(bin, opURL, financeRevokeArgs(env.wsA, "cabut bersamaan")...)
			if err != nil {
				results[i] = cliResult{code: -1, stderr: err.Error()}
				return
			}
			results[i] = res
		}(i)
	}
	close(start)
	wg.Wait()
	revoked, alreadyRev := 0, 0
	for _, res := range results {
		if res.code != 0 {
			t.Fatalf("revoke bersamaan: want exit 0, got %d (%q %q)", res.code, res.stdout, res.stderr)
		}
		switch {
		case strings.HasPrefix(res.stdout, "revoked "):
			revoked++
		case strings.HasPrefix(res.stdout, "already_revoked "):
			alreadyRev++
		default:
			t.Fatalf("revoke bersamaan: output tak dikenal %q", res.stdout)
		}
	}
	if revoked != 1 || alreadyRev != n-1 {
		t.Fatalf("revoke bersamaan: want 1 revoked + %d already_revoked, got %d + %d", n-1, revoked, alreadyRev)
	}
	if c := countFinanceEntitlements(t, boot, env.wsA); c != 0 {
		t.Fatalf("revoke bersamaan: want 0 baris, got %d", c)
	}
	if c := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_revoked"); c != 1 {
		t.Fatalf("revoke bersamaan: want 1 audit revoke, got %d", c)
	}

	// (c) Grant + revoke bersamaan pada workspace segar: hasil akhir
	// harus cocok dengan satu urutan serial — tepat satu audit grant,
	// nol/satu audit revoke, dan baris ada jika dan hanya jika tidak
	// ada audit revoke.
	for iter := 0; iter < 3; iter++ {
		ws := createWorkspace(t, env.srv, env.ownerToken, fmt.Sprintf("Usaha Finance Race %d", iter))
		t.Cleanup(func() { _, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id = $1`, ws) })
		barrier := make(chan struct{})
		var wg2 sync.WaitGroup
		var gRes, rRes cliResult
		var gErr, rErr error
		wg2.Add(2)
		go func() {
			defer wg2.Done()
			<-barrier
			gRes, gErr = runGrantCLIGo(bin, opURL, financeGrantArgs(ws, financeFixtureReason)...)
		}()
		go func() {
			defer wg2.Done()
			<-barrier
			rRes, rErr = runGrantCLIGo(bin, opURL, financeRevokeArgs(ws, "cabut race")...)
		}()
		close(barrier)
		wg2.Wait()
		if gErr != nil || rErr != nil {
			t.Fatalf("race grant/revoke iter %d: err grant=%v revoke=%v", iter, gErr, rErr)
		}
		if gRes.code != 0 || rRes.code != 0 {
			t.Fatalf("race grant/revoke iter %d: exit grant=%d revoke=%d (%q %q)", iter, gRes.code, rRes.code, gRes.stdout, rRes.stdout)
		}
		grants := countFinanceOperatorAudit(t, boot, ws, "entitlement_granted")
		revokes := countFinanceOperatorAudit(t, boot, ws, "entitlement_revoked")
		rows := countFinanceEntitlements(t, boot, ws)
		if grants != 1 || (revokes != 0 && revokes != 1) {
			t.Fatalf("race iter %d: audit tidak serial (grant=%d revoke=%d)", iter, grants, revokes)
		}
		if (rows == 1) != (revokes == 0) {
			t.Fatalf("race iter %d: baris (%d) tidak cocok dengan audit revoke (%d)", iter, rows, revokes)
		}
	}
}

// TestFinanceGrant_LockSerialization membuktikan kunci workspace
// yang sama dipakai grant finance dan aktivasi (aktivasi menunggu
// grant dalam transaksi yang ditahan).
func TestFinanceGrant_LockSerialization(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	env := setupActivation(t, nil)
	t.Cleanup(func() {
		_, _ = boot.Exec(`DELETE FROM module_entitlements WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
	})
	opDB := openOperatorDB(t, opURL)

	tx, err := opDB.Begin()
	if err != nil {
		t.Fatalf("begin grant: %v", err)
	}
	defer tx.Rollback()
	var result string
	if err := tx.QueryRow(`SELECT result FROM app.grant_finance_entitlement($1, 'finance.basic', '0.1.0', 'tes lock')`, env.wsA).Scan(&result); err != nil {
		t.Fatalf("grant dalam tx: %v", err)
	}
	if result != "granted" {
		t.Fatalf("grant dalam tx: want granted, got %q", result)
	}
	type actOut struct {
		code int
		body map[string]any
		err  error
	}
	done := make(chan actOut, 1)
	go func() {
		var buf bytes.Buffer
		_ = json.NewEncoder(&buf).Encode(map[string]string{"version": "0.1.0"})
		req, reqErr := http.NewRequest("POST", env.srv.URL+"/workspaces/"+env.wsA+"/modules/finance.basic/activate", &buf)
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
		t.Fatalf("aktivasi tidak menunggu kunci grant finance: %+v", r)
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

// TestFinanceApprovalRevoke_Race membuktikan approval dan revoke
// yang bersamaan cocok dengan satu urutan serial: approval yang
// commit sebelum revoke tetap immutable (201 + payment ada); yang
// mengeksekusi sesudah revoke ditolak (403 + tanpa payment).
func TestFinanceApprovalRevoke_Race(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, _, _ := financeChainEnv(t, "FIN-RACE-001", "fin-arace")
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "fin-arace-prop", map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)

	barrier := make(chan struct{})
	type apprOut struct {
		code int
		body map[string]any
		err  error
	}
	apprDone := make(chan apprOut, 1)
	revDone := make(chan cliResult, 1)
	go func() {
		<-barrier
		c, b, err := postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals/"+proposalID+"/approvals", env.mgrToken, []string{"fin-arace-appr"}, `{}`)
		apprDone <- apprOut{code: c, body: b, err: err}
	}()
	go func() {
		<-barrier
		res, err := runGrantCLIGo(bin, opURL, financeRevokeArgs(env.wsA, "cabut race approval")...)
		if err != nil {
			revDone <- cliResult{code: -1, stderr: err.Error()}
			return
		}
		revDone <- res
	}()
	close(barrier)
	appr := <-apprDone
	rev := <-revDone
	if appr.err != nil {
		t.Fatalf("approval race: %v", appr.err)
	}
	if rev.code != 0 {
		t.Fatalf("revoke race: want exit 0, got %d (%q %q)", rev.code, rev.stdout, rev.stderr)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah race: want 200, got %d (%v)", code, body)
	}
	payments, _ := body["payments"].([]any)
	switch appr.code {
	case http.StatusCreated:
		if len(payments) != 1 || body["paid_minor"] != "100000" {
			t.Fatalf("approval 201 tapi payment tidak ada/utuh: payments=%d paid=%v", len(payments), body["paid_minor"])
		}
	case http.StatusForbidden:
		if len(payments) != 0 || body["paid_minor"] != "0" {
			t.Fatalf("approval 403 tapi payment tertulis: payments=%d paid=%v", len(payments), body["paid_minor"])
		}
	default:
		t.Fatalf("approval race: want 201 atau 403, got %d (%v)", appr.code, appr.body)
	}
	if n := countFinanceOperatorAudit(t, boot, env.wsA, "entitlement_revoked"); n != 1 {
		t.Fatalf("race: want 1 audit revoke, got %d", n)
	}
}

// --- Marker 014 hilang / fungsi tidak ada ---

func TestFinanceGrant_Missing014FailClosed(t *testing.T) {
	_, boot, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	scratch, scratchBoot := createOwnedScratchDB(t, boot, "umnia_test_no014")
	scratchBootURL := dsnWithDB(t, bootURL, scratch)
	applyMigrationFiles(t, scratchBootURL, append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql", "013_rental_payments.sql"))
	scratchOpURL := dsnWithDB(t, opURL, scratch)

	// Fungsi finance tidak ada di DB tanpa 014.
	var fnNull bool
	if err := scratchBoot.QueryRow(`SELECT to_regprocedure('app.grant_finance_entitlement(uuid, text, text, text)') IS NULL AND to_regprocedure('app.revoke_finance_entitlement(uuid, text, text)') IS NULL`).Scan(&fnNull); err != nil || !fnNull {
		t.Fatalf("fungsi finance ada di DB tanpa 014 (harus tidak ada): %v", err)
	}
	env := setupActivation(t, nil)
	// Workspace harus dibuat DI scratch (ws utama hidup di
	// umnia_test; rutin mengunci baris workspace di DB yang sama).
	var scratchSubj, scratchWS string
	if err := scratchBoot.QueryRow(`INSERT INTO identity_subjects (email, password_hash) VALUES ('scratch-fin@test.local', 'x') RETURNING id`).Scan(&scratchSubj); err != nil {
		t.Fatalf("buat subjek scratch: %v", err)
	}
	if err := scratchBoot.QueryRow(`INSERT INTO workspaces (name, owner_subject_id) VALUES ('scratch', $1) RETURNING id`, scratchSubj).Scan(&scratchWS); err != nil {
		t.Fatalf("buat workspace scratch: %v", err)
	}
	// CLI grant/revoke finance gagal tertutup (not ready), tanpa tulis.
	for _, args := range [][]string{
		financeGrantArgs(scratchWS, financeFixtureReason),
		financeRevokeArgs(scratchWS, "cabut tanpa 014"),
	} {
		res := runGrantCLI(t, bin, scratchOpURL, args...)
		if res.code != grantExitInput || !strings.Contains(res.stderr, "not ready") {
			t.Fatalf("scratch tanpa 014 %v: want exit 2 + not ready, got %d (out=%q err=%q)", args[:2], res.code, res.stdout, res.stderr)
		}
	}
	// Rental tetap jalan (011 ada): bukti kegagalan spesifik 014.
	res := runGrantCLI(t, bin, scratchOpURL, "--workspace", scratchWS, "--module", "rental.basic", "--version", "0.1.0", "--reason", financeFixtureReason)
	if res.code != 0 {
		t.Fatalf("grant rental di scratch tanpa 014: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
	// Terapkan 014: grant finance kini berhasil.
	if code, out := applyMigration014(t, scratchBootURL); code != 0 {
		t.Fatalf("terapkan 014 ke scratch: exit %d (%s)", code, out)
	}
	res = runGrantCLI(t, bin, scratchOpURL, financeGrantArgs(scratchWS, financeFixtureReason)...)
	if res.code != 0 {
		t.Fatalf("grant finance sesudah 014: want exit 0, got %d (err=%q)", res.code, res.stderr)
	}
	// Fungsi ada tapi marker 014 dihapus: rutin menolak (U14NR) —
	// bukti marker dicek DI DALAM fungsi, bukan hanya di CLI.
	if _, err := scratchBoot.Exec(`DELETE FROM schema_migrations WHERE version = '014_finance_operator_entitlement'`); err != nil {
		t.Fatalf("hapus marker 014: %v", err)
	}
	ws2 := scratchWS
	if err := scratchBoot.QueryRow(`INSERT INTO workspaces (name, owner_subject_id) VALUES ('scratch2', $1) RETURNING id`, scratchSubj).Scan(&ws2); err != nil {
		t.Fatalf("buat workspace scratch2: %v", err)
	}
	_ = env
	res = runGrantCLI(t, bin, scratchOpURL, financeGrantArgs(ws2, financeFixtureReason)...)
	if res.code != grantExitInput || !strings.Contains(res.stderr, "not ready") {
		t.Fatalf("fungsi ada + marker hilang: want exit 2 + not ready, got %d (out=%q err=%q)", res.code, res.stdout, res.stderr)
	}
	if n := countFinanceEntitlements(t, scratchBoot, ws2); n != 0 {
		t.Fatalf("fungsi ada + marker hilang: baris tertulis (want 0, got %d)", n)
	}
}

// --- Skema segar / upgrade / rerun ---

func TestFinanceGrant_FreshSchemaUpgrade(t *testing.T) {
	_, boot, _ := financeTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	scratch, scratchBoot := createOwnedScratchDB(t, boot, "umnia_test_fin014")
	scratchURL := dsnWithDB(t, bootURL, scratch)
	files13 := append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql", "013_rental_payments.sql")
	applyMigrationFiles(t, scratchURL, files13)
	if code, out := applyMigration014(t, scratchURL); code != 0 {
		t.Fatalf("terapkan 014: exit %d (%s)", code, out)
	}
	var markers string
	if err := scratchBoot.QueryRow(`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`).Scan(&markers); err != nil {
		t.Fatalf("baca markers: %v", err)
	}
	wantMarkers := "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments,014_finance_operator_entitlement"
	if markers != wantMarkers {
		t.Fatalf("markers: want %s, got %s", wantMarkers, markers)
	}
	// Pemilik + SECURITY DEFINER + search_path kedua rutin.
	for _, fn := range []string{
		"app.grant_finance_entitlement(uuid, text, text, text)",
		"app.revoke_finance_entitlement(uuid, text, text)",
	} {
		var owner, config string
		var secdef bool
		if err := scratchBoot.QueryRow(`SELECT p.proowner::regrole::text, p.prosecdef, COALESCE(p.proconfig::text, '') FROM pg_proc p WHERE p.oid = $1::regprocedure`, fn).Scan(&owner, &secdef, &config); err != nil {
			t.Fatalf("baca rutin %s: %v", fn, err)
		}
		if owner != "umnia_grant_exec" || !secdef {
			t.Fatalf("rutin %s: owner=%q secdef=%v (want umnia_grant_exec/true)", fn, owner, secdef)
		}
		if !strings.Contains(config, "search_path=pg_catalog, pg_temp") {
			t.Fatalf("rutin %s: search_path tidak terkunci (%q)", fn, config)
		}
		var proacl string
		if err := scratchBoot.QueryRow(`SELECT proacl::text FROM pg_proc WHERE oid = $1::regprocedure`, fn).Scan(&proacl); err != nil {
			t.Fatalf("baca proacl %s: %v", fn, err)
		}
		if !strings.Contains(proacl, "umnia_operator=X/") {
			t.Fatalf("proacl %s tidak memuat EXECUTE grup: %q", fn, proacl)
		}
		if strings.Contains(proacl, "{=X/") || strings.Contains(proacl, ",=X/") {
			t.Fatalf("proacl %s memuat PUBLIC EXECUTE: %q", fn, proacl)
		}
		var execOp, execBiz bool
		if err := scratchBoot.QueryRow(`SELECT has_function_privilege('umnia_operator', $1::regprocedure, 'EXECUTE'), has_function_privilege('umnia_business', $1::regprocedure, 'EXECUTE')`, fn).Scan(&execOp, &execBiz); err != nil {
			t.Fatalf("cek EXECUTE %s: %v", fn, err)
		}
		if !execOp || execBiz {
			t.Fatalf("EXECUTE %s: grup=%v runtime=%v (want true/false)", fn, execOp, execBiz)
		}
	}
	// DELETE terbatas: hanya module_entitlements, hanya finance.
	boolCheck := func(q string, want bool, what string) {
		t.Helper()
		var got bool
		if err := scratchBoot.QueryRow(q).Scan(&got); err != nil {
			t.Fatalf("cek ACL %s: %v", what, err)
		}
		if got != want {
			t.Fatalf("cek ACL %s: want %v, got %v", what, want, got)
		}
	}
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.module_entitlements', 'DELETE')`, true, "owner DELETE entitlements")
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.operator_audit_events', 'DELETE')`, false, "owner DELETE audit")
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.module_entitlements', 'UPDATE')`, false, "owner UPDATE entitlements")
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.operator_audit_events', 'UPDATE')`, false, "owner UPDATE audit")
	boolCheck(`SELECT has_table_privilege('umnia_grant_exec', 'public.module_entitlements', 'TRUNCATE')`, false, "owner TRUNCATE entitlements")
	boolCheck(`SELECT relforcerowsecurity FROM pg_class WHERE relname = 'module_entitlements'`, true, "FORCE RLS entitlements")
	var polExpr string
	if err := scratchBoot.QueryRow(`SELECT pg_get_expr(polqual, polrelid) FROM pg_policy WHERE polrelid = 'public.module_entitlements'::regclass AND polcmd = 'd'`).Scan(&polExpr); err != nil {
		t.Fatalf("baca kebijakan DELETE: %v", err)
	}
	if !strings.Contains(polExpr, "finance.basic") || !strings.Contains(polExpr, "0.1.0") {
		t.Fatalf("kebijakan DELETE tidak terbatas finance.basic/0.1.0: %q", polExpr)
	}
	var polRoles string
	if err := scratchBoot.QueryRow(`SELECT polroles::regrole[]::text FROM pg_policy WHERE polrelid = 'public.module_entitlements'::regclass AND polcmd = 'd'`).Scan(&polRoles); err != nil {
		t.Fatalf("baca peran kebijakan DELETE: %v", err)
	}
	if !strings.Contains(polRoles, "umnia_grant_exec") {
		t.Fatalf("kebijakan DELETE bukan untuk umnia_grant_exec: %q", polRoles)
	}
	// CHECK audit memuat entitlement_revoked.
	var checkDef string
	if err := scratchBoot.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'public.operator_audit_events'::regclass AND contype = 'c' AND pg_get_constraintdef(oid) LIKE '%entitlement_revoked%'`).Scan(&checkDef); err != nil {
		t.Fatalf("baca CHECK audit: %v", err)
	}
	if !strings.Contains(checkDef, "entitlement_granted") || !strings.Contains(checkDef, "entitlement_revoked") {
		t.Fatalf("CHECK audit tidak memuat kedua action: %q", checkDef)
	}
	// Rerun 014 + rerun penuh 011+014: idempotent, marker tetap 14.
	if code, out := applyMigration014(t, scratchURL); code != 0 {
		t.Fatalf("rerun 014: exit %d (%s)", code, out)
	}
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "011_operator_entitlement.sql")); code != 0 {
		t.Fatalf("rerun 011: exit %d (%s)", code, out)
	}
	if code, out := applyMigration014(t, scratchURL); code != 0 {
		t.Fatalf("rerun 014 kedua: exit %d (%s)", code, out)
	}
	if err := scratchBoot.QueryRow(`SELECT string_agg(version, ',' ORDER BY version) FROM schema_migrations`).Scan(&markers); err != nil || markers != wantMarkers {
		t.Fatalf("markers sesudah rerun: want %s, got %s (err=%v)", wantMarkers, markers, err)
	}
	var fnCount int
	if err := scratchBoot.QueryRow(`SELECT count(*) FROM pg_proc WHERE proname IN ('grant_finance_entitlement', 'revoke_finance_entitlement') AND pronamespace = 'app'::regnamespace`).Scan(&fnCount); err != nil || fnCount != 2 {
		t.Fatalf("rutin finance sesudah rerun: want 2, got %d (err=%v)", fnCount, err)
	}
	// 014 saja tidak memenuhi readiness (tanpa 012/013 tetap error).
	scratch2, scratch2Boot := createOwnedScratchDB(t, boot, "umnia_test_fin014r")
	scratch2URL := dsnWithDB(t, bootURL, scratch2)
	applyMigrationFiles(t, scratch2URL, append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql"))
	if code, out := applyMigration014(t, scratch2URL); code != 0 {
		t.Fatalf("terapkan 014 tanpa 012/013: exit %d (%s)", code, out)
	}
	scratch2DB, err := sql.Open("pgx", scratch2URL)
	if err != nil {
		t.Fatalf("buka scratch2: %v", err)
	}
	defer scratch2DB.Close()
	_ = scratch2Boot
	if got := checkDatabase(scratch2DB); got != "error" {
		t.Fatalf("checkDatabase 001-011+014 tanpa 012/013: want error, got %q", got)
	}
}

// --- Preflight provisioning 014 ---

// extractPreflightBody membaca badan DO preflight mentah (dengan
// placeholder __V_ROLES__/__V_LOGINS__ utuh) dari satu file
// provisioning, di antara penanda awal dan akhir yang diberikan.
// Dipakai untuk membuktikan ketiga preflight byte-identik sebelum
// satu badan yang sama dieksekusi di regresi.
func extractPreflightBody(t *testing.T, path, startMarker, endMarker string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("baca preflight %s: %v", path, err)
	}
	text := string(raw)
	start := strings.Index(text, startMarker)
	if start < 0 {
		t.Fatalf("penanda awal preflight tidak ditemukan di %s", path)
	}
	start += len(startMarker)
	end := strings.Index(text[start:], endMarker)
	if end < 0 {
		t.Fatalf("penutup preflight tidak ditemukan di %s", path)
	}
	return text[start : start+end]
}

// extractConfigurePreflightSQL membaca DO preflight dari
// deploy/linux/configure-postgres.sh (heredoc yang sama dengan yang
// dieksekusi preflight_operator) dan mengisi daftar peran roster.
func extractConfigurePreflightSQL(t *testing.T) string {
	t.Helper()
	body := extractPreflightBody(t,
		filepath.Join("..", "..", "deploy", "linux", "configure-postgres.sh"),
		"cat <<'SQL'\n", "\nSQL\n)")
	body = strings.ReplaceAll(body, "__V_ROLES__", "ARRAY['umnia_operator', 'umnia_grant_exec', 'umnia_op_test']")
	body = strings.ReplaceAll(body, "__V_LOGINS__", "ARRAY['umnia_op_test']")
	return body
}

func runPreflightSQL(t *testing.T, scratchURL, sqlText string) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "preflight.sql")
	if err := os.WriteFile(path, []byte(sqlText), 0600); err != nil {
		t.Fatalf("tulis preflight.sql: %v", err)
	}
	return applyMigrationFile(t, scratchURL, path)
}

// TestFinancePreflightAllows014Delete membuktikan preflight
// provisioning mengizinkan DELETE terbatas 014 (marker + FORCE RLS +
// kebijakan finance) dan tetap menolak DELETE tanpa 014, overload
// musuh, DELETE asing, dan kebijakan yang dicabut.
func TestFinancePreflightAllows014Delete(t *testing.T) {
	_, boot, _ := financeTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	sqlText := extractConfigurePreflightSQL(t)
	files13 := append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql", "013_rental_payments.sql")

	mkScratch := func(prefix string, files []string) (string, *sql.DB) {
		t.Helper()
		name, sBoot := createOwnedScratchDB(t, boot, prefix)
		url := dsnWithDB(t, bootURL, name)
		applyMigrationFiles(t, url, files)
		return url, sBoot
	}

	// expectRejected menjalankan preflight atas satu scratch dan
	// menuntut penolakan "preflight operator" TANPA mutasi state
	// provisioning (peran/keanggotaan/password) sesudahnya.
	expectRejected := func(name, url string, db *sql.DB) {
		t.Helper()
		snap := snapshotOperatorState(t, db)
		vers := captureVerifiers(t, db)
		code, out := runPreflightSQL(t, url, sqlText)
		if code == 0 {
			t.Fatalf("preflight %s LOLOS (harus ditolak)", name)
		}
		if !strings.Contains(out, "preflight operator") {
			t.Fatalf("preflight %s gagal tanpa pesan preflight: exit %d (%s)", name, code, out)
		}
		if got := snapshotOperatorState(t, db); got != snap {
			t.Fatalf("preflight %s mengubah state peran:\nsebelum:\n%s\nsesudah:\n%s", name, snap, got)
		}
		assertVerifiersUnchanged(t, vers, captureVerifiers(t, db))
	}

	// (a) Izin: 001-014 penuh -> preflight lolos.
	url014, _ := mkScratch("umnia_test_pf014", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if code, out := runPreflightSQL(t, url014, sqlText); code != 0 {
		t.Fatalf("preflight 001-014: want exit 0, got %d (%s)", code, out)
	}
	// (b) Tolak: 001-013 + GRANT DELETE tanpa 014 (tanpa marker).
	urlPre, preBoot := mkScratch("umnia_test_pfpre", files13)
	if _, err := preBoot.Exec(`GRANT DELETE ON module_entitlements TO umnia_grant_exec`); err != nil {
		t.Fatalf("seed DELETE pre-014: %v", err)
	}
	if code, out := runPreflightSQL(t, urlPre, sqlText); code == 0 {
		t.Fatalf("preflight DELETE tanpa 014 LOLOS (harus ditolak)")
	} else if !strings.Contains(out, "preflight operator") {
		t.Fatalf("preflight DELETE tanpa 014 gagal tanpa pesan preflight: exit %d (%s)", code, out)
	}
	// (c) Tolak: overload musuh (tanda tangan lain, pemilik sama).
	urlOver, overBoot := mkScratch("umnia_test_pfover", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := overBoot.Exec(`CREATE FUNCTION app.grant_finance_entitlement(uuid) RETURNS void LANGUAGE sql AS 'SELECT 1'`); err != nil {
		t.Fatalf("buat overload musuh: %v", err)
	}
	if _, err := overBoot.Exec(`ALTER FUNCTION app.grant_finance_entitlement(uuid) OWNER TO umnia_grant_exec`); err != nil {
		t.Fatalf("alihkan overload musuh: %v", err)
	}
	if code, out := runPreflightSQL(t, urlOver, sqlText); code == 0 {
		t.Fatalf("preflight overload musuh LOLOS (harus ditolak)")
	} else if !strings.Contains(out, "preflight operator") {
		t.Fatalf("preflight overload gagal tanpa pesan preflight: exit %d (%s)", code, out)
	}
	// (d) Tolak: DELETE asing (workspaces) meski 014 ada.
	urlForeign, foreignBoot := mkScratch("umnia_test_pffor", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := foreignBoot.Exec(`GRANT DELETE ON workspaces TO umnia_grant_exec`); err != nil {
		t.Fatalf("seed DELETE asing: %v", err)
	}
	if code, out := runPreflightSQL(t, urlForeign, sqlText); code == 0 {
		t.Fatalf("preflight DELETE asing LOLOS (harus ditolak)")
	} else if !strings.Contains(out, "preflight operator") {
		t.Fatalf("preflight DELETE asing gagal tanpa pesan preflight: exit %d (%s)", code, out)
	}
	// (e) Tolak: kebijakan DELETE terbatas dicabut (GRANT tetap ada).
	urlNoPol, noPolBoot := mkScratch("umnia_test_pfnp", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := noPolBoot.Exec(`DROP POLICY IF EXISTS entitlements_exec_owner_delete_finance ON module_entitlements`); err != nil {
		t.Fatalf("cabut kebijakan DELETE: %v", err)
	}
	if code, out := runPreflightSQL(t, urlNoPol, sqlText); code == 0 {
		t.Fatalf("preflight tanpa kebijakan DELETE LOLOS (harus ditolak)")
	} else if !strings.Contains(out, "preflight operator") {
		t.Fatalf("preflight tanpa kebijakan gagal tanpa pesan preflight: exit %d (%s)", code, out)
	}
	// (f) Tolak R1: RLS dimatikan (relrowsecurity false, FORCE tetap
	// true) — cek lama hanya melihat relforcerowsecurity.
	urlNoRLS, noRLSBoot := mkScratch("umnia_test_pfdis", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := noRLSBoot.Exec(`ALTER TABLE public.module_entitlements DISABLE ROW LEVEL SECURITY`); err != nil {
		t.Fatalf("matikan RLS: %v", err)
	}
	expectRejected("RLS dimatikan", urlNoRLS, noRLSBoot)
	// (g) Tolak R1: predikat DELETE dibalik (module_id <> ...).
	// Pengenal LIKE lama menerima kedua substring + tanpa OR.
	urlInv, invBoot := mkScratch("umnia_test_pfinv", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := invBoot.Exec(`ALTER POLICY entitlements_exec_owner_delete_finance ON public.module_entitlements USING (module_id <> 'finance.basic' AND version = '0.1.0')`); err != nil {
		t.Fatalf("balik predikat DELETE: %v", err)
	}
	expectRejected("predikat DELETE terbalik", urlInv, invBoot)
	// (h) Tolak R1: kebijakan FOR ALL lebar (polcmd '*' lolos dari
	// scan polcmd='d' yang lama).
	urlAll, allBoot := mkScratch("umnia_test_pfall", append(append([]string{}, files13...), "014_finance_operator_entitlement.sql"))
	if _, err := allBoot.Exec(`CREATE POLICY qa_broad_all ON public.module_entitlements FOR ALL TO umnia_grant_exec USING(true) WITH CHECK(true)`); err != nil {
		t.Fatalf("buat kebijakan FOR ALL: %v", err)
	}
	expectRejected("kebijakan FOR ALL lebar", urlAll, allBoot)

	// Penjaga sinkron: ketiga preflight (bash Linux, PS Windows,
	// initdb) harus byte-identik — bukan sekadar memuat jarum
	// string yang sama — supaya satu badan yang dieksekusi di atas
	// adalah badan yang sama di ketiga skrip.
	linuxRaw := extractPreflightBody(t,
		filepath.Join("..", "..", "deploy", "linux", "configure-postgres.sh"),
		"cat <<'SQL'\n", "\nSQL\n)")
	initdbRaw := extractPreflightBody(t,
		filepath.Join("..", "..", "deploy", "windows", "initdb", "01-runtime-roles.sh"),
		"cat <<'SQL'\n", "\nSQL\n)")
	psRaw := extractPreflightBody(t,
		filepath.Join("..", "..", "deploy", "windows", "provision-operator.ps1"),
		"$sql = @'\n", "\n'@\n")
	if linuxRaw != initdbRaw || linuxRaw != psRaw {
		t.Fatalf("badan preflight tidak byte-identik (linux=%d initdb=%d ps=%d byte); ketiga skrip harus membawa DO yang sama persis", len(linuxRaw), len(initdbRaw), len(psRaw))
	}
}

// --- Lintas workspace / tenant / admin-bisnis ---

func TestFinanceGrant_CrossWorkspaceAndIsolation(t *testing.T) {
	_, _, opURL := financeTestEnv(t)
	bin := buildGrantCLI(t)
	env, draftID, _, _ := financeChainEnv(t, "FIN-XWS-001", "fin-xws")
	grantFinanceViaCLI(t, bin, opURL, env.wsA, financeFixtureReason)
	activateFinanceViaCLI(t, env, env.wsA, env.ownerToken)

	// Grant wsA tidak memberi hak ke wsB: aktivasi + tulis di wsB 403.
	code, body := activateModule(t, env.srv, env.wsB, "finance.basic", env.otherToken, "0.1.0")
	if code != http.StatusForbidden {
		t.Fatalf("aktivasi wsB sesudah grant wsA: want 403, got %d (%v)", code, body)
	}
	// Grant ke wsB sendiri tetap bisa (operator terskop per workspace).
	grantFinanceViaCLI(t, bin, opURL, env.wsB, financeFixtureReason)

	// Lintas-tenant langsung (RLS): runtime dengan konteks wsB tidak
	// melihat baris entitlement wsA (0 baris). Tabel audit operator
	// bahkan tidak bisa dibaca runtime sama sekali (REVOKE ALL sejak
	// 011): baca lintas-tenant harus ditolak 42501, bukan 0 baris.
	db := testDB(t)
	tx := tenantTx(t, db, env.wsB, env.otherID)
	defer tx.Rollback()
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM module_entitlements WHERE workspace_id = $1`, env.wsA).Scan(&n); err != nil {
		t.Fatalf("baca lintas-tenant entitlements: %v", err)
	}
	if n != 0 {
		t.Fatalf("lintas-tenant: entitlement wsA terlihat dari wsB (want 0, got %d)", n)
	}
	err := tx.QueryRow(`SELECT count(*) FROM operator_audit_events WHERE workspace_id = $1`, env.wsA).Scan(&n)
	assertPermissionDenied(t, err, "lintas-tenant: baca audit operator")
	_ = draftID

	// Admin-bisnis: DB admin tidak punya rutin finance maupun tabel
	// entitlement (segregasi BY ABSENCE).
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	adminURL := strings.Replace(bootURL, "/umnia_test?", "/umnia_admin?", 1)
	adminDB, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatalf("buka umnia_admin: %v", err)
	}
	defer adminDB.Close()
	if err := adminDB.Ping(); err != nil {
		t.Fatalf("ping umnia_admin: %v", err)
	}
	var adminFnNull bool
	if err := adminDB.QueryRow(`SELECT to_regprocedure('app.grant_finance_entitlement(uuid, text, text, text)') IS NULL AND to_regprocedure('app.revoke_finance_entitlement(uuid, text, text)') IS NULL`).Scan(&adminFnNull); err != nil {
		t.Fatalf("cek rutin finance di admin: %v", err)
	}
	if !adminFnNull {
		t.Fatalf("rutin finance ADA di umnia_admin (harus tidak ada)")
	}
	var adminTblNull bool
	if err := adminDB.QueryRow(`SELECT to_regclass('public.module_entitlements') IS NULL`).Scan(&adminTblNull); err != nil {
		t.Fatalf("cek tabel entitlement di admin: %v", err)
	}
	if !adminTblNull {
		t.Fatalf("tabel module_entitlements ADA di umnia_admin (harus tidak ada)")
	}
}

// TestFinanceCheckReplacementPreservesCustomCheck membuktikan R1:
// 014 §1 lama menghapus SEMUA CHECK audit yang menyebut
// 'entitlement_granted' (LIKE atas definisi). CHECK buatan QA di
// sini harus bertahan byte-identik melewati upgrade + rerun 014,
// tetap menolak audit grant invalid, sementara aksi grant/revoke
// yang sah tetap diterima dan baris audit lama tetap ada.
func TestFinanceCheckReplacementPreservesCustomCheck(t *testing.T) {
	_, boot, _ := financeTestEnv(t)
	bootURL := os.Getenv("BUSINESS_BOOTSTRAP_DATABASE_URL")
	files11 := append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql")
	name, sBoot := createOwnedScratchDB(t, boot, "umnia_test_chk014")
	url := dsnWithDB(t, bootURL, name)
	applyMigrationFiles(t, url, files11)

	// Workspace fixture untuk baris audit.
	var subjectID, wsID string
	if err := sBoot.QueryRow(`INSERT INTO identity_subjects (id, email, password_hash) VALUES (gen_random_uuid(), 'qa-check@example.test', 'x') RETURNING id`).Scan(&subjectID); err != nil {
		t.Fatalf("seed subject: %v", err)
	}
	if err := sBoot.QueryRow(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES (gen_random_uuid(), 'WS QA', $1) RETURNING id`, subjectID).Scan(&wsID); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}
	auditRow := func(action, reason, result string) {
		t.Helper()
		if _, err := sBoot.Exec(`INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result) VALUES ($1, 'finance.basic', '0.1.0', 'umnia_op_test', $2, 'operator-cli', $3, $4)`, wsID, action, reason, result); err != nil {
			t.Fatalf("insert audit %s: %v", action, err)
		}
	}
	checkDef := func() string {
		t.Helper()
		var def string
		if err := sBoot.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'public.operator_audit_events'::regclass AND conname = 'qa_nonempty_grant_reason'`).Scan(&def); err != nil {
			t.Fatalf("baca CHECK custom: %v", err)
		}
		return def
	}

	// CHECK buatan QA (repro Windows): menyebut
	// 'entitlement_granted' di definisi, tapi BUKAN CHECK action 011.
	if _, err := sBoot.Exec(`ALTER TABLE public.operator_audit_events ADD CONSTRAINT qa_nonempty_grant_reason CHECK (action <> 'entitlement_granted' OR reason <> '')`); err != nil {
		t.Fatalf("tambah CHECK custom: %v", err)
	}
	wantDef := checkDef()
	// Baris audit grant yang sah SEBELUM 014 (harus tetap ada).
	auditRow("entitlement_granted", "tes grant finance", "granted")

	// Upgrade + rerun 014 (satu transaksi psql -1 per panggilan).
	applyMigrationFiles(t, url, []string{"014_finance_operator_entitlement.sql"})
	applyMigrationFiles(t, url, []string{"014_finance_operator_entitlement.sql"})
	if got := checkDef(); got != wantDef {
		t.Fatalf("CHECK custom berubah/hilang sesudah 014: want %q, got %q", wantDef, got)
	}
	var n int
	if err := sBoot.QueryRow(`SELECT count(*) FROM public.operator_audit_events WHERE action = 'entitlement_granted' AND reason = 'tes grant finance'`).Scan(&n); err != nil {
		t.Fatalf("hitung audit lama: %v", err)
	}
	if n != 1 {
		t.Fatalf("baris audit lama hilang sesudah 014: want 1, got %d", n)
	}
	// CHECK 014 yang benar tetap terpasang dengan definisi kanonis.
	var def014 string
	if err := sBoot.QueryRow(`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conrelid = 'public.operator_audit_events'::regclass AND conname = 'operator_audit_action_check'`).Scan(&def014); err != nil {
		t.Fatalf("baca CHECK 014: %v", err)
	}
	if def014 != `CHECK ((action = ANY (ARRAY['entitlement_granted'::text, 'entitlement_revoked'::text])))` {
		t.Fatalf("CHECK 014: want definisi kanonis, got %q", def014)
	}
	// CHECK custom masih menolak audit grant invalid (reason kosong).
	if _, err := sBoot.Exec(`INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result) VALUES ($1, 'finance.basic', '0.1.0', 'umnia_op_test', 'entitlement_granted', 'operator-cli', '', 'granted')`, wsID); err == nil {
		t.Fatalf("audit grant invalid (reason kosong) LOLOS CHECK custom")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("audit grant invalid: want 23514, got %v", err)
		}
	}
	// Aksi revoke yang sah tetap diterima (enumerasi 014 hidup).
	auditRow("entitlement_revoked", "tes revoke finance", "revoked")
	// Aksi di luar enumerasi tetap ditolak CHECK 014.
	if _, err := sBoot.Exec(`INSERT INTO public.operator_audit_events (workspace_id, module_id, version, operator_id, action, source, reason, result) VALUES ($1, 'finance.basic', '0.1.0', 'umnia_op_test', 'entitlement_frozen', 'operator-cli', 'x', 'granted')`, wsID); err == nil {
		t.Fatalf("aksi audit di luar enumerasi LOLOS CHECK 014")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("aksi audit luar enumerasi: want 23514, got %v", err)
		}
	}
}

// TestFinanceCLI_ModuleValidationNoDB membuktikan R1 pada CLI
// nyata: modul tak dikenal harus exit 2 tanpa menyentuh DB —
// dengan DSN racun yang tidak terjangkau, exit 1 berarti CLI
// mencoba membuka DB dulu.
func TestFinanceCLI_ModuleValidationNoDB(t *testing.T) {
	bin := buildGrantCLI(t)
	poisoned := "postgres://umnia_op_test:ci-operator-only@localhost:59999/umnia_test?sslmode=disable"
	ws := "123e4567-e89b-12d3-a456-426614174000"
	res := runGrantCLI(t, bin, poisoned, "--workspace", ws, "--module", "finance.advanced", "--version", "0.1.0", "--reason", "tes modul")
	if res.code != grantExitInput {
		t.Fatalf("finance.advanced + DSN racun: want exit %d, got %d (out=%q err=%q)", grantExitInput, res.code, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stderr, "invalid input") {
		t.Fatalf("stderr harus berisi 'invalid input', got %q", res.stderr)
	}
	// Kontrol: modul valid dengan DSN racun yang sama tetap exit 1
	// (membuktikan DSN memang tidak terjangkau).
	res = runGrantCLI(t, bin, poisoned, "--workspace", ws, "--module", "rental.basic", "--version", "0.1.0", "--reason", "tes modul")
	if res.code != grantExitUnavailable {
		t.Fatalf("rental.basic + DSN racun: want exit %d, got %d (out=%q err=%q)", grantExitUnavailable, res.code, res.stdout, res.stderr)
	}
}
