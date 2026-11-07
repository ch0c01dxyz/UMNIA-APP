package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

// Custody serah terima + pengembalian F2-01-F (migrasi 010): satu
// baris rental_custody per draft Rental, untuk satu unit serial yang
// diidentifikasi aset + hold-nya. Aktor adalah staf (owner/manager),
// BUKAN pelanggan. Custody adalah catatan in-app — BUKAN consent/
// tanda tangan pelanggan, BUKAN perjanjian hukum, BUKAN invoice/
// pembayaran, BUKAN posting ledger, dan BUKAN jumlah yang disepakati
// baru. Custody tidak memutasi draft, item, harga, atau fakta harga;
// yang berubah hanyalah status hold yang dipakai (held -> consumed).
// Hold TTL bukan pengembalian fisik; periode berakhir bukan unit
// kembali. Selama custody out ada, unit keluar apa pun periode/TTL.

type custodyRow struct {
	ID, WorkspaceID, RentalContractID, RentalAgreedPriceID, AssetID, HoldID, ClientRequestID string
	ReturnClientRequestID                                                                    sql.NullString
	Status                                                                                   string
	HandedOutAt                                                                              time.Time
	ReturnedAt                                                                               sql.NullTime
	HandedOutBy                                                                              string
	ReturnedBy                                                                               sql.NullString
}

type custodyDTO struct {
	ID                    string  `json:"id"`
	WorkspaceID           string  `json:"workspace_id"`
	RentalContractID      string  `json:"rental_contract_id"`
	RentalAgreedPriceID   string  `json:"rental_agreed_price_id"`
	AssetID               string  `json:"asset_id"`
	HoldID                string  `json:"hold_id"`
	ClientRequestID       string  `json:"client_request_id"`
	ReturnClientRequestID *string `json:"return_client_request_id"`
	Status                string  `json:"status"`
	HandedOutAt           string  `json:"handed_out_at"`
	ReturnedAt            *string `json:"returned_at"`
	HandedOutBy           string  `json:"handed_out_by"`
	ReturnedBy            *string `json:"returned_by"`
}

const custodyColumns = `id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, return_client_request_id, status, handed_out_at, returned_at, handed_out_by, returned_by`

func scanCustodyRow(row *sql.Row) (custodyRow, error) {
	var c custodyRow
	err := row.Scan(&c.ID, &c.WorkspaceID, &c.RentalContractID, &c.RentalAgreedPriceID, &c.AssetID,
		&c.HoldID, &c.ClientRequestID, &c.ReturnClientRequestID, &c.Status, &c.HandedOutAt,
		&c.ReturnedAt, &c.HandedOutBy, &c.ReturnedBy)
	return c, err
}

func custodyToDTO(c custodyRow) custodyDTO {
	d := custodyDTO{
		ID: c.ID, WorkspaceID: c.WorkspaceID, RentalContractID: c.RentalContractID,
		RentalAgreedPriceID: c.RentalAgreedPriceID, AssetID: c.AssetID, HoldID: c.HoldID,
		ClientRequestID: c.ClientRequestID, Status: c.Status,
		HandedOutAt: formatRentalTimeUTC(c.HandedOutAt), HandedOutBy: c.HandedOutBy,
	}
	if c.ReturnClientRequestID.Valid {
		s := c.ReturnClientRequestID.String
		d.ReturnClientRequestID = &s
	}
	if c.ReturnedAt.Valid {
		s := formatRentalTimeUTC(c.ReturnedAt.Time)
		d.ReturnedAt = &s
	}
	if c.ReturnedBy.Valid {
		s := c.ReturnedBy.String
		d.ReturnedBy = &s
	}
	return d
}

