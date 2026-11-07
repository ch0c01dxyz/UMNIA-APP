package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Fakta harga disepakati F2-01-E (migrasi 009): satu baris
// rental_agreed_prices per draft Rental, dicatat staf (owner/manager)
// dari quote yang dilihatnya. Fakta ini BUKAN consent/tanda tangan
// pelanggan, BUKAN perjanjian hukum, BUKAN invoice/pembayaran, BUKAN
// posting ledger, BUKAN reservasi aset, dan BUKAN transisi draft.
// Draft, hold, dan versi harga tidak dimutasi oleh penerimaan ini.
// Hold live TIDAK disyaratkan: harga mengikat item katalog ke draft,
// bukan unit fisik. Arsip item / status hold saat ini TIDAK
// divalidasi ulang untuk fakta ini — tetap wajib hak aksi/baca Rental
// saat penerimaan (batas ini eksplisit, bukan kelalaian).

// pricingRuleVersionElapsed24hCeil adalah SATU-SATUNYA identifier
// aturan yang didukung tiket ini, untuk algoritma elapsed-24h/ceil
// F2-01-D: durasi elapsed mikrodetik UTC, ceil 24 jam, minimum 1
// unit, total int64 terperiksa. Bukan baris business_rule_versions,
// bukan UUID, bukan rule engine generik; perubahan formula butuh
// identifier baru + review.
const pricingRuleVersionElapsed24hCeil = "elapsed_24h_ceil_v1"

type agreedPriceRow struct {
	ID, WorkspaceID, RentalContractID, PriceVersionID, PricingRuleVersionID, CatalogItemID, CustomerID, ClientRequestID string
	AmountMinor, Units, TotalMinor                                                                                      int64
	Currency                                                                                                            string
	StartsAt, EndsAt, AcceptedAt                                                                                        time.Time
	AcceptedBy                                                                                                          string
}

type agreedPriceDTO struct {
	ID                   string `json:"id"`
	WorkspaceID          string `json:"workspace_id"`
	RentalContractID     string `json:"rental_contract_id"`
	PriceVersionID       string `json:"price_version_id"`
	PricingRuleVersionID string `json:"pricing_rule_version_id"`
	CatalogItemID        string `json:"catalog_item_id"`
	CustomerID           string `json:"customer_id"`
	ClientRequestID      string `json:"client_request_id"`
	AmountMinor          string `json:"amount_minor"`
	Currency             string `json:"currency"`
	Units                int64  `json:"units"`
	TotalMinor           string `json:"total_minor"`
	StartsAt             string `json:"starts_at"`
	EndsAt               string `json:"ends_at"`
	AcceptedAt           string `json:"accepted_at"`
	AcceptedBy           string `json:"accepted_by"`
}

const agreedPriceColumns = `id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id, client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by`

func agreedPriceToDTO(p agreedPriceRow) agreedPriceDTO {
	return agreedPriceDTO{
		ID: p.ID, WorkspaceID: p.WorkspaceID, RentalContractID: p.RentalContractID,
		PriceVersionID: p.PriceVersionID, PricingRuleVersionID: p.PricingRuleVersionID,
		CatalogItemID: p.CatalogItemID, CustomerID: p.CustomerID, ClientRequestID: p.ClientRequestID,
		AmountMinor: strconv.FormatInt(p.AmountMinor, 10), Currency: p.Currency,
		Units: p.Units, TotalMinor: strconv.FormatInt(p.TotalMinor, 10),
		StartsAt: formatRentalTimeUTC(p.StartsAt), EndsAt: formatRentalTimeUTC(p.EndsAt),
		AcceptedAt: formatRentalTimeUTC(p.AcceptedAt), AcceptedBy: p.AcceptedBy,
	}
}

func scanAgreedPriceRow(row *sql.Row) (agreedPriceRow, error) {
	var p agreedPriceRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.RentalContractID, &p.PriceVersionID, &p.PricingRuleVersionID,
		&p.CatalogItemID, &p.CustomerID, &p.ClientRequestID, &p.AmountMinor, &p.Currency, &p.Units,
		&p.TotalMinor, &p.StartsAt, &p.EndsAt, &p.AcceptedAt, &p.AcceptedBy)
	return p, err
}

