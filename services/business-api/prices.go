package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Handler versi harga + quote F2-01-D. Satu baris price_versions
// adalah satu interval tarif finite [valid_from, valid_to) untuk satu
// catalog item pada price_list 'standard' — BUKAN harga yang disepakati
// (agreed amount), BUKAN snapshot yang disimpan, BUKAN pengikat harga,
// dan BUKAN transisi status draft. Quote dihitung dari versi ini pada
// saat dibaca dan tidak disimpan di mana pun; byte respons quote bukan
// fakta yang disepakati. Tidak ada harga open-ended, koreksi umum,
// backdate, billing, pajak/deposit/biaya, FX, invoice/pembayaran,
// stock ledger, handover/retur, atau UI di slice ini.
// Administrasi harga adalah master data core (manage_business_data),
// tanpa syarat aktivasi/entitlement modul; bacaan list/quote hanya
// view_workspace dan tetap terbaca sesudah arsip/revoke/deactivate.

// microsPerDay adalah jumlah mikrodetik dalam satu unit tarif 24 jam.
const microsPerDay = int64(86400 * 1000000)

// parseCanonicalMoney memeriksa string uang kanonis: "0" atau
// [1-9][0-9]*, tanpa leading zero, tanda, whitespace, fraksi, eksponen,
// dan tanpa melebihi MaxInt64. Bentuk JSON number TIDAK PERNAH sampai
// ke sini: field amount_minor didekode sebagai string, jadi angka JSON
// gagal di decodeStrictJSON (400 body). ParseInt dipakai sesudah bentuk
// diperiksa, jadi "+" / "-" tidak pernah lolos.
func parseCanonicalMoney(s string) (int64, bool) {
	if s == "" {
		return 0, false
	}
	if s == "0" {
		return 0, true
	}
	if s[0] < '1' || s[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// validCurrencyCode memeriksa kode mata uang: tepat 3 huruf ASCII
// A-Z, tanpa trim/default. Ini HANYA sintaks yang disimpan — bukan
// klaim mata uang ISO tervalidasi atau kebijakan komersial.
func validCurrencyCode(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if s[i] < 'A' || s[i] > 'Z' {
			return false
		}
	}
	return true
}

// parsePriceInstant memvalidasi tanggal harga seperti tanggal draft:
// RFC3339 dengan zona eksplisit, tahun input DAN tahun ternormalisasi
// UTC 1-9999, normalisasi mikrodetik UTC.
func parsePriceInstant(s string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil || t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, false
	}
	t = normalizeInstant(t)
	if t.Year() < 1 || t.Year() > 9999 {
		return time.Time{}, false
	}
	return t, true
}

// ceilUnits24h menghitung unit tarif 24 jam dari dua instant yang
// sudah ternormalisasi UTC (mikrodetik) dengan aritmetika integer
// eksak: diff_us = ends.UnixMicro() - starts.UnixMicro(), lalu
// quotient + 1 bila ada sisa bukan nol. time.Duration/Sub dan
// float Hours/Ceil DILARANG: Sub jenuh pada ~292 tahun (probe:
// 2562047h47m16.854775807s == MaxInt64 ns), sedangkan selisih
// UnixMicro maksimum pada rentang yang didukung adalah
// 315537897599999999 < MaxInt64, jadi pengurangan tak dapat overflow;
// units maksimum 3652059. ok=false bila ends tidak sesudah starts.
func ceilUnits24h(starts, ends time.Time) (int64, bool) {
	diffUs := ends.UTC().UnixMicro() - starts.UTC().UnixMicro()
	if diffUs <= 0 {
		return 0, false
	}
	units := diffUs / microsPerDay
	if diffUs%microsPerDay != 0 {
		units++
	}
	return units, true
}

// checkedTotalMinor menghitung rate x units dengan pemeriksaan
// overflow SEBELUM perkalian. Rate 0 menghasilkan total "0" tanpa
// pembagian. ok=false berarti total di luar batas int64 (handler
// membalas 422, bukan 500).
func checkedTotalMinor(rate, units int64) (int64, bool) {
	if rate == 0 {
		return 0, true
	}
	if units <= 0 || rate > math.MaxInt64/units {
		return 0, false
	}
	return rate * units, true
}

type priceRow struct {
	ID              string
	WorkspaceID     string
	CatalogItemID   string
	PriceListID     string
	ClientRequestID string
	ValidFrom       time.Time
	ValidTo         time.Time
	RecordedAt      time.Time
	AmountMinor     int64
	Currency        string
}