// handleHandoverRental menyerahkan satu unit fisik untuk satu draft:
// INSERT custody + konsumsi hold + audit dalam SATU transaksi.
// Urutan guard beku: hak aksi Rental penuh (manage_business_data +
// instalasi aktif + entitlement versi eksak + view_workspace) dicek
// SEBELUM replay, supaya revoke/deactivate memblokir replay 403.
// Replay kunci yang sama mengembalikan catatan SAAT INI (200) tanpa
// tulis/audit — bahkan bila custody sudah dikembalikan sesudahnya —
// dan hold consumed TIDAK divalidasi ulang untuk replay yang valid.
func (s *server) handleHandoverRental(w http.ResponseWriter, r *http.Request) {
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
	// Header: tepat satu nilai Idempotency-Key yang valid (aturan
	// yang sama dengan draft/hold/harga); kunci mentah tidak pernah
	// di-log — hanya dipakai sebagai client_request_id.
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	var body struct {
		AssetID string `json:"asset_id"`
		HoldID  string `json:"hold_id"`
	}
	if err := decodeStrictJSON(w, r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	assetID := strings.TrimSpace(body.AssetID)
	if !isValidUUID(assetID) {
		writeError(w, http.StatusBadRequest, "asset_id tidak valid")
		return
	}
	assetID = strings.ToLower(assetID)
	holdID := strings.TrimSpace(body.HoldID)
	if !isValidUUID(holdID) {
		writeError(w, http.StatusBadRequest, "hold_id tidak valid")
		return
	}
	holdID = strings.ToLower(holdID)
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
	// Cutoff diambil SESUDAH lock: permintaan yang menunggu lock
	// melewati batas jendela harus memakai jam sesudah lock didapat.
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Guard: instalasi rental.basic harus aktif; entitlement dicek
	// ulang tiap request untuk versi aktif yang tepat.
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
	// Izin baca core dicek eksplisit SEBELUM payload draft maupun
	// referensi diakses: sukses bacaan bukan bukti otorisasi.
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	// Replay: kunci yang sama dengan draft + aset + hold yang sama
	// → 200 catatan SAAT INI, tanpa tulis/audit dan tanpa validasi
	// ulang hold yang sudah consumed; beda identitas apa pun → 409.
	existing, err := scanCustodyRow(tx.QueryRow(
		`SELECT `+custodyColumns+`
		 FROM rental_custody WHERE workspace_id = $1 AND client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if existing.RentalContractID == draftID && existing.AssetID == assetID && existing.HoldID == holdID {
			writeJSON(w, http.StatusOK, custodyToDTO(existing))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Draft ini sudah punya custody (kunci lain) → 409, tanpa tulis.
	var existingID string
	err = tx.QueryRow(
		`SELECT id FROM rental_custody WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID).Scan(&existingID)
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		writeError(w, http.StatusConflict, "draft ini sudah memiliki custody")
		return
	}
	// Rujukan untuk handover BARU: draft harus ada di workspace ini;
	// hilang/lintas-tenant → 404 dengan pesan yang sama.
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
	// Invarian tersimpan yang tidak bisa dibuat lewat API (tahun
	// 1-9999, ends > starts) — rusak berarti inkonsistensi database.
	if draftStarts.Year() < 1 || draftStarts.Year() > 9999 || draftEnds.Year() < 1 || draftEnds.Year() > 9999 || !draftEnds.After(draftStarts) {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Fakta harga untuk draft yang sama wajib ada; hilang → 409
	// (bukan 404: draft ada, hanya belum ada fakta harganya).
	fact, err := scanAgreedPriceRow(tx.QueryRow(
		`SELECT `+agreedPriceColumns+` FROM rental_agreed_prices WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusConflict, "harga yang disepakati belum dicatat")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Rantai fakta harus cocok dengan draft tersimpan (pelanggan,
	// item, periode); beda → 409.
	if fact.CustomerID != draftCustomerID || fact.CatalogItemID != draftItemID ||
		!fact.StartsAt.Equal(draftStarts) || !fact.EndsAt.Equal(draftEnds) {
		writeError(w, http.StatusConflict, "harga yang disepakati tidak cocok dengan draft")
		return
	}
	// Hold harus ada di workspace ini; hilang/lintas-tenant → 404.
	hold, err := scanHoldRow(tx.QueryRow(
		`SELECT `+holdColumns+`
		 FROM rental_holds WHERE workspace_id = $1 AND id = $2`,
		wsID, holdID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "hold tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Aset harus ada di workspace ini; hilang/lintas-tenant → 404.
	var assetItemID string
	err = tx.QueryRow(
		`SELECT catalog_item_id FROM physical_assets WHERE workspace_id = $1 AND id = $2`,
		wsID, assetID).Scan(&assetItemID)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "aset tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Hold harus milik draft ini DAN aset yang diminta; beda → 409.
	if hold.DraftID != draftID || hold.AssetID != assetID {
		writeError(w, http.StatusConflict, "hold tidak cocok dengan draft atau aset")
		return
	}
	// Item aset harus sama dengan item draft; beda → 409.
	if assetItemID != draftItemID {
		writeError(w, http.StatusConflict, "aset tidak cocok dengan draft")
		return
	}
	// Status item SAAT INI dibaca ulang untuk handover baru: item
	// yang diarsipkan sesudah draft dibuat → 409. FK komposit
	// menjamin baris item ada; hilang di sini adalah inkonsistensi
	// database → 500.
	var itemStatus string
	err = tx.QueryRow(
		`SELECT status FROM catalog_items WHERE workspace_id = $1 AND id = $2`,
		wsID, draftItemID).Scan(&itemStatus)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if itemStatus != "active" {
		writeError(w, http.StatusConflict, "barang tidak aktif")
		return
	}
	// Hold harus hidup pada cutoff: status mentah 'held' DAN
	// expires_at sesudah cutoff. Hold released/expired/consumed
	// → 409; status efektif tidak pernah dihitung dari jam klien.
	if hold.Status != "held" || !hold.ExpiresAt.After(cutoff) {
		writeError(w, http.StatusConflict, "hold tidak aktif")
		return
	}
	// Jendela serah terima ketat: cutoff harus di dalam periode
	// draft yang direncanakan [starts_at, ends_at). Tidak ada grace
	// window awal maupun akhir; serah terima dini atau telat → 409.
	if cutoff.Before(draftStarts) {
		writeError(w, http.StatusConflict, "periode draft belum dimulai")
		return
	}
	if !cutoff.Before(draftEnds) {
		writeError(w, http.StatusConflict, "periode draft sudah berakhir")
		return
	}
	// Unit tidak boleh sudah keluar (custody out terbuka untuk aset
	// yang sama, apa pun draft/periode). Partial UNIQUE adalah
	// backstop independen bila dua penulis lolos cek ini bersamaan.
	var assetOut bool
	err = tx.QueryRow(
		`SELECT EXISTS(SELECT 1 FROM rental_custody WHERE workspace_id = $1 AND asset_id = $2 AND status = 'out')`,
		wsID, assetID).Scan(&assetOut)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if assetOut {
		writeError(w, http.StatusConflict, "unit sedang dipinjam")
		return
	}
	// Simpan custody + konsumsi hold + audit dalam SATU transaksi;
	// 201 hanya sesudah commit. handed_out_at = cutoff pasca-lock;
	// handed_out_by = subjek staf yang menyerahkan (bukan pelanggan).
	_, err = tx.Exec(
		`INSERT INTO rental_custody
			(id, workspace_id, rental_contract_id, rental_agreed_price_id, asset_id, hold_id, client_request_id, status, handed_out_at, handed_out_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, 'out', $8, $9)`,
		id, wsID, draftID, fact.ID, assetID, holdID, key, cutoff, subject)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			switch pgErr.ConstraintName {
			case "rental_custody_workspace_id_rental_contract_id_key":
				writeError(w, http.StatusConflict, "draft ini sudah memiliki custody")
				return
			case "rental_custody_workspace_id_client_request_id_key":
				writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
				return
			case "rental_custody_one_out_per_asset":
				writeError(w, http.StatusConflict, "unit sedang dipinjam")
				return
			}
		}
		writeError(w, http.StatusInternalServerError, "serah terima gagal dicatat")
		return
	}
	// Konsumsi hold secara kondisional (held -> consumed) dengan
	// updated_at=cutoff; 0 baris berarti hold berubah di antara
	// baca dan UPDATE — rollback BERSAMA custody/audit.
	res, err := tx.Exec(
		`UPDATE rental_holds SET status = 'consumed', updated_at = $2
		 WHERE workspace_id = $1 AND id = $3 AND status = 'held'`,
		wsID, cutoff, holdID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	details, _ := json.Marshal(map[string]string{"rental_contract_id": draftID, "asset_id": assetID})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_handed_over", "rental_custody", id, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	writeJSON(w, http.StatusCreated, custodyToDTO(custodyRow{
		ID: id, WorkspaceID: wsID, RentalContractID: draftID, RentalAgreedPriceID: fact.ID,
		AssetID: assetID, HoldID: holdID, ClientRequestID: key, Status: "out",
		HandedOutAt: cutoff, HandedOutBy: subject,
	}))
}

// handleReturnRental menerima kembali unit yang sudah keluar: SATU
// jalur menyempit SELALU — manage_business_data + view_workspace +
// workspace lock, TANPA gate instalasi/entitlement Rental, supaya
// owner/manager yang sah tetap bisa menerima unit sesudah
// revoke/deactivate. Return hanya menutup custody out yang ada;
// tidak bisa mengalokasikan apa pun, tidak menghitung ulang harga,
// tidak memperbarui hold, tidak memungut biaya.
func (s *server) handleReturnRental(w http.ResponseWriter, r *http.Request) {
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
	// Header: tepat satu nilai Idempotency-Key yang valid; keyspace
	// return TERPISAH dari keyspace handover (kolom berbeda).
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) != 1 || !validIdempotencyKey(keys[0]) {
		writeError(w, http.StatusBadRequest, "header Idempotency-Key tidak valid")
		return
	}
	key := keys[0]
	// Body: kosong atau strict {} saja — tanpa timestamp/aktor/
	// status/biaya dari klien. Body kosong membuat Decode pertama
	// mengembalikan io.EOF, jadi baca byte dulu seperti pola strict.
	bodyBytes, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMasterDataBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "body tidak valid")
		return
	}
	if len(bytes.TrimSpace(bodyBytes)) > 0 {
		var empty struct{}
		dec := json.NewDecoder(bytes.NewReader(bodyBytes))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&empty); err != nil {
			writeError(w, http.StatusBadRequest, "body tidak valid")
			return
		}
		var extra any
		if err := dec.Decode(&extra); err != io.EOF {
			writeError(w, http.StatusBadRequest, "body tidak valid")
			return
		}
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
	// Izin baca dicek eksplisit SEBELUM lock (urutan release hold);
	// return TIDAK mensyaratkan instalasi/entitlement Rental aktif.
	allowed, err = s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "tidak punya izin membaca data core")
		return
	}
	if err := lockWorkspace(tx, wsID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	cutoff, err := captureCutoff(tx)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Replay kunci return: lookup workspace-scoped SEBELUM transisi.
	// Kunci milik draft path yang sama → 200 catatan itu, tanpa
	// tulis/audit/perubahan timestamp; milik draft lain → 409.
	replayed, err := scanCustodyRow(tx.QueryRow(
		`SELECT `+custodyColumns+`
		 FROM rental_custody WHERE workspace_id = $1 AND return_client_request_id = $2`,
		wsID, key))
	if err != nil && err != sql.ErrNoRows {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	if err == nil {
		if replayed.RentalContractID == draftID {
			writeJSON(w, http.StatusOK, custodyToDTO(replayed))
			return
		}
		writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
		return
	}
	// Custody draft ini harus ada; hilang/lintas-tenant → 404.
	custody, err := scanCustodyRow(tx.QueryRow(
		`SELECT `+custodyColumns+`
		 FROM rental_custody WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "custody tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// Sudah dikembalikan (dengan kunci lain — kunci yang sama sudah
	// ditangkap replay di atas) → 409.
	if custody.Status == "returned" {
		writeError(w, http.StatusConflict, "draft ini sudah dikembalikan")
		return
	}
	// Transisi kondisional out -> returned + aktor/waktu/kunci
	// return. Tanpa SELECT FOR UPDATE: kebijakan UPDATE (USING
	// status='out') akan menyaring baris returned — lock workspace +
	// UPDATE kondisional mendukung replay idempotent. 0 baris
	// berarti penulis lain menutupnya bersamaan: baca ulang dan
	// jawab dari keadaan saat ini.
	res, err := tx.Exec(
		`UPDATE rental_custody SET status = 'returned', returned_at = $2, returned_by = $3, return_client_request_id = $4
		 WHERE workspace_id = $1 AND rental_contract_id = $5 AND status = 'out'`,
		wsID, cutoff, subject, key, draftID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "rental_custody_workspace_id_return_client_request_id_key" {
			writeError(w, http.StatusConflict, "kunci idempotency sudah dipakai untuk permintaan yang berbeda")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		current, err := scanCustodyRow(tx.QueryRow(
			`SELECT `+custodyColumns+`
			 FROM rental_custody WHERE workspace_id = $1 AND rental_contract_id = $2`,
			wsID, draftID))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "transaksi gagal")
			return
		}
		if current.Status == "returned" {
			if current.ReturnClientRequestID.Valid && current.ReturnClientRequestID.String == key {
				writeJSON(w, http.StatusOK, custodyToDTO(current))
				return
			}
			writeError(w, http.StatusConflict, "draft ini sudah dikembalikan")
			return
		}
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	details, _ := json.Marshal(map[string]string{"rental_contract_id": draftID, "asset_id": custody.AssetID})
	if err := writeBusinessAudit(tx, wsID, subject, "rental_returned", "rental_custody", custody.ID, string(details)); err != nil {
		writeError(w, http.StatusInternalServerError, "audit gagal ditulis")
		return
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, "transaksi gagal")
		return
	}
	// DTO dibangun dari baris yang sudah dibaca + kolom return yang
	// baru ditulis: baca ulang lewat s.db tanpa konteks tenant akan
	// disaring RLS FORCE (0 baris), jadi tidak ada query sesudah
	// commit di sini.
	custody.Status = "returned"
	custody.ReturnedAt = sql.NullTime{Time: cutoff, Valid: true}
	custody.ReturnedBy = sql.NullString{String: subject, Valid: true}
	custody.ReturnClientRequestID = sql.NullString{String: key, Valid: true}
	writeJSON(w, http.StatusOK, custodyToDTO(custody))
}

// handleGetCustody membaca histori custody satu draft: hanya
// membership + view_workspace, tanpa lock, tanpa tulis/audit, tanpa
// syarat instalasi/entitlement Rental. Draft hilang/lintas-tenant →
// 404 yang berbeda dari "draft ada tapi belum ada custody".
func (s *server) handleGetCustody(w http.ResponseWriter, r *http.Request) {
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
	allowed, err := s.hasPermission(tx, wsID, subject, "view_workspace")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "izin gagal diperiksa")
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "bukan anggota workspace ini")
		return
	}
	var exists string
	err = tx.QueryRow(
		`SELECT id FROM rental_contracts WHERE workspace_id = $1 AND id = $2`,
		wsID, draftID).Scan(&exists)
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "draft tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "custody gagal dibaca")
		return
	}
	row, err := scanCustodyRow(tx.QueryRow(
		`SELECT `+custodyColumns+`
		 FROM rental_custody WHERE workspace_id = $1 AND rental_contract_id = $2`,
		wsID, draftID))
	if err == sql.ErrNoRows {
		writeError(w, http.StatusNotFound, "custody tidak ditemukan")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "custody gagal dibaca")
		return
	}
	writeJSON(w, http.StatusOK, custodyToDTO(row))
}
