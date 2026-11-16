package main

// Handler F2-02-A — pencatatan pembayaran manual + pembalikan catatan
// penuh dengan approval dua aktor (migrasi 013).
// Urutan beku semua tulis: auth -> transaksi tenant -> hak
// manage_business_data + view_workspace -> lock baris workspace ->
// finance.basic 0.1.0 AKTIF + entitlement versi eksak + manifest
// dikenal (dicek ulang sesudah lock) -> satu cutoff DB sesudah lock
// -> replay kunci operasi -> target/kelayakan/pemeriksa/plafon baru
// -> INSERT immutable + audit -> COMMIT. TIDAK ADA kapabilitas
// finance.payment.write (tidak ada di manifest); gerbangnya adalah
// modul/versi eksak + hak bisnis + approval dua aktor. Aktivasi
// modul, reason, atau audit saja TIDAK PERNAH menggantikan approval.
// Replay memakai prelookup di bawah lock + INSERT ... ON CONFLICT
// DO NOTHING RETURNING: tidak ada query sesudah transaksi abort
// 23505, karena 23505 tidak pernah dilempar. Total dihitung SQL
// dalam numeric (sum(bigint) -> numeric); handler tidak pernah
// menjumlah gross dalam int64 dan tidak pernah clamp.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const financeModuleID = "finance.basic"
const financeModuleVersion = "0.1.0"

var errTotalsCorrupt = errors.New("total tidak konsisten")

// beginTenantSnapshot seperti beginTenant tetapi dengan isolasi
// REPEATABLE READ: semua bacaan GET histori berbagi SATU snapshot
// database (bukan bacaan campur di sekitar approval bersamaan).
func beginTenantSnapshot(ctx context.Context, db *sql.DB, subject, workspace string) (*sql.Tx, error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`SELECT set_config('app.current_subject_id', $1, true)`, subject); err != nil {
		tx.Rollback()
		return nil, err
	}
	if workspace != "" {
		if _, err := tx.Exec(`SELECT set_config('app.current_workspace_id', $1, true)`, workspace); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	return tx, nil
}

// financeGuard memeriksa gerbang finance.basic 0.1.0 SESUDAH lock:
// instalasi aktif versi eksak 0.1.0 + entitlement versi eksak +
// manifest dikenal di registry. Mengembalikan "" bila lolos, pesan
// 403 bila tidak, atau galat bila query gagal.
func financeGuard(tx *sql.Tx, wsID string) (string, error) {
	inst, err := loadInstallation(tx, wsID, financeModuleID)
	if err != nil {
		return "", err
	}
	if inst == nil || inst.Status != "active" || inst.Version != financeModuleVersion {
		return "finance belum aktif di workspace ini", nil
	}
	entitled, err := hasEntitlement(tx, wsID, financeModuleID, financeModuleVersion)
	if err != nil {
		return "", err
	}
	if !entitled {
		return "finance belum menjadi hak workspace ini", nil
	}
	_, found, err := loadManifest(tx, financeModuleID, financeModuleVersion)
	if err != nil {
		return "", err
	}
	if !found {
		return "finance belum menjadi hak workspace ini", nil
	}
	return "", nil
}

