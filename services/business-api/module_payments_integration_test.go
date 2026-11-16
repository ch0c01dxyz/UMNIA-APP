package main

// Tes F2-02-A — pencatatan pembayaran manual + pembalikan catatan
// penuh dengan approval dua aktor (migrasi 013). Semua rantai dibuat
// lewat API nyata (tanpa seeding istimewa kecuali entitlement
// finance sintetis lewat bootstrap — routine/CLI operator tetap
// rental-only, BUKAN grant nyata). Live Finance POST tetap 403
// sampai slice pemberian-entitlement finance terpisah; tes memakai
// fixture entitlement sintetis yang terisolasi per workspace.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

// setupPayments membuat env agreed-price + cleanup keempat tabel
// pembayaran untuk wsA/wsB (anak dulu supaya FK tidak menghalangi;
// cleanup ini terdaftar sesudah cleanup agreed-price sehingga
// berjalan LEBIH DULU, LIFO).
func setupPayments(t *testing.T) *activationEnv {
	t.Helper()
	env := setupAgreedPrice(t)
	t.Cleanup(func() {
		for _, tbl := range []string{"rental_payment_reversals", "rental_payment_reversal_proposals", "rental_payments", "rental_payment_proposals"} {
			_, _ = env.boot.Exec(`DELETE FROM `+tbl+` WHERE workspace_id IN ($1, $2)`, env.wsA, env.wsB)
		}
	})
	return env
}

// activateFinance memberi entitlement sintetis finance.basic 0.1.0
// (bootstrap INSERT, BUKAN grant operator) lalu mengaktifkan modul
// lewat API nyata.
func activateFinance(t *testing.T, env *activationEnv, ws, token string) {
	t.Helper()
	grantEntitlement(t, env.boot, ws, "finance.basic", "0.1.0")
	code, body := activateModule(t, env.srv, ws, "finance.basic", token, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi finance: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "finance.basic", "0.1.0", "active")
}

// activateRentalOn seperti activateRental tetapi pada workspace dan
// token yang diberikan (untuk workspace satu aktor).
func activateRentalOn(t *testing.T, env *activationEnv, ws, token string) {
	t.Helper()
	grantEntitlement(t, env.boot, ws, "rental.basic", "0.1.0")
	code, body := activateModule(t, env.srv, ws, "rental.basic", token, "0.1.0")
	if code != http.StatusOK {
		t.Fatalf("aktivasi rental: want 200, got %d (%v)", code, body)
	}
	assertState(t, body, "rental.basic", "0.1.0", "active")
}

// seedPriceOn seperti seedPrice tetapi pada workspace dan token yang
// diberikan.
func seedPriceOn(t *testing.T, env *activationEnv, ws, token, item, key string, from, to time.Time, amount, currency string) string {
	t.Helper()
	code, body := createPrice(t, env.srv, ws, item, token, key, priceBody(from, to, amount, currency))
	if code != http.StatusCreated {
		t.Fatalf("seed price %s: want 201, got %d (%v)", key, code, body)
	}
	id, _ := body["id"].(string)
	return id
}

// seedPaymentChainOn menyiapkan rantai lengkap lewat API nyata pada
// workspace yang diberikan: pelanggan + item, aktivasi Rental, satu
// versi harga, satu draft, quote, accept-price (total dari quote),
// lalu aktivasi finance. amount adalah tarif per unit (string
// kanonis); draftHours adalah lama draft dalam jam (24 -> 1 unit,
// 48 -> 2 unit). Mengembalikan draft, id fakta, dan total kanonis.
func seedPaymentChainOn(t *testing.T, env *activationEnv, ws, token, sku, keyPrefix, amount string, draftHours int) (draftID, factID, total string) {
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
	activateFinance(t, env, ws, token)
	code, body = getAgreedPrice(t, env.srv, ws, draftID, token)
	if code != http.StatusOK {
		t.Fatalf("baca fakta %s: want 200, got %d (%v)", keyPrefix, code, body)
	}
	factID, _ = body["id"].(string)
	return draftID, factID, total
}

// seedPaymentChain adalah rantai standar wsA: tarif 150000 IDR,
// draft 48 jam = 2 unit, total 300000.
func seedPaymentChain(t *testing.T, env *activationEnv, sku, keyPrefix string) (draftID, factID, total string) {
	t.Helper()
	return seedPaymentChainOn(t, env, env.wsA, env.ownerToken, sku, keyPrefix, "150000", 48)
}

// --- HTTP helpers (lima rute, pola doDraftReq / createDraftRaw) ---

func proposePayment(t *testing.T, srv *httptest.Server, ws, draft, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/payment-proposals", token, []string{key}, body)
}

func proposePaymentRaw(t *testing.T, srv *httptest.Server, ws, draft, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/payment-proposals", strings.NewReader(raw))
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

func approvePayment(t *testing.T, srv *httptest.Server, ws, draft, proposal, token, key string) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/payment-proposals/"+proposal+"/approvals", token, []string{key}, map[string]any{})
}

func approvePaymentRaw(t *testing.T, srv *httptest.Server, ws, draft, proposal, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/payment-proposals/"+proposal+"/approvals", strings.NewReader(raw))
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

func proposeReversal(t *testing.T, srv *httptest.Server, ws, draft, payment, token, key string, body any) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/payments/"+payment+"/reversal-proposals", token, []string{key}, body)
}

func proposeReversalRaw(t *testing.T, srv *httptest.Server, ws, draft, payment, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/payments/"+payment+"/reversal-proposals", strings.NewReader(raw))
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

func approveReversal(t *testing.T, srv *httptest.Server, ws, draft, payment, proposal, token, key string) (int, map[string]any) {
	t.Helper()
	return doDraftReq(t, srv, "POST", "/workspaces/"+ws+"/rental/contracts/"+draft+"/payments/"+payment+"/reversal-proposals/"+proposal+"/approvals", token, []string{key}, map[string]any{})
}

func approveReversalRaw(t *testing.T, srv *httptest.Server, ws, draft, payment, proposal, token string, keys []string, raw string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/workspaces/"+ws+"/rental/contracts/"+draft+"/payments/"+payment+"/reversal-proposals/"+proposal+"/approvals", strings.NewReader(raw))
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

func getPayments(t *testing.T, srv *httptest.Server, ws, draft, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/rental/contracts/"+draft+"/payments", token, nil)
}

// postRawGo adalah POST mentah yang aman dari goroutine (tanpa
// t.Fatalf di dalam goroutine; galat dikembalikan, bukan Fatalf).
func postRawGo(srv *httptest.Server, path, token string, keys []string, raw string) (int, map[string]any, error) {
	var buf bytes.Buffer
	buf.WriteString(raw)
	req, err := http.NewRequest(http.MethodPost, srv.URL+path, &buf)
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

// postRawGoCtx seperti postRawGo tetapi dengan context pemanggil:
// request yang terblokir gagal cepat saat context berakhir.
func postRawGoCtx(ctx context.Context, srv *httptest.Server, path, token string, keys []string, raw string) (int, map[string]any, error) {
	var buf bytes.Buffer
	buf.WriteString(raw)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+path, &buf)
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

// --- Pembaca DB langsung (semua string, pola storedAgreedPrice) ---

type storedPaymentProposal struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, ClientRequestID, AmountMinor, Currency, Method string
	Reference                                                                                              sql.NullString
	ProposedBy, ProposedAt                                                                                 string
}

func readPaymentProposal(t *testing.T, boot *sql.DB, ws, id string) (storedPaymentProposal, bool) {
	t.Helper()
	var p storedPaymentProposal
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, rental_agreed_price_id::text,
			client_request_id, amount_minor::text, currency, method, reference, proposed_by::text, proposed_at::text
		 FROM rental_payment_proposals WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID,
		&p.ClientRequestID, &p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt)
	if err == sql.ErrNoRows {
		return storedPaymentProposal{}, false
	}
	if err != nil {
		t.Fatalf("read proposal %s: %v", id, err)
	}
	return p, true
}

type storedPayment struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, ProposalID, ClientRequestID, AmountMinor, Currency, Method string
	Reference                                                                                                          sql.NullString
	ProposedBy, ProposedAt, ApprovedBy, ApprovedAt                                                                     string
}

func readPayment(t *testing.T, boot *sql.DB, ws, id string) (storedPayment, bool) {
	t.Helper()
	var p storedPayment
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, rental_agreed_price_id::text, proposal_id::text,
			client_request_id, amount_minor::text, currency, method, reference, proposed_by::text, proposed_at::text,
			approved_by::text, approved_at::text
		 FROM rental_payments WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID, &p.ProposalID,
		&p.ClientRequestID, &p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt,
		&p.ApprovedBy, &p.ApprovedAt)
	if err == sql.ErrNoRows {
		return storedPayment{}, false
	}
	if err != nil {
		t.Fatalf("read payment %s: %v", id, err)
	}
	return p, true
}

type storedReversalProposal struct {
	ID, WorkspaceID, RentalContractID, PaymentID, ClientRequestID, Reason string
	ProposedBy, ProposedAt                                                string
}

func readReversalProposal(t *testing.T, boot *sql.DB, ws, id string) (storedReversalProposal, bool) {
	t.Helper()
	var p storedReversalProposal
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, payment_id::text,
			client_request_id, reason, proposed_by::text, proposed_at::text
		 FROM rental_payment_reversal_proposals WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID,
		&p.ClientRequestID, &p.Reason, &p.ProposedBy, &p.ProposedAt)
	if err == sql.ErrNoRows {
		return storedReversalProposal{}, false
	}
	if err != nil {
		t.Fatalf("read reversal proposal %s: %v", id, err)
	}
	return p, true
}

type storedReversal struct {
	ID, WorkspaceID, RentalContractID, PaymentID, ReversalProposalID, ClientRequestID, Reason string
	ProposedBy, ProposedAt, ApprovedBy, ApprovedAt                                            string
}

func readReversal(t *testing.T, boot *sql.DB, ws, id string) (storedReversal, bool) {
	t.Helper()
	var p storedReversal
	err := boot.QueryRow(
		`SELECT id::text, workspace_id::text, rental_contract_id::text, payment_id::text, reversal_proposal_id::text,
			client_request_id, reason, proposed_by::text, proposed_at::text, approved_by::text, approved_at::text
		 FROM rental_payment_reversals WHERE workspace_id = $1 AND id = $2`,
		ws, id).Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID, &p.ReversalProposalID,
		&p.ClientRequestID, &p.Reason, &p.ProposedBy, &p.ProposedAt, &p.ApprovedBy, &p.ApprovedAt)
	if err == sql.ErrNoRows {
		return storedReversal{}, false
	}
	if err != nil {
		t.Fatalf("read reversal %s: %v", id, err)
	}
	return p, true
}

// paymentSnapshot adalah hitungan eksak keempat tabel + keempat audit
// + fakta harga (untuk bukti fault tidak mengubah state).
type paymentSnapshot struct {
	proposals, payments, revProposals, reversals int
	proposed, posted, revProposed, revPosted     int
	fact                                         storedAgreedPrice
}

func snapshotPayments(t *testing.T, env *activationEnv, factID string) paymentSnapshot {
	t.Helper()
	var s paymentSnapshot
	s.proposals = countRows(t, env.boot, "rental_payment_proposals", env.wsA)
	s.payments = countRows(t, env.boot, "rental_payments", env.wsA)
	s.revProposals = countRows(t, env.boot, "rental_payment_reversal_proposals", env.wsA)
	s.reversals = countRows(t, env.boot, "rental_payment_reversals", env.wsA)
	s.proposed = countBusinessAudits(t, env.boot, env.wsA, "rental_payment_proposed")
	s.posted = countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted")
	s.revProposed = countBusinessAudits(t, env.boot, env.wsA, "rental_payment_reversal_proposed")
	s.revPosted = countBusinessAudits(t, env.boot, env.wsA, "rental_payment_reversal_posted")
	fact, ok := readAgreedPrice(t, env.boot, env.wsA, factID)
	if !ok {
		t.Fatalf("snapshot: fakta %s tidak ada", factID)
	}
	s.fact = fact
	return s
}

func assertPaymentSnapshotUnchanged(t *testing.T, before, after paymentSnapshot) {
	t.Helper()
	if before != after {
		t.Fatalf("fault mengubah state: sebelum=%+v sesudah=%+v", before, after)
	}
}

// installPaymentTrigger memasang trigger fault sintetis (pola
// TestCustodyResolutionFaults) pada tabel yang diberikan; cleanup
// melepasnya.
func installPaymentTrigger(t *testing.T, boot *sql.DB, name, fn, table, when, cond string) {
	t.Helper()
	if _, err := boot.Exec(fmt.Sprintf(`
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
	if _, err := boot.Exec(fmt.Sprintf(`CREATE TRIGGER %s %s ON %s FOR EACH ROW EXECUTE FUNCTION app.%s()`, name, when, table, fn)); err != nil {
		t.Fatalf("pasang trigger fault %s: %v", name, err)
	}
	t.Cleanup(func() {
		_, _ = boot.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table))
		_, _ = boot.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, fn))
	})
}

func dropPaymentTrigger(t *testing.T, boot *sql.DB, name, fn, table string) {
	t.Helper()
	if _, err := boot.Exec(fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON %s`, name, table)); err != nil {
		t.Fatalf("lepas trigger %s: %v", name, err)
	}
	if _, err := boot.Exec(fmt.Sprintf(`DROP FUNCTION IF EXISTS app.%s()`, fn)); err != nil {
		t.Fatalf("hapus fungsi %s: %v", name, err)
	}
}