type priceDTO struct {
	ID              string `json:"id"`
	WorkspaceID     string `json:"workspace_id"`
	CatalogItemID   string `json:"catalog_item_id"`
	PriceListID     string `json:"price_list_id"`
	ClientRequestID string `json:"client_request_id"`
	ValidFrom       string `json:"valid_from"`
	ValidTo         string `json:"valid_to"`
	RecordedAt      string `json:"recorded_at"`
	AmountMinor     string `json:"amount_minor"`
	Currency        string `json:"currency"`
}

// priceToDTO memakai UTC RFC3339Nano untuk semua timestamp dan string
// desimal kanonis untuk uang, supaya integer di luar 2^53-1 tetap
// eksak melewati JSON (konsumen JavaScript/Nuxt).
func priceToDTO(p priceRow) priceDTO {
	return priceDTO{
		ID: p.ID, WorkspaceID: p.WorkspaceID, CatalogItemID: p.CatalogItemID,
		PriceListID: p.PriceListID, ClientRequestID: p.ClientRequestID,
		ValidFrom: formatRentalTimeUTC(p.ValidFrom), ValidTo: formatRentalTimeUTC(p.ValidTo),
		RecordedAt:  formatRentalTimeUTC(p.RecordedAt),
		AmountMinor: strconv.FormatInt(p.AmountMinor, 10), Currency: p.Currency,
	}
}

const priceColumns = `id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency`

func scanPriceRow(row *sql.Row) (priceRow, error) {
	var p priceRow
	err := row.Scan(&p.ID, &p.WorkspaceID, &p.CatalogItemID, &p.PriceListID, &p.ClientRequestID,
		&p.ValidFrom, &p.ValidTo, &p.RecordedAt, &p.AmountMinor, &p.Currency)
	return p, err
}