func (s *server) handleAcceptPrice(w http.ResponseWriter, r *http.Request) {
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
	// UUID divalidasi dengan sintaks hyphenated kanonis; PostgreSQL
	// mengembalikan UUID dalam huruf kecil, jadi kanonikalisasi ke
	// huruf kecil SEBELUM dipakai di mana pun (perbandingan replay,
	// query, insert, audit) — pola yang sama dengan draft/hold/harga.
	// Idempotency-Key sengaja TIDAK diubah: kunci tetap case-sensitive.
	draftID = strings.ToLower(draftID)
	// Header: tepat satu nilai Idempotency-Key yang valid. Header
	// ganda (atau satu nilai berisi koma) ditolak; kunci mentah tidak
	// pernah di-log — hanya dipakai sebagai client_request_id.
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	var body struct {
		ExpectedPriceVersionID string `json:"expected_price_version_id"`
		ExpectedTotalMinor     string `json:"expected_total_minor"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	expectedVersionID := strings.TrimSpace(body.ExpectedPriceVersionID)
	if !isValidUUID(expectedVersionID) {
		writeError(w, http.StatusBadRequest, "expected_price_version_id tidak valid")
		return
	}
	expectedVersionID = strings.ToLower(expectedVersionID)
	// Total yang diharapkan adalah STRING desimal kanonis (pola yang
	// sama dengan amount_minor harga): tanpa trim — spasi, tanda,
	// leading zero, fraksi, eksponen, angka JSON, dan null ditolak.
	expectedTotal, ok := parseCanonicalMoney(body.ExpectedTotalMinor)
	if !ok {
		writeError(w, http.StatusBadRequest, "expected_total_minor tidak valid")
		return
	}
	id, err := uuidNew()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "id gagal dibuat")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	allowed, err := s.hasPermission(tx, wsID, subject, "manage_business_data")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin mengelola data usaha")
		return
	}
	// Lock baris workspace yang sama dengan B3, sesudah otorisasi dan
	// sebelum semua bacaan guard/state/referensi/replay; dipegang
	// sampai commit/rollback. Mutasi modul dan grant/revoke fixture
	// memakai lock dan urutan yang sama.
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// SATU clock_timestamp() database dalam pernyataan terpisah
	// SESUDAH lock: cutoff observasi untuk accepted_at (dan patokan
	// waktu operasi ini). Bukan klaim urutan commit database.
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Guard Rental: instalasi rental.basic harus aktif; entitlement
	// dicek ulang tiap request untuk versi aktif yang tepat —
	// pencabutan memblokir penerimaan baru walau baris instalasi
	// masih aktif (snapshot intent B3). Galat query → 500, bukan 403.
	inst, err := loadInstallation(tx, wsID, "rental.basic")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if inst == nil || inst.Status != "active" {
		writeError(w, http.StatusForbidden, "rental belum aktif di workspace ini")
		return
	}
	entitled, err := hasEntitlement(tx, wsID, "rental.basic", inst.Version)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if !entitled {
		writeError(w, http.StatusForbidden, "rental belum menjadi hak workspace ini")
		return
	}
	// Izin baca core eksplisit SEBELUM replay/bacaan referensi —
	// pola yang sama dengan draft/hold/harga (temuan F2-01-A:
	// suksesnya query yang disaring RLS bukan bukti otorisasi).
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Replay: identitas kunci mencakup KETIGA field — draft_id
	// ternormalisasi di path, expected_price_version_id
	// ternormalisasi, DAN expected_total_minor terurai kanonis.
	// Kunci yang sama untuk draft LAIN (walau versi + total sama)
	// adalah 409, bukan replay fakta draft lain. Hak Rental saat ini
	// sudah diperiksa DI ATAS, jadi replay sesudah revoke/deactivate
	// → 403; bacaan histori lewat GET tetap jalan.
	row, err := scanAgreedPriceRow(tx.QueryRow(
		`SELECT `+agreedPriceColumns+` FROM rental_agreed_prices WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err == nil {
		if row.RentalContractID == draftID && row.PriceVersionID == expectedVersionID && row.TotalMinor == expectedTotal {
			writeJSON(w, http.StatusOK, agreedPriceToDTO(row))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Satu fakta per draft: kunci lain untuk draft yang sudah punya
	// fakta → 409 terkendali (bukan 500 dari UNIQUE). Dicek di bawah
	// lock, dengan UNIQUE(workspace_id, rental_contract_id) sebagai
	// backstop untuk balapan yang sama persis.
	var existingID string
	err = tx.QueryRow(
		`SELECT id FROM rental_agreed_prices WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID).Scan(&existingID)
	if err == nil {
		writeError(w, http.StatusConflict, "draft ini sudah memiliki harga yang disepakati")
		return
	}
	if err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Draft milik sendiri; hilang/lintas-tenant → 404 identik.
	var draftCustomerID, draftItemID string
	var draftStarts, draftEnds time.Time
	err = tx.QueryRow(
		`SELECT customer_id, catalog_item_id, starts_at, ends_at FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		wsID, draftID).Scan(&draftCustomerID, &draftItemID, &draftStarts, &draftEnds)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Invarian draft tersimpan harus berada dalam rentang yang
	// didukung sebelum aritmetika: pelanggaran adalah galat backend
	// terkendali (500), bukan nilai yang dibungkus/dipotong.
	starts := draftStarts.UTC()
	ends := draftEnds.UTC()
	if starts.Year() < 1 || starts.Year() > 9999 || ends.Year() < 1 || ends.Year() > 9999 || !ends.After(starts) {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Versi efektif adalah tepat satu baris yang memenuhi
	// valid_from <= starts_at < valid_to untuk item + daftar
	// 'standard' ini. Harga finite yang tidak berubah + starts_at
	// draft yang tidak berubah berarti tarif adjacent baru TIDAK
	// bisa menggantikan versi efektif draft yang sama.
	rows, err := tx.Query(
		`SELECT `+priceColumns+`
		 FROM price_versions WHERE workspace_id = $1 AND catalog_item_id = $2 AND price_list_id = 'standard'
		   AND valid_from <= $3 AND $3 < valid_to`,
		wsID, draftItemID, starts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer rows.Close()
	var versions []priceRow
	for rows.Next() {
		var p priceRow
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.CatalogItemID, &p.PriceListID, &p.ClientRequestID,
			&p.ValidFrom, &p.ValidTo, &p.RecordedAt, &p.AmountMinor, &p.Currency); err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		versions = append(versions, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if len(versions) == 0 {
		writeError(w, http.StatusNotFound, "harga efektif tidak ditemukan")
		return
	}
	if len(versions) > 1 {
		// EXCLUDE seharusnya mencegah ini; dua baris berarti
		// invarian database rusak.
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	price := versions[0]
	// Aritmetika server yang sama dengan quote: selisih UnixMicro
	// terperiksa, ceil 24 jam minimum 1, total int64 terperiksa.
	units, ok := ceilUnits24h(starts, ends)
	if !ok {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	total, ok := checkedTotalMinor(price.AmountMinor, units)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "total di luar batas yang dapat diwakili")
		return
	}
	// Bandingkan versi AKTUAL + total YANG DIHITUNG SERVER dengan
	// yang diharapkan klien. Total expected hanya pembanding, BUKAN
	// jumlah otoritatif: yang disimpan selalu hasil hitung server.
	// Beda → 409, tanpa tulis/audit.
	if price.ID != expectedVersionID || total != expectedTotal {
		writeError(w, http.StatusConflict, "harga yang diharapkan tidak cocok dengan penawaran saat ini")
		return
	}
	// Simpan fakta + audit dalam SATU transaksi; 201 hanya sesudah
	// commit. accepted_at = cutoff pasca-lock; accepted_by = subjek
	// staf yang mencatat (bukan pelanggan).
	_, err = tx.Exec(
		`INSERT INTO rental_agreed_prices
			(id, workspace_id, rental_contract_id, price_version_id, pricing_rule_version_id, catalog_item_id, customer_id,
			 client_request_id, amount_minor, currency, units, total_minor, starts_at, ends_at, accepted_at, accepted_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)`,
		id, wsID, draftID, price.ID, pricingRuleVersionElapsed24hCeil, draftItemID, draftCustomerID,
		key, price.AmountMinor, price.Currency, units, total, starts, ends, cutoff, subject)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "rental_agreed_prices_workspace_id_rental_contract_id_key":
				writeError(w, http.StatusConflict, "draft ini sudah memiliki harga yang disepakati")
				return
			case "rental_agreed_prices_workspace_id_client_request_id_key":
				writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "harga gagal disepakati")
		return
	}
	details, _ := json.Marshal(map[string]string{
		"rental_contract_id": draftID,
		"price_version_id":   price.ID,
	})
	if err := writeBusinessAudit(tx, wsID, subject, "agreed_price_recorded", "rental_agreed_price", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, agreedPriceToDTO(agreedPriceRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, PriceVersionID: price.ID,
		PricingRuleVersionID: pricingRuleVersionElapsed24hCeil, CatalogItemID: draftItemID,
		CustomerID: draftCustomerID, ClientRequestID: key, AmountMinor: price.AmountMinor,
		Currency: price.Currency, Units: units, TotalMinor: total,
		StartsAt: starts, EndsAt: ends, AcceptedAt: cutoff, AcceptedBy: subject,
	}))
}

func (s *server) handleGetAgreedPrice(w http.ResponseWriter, r *http.Request) {
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
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	// Bacaan histori: hanya membership + view_workspace, tanpa lock,
	// tanpa tulis/audit, tanpa syarat instalasi/entitlement Rental.
	// Fakta tetap terbaca sesudah revoke/deactivate.
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	// Draft dibaca dulu supaya hilang/lintas-tenant → 404 yang
	// berbeda dari "tidak ada fakta harga".
	var exists string
	err = tx.QueryRow(
		`SELECT id FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		wsID, draftID).Scan(&exists)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "harga yang disepakati gagal dibaca")
		return
	}
	row, err := scanAgreedPriceRow(tx.QueryRow(
		`SELECT `+agreedPriceColumns+` FROM rental_agreed_prices WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "harga yang disepakati tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "harga yang disepakati gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, agreedPriceToDTO(row))
}