// payFullCycle melakukan satu siklus lengkap lewat API nyata:
// propose + approve pembayaran, lalu propose + approve pembalikan
// penuh. Mengembalikan id pembayaran dan id pembalikan posted.
func payFullCycle(t *testing.T, env *activationEnv, draftID, keyPrefix, amount string) (paymentID, reversalID string) {
	t.Helper()
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, keyPrefix+"-p", map[string]any{
		"amount_minor": amount, "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("cycle propose %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, keyPrefix+"-a")
	if code != http.StatusCreated {
		t.Fatalf("cycle approve %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	paymentID, _ = body["id"].(string)
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, keyPrefix+"-rp", map[string]any{
		"reason": "koreksi " + keyPrefix,
	})
	if code != http.StatusCreated {
		t.Fatalf("cycle propose reversal %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	revProposalID, _ := body["id"].(string)
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.ownerToken, keyPrefix+"-ra")
	if code != http.StatusCreated {
		t.Fatalf("cycle approve reversal %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	reversalID, _ = body["id"].(string)
	return paymentID, reversalID
}

// totalsFromGET membaca total dari proyeksi GET (semua string).
func totalsFromGET(t *testing.T, env *activationEnv, draftID string) (total, paid, outstanding string) {
	t.Helper()
	code, body := getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET totals: want 200, got %d (%v)", code, body)
	}
	total, _ = body["total_minor"].(string)
	paid, _ = body["paid_minor"].(string)
	outstanding, _ = body["outstanding_minor"].(string)
	return total, paid, outstanding
}

func TestPaymentProposalHasNoBalanceEffect(t *testing.T) {
	env := setupPayments(t)
	draftID, factID, total := seedPaymentChain(t, env, "SKU-PAY-01", "pay-01")
	if total != "300000" {
		t.Fatalf("total rantai: want 300000, got %s", total)
	}
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-01-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	if body["amount_minor"] != "100000" || body["currency"] != "IDR" || body["method"] != "cash" {
		t.Fatalf("DTO proposal: payload tidak sama: %v", body)
	}
	if body["reference"] != nil {
		t.Fatalf("DTO proposal: reference harus null, got %v", body["reference"])
	}
	if body["rental_contract_id"] != draftID || body["rental_agreed_price_id"] != factID {
		t.Fatalf("DTO proposal: target tidak sama: %v", body)
	}
	if body["proposed_by"] != env.ownerID {
		t.Fatalf("DTO proposal: proposed_by want %s, got %v", env.ownerID, body["proposed_by"])
	}
	if body["status"] != "pending" {
		t.Fatalf("DTO proposal: status want pending, got %v", body["status"])
	}
	// Baris DB eksak: reference NULL, proposed_by owner, waktu finite.
	stored, ok := readPaymentProposal(t, env.boot, env.wsA, proposalID)
	if !ok {
		t.Fatalf("proposal DB tidak ada")
	}
	if stored.AmountMinor != "100000" || stored.Currency != "IDR" || stored.Method != "cash" || stored.Reference.Valid {
		t.Fatalf("proposal DB: payload tidak sama: %+v", stored)
	}
	if stored.ProposedBy != env.ownerID || stored.RentalContractID != draftID || stored.RentalAgreedPriceID != factID {
		t.Fatalf("proposal DB: target/aktor tidak sama: %+v", stored)
	}
	// Proposal TIDAK memengaruhi total dan TIDAK muncul di paid.
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "0" || outstanding != "300000" {
		t.Fatalf("GET sesudah propose: want total 300000 paid 0 outstanding 300000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET: want 200, got %d (%v)", code, body)
	}
	pending, _ := body["pending_payment_proposals"].([]any)
	if len(pending) != 1 {
		t.Fatalf("pending proposals: want 1, got %d (%v)", len(pending), body)
	}
	payments, _ := body["payments"].([]any)
	if len(payments) != 0 {
		t.Fatalf("payments: want 0, got %d", len(payments))
	}
	// Audit: proposed 1, posted 0.
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_proposed"); n != 1 {
		t.Fatalf("audit proposed: want 1, got %d", n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted"); n != 0 {
		t.Fatalf("audit posted: want 0, got %d", n)
	}
}

func TestPaymentApprovalTwoActor(t *testing.T) {
	env := setupPayments(t)
	draftID, factID, _ := seedPaymentChain(t, env, "SKU-PAY-02", "pay-02")
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-02-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "transfer", "reference": "RC-2026-001",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	proposedAt, _ := body["proposed_at"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-02-a1")
	if code != http.StatusCreated {
		t.Fatalf("approve: want 201, got %d (%v)", code, body)
	}
	paymentID, _ := body["id"].(string)
	if body["proposal_id"] != proposalID || body["amount_minor"] != "100000" || body["currency"] != "IDR" ||
		body["method"] != "transfer" || body["reference"] != "RC-2026-001" {
		t.Fatalf("DTO fakta: payload tidak disalin verbatim: %v", body)
	}
	if body["rental_contract_id"] != draftID || body["rental_agreed_price_id"] != factID {
		t.Fatalf("DTO fakta: target tidak sama: %v", body)
	}
	if body["proposed_by"] != env.ownerID || body["approved_by"] != env.mgrID {
		t.Fatalf("DTO fakta: aktor want owner/manager, got %v/%v", body["proposed_by"], body["approved_by"])
	}
	approvedAt, _ := body["approved_at"].(string)
	pa, err1 := time.Parse(time.RFC3339Nano, proposedAt)
	aa, err2 := time.Parse(time.RFC3339Nano, approvedAt)
	if err1 != nil || err2 != nil || aa.Before(pa) {
		t.Fatalf("DTO fakta: waktu tidak terurut: proposed %q approved %q (%v/%v)", proposedAt, approvedAt, err1, err2)
	}
	if body["reversed"] != false {
		t.Fatalf("DTO fakta: reversed want false, got %v", body["reversed"])
	}
	// Baris DB eksak.
	stored, ok := readPayment(t, env.boot, env.wsA, paymentID)
	if !ok {
		t.Fatalf("fakta DB tidak ada")
	}
	if stored.AmountMinor != "100000" || stored.Currency != "IDR" || stored.Method != "transfer" ||
		!stored.Reference.Valid || stored.Reference.String != "RC-2026-001" {
		t.Fatalf("fakta DB: payload tidak sama: %+v", stored)
	}
	if stored.ProposedBy != env.ownerID || stored.ApprovedBy != env.mgrID || stored.ProposalID != proposalID {
		t.Fatalf("fakta DB: aktor/proposal tidak sama: %+v", stored)
	}
	// Total berubah: paid 100000, outstanding 200000; proposal tidak
	// lagi pending.
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "100000" || outstanding != "200000" {
		t.Fatalf("GET sesudah approve: want 300000/100000/200000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	pending, _ := body["pending_payment_proposals"].([]any)
	if len(pending) != 0 {
		t.Fatalf("pending sesudah approve: want 0, got %d", len(pending))
	}
	payments, _ := body["payments"].([]any)
	if len(payments) != 1 {
		t.Fatalf("payments: want 1, got %d", len(payments))
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted"); n != 1 {
		t.Fatalf("audit posted: want 1, got %d", n)
	}
}

func TestPaymentApprovalSelfApprovalDenied(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-03", "pay-03")
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-03-p1", map[string]any{
		"amount_minor": "50000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.ownerToken, "pay-03-a1")
	if code != http.StatusForbidden {
		t.Fatalf("self-approve: want 403, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "penyetuju harus berbeda dari pengusul")
	}
	// Tanpa fakta, tanpa audit posted; proposal tetap pending.
	if n := countRows(t, env.boot, "rental_payments", env.wsA); n != 0 {
		t.Fatalf("fakta sesudah self-approve: want 0, got %d", n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted"); n != 0 {
		t.Fatalf("audit posted sesudah self-approve: want 0, got %d", n)
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); paid != "0" || outstanding != "300000" || gotTotal != "300000" {
		t.Fatalf("GET sesudah self-approve: want 300000/0/300000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
}

func TestPaymentSingleActorFailsClosed(t *testing.T) {
	env := setupPayments(t)
	// Workspace baru dengan satu aktor saja (owner pembuat).
	token := registerAndLogin(t, env.srv, fmt.Sprintf("solo-%d@contoh.id", time.Now().UnixNano()))
	ws := createWorkspace(t, env.srv, token, "WS Solo")
	// Bersihkan rantai di workspace baru ini: cleanup helper bawaan
	// hanya mencakup wsA/wsB env, bukan workspace segar — tanpa ini
	// barisnya bocor dan tes FreshSchema (0 baris pada skema segar)
	// gagal di run berikutnya.
	t.Cleanup(func() {
		for _, table := range []string{
			"rental_payment_reversal_proposals", "rental_payment_reversals",
			"rental_payment_proposals", "rental_payments",
			"rental_agreed_prices", "rental_contracts", "price_versions",
			"catalog_items", "parties",
			// Instalasi + entitlement modul yang diaktifkan rantai
			// (rental + finance) pada workspace ini juga.
			"module_installations", "module_entitlements",
		} {
			if _, err := env.boot.Exec(`DELETE FROM `+table+` WHERE workspace_id = $1`, ws); err != nil {
				t.Errorf("cleanup solo %s: %v", table, err)
			}
		}
	})
	draftID, _, _ := seedPaymentChainOn(t, env, ws, token, "SKU-PAY-SOLO", "pay-solo", "150000", 48)
	code, body := proposePayment(t, env.srv, ws, draftID, token, "pay-solo-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose solo: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, ws, draftID, proposalID, token, "pay-solo-a1")
	if code != http.StatusForbidden {
		t.Fatalf("approve solo: want 403, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "penyetuju harus berbeda dari pengusul")
	}
	if n := countRows(t, env.boot, "rental_payments", ws); n != 0 {
		t.Fatalf("fakta solo: want 0, got %d", n)
	}
}

func TestPaymentCashierRevokeDeactivate(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-04", "pay-04")
	// Kasir: tulis 403, baca histori 200.
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.cashToken, "pay-04-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusForbidden {
		t.Fatalf("propose kasir: want 403, got %d (%v)", code, body)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.cashToken)
	if code != http.StatusOK {
		t.Fatalf("GET kasir: want 200, got %d (%v)", code, body)
	}
	// Cabut entitlement finance: tulis baru 403, histori tetap 200.
	revokeEntitlement(t, env.boot, env.wsA, "finance.basic")
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-04-p2", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusForbidden {
		t.Fatalf("propose sesudah revoke: want 403, got %d (%v)", code, body)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah revoke: want 200, got %d (%v)", code, body)
	}
	// Beri lagi entitlement: tulis bisa lagi (membuktikan revoke yang
	// memblokir, bukan state lain).
	grantEntitlement(t, env.boot, env.wsA, "finance.basic", "0.1.0")
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-04-p3", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah re-grant: want 201, got %d (%v)", code, body)
	}
	// Deactivate modul: tulis baru 403, histori tetap 200.
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deactivate finance: want 200, got %d (%v)", code, body)
	}
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-04-p4", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusForbidden {
		t.Fatalf("propose sesudah deactivate: want 403, got %d (%v)", code, body)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("GET sesudah deactivate: want 200, got %d (%v)", code, body)
	}
}

func TestPaymentReplayFourKeyspaces(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-05", "pay-05")
	// (1) Proposal: kunci sama + payload sama -> 200, tanpa baris/
	// audit baru; payload berubah -> 409.
	proposalBody := map[string]any{"amount_minor": "100000", "currency": "IDR", "method": "cash", "reference": "RC-1"}
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-05-p1", proposalBody)
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, replay := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-05-p1", proposalBody)
	if code != http.StatusOK || replay["id"] != proposalID {
		t.Fatalf("replay propose: want 200 id sama, got %d (%v)", code, replay)
	}
	if n := countRows(t, env.boot, "rental_payment_proposals", env.wsA); n != 1 {
		t.Fatalf("proposal sesudah replay: want 1, got %d", n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_proposed"); n != 1 {
		t.Fatalf("audit proposed sesudah replay: want 1, got %d", n)
	}
	changed := map[string]any{"amount_minor": "100001", "currency": "IDR", "method": "cash", "reference": "RC-1"}
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-05-p1", changed)
	if code != http.StatusConflict {
		t.Fatalf("replay propose payload berubah: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
	}
	// Kunci yang sama di keyspace lain (approval) adalah permintaan
	// berbeda: approval memakai kunci "pay-05-p1" untuk proposal ini
	// harus 201 (keyspace approval terpisah), bukan 409.
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-05-p1")
	if code != http.StatusCreated {
		t.Fatalf("approve kunci sama keyspace lain: want 201, got %d (%v)", code, body)
	}
	paymentID, _ := body["id"].(string)
	// (2) Approval: replay kunci sama -> 200; kunci berbeda untuk
	// proposal yang sudah posted -> 409.
	code, replay = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-05-p1")
	if code != http.StatusOK || replay["id"] != paymentID {
		t.Fatalf("replay approve: want 200 id sama, got %d (%v)", code, replay)
	}
	if n := countRows(t, env.boot, "rental_payments", env.wsA); n != 1 {
		t.Fatalf("fakta sesudah replay approve: want 1, got %d", n)
	}
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-05-a2")
	if code != http.StatusConflict {
		t.Fatalf("approve kedua kunci berbeda: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "proposal sudah disetujui")
	}
	// (3) Reversal proposal: replay -> 200; kunci sama isi berubah
	// -> 409.
	revBody := map[string]any{"reason": "kas salah catat"}
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, "pay-05-rp1", revBody)
	if code != http.StatusCreated {
		t.Fatalf("propose reversal: want 201, got %d (%v)", code, body)
	}
	revProposalID, _ := body["id"].(string)
	code, replay = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, "pay-05-rp1", revBody)
	if code != http.StatusOK || replay["id"] != revProposalID {
		t.Fatalf("replay propose reversal: want 200 id sama, got %d (%v)", code, replay)
	}
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, "pay-05-rp1", map[string]any{"reason": "alasan lain"})
	if code != http.StatusConflict {
		t.Fatalf("replay propose reversal alasan berubah: want 409, got %d (%v)", code, body)
	}
	// (4) Reversal approval: 201, replay -> 200, kunci berbeda -> 409.
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.ownerToken, "pay-05-ra1")
	if code != http.StatusCreated {
		t.Fatalf("approve reversal: want 201, got %d (%v)", code, body)
	}
	reversalID, _ := body["id"].(string)
	code, replay = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.ownerToken, "pay-05-ra1")
	if code != http.StatusOK || replay["id"] != reversalID {
		t.Fatalf("replay approve reversal: want 200 id sama, got %d (%v)", code, replay)
	}
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.ownerToken, "pay-05-ra2")
	if code != http.StatusConflict {
		t.Fatalf("approve reversal kedua kunci berbeda: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "pembayaran sudah dibatalkan")
	}
	// Replay proposal + approval ASLI sesudah pembalikan penuh (akses
	// masih valid): keduanya 200, tanpa fakta/audit baru.
	code, replay = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-05-p1", proposalBody)
	if code != http.StatusOK || replay["id"] != proposalID {
		t.Fatalf("replay propose sesudah reversal: want 200, got %d (%v)", code, replay)
	}
	code, replay = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-05-p1")
	if code != http.StatusOK || replay["id"] != paymentID {
		t.Fatalf("replay approve sesudah reversal: want 200, got %d (%v)", code, replay)
	}
	if n := countRows(t, env.boot, "rental_payments", env.wsA); n != 1 {
		t.Fatalf("fakta sesudah replay pasca-reversal: want 1, got %d", n)
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted"); n != 1 {
		t.Fatalf("audit posted sesudah replay pasca-reversal: want 1, got %d", n)
	}
}

func TestPaymentConcurrentApprovals(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-06", "pay-06")
	// Dua proposal (masing-masing 200000 dari total 300000): approve
	// bersamaan -> satu 201 + satu 409, total tepat.
	var proposalA, proposalB string
	for i, pfx := range []string{"pay-06-a", "pay-06-b"} {
		code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, pfx+"-p", map[string]any{
			"amount_minor": "200000", "currency": "IDR", "method": "cash",
		})
		if code != http.StatusCreated {
			t.Fatalf("propose %s: want 201, got %d (%v)", pfx, code, body)
		}
		id, _ := body["id"].(string)
		if i == 0 {
			proposalA = id
		} else {
			proposalB = id
		}
	}
	type result struct {
		code int
		body map[string]any
		err  error
	}
	run := func(proposal, key string) result {
		code, body, err := postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals/"+proposal+"/approvals", env.mgrToken, []string{key}, "{}")
		return result{code, body, err}
	}
	ch := make(chan result, 2)
	go func() { ch <- run(proposalA, "pay-06-a-approve") }()
	go func() { ch <- run(proposalB, "pay-06-b-approve") }()
	r1, r2 := <-ch, <-ch
	if r1.err != nil || r2.err != nil {
		t.Fatalf("approve konkuren: galat %v / %v", r1.err, r2.err)
	}
	codes := map[int]int{r1.code: 1, r2.code: 1}
	if codes[http.StatusCreated] != 1 || codes[http.StatusConflict] != 1 {
		t.Fatalf("approve konkuren: want 201+409, got %d/%d (%v/%v)", r1.code, r2.code, r1.body, r2.body)
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "200000" || outstanding != "100000" {
		t.Fatalf("GET sesudah race plafon: want 300000/200000/100000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	// Kunci SAMA + payload identik, dua approval bersamaan untuk satu
	// proposal -> satu 201 + satu 200.
	draftID2, _, _ := seedPaymentChain(t, env, "SKU-PAY-06B", "pay-06b")
	code, body := proposePayment(t, env.srv, env.wsA, draftID2, env.ownerToken, "pay-06b-p", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose same-key: want 201, got %d (%v)", code, body)
	}
	proposalC, _ := body["id"].(string)
	runSame := func() result {
		code, body, err := postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID2+"/payment-proposals/"+proposalC+"/approvals", env.mgrToken, []string{"pay-06b-approve"}, "{}")
		return result{code, body, err}
	}
	ch2 := make(chan result, 2)
	go func() { ch2 <- runSame() }()
	go func() { ch2 <- runSame() }()
	s1, s2 := <-ch2, <-ch2
	if s1.err != nil || s2.err != nil {
		t.Fatalf("approve same-key konkuren: galat %v / %v", s1.err, s2.err)
	}
	codes2 := map[int]int{s1.code: 1, s2.code: 1}
	if codes2[http.StatusCreated] != 1 || codes2[http.StatusOK] != 1 {
		t.Fatalf("approve same-key konkuren: want 201+200, got %d/%d (%v/%v)", s1.code, s2.code, s1.body, s2.body)
	}
	if n := countRows(t, env.boot, "rental_payments", env.wsA); n != 2 {
		t.Fatalf("fakta sesudah same-key race: want 2, got %d", n)
	}
	// Kunci BERBEDA untuk SATU proposal yang sama, bersamaan -> satu
	// 201 + satu 409 (target sudah posted).
	draftID3, _, _ := seedPaymentChain(t, env, "SKU-PAY-06C", "pay-06c")
	code, body = proposePayment(t, env.srv, env.wsA, draftID3, env.ownerToken, "pay-06c-p", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose diff-key: want 201, got %d (%v)", code, body)
	}
	proposalD, _ := body["id"].(string)
	runDiff := func(key string) result {
		code, body, err := postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID3+"/payment-proposals/"+proposalD+"/approvals", env.mgrToken, []string{key}, "{}")
		return result{code, body, err}
	}
	ch3 := make(chan result, 2)
	go func() { ch3 <- runDiff("pay-06c-a1") }()
	go func() { ch3 <- runDiff("pay-06c-a2") }()
	d1, d2 := <-ch3, <-ch3
	if d1.err != nil || d2.err != nil {
		t.Fatalf("approve diff-key konkuren: galat %v / %v", d1.err, d2.err)
	}
	codes3 := map[int]int{d1.code: 1, d2.code: 1}
	if codes3[http.StatusCreated] != 1 || codes3[http.StatusConflict] != 1 {
		t.Fatalf("approve diff-key konkuren: want 201+409, got %d/%d (%v/%v)", d1.code, d2.code, d1.body, d2.body)
	}
}

func TestPaymentApprovalAfterCapConsumed(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-07", "pay-07")
	var proposalA, proposalB string
	for i, pfx := range []string{"pay-07-a", "pay-07-b"} {
		code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, pfx+"-p", map[string]any{
			"amount_minor": "200000", "currency": "IDR", "method": "cash",
		})
		if code != http.StatusCreated {
			t.Fatalf("propose %s: want 201, got %d (%v)", pfx, code, body)
		}
		id, _ := body["id"].(string)
		if i == 0 {
			proposalA = id
		} else {
			proposalB = id
		}
	}
	code, body := approvePayment(t, env.srv, env.wsA, draftID, proposalA, env.mgrToken, "pay-07-a-approve")
	if code != http.StatusCreated {
		t.Fatalf("approve A: want 201, got %d (%v)", code, body)
	}
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalB, env.mgrToken, "pay-07-b-approve")
	if code != http.StatusConflict {
		t.Fatalf("approve B sesudah plafon habis: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "pembayaran melebihi sisa")
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "200000" || outstanding != "100000" {
		t.Fatalf("GET sesudah cap: want 300000/200000/100000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
}

func TestPaymentMoneyBoundaries(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-08", "pay-08")
	// Bentuk jumlah tidak valid -> 400 (satu per satu, kunci unik).
	invalidAmounts := []string{
		`"0"`, `"00"`, `"01"`, `"-5"`, `"+5"`, `"1.5"`, `"1e3"`, `" 100"`, `"100 "`, `""`, `"1_000"`,
		`100`, `null`, `true`, `[]`, `{}`,
	}
	for i, raw := range invalidAmounts {
		key := fmt.Sprintf("pay-08-bad-%d", i)
		body := `{"amount_minor": ` + raw + `, "currency": "IDR", "method": "cash"}`
		code, out := proposePaymentRaw(t, env.srv, env.wsA, draftID, env.ownerToken, []string{key}, body)
		if code != http.StatusBadRequest {
			t.Fatalf("amount %s: want 400, got %d (%v)", raw, code, out)
		}
	}
	// Jumlah hilang -> 400.
	code, out := proposePaymentRaw(t, env.srv, env.wsA, draftID, env.ownerToken, []string{"pay-08-noamt"}, `{"currency": "IDR", "method": "cash"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("amount hilang: want 400, got %d (%v)", code, out)
	}
	// Currency format salah -> 400; format benar tapi beda dari fakta
	// -> 409.
	for _, cur := range []string{"idr", "ID", "IDRR", ""} {
		code, out := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-cur-"+cur, map[string]any{
			"amount_minor": "1000", "currency": cur, "method": "cash",
		})
		if code != http.StatusBadRequest {
			t.Fatalf("currency %q: want 400, got %d (%v)", cur, code, out)
		}
	}
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-cur-usd", map[string]any{
		"amount_minor": "1000", "currency": "USD", "method": "cash",
	})
	if code != http.StatusConflict {
		t.Fatalf("currency USD: want 409, got %d (%v)", code, out)
	} else {
		assertFlatError(t, out, "mata uang tidak cocok")
	}
	// Method di luar enum -> 400.
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-meth", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "qris",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("method qris: want 400, got %d (%v)", code, out)
	}
	// Reference: kosong / hanya-whitespace / 129 rune -> 400;
	// 128 rune Unicode -> 201; spasi di ujung disimpan VERBATIM.
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-ref-empty", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash", "reference": "",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("reference kosong: want 400, got %d (%v)", code, out)
	}
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-ref-ws", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash", "reference": "   ",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("reference whitespace: want 400, got %d (%v)", code, out)
	}
	longRef := strings.Repeat("é", 129)
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-ref-long", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash", "reference": longRef,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("reference 129 rune: want 400, got %d (%v)", code, out)
	}
	if utf8.RuneCountInString(longRef) != 129 {
		t.Fatalf("fixture: longRef harus 129 rune")
	}
	ref128 := strings.Repeat("é", 128)
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-ref-128", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash", "reference": ref128,
	})
	if code != http.StatusCreated {
		t.Fatalf("reference 128 rune: want 201, got %d (%v)", code, body)
	}
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-ref-verbatim", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash", "reference": "  RC-1  ",
	})
	if code != http.StatusCreated {
		t.Fatalf("reference verbatim: want 201, got %d (%v)", code, body)
	}
	if body["reference"] != "  RC-1  " {
		t.Fatalf("reference harus disimpan verbatim, got %v", body["reference"])
	}
	// MaxInt64: propose 201 (tanpa cek plafon di proposal), approve
	// 409 (melebihi sisa 300000); MaxInt64+1 -> 400.
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-max", map[string]any{
		"amount_minor": "9223372036854775807", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose MaxInt64: want 201, got %d (%v)", code, body)
	}
	maxProposal, _ := body["id"].(string)
	code, out = approvePayment(t, env.srv, env.wsA, draftID, maxProposal, env.mgrToken, "pay-08-max-a")
	if code != http.StatusConflict {
		t.Fatalf("approve MaxInt64: want 409, got %d (%v)", code, out)
	} else {
		assertFlatError(t, out, "pembayaran melebihi sisa")
	}
	code, out = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-maxplus", map[string]any{
		"amount_minor": "9223372036854775808", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("propose MaxInt64+1: want 400, got %d (%v)", code, out)
	}
	// Total nol: proposal "1" inert (201), approve 409; propose "0"
	// -> 400.
	zeroDraft, _, zeroTotal := seedPaymentChainOn(t, env, env.wsA, env.ownerToken, "SKU-PAY-08Z", "pay-08z", "0", 48)
	if zeroTotal != "0" {
		t.Fatalf("total nol: want 0, got %s", zeroTotal)
	}
	code, body = proposePayment(t, env.srv, env.wsA, zeroDraft, env.ownerToken, "pay-08z-p1", map[string]any{
		"amount_minor": "1", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose total-nol: want 201, got %d (%v)", code, body)
	}
	zeroProposal, _ := body["id"].(string)
	code, out = approvePayment(t, env.srv, env.wsA, zeroDraft, zeroProposal, env.mgrToken, "pay-08z-a1")
	if code != http.StatusConflict {
		t.Fatalf("approve total-nol: want 409, got %d (%v)", code, out)
	} else {
		assertFlatError(t, out, "pembayaran melebihi sisa")
	}
	code, out = proposePayment(t, env.srv, env.wsA, zeroDraft, env.ownerToken, "pay-08z-p0", map[string]any{
		"amount_minor": "0", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("propose 0 total-nol: want 400, got %d (%v)", code, out)
	}
	// Approval body: kosong/null/skalar/array/field tak dikenal -> 400.
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-body-p", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose body test: want 201, got %d (%v)", code, body)
	}
	bodyProposal, _ := body["id"].(string)
	for _, raw := range []string{"", "null", "5", "[]", `{"x":1}`, `{"amount_minor":"1"}`} {
		code, out := approvePaymentRaw(t, env.srv, env.wsA, draftID, bodyProposal, env.mgrToken, []string{"pay-08-body-a"}, raw)
		if code != http.StatusBadRequest {
			t.Fatalf("approval body %q: want 400, got %d (%v)", raw, code, out)
		}
	}
	// Reason: kosong / hanya-whitespace / 501 rune -> 400; spasi di
	// ujung di-TRIM dan disimpan trimmed.
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-08-reason-p", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose reason test: want 201, got %d (%v)", code, body)
	}
	reasonPayment, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, reasonPayment, env.mgrToken, "pay-08-reason-a")
	if code != http.StatusCreated {
		t.Fatalf("approve reason test: want 201, got %d (%v)", code, body)
	}
	reasonPaymentID, _ := body["id"].(string)
	for _, reason := range []string{"", "   ", strings.Repeat("a", 501)} {
		code, out := proposeReversal(t, env.srv, env.wsA, draftID, reasonPaymentID, env.mgrToken, "pay-08-reason-bad", map[string]any{"reason": reason})
		if code != http.StatusBadRequest {
			t.Fatalf("reason %q: want 400, got %d (%v)", reason, code, out)
		}
	}
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, reasonPaymentID, env.mgrToken, "pay-08-reason-trim", map[string]any{"reason": "  kas salah catat  "})
	if code != http.StatusCreated {
		t.Fatalf("reason trim: want 201, got %d (%v)", code, body)
	}
	if body["reason"] != "kas salah catat" {
		t.Fatalf("reason harus disimpan trimmed, got %v", body["reason"])
	}
}

func TestPaymentReversalFull(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-09", "pay-09")
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-09-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalID, env.mgrToken, "pay-09-a1")
	if code != http.StatusCreated {
		t.Fatalf("approve: want 201, got %d (%v)", code, body)
	}
	paymentID, _ := body["id"].(string)
	// Usulan pembalikan saja TIDAK mengubah saldo.
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, "pay-09-rp1", map[string]any{"reason": "kas salah catat"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal: want 201, got %d (%v)", code, body)
	}
	revProposalID, _ := body["id"].(string)
	if body["reason"] != "kas salah catat" || body["payment_id"] != paymentID || body["proposed_by"] != env.mgrID {
		t.Fatalf("DTO usulan pembalikan: tidak sama: %v", body)
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "100000" || outstanding != "200000" {
		t.Fatalf("GET sesudah propose reversal: want 300000/100000/200000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	// Self-approval pembalikan juga 403.
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.mgrToken, "pay-09-ra-self")
	if code != http.StatusForbidden {
		t.Fatalf("self-approve reversal: want 403, got %d (%v)", code, body)
	}
	// Approval aktor kedua -> 201; saldo kembali utuh.
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentID, revProposalID, env.ownerToken, "pay-09-ra1")
	if code != http.StatusCreated {
		t.Fatalf("approve reversal: want 201, got %d (%v)", code, body)
	}
	reversalID, _ := body["id"].(string)
	if body["payment_id"] != paymentID || body["reversal_proposal_id"] != revProposalID || body["reason"] != "kas salah catat" {
		t.Fatalf("DTO fakta pembalikan: tidak sama: %v", body)
	}
	if body["proposed_by"] != env.mgrID || body["approved_by"] != env.ownerID {
		t.Fatalf("DTO fakta pembalikan: aktor want manager/owner, got %v/%v", body["proposed_by"], body["approved_by"])
	}
	stored, ok := readReversal(t, env.boot, env.wsA, reversalID)
	if !ok {
		t.Fatalf("fakta pembalikan DB tidak ada")
	}
	if stored.PaymentID != paymentID || stored.Reason != "kas salah catat" || stored.ProposedBy != env.mgrID || stored.ApprovedBy != env.ownerID {
		t.Fatalf("fakta pembalikan DB: tidak sama: %+v", stored)
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "300000" || paid != "0" || outstanding != "300000" {
		t.Fatalf("GET sesudah reversal: want 300000/0/300000, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	code, body = getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
	payments, _ := body["payments"].([]any)
	if len(payments) != 1 {
		t.Fatalf("payments: want 1, got %d", len(payments))
	}
	p0, _ := payments[0].(map[string]any)
	if p0["reversed"] != true || p0["reversal_id"] != reversalID {
		t.Fatalf("proyeksi reversed: want true/%s, got %v/%v", reversalID, p0["reversed"], p0["reversal_id"])
	}
	reversals, _ := body["reversals"].([]any)
	if len(reversals) != 1 {
		t.Fatalf("reversals: want 1, got %d", len(reversals))
	}
	pendingRev, _ := body["pending_reversal_proposals"].([]any)
	if len(pendingRev) != 0 {
		t.Fatalf("pending reversal: want 0, got %d", len(pendingRev))
	}
	// Usulan pembalikan baru terhadap pembayaran yang sudah dibatalkan
	// -> 409; usulan pending lain untuk pembayaran yang sama SEBELUM
	// pembalikan posted diizinkan (diuji di rantai kedua).
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentID, env.mgrToken, "pay-09-rp2", map[string]any{"reason": "kedua"})
	if code != http.StatusConflict {
		t.Fatalf("propose reversal kedua: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "pembayaran sudah dibatalkan")
	}
	if n := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_reversal_posted"); n != 1 {
		t.Fatalf("audit reversal posted: want 1, got %d", n)
	}
	// Dua usulan pembalikan pending untuk satu pembayaran diizinkan;
	// yang pertama disetujui -> 201, yang kedua -> 409.
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-09c-p", map[string]any{
		"amount_minor": "50000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose pay-09c: want 201, got %d (%v)", code, body)
	}
	propC, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, propC, env.mgrToken, "pay-09c-a")
	if code != http.StatusCreated {
		t.Fatalf("approve pay-09c: want 201, got %d (%v)", code, body)
	}
	paymentC, _ := body["id"].(string)
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentC, env.mgrToken, "pay-09c-rp1", map[string]any{"reason": "satu"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal c1: want 201, got %d (%v)", code, body)
	}
	revC1, _ := body["id"].(string)
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentC, env.ownerToken, "pay-09c-rp2", map[string]any{"reason": "dua"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal c2 (pending kedua): want 201, got %d (%v)", code, body)
	}
	revC2, _ := body["id"].(string)
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentC, revC1, env.ownerToken, "pay-09c-ra1")
	if code != http.StatusCreated {
		t.Fatalf("approve reversal c1: want 201, got %d (%v)", code, body)
	}
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentC, revC2, env.mgrToken, "pay-09c-ra2")
	if code != http.StatusConflict {
		t.Fatalf("approve reversal c2 sesudah c1: want 409, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "pembayaran sudah dibatalkan")
	}
}

func TestPaymentArithmeticGrossExceeds(t *testing.T) {
	env := setupPayments(t)
	// Rantai total MaxInt64 (tarif MaxInt64, draft 24 jam = 1 unit).
	draftID, _, total := seedPaymentChainOn(t, env, env.wsA, env.ownerToken, "SKU-PAY-MAX", "pay-max", "9223372036854775807", 24)
	if total != "9223372036854775807" {
		t.Fatalf("total MaxInt64: want 9223372036854775807, got %s", total)
	}
	// Tiga siklus bayar MaxInt64/2 + batalkan penuh: gross
	// 3*4611686018427387903 = 13835058055282163709 > MaxInt64, tetapi
	// net aktif selalu <= total; GET harus tepat setiap saat.
	half := "4611686018427387903"
	for i := 1; i <= 3; i++ {
		pfx := fmt.Sprintf("pay-max-c%d", i)
		payFullCycle(t, env, draftID, pfx, half)
		if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "9223372036854775807" || paid != "0" || outstanding != "9223372036854775807" {
			t.Fatalf("GET sesudah siklus %d: want MaxInt64/0/MaxInt64, got %s/%s/%s", i, gotTotal, paid, outstanding)
		}
	}
	// Pembayaran akhir tepat MaxInt64 lolos (gross keseluruhan kini
	// 23058430092136939516 > MaxInt64, net = MaxInt64).
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-max-final-p", map[string]any{
		"amount_minor": "9223372036854775807", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose final MaxInt64: want 201, got %d (%v)", code, body)
	}
	finalProposal, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, finalProposal, env.mgrToken, "pay-max-final-a")
	if code != http.StatusCreated {
		t.Fatalf("approve final MaxInt64: want 201, got %d (%v)", code, body)
	}
	if gotTotal, paid, outstanding := totalsFromGET(t, env, draftID); gotTotal != "9223372036854775807" || paid != "9223372036854775807" || outstanding != "0" {
		t.Fatalf("GET final: want MaxInt64/MaxInt64/0, got %s/%s/%s", gotTotal, paid, outstanding)
	}
	// Fixture korup: dua pembayaran posted langsung (masing-masing
	// valid CHECK, bersama-sama over-cap 400000 > 300000) -> GET 500,
	// bukan clamp; approval baru juga 500.
	env2 := setupPayments(t)
	draftID2, factID2, _ := seedPaymentChain(t, env2, "SKU-PAY-CORRUPT", "pay-corrupt")
	cutoff := dbCutoff(t, env2.boot)
	insertPosted := func(key, amount string) {
		t.Helper()
		var proposalID string
		if err := env2.boot.QueryRow(
			`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
			 VALUES ($1, $2, $3, $4, $5, 'IDR', 'cash', NULL, $6, $7) RETURNING id::text`,
			env2.wsA, draftID2, factID2, key, amount, env2.ownerID, cutoff).Scan(&proposalID); err != nil {
			t.Fatalf("sisip proposal korup %s: %v", key, err)
		}
		if _, err := env2.boot.Exec(
			`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
			 VALUES ($1, $2, $3, $4, $5, $6, 'IDR', 'cash', NULL, $7, $8, $9, $8)`,
			env2.wsA, draftID2, factID2, proposalID, key+"-fact", amount, env2.ownerID, cutoff, env2.mgrID); err != nil {
			t.Fatalf("sisip fakta korup %s: %v", key, err)
		}
	}
	insertPosted("pay-corrupt-x1", "200000")
	insertPosted("pay-corrupt-x2", "200000")
	code, body = getPayments(t, env2.srv, env2.wsA, draftID2, env2.ownerToken)
	if code != http.StatusInternalServerError {
		t.Fatalf("GET korup: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "total tidak konsisten")
	}
	code, body = proposePayment(t, env2.srv, env2.wsA, draftID2, env2.ownerToken, "pay-corrupt-p3", map[string]any{
		"amount_minor": "1000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose korup: want 201, got %d (%v)", code, body)
	}
	corruptProposal, _ := body["id"].(string)
	code, body = approvePayment(t, env2.srv, env2.wsA, draftID2, corruptProposal, env2.mgrToken, "pay-corrupt-a3")
	if code != http.StatusInternalServerError {
		t.Fatalf("approve korup: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "total tidak konsisten")
	}
}

func TestPaymentRLSDirect(t *testing.T) {
	env := setupPayments(t)
	draftID, factID, _ := seedPaymentChain(t, env, "SKU-PAY-RLS", "pay-rls")
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-rls-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash", "reference": "RC-RLS",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose RLS: want 201, got %d (%v)", code, body)
	}
	proposalID, _ := body["id"].(string)
	proposal, ok := readPaymentProposal(t, env.boot, env.wsA, proposalID)
	if !ok {
		t.Fatalf("proposal RLS tidak ada")
	}
	// (a) Baca lintas-tenant langsung sebagai runtime: 0 baris.
	db := testDB(t)
	tx := tenantTx(t, db, env.wsB, env.otherID)
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM rental_payment_proposals WHERE workspace_id = $1`, env.wsA).Scan(&n); err != nil {
		t.Fatalf("baca lintas-tenant: %v", err)
	}
	if n != 0 {
		t.Fatalf("baca lintas-tenant: want 0, got %d", n)
	}
	_ = tx.Rollback()
	// (b) INSERT proposal dengan proposed_by palsu (manager, padahal
	// subjek sesi owner) -> ditolak kebijakan (42501).
	tx = tenantTx(t, db, env.wsA, env.ownerID)
	_, err := tx.Exec(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, 'pay-rls-forged-p', 1000, 'IDR', 'cash', NULL, $4, now())`,
		env.wsA, draftID, factID, env.mgrID)
	if err == nil {
		t.Fatalf("INSERT proposal aktor palsu: harus ditolak")
	}
	if !strings.Contains(err.Error(), "42501") {
		t.Fatalf("INSERT proposal aktor palsu: want 42501, got %v", err)
	}
	_ = tx.Rollback()
	// (c) INSERT fakta dengan approved_by palsu -> ditolak.
	tx = tenantTx(t, db, env.wsA, env.mgrID)
	_, err = tx.Exec(
		`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, 'pay-rls-forged-a', 100000, 'IDR', 'cash', 'RC-RLS', $5, $6, $7, now())`,
		env.wsA, draftID, factID, proposalID, env.ownerID, proposal.ProposedAt, env.ownerID)
	if err == nil {
		t.Fatalf("INSERT fakta aktor palsu: harus ditolak")
	}
	if !strings.Contains(err.Error(), "42501") {
		t.Fatalf("INSERT fakta aktor palsu: want 42501, got %v", err)
	}
	_ = tx.Rollback()
	// (d) INSERT fakta dengan payload salinan yang SALAH (jumlah
	// berbeda dari usulan) -> ditolak kebijakan (42501).
	tx = tenantTx(t, db, env.wsA, env.mgrID)
	_, err = tx.Exec(
		`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, 'pay-rls-wrong-amount', 99999, 'IDR', 'cash', 'RC-RLS', $5, $6, $7, now())`,
		env.wsA, draftID, factID, proposalID, env.ownerID, proposal.ProposedAt, env.mgrID)
	if err == nil {
		t.Fatalf("INSERT fakta payload salah: harus ditolak")
	}
	if !strings.Contains(err.Error(), "42501") {
		t.Fatalf("INSERT fakta payload salah: want 42501, got %v", err)
	}
	_ = tx.Rollback()
	// (e) INSERT fakta dengan payload salinan yang BENAR sebagai
	// runtime -> LULUS (jalur sah handler tidak diblokir kebijakan).
	tx = tenantTx(t, db, env.wsA, env.mgrID)
	_, err = tx.Exec(
		`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, 'pay-rls-good-a', 100000, 'IDR', 'cash', 'RC-RLS', $5, $6, $7, now())`,
		env.wsA, draftID, factID, proposalID, env.ownerID, proposal.ProposedAt, env.mgrID)
	if err != nil {
		t.Fatalf("INSERT fakta payload benar: harus lulus, got %v", err)
	}
	// Audit untuk INSERT langsung ini tidak ada (bukan jalur handler);
	// rollback supaya fixture tidak tersisa.
	_ = tx.Rollback()
	// (f) CHECK proposer != approver (23514) lewat bootstrap: INSERT
	// fakta dengan proposed_by == approved_by.
	cutoff := dbCutoff(t, env.boot)
	_, err = env.boot.Exec(
		`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, 'pay-rls-selfcheck', 1000, 'IDR', 'cash', NULL, $5, $6, $5, $6)`,
		env.wsA, draftID, factID, proposalID, env.ownerID, cutoff)
	if err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("INSERT fakta self-checker: want 23514, got %v", err)
	}
	// (g) Kombinasi draft/proposal/pembayaran salah di workspace yang
	// sama -> FK 23503 (usulan memakai fakta draft lain).
	_, factID2, _ := seedPaymentChain(t, env, "SKU-PAY-RLS2", "pay-rls2")
	_, err = env.boot.Exec(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, 'pay-rls-wrong-draft', 1000, 'IDR', 'cash', NULL, $4, now())`,
		env.wsA, draftID, factID2, env.ownerID)
	if err == nil || !strings.Contains(err.Error(), "23503") {
		t.Fatalf("INSERT proposal fakta draft lain: want 23503, got %v", err)
	}
	// (h) Tanpa UPDATE/DELETE/TRUNCATE sebagai runtime; pemilik tabel
	// bukan runtime; runtime tanpa BYPASSRLS. Satu transaksi per
	// pernyataan: pernyataan yang gagal (42501) meng-abort transaksi,
	// sehingga pernyataan berikut di transaksi yang sama hanya akan
	// mengembalikan 25P02, bukan penolakan RLS-nya sendiri.
	for _, tc := range []struct {
		stmt   string
		hasArg bool
	}{
		{`UPDATE rental_payments SET amount_minor = 1 WHERE workspace_id = $1`, true},
		{`DELETE FROM rental_payments WHERE workspace_id = $1`, true},
		{`TRUNCATE rental_payments`, false},
	} {
		tx := tenantTx(t, db, env.wsA, env.ownerID)
		var err error
		if tc.hasArg {
			_, err = tx.Exec(tc.stmt, env.wsA)
		} else {
			_, err = tx.Exec(tc.stmt)
		}
		if err == nil || !strings.Contains(err.Error(), "42501") {
			_ = tx.Rollback()
			t.Fatalf("mutasi runtime (%s): want 42501, got %v", tc.stmt, err)
		}
		_ = tx.Rollback()
	}
	var owner string
	if err := env.boot.QueryRow(`SELECT tableowner FROM pg_tables WHERE schemaname = 'public' AND tablename = 'rental_payments'`).Scan(&owner); err != nil {
		t.Fatalf("baca pemilik rental_payments: %v", err)
	}
	if owner == "umnia_business" {
		t.Fatalf("pemilik rental_payments harus bukan runtime, got %s", owner)
	}
	var bypass bool
	if err := env.boot.QueryRow(`SELECT rolbypassrls FROM pg_roles WHERE rolname = 'umnia_business'`).Scan(&bypass); err != nil {
		t.Fatalf("baca rolbypassrls: %v", err)
	}
	if bypass {
		t.Fatalf("umnia_business tidak boleh BYPASSRLS")
	}
	// (i) Batas non-null / kunci / finite lewat bootstrap.
	_, err = env.boot.Exec(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, 'pay-rls-null-amt', NULL, 'IDR', 'cash', NULL, $4, now())`,
		env.wsA, draftID, factID, env.ownerID)
	if err == nil || !strings.Contains(err.Error(), "23502") {
		t.Fatalf("INSERT proposal amount NULL: want 23502, got %v", err)
	}
	_, err = env.boot.Exec(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, 'pay-rls-bad-time', 1000, 'IDR', 'cash', NULL, $4, '-infinity')`,
		env.wsA, draftID, factID, env.ownerID)
	if err == nil || !strings.Contains(err.Error(), "23514") {
		t.Fatalf("INSERT proposal waktu infinite: want 23514, got %v", err)
	}
	_, err = env.boot.Exec(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, 'pay-rls-p1', 1000, 'IDR', 'cash', NULL, $4, now())`,
		env.wsA, draftID, factID, env.ownerID)
	if err == nil || !strings.Contains(err.Error(), "23505") {
		t.Fatalf("INSERT proposal kunci duplikat: want 23505, got %v", err)
	}
}

func TestPaymentFaults(t *testing.T) {
	env := setupPayments(t)
	draftID, factID, _ := seedPaymentChain(t, env, "SKU-PAY-FA", "pay-fa")
	// (a) Fault audit posting: INSERT audit rental_payment_posted
	// digagalkan -> approve 500 "audit gagal ditulis", snapshot
	// tidak berubah, retry kunci yang sama berhasil.
	code, body := proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-fa-p1", map[string]any{
		"amount_minor": "100000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose (a): want 201, got %d (%v)", code, body)
	}
	proposalA, _ := body["id"].(string)
	installPaymentTrigger(t, env.boot, "qa_fault_payment_posted_audit", "qa_fail_payment_posted_audit", "audit_events", "BEFORE INSERT",
		`NEW.action = 'rental_payment_posted'`)
	before := snapshotPayments(t, env, factID)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalA, env.mgrToken, "pay-fa-a1")
	if code != http.StatusInternalServerError {
		t.Fatalf("approve fault audit: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "audit gagal ditulis")
	}
	assertPaymentSnapshotUnchanged(t, before, snapshotPayments(t, env, factID))
	dropPaymentTrigger(t, env.boot, "qa_fault_payment_posted_audit", "qa_fail_payment_posted_audit", "audit_events")
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalA, env.mgrToken, "pay-fa-a1")
	if code != http.StatusCreated {
		t.Fatalf("approve sesudah lepas fault audit: want 201, got %d (%v)", code, body)
	}
	paymentA, _ := body["id"].(string)
	// (b) Fault tulis proposal: BEFORE INSERT rental_payment_proposals
	// -> propose 500, snapshot tidak berubah, retry berhasil.
	installPaymentTrigger(t, env.boot, "qa_fault_payment_proposal", "qa_fail_payment_proposal", "rental_payment_proposals", "BEFORE INSERT", `true`)
	before = snapshotPayments(t, env, factID)
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-fa-p2", map[string]any{
		"amount_minor": "50000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusInternalServerError {
		t.Fatalf("propose fault tulis: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "transaksi gagal")
	}
	assertPaymentSnapshotUnchanged(t, before, snapshotPayments(t, env, factID))
	dropPaymentTrigger(t, env.boot, "qa_fault_payment_proposal", "qa_fail_payment_proposal", "rental_payment_proposals")
	code, body = proposePayment(t, env.srv, env.wsA, draftID, env.ownerToken, "pay-fa-p2", map[string]any{
		"amount_minor": "50000", "currency": "IDR", "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("propose sesudah lepas fault tulis: want 201, got %d (%v)", code, body)
	}
	proposalB, _ := body["id"].(string)
	// (c) Fault tulis fakta: BEFORE INSERT rental_payments -> approve
	// 500, snapshot tidak berubah (proposal tetap pending), retry 201.
	installPaymentTrigger(t, env.boot, "qa_fault_payment_fact", "qa_fail_payment_fact", "rental_payments", "BEFORE INSERT", `true`)
	before = snapshotPayments(t, env, factID)
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalB, env.mgrToken, "pay-fa-b1")
	if code != http.StatusInternalServerError {
		t.Fatalf("approve fault tulis fakta: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "transaksi gagal")
	}
	assertPaymentSnapshotUnchanged(t, before, snapshotPayments(t, env, factID))
	dropPaymentTrigger(t, env.boot, "qa_fault_payment_fact", "qa_fail_payment_fact", "rental_payments")
	code, body = approvePayment(t, env.srv, env.wsA, draftID, proposalB, env.mgrToken, "pay-fa-b1")
	if code != http.StatusCreated {
		t.Fatalf("approve sesudah lepas fault fakta: want 201, got %d (%v)", code, body)
	}
	// (d) Fault audit pembalikan + fault tulis fakta pembalikan:
	// keduanya rollback penuh dan retry berhasil.
	code, body = proposeReversal(t, env.srv, env.wsA, draftID, paymentA, env.mgrToken, "pay-fa-rp1", map[string]any{"reason": "koreksi fault"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal fault: want 201, got %d (%v)", code, body)
	}
	revProposal, _ := body["id"].(string)
	installPaymentTrigger(t, env.boot, "qa_fault_payment_rev_audit", "qa_fail_payment_rev_audit", "audit_events", "BEFORE INSERT",
		`NEW.action = 'rental_payment_reversal_posted'`)
	before = snapshotPayments(t, env, factID)
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentA, revProposal, env.ownerToken, "pay-fa-ra1")
	if code != http.StatusInternalServerError {
		t.Fatalf("approve reversal fault audit: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "audit gagal ditulis")
	}
	assertPaymentSnapshotUnchanged(t, before, snapshotPayments(t, env, factID))
	dropPaymentTrigger(t, env.boot, "qa_fault_payment_rev_audit", "qa_fail_payment_rev_audit", "audit_events")
	installPaymentTrigger(t, env.boot, "qa_fault_payment_rev_fact", "qa_fail_payment_rev_fact", "rental_payment_reversals", "BEFORE INSERT", `true`)
	before = snapshotPayments(t, env, factID)
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentA, revProposal, env.ownerToken, "pay-fa-ra1")
	if code != http.StatusInternalServerError {
		t.Fatalf("approve reversal fault tulis: want 500, got %d (%v)", code, body)
	} else {
		assertFlatError(t, body, "transaksi gagal")
	}
	assertPaymentSnapshotUnchanged(t, before, snapshotPayments(t, env, factID))
	dropPaymentTrigger(t, env.boot, "qa_fault_payment_rev_fact", "qa_fail_payment_rev_fact", "rental_payment_reversals")
	code, body = approveReversal(t, env.srv, env.wsA, draftID, paymentA, revProposal, env.ownerToken, "pay-fa-ra1")
	if code != http.StatusCreated {
		t.Fatalf("approve reversal sesudah lepas fault: want 201, got %d (%v)", code, body)
	}
}

func TestPaymentFreshSchemaUpgrade(t *testing.T) {
	if _, err := exec.LookPath("psql"); err != nil {
		t.Skip("psql tidak ada di PATH, tes runner psql dilewati")
	}
	scratchURL, scratch := scratchDB(t, "umnia_pay_fresh")
	// Jalur produksi: 001-012 lalu 013, masing-masing satu transaksi
	// psql -1 (applyMigrationFile).
	for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql") {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("terapkan %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	// Fixture referensi (draft + fakta) SEBELUM 013: 013 tidak boleh
	// mengubahnya.
	subj := "11111111-2222-3333-4444-5555555555a1"
	ws := "11111111-2222-3333-4444-5555555555a2"
	cust := "11111111-2222-3333-4444-5555555555a3"
	item := "11111111-2222-3333-4444-5555555555a4"
	draft := "11111111-2222-3333-4444-5555555555a5"
	price := "11111111-2222-3333-4444-5555555555a6"
	fact := "11111111-2222-3333-4444-5555555555a7"
	execMust := func(q string, args ...any) {
		t.Helper()
		if _, err := scratch.Exec(q, args...); err != nil {
			t.Fatalf("fixture upgrade: %v (%s)", err, q)
		}
	}
	execMust(`INSERT INTO identity_subjects (id, email, password_hash) VALUES ($1, 'upgrade-pay@example.test', 'x')`, subj)
	execMust(`INSERT INTO workspaces (id, name, owner_subject_id) VALUES ($1, 'WS Upgrade Pay', $2)`, ws, subj)
	execMust(`INSERT INTO parties (id, workspace_id, type, display_name) VALUES ($1, $2, 'customer', 'Pelanggan Upgrade')`, cust, ws)
	execMust(`INSERT INTO catalog_items (id, workspace_id, sku, name, unit, status) VALUES ($1, $2, 'SKU-UPGRADE-13', 'Barang Upgrade', 'pcs', 'active')`, item, ws)
	execMust(`INSERT INTO rental_contracts (id, workspace_id, customer_id, catalog_item_id, client_request_id, status, starts_at, ends_at) VALUES ($1, $2, $3, $4, 'upgrade-13-draft', 'draft', '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z')`, draft, ws, cust, item)
	execMust(`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency) VALUES ($1, $2, $3, 'standard', 'upgrade-13-price', '2026-11-10T01:00:00Z', '2026-11-13T01:00:00Z', now(), 150000, 'IDR')`, price, ws, item)
	execMust(`INSERT INTO rental_agreed_prices (id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by) VALUES ($1, $2, $3, $4, 'elapsed_24h_ceil_v1', $5, $6, 'upgrade-13-fact', 150000, 'IDR', 2, 300000, '2026-11-10T02:00:00Z', '2026-11-12T02:00:00Z', now(), $7)`, fact, ws, draft, price, item, cust, subj)
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "013_rental_payments.sql")); code != 0 {
		t.Fatalf("terapkan 013: want exit 0, got %d (%s)", code, out)
	}
	var factTotal string
	if err := scratch.QueryRow(`SELECT total_minor::text FROM rental_agreed_prices WHERE id = $1`, fact).Scan(&factTotal); err != nil {
		t.Fatalf("baca fakta sesudah 013: %v", err)
	}
	if factTotal != "300000" {
		t.Fatalf("fakta sesudah 013: want 300000, got %s", factTotal)
	}
	// Marker 13 + keempat tabel kosong + 8 kebijakan + RLS + hak.
	var markers string
	if err := scratch.QueryRow(`SELECT coalesce(string_agg(version, ',' ORDER BY version), '') FROM schema_migrations`).Scan(&markers); err != nil {
		t.Fatalf("baca markers upgrade: %v", err)
	}
	if markers != "001_init,002_identity,003_modules,004_module_installations,005_core_master_data,006_rental_contracts,007_asset_availability,008_price_versions,009_rental_agreed_prices,010_rental_custody,011_operator_entitlement,012_rental_damaged_lost_return,013_rental_payments" {
		t.Fatalf("markers upgrade: want 13 marker, got %s", markers)
	}
	for _, tbl := range []string{"rental_payment_proposals", "rental_payments", "rental_payment_reversal_proposals", "rental_payment_reversals"} {
		var n int
		if err := scratch.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = $1`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("tabel %s: want 1, got %d (%v)", tbl, n, err)
		}
		if err := scratch.QueryRow(`SELECT count(*) FROM pg_class WHERE relname = $1 AND relrowsecurity AND relforcerowsecurity`, tbl).Scan(&n); err != nil || n != 1 {
			t.Fatalf("RLS FORCE %s: want 1, got %d (%v)", tbl, n, err)
		}
		if err := scratch.QueryRow(`SELECT count(*) FROM pg_policies WHERE tablename = $1`, tbl).Scan(&n); err != nil || n != 2 {
			t.Fatalf("kebijakan %s: want 2, got %d (%v)", tbl, n, err)
		}
		if err := scratch.QueryRow(`SELECT count(*) FROM pg_tables WHERE schemaname = 'public' AND tablename = $1 AND tableowner = 'umnia_business'`, tbl).Scan(&n); err != nil || n != 0 {
			t.Fatalf("pemilik %s: tidak boleh runtime, got %d (%v)", tbl, n, err)
		}
	}
	// UNIQUE target 3-kolom pada fakta harga ada (dipakai FK 013).
	var n int
	if err := scratch.QueryRow(`SELECT count(*) FROM pg_constraint WHERE conname = 'rental_agreed_prices_workspace_draft_id_key' AND contype = 'u'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("UNIQUE fakta 3-kolom: want 1, got %d (%v)", n, err)
	}
	// Rerun 013 mempertahankan marker + data; rerun rantai penuh
	// 001-013 juga mempertahankan marker.
	if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", "013_rental_payments.sql")); code != 0 {
		t.Fatalf("rerun 013: want exit 0, got %d (%s)", code, out)
	}
	if err := scratch.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 13 {
		t.Fatalf("marker sesudah rerun 013: want 13, got %d (%v)", n, err)
	}
	if err := scratch.QueryRow(`SELECT total_minor::text FROM rental_agreed_prices WHERE id = $1`, fact).Scan(&factTotal); err != nil || factTotal != "300000" {
		t.Fatalf("fakta sesudah rerun 013: want 300000, got %s (%v)", factTotal, err)
	}
	for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql", "013_rental_payments.sql") {
		if code, out := applyMigrationFile(t, scratchURL, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("rerun rantai %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	if err := scratch.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 13 {
		t.Fatalf("marker sesudah rerun rantai: want 13, got %d (%v)", n, err)
	}
	// Fresh 001-013 langsung: 13 marker.
	scratchURL2, scratch2 := scratchDB(t, "umnia_pay_fresh2")
	for _, f := range append(append([]string{}, migrations001to010...), "011_operator_entitlement.sql", "012_rental_damaged_lost_return.sql", "013_rental_payments.sql") {
		if code, out := applyMigrationFile(t, scratchURL2, filepath.Join("migrations", f)); code != 0 {
			t.Fatalf("fresh %s: want exit 0, got %d (%s)", f, code, out)
		}
	}
	if err := scratch2.QueryRow(`SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil || n != 13 {
		t.Fatalf("marker fresh 001-013: want 13, got %d (%v)", n, err)
	}
}