func (s *server) handleCreatePrice(w http.ResponseWriter, r *http.Request) {
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
	itemID := r.PathValue("item_id")
	if !isValidUUID(itemID) {
		writeError(w, http.StatusBadRequest, "id barang tidak valid")
		return
	}
	itemID = strings.ToLower(itemID)
	// Header: tepat satu nilai Idempotency-Key yang valid (aturan
	// yang sama dengan draft/hold); kunci mentah tidak pernah di-log.
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	var body struct {
		ValidFrom   string `json:"valid_from"`
		ValidTo     string `json:"valid_to"`
		AmountMinor string `json:"amount_minor"`
		Currency    string `json:"currency"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	validFrom, ok := parsePriceInstant(body.ValidFrom)
	if !ok {
		writeError(w, http.StatusBadRequest, "valid_from tidak valid")
		return
	}
	validTo, ok := parsePriceInstant(body.ValidTo)
	if !ok {
		writeError(w, http.StatusBadRequest, "valid_to tidak valid")
		return
	}
	if !validTo.After(validFrom) {
		writeError(w, http.StatusBadRequest, "valid_to harus sesudah valid_from")
		return
	}
	amount, ok := parseCanonicalMoney(body.AmountMinor)
	if !ok {
		writeError(w, http.StatusBadRequest, "amount_minor tidak valid")
		return
	}
	if !validCurrencyCode(body.Currency) {
		writeError(w, http.StatusBadRequest, "currency tidak valid")
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
	// Lock baris workspace yang sama dengan B3/draft/hold, sesudah
	// otorisasi dan sebelum semua bacaan guard/state/referensi/
	// replay; dipegang sampai commit/rollback.
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Cutoff diambil SESUDAH lock dalam pernyataan terpisah:
	// permintaan yang menunggu lock harus memakai jam sesudah lock
	// didapat — valid_from yang tadinya di masa depan bisa sudah
	// lewat saat lock diperoleh, dan itu harus ditolak 400.
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Izin baca core dicek eksplisit SEBELUM replay maupun referensi:
	// sukses bacaan bukan bukti otorisasi (RLS menyaring tanpa galat).
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Replay: cari versi yang sudah tercatat dengan kunci yang sama,
	// di bawah lock dan RLS normal — SEBELUM cek arsip, no-backdate,
	// dan overlap. Replay adalah bacaan atas histori yang sudah
	// commit: kunci yang valid_from-nya kini sudah lewat tetap
	// mengembalikan baris asli tanpa tulis/audit. Bandingkan field
	// ternormalisasi (item, valid_from, valid_to, amount, currency):
	// sama → 200 baris asli; berbeda → 409, tanpa tulis.
	existing, err := scanPriceRow(tx.QueryRow(
		`SELECT `+priceColumns+`
		 FROM price_versions WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if existing.CatalogItemID == itemID && existing.ValidFrom.Equal(validFrom) &&
			existing.ValidTo.Equal(validTo) && existing.AmountMinor == amount &&
			existing.Currency == body.Currency {
			writeJSON(w, http.StatusOK, priceToDTO(existing))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Rujukan untuk jadwal BARU: barang dibaca TANPA filter status di
	// WHERE supaya arsip bisa dibedakan dari hilang: hilang/
	// lintas-tenant → 404, milik sendiri tapi archived → 409.
	var found string
	var itemStatus string
	err = tx.QueryRow(
		`SELECT id, status FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, itemID).Scan(&found, &itemStatus)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "barang tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if itemStatus != "active" {
		writeError(w, http.StatusConflict, "barang tidak aktif")
		return
	}
	// Tanpa backdating: valid_from yang sudah lewat pada cutoff
	// (jam DB sesudah lock) → 400, tanpa tulis/audit. Tidak ada
	// grace period, clamping, atau pengisian historis.
	if validFrom.Before(cutoff) {
		writeError(w, http.StatusBadRequest, "valid_from tidak boleh di masa lalu")
		return
	}
	// Overlap dicek di bawah lock; EXCLUDE bernama adalah backstop
	// untuk penulis yang melewati lock. Interval half-open '[)':
	// v2 yang mulai tepat pada v1.valid_to TIDAK overlap.
	var overlaps bool
	err = tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM price_versions
		 WHERE workspace_id = $1 AND catalog_item_id = $2 AND price_list_id = 'standard'
		   AND tstzrange(valid_from, valid_to, '[)') && tstzrange($3, $4, '[)'))`,
		wsID, itemID, validFrom, validTo).Scan(&overlaps)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if overlaps {
		writeError(w, http.StatusConflict, "interval harga bertabrakan")
		return
	}
	// recorded_at diikat ke cutoff (SATAU-SATUNYA fakta temporal
	// baris ini); tidak ada created_at/updated_at pada tabel ini.
	stored, err := scanPriceRow(tx.QueryRow(
		`INSERT INTO price_versions (id, workspace_id, catalog_item_id, price_list_id, client_request_id, valid_from, valid_to, recorded_at, amount_minor, currency)
		 VALUES ($1, $2, $3, 'standard', $4, $5, $6, $7, $8, $9) RETURNING `+priceColumns,
		id, wsID, itemID, key, validFrom, validTo, cutoff, amount, body.Currency))
	if err != nil {
		// 409 HANYA untuk pelanggaran EXCLUDE overlap bernama
		// (SQLSTATE 23P01); galat lain → 500 tanpa bocor detail SQL
		// dan tanpa menebak jenis galat dari teks pesan.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23P01" &&
			pgErr.ConstraintName == "price_versions_no_overlap" {
			writeError(w, http.StatusConflict, "interval harga bertabrakan")
			return
		}
		writeError(w, http.StatusInternalServerError, "harga gagal dibuat")
		return
	}
	details, _ := json.Marshal(map[string]string{"catalog_item_id": itemID})
	if err := writeBusinessAudit(tx, wsID, subject, "price_version_created", "price_version", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "harga gagal dibuat")
		return
	}
	writeJSON(w, http.StatusCreated, priceToDTO(stored))
}

func (s *server) handleListPrices(w http.ResponseWriter, r *http.Request) {
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
	itemID := r.PathValue("item_id")
	if !isValidUUID(itemID) {
		writeError(w, http.StatusBadRequest, "id barang tidak valid")
		return
	}
	itemID = strings.ToLower(itemID)
	limit, offset, ok := parsePagination(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "limit/offset tidak valid")
		return
	}
	tx, err := beginTenant(s.db, subject, wsID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	defer tx.Rollback()
	// Bacaan histori/core: hanya membership + view_workspace, tanpa
	// syarat aktivasi/entitlement — harga yang sudah tercatat tetap
	// terbaca sesudah arsip/revoke/deactivate.
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	// Keberadaan item dicek eksplisit supaya hilang/lintas-tenant →
	// 404 identik, bukan list kosong; status arsip TIDAK diperiksa
	// di sini (histori harga item yang diarsipkan tetap terbaca).
	var found string
	err = tx.QueryRow(
		`SELECT id FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, itemID).Scan(&found)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "barang tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar harga gagal dibaca")
		return
	}
	rows, err := tx.Query(
		`SELECT `+priceColumns+`
		 FROM price_versions WHERE workspace_id = $1 AND catalog_item_id = $2
		 ORDER BY valid_from, id LIMIT $3 OFFSET $4`,
		wsID, itemID, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "daftar harga gagal dibaca")
		return
	}
	defer rows.Close()
	items := []priceDTO{}
	for rows.Next() {
		var p priceRow
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.CatalogItemID, &p.PriceListID, &p.ClientRequestID,
			&p.ValidFrom, &p.ValidTo, &p.RecordedAt, &p.AmountMinor, &p.Currency); err != nil {
			writeError(w, http.StatusInternalServerError, "daftar harga gagal dibaca")
			return
		}
		items = append(items, priceToDTO(p))
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "daftar harga gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

type quoteDTO struct {
	DraftID        string `json:"draft_id"`
	CatalogItemID  string `json:"catalog_item_id"`
	PriceVersionID string `json:"price_version_id"`
	ValidFrom      string `json:"valid_from"`
	ValidTo        string `json:"valid_to"`
	RecordedAt     string `json:"recorded_at"`
	AmountMinor    string `json:"amount_minor"`
	Currency       string `json:"currency"`
	Unit           string `json:"unit"`
	Units          int64  `json:"units"`
	TotalMinor     string `json:"total_minor"`
	StartsAt       string `json:"starts_at"`
	EndsAt         string `json:"ends_at"`
}

func (s *server) handleGetRentalQuote(w http.ResponseWriter, r *http.Request) {
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
	// Quote adalah bacaan histori/core: hanya membership +
	// view_workspace, tanpa lock, tanpa tulis/audit, tanpa syarat
	// instalasi/entitlement Rental. Quote TIDAK mengotorisasi
	// penyewaan dan TIDAK mengubah status apa pun.
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
	// berbeda dari "tidak ada harga efektif".
	var draftItemID string
	var draftStarts, draftEnds time.Time
	err = tx.QueryRow(
		`SELECT catalog_item_id, starts_at, ends_at FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		wsID, draftID).Scan(&draftItemID, &draftStarts, &draftEnds)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	// Invarian draft tersimpan harus berada dalam rentang yang
	// didukung sebelum aritmetika: pelanggaran adalah galat backend
	// terkendali (500), bukan nilai yang dibungkus/dipotong.
	starts := draftStarts.UTC()
	ends := draftEnds.UTC()
	if starts.Year() < 1 || starts.Year() > 9999 || ends.Year() < 1 || ends.Year() > 9999 || !ends.After(starts) {
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	// Versi efektif adalah tepat satu baris yang memenuhi
	// valid_from <= starts_at < valid_to untuk item + daftar
	// 'standard' ini. Kedua bacaan (draft, lalu harga) berjalan
	// dalam satu transaksi beginTenant yang sama; EXCLUDE menjamin
	// paling banyak satu baris cocok, jadi hasil ini koheren tanpa
	// mengklaim READ COMMITTED sebagai snapshot selebar transaksi.
	rows, err := tx.Query(
		`SELECT `+priceColumns+`
		 FROM price_versions WHERE workspace_id = $1 AND catalog_item_id = $2 AND price_list_id = 'standard'
		   AND valid_from <= $3 AND $3 < valid_to`,
		wsID, draftItemID, starts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	defer rows.Close()
	var versions []priceRow
	for rows.Next() {
		var p priceRow
		if err := rows.Scan(&p.ID, &p.WorkspaceID, &p.CatalogItemID, &p.PriceListID, &p.ClientRequestID,
			&p.ValidFrom, &p.ValidTo, &p.RecordedAt, &p.AmountMinor, &p.Currency); err != nil {
			writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
			return
		}
		versions = append(versions, p)
	}
	if err := rows.Err(); err != nil {
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	if len(versions) == 0 {
		writeError(w, http.StatusNotFound, "harga efektif tidak ditemukan")
		return
	}
	if len(versions) > 1 {
		// EXCLUDE seharusnya mencegah ini; dua baris berarti
		// invarian database rusak.
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	price := versions[0]
	units, ok := ceilUnits24h(starts, ends)
	if !ok {
		writeError(w, http.StatusInternalServerError, "quote gagal dihitung")
		return
	}
	// Total di luar batas int64 adalah 422: permintaan valid yang
	// tidak dapat dikutip dalam batas moneter yang didukung, bukan
	// outage backend. Galat DB/izin yang tak terduga tetap 500.
	total, ok := checkedTotalMinor(price.AmountMinor, units)
	if !ok {
		writeError(w, http.StatusUnprocessableEntity, "total di luar batas yang dapat diwakili")
		return
	}
	writeJSON(w, http.StatusOK, quoteDTO{
		DraftID: draftID, CatalogItemID: draftItemID, PriceVersionID: price.ID,
		ValidFrom: formatRentalTimeUTC(price.ValidFrom), ValidTo: formatRentalTimeUTC(price.ValidTo),
		RecordedAt:  formatRentalTimeUTC(price.RecordedAt),
		AmountMinor: strconv.FormatInt(price.AmountMinor, 10), Currency: price.Currency,
		Unit: "24h", Units: units, TotalMinor: strconv.FormatInt(total, 10),
		StartsAt: formatRentalTimeUTC(starts), EndsAt: formatRentalTimeUTC(ends),
	})
}