// isCanonicalNonNegativeInt memeriksa string desimal nonnegatif
// kanonis ("0" atau [1-9][0-9]*): bentuk yang dihasilkan agregasi
// numeric SQL atas jumlah bulat. Bukan parser uang (itu
// parseCanonicalMoney).
func isCanonicalNonNegativeInt(s string) bool {
	if s == "" {
		return false
	}
	if s == "0" {
		return true
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if i == 0 {
			if c < '1' || c > '9' {
				return false
			}
		} else if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// paymentFact adalah fakta harga acuan untuk satu draft.
type paymentFact struct {
	ID         string
	TotalMinor int64
	Currency   string
}

// readPaymentTotals membaca fakta + paid/outstanding aktif dalam
// numeric (SQL), plus flag over-cap. Paid adalah jumlah pembayaran
// posted yang TIDAK punya pembalikan posted; proposal dikecualikan,
// usulan pembalikan saja tidak berpengaruh. Fakta hilang ->
// sql.ErrNoRows (pemanggil membalas 409). Teks yang bukan desimal
// kanonis -> errTotalsCorrupt.
// readPaymentFact membaca SATU-SATUNYA fakta harga draft (id, total,
// currency) tanpa agregasi pembayaran. Dipakai jalur usulan: usulan
// tidak memeriksa plafon (proposal inert) — hanya keberadaan fakta
// (409 bila belum ada) dan kecocokan currency (409). Agregasi
// paid/outstanding (dan penolakan 500 atas data korup) hanya
// relevan di approval dan GET.
func readPaymentFact(tx *sql.Tx, wsID, draftID string) (paymentFact, error) {
	var f paymentFact
	err := tx.QueryRow(
		`SELECT id, total_minor, currency FROM rental_agreed_prices
		 WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID).Scan(&f.ID, &f.TotalMinor, &f.Currency)
	return f, err
}

func readPaymentTotals(tx *sql.Tx, wsID, draftID string) (paymentFact, string, string, bool, error) {
	var fact paymentFact
	var paidText, outstandingText string
	var overCap bool
	err := tx.QueryRow(
		`SELECT f.id::text, f.total_minor, f.currency,
			COALESCE(SUM(p.amount_minor) FILTER (WHERE NOT EXISTS (
				SELECT 1 FROM rental_payment_reversals r
				WHERE r.workspace_id = p.workspace_id AND r.payment_id = p.id
			)), 0)::text AS paid_text,
			(f.total_minor::numeric - COALESCE(SUM(p.amount_minor) FILTER (WHERE NOT EXISTS (
				SELECT 1 FROM rental_payment_reversals r
				WHERE r.workspace_id = p.workspace_id AND r.payment_id = p.id
			)), 0))::text AS outstanding_text,
			(COALESCE(SUM(p.amount_minor) FILTER (WHERE NOT EXISTS (
				SELECT 1 FROM rental_payment_reversals r
				WHERE r.workspace_id = p.workspace_id AND r.payment_id = p.id
			)), 0) > f.total_minor::numeric) AS over_cap
		 FROM rental_agreed_prices f
		 LEFT JOIN rental_payments p ON p.workspace_id = f.workspace_id AND p.rental_contract_id = f.rental_contract_id
		 WHERE f.workspace_id = $1 AND f.rental_contract_id = $2
		 GROUP BY f.id, f.total_minor, f.currency`,
		wsID, draftID).Scan(&fact.ID, &fact.TotalMinor, &fact.Currency, &paidText, &outstandingText, &overCap)
	if err != nil {
		return paymentFact{}, "", "", false, err
	}
	if !isCanonicalNonNegativeInt(paidText) || !isCanonicalNonNegativeInt(outstandingText) {
		return paymentFact{}, "", "", false, errTotalsCorrupt
	}
	return fact, paidText, outstandingText, overCap, nil
}

// normalizePaymentReference: nil -> NULL; string disimpan VERBATIM
// bila 1-128 rune dan mengandung karakter non-whitespace.
func normalizePaymentReference(ref *string) (sql.NullString, bool) {
	if ref == nil {
		return sql.NullString{}, true
	}
	if utf8.RuneCountInString(*ref) < 1 || utf8.RuneCountInString(*ref) > 128 {
		return sql.NullString{}, false
	}
	for _, r := range *ref {
		if !unicode.IsSpace(r) {
			return sql.NullString{String: *ref, Valid: true}, true
		}
	}
	return sql.NullString{}, false
}

// normalizeReversalReason: wajib string; di-TRIM, 1-500 rune,
// disimpan dalam bentuk trimmed.
func normalizeReversalReason(reason *string) (string, bool) {
	if reason == nil {
		return "", false
	}
	trimmed := strings.TrimSpace(*reason)
	n := utf8.RuneCountInString(trimmed)
	if n < 1 || n > 500 {
		return "", false
	}
	return trimmed, true
}

// decodeEmptyApprovalBody memastikan body approval tepat satu objek
// JSON kosong {}: kosong/null/skalar/array/field tak dikenal/trailing
// ditolak (pola strict handleReturnRental).
func decodeEmptyApprovalBody(w http.ResponseWriter, r *http.Request) error {
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMasterDataBodyBytes))
	if err != nil {
		return err
	}
	if len(bytes.TrimSpace(bodyBytes)) == 0 {
		return errors.New("body kosong")
	}
	trimmed := bytes.TrimSpace(bodyBytes)
	if trimmed[0] != '{' {
		return errors.New("body bukan objek")
	}
	var parsed struct{}
	dec := json.NewDecoder(bytes.NewReader(bodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("dokumen JSON tambahan")
		}
		return err
	}
	return nil
}

// --- Baris + DTO (pola custodyRow/custodyToDTO) ---

const paymentProposalColumns = `id, workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at`

type paymentProposalRow struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, ClientRequestID string
	AmountMinor                                                             int64
	Currency, Method                                                        string
	Reference                                                               sql.NullString
	ProposedBy                                                              string
	ProposedAt                                                              time.Time
}

func scanPaymentProposalRow(row *sql.Row) (paymentProposalRow, error) {
	var p paymentProposalRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID, &p.ClientRequestID,
		&p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt)
	return p, err
}

type paymentProposalDTO struct {
	ID                  string  `json:"id"`
	RentalContractID    string  `json:"rental_contract_id"`
	RentalAgreedPriceID string  `json:"rental_agreed_price_id"`
	AmountMinor         string  `json:"amount_minor"`
	Currency            string  `json:"currency"`
	Method              string  `json:"method"`
	Reference           *string `json:"reference"`
	ProposedBy          string  `json:"proposed_by"`
	ProposedAt          string  `json:"proposed_at"`
	ClientRequestID     string  `json:"client_request_id"`
	Status              string  `json:"status"`
}

func paymentProposalToDTO(p paymentProposalRow) paymentProposalDTO {
	var ref *string
	if p.Reference.Valid {
		s := p.Reference.String
		ref = &s
	}
	return paymentProposalDTO{
		ID: p.ID, RentalContractID: p.RentalContractID, RentalAgreedPriceID: p.RentalAgreedPriceID,
		AmountMinor: strconv.FormatInt(p.AmountMinor, 10), Currency: p.Currency, Method: p.Method,
		Reference: ref, ProposedBy: p.ProposedBy, ProposedAt: p.ProposedAt.UTC().Format(time.RFC3339Nano),
		ClientRequestID: p.ClientRequestID, Status: "pending",
	}
}

const paymentColumns = `id, workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at`

// paymentColumnsWithReversal = paymentColumns + proyeksi pembalikan
// turunan. Dipakai semua SELECT baris-tunggal agar DTO replay juga
// benar soal reversed (baris tanpa alias tabel).
const paymentColumnsWithReversal = paymentColumns + `,
	(SELECT r.id FROM rental_payment_reversals r
	 WHERE r.workspace_id = rental_payments.workspace_id AND r.payment_id = rental_payments.id)`

type paymentRow struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, ProposalID, ClientRequestID string
	AmountMinor                                                                         int64
	Currency, Method                                                                    string
	Reference                                                                           sql.NullString
	ProposedBy                                                                          string
	ProposedAt                                                                          time.Time
	ApprovedBy                                                                          string
	ApprovedAt                                                                          time.Time
	ReversalID                                                                          sql.NullString
}

func scanPaymentRow(row *sql.Row) (paymentRow, error) {
	var p paymentRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID, &p.ProposalID, &p.ClientRequestID,
		&p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt, &p.ApprovedBy, &p.ApprovedAt, &p.ReversalID)
	return p, err
}

type paymentDTO struct {
	ID                  string  `json:"id"`
	ProposalID          string  `json:"proposal_id"`
	RentalContractID    string  `json:"rental_contract_id"`
	RentalAgreedPriceID string  `json:"rental_agreed_price_id"`
	AmountMinor         string  `json:"amount_minor"`
	Currency            string  `json:"currency"`
	Method              string  `json:"method"`
	Reference           *string `json:"reference"`
	ProposedBy          string  `json:"proposed_by"`
	ProposedAt          string  `json:"proposed_at"`
	ApprovedBy          string  `json:"approved_by"`
	ApprovedAt          string  `json:"approved_at"`
	ClientRequestID     string  `json:"client_request_id"`
	Reversed            bool    `json:"reversed"`
	ReversalID          *string `json:"reversal_id"`
}

func paymentToDTO(p paymentRow) paymentDTO {
	var ref *string
	if p.Reference.Valid {
		s := p.Reference.String
		ref = &s
	}
	var revID *string
	if p.ReversalID.Valid {
		s := p.ReversalID.String
		revID = &s
	}
	return paymentDTO{
		ID: p.ID, ProposalID: p.ProposalID, RentalContractID: p.RentalContractID, RentalAgreedPriceID: p.RentalAgreedPriceID,
		AmountMinor: strconv.FormatInt(p.AmountMinor, 10), Currency: p.Currency, Method: p.Method,
		Reference: ref, ProposedBy: p.ProposedBy, ProposedAt: p.ProposedAt.UTC().Format(time.RFC3339Nano),
		ApprovedBy: p.ApprovedBy, ApprovedAt: p.ApprovedAt.UTC().Format(time.RFC3339Nano),
		ClientRequestID: p.ClientRequestID, Reversed: p.ReversalID.Valid, ReversalID: revID,
	}
}

const reversalProposalColumns = `id, workspace_id, rental_contract_id, payment_id, client_request_id, reason, proposed_by, proposed_at`

type reversalProposalRow struct {
	ID, WorkspaceID, RentalContractID, PaymentID, ClientRequestID, Reason string
	ProposedBy                                                            string
	ProposedAt                                                            time.Time
}

func scanReversalProposalRow(row *sql.Row) (reversalProposalRow, error) {
	var p reversalProposalRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID, &p.ClientRequestID,
		&p.Reason, &p.ProposedBy, &p.ProposedAt)
	return p, err
}

type reversalProposalDTO struct {
	ID               string `json:"id"`
	PaymentID        string `json:"payment_id"`
	RentalContractID string `json:"rental_contract_id"`
	Reason           string `json:"reason"`
	ProposedBy       string `json:"proposed_by"`
	ProposedAt       string `json:"proposed_at"`
	ClientRequestID  string `json:"client_request_id"`
	Status           string `json:"status"`
}

func reversalProposalToDTO(p reversalProposalRow) reversalProposalDTO {
	return reversalProposalDTO{
		ID: p.ID, PaymentID: p.PaymentID, RentalContractID: p.RentalContractID, Reason: p.Reason,
		ProposedBy: p.ProposedBy, ProposedAt: p.ProposedAt.UTC().Format(time.RFC3339Nano),
		ClientRequestID: p.ClientRequestID, Status: "pending",
	}
}

const reversalColumns = `id, workspace_id, rental_contract_id, payment_id, reversal_proposal_id, client_request_id, reason, proposed_by, proposed_at, approved_by, approved_at`

type reversalRow struct {
	ID, WorkspaceID, RentalContractID, PaymentID, ReversalProposalID, ClientRequestID, Reason string
	ProposedBy                                                                                string
	ProposedAt                                                                                time.Time
	ApprovedBy                                                                                string
	ApprovedAt                                                                                time.Time
}

func scanReversalRow(row *sql.Row) (reversalRow, error) {
	var p reversalRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID, &p.ReversalProposalID, &p.ClientRequestID,
		&p.Reason, &p.ProposedBy, &p.ProposedAt, &p.ApprovedBy, &p.ApprovedAt)
	return p, err
}

type reversalDTO struct {
	ID                 string `json:"id"`
	PaymentID          string `json:"payment_id"`
	ReversalProposalID string `json:"reversal_proposal_id"`
	RentalContractID   string `json:"rental_contract_id"`
	Reason             string `json:"reason"`
	ProposedBy         string `json:"proposed_by"`
	ProposedAt         string `json:"proposed_at"`
	ApprovedBy         string `json:"approved_by"`
	ApprovedAt         string `json:"approved_at"`
	ClientRequestID    string `json:"client_request_id"`
}

func reversalToDTO(p reversalRow) reversalDTO {
	return reversalDTO{
		ID: p.ID, PaymentID: p.PaymentID, ReversalProposalID: p.ReversalProposalID, RentalContractID: p.RentalContractID,
		Reason: p.Reason, ProposedBy: p.ProposedBy, ProposedAt: p.ProposedAt.UTC().Format(time.RFC3339Nano),
		ApprovedBy: p.ApprovedBy, ApprovedAt: p.ApprovedAt.UTC().Format(time.RFC3339Nano),
		ClientRequestID: p.ClientRequestID,
	}
}

type paymentsHistoryDTO struct {
	RentalContractID         string                `json:"rental_contract_id"`
	RentalAgreedPriceID      string                `json:"rental_agreed_price_id"`
	Currency                 string                `json:"currency"`
	TotalMinor               string                `json:"total_minor"`
	PaidMinor                string                `json:"paid_minor"`
	OutstandingMinor         string                `json:"outstanding_minor"`
	Payments                 []paymentDTO          `json:"payments"`
	Reversals                []reversalDTO         `json:"reversals"`
	PendingPaymentProposals  []paymentProposalDTO  `json:"pending_payment_proposals"`
	PendingReversalProposals []reversalProposalDTO `json:"pending_reversal_proposals"`
}

// --- INSERT aman-konflik (ON CONFLICT DO NOTHING RETURNING) ---
// Bila konflik, mengembalikan "" tanpa galat sehingga transaksi TIDAK
// abort dan pemanggil bisa baca ulang lalu jawab 200/409.

func insertPaymentProposal(tx *sql.Tx, p paymentProposalRow) (string, error) {
	var id string
	err := tx.QueryRow(
		`INSERT INTO rental_payment_proposals (id, workspace_id, rental_contract_id, rental_agreed_price_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (workspace_id, client_request_id) DO NOTHING RETURNING id`,
		p.ID, p.WorkspaceID, p.RentalContractID, p.RentalAgreedPriceID, p.ClientRequestID,
		p.AmountMinor, p.Currency, p.Method, p.Reference, p.ProposedBy, p.ProposedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func insertPayment(tx *sql.Tx, p paymentRow) (string, error) {
	var id string
	err := tx.QueryRow(
		`INSERT INTO rental_payments (id, workspace_id, rental_contract_id, rental_agreed_price_id, proposal_id, client_request_id, amount_minor, currency, method, reference, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
		 ON CONFLICT (workspace_id, client_request_id) DO NOTHING RETURNING id`,
		p.ID, p.WorkspaceID, p.RentalContractID, p.RentalAgreedPriceID, p.ProposalID, p.ClientRequestID,
		p.AmountMinor, p.Currency, p.Method, p.Reference, p.ProposedBy, p.ProposedAt, p.ApprovedBy, p.ApprovedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func insertReversalProposal(tx *sql.Tx, p reversalProposalRow) (string, error) {
	var id string
	err := tx.QueryRow(
		`INSERT INTO rental_payment_reversal_proposals (id, workspace_id, rental_contract_id, payment_id, client_request_id, reason, proposed_by, proposed_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		 ON CONFLICT (workspace_id, client_request_id) DO NOTHING RETURNING id`,
		p.ID, p.WorkspaceID, p.RentalContractID, p.PaymentID, p.ClientRequestID,
		p.Reason, p.ProposedBy, p.ProposedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

func insertReversal(tx *sql.Tx, p reversalRow) (string, error) {
	var id string
	err := tx.QueryRow(
		`INSERT INTO rental_payment_reversals (id, workspace_id, rental_contract_id, payment_id, reversal_proposal_id, client_request_id, reason, proposed_by, proposed_at, approved_by, approved_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		 ON CONFLICT (workspace_id, client_request_id) DO NOTHING RETURNING id`,
		p.ID, p.WorkspaceID, p.RentalContractID, p.PaymentID, p.ReversalProposalID, p.ClientRequestID,
		p.Reason, p.ProposedBy, p.ProposedAt, p.ApprovedBy, p.ApprovedAt).Scan(&id)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return id, nil
}

// paymentAuthIDs adalah awal bersama keempat handler tulis SEBELUM
// body/transaksi: auth -> UUID workspace/draft -> kunci. Mengembalikan
// subjek + id + kunci, atau sudah membalas galat.
func (s *server) paymentAuthIDs(w http.ResponseWriter, r *http.Request) (string, string, string, string, bool) {
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return "", "", "", "", false
	}
	wsID := r.PathValue("id")
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return "", "", "", "", false
	}
	draftID := r.PathValue("draft_id")
	if !isValidUUID(draftID) {
		writeError(w, http.StatusBadRequest, "id draft tidak valid")
		return "", "", "", "", false
	}
	draftID = strings.ToLower(draftID)
	key, ok := paymentKey(w, r)
	if !ok {
		return "", "", "", "", false
	}
	return subject, wsID, draftID, key, true
}

// paymentOpenTx membuka transaksi tenant SESUDAH body terurai: hak
// manage_business_data + view_workspace -> lock baris workspace ->
// guard finance (dicek ulang sesudah lock) -> satu cutoff DB.
// Mengembalikan tx + cutoff, atau sudah membalas galat (tx
// di-rollback di sini pada jalur gagal).
func (s *server) paymentOpenTx(w http.ResponseWriter, subject, wsID string) (*sql.Tx, time.Time, bool) {
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return nil, time.Time{}, false
	}
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_business_data")
	if err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return nil, time.Time{}, false
	}
	if !allowed {
		tx.Rollback()
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola data usaha")
		return nil, time.Time{}, false
	}
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return nil, time.Time{}, false
	}
	if !allowed {
		tx.Rollback()
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return nil, time.Time{}, false
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return nil, time.Time{}, false
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return nil, time.Time{}, false
	}
	if msg, err := financeGuard(tx, wsID); err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return nil, time.Time{}, false
	} else if msg != "" {
		tx.Rollback()
		writeError(w, http.StatusForbidden, msg)
		return nil, time.Time{}, false
	}
	cutoff, err := captureCutoff(tx)
	if err != nil {
		tx.Rollback()
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return nil, time.Time{}, false
	}
	return tx, cutoff, true
}

// paymentKey membaca tepat satu header Idempotency-Key yang valid.
func paymentKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return "", false
	}
	return keys[0], true
}

// paymentProposalMatches membandingkan usulan replay dengan payload
// ternormalisasi + target permintaan saat ini.
func paymentProposalMatches(p paymentProposalRow, draftID string, amount int64, currency, method string, reference sql.NullString) bool {
	return p.RentalContractID == draftID && p.AmountMinor == amount && p.Currency == currency &&
		p.Method == method && p.Reference == reference
}

// draftExists memeriksa draft di workspace ini (404 bila tidak ada).
func draftExists(tx *sql.Tx, wsID, draftID string) (bool, error) {
	var exists bool
	err := tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM rental_contracts WHERE workspace_id = $1 AND id = $2)`, wsID, draftID).Scan(&exists)
	return exists, err
}

// handleProposePayment mengusulkan pembayaran manual (cash/transfer)
// terhadap fakta harga draft yang sama. Proposal TIDAK memeriksa
// plafon saat ini dan TIDAK mereservasi saldo; plafon hanya ditegakkan
// di approval. Replay kunci yang sama + payload ternormalisasi yang
// sama -> 200 tanpa tulis/audit; kunci sama isi/target berbeda -> 409.
func (s *server) handleProposePayment(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, wsID, draftID, key, ok := s.paymentAuthIDs(w, r)
	if !ok {
		return
	}
	var body struct {
		AmountMinor string  `json:"amount_minor"`
		Currency    string  `json:"currency"`
		Method      string  `json:"method"`
		Reference   *string `json:"reference"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	amount, ok := parseCanonicalMoney(body.AmountMinor)
	if !ok || amount <= 0 {
		writeError(w, http.StatusBadRequest, "jumlah tidak valid")
		return
	}
	if !validCurrencyCode(body.Currency) {
		writeError(w, http.StatusBadRequest, "mata uang tidak valid")
		return
	}
	if body.Method != "cash" && body.Method != "transfer" {
		writeError(w, http.StatusBadRequest, "metode tidak valid")
		return
	}
	reference, ok := normalizePaymentReference(body.Reference)
	if !ok {
		writeError(w, http.StatusBadRequest, "referensi tidak valid")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, cutoff, ok := s.paymentOpenTx(w, subject, wsID)
	if !ok {
		return
	}
	defer tx.Rollback()
	// Replay kunci proposal: lookup workspace-scoped SEBELUM kelayakan
	// baru. Kunci + draft + payload ternormalisasi yang sama -> 200.
	replayed, err := scanPaymentProposalRow(tx.QueryRow(
		`SELECT `+paymentProposalColumns+`
		 FROM rental_payment_proposals WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if paymentProposalMatches(replayed, draftID, amount, body.Currency, body.Method, reference) {
			writeJSON(w, http.StatusOK, paymentProposalToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Kelayakan baru: draft ada (404), fakta harga ada (409),
	// currency sama dengan fakta (409). Plafon TIDAK dicek di sini:
	// usulan inert, dan data korup (paid > total) tidak boleh
	// menggagalkan usulan — penolakan 500 hanya di approval/GET.
	exists, err := draftExists(tx, wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	fact, err := readPaymentFact(tx, wsID, draftID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "harga disepakati belum ada")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if body.Currency != fact.Currency {
		writeError(w, http.StatusConflict, "mata uang tidak cocok")
		return
	}
	row := paymentProposalRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, RentalAgreedPriceID: fact.ID,
		ClientRequestID: key, AmountMinor: amount, Currency: body.Currency, Method: body.Method,
		Reference: reference, ProposedBy: subject, ProposedAt: cutoff,
	}
	insertedID, err := insertPaymentProposal(tx, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if insertedID == "" {
		// Penulis lain menyisipkan kunci yang sama di antara prelookup
		// dan INSERT: baca ulang dan jawab dari keadaan saat ini.
		replayed, err := scanPaymentProposalRow(tx.QueryRow(
			`SELECT `+paymentProposalColumns+`
			 FROM rental_payment_proposals WHERE workspace_id = $1 AND client_request_id = $2`,
			wsID, key))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if paymentProposalMatches(replayed, draftID, amount, body.Currency, body.Method, reference) {
			writeJSON(w, http.StatusOK, paymentProposalToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	details, _ := json.Marshal(map[string]string{
		"rental_contract_id": draftID, "rental_agreed_price_id": fact.ID, "proposal_id": id,
		"amount_minor": strconv.FormatInt(amount, 10), "currency": body.Currency, "method": body.Method,
	})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_payment_proposed", "rental_payment_proposal", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, paymentProposalToDTO(row))
}

// handleApprovePayment menyetujui usulan pembayaran (body tepat {})
// menjadi fakta posted immutable. Pemeriksa harus berbeda dari
// pengusul; plafon dihitung ulang di bawah lock; jumlah disalin
// verbatim dari usulan (bukan dari body approval).
func (s *server) handleApprovePayment(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, wsID, draftID, key, ok := s.paymentAuthIDs(w, r)
	if !ok {
		return
	}
	proposalID := r.PathValue("proposal_id")
	if !isValidUUID(proposalID) {
		writeError(w, http.StatusBadRequest, "id proposal tidak valid")
		return
	}
	proposalID = strings.ToLower(proposalID)
	if err := decodeEmptyApprovalBody(w, r); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, cutoff, ok := s.paymentOpenTx(w, subject, wsID)
	if !ok {
		return
	}
	defer tx.Rollback()
	// Replay kunci approval: lookup di fakta posted. Kunci + draft +
	// usulan yang sama -> 200 fakta itu; kunci sama target berbeda
	// -> 409.
	replayed, err := scanPaymentRow(tx.QueryRow(
		`SELECT `+paymentColumnsWithReversal+`
		 FROM rental_payments WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if replayed.RentalContractID == draftID && replayed.ProposalID == proposalID {
			writeJSON(w, http.StatusOK, paymentToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Usulan harus ada pada draft ini (404).
	proposal, err := scanPaymentProposalRow(tx.QueryRow(
		`SELECT `+paymentProposalColumns+`
		 FROM rental_payment_proposals WHERE workspace_id = $1 AND id = $2`,
		wsID, proposalID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "proposal tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if proposal.RentalContractID != draftID {
		writeError(w, http.StatusNotFound, "proposal tidak ditemukan")
		return
	}
	// Usulan sudah diposting dengan kunci lain -> 409.
	var postedID string
	err = tx.QueryRow(`SELECT id FROM rental_payments WHERE workspace_id = $1 AND proposal_id = $2`, wsID, proposalID).Scan(&postedID)
	if err == nil {
		writeError(w, http.StatusConflict, "proposal sudah disetujui")
		return
	}
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Pemeriksa harus berbeda dari pengusul (identitas dari token).
	if proposal.ProposedBy == subject {
		writeError(w, http.StatusForbidden, "penyetuju harus berbeda dari pengusul")
		return
	}
	// Plafon dihitung ulang SEKARANG di bawah lock (numeric SQL).
	_, _, outstandingText, overCap, err := readPaymentTotals(tx, wsID, draftID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "harga disepakati belum ada")
		return
	}
	if err != nil {
		if errors.Is(err, errTotalsCorrupt) {
			writeError(w, http.StatusInternalServerError, "total tidak konsisten")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if overCap {
		writeError(w, http.StatusInternalServerError, "total tidak konsisten")
		return
	}
	outstanding, err := strconv.ParseInt(outstandingText, 10, 64)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "total tidak konsisten")
		return
	}
	if proposal.AmountMinor > outstanding {
		writeError(w, http.StatusConflict, "pembayaran melebihi sisa")
		return
	}
	row := paymentRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, RentalAgreedPriceID: proposal.RentalAgreedPriceID,
		ProposalID: proposalID, ClientRequestID: key, AmountMinor: proposal.AmountMinor,
		Currency: proposal.Currency, Method: proposal.Method, Reference: proposal.Reference,
		ProposedBy: proposal.ProposedBy, ProposedAt: proposal.ProposedAt,
		ApprovedBy: subject, ApprovedAt: cutoff,
	}
	insertedID, err := insertPayment(tx, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if insertedID == "" {
		// Konflik di antara prelookup dan INSERT: baca ulang kunci,
		// lalu target (usulan sudah diposting kunci lain).
		replayed, err := scanPaymentRow(tx.QueryRow(
			`SELECT `+paymentColumnsWithReversal+`
			 FROM rental_payments WHERE workspace_id = $1 AND client_request_id = $2`,
			wsID, key))
		if err == nil {
			if replayed.RentalContractID == draftID && replayed.ProposalID == proposalID {
				writeJSON(w, http.StatusOK, paymentToDTO(replayed))
				return
			}
			writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
			return
		}
		if err != sql.ErrNoRows {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if err := tx.QueryRow(`SELECT id FROM rental_payments WHERE workspace_id = $1 AND proposal_id = $2`, wsID, proposalID).Scan(&postedID); err == nil {
			writeError(w, http.StatusConflict, "proposal sudah disetujui")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	details, _ := json.Marshal(map[string]string{
		"rental_contract_id": draftID, "rental_agreed_price_id": proposal.RentalAgreedPriceID,
		"proposal_id": proposalID, "payment_id": id,
		"amount_minor": strconv.FormatInt(proposal.AmountMinor, 10), "currency": proposal.Currency, "method": proposal.Method,
	})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_payment_posted", "rental_payment", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, paymentToDTO(row))
}

// handleProposeReversal mengusulkan pembalikan penuh satu pembayaran
// posted. Beberapa usulan pending untuk pembayaran yang sama
// diizinkan; usulan baru terhadap pembayaran yang SUDAH dibatalkan
// fakta posted -> 409. Alasan wajib (trim, 1-500 rune).
func (s *server) handleProposeReversal(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, wsID, draftID, key, ok := s.paymentAuthIDs(w, r)
	if !ok {
		return
	}
	paymentID := r.PathValue("payment_id")
	if !isValidUUID(paymentID) {
		writeError(w, http.StatusBadRequest, "id pembayaran tidak valid")
		return
	}
	paymentID = strings.ToLower(paymentID)
	var body struct {
		Reason *string `json:"reason"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	reason, ok := normalizeReversalReason(body.Reason)
	if !ok {
		writeError(w, http.StatusBadRequest, "alasan tidak valid")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, cutoff, ok := s.paymentOpenTx(w, subject, wsID)
	if !ok {
		return
	}
	defer tx.Rollback()
	// Replay kunci usulan pembalikan: kunci + draft + pembayaran +
	// alasan (trimmed) yang sama -> 200.
	replayed, err := scanReversalProposalRow(tx.QueryRow(
		`SELECT `+reversalProposalColumns+`
		 FROM rental_payment_reversal_proposals WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if replayed.RentalContractID == draftID && replayed.PaymentID == paymentID && replayed.Reason == reason {
			writeJSON(w, http.StatusOK, reversalProposalToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Pembayaran harus ada pada draft ini (404).
	payment, err := scanPaymentRow(tx.QueryRow(
		`SELECT `+paymentColumnsWithReversal+`
		 FROM rental_payments WHERE workspace_id = $1 AND id = $2`,
		wsID, paymentID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "pembayaran tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if payment.RentalContractID != draftID {
		writeError(w, http.StatusNotFound, "pembayaran tidak ditemukan")
		return
	}
	// Pembayaran sudah dibatalkan fakta posted -> 409 (usulan pending
	// lain tidak menghalangi; hanya fakta posted yang menghalangi).
	var reversedID string
	err = tx.QueryRow(`SELECT id FROM rental_payment_reversals WHERE workspace_id = $1 AND payment_id = $2`, wsID, paymentID).Scan(&reversedID)
	if err == nil {
		writeError(w, http.StatusConflict, "pembayaran sudah dibatalkan")
		return
	}
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// proposed_at (cutoff) tidak boleh sebelum approved_at pembayaran
	// target (jam DB monoton; pelanggaran berarti inkonsistensi jam).
	if cutoff.Before(payment.ApprovedAt) {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	row := reversalProposalRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, PaymentID: paymentID,
		ClientRequestID: key, Reason: reason, ProposedBy: subject, ProposedAt: cutoff,
	}
	insertedID, err := insertReversalProposal(tx, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if insertedID == "" {
		replayed, err := scanReversalProposalRow(tx.QueryRow(
			`SELECT `+reversalProposalColumns+`
			 FROM rental_payment_reversal_proposals WHERE workspace_id = $1 AND client_request_id = $2`,
			wsID, key))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if replayed.RentalContractID == draftID && replayed.PaymentID == paymentID && replayed.Reason == reason {
			writeJSON(w, http.StatusOK, reversalProposalToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	details, _ := json.Marshal(map[string]string{
		"rental_contract_id": draftID, "payment_id": paymentID, "reversal_proposal_id": id, "reason": reason,
	})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_payment_reversal_proposed", "rental_payment_reversal_proposal", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, reversalProposalToDTO(row))
}

// handleApproveReversal menyetujui usulan pembalikan (body tepat {})
// menjadi fakta pembalikan posted immutable. Jumlah penuh diturunkan
// dari pembayaran immutable (tidak ada jumlah baru dari klien).
// Pemeriksa harus berbeda dari pengusul.
func (s *server) handleApproveReversal(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, wsID, draftID, key, ok := s.paymentAuthIDs(w, r)
	if !ok {
		return
	}
	paymentID := r.PathValue("payment_id")
	if !isValidUUID(paymentID) {
		writeError(w, http.StatusBadRequest, "id pembayaran tidak valid")
		return
	}
	paymentID = strings.ToLower(paymentID)
	proposalID := r.PathValue("proposal_id")
	if !isValidUUID(proposalID) {
		writeError(w, http.StatusBadRequest, "id proposal tidak valid")
		return
	}
	proposalID = strings.ToLower(proposalID)
	if err := decodeEmptyApprovalBody(w, r); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, cutoff, ok := s.paymentOpenTx(w, subject, wsID)
	if !ok {
		return
	}
	defer tx.Rollback()
	// Replay kunci approval pembalikan: kunci + draft + pembayaran +
	// usulan yang sama -> 200 fakta itu.
	replayed, err := scanReversalRow(tx.QueryRow(
		`SELECT `+reversalColumns+`
		 FROM rental_payment_reversals WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if replayed.RentalContractID == draftID && replayed.PaymentID == paymentID && replayed.ReversalProposalID == proposalID {
			writeJSON(w, http.StatusOK, reversalToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Usulan pembalikan harus ada pada draft + pembayaran ini (404).
	proposal, err := scanReversalProposalRow(tx.QueryRow(
		`SELECT `+reversalProposalColumns+`
		 FROM rental_payment_reversal_proposals WHERE workspace_id = $1 AND id = $2`,
		wsID, proposalID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "proposal pembalikan tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if proposal.RentalContractID != draftID || proposal.PaymentID != paymentID {
		writeError(w, http.StatusNotFound, "proposal pembalikan tidak ditemukan")
		return
	}
	// Pembayaran sudah dibatalkan fakta lain -> 409.
	var reversedID string
	err = tx.QueryRow(`SELECT id FROM rental_payment_reversals WHERE workspace_id = $1 AND payment_id = $2`, wsID, paymentID).Scan(&reversedID)
	if err == nil {
		writeError(w, http.StatusConflict, "pembayaran sudah dibatalkan")
		return
	}
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Pemeriksa harus berbeda dari pengusul.
	if proposal.ProposedBy == subject {
		writeError(w, http.StatusForbidden, "penyetuju harus berbeda dari pengusul")
		return
	}
	row := reversalRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, PaymentID: paymentID,
		ReversalProposalID: proposalID, ClientRequestID: key, Reason: proposal.Reason,
		ProposedBy: proposal.ProposedBy, ProposedAt: proposal.ProposedAt,
		ApprovedBy: subject, ApprovedAt: cutoff,
	}
	insertedID, err := insertReversal(tx, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if insertedID == "" {
		replayed, err := scanReversalRow(tx.QueryRow(
			`SELECT `+reversalColumns+`
			 FROM rental_payment_reversals WHERE workspace_id = $1 AND client_request_id = $2`,
			wsID, key))
		if err == nil {
			if replayed.RentalContractID == draftID && replayed.PaymentID == paymentID && replayed.ReversalProposalID == proposalID {
				writeJSON(w, http.StatusOK, reversalToDTO(replayed))
				return
			}
			writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
			return
		}
		if err != sql.ErrNoRows {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if err := tx.QueryRow(`SELECT id FROM rental_payment_reversals WHERE workspace_id = $1 AND payment_id = $2`, wsID, paymentID).Scan(&reversedID); err == nil {
			writeError(w, http.StatusConflict, "pembayaran sudah dibatalkan")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	details, _ := json.Marshal(map[string]string{
		"rental_contract_id": draftID, "payment_id": paymentID,
		"reversal_proposal_id": proposalID, "reversal_id": id, "reason": proposal.Reason,
	})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_payment_reversal_posted", "rental_payment_reversal", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, reversalToDTO(row))
}

// handleGetPayments membaca riwayat pembayaran satu draft dalam SATU
// snapshot (transaksi repeatable-read): total + paid + outstanding +
// payments[] + reversals[] + pending[]. Hanya tenant + membership +
// view_workspace; tetap terbaca sesudah deactivate/revoke.
func (s *server) handleGetPayments(w http.ResponseWriter, r *http.Request) {
	if !s.requireDB(w) {
		return
	}
	subject, ok := s.authedSubject(w, r)
	if !ok {
		return
	}
	wsID := r.PathValue("id")
	if !isValidUUID(wsID) {
		writeError(w, http.StatusBadRequest, "id workspace tidak valid")
		return
	}
	draftID := r.PathValue("draft_id")
	if !isValidUUID(draftID) {
		writeError(w, http.StatusBadRequest, "id draft tidak valid")
		return
	}
	draftID = strings.ToLower(draftID)
	tx, err := beginTenantSnapshot(r.Context(), s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	exists, err := draftExists(tx, wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	fact, paidText, outstandingText, overCap, err := readPaymentTotals(tx, wsID, draftID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "harga disepakati belum ada")
		return
	}
	if err != nil {
		if errors.Is(err, errTotalsCorrupt) {
			writeError(w, http.StatusInternalServerError, "total tidak konsisten")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if overCap {
		writeError(w, http.StatusInternalServerError, "total tidak konsisten")
		return
	}
	// payments[] dengan proyeksi pembalikan turunan (reversed +
	// reversal_id), terurut waktu approval lalu id.
	paymentRows, err := tx.Query(
		`SELECT `+paymentColumns+`,
			(SELECT r.id FROM rental_payment_reversals r WHERE r.workspace_id = p.workspace_id AND r.payment_id = p.id) AS reversal_id
		 FROM rental_payments p
		 WHERE p.workspace_id = $1 AND p.rental_contract_id = $2
		 ORDER BY p.approved_at, p.id`,
		wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	payments := []paymentDTO{}
	for paymentRows.Next() {
		var p paymentRow
		if err := paymentRows.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID, &p.ProposalID, &p.ClientRequestID,
			&p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt, &p.ApprovedBy, &p.ApprovedAt, &p.ReversalID); err != nil {
			paymentRows.Close()
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		payments = append(payments, paymentToDTO(p))
	}
	paymentRows.Close()
	if err := paymentRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// reversals[] terurut waktu approval lalu id.
	reversalRows, err := tx.Query(
		`SELECT `+reversalColumns+`
		 FROM rental_payment_reversals
		 WHERE workspace_id = $1 AND rental_contract_id = $2
		 ORDER BY approved_at, id`,
		wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	reversals := []reversalDTO{}
	for reversalRows.Next() {
		var p reversalRow
		if err := reversalRows.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID, &p.ReversalProposalID, &p.ClientRequestID,
			&p.Reason, &p.ProposedBy, &p.ProposedAt, &p.ApprovedBy, &p.ApprovedAt); err != nil {
			reversalRows.Close()
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		reversals = append(reversals, reversalToDTO(p))
	}
	reversalRows.Close()
	if err := reversalRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// pending_payment_proposals[]: usulan tanpa fakta posted.
	pendingProposalRows, err := tx.Query(
		`SELECT `+paymentProposalColumns+`
		 FROM rental_payment_proposals pp
		 WHERE pp.workspace_id = $1 AND pp.rental_contract_id = $2
		 AND NOT EXISTS (SELECT 1 FROM rental_payments p WHERE p.workspace_id = pp.workspace_id AND p.proposal_id = pp.id)
		 ORDER BY pp.proposed_at, pp.id`,
		wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	pendingProposals := []paymentProposalDTO{}
	for pendingProposalRows.Next() {
		var p paymentProposalRow
		if err := pendingProposalRows.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.RentalAgreedPriceID, &p.ClientRequestID,
			&p.AmountMinor, &p.Currency, &p.Method, &p.Reference, &p.ProposedBy, &p.ProposedAt); err != nil {
			pendingProposalRows.Close()
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		pendingProposals = append(pendingProposals, paymentProposalToDTO(p))
	}
	pendingProposalRows.Close()
	if err := pendingProposalRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// pending_reversal_proposals[]: usulan pembalikan tanpa fakta
	// pembalikan posted.
	pendingReversalRows, err := tx.Query(
		`SELECT `+reversalProposalColumns+`
		 FROM rental_payment_reversal_proposals rp
		 WHERE rp.workspace_id = $1 AND rp.rental_contract_id = $2
		 AND NOT EXISTS (SELECT 1 FROM rental_payment_reversals r WHERE r.workspace_id = rp.workspace_id AND r.reversal_proposal_id = rp.id)
		 ORDER BY rp.proposed_at, rp.id`,
		wsID, draftID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	pendingReversals := []reversalProposalDTO{}
	for pendingReversalRows.Next() {
		var p reversalProposalRow
		if err := pendingReversalRows.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PaymentID, &p.ClientRequestID,
			&p.Reason, &p.ProposedBy, &p.ProposedAt); err != nil {
			pendingReversalRows.Close()
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		pendingReversals = append(pendingReversals, reversalProposalToDTO(p))
	}
	pendingReversalRows.Close()
	if err := pendingReversalRows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusOK, paymentsHistoryDTO{
		RentalContractID: draftID, RentalAgreedPriceID: fact.ID, Currency: fact.Currency,
		TotalMinor: strconv.FormatInt(fact.TotalMinor, 10), PaidMinor: paidText, OutstandingMinor: outstandingText,
		Payments: payments, Reversals: reversals,
		PendingPaymentProposals: pendingProposals, PendingReversalProposals: pendingReversals,
	})
}