func TestPaymentGetSnapshotConsistency(t *testing.T) {
	env := setupPayments(t)
	draftID, _, _ := seedPaymentChain(t, env, "SKU-PAY-SNAP", "pay-snap")
	// Pekerja: 10 siklus bayar 1000 + batalkan penuh pada draft yang
	// sama; GET berjalan bersamaan dan setiap respons harus konsisten
	// internal (paid + outstanding == total; pending mengecualikan
	// yang sudah posted; reversed cocok dengan reversals[]).
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 1; i <= 10; i++ {
			pfx := fmt.Sprintf("pay-snap-w%d", i)
			code, body, err := postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals", env.ownerToken, []string{pfx + "-p"}, `{"amount_minor":"1000","currency":"IDR","method":"cash"}`)
			if err != nil || code != http.StatusCreated {
				return
			}
			proposalID, _ := body["id"].(string)
			code, body, err = postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals/"+proposalID+"/approvals", env.mgrToken, []string{pfx + "-a"}, `{}`)
			if err != nil || code != http.StatusCreated {
				return
			}
			paymentID, _ := body["id"].(string)
			code, body, err = postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payments/"+paymentID+"/reversal-proposals", env.mgrToken, []string{pfx + "-rp"}, `{"reason":"snap"}`)
			if err != nil || code != http.StatusCreated {
				return
			}
			revProposalID, _ := body["id"].(string)
			code, _, err = postRawGo(env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payments/"+paymentID+"/reversal-proposals/"+revProposalID+"/approvals", env.ownerToken, []string{pfx + "-ra"}, `{}`)
			if err != nil || code != http.StatusCreated {
				return
			}
		}
		close(stop)
	}()
	checked := 0
	for {
		select {
		case <-stop:
			wg.Wait()
			if checked == 0 {
				t.Fatalf("GET konsistensi: tidak ada respons GET yang diperiksa")
			}
			return
		default:
		}
		code, body := getPayments(t, env.srv, env.wsA, draftID, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("GET konsistensi: want 200, got %d (%v)", code, body)
		}
		checked++
		total, _ := body["total_minor"].(string)
		paid, _ := body["paid_minor"].(string)
		outstanding, _ := body["outstanding_minor"].(string)
		if total != "300000" {
			t.Fatalf("GET konsistensi: total want 300000, got %s", total)
		}
		paidInt := atoiStrict(t, paid)
		outInt := atoiStrict(t, outstanding)
		if paidInt+outInt != 300000 {
			t.Fatalf("GET konsistensi: paid %d + outstanding %d != 300000", paidInt, outInt)
		}
		payments, _ := body["payments"].([]any)
		reversals, _ := body["reversals"].([]any)
		pendingPay, _ := body["pending_payment_proposals"].([]any)
		pendingRev, _ := body["pending_reversal_proposals"].([]any)
		reversedIDs := map[string]bool{}
		for _, r := range reversals {
			rm, _ := r.(map[string]any)
			pid, _ := rm["payment_id"].(string)
			reversedIDs[pid] = true
		}
		postedProposalIDs := map[string]bool{}
		for _, p := range payments {
			pm, _ := p.(map[string]any)
			pid, _ := pm["id"].(string)
			propID, _ := pm["proposal_id"].(string)
			postedProposalIDs[propID] = true
			rev, _ := pm["reversed"].(bool)
			if rev != reversedIDs[pid] {
				t.Fatalf("GET konsistensi: payment %s reversed %v vs reversals %v", pid, rev, reversedIDs[pid])
			}
		}
		for _, p := range pendingPay {
			pm, _ := p.(map[string]any)
			propID, _ := pm["id"].(string)
			if postedProposalIDs[propID] {
				t.Fatalf("GET konsistensi: proposal pending %s sudah posted", propID)
			}
		}
		_ = pendingRev
		time.Sleep(5 * time.Millisecond)
	}
}

// atoiStrict mengubah string desimal kanonis nonnegatif menjadi int
// (hanya untuk asersi tes; bukan logika produk).
func atoiStrict(t *testing.T, s string) int {
	t.Helper()
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			t.Fatalf("atoiStrict: %q bukan desimal nonnegatif", s)
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// --- F2-02-C: ringkasan pembayaran workspace (read-only) ---

// seedSummaryChain mirrors seedPaymentChainOn but with currency and
// optional finance activation (withFinance=false seeds the rental
// chain + agreed-price fact WITHOUT finance issuance/activation).
func seedSummaryChain(t *testing.T, env *activationEnv, ws, token, sku, keyPrefix, amount string, draftHours int, currency string, withFinance bool) (draftID, factID, total string) {
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
	priceID := seedPriceOn(t, env, ws, token, itemID, keyPrefix+"-price", base.Add(1*time.Hour), base.Add(73*time.Hour), amount, currency)
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
	if withFinance {
		activateFinance(t, env, ws, token)
	}
	code, body = getAgreedPrice(t, env.srv, ws, draftID, token)
	if code != http.StatusOK {
		t.Fatalf("baca fakta %s: want 200, got %d (%v)", keyPrefix, code, body)
	}
	factID, _ = body["id"].(string)
	return draftID, factID, total
}

func getFinanceSummary(t *testing.T, srv *httptest.Server, ws, token string) (int, map[string]any) {
	t.Helper()
	return doJSON(t, srv, "GET", "/workspaces/"+ws+"/finance/summary", token, nil)
}

func getFinanceSummaryRaw(t *testing.T, srv *httptest.Server, ws, token, query string) (int, map[string]any) {
	t.Helper()
	path := "/workspaces/" + ws + "/finance/summary"
	if query != "" {
		path += "?" + query
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL+path, nil)
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

// getFinanceSummaryGoCtx is the context-aware goroutine-safe GET:
// errors returned, never Fatalf. The caller's context bounds the
// request; no global client setting is touched.
func getFinanceSummaryGoCtx(ctx context.Context, srv *httptest.Server, ws, token string) (int, map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/workspaces/"+ws+"/finance/summary", nil)
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
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, out, nil
}

// getFinanceSummaryGo is the goroutine-safe GET: errors returned, never Fatalf.
func getFinanceSummaryGo(srv *httptest.Server, ws, token string) (int, map[string]any, error) {
	return getFinanceSummaryGoCtx(context.Background(), srv, ws, token)
}

func findSummaryCurrency(body map[string]any, currency string) (map[string]any, bool) {
	list, ok := body["currencies"].([]any)
	if !ok {
		return nil, false
	}
	for _, item := range list {
		m, _ := item.(map[string]any)
		if m["currency"] == currency {
			return m, true
		}
	}
	return nil, false
}

func summaryCurrency(t *testing.T, body map[string]any, currency string) map[string]any {
	t.Helper()
	m, ok := findSummaryCurrency(body, currency)
	if !ok {
		t.Fatalf("summary tanpa currency %s: %v", currency, body)
	}
	return m
}

func assertSummaryCurrency(t *testing.T, body map[string]any, currency, total, paid, outstanding string, payments, reversals, pendPay, pendRev int) {
	t.Helper()
	m := summaryCurrency(t, body, currency)
	if m["total_minor"] != total || m["paid_minor"] != paid || m["outstanding_minor"] != outstanding {
		t.Fatalf("summary %s: want total/paid/outstanding %s/%s/%s, got %v", currency, total, paid, outstanding, m)
	}
	for k, v := range map[string]float64{
		"payments_count": float64(payments), "reversals_count": float64(reversals),
		"pending_payment_proposals_count": float64(pendPay), "pending_reversal_proposals_count": float64(pendRev),
	} {
		if m[k] != v {
			t.Fatalf("summary %s: want %s=%v, got %v (%v)", currency, k, v, m[k], m)
		}
	}
}

func assertSummaryCorrupt(t *testing.T, code int, body map[string]any) {
	t.Helper()
	if code != http.StatusInternalServerError {
		t.Fatalf("summary korup: want 500, got %d (%v)", code, body)
	}
	if body["error"] != "total tidak konsisten" {
		t.Fatalf("summary korup: want error total tidak konsisten, got %v", body)
	}
	if _, has := body["currencies"]; has {
		t.Fatalf("summary korup: currencies parsial tidak boleh ada: %v", body)
	}
}

// payPosted proposes + approves one payment on any workspace/currency; returns payment ID.
func payPosted(t *testing.T, env *activationEnv, ws, draft, proposerToken, approverToken, keyPrefix, amount, currency string) string {
	t.Helper()
	code, body := proposePayment(t, env.srv, ws, draft, proposerToken, keyPrefix+"-p", map[string]any{
		"amount_minor": amount, "currency": currency, "method": "cash",
	})
	if code != http.StatusCreated {
		t.Fatalf("pay propose %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	proposalID, _ := body["id"].(string)
	code, body = approvePayment(t, env.srv, ws, draft, proposalID, approverToken, keyPrefix+"-a")
	if code != http.StatusCreated {
		t.Fatalf("pay approve %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	paymentID, _ := body["id"].(string)
	return paymentID
}

// reversePosted proposes + approves a full reversal; returns reversal ID.
func reversePosted(t *testing.T, env *activationEnv, ws, draft, paymentID, proposerToken, approverToken, keyPrefix string) string {
	t.Helper()
	code, body := proposeReversal(t, env.srv, ws, draft, paymentID, proposerToken, keyPrefix+"-rp", map[string]any{"reason": "koreksi " + keyPrefix})
	if code != http.StatusCreated {
		t.Fatalf("rev propose %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	revProposalID, _ := body["id"].(string)
	code, body = approveReversal(t, env.srv, ws, draft, paymentID, revProposalID, approverToken, keyPrefix+"-ra")
	if code != http.StatusCreated {
		t.Fatalf("rev approve %s: want 201, got %d (%v)", keyPrefix, code, body)
	}
	reversalID, _ := body["id"].(string)
	return reversalID
}

// insertCorruptPayment inserts a posted payment row directly, bypassing
// approval/cap. Proposer = owner, approver = manager, timestamps now().
func insertCorruptPayment(t *testing.T, boot *sql.DB, ws, draftID, factID, proposalID, key, amount, currency, ownerID, mgrID string) string {
	t.Helper()
	amt, err := strconv.ParseInt(amount, 10, 64)
	if err != nil {
		t.Fatalf("parse amount %s: %v", amount, err)
	}
	var id string
	err = boot.QueryRow(
		`INSERT INTO rental_payments (workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'cash', NULL, $8, now(), $9, now()) RETURNING id::text`,
		ws, draftID, factID, proposalID, key, amt, currency, ownerID, mgrID).Scan(&id)
	if err != nil {
		t.Fatalf("insert corrupt payment %s: %v", key, err)
	}
	return id
}

// insertCorruptProposal inserts a payment proposal row directly.
func insertCorruptProposal(t *testing.T, boot *sql.DB, ws, draftID, factID, key, amount, currency, ownerID string) string {
	t.Helper()
	amt, err := strconv.ParseInt(amount, 10, 64)
	if err != nil {
		t.Fatalf("parse amount %s: %v", amount, err)
	}
	var id string
	err = boot.QueryRow(
		`INSERT INTO rental_payment_proposals (workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, 'cash', NULL, $7, now()) RETURNING id::text`,
		ws, draftID, factID, key, amt, currency, ownerID).Scan(&id)
	if err != nil {
		t.Fatalf("insert corrupt proposal %s: %v", key, err)
	}
	return id
}

// summaryStateSnapshot is an exact state snapshot for the no-writes proof.
type summaryStateSnapshot struct {
	proposals, payments, revProposals, reversals int
	audits, installations, entitlements          int
	facts, paymentsDump                          string
}

func snapshotSummaryState(t *testing.T, env *activationEnv) summaryStateSnapshot {
	t.Helper()
	var s summaryStateSnapshot
	s.proposals = countRows(t, env.boot, "rental_payment_proposals", env.wsA)
	s.payments = countRows(t, env.boot, "rental_payments", env.wsA)
	s.revProposals = countRows(t, env.boot, "rental_payment_reversal_proposals", env.wsA)
	s.reversals = countRows(t, env.boot, "rental_payment_reversals", env.wsA)
	s.audits = countRows(t, env.boot, "audit_events", env.wsA)
	s.installations = countRows(t, env.boot, "module_installations", env.wsA)
	s.entitlements = countRows(t, env.boot, "module_entitlements", env.wsA)
	if err := env.boot.QueryRow(`SELECT COALESCE(string_agg(id::text || '|' || total_minor::text || '|' || currency, ',' ORDER BY id), '') FROM rental_agreed_prices WHERE workspace_id = $1`, env.wsA).Scan(&s.facts); err != nil {
		t.Fatalf("snapshot facts: %v", err)
	}
	if err := env.boot.QueryRow(`SELECT COALESCE(string_agg(id::text || '|' || amount_minor::text || '|' || currency || '|' || client_request_id, ',' ORDER BY id), '') FROM rental_payments WHERE workspace_id = $1`, env.wsA).Scan(&s.paymentsDump); err != nil {
		t.Fatalf("snapshot payments: %v", err)
	}
	return s
}

func TestFinanceSummary_EmptyFactOnlyZero(t *testing.T) {
	env := setupPayments(t)
	code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary kosong: want 200, got %d (%v)", code, body)
	}
	list, ok := body["currencies"].([]any)
	if !ok || len(list) != 0 {
		t.Fatalf("summary kosong: want currencies [], got %v", body["currencies"])
	}
	if body["workspace_id"] != env.wsA {
		t.Fatalf("summary kosong: want workspace_id %s, got %v", env.wsA, body["workspace_id"])
	}
	observed, ok := body["observed_at"].(string)
	if !ok || observed == "" {
		t.Fatalf("summary tanpa observed_at: %v", body)
	}
	if _, err := time.Parse(time.RFC3339Nano, observed); err != nil {
		t.Fatalf("observed_at bukan RFC3339Nano: %q (%v)", observed, err)
	}
	draftID, _, total := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-00", "sum-00", "150000", 48, "IDR", true)
	if total != "300000" {
		t.Fatalf("total fakta: want 300000, got %s", total)
	}
	code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary fakta saja: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "0", "300000", 0, 0, 0, 0)
	_, _, total0 := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-0Z", "sum-0z", "0", 24, "IDR", true)
	if total0 != "0" {
		t.Fatalf("total nol: want 0, got %s", total0)
	}
	code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary harga nol: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "0", "300000", 0, 0, 0, 0)
	_ = draftID
}

func TestFinanceSummary_MultiDraftCountsNoFanout(t *testing.T) {
	env := setupPayments(t)
	d1, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-10", "sum-10", "150000", 48, "IDR", true)
	p1 := payPosted(t, env, env.wsA, d1, env.ownerToken, env.mgrToken, "sum-10-a", "100000", "IDR")
	p2 := payPosted(t, env, env.wsA, d1, env.ownerToken, env.mgrToken, "sum-10-b", "50000", "IDR")
	reversePosted(t, env, env.wsA, d1, p1, env.mgrToken, env.ownerToken, "sum-10-r1")
	code, body := proposeReversal(t, env.srv, env.wsA, d1, p2, env.mgrToken, "sum-10-r2a", map[string]any{"reason": "koreksi pertama"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal pertama: want 201, got %d (%v)", code, body)
	}
	revPropA, _ := body["id"].(string)
	code, body = proposeReversal(t, env.srv, env.wsA, d1, p2, env.mgrToken, "sum-10-r2b", map[string]any{"reason": "koreksi kedua"})
	if code != http.StatusCreated {
		t.Fatalf("propose reversal kedua: want 201, got %d (%v)", code, body)
	}
	revPropB, _ := body["id"].(string)
	code, body = approveReversal(t, env.srv, env.wsA, d1, p2, revPropA, env.ownerToken, "sum-10-r2a-ok")
	if code != http.StatusCreated {
		t.Fatalf("approve reversal pertama: want 201, got %d (%v)", code, body)
	}
	p3 := payPosted(t, env, env.wsA, d1, env.ownerToken, env.mgrToken, "sum-10-c", "70000", "IDR")
	_ = p3
	code, body = proposePayment(t, env.srv, env.wsA, d1, env.ownerToken, "sum-10-pp", map[string]any{"amount_minor": "10000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("pending proposal: want 201, got %d (%v)", code, body)
	}
	d2, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-11", "sum-11", "150000", 48, "IDR", true)
	payPosted(t, env, env.wsA, d2, env.ownerToken, env.mgrToken, "sum-11-a", "300000", "IDR")
	code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "600000", "370000", "230000", 4, 2, 1, 1)
	if _, ok := findSummaryCurrency(body, "USD"); ok {
		t.Fatalf("summary IDR saja: ada bucket USD: %v", body)
	}
	// Usulan pembalikan kedua tetap pending sesudah yang pertama disetujui.
	code, body = getPayments(t, env.srv, env.wsA, d1, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("histori d1: want 200, got %d (%v)", code, body)
	}
	pendingRev, _ := body["pending_reversal_proposals"].([]any)
	if len(pendingRev) != 1 {
		t.Fatalf("histori d1: want 1 pending reversal, got %v", body["pending_reversal_proposals"])
	}
	pendingRevMap, _ := pendingRev[0].(map[string]any)
	if pendingRevMap["id"] != revPropB {
		t.Fatalf("histori d1: pending reversal want %s, got %v", revPropB, pendingRevMap["id"])
	}
	if body["paid_minor"] != "70000" || body["total_minor"] != "300000" {
		t.Fatalf("histori d1 berubah: %v", body)
	}
}

func TestFinanceSummary_CurrenciesAndLargeAggregates(t *testing.T) {
	env := setupPayments(t)
	maxInt64 := "9223372036854775807"
	dA, _, totalA := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-20", "sum-20", maxInt64, 24, "IDR", true)
	if totalA != maxInt64 {
		t.Fatalf("total A: want %s, got %s", maxInt64, totalA)
	}
	payPosted(t, env, env.wsA, dA, env.ownerToken, env.mgrToken, "sum-20-a", maxInt64, "IDR")
	dB, _, totalB := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-21", "sum-21", maxInt64, 24, "IDR", true)
	if totalB != maxInt64 {
		t.Fatalf("total B: want %s, got %s", maxInt64, totalB)
	}
	for i := 0; i < 3; i++ {
		p := payPosted(t, env, env.wsA, dB, env.ownerToken, env.mgrToken, fmt.Sprintf("sum-21-c%d", i), maxInt64, "IDR")
		reversePosted(t, env, env.wsA, dB, p, env.mgrToken, env.ownerToken, fmt.Sprintf("sum-21-r%d", i))
	}
	payPosted(t, env, env.wsA, dB, env.ownerToken, env.mgrToken, "sum-21-final", maxInt64, "IDR")
	dC, _, totalC := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-22", "sum-22", "250000", 48, "USD", true)
	if totalC != "500000" {
		t.Fatalf("total C: want 500000, got %s", totalC)
	}
	payPosted(t, env, env.wsA, dC, env.ownerToken, env.mgrToken, "sum-22-a", "200000", "USD")
	code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary: want 200, got %d (%v)", code, body)
	}
	twoMax := "18446744073709551614"
	assertSummaryCurrency(t, body, "IDR", twoMax, twoMax, "0", 5, 3, 0, 0)
	assertSummaryCurrency(t, body, "USD", "500000", "200000", "300000", 1, 0, 0, 0)
	list, _ := body["currencies"].([]any)
	if len(list) != 2 {
		t.Fatalf("summary: want 2 currency, got %v", body["currencies"])
	}
	first, _ := list[0].(map[string]any)
	second, _ := list[1].(map[string]any)
	if first["currency"] != "IDR" || second["currency"] != "USD" {
		t.Fatalf("urutan currency: want [IDR USD], got %v", body["currencies"])
	}
}

func TestFinanceSummary_Corruption(t *testing.T) {
	t.Run("over-cap", func(t *testing.T) {
		env := setupPayments(t)
		d, factID, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-30", "sum-30", "100", 24, "IDR", true)
		code, body := proposePayment(t, env.srv, env.wsA, d, env.ownerToken, "sum-30-p", map[string]any{"amount_minor": "100", "currency": "IDR", "method": "cash"})
		if code != http.StatusCreated {
			t.Fatalf("propose: want 201, got %d (%v)", code, body)
		}
		proposalID, _ := body["id"].(string)
		payID := insertCorruptPayment(t, env.boot, env.wsA, d, factID, proposalID, "sum-30-pay", "150", "IDR", env.ownerID, env.mgrID)
		before, ok := readPayment(t, env.boot, env.wsA, payID)
		if !ok {
			t.Fatalf("payment korup tidak terbaca")
		}
		auditsBefore := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted")
		code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		assertSummaryCorrupt(t, code, body)
		after, ok := readPayment(t, env.boot, env.wsA, payID)
		if !ok || before != after {
			t.Fatalf("GET memperbaiki baris korup: sebelum=%+v sesudah=%+v", before, after)
		}
		if got := countBusinessAudits(t, env.boot, env.wsA, "rental_payment_posted"); got != auditsBefore {
			t.Fatalf("GET menulis audit: sebelum=%d sesudah=%d", auditsBefore, got)
		}
	})
	t.Run("offset-hidden", func(t *testing.T) {
		env := setupPayments(t)
		dA, factA, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-31a", "sum-31a", "100", 24, "IDR", true)
		_, _, _ = seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-31b", "sum-31b", "100", 24, "IDR", true)
		code, body := proposePayment(t, env.srv, env.wsA, dA, env.ownerToken, "sum-31a-p", map[string]any{"amount_minor": "100", "currency": "IDR", "method": "cash"})
		if code != http.StatusCreated {
			t.Fatalf("propose: want 201, got %d (%v)", code, body)
		}
		proposalID, _ := body["id"].(string)
		insertCorruptPayment(t, env.boot, env.wsA, dA, factA, proposalID, "sum-31a-pay", "150", "IDR", env.ownerID, env.mgrID)
		code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		assertSummaryCorrupt(t, code, body)
	})
	t.Run("mismatched-payment-currency", func(t *testing.T) {
		env := setupPayments(t)
		d, factID, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-32", "sum-32", "100", 24, "IDR", true)
		code, body := proposePayment(t, env.srv, env.wsA, d, env.ownerToken, "sum-32-p", map[string]any{"amount_minor": "100", "currency": "IDR", "method": "cash"})
		if code != http.StatusCreated {
			t.Fatalf("propose: want 201, got %d (%v)", code, body)
		}
		proposalID, _ := body["id"].(string)
		insertCorruptPayment(t, env.boot, env.wsA, d, factID, proposalID, "sum-32-pay", "50", "USD", env.ownerID, env.mgrID)
		code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		assertSummaryCorrupt(t, code, body)
	})
	t.Run("mismatched-proposal-currency", func(t *testing.T) {
		env := setupPayments(t)
		d, factID, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-33", "sum-33", "100", 24, "IDR", true)
		insertCorruptProposal(t, env.boot, env.wsA, d, factID, "sum-33-pp", "50", "USD", env.ownerID)
		code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		assertSummaryCorrupt(t, code, body)
	})
	t.Run("pending-over-ceiling-stays-valid", func(t *testing.T) {
		env := setupPayments(t)
		d, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-34", "sum-34", "100", 24, "IDR", true)
		code, body := proposePayment(t, env.srv, env.wsA, d, env.ownerToken, "sum-34-p", map[string]any{"amount_minor": "101", "currency": "IDR", "method": "cash"})
		if code != http.StatusCreated {
			t.Fatalf("usulan over-ceiling: want 201, got %d (%v)", code, body)
		}
		code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("summary sesudah usulan over-ceiling: want 200, got %d (%v)", code, body)
		}
		assertSummaryCurrency(t, body, "IDR", "100", "0", "100", 0, 0, 1, 0)
	})
}

func TestFinanceSummary_Access(t *testing.T) {
	env := setupPayments(t)
	d, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-40", "sum-40", "150000", 48, "IDR", true)
	payPosted(t, env, env.wsA, d, env.ownerToken, env.mgrToken, "sum-40-a", "100000", "IDR")
	for _, tc := range []struct{ name, token string }{
		{"owner", env.ownerToken}, {"manager", env.mgrToken}, {"cashier", env.cashToken},
	} {
		code, body := getFinanceSummary(t, env.srv, env.wsA, tc.token)
		if code != http.StatusOK {
			t.Fatalf("summary %s: want 200, got %d (%v)", tc.name, code, body)
		}
		assertSummaryCurrency(t, body, "IDR", "300000", "100000", "200000", 1, 0, 0, 0)
	}
	code, _ := getFinanceSummary(t, env.srv, env.wsA, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("summary tanpa token: want 401, got %d", code)
	}
	code, body := getFinanceSummary(t, env.srv, "bukan-uuid", env.ownerToken)
	if code != http.StatusBadRequest {
		t.Fatalf("summary uuid buruk: want 400, got %d (%v)", code, body)
	}
	code, _ = getFinanceSummary(t, env.srv, "11111111-2222-3333-4444-555555555555", env.ownerToken)
	if code != http.StatusForbidden {
		t.Fatalf("summary ws asing: want 403, got %d", code)
	}
	code, _ = getFinanceSummary(t, env.srv, env.wsA, env.otherToken)
	if code != http.StatusForbidden {
		t.Fatalf("summary nonmember: want 403, got %d", code)
	}
	if _, err := env.boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2 AND role = 'cashier'`, env.wsA, env.cashID); err != nil {
		t.Fatalf("hapus peran cashier: %v", err)
	}
	code, _ = getFinanceSummary(t, env.srv, env.wsA, env.cashToken)
	if code != http.StatusForbidden {
		t.Fatalf("summary tanpa peran baca: want 403, got %d", code)
	}
	if _, err := env.boot.Exec(`INSERT INTO membership_roles (workspace_id, subject_id, role) VALUES ($1, $2, 'cashier') ON CONFLICT DO NOTHING`, env.wsA, env.cashID); err != nil {
		t.Fatalf("pulihkan peran cashier: %v", err)
	}
	code, _ = getFinanceSummary(t, env.srv, env.wsA, env.cashToken)
	if code != http.StatusOK {
		t.Fatalf("summary sesudah pulih peran: want 200, got %d", code)
	}
	if _, err := env.boot.Exec(`DELETE FROM membership_roles WHERE workspace_id = $1 AND subject_id = $2`, env.wsA, env.cashID); err != nil {
		t.Fatalf("hapus peran cashier: %v", err)
	}
	if _, err := env.boot.Exec(`DELETE FROM workspace_memberships WHERE workspace_id = $1 AND subject_id = $2`, env.wsA, env.cashID); err != nil {
		t.Fatalf("hapus membership cashier: %v", err)
	}
	code, _ = getFinanceSummary(t, env.srv, env.wsA, env.cashToken)
	if code != http.StatusForbidden {
		t.Fatalf("summary tanpa membership: want 403, got %d", code)
	}
	code, body = getFinanceSummaryRaw(t, env.srv, env.wsA, env.ownerToken, "currency=IDR")
	if code != http.StatusBadRequest {
		t.Fatalf("summary ?currency=IDR: want 400, got %d (%v)", code, body)
	}
	code, body = getFinanceSummaryRaw(t, env.srv, env.wsA, env.ownerToken, "from=2026-01-01")
	if code != http.StatusBadRequest {
		t.Fatalf("summary ?from=: want 400, got %d (%v)", code, body)
	}
	code, body = getFinanceSummaryRaw(t, env.srv, env.wsA, env.ownerToken, "currency=")
	if code != http.StatusBadRequest {
		t.Fatalf("summary ?currency= kosong: want 400, got %d (%v)", code, body)
	}
	code, body = getPayments(t, env.srv, env.wsA, d, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("histori lama: want 200, got %d (%v)", code, body)
	}
	if body["paid_minor"] != "100000" || body["total_minor"] != "300000" {
		t.Fatalf("histori lama berubah: %v", body)
	}
}

func TestFinanceSummary_NoFinanceGate(t *testing.T) {
	env := setupPayments(t)
	d, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-41", "sum-41", "150000", 48, "IDR", false)
	code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary tanpa finance: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "0", "300000", 0, 0, 0, 0)
	code, body = proposePayment(t, env.srv, env.wsA, d, env.ownerToken, "sum-41-p", map[string]any{"amount_minor": "1000", "currency": "IDR", "method": "cash"})
	if code != http.StatusForbidden {
		t.Fatalf("tulis tanpa finance: want 403, got %d (%v)", code, body)
	}
	if body["error"] != "finance belum aktif di workspace ini" {
		t.Fatalf("tulis tanpa finance: want error finance belum aktif, got %v", body)
	}
	activateFinance(t, env, env.wsA, env.ownerToken)
	payPosted(t, env, env.wsA, d, env.ownerToken, env.mgrToken, "sum-41-a", "100000", "IDR")
	code, body = deactivateModule(t, env.srv, env.wsA, "finance.basic", env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("deaktivasi finance: want 200, got %d (%v)", code, body)
	}
	code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary sesudah deactivate: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "100000", "200000", 1, 0, 0, 0)
	bin := buildGrantCLI(t)
	opURL := os.Getenv("OPERATOR_TEST_DATABASE_URL")
	if opURL == "" {
		t.Fatalf("OPERATOR_TEST_DATABASE_URL kosong — provisioning operator belum dijalankan; tes gagal, bukan dilewati")
	}
	revokeFinanceViaCLI(t, bin, opURL, env.wsA, "tes cabut ringkasan")
	code, body = getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary sesudah revoke: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "100000", "200000", 1, 0, 0, 0)
}

func TestFinanceSummary_NoWritesOnGet(t *testing.T) {
	env := setupPayments(t)
	d, _, _ := seedSummaryChain(t, env, env.wsA, env.ownerToken, "SKU-SUM-50", "sum-50", "150000", 48, "IDR", true)
	p1 := payPosted(t, env, env.wsA, d, env.ownerToken, env.mgrToken, "sum-50-a", "100000", "IDR")
	reversePosted(t, env, env.wsA, d, p1, env.mgrToken, env.ownerToken, "sum-50-r1")
	payPosted(t, env, env.wsA, d, env.ownerToken, env.mgrToken, "sum-50-b", "50000", "IDR")
	code, body := proposePayment(t, env.srv, env.wsA, d, env.ownerToken, "sum-50-pp", map[string]any{"amount_minor": "10000", "currency": "IDR", "method": "cash"})
	if code != http.StatusCreated {
		t.Fatalf("pending proposal: want 201, got %d (%v)", code, body)
	}
	before := snapshotSummaryState(t, env)
	for i := 0; i < 3; i++ {
		code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
		if code != http.StatusOK {
			t.Fatalf("GET %d: want 200, got %d (%v)", i, code, body)
		}
		assertSummaryCurrency(t, body, "IDR", "300000", "50000", "250000", 2, 1, 1, 0)
	}
	after := snapshotSummaryState(t, env)
	if before != after {
		t.Fatalf("GET mengubah state: sebelum=%+v sesudah=%+v", before, after)
	}
}

func TestFinanceSummary_ConcurrentConsistency(t *testing.T) {
	env := setupPayments(t)
	draftID, _, total := seedPaymentChain(t, env, "SKU-SUM-60", "sum-60")
	if total != "300000" {
		t.Fatalf("total: want 300000, got %s", total)
	}
	const cycles = 12
	const amount = "10000"
	const reads = 40
	amountBig := big.NewInt(10000)
	totalBig := big.NewInt(300000)
	// Satu context/deadline untuk SELURUH tes ini, dibuat SEBELUM
	// reader/writer dimulai dan dipropagasi ke setiap GET/POST:
	// request yang terblokir gagal cepat saat deadline, bukan
	// menggantung tes.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	writerErr := make(chan error, 1)
	var writerOK atomic.Int32
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < cycles; i++ {
			pfx := fmt.Sprintf("sum-60-c%d", i)
			code, body, err := postRawGoCtx(ctx, env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals", env.ownerToken, []string{pfx + "-p"}, `{"amount_minor":"10000","currency":"IDR","method":"cash"}`)
			if err != nil || code != http.StatusCreated {
				writerErr <- fmt.Errorf("cycle %d propose: err=%v code=%d", i, err, code)
				return
			}
			proposalID, _ := body["id"].(string)
			code, body, err = postRawGoCtx(ctx, env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payment-proposals/"+proposalID+"/approvals", env.mgrToken, []string{pfx + "-a"}, `{}`)
			if err != nil || code != http.StatusCreated {
				writerErr <- fmt.Errorf("cycle %d approve: err=%v code=%d", i, err, code)
				return
			}
			writerOK.Add(2)
			paymentID, _ := body["id"].(string)
			code, body, err = postRawGoCtx(ctx, env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payments/"+paymentID+"/reversal-proposals", env.mgrToken, []string{pfx + "-rp"}, `{"reason":"siklus `+pfx+`"}`)
			if err != nil || code != http.StatusCreated {
				writerErr <- fmt.Errorf("cycle %d propose reversal: err=%v code=%d", i, err, code)
				return
			}
			revProposalID, _ := body["id"].(string)
			code, _, err = postRawGoCtx(ctx, env.srv, "/workspaces/"+env.wsA+"/rental/contracts/"+draftID+"/payments/"+paymentID+"/reversal-proposals/"+revProposalID+"/approvals", env.ownerToken, []string{pfx + "-ra"}, `{}`)
			if err != nil || code != http.StatusCreated {
				writerErr <- fmt.Errorf("cycle %d approve reversal: err=%v code=%d", i, err, code)
				return
			}
			writerOK.Add(2)
		}
	}()
	var readErr error
	checked := 0
	for i := 0; i < reads; i++ {
		code, body, err := getFinanceSummaryGoCtx(ctx, env.srv, env.wsA, env.ownerToken)
		if err != nil {
			readErr = fmt.Errorf("GET %d: %v", i, err)
			break
		}
		if code != http.StatusOK {
			readErr = fmt.Errorf("GET %d: want 200, got %d (%v)", i, code, body)
			break
		}
		if _, ok := body["observed_at"].(string); !ok {
			readErr = fmt.Errorf("GET %d: tanpa observed_at: %v", i, body)
			break
		}
		m, ok := findSummaryCurrency(body, "IDR")
		if !ok {
			readErr = fmt.Errorf("GET %d: tanpa currency IDR: %v", i, body)
			break
		}
		payments, _ := m["payments_count"].(float64)
		reversals, _ := m["reversals_count"].(float64)
		pendPay, _ := m["pending_payment_proposals_count"].(float64)
		pendRev, _ := m["pending_reversal_proposals_count"].(float64)
		if m["total_minor"] != "300000" {
			readErr = fmt.Errorf("GET %d: total want 300000, got %v", i, m["total_minor"])
			break
		}
		active := big.NewInt(int64(payments - reversals))
		wantPaid := new(big.Int).Mul(amountBig, active)
		if m["paid_minor"] != wantPaid.String() {
			readErr = fmt.Errorf("GET %d: paid want %s, got %v (payments=%v reversals=%v)", i, wantPaid, m["paid_minor"], payments, reversals)
			break
		}
		wantOut := new(big.Int).Sub(totalBig, wantPaid)
		if m["outstanding_minor"] != wantOut.String() {
			readErr = fmt.Errorf("GET %d: outstanding want %s, got %v", i, wantOut, m["outstanding_minor"])
			break
		}
		if payments < 0 || payments > cycles || reversals < 0 || reversals > payments || pendPay < 0 || pendPay > 1 || pendRev < 0 || pendRev > 1 {
			readErr = fmt.Errorf("GET %d: hitungan tak terjangkau: %v", i, m)
			break
		}
		checked++
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		// Deadline: batalkan, lalu tunggu writer benar-benar
		// selesai (request-nya gagal cepat karena context) sebelum
		// cleanup fixture — tanpa waiter tak berbatas.
		cancel()
		<-done
		t.Fatal("concurrent test timed out (context deadline)")
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
	select {
	case err := <-writerErr:
		t.Fatalf("writer gagal: %v", err)
	default:
	}
	if checked == 0 {
		t.Fatalf("tidak ada respons GET yang diperiksa")
	}
	if got := writerOK.Load(); got != 4*cycles {
		t.Fatalf("writer: want %d panggilan sukses, got %d", 4*cycles, got)
	}
	code, body, err := getFinanceSummaryGoCtx(ctx, env.srv, env.wsA, env.ownerToken)
	if err != nil {
		t.Fatalf("summary akhir: %v", err)
	}
	if code != http.StatusOK {
		t.Fatalf("summary akhir: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "0", "300000", cycles, cycles, 0, 0)
}

// TestFinanceSummary_QueryRejected membuktikan ringkasan tidak
// menerima parameter query apa pun: hanya GET polos (atau trailing
// "?" dengan RawQuery kosong) yang lolos; field kosong, field
// malformed, dan escape tidak valid semuanya 400.
func TestFinanceSummary_QueryRejected(t *testing.T) {
	env := setupPayments(t)
	seedPaymentChain(t, env, "SKU-SUM-Q", "sum-q")
	code, body := getFinanceSummary(t, env.srv, env.wsA, env.ownerToken)
	if code != http.StatusOK {
		t.Fatalf("summary polos: want 200, got %d (%v)", code, body)
	}
	assertSummaryCurrency(t, body, "IDR", "300000", "0", "300000", 0, 0, 0, 0)
	// Trailing "?" (RawQuery kosong) tidak ditolak.
	req, err := http.NewRequest(http.MethodGet, env.srv.URL+"/workspaces/"+env.wsA+"/finance/summary?", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+env.ownerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("summary trailing ?: want 200, got %d", resp.StatusCode)
	}
	rejected := []struct {
		name  string
		query string
	}{
		{"currency kosong", "currency="},
		{"as_of kosong", "as_of="},
		{"as_of escape rusak", "as_of=%ZZ"},
		{"dua field", "currency=USD;as_of=2020-01-01"},
		{"nama field escape rusak", "%ZZ=USD"},
		{"currency biasa", "currency=IDR"},
		{"from biasa", "from=2026-01-01"},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			code, body := getFinanceSummaryRaw(t, env.srv, env.wsA, env.ownerToken, tc.query)
			if code != http.StatusBadRequest {
				t.Fatalf("summary ?%s: want 400, got %d (%v)", tc.query, code, body)
			}
		})
	}
}

// TestFinanceSummary_BlockedRequestCancels membuktikan request GET
// ringkasan yang terblokir di server berhenti cepat begitu context
// berakhir — bukan menunggu server melepasnya — dan handler/server
// dilepas (drain) sesudahnya tanpa goroutine bocor.
func TestFinanceSummary_BlockedRequestCancels(t *testing.T) {
	entered := make(chan struct{})
	released := make(chan struct{})
	finished := make(chan struct{})
	var once sync.Once
	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(entered) })
		<-released
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"currencies":[]}`))
		close(finished)
	}))
	defer blocked.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	type result struct {
		code int
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		code, _, err := getFinanceSummaryGoCtx(ctx, blocked, "11111111-2222-3333-4444-555555555555", "token")
		resCh <- result{code, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(released)
		t.Fatal("blocked server tidak menerima request")
	}
	start := time.Now()
	select {
	case res := <-resCh:
		elapsed := time.Since(start)
		if res.err == nil {
			close(released)
			t.Fatalf("blocked GET: want deadline error, got code %d", res.code)
		}
		if !errors.Is(res.err, context.DeadlineExceeded) {
			close(released)
			t.Fatalf("blocked GET: want context.DeadlineExceeded, got %v", res.err)
		}
		if elapsed > 5*time.Second {
			close(released)
			t.Fatalf("blocked GET lambat: %v", elapsed)
		}
	case <-time.After(10 * time.Second):
		close(released)
		t.Fatal("blocked GET tidak berhenti dalam 10 detik")
	}
	close(released)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("handler blocked tidak selesai sesudah release")
	}
}
